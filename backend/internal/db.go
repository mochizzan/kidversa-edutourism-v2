package internal

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	// bootDBAttempts × bootDBRetryDelay bounds the 02 §5 "boot retry loop"
	// before the process fails loud.
	bootDBAttempts   = 30
	bootDBRetryDelay = 2 * time.Second
	migrateLockName  = "kidversa_migrate"
	migrateLockWaitS = 30
)

// dsn builds the 01 §3 data source name; multiStatements is enabled only for
// the short-lived migrator connection (runtime queries never allow stacking).
func dsn(cfg Config, multiStatements bool) string {
	d := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&charset=utf8mb4&loc=Local",
		cfg.DBUser, cfg.DBPassword, cfg.DBHost, cfg.DBPort, cfg.DBName)
	if multiStatements {
		d += "&multiStatements=true"
	}
	return d
}

// OpenDB opens the pool with the locked settings (10 open / 10 idle / 3 min
// lifetime) and retries boot per 02 §5 until MariaDB answers, then fails loud.
func OpenDB(cfg Config) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn(cfg, false))
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(3 * time.Minute)

	var lastErr error
	for i := range bootDBAttempts {
		pingCtx, cancel := context.WithTimeout(context.Background(), bootDBRetryDelay)
		lastErr = db.PingContext(pingCtx)
		cancel()
		if lastErr == nil {
			return db, nil
		}
		if i < bootDBAttempts-1 {
			slog.Warn("db not ready, retrying", "err", lastErr, "retry_in", bootDBRetryDelay)
			time.Sleep(bootDBRetryDelay)
		}
	}
	db.Close()
	return nil, fmt.Errorf("db unreachable after %d attempts: %w", bootDBAttempts, lastErr)
}

// Migrate applies migrations/NNNN_*.sql in order under the MySQL advisory lock
// GET_LOCK('kidversa_migrate', 30). Lock timeout/failure refuses the boot; an
// applied version is recorded in migrations(version, applied_at).
func Migrate(cfg Config) error {
	mdb, err := sql.Open("mysql", dsn(cfg, true))
	if err != nil {
		return fmt.Errorf("open migrator: %w", err)
	}
	defer mdb.Close()

	ctx := context.Background()
	conn, err := mdb.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrator conn: %w", err)
	}
	defer conn.Close()

	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx,
		"SELECT GET_LOCK(?, ?)", migrateLockName, migrateLockWaitS).Scan(&got); err != nil {
		return fmt.Errorf("acquire %s: %w", migrateLockName, err)
	}
	if !got.Valid || got.Int64 != 1 {
		return fmt.Errorf("migrate lock %q not acquired within %d s — refusing to start", migrateLockName, migrateLockWaitS)
	}
	defer func() {
		if _, rErr := conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", migrateLockName); rErr != nil {
			slog.Error("release migrate lock", "err", rErr)
		}
	}()

	if _, err := conn.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS migrations (
			version INT NOT NULL PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	applied := make(map[int]bool)
	rows, err := conn.QueryContext(ctx, "SELECT version FROM migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("scan migrations: %w", err)
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate migrations: %w", err)
	}

	entries, err := os.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	type migration struct {
		version int
		file    string
	}
	var files []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		sep := strings.IndexByte(e.Name(), '_')
		if sep < 1 {
			return fmt.Errorf("migration %q must be named NNNN_name.sql", e.Name())
		}
		v, convErr := strconv.Atoi(e.Name()[:sep])
		if convErr != nil || v < 1 {
			return fmt.Errorf("migration %q has an invalid version prefix", e.Name())
		}
		files = append(files, migration{version: v, file: e.Name()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })

	for _, m := range files {
		if applied[m.version] {
			continue
		}
		body, rErr := os.ReadFile(filepath.Join("migrations", m.file))
		if rErr != nil {
			return fmt.Errorf("read %s: %w", m.file, rErr)
		}
		if _, eErr := conn.ExecContext(ctx, string(body)); eErr != nil {
			return fmt.Errorf("apply %s: %w", m.file, eErr)
		}
		if _, iErr := conn.ExecContext(ctx, "INSERT INTO migrations (version) VALUES (?)", m.version); iErr != nil {
			return fmt.Errorf("record %s: %w", m.file, iErr)
		}
		slog.Info("migration applied", "file", m.file, "version", m.version)
	}
	return nil
}

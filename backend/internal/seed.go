package internal

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const seedDir = "seeds"

// Seed applies every seeds/NNNN_*.sql file verbatim, in ascending filename
// order, on every boot after Migrate. Each file is a single idempotent
// statement, so no version table is needed and the runtime connection (which
// disables multiStatements) can execute the file body as-is.
func Seed(db *sql.DB) error {
	entries, err := os.ReadDir(seedDir)
	if err != nil {
		return fmt.Errorf("read seeds dir: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, name := range names {
		body, rErr := os.ReadFile(filepath.Join(seedDir, name))
		if rErr != nil {
			return fmt.Errorf("read %s: %w", name, rErr)
		}
		if _, eErr := db.ExecContext(ctx, string(body)); eErr != nil {
			return fmt.Errorf("apply %s: %w", name, eErr)
		}
		slog.Info("seed applied", "file", name)
	}
	return nil
}

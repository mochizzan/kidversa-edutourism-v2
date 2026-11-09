package internal

// infra_extra_test.go — coverage for the infrastructure slice: db.go (dsn,
// OpenDB, Migrate, Seed errors), mail.go (reset mail + body builder), wa.go
// (sendText, status probe, handleWaStatus, watcher), ai.go (prompt rendering
// + Gemini response parsing) and pscope.go (the shared P-route prologue).
//
// Migrate/OpenDB open their OWN *sql.DB (no injection point for sqlmock), so
// they are driven against a scripted in-process MySQL wire-protocol fake
// listening on 127.0.0.1. sendResetMail dials SMTP directly, so it runs
// against a scripted in-process SMTP server. generateNarrative posts to a
// fixed Gemini URL, so its http.Client transport is swapped for a canned
// RoundTripper on the per-test Server instance. Everything stays local; no
// goroutine outlives its test.

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// db.go — dsn (pure)
// ---------------------------------------------------------------------------

// TestInxDSN pins the exact data source name dsn() builds, with and without
// the migrator-only multiStatements flag (01 §3).
func TestInxDSN(t *testing.T) {
	cfg := Config{
		DBUser: "app", DBPassword: "s3cr3t", DBHost: "db.internal",
		DBPort: "3306", DBName: "kidversa",
	}
	base := "app:s3cr3t@tcp(db.internal:3306)/kidversa?parseTime=true&charset=utf8mb4&loc=Local"

	require.Equal(t, base, dsn(cfg, false),
		"runtime DSN must pin parseTime/utf8mb4/loc=Local verbatim (01 §3) and never multiStatements")
	require.Equal(t, base+"&multiStatements=true", dsn(cfg, true),
		"migrator DSN must append exactly one multiStatements=true")

	empty := Config{DBHost: "h", DBPort: "1", DBName: "n"}
	require.Equal(t, ":@tcp(h:1)/n?parseTime=true&charset=utf8mb4&loc=Local",
		dsn(empty, false), "empty credentials must still produce a parseable DSN")

	// dsn() interpolates credentials verbatim (escaping is ParseDSN's job).
	special := Config{DBUser: "u", DBPassword: "p@ss/w@rd", DBHost: "h", DBPort: "3306", DBName: "d"}
	require.Equal(t, "u:p@ss/w@rd@tcp(h:3306)/d?parseTime=true&charset=utf8mb4&loc=Local",
		dsn(special, false), "password characters must be embedded verbatim")

	oddPort := Config{DBHost: "h", DBPort: "not-a-port", DBName: "d"}
	require.Equal(t, ":@tcp(h:not-a-port)/d?parseTime=true&charset=utf8mb4&loc=Local&multiStatements=true",
		dsn(oddPort, true), "port is passed through untouched; only multiStatements toggles")
}

// ---------------------------------------------------------------------------
// db.go — OpenDB
// ---------------------------------------------------------------------------

// TestInxOpenDBRejectsUnparsableDSN: sql.Open parses eagerly (DriverContext),
// so a DSN ParseDSN rejects must surface as the wrapped "open db" error
// without any dial attempt.
func TestInxOpenDBRejectsUnparsableDSN(t *testing.T) {
	cfg := Config{DBUser: "root", DBHost: "127.0.0.1", DBPort: "3306", DBName: "x%zz"}
	db, err := OpenDB(cfg)
	require.Nil(t, db)
	require.ErrorContains(t, err, "open db:")
	require.ErrorContains(t, err, `invalid dbname "x%zz"`,
		"the %zz escape must be reported by ParseDSN")
}

// TestInxOpenDBPingsAndLocksPool: a healthy server answers the boot ping;
// OpenDB returns the pool with the locked 10-open setting and issues COM_PING
// (observable in the fake's log).
func TestInxOpenDBPingsAndLocksPool(t *testing.T) {
	srv := InxStartMySQL(t)
	cfg := InxMySQLCfg(t, srv)

	db, err := OpenDB(cfg)
	require.NoError(t, err, "a ready server must satisfy the first boot attempt")
	require.NotNil(t, db)
	require.Equal(t, 10, db.Stats().MaxOpenConnections,
		"boot must lock the pool at 10 open connections (02 §5)")
	require.NoError(t, db.Close())
	require.Contains(t, srv.Log(), "<PING>", "OpenDB must ping before returning")
	srv.RequireDone(t)
}

// ---------------------------------------------------------------------------
// db.go — Migrate (driven against the fake MySQL server)
// ---------------------------------------------------------------------------

// chdirInx switches the working directory for one test (Migrate reads
// ./migrations relative to the backend root; the failure-path tests craft
// their own directory) and restores it afterwards.
func chdirInx(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(old))
	})
}

// captureInxLog runs fn while capturing the default slog output.
func captureInxLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	fn()
	return buf.String()
}

func TestInxMigrateAppliesPendingMigrationAndRecordsVersion(t *testing.T) {
	srv := InxStartMySQL(t,
		InxMySQLStep{Match: "GET_LOCK", Resp: InxMyTable([]string{"l"}, []*string{new("1")})},
		InxMySQLStep{Match: "CREATE TABLE IF NOT EXISTS migrations"},
		InxMySQLStep{Match: "SELECT version FROM migrations", Resp: InxMyTable([]string{"version"})},
		InxMySQLStep{Match: "CREATE TABLE IF NOT EXISTS tenants"},
		InxMySQLStep{Match: "INSERT INTO migrations"},
		InxMySQLStep{Match: "RELEASE_LOCK", Resp: InxMyTable([]string{"r"}, []*string{new("1")})},
	)

	require.NoError(t, Migrate(InxMySQLCfg(t, srv)),
		"a pending migration must apply cleanly")
	srv.RequireDone(t)

	body, err := os.ReadFile(filepath.Join("migrations", "0001_init.sql"))
	require.NoError(t, err)
	require.Contains(t, srv.Log(), string(body),
		"the migration file body must be executed verbatim over COM_QUERY")
	require.Contains(t, srv.Log(), "INSERT INTO migrations (version) VALUES (?)",
		"the applied version must be recorded")
}

func TestInxMigrateSkipsAlreadyAppliedVersion(t *testing.T) {
	srv := InxStartMySQL(t,
		InxMySQLStep{Match: "GET_LOCK", Resp: InxMyTable([]string{"l"}, []*string{new("1")})},
		InxMySQLStep{Match: "CREATE TABLE IF NOT EXISTS migrations"},
		InxMySQLStep{Match: "SELECT version FROM migrations",
			Resp: InxMyTable([]string{"version"}, []*string{new("1")})},
		InxMySQLStep{Match: "RELEASE_LOCK", Resp: InxMyTable([]string{"r"}, []*string{new("1")})},
	)

	require.NoError(t, Migrate(InxMySQLCfg(t, srv)))
	srv.RequireDone(t)
	require.NotContains(t, srv.Log(), "CREATE TABLE IF NOT EXISTS tenants",
		"an applied version must never be re-executed")
	require.NotContains(t, srv.Log(), "INSERT INTO migrations",
		"an applied version must not be recorded twice")
}

func TestInxMigrateLockFailures(t *testing.T) {
	lockNotAcquired := `migrate lock "kidversa_migrate" not acquired within 30 s — refusing to start`

	t.Run("GET_LOCK query error refuses the boot", func(t *testing.T) {
		srv := InxStartMySQL(t, InxMySQLStep{
			Match: "GET_LOCK", Resp: InxMyErr(1064, "lock boom"),
		})
		err := Migrate(InxMySQLCfg(t, srv))
		require.ErrorContains(t, err, "acquire kidversa_migrate:")
		var mq *mysql.MySQLError
		require.ErrorAs(t, err, &mq)
		require.Equal(t, uint16(1064), mq.Number, "server error must be preserved through the wrap")
		srv.RequireDone(t)
	})

	t.Run("GET_LOCK NULL refuses the boot", func(t *testing.T) {
		srv := InxStartMySQL(t, InxMySQLStep{
			Match: "GET_LOCK", Resp: InxMyTable([]string{"l"}, []*string{nil}),
		})
		require.EqualError(t, Migrate(InxMySQLCfg(t, srv)), lockNotAcquired)
		srv.RequireDone(t)
	})

	t.Run("GET_LOCK 0 refuses the boot", func(t *testing.T) {
		srv := InxStartMySQL(t, InxMySQLStep{
			Match: "GET_LOCK", Resp: InxMyTable([]string{"l"}, []*string{new("0")}),
		})
		require.EqualError(t, Migrate(InxMySQLCfg(t, srv)), lockNotAcquired)
		srv.RequireDone(t)
	})
}

func TestInxMigrateStatementFailuresReleaseTheLock(t *testing.T) {
	// Every failure AFTER the lock is acquired must still run the deferred
	// RELEASE_LOCK (the release step is consumed by the fake), wrap the exact
	// operation name and keep the MySQL errno. Each case scripts the wire
	// sequence up to the failure plus the release.
	normal := []InxMySQLStep{
		{Match: "GET_LOCK", Resp: InxMyTable([]string{"l"}, []*string{new("1")})},
		{Match: "CREATE TABLE IF NOT EXISTS migrations"},
		{Match: "SELECT version FROM migrations", Resp: InxMyTable([]string{"version"})},
		{Match: "CREATE TABLE IF NOT EXISTS tenants"},
		{Match: "INSERT INTO migrations"},
	}
	cases := []struct {
		name      string
		idx       int // position of the failing step within the normal sequence
		step      InxMySQLStep
		wantWrap  string
		wantErrno uint16
	}{
		{
			name:      "create migrations table",
			idx:       1,
			step:      InxMySQLStep{Match: "CREATE TABLE IF NOT EXISTS migrations", Resp: InxMyErr(1050, "table exists")},
			wantWrap:  "create migrations table:",
			wantErrno: 1050,
		},
		{
			name:      "read applied versions",
			idx:       2,
			step:      InxMySQLStep{Match: "SELECT version FROM migrations", Resp: InxMyErr(1146, "no table")},
			wantWrap:  "read migrations:",
			wantErrno: 1146,
		},
		{
			name: "scan a non-numeric version",
			idx:  2,
			step: InxMySQLStep{Match: "SELECT version FROM migrations",
				Resp: InxMyTable([]string{"version"}, []*string{new("not-a-number")})},
			wantWrap: "scan migrations:",
		},
		{
			name:      "apply the migration file",
			idx:       3,
			step:      InxMySQLStep{Match: "CREATE TABLE IF NOT EXISTS tenants", Resp: InxMyErr(1064, "bad sql")},
			wantWrap:  "apply 0001_init.sql:",
			wantErrno: 1064,
		},
		{
			name:      "record the version",
			idx:       4,
			step:      InxMySQLStep{Match: "INSERT INTO migrations", Resp: InxMyErr(1062, "duplicate")},
			wantWrap:  "record 0001_init.sql:",
			wantErrno: 1062,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps := append(normal[:tc.idx:tc.idx], tc.step)
			steps = append(steps, InxMySQLStep{
				Match: "RELEASE_LOCK", Resp: InxMyTable([]string{"r"}, []*string{new("1")}),
			})

			srv := InxStartMySQL(t, steps...)
			err := Migrate(InxMySQLCfg(t, srv))
			require.ErrorContains(t, err, tc.wantWrap)
			if tc.wantErrno != 0 {
				var mq *mysql.MySQLError
				require.ErrorAs(t, err, &mq)
				require.Equal(t, tc.wantErrno, mq.Number)
			} else {
				require.ErrorContains(t, err, "not-a-number",
					"the scan failure must surface the offending value")
			}
			srv.RequireDone(t, tc.idx+2) // prefix + failure + release
		})
	}
}

// TestInxMigrateIterateFailure: rows.Err() after a mid-stream error packet.
func TestInxMigrateIterateFailure(t *testing.T) {
	srv := InxStartMySQL(t,
		InxMySQLStep{Match: "GET_LOCK", Resp: InxMyTable([]string{"l"}, []*string{new("1")})},
		InxMySQLStep{Match: "CREATE TABLE IF NOT EXISTS migrations"},
		InxMySQLStep{Match: "SELECT version FROM migrations", Resp: func() InxMySQLResp {
			r := InxMyTable([]string{"version"}, []*string{new("1")})
			r.RowErrno = 1146
			r.RowMsg = "version stream died"
			return r
		}()},
		InxMySQLStep{Match: "RELEASE_LOCK", Resp: InxMyTable([]string{"r"}, []*string{new("1")})},
	)
	err := Migrate(InxMySQLCfg(t, srv))
	require.ErrorContains(t, err, "iterate migrations:")
	var mq *mysql.MySQLError
	require.ErrorAs(t, err, &mq)
	require.Equal(t, uint16(1146), mq.Number)
	srv.RequireDone(t, 4)
}

func TestInxMigrateFilesystemFailures(t *testing.T) {
	dbSteps := func() []InxMySQLStep {
		return []InxMySQLStep{
			{Match: "GET_LOCK", Resp: InxMyTable([]string{"l"}, []*string{new("1")})},
			{Match: "CREATE TABLE IF NOT EXISTS migrations"},
			{Match: "SELECT version FROM migrations", Resp: InxMyTable([]string{"version"})},
			{Match: "RELEASE_LOCK", Resp: InxMyTable([]string{"r"}, []*string{new("1")})},
		}
	}

	t.Run("missing migrations dir", func(t *testing.T) {
		srv := InxStartMySQL(t, dbSteps()...)
		chdirInx(t, t.TempDir())
		require.ErrorContains(t, Migrate(InxMySQLCfg(t, srv)), "read migrations dir:")
		srv.RequireDone(t, 4)
	})

	t.Run("file without NNNN_ prefix", func(t *testing.T) {
		srv := InxStartMySQL(t, dbSteps()...)
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "migrations"), 0o755))
		// ReadDir sorts by name, so aaa.txt is visited (and skipped) first —
		// that exercises the non-.sql/directory continue branch.
		require.NoError(t, os.WriteFile(filepath.Join(dir, "migrations", "aaa.txt"), []byte("notes"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "migrations", "bad.sql"), []byte("x"), 0o644))
		chdirInx(t, dir)
		require.EqualError(t, Migrate(InxMySQLCfg(t, srv)),
			`migration "bad.sql" must be named NNNN_name.sql`)
		srv.RequireDone(t, 4)
	})

	t.Run("two pending migrations apply in version order", func(t *testing.T) {
		srv := InxStartMySQL(t,
			InxMySQLStep{Match: "GET_LOCK", Resp: InxMyTable([]string{"l"}, []*string{new("1")})},
			InxMySQLStep{Match: "CREATE TABLE IF NOT EXISTS migrations"},
			InxMySQLStep{Match: "SELECT version FROM migrations", Resp: InxMyTable([]string{"version"})},
			InxMySQLStep{Match: "-- migration 0001 body"},
			InxMySQLStep{Match: "INSERT INTO migrations"},
			InxMySQLStep{Match: "-- migration 0002 body"},
			InxMySQLStep{Match: "INSERT INTO migrations"},
			InxMySQLStep{Match: "RELEASE_LOCK", Resp: InxMyTable([]string{"r"}, []*string{new("1")})},
		)
		dir := t.TempDir()
		mig := filepath.Join(dir, "migrations")
		require.NoError(t, os.MkdirAll(mig, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(mig, "0002_second.sql"),
			[]byte("-- migration 0002 body\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(mig, "0001_first.sql"),
			[]byte("-- migration 0001 body\n"), 0o644))
		chdirInx(t, dir)

		require.NoError(t, Migrate(InxMySQLCfg(t, srv)))
		srv.RequireDone(t, 8) // two files → body+insert each, plus lock/create/select/release

		log := srv.Log()
		idx := func(needle string) int {
			for i, entry := range log {
				if strings.Contains(entry, needle) {
					return i
				}
			}
			return -1
		}
		first, second := idx("-- migration 0001 body"), idx("-- migration 0002 body")
		require.GreaterOrEqual(t, first, 0, "0001 body must be applied")
		require.GreaterOrEqual(t, second, 0, "0002 body must be applied")
		require.Less(t, first, second, "migrations must apply in ascending version order")
	})

	t.Run("listed file unreadable", func(t *testing.T) {
		srv := InxStartMySQL(t, dbSteps()...)
		dir := t.TempDir()
		mig := filepath.Join(dir, "migrations")
		require.NoError(t, os.MkdirAll(mig, 0o755))
		// A dangling symlink is listed by ReadDir (valid NNNN_.sql name) but
		// fails at read time — the "read <file>" branch.
		require.NoError(t, os.Symlink(filepath.Join(dir, "gone.sql"),
			filepath.Join(mig, "0003_link.sql")))
		chdirInx(t, dir)
		require.ErrorContains(t, Migrate(InxMySQLCfg(t, srv)), "read 0003_link.sql:")
		srv.RequireDone(t, 4)
	})

	t.Run("non-numeric version prefix", func(t *testing.T) {
		srv := InxStartMySQL(t, dbSteps()...)
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "migrations"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "migrations", "abc_x.sql"), []byte("x"), 0o644))
		chdirInx(t, dir)
		require.EqualError(t, Migrate(InxMySQLCfg(t, srv)),
			`migration "abc_x.sql" has an invalid version prefix`)
		srv.RequireDone(t, 4)
	})
}

func TestInxMigrateReleaseLockFailureOnlyLogs(t *testing.T) {
	srv := InxStartMySQL(t,
		InxMySQLStep{Match: "GET_LOCK", Resp: InxMyTable([]string{"l"}, []*string{new("1")})},
		InxMySQLStep{Match: "CREATE TABLE IF NOT EXISTS migrations"},
		InxMySQLStep{Match: "SELECT version FROM migrations",
			Resp: InxMyTable([]string{"version"}, []*string{new("1")})},
		InxMySQLStep{Match: "RELEASE_LOCK", Resp: InxMyErr(1105, "release died")},
	)
	out := captureInxLog(t, func() {
		require.NoError(t, Migrate(InxMySQLCfg(t, srv)),
			"a failed RELEASE_LOCK must not fail an otherwise clean Migrate")
	})
	srv.RequireDone(t)
	require.Contains(t, out, "release migrate lock")
	require.Contains(t, out, "release died")
}

func TestInxMigrateConnectionFailures(t *testing.T) {
	t.Run("unparsable DSN", func(t *testing.T) {
		cfg := Config{DBUser: "root", DBHost: "127.0.0.1", DBPort: "3306", DBName: "x%zz"}
		err := Migrate(cfg)
		require.ErrorContains(t, err, "open migrator:")
		require.ErrorContains(t, err, `invalid dbname "x%zz"`)
	})

	t.Run("server unreachable", func(t *testing.T) {
		cfg := Config{DBUser: "root", DBHost: "127.0.0.1", DBPort: "1", DBName: "kidversa_test"}
		err := Migrate(cfg)
		require.ErrorContains(t, err, "migrator conn:",
			"a refused dial must abort before any statement runs")
		require.ErrorContains(t, err, "127.0.0.1:1")
	})
}

// ---------------------------------------------------------------------------
// mail.go — resetMailBody (pure)
// ---------------------------------------------------------------------------

func TestInxResetMailBody(t *testing.T) {
	require.Equal(t,
		"Permintaan atur ulang sandi akun Kidversa.\n\n"+
			"Kode verifikasi: AB12CD34\n"+
			"Berlaku 15 menit dan hanya dapat dipakai satu kali.\n\n"+
			"Jika Anda tidak meminta email ini, abaikan saja.\n",
		resetMailBody("AB12CD34", 15),
		"the relay copy must match the agreed Indonesian copy verbatim")

	require.Equal(t,
		"Permintaan atur ulang sandi akun Kidversa.\n\n"+
			"Kode verifikasi: \n"+
			"Berlaku 0 menit dan hanya dapat dipakai satu kali.\n\n"+
			"Jika Anda tidak meminta email ini, abaikan saja.\n",
		resetMailBody("", 0),
		"empty inputs must not change the template shape")
}

// ---------------------------------------------------------------------------
// mail.go — sendResetMail
// ---------------------------------------------------------------------------

// InxSMTPFake is a scripted SMTP listener: one response per protocol event,
// capturing every command line and the raw DATA payload.
type InxSMTPFake struct {
	ln         net.Listener
	mu         sync.Mutex
	script     []string
	pos        int
	cmds       []string
	msgs       []string
	problems   []string
	conns      []net.Conn
	connWg     sync.WaitGroup
	acceptDone chan struct{}
}

// InxStartSMTP starts a scripted SMTP server on 127.0.0.1. The script holds
// one entry per event: the greeting first, then one response per command
// line; a "354..." response additionally triggers message capture followed
// by the next scripted response (read by dataCloser.Close).
func InxStartSMTP(t *testing.T, script ...string) *InxSMTPFake {
	t.Helper()
	require.NotEmpty(t, script, "script must start with a greeting")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	f := &InxSMTPFake{ln: ln, script: script, acceptDone: make(chan struct{})}
	go func() {
		defer close(f.acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns = append(f.conns, c)
			f.mu.Unlock()
			f.connWg.Add(1)
			go func(c net.Conn) {
				defer f.connWg.Done()
				defer c.Close()
				f.serve(c)
			}(c)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		<-f.acceptDone
		f.mu.Lock()
		for _, c := range f.conns {
			c.Close()
		}
		f.mu.Unlock()
		f.connWg.Wait()
	})
	return f
}

func (f *InxSMTPFake) Port() string {
	return strconv.Itoa(f.ln.Addr().(*net.TCPAddr).Port)
}

func (f *InxSMTPFake) take() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pos >= len(f.script) {
		f.problems = append(f.problems, "script exhausted")
		return "", false
	}
	r := f.script[f.pos]
	f.pos++
	return r, true
}

func (f *InxSMTPFake) violate(msg string) {
	f.mu.Lock()
	f.problems = append(f.problems, msg)
	f.mu.Unlock()
}

func (f *InxSMTPFake) serve(c net.Conn) {
	r := bufio.NewReader(c)
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	greeting, ok := f.take()
	if !ok {
		return
	}
	if err := inxWriteFull(c, []byte(greeting+"\r\n")); err != nil {
		return
	}
	for {
		_ = c.SetDeadline(time.Now().Add(15 * time.Second))
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		f.mu.Lock()
		f.cmds = append(f.cmds, cmd)
		f.mu.Unlock()
		resp, ok := f.take()
		if !ok {
			return
		}
		if err := inxWriteFull(c, []byte(resp+"\r\n")); err != nil {
			return
		}
		if strings.HasPrefix(resp, "354") {
			var raw strings.Builder
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				raw.WriteString(dl)
			}
			f.mu.Lock()
			f.msgs = append(f.msgs, raw.String())
			f.mu.Unlock()
			after, ok := f.take() // the response dataCloser.Close reads
			if !ok {
				return
			}
			if err := inxWriteFull(c, []byte(after+"\r\n")); err != nil {
				return
			}
		}
	}
}

func (f *InxSMTPFake) Cmds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

func (f *InxSMTPFake) Msgs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.msgs...)
}

func (f *InxSMTPFake) RequireDone(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Empty(t, f.problems, "scripted SMTP protocol problems")
	require.Equal(t, len(f.script), f.pos, "every scripted response must be consumed")
}

func inxWriteFull(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

const inxMailFrom = "no-reply@kidversa.id"

// inxMailScript is the happy-path conversation up to and including DATA.
func inxMailScript(extra ...string) []string {
	return append([]string{
		"220 inx.local ESMTP",
		"250-inx.local\r\n250 OK",
		"250 2.1.0 sender ok",
		"250 2.1.5 recipient ok",
		"354 send it",
		"250 2.0.0 queued",
		"221 2.0.0 bye",
	}, extra...)
}

func inxSMTPServer(t *testing.T) *Server {
	t.Helper()
	s, _ := newTestServer(t)
	s.cfg.SMTPHost = "127.0.0.1"
	s.cfg.SMTPFrom = inxMailFrom
	return s
}

func TestInxSendResetMail(t *testing.T) {
	const to = "parent@school.id"
	const code = "AB12CD34"

	t.Run("no SMTP host returns the exact config error", func(t *testing.T) {
		s, _ := newTestServer(t)
		s.cfg.SMTPHost = ""
		require.EqualError(t, s.sendResetMail(to, code), "smtp not configured")
	})

	t.Run("dial failure surfaces the address", func(t *testing.T) {
		s, _ := newTestServer(t)
		s.cfg.SMTPHost = "127.0.0.1"
		s.cfg.SMTPPort = "1"
		err := s.sendResetMail(to, code)
		require.ErrorContains(t, err, "127.0.0.1:1",
			"the dial error must name the configured endpoint")
	})

	t.Run("greeting rejection aborts before EHLO", func(t *testing.T) {
		srv := InxStartSMTP(t, "554 5.7.1 no service for you")
		s := inxSMTPServer(t)
		s.cfg.SMTPPort = srv.Port()
		err := s.sendResetMail(to, code)
		require.ErrorContains(t, err, `554 "5.7.1 no service for you"`)
		require.Empty(t, srv.Cmds(), "no command may follow a rejected greeting")
		srv.RequireDone(t)
	})

	t.Run("happy path delivers the exact message", func(t *testing.T) {
		srv := InxStartSMTP(t, inxMailScript()...)
		s := inxSMTPServer(t)
		s.cfg.SMTPPort = srv.Port()

		require.NoError(t, s.sendResetMail(to, code))
		require.Equal(t, []string{
			"EHLO localhost",
			"MAIL FROM:<" + inxMailFrom + ">",
			"RCPT TO:<" + to + ">",
			"DATA",
			"QUIT",
		}, srv.Cmds(), "the full SMTP dialogue must run in order")
		msgs := srv.Msgs()
		require.Len(t, msgs, 1)
		require.Contains(t, msgs[0],
			"From: "+inxMailFrom+"\r\nTo: "+to+"\r\nSubject: Atur ulang sandi Kidversa\r\n"+
				"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n",
			"the RFC-5322 header block must be exact")
		require.Contains(t, msgs[0],
			strings.ReplaceAll(resetMailBody(code, 15), "\n", "\r\n"),
			"the body must carry the code and the configured 15-minute TTL (SMTP dot-writer relays CRLF)")
		srv.RequireDone(t)
	})

	t.Run("authenticated send runs AUTH PLAIN", func(t *testing.T) {
		srv := InxStartSMTP(t,
			"220 inx.local ESMTP",
			"250-inx.local\r\n250 OK",
			"235 2.7.0 authenticated",
			"250 2.1.0 sender ok",
			"250 2.1.5 recipient ok",
			"354 send it",
			"250 2.0.0 queued",
			"221 2.0.0 bye",
		)
		s := inxSMTPServer(t)
		s.cfg.SMTPPort = srv.Port()
		s.cfg.SMTPUser = "mailer"
		s.cfg.SMTPPass = "pw3"

		require.NoError(t, s.sendResetMail(to, code))
		wantAuth := "AUTH PLAIN " + base64.StdEncoding.EncodeToString([]byte("\x00mailer\x00pw3"))
		require.Equal(t, []string{
			"EHLO localhost",
			wantAuth,
			"MAIL FROM:<" + inxMailFrom + ">",
			"RCPT TO:<" + to + ">",
			"DATA",
			"QUIT",
		}, srv.Cmds())
		srv.RequireDone(t)
	})

	t.Run("auth rejection propagates the 535", func(t *testing.T) {
		srv := InxStartSMTP(t,
			"220 inx.local ESMTP",
			"250-inx.local\r\n250 OK",
			"535 5.7.8 bad credentials",
			"501 5.5.2 aborted",
			"221 2.0.0 bye",
		)
		s := inxSMTPServer(t)
		s.cfg.SMTPPort = srv.Port()
		s.cfg.SMTPUser = "mailer"
		s.cfg.SMTPPass = "wrong"

		err := s.sendResetMail(to, code)
		require.ErrorContains(t, err, `535 "5.7.8 bad credentials"`)
		require.Equal(t, "QUIT", srv.Cmds()[len(srv.Cmds())-1],
			"a failed AUTH must abort with * then QUIT")
		srv.RequireDone(t)
	})

	t.Run("STARTTLS offered but rejected aborts the send", func(t *testing.T) {
		srv := InxStartSMTP(t,
			"220 inx.local ESMTP",
			"250-inx.local\r\n250-STARTTLS\r\n250 OK",
			"454 4.7.0 TLS unavailable",
		)
		s := inxSMTPServer(t)
		s.cfg.SMTPPort = srv.Port()

		err := s.sendResetMail(to, code)
		require.ErrorContains(t, err, `454 "4.7.0 TLS unavailable"`)
		require.Equal(t, "STARTTLS", srv.Cmds()[1],
			"an advertised STARTTLS extension must be attempted")
		srv.RequireDone(t)
	})

	t.Run("MAIL FROM rejection", func(t *testing.T) {
		srv := InxStartSMTP(t,
			"220 inx.local ESMTP",
			"250-inx.local\r\n250 OK",
			"550 5.1.8 sender rejected",
		)
		s := inxSMTPServer(t)
		s.cfg.SMTPPort = srv.Port()
		require.ErrorContains(t, s.sendResetMail(to, code), `550 "5.1.8 sender rejected"`)
		srv.RequireDone(t)
	})

	t.Run("RCPT TO rejection", func(t *testing.T) {
		srv := InxStartSMTP(t,
			"220 inx.local ESMTP",
			"250-inx.local\r\n250 OK",
			"250 2.1.0 sender ok",
			"550 5.1.1 unknown recipient",
		)
		s := inxSMTPServer(t)
		s.cfg.SMTPPort = srv.Port()
		require.ErrorContains(t, s.sendResetMail(to, code), `550 "5.1.1 unknown recipient"`)
		srv.RequireDone(t)
	})

	t.Run("DATA command rejection", func(t *testing.T) {
		srv := InxStartSMTP(t,
			"220 inx.local ESMTP",
			"250-inx.local\r\n250 OK",
			"250 2.1.0 sender ok",
			"250 2.1.5 recipient ok",
			"450 4.3.1 try later",
		)
		s := inxSMTPServer(t)
		s.cfg.SMTPPort = srv.Port()
		require.ErrorContains(t, s.sendResetMail(to, code), `450 "4.3.1 try later"`)
		srv.RequireDone(t)
	})

	t.Run("message acceptance failure surfaces from w.Close", func(t *testing.T) {
		srv := InxStartSMTP(t,
			"220 inx.local ESMTP",
			"250-inx.local\r\n250 OK",
			"250 2.1.0 sender ok",
			"250 2.1.5 recipient ok",
			"354 send it",
			"451 4.3.0 temporary failure",
		)
		s := inxSMTPServer(t)
		s.cfg.SMTPPort = srv.Port()
		require.ErrorContains(t, s.sendResetMail(to, code), `451 "4.3.0 temporary failure"`)
		require.Len(t, srv.Msgs(), 1, "the message was transmitted before the 451")
		srv.RequireDone(t)
	})

	t.Run("QUIT rejection fails the send", func(t *testing.T) {
		srv := InxStartSMTP(t,
			"220 inx.local ESMTP",
			"250-inx.local\r\n250 OK",
			"250 2.1.0 sender ok",
			"250 2.1.5 recipient ok",
			"354 send it",
			"250 2.0.0 queued",
			"250 unexpected goodbye",
		)
		s := inxSMTPServer(t)
		s.cfg.SMTPPort = srv.Port()
		require.ErrorContains(t, s.sendResetMail(to, code), `250 "unexpected goodbye"`,
			"client.Quit must surface a non-221 goodbye")
		srv.RequireDone(t)
	})
}

// ---------------------------------------------------------------------------
// ai.go — renderVars (pure)
// ---------------------------------------------------------------------------

func TestInxRenderVars(t *testing.T) {
	cases := []struct {
		name string
		tpl  string
		vars map[string]string
		want string
	}{
		{"replaces every named var", "Halo {{Name}}, umur {{Age}} tahun",
			map[string]string{"Name": "Ayu", "Age": "5"}, "Halo Ayu, umur 5 tahun"},
		{"replaces repeated occurrences", "{{A}} then {{A}}",
			map[string]string{"A": "x"}, "x then x"},
		{"leaves unknown placeholders untouched", "keep {{Unknown}} as-is",
			map[string]string{"Other": "v"}, "keep {{Unknown}} as-is"},
		{"ignores map keys absent from the template", "{{A}}",
			map[string]string{"A": "1", "B": "2"}, "1"},
		{"empty template", "", map[string]string{"A": "1"}, ""},
		{"empty value clears the slot", "[{{A}}]", map[string]string{"A": ""}, "[]"},
		{"no HTML escaping", "{{V}}",
			map[string]string{"V": `<b>&"quoted"</b>`}, `<b>&"quoted"</b>`},
		{"multiline value passes through", "{{V}}",
			map[string]string{"V": "l1\nl2"}, "l1\nl2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, renderVars(tc.tpl, tc.vars))
		})
	}
}

// ---------------------------------------------------------------------------
// ai.go — generateNarrative
// ---------------------------------------------------------------------------

// InxRoundTripFunc lets a test answer or fail a request without a network.
type InxRoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper.
func (f InxRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// InxErrReader always fails with the configured error.
type InxErrReader struct{ Err error }

// Read implements io.Reader.
func (r InxErrReader) Read([]byte) (int, error) { return 0, r.Err }

func inxGeminiResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestInxGenerateNarrative(t *testing.T) {
	s, _ := newTestServer(t)
	s.cfg.GeminiAPIKey = "g-key-1"
	s.cfg.GeminiModel = "gemini-unit"

	sysRaw, err := os.ReadFile("prompts/system.md")
	require.NoError(t, err)
	userRaw, err := os.ReadFile("prompts/user.md")
	require.NoError(t, err)

	type capture struct {
		url    string
		apiKey string
		body   string
	}
	var got capture

	// answer sets the transport for the current subtest and records the
	// outgoing request verbatim.
	answer := func(t *testing.T, fn func(*http.Request) (*http.Response, error)) {
		t.Helper()
		s.http = &http.Client{Transport: InxRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Body != nil {
				b, _ := io.ReadAll(r.Body)
				got = capture{url: r.URL.String(), apiKey: r.Header.Get("x-goog-api-key"), body: string(b)}
			}
			return fn(r)
		})}
	}

	t.Run("renders the prompt and parses the narrative", func(t *testing.T) {
		answer(t, func(*http.Request) (*http.Response, error) {
			return inxGeminiResponse(200,
				`{"candidates":[{"content":{"parts":[{"text":"  Bima mulai berani berbicara.  "}]}}]}`), nil
		})
		text, err := s.generateNarrative("Bima <Anak>", 7, "Petualangan Sains",
			"Matematika Tematik", "2026-01-02", "3 BSH - berani mencoba\n2 MB - perlu damping")
		require.NoError(t, err)
		require.Equal(t, "Bima mulai berani berbicara.", text,
			"the narrative must be returned trimmed")

		require.Equal(t,
			"https://generativelanguage.googleapis.com/v1beta/models/gemini-unit:generateContent",
			got.url, "the request must target the configured model endpoint")
		require.Equal(t, "g-key-1", got.apiKey, "the API key header must be sent")

		wantUser := strings.NewReplacer(
			"{{ChildName}}", "Bima <Anak>",
			"{{ChildAge}}", "7",
			"{{SessionName}}", "Petualangan Sains",
			"{{TopicName}}", "Matematika Tematik",
			"{{SessionDate}}", "2026-01-02",
			"{{Assessments}}", "3 BSH - berani mencoba\n2 MB - perlu damping",
		).Replace(string(userRaw))
		wantPrompt := strings.TrimSpace(string(sysRaw) + "\n\n" + wantUser)

		var payload struct {
			Contents []struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
		}
		require.NoError(t, json.Unmarshal([]byte(got.body), &payload))
		require.Len(t, payload.Contents, 1)
		require.Len(t, payload.Contents[0].Parts, 1)
		require.Equal(t, wantPrompt, payload.Contents[0].Parts[0].Text,
			"system+user prompt must be rendered with {{Var}} substitution and no HTML escaping")
	})

	t.Run("non-200 maps to the exact gemini http error", func(t *testing.T) {
		answer(t, func(*http.Request) (*http.Response, error) {
			return inxGeminiResponse(429, "quota exceeded"), nil
		})
		_, err := s.generateNarrative("A", 5, "S", "T", "2026-01-02", "1 BSH")
		require.EqualError(t, err, "gemini http 429: quota exceeded")
	})

	t.Run("unparsable body surfaces the JSON error", func(t *testing.T) {
		answer(t, func(*http.Request) (*http.Response, error) {
			return inxGeminiResponse(200, "{not json"), nil
		})
		_, err := s.generateNarrative("A", 5, "S", "T", "2026-01-02", "1 BSH")
		require.ErrorContains(t, err, "invalid character",
			"the raw JSON error must reach the caller (AI_FAILED upstream)")
	})

	t.Run("missing candidates", func(t *testing.T) {
		answer(t, func(*http.Request) (*http.Response, error) {
			return inxGeminiResponse(200, `{"promptFeedback":{"blockReason":"SAFETY"}}`), nil
		})
		_, err := s.generateNarrative("A", 5, "S", "T", "2026-01-02", "1 BSH")
		require.EqualError(t, err, "gemini returned no candidates")
	})

	t.Run("candidates without parts", func(t *testing.T) {
		answer(t, func(*http.Request) (*http.Response, error) {
			return inxGeminiResponse(200, `{"candidates":[{"content":{}}]}`), nil
		})
		_, err := s.generateNarrative("A", 5, "S", "T", "2026-01-02", "1 BSH")
		require.EqualError(t, err, "gemini returned no candidates")
	})

	t.Run("blank narrative text", func(t *testing.T) {
		answer(t, func(*http.Request) (*http.Response, error) {
			return inxGeminiResponse(200,
				`{"candidates":[{"content":{"parts":[{"text":"   \n "}]}}]}`), nil
		})
		_, err := s.generateNarrative("A", 5, "S", "T", "2026-01-02", "1 BSH")
		require.EqualError(t, err, "gemini returned empty text")
	})

	t.Run("transport failure is returned as-is", func(t *testing.T) {
		sentinel := errors.New("gemini transport cut")
		answer(t, func(*http.Request) (*http.Response, error) {
			return nil, sentinel
		})
		_, err := s.generateNarrative("A", 5, "S", "T", "2026-01-02", "1 BSH")
		require.ErrorIs(t, err, sentinel)
	})

	t.Run("unreadable response body is returned", func(t *testing.T) {
		sentinel := errors.New("body reader died")
		answer(t, func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Header:     make(http.Header),
				Body:       io.NopCloser(InxErrReader{Err: sentinel}),
			}, nil
		})
		_, err := s.generateNarrative("A", 5, "S", "T", "2026-01-02", "1 BSH")
		require.ErrorIs(t, err, sentinel)
	})

	t.Run("model with control chars fails URL construction", func(t *testing.T) {
		answer(t, func(*http.Request) (*http.Response, error) {
			t.Error("no request may be issued for an unparsable URL")
			return inxGeminiResponse(200, `{}`), nil
		})
		s.cfg.GeminiModel = "bad\nmodel"
		defer func() { s.cfg.GeminiModel = "gemini-unit" }()
		_, err := s.generateNarrative("A", 5, "S", "T", "2026-01-02", "1 BSH")
		require.ErrorContains(t, err, "invalid control character")
	})
}

// ---------------------------------------------------------------------------
// wa.go — sendText
// ---------------------------------------------------------------------------

func TestInxSendText(t *testing.T) {
	s, _ := newTestServer(t)

	t.Run("posts the exact payload and returns messageId", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			wantPath := "/api/sessions/" + s.cfg.OpenwaSession + "/messages/send-text"
			if r.URL.Path != wantPath {
				t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
			}
			if r.Header.Get("X-API-Key") != s.cfg.OpenwaAPIKey {
				t.Errorf("X-API-Key = %q, want %q", r.Header.Get("X-API-Key"), s.cfg.OpenwaAPIKey)
			}
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Errorf("payload is not a JSON object: %s", body)
			} else if len(payload) != 2 || payload["chatId"] != "+628111111111@c.us" ||
				payload["text"] != "Halo <b>semua</b>" {
				t.Errorf("payload = %s, want exactly chatId/text verbatim", body)
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"messageId":"wamid.ABC123"}`)
		}))
		t.Cleanup(srv.Close)
		s.cfg.OpenwaBaseURL = srv.URL
		s.cfg.OpenwaSession = "kidversa-unit"
		s.cfg.OpenwaAPIKey = "wa-key-9"

		id, err := s.sendText("+628111111111@c.us", "Halo <b>semua</b>")
		require.NoError(t, err)
		require.Equal(t, "wamid.ABC123", id)
		require.Equal(t, int32(1), hits.Load(), "exactly one attempt on success")
	})

	t.Run("retries exactly once after a 5xx", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hits.Add(1) == 1 {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"messageId":"wamid.RETRY"}`)
		}))
		t.Cleanup(srv.Close)
		s.cfg.OpenwaBaseURL = srv.URL

		id, err := s.sendText("1@c.us", "halo")
		require.NoError(t, err)
		require.Equal(t, "wamid.RETRY", id)
		require.Equal(t, int32(2), hits.Load(), "exactly one retry must run")
	})

	t.Run("persistent 5xx returns the exact openwa http error", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, "boom")
		}))
		t.Cleanup(srv.Close)
		s.cfg.OpenwaBaseURL = srv.URL

		id, err := s.sendText("1@c.us", "halo")
		require.Equal(t, "", id)
		require.EqualError(t, err, "openwa http 500: boom")
		require.Equal(t, int32(2), hits.Load(), "the retry budget is exactly 2 attempts")
	})

	t.Run("2xx without messageId yields empty id and no error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted) // 202: no body at all
		}))
		t.Cleanup(srv.Close)
		s.cfg.OpenwaBaseURL = srv.URL

		id, err := s.sendText("1@c.us", "halo")
		require.NoError(t, err)
		require.Equal(t, "", id,
			"messageId is best-effort: a 2xx answer without one must not fail the send")
	})

	t.Run("unreachable server fails both attempts", func(t *testing.T) {
		s.cfg.OpenwaBaseURL = "http://127.0.0.1:1"
		id, err := s.sendText("1@c.us", "halo")
		require.Equal(t, "", id)
		require.ErrorContains(t, err, "127.0.0.1:1",
			"the transport error must name the unreachable endpoint")
	})

	t.Run("unparsable base URL fails before any request", func(t *testing.T) {
		s.cfg.OpenwaBaseURL = "http://127.0.0.1:1/\nbad"
		id, err := s.sendText("1@c.us", "halo")
		require.Equal(t, "", id)
		require.ErrorContains(t, err, "invalid control character")
	})
}

// ---------------------------------------------------------------------------
// wa.go — handleWaStatus (GET /api/wa/status)
// ---------------------------------------------------------------------------

func inxWaApp(t *testing.T, s *Server) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	s.RegisterWa(app.Group("/api"))
	return app
}

func TestInxHandleWaStatus(t *testing.T) {
	s, mock := newTestServer(t)
	// Mint the tokens against the real clock first: pinning s.now to fixedTS
	// (2026-01-02) would make freshly signed access tokens look expired.
	adminH := hAdmin(t, s)
	fasH := hFAS(t, s)
	s.now = func() time.Time { return fixedTS }
	app := inxWaApp(t, s)

	t.Run("missing token answers INVALID_TOKEN without SQL", func(t *testing.T) {
		res := doReq(t, app, "GET", "/api/wa/status", "", nil)
		requireErrEnvelope(t, res, CodeInvalidToken)
	})

	t.Run("facilitator is FORBIDDEN_ROLE", func(t *testing.T) {
		expectFas := mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
		res := doReq(t, app, "GET", "/api/wa/status", "", fasH)
		requireErrEnvelope(t, res, CodeForbiddenRole)
		require.NotNil(t, expectFas)
	})

	t.Run("unreachable OpenWA answers connected:false", func(t *testing.T) {
		expectAdminAuth(mock)
		s.cfg.OpenwaBaseURL = "http://127.0.0.1:1"
		res := doReq(t, app, "GET", "/api/wa/status", "", adminH)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, map[string]any{"connected": false}, data,
			"an unreachable OpenWA must answer exactly {connected:false} — never WA_FAILED")
	})

	t.Run("ready session answers the live session and checked_at", func(t *testing.T) {
		expectAdminAuth(mock)
		s.invalidateWaID()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/sessions":
				io.WriteString(w, `[{"id":"kidversa-test","name":"Sesi Cerdas","status":"ready"}]`)
			case "/api/sessions/kidversa-test":
				io.WriteString(w, `{"id":"kidversa-test","name":"Sesi Cerdas","status":"ready"}`)
			default:
				t.Errorf("unexpected probe path %q", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)
		s.cfg.OpenwaBaseURL = srv.URL

		res := doReq(t, app, "GET", "/api/wa/status", "", adminH)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, map[string]any{
			"connected":  true,
			"session":    "Sesi Cerdas",
			"checked_at": "2026-01-02T03:04:05Z",
		}, data, "the status envelope must carry the live session name and the clock snapshot")
	})

	t.Run("qr_ready is not connected", func(t *testing.T) {
		expectAdminAuth(mock)
		s.invalidateWaID()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/sessions":
				io.WriteString(w, `[{"id":"kidversa-test","name":"Sesi Cerdas","status":"qr_ready"}]`)
			case "/api/sessions/kidversa-test":
				io.WriteString(w, `{"id":"kidversa-test","name":"Sesi Cerdas","status":"qr_ready"}`)
			default:
				t.Errorf("unexpected probe path %q", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)
		s.cfg.OpenwaBaseURL = srv.URL

		res := doReq(t, app, "GET", "/api/wa/status", "", adminH)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, false, data["connected"])
		require.Equal(t, "Sesi Cerdas", data["session"])
	})

	t.Run("ready session without a name falls back to the configured id", func(t *testing.T) {
		expectAdminAuth(mock)
		s.invalidateWaID()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/sessions":
				io.WriteString(w, `[{"id":"kidversa-test","name":"kidversa-test","status":"ready"}]`)
			case "/api/sessions/kidversa-test":
				io.WriteString(w, `{"status":"ready"}`)
			default:
				t.Errorf("unexpected probe path %q", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)
		s.cfg.OpenwaBaseURL = srv.URL

		res := doReq(t, app, "GET", "/api/wa/status", "", adminH)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, map[string]any{
			"connected":  true,
			"session":    "kidversa-test",
			"checked_at": "2026-01-02T03:04:05Z",
		}, data, "an empty session name falls back to OPENWA_SESSION")
	})

	t.Run("session read failure answers connected:false", func(t *testing.T) {
		expectAdminAuth(mock)
		s.invalidateWaID()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/sessions":
				io.WriteString(w, `[{"id":"kidversa-test","name":"kidversa-test","status":"ready"}]`)
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
		}))
		t.Cleanup(srv.Close)
		s.cfg.OpenwaBaseURL = srv.URL

		res := doReq(t, app, "GET", "/api/wa/status", "", adminH)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, map[string]any{"connected": false}, data)
	})
}

// ---------------------------------------------------------------------------
// wa.go — startWaWatcher lifecycle
// ---------------------------------------------------------------------------

func inxWaitFrame(t *testing.T, sub *subscription, within time.Duration) Frame {
	t.Helper()
	select {
	case f := <-sub.ch:
		return f
	case <-time.After(within):
		t.Fatalf("no frame published within %s", within)
		return Frame{}
	}
}

// TestInxStartWaWatcherPollsPublishesAndStops runs the real 5 s poll loop
// against a local server: poll #1 records the down baseline (no publish),
// poll #2 observes the transition and must publish wa.changed{connected:true};
// Stop must then reap the goroutine before the test ends.
func TestInxStartWaWatcherPollsPublishesAndStops(t *testing.T) {
	s, _ := newTestServer(t)
	var stateHits atomic.Int32
	ticks := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			io.WriteString(w, `[{"id":"kidversa-test","name":"Kidversa"}]`)
		case "/api/sessions/kidversa-test":
			n := stateHits.Add(1)
			select {
			case ticks <- struct{}{}:
			default:
			}
			if n == 1 {
				io.WriteString(w, `{"status":"disconnected"}`) // baseline: down
				return
			}
			io.WriteString(w, `{"status":"ready"}`) // transition: up
		default:
			t.Errorf("watcher probed unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	s.cfg.OpenwaBaseURL = srv.URL

	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)
	t.Cleanup(s.Stop) // idempotent: guarantees the loop dies even on failure

	s.startWaWatcher()

	select {
	case <-ticks:
	case <-time.After(8 * time.Second):
		t.Fatal("the watcher never polled OpenWA within one tick period")
	}
	// First observation only records the baseline — no wa.changed may fire.
	for range 4 {
		time.Sleep(25 * time.Millisecond)
		requireNoFrame(t, spy, "the baseline observation must not publish wa.changed")
	}

	select {
	case <-ticks: // second poll sees down→up
	case <-time.After(8 * time.Second):
		t.Fatal("the watcher did not poll OpenWA a second time")
	}
	f := inxWaitFrame(t, spy, 5*time.Second)
	require.Equal(t, "wa.changed", f.Entity)
	require.Equal(t, "tick", f.Action)
	require.NotNil(t, f.Connected, "the transition frame must carry the connection state")
	require.Equal(t, true, *f.Connected, "down→up must publish connected=true")
	require.Equal(t, int64(0), f.TenantID, "wa.changed broadcasts at tenant 0")

	s.Stop()
	srv.Close()
	s.http.CloseIdleConnections()
	// A global goroutine count would also track unrelated tests' leftovers
	// (stream subs, httptest servers, sqlmock), so match the watcher's OWN
	// frame in a complete stack dump instead.
	watcherAlive := func() bool { return strings.Contains(inxAllStacks(), "startWaWatcher") }
	deadline := time.Now().Add(30 * time.Second)
	for watcherAlive() && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	require.False(t, watcherAlive(), "the startWaWatcher goroutine must exit after Stop")
	require.Equal(t, int32(2), stateHits.Load(), "exactly two session reads must have run")
}

// inxAllStacks dumps every goroutine stack, growing the buffer until the dump
// is complete (runtime.Stack truncates at len(buf) otherwise).
func inxAllStacks() string {
	for size := 1 << 20; ; size *= 4 {
		buf := make([]byte, size)
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
	}
}

// ---------------------------------------------------------------------------
// pscope.go — pScope prologue
// ---------------------------------------------------------------------------

// inxPScopeApp mounts a probe route that sets the auth Locals the middleware
// would set and then runs the real pScope prologue, reporting whether it
// passed (and with which ids).
func inxPScopeApp(t *testing.T, s *Server, u *userRow, tenant int64) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Get("/inx/pscope/:sid/:stid/:cid", func(c fiber.Ctx) error {
		c.Locals(kvUser, u)
		c.Locals(kvTenant, tenant)
		_, cancel, sid, stid, cid, tn, _, okP := s.pScope(c)
		if !okP {
			return nil // envelope already written by pScope
		}
		defer cancel()
		return ok(c, fiber.Map{"sid": sid, "stid": stid, "cid": cid, "tenant": tn})
	})
	return app
}

const (
	inxSQLExists = "SELECT EXISTS(SELECT 1 FROM session_members"
	inxSQLOwner  = "SELECT g.facilitator_id FROM session_members sm"
	inxSQLPhoto  = "SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?"
)

func TestInxPScope(t *testing.T) {
	// --- idParam gate: malformed ids answer NOT_FOUND and run NO query. ---
	t.Run("malformed sid is NOT_FOUND with zero SQL", func(t *testing.T) {
		s, mock := newTestServer(t) // no expectations: any query fails the test
		app := inxPScopeApp(t, s, fasUserRow(), int64(1))
		res := doReq(t, app, "GET", "/inx/pscope/abc/9/3", "", nil)
		requireErrEnvelope(t, res, CodeNotFound)
		require.NoError(t, mock.ExpectationsWereMet(),
			"validation must answer before any DB access")
	})

	t.Run("zero sid is NOT_FOUND with zero SQL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := inxPScopeApp(t, s, fasUserRow(), int64(1))
		res := doReq(t, app, "GET", "/inx/pscope/0/9/3", "", nil)
		requireErrEnvelope(t, res, CodeNotFound)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("malformed stid is NOT_FOUND with zero SQL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := inxPScopeApp(t, s, fasUserRow(), int64(1))
		res := doReq(t, app, "GET", "/inx/pscope/7/x/3", "", nil)
		requireErrEnvelope(t, res, CodeNotFound)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("malformed cid is NOT_FOUND with zero SQL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := inxPScopeApp(t, s, fasUserRow(), int64(1))
		res := doReq(t, app, "GET", "/inx/pscope/7/9/x", "", nil)
		requireErrEnvelope(t, res, CodeNotFound)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	// --- membership gate ---
	t.Run("membership query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		mock.ExpectQuery(q(inxSQLExists)).WithArgs(int64(7), int64(3)).
			WillReturnError(errors.New("membership down"))
		app := inxPScopeApp(t, s, fasUserRow(), int64(1))
		res := doReq(t, app, "GET", "/inx/pscope/7/9/3", "", nil)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("non-member child is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		mock.ExpectQuery(q(inxSQLExists)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
		app := inxPScopeApp(t, s, fasUserRow(), int64(1))
		res := doReq(t, app, "GET", "/inx/pscope/7/9/3", "", nil)
		requireErrEnvelope(t, res, CodeNotFound)
	})

	// --- FAS ownership gate ---
	t.Run("membership row without group is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		mock.ExpectQuery(q(inxSQLExists)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q(inxSQLOwner)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"})) // no rows
		app := inxPScopeApp(t, s, fasUserRow(), int64(1))
		res := doReq(t, app, "GET", "/inx/pscope/7/9/3", "", nil)
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("ownership query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		mock.ExpectQuery(q(inxSQLExists)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q(inxSQLOwner)).WithArgs(int64(7), int64(3)).
			WillReturnError(errors.New("ownership down"))
		app := inxPScopeApp(t, s, fasUserRow(), int64(1))
		res := doReq(t, app, "GET", "/inx/pscope/7/9/3", "", nil)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("group without facilitator is NOT_OWNER", func(t *testing.T) {
		s, mock := newTestServer(t)
		mock.ExpectQuery(q(inxSQLExists)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q(inxSQLOwner)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(nil))
		app := inxPScopeApp(t, s, fasUserRow(), int64(1))
		res := doReq(t, app, "GET", "/inx/pscope/7/9/3", "", nil)
		requireErrEnvelope(t, res, CodeNotOwner)
	})

	t.Run("foreign group is NOT_OWNER", func(t *testing.T) {
		s, mock := newTestServer(t)
		mock.ExpectQuery(q(inxSQLExists)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q(inxSQLOwner)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(int64(99)))
		app := inxPScopeApp(t, s, fasUserRow(), int64(1))
		res := doReq(t, app, "GET", "/inx/pscope/7/9/3", "", nil)
		requireErrEnvelope(t, res, CodeNotOwner)
	})

	// --- photo scope gate ---
	t.Run("stid outside the session is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		mock.ExpectQuery(q(inxSQLExists)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q(inxSQLOwner)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(int64(3)))
		mock.ExpectQuery(q(inxSQLPhoto)).WithArgs(int64(9), int64(7), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"1"})) // no rows
		app := inxPScopeApp(t, s, fasUserRow(), int64(1))
		res := doReq(t, app, "GET", "/inx/pscope/7/9/3", "", nil)
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("photo scope query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		mock.ExpectQuery(q(inxSQLExists)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q(inxSQLOwner)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(int64(3)))
		mock.ExpectQuery(q(inxSQLPhoto)).WithArgs(int64(9), int64(7), int64(1)).
			WillReturnError(errors.New("photo scope down"))
		app := inxPScopeApp(t, s, fasUserRow(), int64(1))
		res := doReq(t, app, "GET", "/inx/pscope/7/9/3", "", nil)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	// --- pass-through ---
	t.Run("FAS owner with valid stid passes with tenant scope", func(t *testing.T) {
		s, mock := newTestServer(t)
		mock.ExpectQuery(q(inxSQLExists)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q(inxSQLOwner)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(int64(3)))
		mock.ExpectQuery(q(inxSQLPhoto)).WithArgs(int64(9), int64(7), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
		app := inxPScopeApp(t, s, fasUserRow(), int64(1))
		res := doReq(t, app, "GET", "/inx/pscope/7/9/3", "", nil)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, map[string]any{
			"sid": float64(7), "stid": float64(9), "cid": float64(3), "tenant": float64(1),
		}, data)
	})

	t.Run("ADMIN skips the ownership query; SA tenant 0 drops the tenant cond", func(t *testing.T) {
		s, mock := newTestServer(t)
		mock.ExpectQuery(q(inxSQLExists)).WithArgs(int64(7), int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		// tenant 0 → photoScope runs WITHOUT the tenant_id argument.
		mock.ExpectQuery(q(inxSQLPhoto)).WithArgs(int64(9), int64(7)).
			WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
		app := inxPScopeApp(t, s, adminUserRow(), int64(0))
		res := doReq(t, app, "GET", "/inx/pscope/7/9/3", "", nil)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, map[string]any{
			"sid": float64(7), "stid": float64(9), "cid": float64(3), "tenant": float64(0),
		}, data)
		require.NoError(t, mock.ExpectationsWereMet(),
			"non-FAS roles must never run the facilitator ownership query")
	})
}

// ---------------------------------------------------------------------------
// Fake MySQL wire server (Migrate/OpenDB open their own pool, so sqlmock
// cannot drive them). One scripted step per protocol event, consumed in
// order; connections speak just enough MySQL 4.1 for go-sql-driver.
// ---------------------------------------------------------------------------

const (
	inxComQuit         = 0x01
	inxComQuery        = 0x03
	inxComPing         = 0x0e
	inxComStmtPrepare  = 0x16
	inxComStmtExecute  = 0x17
	inxComStmtClose    = 0x19
	inxMySQLServerCaps = 0x002BA28D // CLIENT_MYSQL|LONG_FLAG|CONNECT_WITH_DB|LOCAL_FILES|
	// PROTOCOL_41|TRANSACTIONS|SECURE_CONNECTION|MULTI_STATEMENTS|
	// MULTI_RESULTS|PLUGIN_AUTH|PLUGIN_AUTH_LENENC — no DEPRECATE_EOF, so the
	// driver uses classic EOF packets.
)

// InxMySQLResp is one scripted server answer: zero value = OK packet,
// Errno = ERR packet, Cols non-nil = result set.
type InxMySQLResp struct {
	Errno    uint16
	Msg      string
	Cols     []string
	Rows     [][]*string
	RowErrno uint16 // ERR after the rows (mid-iteration failure)
	RowMsg   string
}

// InxMySQLStep binds a SQL substring to its response; steps are consumed in
// order (peek at COM_STMT_PREPARE, consume at query/execute).
type InxMySQLStep struct {
	Match string
	Resp  InxMySQLResp
}

// InxMyErr builds an ERR response.
func InxMyErr(errno uint16, msg string) InxMySQLResp {
	return InxMySQLResp{Errno: errno, Msg: msg}
}

// InxMyTable builds a result-set response.
func InxMyTable(cols []string, rows ...[]*string) InxMySQLResp {
	return InxMySQLResp{Cols: cols, Rows: rows}
}

// InxMySQLFake is a scripted MySQL server on 127.0.0.1.
type InxMySQLFake struct {
	ln         net.Listener
	mu         sync.Mutex
	steps      []InxMySQLStep
	pos        int
	log        []string
	problems   []string
	conns      []net.Conn
	connWg     sync.WaitGroup
	acceptDone chan struct{}
}

// InxStartMySQL starts the fake server and tears it down at test end.
func InxStartMySQL(t *testing.T, steps ...InxMySQLStep) *InxMySQLFake {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	f := &InxMySQLFake{ln: ln, steps: steps, acceptDone: make(chan struct{})}
	go func() {
		defer close(f.acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns = append(f.conns, c)
			f.mu.Unlock()
			f.connWg.Add(1)
			go func(c net.Conn) {
				defer f.connWg.Done()
				defer c.Close()
				f.serve(c)
			}(c)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		<-f.acceptDone
		f.mu.Lock()
		for _, c := range f.conns {
			c.Close()
		}
		f.mu.Unlock()
		f.connWg.Wait()
	})
	return f
}

// InxMySQLCfg returns a Config pointing at the fake server.
func InxMySQLCfg(t *testing.T, f *InxMySQLFake) Config {
	t.Helper()
	return Config{
		DBHost: "127.0.0.1", DBPort: f.Port(), DBName: "kidversa_test", DBUser: "root",
	}
}

// Port returns the listening port.
func (f *InxMySQLFake) Port() string {
	return strconv.Itoa(f.ln.Addr().(*net.TCPAddr).Port)
}

// Log returns every executed statement (COM_QUERY + COM_STMT_EXECUTE) plus
// pings, in wire order.
func (f *InxMySQLFake) Log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

// RequireDone asserts clean protocol handling and full step consumption
// (exactly consumed steps, matching an optional expected count).
func (f *InxMySQLFake) RequireDone(t *testing.T, consumed ...int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Empty(t, f.problems, "fake MySQL protocol/step problems")
	want := len(f.steps)
	if len(consumed) == 1 {
		want = consumed[0]
	}
	require.Equal(t, want, f.pos, "scripted steps consumed")
}

func (f *InxMySQLFake) violate(msg string) {
	f.mu.Lock()
	f.problems = append(f.problems, msg)
	f.mu.Unlock()
}

func (f *InxMySQLFake) appendLog(s string) {
	f.mu.Lock()
	f.log = append(f.log, s)
	f.mu.Unlock()
}

func (f *InxMySQLFake) peekStep(sql string) (InxMySQLStep, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pos >= len(f.steps) {
		f.problems = append(f.problems, "unexpected statement: "+sql)
		return InxMySQLStep{}, false
	}
	st := f.steps[f.pos]
	if !strings.Contains(sql, st.Match) {
		f.problems = append(f.problems,
			fmt.Sprintf("step %d: got %q, want fragment %q", f.pos, sql, st.Match))
		return InxMySQLStep{}, false
	}
	return st, true
}

func (f *InxMySQLFake) takeStep(sql string) (InxMySQLStep, bool) {
	st, ok := f.peekStep(sql)
	if ok {
		f.mu.Lock()
		f.pos++
		f.mu.Unlock()
	}
	return st, ok
}

func (f *InxMySQLFake) serve(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	r := bufio.NewReader(c)
	if err := inxWritePacket(c, inxGreeting(), 0); err != nil {
		f.violate("greeting write: " + err.Error())
		return
	}
	if _, seq, err := inxReadPacket(r); err != nil {
		f.violate("handshake read: " + err.Error())
		return
	} else if seq != 1 {
		f.violate(fmt.Sprintf("handshake response seq=%d, want 1", seq))
	}
	if err := inxWritePacket(c, inxOK(), 2); err != nil {
		return // auth OK (seq follows the handshake response)
	}

	stmts := map[uint32]string{}
	var stmtID uint32
	for {
		_ = c.SetDeadline(time.Now().Add(15 * time.Second))
		payload, seq, err := inxReadPacket(r)
		if err != nil {
			return // client closed (COM_QUIT, EOF or deadline)
		}
		if seq != 0 {
			f.violate(fmt.Sprintf("command seq=%d, want 0", seq))
		}
		if len(payload) == 0 {
			f.violate("empty command packet")
			return
		}
		switch payload[0] {
		case inxComQuit:
			return
		case inxComPing:
			f.appendLog("<PING>")
			if err := inxWritePacket(c, inxOK(), 1); err != nil {
				return
			}
		case inxComQuery:
			sql := string(payload[1:])
			f.appendLog(sql)
			if strings.HasPrefix(sql, "SET NAMES ") {
				// connect-time charset negotiation, always succeeds
				if err := inxWritePacket(c, inxOK(), 1); err != nil {
					return
				}
				continue
			}
			st, ok := f.takeStep(sql)
			if !ok {
				_ = inxWritePacket(c, inxErr(1105, "inx fake: no step for "+sql), 1)
				continue
			}
			if err := f.writeResult(c, st.Resp, false); err != nil {
				return
			}
		case inxComStmtPrepare:
			sql := string(payload[1:])
			st, ok := f.peekStep(sql)
			if !ok {
				_ = inxWritePacket(c, inxErr(1105, "inx fake: no prepare for "+sql), 1)
				continue
			}
			if st.Resp.Errno != 0 {
				// prepare-time failure: the execute never happens
				_, _ = f.takeStep(sql)
				if err := inxWritePacket(c, inxErr(st.Resp.Errno, st.Resp.Msg), 1); err != nil {
					return
				}
				continue
			}
			stmtID++
			stmts[stmtID] = sql
			if err := f.writePackets(c, inxPreparePackets(stmtID, sql, st.Resp)); err != nil {
				return
			}
		case inxComStmtExecute:
			if len(payload) < 5 {
				f.violate("short COM_STMT_EXECUTE")
				return
			}
			id := binary.LittleEndian.Uint32(payload[1:5])
			sql, ok := stmts[id]
			if !ok {
				f.violate(fmt.Sprintf("execute of unknown statement id %d", id))
				return
			}
			st, ok := f.takeStep(sql)
			if !ok {
				_ = inxWritePacket(c, inxErr(1105, "inx fake: no execute for "+sql), 1)
				continue
			}
			f.appendLog(sql)
			if err := f.writeResult(c, st.Resp, true); err != nil {
				return
			}
		case inxComStmtClose:
			// no response packet
		default:
			f.violate(fmt.Sprintf("unsupported command 0x%02x", payload[0]))
			_ = inxWritePacket(c, inxErr(1105, fmt.Sprintf("inx fake: unsupported command 0x%02x", payload[0])), 1)
		}
	}
}

// writeResult answers one statement: OK, ERR or a (binary|text) result set.
func (f *InxMySQLFake) writeResult(c net.Conn, r InxMySQLResp, binaryRows bool) error {
	if r.Errno != 0 {
		return inxWritePacket(c, inxErr(r.Errno, r.Msg), 1)
	}
	if r.Cols == nil {
		return inxWritePacket(c, inxOK(), 1)
	}
	var pkts [][]byte
	pkts = append(pkts, []byte{byte(len(r.Cols))})
	for _, col := range r.Cols {
		pkts = append(pkts, inxColumnDef(col))
	}
	pkts = append(pkts, inxEOF())
	for _, row := range r.Rows {
		if binaryRows {
			pkts = append(pkts, inxBinaryRow(row))
		} else {
			pkts = append(pkts, inxTextRow(row))
		}
	}
	if r.RowErrno != 0 {
		pkts = append(pkts, inxErr(r.RowErrno, r.RowMsg))
	} else {
		pkts = append(pkts, inxEOF())
	}
	return f.writePackets(c, pkts)
}

// writePackets writes one response group with sequences 1..n.
func (f *InxMySQLFake) writePackets(c net.Conn, pkts [][]byte) error {
	for i, p := range pkts {
		if err := inxWritePacket(c, p, byte(i+1)); err != nil {
			return err
		}
	}
	return nil
}

func inxReadPacket(r *bufio.Reader) ([]byte, byte, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, 0, err
	}
	n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, 0, err
	}
	return payload, hdr[3], nil
}

func inxWritePacket(c net.Conn, payload []byte, seq byte) error {
	hdr := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}
	return inxWriteFull(c, append(hdr, payload...))
}

// inxGreeting builds a MySQL 10 handshake v10 packet the driver accepts
// (CLIENT_PROTOCOL_41 + PLUGIN_AUTH, mysql_native_password, classic EOF).
func inxGreeting() []byte {
	const version = "5.7.99-InxFake"
	caps := uint32(inxMySQLServerCaps)
	b := []byte{0x0a}
	b = append(b, version...)
	b = append(b, 0x00)
	b = append(b, 0x01, 0x00, 0x00, 0x00)         // connection id
	b = append(b, []byte("12345678")...)          // auth data part 1
	b = append(b, 0x00)                           // filler
	b = append(b, byte(caps), byte(caps>>8))      // capability lower
	b = append(b, 0x2d)                           // charset
	b = append(b, 0x02, 0x00)                     // status: autocommit
	b = append(b, byte(caps>>16), byte(caps>>24)) // capability upper
	b = append(b, 21)                             // auth plugin data length
	b = append(b, make([]byte, 6)...)             // reserved
	b = append(b, make([]byte, 4)...)             // reserved
	b = append(b, []byte("abcdefghijkl")...)      // auth data part 2
	b = append(b, 0x00)
	b = append(b, "mysql_native_password"...)
	b = append(b, 0x00)
	return b
}

func inxOK() []byte {
	// 0x00, affected=0, insert_id=0, status=autocommit, warnings=0
	return []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}
}

func inxEOF() []byte { return []byte{0xfe, 0x00, 0x00, 0x02, 0x00} }

func inxErr(errno uint16, msg string) []byte {
	p := []byte{0xff, byte(errno), byte(errno >> 8), '#', '4', '2', '0', '0', '0'}
	return append(p, msg...)
}

func inxLenencStr(s string) []byte {
	if len(s) >= 251 {
		panic("inx fake: string too long for 1-byte length encoding")
	}
	b := make([]byte, 0, 1+len(s))
	b = append(b, byte(len(s)))
	return append(b, s...)
}

// inxColumnDef builds one Protocol::ColumnDefinition41 packet (VAR_STRING).
func inxColumnDef(name string) []byte {
	b := inxLenencStr("def")
	b = append(b, inxLenencStr("")...) // schema
	b = append(b, inxLenencStr("")...) // table
	b = append(b, inxLenencStr("")...) // org_table
	b = append(b, inxLenencStr(name)...)
	b = append(b, inxLenencStr(name)...)  // org_name
	b = append(b, 0x0c)                   // length of fixed fields
	b = append(b, 0x2d, 0x00)             // charset
	b = append(b, 0xff, 0x00, 0x00, 0x00) // column length
	b = append(b, 0xfd)                   // VAR_STRING
	b = append(b, 0x00, 0x00)             // flags
	b = append(b, 0x00)                   // decimals
	b = append(b, 0x00, 0x00)             // filler
	return b
}

func inxTextRow(vals []*string) []byte {
	var b []byte
	for _, v := range vals {
		if v == nil {
			b = append(b, 0xfb)
			continue
		}
		b = append(b, inxLenencStr(*v)...)
	}
	return b
}

func inxBinaryRow(vals []*string) []byte {
	n := len(vals)
	bitmap := make([]byte, (n+9)>>3) // (column-count + 7 + 2) / 8
	for i, v := range vals {
		if v == nil {
			bitmap[(i+2)>>3] |= 1 << ((i + 2) & 7)
		}
	}
	b := []byte{0x00} // packet header
	b = append(b, bitmap...)
	for _, v := range vals {
		if v == nil {
			continue
		}
		b = append(b, inxLenencStr(*v)...)
	}
	return b
}

// inxPreparePackets builds the COM_STMT_PREPARE response: header, parameter
// definitions + EOF, column definitions + EOF (classic protocol).
func inxPreparePackets(id uint32, sql string, r InxMySQLResp) [][]byte {
	colCount := len(r.Cols)
	paramCount := strings.Count(sql, "?")
	head := []byte{0x00,
		byte(id), byte(id >> 8), byte(id >> 16), byte(id >> 24),
		byte(colCount), byte(colCount >> 8),
		byte(paramCount), byte(paramCount >> 8),
		0x00,       // filler
		0x00, 0x00, // warnings
	}
	pkts := [][]byte{head}
	for range paramCount {
		pkts = append(pkts, inxColumnDef("?"))
	}
	if paramCount > 0 {
		pkts = append(pkts, inxEOF())
	}
	for _, col := range r.Cols {
		pkts = append(pkts, inxColumnDef(col))
	}
	if colCount > 0 {
		pkts = append(pkts, inxEOF())
	}
	return pkts
}

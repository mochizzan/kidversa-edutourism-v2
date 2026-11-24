package internal

import (
	"bytes"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func newSeedMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db, mock
}

type seedFile struct {
	name string
	body string
}

// seedSQLFiles mirrors Seed's discovery: every seeds/*.sql in ascending name
// order. TestMain already chdir'd to the backend root.
func seedSQLFiles(t *testing.T) []seedFile {
	t.Helper()
	entries, err := os.ReadDir("seeds")
	require.NoError(t, err)
	files := make([]seedFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join("seeds", e.Name()))
		require.NoError(t, err)
		files = append(files, seedFile{name: e.Name(), body: string(body)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	require.NotEmpty(t, files, "the seeds directory must contain SQL files")
	return files
}

// sqlmock collapses whitespace on both sides before matching, so quote the
// normalised file body to assert it was executed verbatim.
func seedExecPattern(body string) string {
	return regexp.QuoteMeta(strings.Join(strings.Fields(body), " "))
}

func expectSeedExecs(mock sqlmock.Sqlmock, files []seedFile) {
	for _, f := range files {
		mock.ExpectExec(seedExecPattern(f.body)).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
}

// captureSeedLog runs fn while capturing the default slog output.
func captureSeedLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	fn()
	return buf.String()
}

func TestSeedAppliesEveryFileVerbatimInOrder(t *testing.T) {
	db, mock := newSeedMock(t)
	files := seedSQLFiles(t)
	expectSeedExecs(mock, files)

	out := captureSeedLog(t, func() {
		require.NoError(t, Seed(db))
	})
	require.Equal(t, len(files), strings.Count(out, "seed applied"))
	for _, f := range files {
		require.Contains(t, out, `file=`+f.name)
	}
	// sqlmock expectations are ordered, so reaching this point also proves the
	// files ran in ascending filename order.
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSeedErrors(t *testing.T) {
	t.Run("missing seeds dir", func(t *testing.T) {
		db, _ := newSeedMock(t)
		old, err := os.Getwd()
		require.NoError(t, err)
		require.NoError(t, os.Chdir(t.TempDir()))
		t.Cleanup(func() { require.NoError(t, os.Chdir(old)) })

		require.ErrorContains(t, Seed(db), "read seeds dir:")
	})

	t.Run("a seed file fails", func(t *testing.T) {
		db, mock := newSeedMock(t)
		files := seedSQLFiles(t)
		boom := errors.New("seed exec down")
		for i, f := range files {
			exp := mock.ExpectExec(seedExecPattern(f.body))
			if i == len(files)-1 {
				exp.WillReturnError(boom)
			} else {
				exp.WillReturnResult(sqlmock.NewResult(0, 1))
			}
		}

		err := Seed(db)
		require.ErrorIs(t, err, boom)
		require.ErrorContains(t, err, "apply "+files[len(files)-1].name+":")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// stripSQLComments removes `--` line comments so clause scans only see SQL.
func stripSQLComments(body string) string {
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// Seeds run on every boot, so they may only fill missing bootstrap rows. Any
// mutating clause would clobber dashboard edits after a restart (the SA row
// used to be rewritten by ON DUPLICATE KEY UPDATE on every boot).
func TestSeedFilesAreInsertOnly(t *testing.T) {
	mutating := regexp.MustCompile(`(?i)\b(UPDATE|DELETE|REPLACE|TRUNCATE|DROP|ALTER)\b`)
	for _, f := range seedSQLFiles(t) {
		body := stripSQLComments(f.body)
		require.Contains(t, strings.ToUpper(body), "INSERT INTO",
			"%s must seed its bootstrap row", f.name)
		require.NotContains(t, strings.ToUpper(body), "ON DUPLICATE KEY",
			"%s must not overwrite an existing row on boot", f.name)
		for _, clause := range mutating.FindAllString(body, -1) {
			t.Fatalf("%s contains mutating clause %q: seeds must be insert-only", f.name, clause)
		}
	}
}

// TestSeedSAGuard pins the insert-only guard: without WHERE NOT EXISTS the
// statement would fail on uq_users_email (or duplicate the SA) on every boot.
func TestSeedSAGuard(t *testing.T) {
	var saBody string
	for _, f := range seedSQLFiles(t) {
		if f.name == "0002_sa.sql" {
			saBody = f.body
		}
	}
	require.NotEmpty(t, saBody, "0002_sa.sql must exist")
	require.Contains(t, strings.ToUpper(stripSQLComments(saBody)), "WHERE NOT EXISTS",
		"0002_sa.sql must skip the insert when the bootstrap email already exists")
}

// Pin the checked-in bootstrap hash: every hash literal in the SA seed must be
// a real bcrypt hash of the documented password (the fixed string replaces the
// old Go-side hashing at boot).
func TestSeedSAHashIsValid(t *testing.T) {
	var saBody string
	for _, f := range seedSQLFiles(t) {
		if f.name == "0002_sa.sql" {
			saBody = f.body
		}
	}
	require.NotEmpty(t, saBody, "0002_sa.sql must exist")

	hashes := regexp.MustCompile(`\$2[aby]\$\d{2}\$[./A-Za-z0-9]{53}`).
		FindAllString(saBody, -1)
	require.NotEmpty(t, hashes)
	for _, hash := range hashes {
		require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("@Kidversafun")))
	}
}

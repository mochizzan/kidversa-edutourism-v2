package internal

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestStoreImageWritesDurableFile(t *testing.T) {
	s, _ := newTestServer(t)
	content := append(append([]byte{}, realPNG(16, 16)...), bytes.Repeat([]byte{1}, 128)...)

	rel, err := s.storeImage("frames", bytes.NewReader(content), "image/png")
	require.NoError(t, err)

	// DB-relative path: <dir>/<32 hex>.<declared ext> — never the client name.
	require.Regexp(t, regexp.MustCompile(`^frames/[0-9a-f]{32}\.png$`), rel)

	full := filepath.Join(s.upload, filepath.FromSlash(rel))
	stored, err := os.ReadFile(full)
	require.NoError(t, err, "the durable file must exist at UPLOAD_DIR/<rel>")
	require.True(t, bytes.Equal(content, stored), "durable bytes must round-trip unchanged")

	// No temp residue: the atomic rename consumed the .tmp file.
	entries, err := os.ReadDir(filepath.Dir(full))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

// Superseded 2026-11-10 (spec 09): the 2026-10-11 "anywhere-accepts,
// store-as-.bin" pin moved to image_test.go as rejection tests, and the
// ≤10 MB byte-for-byte pin lives in TestStoreImageAcceptsDecodeConfigJPGPNG.

func TestRandHexIs32HexChars(t *testing.T) {
	a, err := randHex()
	require.NoError(t, err)
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), a)
	b, err := randHex()
	require.NoError(t, err)
	require.NotEqual(t, a, b, "random names must not collide")
}

func TestRandTokenHexIs64HexChars(t *testing.T) {
	a, err := randTokenHex()
	require.NoError(t, err)
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), a)
	b, err := randTokenHex()
	require.NoError(t, err)
	require.NotEqual(t, a, b, "link tokens must not collide")
}

// TestSweepOrphansCleansOnlyUploadResidue pins the sweep contract: files no
// row references AND older than orphanGrace are removed, young files stay
// (file-before-row upload protection), and dotfile placeholders (.gitkeep)
// are never upload residue.
func TestSweepOrphansCleansOnlyUploadResidue(t *testing.T) {
	s, mock := newTestServer(t)
	old := time.Now().Add(-2 * orphanGrace)

	seed := func(name string, mtime time.Time) string {
		p := filepath.Join(s.upload, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		require.NoError(t, os.Chtimes(p, mtime, mtime))
		return p
	}
	dot := seed(".gitkeep", old)
	orphan := seed("orphan.jpg", old)
	fresh := seed("fresh.jpg", time.Now())

	mock.ExpectQuery(q("SELECT path FROM photos")).
		WillReturnRows(sqlmock.NewRows([]string{"path"}))

	s.sweepOrphans()

	require.NoError(t, mock.ExpectationsWereMet())
	require.FileExists(t, dot, ".gitkeep placeholders must survive the sweep")
	require.NoFileExists(t, orphan, "old unreferenced file must be swept")
	require.FileExists(t, fresh, "files younger than orphanGrace are protected")
}

// TestSweepOrphansRefusesUnresolvedRoot: an empty/"." upload root must abort
// BEFORE any DB access — walking it would treat the working directory (app
// code, migrations, prompts, templates) as upload residue.
func TestSweepOrphansRefusesUnresolvedRoot(t *testing.T) {
	s, _ := newTestServer(t)

	t.Chdir(t.TempDir())
	victim := filepath.Join(".", "victim.txt")
	require.NoError(t, os.WriteFile(victim, []byte("x"), 0o644))
	require.NoError(t, os.Chtimes(victim, time.Now().Add(-2*orphanGrace), time.Now().Add(-2*orphanGrace)))

	// Capture the guard's log: without it a regression would silently fall
	// through to the referencedPaths query (no expectation set → query error
	// → "janitor referenced paths" instead of the skip line).
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s.upload = "."
	s.sweepOrphans()

	require.Contains(t, logBuf.String(), "janitor skip: unresolved upload root",
		"the guard must fire before any DB access or walk")
	require.FileExists(t, victim, "an unresolved root must never be swept")
}

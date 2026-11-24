package internal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fsx helpers (unique across the merged package)
// ---------------------------------------------------------------------------

// FsxCaptureLog swaps the default slog handler for an in-memory buffer for
// the duration of the test (restored via t.Cleanup).
func FsxCaptureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// FsxErrReader is a reader that delivers nothing and always fails with err —
// the disk-failure injection point for durableWrite/storeImage.
type FsxErrReader struct{ Err error }

func (r FsxErrReader) Read([]byte) (int, error) { return 0, r.Err }

// FsxExpectFullSweep declares the exact statement sequence s.sweep issues
// against an empty database: reset-token duty, the three timestamp purges,
// the stale-photo duty and the orphan sweep's referenced-paths union.
func FsxExpectFullSweep(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(q("SELECT DISTINCT school_id FROM reset_tokens")).
		WillReturnRows(sqlmock.NewRows([]string{"school_id"}))
	mock.ExpectExec(q("DELETE FROM reset_tokens WHERE source = 'SELF' AND (expires_at < NOW() OR used_at IS NOT NULL)")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("DELETE FROM reset_tokens WHERE source = 'ADMIN' AND created_at < NOW() - INTERVAL 90 DAY")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = expires_at")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("impersonated_by IS NULL AND expires_at < NOW()")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("DELETE FROM idempotency_keys WHERE created_at < NOW() - INTERVAL 24 HOUR")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("DELETE FROM wa_outbox WHERE created_at < NOW() - INTERVAL 90 DAY")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(q("SELECT id, path FROM photos")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "path"}))
	mock.ExpectQuery(q("UNION SELECT badge_image_snap FROM session_topics WHERE badge_image_snap IS NOT NULL")).
		WillReturnRows(sqlmock.NewRows([]string{"path"}))
}

// FsxWaitForSub polls the hub until a subscription matching want appears.
func FsxWaitForSub(t *testing.T, s *Server, want func(*subscription) bool) *subscription {
	t.Helper()
	var found *subscription
	require.Eventually(t, func() bool {
		s.hubMu.Lock()
		defer s.hubMu.Unlock()
		for _, sub := range s.subs {
			if want(sub) {
				found = sub
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "stream subscription never appeared")
	return found
}

// FsxStartStream issues a stream request and returns (via the hub) once the
// subscription exists — SSE headers are only flushed on the first write, so
// waiting on client.Do alone would deadlock tests that publish nothing.
// The response is delivered on the returned channel once the server flushes.
func FsxStartStream(t *testing.T, s *Server, url string, headers map[string]string,
	want func(*subscription) bool) (<-chan *http.Response, *subscription) {
	t.Helper()
	respCh := make(chan *http.Response, 1)
	go func() {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			close(respCh)
			return
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			close(respCh)
			return
		}
		respCh <- resp
	}()
	sub := FsxWaitForSub(t, s, want)
	return respCh, sub
}

// FsxAwaitResp receives the flushed stream response.
func FsxAwaitResp(t *testing.T, respCh <-chan *http.Response) *http.Response {
	t.Helper()
	select {
	case resp, ok := <-respCh:
		require.True(t, ok, "stream handshake failed before the server flushed headers")
		return resp
	case <-time.After(3 * time.Second):
		t.Fatal("stream response never arrived")
		return nil
	}
}

// FsxDrainSSE closes the client side and drains the reader until EOF so the
// startSSEReader goroutine never outlives the test.
func FsxDrainSSE(resp *http.Response, events <-chan sseEvent) {
	_ = resp.Body.Close()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-deadline:
			return
		}
	}
}

// ---------------------------------------------------------------------------
// sweepResetTokens (files.go)
// ---------------------------------------------------------------------------

// TestFsxSweepResetTokensPublishesSweepFrames pins the happy contract: the
// exact SELECT/DELETE pair runs in order, and every school the select touched
// (NULL/0 → broadcast) receives one reset.changed{swept} frame.
func TestFsxSweepResetTokensPublishesSweepFrames(t *testing.T) {
	s, mock := newTestServer(t)
	sub := s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: 1}, "9.9.9.1")
	defer s.unsubscribe(sub)

	mock.ExpectQuery(q("SELECT DISTINCT school_id FROM reset_tokens")).
		WillReturnRows(sqlmock.NewRows([]string{"school_id"}).
			AddRow(int64(1)).AddRow(nil))
	mock.ExpectExec(q("DELETE FROM reset_tokens WHERE source = 'SELF' AND (expires_at < NOW() OR used_at IS NOT NULL)")).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(q("DELETE FROM reset_tokens WHERE source = 'ADMIN' AND created_at < NOW() - INTERVAL 90 DAY")).
		WillReturnResult(sqlmock.NewResult(0, 0))

	s.sweepResetTokens(context.Background())

	require.NoError(t, mock.ExpectationsWereMet(), "the sweep must issue exactly the pinned SQL")
	require.Len(t, sub.ch, 2, "one sweep frame per touched school")
	f1 := <-sub.ch
	require.Equal(t, "reset.changed", f1.Entity)
	require.Equal(t, "swept", f1.Action)
	require.Equal(t, int64(1), f1.SchoolID)
	require.Equal(t, int64(1), f1.Rev)
	require.NotEmpty(t, f1.TS)
	f2 := <-sub.ch
	require.Equal(t, "reset.changed", f2.Entity, "NULL school rows sweep as broadcast frames")
	require.Equal(t, int64(0), f2.SchoolID)
	require.Equal(t, int64(1), f2.Rev)
}

// TestFsxSweepResetTokensFailurePaths: every failure logs its own janitor
// line and aborts BEFORE the DELETE (no partial deletion, no sweep frames).
func TestFsxSweepResetTokensFailurePaths(t *testing.T) {
	t.Run("select fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		sub := s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: 1}, "9.9.9.2")
		defer s.unsubscribe(sub)
		buf := FsxCaptureLog(t)
		mock.ExpectQuery(q("SELECT DISTINCT school_id FROM reset_tokens")).
			WillReturnError(errors.New("db down"))

		s.sweepResetTokens(context.Background())

		require.Contains(t, buf.String(), "janitor reset_tokens select")
		require.NotContains(t, buf.String(), "janitor reset_tokens delete",
			"a failed select must never fall through to the DELETE")
		require.Empty(t, sub.ch, "no sweep frames on failure")
	})

	t.Run("scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		buf := FsxCaptureLog(t)
		mock.ExpectQuery(q("SELECT DISTINCT school_id FROM reset_tokens")).
			WillReturnRows(sqlmock.NewRows([]string{"school_id"}).AddRow("not-an-id"))

		s.sweepResetTokens(context.Background())

		require.Contains(t, buf.String(), "janitor reset_tokens scan")
		require.NotContains(t, buf.String(), "janitor reset_tokens delete")
	})

	t.Run("rows iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		buf := FsxCaptureLog(t)
		mock.ExpectQuery(q("SELECT DISTINCT school_id FROM reset_tokens")).
			WillReturnRows(sqlmock.NewRows([]string{"school_id"}).
				AddRow(int64(1)).
				RowError(0, errors.New("row boom")))

		s.sweepResetTokens(context.Background())

		require.Contains(t, buf.String(), "janitor reset_tokens rows")
		require.NotContains(t, buf.String(), "janitor reset_tokens delete")
	})

	t.Run("delete fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		sub := s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: 1}, "9.9.9.3")
		defer s.unsubscribe(sub)
		buf := FsxCaptureLog(t)
		mock.ExpectQuery(q("SELECT DISTINCT school_id FROM reset_tokens")).
			WillReturnRows(sqlmock.NewRows([]string{"school_id"}).AddRow(int64(1)))
		mock.ExpectExec(q("DELETE FROM reset_tokens WHERE source = 'SELF' AND (expires_at < NOW() OR used_at IS NOT NULL)")).
			WillReturnError(errors.New("delete boom"))

		s.sweepResetTokens(context.Background())

		require.Contains(t, buf.String(), "janitor reset_tokens delete")
		require.Empty(t, sub.ch,
			"when the DELETE fails nothing was swept — schools must NOT get reset.changed{swept}")
	})
}

// ---------------------------------------------------------------------------
// sweepStalePhotos (files.go)
// ---------------------------------------------------------------------------

// TestFsxSweepStalePhotosRemovesOnlyDeletedRows: exact SQL+args per row; a
// row whose guarded DELETE reports 0 (finished copying since the select)
// keeps its file on disk.
func TestFsxSweepStalePhotosRemovesOnlyDeletedRows(t *testing.T) {
	s, mock := newTestServer(t)
	seed := func(rel string) string {
		p := filepath.Join(s.upload, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("partial"), 0o644))
		return p
	}
	winner := seed("photos/stale1.png")
	late := seed("photos/stale2.png")

	mock.ExpectQuery(q("SELECT id, path FROM photos")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "path"}).
			AddRow(int64(10), "photos/stale1.png").
			AddRow(int64(11), "photos/stale2.png"))
	mock.ExpectExec(q("DELETE FROM photos WHERE id = ? AND file_ready = FALSE")).
		WithArgs(int64(10)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("DELETE FROM photos WHERE id = ? AND file_ready = FALSE")).
		WithArgs(int64(11)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	s.sweepStalePhotos(context.Background())

	require.NoError(t, mock.ExpectationsWereMet())
	require.NoFileExists(t, winner, "deleted row's partial file must be removed")
	require.FileExists(t, late, "a row that finished copying (0 rows deleted) keeps its file")
}

// TestFsxSweepStalePhotosFailurePaths: per-row guard semantics — failures
// log, never delete files of rows whose DELETE failed, and later rows still
// get their turn.
func TestFsxSweepStalePhotosFailurePaths(t *testing.T) {
	t.Run("select fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		buf := FsxCaptureLog(t)
		mock.ExpectQuery(q("SELECT id, path FROM photos")).
			WillReturnError(errors.New("db down"))

		s.sweepStalePhotos(context.Background())

		require.Contains(t, buf.String(), "janitor stale photos select")
		require.NotContains(t, buf.String(), "janitor stale photos delete")
	})

	t.Run("scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		buf := FsxCaptureLog(t)
		mock.ExpectQuery(q("SELECT id, path FROM photos")).
			WillReturnRows(sqlmock.NewRows([]string{"id", "path"}).AddRow(int64(10), nil))

		s.sweepStalePhotos(context.Background())

		require.Contains(t, buf.String(), "janitor stale photos scan")
		require.NotContains(t, buf.String(), "janitor stale photos delete")
	})

	t.Run("rows iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		buf := FsxCaptureLog(t)
		mock.ExpectQuery(q("SELECT id, path FROM photos")).
			WillReturnRows(sqlmock.NewRows([]string{"id", "path"}).
				AddRow(int64(10), "photos/x.png").
				RowError(0, errors.New("row boom")))

		s.sweepStalePhotos(context.Background())

		require.Contains(t, buf.String(), "janitor stale photos rows")
		require.NotContains(t, buf.String(), "janitor stale photos delete")
	})

	t.Run("delete fails for one row only", func(t *testing.T) {
		s, mock := newTestServer(t)
		seed := func(rel string) string {
			p := filepath.Join(s.upload, filepath.FromSlash(rel))
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, []byte("partial"), 0o644))
			return p
		}
		stuck := seed("photos/stuck.png")
		done := seed("photos/done.png")
		buf := FsxCaptureLog(t)
		mock.ExpectQuery(q("SELECT id, path FROM photos")).
			WillReturnRows(sqlmock.NewRows([]string{"id", "path"}).
				AddRow(int64(10), "photos/stuck.png").
				AddRow(int64(11), "photos/done.png"))
		mock.ExpectExec(q("DELETE FROM photos WHERE id = ? AND file_ready = FALSE")).
			WithArgs(int64(10)).
			WillReturnError(errors.New("delete boom"))
		mock.ExpectExec(q("DELETE FROM photos WHERE id = ? AND file_ready = FALSE")).
			WithArgs(int64(11)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		s.sweepStalePhotos(context.Background())

		require.NoError(t, mock.ExpectationsWereMet(), "a failed row must not abort the rest")
		require.Contains(t, buf.String(), "janitor stale photos delete")
		require.FileExists(t, stuck, "the row whose DELETE failed keeps its file")
		require.NoFileExists(t, done, "the next row still gets swept")
	})

	t.Run("file removal fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		// A non-empty directory at the stale path cannot be os.Remove'd.
		dir := filepath.Join(s.upload, "photos", "ghost.png")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "child"), 0o755))
		buf := FsxCaptureLog(t)
		mock.ExpectQuery(q("SELECT id, path FROM photos")).
			WillReturnRows(sqlmock.NewRows([]string{"id", "path"}).
				AddRow(int64(10), "photos/ghost.png"))
		mock.ExpectExec(q("DELETE FROM photos WHERE id = ? AND file_ready = FALSE")).
			WithArgs(int64(10)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		s.sweepStalePhotos(context.Background())

		require.NoError(t, mock.ExpectationsWereMet())
		require.Contains(t, buf.String(), "janitor stale photo file",
			"an undeletable partial file must be logged, not silently dropped")
		require.DirExists(t, dir)
	})
}

// ---------------------------------------------------------------------------
// sweep (files.go) — the full janitor duty list
// ---------------------------------------------------------------------------

// TestFsxSweepRunsAllJanitorDuties pins the sweep orchestration: reset duty
// (with per-school publish), the three timestamp purges, the stale-photo
// duty (file removed with the row) and the orphan sweep (referenced files
// survive, unreferenced old files are removed).
func TestFsxSweepRunsAllJanitorDuties(t *testing.T) {
	s, mock := newTestServer(t)
	sub := s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: 1}, "9.9.9.4")
	defer s.unsubscribe(sub)

	old := time.Now().Add(-2 * orphanGrace)
	seed := func(rel string) string {
		p := filepath.Join(s.upload, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		require.NoError(t, os.Chtimes(p, old, old))
		return p
	}
	stale := seed("photos/stale1.png")
	keep := seed("frames/keep.png")
	orphan := seed("frames/orphan.png")

	mock.ExpectQuery(q("SELECT DISTINCT school_id FROM reset_tokens")).
		WillReturnRows(sqlmock.NewRows([]string{"school_id"}).AddRow(int64(1)))
	mock.ExpectExec(q("DELETE FROM reset_tokens WHERE source = 'SELF' AND (expires_at < NOW() OR used_at IS NOT NULL)")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("DELETE FROM reset_tokens WHERE source = 'ADMIN' AND created_at < NOW() - INTERVAL 90 DAY")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = expires_at")).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(q("impersonated_by IS NULL AND expires_at < NOW()")).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(q("DELETE FROM idempotency_keys WHERE created_at < NOW() - INTERVAL 24 HOUR")).
		WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectExec(q("DELETE FROM wa_outbox WHERE created_at < NOW() - INTERVAL 90 DAY")).
		WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectQuery(q("SELECT id, path FROM photos")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "path"}).
			AddRow(int64(10), "photos/stale1.png"))
	mock.ExpectExec(q("DELETE FROM photos WHERE id = ? AND file_ready = FALSE")).
		WithArgs(int64(10)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("UNION SELECT badge_image_snap FROM session_topics WHERE badge_image_snap IS NOT NULL")).
		WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("frames/keep.png"))
	buf := FsxCaptureLog(t)

	s.sweep()

	require.NoError(t, mock.ExpectationsWereMet(), "sweep must run every duty in order")
	f := <-sub.ch
	require.Equal(t, "reset.changed", f.Entity)
	require.Equal(t, "swept", f.Action)
	require.Equal(t, int64(1), f.SchoolID)
	require.Len(t, sub.ch, 0)
	require.NoFileExists(t, stale, "stale photo file swept with its row")
	require.FileExists(t, keep, "a file still referenced by a row survives the orphan sweep")
	require.NoFileExists(t, orphan, "old unreferenced upload residue removed")
	require.Contains(t, buf.String(), "janitor orphan removed")
	require.Contains(t, buf.String(), "frames/orphan.png")
}

// TestFsxSweepContinuesAfterExecErrors: each purge fails independently —
// every failure is logged and every remaining duty still runs.
func TestFsxSweepContinuesAfterExecErrors(t *testing.T) {
	s, mock := newTestServer(t)
	buf := FsxCaptureLog(t)
	mock.ExpectQuery(q("SELECT DISTINCT school_id FROM reset_tokens")).
		WillReturnRows(sqlmock.NewRows([]string{"school_id"}))
	mock.ExpectExec(q("DELETE FROM reset_tokens WHERE source = 'SELF' AND (expires_at < NOW() OR used_at IS NOT NULL)")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("DELETE FROM reset_tokens WHERE source = 'ADMIN' AND created_at < NOW() - INTERVAL 90 DAY")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = expires_at")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("impersonated_by IS NULL AND expires_at < NOW()")).
		WillReturnError(errors.New("refresh boom"))
	mock.ExpectExec(q("DELETE FROM idempotency_keys WHERE created_at < NOW() - INTERVAL 24 HOUR")).
		WillReturnError(errors.New("idem boom"))
	mock.ExpectExec(q("DELETE FROM wa_outbox WHERE created_at < NOW() - INTERVAL 90 DAY")).
		WillReturnError(errors.New("outbox boom"))
	mock.ExpectQuery(q("SELECT id, path FROM photos")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "path"}))
	mock.ExpectQuery(q("UNION SELECT badge_image_snap FROM session_topics WHERE badge_image_snap IS NOT NULL")).
		WillReturnRows(sqlmock.NewRows([]string{"path"}))

	s.sweep()

	require.NoError(t, mock.ExpectationsWereMet(),
		"one failing purge must not abort the duties after it")
	require.Contains(t, buf.String(), "janitor refresh_tokens")
	require.Contains(t, buf.String(), "janitor idempotency_keys")
	require.Contains(t, buf.String(), "janitor wa_outbox")
}

// ---------------------------------------------------------------------------
// startJanitor / StartBackground (files.go)
// ---------------------------------------------------------------------------

// TestFsxStartJanitorSweepsOnceImmediately: the janitor goroutine runs the
// full sweep at boot (crash-residue cleanup) and Stop ends it — the ticker
// period is an hour, so the immediate sweep is the observable execution.
func TestFsxStartJanitorSweepsOnceImmediately(t *testing.T) {
	s, mock := newTestServer(t)
	t.Cleanup(s.Stop)
	FsxExpectFullSweep(mock)

	s.startJanitor()

	require.Eventually(t, func() bool { return mock.ExpectationsWereMet() == nil },
		3*time.Second, 10*time.Millisecond,
		"startJanitor must run the complete sweep immediately at boot")
}

// TestFsxStartBackgroundRunsEveryBackgroundLoop: StartBackground launches
// janitor + WA watcher + stats tick; the janitor's initial sweep is the
// observable one, and Stop shuts every loop down idempotently.
func TestFsxStartBackgroundRunsEveryBackgroundLoop(t *testing.T) {
	s, mock := newTestServer(t)
	t.Cleanup(s.Stop)
	FsxExpectFullSweep(mock)

	s.StartBackground()

	require.Eventually(t, func() bool { return mock.ExpectationsWereMet() == nil },
		3*time.Second, 10*time.Millisecond,
		"StartBackground must launch the janitor (initial sweep executes)")
	s.Stop()
	s.Stop() // idempotent: closing the stop channel twice must not panic
}

// ---------------------------------------------------------------------------
// sweepOrphans (files.go) — branches the existing tests do not reach
// ---------------------------------------------------------------------------

// TestFsxSweepOrphansKeepsReferencedFiles: a file any row points at is never
// swept even when old (the existing test only exercises the empty-refs case).
func TestFsxSweepOrphansKeepsReferencedFiles(t *testing.T) {
	s, mock := newTestServer(t)
	old := time.Now().Add(-2 * orphanGrace)
	seed := func(name string) string {
		p := filepath.Join(s.upload, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		require.NoError(t, os.Chtimes(p, old, old))
		return p
	}
	keep := seed("frames/keep.png")
	orphan := seed("frames/orphan.png")
	buf := FsxCaptureLog(t)

	mock.ExpectQuery(q("UNION SELECT badge_image_snap FROM session_topics WHERE badge_image_snap IS NOT NULL")).
		WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("frames/keep.png"))

	s.sweepOrphans()

	require.NoError(t, mock.ExpectationsWereMet())
	require.FileExists(t, keep, "referenced files survive the orphan sweep")
	require.NoFileExists(t, orphan, "unreferenced old files are still swept")
	require.Contains(t, buf.String(), "frames/orphan.png")
}

// TestFsxSweepOrphansAbortsOnRefQueryError: without the reference list the
// sweep must NOT delete anything (crash residue is preferable to data loss).
func TestFsxSweepOrphansAbortsOnRefQueryError(t *testing.T) {
	s, mock := newTestServer(t)
	old := time.Now().Add(-2 * orphanGrace)
	orphan := filepath.Join(s.upload, "orphan.jpg")
	require.NoError(t, os.WriteFile(orphan, []byte("x"), 0o644))
	require.NoError(t, os.Chtimes(orphan, old, old))
	buf := FsxCaptureLog(t)

	mock.ExpectQuery(q("UNION SELECT badge_image_snap FROM session_topics WHERE badge_image_snap IS NOT NULL")).
		WillReturnError(errors.New("db down"))

	s.sweepOrphans()

	require.Contains(t, buf.String(), "janitor referenced paths")
	require.FileExists(t, orphan, "no reference list → no deletion at all")
}

// TestFsxSweepOrphansSkipsFreshEnvWithoutRoot: when the upload dir was never
// written the sweep returns silently BEFORE any DB access.
func TestFsxSweepOrphansSkipsFreshEnvWithoutRoot(t *testing.T) {
	s, _ := newTestServer(t)
	s.upload = filepath.Join(t.TempDir(), "never-created")
	buf := FsxCaptureLog(t)

	s.sweepOrphans() // no expectations set: any DB call would log an error

	require.NotContains(t, buf.String(), "janitor referenced paths",
		"a missing upload root must return before the reference query")
	require.NotContains(t, buf.String(), "janitor")
}

// ---------------------------------------------------------------------------
// referencedPaths (files.go)
// ---------------------------------------------------------------------------

// TestFsxReferencedPathsUnionsEveryFileColumn: all five file-bearing tables
// land in one map.
func TestFsxReferencedPathsUnionsEveryFileColumn(t *testing.T) {
	s, mock := newTestServer(t)
	mock.ExpectQuery(q("UNION SELECT badge_image_snap FROM session_topics WHERE badge_image_snap IS NOT NULL")).
		WillReturnRows(sqlmock.NewRows([]string{"path"}).
			AddRow("photos/a.png").
			AddRow("frames/b.png").
			AddRow("badges/prog.png").
			AddRow("badges/topic.png").
			AddRow("snaps/stid9.png"))

	refs, err := s.referencedPaths()

	require.NoError(t, err)
	require.Equal(t, map[string]bool{
		"photos/a.png":     true,
		"frames/b.png":     true,
		"badges/prog.png":  true,
		"badges/topic.png": true,
		"snaps/stid9.png":  true,
	}, refs)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestFsxReferencedPathsPropagatesQueryAndScanErrors: callers (the orphan
// sweep) receive the raw error and a nil map — never a partial set.
func TestFsxReferencedPathsPropagatesQueryAndScanErrors(t *testing.T) {
	t.Run("query error", func(t *testing.T) {
		s, mock := newTestServer(t)
		sentinel := errors.New("db down")
		mock.ExpectQuery(q("UNION SELECT badge_image_snap FROM session_topics WHERE badge_image_snap IS NOT NULL")).
			WillReturnError(sentinel)

		refs, err := s.referencedPaths()

		require.ErrorIs(t, err, sentinel)
		require.Nil(t, refs)
	})

	t.Run("scan error", func(t *testing.T) {
		s, mock := newTestServer(t)
		mock.ExpectQuery(q("UNION SELECT badge_image_snap FROM session_topics WHERE badge_image_snap IS NOT NULL")).
			WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow(nil))

		refs, err := s.referencedPaths()

		require.Error(t, err)
		require.Nil(t, refs)
	})
}

// ---------------------------------------------------------------------------
// durableWrite / storeImage (files.go)
// ---------------------------------------------------------------------------

// TestFsxDurableWritePathAndFailureHandling drives every reachable failure
// branch with observable disk state.
func TestFsxDurableWritePathAndFailureHandling(t *testing.T) {
	t.Run("path escapes upload root", func(t *testing.T) {
		root := t.TempDir()
		_, err := durableWrite(root, "../escaped.txt", strings.NewReader("evil"))
		require.ErrorContains(t, err, "path escapes upload root")
		require.NoFileExists(t, filepath.Join(filepath.Dir(root), "escaped.txt"))
	})

	t.Run("unlimited copy writes exact bytes", func(t *testing.T) {
		root := t.TempDir()
		payload := bytes.Repeat([]byte{0xAB}, 3000)
		n, err := durableWrite(root, "a/b/c.bin", bytes.NewReader(payload))
		require.NoError(t, err)
		require.Equal(t, int64(len(payload)), n, "no size limit — every byte lands")
		stored, err := os.ReadFile(filepath.Join(root, "a", "b", "c.bin"))
		require.NoError(t, err)
		require.Equal(t, payload, stored)
		entries, err := os.ReadDir(filepath.Join(root, "a", "b"))
		require.NoError(t, err)
		require.Len(t, entries, 1, "the atomic rename consumed the .tmp file")
	})

	t.Run("parent dir is a plain file", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "blk"), []byte("x"), 0o644))
		_, err := durableWrite(root, "blk/x.png", strings.NewReader("p"))
		require.Error(t, err, "MkdirAll over a regular file must fail")
	})

	t.Run("tmp path is blocked", func(t *testing.T) {
		root := t.TempDir()
		// A directory squatting on the .tmp name blocks OpenFile.
		require.NoError(t, os.MkdirAll(filepath.Join(root, "sub", "x.png.tmp"), 0o755))
		_, err := durableWrite(root, "sub/x.png", strings.NewReader("p"))
		require.Error(t, err)
		require.NoFileExists(t, filepath.Join(root, "sub", "x.png"))
	})

	t.Run("rename over a live directory fails and cleans up", func(t *testing.T) {
		root := t.TempDir()
		victim := filepath.Join(root, "sub", "child.txt")
		require.NoError(t, os.MkdirAll(filepath.Dir(victim), 0o755))
		require.NoError(t, os.WriteFile(victim, []byte("keep"), 0o644))
		_, err := durableWrite(root, "sub", strings.NewReader("payload"))
		require.Error(t, err, "renaming the tmp file over a non-empty directory must fail")
		require.NoFileExists(t, root+string(filepath.Separator)+"sub.tmp",
			"a failed rename must remove the tmp file")
		require.FileExists(t, victim, "the existing directory tree must stay untouched")
	})

	t.Run("copy error propagates and removes tmp", func(t *testing.T) {
		root := t.TempDir()
		sentinel := errors.New("fsx io broke")
		src := io.MultiReader(strings.NewReader("0123456789"), FsxErrReader{Err: sentinel})
		n, err := durableWrite(root, "sub/x.bin", src)
		require.ErrorIs(t, err, sentinel)
		require.Zero(t, n)
		require.NoFileExists(t, filepath.Join(root, "sub", "x.bin"))
		require.NoFileExists(t, filepath.Join(root, "sub", "x.bin.tmp"))
	})
}

// TestFsxStoreImageMapsDiskAndIOErrors: ENOSPC surfaces as the validation
// mapping (errRejected + "disk full"); any other write error propagates
// unchanged — and neither leaves a durable file behind.
func TestFsxStoreImageMapsDiskAndIOErrors(t *testing.T) {
	t.Run("disk full becomes errRejected", func(t *testing.T) {
		s, _ := newTestServer(t)
		src := io.MultiReader(bytes.NewReader(pngHead), FsxErrReader{Err: syscall.ENOSPC})
		_, err := s.storeImage("frames", src, "image/png")
		require.ErrorIs(t, err, errRejected)
		require.ErrorContains(t, err, "disk full")
		FsxRequireNoImageFiles(t, s, "frames")
	})

	t.Run("write errors propagate unchanged", func(t *testing.T) {
		s, _ := newTestServer(t)
		sentinel := errors.New("fsx write broke")
		src := io.MultiReader(bytes.NewReader(pngHead), FsxErrReader{Err: sentinel})
		_, err := s.storeImage("frames", src, "image/png")
		require.ErrorIs(t, err, sentinel)
		require.NotErrorIs(t, err, errRejected)
		FsxRequireNoImageFiles(t, s, "frames")
	})
}

// FsxRequireNoImageFiles asserts the upload subdirectory holds no files (the
// tmp cleanup must never leave partial images behind).
func FsxRequireNoImageFiles(t *testing.T, s *Server, dir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.upload, dir))
	if os.IsNotExist(err) {
		return // rejected before the directory was even created
	}
	require.NoError(t, err)
	require.Empty(t, entries)
}

// ---------------------------------------------------------------------------
// randBytes (files.go)
// ---------------------------------------------------------------------------

// TestFsxRandBytesProducesResetCodeBytes pins the 4-byte contract the forgot
// flow hex-encodes into an 8-char reset code, with fresh entropy per call.
func TestFsxRandBytesProducesResetCodeBytes(t *testing.T) {
	a, err := randBytes(4)
	require.NoError(t, err)
	require.Len(t, a, 4, "reset codes draw exactly 4 random bytes")
	require.Len(t, hex.EncodeToString(a), 8, "hex encoding yields the 8-char wire code")
	b, err := randBytes(4)
	require.NoError(t, err)
	require.NotEqual(t, a, b, "successive codes must not collide")

	zero, err := randBytes(0)
	require.NoError(t, err)
	require.Empty(t, zero)
}

// ---------------------------------------------------------------------------
// startStatsTick (stream.go)
// ---------------------------------------------------------------------------

// TestFsxStartStatsTickLaunchesAndExitsOnStop: the tick itself is a 7 s
// server-side cadence, so the observable contract is one background
// goroutine that exists while running and leaves on Stop (no leak). The
// deadlines are deliberately generous: the goroutine's scheduler latency
// under a fully-loaded CI box is not the contract being pinned here.
func TestFsxStartStatsTickLaunchesAndExitsOnStop(t *testing.T) {
	s, _ := newTestServer(t)
	// runtime.Stack(all=true) TRUNCATES at len(buf); the merged suite leaves
	// enough goroutines around to blow past a fixed 1 MiB, so grow until the
	// dump is complete (n < len(buf) means "not truncated").
	stacks := func() string {
		for size := 1 << 20; ; size *= 4 {
			buf := make([]byte, size)
			n := runtime.Stack(buf, true)
			if n < len(buf) {
				return string(buf[:n])
			}
		}
	}

	s.startStatsTick()
	require.Eventually(t, func() bool { return strings.Contains(stacks(), "(*Server).startStatsTick") },
		30*time.Second, 20*time.Millisecond,
		"startStatsTick must launch its aggregator goroutine")

	s.Stop()
	require.Eventually(t, func() bool { return !strings.Contains(stacks(), "(*Server).startStatsTick") },
		30*time.Second, 20*time.Millisecond,
		"the stats tick goroutine must exit on Stop")
}

// ---------------------------------------------------------------------------
// stream hub branches (stream.go)
// ---------------------------------------------------------------------------

// TestFsxStreamDeliverClosedSubscriberIsNoop: deliver onto an unsubscribed
// connection must neither panic nor deliver.
func TestFsxStreamDeliverClosedSubscriberIsNoop(t *testing.T) {
	s, _ := newTestServer(t)
	sub := s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: 1}, "9.8.7.1")
	s.unsubscribe(sub)

	f := Frame{SchoolID: 1, Entity: "program.changed", Action: "updated"}
	s.deliver(sub, &f)

	_, open := <-sub.ch
	require.False(t, open, "a closed subscription stays closed and frameless")
}

// TestFsxStreamDeliverDropsOnSlowSubscriber: a subscriber that stopped
// draining its buffer gets the frame DROPPED (slog'd), never blocked on.
func TestFsxStreamDeliverDropsOnSlowSubscriber(t *testing.T) {
	s, _ := newTestServer(t)
	sub := s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: 1}, "9.8.7.2")
	defer s.unsubscribe(sub)
	f := Frame{SchoolID: 1, Entity: "program.changed", Action: "updated"}

	for i := range ringEvents {
		f.Rev = int64(i + 1)
		s.deliver(sub, &f)
	}
	require.Len(t, sub.ch, ringEvents)

	buf := FsxCaptureLog(t)
	f.Rev = ringEvents + 1
	s.deliver(sub, &f)

	require.Len(t, sub.ch, ringEvents, "the overflow frame is dropped, not queued")
	require.Contains(t, buf.String(), "stream frame dropped (slow subscriber)")
}

// TestFsxStreamPublishPublicWarnsAndStillPublishes: routing keys are a
// logged contract, not a gate — the frame still reaches the hub.
func TestFsxStreamPublishPublicWarnsAndStillPublishes(t *testing.T) {
	s, _ := newTestServer(t)
	buf := FsxCaptureLog(t)

	s.publishPublic(Frame{Entity: "report.changed", Action: "created"})

	require.Contains(t, buf.String(), "publishPublic frame missing routing keys")
	s.hubMu.Lock()
	rev := s.revs[0]
	s.hubMu.Unlock()
	require.Equal(t, int64(1), rev, "the frame is still published (log-only contract)")
}

// TestFsxStreamRefreshScopeKeepsSnapshotOnError: a failed scope refresh
// logs and keeps the previous snapshot (never an empty/zeroed scope).
func TestFsxStreamRefreshScopeKeepsSnapshotOnError(t *testing.T) {
	s, _ := newTestServer(t)
	s.registerScopeProvider(func(context.Context, int64, string, int64) (ScopeSnapshot, error) {
		return ScopeSnapshot{}, errors.New("scope db down")
	})
	buf := FsxCaptureLog(t)
	sc := &ScopeSnapshot{
		OwnGroupIDs:     []int64{2},
		OwnedSessionIDs: []int64{5},
		ChildIDs:        []int64{30},
	}

	s.refreshScope(sc)

	require.Contains(t, buf.String(), "stream scope refresh failed")
	require.Equal(t, []int64{2}, sc.OwnGroupIDs)
	require.Equal(t, []int64{5}, sc.OwnedSessionIDs)
	require.Equal(t, []int64{30}, sc.ChildIDs)
}

// TestFsxStreamUnknownRoleFramesAlwaysFiltered: roles outside the
// SA/ADMIN/FASILITATOR triangle never pass the binding filter.
func TestFsxStreamUnknownRoleFramesAlwaysFiltered(t *testing.T) {
	s, _ := newTestServer(t)
	sub := &subscription{
		scope: ScopeSnapshot{Role: "TEACHER", SchoolID: 1},
		ch:    make(chan Frame, 1),
	}
	school := Frame{SchoolID: 1, Entity: "program.changed", EntityID: 4}
	require.False(t, s.frameAllowedLocked(sub, &school),
		"an unknown role must never receive school frames")
	broadcast := Frame{SchoolID: 0, Entity: "stats.changed"}
	require.False(t, s.frameAllowedLocked(sub, &broadcast),
		"an unknown role must never receive broadcasts")
}

// TestFsxStreamPublishEvictsWindowAndCapsRing: the ring drops frames older
// than ringWindow on the next publish AND stays capped at ringEvents.
func TestFsxStreamPublishEvictsWindowAndCapsRing(t *testing.T) {
	s, _ := newTestServer(t)

	// Window eviction: a pre-aged entry makes the eviction loop advance.
	s.hubMu.Lock()
	s.revs[9] = 1
	s.rings[9] = []Frame{{Rev: 1, Entity: "stale.changed",
		at: time.Now().Add(-ringWindow - time.Minute)}}
	s.hubMu.Unlock()
	s.publish(9, Frame{Entity: "program.changed", EntityID: 4, Action: "created"})
	s.hubMu.Lock()
	windowRing := append([]Frame(nil), s.rings[9]...)
	windowRev := s.revs[9]
	s.hubMu.Unlock()
	require.Equal(t, int64(2), windowRev)
	require.Len(t, windowRing, 1, "frames older than ringWindow are evicted on publish")
	require.Equal(t, int64(2), windowRing[0].Rev)

	// Cap: one past the cap keeps only the newest ringEvents frames.
	for range ringEvents + 1 {
		s.publish(8, Frame{Entity: "program.changed", Action: "tick"})
	}
	s.hubMu.Lock()
	capped := append([]Frame(nil), s.rings[8]...)
	cappedRev := s.revs[8]
	s.hubMu.Unlock()
	require.Equal(t, int64(ringEvents)+1, cappedRev, "revs keep counting past the ring cap")
	require.Len(t, capped, ringEvents, "the ring caps at ringEvents frames")
	require.Equal(t, int64(2), capped[0].Rev, "only the oldest overflow frame was dropped")
}

// ---------------------------------------------------------------------------
// Handshake guards over HTTP (stream.go)
// ---------------------------------------------------------------------------

// TestFsxStreamGuardFASFailsDBOnScopeError: when the FAS scope provider
// errors at handshake the stream fails closed with DB_UNAVAILABLE.
func TestFsxStreamGuardFASFailsDBOnScopeError(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	s.registerScopeProvider(func(context.Context, int64, string, int64) (ScopeSnapshot, error) {
		return ScopeSnapshot{}, errors.New("scope db down")
	})
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
	h := bearer(mintAccess(t, s, fasUserRow()))
	h["X-School-ID"] = "1"

	res := doReq(t, app, "GET", "/api/stream", "", h)

	requireErrEnvelope(t, res, CodeDBUnavailable)
	require.Contains(t, res.header.Get("Content-Type"), "application/json",
		"a failed handshake must not open a stream")
}

// TestFsxStreamPublicGuardRejectsMissingToken: no ?token= → INVALID_TOKEN
// before any DB access (identical envelope shape, JSON content type).
func TestFsxStreamPublicGuardRejectsMissingToken(t *testing.T) {
	s, _ := newTestServer(t)
	app := newTestApp(t, s)

	res := doReq(t, app, "GET", "/public/stream", "", nil)

	requireErrEnvelope(t, res, CodeTokenInvalid)
	require.Contains(t, res.header.Get("Content-Type"), "application/json")
}

// TestFsxStreamPublicGuardFailsDBUnavailable: DB failures in either link
// table map to DB_UNAVAILABLE (not a token oracle).
func TestFsxStreamPublicGuardFailsDBUnavailable(t *testing.T) {
	t.Run("public_links failure", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
			WithArgs("tok").WillReturnError(errors.New("db down"))

		res := doReq(t, app, "GET", "/public/stream?token=tok", "", nil)

		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("consent_links failure", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
			WithArgs("tok").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
			WithArgs("tok").WillReturnError(errors.New("db down"))

		res := doReq(t, app, "GET", "/public/stream?token=tok", "", nil)

		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestFsxStreamPublicGuardRateLimitedAtCap: a valid token at the connection
// cap is refused with RATE_LIMITED + Retry-After — the stream never opens.
func TestFsxStreamPublicGuardRateLimitedAtCap(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	// Distinct IPs + spread schools: seeding itself must not trip
	// subscribe's in-subscribe per-IP/per-school re-checks (F-B10).
	for i := range maxStreamsPerReplica {
		s.subscribe(ScopeSnapshot{Public: true, Kind: "REPORT", SchoolID: int64(i%5 + 7)},
			fmt.Sprintf("8.8.8.%d", i))
	}
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok").
		WillReturnRows(sqlmock.NewRows(
			[]string{"school_id", "session_id", "session_topic_id", "child_id", "kind"}).
			AddRow(int64(7), int64(2), nil, int64(30), "REPORT"))

	res := doReq(t, app, "GET", "/public/stream?token=tok", "", nil)

	requireErrEnvelope(t, res, CodeRateLimited)
	require.Equal(t, "30", res.header.Get("Retry-After"))
	require.Contains(t, res.header.Get("Content-Type"), "application/json")
}

// ---------------------------------------------------------------------------
// Live streams (serveStream / publishPublic / replay over the wire)
// ---------------------------------------------------------------------------

// TestFsxStreamServeStreamWithoutScopeNeverSubscribes: the defensive guard
// at the top of serveStream — a stream handler reached without its guard
// (no subscription installed in Locals) opens nothing.
func TestFsxStreamServeStreamWithoutScopeNeverSubscribes(t *testing.T) {
	s, _ := newTestServer(t)
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Use(recover.New())
	app.Get("/fsx-noscope", func(c fiber.Ctx) error { return s.serveStream(c, nil) })

	res := doReq(t, app, "GET", "/fsx-noscope", "", nil)

	require.Equal(t, 200, res.status)
	require.Empty(t, s.subs, "a handshake without an installed scope must never subscribe")
}

// TestFsxStreamServeStreamEndsOnServerUnsubscribe: closing the subscription
// server-side terminates the SSE response (the !open branch).
func TestFsxStreamServeStreamEndsOnServerUnsubscribe(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	base := startListenerApp(t, app)
	t.Cleanup(func() {
		s.Stop()
		app.Shutdown() //nolint:errcheck // shutdown after Stop is best-effort
	})
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(sqlActiveRefreshSession()).WithArgs(int64(1)).
		WillReturnRows(sqlRefreshAliveRows())

	respCh, sub := FsxStartStream(t, s, base+"/api/stream",
		map[string]string{"Authorization": "Bearer " + mintAccess(t, s, saUserRow()),
			"X-School-ID": "1"},
		func(sub *subscription) bool { return !sub.scope.Public && sub.scope.Role == "SA" })

	s.unsubscribe(sub)

	resp := FsxAwaitResp(t, respCh)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	events := startSSEReader(resp.Body)
	select {
	case _, ok := <-events:
		require.False(t, ok, "the SSE response must end once the subscription closes")
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not close after the server closed the subscription")
	}
}

// TestFsxStreamServeStreamEndsOnWriteFailure: a client that vanishes turns
// the next event write into an error, which ends serveStream (unsubscribe on
// return) instead of spinning forever.
func TestFsxStreamServeStreamEndsOnWriteFailure(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	base := startListenerApp(t, app)
	t.Cleanup(func() {
		s.Stop()
		app.Shutdown() //nolint:errcheck // shutdown after Stop is best-effort
	})
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(sqlActiveRefreshSession()).WithArgs(int64(1)).
		WillReturnRows(sqlRefreshAliveRows())

	respCh, _ := FsxStartStream(t, s, base+"/api/stream",
		map[string]string{"Authorization": "Bearer " + mintAccess(t, s, saUserRow()),
			"X-School-ID": "1"},
		func(sub *subscription) bool { return !sub.scope.Public })
	s.publish(1, Frame{Entity: "program.changed", Action: "flush"}) // forces the header flush
	resp := FsxAwaitResp(t, respCh)

	_ = resp.Body.Close() // client vanishes without draining
	for range 2 * ringEvents {
		s.publish(1, Frame{Entity: "program.changed", Action: "updated"})
	}

	require.Eventually(t, func() bool {
		s.hubMu.Lock()
		defer s.hubMu.Unlock()
		return len(s.subs) == 0
	}, 5*time.Second, 20*time.Millisecond,
		"a write failure on a vanished client must end serveStream")
}

// TestFsxStreamPublicLinkRevokedClosesStream: a REPORT token receives its
// terminal link.revoked and the stream terminates right after delivery.
func TestFsxStreamPublicLinkRevokedClosesStream(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	base := startListenerApp(t, app)
	t.Cleanup(func() {
		s.Stop()
		app.Shutdown() //nolint:errcheck // shutdown after Stop is best-effort
	})
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-report").
		WillReturnRows(sqlmock.NewRows(
			[]string{"school_id", "session_id", "session_topic_id", "child_id", "kind"}).
			AddRow(int64(1), int64(2), nil, int64(30), "REPORT"))

	respCh, _ := FsxStartStream(t, s, base+"/public/stream?token=tok-report", nil,
		func(sub *subscription) bool { return sub.scope.Public && sub.scope.Token == "tok-report" })

	sid := int64(2)
	s.publishPublic(Frame{Entity: "link.revoked", SchoolID: 1, SessionID: &sid,
		ChildID: 30, Action: "revoked", Token: "tok-report"})

	resp := FsxAwaitResp(t, respCh)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	events := startSSEReader(resp.Body)

	ev := nextEvent(t, events)
	require.Equal(t, "link.revoked", ev.name)
	require.Contains(t, ev.data, `"entity":"link.revoked"`)
	select {
	case _, ok := <-events:
		require.False(t, ok, "the revoked token's stream must terminate after delivery")
	case <-time.After(3 * time.Second):
		t.Fatal("revoked stream did not close")
	}
}

// TestFsxStreamReplayedLinkRevokedTerminatesReconnect (F-B11): a stored
// link.revoked frame replayed via Last-Event-ID must mark the connection
// terminal exactly like live delivery — the stream ends right after the
// revoke, and the ring's newer frames behind it are never written.
func TestFsxStreamReplayedLinkRevokedTerminatesReconnect(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	base := startListenerApp(t, app)
	t.Cleanup(func() {
		s.Stop()
		app.Shutdown() //nolint:errcheck // shutdown after Stop is best-effort
	})
	sid := int64(2)
	// Ring: rev 1 is the terminal revoke, rev 2 a newer (non-terminal) frame
	// that must never be replayed once the revoke was.
	s.publishPublic(Frame{Entity: "link.revoked", SchoolID: 1, SessionID: &sid,
		ChildID: 30, Action: "revoked", Token: "tok-report"})
	s.publishPublic(Frame{Entity: "report.changed", SchoolID: 1, SessionID: &sid,
		ChildID: 30, Action: "updated"})
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-report").
		WillReturnRows(sqlmock.NewRows(
			[]string{"school_id", "session_id", "session_topic_id", "child_id", "kind"}).
			AddRow(int64(1), int64(2), nil, int64(30), "REPORT"))

	resp, events := connectSSEPublic(t, base, "tok-report", "0")
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	ev := nextEvent(t, events)
	require.Equal(t, "link.revoked", ev.name)
	require.Equal(t, "1", ev.id)
	select {
	case extra, ok := <-events:
		require.False(t, ok,
			"the replayed terminal frame must close the stream before ring rev 2 (got %q, open=%v)",
			extra.name, ok)
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not close after replaying the terminal link.revoked")
	}
}

// TestFsxStreamConsentTokenScopedToConsentEvents: a consent-link token
// opens the stream but never sees report.changed — only consent.ack.
func TestFsxStreamConsentTokenScopedToConsentEvents(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	base := startListenerApp(t, app)
	t.Cleanup(func() {
		s.Stop()
		app.Shutdown() //nolint:errcheck // shutdown after Stop is best-effort
	})
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-consent").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
		WithArgs("tok-consent").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id", "child_id"}).
			AddRow(int64(1), int64(2), int64(30)))

	respCh, _ := FsxStartStream(t, s, base+"/public/stream?token=tok-consent", nil,
		func(sub *subscription) bool { return sub.scope.Public && sub.scope.Kind == "CONSENT" })

	sid := int64(2)
	s.publishPublic(Frame{Entity: "consent.ack", SchoolID: 1, SessionID: &sid,
		ChildID: 30, Action: "acked", Decision: "APPROVED"})
	resp := FsxAwaitResp(t, respCh)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	events := startSSEReader(resp.Body)

	ev := nextEvent(t, events)
	require.Equal(t, "consent.ack", ev.name)
	require.Contains(t, ev.data, `"decision":"APPROVED"`)

	s.publishPublic(Frame{Entity: "report.changed", SchoolID: 1, SessionID: &sid,
		ChildID: 30, Action: "updated"})
	expectNoEvent(t, events) // a consent token must never receive report.changed

	FsxDrainSSE(resp, events)
}

// TestFsxStreamReplayLogsRingOverflowOnce: Last-Event-ID below the oldest
// surviving ring rev is an unclosable gap — logged exactly once, on the
// first frame that survives the scope filter, and replay resumes at the
// oldest rev the ring still covers.
func TestFsxStreamReplayLogsRingOverflowOnce(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	base := startListenerApp(t, app)
	t.Cleanup(func() {
		s.Stop()
		app.Shutdown() //nolint:errcheck // shutdown after Stop is best-effort
	})
	for range ringEvents + 7 { // 107 publishes → the ring keeps revs 8..107
		s.publish(4, Frame{Entity: "program.changed", Action: "tick"})
	}
	buf := FsxCaptureLog(t)
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(sqlActiveRefreshSession()).WithArgs(int64(1)).
		WillReturnRows(sqlRefreshAliveRows())

	resp, events := connectSSE(t, base, mintAccess(t, s, saUserRow()), "4", "6")

	ev := nextEvent(t, events)
	require.Equal(t, "8", ev.id, "replay resumes at the oldest rev the ring still covers")
	require.Equal(t, 1, strings.Count(buf.String(), "stream replay gap: ring overflow"),
		"the gap is logged exactly once per replay")

	FsxDrainSSE(resp, events)
}

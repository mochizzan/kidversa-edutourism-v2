package internal

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	uploadDirPerm = 0o755
	janitorPeriod = time.Hour
	// orphanGrace keeps freshly written files safe: a single upload writes the
	// file durable FIRST, then the row — anything younger than this is never
	// treated as orphaned.
	orphanGrace = time.Hour
)

// randHex returns 32 hex chars from crypto/rand (16 bytes): upload filenames
// and refresh jtis. Link tokens do NOT use it — they need 32 bytes (below).
func randHex() (string, error) {
	raw, err := randBytes(16)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// randTokenHex returns 64 hex chars from crypto/rand (32 bytes): the
// consent_links / public_links tokens (05 §2 CHAR(64)).
func randTokenHex() (string, error) {
	raw, err := randBytes(32)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// randBytes is crypto/rand with a clear name for callers (4 bytes = 8-char
// reset codes).
func randBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("crypto/rand: %w", err)
	}
	return buf, nil
}

// extForFormat maps a VERIFIED image format (image.DecodeConfig's format
// name) to the stored extension — recomputed from the real bytes, never the
// client declaration (spec 09 decision 3: "extension and recorded mime are
// recomputed from the OUTPUT").
func extForFormat(format string) string {
	if format == "jpeg" {
		return "jpg"
	}
	return format
}

// durableWrite writes rel (slash-separated, confined to root) through temp
// file + fsync + atomic rename — the durable half of the honest-file rule
// (05 §5). Every byte of src is written (no size cap — Pembaruan 2026-10-11:
// upload limits are client-side only).
func durableWrite(root, rel string, src io.Reader) (int64, error) {
	full := filepath.Join(root, filepath.FromSlash(rel))
	if !strings.HasPrefix(full, filepath.Clean(root)+string(filepath.Separator)) {
		return 0, fmt.Errorf("path escapes upload root: %s", rel)
	}
	if err := os.MkdirAll(filepath.Dir(full), uploadDirPerm); err != nil {
		return 0, err
	}
	tmp := full + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	written, err := io.Copy(f, src)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, full); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return written, nil
}

// storeImage is the ONE code path for every stored image (frames, badges,
// session/child photos, profile photos — spec 09 decision 2/3):
//
//  1. bounded read (≤50 MB — the over-size slip-through bound),
//  2. REAL image validation: decode-config + declared type cross-check
//     (never trusting the multipart Content-Type) — WebP/non-image/corrupt
//     bytes are REJECTED with errRejected → failStore → 200 VALIDATION,
//     replacing the 2026-10-11 anywhere-accepts store-as-`.bin` behavior,
//  3. honest-file write: ≤ compressTriggerBytes (10 MB) stores byte-for-byte
//     as received; larger files go through the format-preserving compression
//     ladder under the cap-5 semaphore before the write.
//
// The stored extension is recomputed from the VERIFIED data (nothing lies
// about its type); the honest-file rule (05 §5): temp + fsync + atomic
// rename. errRejected maps to VALIDATION; other errors are 500 INTERNAL.
func (s *Server) storeImage(dir string, src io.Reader, declaredType string) (string, error) {
	data, err := readUploadPayload(src)
	if err != nil {
		if errors.Is(err, syscall.ENOSPC) {
			return "", fmt.Errorf("%w: disk full", errRejected)
		}
		return "", err
	}
	format, err := validateImageBytes(data, declaredType)
	if err != nil {
		return "", err
	}
	// >10 MB: the format-preserving compression ladder (spec 09 decision 3)
	// behind the cap-5 semaphore (decision 4). ≤10 MB stores byte-for-byte as
	// received. compressImage only returns an error for an unknown format —
	// impossible here (validateImageBytes just whitelisted it) — and NEVER
	// rejects for incompressibility.
	if int64(len(data)) > compressTriggerBytes {
		var compressed []byte
		if err := s.withCompressSlot(func() error {
			var cerr error
			compressed, cerr = compressImage(data, format, compressTriggerBytes)
			return cerr
		}); err != nil {
			return "", err
		}
		data = compressed
	}
	name, err := randHex()
	if err != nil {
		return "", err
	}
	rel := dir + "/" + name + "." + extForFormat(format)
	if _, err := durableWrite(s.upload, rel, bytes.NewReader(data)); err != nil {
		if errors.Is(err, syscall.ENOSPC) {
			return "", fmt.Errorf("%w: disk full", errRejected)
		}
		return "", err
	}
	return rel, nil
}

// StartBackground launches the in-process janitor, the OpenWA status watcher
// and the stats.changed aggregator tick (02 §2c). main.go owns this call and
// Stop for graceful shutdown.
func (s *Server) StartBackground() {
	s.startJanitor()
	s.startWaWatcher()
	s.startStatsTick()
}

// Stop signals every background goroutine and open stream to exit. Idempotent.
func (s *Server) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
}

// startJanitor sweeps hourly (one ticker goroutine, 05 §5); failures are
// slog-only and never crash boot. The first sweep runs immediately to clear
// crash residue from the previous run.
func (s *Server) startJanitor() {
	go func() {
		s.sweep()
		t := time.NewTicker(janitorPeriod)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.sweep()
			case <-s.stop:
				return
			}
		}
	}()
}

// sweep runs every janitor duty; each step fails independently and only logs.
func (s *Server) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.sweepResetTokens(ctx)
	// Impersonation ledger: stamp natural expiry so `ended_at` is well-defined
	// for every session, then purge ordinary rows only. Ledger rows are kept
	// 90 days (spec §7.3), matching wa_outbox.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked_at = expires_at
		 WHERE impersonated_by IS NOT NULL AND revoked_at IS NULL AND expires_at < NOW()`); err != nil {
		slog.Error("janitor refresh_tokens stamp", "err", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM refresh_tokens
		 WHERE (impersonated_by IS NULL AND expires_at < NOW())
		    OR created_at < NOW() - INTERVAL 90 DAY`); err != nil {
		slog.Error("janitor refresh_tokens", "err", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE created_at < NOW() - INTERVAL 24 HOUR`); err != nil {
		slog.Error("janitor idempotency_keys", "err", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM wa_outbox WHERE created_at < NOW() - INTERVAL 90 DAY`); err != nil {
		slog.Error("janitor wa_outbox", "err", err)
	}
	s.sweepStalePhotos(ctx)
	s.sweepOrphans()
}

// sweepResetTokens purges expired/used SELF-service reset codes (05 §5) and
// signals the reset dashboard for every school it touched (02 §2c
// reset.changed). ADMIN ledger rows never appear on the dashboard and are
// kept 90 days (spec §7.3).
func (s *Server) sweepResetTokens(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT school_id FROM reset_tokens
		 WHERE source = 'SELF' AND (expires_at < NOW() OR used_at IS NOT NULL)`)
	if err != nil {
		slog.Error("janitor reset_tokens select", "err", err)
		return
	}
	var schools []int64
	for rows.Next() {
		var t sql.NullInt64
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			slog.Error("janitor reset_tokens scan", "err", err)
			return
		}
		if !t.Valid || t.Int64 == 0 {
			schools = append(schools, 0) // SA rows (school NULL) → broadcast
		} else {
			schools = append(schools, t.Int64)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Error("janitor reset_tokens rows", "err", err)
		return
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM reset_tokens WHERE source = 'SELF' AND (expires_at < NOW() OR used_at IS NOT NULL)`); err != nil {
		slog.Error("janitor reset_tokens delete", "err", err)
		return
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM reset_tokens WHERE source = 'ADMIN' AND created_at < NOW() - INTERVAL 90 DAY`); err != nil {
		slog.Error("janitor reset_tokens ledger delete", "err", err)
		return
	}
	for _, t := range schools {
		s.publish(t, Frame{Entity: "reset.changed", Action: "swept"})
	}
}

// sweepStalePhotos deletes copy-crash residue: rows stuck file_ready=FALSE for
// more than 24 h are removed together with their partial files (05 §5). The
// delete is per-row guarded on file_ready so a row that finished copying
// between select and delete keeps its file.
func (s *Server) sweepStalePhotos(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, path FROM photos
		 WHERE file_ready = FALSE AND created_at < NOW() - INTERVAL 24 HOUR`)
	if err != nil {
		slog.Error("janitor stale photos select", "err", err)
		return
	}
	type stale struct {
		id   int64
		path string
	}
	var staleRows []stale
	for rows.Next() {
		var st stale
		if err := rows.Scan(&st.id, &st.path); err != nil {
			rows.Close()
			slog.Error("janitor stale photos scan", "err", err)
			return
		}
		staleRows = append(staleRows, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Error("janitor stale photos rows", "err", err)
		return
	}
	for _, st := range staleRows {
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM photos WHERE id = ? AND file_ready = FALSE`, st.id)
		if err != nil {
			slog.Error("janitor stale photos delete", "err", err)
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // finished copying since the select
		}
		if err := os.Remove(filepath.Join(s.upload, filepath.FromSlash(st.path))); err != nil && !os.IsNotExist(err) {
			slog.Error("janitor stale photo file", "err", err, "path", st.path)
		}
	}
}

// sweepOrphans removes upload files no DB row references (crash residue).
// Files younger than orphanGrace are kept: uploads write file-before-row.
func (s *Server) sweepOrphans() {
	root := filepath.Clean(s.upload)
	if root == "." {
		// Unresolved upload root (empty/"." config): walking it would treat
		// the whole working directory as upload residue and try to delete
		// application files. Never sweep in that state.
		slog.Error("janitor skip: unresolved upload root", "upload", s.upload)
		return
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return // uploads never written yet (fresh env)
	}
	refs, err := s.referencedPaths()
	if err != nil {
		slog.Error("janitor referenced paths", "err", err)
		return
	}
	now := time.Now()
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, wErr error) error {
		if wErr != nil || d.IsDir() {
			return nil // unreadable/dir: keep sweeping the rest
		}
		if strings.HasPrefix(d.Name(), ".") {
			// Placeholders like .gitkeep are repository structure, never
			// upload residue (stored names are rand32hex + extension).
			return nil
		}
		info, iErr := d.Info()
		if iErr != nil || now.Sub(info.ModTime()) < orphanGrace {
			return nil
		}
		rel, rErr := filepath.Rel(root, path)
		if rErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if refs[rel] {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Error("janitor orphan file", "err", err, "path", rel)
		} else if err == nil {
			slog.Info("janitor orphan removed", "path", rel)
		}
		return nil
	})
	if walkErr != nil {
		slog.Error("janitor upload walk", "err", walkErr)
	}
}

// referencedPaths is every file path any row still points at — including the
// session badge snapshots that share the master badge bytes and the
// user-keyed profile avatars (spec 09).
func (s *Server) referencedPaths() (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT path FROM photos
		UNION SELECT image_path FROM frames
		UNION SELECT badge_image_path FROM programs WHERE badge_image_path IS NOT NULL
		UNION SELECT badge_image_path FROM topics WHERE badge_image_path IS NOT NULL
		UNION SELECT badge_image_snap FROM session_topics WHERE badge_image_snap IS NOT NULL
		UNION SELECT photo_path FROM users WHERE photo_path IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := make(map[string]bool)
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		refs[p] = true
	}
	return refs, rows.Err()
}

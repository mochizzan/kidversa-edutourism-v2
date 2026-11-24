// photos.go — Photo domain (F-10, FL-10): honest-bytes gallery reads,
// candidate upload behind prefix P, candidate flip and delete.
//
// Binding pins: multipart file field is `file`; only file_ready=TRUE rows are
// ever served (FALSE = copy residue, invisible until durable); a durable row
// whose file is gone carries item-level `missing:true` (E29) — never a
// top-level error; photos are allowed in ANY session status (consent is the
// gate — Finished is explicitly photo-free, Draft is how parents' approvals
// from Draft get used, 06 has no Draft photo prohibition).
//
// Gates (membership → ownership → child active → consent; authz before
// resource state so FAS foreign children always read NOT_OWNER): non-member →
// NOT_FOUND, FAS foreign → NOT_OWNER, inactive child (POST) → CHILD_INACTIVE,
// consent not SETUJU → CONSENT_LOCKED (F-10: no upload/capture/view, all
// roles). The photo cap counts ALL rows of (session, topic, child) with
// SELECT COUNT(*) … FOR UPDATE inside the insert Tx (06 §4, max 10).
package internal

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

// pPrefix is P = /api/sessions/:sid/topics/:stid/children/:cid (bound to the
// /api group by main.go — these registrars mount the path without /api).
const pPrefix = "/sessions/:sid/topics/:stid/children/:cid"

type photoItem struct {
	ID          int64  `json:"id"`
	URL         string `json:"url"`
	IsCandidate bool   `json:"is_candidate"`
	CreatedAt   string `json:"created_at"`
	Mime        string `json:"mime"`
	FileSize    int64  `json:"file_size"`
	Missing     bool   `json:"missing,omitempty"` // E29: TRUE rows whose file is gone
}

// RegisterPhotos mounts the photo routes under prefix P.
func (s *Server) RegisterPhotos(api fiber.Router) {
	api.Get(pPrefix+"/photos", s.authed(), s.handlePhotoList)
	api.Post(pPrefix+"/photos", s.authed(), s.handlePhotoUpload)
	api.Patch(pPrefix+"/photos/:pid", s.authed(), s.handlePhotoPatch)
	api.Delete(pPrefix+"/photos/:pid", s.authed(), s.handlePhotoDelete)
}

// photoScope validates the P prefix (stid belongs to sid) — membership and
// FAS ownership come from requirePChild. "" = pass.
func (s *Server) photoScope(ctx context.Context, sid, stid, school int64) (string, error) {
	tcond, targs := schoolCond(school, "school_id")
	var probe int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?`+tcond,
		append([]any{stid, sid}, targs...)...).Scan(&probe)
	if err == sql.ErrNoRows {
		return CodeNotFound, nil
	}
	if err != nil {
		return "", err
	}
	return "", nil
}

// consentStatus reads the child+session consent ("" = never sent; F-10
// default is locked).
func (s *Server) consentStatus(ctx context.Context, sid, cid int64) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM consent_links WHERE session_id = ? AND child_id = ?`,
		sid, cid).Scan(&status)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return status, nil
}

// handlePhotoList — GET P/photos: durable rows only, created_at ASC, item
// level missing flag.
func (s *Server) handlePhotoList(c fiber.Ctx) error {
	ctx, cancel, sid, stid, cid, school, _, okP := s.pScope(c)
	if !okP {
		return nil
	}
	defer cancel()

	consent, err := s.consentStatus(ctx, sid, cid)
	if err != nil {
		return failDB(c, err)
	}
	if consent != "SETUJU" { // locked until Setuju (F-10: no view, all roles)
		return fail(c, CodeConsentLocked, MsgConsentLocked)
	}

	tcond, targs := schoolCond(school, "school_id")
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, path, is_candidate, created_at, mime, file_size
		 FROM photos
		 WHERE session_id = ? AND session_topic_id = ? AND child_id = ?
		   AND file_ready = TRUE`+tcond+
			` ORDER BY created_at ASC, id ASC`,
		append([]any{sid, stid, cid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	defer rows.Close()
	out := []photoItem{}
	for rows.Next() {
		var (
			item      photoItem
			path      string
			cand      bool
			createdAt time.Time
		)
		if err := rows.Scan(&item.ID, &path, &cand, &createdAt, &item.Mime, &item.FileSize); err != nil {
			return failDB(c, err)
		}
		item.URL = "/uploads/" + path
		item.IsCandidate = cand
		item.CreatedAt = fmtTS(createdAt)
		// E29: the row is durable but the bytes vanished → item flag only.
		if _, err := os.Stat(filepath.Join(s.upload, filepath.FromSlash(path))); err != nil {
			item.Missing = true
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	return ok(c, out)
}

// mimeForExt maps the stored extension back to its wire mime (extensions not
// produced by extFromDeclaredType's photo types — e.g. bin — carry no wire
// mime, so the DB mime column stays metadata-only).
func mimeForExt(ext string) string {
	switch ext {
	case "jpg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	}
	return ""
}

// publishPhotoChanged emits the auth photo.changed signal plus the public
// refetch signals FL-10/02 §2c require whenever a photo change can reach a
// still-valid public token (upload, candidate change, delete). Gallery omits
// Stid (renders ALL topics); report.changed carries it. Best-effort, called
// post-commit/post-durability like every other publish.
func (s *Server) publishPhotoChanged(school, sid, cid, stid, pid int64, action string) {
	s.publish(school, Frame{
		Entity: "photo.changed", EntityID: pid,
		SessionID: &sid, ChildID: cid, Stid: stid, Action: action,
	})
	s.publishPublic(Frame{
		Entity: "gallery.changed", SchoolID: school,
		SessionID: &sid, ChildID: cid, Action: "updated",
	})
	s.publishPublic(Frame{
		Entity: "report.changed", SchoolID: school,
		SessionID: &sid, ChildID: cid, Stid: stid, Action: "updated",
	})
}

// handlePhotoUpload — POST P/photos (multipart field `file`, optional
// `frame_id`), Idempotency-Key. Order: durable file FIRST, then ONE Tx
// (FOR UPDATE count → insert file_ready=TRUE), then 200 + photo.changed —
// no row-without-file window (05 §5 honest-file rule).
func (s *Server) handlePhotoUpload(c fiber.Ctx) error {
	sid, okS := idParam(c, "sid")
	if !okS {
		return nil
	}
	stid, okT := idParam(c, "stid")
	if !okT {
		return nil
	}
	cid, okC := idParam(c, "cid")
	if !okC {
		return nil
	}
	school, okTen := requireSchool(c)
	if !okTen {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)

	fh, err := c.FormFile("file")
	if err != nil {
		return fail(c, CodeValidation, MsgValidation)
	}
	var frameID int64
	if raw := c.FormValue("frame_id"); raw != "" {
		n, convErr := strconv.ParseInt(raw, 10, 64)
		if convErr != nil || n <= 0 {
			return fail(c, CodeValidation, MsgValidation)
		}
		frameID = n
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	code, err := s.requirePChild(ctx, sid, cid, u.ID, u.Role)
	if err != nil {
		return failDB(c, err)
	}
	if code != "" {
		return fail(c, code, msgForCode(code))
	}
	if code, err := s.photoScope(ctx, sid, stid, school); err != nil {
		return failDB(c, err)
	} else if code != "" {
		return fail(c, code, msgForCode(code))
	}

	// Inactive child blocks the write (06 §5; inactive wins over consent).
	var active bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT is_active FROM children WHERE id = ? AND school_id = ?`, cid, school).
		Scan(&active); err != nil {
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		return failDB(c, err)
	}
	if !active {
		return fail(c, CodeChildInactive, MsgChildInactive)
	}
	consent, err := s.consentStatus(ctx, sid, cid)
	if err != nil {
		return failDB(c, err)
	}
	if consent != "SETUJU" {
		return fail(c, CodeConsentLocked, MsgConsentLocked)
	}

	// Optional frame must link the session's program (else VALIDATION).
	if frameID > 0 {
		var programID int64
		if err := s.db.QueryRowContext(ctx,
			`SELECT program_id FROM sessions WHERE id = ? AND school_id = ?`, sid, school).
			Scan(&programID); err != nil {
			if err == sql.ErrNoRows {
				return fail(c, CodeNotFound, MsgNotFound)
			}
			return failDB(c, err)
		}
		var linked int
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM frame_programs fp
			 JOIN frames f ON f.id = fp.frame_id
			 WHERE fp.frame_id = ? AND fp.program_id = ? AND f.school_id = ?)`,
			frameID, programID, school).Scan(&linked); err != nil {
			return failDB(c, err)
		}
		if linked == 0 {
			return fail(c, CodeValidation, MsgValidation)
		}
	}

	return s.idempotent(c, "POST "+c.Path(), func() error {
		ctx, cancel := dbCtx(c)
		defer cancel()

		// File durable FIRST: extension from the VERIFIED bytes + temp/fsync/
		// rename inside storeImage (Pembaruan 2026-11-10 spec 09: the server
		// validates the image — decode-config whitelist + declared-type
		// cross-check, ≤50 MB — before any write; a violation is rejected);
		// the filename is server-generated (never client input).
		src, err := fh.Open()
		if err != nil {
			return fail(c, CodeValidation, MsgValidation)
		}
		dir := fmt.Sprintf("%d/%d/%d/%d", school, sid, stid, cid)
		rel, err := s.storeImage(dir, src, fh.Header.Get("Content-Type"))
		src.Close()
		if err != nil {
			return failStore(c, err)
		}

		// ONE Tx: FOR UPDATE count (never an 11th row), then the row.
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			os.Remove(filepath.Join(s.upload, filepath.FromSlash(rel)))
			return failDB(c, err)
		}
		defer tx.Rollback()
		var count int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM photos
			 WHERE session_id = ? AND session_topic_id = ? AND child_id = ? FOR UPDATE`,
			sid, stid, cid).Scan(&count); err != nil {
			os.Remove(filepath.Join(s.upload, filepath.FromSlash(rel)))
			return failDB(c, err)
		}
		if count >= 10 {
			os.Remove(filepath.Join(s.upload, filepath.FromSlash(rel)))
			return fail(c, CodePhotoLimit, MsgPhotoLimit)
		}
		ext := strings.TrimPrefix(filepath.Ext(rel), ".")
		var frameArg any
		if frameID > 0 {
			frameArg = frameID
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO photos
			 (school_id, session_id, session_topic_id, child_id, child_name_snap,
			  path, frame_id, is_candidate, file_size, mime, created_by, file_ready)
			 SELECT ?, ?, ?, ?, c.name, ?, ?, FALSE, ?, ?, ?, TRUE
			 FROM children c WHERE c.id = ?`,
			school, sid, stid, cid, rel, frameArg, fh.Size, mimeForExt(ext), u.ID, cid)
		if err != nil {
			os.Remove(filepath.Join(s.upload, filepath.FromSlash(rel)))
			return failDB(c, err)
		}
		pid, _ := res.LastInsertId()
		if n, _ := res.RowsAffected(); n == 0 || pid == 0 {
			// Child row vanished between the gate and the insert (rare race).
			os.Remove(filepath.Join(s.upload, filepath.FromSlash(rel)))
			return fail(c, CodeNotFound, MsgNotFound)
		}
		var createdAt time.Time
		if err := tx.QueryRowContext(ctx,
			`SELECT created_at FROM photos WHERE id = ?`, pid).Scan(&createdAt); err != nil {
			os.Remove(filepath.Join(s.upload, filepath.FromSlash(rel)))
			return failDB(c, err)
		}
		if err := tx.Commit(); err != nil {
			os.Remove(filepath.Join(s.upload, filepath.FromSlash(rel)))
			return failDB(c, err)
		}

		s.publishPhotoChanged(school, sid, cid, stid, pid, "created")
		return ok(c, fiber.Map{"photo": photoItem{
			ID: pid, URL: "/uploads/" + rel, IsCandidate: false,
			CreatedAt: fmtTS(createdAt), Mime: mimeForExt(ext),
			FileSize: fh.Size,
		}})
	})
}

type candidateReq struct {
	IsCandidate bool `json:"is_candidate"`
}

// handlePhotoPatch — set the single candidate (one Tx clear+set, scoped;
// allowed in every session status per F-10).
func (s *Server) handlePhotoPatch(c fiber.Ctx) error {
	sid, okS := idParam(c, "sid")
	if !okS {
		return nil
	}
	stid, okT := idParam(c, "stid")
	if !okT {
		return nil
	}
	cid, okC := idParam(c, "cid")
	if !okC {
		return nil
	}
	pid, okP := idParam(c, "pid")
	if !okP {
		return nil
	}
	school, okTen := requireSchool(c)
	if !okTen {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)
	var req candidateReq
	if !bindBody(c, &req) {
		return nil
	}
	if !req.IsCandidate {
		return fail(c, CodeValidation, MsgValidation) // un-setting frees via DELETE
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	code, err := s.requirePChild(ctx, sid, cid, u.ID, u.Role)
	if err != nil {
		return failDB(c, err)
	}
	if code != "" {
		return fail(c, code, msgForCode(code))
	}
	if code, err := s.photoScope(ctx, sid, stid, school); err != nil {
		return failDB(c, err)
	} else if code != "" {
		return fail(c, code, msgForCode(code))
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	tcond, targs := schoolCond(school, "school_id")
	var probe int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM photos
		 WHERE id = ? AND session_id = ? AND session_topic_id = ? AND child_id = ?`+tcond,
		append([]any{pid, sid, stid, cid}, targs...)...).Scan(&probe)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	// Clear the current candidate of this (session, topic, child), then set
	// the new one — DDL's partial-unique candidate_slot backs the invariant.
	if _, err := tx.ExecContext(ctx,
		`UPDATE photos SET is_candidate = FALSE
		 WHERE session_id = ? AND session_topic_id = ? AND child_id = ? AND is_candidate = TRUE`,
		sid, stid, cid); err != nil {
		return failDB(c, err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE photos SET is_candidate = TRUE
		 WHERE id = ? AND session_id = ? AND session_topic_id = ? AND child_id = ?`+tcond,
		append([]any{pid, sid, stid, cid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	var (
		path      string
		createdAt time.Time
		mime      string
		fileSize  int64
	)
	if err := tx.QueryRowContext(ctx,
		`SELECT path, created_at, mime, file_size FROM photos WHERE id = ?`, pid).
		Scan(&path, &createdAt, &mime, &fileSize); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	s.publishPhotoChanged(school, sid, cid, stid, pid, "updated")
	return ok(c, fiber.Map{"photo": photoItem{
		ID: pid, URL: "/uploads/" + path, IsCandidate: true,
		CreatedAt: fmtTS(createdAt), Mime: mime, FileSize: fileSize,
	}})
}

// handlePhotoDelete — row + file (file unlinked after commit); frees the
// candidate slot with the row.
func (s *Server) handlePhotoDelete(c fiber.Ctx) error {
	sid, okS := idParam(c, "sid")
	if !okS {
		return nil
	}
	stid, okT := idParam(c, "stid")
	if !okT {
		return nil
	}
	cid, okC := idParam(c, "cid")
	if !okC {
		return nil
	}
	pid, okP := idParam(c, "pid")
	if !okP {
		return nil
	}
	school, okTen := requireSchool(c)
	if !okTen {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)

	ctx, cancel := dbCtx(c)
	defer cancel()
	code, err := s.requirePChild(ctx, sid, cid, u.ID, u.Role)
	if err != nil {
		return failDB(c, err)
	}
	if code != "" {
		return fail(c, code, msgForCode(code))
	}
	if code, err := s.photoScope(ctx, sid, stid, school); err != nil {
		return failDB(c, err)
	} else if code != "" {
		return fail(c, code, msgForCode(code))
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	tcond, targs := schoolCond(school, "school_id")
	var path string
	err = tx.QueryRowContext(ctx,
		`SELECT path FROM photos
		 WHERE id = ? AND session_id = ? AND session_topic_id = ? AND child_id = ?`+tcond+
			` FOR UPDATE`,
		append([]any{pid, sid, stid, cid}, targs...)...).Scan(&path)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM photos
		 WHERE id = ? AND session_id = ? AND session_topic_id = ? AND child_id = ?`+tcond,
		append([]any{pid, sid, stid, cid}, targs...)...); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	if err := os.Remove(filepath.Join(s.upload, filepath.FromSlash(path))); err != nil && !os.IsNotExist(err) {
		// Row is gone; a leftover file is swept by the hourly orphan sweep.
		// Deleting never fails the operation.
		slog.Error("remove photo file", "err", err, "path", path)
	}
	s.publishPhotoChanged(school, sid, cid, stid, pid, "deleted")
	return ok(c, fiber.Map{"deleted": true})
}

package internal

import (
	"context"
	"database/sql"
	"log/slog"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

// programs.go — program master [SA/ADMIN] (02 §2b, F-04, FL-04) plus the
// master-write helpers shared with topics/frames (masterForm, formField,
// resolveBadge, removeMasterImage).
//
// Program/topic/FRAME writes are multipart/form-data (they can carry an
// image); activities/missions are plain JSON. All master mutations are
// unlimited (01 §2) and publish <entity>.changed after commit.

// masterForm parses the multipart body of one master write. false = VALIDATION
// written (body was not multipart/form-data).
func masterForm(c fiber.Ctx) (*multipart.Form, bool) {
	form, err := c.MultipartForm()
	if err != nil {
		_ = fail(c, CodeValidation, MsgValidation)
		return nil, false
	}
	return form, true
}

// formField returns the first multipart value for key; present=false when the
// key was omitted — partial PATCH must distinguish "absent" from "".
func formField(form *multipart.Form, key string) (string, bool) {
	if v, ok := form.Value[key]; ok && len(v) > 0 {
		return v[0], true
	}
	return "", false
}

// resolveBadge applies the 05 §2 both-or-neither badge rule to one multipart
// master write and stores a new image under the server-verified bytes
// (Pembaruan 2026-11-10 spec 09: whitelist + decode-config cross-check inside
// storeImage — a non-whitelisted badge is rejected with 200 VALIDATION) when
// badge_image is present. existing* is the stored pair (both "" for create).
//
//	returns (name, path, ok, err):
//	ok=true  → persist (name,path); ("","") clears the badge;
//	ok=false, err=nil → VALIDATION already written;
//	err!=nil → return it from the handler (500 unless failStore mapped it).
func (s *Server) resolveBadge(c fiber.Ctx, school int64, form *multipart.Form, existingName, existingPath string) (string, string, bool, error) {
	name := existingName
	nameSet := false
	if v, present := formField(form, "badge_name"); present {
		name = strings.TrimSpace(v)
		nameSet = true
	}
	if len(name) > maxShortTextLen {
		_ = fail(c, CodeValidation, MsgValidation)
		return "", "", false, nil
	}
	files := form.File["badge_image"]
	if len(files) == 0 {
		if nameSet && name == "" {
			return "", "", true, nil // explicit clear → empty pair
		}
		if !validBadgePair(name, existingPath) {
			_ = fail(c, CodeValidation, MsgValidation)
			return "", "", false, nil
		}
		return name, existingPath, true, nil
	}
	if name == "" { // file without a badge name (incl. clear+file
		_ = fail(c, CodeValidation, MsgValidation) // contradiction)
		return "", "", false, nil
	}
	fh := files[0]
	src, err := fh.Open()
	if err != nil {
		return "", "", false, err
	}
	defer src.Close()
	stored, err := s.storeImage("badges/"+strconv.FormatInt(school, 10), src, fh.Header.Get("Content-Type"))
	if err != nil {
		return "", "", false, failStore(c, err)
	}
	return name, stored, true, nil
}

// removeMasterImage deletes a master image file after its row changed, but
// only when no session badge snapshot still shares the bytes (snapshots point
// at the master path — F-04 "sessions unaffected"). Leftovers from a crash
// between row-delete and file-delete are swept hourly by the janitor (FL-04).
// Best-effort: failures only log.
func (s *Server) removeMasterImage(ctx context.Context, rel string) {
	if rel == "" {
		return
	}
	var shared int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_topics WHERE badge_image_snap = ?`, rel).
		Scan(&shared); err != nil {
		slog.Error("master image ref check", "err", err, "path", rel)
		return
	}
	if shared > 0 {
		return
	}
	if err := os.Remove(filepath.Join(s.upload, filepath.FromSlash(rel))); err != nil && !os.IsNotExist(err) {
		slog.Error("master image remove", "err", err, "path", rel)
	}
}

// programItem is the Program wire object (contracts-shapes.md). badgePath is
// the DB-relative file path kept off-wire for delete/replace cleanup.
type programItem struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Description   *string   `json:"description"`
	BadgeName     *string   `json:"badge_name"`
	BadgeImageURL *string   `json:"badge_image_url"`
	IsActive      bool      `json:"is_active"`
	TopicCount    int64     `json:"topic_count"`
	ActivityCount int64     `json:"activity_count"`
	Eligible      bool      `json:"eligible"`
	CreatedAt     time.Time `json:"created_at"`

	badgePath string
}

// programSelect carries the two counts + the eligibility deficit (topics with
// zero activities) so list and detail share one projection.
const programSelect = `SELECT p.id, p.name, p.description, p.badge_name, p.badge_image_path,
	p.is_active, p.created_at,
	(SELECT COUNT(*) FROM topics t WHERE t.program_id = p.id),
	(SELECT COUNT(*) FROM activities a WHERE a.program_id = p.id),
	(SELECT COUNT(*) FROM topics t WHERE t.program_id = p.id
		AND NOT EXISTS (SELECT 1 FROM activities a WHERE a.topic_id = t.id))
	FROM programs p `

func scanProgram(sc scanner) (programItem, error) {
	var it programItem
	var desc, badgeName, badgePath sql.NullString
	var created time.Time
	var deficient int64
	if err := sc.Scan(&it.ID, &it.Name, &desc, &badgeName, &badgePath,
		&it.IsActive, &created, &it.TopicCount, &it.ActivityCount, &deficient); err != nil {
		return programItem{}, err
	}
	it.Description = nullStr(desc)
	it.BadgeName = nullStr(badgeName)
	if badgePath.Valid {
		it.badgePath = badgePath.String
		it.BadgeImageURL = new("/uploads/" + badgePath.String)
	}
	it.CreatedAt = created.UTC()
	it.Eligible = it.TopicCount > 0 && deficient == 0
	return it, nil
}

// nullStr maps a NULL/empty column to a JSON null pointer.
func nullStr(ns sql.NullString) *string {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	return &ns.String
}

// programItemByID loads one program inside the school scope; nil = unknown or
// cross-school (both → NOT_FOUND, identical shape).
func (s *Server) programItemByID(ctx context.Context, id, school int64) (*programItem, error) {
	it, err := scanProgram(s.db.QueryRowContext(ctx,
		programSelect+`WHERE p.id = ? AND p.school_id = ?`, id, school))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// RegisterPrograms mounts the program routes (F-04).
func (s *Server) RegisterPrograms(api fiber.Router) {
	api.Get("/programs", s.authed("ADMIN"), s.listPrograms)
	api.Post("/programs", s.authed("ADMIN"), s.createProgram)
	api.Get("/programs/:id", s.authed("ADMIN"), s.getProgram)
	api.Patch("/programs/:id", s.authed("ADMIN"), s.patchProgram)
	api.Delete("/programs/:id", s.authed("ADMIN"), s.deleteProgram)
}

func (s *Server) listPrograms(c fiber.Ctx) error {
	p, okList := listParams(c, "id", "asc", map[string]string{
		"id": "p.id", "name": "p.name", "created_at": "p.created_at",
	})
	if !okList {
		return nil // VALIDATION already written by listParams
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	where := "WHERE p.school_id = ?"
	args := []any{school}
	if p.Q != "" {
		where += " AND p.name LIKE ?"
		args = append(args, "%"+p.Q+"%")
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	var total int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM programs p `+where, args...).Scan(&total); err != nil {
		return failDB(c, err)
	}
	rows, err := s.db.QueryContext(ctx,
		programSelect+where+` ORDER BY `+p.OrderBy("p.id")+` LIMIT ? OFFSET ?`,
		append(args, p.PerPage, p.Offset)...)
	if err != nil {
		return failDB(c, err)
	}
	defer rows.Close()

	out := make([]programItem, 0, p.PerPage)
	for rows.Next() {
		it, err := scanProgram(rows)
		if err != nil {
			return failDB(c, err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	return okPaged(c, out, p.Page, p.PerPage, total)
}

// createProgram: multipart (name/description/badge_name/badge_image/
// is_active). Badge pair + mime/size are resolved by resolveBadge.
func (s *Server) createProgram(c fiber.Ctx) error {
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	form, okF := masterForm(c)
	if !okF {
		return nil
	}
	name, nameOK := formField(form, "name")
	name = strings.TrimSpace(name)
	if !nameOK || name == "" || len(name) > maxShortTextLen {
		return fail(c, CodeValidation, MsgValidation)
	}
	desc, _ := formField(form, "description")
	active := true
	if v, present := formField(form, "is_active"); present {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fail(c, CodeValidation, MsgValidation)
		}
		active = b
	}
	bName, bPath, okB, err := s.resolveBadge(c, school, form, "", "")
	if err != nil {
		return err
	}
	if !okB {
		return nil
	}

	var descVal any
	if desc != "" {
		descVal = desc
	}
	var nameVal, pathVal any
	if bName != "" {
		nameVal, pathVal = bName, bPath
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO programs (school_id, name, description, badge_name, badge_image_path, is_active)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		school, name, descVal, nameVal, pathVal, active)
	if err != nil {
		return failDB(c, err)
	}
	id, _ := res.LastInsertId()
	it, err := s.programItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil { // raced concurrent delete after commit
		return fail(c, CodeNotFound, MsgNotFound)
	}
	s.publish(school, Frame{Entity: "program.changed", EntityID: id, Action: "created"})
	return ok(c, fiber.Map{"program": it})
}

func (s *Server) getProgram(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	it, err := s.programItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	return ok(c, fiber.Map{"program": it})
}

// patchProgram: every field optional (omitted = untouched); badge follows
// resolveBadge rules; the old badge file is dropped when replaced (share-
// checked by removeMasterImage).
func (s *Server) patchProgram(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	form, okF := masterForm(c)
	if !okF {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	cur, err := s.programItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if cur == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}

	name := cur.Name
	if v, present := formField(form, "name"); present {
		name = strings.TrimSpace(v)
		if name == "" || len(name) > maxShortTextLen {
			return fail(c, CodeValidation, MsgValidation)
		}
	}
	desc := cur.Description
	var descVal any
	if v, present := formField(form, "description"); present {
		if v == "" {
			desc = nil
		} else {
			d := v
			desc = &d
		}
	}
	if desc != nil {
		descVal = *desc
	}
	active := cur.IsActive
	if v, present := formField(form, "is_active"); present {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fail(c, CodeValidation, MsgValidation)
		}
		active = b
	}
	existingName, existingPath := "", cur.badgePath
	if cur.BadgeName != nil {
		existingName = *cur.BadgeName
	}
	bName, bPath, okB, err := s.resolveBadge(c, school, form, existingName, existingPath)
	if err != nil {
		return err
	}
	if !okB {
		return nil
	}
	var nameVal, pathVal any
	if bName != "" {
		nameVal, pathVal = bName, bPath
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE programs SET name = ?, description = ?, badge_name = ?, badge_image_path = ?, is_active = ?
		 WHERE id = ? AND school_id = ?`,
		name, descVal, nameVal, pathVal, active, id, school); err != nil {
		return failDB(c, err)
	}
	if bPath != cur.badgePath {
		s.removeMasterImage(ctx, cur.badgePath) // share-checked (sessions keep their bytes)
	}
	it, err := s.programItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil { // raced concurrent delete after commit
		return fail(c, CodeNotFound, MsgNotFound)
	}
	s.publish(school, Frame{Entity: "program.changed", EntityID: id, Action: "updated"})
	return ok(c, fiber.Map{"program": it})
}

// deleteProgram: one Tx removes the program row + its frame link rows (snap
// data elsewhere is untouched — F-04 snapshot isolation); the badge file goes
// after commit (FL-04, share-checked). Post-commit publishes program.changed
// plus one frame.changed — the frame_programs link sets changed (02 §2c).
func (s *Server) deleteProgram(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	cur, err := s.programItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if cur == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM frame_programs WHERE program_id = ? AND school_id = ?`, id, school); err != nil {
		return failDB(c, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM programs WHERE id = ? AND school_id = ?`, id, school); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}
	s.removeMasterImage(ctx, cur.badgePath)
	s.publish(school, Frame{Entity: "program.changed", EntityID: id, Action: "deleted"})
	// The Tx also dropped this program's frame_programs link rows; one
	// frame.changed (id 0 — no single frame changed, the link sets did)
	// refetches open capture-frame lists (08 §realtime, 02 §2c).
	s.publish(school, Frame{Entity: "frame.changed", EntityID: 0, Action: "updated"})
	return ok(c, fiber.Map{"deleted": true})
}

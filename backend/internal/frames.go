package internal

import (
	"context"
	"database/sql"
	"mime/multipart"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// frames.go — frame master [SA/ADMIN] (02 §2b, F-06, FL-05): multipart CRUD
// (image* + name* + program_ids[1+]), stored under uploads/frames/<school>/.

// frameProgramRef is one frame↔program link (frame_programs snaps).
type frameProgramRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"` // program_name_snap
}

// frameItem is the Frame wire object (contracts-shapes.md).
type frameItem struct {
	ID       int64             `json:"id"`
	Name     string            `json:"name"`
	ImageURL string            `json:"image_url"` // /uploads/… of image_path
	Programs []frameProgramRef `json:"programs"`

	imagePath string
}

const frameSelect = `SELECT id, image_path, name FROM frames f `

func (s *Server) frameItemByID(ctx context.Context, id, school int64) (*frameItem, error) {
	var it frameItem
	err := s.db.QueryRowContext(ctx, frameSelect+`WHERE id = ? AND school_id = ?`, id, school).
		Scan(&it.ID, &it.imagePath, &it.Name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	it.ImageURL = "/uploads/" + it.imagePath
	return &it, nil
}

// framePrograms loads frame_programs links for one page of frames in one query.
func (s *Server) framePrograms(ctx context.Context, school int64, ids []int64) (map[int64][]frameProgramRef, error) {
	out := make(map[int64][]frameProgramRef, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, school)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT frame_id, program_id, program_name_snap FROM frame_programs
		 WHERE school_id = ? AND frame_id IN (`+ph+`) ORDER BY program_name_snap`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref frameProgramRef
		var frameID int64
		if err := rows.Scan(&frameID, &ref.ID, &ref.Name); err != nil {
			return nil, err
		}
		out[frameID] = append(out[frameID], ref)
	}
	return out, rows.Err()
}

// setFramePrograms applies the loaded map so `programs` is never JSON null.
func setFramePrograms(it *frameItem, m map[int64][]frameProgramRef) {
	if p := m[it.ID]; len(p) > 0 {
		it.Programs = p
	} else {
		it.Programs = []frameProgramRef{}
	}
}

// frameProgramIDs reads the program_ids form field — both spellings are
// accepted ("program_ids" and the 02 §2b table's "program_ids[]") — and
// validates it: present ⇒ ≥1, deduped, every id an existing school program
// (else NOT_FOUND). present=false → field absent (caller decides);
// ok=false → a failure envelope was written.
func (s *Server) frameProgramIDs(ctx context.Context, c fiber.Ctx, school int64, form *multipart.Form) (refs []frameProgramRef, present, ok bool) {
	raw := form.Value["program_ids"]
	if len(raw) == 0 {
		raw = form.Value["program_ids[]"]
	}
	if len(raw) == 0 {
		return nil, false, true
	}
	seen := make(map[int64]bool, len(raw))
	var ids []int64
	for _, v := range raw {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			_ = fail(c, CodeValidation, MsgValidation)
			return nil, true, false
		}
		if !seen[n] {
			seen[n] = true
			ids = append(ids, n)
		}
	}
	if len(ids) == 0 {
		_ = fail(c, CodeValidation, MsgValidation)
		return nil, true, false
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, school)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name FROM programs WHERE school_id = ? AND id IN (`+ph+`)`, args...)
	if err != nil {
		_ = failDB(c, err)
		return nil, true, false
	}
	defer rows.Close()
	byID := make(map[int64]string, len(ids))
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			_ = failDB(c, err)
			return nil, true, false
		}
		byID[id] = name
	}
	if err := rows.Err(); err != nil {
		_ = failDB(c, err)
		return nil, true, false
	}
	if len(byID) != len(ids) {
		_ = fail(c, CodeNotFound, MsgNotFound)
		return nil, true, false
	}
	refs = make([]frameProgramRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, frameProgramRef{ID: id, Name: byID[id]})
	}
	return refs, true, true
}

// RegisterFrames mounts the frame routes (F-06).
func (s *Server) RegisterFrames(api fiber.Router) {
	api.Get("/frames", s.authed("ADMIN"), s.listFrames)
	api.Post("/frames", s.authed("ADMIN"), s.createFrame)
	api.Get("/frames/:id", s.authed("ADMIN"), s.getFrame)
	api.Patch("/frames/:id", s.authed("ADMIN"), s.patchFrame)
	api.Delete("/frames/:id", s.authed("ADMIN"), s.deleteFrame)
}

func (s *Server) listFrames(c fiber.Ctx) error {
	p, okList := listParams(c, "id", "asc", map[string]string{
		"id": "f.id", "name": "f.name", "created_at": "f.created_at",
	})
	if !okList {
		return nil // VALIDATION already written by listParams
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	programID, found, okF := queryIDFilter(c, "program_id")
	if !okF {
		return nil
	}
	where := "WHERE f.school_id = ?"
	args := []any{school}
	if found {
		where += ` AND EXISTS (SELECT 1 FROM frame_programs fp
			WHERE fp.frame_id = f.id AND fp.program_id = ?)`
		args = append(args, programID)
	}
	if p.Q != "" {
		where += " AND f.name LIKE ?"
		args = append(args, "%"+p.Q+"%")
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM frames f "+where, args...).Scan(&total); err != nil {
		return failDB(c, err)
	}
	rows, err := s.db.QueryContext(ctx,
		frameSelect+where+` ORDER BY `+p.OrderBy("f.id")+` LIMIT ? OFFSET ?`,
		append(args, p.PerPage, p.Offset)...)
	if err != nil {
		return failDB(c, err)
	}
	out := make([]frameItem, 0, p.PerPage)
	var ids []int64
	for rows.Next() {
		var it frameItem
		if err := rows.Scan(&it.ID, &it.imagePath, &it.Name); err != nil {
			rows.Close()
			return failDB(c, err)
		}
		it.ImageURL = "/uploads/" + it.imagePath
		out = append(out, it)
		ids = append(ids, it.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	linkMap, err := s.framePrograms(ctx, school, ids)
	if err != nil {
		return failDB(c, err)
	}
	for i := range out {
		setFramePrograms(&out[i], linkMap)
	}
	return okPaged(c, out, p.Page, p.PerPage, total)
}

// createFrame: multipart image* + name* + program_ids[1+] →
// uploads/frames/<school>/ (FL-05); links land in the Tx. The image stores
// under the client-declared part type (Pembaruan 2026-10-11: size/format
// validation is client-side only).
func (s *Server) createFrame(c fiber.Ctx) error {
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
	files := form.File["image"]
	if len(files) == 0 {
		return fail(c, CodeValidation, MsgValidation)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	refs, present, okIDs := s.frameProgramIDs(ctx, c, school, form)
	if !okIDs {
		return nil
	}
	if !present { // create requires program_ids[1+]
		return fail(c, CodeValidation, MsgValidation)
	}
	fh := files[0]
	src, err := fh.Open()
	if err != nil {
		return err
	}
	stored, err := s.storeImage("frames/"+strconv.FormatInt(school, 10), src, fh.Header.Get("Content-Type"))
	src.Close()
	if err != nil {
		return failStore(c, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO frames (school_id, image_path, name) VALUES (?, ?, ?)`,
		school, stored, name)
	if err != nil {
		return failDB(c, err)
	}
	id, _ := res.LastInsertId()
	for _, r := range refs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO frame_programs (frame_id, program_id, program_name_snap, school_id)
			 VALUES (?, ?, ?, ?)`, id, r.ID, r.Name, school); err != nil {
			return failDB(c, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	it, err := s.frameItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil { // raced concurrent delete after commit
		return fail(c, CodeNotFound, MsgNotFound)
	}
	linkMap, err := s.framePrograms(ctx, school, []int64{id})
	if err != nil {
		return failDB(c, err)
	}
	setFramePrograms(it, linkMap)
	s.publish(school, Frame{Entity: "frame.changed", EntityID: id, Action: "created"})
	return ok(c, fiber.Map{"frame": it})
}

func (s *Server) getFrame(c fiber.Ctx) error {
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
	it, err := s.frameItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	linkMap, err := s.framePrograms(ctx, school, []int64{id})
	if err != nil {
		return failDB(c, err)
	}
	setFramePrograms(it, linkMap)
	return ok(c, fiber.Map{"frame": it})
}

// patchFrame: partial multipart update — name and/or image and/or program_ids;
// a replaced image is removed after commit (share-checked like badges).
func (s *Server) patchFrame(c fiber.Ctx) error {
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
	cur, err := s.frameItemByID(ctx, id, school)
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
	imagePath := cur.imagePath
	if files := form.File["image"]; len(files) > 0 {
		fh := files[0]
		src, err := fh.Open()
		if err != nil {
			return err
		}
		stored, err := s.storeImage("frames/"+strconv.FormatInt(school, 10), src, fh.Header.Get("Content-Type"))
		src.Close()
		if err != nil {
			return failStore(c, err)
		}
		imagePath = stored
	}
	refs, linksPresent, okIDs := s.frameProgramIDs(ctx, c, school, form)
	if !okIDs {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE frames SET name = ?, image_path = ? WHERE id = ? AND school_id = ?`,
		name, imagePath, id, school); err != nil {
		return failDB(c, err)
	}
	if linksPresent {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM frame_programs WHERE frame_id = ? AND school_id = ?`, id, school); err != nil {
			return failDB(c, err)
		}
		for _, r := range refs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO frame_programs (frame_id, program_id, program_name_snap, school_id)
				 VALUES (?, ?, ?, ?)`, id, r.ID, r.Name, school); err != nil {
				return failDB(c, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}
	if imagePath != cur.imagePath {
		s.removeMasterImage(ctx, cur.imagePath)
	}

	it, err := s.frameItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil { // raced concurrent delete after commit
		return fail(c, CodeNotFound, MsgNotFound)
	}
	linkMap, err := s.framePrograms(ctx, school, []int64{id})
	if err != nil {
		return failDB(c, err)
	}
	setFramePrograms(it, linkMap)
	s.publish(school, Frame{Entity: "frame.changed", EntityID: id, Action: "updated"})
	return ok(c, fiber.Map{"frame": it})
}

// deleteFrame: row + links in one Tx, image file after commit (FL-05
// "deleted + file swept"); photos fused with this frame keep their own bytes.
func (s *Server) deleteFrame(c fiber.Ctx) error {
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
	cur, err := s.frameItemByID(ctx, id, school)
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
		`DELETE FROM frame_programs WHERE frame_id = ? AND school_id = ?`, id, school); err != nil {
		return failDB(c, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM frames WHERE id = ? AND school_id = ?`, id, school); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}
	s.removeMasterImage(ctx, cur.imagePath)
	s.publish(school, Frame{Entity: "frame.changed", EntityID: id, Action: "deleted"})
	return ok(c, fiber.Map{"deleted": true})
}

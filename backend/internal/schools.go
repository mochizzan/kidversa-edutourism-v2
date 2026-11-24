package internal

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

// schools.go — school management [SA] (02 §2b, F-02, FL none).
//
// requireSchool and queryIDFilter live here as the two small helpers shared
// across the B1 domain files (used by programs/topics/activities/missions/
// frames/children/users): everything else stays inside its own file.

// requireSchool returns the school in play for this call. SA may omit
// X-School-ID (authed stores 0) — school-scoped WRITES then have no school to
// target → 200 VALIDATION; reads simply match no rows (school_id = 0).
// false = VALIDATION already written.
func requireSchool(c fiber.Ctx) (int64, bool) {
	t, _ := c.Locals(kvSchool).(int64)
	if t == 0 {
		_ = fail(c, CodeValidation, MsgValidation)
		return 0, false
	}
	return t, true
}

// queryIDFilter parses one optional numeric equality filter (e.g. ?program_id=).
// found=false → filter absent (no restriction); ok=false → VALIDATION written
// (present but malformed).
func queryIDFilter(c fiber.Ctx, param string) (id int64, found, ok bool) {
	var all map[string]string
	if err := c.Bind().Query(&all); err != nil {
		_ = fail(c, CodeValidation, MsgValidation)
		return 0, false, false
	}
	raw := all[param]
	if raw == "" {
		return 0, false, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		_ = fail(c, CodeValidation, MsgValidation)
		return 0, false, false
	}
	return n, true, true
}

// schoolWipeTables lists every table carrying school_id except wa_outbox
// (retained 90 d, 05 §5) and schools itself — the F-02 hard-delete scope.
var schoolWipeTables = []string{
	"users", "refresh_tokens", "reset_tokens",
	"programs", "topics", "activities", "missions", "mission_topics",
	"frames", "frame_programs", "children", "sessions", "session_topics",
	"session_activities", "session_groups", "session_members", "assessments",
	"photos", "narratives", "report_missions", "consent_links", "public_links",
	"idempotency_keys",
}

// schoolItem is the School wire object (contracts-shapes.md): school_name
// replaces name/type; country and city are the new location fields.
type schoolItem struct {
	ID         int64     `json:"id"`
	SchoolName string    `json:"school_name"`
	Country    string    `json:"country"`
	City       *string   `json:"city"` // null when unset
	IsActive   bool      `json:"is_active"`
	CreatedAt  time.Time `json:"created_at"`
}

// scanSchoolItem reads the one school projection shared by list + by-ID.
func scanSchoolItem(sc scanner) (schoolItem, error) {
	var t schoolItem
	var city sql.NullString
	if err := sc.Scan(&t.ID, &t.SchoolName, &t.Country, &city, &t.IsActive, &t.CreatedAt); err != nil {
		return schoolItem{}, err
	}
	if city.Valid {
		t.City = new(city.String)
	}
	t.CreatedAt = t.CreatedAt.UTC()
	return t, nil
}

// schoolCols is the one school projection shared by list + by-ID.
const schoolCols = `id, school_name, country, city, is_active, created_at`

// schoolItemByID loads one school; nil = unknown (caller answers NOT_FOUND).
func (s *Server) schoolItemByID(ctx context.Context, id int64) (*schoolItem, error) {
	t, err := scanSchoolItem(s.db.QueryRowContext(ctx,
		`SELECT `+schoolCols+` FROM schools WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// RegisterSchools mounts the SA-only school routes (02 §2b). Schools
// management is intentionally unlimited (01 §2 — auth+scope are the guard).
func (s *Server) RegisterSchools(api fiber.Router) {
	api.Get("/schools", s.authed("SA"), s.listSchools)
	api.Post("/schools", s.authed("SA"), s.createSchool)
	api.Get("/schools/:id", s.authed("SA"), s.getSchool)
	api.Patch("/schools/:id", s.authed("SA"), s.patchSchool)
	api.Delete("/schools/:id", s.authed("SA"), s.deleteSchool)
	api.Post("/schools/:id/activate", s.authed("SA"), s.schoolActivate)
	api.Post("/schools/:id/deactivate", s.authed("SA"), s.schoolDeactivate)
}

// listSchools: paged, all schools incl. inactive (SA reads are never gated by
// is_active, F-02); ?is_active=true|false narrows the list (strict boolean —
// any other non-empty value is VALIDATION, matching listChildren).
func (s *Server) listSchools(c fiber.Ctx) error {
	p, okList := listParams(c, "id", "asc", map[string]string{
		"id": "id", "school_name": "school_name", "created_at": "created_at",
	})
	if !okList {
		return nil // VALIDATION already written by listParams
	}
	var f struct {
		IsActive string `query:"is_active"`
	}
	_ = c.Bind().Query(&f)
	var active *bool
	if f.IsActive != "" {
		if !strings.EqualFold(f.IsActive, "true") && !strings.EqualFold(f.IsActive, "false") {
			return fail(c, CodeValidation, MsgValidation)
		}
		b := strings.EqualFold(f.IsActive, "true")
		active = &b
	}
	where := "WHERE 1=1"
	var args []any
	if p.Q != "" {
		where += " AND school_name LIKE ?"
		args = append(args, "%"+p.Q+"%")
	}
	if active != nil {
		where += " AND is_active=?"
		args = append(args, *active)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schools "+where, args...).Scan(&total); err != nil {
		return failDB(c, err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+schoolCols+` FROM schools `+where+
			` ORDER BY `+p.OrderBy("id")+` LIMIT ? OFFSET ?`,
		append(args, p.PerPage, p.Offset)...)
	if err != nil {
		return failDB(c, err)
	}
	defer rows.Close()

	out := make([]schoolItem, 0, p.PerPage)
	for rows.Next() {
		t, err := scanSchoolItem(rows)
		if err != nil {
			return failDB(c, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	return okPaged(c, out, p.Page, p.PerPage, total)
}

// schoolReq is the create body (all fields optional except a non-empty
// school_name after trimming). country defaults to Indonesia; city is
// nullable. Names are NOT unique — duplicates are allowed (decision 4).
type schoolReq struct {
	SchoolName string  `json:"school_name"`
	Country    *string `json:"country"`
	City       *string `json:"city"`
}

// createSchool inserts one school with the resolved location defaults.
func (s *Server) createSchool(c fiber.Ctx) error {
	var req schoolReq
	if !bindBody(c, &req) {
		return nil
	}
	name := strings.TrimSpace(req.SchoolName)
	if name == "" || len(name) > maxShortTextLen {
		return fail(c, CodeValidation, MsgValidation)
	}
	country := "Indonesia"
	if req.Country != nil {
		v := strings.TrimSpace(*req.Country)
		if v != "" {
			if len(v) > 100 {
				return fail(c, CodeValidation, MsgValidation)
			}
			country = v
		}
	}
	var city any
	if req.City != nil {
		v := strings.TrimSpace(*req.City)
		if v != "" {
			if len(v) > 100 {
				return fail(c, CodeValidation, MsgValidation)
			}
			city = v
		}
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO schools (school_name, country, city, is_active) VALUES (?, ?, ?, TRUE)`,
		name, country, city)
	if err != nil {
		return failDB(c, err)
	}
	id, _ := res.LastInsertId()
	t, err := s.schoolItemByID(ctx, id)
	if err != nil {
		return failDB(c, err)
	}
	s.publish(id, Frame{Entity: "school.changed", EntityID: id, Action: "created"})
	return ok(c, fiber.Map{"school": t})
}

// getSchool loads one school for the detail view.
func (s *Server) getSchool(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	t, err := s.schoolItemByID(ctx, id)
	if err != nil {
		return failDB(c, err)
	}
	if t == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	return ok(c, fiber.Map{"school": t})
}

// patchSchool updates school_name/country/city/is_active (partial; F-02 CRUD).
func (s *Server) patchSchool(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	var req struct {
		SchoolName *string `json:"school_name"`
		Country    *string `json:"country"`
		City       *string `json:"city"`
		IsActive   *bool   `json:"is_active"`
	}
	if !bindBody(c, &req) {
		return nil
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	cur, err := s.schoolItemByID(ctx, id)
	if err != nil {
		return failDB(c, err)
	}
	if cur == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}

	var set []string
	var args []any
	if req.SchoolName != nil {
		name := strings.TrimSpace(*req.SchoolName)
		if name == "" || len(name) > maxShortTextLen {
			return fail(c, CodeValidation, MsgValidation)
		}
		set, args = append(set, "school_name = ?"), append(args, name)
	}
	if req.Country != nil {
		v := strings.TrimSpace(*req.Country)
		if v == "" {
			v = "Indonesia"
		}
		if len(v) > 100 {
			return fail(c, CodeValidation, MsgValidation)
		}
		set, args = append(set, "country = ?"), append(args, v)
	}
	if req.City != nil {
		v := strings.TrimSpace(*req.City)
		if v == "" {
			set = append(set, "city = NULL")
		} else {
			if len(v) > 100 {
				return fail(c, CodeValidation, MsgValidation)
			}
			set, args = append(set, "city = ?"), append(args, v)
		}
	}
	if req.IsActive != nil {
		set, args = append(set, "is_active = ?"), append(args, *req.IsActive)
	}
	if len(set) > 0 {
		if _, err := s.db.ExecContext(ctx,
			"UPDATE schools SET "+strings.Join(set, ", ")+` WHERE id = ?`,
			append(args, id)...); err != nil {
			return failDB(c, err)
		}
	}
	t, err := s.schoolItemByID(ctx, id)
	if err != nil {
		return failDB(c, err)
	}
	s.publish(id, Frame{Entity: "school.changed", EntityID: id, Action: "updated"})
	return ok(c, fiber.Map{"school": t})
}

// schoolActivate / schoolDeactivate: explicit F-02 transitions (idempotent —
// load first, then UPDATE, so a no-op write is not mistaken for NOT_FOUND).
func (s *Server) schoolActivate(c fiber.Ctx) error   { return s.setSchoolActive(c, true) }
func (s *Server) schoolDeactivate(c fiber.Ctx) error { return s.setSchoolActive(c, false) }

func (s *Server) setSchoolActive(c fiber.Ctx, active bool) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE schools SET is_active = ? WHERE id = ?`, active, id); err != nil {
		return failDB(c, err)
	}
	t, err := s.schoolItemByID(ctx, id)
	if err != nil {
		return failDB(c, err)
	}
	if t == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	s.publish(id, Frame{Entity: "school.changed", EntityID: id, Action: "updated"})
	return ok(c, fiber.Map{"school": t})
}

// deleteSchool is the F-02 hard delete: every school row in ONE Tx, commit,
// THEN disk uploads/<id>/ plus the badge/frame layouts uploads/badges/<id>/
// and uploads/frames/<id>/ (never inside the Tx), THEN school.changed. Typed
// confirmation is client-side. wa_outbox and OpenWA stay untouched.
func (s *Server) deleteSchool(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	for _, table := range schoolWipeTables {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+table+` WHERE school_id = ?`, id); err != nil {
			return failDB(c, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM schools WHERE id = ?`, id); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	// Disk after commit (F-02): a crash between commit and RemoveAll leaves
	// orphan bytes, not broken rows — restore-from-backup re-deletes. All
	// three layouts go: legacy uploads/<id>/ plus the badge/frame dirs
	// (missing dirs are ignored, same as before).
	schoolStr := strconv.FormatInt(id, 10)
	for _, dir := range []string{
		filepath.Join(s.upload, schoolStr),
		filepath.Join(s.upload, "badges", schoolStr),
		filepath.Join(s.upload, "frames", schoolStr),
	} {
		if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
			slog.Error("school wipe disk", "err", err, "dir", dir)
		}
	}
	s.publish(id, Frame{Entity: "school.changed", EntityID: id, Action: "deleted"})
	return ok(c, fiber.Map{"deleted": true})
}

package internal

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

// children.go — child data [ADMIN/SA mutate; FAS read-only] (02 §2b, F-07,
// FL-06). fasChildScope is THE shared scope helper: GET /api/children and
// B2's stream ScopeProvider both call it (never duplicate the predicate).
// Hard delete cascades child-scoped rows + disk and revokes every public/
// consent token (report-link.changed + terminal link.revoked); wa_outbox and
// masters stay untouched. Activate/deactivate never wipes anything.

// fasChildScope returns the child-id restriction for one caller:
// non-facilitators → scoped=false (unrestricted); FASILITATOR → every child
// in their school (scoped=true). Child visibility is tenant-scoped, not
// session-scoped: a facilitator always sees the school's children whether or
// not a session exists yet (the old join through session_members/
// session_groups left facilitators without groups seeing an empty list).
func (s *Server) fasChildScope(ctx context.Context, role string, userID, schoolID int64) ([]int64, bool, error) {
	if role != "FASILITATOR" {
		return nil, false, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM children WHERE school_id = ?`, schoolID)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, true, err
		}
		ids = append(ids, id)
	}
	return ids, true, rows.Err()
}

// childItem is the Child wire object (contracts-shapes.md).
type childItem struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Age          int       `json:"age"`
	AgeConfirmed bool      `json:"age_confirmed"`
	School       string    `json:"school"`
	MotherName   string    `json:"mother_name"`
	MotherWa     string    `json:"mother_wa"`
	FatherName   *string   `json:"father_name"`
	FatherWa     *string   `json:"father_wa"`
	GuardianName *string   `json:"guardian_name"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
}

const childSelect = `SELECT id, name, age, age_confirmed, school, mother_name, mother_wa,
	father_name, father_wa, guardian_name, is_active, created_at FROM children `

func scanChild(sc scanner) (childItem, error) {
	var it childItem
	var fatherName, fatherWa, guardian sql.NullString
	if err := sc.Scan(&it.ID, &it.Name, &it.Age, &it.AgeConfirmed, &it.School,
		&it.MotherName, &it.MotherWa, &fatherName, &fatherWa, &guardian,
		&it.IsActive, &it.CreatedAt); err != nil {
		return childItem{}, err
	}
	it.FatherName = nullStr(fatherName)
	it.FatherWa = nullStr(fatherWa)
	it.GuardianName = nullStr(guardian)
	it.CreatedAt = it.CreatedAt.UTC()
	return it, nil
}

func (s *Server) childItemByID(ctx context.Context, id, schoolID int64) (*childItem, error) {
	it, err := scanChild(s.db.QueryRowContext(ctx,
		childSelect+`WHERE id = ? AND school_id = ?`, id, schoolID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// RegisterChildren mounts the child routes: reads are open to every role (FAS
// scoped by fasChildScope), mutations are ADMIN/SA — FAS gets FORBIDDEN_ROLE
// straight from s.authed("ADMIN").
func (s *Server) RegisterChildren(api fiber.Router) {
	api.Get("/children", s.authed(), s.listChildren)
	api.Get("/children/:id", s.authed(), s.getChild)
	api.Post("/children", s.authed("ADMIN"), s.createChild)
	api.Patch("/children/:id", s.authed("ADMIN"), s.patchChild)
	api.Delete("/children/:id", s.authed("ADMIN"), s.deleteChild)
	api.Post("/children/:id/activate", s.authed("ADMIN"), s.childActivate)
	api.Post("/children/:id/deactivate", s.authed("ADMIN"), s.childDeactivate)
}

// listChildren: ?q= (name substring) + ?is_active= (ParseBool, invalid →
// VALIDATION) + paging/sort per 05 §3; FAS sees the school's children via
// the same helper the stream scope uses.
func (s *Server) listChildren(c fiber.Ctx) error {
	p, okList := listParams(c, "id", "asc", map[string]string{
		"id": "id", "name": "name", "created_at": "created_at",
	})
	if !okList {
		return nil // VALIDATION already written by listParams
	}
	schoolID, okT := requireSchool(c)
	if !okT {
		return nil
	}
	var f struct {
		IsActive string `query:"is_active"`
	}
	_ = c.Bind().Query(&f)
	var active *bool
	if f.IsActive != "" {
		b, err := strconv.ParseBool(f.IsActive)
		if err != nil {
			return fail(c, CodeValidation, MsgValidation)
		}
		active = &b
	}

	u, _ := c.Locals(kvUser).(*userRow)
	ctx, cancel := dbCtx(c)
	defer cancel()
	ids, scoped, err := s.fasChildScope(ctx, u.Role, u.ID, schoolID)
	if err != nil {
		return failDB(c, err)
	}
	if scoped && len(ids) == 0 {
		return okPaged(c, []childItem{}, p.Page, p.PerPage, 0)
	}

	where := "WHERE school_id = ?"
	args := []any{schoolID}
	if scoped {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		where += " AND id IN (" + ph + ")"
		for _, id := range ids {
			args = append(args, id)
		}
	}
	if active != nil {
		where += " AND is_active = ?"
		args = append(args, *active)
	}
	if p.Q != "" {
		where += " AND name LIKE ?"
		args = append(args, "%"+p.Q+"%")
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM children "+where, args...).Scan(&total); err != nil {
		return failDB(c, err)
	}
	rows, err := s.db.QueryContext(ctx,
		childSelect+where+` ORDER BY `+p.OrderBy("id")+` LIMIT ? OFFSET ?`,
		append(args, p.PerPage, p.Offset)...)
	if err != nil {
		return failDB(c, err)
	}
	defer rows.Close()

	out := make([]childItem, 0, p.PerPage)
	for rows.Next() {
		it, err := scanChild(rows)
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

// getChild returns {child, counts} — counts feed the client's typed-delete
// modal (members/assessments/photos/narratives/mission picks/active links).
// FAS foreign child → NOT_FOUND (row outside its scope behaves unknown).
func (s *Server) getChild(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	schoolID, okT := requireSchool(c)
	if !okT {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()

	u, _ := c.Locals(kvUser).(*userRow)
	ids, scoped, err := s.fasChildScope(ctx, u.Role, u.ID, schoolID)
	if err != nil {
		return failDB(c, err)
	}
	if scoped && !slices.Contains(ids, id) {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	it, err := s.childItemByID(ctx, id, schoolID)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	var members, assessments, photos, narratives, picks, consents, links int
	if err := s.db.QueryRowContext(ctx,
		`SELECT
			(SELECT COUNT(*) FROM session_members WHERE child_id = ?),
			(SELECT COUNT(*) FROM assessments WHERE child_id = ?),
			(SELECT COUNT(*) FROM photos WHERE child_id = ?),
			(SELECT COUNT(*) FROM narratives WHERE child_id = ?),
			(SELECT COUNT(*) FROM report_missions WHERE child_id = ?),
			(SELECT COUNT(*) FROM consent_links WHERE child_id = ?),
			(SELECT COUNT(*) FROM public_links WHERE child_id = ? AND revoked_at IS NULL)`,
		id, id, id, id, id, id, id).
		Scan(&members, &assessments, &photos, &narratives, &picks, &consents, &links); err != nil {
		return failDB(c, err)
	}
	return ok(c, fiber.Map{
		"child": it,
		"counts": fiber.Map{
			"members": members, "assessments": assessments, "photos": photos,
			"narratives": narratives, "missions": picks, "links": consents + links,
		},
	})
}

// childReq is the create body AND the (presence-aware) patch body: pointer
// fields distinguish "absent" from zero values; explicit JSON null behaves
// like absent for the optional name/WA fields (clear via "").
type childReq struct {
	Name         *string `json:"name"`
	Age          *int    `json:"age"`
	AgeConfirmed *bool   `json:"age_confirmed"`
	School       *string `json:"school"`
	MotherName   *string `json:"mother_name"`
	MotherWa     *string `json:"mother_wa"`
	FatherName   *string `json:"father_name"`
	FatherWa     *string `json:"father_wa"`
	GuardianName *string `json:"guardian_name"`
}

// optionalName maps absent/"" → SQL NULL and bounds a present value (≤160,
// trimmed). ok=false → over-long (VALIDATION by the caller).
func optionalName(p *string) (val any, ok bool) {
	if p == nil || *p == "" {
		return nil, true
	}
	if len(*p) > maxPersonNameLen {
		return nil, false
	}
	return strings.TrimSpace(*p), true
}

// createChild: F-07 — name/mother required, age 1–10 direct, 11–15 needs
// age_confirmed (else VALIDATION), mother WA required + normalised,
// father/guardian optional with the same normalisation. Active on insert.
// `school` is OPTIONAL: when omitted/blank the server falls back to the
// latest active school's school_name; no active school → VALIDATION
// (decisions 7/11/12). The wire key stays `school` (a different field from
// the school entity's school_name).
func (s *Server) createChild(c fiber.Ctx) error {
	var req childReq
	if !bindBody(c, &req) {
		return nil
	}
	schoolID, okT := requireSchool(c)
	if !okT {
		return nil
	}
	if req.Name == nil || req.MotherName == nil || req.Age == nil {
		return fail(c, CodeValidation, MsgValidation)
	}
	name := strings.TrimSpace(*req.Name)
	var school string
	if req.School != nil {
		school = strings.TrimSpace(*req.School)
	}
	motherName := strings.TrimSpace(*req.MotherName)
	confirmed := req.AgeConfirmed != nil && *req.AgeConfirmed
	if name == "" || len(name) > maxShortTextLen ||
		len(school) > maxShortTextLen ||
		motherName == "" || len(motherName) > maxPersonNameLen ||
		!validAge(*req.Age, confirmed) {
		return fail(c, CodeValidation, MsgValidation)
	}
	motherInput := ""
	if req.MotherWa != nil {
		motherInput = *req.MotherWa
	}
	motherWa, waOK := normalizeWA(motherInput)
	if !waOK {
		return fail(c, CodeValidation, MsgValidation)
	}
	fatherName, okF := optionalName(req.FatherName)
	if !okF {
		return fail(c, CodeValidation, MsgValidation)
	}
	guardian, okG := optionalName(req.GuardianName)
	if !okG {
		return fail(c, CodeValidation, MsgValidation)
	}
	var fatherWa any
	if req.FatherWa != nil && *req.FatherWa != "" {
		fw, fwOK := normalizeWA(*req.FatherWa)
		if !fwOK {
			return fail(c, CodeValidation, MsgValidation)
		}
		fatherWa = fw
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	if school == "" {
		active, err := s.latestActiveSchool(ctx)
		if err != nil {
			return failDB(c, err)
		}
		if active == nil {
			return fail(c, CodeValidation, MsgValidation)
		}
		school = active.SchoolName
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO children (school_id, name, age, age_confirmed, school,
			mother_name, mother_wa, father_name, father_wa, guardian_name, is_active)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, TRUE)`,
		schoolID, name, *req.Age, confirmed, school,
		motherName, motherWa, fatherName, fatherWa, guardian)
	if err != nil {
		return failDB(c, err)
	}
	id, _ := res.LastInsertId()
	it, err := s.childItemByID(ctx, id, schoolID)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil { // raced concurrent delete after commit
		return fail(c, CodeNotFound, MsgNotFound)
	}
	s.publish(schoolID, Frame{Entity: "child.changed", EntityID: id, Action: "created"})
	return ok(c, fiber.Map{"child": it})
}

// patchChild: partial update — only present fields change; an age change into
// 11–15 must carry age_confirmed:true in the SAME payload; mother_wa can be
// replaced but never cleared ("" → VALIDATION).
func (s *Server) patchChild(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	var req childReq
	if !bindBody(c, &req) {
		return nil
	}
	schoolID, okT := requireSchool(c)
	if !okT {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	cur, err := s.childItemByID(ctx, id, schoolID)
	if err != nil {
		return failDB(c, err)
	}
	if cur == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}

	name, school, motherName := cur.Name, cur.School, cur.MotherName
	age, ageConfirmed := cur.Age, cur.AgeConfirmed
	motherWa := cur.MotherWa
	var fatherName, fatherWa, guardian any
	if cur.FatherName != nil {
		fatherName = *cur.FatherName
	}
	if cur.FatherWa != nil {
		fatherWa = *cur.FatherWa
	}
	if cur.GuardianName != nil {
		guardian = *cur.GuardianName
	}
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" || len(name) > maxShortTextLen {
			return fail(c, CodeValidation, MsgValidation)
		}
	}
	if req.School != nil {
		school = strings.TrimSpace(*req.School)
		if school == "" || len(school) > maxShortTextLen {
			return fail(c, CodeValidation, MsgValidation)
		}
	}
	if req.MotherName != nil {
		motherName = strings.TrimSpace(*req.MotherName)
		if motherName == "" || len(motherName) > maxPersonNameLen {
			return fail(c, CodeValidation, MsgValidation)
		}
	}
	if req.Age != nil {
		age = *req.Age
		ageConfirmed = req.AgeConfirmed != nil && *req.AgeConfirmed
		if !validAge(age, ageConfirmed) {
			return fail(c, CodeValidation, MsgValidation)
		}
	} else if req.AgeConfirmed != nil {
		ageConfirmed = *req.AgeConfirmed
	}
	if req.MotherWa != nil {
		w, waOK := normalizeWA(*req.MotherWa)
		if !waOK {
			return fail(c, CodeValidation, MsgValidation)
		}
		motherWa = w
	}
	if req.FatherName != nil {
		v, okN := optionalName(req.FatherName)
		if !okN {
			return fail(c, CodeValidation, MsgValidation)
		}
		fatherName = v
	}
	if req.FatherWa != nil {
		if *req.FatherWa == "" {
			fatherWa = nil
		} else {
			w, waOK := normalizeWA(*req.FatherWa)
			if !waOK {
				return fail(c, CodeValidation, MsgValidation)
			}
			fatherWa = w
		}
	}
	if req.GuardianName != nil {
		v, okN := optionalName(req.GuardianName)
		if !okN {
			return fail(c, CodeValidation, MsgValidation)
		}
		guardian = v
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE children SET name = ?, age = ?, age_confirmed = ?, school = ?,
			mother_name = ?, mother_wa = ?, father_name = ?, father_wa = ?, guardian_name = ?
		 WHERE id = ? AND school_id = ?`,
		name, age, ageConfirmed, school, motherName, motherWa,
		fatherName, fatherWa, guardian, id, schoolID); err != nil {
		return failDB(c, err)
	}
	it, err := s.childItemByID(ctx, id, schoolID)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil { // raced concurrent delete after commit
		return fail(c, CodeNotFound, MsgNotFound)
	}
	s.publish(schoolID, Frame{Entity: "child.changed", EntityID: id, Action: "updated"})
	return ok(c, fiber.Map{"child": it})
}

// childActivate / childDeactivate: is_active toggles only — no side wipe
// (F-07); deactivated children stay visible in history and old reports.
func (s *Server) childActivate(c fiber.Ctx) error   { return s.setChildActive(c, true) }
func (s *Server) childDeactivate(c fiber.Ctx) error { return s.setChildActive(c, false) }

func (s *Server) setChildActive(c fiber.Ctx, active bool) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	schoolID, okT := requireSchool(c)
	if !okT {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE children SET is_active = ? WHERE id = ? AND school_id = ?`,
		active, id, schoolID); err != nil {
		return failDB(c, err)
	}
	it, err := s.childItemByID(ctx, id, schoolID)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	s.publish(schoolID, Frame{Entity: "child.changed", EntityID: id, Action: "updated"})
	return ok(c, fiber.Map{"child": it})
}

// deleteChild is the F-07 hard delete: every child-scoped row in ONE Tx
// (members, assessments, photo rows, narratives, mission picks, consent +
// public link rows), then disk photo removal, then the SSE tail —
// child.changed, report-link.changed per revoked link, and a terminal
// link.revoked on each revoked token's public stream. wa_outbox and masters
// are never touched. Typed confirmation is client-side.
func (s *Server) deleteChild(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	schoolID, okT := requireSchool(c)
	if !okT {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	cur, err := s.childItemByID(ctx, id, schoolID)
	if err != nil {
		return failDB(c, err)
	}
	if cur == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}

	type revokedLink struct {
		linkID    int64
		sessionID int64
		token     string
		public    bool
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()

	// Collect what post-commit work needs: photo files + revoked tokens.
	var photoPaths []string
	rows, err := tx.QueryContext(ctx,
		`SELECT path FROM photos WHERE child_id = ? AND school_id = ?`, id, schoolID)
	if err != nil {
		return failDB(c, err)
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return failDB(c, err)
		}
		photoPaths = append(photoPaths, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}

	var revoked []revokedLink
	rows, err = tx.QueryContext(ctx,
		`SELECT id, session_id, token FROM public_links WHERE child_id = ? AND school_id = ?`,
		id, schoolID)
	if err != nil {
		return failDB(c, err)
	}
	for rows.Next() {
		var l revokedLink
		l.public = true
		if err := rows.Scan(&l.linkID, &l.sessionID, &l.token); err != nil {
			rows.Close()
			return failDB(c, err)
		}
		revoked = append(revoked, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	rows, err = tx.QueryContext(ctx,
		`SELECT id, session_id, token FROM consent_links WHERE child_id = ? AND school_id = ?`,
		id, schoolID)
	if err != nil {
		return failDB(c, err)
	}
	for rows.Next() {
		var l revokedLink
		if err := rows.Scan(&l.linkID, &l.sessionID, &l.token); err != nil {
			rows.Close()
			return failDB(c, err)
		}
		revoked = append(revoked, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}

	for _, table := range []string{"session_members", "assessments", "photos",
		"narratives", "report_missions", "consent_links", "public_links"} {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+table+` WHERE child_id = ? AND school_id = ?`, id, schoolID); err != nil {
			return failDB(c, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM children WHERE id = ? AND school_id = ?`, id, schoolID); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	// Disk after commit: honest bytes never outlive the row.
	for _, p := range photoPaths {
		if err := os.Remove(filepath.Join(s.upload, filepath.FromSlash(p))); err != nil && !os.IsNotExist(err) {
			slog.Error("child delete photo file", "err", err, "path", p)
		}
	}

	s.publish(schoolID, Frame{Entity: "child.changed", EntityID: id, Action: "deleted"})
	for _, l := range revoked {
		if l.public {
			s.publish(schoolID, Frame{
				Entity: "report-link.changed", EntityID: l.linkID, Action: "deleted",
				SessionID: new(l.sessionID), ChildID: id,
			})
		}
		s.publishPublic(Frame{
			Entity: "link.revoked", Action: "deleted",
			SchoolID: schoolID, SessionID: new(l.sessionID), ChildID: id,
			Token: l.token,
		})
	}
	return ok(c, fiber.Map{"deleted": true})
}

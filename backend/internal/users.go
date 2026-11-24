package internal

import (
	"context"
	"database/sql"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

// users.go — user management (02 §2b, F-03, FL-01 tail): list scope
// (SA = all schools / ADMIN = own-school facilitators ONLY), pending badge,
// approve/reject/activate/deactivate (refresh revoke in the same Tx),
// create/patch, and hard delete gated by HAS_OPEN_GROUPS.
//
// No route here is rate-limited (01 §2 — auth + scope are the protection).

// userCols is the item projection behind the users JOIN (contracts-shapes.md
// User object + school_name for the SA view).
const userCols = `u.id, u.name, u.email, u.role, u.decision, u.is_active,
	u.school_id, u.wa_number, u.created_at, sch.school_name`

// "sch" is deliberately not "s" (sessions' alias) and not "t" (the legacy
// alias): it stays collision-free when this JOIN is composed with
// other domain queries.
const userFrom = ` FROM users u LEFT JOIN schools sch ON sch.id = u.school_id `

// userItem is the exact User wire object of contracts-shapes.md.
type userItem struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Email      string    `json:"email"`
	Role       string    `json:"role"`
	Decision   string    `json:"decision"`
	IsActive   bool      `json:"is_active"`
	SchoolID   *int64    `json:"school_id"` // null for SA rows
	WaNumber   *string   `json:"wa_number"` // null when unset
	CreatedAt  time.Time `json:"created_at"`
	SchoolName *string   `json:"school_name"` // null for SA rows
}

// scanner lets one scan helper serve *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanUserItem(sc scanner) (userItem, error) {
	var it userItem
	var schoolID sql.NullInt64
	var wa, schoolName sql.NullString
	if err := sc.Scan(&it.ID, &it.Name, &it.Email, &it.Role, &it.Decision,
		&it.IsActive, &schoolID, &wa, &it.CreatedAt, &schoolName); err != nil {
		return userItem{}, err
	}
	if schoolID.Valid {
		it.SchoolID = &schoolID.Int64
	}
	if wa.Valid {
		it.WaNumber = &wa.String
	}
	if schoolName.Valid {
		it.SchoolName = &schoolName.String
	}
	it.CreatedAt = it.CreatedAt.UTC()
	return it, nil
}

// userItemByID reloads one item (list/get/mutation responses share the shape).
func (s *Server) userItemByID(ctx context.Context, id int64) (*userItem, error) {
	it, err := scanUserItem(s.db.QueryRowContext(ctx,
		`SELECT `+userCols+userFrom+`WHERE u.id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// editUser is the minimal row a scoped mutation needs.
type editUser struct {
	ID       int64
	SchoolID int64 // 0 for SA rows
	Role     string
	Decision string
}

// loadUserForEdit loads :id and enforces the 02 §2b scope: unknown or
// cross-school id → NOT_FOUND (identical shape, no oracle); ADMIN touching a
// non-facilitator row → FORBIDDEN_ROLE. false = a failure envelope was
// written; callers return nil.
func (s *Server) loadUserForEdit(ctx context.Context, c fiber.Ctx, id int64) (*editUser, bool) {
	var t editUser
	err := s.db.QueryRowContext(ctx,
		`SELECT id, COALESCE(school_id, 0), role, decision FROM users WHERE id = ?`, id).
		Scan(&t.ID, &t.SchoolID, &t.Role, &t.Decision)
	if err == sql.ErrNoRows {
		_ = fail(c, CodeNotFound, MsgNotFound)
		return nil, false
	}
	if err != nil {
		_ = failDB(c, err)
		return nil, false
	}
	me, _ := c.Locals(kvUser).(*userRow)
	if me != nil && me.Role != "SA" {
		if t.SchoolID != me.SchoolID {
			_ = fail(c, CodeNotFound, MsgNotFound)
			return nil, false
		}
		if t.Role != "FASILITATOR" {
			_ = fail(c, CodeForbiddenRole, MsgForbiddenRole)
			return nil, false
		}
	}
	return &t, true
}

// pendingCountFor counts PENDING rows in the acting user's LIST scope (02 §2b
// "same scope as list"): global for SA, own-school facilitators for ADMIN.
func (s *Server) pendingCountFor(ctx context.Context, u *userRow) (int, error) {
	q := `SELECT COUNT(*) FROM users WHERE decision = 'PENDING'`
	var args []any
	if u.Role != "SA" {
		q += ` AND school_id = ? AND role = 'FASILITATOR'`
		args = append(args, u.SchoolID)
	}
	var n int
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// publishPending signals the badge value after a decision transition
// (best-effort; a count failure only logs — never fails the mutation).
func (s *Server) publishPending(ctx context.Context, actor *userRow, school int64) {
	n, err := s.pendingCountFor(ctx, actor)
	if err != nil {
		slog.Error("pending count", "err", err)
		return
	}
	s.publish(school, Frame{Entity: "pending.changed", Action: "updated", Pending: new(n)})
}

// openGroup feeds the E31 HAS_OPEN_GROUPS "+ list" payload.
type openGroup struct {
	GID         int64  `json:"gid"`
	SessionID   int64  `json:"session_id"`
	SessionName string `json:"session_name"`
}

// RegisterUsers mounts the user-management routes (static pending-count is
// registered before the :id routes). Only the three admin-control routes are
// rate-limited (10/min/IP, spec §9.1/§10); the rest rely on auth + scope.
func (s *Server) RegisterUsers(api fiber.Router) {
	api.Get("/users/pending-count", s.authed("ADMIN"), s.countPendingUsers)
	api.Get("/users", s.authed("ADMIN"), s.listUsers)
	api.Post("/users", s.authed("ADMIN"), s.createUser)
	api.Get("/users/:id", s.authed("ADMIN"), s.getUser)
	api.Patch("/users/:id", s.authed("ADMIN"), s.patchUser)
	api.Delete("/users/:id", s.authed("ADMIN"), s.deleteUser)
	api.Post("/users/:id/approve", s.authed("ADMIN"), s.approveUser)
	api.Post("/users/:id/reject", s.authed("ADMIN"), s.rejectUser)
	api.Post("/users/:id/activate", s.authed("ADMIN"), s.activateUser)
	api.Post("/users/:id/deactivate", s.authed("ADMIN"), s.deactivateUser)
	// Feature A/B (spec §9.1/§10): no idempotency wrapper, one-shot credentials.
	api.Post("/users/:id/impersonate", newLimiter(10, time.Minute, keyIP), s.authed("ADMIN"), s.impersonateUser)
	api.Post("/users/:id/password/generate", newLimiter(10, time.Minute, keyIP), s.authed("ADMIN"), s.generateUserPassword)
	api.Post("/users/:id/password", newLimiter(10, time.Minute, keyIP), s.authed("ADMIN"), s.adminResetUserPassword)
}

// countPendingUsers is the pending badge endpoint (02 §2b).
func (s *Server) countPendingUsers(c fiber.Ctx) error {
	u, _ := c.Locals(kvUser).(*userRow)
	ctx, cancel := dbCtx(c)
	defer cancel()
	n, err := s.pendingCountFor(ctx, u)
	if err != nil {
		return failDB(c, err)
	}
	return ok(c, fiber.Map{"pending": n})
}

// listUsers: SA sees every row; ADMIN is pinned to own-school facilitators —
// role=ADMIN/role=SA (or no role) still yields facilitator rows only, never
// admin rows (binding, 02 §2b).
func (s *Server) listUsers(c fiber.Ctx) error {
	p, okList := listParams(c, "id", "asc", map[string]string{
		"id": "u.id", "name": "u.name", "created_at": "u.created_at", "email": "u.email",
	})
	if !okList {
		return nil // VALIDATION already written by listParams
	}
	var f struct {
		Role     string `query:"role"`
		Decision string `query:"decision"`
		IsActive string `query:"is_active"`
	}
	_ = c.Bind().Query(&f)
	if f.Role != "" && f.Role != "SA" && f.Role != "ADMIN" && f.Role != "FASILITATOR" {
		return fail(c, CodeValidation, MsgValidation)
	}
	if f.Decision != "" && f.Decision != "PENDING" && f.Decision != "APPROVED" && f.Decision != "REJECTED" {
		return fail(c, CodeValidation, MsgValidation)
	}
	var active *bool
	if f.IsActive != "" {
		b, err := strconv.ParseBool(f.IsActive)
		if err != nil {
			return fail(c, CodeValidation, MsgValidation)
		}
		active = &b
	}

	me, _ := c.Locals(kvUser).(*userRow)
	where := "WHERE 1=1"
	var args []any
	if me.Role != "SA" {
		where += " AND u.school_id = ? AND u.role = 'FASILITATOR'"
		args = append(args, me.SchoolID)
		f.Role = "" // ADMIN: the scope IS facilitator-only; role=ADMIN/SA
		// (or omitted) never widens it (binding, 02 §2b).
	}
	if f.Role != "" {
		where += " AND u.role = ?"
		args = append(args, f.Role)
	}
	if f.Decision != "" {
		where += " AND u.decision = ?"
		args = append(args, f.Decision)
	}
	if active != nil {
		where += " AND u.is_active = ?"
		args = append(args, *active)
	}
	if p.Q != "" {
		where += " AND (u.name LIKE ? OR u.email LIKE ?)"
		args = append(args, "%"+p.Q+"%", "%"+p.Q+"%")
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+userFrom+where, args...).
		Scan(&total); err != nil {
		return failDB(c, err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userCols+userFrom+where+` ORDER BY `+p.OrderBy("u.id")+` LIMIT ? OFFSET ?`,
		append(args, p.PerPage, p.Offset)...)
	if err != nil {
		return failDB(c, err)
	}
	defer rows.Close()

	out := make([]userItem, 0, p.PerPage)
	for rows.Next() {
		it, err := scanUserItem(rows)
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

type createUserReq struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	WaNumber string `json:"wa_number"`
	Password string `json:"password"`
	Role     string `json:"role"`
	SchoolID int64  `json:"school_id"`
}

// createUser: SA creates admins (any active school) / ADMIN creates own-school
// facilitators. Rows are created APPROVED+active — the approving authority IS
// the creator; the PENDING pipeline is reserved for self-registration (FL-01).
// Duplicate email → 200 EMAIL_TAKEN.
//
// POST /api/users is idempotent per 06 §3 (like handleRegister): the
// X-Idempotency-Key claim is scoped to the resolved target school; a replay
// within 24 h returns the original envelope byte-for-byte with no second row.
func (s *Server) createUser(c fiber.Ctx) error {
	var req createUserReq
	if !bindBody(c, &req) {
		return nil
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	me, _ := c.Locals(kvUser).(*userRow)

	if req.Name == "" || len(req.Name) > maxPersonNameLen || !validEmail(req.Email) || !validPassword(req.Password) {
		return fail(c, CodeValidation, MsgValidation)
	}
	wa, waOK := normalizeWA(req.WaNumber)
	if !waOK {
		return fail(c, CodeValidation, MsgValidation)
	}
	if req.Role == "SA" {
		return fail(c, CodeForbiddenRole, MsgForbiddenRole)
	}
	if req.Role != "ADMIN" && req.Role != "FASILITATOR" {
		return fail(c, CodeValidation, MsgValidation)
	}
	if me.Role != "SA" && req.Role != "FASILITATOR" {
		return fail(c, CodeForbiddenRole, MsgForbiddenRole)
	}

	// Target school: ADMIN is pinned to its own; SA picks (default header).
	schoolID := me.SchoolID
	if me.Role == "SA" {
		schoolID = req.SchoolID
		if schoolID == 0 {
			schoolID, _ = c.Locals(kvSchool).(int64)
		}
		if schoolID == 0 {
			return fail(c, CodeValidation, MsgValidation)
		}
	}

	ctx, cancel := dbCtx(c)
	active, err := s.schoolActive(ctx, schoolID)
	cancel()
	if err != nil {
		return failDB(c, err)
	}
	if !active {
		return fail(c, CodeSchoolInactive, MsgSchoolInactive)
	}
	c.Locals(kvSchool, schoolID) // idempotency scope (resolved target school)

	return s.idempotent(c, "POST /api/users", func() error {
		ctx, cancel := dbCtx(c)
		defer cancel()
		var exists int
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM users WHERE email = ?)`, req.Email).Scan(&exists); err != nil {
			return failDB(c, err)
		}
		if exists == 1 {
			return fail(c, CodeEmailTaken, MsgEmailTaken)
		}
		hash, err := hashPassword(req.Password)
		if err != nil {
			return err
		}
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)
			 VALUES (?, ?, ?, ?, ?, ?, 'APPROVED', TRUE)`,
			schoolID, req.Name, req.Email, wa, hash, req.Role)
		if err != nil {
			if isDuplicateErr(err) {
				return fail(c, CodeEmailTaken, MsgEmailTaken)
			}
			return failDB(c, err)
		}
		id, _ := res.LastInsertId()
		it, err := s.userItemByID(ctx, id)
		if err != nil {
			return failDB(c, err)
		}
		if it == nil { // raced concurrent delete after insert
			return fail(c, CodeNotFound, MsgNotFound)
		}
		s.publish(schoolID, Frame{Entity: "user.changed", EntityID: id, Action: "created"})
		return ok(c, fiber.Map{"user": it})
	})
}

// getUser loads one user in the caller's scope (ADMIN foreign/ineligible →
// NOT_FOUND / FORBIDDEN_ROLE via loadUserForEdit).
func (s *Server) getUser(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	if _, okL := s.loadUserForEdit(ctx, c, id); !okL {
		return nil
	}
	it, err := s.userItemByID(ctx, id)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	return ok(c, fiber.Map{"user": it})
}

// patchUser updates name/email/wa_number only — decision/is_active/role move
// through their dedicated routes (F-03).
func (s *Server) patchUser(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	var req struct {
		Name     *string `json:"name"`
		Email    *string `json:"email"`
		WaNumber *string `json:"wa_number"`
	}
	if !bindBody(c, &req) {
		return nil
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	if _, okL := s.loadUserForEdit(ctx, c, id); !okL {
		return nil
	}

	var set []string
	var args []any
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len(name) > maxPersonNameLen {
			return fail(c, CodeValidation, MsgValidation)
		}
		set, args = append(set, "name = ?"), append(args, name)
	}
	if req.Email != nil {
		email := strings.ToLower(strings.TrimSpace(*req.Email))
		if !validEmail(email) {
			return fail(c, CodeValidation, MsgValidation)
		}
		var exists int
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM users WHERE email = ? AND id <> ?)`, email, id).
			Scan(&exists); err != nil {
			return failDB(c, err)
		}
		if exists == 1 {
			return fail(c, CodeEmailTaken, MsgEmailTaken)
		}
		set, args = append(set, "email = ?"), append(args, email)
	}
	if req.WaNumber != nil {
		if *req.WaNumber == "" {
			set = append(set, "wa_number = NULL")
		} else {
			wa, waOK := normalizeWA(*req.WaNumber)
			if !waOK {
				return fail(c, CodeValidation, MsgValidation)
			}
			set, args = append(set, "wa_number = ?"), append(args, wa)
		}
	}
	if len(set) > 0 {
		if _, err := s.db.ExecContext(ctx,
			"UPDATE users SET "+strings.Join(set, ", ")+` WHERE id = ?`,
			append(args, id)...); err != nil {
			if isDuplicateErr(err) {
				return fail(c, CodeEmailTaken, MsgEmailTaken)
			}
			return failDB(c, err)
		}
	}

	it, err := s.userItemByID(ctx, id)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	s.publish(it.pubSchool(), Frame{Entity: "user.changed", EntityID: id, Action: "updated"})
	return ok(c, fiber.Map{"user": it})
}

// pubSchool routes a user frame: row school, 0 (broadcast) for SA rows.
func (u userItem) pubSchool() int64 {
	if u.SchoolID == nil {
		return 0
	}
	return *u.SchoolID
}

// deleteUser: E31 gate (owns groups in any OPENED session → HAS_OPEN_GROUPS
// + list), otherwise one Tx removes the row with its refresh/reset rows;
// snapshots elsewhere stay intact (F-03).
func (s *Server) deleteUser(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	me, _ := c.Locals(kvUser).(*userRow)
	target, okT := s.loadUserForEdit(ctx, c, id)
	if !okT {
		return nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT g.id, s.id, s.name FROM session_groups g
		 JOIN sessions s ON s.id = g.session_id
		 WHERE g.facilitator_id = ? AND s.status = 'OPENED' ORDER BY g.id LIMIT 50`, id)
	if err != nil {
		return failDB(c, err)
	}
	var open []openGroup
	for rows.Next() {
		var g openGroup
		if err := rows.Scan(&g.GID, &g.SessionID, &g.SessionName); err != nil {
			rows.Close()
			return failDB(c, err)
		}
		open = append(open, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	if len(open) > 0 {
		// E31 ships "+ list": an envelope-compatible superset of
		// {error:{code,message}} (spec 06 §1 wins over the two-key shape).
		return c.Status(200).JSON(fiber.Map{"error": fiber.Map{
			"code": CodeHasOpenGroups, "message": MsgHasOpenGroups, "groups": open,
		}})
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE user_id = ? AND impersonated_by IS NULL`, id); err != nil {
		return failDB(c, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM reset_tokens WHERE user_id = ? AND source = 'SELF'`, id); err != nil {
		return failDB(c, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	s.publish(target.SchoolID, Frame{Entity: "user.changed", EntityID: id, Action: "deleted"})
	if target.Decision == "PENDING" {
		s.publishPending(ctx, me, target.SchoolID)
	}
	return ok(c, fiber.Map{"deleted": true})
}

// approveUser / rejectUser: decision transitions (FL-01 tail) — approve sets
// (APPROVED,true), reject (REJECTED,false) with the row retained.
func (s *Server) approveUser(c fiber.Ctx) error { return s.setUserDecision(c, "APPROVED", true) }
func (s *Server) rejectUser(c fiber.Ctx) error  { return s.setUserDecision(c, "REJECTED", false) }

func (s *Server) setUserDecision(c fiber.Ctx, decision string, active bool) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	me, _ := c.Locals(kvUser).(*userRow)
	target, okT := s.loadUserForEdit(ctx, c, id)
	if !okT {
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET decision = ?, is_active = ? WHERE id = ?`,
		decision, active, id); err != nil {
		return failDB(c, err)
	}
	s.publish(target.SchoolID, Frame{Entity: "user.changed", EntityID: id, Action: "updated"})
	s.publishPending(ctx, me, target.SchoolID)
	it, err := s.userItemByID(ctx, id)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil { // row vanished (raced delete)
		return fail(c, CodeNotFound, MsgNotFound)
	}
	return ok(c, fiber.Map{"user": it})
}

// activateUser / deactivateUser: is_active toggles; deactivation revokes the
// user's refresh rows in the SAME Tx (immediate logout, F-03).
func (s *Server) activateUser(c fiber.Ctx) error   { return s.setUserActive(c, true) }
func (s *Server) deactivateUser(c fiber.Ctx) error { return s.setUserActive(c, false) }

func (s *Server) setUserActive(c fiber.Ctx, active bool) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	target, okT := s.loadUserForEdit(ctx, c, id)
	if !okT {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET is_active = ? WHERE id = ?`, active, id); err != nil {
		return failDB(c, err)
	}
	if !active {
		if _, err := tx.ExecContext(ctx,
			`UPDATE refresh_tokens SET revoked_at = NOW()
			 WHERE user_id = ? AND revoked_at IS NULL`, id); err != nil {
			return failDB(c, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}
	s.publish(target.SchoolID, Frame{Entity: "user.changed", EntityID: id, Action: "updated"})
	it, err := s.userItemByID(ctx, id)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil { // row vanished (raced delete)
		return fail(c, CodeNotFound, MsgNotFound)
	}
	return ok(c, fiber.Map{"user": it})
}

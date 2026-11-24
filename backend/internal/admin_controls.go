package internal

// admin_controls.go — Feature A (admin "Login As" impersonation) and Feature B
// (administrative password reset), per
// docs/superpowers/specs/2026-11-11-admin-impersonation-password-reset-design.md.
//
// ONE (actor → target) predicate backs both features (§8.1 and §8.2 are
// identical by construction), so the two can never drift. Transport is the
// usual HTTP-200 envelope; none of these routes is idempotency-wrapped
// (spec §9.1/§10: one-shot credentials — a replay would hand back a dead
// session or rewrite a password).

import (
	"context"
	"database/sql"
	"log/slog"
	"strconv"

	"github.com/gofiber/fiber/v3"
)

// canAdminTarget is the single authorization predicate for both admin
// features. It returns ("", "") when allowed, otherwise the envelope
// code+message to write.
//
// Matrix (spec §8.1; §8.2 is identical):
//
//	             SA              ADMIN                FASILITATOR
//	SA           FORBIDDEN_ROLE  allowed (any school) allowed (any school)
//	ADMIN        FORBIDDEN_ROLE  FORBIDDEN_ROLE       own school only; foreign → NOT_FOUND
//	FASILITATOR  FORBIDDEN_ROLE  FORBIDDEN_ROLE       FORBIDDEN_ROLE
//
// Design call (spec §8.1): the pseudocode lists the ADMIN school check before
// the ADMIN role check, which would answer NOT_FOUND for ADMIN → SA (SA has a
// NULL school). The matrix and acceptance criterion 4 both require
// FORBIDDEN_ROLE, so the role check runs first here — it leaks no school
// information either.
func canAdminTarget(actor, target *userRow) (string, string) {
	if target == nil {
		return CodeNotFound, MsgNotFound
	}
	if actor == nil {
		return CodeForbiddenRole, MsgForbiddenRole
	}
	if target.ID == actor.ID {
		return CodeValidation, MsgValidation // use /auth/change-password instead
	}
	if actor.Role == "FASILITATOR" {
		return CodeForbiddenRole, MsgForbiddenRole
	}
	if actor.Role == "ADMIN" {
		if target.Role != "FASILITATOR" {
			return CodeForbiddenRole, MsgForbiddenRole
		}
		if target.SchoolID != actor.SchoolID {
			return CodeNotFound, MsgNotFound // no id oracle across schools
		}
	}
	if target.Role == "SA" {
		return CodeForbiddenRole, MsgForbiddenRole // SA never a target (A13/S8)
	}
	if target.Decision != "APPROVED" || !target.IsActive {
		return CodeAccountInactive, MsgAccountInactive
	}
	if !target.SchoolActive {
		return CodeSchoolInactive, MsgSchoolInactive
	}
	return "", ""
}

// loadAdminTarget parses :id, loads the target and applies canAdminTarget
// (scope → state → write, mirroring locked decision 68). false = an error
// envelope was already written; the caller returns nil.
func (s *Server) loadAdminTarget(ctx context.Context, c fiber.Ctx, actor *userRow) (*userRow, bool) {
	id, okID := idParam(c, "id")
	if !okID {
		return nil, false
	}
	target, err := s.loadUser(ctx, id)
	if err == sql.ErrNoRows {
		_ = fail(c, CodeNotFound, MsgNotFound)
		return nil, false
	}
	if err != nil {
		_ = failDB(c, err)
		return nil, false
	}
	if code, msg := canAdminTarget(actor, target); code != "" {
		_ = fail(c, code, msg)
		return nil, false
	}
	return target, true
}

// impersonateUser: POST /api/users/:id/impersonate (spec §9).
//
// One Tx revokes the actor's CURRENT jti (only when the presented cookie is
// the actor's) and inserts the target's session row carrying the actor ledger
// columns. The response is LoginData-compatible plus the additive
// `impersonation` block, and the cookie becomes the target's jti.
func (s *Server) impersonateUser(c fiber.Ctx) error {
	actor, _ := c.Locals(kvUser).(*userRow)
	ctx, cancel := dbCtx(c)
	defer cancel()
	target, okTarget := s.loadAdminTarget(ctx, c, actor)
	if !okTarget {
		return nil
	}

	// Safety check (spec §9.5): never revoke a jti that is not the actor's —
	// a stale/foreign cookie must not log out an unrelated user. No cookie (or
	// a mismatched one) means there is nothing to revoke; the ledger still gets
	// a stable random root.
	var oldJTI, root string
	if tok := c.Cookies(refreshCookie); tok != "" {
		if claims, err := s.parseRefresh(tok); err == nil &&
			claims.Subject == strconv.FormatInt(actor.ID, 10) {
			oldJTI = claims.ID
			root = claims.ID
		}
	}
	if root == "" {
		var err error
		root, err = randHex()
		if err != nil {
			return err
		}
	}
	newJTI, err := randHex()
	if err != nil {
		return err
	}
	exp := s.now().Add(s.cfg.RefreshTTL)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	if oldJTI != "" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE refresh_tokens SET revoked_at = NOW()
			 WHERE jti = ? AND user_id = ? AND revoked_at IS NULL`,
			oldJTI, actor.ID); err != nil {
			return failDB(c, err)
		}
	}
	if err := insertRefreshOn(ctx, tx, target, newJTI, exp, &actor.ID, root); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	// Always the TARGET's school, loaded directly: an SA's X-School-ID must not
	// leak into the response of a switch to another school (spec §9.2).
	school, err := s.schoolByID(ctx, target.SchoolID)
	if err != nil {
		return failDB(c, err)
	}
	slog.Info("admin impersonation", "actor_id", actor.ID, "actor_role", actor.Role,
		"target_id", target.ID, "target_role", target.Role, "school_id", target.SchoolID,
		"request_id", c.RequestID())

	imp := &impersonationJSON{ActorID: actor.ID, ActorName: actor.Name, ActorRole: actor.Role}
	return s.writeSession(c, target, newJTI, exp, school, imp)
}

// passwordAlphabet is the unambiguous generation alphabet (spec §10.1: no
// I, O, 0, 1, l).
const passwordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// generatePassword returns 16 characters from passwordAlphabet with at least
// one letter and one digit (so validPassword always passes by construction),
// drawn from crypto/rand and rejection-sampled to avoid modulo bias. Nothing
// is persisted, cached or logged.
func generatePassword() (string, error) {
	const size = 16
	limit := byte(256 - 256%len(passwordAlphabet)) // 228 — discard bytes >= 228
	for {
		out := make([]byte, 0, size)
		for len(out) < size {
			b, err := randBytes(1)
			if err != nil {
				return "", err
			}
			if b[0] >= limit {
				continue
			}
			out = append(out, passwordAlphabet[int(b[0])%len(passwordAlphabet)])
		}
		if pw := string(out); validPassword(pw) {
			return pw, nil
		}
	}
}

// generateUserPassword: POST /api/users/:id/password/generate (spec §10.1).
// Runs the same predicate as the apply call first, so a caller cannot obtain a
// password for a target it could not reset. Nothing is written.
func (s *Server) generateUserPassword(c fiber.Ctx) error {
	actor, _ := c.Locals(kvUser).(*userRow)
	ctx, cancel := dbCtx(c)
	defer cancel()
	target, okTarget := s.loadAdminTarget(ctx, c, actor)
	if !okTarget {
		return nil
	}
	pw, err := generatePassword()
	if err != nil {
		return err
	}
	slog.Info("admin password generate", "actor_id", actor.ID, "target_id", target.ID,
		"request_id", c.RequestID())
	return ok(c, fiber.Map{"password": pw})
}

type adminPasswordReq struct {
	Password string `json:"password"`
}

// adminResetUserPassword: POST /api/users/:id/password (spec §10.2-§10.3).
// The bcrypt hash is produced BEFORE the Tx (short transaction); one Tx
// rewrites the hash, revokes every live refresh row and appends the immutable
// ADMIN ledger row (code_plain NULL, used_at NOW() so it never appears on the
// reset dashboard).
func (s *Server) adminResetUserPassword(c fiber.Ctx) error {
	var req adminPasswordReq
	if !bindBody(c, &req) {
		return nil
	}
	if !validPassword(req.Password) {
		return fail(c, CodeValidation, MsgValidation)
	}
	actor, _ := c.Locals(kvUser).(*userRow)
	ctx, cancel := dbCtx(c)
	defer cancel()
	target, okTarget := s.loadAdminTarget(ctx, c, actor)
	if !okTarget {
		return nil
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		return err
	}
	nonce, err := randHex()
	if err != nil {
		return err
	}
	var school sql.NullInt64
	if target.Role != "SA" {
		school = sql.NullInt64{Int64: target.SchoolID, Valid: true}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, hash, target.ID); err != nil {
		return failDB(c, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = ? AND revoked_at IS NULL`,
		target.ID); err != nil {
		return failDB(c, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO reset_tokens (school_id, user_id, source, created_by, code_hash, code_plain, expires_at, used_at)
		 VALUES (?, ?, 'ADMIN', ?, ?, NULL, NOW(), NOW())`,
		school, target.ID, actor.ID, sha256Hex(nonce)); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	slog.Info("admin password reset", "actor_id", actor.ID, "target_id", target.ID,
		"school_id", target.SchoolID, "request_id", c.RequestID())
	return ok(c, fiber.Map{"reset": true})
}

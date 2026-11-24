// pscope.go — the shared P-route prologue (decision 18 / 02 §2b), extracted
// by finding 003a (F-02-02): one auditable place for the ~15-line guard
// sequence every sid+stid+cid handler used to copy-paste.
package internal

import (
	"context"

	"github.com/gofiber/fiber/v3"
)

// pScope runs the exact P-prefix prologue in the decision-18 order:
//
//  1. 3× idParam(sid, stid, cid) — malformed/missing id → 200 NOT_FOUND
//     (idParam writes the envelope itself; no oracle, 06 §1 E30).
//  2. Locals(school, user) — SA may read school 0 (unscoped reads).
//  3. dbCtx — the 5 s statement budget every DB call must use (05 §5).
//  4. requirePChild(sid, cid) — membership → NOT_FOUND first (no session
//     state leaks to non-members), then FAS ownership → NOT_OWNER.
//  5. photoScope(sid, stid) — stid∈sid linkage → NOT_FOUND.
//
// It writes failDB/fail itself for the two gates; ok=false means the
// envelope is already written and the handler must `return nil` (call sites
// bind the flag as okP — the package's `ok` envelope helper owns that
// name). The caller owns cancel (`defer cancel()`) exactly as with its
// former inline dbCtx.
//
// Variant prologues stay INLINE in their handlers so nothing reorders:
// requireSchool write-strictness (school 0 must fail VALIDATION BEFORE any
// gate), bindBody/FormFile validation that must answer before any SQL, the
// idempotent-wrapper gates, handlers without a stid (consents, batch), and
// gateReportItem (no fiber.Ctx — it maps gates to per-item codes, not
// envelopes).
func (s *Server) pScope(c fiber.Ctx) (ctx context.Context, cancel context.CancelFunc, sid, stid, cid, school int64, u *userRow, ok bool) {
	if sid, ok = idParam(c, "sid"); !ok {
		return nil, nil, 0, 0, 0, 0, nil, false
	}
	if stid, ok = idParam(c, "stid"); !ok {
		return nil, nil, 0, 0, 0, 0, nil, false
	}
	if cid, ok = idParam(c, "cid"); !ok {
		return nil, nil, 0, 0, 0, 0, nil, false
	}
	school, _ = c.Locals(kvSchool).(int64)
	u, _ = c.Locals(kvUser).(*userRow)

	ctx, cancel = dbCtx(c)
	if code, err := s.requirePChild(ctx, sid, cid, u.ID, u.Role); err != nil {
		cancel()
		failDB(c, err)
		return nil, nil, 0, 0, 0, 0, nil, false
	} else if code != "" {
		cancel()
		fail(c, code, msgForCode(code))
		return nil, nil, 0, 0, 0, 0, nil, false
	}
	if code, err := s.photoScope(ctx, sid, stid, school); err != nil {
		cancel()
		failDB(c, err)
		return nil, nil, 0, 0, 0, 0, nil, false
	} else if code != "" {
		cancel()
		fail(c, code, msgForCode(code))
		return nil, nil, 0, 0, 0, 0, nil, false
	}
	return ctx, cancel, sid, stid, cid, school, u, true
}

// consents.go — Consent domain (F-10, FL-11, decisions 7/12/18): send +
// resend of the per-child-per-session consent link (ADMIN/SA only; FAS
// read-only lists), paged consent lists, and the {token, decision, resent}
// envelope the contracts pin. The parent respond path lives in B3's
// public.go (first-wins UPDATE … WHERE PENDING) and writes the same
// consent_links rows read here.
//
// Precedence (FL-11, binding): FINISHED wins over decided — a decided link
// on a FINISHED session answers SESSION_LOCKED, never {resent:false}. WA
// failure answers 200 WA_FAILED but the row + token stay stored (link
// reusable: a retry with a fresh key resends the SAME token).
// Consent tokens: randTokenHex (64 hex chars = 32 random bytes, 05 §2
// CHAR(64)), NO expiry (decision 7).
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

// consentSortAllow is the shared consents sort allowlist (05 §3 — identical
// for both consent lists).
var consentSortAllow = map[string]string{
	"id": "cl.id", "child_name": "c.name", "status": "cl.status",
	"responded_at": "cl.responded_at",
}

type consentItem struct {
	ChildID        int64   `json:"child_id"`
	ChildName      string  `json:"child_name"`
	GroupName      *string `json:"group_name"`
	Status         string  `json:"status"`
	RespondentName *string `json:"respondent_name"`
	RespondedAt    *string `json:"responded_at"`
	ConsentURL     *string `json:"consent_url,omitempty"` // PENDING only (copy/resend)
}

// RegisterConsents mounts the consent routes: both lists are readable by any
// authenticated role (FAS scoped to its own groups/sessions, decision 12 —
// read-only status), send is ADMIN/SA (FAS → FORBIDDEN_ROLE).
func (s *Server) RegisterConsents(api fiber.Router) {
	api.Get("/sessions/:sid/consents", s.authed(), s.handleSessionConsents)
	api.Get("/consents", s.authed(), s.handleConsentList)
	api.Post("/sessions/:sid/children/:cid/send-consent",
		newLimiter(5, time.Minute, keyIP), s.authed("SA", "ADMIN"), s.handleSendConsent)
}

// consentScope builds the WHERE/args for one consent list (filters parsed by
// the caller; FAS adds its own scope predicate).
func (s *Server) consentListRows(ctx context.Context, p ListParams, where string, args []any) ([]consentItem, int64, error) {
	countQ := `SELECT COUNT(*) FROM consent_links cl
		JOIN children c ON c.id = cl.child_id
		LEFT JOIN session_members sm ON sm.session_id = cl.session_id AND sm.child_id = cl.child_id
		LEFT JOIN session_groups g ON g.id = sm.group_id ` + where
	var total int64
	if err := s.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	listQ := `SELECT cl.child_id, c.name, g.name, cl.status, cl.respondent_name,
		cl.responded_at, cl.token
		FROM consent_links cl
		JOIN children c ON c.id = cl.child_id
		LEFT JOIN session_members sm ON sm.session_id = cl.session_id AND sm.child_id = cl.child_id
		LEFT JOIN session_groups g ON g.id = sm.group_id ` + where +
		` ORDER BY ` + p.OrderBy("cl.id") + ` LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, listQ, append(append([]any{}, args...), p.PerPage, p.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []consentItem{}
	for rows.Next() {
		var (
			item        consentItem
			group       sql.NullString
			respondent  sql.NullString
			respondedAt sql.NullTime
			token       string
		)
		if err := rows.Scan(&item.ChildID, &item.ChildName, &group, &item.Status,
			&respondent, &respondedAt, &token); err != nil {
			return nil, 0, err
		}
		item.GroupName = fromNull(group)
		item.RespondentName = fromNull(respondent)
		if respondedAt.Valid {
			ts := fmtTS(respondedAt.Time)
			item.RespondedAt = &ts
		}
		if item.Status == "PENDING" { // URL while pending, omitted once decided
			url := s.consentURL(token)
			item.ConsentURL = &url
		}
		out = append(out, item)
	}
	return out, total, rows.Err()
}

// consentURL is the public parent link (05 §4: PUBLIC_WEB_BASE_URL +
// /view/consent?token=).
func (s *Server) consentURL(token string) string {
	return s.cfg.PublicWebURL + "/view/consent?token=" + token
}

// handleSessionConsents — GET /api/sessions/:sid/consents?status=&q= (paged).
// FAS sees only the consents of children in its own groups.
func (s *Server) handleSessionConsents(c fiber.Ctx) error {
	sid, okID := idParam(c, "sid")
	if !okID {
		return nil
	}
	school, _ := c.Locals(kvSchool).(int64)
	u, _ := c.Locals(kvUser).(*userRow)
	p, okList := listParams(c, "cl.id", "asc", consentSortAllow)
	if !okList {
		return nil // VALIDATION already written by listParams
	}
	var filt struct {
		Status string `query:"status"`
	}
	_ = c.Bind().Query(&filt)
	if filt.Status != "" && filt.Status != "PENDING" && filt.Status != "SETUJU" && filt.Status != "DITOLAK" {
		return fail(c, CodeValidation, MsgValidation)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	if _, err := s.scopedStatus(ctx, sid, school); err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	} else if err != nil {
		return failDB(c, err)
	}

	where := " WHERE cl.session_id = ?"
	args := []any{sid}
	if tcond, targs := schoolCond(school, "cl.school_id"); tcond != "" {
		where += tcond
		args = append(args, targs...)
	}
	if filt.Status != "" {
		where += ` AND cl.status = ?`
		args = append(args, filt.Status)
	}
	if u.Role == "FASILITATOR" {
		ownGroups, err := s.fasOwnScope(ctx, u, sid, school)
		if err == errNotVisible {
			return fail(c, CodeNotFound, MsgNotFound) // same visibility as detail
		}
		if err != nil {
			return failDB(c, err)
		}
		if len(ownGroups) == 0 {
			return okPaged(c, []consentItem{}, p.Page, p.PerPage, 0)
		}
		ids := make([]any, 0, len(ownGroups))
		for gid := range ownGroups {
			ids = append(ids, gid)
		}
		where += ` AND sm.group_id IN (` + placeholders(len(ids)) + `)`
		args = append(args, ids...)
	}
	// ?q (F-B15, audit C13): substring over child/group/respondent name —
	// the predicate mirrors the paged users/children lists.
	if p.Q != "" {
		where += ` AND (c.name LIKE ? OR g.name LIKE ? OR cl.respondent_name LIKE ?)`
		args = append(args, "%"+p.Q+"%", "%"+p.Q+"%", "%"+p.Q+"%")
	}

	out, total, err := s.consentListRows(ctx, p, where, args)
	if err != nil {
		return failDB(c, err)
	}
	return okPaged(c, out, p.Page, p.PerPage, total)
}

// handleConsentList — GET /api/consents?session_id=&child_id=&status=&q=
// (paged, school-scoped; FAS limited to its owned sessions).
func (s *Server) handleConsentList(c fiber.Ctx) error {
	school, _ := c.Locals(kvSchool).(int64)
	u, _ := c.Locals(kvUser).(*userRow)
	p, okList := listParams(c, "cl.id", "asc", consentSortAllow)
	if !okList {
		return nil // VALIDATION already written by listParams
	}
	var filt struct {
		Status    string `query:"status"`
		SessionID string `query:"session_id"`
		ChildID   string `query:"child_id"`
	}
	_ = c.Bind().Query(&filt)
	if filt.Status != "" && filt.Status != "PENDING" && filt.Status != "SETUJU" && filt.Status != "DITOLAK" {
		return fail(c, CodeValidation, MsgValidation)
	}

	where := " WHERE 1=1"
	var args []any
	if tcond, targs := schoolCond(school, "cl.school_id"); tcond != "" {
		where += tcond
		args = append(args, targs...)
	}
	if filt.Status != "" {
		where += ` AND cl.status = ?`
		args = append(args, filt.Status)
	}
	if filt.SessionID != "" {
		n, err := strconv.ParseInt(filt.SessionID, 10, 64)
		if err != nil || n <= 0 {
			return fail(c, CodeValidation, MsgValidation)
		}
		where += ` AND cl.session_id = ?`
		args = append(args, n)
	}
	if filt.ChildID != "" {
		n, err := strconv.ParseInt(filt.ChildID, 10, 64)
		if err != nil || n <= 0 {
			return fail(c, CodeValidation, MsgValidation)
		}
		where += ` AND cl.child_id = ?`
		args = append(args, n)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	if u.Role == "FASILITATOR" {
		_, sessions, err := s.fasSessionScope(ctx, u.ID, school)
		if err != nil {
			return failDB(c, err)
		}
		if len(sessions) == 0 {
			return okPaged(c, []consentItem{}, p.Page, p.PerPage, 0)
		}
		ids := make([]any, 0, len(sessions))
		for _, sid := range sessions {
			ids = append(ids, sid)
		}
		where += ` AND cl.session_id IN (` + placeholders(len(ids)) + `)`
		args = append(args, ids...)
	}
	// ?q (F-B15, audit C13): substring over child/group/respondent name —
	// the predicate mirrors the paged users/children lists.
	if p.Q != "" {
		where += ` AND (c.name LIKE ? OR g.name LIKE ? OR cl.respondent_name LIKE ?)`
		args = append(args, "%"+p.Q+"%", "%"+p.Q+"%", "%"+p.Q+"%")
	}

	out, total, err := s.consentListRows(ctx, p, where, args)
	if err != nil {
		return failDB(c, err)
	}
	return okPaged(c, out, p.Page, p.PerPage, total)
}

// handleSendConsent — POST .../send-consent (ADMIN/SA, idempotent, 5/min/IP).
// First send creates row+token and WAs the mother; resend while PENDING
// reuses the token; decided links answer {token, decision, resent:false}
// without a send; FINISHED sessions are always SESSION_LOCKED.
func (s *Server) handleSendConsent(c fiber.Ctx) error {
	sid, okS := idParam(c, "sid")
	if !okS {
		return nil
	}
	cid, okC := idParam(c, "cid")
	if !okC {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}

	return s.idempotent(c, "POST "+c.Path(), func() error {
		ctx, cancel := dbCtx(c)
		defer cancel()

		// Precedence (FL-11): FINISHED wins over decided — checked before the
		// consent row branch. Membership first (decision 18, no leak), then
		// the inactive-child gate (06 §5), then session status.
		status, err := s.scopedStatus(ctx, sid, school)
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		if err != nil {
			return failDB(c, err)
		}
		member, err := s.requireMembership(ctx, sid, cid)
		if err != nil {
			return failDB(c, err)
		}
		if !member {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		var motherWa string
		var active bool
		if err := s.db.QueryRowContext(ctx,
			`SELECT mother_wa, is_active FROM children
			 WHERE id = ? AND school_id = ?`, cid, school).
			Scan(&motherWa, &active); err != nil {
			if err == sql.ErrNoRows {
				return fail(c, CodeNotFound, MsgNotFound)
			}
			return failDB(c, err)
		}
		if !active {
			return fail(c, CodeChildInactive, MsgChildInactive)
		}
		if status == "FINISHED" {
			return fail(c, CodeSessionLocked, MsgSessionLocked)
		}

		// Load or create the row (UNIQUE(session, child); token permanent).
		var (
			consentID int64
			token     string
			decision  string
			created   bool
		)
		err = s.db.QueryRowContext(ctx,
			`SELECT id, token, status FROM consent_links
			 WHERE session_id = ? AND child_id = ?`, sid, cid).
			Scan(&consentID, &token, &decision)
		if err == sql.ErrNoRows {
			token, err = randTokenHex()
			if err != nil {
				return err
			}
			res, insErr := s.db.ExecContext(ctx,
				`INSERT INTO consent_links (school_id, session_id, child_id, token, status)
				 VALUES (?, ?, ?, ?, 'PENDING')`, school, sid, cid, token)
			if insErr != nil {
				if !isDuplicateErr(insErr) {
					return failDB(c, insErr)
				}
				// Lost the create race: fall back to the existing row.
				if err := s.db.QueryRowContext(ctx,
					`SELECT id, token, status FROM consent_links
					 WHERE session_id = ? AND child_id = ?`, sid, cid).
					Scan(&consentID, &token, &decision); err != nil {
					return failDB(c, err)
				}
			} else {
				consentID, _ = res.LastInsertId()
				decision, created = "PENDING", true
			}
		} else if err != nil {
			return failDB(c, err)
		}

		if decision != "PENDING" {
			// Already decided: link unchanged, WA NOT re-sent (FL-11).
			return ok(c, fiber.Map{"token": token, "decision": decision, "resent": false})
		}
		resent := !created

		// Row committed before the WA call — a failure keeps the link usable.
		if created {
			s.publish(school, Frame{
				Entity: "consent.changed", EntityID: consentID,
				SessionID: &sid, ChildID: cid, Action: "created",
			})
		} else {
			s.publish(school, Frame{
				Entity: "consent.changed", EntityID: consentID,
				SessionID: &sid, ChildID: cid, Action: "updated",
			})
		}

		text := renderVars(s.tplConsent, map[string]string{
			"ConsentURL": s.consentURL(token),
		})
		chatID := strings.TrimPrefix(motherWa, "+") + "@c.us"
		messageID, waErr := s.sendText(chatID, text)

		// Outbox audit row (05 §2: payload_ref = consent URL). Best-effort:
		// a successful send is never turned into a failure by audit trouble.
		outStatus := "SENT"
		var outErr any
		if waErr != nil {
			outStatus, outErr = "FAILED", waErr.Error()
		}
		var messageArg any
		if messageID != "" {
			messageArg = messageID
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO wa_outbox
			 (school_id, kind, target_wa, payload_ref, status, message_id, error, idem_key)
			 VALUES (?, 'CONSENT', ?, ?, ?, ?, ?, ?)`,
			school, motherWa, s.consentURL(token), outStatus, messageArg,
			outErr, c.Get("X-Idempotency-Key")); err != nil {
			slog.Error("consent outbox row", "err", err, "session_id", sid, "child_id", cid)
		}
		if waErr != nil {
			return fail(c, CodeWAFailed, MsgWAFailed)
		}
		return ok(c, fiber.Map{"token": token, "decision": "PENDING", "resent": resent})
	})
}

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

// sessionConsentSortAllow drives the per-session list, which starts from
// session_members (never-sent members have no consent row to sort by).
var sessionConsentSortAllow = map[string]string{
	"id": "sm.child_id", "child_name": "c.name", "group_name": "g.name",
	"status": "cl.status", "responded_at": "cl.responded_at",
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
	// Batch bulk-send (consent table multi-select). One request carries the
	// selection, so the 5/min per-child limiter does not throttle a whole
	// class; idempotency still protects retries.
	api.Post("/sessions/:sid/send-consent",
		s.authed("SA", "ADMIN"), s.handleSendConsentBatch)
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

// sessionConsentRows lists ONE session's consent table from its membership:
// a child with no consent_links row is a real row with the synthetic
// NOT_SENT status (decision 6 — "pending" only means a request was sent).
func (s *Server) sessionConsentRows(ctx context.Context, p ListParams, where string, args []any) ([]consentItem, int64, error) {
	from := ` FROM session_members sm
		JOIN children c ON c.id = sm.child_id
		LEFT JOIN consent_links cl ON cl.session_id = sm.session_id AND cl.child_id = sm.child_id
		LEFT JOIN session_groups g ON g.id = sm.group_id `
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+from+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	listQ := `SELECT sm.child_id, c.name, g.name,
		COALESCE(cl.status, 'NOT_SENT'), cl.respondent_name, cl.responded_at, cl.token` +
		from + where + ` ORDER BY ` + p.OrderBy("sm.child_id") + ` LIMIT ? OFFSET ?`
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
			token       sql.NullString
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
		if item.Status == "PENDING" && token.Valid {
			url := s.consentURL(token.String)
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
	p, okList := listParams(c, "sm.child_id", "asc", sessionConsentSortAllow)
	if !okList {
		return nil // VALIDATION already written by listParams
	}
	var filt struct {
		Status  string `query:"status"`
		GroupID string `query:"group_id"`
	}
	_ = c.Bind().Query(&filt)
	if filt.Status != "" && filt.Status != "NOT_SENT" && filt.Status != "PENDING" &&
		filt.Status != "SETUJU" && filt.Status != "DITOLAK" {
		return fail(c, CodeValidation, MsgValidation)
	}
	var groupID int64
	if filt.GroupID != "" {
		n, err := strconv.ParseInt(filt.GroupID, 10, 64)
		if err != nil || n <= 0 {
			return fail(c, CodeValidation, MsgValidation)
		}
		groupID = n
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	if _, err := s.scopedStatus(ctx, sid, school); err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	} else if err != nil {
		return failDB(c, err)
	}

	where := " WHERE sm.session_id = ?"
	args := []any{sid}
	if tcond, targs := schoolCond(school, "sm.school_id"); tcond != "" {
		where += tcond
		args = append(args, targs...)
	}
	switch filt.Status {
	case "":
	case "NOT_SENT":
		where += ` AND cl.id IS NULL`
	default:
		where += ` AND cl.status = ?`
		args = append(args, filt.Status)
	}
	if groupID != 0 {
		where += ` AND sm.group_id = ?`
		args = append(args, groupID)
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

	out, total, err := s.sessionConsentRows(ctx, p, where, args)
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

// consentSendOutcome is one child's send result. Batch items serialize it as
// {child_id, status, code?}; the single endpoint maps it to the FL-11
// {token, decision, resent} envelope.
type consentSendOutcome struct {
	ChildID  int64  `json:"child_id"`
	Token    string `json:"-"`
	Decision string `json:"decision,omitempty"`
	Resent   bool   `json:"resent,omitempty"`
	Status   string `json:"status,omitempty"`
	Code     string `json:"code,omitempty"`
}

// sendConsentOne performs the per-child send (membership → inactive → status
// precedence → row/token → WA → outbox). DB faults are returned; business
// outcomes are carried on the struct so batch can continue per item.
func (s *Server) sendConsentOne(
	ctx context.Context, school, sid, cid int64, sessionStatus, idemKey string,
) (consentSendOutcome, error) {
	out := consentSendOutcome{ChildID: cid}

	member, err := s.requireMembership(ctx, sid, cid)
	if err != nil {
		return out, err
	}
	if !member {
		out.Code = CodeNotFound
		return out, nil
	}
	var motherWa string
	var active bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT mother_wa, is_active FROM children
		 WHERE id = ? AND school_id = ?`, cid, school).
		Scan(&motherWa, &active); err != nil {
		if err == sql.ErrNoRows {
			out.Code = CodeNotFound
			return out, nil
		}
		return out, err
	}
	if !active {
		out.Code = CodeChildInactive
		return out, nil
	}
	// Precedence (FL-11): FINISHED wins over decided — checked before the
	// consent row branch.
	if sessionStatus == "FINISHED" {
		out.Code = CodeSessionLocked
		return out, nil
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
			return out, err
		}
		res, insErr := s.db.ExecContext(ctx,
			`INSERT INTO consent_links (school_id, session_id, child_id, token, status)
			 VALUES (?, ?, ?, ?, 'PENDING')`, school, sid, cid, token)
		if insErr != nil {
			if !isDuplicateErr(insErr) {
				return out, insErr
			}
			// Lost the create race: fall back to the existing row.
			if err := s.db.QueryRowContext(ctx,
				`SELECT id, token, status FROM consent_links
				 WHERE session_id = ? AND child_id = ?`, sid, cid).
				Scan(&consentID, &token, &decision); err != nil {
				return out, err
			}
		} else {
			consentID, _ = res.LastInsertId()
			decision, created = "PENDING", true
		}
	} else if err != nil {
		return out, err
	}

	if decision != "PENDING" {
		// Already decided: link unchanged, WA NOT re-sent (FL-11).
		out.Token, out.Decision, out.Status = token, decision, "SKIPPED"
		return out, nil
	}
	resent := !created

	// Row committed before the WA call — a failure keeps the link usable.
	action := "updated"
	if created {
		action = "created"
	}
	s.publish(school, Frame{
		Entity: "consent.changed", EntityID: consentID,
		SessionID: &sid, ChildID: cid, Action: action,
	})

	text := renderVars(s.tplConsent, map[string]string{
		"ConsentURL": s.consentURL(token),
	})
	chatID := strings.TrimPrefix(motherWa, "+") + "@c.us"
	messageID, waErr := s.sendText(chatID, text)

	// Outbox audit row (05 §2: payload_ref = consent URL). Best-effort:
	// a successful send is never turned into a failure by audit trouble.
	// The WA call has its own 10 s budget and can outlive the request-scoped
	// dbCtx (5 s), so the audit write gets a fresh bounded context; otherwise
	// a delivered consent leaves no wa_outbox row (context deadline exceeded).
	outStatus := "SENT"
	var outErr any
	if waErr != nil {
		outStatus, outErr = "FAILED", waErr.Error()
	}
	var messageArg any
	if messageID != "" {
		messageArg = messageID
	}
	auditCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(auditCtx,
		`INSERT INTO wa_outbox
		 (school_id, kind, target_wa, payload_ref, status, message_id, error, idem_key)
		 VALUES (?, 'CONSENT', ?, ?, ?, ?, ?, ?)`,
		school, motherWa, s.consentURL(token), outStatus, messageArg,
		outErr, idemKey); err != nil {
		slog.Error("consent outbox row", "err", err, "session_id", sid, "child_id", cid)
	}
	out.Token, out.Decision, out.Resent = token, "PENDING", resent
	if waErr != nil {
		out.Status, out.Code = "FAILED", CodeWAFailed
		return out, nil
	}
	out.Status = "SENT"
	return out, nil
}

// consentMessage maps a per-item business code to its canonical message.
func consentMessage(code string) string {
	switch code {
	case CodeNotFound:
		return MsgNotFound
	case CodeChildInactive:
		return MsgChildInactive
	case CodeSessionLocked:
		return MsgSessionLocked
	case CodeWAFailed:
		return MsgWAFailed
	default:
		return MsgValidation
	}
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

		status, err := s.scopedStatus(ctx, sid, school)
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		if err != nil {
			return failDB(c, err)
		}
		out, err := s.sendConsentOne(ctx, school, sid, cid, status, c.Get("X-Idempotency-Key"))
		if err != nil {
			return failDB(c, err)
		}
		if out.Code != "" {
			return fail(c, out.Code, consentMessage(out.Code))
		}
		return ok(c, fiber.Map{"token": out.Token, "decision": out.Decision, "resent": out.Resent})
	})
}

// handleSendConsentBatch — POST /api/sessions/:sid/send-consent (ADMIN/SA,
// idempotent, no per-IP limiter: ONE request carries the whole selection).
// Each child is gated independently; WA failures stay per item so the rest
// of the class is still sent. Already-decided links come back SKIPPED.
func (s *Server) handleSendConsentBatch(c fiber.Ctx) error {
	sid, okS := idParam(c, "sid")
	if !okS {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	var req struct {
		ChildIDs []int64 `json:"child_ids"`
	}
	if !bindBody(c, &req) {
		return nil
	}
	if len(req.ChildIDs) == 0 || len(req.ChildIDs) > 200 {
		return fail(c, CodeValidation, MsgValidation)
	}
	seen := make(map[int64]struct{}, len(req.ChildIDs))
	ids := make([]int64, 0, len(req.ChildIDs))
	for _, cid := range req.ChildIDs {
		if cid <= 0 {
			return fail(c, CodeValidation, MsgValidation)
		}
		if _, dup := seen[cid]; dup {
			continue
		}
		seen[cid] = struct{}{}
		ids = append(ids, cid)
	}

	return s.idempotent(c, "POST "+c.Path(), func() error {
		ctx, cancel := dbCtx(c)
		defer cancel()

		status, err := s.scopedStatus(ctx, sid, school)
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		if err != nil {
			return failDB(c, err)
		}
		// FINISHED is a session-wide gate for every selected child.
		if status == "FINISHED" {
			return fail(c, CodeSessionLocked, MsgSessionLocked)
		}

		items := make([]consentSendOutcome, 0, len(ids))
		for _, cid := range ids {
			out, err := s.sendConsentOne(ctx, school, sid, cid, status, c.Get("X-Idempotency-Key"))
			if err != nil {
				return failDB(c, err)
			}
			items = append(items, out)
		}
		return ok(c, fiber.Map{"items": items})
	})
}

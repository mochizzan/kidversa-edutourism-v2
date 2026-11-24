// sessions.go — Session domain (F-08, FL-07/08, decision 5/8/13/17/20/21):
// session CRUD + snapshot create, detail (ONE response: session+topics+groups+
// scope), guarded lifecycle transitions, groups/members with option-B cascade
// wipes, copy-data, and the shared helpers B2/B3 build on.
//
// # Exported helpers for B3 (exact signatures — pinned by the orchestrator)
//
//	requireMembership(ctx context.Context, sid, cid int64) (bool, error)
//	    TRUE when a session_members row exists for (session, child). Membership
//	    gate for every per-child-per-topic P op (decision 18): non-member →
//	    200 NOT_FOUND.
//	wipeChildSessionPhotos(ctx context.Context, school, sid, cid int64) ([]string, error)
//	    Deletes ALL photo rows for one child+session (original + copied) in
//	    ONE Tx and returns the DB-relative paths; the caller unlinks those
//	    files AFTER the function returns (post-commit) and then publishes.
//	    Used by B3's consent DITOLAK respond path.
//	reportReadiness(ctx, school, sid, cid, stid) (complete bool,
//	    missing []MissingItem, topicBadge, programBadge bool, err error)
//	    GET P/readiness shape (contracts): {complete, missing:[{said,name}],
//	    badges:{topic,program}} — lives in assessments.go with MissingItem.
//	scopedStatus(ctx context.Context, sid, school int64) (string, error)
//	    DRAFT|OPENED|FINISHED inside one school scope; sql.ErrNoRows when the
//	    session is unknown in that school (callers map to 200 NOT_FOUND).
//
// # Scope provider (stream.go seam, installed from RegisterSessions)
//
// The SAME helpers feed REST scoping and the SSE ScopeSnapshot (decision 24 —
// one predicate, never duplicated): fasSessionScope (own groups + owned
// sessions, used by GET /api/sessions FAS filtering) + B1's fasChildScope
// (children in own groups, shared with GET /api/children).
//
// # Spec interpretations (reported to the orchestrator)
//
//   - copy_available hint: FL-07/§2c ask for the hint inside member.changed /
//     session.changed frames → Frame.CopyAvailable carries it per added and
//     per removed child (computed post-commit; a failed computation is
//     logged and the frame ships without the hint, never failing the
//     mutation).
//   - Open-gate failures (GROUP_WITHOUT_FACILITATOR / VALIDATION) carry the
//     offending groups as an extra `error.groups:[{gid,name,reason}]` key
//     (B11 / 02 §2b + 08); finish/reopen gates have no offender list.
//   - is_candidate is NEVER copied (05 §2 "Copy-data NEVER copies
//     is_candidate" — F-15/decision-21 "candidate preserved" wording is the
//     spec's internal conflict; 05 §2 rationale wins: a pending row must not
//     block the target's candidate slot).
//   - Photos: GET/POST gate on consent SETUJU (any session status — 06 F-10
//     "no upload/capture/view … all roles"), so Draft photos are allowed the
//     moment the parent approves; Finished stays free of SESSION_LOCKED.
//   - Member remove / group delete wipe consent+public link rows of the
//     affected children too (session-scoped data, decision 13) and publish
//     report-link.changed + link.revoked for every token that dies, plus one
//     consent.changed so the consent list refetches.
//   - GET /api/sessions/:sid/groups stays readable in Finished (the
//     Draft/Opened + SESSION_LOCKED rule in 02 §2b guards mutation).
package internal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
)

// queryRower unifies *sql.DB and *sql.Tx for the shared row lookups.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// placeholders builds "?,?,?" for an IN list.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// schoolCond returns the school predicate used by scoped reads; school 0 is
// SA without X-School-ID, which reads across schools (W1 reset-list
// precedent). Every write goes through requireSchool first, so school > 0
// there.
func schoolCond(school int64, col string) (string, []any) {
	if school > 0 {
		return " AND " + col + " = ?", []any{school}
	}
	return "", nil
}

// toNull maps "" to SQL NULL (nullable string columns store NULL, not "").
func toNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// fromNull dereferences a scanned nullable string.
func fromNull(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	return &ns.String
}

// fmtDate renders a DATE column (local wall clock from the driver) — never
// UTC-shifted, or the calendar day would move in non-UTC deployments.
func fmtDate(t time.Time) string { return t.Format("2006-01-02") }

// fmtTS renders a TIMESTAMP column as RFC3339 UTC (contracts).
func fmtTS(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// ---------------------------------------------------------------------------
// Wire shapes
// ---------------------------------------------------------------------------

type sessionItem struct {
	ID          int64   `json:"id"`
	ProgramID   int64   `json:"program_id"`
	ProgramName string  `json:"program_name"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Date        string  `json:"date"`
	StartTime   *string `json:"start_time"`
	EndTime     *string `json:"end_time"`
	Location    string  `json:"location"`
	Status      string  `json:"status"`
	GroupCount  int     `json:"group_count"`
	MemberCount int     `json:"member_count"`
	CreatedAt   string  `json:"created_at"`
}

// sessionJSON is the detail/patch `session` object (contracts).
type sessionJSON struct {
	ID          int64   `json:"id"`
	ProgramID   int64   `json:"program_id"`
	ProgramName string  `json:"program_name"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Date        string  `json:"date"`
	StartTime   *string `json:"start_time"`
	EndTime     *string `json:"end_time"`
	Location    string  `json:"location"`
	Status      string  `json:"status"`
	CreatedBy   *int64  `json:"created_by"`
	CreatedAt   string  `json:"created_at"`
}

type detailActivity struct {
	Said       int64   `json:"said"`
	ActivityID int64   `json:"activity_id"`
	Name       string  `json:"name"`
	Indicator  *string `json:"indicator"`
	SortOrder  int     `json:"sort_order"`
}

type detailTopic struct {
	Stid       int64            `json:"stid"`
	TopicID    int64            `json:"topic_id"`
	Name       string           `json:"name"`
	SortOrder  int              `json:"sort_order"`
	Activities []detailActivity `json:"activities"`
}

type detailMember struct {
	ChildID int64   `json:"child_id"`
	Name    string  `json:"name"`
	Age     int     `json:"age"`
	School  string  `json:"school"`
	Consent *string `json:"consent"` // null = consent never sent
}

type detailOwner struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// groupFull is the own-group shape; foreign FAS groups use groupReduced
// (contracts pin the reduction to gid/name/own/member_count; user decision
// 2026-10-11 adds `status` — the parent session's status, session_groups
// has no status column of its own).
type groupFull struct {
	GID          int64          `json:"gid"`
	Name         string         `json:"name"`
	Own          bool           `json:"own"`
	Owner        *detailOwner   `json:"owner"` // null when owner missing
	OwnerMissing bool           `json:"owner_missing"`
	MemberCount  int            `json:"member_count"`
	Consent      map[string]int `json:"consent"`
	Members      []detailMember `json:"members"`
}

type groupReduced struct {
	GID         int64  `json:"gid"`
	Name        string `json:"name"`
	Own         bool   `json:"own"`
	MemberCount int    `json:"member_count"`
	Status      string `json:"status"` // parent session status (DRAFT/OPENED/FINISHED)
}

// sessionScope is the detail wipe-counts object (typed-text delete modal).
type sessionScope struct {
	Topics       int `json:"topics"`
	Groups       int `json:"groups"`
	Members      int `json:"members"`
	Assessments  int `json:"assessments"`
	Photos       int `json:"photos"`
	Narratives   int `json:"narratives"`
	ReportsSent  int `json:"reports_sent"`
	LinksReport  int `json:"links_report"`
	LinksGallery int `json:"links_gallery"`
	LinksConsent int `json:"links_consent"`
}

// ---------------------------------------------------------------------------
// Scope: one predicate for REST list + SSE snapshot (decision 24)
// ---------------------------------------------------------------------------

// fasSessionScope returns the facilitator's own group ids and the ids of
// sessions containing them — the SINGLE session-scope query used by both
// GET /api/sessions (FAS filter) and the ScopeProvider installed below.
func (s *Server) fasSessionScope(ctx context.Context, userID, schoolID int64) (groupIDs, sessionIDs []int64, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id FROM session_groups
		 WHERE facilitator_id = ? AND school_id = ? ORDER BY id`, userID, schoolID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	groupIDs = make([]int64, 0, 8)
	sessionIDs = make([]int64, 0, 8)
	for rows.Next() {
		var gid, sid int64
		if err := rows.Scan(&gid, &sid); err != nil {
			return nil, nil, err
		}
		groupIDs = append(groupIDs, gid)
		if !slices.Contains(sessionIDs, sid) {
			sessionIDs = append(sessionIDs, sid)
		}
	}
	return groupIDs, sessionIDs, rows.Err()
}

// installScopeProvider wires the stream ScopeSnapshot resolver: own groups +
// owned sessions from fasSessionScope, child scope from B1's fasChildScope
// (the exact helper GET /api/children uses). Non-facilitators are
// unrestricted (school + role already scope their frames).
func (s *Server) installScopeProvider() {
	s.registerScopeProvider(func(ctx context.Context, userID int64, role string, schoolID int64) (ScopeSnapshot, error) {
		sc := ScopeSnapshot{Role: role, SchoolID: schoolID, UserID: userID}
		if role != "FASILITATOR" {
			return sc, nil
		}
		groups, sessions, err := s.fasSessionScope(ctx, userID, schoolID)
		if err != nil {
			return sc, err
		}
		childIDs, _, err := s.fasChildScope(ctx, role, userID, schoolID)
		if err != nil {
			return sc, err
		}
		sc.OwnGroupIDs = groups
		sc.OwnedSessionIDs = sessions
		sc.ChildIDs = childIDs
		return sc, nil
	})
}

// ---------------------------------------------------------------------------
// Shared lookups
// ---------------------------------------------------------------------------

// requireMembership reports whether a session_members row exists (decision 18
// membership gate for every P op and send-consent).
func (s *Server) requireMembership(ctx context.Context, sid, cid int64) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)`,
		sid, cid).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists == 1, nil
}

// loadSessionJSON reads the detail `session` object (school-scoped).
// sql.ErrNoRows = session unknown in this school.
func (s *Server) loadSessionJSON(ctx context.Context, sid, school int64) (*sessionJSON, error) {
	tcond, targs := schoolCond(school, "school_id")
	row := s.db.QueryRowContext(ctx,
		`SELECT id, program_id, program_name_snap, name, description, date, start_time,
		        end_time, location, status, created_by, created_at
		 FROM sessions WHERE id = ?`+tcond, append([]any{sid}, targs...)...)
	var (
		sess      sessionJSON
		desc      sql.NullString
		startT    sql.NullString
		endT      sql.NullString
		createdBy sql.NullInt64
		date      time.Time
		createdAt time.Time
	)
	if err := row.Scan(&sess.ID, &sess.ProgramID, &sess.ProgramName, &sess.Name, &desc,
		&date, &startT, &endT, &sess.Location, &sess.Status, &createdBy, &createdAt); err != nil {
		return nil, err
	}
	sess.Description = fromNull(desc)
	sess.StartTime = fromNull(startT)
	sess.EndTime = fromNull(endT)
	sess.Date = fmtDate(date)
	sess.CreatedAt = fmtTS(createdAt)
	if createdBy.Valid {
		sess.CreatedBy = new(createdBy.Int64)
	}
	return &sess, nil
}

// ownerOK is the open/assessment gate: the group's owner must be an active,
// approved FASILITATOR of the school (E14 when not).
func ownerOK(ctx context.Context, q queryRower, school, fid int64) (bool, error) {
	var ok int
	err := q.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM users
		 WHERE id = ? AND school_id = ? AND role = 'FASILITATOR'
		   AND decision = 'APPROVED' AND is_active = TRUE)`, fid, school).Scan(&ok)
	return ok == 1, err
}

// requestOwner validates a client-supplied owner id (create/groups PATCH).
// Returns "" when valid, else the error code to fail with.
func (s *Server) requestOwner(ctx context.Context, q queryRower, school, fid int64) (string, error) {
	var role, decision string
	var active bool
	var ownerSchool sql.NullInt64
	err := q.QueryRowContext(ctx,
		`SELECT role, decision, is_active, school_id FROM users WHERE id = ?`, fid).
		Scan(&role, &decision, &active, &ownerSchool)
	switch {
	case err == sql.ErrNoRows:
		return CodeNotFound, nil
	case err != nil:
		return "", err
	}
	if !ownerSchool.Valid || ownerSchool.Int64 != school {
		return CodeNotFound, nil // cross-school id: no leak
	}
	if role != "FASILITATOR" {
		return CodeValidation, nil
	}
	if decision != "APPROVED" || !active {
		return CodeGroupNoFacilitator, nil
	}
	return "", nil
}

// lockSessionEditable claims the session row (FOR UPDATE) inside the caller's
// Tx and verifies it is DRAFT/OPENED — the guarded status gate every
// group/member mutation runs behind. false = NOT_FOUND or SESSION_LOCKED
// already written to the envelope.
func (s *Server) lockSessionEditable(ctx context.Context, tx *sql.Tx, c fiber.Ctx, school, sid int64) (bool, error) {
	tcond, targs := schoolCond(school, "school_id")
	var status string
	err := tx.QueryRowContext(ctx,
		`SELECT status FROM sessions WHERE id = ?`+tcond+` FOR UPDATE`,
		append([]any{sid}, targs...)...).Scan(&status)
	if err == sql.ErrNoRows {
		return false, fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return false, failDB(c, err)
	}
	if status == "FINISHED" {
		return false, fail(c, CodeSessionLocked, MsgSessionLocked)
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Routes
// ---------------------------------------------------------------------------

// RegisterSessions mounts the session routes and installs the stream scope
// provider. Params use :sid everywhere (radix consistency across files).
func (s *Server) RegisterSessions(api fiber.Router) {
	s.installScopeProvider()

	api.Get("/sessions", s.authed(), s.handleSessionList)
	api.Post("/sessions", s.authed("SA", "ADMIN"), s.handleSessionCreate)
	api.Get("/sessions/:sid", s.authed(), s.handleSessionDetail)
	api.Patch("/sessions/:sid", s.authed("SA", "ADMIN"), s.handleSessionPatch)
	api.Delete("/sessions/:sid", s.authed("SA", "ADMIN"), s.handleSessionDelete)
	api.Post("/sessions/:sid/open", s.authed("SA", "ADMIN"), s.handleSessionOpen)
	api.Post("/sessions/:sid/finish", s.authed("SA", "ADMIN"), s.handleSessionFinish)
	api.Post("/sessions/:sid/reopen", s.authed("SA", "ADMIN"), s.handleSessionReopen)
	api.Get("/sessions/:sid/readiness", s.authed(), s.handleSessionReadiness)

	api.Get("/sessions/:sid/groups", s.authed(), s.handleGroupList)
	api.Post("/sessions/:sid/groups", s.authed("SA", "ADMIN"), s.handleGroupCreate)
	api.Patch("/sessions/:sid/groups/:gid", s.authed("SA", "ADMIN"), s.handleGroupPatch)
	api.Delete("/sessions/:sid/groups/:gid", s.authed("SA", "ADMIN"), s.handleGroupDelete)
	api.Post("/sessions/:sid/groups/:gid/members", s.authed("SA", "ADMIN"), s.handleMembersAdd)
	api.Delete("/sessions/:sid/groups/:gid/members/:cid", s.authed("SA", "ADMIN"), s.handleMemberRemove)

	api.Get("/sessions/:sid/children/:cid/copy-candidates",
		newLimiter(30, time.Minute, keyIP), s.authed("SA", "ADMIN"), s.handleCopyCandidates)
	api.Post("/sessions/:sid/children/:cid/copy",
		newLimiter(5, time.Minute, keyIP), s.authed("SA", "ADMIN"), s.handleCopyExecute)
}

// sessionSortAllow is the 05 §3 sessions sort allowlist.
var sessionSortAllow = map[string]string{
	"id": "s.id", "name": "s.name", "date": "s.date",
	"status": "s.status", "created_at": "s.created_at",
}

// handleSessionList — paged GET /api/sessions (FAS: own ≥1 group only;
// default sort date DESC).
func (s *Server) handleSessionList(c fiber.Ctx) error {
	u, _ := c.Locals(kvUser).(*userRow)
	school, _ := c.Locals(kvSchool).(int64)
	p, okList := listParams(c, "s.date", "desc", sessionSortAllow)
	if !okList {
		return nil // VALIDATION already written by listParams
	}
	var filt struct {
		Status string `query:"status"`
	}
	_ = c.Bind().Query(&filt)
	if filt.Status != "" && filt.Status != "DRAFT" && filt.Status != "OPENED" && filt.Status != "FINISHED" {
		return fail(c, CodeValidation, MsgValidation)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	where := " WHERE 1=1"
	var args []any
	if tcond, targs := schoolCond(school, "s.school_id"); tcond != "" {
		where += tcond
		args = append(args, targs...)
	}
	if u.Role == "FASILITATOR" {
		_, own, err := s.fasSessionScope(ctx, u.ID, school)
		if err != nil {
			return failDB(c, err)
		}
		if len(own) == 0 {
			return okPaged(c, []sessionItem{}, p.Page, p.PerPage, 0)
		}
		ph := placeholders(len(own))
		where += ` AND s.id IN (` + ph + `)`
		for _, id := range own {
			args = append(args, id)
		}
	}
	if filt.Status != "" {
		where += ` AND s.status = ?`
		args = append(args, filt.Status)
	}
	if p.Q != "" {
		where += ` AND s.name LIKE ?`
		args = append(args, "%"+p.Q+"%")
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions s`+where, args...).Scan(&total); err != nil {
		return failDB(c, err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.id, s.program_id, s.program_name_snap, s.name, s.description, s.date,
		        s.start_time, s.end_time, s.location, s.status,
		        (SELECT COUNT(*) FROM session_groups g WHERE g.session_id = s.id),
		        (SELECT COUNT(*) FROM session_members m WHERE m.session_id = s.id),
		        s.created_at
		 FROM sessions s`+where+` ORDER BY `+p.OrderBy("s.id")+` LIMIT ? OFFSET ?`,
		append(append([]any{}, args...), p.PerPage, p.Offset)...)
	if err != nil {
		return failDB(c, err)
	}
	defer rows.Close()

	out := make([]sessionItem, 0, p.PerPage)
	for rows.Next() {
		var (
			it        sessionItem
			desc      sql.NullString
			startT    sql.NullString
			endT      sql.NullString
			date      time.Time
			createdAt time.Time
		)
		if err := rows.Scan(&it.ID, &it.ProgramID, &it.ProgramName, &it.Name, &desc,
			&date, &startT, &endT, &it.Location, &it.Status,
			&it.GroupCount, &it.MemberCount, &createdAt); err != nil {
			return failDB(c, err)
		}
		it.Description = fromNull(desc)
		it.StartTime = fromNull(startT)
		it.EndTime = fromNull(endT)
		it.Date = fmtDate(date)
		it.CreatedAt = fmtTS(createdAt)
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	return okPaged(c, out, p.Page, p.PerPage, total)
}

// ---------------------------------------------------------------------------
// Create (FL-07, decision 17: groups[] optional)
// ---------------------------------------------------------------------------

type groupInput struct {
	Name          string  `json:"name"`
	FacilitatorID int64   `json:"facilitator_id"`
	ChildIDs      []int64 `json:"child_ids"`
}

type createSessionReq struct {
	ProgramID   int64        `json:"program_id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Date        string       `json:"date"`
	StartTime   string       `json:"start_time"`
	EndTime     string       `json:"end_time"`
	Location    string       `json:"location"`
	Groups      []groupInput `json:"groups"`
}

// handleSessionCreate — ONE Tx: eligibility + snapshot + groups + members;
// idempotency replay returns the original {session_id}.
func (s *Server) handleSessionCreate(c fiber.Ctx) error {
	var req createSessionReq
	if !bindBody(c, &req) {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)

	name := strings.TrimSpace(req.Name)
	loc := strings.TrimSpace(req.Location)
	req.Date = strings.TrimSpace(req.Date)
	if req.ProgramID <= 0 || name == "" || len(name) > maxShortTextLen ||
		loc == "" || len(loc) > maxShortTextLen || len(req.Description) > maxDescriptionLen {
		return fail(c, CodeValidation, MsgValidation)
	}
	if _, err := time.Parse("2006-01-02", req.Date); err != nil {
		return fail(c, CodeValidation, MsgValidation)
	}
	if !validTimeRange(req.StartTime, req.EndTime) {
		return fail(c, CodeValidation, MsgValidation)
	}
	for i := range req.Groups {
		g := &req.Groups[i]
		g.Name = strings.TrimSpace(g.Name)
		if g.Name == "" || len(g.Name) > maxShortTextLen {
			return fail(c, CodeValidation, MsgValidation)
		}
		for _, cid := range g.ChildIDs {
			if cid <= 0 {
				return fail(c, CodeValidation, MsgValidation)
			}
		}
	}

	return s.idempotent(c, "POST "+c.Path(), func() error {
		ctx, cancel := dbCtx(c)
		defer cancel()
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return failDB(c, err)
		}
		defer tx.Rollback()

		// Program + eligibility (E13): ≥1 active topic, each with ≥1 activity.
		var progName string
		if err := tx.QueryRowContext(ctx,
			`SELECT name FROM programs WHERE id = ? AND school_id = ? AND is_active = TRUE`,
			req.ProgramID, school).Scan(&progName); err != nil {
			if err == sql.ErrNoRows {
				return fail(c, CodeNotFound, MsgNotFound)
			}
			return failDB(c, err)
		}
		var topicTotal, topicIncomplete int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM topics
			 WHERE program_id = ? AND school_id = ? AND is_active = TRUE`,
			req.ProgramID, school).Scan(&topicTotal); err != nil {
			return failDB(c, err)
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM topics t
			 WHERE t.program_id = ? AND t.school_id = ? AND t.is_active = TRUE
			   AND NOT EXISTS (SELECT 1 FROM activities a
			                    WHERE a.topic_id = t.id AND a.school_id = t.school_id)`,
			req.ProgramID, school).Scan(&topicIncomplete); err != nil {
			return failDB(c, err)
		}
		if topicTotal == 0 || topicIncomplete > 0 {
			return fail(c, CodeProgramIncomplete, MsgProgramIncomplete)
		}

		// Snapshot session row.
		res, err := tx.ExecContext(ctx,
			`INSERT INTO sessions
			 (school_id, program_id, program_name_snap, name, description, date,
			  start_time, end_time, location, status, created_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'DRAFT', ?)`,
			school, req.ProgramID, progName, name, toNull(req.Description), req.Date,
			toNull(req.StartTime), toNull(req.EndTime), loc, u.ID)
		if err != nil {
			return failDB(c, err)
		}
		sid, _ := res.LastInsertId()

		// Copy program topics (+badge snaps) and activities into session_*.
		// Buffer each SELECT fully and close the rows before writing: the
		// MySQL protocol does not permit executing another statement on a
		// connection while a result set is still streaming (in-loop writes
		// on the same tx would poison the connection — "driver: bad
		// connection").
		tRows, err := tx.QueryContext(ctx,
			`SELECT id, name, sort_order, badge_name, badge_image_path
			 FROM topics WHERE program_id = ? AND school_id = ? AND is_active = TRUE
			 ORDER BY sort_order, id`, req.ProgramID, school)
		if err != nil {
			return failDB(c, err)
		}
		type topicSnap struct {
			id    int64
			name  string
			sort  int
			badge sql.NullString
			image sql.NullString
		}
		topics := make([]topicSnap, 0, 8)
		for tRows.Next() {
			var t topicSnap
			if err := tRows.Scan(&t.id, &t.name, &t.sort, &t.badge, &t.image); err != nil {
				tRows.Close()
				return failDB(c, err)
			}
			topics = append(topics, t)
		}
		if err := tRows.Err(); err != nil {
			tRows.Close()
			return failDB(c, err)
		}
		tRows.Close()
		for _, t := range topics {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO session_topics
				 (school_id, session_id, topic_id, topic_name_snap, sort_order,
				  badge_name_snap, badge_image_snap)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				school, sid, t.id, t.name, t.sort, t.badge, t.image); err != nil {
				return failDB(c, err)
			}
		}

		aRows, err := tx.QueryContext(ctx,
			`SELECT st.id, a.id, a.name, a.indicator, a.sort_order
			 FROM activities a
			 JOIN session_topics st ON st.topic_id = a.topic_id AND st.session_id = ?
			 WHERE a.school_id = ? AND a.topic_id IN
			   (SELECT t.id FROM topics t WHERE t.program_id = ? AND t.school_id = ? AND t.is_active = TRUE)
			 ORDER BY st.id, a.sort_order, a.id`,
			sid, school, req.ProgramID, school)
		if err != nil {
			return failDB(c, err)
		}
		type activitySnap struct {
			stid      int64
			aid       int64
			name      string
			indicator sql.NullString
			sort      int
		}
		activities := make([]activitySnap, 0, 16)
		for aRows.Next() {
			var a activitySnap
			if err := aRows.Scan(&a.stid, &a.aid, &a.name, &a.indicator, &a.sort); err != nil {
				aRows.Close()
				return failDB(c, err)
			}
			activities = append(activities, a)
		}
		if err := aRows.Err(); err != nil {
			aRows.Close()
			return failDB(c, err)
		}
		aRows.Close()
		for _, a := range activities {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO session_activities
				 (school_id, session_id, session_topic_id, activity_id,
				  activity_name_snap, indicator_snap, sort_order)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				school, sid, a.stid, a.aid, a.name, a.indicator, a.sort); err != nil {
				return failDB(c, err)
			}
		}

		// Validate all children up front (FOR UPDATE per 05 §7) before any
		// member insert, so CHILD_DUPLICATE / CHILD_INACTIVE fail the whole Tx.
		childRows := make(map[int64]struct {
			name   string
			active bool
		})
		distinct := make([]int64, 0, 8)
		for i := range req.Groups {
			for _, cid := range req.Groups[i].ChildIDs {
				if _, seen := childRows[cid]; !seen {
					distinct = append(distinct, cid)
				}
			}
		}
		if len(distinct) > 0 {
			args := make([]any, 0, len(distinct)+1)
			for _, cid := range distinct {
				args = append(args, cid)
			}
			args = append(args, school)
			cRows, err := tx.QueryContext(ctx,
				`SELECT id, name, is_active FROM children
				 WHERE id IN (`+placeholders(len(distinct))+`) AND school_id = ? FOR UPDATE`,
				args...)
			if err != nil {
				return failDB(c, err)
			}
			for cRows.Next() {
				var id int64
				var cr struct {
					name   string
					active bool
				}
				if err := cRows.Scan(&id, &cr.name, &cr.active); err != nil {
					cRows.Close()
					return failDB(c, err)
				}
				childRows[id] = cr
			}
			if err := cRows.Err(); err != nil {
				cRows.Close()
				return failDB(c, err)
			}
			cRows.Close()
			for _, cid := range distinct {
				if _, ok := childRows[cid]; !ok {
					return fail(c, CodeNotFound, MsgNotFound) // unknown/cross-school
				}
			}
		}

		seen := make(map[int64]bool)
		createdChildren := make([]int64, 0, len(distinct))
		for i := range req.Groups {
			g := &req.Groups[i]
			// Owner (required per F-08 entry shape).
			if g.FacilitatorID <= 0 {
				return fail(c, CodeValidation, MsgValidation)
			}
			code, err := s.requestOwner(ctx, tx, school, g.FacilitatorID)
			if err != nil {
				return failDB(c, err)
			}
			if code == CodeNotFound {
				return fail(c, CodeNotFound, MsgNotFound)
			}
			if code == CodeValidation {
				return fail(c, CodeValidation, MsgValidation)
			}
			if code != "" {
				return fail(c, CodeGroupNoFacilitator, MsgGroupNoFacilitator)
			}
			var ownerName string
			if err := tx.QueryRowContext(ctx,
				`SELECT name FROM users WHERE id = ?`, g.FacilitatorID).Scan(&ownerName); err != nil {
				return failDB(c, err)
			}
			gRes, err := tx.ExecContext(ctx,
				`INSERT INTO session_groups
				 (school_id, session_id, name, facilitator_id, facilitator_name_snap)
				 VALUES (?, ?, ?, ?, ?)`,
				school, sid, g.Name, g.FacilitatorID, ownerName)
			if err != nil {
				return failDB(c, err)
			}
			gid, _ := gRes.LastInsertId()

			for _, cid := range g.ChildIDs {
				if seen[cid] {
					return fail(c, CodeChildDuplicate, MsgChildDuplicate)
				}
				seen[cid] = true
				cr := childRows[cid]
				if !cr.active {
					return fail(c, CodeChildInactive, MsgChildInactive)
				}
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO session_members
					 (session_id, group_id, child_id, child_name_snap, school_id)
					 VALUES (?, ?, ?, ?, ?)`,
					sid, gid, cid, cr.name, school); err != nil {
					if isDuplicateErr(err) {
						return fail(c, CodeChildDuplicate, MsgChildDuplicate)
					}
					return failDB(c, err)
				}
				createdChildren = append(createdChildren, cid)
			}
		}

		if err := tx.Commit(); err != nil {
			return failDB(c, err)
		}
		if len(createdChildren) == 0 {
			s.publish(school, Frame{
				Entity: "session.changed", EntityID: sid,
				SessionID: &sid, Action: "created",
			})
		} else {
			// Create-with-groups: one frame per new member so the copy hint
			// has a child to attach to (FL-07 / 02 §2c).
			for _, cid := range createdChildren {
				s.publish(school, s.withCopyAvailable(ctx, school, Frame{
					Entity: "session.changed", EntityID: sid,
					SessionID: &sid, ChildID: cid, Action: "created",
				}))
			}
		}
		return ok(c, fiber.Map{"session_id": sid})
	})
}

// ---------------------------------------------------------------------------
// Detail (ONE response: session + topics + groups + scope)
// ---------------------------------------------------------------------------

// handleSessionDetail builds the single detail payload so assessment/gallery/
// report never waterfall (02 §2b). FAS: must own ≥1 group (else NOT_FOUND);
// foreign groups reduced to gid/name/own/member_count/status.
func (s *Server) handleSessionDetail(c fiber.Ctx) error {
	sid, okID := idParam(c, "sid")
	if !okID {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)
	school, _ := c.Locals(kvSchool).(int64)

	ctx, cancel := dbCtx(c)
	defer cancel()

	sess, err := s.loadSessionJSON(ctx, sid, school)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}

	ownGroups, err := s.fasOwnScope(ctx, u, sid, school)
	if err == errNotVisible {
		return fail(c, CodeNotFound, MsgNotFound) // not visible to this FAS
	}
	if err != nil {
		return failDB(c, err)
	}

	// Topics + activities snapshot.
	tcond, targs := schoolCond(school, "st.school_id")
	tRows, err := s.db.QueryContext(ctx,
		`SELECT st.id, st.topic_id, st.topic_name_snap, st.sort_order
		 FROM session_topics st WHERE st.session_id = ?`+tcond+
			` ORDER BY st.sort_order, st.id`, append([]any{sid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	topics := []detailTopic{}
	topicIdx := map[int64]int{}
	for tRows.Next() {
		var (
			stid, topicID int64
			name          string
			sortOrder     int
		)
		if err := tRows.Scan(&stid, &topicID, &name, &sortOrder); err != nil {
			tRows.Close()
			return failDB(c, err)
		}
		topics = append(topics, detailTopic{
			Stid: stid, TopicID: topicID, Name: name,
			SortOrder: sortOrder, Activities: []detailActivity{},
		})
		topicIdx[stid] = len(topics) - 1
	}
	if err := tRows.Err(); err != nil {
		tRows.Close()
		return failDB(c, err)
	}
	tRows.Close()

	acond, aargs := schoolCond(school, "sa.school_id")
	aRows, err := s.db.QueryContext(ctx,
		`SELECT sa.session_topic_id, sa.id, sa.activity_id, sa.activity_name_snap,
		        sa.indicator_snap, sa.sort_order
		 FROM session_activities sa WHERE sa.session_id = ?`+acond+
			` ORDER BY sa.session_topic_id, sa.sort_order, sa.id`,
		append([]any{sid}, aargs...)...)
	if err != nil {
		return failDB(c, err)
	}
	for aRows.Next() {
		var (
			stid, said, aid int64
			name            string
			indicator       sql.NullString
			sortOrder       int
		)
		if err := aRows.Scan(&stid, &said, &aid, &name, &indicator, &sortOrder); err != nil {
			aRows.Close()
			return failDB(c, err)
		}
		if idx, ok := topicIdx[stid]; ok {
			topics[idx].Activities = append(topics[idx].Activities, detailActivity{
				Said: said, ActivityID: aid, Name: name,
				Indicator: fromNull(indicator), SortOrder: sortOrder,
			})
		}
	}
	if err := aRows.Err(); err != nil {
		aRows.Close()
		return failDB(c, err)
	}
	aRows.Close()

	groupsOut, err := s.loadSessionGroups(ctx, sid, school, u, ownGroups, sess.Status)
	if err != nil {
		return failDB(c, err)
	}

	// The wipe-counts object feeds the typed-text delete modal (03 F-08).
	// Only SA/ADMIN can delete a session — FAS never sees the dialog — so
	// FASILITATOR detail OMITS `scope` entirely (contract types it optional;
	// dialog is canManage-gated) and its whole scopeQ never runs
	// (002a/F-ARCH-01). SA/ADMIN keep the eager counts.
	resp := fiber.Map{"session": sess, "topics": topics, "groups": groupsOut}
	if u.Role != "FASILITATOR" {
		scond, sargs := schoolCond(school, "school_id")
		ocond, oargs := schoolCond(school, "o.school_id")
		// reports_sent = SENT REPORT outbox rows whose payload_ref (report
		// URL) carries a token of this session (05 §2 payload_ref = report
		// URL; revoked tokens keep their public_links row, so regenerated
		// links still count their sends). o.school_id bounds the unindexable
		// LIKE scan to this school's outbox slice (R1-F02 — served by
		// idx_wa_outbox_school_created; a cross-school row can never hold
		// this session's token, so the predicate is semantically free).
		scopeQ := `SELECT
		   (SELECT COUNT(*) FROM session_topics WHERE session_id = ?),
		   (SELECT COUNT(*) FROM session_groups WHERE session_id = ?),
		   (SELECT COUNT(*) FROM session_members WHERE session_id = ?),
		   (SELECT COUNT(*) FROM assessments WHERE session_id = ?),
		   (SELECT COUNT(*) FROM photos WHERE session_id = ?),
		   (SELECT COUNT(*) FROM narratives WHERE session_id = ?),
		   (SELECT COUNT(*) FROM public_links
		     WHERE session_id = ? AND kind = 'REPORT' AND revoked_at IS NULL),
		   (SELECT COUNT(*) FROM public_links
		     WHERE session_id = ? AND kind = 'GALLERY' AND revoked_at IS NULL),
		   (SELECT COUNT(*) FROM consent_links WHERE session_id = ?),
		   (SELECT COUNT(*) FROM wa_outbox o
		     WHERE o.kind = 'REPORT' AND o.status = 'SENT'` + ocond + `
		       AND EXISTS (SELECT 1 FROM public_links pl
		                   WHERE pl.session_id = ? AND pl.kind = 'REPORT'
		                     AND o.payload_ref LIKE CONCAT('%', pl.token)))
		 FROM sessions WHERE id = ?`
		var scope sessionScope
		scopeArgs := make([]any, 0, 13)
		for range 9 {
			scopeArgs = append(scopeArgs, sid)
		}
		scopeArgs = append(scopeArgs, oargs...) // wa_outbox school predicate
		scopeArgs = append(scopeArgs, sid)      // public_links session probe
		scopeArgs = append(scopeArgs, sid)      // outer sessions.id
		scopeArgs = append(scopeArgs, sargs...)
		if err := s.db.QueryRowContext(ctx, scopeQ+scond, scopeArgs...).
			Scan(&scope.Topics, &scope.Groups, &scope.Members, &scope.Assessments,
				&scope.Photos, &scope.Narratives, &scope.LinksReport, &scope.LinksGallery,
				&scope.LinksConsent, &scope.ReportsSent); err != nil {
			if err == sql.ErrNoRows {
				return fail(c, CodeNotFound, MsgNotFound)
			}
			return failDB(c, err)
		}
		resp["scope"] = scope
	}

	return ok(c, resp)
}

// loadSessionGroups assembles the detail `groups` array (also served by
// GET /sessions/:sid/groups): full objects for own groups (owner, consent
// counts, members), reduced {gid,name,own,member_count,status} for FAS
// foreign groups (status = the parent session's status — session_groups has
// no status column; user decision 2026-10-11). ownGroups is nil for
// SA/ADMIN (everything is own).
func (s *Server) loadSessionGroups(ctx context.Context, sid, school int64, u *userRow, ownGroups map[int64]bool, status string) ([]any, error) {
	gcond, gargs := schoolCond(school, "g.school_id")
	gRows, err := s.db.QueryContext(ctx,
		`SELECT g.id, g.name, g.facilitator_id FROM session_groups g
		 WHERE g.session_id = ?`+gcond+` ORDER BY g.id`,
		append([]any{sid}, gargs...)...)
	if err != nil {
		return nil, err
	}
	type groupRow struct {
		id    int64
		name  string
		owner sql.NullInt64
	}
	var groupRows []groupRow
	for gRows.Next() {
		var gr groupRow
		if err := gRows.Scan(&gr.id, &gr.name, &gr.owner); err != nil {
			gRows.Close()
			return nil, err
		}
		groupRows = append(groupRows, gr)
	}
	if err := gRows.Err(); err != nil {
		gRows.Close()
		return nil, err
	}
	gRows.Close()

	type memberRow struct {
		gid     int64
		childID int64
		name    string
		age     int
		school  string
		consent sql.NullString
	}
	members := []memberRow{}
	mcond, margs := schoolCond(school, "sm.school_id")
	mRows, err := s.db.QueryContext(ctx,
		`SELECT sm.group_id, sm.child_id, c.name, c.age, c.school, cl.status
		 FROM session_members sm
		 JOIN children c ON c.id = sm.child_id
		 LEFT JOIN consent_links cl ON cl.session_id = sm.session_id AND cl.child_id = sm.child_id
		 WHERE sm.session_id = ?`+mcond+` ORDER BY sm.group_id, sm.child_id`,
		append([]any{sid}, margs...)...)
	if err != nil {
		return nil, err
	}
	for mRows.Next() {
		var mr memberRow
		if err := mRows.Scan(&mr.gid, &mr.childID, &mr.name, &mr.age, &mr.school, &mr.consent); err != nil {
			mRows.Close()
			return nil, err
		}
		members = append(members, mr)
	}
	if err := mRows.Err(); err != nil {
		mRows.Close()
		return nil, err
	}
	mRows.Close()

	// Owners (active FASILITATOR rows only).
	ownerIDs := make([]any, 0, len(groupRows))
	for _, gr := range groupRows {
		if gr.owner.Valid {
			ownerIDs = append(ownerIDs, gr.owner.Int64)
		}
	}
	owners := map[int64]string{}
	if len(ownerIDs) > 0 {
		oRows, err := s.db.QueryContext(ctx,
			`SELECT id, name FROM users
			 WHERE id IN (`+placeholders(len(ownerIDs))+`)
			   AND role = 'FASILITATOR' AND decision = 'APPROVED' AND is_active = TRUE`,
			ownerIDs...)
		if err != nil {
			return nil, err
		}
		for oRows.Next() {
			var id int64
			var name string
			if err := oRows.Scan(&id, &name); err != nil {
				oRows.Close()
				return nil, err
			}
			owners[id] = name
		}
		if err := oRows.Err(); err != nil {
			oRows.Close()
			return nil, err
		}
		oRows.Close()
	}

	// Single pass into per-group slices (002d): O(G+M) instead of rescanning
	// the flat members slice twice per group — emission order still follows
	// groupRows, member order still follows the (group_id, child_id) query.
	byGroup := make(map[int64][]memberRow, len(groupRows))
	for _, mr := range members {
		byGroup[mr.gid] = append(byGroup[mr.gid], mr)
	}

	groupsOut := make([]any, 0, len(groupRows))
	for _, gr := range groupRows {
		own := true
		if u.Role == "FASILITATOR" {
			own = ownGroups[gr.id]
		}
		gMembers := byGroup[gr.id]
		if !own {
			groupsOut = append(groupsOut, groupReduced{
				GID: gr.id, Name: gr.name, Own: false, MemberCount: len(gMembers),
				Status: status,
			})
			continue
		}
		var owner *detailOwner
		if gr.owner.Valid {
			if name, ok := owners[gr.owner.Int64]; ok {
				owner = &detailOwner{ID: gr.owner.Int64, Name: name}
			}
		}
		consent := map[string]int{"PENDING": 0, "SETUJU": 0, "DITOLAK": 0}
		memberOut := []detailMember{}
		for _, mr := range gMembers {
			if mr.consent.Valid {
				consent[mr.consent.String]++
			}
			memberOut = append(memberOut, detailMember{
				ChildID: mr.childID, Name: mr.name, Age: mr.age, School: mr.school,
				Consent: fromNull(mr.consent),
			})
		}
		groupsOut = append(groupsOut, groupFull{
			GID: gr.id, Name: gr.name, Own: true, Owner: owner,
			OwnerMissing: owner == nil, MemberCount: len(gMembers),
			Consent: consent, Members: memberOut,
		})
	}
	return groupsOut, nil
}

// ---------------------------------------------------------------------------
// PATCH / DELETE (decision 5 backdate, decision 8 typed wipe)
// ---------------------------------------------------------------------------

type patchSessionReq struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	// ProgramID is a presence probe only: the snapshot program is immutable
	// (08 acceptance "PATCH containing program_id → SESSION_LOCKED"), so the
	// value never matters — the pointer just tells "sent" from "omitted".
	ProgramID *int64  `json:"program_id"`
	Date      *string `json:"date"`
	StartTime *string `json:"start_time"`
	EndTime   *string `json:"end_time"`
	Location  *string `json:"location"`
}

// fmtClock normalizes a time to the contracts' HH:MM:SS wire form.
func fmtClock(s string) string {
	t, ok := parseClock(s)
	if !ok {
		return s
	}
	return t.Format("15:04:05")
}

// handleSessionPatch — Draft edits snapshot fields; Opened only groups/
// members (any snapshot field → SESSION_LOCKED); Finished always locked. A
// PATCH body containing program_id is always SESSION_LOCKED (the snapshot
// program is immutable — 08 acceptance), gated before any read or write.
// Guarded UPDATE … WHERE status='DRAFT'; 0 rows → re-read (E10/E11).
func (s *Server) handleSessionPatch(c fiber.Ctx) error {
	sid, okID := idParam(c, "sid")
	if !okID {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	var req patchSessionReq
	if !bindBody(c, &req) {
		return nil
	}
	if req.ProgramID != nil {
		// The snapshot program is immutable (F-08 / 08 acceptance): a PATCH
		// carrying program_id — any value — is SESSION_LOCKED for every
		// status. Checked before any read or write so no partial mutation
		// can slip through.
		return fail(c, CodeSessionLocked, MsgSessionLocked)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	cur, err := s.loadSessionJSON(ctx, sid, school)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}

	// Merge changed fields over the current row for validation/response.
	next := *cur
	var sets []string
	var args []any
	if req.Name != nil {
		v := strings.TrimSpace(*req.Name)
		if v == "" || len(v) > maxShortTextLen {
			return fail(c, CodeValidation, MsgValidation)
		}
		next.Name = v
		sets = append(sets, "name = ?")
		args = append(args, v)
	}
	if req.Description != nil {
		if len(*req.Description) > maxDescriptionLen {
			return fail(c, CodeValidation, MsgValidation)
		}
		if strings.TrimSpace(*req.Description) == "" {
			next.Description = nil
		} else {
			next.Description = req.Description
		}
		sets = append(sets, "description = ?")
		args = append(args, toNull(*req.Description))
	}
	if req.Location != nil {
		v := strings.TrimSpace(*req.Location)
		if v == "" || len(v) > maxShortTextLen {
			return fail(c, CodeValidation, MsgValidation)
		}
		next.Location = v
		sets = append(sets, "location = ?")
		args = append(args, v)
	}
	if req.Date != nil {
		v := strings.TrimSpace(*req.Date)
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return fail(c, CodeValidation, MsgValidation)
		}
		next.Date = v
		sets = append(sets, "date = ?")
		args = append(args, v)
	}
	mergedStart, mergedEnd := derefStr(cur.StartTime), derefStr(cur.EndTime)
	if req.StartTime != nil {
		mergedStart = *req.StartTime
		sets = append(sets, "start_time = ?")
		args = append(args, toNull(*req.StartTime))
	}
	if req.EndTime != nil {
		mergedEnd = *req.EndTime
		sets = append(sets, "end_time = ?")
		args = append(args, toNull(*req.EndTime))
	}
	if !validTimeRange(mergedStart, mergedEnd) {
		return fail(c, CodeValidation, MsgValidation)
	}
	if req.StartTime != nil {
		next.StartTime = nullClock(*req.StartTime)
	}
	if req.EndTime != nil {
		next.EndTime = nullClock(*req.EndTime)
	}

	if len(sets) == 0 {
		// Nothing to change: Finished stays locked, otherwise echo current.
		if cur.Status == "FINISHED" {
			return fail(c, CodeSessionLocked, MsgSessionLocked)
		}
		return ok(c, fiber.Map{"session": cur})
	}
	if cur.Status != "DRAFT" {
		return fail(c, CodeSessionLocked, MsgSessionLocked)
	}

	tcond, targs := schoolCond(school, "school_id")
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET `+strings.Join(sets, ", ")+
			` WHERE id = ?`+tcond+` AND status = 'DRAFT'`,
		append(append([]any{}, args...), append([]any{sid}, targs...)...)...)
	if err != nil {
		return failDB(c, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Raced a transition or a delete between the read and the UPDATE:
		// the mandated E10/E11 status re-read classifies the loss (06 §1).
		// Gone → NOT_FOUND; OPENED/FINISHED (or an unknown status) →
		// SESSION_LOCKED — the current state itself forbids Draft edits
		// (E10, wrong-state single writer); still DRAFT: requested values
		// already sitting in the row mean a rival's update landed while the
		// status stayed unchanged → STATE_CONFLICT (E11), while a
		// value-identical request is a legal no-op → echo <next>, no frame.
		after, err := s.scopedStatus(ctx, sid, school)
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		if err != nil {
			return failDB(c, err)
		}
		if after != "DRAFT" {
			return fail(c, CodeSessionLocked, MsgSessionLocked)
		}
		if patchChangesRow(&next, cur) {
			return fail(c, CodeStateConflict, MsgStateConflict)
		}
		return ok(c, fiber.Map{"session": &next})
	}
	s.publish(school, Frame{
		Entity: "session.changed", EntityID: sid,
		SessionID: &sid, Action: "updated",
	})
	return ok(c, fiber.Map{"session": &next})
}

// patchChangesRow reports whether the merged PATCH row differs from the row
// the handler loaded — i.e. the request asked for a REAL change (the
// zero-rows E10/E11 split: no requested change = value-identical no-op,
// requested change = a rival already applied it). Nullable clock/description
// pointers compare by value, never by identity (nil == nil).
func patchChangesRow(next, cur *sessionJSON) bool {
	sameText := func(a, b *string) bool {
		if a == nil || b == nil {
			return a == nil && b == nil
		}
		return *a == *b
	}
	return next.Name != cur.Name ||
		next.Location != cur.Location ||
		next.Date != cur.Date ||
		!sameText(next.Description, cur.Description) ||
		!sameText(next.StartTime, cur.StartTime) ||
		!sameText(next.EndTime, cur.EndTime)
}

// derefStr / nullClock: nullable-time helpers for the PATCH merge.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func nullClock(s string) *string {
	if s == "" {
		return nil
	}
	v := fmtClock(s)
	return &v
}

// scopedStatus re-reads a session status inside one school scope.
func (s *Server) scopedStatus(ctx context.Context, sid, school int64) (string, error) {
	tcond, targs := schoolCond(school, "school_id")
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM sessions WHERE id = ?`+tcond,
		append([]any{sid}, targs...)...).Scan(&status)
	return status, err
}

// handleSessionDelete — Draft/Finished physical wipe in ONE Tx (option A,
// decision 8), photo files unlinked after commit, revocation signals last.
func (s *Server) handleSessionDelete(c fiber.Ctx) error {
	sid, okID := idParam(c, "sid")
	if !okID {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()

	// Guarded status claim (FL-08): Opened sessions must finish first.
	tcond, targs := schoolCond(school, "school_id")
	var status string
	err = tx.QueryRowContext(ctx,
		`SELECT status FROM sessions WHERE id = ?`+tcond+` FOR UPDATE`,
		append([]any{sid}, targs...)...).Scan(&status)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	if status == "OPENED" {
		return fail(c, CodeSessionLocked, MsgSessionLocked)
	}

	// Collect members, wipe every child-scoped session artefact in this Tx.
	childIDs, err := sessionChildIDs(ctx, tx, sid)
	if err != nil {
		return failDB(c, err)
	}
	w, err := s.wipeChildrenScopeTx(ctx, tx, school, sid, childIDs)
	if err != nil {
		return failDB(c, err)
	}
	for _, del := range []string{
		`DELETE FROM session_members WHERE session_id = ?`,
		`DELETE FROM session_groups WHERE session_id = ?`,
		`DELETE FROM session_activities WHERE session_id = ?`,
		`DELETE FROM session_topics WHERE session_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, del, sid); err != nil {
			return failDB(c, err)
		}
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM sessions WHERE id = ? AND status IN ('DRAFT','FINISHED')`+tcond,
		append([]any{sid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Unreachable under the row lock; classified symmetrically to the
		// PATCH zero-rows branch (06 §1 E10/E11): vanished → NOT_FOUND;
		// OPENED/FINISHED (or unknown) → SESSION_LOCKED (the re-read's
		// wrong-state classification, E10); still DRAFT — the row kept the
		// claimed status while the DELETE matched nothing, i.e. a rival
		// already applied the change while the status stayed unchanged →
		// STATE_CONFLICT (E11). A delete has no value-identical no-op echo,
		// unlike the PATCH branch.
		after, err := s.scopedStatus(ctx, sid, school)
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		if err != nil {
			return failDB(c, err)
		}
		if after == "DRAFT" {
			return fail(c, CodeStateConflict, MsgStateConflict)
		}
		return fail(c, CodeSessionLocked, MsgSessionLocked)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	s.unlinkPhotos(w.photoPaths)
	s.publish(school, Frame{
		Entity: "session.deleted", EntityID: sid,
		SessionID: &sid, Action: "deleted",
	})
	s.publishWipedLinks(school, sid, w)
	return ok(c, fiber.Map{"deleted": true})
}

// sessionChildIDs lists the session's member child ids (for cascade wipes).
func sessionChildIDs(ctx context.Context, tx *sql.Tx, sid int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT child_id FROM session_members WHERE session_id = ?`, sid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ---------------------------------------------------------------------------
// Guarded lifecycle transitions (06 E10/E11)
// ---------------------------------------------------------------------------

func (s *Server) handleSessionOpen(c fiber.Ctx) error {
	return s.transitionSession(c, "DRAFT", "OPENED")
}

func (s *Server) handleSessionFinish(c fiber.Ctx) error {
	return s.transitionSession(c, "OPENED", "FINISHED")
}

func (s *Server) handleSessionReopen(c fiber.Ctx) error {
	return s.transitionSession(c, "FINISHED", "OPENED")
}

// statusRank orders the lifecycle DRAFT < OPENED < FINISHED for the E10/E11
// zero-rows re-read classification (unknown → -1, never at/beyond target).
func statusRank(status string) int {
	switch status {
	case "DRAFT":
		return 0
	case "OPENED":
		return 1
	case "FINISHED":
		return 2
	}
	return -1
}

// transitionSession: the steady-state read decides `already` (no publish)
// and wrong-state SESSION_LOCKED (E10); inside one Tx the row is locked, the
// start state re-verified (a rival that moved first → STATE_CONFLICT, E11,
// nothing published), open gates run fresh, then the spec'd guarded
// UPDATE … WHERE status=<expected> commits and publishes session.status.
// If that UPDATE matches 0 rows the mandated E10/E11 re-read classifies the
// loss: rival at-or-beyond the target → STATE_CONFLICT, otherwise the
// current state itself forbids the op → SESSION_LOCKED — never published.
func (s *Server) transitionSession(c fiber.Ctx, from, to string) error {
	sid, okID := idParam(c, "sid")
	if !okID {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	tcond, targs := schoolCond(school, "school_id")

	cur, err := s.scopedStatus(ctx, sid, school)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	if cur == to {
		return ok(c, fiber.Map{"status": cur, "already": true})
	}
	if cur != from {
		return fail(c, CodeSessionLocked, MsgSessionLocked)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()

	err = tx.QueryRowContext(ctx,
		`SELECT status FROM sessions WHERE id = ?`+tcond+` FOR UPDATE`,
		append([]any{sid}, targs...)...).Scan(&cur)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	if cur != from {
		return fail(c, CodeStateConflict, MsgStateConflict) // rival transitioned first
	}

	if from == "DRAFT" { // open gates (decision 17)
		code, offenders, err := s.openGates(ctx, tx, sid, school)
		if err != nil {
			return failDB(c, err)
		}
		switch code {
		case "":
		case CodeValidation:
			return failGroups(c, CodeValidation, MsgValidation, offenders)
		default:
			return failGroups(c, CodeGroupNoFacilitator, MsgGroupNoFacilitator, offenders)
		}
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE sessions SET status = ? WHERE id = ?`+tcond+` AND status = ?`,
		append([]any{to, sid}, append(targs, from)...)...)
	if err != nil {
		return failDB(c, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 06 §1 E10/E11 + §4 mandate a status re-read when the guarded UPDATE
		// matches nothing: a status that has NOT reached the wanted target
		// means the current state itself forbids the op (single-writer) →
		// SESSION_LOCKED; a rival already at or beyond the target (DRAFT →
		// OPENED → FINISHED order) → STATE_CONFLICT. Neither path publishes —
		// only the guarded-Tx winner emits session.status.
		var after string
		if err := tx.QueryRowContext(ctx,
			`SELECT status FROM sessions WHERE id = ?`+tcond,
			append([]any{sid}, targs...)...).Scan(&after); err != nil {
			if err == sql.ErrNoRows {
				return fail(c, CodeNotFound, MsgNotFound)
			}
			return failDB(c, err)
		}
		if statusRank(after) >= statusRank(to) {
			return fail(c, CodeStateConflict, MsgStateConflict)
		}
		return fail(c, CodeSessionLocked, MsgSessionLocked)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}
	s.publish(school, Frame{
		Entity: "session.status", EntityID: sid,
		SessionID: &sid, Action: strings.ToLower(to),
	})
	return ok(c, fiber.Map{"status": to})
}

// gateGroup is one offending group of an open-gate failure — the list the
// envelope carries under `error.groups` (B11 / 02 §2b + 08).
type gateGroup struct {
	GID    int64  `json:"gid"`
	Name   string `json:"name"`
	Reason string `json:"reason"` // "empty" | "owner_missing"
}

// failGroups writes a gate failure with the offending group list attached to
// the error object — the extra-key convention users.go/reports.go already use.
func failGroups(c fiber.Ctx, code, message string, groups []gateGroup) error {
	return c.Status(200).JSON(fiber.Map{"error": fiber.Map{
		"code": code, "message": message, "groups": groups,
	}})
}

// openGates enforces FL-08's open preconditions in order: ≥1 group, every
// group non-empty, every group with an active owner. "" = all gates pass.
// A failing gate also returns the groups that failed IT (empty wins →
// VALIDATION, else ownerless → GROUP_WITHOUT_FACILITATOR), so the envelope
// can ship the list instead of making the client re-derive it.
func (s *Server) openGates(ctx context.Context, tx *sql.Tx, sid, school int64) (string, []gateGroup, error) {
	var groupCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_groups WHERE session_id = ?`, sid).
		Scan(&groupCount); err != nil {
		return "", nil, err
	}
	if groupCount == 0 {
		return CodeValidation, []gateGroup{}, nil // no group to name
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT g.id, g.name, g.facilitator_id,
		        (SELECT COUNT(*) FROM session_members m WHERE m.group_id = g.id)
		 FROM session_groups g WHERE g.session_id = ? ORDER BY g.id`, sid)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	var empty, ownerless []gateGroup
	for rows.Next() {
		var (
			gid     int64
			name    string
			owner   sql.NullInt64
			members int
		)
		if err := rows.Scan(&gid, &name, &owner, &members); err != nil {
			return "", nil, err
		}
		if members == 0 {
			empty = append(empty, gateGroup{GID: gid, Name: name, Reason: "empty"})
		}
		if !owner.Valid {
			ownerless = append(ownerless, gateGroup{GID: gid, Name: name, Reason: "owner_missing"})
			continue
		}
		ok, err := ownerOK(ctx, tx, school, owner.Int64)
		if err != nil {
			return "", nil, err
		}
		if !ok {
			ownerless = append(ownerless, gateGroup{GID: gid, Name: name, Reason: "owner_missing"})
		}
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	if len(empty) > 0 {
		return CodeValidation, empty, nil
	}
	if len(ownerless) > 0 {
		return CodeGroupNoFacilitator, ownerless, nil
	}
	return "", nil, nil
}

// ---------------------------------------------------------------------------
// Groups & members (02 §2b; option-B cascade wipe, decision 13)
// ---------------------------------------------------------------------------

type groupBody struct {
	Name          string `json:"name"`
	FacilitatorID int64  `json:"facilitator_id"`
}

// fasOwnScope computes the caller's own-group set for one session; nil for
// SA/ADMIN (every group is own).
func (s *Server) fasOwnScope(ctx context.Context, u *userRow, sid, school int64) (map[int64]bool, error) {
	if u.Role != "FASILITATOR" {
		return nil, nil
	}
	groupIDs, sessions, err := s.fasSessionScope(ctx, u.ID, school)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(sessions, sid) {
		return nil, errNotVisible
	}
	own := make(map[int64]bool, len(groupIDs))
	for _, gid := range groupIDs {
		own[gid] = true
	}
	return own, nil
}

// errNotVisible marks a session outside the caller's scope (→ NOT_FOUND).
var errNotVisible = errors.New("session not visible")

// handleGroupList — GET /sessions/:sid/groups (readable in any status; the
// Draft/Opened + SESSION_LOCKED rule guards mutation only).
func (s *Server) handleGroupList(c fiber.Ctx) error {
	sid, okID := idParam(c, "sid")
	if !okID {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)
	school, _ := c.Locals(kvSchool).(int64)

	ctx, cancel := dbCtx(c)
	defer cancel()
	status, err := s.scopedStatus(ctx, sid, school)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	ownGroups, err := s.fasOwnScope(ctx, u, sid, school)
	if err == errNotVisible {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	groups, err := s.loadSessionGroups(ctx, sid, school, u, ownGroups, status)
	if err != nil {
		return failDB(c, err)
	}
	return ok(c, groups)
}

// handleGroupCreate — POST /sessions/:sid/groups (Draft/Opened; owner
// optional here — the open gate enforces owners, E14).
func (s *Server) handleGroupCreate(c fiber.Ctx) error {
	sid, okID := idParam(c, "sid")
	if !okID {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	var req groupBody
	if !bindBody(c, &req) {
		return nil
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > maxShortTextLen {
		return fail(c, CodeValidation, MsgValidation)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()

	okE, err := s.lockSessionEditable(ctx, tx, c, school, sid)
	if err != nil {
		return err
	}
	if !okE {
		return nil
	}
	ownerName := ""
	if req.FacilitatorID > 0 {
		code, err := s.requestOwner(ctx, tx, school, req.FacilitatorID)
		if err != nil {
			return failDB(c, err)
		}
		if code != "" {
			return fail(c, code, msgForCode(code))
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT name FROM users WHERE id = ?`, req.FacilitatorID).Scan(&ownerName); err != nil {
			return failDB(c, err)
		}
	}
	var fid any
	if req.FacilitatorID > 0 {
		fid = req.FacilitatorID
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO session_groups (school_id, session_id, name, facilitator_id, facilitator_name_snap)
		 VALUES (?, ?, ?, ?, ?)`, school, sid, name, fid, ownerName)
	if err != nil {
		return failDB(c, err)
	}
	gid, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}
	s.publish(school, Frame{
		Entity: "group.changed", EntityID: gid,
		SessionID: &sid, Action: "created",
	})
	return ok(c, fiber.Map{"gid": gid, "name": name})
}

// msgForCode maps a gate code returned by the shared helpers to its message
// (the single code→message switch: reports/photos/assessments/sessions all
// land here, gateReportItem included — F-02-09).
func msgForCode(code string) string {
	switch code {
	case CodeNotFound:
		return MsgNotFound
	case CodeNotOwner:
		return MsgNotOwner
	case CodeGroupNoFacilitator:
		return MsgGroupNoFacilitator
	case CodeSessionLocked:
		return MsgSessionLocked
	case CodeChildInactive:
		return MsgChildInactive
	case CodeReportIncomplete:
		return MsgReportIncomplete
	case CodeDBUnavailable:
		return MsgDBUnavailable
	default:
		return MsgValidation
	}
}

type patchGroupReq struct {
	Name          *string `json:"name"`
	FacilitatorID *int64  `json:"facilitator_id"` // >0 assign, 0 clear, absent keep
}

// handleGroupPatch — name/owner edit (Draft/Opened; new owner must be an
// active same-school FASILITATOR else VALIDATION).
func (s *Server) handleGroupPatch(c fiber.Ctx) error {
	sid, okID := idParam(c, "sid")
	if !okID {
		return nil
	}
	gid, okG := idParam(c, "gid")
	if !okG {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	var req patchGroupReq
	if !bindBody(c, &req) {
		return nil
	}
	if req.Name == nil && req.FacilitatorID == nil {
		return fail(c, CodeValidation, MsgValidation)
	}
	newName := ""
	if req.Name != nil {
		newName = strings.TrimSpace(*req.Name)
		if newName == "" || len(newName) > maxShortTextLen {
			return fail(c, CodeValidation, MsgValidation)
		}
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()

	okE, err := s.lockSessionEditable(ctx, tx, c, school, sid)
	if err != nil {
		return err
	}
	if !okE {
		return nil
	}
	var curName string
	err = tx.QueryRowContext(ctx,
		`SELECT name FROM session_groups WHERE id = ? AND session_id = ?`,
		gid, sid).Scan(&curName)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	if req.Name != nil {
		curName = newName
	}

	sets := []string{}
	args := []any{}
	if req.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, newName)
	}
	if req.FacilitatorID != nil {
		if *req.FacilitatorID > 0 {
			// 02 §2b: any owner problem on PATCH is a flat VALIDATION
			// (unknown/cross-school/inactive/non-FAS all look the same here).
			code, err := s.requestOwner(ctx, tx, school, *req.FacilitatorID)
			if err != nil {
				return failDB(c, err)
			}
			if code != "" {
				return fail(c, CodeValidation, MsgValidation)
			}
			var ownerName string
			if err := tx.QueryRowContext(ctx,
				`SELECT name FROM users WHERE id = ?`, *req.FacilitatorID).Scan(&ownerName); err != nil {
				return failDB(c, err)
			}
			sets = append(sets, "facilitator_id = ?", "facilitator_name_snap = ?")
			args = append(args, *req.FacilitatorID, ownerName)
		} else {
			sets = append(sets, "facilitator_id = NULL", "facilitator_name_snap = ''")
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE session_groups SET `+strings.Join(sets, ", ")+
			` WHERE id = ? AND session_id = ?`,
		append(args, gid, sid)...); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}
	s.publish(school, Frame{
		Entity: "group.changed", EntityID: gid,
		SessionID: &sid, Action: "updated",
	})
	return ok(c, fiber.Map{"gid": gid, "name": curName})
}

// handleGroupDelete — option-B cascade (decision 13): ONE Tx removes the
// group, its membership and every affected child's session-scoped data
// (assessments, photo rows, narratives, mission picks, consent + public
// links); disk files unlink after commit; revocation signals last.
func (s *Server) handleGroupDelete(c fiber.Ctx) error {
	sid, okID := idParam(c, "sid")
	if !okID {
		return nil
	}
	gid, okG := idParam(c, "gid")
	if !okG {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()

	okE, err := s.lockSessionEditable(ctx, tx, c, school, sid)
	if err != nil {
		return err
	}
	if !okE {
		return nil
	}
	var probe int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM session_groups WHERE id = ? AND session_id = ?`, gid, sid).Scan(&probe)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}

	childIDs, err := memberChildIDs(ctx, tx, "group_id = ?", []any{gid})
	if err != nil {
		return failDB(c, err)
	}
	w, err := s.wipeChildrenScopeTx(ctx, tx, school, sid, childIDs)
	if err != nil {
		return failDB(c, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM session_members WHERE group_id = ? AND session_id = ?`, gid, sid); err != nil {
		return failDB(c, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM session_groups WHERE id = ? AND session_id = ?`, gid, sid); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	s.unlinkPhotos(w.photoPaths)
	s.publish(school, Frame{
		Entity: "group.changed", EntityID: gid,
		SessionID: &sid, Action: "deleted",
	})
	s.publishWipedLinks(school, sid, w)
	return ok(c, fiber.Map{"deleted": true})
}

// memberChildIDs collects session_members child ids for one group scope.
func memberChildIDs(ctx context.Context, tx *sql.Tx, scope string, scopeArgs []any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT child_id FROM session_members WHERE `+scope, scopeArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type membersReq struct {
	ChildIDs []int64 `json:"child_ids"`
}

// handleMembersAdd — bulk add (active children, dedup; request-internal or
// already-in-session duplicate → CHILD_DUPLICATE, inactive → CHILD_INACTIVE,
// unknown/cross-school → NOT_FOUND; children rows locked FOR UPDATE).
func (s *Server) handleMembersAdd(c fiber.Ctx) error {
	sid, okID := idParam(c, "sid")
	if !okID {
		return nil
	}
	gid, okG := idParam(c, "gid")
	if !okG {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	var req membersReq
	if !bindBody(c, &req) {
		return nil
	}
	if len(req.ChildIDs) == 0 {
		return fail(c, CodeValidation, MsgValidation)
	}
	for _, cid := range req.ChildIDs {
		if cid <= 0 {
			return fail(c, CodeValidation, MsgValidation)
		}
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()

	okE, err := s.lockSessionEditable(ctx, tx, c, school, sid)
	if err != nil {
		return err
	}
	if !okE {
		return nil
	}
	var probe int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM session_groups WHERE id = ? AND session_id = ?`, gid, sid).Scan(&probe)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}

	// Locked lookup of every requested child (05 §7 member Tx checks).
	args := make([]any, 0, len(req.ChildIDs)+1)
	for _, cid := range req.ChildIDs {
		args = append(args, cid)
	}
	args = append(args, school)
	rows, err := tx.QueryContext(ctx,
		`SELECT id, name, is_active FROM children
		 WHERE id IN (`+placeholders(len(req.ChildIDs))+`) AND school_id = ? FOR UPDATE`,
		args...)
	if err != nil {
		return failDB(c, err)
	}
	type childRow struct {
		name   string
		active bool
	}
	found := map[int64]childRow{}
	for rows.Next() {
		var id int64
		var cr childRow
		if err := rows.Scan(&id, &cr.name, &cr.active); err != nil {
			rows.Close()
			return failDB(c, err)
		}
		found[id] = cr
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return failDB(c, err)
	}
	rows.Close()

	// Already-member check (UNIQUE(child,session) enforced pre-insert).
	existing := map[int64]bool{}
	mArgs := make([]any, 0, len(req.ChildIDs)+1)
	for _, cid := range req.ChildIDs {
		mArgs = append(mArgs, cid)
	}
	mArgs = append(mArgs, sid)
	mRows, err := tx.QueryContext(ctx,
		`SELECT child_id FROM session_members
		 WHERE child_id IN (`+placeholders(len(req.ChildIDs))+`) AND session_id = ?`,
		mArgs...)
	if err != nil {
		return failDB(c, err)
	}
	for mRows.Next() {
		var id int64
		if err := mRows.Scan(&id); err != nil {
			mRows.Close()
			return failDB(c, err)
		}
		existing[id] = true
	}
	if err := mRows.Err(); err != nil {
		mRows.Close()
		return failDB(c, err)
	}
	mRows.Close()

	seen := map[int64]bool{}
	added := make([]int64, 0, len(req.ChildIDs))
	for _, cid := range req.ChildIDs {
		if seen[cid] || existing[cid] {
			return fail(c, CodeChildDuplicate, MsgChildDuplicate)
		}
		cr, ok := found[cid]
		if !ok {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		if !cr.active {
			return fail(c, CodeChildInactive, MsgChildInactive)
		}
		seen[cid] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO session_members (session_id, group_id, child_id, child_name_snap, school_id)
			 VALUES (?, ?, ?, ?, ?)`, sid, gid, cid, cr.name, school); err != nil {
			if isDuplicateErr(err) {
				return fail(c, CodeChildDuplicate, MsgChildDuplicate)
			}
			return failDB(c, err)
		}
		added = append(added, cid)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}
	for _, cid := range added {
		s.publish(school, s.withCopyAvailable(ctx, school, Frame{
			Entity: "member.changed", EntityID: cid,
			SessionID: &sid, ChildID: cid, Action: "created",
		}))
	}
	return ok(c, fiber.Map{"added": added})
}

// handleMemberRemove — single remove with the SAME per-child wipe as the
// group cascade, silent (decision 13 / FL-08): rows in the Tx, disk after
// commit, link revocations published (consent + public links die with the
// child's session scope).
func (s *Server) handleMemberRemove(c fiber.Ctx) error {
	sid, okID := idParam(c, "sid")
	if !okID {
		return nil
	}
	gid, okG := idParam(c, "gid")
	if !okG {
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

	ctx, cancel := dbCtx(c)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()

	okE, err := s.lockSessionEditable(ctx, tx, c, school, sid)
	if err != nil {
		return err
	}
	if !okE {
		return nil
	}
	var probe int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM session_members
		 WHERE session_id = ? AND group_id = ? AND child_id = ?`,
		sid, gid, cid).Scan(&probe)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}

	w, err := s.wipeChildrenScopeTx(ctx, tx, school, sid, []int64{cid})
	if err != nil {
		return failDB(c, err)
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM session_members
		 WHERE session_id = ? AND group_id = ? AND child_id = ?`, sid, gid, cid)
	if err != nil {
		return failDB(c, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	s.unlinkPhotos(w.photoPaths)
	// Same post-commit copy_available hint as the add path (02 §2c "member
	// add / remove (+ copy_available hint recomputed)"): best-effort — an
	// error only logs and the frame ships without the hint.
	s.publish(school, s.withCopyAvailable(ctx, school, Frame{
		Entity: "member.changed", EntityID: cid,
		SessionID: &sid, ChildID: cid, Action: "deleted",
	}))
	s.publishWipedLinks(school, sid, w)
	return ok(c, fiber.Map{"removed": true})
}

// ---------------------------------------------------------------------------
// Copy-data antar-sesi (F-15 / FL-16 / decision 21) — fill-empty only
// ---------------------------------------------------------------------------

type copyCounts struct {
	Assessments int `json:"assessments"`
	Narratives  int `json:"narratives"`
	Missions    int `json:"missions"`
	Photos      int `json:"photos"`
}

type copyCandidate struct {
	SessionID   int64      `json:"session_id"`
	SessionName string     `json:"session_name"`
	SessionDate string     `json:"session_date"`
	Status      string     `json:"status"`
	Counts      copyCounts `json:"counts"`
}

// copyAvailable reports whether cid already has ANOTHER session of the same
// program (the session's own program) inside the same school — the exact
// predicate GET copy-candidates runs, reduced to a boolean. It is a
// post-commit hint only: callers publish it as member/session.changed
// {copy_available} and never fail a mutation on its error.
func (s *Server) copyAvailable(ctx context.Context, school, sid, cid int64) (bool, error) {
	var other int
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM sessions other
			WHERE other.school_id = ? AND other.id <> ?
			  AND other.program_id = (SELECT program_id FROM sessions WHERE id = ?)
			  AND EXISTS(SELECT 1 FROM session_members m
			             WHERE m.session_id = other.id AND m.child_id = ?))`,
		school, sid, sid, cid).Scan(&other); err != nil {
		return false, err
	}
	return other > 0, nil
}

// withCopyAvailable returns f with CopyAvailable filled from copyAvailable —
// the post-commit hint every member/session.changed frame carries (FL-07 /
// 02 §2c). Best-effort by contract: an error only logs, the frame ships
// without the hint and the mutation is never failed.
func (s *Server) withCopyAvailable(ctx context.Context, school int64, f Frame) Frame {
	if f.SessionID == nil || f.ChildID == 0 {
		return f
	}
	hint, err := s.copyAvailable(ctx, school, *f.SessionID, f.ChildID)
	if err != nil {
		slog.Error("copy_available hint", "err", err,
			"session_id", *f.SessionID, "child_id", f.ChildID)
		return f
	}
	f.CopyAvailable = &hint
	return f
}

// handleCopyCandidates — other same-school same-program sessions containing
// the child, FINISHED first then date DESC (full array, no paging).
func (s *Server) handleCopyCandidates(c fiber.Ctx) error {
	sid, okID := idParam(c, "sid")
	if !okID {
		return nil
	}
	cid, okC := idParam(c, "cid")
	if !okC {
		return nil
	}
	school, _ := c.Locals(kvSchool).(int64)

	ctx, cancel := dbCtx(c)
	defer cancel()

	// Target session + membership gate (candidate list only matters once the
	// child is in the target — same NOT_FOUND as copy execute).
	tcond, targs := schoolCond(school, "school_id")
	var tgtSchool, tgtProgram int64
	err := s.db.QueryRowContext(ctx,
		`SELECT school_id, program_id FROM sessions WHERE id = ?`+tcond,
		append([]any{sid}, targs...)...).Scan(&tgtSchool, &tgtProgram)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	var member int
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)`,
		sid, cid).Scan(&member); err != nil {
		return failDB(c, err)
	}
	if member == 0 {
		return fail(c, CodeNotFound, MsgNotFound)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT s.id, s.name, s.date, s.status,
		        (SELECT COUNT(*) FROM assessments a
		         WHERE a.session_id = s.id AND a.child_id = ?),
		        (SELECT COUNT(*) FROM narratives n
		         WHERE n.session_id = s.id AND n.child_id = ?),
		        (SELECT COUNT(*) FROM report_missions rm
		         WHERE rm.session_id = s.id AND rm.child_id = ?),
		        (SELECT COUNT(*) FROM photos p
		         WHERE p.session_id = s.id AND p.child_id = ? AND p.file_ready = TRUE)
		 FROM sessions s
		 WHERE s.school_id = ? AND s.program_id = ? AND s.id <> ?
		   AND EXISTS (SELECT 1 FROM session_members m
		               WHERE m.session_id = s.id AND m.child_id = ?)
		 ORDER BY (s.status = 'FINISHED') DESC, s.date DESC, s.id DESC`,
		cid, cid, cid, cid, tgtSchool, tgtProgram, sid, cid)
	if err != nil {
		return failDB(c, err)
	}
	defer rows.Close()
	out := []copyCandidate{}
	for rows.Next() {
		var (
			item copyCandidate
			date time.Time
		)
		if err := rows.Scan(&item.SessionID, &item.SessionName, &date, &item.Status,
			&item.Counts.Assessments, &item.Counts.Narratives,
			&item.Counts.Missions, &item.Counts.Photos); err != nil {
			return failDB(c, err)
		}
		item.SessionDate = fmtDate(date)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	return ok(c, out)
}

type copyReq struct {
	SourceSessionID int64 `json:"source_session_id"`
}

// copyJob carries one execution's fixed inputs through the per-category
// functions below (each keeps its own fill-empty counting).
type copyJob struct {
	school    int64
	srcSID    int64
	dstSID    int64
	cid       int64
	copier    int64
	childName string
	consent   string // target consent status ("" = never sent)
}

// copyMaps holds the master-id → snapshot-id matches used by every category:
// topic/activity matching is BY MASTER ID, never by name (F-15).
type copyMaps struct {
	dstTopic map[int64]int64 // master topic_id -> target session_topic id
	dstAct   map[int64]int64 // master activity_id -> target session_activity id
}

// photoCopy is one committed photo row waiting for its durable file. stid is
// the TARGET session_topic so the post-durability publish can carry it.
type photoCopy struct {
	id   int64
	stid int64
	src  string
	dst  string
}

// handleCopyExecute — ONE Tx (fill-empty upserts) + io.Copy files after
// commit; 200 withheld until every file is durable, then copy.completed.
func (s *Server) handleCopyExecute(c fiber.Ctx) error {
	sid, okID := idParam(c, "sid")
	if !okID {
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
	var req copyReq
	if !bindBody(c, &req) {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)

	ctx, cancel := dbCtx(c)
	defer cancel()

	// Target + source sessions (same school via the caller scope, same
	// program else VALIDATION).
	tcond, targs := schoolCond(school, "school_id")
	var tgtProgram int64
	var tgtStatus string
	err := s.db.QueryRowContext(ctx,
		`SELECT program_id, status FROM sessions WHERE id = ?`+tcond,
		append([]any{sid}, targs...)...).Scan(&tgtProgram, &tgtStatus)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	if req.SourceSessionID <= 0 || req.SourceSessionID == sid {
		return fail(c, CodeValidation, MsgValidation)
	}
	var srcSchool, srcProgram int64
	err = s.db.QueryRowContext(ctx,
		`SELECT school_id, program_id FROM sessions WHERE id = ?`+tcond,
		append([]any{req.SourceSessionID}, targs...)...).Scan(&srcSchool, &srcProgram)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	if srcProgram != tgtProgram {
		return fail(c, CodeValidation, MsgValidation)
	}

	// Membership on both ends (FL-16: source rows deleted mid-copy → the
	// membership/existence probes fail with NOT_FOUND before any write).
	for _, pair := range [][2]int64{{sid, cid}, {req.SourceSessionID, cid}} {
		var exists int
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)`,
			pair[0], pair[1]).Scan(&exists); err != nil {
			return failDB(c, err)
		}
		if exists == 0 {
			return fail(c, CodeNotFound, MsgNotFound)
		}
	}
	var childName string
	var childActive bool
	err = s.db.QueryRowContext(ctx,
		`SELECT name, is_active FROM children WHERE id = ? AND school_id = ?`,
		cid, school).Scan(&childName, &childActive)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	if !childActive {
		return fail(c, CodeChildInactive, MsgChildInactive)
	}
	// User decision 2026-10-09 (overrides "photo part may still proceed"): a
	// FINISHED target rejects the copy outright — 200 SESSION_LOCKED before
	// the idempotency claim, any Tx, any row, any file IO, any publish.
	// Source sessions stay unrestricted (Draft/Opened/Finished all allowed).
	if tgtStatus == "FINISHED" {
		return fail(c, CodeSessionLocked, MsgSessionLocked)
	}
	var consent string
	_ = s.db.QueryRowContext(ctx,
		`SELECT status FROM consent_links WHERE session_id = ? AND child_id = ?`,
		sid, cid).Scan(&consent) // no row = never sent ("" behaves as pending)

	job := copyJob{
		school: school, srcSID: req.SourceSessionID, dstSID: sid, cid: cid,
		copier: u.ID, childName: childName, consent: consent,
	}

	return s.idempotent(c, "POST "+c.Path(), func() error {
		ctx, cancel := dbCtx(c)
		defer cancel()
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return failDB(c, err)
		}
		defer tx.Rollback()

		maps := &copyMaps{
			dstTopic: map[int64]int64{},
			dstAct:   map[int64]int64{},
		}
		if err := loadCopyMaps(ctx, tx, job, maps); err != nil {
			return failDB(c, err)
		}

		var copied, skipped copyCounts
		if copied.Assessments, skipped.Assessments, err = copyAssessments(ctx, tx, job, maps); err != nil {
			return failDB(c, err)
		}
		if copied.Narratives, skipped.Narratives, err = copyNarratives(ctx, tx, job, maps); err != nil {
			return failDB(c, err)
		}
		if copied.Missions, skipped.Missions, err = copyMissions(ctx, tx, job, maps); err != nil {
			return failDB(c, err)
		}
		photos, pCopied, pSkipped, err := copyPhotos(ctx, tx, job, maps)
		copied.Photos, skipped.Photos = pCopied, pSkipped
		if err != nil {
			if err == errPhotoLimit {
				return fail(c, CodePhotoLimit, MsgPhotoLimit)
			}
			return failDB(c, err)
		}
		if err := tx.Commit(); err != nil {
			return failDB(c, err)
		}

		// Rows are committed file_ready=FALSE: land every file durable, then
		// flip each row (single-row UPDATE, idempotent). Any failure rolls
		// this execution's rows + files back so a retry starts clean (05 §5
		// honesty bar: no 200 + signal with missing files).
		for i := range photos {
			if err := s.landCopiedPhoto(photos[i]); err != nil {
				s.rollbackCopiedPhotos(photos)
				if errors.Is(err, errRejected) {
					return fail(c, CodeValidation, MsgValidation)
				}
				return err
			}
		}

		s.publish(school, Frame{
			Entity: "copy.completed", EntityID: 0,
			SessionID: &sid, ChildID: cid, Action: "updated",
		})
		// Public refetch for the topics whose photos actually landed (FL-10 /
		// 02 §2c: photo change reaches a still-valid public token). The
		// gallery renders ALL topics so it omits Stid; report.changed carries
		// the per-topic Stid. Only when photos were copied.
		if copied.Photos > 0 {
			seenStid := map[int64]bool{}
			for _, p := range photos {
				if seenStid[p.stid] {
					continue
				}
				seenStid[p.stid] = true
				s.publishPublic(Frame{
					Entity: "report.changed", SchoolID: school,
					SessionID: &sid, ChildID: cid, Stid: p.stid, Action: "updated",
				})
			}
			s.publishPublic(Frame{
				Entity: "gallery.changed", SchoolID: school,
				SessionID: &sid, ChildID: cid, Action: "updated",
			})
		}
		return ok(c, fiber.Map{
			"copied": copied, "skipped": skipped,
		})
	})
}

// errPhotoLimit marks a >10 copy into one target topic (Tx rolled back).
var errPhotoLimit = errors.New("photo limit")

// loadCopyMaps reads the target snapshot indexes for master-id matching.
func loadCopyMaps(ctx context.Context, tx *sql.Tx, job copyJob, maps *copyMaps) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, topic_id FROM session_topics WHERE session_id = ?`, job.dstSID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var stid, master int64
		if err := rows.Scan(&stid, &master); err != nil {
			rows.Close()
			return err
		}
		maps.dstTopic[master] = stid
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	rows, err = tx.QueryContext(ctx,
		`SELECT id, activity_id FROM session_activities WHERE session_id = ?`, job.dstSID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var said, master int64
		if err := rows.Scan(&said, &master); err != nil {
			return err
		}
		maps.dstAct[master] = said
	}
	return rows.Err()
}

// copyActRow is one source assessment row of the copy read.
type copyActRow struct {
	activityID int64
	stars      int
	note       sql.NullString
}

// copyCategory runs the shared fill-empty skeleton for ONE copy category
// (003d / F-02-10): read the source rows → load the already-filled target
// ids (skipped entirely when the source is empty) → per row map/skip/insert
// with duplicate-tolerant counting. The {copied, skipped} counters are
// contract-visible (copy response), so the skeleton is defined HERE once;
// per-category SQL and the row-level decisions stay explicit in the
// caller's closures. copyPhotos keeps its own loop (file contract).
func copyCategory[S any](
	ctx context.Context,
	tx *sql.Tx,
	srcQ string, srcArgs []any,
	scan func(rows *sql.Rows) (S, error),
	filledQ string, filledArgs []any,
	dstKey func(S) (int64, bool),
	insert func(dst int64, row S) (bool, error),
) (copied, skipped int, err error) {
	rows, err := tx.QueryContext(ctx, srcQ, srcArgs...)
	if err != nil {
		return 0, 0, err
	}
	var src []S
	for rows.Next() {
		r, scanErr := scan(rows)
		if scanErr != nil {
			rows.Close()
			return 0, 0, scanErr
		}
		src = append(src, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	rows.Close()

	var filled map[int64]bool
	if len(src) > 0 {
		fRows, err := tx.QueryContext(ctx, filledQ, filledArgs...)
		if err != nil {
			return 0, 0, err
		}
		filled = map[int64]bool{}
		for fRows.Next() {
			var id int64
			if err := fRows.Scan(&id); err != nil {
				fRows.Close()
				return 0, 0, err
			}
			filled[id] = true
		}
		if err := fRows.Err(); err != nil {
			fRows.Close()
			return 0, 0, err
		}
		fRows.Close()
	}

	for _, r := range src {
		dst, ok := dstKey(r)
		if !ok || filled[dst] {
			skipped++
			continue
		}
		ins, err := insert(dst, r)
		if err != nil {
			return 0, 0, err
		}
		if ins {
			copied++
		} else {
			skipped++
		}
	}
	return copied, skipped, nil
}

// copyAssessments fills empty target cells (matched by master activity_id);
// existing target stars are kept, unmatched/unfilled source rows skipped.
func copyAssessments(ctx context.Context, tx *sql.Tx, job copyJob, maps *copyMaps) (int, int, error) {
	return copyCategory(ctx, tx,
		`SELECT sa.activity_id, a.stars, a.note
		 FROM assessments a
		 JOIN session_activities sa ON sa.id = a.session_activity_id
		 WHERE a.session_id = ? AND a.child_id = ?`,
		[]any{job.srcSID, job.cid},
		func(rows *sql.Rows) (copyActRow, error) {
			var r copyActRow
			err := rows.Scan(&r.activityID, &r.stars, &r.note)
			return r, err
		},
		`SELECT session_activity_id FROM assessments
		 WHERE session_id = ? AND child_id = ?`,
		[]any{job.dstSID, job.cid},
		func(r copyActRow) (int64, bool) {
			dst, ok := maps.dstAct[r.activityID]
			return dst, ok
		},
		func(dst int64, r copyActRow) (bool, error) {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO assessments
				 (session_id, session_activity_id, child_id, school_id, stars, note,
				  activity_name_snap, child_name_snap, assessor_id)
				 SELECT ?, ?, ?, ?, ?, ?, sa.activity_name_snap, ?, ?
				 FROM session_activities sa WHERE sa.id = ?`,
				job.dstSID, dst, job.cid, job.school, r.stars, r.note,
				job.childName, job.copier, dst)
			if err != nil {
				if isDuplicateErr(err) { // concurrent fill-empty race: keep existing
					return false, nil
				}
				return false, err
			}
			return true, nil
		})
}

// copyNarrativeRow is one source narrative row of the copy read.
type copyNarrativeRow struct {
	topicID   int64
	text      string
	model     sql.NullString
	generated sql.NullTime
}

// copyNarratives inserts only when the target narrative is empty.
func copyNarratives(ctx context.Context, tx *sql.Tx, job copyJob, maps *copyMaps) (int, int, error) {
	return copyCategory(ctx, tx,
		`SELECT st.topic_id, n.text, n.model, n.generated_at
		 FROM narratives n
		 JOIN session_topics st ON st.id = n.session_topic_id
		 WHERE n.session_id = ? AND n.child_id = ?`,
		[]any{job.srcSID, job.cid},
		func(rows *sql.Rows) (copyNarrativeRow, error) {
			var r copyNarrativeRow
			err := rows.Scan(&r.topicID, &r.text, &r.model, &r.generated)
			return r, err
		},
		`SELECT session_topic_id FROM narratives
		 WHERE session_id = ? AND child_id = ?`,
		[]any{job.dstSID, job.cid},
		func(r copyNarrativeRow) (int64, bool) {
			dst, ok := maps.dstTopic[r.topicID]
			return dst, ok
		},
		func(dst int64, r copyNarrativeRow) (bool, error) {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO narratives
				 (session_id, session_topic_id, child_id, school_id, text, model,
				  generated_at, generated_by)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				job.dstSID, dst, job.cid, job.school, r.text, r.model,
				r.generated, job.copier)
			if err != nil {
				if isDuplicateErr(err) {
					return false, nil
				}
				return false, err
			}
			return true, nil
		})
}

// copyMissionRow is one source mission pick row of the copy read.
type copyMissionRow struct {
	topicID   int64
	missionID int64
	name      string
	body      string
}

// copyMissions copies picks only when the target topic has zero picks AND
// each mission still links that topic (mission_topics, master topic id).
func copyMissions(ctx context.Context, tx *sql.Tx, job copyJob, maps *copyMaps) (int, int, error) {
	return copyCategory(ctx, tx,
		`SELECT st.topic_id, rm.mission_id, rm.mission_name_snap, rm.mission_body_snap
		 FROM report_missions rm
		 JOIN session_topics st ON st.id = rm.session_topic_id
		 WHERE rm.session_id = ? AND rm.child_id = ?`,
		[]any{job.srcSID, job.cid},
		func(rows *sql.Rows) (copyMissionRow, error) {
			var r copyMissionRow
			err := rows.Scan(&r.topicID, &r.missionID, &r.name, &r.body)
			return r, err
		},
		`SELECT session_topic_id FROM report_missions
		 WHERE session_id = ? AND child_id = ?`,
		[]any{job.dstSID, job.cid},
		func(r copyMissionRow) (int64, bool) {
			dst, ok := maps.dstTopic[r.topicID]
			return dst, ok
		},
		func(dst int64, r copyMissionRow) (bool, error) {
			var linked int
			if err := tx.QueryRowContext(ctx,
				`SELECT EXISTS(SELECT 1 FROM mission_topics
				 WHERE mission_id = ? AND topic_id = ?)`,
				r.missionID, r.topicID).Scan(&linked); err != nil {
				return false, err
			}
			if linked == 0 {
				return false, nil
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO report_missions
				 (session_id, session_topic_id, child_id, mission_id,
				  mission_name_snap, mission_body_snap, school_id)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				job.dstSID, dst, job.cid, r.missionID, r.name, r.body, job.school); err != nil {
				if isDuplicateErr(err) {
					return false, nil
				}
				return false, err
			}
			return true, nil
		})
}

// copyPhotos inserts rows file_ready=FALSE only when the target topic has
// zero photos (all rows count toward quota, 05 §2), cap 10 in the locked Tx,
// never copying is_candidate (05 §2). Returns the rows awaiting their files.
func copyPhotos(ctx context.Context, tx *sql.Tx, job copyJob, maps *copyMaps) ([]photoCopy, int, int, error) {
	// Lock the child's target photo rows: this is the spec'd FOR UPDATE cap
	// count (06 §4) and serialises concurrent copies.
	lockRows, err := tx.QueryContext(ctx,
		`SELECT id, session_topic_id FROM photos
		 WHERE session_id = ? AND child_id = ? FOR UPDATE`, job.dstSID, job.cid)
	if err != nil {
		return nil, 0, 0, err
	}
	count := map[int64]int{}
	for lockRows.Next() {
		var id, stid int64
		if err := lockRows.Scan(&id, &stid); err != nil {
			lockRows.Close()
			return nil, 0, 0, err
		}
		count[stid]++
	}
	if err := lockRows.Err(); err != nil {
		lockRows.Close()
		return nil, 0, 0, err
	}
	lockRows.Close()

	rows, err := tx.QueryContext(ctx,
		`SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id
		 FROM photos p
		 JOIN session_topics st ON st.id = p.session_topic_id
		 WHERE p.session_id = ? AND p.child_id = ? AND p.file_ready = TRUE
		 ORDER BY p.id`, job.srcSID, job.cid)
	if err != nil {
		return nil, 0, 0, err
	}
	type srcPhoto struct {
		path     string
		mime     string
		fileSize int64
		frameID  sql.NullInt64
		topicID  int64
	}
	var src []srcPhoto
	for rows.Next() {
		var p srcPhoto
		if err := rows.Scan(&p.path, &p.mime, &p.fileSize, &p.frameID, &p.topicID); err != nil {
			rows.Close()
			return nil, 0, 0, err
		}
		src = append(src, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, 0, err
	}
	rows.Close()

	var pending []photoCopy
	var copied, skipped int
	inserted := map[int64]int{}
	for _, p := range src {
		// Consent gate (F-15): DITOLAK never copies; PENDING/no-row may
		// proceed — the gallery stays locked until Setuju. A FINISHED target
		// never reaches this function (rejected up front).
		if job.consent == "DITOLAK" {
			skipped++
			continue
		}
		dst, ok := maps.dstTopic[p.topicID]
		if !ok || count[dst] > 0 {
			skipped++
			continue
		}
		if count[dst]+inserted[dst] >= 10 {
			return nil, 0, 0, errPhotoLimit
		}
		name, err := randHex()
		if err != nil {
			return nil, 0, 0, err
		}
		ext := strings.TrimPrefix(filepath.Ext(p.path), ".")
		if ext == "" {
			skipped++
			continue
		}
		newRel := fmt.Sprintf("%d/%d/%d/%d/%s.%s",
			job.school, job.dstSID, dst, job.cid, name, ext)
		res, err := tx.ExecContext(ctx,
			`INSERT INTO photos
			 (school_id, session_id, session_topic_id, child_id, child_name_snap,
			  path, frame_id, is_candidate, file_size, mime, created_by, file_ready)
			 VALUES (?, ?, ?, ?, ?, ?, ?, FALSE, ?, ?, ?, FALSE)`,
			job.school, job.dstSID, dst, job.cid, job.childName,
			newRel, p.frameID, p.fileSize, p.mime, job.copier)
		if err != nil {
			if isDuplicateErr(err) {
				skipped++
				continue
			}
			return nil, 0, 0, err
		}
		rowID, _ := res.LastInsertId()
		pending = append(pending, photoCopy{id: rowID, stid: dst, src: p.path, dst: newRel})
		inserted[dst]++
		copied++
	}
	return pending, copied, skipped, nil
}

// landCopiedPhoto durably copies one source file and flips its row
// file_ready=TRUE (single-row, idempotent — 05 §5).
func (s *Server) landCopiedPhoto(p photoCopy) error {
	src, err := os.Open(filepath.Join(s.upload, filepath.FromSlash(p.src)))
	if err != nil {
		return err
	}
	// No size validation: files were client-validated at upload time
	// (Pembaruan 2026-10-11); only a full disk maps to errRejected below.
	_, err = durableWrite(s.upload, p.dst, src)
	if closeErr := src.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		if errors.Is(err, syscall.ENOSPC) {
			return fmt.Errorf("%w: disk full", errRejected)
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.db.ExecContext(ctx,
		`UPDATE photos SET file_ready = TRUE WHERE id = ? AND file_ready = FALSE`, p.id)
	return err
}

// rollbackCopiedPhotos removes this execution's photo rows and any landed
// files after a copy failure — a retried execution starts from zero photos
// again (fill-empty only-if-zero stays resumable).
func (s *Server) rollbackCopiedPhotos(photos []photoCopy) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, p := range photos {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM photos WHERE id = ?`, p.id); err != nil {
			slog.Error("copy rollback photo row", "err", err, "id", p.id)
		}
		if err := os.Remove(filepath.Join(s.upload, filepath.FromSlash(p.dst))); err != nil && !os.IsNotExist(err) {
			slog.Error("copy rollback photo file", "err", err, "path", p.dst)
		}
	}
}

// ---------------------------------------------------------------------------
// Session-scope wipes (option-A/B cascades) — rows inside the caller's Tx,
// disk + revocation signals strictly AFTER its commit
// ---------------------------------------------------------------------------

// linkKill is one public/consent token that dies with a wipe.
type linkKill struct {
	id    int64
	cid   int64
	token string
	kind  string // REPORT | GALLERY | CONSENT
}

// wipeTarget collects everything a committed wipe must do afterwards:
// unlink photo files, publish report-link.changed/consent.changed and push
// the terminal public link.revoked frames.
type wipeTarget struct {
	photoPaths []string
	links      []linkKill
	consents   int // consent_link rows deleted (drives one consent.changed)
}

// photoPathsTx selects then deletes every photo row of the children in this
// session, inside the open Tx; paths are unlinked by the caller post-commit.
func (s *Server) photoPathsTx(ctx context.Context, tx *sql.Tx, school, sid int64, childIDs []int64) ([]string, error) {
	if len(childIDs) == 0 {
		return nil, nil
	}
	tcond, targs := schoolCond(school, "school_id")
	ph := placeholders(len(childIDs))
	args := make([]any, 0, len(childIDs)+1)
	args = append(args, sid)
	for _, id := range childIDs {
		args = append(args, id)
	}
	selArgs := append(append([]any{}, args...), targs...)

	rows, err := tx.QueryContext(ctx,
		`SELECT path FROM photos WHERE session_id = ? AND child_id IN (`+ph+`)`+tcond,
		selArgs...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		paths = append(paths, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM photos WHERE session_id = ? AND child_id IN (`+ph+`)`+tcond,
		selArgs...); err != nil {
		return nil, err
	}
	return paths, nil
}

// wipeChildrenScopeTx deletes the listed children's session-scoped data
// (assessments, photo rows, narratives, mission picks, consent + public
// links) inside an open Tx and reports the post-commit work. Member rows are
// the caller's to delete (group delete vs single remove differ).
func (s *Server) wipeChildrenScopeTx(ctx context.Context, tx *sql.Tx, school, sid int64, childIDs []int64) (wipeTarget, error) {
	var w wipeTarget
	if len(childIDs) == 0 {
		return w, nil
	}
	paths, err := s.photoPathsTx(ctx, tx, school, sid, childIDs)
	if err != nil {
		return w, err
	}
	w.photoPaths = paths

	tcond, targs := schoolCond(school, "school_id")
	ph := placeholders(len(childIDs))
	base := func(cols string) (string, []any) {
		args := make([]any, 0, len(childIDs)+1)
		args = append(args, sid)
		for _, id := range childIDs {
			args = append(args, id)
		}
		args = append(args, targs...)
		return cols + ` WHERE session_id = ? AND child_id IN (` + ph + `)` + tcond, args
	}

	// Collect tokens first (the frames published after commit need them).
	q, qArgs := base(`SELECT id, child_id, token FROM consent_links`)
	rows, err := tx.QueryContext(ctx, q, qArgs...)
	if err != nil {
		return w, err
	}
	for rows.Next() {
		var l linkKill
		if err := rows.Scan(&l.id, &l.cid, &l.token); err != nil {
			rows.Close()
			return w, err
		}
		l.kind = "CONSENT"
		w.links = append(w.links, l)
		w.consents++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return w, err
	}
	rows.Close()

	q, qArgs = base(`SELECT id, child_id, token, kind FROM public_links WHERE revoked_at IS NULL`)
	rows, err = tx.QueryContext(ctx, q, qArgs...)
	if err != nil {
		return w, err
	}
	for rows.Next() {
		var l linkKill
		if err := rows.Scan(&l.id, &l.cid, &l.token, &l.kind); err != nil {
			rows.Close()
			return w, err
		}
		w.links = append(w.links, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return w, err
	}
	rows.Close()

	for _, table := range []string{"assessments", "narratives", "report_missions", "consent_links", "public_links"} {
		dq, dArgs := base(`DELETE FROM ` + table)
		if _, err := tx.ExecContext(ctx, dq, dArgs...); err != nil {
			return w, err
		}
	}
	return w, nil
}

// wipeChildSessionPhotos (B3 contract) deletes ALL photo rows for one
// child+session (original + copied) in ONE standalone Tx and returns the
// relative paths — the caller unlinks them AFTER this returns (post-commit)
// and publishes afterwards. Consent rows/links are untouched: that is the
// DITOLAK photo wipe only (F-10).
func (s *Server) wipeChildSessionPhotos(ctx context.Context, school, sid, cid int64) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	paths, err := s.photoPathsTx(ctx, tx, school, sid, []int64{cid})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return paths, nil
}

// unlinkPhotos removes photo files after their rows are gone; failures are
// logged (the hourly orphan sweep retries — deletion never fails the op).
func (s *Server) unlinkPhotos(paths []string) {
	for _, rel := range paths {
		if err := os.Remove(filepath.Join(s.upload, filepath.FromSlash(rel))); err != nil && !os.IsNotExist(err) {
			slog.Error("remove photo file", "err", err, "path", rel)
		}
	}
}

// publishWipedLinks emits the post-commit revocation signals of a wipe:
// report-link.changed per dead public link, one consent.changed for deleted
// consent rows, then the terminal public link.revoked per killed token
// (public + consent tokens both die — FL-08 "each revoked token").
func (s *Server) publishWipedLinks(school, sid int64, w wipeTarget) {
	for _, l := range w.links {
		if l.kind == "CONSENT" {
			continue
		}
		s.publish(school, Frame{
			Entity: "report-link.changed", EntityID: l.id,
			SessionID: &sid, Action: "deleted",
		})
	}
	if w.consents > 0 {
		s.publish(school, Frame{
			Entity: "consent.changed", SessionID: &sid, Action: "deleted",
		})
	}
	for _, l := range w.links {
		s.publishPublic(Frame{
			Entity: "link.revoked", SchoolID: school,
			SessionID: &sid, ChildID: l.cid, Action: "deleted",
			Token: l.token,
		})
	}
}

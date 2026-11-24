// assessments.go — Assessment domain (F-09, FL-09) + readiness: the
// per-activity star upsert behind prefix P, the assessment list, the
// whole-session progress matrix, session-level readiness, and the exported
// reportReadiness helper B3's P/readiness + report gates are built on.
//
// Gates every P op shares (decision 18 + 02 §2b): membership → NOT_FOUND,
// topic/activity linkage → NOT_FOUND, session must be OPENED for writes
// (DRAFT/FINISHED → SESSION_LOCKED), group with active owner →
// GROUP_WITHOUT_FACILITATOR, FAS foreign child → NOT_OWNER, inactive child →
// CHILD_INACTIVE. requirePChild (below) is the membership+ownership half B3
// may reuse for narrative/missions/report/links/send-report routes.
package internal

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v3"
)

// MissingItem is one unstarred activity in the readiness payload
// (contracts: missing:[{said,name}]).
type MissingItem struct {
	Said int64  `json:"said"`
	Name string `json:"name"`
}

// requirePChild runs the shared P-prefix gates: session membership (decision
// 18 → NOT_FOUND) and FAS ownership (→ NOT_OWNER). Empty code = pass.
func (s *Server) requirePChild(ctx context.Context, sid, cid, userID int64, role string) (string, error) {
	member, err := s.requireMembership(ctx, sid, cid)
	if err != nil {
		return "", err
	}
	if !member {
		return CodeNotFound, nil
	}
	if role != "FASILITATOR" {
		return "", nil
	}
	var facilitator sql.NullInt64
	err = s.db.QueryRowContext(ctx,
		`SELECT g.facilitator_id FROM session_members sm
		 JOIN session_groups g ON g.id = sm.group_id
		 WHERE sm.session_id = ? AND sm.child_id = ?`, sid, cid).Scan(&facilitator)
	if err == sql.ErrNoRows {
		return CodeNotFound, nil
	}
	if err != nil {
		return "", err
	}
	if !facilitator.Valid || facilitator.Int64 != userID {
		return CodeNotOwner, nil
	}
	return "", nil
}

// readyAct is one session activity inside the readiness snapshot: the snap
// name feeds the {said,name} missing list.
type readyAct struct {
	said int64
	name string
}

// readinessSession is the SESSION-varying half of the readiness computation
// (finding 002c, F-02-03): fetched ONCE per request and passed into
// readinessChild, which only scans the child's assessments. The badge half
// (program id, topic badge pair, live programs badge) loads only when the
// caller returns the badge booleans — callers that discard them never pay
// for it, and badgeDefined stays nil then (skipping the unknown-topic probe
// those callers already made upstream via photoScope/linkage).
type readinessSession struct {
	wantBadges   bool
	programID    int64
	progBadgeOK  bool
	acts         map[int64][]readyAct // named activities per topic (missing list)
	actIDs       map[int64][]int64    // ids per topic (shared completeness rule)
	badgeDefined map[int64]bool       // topic badge pair present (both-or-neither)
}

// topicComplete is THE shared completeness rule (002c / F-02-03): a topic is
// complete ⇔ every required activity id is present. reportReadiness's child
// computation (P/readiness, session readiness, send gates, report builder)
// and the session-reports matrix join all derive `complete` from this one
// loop — the matrix used to carry its own third copy of it.
func topicComplete(required []int64, present func(int64) bool) bool {
	for _, said := range required {
		if !present(said) {
			return false
		}
	}
	return true
}

// loadReadinessActivities groups ONE session's activities per topic — the
// session-varying activity set every readiness caller needs, fetched once
// per snapshot: named rows for the missing list plus the plain id slices
// that feed topicComplete.
func (s *Server) loadReadinessActivities(ctx context.Context, school, sid int64) (map[int64][]readyAct, map[int64][]int64, error) {
	tcond, targs := schoolCond(school, "school_id")
	rows, err := s.db.QueryContext(ctx,
		`SELECT sa.session_topic_id, sa.id, sa.activity_name_snap
		 FROM session_activities sa WHERE sa.session_id = ?`+tcond+
			` ORDER BY sa.session_topic_id, sa.sort_order, sa.id`,
		append([]any{sid}, targs...)...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	acts := map[int64][]readyAct{}
	ids := map[int64][]int64{}
	for rows.Next() {
		var topicID int64
		var a readyAct
		if err := rows.Scan(&topicID, &a.said, &a.name); err != nil {
			return nil, nil, err
		}
		acts[topicID] = append(acts[topicID], a)
		ids[topicID] = append(ids[topicID], a.said)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return acts, ids, nil
}

// loadReadinessSession fetches the session-varying inputs once per request
// (002c). wantBadges=false loads only the activity set: send gates and the
// assessment save discard the badge booleans, so they never run the
// program-id / topic-badge / programs lookups at all.
func (s *Server) loadReadinessSession(ctx context.Context, school, sid int64, wantBadges bool) (*readinessSession, error) {
	sess := &readinessSession{wantBadges: wantBadges}
	if wantBadges {
		tcond, targs := schoolCond(school, "school_id")
		// Program id feeds ONLY the live program-badge lookup below.
		if err := s.db.QueryRowContext(ctx,
			`SELECT program_id FROM sessions WHERE id = ?`+tcond,
			append([]any{sid}, targs...)...).Scan(&sess.programID); err != nil {
			return nil, err
		}
	}
	var err error
	if sess.acts, sess.actIDs, err = s.loadReadinessActivities(ctx, school, sid); err != nil {
		return nil, err
	}
	if !wantBadges {
		return sess, nil
	}
	tcond, targs := schoolCond(school, "school_id")

	// Topic badge snaps (both-or-neither by DDL CHECK) — also the session's
	// topic set behind the sql.ErrNoRows unknown-topic contract.
	sess.badgeDefined = map[int64]bool{}
	tRows, err := s.db.QueryContext(ctx,
		`SELECT id, badge_name_snap, badge_image_snap
		 FROM session_topics WHERE session_id = ?`+tcond,
		append([]any{sid}, targs...)...)
	if err != nil {
		return nil, err
	}
	for tRows.Next() {
		var id int64
		var name, image sql.NullString
		if err := tRows.Scan(&id, &name, &image); err != nil {
			tRows.Close()
			return nil, err
		}
		sess.badgeDefined[id] = name.Valid && image.Valid
	}
	if err := tRows.Err(); err != nil {
		tRows.Close()
		return nil, err
	}
	tRows.Close()

	// Program badge: LIVE from programs (never stored, F-09).
	var progBadge int
	if err := s.db.QueryRowContext(ctx,
		`SELECT (badge_name IS NOT NULL AND badge_image_path IS NOT NULL)
		 FROM programs WHERE id = ?`, sess.programID).Scan(&progBadge); err != nil {
		return nil, err
	}
	sess.progBadgeOK = progBadge == 1
	return sess, nil
}

// readinessChild runs the CHILD-varying half (002c): ONE assessments scan
// for (session, child) against a session snapshot loaded once per request.
// The badge booleans are real only when snap carries the badge half
// (snap.wantBadges); complete-only callers get false/false for free.
func (s *Server) readinessChild(ctx context.Context, school, sid, cid, stid int64, snap *readinessSession) (bool, []MissingItem, bool, bool, error) {
	missing := []MissingItem{}
	if snap.badgeDefined != nil {
		if _, ok := snap.badgeDefined[stid]; !ok {
			return false, missing, false, false, sql.ErrNoRows // topic not in this session
		}
	}
	tcond, targs := schoolCond(school, "school_id")

	// Assessed set for this child+session (the only child-varying query).
	assessed := map[int64]bool{}
	aRows, err := s.db.QueryContext(ctx,
		`SELECT session_activity_id FROM assessments
		 WHERE session_id = ? AND child_id = ?`+tcond,
		append([]any{sid, cid}, targs...)...)
	if err != nil {
		return false, missing, false, false, err
	}
	for aRows.Next() {
		var said int64
		if err := aRows.Scan(&said); err != nil {
			aRows.Close()
			return false, missing, false, false, err
		}
		assessed[said] = true
	}
	if err := aRows.Err(); err != nil {
		aRows.Close()
		return false, missing, false, false, err
	}
	aRows.Close()

	// Topic completeness + missing list (shared rule, same sort order).
	present := func(said int64) bool { return assessed[said] }
	complete := topicComplete(snap.actIDs[stid], present)
	if !complete {
		for _, a := range snap.acts[stid] {
			if !assessed[a.said] {
				missing = append(missing, MissingItem{Said: a.said, Name: a.name})
			}
		}
	}
	if !snap.wantBadges {
		return complete, missing, false, false, nil
	}
	// Program completeness: every session topic fully starred.
	programComplete := true
	for topicID := range snap.badgeDefined {
		if !topicComplete(snap.actIDs[topicID], present) {
			programComplete = false
			break
		}
	}
	return complete, missing,
		complete && snap.badgeDefined[stid],
		programComplete && snap.progBadgeOK, nil
}

// reportReadiness (B3 contract) computes the GET P/readiness shape:
// complete ⇔ every activity of the topic carries a star; missing lists the
// rest in sort order; badges are earned AND defined — the topic badge from
// the session snapshot, the program badge live from programs (both derived,
// never stored: F-09). school 0 (SA without X-School-ID) reads unscoped.
// sql.ErrNoRows = unknown session/topic in scope.
// One-shot entry point: loads the full session snapshot (badge half
// included — this signature returns the booleans) then the child scan.
// Batched callers hoist loadReadinessSession once per request instead
// (002c).
func (s *Server) reportReadiness(ctx context.Context, school, sid, cid, stid int64) (bool, []MissingItem, bool, bool, error) {
	sess, err := s.loadReadinessSession(ctx, school, sid, true)
	if err != nil {
		return false, []MissingItem{}, false, false, err
	}
	return s.readinessChild(ctx, school, sid, cid, stid, sess)
}

// RegisterAssessments mounts the assessment routes (prefix P + the
// session-level progress/readiness views).
func (s *Server) RegisterAssessments(api fiber.Router) {
	api.Put("/sessions/:sid/topics/:stid/children/:cid/activities/:said",
		s.authed(), s.handleAssessmentPut)
	api.Get("/sessions/:sid/topics/:stid/children/:cid/assessments",
		s.authed(), s.handleAssessmentsList)
	api.Get("/sessions/:sid/progress", s.authed(), s.handleSessionProgress)
}

type assessmentReq struct {
	Stars int    `json:"stars"`
	Note  string `json:"note"`
}

type assessmentJSON struct {
	Said       int64   `json:"said"`
	Stars      int     `json:"stars"`
	Note       *string `json:"note"`
	AssessorID *int64  `json:"assessor_id"`
	UpdatedAt  string  `json:"updated_at"`
}

// lockSessionOpen is the FL-12 status gate inside the write Tx (B1a): the
// session row is claimed FOR UPDATE so a concurrent OPEN→FINISHED (or
// DRAFT→OPEN) transition cannot land between the pre-read gate order and the
// upsert — only OPENED proceeds (DRAFT and FINISHED are SESSION_LOCKED, the
// same guard the handlers' pre-read applies, re-checked under the lock).
// false = NOT_FOUND or SESSION_LOCKED already written to the envelope; the
// caller rolls back. Mission PUT keeps lockSessionEditable (FINISHED-only).
func (s *Server) lockSessionOpen(ctx context.Context, tx *sql.Tx, c fiber.Ctx, school, sid int64) (bool, error) {
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
	if status != "OPENED" {
		return false, fail(c, CodeSessionLocked, MsgSessionLocked)
	}
	return true, nil
}

// handleAssessmentPut — single-row atomic upsert (LWW, F-09), assessor_id =
// caller. Session must be OPENED, group owned, child active (06 §5 gates).
func (s *Server) handleAssessmentPut(c fiber.Ctx) error {
	sid, okS := idParam(c, "sid")
	if !okS {
		return nil
	}
	stid, okT := idParam(c, "stid")
	if !okT {
		return nil
	}
	cid, okC := idParam(c, "cid")
	if !okC {
		return nil
	}
	said, okA := idParam(c, "said")
	if !okA {
		return nil
	}
	school, okTen := requireSchool(c)
	if !okTen {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)

	var req assessmentReq
	if !bindBody(c, &req) {
		return nil
	}
	if !validStars(req.Stars) || !validNote(req.Note) {
		return fail(c, CodeValidation, MsgValidation)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	// Membership + FAS ownership (decision 18 / E9).
	code, err := s.requirePChild(ctx, sid, cid, u.ID, u.Role)
	if err != nil {
		return failDB(c, err)
	}
	if code != "" {
		return fail(c, code, msgForCode(code))
	}

	// Writes only while OPENED (FL-09: DRAFT and FINISHED are SESSION_LOCKED).
	// This pre-read only orders the gates; the authoritative check re-runs
	// FOR UPDATE inside the write Tx below (B1a — no TOCTOU window).
	status, err := s.scopedStatus(ctx, sid, school)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	if status != "OPENED" {
		return fail(c, CodeSessionLocked, MsgSessionLocked)
	}

	// Topic + activity linkage in one scoped read (mismatch → NOT_FOUND).
	var activityName string
	tcond, targs := schoolCond(school, "sa.school_id")
	err = s.db.QueryRowContext(ctx,
		`SELECT sa.activity_name_snap FROM session_activities sa
		 WHERE sa.id = ? AND sa.session_topic_id = ? AND sa.session_id = ?`+tcond,
		append([]any{said, stid, sid}, targs...)...).Scan(&activityName)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}

	// Child row: exists + active (CHILD_INACTIVE before write, 06 §5).
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

	// Ownership depth: FAS must own the group (checked above); the group's
	// owner must be active or assessment is blocked (E14) for everyone.
	var facilitator sql.NullInt64
	err = s.db.QueryRowContext(ctx,
		`SELECT g.facilitator_id FROM session_members sm
		 JOIN session_groups g ON g.id = sm.group_id
		 WHERE sm.session_id = ? AND sm.child_id = ?`, sid, cid).Scan(&facilitator)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	if !facilitator.Valid {
		return fail(c, CodeGroupNoFacilitator, MsgGroupNoFacilitator)
	}
	ownerActive, err := ownerOK(ctx, s.db, school, facilitator.Int64)
	if err != nil {
		return failDB(c, err)
	}
	if !ownerActive {
		return fail(c, CodeGroupNoFacilitator, MsgGroupNoFacilitator)
	}

	// Atomic LWW upsert (05 §7) in ONE Tx, gated by the in-Tx FOR UPDATE
	// status re-read (B1a): updated_at always bumps, and a session that
	// finished after the pre-read rolls this write back.
	var note any
	if req.Note != "" {
		note = req.Note
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	okL, err := s.lockSessionOpen(ctx, tx, c, school, sid)
	if err != nil {
		return err
	}
	if !okL {
		return nil // NOT_FOUND / SESSION_LOCKED already written; rolled back
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO assessments
		 (session_id, session_activity_id, child_id, school_id, stars, note,
		  activity_name_snap, child_name_snap, assessor_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   stars = VALUES(stars), note = VALUES(note),
		   assessor_id = VALUES(assessor_id), updated_at = CURRENT_TIMESTAMP`,
		sid, said, cid, school, req.Stars, note,
		activityName, childName, u.ID); err != nil {
		return failDB(c, err)
	}
	var updatedAt time.Time
	if err := tx.QueryRowContext(ctx,
		`SELECT updated_at FROM assessments
		 WHERE session_activity_id = ? AND child_id = ?`, said, cid).
		Scan(&updatedAt); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	// Signals: the card refetch + the derived completeness banner (02 §2c).
	s.publish(school, Frame{
		Entity: "assessment.changed", EntityID: said,
		SessionID: &sid, ChildID: cid, Stid: stid, Action: "updated",
	})
	// Badges are discarded on this path (002c): load only the session
	// activity set once, then the child scan — no program/badge lookups.
	sess, rErr := s.loadReadinessSession(ctx, school, sid, false)
	var complete bool
	if rErr == nil {
		complete, _, _, _, rErr = s.readinessChild(ctx, school, sid, cid, stid, sess)
	}
	if rErr != nil {
		slog.Error("readiness after assessment save", "err", rErr,
			"session_id", sid, "child_id", cid, "stid", stid)
	} else {
		s.publish(school, Frame{
			Entity:    "readiness.changed",
			SessionID: &sid, ChildID: cid, Stid: stid,
			Action: "updated", Complete: &complete,
		})
	}

	out := assessmentJSON{
		Said: said, Stars: req.Stars, UpdatedAt: fmtTS(updatedAt),
	}
	if req.Note != "" {
		out.Note = &req.Note
	}
	out.AssessorID = &u.ID
	return ok(c, out)
}

// handleAssessmentsList — GET P/assessments: this topic, this child, only
// rows that exist (unanswered activities stay absent; the client merges
// with the detail activities list).
func (s *Server) handleAssessmentsList(c fiber.Ctx) error {
	ctx, cancel, sid, stid, cid, school, _, okP := s.pScope(c)
	if !okP {
		return nil
	}
	defer cancel()

	tcond, targs := schoolCond(school, "a.school_id")

	rows, err := s.db.QueryContext(ctx,
		`SELECT a.session_activity_id, a.stars, a.note, a.assessor_id, a.updated_at
		 FROM assessments a
		 JOIN session_activities sa ON sa.id = a.session_activity_id
		 WHERE a.session_id = ? AND a.child_id = ? AND sa.session_topic_id = ?`+tcond+
			` ORDER BY a.session_activity_id`,
		append([]any{sid, cid, stid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	defer rows.Close()
	out := []assessmentJSON{}
	for rows.Next() {
		var (
			item     assessmentJSON
			note     sql.NullString
			assessor sql.NullInt64
			updated  time.Time
		)
		if err := rows.Scan(&item.Said, &item.Stars, &note, &assessor, &updated); err != nil {
			return failDB(c, err)
		}
		item.Note = fromNull(note)
		if assessor.Valid {
			item.AssessorID = &assessor.Int64
		}
		item.UpdatedAt = fmtTS(updated)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	return ok(c, out)
}

// ---------------------------------------------------------------------------
// Progress matrix + session readiness
// ---------------------------------------------------------------------------

type progressTopic struct {
	Stid     int64  `json:"stid"`
	Topic    string `json:"topic"`
	Starred  int    `json:"starred"`
	Total    int    `json:"total"`
	Complete bool   `json:"complete"`
}

type progressChild struct {
	ChildID int64           `json:"child_id"`
	Name    string          `json:"name"`
	GID     int64           `json:"gid"`
	Topics  []progressTopic `json:"topics"`
}

// handleSessionProgress — GET /api/sessions/:sid/progress: the whole-session
// matrix {children:[{child_id,name,gid,topics:[…]}]}. FAS sees only the
// children of its own groups (foreign members stay behind the detail
// reduction).
func (s *Server) handleSessionProgress(c fiber.Ctx) error {
	sid, okS := idParam(c, "sid")
	if !okS {
		return nil
	}
	school, _ := c.Locals(kvSchool).(int64)
	u, _ := c.Locals(kvUser).(*userRow)

	ctx, cancel := dbCtx(c)
	defer cancel()

	ownGroups, err := s.fasOwnScope(ctx, u, sid, school)
	if err == errNotVisible {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	tcond, targs := schoolCond(school, "school_id")
	// Joined queries — the school predicate must reference ONE table or MySQL
	// calls it ambiguous (both joined tables carry school_id).
	smtcond, smtargs := schoolCond(school, "sm.school_id")
	acond, aargs := schoolCond(school, "a.school_id")
	if _, err := s.scopedStatus(ctx, sid, school); err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	} else if err != nil {
		return failDB(c, err)
	}

	// Topics (ordered) + activity totals.
	topics := []progressTopic{}
	topicIdx := map[int64]int{}
	tRows, err := s.db.QueryContext(ctx,
		`SELECT id, topic_name_snap FROM session_topics
		 WHERE session_id = ?`+tcond+` ORDER BY sort_order, id`,
		append([]any{sid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	for tRows.Next() {
		var id int64
		var name string
		if err := tRows.Scan(&id, &name); err != nil {
			tRows.Close()
			return failDB(c, err)
		}
		topics = append(topics, progressTopic{Stid: id, Topic: name})
		topicIdx[id] = len(topics) - 1
	}
	if err := tRows.Err(); err != nil {
		tRows.Close()
		return failDB(c, err)
	}
	tRows.Close()

	totalRows, err := s.db.QueryContext(ctx,
		`SELECT session_topic_id, COUNT(*) FROM session_activities
		 WHERE session_id = ?`+tcond+` GROUP BY session_topic_id`,
		append([]any{sid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	for totalRows.Next() {
		var stid int64
		var n int
		if err := totalRows.Scan(&stid, &n); err != nil {
			totalRows.Close()
			return failDB(c, err)
		}
		if idx, ok := topicIdx[stid]; ok {
			topics[idx].Total = n
		}
	}
	if err := totalRows.Err(); err != nil {
		totalRows.Close()
		return failDB(c, err)
	}
	totalRows.Close()

	// Members (FAS: own groups only).
	memberQ := `SELECT sm.child_id, sm.group_id, c.name
		 FROM session_members sm
		 JOIN children c ON c.id = sm.child_id
		 WHERE sm.session_id = ?` + smtcond
	memberArgs := append([]any{sid}, smtargs...)
	if ownGroups != nil {
		if len(ownGroups) == 0 {
			return ok(c, fiber.Map{"children": []progressChild{}})
		}
		ids := make([]any, 0, len(ownGroups))
		ph := make([]string, 0, len(ownGroups))
		for gid := range ownGroups {
			ids = append(ids, gid)
			ph = append(ph, "?")
		}
		memberQ += ` AND sm.group_id IN (` + placeholders(len(ids)) + `)`
		memberArgs = append(memberArgs, ids...)
	}
	memberQ += ` ORDER BY sm.group_id, sm.child_id`
	mRows, err := s.db.QueryContext(ctx, memberQ, memberArgs...)
	if err != nil {
		return failDB(c, err)
	}
	children := []progressChild{}
	childIdx := map[int64]int{}
	for mRows.Next() {
		var ch progressChild
		if err := mRows.Scan(&ch.ChildID, &ch.GID, &ch.Name); err != nil {
			mRows.Close()
			return failDB(c, err)
		}
		children = append(children, ch)
		childIdx[ch.ChildID] = len(children) - 1
	}
	if err := mRows.Err(); err != nil {
		mRows.Close()
		return failDB(c, err)
	}
	mRows.Close()

	// Starred counts per (child, topic).
	starQ := `SELECT a.child_id, sa.session_topic_id, COUNT(*)
		 FROM assessments a
		 JOIN session_activities sa ON sa.id = a.session_activity_id
		 WHERE a.session_id = ?` + acond + ` GROUP BY a.child_id, sa.session_topic_id`
	sRows, err := s.db.QueryContext(ctx, starQ, append([]any{sid}, aargs...)...)
	if err != nil {
		return failDB(c, err)
	}
	type ct struct{ child, stid int64 }
	starred := map[ct]int{}
	for sRows.Next() {
		var ch, stid int64
		var n int
		if err := sRows.Scan(&ch, &stid, &n); err != nil {
			sRows.Close()
			return failDB(c, err)
		}
		starred[ct{ch, stid}] = n
	}
	if err := sRows.Err(); err != nil {
		sRows.Close()
		return failDB(c, err)
	}
	sRows.Close()

	for i := range children {
		children[i].Topics = make([]progressTopic, len(topics))
		for j, t := range topics {
			row := t
			row.Starred = starred[ct{children[i].ChildID, t.Stid}]
			row.Complete = row.Starred >= row.Total && row.Total > 0
			children[i].Topics[j] = row
		}
	}
	return ok(c, fiber.Map{"children": children})
}

// handleSessionReadiness — GET /api/sessions/:sid/readiness?child_id=&
// session_topic_id= (02 §2b session-scoped twin of P/readiness).
func (s *Server) handleSessionReadiness(c fiber.Ctx) error {
	sid, okS := idParam(c, "sid")
	if !okS {
		return nil
	}
	var q struct {
		ChildID int64 `query:"child_id"`
		Stid    int64 `query:"session_topic_id"`
	}
	if err := c.Bind().Query(&q); err != nil || q.ChildID <= 0 || q.Stid <= 0 {
		return fail(c, CodeValidation, MsgValidation)
	}
	school, _ := c.Locals(kvSchool).(int64)
	u, _ := c.Locals(kvUser).(*userRow)

	ctx, cancel := dbCtx(c)
	defer cancel()

	code, err := s.requirePChild(ctx, sid, q.ChildID, u.ID, u.Role)
	if err != nil {
		return failDB(c, err)
	}
	if code != "" {
		return fail(c, code, msgForCode(code))
	}
	tcond, targs := schoolCond(school, "school_id")
	var probe int
	if err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?`+tcond,
		append([]any{q.Stid, sid}, targs...)...).Scan(&probe); err != nil {
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		return failDB(c, err)
	}

	complete, missing, topicBadge, programBadge, err := s.reportReadiness(
		ctx, school, sid, q.ChildID, q.Stid)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	return ok(c, fiber.Map{
		"complete": complete,
		"missing":  missing,
		"badges":   fiber.Map{"topic": topicBadge, "program": programBadge},
	})
}

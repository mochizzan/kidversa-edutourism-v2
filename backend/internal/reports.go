// reports.go — Report domain (F-11/F-12, FL-12/13): readiness gate, the
// canonical ReportData builder shared by GET P/report and the public token
// endpoint, missions replace-set, AI/manual narrative, public links
// create/revoke/regenerate, WA single + batch send and the per-session ops
// matrix.
//
// Gates reuse B2 verbatim: requirePChild (membership → NOT_FOUND, FAS
// ownership → NOT_OWNER), photoScope (stid∈sid linkage probe), scopedStatus,
// reportReadiness — never reimplemented here.
//
// # Spec interpretations (reported to the orchestrator)
//
//   - Program badge: session snapshots only copy TOPIC badges
//     (session_topics.badge_*_snap; 05 §2). The program badge is read LIVE
//     from programs.badge_name/badge_image_path via the session's program_id
//     ("Missions/frames LIVE" precedent; the janitor already keeps
//     programs.badge_image_path files referenced).
//   - Public stream wiring: W1's publicStreamGuard/publicScope (stream.go)
//     resolves ?token= with its OWN SQL against public_links/consent_links —
//     no provider/callback exists, so RegisterPublic wires nothing for SSE;
//     public.go's REST token endpoints wrap the same publicScope (003b).
//   - send-report on DRAFT → SESSION_LOCKED (F-12 allows OPENED+FINISHED).
//   - PUT P/missions is blocked only on FINISHED (FL-16 "missions blocked on
//     FINISHED"); DRAFT/OPENED allowed. CHILD_INACTIVE applies (06 §5).
//   - Mission replace-set publishes mission-pick.changed AND public
//     report.changed (02 §2c lists "mission" changes under public
//     report.changed — the pick changes what the public report shows).
//   - POST/PATCH P/narrative require OPENED ("guards same as assessment",
//     FL-12). PATCH text is validated ≤2000 chars and non-empty (a manual
//     edit may create the row: model/generated_at stay null).
//   - REPORT_INCOMPLETE on single send carries an extra machine-readable
//     error key `missing` (same convention as error.groups).
//   - Batch jitter sleeps 3–10 s (math/rand/v2) BETWEEN send attempts only
//     (never after the last, never for gate-failed items); each item gets a
//     fresh 5 s dbCtx so the loop is never killed by the statement budget.
//     Per-item DB failures become per-item code DB_UNAVAILABLE (a top-level
//     DB_UNAVAILABLE would release the idem claim and double-send on retry).
//   - batch-send publishes ONE report-link.changed per session after the
//     loop (never per item, FL-13 tail) and public report.changed/
//     gallery.changed per child that received a SENT (gallery.changed is
//     published WITHOUT Stid: the gallery token is topic-scoped for routing
//     but renders ALL topics, so every gallery page of that child+session
//     must refetch).
//   - GET /api/sessions/:sid/reports computes `complete` in Go from set
//     queries through topicComplete — the same shared completeness rule
//     readinessChild uses for reportReadiness and the send gates (002c) —
//     no N+1.
//     candidate_photo: null when the pair has zero durable photos, else
//     whether the candidate slot is set. URLs come from active links.
package internal

import (
	"context"
	"database/sql"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

// ---------------------------------------------------------------------------
// Canonical ReportData wire shape (contracts-shapes.md — emitted 1:1 by
// GET P/report AND GET /public/report?token=)
// ---------------------------------------------------------------------------

type reportSessionJSON struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Date     string `json:"date"`
	Location string `json:"location"`
	Status   string `json:"status"`
}

type reportProgramJSON struct {
	Name string `json:"name"`
}

type reportTopicJSON struct {
	Stid int64  `json:"stid"`
	Name string `json:"name"`
}

type reportChildJSON struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Age          int     `json:"age"`
	School       string  `json:"school"`
	MotherName   string  `json:"mother_name"`
	FatherName   *string `json:"father_name"`
	GuardianName *string `json:"guardian_name"`
}

type reportGroupJSON struct {
	Name string `json:"name"`
}

type reportAttestation struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

type reportNarrativeJSON struct {
	Text        *string `json:"text"`
	GeneratedAt *string `json:"generated_at"`
}

type reportOutcome struct {
	Said      int64   `json:"said"`
	Name      string  `json:"activity_name"`
	Indicator *string `json:"indicator"`
	Stars     int     `json:"stars"`
	Note      *string `json:"note"`
}

type reportMission struct {
	MissionID int64  `json:"mission_id"`
	Name      string `json:"name"`
	Body      string `json:"body"`
}

type reportBadge struct {
	Name     string `json:"name"`
	ImageURL string `json:"image_url"`
}

type reportBadges struct {
	Topic   *reportBadge `json:"topic"`
	Program *reportBadge `json:"program"`
}

type candidatePhotoJSON struct {
	URL string `json:"url"`
}

type reportJSON struct {
	Session        reportSessionJSON   `json:"session"`
	Program        reportProgramJSON   `json:"program"`
	Topic          reportTopicJSON     `json:"topic"`
	Child          reportChildJSON     `json:"child"`
	Group          *reportGroupJSON    `json:"group"` // null when membership is gone
	Attestation    reportAttestation   `json:"attestation"`
	Narrative      reportNarrativeJSON `json:"narrative"`
	Outcomes       []reportOutcome     `json:"outcomes"`
	Missions       []reportMission     `json:"missions"`
	Badges         reportBadges        `json:"badges"`
	CandidatePhoto *candidatePhotoJSON `json:"candidate_photo"`
	Complete       bool                `json:"complete"`
	Missing        []MissingItem       `json:"missing"`
}

// narrativeJSON is the GET/POST/PATCH P/narrative shape {text, model,
// generated_at} (model/generated_at null for manual edits).
type narrativeJSON struct {
	Text        string  `json:"text"`
	Model       *string `json:"model"`
	GeneratedAt *string `json:"generated_at"`
}

// buildReportData assembles the canonical ReportData for one
// child+session+topic — the SINGLE builder behind the auth and public paths.
// school 0 (SA without X-School-ID) reads unscoped. sql.ErrNoRows = the
// session/topic/child is not in scope.
func (s *Server) buildReportData(ctx context.Context, school, sid, stid, cid int64) (*reportJSON, error) {
	tcond, targs := schoolCond(school, "school_id")
	out := &reportJSON{Outcomes: []reportOutcome{}, Missions: []reportMission{}}

	// Session snapshot row (program name snap lives here).
	var (
		sessDate    time.Time
		programID   int64
		programName string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, date, location, status, program_id, program_name_snap
		 FROM sessions WHERE id = ?`+tcond,
		append([]any{sid}, targs...)...).
		Scan(&out.Session.ID, &out.Session.Name, &sessDate, &out.Session.Location,
			&out.Session.Status, &programID, &programName)
	if err != nil {
		return nil, err
	}
	out.Session.Date = fmtDate(sessDate)
	out.Program.Name = programName

	// Topic snapshot + topic badge pair (both-or-neither by DDL CHECK).
	var badgeName, badgeImage sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT topic_name_snap, badge_name_snap, badge_image_snap
		 FROM session_topics WHERE id = ? AND session_id = ?`+tcond,
		append([]any{stid, sid}, targs...)...).
		Scan(&out.Topic.Name, &badgeName, &badgeImage)
	if err != nil {
		return nil, err
	}
	out.Topic.Stid = stid
	if badgeName.Valid && badgeImage.Valid {
		out.Badges.Topic = &reportBadge{Name: badgeName.String, ImageURL: "/uploads/" + badgeImage.String}
	}

	// Program badge: LIVE from programs (only topic badges are snapshotted).
	var progBadgeName, progBadgePath sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT badge_name, badge_image_path FROM programs WHERE id = ?`, programID).
		Scan(&progBadgeName, &progBadgePath); err != nil {
		return nil, err
	}
	if progBadgeName.Valid && progBadgePath.Valid {
		out.Badges.Program = &reportBadge{Name: progBadgeName.String, ImageURL: "/uploads/" + progBadgePath.String}
	}

	// Child (PII by design: the report is a public token endpoint).
	var fatherName, guardian sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT name, age, school, mother_name, father_name, guardian_name
		 FROM children WHERE id = ?`+tcond,
		append([]any{cid}, targs...)...).
		Scan(&out.Child.Name, &out.Child.Age, &out.Child.School,
			&out.Child.MotherName, &fatherName, &guardian)
	if err != nil {
		return nil, err
	}
	out.Child.ID = cid
	out.Child.FatherName = fromNull(fatherName)
	out.Child.GuardianName = fromNull(guardian)

	// Group + attestation: LIVE current owner name, facilitator_name_snap
	// fallback when the owner row is gone (F-12 option A). Group is nullable
	// per contracts: no membership row → emit null (scan into locals first —
	// taking &out.Group.Name through the nil pointer panics).
	out.Attestation.Role = "Fasilitator"
	var (
		groupName     string
		facilitatorID sql.NullInt64
		facSnap       string
	)
	err = s.db.QueryRowContext(ctx,
		`SELECT g.name, g.facilitator_id, g.facilitator_name_snap
		 FROM session_members sm JOIN session_groups g ON g.id = sm.group_id
		 WHERE sm.session_id = ? AND sm.child_id = ?`, sid, cid).
		Scan(&groupName, &facilitatorID, &facSnap)
	switch {
	case err == sql.ErrNoRows:
		// Membership wiped (public links die with it; defensive null-safe):
		// group stays null, attestation name stays "".
	case err != nil:
		return nil, err
	default:
		out.Group = &reportGroupJSON{Name: groupName}
		out.Attestation.Name = facSnap
		if facilitatorID.Valid {
			var ownerName string
			if oErr := s.db.QueryRowContext(ctx,
				`SELECT name FROM users WHERE id = ?`, facilitatorID.Int64).Scan(&ownerName); oErr == nil {
				out.Attestation.Name = ownerName
			} else if oErr != sql.ErrNoRows {
				return nil, oErr
			}
		}
	}

	// Narrative (null-safe: absent row = null text/generated_at).
	var (
		narrText  string
		narrModel sql.NullString
		narrGenAt sql.NullTime
		narrFound bool
	)
	err = s.db.QueryRowContext(ctx,
		`SELECT text, model, generated_at FROM narratives
		 WHERE session_id = ? AND session_topic_id = ? AND child_id = ?`,
		sid, stid, cid).Scan(&narrText, &narrModel, &narrGenAt)
	switch {
	case err == nil:
		narrFound = true
	case err == sql.ErrNoRows:
	default:
		return nil, err
	}
	if narrFound {
		text := narrText
		out.Narrative.Text = &text
		if narrGenAt.Valid {
			ts := fmtTS(narrGenAt.Time)
			out.Narrative.GeneratedAt = &ts
		}
	}

	// Outcomes: assessments of this topic+child in activity sort order.
	// JOIN — the school predicate must be qualified or MySQL calls it
	// ambiguous (both assessments and session_activities carry school_id).
	aCond, aArgs := schoolCond(school, "a.school_id")
	oRows, err := s.db.QueryContext(ctx,
		`SELECT a.session_activity_id, a.activity_name_snap, sa.indicator_snap,
		        a.stars, a.note
		 FROM assessments a
		 JOIN session_activities sa ON sa.id = a.session_activity_id
		 WHERE a.session_id = ? AND a.child_id = ? AND sa.session_topic_id = ?`+aCond+
			` ORDER BY sa.sort_order, sa.id`,
		append([]any{sid, cid, stid}, aArgs...)...)
	if err != nil {
		return nil, err
	}
	for oRows.Next() {
		var (
			o         reportOutcome
			indicator sql.NullString
			note      sql.NullString
		)
		if err := oRows.Scan(&o.Said, &o.Name, &indicator, &o.Stars, &note); err != nil {
			oRows.Close()
			return nil, err
		}
		o.Indicator = fromNull(indicator)
		o.Note = fromNull(note)
		out.Outcomes = append(out.Outcomes, o)
	}
	if err := oRows.Err(); err != nil {
		oRows.Close()
		return nil, err
	}
	oRows.Close()

	// Mission picks (snapped).
	mRows, err := s.db.QueryContext(ctx,
		`SELECT mission_id, mission_name_snap, mission_body_snap
		 FROM report_missions
		 WHERE session_id = ? AND session_topic_id = ? AND child_id = ?
		 ORDER BY mission_id`, sid, stid, cid)
	if err != nil {
		return nil, err
	}
	for mRows.Next() {
		var m reportMission
		if err := mRows.Scan(&m.MissionID, &m.Name, &m.Body); err != nil {
			mRows.Close()
			return nil, err
		}
		out.Missions = append(out.Missions, m)
	}
	if err := mRows.Err(); err != nil {
		mRows.Close()
		return nil, err
	}
	mRows.Close()

	// Candidate photo: the single candidate of this topic; E29 honest-file
	// rule — a row whose bytes are gone renders as null, never a broken URL.
	var candPath string
	err = s.db.QueryRowContext(ctx,
		`SELECT path FROM photos
		 WHERE session_id = ? AND session_topic_id = ? AND child_id = ?
		   AND is_candidate = TRUE AND file_ready = TRUE`+tcond+` LIMIT 1`,
		append([]any{sid, stid, cid}, targs...)...).Scan(&candPath)
	switch {
	case err == nil:
		if _, statErr := os.Stat(filepath.Join(s.upload, filepath.FromSlash(candPath))); statErr == nil {
			out.CandidatePhoto = &candidatePhotoJSON{URL: "/uploads/" + candPath}
		}
	case err == sql.ErrNoRows:
	default:
		return nil, err
	}

	// Completeness banner (never gates the render). The session-varying
	// snapshot is assembled from what this builder ALREADY fetched (002c):
	// program_id came with the session row and the stid badge pair with the
	// topic row — so the readiness path runs only its activity-set query and
	// the child's assessments scan here, never its own program/topic/badge
	// lookups (the badge booleans are discarded on this path anyway).
	sess := &readinessSession{
		programID:    programID,
		badgeDefined: map[int64]bool{stid: badgeName.Valid && badgeImage.Valid},
	}
	if sess.acts, sess.actIDs, err = s.loadReadinessActivities(ctx, school, sid); err != nil {
		return nil, err
	}
	complete, missing, _, _, err := s.readinessChild(ctx, school, sid, cid, stid, sess)
	if err != nil {
		return nil, err
	}
	out.Complete, out.Missing = complete, missing
	return out, nil
}

// ---------------------------------------------------------------------------
// Routes
// ---------------------------------------------------------------------------

// RegisterReports mounts the report routes (prefix P + the token-level
// link routes + the session batch/matrix views). main.go wires it.
func (s *Server) RegisterReports(api fiber.Router) {
	api.Get(pPrefix+"/readiness", s.authed(), s.handleReportReadiness)
	api.Get(pPrefix+"/report", s.authed(), s.handleReportGet)
	api.Get(pPrefix+"/missions", s.authed(), s.handleMissionList)
	api.Put(pPrefix+"/missions", s.authed(), s.handleMissionPut)
	api.Get(pPrefix+"/narrative", s.authed(), s.handleNarrativeGet)
	api.Post(pPrefix+"/narrative", s.authed(), s.handleNarrativePost)
	api.Patch(pPrefix+"/narrative", s.authed(), s.handleNarrativePatch)
	api.Get(pPrefix+"/public-links", s.authed(), s.handleLinkList)
	api.Post(pPrefix+"/public-links", s.authed(), s.handleLinkCreate)
	api.Post("/public-links/:token/revoke", s.authed(), s.handleLinkRevoke)
	api.Post("/public-links/:token/regenerate", s.authed(), s.handleLinkRegenerate)
	api.Post(pPrefix+"/send-report",
		newLimiter(5, time.Minute, keyIP), s.authed(), s.handleSendReport)
	api.Post("/sessions/:sid/batch-send",
		newLimiter(2, time.Minute, keyIP), s.authed(), s.handleBatchSend)
	api.Get("/sessions/:sid/reports", s.authed(), s.handleSessionReports)
}

// tokenParam reads the :token route parameter; false = TOKEN_INVALID already
// written (identical shape for malformed/unknown — no oracle).
func tokenParam(c fiber.Ctx) (string, bool) {
	var params map[string]string
	_ = c.Bind().URI(&params)
	tok := params["token"]
	if tok == "" {
		fail(c, CodeTokenInvalid, MsgTokenInvalid)
		return "", false
	}
	return tok, true
}

// handleReportReadiness — GET P/readiness (NO meta; contracts shape).
func (s *Server) handleReportReadiness(c fiber.Ctx) error {
	ctx, cancel, sid, stid, cid, school, _, okP := s.pScope(c)
	if !okP {
		return nil
	}
	defer cancel()

	complete, missing, topicBadge, programBadge, err := s.reportReadiness(ctx, school, sid, cid, stid)
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

// handleReportGet — GET P/report: full ReportData even when incomplete
// (public render is never gated).
func (s *Server) handleReportGet(c fiber.Ctx) error {
	ctx, cancel, sid, stid, cid, school, _, okP := s.pScope(c)
	if !okP {
		return nil
	}
	defer cancel()

	data, err := s.buildReportData(ctx, school, sid, stid, cid)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	return ok(c, data)
}

// ---------------------------------------------------------------------------
// Missions replace-set
// ---------------------------------------------------------------------------

// handleMissionList — GET P/missions → [{mission_id, name, body}].
func (s *Server) handleMissionList(c fiber.Ctx) error {
	ctx, cancel, sid, stid, cid, _, _, okP := s.pScope(c)
	if !okP {
		return nil
	}
	defer cancel()
	return ok(c, s.missionPicks(ctx, sid, stid, cid))
}

// missionPicks reads the current snapped picks (ordered by mission id).
func (s *Server) missionPicks(ctx context.Context, sid, stid, cid int64) []reportMission {
	rows, err := s.db.QueryContext(ctx,
		`SELECT mission_id, mission_name_snap, mission_body_snap
		 FROM report_missions
		 WHERE session_id = ? AND session_topic_id = ? AND child_id = ?
		 ORDER BY mission_id`, sid, stid, cid)
	if err != nil {
		slog.Error("mission picks", "err", err, "session_id", sid, "child_id", cid)
		return []reportMission{}
	}
	defer rows.Close()
	out := []reportMission{}
	for rows.Next() {
		var m reportMission
		if err := rows.Scan(&m.MissionID, &m.Name, &m.Body); err != nil {
			slog.Error("mission picks scan", "err", err)
			return []reportMission{}
		}
		out = append(out, m)
	}
	return out
}

// handleMissionPut — PUT P/missions {mission_ids[]}: ONE Tx replace-set, ≤4
// (MISSION_LIMIT), each mission must exist in-school AND link this topic's
// master topic via mission_topics (else VALIDATION). FINISHED →
// SESSION_LOCKED (FL-16); inactive child → CHILD_INACTIVE (06 §5).
func (s *Server) handleMissionPut(c fiber.Ctx) error {
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
	school, okTen := requireSchool(c)
	if !okTen {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)

	var req struct {
		MissionIDs []int64 `json:"mission_ids"`
	}
	if !bindBody(c, &req) {
		return nil
	}
	if len(req.MissionIDs) > 4 {
		return fail(c, CodeMissionLimit, MsgMissionLimit)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	code, err := s.requirePChild(ctx, sid, cid, u.ID, u.Role)
	if err != nil {
		return failDB(c, err)
	}
	if code != "" {
		return fail(c, code, msgForCode(code))
	}
	tcond, targs := schoolCond(school, "school_id")
	var topicID int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT topic_id FROM session_topics WHERE id = ? AND session_id = ?`+tcond,
		append([]any{stid, sid}, targs...)...).Scan(&topicID); err != nil {
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		return failDB(c, err)
	}
	var active bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT is_active FROM children WHERE id = ? AND school_id = ?`, cid, school).
		Scan(&active); err != nil {
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		return failDB(c, err)
	}
	if !active {
		return fail(c, CodeChildInactive, MsgChildInactive)
	}
	// Pre-read orders the gates only; the authoritative FINISHED check
	// re-runs FOR UPDATE inside the write Tx below (B1a — no TOCTOU window).
	status, err := s.scopedStatus(ctx, sid, school)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	if status == "FINISHED" {
		return fail(c, CodeSessionLocked, MsgSessionLocked)
	}

	// Validate the pick set (dedup keeps the Tx insert conflict-free).
	seen := make(map[int64]bool, len(req.MissionIDs))
	type pick struct {
		id   int64
		name string
		body string
	}
	picks := make([]pick, 0, len(req.MissionIDs))
	for _, id := range req.MissionIDs {
		if id <= 0 || seen[id] {
			return fail(c, CodeValidation, MsgValidation)
		}
		seen[id] = true
		var p pick
		if err := s.db.QueryRowContext(ctx,
			`SELECT name, body FROM missions WHERE id = ? AND school_id = ?`, id, school).
			Scan(&p.name, &p.body); err != nil {
			if err == sql.ErrNoRows {
				return fail(c, CodeValidation, MsgValidation)
			}
			return failDB(c, err)
		}
		var linked int
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM mission_topics WHERE mission_id = ? AND topic_id = ?)`,
			id, topicID).Scan(&linked); err != nil {
			return failDB(c, err)
		}
		if linked == 0 {
			return fail(c, CodeValidation, MsgValidation)
		}
		p.id = id
		picks = append(picks, p)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	// In-Tx FOR UPDATE status re-read (B1a): a session finishing after the
	// pre-read rolls the replace-set back instead of landing the writes.
	okE, err := s.lockSessionEditable(ctx, tx, c, school, sid)
	if err != nil {
		return err
	}
	if !okE {
		return nil // NOT_FOUND / SESSION_LOCKED already written; rolled back
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM report_missions
		 WHERE session_id = ? AND session_topic_id = ? AND child_id = ?`,
		sid, stid, cid); err != nil {
		return failDB(c, err)
	}
	for _, p := range picks {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO report_missions
			 (session_id, session_topic_id, child_id, mission_id,
			  mission_name_snap, mission_body_snap, school_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sid, stid, cid, p.id, p.name, p.body, school); err != nil {
			return failDB(c, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	s.publish(school, Frame{
		Entity: "mission-pick.changed", SessionID: &sid, ChildID: cid, Stid: stid,
		Action: "updated",
	})
	// The pick changes what the public report shows (02 §2c public
	// report.changed covers "mission change in that child+session").
	s.publishPublic(Frame{
		Entity: "report.changed", SchoolID: school,
		SessionID: &sid, ChildID: cid, Stid: stid, Action: "updated",
	})
	return ok(c, s.missionPicks(ctx, sid, stid, cid))
}

// ---------------------------------------------------------------------------
// Narrative (AI generate + manual edit)
// ---------------------------------------------------------------------------

// handleNarrativeGet — GET P/narrative → {text, model, generated_at} or raw
// null when none.
func (s *Server) handleNarrativeGet(c fiber.Ctx) error {
	ctx, cancel, sid, stid, cid, _, _, okP := s.pScope(c)
	if !okP {
		return nil
	}
	defer cancel()
	return ok(c, s.readNarrative(ctx, sid, stid, cid))
}

// readNarrative returns the row or nil (raw null data).
func (s *Server) readNarrative(ctx context.Context, sid, stid, cid int64) *narrativeJSON {
	var (
		n     narrativeJSON
		model sql.NullString
		genAt sql.NullTime
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT text, model, generated_at FROM narratives
		 WHERE session_id = ? AND session_topic_id = ? AND child_id = ?`,
		sid, stid, cid).Scan(&n.Text, &model, &genAt)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		slog.Error("narrative read", "err", err, "session_id", sid, "child_id", cid)
		return nil
	}
	n.Model = fromNull(model)
	if genAt.Valid {
		ts := fmtTS(genAt.Time)
		n.GeneratedAt = &ts
	}
	return &n
}

// starLabel maps 1..4 to the BB/MB/BSH/BSB labels of prompts/system.md.
func starLabel(stars int) string {
	switch stars {
	case 1:
		return "BB"
	case 2:
		return "MB"
	case 3:
		return "BSH"
	default:
		return "BSB"
	}
}

// handleNarrativePost — POST P/narrative (idempotent): gather snapshot +
// stars + notes (empty → NOTHING_TO_NARRATE), call Gemini OUTSIDE any Tx
// (15 s, W1); error → 200 AI_FAILED (old kept, nothing written); success →
// single-row upsert (UNIQUE(session, topic, child)) + publish pair.
func (s *Server) handleNarrativePost(c fiber.Ctx) error {
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
	school, okTen := requireSchool(c)
	if !okTen {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)

	return s.idempotent(c, "POST "+c.Path(), func() error {
		ctx, cancel := dbCtx(c)
		defer cancel()

		code, err := s.requirePChild(ctx, sid, cid, u.ID, u.Role)
		if err != nil {
			return failDB(c, err)
		}
		if code != "" {
			return fail(c, code, msgForCode(code))
		}
		if code, err := s.photoScope(ctx, sid, stid, school); err != nil {
			return failDB(c, err)
		} else if code != "" {
			return fail(c, code, msgForCode(code))
		}

		// Context rows: child + session/topic snapshots (prompt vars).
		var childName string
		var childAge int
		var active bool
		if err := s.db.QueryRowContext(ctx,
			`SELECT name, age, is_active FROM children WHERE id = ? AND school_id = ?`,
			cid, school).Scan(&childName, &childAge, &active); err != nil {
			if err == sql.ErrNoRows {
				return fail(c, CodeNotFound, MsgNotFound)
			}
			return failDB(c, err)
		}
		if !active {
			return fail(c, CodeChildInactive, MsgChildInactive)
		}
		var sessionName, topicName string
		var sDate time.Time
		sTCond, sTArgs := schoolCond(school, "s.school_id")
		if err := s.db.QueryRowContext(ctx,
			`SELECT s.name, s.date, st.topic_name_snap
			 FROM sessions s JOIN session_topics st ON st.id = ? AND st.session_id = s.id
			 WHERE s.id = ?`+sTCond,
			append([]any{stid, sid}, sTArgs...)...).
			Scan(&sessionName, &sDate, &topicName); err != nil {
			if err == sql.ErrNoRows {
				return fail(c, CodeNotFound, MsgNotFound)
			}
			return failDB(c, err)
		}
		sessionDate := fmtDate(sDate)

		// Pre-read orders the gates only; the authoritative check re-runs
		// FOR UPDATE inside the write Tx below (B1a — no TOCTOU window).
		status, err := s.scopedStatus(ctx, sid, school)
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		if err != nil {
			return failDB(c, err)
		}
		if status != "OPENED" { // same guard as assessment (FL-12)
			return fail(c, CodeSessionLocked, MsgSessionLocked)
		}

		// Gather the assessment block (empty → NOTHING_TO_NARRATE).
		aTCond, aTArgs := schoolCond(school, "a.school_id")
		aRows, err := s.db.QueryContext(ctx,
			`SELECT sa.activity_name_snap, a.stars, a.note
			 FROM assessments a
			 JOIN session_activities sa ON sa.id = a.session_activity_id
			 WHERE a.session_id = ? AND a.child_id = ? AND sa.session_topic_id = ?`+
				aTCond+` ORDER BY sa.sort_order, sa.id`,
			append([]any{sid, cid, stid}, aTArgs...)...)
		if err != nil {
			return failDB(c, err)
		}
		var lines []string
		for aRows.Next() {
			var name string
			var stars int
			var note sql.NullString
			if err := aRows.Scan(&name, &stars, &note); err != nil {
				aRows.Close()
				return failDB(c, err)
			}
			line := "- " + name + ": " + strconv.Itoa(stars) + "/4 (" + starLabel(stars) + ")"
			if note.Valid && note.String != "" {
				line += " — " + note.String
			}
			lines = append(lines, line)
		}
		if err := aRows.Err(); err != nil {
			aRows.Close()
			return failDB(c, err)
		}
		aRows.Close()
		if len(lines) == 0 {
			return fail(c, CodeNothingToNarrate, MsgNothingToNarrate)
		}

		// One Gemini call, outside any Tx (15 s budget).
		text, aiErr := s.generateNarrative(childName, childAge, sessionName,
			topicName, sessionDate, strings.Join(lines, "\n"))
		if aiErr != nil {
			slog.Error("gemini narrative", "err", aiErr, "session_id", sid, "child_id", cid)
			return fail(c, CodeAIFailed, MsgAIFailed) // old narrative kept
		}

		// Single-row upsert inside ONE Tx, gated by the in-Tx FOR UPDATE
		// status re-read (B1a): a session finishing during the AI call rolls
		// this write back (the old narrative stays).
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
			`INSERT INTO narratives
			 (session_id, session_topic_id, child_id, school_id, text, model,
			  generated_at, generated_by)
			 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?)
			 ON DUPLICATE KEY UPDATE
			   text = VALUES(text), model = VALUES(model),
			   generated_at = CURRENT_TIMESTAMP, generated_by = VALUES(generated_by)`,
			sid, stid, cid, school, text, s.cfg.GeminiModel, u.ID); err != nil {
			return failDB(c, err)
		}
		if err := tx.Commit(); err != nil {
			return failDB(c, err)
		}
		narr := s.readNarrative(ctx, sid, stid, cid)
		if narr == nil {
			return failDB(c, sql.ErrNoRows)
		}
		s.publish(school, Frame{
			Entity: "narrative.changed", SessionID: &sid, ChildID: cid, Stid: stid,
			Action: "updated",
		})
		s.publishPublic(Frame{
			Entity: "report.changed", SchoolID: school,
			SessionID: &sid, ChildID: cid, Stid: stid, Action: "updated",
		})
		return ok(c, narr)
	})
}

// handleNarrativePatch — PATCH P/narrative {text}: manual edit ≤2000 chars,
// upsert (manual rows keep model/generated_at null), same publish pair.
func (s *Server) handleNarrativePatch(c fiber.Ctx) error {
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
	school, okTen := requireSchool(c)
	if !okTen {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)

	var req struct {
		Text string `json:"text"`
	}
	if !bindBody(c, &req) {
		return nil
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" || !validNote(req.Text) {
		return fail(c, CodeValidation, MsgValidation)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	code, err := s.requirePChild(ctx, sid, cid, u.ID, u.Role)
	if err != nil {
		return failDB(c, err)
	}
	if code != "" {
		return fail(c, code, msgForCode(code))
	}
	if code, err := s.photoScope(ctx, sid, stid, school); err != nil {
		return failDB(c, err)
	} else if code != "" {
		return fail(c, code, msgForCode(code))
	}
	// Pre-read orders the gates only; the authoritative check re-runs
	// FOR UPDATE inside the write Tx below (B1a — no TOCTOU window).
	status, err := s.scopedStatus(ctx, sid, school)
	if err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if err != nil {
		return failDB(c, err)
	}
	if status != "OPENED" { // same guard as generate (FL-12)
		return fail(c, CodeSessionLocked, MsgSessionLocked)
	}

	// Single-row upsert inside ONE Tx, gated by the in-Tx FOR UPDATE status
	// re-read (B1a): a session finishing after the pre-read rolls this back.
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
		`INSERT INTO narratives (session_id, session_topic_id, child_id, school_id, text, generated_by)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE text = VALUES(text), generated_by = VALUES(generated_by)`,
		sid, stid, cid, school, req.Text, u.ID); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}
	narr := s.readNarrative(ctx, sid, stid, cid)
	if narr == nil {
		return failDB(c, sql.ErrNoRows)
	}
	s.publish(school, Frame{
		Entity: "narrative.changed", SessionID: &sid, ChildID: cid, Stid: stid,
		Action: "updated",
	})
	s.publishPublic(Frame{
		Entity: "report.changed", SchoolID: school,
		SessionID: &sid, ChildID: cid, Stid: stid, Action: "updated",
	})
	return ok(c, narr)
}

// ---------------------------------------------------------------------------
// Public links (create / list / revoke / regenerate)
// ---------------------------------------------------------------------------

// reportURL / galleryURL are the public page links (05 §4).
func (s *Server) reportURL(token string) string {
	return s.cfg.PublicWebURL + "/view/report?token=" + token
}

func (s *Server) galleryURL(token string) string {
	return s.cfg.PublicWebURL + "/view/gallery?token=" + token
}

// ensureLinkPairTx loads-or-creates the ACTIVE REPORT and GALLERY links for
// one child+session+topic inside the caller's Tx (F-12: links are per
// child+session+topic; one POST returns the pair).
func ensureLinkPairTx(ctx context.Context, tx *sql.Tx, school, sid, stid, cid int64) (reportTok, galleryTok string, createdReport, createdGallery bool, err error) {
	kinds := []struct {
		kind    string
		created *bool
		token   *string
	}{
		{"REPORT", &createdReport, &reportTok},
		{"GALLERY", &createdGallery, &galleryTok},
	}
	for _, k := range kinds {
		err = tx.QueryRowContext(ctx,
			`SELECT token FROM public_links
			 WHERE session_id = ? AND session_topic_id = ? AND child_id = ?
			   AND kind = ? AND revoked_at IS NULL`,
			sid, stid, cid, k.kind).Scan(k.token)
		switch {
		case err == nil:
			continue
		case err != sql.ErrNoRows:
			return "", "", false, false, err
		}
		tok, genErr := randTokenHex()
		if genErr != nil {
			return "", "", false, false, genErr
		}
		if _, insErr := tx.ExecContext(ctx,
			`INSERT INTO public_links (school_id, session_id, session_topic_id, child_id, kind, token)
			 VALUES (?, ?, ?, ?, ?, ?)`, school, sid, stid, cid, k.kind, tok); insErr != nil {
			return "", "", false, false, insErr
		}
		*k.token, *k.created = tok, true
	}
	return reportTok, galleryTok, createdReport, createdGallery, nil
}

// publishLinkCreated emits report-link.changed for each newly created link.
func (s *Server) publishLinkCreated(school, sid, stid, cid int64, report, gallery bool) {
	if report {
		s.publish(school, Frame{
			Entity: "report-link.changed", SessionID: &sid, ChildID: cid, Stid: stid,
			Action: "created",
		})
	}
	if gallery {
		s.publish(school, Frame{
			Entity: "report-link.changed", SessionID: &sid, ChildID: cid, Stid: stid,
			Action: "created",
		})
	}
}

// handleLinkList — GET P/public-links: active links of this P scope.
func (s *Server) handleLinkList(c fiber.Ctx) error {
	ctx, cancel, sid, stid, cid, school, _, okP := s.pScope(c)
	if !okP {
		return nil
	}
	defer cancel()

	tcond, targs := schoolCond(school, "school_id")
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, token, created_at FROM public_links
		 WHERE session_id = ? AND session_topic_id = ? AND child_id = ? AND revoked_at IS NULL`+
			tcond+` ORDER BY kind, id`,
		append([]any{sid, stid, cid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	defer rows.Close()
	type linkItem struct {
		Kind      string `json:"kind"`
		URL       string `json:"url"`
		Token     string `json:"token"`
		CreatedAt string `json:"created_at"`
	}
	out := []linkItem{}
	for rows.Next() {
		var it linkItem
		var createdAt time.Time
		if err := rows.Scan(&it.Kind, &it.Token, &createdAt); err != nil {
			return failDB(c, err)
		}
		if it.Kind == "REPORT" {
			it.URL = s.reportURL(it.Token)
		} else {
			it.URL = s.galleryURL(it.Token)
		}
		it.CreatedAt = fmtTS(createdAt)
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	return ok(c, out)
}

type linkPairReq struct {
	Kind string `json:"kind"`
}

// handleLinkCreate — POST P/public-links {kind} (idempotent): ensures the
// ACTIVE REPORT+GALLERY pair exists (kind validates the request intent,
// 02 §2b) and returns both URLs.
func (s *Server) handleLinkCreate(c fiber.Ctx) error {
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
	school, okTen := requireSchool(c)
	if !okTen {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)

	var req linkPairReq
	if !bindBody(c, &req) {
		return nil
	}
	if req.Kind != "REPORT" && req.Kind != "GALLERY" {
		return fail(c, CodeValidation, MsgValidation)
	}

	return s.idempotent(c, "POST "+c.Path(), func() error {
		ctx, cancel := dbCtx(c)
		defer cancel()
		code, err := s.requirePChild(ctx, sid, cid, u.ID, u.Role)
		if err != nil {
			return failDB(c, err)
		}
		if code != "" {
			return fail(c, code, msgForCode(code))
		}
		if code, err := s.photoScope(ctx, sid, stid, school); err != nil {
			return failDB(c, err)
		} else if code != "" {
			return fail(c, code, msgForCode(code))
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return failDB(c, err)
		}
		defer tx.Rollback()
		rTok, gTok, cRep, cGal, err := ensureLinkPairTx(ctx, tx, school, sid, stid, cid)
		if err != nil {
			return failDB(c, err)
		}
		if err := tx.Commit(); err != nil {
			return failDB(c, err)
		}
		s.publishLinkCreated(school, sid, stid, cid, cRep, cGal)
		return ok(c, fiber.Map{"report_url": s.reportURL(rTok), "gallery_url": s.galleryURL(gTok)})
	})
}

// handleLinkRevoke — POST /api/public-links/:token/revoke: active → revoked
// in one Tx; unknown/revoked → TOKEN_INVALID (identical shape). FAS is
// scoped to own children through the same requirePChild gate.
func (s *Server) handleLinkRevoke(c fiber.Ctx) error {
	token, okTok := tokenParam(c)
	if !okTok {
		return nil
	}
	school, _ := c.Locals(kvSchool).(int64)
	u, _ := c.Locals(kvUser).(*userRow)

	ctx, cancel := dbCtx(c)
	defer cancel()
	var (
		linkID int64
		sid    int64
		stid   int64
		cid    int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, school_id, session_id, session_topic_id, child_id
		 FROM public_links WHERE token = ? AND revoked_at IS NULL`, token).
		Scan(&linkID, &school, &sid, &stid, &cid)
	if err == sql.ErrNoRows {
		return fail(c, CodeTokenInvalid, MsgTokenInvalid)
	}
	if err != nil {
		return failDB(c, err)
	}
	if u.Role == "FASILITATOR" {
		code, err := s.requirePChild(ctx, sid, cid, u.ID, u.Role)
		if err != nil {
			return failDB(c, err)
		}
		if code != "" {
			return fail(c, code, msgForCode(code))
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE public_links SET revoked_at = NOW() WHERE id = ? AND revoked_at IS NULL`, linkID)
	if err != nil {
		return failDB(c, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fail(c, CodeTokenInvalid, MsgTokenInvalid) // lost the revoke race
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	s.publish(school, Frame{
		Entity: "report-link.changed", EntityID: linkID,
		SessionID: &sid, ChildID: cid, Stid: stid, Action: "revoked",
	})
	s.publishPublic(Frame{
		Entity: "link.revoked", SchoolID: school,
		SessionID: &sid, ChildID: cid, Stid: stid, Action: "revoked",
		Token: token,
	})
	return ok(c, fiber.Map{"revoked": true})
}

// handleLinkRegenerate — POST /api/public-links/:token/regenerate: one Tx
// revokes the pair's active links and creates a fresh REPORT+GALLERY pair;
// unknown/revoked token → TOKEN_INVALID.
func (s *Server) handleLinkRegenerate(c fiber.Ctx) error {
	token, okTok := tokenParam(c)
	if !okTok {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)

	return s.idempotent(c, "POST "+c.Path(), func() error {
		ctx, cancel := dbCtx(c)
		defer cancel()
		var (
			school int64
			sid    int64
			stid   int64
			cid    int64
		)
		err := s.db.QueryRowContext(ctx,
			`SELECT school_id, session_id, session_topic_id, child_id
			 FROM public_links WHERE token = ? AND revoked_at IS NULL`, token).
			Scan(&school, &sid, &stid, &cid)
		if err == sql.ErrNoRows {
			return fail(c, CodeTokenInvalid, MsgTokenInvalid)
		}
		if err != nil {
			return failDB(c, err)
		}
		if u.Role == "FASILITATOR" {
			code, err := s.requirePChild(ctx, sid, cid, u.ID, u.Role)
			if err != nil {
				return failDB(c, err)
			}
			if code != "" {
				return fail(c, code, msgForCode(code))
			}
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return failDB(c, err)
		}
		defer tx.Rollback()
		// Collect the dying tokens first (frames after commit).
		oldRows, err := tx.QueryContext(ctx,
			`SELECT id, token FROM public_links
			 WHERE session_id = ? AND session_topic_id = ? AND child_id = ?
			   AND revoked_at IS NULL FOR UPDATE`, sid, stid, cid)
		if err != nil {
			return failDB(c, err)
		}
		var oldIDs []int64
		var oldTokens []string
		for oldRows.Next() {
			var id int64
			var tok string
			if err := oldRows.Scan(&id, &tok); err != nil {
				oldRows.Close()
				return failDB(c, err)
			}
			oldIDs = append(oldIDs, id)
			oldTokens = append(oldTokens, tok)
		}
		if err := oldRows.Err(); err != nil {
			oldRows.Close()
			return failDB(c, err)
		}
		oldRows.Close()
		if _, err := tx.ExecContext(ctx,
			`UPDATE public_links SET revoked_at = NOW()
			 WHERE session_id = ? AND session_topic_id = ? AND child_id = ? AND revoked_at IS NULL`,
			sid, stid, cid); err != nil {
			return failDB(c, err)
		}
		rTok, gTok, cRep, cGal, err := ensureLinkPairTx(ctx, tx, school, sid, stid, cid)
		if err != nil {
			return failDB(c, err)
		}
		if err := tx.Commit(); err != nil {
			return failDB(c, err)
		}

		for i := range oldIDs {
			s.publish(school, Frame{
				Entity: "report-link.changed", EntityID: oldIDs[i],
				SessionID: &sid, ChildID: cid, Stid: stid, Action: "revoked",
			})
			s.publishPublic(Frame{
				Entity: "link.revoked", SchoolID: school,
				SessionID: &sid, ChildID: cid, Stid: stid, Action: "revoked",
				Token: oldTokens[i],
			})
		}
		s.publishLinkCreated(school, sid, stid, cid, cRep, cGal)
		return ok(c, fiber.Map{"report_url": s.reportURL(rTok), "gallery_url": s.galleryURL(gTok)})
	})
}

// ---------------------------------------------------------------------------
// WA send (single + batch)
// ---------------------------------------------------------------------------

// readinessCache hands ONE request's session-varying readiness snapshot
// (002c) between gateReportItem calls: the first gate call loads it through
// loadReadinessSession, later batch items reuse it (≤10 items → 1 load).
// A failed load is never cached, so each item retries and maps its own
// error exactly as before (per-item CodeNotFound / DB_UNAVAILABLE).
type readinessCache struct {
	sess *readinessSession
}

// gateReportItem runs the per-item send gates and returns the failure code
// ("" = pass). On pass it also returns the child's registered WA numbers;
// on REPORT_INCOMPLETE it returns the missing list for the single-send
// envelope. DB trouble maps to DB_UNAVAILABLE (slog'd) so a batch never
// aborts mid-loop or double-sends on idem replay. rc may be nil (single
// send: one load per call); the batch loop passes one cache for the whole
// request so the session snapshot loads once (002c).
func (s *Server) gateReportItem(ctx context.Context, u *userRow, school, sid, stid, cid int64, rc *readinessCache) (code string, missing []MissingItem, motherWa string, fatherWa sql.NullString) {
	// Order mirrors B2's assessment gate: membership/ownership first (decision
	// 18 — no leak of session state to non-members), then status, linkage,
	// child, readiness.
	code, err := s.requirePChild(ctx, sid, cid, u.ID, u.Role)
	if err != nil {
		slog.Error("batch gate membership", "err", err, "session_id", sid, "child_id", cid)
		return CodeDBUnavailable, nil, "", fatherWa
	}
	if code != "" {
		return code, nil, "", fatherWa
	}
	status, err := s.scopedStatus(ctx, sid, school)
	if err == sql.ErrNoRows {
		return CodeNotFound, nil, "", fatherWa
	}
	if err != nil {
		slog.Error("batch gate status", "err", err, "session_id", sid)
		return CodeDBUnavailable, nil, "", fatherWa
	}
	if status == "DRAFT" { // send allowed on OPENED+FINISHED (F-12)
		return CodeSessionLocked, nil, "", fatherWa
	}
	if code, err = s.photoScope(ctx, sid, stid, school); err != nil {
		slog.Error("batch gate topic", "err", err, "session_id", sid)
		return CodeDBUnavailable, nil, "", fatherWa
	} else if code != "" {
		return code, nil, "", fatherWa
	}
	var active bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT mother_wa, father_wa, is_active FROM children
		 WHERE id = ? AND school_id = ?`, cid, school).
		Scan(&motherWa, &fatherWa, &active); err != nil {
		if err == sql.ErrNoRows {
			return CodeNotFound, nil, "", fatherWa
		}
		slog.Error("batch gate child", "err", err, "child_id", cid)
		return CodeDBUnavailable, nil, "", fatherWa
	}
	if !active {
		return CodeChildInactive, nil, "", fatherWa
	}
	// Readiness (002c): the session-varying snapshot (activity set; badges
	// never — this gate discards them) loads once per request through rc;
	// only this child's assessments scan runs per item.
	var sess *readinessSession
	if rc != nil {
		sess = rc.sess
	}
	if sess == nil {
		if sess, err = s.loadReadinessSession(ctx, school, sid, false); err == nil {
			if rc != nil {
				rc.sess = sess
			}
		} else if err == sql.ErrNoRows {
			return CodeNotFound, nil, "", fatherWa
		} else {
			slog.Error("batch gate readiness", "err", err, "session_id", sid, "child_id", cid)
			return CodeDBUnavailable, nil, "", fatherWa
		}
	}
	var complete bool
	complete, missing, _, _, err = s.readinessChild(ctx, school, sid, cid, stid, sess)
	if err == sql.ErrNoRows {
		return CodeNotFound, nil, "", fatherWa
	}
	if err != nil {
		slog.Error("batch gate readiness", "err", err, "session_id", sid, "child_id", cid)
		return CodeDBUnavailable, nil, "", fatherWa
	}
	if !complete {
		return CodeReportIncomplete, missing, "", fatherWa
	}
	return "", missing, motherWa, fatherWa
}

// resolveSendTarget picks the destination: default = mother's WA; a supplied
// `to` must equal one of the child's registered numbers (F-12) else ok=false.
func resolveSendTarget(motherWa string, fatherWa sql.NullString, to string) (string, bool) {
	if to == "" {
		return motherWa, motherWa != ""
	}
	if to == motherWa || (fatherWa.Valid && to == fatherWa.String) {
		return to, true
	}
	return "", false
}

// batchItemResult is one per-item send outcome (batch + the shared tail).
type batchItemResult struct {
	SessionTopicID int64  `json:"session_topic_id"`
	ChildID        int64  `json:"child_id"`
	Status         string `json:"status"`
	MessageID      string `json:"message_id,omitempty"`
	Code           string `json:"code,omitempty"`
}

// deliverReport ensures the active link pair, renders wa_report.md with both
// URLs and sends one message; writes the wa_outbox row (05 §2 payload_ref =
// report URL). Never fails the request: trouble becomes a FAILED result
// (per-item semantics, 06 §2). publishLinks=true emits report-link.changed
// per newly created link (single send); the batch defers that to one
// session-level frame after its loop (FL-13).
func (s *Server) deliverReport(ctx context.Context, school, sid, stid, cid int64, to string, idemKey string, publishLinks bool) batchItemResult {
	res := batchItemResult{SessionTopicID: stid, ChildID: cid, Status: "FAILED"}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		res.Code = CodeDBUnavailable
		return res
	}
	rTok, gTok, cRep, cGal, err := ensureLinkPairTx(ctx, tx, school, sid, stid, cid)
	if err != nil {
		tx.Rollback()
		slog.Error("send-report links", "err", err, "session_id", sid, "child_id", cid)
		res.Code = CodeDBUnavailable
		return res
	}
	if err := tx.Commit(); err != nil {
		slog.Error("send-report links commit", "err", err, "session_id", sid, "child_id", cid)
		res.Code = CodeDBUnavailable
		return res
	}
	if publishLinks {
		s.publishLinkCreated(school, sid, stid, cid, cRep, cGal)
	}

	reportURL := s.reportURL(rTok)
	text := renderVars(s.tplReport, map[string]string{
		"ReportURL":  reportURL,
		"GalleryURL": s.galleryURL(gTok),
	})
	chatID := strings.TrimPrefix(to, "+") + "@c.us"
	messageID, waErr := s.sendText(chatID, text)

	outStatus, outErr := "SENT", any(nil)
	if waErr != nil {
		outStatus, outErr = "FAILED", waErr.Error()
		res.Code = CodeWAFailed
	} else {
		res.Status, res.MessageID = "SENT", messageID
	}
	var messageArg any
	if messageID != "" {
		messageArg = messageID
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO wa_outbox
		 (school_id, kind, target_wa, payload_ref, status, message_id, error, idem_key)
		 VALUES (?, 'REPORT', ?, ?, ?, ?, ?, ?)`,
		school, to, reportURL, outStatus, messageArg, outErr, idemKey); err != nil {
		slog.Error("report outbox row", "err", err, "session_id", sid, "child_id", cid)
	}
	return res
}

// handleSendReport — POST P/send-report {to?} (5/min/IP, idempotent).
func (s *Server) handleSendReport(c fiber.Ctx) error {
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
	school, okTen := requireSchool(c)
	if !okTen {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)

	var req struct {
		To string `json:"to"`
	}
	if !bindBody(c, &req) {
		return nil
	}

	return s.idempotent(c, "POST "+c.Path(), func() error {
		ctx, cancel := dbCtx(c)
		defer cancel()

		code, missing, motherWa, fatherWa := s.gateReportItem(ctx, u, school, sid, stid, cid, nil)
		switch code {
		case "":
		case CodeReportIncomplete:
			// Machine-readable extra key (error.groups convention).
			return c.Status(200).JSON(fiber.Map{"error": fiber.Map{
				"code": CodeReportIncomplete, "message": MsgReportIncomplete,
				"missing": missing,
			}})
		case CodeDBUnavailable:
			// Typed idempotency signal (F-02-04): a TOP-level DB failure
			// releases the idem claim; the wrapper never sniffs bytes.
			c.Locals(kvDBFail, true)
			return fail(c, CodeDBUnavailable, MsgDBUnavailable)
		default:
			return fail(c, code, msgForCode(code))
		}
		to, okTo := resolveSendTarget(motherWa, fatherWa, req.To)
		if !okTo {
			return fail(c, CodeValidation, MsgValidation)
		}

		res := s.deliverReport(ctx, school, sid, stid, cid, to, c.Get("X-Idempotency-Key"), true)
		if res.Status != "SENT" {
			return ok(c, fiber.Map{"status": "FAILED", "code": res.Code})
		}
		return ok(c, fiber.Map{"status": "SENT", "message_id": res.MessageID})
	})
}

// batchReplayReadBack — the crash-replay read-back for batch-send (B1b /
// K6, 06 §3): a first attempt that dies mid-loop leaves its idem claim with a
// NULL response, so every same-key retry used to park on IN_PROGRESS forever.
// When the handler spots that exact state (claim exists, response NULL) it
// answers from what the claim already recorded in wa_outbox — one row per
// delivered item, joined to public_links for the batch's per-item shape —
// WITHOUT claiming, re-running gates, or re-sending anything. No recorded
// rows → fall through and the wrapper's IN_PROGRESS stands (client re-sends
// the same key, 06 §3). Recorded rows also keep arriving while the first
// attempt is still alive; the answer
// only ever converges to the wrapper's recorded envelope once fn finishes and
// stores it. fresh=true = not this handler's business; the wrapper proceeds
// (key validation, fresh claim, recorded replay, or standing IN_PROGRESS).
// fresh=false = the envelope is already written (recorded items or
// DB_UNAVAILABLE); the caller must not reach the wrapper.
func (s *Server) batchReplayReadBack(c fiber.Ctx, school, sid int64) (bool, error) {
	key := c.Get("X-Idempotency-Key")
	if key == "" || len(key) > 64 {
		return true, nil // key validation belongs to the wrapper
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	var response sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT response FROM idempotency_keys
		 WHERE school_id = ? AND route = ? AND idem_key = ?`,
		school, "POST "+c.Path(), key).Scan(&response)
	if err == sql.ErrNoRows {
		return true, nil // fresh key: the wrapper claims and executes
	}
	if err != nil {
		return false, failDB(c, err)
	}
	if response.Valid {
		return true, nil // recorded envelope: the wrapper replays it byte-for-byte
	}
	// NULL claim: read back what this key already put into wa_outbox.
	rows, err := s.db.QueryContext(ctx,
		`SELECT pl.session_topic_id, pl.child_id, o.status, o.message_id
		 FROM wa_outbox o
		 JOIN public_links pl ON pl.kind = 'REPORT'
		   AND pl.token = SUBSTRING_INDEX(o.payload_ref, 'token=', -1)
		 WHERE o.school_id = ? AND o.kind = 'REPORT' AND o.idem_key = ?
		   AND pl.session_id = ?
		 ORDER BY o.id`, school, key, sid)
	if err != nil {
		return false, failDB(c, err)
	}
	defer rows.Close()
	results := make([]batchItemResult, 0)
	for rows.Next() {
		var res batchItemResult
		var status string
		var msgID sql.NullString
		if err := rows.Scan(&res.SessionTopicID, &res.ChildID, &status, &msgID); err != nil {
			return false, failDB(c, err)
		}
		if status == "SENT" {
			res.Status, res.MessageID = "SENT", msgID.String
		} else { // an outbox FAILED row means OpenWA refused the send (WA_FAILED)
			res.Status, res.Code = "FAILED", CodeWAFailed
		}
		results = append(results, res)
	}
	if err := rows.Err(); err != nil {
		return false, failDB(c, err)
	}
	if len(results) == 0 {
		return true, nil // nothing recorded yet → the wrapper's IN_PROGRESS stands
	}
	return false, ok(c, fiber.Map{"items": results})
}

// handleBatchSend — POST /api/sessions/:sid/batch-send (2/min/IP,
// idempotent). NOT transactional across items (05 §7): sequential loop,
// per-item gates/status/outbox, 3–10 s jitter between send attempts; the
// idem wrapper runs the WHOLE loop once and replays the recorded envelope.
func (s *Server) handleBatchSend(c fiber.Ctx) error {
	sid, okS := idParam(c, "sid")
	if !okS {
		return nil
	}
	school, okTen := requireSchool(c)
	if !okTen {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)

	var req struct {
		Items []struct {
			SessionTopicID int64  `json:"session_topic_id"`
			ChildID        int64  `json:"child_id"`
			To             string `json:"to"`
		} `json:"items"`
	}
	if !bindBody(c, &req) {
		return nil
	}
	if len(req.Items) > 10 { // client chunks (F-12)
		return fail(c, CodeValidation, MsgValidation)
	}

	// Crash-replay read-back (B1b): a same-key retry whose claim is still
	// NULL (first attempt died mid-loop) answers from the recorded outbox
	// rows instead of parking on IN_PROGRESS forever. Anything else falls
	// through to the wrapper untouched (fresh claim, recorded replay, or the
	// IN_PROGRESS that stands while nothing is recorded yet).
	fresh, err := s.batchReplayReadBack(c, school, sid)
	if err != nil {
		return err
	}
	if !fresh {
		return nil // envelope already written (recorded items or DB failure)
	}

	return s.idempotent(c, "POST "+c.Path(), func() error {
		results := make([]batchItemResult, 0, len(req.Items))
		sentChildren := map[int64]bool{}
		sends := 0
		// ONE session snapshot for the whole request (002c): the first
		// item's gate loads it, every later item reuses it.
		rc := &readinessCache{}
		for _, it := range req.Items {
			res := batchItemResult{SessionTopicID: it.SessionTopicID, ChildID: it.ChildID}
			// Fresh statement budget per item: the loop outlives any single
			// dbCtx because of the inter-send jitter.
			ictx, icancel := dbCtx(c)
			code, _, motherWa, fatherWa := s.gateReportItem(ictx, u, school, sid, it.SessionTopicID, it.ChildID, rc)
			if code != "" {
				icancel()
				res.Status, res.Code = "FAILED", code
				results = append(results, res)
				continue
			}
			to, okTo := resolveSendTarget(motherWa, fatherWa, it.To)
			if !okTo {
				icancel()
				res.Status, res.Code = "FAILED", CodeValidation
				results = append(results, res)
				continue
			}
			if sends > 0 { // jitter BETWEEN sends only (never after the last)
				time.Sleep(time.Duration(3+rand.IntN(8)) * time.Second)
			}
			sends++
			out := s.deliverReport(ictx, school, sid, it.SessionTopicID, it.ChildID,
				to, c.Get("X-Idempotency-Key"), false)
			icancel()
			if out.Status == "SENT" {
				sentChildren[it.ChildID] = true
			}
			results = append(results, out)
		}

		if len(req.Items) > 0 {
			// ONE session-level link signal after the batch (FL-13 tail).
			s.publish(school, Frame{
				Entity: "report-link.changed", SessionID: &sid, Action: "batch",
			})
		}
		// Open parent pages refetch their JSON; gallery.changed goes WITHOUT
		// Stid (the gallery renders ALL topics of the child+session).
		for cid := range sentChildren {
			s.publishPublic(Frame{
				Entity: "report.changed", SchoolID: school,
				SessionID: &sid, ChildID: cid, Action: "updated",
			})
			s.publishPublic(Frame{
				Entity: "gallery.changed", SchoolID: school,
				SessionID: &sid, ChildID: cid, Action: "updated",
			})
		}
		return ok(c, fiber.Map{"items": results})
	})
}

// ---------------------------------------------------------------------------
// Session ops matrix
// ---------------------------------------------------------------------------

type reportMatrixRow struct {
	ChildID        int64   `json:"child_id"`
	ChildName      string  `json:"child_name"`
	SessionTopicID int64   `json:"session_topic_id"`
	TopicName      string  `json:"topic_name"`
	Complete       bool    `json:"complete"`
	Narrative      bool    `json:"narrative"`
	Missions       int     `json:"missions"`
	CandidatePhoto *bool   `json:"candidate_photo"` // null = no durable photos for the pair
	ReportURL      *string `json:"report_url"`
	GalleryURL     *string `json:"gallery_url"`
}

// handleSessionReports — GET /api/sessions/:sid/reports (NO meta): one row
// per member×session_topic, computed from a handful of set queries + Go map
// joins (no per-cell SQL). FAS sees only children of its own groups.
func (s *Server) handleSessionReports(c fiber.Ctx) error {
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
	if _, err := s.scopedStatus(ctx, sid, school); err == sql.ErrNoRows {
		return fail(c, CodeNotFound, MsgNotFound)
	} else if err != nil {
		return failDB(c, err)
	}
	tcond, targs := schoolCond(school, "school_id")

	// Session topics (ordered).
	type topicMeta struct {
		id   int64
		name string
		acts []int64 // session_activity ids of this topic
	}
	topics := []topicMeta{}
	tRows, err := s.db.QueryContext(ctx,
		`SELECT id, topic_name_snap FROM session_topics
		 WHERE session_id = ?`+tcond+` ORDER BY sort_order, id`,
		append([]any{sid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	for tRows.Next() {
		var tm topicMeta
		if err := tRows.Scan(&tm.id, &tm.name); err != nil {
			tRows.Close()
			return failDB(c, err)
		}
		topics = append(topics, tm)
	}
	if err := tRows.Err(); err != nil {
		tRows.Close()
		return failDB(c, err)
	}
	tRows.Close()

	// Activity ids per topic (completeness computed in Go).
	actsByTopic := map[int64][]int64{}
	actRows, err := s.db.QueryContext(ctx,
		`SELECT id, session_topic_id FROM session_activities
		 WHERE session_id = ?`+tcond, append([]any{sid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	for actRows.Next() {
		var said, stid int64
		if err := actRows.Scan(&said, &stid); err != nil {
			actRows.Close()
			return failDB(c, err)
		}
		actsByTopic[stid] = append(actsByTopic[stid], said)
	}
	if err := actRows.Err(); err != nil {
		actRows.Close()
		return failDB(c, err)
	}
	actRows.Close()

	// Members (FAS: own groups only).
	// Joined query — the school predicate must reference ONE table or MySQL
	// calls it ambiguous (session_members and children both carry school_id).
	smtcond, smtargs := schoolCond(school, "sm.school_id")
	memberQ := `SELECT sm.child_id, sm.group_id, c.name
		 FROM session_members sm JOIN children c ON c.id = sm.child_id
		 WHERE sm.session_id = ?` + smtcond
	memberArgs := append([]any{sid}, smtargs...)
	if ownGroups != nil {
		if len(ownGroups) == 0 {
			return ok(c, []reportMatrixRow{})
		}
		ids := make([]any, 0, len(ownGroups))
		for gid := range ownGroups {
			ids = append(ids, gid)
		}
		memberQ += ` AND sm.group_id IN (` + placeholders(len(ids)) + `)`
		memberArgs = append(memberArgs, ids...)
	}
	memberQ += ` ORDER BY sm.child_id`
	mRows, err := s.db.QueryContext(ctx, memberQ, memberArgs...)
	if err != nil {
		return failDB(c, err)
	}
	type memberMeta struct {
		id   int64
		name string
	}
	members := []memberMeta{}
	for mRows.Next() {
		var m memberMeta
		var gid int64
		if err := mRows.Scan(&m.id, &gid, &m.name); err != nil {
			mRows.Close()
			return failDB(c, err)
		}
		members = append(members, m)
	}
	if err := mRows.Err(); err != nil {
		mRows.Close()
		return failDB(c, err)
	}
	mRows.Close()

	// Starred set (child, said).
	type ct struct{ child, said int64 }
	assessed := map[ct]bool{}
	aRows, err := s.db.QueryContext(ctx,
		`SELECT child_id, session_activity_id FROM assessments
		 WHERE session_id = ?`+tcond, append([]any{sid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	for aRows.Next() {
		var k ct
		if err := aRows.Scan(&k.child, &k.said); err != nil {
			aRows.Close()
			return failDB(c, err)
		}
		assessed[k] = true
	}
	if err := aRows.Err(); err != nil {
		aRows.Close()
		return failDB(c, err)
	}
	aRows.Close()

	// Narrative presence.
	type cs struct{ child, stid int64 }
	narratives := map[cs]bool{}
	nRows, err := s.db.QueryContext(ctx,
		`SELECT child_id, session_topic_id FROM narratives
		 WHERE session_id = ?`+tcond, append([]any{sid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	for nRows.Next() {
		var k cs
		if err := nRows.Scan(&k.child, &k.stid); err != nil {
			nRows.Close()
			return failDB(c, err)
		}
		narratives[k] = true
	}
	if err := nRows.Err(); err != nil {
		nRows.Close()
		return failDB(c, err)
	}
	nRows.Close()

	// Mission pick counts.
	missionCounts := map[cs]int{}
	mcRows, err := s.db.QueryContext(ctx,
		`SELECT child_id, session_topic_id, COUNT(*) FROM report_missions
		 WHERE session_id = ?`+tcond+` GROUP BY child_id, session_topic_id`,
		append([]any{sid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	for mcRows.Next() {
		var k cs
		var n int
		if err := mcRows.Scan(&k.child, &k.stid, &n); err != nil {
			mcRows.Close()
			return failDB(c, err)
		}
		missionCounts[k] = n
	}
	if err := mcRows.Err(); err != nil {
		mcRows.Close()
		return failDB(c, err)
	}
	mcRows.Close()

	// Candidate slot + durable-photo presence per (child, topic): two
	// unambiguous bool-column queries instead of one MAX() aggregate.
	photoAny := map[cs]bool{}
	paRows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT child_id, session_topic_id FROM photos
		 WHERE session_id = ? AND file_ready = TRUE`+tcond,
		append([]any{sid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	for paRows.Next() {
		var k cs
		if err := paRows.Scan(&k.child, &k.stid); err != nil {
			paRows.Close()
			return failDB(c, err)
		}
		photoAny[k] = true
	}
	if err := paRows.Err(); err != nil {
		paRows.Close()
		return failDB(c, err)
	}
	paRows.Close()
	photoCand := map[cs]bool{}
	pcRows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT child_id, session_topic_id FROM photos
		 WHERE session_id = ? AND file_ready = TRUE AND is_candidate = TRUE`+tcond,
		append([]any{sid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	for pcRows.Next() {
		var k cs
		if err := pcRows.Scan(&k.child, &k.stid); err != nil {
			pcRows.Close()
			return failDB(c, err)
		}
		photoCand[k] = true
	}
	if err := pcRows.Err(); err != nil {
		pcRows.Close()
		return failDB(c, err)
	}
	pcRows.Close()

	// Active link URLs per (child, topic).
	type linkPair struct{ report, gallery *string }
	links := map[cs]*linkPair{}
	lRows, err := s.db.QueryContext(ctx,
		`SELECT child_id, session_topic_id, kind, token FROM public_links
		 WHERE session_id = ? AND revoked_at IS NULL`+tcond,
		append([]any{sid}, targs...)...)
	if err != nil {
		return failDB(c, err)
	}
	for lRows.Next() {
		var k cs
		var kind, token string
		if err := lRows.Scan(&k.child, &k.stid, &kind, &token); err != nil {
			lRows.Close()
			return failDB(c, err)
		}
		pair := links[k]
		if pair == nil {
			pair = &linkPair{}
			links[k] = pair
		}
		if kind == "REPORT" {
			url := s.reportURL(token)
			pair.report = &url
		} else {
			url := s.galleryURL(token)
			pair.gallery = &url
		}
	}
	if err := lRows.Err(); err != nil {
		lRows.Close()
		return failDB(c, err)
	}
	lRows.Close()

	// Join in Go: one row per member×topic. `complete` comes from
	// topicComplete — the ONE shared completeness rule (002c): the same
	// computation readinessChild drives for reportReadiness and the send
	// gates, fed here from the batch set queries (no N+1).
	out := make([]reportMatrixRow, 0, len(members)*len(topics))
	for _, m := range members {
		cid := m.id
		present := func(said int64) bool {
			return assessed[ct{child: cid, said: said}]
		}
		for _, tm := range topics {
			k := cs{child: cid, stid: tm.id}
			row := reportMatrixRow{
				ChildID: cid, ChildName: m.name,
				SessionTopicID: tm.id, TopicName: tm.name,
				Narrative: narratives[k],
				Missions:  missionCounts[k],
				Complete:  topicComplete(actsByTopic[tm.id], present),
			}
			if photoAny[k] {
				cand := photoCand[k]
				row.CandidatePhoto = &cand
			}
			if pair, ok := links[k]; ok {
				row.ReportURL, row.GalleryURL = pair.report, pair.gallery
			}
			out = append(out, row)
		}
	}
	return ok(c, out)
}

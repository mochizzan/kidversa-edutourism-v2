package internal

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Hub seam helpers (stream_test pattern): publish is synchronous, so a
// subscribed SA/ADMIN scope holds every frame the handler emitted by the
// time the request returns — deterministic, no sleeps.
// ---------------------------------------------------------------------------

func covSubscribe(t *testing.T, s *Server, scope ScopeSnapshot) *subscription {
	t.Helper()
	sub := s.subscribe(scope, "9.9.9.9")
	t.Cleanup(func() { s.unsubscribe(sub) })
	return sub
}

func covNextFrame(t *testing.T, sub *subscription, why string) Frame {
	t.Helper()
	select {
	case f := <-sub.ch:
		return f
	default:
		t.Fatalf("expected a published frame: %s", why)
		return Frame{}
	}
}

func covNoFrame(t *testing.T, sub *subscription, why string) {
	t.Helper()
	require.Empty(t, sub.ch, "expected no publish: %s", why)
}

// assessmentFASHeaders wires a FASILITATOR caller (school 1) for the P gates.
func assessmentFASHeaders(t *testing.T, s *Server, mock sqlmock.Sqlmock) map[string]string {
	t.Helper()
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
	h := bearer(mintAccess(t, s, fasUserRow()))
	h["X-School-ID"] = "1"
	return h
}

// TestAssessmentPutHappyPathUpsertsAndPublishes: every gate passes, the LWW
// upsert runs with assessor_id = caller, and BOTH signals fire — the card
// refetch (assessment.changed) and the derived completeness banner
// (readiness.changed with complete=true).
func TestAssessmentPutHappyPathUpsertsAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := assessmentFASHeaders(t, s, mock)

	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	// Membership gate (decision 18) → FAS owner probe → status → linkage →
	// child → owner depth (group owner active).
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	caxExpectFASOwner(mock)
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT sa.activity_name_snap FROM session_activities sa")).
		WithArgs(int64(11), int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap"}).AddRow("Aktivitas A"))
	mock.ExpectQuery(q("SELECT name, is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "is_active"}).AddRow("Ayu", true))
	mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members sm")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(3))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(1))
	// One Tx: the in-Tx FOR UPDATE status re-read (B1a) gates the atomic
	// upsert, then the updated_at re-read, then commit.
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "OPENED")
	mock.ExpectExec(q("INSERT INTO assessments")).
		WithArgs(int64(2), int64(11), int64(30), int64(1), 3, "Catatan bagus",
			"Aktivitas A", "Ayu", int64(3)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(q("SELECT updated_at FROM assessments")).
		WithArgs(int64(11), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(fixedTS))
	mock.ExpectCommit()
	// Readiness snapshot after the save feeds readiness.changed: badges are
	// discarded on this path (002c), so only the session activity set plus
	// this child's assessments scan run — no program/topic/badge lookups.
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id", "activity_name_snap"}).
			AddRow(5, 11, "Aktivitas A").
			AddRow(5, 12, "Aktivitas B"))
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}).AddRow(11).AddRow(12))

	res := jsonReq(t, app, "PUT", "/api/sessions/2/topics/5/children/30/activities/11",
		map[string]any{"stars": 3, "note": "Catatan bagus"}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(11), data["said"])
	require.Equal(t, float64(3), data["stars"])
	require.Equal(t, "Catatan bagus", data["note"])
	require.Equal(t, float64(3), data["assessor_id"])
	require.NotEmpty(t, data["updated_at"])

	changed := covNextFrame(t, sub, "assessment.changed after upsert")
	require.Equal(t, "assessment.changed", changed.Entity)
	require.Equal(t, int64(11), changed.EntityID)
	require.NotNil(t, changed.SessionID)
	require.Equal(t, int64(2), *changed.SessionID)
	require.Equal(t, int64(30), changed.ChildID)
	require.Equal(t, int64(5), changed.Stid)
	require.Equal(t, "updated", changed.Action)

	ready := covNextFrame(t, sub, "readiness.changed after save")
	require.Equal(t, "readiness.changed", ready.Entity)
	require.NotNil(t, ready.Complete, "readiness.changed carries the full-value complete flag")
	require.True(t, *ready.Complete, "both activities starred → complete")
	require.Equal(t, int64(5), ready.Stid)
	covNoFrame(t, sub, "exactly two signals per assessment save")
}

// TestAssessmentPutGateMatrix: the shared P gates in specification order —
// cheap validations before any SQL, membership/ownership/status after.
func TestAssessmentPutGateMatrix(t *testing.T) {
	const putPath = "/api/sessions/2/topics/5/children/30/activities/11"
	expectMembership := func(mock sqlmock.Sqlmock, exists int) {
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(exists))
	}
	expectStatus := func(mock sqlmock.Sqlmock, status string) {
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows(status))
	}
	expectLinkage := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("SELECT sa.activity_name_snap FROM session_activities sa")).
			WithArgs(int64(11), int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap"}).AddRow("Aktivitas A"))
	}
	expectChild := func(mock sqlmock.Sqlmock, active bool) {
		mock.ExpectQuery(q("SELECT name, is_active FROM children WHERE id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "is_active"}).AddRow("Ayu", active))
	}

	t.Run("stars above 4 is validation before any db work", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		res := jsonReq(t, app, "PUT", putPath, map[string]any{"stars": 5}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("stars zero is validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		res := jsonReq(t, app, "PUT", putPath, map[string]any{"stars": 0}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("note over 2000 chars is validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		res := jsonReq(t, app, "PUT", putPath,
			map[string]any{"stars": 3, "note": strings.Repeat("n", 2001)}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("non-member child is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		expectMembership(mock, 0)
		res := jsonReq(t, app, "PUT", putPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("draft session is session locked", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		expectMembership(mock, 1)
		caxExpectFASOwner(mock)
		expectStatus(mock, "DRAFT")
		res := jsonReq(t, app, "PUT", putPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeSessionLocked)
	})

	t.Run("finished session is session locked", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		expectMembership(mock, 1)
		caxExpectFASOwner(mock)
		expectStatus(mock, "FINISHED")
		res := jsonReq(t, app, "PUT", putPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeSessionLocked)
	})

	t.Run("inactive child is child inactive", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		expectMembership(mock, 1)
		caxExpectFASOwner(mock)
		expectStatus(mock, "OPENED")
		expectLinkage(mock)
		expectChild(mock, false)
		res := jsonReq(t, app, "PUT", putPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeChildInactive)
	})

	t.Run("ownerless group blocks for everyone", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		expectMembership(mock, 1)
		caxExpectFASOwner(mock)
		expectStatus(mock, "OPENED")
		expectLinkage(mock)
		expectChild(mock, true)
		mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members sm")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(nil))
		res := jsonReq(t, app, "PUT", putPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeGroupNoFacilitator)
	})

	t.Run("facilitator touching a foreign group is not owner", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		expectMembership(mock, 1)
		mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members sm")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(99))
		res := jsonReq(t, app, "PUT", putPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeNotOwner)
	})
}

// TestAssessmentsListReturnsOnlyExistingRows: GET P/assessments carries only
// stored cells (unanswered activities stay absent), nulls stay null.
func TestAssessmentsListReturnsOnlyExistingRows(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("a.assessor_id, a.updated_at")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"said", "stars", "note", "assessor_id", "updated_at"}).
			AddRow(11, 3, "Bagus", int64(2), fixedTS).
			AddRow(12, 4, nil, nil, fixedTS))

	res := doReq(t, app, "GET", "/api/sessions/2/topics/5/children/30/assessments", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 2)
	first := data[0].(map[string]any)
	require.Equal(t, float64(11), first["said"])
	require.Equal(t, float64(3), first["stars"])
	require.Equal(t, "Bagus", first["note"])
	require.Equal(t, float64(2), first["assessor_id"])
	second := data[1].(map[string]any)
	require.Equal(t, float64(12), second["said"])
	require.Nil(t, second["note"], "a NULL note must stay JSON null")
	require.Nil(t, second["assessor_id"])
	require.Equal(t, "2026-01-02T03:04:05Z", second["updated_at"])
}

// TestSessionProgressScopesFASMembersToOwnGroups: the matrix filters members
// through the facilitator's own group ids (single predicate, decision 24)
// and computes starred/total/complete per topic.
func TestSessionProgressScopesFASMembersToOwnGroups(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := assessmentFASHeaders(t, s, mock)

	mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).AddRow(10, 2))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT id, topic_name_snap FROM session_topics")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_name_snap"}).AddRow(5, "Topik A"))
	mock.ExpectQuery(q("SELECT session_topic_id, COUNT(*) FROM session_activities")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "n"}).AddRow(5, 2))
	// The member query must carry the own-group scoping argument.
	mock.ExpectQuery(q("SELECT sm.child_id, sm.group_id, c.name")).
		WithArgs(int64(2), int64(1), int64(10)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "group_id", "name"}).
			AddRow(30, 10, "Ayu"))
	mock.ExpectQuery(q("SELECT a.child_id, sa.session_topic_id, COUNT(*)")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "stid", "n"}).AddRow(30, 5, 1))

	res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	children := data["children"].([]any)
	require.Len(t, children, 1, "only the FAS's own-group children appear")
	child := children[0].(map[string]any)
	require.Equal(t, float64(30), child["child_id"])
	require.Equal(t, float64(10), child["gid"])
	topics := child["topics"].([]any)
	require.Len(t, topics, 1)
	tc := topics[0].(map[string]any)
	require.Equal(t, float64(5), tc["stid"])
	require.Equal(t, "Topik A", tc["topic"])
	require.Equal(t, float64(1), tc["starred"])
	require.Equal(t, float64(2), tc["total"])
	require.Equal(t, false, tc["complete"], "1 of 2 starred → incomplete")
}

// TestSessionProgressFASForeignSessionIsNotFound: a facilitator outside the
// session never learns it exists (errNotVisible → NOT_FOUND).
func TestSessionProgressFASForeignSessionIsNotFound(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := assessmentFASHeaders(t, s, mock)

	mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}))

	res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
	requireErrEnvelope(t, res, CodeNotFound)
}

// TestConsentDitolakWipeLeavesAssessmentNotes: the DITOLAK wipe touches ONLY
// the photos table (its own Tx) — the expectation set below contains no
// assessments statement at all, so ANY write to that table during the wipe
// fails sqlmock here. The same child+session's assessment notes then still
// list unchanged (notes survive consent denial, audit K15).
func TestConsentDitolakWipeLeavesAssessmentNotes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// 1. Public DITOLAK respond: resolve → idem claim → first-wins UPDATE →
	// photo wipe (own Tx) → recorded envelope. No assessments query anywhere
	// in this sequence — the wipe cannot have touched the notes.
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-note").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
		WithArgs("tok-note").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"child_id"}).AddRow(int64(1), int64(2), int64(30)))
	mock.ExpectQuery(q("SELECT id FROM consent_links WHERE session_id = ? AND child_id = ?")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), "POST /public/consent/respond", "consent-note").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), "POST /public/consent/respond", "consent-note").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("SET status = ?, respondent_name = ?, responded_at = NOW()")).
		WithArgs("DITOLAK", "Ibu Ayu", int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// The wipe's own Tx: select paths → delete rows → commit.
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT path FROM photos WHERE session_id = ?")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("1/2/5/30/a.jpg"))
	mock.ExpectExec(q("DELETE FROM photos WHERE session_id = ?")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST /public/consent/respond", "consent-note").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/public/consent/respond",
		map[string]any{"token": "tok-note", "decision": "DITOLAK",
			"respondent_name": "Ibu Ayu"},
		map[string]string{"X-Idempotency-Key": "consent-note"})
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "DITOLAK", data["decision"])

	// 2. The notes of the SAME child+session still list after the wipe.
	h := sessionAdminHeaders(t, s, mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("a.assessor_id, a.updated_at")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"said", "stars", "note",
			"assessor_id", "updated_at"}).
			AddRow(11, 3, "Catatan bagus", int64(2), fixedTS))

	res = doReq(t, app, "GET", "/api/sessions/2/topics/5/children/30/assessments", "", h)
	items := requireDataEnvelope(t, res).([]any)
	require.Len(t, items, 1, "the assessment note survives the photo wipe")
	item := items[0].(map[string]any)
	require.Equal(t, "Catatan bagus", item["note"])
	require.Equal(t, float64(2), item["assessor_id"])
	require.Equal(t, "2026-01-02T03:04:05Z", item["updated_at"])
}

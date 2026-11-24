package internal

import (
	"database/sql"
	"errors"
	"net/http"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

const (
	missionPutPath = "/api/sessions/2/topics/5/children/30/missions"
	sendReportPath = "/api/sessions/2/topics/5/children/30/send-report"
)

// TestMissionPutRejectsMoreThanFour: the ≤4 replace-set cap (E19) trips
// before any database work.
func TestMissionPutRejectsMoreThanFour(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	res := jsonReq(t, app, "PUT", missionPutPath,
		map[string]any{"mission_ids": []int64{1, 2, 3, 4, 5}}, h)
	requireErrEnvelope(t, res, CodeMissionLimit)
}

// TestMissionPutReplaceSet: exactly 4 is fine — the pick set replaces the old
// rows in one Tx and the response re-reads the snapped picks.
func TestMissionPutReplaceSet(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT topic_id FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id"}).AddRow(9))
	mock.ExpectQuery(q("SELECT is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	// Each pick must exist in-school and link the topic's master topic.
	mock.ExpectQuery(q("SELECT name FROM missions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(11), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Misi 1"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics WHERE mission_id = ? AND topic_id = ?)")).
		WithArgs(int64(11), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"linked"}).AddRow(1))
	mock.ExpectQuery(q("SELECT name FROM missions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(12), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Misi 2"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics WHERE mission_id = ? AND topic_id = ?)")).
		WithArgs(int64(12), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"linked"}).AddRow(1))
	// ONE Tx replace-set, gated by the in-Tx FOR UPDATE status re-read (B1a).
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "OPENED")
	mock.ExpectExec(q("DELETE FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(q("INSERT INTO report_missions")).
		WithArgs(int64(2), int64(5), int64(30), int64(11), "Misi 1", int64(1)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(q("INSERT INTO report_missions")).
		WithArgs(int64(2), int64(5), int64(30), int64(12), "Misi 2", int64(1)).
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(q("FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"mission_id", "mission_name_snap"}).
			AddRow(11, "Misi 1").
			AddRow(12, "Misi 2"))

	res := jsonReq(t, app, "PUT", missionPutPath,
		map[string]any{"mission_ids": []int64{11, 12}}, h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 2)
	first := data[0].(map[string]any)
	require.Equal(t, float64(11), first["mission_id"])
	require.Equal(t, "Misi 1", first["name"])
}

// TestSendReportReadinessGateReturnsIncompleteWithMissing: an unstarred
// activity blocks the send with REPORT_INCOMPLETE + the machine-readable
// missing list (E17).
func TestSendReportReadinessGateReturnsIncompleteWithMissing(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	h["X-Idempotency-Key"] = "send-report-1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT mother_wa, father_wa, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "father_wa", "is_active"}).
			AddRow("+628111111111", nil, true))
	// Readiness gate (002c): badges are discarded here, so the gate loads
	// only the session activity set + this child's assessments scan —
	// one unstarred activity → incomplete with the missing list.
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id", "activity_name_snap"}).
			AddRow(5, 11, "Aktivitas A").
			AddRow(5, 12, "Aktivitas B"))
	mock.ExpectQuery(q("FROM assessments")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}).AddRow(11))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST "+sendReportPath, "send-report-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h)
	errObj := requireErrEnvelope(t, res, CodeReportIncomplete)
	missing, ok := errObj["missing"].([]any)
	require.True(t, ok, "REPORT_INCOMPLETE must carry the missing list: %s", res.body)
	require.Len(t, missing, 1)
	item := missing[0].(map[string]any)
	require.Equal(t, float64(12), item["said"])
	require.Equal(t, "Aktivitas B", item["name"])
}

// TestSendReportOnDraftSessionIsLocked: DRAFT sessions never send (F-12).
func TestSendReportOnDraftSessionIsLocked(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	h["X-Idempotency-Key"] = "send-report-2"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST "+sendReportPath, "send-report-2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeSessionLocked)
}

// ---------------------------------------------------------------------------
// Narrative generation: AI failure propagation (06 §2: Gemini problems are
// runtime failures → 200 AI_FAILED, the previous narrative is kept)
// ---------------------------------------------------------------------------

// blockedTransport fails every outbound request deterministically — the test
// seam for "Gemini unreachable" without touching the network.
type blockedTransport func(*http.Request) (*http.Response, error)

func (f blockedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const narrativePostPath = "/api/sessions/2/topics/5/children/30/narrative"

// expectNarrativeGates wires the narrative gates up to the assessment block.
func expectNarrativeGates(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT name, age, is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "age", "is_active"}).
			AddRow("Ayu", 8, true))
	mock.ExpectQuery(q("FROM sessions s JOIN session_topics st ON st.id = ? AND st.session_id = s.id")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "date", "topic_name_snap"}).
			AddRow("Sesi A", fixedTS, "Topik A"))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
}

func TestNarrativeGenerateAIFailedStaysRecoverable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	// Gemini is unreachable: every outbound call errors instantly.
	s.http = &http.Client{Transport: blockedTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("gemini unreachable (blocked transport)")
	})}
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	h["X-Idempotency-Key"] = "narr-1"

	expectNarrativeGates(mock)
	mock.ExpectQuery(q("FROM assessments a")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap", "stars", "note"}).
			AddRow("Aktivitas A", 3, "Catatan bagus"))
	// The AI_FAILED envelope is recorded like any non-DB failure.
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST "+narrativePostPath, "narr-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeAIFailed)
}

func TestNarrativeNothingToNarrate(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	h["X-Idempotency-Key"] = "narr-2"

	expectNarrativeGates(mock)
	mock.ExpectQuery(q("FROM assessments a")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap", "stars", "note"}))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST "+narrativePostPath, "narr-2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeNothingToNarrate)
}

// ---------------------------------------------------------------------------
// Readiness / list GETs (contracts shapes)
// ---------------------------------------------------------------------------

// TestReportReadinessEndpointShape: GET P/readiness answers the pinned
// {complete, missing:[{said,name}], badges:{topic,program}} — a fully
// starred topic with badges on both levels reports complete with no gap.
func TestReportReadinessEndpointShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"program_id"}).AddRow(9))
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id", "activity_name_snap"}).
			AddRow(5, 11, "Aktivitas A").
			AddRow(5, 12, "Aktivitas B"))
	// Topic badge pair defined…
	mock.ExpectQuery(q("FROM session_topics WHERE session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "badge_name_snap", "badge_image_snap"}).
			AddRow(5, "Medal", "badges/topik.png"))
	// …AND the program badge defined (LIVE) → both badge booleans TRUE —
	// then the ONE child scan: 11+12 starred → complete, no gap (002c).
	mock.ExpectQuery(q("SELECT (badge_name IS NOT NULL AND badge_image_path IS NOT NULL)")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge"}).AddRow(1))
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}).
			AddRow(11).AddRow(12))

	res := doReq(t, app, "GET", "/api/sessions/2/topics/5/children/30/readiness", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["complete"])
	require.Equal(t, []any{}, data["missing"], "no gaps → empty list, never null")
	badges := data["badges"].(map[string]any)
	require.Equal(t, true, badges["topic"])
	require.Equal(t, true, badges["program"])
}

// TestMissionListReturnsSnappedPicks: GET P/missions carries the snapped
// replace-set ordered by mission id.
func TestMissionListReturnsSnappedPicks(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"mission_id", "mission_name_snap"}).
			AddRow(11, "Misi Sehat").
			AddRow(12, "Misi Belajar"))

	res := doReq(t, app, "GET", "/api/sessions/2/topics/5/children/30/missions", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 2)
	first := data[0].(map[string]any)
	require.Equal(t, float64(11), first["mission_id"])
	require.Equal(t, "Misi Sehat", first["name"])
	require.NotContains(t, first, "body", "a pick is its snapped name only")
}

// TestNarrativeGetAbsentThenPresent: no narrative → raw null data; an
// existing row renders {text, model, generated_at} with nulls preserved.
func TestNarrativeGetAbsentThenPresent(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	expectNarrativePrefix := func() {
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
			WithArgs(int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	}
	narrativePath := "/api/sessions/2/topics/5/children/30/narrative"

	expectNarrativePrefix()
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(sql.ErrNoRows)
	res := doReq(t, app, "GET", narrativePath, "", h)
	require.Nil(t, requireDataEnvelope(t, res), "no narrative → raw JSON null data")

	expectNarrativePrefix()
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"text", "model", "generated_at"}).
			AddRow("Cerita anak", "gemini", fixedTS))
	res = doReq(t, app, "GET", narrativePath, "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "Cerita anak", data["text"])
	require.Equal(t, "gemini", data["model"])
	require.Equal(t, "2026-01-02T03:04:05Z", data["generated_at"])
}

// ---------------------------------------------------------------------------
// Public links: list / pair create / revoke
// ---------------------------------------------------------------------------

// TestPublicLinkListRendersUrls: GET P/public-links maps kind → the pinned
// public page URL and stamps created_at as RFC3339.
func TestPublicLinkListRendersUrls(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("WHERE session_id = ? AND session_topic_id = ? AND child_id = ? AND revoked_at IS NULL")).
		WithArgs(int64(2), int64(5), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"kind", "token", "created_at"}).
			AddRow("REPORT", "tokR", fixedTS).
			AddRow("GALLERY", "tokG", fixedTS))

	res := doReq(t, app, "GET", "/api/sessions/2/topics/5/children/30/public-links", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 2)
	report := data[0].(map[string]any)
	require.Equal(t, "REPORT", report["kind"])
	require.Equal(t, "tokR", report["token"])
	require.Equal(t, "http://localhost:8002/view/report?token=tokR", report["url"])
	require.Equal(t, "2026-01-02T03:04:05Z", report["created_at"])
	gallery := data[1].(map[string]any)
	require.Equal(t, "GALLERY", gallery["kind"])
	require.Equal(t, "http://localhost:8002/view/gallery?token=tokG", gallery["url"])
}

// TestPublicLinkCreateEnsuresPair: one idempotent POST creates the missing
// REPORT+GALLERY pair in a single Tx (fresh 64-hex tokens) and publishes one
// report-link.changed per CREATED link.
func TestPublicLinkCreateEnsuresPair(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	h["X-Idempotency-Key"] = "link-create-1"
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	const linkRoute = "POST /api/sessions/2/topics/5/children/30/public-links"
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), linkRoute, "link-create-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), linkRoute, "link-create-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectBegin()
	// REPORT missing → INSERT; GALLERY missing → INSERT (one Tx).
	mock.ExpectQuery(q("SELECT token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30), "REPORT").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(q("INSERT INTO public_links")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(q("SELECT token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30), "GALLERY").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(q("INSERT INTO public_links")).
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), linkRoute, "link-create-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/api/sessions/2/topics/5/children/30/public-links",
		map[string]any{"kind": "REPORT"}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Regexp(t, `^http://localhost:8002/view/report\?token=[0-9a-f]{64}$`,
		data["report_url"], "fresh 64-hex report token")
	require.Regexp(t, `^http://localhost:8002/view/gallery\?token=[0-9a-f]{64}$`,
		data["gallery_url"], "fresh 64-hex gallery token")

	require.Equal(t, "report-link.changed", covNextFrame(t, sub, "report link created").Entity)
	require.Equal(t, "report-link.changed", covNextFrame(t, sub, "gallery link created").Entity)
	covNoFrame(t, sub, "one frame per created link")
}

// TestPublicLinkRevokeKillsTokenAndPublishes: revoke flips revoked_at in one
// Tx, then publishes the auth-side report-link.changed AND the token-precise
// terminal link.revoked for exactly that token.
func TestPublicLinkRevokeKillsTokenAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokR").
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_id", "session_id",
			"session_topic_id", "child_id"}).
			AddRow(9, int64(1), int64(2), int64(5), int64(30)))
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE public_links SET revoked_at = NOW() WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := jsonReq(t, app, "POST", "/api/public-links/tokR/revoke", nil, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["revoked"])

	authFrame := covNextFrame(t, sub, "report-link.changed on revoke")
	require.Equal(t, "report-link.changed", authFrame.Entity)
	require.Equal(t, int64(9), authFrame.EntityID)
	require.NotNil(t, authFrame.SessionID)
	require.Equal(t, int64(2), *authFrame.SessionID)
	require.Equal(t, int64(5), authFrame.Stid)

	revoked := covNextFrame(t, sub, "link.revoked terminal frame")
	require.Equal(t, "link.revoked", revoked.Entity)
	require.Equal(t, "tokR", revoked.Token, "terminal delivery targets exactly this token")
	require.Equal(t, int64(30), revoked.ChildID)
	covNoFrame(t, sub, "exactly two frames per revoke")
}

// ---------------------------------------------------------------------------
// Session reports matrix (GET /api/sessions/:sid/reports)
// ---------------------------------------------------------------------------

// TestSessionReportsMatrixShape: one row per member×topic computed from the
// batch queries — completeness, narrative presence, mission count, the
// candidate slot tri-state and both link URLs.
func TestSessionReportsMatrixShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT id, topic_name_snap FROM session_topics")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_name_snap"}).
			AddRow(5, "Topik A"))
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM session_activities")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}).
			AddRow(11, 5).AddRow(12, 5))
	mock.ExpectQuery(q("SELECT sm.child_id, sm.group_id, c.name")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "group_id", "name"}).
			AddRow(30, 10, "Ayu"))
	// Child 30 starred activity 11 only → incomplete row.
	mock.ExpectQuery(q("SELECT child_id, session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_activity_id"}).
			AddRow(30, 11))
	mock.ExpectQuery(q("SELECT child_id, session_topic_id FROM narratives")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id"}).
			AddRow(30, 5))
	mock.ExpectQuery(q("SELECT child_id, session_topic_id, COUNT(*)")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id", "n"}).
			AddRow(30, 5, 2))
	// Durable photos exist but the candidate slot is empty → {false}.
	mock.ExpectQuery(q("SELECT DISTINCT child_id, session_topic_id FROM photos")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id"}).
			AddRow(30, 5))
	mock.ExpectQuery(q("AND is_candidate = TRUE")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id"}))
	mock.ExpectQuery(q("SELECT child_id, session_topic_id, kind, token FROM public_links")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id", "kind", "token"}).
			AddRow(30, 5, "REPORT", "tokR").
			AddRow(30, 5, "GALLERY", "tokG"))

	res := doReq(t, app, "GET", "/api/sessions/2/reports", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 1, "one row per member×topic")
	row := data[0].(map[string]any)
	require.Equal(t, float64(30), row["child_id"])
	require.Equal(t, "Ayu", row["child_name"])
	require.Equal(t, float64(5), row["session_topic_id"])
	require.Equal(t, "Topik A", row["topic_name"])
	require.Equal(t, false, row["complete"], "activity 12 unstarred")
	require.Equal(t, true, row["narrative"])
	require.Equal(t, float64(2), row["missions"])
	require.Equal(t, false, row["candidate_photo"],
		"durable photos without a candidate → explicit false, not null")
	require.Equal(t, "http://localhost:8002/view/report?token=tokR", row["report_url"])
	require.Equal(t, "http://localhost:8002/view/gallery?token=tokG", row["gallery_url"])
}

// TestSessionReportsMatrixEmpty: a session with activities but ZERO members
// (the /sessions/:sid/reports shape for an unstaffed DRAFT session) answers
// 200 {"data":[]} — the empty slice must serialize as [], never null — so
// the frontend Table leaves its loading state and renders the empty state.
func TestSessionReportsMatrixEmpty(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	// One topic with one activity, zero members anywhere.
	mock.ExpectQuery(q("SELECT id, topic_name_snap FROM session_topics")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_name_snap"}).
			AddRow(4, "Gempa Bumi"))
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM session_activities")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}).
			AddRow(40, 4))
	mock.ExpectQuery(q("SELECT sm.child_id, sm.group_id, c.name")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "group_id", "name"}))
	mock.ExpectQuery(q("SELECT child_id, session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_activity_id"}))
	mock.ExpectQuery(q("SELECT child_id, session_topic_id FROM narratives")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id"}))
	mock.ExpectQuery(q("SELECT child_id, session_topic_id, COUNT(*)")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id", "n"}))
	mock.ExpectQuery(q("SELECT DISTINCT child_id, session_topic_id FROM photos")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id"}))
	mock.ExpectQuery(q("SELECT DISTINCT child_id, session_topic_id FROM photos")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id"}))
	mock.ExpectQuery(q("SELECT child_id, session_topic_id, kind, token FROM public_links")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id", "kind", "token"}))

	res := doReq(t, app, "GET", "/api/sessions/2/reports", "", h)
	require.Equal(t, http.StatusOK, res.status)
	data, ok := envelopeMap(t, res.body)["data"].([]any)
	require.True(t, ok, "empty matrix must serialise as [], never null: %s", res.body)
	require.Empty(t, data, "zero members → zero rows: %s", res.body)
}

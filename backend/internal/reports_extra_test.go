package internal

// reports_extra_test.go — extra behavioral coverage for reports.go: link
// regenerate, manual narrative patch, deliverReport/resolveSendTarget, the
// session-reports matrix guards, narrative generate success, mission-put
// gates and the per-branch DB failure paths. Every test asserts observable
// envelopes (200 + {data} / {error:{code,message}}), frame publications or
// exact sqlmock call sequences; sqlmock fails the test on any unexpected
// statement, so "no SQL before validation" is proven by absence.

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

const (
	rpxNarrativePatchPath = "/api/sessions/2/topics/5/children/30/narrative"
	rpxLinkCreatePath     = "/api/sessions/2/topics/5/children/30/public-links"
	rpxBatchSendPath      = "/api/sessions/2/batch-send"
	rpxSessionReportsPath = "/api/sessions/2/reports"
)

// ---------------------------------------------------------------------------
// Shared expectation fragments for this file's chains (unique per test —
// sqlmock matches by regexp substring within one ordered expectation list).
// ---------------------------------------------------------------------------

func rpxExpectAuthAdmin(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
}

func rpxExpectAuthFAS(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
}

func rpxExpectAuthSA(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
}

// rpxExpectIdemClaim wires the claim DELETE + INSERT of the idempotent
// wrapper for school 1.
func rpxExpectIdemClaim(mock sqlmock.Sqlmock) {
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// rpxExpectIdemRecord wires the success-envelope recording of the wrapper.
func rpxExpectIdemRecord(mock sqlmock.Sqlmock, route, key string) {
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), route, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// rpxExpectIdemRelease wires the typed DB-failure release (kvDBFail).
func rpxExpectIdemRelease(mock sqlmock.Sqlmock, route, key string) {
	mock.ExpectExec(q("DELETE FROM idempotency_keys WHERE school_id = ? AND route = ? AND idem_key = ?")).
		WithArgs(int64(1), route, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// rpxExpectBatchReplayProbe wires batchReplayReadBack's pre-wrapper claim
// probe (B1b): ErrNoRows = no claim yet → the wrapper claims and executes.
func rpxExpectBatchReplayProbe(mock sqlmock.Sqlmock, route, key string) {
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), route, key).WillReturnError(sql.ErrNoRows)
}

// rpxExpectSendGates wires the whole send gate chain up to (and including)
// the child-varying readiness scan, passing: membership → OPENED → topic →
// active child with a WA number → one starred activity.
func rpxExpectSendGates(mock sqlmock.Sqlmock, status string) {
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows(status))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT mother_wa, father_wa, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "father_wa", "is_active"}).
			AddRow("+628111111111", nil, true))
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id", "activity_name_snap"}).
			AddRow(5, 11, "Aktivitas A"))
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}).AddRow(11))
}

// rpxExpectLinkPairExisting wires the deliverReport link pair: both links
// already active (no INSERT, no created frames).
func rpxExpectLinkPairExisting(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30), "REPORT").
		WillReturnRows(sqlmock.NewRows([]string{"token"}).AddRow("tokR"))
	mock.ExpectQuery(q("SELECT token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30), "GALLERY").
		WillReturnRows(sqlmock.NewRows([]string{"token"}).AddRow("tokG"))
	mock.ExpectCommit()
}

// rpxGeminiOK swaps the server's HTTP client for a stub that answers every
// Gemini call with the given text — the deterministic success seam for
// handleNarrativePost without network.
func rpxGeminiOK(s *Server, text string) {
	body := `{"candidates":[{"content":{"parts":[{"text":"` + text + `"}]}}]}`
	s.http = &http.Client{Transport: blockedTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

// ---------------------------------------------------------------------------
// Pure functions: resolveSendTarget + starLabel
// ---------------------------------------------------------------------------

// TestRpxResolveSendTargetMatrix: default = mother's WA; a supplied `to`
// must equal a registered number (mother or father) else ok=false — no
// silent redirect to an unregistered destination (F-12).
func TestRpxResolveSendTargetMatrix(t *testing.T) {
	father := sql.NullString{String: "+628222222222", Valid: true}
	noFather := sql.NullString{}

	to, ok := resolveSendTarget("+628111111111", noFather, "")
	require.True(t, ok)
	require.Equal(t, "+628111111111", to, "default destination is the mother's WA")

	to, ok = resolveSendTarget("+628111111111", noFather, "+628111111111")
	require.True(t, ok)
	require.Equal(t, "+628111111111", to, "mother's own number always resolves")

	to, ok = resolveSendTarget("+628111111111", father, "+628222222222")
	require.True(t, ok)
	require.Equal(t, "+628222222222", to, "registered father number resolves")

	to, ok = resolveSendTarget("+628111111111", noFather, "+628222222222")
	require.False(t, ok, "father number without a registered father must not send")
	require.Empty(t, to)

	to, ok = resolveSendTarget("", noFather, "")
	require.False(t, ok, "no mother WA and no override = nothing to send to")
	require.Empty(t, to)

	to, ok = resolveSendTarget("+628111111111", noFather, "+628999999999")
	require.False(t, ok, "unregistered override is rejected")
	require.Empty(t, to)
}

// TestRpxStarLabelScale: the 1..4 star scale maps to the prompt's
// BB/MB/BSH/BSB labels; out-of-range falls into the BSB default.
func TestRpxStarLabelScale(t *testing.T) {
	require.Equal(t, "BB", starLabel(1))
	require.Equal(t, "MB", starLabel(2))
	require.Equal(t, "BSH", starLabel(3))
	require.Equal(t, "BSB", starLabel(4))
	require.Equal(t, "BSB", starLabel(0))
	require.Equal(t, "BSB", starLabel(9))
}

// ---------------------------------------------------------------------------
// Guard prologues: malformed ids and missing school answer BEFORE any
// handler SQL (only the auth user lookup runs).
// ---------------------------------------------------------------------------

// TestRpxReportRoutesRejectMalformedIDs: every inline-idParam handler and
// every pScope handler answers 200 NOT_FOUND for a malformed id without
// touching the domain tables.
func TestRpxReportRoutesRejectMalformedIDs(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	cases := []struct {
		method, path, body string
	}{
		{"PUT", "/api/sessions/xx/topics/5/children/30/missions", `{"mission_ids":[]}`},
		{"PUT", "/api/sessions/2/topics/xx/children/30/missions", `{"mission_ids":[]}`},
		{"PUT", "/api/sessions/2/topics/5/children/xx/missions", `{"mission_ids":[]}`},
		{"POST", "/api/sessions/xx/topics/5/children/30/narrative", `{}`},
		{"POST", "/api/sessions/2/topics/5/children/xx/narrative", `{}`},
		{"POST", "/api/sessions/2/topics/xx/children/30/narrative", `{}`},
		{"PATCH", "/api/sessions/xx/topics/5/children/30/narrative", `{"text":"x"}`},
		{"PATCH", "/api/sessions/2/topics/xx/children/30/narrative", `{"text":"x"}`},
		{"PATCH", "/api/sessions/2/topics/5/children/xx/narrative", `{"text":"x"}`},
		{"POST", "/api/sessions/xx/topics/5/children/30/public-links", `{"kind":"REPORT"}`},
		{"POST", "/api/sessions/2/topics/xx/children/30/public-links", `{"kind":"REPORT"}`},
		{"POST", "/api/sessions/2/topics/5/children/xx/public-links", `{"kind":"REPORT"}`},
		{"POST", "/api/sessions/xx/topics/5/children/30/send-report", `{}`},
		{"POST", "/api/sessions/2/topics/xx/children/30/send-report", `{}`},
		{"POST", "/api/sessions/2/topics/5/children/xx/send-report", `{}`},
		{"POST", "/api/sessions/xx/batch-send", `{"items":[]}`},
		{"GET", "/api/sessions/xx/reports", ""},
		{"GET", "/api/sessions/xx/topics/5/children/30/readiness", ""},
		{"GET", "/api/sessions/xx/topics/5/children/30/report", ""},
		{"GET", "/api/sessions/xx/topics/5/children/30/missions", ""},
		{"GET", "/api/sessions/xx/topics/5/children/30/narrative", ""},
		{"GET", "/api/sessions/xx/topics/5/children/30/public-links", ""},
	}
	for range cases {
		rpxExpectAuthAdmin(mock)
	}
	for _, tc := range cases {
		res := doReq(t, app, tc.method, tc.path, tc.body, h)
		requireErrEnvelope(t, res, CodeNotFound)
	}
}

// TestRpxMutatingRoutesRequireSchool: an SA without X-School-ID gets 200
// VALIDATION from the write-strict requireSchool guard before any handler
// SQL (auth for SA needs no school lookup).
func TestRpxMutatingRoutesRequireSchool(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hSABare(t, s)

	cases := []struct {
		method, path, body string
	}{
		{"PUT", missionPutPath, `{"mission_ids":[]}`},
		{"POST", rpxNarrativePatchPath, `{}`},
		{"PATCH", rpxNarrativePatchPath, `{"text":"x"}`},
		{"POST", rpxLinkCreatePath, `{"kind":"REPORT"}`},
		{"POST", sendReportPath, `{}`},
		{"POST", rpxBatchSendPath, `{"items":[]}`},
	}
	for range cases {
		rpxExpectAuthSA(mock)
	}
	for _, tc := range cases {
		res := doReq(t, app, tc.method, tc.path, tc.body, h)
		requireErrEnvelope(t, res, CodeValidation)
	}
}

// ---------------------------------------------------------------------------
// Readiness + ReportData error branches
// ---------------------------------------------------------------------------

// TestRpxReportReadinessNotFoundAndDBFail: an unknown session (program_id
// probe hits no row) is NOT_FOUND; a dead probe is DB_UNAVAILABLE.
func TestRpxReportReadinessNotFoundAndDBFail(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
	res := doReq(t, app, "GET", "/api/sessions/2/topics/5/children/30/readiness", "", h)
	requireErrEnvelope(t, res, CodeNotFound)

	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(errors.New("dial tcp: connection refused"))
	res = doReq(t, app, "GET", "/api/sessions/2/topics/5/children/30/readiness", "", h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// rpxReportGetPrefix wires pScope (auth + membership + topic linkage) for
// GET P/report.
func rpxReportGetPrefix(mock sqlmock.Sqlmock) {
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
}

func rpxExpectSessionSnapshot(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date", "location",
			"status", "program_id", "program_name_snap"}).
			AddRow(2, "Sesi A", fixedTS, "Ruang 1", "OPENED", 9, "Program X"))
}

func rpxExpectTopicSnapshot(mock sqlmock.Sqlmock, badgeName, badgeImage any) {
	mock.ExpectQuery(q("SELECT topic_name_snap, badge_name_snap, badge_image_snap")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"topic_name_snap", "badge_name_snap", "badge_image_snap"}).
			AddRow("Topik A", badgeName, badgeImage))
}

func rpxExpectChildRow(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(q("SELECT name, age, school, mother_name, father_name, guardian_name")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "age", "school", "mother_name",
			"father_name", "guardian_name"}).
			AddRow("Ayu", 8, "SD Sehat", "Ibu Ayu", nil, nil))
}

// TestRpxReportGetBuilderErrorBranches: the canonical builder's row-level
// failures map to NOT_FOUND (row absent) or DB_UNAVAILABLE (driver trouble)
// on the auth render path — one request per failure point.
func TestRpxReportGetBuilderErrorBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	getPath := "/api/sessions/2/topics/5/children/30/report"

	// Session row dead → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection reset"))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)

	// Topic absent → NOT_FOUND.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	mock.ExpectQuery(q("SELECT topic_name_snap, badge_name_snap, badge_image_snap")).
		WithArgs(int64(5), int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeNotFound)

	// Programs badge lookup dead → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).WillReturnError(errors.New("connection reset"))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)

	// Child row dead → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).AddRow(nil, nil))
	mock.ExpectQuery(q("SELECT name, age, school, mother_name, father_name, guardian_name")).
		WithArgs(int64(30), int64(1)).WillReturnError(errors.New("connection reset"))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)

	// Group join dead (driver error, not ErrNoRows) → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).AddRow(nil, nil))
	rpxExpectChildRow(mock)
	mock.ExpectQuery(q("FROM session_members sm JOIN session_groups g ON g.id = sm.group_id")).
		WithArgs(int64(2), int64(30)).WillReturnError(errors.New("connection reset"))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)

	// LIVE owner lookup dead → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).AddRow(nil, nil))
	rpxExpectChildRow(mock)
	mock.ExpectQuery(q("FROM session_members sm JOIN session_groups g ON g.id = sm.group_id")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "facilitator_id", "facilitator_name_snap"}).
			AddRow("Kelompok Merdeka", 7, "Kak Sari"))
	mock.ExpectQuery(q("SELECT name FROM users WHERE id = ?")).
		WithArgs(int64(7)).WillReturnError(errors.New("connection reset"))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)

	// Narrative row dead → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).AddRow(nil, nil))
	rpxExpectChildRow(mock)
	mock.ExpectQuery(q("FROM session_members sm JOIN session_groups g ON g.id = sm.group_id")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(errors.New("connection reset"))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)

	// Outcomes query dead → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).AddRow(nil, nil))
	rpxExpectChildRow(mock)
	mock.ExpectQuery(q("FROM session_members sm JOIN session_groups g ON g.id = sm.group_id")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT a.session_activity_id, a.activity_name_snap, sa.indicator_snap")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnError(errors.New("connection reset"))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)

	// Outcomes scan failure (stars not numeric) → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).AddRow(nil, nil))
	rpxExpectChildRow(mock)
	mock.ExpectQuery(q("FROM session_members sm JOIN session_groups g ON g.id = sm.group_id")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT a.session_activity_id, a.activity_name_snap, sa.indicator_snap")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id", "activity_name_snap",
			"indicator_snap", "stars", "note"}).
			AddRow(11, "Aktivitas A", nil, "not-a-number", nil))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)

	// Outcomes iteration error → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).AddRow(nil, nil))
	rpxExpectChildRow(mock)
	mock.ExpectQuery(q("FROM session_members sm JOIN session_groups g ON g.id = sm.group_id")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT a.session_activity_id, a.activity_name_snap, sa.indicator_snap")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id", "activity_name_snap",
			"indicator_snap", "stars", "note"}).
			AddRow(11, "Aktivitas A", nil, 4, nil).RowError(0, errors.New("iter boom")))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)

	// Mission picks query dead → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).AddRow(nil, nil))
	rpxExpectChildRow(mock)
	mock.ExpectQuery(q("FROM session_members sm JOIN session_groups g ON g.id = sm.group_id")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT a.session_activity_id, a.activity_name_snap, sa.indicator_snap")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id", "activity_name_snap",
			"indicator_snap", "stars", "note"}))
	mock.ExpectQuery(q("FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(errors.New("connection reset"))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)

	// Mission pick scan failure → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).AddRow(nil, nil))
	rpxExpectChildRow(mock)
	mock.ExpectQuery(q("FROM session_members sm JOIN session_groups g ON g.id = sm.group_id")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT a.session_activity_id, a.activity_name_snap, sa.indicator_snap")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id", "activity_name_snap",
			"indicator_snap", "stars", "note"}))
	mock.ExpectQuery(q("FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"mission_id", "mission_name_snap"}).
			AddRow("x", "Misi"))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)

	// Mission pick stream dies mid-read → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).AddRow(nil, nil))
	rpxExpectChildRow(mock)
	mock.ExpectQuery(q("FROM session_members sm JOIN session_groups g ON g.id = sm.group_id")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT a.session_activity_id, a.activity_name_snap, sa.indicator_snap")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id", "activity_name_snap",
			"indicator_snap", "stars", "note"}))
	mock.ExpectQuery(q("FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"mission_id", "mission_name_snap"}).
			AddRow(11, "Misi").RowError(0, errors.New("iter boom")))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)

	// Candidate photo lookup dead → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).AddRow(nil, nil))
	rpxExpectChildRow(mock)
	mock.ExpectQuery(q("FROM session_members sm JOIN session_groups g ON g.id = sm.group_id")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT a.session_activity_id, a.activity_name_snap, sa.indicator_snap")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id", "activity_name_snap",
			"indicator_snap", "stars", "note"}))
	mock.ExpectQuery(q("FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"mission_id", "mission_name_snap"}))
	mock.ExpectQuery(q("SELECT path FROM photos")).
		WithArgs(int64(2), int64(5), int64(30), int64(1)).
		WillReturnError(errors.New("connection reset"))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)

	// Readiness activity-set load dead → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).AddRow(nil, nil))
	rpxExpectChildRow(mock)
	mock.ExpectQuery(q("FROM session_members sm JOIN session_groups g ON g.id = sm.group_id")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT a.session_activity_id, a.activity_name_snap, sa.indicator_snap")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id", "activity_name_snap",
			"indicator_snap", "stars", "note"}))
	mock.ExpectQuery(q("FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"mission_id", "mission_name_snap"}))
	mock.ExpectQuery(q("SELECT path FROM photos")).
		WithArgs(int64(2), int64(5), int64(30), int64(1)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection reset"))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)

	// Child readiness scan dead → DB_UNAVAILABLE.
	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).AddRow(nil, nil))
	rpxExpectChildRow(mock)
	mock.ExpectQuery(q("FROM session_members sm JOIN session_groups g ON g.id = sm.group_id")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT a.session_activity_id, a.activity_name_snap, sa.indicator_snap")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id", "activity_name_snap",
			"indicator_snap", "stars", "note"}))
	mock.ExpectQuery(q("FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"mission_id", "mission_name_snap"}))
	mock.ExpectQuery(q("SELECT path FROM photos")).
		WithArgs(int64(2), int64(5), int64(30), int64(1)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id", "activity_name_snap"}).
			AddRow(5, 11, "Aktivitas A"))
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30), int64(1)).WillReturnError(errors.New("connection reset"))
	requireErrEnvelope(t, doReq(t, app, "GET", getPath, "", h), CodeDBUnavailable)
}

// TestRpxReportGetAttestationFallbackRenders: the LIVE group owner row is
// gone → attestation falls back to facilitator_name_snap (F-12 option A);
// the whole render stays 200 with the pinned nullable shapes (null group
// only when membership itself is gone).
func TestRpxReportGetAttestationFallbackRenders(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	rpxReportGetPrefix(mock)
	rpxExpectSessionSnapshot(mock)
	rpxExpectTopicSnapshot(mock, nil, nil)
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).AddRow(nil, nil))
	rpxExpectChildRow(mock)
	mock.ExpectQuery(q("FROM session_members sm JOIN session_groups g ON g.id = sm.group_id")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "facilitator_id", "facilitator_name_snap"}).
			AddRow("Kelompok Merdeka", 7, "Kak Sari"))
	mock.ExpectQuery(q("SELECT name FROM users WHERE id = ?")).
		WithArgs(int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT a.session_activity_id, a.activity_name_snap, sa.indicator_snap")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id", "activity_name_snap",
			"indicator_snap", "stars", "note"}).
			AddRow(11, "Aktivitas A", "Menulis nama", 4, "Mandiri"))
	mock.ExpectQuery(q("FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"mission_id", "mission_name_snap"}).
			AddRow(11, "Misi Sehat"))
	mock.ExpectQuery(q("SELECT path FROM photos")).
		WithArgs(int64(2), int64(5), int64(30), int64(1)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id", "activity_name_snap"}).
			AddRow(5, 11, "Aktivitas A"))
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}).AddRow(11))

	res := doReq(t, app, "GET", "/api/sessions/2/topics/5/children/30/report", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	att := data["attestation"].(map[string]any)
	require.Equal(t, "Kak Sari", att["name"], "owner row gone → snap fallback")
	require.Equal(t, "Fasilitator", att["role"])
	group := data["group"].(map[string]any)
	require.Equal(t, "Kelompok Merdeka", group["name"])
	require.Nil(t, data["narrative"].(map[string]any)["text"])
	require.Nil(t, data["candidate_photo"])
	require.Nil(t, data["badges"].(map[string]any)["topic"])
	require.Nil(t, data["badges"].(map[string]any)["program"])
	require.Equal(t, true, data["complete"])
	require.Equal(t, []any{}, data["missing"])
	outcomes := data["outcomes"].([]any)
	require.Len(t, outcomes, 1)
	first := outcomes[0].(map[string]any)
	require.Equal(t, float64(4), first["stars"])
	require.Equal(t, "Menulis nama", first["indicator"])
	missions := data["missions"].([]any)
	require.Len(t, missions, 1)
	require.Equal(t, "Misi Sehat", missions[0].(map[string]any)["name"])
}

// ---------------------------------------------------------------------------
// Missions
// ---------------------------------------------------------------------------

// TestRpxMissionListQueryTroubleReturnsEmptyList: missionPicks never fails
// the request — DB trouble degrades to the empty list (200 {data:[]}).
func TestRpxMissionListQueryTroubleReturnsEmptyList(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	missPrefix := func() {
		rpxExpectAuthAdmin(mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
			WithArgs(int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	}

	missPrefix()
	mock.ExpectQuery(q("FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(errors.New("connection reset"))
	res := doReq(t, app, "GET", "/api/sessions/2/topics/5/children/30/missions", "", h)
	require.Equal(t, []any{}, requireDataEnvelope(t, res), "query failure degrades to empty list")

	missPrefix()
	mock.ExpectQuery(q("FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"mission_id", "mission_name_snap"}).
			AddRow("x", "Misi"))
	res = doReq(t, app, "GET", "/api/sessions/2/topics/5/children/30/missions", "", h)
	require.Equal(t, []any{}, requireDataEnvelope(t, res), "scan failure degrades to empty list")
}

// rpxExpectMissionPutPrefix wires the mission-put gates up to the child
// check (membership → topic → child); the status probe is queued separately
// because CHILD_INACTIVE returns before it.
func rpxExpectMissionPutPrefix(mock sqlmock.Sqlmock, childActive bool) {
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT topic_id FROM session_topics")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id"}).AddRow(9))
	mock.ExpectQuery(q("SELECT is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(childActive))
}

// rpxExpectMissionPutStatus wires the OPENED status probe after the prefix.
func rpxExpectMissionPutStatus(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
}

// TestRpxMissionPutGateAndValidationBranches: FINISHED blocks the replace
// (FL-16), unknown topic/child are NOT_FOUND, inactive child is
// CHILD_INACTIVE and bad pick sets (id ≤ 0, unknown mission, unlinked
// mission, duplicate id) are VALIDATION — each before any Tx starts.
func TestRpxMissionPutGateAndValidationBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	// FINISHED → SESSION_LOCKED.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT topic_id FROM session_topics")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id"}).AddRow(9))
	mock.ExpectQuery(q("SELECT is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("FINISHED"))
	res := jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{11}}, h)
	requireErrEnvelope(t, res, CodeSessionLocked)

	// Topic unknown → NOT_FOUND.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT topic_id FROM session_topics")).
		WithArgs(int64(5), int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
	res = jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{11}}, h)
	requireErrEnvelope(t, res, CodeNotFound)

	// Inactive child → CHILD_INACTIVE.
	rpxExpectMissionPutPrefix(mock, false)
	res = jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{11}}, h)
	requireErrEnvelope(t, res, CodeChildInactive)

	// id ≤ 0 → VALIDATION (before any mission lookup).
	rpxExpectMissionPutPrefix(mock, true)
	rpxExpectMissionPutStatus(mock)
	res = jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{-1}}, h)
	requireErrEnvelope(t, res, CodeValidation)

	// Unknown mission → VALIDATION.
	rpxExpectMissionPutPrefix(mock, true)
	rpxExpectMissionPutStatus(mock)
	mock.ExpectQuery(q("SELECT name FROM missions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(99), int64(1)).WillReturnError(sql.ErrNoRows)
	res = jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{99}}, h)
	requireErrEnvelope(t, res, CodeValidation)

	// Mission not linked to the topic's master topic → VALIDATION.
	rpxExpectMissionPutPrefix(mock, true)
	rpxExpectMissionPutStatus(mock)
	mock.ExpectQuery(q("SELECT name FROM missions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(11), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Misi 1"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics WHERE mission_id = ? AND topic_id = ?)")).
		WithArgs(int64(11), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"linked"}).AddRow(0))
	res = jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{11}}, h)
	requireErrEnvelope(t, res, CodeValidation)

	// Duplicate id → VALIDATION (dedup keeps the Tx insert conflict-free).
	rpxExpectMissionPutPrefix(mock, true)
	rpxExpectMissionPutStatus(mock)
	mock.ExpectQuery(q("SELECT name FROM missions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(11), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Misi 1"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics WHERE mission_id = ? AND topic_id = ?)")).
		WithArgs(int64(11), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"linked"}).AddRow(1))
	res = jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{11, 11}}, h)
	requireErrEnvelope(t, res, CodeValidation)
}

// TestRpxMissionPutTxTroubleRollsBack: Begin/DELETE/INSERT failures map to
// DB_UNAVAILABLE and every started Tx rolls back (no half-written pick set).
func TestRpxMissionPutTxTroubleRollsBack(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	// Begin dead.
	rpxExpectMissionPutPrefix(mock, true)
	rpxExpectMissionPutStatus(mock)
	mock.ExpectQuery(q("SELECT name FROM missions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(11), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Misi 1"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics WHERE mission_id = ? AND topic_id = ?)")).
		WithArgs(int64(11), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"linked"}).AddRow(1))
	mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
	res := jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{11}}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// DELETE dead → rollback.
	rpxExpectMissionPutPrefix(mock, true)
	rpxExpectMissionPutStatus(mock)
	mock.ExpectQuery(q("SELECT name FROM missions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(11), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Misi 1"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics WHERE mission_id = ? AND topic_id = ?)")).
		WithArgs(int64(11), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"linked"}).AddRow(1))
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "OPENED")
	mock.ExpectExec(q("DELETE FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()
	res = jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{11}}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// INSERT dead → rollback.
	rpxExpectMissionPutPrefix(mock, true)
	rpxExpectMissionPutStatus(mock)
	mock.ExpectQuery(q("SELECT name FROM missions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(11), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Misi 1"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics WHERE mission_id = ? AND topic_id = ?)")).
		WithArgs(int64(11), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"linked"}).AddRow(1))
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "OPENED")
	mock.ExpectExec(q("DELETE FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("INSERT INTO report_missions")).
		WithArgs(int64(2), int64(5), int64(30), int64(11), "Misi 1", int64(1)).
		WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()
	res = jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{11}}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestRpxMissionPutStatusRaceRollsBack (B1a): the pre-read sees a writable
// session but it finishes before the write Tx locks the row — the in-Tx
// FOR UPDATE re-read (lockSessionEditable) answers SESSION_LOCKED and the
// replace-set never runs (rollback, picks unchanged).
func TestRpxMissionPutStatusRaceRollsBack(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	rpxExpectMissionPutPrefix(mock, true)
	rpxExpectMissionPutStatus(mock) // pre-read: still writable (gate order intact)
	mock.ExpectQuery(q("SELECT name FROM missions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(11), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Misi 1"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics WHERE mission_id = ? AND topic_id = ?)")).
		WithArgs(int64(11), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"linked"}).AddRow(1))
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "FINISHED") // the rival transition won the lock
	mock.ExpectRollback()
	res := jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{11}}, h)
	requireErrEnvelope(t, res, CodeSessionLocked)
}

// ---------------------------------------------------------------------------
// Narrative manual edit (PATCH)
// ---------------------------------------------------------------------------

// TestRpxNarrativePatchManualEditUpserts: a manual edit upserts the row
// (model/generated_at stay null) and publishes the pair — narrative.changed
// to the auth side and public report.changed for the parent pages.
func TestRpxNarrativePatchManualEditUpserts(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "OPENED")
	mock.ExpectExec(q("INSERT INTO narratives")).
		WithArgs(int64(2), int64(5), int64(30), int64(1), "Cerita manual", int64(2)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"text", "model", "generated_at"}).
			AddRow("Cerita manual", nil, nil))

	res := jsonReq(t, app, "PATCH", rpxNarrativePatchPath,
		map[string]any{"text": "  Cerita manual  "}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "Cerita manual", data["text"], "text is trimmed before persist")
	require.Nil(t, data["model"], "manual rows keep a null model")
	require.Nil(t, data["generated_at"], "manual rows keep a null generated_at")

	require.Equal(t, "narrative.changed", covNextFrame(t, sub, "auth-side narrative frame").Entity)
	require.Equal(t, "report.changed", covNextFrame(t, sub, "public refetch frame").Entity)
	covNoFrame(t, sub, "exactly the pair per manual edit")
}

// TestRpxNarrativePatchValidationBeforeSQL: malformed JSON, blank text and
// >2000-char text are rejected with VALIDATION while sqlmock has NO
// expectations beyond auth — any domain query would fail the test.
func TestRpxNarrativePatchValidationBeforeSQL(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	for range 3 {
		rpxExpectAuthAdmin(mock)
	}
	res := doReq(t, app, "PATCH", rpxNarrativePatchPath, "{oops", h)
	requireErrEnvelope(t, res, CodeValidation)

	res = jsonReq(t, app, "PATCH", rpxNarrativePatchPath, map[string]any{"text": "   \n\t "}, h)
	requireErrEnvelope(t, res, CodeValidation)

	res = jsonReq(t, app, "PATCH", rpxNarrativePatchPath,
		map[string]any{"text": strings.Repeat("a", 2001)}, h)
	requireErrEnvelope(t, res, CodeValidation)
}

// TestRpxNarrativePatchGateAndFailureBranches: locked session →
// SESSION_LOCKED; membership/topic gaps → NOT_FOUND; upsert/read trouble →
// DB_UNAVAILABLE — no row is ever half-written.
func TestRpxNarrativePatchGateAndFailureBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	patchGates := func(status string) {
		rpxExpectAuthAdmin(mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
			WithArgs(int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows(status))
	}

	// Status absent (unknown session) → NOT_FOUND.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
	res := jsonReq(t, app, "PATCH", rpxNarrativePatchPath, map[string]any{"text": "Cerita"}, h)
	requireErrEnvelope(t, res, CodeNotFound)

	// FINISHED → SESSION_LOCKED (guards same as generate).
	patchGates("FINISHED")
	res = jsonReq(t, app, "PATCH", rpxNarrativePatchPath, map[string]any{"text": "Cerita"}, h)
	requireErrEnvelope(t, res, CodeSessionLocked)

	// Non-member → NOT_FOUND.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	res = jsonReq(t, app, "PATCH", rpxNarrativePatchPath, map[string]any{"text": "Cerita"}, h)
	requireErrEnvelope(t, res, CodeNotFound)

	// Topic outside the session → NOT_FOUND.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
	res = jsonReq(t, app, "PATCH", rpxNarrativePatchPath, map[string]any{"text": "Cerita"}, h)
	requireErrEnvelope(t, res, CodeNotFound)

	// Upsert dead → DB_UNAVAILABLE (Tx rolled back).
	patchGates("OPENED")
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "OPENED")
	mock.ExpectExec(q("INSERT INTO narratives")).
		WithArgs(int64(2), int64(5), int64(30), int64(1), "Cerita", int64(2)).
		WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()
	res = jsonReq(t, app, "PATCH", rpxNarrativePatchPath, map[string]any{"text": "Cerita"}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Read-back missing after the commit → DB_UNAVAILABLE.
	patchGates("OPENED")
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "OPENED")
	mock.ExpectExec(q("INSERT INTO narratives")).
		WithArgs(int64(2), int64(5), int64(30), int64(1), "Cerita", int64(2)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(sql.ErrNoRows)
	res = jsonReq(t, app, "PATCH", rpxNarrativePatchPath, map[string]any{"text": "Cerita"}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestRpxNarrativePatchStatusRaceRollsBack (B1a): the pre-read sees OPENED
// but the session finishes before the write Tx takes the row lock — the
// in-Tx FOR UPDATE re-read answers SESSION_LOCKED, the manual edit never
// lands (rollback) and no frame fires.
func TestRpxNarrativePatchStatusRaceRollsBack(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "FINISHED") // the rival transition won the lock
	mock.ExpectRollback()
	res := jsonReq(t, app, "PATCH", rpxNarrativePatchPath, map[string]any{"text": "Cerita"}, h)
	requireErrEnvelope(t, res, CodeSessionLocked)
}

// ---------------------------------------------------------------------------
// Narrative generate (POST) — success + failure branches
// ---------------------------------------------------------------------------

// TestRpxNarrativePostSuccessStoresAndPublishes: a successful Gemini call
// upserts the generated narrative and publishes the pair; the response
// carries the stored text with its model.
func TestRpxNarrativePostSuccessStoresAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-narr-post-1"
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})
	rpxGeminiOK(s, "Ayu senang belajar hari ini.")

	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT name, age, is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "age", "is_active"}).AddRow("Ayu", 8, true))
	mock.ExpectQuery(q("FROM sessions s JOIN session_topics st ON st.id = ? AND st.session_id = s.id")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "date", "topic_name_snap"}).
			AddRow("Sesi A", fixedTS, "Topik A"))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("FROM assessments a")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap", "stars", "note"}).
			AddRow("Aktivitas A", 4, "Mandiri"))
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "OPENED")
	mock.ExpectExec(q("INSERT INTO narratives")).
		WithArgs(int64(2), int64(5), int64(30), int64(1), "Ayu senang belajar hari ini.", "", int64(2)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"text", "model", "generated_at"}).
			AddRow("Ayu senang belajar hari ini.", "gemini-stub", nil))
	rpxExpectIdemRecord(mock, "POST "+narrativePostPath, "rpx-narr-post-1")

	res := jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "Ayu senang belajar hari ini.", data["text"])
	require.Equal(t, "gemini-stub", data["model"])

	require.Equal(t, "narrative.changed", covNextFrame(t, sub, "auth-side frame").Entity)
	require.Equal(t, "report.changed", covNextFrame(t, sub, "public refetch frame").Entity)
	covNoFrame(t, sub, "exactly the pair per generate")
}

// TestRpxNarrativePostFailureBranches: locked/inactive/unknown-child gates
// answer their codes; AI-side DB trouble maps to DB_UNAVAILABLE and the
// idempotency claim RELEASES (never records a transient failure).
func TestRpxNarrativePostFailureBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-narr-post-2"
	const route = "POST " + narrativePostPath

	narrGates := func(childRows *sqlmock.Rows) {
		rpxExpectAuthAdmin(mock)
		rpxExpectIdemClaim(mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
			WithArgs(int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
		mock.ExpectQuery(q("SELECT name, age, is_active FROM children WHERE id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).WillReturnRows(childRows)
	}

	activeChild := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"name", "age", "is_active"}).AddRow("Ayu", 8, true)
	}
	sessionJoin := func() {
		mock.ExpectQuery(q("FROM sessions s JOIN session_topics st ON st.id = ? AND st.session_id = s.id")).
			WithArgs(int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "date", "topic_name_snap"}).
				AddRow("Sesi A", fixedTS, "Topik A"))
	}

	// Locked session → SESSION_LOCKED (recorded like any non-DB failure).
	narrGates(activeChild())
	sessionJoin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("FINISHED"))
	rpxExpectIdemRecord(mock, route, "rpx-narr-post-2")
	res := jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeSessionLocked)

	// Inactive child → CHILD_INACTIVE.
	narrGates(sqlmock.NewRows([]string{"name", "age", "is_active"}).AddRow("Ayu", 8, false))
	rpxExpectIdemRecord(mock, route, "rpx-narr-post-2")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeChildInactive)

	// Unknown child → NOT_FOUND.
	narrGates(sqlmock.NewRows([]string{"name", "age", "is_active"}))
	rpxExpectIdemRecord(mock, route, "rpx-narr-post-2")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeNotFound)

	// Assessment block dead → DB_UNAVAILABLE, claim released.
	narrGates(activeChild())
	sessionJoin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("FROM assessments a")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-narr-post-2")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Upsert dead → DB_UNAVAILABLE, claim released.
	narrGates(activeChild())
	sessionJoin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("FROM assessments a")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap", "stars", "note"}).
			AddRow("Aktivitas A", 3, nil))
	s.http = &http.Client{Transport: blockedTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"candidates":[{"content":{"parts":[{"text":"Narasi."}]}}]}`))}, nil
	})}
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "OPENED")
	mock.ExpectExec(q("INSERT INTO narratives")).
		WithArgs(int64(2), int64(5), int64(30), int64(1), "Narasi.", "", int64(2)).
		WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()
	rpxExpectIdemRelease(mock, route, "rpx-narr-post-2")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Read-back missing after the upsert → DB_UNAVAILABLE, claim released.
	narrGates(activeChild())
	sessionJoin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("FROM assessments a")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap", "stars", "note"}).
			AddRow("Aktivitas A", 3, nil))
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "OPENED")
	mock.ExpectExec(q("INSERT INTO narratives")).
		WithArgs(int64(2), int64(5), int64(30), int64(1), "Narasi.", "", int64(2)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(sql.ErrNoRows)
	rpxExpectIdemRelease(mock, route, "rpx-narr-post-2")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestRpxNarrativePostStatusRaceRollsBack (B1a): the pre-read sees OPENED
// but the session finishes while Gemini is running — the in-Tx FOR UPDATE
// re-read answers SESSION_LOCKED, the generated text never lands (rollback,
// the old narrative is kept) and the failure envelope is recorded like any
// other non-DB outcome.
func TestRpxNarrativePostStatusRaceRollsBack(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-narr-post-race"
	rpxGeminiOK(s, "Narasi baru.")

	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT name, age, is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "age", "is_active"}).AddRow("Ayu", 8, true))
	mock.ExpectQuery(q("FROM sessions s JOIN session_topics st ON st.id = ? AND st.session_id = s.id")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "date", "topic_name_snap"}).
			AddRow("Sesi A", fixedTS, "Topik A"))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("FROM assessments a")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap", "stars", "note"}).
			AddRow("Aktivitas A", 3, nil))
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "FINISHED") // finished while the AI call ran
	mock.ExpectRollback()
	rpxExpectIdemRecord(mock, "POST "+narrativePostPath, "rpx-narr-post-race")

	res := jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeSessionLocked)
}

// TestRpxNarrativeGetReadTroubleIsNull: readNarrative swallows driver
// trouble (slog'd) and the GET answers raw null data — never a 5xx.
func TestRpxNarrativeGetReadTroubleIsNull(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(errors.New("connection reset"))

	res := doReq(t, app, "GET", rpxNarrativePatchPath, "", h)
	require.Nil(t, requireDataEnvelope(t, res), "driver trouble degrades to raw null data")
}

// ---------------------------------------------------------------------------
// Public links: regenerate / create / revoke / list
// ---------------------------------------------------------------------------

// TestRpxLinkRegenerateRotatesPairAndPublishes: one Tx revokes the pair's
// active links, creates a fresh REPORT+GALLERY pair, and publishes the
// token-precise link.revoked frames for the dying tokens plus the created
// frames for the fresh pair.
func TestRpxLinkRegenerateRotatesPairAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})
	const (
		token   = "tokOld"
		route   = "POST /api/public-links/tokOld/regenerate"
		idemKey = "rpx-regen-1"
	)
	h["X-Idempotency-Key"] = idemKey

	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs(token).
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"session_topic_id", "child_id"}).
			AddRow(int64(1), int64(2), int64(5), int64(30)))
	mock.ExpectBegin()
	// The dying pair is collected FOR UPDATE before the revoke sweep.
	mock.ExpectQuery(q("SELECT id, token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token"}).
			AddRow(10, "tokOldR").AddRow(11, "tokOldG"))
	mock.ExpectExec(q("UPDATE public_links SET revoked_at = NOW()")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	// Fresh pair inside the same Tx.
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
	rpxExpectIdemRecord(mock, route, idemKey)

	res := jsonReq(t, app, "POST", "/api/public-links/tokOld/regenerate", nil, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Regexp(t, `^http://localhost:8002/view/report\?token=[0-9a-f]{64}$`, data["report_url"])
	require.Regexp(t, `^http://localhost:8002/view/gallery\?token=[0-9a-f]{64}$`, data["gallery_url"])

	// Revocation signals first, token-precise, one per dying link…
	revoked := covNextFrame(t, sub, "first revoke signal")
	require.Equal(t, "report-link.changed", revoked.Entity)
	require.Equal(t, int64(10), revoked.EntityID)
	require.Equal(t, "revoked", revoked.Action)
	dead := covNextFrame(t, sub, "first terminal link.revoked")
	require.Equal(t, "link.revoked", dead.Entity)
	require.Equal(t, "tokOldR", dead.Token, "terminal delivery targets the dying token")
	require.Equal(t, int64(30), dead.ChildID)
	require.Equal(t, "report-link.changed", covNextFrame(t, sub, "second revoke signal").Entity)
	require.Equal(t, "tokOldG", covNextFrame(t, sub, "second terminal").Token)
	// …then one created frame per fresh link.
	require.Equal(t, "created", covNextFrame(t, sub, "report link created").Action)
	require.Equal(t, "created", covNextFrame(t, sub, "gallery link created").Action)
	covNoFrame(t, sub, "regenerate publishes exactly six frames")
}

// TestRpxLinkRegenerateFailureBranches: unknown/revoked tokens are
// TOKEN_INVALID, foreign FAS tokens NOT_OWNER, and Tx trouble is
// DB_UNAVAILABLE with the claim released — the old pair stays active.
func TestRpxLinkRegenerateFailureBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	hAdminTok := hAdmin(t, s)
	hAdminTok["X-Idempotency-Key"] = "rpx-regen-bad"
	hFASTok := hFAS(t, s)
	hFASTok["X-Idempotency-Key"] = "rpx-regen-fas"
	const (
		routeBad  = "POST /api/public-links/tokBad/regenerate"
		routeFAS  = "POST /api/public-links/tokF/regenerate"
		routeDead = "POST /api/public-links/tokDead/regenerate"
		routeScan = "POST /api/public-links/tokScan/regenerate"
	)

	// Unknown token → TOKEN_INVALID (identical shape for revoked tokens).
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokBad").WillReturnError(sql.ErrNoRows)
	rpxExpectIdemRecord(mock, routeBad, "rpx-regen-bad")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokBad/regenerate", nil, hAdminTok),
		CodeTokenInvalid)

	// Token lookup dead → DB_UNAVAILABLE, claim released.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokDead").WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, routeDead, "rpx-regen-bad")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokDead/regenerate", nil, hAdminTok),
		CodeDBUnavailable)

	// FAS token outside its own groups → NOT_OWNER (no Tx ever starts).
	rpxExpectAuthFAS(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokF").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"session_topic_id", "child_id"}).
			AddRow(int64(1), int64(2), int64(5), int64(30)))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(99))
	rpxExpectIdemRecord(mock, routeFAS, "rpx-regen-fas")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokF/regenerate", nil, hFASTok),
		CodeNotOwner)

	// The FOR UPDATE collection dies → rollback + claim release.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokDead").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"session_topic_id", "child_id"}).
			AddRow(int64(1), int64(2), int64(5), int64(30)))
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT id, token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()
	rpxExpectIdemRelease(mock, routeDead, "rpx-regen-bad")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokDead/regenerate", nil, hAdminTok),
		CodeDBUnavailable)

	// Scan trouble on the dying pair → rollback + claim release.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokScan").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"session_topic_id", "child_id"}).
			AddRow(int64(1), int64(2), int64(5), int64(30)))
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT id, token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token"}).AddRow("x", "tokOld"))
	mock.ExpectRollback()
	rpxExpectIdemRelease(mock, routeScan, "rpx-regen-bad")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokScan/regenerate", nil, hAdminTok),
		CodeDBUnavailable)
}

// TestRpxLinkCreateRejectsBadKindBeforeSQL: kind validates the request
// intent (02 §2b) — anything but REPORT/GALLERY is VALIDATION with no SQL
// beyond auth, before the idempotent wrapper even claims.
func TestRpxLinkCreateRejectsBadKindBeforeSQL(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	rpxExpectAuthAdmin(mock)
	res := jsonReq(t, app, "POST", rpxLinkCreatePath, map[string]any{"kind": "BOGUS"}, h)
	requireErrEnvelope(t, res, CodeValidation)
}

// TestRpxLinkCreateFailureBranches: gate failures answer their codes (and
// record), Tx trouble is DB_UNAVAILABLE with rollback + claim release.
func TestRpxLinkCreateFailureBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-link-1"
	const route = "POST " + rpxLinkCreatePath

	// Non-member → NOT_FOUND.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	rpxExpectIdemRecord(mock, route, "rpx-link-1")
	res := jsonReq(t, app, "POST", rpxLinkCreatePath, map[string]any{"kind": "REPORT"}, h)
	requireErrEnvelope(t, res, CodeNotFound)

	// Topic outside the session → NOT_FOUND.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
	rpxExpectIdemRecord(mock, route, "rpx-link-1")
	res = jsonReq(t, app, "POST", rpxLinkCreatePath, map[string]any{"kind": "REPORT"}, h)
	requireErrEnvelope(t, res, CodeNotFound)

	// Begin dead → DB_UNAVAILABLE, claim released.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-link-1")
	res = jsonReq(t, app, "POST", rpxLinkCreatePath, map[string]any{"kind": "REPORT"}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Pair query dead → rollback + claim release.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30), "REPORT").
		WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()
	rpxExpectIdemRelease(mock, route, "rpx-link-1")
	res = jsonReq(t, app, "POST", rpxLinkCreatePath, map[string]any{"kind": "REPORT"}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestRpxLinkRevokeFailureBranches: unknown token → TOKEN_INVALID, dead
// lookup → DB_UNAVAILABLE, foreign FAS token → NOT_OWNER, a lost revoke
// race (0 rows) → TOKEN_INVALID with rollback.
func TestRpxLinkRevokeFailureBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	hFASH := hFAS(t, s)

	// Unknown token → TOKEN_INVALID.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokNope").WillReturnError(sql.ErrNoRows)
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokNope/revoke", nil, h), CodeTokenInvalid)

	// Lookup dead → DB_UNAVAILABLE.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokNope").WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokNope/revoke", nil, h), CodeDBUnavailable)

	// FAS token outside its groups → NOT_OWNER, no Tx.
	rpxExpectAuthFAS(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokF").
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_id", "session_id",
			"session_topic_id", "child_id"}).
			AddRow(9, int64(1), int64(2), int64(5), int64(30)))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(99))
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokF/revoke", nil, hFASH), CodeNotOwner)

	// Lost the revoke race (0 rows) → TOKEN_INVALID with rollback.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokF").
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_id", "session_id",
			"session_topic_id", "child_id"}).
			AddRow(9, int64(1), int64(2), int64(5), int64(30)))
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE public_links SET revoked_at = NOW() WHERE id = ?")).
		WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokF/revoke", nil, h), CodeTokenInvalid)

	// UPDATE dead → rollback + DB_UNAVAILABLE.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokF").
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_id", "session_id",
			"session_topic_id", "child_id"}).
			AddRow(9, int64(1), int64(2), int64(5), int64(30)))
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE public_links SET revoked_at = NOW() WHERE id = ?")).
		WithArgs(int64(9)).WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokF/revoke", nil, h), CodeDBUnavailable)

	// Begin dead → DB_UNAVAILABLE.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokF").
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_id", "session_id",
			"session_topic_id", "child_id"}).
			AddRow(9, int64(1), int64(2), int64(5), int64(30)))
	mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokF/revoke", nil, h), CodeDBUnavailable)
}

// TestRpxLinkListTroubleFailsDB: the active-link listing maps query and
// scan trouble to DB_UNAVAILABLE instead of a truncated list.
func TestRpxLinkListTroubleFailsDB(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	listPrefix := func() {
		rpxExpectAuthAdmin(mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
			WithArgs(int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	}

	listPrefix()
	mock.ExpectQuery(q("WHERE session_id = ? AND session_topic_id = ? AND child_id = ? AND revoked_at IS NULL")).
		WithArgs(int64(2), int64(5), int64(30), int64(1)).
		WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		doReq(t, app, "GET", "/api/sessions/2/topics/5/children/30/public-links", "", h),
		CodeDBUnavailable)

	listPrefix()
	mock.ExpectQuery(q("WHERE session_id = ? AND session_topic_id = ? AND child_id = ? AND revoked_at IS NULL")).
		WithArgs(int64(2), int64(5), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"kind", "token", "created_at"}).
			AddRow("REPORT", "tokR", "not-a-timestamp"))
	requireErrEnvelope(t,
		doReq(t, app, "GET", "/api/sessions/2/topics/5/children/30/public-links", "", h),
		CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// WA send: single
// ---------------------------------------------------------------------------

// TestRpxSendReportDeliveredToMotherWASent: a fully-gated send delivers via
// OpenWA, stamps the SENT outbox row (payload_ref = report URL) and answers
// {status:SENT, message_id}; unchanged links publish nothing.
func TestRpxSendReportDeliveredToMotherWASent(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-send-1"
	stub, waURL := startWAStub(t, http.StatusOK)
	s.cfg.OpenwaBaseURL = waURL
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	rpxExpectSendGates(mock, "OPENED")
	rpxExpectLinkPairExisting(mock)
	mock.ExpectExec(q("INSERT INTO wa_outbox")).
		WithArgs(int64(1), "+628111111111", "http://localhost:8002/view/report?token=tokR",
			"SENT", "msg-1", nil, "rpx-send-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	rpxExpectIdemRecord(mock, "POST "+sendReportPath, "rpx-send-1")

	res := jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "SENT", data["status"])
	require.Equal(t, "msg-1", data["message_id"])
	require.EqualValues(t, 1, stub.calls.Load(), "exactly one OpenWA round-trip")
	covNoFrame(t, sub, "no link was created → no link signal")
}

// TestRpxSendReportCreatedReportLinkPublishesFrame: a missing REPORT link
// is created inside the deliver Tx and its report-link.changed fires (single
// send publishes per created link).
func TestRpxSendReportCreatedReportLinkPublishesFrame(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-send-2"
	stub, waURL := startWAStub(t, http.StatusOK)
	s.cfg.OpenwaBaseURL = waURL
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	rpxExpectSendGates(mock, "OPENED")
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30), "REPORT").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(q("INSERT INTO public_links")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(q("SELECT token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30), "GALLERY").
		WillReturnRows(sqlmock.NewRows([]string{"token"}).AddRow("tokG"))
	mock.ExpectCommit()
	mock.ExpectExec(q("INSERT INTO wa_outbox")).
		WithArgs(int64(1), "+628111111111", sqlmock.AnyArg(), "SENT", "msg-1", nil, "rpx-send-2").
		WillReturnResult(sqlmock.NewResult(1, 1))
	rpxExpectIdemRecord(mock, "POST "+sendReportPath, "rpx-send-2")

	res := jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "SENT", data["status"])
	require.EqualValues(t, 1, stub.calls.Load(), "one OpenWA round-trip for the fresh report URL")
	created := covNextFrame(t, sub, "created report link frame")
	require.Equal(t, "report-link.changed", created.Entity)
	require.Equal(t, "created", created.Action)
	require.Equal(t, int64(30), created.ChildID)
	covNoFrame(t, sub, "only the created link publishes")
}

// TestRpxSendReportWAFailedStampsFailedOutbox: an unreachable OpenWA
// (500) retries once and fails the send: the outbox row records FAILED with
// the error and the envelope is a success carrying the FAILED status +
// WA_FAILED code (per-item semantics, never a top-level envelope).
func TestRpxSendReportWAFailedStampsFailedOutbox(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-send-3"
	stub, waURL := startWAStub(t, http.StatusInternalServerError)
	s.cfg.OpenwaBaseURL = waURL

	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	rpxExpectSendGates(mock, "OPENED")
	rpxExpectLinkPairExisting(mock)
	mock.ExpectExec(q("INSERT INTO wa_outbox")).
		WithArgs(int64(1), "+628111111111", "http://localhost:8002/view/report?token=tokR",
			"FAILED", nil, sqlmock.AnyArg(), "rpx-send-3").
		WillReturnResult(sqlmock.NewResult(1, 1))
	rpxExpectIdemRecord(mock, "POST "+sendReportPath, "rpx-send-3")

	res := jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "FAILED", data["status"])
	require.Equal(t, CodeWAFailed, data["code"])
	require.EqualValues(t, 2, stub.calls.Load(), "OpenWA send retries exactly once")
}

// TestRpxSendReportDeliverBeginFailureIsNested: link-Tx trouble inside
// deliverReport never fails the request — it answers the success envelope
// with a nested FAILED/DB_UNAVAILABLE (the claim stays recorded).
func TestRpxSendReportDeliverBeginFailureIsNested(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-send-4"

	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	rpxExpectSendGates(mock, "OPENED")
	mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRecord(mock, "POST "+sendReportPath, "rpx-send-4")

	res := jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "FAILED", data["status"])
	require.Equal(t, CodeDBUnavailable, data["code"])
}

// TestRpxSendReportGateAndTargetBranches: gate failures answer their codes
// (top-level DB trouble releases the claim), and a `to` outside the
// child's registered numbers is VALIDATION before any send.
func TestRpxSendReportGateAndTargetBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-send-5"
	const route = "POST " + sendReportPath

	gatePrefix := func() {
		rpxExpectAuthAdmin(mock)
		rpxExpectIdemClaim(mock)
	}

	// Unknown session → NOT_FOUND.
	gatePrefix()
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
	rpxExpectIdemRecord(mock, route, "rpx-send-5")
	requireErrEnvelope(t, jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h), CodeNotFound)

	// Unknown child → NOT_FOUND.
	gatePrefix()
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT mother_wa, father_wa, is_active FROM children")).
		WithArgs(int64(30), int64(1)).WillReturnError(sql.ErrNoRows)
	rpxExpectIdemRecord(mock, route, "rpx-send-5")
	requireErrEnvelope(t, jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h), CodeNotFound)

	// Inactive child → CHILD_INACTIVE.
	gatePrefix()
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT mother_wa, father_wa, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "father_wa", "is_active"}).
			AddRow("+628111111111", nil, false))
	rpxExpectIdemRecord(mock, route, "rpx-send-5")
	requireErrEnvelope(t, jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h), CodeChildInactive)

	// Topic linkage dead (DB trouble in the gate) → TOP-level DB_UNAVAILABLE
	// with the claim released.
	gatePrefix()
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-send-5")
	requireErrEnvelope(t, jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h), CodeDBUnavailable)

	// Unregistered override → VALIDATION before any deliver work.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	rpxExpectSendGates(mock, "OPENED")
	rpxExpectIdemRecord(mock, route, "rpx-send-5")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", sendReportPath, map[string]any{"to": "+628999999999"}, h),
		CodeValidation)
}

// ---------------------------------------------------------------------------
// WA send: batch
// ---------------------------------------------------------------------------

// TestRpxBatchSendMixedItemsAndFrames: one batch carries a per-item gate
// failure (VALIDATION override) and one delivered item; the readiness
// session snapshot loads ONCE for the whole request, sends happen without
// jitter when only one item sends, and exactly the three spec'd frames fire
// (session batch signal + public report/gallery refetch for the SENT child).
func TestRpxBatchSendMixedItemsAndFrames(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-batch-1"
	stub, waURL := startWAStub(t, http.StatusOK)
	s.cfg.OpenwaBaseURL = waURL
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	rpxExpectAuthAdmin(mock)
	rpxExpectBatchReplayProbe(mock, "POST "+rpxBatchSendPath, "rpx-batch-1")
	rpxExpectIdemClaim(mock)

	batchGates := func(loadSnapshot bool) {
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
			WithArgs(int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
		mock.ExpectQuery(q("SELECT mother_wa, father_wa, is_active FROM children")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "father_wa", "is_active"}).
				AddRow("+628111111111", nil, true))
		if loadSnapshot {
			mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
				WithArgs(int64(2), int64(1)).
				WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id", "activity_name_snap"}).
					AddRow(5, 11, "Aktivitas A"))
		}
		mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}).AddRow(11))
	}

	// Item 1: gate passes but the override is unregistered → per-item VALIDATION.
	batchGates(true)
	// Item 2: same child, no override → delivered; the snapshot is REUSED
	// (no second activities query — the cache assertion lives in sqlmock).
	batchGates(false)
	rpxExpectLinkPairExisting(mock)
	mock.ExpectExec(q("INSERT INTO wa_outbox")).
		WithArgs(int64(1), "+628111111111", "http://localhost:8002/view/report?token=tokR",
			"SENT", "msg-1", nil, "rpx-batch-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	rpxExpectIdemRecord(mock, "POST "+rpxBatchSendPath, "rpx-batch-1")

	res := jsonReq(t, app, "POST", rpxBatchSendPath, map[string]any{
		"items": []map[string]any{
			{"session_topic_id": 5, "child_id": 30, "to": "+628999999999"},
			{"session_topic_id": 5, "child_id": 30},
		},
	}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	items := data["items"].([]any)
	require.Len(t, items, 2)
	first := items[0].(map[string]any)
	require.Equal(t, "FAILED", first["status"])
	require.Equal(t, CodeValidation, first["code"])
	require.Equal(t, float64(5), first["session_topic_id"])
	require.Equal(t, float64(30), first["child_id"])
	second := items[1].(map[string]any)
	require.Equal(t, "SENT", second["status"])
	require.Equal(t, "msg-1", second["message_id"])
	require.EqualValues(t, 1, stub.calls.Load(), "only the deliverable item hits OpenWA")

	require.Equal(t, "batch", covNextFrame(t, sub, "session-level batch signal").Action)
	require.Equal(t, "report.changed", covNextFrame(t, sub, "public report refetch").Entity)
	gallery := covNextFrame(t, sub, "public gallery refetch")
	require.Equal(t, "gallery.changed", gallery.Entity)
	require.Zero(t, gallery.Stid, "gallery.changed routes without Stid (all topics)")
	require.Equal(t, int64(30), gallery.ChildID)
	covNoFrame(t, sub, "exactly three frames per mixed batch")
}

// TestRpxBatchSendGateFailedItemStillSignalsSession: a gate-blocked item
// becomes a per-item FAILED result; the session-level batch frame still
// fires (no public frames — nothing sent).
func TestRpxBatchSendGateFailedItemStillSignalsSession(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-batch-2"
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	rpxExpectAuthAdmin(mock)
	rpxExpectBatchReplayProbe(mock, "POST "+rpxBatchSendPath, "rpx-batch-2")
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	rpxExpectIdemRecord(mock, "POST "+rpxBatchSendPath, "rpx-batch-2")

	res := jsonReq(t, app, "POST", rpxBatchSendPath, map[string]any{
		"items": []map[string]any{{"session_topic_id": 5, "child_id": 30}},
	}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	items := data["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	require.Equal(t, "FAILED", item["status"])
	require.Equal(t, CodeSessionLocked, item["code"], "DRAFT sessions never send (F-12)")

	batchFrame := covNextFrame(t, sub, "batch signal")
	require.Equal(t, "report-link.changed", batchFrame.Entity)
	require.Equal(t, "batch", batchFrame.Action)
	covNoFrame(t, sub, "no public frames without a SENT item")
}

// TestRpxBatchSendValidationAndEmpty: >10 items is VALIDATION before any
// SQL (client chunks, F-12); an empty batch answers the empty item list
// without a loop or any frame.
func TestRpxBatchSendValidationAndEmpty(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	// 11 items → VALIDATION, auth-only SQL.
	rpxExpectAuthAdmin(mock)
	items := make([]map[string]any, 11)
	for i := range items {
		items[i] = map[string]any{"session_topic_id": 5, "child_id": 30}
	}
	res := jsonReq(t, app, "POST", rpxBatchSendPath, map[string]any{"items": items}, h)
	requireErrEnvelope(t, res, CodeValidation)

	// Empty batch → 200 {items:[]} and zero frames.
	rpxExpectAuthAdmin(mock)
	rpxExpectBatchReplayProbe(mock, "POST "+rpxBatchSendPath, "rpx-batch-3")
	rpxExpectIdemClaim(mock)
	rpxExpectIdemRecord(mock, "POST "+rpxBatchSendPath, "rpx-batch-3")
	h["X-Idempotency-Key"] = "rpx-batch-3"
	res = jsonReq(t, app, "POST", rpxBatchSendPath, map[string]any{"items": []any{}}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, []any{}, data["items"], "no items in → empty list out")
	covNoFrame(t, sub, "an empty batch publishes nothing")
}

// TestRpxBatchSendCrashReplayReadsBackRecordedItems (B1b / K6, 06 §3): a
// first attempt that died mid-loop leaves its claim NULL with rows already
// in wa_outbox — a same-key retry answers those recorded per-item statuses
// in the normal batch shape with NO claim, NO gates, NO OpenWA call and no
// frame (any of those would fail the mock — that IS the no-double-send
// assertion).
func TestRpxBatchSendCrashReplayReadsBackRecordedItems(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-batch-crash-1"
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	rpxExpectAuthAdmin(mock)
	// The probe finds the crashed attempt's NULL claim…
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), "POST "+rpxBatchSendPath, "rpx-batch-crash-1").
		WillReturnRows(sqlmock.NewRows([]string{"response"}).AddRow(nil))
	// …and the read-back maps the recorded outbox rows through public_links.
	mock.ExpectQuery(q("FROM wa_outbox o")).
		WithArgs(int64(1), "rpx-batch-crash-1", int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "child_id", "status", "message_id"}).
			AddRow(5, 30, "SENT", "msg-crash-1").
			AddRow(5, 31, "FAILED", nil))

	res := jsonReq(t, app, "POST", rpxBatchSendPath, map[string]any{
		"items": []map[string]any{
			{"session_topic_id": 5, "child_id": 30},
			{"session_topic_id": 5, "child_id": 31},
		},
	}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	items := data["items"].([]any)
	require.Len(t, items, 2, "one recorded result per outbox row: %s", res.body)
	first := items[0].(map[string]any)
	require.Equal(t, float64(5), first["session_topic_id"])
	require.Equal(t, float64(30), first["child_id"])
	require.Equal(t, "SENT", first["status"])
	require.Equal(t, "msg-crash-1", first["message_id"])
	second := items[1].(map[string]any)
	require.Equal(t, float64(31), second["child_id"])
	require.Equal(t, "FAILED", second["status"])
	require.Equal(t, CodeWAFailed, second["code"])
	covNoFrame(t, sub, "a replayed batch publishes nothing")
}

// TestRpxBatchSendCrashReplayWithoutRowsStandsInProgress (B1b): the NULL
// claim with ZERO recorded outbox rows keeps the old behavior — the wrapper
// answers IN_PROGRESS and the client re-sends the same key (06 §3).
func TestRpxBatchSendCrashReplayWithoutRowsStandsInProgress(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-batch-crash-2"

	rpxExpectAuthAdmin(mock)
	// NULL claim, no outbox rows…
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), "POST "+rpxBatchSendPath, "rpx-batch-crash-2").
		WillReturnRows(sqlmock.NewRows([]string{"response"}).AddRow(nil))
	mock.ExpectQuery(q("FROM wa_outbox o")).
		WithArgs(int64(1), "rpx-batch-crash-2", int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "child_id", "status", "message_id"}))
	// …and the wrapper replays IN_PROGRESS without executing the loop.
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), "POST "+rpxBatchSendPath, "rpx-batch-crash-2").
		WillReturnRows(sqlmock.NewRows([]string{"response"}).AddRow(nil))

	res := jsonReq(t, app, "POST", rpxBatchSendPath, map[string]any{
		"items": []map[string]any{{"session_topic_id": 5, "child_id": 30}},
	}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "IN_PROGRESS", data["status"])
}

// ---------------------------------------------------------------------------
// Session ops matrix
// ---------------------------------------------------------------------------

// TestRpxSessionReportsFailureBranches: malformed sid, invisible sessions
// (FAS scope), dead lookups and dead batch queries map to NOT_FOUND /
// DB_UNAVAILABLE instead of a fabricated matrix.
func TestRpxSessionReportsFailureBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	hFASH := hFAS(t, s)

	// Malformed sid → NOT_FOUND.
	rpxExpectAuthAdmin(mock)
	requireErrEnvelope(t,
		doReq(t, app, "GET", "/api/sessions/xx/reports", "", h), CodeNotFound)

	// FAS session outside its own groups → NOT_FOUND (no matrix leak).
	rpxExpectAuthFAS(mock)
	mock.ExpectQuery(q("SELECT id, session_id FROM session_groups")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).AddRow(10, 7))
	requireErrEnvelope(t,
		doReq(t, app, "GET", rpxSessionReportsPath, "", hFASH), CodeNotFound)

	// FAS scope lookup dead → DB_UNAVAILABLE.
	rpxExpectAuthFAS(mock)
	mock.ExpectQuery(q("SELECT id, session_id FROM session_groups")).
		WithArgs(int64(3), int64(1)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		doReq(t, app, "GET", rpxSessionReportsPath, "", hFASH), CodeDBUnavailable)

	// Unknown session → NOT_FOUND.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
	requireErrEnvelope(t,
		doReq(t, app, "GET", rpxSessionReportsPath, "", h), CodeNotFound)

	// Status lookup dead → DB_UNAVAILABLE.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		doReq(t, app, "GET", rpxSessionReportsPath, "", h), CodeDBUnavailable)

	// Topics query dead → DB_UNAVAILABLE.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT id, topic_name_snap FROM session_topics")).
		WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		doReq(t, app, "GET", rpxSessionReportsPath, "", h), CodeDBUnavailable)

	// Activity-set query dead → DB_UNAVAILABLE.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT id, topic_name_snap FROM session_topics")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_name_snap"}))
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM session_activities")).
		WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		doReq(t, app, "GET", rpxSessionReportsPath, "", h), CodeDBUnavailable)

	// Active-links query dead (last batch query) → DB_UNAVAILABLE.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT id, topic_name_snap FROM session_topics")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_name_snap"}).
			AddRow(5, "Topik A"))
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM session_activities")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
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
	mock.ExpectQuery(q("AND is_candidate = TRUE")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id"}))
	mock.ExpectQuery(q("SELECT child_id, session_topic_id, kind, token FROM public_links")).
		WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		doReq(t, app, "GET", rpxSessionReportsPath, "", h), CodeDBUnavailable)
}

// TestRpxSessionReportsFASOwnGroupsScoped: a FAS matrix only joins members
// of its own groups (the IN-scope member query with the exact group ids).
func TestRpxSessionReportsFASOwnGroupsScoped(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	hFASH := hFAS(t, s)

	rpxExpectAuthFAS(mock)
	mock.ExpectQuery(q("SELECT id, session_id FROM session_groups")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).AddRow(10, 2))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT id, topic_name_snap FROM session_topics")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_name_snap"}).
			AddRow(5, "Topik A"))
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM session_activities")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}).AddRow(11, 5))
	// The member set is restricted to the facilitator's own group 10.
	mock.ExpectQuery(q("SELECT sm.child_id, sm.group_id, c.name")).
		WithArgs(int64(2), int64(1), int64(10)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "group_id", "name"}).
			AddRow(30, 10, "Ayu"))
	mock.ExpectQuery(q("SELECT child_id, session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_activity_id"}).
			AddRow(30, 11))
	mock.ExpectQuery(q("SELECT child_id, session_topic_id FROM narratives")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id"}))
	mock.ExpectQuery(q("SELECT child_id, session_topic_id, COUNT(*)")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id", "n"}))
	mock.ExpectQuery(q("SELECT DISTINCT child_id, session_topic_id FROM photos")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id"}).
			AddRow(30, 5))
	mock.ExpectQuery(q("AND is_candidate = TRUE")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id"}).
			AddRow(30, 5))
	mock.ExpectQuery(q("SELECT child_id, session_topic_id, kind, token FROM public_links")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id", "kind", "token"}))

	res := doReq(t, app, "GET", rpxSessionReportsPath, "", hFASH)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 1, "own-group member × topic row")
	row := data[0].(map[string]any)
	require.Equal(t, float64(30), row["child_id"])
	require.Equal(t, "Ayu", row["child_name"])
	require.Equal(t, true, row["complete"], "activity 11 starred")
	require.Equal(t, true, row["candidate_photo"],
		"durable candidate present → explicit true")
}

// TestRpxSessionReportsEveryBatchQueryTroubleFailsDB: for EACH of the nine
// batch set queries a scan failure (bad column type) and an iteration
// failure (RowError) map to DB_UNAVAILABLE — the matrix never renders from
// half-read sets.
func TestRpxSessionReportsEveryBatchQueryTroubleFailsDB(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	type spec struct {
		frag string
		cols []string
		good []driver.Value
		bad  []driver.Value
	}
	specs := []spec{
		{q("SELECT id, topic_name_snap FROM session_topics"),
			[]string{"id", "topic_name_snap"},
			[]driver.Value{int64(5), "Topik A"}, []driver.Value{"x", "Topik A"}},
		{q("SELECT id, session_topic_id FROM session_activities"),
			[]string{"id", "session_topic_id"},
			[]driver.Value{int64(11), int64(5)}, []driver.Value{"x", int64(5)}},
		{q("SELECT sm.child_id, sm.group_id, c.name"),
			[]string{"child_id", "group_id", "name"},
			[]driver.Value{int64(30), int64(10), "Ayu"}, []driver.Value{"x", int64(10), "Ayu"}},
		{q("SELECT child_id, session_activity_id FROM assessments"),
			[]string{"child_id", "session_activity_id"},
			[]driver.Value{int64(30), int64(11)}, []driver.Value{"x", int64(11)}},
		{q("SELECT child_id, session_topic_id FROM narratives"),
			[]string{"child_id", "session_topic_id"},
			[]driver.Value{int64(30), int64(5)}, []driver.Value{"x", int64(5)}},
		{q("SELECT child_id, session_topic_id, COUNT(*)"),
			[]string{"child_id", "session_topic_id", "n"},
			[]driver.Value{int64(30), int64(5), 2}, []driver.Value{"x", int64(5), 2}},
		{q("SELECT DISTINCT child_id, session_topic_id FROM photos"),
			[]string{"child_id", "session_topic_id"},
			[]driver.Value{int64(30), int64(5)}, []driver.Value{"x", int64(5)}},
		{q("AND is_candidate = TRUE"),
			[]string{"child_id", "session_topic_id"},
			[]driver.Value{int64(30), int64(5)}, []driver.Value{"x", int64(5)}},
		{q("SELECT child_id, session_topic_id, kind, token FROM public_links"),
			[]string{"child_id", "session_topic_id", "kind", "token"},
			[]driver.Value{int64(30), int64(5), "REPORT", "tokR"}, []driver.Value{"x", int64(5), "REPORT", "tokR"}},
	}
	// emitPrefix queues auth + status + the first i set queries succeeding
	// with empty rows (the failing query is queued by the caller).
	emitPrefix := func(i int) {
		rpxExpectAuthAdmin(mock)
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		for j := range i {
			mock.ExpectQuery(specs[j].frag).WithArgs(int64(2), int64(1)).
				WillReturnRows(sqlmock.NewRows(specs[j].cols))
		}
	}

	for i, sp := range specs {
		// Scan failure: one row with a non-numeric id.
		emitPrefix(i)
		mock.ExpectQuery(sp.frag).WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows(sp.cols).AddRow(sp.bad...))
		requireErrEnvelope(t,
			doReq(t, app, "GET", rpxSessionReportsPath, "", h), CodeDBUnavailable)

		// Iteration failure: the row stream dies mid-read.
		emitPrefix(i)
		mock.ExpectQuery(sp.frag).WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows(sp.cols).AddRow(sp.good...).
				RowError(0, errors.New("iter boom")))
		requireErrEnvelope(t,
			doReq(t, app, "GET", rpxSessionReportsPath, "", h), CodeDBUnavailable)

		// Query failure: the driver itself dies before any rows stream.
		emitPrefix(i)
		mock.ExpectQuery(sp.frag).WithArgs(int64(2), int64(1)).
			WillReturnError(errors.New("connection refused"))
		requireErrEnvelope(t,
			doReq(t, app, "GET", rpxSessionReportsPath, "", h), CodeDBUnavailable)
	}
}

// TestRpxMissionPutLookupAndCommitFailures: malformed JSON is VALIDATION
// before any domain SQL; dead membership/child/status/mission lookups map
// to DB_UNAVAILABLE, unknown rows to NOT_FOUND/VALIDATION, and a dead
// commit rolls nothing forward.
func TestRpxMissionPutLookupAndCommitFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	// Malformed body → VALIDATION (auth-only SQL).
	rpxExpectAuthAdmin(mock)
	requireErrEnvelope(t,
		doReq(t, app, "PUT", missionPutPath, "{oops", h), CodeValidation)

	// Membership lookup dead → DB_UNAVAILABLE.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{}}, h),
		CodeDBUnavailable)

	// Non-member → NOT_FOUND (no state leak).
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	requireErrEnvelope(t,
		jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{}}, h),
		CodeNotFound)

	// Topic lookup dead → DB_UNAVAILABLE.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT topic_id FROM session_topics")).
		WithArgs(int64(5), int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{}}, h),
		CodeDBUnavailable)

	// Child lookup dead → DB_UNAVAILABLE.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT topic_id FROM session_topics")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id"}).AddRow(9))
	mock.ExpectQuery(q("SELECT is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{}}, h),
		CodeDBUnavailable)

	// Unknown child → NOT_FOUND.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT topic_id FROM session_topics")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id"}).AddRow(9))
	mock.ExpectQuery(q("SELECT is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).WillReturnError(sql.ErrNoRows)
	requireErrEnvelope(t,
		jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{}}, h),
		CodeNotFound)

	// Status lookup dead / unknown session → DB_UNAVAILABLE / NOT_FOUND.
	rpxExpectMissionPutPrefix(mock, true)
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{}}, h),
		CodeDBUnavailable)
	rpxExpectMissionPutPrefix(mock, true)
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
	requireErrEnvelope(t,
		jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{}}, h),
		CodeNotFound)

	// Mission lookup dead → DB_UNAVAILABLE.
	rpxExpectMissionPutPrefix(mock, true)
	rpxExpectMissionPutStatus(mock)
	mock.ExpectQuery(q("SELECT name FROM missions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(11), int64(1)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{11}}, h),
		CodeDBUnavailable)

	// Topic-link probe dead → DB_UNAVAILABLE.
	rpxExpectMissionPutPrefix(mock, true)
	rpxExpectMissionPutStatus(mock)
	mock.ExpectQuery(q("SELECT name FROM missions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(11), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Misi 1"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics WHERE mission_id = ? AND topic_id = ?)")).
		WithArgs(int64(11), int64(9)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{11}}, h),
		CodeDBUnavailable)

	// Dead commit → DB_UNAVAILABLE (the replace never half-applies).
	rpxExpectMissionPutPrefix(mock, true)
	rpxExpectMissionPutStatus(mock)
	mock.ExpectQuery(q("SELECT name FROM missions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(11), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Misi 1"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics WHERE mission_id = ? AND topic_id = ?)")).
		WithArgs(int64(11), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"linked"}).AddRow(1))
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "OPENED")
	mock.ExpectExec(q("DELETE FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("INSERT INTO report_missions")).
		WithArgs(int64(2), int64(5), int64(30), int64(11), "Misi 1", int64(1)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit().WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		jsonReq(t, app, "PUT", missionPutPath, map[string]any{"mission_ids": []int64{11}}, h),
		CodeDBUnavailable)
}

// TestRpxNarrativePostLookupFailures: every remaining per-row lookup failure
// inside the idempotent generate maps to its gate code (recorded) or
// DB_UNAVAILABLE (claim released) — the AI is never called for them.
func TestRpxNarrativePostLookupFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-narr-post-3"
	const route = "POST " + narrativePostPath

	claim := func() {
		rpxExpectAuthAdmin(mock)
		rpxExpectIdemClaim(mock)
	}
	memberOK := func() {
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	}
	topicOK := func() {
		mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
			WithArgs(int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	}
	childOK := func() {
		mock.ExpectQuery(q("SELECT name, age, is_active FROM children WHERE id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "age", "is_active"}).AddRow("Ayu", 8, true))
	}
	joinOK := func() {
		mock.ExpectQuery(q("FROM sessions s JOIN session_topics st ON st.id = ? AND st.session_id = s.id")).
			WithArgs(int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "date", "topic_name_snap"}).
				AddRow("Sesi A", fixedTS, "Topik A"))
	}
	statusOK := func() {
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	}

	// Membership lookup dead → DB_UNAVAILABLE, claim released.
	claim()
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-narr-post-3")
	res := jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Non-member → NOT_FOUND (recorded like any gate code).
	claim()
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	rpxExpectIdemRecord(mock, route, "rpx-narr-post-3")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeNotFound)

	// Topic probe dead / unknown topic → DB_UNAVAILABLE (released) /
	// NOT_FOUND (recorded).
	claim()
	memberOK()
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-narr-post-3")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
	claim()
	memberOK()
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
	rpxExpectIdemRecord(mock, route, "rpx-narr-post-3")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeNotFound)

	// Child lookup dead → DB_UNAVAILABLE, claim released.
	claim()
	memberOK()
	topicOK()
	mock.ExpectQuery(q("SELECT name, age, is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-narr-post-3")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Session/topic snapshot dead (absent / driver trouble) → NOT_FOUND /
	// DB_UNAVAILABLE.
	claim()
	memberOK()
	topicOK()
	childOK()
	mock.ExpectQuery(q("FROM sessions s JOIN session_topics st ON st.id = ? AND st.session_id = s.id")).
		WithArgs(int64(5), int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
	rpxExpectIdemRecord(mock, route, "rpx-narr-post-3")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeNotFound)
	claim()
	memberOK()
	topicOK()
	childOK()
	mock.ExpectQuery(q("FROM sessions s JOIN session_topics st ON st.id = ? AND st.session_id = s.id")).
		WithArgs(int64(5), int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-narr-post-3")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Status probe dead (absent / driver trouble) → NOT_FOUND / DB.
	claim()
	memberOK()
	topicOK()
	childOK()
	joinOK()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
	rpxExpectIdemRecord(mock, route, "rpx-narr-post-3")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeNotFound)
	claim()
	memberOK()
	topicOK()
	childOK()
	joinOK()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-narr-post-3")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Assessment block scan trouble / dead stream → DB_UNAVAILABLE, released.
	claim()
	memberOK()
	topicOK()
	childOK()
	joinOK()
	statusOK()
	mock.ExpectQuery(q("FROM assessments a")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap", "stars", "note"}).
			AddRow("Aktivitas A", "not-a-number", nil))
	rpxExpectIdemRelease(mock, route, "rpx-narr-post-3")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
	claim()
	memberOK()
	topicOK()
	childOK()
	joinOK()
	statusOK()
	mock.ExpectQuery(q("FROM assessments a")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap", "stars", "note"}).
			AddRow("Aktivitas A", 3, nil).RowError(0, errors.New("iter boom")))
	rpxExpectIdemRelease(mock, route, "rpx-narr-post-3")
	res = jsonReq(t, app, "POST", narrativePostPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestRpxNarrativePatchLookupFailures: dead membership/topic/status probes
// map to DB_UNAVAILABLE on the manual-edit path.
func TestRpxNarrativePatchLookupFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	// Membership lookup dead → DB_UNAVAILABLE.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		jsonReq(t, app, "PATCH", rpxNarrativePatchPath, map[string]any{"text": "Cerita"}, h),
		CodeDBUnavailable)

	// Topic probe dead → DB_UNAVAILABLE.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		jsonReq(t, app, "PATCH", rpxNarrativePatchPath, map[string]any{"text": "Cerita"}, h),
		CodeDBUnavailable)

	// Status probe dead → DB_UNAVAILABLE.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		jsonReq(t, app, "PATCH", rpxNarrativePatchPath, map[string]any{"text": "Cerita"}, h),
		CodeDBUnavailable)
}

// TestRpxSendReportRemainingGateBranches: the gate's remaining mapping —
// non-member NOT_FOUND, dead status/child/readiness DB_UNAVAILABLE (claim
// released), unknown topic/child/session NOT_FOUND (recorded).
func TestRpxSendReportRemainingGateBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-send-6"
	const route = "POST " + sendReportPath

	claim := func() {
		rpxExpectAuthAdmin(mock)
		rpxExpectIdemClaim(mock)
	}
	memberOK := func() {
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	}
	statusOK := func() {
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	}
	topicOK := func() {
		mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
			WithArgs(int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	}
	send := func(want string) {
		res := jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h)
		requireErrEnvelope(t, res, want)
	}

	// Non-member → NOT_FOUND (the gate's membership code rides through).
	claim()
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	rpxExpectIdemRecord(mock, route, "rpx-send-6")
	send(CodeNotFound)

	// Dead status probe → DB_UNAVAILABLE, released.
	claim()
	memberOK()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-send-6")
	send(CodeDBUnavailable)

	// Unknown topic → NOT_FOUND (gate code, recorded).
	claim()
	memberOK()
	statusOK()
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
	rpxExpectIdemRecord(mock, route, "rpx-send-6")
	send(CodeNotFound)

	// Dead child probe → DB_UNAVAILABLE, released.
	claim()
	memberOK()
	statusOK()
	topicOK()
	mock.ExpectQuery(q("SELECT mother_wa, father_wa, is_active FROM children")).
		WithArgs(int64(30), int64(1)).WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-send-6")
	send(CodeDBUnavailable)
}

// TestRpxSendReportReadinessGateBranches: the readiness half of the gate —
// an absent snapshot or child scan is NOT_FOUND (recorded), dead probes are
// DB_UNAVAILABLE (claim released). Split from the gate test above so the
// 5/min/IP send limiter never trips inside one server.
func TestRpxSendReportReadinessGateBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-send-8"
	const route = "POST " + sendReportPath

	claim := func() {
		rpxExpectAuthAdmin(mock)
		rpxExpectIdemClaim(mock)
	}
	memberOK := func() {
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	}
	statusOK := func() {
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	}
	topicOK := func() {
		mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
			WithArgs(int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	}
	childOK := func() {
		mock.ExpectQuery(q("SELECT mother_wa, father_wa, is_active FROM children")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "father_wa", "is_active"}).
				AddRow("+628111111111", nil, true))
	}
	actsOK := func() {
		mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id", "activity_name_snap"}).
				AddRow(5, 11, "Aktivitas A"))
	}
	send := func(want string) {
		res := jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h)
		requireErrEnvelope(t, res, want)
	}

	// Readiness snapshot absent (ErrNoRows) → NOT_FOUND, recorded.
	claim()
	memberOK()
	statusOK()
	topicOK()
	childOK()
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
	rpxExpectIdemRecord(mock, route, "rpx-send-8")
	send(CodeNotFound)

	// Dead readiness snapshot → DB_UNAVAILABLE, released.
	claim()
	memberOK()
	statusOK()
	topicOK()
	childOK()
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-send-8")
	send(CodeDBUnavailable)

	// Child readiness scan absent → NOT_FOUND, recorded.
	claim()
	memberOK()
	statusOK()
	topicOK()
	childOK()
	actsOK()
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30), int64(1)).WillReturnError(sql.ErrNoRows)
	rpxExpectIdemRecord(mock, route, "rpx-send-8")
	send(CodeNotFound)

	// Dead child readiness scan → DB_UNAVAILABLE, released.
	claim()
	memberOK()
	statusOK()
	topicOK()
	childOK()
	actsOK()
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30), int64(1)).WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-send-8")
	send(CodeDBUnavailable)
}

// TestRpxSendReportDeliverCommitAndOutboxFailures: a dead link-pair commit
// nests FAILED/DB_UNAVAILABLE in the success envelope; a dead outbox write
// is audit trouble only — the send still answers SENT.
func TestRpxSendReportDeliverCommitAndOutboxFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-send-7"

	// Dead commit.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	rpxExpectSendGates(mock, "OPENED")
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30), "REPORT").
		WillReturnRows(sqlmock.NewRows([]string{"token"}).AddRow("tokR"))
	mock.ExpectQuery(q("SELECT token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30), "GALLERY").
		WillReturnRows(sqlmock.NewRows([]string{"token"}).AddRow("tokG"))
	mock.ExpectCommit().WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRecord(mock, "POST "+sendReportPath, "rpx-send-7")
	res := jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "FAILED", data["status"])
	require.Equal(t, CodeDBUnavailable, data["code"])

	// Dead outbox write: the WhatsApp message already went out — the answer
	// stays SENT (the audit row is best-effort, 05 §2).
	stub, waURL := startWAStub(t, http.StatusOK)
	s.cfg.OpenwaBaseURL = waURL
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	rpxExpectSendGates(mock, "OPENED")
	rpxExpectLinkPairExisting(mock)
	mock.ExpectExec(q("INSERT INTO wa_outbox")).
		WithArgs(int64(1), "+628111111111", sqlmock.AnyArg(), "SENT", "msg-1", nil, "rpx-send-7").
		WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRecord(mock, "POST "+sendReportPath, "rpx-send-7")
	res = jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h)
	data = requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "SENT", data["status"])
	require.EqualValues(t, 1, stub.calls.Load())
}

// TestRpxLinkCreateLookupAndCommitFailures: malformed JSON is VALIDATION
// before the claim; dead membership/topic probes and a dead commit map to
// DB_UNAVAILABLE with rollback + claim release.
func TestRpxLinkCreateLookupAndCommitFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-link-2"
	const route = "POST " + rpxLinkCreatePath

	// Malformed body → VALIDATION before the claim (auth-only SQL).
	rpxExpectAuthAdmin(mock)
	requireErrEnvelope(t,
		doReq(t, app, "POST", rpxLinkCreatePath, "{oops", h), CodeValidation)

	// Membership lookup dead → DB_UNAVAILABLE, released.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-link-2")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", rpxLinkCreatePath, map[string]any{"kind": "REPORT"}, h),
		CodeDBUnavailable)

	// Topic probe dead → DB_UNAVAILABLE, released.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-link-2")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", rpxLinkCreatePath, map[string]any{"kind": "REPORT"}, h),
		CodeDBUnavailable)

	// Dead commit → DB_UNAVAILABLE, released (no half-created pair).
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectBegin()
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
	mock.ExpectCommit().WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, route, "rpx-link-2")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", rpxLinkCreatePath, map[string]any{"kind": "REPORT"}, h),
		CodeDBUnavailable)
}

// TestRpxLinkRevokeOwnerProbeAndCommitFailures: a dead FAS ownership probe
// is DB_UNAVAILABLE; a dead commit never keeps the revocation.
func TestRpxLinkRevokeOwnerProbeAndCommitFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	hFASH := hFAS(t, s)

	// FAS + dead membership probe → DB_UNAVAILABLE.
	rpxExpectAuthFAS(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokF").
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_id", "session_id",
			"session_topic_id", "child_id"}).
			AddRow(9, int64(1), int64(2), int64(5), int64(30)))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokF/revoke", nil, hFASH), CodeDBUnavailable)

	// Dead commit → DB_UNAVAILABLE.
	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokF").
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_id", "session_id",
			"session_topic_id", "child_id"}).
			AddRow(9, int64(1), int64(2), int64(5), int64(30)))
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE public_links SET revoked_at = NOW() WHERE id = ?")).
		WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("connection refused"))
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokF/revoke", nil, h), CodeDBUnavailable)
}

// TestRpxLinkRegenerateLookupAndTxFailures: dead FAS ownership probe,
// Begin, the FOR UPDATE collection (stream + scan), the revoke sweep and
// the fresh-pair insert all roll the Tx back and release the claim; a dead
// commit keeps the old pair.
func TestRpxLinkRegenerateLookupAndTxFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-regen-2"
	hFASH := hFAS(t, s)
	hFASH["X-Idempotency-Key"] = "rpx-regen-3"
	const (
		routeFAS   = "POST /api/public-links/tokF/regenerate"
		routeBegin = "POST /api/public-links/tokB/regenerate"
		routeSweep = "POST /api/public-links/tokS/regenerate"
		routePair  = "POST /api/public-links/tokP/regenerate"
		routeCmt   = "POST /api/public-links/tokC/regenerate"
	)
	tokenRows := func(token string) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"school_id", "session_id",
			"session_topic_id", "child_id"}).
			AddRow(int64(1), int64(2), int64(5), int64(30))
	}

	// FAS + dead membership probe → DB_UNAVAILABLE, released.
	rpxExpectAuthFAS(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokF").WillReturnRows(tokenRows("tokF"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, routeFAS, "rpx-regen-3")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokF/regenerate", nil, hFASH),
		CodeDBUnavailable)

	// Dead Begin → DB_UNAVAILABLE, released.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokB").WillReturnRows(tokenRows("tokB"))
	mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, routeBegin, "rpx-regen-2")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokB/regenerate", nil, h),
		CodeDBUnavailable)

	// Dead collection stream → rollback + release.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokS").WillReturnRows(tokenRows("tokS"))
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT id, token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token"}).
			AddRow(10, "tokOldR").RowError(0, errors.New("iter boom")))
	mock.ExpectRollback()
	rpxExpectIdemRelease(mock, routeSweep, "rpx-regen-2")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokS/regenerate", nil, h),
		CodeDBUnavailable)

	// Dead revoke sweep → rollback + release.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokS").WillReturnRows(tokenRows("tokS"))
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT id, token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token"}).
			AddRow(10, "tokOldR").AddRow(11, "tokOldG"))
	mock.ExpectExec(q("UPDATE public_links SET revoked_at = NOW()")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()
	rpxExpectIdemRelease(mock, routeSweep, "rpx-regen-2")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokS/regenerate", nil, h),
		CodeDBUnavailable)

	// Dead fresh-pair insert → rollback + release.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokP").WillReturnRows(tokenRows("tokP"))
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT id, token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token"}).
			AddRow(10, "tokOldR").AddRow(11, "tokOldG"))
	mock.ExpectExec(q("UPDATE public_links SET revoked_at = NOW()")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectQuery(q("SELECT token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30), "REPORT").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(q("INSERT INTO public_links")).
		WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()
	rpxExpectIdemRelease(mock, routePair, "rpx-regen-2")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokP/regenerate", nil, h),
		CodeDBUnavailable)

	// Dead commit → DB_UNAVAILABLE, released; the old pair survives.
	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tokC").WillReturnRows(tokenRows("tokC"))
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT id, token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token"}).
			AddRow(10, "tokOldR").AddRow(11, "tokOldG"))
	mock.ExpectExec(q("UPDATE public_links SET revoked_at = NOW()")).
		WithArgs(int64(2), int64(5), int64(30)).WillReturnResult(sqlmock.NewResult(0, 2))
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
	mock.ExpectCommit().WillReturnError(errors.New("connection refused"))
	rpxExpectIdemRelease(mock, routeCmt, "rpx-regen-2")
	requireErrEnvelope(t,
		jsonReq(t, app, "POST", "/api/public-links/tokC/regenerate", nil, h),
		CodeDBUnavailable)
}

// TestRpxLinkListDeadStreamFailsDB: an iteration failure on the active-link
// stream is DB_UNAVAILABLE, never a truncated list.
func TestRpxLinkListDeadStreamFailsDB(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	rpxExpectAuthAdmin(mock)
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("WHERE session_id = ? AND session_topic_id = ? AND child_id = ? AND revoked_at IS NULL")).
		WithArgs(int64(2), int64(5), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"kind", "token", "created_at"}).
			AddRow("REPORT", "tokR", fixedTS).RowError(0, errors.New("iter boom")))
	requireErrEnvelope(t,
		doReq(t, app, "GET", "/api/sessions/2/topics/5/children/30/public-links", "", h),
		CodeDBUnavailable)
}

// TestRpxSendReportLinkPairTroubleRollsBack: a dead link-pair probe inside
// deliverReport rolls the pair Tx back and nests FAILED/DB_UNAVAILABLE in
// the success envelope.
func TestRpxSendReportLinkPairTroubleRollsBack(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)
	h["X-Idempotency-Key"] = "rpx-send-9"

	rpxExpectAuthAdmin(mock)
	rpxExpectIdemClaim(mock)
	rpxExpectSendGates(mock, "OPENED")
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT token FROM public_links")).
		WithArgs(int64(2), int64(5), int64(30), "REPORT").
		WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()
	rpxExpectIdemRecord(mock, "POST "+sendReportPath, "rpx-send-9")
	res := jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "FAILED", data["status"])
	require.Equal(t, CodeDBUnavailable, data["code"])
}

// TestRpxSendAndBatchRejectMalformedJSON: a body that will not parse is
// VALIDATION before any handler SQL on the two send routes.
func TestRpxSendAndBatchRejectMalformedJSON(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	rpxExpectAuthAdmin(mock)
	rpxExpectAuthAdmin(mock)
	requireErrEnvelope(t,
		doReq(t, app, "POST", sendReportPath, "{oops", h), CodeValidation)
	requireErrEnvelope(t,
		doReq(t, app, "POST", rpxBatchSendPath, "{oops", h), CodeValidation)
}

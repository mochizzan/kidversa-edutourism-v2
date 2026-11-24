package internal

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

const caxPutPath = "/api/sessions/2/topics/5/children/30/activities/11"

// caxExpectPutGates wires the shared P gates up to the upsert: membership →
// status → linkage → child → owner depth (all passing).
func caxExpectPutGates(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
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
}

// caxExpectStatusLockTx wires the in-Tx FOR UPDATE status re-read (B1a) that
// every guarded write now runs between ExpectBegin and its upsert — the
// authoritative SESSION_LOCKED/NOT_FOUND gate behind the order-setting pre-read.
func caxExpectStatusLockTx(mock sqlmock.Sqlmock, status string) {
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows(status))
}

// caxExpectUpsert wires the guarded upsert Tx: Begin → FOR UPDATE status
// re-read → atomic upsert → updated_at re-read → commit.
func caxExpectUpsert(mock sqlmock.Sqlmock, note any) {
	mock.ExpectBegin()
	caxExpectStatusLockTx(mock, "OPENED")
	mock.ExpectExec(q("INSERT INTO assessments")).
		WithArgs(int64(2), int64(11), int64(30), int64(1), 3, note,
			"Aktivitas A", "Ayu", int64(2)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(q("SELECT updated_at FROM assessments")).
		WithArgs(int64(11), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(fixedTS))
	mock.ExpectCommit()
}

// ---------------------------------------------------------------------------
// handleAssessmentPut — error/edge branches beyond the existing gate matrix
// ---------------------------------------------------------------------------

func TestCaxAssessmentPutErrorBranches(t *testing.T) {
	t.Run("malformed session id is not found before any sql", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		res := jsonReq(t, app, "PUT",
			"/api/sessions/abc/topics/5/children/30/activities/11",
			map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("malformed topic id is not found before any sql", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		res := jsonReq(t, app, "PUT",
			"/api/sessions/2/topics/abc/children/30/activities/11",
			map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("malformed child id is not found before any sql", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		res := jsonReq(t, app, "PUT",
			"/api/sessions/2/topics/5/children/abc/activities/11",
			map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("malformed activity id is not found before any sql", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		res := jsonReq(t, app, "PUT",
			"/api/sessions/2/topics/5/children/30/activities/abc",
			map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("SA without school header is validation before any sql", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
		res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("malformed json body is validation before any sql", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		res := doReq(t, app, "PUT", caxPutPath, `{"stars":`, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("membership read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).WillReturnError(errors.New("connection refused"))
		res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("unknown session is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
		res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("status read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
		res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("unlinked activity is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("SELECT sa.activity_name_snap FROM session_activities sa")).
			WithArgs(int64(11), int64(5), int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
		res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("linkage read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("SELECT sa.activity_name_snap FROM session_activities sa")).
			WithArgs(int64(11), int64(5), int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
		res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("vanishing child row is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("SELECT sa.activity_name_snap FROM session_activities sa")).
			WithArgs(int64(11), int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap"}).AddRow("Aktivitas A"))
		mock.ExpectQuery(q("SELECT name, is_active FROM children WHERE id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).WillReturnError(sql.ErrNoRows)
		res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("child read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("SELECT sa.activity_name_snap FROM session_activities sa")).
			WithArgs(int64(11), int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap"}).AddRow("Aktivitas A"))
		mock.ExpectQuery(q("SELECT name, is_active FROM children WHERE id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).WillReturnError(errors.New("connection refused"))
		res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("member without a group row is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("SELECT sa.activity_name_snap FROM session_activities sa")).
			WithArgs(int64(11), int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap"}).AddRow("Aktivitas A"))
		mock.ExpectQuery(q("SELECT name, is_active FROM children WHERE id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "is_active"}).AddRow("Ayu", true))
		mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members sm")).
			WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
		res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("owner read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("SELECT sa.activity_name_snap FROM session_activities sa")).
			WithArgs(int64(11), int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap"}).AddRow("Aktivitas A"))
		mock.ExpectQuery(q("SELECT name, is_active FROM children WHERE id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "is_active"}).AddRow("Ayu", true))
		mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members sm")).
			WithArgs(int64(2), int64(30)).WillReturnError(errors.New("connection refused"))
		res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("owner active check failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
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
			WithArgs(int64(3), int64(1)).WillReturnError(errors.New("connection refused"))
		res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("inactive group owner blocks for everyone", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
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
			WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(0))
		res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeGroupNoFacilitator)
	})
	t.Run("upsert failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		caxExpectPutGates(mock)
		mock.ExpectBegin()
		caxExpectStatusLockTx(mock, "OPENED")
		mock.ExpectExec(q("INSERT INTO assessments")).
			WithArgs(int64(2), int64(11), int64(30), int64(1), 3, nil,
				"Aktivitas A", "Ayu", int64(2)).
			WillReturnError(errors.New("connection refused"))
		mock.ExpectRollback()
		res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("updated_at re-read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		caxExpectPutGates(mock)
		mock.ExpectBegin()
		caxExpectStatusLockTx(mock, "OPENED")
		mock.ExpectExec(q("INSERT INTO assessments")).
			WithArgs(int64(2), int64(11), int64(30), int64(1), 3, nil,
				"Aktivitas A", "Ayu", int64(2)).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectQuery(q("SELECT updated_at FROM assessments")).
			WithArgs(int64(11), int64(30)).WillReturnError(errors.New("connection refused"))
		mock.ExpectRollback()
		res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestCaxAssessmentPutReadinessFailureKeepsSuccess: a broken readiness
// refetch after the save is logged only — the response stays the saved
// assessment and NO readiness.changed frame is published.
func TestCaxAssessmentPutReadinessFailureKeepsSuccess(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	caxExpectPutGates(mock)
	caxExpectUpsert(mock, nil)
	// The post-save readiness snapshot dies: no program/topic/badge lookups —
	// only the session activity set (wantBadges=false) — and this one fails.
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))

	res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(11), data["said"])
	require.Equal(t, float64(3), data["stars"])
	require.Equal(t, float64(2), data["assessor_id"])
	require.Equal(t, "2026-01-02T03:04:05Z", data["updated_at"])

	changed := covNextFrame(t, sub, "assessment.changed after upsert")
	require.Equal(t, "assessment.changed", changed.Entity)
	covNoFrame(t, sub, "readiness.changed is suppressed when the snapshot fails")
}

// TestCaxAssessmentPutStatusRaceRollsBack (B1a): the pre-read sees OPENED
// but the session changes state before the write Tx takes the row lock — the
// in-Tx FOR UPDATE re-read answers SESSION_LOCKED and the upsert never lands
// (rollback, no publish). DRAFT and FINISHED both lock (FL-09 guard).
func TestCaxAssessmentPutStatusRaceRollsBack(t *testing.T) {
	for _, raced := range []string{"FINISHED", "DRAFT"} {
		t.Run(raced+" between pre-read and upsert", func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			h := sessionAdminHeaders(t, s, mock)

			caxExpectPutGates(mock) // pre-read status = OPENED (gate order intact)
			mock.ExpectBegin()
			caxExpectStatusLockTx(mock, raced) // the rival transition won the lock
			mock.ExpectRollback()
			res := jsonReq(t, app, "PUT", caxPutPath, map[string]any{"stars": 3}, h)
			requireErrEnvelope(t, res, CodeSessionLocked)
		})
	}
}

// ---------------------------------------------------------------------------
// handleAssessmentsList — GET P/assessments error branches
// ---------------------------------------------------------------------------

func TestCaxAssessmentsListErrorBranches(t *testing.T) {
	listPath := "/api/sessions/2/topics/5/children/30/assessments"
	t.Run("non-member child is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
		res := doReq(t, app, "GET", listPath, "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("list query failure is db unavailable", func(t *testing.T) {
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
			WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", listPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("row scan failure is db unavailable", func(t *testing.T) {
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
				AddRow("not-an-id", 3, nil, nil, fixedTS))
		res := doReq(t, app, "GET", listPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("row iteration failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
			WithArgs(int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
		rows := sqlmock.NewRows([]string{"said", "stars", "note", "assessor_id", "updated_at"}).
			AddRow(11, 3, nil, nil, fixedTS).
			RowError(0, errors.New("connection refused"))
		mock.ExpectQuery(q("a.assessor_id, a.updated_at")).
			WithArgs(int64(2), int64(30), int64(5), int64(1)).WillReturnRows(rows)
		res := doReq(t, app, "GET", listPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// handleSessionProgress — GET /api/sessions/:sid/progress
// ---------------------------------------------------------------------------

// TestCaxSessionProgressAdminMatrix: the ADMIN path (ownGroups nil → plain
// member query) computes starred/total/complete per topic; totals rows for
// unknown topics are ignored.
func TestCaxSessionProgressAdminMatrix(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxAdminH(t, s, mock)

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT id, topic_name_snap FROM session_topics")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_name_snap"}).
			AddRow(5, "Topik A").AddRow(7, "Topik B"))
	mock.ExpectQuery(q("SELECT session_topic_id, COUNT(*) FROM session_activities")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "n"}).
			AddRow(5, 2).AddRow(7, 1).AddRow(9, 5)) // 9 is not a session topic → ignored
	mock.ExpectQuery(q("SELECT sm.child_id, sm.group_id, c.name")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "group_id", "name"}).
			AddRow(30, 10, "Ayu"))
	mock.ExpectQuery(q("SELECT a.child_id, sa.session_topic_id, COUNT(*)")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "stid", "n"}).AddRow(30, 5, 2))

	res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	children := data["children"].([]any)
	require.Len(t, children, 1)
	child := children[0].(map[string]any)
	require.Equal(t, "Ayu", child["name"])
	require.Equal(t, float64(10), child["gid"])
	topics := child["topics"].([]any)
	require.Len(t, topics, 2)
	first := topics[0].(map[string]any)
	require.Equal(t, "Topik A", first["topic"])
	require.Equal(t, float64(2), first["starred"])
	require.Equal(t, float64(2), first["total"])
	require.Equal(t, true, first["complete"], "2 of 2 starred → complete")
	second := topics[1].(map[string]any)
	require.Equal(t, "Topik B", second["topic"])
	require.Equal(t, float64(0), second["starred"])
	require.Equal(t, false, second["complete"], "0 of 1 starred → incomplete")
}

// TestCaxSessionProgressGates: malformed id, unknown session, status-read
// failure and a broken FAS scope answer their codes before the matrix runs.
func TestCaxSessionProgressGates(t *testing.T) {
	t.Run("malformed session id is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		res := doReq(t, app, "GET", "/api/sessions/abc/progress", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("unknown session is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("status read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("fas scope read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxFASH(t, s, mock)
		mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
			WithArgs(int64(3), int64(1)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestCaxSessionProgressDBErrors: every batch query of the matrix answers
// DB_UNAVAILABLE on failure (query, row scan and iteration alike).
func TestCaxSessionProgressDBErrors(t *testing.T) {
	auth := func(t *testing.T, s *Server, mock sqlmock.Sqlmock) map[string]string {
		t.Helper()
		h := CaxAdminH(t, s, mock)
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		return h
	}
	expectTopics := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("SELECT id, topic_name_snap FROM session_topics")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "topic_name_snap"}).AddRow(5, "Topik A"))
	}
	expectTotals := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("SELECT session_topic_id, COUNT(*) FROM session_activities")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "n"}))
	}
	expectMembers := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("SELECT sm.child_id, sm.group_id, c.name")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id", "group_id", "name"}).
				AddRow(30, 10, "Ayu"))
	}

	t.Run("topics query failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		mock.ExpectQuery(q("SELECT id, topic_name_snap FROM session_topics")).
			WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("topic scan failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		mock.ExpectQuery(q("SELECT id, topic_name_snap FROM session_topics")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "topic_name_snap"}).AddRow("x", "Topik A"))
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("topic iteration failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		rows := sqlmock.NewRows([]string{"id", "topic_name_snap"}).
			AddRow(5, "Topik A").RowError(0, errors.New("connection refused"))
		mock.ExpectQuery(q("SELECT id, topic_name_snap FROM session_topics")).
			WithArgs(int64(2), int64(1)).WillReturnRows(rows)
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("totals query failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		expectTopics(mock)
		mock.ExpectQuery(q("SELECT session_topic_id, COUNT(*) FROM session_activities")).
			WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("total scan failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		expectTopics(mock)
		mock.ExpectQuery(q("SELECT session_topic_id, COUNT(*) FROM session_activities")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "n"}).AddRow("x", 2))
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("total iteration failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		expectTopics(mock)
		rows := sqlmock.NewRows([]string{"session_topic_id", "n"}).
			AddRow(5, 2).RowError(0, errors.New("connection refused"))
		mock.ExpectQuery(q("SELECT session_topic_id, COUNT(*) FROM session_activities")).
			WithArgs(int64(2), int64(1)).WillReturnRows(rows)
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("member query failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		expectTopics(mock)
		expectTotals(mock)
		mock.ExpectQuery(q("SELECT sm.child_id, sm.group_id, c.name")).
			WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("member scan failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		expectTopics(mock)
		expectTotals(mock)
		mock.ExpectQuery(q("SELECT sm.child_id, sm.group_id, c.name")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id", "group_id", "name"}).
				AddRow("x", 10, "Ayu"))
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("member iteration failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		expectTopics(mock)
		expectTotals(mock)
		rows := sqlmock.NewRows([]string{"child_id", "group_id", "name"}).
			AddRow(30, 10, "Ayu").RowError(0, errors.New("connection refused"))
		mock.ExpectQuery(q("SELECT sm.child_id, sm.group_id, c.name")).
			WithArgs(int64(2), int64(1)).WillReturnRows(rows)
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("star query failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		expectTopics(mock)
		expectTotals(mock)
		expectMembers(mock)
		mock.ExpectQuery(q("SELECT a.child_id, sa.session_topic_id, COUNT(*)")).
			WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("star scan failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		expectTopics(mock)
		expectTotals(mock)
		expectMembers(mock)
		mock.ExpectQuery(q("SELECT a.child_id, sa.session_topic_id, COUNT(*)")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id", "stid", "n"}).AddRow("x", 5, 2))
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("star iteration failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		expectTopics(mock)
		expectTotals(mock)
		expectMembers(mock)
		rows := sqlmock.NewRows([]string{"child_id", "stid", "n"}).
			AddRow(30, 5, 2).RowError(0, errors.New("connection refused"))
		mock.ExpectQuery(q("SELECT a.child_id, sa.session_topic_id, COUNT(*)")).
			WithArgs(int64(2), int64(1)).WillReturnRows(rows)
		res := doReq(t, app, "GET", "/api/sessions/2/progress", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// handleSessionReadiness — GET /api/sessions/:sid/readiness
// ---------------------------------------------------------------------------

// caxExpectReadinessSnapshot wires everything up to the child scan:
// membership + topic probe + program id + activities + badge snaps + the
// live program badge.
func caxExpectReadinessSnapshot(mock sqlmock.Sqlmock, acts *sqlmock.Rows, topics *sqlmock.Rows, progBadge int) {
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
		WithArgs(int64(2), int64(1)).WillReturnRows(acts)
	mock.ExpectQuery(q("SELECT id, badge_name_snap, badge_image_snap")).
		WithArgs(int64(2), int64(1)).WillReturnRows(topics)
	mock.ExpectQuery(q("SELECT (badge_name IS NOT NULL AND badge_image_path IS NOT NULL)")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge"}).AddRow(progBadge))
}

// caxReadyActs builds the standard two-activity topic snapshot rows.
func caxReadyActs() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"session_topic_id", "id", "activity_name_snap"}).
		AddRow(5, 11, "Aktivitas A").
		AddRow(5, 12, "Aktivitas B")
}

// caxTopicBadge5 builds the badge snap with topic 5 fully defined.
func caxTopicBadge5() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "badge_name_snap", "badge_image_snap"}).
		AddRow(5, "Medali", "badges/topik.png")
}

const caxReadinessPath = "/api/sessions/2/readiness?child_id=30&session_topic_id=5"

func TestCaxSessionReadinessValidatesBeforeSQL(t *testing.T) {
	cases := []struct {
		name, target string
	}{
		{"missing child_id is validation", "/api/sessions/2/readiness?session_topic_id=5"},
		{"missing session_topic_id is validation", "/api/sessions/2/readiness?child_id=30"},
		{"non-numeric child_id is validation", "/api/sessions/2/readiness?child_id=abc&session_topic_id=5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			h := CaxAdminH(t, s, mock)
			res := doReq(t, app, "GET", tc.target, "", h)
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
	t.Run("malformed session id is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		res := doReq(t, app, "GET",
			"/api/sessions/abc/readiness?child_id=30&session_topic_id=5", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
}

func TestCaxSessionReadinessGates(t *testing.T) {
	t.Run("non-member child is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("membership read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("facilitator of a foreign group is not owner", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxFASH(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members sm")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(99))
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeNotOwner)
	})
	t.Run("ownerless group member is not owner", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxFASH(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members sm")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(nil))
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeNotOwner)
	})
	t.Run("member whose group row vanished is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxFASH(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members sm")).
			WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("owner read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxFASH(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members sm")).
			WithArgs(int64(2), int64(30)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("topic outside the session is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
			WithArgs(int64(5), int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("topic probe failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
			WithArgs(int64(5), int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestCaxSessionReadinessIncompleteBlocksProgramBadge: one unstarred
// activity → incomplete with the exact {said,name} gap, and a second
// incomplete topic blocks the program badge even though the program badge
// itself is defined.
func TestCaxSessionReadinessIncompleteBlocksProgramBadge(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxAdminH(t, s, mock)

	acts := sqlmock.NewRows([]string{"session_topic_id", "id", "activity_name_snap"}).
		AddRow(5, 11, "Aktivitas A").
		AddRow(5, 12, "Aktivitas B").
		AddRow(7, 21, "Aktivitas C")
	topics := sqlmock.NewRows([]string{"id", "badge_name_snap", "badge_image_snap"}).
		AddRow(5, "Medali", "badges/topik.png").
		AddRow(7, nil, nil)
	caxExpectReadinessSnapshot(mock, acts, topics, 1)
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}).AddRow(11))

	res := doReq(t, app, "GET", caxReadinessPath, "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, false, data["complete"])
	missing := data["missing"].([]any)
	require.Len(t, missing, 1)
	item := missing[0].(map[string]any)
	require.Equal(t, float64(12), item["said"])
	require.Equal(t, "Aktivitas B", item["name"])
	badges := data["badges"].(map[string]any)
	require.Equal(t, false, badges["topic"], "an incomplete topic earns no badge")
	require.Equal(t, false, badges["program"], "another topic's gap blocks the program badge")
}

// TestCaxSessionReadinessCompleteEarnsBothBadges: a fully starred topic with
// defined badges on both levels answers complete with an empty gap list.
func TestCaxSessionReadinessCompleteEarnsBothBadges(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxAdminH(t, s, mock)

	caxExpectReadinessSnapshot(mock, caxReadyActs(), caxTopicBadge5(), 1)
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}).AddRow(11).AddRow(12))

	res := doReq(t, app, "GET", caxReadinessPath, "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["complete"])
	require.Equal(t, []any{}, data["missing"], "no gaps → empty list, never null")
	badges := data["badges"].(map[string]any)
	require.Equal(t, true, badges["topic"])
	require.Equal(t, true, badges["program"])
}

// TestCaxSessionReadinessFASOwnGroupPassesGate: the facilitator who owns the
// child's group passes requirePChild and reads the readiness; an undefined
// program badge only flips that boolean.
func TestCaxSessionReadinessFASOwnGroupPassesGate(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxFASH(t, s, mock)

	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members sm")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(3))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"program_id"}).AddRow(9))
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(caxReadyActs())
	mock.ExpectQuery(q("SELECT id, badge_name_snap, badge_image_snap")).
		WithArgs(int64(2), int64(1)).WillReturnRows(caxTopicBadge5())
	mock.ExpectQuery(q("SELECT (badge_name IS NOT NULL AND badge_image_path IS NOT NULL)")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge"}).AddRow(0))
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}).AddRow(11).AddRow(12))

	res := doReq(t, app, "GET", caxReadinessPath, "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["complete"])
	badges := data["badges"].(map[string]any)
	require.Equal(t, true, badges["topic"])
	require.Equal(t, false, badges["program"], "a program without a badge never earns it")
}

// TestCaxSessionReadinessSnapshotFailures: each snapshot query of
// loadReadinessSession maps to DB_UNAVAILABLE; a vanished program id follows
// the sql.ErrNoRows → NOT_FOUND contract.
func TestCaxSessionReadinessSnapshotFailures(t *testing.T) {
	prefix := func(t *testing.T, s *Server, mock sqlmock.Sqlmock) map[string]string {
		t.Helper()
		h := CaxAdminH(t, s, mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
			WithArgs(int64(5), int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
		return h
	}
	t.Run("program id read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := prefix(t, s, mock)
		mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("vanishing session maps to not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := prefix(t, s, mock)
		mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("activities query failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := prefix(t, s, mock)
		mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"program_id"}).AddRow(9))
		mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("activity scan failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := prefix(t, s, mock)
		mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"program_id"}).AddRow(9))
		mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id", "activity_name_snap"}).
				AddRow(5, "x", "Aktivitas A"))
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("activity iteration failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := prefix(t, s, mock)
		mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"program_id"}).AddRow(9))
		rows := sqlmock.NewRows([]string{"session_topic_id", "id", "activity_name_snap"}).
			AddRow(5, 11, "Aktivitas A").RowError(0, errors.New("connection refused"))
		mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(rows)
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("badge snap query failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := prefix(t, s, mock)
		mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"program_id"}).AddRow(9))
		mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(caxReadyActs())
		mock.ExpectQuery(q("SELECT id, badge_name_snap, badge_image_snap")).
			WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("badge snap scan failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := prefix(t, s, mock)
		mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"program_id"}).AddRow(9))
		mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(caxReadyActs())
		mock.ExpectQuery(q("SELECT id, badge_name_snap, badge_image_snap")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "badge_name_snap", "badge_image_snap"}).
				AddRow("x", "Medali", "badges/topik.png"))
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("badge snap iteration failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := prefix(t, s, mock)
		mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"program_id"}).AddRow(9))
		mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(caxReadyActs())
		rows := sqlmock.NewRows([]string{"id", "badge_name_snap", "badge_image_snap"}).
			AddRow(5, "Medali", "badges/topik.png").RowError(0, errors.New("connection refused"))
		mock.ExpectQuery(q("SELECT id, badge_name_snap, badge_image_snap")).
			WithArgs(int64(2), int64(1)).WillReturnRows(rows)
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("program badge read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := prefix(t, s, mock)
		mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"program_id"}).AddRow(9))
		mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(caxReadyActs())
		mock.ExpectQuery(q("SELECT id, badge_name_snap, badge_image_snap")).
			WithArgs(int64(2), int64(1)).WillReturnRows(caxTopicBadge5())
		mock.ExpectQuery(q("SELECT (badge_name IS NOT NULL AND badge_image_path IS NOT NULL)")).
			WithArgs(int64(9)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestCaxSessionReadinessChildScanFailures: the child-varying scan answers
// DB_UNAVAILABLE on failure and NOT_FOUND when the badge snapshot lost the
// requested topic (the sql.ErrNoRows contract).
func TestCaxSessionReadinessChildScanFailures(t *testing.T) {
	t.Run("assessments query failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		caxExpectReadinessSnapshot(mock, caxReadyActs(), caxTopicBadge5(), 1)
		mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
			WithArgs(int64(2), int64(30), int64(1)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("assessments scan failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		caxExpectReadinessSnapshot(mock, caxReadyActs(), caxTopicBadge5(), 1)
		mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}).AddRow("x"))
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("assessments iteration failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		caxExpectReadinessSnapshot(mock, caxReadyActs(), caxTopicBadge5(), 1)
		rows := sqlmock.NewRows([]string{"session_activity_id"}).
			AddRow(11).RowError(0, errors.New("connection refused"))
		mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
			WithArgs(int64(2), int64(30), int64(1)).WillReturnRows(rows)
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("topic lost from the badge snapshot is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		// The probe still sees topic 5, but the snapshot only carries topic 7
		// (concurrent wipe): readinessChild answers sql.ErrNoRows → NOT_FOUND.
		onlyTopic7 := sqlmock.NewRows([]string{"id", "badge_name_snap", "badge_image_snap"}).
			AddRow(7, nil, nil)
		caxExpectReadinessSnapshot(mock, caxReadyActs(), onlyTopic7, 1)
		res := doReq(t, app, "GET", caxReadinessPath, "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
}

package internal

import (
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// length_caps_test.go — finding 005e boundary tests for the named cap
// constants in validate.go (maxPersonNameLen=160, maxShortTextLen=200,
// maxDescriptionLen=10000, maxNoteLen=2000). Values are pinned as LITERALS
// on purpose: a constant silently widened (160 → 180) fails "cap+1 is
// VALIDATION" here, a narrowing fails "value AT the cap passes" — the
// tests assert the behaviour, never the constant (a tautology).
// Representative create AND update paths per the review's F05-07 note
// (sessions/schools were the untested slices).

// repeat helpers keep the table below readable.
func atCap(n int) string { return strings.Repeat("n", n) }

// ---------------------------------------------------------------------------
// 200 — school names (schools.school_name): create + update
// ---------------------------------------------------------------------------

func TestLengthCapSchoolNameCreateBoundary(t *testing.T) {
	t.Run("200 chars passes", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectSAAuth(mock, 0)
		mock.ExpectExec(q("INSERT INTO schools (school_name, country, city, is_active) VALUES (?, ?, ?, TRUE)")).
			WithArgs(atCap(200), "Indonesia", nil).
			WillReturnResult(sqlmock.NewResult(7, 1))
		mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(7)).
			WillReturnRows(sqlSchoolItemRow(7, atCap(200), "Indonesia", true))

		res := jsonReq(t, app, "POST", "/api/schools",
			map[string]any{"school_name": atCap(200)}, hSABare(t, s))
		school := requireDataEnvelope(t, res).(map[string]any)["school"].(map[string]any)
		require.Len(t, school["school_name"].(string), 200, "at-cap name persists in full")
	})

	t.Run("201 chars is VALIDATION before any write", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectSAAuth(mock, 0) // no INSERT may follow (stray write fails the mock)
		res := jsonReq(t, app, "POST", "/api/schools",
			map[string]any{"school_name": atCap(201)}, hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})
}

func TestLengthCapSchoolNameUpdateBoundary(t *testing.T) {
	t.Run("200 chars passes", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectSAAuth(mock, 0)
		mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
			WillReturnRows(sqlSchoolItemRow(3, "Lama", "Indonesia", true))
		mock.ExpectExec(q("UPDATE schools SET school_name = ? WHERE id = ?")).
			WithArgs(atCap(200), int64(3)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
			WillReturnRows(sqlSchoolItemRow(3, atCap(200), "Indonesia", true))

		res := jsonReq(t, app, "PATCH", "/api/schools/3",
			map[string]any{"school_name": atCap(200)}, hSABare(t, s))
		school := requireDataEnvelope(t, res).(map[string]any)["school"].(map[string]any)
		require.Len(t, school["school_name"].(string), 200, "at-cap rename persists in full")
	})

	t.Run("201 chars is VALIDATION after the load, no UPDATE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectSAAuth(mock, 0)
		mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
			WillReturnRows(sqlSchoolItemRow(3, "Lama", "Indonesia", true))
		// No UPDATE expectation: a stray write fails the mock.
		res := jsonReq(t, app, "PATCH", "/api/schools/3",
			map[string]any{"school_name": atCap(201)}, hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})
}

// ---------------------------------------------------------------------------
// 200 / 10000 — session name + description: create + update
// ---------------------------------------------------------------------------

func TestLengthCapSessionCreateBoundary(t *testing.T) {
	t.Run("200-char name and 10000-char description pass", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		h["X-Idempotency-Key"] = "caps-create-1"

		mock.ExpectExec(q("DELETE FROM idempotency_keys")).
			WithArgs(int64(1), "POST /api/sessions", "caps-create-1").
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(q("INSERT INTO idempotency_keys")).
			WithArgs(int64(1), "POST /api/sessions", "caps-create-1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectBegin()
		mock.ExpectQuery(q("FROM programs WHERE id = ? AND school_id = ? AND is_active = TRUE")).
			WithArgs(int64(9), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Program A"))
		mock.ExpectQuery(q("WHERE program_id = ? AND school_id = ? AND is_active = TRUE")).
			WithArgs(int64(9), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(2))
		mock.ExpectQuery(q("WHERE t.program_id = ? AND t.school_id = ? AND t.is_active = TRUE")).
			WithArgs(int64(9), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
		mock.ExpectExec(q("INSERT INTO sessions")).
			WillReturnResult(sqlmock.NewResult(41, 1))
		mock.ExpectQuery(q("FROM topics WHERE program_id = ?")).
			WithArgs(int64(9), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "sort_order", "badge_name", "badge_image_path"}).
				AddRow(10, "Topik A", 1, nil, nil))
		mock.ExpectExec(q("INSERT INTO session_topics")).
			WillReturnResult(sqlmock.NewResult(51, 1))
		mock.ExpectQuery(q("JOIN session_topics st ON st.topic_id = a.topic_id")).
			WithArgs(int64(41), int64(1), int64(9), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "activity_id", "name", "indicator", "sort_order"}).
				AddRow(51, 101, "Aktivitas A", nil, 1))
		mock.ExpectExec(q("INSERT INTO session_activities")).
			WillReturnResult(sqlmock.NewResult(61, 1))
		// No groups → no children lookup, no member inserts.
		mock.ExpectCommit()
		mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
			WithArgs(sqlmock.AnyArg(), int64(1), "POST /api/sessions", "caps-create-1").
			WillReturnResult(sqlmock.NewResult(0, 1))

		res := jsonReq(t, app, "POST", "/api/sessions", map[string]any{
			"program_id": 9, "name": atCap(200), "description": atCap(10000),
			"date": "2026-01-05", "start_time": "09:00:00", "end_time": "11:00:00",
			"location": "Lab",
		}, h)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, float64(41), data["session_id"],
			"at-cap name+description must create the session")
	})

	t.Run("201-char name is VALIDATION before the idempotency claim", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)

		// No X-Idempotency-Key, no SQL: the cap check runs before s.idempotent.
		res := jsonReq(t, app, "POST", "/api/sessions", map[string]any{
			"program_id": 9, "name": atCap(201), "date": "2026-01-05",
			"location": "Lab",
		}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("10001-char description is VALIDATION", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)

		res := jsonReq(t, app, "POST", "/api/sessions", map[string]any{
			"program_id": 9, "name": "Sesi A", "description": atCap(10001),
			"date": "2026-01-05", "location": "Lab",
		}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
}

func TestLengthCapSessionUpdateBoundary(t *testing.T) {
	loadDraft := func(t *testing.T, mock sqlmock.Sqlmock) {
		t.Helper()
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "program_id",
				"program_name_snap", "name", "description", "date", "start_time",
				"end_time", "location", "status", "created_by", "created_at"}).
				AddRow(2, 9, "Program A", "Sesi A", nil, fixedTS, nil, nil, "Lab",
					"DRAFT", int64(2), fixedTS))
	}

	t.Run("200-char name passes", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)

		loadDraft(t, mock)
		mock.ExpectExec(q("UPDATE sessions SET name = ? WHERE id = ?")).
			WithArgs(atCap(200), int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"name": atCap(200)}, h)
		sess := requireDataEnvelope(t, res).(map[string]any)["session"].(map[string]any)
		require.Len(t, sess["name"].(string), 200, "at-cap rename persists in full")
	})

	t.Run("201-char name is VALIDATION after the load, no UPDATE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)

		loadDraft(t, mock)
		// No UPDATE expectation: a stray write fails the mock.
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"name": atCap(201)}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("10001-char description is VALIDATION after the load", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)

		loadDraft(t, mock)
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"description": atCap(10001)}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
}

// ---------------------------------------------------------------------------
// 2000 — assessment note: update path (PUT P/activities/:said)
// ---------------------------------------------------------------------------

func TestLengthCapAssessmentNoteBoundary(t *testing.T) {
	t.Run("2000-char note passes", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)

		note := atCap(2000)
		// Gate order mirrors TestAssessmentPutHappyPathUpsertsAndPublishes.
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
		// Readiness snapshot: both activities starred → complete=true.
		mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id", "activity_name_snap"}).
				AddRow(5, 11, "Aktivitas A").
				AddRow(5, 12, "Aktivitas B"))
		mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}).AddRow(11).AddRow(12))

		res := jsonReq(t, app, "PUT", "/api/sessions/2/topics/5/children/30/activities/11",
			map[string]any{"stars": 3, "note": note}, h)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Len(t, data["note"].(string), 2000, "at-cap note persists in full")
	})

	t.Run("2001-char note is VALIDATION before any SQL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)

		res := jsonReq(t, app, "PUT", "/api/sessions/2/topics/5/children/30/activities/11",
			map[string]any{"stars": 3, "note": atCap(2001)}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
}

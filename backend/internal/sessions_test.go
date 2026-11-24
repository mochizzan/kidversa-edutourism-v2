package internal

import (
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// sessionAdminHeaders wires the common guarded-transition context: an ADMIN
// caller (school 1) whose token passes authed().
func sessionAdminHeaders(t *testing.T, s *Server, mock sqlmock.Sqlmock) map[string]string {
	t.Helper()
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	return h
}

func statusRows(status string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"status"}).AddRow(status)
}

func TestSessionTransitionLockedWrongState(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	// A DRAFT session can never finish (E10) — decided by the steady-state
	// read alone, no Tx.
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(4), int64(1)).WillReturnRows(statusRows("DRAFT"))

	res := doReq(t, app, "POST", "/api/sessions/4/finish", "", h)
	requireErrEnvelope(t, res, CodeSessionLocked)
}

func TestSessionTransitionAlreadyInTargetState(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(4), int64(1)).WillReturnRows(statusRows("FINISHED"))

	res := doReq(t, app, "POST", "/api/sessions/4/finish", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "FINISHED", data["status"])
	require.Equal(t, true, data["already"])
}

func TestSessionTransitionUnknownSessionIsNotFound(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(404), int64(1)).WillReturnError(sql.ErrNoRows)

	res := doReq(t, app, "POST", "/api/sessions/404/finish", "", h)
	requireErrEnvelope(t, res, CodeNotFound)
}

func TestSessionTransitionHappyPathCommitsGuardedUpdate(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(4), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectBegin()
	// The row is locked and the start state re-verified inside the Tx.
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(4), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectExec(q("UPDATE sessions SET status = ? WHERE id = ?")).
		WithArgs("FINISHED", int64(4), int64(1), "OPENED").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := doReq(t, app, "POST", "/api/sessions/4/finish", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "FINISHED", data["status"])
	_, hasAlready := data["already"]
	require.False(t, hasAlready, "a real transition carries no already flag")
}

func TestSessionTransitionStateConflictWhenRivalMovedFirst(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	// Steady state says OPENED…
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(4), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectBegin()
	// …but the locked re-read sees a different state: E11, nothing published.
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(4), int64(1)).WillReturnRows(statusRows("DRAFT"))
	mock.ExpectRollback()

	res := doReq(t, app, "POST", "/api/sessions/4/finish", "", h)
	requireErrEnvelope(t, res, CodeStateConflict)
}

// ---------------------------------------------------------------------------
// Open transition (decision 17 gates + guarded UPDATE + session.status)
// ---------------------------------------------------------------------------

// TestSessionOpenHappyPathPublishesStatus: DRAFT → OPENED passes every open
// gate, commits the guarded UPDATE and publishes exactly one session.status
// frame.
func TestSessionOpenHappyPathPublishesStatus(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	// Open gates: ≥1 group, every group non-empty, owner active.
	mock.ExpectQuery(q("SELECT COUNT(*) FROM session_groups WHERE session_id = ?")).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery(q("FROM session_groups g WHERE g.session_id = ?")).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id", "members"}).AddRow(10, "Kelompok Merah", 3, 2))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(1))
	mock.ExpectExec(q("UPDATE sessions SET status = ? WHERE id = ?")).
		WithArgs("OPENED", int64(2), int64(1), "DRAFT").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "OPENED", data["status"])
	_, hasAlready := data["already"]
	require.False(t, hasAlready, "a real transition carries no already flag")

	f := covNextFrame(t, sub, "session.status after open")
	require.Equal(t, "session.status", f.Entity)
	require.Equal(t, "opened", f.Action)
	require.Equal(t, int64(2), f.EntityID)
	require.NotNil(t, f.SessionID)
	require.Equal(t, int64(2), *f.SessionID)
	covNoFrame(t, sub, "exactly one frame per transition")
}

// TestSessionOpenAlreadyOpenedEchoesStatus: the steady-state read decides
// `already` before any Tx — idempotent success, no publish.
func TestSessionOpenAlreadyOpenedEchoesStatus(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))

	res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "OPENED", data["status"])
	require.Equal(t, true, data["already"])
	covNoFrame(t, sub, "already-in-target publishes nothing")
}

// TestSessionOpenUpdateZeroRowsIsStateConflictWithoutPublish: the guarded
// UPDATE loses (0 rows) → STATE_CONFLICT (E11 outcome), rollback, and no
// session.status frame ever reaches the hub.
func TestSessionOpenUpdateZeroRowsIsStateConflictWithoutPublish(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM session_groups WHERE session_id = ?")).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery(q("FROM session_groups g WHERE g.session_id = ?")).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id", "members"}).AddRow(10, "Kelompok Merah", 3, 2))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(1))
	// Guarded UPDATE matched nothing → the mandated re-read sees the rival
	// already at the wanted state → STATE_CONFLICT + rollback.
	mock.ExpectExec(q("UPDATE sessions SET status = ? WHERE id = ?")).
		WithArgs("OPENED", int64(2), int64(1), "DRAFT").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectRollback()

	res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
	requireErrEnvelope(t, res, CodeStateConflict)
	covNoFrame(t, sub, "a lost transition publishes nothing")
}

// TestSessionFinishUpdateZeroRowsLockedWhenStateForbids: the OTHER half of
// the mandated E10/E11 zero-rows re-read — when the re-read shows a status
// that itself forbids the op (single-writer wrong-state, here DRAFT for a
// finish), the loser gets SESSION_LOCKED, not STATE_CONFLICT, and nothing
// is published.
func TestSessionFinishUpdateZeroRowsLockedWhenStateForbids(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	// Steady state allows the finish…
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(4), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectBegin()
	// …the locked re-read re-verifies the start state…
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(4), int64(1)).WillReturnRows(statusRows("OPENED"))
	// …the guarded UPDATE matches nothing…
	mock.ExpectExec(q("UPDATE sessions SET status = ? WHERE id = ?")).
		WithArgs("FINISHED", int64(4), int64(1), "OPENED").
		WillReturnResult(sqlmock.NewResult(0, 0))
	// …and the mandated re-read shows DRAFT: the current state itself forbids
	// finishing → SESSION_LOCKED (E10 single-writer), never STATE_CONFLICT.
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(4), int64(1)).WillReturnRows(statusRows("DRAFT"))
	mock.ExpectRollback()

	res := doReq(t, app, "POST", "/api/sessions/4/finish", "", h)
	requireErrEnvelope(t, res, CodeSessionLocked)
	covNoFrame(t, sub, "the SESSION_LOCKED loser publishes nothing")
}

// requireGateGroups asserts the open-gate offender list carried under
// `error.groups` (B11), entry by entry, in order.
func requireGateGroups(t *testing.T, errObj map[string]any, want ...gateGroup) {
	t.Helper()
	raw, ok := errObj["groups"].([]any)
	require.Truef(t, ok, "error.groups must be a list: %#v", errObj["groups"])
	require.Len(t, raw, len(want))
	for i, w := range want {
		g, ok := raw[i].(map[string]any)
		require.Truef(t, ok, "group entry must be an object: %#v", raw[i])
		require.Equal(t, float64(w.GID), g["gid"])
		require.Equal(t, w.Name, g["name"])
		require.Equal(t, w.Reason, g["reason"])
	}
}

// TestSessionOpenGateMatrix: each open precondition fails envelope-only with
// its own code (06 §1 E1/E14), lists the offending groups and never publishes.
func TestSessionOpenGateMatrix(t *testing.T) {
	t.Run("zero groups is validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
		mock.ExpectBegin()
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
		mock.ExpectQuery(q("SELECT COUNT(*) FROM session_groups WHERE session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
		mock.ExpectRollback()

		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		env := requireErrEnvelope(t, res, CodeValidation)
		require.Empty(t, env["groups"], "a Draft with zero groups has no offender to name")
		covNoFrame(t, sub, "gate failure publishes nothing")
	})

	t.Run("empty group is validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)

		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
		mock.ExpectBegin()
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
		mock.ExpectQuery(q("SELECT COUNT(*) FROM session_groups WHERE session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		// Owner present but zero members: ownerOK still runs, then the empty
		// flag wins (E1 branch).
		mock.ExpectQuery(q("FROM session_groups g WHERE g.session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id", "members"}).
				AddRow(10, "Kelompok Kosong", 3, 0))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users")).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(1))
		mock.ExpectRollback()

		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		env := requireErrEnvelope(t, res, CodeValidation)
		requireGateGroups(t, env, gateGroup{GID: 10, Name: "Kelompok Kosong", Reason: "empty"})
	})

	t.Run("owner failing ownerOK is group without facilitator", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)

		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
		mock.ExpectBegin()
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
		mock.ExpectQuery(q("SELECT COUNT(*) FROM session_groups WHERE session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		mock.ExpectQuery(q("FROM session_groups g WHERE g.session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id", "members"}).AddRow(10, "Kelompok Merah", 3, 2))
		// ownerOK: the assigned facilitator is no longer active/approved (E14).
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users")).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(0))
		mock.ExpectRollback()

		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		env := requireErrEnvelope(t, res, CodeGroupNoFacilitator)
		requireGateGroups(t, env, gateGroup{GID: 10, Name: "Kelompok Merah", Reason: "owner_missing"})
	})
}

// TestSessionPatchUpdateLostAfterFinishIsSessionLocked: snapshot PATCH loses
// the guarded DRAFT UPDATE → the status re-read decides SESSION_LOCKED
// (E10, wrong-state single writer) and nothing is published.
func TestSessionPatchUpdateLostAfterFinishIsSessionLocked(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "program_id", "program_name_snap", "name",
			"description", "date", "start_time", "end_time", "location", "status",
			"created_by", "created_at"}).
			AddRow(2, 9, "Program A", "Sesi A", nil, fixedTS, nil, nil, "Lab", "DRAFT",
				int64(2), fixedTS))
	// A rival moved the session between the read and the guarded UPDATE.
	mock.ExpectExec(q("UPDATE sessions SET name = ? WHERE id = ?")).
		WithArgs("Sesi Baru", int64(2), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// 0 rows → status re-read: the row now forbids Draft edits (E10).
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("FINISHED"))

	res := jsonReq(t, app, "PATCH", "/api/sessions/2", map[string]any{"name": "Sesi Baru"}, h)
	requireErrEnvelope(t, res, CodeSessionLocked)
	covNoFrame(t, sub, "a lost guarded UPDATE publishes nothing")
}

// ---------------------------------------------------------------------------
// Create (FL-07 decision 17): eligibility, snapshots, gates, idempotency
// ---------------------------------------------------------------------------

// TestSessionCreateZeroGroupsDraftCommitsSnapshot: a group-less Draft is
// legal — ONE Tx writes the session + topic/activity snapshots and commits;
// session.changed fires after commit.
func TestSessionCreateZeroGroupsDraftCommitsSnapshot(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	h["X-Idempotency-Key"] = "sess-create-1"
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), "POST /api/sessions", "sess-create-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), "POST /api/sessions", "sess-create-1").
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
		WithArgs(sqlmock.AnyArg(), int64(1), "POST /api/sessions", "sess-create-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/api/sessions", map[string]any{
		"program_id": 9, "name": "Sesi Baru", "date": "2026-01-05",
		// The FE's literal payload is HH:MM:SS (F-02-01) — create must pass.
		"start_time": "09:00:00", "end_time": "11:00:00", "location": "Lab",
	}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(41), data["session_id"])

	f := covNextFrame(t, sub, "session.changed after create")
	require.Equal(t, "session.changed", f.Entity)
	require.Equal(t, "created", f.Action)
	require.NotNil(t, f.SessionID)
	require.Equal(t, int64(41), *f.SessionID)
	covNoFrame(t, sub, "one frame per create")
}

// TestSessionCreateProgramIncompleteRollsBack: E13 eligibility gate aborts
// the whole Tx before any snapshot row exists.
func TestSessionCreateProgramIncompleteRollsBack(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	h["X-Idempotency-Key"] = "sess-create-2"

	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), "POST /api/sessions", "sess-create-2").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), "POST /api/sessions", "sess-create-2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM programs WHERE id = ? AND school_id = ? AND is_active = TRUE")).
		WithArgs(int64(9), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Program A"))
	// Zero eligible topics → PROGRAM_INCOMPLETE (both counts still run).
	mock.ExpectQuery(q("WHERE program_id = ? AND school_id = ? AND is_active = TRUE")).
		WithArgs(int64(9), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectQuery(q("WHERE t.program_id = ? AND t.school_id = ? AND t.is_active = TRUE")).
		WithArgs(int64(9), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectRollback()
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST /api/sessions", "sess-create-2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/api/sessions", map[string]any{
		"program_id": 9, "name": "Sesi A", "date": "2026-01-05",
		"location": "Lab",
	}, h)
	requireErrEnvelope(t, res, CodeProgramIncomplete)
}

// TestSessionCreateDuplicateChildFailsWholeTransaction: the same child in
// two groups trips CHILD_DUPLICATE mid-Tx — everything rolls back (E12).
func TestSessionCreateDuplicateChildFailsWholeTransaction(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	h["X-Idempotency-Key"] = "sess-create-3"

	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), "POST /api/sessions", "sess-create-3").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), "POST /api/sessions", "sess-create-3").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM programs WHERE id = ? AND school_id = ? AND is_active = TRUE")).
		WithArgs(int64(9), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Program A"))
	mock.ExpectQuery(q("WHERE program_id = ? AND school_id = ? AND is_active = TRUE")).
		WithArgs(int64(9), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
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
	// Children locked FOR UPDATE up front (05 §7). The distinct list keeps
	// every occurrence (the pre-load dedup map is still empty), so the same
	// child in two groups yields IN (?,?) with both slots.
	mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
		WithArgs(int64(30), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}).
			AddRow(30, "Ayu", true))
	expectOwner := func() {
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"role", "decision", "is_active", "school_id"}).
				AddRow("FASILITATOR", "APPROVED", true, int64(1)))
		mock.ExpectQuery(q("SELECT name FROM users WHERE id = ?")).
			WithArgs(int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Fasil"))
	}
	expectOwner()
	mock.ExpectExec(q("INSERT INTO session_groups")).
		WillReturnResult(sqlmock.NewResult(101, 1))
	mock.ExpectExec(q("INSERT INTO session_members")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	expectOwner()
	mock.ExpectExec(q("INSERT INTO session_groups")).
		WillReturnResult(sqlmock.NewResult(102, 1))
	// Child 30 already seen in group 1 → CHILD_DUPLICATE, whole Tx undone.
	mock.ExpectRollback()
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST /api/sessions", "sess-create-3").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/api/sessions", map[string]any{
		"program_id": 9, "name": "Sesi A", "date": "2026-01-05", "location": "Lab",
		"groups": []map[string]any{
			{"name": "G1", "facilitator_id": 3, "child_ids": []int64{30}},
			{"name": "G2", "facilitator_id": 3, "child_ids": []int64{30}},
		},
	}, h)
	requireErrEnvelope(t, res, CodeChildDuplicate)
}

// TestSessionCreateInactiveOwnerIsGroupWithoutFacilitator: a deactivated
// facilitator can never own a group (E14) — create aborts before any group
// row survives.
func TestSessionCreateInactiveOwnerIsGroupWithoutFacilitator(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	h["X-Idempotency-Key"] = "sess-create-4"

	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), "POST /api/sessions", "sess-create-4").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), "POST /api/sessions", "sess-create-4").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM programs WHERE id = ? AND school_id = ? AND is_active = TRUE")).
		WithArgs(int64(9), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Program A"))
	mock.ExpectQuery(q("WHERE program_id = ? AND school_id = ? AND is_active = TRUE")).
		WithArgs(int64(9), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
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
	// Deactivated owner → requestOwner maps to GROUP_WITHOUT_FACILITATOR.
	mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"role", "decision", "is_active", "school_id"}).
			AddRow("FASILITATOR", "APPROVED", false, int64(1)))
	mock.ExpectRollback()
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST /api/sessions", "sess-create-4").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/api/sessions", map[string]any{
		"program_id": 9, "name": "Sesi A", "date": "2026-01-05", "location": "Lab",
		"groups": []map[string]any{
			{"name": "G1", "facilitator_id": 3, "child_ids": []int64{}},
		},
	}, h)
	requireErrEnvelope(t, res, CodeGroupNoFacilitator)
}

// TestSessionCreateIdempotentReplayReturnsOriginalSessionID: a duplicate key
// answers the RECORDED envelope (original session_id) with exactly one
// INSERT claim — the create Tx never runs again.
func TestSessionCreateIdempotentReplayReturnsOriginalSessionID(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	h["X-Idempotency-Key"] = "sess-create-replay"

	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), "POST /api/sessions", "sess-create-replay").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), "POST /api/sessions", "sess-create-replay").
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	// Replay: recorded envelope only — a re-executed create would BEGIN and
	// fail this expectation set.
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), "POST /api/sessions", "sess-create-replay").
		WillReturnRows(sqlmock.NewRows([]string{"response"}).
			AddRow(`{"data":{"session_id":41}}`))

	res := jsonReq(t, app, "POST", "/api/sessions", map[string]any{
		"program_id": 9, "name": "Sesi A", "date": "2026-01-05", "location": "Lab",
	}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(41), data["session_id"],
		"the replay must return the ORIGINAL session_id, not a fresh insert")
}

// ---------------------------------------------------------------------------
// DELETE (decision 8 option A)
// ---------------------------------------------------------------------------

// TestSessionDeleteOnOpenedIsSessionLocked: Opened sessions must finish
// first — the row-lock claim alone decides E10, nothing else runs.
func TestSessionDeleteOnOpenedIsSessionLocked(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectRollback()

	res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
	requireErrEnvelope(t, res, CodeSessionLocked)
	covNoFrame(t, sub, "a locked delete publishes nothing")
}

// TestSessionDeleteDraftWipesScopeAndPublishesRevocations: ONE Tx wipes every
// session-scoped row (children data + members/groups/snapshots + the session
// itself), then post-commit the revocation signals fan out: session.deleted,
// report-link.changed, consent.changed and a token-precise link.revoked per
// dead public/consent token.
func TestSessionDeleteDraftWipesScopeAndPublishesRevocations(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	// Members → per-child scope wipe inside the Tx.
	mock.ExpectQuery(q("SELECT child_id FROM session_members WHERE session_id = ?")).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id"}).AddRow(30))
	mock.ExpectQuery(q("SELECT path FROM photos WHERE session_id = ?")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("1/2/5/30/a.jpg"))
	mock.ExpectExec(q("DELETE FROM photos WHERE session_id = ?")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT id, child_id, token FROM consent_links")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "child_id", "token"}).
			AddRow(7, int64(30), "ctok"))
	mock.ExpectQuery(q("SELECT id, child_id, token, kind FROM public_links WHERE revoked_at IS NULL")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "child_id", "token", "kind"}).
			AddRow(9, int64(30), "ptok", "REPORT"))
	for _, table := range []string{"assessments", "narratives", "report_missions",
		"consent_links", "public_links"} {
		mock.ExpectExec(q("DELETE FROM "+table+" WHERE session_id = ?")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	for _, table := range []string{"session_members", "session_groups",
		"session_activities", "session_topics"} {
		mock.ExpectExec(q("DELETE FROM " + table + " WHERE session_id = ?")).
			WithArgs(int64(2)).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(q("DELETE FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])

	// Post-commit signal sequence (hub seam).
	require.Equal(t, "session.deleted", covNextFrame(t, sub, "session.deleted").Entity)
	linkFrame := covNextFrame(t, sub, "report-link.changed for the dead REPORT link")
	require.Equal(t, "report-link.changed", linkFrame.Entity)
	require.Equal(t, int64(9), linkFrame.EntityID)
	require.Equal(t, "consent.changed", covNextFrame(t, sub, "consent.changed for wiped links").Entity)
	// publishWipedLinks walks the collected kills in SELECT order: consent
	// links first, then public links — both token-precise.
	rev1 := covNextFrame(t, sub, "link.revoked for the consent token")
	require.Equal(t, "link.revoked", rev1.Entity)
	require.Equal(t, "ctok", rev1.Token, "revoke is token-precise")
	rev2 := covNextFrame(t, sub, "link.revoked for the public token")
	require.Equal(t, "link.revoked", rev2.Entity)
	require.Equal(t, "ptok", rev2.Token)
	covNoFrame(t, sub, "exactly five post-commit signals")
}

// ---------------------------------------------------------------------------
// Copy-data (F-15 / FL-16 / decision 21)
// ---------------------------------------------------------------------------

// TestCopyExecuteFacilitatorIsForbiddenRole: the copy route is SA/ADMIN
// only — a facilitator never reaches any SQL (E8).
func TestCopyExecuteFacilitatorIsForbiddenRole(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := assessmentFASHeaders(t, s, mock)

	res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
		map[string]any{"source_session_id": 5}, h)
	requireErrEnvelope(t, res, CodeForbiddenRole)
}

// TestCopyExecuteCrossProgramIsValidation: cross-program sources are rejected
// before membership probes or any write (06 §5 copy rules).
func TestCopyExecuteCrossProgramIsValidation(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("SELECT program_id, status FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"program_id", "status"}).
			AddRow(9, "OPENED"))
	mock.ExpectQuery(q("SELECT school_id, program_id FROM sessions WHERE id = ?")).
		WithArgs(int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "program_id"}).
			AddRow(int64(1), int64(8)))

	res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
		map[string]any{"source_session_id": 5}, h)
	requireErrEnvelope(t, res, CodeValidation)
}

// TestCopyExecuteInactiveChildIsChildInactive: both membership probes pass
// but a deactivated child blocks the copy before the Tx (06 §5).
func TestCopyExecuteInactiveChildIsChildInactive(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("SELECT program_id, status FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"program_id", "status"}).
			AddRow(9, "OPENED"))
	mock.ExpectQuery(q("SELECT school_id, program_id FROM sessions WHERE id = ?")).
		WithArgs(int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "program_id"}).
			AddRow(int64(1), int64(9)))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT name, is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "is_active"}).
			AddRow("Ayu", false))

	res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
		map[string]any{"source_session_id": 5}, h)
	requireErrEnvelope(t, res, CodeChildInactive)
}

// expectCopyPrologue wires the shared copy-execute prologue: target+source
// sessions (same program), both membership probes, active child, never-sent
// consent, and the idempotency claim.
func expectCopyPrologue(mock sqlmock.Sqlmock, key, tgtStatus string, tgtProgram, srcProgram int64) {
	mock.ExpectQuery(q("SELECT program_id, status FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"program_id", "status"}).
			AddRow(tgtProgram, tgtStatus))
	mock.ExpectQuery(q("SELECT school_id, program_id FROM sessions WHERE id = ?")).
		WithArgs(int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "program_id"}).
			AddRow(int64(1), srcProgram))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT name, is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "is_active"}).
			AddRow("Ayu", true))
	mock.ExpectQuery(q("SELECT status FROM consent_links WHERE session_id = ? AND child_id = ?")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), "POST /api/sessions/2/children/30/copy", key).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), "POST /api/sessions/2/children/30/copy", key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// TestCopyExecuteKeepsFilledTargetCells: fill-empty never overwrites — the
// existing target cell skips its INSERT (kept stars) and unmatched source
// activities skip too; the response reports honest copied/skipped counts.
func TestCopyExecuteKeepsFilledTargetCells(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	h["X-Idempotency-Key"] = "copy-keep-1"
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	expectCopyPrologue(mock, "copy-keep-1", "OPENED", 9, 9)
	mock.ExpectBegin()
	// Master-id maps of the target snapshot.
	mock.ExpectQuery(q("SELECT id, topic_id FROM session_topics WHERE session_id = ?")).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_id"}).AddRow(50, 9))
	mock.ExpectQuery(q("SELECT id, activity_id FROM session_activities WHERE session_id = ?")).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "activity_id"}).AddRow(61, 101))
	// Source assessments: activity 101 maps to a FILLED target cell → skip;
	// activity 102 has no target twin → skip. No INSERT is ever issued.
	mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
		WithArgs(int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}).
			AddRow(101, 3, nil).
			AddRow(102, 4, nil))
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}).AddRow(61))
	// Narratives / missions / photos: empty sources.
	mock.ExpectQuery(q("FROM narratives n")).
		WithArgs(int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id", "text", "model", "generated_at"}))
	mock.ExpectQuery(q("FROM report_missions rm")).
		WithArgs(int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id", "mission_id", "name"}))
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
	mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
		WithArgs(int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"path", "mime", "file_size", "frame_id", "topic_id"}))
	mock.ExpectCommit()
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST /api/sessions/2/children/30/copy", "copy-keep-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
		map[string]any{"source_session_id": 5}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	copied := data["copied"].(map[string]any)
	skipped := data["skipped"].(map[string]any)
	require.Equal(t, float64(0), copied["assessments"], "a filled cell must NOT be re-inserted")
	require.Equal(t, float64(2), skipped["assessments"], "filled + unmatched both skip")
	require.Equal(t, float64(0), copied["narratives"])
	require.Equal(t, float64(0), copied["missions"])
	require.Equal(t, float64(0), copied["photos"])

	f := covNextFrame(t, sub, "copy.completed after commit")
	require.Equal(t, "copy.completed", f.Entity)
	require.NotNil(t, f.SessionID)
	require.Equal(t, int64(2), *f.SessionID)
	require.Equal(t, int64(30), f.ChildID)
}

// TestCopyExecuteFinishedTargetIsSessionLocked: a FINISHED target rejects
// the copy outright (user decision 2026-10-09 — overrides the older partial
// "photo part may still proceed" semantics): 200 SESSION_LOCKED after the
// standard guards, BEFORE the idempotency claim, any Tx, any INSERT, any
// file IO, any publish. Source status stays unrestricted.
func TestCopyExecuteFinishedTargetIsSessionLocked(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	h["X-Idempotency-Key"] = "copy-finished-1"
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	// Prologue through the CHILD_INACTIVE guard only: target FINISHED.
	mock.ExpectQuery(q("SELECT program_id, status FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"program_id", "status"}).
			AddRow(9, "FINISHED"))
	mock.ExpectQuery(q("SELECT school_id, program_id FROM sessions WHERE id = ?")).
		WithArgs(int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "program_id"}).
			AddRow(int64(1), 9))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT name, is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "is_active"}).
			AddRow("Ayu", true))
	// NO consent read, NO idempotency claim, NO Begin/INSERT/commit — the
	// expectations above are the only ones registered, so sqlmock fails the
	// test if the handler touches the DB again.

	res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
		map[string]any{"source_session_id": 5}, h)
	requireErrEnvelope(t, res, CodeSessionLocked)
	covNoFrame(t, sub, "a rejected copy publishes nothing")
}

// ---------------------------------------------------------------------------
// List / detail (05 §3 paging + 02 §2b ONE response)
// ---------------------------------------------------------------------------

// TestSessionListPagedShape: default sort (date DESC), the paged envelope
// (data + meta) and the item shape from the contracts.
func TestSessionListPagedShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("COUNT(*) FROM sessions s")).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery(q("s.date desc, s.id ASC")).
		WithArgs(int64(1), 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "program_id",
			"program_name_snap", "name", "description", "date", "start_time",
			"end_time", "location", "status", "group_count", "member_count",
			"created_at"}).
			AddRow(41, 9, "Program A", "Sesi A", nil, fixedTS, nil, nil, "Lab",
				"OPENED", 2, 5, fixedTS))

	res := doReq(t, app, "GET", "/api/sessions", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 1)
	item := data[0].(map[string]any)
	require.Equal(t, float64(41), item["id"])
	require.Equal(t, "Program A", item["program_name"])
	require.Equal(t, "2026-01-02", item["date"])
	require.Equal(t, "OPENED", item["status"])
	require.Equal(t, float64(2), item["group_count"])
	require.Equal(t, float64(5), item["member_count"])
	meta := envelopeMap(t, res.body)["meta"].(map[string]any)
	require.Equal(t, float64(1), meta["page"])
	require.Equal(t, float64(20), meta["per_page"])
	require.Equal(t, float64(1), meta["total"])
}

// TestSessionListRejectsUnknownStatusFilter: the ?status= value must be one
// of DRAFT/OPENED/FINISHED — validated before any SQL (05 §3).
func TestSessionListRejectsUnknownStatusFilter(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	res := doReq(t, app, "GET", "/api/sessions?status=CLOSED", "", h)
	requireErrEnvelope(t, res, CodeValidation)
}

// TestSessionListFASWithoutGroupsIsEmpty: a facilitator owning no group
// gets the empty page WITHOUT touching the sessions table (own ≥1 group
// only, F-08).
func TestSessionListFASWithoutGroupsIsEmpty(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := assessmentFASHeaders(t, s, mock)

	mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}))

	res := doReq(t, app, "GET", "/api/sessions", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Empty(t, data)
	meta := envelopeMap(t, res.body)["meta"].(map[string]any)
	require.Equal(t, float64(0), meta["total"])
}

// TestSessionDetailFullShape: the ONE detail response — session + topic
// activity snapshot + full group objects (owner, consent counters, members)
// + the top-level wipe-counts scope (contracts pin).
func TestSessionDetailFullShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "program_id",
			"program_name_snap", "name", "description", "date", "start_time",
			"end_time", "location", "status", "created_by", "created_at"}).
			AddRow(2, 9, "Program A", "Sesi A", nil, fixedTS, "09:00", "11:00",
				"Lab", "DRAFT", int64(2), fixedTS))
	// Topic + activity snapshot.
	mock.ExpectQuery(q("FROM session_topics st WHERE st.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_id", "name", "sort_order"}).
			AddRow(5, 9, "Topik A", 1))
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id",
			"activity_id", "name", "indicator", "sort_order"}).
			AddRow(5, 11, 101, "Aktivitas A", "Indikator", 1))
	// Groups → members → owners (full own-group objects for ADMIN).
	mock.ExpectQuery(q("FROM session_groups g")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id"}).
			AddRow(10, "Kelompok Merah", 3))
	mock.ExpectQuery(q("FROM session_members sm")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "child_id", "name",
			"age", "school", "consent"}).
			AddRow(10, 30, "Ayu", 8, "SD Bintang", "SETUJU"))
	mock.ExpectQuery(q("SELECT id, name FROM users")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(3, "Fasil"))
	// Scope wipe counts (9 session-scoped subqueries + the wa_outbox school
	// predicate + public_links probe + outer id + the outer school cond).
	mock.ExpectQuery(q("(SELECT COUNT(*) FROM session_topics WHERE session_id = ?)")).
		WithArgs(int64(2), int64(2), int64(2), int64(2), int64(2), int64(2),
			int64(2), int64(2), int64(2), int64(1), int64(2), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"topics", "groups", "members",
			"assessments", "photos", "narratives", "links_report",
			"links_gallery", "links_consent", "reports_sent"}).
			AddRow(1, 1, 1, 2, 3, 1, 1, 1, 1, 4))

	res := doReq(t, app, "GET", "/api/sessions/2", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)

	sess := data["session"].(map[string]any)
	require.Equal(t, float64(2), sess["id"])
	require.Equal(t, "Program A", sess["program_name"])
	require.Equal(t, "DRAFT", sess["status"])
	require.Equal(t, "09:00", sess["start_time"])
	require.Equal(t, float64(2), sess["created_by"])

	topics := data["topics"].([]any)
	require.Len(t, topics, 1)
	topic := topics[0].(map[string]any)
	require.Equal(t, float64(5), topic["stid"])
	require.Equal(t, float64(9), topic["topic_id"])
	acts := topic["activities"].([]any)
	require.Len(t, acts, 1)
	act := acts[0].(map[string]any)
	require.Equal(t, float64(11), act["said"])
	require.Equal(t, float64(101), act["activity_id"])
	require.Equal(t, "Aktivitas A", act["name"])

	groups := data["groups"].([]any)
	require.Len(t, groups, 1)
	g := groups[0].(map[string]any)
	require.Equal(t, float64(10), g["gid"])
	require.Equal(t, "Kelompok Merah", g["name"])
	require.Equal(t, true, g["own"])
	require.Equal(t, false, g["owner_missing"])
	owner := g["owner"].(map[string]any)
	require.Equal(t, float64(3), owner["id"])
	require.Equal(t, "Fasil", owner["name"])
	require.Equal(t, float64(1), g["member_count"])
	consent := g["consent"].(map[string]any)
	require.Equal(t, float64(1), consent["SETUJU"])
	require.Equal(t, float64(0), consent["PENDING"])
	members := g["members"].([]any)
	require.Len(t, members, 1)
	m := members[0].(map[string]any)
	require.Equal(t, float64(30), m["child_id"])
	require.Equal(t, "Ayu", m["name"])
	require.Equal(t, float64(8), m["age"])
	require.Equal(t, "SETUJU", m["consent"])

	scope := data["scope"].(map[string]any)
	require.Equal(t, float64(1), scope["topics"])
	require.Equal(t, float64(2), scope["assessments"])
	require.Equal(t, float64(3), scope["photos"])
	require.Equal(t, float64(1), scope["links_report"])
	require.Equal(t, float64(1), scope["links_gallery"])
	require.Equal(t, float64(1), scope["links_consent"])
	require.Equal(t, float64(4), scope["reports_sent"])
}

// TestSessionDetailFASOmitsScope: facilitators can never delete a session
// (the scope counts only feed the canManage-gated typed wipe dialog), so the
// FAS detail omits the `scope` object entirely and its scopeQ never runs
// (002a — contract types `scope` optional). Any stray scope query would hit
// the mock unfulfilled and fail this test.
func TestSessionDetailFASOmitsScope(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := assessmentFASHeaders(t, s, mock)

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "program_id",
			"program_name_snap", "name", "description", "date", "start_time",
			"end_time", "location", "status", "created_by", "created_at"}).
			AddRow(2, 9, "Program A", "Sesi A", nil, fixedTS, "09:00", "11:00",
				"Lab", "OPENED", int64(2), fixedTS))
	// FAS own scope: group 10 of session 2.
	mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).AddRow(10, 2))
	// Topic/activity snapshots + group assembly (own group stays full).
	mock.ExpectQuery(q("FROM session_topics st WHERE st.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_id", "name", "sort_order"}))
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id",
			"activity_id", "name", "indicator", "sort_order"}))
	mock.ExpectQuery(q("FROM session_groups g")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id"}).
			AddRow(10, "Kelompok Merah", 3))
	mock.ExpectQuery(q("FROM session_members sm")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "child_id", "name",
			"age", "school", "consent"}).
			AddRow(10, 30, "Ayu", 8, "SD Bintang", "SETUJU"))
	mock.ExpectQuery(q("SELECT id, name FROM users")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(3, "Fasil"))

	res := doReq(t, app, "GET", "/api/sessions/2", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.NotContains(t, data, "scope",
		"FAS detail omits the wipe-counts scope (never deletable, 002a): %s", res.body)
	require.Equal(t, "Sesi A", data["session"].(map[string]any)["name"])
	groups := data["groups"].([]any)
	require.Len(t, groups, 1)
	require.Equal(t, true, groups[0].(map[string]any)["own"])
}

// ---------------------------------------------------------------------------
// Groups & members (02 §2b mutation behind the row-lock status gate)
// ---------------------------------------------------------------------------

// TestSessionGroupCreateCommitsAndPublishes: Draft group creation behind the
// FOR UPDATE status claim, owner snap, then group.changed after commit.
func TestSessionGroupCreateCommitsAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"role", "decision", "is_active", "school_id"}).
			AddRow("FASILITATOR", "APPROVED", true, int64(1)))
	mock.ExpectQuery(q("SELECT name FROM users WHERE id = ?")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Fasil"))
	mock.ExpectExec(q("INSERT INTO session_groups")).
		WillReturnResult(sqlmock.NewResult(101, 1))
	mock.ExpectCommit()

	res := jsonReq(t, app, "POST", "/api/sessions/2/groups",
		map[string]any{"name": "Kelompok Baru", "facilitator_id": 3}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(101), data["gid"])
	require.Equal(t, "Kelompok Baru", data["name"])

	f := covNextFrame(t, sub, "group.changed after create")
	require.Equal(t, "group.changed", f.Entity)
	require.Equal(t, int64(101), f.EntityID)
	require.Equal(t, "created", f.Action)
	covNoFrame(t, sub, "one frame per group create")
}

// TestSessionGroupPatchRenamesAndPublishes: name + owner edit in one UPDATE
// behind the status claim; the response echoes the NEW name.
func TestSessionGroupPatchRenamesAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	mock.ExpectQuery(q("SELECT name FROM session_groups WHERE id = ? AND session_id = ?")).
		WithArgs(int64(10), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Lama"))
	mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"role", "decision", "is_active", "school_id"}).
			AddRow("FASILITATOR", "APPROVED", true, int64(1)))
	mock.ExpectQuery(q("SELECT name FROM users WHERE id = ?")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Fasil"))
	mock.ExpectExec(q("UPDATE session_groups SET")).
		WithArgs("Baru", int64(3), "Fasil", int64(10), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := jsonReq(t, app, "PATCH", "/api/sessions/2/groups/10",
		map[string]any{"name": "Baru", "facilitator_id": 3}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(10), data["gid"])
	require.Equal(t, "Baru", data["name"])

	f := covNextFrame(t, sub, "group.changed after patch")
	require.Equal(t, "group.changed", f.Entity)
	require.Equal(t, "updated", f.Action)
}

// TestSessionGroupDeleteCascadeWipesChildrenScope: option-B cascade (decision
// 13) — ONE Tx removes members + group + each child's session-scoped rows.
func TestSessionGroupDeleteCascadeWipesChildrenScope(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	mock.ExpectQuery(q("SELECT 1 FROM session_groups WHERE id = ? AND session_id = ?")).
		WithArgs(int64(10), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT child_id FROM session_members WHERE group_id = ?")).
		WithArgs(int64(10)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id"}).AddRow(30))
	// Per-child wipe inside the Tx.
	mock.ExpectQuery(q("SELECT path FROM photos WHERE session_id = ?")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"path"}))
	mock.ExpectExec(q("DELETE FROM photos WHERE session_id = ?")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(q("SELECT id, child_id, token FROM consent_links")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "child_id", "token"}))
	mock.ExpectQuery(q("SELECT id, child_id, token, kind FROM public_links WHERE revoked_at IS NULL")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "child_id", "token", "kind"}))
	for _, table := range []string{"assessments", "narratives", "report_missions",
		"consent_links", "public_links"} {
		mock.ExpectExec(q("DELETE FROM "+table+" WHERE session_id = ?")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(q("DELETE FROM session_members WHERE group_id = ? AND session_id = ?")).
		WithArgs(int64(10), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("DELETE FROM session_groups WHERE id = ? AND session_id = ?")).
		WithArgs(int64(10), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])

	f := covNextFrame(t, sub, "group.changed after cascade")
	require.Equal(t, "group.changed", f.Entity)
	require.Equal(t, int64(10), f.EntityID)
	require.Equal(t, "deleted", f.Action)
	covNoFrame(t, sub, "no link kills → exactly one frame")
}

// TestSessionMembersAddHappyPathCommits: locked child lookup, dedup probes
// and the member insert commit to one member.changed frame.
func TestSessionMembersAddHappyPathCommits(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	mock.ExpectQuery(q("SELECT 1 FROM session_groups WHERE id = ? AND session_id = ?")).
		WithArgs(int64(10), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
		WithArgs(int64(31), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}).
			AddRow(31, "Budi", true))
	mock.ExpectQuery(q("SELECT child_id FROM session_members")).
		WithArgs(int64(31), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id"}))
	mock.ExpectExec(q("INSERT INTO session_members")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	// Post-commit copy_available hint for the new member's frame.
	mock.ExpectQuery(q("FROM sessions other")).
		WithArgs(int64(1), int64(2), int64(2), int64(31)).
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(1))

	res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
		map[string]any{"child_ids": []int64{31}}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	added := data["added"].([]any)
	require.Equal(t, []any{float64(31)}, added)

	f := covNextFrame(t, sub, "member.changed after add")
	require.Equal(t, "member.changed", f.Entity)
	require.Equal(t, int64(31), f.EntityID)
	require.NotNil(t, f.SessionID)
	require.Equal(t, int64(2), *f.SessionID)
	require.Equal(t, "created", f.Action)
	require.NotNil(t, f.CopyAvailable, "the frame carries the copy hint")
	require.True(t, *f.CopyAvailable, "child 31 has another same-program session")
	covNoFrame(t, sub, "one frame per added member")
}

// TestSessionMemberRemoveWipesAndPublishes: single remove runs the same
// per-child wipe as the cascade, then member.changed fires once.
func TestSessionMemberRemoveWipesAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	mock.ExpectQuery(q("SELECT 1 FROM session_members")).
		WithArgs(int64(2), int64(10), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	// Per-child wipe.
	mock.ExpectQuery(q("SELECT path FROM photos WHERE session_id = ?")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("1/2/5/30/a.jpg"))
	mock.ExpectExec(q("DELETE FROM photos WHERE session_id = ?")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT id, child_id, token FROM consent_links")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "child_id", "token"}))
	mock.ExpectQuery(q("SELECT id, child_id, token, kind FROM public_links WHERE revoked_at IS NULL")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "child_id", "token", "kind"}))
	for _, table := range []string{"assessments", "narratives", "report_missions",
		"consent_links", "public_links"} {
		mock.ExpectExec(q("DELETE FROM "+table+" WHERE session_id = ?")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(q("DELETE FROM session_members")).
		WithArgs(int64(2), int64(10), int64(30)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// Post-commit copy_available hint for the removed member's frame —
	// 02 §2c "member add / remove (+ copy_available hint recomputed)".
	mock.ExpectQuery(q("FROM sessions other")).
		WithArgs(int64(1), int64(2), int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(1))

	res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["removed"])

	f := covNextFrame(t, sub, "member.changed after remove")
	require.Equal(t, "member.changed", f.Entity)
	require.Equal(t, int64(30), f.EntityID)
	require.Equal(t, "deleted", f.Action)
	require.NotNil(t, f.CopyAvailable, "the remove frame carries the copy hint")
	require.True(t, *f.CopyAvailable, "child 30 has another same-program session")
	covNoFrame(t, sub, "no dead links → exactly one frame")
}

// ---------------------------------------------------------------------------
// Copy candidates + reopen + Draft PATCH happy path
// ---------------------------------------------------------------------------

// TestSessionCopyCandidatesListShape: same-school same-program sessions that
// contain the child, FINISHED first — counts ride along per candidate.
func TestSessionCopyCandidatesListShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("SELECT school_id, program_id FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "program_id"}).
			AddRow(int64(1), int64(9)))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("WHERE s.school_id = ? AND s.program_id = ? AND s.id <> ?")).
		WithArgs(int64(30), int64(30), int64(30), int64(30),
			int64(1), int64(9), int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date", "status",
			"assessments", "narratives", "missions", "photos"}).
			AddRow(5, "Sesi Lama", fixedTS, "FINISHED", 4, 1, 0, 2))

	res := doReq(t, app, "GET", "/api/sessions/2/children/30/copy-candidates", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 1)
	item := data[0].(map[string]any)
	require.Equal(t, float64(5), item["session_id"])
	require.Equal(t, "Sesi Lama", item["session_name"])
	require.Equal(t, "2026-01-02", item["session_date"])
	require.Equal(t, "FINISHED", item["status"])
	counts := item["counts"].(map[string]any)
	require.Equal(t, float64(4), counts["assessments"])
	require.Equal(t, float64(2), counts["photos"])
}

// TestSessionReopenFinishesToOpened: FINISHED → OPENED carries no open gates
// and publishes session.status with action "opened".
func TestSessionReopenFinishesToOpened(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(4), int64(1)).WillReturnRows(statusRows("FINISHED"))
	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(4), int64(1)).WillReturnRows(statusRows("FINISHED"))
	mock.ExpectExec(q("UPDATE sessions SET status = ? WHERE id = ?")).
		WithArgs("OPENED", int64(4), int64(1), "FINISHED").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := doReq(t, app, "POST", "/api/sessions/4/reopen", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "OPENED", data["status"])
	f := covNextFrame(t, sub, "session.status after reopen")
	require.Equal(t, "session.status", f.Entity)
	require.Equal(t, "opened", f.Action)
}

// TestSessionPatchDraftHappyPathPublishes: a Draft snapshot edit commits the
// guarded UPDATE and publishes session.changed with the merged next row.
func TestSessionPatchDraftHappyPathPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "program_id",
			"program_name_snap", "name", "description", "date", "start_time",
			"end_time", "location", "status", "created_by", "created_at"}).
			AddRow(2, 9, "Program A", "Sesi A", nil, fixedTS, nil, nil, "Lab",
				"DRAFT", int64(2), fixedTS))
	mock.ExpectExec(q("UPDATE sessions SET name = ? WHERE id = ?")).
		WithArgs("Nama Baru", int64(2), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "PATCH", "/api/sessions/2",
		map[string]any{"name": "Nama Baru"}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	sess := data["session"].(map[string]any)
	require.Equal(t, "Nama Baru", sess["name"], "response echoes the merged next row")
	require.Equal(t, "DRAFT", sess["status"])

	f := covNextFrame(t, sub, "session.changed after patch")
	require.Equal(t, "session.changed", f.Entity)
	require.Equal(t, "updated", f.Action)
	covNoFrame(t, sub, "exactly one frame per patch")
}

// TestSessionPatchTimeRangeSharesCreateValidator: patch and create run the
// ONE boundary predicate (F-02-01) — HH:MM:SS (the FE wire form) and HH:MM
// both pass, end <= start is VALIDATION before any UPDATE.
func TestSessionPatchTimeRangeSharesCreateValidator(t *testing.T) {
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

	t.Run("HH:MM:SS patch passes", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})
		loadDraft(t, mock)
		mock.ExpectExec(q("UPDATE sessions SET start_time = ?, end_time = ? WHERE id = ?")).
			WithArgs("09:00:00", "11:00:00", int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"start_time": "09:00:00", "end_time": "11:00:00"}, h)
		data := requireDataEnvelope(t, res).(map[string]any)
		sess := data["session"].(map[string]any)
		require.Equal(t, "09:00:00", sess["start_time"])
		require.Equal(t, "11:00:00", sess["end_time"])
		require.Equal(t, "session.changed", covNextFrame(t, sub, "committed HH:MM:SS patch").Entity)
	})

	t.Run("HH:MM patch passes and normalizes to the wire form", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})
		loadDraft(t, mock)
		mock.ExpectExec(q("UPDATE sessions SET start_time = ?, end_time = ? WHERE id = ?")).
			WithArgs("09:00", "11:00", int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"start_time": "09:00", "end_time": "11:00"}, h)
		data := requireDataEnvelope(t, res).(map[string]any)
		sess := data["session"].(map[string]any)
		require.Equal(t, "09:00:00", sess["start_time"], "response echoes the HH:MM:SS wire form")
		require.Equal(t, "11:00:00", sess["end_time"])
		require.Equal(t, "session.changed", covNextFrame(t, sub, "committed HH:MM patch").Entity)
	})

	t.Run("end <= start stays VALIDATION with no UPDATE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		loadDraft(t, mock)

		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"start_time": "11:00:00", "end_time": "09:00:00"}, h)
		requireErrEnvelope(t, res, CodeValidation)
		// No UPDATE expectation: the range gate trips before any write.
	})
}

// TestSessionCopyCandidatesQueryCarriesNoSensitiveTables: the candidates
// query is pinned to its exact SELECT — session columns + the five count
// subqueries, nothing else. consent_links / public_links / wa_outbox may
// never join this list: no consent answer, link token or WA receipt leaks
// into the copy picker (audit K15). The full-text anchor makes sqlmock
// reject ANY variant of the statement — an added table breaks the match,
// the query errors and the handler cannot return candidates at all.
func TestSessionCopyCandidatesQueryCarriesNoSensitiveTables(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("SELECT school_id, program_id FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "program_id"}).
			AddRow(int64(1), int64(9)))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	// Byte-exact replica of the handler's raw literal (sessions.go), anchored
	// ^…$ so the match admits no additions (JOINs/WHEREs on other tables)
	// and no drops (a count subquery silently removed).
	candidateSQL := "SELECT s.id, s.name, s.date, s.status,\n" +
		"\t\t        (SELECT COUNT(*) FROM assessments a\n" +
		"\t\t         WHERE a.session_id = s.id AND a.child_id = ?),\n" +
		"\t\t        (SELECT COUNT(*) FROM narratives n\n" +
		"\t\t         WHERE n.session_id = s.id AND n.child_id = ?),\n" +
		"\t\t        (SELECT COUNT(*) FROM report_missions rm\n" +
		"\t\t         WHERE rm.session_id = s.id AND rm.child_id = ?),\n" +
		"\t\t        (SELECT COUNT(*) FROM photos p\n" +
		"\t\t         WHERE p.session_id = s.id AND p.child_id = ? AND p.file_ready = TRUE)\n" +
		"\t\t FROM sessions s\n" +
		"\t\t WHERE s.school_id = ? AND s.program_id = ? AND s.id <> ?\n" +
		"\t\t   AND EXISTS (SELECT 1 FROM session_members m\n" +
		"\t\t               WHERE m.session_id = s.id AND m.child_id = ?)\n" +
		"\t\t ORDER BY (s.status = 'FINISHED') DESC, s.date DESC, s.id DESC"
	mock.ExpectQuery("^"+regexp.QuoteMeta(candidateSQL)+"$").
		WithArgs(int64(30), int64(30), int64(30), int64(30),
			int64(1), int64(9), int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date", "status",
			"assessments", "narratives", "missions", "photos"}).
			AddRow(5, "Sesi Lama", fixedTS, "FINISHED", 4, 1, 0, 2))

	res := doReq(t, app, "GET", "/api/sessions/2/children/30/copy-candidates", "", h)
	items := requireDataEnvelope(t, res).([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	// The wire shape is counts-only — nothing token/consent/WA-ish ever rides.
	require.ElementsMatch(t, []string{
		"session_id", "session_name", "session_date", "status", "counts",
	}, mapKeys(item))
	require.ElementsMatch(t, []string{
		"assessments", "narratives", "missions", "photos",
	}, mapKeys(item["counts"].(map[string]any)))
}

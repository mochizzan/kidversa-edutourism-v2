package internal

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// SpxWipeArgs builds the (sid, child..., school) argument order every
// session-scoped wipe statement uses.
func SpxWipeArgs(sid, cid, school int64) []driver.Value {
	return []driver.Value{sid, cid, school}
}

// ---------------------------------------------------------------------------
// Spx fixtures — merge-safe helpers for this file only. Every identifier
// carries the Spx marker (8 agents merge into one package). Existing helpers
// (sessionAdminHeaders, assessmentFASHeaders, expectAdminAuth, hAdmin/hFAS/
// hSABare, statusRows, covSubscribe, q(), require*Envelope) are reused as-is.
// ---------------------------------------------------------------------------

const (
	spxCreateRoute = "POST /api/sessions"
	spxCopyRoute   = "POST /api/sessions/2/children/30/copy"
)

// SpxErr is the generic DB failure injected on any query/exec/commit branch
// (sqlmock v1.5.2 has no ErrConnDone; ErrCancelled plays that role here).
var SpxErr = sqlmock.ErrCancelled

// SpxIdemClaim queues the (school, route, key) request-time idempotency claim.
func SpxIdemClaim(mock sqlmock.Sqlmock, route, key string) {
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), route, key).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), route, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// SpxIdemRecord queues the envelope recording of a completed (non-DB-failed)
// execution.
func SpxIdemRecord(mock sqlmock.Sqlmock, route, key string) {
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), route, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// SpxIdemRelease queues the claim release a TOP-level DB_UNAVAILABLE writes
// (failDB sets kvDBFail; idempotent() must not record a transient failure).
func SpxIdemRelease(mock sqlmock.Sqlmock, route, key string) {
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), route, key).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

// SpxEligibility queues program + topic eligibility reads (E13).
func SpxEligibility(mock sqlmock.Sqlmock, pid, school int64, total, incomplete int) {
	mock.ExpectQuery(q("FROM programs WHERE id = ? AND school_id = ? AND is_active = TRUE")).
		WithArgs(pid, school).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Program A"))
	mock.ExpectQuery(q("WHERE program_id = ? AND school_id = ? AND is_active = TRUE")).
		WithArgs(pid, school).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(total))
	mock.ExpectQuery(q("WHERE t.program_id = ? AND t.school_id = ? AND t.is_active = TRUE")).
		WithArgs(pid, school).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(incomplete))
}

// SpxInsertSession queues the DRAFT session row insert (LastInsertId = sid).
func SpxInsertSession(mock sqlmock.Sqlmock, sid int64) {
	mock.ExpectExec(q("INSERT INTO sessions")).
		WillReturnResult(sqlmock.NewResult(sid, 1))
}

// SpxTopicsSnapshot queues the topic snapshot select + insert.
func SpxTopicsSnapshot(mock sqlmock.Sqlmock, pid, school int64) {
	mock.ExpectQuery(q("FROM topics WHERE program_id = ?")).
		WithArgs(pid, school).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "sort_order",
			"badge_name", "badge_image_path"}).AddRow(10, "Topik A", 1, nil, nil))
	mock.ExpectExec(q("INSERT INTO session_topics")).
		WillReturnResult(sqlmock.NewResult(51, 1))
}

// SpxActivitiesSnapshot queues the activity snapshot select + insert.
func SpxActivitiesSnapshot(mock sqlmock.Sqlmock, sid, pid, school int64) {
	mock.ExpectQuery(q("JOIN session_topics st ON st.topic_id = a.topic_id")).
		WithArgs(sid, school, pid, school).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "activity_id",
			"name", "indicator", "sort_order"}).AddRow(51, 101, "Aktivitas A", nil, 1))
	mock.ExpectExec(q("INSERT INTO session_activities")).
		WillReturnResult(sqlmock.NewResult(61, 1))
}

// SpxCreateSnapshot = session + topic + activity snapshot rows.
func SpxCreateSnapshot(mock sqlmock.Sqlmock, sid, pid, school int64) {
	SpxInsertSession(mock, sid)
	SpxTopicsSnapshot(mock, pid, school)
	SpxActivitiesSnapshot(mock, sid, pid, school)
}

// SpxCreateBody is the minimal valid create payload.
func SpxCreateBody() map[string]any {
	return map[string]any{
		"program_id": 9, "name": "Sesi A", "date": "2026-01-05",
		"start_time": "09:00:00", "end_time": "11:00:00", "location": "Lab",
	}
}

// SpxBeginLock queues Begin + the FOR UPDATE Draft row claim every group/
// member mutation runs behind.
func SpxBeginLock(mock sqlmock.Sqlmock, sid, school int64) {
	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(sid, school).WillReturnRows(statusRows("DRAFT"))
}

// SpxWipePhotos queues the photo path select + delete of one child's scope wipe.
func SpxWipePhotos(mock sqlmock.Sqlmock, sid, cid, school int64, paths ...string) {
	rows := sqlmock.NewRows([]string{"path"})
	for _, p := range paths {
		rows.AddRow(p)
	}
	args := SpxWipeArgs(sid, cid, school)
	mock.ExpectQuery(q("SELECT path FROM photos WHERE session_id = ?")).
		WithArgs(args...).WillReturnRows(rows)
	mock.ExpectExec(q("DELETE FROM photos WHERE session_id = ?")).
		WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
}

// SpxWipeTokens queues the consent + public-link token selects of the wipe.
func SpxWipeTokens(mock sqlmock.Sqlmock, sid, cid, school int64,
	consentRows, publicRows *sqlmock.Rows) {
	args := SpxWipeArgs(sid, cid, school)
	mock.ExpectQuery(q("SELECT id, child_id, token FROM consent_links")).
		WithArgs(args...).WillReturnRows(consentRows)
	mock.ExpectQuery(q("SELECT id, child_id, token, kind FROM public_links WHERE revoked_at IS NULL")).
		WithArgs(args...).WillReturnRows(publicRows)
}

// SpxWipeDeletes queues the five per-child DELETEs of the wipe.
func SpxWipeDeletes(mock sqlmock.Sqlmock, sid, cid, school int64) {
	args := SpxWipeArgs(sid, cid, school)
	for _, table := range []string{"assessments", "narratives", "report_missions",
		"consent_links", "public_links"} {
		mock.ExpectExec(q("DELETE FROM " + table + " WHERE session_id = ?")).
			WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
	}
}

// SpxLoadSession queues the detail/patch session row read.
func SpxLoadSession(mock sqlmock.Sqlmock, sid, school int64, status string, createdBy any) {
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(sid, school).
		WillReturnRows(sqlmock.NewRows([]string{"id", "program_id",
			"program_name_snap", "name", "description", "date", "start_time",
			"end_time", "location", "status", "created_by", "created_at"}).
			AddRow(sid, int64(9), "Program A", "Sesi A", nil, fixedTS, nil, nil, "Lab",
				status, createdBy, fixedTS))
}

// ---------------------------------------------------------------------------
// Guards before any domain SQL: malformed ids, missing school, bad body
// ---------------------------------------------------------------------------

// TestSpxMalformedIDsAreNotFoundWithoutSQL pins the no-oracle rule (06 §1
// E30) across every sessions route with :sid/:gid/:cid: a malformed id answers
// 200 NOT_FOUND after the authed user load ONLY — reaching any domain query
// would answer DB_UNAVAILABLE instead and fail the envelope assertion.
func TestSpxMalformedIDsAreNotFoundWithoutSQL(t *testing.T) {
	cases := []struct{ name, method, target string }{
		{"detail", "GET", "/api/sessions/xyz"},
		{"patch", "PATCH", "/api/sessions/xyz"},
		{"delete", "DELETE", "/api/sessions/xyz"},
		{"open", "POST", "/api/sessions/xyz/open"},
		{"group-list", "GET", "/api/sessions/xyz/groups"},
		{"group-create", "POST", "/api/sessions/xyz/groups"},
		{"group-patch", "PATCH", "/api/sessions/xyz/groups/1"},
		{"group-delete", "DELETE", "/api/sessions/xyz/groups/1"},
		{"members-add", "POST", "/api/sessions/xyz/groups/1/members"},
		{"member-remove", "DELETE", "/api/sessions/xyz/groups/1/members/1"},
		{"copy-candidates", "GET", "/api/sessions/xyz/children/1/copy-candidates"},
		{"copy-execute", "POST", "/api/sessions/xyz/children/1/copy"},
		{"bad-gid-group-patch", "PATCH", "/api/sessions/2/groups/abc"},
		{"bad-gid-group-delete", "DELETE", "/api/sessions/2/groups/abc"},
		{"bad-gid-members-add", "POST", "/api/sessions/2/groups/abc/members"},
		{"bad-gid-member-remove", "DELETE", "/api/sessions/2/groups/abc/members/30"},
		{"bad-cid-member-remove", "DELETE", "/api/sessions/2/groups/10/members/abc"},
		{"bad-cid-candidates", "GET", "/api/sessions/2/children/abc/copy-candidates"},
		{"bad-cid-copy", "POST", "/api/sessions/2/children/abc/copy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			h := sessionAdminHeaders(t, s, mock)
			res := doReq(t, app, tc.method, tc.target, "", h)
			requireErrEnvelope(t, res, CodeNotFound)
		})
	}
}

// TestSpxMissingSchoolIsValidBeforeSQL: SA may omit X-School-ID (02 §4), so
// every guarded write reaches its handler and fails requireSchool with
// VALIDATION before the first domain query.
func TestSpxMissingSchoolIsValidBeforeSQL(t *testing.T) {
	cases := []struct {
		name, method, target string
		payload              any
	}{
		{"create", "POST", "/api/sessions", SpxCreateBody()},
		{"patch", "PATCH", "/api/sessions/2", nil},
		{"delete", "DELETE", "/api/sessions/2", nil},
		{"open", "POST", "/api/sessions/2/open", nil},
		{"group-create", "POST", "/api/sessions/2/groups", nil},
		{"group-patch", "PATCH", "/api/sessions/2/groups/10", nil},
		{"group-delete", "DELETE", "/api/sessions/2/groups/10", nil},
		{"members-add", "POST", "/api/sessions/2/groups/10/members", nil},
		{"member-remove", "DELETE", "/api/sessions/2/groups/10/members/30", nil},
		{"copy-execute", "POST", "/api/sessions/2/children/30/copy", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			expectSAAuth(mock, 0)
			h := hSABare(t, s)
			var res httpRes
			if tc.payload != nil {
				res = jsonReq(t, app, tc.method, tc.target, tc.payload, h)
			} else {
				res = doReq(t, app, tc.method, tc.target, "", h)
			}
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

// TestSpxMalformedBodyIsValidBeforeSQL: bindBody rejects a non-JSON body with
// VALIDATION before school guards touch SQL (01 §2 Bind-first).
func TestSpxMalformedBodyIsValidBeforeSQL(t *testing.T) {
	cases := []struct{ name, method, target string }{
		{"create", "POST", "/api/sessions"},
		{"patch", "PATCH", "/api/sessions/2"},
		{"group-create", "POST", "/api/sessions/2/groups"},
		{"group-patch", "PATCH", "/api/sessions/2/groups/10"},
		{"members-add", "POST", "/api/sessions/2/groups/10/members"},
		{"copy-execute", "POST", "/api/sessions/2/children/30/copy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			h := sessionAdminHeaders(t, s, mock)
			res := doReq(t, app, tc.method, tc.target, "{not json", h)
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

// TestSpxEmptyBodyValidationBeforeSQL covers the empty/blank payload gates of
// group PATCH and members ADD: VALIDATION with no Tx ever begun.
func TestSpxEmptyBodyValidationBeforeSQL(t *testing.T) {
	t.Run("group patch without any field", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		res := jsonReq(t, app, "PATCH", "/api/sessions/2/groups/10",
			map[string]any{}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("group patch with blank name", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		res := jsonReq(t, app, "PATCH", "/api/sessions/2/groups/10",
			map[string]any{"name": "   "}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("members add with empty child list", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{}}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("members add with zero child id", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{0}}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
}

// ---------------------------------------------------------------------------
// GET /api/sessions (05 §3 paging + FAS scope)
// ---------------------------------------------------------------------------

// TestSpxSessionListRejectsBadPagingBeforeSQL: listParams failure (bad
// per_page) writes VALIDATION without a single domain query.
func TestSpxSessionListRejectsBadPagingBeforeSQL(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	res := doReq(t, app, "GET", "/api/sessions?per_page=7", "", h)
	requireErrEnvelope(t, res, CodeValidation)
}

// TestSpxSessionListSAWithoutSchoolDropsSchoolPredicate: school 0 (SA without
// X-School-ID) must read across schools — the COUNT runs with ZERO args and
// the row query carries "WHERE 1=1 ORDER BY" (a stray school predicate would
// fail the match → DB_UNAVAILABLE → envelope assertion).
func TestSpxSessionListSAWithoutSchoolDropsSchoolPredicate(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectSAAuth(mock, 0)
	h := hSABare(t, s)

	mock.ExpectQuery(q("COUNT(*) FROM sessions s")).WithoutArgs().
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(3))
	mock.ExpectQuery(q("WHERE 1=1 ORDER BY s.date desc, s.id ASC")).
		WithArgs(int64(20), int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "program_id",
			"program_name_snap", "name", "description", "date", "start_time",
			"end_time", "location", "status", "group_count", "member_count",
			"created_at"}).
			AddRow(41, 9, "Program A", "Sesi A", nil, fixedTS, nil, nil, "Lab",
				"DRAFT", 0, 0, fixedTS))

	res := doReq(t, app, "GET", "/api/sessions", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 1)
	require.Equal(t, "Sesi A", data[0].(map[string]any)["name"])
	meta := envelopeMap(t, res.body)["meta"].(map[string]any)
	require.Equal(t, float64(3), meta["total"])
}

// TestSpxSessionListStatusAndSearchFilters: ?status= + ?q= ride into BOTH the
// COUNT and the row query (args asserted), the paged envelope stays intact.
func TestSpxSessionListStatusAndSearchFilters(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("COUNT(*) FROM sessions s")).
		WithArgs(int64(1), "DRAFT", "% sekolah%").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery(q("ORDER BY s.date desc, s.id ASC")).
		WithArgs(int64(1), "DRAFT", "% sekolah%", int64(20), int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "program_id",
			"program_name_snap", "name", "description", "date", "start_time",
			"end_time", "location", "status", "group_count", "member_count",
			"created_at"}).
			AddRow(7, 9, "Program A", "Sesi sekolah", nil, fixedTS, nil, nil, "Lab",
				"DRAFT", 1, 2, fixedTS))

	res := doReq(t, app, "GET", "/api/sessions?status=DRAFT&q=%20sekolah", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 1)
	require.Equal(t, "DRAFT", data[0].(map[string]any)["status"])
}

// TestSpxSessionListFASOwnSessionsUseINFilter: a facilitator owning groups
// gets the s.id IN (...) restriction on both queries (own ≥1 group only).
func TestSpxSessionListFASOwnSessionsUseINFilter(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := assessmentFASHeaders(t, s, mock)

	mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).
			AddRow(10, 2).
			AddRow(11, 3))
	mock.ExpectQuery(q("COUNT(*) FROM sessions s")).
		WithArgs(int64(1), int64(2), int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery(q("ORDER BY s.date desc, s.id ASC")).
		WithArgs(int64(1), int64(2), int64(3), int64(20), int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "program_id",
			"program_name_snap", "name", "description", "date", "start_time",
			"end_time", "location", "status", "group_count", "member_count",
			"created_at"}).
			AddRow(2, 9, "Program A", "Sesi A", nil, fixedTS, nil, nil, "Lab",
				"OPENED", 1, 1, fixedTS))

	res := doReq(t, app, "GET", "/api/sessions", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 1)
	require.Equal(t, float64(2), data[0].(map[string]any)["id"])
}

// TestSpxSessionListFailureBranches: every read failure maps to
// DB_UNAVAILABLE (200 envelope, never a500).
func TestSpxSessionListFailureBranches(t *testing.T) {
	t.Run("FAS scope query fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
			WithArgs(int64(3), int64(1)).WillReturnError(SpxErr)
		res := doReq(t, app, "GET", "/api/sessions", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("COUNT fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("COUNT(*) FROM sessions s")).
			WithArgs(int64(1)).WillReturnError(SpxErr)
		res := doReq(t, app, "GET", "/api/sessions", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("row query fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("COUNT(*) FROM sessions s")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		mock.ExpectQuery(q("ORDER BY s.date desc, s.id ASC")).
			WillReturnError(SpxErr)
		res := doReq(t, app, "GET", "/api/sessions", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("COUNT(*) FROM sessions s")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		mock.ExpectQuery(q("ORDER BY s.date desc, s.id ASC")).
			WillReturnRows(sqlmock.NewRows([]string{"id", "program_id",
				"program_name_snap", "name", "description", "date", "start_time",
				"end_time", "location", "status", "group_count", "member_count",
				"created_at"}).
				AddRow("not-an-id", 9, "Program A", "Sesi A", nil, fixedTS, nil, nil,
					"Lab", "DRAFT", 1, 2, fixedTS))
		res := doReq(t, app, "GET", "/api/sessions", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("row iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("COUNT(*) FROM sessions s")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		rows := sqlmock.NewRows([]string{"id", "program_id",
			"program_name_snap", "name", "description", "date", "start_time",
			"end_time", "location", "status", "group_count", "member_count",
			"created_at"}).
			AddRow(41, 9, "Program A", "Sesi A", nil, fixedTS, nil, nil, "Lab",
				"DRAFT", 1, 2, fixedTS).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("ORDER BY s.date desc, s.id ASC")).WillReturnRows(rows)
		res := doReq(t, app, "GET", "/api/sessions", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// POST /api/sessions (FL-07 / decision 17)
// ---------------------------------------------------------------------------

// TestSpxCreateValidationBeforeAnyWriteSQL: the payload gates run BEFORE the
// idempotency claim — any query beyond the authed user load would answer
// DB_UNAVAILABLE and fail the envelope assertion.
func TestSpxCreateValidationBeforeAnyWriteSQL(t *testing.T) {
	t.Run("malformed body", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		res := doReq(t, app, "POST", "/api/sessions", "{oops", h)
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("malformed date", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		body := SpxCreateBody()
		body["date"] = "05-01-2026"
		res := jsonReq(t, app, "POST", "/api/sessions", body, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("inverted clock range", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		body := SpxCreateBody()
		body["start_time"] = "11:00:00"
		body["end_time"] = "09:00:00"
		res := jsonReq(t, app, "POST", "/api/sessions", body, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("blank group name", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		body := SpxCreateBody()
		body["groups"] = []map[string]any{
			{"name": "   ", "facilitator_id": 3, "child_ids": []int64{}},
		}
		res := jsonReq(t, app, "POST", "/api/sessions", body, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("zero child id", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		body := SpxCreateBody()
		body["groups"] = []map[string]any{
			{"name": "G1", "facilitator_id": 3, "child_ids": []int64{0}},
		}
		res := jsonReq(t, app, "POST", "/api/sessions", body, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
}

// TestSpxCreateUnknownProgramRollsBackClaimedTx: E13 program gate → the
// claimed Tx rolls back and the NOT_FOUND envelope is RECORDED for replay.
func TestSpxCreateUnknownProgramRollsBackClaimedTx(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	key := "spx-create-nf"

	SpxIdemClaim(mock, spxCreateRoute, key)
	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM programs WHERE id = ? AND school_id = ? AND is_active = TRUE")).
		WithArgs(int64(9), int64(1)).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	SpxIdemRecord(mock, spxCreateRoute, key)

	res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
		withIdem(h, key))
	requireErrEnvelope(t, res, CodeNotFound)
}

// withIdem attaches an X-Idempotency-Key header to a header map.
func withIdem(h map[string]string, key string) map[string]string {
	h["X-Idempotency-Key"] = key
	return h
}

// TestSpxCreateEligibilityCountFailuresReleaseClaim: a DB failure in either
// topic eligibility count is a TOP-level DB_UNAVAILABLE — the claim is
// released (never recorded) so the retry re-executes.
func TestSpxCreateEligibilityCountFailuresReleaseClaim(t *testing.T) {
	t.Run("topics COUNT fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-ec1"
		SpxIdemClaim(mock, spxCreateRoute, key)
		mock.ExpectBegin()
		mock.ExpectQuery(q("FROM programs WHERE id = ? AND school_id = ? AND is_active = TRUE")).
			WithArgs(int64(9), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Program A"))
		mock.ExpectQuery(q("WHERE program_id = ? AND school_id = ? AND is_active = TRUE")).
			WithArgs(int64(9), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
			withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("incomplete-topic COUNT fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-ec2"
		SpxIdemClaim(mock, spxCreateRoute, key)
		mock.ExpectBegin()
		mock.ExpectQuery(q("FROM programs WHERE id = ? AND school_id = ? AND is_active = TRUE")).
			WithArgs(int64(9), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Program A"))
		mock.ExpectQuery(q("WHERE program_id = ? AND school_id = ? AND is_active = TRUE")).
			WithArgs(int64(9), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		mock.ExpectQuery(q("WHERE t.program_id = ? AND t.school_id = ? AND t.is_active = TRUE")).
			WithArgs(int64(9), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
			withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxCreateSessionInsertFailureReleasesClaim: the snapshot INSERT itself
// failing is a TOP-level DB failure → rollback + claim release, no recording.
func TestSpxCreateSessionInsertFailureReleasesClaim(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	key := "spx-create-ins"

	SpxIdemClaim(mock, spxCreateRoute, key)
	mock.ExpectBegin()
	SpxEligibility(mock, 9, 1, 1, 0)
	mock.ExpectExec(q("INSERT INTO sessions")).WillReturnError(SpxErr)
	mock.ExpectRollback()
	SpxIdemRelease(mock, spxCreateRoute, key)

	res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
		withIdem(h, key))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestSpxCreateSnapshotReadAndWriteFailuresReleaseClaim: a failure while
// reading/inserting the topic+activity snapshots aborts the Tx with
// DB_UNAVAILABLE (claim released).
func TestSpxCreateSnapshotReadAndWriteFailuresReleaseClaim(t *testing.T) {
	t.Run("topics select fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-t1"
		SpxIdemClaim(mock, spxCreateRoute, key)
		mock.ExpectBegin()
		SpxEligibility(mock, 9, 1, 1, 0)
		SpxInsertSession(mock, 41)
		mock.ExpectQuery(q("FROM topics WHERE program_id = ?")).
			WithArgs(int64(9), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
			withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("topic row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-t2"
		SpxIdemClaim(mock, spxCreateRoute, key)
		mock.ExpectBegin()
		SpxEligibility(mock, 9, 1, 1, 0)
		SpxInsertSession(mock, 41)
		mock.ExpectQuery(q("FROM topics WHERE program_id = ?")).
			WithArgs(int64(9), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "sort_order",
				"badge_name", "badge_image_path"}).
				AddRow("not-an-id", "Topik A", 1, nil, nil))
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
			withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("topic iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-t3"
		SpxIdemClaim(mock, spxCreateRoute, key)
		mock.ExpectBegin()
		SpxEligibility(mock, 9, 1, 1, 0)
		SpxInsertSession(mock, 41)
		rows := sqlmock.NewRows([]string{"id", "name", "sort_order",
			"badge_name", "badge_image_path"}).
			AddRow(10, "Topik A", 1, nil, nil).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("FROM topics WHERE program_id = ?")).
			WithArgs(int64(9), int64(1)).WillReturnRows(rows)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
			withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("session_topics insert fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-t4"
		SpxIdemClaim(mock, spxCreateRoute, key)
		mock.ExpectBegin()
		SpxEligibility(mock, 9, 1, 1, 0)
		SpxInsertSession(mock, 41)
		mock.ExpectQuery(q("FROM topics WHERE program_id = ?")).
			WithArgs(int64(9), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "sort_order",
				"badge_name", "badge_image_path"}).AddRow(10, "Topik A", 1, nil, nil))
		mock.ExpectExec(q("INSERT INTO session_topics")).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
			withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxCreateActivitySnapshotFailuresReleaseClaim: activities select /
// scan / insert / iteration failures each abort the claimed Tx.
func TestSpxCreateActivitySnapshotFailuresReleaseClaim(t *testing.T) {
	prefix := func(t *testing.T, mock sqlmock.Sqlmock, key string) {
		t.Helper()
		SpxIdemClaim(mock, spxCreateRoute, key)
		mock.ExpectBegin()
		SpxEligibility(mock, 9, 1, 1, 0)
		SpxInsertSession(mock, 41)
		SpxTopicsSnapshot(mock, 9, 1)
	}
	t.Run("activities select fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-a1"
		prefix(t, mock, key)
		mock.ExpectQuery(q("JOIN session_topics st ON st.topic_id = a.topic_id")).
			WithArgs(int64(41), int64(1), int64(9), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
			withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("activity row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-a2"
		prefix(t, mock, key)
		mock.ExpectQuery(q("JOIN session_topics st ON st.topic_id = a.topic_id")).
			WithArgs(int64(41), int64(1), int64(9), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id",
				"activity_id", "name", "indicator", "sort_order"}).
				AddRow("not-an-id", 101, "Aktivitas A", nil, 1))
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
			withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("session_activities insert fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-a3"
		prefix(t, mock, key)
		mock.ExpectQuery(q("JOIN session_topics st ON st.topic_id = a.topic_id")).
			WithArgs(int64(41), int64(1), int64(9), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id",
				"activity_id", "name", "indicator", "sort_order"}).
				AddRow(51, 101, "Aktivitas A", nil, 1))
		mock.ExpectExec(q("INSERT INTO session_activities")).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
			withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("activity iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-a4"
		prefix(t, mock, key)
		rows := sqlmock.NewRows([]string{"session_topic_id", "activity_id",
			"name", "indicator", "sort_order"}).
			AddRow(51, 101, "Aktivitas A", nil, 1).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("JOIN session_topics st ON st.topic_id = a.topic_id")).
			WithArgs(int64(41), int64(1), int64(9), int64(1)).WillReturnRows(rows)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
			withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// POST /api/sessions — child + owner gates inside the claimed Tx
// ---------------------------------------------------------------------------

// SpxCreateBodyWithGroup returns the valid create payload with one group.
func SpxCreateBodyWithGroup(childIDs []int64, facilitatorID int64) map[string]any {
	b := SpxCreateBody()
	b["groups"] = []map[string]any{{
		"name": "G1", "facilitator_id": facilitatorID, "child_ids": childIDs,
	}}
	return b
}

// TestSpxCreateChildLookupFailures: the FOR UPDATE child lock either aborts
// the Tx (DB failure → claim released) or supplies the up-front CHILD_* gates.
func TestSpxCreateChildLookupFailures(t *testing.T) {
	prefix := func(t *testing.T, mock sqlmock.Sqlmock, key string) {
		t.Helper()
		SpxIdemClaim(mock, spxCreateRoute, key)
		mock.ExpectBegin()
		SpxEligibility(mock, 9, 1, 1, 0)
		SpxCreateSnapshot(mock, 41, 9, 1)
	}
	t.Run("children select fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-c1"
		prefix(t, mock, key)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(30), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions",
			SpxCreateBodyWithGroup([]int64{30}, 3), withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("child row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-c2"
		prefix(t, mock, key)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}).
				AddRow("not-an-id", "Ayu", true))
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions",
			SpxCreateBodyWithGroup([]int64{30}, 3), withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("child iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-c3"
		prefix(t, mock, key)
		rows := sqlmock.NewRows([]string{"id", "name", "is_active"}).
			AddRow(30, "Ayu", true).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(30), int64(1)).WillReturnRows(rows)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions",
			SpxCreateBodyWithGroup([]int64{30}, 3), withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("unknown child is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-c4"
		prefix(t, mock, key)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}))
		mock.ExpectRollback()
		SpxIdemRecord(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions",
			SpxCreateBodyWithGroup([]int64{30}, 3), withIdem(h, key))
		requireErrEnvelope(t, res, CodeNotFound)
	})
}

// TestSpxCreateOwnerGateBranches: requestOwner's four outcomes (ok / DB
// failure / unknown-cross-school / wrong-role / inactive) plus the zero-id and
// group-insert gates, each with the right envelope and claim fate.
func TestSpxCreateOwnerGateBranches(t *testing.T) {
	prefix := func(t *testing.T, mock sqlmock.Sqlmock, key string) {
		t.Helper()
		SpxIdemClaim(mock, spxCreateRoute, key)
		mock.ExpectBegin()
		SpxEligibility(mock, 9, 1, 1, 0)
		SpxCreateSnapshot(mock, 41, 9, 1)
	}
	ownerRow := func(role, decision string, active bool, school any) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"role", "decision", "is_active", "school_id"}).
			AddRow(role, decision, active, school)
	}
	t.Run("zero facilitator id is validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-o1"
		prefix(t, mock, key)
		mock.ExpectRollback()
		SpxIdemRecord(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions",
			SpxCreateBodyWithGroup(nil, 0), withIdem(h, key))
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("owner read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-o2"
		prefix(t, mock, key)
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions",
			SpxCreateBodyWithGroup(nil, 3), withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("unknown owner is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-o3"
		prefix(t, mock, key)
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		SpxIdemRecord(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions",
			SpxCreateBodyWithGroup(nil, 3), withIdem(h, key))
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("cross-school owner is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-o4"
		prefix(t, mock, key)
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).
			WillReturnRows(ownerRow("FASILITATOR", "APPROVED", true, nil))
		mock.ExpectRollback()
		SpxIdemRecord(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions",
			SpxCreateBodyWithGroup(nil, 3), withIdem(h, key))
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("non-fasilitator owner is validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-o5"
		prefix(t, mock, key)
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).
			WillReturnRows(ownerRow("ADMIN", "APPROVED", true, int64(1)))
		mock.ExpectRollback()
		SpxIdemRecord(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions",
			SpxCreateBodyWithGroup(nil, 3), withIdem(h, key))
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("owner name read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-o6"
		prefix(t, mock, key)
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).
			WillReturnRows(ownerRow("FASILITATOR", "APPROVED", true, int64(1)))
		mock.ExpectQuery(q("SELECT name FROM users WHERE id = ?")).
			WithArgs(int64(3)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions",
			SpxCreateBodyWithGroup(nil, 3), withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("group insert fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-o7"
		prefix(t, mock, key)
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).
			WillReturnRows(ownerRow("FASILITATOR", "APPROVED", true, int64(1)))
		mock.ExpectQuery(q("SELECT name FROM users WHERE id = ?")).
			WithArgs(int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Fasil"))
		mock.ExpectExec(q("INSERT INTO session_groups")).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions",
			SpxCreateBodyWithGroup(nil, 3), withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxCreateMemberGateBranches: the member loop's CHILD_INACTIVE /
// CHILD_DUPLICATE (1062) / DB-failure outcomes all undo the whole Tx.
func TestSpxCreateMemberGateBranches(t *testing.T) {
	prefix := func(t *testing.T, mock sqlmock.Sqlmock, key string, active bool) {
		t.Helper()
		SpxIdemClaim(mock, spxCreateRoute, key)
		mock.ExpectBegin()
		SpxEligibility(mock, 9, 1, 1, 0)
		SpxCreateSnapshot(mock, 41, 9, 1)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}).
				AddRow(30, "Ayu", active))
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"role", "decision",
				"is_active", "school_id"}).
				AddRow("FASILITATOR", "APPROVED", true, int64(1)))
		mock.ExpectQuery(q("SELECT name FROM users WHERE id = ?")).
			WithArgs(int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Fasil"))
		mock.ExpectExec(q("INSERT INTO session_groups")).
			WillReturnResult(sqlmock.NewResult(101, 1))
	}
	t.Run("inactive child fails the whole Tx", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-m1"
		prefix(t, mock, key, false)
		mock.ExpectRollback()
		SpxIdemRecord(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions",
			SpxCreateBodyWithGroup([]int64{30}, 3), withIdem(h, key))
		requireErrEnvelope(t, res, CodeChildInactive)
	})
	t.Run("member insert duplicate is child duplicate", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-m2"
		prefix(t, mock, key, true)
		mock.ExpectExec(q("INSERT INTO session_members")).
			WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
		mock.ExpectRollback()
		SpxIdemRecord(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions",
			SpxCreateBodyWithGroup([]int64{30}, 3), withIdem(h, key))
		requireErrEnvelope(t, res, CodeChildDuplicate)
	})
	t.Run("member insert failure releases the claim", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-m3"
		prefix(t, mock, key, true)
		mock.ExpectExec(q("INSERT INTO session_members")).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions",
			SpxCreateBodyWithGroup([]int64{30}, 3), withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxCreateCommitFailureReleasesClaim: a failing COMMIT is a top-level DB
// failure — rollback, release, DB_UNAVAILABLE (the create is retryable).
func TestSpxCreateCommitFailureReleasesClaim(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	key := "spx-create-commit"

	SpxIdemClaim(mock, spxCreateRoute, key)
	mock.ExpectBegin()
	SpxEligibility(mock, 9, 1, 1, 0)
	SpxCreateSnapshot(mock, 41, 9, 1)
	mock.ExpectCommit().WillReturnError(SpxErr)
	// Commit marks the Tx done: the deferred Rollback is a driver no-op, so
	// the very next statement is the idempotency claim release.
	SpxIdemRelease(mock, spxCreateRoute, key)

	res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
		withIdem(h, key))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestSpxCreateWithGroupsPublishesPerMemberCopyHint: create-with-groups
// commits session+snapshots+members in ONE Tx, then publishes ONE
// session.changed frame per new member carrying the copy_available hint
// (FL-07 / 02 §2c) and records the {session_id} envelope for replay.
func TestSpxCreateWithGroupsPublishesPerMemberCopyHint(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	key := "spx-create-groups"
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	SpxIdemClaim(mock, spxCreateRoute, key)
	mock.ExpectBegin()
	SpxEligibility(mock, 9, 1, 1, 0)
	SpxCreateSnapshot(mock, 41, 9, 1)
	mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}).
			AddRow(30, "Ayu", true))
	mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"role", "decision",
			"is_active", "school_id"}).
			AddRow("FASILITATOR", "APPROVED", true, int64(1)))
	mock.ExpectQuery(q("SELECT name FROM users WHERE id = ?")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Fasil"))
	mock.ExpectExec(q("INSERT INTO session_groups")).
		WillReturnResult(sqlmock.NewResult(101, 1))
	mock.ExpectExec(q("INSERT INTO session_members")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	// Post-commit copy hint: child 30 already sits in another session of the
	// same program → the frame ships copy_available=true.
	mock.ExpectQuery(q("FROM sessions other")).
		WithArgs(int64(1), int64(41), int64(41), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(1))
	SpxIdemRecord(mock, spxCreateRoute, key)

	res := jsonReq(t, app, "POST", "/api/sessions",
		SpxCreateBodyWithGroup([]int64{30}, 3), withIdem(h, key))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(41), data["session_id"])

	f := covNextFrame(t, sub, "session.changed per new member")
	require.Equal(t, "session.changed", f.Entity)
	require.Equal(t, "created", f.Action)
	require.NotNil(t, f.SessionID)
	require.Equal(t, int64(41), *f.SessionID)
	require.Equal(t, int64(30), f.ChildID, "the frame names the new member")
	require.NotNil(t, f.CopyAvailable, "the hint is attached to the frame")
	require.True(t, *f.CopyAvailable, "child 30 has another same-program session")
	covNoFrame(t, sub, "one frame per created member")
}

// ---------------------------------------------------------------------------
// GET /api/sessions/:sid — ONE detail response
// ---------------------------------------------------------------------------

// SpxDetailEmptySnapshot queues the empty topic/activity snapshot reads.
func SpxDetailEmptySnapshot(mock sqlmock.Sqlmock, sid, school int64) {
	mock.ExpectQuery(q("FROM session_topics st WHERE st.session_id = ?")).
		WithArgs(sid, school).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_id", "name", "sort_order"}))
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(sid, school).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id",
			"activity_id", "name", "indicator", "sort_order"}))
}

// SpxDetailEmptyGroups queues the empty group + member reads (no owners query
// runs without a group row).
func SpxDetailEmptyGroups(mock sqlmock.Sqlmock, sid, school int64) {
	mock.ExpectQuery(q("FROM session_groups g")).
		WithArgs(sid, school).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id"}))
	mock.ExpectQuery(q("FROM session_members sm")).
		WithArgs(sid, school).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "child_id", "name",
			"age", "school", "consent"}))
}

// SpxDetailScope queues the wipe-counts object read (9 session probes +
// wa_outbox school predicate + public_links probe + outer id + school cond).
func SpxDetailScope(mock sqlmock.Sqlmock, sid, school int64, rows *sqlmock.Rows) *sqlmock.ExpectedQuery {
	args := make([]driver.Value, 0, 13)
	for range 9 {
		args = append(args, sid)
	}
	args = append(args, school, sid, sid, school)
	exp := mock.ExpectQuery(q("(SELECT COUNT(*) FROM session_topics WHERE session_id = ?)")).
		WithArgs(args...)
	if rows != nil {
		exp.WillReturnRows(rows)
	}
	return exp
}

// SpxScopeZeroRows builds the zeroed scope-count row.
func SpxScopeZeroRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"topics", "groups", "members", "assessments",
		"photos", "narratives", "links_report", "links_gallery", "links_consent",
		"reports_sent"}).AddRow(0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
}

// TestSpxDetailFailureBranches maps every detail read failure to its envelope
// (NOT_FOUND for an unknown session, DB_UNAVAILABLE for driver failures).
func TestSpxDetailFailureBranches(t *testing.T) {
	t.Run("unknown session is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("session read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
		res := doReq(t, app, "GET", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("fas scope read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "OPENED", int64(2))
		mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
			WithArgs(int64(3), int64(1)).WillReturnError(SpxErr)
		res := doReq(t, app, "GET", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("fas scope row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "OPENED", int64(2))
		mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).
				AddRow("not-an-id", 2))
		res := doReq(t, app, "GET", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("foreign session is not found for fas", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "OPENED", int64(2))
		// The facilitator owns groups only in session 3 — session 2 invisible.
		mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).
				AddRow(11, 3))
		res := doReq(t, app, "GET", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("topics read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		mock.ExpectQuery(q("FROM session_topics st WHERE st.session_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
		res := doReq(t, app, "GET", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("topic row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		mock.ExpectQuery(q("FROM session_topics st WHERE st.session_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "topic_id", "name", "sort_order"}).
				AddRow("not-an-id", 9, "Topik A", 1))
		res := doReq(t, app, "GET", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("topic iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		rows := sqlmock.NewRows([]string{"id", "topic_id", "name", "sort_order"}).
			AddRow(5, 9, "Topik A", 1).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("FROM session_topics st WHERE st.session_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(rows)
		res := doReq(t, app, "GET", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("activities read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		mock.ExpectQuery(q("FROM session_topics st WHERE st.session_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "topic_id", "name", "sort_order"}))
		mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
		res := doReq(t, app, "GET", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("activity row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		mock.ExpectQuery(q("FROM session_topics st WHERE st.session_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "topic_id", "name", "sort_order"}))
		mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id",
				"activity_id", "name", "indicator", "sort_order"}).
				AddRow("not-an-id", 11, 101, "Aktivitas A", nil, 1))
		res := doReq(t, app, "GET", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("activity iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		mock.ExpectQuery(q("FROM session_topics st WHERE st.session_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "topic_id", "name", "sort_order"}))
		rows := sqlmock.NewRows([]string{"session_topic_id", "id", "activity_id",
			"name", "indicator", "sort_order"}).
			AddRow(5, 11, 101, "Aktivitas A", nil, 1).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(rows)
		res := doReq(t, app, "GET", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("group assembly fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		SpxDetailEmptySnapshot(mock, 2, 1)
		mock.ExpectQuery(q("FROM session_groups g")).
			WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
		res := doReq(t, app, "GET", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("scope row missing is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		SpxDetailEmptySnapshot(mock, 2, 1)
		SpxDetailEmptyGroups(mock, 2, 1)
		SpxDetailScope(mock, 2, 1, sqlmock.NewRows([]string{"topics", "groups",
			"members", "assessments", "photos", "narratives", "links_report",
			"links_gallery", "links_consent", "reports_sent"}))
		res := doReq(t, app, "GET", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("scope counts read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		SpxDetailEmptySnapshot(mock, 2, 1)
		SpxDetailEmptyGroups(mock, 2, 1)
		SpxDetailScope(mock, 2, 1, nil).WillReturnError(SpxErr)
		res := doReq(t, app, "GET", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxDetailDropsOrphanActivityAndNullCreator: an activity row whose
// session_topic_id matches no snapshot topic is silently dropped (never
// attached to a wrong topic), a NULL created_by stays JSON null, and the SA
// scope object reports its zero counts.
func TestSpxDetailDropsOrphanActivityAndNullCreator(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	SpxLoadSession(mock, 2, 1, "DRAFT", nil)
	mock.ExpectQuery(q("FROM session_topics st WHERE st.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_id", "name", "sort_order"}).
			AddRow(5, 9, "Topik A", 1))
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id",
			"activity_id", "name", "indicator", "sort_order"}).
			AddRow(999, 11, 101, "Orphan", nil, 1))
	SpxDetailEmptyGroups(mock, 2, 1)
	SpxDetailScope(mock, 2, 1, SpxScopeZeroRows())

	res := doReq(t, app, "GET", "/api/sessions/2", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)

	sess := data["session"].(map[string]any)
	require.Contains(t, sess, "created_by")
	require.Nil(t, sess["created_by"], "a NULL creator stays null on the wire")

	topics := data["topics"].([]any)
	require.Len(t, topics, 1)
	require.Empty(t, topics[0].(map[string]any)["activities"],
		"an activity without its snapshot topic must be dropped")

	require.Contains(t, data, "scope")
	require.Equal(t, float64(0), data["scope"].(map[string]any)["photos"])
}

// ---------------------------------------------------------------------------
// GET /api/sessions/:sid/groups — readable in any status (02 §2b)
// ---------------------------------------------------------------------------

// TestSpxGroupListFailureBranches: session/scope/assembly read failures map
// to their envelopes (200), never a500.
func TestSpxGroupListFailureBranches(t *testing.T) {
	t.Run("unknown session is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
			WithArgs(int64(7), int64(1)).WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/api/sessions/7/groups", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("status read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
			WithArgs(int64(7), int64(1)).WillReturnError(SpxErr)
		res := doReq(t, app, "GET", "/api/sessions/7/groups", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("fas scope read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
			WithArgs(int64(3), int64(1)).WillReturnError(SpxErr)
		res := doReq(t, app, "GET", "/api/sessions/2/groups", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("fas scope row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := assessmentFASHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).
				AddRow("not-an-id", 2))
		res := doReq(t, app, "GET", "/api/sessions/2/groups", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("group read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("FROM session_groups g")).
			WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
		res := doReq(t, app, "GET", "/api/sessions/2/groups", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("group row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("FROM session_groups g")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id"}).
				AddRow("not-an-id", "Kelompok", 3))
		res := doReq(t, app, "GET", "/api/sessions/2/groups", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("group iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		rows := sqlmock.NewRows([]string{"id", "name", "facilitator_id"}).
			AddRow(10, "Kelompok", 3).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("FROM session_groups g")).
			WithArgs(int64(2), int64(1)).WillReturnRows(rows)
		res := doReq(t, app, "GET", "/api/sessions/2/groups", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("member read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("FROM session_groups g")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id"}).
				AddRow(10, "Kelompok", 3))
		mock.ExpectQuery(q("FROM session_members sm")).
			WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
		res := doReq(t, app, "GET", "/api/sessions/2/groups", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("member row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("FROM session_groups g")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id"}).
				AddRow(10, "Kelompok", 3))
		mock.ExpectQuery(q("FROM session_members sm")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"group_id", "child_id",
				"name", "age", "school", "consent"}).
				AddRow(10, "not-an-id", "Ayu", 8, "SD", nil))
		res := doReq(t, app, "GET", "/api/sessions/2/groups", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("member iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("FROM session_groups g")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id"}).
				AddRow(10, "Kelompok", 3))
		rows := sqlmock.NewRows([]string{"group_id", "child_id", "name", "age",
			"school", "consent"}).
			AddRow(10, 30, "Ayu", 8, "SD", nil).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("FROM session_members sm")).
			WithArgs(int64(2), int64(1)).WillReturnRows(rows)
		res := doReq(t, app, "GET", "/api/sessions/2/groups", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("owner read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("FROM session_groups g")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id"}).
				AddRow(10, "Kelompok", 3))
		mock.ExpectQuery(q("FROM session_members sm")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"group_id", "child_id",
				"name", "age", "school", "consent"}))
		mock.ExpectQuery(q("SELECT id, name FROM users")).
			WithArgs(int64(3)).WillReturnError(SpxErr)
		res := doReq(t, app, "GET", "/api/sessions/2/groups", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("owner row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("FROM session_groups g")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id"}).
				AddRow(10, "Kelompok", 3))
		mock.ExpectQuery(q("FROM session_members sm")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"group_id", "child_id",
				"name", "age", "school", "consent"}))
		mock.ExpectQuery(q("SELECT id, name FROM users")).
			WithArgs(int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).
				AddRow("not-an-id", "Fasil"))
		res := doReq(t, app, "GET", "/api/sessions/2/groups", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("owner iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("FROM session_groups g")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id"}).
				AddRow(10, "Kelompok", 3))
		mock.ExpectQuery(q("FROM session_members sm")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"group_id", "child_id",
				"name", "age", "school", "consent"}))
		rows := sqlmock.NewRows([]string{"id", "name"}).
			AddRow(3, "Fasil").
			RowError(0, SpxErr)
		mock.ExpectQuery(q("SELECT id, name FROM users")).
			WithArgs(int64(3)).WillReturnRows(rows)
		res := doReq(t, app, "GET", "/api/sessions/2/groups", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxGroupListAdminFullShape: ADMIN sees full own-group objects — owner,
// consent counters and members assembled in one pass.
func TestSpxGroupListAdminFullShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("FROM session_groups g")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id"}).
			AddRow(10, "Kelompok Merah", 3).
			AddRow(11, "Tanpa Owner", nil))
	mock.ExpectQuery(q("FROM session_members sm")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "child_id", "name",
			"age", "school", "consent"}).
			AddRow(10, 30, "Ayu", 8, "SD Bintang", "SETUJU").
			AddRow(10, 31, "Budi", 9, "SD Bintang", nil))
	mock.ExpectQuery(q("SELECT id, name FROM users")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(3, "Fasil"))

	res := doReq(t, app, "GET", "/api/sessions/2/groups", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 2)

	g1 := data[0].(map[string]any)
	require.Equal(t, float64(10), g1["gid"])
	require.Equal(t, true, g1["own"])
	owner := g1["owner"].(map[string]any)
	require.Equal(t, "Fasil", owner["name"])
	require.Equal(t, false, g1["owner_missing"])
	require.Equal(t, float64(2), g1["member_count"])
	consent := g1["consent"].(map[string]any)
	require.Equal(t, float64(1), consent["SETUJU"])
	require.Equal(t, float64(0), consent["DITOLAK"])
	members := g1["members"].([]any)
	require.Len(t, members, 2)
	require.Nil(t, members[1].(map[string]any)["consent"],
		"consent never sent stays null on the wire")

	g2 := data[1].(map[string]any)
	require.Equal(t, true, g2["own"])
	require.Equal(t, true, g2["owner_missing"], "NULL owner flags owner_missing")
	require.Nil(t, g2["owner"])
}

// TestSpxGroupListFASReducesForeignGroups: a facilitator sees its own groups
// full and every other group of the session reduced to gid/name/own/
// member_count/status (contracts pin the reduction; status = the parent
// session's status, user decision 2026-10-11).
func TestSpxGroupListFASReducesForeignGroups(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := assessmentFASHeaders(t, s, mock)

	mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).
			AddRow(10, 2))
	mock.ExpectQuery(q("FROM session_groups g")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id"}).
			AddRow(10, "Kelompok Sendiri", 3).
			AddRow(11, "Kelompok Orang", 4))
	mock.ExpectQuery(q("FROM session_members sm")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "child_id", "name",
			"age", "school", "consent"}).
			AddRow(10, 30, "Ayu", 8, "SD", "SETUJU").
			AddRow(11, 31, "Budi", 9, "SD", "SETUJU"))
	mock.ExpectQuery(q("SELECT id, name FROM users")).
		WithArgs(int64(3), int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(3, "Fasil"))

	res := doReq(t, app, "GET", "/api/sessions/2/groups", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 2)

	own := data[0].(map[string]any)
	require.Equal(t, true, own["own"])
	require.Contains(t, own, "members", "own groups keep their member list")

	foreign := data[1].(map[string]any)
	require.Equal(t, false, foreign["own"])
	require.Equal(t, float64(1), foreign["member_count"])
	require.Equal(t, "OPENED", foreign["status"],
		"the reduced shape carries the parent session's status verbatim")
	require.NotContains(t, foreign, "members", "foreign groups are reduced")
	require.NotContains(t, foreign, "consent", "foreign groups are reduced")
	require.NotContains(t, foreign, "owner", "foreign groups are reduced")
}

// TestSpxGroupListFASForeignSessionIsNotFound: a session outside the
// facilitator's group scope is invisible (errNotVisible → NOT_FOUND), the
// group assembly never runs.
func TestSpxGroupListFASForeignSessionIsNotFound(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := assessmentFASHeaders(t, s, mock)

	mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
		WithArgs(int64(9), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).
			AddRow(10, 2))

	res := doReq(t, app, "GET", "/api/sessions/9/groups", "", h)
	requireErrEnvelope(t, res, CodeNotFound)
}

// ---------------------------------------------------------------------------
// Group / member mutations behind the row-lock status gate (02 §2b)
// ---------------------------------------------------------------------------

// TestSpxEditableLockFailuresAcrossGroupRoutes pins the shared
// lockSessionEditable guard on EVERY mutation route: Begin failure →
// DB_UNAVAILABLE, unknown session → NOT_FOUND, FINISHED → SESSION_LOCKED,
// lock-read failure → DB_UNAVAILABLE — each answered before any write.
func TestSpxEditableLockFailuresAcrossGroupRoutes(t *testing.T) {
	routes := []struct {
		name, method, target string
		body                 any
	}{
		{"group-create", "POST", "/api/sessions/2/groups",
			map[string]any{"name": "Kelompok Baru"}},
		{"group-patch", "PATCH", "/api/sessions/2/groups/10",
			map[string]any{"name": "Baru"}},
		{"group-delete", "DELETE", "/api/sessions/2/groups/10", nil},
		{"members-add", "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}},
		{"member-remove", "DELETE", "/api/sessions/2/groups/10/members/30", nil},
	}
	modes := []struct {
		mode, code string
	}{
		{"begin-fails", CodeDBUnavailable},
		{"missing-session", CodeNotFound},
		{"finished-session", CodeSessionLocked},
		{"lock-read-fails", CodeDBUnavailable},
	}
	for _, tc := range routes {
		for _, m := range modes {
			t.Run(tc.name+"/"+m.mode, func(t *testing.T) {
				s, mock := newTestServer(t)
				app := newTestApp(t, s)
				h := sessionAdminHeaders(t, s, mock)
				switch m.mode {
				case "begin-fails":
					mock.ExpectBegin().WillReturnError(SpxErr)
				case "missing-session":
					mock.ExpectBegin()
					mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
						WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
					mock.ExpectRollback()
				case "finished-session":
					mock.ExpectBegin()
					mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
						WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("FINISHED"))
					mock.ExpectRollback()
				case "lock-read-fails":
					mock.ExpectBegin()
					mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
						WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
					mock.ExpectRollback()
				}
				var res httpRes
				if tc.body != nil {
					res = jsonReq(t, app, tc.method, tc.target, tc.body, h)
				} else {
					res = doReq(t, app, tc.method, tc.target, "", h)
				}
				requireErrEnvelope(t, res, m.code)
			})
		}
	}
}

// ---------------------------------------------------------------------------
// POST /api/sessions/:sid/groups
// ---------------------------------------------------------------------------

// TestSpxGroupCreateWithoutOwnerInsertsNullOwner: ownerless group creation
// (owner optional here — the open gate enforces owners) writes a NULL
// facilitator row and publishes group.changed after commit.
func TestSpxGroupCreateWithoutOwnerInsertsNullOwner(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	SpxBeginLock(mock, 2, 1)
	mock.ExpectExec(q("INSERT INTO session_groups")).
		WithArgs(int64(1), int64(2), "Kelompok Baru", nil, "").
		WillReturnResult(sqlmock.NewResult(101, 1))
	mock.ExpectCommit()

	res := jsonReq(t, app, "POST", "/api/sessions/2/groups",
		map[string]any{"name": "Kelompok Baru"}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(101), data["gid"])
	require.Equal(t, "Kelompok Baru", data["name"])

	f := covNextFrame(t, sub, "group.changed after create")
	require.Equal(t, "group.changed", f.Entity)
	require.Equal(t, int64(101), f.EntityID)
	require.Equal(t, "created", f.Action)
	covNoFrame(t, sub, "one frame per group create")
}

// TestSpxGroupCreateOwnerGateCodes: requestOwner's codes reach the envelope
// through the ONE msgForCode switch — unknown owner → NOT_FOUND, deactivated
// owner → GROUP_WITHOUT_FACILITATOR, wrong role → VALIDATION (default arm).
func TestSpxGroupCreateOwnerGateCodes(t *testing.T) {
	ownerRow := func(role, decision string, active bool) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"role", "decision", "is_active", "school_id"}).
			AddRow(role, decision, active, int64(1))
	}
	t.Run("unknown owner is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups",
			map[string]any{"name": "G", "facilitator_id": 3}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("deactivated owner is group without facilitator", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).
			WillReturnRows(ownerRow("FASILITATOR", "APPROVED", false))
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups",
			map[string]any{"name": "G", "facilitator_id": 3}, h)
		requireErrEnvelope(t, res, CodeGroupNoFacilitator)
	})
	t.Run("non-fasilitator owner is validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).
			WillReturnRows(ownerRow("ADMIN", "APPROVED", true))
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups",
			map[string]any{"name": "G", "facilitator_id": 3}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
}

// TestSpxGroupCreateDBFailures: owner/owner-name/insert/commit failures all
// answer DB_UNAVAILABLE; the commit failure needs no rollback (Tx done).
func TestSpxGroupCreateDBFailures(t *testing.T) {
	t.Run("owner read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups",
			map[string]any{"name": "G", "facilitator_id": 3}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("owner name read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"role", "decision",
				"is_active", "school_id"}).
				AddRow("FASILITATOR", "APPROVED", true, int64(1)))
		mock.ExpectQuery(q("SELECT name FROM users WHERE id = ?")).
			WithArgs(int64(3)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups",
			map[string]any{"name": "G", "facilitator_id": 3}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("group insert fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectExec(q("INSERT INTO session_groups")).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups",
			map[string]any{"name": "Tanpa Owner"}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("commit fails without rollback", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectExec(q("INSERT INTO session_groups")).
			WillReturnResult(sqlmock.NewResult(101, 1))
		mock.ExpectCommit().WillReturnError(SpxErr)
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups",
			map[string]any{"name": "Tanpa Owner"}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// PATCH /api/sessions/:sid/groups/:gid
// ---------------------------------------------------------------------------

// TestSpxGroupPatchFailureBranches: unknown group → NOT_FOUND; group/owner/
// owner-name/UPDATE/COMMIT failures → DB_UNAVAILABLE; any owner problem on
// PATCH is the flat VALIDATION (02 §2b).
func TestSpxGroupPatchFailureBranches(t *testing.T) {
	curName := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("SELECT name FROM session_groups WHERE id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Lama"))
	}
	t.Run("unknown group is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectQuery(q("SELECT name FROM session_groups WHERE id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		res := jsonReq(t, app, "PATCH", "/api/sessions/2/groups/10",
			map[string]any{"name": "Baru"}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("group read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectQuery(q("SELECT name FROM session_groups WHERE id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := jsonReq(t, app, "PATCH", "/api/sessions/2/groups/10",
			map[string]any{"name": "Baru"}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("owner problem is flat validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		curName(mock)
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		res := jsonReq(t, app, "PATCH", "/api/sessions/2/groups/10",
			map[string]any{"facilitator_id": 3}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("owner name read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		curName(mock)
		mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
			WithArgs(int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"role", "decision",
				"is_active", "school_id"}).
				AddRow("FASILITATOR", "APPROVED", true, int64(1)))
		mock.ExpectQuery(q("SELECT name FROM users WHERE id = ?")).
			WithArgs(int64(3)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := jsonReq(t, app, "PATCH", "/api/sessions/2/groups/10",
			map[string]any{"facilitator_id": 3}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("update fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		curName(mock)
		mock.ExpectExec(q("UPDATE session_groups SET name = ?")).
			WithArgs("Baru", int64(10), int64(2)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := jsonReq(t, app, "PATCH", "/api/sessions/2/groups/10",
			map[string]any{"name": "Baru"}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("commit fails without rollback", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		curName(mock)
		mock.ExpectExec(q("UPDATE session_groups SET name = ?")).
			WithArgs("Baru", int64(10), int64(2)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(SpxErr)
		res := jsonReq(t, app, "PATCH", "/api/sessions/2/groups/10",
			map[string]any{"name": "Baru"}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxGroupPatchClearsOwnerOnZero: facilitator_id=0 clears the owner
// (facilitator_id = NULL, snap = ”) while keeping the name echo.
func TestSpxGroupPatchClearsOwnerOnZero(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	SpxBeginLock(mock, 2, 1)
	mock.ExpectQuery(q("SELECT name FROM session_groups WHERE id = ? AND session_id = ?")).
		WithArgs(int64(10), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Lama"))
	mock.ExpectExec(q("UPDATE session_groups SET facilitator_id = NULL")).
		WithArgs(int64(10), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := jsonReq(t, app, "PATCH", "/api/sessions/2/groups/10",
		map[string]any{"facilitator_id": 0}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(10), data["gid"])
	require.Equal(t, "Lama", data["name"], "name stays untouched without a name field")

	f := covNextFrame(t, sub, "group.changed after clear")
	require.Equal(t, "group.changed", f.Entity)
	require.Equal(t, "updated", f.Action)
	f = covNextFrame(t, sub, "session.changed after facilitator clear")
	require.Equal(t, "session.changed", f.Entity)
	covNoFrame(t, sub, "one frame per group patch")
}

// ---------------------------------------------------------------------------
// DELETE /api/sessions/:sid/groups/:gid (option-B cascade, decision 13)
// ---------------------------------------------------------------------------

// TestSpxGroupDeleteFailureBranches: unknown group → NOT_FOUND; probe, child
// list, wipe, per-table DELETE and COMMIT failures → DB_UNAVAILABLE (claim:
// no group.changed frame is ever published).
func TestSpxGroupDeleteFailureBranches(t *testing.T) {
	probeOK := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("SELECT 1 FROM session_groups WHERE id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	}
	t.Run("unknown group is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectQuery(q("SELECT 1 FROM session_groups WHERE id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("group probe fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectQuery(q("SELECT 1 FROM session_groups WHERE id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("member child list read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		probeOK(mock)
		mock.ExpectQuery(q("SELECT child_id FROM session_members WHERE group_id = ?")).
			WithArgs(int64(10)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("member child row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		probeOK(mock)
		mock.ExpectQuery(q("SELECT child_id FROM session_members WHERE group_id = ?")).
			WithArgs(int64(10)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id"}).AddRow("not-an-id"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("child scope wipe fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		probeOK(mock)
		mock.ExpectQuery(q("SELECT child_id FROM session_members WHERE group_id = ?")).
			WithArgs(int64(10)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id"}).AddRow(30))
		mock.ExpectQuery(q("SELECT path FROM photos WHERE session_id = ?")).
			WithArgs(int64(2), int64(30), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("member delete fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		probeOK(mock)
		mock.ExpectQuery(q("SELECT child_id FROM session_members WHERE group_id = ?")).
			WithArgs(int64(10)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id"}))
		mock.ExpectExec(q("DELETE FROM session_members WHERE group_id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("group delete fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		probeOK(mock)
		mock.ExpectQuery(q("SELECT child_id FROM session_members WHERE group_id = ?")).
			WithArgs(int64(10)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id"}))
		mock.ExpectExec(q("DELETE FROM session_members WHERE group_id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q("DELETE FROM session_groups WHERE id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("commit fails without rollback", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		probeOK(mock)
		mock.ExpectQuery(q("SELECT child_id FROM session_members WHERE group_id = ?")).
			WithArgs(int64(10)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id"}))
		mock.ExpectExec(q("DELETE FROM session_members WHERE group_id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q("DELETE FROM session_groups WHERE id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(SpxErr)
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxGroupDeleteMemberlessGroupStillDeletes: an empty group takes the
// no-children fast path of the wipe (zero child-scoped queries) and still
// removes members + group, publishing exactly one group.changed.
func TestSpxGroupDeleteMemberlessGroupStillDeletes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	SpxBeginLock(mock, 2, 1)
	mock.ExpectQuery(q("SELECT 1 FROM session_groups WHERE id = ? AND session_id = ?")).
		WithArgs(int64(10), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT child_id FROM session_members WHERE group_id = ?")).
		WithArgs(int64(10)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id"}))
	// No wipe queries: the child list is empty (any stray query fails here).
	mock.ExpectExec(q("DELETE FROM session_members WHERE group_id = ? AND session_id = ?")).
		WithArgs(int64(10), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("DELETE FROM session_groups WHERE id = ? AND session_id = ?")).
		WithArgs(int64(10), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])

	f := covNextFrame(t, sub, "group.changed after empty-group delete")
	require.Equal(t, "group.changed", f.Entity)
	require.Equal(t, "deleted", f.Action)
	covNoFrame(t, sub, "no link kills → exactly one frame")
}

// ---------------------------------------------------------------------------
// POST/DELETE members (05 §7 member Tx checks + decision-13 wipe)
// ---------------------------------------------------------------------------

// SpxRemoveProbe queues Begin + Draft lock + the member probe of
// sid=2/gid=10/cid=30.
func SpxRemoveProbe(mock sqlmock.Sqlmock) {
	SpxBeginLock(mock, 2, 1)
	mock.ExpectQuery(q("SELECT 1 FROM session_members")).
		WithArgs(int64(2), int64(10), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
}

// TestSpxMembersAddGateBranches: group probe, locked child lookup, existing
// member probe and the insert gates each answer with their own envelope and
// undo the Tx.
func TestSpxMembersAddGateBranches(t *testing.T) {
	groupProbe := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("SELECT 1 FROM session_groups WHERE id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	}
	t.Run("unknown group is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectQuery(q("SELECT 1 FROM session_groups WHERE id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("group probe fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectQuery(q("SELECT 1 FROM session_groups WHERE id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("children read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		groupProbe(mock)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(31), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("children iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		groupProbe(mock)
		rows := sqlmock.NewRows([]string{"id", "name", "is_active"}).
			AddRow(31, "Budi", true).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(31), int64(1)).WillReturnRows(rows)
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("existing member probe fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		groupProbe(mock)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(31), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}).
				AddRow(31, "Budi", true))
		mock.ExpectQuery(q("SELECT child_id FROM session_members")).
			WithArgs(int64(31), int64(2)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("already a member is child duplicate", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		groupProbe(mock)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(31), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}).
				AddRow(31, "Budi", true))
		mock.ExpectQuery(q("SELECT child_id FROM session_members")).
			WithArgs(int64(31), int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id"}).AddRow(31))
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}, h)
		requireErrEnvelope(t, res, CodeChildDuplicate)
	})
	t.Run("unknown child is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		groupProbe(mock)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(31), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}))
		mock.ExpectQuery(q("SELECT child_id FROM session_members")).
			WithArgs(int64(31), int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id"}))
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("inactive child is child inactive", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		groupProbe(mock)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(31), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}).
				AddRow(31, "Budi", false))
		mock.ExpectQuery(q("SELECT child_id FROM session_members")).
			WithArgs(int64(31), int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id"}))
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}, h)
		requireErrEnvelope(t, res, CodeChildInactive)
	})
	t.Run("member insert duplicate is child duplicate", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		groupProbe(mock)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(31), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}).
				AddRow(31, "Budi", true))
		mock.ExpectQuery(q("SELECT child_id FROM session_members")).
			WithArgs(int64(31), int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id"}))
		mock.ExpectExec(q("INSERT INTO session_members")).
			WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}, h)
		requireErrEnvelope(t, res, CodeChildDuplicate)
	})
	t.Run("member insert failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		groupProbe(mock)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(31), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}).
				AddRow(31, "Budi", true))
		mock.ExpectQuery(q("SELECT child_id FROM session_members")).
			WithArgs(int64(31), int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id"}))
		mock.ExpectExec(q("INSERT INTO session_members")).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("commit fails without rollback", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		groupProbe(mock)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(31), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}).
				AddRow(31, "Budi", true))
		mock.ExpectQuery(q("SELECT child_id FROM session_members")).
			WithArgs(int64(31), int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id"}))
		mock.ExpectExec(q("INSERT INTO session_members")).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit().WillReturnError(SpxErr)
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxMembersAddCopyHintFailureStillPublishesFrame: the post-commit
// copy_available hint is best-effort — a failing lookup logs and the
// member.changed frame ships WITHOUT the hint, the add still answers 200.
func TestSpxMembersAddCopyHintFailureStillPublishesFrame(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	SpxBeginLock(mock, 2, 1)
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
	mock.ExpectQuery(q("FROM sessions other")).
		WithArgs(int64(1), int64(2), int64(2), int64(31)).WillReturnError(SpxErr)

	res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
		map[string]any{"child_ids": []int64{31}}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, []any{float64(31)}, data["added"])

	f := covNextFrame(t, sub, "member.changed ships without the failed hint")
	require.Equal(t, "member.changed", f.Entity)
	require.Equal(t, int64(31), f.EntityID)
	require.Nil(t, f.CopyAvailable, "a failed hint lookup never fails the add")
	covNoFrame(t, sub, "one frame per added member")
}

// TestSpxMemberRemoveFailureBranches: probe/wipe/delete/commit failures of
// the single-member cascade, each with the exact envelope and a rollback.
func TestSpxMemberRemoveFailureBranches(t *testing.T) {
	t.Run("unknown member is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectQuery(q("SELECT 1 FROM session_members")).
			WithArgs(int64(2), int64(10), int64(30)).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("member probe fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxBeginLock(mock, 2, 1)
		mock.ExpectQuery(q("SELECT 1 FROM session_members")).
			WithArgs(int64(2), int64(10), int64(30)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("photo path read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxRemoveProbe(mock)
		mock.ExpectQuery(q("SELECT path FROM photos WHERE session_id = ?")).
			WithArgs(int64(2), int64(30), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("photo path row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxRemoveProbe(mock)
		mock.ExpectQuery(q("SELECT path FROM photos WHERE session_id = ?")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow(nil))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("photo path iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxRemoveProbe(mock)
		rows := sqlmock.NewRows([]string{"path"}).
			AddRow("1/2/5/30/a.jpg").
			RowError(0, SpxErr)
		mock.ExpectQuery(q("SELECT path FROM photos WHERE session_id = ?")).
			WithArgs(int64(2), int64(30), int64(1)).WillReturnRows(rows)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("photo row delete fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxRemoveProbe(mock)
		mock.ExpectQuery(q("SELECT path FROM photos WHERE session_id = ?")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("1/2/5/30/a.jpg"))
		mock.ExpectExec(q("DELETE FROM photos WHERE session_id = ?")).
			WithArgs(int64(2), int64(30), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("consent token read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxRemoveProbe(mock)
		SpxWipePhotos(mock, 2, 30, 1)
		mock.ExpectQuery(q("SELECT id, child_id, token FROM consent_links")).
			WithArgs(int64(2), int64(30), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("consent token row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxRemoveProbe(mock)
		SpxWipePhotos(mock, 2, 30, 1)
		mock.ExpectQuery(q("SELECT id, child_id, token FROM consent_links")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "child_id", "token"}).
				AddRow(7, int64(30), nil))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("consent token iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxRemoveProbe(mock)
		SpxWipePhotos(mock, 2, 30, 1)
		rows := sqlmock.NewRows([]string{"id", "child_id", "token"}).
			AddRow(7, int64(30), "ctok").
			RowError(0, SpxErr)
		mock.ExpectQuery(q("SELECT id, child_id, token FROM consent_links")).
			WithArgs(int64(2), int64(30), int64(1)).WillReturnRows(rows)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("public link read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxRemoveProbe(mock)
		SpxWipePhotos(mock, 2, 30, 1)
		mock.ExpectQuery(q("SELECT id, child_id, token FROM consent_links")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "child_id", "token"}))
		mock.ExpectQuery(q("SELECT id, child_id, token, kind FROM public_links WHERE revoked_at IS NULL")).
			WithArgs(int64(2), int64(30), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("public link row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxRemoveProbe(mock)
		SpxWipePhotos(mock, 2, 30, 1)
		SpxWipeTokens(mock, 2, 30, 1,
			sqlmock.NewRows([]string{"id", "child_id", "token"}),
			sqlmock.NewRows([]string{"id", "child_id", "token", "kind"}).
				AddRow(9, int64(30), "ptok", nil))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("public link iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxRemoveProbe(mock)
		SpxWipePhotos(mock, 2, 30, 1)
		rows := sqlmock.NewRows([]string{"id", "child_id", "token", "kind"}).
			AddRow(9, int64(30), "ptok", "REPORT").
			RowError(0, SpxErr)
		SpxWipeTokens(mock, 2, 30, 1,
			sqlmock.NewRows([]string{"id", "child_id", "token"}), rows)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("wipe table delete fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxRemoveProbe(mock)
		SpxWipePhotos(mock, 2, 30, 1)
		SpxWipeTokens(mock, 2, 30, 1,
			sqlmock.NewRows([]string{"id", "child_id", "token"}),
			sqlmock.NewRows([]string{"id", "child_id", "token", "kind"}))
		mock.ExpectExec(q("DELETE FROM assessments WHERE session_id = ?")).
			WithArgs(int64(2), int64(30), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("member delete fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxRemoveProbe(mock)
		SpxWipePhotos(mock, 2, 30, 1)
		SpxWipeTokens(mock, 2, 30, 1,
			sqlmock.NewRows([]string{"id", "child_id", "token"}),
			sqlmock.NewRows([]string{"id", "child_id", "token", "kind"}))
		SpxWipeDeletes(mock, 2, 30, 1)
		mock.ExpectExec(q("WHERE session_id = ? AND group_id = ? AND child_id = ?")).
			WithArgs(int64(2), int64(10), int64(30)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("member vanished mid-wipe is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxRemoveProbe(mock)
		SpxWipePhotos(mock, 2, 30, 1)
		SpxWipeTokens(mock, 2, 30, 1,
			sqlmock.NewRows([]string{"id", "child_id", "token"}),
			sqlmock.NewRows([]string{"id", "child_id", "token", "kind"}))
		SpxWipeDeletes(mock, 2, 30, 1)
		mock.ExpectExec(q("WHERE session_id = ? AND group_id = ? AND child_id = ?")).
			WithArgs(int64(2), int64(10), int64(30)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("commit fails without rollback", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxRemoveProbe(mock)
		SpxWipePhotos(mock, 2, 30, 1)
		SpxWipeTokens(mock, 2, 30, 1,
			sqlmock.NewRows([]string{"id", "child_id", "token"}),
			sqlmock.NewRows([]string{"id", "child_id", "token", "kind"}))
		SpxWipeDeletes(mock, 2, 30, 1)
		mock.ExpectExec(q("WHERE session_id = ? AND group_id = ? AND child_id = ?")).
			WithArgs(int64(2), int64(10), int64(30)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(SpxErr)
		res := doReq(t, app, "DELETE", "/api/sessions/2/groups/10/members/30", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// PATCH /api/sessions/:sid (decision 5 backdate)
// ---------------------------------------------------------------------------

// TestSpxSessionPatchGateBranches: unknown session → NOT_FOUND, read failure
// → DB_UNAVAILABLE, blank name / malformed date → VALIDATION before any
// UPDATE, an OPENED session rejects snapshot edits with SESSION_LOCKED.
func TestSpxSessionPatchGateBranches(t *testing.T) {
	t.Run("unknown session is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"name": "Baru"}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("session read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"name": "Baru"}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("blank name is validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"name": "   "}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("malformed date is validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"date": "05-01-2026"}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("opened session rejects snapshot edits", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "OPENED", int64(2))
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"name": "Baru"}, h)
		requireErrEnvelope(t, res, CodeSessionLocked)
	})
}

// TestSpxSessionPatchProgramIDLocked: program_id rides the immutable session
// snapshot (08 acceptance) — a PATCH body CONTAINING it answers
// SESSION_LOCKED for every status, before any read or write (only the auth
// guard is queued — a stray SQL fails the ordered mock); the same edit
// without program_id keeps patching normally.
func TestSpxSessionPatchProgramIDLocked(t *testing.T) {
	t.Run("program_id alone is locked before any sql", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock) // guard only — no session read
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"program_id": 9}, h)
		requireErrEnvelope(t, res, CodeSessionLocked)
	})
	t.Run("program_id zero is locked too", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"program_id": 0}, h)
		requireErrEnvelope(t, res, CodeSessionLocked)
	})
	t.Run("program_id riding with other fields blocks them all", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock) // the rename must never reach SQL
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"name": "Baru", "program_id": 9}, h)
		requireErrEnvelope(t, res, CodeSessionLocked)
	})
	t.Run("omitted program_id still patches normally", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		mock.ExpectExec(q("UPDATE sessions SET name = ?")).
			WithArgs("Baru", int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"name": "Baru"}, h)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, "Baru", data["session"].(map[string]any)["name"])
		require.Equal(t, "updated", takeFrame(t, spy).Action)
	})
}

// TestSpxSessionPatchNoopEchoes: an empty patch never writes — Finished stays
// SESSION_LOCKED, any other status echoes the current row. A non-empty patch
// whose guarded UPDATE affects 0 rows hits the E10/E11 status re-read: still
// DRAFT with a value-identical request is a legal no-op echo (no rival), a
// real requested change already sitting in the row is STATE_CONFLICT.
func TestSpxSessionPatchNoopEchoes(t *testing.T) {
	t.Run("finished stays locked", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "FINISHED", int64(2))
		res := jsonReq(t, app, "PATCH", "/api/sessions/2", map[string]any{}, h)
		requireErrEnvelope(t, res, CodeSessionLocked)
	})
	t.Run("draft echoes current row", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		res := jsonReq(t, app, "PATCH", "/api/sessions/2", map[string]any{}, h)
		data := requireDataEnvelope(t, res).(map[string]any)
		sess := data["session"].(map[string]any)
		require.Equal(t, "Sesi A", sess["name"], "the CURRENT row is echoed")
		require.Equal(t, "DRAFT", sess["status"])
	})
	t.Run("affected-0 draft re-read echoes the merged row", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		// Value-identical rename: the guarded UPDATE matches the DRAFT row
		// but changes nothing (0 affected) — the op is legal and there is no
		// rival, so the merged next row echoes back with 200.
		mock.ExpectExec(q("UPDATE sessions SET name = ?")).
			WithArgs("Sesi A", int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"name": "Sesi A"}, h)
		data := requireDataEnvelope(t, res).(map[string]any)
		sess := data["session"].(map[string]any)
		require.Equal(t, "Sesi A", sess["name"], "the no-op echoes <next>")
		require.Equal(t, "DRAFT", sess["status"])
		covNoFrame(t, spy, "a no-op patch publishes no frame")
	})
	t.Run("affected-0 draft with a requested change is state conflict", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		// A REAL rename (row read "Sesi A") reports 0 affected while the
		// status stays DRAFT: another update already applied the requested
		// values while the status was unchanged → STATE_CONFLICT (E11),
		// never a silent 200 echo.
		mock.ExpectExec(q("UPDATE sessions SET name = ?")).
			WithArgs("Baru", int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"name": "Baru"}, h)
		requireErrEnvelope(t, res, CodeStateConflict)
		covNoFrame(t, spy, "a lost guarded patch publishes no frame")
	})
}

// TestSpxSessionPatchMergesDescriptionLocationAndClocks: the merged-next-row
// semantics — description/location/date land in the guarded UPDATE, blank
// description/start clear the column, untouched non-null clocks echo through,
// and a one-sided unparsable clock is accepted by the shared validator.
func TestSpxSessionPatchMergesDescriptionLocationAndClocks(t *testing.T) {
	t.Run("description replaces the text", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		mock.ExpectExec(q("UPDATE sessions SET description = ?")).
			WithArgs("Deskripsi baru", int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"description": "Deskripsi baru"}, h)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, "Deskripsi baru", data["session"].(map[string]any)["description"])
	})
	t.Run("blank description clears the column", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		// The WIRE echoes null (trimmed empty) while toNull only maps "" —
		// the raw request string is what reaches the UPDATE.
		mock.ExpectExec(q("UPDATE sessions SET description = ?")).
			WithArgs("   ", int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"description": "   "}, h)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Nil(t, data["session"].(map[string]any)["description"])
	})
	t.Run("location and date ride one UPDATE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		mock.ExpectExec(q("UPDATE sessions SET location = ?, date = ?")).
			WithArgs("Lab 2", "2026-02-01", int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"location": "Lab 2", "date": "2026-02-01"}, h)
		data := requireDataEnvelope(t, res).(map[string]any)
		sess := data["session"].(map[string]any)
		require.Equal(t, "Lab 2", sess["location"])
		require.Equal(t, "2026-02-01", sess["date"])
	})
	t.Run("unrelated edit keeps non-null clocks", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "program_id",
				"program_name_snap", "name", "description", "date", "start_time",
				"end_time", "location", "status", "created_by", "created_at"}).
				AddRow(2, 9, "Program A", "Sesi A", nil, fixedTS, "09:00:00",
					"11:00:00", "Lab", "DRAFT", int64(2), fixedTS))
		mock.ExpectExec(q("UPDATE sessions SET location = ?")).
			WithArgs("Lab 3", int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"location": "Lab 3"}, h)
		data := requireDataEnvelope(t, res).(map[string]any)
		sess := data["session"].(map[string]any)
		require.Equal(t, "09:00:00", sess["start_time"],
			"existing clocks survive an unrelated edit verbatim")
		require.Equal(t, "11:00:00", sess["end_time"])
	})
	t.Run("blank start clears the clock", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "program_id",
				"program_name_snap", "name", "description", "date", "start_time",
				"end_time", "location", "status", "created_by", "created_at"}).
				AddRow(2, 9, "Program A", "Sesi A", nil, fixedTS, "09:00:00",
					nil, "Lab", "DRAFT", int64(2), fixedTS))
		mock.ExpectExec(q("UPDATE sessions SET start_time = ?")).
			WithArgs(nil, int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"start_time": ""}, h)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Nil(t, data["session"].(map[string]any)["start_time"])
	})
	t.Run("one-sided unparsable clock passes through", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		// End time blank on both sides → the shared range gate accepts the
		// value verbatim and the response echoes it unchanged.
		mock.ExpectExec(q("UPDATE sessions SET start_time = ?")).
			WithArgs("25:99", int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"start_time": "25:99"}, h)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, "25:99", data["session"].(map[string]any)["start_time"])
	})
}

// TestSpxSessionPatchWriteFailures: the guarded UPDATE either fails outright
// (DB_UNAVAILABLE) or loses the race — a vanished row is NOT_FOUND, a dead
// status re-read is DB_UNAVAILABLE, never a silent success.
func TestSpxSessionPatchWriteFailures(t *testing.T) {
	t.Run("update fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		mock.ExpectExec(q("UPDATE sessions SET name = ?")).
			WithArgs("Baru", int64(2), int64(1)).WillReturnError(SpxErr)
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"name": "Baru"}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("row vanished before the update is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		mock.ExpectExec(q("UPDATE sessions SET name = ?")).
			WithArgs("Baru", int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"name": "Baru"}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("re-read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
		mock.ExpectExec(q("UPDATE sessions SET name = ?")).
			WithArgs("Baru", int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		// A re-read that errors (not "no rows") is DB trouble, not a missing
		// row — same mapping as the transition zero-rows re-read.
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
		res := jsonReq(t, app, "PATCH", "/api/sessions/2",
			map[string]any{"name": "Baru"}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// DELETE /api/sessions/:sid (decision 8 option A)
// ---------------------------------------------------------------------------

// TestSpxSessionDeleteFailureBranches: status/child-list/wipe/DELETE/COMMIT
// failures of the physical wipe, each with rollback and no publish.
func TestSpxSessionDeleteFailureBranches(t *testing.T) {
	begin := func(mock sqlmock.Sqlmock, status string) {
		mock.ExpectBegin()
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows(status))
	}
	childList := func(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
		exp := mock.ExpectQuery(q("SELECT child_id FROM session_members WHERE session_id = ?")).
			WithArgs(int64(2))
		if rows != nil {
			exp.WillReturnRows(rows)
		} else {
			exp.WillReturnError(SpxErr)
		}
	}
	sessionDeletes := func(mock sqlmock.Sqlmock) {
		for _, table := range []string{"session_members", "session_groups",
			"session_activities", "session_topics"} {
			mock.ExpectExec(q("DELETE FROM " + table + " WHERE session_id = ?")).
				WithArgs(int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
		}
	}
	t.Run("unknown session is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
			WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("status claim read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
			WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("child list read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		begin(mock, "DRAFT")
		childList(mock, nil)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("child row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		begin(mock, "DRAFT")
		childList(mock, sqlmock.NewRows([]string{"child_id"}).AddRow("not-an-id"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("child scope wipe fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		begin(mock, "DRAFT")
		childList(mock, sqlmock.NewRows([]string{"child_id"}).AddRow(30))
		mock.ExpectQuery(q("SELECT path FROM photos WHERE session_id = ?")).
			WithArgs(int64(2), int64(30), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("snapshot table delete fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		begin(mock, "DRAFT")
		childList(mock, sqlmock.NewRows([]string{"child_id"}))
		mock.ExpectExec(q("DELETE FROM session_members WHERE session_id = ?")).
			WithArgs(int64(2)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("session row delete fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		begin(mock, "DRAFT")
		childList(mock, sqlmock.NewRows([]string{"child_id"}))
		sessionDeletes(mock)
		mock.ExpectExec(q("DELETE FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("guarded delete matching nothing is state conflict", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})
		begin(mock, "DRAFT")
		childList(mock, sqlmock.NewRows([]string{"child_id"}))
		sessionDeletes(mock)
		mock.ExpectExec(q("DELETE FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		// 0 affected → the mandated status re-read (06 §1 E10/E11): the row
		// kept the claimed DRAFT while the DELETE matched nothing — a rival
		// already applied the change → STATE_CONFLICT, symmetric with the
		// PATCH zero-rows branch (which reserves SESSION_LOCKED for a moved
		// status and NOT_FOUND for a vanished row).
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeStateConflict)
		covNoFrame(t, sub, "a rolled-back delete publishes nothing")
	})
	t.Run("affected-0 vanished session is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		begin(mock, "DRAFT")
		childList(mock, sqlmock.NewRows([]string{"child_id"}))
		sessionDeletes(mock)
		mock.ExpectExec(q("DELETE FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		// The re-read finds no row: it vanished between the claim and the
		// guarded DELETE → NOT_FOUND, never a bare STATE_CONFLICT.
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("affected-0 status moved is session locked", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		begin(mock, "DRAFT")
		childList(mock, sqlmock.NewRows([]string{"child_id"}))
		sessionDeletes(mock)
		mock.ExpectExec(q("DELETE FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		// The re-read shows a status that forbids the wipe (E10 wrong-state:
		// a rival opened the session) → SESSION_LOCKED, matching how the
		// PATCH zero-rows branch classifies OPENED/FINISHED.
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeSessionLocked)
	})
	t.Run("affected-0 re-read fails is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		begin(mock, "DRAFT")
		childList(mock, sqlmock.NewRows([]string{"child_id"}))
		sessionDeletes(mock)
		mock.ExpectExec(q("DELETE FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("commit fails without rollback", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		begin(mock, "DRAFT")
		childList(mock, sqlmock.NewRows([]string{"child_id"}))
		sessionDeletes(mock)
		mock.ExpectExec(q("DELETE FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(SpxErr)
		res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxSessionDeleteSurvivesUnlinkFailure: photo files unlink AFTER commit;
// an unlink that fails (here the path is a non-empty directory) is logged and
// never fails or delays the wipe — session.deleted still publishes.
func TestSpxSessionDeleteSurvivesUnlinkFailure(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	full := filepath.Join(s.upload, "1", "2", "5", "30", "a.jpg")
	require.NoError(t, os.MkdirAll(full, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(full, "keep.txt"),
		[]byte("keep"), 0o644))

	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	mock.ExpectQuery(q("SELECT child_id FROM session_members WHERE session_id = ?")).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id"}).AddRow(30))
	SpxWipePhotos(mock, 2, 30, 1, "1/2/5/30/a.jpg")
	SpxWipeTokens(mock, 2, 30, 1,
		sqlmock.NewRows([]string{"id", "child_id", "token"}),
		sqlmock.NewRows([]string{"id", "child_id", "token", "kind"}))
	SpxWipeDeletes(mock, 2, 30, 1)
	for _, table := range []string{"session_members", "session_groups",
		"session_activities", "session_topics"} {
		mock.ExpectExec(q("DELETE FROM " + table + " WHERE session_id = ?")).
			WithArgs(int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(q("DELETE FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])

	require.Equal(t, "session.deleted",
		covNextFrame(t, sub, "wipe publishes after commit").Entity)
	covNoFrame(t, sub, "no link kills → one frame")
	require.DirExists(t, full,
		"the failing unlink must leave the obstacle in place (sweep retries later)")
}

// ---------------------------------------------------------------------------
// Guarded lifecycle transitions (06 E10/E11) + open gates
// ---------------------------------------------------------------------------

// SpxOpenGatesPass queues one non-empty group with an active owner.
func SpxOpenGatesPass(mock sqlmock.Sqlmock, sid int64) {
	mock.ExpectQuery(q("SELECT COUNT(*) FROM session_groups WHERE session_id = ?")).
		WithArgs(sid).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery(q("FROM session_groups g WHERE g.session_id = ?")).
		WithArgs(sid).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id",
			"members"}).AddRow(10, "Kelompok Merah", 3, 2))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(1))
}

// TestSpxTransitionFailureBranches: steady-read/Begin/locked-claim/UPDATE/
// re-read failures of DRAFT → OPENED, each with rollback and no publish.
func TestSpxTransitionFailureBranches(t *testing.T) {
	steady := func(mock sqlmock.Sqlmock, status string) {
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows(status))
	}
	locked := func(mock sqlmock.Sqlmock, status string) {
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows(status))
	}
	t.Run("status read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("begin fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		steady(mock, "DRAFT")
		mock.ExpectBegin().WillReturnError(SpxErr)
		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("locked claim missing is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		steady(mock, "DRAFT")
		mock.ExpectBegin()
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
			WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("locked claim read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		steady(mock, "DRAFT")
		mock.ExpectBegin()
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
			WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("guarded update fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		steady(mock, "DRAFT")
		mock.ExpectBegin()
		locked(mock, "DRAFT")
		SpxOpenGatesPass(mock, 2)
		mock.ExpectExec(q("UPDATE sessions SET status = ? WHERE id = ?")).
			WithArgs("OPENED", int64(2), int64(1), "DRAFT").WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("zero-rows re-read missing is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		steady(mock, "DRAFT")
		mock.ExpectBegin()
		locked(mock, "DRAFT")
		SpxOpenGatesPass(mock, 2)
		mock.ExpectExec(q("UPDATE sessions SET status = ? WHERE id = ?")).
			WithArgs("OPENED", int64(2), int64(1), "DRAFT").
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("zero-rows re-read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		steady(mock, "DRAFT")
		mock.ExpectBegin()
		locked(mock, "DRAFT")
		SpxOpenGatesPass(mock, 2)
		mock.ExpectExec(q("UPDATE sessions SET status = ? WHERE id = ?")).
			WithArgs("OPENED", int64(2), int64(1), "DRAFT").
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("zero-rows unknown re-read status is session locked", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})
		steady(mock, "DRAFT")
		mock.ExpectBegin()
		locked(mock, "DRAFT")
		SpxOpenGatesPass(mock, 2)
		mock.ExpectExec(q("UPDATE sessions SET status = ? WHERE id = ?")).
			WithArgs("OPENED", int64(2), int64(1), "DRAFT").
			WillReturnResult(sqlmock.NewResult(0, 0))
		// A status outside DRAFT/OPENED/FINISHED ranks below the target →
		// the loser gets SESSION_LOCKED (E10), never STATE_CONFLICT.
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("ARCHIVED"))
		mock.ExpectRollback()
		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		requireErrEnvelope(t, res, CodeSessionLocked)
		covNoFrame(t, sub, "a lost transition publishes nothing")
	})
}

// TestSpxTransitionCommitFailureIsDBUnavailable: the guarded reopen commits…
// and the COMMIT itself failing is a 200 DB_UNAVAILABLE.
func TestSpxTransitionCommitFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(4), int64(1)).WillReturnRows(statusRows("FINISHED"))
	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(int64(4), int64(1)).WillReturnRows(statusRows("FINISHED"))
	mock.ExpectExec(q("UPDATE sessions SET status = ? WHERE id = ?")).
		WithArgs("OPENED", int64(4), int64(1), "FINISHED").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(SpxErr)

	res := doReq(t, app, "POST", "/api/sessions/4/reopen", "", h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestSpxOpenGateFailureBranches: every open precheck failure — count/rows/
// scan/iteration/ownerOK read errors → DB_UNAVAILABLE; a NULL owner is the
// owner_missing offender (no ownerOK lookup for it).
func TestSpxOpenGateFailureBranches(t *testing.T) {
	steady := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
		mock.ExpectBegin()
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("DRAFT"))
	}
	t.Run("group count read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		steady(mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM session_groups WHERE session_id = ?")).
			WithArgs(int64(2)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("gate rows read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		steady(mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM session_groups WHERE session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		mock.ExpectQuery(q("FROM session_groups g WHERE g.session_id = ?")).
			WithArgs(int64(2)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("gate row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		steady(mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM session_groups WHERE session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		mock.ExpectQuery(q("FROM session_groups g WHERE g.session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name",
				"facilitator_id", "members"}).
				AddRow("not-an-id", "Kelompok", 3, 2))
		mock.ExpectRollback()
		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("gate row iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		steady(mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM session_groups WHERE session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		rows := sqlmock.NewRows([]string{"id", "name", "facilitator_id", "members"}).
			AddRow(10, "Kelompok", 3, 2).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("FROM session_groups g WHERE g.session_id = ?")).
			WithArgs(int64(2)).WillReturnRows(rows)
		mock.ExpectRollback()
		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("owner check fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		steady(mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM session_groups WHERE session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		mock.ExpectQuery(q("FROM session_groups g WHERE g.session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name",
				"facilitator_id", "members"}).
				AddRow(10, "Kelompok", 3, 2))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users")).
			WithArgs(int64(3), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("null owner is owner missing offender", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})
		steady(mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM session_groups WHERE session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		// NULL facilitator: NO ownerOK lookup may happen for it.
		mock.ExpectQuery(q("FROM session_groups g WHERE g.session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name",
				"facilitator_id", "members"}).
				AddRow(10, "Tanpa Fasilitator", nil, 2))
		mock.ExpectRollback()
		res := doReq(t, app, "POST", "/api/sessions/2/open", "", h)
		env := requireErrEnvelope(t, res, CodeGroupNoFacilitator)
		requireGateGroups(t, env,
			gateGroup{GID: 10, Name: "Tanpa Fasilitator", Reason: "owner_missing"})
		covNoFrame(t, sub, "gate failure publishes nothing")
	})
}

// ---------------------------------------------------------------------------
// Copy-data antar-sesi (F-15 / FL-16 / decision 21)
// ---------------------------------------------------------------------------

// SpxCopyPrologue queues target+source sessions, both membership probes, the
// active child, the consent read and the idempotency claim (nil consent =
// never sent → sql.ErrNoRows).
func SpxCopyPrologue(mock sqlmock.Sqlmock, key, tgtStatus string, consent *string) {
	mock.ExpectQuery(q("SELECT program_id, status FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"program_id", "status"}).
			AddRow(9, tgtStatus))
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
			AddRow("Ayu", true))
	consentQ := mock.ExpectQuery(q("SELECT status FROM consent_links WHERE session_id = ? AND child_id = ?")).
		WithArgs(int64(2), int64(30))
	if consent != nil {
		consentQ.WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(*consent))
	} else {
		consentQ.WillReturnError(sql.ErrNoRows)
	}
	SpxIdemClaim(mock, spxCopyRoute, key)
}

// SpxCopyMapRows queues the target snapshot index reads (master-id matching).
func SpxCopyMapRows(mock sqlmock.Sqlmock, dstSID int64, topics, activities *sqlmock.Rows) {
	mock.ExpectQuery(q("SELECT id, topic_id FROM session_topics WHERE session_id = ?")).
		WithArgs(dstSID).WillReturnRows(topics)
	mock.ExpectQuery(q("SELECT id, activity_id FROM session_activities WHERE session_id = ?")).
		WithArgs(dstSID).WillReturnRows(activities)
}

// SpxCopyDefaultMaps = master topic 9 → stid 50, master activity 101 → said 61.
func SpxCopyDefaultMaps(mock sqlmock.Sqlmock, dstSID int64) {
	SpxCopyMapRows(mock, dstSID,
		sqlmock.NewRows([]string{"id", "topic_id"}).AddRow(50, 9),
		sqlmock.NewRows([]string{"id", "activity_id"}).AddRow(61, 101))
}

// SpxCopyCategoriesEmpty queues empty assessment/narrative/mission sources
// (the fill-empty skeleton then skips every already-filled lookup).
func SpxCopyCategoriesEmpty(mock sqlmock.Sqlmock, srcSID, cid int64) {
	mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
		WithArgs(srcSID, cid).
		WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}))
	mock.ExpectQuery(q("FROM narratives n")).
		WithArgs(srcSID, cid).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id", "text", "model", "generated_at"}))
	mock.ExpectQuery(q("FROM report_missions rm")).
		WithArgs(srcSID, cid).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id", "mission_id", "name"}))
}

// SpxCopyPhotosEmpty queues empty photo lock + source reads.
func SpxCopyPhotosEmpty(mock sqlmock.Sqlmock, dstSID, srcSID, cid int64) {
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
		WithArgs(dstSID, cid).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
	mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
		WithArgs(srcSID, cid).
		WillReturnRows(sqlmock.NewRows([]string{"path", "mime", "file_size",
			"frame_id", "topic_id"}))
}

// SpxWriteSrcPhoto materialises a real source photo file under the upload root.
func SpxWriteSrcPhoto(t *testing.T, s *Server, rel string, size int) {
	t.Helper()
	full := filepath.Join(s.upload, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = 'x'
	}
	require.NoError(t, os.WriteFile(full, buf, 0o644))
}

// TestSpxCopyExecutePrologueFailures: target/source/membership/child gates
// run BEFORE the idempotency claim — their failures never claim a key.
func TestSpxCopyExecutePrologueFailures(t *testing.T) {
	target := func(mock sqlmock.Sqlmock, err error) {
		exp := mock.ExpectQuery(q("SELECT program_id, status FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1))
		if err != nil {
			exp.WillReturnError(err)
		} else {
			exp.WillReturnRows(sqlmock.NewRows([]string{"program_id", "status"}).
				AddRow(9, "OPENED"))
		}
	}
	source := func(mock sqlmock.Sqlmock, err error) {
		exp := mock.ExpectQuery(q("SELECT school_id, program_id FROM sessions WHERE id = ?")).
			WithArgs(int64(5), int64(1))
		if err != nil {
			exp.WillReturnError(err)
		} else {
			exp.WillReturnRows(sqlmock.NewRows([]string{"school_id", "program_id"}).
				AddRow(int64(1), int64(9)))
		}
	}
	member := func(mock sqlmock.Sqlmock, sid, exists int64) {
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(sid, int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(exists))
	}
	t.Run("target session missing is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, sql.ErrNoRows)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("target read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, SpxErr)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("self or missing source id is validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, nil)
		// No source query may run: the id gate trips first.
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 0}, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("source session missing is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, nil)
		source(mock, sql.ErrNoRows)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("source read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, nil)
		source(mock, SpxErr)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("membership probe fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, nil)
		source(mock, nil)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).WillReturnError(SpxErr)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("child missing on the source is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, nil)
		source(mock, nil)
		member(mock, 2, 1)
		member(mock, 5, 0)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("child read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, nil)
		source(mock, nil)
		member(mock, 2, 1)
		member(mock, 5, 1)
		mock.ExpectQuery(q("SELECT name, is_active FROM children WHERE id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).WillReturnError(SpxErr)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("child missing is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, nil)
		source(mock, nil)
		member(mock, 2, 1)
		member(mock, 5, 1)
		mock.ExpectQuery(q("SELECT name, is_active FROM children WHERE id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).WillReturnError(sql.ErrNoRows)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("begin fails after the claim releases it", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		SpxCopyPrologue(mock, "spx-copy-begin", "OPENED", nil)
		mock.ExpectBegin().WillReturnError(SpxErr)
		SpxIdemRelease(mock, spxCopyRoute, "spx-copy-begin")
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, "spx-copy-begin"))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxCopyMapAndCategoryFailuresReleaseClaim: a failure while loading the
// master-id maps or reading any source category rolls the Tx back and
// RELEASES the claim (top-level DB_UNAVAILABLE is never recorded).
func TestSpxCopyMapAndCategoryFailuresReleaseClaim(t *testing.T) {
	setup := func(t *testing.T, mock sqlmock.Sqlmock, key string) {
		t.Helper()
		SpxCopyPrologue(mock, key, "OPENED", nil)
		mock.ExpectBegin()
	}
	t.Run("topic map read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-map1"
		setup(t, mock, key)
		mock.ExpectQuery(q("SELECT id, topic_id FROM session_topics WHERE session_id = ?")).
			WithArgs(int64(2)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("activity map read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-map2"
		setup(t, mock, key)
		mock.ExpectQuery(q("SELECT id, topic_id FROM session_topics WHERE session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "topic_id"}).AddRow(50, 9))
		mock.ExpectQuery(q("SELECT id, activity_id FROM session_activities WHERE session_id = ?")).
			WithArgs(int64(2)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("assessment source read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-cat1"
		setup(t, mock, key)
		SpxCopyDefaultMaps(mock, 2)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("assessment source scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-cat2"
		setup(t, mock, key)
		SpxCopyDefaultMaps(mock, 2)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}).
				AddRow("not-an-id", 3, nil))
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("assessment source iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-cat3"
		setup(t, mock, key)
		SpxCopyDefaultMaps(mock, 2)
		rows := sqlmock.NewRows([]string{"activity_id", "stars", "note"}).
			AddRow(101, 3, nil).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).WillReturnRows(rows)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("assessment filled lookup fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-cat4"
		setup(t, mock, key)
		SpxCopyDefaultMaps(mock, 2)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}).
				AddRow(101, 3, nil))
		mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
			WithArgs(int64(2), int64(30)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("assessment insert fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-cat5"
		setup(t, mock, key)
		SpxCopyDefaultMaps(mock, 2)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}).
				AddRow(101, 3, nil))
		mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}))
		mock.ExpectExec(q("INSERT INTO assessments")).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("narrative source read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-cat6"
		setup(t, mock, key)
		SpxCopyDefaultMaps(mock, 2)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}))
		mock.ExpectQuery(q("FROM narratives n")).
			WithArgs(int64(5), int64(30)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("mission source read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-cat7"
		setup(t, mock, key)
		SpxCopyDefaultMaps(mock, 2)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}))
		mock.ExpectQuery(q("FROM narratives n")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"topic_id", "text", "model", "generated_at"}))
		mock.ExpectQuery(q("FROM report_missions rm")).
			WithArgs(int64(5), int64(30)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("mission link check fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-cat8"
		setup(t, mock, key)
		SpxCopyDefaultMaps(mock, 2)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}))
		mock.ExpectQuery(q("FROM narratives n")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"topic_id", "text", "model", "generated_at"}))
		mock.ExpectQuery(q("FROM report_missions rm")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"topic_id", "mission_id", "name"}).
				AddRow(9, 77, "Misi"))
		mock.ExpectQuery(q("SELECT session_topic_id FROM report_missions")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id"}))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics")).
			WithArgs(int64(77), int64(9)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxCopyExecuteHappyPathLandsPhotos: the full copy — assessments,
// narrative, mission (topic-linked) and one photo insert in the Tx, then the
// post-commit photo LANDING (real file move + file_ready flip) and the
// copy.completed / report.changed / gallery.changed broadcast (02 §2d).
func TestSpxCopyExecuteHappyPathLandsPhotos(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	key := "spx-copy-happy"
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	setuju := "SETUJU"
	SpxCopyPrologue(mock, key, "OPENED", &setuju)
	mock.ExpectBegin()
	SpxCopyDefaultMaps(mock, 2)
	// assessments: source → fill-empty lookup → insert
	mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
		WithArgs(int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}).
			AddRow(101, 3, "catatan lapangan"))
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}))
	// The copier's identity rides as assessor_id (audit K15: bound, not just
	// passed through).
	mock.ExpectExec(q("INSERT INTO assessments")).
		WithArgs(int64(2), int64(61), int64(30), int64(1), 3, "catatan lapangan",
			"Ayu", int64(2), int64(61)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	// narrative: source → fill-empty → insert
	mock.ExpectQuery(q("FROM narratives n")).
		WithArgs(int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id", "text", "model",
			"generated_at"}).AddRow(9, "Teks naratif", "model-x", fixedTS))
	mock.ExpectQuery(q("SELECT session_topic_id FROM narratives")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id"}))
	// Same binding for narratives: generated_by = the copier.
	mock.ExpectExec(q("INSERT INTO narratives")).
		WithArgs(int64(2), int64(50), int64(30), int64(1), "Teks naratif",
			"model-x", fixedTS, int64(2)).
		WillReturnResult(sqlmock.NewResult(2, 1))
	// mission: source → fill-empty → topic link EXISTS → insert
	mock.ExpectQuery(q("FROM report_missions rm")).
		WithArgs(int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id", "mission_id",
			"name"}).AddRow(9, 77, "Misi Konservasi"))
	mock.ExpectQuery(q("SELECT session_topic_id FROM report_missions")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id"}))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics")).
		WithArgs(int64(77), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(1))
	mock.ExpectExec(q("INSERT INTO report_missions")).
		WillReturnResult(sqlmock.NewResult(3, 1))
	// photo: FOR UPDATE lock (none yet) → source → insert
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
	mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
		WithArgs(int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"path", "mime", "file_size",
			"frame_id", "topic_id"}).
			AddRow("1/5/50/30/src.jpg", "image/jpeg", 1234, nil, 9))
	mock.ExpectExec(q("INSERT INTO photos")).
		WillReturnResult(sqlmock.NewResult(701, 1))
	mock.ExpectCommit()
	// Landing: only the file_ready flip for the pending row (real file move).
	mock.ExpectExec(q("UPDATE photos SET file_ready = TRUE WHERE id = ? AND file_ready = FALSE")).
		WithArgs(int64(701)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	SpxIdemRecord(mock, spxCopyRoute, key)

	SpxWriteSrcPhoto(t, s, "1/5/50/30/src.jpg", 64)

	res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
		map[string]any{"source_session_id": 5}, withIdem(h, key))
	data := requireDataEnvelope(t, res).(map[string]any)

	copied := data["copied"].(map[string]any)
	require.Equal(t, float64(1), copied["assessments"])
	require.Equal(t, float64(1), copied["narratives"])
	require.Equal(t, float64(1), copied["missions"])
	require.Equal(t, float64(1), copied["photos"])
	skipped := data["skipped"].(map[string]any)
	for _, k := range []string{"assessments", "narratives", "missions", "photos"} {
		require.Equal(t, float64(0), skipped[k], "nothing skipped for %s", k)
	}

	f := covNextFrame(t, sub, "copy.completed")
	require.Equal(t, "copy.completed", f.Entity)
	require.Equal(t, "updated", f.Action)
	require.NotNil(t, f.SessionID)
	require.Equal(t, int64(2), *f.SessionID)
	require.Equal(t, int64(30), f.ChildID)

	f = covNextFrame(t, sub, "report.changed for the copied topic")
	require.Equal(t, "report.changed", f.Entity)
	require.Equal(t, int64(50), f.Stid)

	f = covNextFrame(t, sub, "gallery.changed after landing")
	require.Equal(t, "gallery.changed", f.Entity)
	covNoFrame(t, sub, "exactly three broadcasts for one landed photo")

	files, err := filepath.Glob(filepath.Join(s.upload, "1", "2", "50", "30", "*"))
	require.NoError(t, err)
	require.Len(t, files, 1, "the landed photo exists under the upload root")
}

// TestSpxCopyExecutePhotoLimitRollsBack: ≥10 photos already pending for the
// same target topic in this Tx → errPhotoLimit → PHOTO_LIMIT + rollback, the
// recorded envelope replays the refusal.
func TestSpxCopyExecutePhotoLimitRollsBack(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	key := "spx-copy-limit"

	SpxCopyPrologue(mock, key, "OPENED", nil)
	mock.ExpectBegin()
	SpxCopyDefaultMaps(mock, 2)
	SpxCopyCategoriesEmpty(mock, 5, 30)
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
	src := sqlmock.NewRows([]string{"path", "mime", "file_size", "frame_id",
		"topic_id"})
	for i := range 11 {
		src.AddRow(fmt.Sprintf("1/5/50/30/p%d.jpg", i), "image/jpeg", 10, nil, 9)
	}
	mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
		WithArgs(int64(5), int64(30)).WillReturnRows(src)
	for i := range 10 {
		mock.ExpectExec(q("INSERT INTO photos")).
			WillReturnResult(sqlmock.NewResult(int64(701+i), 1))
	}
	mock.ExpectRollback()
	SpxIdemRecord(mock, spxCopyRoute, key)

	res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
		map[string]any{"source_session_id": 5}, withIdem(h, key))
	requireErrEnvelope(t, res, CodePhotoLimit)
}

// TestSpxCopyExecuteConsentRefusedSkipsPhotos: DITOLAK skips EVERY source
// photo (02 §3 consent) — nothing inserts, nothing lands, only
// copy.completed broadcasts.
func TestSpxCopyExecuteConsentRefusedSkipsPhotos(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	key := "spx-copy-ditolak"
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	ditolak := "DITOLAK"
	SpxCopyPrologue(mock, key, "OPENED", &ditolak)
	mock.ExpectBegin()
	SpxCopyDefaultMaps(mock, 2)
	SpxCopyCategoriesEmpty(mock, 5, 30)
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
	src := sqlmock.NewRows([]string{"path", "mime", "file_size", "frame_id",
		"topic_id"})
	src.AddRow("1/5/50/30/a.jpg", "image/jpeg", 10, nil, 9).
		AddRow("1/5/50/30/b.jpg", "image/jpeg", 10, nil, 9)
	mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
		WithArgs(int64(5), int64(30)).WillReturnRows(src)
	mock.ExpectCommit()
	SpxIdemRecord(mock, spxCopyRoute, key)

	res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
		map[string]any{"source_session_id": 5}, withIdem(h, key))
	data := requireDataEnvelope(t, res).(map[string]any)
	skipped := data["skipped"].(map[string]any)
	require.Equal(t, float64(2), skipped["photos"],
		"a refused consent skips every source photo")
	require.Equal(t, float64(0), data["copied"].(map[string]any)["photos"])

	f := covNextFrame(t, sub, "copy.completed")
	require.Equal(t, "copy.completed", f.Entity)
	covNoFrame(t, sub, "refused photos never publish report/gallery changes")
}

// TestSpxCopyExecutePhotoSkipGates: unmapped master topic, missing extension
// and an already-populated target topic each skip one photo (counted, never
// inserted) while the copy still succeeds.
func TestSpxCopyExecutePhotoSkipGates(t *testing.T) {
	t.Run("unmapped topic and missing extension skip", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-skip1"
		SpxCopyPrologue(mock, key, "OPENED", nil)
		mock.ExpectBegin()
		SpxCopyDefaultMaps(mock, 2)
		SpxCopyCategoriesEmpty(mock, 5, 30)
		mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
		src := sqlmock.NewRows([]string{"path", "mime", "file_size", "frame_id",
			"topic_id"})
		// Master topic 999 has no snapshot row on the target…
		src.AddRow("1/5/999/30/x.jpg", "image/jpeg", 10, nil, 999)
		// …and this path carries no extension to land with.
		src.AddRow("1/5/50/30/noext", "image/jpeg", 10, nil, 9)
		mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
			WithArgs(int64(5), int64(30)).WillReturnRows(src)
		mock.ExpectCommit()
		SpxIdemRecord(mock, spxCopyRoute, key)

		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, float64(2), data["skipped"].(map[string]any)["photos"])
		require.Equal(t, float64(0), data["copied"].(map[string]any)["photos"])
	})
	t.Run("target topic already has photos skips", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-skip2"
		SpxCopyPrologue(mock, key, "OPENED", nil)
		mock.ExpectBegin()
		SpxCopyDefaultMaps(mock, 2)
		SpxCopyCategoriesEmpty(mock, 5, 30)
		// The FOR UPDATE lock shows stid 50 already populated…
		mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}).
				AddRow(700, 50))
		src := sqlmock.NewRows([]string{"path", "mime", "file_size", "frame_id",
			"topic_id"}).
			AddRow("1/5/50/30/a.jpg", "image/jpeg", 10, nil, 9)
		mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
			WithArgs(int64(5), int64(30)).WillReturnRows(src)
		mock.ExpectCommit()
		SpxIdemRecord(mock, spxCopyRoute, key)

		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, float64(1), data["skipped"].(map[string]any)["photos"])
		require.Equal(t, float64(0), data["copied"].(map[string]any)["photos"])
	})
	t.Run("duplicate photo insert counts as skipped", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-skip3"
		SpxCopyPrologue(mock, key, "OPENED", nil)
		mock.ExpectBegin()
		SpxCopyDefaultMaps(mock, 2)
		SpxCopyCategoriesEmpty(mock, 5, 30)
		mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
		src := sqlmock.NewRows([]string{"path", "mime", "file_size", "frame_id",
			"topic_id"}).
			AddRow("1/5/50/30/a.jpg", "image/jpeg", 10, nil, 9)
		mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
			WithArgs(int64(5), int64(30)).WillReturnRows(src)
		mock.ExpectExec(q("INSERT INTO photos")).
			WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
		mock.ExpectCommit()
		SpxIdemRecord(mock, spxCopyRoute, key)

		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, float64(1), data["skipped"].(map[string]any)["photos"],
			"a 1062 on the photo insert counts as skipped, not failed")
	})
}

// TestSpxCopyExecuteMissingSourceFileRollsBackLanding: the second photo's
// source file is gone — landing fails AFTER the first one landed, so the
// rollback removes BOTH pending rows and the already-written destination file
// (30 §3 LIFO sweep), the claim is released and the handler surfaces the raw
// failure as 500 INTERNAL.
func TestSpxCopyExecuteMissingSourceFileRollsBackLanding(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	key := "spx-copy-land-fail"
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	SpxCopyPrologue(mock, key, "OPENED", nil)
	mock.ExpectBegin()
	SpxCopyDefaultMaps(mock, 2)
	SpxCopyCategoriesEmpty(mock, 5, 30)
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
	src := sqlmock.NewRows([]string{"path", "mime", "file_size", "frame_id",
		"topic_id"})
	src.AddRow("1/5/50/30/landed.jpg", "image/jpeg", 10, nil, 9).
		AddRow("1/5/50/30/gone.jpg", "image/jpeg", 10, nil, 9)
	mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
		WithArgs(int64(5), int64(30)).WillReturnRows(src)
	mock.ExpectExec(q("INSERT INTO photos")).
		WillReturnResult(sqlmock.NewResult(701, 1))
	mock.ExpectExec(q("INSERT INTO photos")).
		WillReturnResult(sqlmock.NewResult(702, 1))
	mock.ExpectCommit()
	// Photo 701 lands (real file move + ready flip)…
	mock.ExpectExec(q("UPDATE photos SET file_ready = TRUE WHERE id = ? AND file_ready = FALSE")).
		WithArgs(int64(701)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// …then 702 fails to open → BOTH rows roll back and the landed file goes.
	mock.ExpectExec(q("DELETE FROM photos WHERE id = ?")).
		WithArgs(int64(701)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("DELETE FROM photos WHERE id = ?")).
		WithArgs(int64(702)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	SpxIdemRelease(mock, spxCopyRoute, key)

	SpxWriteSrcPhoto(t, s, "1/5/50/30/landed.jpg", 64)
	// 1/5/50/30/gone.jpg intentionally never created.

	res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
		map[string]any{"source_session_id": 5}, withIdem(h, key))
	require.Equal(t, 500, res.status,
		"a non-rejected landing error is NOT a 200 envelope")
	body := envelopeMap(t, res.body)
	require.Equal(t, CodeInternal, body["error"].(map[string]any)["code"],
		"the ErrorHandler surfaces it as INTERNAL")

	files, err := filepath.Glob(filepath.Join(s.upload, "1", "2", "50", "30", "*"))
	require.NoError(t, err)
	require.Empty(t, files, "the landed file was swept back out of upload/")
	covNoFrame(t, sub, "a failed landing never broadcasts copy.completed")
}

// TestSpxCopyExecuteLargePhotoLandsWithoutSizeCheck: Pembaruan 2026-10-11 —
// the landing copy enforces NO size limit (files were client-validated at
// upload). A source photo larger than the former 10 MB cap lands durable,
// its row flips file_ready=TRUE, and the execution answers 200 {data}.
func TestSpxCopyExecuteLargePhotoLandsWithoutSizeCheck(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	key := "spx-copy-large"
	bigSize := 10<<20 + 1 // > the former maxPhotoBytes cap

	SpxCopyPrologue(mock, key, "OPENED", nil)
	mock.ExpectBegin()
	SpxCopyDefaultMaps(mock, 2)
	SpxCopyCategoriesEmpty(mock, 5, 30)
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
	mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
		WithArgs(int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"path", "mime", "file_size",
			"frame_id", "topic_id"}).
			AddRow("1/5/50/30/big.jpg", "image/jpeg", int64(bigSize), nil, 9))
	mock.ExpectExec(q("INSERT INTO photos")).
		WillReturnResult(sqlmock.NewResult(701, 1))
	mock.ExpectCommit()
	// The oversized landing succeeds: file moves durable, row flips ready.
	mock.ExpectExec(q("UPDATE photos SET file_ready = TRUE WHERE id = ? AND file_ready = FALSE")).
		WithArgs(int64(701)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	SpxIdemRecord(mock, spxCopyRoute, key)

	SpxWriteSrcPhoto(t, s, "1/5/50/30/big.jpg", bigSize)

	res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
		map[string]any{"source_session_id": 5}, withIdem(h, key))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(1), data["copied"].(map[string]any)["photos"],
		"the large photo copies without any size VALIDATION")

	files, err := filepath.Glob(filepath.Join(s.upload, "1", "2", "50", "30", "*"))
	require.NoError(t, err)
	require.Len(t, files, 1, "the large photo lands under the upload root")
	stored, err := os.Stat(files[0])
	require.NoError(t, err)
	require.Equal(t, int64(bigSize), stored.Size(), "every byte is copied")
}

// TestSpxCopyExecutePhotoAndCommitFailures: photo lock/source read failures
// and the final COMMIT failure are top-level DB_UNAVAILABLE (claim released,
// rollback except on the dead Tx).
func TestSpxCopyExecutePhotoAndCommitFailures(t *testing.T) {
	setup := func(t *testing.T, mock sqlmock.Sqlmock, key string) {
		t.Helper()
		SpxCopyPrologue(mock, key, "OPENED", nil)
		mock.ExpectBegin()
		SpxCopyDefaultMaps(mock, 2)
		SpxCopyCategoriesEmpty(mock, 5, 30)
	}
	t.Run("photo lock read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-p1"
		setup(t, mock, key)
		mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
			WithArgs(int64(2), int64(30)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("photo lock iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-p2"
		setup(t, mock, key)
		rows := sqlmock.NewRows([]string{"id", "session_topic_id"}).
			AddRow(700, 50).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
			WithArgs(int64(2), int64(30)).WillReturnRows(rows)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("photo source read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-p3"
		setup(t, mock, key)
		mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
		mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
			WithArgs(int64(5), int64(30)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("photo source row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-p4"
		setup(t, mock, key)
		mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
		mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"path", "mime", "file_size",
				"frame_id", "topic_id"}).
				AddRow("1/5/50/30/a.jpg", "image/jpeg", "not-a-size", nil, 9))
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("photo source iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-p5"
		setup(t, mock, key)
		mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
		rows := sqlmock.NewRows([]string{"path", "mime", "file_size", "frame_id",
			"topic_id"}).
			AddRow("1/5/50/30/a.jpg", "image/jpeg", 10, nil, 9).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
			WithArgs(int64(5), int64(30)).WillReturnRows(rows)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("commit fails without rollback", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-p6"
		setup(t, mock, key)
		SpxCopyPhotosEmpty(mock, 2, 5, 30)
		mock.ExpectCommit().WillReturnError(SpxErr)
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// GET /api/sessions/:sid/children/:cid/copy-candidates (FL-16 + E12)
// ---------------------------------------------------------------------------

// TestSpxCopyCandidatesFailureBranches: the candidate probe answers
// NOT_FOUND/DB_UNAVAILABLE per gate, with zero writes (read-only GET).
func TestSpxCopyCandidatesFailureBranches(t *testing.T) {
	target := func(mock sqlmock.Sqlmock, err error) {
		exp := mock.ExpectQuery(q("SELECT school_id, program_id FROM sessions WHERE id = ?")).
			WithArgs(int64(2), int64(1))
		if err != nil {
			exp.WillReturnError(err)
		} else {
			exp.WillReturnRows(sqlmock.NewRows([]string{"school_id", "program_id"}).
				AddRow(int64(1), int64(9)))
		}
	}
	t.Run("target session missing is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, sql.ErrNoRows)
		res := doReq(t, app, "GET",
			"/api/sessions/2/children/30/copy-candidates", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("target read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, SpxErr)
		res := doReq(t, app, "GET",
			"/api/sessions/2/children/30/copy-candidates", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("membership probe fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, nil)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).WillReturnError(SpxErr)
		res := doReq(t, app, "GET",
			"/api/sessions/2/children/30/copy-candidates", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("non-member child is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, nil)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
		res := doReq(t, app, "GET",
			"/api/sessions/2/children/30/copy-candidates", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("candidate list read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, nil)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("WHERE s.school_id = ? AND s.program_id = ? AND s.id <> ?")).
			WithArgs(int64(30), int64(30), int64(30), int64(30), int64(1),
				int64(9), int64(2), int64(30)).WillReturnError(SpxErr)
		res := doReq(t, app, "GET",
			"/api/sessions/2/children/30/copy-candidates", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("candidate row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, nil)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("WHERE s.school_id = ? AND s.program_id = ? AND s.id <> ?")).
			WithArgs(int64(30), int64(30), int64(30), int64(30), int64(1),
				int64(9), int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date",
				"status", "assessments", "narratives", "missions", "photos"}).
				AddRow("not-an-id", "Sesi Lain", fixedTS, "DRAFT", 1, 2, 3, 4))
		res := doReq(t, app, "GET",
			"/api/sessions/2/children/30/copy-candidates", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("candidate iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		target(mock, nil)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		rows := sqlmock.NewRows([]string{"id", "name", "date", "status",
			"assessments", "narratives", "missions", "photos"}).
			AddRow(5, "Sesi Lain", fixedTS, "DRAFT", 1, 2, 3, 4).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("WHERE s.school_id = ? AND s.program_id = ? AND s.id <> ?")).
			WithArgs(int64(30), int64(30), int64(30), int64(30), int64(1),
				int64(9), int64(2), int64(30)).WillReturnRows(rows)
		res := doReq(t, app, "GET",
			"/api/sessions/2/children/30/copy-candidates", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// Round-2 gate details
// ---------------------------------------------------------------------------

// TestSpxCreateBeginAndProgramReadFailuresReleaseClaim: the Tx BEGIN failing
// and a non-NotRows program read failure are both top-level DB_UNAVAILABLE
// (rollback where a Tx exists, claim always released).
func TestSpxCreateBeginAndProgramReadFailuresReleaseClaim(t *testing.T) {
	t.Run("begin fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-begin"
		SpxIdemClaim(mock, spxCreateRoute, key)
		mock.ExpectBegin().WillReturnError(SpxErr)
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
			withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("program read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-create-prog"
		SpxIdemClaim(mock, spxCreateRoute, key)
		mock.ExpectBegin()
		mock.ExpectQuery(q("FROM programs WHERE id = ? AND school_id = ? AND is_active = TRUE")).
			WithArgs(int64(9), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCreateRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions", SpxCreateBody(),
			withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxSessionPatchRejectsEmptyLocationBeforeUpdate: a present-but-empty
// location fails the short-text gate after the row read, with no UPDATE.
func TestSpxSessionPatchRejectsEmptyLocationBeforeUpdate(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	SpxLoadSession(mock, 2, 1, "DRAFT", int64(2))
	res := jsonReq(t, app, "PATCH", "/api/sessions/2",
		map[string]any{"location": ""}, h)
	requireErrEnvelope(t, res, CodeValidation)
}

// TestSpxSessionDeleteBeginFailureIsDBUnavailable: the wipe Tx never starting
// is a 200 DB_UNAVAILABLE without a rollback (nothing began).
func TestSpxSessionDeleteBeginFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	mock.ExpectBegin().WillReturnError(SpxErr)
	res := doReq(t, app, "DELETE", "/api/sessions/2", "", h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestSpxGroupCreateRejectsEmptyNameBeforeBegin: blank group name fails
// validation before any Tx statement.
func TestSpxGroupCreateRejectsEmptyNameBeforeBegin(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	res := jsonReq(t, app, "POST", "/api/sessions/2/groups",
		map[string]any{"name": "   "}, h)
	requireErrEnvelope(t, res, CodeValidation)
}

// TestSpxGroupPatchOwnerReadFailureIsDBUnavailable: a driver failure inside
// requestOwner (not NotRows) surfaces as DB_UNAVAILABLE, distinct from the
// flat VALIDATION its business codes get.
func TestSpxGroupPatchOwnerReadFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	SpxBeginLock(mock, 2, 1)
	mock.ExpectQuery(q("SELECT name FROM session_groups WHERE id = ? AND session_id = ?")).
		WithArgs(int64(10), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Lama"))
	mock.ExpectQuery(q("SELECT role, decision, is_active, school_id FROM users WHERE id = ?")).
		WithArgs(int64(3)).WillReturnError(SpxErr)
	mock.ExpectRollback()
	res := jsonReq(t, app, "PATCH", "/api/sessions/2/groups/10",
		map[string]any{"facilitator_id": 3}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestSpxMembersAddRowReadFailures: child scan, existing-member scan and
// existing-member iteration failures inside the member Tx all abort it.
func TestSpxMembersAddRowReadFailures(t *testing.T) {
	prefix := func(t *testing.T, mock sqlmock.Sqlmock) {
		t.Helper()
		SpxBeginLock(mock, 2, 1)
		mock.ExpectQuery(q("SELECT 1 FROM session_groups WHERE id = ? AND session_id = ?")).
			WithArgs(int64(10), int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	}
	t.Run("child row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		prefix(t, mock)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(31), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}).
				AddRow("not-an-id", "Budi", true))
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("existing member row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		prefix(t, mock)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(31), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}).
				AddRow(31, "Budi", true))
		mock.ExpectQuery(q("SELECT child_id FROM session_members")).
			WithArgs(int64(31), int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"child_id"}).AddRow("not-an-id"))
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("existing member iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		prefix(t, mock)
		mock.ExpectQuery(q("SELECT id, name, is_active FROM children")).
			WithArgs(int64(31), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "is_active"}).
				AddRow(31, "Budi", true))
		rows := sqlmock.NewRows([]string{"child_id"}).
			AddRow(31).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("SELECT child_id FROM session_members")).
			WithArgs(int64(31), int64(2)).WillReturnRows(rows)
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/sessions/2/groups/10/members",
			map[string]any{"child_ids": []int64{31}}, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// Scope provider (06 E2/E4) + direct wipe helpers
// ---------------------------------------------------------------------------

// TestSpxScopeProviderInstallAndFailures: the SSE scope provider installed by
// RegisterSessions — ADMIN resolves unrestricted with zero SQL, FASILITATOR
// composes group/session/child scopes, read failures propagate to refreshScope.
func TestSpxScopeProviderInstallAndFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("admin stays unrestricted", func(t *testing.T) {
		s, _ := newTestServer(t) // zero SQL expected on this path
		_ = newTestApp(t, s)     // RegisterSessions installs the provider
		require.NotNil(t, s.scopeProvider)
		snap, err := s.scopeProvider(ctx, 2, "ADMIN", 1)
		require.NoError(t, err)
		require.Equal(t, "ADMIN", snap.Role)
		require.Equal(t, int64(1), snap.SchoolID)
		require.Equal(t, int64(2), snap.UserID)
		require.Empty(t, snap.OwnGroupIDs)
		require.Empty(t, snap.ChildIDs)
	})
	t.Run("fas snapshot carries groups sessions and children", func(t *testing.T) {
		s, mock := newTestServer(t)
		_ = newTestApp(t, s)
		mock.ExpectQuery(q("SELECT id, session_id FROM session_groups")).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).
				AddRow(10, 2).
				AddRow(11, 3))
		mock.ExpectQuery(q("SELECT id FROM children WHERE school_id = ?")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).
				AddRow(30).
				AddRow(31))
		snap, err := s.scopeProvider(ctx, 3, "FASILITATOR", 1)
		require.NoError(t, err)
		require.Equal(t, []int64{10, 11}, snap.OwnGroupIDs)
		require.Equal(t, []int64{2, 3}, snap.OwnedSessionIDs)
		require.Equal(t, []int64{30, 31}, snap.ChildIDs, "school-wide children scope")
	})
	t.Run("fas scope read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		_ = newTestApp(t, s)
		mock.ExpectQuery(q("SELECT id, session_id FROM session_groups")).
			WithArgs(int64(3), int64(1)).WillReturnError(SpxErr)
		_, err := s.scopeProvider(ctx, 3, "FASILITATOR", 1)
		require.Error(t, err, "a scope read failure must not yield a snapshot")
	})
	t.Run("fas child scope read fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		_ = newTestApp(t, s)
		mock.ExpectQuery(q("SELECT id, session_id FROM session_groups")).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).
				AddRow(10, 2))
		mock.ExpectQuery(q("SELECT id FROM children WHERE school_id = ?")).
			WithArgs(int64(1)).WillReturnError(SpxErr)
		_, err := s.scopeProvider(ctx, 3, "FASILITATOR", 1)
		require.Error(t, err)
	})
}

// TestSpxPhotoPathsTxEmptyScopeTouchesNothing: an empty child scope takes the
// early return — ZERO statements reach the Tx (guards the wipe fast path).
func TestSpxPhotoPathsTxEmptyScopeTouchesNothing(t *testing.T) {
	s, mock := newTestServer(t)
	mock.ExpectBegin()
	mock.ExpectRollback()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	paths, err := s.photoPathsTx(ctx, tx, 1, 2, nil)
	require.NoError(t, err)
	require.Empty(t, paths, "no children → no photo rows to unlink")
	require.NoError(t, tx.Rollback())
}

// TestSpxWipeChildSessionPhotosFailureModes: the consent-revocation helper
// surfaces begin/read/commit failures as errors so callers abort the wipe —
// never a silent partial success.
func TestSpxWipeChildSessionPhotosFailureModes(t *testing.T) {
	ctx := context.Background()
	t.Run("begin fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		mock.ExpectBegin().WillReturnError(SpxErr)
		_, err := s.wipeChildSessionPhotos(ctx, 1, 2, 30)
		require.Error(t, err)
	})
	t.Run("photo select fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT path FROM photos WHERE session_id = ?")).
			WithArgs(int64(2), int64(30), int64(1)).WillReturnError(SpxErr)
		mock.ExpectRollback()
		_, err := s.wipeChildSessionPhotos(ctx, 1, 2, 30)
		require.Error(t, err)
	})
	t.Run("commit fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT path FROM photos WHERE session_id = ?")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"path"}))
		mock.ExpectExec(q("DELETE FROM photos WHERE session_id = ?")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit().WillReturnError(SpxErr)
		_, err := s.wipeChildSessionPhotos(ctx, 1, 2, 30)
		require.Error(t, err)
	})
}

// ---------------------------------------------------------------------------
// Round-2 copy details
// ---------------------------------------------------------------------------

// TestSpxCopyExecuteLandsTwoPhotosOneReportBroadcast: two landed photos of
// the SAME topic produce ONE report.changed (seenStid dedupe) plus a single
// gallery.changed after both rows flip file_ready.
func TestSpxCopyExecuteLandsTwoPhotosOneReportBroadcast(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	key := "spx-copy-2photos"
	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	SpxCopyPrologue(mock, key, "OPENED", nil)
	mock.ExpectBegin()
	SpxCopyDefaultMaps(mock, 2)
	SpxCopyCategoriesEmpty(mock, 5, 30)
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
	src := sqlmock.NewRows([]string{"path", "mime", "file_size", "frame_id",
		"topic_id"})
	src.AddRow("1/5/50/30/a.jpg", "image/jpeg", 10, nil, 9).
		AddRow("1/5/50/30/b.jpg", "image/jpeg", 10, nil, 9)
	mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
		WithArgs(int64(5), int64(30)).WillReturnRows(src)
	mock.ExpectExec(q("INSERT INTO photos")).
		WillReturnResult(sqlmock.NewResult(701, 1))
	mock.ExpectExec(q("INSERT INTO photos")).
		WillReturnResult(sqlmock.NewResult(702, 1))
	mock.ExpectCommit()
	mock.ExpectExec(q("UPDATE photos SET file_ready = TRUE WHERE id = ? AND file_ready = FALSE")).
		WithArgs(int64(701)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE photos SET file_ready = TRUE WHERE id = ? AND file_ready = FALSE")).
		WithArgs(int64(702)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	SpxIdemRecord(mock, spxCopyRoute, key)

	SpxWriteSrcPhoto(t, s, "1/5/50/30/a.jpg", 64)
	SpxWriteSrcPhoto(t, s, "1/5/50/30/b.jpg", 64)

	res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
		map[string]any{"source_session_id": 5}, withIdem(h, key))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(2), data["copied"].(map[string]any)["photos"])

	require.Equal(t, "copy.completed", covNextFrame(t, sub, "copy.completed").Entity)
	f := covNextFrame(t, sub, "report.changed deduped to one")
	require.Equal(t, "report.changed", f.Entity)
	require.Equal(t, int64(50), f.Stid)
	require.Equal(t, "gallery.changed", covNextFrame(t, sub, "gallery.changed").Entity)
	covNoFrame(t, sub, "one report.changed per topic no matter the photo count")

	files, err := filepath.Glob(filepath.Join(s.upload, "1", "2", "50", "30", "*"))
	require.NoError(t, err)
	require.Len(t, files, 2, "both photos landed")
}

// TestSpxCopyMapAndCategoryRowReadFailures: scan/iteration failures inside
// the master-id maps and the fill-empty lookups abort the claimed Tx.
func TestSpxCopyMapAndCategoryRowReadFailures(t *testing.T) {
	setup := func(t *testing.T, mock sqlmock.Sqlmock, key string) {
		t.Helper()
		SpxCopyPrologue(mock, key, "OPENED", nil)
		mock.ExpectBegin()
	}
	t.Run("topic map row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-m1"
		setup(t, mock, key)
		mock.ExpectQuery(q("SELECT id, topic_id FROM session_topics WHERE session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "topic_id"}).
				AddRow("not-an-id", 9))
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("topic map iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-m2"
		setup(t, mock, key)
		rows := sqlmock.NewRows([]string{"id", "topic_id"}).
			AddRow(50, 9).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("SELECT id, topic_id FROM session_topics WHERE session_id = ?")).
			WithArgs(int64(2)).WillReturnRows(rows)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("activity map row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-m3"
		setup(t, mock, key)
		mock.ExpectQuery(q("SELECT id, topic_id FROM session_topics WHERE session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "topic_id"}).AddRow(50, 9))
		mock.ExpectQuery(q("SELECT id, activity_id FROM session_activities WHERE session_id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "activity_id"}).
				AddRow("not-an-id", 101))
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("assessment filled row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-m4"
		setup(t, mock, key)
		SpxCopyDefaultMaps(mock, 2)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}).
				AddRow(101, 3, nil))
		mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}).
				AddRow("not-an-id"))
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("assessment filled iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-m5"
		setup(t, mock, key)
		SpxCopyDefaultMaps(mock, 2)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}).
				AddRow(101, 3, nil))
		rows := sqlmock.NewRows([]string{"session_activity_id"}).
			AddRow(61).
			RowError(0, SpxErr)
		mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
			WithArgs(int64(2), int64(30)).WillReturnRows(rows)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxCopyDuplicateTolerantInserts: a concurrent-fill 1062 and an
// unlinked mission count as SKIPPED — the copy still commits and reports.
func TestSpxCopyDuplicateTolerantInserts(t *testing.T) {
	dup1062 := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}
	t.Run("duplicate assessment insert counts skipped", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-dup1"
		SpxCopyPrologue(mock, key, "OPENED", nil)
		mock.ExpectBegin()
		SpxCopyDefaultMaps(mock, 2)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}).
				AddRow(101, 3, nil))
		mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}))
		mock.ExpectExec(q("INSERT INTO assessments")).WillReturnError(dup1062)
		SpxCopyCategoriesRest := func() {
			mock.ExpectQuery(q("FROM narratives n")).
				WithArgs(int64(5), int64(30)).
				WillReturnRows(sqlmock.NewRows([]string{"topic_id", "text",
					"model", "generated_at"}))
			mock.ExpectQuery(q("FROM report_missions rm")).
				WithArgs(int64(5), int64(30)).
				WillReturnRows(sqlmock.NewRows([]string{"topic_id", "mission_id",
					"name"}))
		}
		SpxCopyCategoriesRest()
		SpxCopyPhotosEmpty(mock, 2, 5, 30)
		mock.ExpectCommit()
		SpxIdemRecord(mock, spxCopyRoute, key)

		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, float64(0), data["copied"].(map[string]any)["assessments"])
		require.Equal(t, float64(1), data["skipped"].(map[string]any)["assessments"])
	})
	t.Run("mission without topic link skips", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-dup2"
		SpxCopyPrologue(mock, key, "OPENED", nil)
		mock.ExpectBegin()
		SpxCopyDefaultMaps(mock, 2)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}))
		mock.ExpectQuery(q("FROM narratives n")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"topic_id", "text", "model",
				"generated_at"}))
		mock.ExpectQuery(q("FROM report_missions rm")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"topic_id", "mission_id",
				"name"}).AddRow(9, 77, "Misi"))
		mock.ExpectQuery(q("SELECT session_topic_id FROM report_missions")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id"}))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics")).
			WithArgs(int64(77), int64(9)).
			WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(0))
		SpxCopyPhotosEmpty(mock, 2, 5, 30)
		mock.ExpectCommit()
		SpxIdemRecord(mock, spxCopyRoute, key)

		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, float64(1), data["skipped"].(map[string]any)["missions"],
			"an unlinked master mission is skipped, not copied")
		require.Equal(t, float64(0), data["copied"].(map[string]any)["missions"])
	})
	t.Run("duplicate mission insert counts skipped", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-dup3"
		SpxCopyPrologue(mock, key, "OPENED", nil)
		mock.ExpectBegin()
		SpxCopyDefaultMaps(mock, 2)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}))
		mock.ExpectQuery(q("FROM narratives n")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"topic_id", "text", "model",
				"generated_at"}))
		mock.ExpectQuery(q("FROM report_missions rm")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"topic_id", "mission_id",
				"name"}).AddRow(9, 77, "Misi"))
		mock.ExpectQuery(q("SELECT session_topic_id FROM report_missions")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id"}))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics")).
			WithArgs(int64(77), int64(9)).
			WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(1))
		mock.ExpectExec(q("INSERT INTO report_missions")).WillReturnError(dup1062)
		SpxCopyPhotosEmpty(mock, 2, 5, 30)
		mock.ExpectCommit()
		SpxIdemRecord(mock, spxCopyRoute, key)

		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, float64(1), data["skipped"].(map[string]any)["missions"])
	})
	t.Run("duplicate narrative insert counts skipped", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-dup4"
		SpxCopyPrologue(mock, key, "OPENED", nil)
		mock.ExpectBegin()
		SpxCopyDefaultMaps(mock, 2)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}))
		mock.ExpectQuery(q("FROM narratives n")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"topic_id", "text", "model",
				"generated_at"}).AddRow(9, "Teks", "model", fixedTS))
		mock.ExpectQuery(q("SELECT session_topic_id FROM narratives")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id"}))
		mock.ExpectExec(q("INSERT INTO narratives")).WillReturnError(dup1062)
		mock.ExpectQuery(q("FROM report_missions rm")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"topic_id", "mission_id",
				"name"}))
		SpxCopyPhotosEmpty(mock, 2, 5, 30)
		mock.ExpectCommit()
		SpxIdemRecord(mock, spxCopyRoute, key)

		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, float64(1), data["skipped"].(map[string]any)["narratives"])
		require.Equal(t, float64(0), data["copied"].(map[string]any)["narratives"])
	})
}

// TestSpxCopyInsertFailuresReleaseClaim: non-duplicate insert failures in
// narrative/mission/photo roll the copy back with the claim released.
func TestSpxCopyInsertFailuresReleaseClaim(t *testing.T) {
	setup := func(t *testing.T, mock sqlmock.Sqlmock, key string) {
		t.Helper()
		SpxCopyPrologue(mock, key, "OPENED", nil)
		mock.ExpectBegin()
		SpxCopyDefaultMaps(mock, 2)
	}
	t.Run("narrative insert fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-i1"
		setup(t, mock, key)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}))
		mock.ExpectQuery(q("FROM narratives n")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"topic_id", "text", "model",
				"generated_at"}).AddRow(9, "Teks", "model", fixedTS))
		mock.ExpectQuery(q("SELECT session_topic_id FROM narratives")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id"}))
		mock.ExpectExec(q("INSERT INTO narratives")).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("mission insert fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-i2"
		setup(t, mock, key)
		mock.ExpectQuery(q("JOIN session_activities sa ON sa.id = a.session_activity_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"activity_id", "stars", "note"}))
		mock.ExpectQuery(q("FROM narratives n")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"topic_id", "text", "model",
				"generated_at"}))
		mock.ExpectQuery(q("FROM report_missions rm")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"topic_id", "mission_id",
				"name"}).AddRow(9, 77, "Misi"))
		mock.ExpectQuery(q("SELECT session_topic_id FROM report_missions")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"session_topic_id"}))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM mission_topics")).
			WithArgs(int64(77), int64(9)).
			WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(1))
		mock.ExpectExec(q("INSERT INTO report_missions")).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("photo insert fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		key := "spx-copy-i3"
		setup(t, mock, key)
		SpxCopyCategoriesEmpty(mock, 5, 30)
		mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
		mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
			WithArgs(int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"path", "mime", "file_size",
				"frame_id", "topic_id"}).
				AddRow("1/5/50/30/a.jpg", "image/jpeg", 10, nil, 9))
		mock.ExpectExec(q("INSERT INTO photos")).WillReturnError(SpxErr)
		mock.ExpectRollback()
		SpxIdemRelease(mock, spxCopyRoute, key)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
			map[string]any{"source_session_id": 5}, withIdem(h, key))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestSpxCopyPhotoLockRowScanFails: a malformed FOR UPDATE lock row aborts
// the copy before any photo is read.
func TestSpxCopyPhotoLockRowScanFails(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	key := "spx-copy-lockscan"
	SpxCopyPrologue(mock, key, "OPENED", nil)
	mock.ExpectBegin()
	SpxCopyDefaultMaps(mock, 2)
	SpxCopyCategoriesEmpty(mock, 5, 30)
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}).
			AddRow("not-an-id", 50))
	mock.ExpectRollback()
	SpxIdemRelease(mock, spxCopyRoute, key)

	res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
		map[string]any{"source_session_id": 5}, withIdem(h, key))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestSpxCopyRollbackRowDeleteFailureStillFailsClosed: when the landing fails
// and even the LIFO row-delete errors, the wipe logs it, keeps removing files
// and still closes with the original failure (500 INTERNAL, claim released).
func TestSpxCopyRollbackRowDeleteFailureStillFailsClosed(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	key := "spx-copy-land-delerr"

	SpxCopyPrologue(mock, key, "OPENED", nil)
	mock.ExpectBegin()
	SpxCopyDefaultMaps(mock, 2)
	SpxCopyCategoriesEmpty(mock, 5, 30)
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM photos")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}))
	src := sqlmock.NewRows([]string{"path", "mime", "file_size", "frame_id",
		"topic_id"})
	src.AddRow("1/5/50/30/landed.jpg", "image/jpeg", 10, nil, 9).
		AddRow("1/5/50/30/gone.jpg", "image/jpeg", 10, nil, 9)
	mock.ExpectQuery(q("SELECT p.path, p.mime, p.file_size, p.frame_id, st.topic_id")).
		WithArgs(int64(5), int64(30)).WillReturnRows(src)
	mock.ExpectExec(q("INSERT INTO photos")).
		WillReturnResult(sqlmock.NewResult(701, 1))
	mock.ExpectExec(q("INSERT INTO photos")).
		WillReturnResult(sqlmock.NewResult(702, 1))
	mock.ExpectCommit()
	mock.ExpectExec(q("UPDATE photos SET file_ready = TRUE WHERE id = ? AND file_ready = FALSE")).
		WithArgs(int64(701)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// The first rollback DELETE errors (logged) — the sweep must continue.
	mock.ExpectExec(q("DELETE FROM photos WHERE id = ?")).
		WithArgs(int64(701)).WillReturnError(SpxErr)
	mock.ExpectExec(q("DELETE FROM photos WHERE id = ?")).
		WithArgs(int64(702)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	SpxIdemRelease(mock, spxCopyRoute, key)

	SpxWriteSrcPhoto(t, s, "1/5/50/30/landed.jpg", 64)

	res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/copy",
		map[string]any{"source_session_id": 5}, withIdem(h, key))
	require.Equal(t, 500, res.status)
	body := envelopeMap(t, res.body)
	require.Equal(t, CodeInternal, body["error"].(map[string]any)["code"])

	files, err := filepath.Glob(filepath.Join(s.upload, "1", "2", "50", "30", "*"))
	require.NoError(t, err)
	require.Empty(t, files, "files still unlink after a row-delete failure")
}

// TestSpxSendReportInactiveChildUsesMsgForCode: the single-report send gate
// maps gateReportItem's CodeChildInactive through the msgForCode default arm
// (an inactive child never reaches delivery; the claim records the refusal).
func TestSpxSendReportInactiveChildUsesMsgForCode(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)
	key := "spx-send-inactive"
	route := "POST /api/sessions/2/topics/50/children/30/send-report"

	SpxIdemClaim(mock, route, key)
	// requirePChild → requireMembership (ADMIN skips the FAS owner read).
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	// Send allowed only on OPENED+FINISHED.
	mock.ExpectQuery(q("SELECT status FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	// photoScope: the topic exists in the session.
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(50), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	// The child row: registered but INACTIVE.
	mock.ExpectQuery(q("SELECT mother_wa, father_wa, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "father_wa", "is_active"}).
			AddRow("081200000000", nil, false))
	SpxIdemRecord(mock, route, key)

	res := jsonReq(t, app, "POST",
		"/api/sessions/2/topics/50/children/30/send-report", map[string]any{},
		withIdem(h, key))
	requireErrEnvelope(t, res, CodeChildInactive)
}

// TestSpxMsgForCodeVocabulary pins the shared gate-message mapping (F-02-09).
// Most arms are already asserted through HTTP envelopes (NotFound,
// NotOwner-less validations, SessionLocked, GroupNoFacilitator,
// ChildInactive); REPORT_INCOMPLETE and DB_UNAVAILABLE have no caller that
// routes them through msgForCode today (their only consumer switch matches
// them explicitly), so the pure mapping is asserted directly — an unknown
// code must fall back to the generic validation message.
func TestSpxMsgForCodeVocabulary(t *testing.T) {
	cases := map[string]string{
		CodeNotFound:           MsgNotFound,
		CodeNotOwner:           MsgNotOwner,
		CodeGroupNoFacilitator: MsgGroupNoFacilitator,
		CodeSessionLocked:      MsgSessionLocked,
		CodeChildInactive:      MsgChildInactive,
		CodeReportIncomplete:   MsgReportIncomplete,
		CodeDBUnavailable:      MsgDBUnavailable,
		CodeValidation:         MsgValidation,
		"SOME_UNKNOWN_CODE":    MsgValidation,
	}
	for code, want := range cases {
		require.Equal(t, want, msgForCode(code), "msgForCode(%q)", code)
	}
}

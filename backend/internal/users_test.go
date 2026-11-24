package internal

import (
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// users_test.go — user management (users.go): list scope (ADMIN pinned to
// own-school facilitators), pending badge, create/patch/approve/reject/
// activate/deactivate, E31 HAS_OPEN_GROUPS delete gate.

// SQL fragments specific to this file (pre-quoted for sqlmock's regexp
// matcher — pass them to ExpectQuery without q()).
var (
	// userItemByID projection (distinct from authed()'s userSelect columns).
	sqlUserItem = regexp.QuoteMeta("SELECT u.id, u.name, u.email, u.role, u.decision, u.is_active,")
	// loadUserForEdit's minimal scoped row.
	sqlEditUser = regexp.QuoteMeta("SELECT id, COALESCE(school_id, 0), role, decision FROM users WHERE id = ?")
	// delete user open-group gate.
	sqlOpenGroups = regexp.QuoteMeta("WHERE g.facilitator_id = ? AND s.status = 'OPENED' ORDER BY g.id LIMIT 50")
	// pending badge counts.
	sqlPendingGlobal  = regexp.QuoteMeta("SELECT COUNT(*) FROM users WHERE decision = 'PENDING'")
	sqlPendingFACount = regexp.QuoteMeta("SELECT COUNT(*) FROM users WHERE decision = 'PENDING' AND school_id = ? AND role = 'FASILITATOR'")
	// users list count query (fixed 2026-10-09: previously emitted a double
	// FROM via userFrom — fragment stays a valid prefix of the corrected
	// `SELECT COUNT(*) FROM users u LEFT JOIN schools sch …`).
	sqlUsersCount = regexp.QuoteMeta("SELECT COUNT(*) FROM users u")
)

// sqlEditUserRow builds one loadUserForEdit row.
func sqlEditUserRow(id, schoolID int64, role, decision string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "school_id", "role", "decision"}).
		AddRow(id, schoolID, role, decision)
}

// idemCreateRoute is the route string recorded in idempotency_keys for
// POST /api/users (06 §3).
const idemCreateRoute = "POST /api/users"

// expectUsersCreateClaim pins the idempotency claim prefix (24 h purge +
// claim INSERT) for POST /api/users with the given key.
func expectUsersCreateClaim(mock sqlmock.Sqlmock, key string) {
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), idemCreateRoute, key).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), idemCreateRoute, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// expectUsersCreateRecord pins the response recording after fn wrote its
// envelope (06 §3 — every non-DB outcome is replayable).
func expectUsersCreateRecord(mock sqlmock.Sqlmock, key string) {
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), idemCreateRoute, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// sqlUserItemRow builds one userItemByID/list row (10 columns).
func sqlUserItemRow(id int64, name, email, role, decision string, active bool,
	school, wa, schoolName any) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "name", "email", "role", "decision", "is_active",
		"school_id", "wa_number", "created_at", "school_name",
	}).AddRow(id, name, email, role, decision, active, school, wa, fixedTS, schoolName)
}

// ---------------------------------------------------------------------------
// Role guards (E8): FAS has no access to any user-management route
// ---------------------------------------------------------------------------

func TestUsersFASIsForbiddenOnEveryRoute(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{"pending-count", "GET", "/api/users/pending-count", ""},
		{"list", "GET", "/api/users", ""},
		{"create", "POST", "/api/users", `{}`},
		{"detail", "GET", "/api/users/3", ""},
		{"patch", "PATCH", "/api/users/3", `{}`},
		{"delete", "DELETE", "/api/users/3", ""},
		{"approve", "POST", "/api/users/3/approve", ""},
		{"deactivate", "POST", "/api/users/3/deactivate", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			expectFASAuth(mock) // guard runs after the user load, before any work
			res := doReq(t, app, tc.method, tc.target, tc.body, hFAS(t, s))
			requireErrEnvelope(t, res, CodeForbiddenRole)
		})
	}
}

// ---------------------------------------------------------------------------
// Pending badge: global for SA, own-school facilitators for ADMIN
// ---------------------------------------------------------------------------

func TestUsersPendingCountScopesByRole(t *testing.T) {
	t.Run("sa counts globally", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		mock.ExpectQuery(sqlPendingGlobal).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(5))
		res := doReq(t, app, "GET", "/api/users/pending-count", "", hSABare(t, s))
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, float64(5), data["pending"])
	})

	t.Run("admin counts own-school facilitators only", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlPendingFACount).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
		res := doReq(t, app, "GET", "/api/users/pending-count", "", hAdmin(t, s))
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, float64(2), data["pending"])
	})

	t.Run("db failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlPendingFACount).WithArgs(int64(1)).
			WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", "/api/users/pending-count", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// List scope (binding, 02 §2b): ADMIN role=ADMIN/SA/omitted → facilitator
// rows only, never admin rows — the scope predicate never widens.
// ---------------------------------------------------------------------------

func TestUsersListAdminScopeNeverWidens(t *testing.T) {
	for _, tc := range []struct {
		name       string
		roleFilter string // query suffix appended to /api/users, "" = omitted
	}{
		{"role=ADMIN", "?role=ADMIN"},
		{"role=SA", "?role=SA"},
		{"role omitted", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			expectAdminAuth(mock)

			// Count: ONLY the school argument — no role argument may leak in.
			mock.ExpectQuery(sqlUsersCount).WithArgs(int64(1)).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			// Rows: scope predicate + ORDER BY; args prove no `u.role = ?`
			// predicate was appended after the facilitator pin.
			mock.ExpectQuery(q("AND u.school_id = ? AND u.role = 'FASILITATOR' ORDER BY id asc, u.id ASC LIMIT ? OFFSET ?")).
				WithArgs(int64(1), 20, 0).
				WillReturnRows(sqlUserItemRow(3, "Fasil", "fas@school.id", "FASILITATOR",
					"APPROVED", true, int64(1), "+628111111111", "Bandung"))

			res := doReq(t, app, "GET", "/api/users"+tc.roleFilter, "", hAdmin(t, s))
			require.Equal(t, 200, res.status)
			m := envelopeMap(t, res.body)
			require.Len(t, m, 2)
			rows := m["data"].([]any)
			require.Len(t, rows, 1)
			require.Equal(t, "FASILITATOR", rows[0].(map[string]any)["role"],
				"ADMIN list must yield facilitator rows")
			require.Equal(t,
				map[string]any{"page": float64(1), "per_page": float64(20), "total": float64(1)},
				m["meta"])
		})
	}
}

func TestUsersListSAFilterAndItemShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectSAAuth(mock, 0)

	mock.ExpectQuery(sqlUsersCount).
		WithArgs("SA", "PENDING", false, "%ad%", "%ad%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("WHERE 1=1 AND u.role = ? AND u.decision = ? AND u.is_active = ? AND (u.name LIKE ? OR u.email LIKE ?) ORDER BY u.created_at desc, u.id ASC LIMIT ? OFFSET ?")).
		WithArgs("SA", "PENDING", false, "%ad%", "%ad%", 50, 50).
		WillReturnRows(sqlUserItemRow(1, "Super", "sa@kidversa.id", "SA",
			"PENDING", true, nil, nil, nil))

	res := doReq(t, app, "GET",
		"/api/users?role=SA&decision=PENDING&is_active=false&q=ad&sort=created_at&dir=desc&page=2&per_page=50",
		"", hSABare(t, s))
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2)
	require.Equal(t,
		map[string]any{"page": float64(2), "per_page": float64(50), "total": float64(1)},
		m["meta"])
	item := m["data"].([]any)[0].(map[string]any)
	require.Equal(t, "SA", item["role"])
	require.Nil(t, item["school_id"], "SA rows carry school_id null")
	require.Nil(t, item["wa_number"], "unset WA serialises as null")
	require.Nil(t, item["school_name"])
	require.Contains(t, item, "created_at")
}

func TestUsersListRejectsBadFiltersWithSingleEnvelope(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	for _, target := range []string{
		"/api/users?role=WIDGET",        // outside the role enum
		"/api/users?decision=BOGUS",     // outside the decision enum
		"/api/users?is_active=maybe",    // ParseBool rejects
		"/api/users?per_page=7",         // not an allowed page size
		"/api/users?sort=password_hash", // outside the sort allowlist
	} {
		expectAdminAuth(mock) // rejected before COUNT/SELECT
		res := doReq(t, app, "GET", target, "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	}
}

func TestUsersListCountFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectAdminAuth(mock)
	mock.ExpectQuery(sqlUsersCount).WithArgs(int64(1)).
		WillReturnError(errors.New("connection refused"))
	res := doReq(t, app, "GET", "/api/users", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// Create: validation → code mapping, email uniqueness, publish
// ---------------------------------------------------------------------------

func TestUsersCreateValidationMatrix(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	for _, tc := range []struct {
		name string
		body map[string]any
		code string
		sa   bool // SA without header instead of ADMIN
	}{
		{"blank name", map[string]any{"name": "  ", "email": "a@b.id",
			"password": testPassword, "role": "FASILITATOR"}, CodeValidation, false},
		{"bad email", map[string]any{"name": "X", "email": "nope",
			"password": testPassword, "role": "FASILITATOR"}, CodeValidation, false},
		{"weak password", map[string]any{"name": "X", "email": "a@b.id",
			"password": "short1", "role": "FASILITATOR"}, CodeValidation, false},
		{"bad wa", map[string]any{"name": "X", "email": "a@b.id",
			"wa_number": "12345", "password": testPassword, "role": "FASILITATOR"},
			CodeValidation, false},
		{"role SA is unassignable", map[string]any{"name": "X", "email": "a@b.id",
			"wa_number": "08123456789", "password": testPassword, "role": "SA"},
			CodeForbiddenRole, false},
		{"unknown role", map[string]any{"name": "X", "email": "a@b.id",
			"wa_number": "08123456789", "password": testPassword, "role": "WIDGET"},
			CodeValidation, false},
		{"admin cannot mint admins", map[string]any{"name": "X", "email": "a@b.id",
			"wa_number": "08123456789", "password": testPassword, "role": "ADMIN"},
			CodeForbiddenRole, false},
		{"sa without school target", map[string]any{"name": "X", "email": "a@b.id",
			"wa_number": "08123456789", "password": testPassword, "role": "ADMIN",
			"school_id": 0}, CodeValidation, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var h map[string]string
			if tc.sa {
				expectSAAuth(mock, 0) // bare SA → kvSchool 0 → VALIDATION
				h = hSABare(t, s)
			} else {
				expectAdminAuth(mock)
				h = hAdmin(t, s)
			}
			res := jsonReq(t, app, "POST", "/api/users", tc.body, h)
			requireErrEnvelope(t, res, tc.code)
		})
	}
}

func TestUsersCreateInactiveSchoolIsSchoolInactive(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectAdminAuth(mock)
	mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(false))

	res := jsonReq(t, app, "POST", "/api/users", map[string]any{
		"name": "Fasil Dua", "email": "fas2@school.id", "password": testPassword,
		"wa_number": "08123456789", "role": "FASILITATOR",
	}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeSchoolInactive)
}

func TestUsersCreateEmailTakenPaths(t *testing.T) {
	body := map[string]any{
		"name": "Fasil Dua", "email": "fas2@school.id", "password": testPassword,
		"wa_number": "08123456789", "role": "FASILITATOR",
	}

	t.Run("existing email", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		expectSchoolActive(mock, 1)
		expectUsersCreateClaim(mock, "usr-taken-1")
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
			WithArgs("fas2@school.id").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		expectUsersCreateRecord(mock, "usr-taken-1") // error envelopes replay too
		res := jsonReq(t, app, "POST", "/api/users", body,
			withIdem(hAdmin(t, s), "usr-taken-1"))
		requireErrEnvelope(t, res, CodeEmailTaken)
	})

	t.Run("unique-key race on INSERT", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		expectSchoolActive(mock, 1)
		expectUsersCreateClaim(mock, "usr-race-1")
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
			WithArgs("fas2@school.id").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
		mock.ExpectExec(q("INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)")).
			WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
		expectUsersCreateRecord(mock, "usr-race-1")
		res := jsonReq(t, app, "POST", "/api/users", body,
			withIdem(hAdmin(t, s), "usr-race-1"))
		requireErrEnvelope(t, res, CodeEmailTaken)
	})

	t.Run("reload miss after insert is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		expectSchoolActive(mock, 1)
		expectUsersCreateClaim(mock, "usr-miss-1")
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
			WithArgs("fas2@school.id").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
		mock.ExpectExec(q("INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)")).
			WithArgs(int64(1), "Fasil Dua", "fas2@school.id", "+628123456789",
				sqlmock.AnyArg(), "FASILITATOR").
			WillReturnResult(sqlmock.NewResult(10, 1))
		mock.ExpectQuery(sqlUserItem).WithArgs(int64(10)).
			WillReturnError(sql.ErrNoRows)
		expectUsersCreateRecord(mock, "usr-miss-1")
		res := jsonReq(t, app, "POST", "/api/users", body,
			withIdem(hAdmin(t, s), "usr-miss-1"))
		requireErrEnvelope(t, res, CodeNotFound)
	})
}

func TestUsersCreateHappyPathPublishesUserChanged(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	expectSchoolActive(mock, 1)
	expectUsersCreateClaim(mock, "usr-happy-1")
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
		WithArgs("fas2@school.id").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	mock.ExpectExec(q("INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)")).
		WithArgs(int64(1), "Fasil Dua", "fas2@school.id", "+628123456789",
			sqlmock.AnyArg(), "FASILITATOR").
		WillReturnResult(sqlmock.NewResult(10, 1))
	mock.ExpectQuery(sqlUserItem).WithArgs(int64(10)).
		WillReturnRows(sqlUserItemRow(10, "Fasil Dua", "fas2@school.id", "FASILITATOR",
			"APPROVED", true, int64(1), "+628123456789", "Bandung"))
	expectUsersCreateRecord(mock, "usr-happy-1")

	res := jsonReq(t, app, "POST", "/api/users", map[string]any{
		"name": "  Fasil Dua ", "email": " FAS2@School.ID ",
		"wa_number": "08123456789", "password": testPassword, "role": "FASILITATOR",
	}, withIdem(hAdmin(t, s), "usr-happy-1"))
	data := requireDataEnvelope(t, res).(map[string]any)
	user := data["user"].(map[string]any)
	require.Equal(t, float64(10), user["id"])
	require.Equal(t, "fas2@school.id", user["email"], "email is lowercased+trimmed")
	require.Equal(t, "APPROVED", user["decision"], "creator is the approving authority")
	require.Equal(t, true, user["is_active"])
	require.Equal(t, "Bandung", user["school_name"])

	f := takeFrame(t, spy)
	require.Equal(t, "user.changed", f.Entity)
	require.Equal(t, int64(10), f.EntityID)
	require.Equal(t, "created", f.Action)
	require.Equal(t, int64(1), f.SchoolID)
}

// ---------------------------------------------------------------------------
// Idempotency (06 §3, K2): missing key is VALIDATION; a replay within 24 h
// returns the original envelope byte-for-byte with the INSERT fired once.
// ---------------------------------------------------------------------------

// TestUsersCreateRequiresIdempotencyKey pins the 06 §3 gate: past the domain
// validation, POST /api/users without X-Idempotency-Key answers VALIDATION
// before any idempotency-claim or users SQL runs.
func TestUsersCreateRequiresIdempotencyKey(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	expectSchoolActive(mock, 1) // school gate runs before the key gate
	// No claim/INSERT expectations: the mock rejects any stray query/exec.

	res := jsonReq(t, app, "POST", "/api/users", map[string]any{
		"name": "Fasil Dua", "email": "fas2@school.id", "password": testPassword,
		"wa_number": "08123456789", "role": "FASILITATOR",
	}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeValidation)
}

// TestUsersCreateIdemReplayReturnsOriginalEnvelope: a second POST /api/users
// with the same key replays the first envelope verbatim. The mock carries a
// SINGLE INSERT INTO users expectation, so any second insert fails the test —
// the replay has no side effect (no second frame either).
func TestUsersCreateIdemReplayReturnsOriginalEnvelope(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	body := map[string]any{
		"name": "Fasil Dua", "email": "fas2@school.id", "password": testPassword,
		"wa_number": "08123456789", "role": "FASILITATOR",
	}
	h := withIdem(hAdmin(t, s), "usr-replay-1")

	// First request: claim, insert once, record the success envelope.
	expectAdminAuth(mock)
	expectSchoolActive(mock, 1)
	expectUsersCreateClaim(mock, "usr-replay-1")
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
		WithArgs("fas2@school.id").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	mock.ExpectExec(q("INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)")).
		WithArgs(int64(1), "Fasil Dua", "fas2@school.id", "+628123456789",
			sqlmock.AnyArg(), "FASILITATOR").
		WillReturnResult(sqlmock.NewResult(10, 1))
	mock.ExpectQuery(sqlUserItem).WithArgs(int64(10)).
		WillReturnRows(sqlUserItemRow(10, "Fasil Dua", "fas2@school.id", "FASILITATOR",
			"APPROVED", true, int64(1), "+628123456789", "Bandung"))
	expectUsersCreateRecord(mock, "usr-replay-1")

	res1 := jsonReq(t, app, "POST", "/api/users", body, h)
	require.Equal(t, 200, res1.status)
	requireDataEnvelope(t, res1)
	require.Equal(t, "created", takeFrame(t, spy).Action)

	// Second request, same key: duplicate claim → stored envelope replays.
	// NO second INSERT INTO users expectation — it would fail the mock.
	expectAdminAuth(mock)
	expectSchoolActive(mock, 1)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), idemCreateRoute, "usr-replay-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), idemCreateRoute, "usr-replay-1").
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), idemCreateRoute, "usr-replay-1").
		WillReturnRows(sqlmock.NewRows([]string{"response"}).AddRow(res1.body))

	res2 := jsonReq(t, app, "POST", "/api/users", body, h)
	require.Equal(t, 200, res2.status)
	require.Equal(t, res1.body, res2.body, "replay must return the original envelope verbatim")
	requireNoFrame(t, spy, "replay must not publish a second frame")
}

// ---------------------------------------------------------------------------
// Detail scope: loadUserForEdit (NOT_FOUND / FORBIDDEN_ROLE / SA pass)
// ---------------------------------------------------------------------------

func TestUsersGetDetailScoped(t *testing.T) {
	t.Run("admin reads own facilitator", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
			WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
		mock.ExpectQuery(sqlUserItem).WithArgs(int64(3)).
			WillReturnRows(sqlUserItemRow(3, "Fasil", "fas@school.id", "FASILITATOR",
				"APPROVED", true, int64(1), "+628111111111", "Bandung"))
		res := doReq(t, app, "GET", "/api/users/3", "", hAdmin(t, s))
		user := requireDataEnvelope(t, res).(map[string]any)["user"].(map[string]any)
		require.Equal(t, "fas@school.id", user["email"])
	})

	t.Run("admin touching an admin row is FORBIDDEN_ROLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(9)).
			WillReturnRows(sqlEditUserRow(9, 1, "ADMIN", "APPROVED"))
		res := doReq(t, app, "GET", "/api/users/9", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})

	t.Run("cross-school id is NOT_FOUND (no oracle)", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(77)).
			WillReturnRows(sqlEditUserRow(77, 2, "FASILITATOR", "APPROVED"))
		res := doReq(t, app, "GET", "/api/users/77", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("unknown id is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(404)).
			WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/api/users/404", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("scope lookup failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
			WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", "/api/users/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// Patch: name/email/WA only; email uniqueness; SA rows broadcast at 0
// ---------------------------------------------------------------------------

func TestUsersPatchNameTrimsAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
		WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
	mock.ExpectExec(q("UPDATE users SET name = ? WHERE id = ?")).
		WithArgs("Nama Baru", int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(sqlUserItem).WithArgs(int64(3)).
		WillReturnRows(sqlUserItemRow(3, "Nama Baru", "fas@school.id", "FASILITATOR",
			"APPROVED", true, int64(1), "+628111111111", "Bandung"))

	res := jsonReq(t, app, "PATCH", "/api/users/3",
		map[string]any{"name": "  Nama Baru "}, hAdmin(t, s))
	user := requireDataEnvelope(t, res).(map[string]any)["user"].(map[string]any)
	require.Equal(t, "Nama Baru", user["name"])

	f := takeFrame(t, spy)
	require.Equal(t, "user.changed", f.Entity)
	require.Equal(t, "updated", f.Action)
	require.Equal(t, int64(3), f.EntityID)
}

func TestUsersPatchEmailFlow(t *testing.T) {
	t.Run("success lowercases and checks uniqueness", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
			WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ? AND id <> ?")).
			WithArgs("new@school.id", int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
		mock.ExpectExec(q("UPDATE users SET email = ? WHERE id = ?")).
			WithArgs("new@school.id", int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(sqlUserItem).WithArgs(int64(3)).
			WillReturnRows(sqlUserItemRow(3, "Fasil", "new@school.id", "FASILITATOR",
				"APPROVED", true, int64(1), "+628111111111", "Bandung"))
		res := jsonReq(t, app, "PATCH", "/api/users/3",
			map[string]any{"email": " NEW@School.ID "}, hAdmin(t, s))
		user := requireDataEnvelope(t, res).(map[string]any)["user"].(map[string]any)
		require.Equal(t, "new@school.id", user["email"])
	})

	t.Run("duplicate email is EMAIL_TAKEN", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
			WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ? AND id <> ?")).
			WithArgs("taken@school.id", int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		res := jsonReq(t, app, "PATCH", "/api/users/3",
			map[string]any{"email": "taken@school.id"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeEmailTaken)
	})

	t.Run("unique-key race on UPDATE is EMAIL_TAKEN", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
			WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ? AND id <> ?")).
			WithArgs("race@school.id", int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
		mock.ExpectExec(q("UPDATE users SET email = ? WHERE id = ?")).
			WithArgs("race@school.id", int64(3)).
			WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
		res := jsonReq(t, app, "PATCH", "/api/users/3",
			map[string]any{"email": "race@school.id"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeEmailTaken)
	})

	t.Run("malformed email is VALIDATION before any write", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
			WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
		res := jsonReq(t, app, "PATCH", "/api/users/3",
			map[string]any{"email": "not-an-email"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})
}

func TestUsersPatchWaNumberHandling(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wa      any
		wantSQL string
		wantArg int // 0 = NULL update (id only), 1 = value update
	}{
		{"normalise replacement", "+62 811-1111-1111", "UPDATE users SET wa_number = ? WHERE id = ?", 1},
		{"empty clears to NULL", "", "UPDATE users SET wa_number = NULL WHERE id = ?", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			expectAdminAuth(mock)
			mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
				WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
			if tc.wantArg == 1 {
				mock.ExpectExec(q(tc.wantSQL)).
					WithArgs("+6281111111111", int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				mock.ExpectExec(q(tc.wantSQL)).
					WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectQuery(sqlUserItem).WithArgs(int64(3)).
				WillReturnRows(sqlUserItemRow(3, "Fasil", "fas@school.id", "FASILITATOR",
					"APPROVED", true, int64(1), nil, "Bandung"))
			res := jsonReq(t, app, "PATCH", "/api/users/3",
				map[string]any{"wa_number": tc.wa}, hAdmin(t, s))
			requireDataEnvelope(t, res)
		})
	}

	t.Run("malformed WA is VALIDATION", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
			WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
		res := jsonReq(t, app, "PATCH", "/api/users/3",
			map[string]any{"wa_number": "999"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})
}

func TestUsersPatchEmptyBodyRepublishesRow(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
		WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
	// No UPDATE: {} touches nothing but still reloads+publishes.
	mock.ExpectQuery(sqlUserItem).WithArgs(int64(3)).
		WillReturnRows(sqlUserItemRow(3, "Fasil", "fas@school.id", "FASILITATOR",
			"APPROVED", true, int64(1), "+628111111111", "Bandung"))

	res := jsonReq(t, app, "PATCH", "/api/users/3", map[string]any{}, hAdmin(t, s))
	requireDataEnvelope(t, res)
	require.Equal(t, "updated", takeFrame(t, spy).Action)
}

func TestUsersPatchBlankNameIsValidation(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectAdminAuth(mock)
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
		WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
	res := jsonReq(t, app, "PATCH", "/api/users/3",
		map[string]any{"name": "   "}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeValidation)
}

func TestUsersPatchSARowBroadcastsAtSchoolZero(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 0) // broadcast scope (SA rows have no school)
	defer s.unsubscribe(spy)

	expectSAAuth(mock, 0) // SA bypasses the facilitator scope in loadUserForEdit
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(1)).
		WillReturnRows(sqlEditUserRow(1, 0, "SA", "APPROVED"))
	mock.ExpectExec(q("UPDATE users SET name = ? WHERE id = ?")).
		WithArgs("Super Admin", int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(sqlUserItem).WithArgs(int64(1)).
		WillReturnRows(sqlUserItemRow(1, "Super Admin", "sa@kidversa.id", "SA",
			"APPROVED", true, nil, nil, nil))

	res := jsonReq(t, app, "PATCH", "/api/users/1",
		map[string]any{"name": "Super Admin"}, hSABare(t, s))
	user := requireDataEnvelope(t, res).(map[string]any)["user"].(map[string]any)
	require.Equal(t, "Super Admin", user["name"])

	f := takeFrame(t, spy)
	require.Equal(t, "user.changed", f.Entity)
	require.Equal(t, int64(0), f.SchoolID, "SA-row frames broadcast (pubSchool=0)")
}

// ---------------------------------------------------------------------------
// Delete: E31 HAS_OPEN_GROUPS "+ list", else one Tx + publish (+ pending)
// ---------------------------------------------------------------------------

func TestUsersDeleteOwnsOpenGroupsIsHasOpenGroups(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
		WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
	mock.ExpectQuery(sqlOpenGroups).WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"gid", "sid", "sname"}).
			AddRow(10, 5, "Sesi A").
			AddRow(12, 6, "Sesi B"))
	// NO ExpectBegin: the gate must answer before any Tx starts.

	res := jsonReq(t, app, "DELETE", "/api/users/3", nil, hAdmin(t, s))
	errObj := requireErrEnvelope(t, res, CodeHasOpenGroups)
	require.Equal(t, MsgHasOpenGroups, errObj["message"])
	groups, ok := errObj["groups"].([]any)
	require.Truef(t, ok, "E31 ships the offending groups: %s", res.body)
	require.Len(t, groups, 2)
	require.Equal(t,
		map[string]any{"gid": float64(10), "session_id": float64(5), "session_name": "Sesi A"},
		groups[0])
	require.Equal(t,
		map[string]any{"gid": float64(12), "session_id": float64(6), "session_name": "Sesi B"},
		groups[1])
}

func TestUsersDeleteCommitsSingleTxAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
		WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
	mock.ExpectQuery(sqlOpenGroups).WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"gid", "sid", "sname"}))
	mock.ExpectBegin()
	mock.ExpectExec(q("DELETE FROM refresh_tokens WHERE user_id = ? AND impersonated_by IS NULL")).
		WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(q("DELETE FROM reset_tokens WHERE user_id = ? AND source = 'SELF'")).
		WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("DELETE FROM users WHERE id = ?")).
		WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// APPROVED target → no pending recount (mock would fail on a stray query).

	res := jsonReq(t, app, "DELETE", "/api/users/3", nil, hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])

	f := takeFrame(t, spy)
	require.Equal(t, "user.changed", f.Entity)
	require.Equal(t, "deleted", f.Action)
	require.Equal(t, int64(3), f.EntityID)
	requireNoFrame(t, spy)
}

func TestUsersDeletePendingTargetRecountsBadge(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(8)).
		WillReturnRows(sqlEditUserRow(8, 1, "FASILITATOR", "PENDING"))
	mock.ExpectQuery(sqlOpenGroups).WithArgs(int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"gid", "sid", "sname"}))
	mock.ExpectBegin()
	mock.ExpectExec(q("DELETE FROM refresh_tokens WHERE user_id = ? AND impersonated_by IS NULL")).
		WithArgs(int64(8)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("DELETE FROM reset_tokens WHERE user_id = ? AND source = 'SELF'")).
		WithArgs(int64(8)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("DELETE FROM users WHERE id = ?")).
		WithArgs(int64(8)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(sqlPendingFACount).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	res := jsonReq(t, app, "DELETE", "/api/users/8", nil, hAdmin(t, s))
	require.Equal(t, true, requireDataEnvelope(t, res).(map[string]any)["deleted"])

	f1 := takeFrame(t, spy)
	require.Equal(t, "user.changed", f1.Entity)
	require.Equal(t, "deleted", f1.Action)
	f2 := takeFrame(t, spy)
	require.Equal(t, "pending.changed", f2.Entity, "PENDING target republishes the badge")
	require.NotNil(t, f2.Pending)
	require.Equal(t, 3, *f2.Pending)
	requireNoFrame(t, spy)
}

func TestUsersDeleteCrossSchoolStopsAtScopeGate(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectAdminAuth(mock)
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(77)).
		WillReturnRows(sqlEditUserRow(77, 2, "FASILITATOR", "APPROVED"))
	// No open-group query, no Tx: cross-school dies at loadUserForEdit.
	res := jsonReq(t, app, "DELETE", "/api/users/77", nil, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

// ---------------------------------------------------------------------------
// Decision transitions (approve/reject) + badge publish
// ---------------------------------------------------------------------------

func TestUsersApproveRepublishesUserAndBadge(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(8)).
		WillReturnRows(sqlEditUserRow(8, 1, "FASILITATOR", "PENDING"))
	mock.ExpectExec(q("UPDATE users SET decision = ?, is_active = ? WHERE id = ?")).
		WithArgs("APPROVED", true, int64(8)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(sqlPendingFACount).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(sqlUserItem).WithArgs(int64(8)).
		WillReturnRows(sqlUserItemRow(8, "Fasil", "fas@school.id", "FASILITATOR",
			"APPROVED", true, int64(1), "+628111111111", "Bandung"))

	res := jsonReq(t, app, "POST", "/api/users/8/approve", nil, hAdmin(t, s))
	user := requireDataEnvelope(t, res).(map[string]any)["user"].(map[string]any)
	require.Equal(t, "APPROVED", user["decision"])
	require.Equal(t, true, user["is_active"])

	f1 := takeFrame(t, spy)
	require.Equal(t, "user.changed", f1.Entity)
	require.Equal(t, "updated", f1.Action)
	f2 := takeFrame(t, spy)
	require.Equal(t, "pending.changed", f2.Entity)
	require.Equal(t, 1, *f2.Pending)
}

func TestUsersRejectRetainsRowAndDeactivates(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(8)).
		WillReturnRows(sqlEditUserRow(8, 1, "FASILITATOR", "PENDING"))
	mock.ExpectExec(q("UPDATE users SET decision = ?, is_active = ? WHERE id = ?")).
		WithArgs("REJECTED", false, int64(8)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(sqlPendingFACount).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(sqlUserItem).WithArgs(int64(8)).
		WillReturnRows(sqlUserItemRow(8, "Fasil", "fas@school.id", "FASILITATOR",
			"REJECTED", false, int64(1), "+628111111111", "Bandung"))

	res := jsonReq(t, app, "POST", "/api/users/8/reject", nil, hAdmin(t, s))
	user := requireDataEnvelope(t, res).(map[string]any)["user"].(map[string]any)
	require.Equal(t, "REJECTED", user["decision"])
	require.Equal(t, false, user["is_active"], "reject deactivates but retains the row")
	f1 := takeFrame(t, spy)
	require.Equal(t, "user.changed", f1.Entity)
	f2 := takeFrame(t, spy)
	require.Equal(t, "pending.changed", f2.Entity)
}

func TestUsersDecisionReloadMissIsNotFound(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(8)).
		WillReturnRows(sqlEditUserRow(8, 1, "FASILITATOR", "PENDING"))
	mock.ExpectExec(q("UPDATE users SET decision = ?, is_active = ? WHERE id = ?")).
		WithArgs("APPROVED", true, int64(8)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(sqlPendingFACount).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(sqlUserItem).WithArgs(int64(8)).WillReturnError(sql.ErrNoRows)

	res := jsonReq(t, app, "POST", "/api/users/8/approve", nil, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

// TestUsersBadgeRecountFailureNeverFailsMutation pins publishPending's
// best-effort contract: the recount error only logs — the decision still 200s.
func TestUsersBadgeRecountFailureNeverFailsMutation(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(8)).
		WillReturnRows(sqlEditUserRow(8, 1, "FASILITATOR", "PENDING"))
	mock.ExpectExec(q("UPDATE users SET decision = ?, is_active = ? WHERE id = ?")).
		WithArgs("APPROVED", true, int64(8)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(sqlPendingFACount).WithArgs(int64(1)).
		WillReturnError(errors.New("connection refused")) // logged, not fatal
	mock.ExpectQuery(sqlUserItem).WithArgs(int64(8)).
		WillReturnRows(sqlUserItemRow(8, "Fasil", "fas@school.id", "FASILITATOR",
			"APPROVED", true, int64(1), "+628111111111", "Bandung"))

	res := jsonReq(t, app, "POST", "/api/users/8/approve", nil, hAdmin(t, s))
	user := requireDataEnvelope(t, res).(map[string]any)["user"].(map[string]any)
	require.Equal(t, "APPROVED", user["decision"], "badge failure never fails the mutation")
	require.Equal(t, "user.changed", takeFrame(t, spy).Entity)
	requireNoFrame(t, spy) // no pending.changed without a count
}

// ---------------------------------------------------------------------------
// Activate / deactivate (deactivate revokes refresh rows in the SAME Tx)
// ---------------------------------------------------------------------------

func TestUsersActivateCommitsIsAlone(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
		WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "REJECTED"))
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE users SET is_active = ? WHERE id = ?")).
		WithArgs(true, int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	// No refresh_tokens revoke on activation — the mock rejects stray execs.
	mock.ExpectCommit()
	mock.ExpectQuery(sqlUserItem).WithArgs(int64(3)).
		WillReturnRows(sqlUserItemRow(3, "Fasil", "fas@school.id", "FASILITATOR",
			"REJECTED", true, int64(1), "+628111111111", "Bandung"))

	res := jsonReq(t, app, "POST", "/api/users/3/activate", nil, hAdmin(t, s))
	user := requireDataEnvelope(t, res).(map[string]any)["user"].(map[string]any)
	require.Equal(t, true, user["is_active"])
	require.Equal(t, "user.changed", takeFrame(t, spy).Entity)
}

func TestUsersDeactivateRevokesRefreshTokensInSameTx(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
		WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE users SET is_active = ? WHERE id = ?")).
		WithArgs(false, int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW()")).
		WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	mock.ExpectQuery(sqlUserItem).WithArgs(int64(3)).
		WillReturnRows(sqlUserItemRow(3, "Fasil", "fas@school.id", "FASILITATOR",
			"APPROVED", false, int64(1), "+628111111111", "Bandung"))

	res := jsonReq(t, app, "POST", "/api/users/3/deactivate", nil, hAdmin(t, s))
	user := requireDataEnvelope(t, res).(map[string]any)["user"].(map[string]any)
	require.Equal(t, false, user["is_active"], "deactivation is immediate logout")
	require.Equal(t, "user.changed", takeFrame(t, spy).Entity)
}

func TestUsersDeactivateReloadMissIsNotFound(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
		WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE users SET is_active = ? WHERE id = ?")).
		WithArgs(false, int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW()")).
		WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	mock.ExpectQuery(sqlUserItem).WithArgs(int64(3)).WillReturnError(sql.ErrNoRows)

	res := jsonReq(t, app, "POST", "/api/users/3/deactivate", nil, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

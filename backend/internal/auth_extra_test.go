package internal

// auth_extra_test.go closes the remaining coverage gaps in auth.go,
// envelope.go, config.go, idempotency.go and validate.go. Every test asserts
// an observable contract: HTTP 200 + {data} for success, HTTP 200 + exactly
// {error:{code,message}} for business failures, or the documented 500
// INTERNAL for unhandled faults. Validation paths run with ZERO sqlmock
// expectations (a stray query would surface as DB_UNAVAILABLE and fail the
// envelope assertion); DB branches inject sqlmock errors on the exact
// query and assert the resulting code.

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Local helpers (Asx prefix: 8 agents merge into one package)
// ---------------------------------------------------------------------------

// AsxSign mints an HS256 JWT for arbitrary claims — used to craft tokens the
// production mint helpers cannot produce (expired, malformed subject).
func AsxSign(t *testing.T, secret string, claims jwt.Claims) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	require.NoError(t, err)
	return tok
}

func AsxAdminH(t *testing.T, s *Server) map[string]string {
	t.Helper()
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	return h
}

func AsxFASH(t *testing.T, s *Server) map[string]string {
	t.Helper()
	h := bearer(mintAccess(t, s, fasUserRow()))
	h["X-School-ID"] = "1"
	return h
}

func AsxSAH(t *testing.T, s *Server) map[string]string {
	t.Helper()
	return bearer(mintAccess(t, s, saUserRow()))
}

func AsxResetBody(email string) map[string]any {
	return map[string]any{"email": email, "code": "aabbccdd", "new_password": "BrandNew1"}
}

func AsxRegisterBody(email string) map[string]any { return registerBody(email) }

// AsxResetListRows builds one handleResetList row with a controllable expiry.
func AsxResetListRows(id int64, plain any, expiresAt time.Time, mailFailed bool) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "code_plain", "expires_at", "revoked_at", "mail_failed",
		"u_id", "u_name", "u_email",
	}).AddRow(id, plain, expiresAt, nil, mailFailed,
		int64(3), "Fasil", "fas@school.id")
}

func AsxResetCount(mock sqlmock.Sqlmock, total int64) {
	mock.ExpectQuery(q(sqlResetCount)).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(total))
}

// ---------------------------------------------------------------------------
// Guards: garbage / expired / malformed tokens (authed + refreshAuth)
// ---------------------------------------------------------------------------

// TestAsxAuthGuardRejectsGarbageExpiredAndMalformedTokens pins the three
// validator-failure paths of authed(): unparseable Bearer, token expired
// beyond the 60 s leeway, and a syntactically valid token whose subject is
// not numeric (parseAccess rejects it). None may reach SQL.
func TestAsxAuthGuardRejectsGarbageExpiredAndMalformedTokens(t *testing.T) {
	s, _ := newTestServer(t)
	app := newTestApp(t, s)

	res := doReq(t, app, "GET", "/api/auth/me", "",
		map[string]string{"Authorization": "Bearer not-a-jwt"})
	requireErrEnvelope(t, res, CodeInvalidToken)

	expired := AsxSign(t, testAccessSec, accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "3",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-2 * time.Minute)),
		},
		Role: "FASILITATOR", School: 1,
	})
	res = doReq(t, app, "GET", "/api/auth/me", "", bearer(expired))
	requireErrEnvelope(t, res, CodeInvalidToken) // expired beyond the 60 s leeway

	badSubject := AsxSign(t, testAccessSec, accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "not-a-number",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		Role: "FASILITATOR", School: 1,
	})
	res = doReq(t, app, "GET", "/api/auth/me", "", bearer(badSubject))
	requireErrEnvelope(t, res, CodeInvalidToken) // non-numeric subject rejected in parseAccess
}

// TestAsxRefreshGuardRejectsMissingGarbageAndMalformedCookies covers
// refreshAuth's ErrorHandler (missing cookie), validator failure (garbage
// cookie) and parseRefresh's non-numeric-subject rejection. No SQL.
func TestAsxRefreshGuardRejectsMissingGarbageAndMalformedCookies(t *testing.T) {
	s, _ := newTestServer(t)
	app := newTestApp(t, s)

	res := jsonReq(t, app, "POST", "/api/auth/refresh", nil, nil)
	requireErrEnvelope(t, res, CodeInvalidToken) // missing cookie → refreshAuth ErrorHandler

	res = jsonReq(t, app, "POST", "/api/auth/refresh", nil,
		map[string]string{"Cookie": refreshCookie + "=garbage"})
	requireErrEnvelope(t, res, CodeInvalidToken) // garbage cookie → validator failure

	badSubject := AsxSign(t, testRefreshSec, refreshClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "abc",
			ID:        "jti-badsub",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		Role: "FASILITATOR", School: 1,
	})
	res = jsonReq(t, app, "POST", "/api/auth/refresh", nil,
		map[string]string{"Cookie": refreshCookie + "=" + badSubject})
	requireErrEnvelope(t, res, CodeInvalidToken)
}

// TestAsxRefreshUnknownJtiAndLockFailure: an unknown jti is the
// single-use loser path (INVALID_TOKEN); a lock-query failure is a DB fault
// (DB_UNAVAILABLE). Both roll the Tx back.
func TestAsxRefreshUnknownJtiAndLockFailure(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	ghost := mintRefresh(t, s, fasUserRow(), "jti-ghost", time.Now().Add(time.Hour))
	hGhost := map[string]string{"Cookie": refreshCookie + "=" + ghost}
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT revoked_at, impersonated_by, impersonation_root FROM refresh_tokens WHERE jti = ? FOR UPDATE")).
		WithArgs("jti-ghost").
		WillReturnRows(sqlmock.NewRows([]string{"revoked_at", "impersonated_by", "impersonation_root"})) // no row
	mock.ExpectRollback()
	res := jsonReq(t, app, "POST", "/api/auth/refresh", nil, hGhost)
	requireErrEnvelope(t, res, CodeInvalidToken)

	down := mintRefresh(t, s, fasUserRow(), "jti-lockdown", time.Now().Add(time.Hour))
	hDown := map[string]string{"Cookie": refreshCookie + "=" + down}
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT revoked_at, impersonated_by, impersonation_root FROM refresh_tokens WHERE jti = ? FOR UPDATE")).
		WithArgs("jti-lockdown").
		WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	res = jsonReq(t, app, "POST", "/api/auth/refresh", nil, hDown)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxRefreshUserLookupFailures: deleted user → INVALID_TOKEN, DB fault
// during the user scan → DB_UNAVAILABLE, pending/deactivated user →
// ACCOUNT_INACTIVE — all inside the rotation Tx.
func TestAsxRefreshUserLookupFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	lock := func(jti string) {
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT revoked_at, impersonated_by, impersonation_root FROM refresh_tokens WHERE jti = ? FOR UPDATE")).
			WithArgs(jti).
			WillReturnRows(sqlmock.NewRows([]string{"revoked_at", "impersonated_by", "impersonation_root"}).AddRow(nil, nil, nil))
	}
	cookie := func(jti string) map[string]string {
		return map[string]string{
			"Cookie": refreshCookie + "=" + mintRefresh(t, s, fasUserRow(), jti, time.Now().Add(time.Hour)),
		}
	}

	// User row vanished between the whitelist check and the load.
	lock("jti-gone")
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	res := jsonReq(t, app, "POST", "/api/auth/refresh", nil, cookie("jti-gone"))
	requireErrEnvelope(t, res, CodeInvalidToken)

	// DB fault while loading the user.
	lock("jti-dbdown")
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnError(errors.New("user table down"))
	mock.ExpectRollback()
	res = jsonReq(t, app, "POST", "/api/auth/refresh", nil, cookie("jti-dbdown"))
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Pending/deactivated account may not rotate.
	lock("jti-pending")
	pending := sqlUserRow(3, "Fasil", "fas@school.id", "+628111111111",
		testPasswordHash, "FASILITATOR", "PENDING", false, int64(1), true)
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(pending)
	mock.ExpectRollback()
	res = jsonReq(t, app, "POST", "/api/auth/refresh", nil, cookie("jti-pending"))
	requireErrEnvelope(t, res, CodeAccountInactive)
}

// TestAsxRefreshTransactionFailures: Begin failure, revoke-statement failure,
// new-row insert failure and commit failure must all answer DB_UNAVAILABLE
// (the OLD jti stays valid on commit failure).
func TestAsxRefreshTransactionFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	cookie := func(jti string) map[string]string {
		return map[string]string{
			"Cookie": refreshCookie + "=" + mintRefresh(t, s, fasUserRow(), jti, time.Now().Add(time.Hour)),
		}
	}
	rotatePrefix := func(jti string) {
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT revoked_at, impersonated_by, impersonation_root FROM refresh_tokens WHERE jti = ? FOR UPDATE")).
			WithArgs(jti).
			WillReturnRows(sqlmock.NewRows([]string{"revoked_at", "impersonated_by", "impersonation_root"}).AddRow(nil, nil, nil))
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
	}

	// Begin itself fails.
	mock.ExpectBegin().WillReturnError(errors.New("begin refused"))
	res := jsonReq(t, app, "POST", "/api/auth/refresh", nil, cookie("jti-begin"))
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Revoking the old jti fails.
	rotatePrefix("jti-revokerr")
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE jti = ?")).
		WithArgs("jti-revokerr").WillReturnError(errors.New("revoke failed"))
	mock.ExpectRollback()
	res = jsonReq(t, app, "POST", "/api/auth/refresh", nil, cookie("jti-revokerr"))
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Inserting the replacement row fails.
	rotatePrefix("jti-inserterr")
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE jti = ?")).
		WithArgs("jti-inserterr").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).WillReturnError(errors.New("insert failed"))
	mock.ExpectRollback()
	res = jsonReq(t, app, "POST", "/api/auth/refresh", nil, cookie("jti-inserterr"))
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Commit fails — rollback keeps the old jti usable.
	rotatePrefix("jti-commiterr")
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE jti = ?")).
		WithArgs("jti-commiterr").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit refused"))
	res = jsonReq(t, app, "POST", "/api/auth/refresh", nil, cookie("jti-commiterr"))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxRefreshHandlerRejectsUnsetClaims drives handleRefresh without the
// refreshAuth middleware: unset claims and a non-numeric subject both answer
// INVALID_TOKEN before any SQL.
func TestAsxRefreshHandlerRejectsUnsetClaims(t *testing.T) {
	s, _ := newTestServer(t)
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Post("/asx-refresh-noclaims", s.handleRefresh)
	app.Post("/asx-refresh-badsub", func(c fiber.Ctx) error {
		c.Locals(kvRefreshClaims, &refreshClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "abc"},
		})
		return s.handleRefresh(c)
	})

	res := jsonReq(t, app, "POST", "/asx-refresh-noclaims", nil, nil)
	requireErrEnvelope(t, res, CodeInvalidToken)

	res = jsonReq(t, app, "POST", "/asx-refresh-badsub", nil, nil)
	requireErrEnvelope(t, res, CodeInvalidToken)
}

// ---------------------------------------------------------------------------
// authorize: missing/malformed claims + status/school guards
// ---------------------------------------------------------------------------

// TestAsxAuthorizeRejectsMissingAndMalformedClaims: authorize is the stage
// behind authed(); with no kvClaims or a non-numeric subject it must answer
// INVALID_TOKEN without touching SQL.
func TestAsxAuthorizeRejectsMissingAndMalformedClaims(t *testing.T) {
	s, _ := newTestServer(t)
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Get("/asx-authorize-noclaims", func(c fiber.Ctx) error {
		return s.authorize(c, nil)
	})
	app.Get("/asx-authorize-badsub", func(c fiber.Ctx) error {
		c.Locals(kvClaims, &accessClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "not-a-number"},
		})
		return s.authorize(c, nil)
	})

	res := doReq(t, app, "GET", "/asx-authorize-noclaims", "", nil)
	requireErrEnvelope(t, res, CodeInvalidToken)

	res = doReq(t, app, "GET", "/asx-authorize-badsub", "", nil)
	requireErrEnvelope(t, res, CodeInvalidToken)
}

// TestAsxAuthorizeStatusAndSchoolGuards walks the guard ladder behind
// authed(): user-load DB fault, unapproved account, malformed SA school
// header, SA school-lookup fault and an inactive facilitator school.
func TestAsxAuthorizeStatusAndSchoolGuards(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// User-load DB fault → DB_UNAVAILABLE.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnError(errors.New("users down"))
	res := doReq(t, app, "GET", "/api/auth/me", "", AsxFASH(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Pending account → ACCOUNT_INACTIVE (before any role/school check).
	pending := sqlUserRow(3, "Fasil", "fas@school.id", "+628111111111",
		testPasswordHash, "FASILITATOR", "PENDING", false, int64(1), true)
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(pending)
	res = doReq(t, app, "GET", "/api/auth/me", "", AsxFASH(t, s))
	requireErrEnvelope(t, res, CodeAccountInactive)

	// SA with a malformed X-School-ID → SCHOOL_MISMATCH (no school query).
	hSA := AsxSAH(t, s)
	hSA["X-School-ID"] = "not-a-school"
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	res = doReq(t, app, "GET", "/api/auth/me", "", hSA)
	requireErrEnvelope(t, res, CodeSchoolMismatch)

	// SA school-lookup fault → DB_UNAVAILABLE.
	hSA2 := AsxSAH(t, s)
	hSA2["X-School-ID"] = "5"
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(5)).
		WillReturnError(errors.New("schools down"))
	res = doReq(t, app, "GET", "/api/auth/me", "", hSA2)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Facilitator whose school is disabled → SCHOOL_INACTIVE.
	inactiveSchool := sqlUserRow(3, "Fasil", "fas@school.id", "+628111111111",
		testPasswordHash, "FASILITATOR", "APPROVED", true, int64(1), false)
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(inactiveSchool)
	res = doReq(t, app, "GET", "/api/auth/me", "", AsxFASH(t, s))
	requireErrEnvelope(t, res, CodeSchoolInactive)
}

// TestAsxRoleAllowedMatrix pins the documented role ladder, including the
// ADMIN-passes-facilitator-level superset rule.
func TestAsxRoleAllowedMatrix(t *testing.T) {
	cases := []struct {
		roles []string
		role  string
		want  bool
	}{
		{nil, "FASILITATOR", true},                // empty roles = any authenticated role
		{[]string{}, "ADMIN", true},               // zero-length slice, same rule
		{nil, "SA", true},                         // SA supersedes
		{[]string{"ADMIN"}, "ADMIN", true},        // exact match
		{[]string{"FASILITATOR"}, "ADMIN", false}, // ADMIN no longer passes facilitator-level routes (assessments.go)
		{[]string{"ADMIN"}, "FASILITATOR", false}, // FAS does not pass ADMIN-only routes
		{[]string{"SA"}, "ADMIN", false},          // SA-only routes stay SA-only
		{[]string{"SA"}, "SA", true},              // exact match
		{[]string{"FASILITATOR"}, "FASILITATOR", true},
		{[]string{"SA", "ADMIN"}, "FASILITATOR", false},
	}
	for _, tc := range cases {
		require.Equalf(t, tc.want, roleAllowed(tc.roles, tc.role),
			"roleAllowed(%v, %q)", tc.roles, tc.role)
	}
}

// ---------------------------------------------------------------------------
// handleRegister
// ---------------------------------------------------------------------------

// TestAsxRegisterValidationBeforeSQL: every validation gate answers its own
// code with ZERO sqlmock expectations — a stray query would surface as
// DB_UNAVAILABLE and fail these assertions.
func TestAsxRegisterValidationBeforeSQL(t *testing.T) {
	s, _ := newTestServer(t)
	app := newTestApp(t, s) // register limiter: 5/min/IP — exactly 5 requests

	base := map[string]any{
		"name": "Anak", "email": "ok@school.id", "wa_number": "08123456789",
		"password": testPassword, "role": "FASILITATOR", "school_id": 1,
	}
	mutate := func(kv map[string]any) map[string]any {
		out := make(map[string]any, len(base)+len(kv))
		for k, v := range base {
			out[k] = v
		}
		for k, v := range kv {
			out[k] = v
		}
		return out
	}

	cases := []struct {
		name string
		body map[string]any
		code string
	}{
		{"bad email", mutate(map[string]any{"email": "not-an-email"}), CodeValidation},
		{"sa self-register", mutate(map[string]any{"role": "SA"}), CodeForbiddenRole},
		{"unknown role", mutate(map[string]any{"role": "STUDENT"}), CodeValidation},
		{"bad wa number", mutate(map[string]any{"wa_number": "999"}), CodeValidation},
		{"school missing", mutate(map[string]any{"school_id": 0}), CodeValidation},
	}
	for _, tc := range cases {
		res := jsonReq(t, app, "POST", "/api/auth/register", tc.body, nil)
		requireErrEnvelope(t, res, tc.code) // case name: tc.name
	}
}

// TestAsxRegisterSchoolGuardFailures: a school-lookup DB fault is
// DB_UNAVAILABLE; an unknown school (ErrNoRows → inactive) is SCHOOL_INACTIVE.
func TestAsxRegisterSchoolGuardFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(1)).
		WillReturnError(errors.New("schools down"))
	res := jsonReq(t, app, "POST", "/api/auth/register", AsxRegisterBody("dberr@school.id"), nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// schoolActive maps sql.ErrNoRows to (false, nil) → deleted school acts inactive.
	mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(7)).WillReturnError(sql.ErrNoRows)
	body := AsxRegisterBody("ghost-school@school.id")
	body["school_id"] = 7
	res = jsonReq(t, app, "POST", "/api/auth/register", body, nil)
	requireErrEnvelope(t, res, CodeSchoolInactive)
}

// TestAsxRegisterClaimAndWriteFailures: email-probe DB fault releases the
// idempotency claim; a duplicate-key INSERT race records EMAIL_TAKEN; a
// pending-count fault still creates the user (log-only).
func TestAsxRegisterClaimAndWriteFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// Email probe fails → DB_UNAVAILABLE + claim released.
	h := map[string]string{"X-Idempotency-Key": "asx-probe-1"}
	expectSchoolActive(mock, 1)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-probe-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-probe-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
		WithArgs("probe@school.id").
		WillReturnError(errors.New("probe query down"))
	mock.ExpectExec(q("DELETE FROM idempotency_keys WHERE school_id = ? AND route = ? AND idem_key = ?")).
		WithArgs(int64(1), idemRegisterRoute, "asx-probe-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	res := jsonReq(t, app, "POST", "/api/auth/register", AsxRegisterBody("probe@school.id"), h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Unique-key race on INSERT users → EMAIL_TAKEN, envelope recorded.
	h = map[string]string{"X-Idempotency-Key": "asx-race-1"}
	expectSchoolActive(mock, 1)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-race-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-race-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
		WithArgs("race@school.id").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	mock.ExpectExec(q("INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)")).
		WithArgs(int64(1), "Anak", "race@school.id", "+6281234567890", sqlmock.AnyArg(), "FASILITATOR").
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry 'race@school.id'"})
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), idemRegisterRoute, "asx-race-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	res = jsonReq(t, app, "POST", "/api/auth/register", AsxRegisterBody("race@school.id"), h)
	requireErrEnvelope(t, res, CodeEmailTaken)

	// Pending-count fault logs but keeps the success envelope.
	h = map[string]string{"X-Idempotency-Key": "asx-pend-1"}
	expectSchoolActive(mock, 1)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-pend-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-pend-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
		WithArgs("pend@school.id").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	mock.ExpectExec(q("INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)")).
		WithArgs(int64(1), "Anak", "pend@school.id", "+6281234567890", sqlmock.AnyArg(), "FASILITATOR").
		WillReturnResult(sqlmock.NewResult(42, 1))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM users WHERE school_id = ? AND decision = 'PENDING'")).
		WithArgs(int64(1)).
		WillReturnError(errors.New("count down"))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), idemRegisterRoute, "asx-pend-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	res = jsonReq(t, app, "POST", "/api/auth/register", AsxRegisterBody("pend@school.id"), h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(42), data["id"])
	require.Equal(t, "pending", data["status"])
}

// ---------------------------------------------------------------------------
// handleLogin / handleForgot
// ---------------------------------------------------------------------------

// TestAsxLoginGuardFailures: empty credentials short-circuit before SQL with
// INVALID_CREDENTIALS; a user-lookup fault is DB_UNAVAILABLE; a whitelist
// insert fault after a successful password check is DB_UNAVAILABLE.
func TestAsxLoginGuardFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	res := jsonReq(t, app, "POST", "/api/auth/login",
		map[string]any{"email": "", "password": ""}, nil)
	requireErrEnvelope(t, res, CodeInvalidCredentials)

	mock.ExpectQuery(sqlUserByEmail).WithArgs("dberr@school.id").
		WillReturnError(errors.New("users down"))
	res = jsonReq(t, app, "POST", "/api/auth/login",
		map[string]any{"email": "dberr@school.id", "password": testPassword}, nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	mock.ExpectQuery(sqlUserByEmail).WithArgs("fas@school.id").WillReturnRows(sqlFASRow())
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).WillReturnError(errors.New("whitelist full"))
	res = jsonReq(t, app, "POST", "/api/auth/login",
		map[string]any{"email": "fas@school.id", "password": testPassword}, nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxForgotValidationAndLookupFailures: malformed JSON is VALIDATION with
// no SQL; a user-lookup fault is DB_UNAVAILABLE (the no-enumeration
// contract only applies to sql.ErrNoRows).
func TestAsxForgotValidationAndLookupFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	res := doReq(t, app, "POST", "/api/auth/forgot", "not-json", nil)
	requireErrEnvelope(t, res, CodeValidation)

	mock.ExpectQuery(sqlUserByEmail).WithArgs("dberr@school.id").
		WillReturnError(errors.New("users down"))
	res = jsonReq(t, app, "POST", "/api/auth/forgot",
		map[string]any{"email": "dberr@school.id"}, nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxForgotWriteFailures: sweep DELETE fault and token INSERT fault are
// DB_UNAVAILABLE; a mail_failed stamp fault still answers {sent:true}
// (the mail is unconfigured in tests, so the stamp always runs).
func TestAsxForgotWriteFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// Sweep failure.
	mock.ExpectQuery(sqlUserByEmail).WithArgs("sweep@school.id").WillReturnRows(sqlFASRow())
	mock.ExpectExec(q("DELETE FROM reset_tokens WHERE user_id = ? AND expires_at < NOW()")).
		WithArgs(int64(3)).WillReturnError(errors.New("sweep failed"))
	res := jsonReq(t, app, "POST", "/api/auth/forgot",
		map[string]any{"email": "sweep@school.id"}, nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Insert failure.
	mock.ExpectQuery(sqlUserByEmail).WithArgs("insert@school.id").WillReturnRows(sqlFASRow())
	mock.ExpectExec(q("DELETE FROM reset_tokens WHERE user_id = ? AND expires_at < NOW()")).
		WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO reset_tokens")).WillReturnError(errors.New("insert failed"))
	res = jsonReq(t, app, "POST", "/api/auth/forgot",
		map[string]any{"email": "insert@school.id"}, nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Stamp failure: mail fails (SMTP unconfigured), the UPDATE errors, the
	// answer stays {sent:true}.
	require.Empty(t, s.cfg.SMTPHost, "test config must leave SMTP unconfigured")
	mock.ExpectQuery(sqlUserByEmail).WithArgs("stamp@school.id").WillReturnRows(sqlFASRow())
	mock.ExpectExec(q("DELETE FROM reset_tokens WHERE user_id = ? AND expires_at < NOW()")).
		WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO reset_tokens")).
		WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectExec(q("UPDATE reset_tokens SET mail_failed = TRUE WHERE id = ?")).
		WithArgs(int64(7)).WillReturnError(errors.New("stamp failed"))
	res = jsonReq(t, app, "POST", "/api/auth/forgot",
		map[string]any{"email": "stamp@school.id"}, nil)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["sent"])
}

// ---------------------------------------------------------------------------
// handleReset
// ---------------------------------------------------------------------------

// TestAsxResetValidationAndLookupFailures: malformed JSON is VALIDATION with
// no SQL; an unknown address is VALIDATION (no oracle); a lookup fault is
// DB_UNAVAILABLE.
func TestAsxResetValidationAndLookupFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	res := doReq(t, app, "POST", "/api/auth/reset", "not-json", nil)
	requireErrEnvelope(t, res, CodeValidation)

	mock.ExpectQuery(sqlUserByEmail).WithArgs("ghost@school.id").WillReturnError(sql.ErrNoRows)
	res = jsonReq(t, app, "POST", "/api/auth/reset", AsxResetBody("ghost@school.id"), nil)
	requireErrEnvelope(t, res, CodeValidation)

	mock.ExpectQuery(sqlUserByEmail).WithArgs("dberr@school.id").
		WillReturnError(errors.New("users down"))
	res = jsonReq(t, app, "POST", "/api/auth/reset", AsxResetBody("dberr@school.id"), nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxResetCandidateReadFailures: token-query fault, row-scan fault and
// iteration fault all answer DB_UNAVAILABLE before any write.
func TestAsxResetCandidateReadFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// Query fault.
	mock.ExpectQuery(sqlUserByEmail).WithArgs("qerr@school.id").WillReturnRows(sqlFASRow())
	mock.ExpectQuery(q("FROM reset_tokens WHERE user_id = ? AND source = 'SELF' ORDER BY id DESC LIMIT 20")).
		WithArgs(int64(3)).WillReturnError(errors.New("reset_tokens down"))
	res := jsonReq(t, app, "POST", "/api/auth/reset", AsxResetBody("qerr@school.id"), nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Row scan fault (NULL id cannot scan into int64).
	mock.ExpectQuery(sqlUserByEmail).WithArgs("scanerr@school.id").WillReturnRows(sqlFASRow())
	mock.ExpectQuery(q("FROM reset_tokens WHERE user_id = ? AND source = 'SELF' ORDER BY id DESC LIMIT 20")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "code_hash", "expires_at", "used_at", "revoked_at"}).
			AddRow(nil, sha256Hex("aabbccdd"), time.Now().Add(time.Hour), nil, nil))
	res = jsonReq(t, app, "POST", "/api/auth/reset", AsxResetBody("scanerr@school.id"), nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Iteration fault.
	mock.ExpectQuery(sqlUserByEmail).WithArgs("rowerr@school.id").WillReturnRows(sqlFASRow())
	mock.ExpectQuery(q("FROM reset_tokens WHERE user_id = ? AND source = 'SELF' ORDER BY id DESC LIMIT 20")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "code_hash", "expires_at", "used_at", "revoked_at"}).
			AddRow(int64(9), sha256Hex("aabbccdd"), time.Now().Add(time.Hour), nil, nil).
			RowError(0, errors.New("row boom")))
	res = jsonReq(t, app, "POST", "/api/auth/reset", AsxResetBody("rowerr@school.id"), nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxResetTransactionFailures: Begin fault, stamp fault, lost single-use
// race (0 rows), refresh-revoke fault, password-update fault and commit fault.
func TestAsxResetTransactionFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	prefix := func(email string) {
		mock.ExpectQuery(sqlUserByEmail).WithArgs(email).WillReturnRows(sqlFASRow())
		mock.ExpectQuery(q("FROM reset_tokens WHERE user_id = ? AND source = 'SELF' ORDER BY id DESC LIMIT 20")).
			WithArgs(int64(3)).
			WillReturnRows(resetRows(9, sha256Hex("aabbccdd"), time.Now().Add(time.Hour), nil, nil))
	}

	// Begin fault.
	prefix("beginerr@school.id")
	mock.ExpectBegin().WillReturnError(errors.New("begin refused"))
	res := jsonReq(t, app, "POST", "/api/auth/reset", AsxResetBody("beginerr@school.id"), nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Stamp UPDATE fault.
	prefix("stamperr@school.id")
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE reset_tokens SET used_at = NOW(), code_plain = NULL")).
		WithArgs(int64(9)).WillReturnError(errors.New("stamp failed"))
	mock.ExpectRollback()
	res = jsonReq(t, app, "POST", "/api/auth/reset", AsxResetBody("stamperr@school.id"), nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Lost the single-use race: 0 rows stamped → RESET_USED.
	prefix("race@school.id")
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE reset_tokens SET used_at = NOW(), code_plain = NULL")).
		WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	res = jsonReq(t, app, "POST", "/api/auth/reset", AsxResetBody("race@school.id"), nil)
	requireErrEnvelope(t, res, CodeResetUsed)

	// Refresh-revoke UPDATE fault.
	prefix("revokeerr@school.id")
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE reset_tokens SET used_at = NOW(), code_plain = NULL")).
		WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = ?")).
		WithArgs(int64(3)).WillReturnError(errors.New("revoke failed"))
	mock.ExpectRollback()
	res = jsonReq(t, app, "POST", "/api/auth/reset", AsxResetBody("revokeerr@school.id"), nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Password UPDATE fault.
	prefix("pwderr@school.id")
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE reset_tokens SET used_at = NOW(), code_plain = NULL")).
		WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = ?")).
		WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(q("UPDATE users SET password_hash = ? WHERE id = ?")).
		WithArgs(sqlmock.AnyArg(), int64(3)).WillReturnError(errors.New("password write failed"))
	mock.ExpectRollback()
	res = jsonReq(t, app, "POST", "/api/auth/reset", AsxResetBody("pwderr@school.id"), nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Commit fault.
	prefix("commiterr@school.id")
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE reset_tokens SET used_at = NOW(), code_plain = NULL")).
		WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = ?")).
		WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(q("UPDATE users SET password_hash = ? WHERE id = ?")).
		WithArgs(sqlmock.AnyArg(), int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit refused"))
	res = jsonReq(t, app, "POST", "/api/auth/reset", AsxResetBody("commiterr@school.id"), nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// handleChangePassword / handleProfile / handleSwitchSchool
// ---------------------------------------------------------------------------

// TestAsxChangePasswordFailureBranches: bad JSON, vanished hash row, hash
// lookup fault, Begin fault, both UPDATE faults and the commit fault.
func TestAsxChangePasswordFailureBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := AsxAdminH(t, s)

	// Malformed JSON → VALIDATION before any SQL beyond authed's user load.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	res := doReq(t, app, "POST", "/api/auth/change-password", "not-json", h)
	requireErrEnvelope(t, res, CodeValidation)

	// Hash row vanished → ACCOUNT_INACTIVE.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT password_hash FROM users WHERE id = ?")).
		WithArgs(int64(2)).WillReturnError(sql.ErrNoRows)
	res = jsonReq(t, app, "POST", "/api/auth/change-password",
		map[string]any{"current_password": testPassword, "new_password": "BrandNew1"}, h)
	requireErrEnvelope(t, res, CodeAccountInactive)

	// Hash lookup fault → DB_UNAVAILABLE.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT password_hash FROM users WHERE id = ?")).
		WithArgs(int64(2)).WillReturnError(errors.New("users down"))
	res = jsonReq(t, app, "POST", "/api/auth/change-password",
		map[string]any{"current_password": testPassword, "new_password": "BrandNew1"}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Begin fault.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT password_hash FROM users WHERE id = ?")).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"password_hash"}).AddRow(testPasswordHash))
	mock.ExpectBegin().WillReturnError(errors.New("begin refused"))
	res = jsonReq(t, app, "POST", "/api/auth/change-password",
		map[string]any{"current_password": testPassword, "new_password": "BrandNew1"}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Password UPDATE fault.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT password_hash FROM users WHERE id = ?")).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"password_hash"}).AddRow(testPasswordHash))
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE users SET password_hash = ? WHERE id = ?")).
		WithArgs(sqlmock.AnyArg(), int64(2)).WillReturnError(errors.New("password write failed"))
	mock.ExpectRollback()
	res = jsonReq(t, app, "POST", "/api/auth/change-password",
		map[string]any{"current_password": testPassword, "new_password": "BrandNew1"}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Session-revoke UPDATE fault.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT password_hash FROM users WHERE id = ?")).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"password_hash"}).AddRow(testPasswordHash))
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE users SET password_hash = ? WHERE id = ?")).
		WithArgs(sqlmock.AnyArg(), int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = ?")).
		WithArgs(int64(2)).WillReturnError(errors.New("revoke failed"))
	mock.ExpectRollback()
	res = jsonReq(t, app, "POST", "/api/auth/change-password",
		map[string]any{"current_password": testPassword, "new_password": "BrandNew1"}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Commit fault.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT password_hash FROM users WHERE id = ?")).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"password_hash"}).AddRow(testPasswordHash))
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE users SET password_hash = ? WHERE id = ?")).
		WithArgs(sqlmock.AnyArg(), int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = ?")).
		WithArgs(int64(2)).WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectCommit().WillReturnError(errors.New("commit refused"))
	res = jsonReq(t, app, "POST", "/api/auth/change-password",
		map[string]any{"current_password": testPassword, "new_password": "BrandNew1"}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxProfileFailureBranches: malformed JSON → VALIDATION; name UPDATE
// fault → DB_UNAVAILABLE.
func TestAsxProfileFailureBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := AsxAdminH(t, s)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	res := doReq(t, app, "PATCH", "/api/auth/profile", "not-json", h)
	requireErrEnvelope(t, res, CodeValidation)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectExec(q("UPDATE users SET name = ? WHERE id = ?")).
		WithArgs("Nama Baru", int64(2)).WillReturnError(errors.New("users down"))
	res = jsonReq(t, app, "PATCH", "/api/auth/profile", map[string]any{"name": "Nama Baru"}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxSwitchSchoolFailureBranches: malformed JSON → VALIDATION; target
// lookup fault → DB_UNAVAILABLE; unknown target (ErrNoRows) → SCHOOL_INACTIVE.
func TestAsxSwitchSchoolFailureBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := AsxSAH(t, s)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	res := doReq(t, app, "POST", "/api/auth/switch-school", "not-json", h)
	requireErrEnvelope(t, res, CodeValidation)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(5)).
		WillReturnError(errors.New("schools down"))
	res = jsonReq(t, app, "POST", "/api/auth/switch-school", map[string]any{"school_id": 5}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(9)).WillReturnError(sql.ErrNoRows)
	res = jsonReq(t, app, "POST", "/api/auth/switch-school", map[string]any{"school_id": 9}, h)
	requireErrEnvelope(t, res, CodeSchoolInactive)
}

// TestAsxAuthSchoolsFailureBranches: picker query fault, row-scan fault and
// iteration fault are all DB_UNAVAILABLE.
func TestAsxAuthSchoolsFailureBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	picker := q("SELECT id, school_name FROM schools WHERE is_active = TRUE ORDER BY school_name")

	mock.ExpectQuery(picker).WillReturnError(errors.New("schools down"))
	res := doReq(t, app, "GET", "/api/auth/schools", "", nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	mock.ExpectQuery(picker).
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_name"}).AddRow(nil, nil))
	res = doReq(t, app, "GET", "/api/auth/schools", "", nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	mock.ExpectQuery(picker).
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_name"}).
			AddRow(1, "Kidversa").
			RowError(0, errors.New("row boom")))
	res = doReq(t, app, "GET", "/api/auth/schools", "", nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxLogoutRevokeFailureStillLogsOut: the revoke UPDATE fault is
// log-only — logout always answers {logged_out:true}.
func TestAsxLogoutRevokeFailureStillLogsOut(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	cookie := mintRefresh(t, s, saUserRow(), "jti-logout-err", time.Now().Add(time.Hour))
	h := AsxSAH(t, s)
	h["Cookie"] = refreshCookie + "=" + cookie

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE jti = ? AND revoked_at IS NULL")).
		WithArgs("jti-logout-err").WillReturnError(errors.New("revoke failed"))
	res := jsonReq(t, app, "POST", "/api/auth/logout", nil, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["logged_out"])
}

// TestAsxMeSchoolLookupFailureIsDBUnavailable: /auth/me reuses the loaded
// user; a school-resolution fault must answer DB_UNAVAILABLE.
func TestAsxMeSchoolLookupFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
	mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(1)).
		WillReturnError(errors.New("schools down"))
	res := doReq(t, app, "GET", "/api/auth/me", "", AsxFASH(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxMeSchoolShapeIncludesCreatedAt: /auth/me emits the full contracts
// School shape incl. created_at (RFC3339 UTC) on the session school (F-B05).
func TestAsxMeSchoolShapeIncludesCreatedAt(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(1)).WillReturnRows(sqlSchoolBandung())
	res := doReq(t, app, "GET", "/api/auth/me", "", AsxAdminH(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	school := data["school"].(map[string]any)
	require.Equal(t, "Kidversa", school["school_name"])
	requireAuthSchoolShape(t, school)
}

// TestAsxSALoginSchoolResolution walks schoolForSession's SA ladder through
// the real login path: raw X-School-ID header, latest-active fallback,
// no-active-school (null school), latest-lookup fault and header-school
// lookup fault — the last two surface as DB_UNAVAILABLE from respondSession.
func TestAsxSALoginSchoolResolution(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	latest := sqlSchoolLatestActive()

	login := func(headers map[string]string) httpRes {
		return jsonReq(t, app, "POST", "/api/auth/login",
			map[string]any{"email": "sa@kidversa.id", "password": testPassword}, headers)
	}
	expectSA := func() {
		mock.ExpectQuery(sqlUserByEmail).WithArgs("sa@kidversa.id").WillReturnRows(sqlSARow())
		mock.ExpectExec(q("INSERT INTO refresh_tokens")).WillReturnResult(sqlmock.NewResult(2, 1))
	}

	// Raw header school wins over the first-login default.
	expectSA()
	mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(5)).WillReturnRows(sqlSchoolBandung())
	res := login(map[string]string{"X-School-ID": "5"})
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "Kidversa", data["school"].(map[string]any)["school_name"])

	// No header → latest active school.
	expectSA()
	mock.ExpectQuery(latest).
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
			AddRow(4, "Surabaya", "Indonesia", nil, true, fixedTS))
	res = login(nil)
	data = requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "Surabaya", data["school"].(map[string]any)["school_name"])
	requireAuthSchoolShape(t, data["school"].(map[string]any))

	// No active school at all → school: null.
	expectSA()
	mock.ExpectQuery(latest).WillReturnError(sql.ErrNoRows)
	res = login(nil)
	data = requireDataEnvelope(t, res).(map[string]any)
	require.Nil(t, data["school"], "SA first login without any school must be school:null")

	// Latest-lookup fault → DB_UNAVAILABLE from respondSession.
	expectSA()
	mock.ExpectQuery(latest).WillReturnError(errors.New("schools down"))
	res = login(nil)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Header school lookup fault → DB_UNAVAILABLE from respondSession.
	expectSA()
	mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(5)).WillReturnError(errors.New("schools down"))
	res = login(map[string]string{"X-School-ID": "5"})
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// Reset dashboard: list validation/read faults, reveal, revoke
// ---------------------------------------------------------------------------

// TestAsxResetListValidationBeforeSQL: invalid page and an unbindable query
// string are VALIDATION written before any list SQL (only authed's user load
// runs).
func TestAsxResetListValidationBeforeSQL(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := AsxAdminH(t, s)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	res := doReq(t, app, "GET", "/api/reset-tokens?page=0", "", h)
	requireErrEnvelope(t, res, CodeValidation)

	// ?x%5B=1 decodes to a key with an unmatched bracket — Bind().Query fails.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	res = doReq(t, app, "GET", "/api/reset-tokens?x%5B=1", "", h)
	requireErrEnvelope(t, res, CodeValidation)
}

// TestAsxResetListDirectionOnlySort: ?dir= without a sort key keeps the route
// default column and applies the requested direction (asserted through the
// real ORDER BY fragment) and clamps a past expiry to seconds_left=0.
func TestAsxResetListDirectionOnlySort(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := AsxAdminH(t, s)

	// dir-only: default column "rt.id" (qualified — resetSelect exposes both
	// rt.id and u.id, so a bare `id` is ambiguous), direction desc.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	AsxResetCount(mock, 1)
	mock.ExpectQuery(q("ORDER BY rt.id desc, rt.id ASC")).
		WithArgs(int64(1), 20, 0).
		WillReturnRows(AsxResetListRows(9, "aabbccdd", time.Now().Add(10*time.Minute), false))
	res := doReq(t, app, "GET", "/api/reset-tokens?dir=desc", "", h)
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2, "paged success must carry data+meta: %s", res.body)
	require.Len(t, m["data"].([]any), 1)

	// A past expiry (injected by the DB) clamps to 0 — never negative.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	AsxResetCount(mock, 1)
	mock.ExpectQuery(q(sqlResetRows)).WithArgs(int64(1), 20, 0).
		WillReturnRows(AsxResetListRows(10, "aabbccdd", time.Now().Add(-time.Hour), false))
	res = doReq(t, app, "GET", "/api/reset-tokens", "", h)
	item := envelopeMap(t, res.body)["data"].([]any)[0].(map[string]any)
	require.Equal(t, float64(0), item["seconds_left"],
		"expired rows must clamp seconds_left to 0: %s", res.body)
}

// TestAsxResetListQFilter: ?q narrows by the owning user's name/email — the
// LIKE predicate threads into BOTH the count and list args; a miss returns an
// empty page and an empty q leaves the args untouched (F-B15, audit C13).
func TestAsxResetListQFilter(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := AsxAdminH(t, s)
	emptyRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{
			"id", "code_plain", "expires_at", "revoked_at", "mail_failed",
			"u_id", "u_name", "u_email",
		})
	}

	// q hit: school scope + both LIKE patterns land in count and list.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q(sqlResetCount)).WithArgs(int64(1), "%fas%", "%fas%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q(sqlResetRows)).WithArgs(int64(1), "%fas%", "%fas%", 20, 0).
		WillReturnRows(resetListRows(9, "aabbccdd", true))
	res := doReq(t, app, "GET", "/api/reset-tokens?q=fas", "", h)
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m["data"].([]any), 1)
	require.Equal(t, float64(1), m["meta"].(map[string]any)["total"])

	// q miss: same predicate, zero rows, meta total 0.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q(sqlResetCount)).WithArgs(int64(1), "%ghost%", "%ghost%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(q(sqlResetRows)).WithArgs(int64(1), "%ghost%", "%ghost%", 20, 0).
		WillReturnRows(emptyRows())
	res = doReq(t, app, "GET", "/api/reset-tokens?q=ghost", "", h)
	require.Equal(t, 200, res.status)
	m = envelopeMap(t, res.body)
	require.Empty(t, m["data"].([]any))
	require.Equal(t, float64(0), m["meta"].(map[string]any)["total"])

	// Empty q: no LIKE predicate, args unchanged.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q(sqlResetCount)).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q(sqlResetRows)).WithArgs(int64(1), 20, 0).
		WillReturnRows(resetListRows(9, "aabbccdd", true))
	res = doReq(t, app, "GET", "/api/reset-tokens?q=", "", h)
	require.Equal(t, 200, res.status)
	require.Len(t, envelopeMap(t, res.body)["data"].([]any), 1)
}

// TestAsxResetListReadFailures: COUNT fault, list-query fault and row-scan
// fault each answer DB_UNAVAILABLE.
func TestAsxResetListReadFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := AsxAdminH(t, s)

	// COUNT fault.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q(sqlResetCount)).WithArgs(int64(1)).
		WillReturnError(errors.New("count down"))
	res := doReq(t, app, "GET", "/api/reset-tokens", "", h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// List query fault.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	AsxResetCount(mock, 1)
	mock.ExpectQuery(q(sqlResetRows)).WithArgs(int64(1), 20, 0).
		WillReturnError(errors.New("list down"))
	res = doReq(t, app, "GET", "/api/reset-tokens", "", h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Row scan fault (NULL id).
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	AsxResetCount(mock, 1)
	mock.ExpectQuery(q(sqlResetRows)).WithArgs(int64(1), 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "code_plain", "expires_at", "revoked_at", "mail_failed",
			"u_id", "u_name", "u_email",
		}).AddRow(nil, "aabbccdd", time.Now().Add(10*time.Minute), nil, false,
			int64(3), "Fasil", "fas@school.id"))
	res = doReq(t, app, "GET", "/api/reset-tokens", "", h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxResetListRevealFailures: iteration fault and audited-reveal UPDATE
// fault each answer DB_UNAVAILABLE.
func TestAsxResetListRevealFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := AsxAdminH(t, s)

	// Iteration fault.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	AsxResetCount(mock, 1)
	mock.ExpectQuery(q(sqlResetRows)).WithArgs(int64(1), 20, 0).
		WillReturnRows(AsxResetListRows(9, "aabbccdd", time.Now().Add(10*time.Minute), false).
			RowError(0, errors.New("row boom")))
	res := doReq(t, app, "GET", "/api/reset-tokens", "", h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Reveal UPDATE fault: the plaintext was read, the audit write failed.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	AsxResetCount(mock, 1)
	mock.ExpectQuery(q(sqlResetRows)).WithArgs(int64(1), 20, 0).
		WillReturnRows(AsxResetListRows(9, "aabbccdd", time.Now().Add(10*time.Minute), false))
	mock.ExpectExec(q(sqlResetRevealUpd)).
		WithArgs(int64(2), int64(9)).WillReturnError(errors.New("audit write failed"))
	res = doReq(t, app, "GET", "/api/reset-tokens?reveal=1", "", h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxResetRevealFailureBranches: id 0 behaves like an unknown id
// (NOT_FOUND, no oracle); unknown row → NOT_FOUND; DB fault →
// DB_UNAVAILABLE; audit UPDATE fault → DB_UNAVAILABLE.
func TestAsxResetRevealFailureBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := AsxAdminH(t, s)

	// id=0 → NOT_FOUND, zero list SQL after the guard.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	res := jsonReq(t, app, "POST", "/api/reset-tokens/0/reveal", nil, h)
	requireErrEnvelope(t, res, CodeNotFound)

	// Unknown row.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT code_plain FROM reset_tokens WHERE id = ?")).
		WithArgs(int64(9), int64(1)).WillReturnError(sql.ErrNoRows)
	res = jsonReq(t, app, "POST", "/api/reset-tokens/9/reveal", nil, h)
	requireErrEnvelope(t, res, CodeNotFound)

	// DB fault on the read.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT code_plain FROM reset_tokens WHERE id = ?")).
		WithArgs(int64(9), int64(1)).WillReturnError(errors.New("reset_tokens down"))
	res = jsonReq(t, app, "POST", "/api/reset-tokens/9/reveal", nil, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// DB fault on the audit UPDATE.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT code_plain FROM reset_tokens WHERE id = ?")).
		WithArgs(int64(9), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"code_plain"}).AddRow("xyz789"))
	mock.ExpectExec(q(sqlResetRevealUpd)).
		WithArgs(int64(2), int64(9), int64(1)).WillReturnError(errors.New("audit write failed"))
	res = jsonReq(t, app, "POST", "/api/reset-tokens/9/reveal", nil, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxResetRevokeFailureBranches: id 0 → NOT_FOUND; DB fault on the
// existence read → DB_UNAVAILABLE; DB fault on the revoke UPDATE →
// DB_UNAVAILABLE.
func TestAsxResetRevokeFailureBranches(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := AsxAdminH(t, s)

	// id=0 → NOT_FOUND.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	res := jsonReq(t, app, "POST", "/api/reset-tokens/0/revoke", nil, h)
	requireErrEnvelope(t, res, CodeNotFound)

	// Read fault.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT id FROM reset_tokens WHERE id = ?")).
		WithArgs(int64(9), int64(1)).WillReturnError(errors.New("reset_tokens down"))
	res = jsonReq(t, app, "POST", "/api/reset-tokens/9/revoke", nil, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Revoke UPDATE fault.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT id FROM reset_tokens WHERE id = ?")).
		WithArgs(int64(9), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectExec(q("UPDATE reset_tokens SET revoked_at = NOW(), code_plain = NULL")).
		WithArgs(int64(9)).WillReturnError(errors.New("revoke write failed"))
	res = jsonReq(t, app, "POST", "/api/reset-tokens/9/revoke", nil, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// Idempotency: claim, replay, release
// ---------------------------------------------------------------------------

// TestAsxIdempotencyClaimFailures: purge fault and non-duplicate claim fault
// answer DB_UNAVAILABLE; a duplicate claim whose row vanished (raced purge)
// answers VALIDATION; a duplicate claim with a DB-faulted read answers
// DB_UNAVAILABLE. None executes the handler body.
func TestAsxIdempotencyClaimFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	claimPrefix := func(key string) {
		expectSchoolActive(mock, 1)
		mock.ExpectExec(q("DELETE FROM idempotency_keys")).
			WithArgs(int64(1), idemRegisterRoute, key).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}

	// Purge fault.
	h := map[string]string{"X-Idempotency-Key": "asx-purge-err"}
	expectSchoolActive(mock, 1)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-purge-err").
		WillReturnError(errors.New("purge failed"))
	res := jsonReq(t, app, "POST", "/api/auth/register", AsxRegisterBody("purge@school.id"), h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Non-duplicate claim fault.
	h = map[string]string{"X-Idempotency-Key": "asx-claim-err"}
	claimPrefix("asx-claim-err")
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-claim-err").
		WillReturnError(errors.New("claim insert failed"))
	res = jsonReq(t, app, "POST", "/api/auth/register", AsxRegisterBody("claim@school.id"), h)
	requireErrEnvelope(t, res, CodeDBUnavailable)

	// Duplicate claim, replay row raced away → VALIDATION (retry executes fresh).
	h = map[string]string{"X-Idempotency-Key": "asx-raced"}
	claimPrefix("asx-raced")
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-raced").
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-raced").
		WillReturnError(sql.ErrNoRows)
	res = jsonReq(t, app, "POST", "/api/auth/register", AsxRegisterBody("raced@school.id"), h)
	requireErrEnvelope(t, res, CodeValidation)

	// Duplicate claim, replay read faulted → DB_UNAVAILABLE.
	h = map[string]string{"X-Idempotency-Key": "asx-replay-err"}
	claimPrefix("asx-replay-err")
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-replay-err").
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-replay-err").
		WillReturnError(errors.New("replay read failed"))
	res = jsonReq(t, app, "POST", "/api/auth/register", AsxRegisterBody("replayerr@school.id"), h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxIdempotencyRecordAndReleaseFailures: a failed response-record write
// releases the claim so a retry re-executes (the answer already sent stays a
// success envelope); a failed release after a top-level DB_UNAVAILABLE is
// log-only.
func TestAsxIdempotencyRecordAndReleaseFailures(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// Record fault → release, success envelope already written.
	h := map[string]string{"X-Idempotency-Key": "asx-record-err"}
	expectSchoolActive(mock, 1)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-record-err").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-record-err").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
		WithArgs("recerr@school.id").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	mock.ExpectExec(q("INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)")).
		WithArgs(int64(1), "Anak", "recerr@school.id", "+6281234567890", sqlmock.AnyArg(), "FASILITATOR").
		WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM users WHERE school_id = ? AND decision = 'PENDING'")).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(2))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), idemRegisterRoute, "asx-record-err").
		WillReturnError(errors.New("record write failed"))
	mock.ExpectExec(q("DELETE FROM idempotency_keys WHERE school_id = ? AND route = ? AND idem_key = ?")).
		WithArgs(int64(1), idemRegisterRoute, "asx-record-err").
		WillReturnResult(sqlmock.NewResult(0, 1))
	res := jsonReq(t, app, "POST", "/api/auth/register", AsxRegisterBody("recerr@school.id"), h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(9), data["id"], "the work already succeeded; the sent envelope stands")

	// Release fault after a top-level DB_UNAVAILABLE → log-only, answer stands.
	h = map[string]string{"X-Idempotency-Key": "asx-release-err"}
	expectSchoolActive(mock, 1)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-release-err").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "asx-release-err").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
		WithArgs("relerr@school.id").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	mock.ExpectExec(q("INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)")).
		WithArgs(int64(1), "Anak", "relerr@school.id", "+6281234567890", sqlmock.AnyArg(), "FASILITATOR").
		WillReturnError(errors.New("users down"))
	mock.ExpectExec(q("DELETE FROM idempotency_keys WHERE school_id = ? AND route = ? AND idem_key = ?")).
		WithArgs(int64(1), idemRegisterRoute, "asx-release-err").
		WillReturnError(errors.New("release failed"))
	res = jsonReq(t, app, "POST", "/api/auth/register", AsxRegisterBody("relerr@school.id"), h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestAsxIdempotencyFnErrorReleasesClaim: a non-nil fn error aborts the
// request (500 INTERNAL — the only code allowed on 500) and releases the
// claim so the next attempt re-executes.
func TestAsxIdempotencyFnErrorReleasesClaim(t *testing.T) {
	s, mock := newTestServer(t)
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	const route = "TEST /asx-idem-fnerr"
	app.Post("/asx-idem-fnerr", func(c fiber.Ctx) error {
		c.Locals(kvSchool, int64(1))
		return s.idempotent(c, route, func() error {
			return errors.New("fn exploded mid-flight")
		})
	})

	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), route, "asx-fn-err").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), route, "asx-fn-err").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("DELETE FROM idempotency_keys WHERE school_id = ? AND route = ? AND idem_key = ?")).
		WithArgs(int64(1), route, "asx-fn-err").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/asx-idem-fnerr", nil,
		map[string]string{"X-Idempotency-Key": "asx-fn-err"})
	require.Equal(t, 500, res.status, "an aborted claim surfaces through ErrorHandler: %s", res.body)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 1)
	require.Equal(t, CodeInternal, m["error"].(map[string]any)["code"])
}

// ---------------------------------------------------------------------------
// Middleware / infra: limiter nil-key, ErrorHandler, failStore, idParam
// ---------------------------------------------------------------------------

// TestAsxNewLimiterNilKeyDefaultsToIP: newLimiter(nil key) must key the
// bucket by IP — two rapid in-process hits from one caller trip the limiter
// (200 RATE_LIMITED + Retry-After, never 429).
func TestAsxNewLimiterNilKeyDefaultsToIP(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Post("/asx-limited", newLimiter(1, time.Minute, nil), func(c fiber.Ctx) error {
		return ok(c, fiber.Map{"hit": true})
	})

	res := jsonReq(t, app, "POST", "/asx-limited", nil, nil)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["hit"])

	res = jsonReq(t, app, "POST", "/asx-limited", nil, nil)
	requireErrEnvelope(t, res, CodeRateLimited)
	require.NotEmpty(t, res.header.Get("Retry-After"),
		"rate limiting must advertise Retry-After")
}

// TestAsxErrorHandlerOversizeBodyMapsToValidation pins the app-level 413
// mapping: the oversize-body error (fasthttp ErrBodyTooLarge, normalized to
// fiber's 413 by the server error handler before ErrorHandler runs) is
// answered 200 VALIDATION — never a raw 413 and never a 500.
func TestAsxErrorHandlerOversizeBodyMapsToValidation(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Post("/asx-toobig", func(c fiber.Ctx) error {
		return fiber.ErrRequestEntityTooLarge
	})

	res := jsonReq(t, app, "POST", "/asx-toobig", nil, nil)
	requireErrEnvelope(t, res, CodeValidation)
}

// TestAsxFailStorePassesNonRejectedErrors: rejection maps to VALIDATION, any
// other storeImage-class error passes through to ErrorHandler → 500 INTERNAL.
func TestAsxFailStorePassesNonRejectedErrors(t *testing.T) {
	_, _ = newTestServer(t)
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Get("/asx-failstore", func(c fiber.Ctx) error {
		return failStore(c, errors.New("disk fell off"))
	})

	res := doReq(t, app, "GET", "/asx-failstore", "", nil)
	require.Equal(t, 500, res.status, "non-rejected errors escalate to INTERNAL: %s", res.body)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 1)
	errObj := m["error"].(map[string]any)
	require.Equal(t, CodeInternal, errObj["code"])
	require.Equal(t, MsgInternal, errObj["message"])
}

// TestAsxIdParamMissingRouteParamWritesNotFound: idParam on a route without
// the named parameter (empty raw value) answers NOT_FOUND — malformed ids
// behave exactly like unknown ids.
func TestAsxIdParamMissingRouteParamWritesNotFound(t *testing.T) {
	_, _ = newTestServer(t)
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Get("/asx-idparam", func(c fiber.Ctx) error {
		if _, okID := idParam(c, "id"); !okID {
			return nil // NOT_FOUND already written
		}
		return ok(c, fiber.Map{"reached": true})
	})

	res := doReq(t, app, "GET", "/asx-idparam", "", nil)
	requireErrEnvelope(t, res, CodeNotFound)
}

// ---------------------------------------------------------------------------
// NewServer: asset loading fails loud
// ---------------------------------------------------------------------------

// TestAsxNewServerFailsLoudOnMissingAssets walks NewServer's asset ladder —
// prompts/system.md → prompts/user.md → templates/wa_report.md →
// templates/wa_consent.md — proving construction aborts with the missing
// path in the error instead of booting a broken server.
func TestAsxNewServerFailsLoudOnMissingAssets(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	prev, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(prev), "working directory must be restored")
	})

	writeAsset := func(dir, rel string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("# placeholder\n"), 0o644))
	}

	cases := []struct {
		name    string
		assets  []string
		missing string
	}{
		{"system prompt missing", nil, "prompts/system.md"},
		{"user prompt missing", []string{"prompts/system.md"}, "prompts/user.md"},
		{"report template missing",
			[]string{"prompts/system.md", "prompts/user.md"}, "templates/wa_report.md"},
		{"consent template missing",
			[]string{"prompts/system.md", "prompts/user.md", "templates/wa_report.md"},
			"templates/wa_consent.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, rel := range tc.assets {
				writeAsset(dir, rel)
			}
			require.NoError(t, os.Chdir(dir))
			defer func() { require.NoError(t, os.Chdir(prev)) }()

			s, err := NewServer(db, testConfig(t))
			require.Nil(t, s, "a missing asset must abort construction")
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.missing)
		})
	}
}

// ---------------------------------------------------------------------------
// LoadConfig env parsing
// ---------------------------------------------------------------------------

// TestAsxLoadConfigMissingRequiredKeys: boot fails loud listing every empty
// required key.
func TestAsxLoadConfigMissingRequiredKeys(t *testing.T) {
	for _, k := range requiredEnv {
		t.Setenv(k, "")
	}
	_, err := LoadConfig()
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing required env keys")
	require.Contains(t, err.Error(), "DB_HOST")
	require.Contains(t, err.Error(), "PUBLIC_WEB_BASE_URL")
}

// TestAsxLoadConfigRejectsBadOrigins: CORS_ALLOWED_ORIGINS must be explicit
// scheme://host origins — a wildcard or scheme-less entry is a boot failure.
func TestAsxLoadConfigRejectsBadOrigins(t *testing.T) {
	loadConfigEnv(t)

	t.Setenv("CORS_ALLOWED_ORIGINS", "localhost:8002")
	_, err := LoadConfig()
	require.Error(t, err)
	require.Contains(t, err.Error(), "CORS_ALLOWED_ORIGINS")

	t.Setenv("CORS_ALLOWED_ORIGINS", "http://ok.example, *")
	_, err = LoadConfig()
	require.Error(t, err)
	require.Contains(t, err.Error(), "CORS_ALLOWED_ORIGINS")
}

// TestAsxLoadConfigTTLAndModelEnvValidation covers envMinutes/envDays
// invalid branches plus every LoadConfig error return that wraps them, and
// pins the valid override parsing.
func TestAsxLoadConfigTTLAndModelEnvValidation(t *testing.T) {
	t.Run("access ttl set but empty", func(t *testing.T) {
		loadConfigEnv(t)
		t.Setenv("JWT_ACCESS_TTL_MIN", "")
		_, err := LoadConfig()
		require.Error(t, err)
		require.Contains(t, err.Error(), "JWT_ACCESS_TTL_MIN")
	})
	t.Run("access ttl non-numeric", func(t *testing.T) {
		loadConfigEnv(t)
		t.Setenv("JWT_ACCESS_TTL_MIN", "soon")
		_, err := LoadConfig()
		require.Error(t, err)
		require.Contains(t, err.Error(), "positive integer")
	})
	t.Run("access ttl zero", func(t *testing.T) {
		loadConfigEnv(t)
		t.Setenv("JWT_ACCESS_TTL_MIN", "0")
		_, err := LoadConfig()
		require.Error(t, err)
		require.Contains(t, err.Error(), "JWT_ACCESS_TTL_MIN")
	})
	t.Run("refresh ttl set but empty", func(t *testing.T) {
		loadConfigEnv(t)
		t.Setenv("JWT_REFRESH_TTL_DAYS", "")
		_, err := LoadConfig()
		require.Error(t, err)
		require.Contains(t, err.Error(), "JWT_REFRESH_TTL_DAYS")
	})
	t.Run("refresh ttl negative", func(t *testing.T) {
		loadConfigEnv(t)
		t.Setenv("JWT_REFRESH_TTL_DAYS", "-1")
		_, err := LoadConfig()
		require.Error(t, err)
		require.Contains(t, err.Error(), "positive integer")
	})
	t.Run("reset ttl non-numeric", func(t *testing.T) {
		loadConfigEnv(t)
		t.Setenv("RESET_TOKEN_TTL_MIN", "15minutes")
		_, err := LoadConfig()
		require.Error(t, err)
		require.Contains(t, err.Error(), "RESET_TOKEN_TTL_MIN")
	})
	t.Run("gemini model set but empty", func(t *testing.T) {
		loadConfigEnv(t)
		t.Setenv("GEMINI_MODEL", "")
		_, err := LoadConfig()
		require.Error(t, err)
		require.Contains(t, err.Error(), "GEMINI_MODEL")
	})
	t.Run("valid overrides parse", func(t *testing.T) {
		loadConfigEnv(t)
		t.Setenv("JWT_ACCESS_TTL_MIN", "45")
		t.Setenv("JWT_REFRESH_TTL_DAYS", "3")
		t.Setenv("RESET_TOKEN_TTL_MIN", "30")
		cfg, err := LoadConfig()
		require.NoError(t, err)
		require.Equal(t, 45*time.Minute, cfg.AccessTTL)
		require.Equal(t, 3*24*time.Hour, cfg.RefreshTTL)
		require.Equal(t, 30*time.Minute, cfg.ResetTTL)
	})
}

// ---------------------------------------------------------------------------
// validate.go: parseClock
// ---------------------------------------------------------------------------

// TestAsxParseClockEdges pins the boundary clock parser: blank input is an
// accepted "no clock", both wire layouts parse, out-of-range and malformed
// input is rejected.
func TestAsxParseClockEdges(t *testing.T) {
	got, okFlag := parseClock("")
	require.True(t, okFlag, "blank clock is valid (nullable TIME)")
	require.True(t, got.IsZero(), "blank clock yields the zero time")

	got, okFlag = parseClock("08:05")
	require.True(t, okFlag)
	require.Equal(t, 8, got.Hour())
	require.Equal(t, 5, got.Minute())

	got, okFlag = parseClock("08:05:33")
	require.True(t, okFlag)
	require.Equal(t, 33, got.Second())

	for _, bad := range []string{"8:5", "24:61", "08:05:61", "0805", "08:05:", "noon"} {
		_, okFlag = parseClock(bad)
		require.Falsef(t, okFlag, "parseClock(%q) must be rejected", bad)
	}
}

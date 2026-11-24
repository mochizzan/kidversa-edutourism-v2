package internal

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// sqlSchoolKidversa is the canonical school fixture (new wire shape):
// {id, school_name, country, city, is_active, created_at}.
func sqlSchoolKidversa() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
		AddRow(1, "Kidversa", "Indonesia", nil, true, fixedTS)
}

// sqlSchoolBandung is kept as a compatibility alias so co-owned test files
// that still call the old fixture name keep compiling; it returns the new
// school shape under the canonical Kidversa values.
func sqlSchoolBandung() *sqlmock.Rows { return sqlSchoolKidversa() }

// requireAuthSchoolShape pins the auth-side school object to the contracts
// School shape {id,school_name,country,city,is_active,created_at} (F-B05)
// and its RFC3339 UTC encoding, mirroring schools.go schoolItem.
func requireAuthSchoolShape(t *testing.T, school map[string]any) {
	t.Helper()
	require.ElementsMatch(t, []string{"id", "school_name", "country", "city", "is_active", "created_at"},
		mapKeys(school), "auth school must carry the contracts School shape (F-B05)")
	require.Equal(t, "2026-01-02T03:04:05Z", school["created_at"],
		"created_at must be RFC3339 UTC like schools.go schoolItem")
}

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

func TestAuthLoginSuccessReturnsAccessUserSchool(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByEmail).WithArgs("fas@school.id").WillReturnRows(sqlFASRow())
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(1)).WillReturnRows(sqlSchoolBandung())

	res := jsonReq(t, app, "POST", "/api/auth/login",
		map[string]any{"email": " FAS@School.ID ", "password": testPassword}, nil)
	data := requireDataEnvelope(t, res).(map[string]any)

	access, ok := data["access"].(string)
	require.True(t, ok, "login must return an access token: %s", res.body)
	claims, err := s.parseAccess(access)
	require.NoError(t, err)
	require.Equal(t, "3", claims.Subject)
	require.Equal(t, "FASILITATOR", claims.Role)
	require.Equal(t, int64(1), claims.School)

	user := data["user"].(map[string]any)
	require.Equal(t, "FASILITATOR", user["role"])
	require.Equal(t, "APPROVED", user["decision"])
	require.Equal(t, float64(1), user["school_id"])
	school := data["school"].(map[string]any)
	require.Equal(t, "Kidversa", school["school_name"])
	requireAuthSchoolShape(t, school)

	require.Contains(t, res.header.Get("Set-Cookie"), refreshCookie+"=",
		"login must set the HttpOnly refresh cookie")
}

func TestAuthLoginWrongPasswordIsGenericInvalidCredentials(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	row := sqlUserRow(3, "Fasil", "fas@school.id", "+628111111111",
		otherPasswordHash, "FASILITATOR", "APPROVED", true, int64(1), true)
	mock.ExpectQuery(sqlUserByEmail).WithArgs("fas@school.id").WillReturnRows(row)

	res := jsonReq(t, app, "POST", "/api/auth/login",
		map[string]any{"email": "fas@school.id", "password": testPassword}, nil)
	requireErrEnvelope(t, res, CodeInvalidCredentials)
}

func TestAuthLoginUnknownEmailIsGenericInvalidCredentials(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByEmail).WithArgs("ghost@school.id").
		WillReturnError(sql.ErrNoRows)

	res := jsonReq(t, app, "POST", "/api/auth/login",
		map[string]any{"email": "ghost@school.id", "password": testPassword}, nil)
	requireErrEnvelope(t, res, CodeInvalidCredentials)
}

func TestAuthLoginInactiveUserIsAccountInactive(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	row := sqlUserRow(3, "Fasil", "pending@school.id", "+628111111111",
		testPasswordHash, "FASILITATOR", "PENDING", false, int64(1), true)
	mock.ExpectQuery(sqlUserByEmail).WithArgs("pending@school.id").WillReturnRows(row)

	res := jsonReq(t, app, "POST", "/api/auth/login",
		map[string]any{"email": "pending@school.id", "password": testPassword}, nil)
	requireErrEnvelope(t, res, CodeAccountInactive)
}

func TestAuthLoginInactiveSchoolIsSchoolInactive(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	row := sqlUserRow(3, "Fasil", "fas@school.id", "+628111111111",
		testPasswordHash, "FASILITATOR", "APPROVED", true, int64(1), false)
	mock.ExpectQuery(sqlUserByEmail).WithArgs("fas@school.id").WillReturnRows(row)

	res := jsonReq(t, app, "POST", "/api/auth/login",
		map[string]any{"email": "fas@school.id", "password": testPassword}, nil)
	requireErrEnvelope(t, res, CodeSchoolInactive)
}

// ---------------------------------------------------------------------------
// JWT mint/parse (HS256 pinned, 60 s leeway)
// ---------------------------------------------------------------------------

func TestAuthJWTHS256Pinned(t *testing.T) {
	s, _ := newTestServer(t)

	tok := mintAccess(t, s, fasUserRow())
	claims, err := s.parseAccess(tok)
	require.NoError(t, err)
	require.Equal(t, "3", claims.Subject)
	require.Equal(t, "FASILITATOR", claims.Role)
	require.Equal(t, int64(1), claims.School)

	sign := func(secret string, exp time.Time, withJTI bool) string {
		rc := refreshClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   "3",
				ExpiresAt: jwt.NewNumericDate(exp),
			},
			Role: "FASILITATOR", School: 1,
		}
		if withJTI {
			rc.ID = "jti-1"
		}
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, rc).SignedString([]byte(secret))
		require.NoError(t, err)
		return tok
	}

	// alg=none must never be accepted.
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) +
		"." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"3"}`)) + "."
	_, err = s.parseAccess(unsigned)
	require.Error(t, err, "alg=none token must be rejected")
	_, err = s.parseRefresh(unsigned)
	require.Error(t, err, "alg=none refresh must be rejected")

	// Wrong secret rejected on both token families.
	_, err = s.parseAccess(sign("wrong-secret", time.Now().Add(time.Hour), false))
	require.Error(t, err)
	_, err = s.parseRefresh(sign("wrong-secret", time.Now().Add(time.Hour), true))
	require.Error(t, err)

	// Refresh tokens require a jti (rotation anchor).
	_, err = s.parseRefresh(sign(testRefreshSec, time.Now().Add(time.Hour), false))
	require.Error(t, err)
	rc, err := s.parseRefresh(sign(testRefreshSec, time.Now().Add(time.Hour), true))
	require.NoError(t, err)
	require.Equal(t, "jti-1", rc.ID)
}

func TestAuthJWTSixtySecondLeeway(t *testing.T) {
	s, _ := newTestServer(t)

	signAccess := func(exp time.Time) string {
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   "3",
				ExpiresAt: jwt.NewNumericDate(exp),
			},
			Role: "FASILITATOR", School: 1,
		}).SignedString([]byte(testAccessSec))
		require.NoError(t, err)
		return tok
	}

	// Expired 30 s ago: inside the 60 s leeway window → valid.
	_, err := s.parseAccess(signAccess(time.Now().Add(-30 * time.Second)))
	require.NoError(t, err)

	// Expired 2 min ago: outside the leeway → rejected.
	_, err = s.parseAccess(signAccess(time.Now().Add(-2 * time.Minute)))
	require.Error(t, err)

	// Missing exp is always rejected (WithExpirationRequired).
	noExp, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "3"},
		Role:             "FASILITATOR",
	}).SignedString([]byte(testAccessSec))
	require.NoError(t, err)
	_, err = s.parseAccess(noExp)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Refresh rotation (single use) + logout
// ---------------------------------------------------------------------------

func TestAuthRefreshRotationIsSingleUse(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	old := mintRefresh(t, s, fasUserRow(), "jti-old", time.Now().Add(7*24*time.Hour))
	h := map[string]string{"Cookie": refreshCookie + "=" + old}

	// First refresh: lock old row → revoke → insert new — ONE Tx.
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT revoked_at, impersonated_by, impersonation_root FROM refresh_tokens WHERE jti = ? FOR UPDATE")).
		WithArgs("jti-old").
		WillReturnRows(sqlmock.NewRows([]string{"revoked_at", "impersonated_by", "impersonation_root"}).AddRow(nil, nil, nil))
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE jti = ?")).
		WithArgs("jti-old").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(1)).WillReturnRows(sqlSchoolBandung())

	res := jsonReq(t, app, "POST", "/api/auth/refresh", nil, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.NotEmpty(t, data["access"])
	requireAuthSchoolShape(t, data["school"].(map[string]any))

	// Second use of the SAME cookie: the row is revoked → INVALID_TOKEN.
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT revoked_at, impersonated_by, impersonation_root FROM refresh_tokens WHERE jti = ? FOR UPDATE")).
		WithArgs("jti-old").
		WillReturnRows(sqlmock.NewRows([]string{"revoked_at", "impersonated_by", "impersonation_root"}).AddRow(time.Now(), nil, nil))
	mock.ExpectRollback()

	res2 := jsonReq(t, app, "POST", "/api/auth/refresh", nil, h)
	requireErrEnvelope(t, res2, CodeInvalidToken)
}

func TestAuthLogoutRevokesJTI(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	jti := "logout-jti-1"
	cookie := mintRefresh(t, s, saUserRow(), jti, time.Now().Add(time.Hour))
	h := bearer(mintAccess(t, s, saUserRow()))
	h["Cookie"] = refreshCookie + "=" + cookie

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE jti = ? AND revoked_at IS NULL")).
		WithArgs(jti).WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/api/auth/logout", nil, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["logged_out"])
}

// ---------------------------------------------------------------------------
// Password reset flow
// ---------------------------------------------------------------------------

func resetRows(id int64, code, expiresAt any, usedAt, revokedAt any) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "code_hash", "expires_at", "used_at", "revoked_at"}).
		AddRow(id, code, expiresAt, usedAt, revokedAt)
}

func resetBody(code string) map[string]any {
	return map[string]any{"email": "fas@school.id", "code": code, "new_password": "BrandNew1"}
}

func TestAuthResetExpiredCode(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByEmail).WithArgs("fas@school.id").WillReturnRows(sqlFASRow())
	mock.ExpectQuery(q("FROM reset_tokens WHERE user_id = ? AND source = 'SELF' ORDER BY id DESC LIMIT 20")).
		WithArgs(int64(3)).
		WillReturnRows(resetRows(9, sha256Hex("aabbccdd"), time.Now().Add(-time.Minute), nil, nil))

	res := jsonReq(t, app, "POST", "/api/auth/reset", resetBody("aabbccdd"), nil)
	requireErrEnvelope(t, res, CodeResetExpired)
}

func TestAuthResetUsedAndRevokedCodes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		usedAt   any
		revokedA any
	}{
		{"used", time.Now().Add(-time.Hour), nil},
		{"revoked", nil, time.Now().Add(-time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)

			mock.ExpectQuery(sqlUserByEmail).WithArgs("fas@school.id").WillReturnRows(sqlFASRow())
			mock.ExpectQuery(q("FROM reset_tokens WHERE user_id = ? AND source = 'SELF' ORDER BY id DESC LIMIT 20")).
				WithArgs(int64(3)).
				WillReturnRows(resetRows(9, sha256Hex("aabbccdd"), time.Now().Add(time.Hour), tc.usedAt, tc.revokedA))

			res := jsonReq(t, app, "POST", "/api/auth/reset", resetBody("aabbccdd"), nil)
			requireErrEnvelope(t, res, CodeResetUsed)
		})
	}
}

func TestAuthResetUnknownCodeIsValidation(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByEmail).WithArgs("fas@school.id").WillReturnRows(sqlFASRow())
	mock.ExpectQuery(q("FROM reset_tokens WHERE user_id = ? AND source = 'SELF' ORDER BY id DESC LIMIT 20")).
		WithArgs(int64(3)).
		WillReturnRows(resetRows(9, sha256Hex("totally-different-code"), time.Now().Add(time.Hour), nil, nil))

	res := jsonReq(t, app, "POST", "/api/auth/reset", resetBody("aabbccdd"), nil)
	requireErrEnvelope(t, res, CodeValidation)
}

func TestAuthResetWeakPasswordRejected(t *testing.T) {
	s, _ := newTestServer(t)
	app := newTestApp(t, s)

	res := jsonReq(t, app, "POST", "/api/auth/reset",
		map[string]any{"email": "fas@school.id", "code": "aabbccdd", "new_password": "weak"}, nil)
	requireErrEnvelope(t, res, CodeValidation)
}

func TestAuthResetSuccessRotatesPasswordAndRevokesSessions(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByEmail).WithArgs("fas@school.id").WillReturnRows(sqlFASRow())
	mock.ExpectQuery(q("FROM reset_tokens WHERE user_id = ? AND source = 'SELF' ORDER BY id DESC LIMIT 20")).
		WithArgs(int64(3)).
		WillReturnRows(resetRows(9, sha256Hex("aabbccdd"), time.Now().Add(time.Hour), nil, nil))
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE reset_tokens SET used_at = NOW(), code_plain = NULL")).
		WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = ?")).
		WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(q("UPDATE users SET password_hash = ? WHERE id = ?")).
		WithArgs(sqlmock.AnyArg(), int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := jsonReq(t, app, "POST", "/api/auth/reset", resetBody("aabbccdd"), nil)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["reset"])
}

// ---------------------------------------------------------------------------
// Forgot: always {sent:true}
// ---------------------------------------------------------------------------

func TestAuthForgotAlwaysSentForUnknownEmail(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByEmail).WithArgs("ghost@school.id").WillReturnError(sql.ErrNoRows)

	res := jsonReq(t, app, "POST", "/api/auth/forgot",
		map[string]any{"email": "ghost@school.id"}, nil)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["sent"])
}

func TestAuthForgotStampsMailFailureButStillSent(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	require.Empty(t, s.cfg.SMTPHost, "test config must leave SMTP unconfigured")

	mock.ExpectQuery(sqlUserByEmail).WithArgs("fas@school.id").WillReturnRows(sqlFASRow())
	mock.ExpectExec(q("DELETE FROM reset_tokens WHERE user_id = ? AND expires_at < NOW()")).
		WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO reset_tokens")).
		WillReturnResult(sqlmock.NewResult(5, 1))
	// SMTP is unconfigured → the mail fails → mail_failed is stamped.
	mock.ExpectExec(q("UPDATE reset_tokens SET mail_failed = TRUE WHERE id = ?")).
		WithArgs(int64(5)).WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/api/auth/forgot",
		map[string]any{"email": "fas@school.id"}, nil)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["sent"], "forgot must answer {sent:true} even when the mail fails")
}

// ---------------------------------------------------------------------------
// Register duplicate + idem replay
// ---------------------------------------------------------------------------

func TestAuthRegisterDuplicateEmailThenIdemReplay(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	body := map[string]any{
		"name": "Dup", "email": "dup@school.id", "wa_number": "081234567890",
		"password": testPassword, "role": "FASILITATOR", "school_id": 1,
	}
	h := map[string]string{"X-Idempotency-Key": "reg-dup-1"}
	const route = "POST /api/auth/register"

	// First request: claim the key, discover the email exists.
	mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
		WithArgs("dup@school.id").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), route, "reg-dup-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res1 := jsonReq(t, app, "POST", "/api/auth/register", body, h)
	requireErrEnvelope(t, res1, CodeEmailTaken)

	// Second request, same key: the stored envelope replays byte-for-byte.
	mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), route, "reg-dup-1").
		WillReturnRows(sqlmock.NewRows([]string{"response"}).AddRow(res1.body))

	res2 := jsonReq(t, app, "POST", "/api/auth/register", body, h)
	require.Equal(t, 200, res2.status)
	require.Equal(t, res1.body, res2.body, "replay must return the original envelope verbatim")
}

// ---------------------------------------------------------------------------
// Profile / change-password / switch-school
// ---------------------------------------------------------------------------

func TestAuthProfileChange(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectExec(q("UPDATE users SET name = ? WHERE id = ?")).
		WithArgs("Nama Baru", int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))

	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	res := jsonReq(t, app, "PATCH", "/api/auth/profile", map[string]any{"name": "Nama Baru"}, h)
	user := requireDataEnvelope(t, res).(map[string]any)["user"].(map[string]any)
	require.Equal(t, "Nama Baru", user["name"])

	// Blank names are VALIDATION before any write.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	res = jsonReq(t, app, "PATCH", "/api/auth/profile", map[string]any{"name": "  "}, h)
	requireErrEnvelope(t, res, CodeValidation)
}

func TestAuthChangePassword(t *testing.T) {
	adminH := func(s *Server) map[string]string {
		h := bearer(mintAccess(t, s, adminUserRow()))
		h["X-School-ID"] = "1"
		return h
	}

	t.Run("weak new password", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
		res := jsonReq(t, app, "POST", "/api/auth/change-password",
			map[string]any{"current_password": testPassword, "new_password": "weak"}, adminH(s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("wrong current password", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
		mock.ExpectQuery(q("SELECT password_hash FROM users WHERE id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"password_hash"}).AddRow(testPasswordHash))
		res := jsonReq(t, app, "POST", "/api/auth/change-password",
			map[string]any{"current_password": "OtherPass99", "new_password": "BrandNew1"}, adminH(s))
		requireErrEnvelope(t, res, CodeInvalidCredentials)
	})

	t.Run("success rotates hash and revokes sessions", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
		mock.ExpectQuery(q("SELECT password_hash FROM users WHERE id = ?")).
			WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"password_hash"}).AddRow(testPasswordHash))
		mock.ExpectBegin()
		mock.ExpectExec(q("UPDATE users SET password_hash = ? WHERE id = ?")).
			WithArgs(sqlmock.AnyArg(), int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = ?")).
			WithArgs(int64(2)).WillReturnResult(sqlmock.NewResult(0, 3))
		mock.ExpectCommit()
		res := jsonReq(t, app, "POST", "/api/auth/change-password",
			map[string]any{"current_password": testPassword, "new_password": "BrandNew1"}, adminH(s))
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, true, data["changed"])
	})
}

func TestAuthSwitchSchool(t *testing.T) {
	t.Run("facilitator never switches", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
		h := bearer(mintAccess(t, s, fasUserRow()))
		h["X-School-ID"] = "1" // passes the school guard → handler role check
		res := jsonReq(t, app, "POST", "/api/auth/switch-school", map[string]any{"school_id": 1}, h)
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})

	t.Run("sa validates the target", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
		res := jsonReq(t, app, "POST", "/api/auth/switch-school", map[string]any{"school_id": 0},
			bearer(mintAccess(t, s, saUserRow())))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("sa switches to an active school", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
		mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(5)).WillReturnRows(sqlSchoolBandung())
		res := jsonReq(t, app, "POST", "/api/auth/switch-school", map[string]any{"school_id": 5},
			bearer(mintAccess(t, s, saUserRow())))
		school := requireDataEnvelope(t, res).(map[string]any)["school"].(map[string]any)
		require.Equal(t, "Kidversa", school["school_name"])
		requireAuthSchoolShape(t, school)
	})

	t.Run("sa blocked on an inactive school", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
		mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(9)).WillReturnRows(
			sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
				AddRow(9, "Mati", "Indonesia", nil, false, fixedTS))
		res := jsonReq(t, app, "POST", "/api/auth/switch-school", map[string]any{"school_id": 9},
			bearer(mintAccess(t, s, saUserRow())))
		requireErrEnvelope(t, res, CodeSchoolInactive)
	})
}

// ---------------------------------------------------------------------------
// Reset dashboard (list + audited reveal + revoke)
// ---------------------------------------------------------------------------

const (
	sqlResetCount     = "COUNT(*) FROM reset_tokens rt"
	sqlResetRows      = "SELECT rt.id, rt.code_plain, rt.expires_at"
	sqlResetRevealUpd = "UPDATE reset_tokens SET revealed_at = NOW(), revealed_by = ?"
)

func resetListRows(id int64, plain any, mailFailed bool) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "code_plain", "expires_at", "revoked_at", "mail_failed",
		"u_id", "u_name", "u_email",
	}).AddRow(id, plain, time.Now().Add(10*time.Minute), nil, mailFailed,
		int64(3), "Fasil", "fas@school.id")
}

func TestAuthResetDashboardList(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	// ADMIN scope pins the queries to own-school rows.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q(sqlResetCount)).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q(sqlResetRows)).WithArgs(int64(1), 20, 0).
		WillReturnRows(resetListRows(9, "aabbccdd", true))

	res := doReq(t, app, "GET", "/api/reset-tokens", "", h)
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2)
	item := m["data"].([]any)[0].(map[string]any)
	require.Equal(t, "***dd", item["code_masked"], "mask keeps only the last 2 chars")
	require.Greater(t, item["seconds_left"].(float64), float64(0))
	require.Equal(t, true, item["mail_failed"])
	require.Equal(t, "fas@school.id", item["user"].(map[string]any)["email"])
	require.Equal(t, float64(1), m["meta"].(map[string]any)["total"])

	// ?reveal=1 returns plaintext once and stamps the audit columns.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q(sqlResetCount)).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q(sqlResetRows)).WithArgs(int64(1), 20, 0).
		WillReturnRows(resetListRows(9, "aabbccdd", false))
	mock.ExpectExec(q(sqlResetRevealUpd)).
		WithArgs(int64(2), int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))

	res = doReq(t, app, "GET", "/api/reset-tokens?reveal=1", "", h)
	item = envelopeMap(t, res.body)["data"].([]any)[0].(map[string]any)
	require.Equal(t, "aabbccdd", item["code_plain"], "audited reveal returns the plaintext once")
}

// Regression: the default sort must be table-qualified. resetSelect exposes
// BOTH rt.id and u.id as `id`, so an unqualified `ORDER BY id` is ambiguous
// (MySQL 1052) and the whole list — the /users "Reset code" tab — degraded to
// DB_UNAVAILABLE ("The service is busy") on every default load.
func TestAuthResetDashboardListDefaultOrderQualified(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q(sqlResetCount)).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`(?s)`+q(sqlResetRows)+`.*ORDER BY rt\.id asc, rt\.id ASC LIMIT`).
		WithArgs(int64(1), 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	res := doReq(t, app, "GET", "/api/reset-tokens", "", h)
	require.Equal(t, 200, res.status)
	body := envelopeMap(t, res.body)
	require.Contains(t, body, "meta")
	require.Equal(t, 0.0, body["meta"].(map[string]any)["total"])
}

func TestAuthResetDashboardReveal(t *testing.T) {
	t.Run("reveals and audits", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := bearer(mintAccess(t, s, adminUserRow()))
		h["X-School-ID"] = "1"

		mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
		mock.ExpectQuery(q("SELECT code_plain FROM reset_tokens WHERE id = ?")).
			WithArgs(int64(9), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"code_plain"}).AddRow("xyz789"))
		mock.ExpectExec(q(sqlResetRevealUpd)).
			WithArgs(int64(2), int64(9), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))

		res := jsonReq(t, app, "POST", "/api/reset-tokens/9/reveal", nil, h)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, "xyz789", data["code_plain"])
	})

	t.Run("cleared code is RESET_USED", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := bearer(mintAccess(t, s, adminUserRow()))
		h["X-School-ID"] = "1"

		mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
		mock.ExpectQuery(q("SELECT code_plain FROM reset_tokens WHERE id = ?")).
			WithArgs(int64(9), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"code_plain"}).AddRow(nil))

		res := jsonReq(t, app, "POST", "/api/reset-tokens/9/reveal", nil, h)
		requireErrEnvelope(t, res, CodeResetUsed)
	})

	t.Run("sa reveal skips the school predicate", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
		mock.ExpectQuery(q("SELECT code_plain FROM reset_tokens WHERE id = ?")).
			WithArgs(int64(9)).
			WillReturnRows(sqlmock.NewRows([]string{"code_plain"}).AddRow("plain12"))
		mock.ExpectExec(q(sqlResetRevealUpd)).
			WithArgs(int64(1), int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))

		res := jsonReq(t, app, "POST", "/api/reset-tokens/9/reveal", nil, bearer(mintAccess(t, s, saUserRow())))
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, "plain12", data["code_plain"])
	})
}

func TestAuthResetDashboardRevoke(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT id FROM reset_tokens WHERE id = ?")).
		WithArgs(int64(9), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectExec(q("UPDATE reset_tokens SET revoked_at = NOW(), code_plain = NULL")).
		WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/api/reset-tokens/9/revoke", nil, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["revoked"])

	// Unknown id → NOT_FOUND (no oracle).
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT id FROM reset_tokens WHERE id = ?")).
		WithArgs(int64(404), int64(1)).WillReturnError(sql.ErrNoRows)
	res = jsonReq(t, app, "POST", "/api/reset-tokens/404/revoke", nil, h)
	requireErrEnvelope(t, res, CodeNotFound)
}

// TestAuthRegisterDBErrorReleasesClaim: a DB failure mid-work answers 200
// DB_UNAVAILABLE and RELEASES the idempotency claim (retry re-executes; the
// transient failure is never recorded).
func TestAuthRegisterDBErrorReleasesClaim(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := map[string]string{"X-Idempotency-Key": "idem-dberr-1"}

	expectSchoolActive(mock, 1)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
		WithArgs("dberr@school.id").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	mock.ExpectExec(q("INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)")).
		WillReturnError(errors.New("dial tcp: connection refused"))
	// releaseClaim drops the unusable claim so a retry re-executes.
	mock.ExpectExec(q("DELETE FROM idempotency_keys WHERE school_id = ? AND route = ? AND idem_key = ?")).
		WithArgs(int64(1), idemRegisterRoute, "idem-dberr-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/api/auth/register", registerBody("dberr@school.id"), h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

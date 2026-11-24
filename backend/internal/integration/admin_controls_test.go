//go:build integration

package integration

// admin_controls_test.go — the impersonation/password-reset slice through the
// PRODUCTION route table (spec 2026-11-11 §9/§10): an SA switches into a
// facilitator of another school and the rotated refresh row keeps the ledger
// (refresh + /me still describe the switch); and an SA reset kills the
// target's pre-existing refresh row.

import (
	"database/sql/driver"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// inxUserRow builds one userSelect row with an explicit role/school state.
func inxUserRow(id int64, name, email, role string, school, tActive any) *sqlmock.Rows {
	return sqlmock.NewRows(userRowCols).
		AddRow(id, name, email, "", testPasswordHash, role, "APPROVED", true,
			school, tActive, fixedTS, "")
}

// inxSurabaya is the foreign school (id 2) the switch lands in.
func inxSurabaya() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
		AddRow(2, "Surabaya", "Indonesia", nil, true, fixedTS)
}

// captureArg records the driver values it sees (sqlmock.Argument hook).
type captureArg struct{ got []driver.Value }

func (c *captureArg) Match(v driver.Value) bool {
	c.got = append(c.got, v)
	return true
}

func accessSubject(t *testing.T, token string) string {
	t.Helper()
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(_ *jwt.Token) (any, error) {
		return []byte(accessSecret), nil
	})
	require.NoError(t, err)
	return claims.Subject
}

func TestIntegrationAdminImpersonationSessionHandoff(t *testing.T) {
	app, mock, _ := newApp(t, testConfig(t, "http://127.0.0.1:1"))

	// 1. SA (no refresh cookie) switches into facilitator 7 of school 2.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).
		WillReturnRows(inxUserRow(1, "Super", "sa@kidversa.id", "SA", nil, nil))
	mock.ExpectQuery(q("SELECT is_active FROM schools WHERE id = ?")).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(7)).
		WillReturnRows(inxUserRow(7, "Siti", "siti@school.id", "FASILITATOR", int64(2), true))
	root := &captureArg{}
	mock.ExpectBegin()
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).
		WithArgs(int64(2), int64(7), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(1), root).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(q("SELECT id, school_name, country, city, is_active, created_at FROM schools WHERE id = ?")).
		WithArgs(int64(2)).WillReturnRows(inxSurabaya())

	saToken := mintAccess(t, 1, "SA", 0)
	res := jsonReq(t, app, "POST", "/api/users/7/impersonate", nil,
		map[string]string{"Authorization": "Bearer " + saToken, "X-School-ID": "1"})
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(7), data["user"].(map[string]any)["id"])
	require.Equal(t, "FASILITATOR", data["user"].(map[string]any)["role"])
	require.Equal(t, float64(2), data["school"].(map[string]any)["id"],
		"the switch answers with the TARGET school, not the SA's header")
	require.Equal(t, map[string]any{
		"actor_id": float64(1), "actor_name": "Super", "actor_role": "SA",
	}, data["impersonation"])

	require.Len(t, root.got, 1)
	rootVal, ok := root.got[0].(string)
	require.True(t, ok, "impersonation_root must be stored as a string")
	require.Len(t, rootVal, 32, "no cookie -> random 16-byte hex root")

	cookie := refreshCookieFrom(t, res)
	require.Equal(t, "7", accessSubject(t, data["access"].(string)),
		"the new access token must carry the target subject")

	// 2. Refresh rotates the impersonated row and re-emits the block.
	jti := refreshJTI(t, cookie)
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT revoked_at, impersonated_by, impersonation_root FROM refresh_tokens WHERE jti = ? FOR UPDATE")).
		WithArgs(jti).
		WillReturnRows(sqlmock.NewRows([]string{"revoked_at", "impersonated_by", "impersonation_root"}).
			AddRow(nil, int64(1), rootVal))
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(7)).
		WillReturnRows(inxUserRow(7, "Siti", "siti@school.id", "FASILITATOR", int64(2), true))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE jti = ?")).
		WithArgs(jti).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT name, role FROM users WHERE id = ?")).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "role"}).AddRow("Super", "SA"))
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).
		WithArgs(int64(2), int64(7), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(1), rootVal).
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(q("SELECT id, school_name, country, city, is_active, created_at FROM schools WHERE id = ?")).
		WithArgs(int64(2)).WillReturnRows(inxSurabaya())

	refreshed := doReq(t, app, "POST", "/api/auth/refresh", "",
		map[string]string{"Cookie": "refresh_token=" + cookie})
	next := requireDataEnvelope(t, refreshed).(map[string]any)
	require.Equal(t, map[string]any{
		"actor_id": float64(1), "actor_name": "Super", "actor_role": "SA",
	}, next["impersonation"], "the banner must survive one rotation")
	require.Equal(t, float64(7), next["user"].(map[string]any)["id"])

	// 3. /auth/me describes the same switch for the rotated cookie.
	liveCookie := refreshCookieFrom(t, refreshed)
	liveJTI := refreshJTI(t, liveCookie)
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(7)).
		WillReturnRows(inxUserRow(7, "Siti", "siti@school.id", "FASILITATOR", int64(2), true))
	mock.ExpectQuery(q("SELECT id, school_name, country, city, is_active, created_at FROM schools WHERE id = ?")).
		WithArgs(int64(2)).WillReturnRows(inxSurabaya())
	mock.ExpectQuery(q("SELECT impersonated_by FROM refresh_tokens WHERE jti = ? AND revoked_at IS NULL")).
		WithArgs(liveJTI).
		WillReturnRows(sqlmock.NewRows([]string{"impersonated_by"}).AddRow(int64(1)))
	mock.ExpectQuery(q("SELECT name, role FROM users WHERE id = ?")).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "role"}).AddRow("Super", "SA"))

	me := doReq(t, app, "GET", "/api/auth/me", "", map[string]string{
		"Authorization": "Bearer " + next["access"].(string),
		"X-School-ID":   "2",
		"Cookie":        "refresh_token=" + liveCookie,
	})
	meData := requireDataEnvelope(t, me).(map[string]any)
	require.Equal(t, map[string]any{
		"actor_id": float64(1), "actor_name": "Super", "actor_role": "SA",
	}, meData["impersonation"])
}

// TestIntegrationAdminPasswordResetKillsSessions: the target holds a live
// session, the SA resets the password, and that session can no longer refresh
// (AC 17) — one run through the production route table.
func TestIntegrationAdminPasswordResetKillsSessions(t *testing.T) {
	app, mock, _ := newApp(t, testConfig(t, "http://127.0.0.1:1"))

	// 1. Baseline: facilitator 7 signs in.
	mock.ExpectQuery(q("FROM users u LEFT JOIN schools sch ON sch.id = u.school_id WHERE u.email = ?")).
		WithArgs("siti@school.id").
		WillReturnRows(sqlmock.NewRows(userRowCols).
			AddRow(7, "Siti", "siti@school.id", "", testPasswordHash, "FASILITATOR",
				"APPROVED", true, int64(2), true, fixedTS, ""))
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(q("SELECT id, school_name, country, city, is_active, created_at FROM schools WHERE id = ?")).
		WithArgs(int64(2)).WillReturnRows(inxSurabaya())

	login := jsonReq(t, app, "POST", "/api/auth/login",
		map[string]any{"email": "siti@school.id", "password": testPassword}, nil)
	requireDataEnvelope(t, login)
	oldCookie := refreshCookieFrom(t, login)
	oldJTI := refreshJTI(t, oldCookie)

	// 2. SA resets the facilitator's password: hash + revoke-all + ledger row.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).
		WillReturnRows(inxUserRow(1, "Super", "sa@kidversa.id", "SA", nil, nil))
	mock.ExpectQuery(q("SELECT is_active FROM schools WHERE id = ?")).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(7)).
		WillReturnRows(inxUserRow(7, "Siti", "siti@school.id", "FASILITATOR", int64(2), true))
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE users SET password_hash = ? WHERE id = ?")).
		WithArgs(sqlmock.AnyArg(), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = ? AND revoked_at IS NULL")).
		WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("INSERT INTO reset_tokens (school_id, user_id, source, created_by, code_hash, code_plain, expires_at, used_at)")).
		WithArgs(int64(2), int64(7), int64(1), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	saToken := mintAccess(t, 1, "SA", 0)
	res := jsonReq(t, app, "POST", "/api/users/7/password",
		map[string]any{"password": "ManualPass123"},
		map[string]string{"Authorization": "Bearer " + saToken, "X-School-ID": "1"})
	require.Equal(t, map[string]any{"reset": true}, requireDataEnvelope(t, res))

	// 3. The pre-existing cookie is now the single-use loser.
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT revoked_at, impersonated_by, impersonation_root FROM refresh_tokens WHERE jti = ? FOR UPDATE")).
		WithArgs(oldJTI).
		WillReturnRows(sqlmock.NewRows([]string{"revoked_at", "impersonated_by", "impersonation_root"}).
			AddRow(fixedTS, nil, nil))
	mock.ExpectRollback()

	out := doReq(t, app, "POST", "/api/auth/refresh", "",
		map[string]string{"Cookie": "refresh_token=" + oldCookie})
	requireErrEnvelope(t, out, "INVALID_TOKEN")
}

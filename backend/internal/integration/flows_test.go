//go:build integration

package integration

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/golang-jwt/jwt/v5"
	"github.com/kidversa/edutourism-v2/backend/internal"
	"github.com/stretchr/testify/require"
)

var userItemCols = []string{
	"id", "name", "email", "role", "decision", "is_active",
	"tenant_id", "wa_number", "created_at", "tenant_name",
}

func userItemRow(id int64, name, email, decision string, active bool) *sqlmock.Rows {
	return sqlmock.NewRows(userItemCols).
		AddRow(id, name, email, "FASILITATOR", decision, active, int64(1), "+628123456789", fixedTS, "Bandung")
}

func fasDBRow(id int64, email, decision string, active bool) *sqlmock.Rows {
	return sqlmock.NewRows(userRowCols).
		AddRow(id, "Fas Baru", email, "+628123456789", testPasswordHash,
			"FASILITATOR", decision, active, int64(1), true, fixedTS)
}

// refreshCookieFrom extracts the refresh_token cookie value.
func refreshCookieFrom(t *testing.T, res httpRes) string {
	t.Helper()
	sc := res.header.Get("Set-Cookie")
	idx := strings.Index(sc, "refresh_token=")
	require.GreaterOrEqual(t, idx, 0, "expected a refresh_token cookie, got %q", sc)
	rest := sc[idx+len("refresh_token="):]
	if end := strings.Index(rest, ";"); end >= 0 {
		rest = rest[:end]
	}
	require.NotEmpty(t, rest)
	return rest
}

// refreshJTI reads the jti out of a signed refresh cookie.
func refreshJTI(t *testing.T, token string) string {
	t.Helper()
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(_ *jwt.Token) (any, error) {
		return []byte(refreshSecret), nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, claims.ID)
	return claims.ID
}

// TestIntegrationRegisterApproveLoginRefreshFlow drives the FL-01 tail
// end-to-end through the full route table: register (pending) → approve →
// login → refresh rotation → the old cookie becomes INVALID_TOKEN.
func TestIntegrationRegisterApproveLoginRefreshFlow(t *testing.T) {
	app, mock, _ := newApp(t, testConfig(t, "http://127.0.0.1:1"))
	const email = "fas@school.id"

	// 1. Register: PENDING row behind the idempotency claim.
	mock.ExpectQuery(q("SELECT is_active FROM tenants WHERE id = ?")).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).WithArgs(email).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	mock.ExpectExec(q("INSERT INTO users (tenant_id, name, email, wa_number, password_hash, role, decision, is_active)")).
		WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM users WHERE tenant_id = ? AND decision = 'PENDING'")).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST /api/auth/register", "flow-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/api/auth/register", map[string]any{
		"name": "Fas Baru", "email": email, "wa_number": "081234567890",
		"password": testPassword, "role": "FASILITATOR", "tenant_id": 1,
	}, map[string]string{"X-Idempotency-Key": "flow-1"})
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(7), data["id"])
	require.Equal(t, "pending", data["status"])

	// 2. Approve: the ADMIN decision transition reloads the User wire object.
	adminH := bearer(mintAccess(t, 2, "ADMIN", 1))
	adminH["X-Tenant-ID"] = "1"
	mock.ExpectQuery(q(sqlUserByID)).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT id, COALESCE(tenant_id, 0), role, decision FROM users WHERE id = ?")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "role", "decision"}).
			AddRow(7, int64(1), "FASILITATOR", "PENDING"))
	mock.ExpectExec(q("UPDATE users SET decision = ?, is_active = ? WHERE id = ?")).
		WithArgs("APPROVED", true, int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("AND tenant_id = ? AND role = 'FASILITATOR'")).
		WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectQuery(q(sqlUserByID)).WithArgs(int64(7)).
		WillReturnRows(userItemRow(7, "Fas Baru", email, "APPROVED", true))

	res = jsonReq(t, app, "POST", "/api/users/7/approve", nil, adminH)
	userObj := requireDataEnvelope(t, res).(map[string]any)["user"].(map[string]any)
	require.Equal(t, "APPROVED", userObj["decision"])
	require.Equal(t, true, userObj["is_active"])

	// 3. Login: bcrypt verify + refresh whitelist insert + session envelope.
	mock.ExpectQuery(q("FROM users u LEFT JOIN tenants t ON t.id = u.tenant_id WHERE u.email = ?")).
		WithArgs(email).WillReturnRows(fasDBRow(7, email, "APPROVED", true))
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(q("SELECT id, name, type, is_active, created_at FROM tenants WHERE id = ?")).
		WithArgs(int64(1)).WillReturnRows(sqlTenantBandung())

	res = jsonReq(t, app, "POST", "/api/auth/login",
		map[string]any{"email": email, "password": testPassword}, nil)
	login := requireDataEnvelope(t, res).(map[string]any)
	access1, ok := login["access"].(string)
	require.True(t, ok, "login must return an access token: %s", res.body)
	require.Equal(t, "Bandung", login["tenant"].(map[string]any)["name"])
	cookie1 := refreshCookieFrom(t, res)
	jti1 := refreshJTI(t, cookie1)
	accessClaims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(access1, accessClaims, func(_ *jwt.Token) (any, error) {
		return []byte(accessSecret), nil
	})
	require.NoError(t, err)
	require.Equal(t, "7", accessClaims.Subject, "access token must carry the user id")

	// 4. Refresh: ONE Tx locks the old jti, revokes it and inserts the new one.
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT revoked_at FROM refresh_tokens WHERE jti = ? FOR UPDATE")).
		WithArgs(jti1).WillReturnRows(sqlmock.NewRows([]string{"revoked_at"}).AddRow(nil))
	mock.ExpectQuery(q(sqlUserByID)).WithArgs(int64(7)).
		WillReturnRows(fasDBRow(7, email, "APPROVED", true))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE jti = ?")).
		WithArgs(jti1).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(q("SELECT id, name, type, is_active, created_at FROM tenants WHERE id = ?")).
		WithArgs(int64(1)).WillReturnRows(sqlTenantBandung())

	res = jsonReq(t, app, "POST", "/api/auth/refresh", nil,
		map[string]string{"Cookie": "refresh_token=" + cookie1})
	refreshed := requireDataEnvelope(t, res).(map[string]any)
	access2, ok := refreshed["access"].(string)
	require.True(t, ok, "refresh must return an access token: %s", res.body)
	require.NotEmpty(t, access2)
	cookie2 := refreshCookieFrom(t, res)
	require.NotEqual(t, cookie1, cookie2, "rotation must mint a fresh refresh token")

	// 5. The rotated-away cookie is dead: single-use rotation (06 §3).
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT revoked_at FROM refresh_tokens WHERE jti = ? FOR UPDATE")).
		WithArgs(jti1).WillReturnRows(sqlmock.NewRows([]string{"revoked_at"}).AddRow(time.Now()))
	mock.ExpectRollback()
	res = jsonReq(t, app, "POST", "/api/auth/refresh", nil,
		map[string]string{"Cookie": "refresh_token=" + cookie1})
	requireErrEnvelope(t, res, internal.CodeInvalidToken)
}

// TestIntegrationIdempotencyReplayEndToEnd: same key twice → identical body,
// exactly one user insert (the idempotency store serves the replay).
func TestIntegrationIdempotencyReplayEndToEnd(t *testing.T) {
	app, mock, _ := newApp(t, testConfig(t, "http://127.0.0.1:1"))
	const route = "POST /api/auth/register"
	h := map[string]string{"X-Idempotency-Key": "e2e-idem-1"}
	body := map[string]any{
		"name": "Anak", "email": "a@school.id", "wa_number": "081234567890",
		"password": testPassword, "role": "FASILITATOR", "tenant_id": 1,
	}

	expectFresh := func() {
		mock.ExpectQuery(q("SELECT is_active FROM tenants WHERE id = ?")).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
		mock.ExpectExec(q("DELETE FROM idempotency_keys")).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(q("INSERT INTO idempotency_keys")).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).WithArgs("a@school.id").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
		mock.ExpectExec(q("INSERT INTO users (tenant_id, name, email, wa_number, password_hash, role, decision, is_active)")).
			WillReturnResult(sqlmock.NewResult(9, 1))
		mock.ExpectQuery(q("SELECT COUNT(*) FROM users WHERE tenant_id = ? AND decision = 'PENDING'")).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
			WithArgs(sqlmock.AnyArg(), int64(1), route, "e2e-idem-1").
			WillReturnResult(sqlmock.NewResult(0, 1))
	}

	expectFresh()
	res1 := jsonReq(t, app, "POST", "/api/auth/register", body, h)
	requireDataEnvelope(t, res1)

	// Replay: duplicate claim → the stored envelope, no second execution.
	mock.ExpectQuery(q("SELECT is_active FROM tenants WHERE id = ?")).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), route, "e2e-idem-1").
		WillReturnRows(sqlmock.NewRows([]string{"response"}).AddRow(res1.body))

	res2 := jsonReq(t, app, "POST", "/api/auth/register", body, h)
	require.Equal(t, res1.body, res2.body, "replay must be byte-identical")
}

// TestIntegrationDBErrorMapsToDBUnavailable: a broken database surfaces as
// the 200 DB_UNAVAILABLE envelope, never as a 5xx.
func TestIntegrationDBErrorMapsToDBUnavailable(t *testing.T) {
	app, mock, _ := newApp(t, testConfig(t, "http://127.0.0.1:1"))

	mock.ExpectQuery(q("SELECT id, name, type FROM tenants WHERE is_active = TRUE ORDER BY name")).
		WillReturnError(errors.New("dial tcp 10.0.0.5:3306: connect: connection refused"))

	res := doReq(t, app, "GET", "/api/auth/tenants", "", nil)
	requireErrEnvelope(t, res, internal.CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// OpenWA propagation (base URL injected through cfg → local httptest server)
// ---------------------------------------------------------------------------

// waStub is a local OpenWA stand-in recording every send-text call.
type waStub struct {
	calls atomic.Int64
}

func startWAStub(t *testing.T, sendStatus int) (*waStub, string) {
	t.Helper()
	stub := &waStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sessions", func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(`[{"id":"kidversa-test","name":"Kidversa","status":"ready"}]`))
	})
	mux.HandleFunc("/api/sessions/", func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages/send-text") {
			stub.calls.Add(1)
			rw.WriteHeader(sendStatus)
			_, _ = rw.Write([]byte(`{"messageId":"msg-1"}`))
			return
		}
		_, _ = rw.Write([]byte(`{"id":"kidversa-test","name":"Kidversa","status":"ready"}`)) // session state probe
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return stub, srv.URL
}

// TestIntegrationWAStatusReachable: the status proxy answers 200 {connected}
// with the live session name.
func TestIntegrationWAStatusReachable(t *testing.T) {
	_, waURL := startWAStub(t, http.StatusOK)
	app, mock, _ := newApp(t, testConfig(t, waURL))

	mock.ExpectQuery(q(sqlUserByID)).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	h := bearer(mintAccess(t, 2, "ADMIN", 1))
	h["X-Tenant-ID"] = "1"
	res := doReq(t, app, "GET", "/api/wa/status", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["connected"])
	require.Equal(t, "Kidversa", data["session"])
	require.NotEmpty(t, data["checked_at"])
}

// TestIntegrationWAStatusDownStays200: unreachable OpenWA still answers 200
// {connected:false} — never WA_FAILED (02 §2b).
func TestIntegrationWAStatusDownStays200(t *testing.T) {
	app, mock, _ := newApp(t, testConfig(t, "http://127.0.0.1:1"))

	mock.ExpectQuery(q(sqlUserByID)).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	h := bearer(mintAccess(t, 2, "ADMIN", 1))
	h["X-Tenant-ID"] = "1"
	res := doReq(t, app, "GET", "/api/wa/status", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, false, data["connected"])
}

// TestIntegrationSendConsentWAFailed: an OpenWA outage maps to the 200
// WA_FAILED envelope on the send-consent path, the outbox row records the
// failure and the send was retried exactly once.
func TestIntegrationSendConsentWAFailed(t *testing.T) {
	stub, waURL := startWAStub(t, http.StatusInternalServerError)
	app, mock, _ := newApp(t, testConfig(t, waURL))

	h := bearer(mintAccess(t, 2, "ADMIN", 1))
	h["X-Tenant-ID"] = "1"
	h["X-Idempotency-Key"] = "wa-down-1"

	mock.ExpectQuery(q(sqlUserByID)).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND tenant_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("OPENED"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT mother_wa, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "is_active"}).AddRow("+628111111111", true))
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(q("INSERT INTO consent_links")).WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectExec(q("INSERT INTO wa_outbox")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST /api/sessions/2/children/30/send-consent", "wa-down-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/api/sessions/2/children/30/send-consent", nil, h)
	requireErrEnvelope(t, res, internal.CodeWAFailed)
	require.EqualValues(t, 2, stub.calls.Load(), "OpenWA send must retry exactly once")
}

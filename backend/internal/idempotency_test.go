package internal

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

const idemRegisterRoute = "POST /api/auth/register"

func registerBody(email string) map[string]any {
	return map[string]any{
		"name": "Anak", "email": email, "wa_number": "081234567890",
		"password": testPassword, "role": "FASILITATOR", "school_id": 1,
	}
}

// expectRegisterFreshClaim wires the first-execution expectations: purge
// (DELETE), claim (INSERT), email probe, user insert, pending count and the
// response recording.
func expectRegisterFreshClaim(mock sqlmock.Sqlmock, email string, key string, newID int64, pending int) {
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
		WithArgs(email).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	mock.ExpectExec(q("INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)")).
		WillReturnResult(sqlmock.NewResult(newID, 1))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM users WHERE school_id = ? AND decision = 'PENDING'")).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(pending))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), idemRegisterRoute, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectSchoolActive(mock sqlmock.Sqlmock, school int64) {
	mock.ExpectQuery(sqlSchoolActive()).WithArgs(school).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
}

// TestIdempotencyReplaySkipsSecondSideEffect: the stored envelope replays
// byte-for-byte and the work (INSERT users) runs exactly once.
func TestIdempotencyReplaySkipsSecondSideEffect(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := map[string]string{"X-Idempotency-Key": "idem-replay-1"}

	expectSchoolActive(mock, 1)
	expectRegisterFreshClaim(mock, "a@school.id", "idem-replay-1", 9, 3)
	res1 := jsonReq(t, app, "POST", "/api/auth/register", registerBody("a@school.id"), h)
	data1 := requireDataEnvelope(t, res1).(map[string]any)
	require.Equal(t, float64(9), data1["id"])
	require.Equal(t, "pending", data1["status"])

	// Replay: duplicate claim → recorded response, NO second execution.
	expectSchoolActive(mock, 1)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "idem-replay-1").
		WillReturnRows(sqlmock.NewRows([]string{"response"}).AddRow(res1.body))

	res2 := jsonReq(t, app, "POST", "/api/auth/register", registerBody("a@school.id"), h)
	require.Equal(t, 200, res2.status)
	require.Equal(t, res1.body, res2.body)
}

// TestIdempotencyInProgressSemantics: a duplicate arriving while the original
// claim has no recorded response yet answers IN_PROGRESS.
func TestIdempotencyInProgressSemantics(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := map[string]string{"X-Idempotency-Key": "idem-running-1"}

	expectSchoolActive(mock, 1)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), idemRegisterRoute, "idem-running-1").
		WillReturnRows(sqlmock.NewRows([]string{"response"}).AddRow(nil))

	res := jsonReq(t, app, "POST", "/api/auth/register", registerBody("b@school.id"), h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "IN_PROGRESS", data["status"])
}

// TestIdempotencyMissingKeyRejected: X-Idempotency-Key is mandatory.
func TestIdempotencyMissingKeyRejected(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectSchoolActive(mock, 1)
	res := jsonReq(t, app, "POST", "/api/auth/register", registerBody("c@school.id"), nil)
	requireErrEnvelope(t, res, CodeValidation)
}

// TestIdempotencyCrossSchoolSameKeyNeverReplays: the claim is the composite
// (school, route, key) — the same key under another school executes fresh.
func TestIdempotencyCrossSchoolSameKeyNeverReplays(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := map[string]string{"X-Idempotency-Key": "shared-key"}
	body := func(email string, school int64) map[string]any {
		b := registerBody(email)
		b["school_id"] = school
		return b
	}

	expectSchoolActive(mock, 1)
	expectRegisterFreshClaim(mock, "x1@school.id", "shared-key", 9, 1)
	res1 := jsonReq(t, app, "POST", "/api/auth/register", body("x1@school.id", 1), h)
	data1 := requireDataEnvelope(t, res1).(map[string]any)
	require.Equal(t, float64(9), data1["id"])

	// Same key, school 2: a fresh claim INSERT succeeds → full execution.
	mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
		WithArgs("x2@school.id").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
	mock.ExpectExec(q("INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)")).
		WillReturnResult(sqlmock.NewResult(11, 1))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM users WHERE school_id = ? AND decision = 'PENDING'")).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(2), idemRegisterRoute, "shared-key").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res2 := jsonReq(t, app, "POST", "/api/auth/register", body("x2@school.id", 2), h)
	data2 := requireDataEnvelope(t, res2).(map[string]any)
	require.Equal(t, float64(11), data2["id"], "cross-school same key must NOT replay")
}

// TestIdempotencyStaleClaimPurgedExecutesFresh: a claim older than 24 h is
// deleted by the purge DELETE before the INSERT, so the request executes
// instead of replaying.
func TestIdempotencyStaleClaimPurgedExecutesFresh(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := map[string]string{"X-Idempotency-Key": "stale-key"}

	expectSchoolActive(mock, 1)
	expectRegisterFreshClaim(mock, "s1@school.id", "stale-key", 9, 1)
	res1 := jsonReq(t, app, "POST", "/api/auth/register", registerBody("s1@school.id"), h)
	requireDataEnvelope(t, res1)

	// Second request: the purge DELETE removes the old claim, the claim
	// INSERT therefore succeeds and the work re-executes (fresh, not replay).
	expectSchoolActive(mock, 1)
	expectRegisterFreshClaim(mock, "s1@school.id", "stale-key", 10, 2)
	res2 := jsonReq(t, app, "POST", "/api/auth/register", registerBody("s1@school.id"), h)
	data2 := requireDataEnvelope(t, res2).(map[string]any)
	require.Equal(t, float64(10), data2["id"], "a purged (>24h) claim must re-execute, not replay")
}

// TestIdempotencyNestedPerItemDBCodeRecordedAndReplays: a SUCCESS envelope
// carrying a NESTED per-item "code":"DB_UNAVAILABLE" (batch gate) must be
// RECORDED — the typed signal only fires on a TOP-level DB_UNAVAILABLE, so
// the same-key replay returns the envelope and the loop runs exactly once
// (F-02-04: byte-sniffing released this claim → duplicate WhatsApp sends).
func TestIdempotencyNestedPerItemDBCodeRecordedAndReplays(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	h["X-Idempotency-Key"] = "batch-dbitem-1"
	const batchPath = "/api/sessions/2/batch-send"
	batchBody := map[string]any{
		"items": []map[string]any{{"session_topic_id": 5, "child_id": 30}},
	}

	// First execution: pre-wrapper replay probe (no claim), then the claim,
	// then the per-item gate hits DB trouble → the item's code rides NESTED
	// inside a success envelope.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), "POST "+batchPath, "batch-dbitem-1").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnError(errors.New("dial tcp: connection refused"))
	// No flag → the envelope is RECORDED (the old bytes.Contains released).
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST "+batchPath, "batch-dbitem-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res1 := jsonReq(t, app, "POST", batchPath, batchBody, h)
	data1 := requireDataEnvelope(t, res1).(map[string]any)
	items := data1["items"].([]any)
	require.Len(t, items, 1, "one item in, one result out: %s", res1.body)
	item := items[0].(map[string]any)
	require.Equal(t, "FAILED", item["status"])
	require.Equal(t, CodeDBUnavailable, item["code"],
		"the per-item DB code is nested in the success envelope")

	// Same key again: the pre-wrapper probe sees the RECORDED claim (not
	// NULL) and stands aside; duplicate claim → the recorded envelope
	// replays byte-for-byte; the batch loop does NOT run a second time.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), "POST "+batchPath, "batch-dbitem-1").
		WillReturnRows(sqlmock.NewRows([]string{"response"}).AddRow(res1.body))
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
		WithArgs(int64(1), "POST "+batchPath, "batch-dbitem-1").
		WillReturnRows(sqlmock.NewRows([]string{"response"}).AddRow(res1.body))

	res2 := jsonReq(t, app, "POST", batchPath, batchBody, h)
	require.Equal(t, 200, res2.status)
	require.Equal(t, res1.body, res2.body, "replay must return the recorded envelope")
}

// TestIdempotencyTopLevelGateDBFailReleasesClaim: a TOP-level DB_UNAVAILABLE
// written by the send-report gate sets the typed signal (kvDBFail) — the
// claim releases so a retry re-executes; only nested codes stay recorded.
func TestIdempotencyTopLevelGateDBFailReleasesClaim(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	h["X-Idempotency-Key"] = "send-report-dberr-1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// The first gate query dies → gateReportItem maps it to DB_UNAVAILABLE →
	// the handler writes the TOP-level envelope and flags the request.
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnError(errors.New("dial tcp: connection refused"))
	// releaseClaim drops the claim — no response is ever recorded.
	mock.ExpectExec(q("DELETE FROM idempotency_keys WHERE school_id = ? AND route = ? AND idem_key = ?")).
		WithArgs(int64(1), "POST "+sendReportPath, "send-report-dberr-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", sendReportPath, map[string]any{}, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

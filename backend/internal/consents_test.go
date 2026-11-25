package internal

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// waStub is a local OpenWA stand-in that records every send-text call. The
// session list is answered separately: sendText resolves the configured
// session name to OpenWA's UUID before posting.
type waStub struct {
	calls atomic.Int64
}

func (w *waStub) handler(status int) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sessions", func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(`[{"id":"uuid-wa","name":"kidversa-test","status":"ready"}]`))
	})
	mux.HandleFunc("/api/sessions/", func(rw http.ResponseWriter, _ *http.Request) {
		w.calls.Add(1)
		rw.WriteHeader(status)
		_, _ = rw.Write([]byte(`{"messageId":"msg-1"}`))
	})
	return mux
}

func startWAStub(t *testing.T, status int) (*waStub, string) {
	t.Helper()
	stub := &waStub{}
	srv := httptest.NewServer(stub.handler(status))
	t.Cleanup(srv.Close)
	return stub, srv.URL
}

const consentSendPath = "/api/sessions/2/children/30/send-consent"
const consentBatchPath = "/api/sessions/2/send-consent"

// TestConsentRespondFirstWinsAlreadyDecided: a decision UPDATE hitting 0 rows
// re-reads and answers {decision, already:true} — an idempotent SUCCESS.
func TestConsentRespondFirstWinsAlreadyDecided(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
		WithArgs("tok1").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id", "child_id"}).
			AddRow(int64(1), int64(2), int64(30)))
	mock.ExpectQuery(q("SELECT id FROM consent_links WHERE session_id = ? AND child_id = ?")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(5))
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("SET status = ?, respondent_name = ?, responded_at = NOW()")).
		WithArgs("SETUJU", "Ibu Ayu", int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 0)) // first-wins lost: already decided
	mock.ExpectQuery(q("SELECT status FROM consent_links WHERE id = ?")).
		WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("SETUJU"))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST /public/consent/respond", "consent-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/public/consent/respond",
		map[string]any{"token": "tok1", "decision": "SETUJU", "respondent_name": "Ibu Ayu"},
		map[string]string{"X-Idempotency-Key": "consent-1"})
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "SETUJU", data["decision"])
	require.Equal(t, true, data["already"])
}

// TestConsentRespondRecordsFirstDecision: the first PENDING → SETUJU flip is
// recorded and acknowledged on the public stream.
func TestConsentRespondRecordsFirstDecision(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok2").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
		WithArgs("tok2").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id", "child_id"}).
			AddRow(int64(1), int64(2), int64(30)))
	mock.ExpectQuery(q("SELECT id FROM consent_links WHERE session_id = ? AND child_id = ?")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(6))
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("SET status = ?, respondent_name = ?, responded_at = NOW()")).
		WithArgs("SETUJU", "Ibu Ayu", int64(6)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST /public/consent/respond", "consent-2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/public/consent/respond",
		map[string]any{"token": "tok2", "decision": "SETUJU", "respondent_name": "Ibu Ayu"},
		map[string]string{"X-Idempotency-Key": "consent-2"})
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "SETUJU", data["decision"])
	_, already := data["already"]
	require.False(t, already, "the recorded first decision carries no already flag")
}

func TestConsentRespondUnknownTokenIsValidationSafe(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("ghost").WillReturnError(sql.ErrNoRows)
	// The first consent_links probe of the shared resolver answers "unknown
	// token" without touching idempotency (003b: one token→scope authority).
	mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
		WithArgs("ghost").WillReturnError(sql.ErrNoRows)
	res := jsonReq(t, app, "POST", "/public/consent/respond",
		map[string]any{"token": "ghost", "decision": "SETUJU", "respondent_name": "Ibu"}, nil)
	requireErrEnvelope(t, res, CodeTokenInvalid)
}

// TestSendConsentDecidedSkipsWA: a decided link answers immediately with the
// stored token/decision and NEVER re-sends the WhatsApp message.
func TestSendConsentDecidedSkipsWA(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	stub, waURL := startWAStub(t, http.StatusOK)
	s.cfg.OpenwaBaseURL = waURL

	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	h["X-Idempotency-Key"] = "send-consent-1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT mother_wa, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "is_active"}).
			AddRow("+628111111111", true))
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token", "status"}).
			AddRow(7, "tok-existing", "SETUJU"))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST "+consentSendPath, "send-consent-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", consentSendPath, nil, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "tok-existing", data["token"])
	require.Equal(t, "SETUJU", data["decision"])
	require.Equal(t, false, data["resent"])
	require.Zero(t, stub.calls.Load(), "a decided consent must never re-send WA")
}

// TestSessionConsentListIncludesNeverSent: the session list is driven by
// membership, so a child with no consent_links row is a NOT_SENT row.
func TestSessionConsentListIncludesNeverSent(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM session_members sm")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(q("SELECT sm.child_id, c.name, g.name")).
		WithArgs(int64(2), int64(1), 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"child_id", "name", "group_name", "status", "respondent_name", "responded_at", "token",
		}).
			AddRow(30, "zan", "Kelompok Jerapah", "NOT_SENT", nil, nil, nil).
			AddRow(31, "Aisyah", "Kelompok Jerapah", "PENDING", nil, nil, "tok-31"))

	res := doReq(t, app, "GET", "/api/sessions/2/consents", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 2)
	first := data[0].(map[string]any)
	require.Equal(t, "NOT_SENT", first["status"])
	_, hasURL := first["consent_url"]
	require.False(t, hasURL, "never-sent rows have no link to copy")
	second := data[1].(map[string]any)
	require.Equal(t, "PENDING", second["status"])
	require.Contains(t, second["consent_url"], "tok-31")
}

// TestSessionConsentListFilters: the NOT_SENT and group_id predicates land in
// BOTH the membership count and list queries (frontend filter options).
func TestSessionConsentListFilters(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM session_members sm")).
		WithArgs(int64(2), int64(1), int64(10)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("SELECT sm.child_id, c.name, g.name")).
		WithArgs(int64(2), int64(1), int64(10), 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"child_id", "name", "group_name", "status", "respondent_name", "responded_at", "token",
		}).AddRow(30, "zan", "Kelompok Jerapah", "NOT_SENT", nil, nil, nil))

	res := doReq(t, app, "GET",
		"/api/sessions/2/consents?status=NOT_SENT&group_id=10", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 1)
	require.Equal(t, "NOT_SENT", data[0].(map[string]any)["status"])
}

// TestSessionConsentListRejectsBadGroup: a non-numeric/zero group_id is
// VALIDATION before any list query.
func TestSessionConsentListRejectsBadGroup(t *testing.T) {
	for _, query := range []string{"?group_id=abc", "?group_id=0"} {
		t.Run(query, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			h := bearer(mintAccess(t, s, adminUserRow()))
			h["X-School-ID"] = "1"
			mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
			res := doReq(t, app, "GET", "/api/sessions/2/consents"+query, "", h)
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

// TestSendConsentBatchPerItem: one request processes the selection, skips an
// already-decided child, and dedupes repeated ids.
func TestSendConsentBatchPerItem(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	stub, waURL := startWAStub(t, http.StatusOK)
	s.cfg.OpenwaBaseURL = waURL

	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	h["X-Idempotency-Key"] = "send-consent-batch-1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))

	// Child 30: existing PENDING row → resend.
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT mother_wa, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "is_active"}).
			AddRow("+628111111111", true))
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token", "status"}).
			AddRow(7, "tok-30", "PENDING"))
	mock.ExpectExec(q("INSERT INTO wa_outbox")).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Child 31: already decided → SKIPPED, no WA/outbox.
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(31)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT mother_wa, is_active FROM children")).
		WithArgs(int64(31), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "is_active"}).
			AddRow("+628111111112", true))
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(31)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token", "status"}).
			AddRow(8, "tok-31", "SETUJU"))

	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST "+consentBatchPath, "send-consent-batch-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", consentBatchPath,
		map[string]any{"child_ids": []int64{30, 31, 31}}, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	items := data["items"].([]any)
	require.Len(t, items, 2, "duplicate ids are processed once")
	first := items[0].(map[string]any)
	require.Equal(t, float64(30), first["child_id"])
	require.Equal(t, "SENT", first["status"])
	second := items[1].(map[string]any)
	require.Equal(t, float64(31), second["child_id"])
	require.Equal(t, "SKIPPED", second["status"])
	require.Equal(t, "SETUJU", second["decision"])
	require.EqualValues(t, 1, stub.calls.Load())
}

// TestSendConsentWAFailedEnvelope: an unreachable OpenWA maps to the 200
// WA_FAILED envelope (per the 06 §2 propagation rule) and the outbox row
// records the failure.
func TestSendConsentWAFailedEnvelope(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	stub, waURL := startWAStub(t, http.StatusInternalServerError)
	s.cfg.OpenwaBaseURL = waURL

	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	h["X-Idempotency-Key"] = "send-consent-2"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT mother_wa, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "is_active"}).
			AddRow("+628111111111", true))
	// No consent row yet → create + send.
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(q("INSERT INTO consent_links")).
		WillReturnResult(sqlmock.NewResult(7, 1))
	// The outbox audit row records the FAILED send.
	mock.ExpectExec(q("INSERT INTO wa_outbox")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST "+consentSendPath, "send-consent-2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", consentSendPath, nil, h)
	requireErrEnvelope(t, res, CodeWAFailed)
	require.EqualValues(t, 2, stub.calls.Load(), "OpenWA send must retry exactly once")
}

// TestSendConsentOutboxSurvivesRequestDeadline: the OpenWA send has a 10 s
// budget, longer than the request-scoped 5 s DB context. When the send
// outlives the caller's deadline the audit row must still be written on a
// fresh context; otherwise a delivered consent leaves no wa_outbox record.
func TestSendConsentOutboxSurvivesRequestDeadline(t *testing.T) {
	s, mock := newTestServer(t)

	slow := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			_, _ = rw.Write([]byte(`[{"id":"uuid-wa","name":"kidversa-test","status":"ready"}]`))
			return
		}
		time.Sleep(300 * time.Millisecond)
		_, _ = rw.Write([]byte(`{"messageId":"msg-1"}`))
	}))
	t.Cleanup(slow.Close)
	s.cfg.OpenwaBaseURL = slow.URL

	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT mother_wa, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "is_active"}).
			AddRow("+628111111111", true))
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token", "status"}).
			AddRow(7, "tok-30", "PENDING"))
	mock.ExpectExec(q("INSERT INTO wa_outbox")).
		WillReturnResult(sqlmock.NewResult(1, 1))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	out, err := s.sendConsentOne(ctx, 1, 2, 30, "OPENED", "idem-deadline")
	require.NoError(t, err)
	require.Equal(t, "SENT", out.Status)
}

//go:build integration

package integration

import (
	"bufio"
	"database/sql"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofiber/fiber/v3"
	"github.com/kidversa/edutourism-v2/backend/internal"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// SSE end-to-end over a real listener: a guarded transition publishes, the
// stream replays it on handshake and then delivers a live frame.
// ---------------------------------------------------------------------------

type sseEvent struct {
	id   string
	name string
	data string
}

func startListenerApp(t *testing.T, app *fiber.App) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() { ln.Close() })
	return "http://" + ln.Addr().String()
}

func startSSEReader(r io.Reader) <-chan sseEvent {
	ch := make(chan sseEvent, 32)
	go func() {
		defer close(ch)
		var ev sseEvent
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.name != "" || ev.data != "" {
					ch <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "id:"):
				ev.id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			case strings.HasPrefix(line, "event:"):
				ev.name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				ev.data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
	}()
	return ch
}

func connectSSE(t *testing.T, base, token, school, lastEventID string) (*http.Response, <-chan sseEvent) {
	t.Helper()
	client := &http.Client{}
	var resp *http.Response
	var err error
	deadline := time.Now().Add(3 * time.Second)
	for {
		var req *http.Request
		req, err = http.NewRequest("GET", base+"/api/stream", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		if school != "" {
			req.Header.Set("X-School-ID", school)
		}
		if lastEventID != "" {
			req.Header.Set("Last-Event-ID", lastEventID)
		}
		resp, err = client.Do(req)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect SSE: %v", err)
		}
		time.Sleep(20 * time.Millisecond) // server goroutine startup retry
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, startSSEReader(resp.Body)
}

func nextEvent(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		require.True(t, ok, "stream closed while waiting for an event")
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SSE event")
		return sseEvent{}
	}
}

func expectNoEvent(t *testing.T, ch <-chan sseEvent) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("unexpected extra event %q (id %s)", ev.name, ev.id)
		}
	case <-time.After(250 * time.Millisecond): // negative observation window
	}
}

// connectSSEPublic opens /public/stream?token= (no Authorization).
func connectSSEPublic(t *testing.T, base, token, lastEventID string) (*http.Response, <-chan sseEvent) {
	t.Helper()
	client := &http.Client{}
	var resp *http.Response
	var err error
	deadline := time.Now().Add(3 * time.Second)
	for {
		var req *http.Request
		req, err = http.NewRequest("GET", base+"/public/stream?token="+token, nil)
		require.NoError(t, err)
		if lastEventID != "" {
			req.Header.Set("Last-Event-ID", lastEventID)
		}
		resp, err = client.Do(req)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect public SSE: %v", err)
		}
		time.Sleep(20 * time.Millisecond) // server goroutine startup retry
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, startSSEReader(resp.Body)
}

func statusRows(status string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"status"}).AddRow(status)
}

// expectTransition wires one guarded close transition through the full request
// path (auth → steady-state read → locked Tx → publish).
func expectTransition(mock sqlmock.Sqlmock, sid int64, from, to string) {
	mock.ExpectQuery(q(sqlUserByID)).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(sid, int64(1)).WillReturnRows(statusRows(from))
	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(sid, int64(1)).WillReturnRows(statusRows(from))
	mock.ExpectExec(q("UPDATE sessions SET status = ? WHERE id = ?")).
		WithArgs(to, sid, int64(1), from).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

// expectOpenFromFinished wires a restart (FINISHED → OPENED), which runs the
// same group/child/facilitator gates as the first start.
func expectOpenFromFinished(mock sqlmock.Sqlmock, sid int64) {
	mock.ExpectQuery(q(sqlUserByID)).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(sid, int64(1)).WillReturnRows(statusRows("FINISHED"))
	mock.ExpectBegin()
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ? FOR UPDATE")).
		WithArgs(sid, int64(1)).WillReturnRows(statusRows("FINISHED"))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM session_groups WHERE session_id = ?")).
		WithArgs(sid).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery(q("FROM session_groups g WHERE g.session_id = ?")).
		WithArgs(sid).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id", "members"}).
			AddRow(10, "Kelompok Merah", 3, 2))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(1))
	mock.ExpectExec(q("UPDATE sessions SET status = ? WHERE id = ?")).
		WithArgs("OPENED", sid, int64(1), "FINISHED").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

func TestIntegrationSSEHandshakeReplayAndLiveFrame(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1")
	app, mock, s := newApp(t, cfg)
	base := startListenerApp(t, app)
	t.Cleanup(s.Stop)

	// A guarded transition publishes session.status rev1 (post-commit).
	expectTransition(mock, 2, "OPENED", "FINISHED")
	h := bearer(mintAccess(t, 2, "ADMIN", 1))
	h["X-School-ID"] = "1"
	res := jsonReq(t, app, "POST", "/api/sessions/2/close", nil, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "FINISHED", data["status"])

	// Handshake with Last-Event-ID 0 replays the ring: the frame the
	// transition published arrives as the very first event. ADMIN handshakes
	// need no school probe (the guard reads the joined school state); the
	// session-liveness probe requires an active refresh_tokens row.
	mock.ExpectQuery(q(sqlUserByID)).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("FROM refresh_tokens WHERE user_id = ? AND revoked_at IS NULL AND expires_at > NOW()")).
		WithArgs(int64(2)).WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	resp, events := connectSSE(t, base, mintAccess(t, 2, "ADMIN", 1), "1", "0")
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	ev := nextEvent(t, events)
	require.Equal(t, "session.status", ev.name)
	require.Equal(t, "1", ev.id)
	require.Contains(t, ev.data, `"entity_id":2`)
	require.Contains(t, ev.data, `"session_id":2`)
	require.Contains(t, ev.data, `"action":"finished"`)

	// A second transition publishes rev2 while the stream is live — no
	// reconnect, the frame is pushed. A FINISHED session starts again through
	// the same open gates.
	expectOpenFromFinished(mock, 2)
	res = jsonReq(t, app, "POST", "/api/sessions/2/open", nil, h)
	requireDataEnvelope(t, res)
	ev = nextEvent(t, events)
	require.Equal(t, "session.status", ev.name)
	require.Equal(t, "2", ev.id)
	require.Contains(t, ev.data, `"action":"opened"`)
	resp.Body.Close()

	// K3 acceptance: logout revoked every refresh_tokens row — the SAME
	// still-cryptographically-valid Bearer now fails the handshake with an
	// INVALID_TOKEN envelope and no stream opens. ADMIN is not SA: the
	// reused h carries the matching school header, so the guard reaches
	// the probe instead of failing at SCHOOL_MISMATCH.
	mock.ExpectQuery(q(sqlUserByID)).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("FROM refresh_tokens WHERE user_id = ? AND revoked_at IS NULL AND expires_at > NOW()")).
		WithArgs(int64(2)).WillReturnError(sql.ErrNoRows)
	res = doReq(t, app, "GET", "/api/stream", "", h)
	requireErrEnvelope(t, res, internal.CodeInvalidToken)
	require.Contains(t, res.header.Get("Content-Type"), "application/json",
		"a revoked session must not open a stream")
}

// TestIntegrationPublicStreamTokenScope: /public/stream?token= locks the
// connection to the token's (session, child) scope; the consent respond
// handler's publishPublic reaches it while a foreign scope is filtered.
func TestIntegrationPublicStreamTokenScope(t *testing.T) {
	app, mock, s := newApp(t, testConfig(t, "http://127.0.0.1:1"))
	base := startListenerApp(t, app)
	t.Cleanup(s.Stop)

	respondOK := func(token string, key string, id int64) {
		mock.ExpectExec(q("DELETE FROM idempotency_keys")).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(q("INSERT INTO idempotency_keys")).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q("SET status = ?, respondent_name = ?, responded_at = NOW()")).
			WithArgs("SETUJU", "Ibu", id).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
			WithArgs(sqlmock.AnyArg(), int64(1), "POST /public/consent/respond", key).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}

	// Unknown token → TOKEN_INVALID envelope, no stream.
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("ghost").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
		WithArgs("ghost").WillReturnError(sql.ErrNoRows)
	res := doReq(t, app, "GET", "/public/stream?token=ghost", "", nil)
	requireErrEnvelope(t, res, internal.CodeTokenInvalid)

	// The parent responds FIRST: consent.ack (session 2, child 30) lands in
	// the school ring before any public connection exists. The respond
	// handler resolves the token through the shared resolver (003b), then
	// keys its consent row by (session_id, child_id).
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-c1").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
		WithArgs("tok-c1").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id", "child_id"}).
			AddRow(int64(1), int64(2), int64(30)))
	mock.ExpectQuery(q("SELECT id FROM consent_links WHERE session_id = ? AND child_id = ?")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(50))
	respondOK("tok-c1", "pub-resp-1", 50)
	res = jsonReq(t, app, "POST", "/public/consent/respond",
		map[string]any{"token": "tok-c1", "decision": "SETUJU", "respondent_name": "Ibu"},
		map[string]string{"X-Idempotency-Key": "pub-resp-1"})
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "SETUJU", data["decision"])

	// tok-c1 (session 2, child 30) resolves, locks the connection scope and
	// replays the ack from the ring (filter applies during replay).
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-c1").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
		WithArgs("tok-c1").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id", "child_id"}).
			AddRow(int64(1), int64(2), int64(30)))
	resp, events := connectSSEPublic(t, base, "tok-c1", "0")
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	ev := nextEvent(t, events)
	require.Equal(t, "consent.ack", ev.name)
	require.Contains(t, ev.data, `"decision":"SETUJU"`)

	// A foreign scope (session 9, child 40) publishes too — the tok-c1
	// connection must NOT receive it (token scope lock).
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-c2").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
		WithArgs("tok-c2").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id", "child_id"}).
			AddRow(int64(1), int64(9), int64(40)))
	mock.ExpectQuery(q("SELECT id FROM consent_links WHERE session_id = ? AND child_id = ?")).
		WithArgs(int64(9), int64(40)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(51))
	respondOK("tok-c2", "pub-resp-2", 51)
	res = jsonReq(t, app, "POST", "/public/consent/respond",
		map[string]any{"token": "tok-c2", "decision": "SETUJU", "respondent_name": "Ibu"},
		map[string]string{"X-Idempotency-Key": "pub-resp-2"})
	requireDataEnvelope(t, res)
	expectNoEvent(t, events)
	resp.Body.Close()
}

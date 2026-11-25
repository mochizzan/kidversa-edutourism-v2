package internal

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// waOpenWAStub wires one local OpenWA stand-in into the server config.
func waOpenWAStub(t *testing.T, s *Server, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	s.cfg.OpenwaBaseURL = srv.URL
	s.cfg.OpenwaSession = "kidversa-test"
	s.cfg.OpenwaAPIKey = "k"
}

func TestWaResolveSessionMatchesName(t *testing.T) {
	s, _ := newTestServer(t)
	var listHits atomic.Int32
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions" {
			t.Errorf("unexpected path %q", r.URL.Path)
			return
		}
		listHits.Add(1)
		io.WriteString(w, `[{"id":"uuid-1","name":"kidversa-test","status":"ready"}]`)
	})

	id, err := s.waResolveSession(context.Background())
	require.NoError(t, err)
	require.Equal(t, "uuid-1", id)
	id2, err := s.waResolveSession(context.Background())
	require.NoError(t, err)
	require.Equal(t, "uuid-1", id2)
	require.Equal(t, int32(1), listHits.Load(), "the resolved id must be cached")
}

func TestWaResolveSessionFallsBackToConfiguredValue(t *testing.T) {
	s, _ := newTestServer(t)
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	id, err := s.waResolveSession(context.Background())
	require.NoError(t, err)
	require.Equal(t, "kidversa-test", id)
}

// A name-based OPENWA_SESSION must be resolved to the session UUID before
// posting send-text: OpenWA checks the engine registry by id, so a name path
// answers 400 "Session '<name>' is not active" even while the session is ready.
func TestWaSendTextUsesResolvedSessionID(t *testing.T) {
	s, _ := newTestServer(t)
	var listHits, sendHits atomic.Int32
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			listHits.Add(1)
			io.WriteString(w, `[{"id":"uuid-42","name":"kidversa-test","status":"ready"}]`)
		case "/api/sessions/uuid-42/messages/send-text":
			sendHits.Add(1)
			io.WriteString(w, `{"messageId":"wamid.OK"}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	})

	id, err := s.sendText("628111@c.us", "halo")
	require.NoError(t, err)
	require.Equal(t, "wamid.OK", id)
	require.Equal(t, int32(1), listHits.Load(), "a name-based config must list sessions once")
	require.Equal(t, int32(1), sendHits.Load(), "send-text must use the resolved UUID")
}

func TestWaSessionStateNotFound(t *testing.T) {
	s, _ := newTestServer(t)
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/uuid-1" {
			t.Errorf("unexpected path %q", r.URL.Path)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := s.waSessionState(context.Background(), "uuid-1")
	require.ErrorIs(t, err, errWaMissing)
}

func TestWaQRNoCodeIsEmpty(t *testing.T) {
	s, _ := newTestServer(t)
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/uuid-1/qr" {
			t.Errorf("unexpected path %q", r.URL.Path)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})

	qr, err := s.waQR(context.Background(), "uuid-1")
	require.NoError(t, err)
	require.Equal(t, "", qr)
}

func TestWaStartAlreadyStartedIsOK(t *testing.T) {
	s, _ := newTestServer(t)
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/uuid-1/start" {
			t.Errorf("unexpected request %s %q", r.Method, r.URL.Path)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})

	require.NoError(t, s.waStart(context.Background(), "uuid-1"))
}

func TestWaStartServerError(t *testing.T) {
	s, _ := newTestServer(t)
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	require.Error(t, s.waStart(context.Background(), "uuid-1"))
}

// ---------------------------------------------------------------------------
// Task 2: GET /api/wa/session + POST /api/wa/session/start
// ---------------------------------------------------------------------------

func waConnectApp(t *testing.T, s *Server) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	s.RegisterWa(app.Group("/api"))
	return app
}

// waSessionStub answers the resolver + session-state calls for uuid-1.
func waSessionStub(t *testing.T, s *Server, status, phone string) {
	t.Helper()
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "k" {
			t.Errorf("X-API-Key = %q, want k", r.Header.Get("X-API-Key"))
		}
		switch r.URL.Path {
		case "/api/sessions":
			io.WriteString(w, `[{"id":"uuid-1","name":"kidversa-test"}]`)
		case "/api/sessions/uuid-1":
			io.WriteString(w, `{"id":"uuid-1","name":"Kantor Pusat","status":"`+status+`","phone":"`+phone+`"}`)
		case "/api/sessions/uuid-1/qr":
			if status == "ready" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"qrCode":"data:image/png;base64,AAA","status":"qr_ready"}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	})
}

func TestWaSessionReady(t *testing.T) {
	s, mock := newTestServer(t)
	waSessionStub(t, s, "ready", "628123")
	app := waConnectApp(t, s)
	h := hSABare(t, s)

	expectSAAuth(mock, 0)
	res := doReq(t, app, "GET", "/api/wa/session", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["connected"])
	require.Equal(t, "ready", data["status"])
	require.Equal(t, "Kantor Pusat", data["session"])
	require.Equal(t, "628123", data["phone"])
	require.NotContains(t, data, "qr_code")
}

func TestWaSessionQrReady(t *testing.T) {
	s, mock := newTestServer(t)
	waSessionStub(t, s, "qr_ready", "")
	app := waConnectApp(t, s)
	h := hSABare(t, s)

	expectSAAuth(mock, 0)
	res := doReq(t, app, "GET", "/api/wa/session", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, false, data["connected"])
	require.Equal(t, "qr_ready", data["status"])
	require.Equal(t, "data:image/png;base64,AAA", data["qr_code"])
}

func TestWaSessionUnreachable(t *testing.T) {
	s, mock := newTestServer(t)
	s.cfg.OpenwaBaseURL = "http://127.0.0.1:1"
	app := waConnectApp(t, s)
	h := hSABare(t, s)

	expectSAAuth(mock, 0)
	res := doReq(t, app, "GET", "/api/wa/session", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, false, data["connected"])
	require.Equal(t, "unreachable", data["status"])
	require.NotContains(t, data, "qr_code")
}

func TestWaSessionMissing(t *testing.T) {
	s, mock := newTestServer(t)
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			io.WriteString(w, `[]`)
		case "/api/sessions/kidversa-test":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	app := waConnectApp(t, s)
	h := hSABare(t, s)

	expectSAAuth(mock, 0)
	res := doReq(t, app, "GET", "/api/wa/session", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, false, data["connected"])
	require.Equal(t, "missing", data["status"])
}

// OpenWA's path validation rejects a non-UUID id with 400 before it ever
// looks the session up. A name-based OPENWA_SESSION with no matching session
// therefore reports 400, not 404 — it is still a configuration error
// ("missing"), never an outage.
func TestWaSessionStateBadRequestIsMissing(t *testing.T) {
	s, _ := newTestServer(t)
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"message":"Validation failed (uuid is expected)","error":"Bad Request","statusCode":400}`)
	})

	_, err := s.waSessionState(context.Background(), "not-a-uuid")
	require.ErrorIs(t, err, errWaMissing)
}

func TestWaSessionMissingWhenIdIsNotAUUID(t *testing.T) {
	s, mock := newTestServer(t)
	var listHits atomic.Int32
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			listHits.Add(1)
			io.WriteString(w, `[]`)
		case "/api/sessions/kidversa-test":
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"message":"Validation failed (uuid is expected)","error":"Bad Request","statusCode":400}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	app := waConnectApp(t, s)
	h := hSABare(t, s)

	expectSAAuth(mock, 0)
	res := doReq(t, app, "GET", "/api/wa/session", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, false, data["connected"])
	require.Equal(t, "missing", data["status"])
	require.Equal(t, int32(2), listHits.Load(),
		"a non-UUID id must invalidate the cached id and re-resolve once")
}

func TestWaSessionStartCreated(t *testing.T) {
	s, mock := newTestServer(t)
	var stateHits, startHits atomic.Int32
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			io.WriteString(w, `[{"id":"uuid-1","name":"kidversa-test"}]`)
		case "/api/sessions/uuid-1":
			if stateHits.Add(1) == 1 {
				io.WriteString(w, `{"id":"uuid-1","name":"Kantor Pusat","status":"created"}`)
				return
			}
			io.WriteString(w, `{"id":"uuid-1","name":"Kantor Pusat","status":"qr_ready"}`)
		case "/api/sessions/uuid-1/start":
			startHits.Add(1)
			w.WriteHeader(http.StatusCreated)
		case "/api/sessions/uuid-1/qr":
			io.WriteString(w, `{"qrCode":"data:image/png;base64,AAA","status":"qr_ready"}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	app := waConnectApp(t, s)
	h := hSABare(t, s)

	expectSAAuth(mock, 0)
	res := doReq(t, app, "POST", "/api/wa/session/start", "{}", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, int32(1), startHits.Load(), "created must start the session exactly once")
	require.Equal(t, "qr_ready", data["status"])
	require.Equal(t, "data:image/png;base64,AAA", data["qr_code"])
}

func TestWaSessionStartReadySkipsStart(t *testing.T) {
	s, mock := newTestServer(t)
	var startHits atomic.Int32
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			io.WriteString(w, `[{"id":"uuid-1","name":"kidversa-test"}]`)
		case "/api/sessions/uuid-1":
			io.WriteString(w, `{"id":"uuid-1","name":"Kantor Pusat","status":"ready","phone":"628123"}`)
		case "/api/sessions/uuid-1/start":
			startHits.Add(1)
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	app := waConnectApp(t, s)
	h := hSABare(t, s)

	expectSAAuth(mock, 0)
	res := doReq(t, app, "POST", "/api/wa/session/start", "{}", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, int32(0), startHits.Load(), "a ready session must not be restarted")
	require.Equal(t, true, data["connected"])
	require.Equal(t, "628123", data["phone"])
}

func TestWaSessionStartQrReadySkipsStart(t *testing.T) {
	s, mock := newTestServer(t)
	var startHits atomic.Int32
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			io.WriteString(w, `[{"id":"uuid-1","name":"kidversa-test"}]`)
		case "/api/sessions/uuid-1":
			io.WriteString(w, `{"id":"uuid-1","name":"Kantor Pusat","status":"qr_ready"}`)
		case "/api/sessions/uuid-1/start":
			startHits.Add(1)
			w.WriteHeader(http.StatusCreated)
		case "/api/sessions/uuid-1/qr":
			io.WriteString(w, `{"qrCode":"data:image/png;base64,AAA","status":"qr_ready"}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	app := waConnectApp(t, s)
	h := hSABare(t, s)

	expectSAAuth(mock, 0)
	res := doReq(t, app, "POST", "/api/wa/session/start", "{}", h)
	requireDataEnvelope(t, res)
	require.Equal(t, int32(0), startHits.Load(), "an in-progress pairing must not be restarted")
}

func TestWaSessionStartActionRequiredRetry(t *testing.T) {
	s, mock := newTestServer(t)
	var startHits atomic.Int32
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			io.WriteString(w, `[{"id":"uuid-1","name":"kidversa-test"}]`)
		case "/api/sessions/uuid-1":
			io.WriteString(w, `{"id":"uuid-1","name":"Kantor Pusat","status":"action_required","lastError":"logged out"}`)
		case "/api/sessions/uuid-1/start":
			startHits.Add(1)
			w.WriteHeader(http.StatusCreated)
		case "/api/sessions/uuid-1/qr":
			w.WriteHeader(http.StatusBadRequest)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	app := waConnectApp(t, s)
	h := hSABare(t, s)

	expectSAAuth(mock, 0)
	res := doReq(t, app, "POST", "/api/wa/session/start", `{"retry":true}`, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, int32(1), startHits.Load(), "explicit Retry may start an action_required session")
	require.Equal(t, "logged out", data["last_error"])
}

func TestWaSessionStartActionRequiredNoRetry(t *testing.T) {
	s, mock := newTestServer(t)
	var startHits atomic.Int32
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			io.WriteString(w, `[{"id":"uuid-1","name":"kidversa-test"}]`)
		case "/api/sessions/uuid-1":
			io.WriteString(w, `{"id":"uuid-1","name":"Kantor Pusat","status":"action_required"}`)
		case "/api/sessions/uuid-1/start":
			startHits.Add(1)
			w.WriteHeader(http.StatusCreated)
		case "/api/sessions/uuid-1/qr":
			w.WriteHeader(http.StatusBadRequest)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	app := waConnectApp(t, s)
	h := hSABare(t, s)

	expectSAAuth(mock, 0)
	res := doReq(t, app, "POST", "/api/wa/session/start", "{}", h)
	requireDataEnvelope(t, res)
	require.Equal(t, int32(0), startHits.Load(), "action_required waits for an explicit Retry")
}

func TestWaSessionLogoutReady(t *testing.T) {
	s, mock := newTestServer(t)
	var logoutHits atomic.Int32
	connected := true
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			io.WriteString(w, `[{"id":"uuid-1","name":"kidversa-test"}]`)
		case "/api/sessions/uuid-1":
			if connected {
				io.WriteString(w, `{"id":"uuid-1","name":"Kantor Pusat","status":"ready","phone":"628123"}`)
				return
			}
			io.WriteString(w, `{"id":"uuid-1","name":"Kantor Pusat","status":"disconnected"}`)
		case "/api/sessions/uuid-1/logout":
			if r.Method != http.MethodPost {
				t.Errorf("logout method = %s, want POST", r.Method)
			}
			logoutHits.Add(1)
			connected = false
			io.WriteString(w, `{"id":"uuid-1","status":"disconnected"}`)
		case "/api/sessions/uuid-1/qr":
			w.WriteHeader(http.StatusBadRequest)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	app := waConnectApp(t, s)
	h := hSABare(t, s)

	expectSAAuth(mock, 0)
	res := doReq(t, app, "POST", "/api/wa/session/logout", "{}", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, int32(1), logoutHits.Load(), "logout must be proxied exactly once")
	require.Equal(t, false, data["connected"])
	require.Equal(t, "disconnected", data["status"])
	require.NotContains(t, data, "phone", "the unlinked phone must be cleared")
}

// 400 (not started) and 409 (Baileys has no linked device) both mean there is
// nothing left to unlink; the endpoint must stay a harmless no-op.
func TestWaSessionLogoutAlreadyDisconnectedIsIdempotent(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusConflict} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			s, mock := newTestServer(t)
			var logoutHits atomic.Int32
			waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/sessions":
					io.WriteString(w, `[{"id":"uuid-1","name":"kidversa-test"}]`)
				case "/api/sessions/uuid-1":
					io.WriteString(w, `{"id":"uuid-1","name":"Kantor Pusat","status":"disconnected"}`)
				case "/api/sessions/uuid-1/logout":
					logoutHits.Add(1)
					w.WriteHeader(code)
				case "/api/sessions/uuid-1/qr":
					w.WriteHeader(http.StatusBadRequest)
				default:
					t.Errorf("unexpected path %q", r.URL.Path)
					http.NotFound(w, r)
				}
			})
			app := waConnectApp(t, s)
			h := hSABare(t, s)

			expectSAAuth(mock, 0)
			res := doReq(t, app, "POST", "/api/wa/session/logout", "{}", h)
			data := requireDataEnvelope(t, res).(map[string]any)
			require.Equal(t, int32(1), logoutHits.Load())
			require.Equal(t, false, data["connected"])
			require.Equal(t, "disconnected", data["status"])
		})
	}
}

// A failed unlink is reported through the re-read state, not an error
// envelope: if the session is still ready the modal knows the action failed.
func TestWaSessionLogoutUpstreamFailureKeepsConnectedState(t *testing.T) {
	s, mock := newTestServer(t)
	var logoutHits atomic.Int32
	waOpenWAStub(t, s, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			io.WriteString(w, `[{"id":"uuid-1","name":"kidversa-test"}]`)
		case "/api/sessions/uuid-1":
			io.WriteString(w, `{"id":"uuid-1","name":"Kantor Pusat","status":"ready","phone":"628123"}`)
		case "/api/sessions/uuid-1/logout":
			logoutHits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	app := waConnectApp(t, s)
	h := hSABare(t, s)

	expectSAAuth(mock, 0)
	res := doReq(t, app, "POST", "/api/wa/session/logout", "{}", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, int32(1), logoutHits.Load())
	require.Equal(t, true, data["connected"], "a failed unlink must not claim the phone was removed")
	require.Equal(t, "628123", data["phone"])
}

func TestWaSessionLogoutUnreachable(t *testing.T) {
	s, mock := newTestServer(t)
	s.cfg.OpenwaBaseURL = "http://127.0.0.1:1"
	app := waConnectApp(t, s)
	h := hSABare(t, s)

	expectSAAuth(mock, 0)
	res := doReq(t, app, "POST", "/api/wa/session/logout", "{}", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, false, data["connected"])
	require.Equal(t, "unreachable", data["status"])
}

func TestWaConnectRoleGuard(t *testing.T) {
	s, mock := newTestServer(t)
	app := waConnectApp(t, s)

	t.Run("GET is forbidden for ADMIN", func(t *testing.T) {
		expectAdminAuth(mock)
		res := doReq(t, app, "GET", "/api/wa/session", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})

	t.Run("GET is forbidden for FASILITATOR", func(t *testing.T) {
		expectFASAuth(mock)
		res := doReq(t, app, "GET", "/api/wa/session", "", hFAS(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})

	t.Run("POST is forbidden for ADMIN", func(t *testing.T) {
		expectAdminAuth(mock)
		res := doReq(t, app, "POST", "/api/wa/session/start", "{}", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})

	t.Run("POST is forbidden for FASILITATOR", func(t *testing.T) {
		expectFASAuth(mock)
		res := doReq(t, app, "POST", "/api/wa/session/start", "{}", hFAS(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})

	t.Run("POST is allowed for SA", func(t *testing.T) {
		expectSAAuth(mock, 0)
		res := doReq(t, app, "POST", "/api/wa/session/start", "{}", hSABare(t, s))
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, "unreachable", data["status"])
	})

	t.Run("POST logout is forbidden for ADMIN", func(t *testing.T) {
		expectAdminAuth(mock)
		res := doReq(t, app, "POST", "/api/wa/session/logout", "{}", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})

	t.Run("POST logout is forbidden for FASILITATOR", func(t *testing.T) {
		expectFASAuth(mock)
		res := doReq(t, app, "POST", "/api/wa/session/logout", "{}", hFAS(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})

	t.Run("POST logout is allowed for SA", func(t *testing.T) {
		expectSAAuth(mock, 0)
		res := doReq(t, app, "POST", "/api/wa/session/logout", "{}", hSABare(t, s))
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, false, data["connected"])
	})
}

# OpenWA Connect Modal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a superadmin start the configured OpenWA session and pair WhatsApp from a dashboard modal, while admins keep a read-only status badge.

**Architecture:** Two new SA-only Fiber endpoints proxy OpenWA's session state and QR behind the existing HTTP-200 envelope; a new React modal starts the session, renders the QR, and polls only while open. The existing `/api/wa/status` and `wa.changed` watcher move from gateway health to the session's `ready` state so the badge and modal agree.

**Tech Stack:** Go 1.26 / Fiber v3 / testify / sqlmock; React 19 / Ant Design / i18next / Vitest 5 / MSW 2.15.

**Spec:** `docs/superpowers/specs/2026-11-09-openwa-connect-modal-design.md`

## Global Constraints

- Every `/api/*` response is HTTP 200 with `{data}` or `{error:{code,message}}`; never 401/403/404/409/422/429.
- `GET /api/wa/status` stays `s.authed("ADMIN")`; the two connect routes are `s.authed("SA")`.
- The OpenWA API key and internal URL never reach the browser; `last_error` carries only OpenWA's `lastError`, never a raw Go transport error.
- OpenWA calls use the existing `s.http` client and a 5 s budget per call (10 s for a handler that makes two calls, 15 s for start + re-read).
- All user-facing strings live in the `common` namespace in both `id` and `en`.
- Unit tests only; no smoke/E2E steps. Compile/static checks accompany the unit suite: `go build ./...`, `go test ./internal/...`, `npm run build`, `npm run lint`, `npx vitest run`.
- Test semantics matter, not snippet whitespace; run `gofmt` on Go edits and let Prettier/oxlint settle formatting.
- The design doc is the source of truth for behaviour; this plan names the exact files, signatures, and assertions.

## Review Focus

These are the failure modes most likely to bite an operator; each has a test in the owning task.

1. **OpenWA unreachable / timeout** — the modal must show `unreachable`, never a 5xx or a blank page. (Task 2 backend, Task 4 frontend.)
2. **Configured session missing (404)** — must show `missing`, not `unreachable`; it is a config error. (Task 2 backend, Task 4 frontend.)
3. **Already linked (`ready`)** — must not call start, must show connected + phone, and must not render a QR. (Task 2 backend, Task 4 frontend.)
4. **Pairing already in progress (`qr_ready` / `authenticating`)** — must not call start; the last QR must survive a transient empty poll. (Task 2 backend, Task 4 frontend.)
5. **Admin click** — must not open the modal or call the connect endpoints, while the status popover keeps working. (Task 5 frontend.)

---

### Task 1: Backend OpenWA session primitives

**Files:**
- Modify: `backend/internal/envelope.go` (Server struct, around line 274)
- Modify: `backend/internal/wa.go`
- Create: `backend/internal/wa_connect_test.go`

**Interfaces:**
- Consumes: `s.cfg.OpenwaBaseURL`, `s.cfg.OpenwaAPIKey`, `s.cfg.OpenwaSession`, `s.http`, `s.now()`.
- Produces:
  - `type waSession struct { ID, Name, Status, Phone, LastError string }` with JSON tags `id`, `name`, `status`, `phone`, `lastError`.
  - `var errWaMissing error`
  - `func (s *Server) waResolveSession(ctx context.Context) (string, error)`
  - `func (s *Server) waSessionState(ctx context.Context, id string) (waSession, error)`
  - `func (s *Server) waQR(ctx context.Context, id string) (string, error)`
  - `func (s *Server) waStart(ctx context.Context, id string) error`
  - `func (s *Server) invalidateWaID()`

- [ ] **Step 1: Add the session-id cache fields to `Server`**

In `backend/internal/envelope.go`, add to the `Server` struct next to the other `wa`/hub fields:

```go
	// wa.go: cached name → id resolution for the configured OpenWA session.
	waMu   sync.Mutex
	waID   string
	waIDAt time.Time
```

- [ ] **Step 2: Write the failing primitive tests**

Create `backend/internal/wa_connect_test.go` with these tests (use `httptest`, `atomic.Int32`, `require`):

```go
func TestWaResolveSessionMatchesName(t *testing.T) {
	s, _ := newTestServer(t)
	var listHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions" {
			t.Errorf("unexpected path %q", r.URL.Path)
			return
		}
		listHits.Add(1)
		io.WriteString(w, `[{"id":"uuid-1","name":"kidversa-test","status":"ready"}]`)
	}))
	t.Cleanup(srv.Close)
	s.cfg.OpenwaBaseURL = srv.URL
	s.cfg.OpenwaSession = "kidversa-test"
	s.cfg.OpenwaAPIKey = "k"

	id, err := s.waResolveSession(context.Background())
	require.NoError(t, err)
	require.Equal(t, "uuid-1", id)
	id2, err := s.waResolveSession(context.Background())
	require.NoError(t, err)
	require.Equal(t, "uuid-1", id2)
	require.Equal(t, int32(1), listHits.Load(), "the resolved id must be cached")
}
```

Also write:

- `TestWaResolveSessionFallsBackToConfiguredValue` — `/api/sessions` answers 500; expect `kidversa-test`, no error.
- `TestWaSessionStateNotFound` — `/api/sessions/uuid-1` answers 404; expect `errors.Is(err, errWaMissing)`.
- `TestWaQRNoCodeIsEmpty` — `/api/sessions/uuid-1/qr` answers 400; expect `""`, nil.
- `TestWaStartAlreadyStartedIsOK` — `POST .../start` answers 400; expect nil.
- `TestWaStartServerError` — `POST .../start` answers 500; expect a non-nil error.

- [ ] **Step 3: Run the primitive tests to verify they fail**

Run: `cd backend && go test ./internal/ -run 'TestWa(Resolve|SessionState|QR|Start)' -v`
Expected: FAIL with undefined `waResolveSession`, `waSessionState`, `waQR`, `waStart`, `errWaMissing`.

- [ ] **Step 4: Implement the primitives in `backend/internal/wa.go`**

Add imports `errors`, `net/url`, `strings` if not present. Implement:

```go
const waSessionCacheTTL = 5 * time.Minute

var errWaMissing = errors.New("openwa session not found")

type waSession struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Phone     string `json:"phone"`
	LastError string `json:"lastError"`
}
```

- `waResolveSession`: return the cached id when `s.now().Sub(s.waIDAt) < waSessionCacheTTL`; otherwise `GET /api/sessions`, decode either a top-level array or `{"sessions":[...]}`; match `Name == s.cfg.OpenwaSession` and cache `ID`; on any list failure or no match, cache and return `s.cfg.OpenwaSession`.
- `waSessionState`: `GET /api/sessions/` + `url.PathEscape(id)`; 404 → `errWaMissing`; non-2xx → wrapped error; decode `waSession`.
- `waQR`: `GET .../qr`; 400 → `""`, nil; non-2xx → error; decode `{qrCode string}`.
- `waStart`: `POST .../start`; 400 → nil; non-2xx → error.
- `invalidateWaID`: lock, clear `waID`/`waIDAt`.
- Every request sets `X-API-Key: s.cfg.OpenwaAPIKey`.

- [ ] **Step 5: Run the primitive tests to verify they pass**

Run: `cd backend && go test ./internal/ -run 'TestWa(Resolve|SessionState|QR|Start)' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/envelope.go backend/internal/wa.go backend/internal/wa_connect_test.go
git commit -m "feat(wa): add OpenWA session resolution and QR/start primitives"
```

---

### Task 2: Backend connect endpoints and payload

**Files:**
- Modify: `backend/internal/wa.go`
- Modify: `backend/cmd/api/main.go:107`
- Modify: `backend/internal/helpers_test.go:107`
- Modify: `backend/internal/integration/app_test.go:100`
- Modify: `backend/internal/wa_connect_test.go`

**Interfaces:**
- Consumes: everything from Task 1.
- Produces:
  - `type waStatus struct { Connected bool; Status, Session, Phone, QRCode, LastError, CheckedAt string }` with JSON tags `connected`, `status,omitempty`, `session,omitempty`, `phone,omitempty`, `qr_code,omitempty`, `last_error,omitempty`, `checked_at,omitempty`.
  - `type waStartRequest struct { Retry bool `json:"retry"` }`
  - `func (s *Server) waLoadSession(ctx context.Context) (string, waSession, error)`
  - `func (s *Server) waErrorStatus(err error) waStatus`
  - `func (s *Server) waStatusFor(ctx context.Context, id string, sess waSession) waStatus`
  - `func waStartable(status string, retry bool) bool`
  - `func (s *Server) handleWaSession(c fiber.Ctx) error`
  - `func (s *Server) handleWaSessionStart(c fiber.Ctx) error`
  - `func (s *Server) RegisterWa(api fiber.Router)`

- [ ] **Step 1: Write the failing endpoint tests**

Append to `backend/internal/wa_connect_test.go` a helper and the endpoint tests:

```go
func waConnectApp(t *testing.T, s *Server) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	s.RegisterWa(app.Group("/api"))
	return app
}

func waConnectServer(t *testing.T, s *Server, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	s.cfg.OpenwaBaseURL = srv.URL
	s.cfg.OpenwaSession = "kidversa-test"
	s.cfg.OpenwaAPIKey = "k"
}
```

Write these tests:

- `TestWaSessionReady` — list → `[{"id":"uuid-1","name":"kidversa-test"}]`; state → `{"id":"uuid-1","name":"Kantor Pusat","status":"ready","phone":"628123"}`. `GET /api/wa/session` as `hSABare` after `expectSAAuth(mock, 0)`; assert `connected:true`, `status:"ready"`, `session:"Kantor Pusat"`, `phone:"628123"`, and `require.NotContains(data, "qr_code")`.
- `TestWaSessionQrReady` — state `qr_ready`; `/qr` → `{"qrCode":"data:image/png;base64,AAA","status":"qr_ready"}`; assert `qr_code` equals that string.
- `TestWaSessionUnreachable` — set `s.cfg.OpenwaBaseURL = "http://127.0.0.1:1"`; assert `connected:false`, `status:"unreachable"`, no `qr_code`.
- `TestWaSessionMissing` — list → `[]`, state → 404; assert `status:"missing"`.
- `TestWaSessionStartCreated` — first state `created`, after `POST .../start` (201) state `qr_ready`; count start hits and assert exactly 1; assert `status:"qr_ready"` and `qr_code` present.
- `TestWaSessionStartReadySkipsStart` — state `ready`; POST with body `{}`; assert start hits 0 and `connected:true`.
- `TestWaSessionStartQrReadySkipsStart` — state `qr_ready`; POST with body `{}`; assert start hits 0.
- `TestWaSessionStartActionRequiredRetry` — state `action_required`; POST body `{"retry":true}`; assert start hits 1.
- `TestWaSessionStartActionRequiredNoRetry` — state `action_required`; POST body `{}`; assert start hits 0.
- `TestWaConnectRoleGuard` — `GET /api/wa/session` with `hAdmin` → `FORBIDDEN_ROLE`; with `hFAS` → `FORBIDDEN_ROLE`; `POST /api/wa/session/start` with `hAdmin` → `FORBIDDEN_ROLE`; with `hSABare` + `expectSAAuth(mock, 0)` → data envelope.

Each SA test must queue `expectSAAuth(mock, 0)` before the request. Use `doReq(t, app, "GET", "/api/wa/session", "", hSABare(t, s))` and `doReq(t, app, "POST", "/api/wa/session/start", "{}", hSABare(t, s))`.

- [ ] **Step 2: Run the endpoint tests to verify they fail**

Run: `cd backend && go test ./internal/ -run 'TestWaSession|TestWaConnectRoleGuard' -v`
Expected: FAIL with undefined `RegisterWa`, `handleWaSession`, `handleWaSessionStart`.

- [ ] **Step 3: Implement the payload and handlers in `backend/internal/wa.go`**

Implement exactly these shapes:

```go
type waStatus struct {
	Connected bool   `json:"connected"`
	Status    string `json:"status,omitempty"`
	Session   string `json:"session,omitempty"`
	Phone     string `json:"phone,omitempty"`
	QRCode    string `json:"qr_code,omitempty"`
	LastError string `json:"last_error,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
}

type waStartRequest struct {
	Retry bool `json:"retry"`
}
```

- `waLoadSession`: resolve → state; on `errWaMissing`, `invalidateWaID()`, re-resolve and re-read once; return the final id/state/error.
- `waErrorStatus`: `errWaMissing` → `{Status:"missing", Session:s.cfg.OpenwaSession}`; anything else → `{Status:"unreachable"}`.
- `waStatusFor`: fill `Connected` from `sess.Status == "ready"`, `Status`, `Session` (fallback `s.cfg.OpenwaSession`), `Phone`, `LastError`, `CheckedAt` (`s.now().UTC().Format(time.RFC3339)`); when not connected, call `waQR` and set `QRCode` only when it returns a non-empty string and no error.
- `waStartable`: true for `created`, `disconnected`, `failed`; `action_required` only when `retry` is true; false otherwise.
- `handleWaSession`: 10 s context; `waLoadSession`; on error `ok(c, waErrorStatus(err))`; else `ok(c, waStatusFor(ctx, id, sess))`.
- `handleWaSessionStart`: bind `waStartRequest` with `bindBody` (return nil when it fails); 15 s context; `waLoadSession`; on error `ok(c, waErrorStatus(err))`; if `waStartable(sess.Status, req.Retry)`, call `waStart`, and on success re-read the state (`waSessionState`) before returning; finally `ok(c, waStatusFor(ctx, id, sess))`.
- `RegisterWa` mounts the three routes:

```go
func (s *Server) RegisterWa(api fiber.Router) {
	api.Get("/wa/status", s.authed("ADMIN"), s.handleWaStatus)
	api.Get("/wa/session", s.authed("SA"), s.handleWaSession)
	api.Post("/wa/session/start", s.authed("SA"), s.handleWaSessionStart)
}
```

Add a handler comment on `handleWaSessionStart`: this is a control/proxy action with an ephemeral response, so it deliberately does not use `s.idempotent`; the client's `X-Idempotency-Key` is ignored.

- [ ] **Step 4: Replace the old registrar at every call site**

Change `s.RegisterWaStatus(api)` to `s.RegisterWa(api)` in:
- `backend/cmd/api/main.go:107`
- `backend/internal/helpers_test.go:107`
- `backend/internal/integration/app_test.go:100`
- the `inxWaApp` helper in `backend/internal/infra_extra_test.go` (around line 1277)

Remove the old `RegisterWaStatus` function.

- [ ] **Step 5: Run the endpoint tests to verify they pass**

Run: `cd backend && go test ./internal/ -run 'TestWaSession|TestWaConnectRoleGuard' -v`
Expected: PASS.

- [ ] **Step 6: Run the full backend unit suite**

Run: `cd backend && go test ./internal/...`
Expected: PASS (the `/wa/status` alignment in Task 3 may still be pending; if the existing status tests fail here, continue to Task 3 before treating the suite as green).

- [ ] **Step 7: Commit**

```bash
git add backend/internal/wa.go backend/internal/wa_connect_test.go backend/cmd/api/main.go backend/internal/helpers_test.go backend/internal/integration/app_test.go backend/internal/infra_extra_test.go
git commit -m "feat(wa): add SA-only OpenWA session and start endpoints"
```

---

### Task 3: Align the badge and watcher to session `ready`

**Files:**
- Modify: `backend/internal/wa.go`
- Modify: `backend/internal/infra_extra_test.go` (the WA section, around lines 1152–1457)
- Modify: `backend/internal/integration/flows_test.go` (`startWAStub`, around lines 234–253)

**Interfaces:**
- Produces: `func (s *Server) waConnected(ctx context.Context) bool`.
- Removes: `waReady`, `waSessionName` (and their tests).

- [ ] **Step 1: Update `TestInxHandleWaStatus` stubs**

In `backend/internal/infra_extra_test.go`, replace the `"ready OpenWA answers..."` stub with one that serves the session endpoints:

```go
case "/api/sessions":
	io.WriteString(w, `[{"id":"kidversa-test","name":"Sesi Cerdas","status":"ready"}]`)
case "/api/sessions/kidversa-test":
	io.WriteString(w, `{"id":"kidversa-test","name":"Sesi Cerdas","status":"ready"}`)
```

Keep the existing assertions (`connected:true`, `session:"Sesi Cerdas"`, `checked_at` non-empty). Add a sibling subtest `"qr_ready is not connected"` with the same stub but `status:"qr_ready"`; assert `connected:false` and `session:"Sesi Cerdas"`.

- [ ] **Step 2: Update the watcher test stub**

In `TestInxStartWaWatcherPollsPublishesAndStops`, replace the `/api/health/ready` stub with a session stub whose status flips from `disconnected` to `ready` on the second session-state hit:

```go
var stateHits atomic.Int32
srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/sessions":
		io.WriteString(w, `[{"id":"kidversa-test","name":"Kidversa"}]`)
	case "/api/sessions/kidversa-test":
		if stateHits.Add(1) == 1 {
			io.WriteString(w, `{"status":"disconnected"}`)
			return
		}
		io.WriteString(w, `{"status":"ready"}`)
	default:
		t.Errorf("unexpected path %q", r.URL.Path)
		http.NotFound(w, r)
	}
}))
```

Keep the baseline-no-publish assertion and the `wa.changed{connected:true}` transition assertion. Adjust the final hit count to match `stateHits` (two state reads) rather than the old health count.

- [ ] **Step 3: Update the integration stub**

In `backend/internal/integration/flows_test.go`, add an exact `/api/sessions` handler and make the session-info handler return `status:"ready"`:

```go
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
	_, _ = rw.Write([]byte(`{"id":"kidversa-test","name":"Kidversa","status":"ready"}`))
})
```

`TestIntegrationWAStatusReachable` keeps expecting `connected:true` and `session:"Kidversa"`.

- [ ] **Step 4: Delete the obsolete helper tests**

Delete `TestInxWaReady` and `TestInxWaSessionName` from `backend/internal/infra_extra_test.go`.

- [ ] **Step 5: Run the affected tests to verify they fail**

Run: `cd backend && go test ./internal/ -run 'TestInxHandleWaStatus|TestInxStartWaWatcher' -v`
Expected: FAIL — the current code still probes `/api/health/ready`, so the new stubs return 404/empty status and the assertions fail.

- [ ] **Step 6: Implement the alignment in `backend/internal/wa.go`**

- Add `waConnected(ctx) bool`: `waLoadSession(ctx)`; return `err == nil && sess.Status == "ready"`.
- Rewrite `handleWaStatus`: 10 s context; `waLoadSession`; on error return `ok(c, fiber.Map{"connected": false})`; otherwise return `fiber.Map{"connected": sess.Status == "ready", "session": name, "checked_at": ...}` where `name` falls back to `s.cfg.OpenwaSession`.
- Rewrite `startWaWatcher` to call `up := s.waConnected(context.Background())` instead of `s.waReady()`.
- Delete `waReady` and `waSessionName`.

- [ ] **Step 7: Run the affected tests to verify they pass**

Run: `cd backend && go test ./internal/ -run 'TestInxHandleWaStatus|TestInxStartWaWatcher' -v`
Expected: PASS.

- [ ] **Step 8: Run the full backend unit and integration suites**

Run: `cd backend && go test ./internal/...`
Run: `cd backend && go test ./internal/integration/... -tags=integration`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add backend/internal/wa.go backend/internal/infra_extra_test.go backend/internal/integration/flows_test.go
git commit -m "refactor(wa): derive badge state from the OpenWA session status"
```

---

### Task 4: Frontend connect modal

**Files:**
- Modify: `frontend/src/lib/api.ts` (`get`, around line 257)
- Create: `frontend/src/components/WaConnectModal.tsx`
- Modify: `frontend/src/locales/id/common.json` (`wa` block, around line 54)
- Modify: `frontend/src/locales/en/common.json` (`wa` block, around line 54)
- Modify: `frontend/src/index.css` (WA section, around line 790)
- Create: `frontend/src/components/WaConnectModal.test.tsx`

**Interfaces:**
- Produces:
  - `api.get<T>(path: string, params?: ListParams, opts?: Pick<CallOptions, 'signal'>): Promise<T>`
  - `export interface WaSessionState { connected: boolean; status: string; session?: string; phone?: string; qr_code?: string; last_error?: string; checked_at?: string }`
  - `export default function WaConnectModal({ open, onClose }: { open: boolean; onClose: () => void })`

- [ ] **Step 1: Extend `api.get` with an optional abort signal**

Change the `get` function to:

```ts
function get<T>(path: string, params?: ListParams, opts: Pick<CallOptions, 'signal'> = {}): Promise<T> {
  return send<T>('GET', path, { params, ...opts })
}
```

The existing two-argument calls stay valid.

- [ ] **Step 2: Add the i18n keys**

In both `frontend/src/locales/id/common.json` and `frontend/src/locales/en/common.json`, extend the existing `wa` object with exactly these keys (ID first, EN second):

| key | id | en |
|-----|----|----|
| `connectTitle` | Hubungkan WhatsApp | Connect WhatsApp |
| `connectIntro` | Pilih cara menghubungkan WhatsApp. | Choose how to connect WhatsApp. |
| `openOpenwa` | Buka OpenWA | Open OpenWA |
| `openOpenwaDesc` | Buka dasbor OpenWA di tab baru. | Open the OpenWA dashboard in a new tab. |
| `connectDirectly` | Hubungkan Langsung | Connect Directly |
| `connectDirectlyDesc` | Mulai sesi dan tampilkan kode QR di sini. | Start the session and show the QR code here. |
| `scanTitle` | Pindai kode QR | Scan the QR code |
| `scanHint` | Buka WhatsApp di ponsel, lalu pilih Perangkat tertaut → Tautkan perangkat. | Open WhatsApp on your phone, then go to Linked devices → Link a device. |
| `qrAlt` | Kode QR WhatsApp | WhatsApp QR code |
| `statusInitializing` | Menyiapkan… | Preparing… |
| `statusQrReady` | Menunggu pindai | Waiting for scan |
| `statusAuthenticating` | Menghubungkan… | Connecting… |
| `statusReady` | Terhubung | Connected |
| `statusDisconnected` | Terputus | Disconnected |
| `statusFailed` | Gagal terhubung | Connection failed |
| `statusUnreachable` | OpenWA tidak dapat dijangkau | OpenWA is unreachable |
| `statusMissing` | Sesi OpenWA tidak ditemukan | OpenWA session not found |
| `phone` | Nomor tertaut | Linked number |
| `retry` | Coba lagi | Retry |
| `back` | Kembali | Back |
| `close` | Tutup | Close |
| `adminOnly` | Hanya superadmin yang dapat menghubungkan WhatsApp. | Only superadmins can connect WhatsApp. |

- [ ] **Step 3: Write the failing modal tests**

Create `frontend/src/components/WaConnectModal.test.tsx`. Use the repo helpers:

```tsx
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { http } from 'msw'
import WaConnectModal from './WaConnectModal'
import { renderApp } from '../test/render'
import { server } from '../test/setup'
import { makeLoginData, makeUser, ok } from '../test/handlers'

function saSession() {
  return makeLoginData({ user: makeUser({ role: 'SA' }) })
}

const QR_A = 'data:image/png;base64,AAA'
const QR_B = 'data:image/png;base64,BBB'
```

Write these tests:

1. `shows both choices` — render `<WaConnectModal open onClose={() => {}} />` with `saSession()`; assert buttons `Buka OpenWA` and `Hubungkan Langsung`.
2. `Open OpenWA opens :8003 and closes` — spy `window.open`; click `Buka OpenWA`; assert called once with `http://localhost:8003`, `_blank`, `noopener,noreferrer`, and `onClose` called.
3. `Connect Directly starts the session and shows the QR` — mock `POST /api/wa/session/start` → `ok({connected:false, status:'qr_ready', qr_code:QR_A})`; click `Hubungkan Langsung`; assert an `img` named `Kode QR WhatsApp` with `src=QR_A`.
4. `polls, refreshes the QR, then shows connected + phone` — fake only the timer functions (`vi.useFakeTimers({ toFake: ['setTimeout','clearTimeout','setInterval','clearInterval'] })`); `POST` → `qr_ready` + `QR_A`; `GET /api/wa/session` returns `qr_ready` + `QR_B` on the first call and `ready` + `phone:'628123'` on the second; after the first 2000 ms advance assert `src=QR_B`; after the second assert `Terhubung` and `628123`, and no QR img; advance another 6000 ms and assert the GET call count did not grow.
5. `already ready shows connected + phone with no QR and no polling` — `POST` → `{connected:true, status:'ready', phone:'628123'}`; assert `Terhubung`, `628123`, no QR img; advance 5000 ms and assert `GET /api/wa/session` was never called.
6. `unreachable shows the error and Retry` — `POST` → `{connected:false, status:'unreachable'}`; assert `OpenWA tidak dapat dijangkau` and a `Coba lagi` button.
7. `missing shows the session-not-found copy and Retry` — `POST` → `{connected:false, status:'missing'}`; assert `Sesi OpenWA tidak ditemukan` and `Coba lagi`.
8. `closing stops polling` — render a small harness with `open` state; start a poll, close via the `Tutup` button, advance 10 s, assert the GET count stayed put.
9. `Back returns to the choice view` — start connect, click `Kembali`, assert both choice buttons are visible again.

- [ ] **Step 4: Run the modal tests to verify they fail**

Run: `cd frontend && npx vitest run src/components/WaConnectModal.test.tsx`
Expected: FAIL — `WaConnectModal` does not exist.

- [ ] **Step 5: Implement `WaConnectModal.tsx`**

Use `Modal`, `Button`, `Spin` from antd and `useTranslation('common')`. State: `view: 'choice' | 'connect'`, `state: WaSessionState | null`, `error: string | null`. On `open` becoming true, reset to `choice`.

- Choice view: intro text, a full-width **Open OpenWA** button that calls `window.open(`${window.location.protocol}//${window.location.hostname}:8003`, '_blank', 'noopener,noreferrer')` then `onClose()`, and a primary **Connect Directly** button that calls `connect()`.
- `connect(retry = false)`: set `view='connect'`, clear `error`, `api.post<WaSessionState>('/api/wa/session/start', retry ? { retry: true } : {})`, set `state`; on failure set `error = errorCode(cause)`.
- Connect view: `role="status" aria-live="polite"` status line; a `Spin` for `initializing` / `authenticating`; the QR `<img className="kv-wa-qr" src={state.qr_code} alt={t('wa.qrAlt')} />` plus `scanTitle`/`scanHint` when a QR exists; `phone` when connected; `last_error` as a meta line; an `errors.${error}` alert when the API call itself failed; a `Back` button; a primary `Retry` button when status is `unreachable`, `missing`, `failed`, `action_required`, or `disconnected` (calls `connect(true)`); a `Close` button.
- Status label mapping: `ready → wa.statusReady`, `qr_ready → wa.statusQrReady`, `initializing → wa.statusInitializing`, `authenticating → wa.statusAuthenticating`, `disconnected → wa.statusDisconnected`, `failed/action_required → wa.statusFailed`, `unreachable → wa.statusUnreachable`, `missing → wa.statusMissing`, default `wa.statusDisconnected`.
- Poll effect: run only when `open && view === 'connect' && state && !state.connected`. Use a `setTimeout` chain (wait for the response, then schedule the next 2000 ms), an `AbortController` passed to `api.get('/api/wa/session', undefined, { signal })`, a `cancelled` flag, and cleanup that sets `cancelled`, aborts, and clears the timer. Merge `qr_code` with the previous value (`next.qr_code ?? prev?.qr_code`) so a transient empty poll keeps the last QR. Stop scheduling when `next.connected` is true.

- [ ] **Step 6: Add the CSS**

In `frontend/src/index.css`, after the existing `.kv-wa-detail` rule, add `.kv-wa-modal`, `.kv-wa-choice`, `.kv-wa-qr` (max-width ~240px, image-rendering pixelated), and `.kv-wa-status` using the existing `--kv-*` tokens. Keep the badge's `.kv-wa.is-locked` rule for Task 5.

- [ ] **Step 7: Run the modal tests to verify they pass**

Run: `cd frontend && npx vitest run src/components/WaConnectModal.test.tsx`
Expected: PASS.

- [ ] **Step 8: Run lint and type-check**

Run: `cd frontend && npm run lint`
Run: `cd frontend && npm run build`
Expected: both succeed.

- [ ] **Step 9: Commit**

```bash
git add frontend/src/lib/api.ts frontend/src/components/WaConnectModal.tsx frontend/src/components/WaConnectModal.test.tsx frontend/src/locales/id/common.json frontend/src/locales/en/common.json frontend/src/index.css
git commit -m "feat(web): add the OpenWA connect modal with in-app QR pairing"
```

---

### Task 5: Badge role gating and modal integration

**Files:**
- Modify: `frontend/src/components/WaStatusBadge.tsx`
- Modify: `frontend/src/components/WaStatusBadge.test.tsx`

**Interfaces:**
- Consumes: `WaConnectModal` from Task 4.
- Produces: the badge opens the modal only for `SA`; `ADMIN` keeps the popover and gets an inert click.

- [ ] **Step 1: Update the badge tests**

In `frontend/src/components/WaStatusBadge.test.tsx`:

- Add a `saSession()` helper next to `adminSession()`:
  ```ts
  function saSession() {
    return makeLoginData({ user: makeUser({ role: 'SA' }) })
  }
  ```
- Replace the existing `opens the OpenWA dashboard in a new tab on click` test with two tests:
  - `SA click opens the connect modal` — render with `saSession()`, click the badge, assert the modal title `Hubungkan WhatsApp` is visible and `window.open` was not called.
  - `ADMIN click does nothing` — render with `adminSession()`, click the badge, assert `screen.queryByText('Hubungkan WhatsApp')` is null and `window.open` was not called.
- Add `ADMIN badge is marked disabled and keeps its popover` — render with `adminSession()`, assert the badge has `aria-disabled="true"` and a `title`; hover it and assert the `.kv-wa-detail` popover still appears (the existing `hoverOpen` helper).
- Keep the facilitator-hidden test and the SSE/popover tests unchanged.

- [ ] **Step 2: Run the badge tests to verify they fail**

Run: `cd frontend && npx vitest run src/components/WaStatusBadge.test.tsx`
Expected: FAIL — the click still calls `window.open` for both roles.

- [ ] **Step 3: Implement the gating in `WaStatusBadge.tsx`**

- Import `WaConnectModal`.
- Add `const isSA = user?.role === 'SA'` and `const [connectOpen, setConnectOpen] = useState(false)`.
- Change the button:
  - `className={`kv-wa ${stateClass}${isSA ? '' : ' is-locked'}`}`
  - `aria-disabled={!isSA}`
  - `title={isSA ? undefined : t('wa.adminOnly')}`
  - `onClick={() => { if (isSA) setConnectOpen(true) }}`
- Remove the `window.open` call from the badge.
- Wrap the return in a fragment and render `<WaConnectModal open={connectOpen} onClose={() => setConnectOpen(false)} />` after the `Popover`.
- Add `.kv-wa.is-locked { cursor: not-allowed; opacity: 0.65; }` to `frontend/src/index.css` (if not already added in Task 4).

- [ ] **Step 4: Run the badge tests to verify they pass**

Run: `cd frontend && npx vitest run src/components/WaStatusBadge.test.tsx`
Expected: PASS.

- [ ] **Step 5: Run the full frontend unit suite**

Run: `cd frontend && npx vitest run`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/components/WaStatusBadge.tsx frontend/src/components/WaStatusBadge.test.tsx frontend/src/index.css
git commit -m "feat(web): restrict the OpenWA connect modal to superadmins"
```

---

### Task 6: Update the binding spec documents

**Files:**
- Modify: `docs/spec/02-ARCHITECTURE.md`
- Modify: `docs/spec/03-FEATURES.md`
- Modify: `docs/spec/04-FLOWS.md`
- Modify: `docs/spec/05-DATA.md`
- Modify: `docs/spec/06-ERRORS-AND-EDGE-CASES.md`
- Modify: `docs/spec/08-ACCEPTANCE-CRITERIA.md`
- Modify: `docs/spec/SPEC-INDEX.md`

**Interfaces:** none (documentation).

- [ ] **Step 1: Find every stale statement**

Run: `grep -RniE "never in our SPA|ONLY in OpenWA|health/ready|QR pairing" docs/spec`
Expected: the round-6 lines in `03-FEATURES.md` (~413–420), `04-FLOWS.md` (~416–418), `08-ACCEPTANCE-CRITERIA.md` (~256), `SPEC-INDEX.md` (~194–201), and the `wa.changed` descriptions in `02-ARCHITECTURE.md` §2c, `05-DATA.md`, and `08-ACCEPTANCE-CRITERIA.md`.

- [ ] **Step 2: Update each document**

- `03-FEATURES.md` §F-13: replace the "QR pairing/activation happens ONLY in OpenWA dashboard, never in our SPA" sentence with: SA-only connect modal; `POST /api/wa/session/start` + `GET /api/wa/session`; ADMIN sees status only; QR lives only in the modal.
- `04-FLOWS.md` §FL-14: replace the click-opens-dashboard sentence with the choice modal and the Connect Directly → start → QR → ready flow.
- `02-ARCHITECTURE.md` §2b/§2c/§7: add the two endpoints and the SA guard; change the `wa.changed` description from `/api/health/ready` reachability to the session's `ready` state.
- `05-DATA.md`: if the `wa.changed {connected}` catalogue entry says gateway reachability, change it to "phone linked".
- `06-ERRORS-AND-EDGE-CASES.md`: add the connect cases — unreachable, `missing` session, start 400, QR 400, already ready, `action_required`.
- `08-ACCEPTANCE-CRITERIA.md`: replace the "QR pairing ONLY in OpenWA dashboard" criterion with "QR pairing is SA-only and may happen in the connect modal"; update the `wa.changed` drop case to "phone unlinked".
- `SPEC-INDEX.md`: update the round-6 locked-decision note so it no longer says QR pairing remains OpenWA-dashboard-only.

- [ ] **Step 3: Re-run the stale-statement search**

Run: `grep -RniE "never in our SPA|ONLY in OpenWA|health/ready" docs/spec`
Expected: no matches that still describe the old behaviour (health probes may remain where they refer to infrastructure, not `wa.changed`).

- [ ] **Step 4: Commit**

```bash
git add docs/spec
git commit -m "docs(spec): allow SA-only in-app OpenWA QR pairing"
```

---

### Task 7: Final verification

**Files:** none.

- [ ] **Step 1: Backend build and tests**

Run: `cd backend && go build ./...`
Run: `cd backend && go test ./internal/...`
Run: `cd backend && go test ./internal/integration/... -tags=integration`
Expected: all pass.

- [ ] **Step 2: Frontend lint, type-check, and tests**

Run: `cd frontend && npm run lint`
Run: `cd frontend && npm run build`
Run: `cd frontend && npx vitest run`
Expected: all pass.

- [ ] **Step 3: Confirm the security boundary**

Run: `grep -RniE "OPENWA_API_KEY|OpenwaAPIKey" frontend/src`
Expected: no matches (the key never reaches the browser).

Run: `grep -n "authed(\"SA\")" backend/internal/wa.go`
Expected: both connect routes carry the SA guard; the status route carries `authed("ADMIN")`.

- [ ] **Step 4: Commit any verification fixes**

If a step required a fix, commit it with a focused message; otherwise there is nothing to commit.

## Self-Review Notes

- **Spec coverage:** role gating (Tasks 2, 5), two-option modal (Task 4), start-on-open (Tasks 2, 4), already-ready skip (Tasks 2, 4), QR refresh/poll (Task 4), Open OpenWA new tab (Task 4), API-key isolation (Tasks 2, 7), spec reversal (Task 6).
- **Type consistency:** `waStatus`, `waSession`, `WaSessionState`, `RegisterWa`, and `api.get`'s optional signal are used with the same names across tasks.
- **Review Focus:** every listed failure mode has a backend and/or frontend test in its owning task.
- **Proportion:** the plan names signatures and assertions; bodies are left to the implementer except where the contract (JSON tags, route guards, poll merge) is exact.

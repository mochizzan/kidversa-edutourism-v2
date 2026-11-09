# OpenWA Connect Modal — Design

- **Date:** 2026-11-09
- **Status:** Draft (awaiting approval)
- **Topic:** Dashboard WhatsApp badge → SA-only connect modal with in-app QR pairing

## 1. Intent and why

Today the dashboard's WhatsApp badge is a dead end for connecting. `WaStatusBadge`
shows connected/disconnected and, on click, opens the external OpenWA dashboard at
`host:8003` in a new tab. Pairing a phone therefore leaves the product entirely,
and any `SA`/`ADMIN` who can see the badge can start that flow.

The superadmin wants to connect OpenWA from inside the dashboard, without
handing the browser the OpenWA API key or sending the operator to another tab.
The same badge should also stop being an action for non-superadmins: `ADMIN`
keeps seeing the status, but cannot initiate a connection.

This reverses the current locked decision in `docs/spec` (round 6) that QR
pairing happens **only** in the OpenWA dashboard, never in our SPA. That
decision must be updated with this change.

## 2. Goals

1. Only `SA` can open the connect modal; `ADMIN` still sees the badge and its
   status popover, but clicking does nothing.
2. Clicking the badge opens a modal with exactly two choices:
   - **Open OpenWA** — existing behaviour: open `host:8003` in a new tab.
   - **Connect Directly** — start the configured OpenWA session, then show the
     OpenWA QR code and the live connection status in the modal.
3. The QR auto-refreshes while the modal is open (WhatsApp rotates it).
4. The status updates until the session becomes `ready`; once ready, the modal
   shows **Connected** and the linked phone number instead of a QR.
5. The OpenWA API key never leaves the server. The browser talks only to our
   `/api` envelope endpoints.
6. Update the binding spec documents so the SPA-QR reversal is explicit.

## 3. Non-goals

- No pairing-code (phone-number) flow; QR only for this iteration.
- No create/delete/logout/stop-session controls in the SPA.
- No multi-session picker; the configured `OPENWA_SESSION` is the only session.
- No changes to `send-text`, outbox, consent sending, or report delivery.
- No new SSE event and no page-level background polling. The modal's refresh
  loop exists only while the modal is open.
- No database rows for QR/status; nothing is cached in the idempotency store.

## 4. Anti-goals (would make this a failure)

- Exposing `OPENWA_API_KEY` or the internal `http://wa:2785` URL to the browser.
- Letting `ADMIN` or `FASILITATOR` start a session or obtain a QR.
- A poll that keeps running after the modal closes.
- Treating a failed/unreachable OpenWA as a 5xx or `WA_FAILED` top-level error
  in a way that hides the connection state from the modal.
- Storing a QR (which is a short-lived credential) anywhere durable.

## 5. Constraints

- **Transport:** every `/api/*` response stays HTTP 200 with `{data}` or
  `{error:{code,message}}` (`envelope.go`). No 401/403/404/409/422/429.
- **Auth:** connect endpoints use `s.authed("SA")`. The existing
  `GET /api/wa/status` stays `s.authed("ADMIN")` because `ADMIN` still reads it.
- **OpenWA contract** (v0.29.1 docs):
  - `GET /api/sessions` — list sessions.
  - `GET /api/sessions/{id}` — session state: `status`, `phone`, `lastError`.
  - `POST /api/sessions/{id}/start` — start/resume; `400` if already started.
  - `GET /api/sessions/{id}/qr` — `{qrCode, status}`; `400` when no QR is ready
    or the session is already linked.
  - Header `X-API-Key`; 5 s budget per call.
- **OpenWA statuses:** `created`, `initializing`, `qr_ready`, `authenticating`,
  `ready`, `action_required`, `disconnected`, `failed`.
- **i18n:** all user-facing strings in `common` namespace, ID + EN.
- **Tests:** co-located Go tests and Vitest tests; build/lint must stay green.

## 6. Decisions already made (by the superadmin)

| # | Decision |
|---|----------|
| 1 | `SA` only can click; `ADMIN` keeps the badge visible but the click is inert. |
| 2 | **Connect Directly** immediately starts the OpenWA session and displays the QR. |
| 3 | If the session is already `ready`, skip starting, show **Connected** + phone, hide the QR. |
| 4 | **Open OpenWA** keeps opening `host:8003` in a new tab. |
| 5 | QR auto-refreshes while the modal is open; status keeps updating until ready. |
| 6 | The "QR only in OpenWA dashboard" spec decision is reversed for this SA-only flow. |

## 7. Design

### 7.1 Backend (`backend/internal/wa.go`)

Extend the existing WA vertical slice. Rename the registrar from
`RegisterWaStatus` to `RegisterWa` (update `cmd/api/main.go`) and mount:

| Method | Path | Guard | Purpose |
|--------|------|-------|---------|
| `GET` | `/api/wa/status` | `ADMIN` (unchanged) | Badge detail popover |
| `GET` | `/api/wa/session` | `SA` | Poll current session state + QR |
| `POST` | `/api/wa/session/start` | `SA` | Start/resume, then return state + QR |

**Response shape** (both connect endpoints):

```json
{
  "data": {
    "connected": false,
    "status": "qr_ready",
    "session": "kidversa",
    "phone": "",
    "qr_code": "data:image/png;base64,...",
    "last_error": "",
    "checked_at": "2026-11-09T10:00:00Z"
  }
}
```

- Empty fields are omitted.
- `connected` is true only when OpenWA's session `status == "ready"`.
- `status` is the raw OpenWA status, or one of two synthetic values:
  - `"unreachable"` — the OpenWA gateway cannot be reached.
  - `"missing"` — OpenWA answered but the configured session does not exist
    (a 404 after re-resolve). This is a configuration error, not an outage.
  The UI maps every value to copy; the raw value stays available for
  diagnostics.
- `qr_code` is OpenWA's PNG data URL, passed through unchanged.
- `last_error` carries **only** OpenWA's own session `lastError` field. Raw Go
  transport errors are never copied into the payload — they can contain the
  internal `http://wa:2785` URL (anti-goal #1). A transport failure reports
  `status:"unreachable"` with an empty `last_error`.
- Connect endpoints never emit `WA_FAILED`: an upstream failure is reported in
  `status` / `last_error` so the modal can render the state. This matches the
  existing "status is never an error" rule for `/api/wa/status`.

**Helpers:**

- `waResolveSession(ctx) (id, name string, err error)`
  - `GET /api/sessions`, match `name == cfg.OpenwaSession`; if no match, treat
    the configured value as the id (backwards-compatible with a UUID config).
  - Cache the resolved id in `Server` under a mutex for ~5 minutes; invalidate
    on a 404 from a session call and re-resolve once.
- `waSessionState(ctx, id) (waSession, error)` — `GET /api/sessions/{id}`.
- `waQR(ctx, id) (string, error)` — `GET /api/sessions/{id}/qr`; a `400` means
  "no QR yet", not an error.
- `waStart(ctx, id) error` — `POST /api/sessions/{id}/start`; a `400`
  ("already started") is treated as success.
- `waStatusPayload(ctx) payload` — shared assembly used by `GET /api/wa/status`,
  `GET /api/wa/session`, and `POST /api/wa/session/start`.

**`GET /api/wa/session` behaviour:** resolve → fetch session state → if not
`ready`, fetch QR (ignore 400) → return payload.

**`POST /api/wa/session/start` behaviour:** resolve → fetch state → start only
when the current status is `created`, `disconnected`, or `failed` (400 = already
started, treated as success) → fetch state + QR → return payload. Do **not**
call start while a pairing is already in progress (`initializing`, `qr_ready`,
`authenticating`), and do not call it on `ready`; those states only need a
state/QR read. `action_required` also skips the automatic start (it needs a
human), but the modal's explicit **Retry** button may call start. A repeated
click is safe.

**Badge alignment (my call):** change `GET /api/wa/status` and the
`wa.changed` watcher to derive `connected` from the session's `ready` status,
not only from `GET /api/health/ready`. Gateway health and WhatsApp linkage are
different things; the badge should say "Connected" only when a phone is linked.
`/api/wa/status` keeps its current shape (`connected`, `session`, `checked_at`);
unreachable still answers `{connected:false}`. Existing tests/stubs get updated.

**Idempotency:** `POST /api/wa/session/start` is a control/proxy action with an
ephemeral response, not a stored mutation. It intentionally does not use
`s.idempotent`; the client's automatic `X-Idempotency-Key` header is ignored.
Document this exception in the handler comment.

**Timeouts / limits:** 5 s per OpenWA call (same as `waReady`). No extra rate
limiter: the route is SA-only and the modal is short-lived. If the OpenWA list
call is unavailable, fall back to the configured session value so a UUID config
still works.

### 7.2 Frontend

**`WaStatusBadge.tsx`**

- `const isSA = user?.role === 'SA'`.
- Add `const [connectOpen, setConnectOpen] = useState(false)`.
- Keep the hover/focus `Popover` and the on-demand `GET /api/wa/status` detail
  for both `SA` and `ADMIN`.
- Button click: if `isSA`, open `WaConnectModal`; otherwise do nothing.
- Use `aria-disabled={!isSA}` plus a `title` explaining that connecting is
  superadmin-only, instead of a native `disabled` attribute, so the status
  popover still works for `ADMIN`. Add `.kv-wa.is-locked` (cursor + muted) for
  `ADMIN`.
- Remove the direct `window.open` from the badge; it moves into the modal.

**New `WaConnectModal.tsx`**

- Props: `{ open: boolean; onClose: () => void }`.
- State: `view: 'choice' | 'connect'`, `state: WaSessionState | null`,
  `error: string | null`.
- On open, reset to `choice`.
- **Choice view:** two prominent buttons:
  - **Open OpenWA** → `window.open(host:8003, '_blank', 'noopener,noreferrer')`,
    then close.
  - **Connect Directly** → switch to `connect`, `POST /api/wa/session/start`,
    render the response, and start the modal-scoped refresh loop.
- **Connect view:**
  - Status line: dot + text, never colour-only; `aria-live="polite"`.
  - `qr_code` present → `<img src={qr_code} alt={t('wa.qrAlt')}>` plus scan
    instructions.
  - `connected` → **Connected** + phone number; no QR.
  - `initializing` / `authenticating` → spinner + "Connecting…".
  - `disconnected` / `failed` / `action_required` → status text + `last_error`
    (when present) + **Retry** (re-runs the start call).
  - `unreachable` → "OpenWA is unreachable" + **Retry**.
  - `missing` → "OpenWA session not found" + **Retry** (and a hint to check
    `OPENWA_SESSION`).
  - **Back** returns to the choice view and stops the loop.
- **Refresh loop:** while `open && view === 'connect' && !connected`, poll
  `GET /api/wa/session` **sequentially** — wait for each response, then schedule
  the next call 2 s later (a fixed `setInterval` would stack requests, since one
  poll can take up to ~10 s). Pass an `AbortController` signal and abort the
  in-flight request on Back, close, and unmount. A response with a `qr_code`
  replaces the displayed QR; a response without one (transient `400` /
  not-yet-ready) **keeps the last QR on screen** so it does not flicker
  mid-scan. The QR is cleared only when the session becomes `ready`. Stop on
  `connected`, on Back, and on close/unmount. No timer exists outside the open
  modal.
- Use antd `Modal` + `Button`; no new page/route.

**i18n (`common` namespace, ID + EN):** add `wa.connectTitle`,
`wa.connectIntro`, `wa.openOpenwa`, `wa.openOpenwaDesc`, `wa.connectDirectly`,
`wa.connectDirectlyDesc`, `wa.scanTitle`, `wa.scanHint`, `wa.qrAlt`,
`wa.statusInitializing`, `wa.statusQrReady`, `wa.statusAuthenticating`,
`wa.statusReady`, `wa.statusDisconnected`, `wa.statusFailed`,
`wa.statusUnreachable`, `wa.statusMissing`, `wa.phone`, `wa.retry`, `wa.back`,
`wa.close`, `wa.adminOnly`.

**CSS (`frontend/src/index.css`):** add `.kv-wa.is-locked`, `.kv-wa-modal`,
`.kv-wa-choice`, `.kv-wa-qr`, `.kv-wa-status`. Follow existing CSS-variable
tokens and BEM-ish naming.

### 7.3 Spec updates

Update the binding documents so the reversal is not silently contradicted:

- `docs/spec/03-FEATURES.md` §F-13 — WA status paragraph: SA-only connect
  modal, in-app QR, ADMIN status-only.
- `docs/spec/04-FLOWS.md` §FL-14 — click flow and QR pairing steps.
- `docs/spec/02-ARCHITECTURE.md` §2b/§2c/§7 — new endpoints, SA guard, QR
  proxy, API key stays server-side, and the `wa.changed` watcher now derives
  `connected` from the session's `ready` state rather than gateway reachability.
- `docs/spec/08-ACCEPTANCE-CRITERIA.md` — replace "QR pairing ONLY in OpenWA
  dashboard, never in SPA" with the new SA-only rule; update the `wa.changed`
  drop-case description (it is now "phone unlinked", not "gateway down").
- `docs/spec/SPEC-INDEX.md` — update the locked-decision note (round 6) that
  says QR pairing remains OpenWA-dashboard-only.
- `docs/spec/05-DATA.md` — update the `wa.changed {connected}` catalogue entry
  if it describes the value as gateway reachability.
- `docs/spec/06-ERRORS-AND-EDGE-CASES.md` — add the connect edge cases
  (unreachable, session `missing`, start 400, QR 400, already ready,
  `action_required`).

### 7.4 Tests

**Backend (co-located, `httptest` OpenWA stub):**

- `GET /api/wa/session`: ready → `connected:true` + phone, no QR; `qr_ready` →
  QR data URL; `initializing` → no QR; unreachable → `connected:false` +
  `status:"unreachable"`; session 404 → `status:"missing"`; `400` from QR → no
  `qr_code`.
- `POST /api/wa/session/start`: `created` → start called once, then state
  returned; `ready` → start not called; start `400` → treated as success;
  repeated call safe.
- Role matrix: `SA` allowed; `ADMIN` and `FASILITATOR` → `FORBIDDEN_ROLE` on
  the two connect routes; `ADMIN` still allowed on `GET /api/wa/status`.
- Session resolution: name match via list; UUID fallback; list failure fallback;
  404 invalidates the cached id.
- `GET /api/wa/status` alignment: session `ready` → `connected:true`; session
  `qr_ready` → `connected:false`; unreachable → `{connected:false}`.

**Frontend (Vitest + MSW):**

- `WaStatusBadge.test.tsx`: SA click opens the modal (no `window.open` yet);
  ADMIN click does nothing but the popover still fetches; FAS hidden.
- New `WaConnectModal.test.tsx`:
  - choice view shows both options;
  - Open OpenWA calls `window.open('http://localhost:8003', ...)`;
  - Connect Directly POSTs `/api/wa/session/start` and renders the QR;
  - polling `GET /api/wa/session` updates the QR and flips to Connected +
    phone when `ready`;
  - already-ready response shows Connected + phone with no QR and no polling;
  - unreachable shows the error state + Retry;
  - closing stops the timer (advance timers, assert no further calls);
  - Back returns to the choice view.
- Keep the existing popover/SSE tests green after the badge refactor.

### 7.5 Rollout and risks

- **`OPENWA_SESSION` name vs UUID:** `.env` sets `kidversa`, which looks like a
  name; the OpenWA docs use UUIDs in paths. The new resolver lists sessions and
  matches by name, falling back to the configured value as an id. `sendText`
  is left untouched in this iteration; if production sends are already working
  with the name, no change is needed. Verify at runtime before shipping.
- **OpenWA version drift:** the contract above is from v0.29.1 docs; the image
  is `openwa-openwa:latest`. If a response shape differs, the handler degrades
  to `status:"unreachable"`/`connected:false` rather than failing the page.
- **QR is a credential:** never log it, never store it, never put it in an SSE
  frame. It lives only in the modal's memory.
- **Start side effect:** a start call boots the OpenWA engine. Treating `400`
  as success and checking `ready` first keeps repeated clicks harmless.
- **Polling:** 2 s while the modal is open is a deliberate, scoped exception to
  the "no blind poll" rule, needed because WhatsApp rotates the QR. The badge
  itself stays event-driven.

## 8. Left to the builder

- Exact component split, CSS, and copy.
- Poll interval within 2–3 s.
- Whether to show the raw OpenWA status in a small meta line in addition to the
  mapped label (recommended).
- Whether the modal closes after **Open OpenWA** (recommended: yes).

## 9. Calls made during the design review

These were left open by the first draft and settled here; they are not
superadmin decisions, so check them while reading:

1. **`last_error` is OpenWA-only.** Never echo a raw Go transport error (it
   contains the internal OpenWA URL).
2. **Add `status:"missing"`.** A 404 for the configured session is a config
   error, not an outage, so it gets its own status and copy.
3. **Start only from `created` / `disconnected` / `failed`.** Do not disturb an
   in-progress pairing (`initializing` / `qr_ready` / `authenticating`) or a
   `ready` session; `action_required` waits for an explicit Retry.
4. **Keep the last QR when a poll omits it.** Only clear it when the session
   becomes `ready`, so a transient `400` does not blank the code mid-scan.
5. **Sequential poll with abort.** Wait for each response, then 2 s; abort the
   in-flight request on Back/close/unmount.
6. **Doc scope includes the SSE catalogue.** `02` §2c, `05`, and `08` also
   describe `wa.changed` as gateway reachability and must move to "phone
   linked" alongside the badge change.

## 10. Open questions

None blocking. The superadmin decisions in §6 settle the product behaviour.

## 11. Post-implementation review (2026-11-09)

The final whole-branch review found no critical defects. Two Important items
were fixed before merge, plus two trivial follow-ups folded into the same
pass:

- **Deadline sizing.** Both connect handlers now size their outer deadline to
  the worst-case upstream call count (4 for `GET /api/wa/session`, 5 for
  `POST /api/wa/session/start`) instead of 2x/3x a single call. Otherwise a
  slow `missing` path could expire mid-sequence and be reported as
  `unreachable`.
- **QR retention pinned by a test.** A poll that omits `qr_code` must leave
  the previous code on screen (verified RED before the fix, GREEN after).
- **Guard matrix closed.** The role test now also covers FAS on
  `POST /api/wa/session/start`.
- **Stale header refreshed.** The badge test header no longer claims the
  click opens the OpenWA dashboard.

Deferred deliberately (low risk; revisit if the deployment changes):

1. **Open OpenWA uses the page protocol** (`http(s)://<host>:8003`). This
   matches the pre-existing badge and the local HTTP deployment; a
   TLS-served SPA would open `https://...:8003` against a plain-HTTP OpenWA.
2. **New lint warning** `react(set-state-in-effect)` on the modal's
   reset-on-open effect - the same class already present across the repo.
3. **`url` shadows `net/url`** in `sendText` (function-scoped; compiles).
4. **`waResolveSession` never returns an error today**; callers keep the
   error branch as future-proofing.
5. **Handlers use `context.Background()`**, not `c.Context()`: a client
   abort on close does not cancel the in-flight upstream call (bounded by
   the per-call budget).
6. **Back keeps the previous state**, so re-entering Connect Directly can
   flash the old QR until the new POST resolves.
7. **`07-BEFORE-AFTER.md`** was left untouched (historical change log).

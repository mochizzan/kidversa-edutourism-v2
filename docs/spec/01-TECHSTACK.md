# 01 — Tech Stack

Status: DRAFT (2026-10-09; adding test strategy + A4 fixed report; pending audit)

Legend: REQUIRED = locked decision. Latest-verified API names are used
verbatim so implementers do not guess. Sources checked 2026-10-08:
go.dev, docs.gofiber.io (+`next` v3 branch), vite.dev, react.dev,
reactrouter.com, ant.design, i18next/react-i18next docs, html-to-image,
jsPDF, open-wa.org, ai.google.dev (Interactions API).

## 1. Runtime (REQUIRED)

- Go 1.26.6 (`go 1.26` directive minimum; Fiber v3 requires Go 1.26+).
- Stdlib only beyond §3: `database/sql`, `context`, `net/http` (OpenWA +
  Gemini clients), `net/smtp` (reset mail), `crypto/rand` (32-byte hex
  tokens), `crypto/sha256` + `crypto/subtle` (reset-code hash + compare),
  `os` (`os.Getenv` only), `log/slog`, `encoding/json`,
  `mime/multipart`, `path/filepath`, `strings`, `time`, `math/rand/v2`
  (WA batch jitter 3–10 s).

## 2. Backend framework (REQUIRED): Fiber v3

- Install/import: `go get github.com/gofiber/fiber/v3`;
  `github.com/gofiber/fiber/v3` (+ `/middleware/*`, `/extractors`).
- Bootstrap: `app := fiber.New()` → groups/routes → `app.Listen(":3000")`.
- `Ctx` is an interface. Input parsing MUST use `c.Bind()` only:
  `c.Bind().Body` (JSON/form), `.Query` (query string), `.URI` (route
  params), `.Header`, `.Cookie`, `.All` (precedence URI→Body→Query→
  Headers→Cookies) — verified v3 Bind API; standalone `c.Params()`/
  `c.Query()` convenience methods are NOT relied upon (v3 moved request
  accessors and their surface is uncertain). Other used members:
  `FormFile`, `FormValue`, `IP`, `Path`, `OriginalURL`, `Hostname`,
  `Method`, `Get`, `Cookies`, `c.Status().JSON`, `SendString`, `SendFile`,
  `SendStatus`, `Set`, `ClearCookie`, `Redirect`, `Next`, `Route`,
  `Endpoint`, `Matched`, `Locals`, `Value`, `App`, `Context`,
  `SetContext`, `Elapsed`, `RequestID`, `fiber.NewError`, `fiber.Map`.
- Transport rule (binding user decision): every `/api/*` and `/public/*`
  handler returns HTTP 200 with `{data}` or `{error:{code,message}}`.
  HTTP 500 is reserved for unhandled panics/boot failures caught by
  `recover`/custom `ErrorHandler` (envelope `{error:{code:"INTERNAL"}}`).
  Rate limiting also returns 200 + `Retry-After` header (never 429).
  EXCEPTION (round 7, binding): `GET /api/stream` and
  `GET /public/stream?token=` are NOT envelopes — they are
  `text/event-stream` (HTTP 200, held open). Handshake failures
  (bad token, wrong role, tenant mismatch, revoked public token) return
  a NORMAL 200 envelope (`INVALID_TOKEN` / `FORBIDDEN_ROLE` /
  `TENANT_MISMATCH` / `TENANT_INACTIVE` / `TOKEN_INVALID`) and the stream
  is never opened. Once opened, frames are signals only
  (`{entity, entity_id, rev}` — no PII, no bytes, no tokens, no codes);
  all DATA still flows through 200-envelope REST refetch.
- Custom headers (binding): `X-Tenant-ID` (active tenant, every
  authenticated call), `X-Idempotency-Key` (mutating POSTs: register,
  session create, WA single/batch, consent send, public-link
  create/regenerate, photo upload, narrative generate, public consent
  respond, copy-data execute).
- Middleware (all `fiber/v3/middleware/*`, order binding):
  `recover` → `requestid` → `logger`
  (format `${ip} ${port} ${status} ${method} ${path} ${error} ${latency} ${pid} ${requestid}`
  with `CustomTags: map[string]logger.LogFunc{"requestid": func(o logger.Buffer, c fiber.Ctx, d logger.Data, e string) (int, error) { return o.WriteString(c.RequestID()) }}`
  — REQUIRED in v3 because requestid lives in context (unexported key), NOT in `c.Locals`;
  `requestid` MUST be mounted before logger) → `cors` (explicit `AllowOrigins` from
  `CORS_ALLOWED_ORIGINS`; `AllowCredentials:true` never with `"*"`) →
  per-route `limiter` (`Max`, `Expiration`, `KeyGenerator` = the NAT-safe
  composite function from §limiter-policy — e.g. `IP+lowercased-email`
  for auth, `IP+token` for public — NOT bare `c.IP()` (bare IP collapses
  school-NAT buckets; the snippet `KeyGenerator=c.IP()` seen in older
  drafts is base-only and MUST be replaced by the composite),
  `FixedWindow`, in-process;
  default limiter rejection = HTTP 429, so EVERY instance MUST set
  `LimitReached` handler returning `c.Status(200).Set(Retry-After).JSON(
  {error:{code:RATE_LIMITED}})` — otherwise the HTTP-200 transport rule
  breaks; `TrustProxy` MUST be
  `fiber.Config{TrustProxy:true, ProxyHeader:fiber.HeaderXForwardedFor, TrustProxyConfig:{Proxies:["127.0.0.1","10.0.0.0/8","172.16.0.0/12","192.168.0.0/16"]}}`
  (nginx is the only proxy; without this `c.IP()` sees the proxy IP and limiter buckets collapse)) →
  `keyauth` (`extractors.FromAuthHeader("Bearer")` for access,
  `extractors.FromCookie("refresh_token")` for refresh only — both from
  `github.com/gofiber/fiber/v3/extractors`; field is `keyauth.Config{
  Extractor: …}` (v3 replaced string `KeyLookup`); default
  keyauth failure = HTTP 401 + `WWW-Authenticate`, so BOTH instances MUST
  set a custom `ErrorHandler` returning `c.Status(200).JSON({error:{code:
  INVALID_TOKEN}})` for access / `{code:INVALID_TOKEN}` (cookie missing or
  jti revoked/expired) for refresh — otherwise the HTTP-200 transport rule
  breaks) →
  `RequireRole` → tenant guard (`X-Tenant-ID` vs JWT claim;
  mismatch → 200 + `TENANT_MISMATCH`) → handler.
- CORS (binding): `AllowOrigins` = EXACT list from
  `CORS_ALLOWED_ORIGINS` (comma-separated, 1–2 origins; MVP: the single
  `PUBLIC_WEB_BASE_URL` origin — e.g. `https://rapor.kidversa.id` prod /
  `http://localhost:8002` local; never `"*"` with credentials).
  `AllowMethods:[GET,POST,PUT,PATCH,DELETE,OPTIONS]`,
  `AllowHeaders:[Authorization,Content-Type,X-Tenant-ID,X-Idempotency-Key]`,
  `ExposeHeaders:[Retry-After]`, `MaxAge:600`. Requests arrive
  same-origin via nginx, so browsers send no preflight; CORS only guards
  direct `api:3000` access and future split-hosting.
- Limiter policy (binding, tunable constants; NAT-safe keys — schools
  share one public IP, so auth limits key on IP+email, public limits on
  IP+token): `POST /api/auth/register` 5/min/IP; `POST /api/auth/login`
  10/min per IP+lowercased-email; `POST /api/auth/forgot` 3/15min per
  IP+email; `POST /api/auth/reset` 5/15min per IP+email;
  `POST /api/auth/refresh` 30/min/IP; `GET /api/auth/tenants` 60/min/IP;
  `GET /public/*` 60/min per
  IP+token; `POST /public/consent/respond` 10/min per IP+token (the ONLY
  counter that increments on respond — respond is EXEMPT from the generic
  `GET /public/*` bucket, never double-counted);
  `POST P/send-report` 5/min/IP (P =
  `/api/sessions/:sid/topics/:stid/children/:cid` — per-topic op);
  `POST /api/sessions/:sid/batch-send` 2/min/IP;
  `POST /api/sessions/:sid/children/:cid/send-consent` 5/min/IP
  (session-scoped, NOT under `P`);
  `GET /api/sessions/:sid/children/:cid/copy-candidates` 30/min/IP;
  `POST /api/sessions/:sid/children/:cid/copy` 5/min/IP (copy executes
  disk IO). All other mutating routes (tenants/users/masters/children/
  session/groups/members/assessments/photos/narratives/missions/report/
  public-link create) are INTENTIONALLY unlimited (auth+scope+gates are
  the protection; adding limiters there is a new Change, not a silent
  addition). Exceeding returns 200
  `RATE_LIMITED` + `Retry-After` header (never HTTP 429).
  Stream exemption (round 7, binding): `GET /api/stream` and
  `GET /public/stream?token=` are NEVER rate-limited (a held-open
  connection is not a request); abuse is bounded by concurrent-connection
  caps (single table, binding): max 100 per `api` replica + max 6 per IP
  + max 20 per tenant — enforced at handshake, excess handshake → 200
  `RATE_LIMITED` + `Retry-After` (no separate handshake limiter exists;
  earlier "handshake limiter" wording is superseded by these caps).
- `healthcheck`: `app.Get(healthcheck.LivenessEndpoint, healthcheck.New())`
  + readiness (DB probe) + startup. `static`: `static.New(root,
  static.Config{Browse:false, Download:false, CacheDuration:10*time.Second})`
  for `/uploads/*` (signature `static.New(root string, cfg ...Config)`;
  note: `app.Static()` was removed in v3). Idempotency is manual via
  `idempotency_keys` table (NOT Fiber's idempotency middleware — replay
  must return the recorded envelope incl. per-item WA statuses).
- NOT used: `csrf`, `session`, `cache`, global `compress`, `proxy`,
  `favicon`, `basicauth`, `encryptcookie`, `monitor`, `timeout`.
  `middleware/sse` IS used — transport pieces ONLY (`sse.New`,
  `sse.Config{Handler, Retry, OnClose}`, `stream.Event(sse.Event{Name,
  Data})`, `stream.Context()`, `stream.Done()`, `LastEventID`; hub,
  topics, auth, replay = app code in `internal/stream.go`).

## 3. Backend libraries (REQUIRED, 4 packages)

- `github.com/gofiber/fiber/v3` (framework).
- `github.com/go-sql-driver/mysql`: DSN
  `user:pass@tcp(host:3306)/db?parseTime=true&charset=utf8mb4&loc=Local`;
  pool `SetConnMaxLifetime(3*time.Minute)`, `SetMaxOpenConns(10)`,
  `SetMaxIdleConns(10)`.
- `github.com/golang-jwt/jwt/v5`: `jwt.NewWithClaims(HS256)`,
  `SignedString`, `jwt.Parse(..., jwt.WithValidMethods(["HS256"]))`;
  always pin HS256, reject `alg=none`; 60 s leeway.
- `golang.org/x/crypto/bcrypt`: `GenerateFromPassword(DefaultCost)`,
  `CompareHashAndPassword`.
- NOT used: ORM, validator/config/migration/UUID/HTTP/mail libs
  (replaced by `database/sql` + manual `if` + `os.Getenv` + versioned
  `.sql` + `crypto/rand` hex + stdlib clients).

## 4. Frontend base (REQUIRED): Vite + React + TS

- Vite 8 (Rolldown; no custom build options), Node 20.19+/22.12+,
  `npm create vite@latest -- --template react-ts`.
- React 19 (`createRoot`, `useState/Effect/Ref/Context/Callback/Memo`,
  `<Suspense>`; 19.x extras not needed). Capture via browser
  `getUserMedia` + `<canvas>` (not React).
- TS strict (template default).

## 5. Frontend libraries (REQUIRED, 7 npm packages)

- `react-router` v8: `BrowserRouter, Routes, Route, Outlet, Link,
  NavLink, useParams, useNavigate, useSearchParams`. Public pages read
  `?token=` via `useSearchParams`. SPA page routes for public content are
  `/view/report`, `/view/gallery`, `/view/consent` (distinct from the JSON
  API prefix `/public/*`, which nginx proxies to `api:3000`).
- `antd` v5 (Ant Design 5): enterprise-grade React UI library with
  built-in TypeScript definitions (no `@types/antd` needed). Design Token
  system + CSS-in-JS theming via `ConfigProvider` + `theme.token` /
  `theme.components` / `theme.algorithm`. Components: `Layout` (Header/
  Sider), `Menu`, `Table`, `Form`, `Input`, `Select`, `Checkbox`,
  `Button`, `Modal`, `Drawer`, `Tabs`, `Tag`, `Badge`, `Avatar`,
  `Image` (PreviewGroup), `Spin`, `message`, `Alert`, `List`,
  `Pagination`, `Card`, `Descriptions`, `Tooltip`, `Popconfirm`,
  `DatePicker`, `TimePicker`, `Upload`, `Radio`, `Switch`, `Divider`,
  `Space`, `Row`, `Col`, `Grid`. Report screen uses plain CSS (no antd).
- `@ant-design/icons`: SVG icon set for antd components.
- `i18next` + `react-i18next`: local JSON only (no detector/backend);
  `lng:'id'`, `fallbackLng:'id'`; namespaces `common,menu,auth,master,
  session,assessment,report`; `t(key,{count,name})`,
  `i18n.changeLanguage`; persistence `localStorage 'kv-lang'`.
- `html-to-image`: `toPng(node,{cacheBust:true})` only.
- `jspdf`: `new jsPDF({orientation:'p',unit:'mm',format:'a4'})` +
  `addImage(png,'PNG',x,y,w,h)` + `save()`; NEVER `doc.html()`.
- Frontend deps total is 7 npm packages (1 router + 1 antd + 1 icons +
  2 i18next + 1 html-to-image + 1 jspdf). No Tailwind (antd owns styling
  except plain-CSS report).
- NOT used: axios, TanStack, Zustand/Redux, hook-form/zod, charts,
  i18n detector/backend, server PDF libs, MUI (replaced by `fetch` +
  Context + manual forms + CSS bars + local JSON).

## 6. Database (REQUIRED): MariaDB 12.3 LTS (maintained to Jun 2029)

- Image `mariadb:12.3`; `utf8mb4`; InnoDB; UTC datetimes.
- Access via `database/sql` only. Schema rule (binding): zero FK
  constraints — all references loose (`BIGINT UNSIGNED`, app-enforced).
  Snapshots/duplication over normalisation. Every domain row (including
  link and snapshot tables) carries `tenant_id` + `INDEX(tenant_id, …)`.
- Mime whitelist (binding): photos JPG/PNG/WebP; frames PNG/JPG/WebP;
  badges PNG/JPG/WebP (≤2 MB). ICO is rejected everywhere.

## 7. External services (REQUIRED)

- OpenWA (self-hosted): text-only integration. Verified:
  `POST {OPENWA_BASE_URL}/api/sessions/{OPENWA_SESSION}/messages/send-text`
  `{chatId:"628…@c.us", text:"…"}` + `X-API-Key`. OpenWA itself answers
  HTTP 201 `{messageId, timestamp}` — that is a server-to-server response
  parsed by `wa.go`, NOT our API transport (our endpoints still return
  HTTP 200 envelopes mapping `messageId` into per-item status). No media/file payload
  (link-only decision): WA carries rendered Markdown text + 2 public URLs;
  PNG/PDF are client downloads only. Batch = sequential sends with
  `math/rand/v2` jitter 3–10 s, max 10 items per request (client chunks
  larger sets). Per-item `wa_outbox` row (`SENT`/`FAILED`, no `QUEUED`).
  Engine default `whatsapp-web.js`, SQLite, local storage; data under
  `/app/data` ← `./data/openwa`. Raw `net/http`, no SDK.
- Gemini (user-locked 2026-10-08 round 5 from official doc snippet):
  PRIMARY `POST
  https://generativelanguage.googleapis.com/v1beta/models/{GEMINI_MODEL}:generateContent`
  body `{contents:[{parts:[{text: rendered_prompt}]}]}` + headers
  `x-goog-api-key: $GEMINI_API_KEY` + `Content-Type: application/json`
  (15 s timeout). No `interactions` endpoint (removed — unverified,
  not in public Google docs). Default `GEMINI_MODEL=gemini-3.5-flash`
  per user-provided doc (override via `.env`). Key never leaves server.
  Manual retry only (no auto-loop).
- SMTP via `net/smtp` (any plain SMTP; creds in `.env` only).

## 8. Protocols & contracts

- SPA → API: same-origin relative `/api/*` (nginx → `api:3000`);
  `Authorization: Bearer <access>`; `X-Tenant-ID` on every authenticated
  call; `X-Idempotency-Key` on mutating POSTs.
- Cookies: `refresh_token` HttpOnly, `Path=/`, `SameSite=Lax`
  (+`Secure` in prod).
- Env keys (binding; all via `env_file:[.env]`; `.env.example` documents
  the same list with dummy values — never real secrets): `MARIADB_ROOT_PASSWORD,
  MARIADB_DATABASE, MARIADB_USER, MARIADB_PASSWORD, DB_HOST, DB_PORT, APP_PORT,
  CORS_ALLOWED_ORIGINS, JWT_ACCESS_SECRET, JWT_REFRESH_SECRET,
  JWT_ACCESS_TTL_MIN=15, JWT_REFRESH_TTL_DAYS=7, RESET_TOKEN_TTL_MIN=15,
  SMTP_HOST, SMTP_PORT, SMTP_USER, SMTP_PASS, SMTP_FROM, GEMINI_API_KEY,
  GEMINI_MODEL=gemini-3.5-flash, OPENWA_BASE_URL=http://wa:2785,
  OPENWA_API_KEY, OPENWA_SESSION=kidversa, WA_COUNTRY_DEFAULT=62,
  UPLOAD_DIR=/data/uploads, PUBLIC_WEB_BASE_URL, SEED_TENANT_NAME=Bandung,
  SEED_TENANT_TYPE=KOTA, SEED_SA_NAME, SEED_SA_EMAIL, SEED_SA_PASSWORD`.
  Boot fails loud (refuse listen) when a REQUIRED key is missing/empty;
  optional keys fall back to the `=default` shown. No other env key exists.
- Public JSON: `GET /public/report?token=`, `GET /public/gallery?token=`,
  `GET /public/consent?token=`, `POST /public/consent/respond`.
- Public pages (SPA): `/view/report?token=`, `/view/gallery?token=`,
  `/view/consent?token=` (each fetches its JSON above).
- SSE realtime (round 7, binding): `GET /api/stream` (auth pages) +
  `GET /public/stream?token=` (parent pages). Both `text/event-stream`
  (HTTP 200, held open; NOT envelopes). Opened via `fetch` +
  `ReadableStream` reader (NOT native `EventSource` — it cannot send
  `Authorization` / `X-Tenant-ID`); `lib/stream.ts` (~40 lines parser,
  zero new npm deps) parses `event:/data:/id:/retry:/:ping` frames.
  Frame = signal only: `{entity, entity_id, rev}` + routing keys
  (`tenant_id`, `session_id?`) + `action` — NEVER names/notes/photos/
  tokens/codes/bytes. `id` = `rev` string (drives `Last-Event-ID`
  replay). Heartbeat `:ping` 15 s (below nginx 180 s). Reconnect
  backoff 1 s → 30 s max with `Last-Event-ID`; server ring 100
  events / 5 min per tenant (gap unclosable → full-refetch active page).
  Auth pages: `Authorization: Bearer` + `X-Tenant-ID` on handshake
  (same guards as REST); `auth.expiring` at T-60 s → refresh-once →
  reconnect with new Bearer + last id. Public: scope locked to the token
  (child+session[+topic]); revoked → terminal `link.revoked` + close.
  Stream routes are limiter-exempt (connection caps instead); nginx
  needs `proxy_buffering off` on `/api/stream` + `/public/stream`.
- Uploads: `multipart/form-data`, `c.FormFile`, server mime sniff +
  hard cap 10 MB (client pre-checks 10 MB and blocks larger before send); path
  `<tenant_id>/<session_id>/<stid>/<child_id>/<rand32hex>.<ext>` (numeric IDs,
  ext from sniffed mime), DB stores relative.
- Honest-file rule (round 7, binding): bytes are served ONLY from server
  disk via `/uploads/…` AFTER durable save (DB row committed + file
  `fsync`ed/renamed into place). Upload `POST` responds 200 `{data}`
  only after BOTH row + file are durable. SSE `photo.changed` is
  published only after that point — never before, never on intent.
  Gallery/report `<img src>` URLs therefore always resolve to bytes that
  exist (except `PHOTO_MISSING` when an operator deletes the file
  out-of-band). Client MUST NOT render an upload from its local
  `FileReader`/object-URL as if it were the server copy beyond the
  transient preview state: preview (local blob) → 200 `{data}` (server
  durable) → SSE signal → refetch `GET P/photos` → `<img>` from
  `/uploads/…`. No fake status: no success toast, no gallery item, no
  candidate flag may appear before the server 200.
- WA text: `backend/templates/wa_report.md`, `wa_consent.md` + URLs from
  `PUBLIC_WEB_BASE_URL` + page routes (`/view/…`).

## 9. Constraints (binding)

- New Go module or npm package needs explicit approval (update this file).
- SSE adds ZERO new dependencies: server uses `fiber/v3/middleware/sse`
  (already part of Fiber v3) + stdlib (`sync`, `time`, `context`);
  client uses `fetch` + `ReadableStream` in `lib/stream.ts` (no
  `eventsource` polyfill, no socket library).
- No Tailwind (antd owns styling except plain-CSS report).
- No `VITE_*` API URL; no `environment:` values in `compose.yml`;
  no secrets in images; relative bindmounts only; single `compose.yml`.

## 10. Test Tooling (REQUIRED — user-confirmed 2026-10-09)

### 10.1 Backend (Go 1.26.6 + Fiber v3)

- **Unit test**: `go test` (stdlib) + `github.com/stretchr/testify`
  (assertion: `assert`, `require`, `suite`) + `net/http/httptest`
  (HTTP handler testing).
- **Integration test**: `go test` + `httptest` + `github.com/DATA-DOG/go-sqlmock`
  (DB mocking for repository layer) + `github.com/ory/dockertest` (optional,
  for real DB integration tests in CI).
- **Coverage**: Minimal 90% business logic, 100% critical paths (auth,
  validation, idempotency, tenant isolation, envelope transport).
- **Structure**: `internal/<domain>_test.go` (co-located with source),
  `internal/integration/*_test.go` (cross-domain flows).
- **Commands**:
  - Unit: `go test ./internal/... -v -cover`
  - Integration: `go test ./internal/integration/... -v -tags=integration`
  - Coverage: `go test ./... -coverprofile=coverage.out && go tool cover -html=coverage.out`
- **Pass criteria**: All tests pass, coverage ≥90%, `go vet` clean,
  `gofmt` clean.

### 10.2 Frontend (Vite 8 + React 19 + TS)

- **Unit test**: `vitest` (Vite-native) + `@testing-library/react`
  (component testing) + `@testing-library/jest-dom` (DOM assertions) +
  `@testing-library/user-event` (user interaction simulation).
- **Integration test**: `vitest` + `msw` (Mock Service Worker, API mocking)
  + `@testing-library/react` (full component flows with mocked API).
- **Coverage**: Minimal 90% components, 100% critical paths (auth context,
  forms, report export, SSE stream, envelope handling).
- **Structure**: `src/**/*.test.tsx` (co-located with source),
  `src/test/setup.ts` (global test setup, MSW server).
- **Commands**:
  - Unit: `vitest run`
  - Integration: `vitest run --config vitest.integration.config.ts`
  - Coverage: `vitest run --coverage`
- **Pass criteria**: All tests pass, coverage ≥90%, `tsc --noEmit` clean,
  `oxlint` clean.

### 10.3 Test Strategy Summary

| Layer | Backend (Go) | Frontend (React/TS) |
|---|---|---|
| Unit test | `go test` + `testify` + `httptest` | `vitest` + `@testing-library/react` |
| Integration test | `go test` + `httptest` + `sqlmock` | `vitest` + `msw` + `@testing-library/react` |
| Coverage target | ≥90% business logic, 100% critical | ≥90% components, 100% critical |
| Critical paths | auth, validation, idempotency, tenant isolation, envelope | auth context, forms, report export, SSE, envelope |
| Structure | `internal/<domain>_test.go` | `src/**/*.test.tsx` |
| Pass criteria | all pass + coverage + vet + gofmt | all pass + coverage + tsc + eslint |

### 10.4 Test Dependencies (new packages — user-approved 2026-10-09)

Backend (add to `go.mod`):
- `github.com/stretchr/testify` (assertion)
- `github.com/DATA-DOG/go-sqlmock` (DB mocking)
- `github.com/ory/dockertest` (optional, integration only)

Frontend (add to `package.json`):
- `vitest` (test runner)
- `@testing-library/react` (component testing)
- `@testing-library/jest-dom` (DOM assertions)
- `@testing-library/user-event` (user interaction)
- `msw` (API mocking)
- `@vitest/coverage-v8` (coverage)

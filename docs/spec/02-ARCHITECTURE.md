# 02 — Architecture

Status: DRAFT (2026-10-12; audit spek 5 dimensi selesai; pembaruan konsistensi + notifikasi antd terpasang)

## [PEMBARUAN 2026-10-12] Audit spek 5 dimensi — konsistensi + notifikasi antd

> Penanda pembaruan: hasil audit 5 dimensi paralel (scope & acceptance,
> tech stack & arsitektur, fitur & flows, data & error, indeks & pola)
> per 2026-10-12. Perbaikan kontradiksi/keambiguan diterapkan langsung di
> teks terkait; ketentuan BARU = notifikasi antd `notification` untuk
> hasil aksi user (keputusan 46 — normatif di `02§3`, `03 F-17`, `08`)
> + keputusan 47–50 (route readiness kanonik, persetujuan `oxlint`,
> buku audit §4 non-binding, `DESIGN.md` non-binding). Rincian:
> `07` Change 18 + section indeks pembaruan di `SPEC-INDEX.md`.

## [PEMBARUAN 2026-10-12] Penutupan butir audit §4 — keputusan 51–72

> Penanda pembaruan: 22 butir
> `docs/audit/2026-10-11-spec-codebase-sync-audit.md` §4 dijawab user
> (2026-10-12) dan dikunci sebagai keputusan 51–72 (lihat `SPEC-INDEX`).
> Relevan berkas ini: SA READ tenant nonaktif (53); logout cookie-only
> (52); SA auto-pick tenant aktif untuk stream (54); reveal
> self-destruct (51); teks misi live (56); `GET /missions` untuk FAS
> (58); gate pembuatan link saat incomplete (64); revoke/regenerate
> tenant-scope (65). Rincian: `07` Change 19 + section indeks pembaruan
> di `SPEC-INDEX.md`.

## [PEMBARUAN 2026-10-11] Ketentuan Upload Baru

> Penanda pembaruan: seluruh perubahan ketentuan upload per 2026-10-11
> ditandai heading `[PEMBARUAN 2026-10-11]` agar mudah ditemukan.
> Ketentuan upload lama di berkas ini (≤2 MB untuk badge/frame, ≤10 MB
> untuk foto, validasi mime/ukuran server-side) DICABUT dan DIGANTIKAN
> ketentuan di bawah ini.

- **Ukuran maksimum: 50 MB** per berkas yang di-upload (foto, badge
  program/topik, frame).
- **Validasi: client side validation only** — pemeriksaan format dan
  ukuran dilakukan client sebelum file dikirim; server TIDAK melakukan
  validasi ukuran/format (tanpa hard cap server-side, tanpa server mime
  sniff untuk validasi).
- **Format yang diizinkan: jpg, jpeg, png, webp** (case-insensitive);
  format lain (termasuk ico) ditolak client sebelum pengiriman.

## 1. System overview

Fresh build, no legacy. Four Docker services, one `compose.yml`:

```text
Browser (React SPA, antd chrome + plain-CSS `#report-a4` interior — D-1/decision 38)
  │  same-origin /api/* + /uploads/* + /public/* (nginx → api:3000)
  ▼
web (nginx:alpine, static dist, no env, no volume)
  │  proxies /api/*, /public/*, /uploads/* → api:3000
  ▼
api (Go 1.26.6 + Fiber v3, :3000)
  │──► db (mariadb:12.3, ./data/mariadb:/var/lib/mysql)
  │──► wa (OpenWA, ./data/openwa:/app/data, http://wa:2785/api)
  │──► Gemini + SMTP (egress only)
  └──► /data/uploads (./data/uploads:/data/uploads)
```

- `db`: `mariadb:12.3`, `env_file:[.env]` (only `MARIADB_*`), healthcheck.
- `api`: `build ./backend`, `env_file:[.env]`,
  `depends_on: db (condition: service_healthy)`
  (wa NOT required for boot; OpenWA failures are runtime-only).
- `web`: `build ./frontend`, `ports:["8002:80"]`, `depends_on:[api]`.
  Nginx binding: `proxy_read_timeout 180s; proxy_send_timeout 180s;
  client_max_body_size 52m;` on `/api/*` (batch WA with jitter runs up to
  ~100 s; uploads up to 50 MB + multipart overhead — Pembaruan
  2026-10-11). Fiber `BodyLimit: 52*1024*1024` to match.
  Stream routes (`/api/stream`, `/public/stream`) additionally require
  `proxy_buffering off;` (else nginx buffers SSE frames) with the same
  180 s timeouts (heartbeat `: ping` 15 s keeps the connection alive
  well below it).
- `wa`: `image openwa-openwa:latest` (verified present locally
  2026-10-08: `openwa-openwa:latest` 3.39 GB, exposes `2785/tcp`,
  `WORKDIR /app`, `HOME=/app/data`, healthcheck
  `curl -f http://localhost:2785/api/health/ready`; user-locked round 5),
  `env_file:[.env]`, `ports:["8003:2785"]` (dashboard QR + key minting,
  once), bindmount `./data/openwa:/app/data`.

## 2. Backend — vertical slices, no layers

One Go module, stdlib + 4 packages (`fiber/v3`, `go-sql-driver/mysql`,
`golang-jwt/jwt/v5`, `x/crypto`). Rule: 1 domain = 1
`internal/<domain>.go` (routes + handlers + SQL). No `service/`,
`repository/`, `dto/`, `usecase/` layers.

Files: `cmd/api/main.go` (env → db → migrate → seed → routes → listen);
`internal/config.go db.go auth.go validate.go files.go wa.go mail.go ai.go
tenants.go users.go programs.go topics.go activities.go missions.go
frames.go children.go sessions.go assessments.go photos.go reports.go
`consents.go public.go dashboard.go stream.go`; plus cross-domain infra
modules `envelope.go` (HTTP-200 `{data}`/`{error:{code,message}}`
envelope writer), `pscope.go` (per-connection SSE scope), and
`idempotency.go` (`X-Idempotency-Key` replay store) (`stream.go` = SSE hub ONLY:
`Publish`, `Subscribe`, `Replay` on an in-memory fan-out per
`(tenant, scope)` + ring 100 events / 5 min per tenant; single `api`
replica for MVP — multi-replica fan-out via Redis is out of scope and
would be a new Change, not a silent upgrade); `migrations/NNNN_*.sql`;
`prompts/system.md user.md`; `templates/wa_report.md wa_consent.md`.

Pipeline per protected route (binding order):

```text
recover → requestid → logger → cors → limiter (sensitive routes only)
→ keyauth(access FromAuthHeader Bearer; refresh FromCookie on /refresh only)
→ `RequireRole` (SA passes ALL role checks — superset of ADMIN/FAS in any
  active tenant; ADMIN passes facilitator-level checks in own tenant) →
  tenant guard (`X-Tenant-ID` vs JWT claim)
→ `c.Bind().Body/Query/URI/Header/Cookie` (Bind-first, see 01§2) → validate.go
→ store(`c.Context()`+timeout)
→ c.Status(200).JSON envelope
```

- `c.Context()` with `context.WithTimeout(5s)` for DB, 15 s for Gemini,
  10 s for OpenWA; never pass `c` itself.
- Transport rule (binding user decision): ALL `/api/*` and `/public/*`
  return HTTP 200 with `{data}` or `{error:{code,message}}`, except
  unhandled faults → HTTP 500 `{error:{code:"INTERNAL"}}` via
  `recover`/custom `ErrorHandler`. Rate limiting also returns 200 +
  `Retry-After` header (never 429). Client branches on envelope only.
- `X-Tenant-ID` REQUIRED on every authenticated call. Non-SA: must equal
  JWT `tenant` claim or `TENANT_MISMATCH`. SA: any active tenant
  (switcher sets it); deleted → `TENANT_INACTIVE`; inactive → member
  calls → `TENANT_INACTIVE`, SA READ allowed / SA WRITE →
  `TENANT_INACTIVE` (decision 53). Clarifier
  (2026-10-11): SA may OMIT `X-Tenant-ID` — cross-tenant READS on the
  per-route basis (routes that support tenant-0 unscoped reads, e.g.
  session list / reset list / report+assessment reads); every WRITE goes
  through `requireTenant` → tenant 0 → 200 `VALIDATION` before any gate
  (pipeline order above).
- `X-Idempotency-Key` (UUID/`crypto/rand` hex, client-generated) REQUIRED
  on: `POST /api/auth/register`, `POST /api/sessions`, `POST /api/users`,
  WA single/batch, consent send, public-link create/regenerate, photo
  upload, narrative generate (POST), public consent respond, copy-data
  execute (12 routes). Server stores
  `(tenant, key, route, result)` 24 h (tenant-scoped — cross-tenant same
  key never replays); replay returns the original envelope
  without a second side-effect. Replay after 24 h purge = fresh execution
  (new row, side-effects re-run). Same-key photo replay after row deleted
  = fresh upload (deleted rows are not replayable).
- Session status guard: mutating handlers use
  `UPDATE … WHERE id=? AND status=<expected>`; affected 0 → RE-READ
  current status: wrong-state single-writer → 200 `SESSION_LOCKED`
  (E10), rival-won race → 200 `STATE_CONFLICT` (E11) — see 06 taxonomy
  (the old "affected 0 → STATE_CONFLICT" shorthand is superseded).

## 2b. Route table (canonical; all `/api/*`+`/public/*` JSON, all HTTP 200 except 500 — infra/health/static below are OUTSIDE this rule: `/livez|/readyz|/startupz` plain-text healthcheck, `/uploads/*` static bytes)

Paged-list keys (binding, decision 25): every `GET` list in this table
that returns `meta:{page,per_page,total}` (see `05§3` for the paged vs
full-array split) accepts `?page=&per_page=&q=&sort=&dir=` plus the
route's stated equality filters (the `?q` substring filter applies to
ALL paged list GETs, INCLUDING consents + reset-tokens — decision 43,
2026-10-11); defaults `page=1, per_page=20`, allowed
`per_page` 20/50/100/1000; invalid values → 200 `VALIDATION`.

Auth (`/api/auth/*`): `POST register|login|forgot|reset` (public+limiter;
register needs `X-Idempotency-Key`), `GET /api/auth/tenants` (public, no auth,
limiter — active tenants `{id,name,type}` only for the register picker),
`POST refresh` (cookie only), `POST logout`, `GET me`,
`PATCH profile`, `POST change-password` (revokes ALL refresh rows, like
reset — other sessions re-login), `POST switch-tenant {tenant_id}`
(SA-ONLY; ADMIN/FAS calling returns 200 `FORBIDDEN_ROLE` — their tenant
is fixed to the JWT claim. SA validates target active + returns tenant;
client persists `kv-tenant`). Logout (`POST logout`) revokes jti AND
clears the cookie via `ClearCookie`; cookie-only logout (decision 52):
the route accepts the refresh cookie even when the access token is
expired/absent (revoke jti + clear cookie → 200).

Tenants [SA]: `GET /api/tenants` (all incl. inactive; optional
`?is_active=true|false` filter — invalid value → 200 `VALIDATION`),
`POST /api/tenants`
(name globally UNIQUE → 200 `VALIDATION` on duplicate),
`GET|PATCH|DELETE /api/tenants/:id`,
`POST /api/tenants/:id/activate|deactivate` (typed confirm on DELETE).

Users: `GET /api/users?role=&decision=&is_active=&q=` (SA all / ADMIN
own-tenant FACILITATORS ONLY — ADMIN requesting `role=ADMIN`/`role=SA`
or omitting role gets facilitator rows only, never admin rows
(binding — the route scope is facilitator-list for ADMIN, full-list
for SA); `GET /api/users/:id`, `PATCH /api/users/:id`,
`DELETE /api/users/:id`
(typed confirm; blocked if owns open groups → `HAS_OPEN_GROUPS`),
`POST /api/users/:id/approve|reject|activate|deactivate`
(approve sets (APPROVED,true); reject sets (REJECTED,false) retained;
activate sets true; deactivate sets false + revokes refresh in same Tx).
`POST /api/users` (create a user directly — user-confirmed: callers are
SA + ADMIN only; FAS → 200 `FORBIDDEN_ROLE`. The created row is
APPROVED + `is_active=true`, i.e. the account can log in immediately —
no register + approval flow. `role` accepts ADMIN or FASILITATOR ONLY —
a request with `role=SA` → 200 `FORBIDDEN_ROLE`. ADMIN may create only
in its own tenant. Email stays globally UNIQUE → duplicate → 200
`EMAIL_TAKEN`. The target tenant must be active (inactive → 200
`TENANT_INACTIVE`). `X-Idempotency-Key` is REQUIRED — replay returns the
original envelope).
Pending list = `GET /api/users?decision=PENDING`.
Pending badge = `GET /api/users/pending-count {data:{pending:number}}`
(same scope as list).

Reset dashboard: `GET /api/reset-tokens` (SA all / ADMIN own-tenant;
item = `{id, code_masked:"***AB", expires_at, seconds_left, revoked,
mail_failed, user:{id,name,email}}` — unified field set matching the
contracts doc + `05§2` `mail_failed`; `?reveal=1`
audited returns `code_plain` ONCE then PURGES it — single-shot
self-destruct (decision 51; subsequent reveal → 200 `VALIDATION`, masked
only), `POST /api/reset-tokens/:id/revoke`,
`POST /api/reset-tokens/:id/reveal` (audited; same self-destruct).

Masters (tenant-scoped, [SA/ADMIN]; `GET /api/missions` ALSO readable
by FAS — read-only own-tenant, required by the F-12 mission picker,
decision 58): `GET|POST /api/programs`,
`GET|PATCH|DELETE /api/programs/:id`; same shape for
`/api/topics?program_id=`, `/api/activities?topic_id=&program_id=`,
`/api/missions?topic_id=&program_id=`, `/api/frames?program_id=`
(frames `POST` = multipart image + `name` + `program_ids[]`).

Children [ADMIN/SA mutate; FAS read-only list/detail — user-confirmed
2026-10-08: FAS `POST|PATCH|DELETE` → 200 `FORBIDDEN_ROLE`]:
`GET /api/children?q=&is_active=` (FAS scoped to children in own groups),
`GET /api/children/:id`, `POST /api/children`, `PATCH|DELETE
/api/children/:id` (ADMIN/SA; typed confirm on DELETE),
`POST /api/children/:id/activate|deactivate` (ADMIN/SA).

Sessions [ADMIN/SA mutate; FAS scoped read + own-group ops]:
`GET /api/sessions?status=&q=` (FAS sees only sessions where they own ≥1
group; ADMIN own-tenant all; SA active-tenant all),
`POST /api/sessions` (ADMIN/SA only, FAS → 200 `FORBIDDEN_ROLE`;
Idempotency-Key; user decision round 5: `groups[]` OPTIONAL — missing or
empty allowed → Draft with zero groups, groups added later via
`POST /api/sessions/:id/groups`; open-gate enforces ≥1 group + non-empty +
active owner),
`GET /api/sessions/:id` (detail shape binding: `{session, topics:[{stid,
name, activities:[{said,name}]}], groups:[{gid, name, owner, members,
consent}]}` — topics+groups in ONE response so assessment/gallery/report
never waterfall; FAS foreign groups reduced to
`{gid, name, own, member_count, status}` where `status` = the parent
session's status — `session_groups` has no status column, the value is
DERIVED from the session row (user decision 44 / D-8, 2026-10-11; `own`
marks group ownership), contracts doc pinned identically),
`PATCH /api/sessions/:id` (ADMIN/SA; Draft=anything; Opened=groups/members
only; Finished → 200 `SESSION_LOCKED`, use reopen),
`DELETE /api/sessions/:id` (ADMIN/SA; Draft/Finished = typed-text modal
wipe with scope counts; Opened → 200 `SESSION_LOCKED`, finish first),
`POST /api/sessions/:id/open|finish|reopen` (ADMIN/SA; guarded transitions —
already-in-target returns 200 `{data:{status, already:true}}`),
groups: `GET|POST /api/sessions/:id/groups` (mutate ADMIN/SA; Draft/Opened;
Finished → 200 `SESSION_LOCKED`),
`PATCH /api/sessions/:id/groups/:gid {name?, facilitator_id?}` (ADMIN/SA;
Draft/Opened only, Finished → 200 `SESSION_LOCKED`; new owner must be an
active `FASILITATOR` of the same tenant else 200 `VALIDATION`),
`DELETE /api/sessions/:id/groups/:gid` (ADMIN/SA; Draft/Opened only,
Finished → 200 `SESSION_LOCKED`; user decision 2026-10-08 option B cascade
wipe: deletes the group + removes ALL its members from the session + wipes
their session-scoped data — assessments, photo rows + disk files,
narratives, mission picks, consent/public links for those children in this
session — in one Tx (disk after commit); masters/children untouched;
client confirms via typed-text modal with member + artefact counts),
`POST /api/sessions/:id/groups/:gid/members {child_ids[]}` (bulk add, active children, dedup) /
`DELETE /api/sessions/:id/groups/:gid/members/:childId` (single remove; same per-child session wipe
as group delete, silently — no trash for MVP).

Copy-data [ADMIN/SA] (user decision round 6): `GET
/api/sessions/:sid/children/:cid/copy-candidates` → other same-program
sessions (same tenant) containing the child
`[{session_id, session_name, session_date, status, counts}]`; `POST
/api/sessions/:sid/children/:cid/copy {source_session_id}` (Idempotency-Key;
fill-empty only: unmatched target topics/activities stay empty; copies
stars+notes with `assessor_id`=copier, narratives only-if-empty, mission
picks only-if-zero-and-linked, photo files physically to new paths only
when target topic has 0 photos; consent/links/`wa_outbox` never copied;
badges derived, never copied).

Per-child-per-topic prefix (binding): P =
`/api/sessions/:sid/topics/:stid/children/:cid`. Membership gate
(user decision round 5): EVERY `P` op first checks `session_members`
for (`session_id=:sid`, `child_id=:cid`); non-member → 200 `NOT_FOUND`
(no leak whether the child id exists elsewhere). Topic linkage (binding):
`:stid` must be a `session_topics` row for `:sid` and `:said` (where
present) must be a `session_activities` row for that `:stid`; mismatch →
200 `NOT_FOUND`. Ownership is resolved
server-side via membership lookup (`session_members` → `session_groups`);
no `:gid` appears in these URLs (group ops live under `/groups`).
FAS touching a foreign child → 200 `NOT_OWNER`.

Assessment (prefix P): `PUT P/activities/:said {stars:1-4, note?}`
(`said`=`session_activities.id`, note ≤2000 chars else 200 `VALIDATION`),
`GET P/assessments` (this topic, this child),
`GET /api/sessions/:sid/progress` (whole-session matrix).

Photos (prefix P): `GET P/photos` (all ≤10, `created_at` ASC, no paging;
missing disk file → item carries `missing:true`, E29 `PHOTO_MISSING`),
`POST P/photos` (multipart + Idempotency-Key; frame must link the session
program else 200 `VALIDATION`),
`PATCH P/photos/:pid {is_candidate:true}`,
`DELETE P/photos/:pid` (frees slot).

Narrative + missions + report (prefix P):
`POST P/narrative` (generate, Idempotency-Key) | `GET P/narrative` |
`PATCH P/narrative {text}` (manual edit ≤2000 chars),
`GET P/missions` | `PUT P/missions {mission_ids[≤4]}` (Tx replace-set;
each mission must link this topic via `mission_topics` else 200
`VALIDATION`),
`GET P/report` (full ReportView JSON incl. completeness banner for
incomplete topics — public render is never gated),
`GET P/readiness` → `{complete, missing:[{said,name}],
badges:{topic,program}}`,
`GET /api/sessions/:sid/reports` (per-session ops matrix
`[{child_id, child_name, session_topic_id, topic_name, complete,
narrative, missions, candidate_photo, report_url?, gallery_url?}]`).

Links + WA: `POST P/public-links {kind}` (Idempotency-Key; GATED —
incomplete topic → 200 `REPORT_INCOMPLETE`, decision 64;
`GET P/public-links` lists active `[{kind, url, token, created_at}]`
(code/contracts superset — the token is included for client-side
copy/revoke convenience)),
`POST /api/public-links/:token/revoke|regenerate` (regenerate revokes old,
one Tx; revoked/unknown → 200 `TOKEN_INVALID`; ADMIN caller must belong
to the link's tenant — SA any tenant, cross-tenant → 200 `NOT_FOUND`
no-leak, decision 65),
`POST P/send-report {to?}` (default mother's WA; if supplied must match
one of the child's registered numbers else 200 `VALIDATION`; gated per
topic — incomplete → 200 `REPORT_INCOMPLETE`. Single send = ONE item and
its send outcome is returned per-item INSIDE `data`, NEVER as a
top-level `{error}`: OpenWA down/failing → 200 `{data:{status:"FAILED",
code:"WA_FAILED",…}}` — identical per-item shape to a batch item),
`POST /api/sessions/:sid/batch-send {items:[{session_topic_id, child_id,
to?}]}` (+ `X-Idempotency-Key` header, max 10, jitter 3–10 s between
sends; per-item gating/status —
each item checks membership + topic linkage + readiness; non-member item →
per-item 200 `NOT_FOUND`, incomplete item → per-item 200
`REPORT_INCOMPLETE`, others still send),
`POST /api/sessions/:sid/children/:cid/send-consent` (ADMIN/SA only;
DRAFT/OPENED only; Idempotency-Key).

Consents (auth): `GET /api/sessions/:sid/consents?status=` →
`[{child_id, child_name, group_name, status, respondent_name,
responded_at, consent_url?}]` (URL included while PENDING for copy/resend),
`GET /api/consents?session_id=&child_id=`.

Dashboard: `GET /api/dashboard/overview`,
`GET /api/dashboard/analytics?period=7d|30d|all`
(tenant from `X-Tenant-ID`; SA `?tenant=` override).
`reports_issued` INTENTIONAL SPLIT (two definitions, both deliberate):
the OVERVIEW counts ACTIVE REPORT links only (`public_links kind=REPORT
AND revoked_at IS NULL`) + narratives; the SA tenant-summary below counts
ALL kind=REPORT links (revoked included) + narratives (matches decision
29) — the overview is a live activity metric, the summary is a
lifetime per-tenant ledger.
`GET /api/dashboard/tenant-summary` (SA-ONLY — non-SA → 200
`FORBIDDEN_ROLE`; no limiter; 200 always) →
`{data:{tenants:[{tenant_id,name,type,is_active,children,sessions_open,
sessions_finished,reports_issued}]}}`, ordered `id ASC`: `children` = all
children rows (active+inactive); `sessions_open`/`sessions_finished` =
per-status counts; `reports_issued` = narratives count + ALL public_links
kind=REPORT count (regardless of revoked_at; definition 08). NO mixed cross-tenant aggregates
(user decision #29, 2026-10-11, superseding the "cross-tenant summary"
wording).

WA status [SA/ADMIN] (user decision round 6; connect flow updated
2026-11-09): `GET /api/wa/status` [SA/ADMIN] reads the configured OpenWA
session (`GET /api/sessions` → `GET /api/sessions/{id}`, 5 s each) → 200
`{data:{connected, session, checked_at}}`; `connected` is true only when
OpenWA's session status is `ready`. OpenWA unreachable → 200
`{data:{connected:false}}` (status only, never `WA_FAILED`).
`GET /api/wa/session` and `POST /api/wa/session/start` are SA-ONLY: they
proxy the session state + QR → 200 `{data:{connected, status, session,
phone, qr_code, last_error, checked_at}}` so an SA can pair in-app; the
OpenWA API key never leaves the server. Dashboard `WaStatusBadge`
(event-driven round 7: renders `wa.changed` `{connected}`, detail via
`GET /api/wa/status` on demand; SA click opens the connect modal — Open
OpenWA opens `http://<host>:8003` in a new tab, Connect Directly starts
the session and shows the QR; ADMIN click is inert).

Public JSON (no auth, limiter, HTTP 200):
`GET /public/report?token=`, `GET /public/gallery?token=`,
`GET /public/consent?token=`,
`POST /public/consent/respond {token,decision,respondent_name}`
(Idempotency-Key; first-wins).

Infra: `GET /livez|/readyz|/startupz` (healthcheck; readiness probes DB),
`/uploads/*` (static `Browse:false, Download:false`, read-only).

Realtime stream (round 7, binding — 1 endpoint + 1 thin public endpoint,
signal-only): `GET /api/stream` (auth pages; handshake = `keyauth` Bearer
+ `RequireRole` + `X-Tenant-ID` guard, same as REST, PLUS the
`sessionAlive` live-refresh-session probe — the token owner must still
hold ≥1 live `refresh_tokens` row (audit K3: logout/password-reset/admin
revoke kills the stream for old Bears); failures return a normal 200
envelope and the stream never opens — besides the `06§2` code list the
handshake can answer `ACCOUNT_INACTIVE` (pending/rejected/deactivated
user) and `DB_UNAVAILABLE` (the probe's DB hit fails), both 200
envelopes, stream never opens; limiter-exempt;
concurrent-connection caps (binding triple — same in 01 limiter-policy
+ 06§2: max 100 per `api` replica + max 6 per IP + max 20 per tenant;
enforced at handshake, excess → 200 `RATE_LIMITED` + `Retry-After`) and
`GET /public/stream?token=` (parent pages; scope locked to that token's
child+session[+topic]; revoked/unknown → 200 `TOKEN_INVALID`, no stream).
Both are `text/event-stream` (HTTP 200 held open), NOT envelopes.
Opened client-side via `fetch` + `ReadableStream` (NOT `EventSource`).
Frames carry `{entity, entity_id, rev}` + routing keys only — never names,
notes, photos, tokens, codes, or bytes. Full event catalogue + filter
predicate + refetch table: see §2c. Publish points: every mutating
handler posts to the hub AFTER Tx commit (best-effort — publish failure
never fails the mutation); hub = `internal/stream.go` in-memory fan-out
per `(tenant, scope)` + ring 100 events / 5 min per tenant for
`Last-Event-ID` replay (single `api` replica for MVP).

## 2c. Realtime — 1 SSE endpoint, signal + refetch (round 7, binding)

One stream per tab maximum (opened on page mount, closed on unmount/
tenant-switch/logout). Client routing table (binding — event → refetch,
debounced/coalesced 300 ms per table; signals for pages not currently
open are ignored; signals with `rev <= last_seen_rev` for that table are
ignored without request):

| Event (`event` / `action`) | Published after (post-commit) | Client refetch (existing guards apply) |
|---|---|---|
| `program.changed` (`created\|updated\|deleted`) | program CRUD | `GET /api/programs` (+ picker eligibility) |
| `topic.changed` | topic CRUD | `GET /api/topics?program_id=` |
| `activity.changed` | activity CRUD | `GET /api/activities?topic_id=` |
| `mission.changed` | mission CRUD + mission_topics edit | `GET /api/missions?…` + open pickers |
| `frame.changed` | frame CRUD + frame_programs edit | `GET /api/frames?…` + capture frame list |
| `child.changed` | child CRUD / activate / deactivate / delete | `GET /api/children` (+ open detail) |
| `session.changed` (`created\|updated`) | session create / PATCH | `GET /api/sessions` + open detail |
| `session.status` | open / finish / reopen (guarded Tx winner only) | session detail + status chip |
| `session.deleted` | Draft/Finished wipe commit | session list (drop row) + close detail if open |
| `group.changed` | group create / PATCH / delete | `GET /api/sessions/:id` groups section |
| `member.changed` | member add AND member remove — `copy_available` hint recomputed server-side at publish on BOTH (child in other same-program sessions?) | groups section + (if modal trigger) `GET …/copy-candidates` |
| `assessment.changed` | PUT stars+note commit | `GET P/assessments` + `GET P/readiness` |
| `photo.changed` | photo row commit + file durable (see honest-file rule) / delete / candidate set | `GET P/photos` (bytes from `/uploads/…` only) |
| `narrative.changed` | generate upsert / manual PATCH | `GET P/narrative` |
| `mission-pick.changed` | `PUT P/missions` replace-set | `GET P/missions` |
| `readiness.changed` `{complete}` | any assessment/mission change flips completeness | readiness banner + derived badge display (ONLY full-value exception: `{child_id, stid, complete}` — derived non-PII, safe to render WITHOUT refetch; the assessments table still refetches on the paired `assessment.changed`, never on `readiness.changed` alone) |
| `copy.completed` | copy-data Tx commit + files durable | target session assessments + readiness + photos |
| `consent.changed` | send / parent respond | `GET /api/sessions/:sid/consents` + consent page status |
| `report-link.changed` | public-link create / revoke / regenerate / session-wipe revoke / group-delete-cascade revoke / child hard-delete revoke | `GET P/public-links` + reports matrix |
| `user.changed` | register / approve / reject / activate / deactivate / delete | `GET /api/users?…` |
| `pending.changed` `{pending}` | any decision transition | pending badge (ONLY full-value exception #2: a count, safe to render) |
| `reset.changed` | forgot row / reveal / revoke / use / expiry sweep | `GET /api/reset-tokens` (masked + countdown — replaces 5 s poll) |
| `tenant.changed` | tenant CRUD / activate / deactivate / wipe | tenant list + (if own tenant affected) context reload |
| `stats.changed` | dashboard aggregator tick (5–10 s server tick, not per-mutation) | `GET /api/dashboard/overview\|analytics` (replaces client memo/poll — refetch on SSE signal only) |
| `wa.changed` `{connected}` | server polls the configured OpenWA session every 5 s (replaces the old `/api/health/ready` probe and the 30 s client poll); `connected` = the session is `ready` (phone linked), and the frame is published on a LINK-STATE TRANSITION only (ready↔not-ready — not on every poll tick) | `GET /api/wa/status` only if badge needs detail; `{connected}` renders directly (ONLY full-value exception #3) |
| `auth.expiring` (directed, not broadcast; frame = `{exp: RFC3339}` only — no id, no rev) | T-60 s before access expiry on that connection | refresh-once → reconnect with new Bearer + last id |
| Public: `report.changed` / `gallery.changed` | narrative/mission/photo/link change in that child+session[+topic] | `GET /public/report\|gallery?token=` |
| Public: `consent.ack` `{decision}` | parent respond on that token | consent page shows recorded state (ONLY full-value exception #4, own decision only) |
| Public terminal: `link.revoked` | revoke / regenerate / session wipe / group-delete cascade / child hard-delete | show "link invalid" + close stream |

Filter predicate (binding, T1 resolution — reuse, never duplicate):
a frame is sent on connection C **iff** the corresponding REST `GET`
would return that row to C. Implementation: handshake snapshots
`(user_id, role, tenant_id, own_group_ids, owned_session_ids,
child_scope)` computed by CALLING the same scope functions used by
`GET /api/sessions` / `GET /api/children` (imported, not copied);
per-frame check = (tenant equal OR tenant_id=0 BROADCAST frame — the
stats/wa/reset broadcasts per `05§6`, delivered to EVERY authenticated
connection regardless of its tenant, never to public connections) AND
(ADMIN/SA pass tenant scope;
FAS passes only own-group/session/child scope; public passes only its
token scope). Membership changes (member add/remove, owner reassign)
recompute the affected connections' scope on the next publish (stale
scope window ≤ one publish). Tests MUST cover the matrix
(SA/ADMIN/FAS-owner/FAS-foreign/public × each event → send/skip).

Publish rule (binding): every mutating handler calls
`stream.Publish(tenant_id, frame)` AFTER Tx commit + (for photos/copy:
after files durable). Publish is best-effort (`slog` on drop) and NEVER
fails the mutation. `session.status` publishes ONLY for the guarded-Tx
winner (loser got `STATE_CONFLICT`, publishes nothing). Batch WA
publishes ONE `report-link.changed` per session after the batch
(not per item — per-item statuses stay in the batch response envelope).

Connection caps (binding; single table — same in 01 limiter-policy):
max 100 concurrent streams per `api` replica + max 6 per IP (browser-norm)
+ max 20 per tenant; enforced at handshake, excess handshake → 200
`RATE_LIMITED` + `Retry-After`, stream never opened.
`GET /api/stream` + `GET /public/stream` are limiter-exempt otherwise.

## 3. Frontend — pages per feature, guarded by role

```text
main.tsx → router.tsx (BrowserRouter + RequireRole canonical)
lib/api.ts (relative /api + /public; Bearer; X-Tenant-ID; idem-key;
  envelope branch; 200+error → throw code; refresh-once on
  INVALID_TOKEN then retry once; Retry-After respect)
lib/stream.ts (round 7, binding; zero new npm deps): `openStream(path,
  {onEvent, getLastId})` via `fetch` + `ReadableStream` reader (NOT
  `EventSource`); parses `event:/data:/id:/retry:/: ping`; auto-reconnect
  backoff 1 s → 30 s max with `Last-Event-ID`; per-table routing +
  debounce/coalesce 300 ms → calls `lib/api.ts` refetch; ignores signals
  for closed pages and `rev <= last_seen_rev`; closes on unmount /
  tenant-switch / logout; single active stream per tab.
lib/auth.tsx (Context {user, tenant}; access memory-only;
  tenant localStorage 'kv-tenant'; localStorage 'kv-session' =
  boot-restore indicator only (access token stays memory-only);
  SA switcher default latest)
lib/i18n.ts (lng:'id', fallbackLng:'id', localStorage 'kv-lang')
components/: AppLayout (antd Layout.Header + Drawer; tenant-switcher SA-only),
  Protected (thin renderer of RequireRole decision),
  SettingsModal (language selector with `country-flag-icons` flags —
  decision 40; profile edit), PageHeader,
  ConfirmDialog (antd Modal with typed-text variant for tenant/finished/child wipes),
  StatusChip (antd Tag for Draft/Opened/Finished only), StarInput,
  Pager (Pager.tsx — page-size selector 20/50/100/1000, used by every
  list page),
  WaStatusBadge (antd Badge for SA/ADMIN dashboard; green/red from `wa.changed`
  `{connected}` event, detail via `GET /api/wa/status` on demand;
  click opens OpenWA `:8003` new tab — round 6, event-driven round 7,
  no polling),
  PhotoCapture (getUserMedia+canvas 9:16+frame; transient local preview
  only — gallery items render ONLY from server `/uploads/…` after
  200 `{data}` + `photo.changed` → refetch, per honest-file rule),
  StreamGate (round 7: owns the tab's single `lib/stream.ts` connection;
  opens on authenticated layout mount, closes on unmount/tenant-switch/
  logout; SA with `tenant:null` auto-selects the latest ACTIVE tenant
  via `POST switch-tenant` at mount — no active tenant → stream stays
  closed, graceful degradation (decision 54); public `/view/*` pages do NOT use StreamGate — each open
  `/view/*` page owns its own page-level `openStream` to
  `GET /public/stream?token=` and closes it on unmount; an authenticated
  tab opening a public link navigates to the public route (auth stream
  closed first) so the one-stream-per-tab invariant always holds), TokenCountdown
  (reset-dashboard only; driven by `reset.changed` events + local
  1 s tick-down from `expires_at` — no 5 s poll; forgot page is
  public and shows "check email / ask admin" with no countdown)
pages/: auth, dashboard (SA/ADMIN only — FAS has NO dashboard route;
FAS landing = own Sessions list + Profile), tenants(SA), users, programs, topics,
  activities, missions, frames, children, sessions, gallery,
  assessment, reports, consent, profile, view/{Report,Gallery,Consent}
report/ReportView.tsx + report.css (PLAIN CSS — the `#report-a4` interior per decision 38; chrome AROUND it is antd)
```

- Menu matrix (binding): SA sees Tenants/Users/Dashboard/Sessions/
  Masters/Children/Reports/Consent/Profile; ADMIN sees Dashboard/Users
  (facilitators)/Sessions/Masters/Children/Reports/Consent/Profile;
  FAS sees Sessions (own)/Children (read-only, own groups)/Reports
  (own)/Consent (read-only status)/Profile. Consent SEND button
  ADMIN/SA only (FAS hidden — user-confirmed 2026-10-08);
  Children create/edit/delete buttons hidden for FAS
  (`FORBIDDEN_ROLE` if called directly).
- Endpoint feedback notifications (decision 46, binding): EVERY
  explicit user action (submit/save/send/delete/generate/copy/approve/
  export/upload) surfaces its outcome via antd `notification` (card at
  viewport corner, `useNotification` hook — static methods deprecated):
  success envelope → `notification.success`; business-error envelope →
  `notification.error` with the i18n message + error `code` (+ recovery
  hint from `06§1`). SSE-signal refetches and background page refetches
  NEVER notify (no realtime spam). Defaults = antd defaults
  (`topRight`, ~4.5 s auto-close); implemented once and reused across
  pages (`03 F-17`; acceptance in `08`).
- Paging rule (decision 25, binding server-side pure): EVERY list page
  holds ONLY the active server page (`{data, meta:{page,per_page,total}}`);
  page-size selector offers 20/50/100/1000 (default 20); sort/search/filter
  controls re-request the server (`page` resets to 1 on filter/sort/search
  change); SSE signal → refetch SAME params (page/filters/sort preserved,
  debounced 300 ms, `rev`-gated); `meta.total` drives the pager (no
  client slicing, no full-set hold, no `DataTable` component).
- `RequireRole` (router) is canonical; `Protected` only renders its
  decision (no duplicate logic).
- Report export: `toPng(node,{cacheBust:true})` → `new
  jsPDF({orientation:'p',unit:'mm',format:'a4'})` → `addImage` →
  `save("Rapor-<Child>-<Topic>-<YYYYMMDD>.{png,pdf}")`
  (sanitised: each of Child/Topic transliterated to ASCII, `[^A-Za-z0-9-_]+`
  → `_`, truncated to 40 chars, empty → `anak`/`topik`; date = session date
  `YYYYMMDD`). NEVER `doc.html()`. Long content
  scales to one A4 page (no multi-page for MVP; overflow notice shown).
  Images same-origin `/uploads/*` only (no CORS taint).
- i18n namespaces `common,menu,auth,master,session,assessment,report`;
  report CONTENT Indonesian-only.

## 4. Data ownership & isolation

- Zero FK (binding). Every domain/link/snapshot row carries `tenant_id`
  + index. Queries always filter by request tenant. Email globally unique.
- SA switcher: `localStorage kv-tenant` + `X-Tenant-ID`; default latest
  tenant on first login; `POST switch-tenant` validates.
- Tenant `nonaktif`: any member call → 200 + `TENANT_INACTIVE`; SA
  read-only retained. Hard-delete (typed confirm): wipes ALL tenant rows
  + disk files in ALL three upload layouts — `uploads/<tenant_id>/`,
  `uploads/badges/<tenant_id>/`, `uploads/frames/<tenant_id>/` (decision
  45 / D-4); `wa_outbox` retained 90 d; OpenWA untouched.
- Snapshots: session create copies program/topics/activities/badges
  (+names) into `session_*`; master edits never touch sessions.
  Missions/frames LIVE (resolved at pick/capture; mission name/body
  render LIVE from the bank — `report_missions` snaps are history-only,
  decision 56).
- Photos on disk namespaced; DB relative path only. Report bytes never
  touch the server (WA link-only).

## 5. External boundaries

| Peer | Direction | Auth | Failure stance |
|---|---|---|---|
| MariaDB | api↔db TCP | env | boot retry; `/readyz` false until up; runtime err → 200 `DB_UNAVAILABLE` + Retry |
| OpenWA | api→`http://wa:2785/api…` send-text + `GET /api/wa/status` + SA-only session/QR/start proxy | `X-API-Key` | 1 retry; batch per-item `WA_FAILED`; manual URL copy fallback; QR pairing is SA-only and may happen in the connect modal or the OpenWA `:8003` dashboard (round 6, updated 2026-11-09); key stays server-side |
| Gemini | api→`…/v1beta/models/{MODEL}:generateContent` | `x-goog-api-key` | 15 s timeout; 200 `AI_FAILED`; old kept; manual retry |
| SMTP | api→smtp | env | reset row still created + masked dashboard + reveal/relay fallback |
| Camera | page→device | prompt | `CAMERA_DENIED` → gallery-upload fallback |

## 6. Test Strategy (REQUIRED — user-confirmed 2026-10-09)

### 6.1 Backend Test Strategy (Go 1.26.6 + Fiber v3)

**Unit Test:**
- Tooling: `go test` (stdlib) + `github.com/stretchr/testify` (assertion) + `net/http/httptest` (HTTP handler testing)
- Scope: Business logic, validation, middleware, handlers, SQL queries
- Structure: `internal/<domain>_test.go` (co-located with source)
- Coverage: ≥90% business logic, 100% critical paths (auth, validation, idempotency, tenant isolation, envelope transport)
- Commands: `go test ./internal/... -v -cover`

**Integration Test:**
- Tooling: `go test` + `httptest` + `github.com/DATA-DOG/go-sqlmock` (MOCK DB — no real database, no dockertest, no CI pipeline; unit + integration tests only)
- Scope: Cross-domain flows, API endpoints, SSE stream, full request/response cycle
- Structure: `internal/integration/*_test.go`
- Commands: `go test ./internal/integration/... -v -tags=integration`

**Critical Paths (100% coverage required):**
- Authentication (login, refresh, logout, reset)
- Authorization (role checks, tenant isolation, ownership)
- Validation (input validation, business rules)
- Idempotency (X-Idempotency-Key replay)
- Envelope transport (HTTP 200 + `{data}` / `{error:{code}}`)
- SSE stream (publish, filter, replay, caps)

**Pass Criteria:**
- All tests pass
- Coverage ≥90% (business logic), 100% (critical paths)
- `go vet` clean
- `gofmt` clean

### 6.2 Frontend Test Strategy (Vite 8 + React 19 + TS)

**Unit Test:**
- Tooling: `vitest` (Vite-native) + `@testing-library/react` (component testing) + `@testing-library/jest-dom` (DOM assertions) + `@testing-library/user-event` (user interaction)
- Scope: Components, hooks, utilities, context, API client, SSE stream parser
- Structure: `src/**/*.test.ts(x)` (co-located with source)
- Coverage: ≥90% components, 100% critical paths (auth context, forms, report export, SSE, envelope handling)
- Commands: `vitest run`

**Integration Test:**
- Tooling: `vitest` + `msw` (Mock Service Worker, API mocking) + `@testing-library/react`
- Scope: Full component flows with mocked API, form submissions, navigation, SSE stream
- Structure: `src/**/*.test.tsx` (integration tests marked with `.integration.test.tsx`)
- Commands: `vitest run --config vitest.integration.config.ts`

**Critical Paths (100% coverage required):**
- Auth context (login, refresh, logout, tenant switch)
- Forms (validation, submission, error handling)
- Report export (PNG/PDF generation, A4 fixed layout)
- SSE stream (connection, parsing, refetch, reconnect)
- Envelope handling (success/error branching, retry logic)

**Pass Criteria:**
- All tests pass
- Coverage ≥90% (components), 100% (critical paths)
- `tsc --noEmit` clean
- `oxlint` clean

### 6.3 Test Matrix Summary

| Layer | Backend (Go) | Frontend (React/TS) |
|---|---|---|
| Unit test | `go test` + `testify` + `httptest` | `vitest` + `@testing-library/react` |
| Integration test | `go test` + `httptest` + `sqlmock` | `vitest` + `msw` + `@testing-library/react` |
| Coverage target | ≥90% business logic, 100% critical | ≥90% components, 100% critical |
| Critical paths | auth, validation, idempotency, tenant isolation, envelope, SSE | auth context, forms, report export, SSE, envelope |
| Structure | `internal/<domain>_test.go` | `src/**/*.test.ts(x)` |
| Pass criteria | all pass + coverage + vet + gofmt | all pass + coverage + tsc + oxlint |

### 6.4 Test Execution

**Pre-commit (local):**
- Backend: `go test ./internal/... -v -cover` (unit only)
- Frontend: `vitest run` (unit only)

**Local coverage run (on demand — NO CI pipeline: CI is out of MVP scope,
no CI step exists or is required):**
- Backend: `go test ./... -v -coverprofile=coverage.out` + `go tool cover -html=coverage.out`
- Frontend: `vitest run --coverage` + `tsc --noEmit` + `oxlint`

**Integration tests (on-demand):**
- Backend: `go test ./internal/integration/... -v -tags=integration` (sqlmock — mock DB, never a real database)
- Frontend: `vitest run --config vitest.integration.config.ts`

## 7. Project structure (folder & file layout)

Normative layout of the whole repository, reconciled against the codebase
(decisions 33–37, 2026-10-11). Upload/data-dir paths follow `05§8`
(decision 33); test layout follows `01§10` / `02§6`.

```text
kidversa-edutourism-v2/
├── compose.yml                    # single compose: db, api, web, wa
├── .env                           # secrets only (gitignored)
├── .env.example                   # env manifest (dummy values)
├── .gitignore
├── AGENTS.md
├── backend/
│   ├── Dockerfile                 # multi-stage Go → debian:bookworm-slim
│   ├── go.mod / go.sum            # module github.com/kidversa/edutourism-v2/backend
│   ├── cmd/api/main.go            # env → db → migrate → seed → routes → listen
│   ├── internal/
│   │   ├── <domain>.go            # 24 vertical slices (routes+handlers+SQL; §2)
│   │   ├── envelope.go pscope.go idempotency.go   # cross-domain infra (§2)
│   │   ├── <domain>_test.go       # co-located unit tests
│   │   └── integration/           # cross-domain tests (tag: integration)
│   ├── migrations/NNNN_*.sql      # versioned schema (advisory-lock runner)
│   ├── prompts/                   # system.md, user.md (AI narrative)
│   └── templates/                 # wa_report.md, wa_consent.md
├── frontend/
│   ├── Dockerfile                 # multi-stage node → nginx:alpine
│   ├── nginx.conf                 # proxy /api, /public, /uploads → api:3000
│   ├── index.html
│   ├── package.json / vite.config.ts / tsconfig*.json / .oxlintrc.json
│   ├── vitest.config.ts / vitest.integration.config.ts
│   ├── public/                    # favicon.png, logo.png, icons.svg
│   └── src/
│       ├── main.tsx               # createRoot → router.tsx + index.css
│       ├── router.tsx             # BrowserRouter + RequireRole + group route spread
│       ├── index.css
│       ├── assets/
│       ├── lib/                   # api.ts stream.ts auth.tsx i18n.ts + helpers
│       ├── components/            # AppLayout, Protected, ConfirmDialog, StatusChip,
│       │                          #   StarInput, Pager, WaStatusBadge, PhotoCapture,
│       │                          #   StreamGate, TokenCountdown, SettingsModal, PageHeader
│       ├── pages/<group>/         # route-level pages; each of the 17 groups MUST own routes.tsx:
│       │                          #   activities, assessment, auth, children, consent, dashboard,
│       │                          #   frames, gallery, missions, profile, programs, reports,
│       │                          #   sessions, tenants, topics, users, view — spread into router.tsx (decision 36;
│       │                          #   the former admin/ + masters/ aggregate route files were dissolved into
│       │                          #   their per-page groups — audit round 11)
│       ├── report/                # ReportView.tsx report.css export.ts types.ts
│       ├── locales/{id,en}/       # 7 namespaces each: common menu auth master session assessment report
│       └── test/                  # setup.ts handlers.ts render.tsx (+catalogues/helpers)
├── data/                          # runtime data (bindmounts, gitignored except .gitkeep)
│   ├── mariadb/                   # MariaDB datadir
│   ├── openwa/                    # OpenWA /app/data
│   └── uploads/                   # api UPLOAD_DIR: badges/<tenant_id>/, frames/<tenant_id>/, <tenant>/<sid>/<stid>/<cid>/ (05§8)
└── docs/
    ├── spec/                      # 00–08 + SPEC-INDEX.md (10 spec docs) + DESIGN.md
    ├── superskill/task/
    ├── audit/
    └── efficient-engineering-review/
```

Decisions (2026-10-11):
- **33** — Upload path tenant-di-luar for badges/frames: `badges/<tenant_id>/…`,
  `frames/<tenant_id>/…` (photos stay `<tenant_id>/<session_id>/<stid>/<child_id>/…`).
  Canonical in `05§8`; supersedes the tenant-di-dalam wording in `04-FL-04/05`
  and the former code (`<tenant_id>/badges|frames/…`) — reconciled via
  Change 16; the code now writes tenant-di-luar (Change 17, decision 45).
- **34** — Runtime file storage is `data/uploads/` (host) ← `UPLOAD_DIR`
  `/data/uploads` (container). There is NO `backend/uploads/` dir.
- **35** — `backend/internal/` ships three cross-domain infra modules beyond the
  24 slices: `envelope.go`, `pscope.go`, `idempotency.go` (listed in `02§2`).
- **36** — Every `pages/<group>/` group MUST own a `routes.tsx`; `router.tsx`
  spreads the per-group route tables.
- **37** — `docs/spec/` = 10 spec docs (00–08 + SPEC-INDEX.md); `DESIGN.md` is
  extra and outside the SPEC-INDEX document table (non-binding visual
  design-pattern reference, YowTix — decision 50). `SPEC-INDEX.md` lists a self-row.

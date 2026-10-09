# 07 — Before / After

Status: DRAFT (2026-10-09; adding test strategy + A4 fixed report; pending audit)

Fresh build: BEFORE = empty project directory (only `.omp/skills`).
Every change is greenfield; the table records what did not exist, what
was requested, and what the system MUST do. No migration, no shims.

## Change 1 — Tenancy, roles, transport envelope

### BEFORE
No users, no tenants, no roles.

### REQUESTED CHANGE
3 login roles (SA, Admin, Facilitator); generic tenant; SA switcher;
email globally unique. Post-audit user decision: ALL `/api/*` and
`/public/*` return HTTP 200 with `{data}` / `{error:{code,message}}`;
only unhandled faults → 500. No 4xx statuses.

### AFTER
`tenants` + `users(tenant_id)`; every query tenant-filtered;
`X-Tenant-ID` header + `TENANT_MISMATCH` on stale tab; email UNIQUE
global; seed Bandung + SA (idempotent). Codes: `TENANT_INACTIVE`
(single code; `TENANT_DISABLED` removed).

### AFFECTED COMPONENTS
`tenants.go users.go auth.go` middleware; AppLayout switcher (`kv-tenant`).

### DATA / FLOW / ERROR IMPACT
All domain/link/snapshot rows carry `tenant_id`; login/refresh enforce
tenant status; client branches on envelope, never HTTP status.

## Change 2 — Curriculum master (Program → Topik → Kegiatan)

### BEFORE
Nothing.

### REQUESTED CHANGE
Activity = assessment unit; badge both-or-neither; program usable only
if topics+activities exist.

### AFTER
`programs.go topics.go activities.go` + eligibility gate
(`PROGRAM_INCOMPLETE`); images PNG/JPG/WebP ≤2 MB (ICO rejected).

## Change 3 — Session snapshot + groups (no CANCELED)

### BEFORE
Nothing.

### REQUESTED CHANGE
Session copies one whole program + groups; Draft→Opened→Finished;
reopen by ADMIN/SA. Post-audit decision: NO CANCELED state —
Draft/Finished DELETE = physical wipe, no trace.

### AFTER
`sessions.go` Tx snapshot into `session_*` + `UNIQUE(child,session)`;
guarded transitions (`STATE_CONFLICT`); `GROUP_WITHOUT_FACILITATOR`,
`SESSION_LOCKED`, `CHILD_DUPLICATE`. Idempotency-Key on create.
Round 5: `groups[]` OPTIONAL at create (Draft may start empty; gate at
open: ≥1 group + non-empty + active owner).

## Change 4 — Assessment (stars + notes, per activity)

### BEFORE
Nothing.

### REQUESTED CHANGE
Star 1–4 (=BB/MB/BSH/BSB) + note per child per activity, per-card save,
partial allowed; frozen at Finished (photos free); completion-only badges.

### AFTER
`assessments.go` upsert on snapshot ids (`said`); StarInput; progress;
derived badges; `NOT_OWNER` for foreign groups; LWW documented.

## Change 5 — Photos, frames, gallery, consent

### BEFORE
Nothing.

### REQUESTED CHANGE
Max 10/child/topic/session; 1 candidate (changeable even Finished);
9:16 fused capture or upload; gallery Session→Topic→Group→Child;
per-child-per-session WA consent (default locked, permanent first-wins).

### AFTER
`photos.go consents.go` + PhotoCapture + gallery; Tx count + partial-
unique candidate; repeat consent submit = 200 success `already:true`
(no error code exists for repeat submit). Round 5 membership gate:
consent-send + every per-child `P` op require `session_members` row,
else 200 `NOT_FOUND`.

## Change 6 — AI narrative

### BEFORE
Nothing.

### REQUESTED CHANGE
Per child per topic via server-side Gemini; file templates
(`prompts/system.md` + `user.md`, ≤50 words ID); button-triggered,
editable, regenerable; failure keeps old text.

### AFTER
`ai.go` + narrative endpoints; `models/{MODEL}:generateContent`
(user doc snippet 2026-10-08, default `gemini-3.5-flash`); `AI_FAILED` +
`NOTHING_TO_NARRATE`; Idempotency-Key.

## Change 7 — Report + public links + WA (link-only)

### BEFORE
Nothing.

### REQUESTED CHANGE
Per child per session per topic; gate = all starred; screen + client
PNG/PDF (never stored); missions manual pick ≤4; public report+gallery
URLs (revocable); WA single + batch with jitter. Post-audit decision:
WA is LINK-ONLY (text + 2 URLs via `send-text`); no file attachments —
server never handles report bytes.

### AFTER
`reports.go public.go wa.go` + plain-CSS ReportView
(`toPng→addImage`, never `doc.html()`); `REPORT_INCOMPLETE`,
`MISSION_LIMIT`, per-item `WA_FAILED`; batch max 10/request with
3–10 s jitter; `wa_outbox` SENT/FAILED only (no QUEUED).

## Change 8 — Auth/session/reset (hashed + reveal)

### BEFORE
Nothing.

### REQUESTED CHANGE
Access 15 min + refresh 7 d (whitelist, rotate); password min 8
letters+numbers; reset code 15 min single-use mirrored to dashboard.
Post-audit decision: store `code_hash` + `code_plain`; dashboard shows
masked code + countdown; plaintext reveal-on-demand (audited) + revoke.

### AFTER
`auth.go` (keyauth+extractors, HS256 pinned, 60 s leeway) + bcrypt;
`reset_tokens` with hash/plain/masked/revoke/purge; limiter 200 +
`Retry-After`.

## Change 9 — Docker & repo structure

### BEFORE
Empty repo.

### REQUESTED CHANGE
4 services, bindmounts, 1 compose, `env_file` only, no secrets in images,
local 1:1 prod, flat vertical slices, antd + plain-CSS report, ID/EN.

### AFTER
`compose.yml`, Dockerfiles, `backend/…`, `frontend/…`, `data/` per
`02-ARCHITECTURE.md`; versions per `01-TECHSTACK.md`; page routes
`/view/…` vs JSON `/public/*` disambiguated. Round 5: `wa` image locked
to `openwa-openwa:latest` (verified local 2026-10-08).

## Change 10 — WA status badge + copy-data antar-sesi (round 6)

### BEFORE
No WA health signal in the SPA (activation only via OpenWA `:8003`);
no cross-session copy (each session fully independent).

### REQUESTED CHANGE
(B) `WaStatusBadge` in SA/ADMIN dashboard (proxy status, 30 s poll,
click opens OpenWA); activation stays in OpenWA only. Copy-data modal
"Salin nilai dari Sesi X?" on member-add when the child is in other
same-program sessions: fill-empty only, match by master ids, copy
stars+notes/narratives/missions/photo files (physical), never
consent/links/outbox/badges; Tolak deletes ALL photo rows + files for
that child+session (original + copied).

### AFTER
`sessions.go` + `GET/POST .../copy-candidates|copy` (one Tx + post-commit
`io.Copy`, Idempotency-Key, per-status guards) + `GET /api/wa/status`
(200 status-only) + `WaStatusBadge` (green/red, EVENT-DRIVEN round 7 —
server polls OpenWA, badge renders `wa.changed`, no 30 s client poll);
FL-16 + F-15;
acceptance rows in 08.

### AFFECTED COMPONENTS
`sessions.go` (copy), `dashboard` pages + `WaStatusBadge`, member-add
modals, consent DITOLAK path (photo cleanup).

### DATA / FLOW / ERROR IMPACT
New rows in target snapshot (copier as `assessor_id`); disk files
duplicated (not FK); unmatched topics stay empty; DITOLAK deletes ALL
photo rows + files for that child+session (original + copied);
source-mid-delete → `NOT_FOUND` rollback.

### EDGE CASE IMPACT
Multi-source dropdown (FINISHED-first); re-run idempotent; FINISHED
target partial (photos only); cross-program → empty candidates.

### OPEN DECISIONS
None for this change.

## Change 11 — Realtime SSE: 1 endpoint, signal + refetch (round 7)

### BEFORE
Pure request–response + blind polling: `TokenCountdown` polls
`GET /api/reset-tokens` every 5 s; `WaStatusBadge` polls
`GET /api/wa/status` every 30 s; dashboard memos 60 s. No socket, no
subscription, no invalidation: admin B on the program list never learns
that admin A added a program until manual reload. Photo upload clients
could optimistically render local blobs as if they were server copies.

### REQUESTED CHANGE
Migrate every data flow to SSE-driven invalidation: 1 endpoint
(`GET /api/stream`) + 1 thin public endpoint
(`GET /public/stream?token=`), all frames signal-only
(`{entity, entity_id, rev}` — no PII/bytes/tokens), clients refetch
ordinary REST on signal. Photo/file bytes MUST be honestly served from
server disk only — SSE never carries bytes and never announces a file
before it is durably stored (no fake status).

### AFTER
`stream.go` hub (in-memory fan-out per `(tenant, scope)` + ring 100
events / 5 min / tenant; single `api` replica for MVP) +
`GET /api/stream` + `GET /public/stream?token=` (`text/event-stream`,
handshake = same guards as REST, limiter-exempt with connection caps) +
`lib/stream.ts` (`fetch` + reader, routing table, 300 ms coalesce,
`rev`-gated refetch, reconnect + `Last-Event-ID`) + publish-after-commit
in every mutating handler (best-effort) + send/skip filter REUSING the
REST scope functions + 4 full-value exceptions
(`readiness/pending/wa/consent-ack`) + honest-file rule (temp + `fsync` +
atomic rename; `photo.changed` only after durability; transient local
preview → 200 → signal → refetch → server bytes). Polling deleted
(5 s / 30 s / 60 s → events). Full catalogue: `02§2c`.

### AFFECTED COMPONENTS
`stream.go` (new), every mutating slice (publish line each), `main.go`
(route + caps), nginx (stream `proxy_buffering off`), `lib/stream.ts`
+ `StreamGate` (new), `WaStatusBadge` + `TokenCountdown` (event-driven),
`PhotoCapture` (transient preview), all list/detail pages (refetch on
signal), `/view/*` pages (public stream).

### DATA IMPACT
New wire shape: SSE signal frame (§05.3 — id+rev+routing keys only, never
an envelope). No schema change (no new tables; `rev` in-memory +
`created_at` fallback). File durability becomes a visibility gate:
single upload = file-durable FIRST then Tx insert; copy-data = rows Tx
FIRST, `file_ready=FALSE`, flip TRUE per file, POST 200 WITHHELD until
durable, `ONLY file_ready=TRUE` served, stuck FALSE >24 h janitor-deleted
(never `PHOTO_MISSING` — that flag is ONLY for TRUE rows whose file is
gone); orphans swept, never announced.

### FLOW IMPACT
Every FL-01..FL-16 gains a publish → signal → refetch tail (documented
per flow). FL-10 rewritten around honest bytes (preview → 200 → signal
→ refetch → server `<img>`). FL-14/FL-03 polling replaced by
`stats/wa/reset.changed` events. FL-15 pages open the public stream.

### ERROR IMPACT
No new error codes (stream carries no `{error}`); handshake failures use
existing 200 codes without opening a stream. New propagation: publish
best-effort drop + `slog`; flush-fail → close + reconnect + replay/gap-
refetch; `auth.expiring` → refresh-once → reconnect. Filter bugs bounded:
frames carry no PII (worst case a skipped signal, never leaked data).

### EDGE CASE IMPACT
Batch burst → coalesce; replay overlap → `rev`-gate; restart → ring
empty → full-refetch once; tenant-switch → close + reopen; crash
file-without-row → swept unannounced; crash row-without-publish →
memo/refetch closes the gap; public revoke → `link.revoked` + close.

### OPEN DECISIONS
None for this change (design locked rounds 7a–7c: 1 endpoint,
signal-only, 4 full-value exceptions, public stream included).

## Change 12 — Paging server-side pure + page-size 20/50/100/1000 (round 8)

### BEFORE
Paging contract inherited from draft: envelope
`{data, meta:{page,per_page,total}}`, defaults `page=1, per_page=20`,
max 100 — but ownership of sort/filter/search/slice and the allowed
`per_page` ceiling were implicit, and a cancelled client-side
experiment (round 8 draft: full-set + `RESULT_CAPPED` + `DataTable`)
left no residue by user order (reverted, `DESIGN.md` ignored).

### REQUESTED CHANGE
User-confirmed 2026-10-09: scope, filter, sorter, search, pagination
ALL server-side; client holds only the active page; page-size selector
offers 20/50/100/1000 so operators can pull up to 1000 rows in ONE
envelope when they need a wide view; SSE signal → refetch SAME params
(no reset, no full-set hold, no client slicing).

### AFTER
`05§3` envelope locked: defaults `page=1, per_page=20`, allowed
`per_page` 20/50/100/1000 (max 1000), invalid → 200 `VALIDATION`;
`02§3` paging rule: filter/sort/search change resets to `page=1`,
SSE refetch preserves params (debounced 300 ms, `rev`-gated),
`meta.total` drives the pager; `08` paging acceptance block;
decision 25 in `SPEC-INDEX`.

### AFFECTED COMPONENTS
Every PAGED list handler (tenants, users, programs, topics, activities,
missions, frames, children, sessions, session-consents, consents,
reset-tokens — see `05§3`): apply scope + filter + allowlisted sort
(`sort`/`dir`, default per list, `id ASC` final tiebreak) + `LIMIT/OFFSET`,
return `meta:{page,per_page,total}`. Full-array endpoints
(detail/matrices/photos/narrative/picks/report/readiness/links/candidates/
counts/aggregates/public/picker) and `wa_outbox` (no list route) are
UNTOUCHED. Every list page: page-size selector 20/50/100/1000 + pager
from `meta.total`. `lib/stream.ts` refetch passes current params through.

### DATA IMPACT
No schema change. Wire change: `per_page` ceiling 100 → 1000
(backward compatible: old `per_page≤100` requests behave identically;
unknown keys still ignored). `ORDER BY` must be deterministic per
list so page boundaries are stable across refetch.

### FLOW IMPACT
Every list flow: filter/sort/search/page-size change → re-request
`page=1`; pager click → same filters, new `page`; SSE signal →
same-params refetch (never page-1 reset, never full dump).

### ERROR IMPACT
No new codes. Invalid `page`/`per_page` (non-integer, <1, >1000,
non-allowlisted custom value) → 200 `VALIDATION` (existing E1).
`per_page=1000` on huge scopes is bounded by Tx/statement timeouts
(5 s DB); timeout → 200 `DB_UNAVAILABLE` (existing E27), client
retries with smaller page-size.

### EDGE CASE IMPACT
Concurrent insert during paging → row may shift pages (accepted MVP;
SSE refetch converges). `per_page=1000` payload on slow links →
client shows progress + envelope stays ONE JSON (no chunking MVP).
Tenant-switch/logout mid-list → stream closes, list state resets.

### OPEN DECISIONS
None for this change.

## OPEN DECISIONS
None blocking. At implementation confirm from live service only:
OpenWA dashboard QR-pairing flow (text-only sends need no media-field
check). Locked round 5: `GEMINI_MODEL=gemini-3.5-flash` via
`:generateContent`; `wa` image `openwa-openwa:latest`.

# 07 — Before / After

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
> (2026-10-12) dan dikunci sebagai keputusan 51–72 (lihat `SPEC-INDEX`);
> seluruh butir §4 kini TERTUTUP. Rincian: **Change 19** di bawah +
> section indeks pembaruan di `SPEC-INDEX.md`.

## [PEMBARUAN 2026-10-11] Ketentuan Upload Baru

> Penanda pembaruan: seluruh perubahan ketentuan upload per 2026-10-11
> ditandai heading `[PEMBARUAN 2026-10-11]` agar mudah ditemukan.
> Ketentuan upload lama di berkas ini (≤2 MB untuk badge/frame, ≤10 MB
> untuk foto, validasi mime/ukuran server-side) DICABUT dan DIGANTIKAN
> ketentuan di bawah ini; dicatat sebagai **Change 15** di bawah.

- **Ukuran maksimum: 50 MB** per berkas yang di-upload (foto, badge
  program/topik, frame).
- **Validasi: client side validation only** — pemeriksaan format dan
  ukuran dilakukan client sebelum file dikirim; server TIDAK melakukan
  validasi ukuran/format (tanpa hard cap server-side, tanpa server mime
  sniff untuk validasi).
- **Format yang diizinkan: jpg, jpeg, png, webp** (case-insensitive);
  format lain (termasuk ico) ditolak client sebelum pengiriman.

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
(`PROGRAM_INCOMPLETE`); images PNG/JPG/WebP ≤2 MB (ICO rejected) — old
upload rule, superseded by Pembaruan 2026-10-11 (see Change 15).

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
target → entire copy rejected (200 `SESSION_LOCKED`); the photo part
never runs; cross-program → empty candidates.

### OPEN DECISIONS
None for this change.

## Change 11 — Realtime SSE: 1 endpoint, signal + refetch (round 7)

### BEFORE
Pure request–response + blind polling: `TokenCountdown` polls
`GET /api/reset-tokens` every 5 s; `WaStatusBadge` polls
`GET /api/wa/status` every 30 s; dashboard refetched only by client
memo age. No socket, no
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
preview → 200 → signal → refetch → server bytes). Polling + memo age
gates deleted (blind timers → events; refetch on SSE signal only). Full catalogue: `02§2c`.

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
next-signal refetch closes the gap (signal-only, no memo age); public revoke → `link.revoked` + close.

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
(allowlist is 20/50/100/1000; any other value → `VALIDATION` — the
allowlist introduced no behavior change for previously-valid 20/50/100
values; unknown keys still ignored). `ORDER BY` must be deterministic per
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

## Change 13 — Test strategy + A4 fixed report (round 9)

### BEFORE
No shared test-strategy decision: backend/frontend test tooling,
coverage targets, and report paper format were undefined; report layout
floated freely.

### REQUESTED CHANGE
User-confirmed 2026-10-09 (SPEC-INDEX decisions 26-28): backend = go
test + testify + httptest + go-sqlmock (mock DB, no Docker, no CI);
frontend = vitest + @testing-library/react/jest-dom/user-event + msw +
@vitest/coverage-v8; coverage ≥90% (business logic/components), 100%
critical paths; report = A4 portrait fixed (210×297mm, 10mm margins,
content 190×277mm, scale to ONE page, overflow notice,
`@page { size: A4 portrait; margin: 10mm; }`, PNG via toPng + PDF via
jsPDF format:'a4', same A4 layout scaled for screen preview).

### AFTER
`internal/*_test.go` co-located unit tests +
`internal/integration/*_test.go` (tag `integration`, sqlmock+httptest)
with parity/wire-pin suites; frontend `*.test.ts(x)` +
`*.integration.test.tsx` + `src/test/{setup,handlers,render}.ts(x)`;
configs `vitest.config.ts` / `vitest.integration.config.ts`,
`.oxlintrc.json`; `report/report.css` A4 @page +
`report/ReportView.tsx` scale-to-fit + overflow notice;
`report/export.ts` toPng→jsPDF with sanitised
`Rapor-<Child>-<Topic>-<YYYYMMDD>` filenames.

### AFFECTED COMPONENTS
`backend/internal/*_test.go`, `backend/internal/integration/*`,
`frontend/vitest.config.ts`, `frontend/vitest.integration.config.ts`,
`frontend/src/test/*`, `frontend/src/**/*.test.ts(x)`,
`frontend/src/report/*`.

### OPEN DECISIONS
None for this change.

## Change 14 — Spec-code sync remediation (round 10)

### BEFORE
The 2026-10-11 8-dimension sync audit (`docs/audit/2026-10-11-spec-codebase-sync-audit.md`)
found spec/code drift in both directions: 12 items where code deviated from
locked spec (no guarded status writes on assessment/narrative/mission PUTs —
a finish race could land a star in a FINISHED session; `POST /api/users`
missing server-side idempotency; stream handshake not checking a live
refresh session; session-wizard pickers not registered to SSE; WA batch
crash-replay stuck at IN_PROGRESS; TS strict off; A4 mini-report preview
not scaled; 5 unformatted test files; no draft-notice; cascade publishes
missing on program/topic delete; PATCH `{program_id}` silently ignored;
tenants `?is_active` filter dead) plus spec-list drift (idempotency list,
antd/React API lists, `sse.Config` shape, upload order wording, copy-data
guard wording, E7/E17/E18 wording) and SA lacking the promised
cross-tenant summary.

### REQUESTED CHANGE
User-confirmed 2026-10-11: fix spec first (10 spec files synced), then fix
all 12 code items, add `GET /api/dashboard/tenant-summary` (decision 29),
store AI output as-is (decision 30), implement `?is_active` (decision 31),
and add the missing acceptance-binding tests (AI_FAILED integration,
PHOTO_LIMIT race, assessor/generated_by bindings, negative copy pin,
DITOLAK-notes survival, inactive-child public report).

### AFTER
Code conforms: in-Tx `FOR UPDATE` status locks on assessment PUT /
narrative POST+PATCH / mission PUT (SESSION_LOCKED on wrong state, rollback);
`POST /api/users` wrapped in `s.idempotent` (replay returns the original
envelope); `GET /api/stream` handshake requires ≥1 live `refresh_tokens`
row (logout-revoked Bearer → INVALID_TOKEN, no stream); SessionCreatePage /
GroupsPanel refetch on `program.changed`/`child.changed`, MissionPanel
refetches the OPEN bank on `mission.changed`; batch-send replay reads back
recorded `wa_outbox` per-item statuses by idem key (no double-send);
`tsconfig.app.json`/`tsconfig.node.json` enforce `strict: true` (codebase
was already strict-clean); ReportsPage mini-report scales to the container
(no horizontal scroll); draft-notice `beforeunload` guard on AssessmentPage
with id/en strings; `deleteProgram`/`deleteTopic` publish one
`frame.changed`/`mission.changed` for the link-set change; PATCH with
`program_id` → SESSION_LOCKED before any read/write; `GET /api/tenants`
supports strict `?is_active=true|false` (invalid → VALIDATION); new
SA-only `GET /api/dashboard/tenant-summary` (per-tenant counts, `id ASC`,
no mixed aggregate) with an SA-only dashboard table refetched on
`stats.changed`; 6 acceptance-binding test groups added (incl. an
AI_FAILED integration test via a `GeminiBaseURL` test seam); `gofmt` clean.
Specs synced: 01 (idempotency list, React/antd reality, `sse.Config`, env-key
classes, jsdom, stdlib test note), 02 (`service_healthy`, tenants filter,
tenant-summary route, Pager/kv-session), 03 (SA summary, copy guards, upload
order, group status errata, AI as-is), 04 (upload order, copy guard, SA
stale-tab scope), 05 (consent_links PK note, `copy_available` hint, rev
counter, +62 wording, tenants filter), 06 (users idem, batch-vs-finish,
E17/E18, E7 scope, copy retry wording), 07 (Change 13 + this Change 14),
08 (test structure, AI wording, 8 new acceptance bullets, stale-tab SA
scope), SPEC-INDEX (decisions 29-31).

### AFFECTED COMPONENTS
`backend/internal/{assessments,reports,users,stream,programs,topics,sessions,tenants,dashboard,ai}.go`,
`backend/internal/{assessments,reports,users,stream,programs,topics,sessions,tenants,dashboard,photos,public,sessions_extra,reports_extra,misc_extra,files_extra,infra_extra}_test.go`,
`backend/internal/idempotency_test.go`, `backend/internal/length_caps_test.go`,
`backend/internal/integration/{stream,narrative}_test.go`,
`frontend/tsconfig.app.json`, `frontend/tsconfig.node.json`,
`frontend/src/pages/sessions/{SessionCreatePage,GroupsPanel}(.test).tsx`,
`frontend/src/pages/reports/{MissionPanel(.test).tsx,ReportsPage.tsx}`,
`frontend/src/pages/assessment/{AssessmentPage(.test).tsx}`,
`frontend/src/pages/dashboard/{DashboardPage.tsx,TenantSummaryTable.tsx,DashboardPage.test.tsx}`,
`frontend/src/locales/{id,en}/assessment.json`,
`docs/spec/{00,01,02,03,04,05,06,07,08,SPEC-INDEX}*.md`.

### OPEN DECISIONS
None for this change (items still requiring user confirmation from the
audit remain tracked in the audit report §4 and are untouched here).

## Change 15 — Upload rule change: max 50 MB, client side validation only (Pembaruan 2026-10-11)

### BEFORE
Server-enforced upload limits: photos ≤10 MB with server-side mime
sniff; program/topic badges + frames ≤2 MB (PNG/JPG/WebP, ICO rejected
→ 200 `VALIDATION`); nginx `client_max_body_size 12m` + Fiber
`BodyLimit: 12*1024*1024`.

### REQUESTED CHANGE
One upload rule for every file type: max 50 MB, formats
jpg/jpeg/png/webp, and CLIENT SIDE VALIDATION ONLY (the server
validates neither size nor format). Every change is marked with a
`[PEMBARUAN 2026-10-11]` update section in each spec file.

### AFTER
Binding upload rule (Pembaruan 2026-10-11): max 50 MB per uploaded file;
formats jpg/jpeg/png/webp (case-insensitive; anything else incl. ICO
rejected client-side before send); all size/format validation
client-side only — the server has no hard cap and no mime sniff for
validation. nginx `client_max_body_size 52m` + Fiber
`BodyLimit: 52*1024*1024` (50 MB + multipart overhead). `[PEMBARUAN
2026-10-11]` sections added to `00`..`08` + `SPEC-INDEX` (decision 32).

### AFFECTED COMPONENTS
`docs/spec/{00,01,02,03,04,05,06,07,08,SPEC-INDEX}.md` (spec-only change;
removing the existing server-side upload validation code + raising the
body limit in code is a separate implementation change).

### DATA / FLOW / ERROR IMPACT
`05`: server hard caps replaced by the client-side-only rule; `<ext>`
from the client-declared type. `06`: E1 no longer lists "bad mime/size".
`08`: badge acceptance keeps both-or-neither server-side; the
format/size assertion moves client-side.

### OPEN DECISIONS
None (user decision 2026-10-11, decision 32).

## Change 16 — Project structure: folder & file layout spec (decisions 33–37)

### BEFORE
Folder/file structure was implicit and inconsistent across specs and the
codebase: badge/frame upload paths contradicted each other
(`05§5` tenant-di-dalam vs `04-FL-04/05`); AGENTS.md referenced a
`backend/uploads/` dir that does not exist; `02§2` omitted three real
cross-domain modules; `02§3` implied a `routes.tsx` per page group while
only 5 groups had one; `SPEC-INDEX` document table had no self-row and the
`docs/spec/` file set was undocumented.

### REQUESTED CHANGE
User-confirmed 2026-10-11 (SPEC-INDEX decisions 33–37): add a normative
project-structure section `02§7`; make `05§8` canonical for upload/data-dir
paths with badge/frame **tenant-di-luar**; list `envelope.go`/`pscope.go`/
`idempotency.go` in `02§2`; require a `routes.tsx` per `pages/<group>/`;
and set the document index to 10 spec docs with a `SPEC-INDEX.md` self-row.

### AFTER
`02§7` = authoritative folder/file tree (root, backend, frontend, data,
docs) + decisions 33–37. `05§8` = canonical storage layout (badges/frames
tenant-di-luar) + runtime data dirs (`data/mariadb|openwa|uploads`).
`04-FL-04`/`04-FL-05` badge/frame paths aligned to tenant-di-luar.
`SPEC-INDEX.md` = self-row + decisions 33–37 + this Change 16. NOTE
(SUPERSEDED by Change 17 — decision 45): at the time this change was
written the badge/frame path decision **diverged from the then-current
code** (code wrote tenant-di-dalam) and an implementation change was
required to match; Change 17 landed that code change (new writes are
tenant-di-luar).

### AFFECTED COMPONENTS
`docs/spec/{02,04,05,07,SPEC-INDEX}.md` (spec-only; the badge/frame code
change is a separate implementation task). `AGENTS.md` note: its
`backend/uploads/` reference is superseded by decision 34 (`data/uploads`).

### DATA / FLOW / ERROR IMPACT
`05§8`/`04-FL-04/05`: badge/frame on-disk layout tenant-di-luar
(decision 33); photos unchanged. No schema, flow, or error-code impact.

### OPEN DECISIONS
None (user decisions 33–37). Residual implementation debt: badge/frame
storage code + tenant-wipe scan must move to tenant-di-luar (Change 16) —
CLOSED by Change 17 (round-11 code fixes + migration stance, decision 45).

## Change 17 — Spec↔code audit + total antd migration (2026-10-11, round 11)

### BEFORE
The 2026-10-11 audit & antd-migration round (`docs/superskill/task/2026-10-11-spec-audit-antd-migration.md`)
found, on top of round 10: **12 backend spec-vs-code deviations**
(F-B01..B17 fixed range), **15 frontend behavior deviations**
(DEV-F01..F15: routes.tsx on only 5/19 page groups, unlisted
`country-flag-icons` dep, missing `copy.completed` handler, batch retry
minting FRESH idem keys, missing `tenant.changed` context reload, no
`rev` gate on the public stream, value-events bypassing the `rev` gate,
non-retryable stream-handshake codes looping, copy modal clearing the
idem key at cap, assessment progress dots vs counter, MIME-vs-extension
over-reject, `accept` attr mismatch, a `sessionDate` test gap, no
typecheck script, vitest configs not typechecked), **107 non-antd UI
sites across 41 files** (custom buttons/inputs/selects/tables/dialogs,
div-grid layouts, `figure` photo grids, hand-rolled tabs/dropdowns), and
a set of **spec-INTERNAL contradictions** (package count 9-vs-7,
report "plain CSS" vs total-antd, copy-data FINISHED edge wording,
REPORT_INCOMPLETE enforcement point, `per_page` allowlist wording,
BodyLimit-52 MB vs "no server cap", public limiter key, stale decision
counts (28/32 vs 31→33 gap), tenant-wipe scope vs decision 33,
reset-dashboard field drift, antd catalog v6-vs-v5, component/page-group
inventory drift, `logger.Data` vs `*logger.Data`, `os.Getenv` only vs
`LookupEnv`, `test.tsx` vs `test.ts(x)` globs, FAS reduced-group shape
02§2b-vs-contracts).

### REQUESTED CHANGE
User interjection 2026-10-11: "semua keputusan wajib tertulis di spek,
dan pastikan tidak ada kontradiksi" — (1) fix the CODE to match the
spec (backend fixes F-B01..B17, frontend fixes F-F01..F15, parallel
execution), (2) record EVERY user decision in the spec (new locked
decisions 38–45 in `SPEC-INDEX`), (3) remove every contradiction from
the spec docs, (4) TOTAL antd: every non-antd UI element replaced with
direct antd components per the approved ambiguity register D-1..D-8
(report-interior exception, `@ant-design/plots` approved,
`country-flag-icons` retained, badge/frame migration stance, StarInput
→ antd `Rate`, `?q` on all paged lists, FAS combined group shape).

### AFTER
Decisions **38–45** locked in `SPEC-INDEX` (report UI split, plots dep,
flag-icons dep, StarInput `Rate`, total-antd + exceptions, `?q` on all
paged lists incl. consents/reset-tokens, FAS combined group shape,
badge/frame migration stance + wipe scope) and decision **32** added so
the locked list is contiguous **1–45**. Code fixes landed by section:
backend `F-B01..B17` (backend-fixer, task Section 6 — incl. guarded
status writes, `POST /api/users` idempotency, `sessionAlive` stream
probe, batch crash-replay, `copy_available` recompute on remove,
`?is_active`/`?q` filters, CORS 1–2 origin count, badge/frame
tenant-di-luar writes + three-dir tenant wipe, oversize-body →
`VALIDATION`), frontend `F-F01..F15` (frontend-fixer, Section 7 — incl.
`routes.tsx` for all page groups (17 after the former admin/ + masters/
aggregates were dissolved into per-page groups), `copy.completed` handler, batch key
reuse, stream `rev` gating, handshake non-retryable code handling) plus
the total antd repaint (antd-migrator, Section 8). All spec-internal
contradictions listed in BEFORE reconciled in `00`–`08` +
`SPEC-INDEX` + the binding contracts doc (01 §5 deps 7→9 + final antd
set + report split; 02 FAS shape/reset item/dashboard split/tenant-guard
clarifier/wa.changed transitions/auth.expiring `{exp}`/handshake
codes/19 page groups; 03 F-01/F-02/F-12/F-13/F-14; 04 FL-03/08/11/13/14/15;
05 frame routing keys/`q` scope/orphan grace/replay union/migration
stance/reset item; 06 E1/E17/E22/gate-vs-claim ordering/handshake
codes/gallery reasons/oversize body; 07 Changes 10/12 + this Change 17;
08 header + acceptance alignments).

### AFFECTED COMPONENTS
`backend/internal/*` (F-B01..B17 slices + co-located tests),
`frontend/src/*` (F-F01..F15 + antd repaint + rewritten StarInput
tests), `docs/spec/{00,01,02,03,04,05,06,07,08,SPEC-INDEX}.md`,
`docs/superskill/task/2026-10-09-contracts-shapes.md`.

### DATA / FLOW / ERROR IMPACT
Badge/frame NEW writes land in `badges/<tenant_id>/` +
`frames/<tenant_id>/` (existing data untouched, reads path-agnostic)
and the tenant hard-delete wipe covers all three dirs; the FAS group
wire gains `status` (derived from the session status) alongside `own`;
batch chunk retry REUSES the chunk's idem key (fresh key only for a new
item set); `?q` applies to 2 more paged lists (consents,
reset-tokens); stream handshake gains terminal `ACCOUNT_INACTIVE` /
`DB_UNAVAILABLE` (200 envelopes, no stream) + the `sessionAlive`
live-session probe; UI = antd everywhere EXCEPT the `#report-a4`
interior, the native `<video>` camera preview, and canvas capture
mechanics.

### OPEN DECISIONS
None (user decisions 38–45, 2026-10-11).

## Change 18 — Consistency audit round 12 + antd notification (Pembaruan 2026-10-12)

### BEFORE
Five parallel audit passes over all 11 `docs/spec/` files confirmed the
bookkeeping backbone was sound (decisions 1–45 contiguous, Changes
1–17 contiguous, `[PEMBARUAN 2026-10-11]` markers exactly where the
index tables claim, identical status values, supersede chains marked)
but surfaced cross-file inconsistencies and ambiguities: the binding
SSE filter predicate ("tenant equal") could not deliver the tenant-0
broadcast frames `05§6` mandates (`stats.changed`/`wa.changed`
undeliverable); a duplicate readiness route in `02§2b` with divergent
guard semantics vs the `GET P/readiness` used everywhere else; a
truncated `X-Tenant-ID` clause in `01§2`; stale §refs and file refs
(`01` Uploads bullet cited as §9 but living in §8; a referenced
`docs/ANTD-COMPONENT.md` that does not exist; the malformed "see
05§5 §8" citation; the `02§7` decision-33 recap still saying "current
code" writes tenant-di-dalam; the unmarked superseded Change-16 NOTE);
incomplete E1/E23/E32 When-cells and a codes-vs-enum mix-up in the
removed-codes note; garbled fragments/typos in `05` (`(retention
only.););`, "Refresh/consent/ DB-backed"); `FL-09` missing the
`GROUP_WITHOUT_FACILITATOR` branch that 03 F-08 + 06 E14 mandate;
`FL-13` missing its role tag; the SPEC-INDEX 04 row claiming
"FL-01..FL-16" (FL-17 exists); all 10 status lines stale at
"2026-10-09 … pending audit" although rounds 10–11 completed; the
backend critical-path enumeration omitting SSE in `01§10.1/§10.3` +
`08`; `oxlint` required as an acceptance gate but absent from every
approved package inventory; the 22 audit-§4 items unreconciled with
"DECISION REQUIRED: none"; `DESIGN.md` (a YowTix design guide) sitting
in `docs/spec/` with its stance only defined negatively; and no spec
rule at all for endpoint success/error UI feedback.

### REQUESTED CHANGE
User-confirmed 2026-10-12 (SPEC-INDEX decisions 46–50): (1) apply the
full consistency-repair set across `00`–`08` + `SPEC-INDEX` — every
item either reconciled from verified spec context (a binding/dated
sibling statement governs) or locked as a decision, none guessed;
(2) NEW binding UI rule — antd `notification` for every explicit user
action outcome (success + business error), SSE/background refetches
silent (decision 46; `02§3` + new `03 F-17` + `08` acceptance rows);
(3) drop the duplicate session-level readiness route (`GET P/readiness`
canonical — decision 47); (4) approve `oxlint` as a test dependency
(decision 48); (5) scope "DECISION REQUIRED: none" to locked spec
decisions, with the 22 audit-§4 items tracked NON-BLOCKING in the audit
report (decision 49); (6) `DESIGN.md` = non-binding visual
design-pattern reference, stays outside the spec set (decision 50);
(7) refresh all 10 status lines to 2026-10-12.

### AFTER
Reconciliations applied in place. `00`: upload section gains the 52 MB
transport-framing caveat every sibling carries; §6 package citation
split into `01§3`+`01§5` with the approved test-dependency carve-out;
§8 glosses gain the SA exemption, the per-item WA envelope carve-out,
and the `P` prefix definition. `01`: `X-Tenant-ID` sentence completed;
4 full-value exception frames noted in both SSE descriptions; handshake
list pointer → `02§2b`; `docs/ANTD-DOCS.md` (the file that exists);
SSE added to the backend critical paths (§10.1/§10.3, decision 26);
oxlint added to §10.4 (decision 48). `02`: filter predicate gains the
tenant-0 broadcast carve-out (frames with `tenant_id=0` go to every
authenticated connection, never public — consistent with `05§6`);
duplicate readiness route removed; `sessions`/`gallery` groups spelled
out; `SettingsModal` + `PageHeader` restored to the component
inventory; decision-33 recap aligned ("former code"; Change 17 landed);
new binding notification bullet (decision 46); decision-37 recap notes
the DESIGN.md stance. `03`: F-10 consent wording aligned with 04
FL-11 + honest-bytes invariant scoped with the copy-window
`{missing:true}` clarification; F-15 realtime wording aligned with
FL-16; new `F-17 Endpoint feedback notifications`. `04`: canonical
`05§8` citations; FL-09 gains the ownerless-group branch; FL-13 gains
its role tag. `05`: §1 principle gains the SA tenant-0 READ
clarifier; `consent_links` duplicate note removed; `(retention only.)`
typo fixed; unnumbered date-cite → decision 31; the `stid` routing-key
citation scoped to the `readiness.changed` exception; §6 garbled
fragment rewritten (refresh/consent DB-backed) + live broadcast fan-out
stated. `06`: E1 gains the disk-full trigger; E23 now points at the
full binding `01§limiter-policy` set + stream caps; E32 now points at
the §5 inactive-child gate list; removed-codes note splits error codes
from removed ENUM values; "UUIDs" → `<rand32hex>` per `05§8`. `07`:
Change-16 NOTE marked SUPERSEDED by Change 17; this Change 18.
`08`: header decision claim scoped (1–50 + audit-§4 note); backend
critical-path criterion gains SSE; new "Endpoint feedback
notifications" acceptance section. `SPEC-INDEX`: status + marker
section + decisions 46–50; rows 03/04/07/self updated (F-17, FL-17,
Change 18, decision counts); Uploads letak ref §9→§8; Implementation
notes moved below the decision list; Unresolved rewritten.

### AFFECTED COMPONENTS
`docs/spec/{00,01,02,03,04,05,06,07,08,SPEC-INDEX}.md` (spec-only; the
antd-notification implementation in `frontend/src` and any code change
from decisions 46–47 are separate implementation tasks).

### DATA / FLOW / ERROR IMPACT
No schema change, no new/renumbered error codes, no wire-shape change.
Routing impact: only the removal of the duplicate readiness route
(canonical stays `GET P/readiness`). SSE semantics unchanged — the
filter-predicate carve-out makes `02§2c` consistent with the `05§6`
broadcast ring that already existed. Decision 46 is a frontend UX rule
over existing envelopes.

### OPEN DECISIONS
None (user decisions 46–50, 2026-10-12). The 22 implementation-
confirmation items from the round-10 audit remain tracked NON-BLOCKING
in `docs/audit/2026-10-11-spec-codebase-sync-audit.md` §4 (decision 49).

## Change 19 — Audit-§4 closure (Pembaruan 2026-10-12, decisions 51–72)

### BEFORE
Decision 49 left the 22 implementation-confirmation items of
`docs/audit/2026-10-11-spec-codebase-sync-audit.md` §4 tracked but
unanswered ("PERLU KONFIRMASI PENGGUNA"), while the spec's own wording
carried the tensions those items described: reset-code reveal kept
plaintext alive after the first reveal; logout sat behind the authed()
middleware so an expired access token left a live ≤7-day refresh
cookie; `02§4`/`03 F-02`/`08` promised "SA read-only retained" for
inactive tenants while `02§2` (and the code) returned
`TENANT_INACTIVE`; StreamGate blocked SA sessions with
`tenant:null` and refresh-retry/rotation ignored forced logout and
tenant-active state; "reports resolve LIVE" collided with pick-time
mission snaps; eligibility (`Eligible` wire vs the is_active create
gate) and picker wording ("excluded with reason" vs "hidden"/"vanish")
diverged; FAS was locked out of `GET /missions` while F-12 mandated
their mission picker; FL-04's literal badge-file removal collided with
the code's share-check and with "sessions unaffected"; FL-08 read as if
the owner gate ran before the empty-group check; "blank = 24 h"
suggested a computed end; program deletes orphaned master rows and
skipped `frame.changed`; F-11 implied both prompt files were rendered
and the ≤50-word rule looked unenforced; the 2026-10-11 enforcement
clarification kept "public links never gated" (letting parents open
incomplete reports); public-link revoke/regenerate accepted any ADMIN's
token regardless of tenant; "star distribution per activity/topic"
promised a per-activity histogram the code and contracts doc never had;
copy-data carried three ambiguities (mid-execution wipe, "candidate
NEVER copied", the 4-exception claim vs `copy_available`); gate order
(validation vs membership) was unspecified; 5 backend files failed the
`gofmt` gate; the frontend coverage claim was unverifiable from
artifacts and oxlint showed 11 warnings against a "no lint errors"
gate; backups existed only as a docs sentence; the Docker live boot
claim was verified statically only.

### REQUESTED CHANGE
User answered all 22 §4 items on 2026-10-12 (SPEC-INDEX decisions
51–72), instructing: case studies + handling recommendations for the
business-rule items, spec updated per the answers, code fixes recorded
as separate implementation tasks. Key answers: reveal = single-shot
SELF-DESTRUCT (51); cookie-only logout (52); item 3 = no change (the
reset-dashboard superset shape was already unified 2026-10-11); SA READ
of inactive tenants allowed (53); SA `tenant:null` auto-picks the
latest active tenant for StreamGate (54); forced logout on double
INVALID_TOKEN + tenant-active check on rotation (55); mission text
renders LIVE (snaps history-only, 56); eligibility includes is_active
and inactive picker entries render disabled+reason (57);
`GET /missions` opened read-only to FAS (58); badge files always
deleted with their master + used-by-sessions typed-confirm warning
(59); deterministic open-gate order, empty-group first (60);
start/end both-or-neither, blank = NULL all-day (61); program delete
cascades master children + link rows + `frame.changed` (62);
system.md verbatim / user.md rendered, no word enforcement (63);
public-link CREATION gated while incomplete (64 — supersedes the
"links never gated" part of the 2026-10-11 clarification);
revoke/regenerate tenant-scoped (65); star distribution clarified as
global+per-topic (66); copy race accepted + candidate-flag wording +
`copy_available` hint clarified (67); membership-first uniform gate
order (68 — code reorder task); gofmt the 5 files (69); oxlint
zero-warning strict (70); backups explicitly manual-MVP (71); full
coverage run + Docker live boot recorded as open verification items
(72).

### AFTER
Marker sections added across `00`–`08` + `SPEC-INDEX` (letak table
below); in-place edits per decision in `00` (reveal, analytics),
`01` (oxlint gate wording ×2), `02` (SA inactive, logout cookie-only,
reveal ×2, links create gate, revoke tenant-scope, masters FAS read,
mission-live wording, StreamGate SA auto-pick), `03` (F-01 ×3,
F-04 ×3, F-05, F-08, F-11, F-12 ×2, F-13, F-15 ×2, F-16), `04`
(FL-03, FL-04, FL-05, FL-06 ×2, FL-07, FL-08, FL-12, FL-13 ×2,
FL-16), `05` (reset purge + masked, sessions CHECK, report_missions,
backup), `06` (E17 ×2, deactivated-child, tenant-inactive, copy race,
new logout/hardening/gate-order bullets), `08` (closure acceptance
section + oxlint gate + open-verification note), `SPEC-INDEX` (this
marker's index, decisions 51–72, rows, Unresolved rewritten — §4 now
CLOSED).

### AFFECTED COMPONENTS
`docs/spec/{00,01,02,03,04,05,06,07,08,SPEC-INDEX}.md` (spec-only).
Separate CODE tasks recorded by these decisions: reset purge on
reveal (51), cookie-only logout (52), SA-read tenant guard (53), SA
stream auto-switch (54), refresh hardening (55), live mission render
(56), Eligible-flag is_active alignment (57), missions route guard
(58), badge delete + confirm-count (59), master cascade + publish
(61 DDL, 62), link-create gate (64), revoke tenant-scope (65),
membership-first reorder (68), gofmt 5 files (69), oxlint 11 warnings
(70).

### DATA / FLOW / ERROR IMPACT
One DDL change (61: sessions both-or-neither CHECK rides the migration
update); no new/renumbered error codes; two behavioral wire changes —
`POST P/public-links` now answers `REPORT_INCOMPLETE` when incomplete
(64) and revoke/regenerate answers `NOT_FOUND` cross-tenant (65);
reset reveal becomes single-shot (51); copy mid-execution wipe now
documented as accepted partial-counts (67, matches code).

### OPEN DECISIONS
None (user decisions 51–72, 2026-10-12 — audit §4 CLOSED). Open
verification items (decision 72): full frontend coverage run + live
Docker compose boot, both pending dedicated sessions.

## OPEN DECISIONS
None blocking. Audit §4 is CLOSED (decisions 51–72). Open verification
items (decision 72): full frontend coverage run + live Docker compose
boot. At implementation confirm from live service only:
OpenWA dashboard QR-pairing flow (text-only sends need no media-field
check). Locked round 5: `GEMINI_MODEL=gemini-3.5-flash` via
`:generateContent`; `wa` image `openwa-openwa:latest`.

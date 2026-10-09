# 04 — Flows

Status: DRAFT (2026-10-09; adding test strategy + A4 fixed report; pending audit)

Canonical pipeline:

```text
Trigger → Client → Network → Server entry → Middleware → Validation
→ Authorization → Business logic → DB / external → Response (HTTP 200
  envelope) → Client processing → Final state → User-visible result
```

Transport rule (binding): every `/api/*` and `/public/*` returns HTTP 200
with `{data}` or `{error:{code,message}}`. Only unhandled faults return
HTTP 500 `{error:{code:"INTERNAL"}}`. Rate limiting returns 200 +
`Retry-After`. All flows below use snapshot ids inside sessions
(`stid=session_topics.id`, `said=session_activities.id`,
`gid=session_groups.id`). Every authenticated call sends `X-Tenant-ID`.
Realtime (round 7, binding): after every successful mutation below, the
server publishes a signal frame on the SSE hub (post-commit,
best-effort — never fails the mutation); other tabs' `lib/stream.ts`
receives it and refetches the listed REST endpoint (existing guards
apply). `GET /api/stream` + `GET /public/stream?token=` are the ONLY
non-envelope routes among `/api/*`+`/public/*` (`text/event-stream`); infra
`/livez|/readyz|/startupz` (plain-text) and `/uploads/*` (static bytes)
are likewise not envelopes but sit OUTSIDE the JSON rule per 02§2b.
Handshake failures return a normal 200 envelope and no stream opens.
Full event catalogue: `02§2c`.

## FL-01 Registration → approval → login

Trigger: visitor opens Register. Client validates email format, password
min 8 letters+numbers, WA digits. `POST /api/auth/register` JSON +
`X-Idempotency-Key`. Server: limiter → `Bind().Body` → email globally
unique? tenant active? role allowed? → bcrypt hash → insert
`is_active=false`. Returns 200 `{data:{id,status:"pending"}}`; replay
same key returns original. Login now → 200 `{error:{code:
ACCOUNT_INACTIVE}}`. SA sees pending admins (all tenants), ADMIN sees
pending facilitators (own tenant) via `GET /api/users?decision=PENDING` →
`POST /api/users/:id/approve|reject`. Rejected address is INTENTIONALLY
terminal (no re-register, no auto-delete — SA/ADMIN deletes the rejected
row explicitly if the person must re-apply, then the address is reusable).
Duplicate email → 200 `EMAIL_TAKEN`; inactive
tenant at register → 200 `TENANT_INACTIVE`. Realtime (round 7):
register publishes `user.changed` + `pending.changed {pending}`; approve/
reject publishes the same pair; other tabs' user tables + pending badges
refetch (no poll).

## FL-02 Login → JWT pair → tenant context

`POST /api/auth/login {email,password}` (limiter). Server: find user →
active? tenant active (or SA)? → bcrypt compare → access 15 min
(`sub,role,tenant`) + refresh 7 d jti → whitelist row → `Set-Cookie
refresh_token HttpOnly Path=/ SameSite=Lax (+Secure prod)`. Returns 200
`{data:{user,tenant}}`. Client: access memory-only; tenant
`localStorage kv-tenant` + header; SA switcher defaults latest tenant,
`POST /api/auth/switch-tenant {tenant_id}` validates. Refresh: 200 envelope
`INVALID_TOKEN` → `POST /api/auth/refresh` (cookie) → rotate
(insert new + revoke old, one Tx) → retry original once. Logout:
`POST /api/auth/logout` revokes jti. Wrong password → 200
`INVALID_CREDENTIALS`; inactive → 200 `ACCOUNT_INACTIVE`; member of
inactive tenant → 200 `TENANT_INACTIVE`; stale-tab write to previous
tenant → 200 `TENANT_MISMATCH` (client reloads context, re-switch).
Realtime (round 7): login opens no stream by itself (`StreamGate` opens
`GET /api/stream` on authenticated layout mount); logout closes the
tab's stream (handshake jti revoked → any reconnect attempt gets 200
`INVALID_TOKEN`, no stream); SA tenant-switch closes + reopens with the
new `X-Tenant-ID`. Proactive expiry (binding): server emits directed
`auth.expiring` at T-60 s per connection → client refreshes once →
reconnects the stream (missed while tab asleep → next write/refetch gets
200 `INVALID_TOKEN` → refresh-once → reconnect, max 1 retry, then re-login).

## FL-03 Forgot/reset (hashed code + masked dashboard + reveal)

`POST /api/auth/forgot {email}` (strict limiter) → always 200
`{data:{sent:true}}`. If user exists: 8-char code via `crypto/rand`,
store `code_hash=SHA256`, `code_plain`, `expires_at` 15 min, single-use;
SMTP attempt. `GET /api/reset-tokens` (SA all / ADMIN own-tenant) lists
`{code_masked:"***AB", expires_at, seconds_left, user, revoked}` (EVENT-
DRIVEN round 7: `reset.changed` → refetch list; `TokenCountdown` ticks
down locally 1 s from `expires_at` — no 5 s poll; the public forgot page shows only
"check email / ask admin", no countdown — it cannot call the authenticated
list endpoint); `?reveal=1` / `POST /api/reset-tokens/:id/reveal` returns
`code_plain` once (audit-logged); `POST /api/reset-tokens/:id/revoke` voids. Reset
`POST /api/auth/reset {email,code,new_password}` → hash compare
constant-time → bcrypt new → revoke ALL refresh + clear `code_plain`,
set `used_at`. Expired → 200 `RESET_EXPIRED`; used/revoked → 200
`RESET_USED`; weak password → 200 `VALIDATION`. Purge job hourly + sweep
on access. SMTP down: row still listed, admin reveals + relays manually.

## FL-04 Program → Topik → Kegiatan master [ADMIN/SA]

CRUD `/api/programs|/topics|/activities` + query filters. Validate names*;
badge both-or-neither (image PNG/JPG/WebP ≤2 MB) else 200 `VALIDATION`;
topic needs program*, activity needs program*+topic*. Badge files
`uploads/<tenant_id>/badges/`. Delete any time (snapshot isolation);
delete ALSO removes the badge file from disk in the same delete scope
(crash-mid-delete orphans swept hourly, never announced).
Eligible iff ≥1 topic and each ≥1 activity; else picker hides with reason,
direct session-create → 200 `PROGRAM_INCOMPLETE`. Cross-tenant id → 200
`NOT_FOUND` (no leak). Realtime (round 7): every master CRUD publishes
`program/topic/activity.changed`; other tabs' lists + pickers refetch —
admin B sees admin A's new program without reload (the motivating case).

## FL-05 Mission bank + Frame [ADMIN/SA]

Mission `POST /api/missions {program*,name*,body*,topic_ids[1+]}` (same
tenant). Frame `POST /api/frames` multipart (PNG/JPG/WebP ≤2 MB, ICO rejected → 200 `VALIDATION`) + `name*` + `program_ids[1+]` → `uploads/<tenant>/frames/`.
Both LIVE (resolved at pick/capture; picks snapped). Update/delete follow the master shape (`PATCH` validates same rules → 200 `VALIDATION` on bad mime/size/empty name; unknown/cross-tenant id → 200 `NOT_FOUND`; terminal = updated row or deleted + file swept). Realtime (round 7):
mission/frame CRUD publishes `mission.changed` / `frame.changed`; open
pickers + capture frame lists refetch.

## FL-06 Child CRUD + activate/deactivate [ADMIN/SA; FAS read-only — user-confirmed 2026-10-08]

Validate name*, age* int (1–10 ok; 11–15 needs `age_confirmed=true` via
modal; else 200 `VALIDATION`), school* free text, mother name* + WA*
(digits → `+62…`), father/guardian optional (same norm), tenant auto.
Deactivate → excluded from pickers/creates; history + public report stay.
Hard delete (typed confirm): removes members, assessments, photo rows +
disk, narratives, mission picks, consent/public rows (links revoked);
`wa_outbox` retained; masters untouched. Invalid WA → 200 `VALIDATION`.
Realtime (round 7): child CRUD/activate/deactivate/delete publishes
`child.changed`; hard-delete ALSO publishes `report-link.changed` per
revoked link + `link.revoked` on each revoked token's public stream
(same tail as session-wipe revoke — open parent pages show
"link invalid" + close); other tabs' lists + pickers refetch (deactivated child
vanishes from pickers without reload).

## FL-07 Session create (Draft) [ADMIN/SA]

Client picks eligible program → meta (name*, desc, date* past/present/future
allowed — backdate permitted, format-only validation; start/end blank=24 h with end>start check, location*) → groups[] OPTIONAL
(round 5: missing/empty allowed → Draft with zero groups; groups added
later; when present: name + active
owner + active children, dedup client+server). `POST /api/sessions` +
Idempotency-Key, one Tx: eligibility + in-tenant + owners active →
COPY program/topics/activities/badges (+names) into `session_*` → groups
+ members. Replay returns original `session_id`. Double child → 200
`CHILD_DUPLICATE`; owner inactive → 200 `GROUP_WITHOUT_FACILITATOR`;
ineligible program → 200 `PROGRAM_INCOMPLETE`; cross-tenant/unknown
program/child/owner id → 200 `NOT_FOUND` (no leak); end≤start →
200 `VALIDATION`. Create-with-`groups[]` recomputes the `copy_available`
hint at publish time and carries it INSIDE the `session.changed`
(`created`) frame's routing keys (create-with-groups emits NO separate
`member.changed` — same rule as member-add, same hint source) so the
copy modal can trigger immediately without a second add. Realtime (round 7):
create publishes `session.changed` (`created`); other tabs' session
lists refetch.

## FL-08 Lifecycle Draft → Opened → Finished (+reopen/delete)

- Open: `POST /api/sessions/:id/open` allowed from DRAFT only
  (already-OPENED → 200 `{data:{status:"OPENED", already:true}}`;
  on FINISHED → 200 `SESSION_LOCKED`); needs
  every group with an active owner (else 200 `GROUP_WITHOUT_FACILITATOR` +
  list) AND every group non-empty (else 200 `VALIDATION` + list) AND at
  least 1 group exists (empty Draft must add groups before open).
  Draft→Opened via `UPDATE … WHERE status='DRAFT'`; loser → 200
  `STATE_CONFLICT` (publishes nothing). Winner publishes
  `session.status` (round 7); other tabs refetch detail + status chip.
- Opened edits: groups/members/owner only
  (`PATCH /api/sessions/:id/groups/:gid`; group DELETE =
  cascade wipe of its members' session data per 02§2b, typed-text modal).
  Snapshot names/dates/program immutable (200 `SESSION_LOCKED`).
  Removing assessed child deletes their session assessments silently.
  Every group/member/owner mutation publishes `group.changed` /
  `member.changed` (round 7); member-add also recomputes the
  `copy_available` hint so other tabs' copy modals stay accurate.
- Finish: `POST /api/sessions/:id/finish` (partial allowed, warn list). Opened→Finished
  (guarded update; winner publishes `session.status`). Stars/notes frozen; photos free.
- Reopen (ADMIN/SA): `POST /api/sessions/:id/reopen` → Opened (writes resume; publishes `session.status`).
- DELETE Draft or Finished only — DELETE on Opened → 200
  `SESSION_LOCKED` (Finish first, then optionally delete Finished).
  (user decisions 2026-10-08: direct wipe, no
  soft-delete, no link-revoke gate, ADMIN+SA may delete, but ALWAYS via
  typed-text ConfirmDialog modal showing scope counts INCLUDING active
  report/gallery links + consent links): physical wipe of session
  scope (snapshot, groups, assessments, photos+disk, narratives, picks,
  consent/public links), no trace, no CANCELED state. Masters/children
  untouched. After wipe, old links → 200 `TOKEN_INVALID`; `wa_outbox`
  retained 90 d. Wipe publishes `session.deleted` (session list drops the
  row + open detail closes) + `report-link.changed` per revoked link +
  `link.revoked` on each revoked token's public stream. Group-DELETE
  cascade wipe publishes the same triple for the affected members' links.
  Cancelling during
  Opened/Finished is not a state — Finish then optionally delete Finished.

## FL-09 Assessment [FAS owner (+ADMIN/SA)]

`PUT /api/sessions/:sid/topics/:stid/children/:cid/activities/:said
{stars:1-4, note?}` (prefix P, no `:gid` — ownership resolved server-side;
partial ok). Checks: session Opened? caller is owner (via membership) or
ADMIN/SA? → upsert (single-row atomic, LWW accepted).
Client StarInput BB–BSB + per-card state + progress. Derived: topic
complete ⇔ all activities starred → badge (if defined); all topics →
program badge (values ignored). Finished → 200 `SESSION_LOCKED`
(read-only banner); DRAFT (not opened yet) → 200 `SESSION_LOCKED` (same
code — no assessment before open); stars outside 1–4 or note >2000 chars
→ 200 `VALIDATION`; foreign group (FAS) → 200 `NOT_OWNER`; unknown →
200 `NOT_FOUND`. Realtime (round 7): commit publishes
`assessment.changed` + `readiness.changed {complete}`; other tabs
refetch assessments + readiness (badges derived live, no polling).

## FL-10 Photo capture/upload + candidate (honest bytes, round 7)

Precondition consent Setuju else UI locked + API 200 `CONSENT_LOCKED`.
Non-member child on any `P` photo op → 200 `NOT_FOUND` (membership gate,
no leak); FAS foreign → 200 `NOT_OWNER`. Missing-file item → gallery
item `{missing:true}` + `PHOTO_MISSING` flag (report exports skip it —
never a top-level error envelope).
Capture `PhotoCapture` (portrait, frame preview, canvas 9:16 fuse) →
client holds a TRANSIENT local preview (blob URL, labelled
"mengunggah…", NOT in gallery, NOT candidate-able) →
`POST /api/sessions/:sid/topics/:stid/children/:cid/photos` multipart +
Idempotency-Key (prefix P; frame optional, program-linked only). Server: consent → mime sniff →
≤10 MB → Tx `COUNT(*) … FOR UPDATE` (<10 else 200 `PHOTO_LIMIT`) → write
file durable FIRST (temp + `fsync` + atomic rename into
`<tenant_id>/<session_id>/<stid>/<child_id>/<rand32hex>.<ext>`, numeric
IDs, ext from sniffed mime) → row commit in the same durable step (a row
without its file, or a file without its row, NEVER becomes visible) →
200 `{data:{photo}}` → publish `photo.changed` (signal only — no bytes,
only after durability; never on intent, never on preview). Upload same
endpoint as-is. Client on 200: drops the local preview, waits for
`photo.changed` (own signal included) → refetch `GET P/photos` →
renders gallery `<img src="/uploads/…">` from SERVER bytes. No success
toast, no gallery item, no candidate flag may appear before the server
200 (no fake status). Other tabs (incl. parent gallery page via
`gallery.changed` → refetch `GET /public/gallery?token=`) render the
same server bytes — never pushed bytes, never optimistic render.
Candidate `PATCH P/photos/:pid {is_candidate:true}`
(one Tx clear+set, partial-unique, changeable even Finished; publishes
`photo.changed`). Delete (row + file) frees slot (publishes
`photo.changed`). Crash between file-write and row-commit → janitor
sweeps the orphan file (never announced, never served).

## FL-11 Consent send → respond (no login)

ADMIN/SA `POST /api/sessions/:sid/children/:cid/send-consent` (Idempotency-Key; membership gate
round 5: child must be a `session_members` row for `:sid`, else 200 `NOT_FOUND`) → OpenWA
`send-text` to mother WA with `/view/consent?token=` from
`wa_consent.md`. Resend (same child+session, still PENDING, DRAFT/OPENED):
same token returned + WA re-sent (no new row — `UNIQUE(session,child)`).
Resend after decision → 200 `{data:{token, decision, resent:false}}`
(link unchanged, WA NOT re-sent); resend on FINISHED → 200
`SESSION_LOCKED` (WA not re-sent; token still viewable via
`GET /api/sessions/:sid/consents` list). Precedence (binding): FINISHED
wins over decided — a decided link on a FINISHED session returns
`SESSION_LOCKED`, never `{resent:false}`.
Same idem-key replay → original envelope.
FAS sees status only. Parent (no auth):
`GET /public/consent?token=` summary → submit `{token,decision,
respondent_name}` → first-wins `UPDATE … WHERE PENDING`; repeat returns
200 `{data:{decision, already:true}}` (idempotent success). Setuju unlocks
gallery permanently; Tolak/locked HIDES photos immediately (all roles —
gallery/API return locked, rows + files still on disk for the respond
instant) AND deletes in the same respond scope: photo rows deleted in Tx,
files deleted after commit before `consent.changed` publish (original +
copied ALL for that child+session — same rule as 03 F-15/04 FL-16; stars/notes/
narratives/missions stay; crash-mid-delete residue swept hourly, never
announced). Notes/report continue
unaffected. Bad/revoked → 200 `TOKEN_INVALID`. WA down → 200
`WA_FAILED` + Retry (link reusable). Realtime (round 7): send publishes
`consent.changed`; parent respond publishes `consent.changed` (auth
dashboards refetch consent list) + public `consent.ack {decision}` to
that token's stream (page shows recorded state without refresh).

## FL-12 AI narrative

`POST P/narrative` (Idempotency-Key; full path
`/api/sessions/:sid/topics/:stid/children/:cid/narrative`): gather
snapshot names + stars + notes → render `system.md` + `user.md`
(`strings.Replace`, no HTML-escape of prompt) → `POST
https://generativelanguage.googleapis.com/v1beta/models/{GEMINI_MODEL}:generateContent`
body `{contents:[{parts:[{text: rendered_prompt}]}]}` + headers
`x-goog-api-key` + `Content-Type: application/json` (15 s timeout;
user-locked 2026-10-08 from official doc snippet; default
`GEMINI_MODEL=gemini-3.5-flash`) → ≤50-word ID paragraph → upsert editable.
Empty set → 200 `NOTHING_TO_NARRATE`. Failure → 200 `AI_FAILED`
(old kept) + manual Retry. Guards (same as assessment): FINISHED target
→ 200 `SESSION_LOCKED`; non-member child → 200 `NOT_FOUND`; FAS foreign
→ 200 `NOT_OWNER`. Report works without it ("—"). Realtime
(round 7): success publishes `narrative.changed` (auth dashboards refetch
`GET P/narrative`) AND public `report.changed` for that child+session
(open parent report pages refetch their JSON — same dual-publish as
photo `photo.changed` + `gallery.changed`); manual PATCH publishes
the same pair; other tabs refetch `GET P/narrative`.

## FL-13 Report compose → export → links → WA (link-only)

Gate `GET P/readiness` → blocked 200 `REPORT_INCOMPLETE` + missing
list until all starred. Compose: `ReportView` (plain CSS), mission picker
`PUT P/missions {mission_ids[]}` (Tx replace, ≤4 else 200 `MISSION_LIMIT`),
narrative (FL-12), candidate indicator, derived badges, attestation:
LIVE owner name (binding, user decision 2026-10-08 option A — same as
03 F-12: shows the CURRENT group owner's name when the report/link is
opened; if the owner was deleted, falls back to `facilitator_name_snap`
from `session_groups`). Export client-only: `toPng(node,{cacheBust:true})`
→ PNG; `new jsPDF({orientation:'p',unit:'mm',format:'a4'})` + `addImage`
→ PDF `Rapor-<Child>-<Topic>-<YYYYMMDD>.{png,pdf}` (session date,
sanitised; single A4 scaled, overflow notice). Send path is gated per
topic too: batch items for incomplete topics return per-item 200
`REPORT_INCOMPLETE` (others send). Links `POST P/public-links {kind}`
(Idempotency-Key) → `/view/report?token=`
+ `/view/gallery?token=`; `POST /api/public-links/:token/revoke|
regenerate` (regenerate revokes old, one Tx). WA single `POST P/send-report
{to}` (if supplied, `to` must match one of the child's registered
numbers else 200 `VALIDATION`) / batch `POST /api/sessions/:sid/batch-send {items[]}` (per-item `to` same rule; plus
`X-Idempotency-Key` header; max 10, jitter 3–10 s): render `wa_report.md` + 2 URLs → `send-text` →
per-item `{status, message_id?, code?}` + `wa_outbox` row. No file bytes
touch the server. Failures 200 `WA_FAILED` per item + URL-copy fallback.
Realtime (round 7): mission replace-set publishes `mission-pick.changed`;
public-link create/revoke/regenerate publishes `report-link.changed`;
batch WA publishes ONE `report-link.changed` per session after the batch
(per-item statuses stay in the batch response envelope, not in events);
open parent pages receive public `report.changed` / `gallery.changed`
→ refetch their JSON (bytes from server only).

## FL-14 Dashboards & analytics

`GET /api/dashboard/overview|analytics?period=7d|30d|all` (tenant from
`X-Tenant-ID`; SA `?tenant=` override). Aggregates: children, sessions
open/finished, reports issued (= narratives + REPORT links), progress/
session, pending approvals; star distribution; facilitator activity.
Plain SQL, no OLAP; EVENT-DRIVEN round 7: server aggregator ticks 5–10 s
(not per-mutation) and publishes `stats.changed`; client refetches on
signal OR when data older than 60 s at mount/focus (stale-while-render
guard — NOT an interval poll; no timer fires without mount, focus, or
signal). DB slow → 200 `DB_UNAVAILABLE` +
Retry.

WA status badge [SA/ADMIN] (round 6): dashboard mounts `WaStatusBadge`
→ EVENT-DRIVEN round 7: server polls OpenWA `/api/health/ready` +
session info (5 s each) and publishes `wa.changed {connected}`; badge
renders `{connected}` directly and calls `GET /api/wa/status` only for
detail on demand (no 30 s client poll) → green (`connected:true`) / red
(`connected:false`, incl. OpenWA unreachable — status only, never
`WA_FAILED`); click opens OpenWA dashboard `:8003` in a new tab. Activation/QR pairing
happens ONLY in OpenWA dashboard (API key minted there, stored in `.env`
as `OPENWA_API_KEY`, `api` recreated after) — never in our SPA.

## FL-15 Public JSON + pages (no login)

JSON (limiter, 200): `GET /public/report?token=`, `GET
/public/gallery?token=`, `GET /public/consent?token=`,
`POST /public/consent/respond`. Revoked/unknown → 200 `TOKEN_INVALID`
(identical shape). Pages (SPA, no auth): `/view/report?token=`,
`/view/gallery?token=`, `/view/consent?token=` fetch JSON above via
`useSearchParams`. Tokens 32-byte hex (`crypto/rand`); limiter per IP.
EVENT-DRIVEN round 7: each open `/view/*` page also opens
`GET /public/stream?token=` (same token, scope-locked); server
`report/gallery.changed` → page refetches its JSON (bytes from server
only); `consent.ack {decision}` → consent page shows recorded state;
`link.revoked` (terminal) → page shows "link invalid" + stream closes.

## FL-16 Copy-data antar-sesi [ADMIN/SA; round 6]

Trigger: `POST /api/sessions/:id/groups/:gid/members {child_ids[]}`
(or session create with `groups[]`) includes a child who is already a
member of other same-program sessions (same tenant). Client (after the
add succeeds) calls `GET
/api/sessions/:sid/children/:cid/copy-candidates` → if non-empty, shows
modal "Salin nilai dari Sesi X?" with dropdown (default latest FINISHED,
fallback latest date) + Ya/Tidak. Tidak = stop (no copy, member stays).
Ya → `POST /api/sessions/:sid/children/:cid/copy {source_session_id}`
(Idempotency-Key) → server: source session/rows deleted mid-copy →
200 `NOT_FOUND`, Tx rolled back (no half-copy); same-tenant + same-program check (else 200
`VALIDATION`); inactive child → 200 `CHILD_INACTIVE`; target FINISHED →
200 `SESSION_LOCKED` for stars/notes/narratives/missions (photo part may
still proceed); match topics by master `topic_id`, activities by master
`activity_id`; fill-empty only (existing target stars kept; narratives
only-if-empty; missions only-if-zero-and-linked; photos only-if-target-
topic-zero, `io.Copy` files to new paths, cap 10 in Tx); `assessor_id` /
`generated_by` = copier; consent/links/outbox never copied; badges
recompute. Response 200
`{data:{copied:{assessments, narratives, missions, photos}, skipped:{...}}}`;
same key replay returns original counts. Later DITOLAK on target →
ALL photo rows + files for that child+session deleted (original +
copied — same rule as FL-11; stars/notes/
narratives/missions stay). Realtime (round 7): commit publishes
`member.changed` (already, at add time) + `copy.completed`; other tabs
refetch the target session (assessments + readiness + photos); copied
photo bytes render from server `/uploads/…` only (physical `io.Copy`
completed before publish — no fake status).

## FL-17 Test Flows (REQUIRED — user-confirmed 2026-10-09)

### FL-17.1 Backend Unit Test Flow (Go)

```text
Test trigger → go test → testify assertion → httptest request →
Fiber handler → mock DB (sqlmock) → assertion → coverage report
```

**Scope:**
- Business logic (validation, calculations, transformations)
- Middleware (auth, tenant guard, role check, limiter)
- Handlers (request parsing, response building, error mapping)
- SQL queries (with sqlmock)

**Structure:** `internal/<domain>_test.go` (co-located with source)

**Commands:**
- `go test ./internal/... -v -cover` (unit only)
- `go test ./... -coverprofile=coverage.out && go tool cover -html=coverage.out`

**Pass criteria:** All tests pass, coverage ≥90%, `go vet` clean, `gofmt` clean

### FL-17.2 Backend Integration Test Flow (Go)

```text
Test trigger → go test -tags=integration → httptest server →
Fiber app → real DB (dockertest) → HTTP request →
assertion → coverage report
```

**Scope:**
- Full request/response cycle
- SSE stream (publish, filter, replay, caps)
- Cross-domain flows (auth → session → assessment → report)
- Idempotency replay
- Error propagation

**Structure:** `internal/integration/*_test.go`

**Commands:**
- `go test ./internal/integration/... -v -tags=integration`

**Pass criteria:** All tests pass, coverage ≥90% (critical paths 100%)

### FL-17.3 Frontend Unit Test Flow (React/TS)

```text
Test trigger → vitest → @testing-library/react render →
user-event interaction → assertion → coverage report
```

**Scope:**
- Components (rendering, props, state)
- Hooks (useState, useEffect, useContext)
- Utilities (transformations, validations)
- Context (auth, tenant)
- API client (envelope handling, retry logic)
- SSE stream parser

**Structure:** `src/**/*.test.tsx` (co-located with source)

**Commands:**
- `vitest run` (unit only)
- `vitest run --coverage`

**Pass criteria:** All tests pass, coverage ≥90%, `tsc --noEmit` clean, `oxlint` clean

### FL-17.4 Frontend Integration Test Flow (React/TS)

```text
Test trigger → vitest → MSW server → @testing-library/react render →
user-event interaction → MSW API mock → assertion → coverage report
```

**Scope:**
- Full component flows with mocked API
- Form submissions (validation, error handling, success)
- Navigation (routing, guards, redirects)
- SSE stream (connection, parsing, refetch, reconnect)
- Report export (PNG/PDF generation, A4 fixed layout)

**Structure:** `src/**/*.integration.test.tsx`

**Commands:**
- `vitest run --config vitest.integration.config.ts`

**Pass criteria:** All tests pass, coverage ≥90% (critical paths 100%)

### FL-17.5 Test Matrix Summary

| Layer | Backend (Go) | Frontend (React/TS) |
|---|---|---|
| Unit test | `go test` + `testify` + `httptest` | `vitest` + `@testing-library/react` |
| Integration test | `go test` + `httptest` + `sqlmock` | `vitest` + `msw` + `@testing-library/react` |
| Coverage target | ≥90% business logic, 100% critical | ≥90% components, 100% critical |
| Critical paths | auth, validation, idempotency, tenant isolation, envelope, SSE | auth context, forms, report export, SSE, envelope |
| Structure | `internal/<domain>_test.go` | `src/**/*.test.tsx` |
| Pass criteria | all pass + coverage + vet + gofmt | all pass + coverage + tsc + eslint |

### FL-17.6 Test Execution

**Pre-commit (local):**
- Backend: `go test ./internal/... -v -cover` (unit only)
- Frontend: `vitest run` (unit only)

**CI pipeline:**
- Backend: `go test ./... -v -coverprofile=coverage.out` + `go tool cover -html=coverage.out`
- Frontend: `vitest run --coverage` + `tsc --noEmit` + `eslint`

**Integration tests (nightly or on-demand):**
- Backend: `go test ./internal/integration/... -v -tags=integration`
- Frontend: `vitest run --config vitest.integration.config.ts`

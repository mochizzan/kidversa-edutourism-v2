# 08 — Acceptance Criteria

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
> Relevan berkas ini: section acceptance penutupan §4 (51–70); gate
> oxlint zero-warning (70); catatan verifikasi terbuka (72). Rincian:
> `07` Change 19 + section indeks pembaruan di `SPEC-INDEX.md`.

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
  sniff untuk validasi); karena itu server tidak pernah mengeluarkan
  `VALIDATION` karena mime/ukuran file (PENGECUALIAN framing transport:
  body > 52 MB → 200 `VALIDATION`, lihat 06 §5 — framing permintaan,
  bukan validasi file per-berkas).
- **Format yang diizinkan: jpg, jpeg, png, webp** (case-insensitive);
  format lain (termasuk ico) ditolak client sebelum pengiriman.

Format: Given/When/Then. `DECISION REQUIRED: none` (all user decisions
locked in SPEC-INDEX, 1–72; audit §4 is CLOSED via decisions 51–72 —
the former decision-49 non-blocking bookkeeping is superseded; open
verification items tracked under decision 72). Every API assertion below means HTTP 200 with the named
`{error:{code}}` unless marked `500 INTERNAL`.

## Auth & tenant

- Given a visitor When registering (valid) Then 200
  `{data:{id,status:"pending"}}`; replay same `X-Idempotency-Key` Then
  same envelope, one row. Login now → 200 `ACCOUNT_INACTIVE`.
- Given a double-submit of `POST /api/users` with the same
  `X-Idempotency-Key` When replayed within 24 h Then 200 `{data}` of the
  ORIGINAL envelope and only one user row (no `EMAIL_TAKEN` mask).
- Given pending facilitator When ADMIN approves Then login works (access
  memory + refresh cookie + `kv-tenant`); When rejected Then stays inactive.
- Given valid credentials When logging in Then 200
  `{data:{access,user,tenant}}`;
  refresh rotates (single-use jti); logout revokes; parallel double
  refresh → loser 200 `INVALID_TOKEN` → re-login (no loop).
- Given all refresh tokens of a user revoked (logout everywhere) When
  opening `GET /api/stream` with the old Bearer Then 200 `INVALID_TOKEN`
  and NO stream opens.
- Given wrong password repeatedly When exceeding limiter Then 200
  `RATE_LIMITED` + `Retry-After` header.
- Given duplicate email When registering Then 200 `EMAIL_TAKEN` (globally
  unique); rejected address stays terminal until SA/ADMIN deletes the row.
- Given wrong email/password When logging in Then 200
  `INVALID_CREDENTIALS` (generic message, no oracle).
- Given expired access When calling Then 200 `INVALID_TOKEN` →
  refresh-once → retry once; lost-response rotation → re-login.
- Given SMTP down When forgot submitted Then still 200 `{sent:true}` BUT
  dashboard row shows `mail_failed:true` (the `MAIL_FAILED` signal) →
  admin reveals + relays manually.
- Given DB down When any op runs Then 200 `DB_UNAVAILABLE` + Retry;
  `/readyz` false; `per_page=1000` stall → same code → retry smaller.
- Given camera refused When capturing Then client `CAMERA_DENIED` state →
  upload fallback (no HTTP involved).
- Given missing disk file When gallery renders Then item `{missing:true}`
  + `PHOTO_MISSING` flag (report exports skip it — never top-level error).
- Given facilitator owning OPENED groups When deleted Then 200
  `HAS_OPEN_GROUPS` + list → reassign first.
- Given unhandled panic When any handler crashes Then HTTP 500
  `{error:{code:"INTERNAL"}}` (the ONLY 500 in the system).
- Given deactivated user When calling with old access Then 200
  `ACCOUNT_INACTIVE`; refresh rejected; client clears session.
- Given inactive tenant When member registers/logs in/calls Then 200
  `TENANT_INACTIVE`; SA READ of inactive-tenant data allowed, SA WRITE →
  `TENANT_INACTIVE` (decision 53).
- Given SA first login Then default tenant = latest created; switcher
  validates via `POST switch-tenant`; stale-tab write by a NON-SA member
  → 200 `TENANT_MISMATCH` → context reload (SA with a valid tenant header
  is always accepted; SA to a deleted tenant → `TENANT_INACTIVE`; SA to
  an inactive tenant → READ allowed, WRITE → `TENANT_INACTIVE`,
  decision 53; SA with `tenant:null` auto-picks the latest ACTIVE tenant
  for the stream — decision 54).
- Given forgot request When submitted Then always 200 `{sent:true}`;
  dashboard lists masked code + countdown; reveal returns plain ONCE
  then PURGES it — single-shot self-destruct (decision 51; subsequent
  reveal → 200 `VALIDATION`, masked only); reset with code works once (revokes ALL refresh, clears
  plain); expired → 200 `RESET_EXPIRED`; reused → 200 `RESET_USED`;
  weak password → 200 `VALIDATION`.

## Master data

- Given program without topics When picking for session Then hidden with
  reason; direct create → 200 `PROGRAM_INCOMPLETE`.
- Given badge name without image When saving Then 200
  `VALIDATION` (both-or-neither — server-side rule).
- Given oversize (>50 MB) or non jpg/jpeg/png/webp file When uploading
  Then client blocks it before send; the server does NOT reject on
  per-file mime/size (client side validation only — Pembaruan
  2026-10-11); a body over the 52 MB transport frame still → 200
  `VALIDATION` (request framing, 06 §5).
- Given program deleted after sessions exist When opening old session
  Then intact (snapshot isolation).
- Given mission linked to A+B When composing report for A Then offered;
  `PUT missions` with 5 ids → 200 `MISSION_LIMIT`; 4 replace-set in one Tx.
- Given frame linked to P1+P2 When capturing in P1 session Then offered;
  saved JPEG fused 9:16.

## Children

- Given age 11 + confirm When saving Then stored `age_confirmed=true`;
  age 0/16/non-integer → 200 `VALIDATION`.
- Given mother WA `812…` When saving Then stored `+62812…`; invalid →
  200 `VALIDATION`.
- Given FAS When calling `POST|PATCH|DELETE /api/children` Then 200
  `FORBIDDEN_ROLE` (user-confirmed 2026-10-08: FAS read-only; create/edit
  via ADMIN/SA); `GET` scoped to own-group children works.
- Given deactivated child When adding to group/session ops Then 200
  `CHILD_INACTIVE`; old reports still open; public gallery →
  `{photos:[], locked:true, reason:"CHILD_INACTIVE"}`.
- Given assessed child When hard-deleted (typed) Then members,
  assessments, photo rows+disk, narratives, picks, links gone;
  masters + `wa_outbox` kept.

## Sessions & groups

- Given complete program When creating with 2 groups Then Draft +
  snapshot rows; double child → 200 `CHILD_DUPLICATE`; same idem-key
  replay → same `session_id`, no duplicate.
- Given complete program When creating with missing/empty `groups[]`
  Then Draft with zero groups (round 5); When opening Then 200
  `VALIDATION` (+ list) until ≥1 non-empty group with active owner added.
- Given group without active owner When opening Then 200
  `GROUP_WITHOUT_FACILITATOR` + list.
- Given Opened When ADMIN adds child/removes group/reassigns Then
  allowed; removed assessed child loses session assessments silently.
- Given Opened When editing snapshot name/date Then 200 `SESSION_LOCKED`.
- Given a PATCH `/api/sessions/:id` containing `program_id` Then 200
  `SESSION_LOCKED` (snapshot program is immutable).
- Given Finished When saving star Then 200 `SESSION_LOCKED` (banner +
  disabled); photo upload still succeeds 200 `{data}`; reopen restores writes.
- Given concurrent open/finish When racing Then loser 200
  `STATE_CONFLICT` → reload.
- Given Draft DELETE / Finished DELETE via typed-text modal (with scope
  counts INCLUDING active report/gallery/consent links) Then session scope
  wiped without trace (old links → 200 `TOKEN_INVALID`; `wa_outbox` kept 90 d);
  masters/children kept. No CANCELED exists. No soft-delete, no link-revoke
  gate, ADMIN+SA may delete (user decisions 2026-10-08).
- Given FAS opening foreign group Then 200 `NOT_OWNER` (thumbnail only).

## Assessment & badges

- Given owner saves stars+notes per activity Then 200 per card; partial
  shows progress; completing all → topic badge in report (if defined);
  all topics → program badge; all stars=1 still badges (completion-only).
- Given non-member child When calling any per-child `P` op (assessment,
  photo, narrative, mission, report, public-link, send-report) Then 200
  `NOT_FOUND` (round 5 membership gate).
- Given an OPENED session that is FINISHED concurrently between the
  status pre-check and the assessment write Then the write is rejected
  with 200 `SESSION_LOCKED` (guarded status check inside the write
  transaction).
- Given a browser refresh or tab close with unsaved card input Then a
  draft-notice / beforeunload warning is shown (06§5).

## Photos & consent

- Given unanswered consent When capturing Then UI locked + 200
  `CONSENT_LOCKED`; notes save; report without photo.
- Given parent submits Setuju+name (no login) Then gallery unlocks
  permanently; repeat submit → 200 `{decision, already:true}` (no error).
- Given FAS When viewing consent page Then status read-only (send hidden);
  ADMIN/SA send → OpenWA text + `/view/consent?token=`; non-member child
  send → 200 `NOT_FOUND` (round 5 membership gate).
- Given 10 photos When uploading 11th (incl. race) Then 200 `PHOTO_LIMIT`.
- Given candidate set When changing post-Finish Then allowed; single
  candidate holds.

## Copy-data antar-sesi (round 6)

- Given child added to a group When the child is in other same-program
  sessions Then `GET .../copy-candidates` lists them (FINISHED-first)
  and the client shows modal "Salin nilai dari Sesi X?" (dropdown +
  Ya/Tidak); cross-program sessions never listed (empty → no modal).
- Given Ya with source When executed Then fill-empty only: existing
  target stars kept; unmatched target topics/activities (added later)
  stay empty; `assessor_id`/`generated_by` = copier; narratives
  only-if-empty; missions only-if-zero-and-linked; photo files physically
  copied to new paths only when target topic has 0 photos (cap 10 holds);
  consent/links/outbox never copied; badges recompute automatically.
  Same idem-key replay → original counts (no double-copy).
- Given PENDING target consent When copying photos Then copies proceed
  but gallery stays locked until Setuju; Given later DITOLAK Then ALL
  photo rows + files for that child+session are deleted (original +
  copied) while
  stars/notes/narratives/missions stay.
- Given FINISHED target When copying Then ENTIRE copy rejected → 200
  `SESSION_LOCKED` (stars/notes/narratives/missions AND photos all
  blocked — the photo part never runs, nothing is copied); Given FAS When
  calling copy endpoints Then 200 `FORBIDDEN_ROLE`.

## AI narrative

- Given complete topic When Generate Then output directed by prompt to be
  a ≤50-word ID paragraph, stored AS-IS (no programmatic truncation;
  decision 30), editable; regenerate replaces only on success
  (old kept on failure); empty set → 200 `NOTHING_TO_NARRATE`; Gemini
  down → 200 `AI_FAILED`, old kept.

## Report & WA (link-only)

- Given incomplete topic When sending (single `POST P/send-report` or a
  batch item) Then 200 `REPORT_INCOMPLETE` + missing list (other batch
  items still send); Given incomplete topic When composing Then the
  client readiness UI blocks compose (client-side only) — `GET P/report`
  still returns full data (`complete:false` + `missing`) and is NEVER
  gated.
- Given complete When exporting Then PNG (`toPng`) + PDF (`addImage`,
  A4, `Rapor-<Child>-<Topic>-<YYYYMMDD>`) download fully; server
  uninvolved; overflow shows single-page notice.
- Given links created When opening `/view/report|gallery?token=`
  logged-out Then full content; revoked/unknown → 200 `TOKEN_INVALID`
  identical; regenerate revokes old (one Tx).
- Given single send When pressed Then parent WA receives text + 2 URLs
  (no attachments); OpenWA down → 200 `{data:{status:"FAILED",
  code:"WA_FAILED",…}}` (per-item status INSIDE `data`, never a
  top-level `{error}`) + retry + copy URLs.
- Given batch of 10 When sent Then 3–10 s jitter; per-item statuses;
  one failure continues others; same idem-key retry → recorded statuses
  (no double-send).
- Given a batch send crashed after some OpenWA accepts When retried with
  the SAME `X-Idempotency-Key` Then recorded per-item statuses are
  returned inside `data` (no double-send).
- Given SA/ADMIN When opening dashboard Then `WaStatusBadge` shows green
  when the OpenWA session is `ready` (`wa.changed {connected:true}`
  renders directly; `GET /api/wa/status` only for detail) and red when the
  session is not linked or OpenWA is unreachable (`{connected:false}` —
  status only, never `WA_FAILED`). FAS has no badge. No 30 s poll exists
  (event-driven round 7).
- Given SA When clicking the badge Then the connect modal opens with
  **Open OpenWA** (opens `:8003` in a new tab) and **Connect Directly**
  (starts the session, renders the QR, polls until `ready`, then shows
  Connected + phone). Given ADMIN When clicking the badge Then nothing
  happens (the status popover still works). QR pairing is SA-only and may
  happen in the connect modal; it is never stored or sent over SSE.

## Realtime SSE — 1 endpoint, signal + refetch (round 7)

- Given admin A adds a program While admin B views the program list
  Then admin B's list updates without reload (`program.changed` →
  refetch `GET /api/programs`); the frame carries only
  `{entity, entity_id, rev}` (no names/PII/bytes).
- Given another admin editing programs/missions/frames linkage (program
  delete removes `frame_programs`; topic delete removes
  `mission_topics`) Then `frame.changed` / `mission.changed` are
  published and open pickers refetch (02§2c).
- Given FAS-foreign tab open on children/sessions While ADMIN mutates a
  foreign-group child Then the foreign tab receives NO frame for it
  (send/skip filter reuses REST scope); refetch would still 200
  `NOT_OWNER`/`NOT_FOUND` if forced.
- Given upload in flight When bytes not yet durable Then no gallery item,
  no toast, no `photo.changed` exists anywhere (no fake status); Given
  server 200 `{data}` + `photo.changed` When any tab/gallery refetches
  Then `<img>` resolves to server `/uploads/…` bytes (honest-file rule).
- Given disconnect mid-batch When reconnecting with `Last-Event-ID`
  Then missed signals replay from the ring; unclosable gap → one
  full-refetch of the active page (no stale forever, no double-write).
- Given access expiry approaching While stream open Then `auth.expiring`
  → refresh-once → reconnect with new Bearer + last id (max 1 retry,
  then re-login); Given tenant switch Then stream closes + reopens
  (no cross-tenant frame).
- Given SA/ADMIN dashboard open While the phone becomes unlinked Then
  `wa.changed {connected:false}` renders red without poll; Given reset row created/
  revealed/revoked/used Then `reset.changed` → dashboard refetches
  (no 5 s poll; countdown ticks locally from `expires_at`).
- Given parent gallery page open While facilitator uploads + consent
  Setuju Then `gallery.changed` → page refetches `GET
  /public/gallery?token=` and shows server bytes; Given link revoked
  Then `link.revoked` → "link invalid" + stream closes.
- Given bad token/role/tenant on handshake When opening a stream Then
  normal 200 envelope (`INVALID_TOKEN` / `FORBIDDEN_ROLE` /
  `TENANT_MISMATCH` / `TENANT_INACTIVE` / `TOKEN_INVALID`) and NO stream
  opens; Given a dead refresh session / DB failure on the `sessionAlive`
  probe Then 200 `ACCOUNT_INACTIVE` / `DB_UNAVAILABLE` and NO stream;
  Given over-cap handshake Then 200 `RATE_LIMITED` + `Retry-After`.
- Given source deleted mid-copy When executing Then 200 `NOT_FOUND`, Tx
  rolled back (no half-copy); Given disk full Then 200 `VALIDATION`.
- Given crash after commit before publish When client refetches Then row
  exists (at-most-once delivery, refetch closes the gap); Given orphan
  temp file Then swept hourly, never announced.

## Paging — server-side pure (decision 25)

- Given any PAGED list (05§3 split) When fetched default Then `meta:{page:1, per_page:20,
  total}` with ≤20 rows; page-size selector offers 20/50/100/1000;
  `per_page=1000` returns ≤1000 rows in ONE envelope.
- Given filter/sort/search change When applied Then client re-requests
  with `page=1` + same `per_page`; server applies scope + filter + sort
  + slice; invalid paging → 200 `VALIDATION`.
- Given SSE signal on an open list When debounced 300 ms Then client
  refetches SAME params (page/filters/sort preserved, `rev`-gated);
  `meta.total` drives the pager; client never slices or holds full-set.

## Dashboards/i18n/persistence

- Given ADMIN Then own-tenant counts + progress; SA switcher + summary;
  FAS own sessions + profile; SA `?tenant=` overrides header.
- Given SA When opening the dashboard Then the per-tenant summary table
  renders (`GET /api/dashboard/tenant-summary` — each row's numbers
  belong to ONE tenant; no cross-tenant mixed aggregate) and refetches on
  `stats.changed`.
- Given `GET /api/tenants?is_active=true|false` When called Then filtered
  rows return; invalid value → 200 `VALIDATION`.
- Given EN switch Then UI English (persisted `kv-lang`); report stays ID.
- Given restart Then sessions/assessments/photos/tokens persist; no WA
  re-pair (bindmount kept).
- Given fresh `docker compose up` with only `.env` Then 4 services boot,
  seed idempotent (Bandung + SA), migrations lock, `/readyz` gates
  traffic; QR pairing is the only manual step.

## Endpoint feedback notifications (user-confirmed 2026-10-12, decision 46)

- Given an explicit user action (submit/save/send/delete/generate/copy/
  approve/export/upload) When the response is a success envelope Then an
  antd `notification.success` card appears (outcome + i18n message).
- Given the same action When the response is a business-error envelope
  Then an antd `notification.error` card appears containing the i18n
  message + the error `code` (recovery hint where `06§1` provides one).
- Given an SSE-signal refetch or background page refetch When any
  response returns Then NO notification is shown (refetches stay
  silent).
- Given the notification implementation When inspected Then it uses the
  antd `useNotification` hook (static methods deprecated) with antd
  defaults (`topRight`, ~4.5 s auto-close).

## Audit-§4 closure (user-confirmed 2026-10-12, decisions 51–72)

- Given logout with ONLY the refresh cookie (access expired/absent)
  When called Then 200, jti revoked, cookie cleared (decision 52).
- Given refresh-retry returning INVALID_TOKEN twice consecutively When
  observed Then the client forces logout (clear session → login);
  refresh rotation against an inactive tenant → revoke + clear +
  200 `TENANT_INACTIVE` (decision 55).
- Given a mission in the bank edited after being picked When the report
  link is opened Then the report shows the CURRENT bank name/body
  (decision 56 — snaps are history-only).
- Given a program/topic `is_active=false` or without ≥1×1 activity When
  the session-create picker opens Then the entry is DISABLED with
  reason (decision 57); direct API create → 200 `PROGRAM_INCOMPLETE`.
- Given FAS When calling `GET /api/missions` Then 200 with own-tenant
  list (decision 58; other master routes stay ADMIN/SA → `FORBIDDEN_ROLE`).
- Given a program used by ≥1 session When ADMIN deletes it Then the
  typed confirm shows the affected session count; after confirm the
  badge file is gone and old sessions render WITHOUT the badge section
  (decisions 59/62).
- Given a session with a group that is empty AND ownerless When opened
  Then 200 `VALIDATION` (empty-group check first, decision 60).
- Given start filled without end (or end without start) When submitted
  Then 200 `VALIDATION`; both blank → NULL all-day; both set →
  `end>start` enforced (decision 61).
- Given an incomplete topic When `POST P/public-links` is called Then
  200 `REPORT_INCOMPLETE` (no link exists; decision 64).
- Given an ADMIN of tenant B with a valid token of tenant A's link When
  calling revoke/regenerate Then 200 `NOT_FOUND` (no-leak;
  decision 65).
- Given a non-member child with an INVALID payload When calling a
  per-child P op Then 200 `NOT_FOUND` (membership checked first —
  decision 68); consent send to a non-member in a FINISHED session →
  200 `NOT_FOUND` (not `SESSION_LOCKED`).
- Open verification items (decision 72): full frontend coverage run
  (`vitest run --coverage`, writes artifacts) and live
  `docker compose up` boot (4 services + seed + `/readyz`) are PENDING
  dedicated sessions — verified statically only to date.

## Test Acceptance Criteria (REQUIRED — user-confirmed 2026-09-09)

### Backend Unit Test (Go)

- Given `go test ./internal/... -v -cover` When executed Then all tests pass,
  coverage ≥90% (business logic), 100% (critical paths: auth, validation,
  idempotency, tenant isolation, envelope transport, SSE — decision 26).
- Given `go vet ./...` When executed Then no warnings.
- Given `gofmt -l .` When executed Then no files listed (all formatted).
- Given `go test ./internal/... -run TestAuth` When executed Then auth tests
  pass (login, refresh, logout, reset, JWT validation).
- Given `go test ./internal/... -run TestValidation` When executed Then
  validation tests pass (input validation, business rules, error codes).
- Given `go test ./internal/... -run TestIdempotency` When executed Then
  idempotency tests pass (X-Idempotency-Key replay, no double side-effect).
- Given `go test ./internal/... -run TestTenant` When executed Then tenant
  isolation tests pass (cross-tenant access blocked, tenant guard).
- Given `go test ./internal/... -run TestEnvelope` When executed Then
  envelope transport tests pass (HTTP 200 + `{data}` / `{error:{code}}`).

### Backend Integration Test (Go)

- Given `go test ./internal/integration/... -v -tags=integration` When
  executed Then all integration tests pass (full request/response cycle,
  SSE stream, cross-domain flows).
- Given SSE stream test When executed Then publish, filter, replay, caps
  tests pass.
- Given idempotency replay test When executed Then replay returns original
  envelope without second side-effect.
- Given error propagation test When executed Then DB down → 200
  `DB_UNAVAILABLE`, OpenWA down → 200 `WA_FAILED`, Gemini down → 200
  `AI_FAILED`.

### Frontend Unit Test (React/TS)

- Given `vitest run` When executed Then all tests pass, coverage ≥90%
  (components), 100% (critical paths: auth context, forms, report export,
  SSE, envelope handling).
- Given `tsc --noEmit` When executed Then no type errors.
- Given `oxlint .` When executed Then 0 error AND 0 warning
  (zero-warning strict — decision 70).
- Given `vitest run --testNamePattern=auth` When executed Then auth context
  tests pass (login, refresh, logout, tenant switch).
- Given `vitest run --testNamePattern=form` When executed Then form tests
  pass (validation, submission, error handling).
- Given `vitest run --testNamePattern=report` When executed Then report
  export tests pass (PNG/PDF generation, A4 fixed layout).
- Given `vitest run --testNamePattern=sse` When executed Then SSE stream
  tests pass (connection, parsing, refetch, reconnect).
- Given `vitest run --testNamePattern=envelope` When executed Then envelope
  handling tests pass (success/error branching, retry logic).

### Frontend Integration Test (React/TS)

- Given `vitest run --config vitest.integration.config.ts` When executed
  Then all integration tests pass (full component flows with mocked API).
- Given form submission flow When executed Then validation, error handling,
  success path tested with MSW.
- Given navigation flow When executed Then routing, guards, redirects tested.
- Given SSE stream flow When executed Then connection, parsing, refetch,
  reconnect tested with MSW.
- Given report export flow When executed Then PNG/PDF generation, A4 fixed
  layout tested.

### Test Matrix Summary

| Layer | Backend (Go) | Frontend (React/TS) |
|---|---|---|
| Unit test | `go test` + `testify` + `httptest` | `vitest` + `@testing-library/react` |
| Integration test | `go test` + `httptest` + `sqlmock` | `vitest` + `msw` + `@testing-library/react` |
| Coverage target | ≥90% business logic, 100% critical | ≥90% components, 100% critical |
| Critical paths | auth, validation, idempotency, tenant isolation, envelope, SSE | auth context, forms, report export, SSE, envelope |
| Structure | `internal/<domain>_test.go` | `src/**/*.test.ts(x)` |
| Pass criteria | all pass + coverage + vet + gofmt | all pass + coverage + tsc + oxlint |

### Test Execution

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

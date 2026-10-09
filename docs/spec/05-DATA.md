# 05 — Data

Status: DRAFT (2026-10-09; adding test strategy + A4 fixed report; pending audit)

## 1. Principles (binding)

- Zero FK constraints: every reference is a loose `BIGINT UNSIGNED`,
  enforced in app code. Domain CHECKs (badge both-or-neither, age range,
  stars 1–4, end>start) live in DDL as documented in §2 AND are
  re-validated in `validate.go` (defence in depth; API returns 200
  `VALIDATION` before the DB ever rejects). Snapshots/duplication over
  normalisation.
- Every domain, link, and snapshot row carries `tenant_id NOT NULL`
  and every list-driving query filters by it (+ a tenant-leading INDEX on
  each table — see `INDEX` lines in §2, which are normative: where a
  table's §2 line omits the tenant INDEX, the migration MUST still add
  `INDEX(tenant_id, …)`); exceptions: `tenants`, `migrations`
  (no tenant column). Nullable-tenant tables (SA rows): `users`,
  `refresh_tokens`, `reset_tokens` (`users.tenant_id` NULL for SA).
- `id BIGINT UNSIGNED AUTO_INCREMENT` PK on all entity tables
  (incl. `session_topics`, `session_activities`, `session_groups`,
  `photos`, which keep a surrogate id despite being snapshot-value rows).
  True composite PKs (no surrogate id): `mission_topics`
  PK(mission_id,topic_id), `frame_programs` PK(frame_id,program_id),
  `report_missions` PK(session_id,session_topic_id,child_id,mission_id).
  Uniqueness-without-PK (surrogate-less UNIQUE, enforced in DDL + Tx):
  `session_members` UNIQUE(child_id,session_id),
  `assessments` UNIQUE(session_activity_id,child_id),
  `narratives` UNIQUE(session_id,session_topic_id,child_id),
  `consent_links` UNIQUE(session_id,child_id),
  `idempotency_keys` UNIQUE(tenant_id,route,idem_key) (tenant-scoped —
  same key+route in another tenant is a distinct row, never a replay),
  active `public_links` uniqueness enforced by app Tx (revoke-old +
  insert-new in one Tx; NO filtered-UNIQUE DDL — MariaDB 12.3 has no
  `UNIQUE … WHERE` syntax), single-candidate via `candidate_slot`
  generated UNIQUE (see `photos`). `utf8mb4` (`utf8mb4_unicode_ci`); UTC datetimes; `created_at` default
  `CURRENT_TIMESTAMP`, `updated_at` on-update where mutated.
- PII (names/WA/email) unencrypted at rest for MVP (documented).
  Reset `code_plain` is the only sensitive plaintext: revealed once
  (audited) or revoked, purged on use/expiry.

## 2. Entities & fields

`tenants`: id, name* `VARCHAR(160)` UNIQUE (avoids picker confusion),
type* ENUM(KOTA,NEGARA,LAINNYA),
`is_active BOOL DEFAULT TRUE`, created_at. Seed (idempotent):
(Bandung, KOTA, active).

`users`: id, tenant_id NULLABLE+INDEX (SA NULL), name* `VARCHAR(160)`,
email* `VARCHAR(320)` UNIQUE GLOBAL, wa_number `VARCHAR(20)` NULL,
password_hash* `VARCHAR(255)`, role* ENUM(SA,ADMIN,FASILITATOR),
decision* ENUM(PENDING,APPROVED,REJECTED) DEFAULT PENDING,
`is_active BOOL DEFAULT FALSE`, created_at.
`INDEX(tenant_id,role,decision,is_active)`.
Semantics: register sets (PENDING,false). Approve sets (APPROVED,true).
Reject sets (REJECTED,false) — row retained, never shown as pending.
Deactivate sets is_active=false (decision untouched); reactivate sets true.
Pending list = `decision=PENDING`. Login requires
decision=APPROVED AND is_active=true (else 200 `ACCOUNT_INACTIVE`).

`refresh_tokens`: id, tenant_id NULLABLE+INDEX (NULL for SA sessions —
SA `users.tenant_id` is NULL), user_id* (loose)+INDEX,
jti* `VARCHAR(64)` UNIQUE, expires_at* (7 d), revoked_at NULL,
created_at. Rotation = insert new + revoke old in one Tx.

`reset_tokens`: id, tenant_id NULLABLE+INDEX (NULL for SA rows — SA has no
tenant; ADMIN scope = own-tenant rows, SA sees all), user_id* (loose)+INDEX,
`code_hash CHAR(64)*`, `code_plain VARCHAR(16)` NULL (cleared on
use/revoke/expiry), expires_at* (15 min), used_at NULL, revoked_at NULL,
revealed_at NULL, revealed_by NULL (loose user id), created_at.
Dashboard shows `code_masked = "***"+RIGHT(code_plain,2)` (never the full
code except audited reveal). `INDEX(expires_at)`, `INDEX(user_id,expires_at)`.
Purge by hourly janitor + sweep on access.

`programs`: id, tenant_id*+INDEX, name* `VARCHAR(200)`, description TEXT
NULL, badge_name `VARCHAR(200)` NULL, badge_image_path `VARCHAR(512)`
NULL (`CHECK`: both NULL or both NOT NULL), `is_active DEFAULT TRUE`,
created_at. `INDEX(tenant_id,is_active)`.

`topics`: id, tenant_id*, program_id* (loose)+INDEX, program_name_snap*
`VARCHAR(200)`, name*, description NULL, sort_order INT DEFAULT 0,
badge pair (same CHECK), is_active, created_at.
`INDEX(tenant_id,program_id,sort_order)`.

`activities`: id, tenant_id*, program_id* + topic_id* (loose)+INDEX,
program/topic_name_snap*, name*, indicator TEXT NULL, sort_order,
created_at. `INDEX(tenant_id,topic_id,sort_order)`.

`missions`: id, tenant_id*, program_id* (loose), program_name_snap*,
name*, body* TEXT, created_at. `mission_topics(mission_id*,
topic_id*, topic_name_snap*, tenant_id*)`
PK(mission_id,topic_id), `INDEX(topic_id)`.

`frames`: id, tenant_id*, image_path* `VARCHAR(512)`, name*
`VARCHAR(200)`, created_at. `frame_programs(frame_id*, program_id*,
program_name_snap*, tenant_id*)` PK(frame_id,program_id).

`children`: id, tenant_id*+INDEX, name*, age* `TINYINT UNSIGNED`
(`CHECK 1–15`), age_confirmed* `BOOL DEFAULT FALSE`
(`CHECK(age<=10 OR age_confirmed=TRUE)`), school* `VARCHAR(200)`,
mother_name*, mother_wa* `VARCHAR(20)` (`+62…`), father_name,
father_wa `VARCHAR(20)` NULL, guardian_name NULL, `is_active DEFAULT
TRUE`, created_at. `INDEX(tenant_id,is_active)`.

`sessions`: id, tenant_id*+INDEX, program_id* (loose),
program_name_snap*, name*, description NULL, date* DATE,
start_time/end_time TIME NULL (`CHECK(end_time IS NULL OR
start_time IS NULL OR end_time>start_time)`), location*,
status* ENUM(DRAFT,OPENED,FINISHED) DEFAULT DRAFT,
created_by (loose, NULL=deleted), created_at.
`INDEX(tenant_id,status,date)`. No CANCELED value exists.

`session_topics`: id, tenant_id*, session_id*+INDEX, topic_id* (loose),
topic_name_snap*, sort_order, badge_name_snap NULL, badge_image_snap
NULL. `INDEX(session_id,sort_order)`.

`session_activities`: id, tenant_id*, session_id*+INDEX,
session_topic_id*+INDEX, activity_id* (loose), activity_name_snap*,
indicator_snap NULL, sort_order. `INDEX(session_topic_id,sort_order)`.

`session_groups`: id, tenant_id*, session_id*+INDEX, name*,
facilitator_id NULL (loose; NULL = missing → assessment blocked),
facilitator_name_snap*.

`session_members`: session_id*, group_id*+INDEX, child_id*,
child_name_snap*, tenant_id*, `UNIQUE(child_id,session_id)`,
`INDEX(session_id,group_id)`.

`assessments`: session_id*, session_activity_id* (loose),
child_id*, tenant_id*, stars* `TINYINT CHECK 1–4`, note TEXT NULL,
activity_name_snap*, child_name_snap*, assessor_id (loose, NULL kept),
updated_at. `UNIQUE(session_activity_id,child_id)`,
`INDEX(session_id,child_id)`. Upsert atomic; LWW accepted.

`photos`: id, tenant_id*, session_id*+INDEX, session_topic_id*+INDEX,
child_id*+INDEX, child_name_snap*, path* `VARCHAR(512)` (relative),
frame_id NULL (loose), is_candidate* `BOOL DEFAULT FALSE`, file_size INT,
mime `VARCHAR(64)`, created_by (loose), created_at, file_ready*
`BOOL DEFAULT TRUE` (single upload inserts TRUE — file already durable;
copy-data inserts FALSE, flips TRUE per file via single-row UPDATE after
each `io.Copy` lands (non-transactional, idempotent);
`GET P/photos` and public gallery serve ONLY `file_ready=TRUE` rows, so
a crash between copy-commit and copy leaves rows that are INVISIBLE,
never fake gallery bytes; FALSE rows NEVER surface as `PHOTO_MISSING`
(`PHOTO_MISSING` = ONLY `file_ready=TRUE` rows whose file is gone —
see 06 E29); FALSE rows stuck >24 h are deleted row+files by the janitor
— see §5). Count-10 counts ALL rows regardless of `file_ready`
(conservative — invisible pending rows still hold quota, prevents
overcommit under concurrent copy); scoped predicate below; >10 → 200
`PHOTO_LIMIT`). Copy-data NEVER copies `is_candidate` (always inserts
FALSE — candidate must be chosen explicitly in the target, so a pending
row can never block the target's candidate slot). Single-candidate enforced by Tx
(`SELECT COUNT(*) FROM photos WHERE session_id=? AND session_topic_id=?
AND child_id=? FOR UPDATE` — scoped count, never global; >10 → 200
`PHOTO_LIMIT`). Single-candidate enforced by Tx
clear+set AND DDL generated column (MariaDB partial-unique trick):
`candidate_slot VARCHAR(128) GENERATED ALWAYS AS
(IF(is_candidate=1, CONCAT(session_id,':',session_topic_id,':',child_id), NULL))
VIRTUAL NULL, UNIQUE(candidate_slot)` (multiple NULLs allowed, one non-NULL
per child+topic+session).

`narratives`: session_id*, session_topic_id*, child_id*, tenant_id*,
text* TEXT (≤50 words by template, not DB), model `VARCHAR(64)` NULL,
generated_at NULL, generated_by (loose). `UNIQUE(session_id,
session_topic_id,child_id)`. Upsert on success only.

`report_missions`: session_id*, session_topic_id*, child_id*,
mission_id* (loose), mission_name_snap*, mission_body_snap* TEXT,
tenant_id*. PK(session_id,session_topic_id,child_id,mission_id).
Replace-set in one Tx; app + `CHECK`-via-count (≤4 → else `MISSION_LIMIT`).

`consent_links`: id, tenant_id*, session_id*+INDEX, child_id*+INDEX,
token* `CHAR(64)` UNIQUE, status* ENUM(PENDING,SETUJU,DITOLAK) DEFAULT
PENDING, respondent_name `VARCHAR(160)` NULL, responded_at NULL,
created_at. `UNIQUE(session_id,child_id)` (one link per child+session).

`public_links`: id, tenant_id*, session_id*+INDEX, session_topic_id*,
child_id*+INDEX, kind* ENUM(REPORT,GALLERY), token* `CHAR(64)` UNIQUE,
revoked_at NULL, created_at. Active-link uniqueness enforced by app Tx
(regenerate = revoke old + insert new in one Tx; NO filtered-UNIQUE DDL —
MariaDB 12.3 has no `UNIQUE … WHERE` syntax).

`wa_outbox`: id, tenant_id*+INDEX, kind* ENUM(REPORT,CONSENT),
target_wa* `VARCHAR(20)`, payload_ref* `VARCHAR(512)` (report URL or
consent URL — never file bytes), status* ENUM(SENT,FAILED)
(no QUEUED value), message_id `VARCHAR(128)` NULL, error TEXT NULL,
idem_key `VARCHAR(64)` NULL+INDEX, created_at.
`INDEX(status,created_at)`, `INDEX(tenant_id,created_at)`. Retention 90 d.

`idempotency_keys`: route* `VARCHAR(200)`, idem_key* `VARCHAR(64)`,
tenant_id*, response JSON MEDIUMTEXT, created_at,
`UNIQUE(tenant_id,route,idem_key)`, purge >24 h.

## 3. Request/response shapes (canonical; all HTTP 200)

- Envelopes: success `{data:…}` (+ paged lists `{data:[…],
  meta:{page,per_page,total}}` — decision 25, binding server-side pure:
  scope/filter/sort/search/pagination ALL server-side; defaults `page=1,
  per_page=20`; allowed `per_page` 20/50/100/1000, max 1000; client page-size
  selector offers 20/50/100/1000. Canonical list keys: `page`, `per_page`,
  `q` (substring search where the route shows `q=`), `sort`, `dir`
  (`asc|desc`) + the route's stated equality filters; any other query key
  is ignored; invalid `page`/`per_page`/`sort`/`dir`/filter values → 200
  `VALIDATION`. Default sort `id ASC` (`id ASC` always appended as final
  tiebreak) except: topics/activities (+ session copies) default
  `sort_order ASC`; photos default `created_at ASC`; sessions default
  `date DESC`; copy-candidates fixed FINISHED-first then date DESC
  (full-array, not pageable). Sort allowlist per list: tenants/users/
  programs/missions/frames/children `id|name|created_at` (+ users `email`);
  topics/activities `sort_order|name|id`; sessions
  `id|name|date|status|created_at`; consents `id|child_name|status|
  responded_at`; reset-tokens `id|expires_at|created_at`. The `consents`
  allowlist covers BOTH consent lists identically
  (`GET /api/sessions/:sid/consents?status=` and
  `GET /api/consents?session_id=&child_id=` — same keys, same defaults).
  Paged lists (meta
  REQUIRED): tenants, users, programs, topics, activities, missions,
  frames, children, sessions, session-consents, consents, reset-tokens.
  Full/singleton (no meta, bounded scope): session detail (topics+groups ONE
  response), `P/assessments`, `P/photos` (≤10), narrative, mission picks,
  report, readiness, progress + reports matrices, public-links,
  copy-candidates, `pending-count`, `wa/status`, dashboard aggregates,
  public JSON, `GET /api/auth/tenants` picker. `wa_outbox` has no list
  route (retention only).);
  business failure
  `{error:{code,message}}`; unhandled → HTTP 500
  `{error:{code:"INTERNAL",message:"…"}}`.
- Auth: login req `{email,password}` → `{data:{user:{…},tenant:{…}}}`
  + cookie. Register/forgot/reset as in FL-01..03.
- Session create req `{program_id,name,description,date,start_time?,
  end_time?,location,groups:[{name,facilitator_id,child_ids[]}]}` →
  `{data:{session_id}}`.
- Assessment req `{stars:1-4, note?}` (PUT, per activity).
- Copy candidates → `{data:[{session_id, session_name, session_date,
  status, counts:{assessments, narratives, missions, photos}}]}` (ordered
  FINISHED-first then latest date). Copy execute req
  `{source_session_id}` → `{data:{copied:{assessments, narratives,
  missions, photos}, skipped:{assessments, narratives, missions,
  photos}}}` (counts per category).
- WA status → `{data:{connected, session, checked_at}}` (200 always;
  unreachable → `{connected:false}`).
- SSE signal frame (round 7, binding — `text/event-stream`, NOT an
  envelope): `id: <rev>\nevent: <name>\ndata: {"entity":"…",
  "entity_id":N, "rev":N, "tenant_id":N, "session_id":N|null,
  "action":"created|updated|deleted|…", "ts":"RFC3339"}\n\n`.
  `id` = `rev` string (monoton per tenant; drives `Last-Event-ID`).
  `data` NEVER carries names, notes, photos, tokens, codes, or bytes —
  only routing keys. Four full-value exceptions ONLY (small derived
  non-PII merged into `data`): `readiness.changed {complete}`,
  `pending.changed {pending}`, `wa.changed {connected}`, public
  `consent.ack {decision}` (own decision only). Heartbeat = `:ping`
  comment (15 s). Terminal public frame: `link.revoked` (then close).
- Readiness `{data:{complete, missing:[{said,name}], badges:{topic,
  program}}}`. Report JSON = full ReportView content (profile, outcomes,
  narrative, missions, badges, candidate photo URL via `/uploads/…`,
  attestation = CURRENT group owner name at open time, fallback
  `facilitator_name_snap` if owner deleted — LIVE, user decision
  2026-10-08).

## 4. Transformations

- WA: strip non-digits → if starts `0`, replace leading `0` with `62`;
  else if starts `8`, prefix `62`; else if starts `62`, keep; else if starts
  `+`, strip `+` and apply above to remainder; else reject (`VALIDATION`).
  Stored `+62…` (8–15 digits after country code); `chatId = 62…@c.us`
  (strip `+`). Default country 62 prefilled in UI picker.
- Stars 1↔BB, 2↔MB, 3↔BSH, 4↔BSB (i18n labels; report stays ID).
- Prompts: `{{Var}}` via `strings.Replace` (no HTML-escaping);
  vars ChildName/ChildAge/SessionName/TopicName/SessionDate/Assessments.
- WA text: `wa_report.md` / `wa_consent.md` + `PUBLIC_WEB_BASE_URL` +
  `/view/…?token=`.
- Photos: capture canvas 9:16 JPEG (frame fused); upload kept as-is
  (JPG/PNG/WebP).

## 5. Persistence & files

- MariaDB 12.3, pool `SetMaxOpenConns(10)` / `SetMaxIdleConns(10)` /
  `SetConnMaxLifetime(3*time.Minute)`, `c.Context()` + 5 s statement
  budget per op (15 s Gemini call, 10 s OpenWA call; narrative Tx runs
  Gemini OUTSIDE the DB Tx — DB write after AI returns, so a Gemini
  timeout yields `AI_FAILED`, never `DB_UNAVAILABLE`; only a DB-side
  stall yields `DB_UNAVAILABLE`).
- Migrations `NNNN_*.sql` in order at boot: acquire MySQL advisory
  `GET_LOCK('kidversa_migrate',30)`, apply missing versions into
  `migrations(version INT PRIMARY KEY, applied_at)`, `RELEASE_LOCK`;
  lock timeout/failure = refuse listen. Seed (Bandung + SA) idempotent via
  UNIQUE keys + `INSERT IGNORE` + update-if-exists for SA password rotate
  (env-driven, never duplicated).
- In-process hourly janitor (single `time.Ticker` goroutine in `api`,
  NOT a separate service): purge expired/used `reset_tokens`, expired
  `refresh_tokens`, `idempotency_keys` older than 24 h, `wa_outbox` older
  than 90 d, `photos` rows stuck `file_ready=FALSE` older than 24 h
  (copy-crash residue — row + partial files deleted together),
  and orphan upload files (on disk but no DB row). Sweep
  failures only logged (`slog`), never crash boot.
- Uploads `<tenant_id>/<session_id>/<stid>/<child_id>/<rand32hex>.<ext>`
  (all numeric IDs, never names — stable on rename, no traversal;
  `<ext>` from sniffed mime: jpg/png/webp, never original filename); badges
  `…/badges/<tenant_id>/<rand>.<ext>`; frames `…/frames/<tenant_id>/<rand>.<ext>`;
  DB relative path only; static
  `Browse:false, Download:false, CacheDuration:10*time.Second`; nginx proxy.
  Server hard caps (binding): photos 10 MB (client pre-checks 10 MB);
  curriculum images (program/topic badges, frames) PNG/JPG/WebP ≤2 MB
  (ICO rejected → 200 `VALIDATION`); mime always sniffed server-side.
- Durable-file rule (round 7, binding): photo/frame/badge writes use
  temp-file + `fsync` + atomic rename; the row commit and the durable
  file form ONE visible step — never file-without-row ANNOUNCED, never a
  status/SSE signal before durability (`photo.changed` fires only after
  durability). Two orderings, both honest: (a) single upload — file
  durable FIRST, then one Tx inserts the row, then 200 + signal (no
  row-without-file window exists for the client); (b) copy-data — rows
  committed FIRST (Tx), `io.Copy` files AFTER commit, POST 200 withheld
  until files durable (same honesty bar as (a): the client never sees
  200 + signal with missing files; a crash between commit and copy
  leaves INVISIBLE `file_ready=FALSE` rows — never announced on SSE,
  never `PHOTO_MISSING` (that flag is ONLY for `file_ready=TRUE` rows
  whose file is gone — see 06 E29); stuck FALSE rows >24 h are deleted
  row+files by the janitor — see §5).
  Orphan temp/disk-without-row files (crash mid-write) are swept by the
  janitor and were NEVER announced on SSE (no fake status).
- Report bytes never persisted. Backups = host copy of
  `data/mariadb + data/uploads + data/openwa` (non-atomic live copy;
  PITR out of scope for MVP).

## 6. Caching & sync

- No server cache. Client memo rule (round 7, binding): refetch on SSE
  signal OR when data older than 60 s (no blind poll). Refresh/consent/
  public/idempotency DB-backed; limiter in-process (restart reset
  accepted). Stream hub (`internal/stream.go`): in-memory fan-out per
  `(tenant, scope)` + ring 100 events / 5 min per tenant for
  `Last-Event-ID` replay; single `api` replica for MVP (no Redis).

## 7. Transactions & idempotency (binding SQL intent)

- Session create: `BEGIN;` eligibility + member Tx checks
  (`SELECT … FOR UPDATE` on child rows); copy snapshots; insert groups/
  members (`groups[]` OPTIONAL round 5 — zero groups allowed, Tx still
  commits snapshot only); `COMMIT` (rollback on violation). Idem-key replay short-circuits.
- Refresh rotation / reset-use (revoke ALL refresh + clear plain + stamp)
  / deactivate (inactive + revoke) / approve / candidate clear+set /
  consent first-wins (`UPDATE … WHERE status='PENDING'`) / mission
  replace-set / public regenerate (revoke old + insert new) / status
  transitions (`UPDATE … WHERE status=<expected>`): each one Tx.
- Assessment upsert / narrative upsert (success only): single-row atomic.
- Copy-data (round 6): ONE Tx per execution — matched assessment upserts
  (fill-empty) + narrative inserts (only-if-empty) + mission replace
  (only-if-zero-and-linked) + photo row inserts (only-if-target-zero,
  cap 10 counted in Tx); `io.Copy` disk files AFTER commit (orphan-swept
  on crash); POST 200 WITHHELD until copied files are durable (same
  honesty bar as photo upload — client never sees 200 + signal with
  missing files). Consent DITOLAK later → delete ALL photo rows + files
  for that child+session (original + copied; separate Tx + disk delete).
- Batch WA: NOT transactional — sequential sends, per-item Tx
  (outbox row + status), idem-key per request, resume = new request with
  new key for failed items only.
- Photo single upload: file durable FIRST (temp+fsync+rename), then ONE Tx
  inserts the row, then 200 + signal (no row-without-file window).
  Copy-data: rows committed FIRST (Tx), `io.Copy` files AFTER commit,
  POST 200 withheld until files durable (same honesty bar). Crash between
  copy-commit and copy leaves INVISIBLE `file_ready=FALSE` rows (never
  announced, never `PHOTO_MISSING` — see 06 E29); stuck >24 h deleted by
  the janitor; orphan
  disk-without-row files swept hourly.
- SSE publish (round 7, binding): every Tx above is followed (on COMMIT
  only, never on rollback) by `stream.Publish(tenant_id, frame)` in the
  same handler AFTER commit (+ after file durability for photo/copy
  paths). Publish is best-effort (`slog` on hub-full drop) and NEVER
  fails/retries the mutation. Guarded-transition losers (`STATE_CONFLICT`)
  publish nothing. Batch WA publishes ONE `report-link.changed` per
  session after the batch loop (not per item). `rev` is assigned by the
  hub (monoton per tenant; in-memory counter + `created_at` fallback —
  restart resets the counter and all clients full-refetch once).

# 03 — Features

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
> Relevan berkas ini: F-01 (51/52/55); F-04 (57/59/62); F-05 (56);
> F-08 (61); F-11 (63); F-12 (64/65); F-13 (66); F-15/F-16 (67).
> Rincian: `07` Change 19 + section indeks pembaruan di `SPEC-INDEX.md`.

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

Conventions: `[ROLE: SA/ADMIN/FAS]` = who may use. All tenant-scoped.
`*` = required. All `/api/*` and `/public/*` return HTTP 200 with
`{data}` or `{error:{code,message}}` (only unhandled faults → 500).
`X-Tenant-ID` required on every authenticated call.
`X-Idempotency-Key` required on mutating POSTs listed in `02§2`.

## F-01 Authentication & session

- [SA/ADMIN/FAS] Login email + password → access JWT 15 min (memory,
  `Authorization: Bearer`) + refresh JWT 7 d (HttpOnly cookie `Path=/`,
  `SameSite=Lax` (+`Secure` prod) + DB whitelist, single-use jti rotate).
  Claims: `sub, role, tenant` (+`jti` for refresh). Leeway 60 s.
  Logout is cookie-only-capable (decision 52): `POST logout` accepts
  the refresh cookie even when the access token is expired/absent
  (revoke jti + clear cookie → 200). Hardening (decision 55): a second
  consecutive INVALID_TOKEN during refresh-retry forces logout (clear
  session → login); refresh rotation validates the target tenant's
  active state — inactive → revoke jti + clear cookie + 200
  `TENANT_INACTIVE` (fail-closed).
- [—] Register: name*, email* (global UNIQUE, ≤320 chars, format-checked),
  WA*, password* (min 8, letters+numbers, MAX 72 bytes — bcrypt
  `GenerateFromPassword` truncates beyond 72, so longer → 200 `VALIDATION`),
  role* (ADMIN/FASILITATOR ONLY — `SA` rejected with 200
  `FORBIDDEN_ROLE`; SA exists via seed only), tenant* (active only).
  New row `is_active=false` (pending). Replay with same
  `X-Idempotency-Key` returns original result (no double row).
- [SA] Approve/reject ADMIN (any tenant); [ADMIN] approve/reject
  FASILITATOR (own tenant). Reject sets (REJECTED,false) — row retained,
  never pending. Pending list = `GET /api/users?decision=PENDING` +
  badge `GET /api/users/pending-count`.
- [SA/ADMIN/FAS] Logout revokes refresh jti AND clears cookie; change own
  name/password (password change revokes ALL own refresh rows — other
  devices re-login).
  [ADMIN] edits facilitator rows; [SA] edits admin rows. Email change
  allowed if globally unique.
- [—] Forgot: `POST /api/auth/forgot {email}` → always
  200 `{data:{sent:true}}` (no enumeration). If user exists: random
  8-char code, store `code_hash=SHA256(code)` + `code_plain` (for relay)
  + `expires_at` 15 min, single-use, purge on use/expiry (hourly job +
  on-access sweep). SMTP send attempted.
- Reset dashboard: `GET /api/reset-tokens` lists unexpired rows for
  SA (all) / ADMIN (own tenant) as `{id, code_masked, expires_at,
  seconds_left, revoked, mail_failed, user:{id,name,email}}`
  (reset-dashboard is EVENT-DRIVEN round 7:
  `reset.changed` signals → refetch; `TokenCountdown` ticks down locally
  1 s from `expires_at` — no 5 s poll;
  public forgot page shows no countdown); `?reveal=1` or
  `POST /api/reset-tokens/:id/reveal` returns `code_plain` ONCE then
  PURGES it — single-shot self-destruct (audit-logged; decision 51;
  subsequent reveal → 200 `VALIDATION`, masked only);
  `POST /api/reset-tokens/:id/revoke` voids it. Reset:
  `POST /api/auth/reset {email,code,new_password}` → constant-time hash
  compare → bcrypt new → revoke ALL user refresh rows → clear
  `code_plain`, set `used_at`.
- [SA] Tenant switcher: `POST /api/auth/switch-tenant {tenant_id}`
  validates active membership; client persists `kv-tenant` +
  sends `X-Tenant-ID`. Default on first login = latest created tenant.

## F-02 Tenant management [SA]

- CRUD: name*, type* (KOTA/NEGARA/LAINNYA), `is_active`.
  Inactive: every member call → 200 `{error:{code:TENANT_INACTIVE}}`;
  SA read-only retained. No `TENANT_DISABLED` code exists.
- Hard-delete only with typed confirmation: deletes ALL tenant rows +
  disk files in ALL three upload layouts — `uploads/<tenant_id>/`,
  `uploads/badges/<tenant_id>/`, `uploads/frames/<tenant_id>/` (decision
  45 / D-4); `wa_outbox` retained 90 d;
  OpenWA session/key untouched. Disk removal runs after DB commit;
  partial-wipe recovery = restore from host backup, then re-delete.
  Badge/frame on-disk layout is tenant-di-luar per `05§8` (decision 33);
  the storage-root & data-dir layout is `02§7` / `05§8` (decisions 33–34).

## F-03 User management

- [SA] CRUD admins (any tenant) + activate/deactivate.
  [ADMIN] CRUD facilitators (own tenant) + activate/deactivate.
- Besides the register + approval flow, ADMIN/SA may create an account
  directly as APPROVED (immediately loginable) via `POST /api/users`
  (see 02§2b: SA+ADMIN callers, roles ADMIN/FASILITATOR only,
  `X-Idempotency-Key` required).
- Deactivation revokes refresh rows in the same Tx (immediate logout;
  in-flight access fails closed on next middleware check with
  200 `ACCOUNT_INACTIVE` → client re-login).
- Delete (typed confirm): blocked with 200 `HAS_OPEN_GROUPS` if the user
  owns groups in any OPENED session (reassign first). Otherwise hard
  delete; historical snapshot names (`facilitator_name_snap`,
  `assessor_id` → kept as id, name snap shown) stay intact.

## F-04 Program / Topik / Kegiatan master [SA/ADMIN]

- Program: name*, description, badge section (badge name + badge image
  both set or both empty; else 200 `VALIDATION`). Image JPG/JPEG/PNG/WebP
  ≤50 MB, client side validation only (Pembaruan 2026-10-11).
- Topik: program*, name*, description, order (default append), badge
  section (same rule, same format/size).
- Kegiatan: program*, topic*, name*, indicator/description, order.
- Create/edit/delete any time (snapshot isolation — sessions
  unaffected). Program delete CASCADES its master topics + kegiatan +
  link rows (`frame_programs`, `mission_topics`) in one Tx, publishing
  `program.changed` + `frame.changed` when a frame link is removed
  (decision 62); asset files (badge) are ALWAYS deleted with their
  master (decision 59); deleting a program/topic already used by ≥1
  session requires typed-text confirmation showing the affected session
  count (decision 59).
- Program session-eligible iff ≥1 topic AND every topic ≥1 activity AND
  the program + its topics are `is_active` (decision 57 — create gate
  binding; the wire `Eligible` flag matches the gate); otherwise
  excluded from picker with reason; direct API → 200 `PROGRAM_INCOMPLETE`.

## F-05 Bank Misi [SA/ADMIN]

- Per-tenant bank: program*, name*, body*, `topic_ids[1+]` (same tenant,
  any program). Editable any time; reports resolve LIVE — the report
  renders the mission name/body LIVE from the bank at open time
  (decision 56: bank edits after pick change what links show; the
  `report_missions` snaps are history-only), picks (the mission SET)
  resolve live per child+topic (`report_missions`).
- Server enforces max 4 per child+topic (`PUT P/missions` with
  `{mission_ids[]}` replaces set in one Tx; 5th → 200 `MISSION_LIMIT`).

## F-06 Frame [SA/ADMIN]

- Per-tenant: image* JPG/JPEG/PNG/WebP ≤50 MB, client side validation
  only (Pembaruan 2026-10-11), name* (prefilled
  from filename, editable), `program_ids[1+]`.
- At capture, facilitator picks 1 frame from frames linked to the
  session's program (or no frame). Frame fused into saved JPEG 9:16.

## F-07 Child data (Anak) [ADMIN/SA create/manage; FAS read-only list/detail — user-confirmed 2026-10-08: FAS cannot create/edit/delete children]

- Fields: name*, age* integer 1–10 direct (`age_confirmed` BOOL default
  false; age >10 (11–15) → warning modal; confirmed stores
  `age_confirmed=true`; age <1/non-integer/>15 → 200 `VALIDATION`;
  user-confirmed 2026-10-08: keep 1–10 + modal above 10), school* free text,
  mother_name*, mother_wa* (digits → stored E.164 `+62…`, default country
  62; strict client + server re-check), father/guardian optional + WA
  (same normalisation), `is_active`, tenant auto. FAS `POST|PATCH|DELETE`
  on children → 200 `FORBIDDEN_ROLE` (no create/edit/deactivate/delete;
  wrong-number fixes go through ADMIN/SA).
- No duplicate-name check (UI disambiguates via school/mother/age).
- Deactivate: excluded from all pickers/creates; historical assessments,
  reports, public report remain visible. Public gallery for an inactive
  child always returns `{data:{photos:[], locked:true,
  reason:"CHILD_INACTIVE"}}` (inactive wins over consent).
- Hard delete (typed confirm): removes child row + `session_members` +
  assessments + photo rows + disk files + narratives + `report_missions` +
  consent/public link rows for that child (links revoked). `wa_outbox`
  retained. Masters untouched.

## F-08 Session (Sesi) [ADMIN create; SA full; FAS view/own-groups]

- Create: program* (eligible only), name*, description, date*
  (past/present/future allowed — backdate permitted for histori input;
  only format validated),
  start/end BOTH-OR-NEITHER (decision 61: one without the other →
  200 `VALIDATION`, client form enforces the pairing; both empty →
  NULL = all-day event; both set → end>start else `VALIDATION`),
  location*, `groups[]` OPTIONAL (user decision round 5: missing or empty
  allowed → Draft with zero groups, groups added later; when present each
  entry = name + active owner facilitator + active child
  list; 1-child-1-group enforced client + `UNIQUE(child,session)` + Tx
  check → 200 `CHILD_DUPLICATE`). `POST /api/sessions` with
  Idempotency-Key; single Tx (snapshot + groups + members); replay
  returns original `session_id`.
- Status (only 3, no CANCELED): Draft → Opened → Finished. Concurrent
  transition uses `UPDATE … WHERE status=<expected>` + status re-read on
  0 rows (wrong-state → 200 `SESSION_LOCKED`, rival-won → 200
  `STATE_CONFLICT`, per 06 E10/E11). Draft/Finished DELETE = direct wipe via
  typed-text ConfirmDialog (user decisions 2026-10-08: no soft-delete;
  no link-revoke gate; ADMIN and SA may delete; modal shows scope counts
  INCLUDING active report/gallery links + consent links, e.g. "20 rapor
  terkirim, 35 foto, 20 link aktif akan ikut mati"): physical wipe
  (snapshot, groups, assessments, photos+disk, narratives, missions
  picks, consent/public links for that session) with no trace;
  masters/children kept. After wipe, opening an old link → 200
  `TOKEN_INVALID` ("link invalid"); `wa_outbox` rows retained 90 d as
  send audit. No other cancel state exists.
- Draft: edit anything (`PATCH`). Opened: only groups/members/owner edits.
  Snapshot names/dates/program immutable after creation (200 `SESSION_LOCKED`
  on attempt). Finish allowed with partial assessments (warn list).
  Reopen (ADMIN/SA) → Opened (writes resume; badges recompute).
- Assessment allowed only while Opened AND group has active owner;
  otherwise 200 `GROUP_WITHOUT_FACILITATOR` / `SESSION_LOCKED`.
- Session detail: other groups show name/headcount only
  (FAS); own groups open fully. Gallery nav
  Session → Topic → Group → Child.

## F-09 Assessment [FAS owner; ADMIN/SA read/manage]

- Path: Session → own Group → Child → per-activity card
  (`PUT P/activities/:said {stars:1-4, note?}`, `said` =
  `session_activities.id`, P = `/api/sessions/:sid/topics/:stid/children/:cid`). Partial saves allowed; per-card save state;
  progress dots. Upsert single-row atomic; two writers LWW (accepted;
  single-owner makes rare; ADMIN overwrite intentional). Every write
  sets `assessor_id` = caller id (report/historical attribution after
  facilitator delete uses this + `activity_name_snap`).
- Finished: stars/notes 200 `SESSION_LOCKED` (read-only banner +
  disabled inputs); photos still free.
- Foreign group (FAS): 200 `NOT_OWNER` (UI thumbnail only).
- Completion (derived, never stored): child×topic complete ⇔ all its
  activities starred → topic badge (if defined); all topics → program
  badge (if defined). Star VALUES ignored (completion-only). Badges are
  derived live — never copied by copy-data (round 6); once copied stars
  complete a topic/program, its badge appears automatically.

## F-10 Photo & consent

- Consent link per child+session (32-byte hex token, NO EXPIRY —
  permanent by user decision 2026-10-08; revocable only via child/session
  delete; leak runbook: deactivate child or delete session scope to kill
  links; no login):
  SEND by ADMIN/SA only (`POST /api/sessions/:sid/children/:cid/send-consent`,
  Idempotency-Key, link `/view/consent?token=` — NOTE: session-scoped,
  NOT under per-topic prefix `P`, because consent is per child+session). Membership gate
  (round 5): child must be a member of the session (`session_members`
  row); non-member → 200 `NOT_FOUND`. Allowed only when session status is
  DRAFT or OPENED (Finished → 200 `SESSION_LOCKED`; consent links exist
  from Draft so parents can approve before activities start). FAS sees
  the full read-only consent item (incl. `consent_url` while PENDING —
  one wire shape for all roles per 04 FL-11) and cannot SEND (resend
  hidden, decision 12). Default = locked.
- Parent page: session/child summary + respondent name* + Setuju/Tolak →
  `POST /public/consent/respond` (first-wins `UPDATE … WHERE
  PENDING`; repeat returns 200 `{data:{decision, already:true}}`
  — idempotent success, no error). Permanent (no revote).
- Tolak/locked: no upload/capture/view for that child+session (all
  topics, all roles; API 200 `CONSENT_LOCKED`); stars/notes still work;
  report generatable without photo (section hidden). DITOLAK deletes ALL
  photo rows + files for that child+session INCLUDING copied photos
  (round 6); copied stars/notes/narratives/missions stay.
- Setuju: gallery unlocks permanently for that child+session.
- Photos: `POST P/photos` multipart (frame optional, program-linked
  only). Pembaruan 2026-10-11 (client side validation only): the client
  checks jpg/jpeg/png/webp ≤50 MB before send; the server does not
  validate mime/size. Server: consent check →
  write file durable FIRST (temp + `fsync` + atomic rename into
  `<tenant_id>/<session_id>/<stid>/<child_id>/<rand32hex>.<ext>`) →
  ONE Tx (`SELECT COUNT(*) … FOR UPDATE` max 10 → 200 `PHOTO_LIMIT`,
  then INSERT row) → publish `photo.changed` → 200 `{data}` — a row
  without its file never becomes visible (upload path = file-before-row;
  in the copy-data crash window a refetch may catch a file-less copied
  row, which renders as `{missing:true}` + `PHOTO_MISSING` per 04 FL-10
  and the orphan is janitor-swept — 04 FL-16 / F-15).
  Upload same endpoint (kept as-is). Capture fused 9:16 JPEG.
  Honest-upload rule (round 7, binding): bytes served ONLY from server
  disk via `/uploads/…`; SSE never carries bytes and never announces a
  file before durability. Client states: `preview` (local blob, labelled
  "mengunggah…", NOT in gallery, NOT candidate-able) → server 200
  `{data}` → `photo.changed` → refetch `GET P/photos` → `stored`
  (gallery `<img src="/uploads/…">`, candidate-able). No success toast,
  no gallery item, no candidate flag before the server 200. Other tabs'
  galleries update ONLY via signal → refetch → server bytes (never via
  pushed bytes, never optimistic).
- Candidate: `PATCH P/photos/:pid {is_candidate:true}` (owner/ADMIN/SA;
  one Tx clear+set; partial-unique enforced; changeable even Finished).
  Delete frees slot.

## F-11 AI narrative [FAS owner generates; ADMIN/SA too]

- Per child per topic: `POST P/narrative` (Idempotency-Key; P =
  `/api/sessions/:sid/topics/:stid/children/:cid`) sends
  (name, age, session, topic, date, activities+stars+notes) rendered
  into `prompts/user.md` (strings.Replace) with `prompts/system.md`
  pasted VERBATIM as the system instruction (system.md carries no
  placeholders — decision 63) to
  `POST https://generativelanguage.googleapis.com/v1beta/models/{GEMINI_MODEL}:generateContent`
  body `{contents:[{parts:[{text: rendered_prompt}]}]}` + headers
  `x-goog-api-key` + `Content-Type: application/json` (15 s timeout;
  user-locked 2026-10-08 from official doc snippet; default
  `GEMINI_MODEL=gemini-3.5-flash`).
  Output directed by prompt to be ≤50 words ID, 1 paragraph plain
  text; the returned text is stored AS-IS — no programmatic
  word-truncation (keputusan user 2026-10-11). Upsert editable;
  regenerate replaces only on success.
- Empty assessment set → 200 `NOTHING_TO_NARRATE`. Failure →
  200 `AI_FAILED` (old kept) + Retry. Report works without narrative
  (section shows "—").

## F-12 Report (Rapor) [FAS own-groups; ADMIN all-tenant; SA active-tenant]

- Enforcement point (clarified 2026-10-11; REVISED 2026-10-12,
  decision 64): the session-scoped SEND endpoints — `POST P/send-report`
  and `POST /api/sessions/:sid/batch-send` — AND public-link creation
  — `POST P/public-links` — return 200 `REPORT_INCOMPLETE` + missing
  list for incomplete topics (batch: per-item code, other items still
  send; single send carries the machine-readable `missing` list; link
  creation is rejected entirely while incomplete — an incomplete
  report can never have a public link, so parents never see one).
  `GET P/report` returns the FULL data incl. `complete` + `missing` and
  is NEVER gated (admin/FAS/SA previews stay open; public render is
  never gated — links cannot exist while incomplete). Compose
  gating = client-side readiness UI driven by `GET P/readiness` (lists
  unstarred activities) — the compose screen itself makes no gated
  server call.
- Compose: `ReportView` preview (plain CSS), mission picker (≤4, Tx),
  narrative (F-11), candidate indicator, derived badges, attestation:
  LIVE owner name (user decision 2026-10-08 option A — consistent with
  LIVE report: shows the CURRENT group owner's name when the link is
  opened; if the owner was deleted, falls back to `facilitator_name_snap`
  from `session_groups`; `assessor_id` per assessment row keeps per-card
  authorship history). Report content is LIVE
  (user-confirmed 2026-10-08: editing missions/photos/narrative after a
  link is sent changes what the link shows; no per-link frozen version
  for MVP).
- Export (client-only): PNG `toPng(node,{cacheBust:true})`; PDF
  `new jsPDF({orientation:'p',unit:'mm',format:'a4'})` + `addImage` +
  `save("Rapor-<Child>-<Topic>-<YYYYMMDD>.{png,pdf}")` (sanitised per
  02§3: ASCII, `[^A-Za-z0-9-_]+`→`_`, 40 chars, empty→`anak`/`topik`;
  date = session date). Never `doc.html()`. Single A4 page scaled;
  overflow shows notice (no multi-page MVP). Server uninvolved.
- Public links per child+session+topic: `POST P/public-links {kind}`
  (Idempotency-Key) → `{report_url, gallery_url}` on `/view/…?token=`
  (tokens NO EXPIRY — permanent by user decision 2026-10-08);
  revocable/regenerable (`POST /api/public-links/:token/revoke|
  regenerate`; regenerate revokes old in same Tx; ADMIN caller must
  belong to the link's tenant — SA any, cross-tenant → 200 `NOT_FOUND`
  no-leak, decision 65). Revoked/unknown → identical 200
  `TOKEN_INVALID` (no oracle).
- WA single (`POST P/send-report {to}`) and batch
  (`POST /api/sessions/:sid/batch-send {items[]}`, max 10 per request, client chunks;
  Idempotency-Key; jitter 3–10 s between sends): allowed on OPENED and
  FINISHED sessions (report delivery outlives assessment freeze; data is
  read-only snapshot). Session DELETED mid-batch → remaining items 200
  `NOT_FOUND` per item. Backend renders
  `wa_report.md` + 2 URLs → OpenWA `send-text` → per-item
  `{status:SENT|FAILED, message_id?, code?}` + `wa_outbox` row.
  Link-only (no file bytes touch the server). Failures retryable
  per item; manual URL-copy fallback always available.
- Content (ID only): logo, profile, names, AI summary, missions,
  attestation, outcomes (activities+stars), badges, candidate photo.

### F-12.1 Report Layout — A4 Fixed (REQUIRED — user-confirmed 2026-10-09)

- **Paper size**: A4 portrait (210mm × 297mm) FIXED. No other paper sizes.
- **Orientation**: Portrait only. No landscape variant.
- **Margins**: 10mm all sides (top, bottom, left, right).
- **Content area**: 190mm × 277mm (A4 minus margins).
- **Scaling**: Content scales to fit ONE A4 page. No multi-page for MVP.
- **Overflow**: If content exceeds one page, show overflow notice (no auto-paginate).
- **Export formats**: PNG (via `toPng`) and PDF (via `jsPDF` with `format:'a4'`).
- **PDF settings**: `new jsPDF({orientation:'p',unit:'mm',format:'a4'})` — portrait, mm units, A4.
- **PNG settings**: `toPng(node,{cacheBust:true})` — full node capture, cache-bust enabled.
- **Print CSS**: `@page { size: A4 portrait; margin: 10mm; }` for browser print.
- **Responsive**: Report view adapts to screen size but maintains A4 aspect ratio (1:1.414).
- **Mini report**: Same A4 fixed layout, scaled down for screen preview. No separate mini layout.

## F-13 Dashboard & analytics

- SA: switcher + tenant list + `GET /api/dashboard/tenant-summary`
  (SA-only; per-tenant counts: children, sessions open/finished,
  reports issued = ALL kind=REPORT public links incl. revoked +
  narratives — NO cross-tenant mixed aggregate; keputusan user
  2026-10-11) + per-tenant overview/analytics when active. The SA
  dashboard shows a per-tenant summary table (one row per tenant: name,
  type, active, children, sessions open/finished, reports issued) fed
  by that endpoint; it refetches on the existing `stats.changed` signal
  (no new SSE event). ADMIN: own-tenant. FAS: own sessions +
  profile. Tenant from `X-Tenant-ID` (SA `?tenant=` override).
- Overview: children, sessions open/finished, reports issued (= count of
  ACTIVE REPORT-kind public links — `revoked_at IS NULL` — + count of
  narratives; activity metric — one
  child+topic may count twice, drafted + shared; INTENTIONAL SPLIT vs
  the SA tenant-summary below, which counts ALL kind=REPORT links — see
  02§2b), assessment
  progress/session, pending approvals. Analytics: totals per period (7d/30d/all), star
  distribution (per-tenant global BB/MB/BSH/BSB + per-topic aggregate —
  per-activity histogram OUT of MVP, decision 66), per-session progress, facilitator
  activity. Plain SQL aggregates; EVENT-DRIVEN round 7:
  `stats.changed` (server aggregator tick 5–10 s, not per-mutation) →
  refetch overview/analytics; refetch on SSE signal only (all pages — no
  blind poll, no data-age guard).
- WA status [SA/ADMIN] (round 6; connect flow updated 2026-11-09):
  `GET /api/wa/status` [SA/ADMIN] reads the configured OpenWA session
  (list → session state, 5 s each) → 200 `{data:{connected, session,
  checked_at}}`; `connected` is true only when the session status is
  `ready`; unreachable → 200 `{data:{connected:false}}` (status only,
  never `WA_FAILED`). Dashboard `WaStatusBadge` (green/red from
  `wa.changed` `{connected}` event; `GET /api/wa/status` only for detail
  on demand; NO 30 s poll round 7). SA click opens the connect modal:
  **Open OpenWA** opens the OpenWA dashboard `:8003` in a new tab;
  **Connect Directly** calls SA-only `POST /api/wa/session/start` and
  renders OpenWA's QR from SA-only `GET /api/wa/session` until the
  session is `ready`, then Connected + linked phone. ADMIN sees the badge
  and popover but the click is inert. QR pairing is SA-only and lives only
  in the open modal — never stored, never sent over SSE.

## F-14 Bilingual UI + profile

- ID default / EN — language switch via the SettingsModal opened from
  AppLayout; persisted `kv-lang`;
  namespaces `common,menu,auth,master,session,assessment,report`;
  report content stays ID. Profile: own name + password for all roles.

## F-15 Copy-data antar-sesi [ADMIN/SA only — round 6]

- Trigger: adding a child to a group (`POST
  .../groups/:gid/members` or `POST /api/sessions` with `groups[]`) when
  the child is already a member of other same-program sessions (same
  tenant). Client calls `GET
  /api/sessions/:sid/children/:cid/copy-candidates` → list ordered
  FINISHED-first then latest date; modal shows dropdown (default = latest
  FINISHED, fallback latest date) + explicit Ya/Tidak buttons.
  Re-runnable per source (fill-empty is idempotent, safe to repeat for
  another source).
- Semantics (binding): FILL-EMPTY ONLY — target cells that already have
  stars are kept. Topic/activity matching BY master `topic_id` /
  `activity_id` (not names): unmatched target topics/activities (added or
  removed in the new session) stay empty for fresh assessment.
- Copied: stars + notes per matched activity (`assessor_id` = copier id,
  new rows in target snapshot); narratives per matched topic ONLY if
  target narrative empty (copies text + model, `generated_by` = copier);
  mission picks per matched topic ONLY if target has 0 picks AND each
  mission still links the target topic (new `report_missions` rows).
- Photos: physical file copy (`io.Copy` source disk → new target path
  `<tenant_id>/<session_id>/<stid>/<child_id>/<rand32hex>.<ext>` + new
  rows inserted with `is_candidate=false` — the candidate PHOTO itself
  IS copied (as a normal photo); only the FLAG never transfers, matching
  05§2 "Copy-data NEVER copies `is_candidate`" (decision 67); the
  candidate is re-picked manually in the target session) ONLY when the
  target topic for that
  child has 0 photos; target cap 10 still enforced in Tx (copied files
  counted). Copies proceed even while target consent is PENDING (gallery
  stays locked until Setuju).
- NEVER copied: consent rows/links, public report/gallery links,
  `wa_outbox` rows. Badges NEVER copied (derived completion-only —
  recompute automatically once copied stars complete a topic/program).
- Consent interaction (round 6): if the parent later responds DITOLAK on
  the target session, ALL copied photo rows + files for that
  child+session are deleted (same as Tolak semantics); copied
  stars/notes/narratives/missions REMAIN (facilitator data, not consent
  data).
- Guards: ADMIN/SA only (FAS → 200 `FORBIDDEN_ROLE`); source unknown or
  cross-tenant → 200 `NOT_FOUND` (no-leak, same shape); same tenant but
  different program → 200 `VALIDATION`; inactive
  child → 200 `CHILD_INACTIVE`; target FINISHED → 200 `SESSION_LOCKED`
  for the ENTIRE copy (stars/notes/narratives/missions AND photos all
  blocked — the photo part never runs, nothing is copied); source may be
  any status (Draft/Opened/Finished — partial source copies what exists).
- One Tx per execution (assessments + narratives + missions + photo rows;
  disk files written after commit, orphan-swept by janitor on crash).
  Idempotency-Key required; replay returns original counts. Race
  accepted (decision 67): source wiped mid-execution → the copy proceeds
  on what still exists and returns 200 with the ACTUAL (possibly
  partial) counts — zero-FK snapshot semantics; re-run copies the
  remainder (fill-empty is idempotent).
- The commit publishes `copy.completed` (`member.changed` already
  fired at member-add time — no duplicate, per 04 FL-16; round 7);
  other tabs refetch target session (no pushed rows).

## F-16 Realtime SSE — 1 endpoint, signal + refetch [round 7]

- Transport: `GET /api/stream` (auth pages) + `GET /public/stream?token=`
  (parent pages); `text/event-stream`, opened via `fetch` +
  `ReadableStream` in `lib/stream.ts` (zero new deps). Frames are
  signals `{entity, entity_id, rev}` + routing keys — never PII, bytes,
  tokens, or codes. Four full-value exceptions ONLY (small derived
  non-PII, render directly): `readiness.changed {complete}`,
  `pending.changed {pending}`, `wa.changed {connected}`,
  public `consent.ack {decision}` (own decision only). Plus the non-PII
  `copy_available` boolean HINT riding on `member.changed` /
  `session.changed{created}` routing keys — a hint, not a render-direct
  payload; the full-value exceptions stay exactly four (decision 67).
- Behaviour: one stream per tab max (`StreamGate` owns the AUTH stream;
  public `/view/*` pages use page-level `openStream`, never `StreamGate` —
  see 02§3; closes on
  unmount/tenant-switch/logout); handshake = same guards as REST
  (failures = 200 envelope, no stream); per-connection send/skip filter
  REUSES the REST scope functions (T1 — no duplicated auth logic);
  per-table debounce/coalesce 300 ms; `rev`-gated refetch (stale/duplicate
  signals ignored); reconnect backoff 1 s → 30 s with `Last-Event-ID`,
  ring 100 events / 5 min per tenant, unclosable gap → full-refetch.
  `auth.expiring` at T-60 s → refresh-once → reconnect. Full catalogue +
  filter + caps: `02§2c`. Publish points: every mutating handler,
  post-commit, best-effort (never fails the mutation).
- Honest files: photo/gallery/report bytes render ONLY from server
  `/uploads/…` after durability; SSE never carries bytes and never
  announces files early (see F-10 honest-upload rule). No polling
  remains: reset countdown (event + local tick), WA badge (event),
  dashboard refetch (SSE signal only — no age guard).

## F-17 Endpoint feedback notifications (user-confirmed 2026-10-12, decision 46)

- Every explicit user action (submit/save/send/delete/generate/copy/
  approve/export/upload) MUST surface its outcome via antd `notification`
  (card at viewport corner, `useNotification` hook — static methods
  deprecated): success envelope → `notification.success`; business-error
  envelope → `notification.error` displaying the i18n message + error
  `code` (+ recovery hint where `06§1` provides one).
- SSE-signal refetches and background page refetches NEVER notify (no
  realtime spam). Pure-client failures (e.g. `CAMERA_DENIED`) surface
  inline in the component (no endpoint result exists).
- Canonical convention text: `02§3` (binding); acceptance rows: `08`.

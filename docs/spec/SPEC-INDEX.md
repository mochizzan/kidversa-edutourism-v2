# SPEC-INDEX — Kidversa EduTourism v2

Specification Status: DRAFT (2026-10-12; audit spek 5 dimensi selesai; pembaruan konsistensi + notifikasi antd terpasang)

## Project

Kidversa EduTourism v2 — PAUD/TK assessment and portfolio-report web
application. Fresh build (no v1). See `00-SCOPE.md`.

## Document index

| File | Content |
|---|---|
| `00-SCOPE.md` | problem, objective, scope, actors, constraints, assumptions, terminology (incl. envelope, X-Tenant-ID, Idempotency-Key) |
| `01-TECHSTACK.md` | locked stack + verified APIs (Fiber v3 incl. `middleware/sse` transport-only, React 19, Vite 8, antd v5, OpenWA send-text, Gemini generateContent) + SSE protocol + honest-file rule |
| `02-ARCHITECTURE.md` | components, pipeline, canonical route table (`GET /api/stream` + `GET /public/stream?token=`), event catalogue + filter + caps (§2c), frontend guards/menu (`lib/stream.ts`, `StreamGate`, event-driven badge/countdown), isolation, boundaries |
| `03-FEATURES.md` | feature inventory + functional requirements + business rules per role (F-01..F-17 incl. realtime SSE + endpoint feedback notifications) |
| `04-FLOWS.md` | canonical end-to-end flows FL-01..FL-17 (FL-01..FL-16 product flows + FL-17 test flows) with snapshot ids, failure branches, and SSE publish → signal → refetch tails (FL-10 honest bytes) |
| `05-DATA.md` | entities, fields, checks, indexes, shapes (incl. SSE frame), transforms, persistence (durable-file rule), hub + publish-after-commit |
| `06-ERRORS-AND-EDGE-CASES.md` | HTTP-200 envelope taxonomy (E1..E32 + EX), stream propagation/reconnect/filter/caps, retry, races, edges (incl. SSE gap/orphan/revoke) |
| `07-BEFORE-AFTER.md` | BEFORE (empty) vs AFTER per change incl. decisions recorded per change (Change 11 = SSE migration, Change 12 = server-side paging, Change 13 = test strategy + A4 fixed, Change 14 = spec-code sync remediation round 10, Change 15 = upload rule 50 MB client-side only, Pembaruan 2026-10-11, Change 16 = project structure decisions 33–37, Change 17 = spec↔code audit + total antd migration decisions 38–45, Change 18 = consistency audit round 12 + antd notification decisions 46–50, Change 19 = audit-§4 closure decisions 51–72) |
| `08-ACCEPTANCE-CRITERIA.md` | testable Given/When/Then (all 200-envelope assertions + SSE realtime + paging blocks + test strategy acceptance criteria) |
| `SPEC-INDEX.md` | document index, locked user decisions 1–72 (incl. project-structure decisions 33–37 + audit & antd-migration decisions 38–45 + consistency/notification decisions 46–50 + audit-§4 closure decisions 51–72), implementation notes |

## [PEMBARUAN 2026-10-11] Ketentuan Upload Baru — indeks pembaruan (decision 32)

Keputusan user 2026-10-11 (decision 32): semua upload (foto, badge
program/topik, frame) — ukuran maksimum **50 MB**, format
**jpg/jpeg/png/webp** (case-insensitive), **client side validation only**
(server TIDAK memvalidasi ukuran/format). Ketentuan lama dicabut: ≤2 MB
(badge/frame), ≤10 MB (foto), server mime sniff + hard cap; nginx
`client_max_body_size` 12m → 52m dan Fiber `BodyLimit` 12 MB → 52 MB
(menyesuaikan maks 50 MB + multipart overhead).

Letak section `[PEMBARUAN 2026-10-11]` di tiap berkas:

| Berkas | Letak |
|---|---|
| `00-SCOPE.md` | section `[PEMBARUAN 2026-10-11]` tepat setelah baris Status |
| `01-TECHSTACK.md` | idem + revisi whitelist (§6) & bullet Uploads (§8) |
| `02-ARCHITECTURE.md` | idem + revisi nginx `client_max_body_size` / Fiber `BodyLimit` |
| `03-FEATURES.md` | idem + revisi F-04 (badge), F-06 (frame), F-10 (foto) |
| `04-FLOWS.md` | idem + revisi FL-04 (badge), FL-05 (frame), FL-10 (foto) |
| `05-DATA.md` | idem + revisi persistence (hard cap server → client-side only) |
| `06-ERRORS-AND-EDGE-CASES.md` | idem + revisi baris E1 |
| `07-BEFORE-AFTER.md` | idem + **Change 15** + penanda supersede di Change 2 |
| `08-ACCEPTANCE-CRITERIA.md` | idem + revisi acceptance badge/upload |
| `SPEC-INDEX.md` | section ini |

## [PEMBARUAN 2026-10-11] Struktur folder & file proyek — indeks pembaruan (decisions 33–37)

Pembaruan spek struktur folder & file proyek (2026-10-11), hasil eksplorasi
codebase aktual + interview user. Keputusan terkunci 33–37 (lihat §di bawah);
section normatif baru = `02§7 Project structure`; path upload/direktori data
= `05§8` (keputusan 33–34); modul lintas-domain = `02§2` (keputusan 35);
routes per grup = `02§3` + `02§7` (keputusan 36); indeks 10 berkas +
self-row = tabel Document index (keputusan 37).

Letak pembaruan struktur di tiap berkas:

| Berkas | Letak |
|---|---|
| `02-ARCHITECTURE.md` | §2 (modul lintas-domain envelope/pscope/idempotency) + §7 baru (peta struktur + decisions 33–37) |
| `04-FLOWS.md` | FL-04 (badge path), FL-05 (frame path) → tenant-di-luar |
| `05-DATA.md` | §5 (badge/frame path), §8 baru (layout penyimpanan + data dirs) |
| `SPEC-INDEX.md` | section ini + self-row + decisions 33–37 |

## [PEMBARUAN 2026-10-12] Audit spek 5 dimensi — indeks pembaruan (decisions 46–50)

Pembaruan spek 2026-10-12 hasil audit 5 dimensi paralel (scope &
acceptance, tech stack & arsitektur, fitur & flows, data & error,
indeks & pola) + interview user. Keputusan terkunci 46–50 (lihat
§di bawah): notifikasi antd endpoint (keputusan 46; normatif di
`02§3` + `03 F-17` + baris acceptance `08`); route readiness kanonik =
`GET P/readiness` (keputusan 47; duplikat session-level dihapus dari
`02§2b`); persetujuan `oxlint` (keputusan 48; masuk `01§10.4`);
buku audit §4 = non-blocking (keputusan 49); `DESIGN.md` = referensi
pola desain non-binding (keputusan 50). Seluruh perbaikan
kontradiksi/keambiguan lain dari audit diterapkan langsung di teks
terkait di setiap berkas (ringkasan lengkap: `07` Change 18).

Letak pembaruan di tiap berkas:

| Berkas | Letak |
|---|---|
| `00-SCOPE.md` | marker + §PEMBARUAN upload (caveat framing 52 MB) + §6 (sitasi `01§3`/`01§5` + carve-out test-dep) + §8 (gloss X-Tenant-ID non-SA, envelope per-item, istilah `P`) |
| `01-TECHSTACK.md` | marker + §2 (kalimat `X-Tenant-ID` lengkap, 4 exception frames, pointer `02§2b`) + §5 (`docs/ANTD-DOCS.md`) + §8 (exception frames) + §10.1/§10.3 (SSE di critical paths) + §10.4 (oxlint) |
| `02-ARCHITECTURE.md` | marker + §2c (carve-out broadcast tenant-0 di predikat filter) + §2b (hapus route readiness duplikat) + §3 (group `gallery`, `SettingsModal`/`PageHeader`, bullet notifikasi decision 46) + §7 (recap keputusan 33 & 37) |
| `03-FEATURES.md` | marker + F-10 (wording consent FAS + clarifier honest-bytes jendela copy) + F-15 (wording realtime copy) + F-17 baru (notifikasi endpoint) |
| `04-FLOWS.md` | marker + FL-04/FL-05 (sitasi kanonik `05§8`) + FL-09 (cabang `GROUP_WITHOUT_FACILITATOR`) + FL-13 (tag peran) |
| `05-DATA.md` | marker + §1 (clarifier baca SA tenant-0) + §2 (catatan ganda `consent_links`) + §3 (typo, cite decision 31, scope cite `stid`) + §6 (fragment rusak + catatan broadcast live) |
| `06-ERRORS-AND-EDGE-CASES.md` | marker + E1/E23/E32 (sel When lengkap) + catatan removed-codes (kode vs nilai ENUM) + §5 (`<rand32hex>` bukan UUID) |
| `07-BEFORE-AFTER.md` | marker + Change 16 NOTE superseded + **Change 18** |
| `08-ACCEPTANCE-CRITERIA.md` | marker + header (scope 1–50 + audit §4) + kriteria backend +SSE + section "Endpoint feedback notifications" |
| `SPEC-INDEX.md` | section ini + self-row + baris 03/04/07 (F-17, FL-17, Change 18) + letak Uploads §8 + decisions 46–50 + Unresolved |

## [PEMBARUAN 2026-10-12] Penutupan butir audit §4 — indeks pembaruan (decisions 51–72)

Seluruh 22 butir `docs/audit/2026-10-11-spec-codebase-sync-audit.md` §4
dijawab user (2026-10-12, via studi kasus + rekomendasi handling) dan
dikunci sebagai keputusan 51–72 (§di bawah); **audit §4 kini
TERTUTUP** (item 3 = tanpa perubahan: bentuk superset reset-dashboard
sudah disatukan 2026-10-11; item 20b+22 = butir verifikasi terbuka
keputusan 72). Rincian: `07` Change 19.

Letak pembaruan di tiap berkas:

| Berkas | Letak |
|---|---|
| `00-SCOPE.md` | marker + §3 (reveal self-destruct 51; granularitas star distribution 66) |
| `01-TECHSTACK.md` | marker + §10.2/§10.4 (gerbang oxlint zero-warning 70) |
| `02-ARCHITECTURE.md` | marker + §2 (SA inactive READ 53; logout cookie-only 52; reveal 51; misi live 56) + §2b (missions FAS 58; gate link 64; revoke tenant-scope 65) + §3 (SA auto-pick stream 54) |
| `03-FEATURES.md` | marker + F-01 (51/52/55) + F-04 (57/59/62) + F-05 (56) + F-08 (61) + F-11 (63) + F-12 (64/65) + F-13 (66) + F-15/F-16 (67) |
| `04-FLOWS.md` | marker + FL-03 (51) + FL-04 (59/62) + FL-05 (56) + FL-06 (57) + FL-07 (61) + FL-08 (60) + FL-12 (63) + FL-13 (64/65) + FL-16 (67) |
| `05-DATA.md` | marker + §2 (purge reveal 51; CHECK jam sesi 61; snaps misi riwayat 56) + §5 (backup manual 71) |
| `06-ERRORS-AND-EDGE-CASES.md` | marker + §1 E17 (64) + §5 (52/55; urutan gate 68; race copy 67; deactivated picker 57; SA inactive 53) |
| `07-BEFORE-AFTER.md` | marker + **Change 19** |
| `08-ACCEPTANCE-CRITERIA.md` | marker + baris tenant/reveal (51/53/54) + section "Audit-§4 closure" + gate oxlint (70) + catatan verifikasi terbuka (72) |
| `SPEC-INDEX.md` | section ini + self-row + baris 07 (Change 19) + decisions 51–72 + Unresolved (§4 tertutup) |

## User decisions locked (2026-10-08 post-audit)

1. Reset codes: store `code_hash` (SHA256) + `code_plain` for relay;
   dashboard shows masked + countdown; plaintext reveal-on-demand
   (audited) + revoke; purge on use/expiry. (replaces hash-vs-plaintext
   contradiction)
2. Session cancel: NO CANCELED state. Draft/Finished DELETE = physical
   wipe, no trace. ENUM is exactly DRAFT/OPENED/FINISHED.
3. WA delivery: LINK-ONLY. Text + 2 public URLs via `send-text`; no PNG/PDF
   attachments; server never handles report bytes. (resolves
   "server uninvolved" contradiction)
4. Transport: ALL `/api/*` + `/public/*` return HTTP 200 with
   `{data}` / `{error:{code}}`; only unhandled faults → HTTP 500
   `INTERNAL`. No 4xx statuses. `TENANT_DISABLED` removed (single
   `TENANT_INACTIVE`); `TENANT_MISMATCH` kept for stale-tab SA writes.

## User decisions locked (2026-10-08 re-audit round 2)

5. Session backdate ALLOWED (past/present/future; format-only validation).
6. Child age: keep 1–10 direct + warning modal for >10 (11–15 confirmed),
   reject <1/non-integer/>15.
7. Public/consent tokens: NO EXPIRY (permanent); leak runbook = deactivate
   child or delete session scope (revoke/regenerate for public links).
8. Finished DELETE: direct wipe (no soft-delete), ALWAYS via typed-text
   ConfirmDialog modal showing affected scope counts.

## User decisions locked (2026-10-08 re-audit round 3)

9. FAS children READ-ONLY (no create/edit/delete/deactivate; fixes via
   ADMIN/SA). FAS `POST|PATCH|DELETE /api/children` → 200 `FORBIDDEN_ROLE`.
10. Report content LIVE per link (missions/photos/narrative edits after
    send change what the link shows; no frozen per-link version for MVP).
11. Finished DELETE with sent links: direct wipe (option A) — modal shows
    scope counts INCLUDING active report/gallery/consent links; after wipe
    old links → 200 `TOKEN_INVALID`; `wa_outbox` retained 90 d; no
    link-revoke gate; ADMIN+SA may delete.
12. Consent SEND stays ADMIN/SA-only (FAS read-only status; resend hidden).

## User decisions locked (2026-10-08 re-audit round 4)

13. Group DELETE (Draft/Opened): cascade wipe (option B) — removes members
    from session + wipes their session data in one Tx; typed-text modal
    with counts. Single member remove: same per-child wipe, silent.
14. Report attestation: LIVE current owner name at open time, fallback
    `facilitator_name_snap` if owner deleted (option A); `assessor_id`
    keeps per-card authorship history.

## User decisions locked (2026-10-08 re-audit round 5)

15. Gemini endpoint LOCKED from user-provided official doc snippet:
    PRIMARY `POST
    https://generativelanguage.googleapis.com/v1beta/models/{GEMINI_MODEL}:generateContent`
    body `{contents:[{parts:[{text}]}]}` + headers `x-goog-api-key` +
    `Content-Type: application/json` (15 s timeout); default
    `GEMINI_MODEL=gemini-3.5-flash`. No `interactions` endpoint.
16. OpenWA image LOCKED to `openwa-openwa:latest` (verified present
    locally 2026-10-08: 3.39 GB, exposes `2785/tcp`, `WORKDIR /app`,
    `HOME=/app/data`, healthcheck `/api/health/ready`).
17. Session create MAY start empty: `POST /api/sessions` accepts missing
    or empty `groups[]` → Draft with zero groups; groups added later via
    `POST /api/sessions/:id/groups`. Gate enforced only at open:
    ≥1 group + every group non-empty + every group has active owner,
    else 200 `VALIDATION` / `GROUP_WITHOUT_FACILITATOR`.
18. Consent send REQUIRES membership: `POST
    /api/sessions/:sid/children/:cid/send-consent` only when the child is
    a member of that session (`session_members` row exists); non-member
    → 200 `NOT_FOUND` (same shape, no leak). Same membership gate
    applies to all per-child-per-topic `P` ops (assessment, photos,
    narrative, missions, report, public-links, send-report).

## User decisions locked (2026-10-08 clarification round 6 — WA + copy-data)

19. WA activation (round 6; updated 2026-11-09): an SA may connect
    WhatsApp from the dashboard connect modal — **Open OpenWA** still
    opens the OpenWA dashboard at `http://<host>:8003` (QR scan + API-key
    minting, once; key stored in `.env` as `OPENWA_API_KEY`, `api`
    recreated after), and **Connect Directly** starts the configured
    session and shows the QR in-app. ADMIN sees the badge/popover but the
    click is inert. `GET /api/wa/status` [SA/ADMIN] reads the configured
    OpenWA session (list → state, 5 s each) → 200 `{data:{connected,
    session, checked_at}}`; `connected` = session `ready`; unreachable →
    200 `{data:{connected:false}}`. `GET /api/wa/session` + `POST
    /api/wa/session/start` are SA-only and keep the API key server-side.
    Dashboard `WaStatusBadge` is EVENT-DRIVEN round 7 (`wa.changed`, no
    30 s poll). QR pairing remains the only manual step.
20. Same child in 2 sessions of the same program: ALLOWED, fully
    independent snapshots (assessment/photo/consent/report per session;
    consent per session; reports duplicated). No cross-session block,
    no warning gate — plus an OPTIONAL copy-data offer (decision 21).
21. Copy-data ("salin nilai dari Sesi X", ADMIN/SA only): offered in a
    modal when adding a child to a group if the child is a member of
    other same-program sessions (same tenant); modal lists candidates
    via `GET copy-candidates` with a dropdown (default = latest
    FINISHED, fallback latest date) + explicit Ya/Tidak per execution
    (re-runnable for other sources). Semantics: FILL-EMPTY ONLY;
    topic/activity matching BY master `topic_id`/`activity_id`;
    copies stars + notes (`assessor_id` = copier), narratives
    (only-if-empty), mission picks (only-if-zero-and-linked), photo
    FILES physically (new paths, only-if-target-
    zero, cap 10). The candidate flag is NEVER copied — copied rows are
    always `is_candidate=false`, the candidate is re-picked manually in
    the target session. Target FINISHED → the ENTIRE copy is rejected
    (200 `SESSION_LOCKED`: stars/notes/narratives/missions AND photos —
    no photo part runs). Consent + public links + `wa_outbox` NEVER copied.
    Photos copy even while target consent PENDING; later DITOLAK deletes
    ALL photo rows + files for that child+session (original + copied). Badges NEVER copied (derived, recompute).

## User decisions locked (2026-10-08 round 7 — realtime SSE, signal + refetch)

22. Realtime = 1 SSE endpoint (`GET /api/stream`) + 1 thin public
    endpoint (`GET /public/stream?token=`); ALL frames signal-only
    (`{entity, entity_id, rev}` — no PII, no bytes, no tokens, no codes);
    clients refetch ordinary REST (existing guards) on signal, debounced
    300 ms per table. Polling (5 s / 30 s / 60 s) DELETED.
23. Honest files: photo/gallery/report bytes render ONLY from server
    disk via `/uploads/…` after durability (temp + `fsync` + atomic
    rename; row + file one visible step). SSE never carries bytes and
    never announces a file before durability (no fake status): transient
    local preview → server 200 `{data}` → `photo.changed` → refetch →
    server `<img>`. Orphans swept unannounced.
24. Filter reuses REST scope functions (no duplicated auth logic);
    handshake = same guards as REST (failures = 200 envelope, no stream);
    limiter-exempt streams with connection caps (100/replica + 6/IP + 20/tenant);
    4 full-value exceptions ONLY (`readiness/pending/wa/consent-ack`);
    hub in-memory single-replica for MVP (no Redis); opened via `fetch`
    + reader in `lib/stream.ts` (zero new deps).

## User decisions locked (2026-10-09 round 8 — paging server-side pure)

25. Paging = SERVER-SIDE PURE (user-confirmed 2026-10-09): scope, filter,
    sorter, search, pagination ALL server-side; envelope stays
    `{data, meta:{page,per_page,total}}`; defaults `page=1, per_page=20`;
    allowed `per_page` 20/50/100/1000 (max 1000); client holds ONLY the
    active page; SSE signal → refetch SAME params (debounced 300 ms,
    `rev`-gated); no client slicing, no full-set hold, no `DataTable`.

## User decisions locked (2026-10-09 round 9 — test strategy + A4 fixed report)

26. Backend test tooling (user-confirmed 2026-10-09): `go test` (stdlib)
    + `github.com/stretchr/testify` (assertion) + `net/http/httptest`
    (HTTP handler testing) + `github.com/DATA-DOG/go-sqlmock` (mock DB —
    integration tests NEVER use a real database; no dockertest, no CI
    pipeline: CI is out of MVP scope). Coverage ≥90%
    business logic, 100% critical paths (auth, validation, idempotency,
    tenant isolation, envelope transport, SSE). Structure:
    `internal/<domain>_test.go` (co-located). Commands: `go test ./internal/... -v -cover`
    (unit), `go test ./internal/integration/... -v -tags=integration` (integration).
    Pass criteria: all pass + coverage + `go vet` + `gofmt`.

27. Frontend test tooling (user-confirmed 2026-10-09): `vitest` (Vite-native)
    + `@testing-library/react` (component testing) + `@testing-library/jest-dom`
    (DOM assertions) + `@testing-library/user-event` (user interaction)
    + `msw` (Mock Service Worker, API mocking) + `@vitest/coverage-v8` (coverage).
    Coverage ≥90% components, 100% critical paths (auth context, forms,
    report export, SSE, envelope handling). Structure: `src/**/*.test.ts(x)`
    (co-located). Commands: `vitest run` (unit), `vitest run --config vitest.integration.config.ts`
    (integration). Pass criteria: all pass + coverage + `tsc --noEmit` + `oxlint`.

28. Report layout A4 FIXED (user-confirmed 2026-10-09): A4 portrait
    (210mm × 297mm) FIXED. No other paper sizes. Orientation: Portrait only.
    Margins: 10mm all sides. Content area: 190mm × 277mm. Scaling: Content
    scales to fit ONE A4 page (no multi-page for MVP). Overflow: show
    overflow notice (no auto-paginate). Export formats: PNG (via `toPng`)
    and PDF (via `jsPDF` with `format:'a4'`). Print CSS: `@page { size: A4
    portrait; margin: 10mm; }`. Responsive: Report view adapts to screen
    size but maintains A4 aspect ratio (1:1.414). Mini report: Same A4
    fixed layout, scaled down for screen preview (no separate mini layout).

## User decisions locked (2026-10-11 — spec-code sync remediation round 10)

29. SA cross-tenant summary = PER-TENANT COUNTS (user-confirmed 2026-10-11,
    audit remediation K7): `GET /api/dashboard/tenant-summary` [SA-only]
    returns `{data:{tenants:[{tenant_id,name,type,is_active,children,sessions_open,sessions_finished,reports_issued}]}}`
    ordered `id ASC`; every number belongs to ONE tenant row — NO
    cross-tenant mixed aggregate anywhere; `reports_issued` = narratives +
    public_links kind=REPORT (08 definition); FE renders a per-tenant
    summary table on the SA dashboard, refetched on `stats.changed` (no
    new SSE event).
30. AI narrative output stored AS-IS (user-confirmed 2026-10-11): the ≤50-word
    ID paragraph is guaranteed via prompt instruction only
    (`prompts/system.md`); NO programmatic word-truncation or language check
    on the Gemini response; regenerate replaces only on success.
31. `GET /api/tenants` accepts optional `?is_active=true|false`
    (user-confirmed 2026-10-11): invalid value → 200 VALIDATION; complements
    the existing `?q` filter (FE TenantsPage already sends it).

## User decisions locked (2026-10-11 — ketentuan upload, decision 32)

32. Upload rule (user-confirmed 2026-10-11, Pembaruan): ALL uploads
    (foto, badge program/topik, frame) — ukuran maksimum **50 MB** per
    berkas, format **jpg/jpeg/png/webp** (case-insensitive), **client
    side validation only** (server TIDAK memvalidasi ukuran/format; tanpa
    hard cap server-side, tanpa server mime sniff). Ketentuan lama
    dicabut: ≤2 MB (badge/frame), ≤10 MB (foto), server mime sniff + hard
    cap; nginx `client_max_body_size` 12m → 52m dan Fiber `BodyLimit`
    12 MB → 52 MB (menyesuaikan maks 50 MB + multipart overhead —
    transport framing, bukan validasi file; body > limit → 200
    `VALIDATION`, lihat 06). Full text: section `[PEMBARUAN 2026-10-11]`
    di atas; diperingkas sebagai **Change 15** di `07`.

## User decisions locked (2026-10-11 — project structure, folder & file layout)

33. Badge/frame upload path = **tenant-di-luar** (user-confirmed 2026-10-11):
    `badges/<tenant_id>/<rand32hex>.<ext>` and
    `frames/<tenant_id>/<rand32hex>.<ext>`; photos stay
    `<tenant_id>/<session_id>/<stid>/<child_id>/<rand32hex>.<ext>`.
    Canonical in `05§8`; supersedes the tenant-di-dalam wording in
    `04-FL-04`/`04-FL-05` and the former code
    (`<tenant_id>/badges|frames/…`) — reconciled via `07` Change 16;
    the code now writes the tenant-di-luar layout (implemented in
    audit round 11, `07` Change 17 — new writes only; existing data
    stays per decision 45).
34. Runtime file storage = `data/uploads/` (host bindmount) ←
    `UPLOAD_DIR=/data/uploads` (container). There is NO `backend/uploads/`
    dir; earlier AGENTS.md wording is corrected.
35. `backend/internal/` ships three cross-domain infra modules beyond the 24
    vertical slices, listed in `02§2`: `envelope.go` (HTTP-200 envelope
    writer), `pscope.go` (per-connection SSE scope), `idempotency.go`
    (`X-Idempotency-Key` replay store).
36. Every `frontend/src/pages/<group>/` group MUST own a `routes.tsx`;
    `router.tsx` spreads the per-group route tables (canonical, `02§3`+`02§7`).
37. `docs/spec/` = 10 spec docs (00–08 + SPEC-INDEX.md). `DESIGN.md` is extra
    and outside the SPEC-INDEX document table; the Document index table
    includes a `SPEC-INDEX.md` self-row.

## User decisions locked (2026-10-11 — audit & antd migration round 11)

38. Report UI split (D-1, user-confirmed 2026-10-11): the interior of
    `#report-a4` (the export capture node rendered by `ReportView`) STAYS
    plain CSS — the spec wins over the total-antd directive for that
    interior; all chrome AROUND it (view pages, buttons, alerts, panels,
    menus) uses antd. The old `01§5` "report screen = plain CSS" claim
    reworded to
    this split form.
39. `@ant-design/plots` approved as a new frontend dep (D-2,
    user-confirmed 2026-10-11; charts on the dashboard; §9 approval
    granted). Frontend deps are now 9: `react-router`, `antd`,
    `@ant-design/icons`, `@ant-design/plots`, `country-flag-icons`,
    `i18next`, `react-i18next`, `html-to-image`, `jspdf` (listed in
    `01§5`).
40. `country-flag-icons` RETAINED and added to the `01§5` package list
    (D-3, user-confirmed 2026-10-11) — flag icons in the SettingsModal
    language selector; no longer an unlisted dep.
41. `StarInput` becomes antd `Rate` (D-5, user-confirmed 2026-10-11);
    the StarInput tests are rewritten accordingly (BB/MB/BSH/BSB labels
    preserved via i18n).
42. TOTAL antd replacement (D-6, user-confirmed 2026-10-11): every
    non-antd UI element in `frontend/src` is replaced/repainted with
    DIRECT antd components (figure→Card, div grid→Row/Col, etc.).
    Exceptions: (a) interior `#report-a4` (decision 38), (b) native
    `<video>` camera preview (no antd equivalent), (c) canvas capture
    mechanics. `01§5` component enumeration = the FINAL antd set used
    post-migration.
43. `?q` substring filter implemented on ALL paged list GETs (D-7,
    user-confirmed 2026-10-11), INCLUDING consents
    (`GET /api/sessions/:sid/consents`, `GET /api/consents`) and
    reset-tokens (`GET /api/reset-tokens`) — `02§2b` paged-list keys
    govern; `05§3` reworded accordingly.
44. FAS foreign-group reduced shape (D-8, user-confirmed 2026-10-11) =
    `{gid, name, own, member_count, status}` where `status` = the parent
    session's status (`session_groups` has no status column — the value
    is DERIVED from the session row). `02§2b` adds `own` (keeps
    `status`); the contracts doc adds `status` (keeps `own:false`
    semantics); both note the derivation.
45. Badge/frame migration stance (D-4, user-confirmed 2026-10-11): NEW
    writes go to the tenant-di-luar layout (`badges/<tenant_id>/`,
    `frames/<tenant_id>/`, decision 33); EXISTING data stays untouched
    (reads are path-agnostic — old tenant-di-dalam files keep serving);
    tenant hard-delete wipe ALSO removes `<uploads>/badges/<tenant_id>`
    and `<uploads>/frames/<tenant_id>` (all three dirs, `03 F-02` /
    `02§4`; migration note in `05§8`).

## User decisions locked (2026-10-12 — consistency audit round 12 + antd notification)

46. Endpoint feedback notifications (user-confirmed 2026-10-12): EVERY
    explicit user action (submit/save/send/delete/generate/copy/
    approve/export/upload) surfaces its outcome via antd `notification`
    (card at viewport corner, `useNotification` hook — static methods
    deprecated): success envelope → `notification.success`;
    business-error envelope → `notification.error` with the i18n
    message + error `code` (+ recovery hint from `06§1`). SSE-signal
    refetches and background page refetches NEVER notify (no realtime
    spam). Antd defaults (`topRight`, ~4.5 s). Normative: `02§3` +
    `03 F-17` + `08` acceptance rows. Spec-only — the `frontend/src`
    implementation is a separate task.
47. Readiness canonical route (user-confirmed 2026-10-12): `GET
    P/readiness` (= `/api/sessions/:sid/topics/:stid/children/:cid/
    readiness`) is the ONLY readiness endpoint; the duplicate
    session-level `GET /api/sessions/:id/readiness?child_id=&
    session_topic_id=` is removed from `02§2b` (membership gate
    decision 18 applies — non-member → 200 `NOT_FOUND`).
48. `oxlint` APPROVED as the frontend lint gate (user-confirmed
    2026-10-12): added to `01§10.4` test dependencies; the `08`
    pass criteria keep `oxlint` ("no lint errors").
49. Audit-§4 bookkeeping (user-confirmed 2026-10-12): the 22
    implementation-confirmation items tracked in
    `docs/audit/2026-10-11-spec-codebase-sync-audit.md` §4 remain OPEN
    but NON-BLOCKING; every "DECISION REQUIRED: none" claim in
    `07`/`08`/`SPEC-INDEX` is scoped to the locked spec decisions
    (now 1–50). No item is silently answered or guessed.
50. `DESIGN.md` stance (user-confirmed 2026-10-12): the file STAYS in
    `docs/spec/` as a NON-BINDING visual design-pattern reference
    (YowTix dashboard — hierarchy/density/palette/card treatment/chart
    language); per decision 37 it is outside the spec document set and
    never overrides Kidversa spec text.

## User decisions locked (2026-10-12 — audit §4 closure, decisions 51–72)

51. Reset-code reveal = single-shot SELF-DESTRUCT (item 1,
    user-confirmed 2026-10-12): the audited reveal (`?reveal=1` /
    `POST /api/reset-tokens/:id/reveal`) returns `code_plain` ONCE then
    purges it immediately; subsequent reveal → 200 `VALIDATION`
    (masked only, shows plain `***`); purge on
    use/revoke/expiry/FIRST-REVEAL. Locations: `00§3`, `02§2b`,
    `03 F-01`, `04 FL-03`, `05§2`, `08`. Code task.
52. Cookie-only logout (item 2): `POST logout` accepts the refresh
    cookie even when the access token is expired/absent → revoke jti +
    clear cookie → 200. Locations: `02§2b`, `03 F-01`, `06§5`, `08`.
    Code task.
53. SA READ of inactive tenants ALLOWED (item 4, direction confirmed:
    "perbaiki kode"): member calls → 200 `TENANT_INACTIVE`; SA READ
    allowed / SA WRITE → `TENANT_INACTIVE`; deleted tenant →
    `TENANT_INACTIVE` for all. `02§2` wording amended; upholds the
    `02§4`/`03 F-02`/`08` "SA read-only retained" claims. Code task
    (tenant guard bypass for SA reads only).
54. SA stream auto-pick (item 5a): StreamGate with SA `tenant:null`
    auto-selects the latest ACTIVE tenant via `POST switch-tenant` at
    mount (mirrors the SA first-login default); no active tenant →
    stream stays closed (graceful degradation). Locations: `02§3`,
    `08`. Code task.
55. Session hardening (item 5b): (1) second consecutive INVALID_TOKEN
    during refresh-retry forces logout (clear session → login);
    (2) refresh rotation validates the target tenant's active state —
    inactive → revoke jti + clear cookie + 200 `TENANT_INACTIVE`
    (fail-closed). Locations: `03 F-01`, `06§5`, `08`. Code task.
56. Mission text LIVE (item 6): the report renders the mission
    name/body LIVE from the bank at open time; `report_missions`
    `mission_name_snap`/`mission_body_snap` are HISTORY-ONLY; the
    mission SET resolves live. Supersedes the "picks snapped (render)"
    reading. Locations: `02§2`, `03 F-05`, `04 FL-05`, `05§2`. Code
    task (render reads bank).
57. Eligibility + picker wording (item 7): program eligible iff
    ≥1 topic × ≥1 activity AND program + topics `is_active` (create
    gate binding; the wire `Eligible` flag aligned with the gate);
    inactive picker entries render DISABLED with reason (not hidden).
    Locations: `03 F-04`, `04 FL-04/FL-06`, `06§5`, `08`. Code task
    (Eligible flag).
58. `GET /api/missions` readable by FAS (item 8): read-only,
    own-tenant (required by the F-12 mission picker); other master
    routes stay ADMIN/SA. Location: `02§2b`, `08`. Code task.
59. Badge files ALWAYS deleted with their master (item 9): the code's
    share-check is REMOVED; deleting a program/topic already used by
    ≥1 session requires typed-text confirmation showing the affected
    session count; old sessions stay intact except the badge image,
    which the report omits via the badge-less rule. Locations:
    `03 F-04`, `04 FL-04`, `08`. Code task (remove share-check +
    confirm counts).
60. Open-gate deterministic order (item 10): (1) ≥1 group AND every
    group non-empty → else 200 `VALIDATION` (+list) — empty-group
    check FIRST; (2) every group with an active owner → else 200
    `GROUP_WITHOUT_FACILITATOR` (+list). Location: `04 FL-08`, `08`.
61. Session times both-or-neither (item 11, user form rule): start
    filled ⇔ end required (one without the other → 200 `VALIDATION`,
    client form enforces); BOTH blank → NULL (all-day event, no
    computed end); both set → `end>start` else `VALIDATION`. Locations:
    `03 F-08`, `04 FL-07`, `05§2` CHECK (DDL rides the migration
    update). Code + migration task.
62. Program delete cascade (item 12): master topics + kegiatan +
    link rows (`frame_programs`, `mission_topics`) deleted in one Tx;
    publish `program.changed` + `frame.changed` when a frame link is
    removed (fixes K12). Locations: `03 F-04`, `04 FL-04`, `08`. Code
    task.
63. Narrative prompt redaksi (item 13): `user.md` rendered via
    `strings.Replace`; `system.md` pasted VERBATIM as system
    instruction (no placeholders); ≤50 words stays
    prompt-instruction-only — NO programmatic enforcement (confirms
    decision 30). Locations: `03 F-11`, `04 FL-12`.
64. Public-link creation gated (item 14, supersedes the "links never
    gated" part of the 2026-10-11 enforcement clarification):
    `POST P/public-links` → 200 `REPORT_INCOMPLETE` while incomplete —
    an incomplete report can never have a public link; `GET P/report` +
    admin/FAS/SA previews + public render stay NEVER gated. Locations:
    `02§2b`, `03 F-12`, `04 FL-13`, `06 E17`, `08`. Code task.
65. Revoke/regenerate tenant-scoped (item 15): the ADMIN caller must
    belong to the link's tenant (SA any tenant); cross-tenant →
    200 `NOT_FOUND` (no-leak). Locations: `02§2b`, `03 F-12`,
    `04 FL-13`, `08`. Code task.
66. Star distribution granularity (item 16): per-tenant global
    BB/MB/BSH/BSB distribution + per-topic aggregate; per-activity
    histogram OUT of MVP (contracts shape stays). Locations: `00§3`,
    `03 F-13`.
67. Copy-data trio (item 17): (i) source wiped MID-execution (after
    the pre-Tx probe) → accepted race: 200 with the ACTUAL (possibly
    partial) counts, re-run copies the remainder; (ii) the candidate
    FLAG never transfers — the candidate photo itself copies as a
    normal photo (`is_candidate=false`); (iii) `copy_available` = a
    non-PII boolean hint on `member.changed`/`session.changed{created}`
    routing keys — the four full-value exceptions stay exactly four.
    Locations: `03 F-15/F-16`, `04 FL-16`, `06§5`.
68. Membership-first uniform gate order (item 18): per-child P ops
    check MEMBERSHIP first (non-member → 200 `NOT_FOUND` regardless of
    payload validity), then body VALIDATION, then state gates; consent
    send: membership → state (non-member in FINISHED → `NOT_FOUND`).
    Location: `06§5`. Code reorder task.
69. gofmt the 5 files (item 19): run `gofmt` on the 5 unformatted
    backend test files (auth_extra, consents_extra, infra_extra,
    misc_extra, reports_extra) — formatting only, no behavior change;
    the `08` gate stays truthful. Code task.
70. Oxlint zero-warning strict (item 20): the gate = 0 error AND
    0 warning; the 11 existing React warnings must be fixed.
    Locations: `01§10.2`/`§10.4`, `08`. Code task.
71. Backups manual-MVP (item 21): backups = MANUAL host copy for MVP;
    NO schedule/tool/restore-test in repo by design; automation +
    restore verification = post-MVP. Location: `05§5`.
72. Open verification items (items 20b+22): (a) full frontend coverage
    run (`vitest run --coverage`, writes artifacts); (b) live
    `docker compose up` boot (4 services + seed + `/readyz`) — both
    PENDING dedicated sessions; recorded in `08`. Item 3 = NO change
    (the reset-dashboard superset `{id, code_masked, expires_at,
    seconds_left, revoked, mail_failed, user}` was already unified
    2026-10-11 in `02§2b`/`03 F-01`/`04 FL-03`/`05§2` — confirmed).

## Implementation notes

- Read `01-TECHSTACK.md` first for verbatim API names; `02§2b` for the
  canonical route table; `06§1` for the single code taxonomy.
- DB: zero FK, every domain/link/snapshot row carries `tenant_id`;
  snapshot ids (`stid/said/gid`) in URLs inside sessions.
- Headers: `Authorization: Bearer`, `X-Tenant-ID` (required on every
  authenticated call; SA may differ per switcher semantics),
  `X-Idempotency-Key` (mutating POSTs, 24 h replay).
- Report UI split (decision 38): interior of `#report-a4` plain CSS,
  chrome around it antd; export `toPng→addImage`, never
  `doc.html()`; WA has no file payload.
- At implementation confirm live only: QR pairing flow (text-only sends
  need no media-field check). Locked 2026-10-08 round 5:
  `GEMINI_MODEL=gemini-3.5-flash` via `:generateContent` (user doc
  snippet); OpenWA image `openwa-openwa:latest` (verified local).

## Unresolved decisions

None blocking among locked spec decisions (1–72). Audit §4 is CLOSED
(decisions 51–72, 2026-10-12 — former decision 49 bookkeeping
superseded). Open VERIFICATION items (decision 72): full frontend
coverage run + live Docker compose boot — pending dedicated sessions.
`DECISION REQUIRED: none` applies to the spec itself.

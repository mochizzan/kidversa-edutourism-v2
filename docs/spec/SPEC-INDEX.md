# SPEC-INDEX — Kidversa EduTourism v2

Specification Status: DRAFT (2026-10-09; adding test strategy + A4 fixed report; pending audit)

## Project

Kidversa EduTourism v2 — PAUD/TK assessment and portfolio-report web
application. Fresh build (no v1). See `00-SCOPE.md`.

## Document index

| File | Content |
|---|---|
| `00-SCOPE.md` | problem, objective, scope, actors, constraints, assumptions, terminology (incl. envelope, X-Tenant-ID, Idempotency-Key) |
| `01-TECHSTACK.md` | locked stack + verified APIs (Fiber v3 incl. `middleware/sse` transport-only, React 19, Vite 8, antd v5, OpenWA send-text, Gemini generateContent) + SSE protocol + honest-file rule |
| `02-ARCHITECTURE.md` | components, pipeline, canonical route table (`GET /api/stream` + `GET /public/stream?token=`), event catalogue + filter + caps (§2c), frontend guards/menu (`lib/stream.ts`, `StreamGate`, event-driven badge/countdown), isolation, boundaries |
| `03-FEATURES.md` | feature inventory + functional requirements + business rules per role (F-01..F-16 incl. realtime SSE) |
| `04-FLOWS.md` | canonical end-to-end flows FL-01..FL-16 with snapshot ids, failure branches, and SSE publish → signal → refetch tails (FL-10 honest bytes) |
| `05-DATA.md` | entities, fields, checks, indexes, shapes (incl. SSE frame), transforms, persistence (durable-file rule), hub + publish-after-commit |
| `06-ERRORS-AND-EDGE-CASES.md` | HTTP-200 envelope taxonomy (E1..E32 + EX), stream propagation/reconnect/filter/caps, retry, races, edges (incl. SSE gap/orphan/revoke) |
| `07-BEFORE-AFTER.md` | BEFORE (empty) vs AFTER per change incl. 28 user decisions (Change 11 = SSE migration, Change 12 = server-side paging, Change 13 = test strategy + A4 fixed) |
| `08-ACCEPTANCE-CRITERIA.md` | testable Given/When/Then (all 200-envelope assertions + SSE realtime + paging blocks + test strategy acceptance criteria) |

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

## Implementation notes

- Read `01-TECHSTACK.md` first for verbatim API names; `02§2b` for the
  canonical route table; `06§1` for the single code taxonomy.
- DB: zero FK, every domain/link/snapshot row carries `tenant_id`;
  snapshot ids (`stid/said/gid`) in URLs inside sessions.
- Headers: `Authorization: Bearer`, `X-Tenant-ID` (always),
  `X-Idempotency-Key` (mutating POSTs, 24 h replay).
- Report screen plain CSS (no antd); export `toPng→addImage`, never
  `doc.html()`; WA has no file payload.
- At implementation confirm live only: QR pairing flow (text-only sends
  need no media-field check). Locked 2026-10-08 round 5:
  `GEMINI_MODEL=gemini-3.5-flash` via `:generateContent` (user doc
  snippet); OpenWA image `openwa-openwa:latest` (verified local).

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

19. WA activation: WhatsApp connection ("Sesi WA") is activated ONLY in
    the OpenWA dashboard at `http://<host>:8003` (QR scan + API-key
    minting, once; key stored in `.env` as `OPENWA_API_KEY`, `api`
    recreated after). It is NEVER activated from the SA dashboard.
    `GET /api/wa/status` [SA/ADMIN] proxies OpenWA `/api/health/ready`
    (+ best-effort session info, 5 s each) → 200
    `{data:{connected, session, checked_at}}`; unreachable → 200
    `{data:{connected:false}}`. Dashboard `WaStatusBadge` is EVENT-DRIVEN
    round 7 (`wa.changed`, no 30 s poll). QR pairing remains the only
    manual step.
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
    FILES physically (new paths, candidate preserved, only-if-target-
    zero, cap 10). Consent + public links + `wa_outbox` NEVER copied.
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
    (HTTP handler testing) + `github.com/DATA-DOG/go-sqlmock` (DB mocking)
    + `github.com/ory/dockertest` (optional, real DB in CI). Coverage ≥90%
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
    report export, SSE, envelope handling). Structure: `src/**/*.test.tsx`
    (co-located). Commands: `vitest run` (unit), `vitest run --config vitest.integration.config.ts`
    (integration). Pass criteria: all pass + coverage + `tsc --noEmit` + `eslint`.

28. Report layout A4 FIXED (user-confirmed 2026-10-09): A4 portrait
    (210mm × 297mm) FIXED. No other paper sizes. Orientation: Portrait only.
    Margins: 10mm all sides. Content area: 190mm × 277mm. Scaling: Content
    scales to fit ONE A4 page (no multi-page for MVP). Overflow: show
    overflow notice (no auto-paginate). Export formats: PNG (via `toPng`)
    and PDF (via `jsPDF` with `format:'a4'`). Print CSS: `@page { size: A4
    portrait; margin: 10mm; }`. Responsive: Report view adapts to screen
    size but maintains A4 aspect ratio (1:1.414). Mini report: Same A4
    fixed layout, scaled down for screen preview (no separate mini layout).

## Unresolved decisions

None blocking. `DECISION REQUIRED: none`.

# 00 — Scope

Status: DRAFT (2026-10-09; adding test strategy + A4 fixed report; pending audit)

## 1. Problem

PAUD/TK teachers need a simple way to assess young children per learning
activity and produce a shareable progress report (rapor) for parents.
There is no existing system (fresh build, no v1). Manual recording is
slow, inconsistent, and hard to share.

## 2. Objective

Build Kidversa EduTourism v2 — a web application that lets facilitators
(teachers) assess children per activity inside a scheduled session and
lets admins/SA generate a per-topic portfolio report per child, enriched
with an AI narrative, home missions, badges, and photos, then deliver it
to parents via WhatsApp (text + public links; no file attachments).

## 3. Scope (IN)

- Multi-tenant data isolation (tenant = generic: city today, country/other tomorrow).
- 3 login roles: Superadmin (SA), Admin (per tenant), Facilitator (assessor).
- Registered-only data: Child (Siswa/Peserta), Parent (Orang tua, no login, no dashboard).
- Master data per tenant: Program, Topik, Kegiatan, Bank Misi, Frame, Child, User.
- Session management: snapshot a whole Program into a Session, organise
  Groups (group name + owner facilitator + child participants; `groups[]`
  OPTIONAL at create — Draft may start empty, groups added later, gate at
  open), run the
  Draft → Opened → Finished lifecycle (no CANCELED state; Draft/Finished
  delete = physical wipe, no trace).
- Assessment: star rating 1–4 (BB/MB/BSH/BSB) + observation note per child
  per activity, saved per activity, partial save allowed.
- Photos: max 10 per child per topic per session, 1 report candidate,
  camera capture with 9:16 frame overlay (frame fused) or gallery upload.
- Photo consent: per-child-per-session approval link sent by ADMIN/SA
  via WhatsApp (no login; FAS read-only), default locked, permanent
  first-wins decision.
- AI narrative: per child per topic, generated via Google AI Studio
  (Gemini), editable, regenerable.
- Report: per child per session per topic — screen view + client-side
  PNG/PDF export (never stored server-side); public no-login URLs for
  report and gallery; WhatsApp delivery single + batch (text + 2 URLs,
  3–10 s random jitter, per-item status).
- Reset-password code relay: hashed storage; dashboard shows masked code +
  expiry countdown; plaintext reveal-on-demand (audited) + revoke.
- Dashboards, overview, analytics (counts, star distribution, progress).
- Bilingual UI (Indonesian default + English). Report content stays in
  Indonesian.
- Docker deployment: 4 services (`db`, `api`, `web`, `wa`), bindmount
  persistence, single `compose.yml`, env via `env_file`, local 1:1 prod.

## 4. Out of scope

- Parent/student login or dashboards.
- Global program templates shared across tenants.
- Online payment, scheduling beyond session date/time/location.
- Native mobile apps (web app only for MVP).
- Automatic PDF rendering on the server; WA file attachments (link-only).
- Multi-language report content (UI only for MVP).
- Advanced analytics/ML beyond counts, distributions, progress.
- Offline mode (edits are online-only; per-activity saves persist).

## 5. Actors

| Actor | Login | Scope |
|---|---|---|
| Superadmin (SA) | yes (seeded) | all tenants via tenant switcher; manages tenants + admin accounts; can act as admin in any active tenant |
| Admin | yes (register + SA approval) | one tenant; manages everything in that tenant except tenants |
| Facilitator | yes (register + Admin/SA approval) | own groups only: assess, photo, gallery, report, report-WA send (link-only); consent status read-only (send hidden); plus own profile |
| Child | no | registered data row |
| Parent | no | registered data (mother required, father/guardian optional); receives WA text+links; opens public links; responds to consent link |

## 6. Constraints

- Stack locked: Go 1.26.6 + Fiber v3, Vite React TS, MariaDB 12.3 LTS
  (see `01-TECHSTACK.md`).
- Minimal libraries only (4 backend + 9 frontend npm packages, listed in `01`).
- Database: zero FK constraints (all references loose, app-enforced);
  snapshots/duplication preferred over strict normalisation; every domain
  row carries `tenant_id`.
- Photos stored on server disk (`/data/uploads`), DB stores relative path only.
- Report PDF/PNG generated in the browser, never stored or proxied.
- WA delivery is text + public URLs only (`send-text`); server never
  handles report file bytes.
- API response rule (binding, detail in `02§2b`, `06§1`): every
  `/api/*` and `/public/*` endpoint returns HTTP 200 for all business
  outcomes (`{data}` or `{error:{code,message}}`); HTTP 500 only for
  unhandled server faults. Client branches on the envelope, never on status.
- All persistent data via relative bindmounts (`./data/...`), no named volumes.
- One `compose.yml` for local and production; secrets only in `.env`
  (never `environment:` values, never baked into images).

## 7. Assumptions

- Seed: 1 tenant (Bandung, type KOTA) + 1 SA account (idempotent rerun).
- Email is globally unique (one email = one account system-wide).
- 1 session always covers 1 whole program (all topics assessed in it).
- Same child MAY join 2+ sessions of the same program (round 6): each
  session is a fully independent snapshot (own assessment/photo/consent/
  report); optional fill-empty copy-data modal on member-add.
- Realtime (round 7): 1 SSE endpoint for authenticated pages
  (`GET /api/stream`) + 1 thin public endpoint (`GET /public/stream?token=`);
  all SSE frames are signals (`{entity, entity_id, rev}` — no PII, no
  bytes, no tokens); clients refetch ordinary REST (with existing guards)
  on signal. Polling (5 s / 30 s / 60 s) removed. Photo/file bytes are
  served ONLY from server disk via `/uploads/…` after durable save;
  SSE never carries bytes and never announces a file before it is
  durably stored (no fake status).
- No limits on group/session/participant counts; batch WA max 10 items
  per request (client chunks larger sets).
- School name is free text (no school master).
- Duplicate child names allowed (no dedup check; UI disambiguates).
- Logo on reports is global/static placeholder for MVP.
- Server clock is UTC; JWT leeway 60 s; reset/AI TTLs server-side only.

## 8. Terminology

- Tenant: isolated data owner (city/country/other). All domain rows carry it.
- Program → Topik → Kegiatan: curriculum hierarchy. Kegiatan is the unit of assessment.
- Sesi (Session): scheduled execution of one whole program; holds an
  isolated snapshot copy of program/topics/activities/badges.
- Snapshot ID: `session_topics.id` / `session_activities.id` /
  `session_groups.id` used in URLs (never master-table ids inside sessions).
- Kelompok (Group): subdivision of a session; 1 group = 1 owner
  facilitator + N children; one child in at most 1 group per session
  (UNIQUE(child,session) + app check).
- Badge: completion marker (topic-level + program-level), independent of
  star values (completion-only).
- Misi (Mission): home activity from the per-tenant mission bank, linked
  to 1+ topics, manually picked (max 4, server-enforced) at report time.
- Frame: PNG/JPG/WebP overlay for photos, owned per tenant, linkable to 1+ programs.
- Consent: parent photo permission per child per session (first decision wins).
- Rapor: portfolio report per child per session per topic.
- Envelope: `{data}` success vs `{error:{code,message}}` business failure,
  always HTTP 200 (except 500).
- X-Tenant-ID: request header carrying the client's active tenant;
  compared against the JWT tenant claim (`TENANT_MISMATCH` on stale tab).
- Idempotency-Key: `X-Idempotency-Key` header on mutating POSTs listed in
  `02§2b`; replay returns the original result without a second side-effect.

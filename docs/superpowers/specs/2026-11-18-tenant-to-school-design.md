# Tenant → School: full end-to-end refactor — design

Date: 2026-11-18
Status: approved and implemented 2026-11-18 (see `docs/superpowers/plans/2026-11-18-tenant-to-school.md`)
Scope: the entire repo (database, Go backend, React frontend, tests, docs)

## 1. Intent and why

The product's tenant concept is now a **School**. This is not a cosmetic reword:
the tenant entity's identity field becomes `school_name`, the old `type`
classification goes away, and Country / City become first-class optional fields.
The user also chose to rename the multi-tenancy plumbing itself — identifiers,
HTTP header, storage key, error codes, SSE events, DB table and columns — so
that the codebase consistently says "school" instead of "tenant".

Why: the current schema stores a classification (`KOTA` / `NEGARA` / `LAINNYA`)
where the product now needs actual location data, and the entity being managed
is a school rather than a generic tenant.

## 2. Goals

1. A `School` entity with fields: `id`, `school_name`, `country`, `city`, `is_active`, `created_at`.
2. `country` is optional and defaults to `"Indonesia"`; `city` is optional and nullable.
3. Existing rows become schools named `"Kidversa"` in Indonesia with no city.
4. `school_name` is **not** globally unique; duplicate names are allowed.
5. Every "tenant" identifier in code, API, events, storage, and schema becomes "school".
6. A new child's `school` defaults to the active school's `school_name` (prefilled in the form, and filled by the server if omitted).
7. Country and City appear in the create/edit modal and the schools list columns.
8. All existing tests are updated to the new shape and still pass.

## 3. Non-goals / anti-goals

- **Not** renaming the product brand "Kidversa" (`.env` `MARIADB_DATABASE`, `MARIADB_USER`, `OPENWA_SESSION`, `data/openwa/sessions/session-kidversa`, module path, WA templates, page title). Those are product names, not the tenant concept.
- **Not** changing `children.school` — it stays a free-text child-school name, `VARCHAR(200)`, with its existing wire key `school`. It is a different field from the school entity's `school_name`.
- **Not** changing numeric upload path segments (`data/uploads/<id>/...`). Only the vocabulary changes; IDs stay the same.
- **Not** adding a school master list, school lookup, or school hierarchy.
- **Not** changing any business flow other than the school rename, the new fields, and the child-school default.
- Anti-goal: a half-rename where the UI says "School" but headers, events, or DB columns still say "tenant". The user explicitly chose the full rename.

## 4. Constraints

- MariaDB 12.3, zero FK constraints (app-enforced), `utf8mb4_unicode_ci`, UTC datetimes.
- Migration runner (`backend/internal/db.go`) discovers `migrations/NNNN_*.sql` dynamically under `GET_LOCK('kidversa_migrate', 30)`; no runner code change is needed for `0004`. `backend/Dockerfile` copies the whole `migrations/` directory.
- DDL in MariaDB is not transactional: a failure partway through `0004` can leave a partially renamed schema. Deploy `0004` once, with a backup, and verify afterwards.
- Frontend and backend must be deployed together — the header, routes, envelope keys, SSE events, and error codes all change at the same time.
- `catalog_test.go` pins exact error-code and SSE-event catalogues; `frontend/src/test/catalogues.ts`, `errors.parity.test.ts`, `stream.parity.test.ts`, and `i18n.parity.test.ts` pin the same sets. All renames are 1-for-1, so catalogue sizes stay at 33 error codes and 30 SSE events, and id/en locale key sets must stay identical.
- `sqlmock` assertions use `regexp.QuoteMeta` fragments of the production SQL; every SQL fragment in tests must change in lockstep with the production query strings.
- Git-tracked runtime data does not exist (`data/**` is ignored); the live DB and OpenWA profile need an operational migration, not a file edit.

## 5. Locked decisions (from the user)

| # | Topic | Decision |
|---|-------|----------|
| 1 | `type` (`KOTA`/`NEGARA`/`LAINNYA`) | Remove completely — column, API field, UI select, i18n keys, type alias. |
| 2 | Name field | Rename to `school_name` end-to-end (DB, JSON, TS, variables). |
| 3 | Existing rows | Set `school_name = 'Kidversa'`, `country = 'Indonesia'`, `city = NULL`. Do not infer city from old data. |
| 4 | Uniqueness | Drop the global unique constraint on the name. Duplicate school names are allowed. |
| 5 | `children.school` | Stays separate. Default it to the active school's `school_name` on child creation. |
| 6 | Rename scope | Full: DB table/columns, all identifiers, `X-Tenant-ID` header, `kv-tenant` storage, error codes, SSE events, routes. |
| 7 | Child default layer | Both: prefill in the create form (editable) **and** server-side fallback when `school` is omitted. |
| 8 | Where Country/City show | Create/edit modal and schools list columns only. Not dashboard summary, not switcher. |
| 9 | Boot seed idempotency | Insert the seed school only when `schools` is empty (no name-based lookup). |
| 10 | Seed location | Move hardcoded seed data into separate SQL files under `backend/seeds/`, with the SA bcrypt hash checked in as a fixed string. Drop the Go seed constants and SA hashing/overwrite logic. |
| 11 | Child fallback source | Server uses `latestActiveSchool()` (global latest active), not the request-scoped session school. It may disagree with the form prefill; that is accepted. |
| 12 | No active school at child create | Fail with `VALIDATION`; never create a child with a blank school. |

### Calls made by the author (not stated by the user; marked so they can be checked)

- `school_name VARCHAR(200) NOT NULL` — reuses the existing `maxShortTextLen = 200` constant, matching `children.school`. The old `name` cap was 160.
- `country VARCHAR(100) NOT NULL DEFAULT 'Indonesia'`; `city VARCHAR(100) NULL`. Values are trimmed; empty `country` falls back to `"Indonesia"`, empty `city` becomes `NULL`. Both optional on input.
- JWT claim key becomes `school` (was `tenant`); existing sessions are invalidated and users log in again. This is a consequence of the full rename, accepted here rather than silently keeping a legacy claim.
- `kv-tenant` becomes `kv-school` with no compatibility shim; a browser with a stale `kv-tenant` key simply receives the server-chosen default school on next login.
- To disambiguate in the UI, the entity label becomes "School" / "Sekolah" and the child field label becomes "Child's school" / "Sekolah anak" (the `master:children.school` key may stay; only its copy changes).
- Existing `0001_init.sql`–`0003_admin_controls.sql` stay immutable; all schema change happens in a new `0004`. This keeps fresh installs working (0001 then 0004) and preserves the migration-runner tests that match `CREATE TABLE IF NOT EXISTS tenants`.
- Seed files are under `backend/seeds/` with `NNNN_*.sql` names, discovered dynamically like migrations. No version table is needed because every seed file is idempotent by construction. One statement per file so the runtime connection (which has `multiStatements=false`) can execute each verbatim.

## 6. Wire contracts after the change

### `GET /api/schools` list item and `POST /api/auth/login` / `refresh` / `me` school object

```json
{
  "id": 1,
  "school_name": "Kidversa",
  "country": "Indonesia",
  "city": null,
  "is_active": true,
  "created_at": "2026-01-02T03:04:05Z"
}
```

Keys removed: `name`, `type`. Keys added: `school_name`, `country`, `city`.

### Other wire changes (1-for-1 renames)

| Old | New |
|-----|-----|
| `tenant_id` (JWT claim, frame payload, request/response fields) | `school_id` |
| envelope key `tenant` | `school` |
| `tenant_name` (users list join column) | `school_name` |
| `X-Tenant-ID` header | `X-School-ID` |
| `TENANT_INACTIVE`, `TENANT_MISMATCH` | `SCHOOL_INACTIVE`, `SCHOOL_MISMATCH` |
| `tenant.changed` SSE event | `school.changed` |
| `/api/tenants`, `/api/auth/tenants`, `/api/auth/switch-tenant`, `/api/dashboard/tenant-summary` | `/api/schools`, `/api/auth/schools`, `/api/auth/switch-school`, `/api/dashboard/school-summary` |
| `data.tenants` (dashboard summary array) | `data.schools` |
| `?tenant=` dashboard query | `?school=` |
| `kv-tenant` localStorage | `kv-school` |

`children.school` and `master:children.school` are unchanged.

## 7. Database migration (`backend/migrations/0004_school_rename.sql`)

Ordered steps under the existing advisory lock:

1. `RENAME TABLE tenants TO schools;`
2. `ALTER TABLE schools`:
   - `DROP INDEX uq_tenants_name`
   - `RENAME COLUMN name TO school_name` (then widen to `VARCHAR(200)` if needed)
   - `DROP COLUMN type`
   - `ADD COLUMN country VARCHAR(100) NOT NULL DEFAULT 'Indonesia' AFTER school_name`
   - `ADD COLUMN city VARCHAR(100) NULL AFTER country`
   - `ADD KEY idx_schools_school_name (school_name)`
3. `UPDATE schools SET school_name = 'Kidversa';` (country default and city NULL are already correct).
4. For each of the 24 tables that carry the column: `ALTER TABLE <table> RENAME COLUMN tenant_id TO school_id;` and `RENAME INDEX <old_tenant_index> TO <new_school_index>;` — preserving each column's original NULL/NOT NULL and the index column order. Tables: users, refresh_tokens, reset_tokens, programs, topics, activities, missions, mission_topics, frames, frame_programs, children, sessions, session_topics, session_activities, session_groups, session_members, assessments, photos, narratives, report_missions, consent_links, public_links, wa_outbox, idempotency_keys. (`uq_idem_tenant_route_key` → `uq_idem_school_route_key`.)

`0001_init.sql` remains as-is; it is historical. Fresh installs run 0001 → 0002 → 0003 → 0004.

### Seed files (new `backend/seeds/`)

Seed data leaves Go. Add two single-statement SQL files, discovered and executed in filename order on every boot after migrations:

- `backend/seeds/0001_school.sql`
  ```sql
  INSERT INTO schools (school_name, country, city, is_active)
  SELECT 'Kidversa', 'Indonesia', NULL, TRUE
  WHERE NOT EXISTS (SELECT 1 FROM schools);
  ```
  Idempotent by emptiness, not by name — an upgraded DB already has backfilled rows, so it gets no new seed; a fresh empty DB gets one Kidversa school.

- `backend/seeds/0002_sa.sql`
  ```sql
  INSERT INTO users (school_id, name, email, password_hash, role, decision, is_active)
  SELECT NULL, 'Super Admin', 'sa@example.com', '<FIXED_BCRYPT_HASH>', 'SA', 'APPROVED', TRUE
  FROM DUAL
  WHERE NOT EXISTS (SELECT 1 FROM users WHERE email = 'sa@example.com');
  ```
  Insert-only (amended 2026-11-24): seeds run on every boot, so an existing
  bootstrap row is never rewritten. Dashboard edits to the SA row (password,
  name, decision, is_active) survive container restarts/recreation; the seed
  only fills the gap when the row is missing. The `NOT EXISTS` guard also
  preserves the existing rule that an email held by a non-SA row is never
  promoted or overwritten.

`<FIXED_BCRYPT_HASH>` is the bootstrap password for fresh installs. Because the
seed no longer rewrites an existing SA row, rotate the password from the
dashboard after first login (editing the file only affects fresh/missing rows).

`Seed(db *sql.DB)` becomes a small reader: list `seeds/*.sql`, sort ascending, `ExecContext` each file verbatim under a 10 s context, log a generic "seed applied" per file. No version table, no bcrypt import, no hardcoded constants, no multi-statements connection needed. `cmd/api/main.go` keeps calling `Seed(db)` after `Migrate(cfg)`. `backend/Dockerfile` gains `COPY seeds ./seeds`.

`backend/internal/db_test.go` is rewritten to mock one `ExecContext` per seed file instead of the INSERT IGNORE + SA lookup/update sequence.

## 8. Backend changes

- `internal/envelope.go`: rename codes, messages, `kvTenant` → `kvSchool`, frame doc comment; catalogue sizes unchanged.
- `internal/tenants.go` → `internal/schools.go` (and `tenants_test.go` → `schools_test.go`, `tenant_test.go` → `school_test.go`): `tenantItem` → `schoolItem` with the new shape; `validTenantType` deleted; `requireTenant` → `requireSchool`; `tenantWipeTables` stays the same table list; CRUD SQL selects `id, school_name, country, city, is_active, created_at`; create/patch validation uses `school_name` (trim, ≤200), `country` (trim, empty → Indonesia, ≤100), `city` (trim, empty → NULL, ≤100); delete duplicate-name VALIDATION branches; envelope key `school`; SSE `school.changed`; routes `/api/schools`.
- `internal/auth.go`: `tenantJSON` → `schoolJSON`; JWT claim `school`; header `X-School-ID`; `tenantActive`/`tenantByID`/`latestActiveTenant`/`tenantForSession` → `school*`; SQL column/table renames; `switch-tenant` → `switch-school`; `/auth/schools`; envelope key `school`.
- `internal/users.go`: join alias `t` → a non-colliding alias (do **not** blindly use `s`, which is already an alias for `sessions` in other queries); `tenant_name` → `school_name`; user wire keys `tenant_id` → `school_id`.
- `internal/children.go`: `tenant_id` → `school_id` SQL; make `school` optional on create; when omitted/blank, call `latestActiveSchool()` and use its `school_name`. If no active school exists, return `VALIDATION`. Preserve `school` as the child JSON key. (The frontend prefill uses the session's selected school, so the two sources can differ; this is accepted.)
- Every other domain slice (sessions, reports, assessments, photos, consents, programs, topics, activities, missions, frames, dashboard, idempotency, stream, files, seed, pscope, public): mechanical `tenant_id` → `school_id`, `tenant` locals → `school`/`schoolID`, SSE payload key, and log/slog keys.
- `cmd/api/main.go`: CORS `AllowHeaders` becomes `X-School-ID`.
- `internal/seed.go`: replaced by the SQL seed reader described above; remove `seedTenantName`, `seedTenantType`, `seedSAName`, `seedSAEmail`, `seedSAPassword`, `insertSeedSA`, `updateSeedSA`, `hashSeedSAPassword`.
- All test files: update hardcoded SQL fragments, row column lists, fixtures (`sqlTenantBandung()` → `sqlSchoolKidversa()`), exact JSON-key pins (`wire_pins_test.go`), catalogue pins (`catalog_test.go`), ordered dashboard-summary regex, `tenantWipeOrder`, and migration-runner fixture names.

No change to `backend/prompts/` or `backend/templates/` (no tenant placeholders).

## 9. Frontend changes

- `lib/auth.tsx`: `Tenant` → `School` with `{id, school_name, country, city, is_active}`; delete `TenantType`; `AuthContextValue.school`, `switchSchool`, `kv-school`, signal `school.changed`, payload `school_id`; routes to `/api/auth/switch-school`.
- `lib/api.ts`: `X-School-ID`, `kv-school`, `SCHOOL_MISMATCH` recovery, `setSessionSchool`, `healSchoolHeader`.
- `lib/stream.ts`: `X-School-ID`, `kv-school`, `SCHOOL_*` codes.
- `lib/feedback.ts` and `common.json`: entity `tenant` → `school`.
- `pages/tenants/` → `pages/schools/`: `TenantsPage` → `SchoolsPage`, `TenantModal` → `SchoolModal`, `routes.tsx` exports `schoolsRoutes` at `/schools`; remove the Type form item, add Country (default `Indonesia`) and City inputs, add both list columns, remove the duplicate-name error mapping and `nameTaken` key.
- `pages/dashboard/TenantSummaryTable.tsx` → `SchoolSummaryTable.tsx`: type/row keys and `data.schools`; no Country/City columns.
- `AppLayout.tsx`: switcher fetches `/api/schools`, CSS classes `kv-rail__school` / `kv-rail-school`; breadcrumbs key `schools`.
- `pages/children/ChildrenPage.tsx`: prefill `school` from `useAuth().school?.school_name` for new children (editable).
- `pages/users/*`, `pages/auth/RegisterPage.tsx`, `pages/sessions/controls.tsx`, `pages/profile/ProfilePage.tsx`: all tenant identifiers, `/api/schools`, `/api/auth/schools`, `school_id`, `school_name`.
- `router.tsx`, `test/{handlers,render,setup,catalogues}.ts`, `src/index.css`, `dashboard.css`: rename keys, paths, helpers, and classes.
- Locales `en/auth.json`, `id/auth.json`, `common.json`, `menu.json`, `session.json`: rename tenant keys/values to school, add `schools.schoolName` / `schools.country` / `schools.city`, drop `typeOptions` and `nameTaken`; keep id/en key sets identical. Child-field copy becomes "Child's school" / "Sekolah anak".
- All affected tests: endpoint paths, headers, storage keys, SSE frame text, catalogue pins, exact UI strings (`'Nama sekolah sudah digunakan.'` is deleted), and route inventory.

## 10. Config, docs, data

- `backend/cmd/api/main.go` CORS header (covered above). `backend/Dockerfile` gains `COPY seeds ./seeds`. `compose.yml` and `frontend/nginx.conf` need no edit (nginx forwards headers by default).
- `AGENTS.md` and every file under `docs/spec/`, `docs/audit/`, and `docs/superpowers/specs/` that mentions the tenant concept: terminology and contract updates. Product-brand "Kidversa" references stay.
- Operational, not code: back up and run `0004` against `data/mariadb/kidversa/`; leave `data/openwa/` and `data/uploads/` numeric paths alone.
- Regenerate `backend/cover` and frontend coverage artifacts after the rename; do not hand-edit them.

## 11. Verification

1. `cd backend && go build ./...`
2. `cd backend && go test ./internal/... -v -cover`
3. `cd backend && go test ./internal/integration/... -v -tags=integration`
4. `cd frontend && npm run build`
5. `cd frontend && npm run lint`
6. `cd frontend && npx vitest run`
7. `cd frontend && npx vitest run --config vitest.integration.config.ts`
8. Manual smoke: fresh DB migration and upgrade of the existing DB; restart twice and confirm no duplicate Kidversa school or SA row appears; login → school switcher shows "Kidversa"; create school with duplicate name succeeds; country defaults to Indonesia, city blank; create child under that school shows school prefilled; omit `school` and confirm the server falls back to the latest active school; deactivate all schools and confirm child create returns VALIDATION; report still shows the child's own school.

## 12. Left to the builder

Sequencing and commit slicing are left to the plan. Minor details the builder may decide without asking: exact CSS class names, variable names, whether `idx_schools_school_name` is added (recommended), and the precise order of the mechanical search/replace passes. Any new user-facing decision (for example, keeping legacy sessions alive or preserving the old browser storage key) is **not** left to the builder — bring it back to the user.

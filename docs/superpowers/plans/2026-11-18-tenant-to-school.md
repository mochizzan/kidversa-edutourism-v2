# Tenant → School: implementation plan

Date: 2026-11-18
Spec: `docs/superpowers/specs/2026-11-18-tenant-to-school-design.md`
Execution authorized by the user's explicit `@dispatching-parallel-agents ... Please execute the following specification` instruction.

## Coordination rules (binding for every worker)

1. **File ownership is exclusive.** A file listed under one task may only be edited by that task's worker. Never touch a file owned by another task, even to "help". If you discover a needed change in another task's file, report it in your final summary instead of editing it.
2. **Use the canonical rename map below verbatim.** Do not invent alternate names. Where the map says "deleted", remove the symbol/field entirely.
3. **Do not run the full test suite or a full build.** Other workers are editing the same repo concurrently. Run only cheap, self-contained checks inside your own files (e.g. `gofmt -l`, `tsc` on a scratch basis is NOT allowed). The coordinator runs all verification at the end.
4. **Preserve product-brand "Kidversa"** (DB name, user, OpenWA session, module path, page title, WA templates). Rename only the tenant concept.
5. **`children.school` is a different field** and keeps its wire key `school` and DB column `school`. Only the school entity's name becomes `school_name`.
6. **Old migrations `0001`–`0003` are immutable.** All schema change is in new `0004`.
7. Report a concise summary: files created/renamed/deleted, symbols renamed, any cross-task issue found.

## Canonical rename map (authoritative)

### Wire / config strings

| Old | New |
|-----|-----|
| `X-Tenant-ID` | `X-School-ID` |
| `kv-tenant` | `kv-school` |
| `TENANT_INACTIVE` / `CodeTenantInactive` | `SCHOOL_INACTIVE` / `CodeSchoolInactive` |
| `TENANT_MISMATCH` / `CodeTenantMismatch` | `SCHOOL_MISMATCH` / `CodeSchoolMismatch` |
| `MsgTenantInactive` (`tenant disabled`) | `MsgSchoolInactive` (`school disabled`) |
| `MsgTenantMismatch` (`tenant changed, reloading`) | `MsgSchoolMismatch` (`school changed, reloading`) |
| `kv.tenant` (Locals key value) | `kv.school` |
| `"tenant.changed"` (SSE) | `"school.changed"` |
| JWT/frame JSON key `tenant_id` | `school_id` |
| JWT claim key `tenant` | `school` |
| envelope key `{"tenant": …}` | `{"school": …}` |
| users-list join column `tenant_name` | `school_name` |
| `/api/tenants` | `/api/schools` |
| `/api/auth/tenants` | `/api/auth/schools` |
| `/api/auth/switch-tenant` | `/api/auth/switch-school` |
| `/api/dashboard/tenant-summary` | `/api/dashboard/school-summary` |
| `data.tenants` (dashboard) | `data.schools` |
| `?tenant=` dashboard query | `?school=` |

### Database

- Table `tenants` → `schools`.
- `schools.name` → `schools.school_name` (`VARCHAR(200) NOT NULL`).
- `schools.type` → **deleted**.
- New `schools.country VARCHAR(100) NOT NULL DEFAULT 'Indonesia'` after `school_name`.
- New `schools.city VARCHAR(100) NULL` after `country`.
- `uq_tenants_name` → dropped; add `idx_schools_school_name`.
- `tenant_id` → `school_id` on all 24 tables; rename every index whose name contains `tenant` to the `school` equivalent (e.g. `idx_refresh_tenant` → `idx_refresh_school`, `uq_idem_tenant_route_key` → `uq_idem_school_route_key`, `idx_children_tenant_active` → `idx_children_school_active`, `idx_users_scope` → `idx_users_school_scope`). Preserve each column's NULL/NOT NULL and each index's column order.
- Existing rows: `UPDATE schools SET school_name = 'Kidversa'` (country default / city NULL already correct).
- Seed SQL files are single-statement, idempotent, discovered dynamically.

### Go identifiers

| Old | New |
|-----|-----|
| `tenantItem` | `schoolItem` — fields `ID, SchoolName, Country, City, IsActive, CreatedAt` |
| `tenantReq` | `schoolReq` — `SchoolName`, `Country`, `City` |
| `tenantJSON` | `schoolJSON` — `SchoolName`, `Country`, `City` |
| `tenantBrief` | `schoolBrief` — `ID`, `SchoolName` (type field deleted) |
| `validTenantType` | **deleted** |
| `requireTenant` | `requireSchool` |
| `tenantWipeTables` | `schoolWipeTables` |
| `tenantItemByID` | `schoolItemByID` |
| `tenantActive` | `schoolActive` |
| `tenantByID` | `schoolByID` |
| `latestActiveTenant` | `latestActiveSchool` |
| `tenantForSession` | `schoolForSession` |
| `tenantPubFor` | `schoolPubFor` |
| `switchTenantReq` | `switchSchoolReq` |
| `handleSwitchTenant` | `handleSwitchSchool` |
| `handleAuthTenants` | `handleAuthSchools` |
| `RegisterTenants` | `RegisterSchools` |
| `listTenants`, `createTenant`, `getTenant`, `patchTenant`, `deleteTenant`, `tenantActivate`, `tenantDeactivate`, `setTenantActive` | `listSchools`, `createSchool`, `getSchool`, `patchSchool`, `deleteSchool`, `schoolActivate`, `schoolDeactivate`, `setSchoolActive` |
| `userRow.TenantID` / `.TenantActive` | `.SchoolID` / `.SchoolActive` |
| `userJSON.TenantID` (`json:"tenant_id"`) | `.SchoolID` (`json:"school_id"`) |
| `accessClaims.Tenant` / `refreshClaims.Tenant` (`json:"tenant"`) | `.School` (`json:"school"`) |
| `Frame.TenantID` (`json:"tenant_id"`) | `.SchoolID` (`json:"school_id"`) |
| `ScopeSnapshot.TenantID` | `.SchoolID` |
| `ScopeProvider` param `tenantID` | `schoolID` |
| `maxStreamsPerTenant` | `maxStreamsPerSchool` |
| `sub.tenant` (private field) | `sub.school` |
| all other `tenantID`/`tenant` locals, slog keys, comments | `schoolID`/`school` |
| `tenants.go` file | `schools.go` |
| `tenants_test.go`, `tenant_test.go` | `schools_test.go`, `school_test.go` |
| `seedTenantName`, `seedTenantType`, `seedSAName`, `seedSAEmail`, `seedSAPassword`, `insertSeedSA`, `updateSeedSA`, `hashSeedSAPassword` | **deleted** (SQL seeds replace them) |
| `seed.go` | small SQL-file reader: list `seeds/*.sql`, sort ascending, `ExecContext` each verbatim under a 10 s context, log generic "seed applied". |

School create/patch validation: `school_name` trim, non-empty, `len ≤ maxShortTextLen` (find the real constant; it is 200 — reuses the existing cap used by `children.school`, previously the tenant code used `maxPersonNameLen = 160`); `country` trim, empty → `"Indonesia"`, `len ≤ 100`; `city` trim, empty → `NULL`, `len ≤ 100`. All optional on input. Remove the duplicate-name `VALIDATION` branches.

Child create: `school` becomes optional; when omitted/blank, call `latestActiveSchool(ctx)` and use its `school_name`; if none, return `VALIDATION`. Preserve the `school` JSON key.

### TypeScript identifiers

| Old | New |
|-----|-----|
| `Tenant` interface | `School` — `{ id, school_name, country, city, is_active }` |
| `TenantType` | **deleted** |
| `AuthContextValue.tenant` / `switchTenant` | `.school` / `switchSchool` |
| `setSessionTenant` | `setSessionSchool` |
| `healTenantHeader` | `healSchoolHeader` |
| `sessionTenantId` | `sessionSchoolId` |
| `pages/tenants/` dir | `pages/schools/` |
| `TenantsPage` / `TenantModal` / `tenantsRoutes` | `SchoolsPage` / `SchoolModal` / `schoolsRoutes` |
| `TenantSummaryTable` | `SchoolSummaryTable` |
| `kv-rail__tenant` / `kv-rail-tenant` CSS | `kv-rail__school` / `kv-rail-school` |
| i18n `tenants.*` keys, `tenants` menu key | `schools.*`, `schools` |
| i18n `typeOptions`, `nameTaken` keys | **deleted** |
| i18n entity `tenant` (feedback) | `school` |
| new i18n keys | `schools.schoolName`, `schools.country`, `schools.city` |
| child field label | "Child's school" / "Sekolah anak" |

## Task slices and ownership

All six tasks are independent in file ownership and may run in parallel.

### Task A — Database & seeding
Owns (exclusive):
- `backend/migrations/0004_school_rename.sql` (new)
- `backend/seeds/0001_school.sql` (new)
- `backend/seeds/0002_sa.sql` (new)
- `backend/internal/seed.go` (rewrite as SQL reader)
- `backend/internal/db_test.go` (seed tests rewritten: one `ExecContext` per seed file; migration-runner fixtures for 0001 stay)
- `backend/Dockerfile` (add `COPY seeds ./seeds`)

Generate the SA bcrypt hash once with a throwaway Go program using `golang.org/x/crypto/bcrypt` for password `@Kidversafun`, then delete the throwaway program. Check the hash string into `0002_sa.sql`. Do not leave temp files in the repo.

### Task B — Backend semantic core
Owns (exclusive):
- `backend/internal/envelope.go`
- `backend/internal/schools.go` (new; delete `backend/internal/tenants.go`)
- `backend/internal/auth.go`
- `backend/internal/users.go`
- `backend/internal/children.go`
- `backend/internal/catalog_test.go`
- `backend/internal/wire_pins_test.go`
- `backend/internal/school_test.go` (new; delete `backend/internal/tenant_test.go`)
- `backend/internal/schools_test.go` (new; delete `backend/internal/tenants_test.go`)
- `backend/internal/auth_test.go`, `auth_extra_test.go`, `auth_photo_test.go`
- `backend/internal/children_test.go`
- `backend/internal/users_test.go`
- `backend/internal/envelope_test.go`
- `backend/cmd/api/main.go`

Note: `main.go` owns all route-registration renames (e.g. `RegisterTenants` → `RegisterSchools`) and the CORS `AllowHeaders` change to `X-School-ID`.

### Task C — Backend mechanical
Owns (exclusive): every backend `internal/*.go` and `internal/integration/*.go` file **not** owned by Task A or B, including but not limited to:
`activities.go`, `admin_controls.go`, `assessments.go`, `config.go`, `consents.go`, `dashboard.go`, `files.go`, `frames.go`, `idempotency.go`, `image.go`, `mail.go`, `missions.go`, `photos.go`, `programs.go`, `pscope.go`, `public.go`, `reports.go`, `sessions.go`, `stream.go`, `topics.go`, `validate.go`, `wa.go`, plus all their `*_test.go`, plus `internal/integration/*.go`.

Do not edit `main.go` (owned by Task B) — report any needed main.go changes instead. Do not edit `migrations/` or `seeds/` (owned by Task A).

Apply all SQL column renames (`tenant_id` → `school_id`), SSE entity renames (`tenant.changed` → `school.changed`), Frame field renames, local variable / slog key renames, and update every `sqlmock` `regexp.QuoteMeta` fragment and exact JSON-key pin in lockstep.

### Task D — Frontend lib & infrastructure
Owns (exclusive):
- `frontend/src/lib/auth.tsx`, `auth.test.tsx`
- `frontend/src/lib/api.ts`, `api.test.ts`
- `frontend/src/lib/stream.ts`, `stream.test.ts`, `stream.parity.test.ts`
- `frontend/src/lib/feedback.ts`, `errors.ts`, `errors.parity.test.ts`
- `frontend/src/lib/i18n.ts`, `i18n.test.ts`, `i18n.parity.test.ts`
- `frontend/src/test/catalogues.ts`, `handlers.ts`, `render.tsx`, `setup.ts`
- `frontend/src/router.tsx`
- `frontend/src/locales/en/auth.json`, `common.json`, `menu.json`, `session.json`
- `frontend/src/locales/id/auth.json`, `common.json`, `menu.json`, `session.json`

Keep id/en locale key sets identical; catalogue sizes stay 33 error codes / 30 SSE events.

### Task E — Frontend pages & styles
Owns (exclusive):
- `frontend/src/pages/tenants/` → `frontend/src/pages/schools/` (all files, including `routes.tsx`, `TenantsPage.tsx`, `TenantModal.tsx`, their tests)
- `frontend/src/pages/dashboard/TenantSummaryTable.tsx` → `SchoolSummaryTable.tsx` (+ tests; no Country/City columns)
- `frontend/src/pages/dashboard/DashboardPage.tsx`, `dashboard.css`, `dashboard` tests
- `frontend/src/components/AppLayout.tsx` + all its tests
- `frontend/src/components/Breadcrumbs.tsx` + test
- `frontend/src/components/ConfirmDialog.tsx` + test, `StreamGate.tsx` + test, `WaStatusBadge.tsx` + test
- `frontend/src/lib/crumbs.tsx`, `session-contracts.ts`
- `frontend/src/index.css`
- `frontend/src/pages/children/` (prefill `school` from `useAuth().school?.school_name`; editable)
- `frontend/src/pages/users/`, `pages/auth/`, `pages/sessions/`, `pages/profile/`, `pages/masters/`, `pages/missions/`, `pages/view/`, `pages/routes-inventory.test.ts`

Do not edit `frontend/src/lib/{auth,api,stream,feedback,errors,i18n}.ts*` or `frontend/src/test/*` or `frontend/src/locales/*` (Task D). Use the new names from the map; report any cross-task mismatch.

### Task F — Docs & ops config
Owns (exclusive):
- `AGENTS.md`
- `docs/spec/**` (all files mentioning tenant concept)
- `docs/audit/**`
- `docs/superpowers/specs/2026-11-11-admin-impersonation-password-reset-design.md`
- `ops/**` (if tenant references exist)
- `compose.yml` and `frontend/nginx.conf` — verify only; no edit expected

Leave `docs/superpowers/specs/2026-11-18-tenant-to-school-design.md` and this plan unchanged: they are the historical record of the rename and intentionally mention "tenant".

## Sequencing

1. Dispatch all six tasks in parallel (disjoint file sets).
2. On return, coordinator reviews summaries, greps for leftover `tenant`/`Tenant`/`TENANT` in source (excluding the two historical spec docs, coverage artifacts, `dist/`, product-brand Kidversa references, and `children.school` prose that legitimately says "school"), and reconciles cross-task findings.
3. Coordinator runs verification:
   - `cd backend && go build ./...`
   - `cd backend && go test ./internal/... -v -cover`
   - `cd backend && go test ./internal/integration/... -v -tags=integration`
   - `cd frontend && npm run build`
   - `cd frontend && npm run lint`
   - `cd frontend && npx vitest run`
   - `cd frontend && npx vitest run --config vitest.integration.config.ts`
4. Regenerate coverage artifacts (`backend/cover`, frontend coverage dir) from the passing runs; do not hand-edit.
5. Report results; do not commit unless asked.

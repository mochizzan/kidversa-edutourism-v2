# Repository Guidelines

## Project Overview

**Kidversa EduTourism v2** is an educational tourism platform for children's learning sessions/tours. It manages session lifecycles (Draft → Opened → Finished), child progress tracking, AI-generated narratives (Gemini), WhatsApp notifications (OpenWA), and multi-school data isolation.

**Status**: Implementation complete (verified 2026-10-10; full rename to the School model applied 2026-11-18 — see `docs/superpowers/specs/2026-11-18-tenant-to-school-design.md`). The 28 locked user decisions in `docs/spec/` are implemented end-to-end: Go+Fiber backend (vertical slices, migrations, unit+integration tests), React SPA frontend (pages, i18n, tests), and Docker deployment (`compose.yml`, Dockerfiles, nginx). Wire-shape contracts binding all layers: `docs/superskill/task/2026-10-09-contracts-shapes.md`.

---

## Architecture & Data Flow

### High-Level Structure

```
┌─────────────────────────────────────────────────────────┐
│  Browser (React SPA)                                    │
│  React 19 + Ant Design + i18next + react-router v7     │
└──────────────────────┬──────────────────────────────────┘
                       │ same-origin /api/* via nginx proxy
┌──────────────────────▼──────────────────────────────────┐
│  Go Fiber v3 API (backend/)                             │
│  Vertical slices: internal/<domain>.go                  │
│  Middleware: recover → requestid → logger → cors →     │
│  limiter → keyauth → RequireRole → school guard         │
└──────────────────────┬──────────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────────┐
│  MariaDB 12.3 LTS (zero FK constraints, app-enforced)   │
│  school_id on every row                                 │
└─────────────────────────────────────────────────────────┘
```

### Key Modules

| Module | Location | Purpose |
|--------|----------|---------|
| API Entry | `backend/cmd/api/main.go` | Fiber app setup, middleware chain, route mounting |
| Domain Logic | `backend/internal/<domain>.go` | Vertical slices: routes + handlers + SQL per domain |
| Migrations | `backend/migrations/NNNN_*.sql` | Versioned schema changes with MySQL advisory lock (`0001`–`0003` immutable; `0004_school_rename.sql` creates the `schools` table / `school_id` columns) |
| Seeds | `backend/seeds/NNNN_*.sql` | Single-statement idempotent SQL seeds (`0001_school.sql`, `0002_sa.sql`) applied on boot |
| Frontend App | `frontend/src/` | React SPA with pages, components, context |
| API Client | `frontend/src/lib/api.ts` | Envelope-based fetch wrapper with token refresh |
| Auth Context | `frontend/src/lib/auth.tsx` | React Context for user/school state |

### Data Flow

1. **API Responses**: Every `/api/*` and `/public/*` endpoint returns HTTP 200 for all business outcomes — `{data}` for success, `{error:{code,message}}` for business failures. HTTP 500 only for unhandled server faults.
2. **Authentication**: JWT access token (15 min, memory-only) + refresh token (7 days, HttpOnly cookie, DB whitelist with jti rotation). Headers: `Authorization: Bearer <access>`, `X-School-ID` (always), `X-Idempotency-Key` (on mutating POSTs).
3. **Realtime**: SSE (Server-Sent Events) with signal-only frames `{entity, entity_id, rev}`. Clients refetch REST on signal.
4. **File Storage**: Photos on server disk at `/data/uploads/<school_id>/<session_id>/<stid>/<child_id>/<rand32hex>.<ext>`. DB stores only relative paths.
5. **Idempotency**: `X-Idempotency-Key` header on mutating POSTs. Server stores `(school, key, route, result)` for 24h. Replay returns original result.

### External Services

| Service | Purpose |
|---------|---------|
| Google Gemini | AI narrative generation (gemini-3.5-flash) |
| OpenWA | Self-hosted WhatsApp integration (text-only) |
| SMTP | Password reset emails |
| MariaDB 12.3 | Primary database |

---

## Key Directories

```
kidversa-edutourism-v2/
├── backend/
│   ├── cmd/api/          # main.go entry point (route wiring, middleware chain)
│   ├── internal/         # Domain logic — vertical slices + co-located unit tests
│   ├── internal/integration/ # Tag-gated cross-domain tests (-tags=integration)
│   ├── migrations/       # NNNN_*.sql versioned migrations (advisory-lock runner; 0004 = schools rename)
│   ├── seeds/            # NNNN_*.sql idempotent boot seeds (school + SA)
│   ├── prompts/          # AI prompt templates ({{Var}} rendering)
│   ├── templates/        # WhatsApp message templates
│   ├── uploads/          # Runtime file storage (local dev)
│   └── go.mod            # Go module definition
├── frontend/
│   ├── src/
│   │   ├── components/   # Reusable UI components (AppLayout, StreamGate, …)
│   │   ├── pages/        # Route-level page components (+ routes.tsx per group)
│   │   ├── lib/          # API client, auth context, stream, list/validation helpers
│   │   ├── report/       # A4 ReportView + export (PNG/PDF)
│   │   ├── locales/      # i18n translation files (id/en × 7 namespaces)
│   │   ├── test/         # MSW handlers, render helper, setup
│   │   ├── App.tsx       # Root component with router
│   │   ├── main.tsx      # Entry point
│   │   └── index.css     # Global styles
│   ├── vitest.config.ts / vitest.integration.config.ts
│   ├── package.json
│   ├── vite.config.ts
│   ├── tsconfig.json
│   └── .oxlintrc.json
├── compose.yml           # 4 services: db, api, web (nginx), wa (openwa)
├── docs/spec/            # 11 specification documents (incl. DESIGN.md patterns)
├── docs/superskill/task/ # Execution task file + binding wire-shape contracts
├── data/                 # Runtime data dirs (openwa/, uploads/, mariadb/)
└── AGENTS.md             # This file
```

---

## Development Commands

### Frontend

```bash
cd frontend
npm install              # Install dependencies
npm run dev              # Start Vite dev server with HMR
npm run build            # Type-check (tsc -b) + production build
npm run lint             # Run Oxlint
npm run preview          # Preview production build
```

### Backend

```bash
cd backend
go mod download          # Download Go dependencies
go build ./...           # Build all packages
go test ./internal/... -v -cover           # Unit tests
go test ./internal/integration/... -v -tags=integration  # Integration tests
```

### Testing (when tests exist)

```bash
# Frontend
npx vitest run                              # Unit tests
npx vitest run --config vitest.integration.config.ts  # Integration tests
npx vitest run --coverage                   # With coverage

# Backend
go test ./internal/... -v -cover           # Unit tests
go test ./internal/integration/... -v -tags=integration  # Integration tests
```

---

## Code Conventions & Common Patterns

### Frontend

- **Naming**: camelCase for functions/variables, PascalCase for components, kebab-case for CSS classes
- **Styling**: CSS with CSS custom properties (variables), BEM-ish class naming (`.hero`, `.counter`, `.button-icon`)
- **State Management**: React Context + useState + useCallback + useMemo (no Zustand/Redux)
- **API Client Pattern** (`lib/api.ts`):
  - Relative `/api` + `/public` paths
  - Bearer token from memory
  - Envelope branch: `{data}` vs `{error:{code}}`
  - Refresh-once on `INVALID_TOKEN`
- **Auth Pattern** (`lib/auth.tsx`):
  - Context `{user, school}`
  - Access token memory-only
  - School in localStorage `'kv-school'`

### Backend

- **Naming**: camelCase for unexported, PascalCase for exported, ALL_CAPS for env vars
- **Structure**: Package per domain, vertical slices, no service/repository/dto layers
- **Error Handling**: Custom codes E1-E33 + EX taxonomy (VALIDATION, INVALID_CREDENTIALS, TOKEN_INVALID, etc.)
- **No HTTP Errors**: No 401/403/404/409/422/429 — all are HTTP 200 envelopes
- **Middleware Pipeline Order**:
  1. recover
  2. requestid
  3. logger
  4. cors
  5. limiter (sensitive routes only)
  6. keyauth (Bearer token from auth header, refresh from cookie)
  7. RequireRole (SA passes all, ADMIN passes facilitator-level)
  8. school guard (X-School-ID vs JWT claim)
  9. `c.Bind()` input parsing
  10. validate.go
  11. store (database operations with context timeout)
  12. `c.Status(200).JSON` envelope response

### API Response Envelope

```json
// Success
{ "data": { ... } }

// Business failure
{ "error": { "code": "VALIDATION", "message": "..." } }
```

---

## Important Files

| File | Purpose |
|------|---------|
| `frontend/package.json` | Frontend dependencies and scripts |
| `frontend/vite.config.ts` | Vite bundler configuration |
| `frontend/tsconfig.app.json` | App TypeScript config (strict, ES2023, bundler resolution) |
| `frontend/.oxlintrc.json` | Oxlint linter config (react, typescript, oxc plugins) |
| `backend/go.mod` | Go module definition (Go 1.26.6, Fiber v3, JWT, MySQL) |
| `docs/spec/00-SCOPE.md` | Project scope and requirements |
| `docs/spec/01-TECHSTACK.md` | Technology stack decisions |
| `docs/spec/02-ARCHITECTURE.md` | Architecture and deployment design |
| `docs/spec/03-FEATURES.md` | Feature specifications |
| `docs/spec/04-FLOWS.md` | User flows and journeys |
|`docs/spec/05-DATA.md`|Data model and schema|
|`docs/spec/06-ERRORS-AND-EDGE-CASES.md`|Error handling specifications|
|`docs/spec/08-ACCEPTANCE-CRITERIA.md`|Acceptance criteria and test strategy|
|`docs/superskill/task/2026-10-09-contracts-shapes.md`|BINDING wire-shape contracts (Go↔TS JSON shapes, SSE event catalogue, report module signatures)|

---

## Runtime/Tooling Preferences

### Required Runtimes

| Runtime | Version | Used By |
|---------|---------|---------|
| Go | 1.26.6+ | Backend |
| Node.js | 20.19+ / 22.12+ | Frontend |
| MariaDB | 12.3 LTS | Database |

### Package Manager

- **Frontend**: npm
- **Backend**: Go modules

### Tooling

| Tool | Purpose |
|------|---------|
| Vite 8.3 | Frontend build tool / dev server |
| TypeScript 6.0 | Type checking |
| Oxlint 1.81 | Linting (replaces ESLint) |
| Vitest 5.0 | Test runner |
| @testing-library/react | Component testing |
| MSW 2.15 | API mocking in tests |
| @vitest/coverage-v8 | Coverage reporting |
| testify 1.12 | Go testing assertions |
| go-sqlmock 1.5 | Go SQL mocking |
| ory/dockertest | Go integration test containers |

### Deployment

Documented in `docs/spec/02-ARCHITECTURE.md`: 4 Docker services (db=mariadb:12.3, api=Go+Fiber v3, web=nginx:alpine, wa=openwa-openwa:latest). Single `compose.yml` for local and prod. Bindmount persistence via `./data/`. No Dockerfiles or compose.yml exist yet.

---

## Testing & QA

### Test Frameworks

| Layer | Framework | Status |
|-------|-----------|--------|
| Frontend Unit | Vitest 5 + @testing-library/react (jsdom) | Implemented (`src/**/*.test.tsx`) |
| Frontend Integration | Vitest + MSW (`*.integration.test.tsx`) | Implemented |
| Backend Unit | Go testing + testify + sqlmock | Implemented (`internal/*_test.go`) |
| Backend Integration | Go + sqlmock + fiber app.Test (`-tags=integration`, runs without Docker) | Implemented |
| E2E | None | Not configured (out of scope per spec) |

### Test Patterns

- **Frontend**: `src/**/*.test.tsx` (co-located), `src/test/{setup.ts,handlers.ts,render.tsx}` (MSW + render helper), `.integration.test.tsx` for integration; configs `vitest.config.ts` / `vitest.integration.config.ts` (`environment: 'jsdom'`)
- **Backend**: `internal/<domain>_test.go` (co-located unit), `internal/integration/*_test.go` (build tag `integration`; `internal/integration/doc.go` keeps `./...` buildable without the tag)

### Coverage Expectations

- **Frontend**: >=90% components, 100% critical paths
- **Backend**: >=90% business logic, 100% critical paths

### Test Commands

```bash
# Frontend
npx vitest run                              # Unit tests
npx vitest run --config vitest.integration.config.ts  # Integration tests
npx vitest run --coverage                   # With coverage

# Backend
go test ./internal/... -v -cover           # Unit tests
go test ./internal/integration/... -v -tags=integration  # Integration tests
```

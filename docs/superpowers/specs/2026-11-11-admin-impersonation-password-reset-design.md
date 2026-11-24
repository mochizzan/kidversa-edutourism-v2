# Admin Impersonation + Administrative Password Reset — Design

- **Date:** 2026-11-11
- **Status:** Draft (awaiting approval)
- **Topic:** `SA`/`ADMIN` "Login As" impersonation and admin-forced password reset
- **Related:** `docs/spec/02-ARCHITECTURE.md`, `03-FEATURES.md`, `04-FLOWS.md`, `05-DATA.md`, `06-ERRORS-AND-EDGE-CASES.md`, `08-ACCEPTANCE-CRITERIA.md`

---

## 1. Intent and why

Support staff currently have no way to reproduce a facilitator's problem
("I can't see my group") or to help a user who is locked out. The two gaps:

1. **No impersonation.** Reproducing a role-scoped bug means asking the user
   for a screen recording or creating a throwaway account.
2. **No admin-forced password reset.** `POST /api/auth/reset` requires the
   user to receive an emailed code. When SMTP is down (`MAIL_FAILED`), or the
   user has lost mailbox access, an admin has no recovery path that does not
   involve direct SQL.

This document specifies both features end-to-end: authorization matrix, API
contract, session-transition data flow, validation schema, error mapping, and
the audit trail — while staying inside the project's binding conventions
(HTTP-200 envelope, zero FK constraints, vertical slices, no service layer).

---

## 2. Locked decisions (user, 2026-11-11)

| # | Decision | Consequence |
|---|---|---|
| 1 | **Impersonation = hard switch, no return path.** The actor's current session is revoked and replaced by the target's. To go back, the admin signs in again. | No second session lifetime, no "return" token, no extra state machine. |
| 2 | **Audit persistence reuses/extends existing tables.** No new audit table. | `refresh_tokens` becomes the session ledger; `reset_tokens` becomes the password-change ledger. Janitor queries must be adjusted (see §7). |
| 3 | **Transport stays the 200-envelope.** No 201/204/403/404/409. | Every failure below is `HTTP 200 {error:{code,message}}`. Only unhandled faults are `HTTP 500 INTERNAL`. |
| 4 | **No step-up re-authentication.** Role + active session is sufficient. | No `current_password` in either request body. |

---

## 3. Goals

1. `SA` can impersonate any `ADMIN` or `FASILITATOR` in any active school.
2. `ADMIN` can impersonate `FASILITATOR` users **of its own school only**.
3. Impersonation is impossible for `FASILITATOR` at both API and UI level.
4. The acting session is revoked and the target's session is established in
   one atomic step; the browser ends up holding only the target's credentials.
5. `SA`/`ADMIN` can reset another user's password either by generating a
   secure random one (shown once) or by typing one that passes the existing
   password policy.
6. A password reset immediately revokes every refresh session of the target.
7. Both actions leave a durable, queryable record of who did what to whom.

## 4. Non-goals

- No "return to admin" / session-stacking (decision 1).
- No impersonation of `SA` by anyone, including another `SA`.
- No self-impersonation or self-service reset through these routes.
- No new SSE event; no `user.changed` publish for password resets.
- No email/WhatsApp notification to the target when an admin resets their
  password (the existing forgot/reset mail flow is untouched).
- No password history, rotation policy, or "must change at next login" flag.
- No UI for browsing the audit ledger in this iteration.

## 5. Anti-goals (would make this a failure)

- An `ADMIN` reaching a user outside its school, or any `ADMIN`/`SA` target.
- A `FASILITATOR` being able to call either endpoint successfully.
- The actor's refresh `jti` staying valid after the switch.
- A generated password being written to `idempotency_keys`, a log line, or
  any column in plaintext.
- A password reset that leaves the target's old sessions alive.
- `FORBIDDEN_ROLE` vs `NOT_FOUND` differing in a way that lets an `ADMIN`
  enumerate other schools' user ids.

---

## 6. Phase 1 findings — current state and risks

### 6.1 Authentication and session management (as built)

| Piece | Location | Behaviour |
|---|---|---|
| Access JWT | `internal/auth.go` `signAccess` / `parseAccess` | HS256, `JWT_ACCESS_TTL_MIN` (15 min), claims `sub`, `role`, `school`, `iat`, `exp`, 60 s leeway. **No `jti`** → not revocable. |
| Refresh JWT | `signRefresh` / `parseRefresh` | HS256, `JWT_REFRESH_TTL_DAYS` (7 d), `jti` required, signed with a separate secret. |
| Cookie | `setRefreshCookie` | `refresh_token`, `Path=/`, `HttpOnly`, `SameSite=Lax`, `Secure` only when `PUBLIC_WEB_BASE_URL` is https. No `__Host-` prefix. |
| Whitelist | `refresh_tokens` | Rotation = revoke old `jti` + insert new, in one Tx with `SELECT … FOR UPDATE`. |
| Guard chain | `authed()` → `authorize()` | `keyauth(Bearer)` → reload user row from DB → `decision`/`is_active` → `roleAllowed` → school guard. |
| Revocation today | `handleLogout`, `handleChangePassword`, `handleReset`, `setUserActive` | Refresh rows only. Access tokens survive up to 15 min. |
| SSE | `stream.go` | Handshake re-checks `refresh_tokens` for a live row; `auth.expiring` fires at T-60 s. |
| Client token | `frontend/src/lib/api.ts` | Access token is a module variable (memory only); `X-School-ID` read from `localStorage['kv-school']` on every call. |

### 6.2 RBAC (as built)

- Exactly three roles: `SA`, `ADMIN`, `FASILITATOR` (`users.role` ENUM).
- `roleAllowed(roles, role)`:
  - `SA` **short-circuits `true` for every guard**, regardless of the list.
  - `ADMIN` also satisfies a `FASILITATOR` entry.
  - an empty role list means "any authenticated user".
- Live consequence for this design:
  - `authed("ADMIN")` admits `SA` and `ADMIN` — correct for both endpoints.
  - `authed("FASILITATOR")` would admit `ADMIN` and `SA` — therefore
    **impersonation must never be expressed as a role list**; it needs the
    explicit `(actor role × target role)` predicate in §8.1.
- `loadUserForEdit()` already implements the scope semantics we want:
  - unknown id → `NOT_FOUND`
  - `ADMIN` + cross-school target → `NOT_FOUND`
  - `ADMIN` + non-`FASILITATOR` target → `FORBIDDEN_ROLE`
- `listUsers` pins `ADMIN` to `school_id = own AND role = 'FASILITATOR'` in SQL.

### 6.3 Password machinery (as built)

- Hashing: `bcrypt.DefaultCost` in `hashPassword` / `checkPassword`.
- Policy: `validPassword()` — 8–72 **bytes**, at least one letter **and** one
  digit. No symbol requirement, no blocklist.
- Input handling: no sanitizer middleware. Each handler calls `bindBody`
  (invalid JSON → `VALIDATION`) then a pure predicate; SQL is fully
  parameterized.
- Existing self-service reset: 8-hex-char code, SHA-256 stored, constant-time
  compare, single use, 15 min TTL, revokes all refresh rows.
- Passwords are never returned by any existing endpoint.

### 6.4 Security findings that shape this design

| # | Finding | Handling in this spec |
|---|---|---|
| S1 | **No durable audit trail.** Only `slog` lines and `reset_tokens.revealed_by`. | Decision 2: extend `refresh_tokens` + `reset_tokens`; adjust janitor retention (§7). |
| S2 | **Access tokens cannot be revoked.** | Accepted residual risk; documented in §11. Client discards it and the server revokes the refresh row. |
| S3 | **Cookie overwrite ≠ revocation.** Issuing the target cookie would leave the actor's old `jti` valid. | Explicit `UPDATE … revoked_at` inside the same Tx (§9). |
| S4 | **`roleAllowed` composition trap** (SA bypass, ADMIN⊇FASILITATOR). | Single `canImpersonate` / `canResetPassword` predicate; never a bare role list. |
| S5 | **`/api/users/*` has no rate limiting.** | Add `newLimiter(10, time.Minute, keyIP)` to all three new routes. |
| S6 | **Global unique email**; differing error shapes leak cross-school ids. | `ADMIN` cross-school target returns the same `NOT_FOUND` envelope as an unknown id. |
| S7 | **`idempotent()` persists the response body for 24 h.** | Password endpoints must never be idempotency-wrapped (plaintext would land in `idempotency_keys.response`). Impersonation is excluded too (one-shot credentials). |
| S8 | **`updateSeedSA()` rewrites the SA password on every boot.** | SA is excluded as a reset target (§8.2). Flagged for a separate fix (§13). |
| S9 | Janitor purges `reset_tokens WHERE used_at IS NOT NULL` and `refresh_tokens WHERE expires_at < NOW()`. | Both queries change so ledger rows survive (90-day retention, §7.3). |
| S10 | `users` has no `updated_at`; decisions/activations have no history. | Out of scope; the ledger tables carry `created_at`. |

---

## 7. Data model

### 7.1 Migration `0003_admin_controls.sql`

```sql
-- Impersonation ledger: the session row itself records the actor.
-- impersonation_root is the STABLE key for one impersonation session; it is
-- copied unchanged onto every rotated row, so a refresh does not fragment or
-- lose the record. It equals the actor's revoked jti when the caller had a
-- refresh cookie, otherwise a random 16-byte hex generated at switch time.
ALTER TABLE refresh_tokens
    ADD COLUMN impersonated_by    BIGINT UNSIGNED NULL AFTER user_id,
    ADD COLUMN impersonation_root VARCHAR(64)     NULL AFTER impersonated_by,
    ADD KEY idx_refresh_impersonated (impersonated_by, created_at),
    ADD KEY idx_refresh_imp_root      (impersonation_root);

-- Password-change ledger: distinguish self-service codes from admin resets.
ALTER TABLE reset_tokens
    ADD COLUMN source     ENUM('SELF','ADMIN') NOT NULL DEFAULT 'SELF' AFTER user_id,
    ADD COLUMN created_by BIGINT UNSIGNED      NULL AFTER source,
    ADD KEY idx_reset_source (source, created_at);
```

No FK constraints (project rule). All references are loose `BIGINT UNSIGNED`,
app-enforced.

### 7.2 What each row means

**Impersonation** — `refresh_tokens` is the session ledger. The grain is
**one impersonation session**, not one row: normal rotation creates a new row
every refresh, so `handleRefresh` must copy `impersonated_by` and
`impersonation_root` forward. Grouping key = `impersonation_root`.

| Column | Value |
|---|---|
| `user_id` | the **target** |
| `school_id` | the target's school |
| `jti` | the session id of this rotation |
| `impersonated_by` | the **actor** (`users.id`), copied on every rotation |
| `impersonation_root` | stable id for the session (actor's revoked `jti`, or a random hex when the caller had no cookie), copied on every rotation |
| `created_at` | this rotation's issue time (first row = session start) |
| `revoked_at` | logout / rotation / deactivation; equal to `expires_at` after natural expiry (janitor stamp) |

Queries:

```sql
-- one row per impersonation SESSION
SELECT impersonation_root, user_id, impersonated_by,
       MIN(created_at) AS started_at,
       MAX(COALESCE(revoked_at, expires_at)) AS ended_at,
       COUNT(*) AS rotations
  FROM refresh_tokens
 WHERE impersonated_by IS NOT NULL
 GROUP BY impersonation_root, user_id, impersonated_by
 ORDER BY started_at DESC;
```

The read joins `users` with a **LEFT JOIN** and renders
`COALESCE(u.name, '[deleted #' || id || ']')` (see §7.3 for deletion
semantics).

**Admin password reset** — one completed `reset_tokens` row:

| Column | Value |
|---|---|
| `user_id` | the **target** |
| `school_id` | the target's school (NULL for `SA`, which is never a target) |
| `source` | `'ADMIN'` |
| `created_by` | the **actor** |
| `code_hash` | `sha256Hex(randHex())` — an unusable nonce, satisfies `NOT NULL` |
| `code_plain` | `NULL` — never stored |
| `expires_at` | `NOW()` |
| `used_at` | `NOW()` — marks it as already consumed |

Because `used_at IS NOT NULL`, the row never appears in the reset dashboard.

### 7.3 Janitor and query changes (required, not optional)

The existing janitor would delete both ledgers almost immediately. All four
touch-points change:

| File | Current | New |
|---|---|---|
| `files.go` `sweep()` | `DELETE FROM refresh_tokens WHERE expires_at < NOW()` | `UPDATE refresh_tokens SET revoked_at = expires_at WHERE impersonated_by IS NOT NULL AND revoked_at IS NULL AND expires_at < NOW()` **then** `DELETE FROM refresh_tokens WHERE (impersonated_by IS NULL AND expires_at < NOW()) OR created_at < NOW() - INTERVAL 90 DAY` |
| `files.go` `sweepResetTokens` select | `… WHERE expires_at < NOW() OR used_at IS NOT NULL` | `… WHERE source = 'SELF' AND (expires_at < NOW() OR used_at IS NOT NULL)` |
| `files.go` `sweepResetTokens` delete | same predicate | same predicate **+** `DELETE FROM reset_tokens WHERE source='ADMIN' AND created_at < NOW() - INTERVAL 90 DAY` |
| `auth.go` `handleForgot` sweep | `DELETE FROM reset_tokens WHERE user_id = ? AND expires_at < NOW()` | add `AND source = 'SELF'` |
| `auth.go` `handleReset` candidate read | last 20 rows by `user_id` | add `AND source = 'SELF'` |
| `auth.go` `handleResetList` / `resetSelect` | no source filter | add `AND rt.source = 'SELF'` |
| `users.go` `deleteUser` | `DELETE FROM refresh_tokens WHERE user_id = ?` / `DELETE FROM reset_tokens WHERE user_id = ?` | delete only non-ledger rows: `AND impersonated_by IS NULL` / `AND source = 'SELF'` |

**Natural expiry.** Without the `UPDATE` above, an impersonated session that
simply expires would keep `revoked_at IS NULL` forever and the ledger could not
tell "ended" from "stale". Stamping `revoked_at = expires_at` makes
`ended_at` well-defined for every session.

**Retention:** 90 days, matching the existing `wa_outbox` retention.

**Deletion semantics.** Ledger rows are immutable: deleting a user (actor or
target) no longer erases them, so reads must `LEFT JOIN users` and fall back to
`[deleted #<id>]`. This deliberately deviates from the project's
hard-delete/no-trace stance for ordinary rows (design call C10) — an
administrative-abuse record must not be erasable by deleting the account that
produced it. Ordinary refresh/reset rows keep the existing purge-on-delete
behaviour.

---

## 8. Authorization

### 8.1 Impersonation predicate

```
canImpersonate(actor, target) -> (allowed, code)
  target.ID == actor.ID                 -> VALIDATION          (self)
  target missing                        -> NOT_FOUND
  actor.Role == "FASILITATOR"           -> FORBIDDEN_ROLE
  actor.Role == "ADMIN" && target.SchoolID != actor.SchoolID -> NOT_FOUND
  actor.Role == "ADMIN" && target.Role != "FASILITATOR"      -> FORBIDDEN_ROLE
  actor.Role == "SA"    && target.Role == "SA"               -> FORBIDDEN_ROLE
  target.Decision != "APPROVED" || !target.IsActive          -> ACCOUNT_INACTIVE
  target.Role != "SA" && !target.SchoolActive                -> SCHOOL_INACTIVE
  otherwise                                                  -> allowed
```

Resulting matrix:

| Actor ↓ / Target → | `SA` | `ADMIN` | `FASILITATOR` |
|---|---|---|---|
| `SA` | ❌ `FORBIDDEN_ROLE` | ✅ any school | ✅ any school |
| `ADMIN` | ❌ `FORBIDDEN_ROLE` | ❌ `FORBIDDEN_ROLE` | ✅ own school only; foreign → `NOT_FOUND` |
| `FASILITATOR` | ❌ `FORBIDDEN_ROLE` | ❌ `FORBIDDEN_ROLE` | ❌ `FORBIDDEN_ROLE` |

`SA → SA` is blocked because it grants nothing and doubles the blast radius of
a stolen SA session. Marked as a **design call** (§13).

### 8.2 Password-reset predicate

```
canResetPassword(actor, target) -> (allowed, code)
  target.ID == actor.ID                 -> VALIDATION          (use /auth/change-password)
  target missing                        -> NOT_FOUND
  actor.Role == "FASILITATOR"           -> FORBIDDEN_ROLE
  actor.Role == "ADMIN" && target.SchoolID != actor.SchoolID -> NOT_FOUND
  actor.Role == "ADMIN" && target.Role != "FASILITATOR"      -> FORBIDDEN_ROLE
  actor.Role == "SA"    && target.Role == "SA"               -> FORBIDDEN_ROLE
  target.Decision != "APPROVED" || !target.IsActive          -> ACCOUNT_INACTIVE
  target.Role != "SA" && !target.SchoolActive                -> SCHOOL_INACTIVE
  otherwise                                                  -> allowed
```

Identical to §8.1 by construction — one helper, two call sites, no drift.
`SA → SA` is additionally blocked because of finding **S8** (the seed would
silently revert it).

### 8.3 UI visibility (defence in depth, never the enforcement)

Row action `Login As` and `Reset Password` render only when the §8 predicates
are true for the row currently on screen:

- `me.role === 'SA'` → show for `row.role === 'ADMIN' || row.role === 'FASILITATOR'`
- `me.role === 'ADMIN'` → show for `row.role === 'FASILITATOR'` (the ADMIN list
  is already school-pinned and facilitator-only)
- never for `row.id === me.id`

`UsersPage` already has `me` and the row payload (`Role`, `SchoolID`), so no
extra fetch is needed.

---

## 9. Feature A — Impersonation

### 9.1 Endpoint

```
POST /api/users/:id/impersonate
```

| Aspect | Value |
|---|---|
| Guard | `s.authed("ADMIN")` (admits SA + ADMIN) |
| Limiter | `newLimiter(10, time.Minute, keyIP)` |
| Idempotency | **none** (S7: one-shot credential; replay would return an expired token) |
| Body | none |
| Headers | `Authorization: Bearer <actor access>`, `X-School-ID` (required for ADMIN), refresh cookie (optional) |

### 9.2 Success response (`HTTP 200`)

```json
{
  "data": {
    "access": "<target access JWT>",
    "user": {
      "id": 77, "name": "Siti", "email": "siti@school.id",
      "role": "FASILITATOR", "decision": "APPROVED", "is_active": true,
      "school_id": 4, "wa_number": "+6281234567890",
      "created_at": "2026-10-01T08:00:00Z"
    },
    "school": { "id": 4, "school_name": "TK Negeri 1", "country": "Indonesia", "city": null, "is_active": true },
    "impersonation": { "actor_id": 12, "actor_name": "Budi", "actor_role": "ADMIN" }
  }
}
```

Plus `Set-Cookie: refresh_token=<target jti>; Path=/; HttpOnly;
SameSite=Lax; Expires=<now+7d>[; Secure]`.

`access`/`user`/`school` are byte-compatible with the pinned `LoginData` shape
(`frontend/src/lib/auth.tsx`). `impersonation` is **additive and optional**.

**Banner contract.** The banner must never claim "impersonating" when the
session is not, and never hide it while it is.

- `POST /api/auth/refresh` returns `impersonation` whenever the row being
  rotated has `impersonated_by IS NOT NULL`, **and** `handleRefresh` copies
  `impersonated_by`/`impersonation_root` onto the new row. This is what makes
  the banner survive a hard reload: `AuthProvider`'s boot restore calls
  `/auth/refresh` and feeds the full payload to `setSession`.
- `GET /api/auth/me` is **best-effort**: it returns the block only when the
  request carries a valid refresh cookie whose `sub` equals the access token's
  `sub`. Bearer-only callers and multi-device users (several live rows) get no
  block rather than a guessed one. Nothing in the app depends on `/me` for the
  banner.
- `api.ts`'s silent `doRefresh` does **not** need to surface the block: within
  one live session the identity cannot change except through impersonation
  (which replaces the whole session) or logout. Surfacing it there would require
  widening the refresh contract for no behavioural gain.
- Deferred, documented limitation: a second tab that was open before the switch
  keeps its in-memory admin identity until its next refresh; the shared cookie
  has already been replaced, so its refresh lands on the target and the tab
  must reload. Multi-tab session coherence is a pre-existing gap, not part of
  this feature.

### 9.3 Failure responses (`HTTP 200`, `{error:{code,message}}`)

| Condition | Code |
|---|---|
| malformed/zero `:id` | `NOT_FOUND` |
| unknown target | `NOT_FOUND` |
| ADMIN targets a foreign-school user | `NOT_FOUND` |
| target is the actor | `VALIDATION` |
| ADMIN → ADMIN/SA, SA → SA | `FORBIDDEN_ROLE` |
| actor is FASILITATOR | `FORBIDDEN_ROLE` |
| target not `APPROVED` or inactive | `ACCOUNT_INACTIVE` |
| target's school inactive | `SCHOOL_INACTIVE` |
| ADMIN's `X-School-ID` ≠ own school | `SCHOOL_MISMATCH` |
| > 10 requests/min/IP | `RATE_LIMITED` + `Retry-After` |
| bad/expired Bearer | `INVALID_TOKEN` |
| DB fault | `DB_UNAVAILABLE` |

Evaluation order is exactly the table order (scope before state before write),
mirroring locked decision 68.

### 9.4 Data-flow sequence (session termination → transition)

```mermaid
sequenceDiagram
    autonumber
    participant B as Admin browser
    participant API as Go API
    participant DB as MariaDB

    B->>API: POST /api/users/77/impersonate<br/>Bearer(actor) · X-School-ID · cookie(J1)
    API->>API: authed("ADMIN") → authorize() loads actor row
    API->>DB: SELECT target user (+ school state)
    API->>API: canImpersonate(actor, target)
    API->>API: J1 := parseRefresh(cookie); require sub == actor.id
    API->>DB: BEGIN
    API->>DB: UPDATE refresh_tokens SET revoked_at=NOW()<br/>WHERE jti=J1 AND user_id=actor.id AND revoked_at IS NULL
    API->>DB: INSERT refresh_tokens(user_id=target, jti=J2,<br/>impersonated_by=actor.id, impersonation_root=J1, exp=now+7d)
    API->>DB: COMMIT
    API-->>B: 200 {access(target), user(target), school(target), impersonation}<br/>Set-Cookie: refresh_token=J2
    B->>B: setSession(data): replace memory token, kv-school, kv-session
    B->>B: StreamGate effect re-runs (user identity changed)<br/>→ close old SSE, open new
    B->>API: GET /api/stream with the target access token
    Note over B,API: Actor's old access token is discarded client-side<br/>but remains technically valid until exp (≤15 min, S2)
```

**Exit path (unchanged primitives):** `POST /api/auth/logout` revokes `J2`,
clears the cookie, the client clears memory + `kv-session` + `kv-school`, and
the login page renders. Re-authenticating as the admin starts a brand-new
session (`J3`).

### 9.5 Implementation notes

- The revoke is scoped to the caller's **current** `jti`, not every device.
  Cross-device revocation is out of scope for decision 1 ("the active
  session").
- `impersonation_root` is a random 16-byte hex when the caller presented no
  refresh cookie (pure Bearer client). The session transition still works;
  there is simply nothing to revoke in this browser.
- **Safety check:** never revoke `J1` unless `claims.Subject == actor.ID`.
  Otherwise a mismatched cookie (stale tab, another account) would log out an
  unrelated user.
- Reuse `respondSession`-style output but do **not** call `insertRefreshOn`
  blindly — it must accept and set `impersonated_by`/`impersonation_root`.
- **`handleRefresh` must propagate both columns** onto the rotated row
  (`insertRefreshOn` currently inserts only `school_id, user_id, jti,
  expires_at`). Without this the ledger fragments and the banner dies on the
  next reload. Extend the rotation `SELECT … FOR UPDATE` to also read
  `impersonated_by, impersonation_root`.
- The new access token is minted with the target's `role`/`school`; every
  subsequent request re-reads the user row anyway (`authorize`), so a
  deactivation takes effect immediately.

### 9.6 Frontend

1. `UsersPage` gains two row actions, gated by §8.3.
2. `Login As` opens a confirmation modal: target name, email, role, school, and
   a non-dismissable warning — *"You will be signed out of your account and
   signed in as {name}. You must sign in again to return."*
3. On confirm: `api.post('/api/users/{id}/impersonate')` → `setSession(data)`.
   The auto-generated `X-Idempotency-Key` is harmless (the route ignores it).
4. After `setSession`, navigate to the target's landing page
   (`FASILITATOR → /sessions`, else `/`). The existing `RequireRole` guard
   redirects out of `/users` for a facilitator anyway.
5. A persistent banner in `AppLayout` renders `impersonation.actor_name` and an
   **Exit** button that calls `logout()`.
6. **Race to guard:** `auth.tsx` keeps a module-level `restoreInFlight`. A boot
   restore still resolving after `setSession(target)` would overwrite the new
   identity with the old one. Add a monotonic `sessionEpoch` ref, incremented
   in `setSession`/`clearLocal`; a restore whose epoch is stale must not call
   `setSession`.
7. i18n keys land in `locales/{id,en}/auth.json` under `users.*`
   (`loginAs`, `loginAsTitle`, `loginAsWarning`, `impersonationBanner`,
   `exitImpersonation`, …).

---

## 10. Feature B — Administrative password reset

Two calls, mirroring the requested UX: generate → show → confirm → persist.
Neither call is idempotency-wrapped (S7).

### 10.1 Method 1 — generate

```
POST /api/users/:id/password/generate
```
Guard `authed("ADMIN")`, limiter `10/min/IP`, **no body**, **nothing written**.
Runs `canResetPassword` first, so a caller cannot obtain a password for a target
it could not reset.

Success (`HTTP 200`):

```json
{ "data": { "password": "K7mQp2xR9tVw4nHs" } }
```

**Generation policy** (design call, §13):

- 16 characters from an unambiguous alphabet:
  `ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789`
  (no `I`, `O`, `0`, `1`, `l`).
- Guaranteed to contain at least one letter and one digit, so the output always
  satisfies `validPassword()` by construction.
- Drawn from `crypto/rand` (`randBytes` in `envelope.go` helpers), rejection-
  sampled to avoid modulo bias.
- Never logged, never cached, never persisted.

### 10.2 Method 2 — apply (manual or the generated value)

```
POST /api/users/:id/password
```

Request body:

```json
{ "password": "ManualPass123" }
```

Success (`HTTP 200`):

```json
{ "data": { "reset": true } }
```

### 10.3 Apply — atomic writes

One transaction:

```sql
-- 1
UPDATE users SET password_hash = ? WHERE id = ?;
-- 2
UPDATE refresh_tokens
   SET revoked_at = NOW()
 WHERE user_id = ? AND revoked_at IS NULL;
-- 3
INSERT INTO reset_tokens
  (school_id, user_id, source, created_by, code_hash, code_plain, expires_at, used_at)
VALUES (?, ?, 'ADMIN', ?, ?, NULL, NOW(), NOW());
```

`password_hash` is produced with `hashPassword` (bcrypt, default cost) **before**
the Tx opens, so the transaction is short. Mirrors `handleChangePassword`’s
shape, plus the ledger row.

No SSE publish. The target’s next request fails with `INVALID_TOKEN` on the
refresh path and the client lands on the login page; the current access token
may live up to 15 minutes (S2, accepted).

### 10.4 Validation schema

| Field | Type | Rule | Failure |
|---|---|---|---|
| `:id` (path) | integer > 0 | parsed by `idParam`; digits only | `NOT_FOUND` |
| `password` (body) | string | `validPassword()`: 8–72 bytes, ≥1 letter, ≥1 digit | `VALIDATION` |
| body shape | JSON object | `bindBody` on malformed JSON | `VALIDATION` |

Deliberately **no** extra rules (no symbol/uppercase requirement, no
blocklist): the brief says "apply and enforce existing system password
validation rules", and `validPassword` is the single source of truth.
The frontend duplicates the predicate only for instant feedback; the server is
authoritative. Generate returns a value that already passes.

### 10.5 Failure responses (`HTTP 200`)

| Condition | Code |
|---|---|
| malformed/zero `:id` | `NOT_FOUND` |
| unknown target | `NOT_FOUND` |
| ADMIN targets a foreign-school user | `NOT_FOUND` |
| target is the actor | `VALIDATION` |
| ADMIN → ADMIN/SA, SA → SA | `FORBIDDEN_ROLE` |
| actor is FASILITATOR | `FORBIDDEN_ROLE` |
| target not `APPROVED` or inactive | `ACCOUNT_INACTIVE` |
| target's school inactive | `SCHOOL_INACTIVE` |
| missing / malformed / weak `password` | `VALIDATION` |
| > 10 requests/min/IP | `RATE_LIMITED` + `Retry-After` |
| bad/expired Bearer | `INVALID_TOKEN` |
| DB fault | `DB_UNAVAILABLE` |

### 10.6 Frontend

1. `UsersPage` row action `Reset Password` (gated by §8.3) opens a modal with two
   tabs:
   - **Generate** — button calls `…/password/generate`, shows the value in a
     read-only field with a copy button and a "this is shown only once"
     warning. **Confirm** then calls `…/password` with that value.
   - **Manual** — password + confirm-password inputs with the client-side copy
     of `validPassword` (min 8, letter + digit), submit disabled until valid.
2. Closing the modal before confirm discards the generated value — it was never
   persisted.
3. Success → `notification.success`, modal closes, list refreshes.
   Failure → `notification.error` with `common:errors.<code>` (§8.3 of
   `03-FEATURES.md`, decision 46).
4. No plaintext is ever written to `localStorage`/`sessionStorage`.

---

## 11. Security controls and residual risk

**Controls added**

- One shared `(actor × target)` predicate for both features.
- Atomic revoke-then-issue for impersonation; atomic hash + revoke for resets.
- `NOT_FOUND` for cross-school `ADMIN` access (no id oracle).
- Password endpoints excluded from the idempotency store.
- Rate limiting on all three routes.
- Durable actor→target ledger, 90-day retention, **immutable across refresh
  rotation and account deletion**.
- `impersonation` banner so an impersonated session can never be mistaken for
  the admin's own.
- Generated passwords never persisted or logged.

**Residual risks (accepted, documented)**

| Risk | Why accepted | Mitigation available later |
|---|---|---|
| Actor’s access token valid ≤15 min after the switch | Stateless JWT by design (S2) | Add `jti` + a revocation set, or shorten `JWT_ACCESS_TTL_MIN` |
| Impersonated session has full target privileges | That is the point of impersonation | Restrict to read-only routes via an `impersonated` claim |
| Ledger rows **survive** account deletion (deliberate, C10) | An abuse record must not be erasable by deleting the account | Revisit with the privacy owner; a hard-erasure job would need legal sign-off |
| 90-day retention is not legally certified | Matches `wa_outbox` | Lengthen once compliance signs off |
| Refresh rotation multiplies ledger rows | Grouping is by `impersonation_root`, not row count | Add a dedicated session table if volume ever matters |
| No step-up auth (decision 4) | User decision | Add `current_password` later without schema change |
| `request_id` exists only in `slog`, not in the ledger | No column | Add `request_id`/`ip` columns to both ledger tables |

---

## 12. Acceptance criteria

**Impersonation**

1. `SA` → `FASILITATOR` (any school): 200 `{access,user,school,impersonation}`;
   the new access token decodes to the target’s `sub`/`role`/`school`.
2. `SA` → `ADMIN`: allowed. `SA` → `SA`: `FORBIDDEN_ROLE`.
3. `ADMIN` → own-school `FASILITATOR`: allowed.
4. `ADMIN` → `ADMIN`, `ADMIN` → `SA`, `ADMIN` → `FASILITATOR` of another
   school: `FORBIDDEN_ROLE` / `NOT_FOUND` respectively.
5. `FASILITATOR` → anyone: `FORBIDDEN_ROLE`, and no `Set-Cookie` in the
   response.
6. Every successful call sets exactly one new `refresh_token` cookie and leaves
   the actor’s previous `jti` with `revoked_at IS NOT NULL`.
7. `SELECT COUNT(DISTINCT impersonation_root) FROM refresh_tokens WHERE
   impersonated_by IS NOT NULL` increases by exactly 1 per success, and every
   row of that root carries the same `impersonated_by`.
8. `POST /api/auth/refresh` after the switch returns the target identity **and**
   the `impersonation` block; a second refresh (one rotation later) still does.
9. `GET /api/auth/me` returns the block when the refresh cookie is present and
   belongs to the caller, and omits it when it is absent.
10. Self-target: `VALIDATION`.
11. Target `PENDING`/`REJECTED`/inactive: `ACCOUNT_INACTIVE`. Inactive school:
    `SCHOOL_INACTIVE`.
12. The UI hides the action for every matrix cell that is ❌.
13. A failed attempt returns no `Set-Cookie` and does not modify any
    `refresh_tokens` row.

**Password reset**

14. `SA` → `ADMIN`/`FASILITATOR` and `ADMIN` → own-school `FASILITATOR`:
    `{reset:true}`; the new password logs in successfully.
15. `ADMIN` → peer `ADMIN`, `SA` → `SA`, any `FASILITATOR` caller:
    `FORBIDDEN_ROLE`. Cross-school `ADMIN`: `NOT_FOUND`.
16. `password` shorter than 8, longer than 72 bytes, or letters-only:
    `VALIDATION` and `password_hash` is unchanged.
17. After a reset, every pre-existing refresh row of the target has
    `revoked_at IS NOT NULL`, and `POST /api/auth/refresh` for that user returns
    `INVALID_TOKEN`.
18. A `reset_tokens` row exists with `source='ADMIN'`, `created_by` = actor,
    `code_plain IS NULL`, `used_at IS NOT NULL`.
19. The reset dashboard (`GET /api/reset-tokens`) never lists an admin-ledger
    row.
20. `POST …/password/generate` returns a 16-char value that passes
    `validPassword`, writes nothing, and does not create an
    `idempotency_keys` row.
21. Restarting the API does not change any target’s `password_hash` (guards
    S8 by exclusion of SA).

**Transport**

22. Every failure above is `HTTP 200` with `{error:{code,message}}`; no 4xx.
23. Unknown route id (`/api/users/abc/impersonate`) → `NOT_FOUND`.

---

## 13. Calls I made (check these while reading)

| # | Call | Alternative if you disagree |
|---|---|---|
| C1 | `SA → SA` impersonation is blocked. | Allow it; then also fix S8. |
| C2 | `ADMIN` reset scope = own-school `FASILITATOR` only (same as impersonation). | Allow `ADMIN → ADMIN` within the school. |
| C3 | Both features block self-targeting; self-service stays on `/auth/change-password`. | Let an `SA` reset its own password here (needs S8 fixed first). |
| C4 | Impersonation revokes only the caller’s current `jti`, not every device. | Revoke all actor rows (`WHERE user_id = actor AND revoked_at IS NULL`). |
| C5 | Generated password = 16 chars, unambiguous alphabet, letter+digit guaranteed. | Any length ≥8 and any `validPassword`-satisfying generator. |
| C6 | Ledger retention = 90 days, reusing `wa_outbox`’s window. | 30/180/365 days, or no purge. |
| C7 | No idempotency on any of the three routes. | Wrap the two apply routes; must never wrap `generate`. |
| C8 | Reset ledger rows live in `reset_tokens` with `source='ADMIN'`. | A dedicated `admin_audit_events` table would be cleaner but was ruled out by decision 2. |
| C9 | `impersonation` rides on `/auth/refresh` (authoritative) and `/auth/me` (best-effort), so the banner survives a hard reload. | Return it only on the impersonate call and lose the banner after F5 — not recommended, an admin could then act on the target's account believing it is their own. |
| C10 | Ledger rows survive account deletion and are read with a `LEFT JOIN` + `[deleted #id]` fallback. | Delete actor/target ledger rows too (matches the project's no-trace stance, but lets an admin erase their own abuse record by deleting the account). |
| C11 | Ledger grain = `impersonation_root` (rows multiply on refresh rotation); AC 7 counts distinct roots. | One row per impersonation with a separate session id; requires not propagating on rotation, which breaks the banner. |

---

## 14. Implementation map (for the plan, not this document)

| Area | File | Change |
|---|---|---|
| Migration | `backend/migrations/0003_admin_controls.sql` | §7.1 |
| Janitor | `backend/internal/files.go` | §7.3 retention + expiry stamp |
| Refresh rotation | `backend/internal/auth.go` `handleRefresh` / `insertRefreshOn` | carry `impersonated_by` + `impersonation_root` |
| Auth queries | `backend/internal/auth.go` | `source='SELF'` filters; `impersonation` on refresh (authoritative) and me (best-effort) |
| Deletion | `backend/internal/users.go` `deleteUser` | keep ledger rows (`AND impersonated_by IS NULL` / `AND source='SELF'`) |
| Impersonation | `backend/internal/users.go` | 3 routes, predicate, Tx, generation |
| Tests | `backend/internal/users_test.go`, `auth_test.go` | §12 |
| Routes/UI | `frontend/src/pages/users/UsersPage.tsx`, new modal | §9.6, §10.6 |
| Session | `frontend/src/lib/auth.tsx` | `impersonation` field, session epoch |
| Banner | `frontend/src/components/AppLayout.tsx` | impersonation strip |
| i18n | `frontend/src/locales/{id,en}/auth.json`, `common.json` | new keys |
| Contracts | `docs/superskill/task/2026-10-09-contracts-shapes.md` | `impersonation` shape |
| Specs | `02`, `03`, `04`, `05`, `06`, `08`, `SPEC-INDEX.md` | decisions + route table + data model |

---

## 15. Open item carried forward

**Resolved 2026-11-24.** `updateSeedSA()` (`internal/seed.go`) overwrote the
`SA` password with the hardcoded bootstrap value on **every boot**. Feature B
avoided the trap by blocking `SA` as a reset target. The SQL seed refactor
(`backend/seeds/0002_sa.sql`) now inserts the SA row only when the bootstrap
email is missing, so dashboard edits survive restarts; a regression test
(`TestSeedFilesAreInsertOnly`) pins the insert-only rule. Blocking `SA` as a
manual reset target remains the product decision.

# 06 — Errors and Edge Cases

Status: DRAFT (2026-10-09; adding test strategy + A4 fixed report; pending audit)

Transport rule (binding, from user decision): every `/api/*` and
`/public/*` endpoint returns HTTP 200 with `{data}` or
`{error:{code,message}}`. The ONLY exception is an unhandled server fault
→ HTTP 500 `{error:{code:"INTERNAL"}}`. There is no 401/403/404/409/422/
429 status in this API: not-found, not-authorized, conflict, validation,
and rate-limit outcomes are all HTTP 200 envelopes whose `code` tells the
client what happened. Rate limiting adds a `Retry-After` response header.

## 1. Error taxonomy (code → HTTP → user message → recovery)

All rows: HTTP 200 (except INTERNAL → 500).

| # | Code | When | User-visible (ID/EN via i18n) | Recovery |
|---|---|---|---|---|
| E1 | VALIDATION | missing/invalid field, badge half-filled, bad mime/size, weak password, age out of range, end≤start | field errors inline | fix + resubmit |
| E2 | EMAIL_TAKEN | email exists globally | "email already used" | use another / login |
| E3 | INVALID_CREDENTIALS | wrong email/password | generic "wrong email or password" | retry (limiter counts, `Retry-After`) |
| E4 | INVALID_TOKEN | bad/expired access JWT | re-login silently via refresh once | refresh-once then re-login |
| E5 | ACCOUNT_INACTIVE | pending/rejected/deactivated | "waiting approval / deactivated" | contact admin; client clears session |
| E6 | TENANT_INACTIVE | tenant nonaktif (register/login/any member call); tenant deleted | "tenant disabled" | SA only; client blocks writes |
| E7 | TENANT_MISMATCH | `X-Tenant-ID` ≠ JWT claim (SA stale tab switching tenants; non-SA tampered/forged header) | "tenant changed, reloading" | SA: client reloads context + re-switch; non-SA: client clears to own tenant from JWT claim; both logged as auth-anomaly |
| E8 | FORBIDDEN_ROLE | wrong role for endpoint (incl. FAS consent-send, FAS children mutate, non-SA tenant admin) | "no access" | switch account / hide button |
| E9 | NOT_OWNER | facilitator touches foreign group/child | thumbnail only | open own group |
| E10 | SESSION_LOCKED | wrong-STATE call by a single writer (stars/notes write on Draft or Finished; snapshot/program/groups/members write on Finished; DELETE on Opened; finish-on-Draft / open-on-Finished / reopen-on-non-Finished) — decided by a status RE-READ after the guarded UPDATE hits 0 (expected≠current and no rival transition: current state itself forbids the op) | read-only notice | open/reopen/finish first |
| E11 | STATE_CONFLICT | concurrent status transition lost (guarded `UPDATE…WHERE status` hit 0 AND re-read shows status already moved to the wanted-or-beyond state by a rival) — decided by the same re-read (rival won the race) | "changed elsewhere, reloaded" | reload session |
| E12 | CHILD_DUPLICATE | child in 2 groups same session | blocked with names | remove from one |
| E13 | PROGRAM_INCOMPLETE | program w/o topics/activities into session | blocked with reason | complete master |
| E14 | GROUP_WITHOUT_FACILITATOR | open with ownerless group / assessment with missing owner | blocked list | assign owner |
| E15 | PHOTO_LIMIT | 11th photo (Tx count) | "max 10, delete or replace" | delete/pick |
| E16 | CONSENT_LOCKED | photo op while locked/rejected | "waiting parent approval" + resend (ADMIN/SA) | send consent |
| E17 | REPORT_INCOMPLETE | generate/send with unstarred activities | missing list | finish assessing |
| E18 | NOTHING_TO_NARRATE | narrative with zero assessments | "assess first" | assess first |
| E19 | MISSION_LIMIT | 5th mission pick | "max 4" | unpick one |
| E20 | AI_FAILED | Gemini error/timeout/limit | "AI failed" + Retry, old kept | retry later |
| E21 | WA_FAILED | OpenWA error/down (per item) | per-item error + retry + copy URLs | retry/manual |
| E22 | MAIL_FAILED | SMTP down (forgot still 200 `sent:true`; row masked-listed) | "email failed, ask admin for code" | dashboard reveal + manual relay |
| E23 | RATE_LIMITED | limiter (login/reset/public/respond; HTTP 200 + `Retry-After`) | "too many, wait Xs" | wait, respect header |
| E24 | TOKEN_INVALID | bad/revoked/expired public/consent token (identical shape, no oracle) | public "link invalid" | request new link |
| E25 | RESET_EXPIRED | code past `expires_at` | "code expired" | request again |
| E26 | RESET_USED | code already used/revoked | "code used" | request again |
| E27 | DB_UNAVAILABLE | pool down / Tx timeout | "service busy" + Retry | retry; `/readyz` false |
| E28 | CAMERA_DENIED | getUserMedia refused (client-side, no HTTP) | "allow camera or upload" | upload fallback |
| E29 | PHOTO_MISSING | photo row present but file gone on disk — an ITEM-level flag (`{missing:true}` on the gallery item; report exports skip it), never a top-level `{error}` envelope | item hidden with notice | re-upload / delete row |
| E30 | NOT_FOUND | unknown id or cross-tenant id (no leak; same shape); non-member child on any per-child-per-topic `P` op or consent-send (round 5 membership gate) | "not found" | back to list |
| E31 | HAS_OPEN_GROUPS | delete user owning OPENED groups | "reassign groups first" + list | reassign then delete |
| E32 | CHILD_INACTIVE | create/add involving inactive child | "child inactive" | reactivate |
| EX | INTERNAL | unhandled panic/DB-boot fail (HTTP 500 only) | "unexpected error" | retry; check logs |

Removed codes (do not implement): `TENANT_DISABLED` (folded into
`TENANT_INACTIVE`), `CANCELED`, `QUEUED`, `CONSENT_FINAL` (dead — no
endpoint returns it; repeat submit = 200 success `already:true`).

## 2. Failure propagation (canonical)

```text
DB down → store err → 200 DB_UNAVAILABLE + Retry → /readyz false →
  boot retry loop; client toast, no state loss (saves are per-card)
OpenWA down → wa.go err (1 retry) → 200 WA_FAILED per item → UI marks
  failed, others continue → per-item Retry + copy-URLs fallback
Gemini down/timeout → 200 AI_FAILED → old narrative kept → manual Retry;
  report completable without narrative ("—")
SMTP down → 200 {sent:true} + masked dashboard row → ADMIN reveals +
  relays manually (MAIL_FAILED shown only on relay-diagnostic path)
Camera denied → E28 client state → gallery-upload button
Token miss/revoked → 200 TOKEN_INVALID identical shape (no oracle)
Stale SA tab → 200 TENANT_MISMATCH → client reloads tenant context
Concurrent open/finish/reopen → loser 200 STATE_CONFLICT → reload
SSE stream (round 7): publish is best-effort — hub-full/DB-down at
  publish time → drop + `slog`, mutation already committed (client learns
  via its own 200 `{data}` + next refetch; other tabs catch up on next
  event or 60 s memo expiry). `stream.Write` flush failure → server closes
  that connection (`stream.Done()`); client reconnects with
  `Last-Event-ID` (replay from ring; unclosable gap → full-refetch active
  page). NO error code exists for stream transport itself (frames carry no
  `{error}`); handshake failures use the normal 200 codes
  (`INVALID_TOKEN` / `FORBIDDEN_ROLE` / `TENANT_MISMATCH` /
  `TENANT_INACTIVE` / `TOKEN_INVALID`) and never open a stream.
```

## 3. Retry & idempotency

- Mutating POSTs (`POST /api/auth/register`, `POST /api/sessions`, WA
  single/batch, consent send, public-link create/regenerate, photo upload,
  narrative generate (POST), public consent respond, copy-data execute)
  REQUIRE
  `X-Idempotency-Key`; replay within 24 h returns the original envelope
  (no second row/send). Key is stored at REQUEST time (before work
  starts); retry same-key DURING copy execution (before the first 200)
  returns 200 `{data:{status:"IN_PROGRESS"}}` with NO new Tx — the client
  polls copy-candidates until `copy.completed` arrives. Register retry → same key, no `EMAIL_TAKEN` mask.
- Refresh rotation: single-use jti; two parallel refreshes → one wins,
  loser gets 200 `INVALID_TOKEN` → client re-logins (no loop: max 1 retry).
  If the rotation Tx fails (DB down mid-rotation): the OLD jti stays valid
  (revoke did not commit) and the client retries refresh with the same
  cookie → 200 `DB_UNAVAILABLE` + Retry, no spurious logout.
  Commit-succeeded-but-response-lost (client retries with the now-revoked
  OLD jti): server returns 200 `INVALID_TOKEN` → client MUST re-login
  (no grace/reuse window for MVP — a successful rotation invalidates the
  old jti immediately; the user already holds the new tokens server-side
  only in the lost response, so re-login is the defined recovery).
- Reset single-use: success-lost retry with same code → 200 `RESET_USED`
  BUT client first re-logins with the new password to disambiguate
  (password-changed vs code-wrong).
- Relay-diagnostic path (binding, where `MAIL_FAILED` surfaces): the
  forgot-password response path itself — SMTP-down forgot still returns
  200 `{sent:true}` BUT the row is masked-listed in the reset dashboard
  with `mail_failed:true`, which IS the 200-`MAIL_FAILED` signal the admin
  acts on (dashboard reveal + manual relay). No separate diagnostic
  endpoint exists; `MAIL_FAILED` never appears as a top-level error on any
  other route.
- Consent success-lost: client re-GETs `/public/consent?token=`; repeat
  submit → 200 `{already:true}` (never an error).
- Photo POST: same key returns original row (no duplicate toward cap).
- Copy-data execute: same key returns original
  `{copied, skipped}` counts (fill-empty makes re-execution safe anyway).
- WA batch: crash after OpenWA accept → retry SAME key returns recorded
  per-item statuses (no double-send); new sends need a new key.
- Assessment PUT / mission PUT / candidate PATCH are naturally idempotent.
- AI generate: manual retry only (no auto-loop, protects free-tier quota).
- Offline edits are NOT queued (no offline mode): error surfaced, user
  retries when online; per-activity saves already persist.

## 4. Concurrency & races

- Status machine guarded by `UPDATE … WHERE status=<expected>` + status
  re-read on 0 rows affected (wrong-state single-writer → `SESSION_LOCKED`;
  rival-won race → `STATE_CONFLICT`; no silent LWW on transitions).
- Two assessors same card: LWW accepted (single owner; ADMIN overwrite
  intentional) — documented, no version guard for MVP.
- Photo cap: Tx `SELECT COUNT(*) … FOR UPDATE` (no 11th row under race).
- Missions ≤4: Tx count check → `MISSION_LIMIT` (client pre-blocks 5th).
- Candidate: one Tx clear+set + partial-unique (no double-candidate).
- Consent: `UPDATE … WHERE PENDING` first-wins + button disable.
- SSE filter (round 7, binding): send/skip per connection REUSES the REST
  scope functions (same import, not a copy). Scope snapshot per
  connection is taken at handshake and recomputed on membership-affecting
  publishes (`member.changed`, owner reassign, child deactivate); a stale
  scope window of at most one publish is accepted (refetch guards still
  enforce — worst case a skipped signal, never leaked data, because
  frames carry no PII).
- Stream caps: max 100 concurrent streams per `api` replica + max 6 per
  IP + max 20 per tenant (single table — same in 01/02§2c); excess handshake → 200 `RATE_LIMITED` + `Retry-After` (no stream).
  Stream routes are otherwise limiter-exempt.
- Batch vs finish mid-batch: items send on data present at send time;
  deleted/finished-midway items return per-item `WA_FAILED`/`NOT_FOUND`.

## 5. Edge cases (specified behaviour)

- Empty program/topic → picker hides with reason; direct API → E13.
- No assessments → E17/E18 with missing list.
- Badge-less topic/program → section omitted (no error).
- Report without photo/narrative → generatable (photo hidden, narrative "—").
- Child removed after assessment → session assessments deleted silently
  (no trash for MVP); masters untouched.
- Child deactivated → hidden from pickers/creates; history + public
  report stay LIVE (deactivate revokes NO links — `link.revoked` fires
  only on revoke/regenerate/session-wipe/child-HARD-delete); public
  gallery returns `{photos:[], locked:true,
  reason:"CHILD_INACTIVE"}` (inactive wins over consent). Inactive-child
  gates (binding): session-create `groups[].child_ids`, member add,
  `send-consent`, assessment PUT, photo POST, narrative POST, mission PUT,
  `send-report`/batch items involving an inactive child → 200
  `CHILD_INACTIVE` (no row/send). Consent RESPOND still recorded
  (first-wins) but gallery stays locked until reactivation.
- Facilitator deleted while owning OPENED groups → blocked (`HAS_OPEN_GROUPS`);
  after reassign+delete, groups show snapped name; assessment blocked only
  while owner missing; old artefacts stay.
- Tenant inactive → member calls 200 `TENANT_INACTIVE`; SA read-only.
- Tenant hard-deleted (typed) → tenant rows + disk gone; `wa_outbox` 90 d;
  OpenWA untouched. Partial wipe → restore from host backup, re-delete.
- Finished deleted (typed) → session scope wiped; masters/children kept.
- Age: 1–10 ok; 11–15 needs modal (`age_confirmed=true`); else `VALIDATION`.
- WA invalid → `VALIDATION`; duplicates across children allowed (send
  failure per item if unreachable).
- Duplicate names allowed; UI disambiguates (school/mother/age).
- Restart → state in DB/disk; whitelist survives; limiter resets (accepted).
- Clock skew → 60 s JWT leeway; TTLs server-side.
- Browser refresh mid-assessment → per-activity saves persist; unsaved
  card input lost (draft-notice).
- Slow images → `cacheBust` + `onload` gate, 10 s timeout → error + retry
  (no half-file download).
- Copy-data (round 6): source deleted mid-copy → affected rows 200
  `NOT_FOUND` per execution (partial Tx rolled back, no half-copy);
  crash between copy-commit and `io.Copy` does NOT leave servable
  half-copies: copied photo rows are created `file_ready=FALSE` and are
  INVISIBLE until their file lands (gallery/PHOTOS queries serve ONLY
  `file_ready=TRUE`; FALSE rows NEVER surface as `PHOTO_MISSING` — that
  flag is ONLY for TRUE rows whose file is gone; stuck FALSE rows >24 h
  are deleted row+files by the janitor). Source FINISHED vs target OPENED allowed (reads any status); target
  FINISHED → stars/notes/narratives/missions blocked (`SESSION_LOCKED`),
  photo part may still proceed; unmatched target topics/activities stay
  empty (added-topic case → fresh assessment required, never auto-filled);
  re-run same key → original counts (no double-copy — fill-empty is
  idempotent); copy-candidates on cross-program sessions → empty list
  (no modal); later DITOLAK on target → ALL photo rows + files for that
  child+session deleted (original + copied), non-photo copies stay.
- SSE stream (round 7): disconnect mid-batch → reconnect + `Last-Event-ID`
  replays missed signals from the ring (100 events / 5 min / tenant);
  replay applies the connection's scope filter FIRST, then the gap check
  (a filtered-out `rev` jump is not a gap — no leak, no false full-refetch);
  ring overflow or server restart → unclosable gap → client full-refetches
  the active page once (accepted, logged). Duplicate delivery (replay
  overlap) → ignored via `rev <= last_seen_rev` + idempotent GET refetch
  (no double-write — SSE never mutates). `auth.expiring` missed (tab
  asleep) → next write/refetch gets 200 `INVALID_TOKEN` → refresh-once →
  reconnect (max 1 retry, then re-login). SA switches tenant in another
  tab → old-tab stream keeps OLD tenant scope until its next signal;
  tenant-switch ALWAYS closes + reopens the stream (no cross-tenant
  frame can land — filter checks tenant first). Two tabs same tenant =
  two connections, both receive; coalesce is per-tab. Browser kills tab →
  server notices on next flush error (`stream.Done()`) and drops the
  subscription (no leak). Public token revoked while page open →
  `link.revoked` + close + "link invalid" (same meaning as
  `TOKEN_INVALID`). Upload crash after file durable but before row commit
  → orphan swept, NEVER announced (no fake status); crash after commit
  but before publish → row exists, no signal — next refetch/memo shows
  it (at-most-once delivery, refetch closes the gap).
- Disk full / path traversal: server 200 `VALIDATION` (full) / `INTERNAL`
  only on unexpected IO; filenames are server-generated UUIDs (no
  traversal); photo file missing on disk but row present → gallery item
  returns placeholder + `PHOTO_MISSING` flag (report exports without it).

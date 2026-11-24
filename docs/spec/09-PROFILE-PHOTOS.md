# 09 — PROFILE PHOTOS (design, 2026-11-10)

Per-account profile photo, self-edited on the Profile page, shown in the rail's
avatar placeholder. This document is the written design for the 2026-11-10
interview; it adds to (and never rescinds) the 28 locked decisions in
`docs/spec/00-08`. Consolidation duties it assumes: **one** frontend upload
gate, **one** server compression path.

## Intent & why

Accounts — SA, ADMIN, FASILITATOR (roles with a login; no children/parents) —
get a profile photo. The rail footer's existing avatar placeholder
(`AppLayout.tsx` `kv-rail-user__btn`) shows the photo when one exists and keeps
the current `avatarUrl(name)` initials fallback when not. The photo is edited
through a modal on the existing ProfilePage (`F-14`).

## Locked decisions (client, 2026-11-10 interview)

1. **Frontend gate — 50 MB.** Files >50 MB are rejected client-side; ≤50 MB go
   to the server. The gate is ONE shared function in `lib/validation.ts` used
   by every upload surface. Consolidation: `isAcceptedImage`
   (`pages/masters/shared.tsx`) migrates to the shared helper and the inline
   `file.size > MAX_UPLOAD_BYTES` check in `GalleryPage.tsx` is deleted;
   existing tests follow their surfaces.
2. **Server whitelist — JPG/JPEG/PNG only, platform-wide.** Frames, badges,
   session/child photos and profile photos all accept only JPG/JPEG/PNG.
   WebP is retired from NEW uploads: server rejects it, frontend accept-lists
   and every locale hint string ("JPG/JPEG/PNG/WebP, maksimal 50 MB") drop it.
   Enforcement is real server-side verification (decode-config + declared type
   cross-check), NOT trust of the multipart Content-Type. Previously stored
   WebP files keep serving. The old anywhere-accepts store-as-`.bin` behavior
   is replaced on the image path by VALIDATION rejections.
3. **Server compression — centralized, triggered >10 MB.** ONE code path: the
   shared image-storage helper (`storeImage`) compresses any stored image
   larger than 10 MB. Original format is PRESERVED (never converted):
   - JPEG: quality ladder ~90→80→70… until ≤10 MB; a resolution step is the
     final backstop.
   - PNG: resolution steps (CatmullRom downscale) + `png.Encoder` at
     BestCompression until ≤10 MB.
   Files ≤10 MB are stored byte-for-byte as received (unchanged behavior).
   Extension and recorded mime are recomputed from the OUTPUT (e.g. an output
   JPEG stores as `.jpg` + `image/jpeg`) — nothing lies about its type.
4. **Concurrency cap — 5.** A buffered-channel semaphore (NOT a mutex) limits
   concurrent compressions to 5. Handlers block while waiting; the slot is
   released on every outcome (defer). FIFO queueing beyond 5, HTTP timeouts
   are the backstop.
5. **Codec — `golang.org/x/image/draw` (CatmullRom) + stdlib encoders.**
   No cgo, no libvips, `CGO_ENABLED=0` untouched. Compression runs
   synchronously inside the upload handler.
6. **Editor UX — modal on ProfilePage.** Drag-drop + file picker; zoom (wheel + control); pan by drag; rotate **slider 0–360°**; a **standard grid** overlay; Confirm / Cancel; Delete visible only when a photo exists;
   **real-time upload progress 0–100%**, then an honest "processing" state
   while the server compresses/stores.
7. **Composition — option A.** The browser composes the crop on a canvas and
   uploads the finished image (512×512 square JPEG, high quality); the server
   never sees crop parameters and never keeps the original.
8. **Avatar — the existing placeholder only.** Rail avatar `src` =
   `user.photo_url ?? avatarUrl(user.name)`. Badge avatars (programs, topics,
   frames) are untouched.

## Builder's calls (settled here — veto if wrong)

- **Endpoints:** `POST /api/auth/profile-photo` (multipart field `file`,
  `X-Idempotency-Key` per convention) and `DELETE /api/auth/profile-photo`.
  Both return `{data:{user}}` in the PATCH-profile pattern; roles reach them
  through the profile page they already own. Old photo file is unlinked only
  after the row update succeeds (frames/badges replacement pattern).
- **Migration:** next-numbered `ALTER TABLE users ADD COLUMN photo_path
  VARCHAR(255) NULL` (match existing path column widths; nullable).
- **Storage:** `uploads/avatars/<user_id>/<rand32hex>.<ext>` — keyed by the
  account, not the school, which cleanly covers SA's school-null case. The
  janitor's `referencedPaths` adds `users.photo_path`. Honest-file rule (temp +
  fsync + rename) applies as everywhere else.
- **Failure envelope:** over-size-slip-through, non-image, undecodable, or
  compression-failure responses use the existing `VALIDATION` code (HTTP 200
  envelope), per the no-HTTP-errors rule. Full-disk stays
  `failStore`/errRejected.
- **No new SSE signal.** The uploading client refreshes its avatar from the
  POST response; other tabs/devices of the same account pick the photo up on
  their next session/profile fetch. (The contracts file the AGENTS.md pins —
  `docs/superskill/task/2026-10-09-contracts-shapes.md` — is absent from the
  tree; the de-facto contracts are the pinned shapes in code: `User` in
  `lib/auth.tsx`, `userRow.json()`/`userItem` in the backend.)
- **Which payloads gain `photo_url`:** the auth user objects only — login,
  refresh-restore, `/api/auth/me`, `/api/auth/profile` PATCH, and the photo
  POST/DELETE — so the rail avatar survives a reload. Users-list rows
  (`userItem`) stay untouched per the non-goal; the backend splits the shared
  JSON shape and the TS `User` type gains an optional `photo_url`.
- **Ladder termination:** the resolution lever guarantees the ≤10 MB target is
  reached (every moderate-resolution re-encode fits); an iteration guard logs
  and stores the best effort if the guard ever trips — the server NEVER
  rejects an image for being unable to compress.
- **Frontend plumbing:** `uploadWithProgress` (XHR, same envelope + token +
  idempotency semantics) added beside the fetch-based `upload()` in
  `lib/api.ts`; used by the photo POST only. Auth context's `User` gains
  `photo_url`.
- **Editor component:** `components/profile/PhotoEditorModal.tsx`; zoom bound
  to fit-scale ≤ ~3×; crop math pure functions where testable; keyboard
  accessible, reduced-motion respected, focus-trapped like other modals; i18n
  under the existing `auth:profile.*` namespace (id + en).
- **Server-side size cross-check:** the request body is bounded (≤50 MB + the
  multipart overhead) at the endpoint; a body exceeding the client gate's
  slip-through is VALIDATION.

## Non-goals

- No re-crop from the original later (the original is discarded; only the
  composed export is stored).
- No avatars for children, parents, or consent respondents — accounts only.
- No avatar column in the Users list payloads/views (rail + profile only).
- No background worker/queue — the cap-5 semaphore in-request is the whole
  scheduler.

## Risks

- **jsdom has no canvas 2D:** editor tests stub `getContext`/`toBlob`
  (project-standard vitest pattern); crop math stays pure functions so the
  important parts test without canvas.
- **Memory:** cap-5 × ~67 MB pixel buffers worst-case on 50 MB 4K decodes;
  acceptable, bounded by the semaphore (risk accepted in interview).
- **ui-avatars.com stays the fallback** (pre-existing external dependency,
  unchanged by this feature).

## Test expectations (mirrors repo conventions)

- Backend: sqlmock + multipart envelope tests for both endpoints; whitelist
  tests (webp + garbage + decodable jpg/png through DecodeConfig); compression
  loop test on a synthetic >10 MB JPEG (assert ≤10 MB, JPEG mime, byte-length
  sanity) and a big PNG; semaphore cap test; old-file unlinked after replace.
- Frontend: gate dedupe (one helper, all surfaces); editor
  zoom/rotate/pan/crop math; progress callback ladder 0–100; delete flows;
  rail avatar photo-vs-fallback switch.

# Laporan Audit Sinkronisasi Spec ↔ Codebase — Kidversa EduTourism v2

Tanggal: 2026-10-11
Catatan terminologi: laporan ini ditulis sebelum rename Tenant → School (2026-11-18). Seluruh istilah "school" di bawah adalah istilah hasil rename dari "tenant" pada saat audit; temuan historis menggambarkan perilaku lama dengan kosakata terkini. Rincian keputusan: `docs/superpowers/specs/2026-11-18-tenant-to-school-design.md`.
Metode: 8 subagent `task` paralel (masing-masing dengan subagent bertingkat, semua di-`wait` sebelum konsolidasi), eksplorasi via codebase-memory-mcp (project `D-Project-PakAdit-Project-kidversa-edutourism-v2`, index ready 5819 nodes/25168 edges) + codegraph + native read/grep/glob/lsp/ast_grep. Audit murni READ-ONLY; satu-satunya berkas yang dibuat adalah laporan ini. Sepuluh berkas spec dibaca utuh sebelum audit: 00-SCOPE, 01-TECHSTACK, 02-ARCHITECTURE, 03-FEATURES, 04-FLOWS, 05-DATA, 06-ERRORS-AND-EDGE-CASES, 07-BEFORE-AFTER, 08-ACCEPTANCE-CRITERIA, SPEC-INDEX.

Catatan path: goal menyebut `D:\Project\PakAdit\Project\kidversa-team-v2\docs\spec\00-SCOPE.md` — path tersebut TIDAK ADA; 00-SCOPE.md yang diaudit adalah `docs/spec/00-SCOPE.md` di repo ini.

---

## 1. Ringkasan eksekutif

Verdict per dimensi:

| # | Dimensi | Verdict | Ketidaksesuaian |
|---|---|---|---|
| 1 | Scope & fitur inti (F-01..F-08) | SINKRON | 6 (1 material) |
| 2 | Tech stack & dependensi | SINKRON mayoritas | 11 (1 material: TS strict mati) |
| 3 | Arsitektur & struktur modul | SINKRON (route table diff dua arah kosong) | 6 minor + 2 belum terverifikasi |
| 4 | Alur inti FL-01..FL-08 | SINKRON mayoritas | 8 (2 material: stream handshake, picker SSE) |
| 5 | Fitur turunan F-09..F-16 | SINKRON mayoritas | 8 (1 material: SA cross-school summary) |
| 6 | Data model & layer data | SANGAT SINKRON (25/25 tabel identik) | 5 minor (semua spec usang/redaksi) |
| 7 | Error & edge case | SINKRON (33/33 code ada; 4 kode dihapus benar-benar absen) | 5 (2 material) |
| 8 | Kriteria penerimaan & non-fungsional | SINKRON (semua blok acceptance punya implementasi; semua test gate LULUS kecuali gofmt) | 9 |

Bukti verifikasi perintah (dimensi 8, dijalankan subagent):
- `go vet ./...` clean; `go test ./internal/... -cover` **LULUS, coverage 99.1%**; `go test ./internal/integration/... -tags=integration` **LULUS** (sqlmock, tanpa Docker).
- `npx tsc --noEmit -p tsconfig.app.json` **LULUS**; `npx oxlint .` **0 error** (11 warning react); `npx vitest run` **590/590 LULUS**; `vitest --config vitest.integration.config.ts` **14/14 LULUS**.
- `gofmt -l .` **menampilkan 5 file** (spek menuntut nol) — satu-satunya gate perintah yang gagal.

Keseluruhan: **codebase sangat sinkron dengan spec** — skema data identik penuh, route table dua arah kosong, transport rule 200-envelope konsisten, SSE/honest-file/paging sesuai keputusan user #22–#28. Ketidaksesuaian terkonsentrasi di: (a) beberapa celah jaminan server (idempotency POST /users, guarded-update assessment, stream handshake jti), (b) pendaftaran SSE di beberapa picker frontend, (c) daftar spec yang usang (terutama 01-TECHSTACK §5 dan beberapa teks di 05/06), (d) gap test.

---

## 2. Item SESUAI (ringkas, bukti lengkap di laporan subagent)

- **Scope/fitur inti**: seed idempoten `Kidversa` school + SA (SQL seeds); JWT 15m/7d + leeway 60s + jti rotation 1 Tx; reset code hashed+masked+reveal-audited+revoke+purge; SCHOOL_INACTIVE tanpa SCHOOL_DISABLED; badge both-or-neither (server+klien+DDL CHECK); age 1-10/11-15 age_confirmed/>15 VALIDATION; WA +62; session 3-status tanpa CANCELED, guarded transition, physical wipe + typed modal, UNIQUE(child,session), groups[] opsional; scope OUT bersih (tanpa parent login/payment/server-PDF).
- **Tech stack**: Go 1.26.6 + tepat 4 paket runtime (+ testify/sqlmock untuk test; uuid/schema hanya indirect); 32 stdlib persis daftar; middleware Fiber v3 sesuai (Bind-only, keyauth Extractor, limiter komposit + LimitReached 200, TrustProxy, static.New, sse transport-only); 7 npm inti + React 19/Vite 8/antd v5; tanpa axios/tailwind/zustand/VITE_*; 31 env key = .env.example + boot-fail loud 13 key; compose/nginx persis (env_file only, buffering off stream, 12m, 180s).
- **Arsitektur**: route table §2b diff dua arah KOSONG; pipeline order persis; 24 file internal + 3 wajar (envelope/idempotency/pscope), tanpa layer; stream hub ring 100/5m + caps 100/6/20; 12 paged route + full-array split; frontend §3 19/19 (RequireRole, api.ts envelope+refresh-once, stream.ts fetch+reader, StreamGate auth-only, /view/* page-level, menu matrix, report plain CSS, paging server-side); zero FK; semua peer boundary ada.
- **Alur FL-01..FL-08**: seluruh publish `s.publish` terverifikasi post-commit; loser STATE_CONFLICT publish nothing; wipe + token-precise link.revoked; 8 gate CHILD_INACTIVE lengkap.
- **Fitur turunan F-09..F-16**: honest-upload (preview lokal → 200 → photo.changed → refetch → /uploads), consent first-wins + DITOLAK wipe semua foto (original+copied) post-commit sebelum publish, WA single status SELALU di dalam data, batch ≤10 + jitter 3-10s + 1 report-link.changed per batch, attestation LIVE owner + fallback snap, export A4 fixed (report.css @page A4 10mm, 210×297, aspek 1:1.414, overflow notice, jsPDF a4 + toPng cacheBust), copy-data fill-empty + file_ready flip + 200 withheld + target FINISHED blokir seluruh copy, SSE signal-only + 4 full-value exceptions + filter reuse scope REST + rev gating + reconnect Last-Event-ID + auth.expiring; **tanpa sisa polling 5/30/60s**.
- **Data model**: 25/25 tabel §2 = DDL dengan field/ENUM/CHECK/UNIQUE/index cocok; zero FK; candidate_slot generated UNIQUE; sort allowlist + default sort + id ASC tiebreak persis; transforms WA/stars/prompt; janitor hourly lengkap; Tx per §7 + publish-after-commit di semua klasifikasi.
- **Error/edge**: 33/33 code E1-E32+EX ada + parity test 3 arah (BE catalog_test + FE errors.parity); 4 kode dihapus absen; E10/E11 via re-read; 11 situs `s.idempotent` persis daftar 06§3; refresh/reset/consent idempotency paths persis; 28/28 edge §5 (13/15 + 15/15 per bagian).
- **Penerimaan**: semua 12 blok acceptance + Test Acceptance backend/frontend punya implementasi+test; Change 1-12 07-BEFORE-AFTER semua komponen AFTER ditemukan; 28 keputusan SPEC-INDEX utuh (#22/#23/#25/#26/#27/#28 spot-check lolos); tanpa CI config (sesuai "CI out of MVP").

---

## 3. Item TIDAK SESUAI

### 3A. Kode kurang (perbaikan kode yang dipertimbangkan)

| # | Temuan | Bukti | Sumber |
|---|---|---|---|
| K1 | **[material]** Tulisan assessment/narrative/mission hanya pre-read status, TANPA guarded UPDATE → TOCTOU: finish yang commit antara pre-read & upsert membuat bintang mendarat di sesi FINISHED (transisi status sendiri sudah guarded) | assessments.go:338-347,408-419; reports.go:568-577,614-637,778-787,906-915 | Audit7 |
| K2 | **[material]** `POST /api/users` tanpa idempotency server-side (route tanpa `s.idempotent`; FE kirim header tapi diabaikan; double-submit → EMAIL_TAKEN, bukan replay) | users.go:155; banding auth.go:588 | Audit1 |
| K3 | **[material]** Handshake `GET /api/stream` tidak memverifikasi jti whitelist — Bearer valid milik sesi yang sudah logout tetap membuka stream s.d. expired (client aman, jaminan server tidak) | stream.go:125 + auth.go:128-155 | Audit4 |
| K4 | **[material]** Wizard `SessionCreatePage`/`GroupsPanel` TIDAK meregistrasikan SSE (program.changed/child.changed) — program baru admin A / child dinonaktifkan tidak refetch di tab lain (melanggar motivating case FL-04) | SessionCreatePage.tsx:60-68; GroupsPanel.tsx:74-78 | Audit4 |
| K5 | `MissionPanel` terbuka hanya dengar mission-pick.changed, tidak refetch bank saat mission.changed | MissionPanel.tsx:55-67,137-141 | Audit4 |
| K6 | WA batch crash di tengah loop → retry same-key = IN_PROGRESS, bukan recorded per-item statuses (tanpa read-back wa_outbox by idem_key; anti-double-send tetap aman) | reports.go:1453-1456,1552; idempotency.go:97-99 | Audit7 |
| K7 | **[material, bisa juga spec usang]** SA cross-school summary (F-13 "switcher + cross-school summary + school list") ditolak → VALIDATION; hanya 2 route dashboard + listSchools tanpa angka | dashboard.go:21-22,36-39,66-69 | Audit5 |
| K8 | **TS strict mati** (01§4 "TS strict (template default)") — grep strict=0 di semua tsconfig | tsconfig.app.json:19-26 | Audit2 |
| K9 | Mini-report preview kompose TIDAK di-scale (render 210mm fixed + overflowX auto); skala hanya di halaman publik | ReportsPage.tsx:277-279 | Audit5 |
| K10 | `gofmt -l .` menampilkan 5 file test (spek menuntut nol) | auth_extra_test.go, consents_extra_test.go, infra_extra_test.go, misc_extra_test.go, reports_extra_test.go | Audit8 |
| K11 | "unsaved card input lost (draft-notice)" — draft-notice tidak ada di frontend | grep 0 match; AssessmentPage.tsx | Audit7 |
| K12 | Edit kaskade link tidak dipublish: deleteProgram menghapus frame_programs hanya publish program.changed; deleteTopic menghapus mission_topics hanya publish topic.changed | programs.go:442-445; topics.go:366-369 | Audit4 |
| K13 | PATCH `{program_id}` diabaikan dengan 200 sukses (len(sets)==0), bukan SESSION_LOCKED seperti kontrak wire spec | sessions.go:1144-1151,1245-1252 | Audit1 |
| K14 | Filter `?is_active` di SchoolsPage non-fungsional (backend hanya membaca ?q) | SchoolsPage.tsx:44-47; schools.go:113-145 | Audit1 |
| K15 | Gap test: test integrasi AI_FAILED tidak ada (DB_UNAVAILABLE & WA_FAILED ada); larangan ≤50 kata hanya di prompt tanpa enforcement/tes; ~6 sub-klaim acceptance tanpa test pengikat (notes selamat saat consent pending, PHOTO_LIMIT race, assessor_id=copier, negative copy, report publik anak nonaktif) | integration/ (grep 0); ai.go:81-85; system.md:4 | Audit8 |

### 3B. Spec usang (perbaikan spec, kode benar)

| # | Klaim spec | Realita kode | Sumber |
|---|---|---|---|
| S1 | 01§5 router memakai `Routes, Route` dan `NavLink` | `useRoutes` + RouteObject; nav via Menu+location (router.tsx:15,91-92) | Audit2 |
| S2 | 01§5 daftar antd: Sider, Image/PreviewGroup, List, Card, Descriptions, DatePicker, TimePicker, Divider, Row, Col, Grid; theming `theme.components/algorithm` | tidak dipakai (mobile Drawer; hanya theme.token) | Audit2 |
| S3 | 01§1 daftar stdlib "literal dan exhaustive" | tidak memutakhirkan paket test (httptest, testing, sync/atomic, dst.) | Audit2 |
| S4 | 01§2 `sse.Config{Handler, Retry, OnClose}` | kode sengaja tanpa Retry + `DisableHeartbeat:true` (stream.go:108-124) | Audit2 |
| S5 | 03 F-08: grup asing FAS menampilkan "name/headcount/**status**" | payload {gid,name,own,member_count} tanpa status (mustahil dari skema) — sudah tercatat errata | Audit1 |
| S6 | 06§3: "client polls copy-candidates until copy.completed" | client re-POST /copy key sama tiap 2s (IN_PROGRESS behavior benar) | Audit5/Audit7 |
| S7 | 05§3: daftar data-canonical hanya 4 full-value exception | kode + 02§2c/FL-07 mengesahkan field ke-5 `copy_available` (+ child_id/stid routing keys) — 05§3 usang | Audit6 |
| S8 | 05§7: rev "in-memory counter + created_at fallback" | tanpa created_at fallback; restart → slog + client full-refetch | Audit6 |
| S9 | 05§4: "Default country 62 prefilled in UI **picker**" | tanpa dial-code picker; input biasa + preview +62 | Audit6 |
| S10 | 08/02§6 struktur unit test `src/**/*.test.tsx` | tema kritis di file `.ts` (api/stream/export tests) — include vitest benar | Audit8 |
| S11 | SPEC-INDEX mengklaim "Change 13 = test strategy + A4 fixed" | 07-BEFORE-AFTER berhenti di Change 12 | Audit8 |
| S12 | 01 header sumber "ai.google.dev (Interactions API)" | referensi mati (baris 229 sendiri menyatakan tidak ada interactions endpoint) | Audit2 |
| S13 | 02§1 api `depends_on:[db]` | kode memakai `condition: service_healthy` (lebih ketat) | Audit3 |
| S14 | 02§3 daftar components 9 item | Pager.tsx (PAGE_SIZES) dibutuhkan aturan paging tapi tak terdaftar; kv-session localStorage undocumented | Audit3 |

### 3C. Inkonsistensi internal spec (butuh keputusan: mana yang mengikat)

| # | Konflik | Bukti | Sumber |
|---|---|---|---|
| I1 | 02§2b + 03 F-03 mewajibkan `X-Idempotency-Key` di POST /api/users; 01§2 & 06§3 daftar wajib TIDAK mencantumkannya (kode mengikuti 01/06) | 01:56-59; 06:88-92; users.go:155 | Audit1 |
| I2 | Dua definisi program eligibility soal `is_active`: wire `Eligible` tanpa filter is_active vs gate create sesi wajib is_active program+topik → program lengkap tapi nonaktif: eligible=true tapi create → NOT_FOUND; topik nonaktif tanpa kegiatan: eligible=false tapi create lolos | programs.go:136-160; sessions.go:583-606 | Audit1/Audit4 |
| I3 | 02§2b menutup GET /missions untuk FAS (authed ADMIN) padahal F-12/FL-13 mewajibkan FAS mission picker → FAS selalu FORBIDDEN_ROLE, bank tak pernah terisi | missions.go:144; ReportsPage.tsx:243; MissionPanel.tsx:61-67 | Audit1 |
| I4 | 06§4 "deleted/finished-midway items return per-item WA_FAILED/NOT_FOUND" vs 03 F-12 "send allowed on OPENED and FINISHED" — kode mengikuti 03 (sesi FINISHED di tengah batch tetap terkirim) | reports.go:1323-1325 | Audit7 |
| I5 | 06§5 "No assessments → E17/E18 **with missing list**" vs §1 E18 yang hanya "assess first" — kode E18 tanpa missing list | reports.go:821-822 | Audit7 |
| I6 | 05§1 memasukkan consent_links ke daftar surrogate-less UNIQUE, padahal §2 + DDL memberinya id AUTO_INCREMENT PK | 0001_init.sql:327,336 | Audit6 |
| I7 | 06§1 E7 mendeskripsikan arah SA "stale tab switching schools" → SCHOOL_MISMATCH, tapi kode SA menerima header school valid apa pun (hanya malformed yang error) — arah SA tak dapat diproduksi | auth.go:282-300 | Audit4 |
| I8 | Redaksi urutan upload foto ambigu: 03 F-10 menulis "COUNT FOR UPDATE → write file", 05§7/FL-10 menulis "file durable FIRST" — kode: file → Tx COUNT → publish → 200 (invariant honest-file & cap tetap terpenuhi; publish sedikit sebelum body 200) | photos.go:281-345 | Audit5/Audit6 |
| I9 | 03 F-15 "same school + same program else VALIDATION" vs 04 no-leak — kode source lintas-school → NOT_FOUND (mengikuti no-leak) | sessions.go:2410-2427 | Audit5 |
| I10 | 08 coverage "≥90%" tapi artefak coverage-cov-admin (run parsial) = statements 89.9%/functions 86.3%/branches 79.4%; reporter config kini `text` — artefak tak bisa jadi bukti gate | coverage-cov-admin/coverage-summary.json | Audit8 |
| I11 | 08 pass criteria `tsc --noEmit` — root tsconfig solution-style sehingga perintah telanjang tak memeriksa project app (hanya bermakna dengan `-p tsconfig.app.json`) | tsconfig.json; package.json:9-12 | Audit8 |
| I12 | 01§8 "optional keys fall back to =default" — dua perilaku: GEMINI_MODEL/UPLOAD_DIR/TTLs set-kosong = error boot, OPENWA_*/SEED_* set-kosong = fallback sunyi | config.go:126-141,157-181 | Audit2 |
| I13 | 01§10.4/§9 tidak mencantumkan `jsdom` padahal dipakai vitest environment | vitest.config.ts:12; package.json:31 | Audit2 |

### 3D. Kode berlebih (lebih ketat/luar daftar — konfirmasi atau catat di spec)

- Wipe mengirim juga `consent.changed` di luar triple spec (benign, refetch ekstra) — sessions.go:3160-3164 (Audit4).
- Copy-data men-skip foto bila consent target sudah DITOLAK saat eksekusi (lebih ketat, konsisten F-10) — sessions.go:2884-2890 (Audit5).
- antd Dropdown/Popover/Empty/Segmented + Navigate/useLocation/useRoutes di luar daftar eksaustif 01§5 (daftar memang tidak eksaustif) (Audit2).
- Orphan sweep menambah grace 1 jam + guard defensif di luar teks spec — files.go:22-23,281-290 (Audit6).
- Gate `is_active` saat create sesi (sessions.go:583) = penguatan yang tidak disebut spec F-04 (lihat I2) (Audit4).

---

## 4. Item PERLU KONFIRMASI PENGGUNA (dilarang menebak — dipilih dari seluruh subagent)

**Auth & reset**
1. Reveal reset-code "sekali": kode TIDAK menghapus code_plain setelah reveal (plaintext bertahan sampai use/revoke; reveal berulang selalu ter-audit). Apakah "once" = self-destruct setelah reveal pertama, atau satu aksi reveal ter-audit (kode benar)? (auth.go:1168-1187,1235-1240)
2. Logout saat access expired: route logout di belakang authed() → INVALID_TOKEN, jti tidak di-revoke + cookie refresh sisa technically valid ≤7d. Perlu logout path cookie-only?
3. Reset dashboard mengembalikan superset field (id/revoked/mail_failed di luar {code_masked,expires_at,seconds_left,user}) — konfirmasi spec-usang ringan.
4. "SA read-only retained" (school nonaktif): kode membuat SA membaca data member school nonaktif via X-School-ID kena SCHOOL_INACTIVE (binding 02§4). Jika maksud spec SA boleh READ data member → kode kurang; rekomendasi auditor: SESUAI 02§4.
5. StreamGate mewajibkan `user && school` → SA edge `school:null` tidak buka stream; retry ke-2 INVALID_TOKEN tidak memaksa re-login; rotasi refresh tidak cek school aktif (auth.go:893-895).

**Business rules**
6. "Reports resolve LIVE" (F-05): picks dibaca live, tapi teks misi bank yang sudah di-pick di-snap saat PUT — jika mencakup edit bank pasca-pick, klaim tak terpenuhi.
7. Dua definisi eligibility is_active (I2): gate create = penguatan [kode berlebih] atau F-04 perlu menyebut is_active [spec usang]? Juga: "excluded from picker with reason" (03/08) vs "hidden" (04) — kode menampilkan disabled+alasan.
8. FAS mission picker (I3): buka GET /missions untuk FAS, atau hapus kewajiban picker FAS dari F-12?
9. File badge tidak dihapus bila masih dipakai snapshot (share-check programs.go:102-111) — literal FL-04 vs F-04 "sessions unaffected"; tetapkan yang mengikat.
10. Urutan gate open: grup kosong + tanpa owner serentak → kode memilih VALIDATION (empty menang); redaksi FL-08 menyebut gate owner lebih dulu.
11. "Blank = 24 jam": kode hanya NULL-kan kolom (sepanjang hari), bukan end=start+24h literal.
12. Cascade delete topik/kegiatan setelah program dihapus: kode membiarkannya yatim — perlu cascade?
13. F-11: kode me-render user.md + menempel system.md verbatim (efek identik, system.md tanpa placeholder); ≤50 kata hanya instruksi prompt tanpa enforcement programatik — apakah perlu enforcement?
14. Gate compose server-side: GET P/report tidak pernah digate saat incomplete (public render memang tak boleh digate) — apakah spec menuntut gate server-side untuk compose/send saja?
15. Revoke/regenerate public-link by token tanpa syarat school (gate hanya untuk FASILITATOR) — ADMIN lintas-school yang mengetahui token bisa mencabut; perlu ditindaklanjuti (security follow-up)?
16. Star distribution "per activity/topic" (F-13): kode = distribusi global per school + per-topic aggregate, tanpa histogram per activity; contract shapes.md mem-pin shape global.
17. Copy-data: race source-terhapus SETELAH probe pra-Tx (bisa 200 counts parsial); foto kandidat sumber ikut tersalin sebagai foto biasa (flag tak dibawa — konsisten 05, tapi bertabrakan bacaan literal "candidate TIDAK PERNAH di-copy"); `copy_available` field ke-5 di frame vs "tepat 4 exception".
18. Gate order: validasi body SEBELUM membership gate (non-member + stars invalid → VALIDATION, bukan NOT_FOUND); consent send: membership dicek sebelum FINISHED (non-member di sesi FINISHED → NOT_FOUND) — spec tak menetapkan urutan.
19. 5 file backend belum gofmt — format (perubahan file) atau revisi klaim spek?
20. Coverage frontend ≥90% tak dapat dibuktikan dari artefak parsial; run coverage penuh menulis file (perlu izin terpisah). Oxlint 11 warning: diterima sebagai lulus ("no lint errors") atau warning wajib nol?
21. Operasional: backup host-copy (05-DATA) hanya ada di docs — jadwal/tool/verifikasi tidak ada di repo.
22. Docker live boot ("fresh compose up hanya dengan .env → 4 services, seed, /readyz") hanya diverifikasi statis.

*(Item kecil lain yang sudah diklasifikasikan aman: autocommit satu-statement ≈ satu Tx untuk consent/approve; INSERT tolerate-duplicate untuk copy fill-empty; nama file 32-hex crypto/rand (bukan literal UUID); label BB/MB/BSH/BSB hardcoded dua bahasa; cek len(mission_ids)>4 di luar Tx dengan replace-set atomik; DB_UNAVAILABLE tanpa header Retry-After (konsisten §1); stats.changed/reset.changed publish non-mutasi; gallery retry tanpa timeout 10s.)*

---

## 5. Belum terverifikasi (dengan alasan)

- Isi `.env` (dikecualikan audit; file gitignored) — perlu sekali agar dipastikan service `db` hanya menerima MARIADB_*.
- Runtime behavior live (honest-file di browser, Docker boot, QR pairing) — audit read-only tanpa boot service; diverifikasi statis + test.
- Coverage frontend penuh & pemisahan 100% per critical path backend — butuh run yang menulis artefak (di luar izin audit).

---

## 6. Cakupan subagent bertingkat (semua di-wait sebelum konsolidasi)

- **Audit1ScopeFeatures**: AuditF01Auth, AuditF02F03SchoolUser, AuditF04F05F06Masters, AuditF07F08ChildSession — 4 selesai.
- **Audit2TechStack**: BackendDeps, FrontendDeps, EnvComposeExternal, FrontendLibUsage — 4 selesai.
- **Audit3Architecture**: AuditRoutes, AuditFront, AuditInfra — 3 selesai.
- **Audit4FlowsCore**: AuditFL0103, AuditFL02, AuditFL0405, AuditFL06, AuditFL0708 — 5 selesai.
- **Audit5FeaturesFlows**: AuditF09, AuditF10Photos, AuditF10Consent, AuditF11, AuditF12, AuditF13F14, AuditF15, AuditF16 — 8 selesai.
- **Audit6DataModel**: Spec3Shapes, Spec4WireFE, Spec5Persist, Spec7Tx — 4 selesai.
- **Audit7Errors**: Sec5a, Sec5b, Sec4, Sec3, Sec2 — 5 selesai.
- **Audit8Acceptance**: AuditBackendBlocks, AuditSsePagingTests, AuditChangesDecisions — 3 selesai.

Total: 8 auditor tingkat-1 + 36 subagent bertingkat, seluruhnya diselesaikan dan disinkronkan via wait sebelum laporan ini disusun. Seluruh temuan diverifikasi terhadap berkas aktual (read/grep) — tidak ada temuan tanpa bukti berkas.

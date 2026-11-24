package internal

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// TestPublicTokenInvalidIdenticalShapeAcrossEndpoints: unknown token and
// missing token answer the IDENTICAL TOKEN_INVALID envelope on all three
// GETs — no existence oracle (06 §1 E24).
func TestPublicTokenInvalidIdenticalShapeAcrossEndpoints(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// Unknown token: the shared resolver (003b) probes public_links, then
	// consent_links — for every endpoint, identically (no oracle).
	for range 3 {
		mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
			WithArgs("nope").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
			WithArgs("nope").WillReturnError(sql.ErrNoRows)
	}
	// A missing ?token= fails before any SQL.

	paths := []string{
		"/public/report?token=nope",
		"/public/gallery?token=nope",
		"/public/consent?token=nope",
		"/public/report",
	}
	var messages []string
	for _, p := range paths {
		res := doReq(t, app, "GET", p, "", nil)
		errObj := requireErrEnvelope(t, res, CodeTokenInvalid)
		require.Equal(t, MsgTokenInvalid, errObj["message"],
			"every token failure must look exactly the same: %s", p)
		messages = append(messages, errObj["message"].(string))
	}
	for _, m := range messages {
		require.Equal(t, messages[0], m, "identical shape across endpoints")
	}
}

// TestPublicGalleryInactiveChildWinsOverConsent: a deactivated child locks
// the gallery with reason CHILD_INACTIVE — the consent status is never even
// consulted (inactive wins over CONSENT_DENIED, FL-15).
func TestPublicGalleryInactiveChildWinsOverConsent(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-inact").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"session_topic_id", "child_id", "kind"}).
			AddRow(int64(1), int64(2), int64(5), int64(30), "GALLERY"))
	mock.ExpectQuery(q("SELECT id, name, age, school, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "age", "school", "is_active"}).
			AddRow(30, "Ayu", 8, "SD Bintang", false))
	mock.ExpectQuery(q("SELECT id, name, date FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date"}).
			AddRow(2, "Sesi A", fixedTS))
	// No consent query: the switch short-circuits on the inactive child.

	res := doReq(t, app, "GET", "/public/gallery?token=tok-inact", "", nil)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["locked"])
	require.Equal(t, "CHILD_INACTIVE", data["reason"],
		"inactive must win over the (possible) consent denial")
	require.Empty(t, data["photos"])
	require.Equal(t, float64(30), data["child"].(map[string]any)["id"])
	require.Equal(t, "2026-01-02", data["session"].(map[string]any)["date"])
}

// TestPublicGalleryConsentDeniedLocks: an active child with a rejected
// consent locks with CONSENT_DENIED (still no photo query).
func TestPublicGalleryConsentDeniedLocks(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-deny").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"session_topic_id", "child_id", "kind"}).
			AddRow(int64(1), int64(2), int64(5), int64(30), "GALLERY"))
	mock.ExpectQuery(q("SELECT id, name, age, school, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "age", "school", "is_active"}).
			AddRow(30, "Ayu", 8, "SD Bintang", true))
	mock.ExpectQuery(q("SELECT id, name, date FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date"}).
			AddRow(2, "Sesi A", fixedTS))
	mock.ExpectQuery(q("SELECT status FROM consent_links WHERE session_id = ? AND child_id = ?")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("DITOLAK"))

	res := doReq(t, app, "GET", "/public/gallery?token=tok-deny", "", nil)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["locked"])
	require.Equal(t, "CONSENT_DENIED", data["reason"])
	require.Empty(t, data["photos"])
}

// TestPublicGalleryUnlockedServesDurablePhotos: SETUJU unlocks ALL topics;
// durable rows serve with item-level E29 `missing` flags (present file vs
// gone bytes), never a top-level error.
func TestPublicGalleryUnlockedServesDurablePhotos(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// One durable file exists on disk; its sibling row lost its bytes.
	existsDir := filepath.Join(s.upload, "1", "2", "5", "30")
	require.NoError(t, os.MkdirAll(existsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(existsDir, "exists.jpg"), []byte("x"), 0o644))

	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-ok").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"session_topic_id", "child_id", "kind"}).
			AddRow(int64(1), int64(2), int64(5), int64(30), "GALLERY"))
	mock.ExpectQuery(q("SELECT id, name, age, school, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "age", "school", "is_active"}).
			AddRow(30, "Ayu", 8, "SD Bintang", true))
	mock.ExpectQuery(q("SELECT id, name, date FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date"}).
			AddRow(2, "Sesi A", fixedTS))
	mock.ExpectQuery(q("SELECT status FROM consent_links WHERE session_id = ? AND child_id = ?")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("SETUJU"))
	mock.ExpectQuery(q("AND p.file_ready = TRUE")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "path", "is_candidate",
			"created_at", "stid", "topic"}).
			AddRow(101, "1/2/5/30/exists.jpg", false, fixedTS, 5, "Topik A").
			AddRow(102, "1/2/5/30/gone.jpg", true, fixedTS, 5, "Topik A"))

	res := doReq(t, app, "GET", "/public/gallery?token=tok-ok", "", nil)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, false, data["locked"])
	require.Nil(t, data["reason"])
	photos := data["photos"].([]any)
	require.Len(t, photos, 2)
	first := photos[0].(map[string]any)
	require.Equal(t, float64(101), first["id"])
	require.Equal(t, "/uploads/1/2/5/30/exists.jpg", first["url"])
	require.Equal(t, false, first["is_candidate"])
	_, missing := first["missing"]
	require.False(t, missing, "durable bytes → no missing flag")
	topic := first["topic"].(map[string]any)
	require.Equal(t, float64(5), topic["stid"])
	require.Equal(t, "Topik A", topic["name"])
	second := photos[1].(map[string]any)
	require.Equal(t, true, second["is_candidate"])
	require.Equal(t, true, second["missing"], "row present, bytes gone → item flag")
}

// TestReportDataCanonicalShape replaces the earlier characterization test
// (it documented the nil-Group panic in buildReportData — since fixed by
// scanning the group row into locals before allocating reportJSON.Group).
// It pins the FULL canonical ReportData per contracts-shapes.md on both
// paths: scenario A through auth GET P/report with everything present
// (group, LIVE attestation, narrative, both badges, candidate photo,
// complete=true/missing=[]), scenario B through GET /public/report with the
// nullable set absent (group null, narrative nulls, badges null,
// candidate_photo null, complete=false + missing list).
func TestReportDataCanonicalShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// Candidate bytes must exist on disk or E29 renders null.
	candDir := filepath.Join(s.upload, "1", "2", "5", "30")
	require.NoError(t, os.MkdirAll(candDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(candDir, "cand.jpg"), []byte("x"), 0o644))

	// --- Scenario A: auth GET P/report, full row set ---
	hdr := bearer(mintAccess(t, s, adminUserRow()))
	hdr["X-School-ID"] = "1"
	mock.ExpectQuery(q(sqlUserByID)).
		WithArgs(int64(2)).
		WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT 1 FROM session_topics WHERE id = ? AND session_id = ? AND school_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	// buildReportData — session snapshot.
	mock.ExpectQuery(q("SELECT id, name, date, location, status, program_id, program_name_snap")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date", "location",
			"status", "program_id", "program_name_snap"}).
			AddRow(2, "Sesi A", fixedTS, "Lab Ruang 2", "OPENED", 9, "Program A"))
	// Topic badge snap + live program badge.
	mock.ExpectQuery(q("SELECT topic_name_snap, badge_name_snap, badge_image_snap")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"topic_name_snap", "badge_name_snap", "badge_image_snap"}).
			AddRow("Topik A", "Medali Topik", "badges/topik.png"))
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).
			AddRow("Medali Program", "badges/program.png"))
	// Child PII (by design for the public token endpoint).
	mock.ExpectQuery(q("SELECT name, age, school, mother_name, father_name, guardian_name")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "age", "school",
			"mother_name", "father_name", "guardian_name"}).
			AddRow("Ayu", 8, "SD Bintang", "Ibu Ayu", "Pak Ayu", "Wali Ayu"))
	// Membership row: group + LIVE owner fallback chain.
	mock.ExpectQuery(q("SELECT g.name, g.facilitator_id, g.facilitator_name_snap")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "facilitator_id",
			"facilitator_name_snap"}).
			AddRow("Kelompok Merah", int64(3), "Fasil Snap"))
	mock.ExpectQuery(q("SELECT name FROM users WHERE id = ?")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Kak Sari"))
	// Narrative row.
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"text", "model", "generated_at"}).
			AddRow("Narasi rapor.", "gemini-3.5-flash", fixedTS))
	// Outcomes (activity sort order) + mission picks.
	mock.ExpectQuery(q("a.activity_name_snap, sa.indicator_snap")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id",
			"activity_name_snap", "indicator_snap", "stars", "note"}).
			AddRow(11, "Aktivitas Satu", "Indikator Satu", 4, "Catatan fasilitator"))
	mock.ExpectQuery(q("FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"mission_id",
			"mission_name_snap", "mission_body_snap"}).
			AddRow(7, "Misal Misi", "Body misi"))
	// Candidate photo (bytes exist on disk).
	mock.ExpectQuery(q("AND is_candidate = TRUE AND file_ready = TRUE")).
		WithArgs(int64(2), int64(5), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("1/2/5/30/cand.jpg"))
	// Report builder's completeness banner (002c): assembled from what the
	// builder already fetched — only the activity set + this child's
	// assessments scan run here (badge booleans discarded, so no separate
	// program/topic/badge lookups).
	mock.ExpectQuery(q("SELECT sa.session_topic_id, sa.id, sa.activity_name_snap")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id",
			"activity_name_snap"}).AddRow(5, 11, "Aktivitas Satu"))
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}).AddRow(11))

	res := doReq(t, app, "GET", "/api/sessions/2/topics/5/children/30/report", "", hdr)
	data := requireDataEnvelope(t, res).(map[string]any)

	// session / program / topic (snapshots).
	sess := data["session"].(map[string]any)
	require.Equal(t, float64(2), sess["id"])
	require.Equal(t, "Sesi A", sess["name"])
	require.Equal(t, "2026-01-02", sess["date"])
	require.Equal(t, "Lab Ruang 2", sess["location"])
	require.Equal(t, "OPENED", sess["status"])
	require.Equal(t, "Program A", data["program"].(map[string]any)["name"])
	topic := data["topic"].(map[string]any)
	require.Equal(t, float64(5), topic["stid"])
	require.Equal(t, "Topik A", topic["name"])

	// child (PII by design) + group present.
	child := data["child"].(map[string]any)
	require.Equal(t, float64(30), child["id"])
	require.Equal(t, "Ayu", child["name"])
	require.Equal(t, float64(8), child["age"])
	require.Equal(t, "SD Bintang", child["school"])
	require.Equal(t, "Ibu Ayu", child["mother_name"])
	require.Equal(t, "Pak Ayu", child["father_name"])
	require.Equal(t, "Wali Ayu", child["guardian_name"])
	group, ok := data["group"].(map[string]any)
	require.True(t, ok, "membership row present → group object, not null: %s", res.body)
	require.Equal(t, "Kelompok Merah", group["name"])

	// attestation: LIVE owner name wins over the snap.
	att := data["attestation"].(map[string]any)
	require.Equal(t, "Kak Sari", att["name"])
	require.Equal(t, "Fasilitator", att["role"])

	// narrative present.
	narr := data["narrative"].(map[string]any)
	require.Equal(t, "Narasi rapor.", narr["text"])
	require.Equal(t, "2026-01-02T03:04:05Z", narr["generated_at"])

	// outcomes + missions.
	outs := data["outcomes"].([]any)
	require.Len(t, outs, 1)
	out0 := outs[0].(map[string]any)
	require.Equal(t, float64(11), out0["said"])
	require.Equal(t, "Aktivitas Satu", out0["activity_name"])
	require.Equal(t, "Indikator Satu", out0["indicator"])
	require.Equal(t, float64(4), out0["stars"])
	require.Equal(t, "Catatan fasilitator", out0["note"])
	mis := data["missions"].([]any)
	require.Len(t, mis, 1)
	mis0 := mis[0].(map[string]any)
	require.Equal(t, float64(7), mis0["mission_id"])
	require.Equal(t, "Misal Misi", mis0["name"])
	require.Equal(t, "Body misi", mis0["body"])

	// badges + candidate photo + completeness.
	badges := data["badges"].(map[string]any)
	tb := badges["topic"].(map[string]any)
	require.Equal(t, "Medali Topik", tb["name"])
	require.Equal(t, "/uploads/badges/topik.png", tb["image_url"])
	pb := badges["program"].(map[string]any)
	require.Equal(t, "Medali Program", pb["name"])
	require.Equal(t, "/uploads/badges/program.png", pb["image_url"])
	cand := data["candidate_photo"].(map[string]any)
	require.Equal(t, "/uploads/1/2/5/30/cand.jpg", cand["url"])
	require.Equal(t, true, data["complete"])
	missing := data["missing"].([]any)
	require.Empty(t, missing, "complete report → empty array, never null")

	// --- Scenario B: public GET /public/report, nullable set absent ---
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-null").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"session_topic_id", "child_id", "kind"}).
			AddRow(int64(1), int64(2), int64(5), int64(30), "REPORT"))
	mock.ExpectQuery(q("SELECT id, name, date, location, status, program_id, program_name_snap")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date", "location",
			"status", "program_id", "program_name_snap"}).
			AddRow(2, "Sesi A", fixedTS, "Lab Ruang 2", "OPENED", 9, "Program A"))
	mock.ExpectQuery(q("SELECT topic_name_snap, badge_name_snap, badge_image_snap")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"topic_name_snap", "badge_name_snap", "badge_image_snap"}).
			AddRow("Topik A", nil, nil))
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).
			AddRow(nil, nil))
	mock.ExpectQuery(q("SELECT name, age, school, mother_name, father_name, guardian_name")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "age", "school",
			"mother_name", "father_name", "guardian_name"}).
			AddRow("Ayu", 8, "SD Bintang", "Ibu Ayu", nil, nil))
	// No membership row → group must render null (never a panic).
	mock.ExpectQuery(q("SELECT g.name, g.facilitator_id, g.facilitator_name_snap")).
		WithArgs(int64(2), int64(30)).
		WillReturnError(sql.ErrNoRows)
	// No narrative row.
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnError(sql.ErrNoRows)
	// No assessments / no mission picks / no candidate.
	mock.ExpectQuery(q("a.activity_name_snap, sa.indicator_snap")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id",
			"activity_name_snap", "indicator_snap", "stars", "note"}))
	mock.ExpectQuery(q("FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"mission_id",
			"mission_name_snap", "mission_body_snap"}))
	mock.ExpectQuery(q("AND is_candidate = TRUE AND file_ready = TRUE")).
		WithArgs(int64(2), int64(5), int64(30), int64(1)).
		WillReturnError(sql.ErrNoRows)
	// report builder completeness banner (002c): two unstarred activities →
	// incomplete with a list; only the activity set + child scan run here.
	mock.ExpectQuery(q("SELECT sa.session_topic_id, sa.id, sa.activity_name_snap")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id",
			"activity_name_snap"}).
			AddRow(5, 11, "Aktivitas Satu").
			AddRow(5, 12, "Aktivitas Dua"))
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}))

	res = doReq(t, app, "GET", "/public/report?token=tok-null", "", nil)
	data = requireDataEnvelope(t, res).(map[string]any)

	// group null (contracts: group is nullable).
	raw, ok := data["group"]
	require.True(t, ok, "group key must be present")
	require.Nil(t, raw, "no membership row → group: null, not an object")

	// narrative nulls.
	narr = data["narrative"].(map[string]any)
	require.Nil(t, narr["text"])
	require.Nil(t, narr["generated_at"])

	// badges null (undefined topic pair + undefined program pair).
	badges = data["badges"].(map[string]any)
	require.Nil(t, badges["topic"])
	require.Nil(t, badges["program"])

	// candidate_photo null.
	require.Nil(t, data["candidate_photo"])

	// attestation falls back to the snap (empty here, never null).
	att = data["attestation"].(map[string]any)
	require.Equal(t, "", att["name"])
	require.Equal(t, "Fasilitator", att["role"])

	// complete=false carries the missing list; empty collections stay arrays.
	require.Equal(t, false, data["complete"])
	missing = data["missing"].([]any)
	require.Len(t, missing, 2)
	require.Equal(t, float64(11), missing[0].(map[string]any)["said"])
	require.Equal(t, "Aktivitas Satu", missing[0].(map[string]any)["name"])
	require.Equal(t, float64(12), missing[1].(map[string]any)["said"])
	require.Equal(t, "Aktivitas Dua", missing[1].(map[string]any)["name"])
	require.Empty(t, data["outcomes"].([]any))
	require.Empty(t, data["missions"].([]any))

	// nullables on the child render as null.
	child = data["child"].(map[string]any)
	require.Nil(t, child["father_name"])
	require.Nil(t, child["guardian_name"])
}

// TestPublicConsentShowsDecisionShape: decided vs pending rows render the
// contracts consent payload (nullable respondent fields).
func TestPublicConsentShowsDecisionShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// The token resolves through the shared resolver (003b); the display
	// query keys by the (session_id, child_id) it returned.
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-yes").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
		WithArgs("tok-yes").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"child_id"}).AddRow(int64(1), int64(2), int64(30)))
	mock.ExpectQuery(q("FROM consent_links cl")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "respondent_name",
			"responded_at", "session_name", "date", "location", "child_name", "school"}).
			AddRow("SETUJU", "Ibu Ayu", fixedTS, "Sesi A", fixedTS, "Lab Ruang", "Ayu", "SD Bintang"))
	res := doReq(t, app, "GET", "/public/consent?token=tok-yes", "", nil)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "SETUJU", data["status"])
	require.Equal(t, "Ibu Ayu", data["respondent_name"])
	require.Equal(t, "2026-01-02T03:04:05Z", data["responded_at"])
	sess := data["session"].(map[string]any)
	require.Equal(t, "Sesi A", sess["name"])
	require.Equal(t, "2026-01-02", sess["date"])
	require.Equal(t, "Lab Ruang", sess["location"])
	child := data["child"].(map[string]any)
	require.Equal(t, "Ayu", child["name"])
	require.Equal(t, "SD Bintang", child["school"])

	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-wait").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
		WithArgs("tok-wait").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"child_id"}).AddRow(int64(1), int64(2), int64(30)))
	mock.ExpectQuery(q("FROM consent_links cl")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "respondent_name",
			"responded_at", "session_name", "date", "location", "child_name", "school"}).
			AddRow("PENDING", nil, nil, "Sesi B", fixedTS, "Lab", "Budi", "SD Cerdas"))
	res = doReq(t, app, "GET", "/public/consent?token=tok-wait", "", nil)
	data = requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "PENDING", data["status"])
	require.Nil(t, data["respondent_name"], "never answered → JSON null")
	require.Nil(t, data["responded_at"])
}

// TestConsentRespondDitolakWipesPhotosAndPublishesAck: the first-wins
// decision commits, THEN the child+session photo rows die in their own Tx
// (wipeChildSessionPhotos), and the recorded decision fans out —
// photo.changed + gallery.changed + consent.changed on the auth hub and the
// consent.ack {decision} on the matching public stream.
func TestConsentRespondDitolakWipesPhotosAndPublishesAck(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	adminSub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})
	galSub := covSubscribe(t, s, ScopeSnapshot{Public: true, Kind: "GALLERY",
		Token: "tok-gallery", TokenSession: 2, TokenChild: 30, SchoolID: 1})
	conSub := covSubscribe(t, s, ScopeSnapshot{Public: true, Kind: "CONSENT",
		Token: "tok-consent", TokenSession: 2, TokenChild: 30, SchoolID: 1})

	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-d").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
		WithArgs("tok-d").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"child_id"}).AddRow(int64(1), int64(2), int64(30)))
	mock.ExpectQuery(q("SELECT id FROM consent_links WHERE session_id = ? AND child_id = ?")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), "POST /public/consent/respond", "consent-d1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), "POST /public/consent/respond", "consent-d1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// First-wins flip (1 row).
	mock.ExpectExec(q("SET status = ?, respondent_name = ?, responded_at = NOW()")).
		WithArgs("DITOLAK", "Ibu Ayu", int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// DITOLAK wipes ALL photo rows of this child+session in one Tx.
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT path FROM photos WHERE session_id = ?")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("1/2/5/30/a.jpg"))
	mock.ExpectExec(q("DELETE FROM photos WHERE session_id = ?")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST /public/consent/respond", "consent-d1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := jsonReq(t, app, "POST", "/public/consent/respond",
		map[string]any{"token": "tok-d", "decision": "DITOLAK",
			"respondent_name": "Ibu Ayu"},
		map[string]string{"X-Idempotency-Key": "consent-d1"})
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "DITOLAK", data["decision"])
	_, already := data["already"]
	require.False(t, already, "the first recorded decision carries no already flag")

	// Auth hub sequence: photo wipe signal, public gallery refetch, consent
	// list refetch, then the terminal public ack.
	require.Equal(t, "photo.changed", covNextFrame(t, adminSub, "photo.changed after wipe").Entity)
	require.Equal(t, "gallery.changed", covNextFrame(t, adminSub, "gallery.changed").Entity)
	require.Equal(t, "consent.changed", covNextFrame(t, adminSub, "consent.changed").Entity)
	ackAdmin := covNextFrame(t, adminSub, "consent.ack")
	require.Equal(t, "consent.ack", ackAdmin.Entity)
	require.Equal(t, "DITOLAK", ackAdmin.Decision)
	covNoFrame(t, adminSub, "exactly four signals per DITOLAK respond")

	gal := covNextFrame(t, galSub, "gallery.changed to the public gallery stream")
	require.Equal(t, "gallery.changed", gal.Entity)
	require.NotNil(t, gal.SessionID)
	require.Equal(t, int64(2), *gal.SessionID)
	require.Equal(t, int64(30), gal.ChildID)

	ack := covNextFrame(t, conSub, "consent.ack to the consent stream")
	require.Equal(t, "consent.ack", ack.Entity)
	require.Equal(t, "DITOLAK", ack.Decision)
	covNoFrame(t, conSub, "one ack per decision")
}

// TestPublicReportStaysOpenForInactiveChild: gallery and report are gated
// differently on purpose (audit K15) — the SAME inactive child locks the
// gallery with CHILD_INACTIVE (FL-15), yet a valid REPORT token keeps
// serving the full ReportData: deactivation revokes no link and never hides
// the report. The gallery request below is the fixture's proof that the
// child really is inactive (its query is the one that reads is_active);
// the report request then shows the report path answers 200 regardless.
func TestPublicReportStaysOpenForInactiveChild(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// 1. Gallery token for (school 1, session 2, child 30): is_active=false
	// → locked with CHILD_INACTIVE.
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-inact").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"session_topic_id", "child_id", "kind"}).
			AddRow(int64(1), int64(2), int64(5), int64(30), "GALLERY"))
	mock.ExpectQuery(q("SELECT id, name, age, school, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "age", "school", "is_active"}).
			AddRow(30, "Ayu", 8, "SD Bintang", false))
	mock.ExpectQuery(q("SELECT id, name, date FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date"}).
			AddRow(2, "Sesi A", fixedTS))

	res := doReq(t, app, "GET", "/public/gallery?token=tok-inact", "", nil)
	gdata := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, gdata["locked"])
	require.Equal(t, "CHILD_INACTIVE", gdata["reason"])

	// 2. REPORT token for the SAME child+session: the full ReportData — the
	// report path never reads children.is_active, so nothing gates it.
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-rep").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"session_topic_id", "child_id", "kind"}).
			AddRow(int64(1), int64(2), int64(5), int64(30), "REPORT"))
	mock.ExpectQuery(q("SELECT id, name, date, location, status, program_id, program_name_snap")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date", "location",
			"status", "program_id", "program_name_snap"}).
			AddRow(2, "Sesi A", fixedTS, "Lab Ruang 2", "OPENED", 9, "Program A"))
	mock.ExpectQuery(q("SELECT topic_name_snap, badge_name_snap, badge_image_snap")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"topic_name_snap", "badge_name_snap", "badge_image_snap"}).
			AddRow("Topik A", nil, nil))
	mock.ExpectQuery(q("SELECT badge_name, badge_image_path FROM programs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"badge_name", "badge_image_path"}).
			AddRow(nil, nil))
	mock.ExpectQuery(q("SELECT name, age, school, mother_name, father_name, guardian_name")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "age", "school",
			"mother_name", "father_name", "guardian_name"}).
			AddRow("Ayu", 8, "SD Bintang", "Ibu Ayu", nil, nil))
	// No membership row → group null; no narrative row.
	mock.ExpectQuery(q("SELECT g.name, g.facilitator_id, g.facilitator_name_snap")).
		WithArgs(int64(2), int64(30)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnError(sql.ErrNoRows)
	// No assessments / mission picks / candidate photo.
	mock.ExpectQuery(q("a.activity_name_snap, sa.indicator_snap")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id",
			"activity_name_snap", "indicator_snap", "stars", "note"}))
	mock.ExpectQuery(q("FROM report_missions")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"mission_id",
			"mission_name_snap", "mission_body_snap"}))
	mock.ExpectQuery(q("AND is_candidate = TRUE AND file_ready = TRUE")).
		WithArgs(int64(2), int64(5), int64(30), int64(1)).
		WillReturnError(sql.ErrNoRows)
	// Completeness banner: one activity, no assessments → incomplete.
	mock.ExpectQuery(q("SELECT sa.session_topic_id, sa.id, sa.activity_name_snap")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id",
			"activity_name_snap"}).AddRow(5, 11, "Aktivitas Satu"))
	mock.ExpectQuery(q("SELECT session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_activity_id"}))

	res = doReq(t, app, "GET", "/public/report?token=tok-rep", "", nil)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "Ayu", data["child"].(map[string]any)["name"],
		"the inactive child's public report still renders in full")
	require.Equal(t, "Sesi A", data["session"].(map[string]any)["name"])
	require.Equal(t, false, data["complete"])
	_, hasLock := data["locked"]
	require.False(t, hasLock, "the report payload carries no lock concept")
}

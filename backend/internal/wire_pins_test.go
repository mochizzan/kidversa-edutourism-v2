package internal

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// wire_pins_test.go — finding 005d: key-set pins for the UI-critical wire
// shapes (docs/superskill/task/2026-10-09-contracts-shapes.md), extending
// the mapKeys convention of programs_test/topics_test/dashboard_test from
// 3 to 13 shapes. Each test drives the REAL handler through sqlmock
// fixtures and asserts the EXACT JSON key set (deeply, per nested object),
// so a field rename/add/drop in Go fails here even when every value
// assertion elsewhere still passes. Values are asserted only where they
// prove the shape (nullability / conditional keys).

// ---------------------------------------------------------------------------
// Identity: User object + School object
// ---------------------------------------------------------------------------

// TestWirePinUserObject pins the User wire object of the users list
// (contracts §Users): the 9 contract fields plus the always-present
// (nullable) school_name join column.
func TestWirePinUserObject(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlUsersCount).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("AND u.school_id = ? AND u.role = 'FASILITATOR' ORDER BY id asc, u.id ASC LIMIT ? OFFSET ?")).
		WithArgs(int64(1), 20, 0).
		WillReturnRows(sqlUserItemRow(3, "Fasil", "fas@school.id", "FASILITATOR",
			"APPROVED", true, int64(1), "+628111111111", "Bandung"))

	res := doReq(t, app, "GET", "/api/users", "", hAdmin(t, s))
	rows := requireDataEnvelope(t, res).([]any)
	item := rows[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"id", "name", "email", "role", "decision", "is_active",
		"school_id", "wa_number", "created_at", "school_name",
	}, mapKeys(item), "User object must match contracts-shapes.md exactly")
}

// TestWirePinSchoolObject pins the School wire object (contracts §Schools).
func TestWirePinSchoolObject(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectSAAuth(mock, 0)
	mock.ExpectQuery(q("SELECT COUNT(*) FROM schools WHERE 1=1 AND school_name LIKE ?")).
		WithArgs("%Band%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("FROM schools WHERE 1=1 AND school_name LIKE ? ORDER BY school_name desc, id ASC LIMIT ? OFFSET ?")).
		WithArgs("%Band%", 50, 50).
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
			AddRow(1, "Bandung", "Indonesia", nil, true, fixedTS))

	res := doReq(t, app, "GET",
		"/api/schools?q=Band&sort=school_name&dir=desc&page=2&per_page=50", "", hSABare(t, s))
	m := envelopeMap(t, res.body)
	item := m["data"].([]any)[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"id", "school_name", "country", "city", "is_active", "created_at",
	}, mapKeys(item), "School object must match contracts-shapes.md exactly")
}

// TestWirePinAuthSchoolObject pins the auth-side School wire object emitted
// by login (also refresh/me/switch-school — same schoolJSON): the SAME
// 6-key contracts shape as the /api/schools list item (F-B05).
func TestWirePinAuthSchoolObject(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByEmail).WithArgs("fas@school.id").
		WillReturnRows(sqlFASRow())
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(1)).WillReturnRows(sqlSchoolKidversa())

	res := jsonReq(t, app, "POST", "/api/auth/login",
		map[string]any{"email": "fas@school.id", "password": testPassword}, nil)
	data := requireDataEnvelope(t, res).(map[string]any)
	school := data["school"].(map[string]any)
	require.ElementsMatch(t, []string{
		"id", "school_name", "country", "city", "is_active", "created_at",
	}, mapKeys(school), "auth-side School object must match contracts-shapes.md exactly")
	require.Equal(t, "2026-01-02T03:04:05Z", school["created_at"],
		"created_at must be RFC3339 UTC like the /api/schools list item")
}

// ---------------------------------------------------------------------------
// Session detail: session + topics + activities + groups + scope (deep pin)
// ---------------------------------------------------------------------------

// TestWirePinSessionDetailShape pins the ONE detail response deeply: the
// top-level envelope, the session object, topic/activity rows, the full
// own-group object (owner/consent/members) and the wipe-counts scope.
func TestWirePinSessionDetailShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "program_id",
			"program_name_snap", "name", "description", "date", "start_time",
			"end_time", "location", "status", "created_by", "created_at"}).
			AddRow(2, 9, "Program A", "Sesi A", nil, fixedTS, "09:00", "11:00",
				"Lab", "DRAFT", int64(2), fixedTS))
	// Topic + activity snapshot.
	mock.ExpectQuery(q("FROM session_topics st WHERE st.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_id", "name", "sort_order"}).
			AddRow(5, 9, "Topik A", 1))
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id",
			"activity_id", "name", "indicator", "sort_order"}).
			AddRow(5, 11, 101, "Aktivitas A", "Indikator", 1))
	// Groups → members → owners (full own-group objects for ADMIN).
	mock.ExpectQuery(q("FROM session_groups g")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id"}).
			AddRow(10, "Kelompok Merah", 3))
	mock.ExpectQuery(q("FROM session_members sm")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "child_id", "name",
			"age", "school", "consent"}).
			AddRow(10, 30, "Ayu", 8, "SD Bintang", "SETUJU"))
	mock.ExpectQuery(q("SELECT id, name FROM users")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(3, "Fasil"))
	// Scope wipe counts (9 session-scoped subqueries + the wa_outbox school
	// predicate + public_links probe + outer id + the outer school cond).
	mock.ExpectQuery(q("(SELECT COUNT(*) FROM session_topics WHERE session_id = ?)")).
		WithArgs(int64(2), int64(2), int64(2), int64(2), int64(2), int64(2),
			int64(2), int64(2), int64(2), int64(1), int64(2), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"topics", "groups", "members",
			"assessments", "photos", "narratives", "links_report",
			"links_gallery", "links_consent", "reports_sent"}).
			AddRow(1, 1, 1, 2, 3, 1, 1, 1, 1, 4))

	res := doReq(t, app, "GET", "/api/sessions/2", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.ElementsMatch(t, []string{
		"session", "topics", "groups", "scope",
	}, mapKeys(data), "session detail must carry exactly the contract's 4 sections")

	sess := data["session"].(map[string]any)
	require.ElementsMatch(t, []string{
		"id", "program_id", "program_name", "name", "description", "date",
		"start_time", "end_time", "location", "status", "created_by", "created_at",
	}, mapKeys(sess), "detail session object must match contracts-shapes.md")

	topic := data["topics"].([]any)[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"stid", "topic_id", "name", "sort_order", "activities",
	}, mapKeys(topic), "detail topic object must match contracts-shapes.md")
	act := topic["activities"].([]any)[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"said", "activity_id", "name", "indicator", "sort_order",
	}, mapKeys(act), "detail activity object must match contracts-shapes.md")

	g := data["groups"].([]any)[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"gid", "name", "own", "owner", "owner_missing",
		"member_count", "consent", "members",
	}, mapKeys(g), "full group object must match contracts-shapes.md")
	owner := g["owner"].(map[string]any)
	require.ElementsMatch(t, []string{"id", "name"}, mapKeys(owner),
		"group owner object must match contracts-shapes.md")
	member := g["members"].([]any)[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"child_id", "name", "age", "school", "consent",
	}, mapKeys(member), "group member object must match contracts-shapes.md")

	scope := data["scope"].(map[string]any)
	require.ElementsMatch(t, []string{
		"topics", "groups", "members", "assessments", "photos",
		"narratives", "reports_sent", "links_report",
		"links_gallery", "links_consent",
	}, mapKeys(scope), "wipe-counts scope must match contracts-shapes.md")
}

// TestWirePinSessionDetailForeignGroupShape pins the reduced FAS foreign
// group object of the session detail (user decision 2026-10-11: the
// combined {gid,name,own,member_count,status} shape — contracts' reduction
// plus the parent session's status; session_groups has no status column).
func TestWirePinSessionDetailForeignGroupShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := assessmentFASHeaders(t, s, mock)

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "program_id",
			"program_name_snap", "name", "description", "date", "start_time",
			"end_time", "location", "status", "created_by", "created_at"}).
			AddRow(2, 9, "Program A", "Sesi A", nil, fixedTS, "09:00", "11:00",
				"Lab", "OPENED", int64(2), fixedTS))
	// FAS own scope: group 10 of session 2 → group 11 is foreign.
	mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).AddRow(10, 2))
	// Topic/activity snapshots stay empty — only the group shape is pinned.
	mock.ExpectQuery(q("FROM session_topics st WHERE st.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_id", "name", "sort_order"}))
	mock.ExpectQuery(q("FROM session_activities sa WHERE sa.session_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"session_topic_id", "id",
			"activity_id", "name", "indicator", "sort_order"}))
	// Group assembly: own group full, foreign group reduced.
	mock.ExpectQuery(q("FROM session_groups g")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "facilitator_id"}).
			AddRow(10, "Kelompok Sendiri", 3).
			AddRow(11, "Kelompok Orang", 4))
	mock.ExpectQuery(q("FROM session_members sm")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "child_id", "name",
			"age", "school", "consent"}).
			AddRow(10, 30, "Ayu", 8, "SD", "SETUJU").
			AddRow(11, 31, "Budi", 9, "SD", nil))
	mock.ExpectQuery(q("SELECT id, name FROM users")).
		WithArgs(int64(3), int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(3, "Fasil"))

	res := doReq(t, app, "GET", "/api/sessions/2", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.NotContains(t, data, "scope", "FAS detail omits the wipe-counts scope")

	groups := data["groups"].([]any)
	require.Len(t, groups, 2)
	own := groups[0].(map[string]any)
	require.Equal(t, true, own["own"])
	require.NotContains(t, own, "status",
		"the full own-group shape stays the pinned contracts shape")

	foreign := groups[1].(map[string]any)
	require.ElementsMatch(t, []string{
		"gid", "name", "own", "member_count", "status",
	}, mapKeys(foreign), "reduced foreign group must match the decided shape")
	require.Equal(t, false, foreign["own"])
	require.Equal(t, "Kelompok Orang", foreign["name"])
	require.Equal(t, float64(1), foreign["member_count"])
	require.Equal(t, "OPENED", foreign["status"],
		"reduced status mirrors the parent session's status verbatim")
}

// ---------------------------------------------------------------------------
// Reports matrix row + copy candidate
// ---------------------------------------------------------------------------

// TestWirePinReportMatrixRow pins the per-session ops matrix row
// (contracts §Sessions, GET /api/sessions/:sid/reports).
func TestWirePinReportMatrixRow(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT id, topic_name_snap FROM session_topics")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "topic_name_snap"}).
			AddRow(5, "Topik A"))
	mock.ExpectQuery(q("SELECT id, session_topic_id FROM session_activities")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_topic_id"}).
			AddRow(11, 5).AddRow(12, 5))
	mock.ExpectQuery(q("SELECT sm.child_id, sm.group_id, c.name")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "group_id", "name"}).
			AddRow(30, 10, "Ayu"))
	// Child 30 starred activity 11 only → incomplete row.
	mock.ExpectQuery(q("SELECT child_id, session_activity_id FROM assessments")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_activity_id"}).
			AddRow(30, 11))
	mock.ExpectQuery(q("SELECT child_id, session_topic_id FROM narratives")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id"}).
			AddRow(30, 5))
	mock.ExpectQuery(q("SELECT child_id, session_topic_id, COUNT(*)")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id", "n"}).
			AddRow(30, 5, 2))
	// Durable photos exist but the candidate slot is empty → {false}.
	mock.ExpectQuery(q("SELECT DISTINCT child_id, session_topic_id FROM photos")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id"}).
			AddRow(30, 5))
	mock.ExpectQuery(q("AND is_candidate = TRUE")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id"}))
	mock.ExpectQuery(q("SELECT child_id, session_topic_id, kind, token FROM public_links")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "session_topic_id", "kind", "token"}).
			AddRow(30, 5, "REPORT", "tokR").
			AddRow(30, 5, "GALLERY", "tokG"))

	res := doReq(t, app, "GET", "/api/sessions/2/reports", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 1, "one row per member×topic")
	row := data[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"child_id", "child_name", "session_topic_id", "topic_name",
		"complete", "narrative", "missions", "candidate_photo",
		"report_url", "gallery_url",
	}, mapKeys(row), "report matrix row must match contracts-shapes.md exactly")
}

// TestWirePinCopyCandidate pins the copy candidate item + its counts
// object (contracts §Sessions, GET …/copy-candidates — full array).
func TestWirePinCopyCandidate(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("SELECT school_id, program_id FROM sessions WHERE id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "program_id"}).
			AddRow(int64(1), int64(9)))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("WHERE s.school_id = ? AND s.program_id = ? AND s.id <> ?")).
		WithArgs(int64(30), int64(30), int64(30), int64(30),
			int64(1), int64(9), int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date", "status",
			"assessments", "narratives", "missions", "photos"}).
			AddRow(5, "Sesi Lama", fixedTS, "FINISHED", 4, 1, 0, 2))

	res := doReq(t, app, "GET", "/api/sessions/2/children/30/copy-candidates", "", h)
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 1)
	item := data[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"session_id", "session_name", "session_date", "status", "counts",
	}, mapKeys(item), "copy candidate must match contracts-shapes.md exactly")
	counts := item["counts"].(map[string]any)
	require.ElementsMatch(t, []string{
		"assessments", "narratives", "missions", "photos",
	}, mapKeys(counts), "copy candidate counts must match contracts-shapes.md")
}

// ---------------------------------------------------------------------------
// Consent list item (GET /api/consents — shared consentItem shape)
// ---------------------------------------------------------------------------

// TestWirePinConsentListItem pins the paged consent item for a PENDING row
// (all 7 keys incl. consent_url, which the wire drops once decided).
func TestWirePinConsentListItem(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q("SELECT COUNT(*) FROM consent_links cl")).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("SELECT cl.child_id, c.name, g.name, cl.status, cl.respondent_name")).
		WithArgs(int64(1), 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "name",
			"group_name", "status", "respondent_name", "responded_at", "token"}).
			AddRow(30, "Ayu", "Kelompok Merah", "PENDING", nil, nil, "tokC"))

	res := doReq(t, app, "GET", "/api/consents", "", hAdmin(t, s))
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2, "paged envelope is data+meta only: %s", res.body)
	item := m["data"].([]any)[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"child_id", "child_name", "group_name", "status",
		"respondent_name", "responded_at", "consent_url",
	}, mapKeys(item), "pending consent item must match contracts-shapes.md exactly")
	require.NotNil(t, item["consent_url"], "PENDING rows carry the copy/resend URL")
}

// ---------------------------------------------------------------------------
// Public JSON: gallery envelope + consent envelope
// ---------------------------------------------------------------------------

// TestWirePinPublicGalleryEnvelope pins the public gallery envelope deeply:
// top-level sections, child/session objects and the photo item (the E29
// `missing` flag is conditional: present only for gone-bytes rows).
func TestWirePinPublicGalleryEnvelope(t *testing.T) {
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
	require.ElementsMatch(t, []string{
		"child", "session", "photos", "locked", "reason",
	}, mapKeys(data), "public gallery envelope must match contracts-shapes.md exactly")
	child := data["child"].(map[string]any)
	require.ElementsMatch(t, []string{"id", "name", "age", "school"},
		mapKeys(child), "public gallery child must match contracts-shapes.md")
	sess := data["session"].(map[string]any)
	require.ElementsMatch(t, []string{"id", "name", "date"},
		mapKeys(sess), "public gallery session must match contracts-shapes.md")

	photos := data["photos"].([]any)
	require.Len(t, photos, 2)
	durable := photos[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"id", "url", "topic", "created_at", "is_candidate",
	}, mapKeys(durable), "durable photo item omits the missing flag")
	topic := durable["topic"].(map[string]any)
	require.ElementsMatch(t, []string{"stid", "name"},
		mapKeys(topic), "gallery photo topic must match contracts-shapes.md")
	gone := photos[1].(map[string]any)
	require.ElementsMatch(t, []string{
		"id", "url", "topic", "created_at", "is_candidate", "missing",
	}, mapKeys(gone), "gone-bytes photo carries the E29 missing flag")
	require.Equal(t, true, gone["missing"])
}

// TestWirePinPublicConsentEnvelope pins the public consent envelope and
// its two nested objects (contracts §Public JSON).
func TestWirePinPublicConsentEnvelope(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

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
	require.ElementsMatch(t, []string{
		"session", "child", "status", "respondent_name", "responded_at",
	}, mapKeys(data), "public consent envelope must match contracts-shapes.md exactly")
	sess := data["session"].(map[string]any)
	require.ElementsMatch(t, []string{"name", "date", "location"},
		mapKeys(sess), "public consent session must match contracts-shapes.md")
	child := data["child"].(map[string]any)
	require.ElementsMatch(t, []string{"name", "school"},
		mapKeys(child), "public consent child must match contracts-shapes.md")
	require.NotNil(t, data["respondent_name"], "decided consent carries the respondent")
}

// ---------------------------------------------------------------------------
// Dashboard: overview + analytics
// ---------------------------------------------------------------------------

// TestWirePinDashboardOverview pins the overview envelope + progress rows
// (contracts §Dashboard / WA).
func TestWirePinDashboardOverview(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("(SELECT COUNT(*) FROM children WHERE school_id = ?)")).
		WithArgs(int64(1), int64(1), int64(1), int64(1), int64(1), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"children", "open", "finished",
			"report_links", "narratives", "pending"}).
			AddRow(10, 2, 3, 4, 5, 1))
	mock.ExpectQuery(q("FROM sessions s WHERE s.school_id = ?")).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date",
			"members", "activities", "assessed"}).
			AddRow(41, "Sesi A", fixedTS, 2, 3, 4))

	res := doReq(t, app, "GET", "/api/dashboard/overview", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.ElementsMatch(t, []string{
		"children", "sessions_open", "sessions_finished",
		"reports_issued", "pending", "progress",
	}, mapKeys(data), "dashboard overview must match contracts-shapes.md exactly")
	row := data["progress"].([]any)[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"session_id", "name", "date", "total", "assessed", "percent",
	}, mapKeys(row), "overview progress row must match contracts-shapes.md")
}

// TestWirePinDashboardAnalytics pins the analytics envelope and every row
// type (the sessions row is additionally pinned in dashboard_test.go).
func TestWirePinDashboardAnalytics(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	expectDashboardAnalytics(mock, int64(1), " AND a.updated_at >= NOW() - INTERVAL 7 DAY")

	res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.ElementsMatch(t, []string{
		"period", "star_distribution", "topics", "sessions", "facilitators",
	}, mapKeys(data), "dashboard analytics must match contracts-shapes.md exactly")

	star := data["star_distribution"].([]any)[0].(map[string]any)
	require.ElementsMatch(t, []string{"stars", "count"},
		mapKeys(star), "star_distribution rows must match contracts-shapes.md")

	topic := data["topics"].([]any)[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"topic_id", "name", "assessed", "total", "avg_stars",
	}, mapKeys(topic), "analytics topic rows must match contracts-shapes.md")

	sess := data["sessions"].([]any)[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"session_id", "name", "date", "percent",
	}, mapKeys(sess), "analytics session rows must match contracts-shapes.md")

	fac := data["facilitators"].([]any)[0].(map[string]any)
	require.ElementsMatch(t, []string{"id", "name", "assessments"},
		mapKeys(fac), "analytics facilitator rows must match contracts-shapes.md")
}

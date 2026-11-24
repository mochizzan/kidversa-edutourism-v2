package internal

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// missions_test.go — mission bank (missions.go): envelope guards, paged list
// + topic/program filters with the link query, create/get/patch/delete.
// sqlmock is ORDERED: every request starts with expectAdminAuth (the authed()
// user load), and any query/Tx the test did not declare fails the mock.

// q()-quoted fragments pinned per handler path (unique within each test).
const (
	misCountFrag   = "SELECT COUNT(*) FROM missions m WHERE m.school_id = ?"
	misRowsFrag    = "FROM missions m WHERE m.school_id = ? ORDER BY m.id asc, m.id ASC LIMIT ? OFFSET ?"
	misByIDFrag    = "FROM missions m WHERE m.id = ? AND m.school_id = ?"
	misProgFrag    = "SELECT name FROM programs WHERE id = ? AND school_id = ?"
	misLinkSelFrag = "SELECT mission_id, topic_id, topic_name_snap FROM mission_topics"
	misInsFrag     = "INSERT INTO missions (school_id, program_id, program_name_snap, name)"
	misInsLinkFrag = "INSERT INTO mission_topics (mission_id, topic_id, topic_name_snap, school_id)"
	misUpdFrag     = "UPDATE missions SET program_id = ?, program_name_snap = ?, name = ?"
	misDelLinkFrag = "DELETE FROM mission_topics WHERE mission_id = ? AND school_id = ?"
	misDelFrag     = "DELETE FROM missions WHERE id = ? AND school_id = ?"
)

// misRow builds one missionSelect row (4 columns: missionSelect order).
func misRow(id, programID int64, programName, name string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "program_id", "program_name_snap", "name"}).
		AddRow(id, programID, programName, name)
}

// misLinkRows builds one mission_topics link row (3 columns).
func misLinkRows(missionID, topicID int64, topicName string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"mission_id", "topic_id", "topic_name_snap"}).
		AddRow(missionID, topicID, topicName)
}

// ---------------------------------------------------------------------------
// Envelope guards (06 §1): no token / wrong role / missing school header
// ---------------------------------------------------------------------------

func TestMissionsEnvelopeGuards(t *testing.T) {
	t.Run("no token is INVALID_TOKEN without SQL", func(t *testing.T) {
		s, _ := newTestServer(t)
		app := newTestApp(t, s)
		res := doReq(t, app, "GET", "/api/missions", "", nil)
		requireErrEnvelope(t, res, CodeInvalidToken)
	})

	t.Run("facilitator is FORBIDDEN_ROLE after the user load", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectFASAuth(mock) // role check precedes the school guard: no school query
		res := doReq(t, app, "GET", "/api/missions", "", hFAS(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})

	t.Run("SA without X-School-ID is VALIDATION before the COUNT", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0) // kvSchool 0 → requireSchool; no COUNT may follow
		res := doReq(t, app, "GET", "/api/missions", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})
}

// ---------------------------------------------------------------------------
// List: default sort, 5-column item + topics (setMissionTopics branches)
// ---------------------------------------------------------------------------

func TestMissionsListDefaultSortTopicsAndItemShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misCountFrag)).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(q(misRowsFrag)).WithArgs(int64(1), 20, 0).
		WillReturnRows(misRow(1, 3, "Progam Literasi", "Misi A").
			AddRow(2, 3, "Progam Literasi", "Misi B"))
	// One link query for the whole page; placeholder list grows per mission id.
	mock.ExpectQuery(q(misLinkSelFrag)).WithArgs(int64(1), int64(1), int64(2)).
		WillReturnRows(misLinkRows(1, 5, "Topic Membaca").
			AddRow(1, 6, "Topic Menulis"))

	res := doReq(t, app, "GET", "/api/missions", "", h)
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2, "paged envelope must carry exactly data+meta: %s", res.body)
	require.Equal(t,
		map[string]any{"page": float64(1), "per_page": float64(20), "total": float64(2)},
		m["meta"])
	rows, ok := m["data"].([]any)
	require.True(t, ok, "data must be an array: %s", res.body)
	require.Len(t, rows, 2)

	first := rows[0].(map[string]any)
	require.Len(t, first, 5, "mission item carries exactly the contract fields")
	require.Equal(t, "Misi A", first["name"])
	require.Equal(t, []any{
		map[string]any{"topic_id": float64(5), "name": "Topic Membaca"},
		map[string]any{"topic_id": float64(6), "name": "Topic Menulis"},
	}, first["topics"])

	second := rows[1].(map[string]any)
	topics, ok := second["topics"].([]any)
	require.True(t, ok, "topics must serialise as [] not null: %s", res.body)
	require.Empty(t, topics)
}

func TestMissionsListEmptyPageSkipsLinkQuery(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misCountFrag)).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(q(misRowsFrag)).WithArgs(int64(1), 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "program_id", "program_name_snap", "name",
		}))
	// missionTopics early-returns on zero ids: NO link query at all — a stray
	// expectation-free query fails the mock.

	res := doReq(t, app, "GET", "/api/missions", "", hAdmin(t, s))
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2, "paged envelope must carry exactly data+meta: %s", res.body)
	require.Equal(t,
		map[string]any{"page": float64(1), "per_page": float64(20), "total": float64(0)},
		m["meta"])
	require.Empty(t, m["data"].([]any))
}

// ---------------------------------------------------------------------------
// List: filters (topic EXISTS / program / q) and the sort allowlist
// ---------------------------------------------------------------------------

func TestMissionsListFiltersAndSort(t *testing.T) {
	const (
		countExists = "SELECT COUNT(*) FROM missions m WHERE m.school_id = ? AND EXISTS (SELECT 1 FROM mission_topics mt"
		rowsExists  = "FROM missions m WHERE m.school_id = ? AND EXISTS (SELECT 1 FROM mission_topics mt"
	)
	for _, tc := range []struct {
		name      string
		query     string
		countFrag string
		countArgs []driver.Value
		rowsFrag  string
		rowsArgs  []driver.Value
	}{
		{
			"topic filter is the EXISTS clause",
			"?topic_id=7",
			countExists,
			[]driver.Value{int64(1), int64(7)},
			rowsExists,
			[]driver.Value{int64(1), int64(7), 20, 0},
		},
		{
			"program filter",
			"?program_id=3",
			"SELECT COUNT(*) FROM missions m WHERE m.school_id = ? AND m.program_id = ?",
			[]driver.Value{int64(1), int64(3)},
			"FROM missions m WHERE m.school_id = ? AND m.program_id = ? ORDER BY m.id asc, m.id ASC LIMIT ? OFFSET ?",
			[]driver.Value{int64(1), int64(3), 20, 0},
		},
		{
			"topic and program together keep arg order",
			"?topic_id=7&program_id=3",
			countExists,
			[]driver.Value{int64(1), int64(7), int64(3)},
			rowsExists,
			[]driver.Value{int64(1), int64(7), int64(3), 20, 0},
		},
		{
			"q becomes a name LIKE",
			"?q=Huruf",
			"SELECT COUNT(*) FROM missions m WHERE m.school_id = ? AND m.name LIKE ?",
			[]driver.Value{int64(1), "%Huruf%"},
			"FROM missions m WHERE m.school_id = ? AND m.name LIKE ? ORDER BY m.id asc, m.id ASC LIMIT ? OFFSET ?",
			[]driver.Value{int64(1), "%Huruf%", 20, 0},
		},
		{
			"sort allowlist name desc",
			"?sort=name&dir=desc",
			misCountFrag,
			[]driver.Value{int64(1)},
			"FROM missions m WHERE m.school_id = ? ORDER BY m.name desc, m.id ASC LIMIT ? OFFSET ?",
			[]driver.Value{int64(1), 20, 0},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)

			expectAdminAuth(mock)
			mock.ExpectQuery(q(tc.countFrag)).WithArgs(tc.countArgs...).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			mock.ExpectQuery(q(tc.rowsFrag)).WithArgs(tc.rowsArgs...).
				WillReturnRows(misRow(11, 3, "Progam Literasi", "Misi Sebelas"))
			mock.ExpectQuery(q(misLinkSelFrag)).WithArgs(int64(1), int64(11)).
				WillReturnRows(sqlmock.NewRows([]string{
					"mission_id", "topic_id", "topic_name_snap",
				})) // no links → topics must be []

			res := doReq(t, app, "GET", "/api/missions"+tc.query, "", hAdmin(t, s))
			data := requireDataEnvelope(t, res).([]any)
			require.Len(t, data, 1)
			topics, ok := data[0].(map[string]any)["topics"].([]any)
			require.True(t, ok, "topics must serialise as [] not null")
			require.Empty(t, topics)
		})
	}
}

func TestMissionsListRejectsBadParamsBeforeCount(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	for _, target := range []string{
		"/api/missions?topic_id=abc",   // queryIDFilter: not numeric
		"/api/missions?topic_id=0",     // queryIDFilter: must be > 0
		"/api/missions?program_id=abc", // queryIDFilter: not numeric
		"/api/missions?per_page=7",     // paging allowlist
	} {
		expectAdminAuth(mock) // rejected before the COUNT query
		res := doReq(t, app, "GET", target, "", h)
		requireErrEnvelope(t, res, CodeValidation)
	}
}

func TestMissionsListCountFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misCountFrag)).WithArgs(int64(1)).
		WillReturnError(errors.New("connection refused"))

	res := doReq(t, app, "GET", "/api/missions", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

func TestMissionsListRowsFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misCountFrag)).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q(misRowsFrag)).WithArgs(int64(1), 20, 0).
		WillReturnError(errors.New("dial tcp: connection refused"))

	res := doReq(t, app, "GET", "/api/missions", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// Create: validation before SQL, program/topic gates, dedupe, one Tx
// ---------------------------------------------------------------------------

func TestMissionsCreateRejectsInvalidPayloadBeforeAnySQL(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"program_id zero", map[string]any{"program_id": 0, "name": "Misi", "topic_ids": []int64{5}}},
		{"program_id missing", map[string]any{"name": "Misi", "topic_ids": []int64{5}}},
		{"blank name", map[string]any{"program_id": 3, "name": "  ", "topic_ids": []int64{5}}},
		{"name over 200", map[string]any{"program_id": 3, "name": strings.Repeat("m", 201), "topic_ids": []int64{5}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectAdminAuth(mock) // guard only — no program lookup may follow
			res := jsonReq(t, app, "POST", "/api/missions", tc.body, h)
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

func TestMissionsCreateUnknownProgramIsNotFound(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misProgFrag)).WithArgs(int64(404), int64(1)).
		WillReturnError(sql.ErrNoRows)

	res := jsonReq(t, app, "POST", "/api/missions",
		map[string]any{"program_id": 404, "name": "Misi",
			"topic_ids": []int64{5}}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

func TestMissionsCreateRejectsMissingTopicIDsAfterProgramLookup(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	for _, tc := range []struct {
		name string
		ids  []int64
		omit bool
	}{
		{"absent", nil, true},
		{"empty", []int64{}, false},
		{"only zeros", []int64{0, 0}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectAdminAuth(mock)
			mock.ExpectQuery(q(misProgFrag)).WithArgs(int64(3), int64(1)).
				WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
			// validateMissionTopics rejects before the topics SELECT: no Tx, no INSERT.
			body := map[string]any{"program_id": 3, "name": "Misi"}
			if !tc.omit {
				body["topic_ids"] = tc.ids
			}
			res := jsonReq(t, app, "POST", "/api/missions", body, h)
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

func TestMissionsCreateDedupesTopicsAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misProgFrag)).WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
	// topic_ids [5,5,6] → ONE topics IN query with the deduped args (5,6).
	mock.ExpectQuery(q("SELECT id, name FROM topics WHERE school_id = ? AND id IN (?,?)")).
		WithArgs(int64(1), int64(5), int64(6)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).
			AddRow(int64(5), "Topic Membaca").
			AddRow(int64(6), "Topic Menulis"))
	mock.ExpectBegin()
	mock.ExpectExec(q(misInsFrag)).
		WithArgs(int64(1), int64(3), "Progam Literasi", "Misi Baru").
		WillReturnResult(sqlmock.NewResult(21, 1))
	// Exactly TWO link INSERTs — an undeduped third would fail the mock.
	mock.ExpectExec(q(misInsLinkFrag)).
		WithArgs(int64(21), int64(5), "Topic Membaca", int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q(misInsLinkFrag)).
		WithArgs(int64(21), int64(6), "Topic Menulis", int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(21), int64(1)).
		WillReturnRows(misRow(21, 3, "Progam Literasi", "Misi Baru"))
	mock.ExpectQuery(q(misLinkSelFrag)).WithArgs(int64(1), int64(21)).
		WillReturnRows(misLinkRows(21, 5, "Topic Membaca").
			AddRow(21, 6, "Topic Menulis"))

	res := jsonReq(t, app, "POST", "/api/missions", map[string]any{
		"program_id": 3, "name": "  Misi Baru  ",
		"topic_ids": []int64{5, 5, 6},
	}, hAdmin(t, s))
	mission := requireDataEnvelope(t, res).(map[string]any)["mission"].(map[string]any)
	require.Equal(t, float64(21), mission["id"])
	require.Equal(t, "Misi Baru", mission["name"], "name trimmed before persist")
	require.Equal(t, []any{
		map[string]any{"topic_id": float64(5), "name": "Topic Membaca"},
		map[string]any{"topic_id": float64(6), "name": "Topic Menulis"},
	}, mission["topics"])

	f := takeFrame(t, spy)
	require.Equal(t, "mission.changed", f.Entity)
	require.Equal(t, int64(21), f.EntityID)
	require.Equal(t, "created", f.Action)
	require.Equal(t, int64(1), f.SchoolID)
}

func TestMissionsCreateUnknownTopicIsNotFoundWithoutTx(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misProgFrag)).WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
	// Fewer rows than ids (9 is unknown/cross-school) → NOT_FOUND, no Tx.
	mock.ExpectQuery(q("SELECT id, name FROM topics WHERE school_id = ? AND id IN (?,?)")).
		WithArgs(int64(1), int64(5), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).
			AddRow(int64(5), "Topic Membaca"))

	res := jsonReq(t, app, "POST", "/api/missions", map[string]any{
		"program_id": 3, "name": "Misi", "topic_ids": []int64{5, 9},
	}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

func TestMissionsCreateInsertFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misProgFrag)).WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
	mock.ExpectQuery(q("SELECT id, name FROM topics WHERE school_id = ? AND id IN (?)")).
		WithArgs(int64(1), int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).
			AddRow(int64(5), "Topic Membaca"))
	mock.ExpectBegin()
	mock.ExpectExec(q(misInsFrag)).
		WithArgs(int64(1), int64(3), "Progam Literasi", "Misi").
		WillReturnError(errors.New("disk full"))
	mock.ExpectRollback()

	res := jsonReq(t, app, "POST", "/api/missions", map[string]any{
		"program_id": 3, "name": "Misi", "topic_ids": []int64{5},
	}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

func TestMissionsCreateLinkInsertFailureRollsBack(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misProgFrag)).WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
	mock.ExpectQuery(q("SELECT id, name FROM topics WHERE school_id = ? AND id IN (?)")).
		WithArgs(int64(1), int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).
			AddRow(int64(5), "Topic Membaca"))
	mock.ExpectBegin()
	mock.ExpectExec(q(misInsFrag)).
		WithArgs(int64(1), int64(3), "Progam Literasi", "Misi").
		WillReturnResult(sqlmock.NewResult(22, 1))
	mock.ExpectExec(q(misInsLinkFrag)).
		WithArgs(int64(22), int64(5), "Topic Membaca", int64(1)).
		WillReturnError(errors.New("connection reset"))
	mock.ExpectRollback()

	res := jsonReq(t, app, "POST", "/api/missions", map[string]any{
		"program_id": 3, "name": "Misi", "topic_ids": []int64{5},
	}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// Get: found / unknown before the link query
// ---------------------------------------------------------------------------

func TestMissionsGetDetail(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(9), int64(1)).
			WillReturnRows(misRow(9, 3, "Progam Literasi", "Misi Sembilan"))
		mock.ExpectQuery(q(misLinkSelFrag)).WithArgs(int64(1), int64(9)).
			WillReturnRows(misLinkRows(9, 5, "Topic Membaca"))

		res := doReq(t, app, "GET", "/api/missions/9", "", hAdmin(t, s))
		mission := requireDataEnvelope(t, res).(map[string]any)["mission"].(map[string]any)
		require.Equal(t, float64(9), mission["id"])
		require.Equal(t, "Misi Sembilan", mission["name"])
		require.Equal(t, []any{
			map[string]any{"topic_id": float64(5), "name": "Topic Membaca"},
		}, mission["topics"])
	})

	t.Run("unknown id is NOT_FOUND before the link query", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(404), int64(1)).
			WillReturnError(sql.ErrNoRows)
		// No link query: missionTopics must not run for a missing mission.

		res := doReq(t, app, "GET", "/api/missions/404", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})
}

// ---------------------------------------------------------------------------
// Patch: load first, validation matrix, program reassign, relink in one Tx
// ---------------------------------------------------------------------------

func TestMissionsPatchRejectsInvalidFieldsAfterLoad(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"blank name", map[string]any{"name": "   "}},
		{"name over 200", map[string]any{"name": strings.Repeat("m", 201)}},
		{"program_id zero", map[string]any{"program_id": 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectAdminAuth(mock)
			mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
				WillReturnRows(misRow(1, 1, "Progam Lama", "Misi Lama"))
			// No UPDATE/Tx: a stray write fails the mock.
			res := jsonReq(t, app, "PATCH", "/api/missions/1", tc.body, h)
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

func TestMissionsPatchUnknownIsNotFoundBeforeWrite(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(404), int64(1)).
		WillReturnError(sql.ErrNoRows)
	// No Tx: load precedes every write.

	res := jsonReq(t, app, "PATCH", "/api/missions/404",
		map[string]any{"name": "Baru"}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

func TestMissionsPatchProgramReassignUnknownIsNotFoundWithoutTx(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
		WillReturnRows(misRow(1, 1, "Progam Lama", "Misi Lama"))
	mock.ExpectQuery(q(misProgFrag)).WithArgs(int64(404), int64(1)).
		WillReturnError(sql.ErrNoRows)

	res := jsonReq(t, app, "PATCH", "/api/missions/1",
		map[string]any{"program_id": 404}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

func TestMissionsPatchProgramReassignRefreshesNameSnap(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
		WillReturnRows(misRow(1, 1, "Progam Lama", "Misi Lama"))
	mock.ExpectQuery(q(misProgFrag)).WithArgs(int64(4), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Baru"))
	mock.ExpectBegin()
	mock.ExpectExec(q(misUpdFrag)).
		WithArgs(int64(4), "Progam Baru", "Misi Lama", int64(1), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// topic_ids absent → topics stays nil → NO link DELETE (stray fails mock).
	mock.ExpectCommit()
	mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
		WillReturnRows(misRow(1, 4, "Progam Baru", "Misi Lama"))
	mock.ExpectQuery(q(misLinkSelFrag)).WithArgs(int64(1), int64(1)).
		WillReturnRows(misLinkRows(1, 5, "Topic Membaca"))

	res := jsonReq(t, app, "PATCH", "/api/missions/1",
		map[string]any{"program_id": 4}, hAdmin(t, s))
	mission := requireDataEnvelope(t, res).(map[string]any)["mission"].(map[string]any)
	require.Equal(t, float64(4), mission["program_id"])
	require.Equal(t, "Progam Baru", mission["program_name"],
		"program_name_snap refreshed from the new program row")

	f := takeFrame(t, spy)
	require.Equal(t, "mission.changed", f.Entity)
	require.Equal(t, int64(1), f.EntityID)
	require.Equal(t, "updated", f.Action)
}

func TestMissionsPatchRelinksTopicsInOneTx(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
		WillReturnRows(misRow(1, 1, "Progam Lama", "Misi Lama"))
	mock.ExpectQuery(q("SELECT id, name FROM topics WHERE school_id = ? AND id IN (?)")).
		WithArgs(int64(1), int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).
			AddRow(int64(8), "Topic Pagi"))
	mock.ExpectBegin()
	mock.ExpectExec(q(misUpdFrag)).
		WithArgs(int64(1), "Progam Lama", "Misi Baru", int64(1), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// topic_ids present → whole link set replaced: DELETE then one INSERT.
	mock.ExpectExec(q(misDelLinkFrag)).WithArgs(int64(1), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(q(misInsLinkFrag)).
		WithArgs(int64(1), int64(8), "Topic Pagi", int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
		WillReturnRows(misRow(1, 1, "Progam Lama", "Misi Baru"))
	mock.ExpectQuery(q(misLinkSelFrag)).WithArgs(int64(1), int64(1)).
		WillReturnRows(misLinkRows(1, 8, "Topic Pagi"))

	res := jsonReq(t, app, "PATCH", "/api/missions/1", map[string]any{
		"name": "  Misi Baru  ", "topic_ids": []int64{8},
	}, hAdmin(t, s))
	mission := requireDataEnvelope(t, res).(map[string]any)["mission"].(map[string]any)
	require.Equal(t, "Misi Baru", mission["name"], "name trimmed before persist")
	require.Equal(t, []any{
		map[string]any{"topic_id": float64(8), "name": "Topic Pagi"},
	}, mission["topics"])

	f := takeFrame(t, spy)
	require.Equal(t, "mission.changed", f.Entity)
	require.Equal(t, "updated", f.Action)
}

func TestMissionsPatchTopicIDSGatesBeforeTx(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
		code string
	}{
		{"empty topic_ids is VALIDATION", map[string]any{"topic_ids": []int64{}}, CodeValidation},
		{"zero-only topic_ids is VALIDATION", map[string]any{"topic_ids": []int64{0}}, CodeValidation},
		{"unknown topic is NOT_FOUND", map[string]any{"topic_ids": []int64{99}}, CodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)

			expectAdminAuth(mock)
			mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
				WillReturnRows(misRow(1, 1, "Progam Lama", "Misi Lama"))
			if tc.code == CodeNotFound {
				// Only the unknown-topic case reaches the topics SELECT.
				mock.ExpectQuery(q("SELECT id, name FROM topics WHERE school_id = ? AND id IN (?)")).
					WithArgs(int64(1), int64(99)).
					WillReturnRows(sqlmock.NewRows([]string{"id", "name"}))
			}
			// No Tx in either branch.

			res := jsonReq(t, app, "PATCH", "/api/missions/1", tc.body, hAdmin(t, s))
			requireErrEnvelope(t, res, tc.code)
		})
	}
}

func TestMissionsPatchUpdateFailureRollsBack(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
		WillReturnRows(misRow(1, 1, "Progam Lama", "Misi Lama"))
	mock.ExpectBegin()
	mock.ExpectExec(q(misUpdFrag)).
		WithArgs(int64(1), "Progam Lama", "Misi Baru", int64(1), int64(1)).
		WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()

	res := jsonReq(t, app, "PATCH", "/api/missions/1",
		map[string]any{"name": "Misi Baru"}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// Delete: one Tx (links + row + commit) → publish → {deleted:true}
// ---------------------------------------------------------------------------

func TestMissionsDeleteRunsOneTxAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
		WillReturnRows(misRow(1, 1, "Progam Lama", "Misi Lama"))
	mock.ExpectBegin()
	mock.ExpectExec(q(misDelLinkFrag)).WithArgs(int64(1), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(q(misDelFrag)).WithArgs(int64(1), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := doReq(t, app, "DELETE", "/api/missions/1", "", hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])

	f := takeFrame(t, spy)
	require.Equal(t, "mission.changed", f.Entity)
	require.Equal(t, int64(1), f.EntityID)
	require.Equal(t, "deleted", f.Action)
	require.Equal(t, int64(1), f.SchoolID)
}

func TestMissionsDeleteUnknownIsNotFoundWithoutTx(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(404), int64(1)).
		WillReturnError(sql.ErrNoRows)
	// No Tx: a stray Begin would fail the mock.

	res := doReq(t, app, "DELETE", "/api/missions/404", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

func TestMissionsDeleteRowFailureRollsBack(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
		WillReturnRows(misRow(1, 1, "Progam Lama", "Misi Lama"))
	mock.ExpectBegin()
	mock.ExpectExec(q(misDelLinkFrag)).WithArgs(int64(1), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(q(misDelFrag)).WithArgs(int64(1), int64(1)).
		WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()

	res := doReq(t, app, "DELETE", "/api/missions/1", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

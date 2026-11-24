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

// activities_test.go — activity master (activities.go): envelope guards,
// paged list + filters, create/get/patch/delete. sqlmock is ORDERED: every
// authenticated request first runs authed()'s user load (expectAdminAuth),
// and any query/Exec the test did not declare fails the mock — so a missing
// expectation is itself a "no SQL may happen here" assertion.

// q()-quoted fragments pinned per handler path (unique within each test).
const (
	actCountFrag = "SELECT COUNT(*) FROM activities WHERE school_id = ?"
	actRowsFrag  = "FROM activities WHERE school_id = ? ORDER BY sort_order asc, id ASC LIMIT ? OFFSET ?"
	actByIDFrag  = "sort_order FROM activities WHERE id = ? AND school_id = ?"
	actProgFrag  = "SELECT name FROM programs WHERE id = ? AND school_id = ?"
	actTopicFrag = "SELECT name, program_id FROM topics WHERE id = ? AND school_id = ?"
	actMaxFrag   = "SELECT COALESCE(MAX(sort_order), 0) + 1 FROM activities"
	actInsFrag   = "INSERT INTO activities (school_id, program_id, topic_id, program_name_snap, topic_name_snap,"
	actUpdFrag   = "UPDATE activities SET name = ?, indicator = ?, sort_order = ?"
	actDelFrag   = "DELETE FROM activities WHERE id = ? AND school_id = ?"
)

// actRow builds one activitySelect row (8 columns: activitySelect order).
func actRow(id, programID, topicID int64, programName, topicName, name string,
	indicator any, sort int) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "program_id", "topic_id", "program_name_snap", "topic_name_snap",
		"name", "indicator", "sort_order",
	}).AddRow(id, programID, topicID, programName, topicName, name, indicator, sort)
}

// ---------------------------------------------------------------------------
// Envelope guards (06 §1): no token / wrong role / missing school header
// ---------------------------------------------------------------------------

func TestActivitiesEnvelopeGuards(t *testing.T) {
	t.Run("no token is INVALID_TOKEN without SQL", func(t *testing.T) {
		s, _ := newTestServer(t)
		app := newTestApp(t, s)
		res := doReq(t, app, "GET", "/api/activities", "", nil)
		requireErrEnvelope(t, res, CodeInvalidToken)
	})

	t.Run("facilitator is FORBIDDEN_ROLE after the user load", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectFASAuth(mock) // role check precedes the school guard: no school query
		res := doReq(t, app, "GET", "/api/activities", "", hFAS(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})

	t.Run("SA without X-School-ID is VALIDATION before the COUNT", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0) // kvSchool 0 → requireSchool; no COUNT may follow
		res := doReq(t, app, "GET", "/api/activities", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})
}

// ---------------------------------------------------------------------------
// List: default sort, 8-column item shape, meta, NULL indicator
// ---------------------------------------------------------------------------

func TestActivitiesListDefaultSortAndItemShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actCountFrag)).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(q(actRowsFrag)).WithArgs(int64(1), 20, 0).
		WillReturnRows(actRow(11, 3, 7, "Progam Literasi", "Topic Membaca",
			"Membaca Dimulai", "Bisa membaca lancar", 1).
			AddRow(12, 3, 7, "Progam Literasi", "Topic Membaca",
				"Membaca Lanjut", nil, 2))

	res := doReq(t, app, "GET", "/api/activities", "", h)
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
	require.Len(t, first, 8, "activity item carries exactly the contract fields")
	require.Equal(t, map[string]any{
		"id": float64(11), "program_id": float64(3), "topic_id": float64(7),
		"program_name": "Progam Literasi", "topic_name": "Topic Membaca",
		"name": "Membaca Dimulai", "indicator": "Bisa membaca lancar",
		"sort_order": float64(1),
	}, first)

	second := rows[1].(map[string]any)
	require.Nil(t, second["indicator"], "NULL indicator serialises as JSON null")
}

// ---------------------------------------------------------------------------
// List: filters (topic/program/q) and the sort allowlist
// ---------------------------------------------------------------------------

func TestActivitiesListFiltersAndSort(t *testing.T) {
	for _, tc := range []struct {
		name      string
		query     string
		countFrag string
		countArgs []driver.Value
		rowsFrag  string
		rowsArgs  []driver.Value
	}{
		{
			"topic filter",
			"?topic_id=7",
			"SELECT COUNT(*) FROM activities WHERE school_id = ? AND topic_id = ?",
			[]driver.Value{int64(1), int64(7)},
			"FROM activities WHERE school_id = ? AND topic_id = ? ORDER BY sort_order asc, id ASC LIMIT ? OFFSET ?",
			[]driver.Value{int64(1), int64(7), 20, 0},
		},
		{
			"program filter",
			"?program_id=3",
			"SELECT COUNT(*) FROM activities WHERE school_id = ? AND program_id = ?",
			[]driver.Value{int64(1), int64(3)},
			"FROM activities WHERE school_id = ? AND program_id = ? ORDER BY sort_order asc, id ASC LIMIT ? OFFSET ?",
			[]driver.Value{int64(1), int64(3), 20, 0},
		},
		{
			"topic and program together keep arg order",
			"?topic_id=7&program_id=3",
			"SELECT COUNT(*) FROM activities WHERE school_id = ? AND topic_id = ? AND program_id = ?",
			[]driver.Value{int64(1), int64(7), int64(3)},
			"FROM activities WHERE school_id = ? AND topic_id = ? AND program_id = ? ORDER BY sort_order asc, id ASC LIMIT ? OFFSET ?",
			[]driver.Value{int64(1), int64(7), int64(3), 20, 0},
		},
		{
			"q becomes a name LIKE",
			"?q=Huruf",
			"SELECT COUNT(*) FROM activities WHERE school_id = ? AND name LIKE ?",
			[]driver.Value{int64(1), "%Huruf%"},
			"FROM activities WHERE school_id = ? AND name LIKE ? ORDER BY sort_order asc, id ASC LIMIT ? OFFSET ?",
			[]driver.Value{int64(1), "%Huruf%", 20, 0},
		},
		{
			"sort allowlist name desc",
			"?sort=name&dir=desc",
			actCountFrag,
			[]driver.Value{int64(1)},
			"FROM activities WHERE school_id = ? ORDER BY name desc, id ASC LIMIT ? OFFSET ?",
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
				WillReturnRows(actRow(11, 3, 7, "Progam Literasi", "Topic Membaca",
					"Huruf A", nil, 1))

			res := doReq(t, app, "GET", "/api/activities"+tc.query, "", hAdmin(t, s))
			data := requireDataEnvelope(t, res).([]any)
			require.Len(t, data, 1)
		})
	}
}

func TestActivitiesListRejectsBadParamsBeforeCount(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	for _, target := range []string{
		"/api/activities?topic_id=abc",   // queryIDFilter: not numeric
		"/api/activities?topic_id=0",     // queryIDFilter: must be > 0
		"/api/activities?program_id=abc", // queryIDFilter: not numeric
		"/api/activities?program_id=-2",  // queryIDFilter: must be > 0
		"/api/activities?per_page=7",     // paging allowlist
	} {
		expectAdminAuth(mock) // rejected before the COUNT query
		res := doReq(t, app, "GET", target, "", h)
		requireErrEnvelope(t, res, CodeValidation)
	}
}

func TestActivitiesListCountFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actCountFrag)).WithArgs(int64(1)).
		WillReturnError(errors.New("connection refused"))

	res := doReq(t, app, "GET", "/api/activities", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

func TestActivitiesListRowsFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actCountFrag)).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q(actRowsFrag)).WithArgs(int64(1), 20, 0).
		WillReturnError(errors.New("dial tcp: connection refused"))

	res := doReq(t, app, "GET", "/api/activities", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// Create: validation before any SQL, lookup failures, append sort, success
// ---------------------------------------------------------------------------

func TestActivitiesCreateRejectsInvalidPayloadBeforeAnySQL(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"program_id zero", map[string]any{"program_id": 0, "topic_id": 7, "name": "Kegiatan"}},
		{"program_id missing", map[string]any{"topic_id": 7, "name": "Kegiatan"}},
		{"topic_id zero", map[string]any{"program_id": 3, "topic_id": 0, "name": "Kegiatan"}},
		{"blank name", map[string]any{"program_id": 3, "topic_id": 7, "name": "   "}},
		{"name over 200", map[string]any{"program_id": 3, "topic_id": 7, "name": strings.Repeat("k", 201)}},
		{"negative sort_order", map[string]any{"program_id": 3, "topic_id": 7, "name": "Kegiatan", "sort_order": -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectAdminAuth(mock) // guard only — no lookups/INSERT may follow
			res := jsonReq(t, app, "POST", "/api/activities", tc.body, h)
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

func TestActivitiesCreateUnknownProgramIsNotFound(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actProgFrag)).WithArgs(int64(404), int64(1)).
		WillReturnError(sql.ErrNoRows)

	res := jsonReq(t, app, "POST", "/api/activities",
		map[string]any{"program_id": 404, "topic_id": 7, "name": "Kegiatan"}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

func TestActivitiesCreateUnknownTopicIsNotFound(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actProgFrag)).WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
	mock.ExpectQuery(q(actTopicFrag)).WithArgs(int64(404), int64(1)).
		WillReturnError(sql.ErrNoRows)

	res := jsonReq(t, app, "POST", "/api/activities",
		map[string]any{"program_id": 3, "topic_id": 404, "name": "Kegiatan"}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

func TestActivitiesCreateTopicOutOfProgramIsValidation(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actProgFrag)).WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
	mock.ExpectQuery(q(actTopicFrag)).WithArgs(int64(7), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "program_id"}).
			AddRow("Topic Lain", int64(9))) // topic belongs to program 9, not 3
	// No MAX query, no INSERT: a stray write fails the mock.

	res := jsonReq(t, app, "POST", "/api/activities",
		map[string]any{"program_id": 3, "topic_id": 7, "name": "Kegiatan"}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeValidation)
}

func TestActivitiesCreateAppendsSortOrderAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actProgFrag)).WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
	mock.ExpectQuery(q(actTopicFrag)).WithArgs(int64(7), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "program_id"}).
			AddRow("Topic Membaca", int64(3)))
	// sort_order omitted → append = MAX+1 inside the topic.
	mock.ExpectQuery(q(actMaxFrag)).WithArgs(int64(7), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(4))
	mock.ExpectExec(q(actInsFrag)).
		WithArgs(int64(1), int64(3), int64(7), "Progam Literasi", "Topic Membaca",
			"Kegiatan Baru", nil, 4). // indicator omitted → NULL
		WillReturnResult(sqlmock.NewResult(11, 1))
	mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(11), int64(1)).
		WillReturnRows(actRow(11, 3, 7, "Progam Literasi", "Topic Membaca",
			"Kegiatan Baru", nil, 4))

	res := jsonReq(t, app, "POST", "/api/activities",
		map[string]any{"program_id": 3, "topic_id": 7, "name": "  Kegiatan Baru  "},
		hAdmin(t, s))
	item := requireDataEnvelope(t, res).(map[string]any)["activity"].(map[string]any)
	require.Equal(t, float64(11), item["id"])
	require.Equal(t, "Kegiatan Baru", item["name"], "name is trimmed before persist")
	require.Nil(t, item["indicator"])
	require.Equal(t, float64(4), item["sort_order"])

	f := takeFrame(t, spy)
	require.Equal(t, "activity.changed", f.Entity)
	require.Equal(t, int64(11), f.EntityID)
	require.Equal(t, "created", f.Action)
	require.Equal(t, int64(1), f.SchoolID)
}

func TestActivitiesCreateExplicitSortAndIndicator(t *testing.T) {
	t.Run("explicit sort skips the MAX query and stores the indicator", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectAdminAuth(mock)
		mock.ExpectQuery(q(actProgFrag)).WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
		mock.ExpectQuery(q(actTopicFrag)).WithArgs(int64(7), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "program_id"}).
				AddRow("Topic Membaca", int64(3)))
		mock.ExpectExec(q(actInsFrag)).
			WithArgs(int64(1), int64(3), int64(7), "Progam Literasi", "Topic Membaca",
				"Kegiatan 0", "Bisa membaca", 0).
			WillReturnResult(sqlmock.NewResult(13, 1))
		mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(13), int64(1)).
			WillReturnRows(actRow(13, 3, 7, "Progam Literasi", "Topic Membaca",
				"Kegiatan 0", "Bisa membaca", 0))

		res := jsonReq(t, app, "POST", "/api/activities",
			map[string]any{"program_id": 3, "topic_id": 7, "name": "Kegiatan 0",
				"sort_order": 0, "indicator": "Bisa membaca"}, hAdmin(t, s))
		item := requireDataEnvelope(t, res).(map[string]any)["activity"].(map[string]any)
		require.Equal(t, float64(0), item["sort_order"])
		require.Equal(t, "Bisa membaca", item["indicator"])
	})

	t.Run("empty indicator persists as NULL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectAdminAuth(mock)
		mock.ExpectQuery(q(actProgFrag)).WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
		mock.ExpectQuery(q(actTopicFrag)).WithArgs(int64(7), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "program_id"}).
				AddRow("Topic Membaca", int64(3)))
		mock.ExpectExec(q(actInsFrag)).
			WithArgs(int64(1), int64(3), int64(7), "Progam Literasi", "Topic Membaca",
				"Kegiatan Kosong", nil, 1).
			WillReturnResult(sqlmock.NewResult(14, 1))
		mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(14), int64(1)).
			WillReturnRows(actRow(14, 3, 7, "Progam Literasi", "Topic Membaca",
				"Kegiatan Kosong", nil, 1))

		res := jsonReq(t, app, "POST", "/api/activities",
			map[string]any{"program_id": 3, "topic_id": 7, "name": "Kegiatan Kosong",
				"sort_order": 1, "indicator": ""}, hAdmin(t, s))
		item := requireDataEnvelope(t, res).(map[string]any)["activity"].(map[string]any)
		require.Nil(t, item["indicator"])
	})
}

func TestActivitiesCreateInsertFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actProgFrag)).WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
	mock.ExpectQuery(q(actTopicFrag)).WithArgs(int64(7), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "program_id"}).
			AddRow("Topic Membaca", int64(3)))
	mock.ExpectExec(q(actInsFrag)).
		WithArgs(int64(1), int64(3), int64(7), "Progam Literasi", "Topic Membaca",
			"Kegiatan", nil, 1).
		WillReturnError(errors.New("disk full"))

	res := jsonReq(t, app, "POST", "/api/activities",
		map[string]any{"program_id": 3, "topic_id": 7, "name": "Kegiatan", "sort_order": 1},
		hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

func TestActivitiesCreateReloadFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actProgFrag)).WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
	mock.ExpectQuery(q(actTopicFrag)).WithArgs(int64(7), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "program_id"}).
			AddRow("Topic Membaca", int64(3)))
	mock.ExpectExec(q(actInsFrag)).
		WithArgs(int64(1), int64(3), int64(7), "Progam Literasi", "Topic Membaca",
			"Kegiatan", nil, 1).
		WillReturnResult(sqlmock.NewResult(15, 1))
	mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(15), int64(1)).
		WillReturnError(errors.New("connection reset"))

	res := jsonReq(t, app, "POST", "/api/activities",
		map[string]any{"program_id": 3, "topic_id": 7, "name": "Kegiatan", "sort_order": 1},
		hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// Get: found / unknown
// ---------------------------------------------------------------------------

func TestActivitiesGetDetail(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(5), int64(1)).
			WillReturnRows(actRow(5, 3, 7, "Progam Literasi", "Topic Membaca",
				"Kegiatan Lima", "Indikator", 2))

		res := doReq(t, app, "GET", "/api/activities/5", "", hAdmin(t, s))
		item := requireDataEnvelope(t, res).(map[string]any)["activity"].(map[string]any)
		require.Equal(t, float64(5), item["id"])
		require.Equal(t, "Kegiatan Lima", item["name"])
		require.Equal(t, "Indikator", item["indicator"])
	})

	t.Run("unknown id is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(404), int64(1)).
			WillReturnError(sql.ErrNoRows)

		res := doReq(t, app, "GET", "/api/activities/404", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})
}

// ---------------------------------------------------------------------------
// Patch: load first, validation, indicator clear, success, failures
// ---------------------------------------------------------------------------

func TestActivitiesPatchRejectsInvalidFieldsAfterLoad(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := hAdmin(t, s)

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"blank name", map[string]any{"name": "   "}},
		{"name over 200", map[string]any{"name": strings.Repeat("x", 201)}},
		{"negative sort_order", map[string]any{"sort_order": -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectAdminAuth(mock)
			mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(5), int64(1)).
				WillReturnRows(actRow(5, 3, 7, "Progam Literasi", "Topic Membaca",
					"Lama", "Ind lama", 1))
			// No UPDATE expectation: a stray write fails the mock.
			res := jsonReq(t, app, "PATCH", "/api/activities/5", tc.body, h)
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

func TestActivitiesPatchUnknownIsNotFoundBeforeWrite(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(404), int64(1)).
		WillReturnError(sql.ErrNoRows)
	// No UPDATE: load precedes every write.

	res := jsonReq(t, app, "PATCH", "/api/activities/404",
		map[string]any{"name": "Baru"}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

func TestActivitiesPatchUpdatesAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(5), int64(1)).
		WillReturnRows(actRow(5, 3, 7, "Progam Literasi", "Topic Membaca",
			"Lama", "Ind lama", 1))
	mock.ExpectExec(q(actUpdFrag)).
		WithArgs("Baru", "Ind lama", 9, int64(5), int64(1)). // indicator untouched
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(5), int64(1)).
		WillReturnRows(actRow(5, 3, 7, "Progam Literasi", "Topic Membaca",
			"Baru", "Ind lama", 9))

	res := jsonReq(t, app, "PATCH", "/api/activities/5",
		map[string]any{"name": "  Baru ", "sort_order": 9}, hAdmin(t, s))
	item := requireDataEnvelope(t, res).(map[string]any)["activity"].(map[string]any)
	require.Equal(t, "Baru", item["name"], "name trimmed before persist")
	require.Equal(t, float64(9), item["sort_order"])
	require.Equal(t, "Ind lama", item["indicator"])

	f := takeFrame(t, spy)
	require.Equal(t, "activity.changed", f.Entity)
	require.Equal(t, int64(5), f.EntityID)
	require.Equal(t, "updated", f.Action)
}

func TestActivitiesPatchClearsIndicatorToNull(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(5), int64(1)).
		WillReturnRows(actRow(5, 3, 7, "Progam Literasi", "Topic Membaca",
			"Lama", "Ind lama", 1))
	// indicator "" on the wire → SQL NULL on the wire-back.
	mock.ExpectExec(q(actUpdFrag)).
		WithArgs("Lama", nil, 1, int64(5), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(5), int64(1)).
		WillReturnRows(actRow(5, 3, 7, "Progam Literasi", "Topic Membaca",
			"Lama", nil, 1))

	res := jsonReq(t, app, "PATCH", "/api/activities/5",
		map[string]any{"indicator": ""}, hAdmin(t, s))
	item := requireDataEnvelope(t, res).(map[string]any)["activity"].(map[string]any)
	require.Nil(t, item["indicator"], "cleared indicator reloads as JSON null")
}

func TestActivitiesPatchUpdateFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(5), int64(1)).
		WillReturnRows(actRow(5, 3, 7, "Progam Literasi", "Topic Membaca",
			"Lama", nil, 1))
	mock.ExpectExec(q(actUpdFrag)).
		WithArgs("Baru", nil, 1, int64(5), int64(1)).
		WillReturnError(errors.New("connection refused"))

	res := jsonReq(t, app, "PATCH", "/api/activities/5",
		map[string]any{"name": "Baru"}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// Delete: single DELETE → publish → {deleted:true}; unknown → no write
// ---------------------------------------------------------------------------

func TestActivitiesDeleteRemovesRowAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(5), int64(1)).
		WillReturnRows(actRow(5, 3, 7, "Progam Literasi", "Topic Membaca",
			"Lama", nil, 1))
	mock.ExpectExec(q(actDelFrag)).WithArgs(int64(5), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := doReq(t, app, "DELETE", "/api/activities/5", "", hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])

	f := takeFrame(t, spy)
	require.Equal(t, "activity.changed", f.Entity)
	require.Equal(t, int64(5), f.EntityID)
	require.Equal(t, "deleted", f.Action)
}

func TestActivitiesDeleteUnknownIsNotFoundWithoutWrite(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(404), int64(1)).
		WillReturnError(sql.ErrNoRows)
	// No DELETE expectation: a stray write fails the mock.

	res := doReq(t, app, "DELETE", "/api/activities/404", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

func TestActivitiesDeleteFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(5), int64(1)).
		WillReturnRows(actRow(5, 3, 7, "Progam Literasi", "Topic Membaca",
			"Lama", nil, 1))
	mock.ExpectExec(q(actDelFrag)).WithArgs(int64(5), int64(1)).
		WillReturnError(errors.New("connection refused"))

	res := doReq(t, app, "DELETE", "/api/activities/5", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestActivitiesRacedReloadIsNotFound pins the cross-domain reload guard
// (programs/topics/missions/frames share it): a row that vanishes between the
// write and its reload answers NOT_FOUND, never a null-item 200.
func TestActivitiesRacedReloadIsNotFound(t *testing.T) {
	t.Run("raced delete after insert", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		mock.ExpectQuery(q(actProgFrag)).WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
		mock.ExpectQuery(q(actTopicFrag)).WithArgs(int64(7), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "program_id"}).
				AddRow("Topic Membaca", int64(3)))
		mock.ExpectExec(q(actInsFrag)).
			WithArgs(int64(1), int64(3), int64(7), "Progam Literasi", "Topic Membaca",
				"Kegiatan", nil, 1).
			WillReturnResult(sqlmock.NewResult(15, 1))
		mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(15), int64(1)).
			WillReturnError(sql.ErrNoRows) // raced concurrent delete

		res := jsonReq(t, app, "POST", "/api/activities",
			map[string]any{"program_id": 3, "topic_id": 7, "name": "Kegiatan", "sort_order": 1},
			hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
		requireNoFrame(t, spy)
	})

	t.Run("raced delete after update", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(5), int64(1)).
			WillReturnRows(actRow(5, 3, 7, "Progam Literasi", "Topic Membaca",
				"Lama", nil, 1))
		mock.ExpectExec(q(actUpdFrag)).WithArgs("Baru", nil, 1, int64(5), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(5), int64(1)).
			WillReturnError(sql.ErrNoRows) // raced concurrent delete

		res := jsonReq(t, app, "PATCH", "/api/activities/5",
			map[string]any{"name": "Baru"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
		requireNoFrame(t, spy)
	})
}

package internal

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// topics_test.go — topic master (topics.go): envelope guards, paged list with
// the queryIDFilter program_id guard, multipart create (program existence +
// sort_order default-append + badge rules), patch and delete with the badge
// file lifecycle. Fixtures come from helpers_test.go / schools_test.go.

// ---------------------------------------------------------------------------
// Local fixtures (unique to this file)
// ---------------------------------------------------------------------------

// Distinctive single-line SQL fragments for q()'s regexp matcher; the list
// fragment takes optional " AND program_id = ?" / " AND name LIKE ?" suffixes.
const (
	sqlTopicCount    = "SELECT COUNT(*) FROM topics WHERE school_id = ?"
	sqlTopicListFrom = "FROM topics WHERE school_id = ?"
	sqlTopicByID     = "FROM topics WHERE id = ? AND school_id = ?"
	sqlTopicProgram  = "SELECT name FROM programs WHERE id = ? AND school_id = ?"
	sqlTopicNextSort = "SELECT COALESCE(MAX(sort_order), 0) + 1 FROM topics"
	sqlTopicInsert   = "INSERT INTO topics (school_id, program_id, program_name_snap, name, description,"
	sqlTopicUpdate   = "UPDATE topics SET name = ?, description = ?, sort_order = ?"
	sqlTopicDelLinks = "DELETE FROM mission_topics WHERE topic_id = ? AND school_id = ?"
	sqlTopicDelRow   = "DELETE FROM topics WHERE id = ? AND school_id = ?"
)

var topicRowCols = []string{
	"id", "program_id", "program_name_snap", "name", "description",
	"sort_order", "badge_name", "badge_image_path", "is_active",
}

// topicRow builds one topicSelect row (9 columns); nil desc/badge args become
// SQL NULL.
func topicRow(id, programID int64, programName, name string, desc any,
	sortOrder int, badgeName, badgePath any, active bool) *sqlmock.Rows {
	return sqlmock.NewRows(topicRowCols).
		AddRow(id, programID, programName, name, desc, sortOrder, badgeName, badgePath, active)
}

// ---------------------------------------------------------------------------
// Envelope guards: token / role
// ---------------------------------------------------------------------------

func TestTopicsEnvelopeGuardRows(t *testing.T) {
	t.Run("no token is INVALID_TOKEN with no SQL", func(t *testing.T) {
		s, _ := newTestServer(t)
		app := newTestApp(t, s)
		res := doReq(t, app, "GET", "/api/topics", "", nil)
		requireErrEnvelope(t, res, CodeInvalidToken)
	})

	t.Run("facilitator on list is FORBIDDEN_ROLE after the user load", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectFASAuth(mock)
		res := doReq(t, app, "GET", "/api/topics", "", hFAS(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})

	t.Run("facilitator on create is FORBIDDEN_ROLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectFASAuth(mock)
		res := doMP(t, app, "POST", "/api/topics",
			map[string]string{"name": "T", "program_id": "5"}, nil, hFAS(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})
}

// ---------------------------------------------------------------------------
// List: default sort, program_id filter, search, validation, failures
// ---------------------------------------------------------------------------

func TestTopicsListDefaultSortPaged(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlTopicCount)).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q(sqlTopicListFrom+" ORDER BY sort_order asc, id ASC LIMIT ? OFFSET ?")).
		WithArgs(int64(1), 20, 0).
		WillReturnRows(topicRow(6, 5, "Math", "Numbers", "Counting", 1, nil, nil, true))

	res := doReq(t, app, "GET", "/api/topics", "", hAdmin(t, s))
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2, "paged envelope is data+meta only: %s", res.body)
	require.Equal(t,
		map[string]any{"page": float64(1), "per_page": float64(20), "total": float64(1)},
		m["meta"])
	item := m["data"].([]any)[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"id", "program_id", "program_name", "name", "description", "sort_order",
		"badge_name", "badge_image_url", "is_active",
	}, mapKeys(item), "Topic object must match contracts-shapes.md exactly")
	require.Equal(t, float64(6), item["id"])
	require.Equal(t, float64(5), item["program_id"])
	require.Equal(t, "Math", item["program_name"], "program_name is the snap column")
	require.Equal(t, "Numbers", item["name"])
	require.Equal(t, "Counting", item["description"])
	require.Equal(t, float64(1), item["sort_order"])
	require.Nil(t, item["badge_name"])
	require.Nil(t, item["badge_image_url"])
	require.Equal(t, true, item["is_active"])
}

func TestTopicsListProgramFilterSearchAndSort(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlTopicCount+" AND program_id = ? AND name LIKE ?")).
		WithArgs(int64(1), int64(5), "%ip%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(q(sqlTopicListFrom+
		" AND program_id = ? AND name LIKE ? ORDER BY name desc, id ASC LIMIT ? OFFSET ?")).
		WithArgs(int64(1), int64(5), "%ip%", 50, 50).
		WillReturnRows(topicRow(7, 5, "Math", "Algebra", nil, 2, "Top", "badges/1/top.png", true).
			AddRow(8, 5, "Math", "Geometry", "Shapes", 3, nil, nil, false))

	res := doReq(t, app, "GET",
		"/api/topics?program_id=5&q=ip&sort=name&dir=desc&page=2&per_page=50", "", hAdmin(t, s))
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2, "paged envelope is data+meta only: %s", res.body)
	require.Equal(t,
		map[string]any{"page": float64(2), "per_page": float64(50), "total": float64(2)},
		m["meta"])
	rows := m["data"].([]any)
	require.Len(t, rows, 2)
	first := rows[0].(map[string]any)
	require.Equal(t, "/uploads/badges/1/top.png", first["badge_image_url"])
	second := rows[1].(map[string]any)
	require.Nil(t, second["badge_image_url"])
	require.Equal(t, false, second["is_active"])
}

func TestTopicsListRejectsBadParamsWithSingleEnvelope(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	for _, target := range []string{
		"/api/topics?per_page=3",      // allowed set {20,50,100,1000}
		"/api/topics?sort=badge_name", // outside allowlist sort_order|name|id
		"/api/topics?program_id=abc",  // queryIDFilter: non-numeric
		"/api/topics?program_id=0",    // queryIDFilter: must be > 0
	} {
		expectAdminAuth(mock) // rejected before the COUNT query
		res := doReq(t, app, "GET", target, "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation) // exactly one envelope key
	}
}

func TestTopicsListFailuresAreDBUnavailable(t *testing.T) {
	t.Run("count fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicCount)).
			WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", "/api/topics", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("rows query fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicCount)).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(q(sqlTopicListFrom)).
			WillReturnError(errors.New("dial tcp: broken pipe"))
		res := doReq(t, app, "GET", "/api/topics", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicCount)).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		// NULL id cannot scan into int64 → scanTopic errors → DB_UNAVAILABLE.
		mock.ExpectQuery(q(sqlTopicListFrom)).
			WillReturnRows(sqlmock.NewRows(topicRowCols).
				AddRow(nil, int64(5), "Math", "X", nil, 1, nil, nil, true))
		res := doReq(t, app, "GET", "/api/topics", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// Create: validation matrix, program existence, default-append, badge, insert
// ---------------------------------------------------------------------------

func TestTopicsCreateValidationBeforeAnySQL(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	for _, tc := range []struct {
		name   string
		fields map[string]string
	}{
		{"missing name", map[string]string{"program_id": "5"}},
		{"blank name", map[string]string{"name": "   ", "program_id": "5"}},
		{"name over 200 chars", map[string]string{"name": strings.Repeat("n", 201), "program_id": "5"}},
		{"missing program_id", map[string]string{"name": "T"}},
		{"non-numeric program_id", map[string]string{"name": "T", "program_id": "abc"}},
		{"zero program_id", map[string]string{"name": "T", "program_id": "0"}},
		{"negative program_id", map[string]string{"name": "T", "program_id": "-3"}},
		{"negative sort_order", map[string]string{"name": "T", "program_id": "5", "sort_order": "-1"}},
		{"non-numeric sort_order", map[string]string{"name": "T", "program_id": "5", "sort_order": "x"}},
		{"garbage is_active", map[string]string{"name": "T", "program_id": "5", "is_active": "maybe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectAdminAuth(mock) // no SQL may follow (stray query fails the mock)
			res := doMP(t, app, "POST", "/api/topics", tc.fields, nil, hAdmin(t, s))
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

func TestTopicsCreateUnknownProgramIsNotFound(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlTopicProgram)).WithArgs(int64(404), int64(1)).
		WillReturnError(sql.ErrNoRows)
	res := doMP(t, app, "POST", "/api/topics",
		map[string]string{"name": "T", "program_id": "404", "sort_order": "1"},
		nil, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound) // no INSERT queued
}

func TestTopicsCreateDefaultAppendSortOrderPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlTopicProgram)).WithArgs(int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Math"))
	mock.ExpectQuery(q(sqlTopicNextSort)).WithArgs(int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"next"}).AddRow(7))
	mock.ExpectExec(q(sqlTopicInsert)).
		WithArgs(int64(1), int64(5), "Math", "Topic A", nil, 7, nil, nil, true).
		WillReturnResult(sqlmock.NewResult(11, 1))
	mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(11), int64(1)).
		WillReturnRows(topicRow(11, 5, "Math", "Topic A", nil, 7, nil, nil, true))

	res := doMP(t, app, "POST", "/api/topics",
		map[string]string{"name": "Topic A", "program_id": "5"}, nil, hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	item := data["topic"].(map[string]any)
	require.Equal(t, float64(11), item["id"])
	require.Equal(t, float64(5), item["program_id"])
	require.Equal(t, "Math", item["program_name"], "snap taken from the parent program")
	require.Equal(t, "Topic A", item["name"])
	require.Nil(t, item["description"], "omitted description persists NULL")
	require.Equal(t, float64(7), item["sort_order"], "default append = MAX(sort_order)+1")
	require.Nil(t, item["badge_name"])
	require.Nil(t, item["badge_image_url"])
	require.Equal(t, true, item["is_active"], "is_active defaults to true")

	f := takeFrame(t, spy)
	require.Equal(t, "topic.changed", f.Entity)
	require.Equal(t, int64(11), f.EntityID)
	require.Equal(t, "created", f.Action)
	require.Equal(t, int64(1), f.SchoolID)
}

func TestTopicsCreateHalfFilledBadgeStopsAfterProgramCheck(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlTopicProgram)).WithArgs(int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Math"))
	// Badge rule fails after the parent check but before any store/INSERT.
	res := doMP(t, app, "POST", "/api/topics",
		map[string]string{"name": "T", "program_id": "5", "sort_order": "1", "badge_name": "Gold"},
		nil, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeValidation)
	requireNoBadgesDir(t, s, "1")
}

func TestTopicsCreateSuccessExplicitSortPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlTopicProgram)).WithArgs(int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Math"))
	// Explicit sort_order → no default-append query.
	mock.ExpectExec(q(sqlTopicInsert)).
		WithArgs(int64(1), int64(5), "Math", "Topic A", "Desc", 3, nil, nil, false).
		WillReturnResult(sqlmock.NewResult(12, 1))
	mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(12), int64(1)).
		WillReturnRows(topicRow(12, 5, "Math", "Topic A", "Desc", 3, nil, nil, false))

	res := doMP(t, app, "POST", "/api/topics",
		map[string]string{"name": "Topic A", "program_id": "5", "sort_order": "3",
			"description": "Desc", "is_active": "false"},
		nil, hAdmin(t, s))
	item := requireDataEnvelope(t, res).(map[string]any)["topic"].(map[string]any)
	require.Equal(t, float64(3), item["sort_order"])
	require.Equal(t, "Desc", item["description"])
	require.Equal(t, false, item["is_active"])

	f := takeFrame(t, spy)
	require.Equal(t, "topic.changed", f.Entity)
	require.Equal(t, "created", f.Action)
}

func TestTopicsCreateFailuresBeforePublish(t *testing.T) {
	t.Run("insert error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicProgram)).WithArgs(int64(5), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Math"))
		mock.ExpectExec(q(sqlTopicInsert)).
			WillReturnError(errors.New("connection refused"))
		res := doMP(t, app, "POST", "/api/topics",
			map[string]string{"name": "T", "program_id": "5", "sort_order": "1"},
			nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("reload failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicProgram)).WithArgs(int64(5), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Math"))
		mock.ExpectExec(q(sqlTopicInsert)).
			WithArgs(int64(1), int64(5), "Math", "T", nil, 1, nil, nil, true).
			WillReturnResult(sqlmock.NewResult(11, 1))
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(11), int64(1)).
			WillReturnError(errors.New("connection refused"))
		res := doMP(t, app, "POST", "/api/topics",
			map[string]string{"name": "T", "program_id": "5", "sort_order": "1"},
			nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("raced delete after insert is NOT_FOUND without a frame", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicProgram)).WithArgs(int64(5), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Math"))
		mock.ExpectExec(q(sqlTopicInsert)).
			WithArgs(int64(1), int64(5), "Math", "T", nil, 1, nil, nil, true).
			WillReturnResult(sqlmock.NewResult(11, 1))
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(11), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := doMP(t, app, "POST", "/api/topics",
			map[string]string{"name": "T", "program_id": "5", "sort_order": "1"},
			nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
		requireNoFrame(t, spy)
	})
}

// ---------------------------------------------------------------------------
// Detail: found / unknown / cross-school / malformed / failure
// ---------------------------------------------------------------------------

func TestTopicsGetDetail(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(6), int64(1)).
			WillReturnRows(topicRow(6, 5, "Math", "Numbers", "Counting", 1,
				"Top", "badges/1/top.png", true))
		res := doReq(t, app, "GET", "/api/topics/6", "", hAdmin(t, s))
		item := requireDataEnvelope(t, res).(map[string]any)["topic"].(map[string]any)
		require.Equal(t, float64(6), item["id"])
		require.Equal(t, "Numbers", item["name"])
		require.Equal(t, "/uploads/badges/1/top.png", item["badge_image_url"])
	})

	t.Run("unknown id is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(404), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/api/topics/404", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("cross-school id via SA has identical NOT_FOUND shape", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 1)
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(77), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/api/topics/77", "", hSA(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("malformed id behaves as unknown with no SQL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		res := doReq(t, app, "GET", "/api/topics/abc", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("load failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(6), int64(1)).
			WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", "/api/topics/6", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// Patch: validation after the load, badge clear with share check, full update
// ---------------------------------------------------------------------------

func TestTopicsPatchRejectsNonMultipartBeforeLoad(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectAdminAuth(mock) // masterForm fails before the row load (no SQL queued)
	res := jsonReq(t, app, "PATCH", "/api/topics/3",
		map[string]any{"name": "Renamed"}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeValidation)
}

func TestTopicsPatchValidationAfterLoadWithoutWrite(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	for _, tc := range []struct {
		name   string
		fields map[string]string
	}{
		{"blank name", map[string]string{"name": "   "}},
		{"name over 200 chars", map[string]string{"name": strings.Repeat("n", 201)}},
		{"non-numeric sort_order", map[string]string{"name": "Ok", "sort_order": "x"}},
		{"negative sort_order", map[string]string{"name": "Ok", "sort_order": "-2"}},
		{"garbage is_active", map[string]string{"name": "Ok", "is_active": "maybe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectAdminAuth(mock)
			mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(3), int64(1)).
				WillReturnRows(topicRow(3, 5, "Math", "Old Topic", "Old desc", 4, nil, nil, true))
			// No UPDATE expectation: a stray write fails the mock.
			res := doMP(t, app, "PATCH", "/api/topics/3", tc.fields, nil, hAdmin(t, s))
			requireErrEnvelope(t, res, CodeValidation)
		})
	}

	t.Run("load failure is DB_UNAVAILABLE", func(t *testing.T) {
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(3), int64(1)).
			WillReturnError(errors.New("connection refused"))
		res := doMP(t, app, "PATCH", "/api/topics/3",
			map[string]string{"name": "X"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestTopicsPatchBadgeClearRemovesFileAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)
	oldAbs := seedUploadFile(t, s, "badges/1/gold.png")

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(3), int64(1)).
		WillReturnRows(topicRow(3, 5, "Math", "Old Topic", "Old desc", 4,
			"Gold", "badges/1/gold.png", true))
	// Clear = badge_name:"" with no file → NULL pair; description:"" → NULL.
	mock.ExpectExec(q(sqlTopicUpdate)).
		WithArgs("Old Topic", nil, 4, nil, nil, true, int64(3), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q(sqlBadgeShared)).WithArgs("badges/1/gold.png").
		WillReturnRows(sqlmock.NewRows([]string{"shared"}).AddRow(0))
	mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(3), int64(1)).
		WillReturnRows(topicRow(3, 5, "Math", "Old Topic", nil, 4, nil, nil, true))

	res := doMP(t, app, "PATCH", "/api/topics/3",
		map[string]string{"description": "", "badge_name": ""}, nil, hAdmin(t, s))
	item := requireDataEnvelope(t, res).(map[string]any)["topic"].(map[string]any)
	require.Nil(t, item["description"], "empty description clears to JSON null")
	require.Nil(t, item["badge_name"])
	require.Nil(t, item["badge_image_url"])

	require.NoFileExists(t, oldAbs, "unshared old badge file must be removed")
	f := takeFrame(t, spy)
	require.Equal(t, "topic.changed", f.Entity)
	require.Equal(t, int64(3), f.EntityID)
	require.Equal(t, "updated", f.Action)
}

func TestTopicsPatchFullFieldUpdatePublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(3), int64(1)).
		WillReturnRows(topicRow(3, 5, "Math", "Old Topic", "Old desc", 4, nil, nil, true))
	mock.ExpectExec(q(sqlTopicUpdate)).
		WithArgs("New Topic", "New desc", 9, nil, nil, false, int64(3), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(3), int64(1)).
		WillReturnRows(topicRow(3, 5, "Math", "New Topic", "New desc", 9, nil, nil, false))

	res := doMP(t, app, "PATCH", "/api/topics/3",
		map[string]string{"name": " New Topic ", "description": "New desc",
			"sort_order": "9", "is_active": "false"},
		nil, hAdmin(t, s))
	item := requireDataEnvelope(t, res).(map[string]any)["topic"].(map[string]any)
	require.Equal(t, "New Topic", item["name"], "name trimmed before persist")
	require.Equal(t, "New desc", item["description"])
	require.Equal(t, float64(9), item["sort_order"])
	require.Equal(t, false, item["is_active"])
	require.Nil(t, item["badge_image_url"], "badge pair untouched stays NULL")

	f := takeFrame(t, spy)
	require.Equal(t, "topic.changed", f.Entity)
	require.Equal(t, "updated", f.Action)
}

func TestTopicsPatchWriteAndReloadFailures(t *testing.T) {
	t.Run("update error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(3), int64(1)).
			WillReturnRows(topicRow(3, 5, "Math", "Old Topic", nil, 4, nil, nil, true))
		mock.ExpectExec(q(sqlTopicUpdate)).
			WithArgs("New", nil, 4, nil, nil, true, int64(3), int64(1)).
			WillReturnError(errors.New("connection refused"))
		res := doMP(t, app, "PATCH", "/api/topics/3",
			map[string]string{"name": "New"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("raced delete after update is NOT_FOUND without a frame", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(3), int64(1)).
			WillReturnRows(topicRow(3, 5, "Math", "Old Topic", nil, 4, nil, nil, true))
		mock.ExpectExec(q(sqlTopicUpdate)).
			WithArgs("New", nil, 4, nil, nil, true, int64(3), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(3), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := doMP(t, app, "PATCH", "/api/topics/3",
			map[string]string{"name": "New"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
		requireNoFrame(t, spy)
	})
}

func TestTopicsPatchUnknownIsNotFoundBeforeWrite(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(404), int64(1)).
		WillReturnError(sql.ErrNoRows)
	// No UPDATE queued: a stray write fails the mock.
	res := doMP(t, app, "PATCH", "/api/topics/404",
		map[string]string{"name": "X"}, nil, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

// ---------------------------------------------------------------------------
// Delete: one Tx + share-checked file removal + publish
// ---------------------------------------------------------------------------

func TestTopicsDeleteOneTxRemovesBadgeAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)
	oldAbs := seedUploadFile(t, s, "badges/1/gold.png")

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(4), int64(1)).
		WillReturnRows(topicRow(4, 5, "Math", "Doomed", nil, 2,
			"Gold", "badges/1/gold.png", true))
	mock.ExpectBegin()
	mock.ExpectExec(q(sqlTopicDelLinks)).WithArgs(int64(4), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q(sqlTopicDelRow)).WithArgs(int64(4), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(q(sqlBadgeShared)).WithArgs("badges/1/gold.png").
		WillReturnRows(sqlmock.NewRows([]string{"shared"}).AddRow(0))

	res := doReq(t, app, "DELETE", "/api/topics/4", "", hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])
	require.NoFileExists(t, oldAbs, "unshared badge file removed after commit")

	f := takeFrame(t, spy)
	require.Equal(t, "topic.changed", f.Entity)
	require.Equal(t, int64(4), f.EntityID)
	require.Equal(t, "deleted", f.Action)
	require.Equal(t, int64(1), f.SchoolID)

	// The Tx dropped mission_topics too: one mission.changed (no mission id
	// — the link sets changed) makes open mission pickers refetch.
	g := takeFrame(t, spy)
	require.Equal(t, "mission.changed", g.Entity)
	require.Equal(t, int64(0), g.EntityID)
	require.Equal(t, "updated", g.Action)
	require.Equal(t, int64(1), g.SchoolID)
	requireNoFrame(t, spy, "deleteTopic publishes exactly two frames")
}

func TestTopicsDeleteShareCheckFailureKeepsFileAndStillPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)
	oldAbs := seedUploadFile(t, s, "badges/1/gold.png")

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(4), int64(1)).
		WillReturnRows(topicRow(4, 5, "Math", "Doomed", nil, 2,
			"Gold", "badges/1/gold.png", true))
	mock.ExpectBegin()
	mock.ExpectExec(q(sqlTopicDelLinks)).WithArgs(int64(4), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q(sqlTopicDelRow)).WithArgs(int64(4), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// removeMasterImage is best-effort: a failed ref check only logs.
	mock.ExpectQuery(q(sqlBadgeShared)).WithArgs("badges/1/gold.png").
		WillReturnError(errors.New("connection refused"))

	res := doReq(t, app, "DELETE", "/api/topics/4", "", hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])
	require.FileExists(t, oldAbs, "failed share check must keep the file (janitor sweeps later)")
	require.Equal(t, "deleted", takeFrame(t, spy).Action)
}

func TestTopicsDeleteWithoutBadgeSkipsShareCheck(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(6), int64(1)).
		WillReturnRows(topicRow(6, 5, "Math", "Plain", nil, 1, nil, nil, true))
	mock.ExpectBegin()
	mock.ExpectExec(q(sqlTopicDelLinks)).WithArgs(int64(6), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q(sqlTopicDelRow)).WithArgs(int64(6), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// No share-check query: removeMasterImage("") returns immediately.

	res := doReq(t, app, "DELETE", "/api/topics/6", "", hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])
	require.Equal(t, "deleted", takeFrame(t, spy).Action)
}

func TestTopicsDeleteFailures(t *testing.T) {
	t.Run("unknown id is NOT_FOUND with no Tx", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(404), int64(1)).
			WillReturnError(sql.ErrNoRows)
		// No ExpectBegin: a stray Begin fails the mock → not NOT_FOUND.
		res := doReq(t, app, "DELETE", "/api/topics/404", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("load failure is DB_UNAVAILABLE before the Tx", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(4), int64(1)).
			WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "DELETE", "/api/topics/4", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("tx delete failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(4), int64(1)).
			WillReturnRows(topicRow(4, 5, "Math", "Doomed", nil, 2, nil, nil, true))
		mock.ExpectBegin()
		mock.ExpectExec(q(sqlTopicDelLinks)).WithArgs(int64(4), int64(1)).
			WillReturnError(errors.New("deadlock"))
		res := doReq(t, app, "DELETE", "/api/topics/4", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

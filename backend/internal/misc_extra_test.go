package internal

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// misc_extra_test.go — branch coverage for the rare failure paths of the ten
// B1 domain files (missions/users/dashboard/public/schools/children/
// activities/frames/topics/programs) that the focused suites do not reach:
// guard-false branches (malformed id / bad body / schoolless SA), count-vs-
// rows-vs-scan-vs-rows.Err failure splits, Tx begin/exec/commit failures,
// post-commit reload failures, and the upload-disk failure branches.
// sqlmock stays ORDERED; every expectation mirrors the handler's SQL order.

// ---------------------------------------------------------------------------
// Local fixtures (unique to this file)
// ---------------------------------------------------------------------------

// MscShortRow builds rows with fewer columns than the handler scans, turning
// the very first Scan into an error (the "rows fail" style branch).
func MscShortRow(cols int) *sqlmock.Rows {
	names := make([]string, cols)
	vals := make([]driver.Value, cols)
	for i := range names {
		names[i] = "c"
	}
	return sqlmock.NewRows(names).AddRow(vals...)
}

// MscErrRows builds one well-formed row followed by a driver error so
// rows.Next() yields the row set and rows.Err() reports the failure.
func MscErrRows(cols []string, vals []any, atRow int) *sqlmock.Rows {
	dv := make([]driver.Value, len(vals))
	for i, v := range vals {
		dv[i] = v
	}
	return sqlmock.NewRows(cols).AddRow(dv...).
		RowError(atRow, errors.New("driver: connection lost"))
}

// MscBlockSchoolUpload replaces uploads/badges with a regular FILE so the
// next MkdirAll(badges/<school>) inside storeImage fails with a raw (non
// errRejected) error — the handler must answer 500 INTERNAL via ErrorHandler.
func MscBlockSchoolUpload(t *testing.T, s *Server) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(s.upload, "badges"), []byte("not-a-dir"), 0o644))
}

// MscUnremovablePath creates rel under the upload root as a NON-EMPTY
// directory: os.Remove/os.Remove… on it fails deterministically on every
// platform, exercising the best-effort slog branches.
func MscUnremovablePath(t *testing.T, s *Server, rel string) {
	t.Helper()
	dir := filepath.Join(s.upload, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "child.bin"), []byte("x"), 0o644))
}

// MscRequireInternal asserts the system's only HTTP 500 envelope (an error
// returned straight from a handler, e.g. a raw storeImage failure).
func MscRequireInternal(t *testing.T, res httpRes) {
	t.Helper()
	require.Equal(t, 500, res.status, "raw store failures surface as HTTP 500: %s", res.body)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 1, "500 envelope carries exactly one error key: %s", res.body)
	errObj, ok := m["error"].(map[string]any)
	require.Truef(t, ok, "500 envelope malformed: %s", res.body)
	require.Equal(t, CodeInternal, errObj["code"])
	require.Equal(t, MsgInternal, errObj["message"])
}

// ---------------------------------------------------------------------------
// missions.go — guard-false branches, rows/scan/rows.Err splits, Tx and
// post-commit reload failures
// ---------------------------------------------------------------------------

func TestMscMissionsGuardFalseBranches(t *testing.T) {
	t.Run("malformed id is NOT_FOUND without any lookup", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		for _, tc := range []struct {
			method, target, body string
		}{
			{"GET", "/api/missions/abc", ""},
			{"PATCH", "/api/missions/abc", `{"name":"X"}`},
			{"DELETE", "/api/missions/abc", ""},
		} {
			expectAdminAuth(mock) // idParam fails before the item query
			res := doReq(t, app, tc.method, tc.target, tc.body, hAdmin(t, s))
			requireErrEnvelope(t, res, CodeNotFound)
		}
	})

	t.Run("malformed body is VALIDATION before the school guard", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		res := doReq(t, app, "POST", "/api/missions", "not-json", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
		expectAdminAuth(mock)
		res = doReq(t, app, "PATCH", "/api/missions/1", "not-json", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("schoolless SA is VALIDATION on every route", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		// GET: idParam ok → requireSchool → VALIDATION before the item query.
		expectSAAuth(mock, 0)
		res := doReq(t, app, "GET", "/api/missions/1", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
		// POST: body valid → requireSchool before the program lookup.
		expectSAAuth(mock, 0)
		res = jsonReq(t, app, "POST", "/api/missions", map[string]any{
			"program_id": 3, "name": "Misi", "topic_ids": []int64{5},
		}, hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
		// PATCH: load never runs without a school.
		expectSAAuth(mock, 0)
		res = jsonReq(t, app, "PATCH", "/api/missions/1",
			map[string]any{"name": "X"}, hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
		// DELETE likewise.
		expectSAAuth(mock, 0)
		res = doReq(t, app, "DELETE", "/api/missions/1", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})
}

func TestMscMissionsListRowsAndLinkFailures(t *testing.T) {
	t.Run("row scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(misCountFrag)).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		// missionSelect scans 4 columns; 3 make the Scan fail mid-iteration.
		mock.ExpectQuery(q(misRowsFrag)).WithArgs(int64(1), 20, 0).
			WillReturnRows(MscShortRow(3))
		res := doReq(t, app, "GET", "/api/missions", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("driver error after rows is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(misCountFrag)).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(q(misRowsFrag)).WithArgs(int64(1), 20, 0).
			WillReturnRows(MscErrRows(
				[]string{"id", "program_id", "program_name_snap", "name"},
				[]any{int64(1), int64(3), "Prog", "Misi"}, 0))
		res := doReq(t, app, "GET", "/api/missions", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("link query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(misCountFrag)).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(q(misRowsFrag)).WithArgs(int64(1), 20, 0).
			WillReturnRows(misRow(1, 3, "Prog", "Misi"))
		mock.ExpectQuery(q(misLinkSelFrag)).WithArgs(int64(1), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/missions", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscMissionsGetDetailLoadAndLinkFailures(t *testing.T) {
	t.Run("item load failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(9), int64(1)).
			WillReturnError(errors.New("bad connection"))
		res := doReq(t, app, "GET", "/api/missions/9", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("link query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(9), int64(1)).
			WillReturnRows(misRow(9, 3, "Prog", "Misi"))
		mock.ExpectQuery(q(misLinkSelFrag)).WithArgs(int64(1), int64(9)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/missions/9", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("link scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(9), int64(1)).
			WillReturnRows(misRow(9, 3, "Prog", "Misi"))
		// missionTopics scans 3 columns; 2 make the Scan fail.
		mock.ExpectQuery(q(misLinkSelFrag)).WithArgs(int64(1), int64(9)).
			WillReturnRows(MscShortRow(2))
		res := doReq(t, app, "GET", "/api/missions/9", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscMissionsTopicValidationQueryFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows *sqlmock.Rows
	}{
		{"topics SELECT failure is DB_UNAVAILABLE", nil},
		{"topics scan failure is DB_UNAVAILABLE", MscShortRow(1)},
		{"topics driver error is DB_UNAVAILABLE", MscErrRows(
			[]string{"id", "name"}, []any{int64(5), "Topic"}, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			expectAdminAuth(mock)
			mock.ExpectQuery(q(misProgFrag)).WithArgs(int64(3), int64(1)).
				WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
			frag := q("SELECT id, name FROM topics WHERE school_id = ? AND id IN (?)")
			if tc.rows == nil {
				mock.ExpectQuery(frag).WithArgs(int64(1), int64(5)).
					WillReturnError(errors.New("dial tcp: connection refused"))
			} else {
				mock.ExpectQuery(frag).WithArgs(int64(1), int64(5)).
					WillReturnRows(tc.rows)
			}
			// No Tx in any branch: the failure answers before begin.
			res := jsonReq(t, app, "POST", "/api/missions", map[string]any{
				"program_id": 3, "name": "Misi", "topic_ids": []int64{5},
			}, hAdmin(t, s))
			requireErrEnvelope(t, res, CodeDBUnavailable)
		})
	}
}

func TestMscMissionsCreateProgramLookupFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectAdminAuth(mock)
	mock.ExpectQuery(q(misProgFrag)).WithArgs(int64(3), int64(1)).
		WillReturnError(errors.New("dial tcp: connection refused"))
	res := jsonReq(t, app, "POST", "/api/missions", map[string]any{
		"program_id": 3, "name": "Misi", "topic_ids": []int64{5},
	}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

func TestMscMissionsCreateTxFailures(t *testing.T) {
	t.Run("BeginTx failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(misProgFrag)).WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
		mock.ExpectQuery(q("SELECT id, name FROM topics WHERE school_id = ? AND id IN (?)")).
			WithArgs(int64(1), int64(5)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).
				AddRow(int64(5), "Topic Membaca"))
		mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
		res := jsonReq(t, app, "POST", "/api/missions", map[string]any{
			"program_id": 3, "name": "Misi", "topic_ids": []int64{5},
		}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("commit failure is DB_UNAVAILABLE", func(t *testing.T) {
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
			WillReturnResult(sqlmock.NewResult(21, 1))
		mock.ExpectExec(q(misInsLinkFrag)).
			WithArgs(int64(21), int64(5), "Topic Membaca", int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(errors.New("driver: bad connection"))
		res := jsonReq(t, app, "POST", "/api/missions", map[string]any{
			"program_id": 3, "name": "Misi", "topic_ids": []int64{5},
		}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscMissionsCreateReloadFailuresAfterCommit(t *testing.T) {
	wire := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q(misProgFrag)).WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
		mock.ExpectQuery(q("SELECT id, name FROM topics WHERE school_id = ? AND id IN (?)")).
			WithArgs(int64(1), int64(5)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).
				AddRow(int64(5), "Topic Membaca"))
		mock.ExpectBegin()
		mock.ExpectExec(q(misInsFrag)).
			WithArgs(int64(1), int64(3), "Progam Literasi", "Misi").
			WillReturnResult(sqlmock.NewResult(21, 1))
		mock.ExpectExec(q(misInsLinkFrag)).
			WithArgs(int64(21), int64(5), "Topic Membaca", int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
	}
	body := map[string]any{
		"program_id": 3, "name": "Misi", "topic_ids": []int64{5},
	}

	t.Run("reload driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		wire(mock)
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(21), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "POST", "/api/missions", body, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("raced delete after commit is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		wire(mock)
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(21), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := jsonReq(t, app, "POST", "/api/missions", body, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("post-commit link query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		wire(mock)
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(21), int64(1)).
			WillReturnRows(misRow(21, 3, "Progam Literasi", "Misi"))
		mock.ExpectQuery(q(misLinkSelFrag)).WithArgs(int64(1), int64(21)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "POST", "/api/missions", body, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscMissionsPatchFailureBranches(t *testing.T) {
	loadCur := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
			WillReturnRows(misRow(1, 1, "Progam Lama", "Misi Lama"))
	}

	t.Run("load failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
			WillReturnError(errors.New("bad connection"))
		res := jsonReq(t, app, "PATCH", "/api/missions/1",
			map[string]any{"name": "Baru"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("program reassign lookup driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectQuery(q(misProgFrag)).WithArgs(int64(4), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "PATCH", "/api/missions/1",
			map[string]any{"program_id": 4}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("BeginTx failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
		res := jsonReq(t, app, "PATCH", "/api/missions/1",
			map[string]any{"name": "Baru"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("link DELETE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectQuery(q("SELECT id, name FROM topics WHERE school_id = ? AND id IN (?)")).
			WithArgs(int64(1), int64(8)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).
				AddRow(int64(8), "Topic Pagi"))
		mock.ExpectBegin()
		mock.ExpectExec(q(misUpdFrag)).
			WithArgs(int64(1), "Progam Lama", "Misi Lama", int64(1), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q(misDelLinkFrag)).WithArgs(int64(1), int64(1)).
			WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()
		res := jsonReq(t, app, "PATCH", "/api/missions/1",
			map[string]any{"topic_ids": []int64{8}}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("link INSERT failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectQuery(q("SELECT id, name FROM topics WHERE school_id = ? AND id IN (?)")).
			WithArgs(int64(1), int64(8)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).
				AddRow(int64(8), "Topic Pagi"))
		mock.ExpectBegin()
		mock.ExpectExec(q(misUpdFrag)).
			WithArgs(int64(1), "Progam Lama", "Misi Lama", int64(1), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q(misDelLinkFrag)).WithArgs(int64(1), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 3))
		mock.ExpectExec(q(misInsLinkFrag)).
			WithArgs(int64(1), int64(8), "Topic Pagi", int64(1)).
			WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()
		res := jsonReq(t, app, "PATCH", "/api/missions/1",
			map[string]any{"topic_ids": []int64{8}}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("commit failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q(misUpdFrag)).
			WithArgs(int64(1), "Progam Lama", "Baru", int64(1), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(errors.New("driver: bad connection"))
		res := jsonReq(t, app, "PATCH", "/api/missions/1",
			map[string]any{"name": "Baru"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("post-commit reload driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q(misUpdFrag)).
			WithArgs(int64(1), "Progam Lama", "Baru", int64(1), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "PATCH", "/api/missions/1",
			map[string]any{"name": "Baru"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("raced delete after commit is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q(misUpdFrag)).
			WithArgs(int64(1), "Progam Lama", "Baru", int64(1), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := jsonReq(t, app, "PATCH", "/api/missions/1",
			map[string]any{"name": "Baru"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("post-commit link query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q(misUpdFrag)).
			WithArgs(int64(1), "Progam Lama", "Baru", int64(1), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
			WillReturnRows(misRow(1, 1, "Progam Lama", "Baru"))
		mock.ExpectQuery(q(misLinkSelFrag)).WithArgs(int64(1), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "PATCH", "/api/missions/1",
			map[string]any{"name": "Baru"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscMissionsDeleteFailureBranches(t *testing.T) {
	loadCur := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
			WillReturnRows(misRow(1, 1, "Progam Lama", "Misi Lama"))
	}

	t.Run("load failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(misByIDFrag)).WithArgs(int64(1), int64(1)).
			WillReturnError(errors.New("bad connection"))
		res := doReq(t, app, "DELETE", "/api/missions/1", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("BeginTx failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "DELETE", "/api/missions/1", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("link DELETE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q(misDelLinkFrag)).WithArgs(int64(1), int64(1)).
			WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/missions/1", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("commit failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q(misDelLinkFrag)).WithArgs(int64(1), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 3))
		mock.ExpectExec(q(misDelFrag)).WithArgs(int64(1), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(errors.New("driver: bad connection"))
		res := doReq(t, app, "DELETE", "/api/missions/1", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// users.go — guard-false branches, list rows failures, create/get/patch
// reload and write failures, E31-gate and Tx failures, decision/active tails
// ---------------------------------------------------------------------------

func TestMscUsersGuardFalseBranches(t *testing.T) {
	t.Run("malformed id is NOT_FOUND without any lookup", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		for _, tc := range []struct {
			method, target, body string
		}{
			{"GET", "/api/users/abc", ""},
			{"PATCH", "/api/users/abc", `{"name":"X"}`},
			{"DELETE", "/api/users/abc", ""},
			{"POST", "/api/users/abc/approve", ""},
			{"POST", "/api/users/abc/activate", ""},
		} {
			expectAdminAuth(mock) // idParam fails before loadUserForEdit
			res := doReq(t, app, tc.method, tc.target, tc.body, hAdmin(t, s))
			requireErrEnvelope(t, res, CodeNotFound)
		}
	})

	t.Run("malformed body is VALIDATION before the scope load", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		res := doReq(t, app, "POST", "/api/users", "not-json", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
		expectAdminAuth(mock)
		res = doReq(t, app, "PATCH", "/api/users/3", "not-json", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("cross-school id stops at loadUserForEdit on patch/approve/activate", func(t *testing.T) {
		for _, tc := range []struct {
			method, target string
			body           map[string]any
		}{
			{"PATCH", "/api/users/77", map[string]any{"name": "X"}},
			{"POST", "/api/users/77/approve", nil},
			{"POST", "/api/users/77/activate", nil},
		} {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			expectAdminAuth(mock)
			mock.ExpectQuery(sqlEditUser).WithArgs(int64(77)).
				WillReturnRows(sqlEditUserRow(77, 2, "FASILITATOR", "APPROVED"))
			// No write may follow the scope gate.
			res := jsonReq(t, app, tc.method, tc.target, tc.body, hAdmin(t, s))
			requireErrEnvelope(t, res, CodeNotFound)
		}
	})
}

func TestMscUsersListRowsFailureBranches(t *testing.T) {
	rowsFrag := q("AND u.school_id = ? AND u.role = 'FASILITATOR' ORDER BY id asc, u.id ASC LIMIT ? OFFSET ?")

	t.Run("rows query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlUsersCount).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(rowsFrag).WithArgs(int64(1), 20, 0).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/users", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("row scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlUsersCount).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		// userCols scans 10 destinations; 9 make Scan fail mid-iteration.
		mock.ExpectQuery(rowsFrag).WithArgs(int64(1), 20, 0).
			WillReturnRows(MscShortRow(9))
		res := doReq(t, app, "GET", "/api/users", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("driver error after rows is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlUsersCount).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(rowsFrag).WithArgs(int64(1), 20, 0).
			WillReturnRows(MscErrRows(
				[]string{"id", "name", "email", "role", "decision", "is_active",
					"school_id", "wa_number", "created_at", "school_name"},
				[]any{int64(3), "Fasil", "fas@school.id", "FASILITATOR",
					"APPROVED", true, int64(1), "+628111111111", fixedTS, "Bandung"}, 0))
		res := doReq(t, app, "GET", "/api/users", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscUsersCreateDBFailureBranches(t *testing.T) {
	body := map[string]any{
		"name": "Fasil Dua", "email": "fas2@school.id", "password": testPassword,
		"wa_number": "08123456789", "role": "FASILITATOR",
	}

	t.Run("school active lookup failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "POST", "/api/users", body, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("email EXISTS probe failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
		expectUsersCreateClaim(mock, "msc-exists-1")
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
			WithArgs("fas2@school.id").
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "POST", "/api/users", body,
			withIdem(hAdmin(t, s), "msc-exists-1"))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("non-duplicate INSERT failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
		expectUsersCreateClaim(mock, "msc-insert-1")
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
			WithArgs("fas2@school.id").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
		mock.ExpectExec(q("INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)")).
			WillReturnError(errors.New("disk full"))
		res := jsonReq(t, app, "POST", "/api/users", body,
			withIdem(hAdmin(t, s), "msc-insert-1"))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("reload driver error after insert is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
		expectUsersCreateClaim(mock, "msc-reload-1")
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ?")).
			WithArgs("fas2@school.id").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
		mock.ExpectExec(q("INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)")).
			WithArgs(int64(1), "Fasil Dua", "fas2@school.id", "+628123456789",
				sqlmock.AnyArg(), "FASILITATOR").
			WillReturnResult(sqlmock.NewResult(10, 1))
		mock.ExpectQuery(sqlUserItem).WithArgs(int64(10)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "POST", "/api/users", body,
			withIdem(hAdmin(t, s), "msc-reload-1"))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscUsersGetReloadFailureBranches(t *testing.T) {
	t.Run("reload driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
			WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
		mock.ExpectQuery(sqlUserItem).WithArgs(int64(3)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/users/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("row vanished between scope load and reload is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
			WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
		mock.ExpectQuery(sqlUserItem).WithArgs(int64(3)).
			WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/api/users/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})
}

func TestMscUsersPatchDBFailureBranches(t *testing.T) {
	loadScope := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
			WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
	}

	t.Run("email EXISTS probe failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM users WHERE email = ? AND id <> ?")).
			WithArgs("new@school.id", int64(3)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "PATCH", "/api/users/3",
			map[string]any{"email": "new@school.id"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("non-duplicate UPDATE failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		mock.ExpectExec(q("UPDATE users SET name = ? WHERE id = ?")).
			WithArgs("Nama Baru", int64(3)).
			WillReturnError(errors.New("disk full"))
		res := jsonReq(t, app, "PATCH", "/api/users/3",
			map[string]any{"name": "Nama Baru"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("reload driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		mock.ExpectExec(q("UPDATE users SET name = ? WHERE id = ?")).
			WithArgs("Nama Baru", int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(sqlUserItem).WithArgs(int64(3)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "PATCH", "/api/users/3",
			map[string]any{"name": "Nama Baru"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("row vanished after the update is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		mock.ExpectExec(q("UPDATE users SET name = ? WHERE id = ?")).
			WithArgs("Nama Baru", int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(sqlUserItem).WithArgs(int64(3)).
			WillReturnError(sql.ErrNoRows)
		res := jsonReq(t, app, "PATCH", "/api/users/3",
			map[string]any{"name": "Nama Baru"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})
}

func TestMscUsersDeleteDBFailureBranches(t *testing.T) {
	loadScope := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
			WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
	}
	emptyGroups := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(sqlOpenGroups).WithArgs(int64(3)).
			WillReturnRows(sqlmock.NewRows([]string{"gid", "sid", "sname"}))
	}

	t.Run("open-group query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		mock.ExpectQuery(sqlOpenGroups).WithArgs(int64(3)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "DELETE", "/api/users/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("open-group scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		// The gate scans gid/sid/sname; 2 columns fail mid-iteration.
		mock.ExpectQuery(sqlOpenGroups).WithArgs(int64(3)).
			WillReturnRows(MscShortRow(2))
		res := doReq(t, app, "DELETE", "/api/users/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("open-group driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		mock.ExpectQuery(sqlOpenGroups).WithArgs(int64(3)).
			WillReturnRows(MscErrRows([]string{"gid", "sid", "sname"},
				[]any{int64(10), int64(5), "Sesi A"}, 0))
		res := doReq(t, app, "DELETE", "/api/users/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("BeginTx failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		emptyGroups(mock)
		mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "DELETE", "/api/users/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("refresh-token DELETE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		emptyGroups(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("DELETE FROM refresh_tokens WHERE user_id = ? AND impersonated_by IS NULL")).
			WithArgs(int64(3)).WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/users/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("reset-token DELETE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		emptyGroups(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("DELETE FROM refresh_tokens WHERE user_id = ? AND impersonated_by IS NULL")).
			WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(q("DELETE FROM reset_tokens WHERE user_id = ? AND source = 'SELF'")).
			WithArgs(int64(3)).WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/users/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("user row DELETE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		emptyGroups(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("DELETE FROM refresh_tokens WHERE user_id = ? AND impersonated_by IS NULL")).
			WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(q("DELETE FROM reset_tokens WHERE user_id = ? AND source = 'SELF'")).
			WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q("DELETE FROM users WHERE id = ?")).
			WithArgs(int64(3)).WillReturnError(errors.New("foreign key constraint"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/users/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("commit failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		emptyGroups(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("DELETE FROM refresh_tokens WHERE user_id = ? AND impersonated_by IS NULL")).
			WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(q("DELETE FROM reset_tokens WHERE user_id = ? AND source = 'SELF'")).
			WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q("DELETE FROM users WHERE id = ?")).
			WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(errors.New("driver: bad connection"))
		res := doReq(t, app, "DELETE", "/api/users/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscUsersDecisionWriteAndReloadFailures(t *testing.T) {
	t.Run("decision UPDATE failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(8)).
			WillReturnRows(sqlEditUserRow(8, 1, "FASILITATOR", "PENDING"))
		mock.ExpectExec(q("UPDATE users SET decision = ?, is_active = ? WHERE id = ?")).
			WithArgs("APPROVED", true, int64(8)).
			WillReturnError(errors.New("connection reset"))
		res := jsonReq(t, app, "POST", "/api/users/8/approve", nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("reload driver error after the decision is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(8)).
			WillReturnRows(sqlEditUserRow(8, 1, "FASILITATOR", "PENDING"))
		mock.ExpectExec(q("UPDATE users SET decision = ?, is_active = ? WHERE id = ?")).
			WithArgs("REJECTED", false, int64(8)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(sqlPendingFACount).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(sqlUserItem).WithArgs(int64(8)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "POST", "/api/users/8/reject", nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscUsersActiveTxFailureBranches(t *testing.T) {
	loadScope := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(sqlEditUser).WithArgs(int64(3)).
			WillReturnRows(sqlEditUserRow(3, 1, "FASILITATOR", "APPROVED"))
	}

	t.Run("BeginTx failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
		res := jsonReq(t, app, "POST", "/api/users/3/deactivate", nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("is_active UPDATE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("UPDATE users SET is_active = ? WHERE id = ?")).
			WithArgs(false, int64(3)).WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/users/3/deactivate", nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("refresh revoke failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("UPDATE users SET is_active = ? WHERE id = ?")).
			WithArgs(false, int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW()")).
			WithArgs(int64(3)).WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()
		res := jsonReq(t, app, "POST", "/api/users/3/deactivate", nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("commit failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("UPDATE users SET is_active = ? WHERE id = ?")).
			WithArgs(true, int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(errors.New("driver: bad connection"))
		res := jsonReq(t, app, "POST", "/api/users/3/activate", nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("reload driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadScope(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("UPDATE users SET is_active = ? WHERE id = ?")).
			WithArgs(true, int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(sqlUserItem).WithArgs(int64(3)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "POST", "/api/users/3/activate", nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// dashboard.go — school-override lookup failure, the 30d window, and every
// aggregate/rows/scan/rows.Err failure branch of overview + analytics
// ---------------------------------------------------------------------------

func TestMscDashboardSchoolAndPeriodBranches(t *testing.T) {
	t.Run("SA override lookup failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := dashboardSAHeaders(t, s, mock)
		mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(7)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/dashboard/overview?school=7", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("analytics with no selected school is VALIDATION", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := dashboardSAHeaders(t, s, mock)
		res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("period 30d pins the thirty-day window", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		expectDashboardAnalytics(mock, int64(1), " AND a.updated_at >= NOW() - INTERVAL 30 DAY")
		res := doReq(t, app, "GET", "/api/dashboard/analytics?period=30d", "", h)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, "30d", data["period"])
	})
}

func TestMscDashboardOverviewAggregateFailures(t *testing.T) {
	t.Run("aggregate query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("(SELECT COUNT(*) FROM children WHERE school_id = ?)")).
			WithArgs(int64(1), int64(1), int64(1), int64(1), int64(1), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/dashboard/overview", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("progress query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("(SELECT COUNT(*) FROM children WHERE school_id = ?)")).
			WithArgs(int64(1), int64(1), int64(1), int64(1), int64(1), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"children", "open", "finished",
				"report_links", "narratives", "pending"}).AddRow(1, 0, 0, 0, 0, 0))
		mock.ExpectQuery(q("FROM sessions s WHERE s.school_id = ?")).
			WithArgs(int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/dashboard/overview", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("progress scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("(SELECT COUNT(*) FROM children WHERE school_id = ?)")).
			WithArgs(int64(1), int64(1), int64(1), int64(1), int64(1), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"children", "open", "finished",
				"report_links", "narratives", "pending"}).AddRow(1, 0, 0, 0, 0, 0))
		// progressRows scans 6 destinations; 4 fail mid-iteration.
		mock.ExpectQuery(q("FROM sessions s WHERE s.school_id = ?")).
			WithArgs(int64(1)).
			WillReturnRows(MscShortRow(4))
		res := doReq(t, app, "GET", "/api/dashboard/overview", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscDashboardAnalyticsDistributionFailures(t *testing.T) {
	const win = " AND a.updated_at >= NOW() - INTERVAL 7 DAY"

	t.Run("star distribution query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("WHERE a.school_id = ?" + win + " GROUP BY stars")).
			WithArgs(int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("star distribution scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("WHERE a.school_id = ?" + win + " GROUP BY stars")).
			WithArgs(int64(1)).
			WillReturnRows(MscShortRow(1))
		res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("star distribution driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		mock.ExpectQuery(q("WHERE a.school_id = ?" + win + " GROUP BY stars")).
			WithArgs(int64(1)).
			WillReturnRows(MscErrRows([]string{"stars", "count"},
				[]any{1, int64(2)}, 0))
		res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscDashboardAnalyticsTopicAggFailures(t *testing.T) {
	const win = " AND a.updated_at >= NOW() - INTERVAL 7 DAY"

	expectDist := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("WHERE a.school_id = ?" + win + " GROUP BY stars")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"stars", "count"}).AddRow(1, int64(2)))
	}

	t.Run("topic totals query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		expectDist(mock)
		mock.ExpectQuery(q("SELECT st.topic_id, MAX(st.topic_name_snap), COUNT(sa.id)")).
			WithArgs(int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("topic totals scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		expectDist(mock)
		mock.ExpectQuery(q("SELECT st.topic_id, MAX(st.topic_name_snap), COUNT(sa.id)")).
			WithArgs(int64(1)).
			WillReturnRows(MscShortRow(2))
		res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("topic totals driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		expectDist(mock)
		mock.ExpectQuery(q("SELECT st.topic_id, MAX(st.topic_name_snap), COUNT(sa.id)")).
			WithArgs(int64(1)).
			WillReturnRows(MscErrRows([]string{"topic_id", "name", "total"},
				[]any{int64(9), "Topik A", int64(12)}, 0))
		res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscDashboardAnalyticsWindowAndFacilitatorFailures(t *testing.T) {
	const win = " AND a.updated_at >= NOW() - INTERVAL 7 DAY"

	expectDistAndTopics := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("WHERE a.school_id = ?" + win + " GROUP BY stars")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"stars", "count"}).AddRow(1, int64(2)))
		mock.ExpectQuery(q("SELECT st.topic_id, MAX(st.topic_name_snap), COUNT(sa.id)")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"topic_id", "name", "total"}).
				AddRow(int64(9), "Topik A", int64(12)))
	}

	expectWindowedOK := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("WHERE a.school_id = ?" + win + " GROUP BY st.topic_id")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"topic_id", "assessed", "avg"}).
				AddRow(int64(9), int64(8), 3.6))
	}

	expectProgressOK := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("FROM sessions s WHERE s.school_id = ?")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date",
				"members", "activities", "assessed"}))
	}

	t.Run("windowed assessed query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		expectDistAndTopics(mock)
		mock.ExpectQuery(q("WHERE a.school_id = ?" + win + " GROUP BY st.topic_id")).
			WithArgs(int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("windowed assessed scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		expectDistAndTopics(mock)
		mock.ExpectQuery(q("WHERE a.school_id = ?" + win + " GROUP BY st.topic_id")).
			WithArgs(int64(1)).
			WillReturnRows(MscShortRow(2))
		res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("windowed assessed driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		expectDistAndTopics(mock)
		mock.ExpectQuery(q("WHERE a.school_id = ?" + win + " GROUP BY st.topic_id")).
			WithArgs(int64(1)).
			WillReturnRows(MscErrRows([]string{"topic_id", "assessed", "avg"},
				[]any{int64(9), int64(8), 3.6}, 0))
		res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("analytics progress query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		expectDistAndTopics(mock)
		expectWindowedOK(mock)
		mock.ExpectQuery(q("FROM sessions s WHERE s.school_id = ?")).
			WithArgs(int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("facilitator query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		expectDistAndTopics(mock)
		expectWindowedOK(mock)
		expectProgressOK(mock)
		mock.ExpectQuery(q("IS NOT NULL" + win + " GROUP BY a.assessor_id")).
			WithArgs(int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("facilitator scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		expectDistAndTopics(mock)
		expectWindowedOK(mock)
		expectProgressOK(mock)
		mock.ExpectQuery(q("IS NOT NULL" + win + " GROUP BY a.assessor_id")).
			WithArgs(int64(1)).
			WillReturnRows(MscShortRow(2))
		res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("facilitator driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := sessionAdminHeaders(t, s, mock)
		expectDistAndTopics(mock)
		expectWindowedOK(mock)
		expectProgressOK(mock)
		mock.ExpectQuery(q("IS NOT NULL" + win + " GROUP BY a.assessor_id")).
			WithArgs(int64(1)).
			WillReturnRows(MscErrRows([]string{"assessor_id", "name", "count"},
				[]any{int64(3), "Fasil", int64(7)}, 0))
		res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// public.go — resolver/builder failures on the report, gallery lock-order
// branches, consent display failures, and every respond failure branch
// (validation, first-wins UPDATE, idempotent re-read, DITOLAK wipe error)
// ---------------------------------------------------------------------------

// mscPublicGalleryLink wires one active GALLERY public link.
func mscPublicGalleryLink(mock sqlmock.Sqlmock, token string) {
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs(token).
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
			"session_topic_id", "child_id", "kind"}).
			AddRow(int64(1), int64(2), int64(5), int64(30), "GALLERY"))
}

func TestMscPublicReportResolverAndBuilderFailures(t *testing.T) {
	t.Run("resolver driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
			WithArgs("tok-r").
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/public/report?token=tok-r", "", nil)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("stale session row is TOKEN_INVALID (builder ErrNoRows)", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
			WithArgs("tok-r").
			WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
				"session_topic_id", "child_id", "kind"}).
				AddRow(int64(1), int64(2), int64(5), int64(30), "REPORT"))
		mock.ExpectQuery(q("SELECT id, name, date, location, status, program_id, program_name_snap")).
			WithArgs(int64(2), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/public/report?token=tok-r", "", nil)
		requireErrEnvelope(t, res, CodeTokenInvalid)
	})

	t.Run("builder driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
			WithArgs("tok-r").
			WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id",
				"session_topic_id", "child_id", "kind"}).
				AddRow(int64(1), int64(2), int64(5), int64(30), "REPORT"))
		mock.ExpectQuery(q("SELECT id, name, date, location, status, program_id, program_name_snap")).
			WithArgs(int64(2), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/public/report?token=tok-r", "", nil)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscPublicGalleryFailureAndPendingBranches(t *testing.T) {
	childOK := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("SELECT id, name, age, school, is_active FROM children")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "age", "school", "is_active"}).
				AddRow(30, "Ayu", 8, "SD Bintang", true))
	}
	sessionOK := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("SELECT id, name, date FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date"}).
				AddRow(2, "Sesi A", fixedTS))
	}

	t.Run("resolver driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
			WithArgs("tok-g").
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/public/gallery?token=tok-g", "", nil)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("unknown child is TOKEN_INVALID", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mscPublicGalleryLink(mock, "tok-g")
		mock.ExpectQuery(q("SELECT id, name, age, school, is_active FROM children")).
			WithArgs(int64(30), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/public/gallery?token=tok-g", "", nil)
		requireErrEnvelope(t, res, CodeTokenInvalid)
	})

	t.Run("unknown session is TOKEN_INVALID", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mscPublicGalleryLink(mock, "tok-g")
		childOK(mock)
		mock.ExpectQuery(q("SELECT id, name, date FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/public/gallery?token=tok-g", "", nil)
		requireErrEnvelope(t, res, CodeTokenInvalid)
	})

	t.Run("consent lookup failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mscPublicGalleryLink(mock, "tok-g")
		childOK(mock)
		sessionOK(mock)
		mock.ExpectQuery(q("SELECT status FROM consent_links WHERE session_id = ? AND child_id = ?")).
			WithArgs(int64(2), int64(30)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/public/gallery?token=tok-g", "", nil)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("pending consent locks with CONSENT_PENDING", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mscPublicGalleryLink(mock, "tok-g")
		childOK(mock)
		sessionOK(mock)
		mock.ExpectQuery(q("SELECT status FROM consent_links WHERE session_id = ? AND child_id = ?")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("PENDING"))
		res := doReq(t, app, "GET", "/public/gallery?token=tok-g", "", nil)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, true, data["locked"])
		require.Equal(t, "CONSENT_PENDING", data["reason"])
		require.Empty(t, data["photos"], "a locked gallery never queries photos")
	})

	t.Run("photo query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mscPublicGalleryLink(mock, "tok-g")
		childOK(mock)
		sessionOK(mock)
		mock.ExpectQuery(q("SELECT status FROM consent_links WHERE session_id = ? AND child_id = ?")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("SETUJU"))
		mock.ExpectQuery(q("AND p.file_ready = TRUE")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/public/gallery?token=tok-g", "", nil)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("photo scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mscPublicGalleryLink(mock, "tok-g")
		childOK(mock)
		sessionOK(mock)
		mock.ExpectQuery(q("SELECT status FROM consent_links WHERE session_id = ? AND child_id = ?")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("SETUJU"))
		// The photo row scan takes 6 destinations; 5 fail mid-iteration.
		mock.ExpectQuery(q("AND p.file_ready = TRUE")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnRows(MscShortRow(5))
		res := doReq(t, app, "GET", "/public/gallery?token=tok-g", "", nil)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("photo driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mscPublicGalleryLink(mock, "tok-g")
		childOK(mock)
		sessionOK(mock)
		mock.ExpectQuery(q("SELECT status FROM consent_links WHERE session_id = ? AND child_id = ?")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("SETUJU"))
		mock.ExpectQuery(q("AND p.file_ready = TRUE")).
			WithArgs(int64(2), int64(30), int64(1)).
			WillReturnRows(MscErrRows(
				[]string{"id", "path", "is_candidate", "created_at", "stid", "topic"},
				[]any{int64(101), "1/2/5/30/x.jpg", false, fixedTS, int64(5), "Topik A"}, 0))
		res := doReq(t, app, "GET", "/public/gallery?token=tok-g", "", nil)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscPublicConsentDisplayFailures(t *testing.T) {
	resolveViaConsent := func(mock sqlmock.Sqlmock, token string) {
		mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
			WithArgs(token).WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
			WithArgs(token).
			WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id", "child_id"}).
				AddRow(int64(1), int64(2), int64(30)))
	}

	t.Run("resolver driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
			WithArgs("tok-c").
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/public/consent?token=tok-c", "", nil)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("consent row vanished is TOKEN_INVALID", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		resolveViaConsent(mock, "tok-c")
		mock.ExpectQuery(q("FROM consent_links cl")).
			WithArgs(int64(2), int64(30)).
			WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/public/consent?token=tok-c", "", nil)
		requireErrEnvelope(t, res, CodeTokenInvalid)
	})

	t.Run("display query driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		resolveViaConsent(mock, "tok-c")
		mock.ExpectQuery(q("FROM consent_links cl")).
			WithArgs(int64(2), int64(30)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/public/consent?token=tok-c", "", nil)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscPublicConsentRespondValidationBranches(t *testing.T) {
	s, _ := newTestServer(t)
	app := newTestApp(t, s)

	// All three guards run before any SQL — a stray query fails the mock.
	res := doReq(t, app, "POST", "/public/consent/respond", "not-json", nil)
	requireErrEnvelope(t, res, CodeValidation)
	res = jsonReq(t, app, "POST", "/public/consent/respond",
		map[string]any{"token": "t", "decision": "SETUJU", "respondent_name": "  "}, nil)
	requireErrEnvelope(t, res, CodeValidation)
	res = jsonReq(t, app, "POST", "/public/consent/respond",
		map[string]any{"token": "", "decision": "SETUJU", "respondent_name": "Ibu"}, nil)
	requireErrEnvelope(t, res, CodeTokenInvalid)
}

// mscRespondClaim queues the idempotency claim pair for one respond key.
func mscRespondClaim(mock sqlmock.Sqlmock, key string) {
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), "POST /public/consent/respond", key).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), "POST /public/consent/respond", key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestMscPublicConsentRespondResolutionFailures(t *testing.T) {
	t.Run("resolver driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
			WithArgs("tok-d").
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "POST", "/public/consent/respond",
			map[string]any{"token": "tok-d", "decision": "SETUJU", "respondent_name": "Ibu"},
			map[string]string{"X-Idempotency-Key": "msc-r1"})
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("consent row vanished is TOKEN_INVALID", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
			WithArgs("tok-d").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
			WithArgs("tok-d").
			WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id", "child_id"}).
				AddRow(int64(1), int64(2), int64(30)))
		mock.ExpectQuery(q("SELECT id FROM consent_links WHERE session_id = ? AND child_id = ?")).
			WithArgs(int64(2), int64(30)).
			WillReturnError(sql.ErrNoRows)
		res := jsonReq(t, app, "POST", "/public/consent/respond",
			map[string]any{"token": "tok-d", "decision": "SETUJU", "respondent_name": "Ibu"},
			map[string]string{"X-Idempotency-Key": "msc-r1"})
		requireErrEnvelope(t, res, CodeTokenInvalid)
	})

	t.Run("consent id lookup driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
			WithArgs("tok-d").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
			WithArgs("tok-d").
			WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id", "child_id"}).
				AddRow(int64(1), int64(2), int64(30)))
		mock.ExpectQuery(q("SELECT id FROM consent_links WHERE session_id = ? AND child_id = ?")).
			WithArgs(int64(2), int64(30)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "POST", "/public/consent/respond",
			map[string]any{"token": "tok-d", "decision": "SETUJU", "respondent_name": "Ibu"},
			map[string]string{"X-Idempotency-Key": "msc-r1"})
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscPublicConsentRespondWriteFailures(t *testing.T) {
	wireResolve := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
			WithArgs("tok-d").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
			WithArgs("tok-d").
			WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id", "child_id"}).
				AddRow(int64(1), int64(2), int64(30)))
		mock.ExpectQuery(q("SELECT id FROM consent_links WHERE session_id = ? AND child_id = ?")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	}
	body := map[string]any{
		"token": "tok-d", "decision": "SETUJU", "respondent_name": "Ibu Ayu",
	}

	t.Run("first-wins UPDATE failure releases the claim", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		wireResolve(mock)
		mscRespondClaim(mock, "msc-w1")
		mock.ExpectExec(q("SET status = ?, respondent_name = ?, responded_at = NOW()")).
			WithArgs("SETUJU", "Ibu Ayu", int64(7)).
			WillReturnError(errors.New("connection reset"))
		// A TOP-level DB_UNAVAILABLE releases the claim instead of recording.
		mock.ExpectExec(q("DELETE FROM idempotency_keys")).
			WithArgs(int64(1), "POST /public/consent/respond", "msc-w1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		res := jsonReq(t, app, "POST", "/public/consent/respond", body,
			map[string]string{"X-Idempotency-Key": "msc-w1"})
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("already-decided re-read vanished is TOKEN_INVALID and stays recorded", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		wireResolve(mock)
		mscRespondClaim(mock, "msc-w2")
		// 0 rows affected → the row is already decided elsewhere.
		mock.ExpectExec(q("SET status = ?, respondent_name = ?, responded_at = NOW()")).
			WithArgs("SETUJU", "Ibu Ayu", int64(7)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(q("SELECT status FROM consent_links WHERE id = ?")).
			WithArgs(int64(7)).
			WillReturnError(sql.ErrNoRows)
		// TOKEN_INVALID is NOT a DB failure: the envelope is recorded.
		mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
			WithArgs(sqlmock.AnyArg(), int64(1), "POST /public/consent/respond", "msc-w2").
			WillReturnResult(sqlmock.NewResult(0, 1))
		res := jsonReq(t, app, "POST", "/public/consent/respond", body,
			map[string]string{"X-Idempotency-Key": "msc-w2"})
		requireErrEnvelope(t, res, CodeTokenInvalid)
	})

	t.Run("already-decided re-read driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		wireResolve(mock)
		mscRespondClaim(mock, "msc-w3")
		mock.ExpectExec(q("SET status = ?, respondent_name = ?, responded_at = NOW()")).
			WithArgs("SETUJU", "Ibu Ayu", int64(7)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(q("SELECT status FROM consent_links WHERE id = ?")).
			WithArgs(int64(7)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		mock.ExpectExec(q("DELETE FROM idempotency_keys")).
			WithArgs(int64(1), "POST /public/consent/respond", "msc-w3").
			WillReturnResult(sqlmock.NewResult(0, 1))
		res := jsonReq(t, app, "POST", "/public/consent/respond", body,
			map[string]string{"X-Idempotency-Key": "msc-w3"})
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("DITOLAK wipe failure logs and records without photo/gallery publishes", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		adminSub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})
		defer s.unsubscribe(adminSub)

		wireResolve(mock)
		mscRespondClaim(mock, "msc-w4")
		mock.ExpectExec(q("SET status = ?, respondent_name = ?, responded_at = NOW()")).
			WithArgs("DITOLAK", "Ibu Ayu", int64(7)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		// The photo wipe's own Tx fails to begin — deletion never fails the
		// recorded decision, and a deletion that did NOT happen is never
		// announced: no photo.changed / gallery.changed may follow.
		mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
		mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
			WithArgs(sqlmock.AnyArg(), int64(1), "POST /public/consent/respond", "msc-w4").
			WillReturnResult(sqlmock.NewResult(0, 1))

		res := jsonReq(t, app, "POST", "/public/consent/respond",
			map[string]any{"token": "tok-d", "decision": "DITOLAK",
				"respondent_name": "Ibu Ayu"},
			map[string]string{"X-Idempotency-Key": "msc-w4"})
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, "DITOLAK", data["decision"])
		_, already := data["already"]
		require.False(t, already, "the recorded decision carries no already flag")

		// consent.changed + consent.ack are unconditional (the decision IS
		// recorded); they are the ONLY frames a failed wipe may publish.
		require.Equal(t, "consent.changed", covNextFrame(t, adminSub, "consent refetch").Entity)
		require.Equal(t, "consent.ack", covNextFrame(t, adminSub, "public ack").Entity)
		covNoFrame(t, adminSub,
			"the failed wipe must not publish photo.changed or gallery.changed")
	})
}

// ---------------------------------------------------------------------------
// schools.go — list rows failures, INSERT/reload failures, patch/active guard
// and write failures, and the hard-delete Tx failure branches
// ---------------------------------------------------------------------------

func TestMscSchoolsListRowsFailureBranches(t *testing.T) {
	rowsFrag := q("FROM schools WHERE 1=1 ORDER BY id asc, id ASC LIMIT ? OFFSET ?")

	t.Run("rows query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM schools WHERE 1=1")).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(rowsFrag).WithArgs(20, 0).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/schools", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("row scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM schools WHERE 1=1")).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		// schoolItem scans 6 destinations; 4 fail mid-iteration.
		mock.ExpectQuery(rowsFrag).WithArgs(20, 0).
			WillReturnRows(MscShortRow(4))
		res := doReq(t, app, "GET", "/api/schools", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("driver error after rows is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM schools WHERE 1=1")).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(rowsFrag).WithArgs(20, 0).
			WillReturnRows(MscErrRows(
				[]string{"id", "school_name", "country", "city", "is_active", "created_at"},
				[]any{int64(1), "Bandung", "Indonesia", nil, true, fixedTS}, 0))
		res := doReq(t, app, "GET", "/api/schools", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscSchoolsCreateInsertFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectSAAuth(mock, 0)
	mock.ExpectExec(q("INSERT INTO schools (school_name, country, city, is_active) VALUES (?, ?, ?, TRUE)")).
		WithArgs("Bandung", "Indonesia", nil).
		WillReturnError(errors.New("disk full"))
	res := jsonReq(t, app, "POST", "/api/schools",
		map[string]any{"school_name": "Bandung"}, hSABare(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

func TestMscSchoolsGetReloadFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectSAAuth(mock, 0)
	mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
		WillReturnError(errors.New("dial tcp: connection refused"))
	res := doReq(t, app, "GET", "/api/schools/3", "", hSABare(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

func TestMscSchoolsPatchFailureBranches(t *testing.T) {
	loadCur := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
			WillReturnRows(sqlSchoolItemRow(3, "Lama", "Indonesia", true))
	}

	t.Run("malformed id is NOT_FOUND without any lookup", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		res := jsonReq(t, app, "PATCH", "/api/schools/abc",
			map[string]any{"school_name": "X"}, hSABare(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("malformed body is VALIDATION before the load", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		res := doReq(t, app, "PATCH", "/api/schools/3", "not-json", hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("load failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "PATCH", "/api/schools/3",
			map[string]any{"school_name": "Baru"}, hSABare(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("is_active flip writes and reloads", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 3)
		defer s.unsubscribe(spy)

		expectSAAuth(mock, 0)
		loadCur(mock)
		mock.ExpectExec(q("UPDATE schools SET is_active = ? WHERE id = ?")).
			WithArgs(false, int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
			WillReturnRows(sqlSchoolItemRow(3, "Lama", "Indonesia", false))

		res := jsonReq(t, app, "PATCH", "/api/schools/3",
			map[string]any{"is_active": false}, hSABare(t, s))
		school := requireDataEnvelope(t, res).(map[string]any)["school"].(map[string]any)
		require.Equal(t, false, school["is_active"])
		require.Equal(t, "updated", takeFrame(t, spy).Action)
	})

	t.Run("non-duplicate UPDATE failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		loadCur(mock)
		mock.ExpectExec(q("UPDATE schools SET is_active = ? WHERE id = ?")).
			WithArgs(false, int64(3)).
			WillReturnError(errors.New("disk full"))
		res := jsonReq(t, app, "PATCH", "/api/schools/3",
			map[string]any{"is_active": false}, hSABare(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("reload driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		loadCur(mock)
		mock.ExpectExec(q("UPDATE schools SET is_active = ? WHERE id = ?")).
			WithArgs(false, int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "PATCH", "/api/schools/3",
			map[string]any{"is_active": false}, hSABare(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscSchoolsActiveFailureBranches(t *testing.T) {
	t.Run("malformed id is NOT_FOUND without the UPDATE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		res := jsonReq(t, app, "POST", "/api/schools/abc/activate", nil, hSABare(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("UPDATE failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		mock.ExpectExec(q("UPDATE schools SET is_active = ? WHERE id = ?")).
			WithArgs(false, int64(3)).
			WillReturnError(errors.New("connection reset"))
		res := jsonReq(t, app, "POST", "/api/schools/3/deactivate", nil, hSABare(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("reload driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		mock.ExpectExec(q("UPDATE schools SET is_active = ? WHERE id = ?")).
			WithArgs(true, int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "POST", "/api/schools/3/activate", nil, hSABare(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscSchoolsDeleteTxFailureBranches(t *testing.T) {
	t.Run("malformed id is NOT_FOUND without BeginTx", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		res := doReq(t, app, "DELETE", "/api/schools/abc", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("BeginTx failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "DELETE", "/api/schools/10", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("wipe-table DELETE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		mock.ExpectBegin()
		mock.ExpectExec(q("DELETE FROM users WHERE school_id = ?")).
			WithArgs(int64(10)).WillReturnError(errors.New("foreign key constraint"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/schools/10", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("schools row DELETE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		mock.ExpectBegin()
		for _, table := range schoolWipeOrder {
			mock.ExpectExec(q("DELETE FROM " + table + " WHERE school_id = ?")).
				WithArgs(int64(10)).WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectExec(q("DELETE FROM schools WHERE id = ?")).
			WithArgs(int64(10)).WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/schools/10", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("commit failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		mock.ExpectBegin()
		for _, table := range schoolWipeOrder {
			mock.ExpectExec(q("DELETE FROM " + table + " WHERE school_id = ?")).
				WithArgs(int64(10)).WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectExec(q("DELETE FROM schools WHERE id = ?")).
			WithArgs(int64(10)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(errors.New("driver: bad connection"))
		res := doReq(t, app, "DELETE", "/api/schools/10", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// children.go — list rows failures and the deleteTx collect/exec/commit
// failure branches plus the best-effort disk sweep
// ---------------------------------------------------------------------------

func TestMscChildrenListRowsFailureBranches(t *testing.T) {
	rowsFrag := q("FROM children WHERE school_id = ? ORDER BY id asc, id ASC LIMIT ? OFFSET ?")

	t.Run("row scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM children WHERE school_id = ?")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		// childSelect scans 12 destinations; 11 fail mid-iteration.
		mock.ExpectQuery(rowsFrag).WithArgs(int64(1), 20, 0).
			WillReturnRows(MscShortRow(11))
		res := doReq(t, app, "GET", "/api/children", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("driver error after rows is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM children WHERE school_id = ?")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(rowsFrag).WithArgs(int64(1), 20, 0).
			WillReturnRows(MscErrRows(childrenTestCols,
				[]any{int64(3), "Ayu", 8, false, "SD 1", "Ibu A", "+62811111111",
					nil, nil, nil, true, fixedTS}, 0))
		res := doReq(t, app, "GET", "/api/children", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscChildrenDeleteLoadFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectAdminAuth(mock)
	mock.ExpectQuery(q(childrenByIDFrag)).WithArgs(int64(30), int64(1)).
		WillReturnError(errors.New("dial tcp: connection refused"))
	res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

func TestMscChildrenDeleteCollectQueryFailures(t *testing.T) {
	loadCur := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q(childrenByIDFrag)).WithArgs(int64(30), int64(1)).
			WillReturnRows(childrenTestRow(30, "Citra", 9, false, "SD 1", "Ibu C",
				"+62833333333", nil, nil, nil, true))
		mock.ExpectBegin()
	}
	photosFrag := q("SELECT path FROM photos WHERE child_id = ? AND school_id = ?")
	publicFrag := q("SELECT id, session_id, token FROM public_links WHERE child_id = ? AND school_id = ?")
	consentFrag := q("SELECT id, session_id, token FROM consent_links WHERE child_id = ? AND school_id = ?")

	t.Run("photos query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectQuery(photosFrag).WithArgs(int64(30), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("photos scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectQuery(photosFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(MscShortRow(1))
		res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("photos driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectQuery(photosFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(MscErrRows([]string{"path"},
				[]any{"1/a.jpg"}, 0))
		res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("public link query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectQuery(photosFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"path"}))
		mock.ExpectQuery(publicFrag).WithArgs(int64(30), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("public link scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectQuery(photosFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"path"}))
		mock.ExpectQuery(publicFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(MscShortRow(2))
		res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("public link driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectQuery(photosFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"path"}))
		mock.ExpectQuery(publicFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(MscErrRows([]string{"id", "session_id", "token"},
				[]any{int64(90), int64(5), "tokA"}, 0))
		res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("consent link query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectQuery(photosFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"path"}))
		mock.ExpectQuery(publicFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_id", "token"}))
		mock.ExpectQuery(consentFrag).WithArgs(int64(30), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("consent link scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectQuery(photosFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"path"}))
		mock.ExpectQuery(publicFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_id", "token"}))
		mock.ExpectQuery(consentFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(MscShortRow(2))
		res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("consent link driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectQuery(photosFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"path"}))
		mock.ExpectQuery(publicFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_id", "token"}))
		mock.ExpectQuery(consentFrag).WithArgs(int64(30), int64(1)).
			WillReturnRows(MscErrRows([]string{"id", "session_id", "token"},
				[]any{int64(91), int64(5), "tokB"}, 0))
		res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscChildrenDeleteExecAndCommitFailures(t *testing.T) {
	loadCur := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q(childrenByIDFrag)).WithArgs(int64(30), int64(1)).
			WillReturnRows(childrenTestRow(30, "Citra", 9, false, "SD 1", "Ibu C",
				"+62833333333", nil, nil, nil, true))
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT path FROM photos WHERE child_id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"path"}))
		mock.ExpectQuery(q("SELECT id, session_id, token FROM public_links WHERE child_id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_id", "token"}))
		mock.ExpectQuery(q("SELECT id, session_id, token FROM consent_links WHERE child_id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_id", "token"}))
	}

	t.Run("first child-scoped DELETE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectExec(q("DELETE FROM session_members WHERE child_id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).
			WillReturnError(errors.New("foreign key constraint"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("child row DELETE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		for _, table := range []string{"session_members", "assessments", "photos",
			"narratives", "report_missions", "consent_links", "public_links"} {
			mock.ExpectExec(q("DELETE FROM "+table+" WHERE child_id = ? AND school_id = ?")).
				WithArgs(int64(30), int64(1)).
				WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectExec(q("DELETE FROM children WHERE id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).
			WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("commit failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		for _, table := range []string{"session_members", "assessments", "photos",
			"narratives", "report_missions", "consent_links", "public_links"} {
			mock.ExpectExec(q("DELETE FROM "+table+" WHERE child_id = ? AND school_id = ?")).
				WithArgs(int64(30), int64(1)).
				WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectExec(q("DELETE FROM children WHERE id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(errors.New("driver: bad connection"))
		res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscChildrenDeleteUnremovablePhotoFileStillSucceeds(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	// The collected photo path is a NON-EMPTY directory: the post-commit
	// os.Remove fails and only logs — the delete still answers {deleted:true}.
	MscUnremovablePath(t, s, "1/x/photo.jpg")

	expectAdminAuth(mock)
	mock.ExpectQuery(q(childrenByIDFrag)).WithArgs(int64(30), int64(1)).
		WillReturnRows(childrenTestRow(30, "Citra", 9, false, "SD 1", "Ibu C",
			"+62833333333", nil, nil, nil, true))
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT path FROM photos WHERE child_id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("1/x/photo.jpg"))
	mock.ExpectQuery(q("SELECT id, session_id, token FROM public_links WHERE child_id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id", "token"}))
	mock.ExpectQuery(q("SELECT id, session_id, token FROM consent_links WHERE child_id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id", "token"}))
	for _, table := range []string{"session_members", "assessments", "photos",
		"narratives", "report_missions", "consent_links", "public_links"} {
		mock.ExpectExec(q("DELETE FROM "+table+" WHERE child_id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(q("DELETE FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"],
		"an unremovable file never fails the committed delete")
	require.DirExists(t, filepath.Join(s.upload, "1", "x", "photo.jpg"),
		"the unremovable directory stays behind for the janitor")
}

// ---------------------------------------------------------------------------
// activities.go — list rows failures, create guard/lookup branches, route
// guard-false branches and the reload failures of get/patch/delete
// ---------------------------------------------------------------------------

func TestMscActivitiesListRowsFailureBranches(t *testing.T) {
	t.Run("row scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(actCountFrag)).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		// activitySelect scans 8 destinations; 7 fail mid-iteration.
		mock.ExpectQuery(q(actRowsFrag)).WithArgs(int64(1), 20, 0).
			WillReturnRows(MscShortRow(7))
		res := doReq(t, app, "GET", "/api/activities", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("driver error after rows is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(actCountFrag)).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(q(actRowsFrag)).WithArgs(int64(1), 20, 0).
			WillReturnRows(MscErrRows(
				[]string{"id", "program_id", "topic_id", "program_name_snap",
					"topic_name_snap", "name", "indicator", "sort_order"},
				[]any{int64(1), int64(3), int64(5), "Prog", "Topik", "Aktivitas",
					"Indikator", 1}, 0))
		res := doReq(t, app, "GET", "/api/activities", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscActivitiesCreateGuardAndLookupFailures(t *testing.T) {
	body := map[string]any{
		"program_id": 3, "topic_id": 5, "name": "Aktivitas", "sort_order": 1,
	}

	t.Run("malformed body is VALIDATION before the school guard", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		res := doReq(t, app, "POST", "/api/activities", "not-json", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("schoolless SA is VALIDATION before any lookup", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		res := jsonReq(t, app, "POST", "/api/activities", body, hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("program lookup driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(actProgFrag)).WithArgs(int64(3), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "POST", "/api/activities", body, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("topic lookup driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(actProgFrag)).WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
		mock.ExpectQuery(q(actTopicFrag)).WithArgs(int64(5), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "POST", "/api/activities", body, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("sort append probe failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(actProgFrag)).WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
		mock.ExpectQuery(q(actTopicFrag)).WithArgs(int64(5), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "program_id"}).
				AddRow("Topik Membaca", int64(3)))
		// sort_order omitted → the MAX probe runs and fails.
		mock.ExpectQuery(q(actMaxFrag)).WithArgs(int64(5), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "POST", "/api/activities",
			map[string]any{"program_id": 3, "topic_id": 5, "name": "Aktivitas"},
			hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscActivitiesRouteGuardFalseBranches(t *testing.T) {
	t.Run("malformed id and schoolless SA are single envelopes", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		for _, tc := range []struct {
			method, target, body string
			sa                   bool
		}{
			{"GET", "/api/activities/abc", "", false},
			{"GET", "/api/activities/1", "", true},
			{"PATCH", "/api/activities/abc", `{"name":"X"}`, false},
			{"PATCH", "/api/activities/1", `{"name":"X"}`, true},
			{"DELETE", "/api/activities/abc", "", false},
			{"DELETE", "/api/activities/1", "", true},
		} {
			var h map[string]string
			if tc.sa {
				expectSAAuth(mock, 0)
				h = hSABare(t, s)
			} else {
				expectAdminAuth(mock)
				h = hAdmin(t, s)
			}
			res := doReq(t, app, tc.method, tc.target, tc.body, h)
			if tc.sa {
				requireErrEnvelope(t, res, CodeValidation)
			} else {
				requireErrEnvelope(t, res, CodeNotFound)
			}
		}
	})

	t.Run("patch with a malformed body is VALIDATION before the load", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		res := doReq(t, app, "PATCH", "/api/activities/1", "not-json", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})
}

func TestMscActivitiesReloadFailureBranches(t *testing.T) {
	t.Run("get load failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(1), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/activities/1", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("patch load failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(1), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "PATCH", "/api/activities/1",
			map[string]any{"name": "Baru"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("patch reload failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(1), int64(1)).
			WillReturnRows(actRow(1, 3, 5, "Prog", "Topik", "Aktivitas", nil, 1))
		mock.ExpectExec(q(actUpdFrag)).
			WithArgs("Baru", nil, 1, int64(1), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(1), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "PATCH", "/api/activities/1",
			map[string]any{"name": "Baru"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("delete load failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(1), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "DELETE", "/api/activities/1", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscActivitiesPatchIndicatorSetPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(1), int64(1)).
		WillReturnRows(actRow(1, 3, 5, "Prog", "Topik", "Aktivitas", nil, 1))
	mock.ExpectExec(q(actUpdFrag)).
		WithArgs("Aktivitas", "Indikator baru", 1, int64(1), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q(actByIDFrag)).WithArgs(int64(1), int64(1)).
		WillReturnRows(actRow(1, 3, 5, "Prog", "Topik", "Aktivitas", "Indikator baru", 1))

	res := jsonReq(t, app, "PATCH", "/api/activities/1",
		map[string]any{"indicator": "Indikator baru"}, hAdmin(t, s))
	activity := requireDataEnvelope(t, res).(map[string]any)["activity"].(map[string]any)
	require.Equal(t, "Indikator baru", activity["indicator"],
		"a non-empty indicator replaces the NULL snapshot")
	require.Equal(t, "activity.changed", takeFrame(t, spy).Entity)
}

// ---------------------------------------------------------------------------
// frames.go — create/patch/delete Tx failure branches and the patch image
// rejection; reload/link-map failures answer DB_UNAVAILABLE
// ---------------------------------------------------------------------------

func TestMscFramesCreateTxFailureBranches(t *testing.T) {
	wirePrograms := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(frameProgsQ(1)).
			WithArgs(int64(1), int64(5)).
			WillReturnRows(sqlmock.NewRows(frameProgCols).AddRow(5, "Lima"))
	}

	t.Run("BeginTx failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		wirePrograms(mock)
		mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
		res := doMP(t, app, "POST", "/api/frames",
			map[string]string{"name": "Poster", "program_ids": "5"}, frameImgFile(), hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("frame_programs INSERT failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		wirePrograms(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("INSERT INTO frames (school_id, image_path, name) VALUES (?, ?, ?)")).
			WithArgs(int64(1), sqlmock.AnyArg(), "Poster").
			WillReturnResult(sqlmock.NewResult(7, 1))
		mock.ExpectExec(q(`INSERT INTO frame_programs (frame_id, program_id, program_name_snap, school_id)`)).
			WithArgs(int64(7), int64(5), "Lima", int64(1)).
			WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()
		res := doMP(t, app, "POST", "/api/frames",
			map[string]string{"name": "Poster", "program_ids": "5"}, frameImgFile(), hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("commit failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		wirePrograms(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("INSERT INTO frames (school_id, image_path, name) VALUES (?, ?, ?)")).
			WithArgs(int64(1), sqlmock.AnyArg(), "Poster").
			WillReturnResult(sqlmock.NewResult(7, 1))
		mock.ExpectExec(q(`INSERT INTO frame_programs (frame_id, program_id, program_name_snap, school_id)`)).
			WithArgs(int64(7), int64(5), "Lima", int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(errors.New("driver: bad connection"))
		res := doMP(t, app, "POST", "/api/frames",
			map[string]string{"name": "Poster", "program_ids": "5"}, frameImgFile(), hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("reload driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		wirePrograms(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("INSERT INTO frames (school_id, image_path, name) VALUES (?, ?, ?)")).
			WithArgs(int64(1), sqlmock.AnyArg(), "Poster").
			WillReturnResult(sqlmock.NewResult(7, 1))
		mock.ExpectExec(q(`INSERT INTO frame_programs (frame_id, program_id, program_name_snap, school_id)`)).
			WithArgs(int64(7), int64(5), "Lima", int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(q(frameItemFrag)).WithArgs(int64(7), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doMP(t, app, "POST", "/api/frames",
			map[string]string{"name": "Poster", "program_ids": "5"}, frameImgFile(), hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("post-commit link query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		wirePrograms(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("INSERT INTO frames (school_id, image_path, name) VALUES (?, ?, ?)")).
			WithArgs(int64(1), sqlmock.AnyArg(), "Poster").
			WillReturnResult(sqlmock.NewResult(7, 1))
		mock.ExpectExec(q(`INSERT INTO frame_programs (frame_id, program_id, program_name_snap, school_id)`)).
			WithArgs(int64(7), int64(5), "Lima", int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(q(frameItemFrag)).WithArgs(int64(7), int64(1)).
			WillReturnRows(frameItemRows(7, frameRelNew, "Poster"))
		mock.ExpectQuery(frameLinkQ(1)).WithArgs(int64(1), int64(7)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doMP(t, app, "POST", "/api/frames",
			map[string]string{"name": "Poster", "program_ids": "5"}, frameImgFile(), hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscFramesPatchEmptyNameIsValidation(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectAdminAuth(mock)
	mock.ExpectQuery(q(frameItemFrag)).WithArgs(int64(5), int64(1)).
		WillReturnRows(frameItemRows(5, frameRelOld, "Lama"))
	// An explicitly blank name fails the non-empty rule before any image is
	// stored or any Tx opens (upload rules are client-side only — Pembaruan
	// 2026-10-11 — so bytes can no longer trigger VALIDATION here).
	res := doMP(t, app, "PATCH", "/api/frames/5",
		map[string]string{"name": ""}, nil, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeValidation)
}

func TestMscFramesPatchTxFailureBranches(t *testing.T) {
	loadCur := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q(frameItemFrag)).WithArgs(int64(5), int64(1)).
			WillReturnRows(frameItemRows(5, frameRelOld, "Lama"))
	}

	t.Run("BeginTx failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
		res := doMP(t, app, "PATCH", "/api/frames/5",
			map[string]string{"name": "Baru"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("frames UPDATE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("UPDATE frames SET name = ?, image_path = ? WHERE id = ? AND school_id = ?")).
			WithArgs("Baru", frameRelOld, int64(5), int64(1)).
			WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()
		res := doMP(t, app, "PATCH", "/api/frames/5",
			map[string]string{"name": "Baru"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("frame_programs DELETE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectQuery(frameProgsQ(1)).
			WithArgs(int64(1), int64(5)).
			WillReturnRows(sqlmock.NewRows(frameProgCols).AddRow(5, "Lima"))
		mock.ExpectBegin()
		mock.ExpectExec(q("UPDATE frames SET name = ?, image_path = ? WHERE id = ? AND school_id = ?")).
			WithArgs("Baru", frameRelOld, int64(5), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q("DELETE FROM frame_programs WHERE frame_id = ? AND school_id = ?")).
			WithArgs(int64(5), int64(1)).
			WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()
		res := doMP(t, app, "PATCH", "/api/frames/5",
			map[string]string{"name": "Baru", "program_ids": "5"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("frame_programs INSERT failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameItemFrag)).WithArgs(int64(5), int64(1)).
			WillReturnRows(frameItemRows(5, frameRelOld, "Lama"))
		mock.ExpectQuery(frameProgsQ(1)).
			WithArgs(int64(1), int64(5)).
			WillReturnRows(sqlmock.NewRows(frameProgCols).AddRow(5, "Lima"))
		mock.ExpectBegin()
		mock.ExpectExec(q("UPDATE frames SET name = ?, image_path = ? WHERE id = ? AND school_id = ?")).
			WithArgs("Lama", frameRelOld, int64(5), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q("DELETE FROM frame_programs WHERE frame_id = ? AND school_id = ?")).
			WithArgs(int64(5), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(q(`INSERT INTO frame_programs (frame_id, program_id, program_name_snap, school_id)`)).
			WithArgs(int64(5), int64(5), "Lima", int64(1)).
			WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()
		res := doMP(t, app, "PATCH", "/api/frames/5",
			map[string]string{"program_ids": "5"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("commit failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("UPDATE frames SET name = ?, image_path = ? WHERE id = ? AND school_id = ?")).
			WithArgs("Baru", frameRelOld, int64(5), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(errors.New("driver: bad connection"))
		res := doMP(t, app, "PATCH", "/api/frames/5",
			map[string]string{"name": "Baru"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("reload driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("UPDATE frames SET name = ?, image_path = ? WHERE id = ? AND school_id = ?")).
			WithArgs("Baru", frameRelOld, int64(5), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(q(frameItemFrag)).WithArgs(int64(5), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doMP(t, app, "PATCH", "/api/frames/5",
			map[string]string{"name": "Baru"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("post-commit link query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("UPDATE frames SET name = ?, image_path = ? WHERE id = ? AND school_id = ?")).
			WithArgs("Baru", frameRelOld, int64(5), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(q(frameItemFrag)).WithArgs(int64(5), int64(1)).
			WillReturnRows(frameItemRows(5, frameRelOld, "Baru"))
		mock.ExpectQuery(frameLinkQ(1)).WithArgs(int64(1), int64(5)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doMP(t, app, "PATCH", "/api/frames/5",
			map[string]string{"name": "Baru"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscFramesDeleteTxFailureBranches(t *testing.T) {
	loadCur := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q(frameItemFrag)).WithArgs(int64(5), int64(1)).
			WillReturnRows(frameItemRows(5, frameRelOld, "Lama"))
	}

	t.Run("BeginTx failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "DELETE", "/api/frames/5", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("link DELETE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("DELETE FROM frame_programs WHERE frame_id = ? AND school_id = ?")).
			WithArgs(int64(5), int64(1)).
			WillReturnError(errors.New("connection reset"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/frames/5", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("frame row DELETE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("DELETE FROM frame_programs WHERE frame_id = ? AND school_id = ?")).
			WithArgs(int64(5), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(q("DELETE FROM frames WHERE id = ? AND school_id = ?")).
			WithArgs(int64(5), int64(1)).
			WillReturnError(errors.New("foreign key constraint"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/frames/5", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("commit failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q("DELETE FROM frame_programs WHERE frame_id = ? AND school_id = ?")).
			WithArgs(int64(5), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(q("DELETE FROM frames WHERE id = ? AND school_id = ?")).
			WithArgs(int64(5), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(errors.New("driver: bad connection"))
		res := doReq(t, app, "DELETE", "/api/frames/5", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// topics.go — schoolless guard branches, list rows.Err, create lookup/append
// failures incl. the raw storeImage error (500), badge branches, delete Tx
// ---------------------------------------------------------------------------

func TestMscTopicsGuardAndListFailureBranches(t *testing.T) {
	t.Run("schoolless SA is VALIDATION on list/get/patch/delete", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		for _, tc := range []struct {
			method, target string
		}{
			{"GET", "/api/topics"},
			{"POST", "/api/topics"},
			{"GET", "/api/topics/1"},
			{"PATCH", "/api/topics/1"},
			{"DELETE", "/api/topics/1"},
		} {
			expectSAAuth(mock, 0)
			res := doReq(t, app, tc.method, tc.target, "", hSABare(t, s))
			requireErrEnvelope(t, res, CodeValidation)
		}
	})

	t.Run("malformed id is NOT_FOUND without any lookup", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		res := doReq(t, app, "PATCH", "/api/topics/abc", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
		expectAdminAuth(mock)
		res = doReq(t, app, "DELETE", "/api/topics/abc", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("non-multipart create body is VALIDATION before any SQL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		res := jsonReq(t, app, "POST", "/api/topics",
			map[string]any{"name": "Topik"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("list driver error after rows is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicCount)).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(q(sqlTopicListFrom)+
			q(" ORDER BY sort_order asc, id ASC LIMIT ? OFFSET ?")).
			WithArgs(int64(1), 20, 0).
			WillReturnRows(MscErrRows(topicRowCols,
				[]any{int64(1), int64(5), "Prog", "Topik", nil, 1, nil, nil, true}, 0))
		res := doReq(t, app, "GET", "/api/topics", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscTopicsCreateLookupFailures(t *testing.T) {
	baseFields := map[string]string{"name": "Topik", "program_id": "3"}

	t.Run("program lookup driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicProgram)).WithArgs(int64(3), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doMP(t, app, "POST", "/api/topics", baseFields, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("sort append probe failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicProgram)).WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
		mock.ExpectQuery(q(sqlTopicNextSort)).WithArgs(int64(3), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doMP(t, app, "POST", "/api/topics", baseFields, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("raw badge store failure is the HTTP 500 INTERNAL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		// uploads/badges is a FILE: MkdirAll(badges/1) fails with a raw error
		// the handler rethrows → ErrorHandler → 500.
		MscBlockSchoolUpload(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlTopicProgram)).WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
		res := doMP(t, app, "POST", "/api/topics",
			map[string]string{"name": "Topik", "program_id": "3", "sort_order": "1",
				"badge_name": "Medali"},
			map[string]string{"badge_image": string(framesPNG)}, hAdmin(t, s))
		MscRequireInternal(t, res)
	})
}

func TestMscTopicsCreateBadgePairPersists(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlTopicProgram)).WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Progam Literasi"))
	mock.ExpectExec(q(sqlTopicInsert)).
		WithArgs(int64(1), int64(3), "Progam Literasi", "Topik", nil, 1,
			"Medali", sqlmock.AnyArg(), true).
		WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(9), int64(1)).
		WillReturnRows(topicRow(9, 3, "Progam Literasi", "Topik", nil, 1,
			"Medali", "badges/1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png", true))

	res := doMP(t, app, "POST", "/api/topics",
		map[string]string{"name": "Topik", "program_id": "3", "sort_order": "1",
			"badge_name": "Medali"},
		map[string]string{"badge_image": string(framesPNG)}, hAdmin(t, s))
	topic := requireDataEnvelope(t, res).(map[string]any)["topic"].(map[string]any)
	require.Equal(t, "Medali", topic["badge_name"])
	require.Equal(t, "/uploads/badges/1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png",
		topic["badge_image_url"], "the stored badge path is served off-wire")
	require.Equal(t, "topic.changed", takeFrame(t, spy).Entity)
}

func TestMscTopicsPatchBadgeFailureBranches(t *testing.T) {
	loadCur := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(3), int64(1)).
			WillReturnRows(topicRow(3, 5, "Math", "Old Topic", nil, 4, nil, nil, true))
	}

	t.Run("raw badge store failure is the HTTP 500 INTERNAL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		MscBlockSchoolUpload(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		res := doMP(t, app, "PATCH", "/api/topics/3",
			map[string]string{"badge_name": "Medali"},
			map[string]string{"badge_image": string(framesPNG)}, hAdmin(t, s))
		MscRequireInternal(t, res)
	})

	t.Run("badge name without image on an unbadged topic is VALIDATION", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		// validBadgePair("Medali","") fails both-or-neither → VALIDATION
		// before any UPDATE.
		res := doMP(t, app, "PATCH", "/api/topics/3",
			map[string]string{"badge_name": "Medali"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("badge pair upload persists the new snapshot", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectExec(q(sqlTopicUpdate)).
			WithArgs("Old Topic", nil, 4, "Medali", sqlmock.AnyArg(), true,
				int64(3), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(3), int64(1)).
			WillReturnRows(topicRow(3, 5, "Math", "Old Topic", nil, 4,
				"Medali", "badges/1/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.png", true))

		res := doMP(t, app, "PATCH", "/api/topics/3",
			map[string]string{"badge_name": "Medali"},
			map[string]string{"badge_image": string(framesPNG)}, hAdmin(t, s))
		topic := requireDataEnvelope(t, res).(map[string]any)["topic"].(map[string]any)
		require.Equal(t, "/uploads/badges/1/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.png",
			topic["badge_image_url"])
		require.Equal(t, "topic.changed", takeFrame(t, spy).Entity)
	})

	t.Run("reload driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectExec(q(sqlTopicUpdate)).
			WithArgs("Old Topic", nil, 4, nil, nil, true, int64(3), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(3), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doMP(t, app, "PATCH", "/api/topics/3",
			map[string]string{"name": "Old Topic"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestMscTopicsDeleteTxFailureBranches(t *testing.T) {
	loadCur := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q(sqlTopicByID)).WithArgs(int64(3), int64(1)).
			WillReturnRows(topicRow(3, 5, "Math", "Old Topic", nil, 4, nil, nil, true))
	}

	t.Run("BeginTx failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "DELETE", "/api/topics/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("topic row DELETE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q(sqlTopicDelLinks)).WithArgs(int64(3), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(q(sqlTopicDelRow)).WithArgs(int64(3), int64(1)).
			WillReturnError(errors.New("foreign key constraint"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/topics/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("commit failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q(sqlTopicDelLinks)).WithArgs(int64(3), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(q(sqlTopicDelRow)).WithArgs(int64(3), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(errors.New("driver: bad connection"))
		res := doReq(t, app, "DELETE", "/api/topics/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// programs.go — guard branches, list rows.Err, raw badge store failures (500),
// patch field/badge branches, removeMasterImage disk failure, delete Tx
// ---------------------------------------------------------------------------

func TestMscProgramsGuardFalseBranches(t *testing.T) {
	t.Run("schoolless SA is VALIDATION on create/get/patch/delete", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		for _, tc := range []struct {
			method, target string
		}{
			{"POST", "/api/programs"},
			{"GET", "/api/programs/1"},
			{"PATCH", "/api/programs/1"},
			{"DELETE", "/api/programs/1"},
		} {
			expectSAAuth(mock, 0)
			res := doReq(t, app, tc.method, tc.target, "", hSABare(t, s))
			requireErrEnvelope(t, res, CodeValidation)
		}
	})

	t.Run("malformed id is NOT_FOUND without any lookup", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		res := doReq(t, app, "PATCH", "/api/programs/abc", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
		expectAdminAuth(mock)
		res = doReq(t, app, "DELETE", "/api/programs/abc", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})
}

func TestMscProgramsListRowsErrIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlProgCount)).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q(sqlProgListFrom)+
		q(" ORDER BY id asc, p.id ASC LIMIT ? OFFSET ?")).
		WithArgs(int64(1), 20, 0).
		WillReturnRows(MscErrRows(programRowCols,
			[]any{int64(1), "Prog", nil, nil, nil, true, fixedTS,
				int64(2), int64(5), int64(0)}, 0))
	res := doReq(t, app, "GET", "/api/programs", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

func TestMscProgramsCreateBadgeStoreErrorIs500(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	// uploads/badges is a FILE: the badge MkdirAll fails raw → returned → 500.
	MscBlockSchoolUpload(t, s)
	expectAdminAuth(mock)
	res := doMP(t, app, "POST", "/api/programs",
		map[string]string{"name": "Prog", "badge_name": "Medali"},
		map[string]string{"badge_image": string(framesPNG)}, hAdmin(t, s))
	MscRequireInternal(t, res)
}

func TestMscProgramsPatchFieldAndBadgeBranches(t *testing.T) {
	loadCur := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(9), int64(1)).
			WillReturnRows(programRow(9, "Prog", nil, nil, "", true, 2, 5, 0))
	}

	t.Run("description replace and is_active flip persist together", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectExec(q(sqlProgUpdate)).
			WithArgs("Prog", "Baru", nil, nil, false, int64(9), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(9), int64(1)).
			WillReturnRows(programRow(9, "Prog", "Baru", nil, "", false, 2, 5, 0))

		res := doMP(t, app, "PATCH", "/api/programs/9",
			map[string]string{"description": "Baru", "is_active": "false"},
			nil, hAdmin(t, s))
		program := requireDataEnvelope(t, res).(map[string]any)["program"].(map[string]any)
		require.Equal(t, "Baru", program["description"], "description replace branch")
		require.Equal(t, false, program["is_active"])
		require.Equal(t, "program.changed", takeFrame(t, spy).Entity)
	})

	t.Run("raw badge store failure is the HTTP 500 INTERNAL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		MscBlockSchoolUpload(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		res := doMP(t, app, "PATCH", "/api/programs/9",
			map[string]string{"badge_name": "Medali"},
			map[string]string{"badge_image": string(framesPNG)}, hAdmin(t, s))
		MscRequireInternal(t, res)
	})

	t.Run("badge name without image on an unbadged program is VALIDATION", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		// validBadgePair("Medali","") fails both-or-neither → VALIDATION,
		// no UPDATE.
		res := doMP(t, app, "PATCH", "/api/programs/9",
			map[string]string{"badge_name": "Medali"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})
}

func TestMscProgramsPatchReplaceBadgeWithUnremovableOldFile(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	// The OLD badge path is a non-empty directory: the share-checked
	// os.Remove fails and only logs — the patch still publishes.
	MscUnremovablePath(t, s, "badges/1/old.png")

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(9), int64(1)).
		WillReturnRows(programRow(9, "Prog", nil, "Medali", "badges/1/old.png",
			true, 2, 5, 0))
	mock.ExpectExec(q(sqlProgUpdate)).
		WithArgs("Prog", nil, "Medali", sqlmock.AnyArg(), true, int64(9), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// removeMasterImage: the session_topics share check runs first.
	mock.ExpectQuery(q(sqlBadgeShared)).WithArgs("badges/1/old.png").
		WillReturnRows(sqlmock.NewRows([]string{"shared"}).AddRow(0))
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(9), int64(1)).
		WillReturnRows(programRow(9, "Prog", nil, "Medali",
			"badges/1/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.png", true, 2, 5, 0))

	res := doMP(t, app, "PATCH", "/api/programs/9",
		map[string]string{"badge_name": "Medali"},
		map[string]string{"badge_image": string(framesPNG)}, hAdmin(t, s))
	program := requireDataEnvelope(t, res).(map[string]any)["program"].(map[string]any)
	require.Equal(t, "/uploads/badges/1/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.png",
		program["badge_image_url"], "the new badge replaces the row")
	require.Equal(t, "program.changed", takeFrame(t, spy).Entity)
	require.DirExists(t, filepath.Join(s.upload, "badges", "1", "old.png"),
		"the unremovable old file stays behind for the janitor")
}

func TestMscProgramsDeleteTxFailureBranches(t *testing.T) {
	loadCur := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(9), int64(1)).
			WillReturnRows(programRow(9, "Prog", nil, nil, "", true, 2, 5, 0))
	}

	t.Run("BeginTx failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "DELETE", "/api/programs/9", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("program row DELETE failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q(sqlProgDelLinks)).WithArgs(int64(9), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q(sqlProgDelRow)).WithArgs(int64(9), int64(1)).
			WillReturnError(errors.New("foreign key constraint"))
		mock.ExpectRollback()
		res := doReq(t, app, "DELETE", "/api/programs/9", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("commit failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		loadCur(mock)
		mock.ExpectBegin()
		mock.ExpectExec(q(sqlProgDelLinks)).WithArgs(int64(9), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q(sqlProgDelRow)).WithArgs(int64(9), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(errors.New("driver: bad connection"))
		res := doReq(t, app, "DELETE", "/api/programs/9", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

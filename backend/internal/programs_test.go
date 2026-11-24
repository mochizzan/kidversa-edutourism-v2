package internal

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// programs_test.go — program master (programs.go): envelope guards, paged
// list, multipart create/patch/delete with the full badge file lifecycle, and
// the shared master-write helpers (masterForm, formField, resolveBadge,
// removeMasterImage). Fixtures come from helpers_test.go / schools_test.go;
// sqlmock is ORDERED (a stray statement fails the mock) and every response is
// an HTTP 200 envelope.

// ---------------------------------------------------------------------------
// Local fixtures (unique to this file)
// ---------------------------------------------------------------------------

// Distinctive single-line SQL fragments for q()'s regexp matcher; the list
// fragment takes the optional " AND p.name LIKE ?" / ORDER BY suffix.
const (
	sqlProgCount    = "SELECT COUNT(*) FROM programs p WHERE p.school_id = ?"
	sqlProgListFrom = "FROM programs p WHERE p.school_id = ?"
	sqlProgByID     = "WHERE p.id = ? AND p.school_id = ?"
	sqlProgInsert   = "INSERT INTO programs (school_id, name, description, badge_name, badge_image_path, is_active)"
	sqlProgUpdate   = "UPDATE programs SET name = ?, description = ?, badge_name = ?, badge_image_path = ?, is_active = ?"
	sqlProgDelLinks = "DELETE FROM frame_programs WHERE program_id = ? AND school_id = ?"
	sqlProgDelRow   = "DELETE FROM programs WHERE id = ? AND school_id = ?"
	// Shared with topics_test.go (removeMasterImage share check).
	sqlBadgeShared = "SELECT COUNT(*) FROM session_topics WHERE badge_image_snap = ?"
)

var programRowCols = []string{
	"id", "name", "description", "badge_name", "badge_image_path",
	"is_active", "created_at", "topic_count", "activity_count", "deficient",
}

// programRow builds one programSelect row (10 columns); nil desc/badge args
// become SQL NULL.
func programRow(id int64, name string, desc, badgeName, badgePath any,
	active bool, topics, activities, deficient int64) *sqlmock.Rows {
	return sqlmock.NewRows(programRowCols).
		AddRow(id, name, desc, badgeName, badgePath, active, fixedTS,
			topics, activities, deficient)
}

// seedUploadFile pre-creates an upload file (badge lifecycle tests) and
// returns its absolute path.
func seedUploadFile(t *testing.T, s *Server, rel string) string {
	t.Helper()
	abs := filepath.Join(s.upload, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, []byte("seed-badge"), 0o644))
	return abs
}

// requireNoBadgesDir fails when anything was stored under uploads/badges/<school>
// (the badge write layout since the school moved out of the dir prefix).
func requireNoBadgesDir(t *testing.T, s *Server, school string) {
	t.Helper()
	_, err := os.Stat(filepath.Join(s.upload, "badges", school))
	require.Truef(t, os.IsNotExist(err),
		"no badge image may reach the upload dir, stat err=%v", err)
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ---------------------------------------------------------------------------
// Envelope guards: token / role / school (06 §1 codes)
// ---------------------------------------------------------------------------

func TestProgramsEnvelopeGuardRows(t *testing.T) {
	t.Run("no token is INVALID_TOKEN with no SQL", func(t *testing.T) {
		s, _ := newTestServer(t)
		app := newTestApp(t, s)
		res := doReq(t, app, "GET", "/api/programs", "", nil)
		requireErrEnvelope(t, res, CodeInvalidToken)
	})

	t.Run("facilitator on list is FORBIDDEN_ROLE after the user load", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectFASAuth(mock) // role check precedes school guard and any program query
		res := doReq(t, app, "GET", "/api/programs", "", hFAS(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})

	t.Run("facilitator on create is FORBIDDEN_ROLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectFASAuth(mock)
		res := jsonReq(t, app, "POST", "/api/programs",
			map[string]any{"name": "Math"}, hFAS(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})

	t.Run("SA without X-School-ID is VALIDATION", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0) // kvSchool 0 → requireSchool fails, no SQL after
		res := doReq(t, app, "GET", "/api/programs", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})
}

// ---------------------------------------------------------------------------
// List: paged meta, ?q= LIKE, sort allowlist, single-envelope validation
// ---------------------------------------------------------------------------

func TestProgramsListPagedSearchAndSort(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlProgCount+" AND p.name LIKE ?")).
		WithArgs(int64(1), "%Math%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(q(sqlProgListFrom+
		" AND p.name LIKE ? ORDER BY p.name asc, p.id ASC LIMIT ? OFFSET ?")).
		WithArgs(int64(1), "%Math%", 20, 0).
		WillReturnRows(programRow(7, "Math Fun", "All about math",
			"Gold", "badges/1/gold.png", true, 3, 5, 0).
			AddRow(8, "Math Lab", nil, nil, nil, false, fixedTS, 2, 0, 2))

	res := doReq(t, app, "GET", "/api/programs?q=Math&sort=name", "", hAdmin(t, s))
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2, "paged envelope is data+meta only: %s", res.body)
	require.Equal(t,
		map[string]any{"page": float64(1), "per_page": float64(20), "total": float64(2)},
		m["meta"])
	rows := m["data"].([]any)
	require.Len(t, rows, 2)

	first := rows[0].(map[string]any)
	require.ElementsMatch(t, []string{
		"id", "name", "description", "badge_name", "badge_image_url", "is_active",
		"topic_count", "activity_count", "eligible", "created_at",
	}, mapKeys(first), "Program object must match contracts-shapes.md exactly")
	require.Equal(t, float64(7), first["id"])
	require.Equal(t, "Math Fun", first["name"])
	require.Equal(t, "All about math", first["description"])
	require.Equal(t, "Gold", first["badge_name"])
	require.Equal(t, "/uploads/badges/1/gold.png", first["badge_image_url"])
	require.Equal(t, true, first["is_active"])
	require.Equal(t, float64(3), first["topic_count"])
	require.Equal(t, float64(5), first["activity_count"])
	require.Equal(t, true, first["eligible"], "topics>0 with zero deficient → eligible")
	require.Equal(t, fixedTS.Format(time.RFC3339), first["created_at"])

	second := rows[1].(map[string]any)
	require.Equal(t, false, second["eligible"],
		"topic without activities counts as deficient → not eligible")
	require.Nil(t, second["description"])
	require.Nil(t, second["badge_name"])
	require.Nil(t, second["badge_image_url"], "NULL badge path serializes as JSON null")
	require.Equal(t, false, second["is_active"])
}

func TestProgramsListDefaultSortEmptyPage(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlProgCount)).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(q(sqlProgListFrom+" ORDER BY id asc, p.id ASC LIMIT ? OFFSET ?")).
		WithArgs(int64(1), 20, 0).
		WillReturnRows(sqlmock.NewRows(programRowCols)) // zero rows

	res := doReq(t, app, "GET", "/api/programs", "", hAdmin(t, s))
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2, "paged envelope is data+meta only: %s", res.body)
	require.Empty(t, m["data"])
	require.Equal(t,
		map[string]any{"page": float64(1), "per_page": float64(20), "total": float64(0)},
		m["meta"])
}

func TestProgramsListRejectsBadParamsWithSingleEnvelope(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	for _, target := range []string{
		"/api/programs?per_page=3",      // allowed set {20,50,100,1000}
		"/api/programs?sort=badge_name", // outside allowlist id|name|created_at
		"/api/programs?page=abc",        // numeric
		"/api/programs?dir=sideways",    // asc|desc
	} {
		expectAdminAuth(mock) // rejected before the COUNT query
		res := doReq(t, app, "GET", target, "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation) // exactly one envelope key
	}
}

func TestProgramsListFailuresAreDBUnavailable(t *testing.T) {
	t.Run("count fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlProgCount)).
			WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", "/api/programs", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("rows query fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlProgCount)).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(q(sqlProgListFrom)).
			WillReturnError(errors.New("dial tcp: broken pipe"))
		res := doReq(t, app, "GET", "/api/programs", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlProgCount)).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		// NULL id cannot scan into int64 → scanProgram errors → DB_UNAVAILABLE.
		mock.ExpectQuery(q(sqlProgListFrom)).
			WillReturnRows(sqlmock.NewRows(programRowCols).
				AddRow(nil, "X", nil, nil, nil, true, fixedTS, int64(1), int64(1), int64(0)))
		res := doReq(t, app, "GET", "/api/programs", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// Create: multipart-only, validation matrix, badge rules, insert/reload
// ---------------------------------------------------------------------------

func TestProgramsCreateRejectsNonMultipartBody(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock) // masterForm fails before any SQL
	res := jsonReq(t, app, "POST", "/api/programs",
		map[string]any{"name": "Math"}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeValidation)

	expectAdminAuth(mock)
	res = doReq(t, app, "POST", "/api/programs", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeValidation)
}

func TestProgramsCreateValidationBeforeAnyInsert(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	png := string(realPNG(8, 8))

	for _, tc := range []struct {
		name   string
		fields map[string]string
		files  map[string]string
	}{
		{"missing name", map[string]string{"description": "x"}, nil},
		{"blank name", map[string]string{"name": "   "}, nil},
		{"name over 200 chars", map[string]string{"name": strings.Repeat("n", 201)}, nil},
		{"bad is_active", map[string]string{"name": "Math", "is_active": "yes"}, nil},
		{"badge name without file", map[string]string{"name": "Math", "badge_name": "Gold"}, nil},
		{"badge name over 200 chars",
			map[string]string{"name": "Math", "badge_name": strings.Repeat("b", 201)}, nil},
		{"badge file without name", map[string]string{"name": "Math"},
			map[string]string{"badge_image": png}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectAdminAuth(mock) // no INSERT may follow (stray write fails the mock)
			res := doMP(t, app, "POST", "/api/programs", tc.fields, tc.files, hAdmin(t, s))
			requireErrEnvelope(t, res, CodeValidation)
			requireNoBadgesDir(t, s, "1")
		})
	}
}

// TestProgramsCreateIcoBadgeRejected: Pembaruan 2026-11-10 (spec 09) — the
// server DecodeConfigs the real image bytes; ICO data (declared
// image/x-icon) is rejected with failStore → 200 VALIDATION BEFORE any DB
// write: nothing lands on disk, no publish leaves, no row is written.
func TestProgramsCreateIcoBadgeRejected(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)

	res := doMP(t, app, "POST", "/api/programs",
		map[string]string{"name": "Math", "badge_name": "Gold"},
		map[string]string{"badge_image": string(icoHead)}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeValidation) // non-image bytes rejected by the decode-config whitelist

	entries, err := os.ReadDir(filepath.Join(s.upload, "badges", "1"))
	if err == nil {
		require.Empty(t, entries, "a rejected badge upload must not store a file")
	}
	requireNoFrame(t, spy, "a rejected upload must not publish")
}

func TestProgramsCreateSuccessWithoutBadgePublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectExec(q(sqlProgInsert)).
		WithArgs(int64(1), "Math Fun", nil, nil, nil, true). // desc/badge omitted → NULL
		WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(7), int64(1)).
		WillReturnRows(programRow(7, "Math Fun", nil, nil, nil, true, 0, 0, 0))

	res := doMP(t, app, "POST", "/api/programs",
		map[string]string{"name": " Math Fun "}, nil, hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	p := data["program"].(map[string]any)
	require.Equal(t, float64(7), p["id"])
	require.Equal(t, "Math Fun", p["name"], "name is trimmed before persist")
	require.Nil(t, p["description"])
	require.Nil(t, p["badge_name"])
	require.Nil(t, p["badge_image_url"])
	require.Equal(t, true, p["is_active"], "is_active defaults to true")
	require.Equal(t, false, p["eligible"], "no topics → not eligible")
	requireNoBadgesDir(t, s, "1")

	f := takeFrame(t, spy)
	require.Equal(t, "program.changed", f.Entity)
	require.Equal(t, int64(7), f.EntityID)
	require.Equal(t, "created", f.Action)
	require.Equal(t, int64(1), f.SchoolID)
}

func TestProgramsCreateWithBadgeStoresFileAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)
	png := string(realPNG(8, 8))

	expectAdminAuth(mock)
	mock.ExpectExec(q(sqlProgInsert)).
		WithArgs(int64(1), "Science", "Desc", "Gold", sqlmock.AnyArg(), false).
		WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(9), int64(1)).
		WillReturnRows(programRow(9, "Science", "Desc", "Gold",
			"badges/1/deadbeef.png", false, 2, 2, 0))

	res := doMP(t, app, "POST", "/api/programs",
		map[string]string{"name": "Science", "description": "Desc",
			"badge_name": "Gold", "is_active": "false"},
		map[string]string{"badge_image": png}, hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	p := data["program"].(map[string]any)
	require.Equal(t, "/uploads/badges/1/deadbeef.png", p["badge_image_url"],
		"badge_image_url is the /uploads/<path> form")
	require.Equal(t, false, p["is_active"])

	// The badge REALLY landed on disk under uploads/badges/1/<rand32hex>.png.
	entries, err := os.ReadDir(filepath.Join(s.upload, "badges", "1"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "exactly one stored badge file expected")
	require.Regexp(t, `^[0-9a-f]{32}\.png$`, entries[0].Name())

	f := takeFrame(t, spy)
	require.Equal(t, "program.changed", f.Entity)
	require.Equal(t, int64(9), f.EntityID)
	require.Equal(t, "created", f.Action)
}

func TestProgramsCreateFailuresBeforePublish(t *testing.T) {
	t.Run("insert error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectExec(q(sqlProgInsert)).
			WillReturnError(errors.New("connection refused"))
		res := doMP(t, app, "POST", "/api/programs",
			map[string]string{"name": "Math"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("reload failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectExec(q(sqlProgInsert)).
			WithArgs(int64(1), "Math", nil, nil, nil, true).
			WillReturnResult(sqlmock.NewResult(7, 1))
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(7), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doMP(t, app, "POST", "/api/programs",
			map[string]string{"name": "Math"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("raced delete after insert is NOT_FOUND without a frame", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)
		expectAdminAuth(mock)
		mock.ExpectExec(q(sqlProgInsert)).
			WithArgs(int64(1), "Math", nil, nil, nil, true).
			WillReturnResult(sqlmock.NewResult(7, 1))
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(7), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := doMP(t, app, "POST", "/api/programs",
			map[string]string{"name": "Math"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
		requireNoFrame(t, spy)
	})
}

// ---------------------------------------------------------------------------
// Detail: found / unknown / cross-school / malformed / failure
// ---------------------------------------------------------------------------

func TestProgramsGetDetail(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
			WillReturnRows(programRow(3, "Science", "Desc", "Gold",
				"badges/1/gold.png", true, 4, 6, 1))
		res := doReq(t, app, "GET", "/api/programs/3", "", hAdmin(t, s))
		p := requireDataEnvelope(t, res).(map[string]any)["program"].(map[string]any)
		require.Equal(t, float64(3), p["id"])
		require.Equal(t, "Science", p["name"])
		require.Equal(t, "/uploads/badges/1/gold.png", p["badge_image_url"])
		require.Equal(t, false, p["eligible"], "one deficient topic → not eligible")
	})

	t.Run("unknown id is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(404), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/api/programs/404", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("cross-school id via SA has identical NOT_FOUND shape", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 1)
		// Row of school 2 asked from school 1: scoped lookup just misses.
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(77), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/api/programs/77", "", hSA(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("malformed id behaves as unknown with no SQL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		res := doReq(t, app, "GET", "/api/programs/abc", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("load failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
			WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", "/api/programs/3", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// Patch: validation after the load, badge clear/replace with share check,
// no-op body, failures
// ---------------------------------------------------------------------------

func TestProgramsPatchRejectsNonMultipartBeforeLoad(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectAdminAuth(mock) // masterForm fails before the row load (no SQL queued)
	res := jsonReq(t, app, "PATCH", "/api/programs/3",
		map[string]any{"name": "Renamed"}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeValidation)
}

func TestProgramsPatchValidationAfterLoadWithoutWrite(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	for _, tc := range []struct {
		name   string
		fields map[string]string
	}{
		{"blank name", map[string]string{"name": "   "}},
		{"name over 200 chars", map[string]string{"name": strings.Repeat("n", 201)}},
		{"bad is_active", map[string]string{"name": "Ok", "is_active": "maybe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectAdminAuth(mock)
			mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
				WillReturnRows(programRow(3, "Old", "Old desc", nil, nil, true, 1, 1, 0))
			// No UPDATE expectation: a stray write fails the mock.
			res := doMP(t, app, "PATCH", "/api/programs/3", tc.fields, nil, hAdmin(t, s))
			requireErrEnvelope(t, res, CodeValidation)
		})
	}

	t.Run("load failure is DB_UNAVAILABLE", func(t *testing.T) {
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
			WillReturnError(errors.New("connection refused"))
		res := doMP(t, app, "PATCH", "/api/programs/3",
			map[string]string{"name": "X"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

func TestProgramsPatchRenamePublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
		WillReturnRows(programRow(3, "Old", "Old desc", nil, nil, true, 1, 1, 0))
	mock.ExpectExec(q(sqlProgUpdate)).
		WithArgs("Renamed", "Old desc", nil, nil, true, int64(3), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
		WillReturnRows(programRow(3, "Renamed", "Old desc", nil, nil, true, 1, 1, 0))

	res := doMP(t, app, "PATCH", "/api/programs/3",
		map[string]string{"name": "  Renamed "}, nil, hAdmin(t, s))
	p := requireDataEnvelope(t, res).(map[string]any)["program"].(map[string]any)
	require.Equal(t, "Renamed", p["name"], "name trimmed; untouched fields persisted as-is")
	require.Equal(t, "Old desc", p["description"])

	f := takeFrame(t, spy)
	require.Equal(t, "program.changed", f.Entity)
	require.Equal(t, int64(3), f.EntityID)
	require.Equal(t, "updated", f.Action)
}

func TestProgramsPatchBadgeClearRemovesFileAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)
	oldAbs := seedUploadFile(t, s, "badges/1/old.png")

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
		WillReturnRows(programRow(3, "Old", "Old desc", "Gold", "badges/1/old.png", true, 1, 1, 0))
	// Clear = badge_name:"" with no file → NULL pair; description:"" → NULL.
	mock.ExpectExec(q(sqlProgUpdate)).
		WithArgs("Old", nil, nil, nil, true, int64(3), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q(sqlBadgeShared)).WithArgs("badges/1/old.png").
		WillReturnRows(sqlmock.NewRows([]string{"shared"}).AddRow(0))
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
		WillReturnRows(programRow(3, "Old", nil, nil, nil, true, 1, 1, 0))

	res := doMP(t, app, "PATCH", "/api/programs/3",
		map[string]string{"description": "", "badge_name": ""}, nil, hAdmin(t, s))
	p := requireDataEnvelope(t, res).(map[string]any)["program"].(map[string]any)
	require.Nil(t, p["description"], "empty description clears to JSON null")
	require.Nil(t, p["badge_name"])
	require.Nil(t, p["badge_image_url"])

	require.NoFileExists(t, oldAbs, "unshared old badge file must be removed")
	f := takeFrame(t, spy)
	require.Equal(t, "program.changed", f.Entity)
	require.Equal(t, "updated", f.Action)
}

func TestProgramsPatchBadgeReplaceRemovesUnsharedOldFile(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)
	oldAbs := seedUploadFile(t, s, "badges/1/old.png")
	png := string(realPNG(8, 8))

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
		WillReturnRows(programRow(3, "Old", "Old desc", "Gold", "badges/1/old.png", true, 1, 1, 0))
	mock.ExpectExec(q(sqlProgUpdate)).
		WithArgs("Old", "Old desc", "Silver", sqlmock.AnyArg(), true, int64(3), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q(sqlBadgeShared)).WithArgs("badges/1/old.png").
		WillReturnRows(sqlmock.NewRows([]string{"shared"}).AddRow(0))
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
		WillReturnRows(programRow(3, "Old", "Old desc", "Silver", "badges/1/new.png", true, 1, 1, 0))

	res := doMP(t, app, "PATCH", "/api/programs/3",
		map[string]string{"badge_name": "Silver"},
		map[string]string{"badge_image": png}, hAdmin(t, s))
	p := requireDataEnvelope(t, res).(map[string]any)["program"].(map[string]any)
	require.Equal(t, "Silver", p["badge_name"])
	require.Equal(t, "/uploads/badges/1/new.png", p["badge_image_url"])

	require.NoFileExists(t, oldAbs, "unshared replaced badge must be removed")
	entries, err := os.ReadDir(filepath.Join(s.upload, "badges", "1"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the freshly stored badge remains")
	require.Regexp(t, `^[0-9a-f]{32}\.png$`, entries[0].Name())
	require.Equal(t, "updated", takeFrame(t, spy).Action)
}

func TestProgramsPatchBadgeReplaceKeepsSharedOldFile(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)
	oldAbs := seedUploadFile(t, s, "badges/1/old.png")
	png := string(realPNG(8, 8))

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
		WillReturnRows(programRow(3, "Old", nil, "Gold", "badges/1/old.png", true, 1, 1, 0))
	mock.ExpectExec(q(sqlProgUpdate)).
		WithArgs("Old", nil, "Silver", sqlmock.AnyArg(), true, int64(3), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// A session badge snapshot still points at the old bytes → keep the file.
	mock.ExpectQuery(q(sqlBadgeShared)).WithArgs("badges/1/old.png").
		WillReturnRows(sqlmock.NewRows([]string{"shared"}).AddRow(1))
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
		WillReturnRows(programRow(3, "Old", nil, "Silver", "badges/1/new.png", true, 1, 1, 0))

	res := doMP(t, app, "PATCH", "/api/programs/3",
		map[string]string{"badge_name": "Silver"},
		map[string]string{"badge_image": png}, hAdmin(t, s))
	requireDataEnvelope(t, res)
	require.FileExists(t, oldAbs, "shared old badge file must survive the replace")
	require.Equal(t, "updated", takeFrame(t, spy).Action)
}

func TestProgramsPatchEmptyBodyWritesSameRowAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
		WillReturnRows(programRow(3, "Old", "Old desc", nil, nil, true, 1, 1, 0))
	// patchProgram has no dirty-check (unlike patchSchool): an empty multipart
	// body rewrites the identical values, then reloads and publishes.
	mock.ExpectExec(q(sqlProgUpdate)).
		WithArgs("Old", "Old desc", nil, nil, true, int64(3), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
		WillReturnRows(programRow(3, "Old", "Old desc", nil, nil, true, 1, 1, 0))

	res := doMP(t, app, "PATCH", "/api/programs/3", map[string]string{}, nil, hAdmin(t, s))
	p := requireDataEnvelope(t, res).(map[string]any)["program"].(map[string]any)
	require.Equal(t, "Old", p["name"])
	require.Equal(t, "Old desc", p["description"])
	require.Equal(t, "updated", takeFrame(t, spy).Action)
}

func TestProgramsPatchUnknownIsNotFoundBeforeWrite(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(404), int64(1)).
		WillReturnError(sql.ErrNoRows)
	// No UPDATE queued: a stray write fails the mock.
	res := doMP(t, app, "PATCH", "/api/programs/404",
		map[string]string{"name": "X"}, nil, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

func TestProgramsPatchWriteAndReloadFailures(t *testing.T) {
	t.Run("update error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
			WillReturnRows(programRow(3, "Old", nil, nil, nil, true, 1, 1, 0))
		mock.ExpectExec(q(sqlProgUpdate)).
			WithArgs("New", nil, nil, nil, true, int64(3), int64(1)).
			WillReturnError(errors.New("connection refused"))
		res := doMP(t, app, "PATCH", "/api/programs/3",
			map[string]string{"name": "New"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("raced delete after update is NOT_FOUND without a frame", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
			WillReturnRows(programRow(3, "Old", nil, nil, nil, true, 1, 1, 0))
		mock.ExpectExec(q(sqlProgUpdate)).
			WithArgs("New", nil, nil, nil, true, int64(3), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := doMP(t, app, "PATCH", "/api/programs/3",
			map[string]string{"name": "New"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
		requireNoFrame(t, spy)
	})

	t.Run("reload failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
			WillReturnRows(programRow(3, "Old", nil, nil, nil, true, 1, 1, 0))
		mock.ExpectExec(q(sqlProgUpdate)).
			WithArgs("New", nil, nil, nil, true, int64(3), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(3), int64(1)).
			WillReturnError(errors.New("connection refused"))
		res := doMP(t, app, "PATCH", "/api/programs/3",
			map[string]string{"name": "New"}, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// Delete: one Tx + share-checked file removal + publish
// ---------------------------------------------------------------------------

func TestProgramsDeleteOneTxRemovesBadgeAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)
	oldAbs := seedUploadFile(t, s, "badges/1/old.png")

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(5), int64(1)).
		WillReturnRows(programRow(5, "Doomed", nil, "Gold", "badges/1/old.png", true, 1, 1, 0))
	mock.ExpectBegin()
	mock.ExpectExec(q(sqlProgDelLinks)).WithArgs(int64(5), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(q(sqlProgDelRow)).WithArgs(int64(5), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(q(sqlBadgeShared)).WithArgs("badges/1/old.png").
		WillReturnRows(sqlmock.NewRows([]string{"shared"}).AddRow(0))

	res := doReq(t, app, "DELETE", "/api/programs/5", "", hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])
	require.NoFileExists(t, oldAbs, "unshared badge file removed after commit")

	f := takeFrame(t, spy)
	require.Equal(t, "program.changed", f.Entity)
	require.Equal(t, int64(5), f.EntityID)
	require.Equal(t, "deleted", f.Action)
	require.Equal(t, int64(1), f.SchoolID)

	// The Tx dropped frame_programs too: one frame.changed (no frame id —
	// the link sets changed) makes open capture-frame lists refetch.
	g := takeFrame(t, spy)
	require.Equal(t, "frame.changed", g.Entity)
	require.Equal(t, int64(0), g.EntityID)
	require.Equal(t, "updated", g.Action)
	require.Equal(t, int64(1), g.SchoolID)
	requireNoFrame(t, spy, "deleteProgram publishes exactly two frames")
}

func TestProgramsDeleteWithoutBadgeSkipsShareCheck(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(6), int64(1)).
		WillReturnRows(programRow(6, "Plain", nil, nil, nil, true, 0, 0, 0))
	mock.ExpectBegin()
	mock.ExpectExec(q(sqlProgDelLinks)).WithArgs(int64(6), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q(sqlProgDelRow)).WithArgs(int64(6), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// No share-check query: removeMasterImage("") returns immediately.

	res := doReq(t, app, "DELETE", "/api/programs/6", "", hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])
	require.Equal(t, "deleted", takeFrame(t, spy).Action)
}

func TestProgramsDeleteFailures(t *testing.T) {
	t.Run("unknown id is NOT_FOUND with no Tx", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(404), int64(1)).
			WillReturnError(sql.ErrNoRows)
		// No ExpectBegin: a stray Begin fails the mock → the envelope would
		// stop being NOT_FOUND.
		res := doReq(t, app, "DELETE", "/api/programs/404", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("load failure is DB_UNAVAILABLE before the Tx", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(5), int64(1)).
			WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "DELETE", "/api/programs/5", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("tx delete failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(sqlProgByID)).WithArgs(int64(5), int64(1)).
			WillReturnRows(programRow(5, "Doomed", nil, nil, nil, true, 0, 0, 0))
		mock.ExpectBegin()
		mock.ExpectExec(q(sqlProgDelLinks)).WithArgs(int64(5), int64(1)).
			WillReturnError(errors.New("deadlock"))
		res := doReq(t, app, "DELETE", "/api/programs/5", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

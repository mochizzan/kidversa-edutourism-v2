package internal

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// children_test.go — REST behaviour of internal/children.go (02 §2b, F-07,
// 06 §1/§5): guard matrix, list filters + FAS scope, detail counts, create/
// patch validation, activate/deactivate and the F-07 hard-delete cascade
// (one Tx + disk removal + revocation frame order). Shared fixtures live in
// helpers_test.go / schools_test.go and are reused, never redefined.
// school_test.go (sibling) owns the plain cross-school NOT_FOUND shapes and
// the unfiltered FAS scoped-list case — those exact shapes are not repeated
// here; the FAS list below adds the is_active filter on top of the scope.

// childrenTestCols mirrors the 12 childSelect columns in wire order.
var childrenTestCols = []string{
	"id", "name", "age", "age_confirmed", "school", "mother_name",
	"mother_wa", "father_name", "father_wa", "guardian_name", "is_active",
	"created_at",
}

// childrenByIDFrag is childItemByID's distinctive WHERE clause (quote with q()).
const childrenByIDFrag = "FROM children WHERE id = ? AND school_id = ?"

// childrenTestRow builds one childSelect row; father/guardian accept nil.
func childrenTestRow(id int64, name string, age int, confirmed bool, school,
	mother, wa string, fatherName, fatherWa, guardian any, active bool) *sqlmock.Rows {
	return sqlmock.NewRows(childrenTestCols).
		AddRow(id, name, age, confirmed, school, mother, wa,
			fatherName, fatherWa, guardian, active, fixedTS)
}

// childrenPatchCur is the row every patch case loads before validating:
// id 3, school 1, all optional names present so UPDATE args prove the
// untouched values are carried over.
func childrenPatchCur() *sqlmock.Rows {
	return childrenTestRow(3, "Ayu", 8, false, "SD 1", "Ibu A", "+62811111111",
		"Ayah A", "+62812222222", "Wali A", true)
}

// ---------------------------------------------------------------------------
// Envelope + guard matrix (auth → role → school, all before any domain SQL)
// ---------------------------------------------------------------------------

func TestChildrenGuardMatrix(t *testing.T) {
	t.Run("no token is INVALID_TOKEN with no SQL", func(t *testing.T) {
		s, _ := newTestServer(t)
		app := newTestApp(t, s)
		res := doReq(t, app, "GET", "/api/children", "", nil)
		requireErrEnvelope(t, res, CodeInvalidToken)
	})

	// Mutations are s.authed("ADMIN"): FAS dies at the role check right after
	// the user load — before the school guard and before any handler SQL.
	for _, tc := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{"FAS create", "POST", "/api/children", `{}`},
		{"FAS patch", "PATCH", "/api/children/3", `{}`},
		{"FAS delete", "DELETE", "/api/children/3", ""},
		{"FAS activate", "POST", "/api/children/3/activate", ""},
		{"FAS deactivate", "POST", "/api/children/3/deactivate", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			expectFASAuth(mock)
			res := doReq(t, app, tc.method, tc.target, tc.body, hFAS(t, s))
			requireErrEnvelope(t, res, CodeForbiddenRole)
		})
	}

	// Malformed ids behave as unknown (no oracle, 06 §1 E30) and short-circuit
	// in idParam before any query on every id-bearing route.
	for _, tc := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{"detail malformed id", "GET", "/api/children/abc", ""},
		{"patch malformed id", "PATCH", "/api/children/abc", `{}`},
		{"delete malformed id", "DELETE", "/api/children/abc", ""},
		{"activate malformed id", "POST", "/api/children/abc/activate", ""},
		{"deactivate malformed id", "POST", "/api/children/abc/deactivate", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			expectAdminAuth(mock) // auth only: no row lookup may follow
			res := doReq(t, app, tc.method, tc.target, tc.body, hAdmin(t, s))
			requireErrEnvelope(t, res, CodeNotFound)
		})
	}

	t.Run("SA list without X-School-ID is VALIDATION", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		res := doReq(t, app, "GET", "/api/children", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("bad is_active rejects before fasChildScope", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectFASAuth(mock)
		// No scope-query expectation: ParseBool fails first, so a scope query
		// would hit the mock unfulfilled and change the envelope.
		res := doReq(t, app, "GET", "/api/children?is_active=maybe", "", hFAS(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})
}

// TestChildrenSchoollessSAIsValidationOnEveryRoute pins requireSchool on each
// handler that carries its own guard: SA authed without X-School-ID stores
// school 0, and every route rejects with VALIDATION before any domain SQL.
func TestChildrenSchoollessSAIsValidationOnEveryRoute(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{"detail", "GET", "/api/children/4", ""},
		{"create", "POST", "/api/children",
			`{"name":"Ayu","school":"SD 1","mother_name":"Ibu A","age":8}`},
		{"patch", "PATCH", "/api/children/4", `{"name":"Baru"}`},
		{"delete", "DELETE", "/api/children/4", ""},
		{"activate", "POST", "/api/children/4/activate", ""},
		{"deactivate", "POST", "/api/children/4/deactivate", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			expectSAAuth(mock, 0) // user load only; school 0 has no check
			res := doReq(t, app, tc.method, tc.target, tc.body, hSABare(t, s))
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

// ---------------------------------------------------------------------------
// listChildren: filters, paging meta, sort allowlist, FAS scope
// ---------------------------------------------------------------------------

func TestChildrenListActiveFilterParsesAndQueries(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q("SELECT COUNT(*) FROM children WHERE school_id = ? AND is_active = ?")).
		WithArgs(int64(1), true).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(q("FROM children WHERE school_id = ? AND is_active = ? ORDER BY id asc, id ASC LIMIT ? OFFSET ?")).
		WithArgs(int64(1), true, 20, 0).
		WillReturnRows(childrenTestRow(1, "Ayu", 8, false, "SD 1", "Ibu A", "+62811111111", nil, nil, nil, true).
			AddRow(2, "Bima", 10, false, "SD 2", "Ibu B", "+62822222222", "Ayah B", nil, "Wali B", true, fixedTS))

	res := doReq(t, app, "GET", "/api/children?is_active=true", "", hAdmin(t, s))
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2, "paged envelope is data+meta only: %s", res.body)
	require.Equal(t,
		map[string]any{"page": float64(1), "per_page": float64(20), "total": float64(2)},
		m["meta"])

	items := m["data"].([]any)
	require.Len(t, items, 2)
	first := items[0].(map[string]any)
	require.Len(t, first, 12, "child item carries exactly the 12 contract fields")
	require.Equal(t, map[string]any{
		"id": float64(1), "name": "Ayu", "age": float64(8),
		"age_confirmed": false, "school": "SD 1", "mother_name": "Ibu A",
		"mother_wa": "+62811111111", "father_name": nil, "father_wa": nil,
		"guardian_name": nil, "is_active": true,
		"created_at": "2026-01-02T03:04:05Z",
	}, first)
	second := items[1].(map[string]any)
	require.Equal(t, "Ayah B", second["father_name"], "nullable names surface as strings")
	require.Equal(t, "Wali B", second["guardian_name"])
	require.Nil(t, second["father_wa"])
	require.Equal(t, true, second["is_active"])
}

func TestChildrenListSearchAddsLikeWithSort(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q("SELECT COUNT(*) FROM children WHERE school_id = ? AND name LIKE ?")).
		WithArgs(int64(1), "%Ayu%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("FROM children WHERE school_id = ? AND name LIKE ? ORDER BY name desc, id ASC LIMIT ? OFFSET ?")).
		WithArgs(int64(1), "%Ayu%", 20, 0).
		WillReturnRows(childrenTestRow(1, "Ayu", 8, false, "SD 1", "Ibu A", "+62811111111", nil, nil, nil, true))

	res := doReq(t, app, "GET", "/api/children?q=Ayu&sort=name&dir=desc", "", hAdmin(t, s))
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2)
	require.Len(t, m["data"].([]any), 1)
	require.Equal(t, map[string]any{"page": float64(1), "per_page": float64(20), "total": float64(1)},
		m["meta"])
}

func TestChildrenListRejectsSortOutsideAllowlist(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock) // rejected before COUNT/rows
	res := doReq(t, app, "GET", "/api/children?sort=age", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeValidation) // exactly one envelope key
}

func TestChildrenListFASScopeCombinesWithIsActive(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectFASAuth(mock)
	mock.ExpectQuery(q("SELECT id FROM children WHERE school_id = ?")).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(3))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM children WHERE school_id = ? AND id IN (?) AND is_active = ?")).
		WithArgs(int64(1), int64(3), true).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("FROM children WHERE school_id = ? AND id IN (?) AND is_active = ? ORDER BY id asc, id ASC LIMIT ? OFFSET ?")).
		WithArgs(int64(1), int64(3), true, 20, 0).
		WillReturnRows(childrenTestRow(3, "Ayu", 8, false, "SD 1", "Ibu A", "+62811111111", nil, nil, nil, true))

	res := doReq(t, app, "GET", "/api/children?is_active=true", "", hFAS(t, s))
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2)
	items := m["data"].([]any)
	require.Len(t, items, 1)
	require.Equal(t, float64(3), items[0].(map[string]any)["id"])
	require.Equal(t, float64(1), m["meta"].(map[string]any)["total"])
}

// ---------------------------------------------------------------------------
// getChild: {child, counts} detail
// ---------------------------------------------------------------------------

func TestChildrenGetDetailReturnsChildAndCounts(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(childrenByIDFrag)).
		WithArgs(int64(4), int64(1)).
		WillReturnRows(childrenTestRow(4, "Ayu", 9, false, "SD 1", "Ibu A",
			"+62811111111", nil, nil, nil, true))
	mock.ExpectQuery(q("(SELECT COUNT(*) FROM public_links WHERE child_id = ? AND revoked_at IS NULL)")).
		WithArgs(int64(4), int64(4), int64(4), int64(4), int64(4), int64(4), int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{
			"members", "assessments", "photos", "narratives", "missions", "consents", "links",
		}).AddRow(2, 3, 1, 4, 5, 6, 7))

	res := doReq(t, app, "GET", "/api/children/4", "", hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	child := data["child"].(map[string]any)
	require.Equal(t, float64(4), child["id"])
	require.Equal(t, "Ayu", child["name"])
	require.Equal(t, float64(9), child["age"])
	require.Equal(t, "+62811111111", child["mother_wa"])
	require.Equal(t, true, child["is_active"])
	// links = consent_links + active public_links (6 + 7), and counts carries
	// exactly the six modal keys — no raw consents/public split.
	require.Equal(t, map[string]any{
		"members": float64(2), "assessments": float64(3), "photos": float64(1),
		"narratives": float64(4), "missions": float64(5), "links": float64(13),
	}, data["counts"])
}

func TestChildrenGetUnknownIDIsNotFoundBeforeCounts(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(childrenByIDFrag)).
		WithArgs(int64(404), int64(1)).
		WillReturnError(sql.ErrNoRows)
	// No counts expectation: the handler must return NOT_FOUND first.

	res := doReq(t, app, "GET", "/api/children/404", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

func TestChildrenGetFASForeignChildIsNotFoundWithoutRowLookup(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectFASAuth(mock)
	mock.ExpectQuery(q("SELECT id FROM children WHERE school_id = ?")).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(3))
	// No child-row expectation: a row lookup for the foreign id would fail the
	// mock — the scope miss must answer NOT_FOUND on its own.

	res := doReq(t, app, "GET", "/api/children/77", "", hFAS(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

// ---------------------------------------------------------------------------
// createChild: validation matrix before any INSERT, then success + publish
// ---------------------------------------------------------------------------

func TestChildrenCreateValidationBeforeInsert(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	for _, tc := range []struct {
		name string
		body string
	}{
		{"malformed json", `not-json`},
		{"missing name", `{"school":"SD 1","mother_name":"Ibu A","age":8}`},
		{"missing mother_name", `{"name":"Ayu","school":"SD 1","age":8}`},
		{"missing age", `{"name":"Ayu","school":"SD 1","mother_name":"Ibu A"}`},
		{"missing mother_wa (required)", `{"name":"Ayu","school":"SD 1","mother_name":"Ibu A","age":8}`},
		{"blank name", `{"name":"   ","school":"SD 1","mother_name":"Ibu A","age":8}`},
		{"blank mother_name", `{"name":"Ayu","school":"SD 1","mother_name":"","age":8}`},
		{"age zero", `{"name":"Ayu","school":"SD 1","mother_name":"Ibu A","age":0}`},
		{"age sixteen", `{"name":"Ayu","school":"SD 1","mother_name":"Ibu A","age":16}`},
		{"age eleven without modal confirmation", `{"name":"Ayu","school":"SD 1","mother_name":"Ibu A","age":11}`},
		{"age twelve with age_confirmed false", `{"name":"Ayu","school":"SD 1","mother_name":"Ibu A","age":12,"age_confirmed":false}`},
		{"invalid mother WA", `{"name":"Ayu","school":"SD 1","mother_name":"Ibu A","age":8,"mother_wa":"12345"}`},
		{"invalid father WA", `{"name":"Ayu","school":"SD 1","mother_name":"Ibu A","age":8,"mother_wa":"08123456789","father_wa":"xyz"}`},
		{"father_name over 160", `{"name":"Ayu","school":"SD 1","mother_name":"Ibu A","age":8,"mother_wa":"08123456789","father_name":"` + strings.Repeat("f", 161) + `"}`},
		{"guardian_name over 160", `{"name":"Ayu","school":"SD 1","mother_name":"Ibu A","age":8,"mother_wa":"08123456789","guardian_name":"` + strings.Repeat("g", 161) + `"}`},
		{"mother_name over 160", `{"name":"Ayu","school":"SD 1","mother_name":"` + strings.Repeat("m", 161) + `","age":8}`},
		{"name over 200", `{"name":"` + strings.Repeat("n", 201) + `","school":"SD 1","mother_name":"Ibu A","age":8}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectAdminAuth(mock) // auth + school only: a stray INSERT fails the mock
			res := doReq(t, app, "POST", "/api/children", tc.body, hAdmin(t, s))
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

// TestChildrenCreateSchoolFallsBackToLatestActiveSchool pins decision 11:
// an omitted/blank `school` is filled from the latest active school's
// school_name (not the request-scoped session school).
func TestChildrenCreateSchoolFallsBackToLatestActiveSchool(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"omitted", map[string]any{"name": "Ayu", "mother_name": "Ibu A", "mother_wa": "08123456789", "age": 8}},
		{"blank", map[string]any{"name": "Ayu", "school": "  ", "mother_name": "Ibu A", "mother_wa": "08123456789", "age": 8}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)

			expectAdminAuth(mock)
			mock.ExpectQuery(q("SELECT id, school_name, country, city, is_active, created_at FROM schools WHERE is_active = TRUE ORDER BY id DESC LIMIT 1")).
				WillReturnRows(sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
					AddRow(4, "Kidversa", "Indonesia", nil, true, fixedTS))
			mock.ExpectExec(q("INSERT INTO children (school_id, name, age, age_confirmed, school,")).
				WithArgs(int64(1), "Ayu", 8, false, "Kidversa", "Ibu A", "+628123456789", nil, nil, nil).
				WillReturnResult(sqlmock.NewResult(7, 1))
			mock.ExpectQuery(q(childrenByIDFrag)).WithArgs(int64(7), int64(1)).
				WillReturnRows(childrenTestRow(7, "Ayu", 8, false, "Kidversa",
					"Ibu A", "+628123456789", nil, nil, nil, true))

			res := jsonReq(t, app, "POST", "/api/children", tc.body, hAdmin(t, s))
			child := requireDataEnvelope(t, res).(map[string]any)["child"].(map[string]any)
			require.Equal(t, "Kidversa", child["school"],
				"server fallback fills the child's school from the latest active school")
		})
	}
}

// TestChildrenCreateWithoutActiveSchoolIsValidation pins decision 12: with
// no active school, a child created without `school` fails VALIDATION.
func TestChildrenCreateWithoutActiveSchoolIsValidation(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q("SELECT id, school_name, country, city, is_active, created_at FROM schools WHERE is_active = TRUE ORDER BY id DESC LIMIT 1")).
		WillReturnError(sql.ErrNoRows)

	res := jsonReq(t, app, "POST", "/api/children",
		map[string]any{"name": "Ayu", "mother_name": "Ibu A", "mother_wa": "08123456789", "age": 8}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeValidation)
}

func TestChildrenCreateSuccessInsertsReloadsAndPublishes(t *testing.T) {
	t.Run("with optional father and guardian", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		mock.ExpectExec(q("INSERT INTO children (school_id, name, age, age_confirmed, school,")).
			WithArgs(int64(1), "Citra Lestari", 12, true, "SD Nusantara",
				"Ibu Rina", "+628123456789", "Ayah Danu", "+628129998887", "Kakek Hadi").
			WillReturnResult(sqlmock.NewResult(7, 1))
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(7), int64(1)).
			WillReturnRows(childrenTestRow(7, "Citra Lestari", 12, true, "SD Nusantara",
				"Ibu Rina", "+628123456789", "Ayah Danu", "+628129998887", "Kakek Hadi", true))

		res := jsonReq(t, app, "POST", "/api/children", map[string]any{
			"name": "  Citra Lestari  ", "school": " SD Nusantara ",
			"mother_name": "Ibu Rina", "age": 12, "age_confirmed": true,
			"mother_wa": "08123456789", "father_name": "Ayah Danu",
			"father_wa": "08129998887", "guardian_name": "Kakek Hadi",
		}, hAdmin(t, s))
		data := requireDataEnvelope(t, res).(map[string]any)
		child := data["child"].(map[string]any)
		require.Equal(t, float64(7), child["id"])
		require.Equal(t, "Citra Lestari", child["name"], "name is trimmed before persist")
		require.Equal(t, "SD Nusantara", child["school"], "school is trimmed before persist")
		require.Equal(t, float64(12), child["age"])
		require.Equal(t, true, child["age_confirmed"])
		require.Equal(t, "Ibu Rina", child["mother_name"])
		require.Equal(t, "+628123456789", child["mother_wa"], "08-prefixed WA normalises to +62")
		require.Equal(t, "Ayah Danu", child["father_name"])
		require.Equal(t, "+628129998887", child["father_wa"])
		require.Equal(t, "Kakek Hadi", child["guardian_name"])
		require.Equal(t, true, child["is_active"], "INSERT carries the TRUE is_active literal")

		f := takeFrame(t, spy)
		require.Equal(t, "child.changed", f.Entity)
		require.Equal(t, int64(7), f.EntityID)
		require.Equal(t, "created", f.Action)
		require.Equal(t, int64(1), f.SchoolID)
		requireNoFrame(t, spy)
	})

	t.Run("minimal body leaves the optionals NULL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		mock.ExpectExec(q("INSERT INTO children (school_id, name, age, age_confirmed, school,")).
			WithArgs(int64(1), "Bima", 8, false, "SD 2",
				"Ibu B", "+628123456789", nil, nil, nil).
			WillReturnResult(sqlmock.NewResult(8, 1))
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(8), int64(1)).
			WillReturnRows(childrenTestRow(8, "Bima", 8, false, "SD 2",
				"Ibu B", "+628123456789", nil, nil, nil, true))

		// mother_wa is required too (F-07): only father/guardian are optional.
		res := jsonReq(t, app, "POST", "/api/children", map[string]any{
			"name": "Bima", "school": "SD 2", "mother_name": "Ibu B", "age": 8,
			"mother_wa": "08123456789",
		}, hAdmin(t, s))
		child := requireDataEnvelope(t, res).(map[string]any)["child"].(map[string]any)
		require.Equal(t, float64(8), child["id"])
		require.Nil(t, child["father_name"])
		require.Nil(t, child["father_wa"])
		require.Nil(t, child["guardian_name"])
		require.Equal(t, "+628123456789", child["mother_wa"])
		require.Equal(t, true, child["is_active"])

		f := takeFrame(t, spy)
		require.Equal(t, "child.changed", f.Entity)
		require.Equal(t, int64(8), f.EntityID)
		require.Equal(t, "created", f.Action)
		requireNoFrame(t, spy)
	})
}

func TestChildrenCreateFailuresAreDBUnavailable(t *testing.T) {
	t.Run("INSERT fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		mock.ExpectExec(q("INSERT INTO children (school_id, name, age, age_confirmed, school,")).
			WithArgs(int64(1), "Ayu", 8, false, "SD 1", "Ibu A", "+628123456789", nil, nil, nil).
			WillReturnError(errors.New("dial tcp: connection refused"))

		res := jsonReq(t, app, "POST", "/api/children", map[string]any{
			"name": "Ayu", "school": "SD 1", "mother_name": "Ibu A", "age": 8,
			"mother_wa": "08123456789",
		}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
		requireNoFrame(t, spy)
	})

	t.Run("reload after INSERT fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		mock.ExpectExec(q("INSERT INTO children (school_id, name, age, age_confirmed, school,")).
			WithArgs(int64(1), "Ayu", 8, false, "SD 1", "Ibu A", "+628123456789", nil, nil, nil).
			WillReturnResult(sqlmock.NewResult(7, 1))
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(7), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))

		res := jsonReq(t, app, "POST", "/api/children", map[string]any{
			"name": "Ayu", "school": "SD 1", "mother_name": "Ibu A", "age": 8,
			"mother_wa": "08123456789",
		}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
		requireNoFrame(t, spy)
	})
}

// ---------------------------------------------------------------------------
// patchChild: load cur first, validate without writing, then UPDATE + publish
// ---------------------------------------------------------------------------

func TestChildrenPatchValidationNeverWrites(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"blank name", map[string]any{"name": "  "}},
		{"blank school", map[string]any{"school": ""}},
		{"mother_name over 160", map[string]any{"mother_name": strings.Repeat("m", 161)}},
		{"age 11 without modal confirmation", map[string]any{"age": 11}},
		{"age 12 with age_confirmed false", map[string]any{"age": 12, "age_confirmed": false}},
		{"unnormalisable mother_wa", map[string]any{"mother_wa": "abc"}},
		{"mother_wa never clears to empty", map[string]any{"mother_wa": ""}},
		{"father_name over 160", map[string]any{"father_name": strings.Repeat("f", 161)}},
		{"invalid father_wa", map[string]any{"father_wa": "xyz"}},
		{"guardian_name over 160", map[string]any{"guardian_name": strings.Repeat("g", 161)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectAdminAuth(mock)
			mock.ExpectQuery(q(childrenByIDFrag)).
				WithArgs(int64(3), int64(1)).
				WillReturnRows(childrenPatchCur())
			// No UPDATE expectation: a stray write fails the mock AND the
			// single-envelope assertion.
			res := jsonReq(t, app, "PATCH", "/api/children/3", tc.body, hAdmin(t, s))
			requireErrEnvelope(t, res, CodeValidation)
		})
	}

	t.Run("malformed body rejects before the row load", func(t *testing.T) {
		expectAdminAuth(mock) // no cur-query expectation may follow
		res := doReq(t, app, "PATCH", "/api/children/3", `not-json`, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("unknown child is NOT_FOUND before any write", func(t *testing.T) {
		expectAdminAuth(mock)
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(404), int64(1)).
			WillReturnError(sql.ErrNoRows)
		// No UPDATE expectation.
		res := jsonReq(t, app, "PATCH", "/api/children/404",
			map[string]any{"name": "Baru"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})
}

func TestChildrenPatchUpdatesReloadsAndPublishes(t *testing.T) {
	t.Run("age into 11-15 carries the modal flag and trims text fields", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(childrenPatchCur())
		mock.ExpectExec(q("UPDATE children SET name = ?, age = ?, age_confirmed = ?, school = ?,")).
			WithArgs("Ayu Putri", 11, true, "SD Nusantara", "Ibu Rina", "+62811111111",
				"Ayah A", "+62812222222", "Wali A", int64(3), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(childrenTestRow(3, "Ayu Putri", 11, true, "SD Nusantara",
				"Ibu Rina", "+62811111111", "Ayah A", "+62812222222", "Wali A", true))

		res := jsonReq(t, app, "PATCH", "/api/children/3", map[string]any{
			"name": "  Ayu Putri ", "school": " SD Nusantara ", "mother_name": " Ibu Rina",
			"age": 11, "age_confirmed": true,
		}, hAdmin(t, s))
		child := requireDataEnvelope(t, res).(map[string]any)["child"].(map[string]any)
		require.Equal(t, "Ayu Putri", child["name"], "text fields are trimmed")
		require.Equal(t, "SD Nusantara", child["school"])
		require.Equal(t, "Ibu Rina", child["mother_name"])
		require.Equal(t, float64(11), child["age"])
		require.Equal(t, true, child["age_confirmed"])

		f := takeFrame(t, spy)
		require.Equal(t, "child.changed", f.Entity)
		require.Equal(t, int64(3), f.EntityID)
		require.Equal(t, "updated", f.Action)
		require.Equal(t, int64(1), f.SchoolID)
		requireNoFrame(t, spy)
	})

	t.Run("age_confirmed without age flips the flag only", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(childrenPatchCur())
		// Age stays at the current 8 — only the flag moves.
		mock.ExpectExec(q("UPDATE children SET name = ?, age = ?, age_confirmed = ?, school = ?,")).
			WithArgs("Ayu", 8, true, "SD 1", "Ibu A", "+62811111111",
				"Ayah A", "+62812222222", "Wali A", int64(3), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(childrenTestRow(3, "Ayu", 8, true, "SD 1", "Ibu A",
				"+62811111111", "Ayah A", "+62812222222", "Wali A", true))

		res := jsonReq(t, app, "PATCH", "/api/children/3",
			map[string]any{"age_confirmed": true}, hAdmin(t, s))
		child := requireDataEnvelope(t, res).(map[string]any)["child"].(map[string]any)
		require.Equal(t, float64(8), child["age"], "absent age keeps the stored value")
		require.Equal(t, true, child["age_confirmed"])
		require.Equal(t, "updated", takeFrame(t, spy).Action)
	})

	t.Run("father_wa empty clears the column to NULL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(childrenPatchCur())
		mock.ExpectExec(q("UPDATE children SET name = ?, age = ?, age_confirmed = ?, school = ?,")).
			WithArgs("Ayu", 8, false, "SD 1", "Ibu A", "+62811111111",
				"Ayah A", nil, "Wali A", int64(3), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(childrenTestRow(3, "Ayu", 8, false, "SD 1", "Ibu A",
				"+62811111111", "Ayah A", nil, "Wali A", true))

		res := jsonReq(t, app, "PATCH", "/api/children/3",
			map[string]any{"father_wa": ""}, hAdmin(t, s))
		child := requireDataEnvelope(t, res).(map[string]any)["child"].(map[string]any)
		require.Nil(t, child["father_wa"], "empty father_wa persists as SQL NULL")
		require.Equal(t, "Ayah A", child["father_name"])
		require.Equal(t, "updated", takeFrame(t, spy).Action)
	})

	t.Run("optional fields normalise and replace", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(childrenPatchCur())
		mock.ExpectExec(q("UPDATE children SET name = ?, age = ?, age_confirmed = ?, school = ?,")).
			WithArgs("Ayu", 8, false, "SD 1", "Ibu A", "+628990001112",
				"Ayah Baru", "+628990001112", "Wali Baru", int64(3), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(childrenTestRow(3, "Ayu", 8, false, "SD 1", "Ibu A",
				"+628990001112", "Ayah Baru", "+628990001112", "Wali Baru", true))

		res := jsonReq(t, app, "PATCH", "/api/children/3", map[string]any{
			"mother_wa": "08990001112", "father_name": "Ayah Baru",
			"father_wa": "08990001112", "guardian_name": "Wali Baru",
		}, hAdmin(t, s))
		child := requireDataEnvelope(t, res).(map[string]any)["child"].(map[string]any)
		require.Equal(t, "+628990001112", child["mother_wa"], "replacement WA normalises")
		require.Equal(t, "Ayah Baru", child["father_name"])
		require.Equal(t, "+628990001112", child["father_wa"])
		require.Equal(t, "Wali Baru", child["guardian_name"])
		require.Equal(t, "updated", takeFrame(t, spy).Action)
	})
}

func TestChildrenPatchFailuresAreDBUnavailable(t *testing.T) {
	t.Run("current row load fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(3), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "PATCH", "/api/children/3",
			map[string]any{"name": "Baru"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("UPDATE fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(childrenPatchCur())
		mock.ExpectExec(q("UPDATE children SET name = ?, age = ?, age_confirmed = ?, school = ?,")).
			WithArgs("Ayu", 8, false, "SD 1", "Ibu A", "+62811111111",
				"Ayah A", "+62812222222", "Wali A", int64(3), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "PATCH", "/api/children/3",
			map[string]any{"name": "Ayu"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
		requireNoFrame(t, spy)
	})

	t.Run("reload after UPDATE fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(childrenPatchCur())
		mock.ExpectExec(q("UPDATE children SET name = ?, age = ?, age_confirmed = ?, school = ?,")).
			WithArgs("Ayu", 8, false, "SD 1", "Ibu A", "+62811111111",
				"Ayah A", "+62812222222", "Wali A", int64(3), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(3), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := jsonReq(t, app, "PATCH", "/api/children/3",
			map[string]any{"name": "Ayu"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
		requireNoFrame(t, spy)
	})
}

// TestChildrenRacedReloadIsNotFound pins the cross-domain reload guard
// (programs/topics/missions/frames/activities share it): a row that vanishes
// between the write and its reload answers NOT_FOUND, never a null-item 200.
func TestChildrenRacedReloadIsNotFound(t *testing.T) {
	t.Run("raced delete after insert", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		mock.ExpectExec(q("INSERT INTO children (school_id, name, age, age_confirmed, school,")).
			WithArgs(int64(1), "Ayu", 8, false, "SD 1", "Ibu A", "+628123456789", nil, nil, nil).
			WillReturnResult(sqlmock.NewResult(7, 1))
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(7), int64(1)).
			WillReturnError(sql.ErrNoRows) // raced concurrent delete

		res := jsonReq(t, app, "POST", "/api/children", map[string]any{
			"name": "Ayu", "school": "SD 1", "mother_name": "Ibu A", "age": 8,
			"mother_wa": "08123456789",
		}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
		requireNoFrame(t, spy)
	})

	t.Run("raced delete after update", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(childrenPatchCur())
		mock.ExpectExec(q("UPDATE children SET name = ?, age = ?, age_confirmed = ?, school = ?,")).
			WithArgs("Ayu", 8, false, "SD 1", "Ibu A", "+62811111111",
				"Ayah A", "+62812222222", "Wali A", int64(3), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(3), int64(1)).
			WillReturnError(sql.ErrNoRows) // raced concurrent delete

		res := jsonReq(t, app, "PATCH", "/api/children/3",
			map[string]any{"name": "Ayu"}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
		requireNoFrame(t, spy)
	})
}

// ---------------------------------------------------------------------------
// activate / deactivate: is_active toggle only
// ---------------------------------------------------------------------------

func TestChildrenActivateDeactivateFlipIsActive(t *testing.T) {
	t.Run("activate flips the flag and publishes", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		mock.ExpectExec(q("UPDATE children SET is_active = ? WHERE id = ? AND school_id = ?")).
			WithArgs(true, int64(5), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(5), int64(1)).
			WillReturnRows(childrenTestRow(5, "Bima", 10, false, "SD 2", "Ibu B",
				"+62822222222", nil, nil, nil, true))

		res := jsonReq(t, app, "POST", "/api/children/5/activate", nil, hAdmin(t, s))
		child := requireDataEnvelope(t, res).(map[string]any)["child"].(map[string]any)
		require.Equal(t, true, child["is_active"])
		f := takeFrame(t, spy)
		require.Equal(t, "child.changed", f.Entity)
		require.Equal(t, int64(5), f.EntityID)
		require.Equal(t, "updated", f.Action)
		requireNoFrame(t, spy)
	})

	t.Run("deactivate flips the flag back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 1)
		defer s.unsubscribe(spy)

		expectAdminAuth(mock)
		mock.ExpectExec(q("UPDATE children SET is_active = ? WHERE id = ? AND school_id = ?")).
			WithArgs(false, int64(5), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(5), int64(1)).
			WillReturnRows(childrenTestRow(5, "Bima", 10, false, "SD 2", "Ibu B",
				"+62822222222", nil, nil, nil, false))

		res := jsonReq(t, app, "POST", "/api/children/5/deactivate", nil, hAdmin(t, s))
		child := requireDataEnvelope(t, res).(map[string]any)["child"].(map[string]any)
		require.Equal(t, false, child["is_active"])
		require.Equal(t, "updated", takeFrame(t, spy).Action)
	})

	t.Run("unknown id is NOT_FOUND after the zero-row UPDATE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectAdminAuth(mock)
		mock.ExpectExec(q("UPDATE children SET is_active = ? WHERE id = ? AND school_id = ?")).
			WithArgs(false, int64(404), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(404), int64(1)).
			WillReturnError(sql.ErrNoRows)

		res := jsonReq(t, app, "POST", "/api/children/404/deactivate", nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("toggle UPDATE failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectAdminAuth(mock)
		mock.ExpectExec(q("UPDATE children SET is_active = ? WHERE id = ? AND school_id = ?")).
			WithArgs(true, int64(5), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))

		res := jsonReq(t, app, "POST", "/api/children/5/activate", nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("reload failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectAdminAuth(mock)
		mock.ExpectExec(q("UPDATE children SET is_active = ? WHERE id = ? AND school_id = ?")).
			WithArgs(true, int64(5), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(5), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))

		res := jsonReq(t, app, "POST", "/api/children/5/activate", nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// deleteChild — the F-07 hard-delete cascade
// ---------------------------------------------------------------------------

func TestChildrenDeleteCascadesRowsDiskAndRevokes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// Pre-create both photo files the Tx will collect; the handler must
	// remove them from disk after commit.
	photoFiles := []string{"1/abc.jpg", "1/def.jpg"}
	for _, rel := range photoFiles {
		p := filepath.Join(s.upload, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("jpeg-bytes"), 0o600))
	}

	saSpy := frameSpy(s, 1)
	defer s.unsubscribe(saSpy)
	// The revoked token's public page, plus a sibling token for the SAME
	// child+session that must never hear about this delete.
	pubTokA := s.subscribe(ScopeSnapshot{
		Public: true, Kind: "REPORT", Token: "tokA",
		TokenSession: 5, TokenChild: 30, SchoolID: 1,
	}, "10.0.0.8")
	defer s.unsubscribe(pubTokA)
	pubTokC := s.subscribe(ScopeSnapshot{
		Public: true, Kind: "REPORT", Token: "tokC",
		TokenSession: 5, TokenChild: 30, SchoolID: 1,
	}, "10.0.0.8")
	defer s.unsubscribe(pubTokC)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(childrenByIDFrag)).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(childrenTestRow(30, "Citra", 9, false, "SD 1", "Ibu C",
			"+628333333333", nil, nil, nil, true))
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT path FROM photos WHERE child_id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"path"}).
			AddRow("1/abc.jpg").AddRow("1/def.jpg"))
	mock.ExpectQuery(q("SELECT id, session_id, token FROM public_links WHERE child_id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id", "token"}).
			AddRow(90, 5, "tokA"))
	mock.ExpectQuery(q("SELECT id, session_id, token FROM consent_links WHERE child_id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id", "token"}).
			AddRow(91, 5, "tokB"))
	// Exactly seven child-scoped tables, in code order, then the child row.
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
	// Deliberately NO wa_outbox / masters / users expectations anywhere: any
	// stray query or exec outside the child scope fails the mock — that IS
	// the F-07 scope assertion.

	res := doReq(t, app, "DELETE", "/api/children/30", "", hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Len(t, data, 1)
	require.Equal(t, true, data["deleted"])

	for _, rel := range photoFiles {
		_, err := os.Stat(filepath.Join(s.upload, filepath.FromSlash(rel)))
		require.True(t, os.IsNotExist(err), "photo file must be removed from disk: %s", rel)
	}

	// SA fan-out order: child row → per-public-link signal → terminal
	// revokes (public token first, then the consent token).
	f := takeFrame(t, saSpy)
	require.Equal(t, "child.changed", f.Entity)
	require.Equal(t, int64(30), f.EntityID)
	require.Equal(t, "deleted", f.Action)
	require.Equal(t, int64(1), f.SchoolID)
	require.Nil(t, f.SessionID)

	f = takeFrame(t, saSpy)
	require.Equal(t, "report-link.changed", f.Entity)
	require.Equal(t, int64(90), f.EntityID)
	require.Equal(t, "deleted", f.Action)
	require.NotNil(t, f.SessionID)
	require.Equal(t, int64(5), *f.SessionID)
	require.Equal(t, int64(30), f.ChildID)

	f = takeFrame(t, saSpy)
	require.Equal(t, "link.revoked", f.Entity)
	require.Equal(t, "deleted", f.Action)
	require.Equal(t, "tokA", f.Token)
	require.NotNil(t, f.SessionID)
	require.Equal(t, int64(5), *f.SessionID)
	require.Equal(t, int64(30), f.ChildID)

	f = takeFrame(t, saSpy)
	require.Equal(t, "link.revoked", f.Entity)
	require.Equal(t, "deleted", f.Action)
	require.Equal(t, "tokB", f.Token)
	require.Equal(t, int64(30), f.ChildID)
	requireNoFrame(t, saSpy)

	// The revoked token's public stream receives exactly its own terminal
	// frame and is marked closed (terminal delivery).
	fa := takeFrame(t, pubTokA)
	require.Equal(t, "link.revoked", fa.Entity)
	require.Equal(t, "deleted", fa.Action)
	require.Equal(t, "tokA", fa.Token)
	require.NotNil(t, fa.SessionID)
	require.Equal(t, int64(5), *fa.SessionID)
	require.Equal(t, int64(30), fa.ChildID)
	require.True(t, pubTokA.takeClosing(), "revoked token's stream must terminate")
	requireNoFrame(t, pubTokA)

	// A sibling token (same child+session, different token) gets nothing:
	// neither child.changed, report-link.changed, nor either link.revoked.
	requireNoFrame(t, pubTokC)
}

func TestChildrenDeleteUnknownIDIsNotFoundWithoutBegin(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(childrenByIDFrag)).
		WithArgs(int64(404), int64(1)).
		WillReturnError(sql.ErrNoRows)
	// No ExpectBegin and no DELETE/tx expectations: beginning a wipe (or any
	// wa_outbox/masters touch) for an unknown id would fail the mock and the
	// envelope could not be NOT_FOUND.

	res := doReq(t, app, "DELETE", "/api/children/404", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

func TestChildrenDeleteBeginFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(childrenByIDFrag)).
		WithArgs(int64(8), int64(1)).
		WillReturnRows(childrenTestRow(8, "Bima", 8, false, "SD 2", "Ibu B",
			"+62811111111", nil, nil, nil, true))
	// No ExpectBegin: the Begin error must surface as DB_UNAVAILABLE with no
	// frame published (publish happens only after a successful commit).

	res := doReq(t, app, "DELETE", "/api/children/8", "", hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
	requireNoFrame(t, spy)
}

// ---------------------------------------------------------------------------
// Read-path failure propagation (06 §2: every DB failure → DB_UNAVAILABLE)
// ---------------------------------------------------------------------------

func TestChildrenReadFailuresAreDBUnavailable(t *testing.T) {
	t.Run("list scope query fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectFASAuth(mock)
		mock.ExpectQuery(q("SELECT id FROM children WHERE school_id = ?")).
			WithArgs(int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/children", "", hFAS(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("list scope row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectFASAuth(mock)
		// A child_id that cannot scan into int64 → helper error → failDB.
		mock.ExpectQuery(q("SELECT id FROM children WHERE school_id = ?")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(fixedTS))
		res := doReq(t, app, "GET", "/api/children", "", hFAS(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("detail scope query fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectFASAuth(mock)
		mock.ExpectQuery(q("SELECT id FROM children WHERE school_id = ?")).
			WithArgs(int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/children/4", "", hFAS(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("list COUNT fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM children WHERE school_id = ?")).
			WithArgs(int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/children", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("list rows query fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM children WHERE school_id = ?")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(q("FROM children WHERE school_id = ? ORDER BY id asc, id ASC LIMIT ? OFFSET ?")).
			WithArgs(int64(1), 20, 0).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/children", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("detail row load fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(4), int64(1)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/children/4", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("detail counts query fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(childrenByIDFrag)).
			WithArgs(int64(4), int64(1)).
			WillReturnRows(childrenTestRow(4, "Ayu", 9, false, "SD 1", "Ibu A",
				"+62811111111", nil, nil, nil, true))
		mock.ExpectQuery(q("(SELECT COUNT(*) FROM public_links WHERE child_id = ? AND revoked_at IS NULL)")).
			WithArgs(int64(4), int64(4), int64(4), int64(4), int64(4), int64(4), int64(4)).
			WillReturnError(errors.New("dial tcp: connection refused"))
		res := doReq(t, app, "GET", "/api/children/4", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

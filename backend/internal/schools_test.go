package internal

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// schools_test.go — SA-only school management (schools.go) plus the shared
// fixtures the whole identity/masters slice reuses: auth-header shortcuts,
// the multipart writer for master writes, and the SSE publish spies.
// (helpers_test.go is shared with a sibling agent and must not change.)

// ---------------------------------------------------------------------------
// Shared fixtures (users/programs/topics/activities/missions/frames/children
// tests)
// ---------------------------------------------------------------------------

// expectAdminAuth queues the authed() user load for the shared ADMIN fixture
// (school 1, school active → the X-School-ID guard needs no extra query).
func expectAdminAuth(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
}

// expectFASAuth queues the authed() user load for the shared FASILITATOR.
func expectFASAuth(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
}

// expectSAAuth queues the authed() load for the shared SA; school>0 also
// queues the active-school check SA performs for X-School-ID.
func expectSAAuth(mock sqlmock.Sqlmock, school int64) {
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	if school != 0 {
		mock.ExpectQuery(sqlSchoolActive()).WithArgs(school).
			WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	}
}

// hAdmin / hFAS / hSA / hSABare build request headers for the fixtures above.
func hAdmin(t *testing.T, s *Server) map[string]string {
	t.Helper()
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	return h
}

func hFAS(t *testing.T, s *Server) map[string]string {
	t.Helper()
	h := bearer(mintAccess(t, s, fasUserRow()))
	h["X-School-ID"] = "1"
	return h
}

func hSA(t *testing.T, s *Server) map[string]string {
	t.Helper()
	h := bearer(mintAccess(t, s, saUserRow()))
	h["X-School-ID"] = "1"
	return h
}

func hSABare(t *testing.T, s *Server) map[string]string {
	t.Helper()
	return bearer(mintAccess(t, s, saUserRow()))
}

// masterMP builds a multipart/form-data body (value fields + file bytes keyed
// by field name) for the master writes that can carry an image.
func masterMP(t *testing.T, fields map[string]string, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		require.NoError(t, w.WriteField(k, v))
	}
	for k, content := range files {
		mpWriteFile(t, w, k, "upload.bin", []byte(content))
	}
	require.NoError(t, w.Close())
	return &buf, w.FormDataContentType()
}

// doMP performs a multipart request through the fiber app.
func doMP(t *testing.T, app *fiber.App, method, target string,
	fields, files, headers map[string]string) httpRes {
	t.Helper()
	body, ct := masterMP(t, fields, files)
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Content-Type", ct)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	require.NoError(t, err)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return httpRes{status: resp.StatusCode, header: resp.Header, body: string(b)}
}

// frameSpy subscribes a school-scoped SA listener. publish() fans out
// synchronously, so a frame published while the handler runs is buffered and
// readable the moment app.Test returns (publish-after-commit spy).
func frameSpy(s *Server, school int64) *subscription {
	return s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: school}, "10.0.0.9")
}

// takeFrame pops one buffered frame; fails when nothing was published.
func takeFrame(t *testing.T, sub *subscription) Frame {
	t.Helper()
	select {
	case f := <-sub.ch:
		return f
	default:
		t.Fatal("expected a published frame after commit")
		return Frame{}
	}
}

// requireNoFrame fails when any frame reached the spy. Optional msgAndArgs
// forward to testify (subagents annotate which step they assert on).
func requireNoFrame(t *testing.T, sub *subscription, msgAndArgs ...any) {
	t.Helper()
	require.Zero(t, len(sub.ch), msgAndArgs...)
}

// sqlSchoolItemRow builds one schoolItemByID row (new 6-column shape).
// The 4-argument form keeps co-owned fixtures compiling; the second value is
// school_name, the third is country, and city is NULL.
func sqlSchoolItemRow(id int64, name, country string, active bool) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
		AddRow(id, name, country, nil, active, fixedTS)
}

const sqlSchoolItemByID = "SELECT id, school_name, country, city, is_active, created_at FROM schools WHERE id = ?"

// ---------------------------------------------------------------------------
// SA-only role guard (02 §2b: school admin is an SA route, E8)
// ---------------------------------------------------------------------------

func TestSchoolsRoutesRejectNonSA(t *testing.T) {
	for _, tc := range []struct {
		name   string
		role   string
		method string
		target string
		body   string
	}{
		{"admin list", "ADMIN", "GET", "/api/schools", ""},
		{"admin create", "ADMIN", "POST", "/api/schools", `{"school_name":"X"}`},
		{"admin patch", "ADMIN", "PATCH", "/api/schools/1", `{"school_name":"Y"}`},
		{"fas delete", "FASILITATOR", "DELETE", "/api/schools/1", ""},
		{"fas activate", "FASILITATOR", "POST", "/api/schools/1/activate", ""},
		{"fas detail", "FASILITATOR", "GET", "/api/schools/1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			var h map[string]string
			if tc.role == "ADMIN" {
				expectAdminAuth(mock)
				h = hAdmin(t, s)
			} else {
				expectFASAuth(mock)
				h = hFAS(t, s)
			}
			res := doReq(t, app, tc.method, tc.target, tc.body, h)
			requireErrEnvelope(t, res, CodeForbiddenRole)
		})
	}
}

// ---------------------------------------------------------------------------
// List: paged meta + search + sort allowlist + single-envelope validation
// ---------------------------------------------------------------------------

func TestSchoolsListPagedWithSearchAndSort(t *testing.T) {
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
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2, "paged envelope is data+meta only: %s", res.body)
	require.Equal(t,
		map[string]any{"page": float64(2), "per_page": float64(50), "total": float64(1)},
		m["meta"])
	item := m["data"].([]any)[0].(map[string]any)
	require.Equal(t, float64(1), item["id"])
	require.Equal(t, "Bandung", item["school_name"])
	require.Equal(t, "Indonesia", item["country"])
	require.Nil(t, item["city"])
	require.Equal(t, true, item["is_active"])
	require.Contains(t, item, "created_at", "School object carries created_at")
}

func TestSchoolsListRejectsBadParamsWithSingleEnvelope(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	for _, target := range []string{
		"/api/schools?per_page=3",   // not in the allowed set
		"/api/schools?page=0",       // page ≥ 1
		"/api/schools?page=abc",     // numeric
		"/api/schools?sort=name",    // removed field is outside the allowlist
		"/api/schools?dir=sideways", // asc|desc only
	} {
		expectSAAuth(mock, 0) // rejected before any school query
		res := doReq(t, app, "GET", target, "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	}
}

// TestSchoolsListIsActiveFilter: ?is_active=true|false appends the
// `AND is_active=?` clause after the (optional) school_name LIKE, with the
// parsed boolean passed as the bound arg — both COUNT and rows carry it.
func TestSchoolsListIsActiveFilter(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		want   bool
		like   string // "" = no ?q=; else the LIKE arg
	}{
		{name: "true", target: "/api/schools?is_active=true", want: true},
		{name: "false", target: "/api/schools?is_active=false", want: false},
		{name: "uppercase TRUE is case-insensitive", target: "/api/schools?is_active=TRUE", want: true},
		{name: "combines with q search", target: "/api/schools?q=Band&is_active=false", want: false, like: "%Band%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)

			where := "WHERE 1=1"
			var countArgs []driver.Value
			if tc.like != "" {
				where += " AND school_name LIKE ?"
				countArgs = append(countArgs, tc.like)
			}
			where += " AND is_active=?"
			countArgs = append(countArgs, tc.want)

			expectSAAuth(mock, 0)
			mock.ExpectQuery(q("SELECT COUNT(*) FROM schools " + where)).
				WithArgs(countArgs...).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			mock.ExpectQuery(q("FROM schools " + where + " ORDER BY id asc, id ASC LIMIT ? OFFSET ?")).
				WithArgs(append(append([]driver.Value{}, countArgs...), 20, 0)...).
				WillReturnRows(sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
					AddRow(1, "Bandung", "Indonesia", nil, tc.want, fixedTS))

			res := doReq(t, app, "GET", tc.target, "", hSABare(t, s))
			require.Equal(t, 200, res.status)
			m := envelopeMap(t, res.body)
			items := m["data"].([]any)
			require.Len(t, items, 1)
			require.Equal(t, tc.want, items[0].(map[string]any)["is_active"])
		})
	}
}

// TestSchoolsListRejectsInvalidIsActive: strict boolean — "maybe"/"1"/"0"
// answer VALIDATION before any school SQL; empty/absent keeps the old
// unrestricted WHERE 1=1.
func TestSchoolsListRejectsInvalidIsActive(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	for _, target := range []string{
		"/api/schools?is_active=maybe",
		"/api/schools?is_active=1", // ParseBool would accept it; strict contract does not
	} {
		expectSAAuth(mock, 0) // rejected before any school query
		res := doReq(t, app, "GET", target, "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	}
}

// TestSchoolsListWithoutIsActiveKeepsOldQuery: absent (or empty) is_active
// never touches the WHERE clause — the paged SELECT stays WHERE 1=1.
func TestSchoolsListWithoutIsActiveKeepsOldQuery(t *testing.T) {
	for _, target := range []string{"/api/schools", "/api/schools?is_active="} {
		t.Run(target, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)

			expectSAAuth(mock, 0)
			mock.ExpectQuery(q("SELECT COUNT(*) FROM schools WHERE 1=1")).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
			mock.ExpectQuery(q("FROM schools WHERE 1=1 ORDER BY id asc, id ASC LIMIT ? OFFSET ?")).
				WithArgs(20, 0).
				WillReturnRows(sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
					AddRow(1, "Bandung", "Indonesia", nil, true, fixedTS).
					AddRow(2, "Mati", "Indonesia", nil, false, fixedTS))

			res := doReq(t, app, "GET", target, "", hSABare(t, s))
			require.Equal(t, 200, res.status)
			m := envelopeMap(t, res.body)
			require.Len(t, m["data"].([]any), 2)
			require.Equal(t,
				map[string]any{"page": float64(1), "per_page": float64(20), "total": float64(2)},
				m["meta"])
		})
	}
}

func TestSchoolsListCountFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectSAAuth(mock, 0)
	mock.ExpectQuery(q("SELECT COUNT(*) FROM schools")).
		WillReturnError(errors.New("connection refused"))

	res := doReq(t, app, "GET", "/api/schools", "", hSABare(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// Create: validation matrix, location defaults, publish after commit
// ---------------------------------------------------------------------------

func TestSchoolsCreateValidationBeforeAnyWrite(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	for _, tc := range []struct {
		name string
		body string
	}{
		{"empty school_name", `{"school_name":"  "}`},
		{"school_name over 200 chars", `{"school_name":"` + strings.Repeat("x", 201) + `"}`},
		{"country over 100 chars", `{"school_name":"Bandung","country":"` + strings.Repeat("x", 101) + `"}`},
		{"city over 100 chars", `{"school_name":"Bandung","city":"` + strings.Repeat("x", 101) + `"}`},
		{"not json", `not-json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectSAAuth(mock, 0) // guard only — no INSERT may follow
			res := doReq(t, app, "POST", "/api/schools", tc.body, hSABare(t, s))
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

// TestSchoolsCreateDuplicateNameIsAllowed: decision 4 drops the global
// unique name index, so a duplicate-entry fault is a plain DB failure, not
// a 1062→VALIDATION translation.
func TestSchoolsCreateDuplicateNameIsAllowed(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectSAAuth(mock, 0)
	mock.ExpectExec(q("INSERT INTO schools (school_name, country, city, is_active) VALUES (?, ?, ?, TRUE)")).
		WithArgs("Bandung", "Indonesia", nil).
		WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(7)).
		WillReturnRows(sqlSchoolItemRow(7, "Bandung", "Indonesia", true))

	res := jsonReq(t, app, "POST", "/api/schools",
		map[string]any{"school_name": "Bandung"}, hSABare(t, s))
	require.Equal(t, 200, res.status)
}

func TestSchoolsCreateSuccessPublishesAfterCommit(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 7) // the new school's id
	defer s.unsubscribe(spy)

	expectSAAuth(mock, 0)
	mock.ExpectExec(q("INSERT INTO schools (school_name, country, city, is_active) VALUES (?, ?, ?, TRUE)")).
		WithArgs("Bandung Utara", "Indonesia", "Bandung"). // trimmed before persist
		WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
			AddRow(7, "Bandung Utara", "Indonesia", "Bandung", true, fixedTS))

	res := jsonReq(t, app, "POST", "/api/schools",
		map[string]any{"school_name": "  Bandung Utara  ", "country": " Indonesia ", "city": " Bandung "}, hSABare(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	school := data["school"].(map[string]any)
	require.Equal(t, float64(7), school["id"])
	require.Equal(t, "Bandung Utara", school["school_name"])
	require.Equal(t, "Indonesia", school["country"])
	require.Equal(t, "Bandung", school["city"])
	require.Equal(t, true, school["is_active"])

	f := takeFrame(t, spy)
	require.Equal(t, "school.changed", f.Entity)
	require.Equal(t, int64(7), f.EntityID)
	require.Equal(t, "created", f.Action)
	require.Equal(t, int64(7), f.SchoolID, "frame routes to the new school")
}

func TestSchoolsCreateReloadFailureIsDBUnavailable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectSAAuth(mock, 0)
	mock.ExpectExec(q("INSERT INTO schools (school_name, country, city, is_active) VALUES (?, ?, ?, TRUE)")).
		WithArgs("Bandung", "Indonesia", nil).
		WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(7)).
		WillReturnError(errors.New("dial tcp: connection refused"))

	res := jsonReq(t, app, "POST", "/api/schools",
		map[string]any{"school_name": "Bandung"}, hSABare(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// Detail: found / unknown / malformed id (no oracle)
// ---------------------------------------------------------------------------

func TestSchoolsGetDetail(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
			WillReturnRows(sqlSchoolItemRow(3, "Semarang", "Indonesia", true))
		res := doReq(t, app, "GET", "/api/schools/3", "", hSABare(t, s))
		school := requireDataEnvelope(t, res).(map[string]any)["school"].(map[string]any)
		require.Equal(t, "Semarang", school["school_name"])
	})

	t.Run("unknown id is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(404)).
			WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/api/schools/404", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("malformed id behaves as unknown", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0) // no row lookup at all
		res := doReq(t, app, "GET", "/api/schools/abc", "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})
}

// ---------------------------------------------------------------------------
// Patch: partial update, validation, publish
// ---------------------------------------------------------------------------

func TestSchoolsPatchFieldsPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 3)
	defer s.unsubscribe(spy)

	expectSAAuth(mock, 0)
	mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
		WillReturnRows(sqlSchoolItemRow(3, "Lama", "Indonesia", true))
	mock.ExpectExec(q("UPDATE schools SET school_name = ?, country = ?, city = ? WHERE id = ?")).
		WithArgs("Baru", "Indonesia", "Bandung", int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
			AddRow(3, "Baru", "Indonesia", "Bandung", true, fixedTS))

	res := jsonReq(t, app, "PATCH", "/api/schools/3",
		map[string]any{"school_name": "  Baru ", "country": " Indonesia ", "city": " Bandung "}, hSABare(t, s))
	school := requireDataEnvelope(t, res).(map[string]any)["school"].(map[string]any)
	require.Equal(t, "Baru", school["school_name"], "name is trimmed before persist")
	require.Equal(t, "Indonesia", school["country"])
	require.Equal(t, "Bandung", school["city"])

	f := takeFrame(t, spy)
	require.Equal(t, "school.changed", f.Entity)
	require.Equal(t, int64(3), f.EntityID)
	require.Equal(t, "updated", f.Action)
}

func TestSchoolsPatchIsOnlyValidationNoExec(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"blank school_name", map[string]any{"school_name": "   "}},
		{"school_name over 200", map[string]any{"school_name": strings.Repeat("n", 201)}},
		{"country over 100", map[string]any{"country": strings.Repeat("c", 101)}},
		{"city over 100", map[string]any{"city": strings.Repeat("c", 101)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			expectSAAuth(mock, 0)
			mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
				WillReturnRows(sqlSchoolItemRow(3, "Lama", "Indonesia", true))
			// No UPDATE expectation: a stray write would fail the mock.
			res := jsonReq(t, app, "PATCH", "/api/schools/3", tc.body, hSABare(t, s))
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

func TestSchoolsPatchUnknownSchoolIsNotFound(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	expectSAAuth(mock, 0)
	mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(404)).
		WillReturnError(sql.ErrNoRows)

	res := jsonReq(t, app, "PATCH", "/api/schools/404",
		map[string]any{"school_name": "Baru"}, hSABare(t, s))
	requireErrEnvelope(t, res, CodeNotFound)
}

func TestSchoolsPatchEmptyBodyReloadsWithoutWrite(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 3)
	defer s.unsubscribe(spy)

	expectSAAuth(mock, 0)
	mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
		WillReturnRows(sqlSchoolItemRow(3, "Lama", "Indonesia", true))
	// No UPDATE: {} touches nothing, yet the response is the fresh row.
	mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
		WillReturnRows(sqlSchoolItemRow(3, "Lama", "Indonesia", true))

	res := jsonReq(t, app, "PATCH", "/api/schools/3", map[string]any{}, hSABare(t, s))
	school := requireDataEnvelope(t, res).(map[string]any)["school"].(map[string]any)
	require.Equal(t, "Lama", school["school_name"])
	require.Equal(t, "updated", takeFrame(t, spy).Action)
}

// ---------------------------------------------------------------------------
// Activate / deactivate
// ---------------------------------------------------------------------------

func TestSchoolsActivateDeactivateTransitions(t *testing.T) {
	t.Run("activate flips the flag and publishes", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		spy := frameSpy(s, 3)
		defer s.unsubscribe(spy)

		expectSAAuth(mock, 0)
		mock.ExpectExec(q("UPDATE schools SET is_active = ? WHERE id = ?")).
			WithArgs(true, int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(3)).
			WillReturnRows(sqlSchoolItemRow(3, "Lama", "Indonesia", true))

		res := jsonReq(t, app, "POST", "/api/schools/3/activate", nil, hSABare(t, s))
		school := requireDataEnvelope(t, res).(map[string]any)["school"].(map[string]any)
		require.Equal(t, true, school["is_active"])
		f := takeFrame(t, spy)
		require.Equal(t, "school.changed", f.Entity)
		require.Equal(t, "updated", f.Action)
	})

	t.Run("deactivate on unknown school is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectSAAuth(mock, 0)
		mock.ExpectExec(q("UPDATE schools SET is_active = ? WHERE id = ?")).
			WithArgs(false, int64(404)).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(sqlSchoolItemByID).WithArgs(int64(404)).
			WillReturnError(sql.ErrNoRows)

		res := jsonReq(t, app, "POST", "/api/schools/404/deactivate", nil, hSABare(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})
}

// ---------------------------------------------------------------------------
// Hard delete: 23 wipe tables + schools row in ONE Tx, disk after commit,
// then school.changed
// ---------------------------------------------------------------------------

// schoolWipeOrder pins the F-02 wipe scope: every school-carrying table
// except wa_outbox/schools is deleted inside the transaction.
var schoolWipeOrder = []string{
	"users", "refresh_tokens", "reset_tokens",
	"programs", "topics", "activities", "missions", "mission_topics",
	"frames", "frame_programs", "children", "sessions", "session_topics",
	"session_activities", "session_groups", "session_members", "assessments",
	"photos", "narratives", "report_missions", "consent_links", "public_links",
	"idempotency_keys",
}

func TestSchoolsDeleteWipesScopeInOneTxThenPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 10)
	defer s.unsubscribe(spy)

	expectSAAuth(mock, 0)
	mock.ExpectBegin()
	for _, table := range schoolWipeOrder {
		mock.ExpectExec(q("DELETE FROM " + table + " WHERE school_id = ?")).
			WithArgs(int64(10)).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(q("DELETE FROM schools WHERE id = ?")).
		WithArgs(int64(10)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := jsonReq(t, app, "DELETE", "/api/schools/10", nil, hSABare(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])

	f := takeFrame(t, spy)
	require.Equal(t, "school.changed", f.Entity)
	require.Equal(t, int64(10), f.EntityID)
	require.Equal(t, "deleted", f.Action)
	require.Equal(t, int64(10), f.SchoolID, "wipe frame routes to the dead school")
}

// TestSchoolsDeleteRemovesAllDiskLayouts pins F-B03: the post-commit disk
// wipe covers EVERY layout school bytes live in — legacy uploads/<id>/ plus
// the badge/frame dirs uploads/badges/<id>/ and uploads/frames/<id>/.
func TestSchoolsDeleteRemovesAllDiskLayouts(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 10)
	defer s.unsubscribe(spy)

	legacy := seedUploadFile(t, s, "10/photo.png")
	badge := seedUploadFile(t, s, "badges/10/gold.png")
	frame := seedUploadFile(t, s, "frames/10/poster.png")

	expectSAAuth(mock, 0)
	mock.ExpectBegin()
	for _, table := range schoolWipeOrder {
		mock.ExpectExec(q("DELETE FROM " + table + " WHERE school_id = ?")).
			WithArgs(int64(10)).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(q("DELETE FROM schools WHERE id = ?")).
		WithArgs(int64(10)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := jsonReq(t, app, "DELETE", "/api/schools/10", nil, hSABare(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])

	require.NoFileExists(t, legacy, "legacy uploads/<id>/ bytes are wiped")
	require.NoFileExists(t, badge, "uploads/badges/<id>/ bytes are wiped")
	require.NoFileExists(t, frame, "uploads/frames/<id>/ bytes are wiped")
	require.Equal(t, "deleted", takeFrame(t, spy).Action,
		"the wipe frame still publishes after the disk sweep")
}

// ---------------------------------------------------------------------------
// schools.go shared helpers (requireSchool / queryIDFilter) through the
// masters routes that call them
// ---------------------------------------------------------------------------

func TestSchoolsRequireSchoolRejectsSASchoollessWrite(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// SA without X-School-ID: authed stores 0 → a school-scoped read has no
	// school to target → VALIDATION before any query.
	expectSAAuth(mock, 0)
	res := doReq(t, app, "GET", "/api/programs", "", hSABare(t, s))
	requireErrEnvelope(t, res, CodeValidation)
}

func TestSchoolsQueryIDFilterRejectsMalformedIDs(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	for _, target := range []string{
		"/api/topics?program_id=abc",
		"/api/topics?program_id=0",
		"/api/topics?program_id=-4",
	} {
		expectAdminAuth(mock) // rejected before the COUNT query
		res := doReq(t, app, "GET", target, "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	}
}

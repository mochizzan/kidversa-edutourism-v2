package internal

import (
	"strconv"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/stretchr/testify/require"
)

// TestEnvelopeHandlerFamiliesAnswer200 walks every handler family with a
// failure input and pins the binding transport rule: HTTP 200 with a single
// {error:{code,message}} envelope (01 §2, 06 §1).
func TestEnvelopeHandlerFamiliesAnswer200(t *testing.T) {
	s, _ := newTestServer(t)
	app := newTestApp(t, s)

	cases := []struct {
		name    string
		method  string
		target  string
		body    string
		headers map[string]string
		code    string
	}{
		{"auth login bad json", "POST", "/api/auth/login", "not-json", nil, CodeValidation},
		{"auth me no token", "GET", "/api/auth/me", "", nil, CodeInvalidToken},
		{"reset-tokens no token", "GET", "/api/reset-tokens", "", nil, CodeInvalidToken},
		{"schools no token", "POST", "/api/schools", "{}", nil, CodeInvalidToken},
		{"users no token", "GET", "/api/users", "", nil, CodeInvalidToken},
		{"programs no token", "GET", "/api/programs", "", nil, CodeInvalidToken},
		{"topics no token", "GET", "/api/topics", "", nil, CodeInvalidToken},
		{"activities no token", "GET", "/api/activities", "", nil, CodeInvalidToken},
		{"missions no token", "GET", "/api/missions", "", nil, CodeInvalidToken},
		{"frames no token", "GET", "/api/frames", "", nil, CodeInvalidToken},
		{"children no token", "GET", "/api/children", "", nil, CodeInvalidToken},
		{"sessions no token", "GET", "/api/sessions", "", nil, CodeInvalidToken},
		{"sessions detail no token", "GET", "/api/sessions/1", "", nil, CodeInvalidToken},
		{"consents no token", "GET", "/api/consents", "", nil, CodeInvalidToken},
		{"reports matrix no token", "GET", "/api/sessions/1/reports", "", nil, CodeInvalidToken},
		{"dashboard no token", "GET", "/api/dashboard/overview", "", nil, CodeInvalidToken},
		{"wa status no token", "GET", "/api/wa/status", "", nil, CodeInvalidToken},
		{"stream no token", "GET", "/api/stream", "", nil, CodeInvalidToken},
		{"public report no token", "GET", "/api/report", "", nil, CodeTokenInvalid},
		{"public gallery no token", "GET", "/api/gallery", "", nil, CodeTokenInvalid},
		{"public consent no token", "GET", "/api/consent", "", nil, CodeTokenInvalid},
		{"public respond bad decision", "POST", "/api/consent/respond", `{"token":"t","decision":"MAYBE","respondent_name":"Ibu"}`, nil, CodeValidation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := doReq(t, app, tc.method, routePath(tc.target), tc.body, tc.headers)
			requireErrEnvelope(t, res, tc.code)
		})
	}
}

// routePath maps the table's spelling onto the real mount points (/public
// routes live under the /public group).
func routePath(target string) string {
	switch target {
	case "/api/report", "/api/gallery", "/api/consent", "/api/consent/respond":
		return "/public" + target[len("/api"):]
	}
	return target
}

// TestEnvelopeSuccessShape covers the success + business-failure + NOT_FOUND
// paths of one handler: HTTP 200 with exactly one envelope.
func TestEnvelopeSuccessShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(q("SELECT id, school_name FROM schools WHERE is_active = TRUE ORDER BY school_name")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_name"}).AddRow(1, "Kidversa"))

	res := doReq(t, app, "GET", "/api/auth/schools", "", nil)
	data := requireDataEnvelope(t, res)
	list, ok := data.([]any)
	require.True(t, ok, "auth schools data must be an array: %s", res.body)
	require.Len(t, list, 1)
	require.Equal(t, map[string]any{"id": float64(1), "school_name": "Kidversa"}, list[0])
}

func TestEnvelopeNotFoundShape(t *testing.T) {
	s, _ := newTestServer(t)
	app := newTestApp(t, s)
	res := doReq(t, app, "GET", "/api/definitely-missing", "", nil)
	requireErrEnvelope(t, res, CodeNotFound)
}

// TestEnvelopeErrorHandlerPanicMapsTo500 pins the system's ONLY HTTP 500:
// a panicking handler passes through fiber recover into ErrorHandler.
func TestEnvelopeErrorHandlerPanicMapsTo500(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Use(recover.New())
	app.Get("/boom", func(fiber.Ctx) error { panic("boom in handler") })

	res := doReq(t, app, "GET", "/boom", "", nil)
	require.Equal(t, 500, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 1)
	errObj := m["error"].(map[string]any)
	require.Equal(t, CodeInternal, errObj["code"])
	require.Equal(t, MsgInternal, errObj["message"])
}

// TestEnvelopeRateLimitedStays200WithRetryAfter pins the limiter contract:
// rejection is HTTP 200 RATE_LIMITED + Retry-After, never 429.
func TestEnvelopeRateLimitedStays200WithRetryAfter(t *testing.T) {
	s, _ := newTestServer(t)
	app := newTestApp(t, s)

	// POST /api/auth/register allows 5/min per IP; empty bodies short-circuit
	// as VALIDATION before any DB work.
	for i := range 5 {
		res := doReq(t, app, "POST", "/api/auth/register", "", nil)
		requireErrEnvelope(t, res, CodeValidation)
		_ = i
	}
	res := doReq(t, app, "POST", "/api/auth/register", "", nil)
	requireErrEnvelope(t, res, CodeRateLimited)
	retryAfter := res.header.Get("Retry-After")
	require.NotEmpty(t, retryAfter, "rate limiting must advertise Retry-After")
	require.Positive(t, func() int { n, _ := strconv.Atoi(retryAfter); return n }(),
		"Retry-After must be a positive number of seconds, got %q", retryAfter)
}

// TestEnvelopePagedListMeta pins {data, meta:{page,per_page,total}} and the
// invalid per_page path: listParams writes the VALIDATION envelope itself, so
// the response must carry exactly one envelope (no double-write corruption).
func TestEnvelopePagedListMeta(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	tok := mintAccess(t, s, adminUserRow())
	h := bearer(tok)
	h["X-School-ID"] = "1"

	// Invalid per_page → VALIDATION before any DB call (single envelope).
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	res := doReq(t, app, "GET", "/api/users?per_page=3", "", h)
	requireErrEnvelope(t, res, CodeValidation)

	// Valid paged list → data + meta.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT COUNT(*) FROM users u")).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(q("AND u.school_id = ? AND u.role = 'FASILITATOR' ORDER BY")).
		WithArgs(int64(1), 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "email", "role", "decision", "is_active",
			"school_id", "wa_number", "created_at", "school_name",
		}).
			AddRow(3, "Fasil", "fas@school.id", "FASILITATOR", "APPROVED", true, int64(1), "+628111111111", fixedTS, "Bandung").
			AddRow(4, "Fasil2", "fas2@school.id", "FASILITATOR", "PENDING", false, int64(1), nil, fixedTS, "Bandung"))

	res = doReq(t, app, "GET", "/api/users", "", h)
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2, "paged envelope must carry exactly data+meta: %s", res.body)
	rows, ok := m["data"].([]any)
	require.True(t, ok)
	require.Len(t, rows, 2)
	meta, ok := m["meta"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, map[string]any{"page": float64(1), "per_page": float64(20), "total": float64(2)}, meta)
}

// TestEnvelopeListParamsRejectsInvalidSortAndPage drives listParams'
// validation matrix through a real paged route.
func TestEnvelopeListParamsRejectsInvalidSortAndPage(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"

	for _, target := range []string{
		"/api/users?sort=password_hash", // outside allowlist
		"/api/users?dir=sideways",
		"/api/users?page=0",
		"/api/users?page=abc",
	} {
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
		res := doReq(t, app, "GET", target, "", h)
		requireErrEnvelope(t, res, CodeValidation)
	}
}

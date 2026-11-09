package internal

import (
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// TestMain runs every unit test from the backend root so NewServer finds the
// prompts/templates assets it loads at construction (test binaries start in
// the package directory backend/internal).
func TestMain(m *testing.M) {
	if err := os.Chdir(".."); err != nil {
		panic("chdir to backend root: " + err.Error())
	}
	os.Exit(m.Run())
}

const (
	testPassword   = "Password123"
	testAccessSec  = "unit-access-secret"
	testRefreshSec = "unit-refresh-secret"
)

// testPasswordHash is precomputed once (MinCost) so login tests pay only the
// comparison cost.
var testPasswordHash = func() string {
	h, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	return string(h)
}()

var otherPasswordHash = func() string {
	h, err := bcrypt.GenerateFromPassword([]byte("OtherPass99"), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	return string(h)
}()

// testConfig is a fully-populated Config: temp upload dir, unreachable
// OpenWA base (unit tests never reach a real server; the WA tests point it at
// a local httptest server), no SMTP (forgot stamps mail_failed).
func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		JWTAccessSecret:  testAccessSec,
		JWTRefreshSecret: testRefreshSec,
		AccessTTL:        15 * time.Minute,
		RefreshTTL:       7 * 24 * time.Hour,
		ResetTTL:         15 * time.Minute,
		OpenwaBaseURL:    "http://127.0.0.1:1",
		OpenwaSession:    "kidversa-test",
		PublicWebURL:     "http://localhost:8002",
		UploadDir:        t.TempDir(),
		CorsOrigins:      []string{"http://localhost:8002"},
	}
}

// newTestServer builds a Server over a fresh sqlmock; every declared
// expectation is verified at test end.
func newTestServer(t *testing.T) (*Server, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	s, err := NewServer(db, testConfig(t))
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sqlmock expectations: %v", err)
		}
		db.Close()
	})
	return s, mock
}

// newTestApp assembles the FULL production route table in the main.go order
// (logging omitted to keep test output readable). Building it in every test
// doubles as the fiber duplicate-route runtime check: fiber panics on
// conflicting paths, so a registration conflict fails here immediately.
func newTestApp(t *testing.T, s *Server) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{
		BodyLimit:    52 * 1024 * 1024, // matches main.go (52m, Pembaruan 2026-10-11)
		ErrorHandler: ErrorHandler,
	})
	app.Use(recover.New())
	api := app.Group("/api")
	pub := app.Group("/public")
	s.RegisterAuth(api)
	s.RegisterStream(api, pub)
	s.RegisterWa(api)
	s.RegisterTenants(api)
	s.RegisterUsers(api)
	s.RegisterPrograms(api)
	s.RegisterTopics(api)
	s.RegisterActivities(api)
	s.RegisterMissions(api)
	s.RegisterFrames(api)
	s.RegisterChildren(api)
	s.RegisterSessions(api)
	s.RegisterAssessments(api)
	s.RegisterPhotos(api)
	s.RegisterConsents(api)
	s.RegisterReports(api)
	s.RegisterDashboard(api)
	s.RegisterPublic(pub)
	return app
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

type httpRes struct {
	status int
	header http.Header
	body   string
}

// mpWriteFile writes one multipart file part whose Content-Type declares what
// a browser would send for these bytes (net/http sniffing on the CLIENT side).
// Pembaruan 2026-10-11: the server stores under the declared type — no server
// sniff — so honest declarations land as .jpg/.png/.webp and anything else
// lands as .bin, never a rejection.
func mpWriteFile(t *testing.T, w *multipart.Writer, field, filename string, content []byte) {
	t.Helper()
	// net/textproto is off the spec's test-import allowlist (F-B08): pass the
	// unnamed map[string][]string type straight to CreatePart — it is
	// assignable to textproto.MIMEHeader and CreatePart writes the keys
	// as-is (already in canonical form).
	fw, err := w.CreatePart(map[string][]string{
		"Content-Disposition": {multipart.FileContentDisposition(field, filename)},
		"Content-Type":        {http.DetectContentType(content)},
	})
	require.NoError(t, err)
	_, err = fw.Write(content)
	require.NoError(t, err)
}

func doReq(t *testing.T, app *fiber.App, method, target, body string, headers map[string]string) httpRes {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
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

func jsonReq(t *testing.T, app *fiber.App, method, target string, payload any, headers map[string]string) httpRes {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	return doReq(t, app, method, target, string(body), headers)
}

func bearer(tok string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + tok}
}

// envelopeMap decodes one response body as a JSON object.
func envelopeMap(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoErrorf(t, json.Unmarshal([]byte(body), &m), "response is not a JSON object: %s", body)
	return m
}

// requireErrEnvelope asserts HTTP 200 with EXACTLY one top-level "error"
// object carrying code+message (a second envelope written by a buggy handler
// would produce a second key or unparseable JSON and fail here).
func requireErrEnvelope(t *testing.T, res httpRes, code string) map[string]any {
	t.Helper()
	require.Equal(t, 200, res.status, "envelope transport requires HTTP 200: %s", res.body)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 1, "failure envelope must carry exactly one top-level key: %s", res.body)
	errObj, ok := m["error"].(map[string]any)
	require.Truef(t, ok, "error envelope malformed: %s", res.body)
	require.Equal(t, code, errObj["code"])
	require.NotEmpty(t, errObj["message"])
	return errObj
}

// requireDataEnvelope asserts HTTP 200 with EXACTLY the success envelope
// ({data} or {data,meta}) and returns the data value.
func requireDataEnvelope(t *testing.T, res httpRes) any {
	t.Helper()
	require.Equal(t, 200, res.status, "envelope transport requires HTTP 200: %s", res.body)
	m := envelopeMap(t, res.body)
	_, hasData := m["data"]
	_, hasMeta := m["meta"]
	require.True(t, hasData, "success envelope must carry data: %s", res.body)
	require.Contains(t, []int{1, 2}, len(m), "success envelope must carry only data (+meta): %s", res.body)
	if hasMeta {
		require.Len(t, m, 2)
	}
	return m["data"]
}

// ---------------------------------------------------------------------------
// Auth fixtures
// ---------------------------------------------------------------------------

var fixedTS = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

var userRowCols = []string{
	"id", "name", "email", "wa_number", "password_hash", "role",
	"decision", "is_active", "tenant_id", "t_active", "created_at",
}

// sqlUserRow builds one userSelect row; tenant/tActive are nil for SA (NULL).
func sqlUserRow(id int64, name, email, wa, hash, role, decision string, active bool, tenant, tActive any) *sqlmock.Rows {
	return sqlmock.NewRows(userRowCols).
		AddRow(id, name, email, wa, hash, role, decision, active, tenant, tActive, fixedTS)
}

func sqlAdminRow() *sqlmock.Rows {
	return sqlUserRow(2, "Admin", "admin@school.id", "", testPasswordHash, "ADMIN", "APPROVED", true, int64(1), true)
}

func sqlFASRow() *sqlmock.Rows {
	return sqlUserRow(3, "Fasil", "fas@school.id", "+628111111111", testPasswordHash, "FASILITATOR", "APPROVED", true, int64(1), true)
}

func sqlSARow() *sqlmock.Rows {
	return sqlUserRow(1, "Super", "sa@kidversa.id", "", testPasswordHash, "SA", "APPROVED", true, nil, nil)
}

func saUserRow() *userRow {
	return &userRow{ID: 1, Name: "Super", Email: "sa@kidversa.id", Role: "SA",
		Decision: "APPROVED", IsActive: true, TenantActive: true}
}

func adminUserRow() *userRow {
	return &userRow{ID: 2, Name: "Admin", Email: "admin@school.id", Role: "ADMIN",
		Decision: "APPROVED", IsActive: true, TenantID: 1, TenantActive: true}
}

func fasUserRow() *userRow {
	return &userRow{ID: 3, Name: "Fasil", Email: "fas@school.id", Role: "FASILITATOR",
		Decision: "APPROVED", IsActive: true, TenantID: 1, TenantActive: true}
}

// mintAccess signs a real HS256 access token for the given identity.
func mintAccess(t *testing.T, s *Server, u *userRow) string {
	t.Helper()
	tok, err := s.signAccess(u)
	require.NoError(t, err)
	return tok
}

// mintRefresh signs a refresh JWT with an explicit jti/exp (logout/refresh
// tests need to know the jti they revoke).
func mintRefresh(t *testing.T, s *Server, u *userRow, jti string, exp time.Time) string {
	t.Helper()
	tok, err := s.signRefresh(u, jti, exp)
	require.NoError(t, err)
	return tok
}

// ---------------------------------------------------------------------------
// SQL matcher helpers
// ---------------------------------------------------------------------------

// q quotes a literal SQL fragment for sqlmock's regexp matcher (unanchored
// substring match — fragments must be unique within one test's expectations).
func q(fragment string) string { return regexp.QuoteMeta(fragment) }

// Distinctive fragments for the shared user lookups.
const (
	sqlUserByID    = "FROM users u LEFT JOIN tenants t ON t.id = u.tenant_id WHERE u.id = ?"
	sqlUserByEmail = "FROM users u LEFT JOIN tenants t ON t.id = u.tenant_id WHERE u.email = ?"
)

func sqlTenantByID() string {
	return q("SELECT id, name, type, is_active, created_at FROM tenants WHERE id = ?")
}

func sqlTenantLatestActive() string {
	return q("SELECT id, name, type, is_active, created_at FROM tenants WHERE is_active = TRUE ORDER BY id DESC LIMIT 1")
}

func sqlTenantActive() string { return q("SELECT is_active FROM tenants WHERE id = ?") }

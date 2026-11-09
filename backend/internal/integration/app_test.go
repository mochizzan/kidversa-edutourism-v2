//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/golang-jwt/jwt/v5"
	"github.com/kidversa/edutourism-v2/backend/internal"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// TestMain runs the integration suite from the backend root so NewServer
// finds its prompts/templates assets (the package dir is
// backend/internal/integration).
func TestMain(m *testing.M) {
	if err := os.Chdir("../.."); err != nil {
		panic("chdir to backend root: " + err.Error())
	}
	os.Exit(m.Run())
}

const (
	accessSecret  = "integration-access-secret"
	refreshSecret = "integration-refresh-secret"
	testPassword  = "Password123"
)

var testPasswordHash = func() string {
	h, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	return string(h)
}()

var fixedTS = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// testConfig builds a fully-populated Config through the exported fields;
// OpenwaBaseURL/UploadDir are pointed at per-test fixtures.
func testConfig(t *testing.T, openwaURL string) internal.Config {
	t.Helper()
	return internal.Config{
		JWTAccessSecret:  accessSecret,
		JWTRefreshSecret: refreshSecret,
		AccessTTL:        15 * time.Minute,
		RefreshTTL:       7 * 24 * time.Hour,
		ResetTTL:         15 * time.Minute,
		OpenwaBaseURL:    openwaURL,
		OpenwaSession:    "kidversa-test",
		PublicWebURL:     "http://localhost:8002",
		UploadDir:        t.TempDir(),
		CorsOrigins:      []string{"http://localhost:8002"},
	}
}

// newApp assembles the production route table exactly like cmd/api/main.go
// (recover + requestid + the same middleware-ordered ErrorHandler, then ALL
// registrars). Fiber panics on duplicate paths, so building the full app here
// is the runtime route-conflict check for the entire backend.
func newApp(t *testing.T, cfg internal.Config) (*fiber.App, sqlmock.Sqlmock, *internal.Server) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	s, err := internal.NewServer(db, cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sqlmock expectations: %v", err)
		}
		db.Close()
	})

	app := fiber.New(fiber.Config{
		BodyLimit:    52 * 1024 * 1024, // matches main.go (52m, Pembaruan 2026-10-11)
		ErrorHandler: internal.ErrorHandler,
	})
	app.Use(recover.New())
	app.Use(requestid.New())
	api := app.Group("/api")
	pub := app.Group("/public")

	// main.go wiring order — a registration conflict panics right here.
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
	return app, mock, s
}

// ---------------------------------------------------------------------------
// Request/envelope helpers
// ---------------------------------------------------------------------------

type httpRes struct {
	status int
	header http.Header
	body   string
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

func envelopeMap(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoErrorf(t, json.Unmarshal([]byte(body), &m), "response is not a JSON object: %s", body)
	return m
}

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

func requireDataEnvelope(t *testing.T, res httpRes) any {
	t.Helper()
	require.Equal(t, 200, res.status, "envelope transport requires HTTP 200: %s", res.body)
	m := envelopeMap(t, res.body)
	require.Contains(t, m, "data", "success envelope must carry data: %s", res.body)
	require.LessOrEqual(t, len(m), 2, "success envelope carries only data (+meta): %s", res.body)
	return m["data"]
}

// ---------------------------------------------------------------------------
// Tokens + SQL fixtures
// ---------------------------------------------------------------------------

// mintAccess issues a real HS256 access token for one subject/role/tenant.
func mintAccess(t *testing.T, sub int64, role string, tenant int64) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":    strconv.FormatInt(sub, 10),
		"role":   role,
		"tenant": tenant,
		"iat":    time.Now().Unix(),
		"exp":    time.Now().Add(15 * time.Minute).Unix(),
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(accessSecret))
	require.NoError(t, err)
	return tok
}

func q(fragment string) string { return regexp.QuoteMeta(fragment) }

var userRowCols = []string{
	"id", "name", "email", "wa_number", "password_hash", "role",
	"decision", "is_active", "tenant_id", "t_active", "created_at",
}

func sqlAdminRow() *sqlmock.Rows {
	return sqlmock.NewRows(userRowCols).
		AddRow(2, "Admin", "admin@school.id", "", testPasswordHash, "ADMIN", "APPROVED", true, int64(1), true, fixedTS)
}

func sqlTenantBandung() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "name", "type", "is_active", "created_at"}).
		AddRow(1, "Bandung", "KOTA", true, fixedTS)
}

const sqlUserByID = "FROM users u LEFT JOIN tenants t ON t.id = u.tenant_id WHERE u.id = ?"

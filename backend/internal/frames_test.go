package internal

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// frames_test.go — frames.go (F-06 frame master, 02 §2b) unit coverage:
// envelope guards, list allowlists with exact SQL order/args, the multipart
// create/patch validation gates, frame_programs transactions, real image
// files under UPLOAD_DIR and frame.changed publishes after commit. sqlmock
// is ordered: expectations are queued in handler execution order and are
// verified at test end.

// ---------------------------------------------------------------------------
// Frames-only fixtures (shared helpers live in helpers_test.go / schools_test.go)
// ---------------------------------------------------------------------------

// framesPNG is a PNG payload for the multipart image field (the multipart
// helper declares image/png client-side). Pembaruan 2026-11-10 (spec 09):
// the server DecodeConfigs real image bytes, so the payload must decodably
// start with a valid PNG (the trailing pad is never parsed by DecodeConfig).
var framesPNG = append(realPNG(8, 8), bytes.Repeat([]byte{1}, 64)...)

// Fixed DB-relative image paths shaped like storeImage output (32 hex chars).
const (
	frameRelA   = "frames/1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png"
	frameRelB   = "frames/1/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.png"
	frameRelOld = "frames/1/cccccccccccccccccccccccccccccccc.png"
	frameRelNew = "frames/1/dddddddddddddddddddddddddddddddd.png"
	frameRelDel = "frames/1/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee.png"
)

// SQL fragments shared across the frame routes.
const (
	frameItemFrag = `FROM frames f WHERE id = ? AND school_id = ?`
	frameCntFrag  = `SELECT COUNT(*) FROM frames f WHERE f.school_id = ?`
)

var (
	frameItemCols = []string{"id", "image_path", "name"}
	frameLinkCols = []string{"frame_id", "program_id", "program_name_snap"}
	frameProgCols = []string{"id", "name"}
	frameCntCols  = []string{"count"}
)

// framePathCapture is a sqlmock Argument that records the random storeImage
// path a write persisted, tying INSERT/UPDATE args to the file on disk.
type framePathCapture struct{ v string }

func (c *framePathCapture) Match(val driver.Value) bool {
	s, ok := val.(string)
	if !ok {
		return false
	}
	c.v = s
	return true
}

// frameItemRows builds one frameSelect result row.
func frameItemRows(id int64, path, name string) *sqlmock.Rows {
	return sqlmock.NewRows(frameItemCols).AddRow(id, path, name)
}

// frameLinkQ / frameProgsQ quote the IN (...) fragments for n ids.
func frameLinkQ(n int) string {
	ph := strings.TrimSuffix(strings.Repeat("?,", n), ",")
	return q(`WHERE school_id = ? AND frame_id IN (` + ph + `) ORDER BY program_name_snap`)
}

func frameProgsQ(n int) string {
	ph := strings.TrimSuffix(strings.Repeat("?,", n), ",")
	return q(`SELECT id, name FROM programs WHERE school_id = ? AND id IN (` + ph + `)`)
}

// frameSeedImage pre-creates an image file under the upload root so the
// removeMasterImage share-check paths can assert on real disk state.
func frameSeedImage(t *testing.T, s *Server, rel string) string {
	t.Helper()
	full := filepath.Join(s.upload, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, framesPNG, 0o644))
	return full
}

// frameMPReq posts a multipart body whose form keys may repeat — the shared
// masterMP map cannot express program_ids=5&program_ids=5.
func frameMPReq(t *testing.T, app *fiber.App, target string, fields [][2]string,
	files map[string]string, headers map[string]string) httpRes {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, kv := range fields {
		require.NoError(t, w.WriteField(kv[0], kv[1]))
	}
	for field, content := range files {
		mpWriteFile(t, w, field, "upload.bin", []byte(content))
	}
	require.NoError(t, w.Close())
	req := httptest.NewRequest("POST", target, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
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

// ---------------------------------------------------------------------------
// Route/envelope guards: token, role, school (06 §1 — HTTP 200 envelopes)
// ---------------------------------------------------------------------------

func TestFramesRouteGuards(t *testing.T) {
	t.Run("no token is INVALID_TOKEN with no SQL", func(t *testing.T) {
		s, _ := newTestServer(t)
		app := newTestApp(t, s)
		res := doReq(t, app, "GET", "/api/frames", "", nil)
		requireErrEnvelope(t, res, CodeInvalidToken)
	})

	t.Run("FASILITATOR list and create are FORBIDDEN_ROLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		// Role is checked right after the user load — no school query follows.
		expectFASAuth(mock)
		requireErrEnvelope(t,
			doReq(t, app, "GET", "/api/frames", "", hFAS(t, s)), CodeForbiddenRole)
		expectFASAuth(mock)
		requireErrEnvelope(t,
			doReq(t, app, "POST", "/api/frames", `{"name":"x"}`, hFAS(t, s)), CodeForbiddenRole)
	})

	t.Run("SA without X-School-ID is VALIDATION before any row query", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		for _, tc := range []struct{ method, target, body string }{
			{"GET", "/api/frames", ""},
			{"POST", "/api/frames", `{"name":"x"}`},
			{"GET", "/api/frames/1", ""},
			{"PATCH", "/api/frames/1", ""},
			{"DELETE", "/api/frames/1", ""},
		} {
			expectSAAuth(mock, 0) // kvSchool=0 → requireSchool; no further SQL
			res := doReq(t, app, tc.method, tc.target, tc.body, hSABare(t, s))
			requireErrEnvelope(t, res, CodeValidation)
		}
	})
}

// ---------------------------------------------------------------------------
// listFrames: default sort/paging, item shape, program/search filters
// ---------------------------------------------------------------------------

func TestFramesListDefaultSortPaged(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(frameCntFrag)).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows(frameCntCols).AddRow(2))
	// frameSelect declares the `f` alias (frames.go:32), so the row query reads
	// "FROM frames f WHERE f.school_id …" — assert the SQL the handler sends.
	mock.ExpectQuery(q("FROM frames f WHERE f.school_id = ? ORDER BY id asc, f.id ASC LIMIT ? OFFSET ?")).
		WithArgs(int64(1), 20, 0).
		WillReturnRows(frameItemRows(11, frameRelA, "Alpha").
			AddRow(12, frameRelB, "Beta"))
	mock.ExpectQuery(frameLinkQ(2)).
		WithArgs(int64(1), int64(11), int64(12)).
		WillReturnRows(sqlmock.NewRows(frameLinkCols).AddRow(11, 4, "Empat"))

	res := doReq(t, app, "GET", "/api/frames", "", hAdmin(t, s))
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2, "paged envelope is data+meta only: %s", res.body)
	require.Equal(t,
		map[string]any{"page": float64(1), "per_page": float64(20), "total": float64(2)},
		m["meta"])
	items := m["data"].([]any)
	require.Len(t, items, 2)

	first := items[0].(map[string]any)
	require.Equal(t, float64(11), first["id"])
	require.Equal(t, "Alpha", first["name"])
	require.Equal(t, "/uploads/"+frameRelA, first["image_url"])
	require.Equal(t,
		[]any{map[string]any{"id": float64(4), "name": "Empat"}},
		first["programs"])
	require.Equal(t, "/uploads/"+frameRelB, items[1].(map[string]any)["image_url"])
	require.Equal(t, []any{}, items[1].(map[string]any)["programs"],
		"a frame without links serializes [] not null")
}

func TestFramesListProgramFilterSearchAndEmpty(t *testing.T) {
	t.Run("program_id adds EXISTS and still loads links", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameCntFrag+` AND EXISTS (SELECT 1 FROM frame_programs fp`)).
			WithArgs(int64(1), int64(3)).
			WillReturnRows(sqlmock.NewRows(frameCntCols).AddRow(1))
		mock.ExpectQuery(q(`WHERE fp.frame_id = f.id AND fp.program_id = ?) ORDER BY id asc, f.id ASC LIMIT ? OFFSET ?`)).
			WithArgs(int64(1), int64(3), 20, 0).
			WillReturnRows(frameItemRows(21, frameRelA, "Gamma"))
		mock.ExpectQuery(frameLinkQ(1)).
			WithArgs(int64(1), int64(21)).
			WillReturnRows(sqlmock.NewRows(frameLinkCols)) // links absent

		res := doReq(t, app, "GET", "/api/frames?program_id=3", "", hAdmin(t, s))
		items := requireDataEnvelope(t, res).([]any)
		require.Len(t, items, 1)
		require.Equal(t, float64(21), items[0].(map[string]any)["id"])
		require.Equal(t, []any{}, items[0].(map[string]any)["programs"])
	})

	t.Run("empty page skips the link query entirely", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameCntFrag+` AND EXISTS (SELECT 1 FROM frame_programs fp`)).
			WithArgs(int64(1), int64(3)).
			WillReturnRows(sqlmock.NewRows(frameCntCols).AddRow(0))
		mock.ExpectQuery(q(`WHERE fp.frame_id = f.id AND fp.program_id = ?) ORDER BY id asc, f.id ASC LIMIT ? OFFSET ?`)).
			WithArgs(int64(1), int64(3), 20, 0).
			WillReturnRows(sqlmock.NewRows(frameItemCols))
		// No frame_programs expectation: framePrograms early-returns on zero ids.

		res := doReq(t, app, "GET", "/api/frames?program_id=3", "", hAdmin(t, s))
		m := envelopeMap(t, res.body)
		require.Equal(t, []any{}, m["data"])
		require.Equal(t,
			map[string]any{"page": float64(1), "per_page": float64(20), "total": float64(0)},
			m["meta"])
	})

	t.Run("q and sort=name flow into COUNT and ORDER BY", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameCntFrag+` AND f.name LIKE ?`)).
			WithArgs(int64(1), "%amm%").
			WillReturnRows(sqlmock.NewRows(frameCntCols).AddRow(0))
		mock.ExpectQuery(q("FROM frames f WHERE f.school_id = ? AND f.name LIKE ? ORDER BY f.name asc, f.id ASC LIMIT ? OFFSET ?")).
			WithArgs(int64(1), "%amm%", 20, 0).
			WillReturnRows(sqlmock.NewRows(frameItemCols))

		res := doReq(t, app, "GET", "/api/frames?q=amm&sort=name", "", hAdmin(t, s))
		m := envelopeMap(t, res.body)
		require.Equal(t, []any{}, m["data"])
		require.Equal(t, float64(0), m["meta"].(map[string]any)["total"])
	})
}

func TestFramesListRejectsBadParamsBeforeCount(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	for _, target := range []string{
		"/api/frames?program_id=abc",  // queryIDFilter: numeric, > 0
		"/api/frames?program_id=0",    //
		"/api/frames?sort=image_path", // outside the sort allowlist (E1)
		"/api/frames?page=0",          // page ≥ 1
		"/api/frames?per_page=7",      // only the allowed set
		"/api/frames?dir=sideways",    // asc | desc
	} {
		expectAdminAuth(mock) // rejected before the COUNT query
		res := doReq(t, app, "GET", target, "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	}
}

// ---------------------------------------------------------------------------
// listFrames: failure propagation (06 §2 → DB_UNAVAILABLE, single envelope)
// ---------------------------------------------------------------------------

func TestFramesListFailuresAreDBUnavailable(t *testing.T) {
	t.Run("count query fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameCntFrag)).
			WillReturnError(errors.New("connection refused"))
		requireErrEnvelope(t,
			doReq(t, app, "GET", "/api/frames", "", hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("row query fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameCntFrag)).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows(frameCntCols).AddRow(1))
		// No row-query expectation → sqlmock rejects the call → failDB.
		requireErrEnvelope(t,
			doReq(t, app, "GET", "/api/frames", "", hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("row scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameCntFrag)).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows(frameCntCols).AddRow(1))
		mock.ExpectQuery(q("FROM frames f WHERE f.school_id = ? ORDER BY id asc, f.id ASC LIMIT ? OFFSET ?")).
			WithArgs(int64(1), 20, 0).
			WillReturnRows(sqlmock.NewRows(frameItemCols).AddRow(11, nil, "Alpha"))
		requireErrEnvelope(t,
			doReq(t, app, "GET", "/api/frames", "", hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("row iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameCntFrag)).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows(frameCntCols).AddRow(1))
		mock.ExpectQuery(q("FROM frames f WHERE f.school_id = ? ORDER BY id asc, f.id ASC LIMIT ? OFFSET ?")).
			WithArgs(int64(1), 20, 0).
			WillReturnRows(sqlmock.NewRows(frameItemCols).
				AddRow(11, frameRelA, "Alpha").
				RowError(0, errors.New("stream broke")))
		requireErrEnvelope(t,
			doReq(t, app, "GET", "/api/frames", "", hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("link query fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameCntFrag)).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows(frameCntCols).AddRow(1))
		mock.ExpectQuery(q("FROM frames f WHERE f.school_id = ? ORDER BY id asc, f.id ASC LIMIT ? OFFSET ?")).
			WithArgs(int64(1), 20, 0).
			WillReturnRows(frameItemRows(11, frameRelA, "Alpha"))
		// No link expectation → framePrograms errors → failDB.
		requireErrEnvelope(t,
			doReq(t, app, "GET", "/api/frames", "", hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("link scan fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameCntFrag)).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows(frameCntCols).AddRow(1))
		mock.ExpectQuery(q("FROM frames f WHERE f.school_id = ? ORDER BY id asc, f.id ASC LIMIT ? OFFSET ?")).
			WithArgs(int64(1), 20, 0).
			WillReturnRows(frameItemRows(11, frameRelA, "Alpha"))
		mock.ExpectQuery(frameLinkQ(1)).
			WithArgs(int64(1), int64(11)).
			WillReturnRows(sqlmock.NewRows(frameLinkCols).AddRow(nil, 4, "Empat"))
		requireErrEnvelope(t,
			doReq(t, app, "GET", "/api/frames", "", hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("link iteration fails", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameCntFrag)).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows(frameCntCols).AddRow(1))
		mock.ExpectQuery(q("FROM frames f WHERE f.school_id = ? ORDER BY id asc, f.id ASC LIMIT ? OFFSET ?")).
			WithArgs(int64(1), 20, 0).
			WillReturnRows(frameItemRows(11, frameRelA, "Alpha"))
		mock.ExpectQuery(frameLinkQ(1)).
			WithArgs(int64(1), int64(11)).
			WillReturnRows(sqlmock.NewRows(frameLinkCols).
				AddRow(11, 4, "Empat").
				RowError(0, errors.New("stream broke")))
		requireErrEnvelope(t,
			doReq(t, app, "GET", "/api/frames", "", hAdmin(t, s)), CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// createFrame: validation gates fire before ANY query (06 §1, E13-style)
// ---------------------------------------------------------------------------

func TestFramesCreateRejectsBeforeProgramQuery(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	img := map[string]string{"image": string(framesPNG)}

	for _, tc := range []struct {
		name     string
		jsonBody string // non-multipart request (empty → multipart)
		fields   map[string]string
		files    map[string]string
	}{
		{"non-multipart body", `{"name":"Poster"}`, nil, nil},
		{"missing name", "", map[string]string{"program_ids": "5"}, img},
		{"blank name", "", map[string]string{"name": "   ", "program_ids": "5"}, img},
		{"name over 200 chars", "", map[string]string{
			"name": strings.Repeat("n", 201), "program_ids": "5"}, img},
		{"missing image file", "", map[string]string{
			"name": "Poster", "program_ids": "5"}, nil},
		{"program_ids absent on create", "", map[string]string{"name": "Poster"}, img},
		{"program_ids empty value", "", map[string]string{
			"name": "Poster", "program_ids": ""}, img},
		{"program_ids not numeric", "", map[string]string{
			"name": "Poster", "program_ids": "abc"}, img},
		{"program_ids zero", "", map[string]string{
			"name": "Poster", "program_ids": "0"}, img},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectAdminAuth(mock) // the ONLY query: programs IN must not run
			var res httpRes
			if tc.jsonBody != "" {
				res = doReq(t, app, "POST", "/api/frames", tc.jsonBody, hAdmin(t, s))
			} else {
				res = doMP(t, app, "POST", "/api/frames", tc.fields, tc.files, hAdmin(t, s))
			}
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

func TestFramesCreateProgramIDsGate(t *testing.T) {
	pngMP := map[string]string{"image": string(framesPNG)}

	t.Run("unknown program is NOT_FOUND before any write", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(frameProgsQ(1)).
			WithArgs(int64(1), int64(5)).
			WillReturnRows(sqlmock.NewRows(frameProgCols)) // program 5 not in school
		res := doMP(t, app, "POST", "/api/frames",
			map[string]string{"name": "Poster", "program_ids": "5"}, pngMP, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
		require.NoDirExists(t, filepath.Join(s.upload, "frames", "1"),
			"NOT_FOUND fires before storeImage")
	})

	t.Run("programs query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock) // no programs expectation → sqlmock rejects the call
		res := doMP(t, app, "POST", "/api/frames",
			map[string]string{"name": "Poster", "program_ids": "5"}, pngMP, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("programs scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(frameProgsQ(1)).
			WithArgs(int64(1), int64(5)).
			WillReturnRows(sqlmock.NewRows(frameProgCols).AddRow(5, nil))
		res := doMP(t, app, "POST", "/api/frames",
			map[string]string{"name": "Poster", "program_ids": "5"}, pngMP, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("programs row iteration failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(frameProgsQ(1)).
			WithArgs(int64(1), int64(5)).
			WillReturnRows(sqlmock.NewRows(frameProgCols).
				AddRow(5, "Lima").
				RowError(0, errors.New("stream broke")))
		res := doMP(t, app, "POST", "/api/frames",
			map[string]string{"name": "Poster", "program_ids": "5"}, pngMP, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("duplicate program_ids collapse to ONE lookup with one arg", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		// A second programs query, or (1,5,5) args, would not match this
		// expectation → the 200 below proves the deduped single lookup (the
		// request then stores its image and inserts exactly one link row).
		mock.ExpectQuery(frameProgsQ(1)).
			WithArgs(int64(1), int64(5)).
			WillReturnRows(sqlmock.NewRows(frameProgCols).AddRow(5, "Lima"))
		stored := &framePathCapture{}
		mock.ExpectBegin()
		mock.ExpectExec(q("INSERT INTO frames (school_id, image_path, name) VALUES (?, ?, ?)")).
			WithArgs(int64(1), stored, "Dedupe").
			WillReturnResult(sqlmock.NewResult(8, 1))
		mock.ExpectExec(q(`INSERT INTO frame_programs (frame_id, program_id, program_name_snap, school_id)`)).
			WithArgs(int64(8), int64(5), "Lima", int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(q(frameItemFrag)).
			WithArgs(int64(8), int64(1)).
			WillReturnRows(frameItemRows(8, frameRelNew, "Dedupe"))
		mock.ExpectQuery(frameLinkQ(1)).
			WithArgs(int64(1), int64(8)).
			WillReturnRows(sqlmock.NewRows(frameLinkCols).AddRow(8, 5, "Lima"))
		res := frameMPReq(t, app, "/api/frames", [][2]string{
			{"name", "Dedupe"},
			{"program_ids", "5"},
			{"program_ids", "5"},
		}, map[string]string{"image": string(framesPNG)}, hAdmin(t, s))
		requireDataEnvelope(t, res)
		// The verified-decodable PNG part stores as .png — Pembaruan
		// 2026-11-10: the stored extension comes from verified data.
		require.Regexp(t, `^frames/1/[0-9a-f]{32}\.png$`, stored.v)
		require.FileExists(t, filepath.Join(s.upload, filepath.FromSlash(stored.v)),
			"storeImage must write the durable file")
	})

	t.Run("program_ids[] spelling is accepted", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		// The bracket field alone feeds the lookup — absent support would
		// answer VALIDATION before running this expectation (unmet → fail).
		mock.ExpectQuery(frameProgsQ(1)).
			WithArgs(int64(1), int64(5)).
			WillReturnRows(sqlmock.NewRows(frameProgCols).AddRow(5, "Lima"))
		mock.ExpectBegin()
		mock.ExpectExec(q("INSERT INTO frames (school_id, image_path, name) VALUES (?, ?, ?)")).
			WithArgs(int64(1), sqlmock.AnyArg(), "Bracket").
			WillReturnResult(sqlmock.NewResult(9, 1))
		mock.ExpectExec(q(`INSERT INTO frame_programs (frame_id, program_id, program_name_snap, school_id)`)).
			WithArgs(int64(9), int64(5), "Lima", int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(q(frameItemFrag)).
			WithArgs(int64(9), int64(1)).
			WillReturnRows(frameItemRows(9, frameRelNew, "Bracket"))
		mock.ExpectQuery(frameLinkQ(1)).
			WithArgs(int64(1), int64(9)).
			WillReturnRows(sqlmock.NewRows(frameLinkCols).AddRow(9, 5, "Lima"))
		res := doMP(t, app, "POST", "/api/frames",
			map[string]string{"name": "Bracket", "program_ids[]": "5"},
			map[string]string{"image": string(framesPNG)}, hAdmin(t, s))
		requireDataEnvelope(t, res)
		entries, err := os.ReadDir(filepath.Join(s.upload, "frames", "1"))
		require.NoError(t, err)
		require.Len(t, entries, 1, "the uploaded image stored under the verified format")
		require.Regexp(t, `^[0-9a-f]{32}\.png$`, entries[0].Name())
	})
}

func TestFramesCreateSuccessPublishesAfterCommit(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(frameProgsQ(2)).
		WithArgs(int64(1), int64(5), int64(6)).
		WillReturnRows(sqlmock.NewRows(frameProgCols).
			AddRow(5, "Lima").
			AddRow(6, "Enam"))
	stored := &framePathCapture{}
	mock.ExpectBegin()
	mock.ExpectExec(q("INSERT INTO frames (school_id, image_path, name) VALUES (?, ?, ?)")).
		WithArgs(int64(1), stored, "Poster"). // name trimmed before persist
		WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectExec(q(`INSERT INTO frame_programs (frame_id, program_id, program_name_snap, school_id)`)).
		WithArgs(int64(7), int64(5), "Lima", int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q(`INSERT INTO frame_programs (frame_id, program_id, program_name_snap, school_id)`)).
		WithArgs(int64(7), int64(6), "Enam", int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(q(frameItemFrag)).
		WithArgs(int64(7), int64(1)).
		WillReturnRows(frameItemRows(7, frameRelNew, "Poster"))
	mock.ExpectQuery(frameLinkQ(1)).
		WithArgs(int64(1), int64(7)).
		WillReturnRows(sqlmock.NewRows(frameLinkCols).
			AddRow(7, 6, "Enam"). // ORDER BY program_name_snap
			AddRow(7, 5, "Lima"))

	res := frameMPReq(t, app, "/api/frames", [][2]string{
		{"name", "  Poster  "},
		{"program_ids", "5"},
		{"program_ids", "6"},
	}, map[string]string{"image": string(framesPNG)}, hAdmin(t, s))

	// The INSERT persisted the random storeImage path — assert the real file.
	require.Regexp(t, `^frames/1/[0-9a-f]{32}\.png$`, stored.v)
	full := filepath.Join(s.upload, filepath.FromSlash(stored.v))
	require.FileExists(t, full, "storeImage must write the durable file")
	entries, err := os.ReadDir(filepath.Join(s.upload, "frames", "1"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "exactly the one stored frame image")
	require.Equal(t, filepath.Base(stored.v), entries[0].Name())

	frame := requireDataEnvelope(t, res).(map[string]any)["frame"].(map[string]any)
	require.Equal(t, float64(7), frame["id"])
	require.Equal(t, "Poster", frame["name"])
	require.Equal(t, "/uploads/"+frameRelNew, frame["image_url"])
	require.Equal(t,
		[]any{
			map[string]any{"id": float64(6), "name": "Enam"},
			map[string]any{"id": float64(5), "name": "Lima"},
		},
		frame["programs"])

	f := takeFrame(t, spy)
	require.Equal(t, "frame.changed", f.Entity)
	require.Equal(t, int64(7), f.EntityID)
	require.Equal(t, "created", f.Action)
	require.Equal(t, int64(1), f.SchoolID, "publish routes to the frame school")
}

func TestFramesCreateFailuresAreDBUnavailable(t *testing.T) {
	t.Run("INSERT failure rolls back", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(frameProgsQ(1)).
			WithArgs(int64(1), int64(5)).
			WillReturnRows(sqlmock.NewRows(frameProgCols).AddRow(5, "Lima"))
		mock.ExpectBegin()
		mock.ExpectExec(q("INSERT INTO frames (school_id, image_path, name) VALUES (?, ?, ?)")).
			WithArgs(int64(1), sqlmock.AnyArg(), "Poster").
			WillReturnError(errors.New("deadlock"))
		mock.ExpectRollback()

		res := doMP(t, app, "POST", "/api/frames",
			map[string]string{"name": "Poster", "program_ids": "5"}, frameImgFile(), hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("row vanishing after commit answers NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(frameProgsQ(1)).
			WithArgs(int64(1), int64(5)).
			WillReturnRows(sqlmock.NewRows(frameProgCols).AddRow(5, "Lima"))
		mock.ExpectBegin()
		mock.ExpectExec(q("INSERT INTO frames (school_id, image_path, name) VALUES (?, ?, ?)")).
			WithArgs(int64(1), sqlmock.AnyArg(), "Poster").
			WillReturnResult(sqlmock.NewResult(7, 1))
		mock.ExpectExec(q(`INSERT INTO frame_programs (frame_id, program_id, program_name_snap, school_id)`)).
			WithArgs(int64(7), int64(5), "Lima", int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(q(frameItemFrag)).
			WithArgs(int64(7), int64(1)).
			WillReturnError(sql.ErrNoRows) // raced concurrent delete

		res := doMP(t, app, "POST", "/api/frames",
			map[string]string{"name": "Poster", "program_ids": "5"}, frameImgFile(), hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})
}

// frameImgFile is the multipart image fixture for the doMP-based tests.
func frameImgFile() map[string]string {
	return map[string]string{"image": string(framesPNG)}
}

// ---------------------------------------------------------------------------
// getFrame: found / unknown / malformed / failure propagation
// ---------------------------------------------------------------------------

func TestFramesGetFrameDetail(t *testing.T) {
	t.Run("found with links", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameItemFrag)).
			WithArgs(int64(10), int64(1)).
			WillReturnRows(frameItemRows(10, frameRelA, "Frame A"))
		mock.ExpectQuery(frameLinkQ(1)).
			WithArgs(int64(1), int64(10)).
			WillReturnRows(sqlmock.NewRows(frameLinkCols).AddRow(10, 4, "Empat"))

		res := doReq(t, app, "GET", "/api/frames/10", "", hAdmin(t, s))
		frame := requireDataEnvelope(t, res).(map[string]any)["frame"].(map[string]any)
		require.Equal(t, float64(10), frame["id"])
		require.Equal(t, "Frame A", frame["name"])
		require.Equal(t, "/uploads/"+frameRelA, frame["image_url"])
		require.Equal(t,
			[]any{map[string]any{"id": float64(4), "name": "Empat"}},
			frame["programs"])
	})

	t.Run("unknown id is NOT_FOUND before the link query", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameItemFrag)).
			WithArgs(int64(404), int64(1)).
			WillReturnError(sql.ErrNoRows)
		// No link expectation: framePrograms must not run for a missing row.
		res := doReq(t, app, "GET", "/api/frames/404", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("malformed id is NOT_FOUND without any lookup", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		res := doReq(t, app, "GET", "/api/frames/abc", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("load failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock) // no item expectation → sqlmock rejects the call
		res := doReq(t, app, "GET", "/api/frames/10", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("link failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameItemFrag)).
			WithArgs(int64(10), int64(1)).
			WillReturnRows(frameItemRows(10, frameRelA, "Frame A"))
		// No link expectation → framePrograms errors after the row was found.
		res := doReq(t, app, "GET", "/api/frames/10", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// patchFrame: load-first gates, then validation, then the relink Tx
// ---------------------------------------------------------------------------

func TestFramesPatchRejectsBeforeWrite(t *testing.T) {
	t.Run("non-multipart body is VALIDATION before the load", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock) // masterForm fails — no SELECT may follow
		res := doReq(t, app, "PATCH", "/api/frames/5", `{"name":"Baru"}`, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("malformed id is NOT_FOUND without any lookup", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		res := doReq(t, app, "PATCH", "/api/frames/abc", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("unknown id is NOT_FOUND before any write", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameItemFrag)).
			WithArgs(int64(404), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := doMP(t, app, "PATCH", "/api/frames/404", nil, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("load failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock) // no item expectation → sqlmock rejects the call
		res := doMP(t, app, "PATCH", "/api/frames/5", nil, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	for _, tc := range []struct {
		name   string
		fields map[string]string
	}{
		{"blank name", map[string]string{"name": "   "}},
		{"name over 200 chars", map[string]string{"name": strings.Repeat("n", 201)}},
		{"program_ids not numeric", map[string]string{"program_ids": "abc"}},
	} {
		t.Run(tc.name+" is VALIDATION after load, no Tx", func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			expectAdminAuth(mock)
			mock.ExpectQuery(q(frameItemFrag)).
				WithArgs(int64(5), int64(1)).
				WillReturnRows(frameItemRows(5, frameRelOld, "Lama"))
			// No Begin: a stray transaction would fail the mock → wrong code.
			res := doMP(t, app, "PATCH", "/api/frames/5", tc.fields, nil, hAdmin(t, s))
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

func TestFramesPatchReplacesImageShareChecked(t *testing.T) {
	for _, tc := range []struct {
		name   string
		shared int // session_topics rows still pointing at the old bytes
	}{
		{"unshared old image is removed", 0},
		{"image shared with a session survives", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			oldFull := frameSeedImage(t, s, frameRelOld)
			spy := frameSpy(s, 1)
			defer s.unsubscribe(spy)

			expectAdminAuth(mock)
			mock.ExpectQuery(q(frameItemFrag)).
				WithArgs(int64(5), int64(1)).
				WillReturnRows(frameItemRows(5, frameRelOld, "Lama"))
			stored := &framePathCapture{}
			mock.ExpectBegin()
			// name absent → stays cur.Name; image_path becomes the new store.
			mock.ExpectExec(q("UPDATE frames SET name = ?, image_path = ? WHERE id = ? AND school_id = ?")).
				WithArgs("Lama", stored, int64(5), int64(1)).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			mock.ExpectQuery(q("SELECT COUNT(*) FROM session_topics WHERE badge_image_snap = ?")).
				WithArgs(frameRelOld).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(tc.shared))
			// program_ids absent → NO DELETE frame_programs may run (the next
			// expectation is the reload; a stray DELETE fails the mock).
			mock.ExpectQuery(q(frameItemFrag)).
				WithArgs(int64(5), int64(1)).
				WillReturnRows(frameItemRows(5, frameRelNew, "Lama"))
			mock.ExpectQuery(frameLinkQ(1)).
				WithArgs(int64(1), int64(5)).
				WillReturnRows(sqlmock.NewRows(frameLinkCols).AddRow(5, 9, "Sembilan"))

			res := doMP(t, app, "PATCH", "/api/frames/5", nil,
				frameImgFile(), hAdmin(t, s))
			frame := requireDataEnvelope(t, res).(map[string]any)["frame"].(map[string]any)
			require.Equal(t, "/uploads/"+frameRelNew, frame["image_url"])
			require.Equal(t, "Lama", frame["name"])
			require.Equal(t,
				[]any{map[string]any{"id": float64(9), "name": "Sembilan"}},
				frame["programs"])

			f := takeFrame(t, spy)
			require.Equal(t, "frame.changed", f.Entity)
			require.Equal(t, int64(5), f.EntityID)
			require.Equal(t, "updated", f.Action)

			require.Regexp(t, `^frames/1/[0-9a-f]{32}\.png$`, stored.v)
			require.FileExists(t, filepath.Join(s.upload, filepath.FromSlash(stored.v)),
				"the replacement image must be durable")
			if tc.shared == 0 {
				require.NoFileExists(t, oldFull, "unshared old image must be swept")
			} else {
				require.FileExists(t, oldFull, "shared bytes must survive")
			}
		})
	}
}

func TestFramesPatchRelinksProgramsWithoutImage(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(frameItemFrag)).
		WithArgs(int64(5), int64(1)).
		WillReturnRows(frameItemRows(5, frameRelOld, "Lama"))
	mock.ExpectQuery(frameProgsQ(1)).
		WithArgs(int64(1), int64(9)).
		WillReturnRows(sqlmock.NewRows(frameProgCols).AddRow(9, "Sembilan"))
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE frames SET name = ?, image_path = ? WHERE id = ? AND school_id = ?")).
		WithArgs("Baru", frameRelOld, int64(5), int64(1)). // image untouched
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("DELETE FROM frame_programs WHERE frame_id = ? AND school_id = ?")).
		WithArgs(int64(5), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q(`INSERT INTO frame_programs (frame_id, program_id, program_name_snap, school_id)`)).
		WithArgs(int64(5), int64(9), "Sembilan", int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// image_path unchanged → NO share-check query; next is the reload.
	mock.ExpectQuery(q(frameItemFrag)).
		WithArgs(int64(5), int64(1)).
		WillReturnRows(frameItemRows(5, frameRelOld, "Baru"))
	mock.ExpectQuery(frameLinkQ(1)).
		WithArgs(int64(1), int64(5)).
		WillReturnRows(sqlmock.NewRows(frameLinkCols).AddRow(5, 9, "Sembilan"))

	res := doMP(t, app, "PATCH", "/api/frames/5",
		map[string]string{"name": "Baru", "program_ids": "9"}, nil, hAdmin(t, s))
	frame := requireDataEnvelope(t, res).(map[string]any)["frame"].(map[string]any)
	require.Equal(t, "Baru", frame["name"])
	require.Equal(t, "/uploads/"+frameRelOld, frame["image_url"],
		"no image field → image_path stays cur.imagePath")
	require.Equal(t,
		[]any{map[string]any{"id": float64(9), "name": "Sembilan"}},
		frame["programs"])
	require.Equal(t, "updated", takeFrame(t, spy).Action)
}

func TestFramesPatchReloadOutcomes(t *testing.T) {
	prefix := func(t *testing.T, mock sqlmock.Sqlmock) {
		t.Helper()
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameItemFrag)).
			WithArgs(int64(5), int64(1)).
			WillReturnRows(frameItemRows(5, frameRelOld, "Lama"))
		mock.ExpectBegin()
		mock.ExpectExec(q("UPDATE frames SET name = ?, image_path = ? WHERE id = ? AND school_id = ?")).
			WithArgs("Baru", frameRelOld, int64(5), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
	}
	body := map[string]string{"name": "Baru"}

	t.Run("row vanishing after commit answers NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		prefix(t, mock)
		mock.ExpectQuery(q(frameItemFrag)).
			WithArgs(int64(5), int64(1)).
			WillReturnError(sql.ErrNoRows)
		res := doMP(t, app, "PATCH", "/api/frames/5", body, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("reload failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		prefix(t, mock)
		// No reload expectation → sqlmock rejects the call after commit.
		res := doMP(t, app, "PATCH", "/api/frames/5", body, nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// deleteFrame: one Tx (links + row), file swept share-checked, then publish
// ---------------------------------------------------------------------------

func TestFramesDeleteSweepsRowLinksAndFile(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	oldFull := frameSeedImage(t, s, frameRelDel)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(frameItemFrag)).
		WithArgs(int64(7), int64(1)).
		WillReturnRows(frameItemRows(7, frameRelDel, "Lama"))
	mock.ExpectBegin()
	mock.ExpectExec(q("DELETE FROM frame_programs WHERE frame_id = ? AND school_id = ?")).
		WithArgs(int64(7), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("DELETE FROM frames WHERE id = ? AND school_id = ?")).
		WithArgs(int64(7), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(q("SELECT COUNT(*) FROM session_topics WHERE badge_image_snap = ?")).
		WithArgs(frameRelDel).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	res := doReq(t, app, "DELETE", "/api/frames/7", "", hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])
	require.NoFileExists(t, oldFull, "unshared frame image must be swept after commit")

	f := takeFrame(t, spy)
	require.Equal(t, "frame.changed", f.Entity)
	require.Equal(t, int64(7), f.EntityID)
	require.Equal(t, "deleted", f.Action)
	require.Equal(t, int64(1), f.SchoolID)
}

func TestFramesDeleteEmptyImagePathSkipsFileCheck(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := frameSpy(s, 1)
	defer s.unsubscribe(spy)

	expectAdminAuth(mock)
	mock.ExpectQuery(q(frameItemFrag)).
		WithArgs(int64(8), int64(1)).
		WillReturnRows(frameItemRows(8, "", "TanpaGambar"))
	mock.ExpectBegin()
	mock.ExpectExec(q("DELETE FROM frame_programs WHERE frame_id = ? AND school_id = ?")).
		WithArgs(int64(8), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("DELETE FROM frames WHERE id = ? AND school_id = ?")).
		WithArgs(int64(8), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// No share-check expectation: rel == "" returns before any query.

	res := doReq(t, app, "DELETE", "/api/frames/8", "", hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])
	require.Equal(t, "deleted", takeFrame(t, spy).Action)
}

func TestFramesDeleteUnknownAndMalformedID(t *testing.T) {
	t.Run("unknown id is NOT_FOUND with NO Tx", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		mock.ExpectQuery(q(frameItemFrag)).
			WithArgs(int64(404), int64(1)).
			WillReturnError(sql.ErrNoRows)
		// No Begin: a stray transaction would fail the mock.
		res := doReq(t, app, "DELETE", "/api/frames/404", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("malformed id is NOT_FOUND without any lookup", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock)
		res := doReq(t, app, "DELETE", "/api/frames/abc", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("load failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		expectAdminAuth(mock) // no item expectation → sqlmock rejects the call
		res := doReq(t, app, "DELETE", "/api/frames/7", "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

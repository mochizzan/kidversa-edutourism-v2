package internal

// photos_extra_test.go — coverage for photos.go: handlePhotoList,
// handlePhotoUpload, handlePhotoPatch, handlePhotoDelete, mimeForExt and
// publishPhotoChanged (via the hub seam). Mirrors photos_test.go conventions:
// sqlmock in exact call order, multipart `file` field, envelope assertions.

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Routes and headers
// ---------------------------------------------------------------------------

const (
	PhxPhotosPath   = "/api/sessions/2/topics/5/children/30/photos"
	PhxPhotoPath77  = PhxPhotosPath + "/77"
	PhxPhotoPathBad = PhxPhotosPath + "/abc"
	PhxUploadSidBad = "/api/sessions/x/topics/5/children/30/photos"
	PhxUploadStidBd = "/api/sessions/2/topics/x/children/30/photos"
	PhxUploadCidBad = "/api/sessions/2/topics/5/children/x/photos"
	PhxIdemKey      = "photo-upload-1"
)

// PhxAuthAdmin/PhxAuthFAS/PhxAuthSA register the authed() user load.
func PhxAuthAdmin(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
}

func PhxAuthFAS(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
}

func PhxAuthSA(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
}

// ---------------------------------------------------------------------------
// P-prefix gate expectations (sid=2, stid=5, cid=30, school=1)
// ---------------------------------------------------------------------------

func PhxExpectMembership(mock sqlmock.Sqlmock, member bool) {
	v := 0
	if member {
		v = 1
	}
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(v))
}

func PhxExpectScope(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(q("FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
}

func PhxExpectScopeSA(mock sqlmock.Sqlmock) { // SA without school → unscoped probe
	mock.ExpectQuery(q("FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
}

func PhxExpectScopeFail(mock sqlmock.Sqlmock, err error) {
	mock.ExpectQuery(q("FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnError(err)
}

// PhxExpectConsent wires the consent_links read; err != nil simulates the
// statement failure (sql.ErrNoRows → "" = never sent → locked).
func PhxExpectConsent(mock sqlmock.Sqlmock, status string, err error) {
	e := mock.ExpectQuery(q("SELECT status FROM consent_links WHERE session_id = ? AND child_id = ?")).
		WithArgs(int64(2), int64(30))
	if err != nil {
		e.WillReturnError(err)
		return
	}
	e.WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(status))
}

// PhxExpectFASOwner wires the FAS ownership probe (3 = own group, 99 = foreign).
func PhxExpectFASOwner(mock sqlmock.Sqlmock, facID any) {
	mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members sm")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(facID))
}

// ---------------------------------------------------------------------------
// Idempotency expectations for the upload route
// ---------------------------------------------------------------------------

func PhxExpectIdemClaim(mock sqlmock.Sqlmock) {
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func PhxExpectIdemRecord(mock sqlmock.Sqlmock, key string) {
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), photoUploadRoute, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func PhxExpectIdemRelease(mock sqlmock.Sqlmock) {
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// PhxUploadPreTx wires auth + membership + topic scope + active child +
// SETUJU consent + the idempotency claim for the standard upload route.
func PhxUploadPreTx(mock sqlmock.Sqlmock) {
	PhxAuthAdmin(mock)
	PhxExpectMembership(mock, true)
	PhxExpectScope(mock)
	PhxExpectChild(mock, true)
	PhxExpectConsent(mock, "SETUJU", nil)
	PhxExpectIdemClaim(mock)
}

func PhxExpectChild(mock sqlmock.Sqlmock, active bool) {
	mock.ExpectQuery(q("SELECT is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(active))
}

// ---------------------------------------------------------------------------
// Multipart helpers (generalize the photos_test.go uploadPhoto helper)
// ---------------------------------------------------------------------------

// PhxMPBody builds a multipart body: files (any field name) plus text fields.
func PhxMPBody(t *testing.T, files map[string]string, text map[string]string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for field, filename := range files {
		mpWriteFile(t, w, field, filename, content)
	}
	for k, v := range text {
		require.NoError(t, w.WriteField(k, v))
	}
	require.NoError(t, w.Close())
	return &buf, w.FormDataContentType()
}

// PhxUploadReq POSTs a multipart body to target (the photos collection route).
func PhxUploadReq(t *testing.T, app *fiber.App, target string, h map[string]string,
	files map[string]string, text map[string]string, content []byte) httpRes {
	t.Helper()
	body, ct := PhxMPBody(t, files, text, content)
	req := httptest.NewRequest("POST", target, body)
	req.Header.Set("Content-Type", ct)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	require.NoError(t, err)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return httpRes{status: resp.StatusCode, header: resp.Header, body: string(b)}
}

// PhxPNG / PhxJPG build PNG/JPG payloads of n padding bytes — the multipart
// helper declares their type client-side (image/png / image/jpeg).
func PhxPNG(n int) []byte {
	return append(append([]byte{}, realPNG(8, 8)...), bytes.Repeat([]byte{0}, n)...)
}

func PhxJPG(n int) []byte {
	return append(append([]byte{}, realJPEG(8, 8)...), bytes.Repeat([]byte{0}, n)...)
}

// PhxRequireNoOrphans asserts the per-child upload dir holds no file (every
// failed durable upload must remove the just-written bytes again).
func PhxRequireNoOrphans(t *testing.T, s *Server) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.upload, "1", "2", "5", "30"))
	require.NoError(t, err)
	require.Empty(t, entries, "a failed durable upload must not leave files behind")
}

// ---------------------------------------------------------------------------
// Publish assertions (publishPhotoChanged = photo.changed + gallery.changed
// + report.changed, in that order)
// ---------------------------------------------------------------------------

func PhxAssertPublishes(t *testing.T, spy *subscription, entity string, entityID int64, action string) {
	t.Helper()
	f := takeFrame(t, spy)
	require.Equal(t, entity, f.Entity)
	require.Equal(t, entityID, f.EntityID)
	require.Equal(t, action, f.Action)
	require.Equal(t, int64(1), f.SchoolID)
	require.NotNil(t, f.SessionID)
	require.Equal(t, int64(2), *f.SessionID)
	require.Equal(t, int64(30), f.ChildID)
	require.Equal(t, int64(5), f.Stid)

	g := takeFrame(t, spy)
	require.Equal(t, "gallery.changed", g.Entity)
	require.Equal(t, "updated", g.Action)
	require.Equal(t, int64(1), g.SchoolID)
	require.NotNil(t, g.SessionID)
	require.Equal(t, int64(2), *g.SessionID)
	require.Equal(t, int64(30), g.ChildID)

	r := takeFrame(t, spy)
	require.Equal(t, "report.changed", r.Entity)
	require.Equal(t, "updated", r.Action)
	require.Equal(t, int64(1), r.SchoolID)
	require.Equal(t, int64(5), r.Stid)

	requireNoFrame(t, spy, "publishPhotoChanged emits exactly three frames")
}

// ---------------------------------------------------------------------------
// handlePhotoPatch
// ---------------------------------------------------------------------------

// TestPhxPatchSuccessSetsCandidateAndPublishes: the clear+set Tx runs scoped,
// the response carries the full photo item, and all three refetch signals
// fire (publishPhotoChanged "updated").
func TestPhxPatchSuccessSetsCandidateAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := covSubscribe(t, s, ScopeSnapshot{Role: "SA", SchoolID: 1})

	PhxAuthAdmin(mock)
	PhxExpectMembership(mock, true)
	PhxExpectScope(mock)
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT 1 FROM photos")).
		WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectExec(q("UPDATE photos SET is_candidate = FALSE")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE photos SET is_candidate = TRUE")).
		WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(q("SELECT path, created_at, mime, file_size FROM photos WHERE id = ?")).
		WithArgs(int64(77)).
		WillReturnRows(sqlmock.NewRows([]string{"path", "created_at", "mime", "file_size"}).
			AddRow("1/2/5/30/photo.png", fixedTS, "image/png", int64(999)))
	mock.ExpectCommit()

	res := jsonReq(t, app, "PATCH", PhxPhotoPath77,
		map[string]any{"is_candidate": true}, hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	ph := data["photo"].(map[string]any)
	require.Equal(t, float64(77), ph["id"])
	require.Equal(t, "/uploads/1/2/5/30/photo.png", ph["url"])
	require.Equal(t, true, ph["is_candidate"])
	require.Equal(t, fmtTS(fixedTS), ph["created_at"])
	require.Equal(t, "image/png", ph["mime"])
	require.Equal(t, float64(999), ph["file_size"])

	PhxAssertPublishes(t, spy, "photo.changed", 77, "updated")
}

// TestPhxPatchValidationBeforeSQLOrder: payload/school rejections happen
// BEFORE any gate SQL (only the authed() user load may run).
func TestPhxPatchValidationBeforeSQLOrder(t *testing.T) {
	t.Run("malformed JSON body is VALIDATION without SQL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		res := doReq(t, app, "PATCH", PhxPhotoPath77, `{"is_candidate":`, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("is_candidate false is VALIDATION without SQL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		res := jsonReq(t, app, "PATCH", PhxPhotoPath77,
			map[string]any{"is_candidate": false}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("SA without school header is VALIDATION without SQL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthSA(mock)
		res := jsonReq(t, app, "PATCH", PhxPhotoPath77,
			map[string]any{"is_candidate": true}, hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("missing token is INVALID_TOKEN with zero SQL", func(t *testing.T) {
		s, _ := newTestServer(t)
		app := newTestApp(t, s)
		res := jsonReq(t, app, "PATCH", PhxPhotoPath77,
			map[string]any{"is_candidate": true}, nil)
		requireErrEnvelope(t, res, CodeInvalidToken)
	})

	t.Run("wrong school header is SCHOOL_MISMATCH without handler SQL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		h := bearer(mintAccess(t, s, adminUserRow()))
		h["X-School-ID"] = "2"
		res := jsonReq(t, app, "PATCH", PhxPhotoPath77,
			map[string]any{"is_candidate": true}, h)
		requireErrEnvelope(t, res, CodeSchoolMismatch)
	})
}

// TestPhxPatchParamGuards: malformed ids behave as unknown ids (NOT_FOUND),
// no SQL beyond the authed() user load.
func TestPhxPatchParamGuards(t *testing.T) {
	for _, target := range []string{
		"/api/sessions/x/topics/5/children/30/photos/77",
		"/api/sessions/2/topics/x/children/30/photos/77",
		"/api/sessions/2/topics/5/children/x/photos/77",
		PhxPhotoPathBad,
	} {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		res := jsonReq(t, app, "PATCH", target,
			map[string]any{"is_candidate": true}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	}
}

// TestPhxPatchGateFailures: membership → NOT_FOUND, FAS foreign group →
// NOT_OWNER, topic/session mismatch → NOT_FOUND, scope DB failure →
// DB_UNAVAILABLE — each before the photo Tx starts.
func TestPhxPatchGateFailures(t *testing.T) {
	t.Run("non-member is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, false)
		res := jsonReq(t, app, "PATCH", PhxPhotoPath77,
			map[string]any{"is_candidate": true}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("membership lookup DB failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnError(errors.New("sqlmock: connection done"))
		res := jsonReq(t, app, "PATCH", PhxPhotoPath77,
			map[string]any{"is_candidate": true}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("facilitator touching a foreign group is NOT_OWNER", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthFAS(mock)
		PhxExpectMembership(mock, true)
		PhxExpectFASOwner(mock, 99)
		res := jsonReq(t, app, "PATCH", PhxPhotoPath77,
			map[string]any{"is_candidate": true}, hFAS(t, s))
		requireErrEnvelope(t, res, CodeNotOwner)
	})

	t.Run("topic outside the session is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScopeFail(mock, sql.ErrNoRows)
		res := jsonReq(t, app, "PATCH", PhxPhotoPath77,
			map[string]any{"is_candidate": true}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("scope DB failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScopeFail(mock, errors.New("sqlmock: connection done"))
		res := jsonReq(t, app, "PATCH", PhxPhotoPath77,
			map[string]any{"is_candidate": true}, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestPhxPatchRowAndTxStates: photo missing → NOT_FOUND; every statement
// failure inside the candidate Tx → DB_UNAVAILABLE; the raced UPDATE that
// affects 0 rows → NOT_FOUND. Each failure rolls the Tx back.
func TestPhxPatchRowAndTxStates(t *testing.T) {
	patch := func(t *testing.T, app *fiber.App, h map[string]string) httpRes {
		return jsonReq(t, app, "PATCH", PhxPhotoPath77,
			map[string]any{"is_candidate": true}, h)
	}

	t.Run("BEGIN failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectBegin().WillReturnError(errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, patch(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("photo probe NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT 1 FROM photos")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		requireErrEnvelope(t, patch(t, app, hAdmin(t, s)), CodeNotFound)
	})

	t.Run("photo probe DB failure", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT 1 FROM photos")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnError(errors.New("sqlmock: connection done"))
		mock.ExpectRollback()
		requireErrEnvelope(t, patch(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("clear-candidate UPDATE failure", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT 1 FROM photos")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
		mock.ExpectExec(q("UPDATE photos SET is_candidate = FALSE")).
			WithArgs(int64(2), int64(5), int64(30)).
			WillReturnError(errors.New("sqlmock: connection done"))
		mock.ExpectRollback()
		requireErrEnvelope(t, patch(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("set-candidate UPDATE failure", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT 1 FROM photos")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
		mock.ExpectExec(q("UPDATE photos SET is_candidate = FALSE")).
			WithArgs(int64(2), int64(5), int64(30)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q("UPDATE photos SET is_candidate = TRUE")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnError(errors.New("sqlmock: connection done"))
		mock.ExpectRollback()
		requireErrEnvelope(t, patch(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("raced UPDATE affecting zero rows is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT 1 FROM photos")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
		mock.ExpectExec(q("UPDATE photos SET is_candidate = FALSE")).
			WithArgs(int64(2), int64(5), int64(30)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q("UPDATE photos SET is_candidate = TRUE")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 0)) // row vanished between probe and UPDATE
		mock.ExpectRollback()
		requireErrEnvelope(t, patch(t, app, hAdmin(t, s)), CodeNotFound)
	})

	t.Run("final SELECT failure", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT 1 FROM photos")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
		mock.ExpectExec(q("UPDATE photos SET is_candidate = FALSE")).
			WithArgs(int64(2), int64(5), int64(30)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q("UPDATE photos SET is_candidate = TRUE")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectQuery(q("SELECT path, created_at, mime, file_size FROM photos WHERE id = ?")).
			WithArgs(int64(77)).
			WillReturnError(errors.New("sqlmock: connection done"))
		mock.ExpectRollback()
		requireErrEnvelope(t, patch(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("commit failure", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT 1 FROM photos")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
		mock.ExpectExec(q("UPDATE photos SET is_candidate = FALSE")).
			WithArgs(int64(2), int64(5), int64(30)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(q("UPDATE photos SET is_candidate = TRUE")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectQuery(q("SELECT path, created_at, mime, file_size FROM photos WHERE id = ?")).
			WithArgs(int64(77)).
			WillReturnRows(sqlmock.NewRows([]string{"path", "created_at", "mime", "file_size"}).
				AddRow("1/2/5/30/photo.png", fixedTS, "image/png", int64(999)))
		mock.ExpectCommit().WillReturnError(errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, patch(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// handlePhotoUpload
// ---------------------------------------------------------------------------

// TestPhxUploadSuccessWithFramePublishes: full happy path with a linked
// frame — durable jpg on disk (declared image/jpeg by the client helper),
// INSERT with frame_id and the wire mime, the recorded idempotency response,
// the photo item and all three refetch signals ("created").
func TestPhxUploadSuccessWithFramePublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := covSubscribe(t, s, ScopeSnapshot{Role: "SA", SchoolID: 1})

	PhxAuthAdmin(mock)
	PhxExpectMembership(mock, true)
	PhxExpectScope(mock)
	PhxExpectChild(mock, true)
	PhxExpectConsent(mock, "SETUJU", nil)
	// Optional frame: the session's program plus the frame_programs link.
	mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"program_id"}).AddRow(int64(5)))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM frame_programs fp")).
		WithArgs(int64(9), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"linked"}).AddRow(1))
	PhxExpectIdemClaim(mock)

	content := PhxJPG(128)
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT COUNT(*) FROM photos")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectExec(q("INSERT INTO photos")).
		WithArgs(int64(1), int64(2), int64(5), int64(30), sqlmock.AnyArg(),
			int64(9), int64(len(content)), "image/jpeg", int64(2), int64(30)).
		WillReturnResult(sqlmock.NewResult(42, 1))
	mock.ExpectQuery(q("SELECT created_at FROM photos WHERE id = ?")).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(fixedTS))
	mock.ExpectCommit()
	PhxExpectIdemRecord(mock, PhxIdemKey)

	res := PhxUploadReq(t, app, PhxPhotosPath, uploadHeaders(t, s),
		map[string]string{"file": "shot.jpg"},
		map[string]string{"frame_id": "9"}, content)
	data := requireDataEnvelope(t, res).(map[string]any)
	ph := data["photo"].(map[string]any)
	require.Equal(t, float64(42), ph["id"])
	require.Regexp(t, `^/uploads/1/2/5/30/[0-9a-f]{32}\.jpg$`, ph["url"])
	require.Equal(t, false, ph["is_candidate"])
	require.Equal(t, fmtTS(fixedTS), ph["created_at"])
	require.Equal(t, "image/jpeg", ph["mime"], "jpg bytes map to image/jpeg via mimeForExt")
	require.Equal(t, float64(len(content)), ph["file_size"])

	// The file REALLY landed on disk.
	entries, err := os.ReadDir(filepath.Join(s.upload, "1", "2", "5", "30"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Regexp(t, `^[0-9a-f]{32}\.jpg$`, entries[0].Name())

	PhxAssertPublishes(t, spy, "photo.changed", 42, "created")
}

// TestPhxUploadParamGuards: malformed path ids → NOT_FOUND, a missing write
// school → VALIDATION before FormFile, and a missing/invalid `file` or
// `frame_id` field → VALIDATION before any gate SQL.
func TestPhxUploadParamGuards(t *testing.T) {
	t.Run("malformed sid/stid/cid are NOT_FOUND without handler SQL", func(t *testing.T) {
		for _, target := range []string{PhxUploadSidBad, PhxUploadStidBd, PhxUploadCidBad} {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			PhxAuthAdmin(mock)
			res := doReq(t, app, "POST", target, "", hAdmin(t, s))
			requireErrEnvelope(t, res, CodeNotFound)
		}
	})

	t.Run("SA without school header is VALIDATION before FormFile", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthSA(mock)
		res := PhxUploadReq(t, app, PhxPhotosPath, hSABare(t, s),
			map[string]string{"file": "shot.png"}, nil, PhxPNG(64))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("missing file field is VALIDATION before any gate SQL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		res := PhxUploadReq(t, app, PhxPhotosPath, hAdmin(t, s),
			map[string]string{"notfile": "shot.png"}, nil, PhxPNG(64))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("non-numeric frame_id is VALIDATION before any gate SQL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		res := PhxUploadReq(t, app, PhxPhotosPath, hAdmin(t, s),
			map[string]string{"file": "shot.png"},
			map[string]string{"frame_id": "abc"}, PhxPNG(64))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("zero frame_id is VALIDATION before any gate SQL", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		res := PhxUploadReq(t, app, PhxPhotosPath, hAdmin(t, s),
			map[string]string{"file": "shot.png"},
			map[string]string{"frame_id": "0"}, PhxPNG(64))
		requireErrEnvelope(t, res, CodeValidation)
	})
}

// TestPhxUploadGateFailures: membership/ownership/scope/child/consent gates
// answer before the idempotency store or the disk is touched.
func TestPhxUploadGateFailures(t *testing.T) {
	content := PhxPNG(64)
	upload := func(t *testing.T, app *fiber.App, h map[string]string) httpRes {
		return PhxUploadReq(t, app, PhxPhotosPath, h,
			map[string]string{"file": "shot.png"}, nil, content)
	}

	t.Run("non-member is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, false)
		requireErrEnvelope(t, upload(t, app, hAdmin(t, s)), CodeNotFound)
	})

	t.Run("membership lookup DB failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnError(errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, upload(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("facilitator touching a foreign group is NOT_OWNER", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthFAS(mock)
		PhxExpectMembership(mock, true)
		PhxExpectFASOwner(mock, 99)
		requireErrEnvelope(t, upload(t, app, hFAS(t, s)), CodeNotOwner)
	})

	t.Run("topic outside the session is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScopeFail(mock, sql.ErrNoRows)
		requireErrEnvelope(t, upload(t, app, hAdmin(t, s)), CodeNotFound)
	})

	t.Run("scope DB failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScopeFail(mock, errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, upload(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("inactive child is CHILD_INACTIVE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectQuery(q("SELECT is_active FROM children WHERE id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(false))
		requireErrEnvelope(t, upload(t, app, hAdmin(t, s)), CodeChildInactive)
	})

	t.Run("vanished child is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectQuery(q("SELECT is_active FROM children WHERE id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).
			WillReturnError(sql.ErrNoRows)
		requireErrEnvelope(t, upload(t, app, hAdmin(t, s)), CodeNotFound)
	})

	t.Run("child lookup DB failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectQuery(q("SELECT is_active FROM children WHERE id = ? AND school_id = ?")).
			WithArgs(int64(30), int64(1)).
			WillReturnError(errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, upload(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("consent never sent is CONSENT_LOCKED", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		PhxExpectChild(mock, true)
		PhxExpectConsent(mock, "", sql.ErrNoRows) // "" = never sent → locked
		requireErrEnvelope(t, upload(t, app, hAdmin(t, s)), CodeConsentLocked)
	})

	t.Run("consent DB failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		PhxExpectChild(mock, true)
		PhxExpectConsent(mock, "", errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, upload(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})
}

// TestPhxUploadFrameBranches: the optional frame must link the session's
// program; lookup failures and the unlinked frame answer before the
// idempotency store or the disk.
func TestPhxUploadFrameBranches(t *testing.T) {
	content := PhxPNG(64)
	upload := func(t *testing.T, app *fiber.App, h map[string]string) httpRes {
		return PhxUploadReq(t, app, PhxPhotosPath, h,
			map[string]string{"file": "shot.png"},
			map[string]string{"frame_id": "9"}, content)
	}

	preFrame := func(mock sqlmock.Sqlmock) {
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		PhxExpectChild(mock, true)
		PhxExpectConsent(mock, "SETUJU", nil)
	}

	t.Run("unknown session is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		preFrame(mock)
		mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnError(sql.ErrNoRows)
		requireErrEnvelope(t, upload(t, app, hAdmin(t, s)), CodeNotFound)
	})

	t.Run("session lookup DB failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		preFrame(mock)
		mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnError(errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, upload(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("frame not linked to the program is VALIDATION without any write", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		preFrame(mock)
		mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"program_id"}).AddRow(int64(5)))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM frame_programs fp")).
			WithArgs(int64(9), int64(5), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"linked"}).AddRow(0))
		requireErrEnvelope(t, upload(t, app, hAdmin(t, s)), CodeValidation)
		require.NoDirExists(t, filepath.Join(s.upload, "1", "2", "5", "30"),
			"a rejected frame must not write any file")
	})

	t.Run("frame link lookup DB failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		preFrame(mock)
		mock.ExpectQuery(q("SELECT program_id FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"program_id"}).AddRow(int64(5)))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM frame_programs fp")).
			WithArgs(int64(9), int64(5), int64(1)).
			WillReturnError(errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, upload(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})
}

// TestPhxUploadIdempotencyFailures: the X-Idempotency-Key gate answers after
// the domain gates; claim failures are DB_UNAVAILABLE and a duplicate claim
// replays IN_PROGRESS without executing the upload.
func TestPhxUploadIdempotencyFailures(t *testing.T) {
	content := PhxPNG(64)

	t.Run("missing idempotency key is VALIDATION", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		PhxExpectChild(mock, true)
		PhxExpectConsent(mock, "SETUJU", nil)
		h := hAdmin(t, s) // no X-Idempotency-Key
		res := PhxUploadReq(t, app, PhxPhotosPath, h,
			map[string]string{"file": "shot.png"}, nil, content)
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("claim DELETE failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		PhxExpectChild(mock, true)
		PhxExpectConsent(mock, "SETUJU", nil)
		mock.ExpectExec(q("DELETE FROM idempotency_keys")).
			WillReturnError(errors.New("sqlmock: connection done"))
		res := PhxUploadReq(t, app, PhxPhotosPath, uploadHeaders(t, s),
			map[string]string{"file": "shot.png"}, nil, content)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("claim INSERT failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		PhxExpectChild(mock, true)
		PhxExpectConsent(mock, "SETUJU", nil)
		mock.ExpectExec(q("DELETE FROM idempotency_keys")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(q("INSERT INTO idempotency_keys")).
			WillReturnError(errors.New("sqlmock: connection done"))
		res := PhxUploadReq(t, app, PhxPhotosPath, uploadHeaders(t, s),
			map[string]string{"file": "shot.png"}, nil, content)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("duplicate claim replays IN_PROGRESS without executing", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		PhxExpectChild(mock, true)
		PhxExpectConsent(mock, "SETUJU", nil)
		mock.ExpectExec(q("DELETE FROM idempotency_keys")).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(q("INSERT INTO idempotency_keys")).
			WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
		// Replay: NULL response = the first attempt is still running; no Tx
		// or file write may follow (the mock has no further expectations).
		mock.ExpectQuery(q("SELECT response FROM idempotency_keys")).
			WithArgs(int64(1), photoUploadRoute, PhxIdemKey).
			WillReturnRows(sqlmock.NewRows([]string{"response"}).AddRow(nil))
		res := PhxUploadReq(t, app, PhxPhotosPath, uploadHeaders(t, s),
			map[string]string{"file": "shot.png"}, nil, content)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, "IN_PROGRESS", data["status"])
		require.NoDirExists(t, filepath.Join(s.upload, "1", "2", "5", "30"),
			"a replayed key must not write any file")
	})
}

// TestPhxUploadTxCleanupBranches: every durable-file-then-Tx failure removes
// the just-written file again (no orphan) and answers DB_UNAVAILABLE, except
// the raced insert (0 rows) which is a business NOT_FOUND and gets recorded.
func TestPhxUploadTxCleanupBranches(t *testing.T) {
	content := PhxPNG(64)
	upload := func(t *testing.T, s *Server, app *fiber.App) httpRes {
		return PhxUploadReq(t, app, PhxPhotosPath, uploadHeaders(t, s),
			map[string]string{"file": "shot.png"}, nil, content)
	}

	t.Run("BEGIN failure removes the stored file", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxUploadPreTx(mock)
		mock.ExpectBegin().WillReturnError(errors.New("sqlmock: connection done"))
		PhxExpectIdemRelease(mock) // transient failure: claim released, not recorded
		requireErrEnvelope(t, upload(t, s, app), CodeDBUnavailable)
		PhxRequireNoOrphans(t, s)
	})

	t.Run("FOR UPDATE count failure removes the stored file", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxUploadPreTx(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT COUNT(*) FROM photos")).
			WithArgs(int64(2), int64(5), int64(30)).
			WillReturnError(errors.New("sqlmock: connection done"))
		mock.ExpectRollback()
		PhxExpectIdemRelease(mock)
		requireErrEnvelope(t, upload(t, s, app), CodeDBUnavailable)
		PhxRequireNoOrphans(t, s)
	})

	t.Run("INSERT failure removes the stored file", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxUploadPreTx(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT COUNT(*) FROM photos")).
			WithArgs(int64(2), int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
		mock.ExpectExec(q("INSERT INTO photos")).
			WillReturnError(errors.New("sqlmock: connection done"))
		mock.ExpectRollback()
		PhxExpectIdemRelease(mock)
		requireErrEnvelope(t, upload(t, s, app), CodeDBUnavailable)
		PhxRequireNoOrphans(t, s)
	})

	t.Run("raced INSERT affecting zero rows is NOT_FOUND and gets recorded", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxUploadPreTx(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT COUNT(*) FROM photos")).
			WithArgs(int64(2), int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
		mock.ExpectExec(q("INSERT INTO photos")).
			WillReturnResult(sqlmock.NewResult(0, 0)) // child row vanished mid-race
		mock.ExpectRollback()
		PhxExpectIdemRecord(mock, PhxIdemKey) // business failure IS recorded
		requireErrEnvelope(t, upload(t, s, app), CodeNotFound)
		PhxRequireNoOrphans(t, s)
	})

	t.Run("created_at re-read failure removes the stored file", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxUploadPreTx(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT COUNT(*) FROM photos")).
			WithArgs(int64(2), int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
		mock.ExpectExec(q("INSERT INTO photos")).
			WillReturnResult(sqlmock.NewResult(42, 1))
		mock.ExpectQuery(q("SELECT created_at FROM photos WHERE id = ?")).
			WithArgs(int64(42)).
			WillReturnError(errors.New("sqlmock: connection done"))
		mock.ExpectRollback()
		PhxExpectIdemRelease(mock)
		requireErrEnvelope(t, upload(t, s, app), CodeDBUnavailable)
		PhxRequireNoOrphans(t, s)
	})

	t.Run("commit failure removes the stored file", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxUploadPreTx(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT COUNT(*) FROM photos")).
			WithArgs(int64(2), int64(5), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
		mock.ExpectExec(q("INSERT INTO photos")).
			WillReturnResult(sqlmock.NewResult(42, 1))
		mock.ExpectQuery(q("SELECT created_at FROM photos WHERE id = ?")).
			WithArgs(int64(42)).
			WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(fixedTS))
		mock.ExpectCommit().WillReturnError(errors.New("sqlmock: connection done"))
		PhxExpectIdemRelease(mock)
		requireErrEnvelope(t, upload(t, s, app), CodeDBUnavailable)
		PhxRequireNoOrphans(t, s)
	})
}

// TestPhxUploadStoreFailureReturns500Internal: a non-rejection storage error
// (the per-child dir cannot be created) surfaces as the system's only HTTP
// 500 and releases the idempotency claim for a retry.
func TestPhxUploadStoreFailureReturns500Internal(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	// A regular FILE at uploads/1/2 makes MkdirAll of the child dir fail.
	require.NoError(t, os.MkdirAll(filepath.Join(s.upload, "1"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(s.upload, "1", "2"), []byte("block"), 0o644))

	PhxUploadPreTx(mock)
	PhxExpectIdemRelease(mock)

	res := PhxUploadReq(t, app, PhxPhotosPath, uploadHeaders(t, s),
		map[string]string{"file": "shot.png"}, nil, PhxPNG(64))
	require.Equal(t, 500, res.status, "storage failure is the only HTTP 500: %s", res.body)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 1)
	errObj, okk := m["error"].(map[string]any)
	require.Truef(t, okk, "error envelope malformed: %s", res.body)
	require.Equal(t, CodeInternal, errObj["code"])
}

// ---------------------------------------------------------------------------
// handlePhotoDelete
// ---------------------------------------------------------------------------

// TestPhxDeleteSuccessFASOwnerRemovesFileAndPublishes: the owner facilitator
// deletes the row and the file in order (file unlinked after commit) and all
// three refetch signals fire ("deleted").
func TestPhxDeleteSuccessFASOwnerRemovesFileAndPublishes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := covSubscribe(t, s, ScopeSnapshot{Role: "SA", SchoolID: 1})
	seedUploadFile(t, s, "1/2/5/30/photo.png")

	PhxAuthFAS(mock)
	PhxExpectMembership(mock, true)
	PhxExpectFASOwner(mock, 3)
	PhxExpectScope(mock)
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT path FROM photos")).
		WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("1/2/5/30/photo.png"))
	mock.ExpectExec(q("DELETE FROM photos")).
		WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := doReq(t, app, "DELETE", PhxPhotoPath77, "", hFAS(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"])
	require.NoFileExists(t, filepath.Join(s.upload, "1", "2", "5", "30", "photo.png"),
		"the stored file must be unlinked after the commit")

	PhxAssertPublishes(t, spy, "photo.changed", 77, "deleted")
}

// TestPhxDeleteSucceedsWhenFileUnremovable: the row is gone but the file
// cannot be removed (a non-empty directory sits at its path) — deleting never
// fails the operation (the sweep owns the residue).
func TestPhxDeleteSucceedsWhenFileUnremovable(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	spy := covSubscribe(t, s, ScopeSnapshot{Role: "SA", SchoolID: 1})
	// A non-empty directory at the photo path: os.Remove fails on it.
	dirAbs := filepath.Join(s.upload, "1", "2", "5", "30", "dir.png")
	require.NoError(t, os.MkdirAll(dirAbs, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dirAbs, "keep.txt"), []byte("x"), 0o644))

	PhxAuthAdmin(mock)
	PhxExpectMembership(mock, true)
	PhxExpectScope(mock)
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT path FROM photos")).
		WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("1/2/5/30/dir.png"))
	mock.ExpectExec(q("DELETE FROM photos")).
		WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res := doReq(t, app, "DELETE", PhxPhotoPath77, "", hAdmin(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, true, data["deleted"], "an unremovable file never fails the delete")
	require.DirExists(t, dirAbs, "the residue stays for the orphan sweep")

	PhxAssertPublishes(t, spy, "photo.changed", 77, "deleted")
}

// TestPhxDeleteParamGuards: malformed ids → NOT_FOUND; SA without a school
// header → VALIDATION; missing token → INVALID_TOKEN; none may reach SQL
// beyond the authed() user load.
func TestPhxDeleteParamGuards(t *testing.T) {
	for _, target := range []string{
		"/api/sessions/x/topics/5/children/30/photos/77",
		"/api/sessions/2/topics/x/children/30/photos/77",
		"/api/sessions/2/topics/5/children/x/photos/77",
		PhxPhotoPathBad,
	} {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		res := doReq(t, app, "DELETE", target, "", hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	}

	t.Run("SA without school header is VALIDATION", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthSA(mock)
		res := doReq(t, app, "DELETE", PhxPhotoPath77, "", hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})

	t.Run("missing token is INVALID_TOKEN with zero SQL", func(t *testing.T) {
		s, _ := newTestServer(t)
		app := newTestApp(t, s)
		res := doReq(t, app, "DELETE", PhxPhotoPath77, "", nil)
		requireErrEnvelope(t, res, CodeInvalidToken)
	})
}

// TestPhxDeleteGateFailures: membership/ownership/scope gates answer before
// the delete Tx starts.
func TestPhxDeleteGateFailures(t *testing.T) {
	t.Run("non-member is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, false)
		requireErrEnvelope(t, doReq(t, app, "DELETE", PhxPhotoPath77, "", hAdmin(t, s)), CodeNotFound)
	})

	t.Run("membership lookup DB failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnError(errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, doReq(t, app, "DELETE", PhxPhotoPath77, "", hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("facilitator touching a foreign group is NOT_OWNER", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthFAS(mock)
		PhxExpectMembership(mock, true)
		PhxExpectFASOwner(mock, 99)
		requireErrEnvelope(t, doReq(t, app, "DELETE", PhxPhotoPath77, "", hFAS(t, s)), CodeNotOwner)
	})

	t.Run("topic outside the session is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScopeFail(mock, sql.ErrNoRows)
		requireErrEnvelope(t, doReq(t, app, "DELETE", PhxPhotoPath77, "", hAdmin(t, s)), CodeNotFound)
	})

	t.Run("scope DB failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScopeFail(mock, errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, doReq(t, app, "DELETE", PhxPhotoPath77, "", hAdmin(t, s)), CodeDBUnavailable)
	})
}

// TestPhxDeleteRowAndTxStates: unknown photo → NOT_FOUND; statement failures
// inside the Tx → DB_UNAVAILABLE; the file is never touched before the commit.
func TestPhxDeleteRowAndTxStates(t *testing.T) {
	del := func(t *testing.T, app *fiber.App, h map[string]string) httpRes {
		return doReq(t, app, "DELETE", PhxPhotoPath77, "", h)
	}

	t.Run("BEGIN failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectBegin().WillReturnError(errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, del(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("photo probe NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT path FROM photos")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		requireErrEnvelope(t, del(t, app, hAdmin(t, s)), CodeNotFound)
	})

	t.Run("photo probe DB failure", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT path FROM photos")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnError(errors.New("sqlmock: connection done"))
		mock.ExpectRollback()
		requireErrEnvelope(t, del(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("DELETE row failure", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT path FROM photos")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("1/2/5/30/photo.png"))
		mock.ExpectExec(q("DELETE FROM photos")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnError(errors.New("sqlmock: connection done"))
		mock.ExpectRollback()
		requireErrEnvelope(t, del(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("commit failure", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT path FROM photos")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("1/2/5/30/photo.png"))
		mock.ExpectExec(q("DELETE FROM photos")).
			WithArgs(int64(77), int64(2), int64(5), int64(30), int64(1)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit().WillReturnError(errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, del(t, app, hAdmin(t, s)), CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// handlePhotoList
// ---------------------------------------------------------------------------

// TestPhxListSuccessMarksMissingFiles: durable rows only, /uploads/ URLs,
// RFC3339 created_at, and the E29 item-level missing flag for the row whose
// bytes are gone.
func TestPhxListSuccessMarksMissingFiles(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	seedUploadFile(t, s, "1/2/5/30/a.png") // row 10's bytes exist

	PhxAuthAdmin(mock)
	PhxExpectMembership(mock, true)
	PhxExpectScope(mock)
	PhxExpectConsent(mock, "SETUJU", nil)
	mock.ExpectQuery(q("SELECT id, path, is_candidate, created_at, mime, file_size")).
		WithArgs(int64(2), int64(5), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "path", "is_candidate", "created_at", "mime", "file_size"}).
			AddRow(10, "1/2/5/30/a.png", true, fixedTS, "image/png", int64(55)).
			AddRow(11, "1/2/5/30/gone.png", false, fixedTS, "image/jpeg", int64(66)))

	res := doReq(t, app, "GET", PhxPhotosPath, "", hAdmin(t, s))
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 2)

	m0 := data[0].(map[string]any)
	require.Equal(t, float64(10), m0["id"])
	require.Equal(t, "/uploads/1/2/5/30/a.png", m0["url"])
	require.Equal(t, true, m0["is_candidate"])
	require.Equal(t, fmtTS(fixedTS), m0["created_at"])
	require.Equal(t, "image/png", m0["mime"])
	require.Equal(t, float64(55), m0["file_size"])
	_, hasMissing := m0["missing"]
	require.False(t, hasMissing, "a present file never carries the missing flag")

	m1 := data[1].(map[string]any)
	require.Equal(t, float64(11), m1["id"])
	require.Equal(t, "/uploads/1/2/5/30/gone.png", m1["url"])
	require.Equal(t, true, m1["missing"], "durable row whose bytes vanished is flagged item-level (E29)")
}

// TestPhxListFASOwnerEmptyList: the owner facilitator passes the gates and an
// empty durable set is [] (never null).
func TestPhxListFASOwnerEmptyList(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	PhxAuthFAS(mock)
	PhxExpectMembership(mock, true)
	PhxExpectFASOwner(mock, 3)
	PhxExpectScope(mock)
	PhxExpectConsent(mock, "SETUJU", nil)
	mock.ExpectQuery(q("SELECT id, path, is_candidate, created_at, mime, file_size")).
		WithArgs(int64(2), int64(5), int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "path", "is_candidate", "created_at", "mime", "file_size"}))

	res := doReq(t, app, "GET", PhxPhotosPath, "", hFAS(t, s))
	data := requireDataEnvelope(t, res).([]any)
	require.Empty(t, data, "an empty gallery is an empty array, not null")
}

// TestPhxListSABareUnscoped: SA without a school header reads unscoped (the
// SQL carries no school predicate and no school arg).
func TestPhxListSABareUnscoped(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	PhxAuthSA(mock)
	PhxExpectMembership(mock, true)
	PhxExpectScopeSA(mock)
	PhxExpectConsent(mock, "SETUJU", nil)
	mock.ExpectQuery(q("SELECT id, path, is_candidate, created_at, mime, file_size")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "path", "is_candidate", "created_at", "mime", "file_size"}).
			AddRow(12, "1/2/5/30/c.webp", false, fixedTS, "image/webp", int64(77)))

	res := doReq(t, app, "GET", PhxPhotosPath, "", hSABare(t, s))
	data := requireDataEnvelope(t, res).([]any)
	require.Len(t, data, 1)
	m0 := data[0].(map[string]any)
	require.Equal(t, "/uploads/1/2/5/30/c.webp", m0["url"])
}

// TestPhxListConsentGuards: the list is consent-gated before the photo query
// (F-10: no view without SETUJU) — a never-sent consent counts as locked.
func TestPhxListConsentGuards(t *testing.T) {
	t.Run("pending consent is CONSENT_LOCKED", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		PhxExpectConsent(mock, "PENDING", nil)
		requireErrEnvelope(t, doReq(t, app, "GET", PhxPhotosPath, "", hAdmin(t, s)), CodeConsentLocked)
	})

	t.Run("never-sent consent is CONSENT_LOCKED", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		PhxExpectConsent(mock, "", sql.ErrNoRows)
		requireErrEnvelope(t, doReq(t, app, "GET", PhxPhotosPath, "", hAdmin(t, s)), CodeConsentLocked)
	})

	t.Run("consent DB failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		PhxExpectConsent(mock, "", errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, doReq(t, app, "GET", PhxPhotosPath, "", hAdmin(t, s)), CodeDBUnavailable)
	})
}

// TestPhxListGateFailures: membership/ownership/scope gates answer before
// the consent check and the photo query.
func TestPhxListGateFailures(t *testing.T) {
	t.Run("non-member is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, false)
		requireErrEnvelope(t, doReq(t, app, "GET", PhxPhotosPath, "", hAdmin(t, s)), CodeNotFound)
	})

	t.Run("facilitator touching a foreign group is NOT_OWNER", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthFAS(mock)
		PhxExpectMembership(mock, true)
		PhxExpectFASOwner(mock, 99)
		requireErrEnvelope(t, doReq(t, app, "GET", PhxPhotosPath, "", hFAS(t, s)), CodeNotOwner)
	})

	t.Run("topic outside the session is NOT_FOUND", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScopeFail(mock, sql.ErrNoRows)
		requireErrEnvelope(t, doReq(t, app, "GET", PhxPhotosPath, "", hAdmin(t, s)), CodeNotFound)
	})

	t.Run("scope DB failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScopeFail(mock, errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, doReq(t, app, "GET", PhxPhotosPath, "", hAdmin(t, s)), CodeDBUnavailable)
	})
}

// TestPhxListDBBranches: the photo query fails, a row fails to scan, and a
// mid-iteration driver error — all DB_UNAVAILABLE, never a partial gallery.
func TestPhxListDBBranches(t *testing.T) {
	preList := func(mock sqlmock.Sqlmock) {
		PhxAuthAdmin(mock)
		PhxExpectMembership(mock, true)
		PhxExpectScope(mock)
		PhxExpectConsent(mock, "SETUJU", nil)
	}

	t.Run("photo query failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		preList(mock)
		mock.ExpectQuery(q("SELECT id, path, is_candidate, created_at, mime, file_size")).
			WithArgs(int64(2), int64(5), int64(30), int64(1)).
			WillReturnError(errors.New("sqlmock: connection done"))
		requireErrEnvelope(t, doReq(t, app, "GET", PhxPhotosPath, "", hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("row scan failure is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		preList(mock)
		mock.ExpectQuery(q("SELECT id, path, is_candidate, created_at, mime, file_size")).
			WithArgs(int64(2), int64(5), int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "path", "is_candidate", "created_at", "mime", "file_size"}).
				AddRow(10, "1/2/5/30/a.png", false, fixedTS, "image/png", "not-a-number"))
		requireErrEnvelope(t, doReq(t, app, "GET", PhxPhotosPath, "", hAdmin(t, s)), CodeDBUnavailable)
	})

	t.Run("mid-iteration driver error is DB_UNAVAILABLE", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		preList(mock)
		rows := sqlmock.NewRows([]string{"id", "path", "is_candidate", "created_at", "mime", "file_size"}).
			AddRow(10, "1/2/5/30/a.png", false, fixedTS, "image/png", int64(55)).
			AddRow(11, "1/2/5/30/b.png", false, fixedTS, "image/png", int64(55)).
			RowError(1, errors.New("driver: connection broken"))
		mock.ExpectQuery(q("SELECT id, path, is_candidate, created_at, mime, file_size")).
			WithArgs(int64(2), int64(5), int64(30), int64(1)).
			WillReturnRows(rows)
		requireErrEnvelope(t, doReq(t, app, "GET", PhxPhotosPath, "", hAdmin(t, s)), CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// mimeForExt (exercised end-to-end by the jpg upload; the table pins the
// full mapping including the fall-through)
// ---------------------------------------------------------------------------

func TestPhxMimeForExtMapping(t *testing.T) {
	require.Equal(t, "image/jpeg", mimeForExt("jpg"))
	require.Equal(t, "image/png", mimeForExt("png"))
	require.Equal(t, "image/webp", mimeForExt("webp"))
	require.Equal(t, "", mimeForExt("gif"), "non-photo extensions carry no wire mime")
	require.Equal(t, "", mimeForExt(""))
}

package internal

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// multipartUpload builds a real multipart body with the binding `file` field.
func multipartUpload(t *testing.T, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	mpWriteFile(t, w, "file", filename, content)
	require.NoError(t, w.Close())
	return &buf, w.FormDataContentType()
}

// uploadPhoto POSTs P/photos with a real multipart body (field `file`).
func uploadPhoto(t *testing.T, app *fiber.App, h map[string]string, filename string, content []byte) httpRes {
	t.Helper()
	body, ct := multipartUpload(t, filename, content)
	req := httptest.NewRequest("POST", "/api/sessions/2/topics/5/children/30/photos", body)
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

// uploadGates wires the pre-idempotency gates: auth, membership, topic scope,
// active child and SETUJU consent.
func uploadGates(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(q("SELECT status FROM consent_links WHERE session_id = ? AND child_id = ?")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("SETUJU"))
}

func uploadHeaders(t *testing.T, s *Server) map[string]string {
	t.Helper()
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	h["X-Idempotency-Key"] = "photo-upload-1"
	return h
}

const photoUploadRoute = "POST /api/sessions/2/topics/5/children/30/photos"

// TestPhotoUploadLimitRejectsEleventhAndCleansFile: the FOR UPDATE count cap
// (≤10) rejects the upload AFTER the durable file was written — and the
// rejected file is removed again (no orphan).
func TestPhotoUploadLimitRejectsEleventhAndCleansFile(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := uploadHeaders(t, s)

	uploadGates(t, mock)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT COUNT(*) FROM photos")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(10))
	mock.ExpectRollback()
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), photoUploadRoute, "photo-upload-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	content := append(realPNG(8, 8), bytes.Repeat([]byte{0}, 64)...)
	res := uploadPhoto(t, app, h, "shot.png", content)
	requireErrEnvelope(t, res, CodePhotoLimit)

	// The durable file was removed again: the per-child upload dir is empty.
	dir := filepath.Join(s.upload, "1", "2", "5", "30")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "rejected upload must not leave files behind")
}

// TestPhotoUploadRejectsNonWhitelistedDeclaredType: Pembaruan 2026-11-10
// (spec 09) — upload validation is enforced server-side via decode-config +
// declared-type cross-check. ICO bytes (declared image/x-icon) are rejected
// with failStore → 200 VALIDATION before any durable write, idempotency
// record or row.
func TestPhotoUploadRejectsNonWhitelistedDeclaredType(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := uploadHeaders(t, s)

	uploadGates(t, mock)
	// The upload fn runs inside s.idempotent: claim (cleanup + insert) and the
	// rejection-response record both happen even though the fn itself fails.
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), photoUploadRoute, "photo-upload-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := uploadPhoto(t, app, h, "fake.ico", icoHead)
	requireErrEnvelope(t, res, CodeValidation)

	// Nothing landed on disk: no orphan from a rejected upload.
	dir := filepath.Join(s.upload, "1", "2", "5", "30")
	if _, statErr := os.Stat(dir); statErr == nil {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Empty(t, entries, "a rejected upload must not store files")
	}
}

// TestPhotoUploadConsentLocked: without a SETUJU consent the upload never
// reaches the idempotency store or the disk.
func TestPhotoUploadConsentLocked(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := uploadHeaders(t, s)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(q("SELECT status FROM consent_links WHERE session_id = ? AND child_id = ?")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("PENDING"))

	content := append(realPNG(8, 8), bytes.Repeat([]byte{0}, 64)...)
	res := uploadPhoto(t, app, h, "shot.png", content)
	requireErrEnvelope(t, res, CodeConsentLocked)
}

// TestPhotoUploadSiblingCommitFillsCapBlocksNext: the cap check runs inside
// the upload's own Tx — a sibling upload committing in the race window fills
// the tenth slot, so the next open Tx counts 10 and refuses with
// PHOTO_LIMIT (never an 11th row) and its file is removed again. This is the
// sqlmock form of the two-concurrent-uploads race: the first Tx sees 9 and
// lands, the second sees the committed 10.
func TestPhotoUploadSiblingCommitFillsCapBlocksNext(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := uploadHeaders(t, s)

	content := append(realPNG(8, 8), bytes.Repeat([]byte{0}, 64)...)

	// 1. The sibling lands first: in-Tx count 9 → INSERT → commit (fills the
	// cap to ten).
	uploadGates(t, mock)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT COUNT(*) FROM photos")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(9))
	mock.ExpectExec(q("INSERT INTO photos")).
		WithArgs(int64(1), int64(2), int64(5), int64(30), sqlmock.AnyArg(),
			sqlmock.AnyArg(), int64(len(content)), "image/png", int64(2), int64(30)).
		WillReturnResult(sqlmock.NewResult(41, 1))
	mock.ExpectQuery(q("SELECT created_at FROM photos WHERE id = ?")).
		WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(fixedTS))
	mock.ExpectCommit()
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), photoUploadRoute, "photo-upload-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := uploadPhoto(t, app, h, "first.png", content)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(41), data["photo"].(map[string]any)["id"],
		"the sibling's upload lands and commits")

	// 2. The next upload's open Tx now counts the committed row (10) →
	// PHOTO_LIMIT + rollback; no INSERT is even queued, so an attempted write
	// would fail this test.
	h2 := uploadHeaders(t, s)
	h2["X-Idempotency-Key"] = "photo-upload-2"
	uploadGates(t, mock)
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT COUNT(*) FROM photos")).
		WithArgs(int64(2), int64(5), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(10))
	mock.ExpectRollback()
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), photoUploadRoute, "photo-upload-2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res = uploadPhoto(t, app, h2, "second.png", content)
	requireErrEnvelope(t, res, CodePhotoLimit)

	// Only the committed sibling's file remains; the refused one is cleaned.
	entries, err := os.ReadDir(filepath.Join(s.upload, "1", "2", "5", "30"))
	require.NoError(t, err)
	require.Len(t, entries, 1,
		"the committed upload's file survives; the refused sibling leaves no orphan")
}

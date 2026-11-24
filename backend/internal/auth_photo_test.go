package internal

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Profile photos (spec 09): POST /api/auth/profile-photo (multipart, one
// X-Idempotency-Key per convention) + DELETE /api/auth/profile-photo. Both
// answer {data:{user}} in the PATCH-profile pattern; storage is user-keyed
// avatars/<user_id>/ (covers SA's school-null case); the old photo file is
// unlinked only AFTER the row update succeeds (frames/badges pattern).
// ---------------------------------------------------------------------------

const (
	profilePhotoPostRoute = "POST /api/auth/profile-photo"
	profilePhotoDelRoute  = "DELETE /api/auth/profile-photo"
)

// profilePhotoUserCols mirrors userRowCols with photo_path appended last —
// the column order userSelect pins for the auth-shaped rows.
var profilePhotoUserCols = []string{
	"id", "name", "email", "wa_number", "password_hash", "role",
	"decision", "is_active", "school_id", "t_active", "created_at", "photo_path",
}

// sqlUserWithPhotoRow builds one auth row with an explicit photo_path (nil /
// string). school/tActive are nil for SA.
func sqlUserWithPhotoRow(id int64, name, email, wa, hash, role, decision string,
	active bool, school, tActive, photo any) *sqlmock.Rows {
	return sqlmock.NewRows(profilePhotoUserCols).
		AddRow(id, name, email, wa, hash, role, decision, active, school, tActive, fixedTS, photo)
}

func sqlAdminWithPhoto(photo any) *sqlmock.Rows {
	return sqlUserWithPhotoRow(2, "Admin", "admin@school.id", "", testPasswordHash,
		"ADMIN", "APPROVED", true, int64(1), true, photo)
}

// profilePhotoHeaders mints an ADMIN bearer + school + (optional) idem key.
func profilePhotoHeaders(t *testing.T, s *Server, key string) map[string]string {
	t.Helper()
	h := bearer(mintAccess(t, s, adminUserRow()))
	h["X-School-ID"] = "1"
	if key != "" {
		h["X-Idempotency-Key"] = key
	}
	return h
}

// postProfilePhoto sends the multipart POST and returns the wire result.
func postProfilePhoto(t *testing.T, app *fiber.App, h map[string]string, filename string, content []byte) httpRes {
	t.Helper()
	body, ct := multipartUpload(t, filename, content)
	req := httptest.NewRequest("POST", "/api/auth/profile-photo", body)
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

func deleteProfilePhoto(t *testing.T, app *fiber.App, h map[string]string) httpRes {
	t.Helper()
	return doReq(t, app, "DELETE", "/api/auth/profile-photo", "", h)
}

// expectPhotoIdemClaim wires the idempotency claim (24 h cleanup + insert).
func expectPhotoIdemClaim(mock sqlmock.Sqlmock) {
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// expectPhotoIdemRecord wires the recorded response after fn's own writes.
func expectPhotoIdemRecord(mock sqlmock.Sqlmock, school int64, key string) {
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), school, profilePhotoPostRoute, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// TestProfilePhotoPostStoresAvatarAndRespondsUser: the upload lands under the
// USER-keyed avatars/<user_id>/ dir (no school in the path), the row points at
// it and {data:{user}} carries photo_url recomputed from the row.
func TestProfilePhotoPostStoresAvatarAndRespondsUser(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := profilePhotoHeaders(t, s, "pp-1")

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	expectPhotoIdemClaim(mock)
	mock.ExpectExec(q("UPDATE users SET photo_path = ?")).
		WithArgs(sqlmock.AnyArg(), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectPhotoIdemRecord(mock, 1, "pp-1")

	res := postProfilePhoto(t, app, h, "me.png", realPNG(8, 8))
	data, ok := requireDataEnvelope(t, res).(map[string]any)
	require.True(t, ok, "photo POST must respond {data:{user}}")
	user, ok := data["user"].(map[string]any)
	require.True(t, ok)

	photoURL, _ := user["photo_url"].(string)
	re := regexp.MustCompile(`^/uploads/avatars/2/[0-9a-f]{32}\.png$`)
	require.Truef(t, re.MatchString(photoURL),
		"photo_url must be /uploads/avatars/2/<32hex>.png (user-keyed), got %q", photoURL)

	// The file exists on disk at the DB-reported path.
	rel := strings.TrimPrefix(photoURL, "/uploads/")
	stored, err := os.ReadFile(filepath.Join(s.upload, filepath.FromSlash(rel)))
	require.NoError(t, err, "the stored avatar file must exist")
	require.Equal(t, realPNG(8, 8), stored, "≤10 MB avatars store byte-for-byte")

	require.NoError(t, mock.ExpectationsWereMet())
}

// TestProfilePhotoPostReplacesAndUnlinksOldFile: replacing an existing photo
// unlinks the OLD file AFTER the row update succeeded (frames/badges pattern).
func TestProfilePhotoPostReplacesAndUnlinksOldFile(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := profilePhotoHeaders(t, s, "pp-2")

	oldRel := "avatars/2/" + strings.Repeat("a", 32) + ".png"
	require.NoError(t, os.MkdirAll(filepath.Join(s.upload, "avatars", "2"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(s.upload, filepath.FromSlash(oldRel)), []byte("old"), 0o644))

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).
		WillReturnRows(sqlAdminWithPhoto(oldRel))
	expectPhotoIdemClaim(mock)
	mock.ExpectExec(q("UPDATE users SET photo_path = ?")).
		WithArgs(sqlmock.AnyArg(), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectPhotoIdemRecord(mock, 1, "pp-2")

	res := postProfilePhoto(t, app, h, "me.png", realPNG(8, 8))
	data, ok := requireDataEnvelope(t, res).(map[string]any)
	require.True(t, ok)
	user := data["user"].(map[string]any)
	photoURL, _ := user["photo_url"].(string)
	re := regexp.MustCompile(`^/uploads/avatars/2/[0-9a-f]{32}\.png$`)
	require.Truef(t, re.MatchString(photoURL), "new photo_url expected, got %q", photoURL)

	_, err := os.Stat(filepath.Join(s.upload, filepath.FromSlash(oldRel)))
	require.Truef(t, os.IsNotExist(err), "the old avatar file must be unlinked after the row update")

	require.NoError(t, mock.ExpectationsWereMet())
}

// TestProfilePhotoDeleteClearsRowAndUnlinks: DELETE nulls the row, then
// unlinks the old file; the response user no longer carries a photo_url key.
// No idempotency: it is not a mutating POST.
func TestProfilePhotoDeleteClearsRowAndUnlinks(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := profilePhotoHeaders(t, s, "")

	oldRel := "avatars/2/" + strings.Repeat("b", 32) + ".jpg"
	require.NoError(t, os.MkdirAll(filepath.Join(s.upload, "avatars", "2"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(s.upload, filepath.FromSlash(oldRel)), []byte("old"), 0o644))

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).
		WillReturnRows(sqlAdminWithPhoto(oldRel))
	mock.ExpectExec(q("UPDATE users SET photo_path = NULL WHERE id = ?")).
		WithArgs(int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := deleteProfilePhoto(t, app, h)
	data, ok := requireDataEnvelope(t, res).(map[string]any)
	require.True(t, ok)
	user := data["user"].(map[string]any)
	require.NotContains(t, user, "photo_url",
		"after DELETE the user shape must omit photo_url entirely")

	_, err := os.Stat(filepath.Join(s.upload, filepath.FromSlash(oldRel)))
	require.Truef(t, os.IsNotExist(err), "the deleted avatar file must be unlinked")

	require.NoError(t, mock.ExpectationsWereMet())
}

// TestProfilePhotoPostSAWithoutSchoolHeader: SA posts WITHOUT X-School-ID —
// the avatar is still stored user-keyed (avatars/1) and the idempotency scope
// is school 0; no school probe runs.
func TestProfilePhotoPostSAWithoutSchoolHeader(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := bearer(mintAccess(t, s, saUserRow()))
	h["X-Idempotency-Key"] = "pp-sa"

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	expectPhotoIdemClaim(mock)
	mock.ExpectExec(q("UPDATE users SET photo_path = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectPhotoIdemRecord(mock, 0, "pp-sa")

	res := postProfilePhoto(t, app, h, "me.png", realPNG(8, 8))
	data, ok := requireDataEnvelope(t, res).(map[string]any)
	require.True(t, ok)
	user := data["user"].(map[string]any)
	photoURL, _ := user["photo_url"].(string)
	re := regexp.MustCompile(`^/uploads/avatars/1/[0-9a-f]{32}\.png$`)
	require.Truef(t, re.MatchString(photoURL), "SA avatar is user-keyed avatars/1, got %q", photoURL)

	require.NoError(t, mock.ExpectationsWereMet())
}

// TestProfilePhotoPostRejectsWebPDeclaration: the shared whitelist runs on the
// profile path too — image/webp (any bytes) is rejected with 200 VALIDATION
// before any durable write or row update; the idempotency claim still records
// the rejection envelope (fn wrote it, so the claim is consumed).
func TestProfilePhotoPostRejectsWebPDeclaration(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := profilePhotoHeaders(t, s, "pp-webp")

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), profilePhotoPostRoute, "pp-webp").
		WillReturnResult(sqlmock.NewResult(0, 1))

	res := postProfilePhoto(t, app, h, "me.webp",
		append(append([]byte{}, webpHead...), bytes.Repeat([]byte{0}, 32)...))
	requireErrEnvelope(t, res, CodeValidation)

	// Nothing landed under avatars/ (the dir may not even exist — the
	// rejection happens before any write).
	if _, statErr := os.Stat(filepath.Join(s.upload, "avatars", "2")); statErr == nil {
		entries, err := os.ReadDir(filepath.Join(s.upload, "avatars", "2"))
		require.NoError(t, err)
		require.Empty(t, entries, "a rejected avatar upload must not store files")
	}

	require.NoError(t, mock.ExpectationsWereMet())
}

// TestProfilePhotoPostMissingFileIsValidation: no multipart file field →
// 200 VALIDATION (bound in the handler, before idempotency).
func TestProfilePhotoPostMissingFileIsValidation(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := profilePhotoHeaders(t, s, "pp-nofile")

	// authed() runs first: one real user load before the FormFile rejection.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())

	req := httptest.NewRequest("POST", "/api/auth/profile-photo", strings.NewReader("--x--\r\n"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	require.NoError(t, err)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	requireErrEnvelope(t, httpRes{status: resp.StatusCode, header: resp.Header, body: string(b)}, CodeValidation)
}

// TestUserJSONOmitsPhotoURLWhenUnset: pins the omitempty decision — the auth
// user shape without a photo must NOT carry a photo_url key (this keeps the
// existing JSON-pinning tests valid).
func TestUserJSONOmitsPhotoURLWhenUnset(t *testing.T) {
	b, err := json.Marshal(adminUserRow().json())
	require.NoError(t, err)
	require.NotContains(t, string(b), "photo_url")
}

// TestJanitorReferencesUserPhotoPath: the orphan sweep's referenced query
// adds users.photo_path — an avatar whose row was already committed can never
// be swept as a crash-residue orphan.
func TestJanitorReferencesUserPhotoPath(t *testing.T) {
	s, mock := newTestServer(t)
	avatarRel := "avatars/1/" + strings.Repeat("c", 32) + ".png"
	mock.ExpectQuery(q("UNION SELECT photo_path FROM users WHERE photo_path IS NOT NULL")).
		WillReturnRows(sqlmock.NewRows([]string{"path"}).
			AddRow("photos/a.png").
			AddRow(avatarRel))

	refs, err := s.referencedPaths()

	require.NoError(t, err)
	require.True(t, refs["photos/a.png"])
	require.True(t, refs[avatarRel])
	require.NoError(t, mock.ExpectationsWereMet())
}

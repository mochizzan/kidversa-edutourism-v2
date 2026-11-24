package internal

// admin_controls_test.go — Feature A/B coverage (spec
// docs/superpowers/specs/2026-11-11-admin-impersonation-password-reset-design.md):
// the shared (actor → target) predicate, the generation policy, the
// impersonation session swap (ledger columns, cookie safety, refusals) and the
// administrative password reset (hash + revoke + immutable ledger row).

import (
	"context"
	"database/sql/driver"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// hashArg matches a bcrypt hash of Password (the plaintext is never asserted
// in a message, so a failure cannot leak it into test output). got, when set,
// receives the stored hash so a later login can prove it is usable.
type hashArg struct {
	password string
	got      *string
}

func (a hashArg) Match(v driver.Value) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	if a.got != nil {
		*a.got = s
	}
	return bcrypt.CompareHashAndPassword([]byte(s), []byte(a.password)) == nil
}

// adminTargetRows builds one target userSelect row with explicit state.
func adminTargetRows(id int64, name, role string, school int64, tActive bool) *sqlmock.Rows {
	var tid any
	var ta any
	if school != 0 {
		tid, ta = school, tActive
	}
	decision := "APPROVED"
	return sqlUserRow(id, name, name+"@school.id", "", testPasswordHash, role, decision, true, tid, ta)
}

func pendingTargetRows(id int64) *sqlmock.Rows {
	return sqlUserRow(id, "Pending", "pending@school.id", "", testPasswordHash,
		"FASILITATOR", "PENDING", true, int64(1), true)
}

func inactiveTargetRows(id int64) *sqlmock.Rows {
	return sqlUserRow(id, "Off", "off@school.id", "", testPasswordHash,
		"FASILITATOR", "APPROVED", false, int64(1), true)
}

var errBoom = errors.New("boom")

// ---------------------------------------------------------------------------
// canAdminTarget — the single shared predicate (§8.1 + §8.2)
// ---------------------------------------------------------------------------

func TestAdminCanTargetMatrix(t *testing.T) {
	sa := func(id int64) *userRow {
		return &userRow{ID: id, Role: "SA", Decision: "APPROVED", IsActive: true}
	}
	admin := func(id, school int64) *userRow {
		return &userRow{ID: id, Role: "ADMIN", Decision: "APPROVED", IsActive: true,
			SchoolID: school, SchoolActive: true}
	}
	fas := func(id, school int64) *userRow {
		return &userRow{ID: id, Role: "FASILITATOR", Decision: "APPROVED", IsActive: true,
			SchoolID: school, SchoolActive: true}
	}

	cases := []struct {
		name   string
		actor  *userRow
		target *userRow
		code   string
	}{
		{"nil target", admin(2, 1), nil, CodeNotFound},
		{"nil actor", nil, fas(3, 1), CodeForbiddenRole},

		{"ADMIN self", admin(2, 1), admin(2, 1), CodeValidation},
		{"ADMIN -> ADMIN", admin(2, 1), admin(4, 1), CodeForbiddenRole},
		{"ADMIN -> SA", admin(2, 1), sa(1), CodeForbiddenRole},
		{"ADMIN -> foreign FAS", admin(2, 1), fas(3, 2), CodeNotFound},
		{"ADMIN -> own FAS", admin(2, 1), fas(3, 1), ""},
		{"ADMIN -> own FAS pending decision", admin(2, 1),
			&userRow{ID: 3, Role: "FASILITATOR", Decision: "PENDING", IsActive: true,
				SchoolID: 1, SchoolActive: true}, CodeAccountInactive},
		{"ADMIN -> own FAS deactivated", admin(2, 1),
			&userRow{ID: 3, Role: "FASILITATOR", Decision: "APPROVED", IsActive: false,
				SchoolID: 1, SchoolActive: true}, CodeAccountInactive},
		{"ADMIN -> own FAS school off", admin(2, 1),
			&userRow{ID: 3, Role: "FASILITATOR", Decision: "APPROVED", IsActive: true,
				SchoolID: 1, SchoolActive: false}, CodeSchoolInactive},

		{"SA self", sa(1), sa(1), CodeValidation},
		{"SA -> SA", sa(1), sa(5), CodeForbiddenRole},
		{"SA -> ADMIN", sa(1), admin(2, 1), ""},
		{"SA -> FAS", sa(1), fas(3, 1), ""},
		{"SA -> foreign FAS", sa(1), fas(7, 2), ""},
		{"SA -> FAS pending", sa(1),
			&userRow{ID: 8, Role: "FASILITATOR", Decision: "PENDING", IsActive: true,
				SchoolID: 1, SchoolActive: true}, CodeAccountInactive},
		{"SA -> FAS school off", sa(1),
			&userRow{ID: 9, Role: "FASILITATOR", Decision: "APPROVED", IsActive: true,
				SchoolID: 1, SchoolActive: false}, CodeSchoolInactive},

		{"FAS -> SA", fas(3, 1), sa(1), CodeForbiddenRole},
		{"FAS -> ADMIN", fas(3, 1), admin(2, 1), CodeForbiddenRole},
		{"FAS -> FAS", fas(3, 1), fas(4, 1), CodeForbiddenRole},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := canAdminTarget(tc.actor, tc.target)
			require.Equal(t, tc.code, code)
			if tc.code == "" {
				require.Empty(t, msg)
				return
			}
			require.NotEmpty(t, msg)
		})
	}
}

// ---------------------------------------------------------------------------
// generatePassword (§10.1)
// ---------------------------------------------------------------------------

func TestGeneratePasswordPolicy(t *testing.T) {
	const ambiguous = "IO01l"
	seen := map[string]bool{}
	for range 200 {
		pw, err := generatePassword()
		require.NoError(t, err)
		require.Len(t, pw, 16)
		require.True(t, validPassword(pw), "generated password must satisfy the server policy: %q", pw)
		require.False(t, strings.ContainsAny(pw, ambiguous),
			"generated password must avoid ambiguous glyphs: %q", pw)
		for _, r := range pw {
			require.Contains(t, passwordAlphabet, string(r), "unexpected character in %q", pw)
		}
		seen[pw] = true
	}
	require.Greater(t, len(seen), 190, "generation must not be deterministic")
}

// ---------------------------------------------------------------------------
// Impersonation — happy paths (§9)
// ---------------------------------------------------------------------------

func withCookie(h map[string]string, token string) map[string]string {
	out := make(map[string]string, len(h)+1)
	for k, v := range h {
		out[k] = v
	}
	out["Cookie"] = refreshCookie + "=" + token
	return out
}

// expectImpersonateTx queues the ONE Tx: revoke the caller's jti (only when a
// matching cookie is presented) then insert the target row with the ledger.
// root is either an exact string (cookie root) or sqlmock.AnyArg().
func expectImpersonateTx(mock sqlmock.Sqlmock, oldJTI string, actorID, targetID, school int64, root any) {
	mock.ExpectBegin()
	if oldJTI != "" {
		mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW()")).
			WithArgs(oldJTI, actorID).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).
		WithArgs(int64(school), int64(targetID), sqlmock.AnyArg(), sqlmock.AnyArg(),
			actorID, root).
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()
}

func TestAdminImpersonateHappyPaths(t *testing.T) {
	t.Run("SA -> ADMIN carries the target school not the header school", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectSAAuth(mock, 1)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
		cookie := mintRefresh(t, s, saUserRow(), "jti-sa", time.Now().Add(time.Hour))
		expectImpersonateTx(mock, "jti-sa", 1, 2, 1, "jti-sa")
		mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(1)).WillReturnRows(sqlSchoolBandung())

		res := jsonReq(t, app, "POST", "/api/users/2/impersonate", nil, withCookie(hSA(t, s), cookie))
		data := requireDataEnvelope(t, res).(map[string]any)

		require.Equal(t, map[string]any{
			"actor_id": float64(1), "actor_name": "Super", "actor_role": "SA",
		}, data["impersonation"])
		user := data["user"].(map[string]any)
		require.Equal(t, float64(2), user["id"])
		require.Equal(t, "ADMIN", user["role"])
		school := data["school"].(map[string]any)
		require.Equal(t, "Kidversa", school["school_name"])

		claims, err := s.parseAccess(data["access"].(string))
		require.NoError(t, err)
		require.Equal(t, "2", claims.Subject)
		require.Equal(t, "ADMIN", claims.Role)
		require.Contains(t, res.header.Get("Set-Cookie"), refreshCookie+"=",
			"the cookie must become the target's session")
	})

	t.Run("SA -> FAS cross school", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectSAAuth(mock, 1)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(7)).
			WillReturnRows(adminTargetRows(7, "Siti", "FASILITATOR", 2, true))
		cookie := mintRefresh(t, s, saUserRow(), "jti-root", time.Now().Add(time.Hour))
		expectImpersonateTx(mock, "jti-root", 1, 7, 2, "jti-root")
		mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(2)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
				AddRow(2, "Surabaya", "Indonesia", nil, true, fixedTS))

		res := jsonReq(t, app, "POST", "/api/users/7/impersonate", nil, withCookie(hSA(t, s), cookie))
		data := requireDataEnvelope(t, res).(map[string]any)

		require.Equal(t, float64(2), data["school"].(map[string]any)["id"],
			"an SA switch must answer with the TARGET's school (spec §9.2)")
		claims, err := s.parseAccess(data["access"].(string))
		require.NoError(t, err)
		require.Equal(t, int64(2), claims.School)
		require.Equal(t, "FASILITATOR", claims.Role)
	})

	t.Run("ADMIN -> own school FASILITATOR", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectAdminAuth(mock)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
		expectImpersonateTx(mock, "", 2, 3, 1, sqlmock.AnyArg())
		mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(1)).WillReturnRows(sqlSchoolBandung())

		res := jsonReq(t, app, "POST", "/api/users/3/impersonate", nil, hAdmin(t, s))
		data := requireDataEnvelope(t, res).(map[string]any)

		require.Equal(t, map[string]any{
			"actor_id": float64(2), "actor_name": "Admin", "actor_role": "ADMIN",
		}, data["impersonation"])
		user := data["user"].(map[string]any)
		require.Equal(t, float64(3), user["id"])
		require.Equal(t, "FASILITATOR", user["role"])
		require.Contains(t, res.header.Get("Set-Cookie"), refreshCookie+"=")
	})
}

// ---------------------------------------------------------------------------
// Impersonation — cookie safety (§9.5)
// ---------------------------------------------------------------------------

func TestAdminImpersonateCookieSafety(t *testing.T) {
	t.Run("no cookie: random root, nothing revoked", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectAdminAuth(mock)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
		// Root = random 32-hex generated at switch time.
		mock.ExpectBegin()
		mock.ExpectExec(q("INSERT INTO refresh_tokens")).
			WithArgs(int64(1), int64(3), sqlmock.AnyArg(), sqlmock.AnyArg(),
				int64(2), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(2, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(1)).WillReturnRows(sqlSchoolBandung())

		res := jsonReq(t, app, "POST", "/api/users/3/impersonate", nil, hAdmin(t, s))
		requireDataEnvelope(t, res)
	})

	t.Run("foreign cookie is never revoked", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectAdminAuth(mock)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
		// Subject 3 != actor 2: the handler must NOT revoke it or reuse its jti.
		foreign := mintRefresh(t, s, fasUserRow(), "jti-foreign", time.Now().Add(time.Hour))
		mock.ExpectBegin()
		mock.ExpectExec(q("INSERT INTO refresh_tokens")).
			WithArgs(int64(1), int64(3), sqlmock.AnyArg(), sqlmock.AnyArg(),
				int64(2), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(2, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(1)).WillReturnRows(sqlSchoolBandung())

		res := jsonReq(t, app, "POST", "/api/users/3/impersonate", nil, withCookie(hAdmin(t, s), foreign))
		requireDataEnvelope(t, res)
	})
}

// ---------------------------------------------------------------------------
// Impersonation — refusals
// ---------------------------------------------------------------------------

func TestAdminImpersonateDenials(t *testing.T) {
	type setup struct {
		actor     string // sa | admin | fas | adminNoSchool
		targetID  string
		target    *sqlmock.Rows
		wantCode  string
		extraAuth bool
	}
	cases := []struct {
		name string
		set  setup
	}{
		{"self is VALIDATION", setup{actor: "admin", targetID: "2",
			target: sqlAdminRow(), wantCode: CodeValidation}},
		{"unknown target", setup{actor: "admin", targetID: "99",
			target: sqlmock.NewRows(userRowCols), wantCode: CodeNotFound}},
		{"malformed id", setup{actor: "admin", targetID: "abc",
			wantCode: CodeNotFound}},
		{"ADMIN -> ADMIN", setup{actor: "admin", targetID: "4",
			target: adminTargetRows(4, "Admin2", "ADMIN", 1, true), wantCode: CodeForbiddenRole}},
		{"ADMIN -> SA", setup{actor: "admin", targetID: "1",
			target: sqlSARow(), wantCode: CodeForbiddenRole}},
		{"ADMIN -> foreign FAS", setup{actor: "admin", targetID: "7",
			target: adminTargetRows(7, "Foreign", "FASILITATOR", 2, true), wantCode: CodeNotFound}},
		{"ADMIN -> pending FAS", setup{actor: "admin", targetID: "8",
			target: pendingTargetRows(8), wantCode: CodeAccountInactive}},
		{"ADMIN -> deactivated FAS", setup{actor: "admin", targetID: "9",
			target: inactiveTargetRows(9), wantCode: CodeAccountInactive}},
		{"ADMIN -> FAS in disabled school", setup{actor: "admin", targetID: "10",
			target: adminTargetRows(10, "OffSchool", "FASILITATOR", 1, false),
			wantCode: CodeSchoolInactive}},
		{"SA -> SA", setup{actor: "sa", targetID: "5",
			target: adminTargetRows(5, "OtherSA", "SA", 0, false), wantCode: CodeForbiddenRole}},
		{"FASILITATOR actor is rejected by the guard", setup{actor: "fas", targetID: "3",
			wantCode: CodeForbiddenRole}},
		{"ADMIN without X-School-ID", setup{actor: "adminNoSchool", targetID: "3",
			wantCode: CodeSchoolMismatch}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)

			var headers map[string]string
			switch tc.set.actor {
			case "sa":
				expectSAAuth(mock, 1)
				headers = hSA(t, s)
			case "admin":
				expectAdminAuth(mock)
				headers = hAdmin(t, s)
			case "fas":
				expectFASAuth(mock)
				headers = hFAS(t, s)
			case "adminNoSchool":
				expectAdminAuth(mock)
				headers = bearer(mintAccess(t, s, adminUserRow()))
			}
			if tc.set.target != nil {
				id, pErr := strconv.ParseInt(tc.set.targetID, 10, 64)
				require.NoError(t, pErr)
				mock.ExpectQuery(sqlUserByID).WithArgs(id).
					WillReturnRows(tc.set.target)
			}
			// NO ExpectBegin: every refusal answers before a Tx starts.

			res := jsonReq(t, app, "POST", "/api/users/"+tc.set.targetID+"/impersonate", nil, headers)
			errObj := requireErrEnvelope(t, res, tc.set.wantCode)
			require.NotEmpty(t, errObj["message"])
			require.Empty(t, res.header.Get("Set-Cookie"),
				"a refused impersonation must never mint a session")
		})
	}
}

func TestAdminImpersonateDBFault(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
	mock.ExpectBegin()
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).WillReturnError(errBoom)
	mock.ExpectRollback()

	res := jsonReq(t, app, "POST", "/api/users/3/impersonate", nil, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
	require.Empty(t, res.header.Get("Set-Cookie"))
}

// ---------------------------------------------------------------------------
// Generated password endpoint (§10.1)
// ---------------------------------------------------------------------------

func TestAdminGeneratePasswordEndpoint(t *testing.T) {
	t.Run("SA obtains a policy-compliant value without writing", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectSAAuth(mock, 1)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(7)).
			WillReturnRows(adminTargetRows(7, "Siti", "FASILITATOR", 2, true))

		res := jsonReq(t, app, "POST", "/api/users/7/password/generate", nil, hSA(t, s))
		data := requireDataEnvelope(t, res).(map[string]any)
		pw, ok := data["password"].(string)
		require.True(t, ok, "generate must return the plaintext once: %s", res.body)
		require.True(t, validPassword(pw))
	})

	t.Run("unknown target", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectAdminAuth(mock)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(99)).
			WillReturnRows(sqlmock.NewRows(userRowCols))

		res := jsonReq(t, app, "POST", "/api/users/99/password/generate", nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeNotFound)
	})

	t.Run("SA -> SA is forbidden", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectSAAuth(mock, 1)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(5)).
			WillReturnRows(adminTargetRows(5, "OtherSA", "SA", 0, false))

		res := jsonReq(t, app, "POST", "/api/users/5/password/generate", nil, hSA(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})

	t.Run("FASILITATOR actor never reaches the handler", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectFASAuth(mock)
		res := jsonReq(t, app, "POST", "/api/users/3/password/generate", nil, hFAS(t, s))
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})
}

// ---------------------------------------------------------------------------
// Administrative password reset (§10.2-§10.3)
// ---------------------------------------------------------------------------

func TestAdminResetPasswordHappyPath(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	const newPassword = "ManualPass123"
	var stored string
	expectSAAuth(mock, 1)
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(7)).
		WillReturnRows(adminTargetRows(7, "Siti", "FASILITATOR", 2, true))
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE users SET password_hash = ? WHERE id = ?")).
		WithArgs(hashArg{password: newPassword, got: &stored}, int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = ? AND revoked_at IS NULL")).
		WithArgs(int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	// Ledger row: source='ADMIN', created_by=actor, plaintext NULL, already used.
	mock.ExpectExec(q("INSERT INTO reset_tokens (school_id, user_id, source, created_by, code_hash, code_plain, expires_at, used_at)")).
		WithArgs(int64(2), int64(7), int64(1), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	res := jsonReq(t, app, "POST", "/api/users/7/password",
		map[string]any{"password": newPassword}, hSA(t, s))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, map[string]any{"reset": true}, data)

	// AC 14: the stored hash must authenticate the new password. The captured
	// value feeds a real login round-trip.
	require.NotEmpty(t, stored)
	mock.ExpectQuery(sqlUserByEmail).WithArgs("siti@school.id").
		WillReturnRows(sqlUserRow(7, "Siti", "siti@school.id", "", stored,
			"FASILITATOR", "APPROVED", true, int64(2), true))
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).WillReturnResult(sqlmock.NewResult(3, 1))
	mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
			AddRow(2, "Surabaya", "Indonesia", nil, true, fixedTS))

	login := jsonReq(t, app, "POST", "/api/auth/login",
		map[string]any{"email": "siti@school.id", "password": newPassword}, nil)
	loginData := requireDataEnvelope(t, login).(map[string]any)
	require.Equal(t, float64(7), loginData["user"].(map[string]any)["id"],
		"the reset password must log in successfully")
}

func TestAdminResetPasswordRefusals(t *testing.T) {
	cases := []struct {
		name     string
		actor    string
		targetID string
		target   *sqlmock.Rows
		body     any
		wantCode string
	}{
		{"weak password fails before the target lookup", "admin", "3", nil,
			map[string]any{"password": "short"}, CodeValidation},
		{"missing password", "admin", "3", nil,
			map[string]any{}, CodeValidation},
		{"unknown target", "admin", "99", sqlmock.NewRows(userRowCols),
			map[string]any{"password": "ManualPass123"}, CodeNotFound},
		{"self reset is VALIDATION", "admin", "2", sqlAdminRow(),
			map[string]any{"password": "ManualPass123"}, CodeValidation},
		{"ADMIN -> ADMIN", "admin", "4", adminTargetRows(4, "Admin2", "ADMIN", 1, true),
			map[string]any{"password": "ManualPass123"}, CodeForbiddenRole},
		{"ADMIN -> foreign FAS", "admin", "7", adminTargetRows(7, "Foreign", "FASILITATOR", 2, true),
			map[string]any{"password": "ManualPass123"}, CodeNotFound},
		{"SA -> SA", "sa", "5", adminTargetRows(5, "OtherSA", "SA", 0, false),
			map[string]any{"password": "ManualPass123"}, CodeForbiddenRole},
		{"FASILITATOR actor", "fas", "3", nil,
			map[string]any{"password": "ManualPass123"}, CodeForbiddenRole},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)

			var headers map[string]string
			switch tc.actor {
			case "sa":
				expectSAAuth(mock, 1)
				headers = hSA(t, s)
			case "admin":
				expectAdminAuth(mock)
				headers = hAdmin(t, s)
			case "fas":
				expectFASAuth(mock)
				headers = hFAS(t, s)
			}
			if tc.target != nil {
				id, pErr := strconv.ParseInt(tc.targetID, 10, 64)
				require.NoError(t, pErr)
				mock.ExpectQuery(sqlUserByID).WithArgs(id).WillReturnRows(tc.target)
			}

			res := jsonReq(t, app, "POST", "/api/users/"+tc.targetID+"/password", tc.body, headers)
			requireErrEnvelope(t, res, tc.wantCode)
		})
	}
}

func TestAdminResetPasswordDBFault(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectAdminAuth(mock)
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
	mock.ExpectBegin()
	mock.ExpectExec(q("UPDATE users SET password_hash = ? WHERE id = ?")).
		WithArgs(sqlmock.AnyArg(), int64(3)).
		WillReturnError(errBoom)
	mock.ExpectRollback()

	res := jsonReq(t, app, "POST", "/api/users/3/password",
		map[string]any{"password": "ManualPass123"}, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// ---------------------------------------------------------------------------
// Ledger propagation through refresh / me (AC 8, AC 9)
// ---------------------------------------------------------------------------

// cookieValue extracts the raw refresh JWT from a response's Set-Cookie.
func cookieValue(t *testing.T, res httpRes) string {
	t.Helper()
	sc := res.header.Get("Set-Cookie")
	idx := strings.Index(sc, refreshCookie+"=")
	require.GreaterOrEqual(t, idx, 0, "expected a refresh_token cookie, got %q", sc)
	rest := sc[idx+len(refreshCookie)+1:]
	if end := strings.Index(rest, ";"); end >= 0 {
		rest = rest[:end]
	}
	require.NotEmpty(t, rest)
	return rest
}

func TestAuthRefreshPropagatesImpersonationLedger(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// impersonated_by 12 (Budi, ADMIN) with root "root-1" already on the row.
	expectRotation := func(oldJTI string) {
		mock.ExpectBegin()
		mock.ExpectQuery(q("SELECT revoked_at, impersonated_by, impersonation_root FROM refresh_tokens WHERE jti = ? FOR UPDATE")).
			WithArgs(oldJTI).
			WillReturnRows(sqlmock.NewRows([]string{"revoked_at", "impersonated_by", "impersonation_root"}).
				AddRow(nil, int64(12), "root-1"))
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
		mock.ExpectExec(q("UPDATE refresh_tokens SET revoked_at = NOW() WHERE jti = ?")).
			WithArgs(oldJTI).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(q("SELECT name, role FROM users WHERE id = ?")).WithArgs(int64(12)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "role"}).AddRow("Budi", "ADMIN"))
		mock.ExpectExec(q("INSERT INTO refresh_tokens")).
			WithArgs(int64(1), int64(3), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(12), "root-1").
			WillReturnResult(sqlmock.NewResult(2, 1))
		mock.ExpectCommit()
		mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(1)).WillReturnRows(sqlSchoolBandung())
	}
	wantBlock := map[string]any{
		"actor_id": float64(12), "actor_name": "Budi", "actor_role": "ADMIN",
	}

	cookie := mintRefresh(t, s, fasUserRow(), "jti-imp-1", time.Now().Add(time.Hour))
	expectRotation("jti-imp-1")
	res := doReq(t, app, "POST", "/api/auth/refresh", "", withCookie(nil, cookie))
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, wantBlock, data["impersonation"])
	require.Equal(t, float64(3), data["user"].(map[string]any)["id"])

	// One rotation later the banner still works and the root is unchanged.
	next := cookieValue(t, res)
	claims, err := s.parseRefresh(next)
	require.NoError(t, err)
	expectRotation(claims.ID)
	res = doReq(t, app, "POST", "/api/auth/refresh", "", withCookie(nil, next))
	data = requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, wantBlock, data["impersonation"])
}

func TestAuthMeImpersonationBlock(t *testing.T) {
	t.Run("cookie of the caller returns the block", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectFASAuth(mock)
		mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(1)).WillReturnRows(sqlSchoolBandung())
		mock.ExpectQuery(q("SELECT impersonated_by FROM refresh_tokens WHERE jti = ? AND revoked_at IS NULL")).
			WithArgs("jti-live").
			WillReturnRows(sqlmock.NewRows([]string{"impersonated_by"}).AddRow(int64(12)))
		mock.ExpectQuery(q("SELECT name, role FROM users WHERE id = ?")).WithArgs(int64(12)).
			WillReturnRows(sqlmock.NewRows([]string{"name", "role"}).AddRow("Budi", "ADMIN"))
		cookie := mintRefresh(t, s, fasUserRow(), "jti-live", time.Now().Add(time.Hour))

		res := doReq(t, app, "GET", "/api/auth/me", "", withCookie(hFAS(t, s), cookie))
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, map[string]any{
			"actor_id": float64(12), "actor_name": "Budi", "actor_role": "ADMIN",
		}, data["impersonation"])
	})

	t.Run("no cookie omits the block", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectFASAuth(mock)
		mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(1)).WillReturnRows(sqlSchoolBandung())

		res := doReq(t, app, "GET", "/api/auth/me", "", hFAS(t, s))
		data := requireDataEnvelope(t, res).(map[string]any)
		_, has := data["impersonation"]
		require.False(t, has, "bearer-only callers get no guessed block")
	})

	t.Run("another user's cookie omits the block", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)

		expectFASAuth(mock)
		mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(1)).WillReturnRows(sqlSchoolBandung())
		foreign := mintRefresh(t, s, adminUserRow(), "jti-other", time.Now().Add(time.Hour))

		res := doReq(t, app, "GET", "/api/auth/me", "", withCookie(hFAS(t, s), foreign))
		data := requireDataEnvelope(t, res).(map[string]any)
		_, has := data["impersonation"]
		require.False(t, has)
	})
}

// TestImpersonationBlockDeletedActor pins C10: a ledger row whose actor was
// deleted still describes the session instead of hiding the banner.
func TestImpersonationBlockDeletedActor(t *testing.T) {
	s, mock := newTestServer(t)
	mock.ExpectQuery(q("SELECT name, role FROM users WHERE id = ?")).WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "role"}))

	imp, err := s.impersonationBlock(context.Background(), s.db, 99)
	require.NoError(t, err)
	require.Equal(t, int64(99), imp.ActorID)
	require.Equal(t, "[deleted #99]", imp.ActorName)
	require.Empty(t, imp.ActorRole)
}

// ---------------------------------------------------------------------------
// Route-level rate limiting (§9.1/§10: 10/min/IP on all three routes)
// ---------------------------------------------------------------------------

func TestAdminControlRoutesAreRateLimited(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// Ten allowed calls (each refused at the predicate so nothing is written):
	// FAS rescanned as a self target only needs the two auth/lookup rows.
	for range 10 {
		expectAdminAuth(mock)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(2)).WillReturnRows(sqlAdminRow())
		res := jsonReq(t, app, "POST", "/api/users/2/impersonate", nil, hAdmin(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	}
	// Eleventh: limiter answers before auth, with Retry-After (envelope stays 200).
	res := jsonReq(t, app, "POST", "/api/users/2/impersonate", nil, hAdmin(t, s))
	requireErrEnvelope(t, res, CodeRateLimited)
	require.NotEmpty(t, res.header.Get("Retry-After"))
}

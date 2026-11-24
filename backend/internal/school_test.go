package internal

import (
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// TestSchoolGuardNonSAMismatch: X-School-ID must equal the JWT's school claim
// for non-SA roles (E7).
func TestSchoolGuardNonSAMismatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
	}{
		{"different school", "2"},
		{"not a number", "abc"},
		{"missing header", ""},
		{"zero", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)

			// FAS token claims school 1.
			mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
			h := bearer(mintAccess(t, s, fasUserRow()))
			if tc.header != "" {
				h["X-School-ID"] = tc.header
			}
			res := doReq(t, app, "GET", "/api/auth/me", "", h)
			requireErrEnvelope(t, res, CodeSchoolMismatch)
		})
	}
}

// TestSchoolGuardSAInactiveSchool: SA may target any ACTIVE school; an
// inactive school is SCHOOL_INACTIVE (E6).
func TestSchoolGuardSAInactiveSchool(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(false))

	h := bearer(mintAccess(t, s, saUserRow()))
	h["X-School-ID"] = "9"
	res := doReq(t, app, "GET", "/api/auth/me", "", h)
	requireErrEnvelope(t, res, CodeSchoolInactive)
}

// TestSchoolGuardSAValidSwitchPasses: SA + active school header → the request
// runs against that school and /auth/me reflects it.
func TestSchoolGuardSAValidSwitchPasses(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(sqlSchoolByID()).WithArgs(int64(5)).WillReturnRows(
		sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
			AddRow(5, "Surabaya", "Indonesia", nil, true, fixedTS))

	h := bearer(mintAccess(t, s, saUserRow()))
	h["X-School-ID"] = "5"
	res := doReq(t, app, "GET", "/api/auth/me", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	school := data["school"].(map[string]any)
	require.Equal(t, "Surabaya", school["school_name"])
}

// TestSchoolGuardSAWithoutHeaderFallsBackToLatest: SA may omit the header;
// the session school is the latest active school (F-01 default).
func TestSchoolGuardSAWithoutHeaderFallsBackToLatest(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	mock.ExpectQuery(q("ORDER BY id DESC LIMIT 1")).WillReturnRows(
		sqlmock.NewRows([]string{"id", "school_name", "country", "city", "is_active", "created_at"}).
			AddRow(1, "Bandung", "Indonesia", nil, true, fixedTS))

	res := doReq(t, app, "GET", "/api/auth/me", "", bearer(mintAccess(t, s, saUserRow())))
	data := requireDataEnvelope(t, res).(map[string]any)
	school := data["school"].(map[string]any)
	require.Equal(t, "Bandung", school["school_name"])
}

// TestSchoolCrossSchoolIDIsNotFoundWithoutLeak: an id outside the caller's
// school scope answers the IDENTICAL NOT_FOUND envelope as a genuinely
// unknown id — no existence oracle.
func TestSchoolCrossSchoolIDIsNotFoundWithoutLeak(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// FAS with own children {3}: asking for child 77 (another facilitator's)
	// must be NOT_FOUND with NO row lookup at all.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
	mock.ExpectQuery(q("SELECT DISTINCT sm.child_id FROM session_members sm")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id"}).AddRow(3))
	hFAS := bearer(mintAccess(t, s, fasUserRow()))
	hFAS["X-School-ID"] = "1"
	resFAS := doReq(t, app, "GET", "/api/children/77", "", hFAS)
	requireErrEnvelope(t, resFAS, CodeNotFound)

	// SA asking for the same id in school 1: the scoped row lookup misses.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(q("FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(77), int64(1)).WillReturnError(sql.ErrNoRows)
	hSA := bearer(mintAccess(t, s, saUserRow()))
	hSA["X-School-ID"] = "1"
	resSA := doReq(t, app, "GET", "/api/children/77", "", hSA)
	requireErrEnvelope(t, resSA, CodeNotFound)

	require.Equal(t, resFAS.body, resSA.body,
		"cross-school and unknown ids must answer byte-identical NOT_FOUND envelopes")
}

// TestSchoolFASScopedChildrenList: the children list runs through
// fasChildScope — the same helper the stream filter uses.
func TestSchoolFASScopedChildrenList(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
	mock.ExpectQuery(q("SELECT DISTINCT sm.child_id FROM session_members sm")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id"}).AddRow(3).AddRow(5))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM children WHERE school_id = ? AND id IN (?,?)")).
		WithArgs(int64(1), int64(3), int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	childCols := []string{"id", "name", "age", "age_confirmed", "school", "mother_name",
		"mother_wa", "father_name", "father_wa", "guardian_name", "is_active", "created_at"}
	mock.ExpectQuery(q("ORDER BY id asc, id ASC LIMIT ? OFFSET ?")).
		WithArgs(int64(1), int64(3), int64(5), 20, 0).
		WillReturnRows(sqlmock.NewRows(childCols).
			AddRow(3, "Ayu", 8, false, "SD 1", "Ibu A", "+628111", nil, nil, nil, true, fixedTS).
			AddRow(5, "Bima", 10, false, "SD 2", "Ibu B", "+628222", nil, nil, nil, true, fixedTS))

	h := bearer(mintAccess(t, s, fasUserRow()))
	h["X-School-ID"] = "1"
	res := doReq(t, app, "GET", "/api/children", "", h)
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m, 2)
	require.Len(t, m["data"].([]any), 2)
	require.Equal(t, float64(2), m["meta"].(map[string]any)["total"])
}

// TestSchoolFASChildScopeHelper pins the helper semantics directly:
// non-facilitators unrestricted, FAS restricted to own-group children.
func TestSchoolFASChildScopeHelper(t *testing.T) {
	s, mock := newTestServer(t)
	ctx := t.Context()

	ids, scoped, err := s.fasChildScope(ctx, "ADMIN", 2, 1)
	require.NoError(t, err)
	require.False(t, scoped)
	require.Nil(t, ids)

	mock.ExpectQuery(q("SELECT DISTINCT sm.child_id FROM session_members sm")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id"}).AddRow(3).AddRow(5))
	ids, scoped, err = s.fasChildScope(ctx, "FASILITATOR", 3, 1)
	require.NoError(t, err)
	require.True(t, scoped)
	require.Equal(t, []int64{3, 5}, ids, "DISTINCT children in own groups")
}

// TestSchoolFASForeignSessionDetailIsNotFound: session detail visibility
// mirrors the list scope for facilitators.
func TestSchoolFASForeignSessionDetailIsNotFound(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
	// The session exists in the school…
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(99), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "program_id", "program_name_snap", "name", "description", "date",
			"start_time", "end_time", "location", "status", "created_by", "created_at",
		}).AddRow(99, 1, "Program A", "Sesi Luar", nil, fixedTS, nil, nil, "Lab", "OPENED", nil, fixedTS))
	// …but none of the facilitator's groups live there → NOT_FOUND.
	mock.ExpectQuery(q("SELECT id, session_id FROM session_groups WHERE facilitator_id = ?")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).AddRow(10, 2))

	h := bearer(mintAccess(t, s, fasUserRow()))
	h["X-School-ID"] = "1"
	res := doReq(t, app, "GET", "/api/sessions/99", "", h)
	requireErrEnvelope(t, res, CodeNotFound)
}

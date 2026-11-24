package internal

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Shared fixtures (Cax prefix: 8-agent merge safety)
// ---------------------------------------------------------------------------

// CaxAdminH wires an ADMIN caller (school 1) with the auth expectation.
func CaxAdminH(t *testing.T, s *Server, mock sqlmock.Sqlmock) map[string]string {
	t.Helper()
	expectAdminAuth(mock)
	return hAdmin(t, s)
}

// CaxFASH wires a FASILITATOR caller (user 3, school 1) with the auth
// expectation.
func CaxFASH(t *testing.T, s *Server, mock sqlmock.Sqlmock) map[string]string {
	t.Helper()
	return assessmentFASHeaders(t, s, mock)
}

// CaxIdem claims the (school, route, key) idempotency slot: purge + INSERT.
func CaxIdem(mock sqlmock.Sqlmock, route, key string) {
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), route, key).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), route, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// CaxRecord expects the success/failure envelope recording UPDATE.
func CaxRecord(mock sqlmock.Sqlmock, route, key string) {
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), route, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// CaxRelease expects the top-level DB_UNAVAILABLE claim release DELETE.
func CaxRelease(mock sqlmock.Sqlmock, route, key string) {
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), route, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// CaxSendGatesOpen wires the send-consent gates up to the consent row:
// OPENED session + member + active child with a mother WA number.
func CaxSendGatesOpen(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT mother_wa, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "is_active"}).
			AddRow("+628111111111", true))
}

const (
	caxConsentListPath   = "/api/consents"
	caxSessionConsenPath = "/api/sessions/2/consents"
)

// ---------------------------------------------------------------------------
// handleSessionConsents — GET /api/sessions/:sid/consents
// ---------------------------------------------------------------------------

// TestCaxSessionConsentsDefaultShapes: the admin paged list without filters —
// PENDING rows carry the copy/resend consent_url, decided rows drop it and
// stamp responded_at; NULL group/respondent stay JSON null; meta mirrors the
// page params.
func TestCaxSessionConsentsDefaultShapes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxAdminH(t, s, mock)

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM session_members sm")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(q("SELECT sm.child_id, c.name, g.name")).
		WithArgs(int64(2), int64(1), 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "name", "group_name",
			"status", "respondent_name", "responded_at", "token"}).
			AddRow(30, "Ayu", "Kelompok Merah", "PENDING", nil, nil, "tok-cax-1").
			AddRow(31, "Budi", nil, "SETUJU", "Ibu Ayu", fixedTS, "tok-cax-2"))

	res := doReq(t, app, "GET", caxSessionConsenPath, "", h)
	m := envelopeMap(t, res.body)
	_ = requireDataEnvelope(t, res)
	data := m["data"].([]any)
	require.Len(t, data, 2)

	pending := data[0].(map[string]any)
	require.Equal(t, "PENDING", pending["status"])
	require.Equal(t, "Kelompok Merah", pending["group_name"])
	require.Equal(t, "http://localhost:8002/view/consent?token=tok-cax-1",
		pending["consent_url"], "a PENDING row exposes the parent link")
	require.Nil(t, pending["respondent_name"])
	require.Nil(t, pending["responded_at"])

	decided := data[1].(map[string]any)
	require.Equal(t, "SETUJU", decided["status"])
	require.Nil(t, decided["group_name"], "NULL group stays JSON null")
	require.Nil(t, decided["consent_url"], "decided rows drop the link")
	require.Equal(t, "Ibu Ayu", decided["respondent_name"])
	require.Equal(t, "2026-01-02T03:04:05Z", decided["responded_at"])

	meta := m["meta"].(map[string]any)
	require.Equal(t, float64(1), meta["page"])
	require.Equal(t, float64(20), meta["per_page"])
	require.Equal(t, float64(2), meta["total"])
}

// TestCaxSessionConsentsStatusFilter: ?status=DITOLAK threads the filter
// into BOTH the count and list arguments.
func TestCaxSessionConsentsStatusFilter(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxAdminH(t, s, mock)

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM session_members sm")).
		WithArgs(int64(2), int64(1), "DITOLAK").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("SELECT sm.child_id, c.name, g.name")).
		WithArgs(int64(2), int64(1), "DITOLAK", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "name", "group_name",
			"status", "respondent_name", "responded_at", "token"}).
			AddRow(32, "Citra", nil, "DITOLAK", "Pak Adi", fixedTS, "tok-cax-3"))

	res := doReq(t, app, "GET", caxSessionConsenPath+"?status=DITOLAK", "", h)
	m := envelopeMap(t, res.body)
	_ = requireDataEnvelope(t, res)
	item := m["data"].([]any)[0].(map[string]any)
	require.Equal(t, "DITOLAK", item["status"])
	require.Nil(t, item["consent_url"])
	require.Equal(t, "2026-01-02T03:04:05Z", item["responded_at"])
	require.Equal(t, float64(1), m["meta"].(map[string]any)["total"])
}

// TestCaxSessionConsentsValidatesBeforeSQL: malformed route id, bad paging
// and an unknown status filter all answer their code with NO query issued
// beyond the auth lookup (any unexpected sqlmock call fails the test).
func TestCaxSessionConsentsValidatesBeforeSQL(t *testing.T) {
	t.Run("malformed session id is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		res := doReq(t, app, "GET", "/api/sessions/abc/consents", "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("per_page outside the allowlist is validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		res := doReq(t, app, "GET", caxSessionConsenPath+"?per_page=15", "", h)
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("unknown status filter is validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		res := doReq(t, app, "GET", caxSessionConsenPath+"?status=EXPIRED", "", h)
		requireErrEnvelope(t, res, CodeValidation)
	})
}

// TestCaxSessionConsentsSessionGate: the session existence gate answers
// NOT_FOUND for an unknown session and DB_UNAVAILABLE when the status read
// fails.
func TestCaxSessionConsentsSessionGate(t *testing.T) {
	t.Run("unknown session is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", caxSessionConsenPath, "", h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("status read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", caxSessionConsenPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestCaxSessionConsentsFASOwnGroupsScope: a facilitator's list runs the
// own-group predicate — the exact group ids land in the count/list args.
func TestCaxSessionConsentsFASOwnGroupsScope(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxFASH(t, s, mock)

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).AddRow(10, 2))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM session_members sm")).
		WithArgs(int64(2), int64(1), int64(10)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("SELECT sm.child_id, c.name, g.name")).
		WithArgs(int64(2), int64(1), int64(10), 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "name", "group_name",
			"status", "respondent_name", "responded_at", "token"}).
			AddRow(30, "Ayu", "Kelompok Merah", "PENDING", nil, nil, "tok-cax-4"))

	res := doReq(t, app, "GET", caxSessionConsenPath, "", h)
	m := envelopeMap(t, res.body)
	_ = requireDataEnvelope(t, res)
	require.Equal(t, float64(1), m["meta"].(map[string]any)["total"],
		"the FAS query args bound the result to its own group")
}

// TestCaxSessionConsentsFASForeignSessionIsNotFound: a session outside the
// facilitator's own groups never reveals its consents (errNotVisible →
// NOT_FOUND before any list query).
func TestCaxSessionConsentsFASForeignSessionIsNotFound(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxFASH(t, s, mock)

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).AddRow(10, 9))

	res := doReq(t, app, "GET", caxSessionConsenPath, "", h)
	requireErrEnvelope(t, res, CodeNotFound)
}

// TestCaxSessionConsentsFASScopeDBError: a failed own-scope read propagates
// as DB_UNAVAILABLE.
func TestCaxSessionConsentsFASScopeDBError(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxFASH(t, s, mock)

	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
		WithArgs(int64(3), int64(1)).WillReturnError(errors.New("connection refused"))

	res := doReq(t, app, "GET", caxSessionConsenPath, "", h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestCaxSessionConsentsListDBErrors: the three consentListRows failure
// branches (count, list, scan) all answer DB_UNAVAILABLE.
func TestCaxSessionConsentsListDBErrors(t *testing.T) {
	prefix := func(t *testing.T, s *Server, mock sqlmock.Sqlmock) map[string]string {
		t.Helper()
		h := CaxAdminH(t, s, mock)
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		return h
	}
	t.Run("count failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := prefix(t, s, mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM session_members sm")).
			WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", caxSessionConsenPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("list failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := prefix(t, s, mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM session_members sm")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(q("SELECT sm.child_id, c.name, g.name")).
			WithArgs(int64(2), int64(1), 20, 0).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", caxSessionConsenPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("row scan failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := prefix(t, s, mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM session_members sm")).
			WithArgs(int64(2), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(q("SELECT sm.child_id, c.name, g.name")).
			WithArgs(int64(2), int64(1), 20, 0).
			WillReturnRows(sqlmock.NewRows([]string{"child_id", "name", "group_name",
				"status", "respondent_name", "responded_at", "token"}).
				AddRow("not-an-id", "Ayu", nil, "PENDING", nil, nil, "tok-cax-5"))
		res := doReq(t, app, "GET", caxSessionConsenPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// TestCaxSessionConsentsQFilter: ?q threads a case-insensitive substring over
// child/group/respondent name into BOTH the count and list args after the
// sid/school scope; a miss returns an empty page and an empty q leaves the
// args untouched (F-B15, audit C13).
func TestCaxSessionConsentsQFilter(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxAdminH(t, s, mock)
	likeFrag := q("AND (c.name LIKE ? OR g.name LIKE ? OR cl.respondent_name LIKE ?)")
	listCols := []string{"child_id", "name", "group_name",
		"status", "respondent_name", "responded_at", "token"}
	expectGate := func() {
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	}

	// q hit: sid + school + three LIKE patterns in count and list.
	expectGate()
	mock.ExpectQuery(likeFrag).
		WithArgs(int64(2), int64(1), "%kel%", "%kel%", "%kel%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("SELECT sm.child_id, c.name, g.name")).
		WithArgs(int64(2), int64(1), "%kel%", "%kel%", "%kel%", 20, 0).
		WillReturnRows(sqlmock.NewRows(listCols).
			AddRow(30, "Ayu", "Kelompok Merah", "PENDING", nil, nil, "tok-cax-q1"))
	res := doReq(t, app, "GET", caxSessionConsenPath+"?q=kel", "", h)
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m["data"].([]any), 1)
	require.Equal(t, float64(1), m["meta"].(map[string]any)["total"])

	// q miss: same predicate, zero rows.
	expectAdminAuth(mock)
	expectGate()
	mock.ExpectQuery(likeFrag).
		WithArgs(int64(2), int64(1), "%zzz%", "%zzz%", "%zzz%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(q("SELECT sm.child_id, c.name, g.name")).
		WithArgs(int64(2), int64(1), "%zzz%", "%zzz%", "%zzz%", 20, 0).
		WillReturnRows(sqlmock.NewRows(listCols))
	res = doReq(t, app, "GET", caxSessionConsenPath+"?q=zzz", "", h)
	require.Equal(t, 200, res.status)
	m = envelopeMap(t, res.body)
	require.Empty(t, m["data"].([]any))
	require.Equal(t, float64(0), m["meta"].(map[string]any)["total"])

	// Empty q: no LIKE predicate, args unchanged.
	expectAdminAuth(mock)
	expectGate()
	mock.ExpectQuery(q("SELECT COUNT(*) FROM session_members sm")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("SELECT sm.child_id, c.name, g.name")).
		WithArgs(int64(2), int64(1), 20, 0).
		WillReturnRows(sqlmock.NewRows(listCols).
			AddRow(30, "Ayu", nil, "PENDING", nil, nil, "tok-cax-q2"))
	res = doReq(t, app, "GET", caxSessionConsenPath+"?q=", "", h)
	require.Equal(t, 200, res.status)
	require.Len(t, envelopeMap(t, res.body)["data"].([]any), 1)
}

// ---------------------------------------------------------------------------
// handleConsentList — GET /api/consents
// ---------------------------------------------------------------------------

// TestCaxConsentListFiltersThreadIntoSQL: status+session_id+child_id all
// land in the count/list arguments, and the decided row carries no
// consent_url.
func TestCaxConsentListFiltersThreadIntoSQL(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxAdminH(t, s, mock)

	mock.ExpectQuery(q("SELECT COUNT(*) FROM consent_links cl")).
		WithArgs(int64(1), "SETUJU", int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("SELECT cl.child_id, c.name, g.name, cl.status, cl.respondent_name")).
		WithArgs(int64(1), "SETUJU", int64(2), int64(30), 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"child_id", "name", "group_name",
			"status", "respondent_name", "responded_at", "token"}).
			AddRow(30, "Ayu", "Kelompok Merah", "SETUJU", "Ibu Ayu", fixedTS, "tok-cax-6"))

	res := doReq(t, app, "GET",
		caxConsentListPath+"?status=SETUJU&session_id=2&child_id=30", "", h)
	m := envelopeMap(t, res.body)
	_ = requireDataEnvelope(t, res)
	item := m["data"].([]any)[0].(map[string]any)
	require.Equal(t, "SETUJU", item["status"])
	require.Nil(t, item["consent_url"])
	require.Equal(t, "2026-01-02T03:04:05Z", item["responded_at"])
	require.Equal(t, float64(1), m["meta"].(map[string]any)["total"])
}

// TestCaxConsentListQFilter: ?q on the school-wide consent list threads the
// child/group/respondent LIKE predicate into BOTH the count and list args; a
// miss returns an empty page and an empty q leaves the args untouched
// (F-B15, audit C13).
func TestCaxConsentListQFilter(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxAdminH(t, s, mock)
	likeFrag := q("AND (c.name LIKE ? OR g.name LIKE ? OR cl.respondent_name LIKE ?)")
	listCols := []string{"child_id", "name", "group_name",
		"status", "respondent_name", "responded_at", "token"}

	// q hit: school scope + three LIKE patterns in count and list.
	mock.ExpectQuery(likeFrag).
		WithArgs(int64(1), "%ibu%", "%ibu%", "%ibu%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("SELECT cl.child_id, c.name, g.name, cl.status, cl.respondent_name")).
		WithArgs(int64(1), "%ibu%", "%ibu%", "%ibu%", 20, 0).
		WillReturnRows(sqlmock.NewRows(listCols).
			AddRow(30, "Ayu", "Kelompok Merah", "SETUJU", "Ibu Ayu", fixedTS, "tok-cax-q3"))
	res := doReq(t, app, "GET", caxConsentListPath+"?q=ibu", "", h)
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	require.Len(t, m["data"].([]any), 1)
	require.Equal(t, float64(1), m["meta"].(map[string]any)["total"])

	// q miss: same predicate, zero rows.
	expectAdminAuth(mock)
	mock.ExpectQuery(likeFrag).
		WithArgs(int64(1), "%zzz%", "%zzz%", "%zzz%").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(q("SELECT cl.child_id, c.name, g.name, cl.status, cl.respondent_name")).
		WithArgs(int64(1), "%zzz%", "%zzz%", "%zzz%", 20, 0).
		WillReturnRows(sqlmock.NewRows(listCols))
	res = doReq(t, app, "GET", caxConsentListPath+"?q=zzz", "", h)
	require.Equal(t, 200, res.status)
	m = envelopeMap(t, res.body)
	require.Empty(t, m["data"].([]any))
	require.Equal(t, float64(0), m["meta"].(map[string]any)["total"])

	// Empty q: no LIKE predicate, args unchanged.
	expectAdminAuth(mock)
	mock.ExpectQuery(q("SELECT COUNT(*) FROM consent_links cl")).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("SELECT cl.child_id, c.name, g.name, cl.status, cl.respondent_name")).
		WithArgs(int64(1), 20, 0).
		WillReturnRows(sqlmock.NewRows(listCols).
			AddRow(30, "Ayu", nil, "PENDING", nil, nil, "tok-cax-q4"))
	res = doReq(t, app, "GET", caxConsentListPath+"?q=", "", h)
	require.Equal(t, 200, res.status)
	require.Len(t, envelopeMap(t, res.body)["data"].([]any), 1)
}

// TestCaxConsentListValidatesBeforeSQL: every rejected filter answers
// VALIDATION without a single query beyond the auth lookup.
func TestCaxConsentListValidatesBeforeSQL(t *testing.T) {
	cases := []struct {
		name, query string
	}{
		{"unknown status is validation", "?status=EXPIRED"},
		{"non-numeric session_id is validation", "?session_id=abc"},
		{"zero session_id is validation", "?session_id=0"},
		{"negative child_id is validation", "?child_id=-3"},
		{"per_page outside the allowlist is validation", "?per_page=7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			h := CaxAdminH(t, s, mock)
			res := doReq(t, app, "GET", caxConsentListPath+tc.query, "", h)
			requireErrEnvelope(t, res, CodeValidation)
		})
	}
}

// TestCaxConsentListFASSessionScope: a facilitator's list injects its owned
// session ids into the IN clause; an empty scope answers the empty page
// without touching the consent tables.
func TestCaxConsentListFASSessionScope(t *testing.T) {
	t.Run("owned sessions scope the IN clause", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxFASH(t, s, mock)

		mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).
				AddRow(10, 2).AddRow(11, 9))
		mock.ExpectQuery(q("SELECT COUNT(*) FROM consent_links cl")).
			WithArgs(int64(1), int64(2), int64(9)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(q("SELECT cl.child_id, c.name, g.name, cl.status, cl.respondent_name")).
			WithArgs(int64(1), int64(2), int64(9), 20, 0).
			WillReturnRows(sqlmock.NewRows([]string{"child_id", "name", "group_name",
				"status", "respondent_name", "responded_at", "token"}))

		res := doReq(t, app, "GET", caxConsentListPath, "", h)
		m := envelopeMap(t, res.body)
		require.Equal(t, []any{}, m["data"], "empty scope → empty list, never null")
		require.Equal(t, float64(0), m["meta"].(map[string]any)["total"])
	})
	t.Run("no owned sessions answers the empty page without consent queries", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxFASH(t, s, mock)

		mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
			WithArgs(int64(3), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}))

		res := doReq(t, app, "GET", caxConsentListPath, "", h)
		m := envelopeMap(t, res.body)
		require.Equal(t, []any{}, m["data"])
		require.Equal(t, float64(0), m["meta"].(map[string]any)["total"])
	})
}

// TestCaxConsentListFASScopeDBError: a failed session-scope read is
// DB_UNAVAILABLE.
func TestCaxConsentListFASScopeDBError(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxFASH(t, s, mock)

	mock.ExpectQuery(q("WHERE facilitator_id = ? AND school_id = ? ORDER BY id")).
		WithArgs(int64(3), int64(1)).WillReturnError(errors.New("connection refused"))

	res := doReq(t, app, "GET", caxConsentListPath, "", h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestCaxConsentListDBErrors: count/list/scan failures of the shared
// consentListRows answer DB_UNAVAILABLE on this route too.
func TestCaxConsentListDBErrors(t *testing.T) {
	t.Run("count failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM consent_links cl")).
			WithArgs(int64(1)).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", caxConsentListPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("list failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM consent_links cl")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(q("SELECT cl.child_id, c.name, g.name, cl.status, cl.respondent_name")).
			WithArgs(int64(1), 20, 0).WillReturnError(errors.New("connection refused"))
		res := doReq(t, app, "GET", caxConsentListPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("row scan failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		mock.ExpectQuery(q("SELECT COUNT(*) FROM consent_links cl")).
			WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(q("SELECT cl.child_id, c.name, g.name, cl.status, cl.respondent_name")).
			WithArgs(int64(1), 20, 0).
			WillReturnRows(sqlmock.NewRows([]string{"child_id", "name", "group_name",
				"status", "respondent_name", "responded_at", "token"}).
				AddRow("not-an-id", "Ayu", nil, "PENDING", nil, nil, "tok-cax-7"))
		res := doReq(t, app, "GET", caxConsentListPath, "", h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
}

// ---------------------------------------------------------------------------
// handleSendConsent — POST .../send-consent
// ---------------------------------------------------------------------------

// TestCaxSendConsentValidatesBeforeAnyWrite: role/school/param/idempotency
// gates each answer its code with NO write issued — sqlmock carries only the
// auth expectation, so any extra query fails the test.
func TestCaxSendConsentValidatesBeforeAnyWrite(t *testing.T) {
	t.Run("facilitator is forbidden role", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxFASH(t, s, mock)
		res := jsonReq(t, app, "POST", consentSendPath, nil, h)
		requireErrEnvelope(t, res, CodeForbiddenRole)
	})
	t.Run("SA without school header is validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
		res := jsonReq(t, app, "POST", consentSendPath, nil, hSABare(t, s))
		requireErrEnvelope(t, res, CodeValidation)
	})
	t.Run("malformed session id is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		res := jsonReq(t, app, "POST", "/api/sessions/abc/children/30/send-consent", nil, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("malformed child id is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		res := jsonReq(t, app, "POST", "/api/sessions/2/children/abc/send-consent", nil, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("missing idempotency key is validation", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := CaxAdminH(t, s, mock)
		res := jsonReq(t, app, "POST", consentSendPath, nil, h)
		requireErrEnvelope(t, res, CodeValidation)
	})
}

// TestCaxSendConsentGateFailures: the in-transaction gates — unknown
// session, membership, child row, inactive child, FINISHED precedence —
// each answer its code and record/release the claim correctly.
func TestCaxSendConsentGateFailures(t *testing.T) {
	const route = "POST " + consentSendPath
	auth := func(t *testing.T, s *Server, mock sqlmock.Sqlmock) map[string]string {
		t.Helper()
		h := CaxAdminH(t, s, mock)
		h["X-Idempotency-Key"] = "cax-gate"
		return h
	}

	t.Run("unknown session is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		CaxIdem(mock, route, "cax-gate")
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(sql.ErrNoRows)
		CaxRecord(mock, route, "cax-gate")
		res := jsonReq(t, app, "POST", consentSendPath, nil, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("session status failure is db unavailable and releases the claim", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		CaxIdem(mock, route, "cax-gate")
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnError(errors.New("connection refused"))
		CaxRelease(mock, route, "cax-gate")
		res := jsonReq(t, app, "POST", consentSendPath, nil, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("membership read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		CaxIdem(mock, route, "cax-gate")
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).WillReturnError(errors.New("connection refused"))
		CaxRelease(mock, route, "cax-gate")
		res := jsonReq(t, app, "POST", consentSendPath, nil, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("non-member child is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		CaxIdem(mock, route, "cax-gate")
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(0))
		CaxRecord(mock, route, "cax-gate")
		res := jsonReq(t, app, "POST", consentSendPath, nil, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("missing child row is not found", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		CaxIdem(mock, route, "cax-gate")
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT mother_wa, is_active FROM children")).
			WithArgs(int64(30), int64(1)).WillReturnError(sql.ErrNoRows)
		CaxRecord(mock, route, "cax-gate")
		res := jsonReq(t, app, "POST", consentSendPath, nil, h)
		requireErrEnvelope(t, res, CodeNotFound)
	})
	t.Run("child read failure is db unavailable", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		CaxIdem(mock, route, "cax-gate")
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT mother_wa, is_active FROM children")).
			WithArgs(int64(30), int64(1)).WillReturnError(errors.New("connection refused"))
		CaxRelease(mock, route, "cax-gate")
		res := jsonReq(t, app, "POST", consentSendPath, nil, h)
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})
	t.Run("inactive child is child inactive", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := auth(t, s, mock)
		CaxIdem(mock, route, "cax-gate")
		mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
			WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
		mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
			WithArgs(int64(2), int64(30)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
		mock.ExpectQuery(q("SELECT mother_wa, is_active FROM children")).
			WithArgs(int64(30), int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "is_active"}).
				AddRow("+628111111111", false))
		CaxRecord(mock, route, "cax-gate")
		res := jsonReq(t, app, "POST", consentSendPath, nil, h)
		requireErrEnvelope(t, res, CodeChildInactive)
	})
	t.Run("finished session is locked even before the consent row is read", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		stub, waURL := startWAStub(t, 200)
		s.cfg.OpenwaBaseURL = waURL
		h := auth(t, s, mock)
		CaxIdem(mock, route, "cax-gate")
		CaxSendGatesOpenFINISHED(mock)
		CaxRecord(mock, route, "cax-gate")
		res := jsonReq(t, app, "POST", consentSendPath, nil, h)
		requireErrEnvelope(t, res, CodeSessionLocked)
		require.Zero(t, stub.calls.Load(), "a locked session never reaches WA")
	})
}

// CaxSendGatesOpenFINISHED is CaxSendGatesOpen with a FINISHED session.
func CaxSendGatesOpenFINISHED(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("FINISHED"))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(q("SELECT mother_wa, is_active FROM children")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"mother_wa", "is_active"}).
			AddRow("+628111111111", true))
}

// TestCaxSendConsentFirstSendCreatesAndWAs: no consent row yet → INSERT
// (fresh 64-hex token), one consent.changed frame, ONE WA send, the outbox
// audit row, and the {token, decision:PENDING, resent:false} envelope.
func TestCaxSendConsentFirstSendCreatesAndWAs(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	stub, waURL := startWAStub(t, 200)
	s.cfg.OpenwaBaseURL = waURL
	h := CaxAdminH(t, s, mock)
	h["X-Idempotency-Key"] = "cax-first-1"
	const route = "POST " + consentSendPath

	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	CaxIdem(mock, route, "cax-first-1")
	CaxSendGatesOpen(mock)
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(q("INSERT INTO consent_links")).
		WithArgs(int64(1), int64(2), int64(30), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(8, 1))
	mock.ExpectExec(q("INSERT INTO wa_outbox")).
		WithArgs(int64(1), "+628111111111", sqlmock.AnyArg(), "SENT",
			"msg-1", nil, "cax-first-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	CaxRecord(mock, route, "cax-first-1")

	res := jsonReq(t, app, "POST", consentSendPath, nil, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Regexp(t, `^[0-9a-f]{64}$`, data["token"], "fresh CHAR(64) token")
	require.Equal(t, "PENDING", data["decision"])
	require.Equal(t, false, data["resent"])
	require.EqualValues(t, 1, stub.calls.Load(), "the first send WAs the mother once")

	created := covNextFrame(t, sub, "consent.changed after create")
	require.Equal(t, "consent.changed", created.Entity)
	require.Equal(t, int64(8), created.EntityID)
	require.Equal(t, "created", created.Action)
}

// TestCaxSendConsentPendingResendReusesToken: a PENDING row is resent with
// the SAME token (resent:true) and one more WA call.
func TestCaxSendConsentPendingResendReusesToken(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	stub, waURL := startWAStub(t, 200)
	s.cfg.OpenwaBaseURL = waURL
	h := CaxAdminH(t, s, mock)
	h["X-Idempotency-Key"] = "cax-resend-1"
	const route = "POST " + consentSendPath

	sub := covSubscribe(t, s, ScopeSnapshot{Role: "ADMIN", SchoolID: 1})

	CaxIdem(mock, route, "cax-resend-1")
	CaxSendGatesOpen(mock)
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token", "status"}).
			AddRow(9, "tok-cax-p", "PENDING"))
	mock.ExpectExec(q("INSERT INTO wa_outbox")).
		WithArgs(int64(1), "+628111111111",
			"http://localhost:8002/view/consent?token=tok-cax-p", "SENT",
			"msg-1", nil, "cax-resend-1").
		WillReturnResult(sqlmock.NewResult(2, 1))
	CaxRecord(mock, route, "cax-resend-1")

	res := jsonReq(t, app, "POST", consentSendPath, nil, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "tok-cax-p", data["token"], "the resend reuses the stored token")
	require.Equal(t, "PENDING", data["decision"])
	require.Equal(t, true, data["resent"])
	require.EqualValues(t, 1, stub.calls.Load())

	updated := covNextFrame(t, sub, "consent.changed after resend")
	require.Equal(t, "consent.changed", updated.Entity)
	require.Equal(t, int64(9), updated.EntityID)
	require.Equal(t, "updated", updated.Action)
}

// TestCaxSendConsentCreateRaceFallsBackToExistingRow: a duplicate-key INSERT
// (lost create race) re-reads the winner's row and resends ITS token.
func TestCaxSendConsentCreateRaceFallsBackToExistingRow(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	stub, waURL := startWAStub(t, 200)
	s.cfg.OpenwaBaseURL = waURL
	h := CaxAdminH(t, s, mock)
	h["X-Idempotency-Key"] = "cax-race-1"
	const route = "POST " + consentSendPath

	CaxIdem(mock, route, "cax-race-1")
	CaxSendGatesOpen(mock)
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(q("INSERT INTO consent_links")).
		WithArgs(int64(1), int64(2), int64(30), sqlmock.AnyArg()).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token", "status"}).
			AddRow(10, "tok-cax-r", "PENDING"))
	mock.ExpectExec(q("INSERT INTO wa_outbox")).
		WithArgs(int64(1), "+628111111111", sqlmock.AnyArg(), "SENT",
			"msg-1", nil, "cax-race-1").
		WillReturnResult(sqlmock.NewResult(3, 1))
	CaxRecord(mock, route, "cax-race-1")

	res := jsonReq(t, app, "POST", consentSendPath, nil, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "tok-cax-r", data["token"], "the fallback reads the winner's token")
	require.Equal(t, true, data["resent"])
	require.EqualValues(t, 1, stub.calls.Load())
}

// TestCaxSendConsentConsentRowDBError: a failed consent-row read is
// DB_UNAVAILABLE and releases the claim for a clean retry.
func TestCaxSendConsentConsentRowDBError(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxAdminH(t, s, mock)
	h["X-Idempotency-Key"] = "cax-cerr-1"
	const route = "POST " + consentSendPath

	CaxIdem(mock, route, "cax-cerr-1")
	CaxSendGatesOpen(mock)
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(30)).WillReturnError(errors.New("connection refused"))
	CaxRelease(mock, route, "cax-cerr-1")

	res := jsonReq(t, app, "POST", consentSendPath, nil, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestCaxSendConsentInsertDBErrorReleasesClaim: a non-duplicate INSERT
// failure is DB_UNAVAILABLE + claim release (retryable transient).
func TestCaxSendConsentInsertDBErrorReleasesClaim(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxAdminH(t, s, mock)
	h["X-Idempotency-Key"] = "cax-ierr-1"
	const route = "POST " + consentSendPath

	CaxIdem(mock, route, "cax-ierr-1")
	CaxSendGatesOpen(mock)
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(q("INSERT INTO consent_links")).
		WithArgs(int64(1), int64(2), int64(30), sqlmock.AnyArg()).
		WillReturnError(errors.New("connection refused"))
	CaxRelease(mock, route, "cax-ierr-1")

	res := jsonReq(t, app, "POST", consentSendPath, nil, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestCaxSendConsentRaceFallbackReadFails: when the duplicate-key fallback
// re-read also fails, the send answers DB_UNAVAILABLE and releases the claim.
func TestCaxSendConsentRaceFallbackReadFails(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := CaxAdminH(t, s, mock)
	h["X-Idempotency-Key"] = "cax-race-2"
	const route = "POST " + consentSendPath

	CaxIdem(mock, route, "cax-race-2")
	CaxSendGatesOpen(mock)
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(30)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(q("INSERT INTO consent_links")).
		WithArgs(int64(1), int64(2), int64(30), sqlmock.AnyArg()).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(30)).WillReturnError(errors.New("connection refused"))
	CaxRelease(mock, route, "cax-race-2")

	res := jsonReq(t, app, "POST", consentSendPath, nil, h)
	requireErrEnvelope(t, res, CodeDBUnavailable)
}

// TestCaxSendConsentOutboxFailureKeepsSuccess: the audit row is best-effort —
// an outbox INSERT failure logs but the resend still answers its success
// envelope (the link stays usable).
func TestCaxSendConsentOutboxFailureKeepsSuccess(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	stub, waURL := startWAStub(t, 200)
	s.cfg.OpenwaBaseURL = waURL
	h := CaxAdminH(t, s, mock)
	h["X-Idempotency-Key"] = "cax-ob-1"
	const route = "POST " + consentSendPath

	CaxIdem(mock, route, "cax-ob-1")
	CaxSendGatesOpen(mock)
	mock.ExpectQuery(q("SELECT id, token, status FROM consent_links")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token", "status"}).
			AddRow(11, "tok-cax-ob", "PENDING"))
	mock.ExpectExec(q("INSERT INTO wa_outbox")).
		WithArgs(int64(1), "+628111111111", sqlmock.AnyArg(), "SENT",
			"msg-1", nil, "cax-ob-1").
		WillReturnError(errors.New("connection refused"))
	CaxRecord(mock, route, "cax-ob-1")

	res := jsonReq(t, app, "POST", consentSendPath, nil, h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "tok-cax-ob", data["token"])
	require.Equal(t, true, data["resent"])
	require.EqualValues(t, 1, stub.calls.Load())
}

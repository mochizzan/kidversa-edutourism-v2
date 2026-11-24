package internal

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// dashboardSAHeaders wires an SA caller WITHOUT X-School-ID (the dashboard
// school override path).
func dashboardSAHeaders(t *testing.T, s *Server, mock sqlmock.Sqlmock) map[string]string {
	t.Helper()
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	return bearer(mintAccess(t, s, saUserRow()))
}

// TestDashboardOverviewAggregatesShape: the six COUNT aggregates land in the
// contracts shape (reports_issued = REPORT links + narratives) and the
// shared progress rows carry total/assessed/percent.
func TestDashboardOverviewAggregatesShape(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	mock.ExpectQuery(q("(SELECT COUNT(*) FROM children WHERE school_id = ?)")).
		WithArgs(int64(1), int64(1), int64(1), int64(1), int64(1), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"children", "open", "finished",
			"report_links", "narratives", "pending"}).
			AddRow(10, 2, 3, 4, 5, 1))
	mock.ExpectQuery(q("FROM sessions s WHERE s.school_id = ?")).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date",
			"members", "activities", "assessed"}).
			AddRow(41, "Sesi A", fixedTS, 2, 3, 4))

	res := doReq(t, app, "GET", "/api/dashboard/overview", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, float64(10), data["children"])
	require.Equal(t, float64(2), data["sessions_open"])
	require.Equal(t, float64(3), data["sessions_finished"])
	require.Equal(t, float64(9), data["reports_issued"],
		"activity metric = 4 REPORT links + 5 narratives")
	require.Equal(t, float64(1), data["pending"])

	progress := data["progress"].([]any)
	require.Len(t, progress, 1)
	row := progress[0].(map[string]any)
	require.Equal(t, float64(41), row["session_id"])
	require.Equal(t, "2026-01-02", row["date"])
	require.Equal(t, float64(6), row["total"], "members × activities")
	require.Equal(t, float64(4), row["assessed"])
	require.Equal(t, float64(66.7), row["percent"], "round(4000/6)/10")
}

// expectDashboardAnalytics wires the four analytics queries with an explicit
// window fragment so the SQL itself pins the period (7d vs all).
func expectDashboardAnalytics(mock sqlmock.Sqlmock, school int64, winFrag string) {
	mock.ExpectQuery(q("WHERE a.school_id = ?" + winFrag + " GROUP BY stars")).
		WithArgs(school).
		WillReturnRows(sqlmock.NewRows([]string{"stars", "count"}).
			AddRow(1, 2).AddRow(3, 5).AddRow(5, 9)) // 5 is out of range → ignored
	mock.ExpectQuery(q("SELECT st.topic_id, MAX(st.topic_name_snap), COUNT(sa.id)")).
		WithArgs(school).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id", "name", "total"}).
			AddRow(9, "Topik A", 12))
	mock.ExpectQuery(q("WHERE a.school_id = ?" + winFrag + " GROUP BY st.topic_id")).
		WithArgs(school).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id", "assessed", "avg"}).
			AddRow(9, 8, 3.6))
	mock.ExpectQuery(q("FROM sessions s WHERE s.school_id = ?")).
		WithArgs(school).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date",
			"members", "activities", "assessed"}).
			AddRow(41, "Sesi A", fixedTS, 2, 3, 4))
	mock.ExpectQuery(q("IS NOT NULL" + winFrag + " GROUP BY a.assessor_id")).
		WithArgs(school).
		WillReturnRows(sqlmock.NewRows([]string{"assessor_id", "name", "count"}).
			AddRow(3, "Fasil", 7).
			AddRow(9, nil, 4)) // deleted assessor → "#9" fallback
}

// TestDashboardAnalyticsDefaultsToSevenDays: omitted ?period= selects the 7d
// window and the response matches the contracts analytics shape.
func TestDashboardAnalyticsDefaultsToSevenDays(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	// The default window is asserted BY the query fragment: if the handler
	// dropped `INTERVAL 7 DAY`, sqlmock would not match and the response
	// would become DB_UNAVAILABLE.
	expectDashboardAnalytics(mock, int64(1), " AND a.updated_at >= NOW() - INTERVAL 7 DAY")

	res := doReq(t, app, "GET", "/api/dashboard/analytics", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "7d", data["period"])

	dist := data["star_distribution"].([]any)
	require.Len(t, dist, 4, "rows 1..4 always present, 0-filled")
	third := dist[2].(map[string]any)
	require.Equal(t, float64(3), third["stars"])
	require.Equal(t, float64(5), third["count"])
	require.Equal(t, float64(0), dist[1].(map[string]any)["count"])
	require.Equal(t, float64(0), dist[3].(map[string]any)["count"])

	topics := data["topics"].([]any)
	require.Len(t, topics, 1)
	tc := topics[0].(map[string]any)
	require.Equal(t, float64(9), tc["topic_id"])
	require.Equal(t, "Topik A", tc["name"])
	require.Equal(t, float64(8), tc["assessed"])
	require.Equal(t, float64(12), tc["total"])
	require.Equal(t, 3.6, tc["avg_stars"])

	sessions := data["sessions"].([]any)
	require.Len(t, sessions, 1)
	row := sessions[0].(map[string]any)
	require.Equal(t, float64(66.7), row["percent"])
	require.Equal(t, float64(41), row["session_id"])
	require.Equal(t, "Sesi A", row["name"])
	require.Equal(t, "2026-01-02", row["date"])
	// Analytics ships ONLY the pinned 4 fields — total/assessed are
	// overview-only over-fetch on this route (R1-F03 tier 1).
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	require.ElementsMatch(t, []string{"session_id", "name", "date", "percent"}, keys,
		"analytics sessions wire shape must be exactly the contract's 4 fields")

	facs := data["facilitators"].([]any)
	require.Len(t, facs, 2)
	require.Equal(t, "Fasil", facs[0].(map[string]any)["name"])
	require.Equal(t, float64(7), facs[0].(map[string]any)["assessments"])
	require.Equal(t, "#9", facs[1].(map[string]any)["name"],
		"a deleted assessor renders as #<id>")
}

// TestDashboardAnalyticsAllPeriodHasNoWindow: ?period=all runs every
// aggregate without the updated_at window (fragment-pinned).
func TestDashboardAnalyticsAllPeriodHasNoWindow(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	expectDashboardAnalytics(mock, int64(1), "")

	res := doReq(t, app, "GET", "/api/dashboard/analytics?period=all", "", h)
	data := requireDataEnvelope(t, res).(map[string]any)
	require.Equal(t, "all", data["period"])
	require.Len(t, data["star_distribution"].([]any), 4)
	require.Len(t, data["facilitators"].([]any), 2)
}

// TestDashboardRejectsUnknownPeriod: only 7d/30d/all pass the window switch.
func TestDashboardRejectsUnknownPeriod(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := sessionAdminHeaders(t, s, mock)

	res := doReq(t, app, "GET", "/api/dashboard/analytics?period=1y", "", h)
	requireErrEnvelope(t, res, CodeValidation)
}

// TestDashboardSASchoolOverrideValidatesActive: SA may target any ACTIVE
// school via ?school= (aggregates follow the override); an inactive school
// answers SCHOOL_INACTIVE before any aggregate runs.
func TestDashboardSASchoolOverrideValidatesActive(t *testing.T) {
	t.Run("active override aggregates that school", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := dashboardSAHeaders(t, s, mock)

		mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(7)).
			WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
		mock.ExpectQuery(q("(SELECT COUNT(*) FROM children WHERE school_id = ?)")).
			WithArgs(int64(7), int64(7), int64(7), int64(7), int64(7), int64(7)).
			WillReturnRows(sqlmock.NewRows([]string{"children", "open", "finished",
				"report_links", "narratives", "pending"}).
				AddRow(1, 0, 0, 0, 0, 2))
		mock.ExpectQuery(q("FROM sessions s WHERE s.school_id = ?")).
			WithArgs(int64(7)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "name", "date",
				"members", "activities", "assessed"}))

		res := doReq(t, app, "GET", "/api/dashboard/overview?school=7", "", h)
		data := requireDataEnvelope(t, res).(map[string]any)
		require.Equal(t, float64(1), data["children"])
		require.Equal(t, float64(2), data["pending"])
		require.Empty(t, data["progress"], "school 7 has no sessions yet")
	})

	t.Run("inactive override is school inactive", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := dashboardSAHeaders(t, s, mock)

		mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(7)).
			WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(false))

		res := doReq(t, app, "GET", "/api/dashboard/overview?school=7", "", h)
		requireErrEnvelope(t, res, CodeSchoolInactive)
	})
}

// TestDashboardWithoutSelectedSchoolIsValidation: SA with neither
// X-School-ID nor ?school= refuses an unscoped cross-school aggregate.
func TestDashboardWithoutSelectedSchoolIsValidation(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	h := dashboardSAHeaders(t, s, mock)

	res := doReq(t, app, "GET", "/api/dashboard/overview", "", h)
	requireErrEnvelope(t, res, CodeValidation)
}

// expectSchoolSummary pins the school-summary SQL: every count is a
// school-correlated subquery (children unpinned by is_active — ALL rows;
// reports = narratives + REPORT links with NO revoked_at pin), rows ordered
// id ASC.
func expectSchoolSummary(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectQuery(
		q(`FROM children ch WHERE ch.school_id = t.id`) + `[\s\S]*` +
			q(`FROM sessions so WHERE so.school_id = t.id AND so.status = 'OPENED'`) + `[\s\S]*` +
			q(`FROM sessions sf WHERE sf.school_id = t.id AND sf.status = 'FINISHED'`) + `[\s\S]*` +
			q(`(SELECT COUNT(*) FROM narratives n WHERE n.school_id = t.id)`) +
			` \+ ` +
			q(`(SELECT COUNT(*) FROM public_links pl WHERE pl.school_id = t.id AND pl.kind = 'REPORT')`) + `[\s\S]*` +
			q(`FROM schools t`) + `[\s\S]*` + q(`ORDER BY t.id ASC`)).
		WillReturnRows(rows)
}

func summaryRowCols() []string {
	return []string{"id", "school_name", "is_active",
		"children", "open", "finished", "reports"}
}

// TestDashboardSchoolSummarySAExactShapeAndOrder: SA gets the exact contract
// wire shape (7 fields per row), rows ordered id ASC, inactive schools kept
// with is_active=false; an empty schools table serializes as [] not null.
func TestDashboardSchoolSummarySAExactShapeAndOrder(t *testing.T) {
	t.Run("two schools keep order and exact fields", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := dashboardSAHeaders(t, s, mock)

		expectSchoolSummary(mock, sqlmock.NewRows(summaryRowCols()).
			AddRow(1, "Bandung", true, 120, 3, 15, 42).
			AddRow(2, "Ambon", false, 0, 0, 0, 1))

		res := doReq(t, app, "GET", "/api/dashboard/school-summary", "", h)
		data := requireDataEnvelope(t, res).(map[string]any)
		rows := data["schools"].([]any)
		require.Len(t, rows, 2)

		first := rows[0].(map[string]any)
		keys := make([]string, 0, len(first))
		for k := range first {
			keys = append(keys, k)
		}
		require.ElementsMatch(t, []string{"school_id", "school_name", "is_active",
			"children", "sessions_open", "sessions_finished", "reports_issued"}, keys,
			"school-summary row must be exactly the contract's 7 fields")
		require.Equal(t, map[string]any{
			"school_id":         float64(1),
			"school_name":       "Bandung",
			"is_active":         true,
			"children":          float64(120),
			"sessions_open":     float64(3),
			"sessions_finished": float64(15),
			"reports_issued":    float64(42),
		}, first)

		second := rows[1].(map[string]any)
		require.Equal(t, float64(2), second["school_id"], "rows ordered id ASC")
		require.Equal(t, false, second["is_active"],
			"inactive schools stay listed with is_active=false")
	})

	t.Run("no schools serializes schools as [] not null", func(t *testing.T) {
		s, mock := newTestServer(t)
		app := newTestApp(t, s)
		h := dashboardSAHeaders(t, s, mock)

		expectSchoolSummary(mock, sqlmock.NewRows(summaryRowCols()))

		res := doReq(t, app, "GET", "/api/dashboard/school-summary", "", h)
		data := requireDataEnvelope(t, res).(map[string]any)
		rows, okSchools := data["schools"].([]any)
		require.True(t, okSchools, "schools must serialize as an array, not null")
		require.Empty(t, rows)
	})
}

// TestDashboardSchoolSummaryForbiddenRole: SA-only route — ADMIN and
// FASILITATOR answer FORBIDDEN_ROLE right after the user load (the role
// guard precedes the school guard, so no school/summary query ever runs).
func TestDashboardSchoolSummaryForbiddenRole(t *testing.T) {
	cases := []struct {
		name string
		id   int64
		row  func() *sqlmock.Rows
		u    *userRow
	}{
		{"ADMIN", int64(2), sqlAdminRow, adminUserRow()},
		{"FASILITATOR", int64(3), sqlFASRow, fasUserRow()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestServer(t)
			app := newTestApp(t, s)
			mock.ExpectQuery(sqlUserByID).WithArgs(tc.id).WillReturnRows(tc.row())

			res := doReq(t, app, "GET", "/api/dashboard/school-summary", "",
				bearer(mintAccess(t, s, tc.u)))
			requireErrEnvelope(t, res, CodeForbiddenRole)
		})
	}
}

// dashboard.go — Dashboard domain (F-13, FL-14): overview + analytics for
// SA/ADMIN plus the SA-only per-school summary (FAS has no dashboard route).
// Plain SQL aggregates, school from X-School-ID with the SA ?school= override;
// every query uses the shared 5 s dbCtx budget and DB trouble answers failDB.
// The stats.changed aggregator tick already lives in W1 (stream.go
// startStatsTick) — NOT duplicated here.
//
// # Spec interpretations (reported to the orchestrator)
//
//   - `children` counts ALL school children (active + inactive — the spec
//     says "children" with no active-only pin).
//   - `reports_issued` = active REPORT public_links + narratives rows
//     (activity metric, may double-count — spec'd in F-13).
//   - Analytics period window filters on assessments.updated_at (the
//     assessment's own activity timestamp); `all` = no window.
//   - topics[] aggregates across the session snapshot joins: total = every
//     session_activity instance of that master topic in the school,
//     assessed/avg_stars = windowed assessments only (0-assessed topics
//     stay listed with 0).
//   - facilitators[] LEFT JOINs users; a deleted assessor renders as "#id".
//     Rows with NULL assessor_id are excluded (no identity to attribute).
//   - SA without X-School-ID and without ?school= → VALIDATION (an
//     unscoped cross-school aggregate would mislead).
//   - percent = assessed ÷ (members × activities) × 100, one decimal.
//   - school-summary (SA-only, keputusan #29): one row per schools row —
//     inactive schools INCLUDED with is_active=false; every number is a
//     per-school correlated COUNT (no cross-school mixed aggregate). Unlike
//     the overview, reports_issued here counts REPORT links REGARDLESS of
//     revoked_at (wire shape says kind=REPORT, no active pin).
package internal

import (
	"context"
	"database/sql"
	"math"
	"strconv"

	"github.com/gofiber/fiber/v3"
)

// RegisterDashboard mounts the SA/ADMIN dashboard routes (main.go wires it).
// school-summary is SA-only via the authed("SA") role guard — same pattern as
// the /schools routes; non-SA answers FORBIDDEN_ROLE at the guard (auth.go
// authorize, before any school scoping). No limiter, matching the group.
func (s *Server) RegisterDashboard(api fiber.Router) {
	api.Get("/dashboard/overview", s.authed("ADMIN"), s.handleDashboardOverview)
	api.Get("/dashboard/analytics", s.authed("ADMIN"), s.handleDashboardAnalytics)
	api.Get("/dashboard/school-summary", s.authed("SA"), s.handleDashboardSchoolSummary)
}

// dashboardSchool resolves the aggregate's school: SA may override with
// ?school= (must be an active school, else SCHOOL_INACTIVE); the X-School-ID
// Locals value covers the header case; nothing selected → VALIDATION.
func (s *Server) dashboardSchool(c fiber.Ctx, u *userRow) (int64, bool) {
	school, _ := c.Locals(kvSchool).(int64)
	if u.Role == "SA" {
		var q struct {
			School int64 `query:"school"`
		}
		_ = c.Bind().Query(&q)
		if q.School > 0 {
			ctx, cancel := dbCtx(c)
			defer cancel()
			active, err := s.schoolActive(ctx, q.School)
			if err != nil {
				failDB(c, err)
				return 0, false
			}
			if !active {
				fail(c, CodeSchoolInactive, MsgSchoolInactive)
				return 0, false
			}
			return q.School, true
		}
	}
	if school == 0 { // SA with neither header nor override
		fail(c, CodeValidation, MsgValidation)
		return 0, false
	}
	return school, true
}

// dashboardProgress is the shared per-session progress row.
type dashboardProgress struct {
	SessionID int64   `json:"session_id"`
	Name      string  `json:"name"`
	Date      string  `json:"date"`
	Total     int     `json:"total"`
	Assessed  int     `json:"assessed"`
	Percent   float64 `json:"percent"`
}

// progressRows computes the per-session progress list (members × activities
// vs assessed) in ONE query with correlated counts.
func (s *Server) progressRows(ctx context.Context, school int64) ([]dashboardProgress, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.id, s.name, s.date,
		        (SELECT COUNT(*) FROM session_members m WHERE m.session_id = s.id),
		        (SELECT COUNT(*) FROM session_activities a WHERE a.session_id = s.id),
		        (SELECT COUNT(*) FROM assessments a WHERE a.session_id = s.id)
		 FROM sessions s WHERE s.school_id = ? ORDER BY s.date DESC, s.id`, school)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dashboardProgress{}
	for rows.Next() {
		var p dashboardProgress
		var members, acts, assessed int
		var date sql.NullTime
		if err := rows.Scan(&p.SessionID, &p.Name, &date, &members, &acts, &assessed); err != nil {
			return nil, err
		}
		if date.Valid {
			p.Date = date.Time.Format("2006-01-02")
		}
		p.Total = members * acts
		p.Assessed = assessed
		if p.Total > 0 {
			p.Percent = math.Round(float64(assessed)*1000/float64(p.Total)) / 10
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// handleDashboardOverview — GET /api/dashboard/overview (contracts shape).
func (s *Server) handleDashboardOverview(c fiber.Ctx) error {
	u, _ := c.Locals(kvUser).(*userRow)
	school, okSchool := s.dashboardSchool(c, u)
	if !okSchool {
		return nil
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	var (
		children    int64
		open        int64
		finished    int64
		reportLinks int64
		narratives  int64
		pending     int64
	)
	if err := s.db.QueryRowContext(ctx,
		`SELECT
		   (SELECT COUNT(*) FROM children WHERE school_id = ?),
		   (SELECT COUNT(*) FROM sessions WHERE school_id = ? AND status = 'OPENED'),
		   (SELECT COUNT(*) FROM sessions WHERE school_id = ? AND status = 'FINISHED'),
		   (SELECT COUNT(*) FROM public_links
		     WHERE school_id = ? AND kind = 'REPORT' AND revoked_at IS NULL),
		   (SELECT COUNT(*) FROM narratives WHERE school_id = ?),
		   (SELECT COUNT(*) FROM users WHERE school_id = ? AND decision = 'PENDING')`,
		school, school, school, school, school, school).
		Scan(&children, &open, &finished, &reportLinks, &narratives, &pending); err != nil {
		return failDB(c, err)
	}
	progress, err := s.progressRows(ctx, school)
	if err != nil {
		return failDB(c, err)
	}
	return ok(c, fiber.Map{
		"children":          children,
		"sessions_open":     open,
		"sessions_finished": finished,
		// Activity metric: links + narratives (may double-count, F-13).
		"reports_issued": reportLinks + narratives,
		"pending":        pending,
		"progress":       progress,
	})
}

// handleDashboardAnalytics — GET /api/dashboard/analytics?period=7d|30d|all
func (s *Server) handleDashboardAnalytics(c fiber.Ctx) error {
	u, _ := c.Locals(kvUser).(*userRow)
	school, okSchool := s.dashboardSchool(c, u)
	if !okSchool {
		return nil
	}
	var q struct {
		Period string `query:"period"`
	}
	_ = c.Bind().Query(&q)
	if q.Period == "" {
		q.Period = "7d"
	}
	var win string
	switch q.Period {
	case "7d":
		win = ` AND a.updated_at >= NOW() - INTERVAL 7 DAY`
	case "30d":
		win = ` AND a.updated_at >= NOW() - INTERVAL 30 DAY`
	case "all":
	default:
		return fail(c, CodeValidation, MsgValidation)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	// Star distribution: rows 1..4 always present (0-filled).
	dist := make([]fiber.Map, 0, 4)
	for i := 1; i <= 4; i++ {
		dist = append(dist, fiber.Map{"stars": i, "count": int64(0)})
	}
	dRows, err := s.db.QueryContext(ctx,
		`SELECT stars, COUNT(*) FROM assessments a
		 WHERE a.school_id = ?`+win+` GROUP BY stars`, school)
	if err != nil {
		return failDB(c, err)
	}
	for dRows.Next() {
		var stars int
		var count int64
		if err := dRows.Scan(&stars, &count); err != nil {
			dRows.Close()
			return failDB(c, err)
		}
		if stars >= 1 && stars <= 4 {
			dist[stars-1]["count"] = count
		}
	}
	if err := dRows.Err(); err != nil {
		dRows.Close()
		return failDB(c, err)
	}
	dRows.Close()

	// Topics: totals from the session snapshot joins; windowed assessed/avg.
	type topicAgg struct {
		id       int64
		name     string
		assessed int64
		total    int64
		avg      float64
	}
	byID := map[int64]*topicAgg{}
	tRows, err := s.db.QueryContext(ctx,
		`SELECT st.topic_id, MAX(st.topic_name_snap), COUNT(sa.id)
		 FROM session_topics st
		 LEFT JOIN session_activities sa ON sa.session_topic_id = st.id
		 WHERE st.school_id = ? GROUP BY st.topic_id`, school)
	if err != nil {
		return failDB(c, err)
	}
	for tRows.Next() {
		var t topicAgg
		if err := tRows.Scan(&t.id, &t.name, &t.total); err != nil {
			tRows.Close()
			return failDB(c, err)
		}
		byID[t.id] = &t
	}
	if err := tRows.Err(); err != nil {
		tRows.Close()
		return failDB(c, err)
	}
	tRows.Close()
	wRows, err := s.db.QueryContext(ctx,
		`SELECT st.topic_id, COUNT(*), COALESCE(AVG(a.stars), 0)
		 FROM assessments a
		 JOIN session_activities sa ON sa.id = a.session_activity_id
		 JOIN session_topics st ON st.id = sa.session_topic_id
		 WHERE a.school_id = ?`+win+` GROUP BY st.topic_id`, school)
	if err != nil {
		return failDB(c, err)
	}
	for wRows.Next() {
		var id int64
		var assessed int64
		var avg float64
		if err := wRows.Scan(&id, &assessed, &avg); err != nil {
			wRows.Close()
			return failDB(c, err)
		}
		if t := byID[id]; t != nil {
			t.assessed, t.avg = assessed, avg
		}
	}
	if err := wRows.Err(); err != nil {
		wRows.Close()
		return failDB(c, err)
	}
	wRows.Close()
	topics := make([]fiber.Map, 0, len(byID))
	for _, t := range byID {
		topics = append(topics, fiber.Map{
			"topic_id":  t.id,
			"name":      t.name,
			"assessed":  t.assessed,
			"total":     t.total,
			"avg_stars": math.Round(t.avg*10) / 10,
		})
	}

	// Sessions progress (same computation as the overview), narrowed to the
	// contract's analytics wire shape {session_id,name,date,percent} —
	// total/assessed stay overview-only (R1-F03 tier 1).
	progress, err := s.progressRows(ctx, school)
	if err != nil {
		return failDB(c, err)
	}
	sessions := make([]fiber.Map, 0, len(progress))
	for _, p := range progress {
		sessions = append(sessions, fiber.Map{
			"session_id": p.SessionID,
			"name":       p.Name,
			"date":       p.Date,
			"percent":    p.Percent,
		})
	}

	// Facilitator activity: windowed assessments per assessor; deleted users
	// fall back to "#<id>" (assessments carry no assessor name snap).
	facilitators := []fiber.Map{}
	fRows, err := s.db.QueryContext(ctx,
		`SELECT a.assessor_id, u.name, COUNT(*)
		 FROM assessments a LEFT JOIN users u ON u.id = a.assessor_id
		 WHERE a.school_id = ? AND a.assessor_id IS NOT NULL`+win+
			` GROUP BY a.assessor_id, u.name
		 ORDER BY COUNT(*) DESC, a.assessor_id ASC`, school)
	if err != nil {
		return failDB(c, err)
	}
	for fRows.Next() {
		var id int64
		var name sql.NullString
		var count int64
		if err := fRows.Scan(&id, &name, &count); err != nil {
			fRows.Close()
			return failDB(c, err)
		}
		display := "#" + strconv.FormatInt(id, 10)
		if name.Valid {
			display = name.String
		}
		facilitators = append(facilitators, fiber.Map{
			"id": id, "name": display, "assessments": count,
		})
	}
	if err := fRows.Err(); err != nil {
		fRows.Close()
		return failDB(c, err)
	}
	fRows.Close()

	return ok(c, fiber.Map{
		"period":            q.Period,
		"star_distribution": dist,
		"topics":            topics,
		"sessions":          sessions,
		"facilitators":      facilitators,
	})
}

// dashboardSchoolSummaryRow is one row of the SA-only per-school summary
// (wire shape, keputusan #29 / 02 §2b): school identity + its four counts.
type dashboardSchoolSummaryRow struct {
	SchoolID         int64  `json:"school_id"`
	SchoolName       string `json:"school_name"`
	IsActive         bool   `json:"is_active"`
	Children         int64  `json:"children"`
	SessionsOpen     int64  `json:"sessions_open"`
	SessionsFinished int64  `json:"sessions_finished"`
	ReportsIssued    int64  `json:"reports_issued"`
}

// handleDashboardSchoolSummary — GET /api/dashboard/school-summary (SA-only,
// keputusan #29 / 02 §2b, F-13). One row per schools row — inactive schools
// INCLUDED with is_active=false — ordered id ASC; every number is a
// per-school correlated COUNT (NO cross-school mixed aggregate, 08 AC).
// Single query with correlated subqueries on school-leading indexes, under
// the shared 5 s dbCtx budget.
func (s *Server) handleDashboardSchoolSummary(c fiber.Ctx) error {
	ctx, cancel := dbCtx(c)
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.id, t.school_name, t.is_active,
		        (SELECT COUNT(*) FROM children ch WHERE ch.school_id = t.id),
		        (SELECT COUNT(*) FROM sessions so WHERE so.school_id = t.id AND so.status = 'OPENED'),
		        (SELECT COUNT(*) FROM sessions sf WHERE sf.school_id = t.id AND sf.status = 'FINISHED'),
		        (SELECT COUNT(*) FROM narratives n WHERE n.school_id = t.id)
		            + (SELECT COUNT(*) FROM public_links pl WHERE pl.school_id = t.id AND pl.kind = 'REPORT')
		 FROM schools t
		 ORDER BY t.id ASC`)
	if err != nil {
		return failDB(c, err)
	}
	defer rows.Close()
	schools := make([]dashboardSchoolSummaryRow, 0, 8)
	for rows.Next() {
		var t dashboardSchoolSummaryRow
		if err := rows.Scan(&t.SchoolID, &t.SchoolName, &t.IsActive,
			&t.Children, &t.SessionsOpen, &t.SessionsFinished, &t.ReportsIssued); err != nil {
			return failDB(c, err)
		}
		schools = append(schools, t)
	}
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	return ok(c, fiber.Map{"schools": schools})
}

package internal

import (
	"context"
	"database/sql"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// activities.go — activity master [SA/ADMIN] (02 §2b, F-04, FL-04). Plain
// JSON (no upload): program*+topic* on create, both immutable afterwards so
// program_name_snap / topic_name_snap stay true.

// activityItem is the Activity wire object (contracts-shapes.md).
type activityItem struct {
	ID          int64   `json:"id"`
	ProgramID   int64   `json:"program_id"`
	TopicID     int64   `json:"topic_id"`
	ProgramName string  `json:"program_name"` // program_name_snap
	TopicName   string  `json:"topic_name"`   // topic_name_snap
	Name        string  `json:"name"`
	Indicator   *string `json:"indicator"`
	SortOrder   int     `json:"sort_order"`
}

const activitySelect = `SELECT id, program_id, topic_id, program_name_snap, topic_name_snap,
	name, indicator, sort_order FROM activities `

func scanActivity(sc scanner) (activityItem, error) {
	var it activityItem
	var indicator sql.NullString
	if err := sc.Scan(&it.ID, &it.ProgramID, &it.TopicID, &it.ProgramName, &it.TopicName,
		&it.Name, &indicator, &it.SortOrder); err != nil {
		return activityItem{}, err
	}
	it.Indicator = nullStr(indicator)
	return it, nil
}

func (s *Server) activityItemByID(ctx context.Context, id, school int64) (*activityItem, error) {
	it, err := scanActivity(s.db.QueryRowContext(ctx,
		activitySelect+`WHERE id = ? AND school_id = ?`, id, school))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// RegisterActivities mounts the activity routes (F-04).
func (s *Server) RegisterActivities(api fiber.Router) {
	api.Get("/activities", s.authed("ADMIN"), s.listActivities)
	api.Post("/activities", s.authed("ADMIN"), s.createActivity)
	api.Get("/activities/:id", s.authed("ADMIN"), s.getActivity)
	api.Patch("/activities/:id", s.authed("ADMIN"), s.patchActivity)
	api.Delete("/activities/:id", s.authed("ADMIN"), s.deleteActivity)
}

func (s *Server) listActivities(c fiber.Ctx) error {
	p, okList := listParams(c, "sort_order", "asc", map[string]string{
		"sort_order": "sort_order", "name": "name", "id": "id",
	})
	if !okList {
		return nil // VALIDATION already written by listParams
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	topicID, topicSet, okF := queryIDFilter(c, "topic_id")
	if !okF {
		return nil
	}
	programID, programSet, okF := queryIDFilter(c, "program_id")
	if !okF {
		return nil
	}
	where := "WHERE school_id = ?"
	args := []any{school}
	if topicSet {
		where += " AND topic_id = ?"
		args = append(args, topicID)
	}
	if programSet {
		where += " AND program_id = ?"
		args = append(args, programID)
	}
	if p.Q != "" {
		where += " AND name LIKE ?"
		args = append(args, "%"+p.Q+"%")
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM activities "+where, args...).Scan(&total); err != nil {
		return failDB(c, err)
	}
	rows, err := s.db.QueryContext(ctx,
		activitySelect+where+` ORDER BY `+p.OrderBy("id")+` LIMIT ? OFFSET ?`,
		append(args, p.PerPage, p.Offset)...)
	if err != nil {
		return failDB(c, err)
	}
	defer rows.Close()

	out := make([]activityItem, 0, p.PerPage)
	for rows.Next() {
		it, err := scanActivity(rows)
		if err != nil {
			return failDB(c, err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	return okPaged(c, out, p.Page, p.PerPage, total)
}

// createActivity: program*+topic* (unknown/cross-school → NOT_FOUND; topic
// outside the given program → VALIDATION); sort_order omitted = append in the
// topic (F-04).
func (s *Server) createActivity(c fiber.Ctx) error {
	var req struct {
		ProgramID int64   `json:"program_id"`
		TopicID   int64   `json:"topic_id"`
		Name      string  `json:"name"`
		Indicator *string `json:"indicator"`
		SortOrder *int    `json:"sort_order"`
	}
	if !bindBody(c, &req) {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.ProgramID <= 0 || req.TopicID <= 0 || req.Name == "" || len(req.Name) > maxShortTextLen {
		return fail(c, CodeValidation, MsgValidation)
	}
	sortOrder := -1 // -1 → default append
	if req.SortOrder != nil {
		if *req.SortOrder < 0 {
			return fail(c, CodeValidation, MsgValidation)
		}
		sortOrder = *req.SortOrder
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	var programName string
	if err := s.db.QueryRowContext(ctx,
		`SELECT name FROM programs WHERE id = ? AND school_id = ?`, req.ProgramID, school).
		Scan(&programName); err != nil {
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		return failDB(c, err)
	}
	var topicName string
	var topicProgram int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT name, program_id FROM topics WHERE id = ? AND school_id = ?`, req.TopicID, school).
		Scan(&topicName, &topicProgram); err != nil {
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		return failDB(c, err)
	}
	if topicProgram != req.ProgramID {
		return fail(c, CodeValidation, MsgValidation) // topic belongs to another program
	}
	if sortOrder < 0 {
		if err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sort_order), 0) + 1 FROM activities
			 WHERE topic_id = ? AND school_id = ?`, req.TopicID, school).
			Scan(&sortOrder); err != nil {
			return failDB(c, err)
		}
	}
	var indicator any
	if req.Indicator != nil && *req.Indicator != "" {
		indicator = *req.Indicator
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO activities (school_id, program_id, topic_id, program_name_snap, topic_name_snap,
			name, indicator, sort_order) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		school, req.ProgramID, req.TopicID, programName, topicName, req.Name, indicator, sortOrder)
	if err != nil {
		return failDB(c, err)
	}
	id, _ := res.LastInsertId()
	it, err := s.activityItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil { // raced concurrent delete after commit
		return fail(c, CodeNotFound, MsgNotFound)
	}
	s.publish(school, Frame{Entity: "activity.changed", EntityID: id, Action: "created"})
	return ok(c, fiber.Map{"activity": it})
}

func (s *Server) getActivity(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	it, err := s.activityItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	return ok(c, fiber.Map{"activity": it})
}

// patchActivity: name/indicator/sort_order only (program/topic immutable).
func (s *Server) patchActivity(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	var req struct {
		Name      *string `json:"name"`
		Indicator *string `json:"indicator"`
		SortOrder *int    `json:"sort_order"`
	}
	if !bindBody(c, &req) {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	cur, err := s.activityItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if cur == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}

	name := cur.Name
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" || len(name) > maxShortTextLen {
			return fail(c, CodeValidation, MsgValidation)
		}
	}
	indicator := cur.Indicator
	if req.Indicator != nil {
		if *req.Indicator == "" {
			indicator = nil
		} else {
			indicator = req.Indicator
		}
	}
	sortOrder := cur.SortOrder
	if req.SortOrder != nil {
		if *req.SortOrder < 0 {
			return fail(c, CodeValidation, MsgValidation)
		}
		sortOrder = *req.SortOrder
	}
	var indicatorVal any
	if indicator != nil {
		indicatorVal = *indicator
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE activities SET name = ?, indicator = ?, sort_order = ?
		 WHERE id = ? AND school_id = ?`,
		name, indicatorVal, sortOrder, id, school); err != nil {
		return failDB(c, err)
	}
	it, err := s.activityItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil { // raced concurrent delete after commit
		return fail(c, CodeNotFound, MsgNotFound)
	}
	s.publish(school, Frame{Entity: "activity.changed", EntityID: id, Action: "updated"})
	return ok(c, fiber.Map{"activity": it})
}

// deleteActivity: single row, no dependants (sessions keep their snaps).
func (s *Server) deleteActivity(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	cur, err := s.activityItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if cur == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM activities WHERE id = ? AND school_id = ?`, id, school); err != nil {
		return failDB(c, err)
	}
	s.publish(school, Frame{Entity: "activity.changed", EntityID: id, Action: "deleted"})
	return ok(c, fiber.Map{"deleted": true})
}

package internal

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// topics.go — topic master [SA/ADMIN] (02 §2b, F-04, FL-04): multipart CRUD
// under a program, sort_order default-append, badge pair via resolveBadge.
// program_id is immutable on PATCH (program_name_snap must stay true).

// topicItem is the Topic wire object (contracts-shapes.md).
type topicItem struct {
	ID            int64   `json:"id"`
	ProgramID     int64   `json:"program_id"`
	ProgramName   string  `json:"program_name"` // program_name_snap
	Name          string  `json:"name"`
	Description   *string `json:"description"`
	SortOrder     int     `json:"sort_order"`
	BadgeName     *string `json:"badge_name"`
	BadgeImageURL *string `json:"badge_image_url"`
	IsActive      bool    `json:"is_active"`

	badgePath string
}

const topicSelect = `SELECT id, program_id, program_name_snap, name, description,
	sort_order, badge_name, badge_image_path, is_active FROM topics `

func scanTopic(sc scanner) (topicItem, error) {
	var it topicItem
	var desc, badgeName, badgePath sql.NullString
	if err := sc.Scan(&it.ID, &it.ProgramID, &it.ProgramName, &it.Name, &desc,
		&it.SortOrder, &badgeName, &badgePath, &it.IsActive); err != nil {
		return topicItem{}, err
	}
	it.Description = nullStr(desc)
	it.BadgeName = nullStr(badgeName)
	if badgePath.Valid {
		it.badgePath = badgePath.String
		it.BadgeImageURL = new("/uploads/" + badgePath.String)
	}
	return it, nil
}

func (s *Server) topicItemByID(ctx context.Context, id, school int64) (*topicItem, error) {
	it, err := scanTopic(s.db.QueryRowContext(ctx,
		topicSelect+`WHERE id = ? AND school_id = ?`, id, school))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// RegisterTopics mounts the topic routes (F-04).
func (s *Server) RegisterTopics(api fiber.Router) {
	api.Get("/topics", s.authed("ADMIN"), s.listTopics)
	api.Post("/topics", s.authed("ADMIN"), s.createTopic)
	api.Get("/topics/:id", s.authed("ADMIN"), s.getTopic)
	api.Patch("/topics/:id", s.authed("ADMIN"), s.patchTopic)
	api.Delete("/topics/:id", s.authed("ADMIN"), s.deleteTopic)
}

func (s *Server) listTopics(c fiber.Ctx) error {
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
	programID, found, okF := queryIDFilter(c, "program_id")
	if !okF {
		return nil
	}
	where := "WHERE school_id = ?"
	args := []any{school}
	if found {
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
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM topics "+where, args...).Scan(&total); err != nil {
		return failDB(c, err)
	}
	rows, err := s.db.QueryContext(ctx,
		topicSelect+where+` ORDER BY `+p.OrderBy("id")+` LIMIT ? OFFSET ?`,
		append(args, p.PerPage, p.Offset)...)
	if err != nil {
		return failDB(c, err)
	}
	defer rows.Close()

	out := make([]topicItem, 0, p.PerPage)
	for rows.Next() {
		it, err := scanTopic(rows)
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

// createTopic: program* must exist in the school (else NOT_FOUND, no leak);
// sort_order omitted = append after the program's current max (F-04).
func (s *Server) createTopic(c fiber.Ctx) error {
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	form, okF := masterForm(c)
	if !okF {
		return nil
	}
	name, nameOK := formField(form, "name")
	name = strings.TrimSpace(name)
	if !nameOK || name == "" || len(name) > maxShortTextLen {
		return fail(c, CodeValidation, MsgValidation)
	}
	rawProgram, present := formField(form, "program_id")
	programID, pErr := strconv.ParseInt(rawProgram, 10, 64)
	if !present || pErr != nil || programID <= 0 {
		return fail(c, CodeValidation, MsgValidation)
	}
	sortOrder := -1 // -1 → default append
	if v, has := formField(form, "sort_order"); has {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 0 {
			return fail(c, CodeValidation, MsgValidation)
		}
		sortOrder = n
	}
	active := true
	if v, has := formField(form, "is_active"); has {
		b, convErr := strconv.ParseBool(v)
		if convErr != nil {
			return fail(c, CodeValidation, MsgValidation)
		}
		active = b
	}
	bName, bPath, okB := "", "", false
	var err error

	ctx, cancel := dbCtx(c)
	defer cancel()
	var programName string
	if err := s.db.QueryRowContext(ctx,
		`SELECT name FROM programs WHERE id = ? AND school_id = ?`, programID, school).
		Scan(&programName); err != nil {
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		return failDB(c, err)
	}
	if sortOrder < 0 {
		if err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sort_order), 0) + 1 FROM topics
			 WHERE program_id = ? AND school_id = ?`, programID, school).
			Scan(&sortOrder); err != nil {
			return failDB(c, err)
		}
	}
	// Store the badge only after the parent checks passed (no orphan file on
	// NOT_FOUND).
	bName, bPath, okB, err = s.resolveBadge(c, school, form, "", "")
	if err != nil {
		return err
	}
	if !okB {
		return nil
	}

	desc, _ := formField(form, "description")
	var descVal, badgeNameVal, badgePathVal any
	if desc != "" {
		descVal = desc
	}
	if bName != "" {
		badgeNameVal, badgePathVal = bName, bPath
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO topics (school_id, program_id, program_name_snap, name, description,
			sort_order, badge_name, badge_image_path, is_active)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		school, programID, programName, name, descVal, sortOrder, badgeNameVal, badgePathVal, active)
	if err != nil {
		return failDB(c, err)
	}
	id, _ := res.LastInsertId()
	it, err := s.topicItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil { // raced concurrent delete after commit
		return fail(c, CodeNotFound, MsgNotFound)
	}
	s.publish(school, Frame{Entity: "topic.changed", EntityID: id, Action: "created"})
	return ok(c, fiber.Map{"topic": it})
}

func (s *Server) getTopic(c fiber.Ctx) error {
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
	it, err := s.topicItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	return ok(c, fiber.Map{"topic": it})
}

// patchTopic: partial multipart update — omitted fields untouched, badge via
// resolveBadge (clear = badge_name:"" with no file).
func (s *Server) patchTopic(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	form, okF := masterForm(c)
	if !okF {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	cur, err := s.topicItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if cur == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}

	name := cur.Name
	if v, present := formField(form, "name"); present {
		name = strings.TrimSpace(v)
		if name == "" || len(name) > maxShortTextLen {
			return fail(c, CodeValidation, MsgValidation)
		}
	}
	desc := cur.Description
	if v, present := formField(form, "description"); present {
		if v == "" {
			desc = nil
		} else {
			d := v
			desc = &d
		}
	}
	sortOrder := cur.SortOrder
	if v, present := formField(form, "sort_order"); present {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 0 {
			return fail(c, CodeValidation, MsgValidation)
		}
		sortOrder = n
	}
	active := cur.IsActive
	if v, present := formField(form, "is_active"); present {
		b, convErr := strconv.ParseBool(v)
		if convErr != nil {
			return fail(c, CodeValidation, MsgValidation)
		}
		active = b
	}
	existingName, existingPath := "", cur.badgePath
	if cur.BadgeName != nil {
		existingName = *cur.BadgeName
	}
	bName, bPath, okB, err := s.resolveBadge(c, school, form, existingName, existingPath)
	if err != nil {
		return err
	}
	if !okB {
		return nil
	}

	var descVal, badgeNameVal, badgePathVal any
	if desc != nil {
		descVal = *desc
	}
	if bName != "" {
		badgeNameVal, badgePathVal = bName, bPath
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE topics SET name = ?, description = ?, sort_order = ?,
			badge_name = ?, badge_image_path = ?, is_active = ?
		 WHERE id = ? AND school_id = ?`,
		name, descVal, sortOrder, badgeNameVal, badgePathVal, active, id, school); err != nil {
		return failDB(c, err)
	}
	if bPath != cur.badgePath {
		s.removeMasterImage(ctx, cur.badgePath)
	}
	it, err := s.topicItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil { // raced concurrent delete after commit
		return fail(c, CodeNotFound, MsgNotFound)
	}
	s.publish(school, Frame{Entity: "topic.changed", EntityID: id, Action: "updated"})
	return ok(c, fiber.Map{"topic": it})
}

// deleteTopic: one Tx removes the topic + its mission link rows (sessions keep
// their snapshots — F-04); the badge file goes after commit (share-checked).
// Post-commit publishes topic.changed plus one mission.changed — the
// mission_topics link sets changed (02 §2c).
func (s *Server) deleteTopic(c fiber.Ctx) error {
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
	cur, err := s.topicItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if cur == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM mission_topics WHERE topic_id = ? AND school_id = ?`, id, school); err != nil {
		return failDB(c, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM topics WHERE id = ? AND school_id = ?`, id, school); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}
	s.removeMasterImage(ctx, cur.badgePath)
	s.publish(school, Frame{Entity: "topic.changed", EntityID: id, Action: "deleted"})
	// The Tx also dropped this topic's mission_topics link rows; one
	// mission.changed (id 0 — no single mission changed, the link sets did)
	// refetches open mission pickers (08 §realtime, 02 §2c).
	s.publish(school, Frame{Entity: "mission.changed", EntityID: 0, Action: "updated"})
	return ok(c, fiber.Map{"deleted": true})
}

package internal

import (
	"context"
	"database/sql"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// missions.go — mission bank [SA/ADMIN] (02 §2b, F-05, FL-05): plain JSON,
// topic_ids[1+] linked via mission_topics (same school, any program), PATCH
// may re-link topics / reassign the program. report_missions picks are session
// data and are never touched here.

// missionTopicRef is one mission↔topic link (mission_topics snaps).
type missionTopicRef struct {
	TopicID int64  `json:"topic_id"`
	Name    string `json:"name"` // topic_name_snap
}

// missionItem is the Mission wire object (contracts-shapes.md).
type missionItem struct {
	ID          int64             `json:"id"`
	ProgramID   int64             `json:"program_id"`
	ProgramName string            `json:"program_name"` // program_name_snap
	Name        string            `json:"name"`
	Body        string            `json:"body"`
	Topics      []missionTopicRef `json:"topics"`
}

const missionSelect = `SELECT m.id, m.program_id, m.program_name_snap, m.name, m.body FROM missions m `

func (s *Server) missionItemByID(ctx context.Context, id, school int64) (*missionItem, error) {
	var it missionItem
	err := s.db.QueryRowContext(ctx,
		missionSelect+`WHERE m.id = ? AND m.school_id = ?`, id, school).
		Scan(&it.ID, &it.ProgramID, &it.ProgramName, &it.Name, &it.Body)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// missionTopics loads the link rows for one page of missions in a single
// query keyed by mission_id.
func (s *Server) missionTopics(ctx context.Context, school int64, ids []int64) (map[int64][]missionTopicRef, error) {
	out := make(map[int64][]missionTopicRef, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, school)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT mission_id, topic_id, topic_name_snap FROM mission_topics
		 WHERE school_id = ? AND mission_id IN (`+ph+`) ORDER BY topic_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m missionTopicRef
		var missionID int64
		if err := rows.Scan(&missionID, &m.TopicID, &m.Name); err != nil {
			return nil, err
		}
		out[missionID] = append(out[missionID], m)
	}
	return out, rows.Err()
}

// setMissionTopics applies the loaded map so `topics` is never JSON null.
func setMissionTopics(it *missionItem, m map[int64][]missionTopicRef) {
	if t := m[it.ID]; len(t) > 0 {
		it.Topics = t
	} else {
		it.Topics = []missionTopicRef{}
	}
}

// validateMissionTopics dedupes topic_ids, enforces ≥1 and loads every topic
// in the school (unknown/cross-school → NOT_FOUND, no leak). false = a
// failure envelope was written.
func (s *Server) validateMissionTopics(ctx context.Context, c fiber.Ctx, school int64, ids []int64) ([]missionTopicRef, bool) {
	seen := make(map[int64]bool, len(ids))
	uniq := ids[:0]
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) == 0 {
		_ = fail(c, CodeValidation, MsgValidation)
		return nil, false
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(uniq)), ",")
	args := make([]any, 0, len(uniq)+1)
	args = append(args, school)
	for _, id := range uniq {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name FROM topics WHERE school_id = ? AND id IN (`+ph+`)`, args...)
	if err != nil {
		_ = failDB(c, err)
		return nil, false
	}
	defer rows.Close()
	byID := make(map[int64]string, len(uniq))
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			_ = failDB(c, err)
			return nil, false
		}
		byID[id] = name
	}
	if err := rows.Err(); err != nil {
		_ = failDB(c, err)
		return nil, false
	}
	if len(byID) != len(uniq) {
		_ = fail(c, CodeNotFound, MsgNotFound)
		return nil, false
	}
	out := make([]missionTopicRef, 0, len(uniq))
	for _, id := range uniq {
		out = append(out, missionTopicRef{TopicID: id, Name: byID[id]})
	}
	return out, true
}

// RegisterMissions mounts the mission-bank routes (F-05).
func (s *Server) RegisterMissions(api fiber.Router) {
	api.Get("/missions", s.authed("ADMIN"), s.listMissions)
	api.Post("/missions", s.authed("ADMIN"), s.createMission)
	api.Get("/missions/:id", s.authed("ADMIN"), s.getMission)
	api.Patch("/missions/:id", s.authed("ADMIN"), s.patchMission)
	api.Delete("/missions/:id", s.authed("ADMIN"), s.deleteMission)
}

func (s *Server) listMissions(c fiber.Ctx) error {
	p, okList := listParams(c, "m.id", "asc", map[string]string{
		"id": "m.id", "name": "m.name", "created_at": "m.created_at",
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
	where := "WHERE m.school_id = ?"
	args := []any{school}
	if topicSet {
		where += ` AND EXISTS (SELECT 1 FROM mission_topics mt
			WHERE mt.mission_id = m.id AND mt.topic_id = ?)`
		args = append(args, topicID)
	}
	if programSet {
		where += " AND m.program_id = ?"
		args = append(args, programID)
	}
	if p.Q != "" {
		where += " AND m.name LIKE ?"
		args = append(args, "%"+p.Q+"%")
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM missions m "+where, args...).Scan(&total); err != nil {
		return failDB(c, err)
	}
	rows, err := s.db.QueryContext(ctx,
		missionSelect+where+` ORDER BY `+p.OrderBy("m.id")+` LIMIT ? OFFSET ?`,
		append(args, p.PerPage, p.Offset)...)
	if err != nil {
		return failDB(c, err)
	}
	out := make([]missionItem, 0, p.PerPage)
	var ids []int64
	for rows.Next() {
		var it missionItem
		if err := rows.Scan(&it.ID, &it.ProgramID, &it.ProgramName, &it.Name, &it.Body); err != nil {
			rows.Close()
			return failDB(c, err)
		}
		out = append(out, it)
		ids = append(ids, it.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	topicMap, err := s.missionTopics(ctx, school, ids)
	if err != nil {
		return failDB(c, err)
	}
	for i := range out {
		setMissionTopics(&out[i], topicMap)
	}
	return okPaged(c, out, p.Page, p.PerPage, total)
}

// createMission: program* + name* + body* + topic_ids[1+] (same school, any
// program — F-05); links land in the same Tx as the row.
func (s *Server) createMission(c fiber.Ctx) error {
	var req struct {
		ProgramID int64   `json:"program_id"`
		Name      string  `json:"name"`
		Body      string  `json:"body"`
		TopicIDs  []int64 `json:"topic_ids"`
	}
	if !bindBody(c, &req) {
		return nil
	}
	school, okT := requireSchool(c)
	if !okT {
		return nil
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Body = strings.TrimSpace(req.Body)
	if req.ProgramID <= 0 || req.Name == "" || len(req.Name) > maxShortTextLen || req.Body == "" {
		return fail(c, CodeValidation, MsgValidation)
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
	topics, okV := s.validateMissionTopics(ctx, c, school, req.TopicIDs)
	if !okV {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO missions (school_id, program_id, program_name_snap, name, body)
		 VALUES (?, ?, ?, ?, ?)`, school, req.ProgramID, programName, req.Name, req.Body)
	if err != nil {
		return failDB(c, err)
	}
	id, _ := res.LastInsertId()
	for _, t := range topics {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO mission_topics (mission_id, topic_id, topic_name_snap, school_id)
			 VALUES (?, ?, ?, ?)`, id, t.TopicID, t.Name, school); err != nil {
			return failDB(c, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	it, err := s.missionItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil { // raced concurrent delete after commit
		return fail(c, CodeNotFound, MsgNotFound)
	}
	topicMap, err := s.missionTopics(ctx, school, []int64{id})
	if err != nil {
		return failDB(c, err)
	}
	setMissionTopics(it, topicMap)
	s.publish(school, Frame{Entity: "mission.changed", EntityID: id, Action: "created"})
	return ok(c, fiber.Map{"mission": it})
}

func (s *Server) getMission(c fiber.Ctx) error {
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
	it, err := s.missionItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil {
		return fail(c, CodeNotFound, MsgNotFound)
	}
	topicMap, err := s.missionTopics(ctx, school, []int64{id})
	if err != nil {
		return failDB(c, err)
	}
	setMissionTopics(it, topicMap)
	return ok(c, fiber.Map{"mission": it})
}

// patchMission: partial update; topic_ids (when present) replaces the whole
// link set in one Tx; program_id reassigns + refreshes the name snap.
func (s *Server) patchMission(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	var req struct {
		Name      *string  `json:"name"`
		Body      *string  `json:"body"`
		ProgramID *int64   `json:"program_id"`
		TopicIDs  *[]int64 `json:"topic_ids"`
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
	cur, err := s.missionItemByID(ctx, id, school)
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
	body := cur.Body
	if req.Body != nil {
		body = strings.TrimSpace(*req.Body)
		if body == "" {
			return fail(c, CodeValidation, MsgValidation)
		}
	}
	programID := cur.ProgramID
	programName := cur.ProgramName
	if req.ProgramID != nil {
		if *req.ProgramID <= 0 {
			return fail(c, CodeValidation, MsgValidation)
		}
		if err := s.db.QueryRowContext(ctx,
			`SELECT name FROM programs WHERE id = ? AND school_id = ?`, *req.ProgramID, school).
			Scan(&programName); err != nil {
			if err == sql.ErrNoRows {
				return fail(c, CodeNotFound, MsgNotFound)
			}
			return failDB(c, err)
		}
		programID = *req.ProgramID
	}
	var topics []missionTopicRef
	if req.TopicIDs != nil {
		var okV bool
		topics, okV = s.validateMissionTopics(ctx, c, school, *req.TopicIDs)
		if !okV {
			return nil
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE missions SET program_id = ?, program_name_snap = ?, name = ?, body = ?
		 WHERE id = ? AND school_id = ?`,
		programID, programName, name, body, id, school); err != nil {
		return failDB(c, err)
	}
	if topics != nil {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM mission_topics WHERE mission_id = ? AND school_id = ?`, id, school); err != nil {
			return failDB(c, err)
		}
		for _, t := range topics {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO mission_topics (mission_id, topic_id, topic_name_snap, school_id)
				 VALUES (?, ?, ?, ?)`, id, t.TopicID, t.Name, school); err != nil {
				return failDB(c, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	it, err := s.missionItemByID(ctx, id, school)
	if err != nil {
		return failDB(c, err)
	}
	if it == nil { // raced concurrent delete after commit
		return fail(c, CodeNotFound, MsgNotFound)
	}
	topicMap, err := s.missionTopics(ctx, school, []int64{id})
	if err != nil {
		return failDB(c, err)
	}
	setMissionTopics(it, topicMap)
	s.publish(school, Frame{Entity: "mission.changed", EntityID: id, Action: "updated"})
	return ok(c, fiber.Map{"mission": it})
}

// deleteMission: row + its mission_topics links in one Tx; existing
// report_missions picks keep their snaps (F-05 "picks snapped").
func (s *Server) deleteMission(c fiber.Ctx) error {
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
	cur, err := s.missionItemByID(ctx, id, school)
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
		`DELETE FROM mission_topics WHERE mission_id = ? AND school_id = ?`, id, school); err != nil {
		return failDB(c, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM missions WHERE id = ? AND school_id = ?`, id, school); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}
	s.publish(school, Frame{Entity: "mission.changed", EntityID: id, Action: "deleted"})
	return ok(c, fiber.Map{"deleted": true})
}

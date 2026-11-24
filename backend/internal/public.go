// public.go — Public JSON domain (F-10/F-12/F-15, FL-11/15): no auth, token
// scope only. Every token failure is the IDENTICAL 200 TOKEN_INVALID envelope
// (unknown/revoked/malformed — no oracle). Limiters: 60/min per IP+token on
// the three GETs, 10/min per IP+token on the respond POST (a separate route
// so the generic GET bucket never double-counts it).
//
// # Spec interpretations (reported to the orchestrator)
//
//   - Public stream wiring: W1's publicStreamGuard resolves ?token= with its
//     OWN SQL against public_links/consent_links (stream.go publicScope) —
//     no provider seam exists, so RegisterPublic wires NOTHING for SSE.
//     The REST token endpoints resolve through the SAME publicScope via the
//     resolveLink wrapper (003b): one token→scope authority for both
//     transports, no duplicated token SELECTs.
//   - Inactive child: the report still renders (deactivation revokes no
//     links — spec); the gallery locks with CHILD_INACTIVE (wins over
//     consent). Consent respond is still recorded first-wins on an inactive
//     child (06 §5).
//   - DITOLAK sequence: the first-wins decision UPDATE runs first, then B2's
//     wipeChildSessionPhotos (its own Tx), then file unlink, then publish.
//     A wipe failure leaves rows+files behind while the gallery is already
//     locked (acceptable per orchestrator pin — slog'd, never announced):
//     photo.changed/gallery.changed are gated on a successful wipe, while
//     consent.changed/consent.ack always fire (the decision IS recorded).
//     SETUJU changes nothing else (gallery unlocks on next read).
//   - consent.changed / consent.ack / photo.changed / gallery.changed fire
//     only on a RECORDED decision; an already-decided repeat answers
//     {decision, already:true} with no events (no state changed).
package internal

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

// keyIPBodyToken mirrors keyIPEmail for the JSON-body token of
// POST /public/consent/respond (schools share one public IP).
func keyIPBodyToken(c fiber.Ctx) string {
	var body struct {
		Token string `json:"token"`
	}
	_ = c.Bind().Body(&body)
	return c.IP() + "|" + body.Token
}

// RegisterPublic mounts the token-scoped public routes (main.go wires it).
func (s *Server) RegisterPublic(pub fiber.Router) {
	pub.Get("/report", newLimiter(60, time.Minute, keyIPQuery("token")), s.handlePublicReport)
	pub.Get("/gallery", newLimiter(60, time.Minute, keyIPQuery("token")), s.handlePublicGallery)
	pub.Get("/consent", newLimiter(60, time.Minute, keyIPQuery("token")), s.handlePublicConsent)
	pub.Post("/consent/respond",
		newLimiter(10, time.Minute, keyIPBodyToken), s.handleConsentRespond)
}

// queryToken reads ?token=; false = TOKEN_INVALID already written.
func queryToken(c fiber.Ctx) (string, bool) {
	var q struct {
		Token string `query:"token"`
	}
	if err := c.Bind().Query(&q); err != nil || q.Token == "" {
		fail(c, CodeTokenInvalid, MsgTokenInvalid)
		return "", false
	}
	return q.Token, true
}

// resolveLink is public.go's single token→scope gate (003b): it wraps
// stream.go's publicScope — the ONE token→scope authority behind both
// /public/stream and every REST token endpoint, so the SSE filter and the
// REST gate cannot drift (decision-24 parity) — with this endpoint's kind
// check. nil (no error) = unknown/revoked/wrong-kind → the caller answers
// the IDENTICAL TOKEN_INVALID envelope (no oracle).
func (s *Server) resolveLink(ctx context.Context, token, expectedKind string) (*ScopeSnapshot, error) {
	sc, err := s.publicScope(ctx, token)
	if err != nil {
		return nil, err
	}
	if sc == nil || sc.Kind != expectedKind {
		return nil, nil
	}
	return sc, nil
}

// handlePublicReport — GET /public/report?token= → the SAME ReportData the
// auth path emits (single builder in reports.go). Never gated on readiness.
func (s *Server) handlePublicReport(c fiber.Ctx) error {
	token, okTok := queryToken(c)
	if !okTok {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	link, err := s.resolveLink(ctx, token, "REPORT")
	if err != nil {
		return failDB(c, err)
	}
	if link == nil {
		return fail(c, CodeTokenInvalid, MsgTokenInvalid)
	}
	data, err := s.buildReportData(ctx, link.SchoolID, link.TokenSession, link.TokenTopic, link.TokenChild)
	if err == sql.ErrNoRows {
		return fail(c, CodeTokenInvalid, MsgTokenInvalid)
	}
	if err != nil {
		return failDB(c, err)
	}
	return ok(c, data)
}

// publicGalleryPhoto is one gallery item (E29 missing flag is item-level).
type publicGalleryPhoto struct {
	ID          int64        `json:"id"`
	URL         string       `json:"url"`
	Topic       galleryTopic `json:"topic"`
	CreatedAt   string       `json:"created_at"`
	IsCandidate bool         `json:"is_candidate"`
	Missing     bool         `json:"missing,omitempty"`
}

type galleryTopic struct {
	Stid int64  `json:"stid"`
	Name string `json:"name"`
}

// handlePublicGallery — GET /public/gallery?token=: ALL topics' durable
// photos for the child+session. Lock ORDER: inactive child → consent.
func (s *Server) handlePublicGallery(c fiber.Ctx) error {
	token, okTok := queryToken(c)
	if !okTok {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	link, err := s.resolveLink(ctx, token, "GALLERY")
	if err != nil {
		return failDB(c, err)
	}
	if link == nil {
		return fail(c, CodeTokenInvalid, MsgTokenInvalid)
	}

	var (
		childID   int64
		childName string
		age       int
		school    string
		active    bool
	)
	if err := s.db.QueryRowContext(ctx,
		`SELECT id, name, age, school, is_active FROM children
		 WHERE id = ? AND school_id = ?`, link.TokenChild, link.SchoolID).
		Scan(&childID, &childName, &age, &school, &active); err != nil {
		return fail(c, CodeTokenInvalid, MsgTokenInvalid)
	}
	var (
		sessID   int64
		sessName string
		sessDate time.Time
	)
	if err := s.db.QueryRowContext(ctx,
		`SELECT id, name, date FROM sessions WHERE id = ? AND school_id = ?`,
		link.TokenSession, link.SchoolID).Scan(&sessID, &sessName, &sessDate); err != nil {
		return fail(c, CodeTokenInvalid, MsgTokenInvalid)
	}

	photos := []publicGalleryPhoto{}
	var locked bool
	var reason *string
	switch {
	case !active: // inactive wins over consent (FL-15)
		locked = true
		reason = new(string)
		*reason = "CHILD_INACTIVE"
	default:
		consent, err := s.consentStatus(ctx, link.TokenSession, link.TokenChild)
		if err != nil {
			return failDB(c, err)
		}
		if consent != "SETUJU" {
			locked = true
			reason = new(string)
			if consent == "DITOLAK" {
				*reason = "CONSENT_DENIED"
			} else {
				*reason = "CONSENT_PENDING" // PENDING or never sent
			}
		}
	}
	if !locked {
		rows, err := s.db.QueryContext(ctx,
			`SELECT p.id, p.path, p.is_candidate, p.created_at, st.id, st.topic_name_snap
			 FROM photos p
			 JOIN session_topics st ON st.id = p.session_topic_id
			 WHERE p.session_id = ? AND p.child_id = ? AND p.file_ready = TRUE
			   AND p.school_id = ?
			 ORDER BY p.created_at, p.id`, link.TokenSession, link.TokenChild, link.SchoolID)
		if err != nil {
			return failDB(c, err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				p         publicGalleryPhoto
				path      string
				createdAt time.Time
			)
			if err := rows.Scan(&p.ID, &path, &p.IsCandidate, &createdAt,
				&p.Topic.Stid, &p.Topic.Name); err != nil {
				return failDB(c, err)
			}
			p.URL = "/uploads/" + path
			p.CreatedAt = fmtTS(createdAt)
			if _, statErr := os.Stat(filepath.Join(s.upload, filepath.FromSlash(path))); statErr != nil {
				p.Missing = true // E29: durable row, gone bytes
			}
			photos = append(photos, p)
		}
		if err := rows.Err(); err != nil {
			return failDB(c, err)
		}
	}

	return ok(c, fiber.Map{
		"child":   fiber.Map{"id": childID, "name": childName, "age": age, "school": school},
		"session": fiber.Map{"id": sessID, "name": sessName, "date": fmtDate(sessDate)},
		"photos":  photos,
		"locked":  locked,
		"reason":  reason,
	})
}

// handlePublicConsent — GET /public/consent?token= (consent_links scope):
// the token resolves through the shared resolver (003b); the display query
// then keys by the (session_id, child_id) that resolution already returned.
func (s *Server) handlePublicConsent(c fiber.Ctx) error {
	token, okTok := queryToken(c)
	if !okTok {
		return nil
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	link, err := s.resolveLink(ctx, token, "CONSENT")
	if err != nil {
		return failDB(c, err)
	}
	if link == nil {
		return fail(c, CodeTokenInvalid, MsgTokenInvalid)
	}
	var (
		status      string
		respondent  sql.NullString
		respondedAt sql.NullTime
		sessionName string
		sessionDate time.Time
		location    string
		childName   string
		school      string
	)
	err = s.db.QueryRowContext(ctx,
		`SELECT cl.status, cl.respondent_name, cl.responded_at,
		        s.name, s.date, s.location, c.name, c.school
		 FROM consent_links cl
		 JOIN sessions s ON s.id = cl.session_id
		 JOIN children c ON c.id = cl.child_id
		 WHERE cl.session_id = ? AND cl.child_id = ?`,
		link.TokenSession, link.TokenChild).
		Scan(&status, &respondent, &respondedAt, &sessionName, &sessionDate,
			&location, &childName, &school)
	if err == sql.ErrNoRows {
		return fail(c, CodeTokenInvalid, MsgTokenInvalid)
	}
	if err != nil {
		return failDB(c, err)
	}
	out := fiber.Map{
		"session": fiber.Map{
			"name": sessionName, "date": fmtDate(sessionDate), "location": location,
		},
		"child":           fiber.Map{"name": childName, "school": school},
		"status":          status,
		"respondent_name": nil,
		"responded_at":    nil,
	}
	if respondent.Valid {
		out["respondent_name"] = respondent.String
	}
	if respondedAt.Valid {
		out["responded_at"] = fmtTS(respondedAt.Time)
	}
	return ok(c, out)
}

// handleConsentRespond — POST /public/consent/respond (10/min IP+token,
// idempotent): first-wins `UPDATE … WHERE status='PENDING'`; 0 rows → re-read
// → 200 {decision, already:true} (idempotent SUCCESS, never an error).
func (s *Server) handleConsentRespond(c fiber.Ctx) error {
	var req struct {
		Token          string `json:"token"`
		Decision       string `json:"decision"`
		RespondentName string `json:"respondent_name"`
	}
	if !bindBody(c, &req) {
		return nil
	}
	req.RespondentName = strings.TrimSpace(req.RespondentName)
	if req.Decision != "SETUJU" && req.Decision != "DITOLAK" {
		return fail(c, CodeValidation, MsgValidation)
	}
	if req.RespondentName == "" || len(req.RespondentName) > maxPersonNameLen {
		return fail(c, CodeValidation, MsgValidation)
	}
	if req.Token == "" {
		return fail(c, CodeTokenInvalid, MsgTokenInvalid)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	link, err := s.resolveLink(ctx, req.Token, "CONSENT")
	if err != nil {
		return failDB(c, err)
	}
	if link == nil {
		return fail(c, CodeTokenInvalid, MsgTokenInvalid)
	}
	school, sid, cid := link.SchoolID, link.TokenSession, link.TokenChild
	// The consent row id keys the first-wins UPDATE and the consent.changed
	// frame; resolution already returned this row's (session_id, child_id).
	var consentID int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM consent_links WHERE session_id = ? AND child_id = ?`,
		sid, cid).Scan(&consentID); err != nil {
		if err == sql.ErrNoRows {
			return fail(c, CodeTokenInvalid, MsgTokenInvalid)
		}
		return failDB(c, err)
	}
	c.Locals(kvSchool, school) // idempotency scope (public handler, W1 header)

	return s.idempotent(c, "POST "+c.Path(), func() error {
		ctx, cancel := dbCtx(c)
		defer cancel()

		// First-wins: only a PENDING row may flip (05 §7).
		res, err := s.db.ExecContext(ctx,
			`UPDATE consent_links
			 SET status = ?, respondent_name = ?, responded_at = NOW()
			 WHERE id = ? AND status = 'PENDING'`,
			req.Decision, req.RespondentName, consentID)
		if err != nil {
			return failDB(c, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var status string
			if err := s.db.QueryRowContext(ctx,
				`SELECT status FROM consent_links WHERE id = ?`, consentID).Scan(&status); err != nil {
				if err == sql.ErrNoRows {
					return fail(c, CodeTokenInvalid, MsgTokenInvalid)
				}
				return failDB(c, err)
			}
			return ok(c, fiber.Map{"decision": status, "already": true})
		}

		// DITOLAK wipes this child+session's photos AFTER the decision is
		// recorded: wipe (own Tx) → unlink files → publish.
		if req.Decision == "DITOLAK" {
			paths, wErr := s.wipeChildSessionPhotos(ctx, school, sid, cid)
			if wErr != nil {
				// Rows may remain but the gallery is already locked — data is
				// hidden either way; deletion never fails the recorded answer
				// and never announces a deletion that did not happen.
				slog.Error("consent ditolak photo wipe", "err", wErr,
					"session_id", sid, "child_id", cid)
			} else {
				s.unlinkPhotos(paths)
				s.publish(school, Frame{
					Entity: "photo.changed", SessionID: &sid, ChildID: cid, Action: "deleted",
				})
				// No Stid: the gallery renders ALL topics and must fully refetch.
				s.publishPublic(Frame{
					Entity: "gallery.changed", SchoolID: school,
					SessionID: &sid, ChildID: cid, Action: "updated",
				})
			}
		}

		s.publish(school, Frame{
			Entity: "consent.changed", EntityID: consentID,
			SessionID: &sid, ChildID: cid, Action: "updated",
		})
		s.publishPublic(Frame{
			Entity: "consent.ack", SchoolID: school,
			SessionID: &sid, ChildID: cid, Action: "decided",
			Decision: req.Decision,
		})
		return ok(c, fiber.Map{"decision": req.Decision})
	})
}

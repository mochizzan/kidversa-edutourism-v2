package internal

import (
	"cmp"
	"context"
	"database/sql"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/sse"
)

// Connection caps (binding single table, 01 §2 / 02 §2c):100 streams per
// replica,6 per IP,20 per school — enforced at handshake → 200
// RATE_LIMITED + Retry-After, stream never opens.
const (
	maxStreamsPerReplica = 100
	maxStreamsPerIP      = 6
	maxStreamsPerSchool  = 20
	ringEvents           = 100
	ringWindow           = 5 * time.Minute
	heartbeatInterval    = 15 * time.Second
	streamCapRetryAfter  = "30"
)

// Frame is the SSE signal (05 §3): routing keys only — never names, notes,
// photos, tokens, codes or bytes. Four full-value exceptions are merged in:
// pending.changed {pending}, wa.changed {connected}, readiness.changed
// {child_id,stid,complete}, public consent.ack {decision} — plus the
// copy_available hint on member/session.changed (FL-07 / 02 §2c).
type Frame struct {
	Entity    string `json:"entity"`    // catalogue event name, e.g. "program.changed"
	EntityID  int64  `json:"entity_id"` //0 for aggregate signals (pending/stats/wa)
	Rev       int64  `json:"rev"`       // assigned by the hub, per school monoton
	SchoolID  int64  `json:"school_id"` //0 = broadcast (SA/ADMIN connections only)
	SessionID *int64 `json:"session_id"`
	Action    string `json:"action"`
	TS        string `json:"ts"` // RFC3339 UTC

	ChildID       int64  `json:"child_id,omitempty"`       // public/readiness routing
	Stid          int64  `json:"stid,omitempty"`           // readiness/topic routing
	Pending       *int   `json:"pending,omitempty"`        // pending.changed
	Connected     *bool  `json:"connected,omitempty"`      // wa.changed
	Complete      *bool  `json:"complete,omitempty"`       // readiness.changed
	Decision      string `json:"decision,omitempty"`       // consent.ack
	CopyAvailable *bool  `json:"copy_available,omitempty"` // member/session.changed copy hint

	// Token targets terminal link.revoked delivery SERVER-SIDE ONLY:
	// json:"-" keeps it out of the wire data (no token ever leaks), and a
	// set value terminates exactly the revoked token's stream instead of a
	// sibling REPORT/GALLERY page for the same child+session.
	Token string `json:"-"`

	at time.Time // hub ring timestamp (not serialized)
}

// ScopeSnapshot is one stream connection's REST-visibility snapshot — the
// 02 §2c filter predicate "a frame is sent iff the matching REST GET would
// return that row". Authenticated fields are filled by the ScopeProvider;
// Token* fields describe a public link scope (public stream only).
type ScopeSnapshot struct {
	Role            string
	SchoolID        int64
	UserID          int64
	OwnGroupIDs     []int64
	OwnedSessionIDs []int64
	ChildIDs        []int64 // FAS: children in own groups

	Public       bool
	Kind         string // token kind: REPORT | GALLERY | CONSENT
	Token        string // the presented public token (server-side filter only)
	TokenSession int64
	TokenChild   int64
	TokenTopic   int64
}

// ScopeProvider computes the authenticated scope for one stream handshake.
// B2 MUST implement it in sessions.go by CALLING the same scope helpers used
// by GET /api/sessions and GET /api/children (reuse, never duplicate auth
// logic) and install it from RegisterSessions via registerScopeProvider.
// Without a provider, FAS connections fail closed (no session/child frames).
type ScopeProvider func(ctx context.Context, userID int64, role string, schoolID int64) (ScopeSnapshot, error)

type subscription struct {
	mu         sync.Mutex
	scope      ScopeSnapshot
	ch         chan Frame
	ip         string // immutable after subscribe
	school     int64  // immutable after subscribe (caps + replay ring)
	closed     bool
	closeAfter bool // set before sending terminal link.revoked
}

// registerScopeProvider installs the stream scope resolver (see ScopeProvider).
// Domain sections call it once during their Register* function.
func (s *Server) registerScopeProvider(fn ScopeProvider) {
	s.scopeProvider = fn
}

// RegisterStream mounts GET /api/stream (same guards as REST, plus the
// live-refresh-session check of sessionAlive and streamSubscribe's
// cap-re-checked subscription hand-off) and
// GET /public/stream?token= — both text/event-stream; handshake failures are
// ordinary 200 envelopes and no stream opens (01 §2).
func (s *Server) RegisterStream(api, pub fiber.Router) {
	transport := sse.New(sse.Config{
		Handler: s.serveStream,
		// No `retry:` hint: a set Retry would emit `retry: <ms>` and pin the
		// client's reconnect delay (lib/stream.ts uses retryMs ?? backoff),
		// defeating the spec's 1 s → 30 s doubling backoff (01 §8). Zero = no
		// `retry:` line sent; the client's own backoff governs reconnection.
		// The hub unsubscription is serveStream's own defer; this hook only
		// records a transport-level close (write failure / vanished client).
		OnClose: func(c fiber.Ctx, err error) {
			if err != nil {
				slog.Debug("stream closed", "err", err, "path", c.Path())
			}
		},
		// Our own ": ping" comment heartbeat every15 s (05 §3) — the
		// built-in one would emit a second, differently-shaped heartbeat.
		DisableHeartbeat: true,
	})
	api.Get("/stream", s.authed(), s.streamGuard, s.sessionAlive, s.streamSubscribe, transport)
	pub.Get("/stream", s.publicStreamGuard, s.streamSubscribe, transport)
}

// publish assigns rev/ts, keeps the per-school ring (100 events /5 min) and
// fans out best-effort — a dropped frame is slog'd and NEVER fails the
// mutation (06 §2). Call only AFTER Tx commit (+ file durability for
// photo/copy paths). schoolID0 broadcasts to SA/ADMIN connections
// (stats.changed, wa.changed).
func (s *Server) publish(schoolID int64, f Frame) {
	now := time.Now().UTC()
	f.SchoolID = schoolID
	f.TS = now.Format(time.RFC3339)
	f.at = now

	s.hubMu.Lock()
	f.Rev = s.revs[schoolID] + 1
	s.revs[schoolID] = f.Rev
	ring := append(s.rings[schoolID], f)
	cutoff := now.Add(-ringWindow)
	first := 0
	for first < len(ring) && ring[first].at.Before(cutoff) {
		first++
	}
	if first > 0 {
		ring = append([]Frame(nil), ring[first:]...)
	}
	if len(ring) > ringEvents {
		ring = append([]Frame(nil), ring[len(ring)-ringEvents:]...)
	}
	s.rings[schoolID] = ring
	subs := make([]*subscription, len(s.subs))
	copy(subs, s.subs)
	s.hubMu.Unlock()

	for _, sub := range subs {
		s.deliver(sub, &f)
	}
}

// publishPublic broadcasts a public-scope frame (02 §2c). Set SchoolID,
// SessionID, ChildID, Stid, Entity, Action (and Decision for consent.ack);
// pointer fields take Go 1.26 new(expr) (e.g. SessionID: new(sid)). Entity
// "link.revoked" is terminal: matching connections close right after delivery.
func (s *Server) publishPublic(f Frame) {
	if f.SchoolID == 0 || f.SessionID == nil || f.ChildID == 0 {
		slog.Error("publishPublic frame missing routing keys", "entity", f.Entity, "school_id", f.SchoolID)
	}
	s.publish(f.SchoolID, f)
}

// deliver filters one frame for one subscriber, refreshing the FAS scope on
// membership-affecting events (stale window ≤ one publish, 06 §4).
func (s *Server) deliver(sub *subscription, f *Frame) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.closed {
		return
	}
	// Cheap, scope-independent half of frameAllowedLocked — COUPLING: this
	// mirrors frameAllowedLocked's first checks, so keep them in lockstep.
	// Only the ID-membership half can depend on the refreshed snapshot; a
	// school-0 broadcast passes only SA/ADMIN and a school-scoped frame never
	// crosses schools, so those frames can never pass the filter for this
	// connection and skip the (2-query) refresh entirely (R1-F01).
	if !sub.public() {
		if f.SchoolID == 0 {
			if sub.scope.Role != "SA" && sub.scope.Role != "ADMIN" {
				return
			}
		} else if f.SchoolID != sub.scope.SchoolID {
			return
		}
	}
	if !sub.public() && sub.scope.Role == "FASILITATOR" && s.scopeProvider != nil && scopeRefreshOn(f.Entity) {
		s.refreshScope(&sub.scope)
	}
	if !s.frameAllowedLocked(sub, f) {
		return
	}
	if f.Entity == "link.revoked" {
		sub.closeAfter = true // set before send so the handler always sees it
	}
	select {
	case sub.ch <- *f:
	default:
		slog.Warn("stream frame dropped (slow subscriber)", "entity", f.Entity, "rev", f.Rev)
	}
}

// public reports whether this is a token-scoped public connection.
func (sub *subscription) public() bool { return sub.scope.Public }

// takeClosing reports whether the terminal frame was already delivered.
func (sub *subscription) takeClosing() bool {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	return sub.closeAfter
}

// scopeRefreshOn lists event bases that must recompute a FAS snapshot.
func scopeRefreshOn(entity string) bool {
	switch entityBase(entity) {
	case "member", "group", "child":
		return true
	}
	return false
}

// entityBase is "mission-pick.changed" → "mission-pick".
func entityBase(entity string) string {
	base, _, _ := strings.Cut(entity, ".")
	return base
}

// refreshScope recomputes a FAS snapshot through the provider (DB,2 s budget).
func (s *Server) refreshScope(sc *ScopeSnapshot) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	next, err := s.scopeProvider(ctx, sc.UserID, sc.Role, sc.SchoolID)
	if err != nil {
		slog.Error("stream scope refresh failed", "err", err, "user_id", sc.UserID)
		return
	}
	sc.OwnGroupIDs = next.OwnGroupIDs
	sc.OwnedSessionIDs = next.OwnedSessionIDs
	sc.ChildIDs = next.ChildIDs
}

// frameAllowedLocked is the binding filter (02 §2c): school first, then role
// scope. Caller holds sub.mu.
func (s *Server) frameAllowedLocked(sub *subscription, f *Frame) bool {
	sc := &sub.scope
	if f.SchoolID == 0 {
		return !sc.Public && (sc.Role == "SA" || sc.Role == "ADMIN")
	}
	if sc.Public {
		return publicFrameMatches(sc, f)
	}
	if f.SchoolID != sc.SchoolID {
		return false
	}
	switch sc.Role {
	case "SA", "ADMIN":
		return true
	case "FASILITATOR":
		return fasFrameAllowed(sc, f)
	}
	return false
}

// fasFrameAllowed scopes a facilitator to its own sessions/children; masters,
// users, schools, resets and stats frames are skipped (FAS has no REST access
// to them — the predicate mirrors GET, never a copy of auth logic).
func fasFrameAllowed(sc *ScopeSnapshot, f *Frame) bool {
	switch entityBase(f.Entity) {
	case "child":
		return slices.Contains(sc.ChildIDs, f.EntityID)
	case "session", "group", "member", "assessment", "photo", "narrative",
		"mission-pick", "readiness", "copy", "consent", "report-link":
		return f.SessionID != nil && slices.Contains(sc.OwnedSessionIDs, *f.SessionID)
	default:
		return false
	}
}

// publicFrameMatches locks a public connection to its token's
// child+session[+topic] scope (01 §8).
func publicFrameMatches(sc *ScopeSnapshot, f *Frame) bool {
	if f.SessionID == nil || *f.SessionID != sc.TokenSession || f.ChildID != sc.TokenChild {
		return false
	}
	if sc.TokenTopic != 0 && f.Stid != 0 && f.Stid != sc.TokenTopic {
		return false
	}
	switch f.Entity {
	case "report.changed":
		return sc.Kind == "REPORT"
	case "gallery.changed":
		return sc.Kind == "GALLERY"
	case "consent.ack":
		return sc.Kind == "CONSENT"
	case "link.revoked":
		// Terminal delivery is token-precise: a set Token (single revoke or
		// regenerate) closes ONLY that token's stream — never a sibling
		// REPORT/GALLERY page for the same child+session. An empty Token is
		// a cascade kill (child hard-delete / session wipe — every link in
		// this scope died), so terminate everything that matches.
		return f.Token == "" || f.Token == sc.Token
	default:
		return false
	}
}

// streamCapExceeded enforces the connection caps at handshake. subscribe
// re-checks the same caps atomically with its insert (see subscribe): the
// guard's check and the insert are two hubMu critical sections, so only the
// combination closes the concurrent-handshake race.
func (s *Server) streamCapExceeded(ip string, school int64) bool {
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	return s.capExceededLocked(ip, school)
}

// capExceededLocked is streamCapExceeded's body; caller holds hubMu.
func (s *Server) capExceededLocked(ip string, school int64) bool {
	if len(s.subs) >= maxStreamsPerReplica {
		return true
	}
	var ipN, schoolN int
	for _, sub := range s.subs {
		if sub.ip == ip {
			ipN++
		}
		if school > 0 && sub.school == school {
			schoolN++
		}
	}
	return ipN >= maxStreamsPerIP || (school > 0 && schoolN >= maxStreamsPerSchool)
}

// subscribe registers one connection's subscription, re-checking the caps in
// the SAME hubMu critical section that inserts it (F-B10: the guard's
// handshake check and this insert are two critical sections, so concurrent
// handshakes could all slip past the guard and open together — here the race
// loser gets nil and its handshake must fail with the guard's RATE_LIMITED
// envelope instead). The caps count existing subscriptions, so filling a hub
// up TO a cap still succeeds; only the next subscribe is refused.
func (s *Server) subscribe(scope ScopeSnapshot, ip string) *subscription {
	sub := &subscription{
		ch:     make(chan Frame, ringEvents),
		scope:  scope,
		ip:     ip,
		school: scope.SchoolID,
	}
	s.hubMu.Lock()
	if s.capExceededLocked(ip, scope.SchoolID) {
		s.hubMu.Unlock()
		return nil
	}
	s.subs = append(s.subs, sub)
	s.hubMu.Unlock()
	return sub
}

func (s *Server) unsubscribe(sub *subscription) {
	sub.mu.Lock()
	if !sub.closed {
		sub.closed = true
		close(sub.ch)
	}
	sub.mu.Unlock()

	s.hubMu.Lock()
	for i, x := range s.subs {
		if x == sub {
			s.subs = append(s.subs[:i], s.subs[i+1:]...)
			break
		}
	}
	s.hubMu.Unlock()
}

// replay delivers ring frames with rev > Last-Event-ID after the scope filter
// (filter first — a filtered-out rev jump is never a gap, 06 §5). Authenticated
// connections replay the UNION of their school ring and the school-0 broadcast
// ring (F-B16): they receive broadcasts live, so a Last-Event-ID reconnect
// must replay them too; a public token connection keeps its school ring only.
// A ring that cannot cover last+1 (restart /100-event overflow) is an
// unclosable gap: logged once, on the first frame that survives the filter;
// the client's full-refetch closes it. replay reports whether it wrote the
// terminal link.revoked frame (F-B11) — the caller must end the stream.
func (s *Server) replay(sub *subscription, st *sse.Stream, lastID string) bool {
	last, _ := strconv.ParseInt(lastID, 10, 64)
	s.hubMu.Lock()
	ring := append([]Frame(nil), s.rings[sub.school]...)
	maxRev := s.revs[sub.school]
	oldestRev := int64(0)
	if len(ring) > 0 {
		oldestRev = ring[0].Rev
	}
	// F-B16: school-0 broadcasts live in their own ring, and a school-scoped
	// AUTHENTICATED connection receives them live — replay must union both
	// rings or every broadcast tick between last and reconnect is lost
	// (frameAllowedLocked still applies per frame: broadcasts pass only
	// SA/ADMIN, and a public token scope never matches school-0). Revs are
	// per-ring counters sharing one client cursor, so the merge is by rev
	// with the school ring first on ties; the gap check below stays on the
	// SCHOOL ring's oldest rev — the broadcast counter cannot judge it.
	if !sub.public() && sub.school != 0 {
		if bcast := s.rings[0]; len(bcast) > 0 {
			ring = append(ring, bcast...)
			slices.SortStableFunc(ring, func(a, b Frame) int { return cmp.Compare(a.Rev, b.Rev) })
		}
		if rev := s.revs[0]; rev > maxRev {
			maxRev = rev // the restart heuristic must not fire on a broadcast rev
		}
	}
	s.hubMu.Unlock()

	if last > 0 && last > maxRev {
		slog.Info("stream replay gap: rev counter reset (restart)", "last_event_id", last, "max_rev", maxRev)
	}
	gapLogged := false
	for i := range ring {
		f := ring[i]
		if f.Rev <= last {
			continue
		}
		sub.mu.Lock()
		allowed := s.frameAllowedLocked(sub, &f)
		if allowed && f.Entity == "link.revoked" {
			// Terminal delivery (F-B11): the replay path must mark the
			// connection closing exactly like deliver does — set under
			// sub.mu before the write so the handler always sees it.
			sub.closeAfter = true
		}
		sub.mu.Unlock()
		if !allowed {
			continue
		}
		// Judged only once a frame survives the filter (06 §5): a rev jump
		// made purely of filtered frames is not a gap, and the log fires at
		// most once per replay.
		if !gapLogged && last > 0 && oldestRev > last+1 {
			gapLogged = true
			slog.Info("stream replay gap: ring overflow", "last_event_id", last, "oldest_rev", oldestRev)
		}
		if err := st.Event(sse.Event{ID: strconv.FormatInt(f.Rev, 10), Name: f.Entity, Data: f}); err != nil {
			return false
		}
		if f.Entity == "link.revoked" {
			return true // terminal: the stream ends right after this frame
		}
	}
	return false
}

// streamGuard runs after authed(): caps + scope snapshot for the auth stream.
func (s *Server) streamGuard(c fiber.Ctx) error {
	u, _ := c.Locals(kvUser).(*userRow)
	school, _ := c.Locals(kvSchool).(int64)
	if s.streamCapExceeded(c.IP(), school) {
		c.Set(fiber.HeaderRetryAfter, streamCapRetryAfter)
		return fail(c, CodeRateLimited, MsgRateLimited)
	}
	scope := ScopeSnapshot{Role: u.Role, SchoolID: school, UserID: u.ID}
	if u.Role == "FASILITATOR" && s.scopeProvider != nil {
		ctx, cancel := dbCtx(c)
		defer cancel()
		loaded, err := s.scopeProvider(ctx, u.ID, u.Role, school)
		if err != nil {
			return failDB(c, err)
		}
		loaded.Role, loaded.SchoolID, loaded.UserID = u.Role, school, u.ID
		loaded.Public = false
		scope = loaded
	}
	c.Locals(kvScope, &scope)
	return c.Next()
}

// sessionAlive runs after streamGuard and opens the handshake only when the
// token owner still holds a live refresh session. The access JWT carries no
// jti (06 §1), so a Bearer minted before logout would otherwise keep opening
// streams until it expires (audit K3): logout / password-reset / admin
// revoke every refresh_tokens row, and zero active rows fail the handshake
// with the usual INVALID_TOKEN envelope — no stream opens (06 §3, 08).
func (s *Server) sessionAlive(c fiber.Ctx) error {
	u, _ := c.Locals(kvUser).(*userRow)
	if u == nil {
		return fail(c, CodeInvalidToken, MsgInvalidToken)
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM refresh_tokens WHERE user_id = ? AND revoked_at IS NULL AND expires_at > NOW() LIMIT 1`,
		u.ID).Scan(&one)
	switch {
	case err == sql.ErrNoRows:
		return fail(c, CodeInvalidToken, MsgInvalidToken)
	case err != nil:
		return failDB(c, err)
	}
	return c.Next()
}

// publicStreamGuard validates ?token= (TOKEN_INVALID — identical shape for
// unknown/revoked, no oracle), locks the scope to that token and enforces caps.
func (s *Server) publicStreamGuard(c fiber.Ctx) error {
	var q struct {
		Token string `query:"token"`
	}
	if err := c.Bind().Query(&q); err != nil || q.Token == "" {
		return fail(c, CodeTokenInvalid, MsgTokenInvalid)
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	scope, err := s.publicScope(ctx, q.Token)
	if err != nil {
		return failDB(c, err)
	}
	if scope == nil {
		return fail(c, CodeTokenInvalid, MsgTokenInvalid)
	}
	if s.streamCapExceeded(c.IP(), scope.SchoolID) {
		c.Set(fiber.HeaderRetryAfter, streamCapRetryAfter)
		return fail(c, CodeRateLimited, MsgRateLimited)
	}
	c.Locals(kvScope, scope)
	return c.Next()
}

// publicScope resolves a public token to its link scope: active public_links
// (report/gallery) or consent_links (consent). nil = unknown/revoked.
func (s *Server) publicScope(ctx context.Context, token string) (*ScopeSnapshot, error) {
	sc := &ScopeSnapshot{Public: true, Kind: "CONSENT", Token: token}
	var topic sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT school_id, session_id, session_topic_id, child_id, kind
		 FROM public_links WHERE token = ? AND revoked_at IS NULL`, token).
		Scan(&sc.SchoolID, &sc.TokenSession, &topic, &sc.TokenChild, &sc.Kind)
	switch {
	case err == nil:
		sc.TokenTopic = topic.Int64
		return sc, nil
	case err != sql.ErrNoRows:
		return nil, err
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT school_id, session_id, child_id FROM consent_links WHERE token = ?`, token).
		Scan(&sc.SchoolID, &sc.TokenSession, &sc.TokenChild)
	switch {
	case err == sql.ErrNoRows:
		return nil, nil
	case err != nil:
		return nil, err
	}
	return sc, nil
}

// kvStreamSub hands the locked-in subscription from streamSubscribe to
// serveStream; stream-local companion to the envelope.go kv* keys.
const kvStreamSub = "kv.streamsub"

// streamSubscribe runs between the handshake guards and the SSE transport: it
// inserts the subscription — with the connection caps re-checked INSIDE
// subscribe, in the same hubMu critical section as the insert (F-B10: the
// guard's earlier check and this insert would otherwise be two critical
// sections, letting concurrent handshakes all slip past the guard) — and
// installs it for serveStream. A capped handshake aborts HERE, before the
// transport opens, with the guard's exact RATE_LIMITED envelope.
func (s *Server) streamSubscribe(c fiber.Ctx) error {
	scope, _ := c.Locals(kvScope).(*ScopeSnapshot)
	if scope == nil {
		return nil // unreachable: guards always set it before Next()
	}
	sub := s.subscribe(*scope, c.IP())
	if sub == nil {
		c.Set(fiber.HeaderRetryAfter, streamCapRetryAfter)
		return fail(c, CodeRateLimited, MsgRateLimited)
	}
	c.Locals(kvStreamSub, sub)
	return c.Next()
}

// serveStream is the sse.Handler body: replay Last-Event-ID, then pump
// frames + ": ping" heartbeat + directed auth.expiring at T-60 s until the
// client drops or the process shuts down. The subscription was inserted (and
// cap-re-checked) by streamSubscribe before the transport opened; the hub
// unsubscription is this function's own defer.
func (s *Server) serveStream(c fiber.Ctx, st *sse.Stream) error {
	sub, _ := c.Locals(kvStreamSub).(*subscription)
	if sub == nil {
		return nil // unreachable: streamSubscribe always installs one
	}
	defer s.unsubscribe(sub)

	if s.replay(sub, st, st.LastEventID()) {
		return nil // a replayed terminal link.revoked ends the stream here
	}

	var expC <-chan time.Time
	if claims, ok := c.Locals(kvClaims).(*accessClaims); ok && claims.ExpiresAt != nil {
		timer := time.NewTimer(time.Until(claims.ExpiresAt.Time.Add(-60 * time.Second)))
		defer timer.Stop()
		expC = timer.C
	}
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-st.Done():
			return nil
		case <-s.stop:
			return nil
		case f, open := <-sub.ch:
			if !open {
				return nil
			}
			if err := st.Event(sse.Event{ID: strconv.FormatInt(f.Rev, 10), Name: f.Entity, Data: f}); err != nil {
				return nil
			}
			if sub.takeClosing() {
				return nil
			}
		case <-heartbeat.C:
			if err := st.Comment("ping"); err != nil {
				return nil
			}
		case <-expC:
			expC = nil // one-shot per connection
			if claims, ok := c.Locals(kvClaims).(*accessClaims); ok && claims.ExpiresAt != nil {
				if err := st.Event(sse.Event{
					Name: "auth.expiring",
					Data: fiber.Map{"exp": claims.ExpiresAt.Time.UTC().Format(time.RFC3339)},
				}); err != nil {
					return nil
				}
			}
		}
	}
}

// startStatsTick publishes stats.changed on the 02 §2c aggregator tick
// (5–10 s server tick, not per-mutation; broadcast → SA/ADMIN dashboards).
func (s *Server) startStatsTick() {
	go func() {
		t := time.NewTicker(7 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.publish(0, Frame{Entity: "stats.changed", Action: "tick"})
			case <-s.stop:
				return
			}
		}
	}()
}

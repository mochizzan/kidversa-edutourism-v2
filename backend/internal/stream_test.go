package internal

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Hub mechanics
// ---------------------------------------------------------------------------

func TestStreamHubPublishDeliversToSubscriber(t *testing.T) {
	s, _ := newTestServer(t)
	sub := s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: 1}, "1.1.1.1")
	defer s.unsubscribe(sub)

	s.publish(1, Frame{Entity: "program.changed", EntityID: 4, Action: "created"})

	select {
	case f := <-sub.ch:
		require.Equal(t, "program.changed", f.Entity)
		require.Equal(t, int64(1), f.Rev, "first frame in a school gets rev 1")
		require.Equal(t, int64(1), f.SchoolID)
		require.NotEmpty(t, f.TS, "hub stamps RFC3339 ts")
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the published frame")
	}
}

func TestStreamRevCountersArePerSchool(t *testing.T) {
	s, _ := newTestServer(t)
	s.publish(1, Frame{Entity: "program.changed", Action: "created"})
	s.publish(1, Frame{Entity: "program.changed", Action: "updated"})
	s.publish(0, Frame{Entity: "stats.changed", Action: "tick"})
	s.publish(7, Frame{Entity: "user.changed", Action: "created"})

	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	require.Equal(t, int64(2), s.revs[1], "school 1 keeps its own monotonic rev")
	require.Equal(t, int64(1), s.revs[0], "the broadcast stream has its own rev counter")
	require.Equal(t, int64(1), s.revs[7], "schools never share rev counters")
}

// ---------------------------------------------------------------------------
// Filter matrix (02 §2c: a frame is sent iff the matching REST GET would
// return that row)
// ---------------------------------------------------------------------------

func TestStreamAuthenticatedFilterMatrix(t *testing.T) {
	s, _ := newTestServer(t)
	own, foreign := int64(2), int64(9)

	cases := []struct {
		name  string
		scope ScopeSnapshot
		frame Frame
		want  bool
	}{
		{"SA receives school frames", ScopeSnapshot{Role: "SA", SchoolID: 1},
			Frame{SchoolID: 1, Entity: "program.changed"}, true},
		{"SA receives broadcast stats", ScopeSnapshot{Role: "SA", SchoolID: 1},
			Frame{SchoolID: 0, Entity: "stats.changed"}, true},
		{"ADMIN receives broadcast wa", ScopeSnapshot{Role: "ADMIN", SchoolID: 1},
			Frame{SchoolID: 0, Entity: "wa.changed"}, true},
		{"FAS never receives broadcast", ScopeSnapshot{Role: "FASILITATOR", SchoolID: 1},
			Frame{SchoolID: 0, Entity: "stats.changed"}, false},
		{"other school dropped", ScopeSnapshot{Role: "SA", SchoolID: 1},
			Frame{SchoolID: 2, Entity: "program.changed"}, false},
		{"FAS own session frame", ScopeSnapshot{Role: "FASILITATOR", SchoolID: 1, OwnedSessionIDs: []int64{own}},
			Frame{SchoolID: 1, Entity: "assessment.changed", SessionID: &own}, true},
		{"FAS foreign session frame", ScopeSnapshot{Role: "FASILITATOR", SchoolID: 1, OwnedSessionIDs: []int64{own}},
			Frame{SchoolID: 1, Entity: "assessment.changed", SessionID: &foreign}, false},
		{"FAS session frame without session key", ScopeSnapshot{Role: "FASILITATOR", SchoolID: 1, OwnedSessionIDs: []int64{own}},
			Frame{SchoolID: 1, Entity: "session.changed"}, false},
		{"FAS own child frame", ScopeSnapshot{Role: "FASILITATOR", SchoolID: 1, ChildIDs: []int64{30}},
			Frame{SchoolID: 1, Entity: "child.changed", EntityID: 30}, true},
		{"FAS foreign child frame", ScopeSnapshot{Role: "FASILITATOR", SchoolID: 1, ChildIDs: []int64{30}},
			Frame{SchoolID: 1, Entity: "child.changed", EntityID: 40}, false},
		{"FAS skips masters/users frames", ScopeSnapshot{Role: "FASILITATOR", SchoolID: 1},
			Frame{SchoolID: 1, Entity: "user.changed", EntityID: 40}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := &subscription{scope: tc.scope, ch: make(chan Frame, 1)}
			frame := tc.frame
			require.Equal(t, tc.want, s.frameAllowedLocked(sub, &frame))
		})
	}
}

func TestStreamPublicFilterMatrix(t *testing.T) {
	s, _ := newTestServer(t)
	sess, otherSess := int64(2), int64(9)
	base := func(kind, token string) ScopeSnapshot {
		return ScopeSnapshot{Public: true, Kind: kind, Token: token,
			TokenSession: sess, TokenChild: 30, TokenTopic: 5}
	}
	frame := func(entity string, stid int64, token string) Frame {
		return Frame{SchoolID: 1, SessionID: &sess, ChildID: 30,
			Entity: entity, Stid: stid, Token: token}
	}

	cases := []struct {
		name  string
		scope ScopeSnapshot
		frame Frame
		want  bool
	}{
		{"REPORT token receives report.changed", base("REPORT", "tokA"), frame("report.changed", 0, ""), true},
		{"REPORT token skips gallery.changed", base("REPORT", "tokA"), frame("gallery.changed", 0, ""), false},
		{"REPORT token skips consent.ack", base("REPORT", "tokA"), frame("consent.ack", 0, ""), false},
		{"GALLERY token receives gallery.changed", base("GALLERY", "tokA"), frame("gallery.changed", 0, ""), true},
		{"GALLERY token skips report.changed", base("GALLERY", "tokA"), frame("report.changed", 5, ""), false},
		{"CONSENT token receives consent.ack", base("CONSENT", "tokA"), frame("consent.ack", 0, ""), true},
		{"foreign session filtered", base("REPORT", "tokA"),
			func() Frame { f := frame("report.changed", 0, ""); f.SessionID = &otherSess; return f }(), false},
		{"foreign child filtered", base("REPORT", "tokA"),
			func() Frame { f := frame("report.changed", 0, ""); f.ChildID = 31; return f }(), false},
		{"topic-scoped token drops sibling topic", base("REPORT", "tokA"), frame("report.changed", 6, ""), false},
		{"topic-scoped token keeps its topic", base("REPORT", "tokA"), frame("report.changed", 5, ""), true},
		{"link.revoked token-precise delivery", base("REPORT", "tokA"), frame("link.revoked", 0, "tokA"), true},
		{"link.revoked spares sibling token", base("REPORT", "tokA"), frame("link.revoked", 0, "tokB"), false},
		{"link.revoked cascade reaches everyone", base("REPORT", "tokA"), frame("link.revoked", 0, ""), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := &subscription{scope: tc.scope, ch: make(chan Frame, 1)}
			f := tc.frame
			require.Equal(t, tc.want, s.frameAllowedLocked(sub, &f))
		})
	}
}

func TestStreamScopeRefreshOnMembershipEvents(t *testing.T) {
	for _, entity := range []string{"member.changed", "group.changed", "child.changed"} {
		require.Truef(t, scopeRefreshOn(entity), "%s must refresh the FAS scope", entity)
	}
	for _, entity := range []string{"session.changed", "program.changed", "stats.changed"} {
		require.Falsef(t, scopeRefreshOn(entity), "%s must not refresh the FAS scope", entity)
	}
}

// TestStreamLinkRevokedClosesMatchingSubscriber: terminal delivery flips
// closeAfter ONLY on the revoked token's connection (a sibling REPORT page
// for the same child+session survives).
func TestStreamLinkRevokedClosesMatchingSubscriber(t *testing.T) {
	s, _ := newTestServer(t)
	scope := func(token string) ScopeSnapshot {
		return ScopeSnapshot{Public: true, Kind: "REPORT", Token: token,
			TokenSession: 2, TokenChild: 30, SchoolID: 1}
	}
	sid := int64(2)
	revoked := Frame{Entity: "link.revoked", SchoolID: 1, SessionID: &sid,
		ChildID: 30, Action: "revoked", Token: "tokA"}

	victim := s.subscribe(scope("tokA"), "2.2.2.2")
	defer s.unsubscribe(victim)
	s.deliver(victim, &revoked)
	require.Len(t, victim.ch, 1, "the revoked token's stream receives the frame")
	require.True(t, victim.takeClosing(), "delivery is terminal for that token")

	sibling := s.subscribe(scope("tokB"), "2.2.2.2")
	defer s.unsubscribe(sibling)
	s.deliver(sibling, &revoked)
	require.Empty(t, sibling.ch, "a sibling token's stream must NOT receive the revoke")
	require.False(t, sibling.takeClosing())
}

// TestStreamRefreshesFAScopeOnMembershipEvents: a membership-affecting frame
// recomputes the FAS snapshot through the provider BEFORE filtering, so a
// child just added to the facilitator's session becomes visible immediately.
func TestStreamRefreshesFAScopeOnMembershipEvents(t *testing.T) {
	s, _ := newTestServer(t)
	calls := 0
	s.registerScopeProvider(func(_ context.Context, userID int64, role string, schoolID int64) (ScopeSnapshot, error) {
		calls++
		// The refreshed snapshot sees the new membership (session 5).
		return ScopeSnapshot{Role: role, SchoolID: schoolID, UserID: userID,
			OwnedSessionIDs: []int64{2, 5}, ChildIDs: []int64{30}}, nil
	})

	sub := s.subscribe(ScopeSnapshot{Role: "FASILITATOR", SchoolID: 1, UserID: 3,
		OwnedSessionIDs: []int64{2}}, "3.3.3.3")
	defer s.unsubscribe(sub)

	fresh := int64(5)
	s.publish(1, Frame{Entity: "member.changed", SessionID: &fresh, Action: "added"})
	require.Equal(t, 1, calls, "membership events must refresh the FAS snapshot")
	require.Len(t, sub.ch, 1, "session 5 is own after the refresh → frame delivered")

	// Non-membership events never hit the provider and stay filtered.
	s.publish(1, Frame{Entity: "program.changed", EntityID: 1, Action: "created"})
	require.Equal(t, 1, calls)
	require.Len(t, sub.ch, 1, "masters frames stay filtered for FAS")
}

// TestStreamCheapGateSkipsScopeRefreshBeforeSchoolGate: deliver applies the
// cheap scope-independent half of frameAllowedLocked BEFORE refreshScope
// (R1-F01) — frames this connection can never pass never pay the 2-query
// refresh, while a same-school FAS subscriber still refreshes before the
// ID-membership half of the filter (stale window ≤ one publish preserved).
func TestStreamCheapGateSkipsScopeRefreshBeforeSchoolGate(t *testing.T) {
	s, _ := newTestServer(t)
	calls := 0
	s.registerScopeProvider(func(_ context.Context, userID int64, role string, schoolID int64) (ScopeSnapshot, error) {
		calls++
		// The refreshed snapshot sees the freshly added session 5.
		return ScopeSnapshot{Role: role, SchoolID: schoolID, UserID: userID,
			OwnedSessionIDs: []int64{2, 5}, ChildIDs: []int64{30}}, nil
	})

	fas := s.subscribe(ScopeSnapshot{Role: "FASILITATOR", SchoolID: 1, UserID: 3,
		OwnedSessionIDs: []int64{2}}, "3.3.3.1")
	defer s.unsubscribe(fas)
	sa := s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: 1}, "1.1.3.1")
	defer s.unsubscribe(sa)
	admin := s.subscribe(ScopeSnapshot{Role: "ADMIN", SchoolID: 1}, "1.1.3.2")
	defer s.unsubscribe(admin)
	pub := s.subscribe(ScopeSnapshot{Public: true, Kind: "REPORT",
		TokenSession: 2, TokenChild: 30}, "1.1.3.3")
	defer s.unsubscribe(pub)

	sid := int64(5)

	// Cross-school membership event: the cheap gate rejects before refresh.
	s.publish(2, Frame{Entity: "member.changed", SessionID: &sid, Action: "updated"})
	require.Equal(t, 0, calls, "cross-school frames must not refresh the FAS scope")
	require.Empty(t, fas.ch, "a cross-school frame never delivers")

	// School-0 broadcasts pass only SA/ADMIN — the FAS connection rejects
	// without refreshing (the old order refreshed first).
	s.publish(0, Frame{Entity: "member.changed", Action: "added"})
	require.Equal(t, 0, calls, "school-0 broadcasts must not refresh the FAS scope")
	require.Empty(t, fas.ch, "FAS never receives school-0 broadcasts")
	require.Len(t, sa.ch, 1, "SA keeps receiving school-0 broadcasts")

	// Same-school membership event: refresh first, then the full filter —
	// the refreshed scope owns session 5, so the frame delivers.
	s.publish(1, Frame{Entity: "member.changed", SessionID: &sid, Action: "updated"})
	require.Equal(t, 1, calls, "same-school FAS refreshes exactly once")
	require.Len(t, fas.ch, 1, "refreshed scope owns session 5 → delivered")

	// SA/ADMIN/public never touch the provider (Role/public gate stands) —
	// they still receive the broadcast + same-school frame via the filter.
	require.Equal(t, 1, calls, "SA/ADMIN/public connections never refresh")
	require.Len(t, admin.ch, 2, "ADMIN gets the broadcast and the same-school frame")
	require.Empty(t, pub.ch, "public token scope never matches member.changed")
}

// ---------------------------------------------------------------------------
// Frame wire shape
// ---------------------------------------------------------------------------

// TestStreamFrameTokenExcludedFromJSON is the wire-shape guard: the
// server-side link.revoked targeting token must never serialize.
func TestStreamFrameTokenExcludedFromJSON(t *testing.T) {
	sid := int64(2)
	b, err := json.Marshal(Frame{
		Entity: "link.revoked", SchoolID: 1, SessionID: &sid, ChildID: 30,
		Action: "revoked", Token: "super-secret-token",
	})
	require.NoError(t, err)
	require.NotContains(t, string(b), "super-secret-token")
	require.NotContains(t, string(b), `"token"`)
	require.Contains(t, string(b), `"entity":"link.revoked"`)
}

// ---------------------------------------------------------------------------
// Connection caps (unit level: 100 / replica, 6 / IP, 20 / school)
// ---------------------------------------------------------------------------

func TestStreamConnectionCaps(t *testing.T) {
	t.Run("six per IP", func(t *testing.T) {
		s, _ := newTestServer(t)
		for range 6 {
			s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: 1}, "7.7.7.7")
		}
		require.True(t, s.streamCapExceeded("7.7.7.7", 0))
		require.False(t, s.streamCapExceeded("8.8.8.8", 0))
	})

	t.Run("twenty per school", func(t *testing.T) {
		s, _ := newTestServer(t)
		for i := range 20 {
			s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: 7}, fmt.Sprintf("10.0.0.%d", i))
		}
		require.True(t, s.streamCapExceeded("9.9.9.9", 7))
		require.False(t, s.streamCapExceeded("9.9.9.9", 8), "other schools unaffected")
	})

	t.Run("one hundred per replica", func(t *testing.T) {
		s, _ := newTestServer(t)
		for i := range 100 {
			s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: int64(i%5 + 1)}, fmt.Sprintf("10.1.0.%d", i))
		}
		require.True(t, s.streamCapExceeded("4.4.4.4", 0))
	})

	// F-B10: the guard's handshake check and subscribe's insert are two hubMu
	// critical sections — subscribe re-checks the caps atomically with the
	// insert, so the race loser of two concurrent handshakes gets nil and
	// inserts nothing. (The public HTTP path cannot reach this sequentially —
	// the guard refuses first — so this exercises subscribe directly.)
	t.Run("in-subscribe re-check refuses past the cap", func(t *testing.T) {
		s, _ := newTestServer(t)
		for range maxStreamsPerIP {
			s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: 1}, "7.7.7.8")
		}
		require.Nil(t, s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: 1}, "7.7.7.8"),
			"the in-subscribe re-check must refuse a handshake slipped past the guard")
		s.hubMu.Lock()
		n := len(s.subs)
		s.hubMu.Unlock()
		require.Equal(t, maxStreamsPerIP, n, "a refused subscribe must insert nothing")
	})
}

// TestStreamSubscribeCapsEnvelopeBeforeTransport drives the in-subscribe
// re-check's abort path (streamSubscribe) with the caps already full —
// unreachable sequentially through /api/stream because the guard refuses
// first — and pins that it produces the guard's exact RATE_LIMITED envelope
// BEFORE the SSE transport opens, while a below-cap handshake advances with
// the subscription installed.
func TestStreamSubscribeCapsEnvelopeBeforeTransport(t *testing.T) {
	newRaceLoserApp := func(s *Server) *fiber.App {
		app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
		app.Get("/fsx-race-loser",
			func(c fiber.Ctx) error {
				scope := ScopeSnapshot{Role: "SA", SchoolID: 1}
				c.Locals(kvScope, &scope)
				return c.Next()
			},
			s.streamSubscribe,
			func(c fiber.Ctx) error { return c.SendString("REACHED") },
		)
		return app
	}

	t.Run("at cap the handshake aborts with the guard envelope", func(t *testing.T) {
		s, _ := newTestServer(t)
		for i := range maxStreamsPerReplica {
			s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: int64(i%5 + 1)}, fmt.Sprintf("6.6.6.%d", i))
		}
		app := newRaceLoserApp(s)

		res := doReq(t, app, "GET", "/fsx-race-loser", "", nil)

		requireErrEnvelope(t, res, CodeRateLimited)
		require.Equal(t, "30", res.header.Get("Retry-After"))
		require.Contains(t, res.header.Get("Content-Type"), "application/json",
			"a capped subscribe must abort before the SSE transport opens")
		s.hubMu.Lock()
		after := len(s.subs)
		s.hubMu.Unlock()
		require.Equal(t, maxStreamsPerReplica, after,
			"the refused handshake must never subscribe")
	})

	t.Run("below the cap the subscription advances", func(t *testing.T) {
		s, _ := newTestServer(t)
		app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
		app.Get("/fsx-open",
			func(c fiber.Ctx) error {
				scope := ScopeSnapshot{Role: "SA", SchoolID: 1}
				c.Locals(kvScope, &scope)
				return c.Next()
			},
			s.streamSubscribe,
			func(c fiber.Ctx) error {
				sub, _ := c.Locals(kvStreamSub).(*subscription)
				if sub == nil {
					return c.Status(500).SendString("no subscription installed")
				}
				s.unsubscribe(sub)
				return c.SendString("REACHED")
			},
		)

		res := doReq(t, app, "GET", "/fsx-open", "", nil)

		require.Equal(t, 200, res.status)
		require.Equal(t, "REACHED", res.body,
			"below the cap streamSubscribe must install the subscription and advance")
		require.Empty(t, s.subs)
	})
}

// ---------------------------------------------------------------------------
// Handshake over HTTP: failures are ordinary 200 envelopes, no stream
// ---------------------------------------------------------------------------

// sqlActiveRefreshSession is the session-liveness probe of sessionAlive: the
// auth-stream handshake requires the token owner to hold ≥1 live
// refresh_tokens row (logout revokes them all → INVALID_TOKEN, K3).
func sqlActiveRefreshSession() string {
	return q("FROM refresh_tokens WHERE user_id = ? AND revoked_at IS NULL AND expires_at > NOW()")
}

func sqlRefreshAliveRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"1"}).AddRow(1)
}

func TestStreamHandshakeFailuresAreEnvelopes(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	t.Run("missing token", func(t *testing.T) {
		res := doReq(t, app, "GET", "/api/stream", "", nil)
		requireErrEnvelope(t, res, CodeInvalidToken)
		require.Contains(t, res.header.Get("Content-Type"), "application/json",
			"a failed handshake must not open a stream")
	})

	t.Run("unknown subject", func(t *testing.T) {
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(404)).WillReturnError(sql.ErrNoRows)
		tok := mintAccess(t, s, &userRow{ID: 404, Role: "ADMIN", SchoolID: 1})
		res := doReq(t, app, "GET", "/api/stream", "", bearer(tok))
		requireErrEnvelope(t, res, CodeInvalidToken)
	})

	t.Run("facilitator school mismatch", func(t *testing.T) {
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
		h := bearer(mintAccess(t, s, fasUserRow()))
		h["X-School-ID"] = "2"
		res := doReq(t, app, "GET", "/api/stream", "", h)
		requireErrEnvelope(t, res, CodeSchoolMismatch)
	})

	// K3 / 08: the access JWT survives logout, but the owner no longer holds
	// any active refresh_tokens row → the handshake fails, no stream opens.
	t.Run("owner has no live refresh session", func(t *testing.T) {
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
		mock.ExpectQuery(sqlActiveRefreshSession()).
			WithArgs(int64(1)).WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/api/stream", "", bearer(mintAccess(t, s, saUserRow())))
		requireErrEnvelope(t, res, CodeInvalidToken)
		require.Contains(t, res.header.Get("Content-Type"), "application/json",
			"a revoked session must fail the handshake, not open a stream")
	})

	t.Run("session probe DB failure", func(t *testing.T) {
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
		mock.ExpectQuery(sqlActiveRefreshSession()).
			WithArgs(int64(1)).WillReturnError(errors.New("probe down"))
		res := doReq(t, app, "GET", "/api/stream", "", bearer(mintAccess(t, s, saUserRow())))
		requireErrEnvelope(t, res, CodeDBUnavailable)
	})

	t.Run("public stream unknown token", func(t *testing.T) {
		mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
			WithArgs("nope").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
			WithArgs("nope").WillReturnError(sql.ErrNoRows)
		res := doReq(t, app, "GET", "/public/stream?token=nope", "", nil)
		requireErrEnvelope(t, res, CodeTokenInvalid)
	})
}

func TestStreamHandshakeRateLimitedEnvelope(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	// Fill the replica cap with live subscriptions. Distinct IPs + spread
	// schools: seeding itself must not trip subscribe's in-subscribe
	// per-IP/per-school re-checks (F-B10).
	for i := range maxStreamsPerReplica {
		s.subscribe(ScopeSnapshot{Role: "SA", SchoolID: int64(i%5 + 1)}, fmt.Sprintf("5.5.5.%d", i))
	}
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	res := doReq(t, app, "GET", "/api/stream", "", bearer(mintAccess(t, s, saUserRow())))
	requireErrEnvelope(t, res, CodeRateLimited)
	require.Equal(t, "30", res.header.Get("Retry-After"))
}

// TestStreamHandshakeRequiresLiveRefreshSession is the K3 fix / 08
// acceptance: while the owner holds an active refresh_tokens row (logged
// in) the Bearer opens the stream; once every row is revoked (logout) the
// SAME still-cryptographically-valid access token fails the handshake with
// an ordinary INVALID_TOKEN envelope and no stream opens.
func TestStreamHandshakeRequiresLiveRefreshSession(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	base := startListenerApp(t, app)
	t.Cleanup(func() {
		s.Stop()
		app.Shutdown() //nolint:errcheck // shutdown after Stop is best-effort
	})
	tok := mintAccess(t, s, saUserRow())

	// Logged in: auth guard + live refresh row → the stream opens and
	// delivers frames.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery(sqlActiveRefreshSession()).WithArgs(int64(1)).
		WillReturnRows(sqlRefreshAliveRows())
	resp, events := connectSSE(t, base, tok, "1", "0")
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	s.publish(1, Frame{Entity: "program.changed", EntityID: 4, Action: "created"})
	ev := nextEvent(t, events)
	require.Equal(t, "program.changed", ev.name)
	resp.Body.Close()
	subCount := func() int {
		s.hubMu.Lock()
		defer s.hubMu.Unlock()
		return len(s.subs)
	}

	// Logged out: logout revoked every refresh_tokens row — the same token
	// now fails the handshake (200 envelope, JSON, no stream, no sub).
	// No X-School-ID header → the SA guard skips the school probe.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
	mock.ExpectQuery(sqlActiveRefreshSession()).WithArgs(int64(1)).
		WillReturnError(sql.ErrNoRows)
	subsBefore := subCount()
	res := doReq(t, app, "GET", "/api/stream", "", bearer(tok))
	requireErrEnvelope(t, res, CodeInvalidToken)
	require.Contains(t, res.header.Get("Content-Type"), "application/json",
		"a revoked session must not open a stream")
	// The first connection's subscription may linger until its next heartbeat
	// write fails; the revoked handshake must never ADD one (a drain only
	// lowers the count).
	require.LessOrEqual(t, subCount(), subsBefore,
		"the revoked handshake must never subscribe")
}

// ---------------------------------------------------------------------------
// Live listener: ring replay, filter-before-gap, rev <= last, auth.expiring
// ---------------------------------------------------------------------------

type sseEvent struct {
	id   string
	name string
	data string
}

func startListenerApp(t *testing.T, app *fiber.App) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() { ln.Close() })
	return "http://" + ln.Addr().String()
}

func startSSEReader(r io.Reader) <-chan sseEvent {
	ch := make(chan sseEvent, 32)
	go func() {
		defer close(ch)
		var ev sseEvent
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.name != "" || ev.data != "" {
					ch <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "id:"):
				ev.id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			case strings.HasPrefix(line, "event:"):
				ev.name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				ev.data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
	}()
	return ch
}

func connectSSE(t *testing.T, base, token, school, lastEventID string) (*http.Response, <-chan sseEvent) {
	t.Helper()
	client := &http.Client{}
	var resp *http.Response
	var err error
	deadline := time.Now().Add(3 * time.Second)
	for {
		var req *http.Request
		req, err = http.NewRequest("GET", base+"/api/stream", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		if school != "" {
			req.Header.Set("X-School-ID", school)
		}
		if lastEventID != "" {
			req.Header.Set("Last-Event-ID", lastEventID)
		}
		resp, err = client.Do(req)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect SSE: %v", err)
		}
		time.Sleep(20 * time.Millisecond) // server goroutine startup retry
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, startSSEReader(resp.Body)
}

// connectSSEPublic opens /public/stream?token= (no Authorization). Unlike
// FsxStartStream it does not wait on the hub: for a replay-terminal frame
// (link.revoked) the subscription can exist and die between two polls, and
// client.Do already returns as soon as the first replay write flushes the
// headers.
func connectSSEPublic(t *testing.T, base, token, lastEventID string) (*http.Response, <-chan sseEvent) {
	t.Helper()
	client := &http.Client{}
	var resp *http.Response
	var err error
	deadline := time.Now().Add(3 * time.Second)
	for {
		var req *http.Request
		req, err = http.NewRequest("GET", base+"/public/stream?token="+token, nil)
		require.NoError(t, err)
		if lastEventID != "" {
			req.Header.Set("Last-Event-ID", lastEventID)
		}
		resp, err = client.Do(req)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect public SSE: %v", err)
		}
		time.Sleep(20 * time.Millisecond) // server goroutine startup retry
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, startSSEReader(resp.Body)
}

func nextEvent(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		require.True(t, ok, "stream closed while waiting for an event")
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SSE event")
		return sseEvent{}
	}
}

func expectNoEvent(t *testing.T, ch <-chan sseEvent) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("unexpected extra event %q (id %s)", ev.name, ev.id)
		}
	case <-time.After(250 * time.Millisecond): // negative observation window
	}
}

func TestStreamReplayFilteringAndAuthExpiring(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	base := startListenerApp(t, app)
	t.Cleanup(func() {
		s.Stop()
		app.Shutdown() //nolint:errcheck // shutdown after Stop is best-effort
	})

	// Ring: rev1 masters frame, rev2 foreign-session frame, rev3 own-session.
	s.publish(1, Frame{Entity: "program.changed", EntityID: 4, Action: "created"})
	foreign := int64(9)
	s.publish(1, Frame{Entity: "session.changed", EntityID: 7, SessionID: &foreign, Action: "updated"})
	own := int64(2)
	s.publish(1, Frame{Entity: "session.changed", EntityID: 8, SessionID: &own, Action: "updated"})

	expectSA := func() {
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
		mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
		mock.ExpectQuery(sqlActiveRefreshSession()).WithArgs(int64(1)).
			WillReturnRows(sqlRefreshAliveRows())
	}

	// SA replay from the start: both school frames arrive in rev order.
	expectSA()
	resp, events := connectSSE(t, base, mintAccess(t, s, saUserRow()), "1", "0")
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	ev := nextEvent(t, events)
	require.Equal(t, "program.changed", ev.name)
	require.Equal(t, "1", ev.id)
	require.Contains(t, ev.data, `"entity_id":4`)
	ev = nextEvent(t, events)
	require.Equal(t, "session.changed", ev.name)
	require.Equal(t, "2", ev.id)
	require.Contains(t, ev.data, `"session_id":9`)
	resp.Body.Close()

	// rev <= last_seen is ignored: Last-Event-ID 1 replays only rev 2+ (both
	// remaining frames — rev 1 never reappears).
	expectSA()
	resp2, events2 := connectSSE(t, base, mintAccess(t, s, saUserRow()), "1", "1")
	ev = nextEvent(t, events2)
	require.Equal(t, "2", ev.id)
	ev = nextEvent(t, events2)
	require.Equal(t, "3", ev.id)
	expectNoEvent(t, events2)
	resp2.Body.Close()

	// FAS replay with Last-Event-ID 0: rev1 (masters) and rev2 (foreign
	// session) are FILTERED — the first frame the facilitator receives is
	// rev3, a filtered rev jump is never delivered nor treated as a gap.
	mock.ExpectQuery(sqlUserByID).WithArgs(int64(3)).WillReturnRows(sqlFASRow())
	mock.ExpectQuery(q("SELECT id, session_id FROM session_groups WHERE facilitator_id = ?")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "session_id"}).AddRow(10, own))
	mock.ExpectQuery(q("SELECT DISTINCT sm.child_id FROM session_members sm")).
		WithArgs(int64(3), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"child_id"}).AddRow(30))
	mock.ExpectQuery(sqlActiveRefreshSession()).WithArgs(int64(3)).
		WillReturnRows(sqlRefreshAliveRows())
	resp3, events3 := connectSSE(t, base, mintAccess(t, s, fasUserRow()), "1", "0")
	require.Equal(t, "text/event-stream", resp3.Header.Get("Content-Type"))
	ev = nextEvent(t, events3)
	require.Equal(t, "3", ev.id, "filtered revs 1-2 must never surface to the FAS")
	require.Contains(t, ev.data, `"session_id":2`)
	expectNoEvent(t, events3)
	resp3.Body.Close()

	// auth.expiring fires at T-60 s of the token lifetime (directed, no id).
	origNow := s.now
	s.now = func() time.Time { return time.Now().Add(61*time.Second - s.cfg.AccessTTL) }
	shortTok := mintAccess(t, s, saUserRow())
	s.now = origNow
	expectSA()
	resp4, events4 := connectSSE(t, base, shortTok, "1", "999") // nothing replayed
	ev = nextEvent(t, events4)
	require.Equal(t, "auth.expiring", ev.name)
	require.Contains(t, ev.data, `"exp"`)
	resp4.Body.Close()
}

// TestStreamReplayUnionsBroadcastRingForAuthConnections (F-B16): school-0
// broadcast frames live in rings[0], and an SA connection receives them
// live — a Last-Event-ID reconnect must replay the union of the school ring
// and the broadcast ring, or every broadcast tick between last and reconnect
// is lost. A public token connection must NOT gain frame classes from
// rings[0] beyond its token scope.
func TestStreamReplayUnionsBroadcastRingForAuthConnections(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)
	base := startListenerApp(t, app)
	t.Cleanup(func() {
		s.Stop()
		app.Shutdown() //nolint:errcheck // shutdown after Stop is best-effort
	})

	expectSA := func() {
		mock.ExpectQuery(sqlUserByID).WithArgs(int64(1)).WillReturnRows(sqlSARow())
		mock.ExpectQuery(sqlSchoolActive()).WithArgs(int64(1)).
			WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
		mock.ExpectQuery(sqlActiveRefreshSession()).WithArgs(int64(1)).
			WillReturnRows(sqlRefreshAliveRows())
	}

	// Phase 1: the SA sees its school frame (rings[1] rev 1) and goes away.
	s.publish(1, Frame{Entity: "program.changed", EntityID: 4, Action: "created"})
	expectSA()
	resp, events := connectSSE(t, base, mintAccess(t, s, saUserRow()), "1", "0")
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	ev := nextEvent(t, events)
	require.Equal(t, "program.changed", ev.name)
	require.Equal(t, "1", ev.id)
	resp.Body.Close()

	// Phase 2: two broadcast ticks land in rings[0] while the client is away
	// (broadcast revs 1 and 2 — per-ring counters share the client's single
	// Last-Event-ID by design, so rev 1 counts as seen and rev 2 must come
	// back on reconnect).
	s.publish(0, Frame{Entity: "stats.changed", Action: "tick"})
	s.publish(0, Frame{Entity: "wa.changed", Action: "update"})
	expectSA()
	resp2, events2 := connectSSE(t, base, mintAccess(t, s, saUserRow()), "1", "1")
	ev = nextEvent(t, events2)
	require.Equal(t, "wa.changed", ev.name,
		"a broadcast tick stored in rings[0] must replay on an auth reconnect")
	require.Equal(t, "2", ev.id)
	expectNoEvent(t, events2)
	resp2.Body.Close()

	// The same rings must not leak to a public connection: a consent token
	// replays only its own scope out of the SCHOOL ring — the school-0
	// stats/wa frames stay invisible (no new frame classes from rings[0]).
	mock.ExpectQuery(q("FROM public_links WHERE token = ? AND revoked_at IS NULL")).
		WithArgs("tok-consent").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(q("FROM consent_links WHERE token = ?")).
		WithArgs("tok-consent").
		WillReturnRows(sqlmock.NewRows([]string{"school_id", "session_id", "child_id"}).
			AddRow(int64(1), int64(2), int64(30)))
	sid := int64(2)
	s.publishPublic(Frame{Entity: "consent.ack", SchoolID: 1, SessionID: &sid,
		ChildID: 30, Action: "acked", Decision: "APPROVED"})
	respCh, _ := FsxStartStream(t, s, base+"/public/stream?token=tok-consent",
		map[string]string{"Last-Event-ID": "0"},
		func(sub *subscription) bool { return sub.scope.Public && sub.scope.Kind == "CONSENT" })
	respPub := FsxAwaitResp(t, respCh)
	require.Equal(t, "text/event-stream", respPub.Header.Get("Content-Type"))
	eventsPub := startSSEReader(respPub.Body)

	ev = nextEvent(t, eventsPub)
	require.Equal(t, "consent.ack", ev.name, "the token scope replays from the school ring")
	require.Equal(t, "2", ev.id)
	// No stats.changed / wa.changed may surface from rings[0] (F-B16).
	expectNoEvent(t, eventsPub)
	FsxDrainSSE(respPub, eventsPub)
}

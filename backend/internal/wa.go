package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gofiber/fiber/v3"
)

const (
	waTimeout     = 10 * time.Second // 05 §5 OpenWA statement budget
	waPollTick    = 5 * time.Second  // 02 §2c wa.changed poll tick
	waCallTimeout = 5 * time.Second  // one OpenWA API call (design §5)
	// waSessionCacheTTL keeps the configured name → id resolution cheap while
	// still noticing a recreated session within a few minutes.
	waSessionCacheTTL = 5 * time.Minute
)

// errWaMissing marks a 404 for the configured session: a configuration error
// ("missing"), not an OpenWA outage ("unreachable").
var errWaMissing = errors.New("openwa session not found")

// waSession is the subset of OpenWA's session record the SPA needs.
type waSession struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Phone     string `json:"phone"`
	LastError string `json:"lastError"`
}

// waDo performs one OpenWA call with the configured API key and a per-call
// budget, returning the status code and a bounded body. Transport errors are
// never echoed to the browser (they can name the internal OpenWA URL).
func (s *Server) waDo(ctx context.Context, method, path string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, waCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, s.cfg.OpenwaBaseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-API-Key", s.cfg.OpenwaAPIKey)
	resp, err := s.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// waResolveSession maps OPENWA_SESSION (a name in our deployment) to the
// OpenWA session id, caching the result for waSessionCacheTTL. Any list
// failure or unmatched name falls back to the configured value, so a UUID
// config keeps working.
func (s *Server) waResolveSession(ctx context.Context) (string, error) {
	s.waMu.Lock()
	if s.waID != "" && s.now().Sub(s.waIDAt) < waSessionCacheTTL {
		id := s.waID
		s.waMu.Unlock()
		return id, nil
	}
	s.waMu.Unlock()

	id := s.cfg.OpenwaSession
	if code, body, err := s.waDo(ctx, http.MethodGet, "/api/sessions"); err == nil &&
		code >= 200 && code < 300 {
		var list []waSession
		if json.Unmarshal(body, &list) != nil {
			var wrap struct {
				Sessions []waSession `json:"sessions"`
			}
			if json.Unmarshal(body, &wrap) == nil {
				list = wrap.Sessions
			}
		}
		for _, sess := range list {
			if sess.Name == s.cfg.OpenwaSession {
				id = sess.ID
				break
			}
		}
	}

	s.waMu.Lock()
	s.waID = id
	s.waIDAt = s.now()
	s.waMu.Unlock()
	return id, nil
}

// invalidateWaID drops the cached id so the next call re-resolves after a 404.
func (s *Server) invalidateWaID() {
	s.waMu.Lock()
	s.waID = ""
	s.waIDAt = time.Time{}
	s.waMu.Unlock()
}

// waSessionState reads GET /api/sessions/{id}; 404 is errWaMissing.
func (s *Server) waSessionState(ctx context.Context, id string) (waSession, error) {
	code, body, err := s.waDo(ctx, http.MethodGet, "/api/sessions/"+url.PathEscape(id))
	if err != nil {
		return waSession{}, err
	}
	if code == http.StatusNotFound {
		return waSession{}, errWaMissing
	}
	if code < 200 || code >= 300 {
		return waSession{}, fmt.Errorf("openwa session http %d", code)
	}
	var out waSession
	if err := json.Unmarshal(body, &out); err != nil {
		return waSession{}, err
	}
	return out, nil
}

// waQR reads GET /api/sessions/{id}/qr; 400 means "no QR yet / already
// linked", which is an empty code rather than an error.
func (s *Server) waQR(ctx context.Context, id string) (string, error) {
	code, body, err := s.waDo(ctx, http.MethodGet, "/api/sessions/"+url.PathEscape(id)+"/qr")
	if err != nil {
		return "", err
	}
	if code == http.StatusBadRequest {
		return "", nil
	}
	if code < 200 || code >= 300 {
		return "", fmt.Errorf("openwa qr http %d", code)
	}
	var out struct {
		QRCode string `json:"qrCode"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	return out.QRCode, nil
}

// waStart posts POST /api/sessions/{id}/start; 400 ("already started") is
// treated as success so repeated clicks stay harmless.
func (s *Server) waStart(ctx context.Context, id string) error {
	code, _, err := s.waDo(ctx, http.MethodPost, "/api/sessions/"+url.PathEscape(id)+"/start")
	if err != nil {
		return err
	}
	if code == http.StatusBadRequest {
		return nil
	}
	if code < 200 || code >= 300 {
		return fmt.Errorf("openwa start http %d", code)
	}
	return nil
}

// RegisterWa mounts the OpenWA proxy (02 §2b): the status badge stays
// SA/ADMIN-readable; starting a session and fetching its QR are SA-only.
func (s *Server) RegisterWa(api fiber.Router) {
	api.Get("/wa/status", s.authed("ADMIN"), s.handleWaStatus)
	api.Get("/wa/session", s.authed("SA"), s.handleWaSession)
	api.Post("/wa/session/start", s.authed("SA"), s.handleWaSessionStart)
}

// sendText posts one text message to OpenWA (01 §7 verified endpoint) with
// exactly one retry. Returns OpenWA's messageId; any failure is the caller's
// WA_FAILED (per item — never a top-level envelope here).
func (s *Server) sendText(chatID, text string) (string, error) {
	payload, err := json.Marshal(map[string]string{"chatId": chatID, "text": text})
	if err != nil {
		return "", err
	}
	url := s.cfg.OpenwaBaseURL + "/api/sessions/" + s.cfg.OpenwaSession + "/messages/send-text"

	var lastErr error
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), waTimeout)
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if reqErr != nil {
			cancel()
			return "", reqErr
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", s.cfg.OpenwaAPIKey)

		resp, doErr := s.http.Do(req)
		if doErr != nil {
			lastErr = doErr
			cancel()
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		cancel()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("openwa http %d: %.200s", resp.StatusCode, body)
			continue
		}
		var out struct {
			MessageID string `json:"messageId"`
		}
		_ = json.Unmarshal(body, &out)
		return out.MessageID, nil
	}
	return "", lastErr
}

// waStatus is the connect-modal payload (design §7.1). Empty fields are
// omitted; connected is true only for OpenWA's `ready` state.
type waStatus struct {
	Connected bool   `json:"connected"`
	Status    string `json:"status,omitempty"`
	Session   string `json:"session,omitempty"`
	Phone     string `json:"phone,omitempty"`
	QRCode    string `json:"qr_code,omitempty"`
	LastError string `json:"last_error,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
}

// waStartRequest is the optional POST body; retry lets the modal explicitly
// resume an action_required session.
type waStartRequest struct {
	Retry bool `json:"retry"`
}

// waLoadSession resolves the configured session and reads its state. A 404
// invalidates the cached id and re-resolves once (a recreated session can
// have a new id); a second 404 surfaces as errWaMissing.
func (s *Server) waLoadSession(ctx context.Context) (string, waSession, error) {
	id, err := s.waResolveSession(ctx)
	if err != nil {
		return "", waSession{}, err
	}
	sess, err := s.waSessionState(ctx, id)
	if errors.Is(err, errWaMissing) {
		s.invalidateWaID()
		id, err = s.waResolveSession(ctx)
		if err != nil {
			return "", waSession{}, err
		}
		sess, err = s.waSessionState(ctx, id)
	}
	return id, sess, err
}

// waErrorStatus maps a failed session read to the modal's synthetic status.
// errWaMissing is a configuration error; everything else is an outage. Raw
// Go errors are never copied into the payload (they can name the internal URL).
func (s *Server) waErrorStatus(err error) waStatus {
	if errors.Is(err, errWaMissing) {
		return waStatus{Status: "missing", Session: s.cfg.OpenwaSession}
	}
	return waStatus{Status: "unreachable"}
}

// waStatusFor assembles the payload; a QR is fetched only while the session
// is not ready, and a 400 (no QR yet / already linked) is simply omitted.
func (s *Server) waStatusFor(ctx context.Context, id string, sess waSession) waStatus {
	name := sess.Name
	if name == "" {
		name = s.cfg.OpenwaSession
	}
	out := waStatus{
		Connected: sess.Status == "ready",
		Status:    sess.Status,
		Session:   name,
		Phone:     sess.Phone,
		LastError: sess.LastError,
		CheckedAt: s.now().UTC().Format(time.RFC3339),
	}
	if !out.Connected {
		if qr, err := s.waQR(ctx, id); err == nil && qr != "" {
			out.QRCode = qr
		}
	}
	return out
}

// waStartable decides whether the start endpoint may boot the session. A
// pairing in progress (initializing/qr_ready/authenticating) and a ready
// session are left alone; action_required needs the explicit Retry.
func waStartable(status string, retry bool) bool {
	switch status {
	case "created", "disconnected", "failed":
		return true
	case "action_required":
		return retry
	}
	return false
}

// handleWaSession answers GET /api/wa/session: resolve → read state → QR when
// not ready. Upstream failures become a status, never an error envelope.
func (s *Server) handleWaSession(c fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*waCallTimeout)
	defer cancel()
	id, sess, err := s.waLoadSession(ctx)
	if err != nil {
		return ok(c, s.waErrorStatus(err))
	}
	return ok(c, s.waStatusFor(ctx, id, sess))
}

// handleWaSessionStart answers POST /api/wa/session/start: start only when
// waStartable allows it, then re-read the state before returning the payload.
//
// Deliberately NOT wrapped in s.idempotent: this is a control/proxy action
// with an ephemeral response (no stored mutation), so the client's automatic
// X-Idempotency-Key is ignored.
func (s *Server) handleWaSessionStart(c fiber.Ctx) error {
	var req waStartRequest
	if !bindBody(c, &req) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*waCallTimeout)
	defer cancel()
	id, sess, err := s.waLoadSession(ctx)
	if err != nil {
		return ok(c, s.waErrorStatus(err))
	}
	if waStartable(sess.Status, req.Retry) {
		if err := s.waStart(ctx, id); err == nil {
			if next, rerr := s.waSessionState(ctx, id); rerr == nil {
				sess = next
			}
		}
	}
	return ok(c, s.waStatusFor(ctx, id, sess))
}

// handleWaStatus answers 200 ALWAYS: reachable → {connected, session,
// checked_at}; unreachable → {connected:false} — never WA_FAILED (02 §2b).
func (s *Server) handleWaStatus(c fiber.Ctx) error {
	if !s.waReady() {
		return ok(c, fiber.Map{"connected": false})
	}
	session := s.cfg.OpenwaSession
	if name, ok := s.waSessionName(); ok {
		session = name
	}
	return ok(c, fiber.Map{
		"connected":  true,
		"session":    session,
		"checked_at": s.now().UTC().Format(time.RFC3339),
	})
}

// waReady probes OpenWA /api/health/ready with a5 s budget.
func (s *Server) waReady() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.OpenwaBaseURL+"/api/health/ready", nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-API-Key", s.cfg.OpenwaAPIKey)
	resp, err := s.http.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// waSessionName is the best-effort session info call (5 s); falls back to the
// configured session id when OpenWA does not answer with a name.
func (s *Server) waSessionName() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.cfg.OpenwaBaseURL+"/api/sessions/"+s.cfg.OpenwaSession, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("X-API-Key", s.cfg.OpenwaAPIKey)
	resp, err := s.http.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", false
	}
	var info struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(body, &info) != nil || info.Name == "" {
		return "", false
	}
	return info.Name, true
}

// startWaWatcher polls OpenWA every5 s and publishes wa.changed {connected}
// only on transitions (02 §2c — the badge is event-driven, no client poll).
func (s *Server) startWaWatcher() {
	go func() {
		t := time.NewTicker(waPollTick)
		defer t.Stop()
		var last *bool
		for {
			select {
			case <-t.C:
				up := s.waReady()
				if last != nil && *last == up {
					continue
				}
				if last != nil { // first observation only sets the baseline
					s.publish(0, Frame{Entity: "wa.changed", Action: "tick", Connected: new(up)})
				}
				last = new(up)
			case <-s.stop:
				return
			}
		}
	}()
}

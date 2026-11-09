// Package internal implements the Kidversa EduTourism v2 backend: one
// vertical-slice file per domain (routes + handlers + SQL), no service /
// repository / dto layers (02 §2).
//
// envelope.go is the shared transport contract every section (B1, B2, B3, T1)
// builds on. Keep this header in sync when adding a shared helper.
//
// # Transport rule (01 §2, 06 §1)
//
// Every /api/* and /public/* handler answers HTTP 200 with ok()/okPaged() or
// fail(); only unhandled faults reach ErrorHandler → HTTP 500
// {"error":{"code":"INTERNAL"}}. Rate-limited requests stay 200 and only add a
// Retry-After header. SSE routes, health probes and /uploads/* are the
// documented exceptions (text/event-stream, plain text, static bytes).
//
// # Response helpers
//
//	ok(c, data)                             200 {"data": data}
//	okPaged(c, rows, page, perPage, total)  200 {"data": rows,
//	                                            "meta":{"page","per_page","total"}}
//	fail(c, code, message)                  200 {"error":{"code","message"}}
//	failDB(c, err)                          slog + 200 DB_UNAVAILABLE + Locals
//	                                        kvDBFail (idempotency release signal)
//	failStore(c, err)                       storeImage errRejected (disk full) →
//	                                        200 VALIDATION, any other error is
//	                                        returned unchanged (→ 500 INTERNAL
//	                                        via ErrorHandler)
//	ErrorHandler(c, err)                    app-level: 404/405 → NOT_FOUND envelope,
//	                                        413 → VALIDATION, else 500 INTERNAL
//
// Error code/message pairs: CodeX + MsgX constants (06 §1 taxonomy E1..E32+EX).
// Always pass them together: fail(c, CodeEmailTaken, MsgEmailTaken).
//
// # Request helpers
//
//	bindBody(c, &req) bool           JSON/form body; false = VALIDATION already
//	                                 written → return nil from the handler
//	idParam(c, "id") (int64, bool)   malformed/unknown route id → NOT_FOUND written
//	listParams(c, defCol, defDir, allow) (ListParams, bool)
//	                                 ?page&per_page&q&sort&dir per 05 §3 (page ≥1,
//	                                 per_page ∈ {20,50,100,1000} default 1/20, dir
//	                                 ∈ {asc,desc}, sort key validated against the
//	                                 per-route allow map API-key→SQL-expression);
//	                                 false = VALIDATION written; p.OrderBy(tiebreak)
//	                                 yields "<col> <dir>, <tiebreak> ASC" (id ASC
//	                                 always final; qualify it for joined queries)
//	dbCtx(c) (ctx, cancel)           5 s statement budget for every DB call
//
// # Auth (auth.go)
//
//	s.authed(roles ...string) fiber.Handler
//	                                  keyauth(Bearer; custom ErrorHandler → 200
//	                                  INVALID_TOKEN) → user status
//	                                  (ACCOUNT_INACTIVE) → role (SA passes all
//	                                  role checks; ADMIN passes facilitator-level
//	                                  checks; empty roles = any authenticated
//	                                  role) → tenant guard (X-Tenant-ID: non-SA
//	                                  must equal its tenant else TENANT_MISMATCH;
//	                                  inactive tenant → TENANT_INACTIVE; SA may
//	                                  target any active tenant, header optional).
//	                                  On success Locals kvUser (*userRow) and
//	                                  kvTenant (int64; 0 = SA without header).
//	                                  DB failure → DB_UNAVAILABLE.
//
// # Idempotency (idempotency.go)
//
//	s.idempotent(c, route string, fn func() error) error
//	                                  X-Idempotency-Key required (else VALIDATION).
//	                                  Claims (tenant, route, key) at REQUEST time,
//	                                  runs fn once, stores fn's written envelope for
//	                                  24 h replay; concurrent duplicate → 200
//	                                  {data:{status:"IN_PROGRESS"}}. fn must write
//	                                  its own envelope (ok/fail/failDB) and return
//	                                  nil; a non-nil error aborts and releases the
//	                                  claim. Tenant comes from Locals kvTenant
//	                                  (authed sets it; public handlers must set
//	                                  c.Locals(kvTenant, tid) before calling).
//	                                  A TOP-level DB_UNAVAILABLE (failDB or a
//	                                  direct top-level write that sets
//	                                  Locals kvDBFail) releases the claim so a
//	                                  retry re-executes; nested per-item DB codes
//	                                  inside success envelopes stay recorded.
//
// # Realtime (stream.go)
//
//	s.publish(tenantID int64, frame Frame)
//	                                  hub assigns rev + ts, appends to the per-tenant
//	                                  ring (100 events / 5 min), fans out best-effort
//	                                  (slog on drop). tenantID 0 = broadcast to SA/ADMIN
//	                                  connections (stats.changed, wa.changed).
//	                                  Call ONLY after Tx commit (+ file durability for
//	                                  photo/copy paths) — never on rollback.
//	s.publishPublic(f Frame)          public fan-out; set TenantID, SessionID, ChildID,
//	                                  Stid, Entity, Action (+ Decision for consent.ack).
//	                                  Entity "link.revoked" is terminal: matching
//	                                  connections close right after delivery. Set
//	                                  Token (the dying token) to target delivery at
//	                                  exactly that token's stream; Token is json:"-"
//	                                  (server-side filter only, never on the wire).
//	                                  Empty Token = cascade kill → terminate every
//	                                  public connection in that child+session scope.
//	s.registerScopeProvider(fn)       B2 installs the authenticated stream scope
//	                                  resolver inside RegisterSessions: fn MUST call
//	                                  the SAME scope helpers used by GET /api/sessions
//	                                  and GET /api/children (reuse, never duplicate
//	                                  auth logic). Without a provider, FAS connections
//	                                  receive only tenant-matching frames that need no
//	                                  scope (fail-closed for session/child frames).
//	                                  Signature: ScopeProvider.
//
// Wire format: id:<rev> event:<Entity> data:{entity, entity_id, rev, tenant_id,
// session_id, action, ts [, child_id, stid, pending, connected, complete,
// decision]}; heartbeat SSE comment ": ping" every 15 s; auth.expiring directed
// at T-60 s (no id). Handshake failures are ordinary 200 JSON envelopes — no
// stream opens.
//
// # Limiter factories (auth.go)
//
//	newLimiter(max int, window time.Duration, key func(fiber.Ctx) string) fiber.Handler
//	                                  FixedWindow, in-process; rejection = 200
//	                                  RATE_LIMITED + Retry-After (never 429).
//	                                  key nil → IP only. Composites: keyIP,
//	                                  keyIPEmail (IP+lowercased body email),
//	                                  keyIPQuery(param) (IP+query value, tokens).
//	                                  TrustProxy lives in fiber.Config (main.go).
//
// # Uploads (validate.go + files.go)
//
//	s.storeImage(dir string, src io.Reader, declaredType string) (string, error)
//	                                  names the extension from the CLIENT-
//	                                  DECLARED part content type (no sniff, no
//	                                  size/format check — Pembaruan 2026-10-11:
//	                                  validation is client-side only; anything
//	                                  outside image/jpeg|png|webp stores as .bin);
//	                                  writes UPLOAD_DIR/<dir>/<rand32hex>.<ext>
//	                                  via temp+fsync+atomic rename; returns the
//	                                  DB-relative path. Only a full disk wraps
//	                                  errRejected → failStore → 200 VALIDATION.
//	randHex() (string, error)         crypto/rand 16-byte hex (filenames, jtis, codes)
//	randTokenHex() (string, error)    crypto/rand 32-byte hex (consent/public link
//	                                  tokens, 05 §2 CHAR(64))
//
// # External services
//
//	s.generateNarrative(childName string, childAge int, sessionName, topicName,
//		sessionDate, assessments string) (string, error)
//	                                  renders prompts/system.md + prompts/user.md
//	                                  ({{Var}} via strings.Replace), one Gemini call
//	                                  (15 s, manual retry only); err → caller maps to
//	                                  AI_FAILED and keeps the old narrative.
//	s.sendText(chatID, text string) (string, error)
//	                                  OpenWA send-text, 1 retry, 10 s; err → caller
//	                                  maps to WA_FAILED; returns OpenWA messageId.
//	s.sendResetMail(to, code string) error
//	                                  SMTP reset mail; err → caller stamps
//	                                  mail_failed (the forgot 200 stays {sent:true}).
//	s.tplReport / s.tplConsent        preloaded templates/wa_report.md
//	                                  ({{ReportURL}} {{GalleryURL}}) and
//	                                  templates/wa_consent.md ({{ConsentURL}})
//	                                  for B3's send handlers; render with
//	                                  renderVars (ai.go).
//
// # Validation (validate.go)
//
//	validEmail, validPassword, normalizeWA, validAge, validStars, validNote,
//	validBadgePair, validTimeRange — pure predicates per 05 §4 / 06 §1; all
//	map to 200 VALIDATION at the handler. extFromDeclaredType (files naming)
//	never rejects — upload rules are client-side only (Pembaruan 2026-10-11).
package internal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/gofiber/fiber/v3"
	"golang.org/x/crypto/bcrypt"
)

// Error codes (06 §1 taxonomy E1..E32 + EX). HTTP is 200 for all of them;
// only CodeInternal ever travels with HTTP 500 (app ErrorHandler).
const (
	CodeValidation         = "VALIDATION"                // E1
	CodeEmailTaken         = "EMAIL_TAKEN"               // E2
	CodeInvalidCredentials = "INVALID_CREDENTIALS"       // E3
	CodeInvalidToken       = "INVALID_TOKEN"             // E4
	CodeAccountInactive    = "ACCOUNT_INACTIVE"          // E5
	CodeTenantInactive     = "TENANT_INACTIVE"           // E6
	CodeTenantMismatch     = "TENANT_MISMATCH"           // E7
	CodeForbiddenRole      = "FORBIDDEN_ROLE"            // E8
	CodeNotOwner           = "NOT_OWNER"                 // E9
	CodeSessionLocked      = "SESSION_LOCKED"            // E10
	CodeStateConflict      = "STATE_CONFLICT"            // E11
	CodeChildDuplicate     = "CHILD_DUPLICATE"           // E12
	CodeProgramIncomplete  = "PROGRAM_INCOMPLETE"        // E13
	CodeGroupNoFacilitator = "GROUP_WITHOUT_FACILITATOR" // E14
	CodePhotoLimit         = "PHOTO_LIMIT"               // E15
	CodeConsentLocked      = "CONSENT_LOCKED"            // E16
	CodeReportIncomplete   = "REPORT_INCOMPLETE"         // E17
	CodeNothingToNarrate   = "NOTHING_TO_NARRATE"        // E18
	CodeMissionLimit       = "MISSION_LIMIT"             // E19
	CodeAIFailed           = "AI_FAILED"                 // E20
	CodeWAFailed           = "WA_FAILED"                 // E21
	CodeMailFailed         = "MAIL_FAILED"               // E22
	CodeRateLimited        = "RATE_LIMITED"              // E23
	CodeTokenInvalid       = "TOKEN_INVALID"             // E24
	CodeResetExpired       = "RESET_EXPIRED"             // E25
	CodeResetUsed          = "RESET_USED"                // E26
	CodeDBUnavailable      = "DB_UNAVAILABLE"            // E27
	CodeCameraDenied       = "CAMERA_DENIED"             // E28 (client-side only)
	CodePhotoMissing       = "PHOTO_MISSING"             // E29 (item flag, never an envelope)
	CodeNotFound           = "NOT_FOUND"                 // E30
	CodeHasOpenGroups      = "HAS_OPEN_GROUPS"           // E31
	CodeChildInactive      = "CHILD_INACTIVE"            // E32
	CodeInternal           = "INTERNAL"                  // EX (HTTP 500 only)
)

// Default messages (06 §1 user-visible text; clients retranslate by code).
const (
	MsgValidation         = "invalid or missing field"
	MsgEmailTaken         = "email already used"
	MsgInvalidCredentials = "wrong email or password"
	MsgInvalidToken       = "invalid or expired token"
	MsgAccountInactive    = "account pending approval or deactivated"
	MsgTenantInactive     = "tenant disabled"
	MsgTenantMismatch     = "tenant changed, reloading"
	MsgForbiddenRole      = "no access"
	MsgNotOwner           = "not your group"
	MsgSessionLocked      = "session is locked"
	MsgStateConflict      = "changed elsewhere, reloading"
	MsgChildDuplicate     = "child already belongs to a group in this session"
	MsgProgramIncomplete  = "program is incomplete"
	MsgGroupNoFacilitator = "group has no facilitator"
	MsgPhotoLimit         = "maximum 10 photos"
	MsgConsentLocked      = "waiting for parent approval"
	MsgReportIncomplete   = "report incomplete"
	MsgNothingToNarrate   = "no assessments to narrate"
	MsgMissionLimit       = "maximum 4 missions"
	MsgAIFailed           = "AI failed, try again"
	MsgWAFailed           = "WhatsApp send failed"
	MsgMailFailed         = "email failed, ask an admin for the code"
	MsgRateLimited        = "too many requests, try again shortly"
	MsgTokenInvalid       = "link invalid"
	MsgResetExpired       = "code expired"
	MsgResetUsed          = "code already used"
	MsgDBUnavailable      = "service busy, retry"
	MsgCameraDenied       = "allow the camera or upload a file"
	MsgPhotoMissing       = "photo file missing"
	MsgNotFound           = "not found"
	MsgHasOpenGroups      = "reassign this user's groups first"
	MsgChildInactive      = "child inactive"
	MsgInternal           = "unexpected error"
)

// Request-context Locals keys (set by authed/idempotency/stream guards).
const (
	kvUser   = "kv.user"   // *userRow after authed
	kvClaims = "kv.claims" // *accessClaims after keyauth
	kvTenant = "kv.tenant" // int64 active tenant for this call (0 = SA, no header)
	kvScope  = "kv.scope"  // *ScopeSnapshot for the stream handshake
	kvDBFail = "kv.dbfail" // bool: a TOP-level DB_UNAVAILABLE envelope was written
)

// Server carries everything the slices share: pool, config, upload root, the
// SSE hub, template/prompt assets, HTTP client and clock (tests override now).
type Server struct {
	db     *sql.DB
	cfg    Config
	upload string
	now    func() time.Time

	stop     chan struct{}
	stopOnce sync.Once

	http       *http.Client
	dummyHash  []byte // bcrypt hash for login timing symmetry on unknown emails
	sysPrompt  string
	userPrompt string
	tplReport  string
	tplConsent string

	// wa.go: cached name → id resolution for the configured OpenWA session.
	waMu   sync.Mutex
	waID   string
	waIDAt time.Time

	// SSE hub (stream.go): ring 100 events / 5 min per tenant, fan-out subs.
	hubMu         sync.Mutex
	subs          []*subscription
	rings         map[int64][]Frame
	revs          map[int64]int64
	scopeProvider ScopeProvider
}

// NewServer loads the prompt/WhatsApp templates (boot fails loud when a repo
// asset is missing) and prepares shared runtime state.
func NewServer(db *sql.DB, cfg Config) (*Server, error) {
	sysPrompt, err := os.ReadFile("prompts/system.md")
	if err != nil {
		return nil, fmt.Errorf("read prompts/system.md: %w", err)
	}
	userPrompt, err := os.ReadFile("prompts/user.md")
	if err != nil {
		return nil, fmt.Errorf("read prompts/user.md: %w", err)
	}
	tplReport, err := os.ReadFile("templates/wa_report.md")
	if err != nil {
		return nil, fmt.Errorf("read templates/wa_report.md: %w", err)
	}
	tplConsent, err := os.ReadFile("templates/wa_consent.md")
	if err != nil {
		return nil, fmt.Errorf("read templates/wa_consent.md: %w", err)
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte("timing-equalizer"), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return &Server{
		db:         db,
		cfg:        cfg,
		upload:     cfg.UploadDir,
		now:        time.Now,
		stop:       make(chan struct{}),
		http:       &http.Client{Timeout: 15 * time.Second},
		dummyHash:  dummy,
		sysPrompt:  string(sysPrompt),
		userPrompt: string(userPrompt),
		tplReport:  string(tplReport),
		tplConsent: string(tplConsent),
		rings:      make(map[int64][]Frame),
		revs:       make(map[int64]int64),
	}, nil
}

// dbCtx is the 5 s statement budget every DB call must use (05 §5).
func dbCtx(c fiber.Ctx) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Context(), 5*time.Second)
}

// ok writes the success envelope (HTTP 200).
func ok(c fiber.Ctx, data any) error {
	return c.Status(200).JSON(fiber.Map{"data": data})
}

// okPaged writes a paged-list envelope (HTTP 200, decision 25).
func okPaged(c fiber.Ctx, rows any, page, perPage int, total int64) error {
	return c.Status(200).JSON(fiber.Map{
		"data": rows,
		"meta": fiber.Map{"page": page, "per_page": perPage, "total": total},
	})
}

// fail writes the business-failure envelope (HTTP 200).
func fail(c fiber.Ctx, code, message string) error {
	return c.Status(200).JSON(fiber.Map{"error": fiber.Map{"code": code, "message": message}})
}

// failDB logs err and answers 200 DB_UNAVAILABLE (06 §2 propagation), flagging
// the request so idempotency releases the claim instead of recording a
// transient failure (typed signal — F-02-04; NESTED per-item DB codes never
// set the flag, so success envelopes with per-item codes stay recorded).
func failDB(c fiber.Ctx, err error) error {
	slog.Error("db failure", "err", err, "method", c.Method(), "path", c.Path(), "request_id", c.RequestID())
	c.Locals(kvDBFail, true)
	return fail(c, CodeDBUnavailable, MsgDBUnavailable)
}

// failStore maps storeImage errors: errRejected (disk full) → 200 VALIDATION,
// anything else is returned so the app ErrorHandler turns it into 500 INTERNAL.
func failStore(c fiber.Ctx, err error) error {
	if errors.Is(err, errRejected) {
		return fail(c, CodeValidation, MsgValidation)
	}
	return err
}

// ErrorHandler is the app-level fiber error handler (main.go): unmatched
// routes become 200 NOT_FOUND envelopes, oversize bodies 200 VALIDATION, and
// every other error is the system's only HTTP 500 {"error":{"code":"INTERNAL"}}.
func ErrorHandler(c fiber.Ctx, err error) error {
	var fErr *fiber.Error
	if errors.As(err, &fErr) {
		switch fErr.Code {
		case fiber.StatusNotFound, fiber.StatusMethodNotAllowed:
			return fail(c, CodeNotFound, MsgNotFound)
		case fiber.StatusRequestEntityTooLarge:
			return fail(c, CodeValidation, MsgValidation)
		}
	}
	slog.Error("unhandled error", "err", err, "method", c.Method(), "path", c.Path(), "request_id", c.RequestID())
	return c.Status(500).JSON(fiber.Map{"error": fiber.Map{"code": CodeInternal, "message": MsgInternal}})
}

// bindBody binds the JSON/form body; false means VALIDATION was written and
// the handler must return nil.
func bindBody(c fiber.Ctx, out any) bool {
	if err := c.Bind().Body(out); err != nil {
		fail(c, CodeValidation, MsgValidation)
		return false
	}
	return true
}

// idParam parses a numeric route parameter via Bind().URI (Bind-first, 01 §2);
// false means NOT_FOUND was written — malformed ids behave as unknown ids
// (no oracle, 06 §1 E30).
func idParam(c fiber.Ctx, name string) (int64, bool) {
	var params map[string]string
	_ = c.Bind().URI(&params)
	raw := params[name]
	if raw == "" {
		fail(c, CodeNotFound, MsgNotFound)
		return 0, false
	}
	for i := range len(raw) {
		if raw[i] < '0' || raw[i] > '9' {
			fail(c, CodeNotFound, MsgNotFound)
			return 0, false
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id == 0 {
		fail(c, CodeNotFound, MsgNotFound)
		return 0, false
	}
	return id, true
}

// ListParams holds validated server-side paging/sorting for paged lists
// (05 §3). Build with listParams, order with OrderBy.
type ListParams struct {
	Page    int
	PerPage int
	Offset  int
	Q       string
	Sort    string // API key exactly as sent ("" = route default)
	Dir     string // "asc" | "desc"
	column  string // resolved SQL expression for ORDER BY
}

// OrderBy returns "<expr> <dir>, <tiebreak> ASC" — id ASC is the final
// tiebreak everywhere (05 §3). Pass the id column, qualified when the route
// query joins a second table that also has id (e.g. "rt.id").
func (p ListParams) OrderBy(tiebreak string) string {
	return p.column + " " + p.Dir + ", " + tiebreak + " ASC"
}

// listParams parses ?page&per_page&q&sort&dir for one paged route. defCol/defDir
// are the route default sort ("id","asc" …), allow maps this route's sort keys
// to SQL expressions. false = VALIDATION already written (the caller must
// return nil without writing a second envelope).
func listParams(c fiber.Ctx, defCol, defDir string, allow map[string]string) (ListParams, bool) {
	invalid := func() (ListParams, bool) {
		fail(c, CodeValidation, MsgValidation)
		return ListParams{}, false
	}
	var raw struct {
		Page    string `query:"page"`
		PerPage string `query:"per_page"`
		Q       string `query:"q"`
		Sort    string `query:"sort"`
		Dir     string `query:"dir"`
	}
	if err := c.Bind().Query(&raw); err != nil {
		return invalid()
	}

	page := 1
	if raw.Page != "" {
		n, err := strconv.Atoi(raw.Page)
		if err != nil || n < 1 {
			return invalid()
		}
		page = n
	}
	perPage := 20
	if raw.PerPage != "" {
		n, err := strconv.Atoi(raw.PerPage)
		if err != nil || (n != 20 && n != 50 && n != 100 && n != 1000) {
			return invalid()
		}
		perPage = n
	}
	dir := raw.Dir
	if dir != "" && dir != "asc" && dir != "desc" {
		return invalid()
	}

	p := ListParams{
		Page:    page,
		PerPage: perPage,
		Offset:  (page - 1) * perPage,
		Q:       raw.Q,
		Sort:    raw.Sort,
	}
	switch {
	case raw.Sort == "" && dir == "":
		p.column, p.Dir = defCol, defDir
	case raw.Sort == "":
		p.column, p.Dir = defCol, dir
	default:
		col, ok := allow[raw.Sort]
		if !ok {
			return invalid()
		}
		p.column = col
		if dir == "" {
			dir = "asc"
		}
		p.Dir = dir
	}
	return p, true
}

// isDuplicateErr reports the MySQL unique-key violation (1062).
func isDuplicateErr(err error) bool {
	var mErr *mysql.MySQLError
	return errors.As(err, &mErr) && mErr.Number == 1062
}

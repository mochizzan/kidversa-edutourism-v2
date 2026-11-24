package internal

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/extractors"
	"github.com/gofiber/fiber/v3/middleware/keyauth"
	"github.com/gofiber/fiber/v3/middleware/limiter"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// refreshCookie is the HttpOnly refresh token cookie (01 §8).
const refreshCookie = "refresh_token"

// kvRefreshClaims is the Locals key for the parsed refresh JWT.
const kvRefreshClaims = "kv.rclaims"

// leeway covers clock skew (01 §3, 06 §5).
const jwtLeeway = 60 * time.Second

const userSelect = `SELECT u.id, u.name, u.email, u.wa_number, u.password_hash, u.role,
	u.decision, u.is_active, u.school_id, sch.is_active, u.created_at, u.photo_path
	FROM users u LEFT JOIN schools sch ON sch.id = u.school_id `

// userRow is the loaded users row (+ joined school state).
type userRow struct {
	ID           int64
	Name         string
	Email        string
	WaNumber     string
	PasswordHash string
	Role         string
	Decision     string
	IsActive     bool
	SchoolID     int64 // 0 for SA (NULL)
	SchoolActive bool
	CreatedAt    time.Time
	PhotoPath    string // DB-relative avatar path, "" = none (spec 09)
}

type userJSON struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Decision  string    `json:"decision"`
	IsActive  bool      `json:"is_active"`
	SchoolID  *int64    `json:"school_id"`  // null for SA
	WaNumber  *string   `json:"wa_number"`  // null when unset
	CreatedAt time.Time `json:"created_at"` // RFC3339 UTC
	// photo_url only on the AUTH-shaped payloads (login, refresh-restore,
	// /me, profile PATCH, photo POST/DELETE); omitempty keeps every existing
	// JSON-pinning fixture byte-identical for accounts without an avatar.
	PhotoURL string `json:"photo_url,omitempty"`
}

func (u *userRow) json() userJSON {
	out := userJSON{
		ID: u.ID, Name: u.Name, Email: u.Email, Role: u.Role,
		Decision: u.Decision, IsActive: u.IsActive,
		CreatedAt: u.CreatedAt.UTC(),
	}
	if u.Role != "SA" {
		out.SchoolID = new(u.SchoolID)
	}
	if u.WaNumber != "" {
		out.WaNumber = new(u.WaNumber)
	}
	if u.PhotoPath != "" {
		out.PhotoURL = "/uploads/" + u.PhotoPath
	}
	return out
}

type schoolJSON struct {
	ID         int64     `json:"id"`
	SchoolName string    `json:"school_name"`
	Country    string    `json:"country"`
	City       *string   `json:"city"` // null when unset
	IsActive   bool      `json:"is_active"`
	CreatedAt  time.Time `json:"created_at"` // RFC3339 UTC, matches schools.go schoolItem (F-B05)
}

// schoolBrief is the public register picker shape {id,school_name} (active
// only). Decision 8: country/city never appear in the switcher.
type schoolBrief struct {
	ID         int64  `json:"id"`
	SchoolName string `json:"school_name"`
}

// accessClaims / refreshClaims: HS256 pinned, 60 s leeway (01 §3).
type accessClaims struct {
	jwt.RegisteredClaims
	Role   string `json:"role"`
	School int64  `json:"school"`
}

type refreshClaims struct {
	jwt.RegisteredClaims
	Role   string `json:"role"`
	School int64  `json:"school"`
}

// ---------------------------------------------------------------------------
// Passwords & tokens
// ---------------------------------------------------------------------------

func hashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

func checkPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (s *Server) signAccess(u *userRow) (string, error) {
	now := s.now()
	var school int64
	if u.Role != "SA" {
		school = u.SchoolID
	}
	claims := accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(u.ID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.AccessTTL)),
		},
		Role:   u.Role,
		School: school,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.cfg.JWTAccessSecret))
}

// parseAccess validates an access JWT: HS256 only, exp required, 60 s leeway.
func (s *Server) parseAccess(token string) (*accessClaims, error) {
	claims := &accessClaims{}
	if _, err := jwt.ParseWithClaims(token, claims,
		func(_ *jwt.Token) (any, error) { return []byte(s.cfg.JWTAccessSecret), nil },
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithLeeway(jwtLeeway),
		jwt.WithExpirationRequired(),
	); err != nil {
		return nil, err
	}
	if _, err := strconv.ParseInt(claims.Subject, 10, 64); err != nil {
		return nil, err
	}
	return claims, nil
}

func (s *Server) signRefresh(u *userRow, jti string, exp time.Time) (string, error) {
	var school int64
	if u.Role != "SA" {
		school = u.SchoolID
	}
	claims := refreshClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(u.ID, 10),
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(s.now()),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		Role:   u.Role,
		School: school,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.cfg.JWTRefreshSecret))
}

func (s *Server) parseRefresh(token string) (*refreshClaims, error) {
	claims := &refreshClaims{}
	if _, err := jwt.ParseWithClaims(token, claims,
		func(_ *jwt.Token) (any, error) { return []byte(s.cfg.JWTRefreshSecret), nil },
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithLeeway(jwtLeeway),
		jwt.WithExpirationRequired(),
	); err != nil {
		return nil, err
	}
	if _, err := strconv.ParseInt(claims.Subject, 10, 64); err != nil {
		return nil, err
	}
	if claims.ID == "" {
		return nil, jwt.ErrTokenRequiredClaimMissing
	}
	return claims, nil
}

// ---------------------------------------------------------------------------
// Guards
// ---------------------------------------------------------------------------

// roleAllowed: SA passes every role check (superset); ADMIN also passes
// facilitator-level checks; empty roles = any authenticated role (02 pipeline).
func roleAllowed(roles []string, role string) bool {
	if role == "SA" || len(roles) == 0 {
		return true
	}
	for _, r := range roles {
		if r == role {
			return true
		}
		if role == "ADMIN" && r == "FASILITATOR" {
			return true
		}
	}
	return false
}

// authed is the protected-route chain: keyauth(Bearer) → user status →
// RequireRole → school guard. Failures are ordinary 200 envelopes; success
// sets kvUser + kvSchool Locals and calls c.Next() (see envelope.go header).
func (s *Server) authed(roles ...string) fiber.Handler {
	return keyauth.New(keyauth.Config{
		Extractor: extractors.FromAuthHeader("Bearer"),
		Validator: func(c fiber.Ctx, key string) (bool, error) {
			claims, err := s.parseAccess(key)
			if err != nil {
				return false, nil
			}
			c.Locals(kvClaims, claims)
			return true, nil
		},
		SuccessHandler: func(c fiber.Ctx) error { return s.authorize(c, roles) },
		ErrorHandler: func(c fiber.Ctx, _ error) error {
			return fail(c, CodeInvalidToken, MsgInvalidToken)
		},
	})
}

// refreshAuth is the cookie-only keyauth instance for POST /auth/refresh.
func (s *Server) refreshAuth() fiber.Handler {
	return keyauth.New(keyauth.Config{
		Extractor: extractors.FromCookie(refreshCookie),
		Validator: func(c fiber.Ctx, key string) (bool, error) {
			claims, err := s.parseRefresh(key)
			if err != nil {
				return false, nil
			}
			c.Locals(kvRefreshClaims, claims)
			return true, nil
		},
		ErrorHandler: func(c fiber.Ctx, _ error) error {
			return fail(c, CodeInvalidToken, MsgInvalidToken)
		},
	})
}

// authorize is the RequireRole + school-guard stage behind authed().
func (s *Server) authorize(c fiber.Ctx, roles []string) error {
	claims, _ := c.Locals(kvClaims).(*accessClaims)
	if claims == nil {
		return fail(c, CodeInvalidToken, MsgInvalidToken)
	}
	userID, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil {
		return fail(c, CodeInvalidToken, MsgInvalidToken)
	}
	ctx, cancel := dbCtx(c)
	defer cancel()

	u, err := s.loadUser(ctx, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fail(c, CodeInvalidToken, MsgInvalidToken)
		}
		return failDB(c, err)
	}
	if u.Decision != "APPROVED" || !u.IsActive {
		return fail(c, CodeAccountInactive, MsgAccountInactive)
	}
	if !roleAllowed(roles, u.Role) {
		return fail(c, CodeForbiddenRole, MsgForbiddenRole)
	}

	header := c.Get("X-School-ID")
	if u.Role == "SA" {
		// SA may target any active school; the header is optional (02 §4).
		if header == "" {
			c.Locals(kvSchool, int64(0))
		} else {
			id, pErr := strconv.ParseInt(header, 10, 64)
			if pErr != nil || id <= 0 {
				slog.Warn("auth-anomaly", "user_id", u.ID, "role", u.Role, "path", c.Path(),
					"method", c.Method(), "user_school", u.SchoolID, "request_id", c.RequestID())
				return fail(c, CodeSchoolMismatch, MsgSchoolMismatch)
			}
			active, aErr := s.schoolActive(ctx, id)
			if aErr != nil {
				return failDB(c, aErr)
			}
			if !active {
				return fail(c, CodeSchoolInactive, MsgSchoolInactive)
			}
			c.Locals(kvSchool, id)
		}
	} else {
		id, pErr := strconv.ParseInt(header, 10, 64)
		if pErr != nil || id <= 0 || id != u.SchoolID {
			slog.Warn("auth-anomaly", "user_id", u.ID, "role", u.Role, "path", c.Path(),
				"method", c.Method(), "user_school", u.SchoolID, "request_id", c.RequestID())
			return fail(c, CodeSchoolMismatch, MsgSchoolMismatch)
		}
		if !u.SchoolActive {
			return fail(c, CodeSchoolInactive, MsgSchoolInactive)
		}
		c.Locals(kvSchool, id)
	}
	c.Locals(kvUser, u)
	return c.Next()
}

// ---------------------------------------------------------------------------
// User/school lookups shared by the auth handlers
// ---------------------------------------------------------------------------

func scanUser(row *sql.Row) (*userRow, error) {
	var u userRow
	var wa sql.NullString
	var schoolID sql.NullInt64
	var schoolActive sql.NullBool
	var photo sql.NullString
	if err := row.Scan(&u.ID, &u.Name, &u.Email, &wa, &u.PasswordHash, &u.Role,
		&u.Decision, &u.IsActive, &schoolID, &schoolActive, &u.CreatedAt, &photo); err != nil {
		return nil, err
	}
	u.WaNumber = wa.String
	u.SchoolID = schoolID.Int64
	u.SchoolActive = schoolActive.Valid && schoolActive.Bool
	u.PhotoPath = photo.String
	return &u, nil
}

func (s *Server) loadUser(ctx context.Context, id int64) (*userRow, error) {
	return scanUser(s.db.QueryRowContext(ctx, userSelect+"WHERE u.id = ?", id))
}

func (s *Server) userByEmail(ctx context.Context, email string) (*userRow, error) {
	return scanUser(s.db.QueryRowContext(ctx, userSelect+"WHERE u.email = ?", email))
}

func (s *Server) schoolActive(ctx context.Context, id int64) (bool, error) {
	var active bool
	err := s.db.QueryRowContext(ctx, `SELECT is_active FROM schools WHERE id = ?`, id).Scan(&active)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return active, nil
}

// scanSchoolJSON reads the shared school projection into the auth-side shape.
func scanSchoolJSON(sc scanner) (schoolJSON, error) {
	var t schoolJSON
	var city sql.NullString
	if err := sc.Scan(&t.ID, &t.SchoolName, &t.Country, &city, &t.IsActive, &t.CreatedAt); err != nil {
		return schoolJSON{}, err
	}
	if city.Valid {
		t.City = new(city.String)
	}
	t.CreatedAt = t.CreatedAt.UTC()
	return t, nil
}

func (s *Server) schoolByID(ctx context.Context, id int64) (*schoolJSON, error) {
	t, err := scanSchoolJSON(s.db.QueryRowContext(ctx,
		`SELECT `+schoolCols+` FROM schools WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// latestActiveSchool is the SA first-login default: latest created school
// (F-01). It is also the child-create fallback source (decision 11).
func (s *Server) latestActiveSchool(ctx context.Context) (*schoolJSON, error) {
	t, err := scanSchoolJSON(s.db.QueryRowContext(ctx,
		`SELECT `+schoolCols+` FROM schools WHERE is_active = TRUE ORDER BY id DESC LIMIT 1`))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// schoolForSession resolves the school half of {access,user,school}: non-SA →
// own school; SA → the X-School-ID in play (Locals, else raw header) when it
// is an active school, else the latest active school (first-login default).
func (s *Server) schoolForSession(ctx context.Context, c fiber.Ctx, u *userRow) (*schoolJSON, error) {
	if u.Role != "SA" {
		return s.schoolByID(ctx, u.SchoolID)
	}
	var id int64
	if v, ok := c.Locals(kvSchool).(int64); ok {
		id = v
	}
	if id == 0 {
		if h := c.Get("X-School-ID"); h != "" {
			if n, err := strconv.ParseInt(h, 10, 64); err == nil && n > 0 {
				id = n
			}
		}
	}
	if id > 0 {
		t, err := s.schoolByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if t != nil && t.IsActive {
			return t, nil
		}
	}
	return s.latestActiveSchool(ctx)
}

// execer unifies *sql.DB and *sql.Tx for the refresh whitelist writes.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// insertRefresh adds one whitelist row (rotation = insert new + revoke old).
// impersonatedBy/root carry the Feature A ledger forward: a rotation of an
// impersonated session MUST copy both columns, otherwise the ledger fragments
// and the banner dies on the next reload (spec §7.2, §9.5). nil/"" = ordinary
// session.
func insertRefreshOn(ctx context.Context, q execer, u *userRow, jti string, exp time.Time, impersonatedBy *int64, root string) error {
	var school sql.NullInt64
	if u.Role != "SA" {
		school = sql.NullInt64{Int64: u.SchoolID, Valid: true}
	}
	var impBy sql.NullInt64
	if impersonatedBy != nil {
		impBy = sql.NullInt64{Int64: *impersonatedBy, Valid: true}
	}
	var impRoot sql.NullString
	if root != "" {
		impRoot = sql.NullString{String: root, Valid: true}
	}
	_, err := q.ExecContext(ctx,
		`INSERT INTO refresh_tokens (school_id, user_id, jti, expires_at, impersonated_by, impersonation_root)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		school, u.ID, jti, exp, impBy, impRoot)
	return err
}

// setRefreshCookie issues the HttpOnly cookie (Path=/, SameSite=Lax, +Secure
// when PUBLIC_WEB_BASE_URL is https).
func (s *Server) setRefreshCookie(c fiber.Ctx, token string, exp time.Time) {
	c.Cookie(&fiber.Cookie{
		Name:     refreshCookie,
		Value:    token,
		Path:     "/",
		Expires:  exp,
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteLaxMode,
		Secure:   s.cfg.SecureCookie,
	})
}

// impersonationJSON is the additive, optional `impersonation` block of the
// session payload (spec §9.2). Its absence means "not impersonated".
type impersonationJSON struct {
	ActorID   int64  `json:"actor_id"`
	ActorName string `json:"actor_name"`
	ActorRole string `json:"actor_role"`
}

// queryer is the read half shared by *sql.DB and *sql.Tx, so the refresh
// rotation can resolve the actor without a second connection while it holds
// the row lock.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// impersonationBlock loads the actor name/role for a ledger row. Rows survive
// account deletion (spec §7.3 C10), so a missing actor degrades to the same
// placeholder the ledger reads use instead of hiding the banner.
func (s *Server) impersonationBlock(ctx context.Context, q queryer, actorID int64) (*impersonationJSON, error) {
	var name, role string
	err := q.QueryRowContext(ctx, `SELECT name, role FROM users WHERE id = ?`, actorID).
		Scan(&name, &role)
	if err == sql.ErrNoRows {
		return &impersonationJSON{ActorID: actorID, ActorName: fmt.Sprintf("[deleted #%d]", actorID)}, nil
	}
	if err != nil {
		return nil, err
	}
	return &impersonationJSON{ActorID: actorID, ActorName: name, ActorRole: role}, nil
}

// respondSession writes {access,user,school} + the refresh cookie. The caller
// must already have the whitelist row (login insert / refresh rotation).
func (s *Server) respondSession(ctx context.Context, c fiber.Ctx, u *userRow, jti string, exp time.Time) error {
	school, err := s.schoolForSession(ctx, c, u)
	if err != nil {
		return failDB(c, err)
	}
	return s.writeSession(c, u, jti, exp, school, nil)
}

// writeSession signs the access/refresh pair, sets the cookie and emits the
// session envelope. A non-nil imp adds the additive `impersonation` block; the
// caller supplies the school explicitly because an impersonation switch must
// answer with the TARGET's school, never the actor's X-School-ID (spec §9.2).
func (s *Server) writeSession(c fiber.Ctx, u *userRow, jti string, exp time.Time, school *schoolJSON, imp *impersonationJSON) error {
	access, err := s.signAccess(u)
	if err != nil {
		return err
	}
	refresh, err := s.signRefresh(u, jti, exp)
	if err != nil {
		return err
	}
	s.setRefreshCookie(c, refresh, exp)
	payload := fiber.Map{"access": access, "user": u.json(), "school": school}
	if imp != nil {
		payload["impersonation"] = imp
	}
	return ok(c, payload)
}

// ---------------------------------------------------------------------------
// Limiter factories (01 §2): FixedWindow, composite keys, 200 + Retry-After
// ---------------------------------------------------------------------------

// newLimiter builds one limiter instance. Each instance owns its bucket (the
// v3 limiter keeps per-instance memory), so keys need no route prefix.
// key nil → IP only. Rejection is ALWAYS HTTP 200 RATE_LIMITED + Retry-After.
func newLimiter(max int, window time.Duration, key func(fiber.Ctx) string) fiber.Handler {
	if key == nil {
		key = keyIP
	}
	retry := strconv.Itoa(int((window + time.Second - 1) / time.Second))
	return limiter.New(limiter.Config{
		Max:               max,
		Expiration:        window,
		LimiterMiddleware: limiter.FixedWindow{},
		KeyGenerator:      key,
		LimitReached: func(c fiber.Ctx) error {
			if c.Get(fiber.HeaderRetryAfter) == "" {
				c.Set(fiber.HeaderRetryAfter, retry)
			}
			return fail(c, CodeRateLimited, MsgRateLimited)
		},
	})
}

// keyIP is the plain per-IP key (register, refresh, auth-schools picker).
func keyIP(c fiber.Ctx) string { return c.IP() }

// keyIPEmail is the NAT-safe composite IP+lowercased-email key (login/forgot/
// reset): schools share one public IP, so a bare-IP bucket would collapse.
func keyIPEmail(c fiber.Ctx) string {
	var body struct {
		Email string `json:"email"`
	}
	_ = c.Bind().Body(&body)
	return c.IP() + "|" + strings.ToLower(strings.TrimSpace(body.Email))
}

// keyIPQuery is the IP+query-value composite (public GET token limiter).
func keyIPQuery(param string) func(fiber.Ctx) string {
	ex := extractors.FromQuery(param)
	return func(c fiber.Ctx) string {
		v, _ := ex.Extract(c)
		return c.IP() + "|" + v
	}
}

// ---------------------------------------------------------------------------
// Routes
// ---------------------------------------------------------------------------

// RegisterAuth mounts /api/auth/* and /api/reset-tokens/* (main.go calls it;
// per-route limiters and guards attach here, per 02 §2b).
func (s *Server) RegisterAuth(api fiber.Router) {
	api.Post("/auth/register", newLimiter(5, time.Minute, keyIP), s.handleRegister)
	api.Post("/auth/login", newLimiter(10, time.Minute, keyIPEmail), s.handleLogin)
	api.Post("/auth/forgot", newLimiter(3, 15*time.Minute, keyIPEmail), s.handleForgot)
	api.Post("/auth/reset", newLimiter(5, 15*time.Minute, keyIPEmail), s.handleReset)
	api.Post("/auth/refresh", newLimiter(30, time.Minute, keyIP), s.refreshAuth(), s.handleRefresh)
	api.Post("/auth/logout", s.authed(), s.handleLogout)
	api.Get("/auth/me", s.authed(), s.handleMe)
	api.Patch("/auth/profile", s.authed(), s.handleProfile)
	api.Post("/auth/profile-photo", s.authed(), s.handleProfilePhotoPost)
	api.Delete("/auth/profile-photo", s.authed(), s.handleProfilePhotoDelete)
	api.Post("/auth/change-password", s.authed(), s.handleChangePassword)
	api.Post("/auth/switch-school", s.authed(), s.handleSwitchSchool)
	api.Get("/auth/schools", newLimiter(60, time.Minute, keyIP), s.handleAuthSchools)

	api.Get("/reset-tokens", s.authed("ADMIN"), s.handleResetList)
	api.Post("/reset-tokens/:id/reveal", s.authed("ADMIN"), s.handleResetReveal)
	api.Post("/reset-tokens/:id/revoke", s.authed("ADMIN"), s.handleResetRevoke)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

type registerReq struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	WaNumber string `json:"wa_number"`
	Password string `json:"password"`
	Role     string `json:"role"`
	SchoolID int64  `json:"school_id"`
}

// handleRegister: 5/min/IP + X-Idempotency-Key; insert PENDING/inactive →
// {data:{id,status:"pending"}} (FL-01).
func (s *Server) handleRegister(c fiber.Ctx) error {
	var req registerReq
	if !bindBody(c, &req) {
		return nil
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Name == "" || len(req.Name) > maxPersonNameLen || !validEmail(req.Email) || !validPassword(req.Password) {
		return fail(c, CodeValidation, MsgValidation)
	}
	if req.Role == "SA" {
		return fail(c, CodeForbiddenRole, MsgForbiddenRole)
	}
	if req.Role != "ADMIN" && req.Role != "FASILITATOR" {
		return fail(c, CodeValidation, MsgValidation)
	}
	wa, waOK := normalizeWA(req.WaNumber)
	if !waOK {
		return fail(c, CodeValidation, MsgValidation)
	}
	if req.SchoolID <= 0 {
		return fail(c, CodeValidation, MsgValidation)
	}

	ctx, cancel := dbCtx(c)
	active, err := s.schoolActive(ctx, req.SchoolID)
	cancel()
	if err != nil {
		return failDB(c, err)
	}
	if !active { // unknown or inactive school → E6 (deleted schools behave inactive)
		return fail(c, CodeSchoolInactive, MsgSchoolInactive)
	}
	c.Locals(kvSchool, req.SchoolID) // idempotency scope

	return s.idempotent(c, "POST /api/auth/register", func() error {
		ctx, cancel := dbCtx(c)
		defer cancel()
		var exists int
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM users WHERE email = ?)`, req.Email).Scan(&exists); err != nil {
			return failDB(c, err)
		}
		if exists == 1 {
			return fail(c, CodeEmailTaken, MsgEmailTaken)
		}
		hash, err := hashPassword(req.Password)
		if err != nil {
			return err
		}
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO users (school_id, name, email, wa_number, password_hash, role, decision, is_active)
			 VALUES (?, ?, ?, ?, ?, ?, 'PENDING', FALSE)`,
			req.SchoolID, req.Name, req.Email, wa, hash, req.Role)
		if err != nil {
			if isDuplicateErr(err) {
				return fail(c, CodeEmailTaken, MsgEmailTaken)
			}
			return failDB(c, err)
		}
		id, _ := res.LastInsertId()
		s.publish(req.SchoolID, Frame{Entity: "user.changed", EntityID: id, Action: "created"})
		var pending int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM users WHERE school_id = ? AND decision = 'PENDING'`,
			req.SchoolID).Scan(&pending); err != nil {
			slog.Error("pending count after register", "err", err)
		} else {
			s.publish(req.SchoolID, Frame{Entity: "pending.changed", Action: "updated", Pending: new(pending)})
		}
		return ok(c, fiber.Map{"id": id, "status": "pending"})
	})
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleLogin: 10/min IP+email; decision+is_active+school first, then bcrypt;
// generic INVALID_CREDENTIALS (no oracle); sets the refresh cookie (FL-02).
func (s *Server) handleLogin(c fiber.Ctx) error {
	var req loginReq
	if !bindBody(c, &req) {
		return nil
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" {
		return fail(c, CodeInvalidCredentials, MsgInvalidCredentials)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	u, err := s.userByEmail(ctx, email)
	if err != nil && err != sql.ErrNoRows {
		return failDB(c, err)
	}
	if u == nil {
		// Equalize timing against existing accounts.
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(req.Password))
		return fail(c, CodeInvalidCredentials, MsgInvalidCredentials)
	}
	if u.Decision != "APPROVED" || !u.IsActive {
		return fail(c, CodeAccountInactive, MsgAccountInactive)
	}
	if u.Role != "SA" && !u.SchoolActive {
		return fail(c, CodeSchoolInactive, MsgSchoolInactive)
	}
	if !checkPassword(u.PasswordHash, req.Password) {
		return fail(c, CodeInvalidCredentials, MsgInvalidCredentials)
	}

	jti, err := randHex()
	if err != nil {
		return err
	}
	exp := s.now().Add(s.cfg.RefreshTTL)
	if err := insertRefreshOn(ctx, s.db, u, jti, exp, nil, ""); err != nil {
		return failDB(c, err)
	}
	return s.respondSession(ctx, c, u, jti, exp)
}

type forgotReq struct {
	Email string `json:"email"`
}

// handleForgot: ALWAYS 200 {sent:true} (no enumeration);8-char crypto/rand
// code, SHA-256 hash + plaintext for relay,15 min expiry; SMTP failure stamps
// mail_failed (the dashboard's MAIL_FAILED signal,06 §3).
func (s *Server) handleForgot(c fiber.Ctx) error {
	var req forgotReq
	if !bindBody(c, &req) {
		return nil
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))

	ctx, cancel := dbCtx(c)
	defer cancel()
	// ALWAYS 200 {sent:true} — even for a malformed/unknown address (no
	// enumeration, 06 §2); a syntactically invalid email simply has no user.
	u, err := s.userByEmail(ctx, email)
	if err != nil && err != sql.ErrNoRows {
		return failDB(c, err)
	}
	if u == nil {
		return ok(c, fiber.Map{"sent": true})
	}

	// On-access sweep: drop this user's expired SELF-service rows before
	// issuing a new code. ADMIN ledger rows are immutable and kept 90 days.
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM reset_tokens WHERE user_id = ? AND expires_at < NOW() AND source = 'SELF'`, u.ID); err != nil {
		return failDB(c, err)
	}
	codeRaw, err := randBytes(4)
	if err != nil {
		return err
	}
	code := hex.EncodeToString(codeRaw) // 8 chars
	var school sql.NullInt64
	if u.Role != "SA" {
		school = sql.NullInt64{Int64: u.SchoolID, Valid: true}
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO reset_tokens (school_id, user_id, code_hash, code_plain, expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		school, u.ID, sha256Hex(code), code, s.now().Add(s.cfg.ResetTTL))
	if err != nil {
		return failDB(c, err)
	}
	rowID, _ := res.LastInsertId()

	if mailErr := s.sendResetMail(u.Email, code); mailErr != nil {
		slog.Warn("reset mail failed", "err", mailErr, "user_id", u.ID)
		if _, mErr := s.db.ExecContext(ctx,
			`UPDATE reset_tokens SET mail_failed = TRUE WHERE id = ?`, rowID); mErr != nil {
			slog.Error("stamp mail_failed", "err", mErr)
		}
	}
	schoolPub := int64(0)
	if u.Role != "SA" {
		schoolPub = u.SchoolID
	}
	s.publish(schoolPub, Frame{Entity: "reset.changed", EntityID: rowID, Action: "created"})
	return ok(c, fiber.Map{"sent": true})
}

type resetReq struct {
	Email       string `json:"email"`
	Code        string `json:"code"`
	NewPassword string `json:"new_password"`
}

// handleReset: constant-time hash compare (RESET_EXPIRED / RESET_USED / wrong
// code → VALIDATION), bcrypt the new password, one Tx stamps the code used
// (clears code_plain), revokes ALL refresh rows and rotates the password.
func (s *Server) handleReset(c fiber.Ctx) error {
	var req resetReq
	if !bindBody(c, &req) {
		return nil
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	req.Code = strings.TrimSpace(req.Code)
	if !validEmail(email) || req.Code == "" || !validPassword(req.NewPassword) {
		return fail(c, CodeValidation, MsgValidation)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	u, err := s.userByEmail(ctx, email)
	if err != nil && err != sql.ErrNoRows {
		return failDB(c, err)
	}
	if u == nil {
		return fail(c, CodeValidation, MsgValidation)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, code_hash, expires_at, used_at, revoked_at
		 FROM reset_tokens WHERE user_id = ? AND source = 'SELF' ORDER BY id DESC LIMIT 20`, u.ID)
	if err != nil {
		return failDB(c, err)
	}
	type candidate struct {
		id      int64
		exp     time.Time
		used    sql.NullTime
		revoked sql.NullTime
	}
	want := sha256Hex(req.Code)
	var found *candidate
	var matched bool
	now := s.now()
	for rows.Next() {
		var cand candidate
		var hash string
		if err := rows.Scan(&cand.id, &hash, &cand.exp, &cand.used, &cand.revoked); err != nil {
			rows.Close()
			return failDB(c, err)
		}
		if subtle.ConstantTimeCompare([]byte(hash), []byte(want)) == 1 {
			matched = true
			found = &cand
			break
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	if !matched {
		return fail(c, CodeValidation, MsgValidation)
	}
	if found.revoked.Valid || found.used.Valid {
		return fail(c, CodeResetUsed, MsgResetUsed)
	}
	if !found.exp.After(now) {
		return fail(c, CodeResetExpired, MsgResetExpired)
	}

	newHash, err := hashPassword(req.NewPassword)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE reset_tokens SET used_at = NOW(), code_plain = NULL
		 WHERE id = ? AND used_at IS NULL AND revoked_at IS NULL`, found.id)
	if err != nil {
		return failDB(c, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fail(c, CodeResetUsed, MsgResetUsed) // lost the single-use race
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = ? AND revoked_at IS NULL`,
		u.ID); err != nil {
		return failDB(c, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, newHash, u.ID); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}

	schoolPub := int64(0)
	if u.Role != "SA" {
		schoolPub = u.SchoolID
	}
	s.publish(schoolPub, Frame{Entity: "reset.changed", EntityID: found.id, Action: "used"})
	return ok(c, fiber.Map{"reset": true})
}

// handleRefresh: cookie-only; single-use rotation in ONE Tx (lock old row →
// revoke → insert new). Parallel loser → INVALID_TOKEN; Tx failure →
// DB_UNAVAILABLE with the OLD jti intact (06 §3).
func (s *Server) handleRefresh(c fiber.Ctx) error {
	claims, _ := c.Locals(kvRefreshClaims).(*refreshClaims)
	if claims == nil {
		return fail(c, CodeInvalidToken, MsgInvalidToken)
	}
	userID, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil {
		return fail(c, CodeInvalidToken, MsgInvalidToken)
	}

	ctx, cancel := dbCtx(c)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()

	var revoked sql.NullTime
	var impBy sql.NullInt64
	var impRoot sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT revoked_at, impersonated_by, impersonation_root FROM refresh_tokens WHERE jti = ? FOR UPDATE`, claims.ID).
		Scan(&revoked, &impBy, &impRoot); err != nil {
		if err == sql.ErrNoRows {
			return fail(c, CodeInvalidToken, MsgInvalidToken)
		}
		return failDB(c, err)
	}
	if revoked.Valid {
		return fail(c, CodeInvalidToken, MsgInvalidToken) // double-refresh loser
	}
	u, err := scanUser(tx.QueryRowContext(ctx, userSelect+"WHERE u.id = ?", userID))
	if err == sql.ErrNoRows {
		return fail(c, CodeInvalidToken, MsgInvalidToken)
	}
	if err != nil {
		return failDB(c, err)
	}
	if u.Decision != "APPROVED" || !u.IsActive {
		return fail(c, CodeAccountInactive, MsgAccountInactive)
	}

	newJti, err := randHex()
	if err != nil {
		return err
	}
	newExp := s.now().Add(s.cfg.RefreshTTL)
	if _, err := tx.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked_at = NOW() WHERE jti = ?`, claims.ID); err != nil {
		return failDB(c, err)
	}
	// Propagate the impersonation ledger onto the rotated row, and resolve the
	// banner block inside the Tx: a failure must abort the rotation instead of
	// committing a session whose identity we cannot describe.
	var imp *impersonationJSON
	if impBy.Valid {
		imp, err = s.impersonationBlock(ctx, tx, impBy.Int64)
		if err != nil {
			return failDB(c, err)
		}
	}
	var impActor *int64
	if impBy.Valid {
		impActor = new(impBy.Int64)
	}
	if err := insertRefreshOn(ctx, tx, u, newJti, newExp, impActor, impRoot.String); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err) // rollback keeps the old jti valid
	}
	school, err := s.schoolForSession(ctx, c, u)
	if err != nil {
		return failDB(c, err)
	}
	return s.writeSession(c, u, newJti, newExp, school, imp)
}

// handleLogout revokes the cookie's jti and clears the cookie; always 200.
func (s *Server) handleLogout(c fiber.Ctx) error {
	if tok := c.Cookies(refreshCookie); tok != "" {
		if claims, err := s.parseRefresh(tok); err == nil {
			ctx, cancel := dbCtx(c)
			if _, err := s.db.ExecContext(ctx,
				`UPDATE refresh_tokens SET revoked_at = NOW() WHERE jti = ? AND revoked_at IS NULL`,
				claims.ID); err != nil {
				slog.Error("logout revoke", "err", err)
			}
			cancel()
		}
	}
	c.ClearCookie(refreshCookie)
	return ok(c, fiber.Map{"logged_out": true})
}

// handleMe reuses the user row authed() already loaded (no second query).
// The impersonation block is BEST-EFFORT (spec §9.2): it is returned only when
// the request carries a valid refresh cookie whose subject is this access
// token's subject AND that whitelist row is an impersonated session. Bearer-
// only callers and multi-device users get no block rather than a guessed one;
// nothing in the app depends on /me for the banner (refresh is authoritative).
func (s *Server) handleMe(c fiber.Ctx) error {
	u, _ := c.Locals(kvUser).(*userRow)
	ctx, cancel := dbCtx(c)
	defer cancel()
	school, err := s.schoolForSession(ctx, c, u)
	if err != nil {
		return failDB(c, err)
	}
	payload := fiber.Map{"user": u.json(), "school": school}
	if tok := c.Cookies(refreshCookie); tok != "" {
		claims, rErr := s.parseRefresh(tok)
		if rErr == nil && claims.Subject == strconv.FormatInt(u.ID, 10) {
			var actor sql.NullInt64
			qErr := s.db.QueryRowContext(ctx,
				`SELECT impersonated_by FROM refresh_tokens WHERE jti = ? AND revoked_at IS NULL`,
				claims.ID).Scan(&actor)
			if qErr == nil && actor.Valid {
				if imp, bErr := s.impersonationBlock(ctx, s.db, actor.Int64); bErr == nil {
					payload["impersonation"] = imp
				}
			}
		}
	}
	return ok(c, payload)
}

type profileReq struct {
	Name string `json:"name"`
}

// handleProfile changes the caller's display name (password lives on
// change-password; email edits live on /api/users for admin rows).
func (s *Server) handleProfile(c fiber.Ctx) error {
	var req profileReq
	if !bindBody(c, &req) {
		return nil
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > maxPersonNameLen {
		return fail(c, CodeValidation, MsgValidation)
	}
	u, _ := c.Locals(kvUser).(*userRow)
	ctx, cancel := dbCtx(c)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET name = ? WHERE id = ?`, name, u.ID); err != nil {
		return failDB(c, err)
	}
	u.Name = name
	return ok(c, fiber.Map{"user": u.json()})
}

type changePasswordReq struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleProfilePhotoPost stores the composed image the editor uploads (the
// browser crops first; the server never sees crop parameters, spec 09
// decision 7) as the caller's avatar. Storage is USER-keyed avatars/<id>/
// (covers SA's school-null case); the whitelist/compression storeImage rules
// apply unchanged. The OLD photo file is unlinked only after the row update
// succeeded (frames/badges replacement pattern); the response answers
// {data:{user}} in the PATCH-profile pattern.
func (s *Server) handleProfilePhotoPost(c fiber.Ctx) error {
	u, _ := c.Locals(kvUser).(*userRow)
	fh, err := c.FormFile("file")
	if err != nil {
		return fail(c, CodeValidation, MsgValidation)
	}
	return s.idempotent(c, "POST /api/auth/profile-photo", func() error {
		ctx, cancel := dbCtx(c)
		defer cancel()

		src, err := fh.Open()
		if err != nil {
			return fail(c, CodeValidation, MsgValidation)
		}
		defer src.Close()
		rel, err := s.storeImage("avatars/"+strconv.FormatInt(u.ID, 10), src, fh.Header.Get("Content-Type"))
		if err != nil {
			return failStore(c, err)
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE users SET photo_path = ? WHERE id = ?`, rel, u.ID); err != nil {
			// Row update failed: the fresh file would be an orphan.
			os.Remove(filepath.Join(s.upload, filepath.FromSlash(rel)))
			return failDB(c, err)
		}
		// The row now points at the NEW file: the replaced one may go.
		if u.PhotoPath != "" && u.PhotoPath != rel {
			if err := os.Remove(filepath.Join(s.upload, filepath.FromSlash(u.PhotoPath))); err != nil && !os.IsNotExist(err) {
				slog.Error("profile photo old unlink", "err", err, "path", u.PhotoPath)
			}
		}
		u.PhotoPath = rel
		return ok(c, fiber.Map{"user": u.json()})
	})
}

// handleProfilePhotoDelete nulls the row (idempotent for a photo-less
// account) and unlinks the old file only after that update succeeded. No
// idempotency: DELETE is not a mutating POST (06 §3). The response is the
// refresh-recipient user shape without a photo_url key (omitempty).
func (s *Server) handleProfilePhotoDelete(c fiber.Ctx) error {
	u, _ := c.Locals(kvUser).(*userRow)
	ctx, cancel := dbCtx(c)
	defer cancel()

	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET photo_path = NULL WHERE id = ?`, u.ID); err != nil {
		return failDB(c, err)
	}
	if u.PhotoPath != "" {
		if err := os.Remove(filepath.Join(s.upload, filepath.FromSlash(u.PhotoPath))); err != nil && !os.IsNotExist(err) {
			slog.Error("profile photo delete unlink", "err", err, "path", u.PhotoPath)
		}
	}
	u.PhotoPath = ""
	return ok(c, fiber.Map{"user": u.json()})
}

// handleChangePassword verifies the current password, stores the new one and
// revokes ALL refresh rows in one Tx (other devices re-login, F-01).
func (s *Server) handleChangePassword(c fiber.Ctx) error {
	var req changePasswordReq
	if !bindBody(c, &req) {
		return nil
	}
	if !validPassword(req.NewPassword) {
		return fail(c, CodeValidation, MsgValidation)
	}
	u, _ := c.Locals(kvUser).(*userRow)
	ctx, cancel := dbCtx(c)
	defer cancel()

	var hash string
	if err := s.db.QueryRowContext(ctx,
		`SELECT password_hash FROM users WHERE id = ?`, u.ID).Scan(&hash); err != nil {
		if err == sql.ErrNoRows {
			return fail(c, CodeAccountInactive, MsgAccountInactive)
		}
		return failDB(c, err)
	}
	if !checkPassword(hash, req.CurrentPassword) {
		return fail(c, CodeInvalidCredentials, MsgInvalidCredentials)
	}
	newHash, err := hashPassword(req.NewPassword)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failDB(c, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, newHash, u.ID); err != nil {
		return failDB(c, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = ? AND revoked_at IS NULL`,
		u.ID); err != nil {
		return failDB(c, err)
	}
	if err := tx.Commit(); err != nil {
		return failDB(c, err)
	}
	return ok(c, fiber.Map{"changed": true})
}

type switchSchoolReq struct {
	SchoolID int64 `json:"school_id"`
}

// handleSwitchSchool: SA-only (others → FORBIDDEN_ROLE), validates the target
// school is active; the client keeps the SAME access token and switches
// X-School-ID (02 §4).
func (s *Server) handleSwitchSchool(c fiber.Ctx) error {
	u, _ := c.Locals(kvUser).(*userRow)
	if u.Role != "SA" {
		return fail(c, CodeForbiddenRole, MsgForbiddenRole)
	}
	var req switchSchoolReq
	if !bindBody(c, &req) {
		return nil
	}
	if req.SchoolID <= 0 {
		return fail(c, CodeValidation, MsgValidation)
	}
	ctx, cancel := dbCtx(c)
	defer cancel()
	t, err := s.schoolByID(ctx, req.SchoolID)
	if err != nil {
		return failDB(c, err)
	}
	if t == nil || !t.IsActive { // unknown behaves like deleted → E6
		return fail(c, CodeSchoolInactive, MsgSchoolInactive)
	}
	return ok(c, fiber.Map{"school": t})
}

// handleAuthSchools is the public register picker: active schools only.
func (s *Server) handleAuthSchools(c fiber.Ctx) error {
	ctx, cancel := dbCtx(c)
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, school_name FROM schools WHERE is_active = TRUE ORDER BY school_name`)
	if err != nil {
		return failDB(c, err)
	}
	defer rows.Close()
	list := make([]schoolBrief, 0, 8)
	for rows.Next() {
		var t schoolBrief
		if err := rows.Scan(&t.ID, &t.SchoolName); err != nil {
			return failDB(c, err)
		}
		list = append(list, t)
	}
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}
	return ok(c, list)
}

// ---------------------------------------------------------------------------
// Reset dashboard (05 §3 / F-01): masked list + audited reveal + revoke
// ---------------------------------------------------------------------------

type resetUserJSON struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type resetTokenJSON struct {
	ID          int64         `json:"id"`
	CodeMasked  string        `json:"code_masked"`
	ExpiresAt   time.Time     `json:"expires_at"`
	SecondsLeft int64         `json:"seconds_left"`
	User        resetUserJSON `json:"user"`
	Revoked     bool          `json:"revoked"`
	MailFailed  bool          `json:"mail_failed"`
	CodePlain   *string       `json:"code_plain,omitempty"` // only on audited reveal
}

const resetSelect = `SELECT rt.id, rt.code_plain, rt.expires_at, rt.revoked_at,
	rt.mail_failed, u.id, u.name, u.email
	FROM reset_tokens rt JOIN users u ON u.id = rt.user_id
	WHERE rt.expires_at > NOW() AND rt.used_at IS NULL AND rt.source = 'SELF' `

// handleResetList: SA sees every active code, ADMIN only own-school rows.
// Paging/sort per 05 §3 (id|expires_at|created_at, default id asc). With
// ?reveal=1 the page's plaintext codes are returned once and audited.
func (s *Server) handleResetList(c fiber.Ctx) error {
	u, _ := c.Locals(kvUser).(*userRow)
	school, _ := c.Locals(kvSchool).(int64)

	p, okList := listParams(c, "rt.id", "asc", map[string]string{
		"id": "rt.id", "expires_at": "rt.expires_at", "created_at": "rt.created_at",
	})
	if !okList {
		return nil // VALIDATION already written inside listParams
	}
	var reveal struct {
		Reveal string `query:"reveal"`
	}
	_ = c.Bind().Query(&reveal)

	scope := ""
	var scopeArgs []any
	if u.Role != "SA" {
		scope = "AND rt.school_id = ? "
		scopeArgs = append(scopeArgs, school)
	}
	// ?q (F-B15, audit C13): substring over the owning user's name/email —
	// the predicate mirrors the users list (users.go). The count query gains
	// the same JOIN so both halves apply the identical filter.
	countJoin := ""
	filter := ""
	var filterArgs []any
	if p.Q != "" {
		countJoin = "JOIN users u ON u.id = rt.user_id "
		filter = "AND (u.name LIKE ? OR u.email LIKE ?) "
		filterArgs = append(filterArgs, "%"+p.Q+"%", "%"+p.Q+"%")
	}
	ctx, cancel := dbCtx(c)
	defer cancel()

	var total int64
	countQ := `SELECT COUNT(*) FROM reset_tokens rt ` + countJoin +
		`WHERE rt.expires_at > NOW() AND rt.used_at IS NULL AND rt.source = 'SELF' ` + scope + filter
	countArgs := append(append([]any{}, scopeArgs...), filterArgs...)
	if err := s.db.QueryRowContext(ctx, countQ, countArgs...).Scan(&total); err != nil {
		return failDB(c, err)
	}

	listQ := resetSelect + scope + filter + `ORDER BY ` + p.OrderBy("rt.id") + ` LIMIT ? OFFSET ?`
	args := append(append([]any{}, countArgs...), p.PerPage, p.Offset)
	rows, err := s.db.QueryContext(ctx, listQ, args...)
	if err != nil {
		return failDB(c, err)
	}
	defer rows.Close()

	now := s.now()
	out := make([]resetTokenJSON, 0, p.PerPage)
	var revealIDs []any
	for rows.Next() {
		var item resetTokenJSON
		var plain sql.NullString
		var revoked sql.NullTime
		if err := rows.Scan(&item.ID, &plain, &item.ExpiresAt, &revoked,
			&item.MailFailed, &item.User.ID, &item.User.Name, &item.User.Email); err != nil {
			return failDB(c, err)
		}
		item.ExpiresAt = item.ExpiresAt.UTC() // DSN loc=Local parses the container TZ (UTC); envelope contract is UTC
		item.Revoked = revoked.Valid
		item.SecondsLeft = int64(item.ExpiresAt.Sub(now).Seconds())
		if item.SecondsLeft < 0 {
			item.SecondsLeft = 0
		}
		item.CodeMasked = "***"
		if plain.Valid && len(plain.String) >= 2 {
			item.CodeMasked = "***" + plain.String[len(plain.String)-2:]
		}
		if reveal.Reveal == "1" && plain.Valid {
			item.CodePlain = new(plain.String)
			revealIDs = append(revealIDs, item.ID)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return failDB(c, err)
	}

	if len(revealIDs) > 0 {
		placeholders := strings.Repeat("?,", len(revealIDs))
		placeholders = placeholders[:len(placeholders)-1]
		q := `UPDATE reset_tokens SET revealed_at = NOW(), revealed_by = ? WHERE id IN (` + placeholders + `)`
		args := append([]any{u.ID}, revealIDs...)
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			return failDB(c, err)
		}
		slog.Info("audit: reset codes revealed", "count", len(revealIDs), "by", u.ID, "via", "list")
		s.publish(schoolPubFor(u), Frame{Entity: "reset.changed", Action: "revealed"})
	}
	return okPaged(c, out, p.Page, p.PerPage, total)
}

// schoolPubFor routes a reset.changed publish: SA-row events broadcast (0),
// otherwise the caller's school scope.
func schoolPubFor(u *userRow) int64 {
	if u.Role == "SA" {
		return 0
	}
	return u.SchoolID
}

// handleResetReveal returns code_plain once for one row — audited in the log
// and stamped revealed_at/revealed_by (05 §2).
func (s *Server) handleResetReveal(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)
	school, _ := c.Locals(kvSchool).(int64)

	ctx, cancel := dbCtx(c)
	defer cancel()
	q := `SELECT code_plain FROM reset_tokens WHERE id = ?`
	args := []any{id}
	if u.Role != "SA" {
		q += ` AND school_id = ?`
		args = append(args, school)
	}
	var plain sql.NullString
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&plain); err != nil {
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		return failDB(c, err)
	}
	if !plain.Valid || plain.String == "" {
		return fail(c, CodeResetUsed, MsgResetUsed) // cleared on use/revoke
	}
	upQ := `UPDATE reset_tokens SET revealed_at = NOW(), revealed_by = ? WHERE id = ?`
	upArgs := []any{u.ID, id}
	if u.Role != "SA" {
		upQ += ` AND school_id = ?`
		upArgs = append(upArgs, school)
	}
	if _, err := s.db.ExecContext(ctx, upQ, upArgs...); err != nil {
		return failDB(c, err)
	}
	slog.Info("audit: reset code revealed", "reset_id", id, "by", u.ID, "via", "post")
	s.publish(schoolPubFor(u), Frame{Entity: "reset.changed", EntityID: id, Action: "revealed"})
	return ok(c, fiber.Map{"code_plain": plain.String})
}

// handleResetRevoke voids a code (clears plaintext); idempotent.
func (s *Server) handleResetRevoke(c fiber.Ctx) error {
	id, okID := idParam(c, "id")
	if !okID {
		return nil
	}
	u, _ := c.Locals(kvUser).(*userRow)
	school, _ := c.Locals(kvSchool).(int64)

	ctx, cancel := dbCtx(c)
	defer cancel()
	q := `SELECT id FROM reset_tokens WHERE id = ?`
	args := []any{id}
	if u.Role != "SA" {
		q += ` AND school_id = ?`
		args = append(args, school)
	}
	var found int64
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&found); err != nil {
		if err == sql.ErrNoRows {
			return fail(c, CodeNotFound, MsgNotFound)
		}
		return failDB(c, err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE reset_tokens SET revoked_at = NOW(), code_plain = NULL
		 WHERE id = ? AND revoked_at IS NULL AND used_at IS NULL`, id); err != nil {
		return failDB(c, err)
	}
	s.publish(schoolPubFor(u), Frame{Entity: "reset.changed", EntityID: id, Action: "revoked"})
	return ok(c, fiber.Map{"revoked": true})
}

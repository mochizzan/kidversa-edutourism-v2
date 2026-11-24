package internal

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the exact 01 §8 env manifest. Boot fails loud (non-zero exit with
// a clear error) when a required key is missing/empty; optional keys fall back
// to the documented defaults. No other env key exists.
type Config struct {
	// Database / runtime (required unless noted).
	DBHost     string // DB_HOST
	DBPort     string // DB_PORT
	DBName     string // MARIADB_DATABASE
	DBUser     string // MARIADB_USER
	DBPassword string // MARIADB_PASSWORD
	AppPort    string // APP_PORT

	// CORS_ALLOWED_ORIGINS — required, comma-separated explicit origins,
	// never "*" (validated at load; AllowCredentials only works with this list).
	CorsOrigins []string

	// JWT (secrets required; TTLs optional with defaults).
	JWTAccessSecret  string        // JWT_ACCESS_SECRET
	JWTRefreshSecret string        // JWT_REFRESH_SECRET
	AccessTTL        time.Duration // JWT_ACCESS_TTL_MIN, default 15 min
	RefreshTTL       time.Duration // JWT_REFRESH_TTL_DAYS, default 7 d
	ResetTTL         time.Duration // RESET_TOKEN_TTL_MIN, default 15 min

	// External services — runtime-only (06 §2: SMTP/Gemini/OpenWA failures are
	// runtime failures, never boot failures; empty values degrade features).
	SMTPHost string // SMTP_HOST
	SMTPPort string // SMTP_PORT
	SMTPUser string // SMTP_USER
	SMTPPass string // SMTP_PASS
	SMTPFrom string // SMTP_FROM

	GeminiAPIKey string // GEMINI_API_KEY
	GeminiModel  string // GEMINI_MODEL, default gemini-3.5-flash

	OpenwaBaseURL string // OPENWA_BASE_URL, default http://wa:2785
	OpenwaAPIKey  string // OPENWA_API_KEY
	OpenwaSession string // OPENWA_SESSION, default kidversa

	// WACountryDefault is loaded for manifest completeness (01 §8); the UI
	// prefills 62 and normalizeWA is country-fixed by 05 §4.
	WACountryDefault string // WA_COUNTRY_DEFAULT, default 62

	// UploadDir required-with-default; PublicWebURL required (report/consent URLs).
	UploadDir    string // UPLOAD_DIR, default /data/uploads
	PublicWebURL string // PUBLIC_WEB_BASE_URL

	// MARIADB_ROOT_PASSWORD is compose-only (db service); loaded for manifest
	// completeness, never used by the api process.
	MARIADBRootPassword string

	// SecureCookie drives refresh-cookie Secure (https public URL = prod).
	SecureCookie bool
}

// requiredEnv names of keys without a documented default that the api process
// cannot boot without (01 §8). Service credentials (SMTP/Gemini/OpenWA keys)
// are deliberately absent: 01/06 classify their failures as runtime-only.
var requiredEnv = []string{
	"DB_HOST", "DB_PORT", "MARIADB_DATABASE", "MARIADB_USER", "MARIADB_PASSWORD",
	"APP_PORT", "CORS_ALLOWED_ORIGINS",
	"JWT_ACCESS_SECRET", "JWT_REFRESH_SECRET",
	"PUBLIC_WEB_BASE_URL",
}

// LoadConfig reads the env (os.Getenv only), enforces required keys and
// validates/derives the rest. Any problem is a boot failure.
func LoadConfig() (Config, error) {
	var missing []string
	for _, key := range requiredEnv {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env keys: %s", strings.Join(missing, ", "))
	}

	cfg := Config{
		DBHost:              os.Getenv("DB_HOST"),
		DBPort:              os.Getenv("DB_PORT"),
		DBName:              os.Getenv("MARIADB_DATABASE"),
		DBUser:              os.Getenv("MARIADB_USER"),
		DBPassword:          os.Getenv("MARIADB_PASSWORD"),
		AppPort:             os.Getenv("APP_PORT"),
		JWTAccessSecret:     os.Getenv("JWT_ACCESS_SECRET"),
		JWTRefreshSecret:    os.Getenv("JWT_REFRESH_SECRET"),
		SMTPHost:            os.Getenv("SMTP_HOST"),
		SMTPPort:            os.Getenv("SMTP_PORT"),
		SMTPUser:            os.Getenv("SMTP_USER"),
		SMTPPass:            os.Getenv("SMTP_PASS"),
		SMTPFrom:            os.Getenv("SMTP_FROM"),
		GeminiAPIKey:        os.Getenv("GEMINI_API_KEY"),
		OpenwaBaseURL:       strings.TrimRight(os.Getenv("OPENWA_BASE_URL"), "/"),
		OpenwaAPIKey:        os.Getenv("OPENWA_API_KEY"),
		OpenwaSession:       os.Getenv("OPENWA_SESSION"),
		WACountryDefault:    os.Getenv("WA_COUNTRY_DEFAULT"),
		PublicWebURL:        strings.TrimRight(os.Getenv("PUBLIC_WEB_BASE_URL"), "/"),
		MARIADBRootPassword: os.Getenv("MARIADB_ROOT_PASSWORD"),
	}
	if cfg.OpenwaBaseURL == "" {
		cfg.OpenwaBaseURL = "http://wa:2785"
	}
	if cfg.OpenwaSession == "" {
		cfg.OpenwaSession = "kidversa"
	}
	if cfg.WACountryDefault == "" {
		cfg.WACountryDefault = "62"
	}
	cfg.SecureCookie = strings.HasPrefix(cfg.PublicWebURL, "https://")

	origins := strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",")
	// F-B14 (audit C3): exactly 1–2 origins. Count the non-blank entries
	// first so a separator-only value is a count failure, not a silent empty
	// list; the element loop below keeps rejecting wildcard/scheme-less
	// origins with the original error.
	nOrigins := 0
	for _, o := range origins {
		if strings.TrimSpace(o) != "" {
			nOrigins++
		}
	}
	if nOrigins < 1 || nOrigins > 2 {
		return Config{}, fmt.Errorf("CORS_ALLOWED_ORIGINS must be 1-2 explicit scheme://host origins, got %q", os.Getenv("CORS_ALLOWED_ORIGINS"))
	}
	for _, o := range origins {
		o = strings.TrimSpace(o)
		if o == "" || strings.Contains(o, "*") || !strings.Contains(o, "://") {
			return Config{}, fmt.Errorf("CORS_ALLOWED_ORIGINS must be explicit scheme://host origins, got %q", os.Getenv("CORS_ALLOWED_ORIGINS"))
		}
		cfg.CorsOrigins = append(cfg.CorsOrigins, o)
	}

	var err error
	if cfg.AccessTTL, err = envMinutes("JWT_ACCESS_TTL_MIN", 15); err != nil {
		return Config{}, err
	}
	if cfg.RefreshTTL, err = envDays("JWT_REFRESH_TTL_DAYS", 7); err != nil {
		return Config{}, err
	}
	if cfg.ResetTTL, err = envMinutes("RESET_TOKEN_TTL_MIN", 15); err != nil {
		return Config{}, err
	}
	if cfg.GeminiModel, err = envDefault("GEMINI_MODEL", "gemini-3.5-flash"); err != nil {
		return Config{}, err
	}
	// UPLOAD_DIR required-with-default (01 §8): photo/badge/frame bytes live
	// under it; an empty value would make the orphan sweep walk the whole
	// working directory.
	if cfg.UploadDir, err = envDefault("UPLOAD_DIR", "/data/uploads"); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// envDefault returns the key or def; a set-but-empty key is an error.
func envDefault(key, def string) (string, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def, nil
	}
	if v == "" {
		return "", fmt.Errorf("env %s is set but empty", key)
	}
	return v, nil
}

func envMinutes(key string, def int) (time.Duration, error) {
	raw, err := envDefault(key, strconv.Itoa(def))
	if err != nil {
		return 0, err
	}
	n, convErr := strconv.Atoi(raw)
	if convErr != nil || n <= 0 {
		return 0, fmt.Errorf("env %s must be a positive integer, got %q", key, raw)
	}
	return time.Duration(n) * time.Minute, nil
}

func envDays(key string, def int) (time.Duration, error) {
	raw, err := envDefault(key, strconv.Itoa(def))
	if err != nil {
		return 0, err
	}
	n, convErr := strconv.Atoi(raw)
	if convErr != nil || n <= 0 {
		return 0, fmt.Errorf("env %s must be a positive integer, got %q", key, raw)
	}
	return time.Duration(n) * 24 * time.Hour, nil
}

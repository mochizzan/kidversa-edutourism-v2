package internal

import (
	"os"
	"strings"
	"testing"
)

// loadConfigEnv sets every requiredEnv key to a valid minimal value so
// LoadConfig reaches the field-loading stage.
func loadConfigEnv(t *testing.T) {
	t.Helper()
	for _, kv := range [][2]string{
		{"DB_HOST", "db"}, {"DB_PORT", "3306"}, {"MARIADB_DATABASE", "kidversa"},
		{"MARIADB_USER", "kidversa"}, {"MARIADB_PASSWORD", "pw"},
		{"APP_PORT", "3000"}, {"CORS_ALLOWED_ORIGINS", "http://localhost:8002"},
		{"JWT_ACCESS_SECRET", "access"}, {"JWT_REFRESH_SECRET", "refresh"},
		{"PUBLIC_WEB_BASE_URL", "http://localhost:8002"},
	} {
		t.Setenv(kv[0], kv[1])
	}
}

// TestLoadConfigIgnoresSeedEnv guards the removal of seed data from the env
// manifest: seeding must not depend on any SEED_* key.
func TestLoadConfigIgnoresSeedEnv(t *testing.T) {
	loadConfigEnv(t)
	for _, k := range []string{
		"SEED_SCHOOL_NAME", "SEED_SCHOOL_TYPE",
		"SEED_SA_NAME", "SEED_SA_EMAIL", "SEED_SA_PASSWORD",
	} {
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadConfig(); err != nil {
		t.Fatalf("LoadConfig must not require seed env: %v", err)
	}
}

// TestLoadConfigUploadDir guards the runtime upload root: an unpopulated
// UploadDir made the orphan sweep walk the working directory (it tried to
// delete migrations/prompts/templates in the container) and made every
// durableWrite fail with "path escapes upload root".
func TestLoadConfigUploadDir(t *testing.T) {
	loadConfigEnv(t)

	t.Run("defaults to /data/uploads when unset", func(t *testing.T) {
		t.Setenv("UPLOAD_DIR", "")
		if err := os.Unsetenv("UPLOAD_DIR"); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.UploadDir != "/data/uploads" {
			t.Fatalf("UploadDir = %q, want /data/uploads", cfg.UploadDir)
		}
	})

	t.Run("explicit env value wins", func(t *testing.T) {
		t.Setenv("UPLOAD_DIR", "/custom/uploads")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.UploadDir != "/custom/uploads" {
			t.Fatalf("UploadDir = %q, want /custom/uploads", cfg.UploadDir)
		}
	})

	t.Run("set-but-empty is a boot failure", func(t *testing.T) {
		t.Setenv("UPLOAD_DIR", "")
		_, err := LoadConfig()
		if err == nil || !strings.Contains(err.Error(), "UPLOAD_DIR") {
			t.Fatalf("want UPLOAD_DIR boot error, got %v", err)
		}
	})
}

// TestCfgCorsOriginCount pins the 1–2 origin count guard (F-B14, audit C3):
// zero and three origins are loud boot failures; one and two boot clean.
func TestCfgCorsOriginCount(t *testing.T) {
	t.Run("zero origins is a boot failure", func(t *testing.T) {
		loadConfigEnv(t)
		t.Setenv("CORS_ALLOWED_ORIGINS", " , ")
		_, err := LoadConfig()
		if err == nil || !strings.Contains(err.Error(), "CORS_ALLOWED_ORIGINS") ||
			!strings.Contains(err.Error(), "1-2") {
			t.Fatalf("want CORS_ALLOWED_ORIGINS 1-2 count boot error, got %v", err)
		}
	})

	t.Run("empty value is a boot failure", func(t *testing.T) {
		loadConfigEnv(t)
		t.Setenv("CORS_ALLOWED_ORIGINS", "")
		_, err := LoadConfig()
		if err == nil || !strings.Contains(err.Error(), "CORS_ALLOWED_ORIGINS") {
			t.Fatalf("want CORS_ALLOWED_ORIGINS boot error, got %v", err)
		}
	})

	t.Run("one origin boots", func(t *testing.T) {
		loadConfigEnv(t)
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if len(cfg.CorsOrigins) != 1 || cfg.CorsOrigins[0] != "http://localhost:8002" {
			t.Fatalf("CorsOrigins = %v, want [http://localhost:8002]", cfg.CorsOrigins)
		}
	})

	t.Run("two origins boot", func(t *testing.T) {
		loadConfigEnv(t)
		t.Setenv("CORS_ALLOWED_ORIGINS", "http://localhost:8002, http://admin.example")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if len(cfg.CorsOrigins) != 2 ||
			cfg.CorsOrigins[0] != "http://localhost:8002" ||
			cfg.CorsOrigins[1] != "http://admin.example" {
			t.Fatalf("CorsOrigins = %v, want both origins in order", cfg.CorsOrigins)
		}
	})

	t.Run("three origins is a boot failure", func(t *testing.T) {
		loadConfigEnv(t)
		t.Setenv("CORS_ALLOWED_ORIGINS",
			"http://a.example, http://b.example, http://c.example")
		_, err := LoadConfig()
		if err == nil || !strings.Contains(err.Error(), "CORS_ALLOWED_ORIGINS") ||
			!strings.Contains(err.Error(), "1-2") {
			t.Fatalf("want CORS_ALLOWED_ORIGINS 1-2 count boot error, got %v", err)
		}
	})
}

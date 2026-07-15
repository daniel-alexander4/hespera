package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"hespera/internal/config"
)

func TestMaskKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"abcd", "••••"},
		{"  ", ""},
		{"abcdefgh", "••••efgh"},
		{"1234567890", "••••7890"},
	}
	for _, tt := range tests {
		if got := maskKey(tt.in); got != tt.want {
			t.Fatalf("maskKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEffectiveTMDBKey(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()

	if got := h.effectiveTMDBKey(ctx); got != "" {
		t.Fatalf("no env, no db: got %q, want empty", got)
	}

	h.cfg.TMDBAPIKey = "env-key"
	if got := h.effectiveTMDBKey(ctx); got != "env-key" {
		t.Fatalf("env only: got %q, want env-key", got)
	}

	if _, err := db.Exec("INSERT INTO app_settings (key, value) VALUES ('tmdb_api_key', 'db-key')"); err != nil {
		t.Fatal(err)
	}
	if got := h.effectiveTMDBKey(ctx); got != "db-key" {
		t.Fatalf("db overrides env: got %q, want db-key", got)
	}

	// A blank/whitespace DB value falls back to env.
	if _, err := db.Exec("UPDATE app_settings SET value='  ' WHERE key='tmdb_api_key'"); err != nil {
		t.Fatal(err)
	}
	if got := h.effectiveTMDBKey(ctx); got != "env-key" {
		t.Fatalf("blank db falls back to env: got %q, want env-key", got)
	}
}

// TestEffectiveKeyBundledFallback covers the third resolution tier the release
// binaries add: a link-injected bundled key behind the env default, overridden
// by both a DB value and an env value. The bundled globals are package-level
// (normally set only by build.sh -ldflags), so each case saves and restores
// them to stay hermetic.
func TestEffectiveKeyBundledFallback(t *testing.T) {
	restore := func() {
		config.EmbeddedTMDBKey = ""
		config.EmbeddedFanartKey = ""
		config.EmbeddedOpenSubtitlesKey = ""
	}
	t.Cleanup(restore)
	restore()

	h, db := newTestHandler(t)
	ctx := context.Background()

	config.EmbeddedTMDBKey = "bundled-tmdb"
	config.EmbeddedFanartKey = "bundled-fanart"
	config.EmbeddedOpenSubtitlesKey = "bundled-os"

	// Nothing in DB or env → the bundled key is in force for each provider.
	if got := h.effectiveTMDBKey(ctx); got != "bundled-tmdb" {
		t.Fatalf("tmdb bundled fallback: got %q, want bundled-tmdb", got)
	}
	if got := h.effectiveFanartKey(ctx); got != "bundled-fanart" {
		t.Fatalf("fanart bundled fallback: got %q, want bundled-fanart", got)
	}
	if got := h.effectiveOpenSubtitlesKey(ctx); got != "bundled-os" {
		t.Fatalf("opensubtitles bundled fallback: got %q, want bundled-os", got)
	}

	// keyStatus reports the source as "bundled" (not "none") when the effective
	// value is the bundled key with no DB/env value in force.
	if cfg, src, _ := h.keyStatus(ctx, "tmdb_api_key", h.cfg.TMDBAPIKey, h.effectiveTMDBKey(ctx)); !cfg || src != "bundled" {
		t.Fatalf("keyStatus bundled: configured=%v source=%q, want true/bundled", cfg, src)
	}

	// An env value wins over bundled.
	h.cfg.TMDBAPIKey = "env-tmdb"
	if got := h.effectiveTMDBKey(ctx); got != "env-tmdb" {
		t.Fatalf("env over bundled: got %q, want env-tmdb", got)
	}

	// A DB value wins over both.
	if _, err := db.Exec("INSERT INTO app_settings (key, value) VALUES ('tmdb_api_key', 'db-tmdb')"); err != nil {
		t.Fatal(err)
	}
	if got := h.effectiveTMDBKey(ctx); got != "db-tmdb" {
		t.Fatalf("db over env/bundled: got %q, want db-tmdb", got)
	}
}

func apiKeyForm(key string) url.Values { return url.Values{"tmdb_api_key": {key}} }

func TestSettingsAPIKeys(t *testing.T) {
	t.Run("POST saves and reports validity", func(t *testing.T) {
		h, db := newTestHandler(t)
		h.tmdbValidate = func(ctx context.Context, key string) (bool, error) { return true, nil }

		rr := postForm(t, h.Router(), "/settings", apiKeyForm("  my-key  "))
		if rr.Code != http.StatusSeeOther {
			t.Fatalf("code = %d, want 303", rr.Code)
		}
		if loc := rr.Header().Get("Location"); loc != "/settings?open=integrations&saved=1&valid=1" {
			t.Fatalf("Location = %q", loc)
		}
		var v string
		if err := db.QueryRow("SELECT value FROM app_settings WHERE key='tmdb_api_key'").Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v != "my-key" { // trimmed
			t.Fatalf("stored value = %q, want my-key", v)
		}
	})

	t.Run("POST saves even when TMDB rejects the key", func(t *testing.T) {
		h, db := newTestHandler(t)
		h.tmdbValidate = func(ctx context.Context, key string) (bool, error) { return false, nil }
		rr := postForm(t, h.Router(), "/settings", apiKeyForm("bad-key"))
		if loc := rr.Header().Get("Location"); loc != "/settings?open=integrations&saved=1&valid=0" {
			t.Fatalf("Location = %q, want valid=0", loc)
		}
		var v string
		_ = db.QueryRow("SELECT value FROM app_settings WHERE key='tmdb_api_key'").Scan(&v)
		if v != "bad-key" {
			t.Fatalf("a rejected key should still be saved, got %q", v)
		}
	})

	t.Run("POST blank clears the override", func(t *testing.T) {
		h, db := newTestHandler(t)
		if _, err := db.Exec("INSERT INTO app_settings (key, value) VALUES ('tmdb_api_key', 'old')"); err != nil {
			t.Fatal(err)
		}
		rr := postForm(t, h.Router(), "/settings", apiKeyForm(""))
		if loc := rr.Header().Get("Location"); loc != "/settings?open=integrations&saved=cleared" {
			t.Fatalf("Location = %q, want saved=cleared", loc)
		}
		var n int
		_ = db.QueryRow("SELECT COUNT(*) FROM app_settings WHERE key='tmdb_api_key'").Scan(&n)
		if n != 0 {
			t.Fatalf("row count = %d, want 0 after clear", n)
		}
	})

	t.Run("GET renders", func(t *testing.T) {
		h, _ := newTestHandler(t)
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		rr := httptest.NewRecorder()
		h.settings(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET code = %d, want 200", rr.Code)
		}
	})

	t.Run("PUT rejected", func(t *testing.T) {
		h, _ := newTestHandler(t)
		req := httptest.NewRequest(http.MethodPut, "/settings", nil)
		rr := httptest.NewRecorder()
		h.settings(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("PUT code = %d, want 405", rr.Code)
		}
	})
}

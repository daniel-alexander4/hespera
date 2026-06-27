package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
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

func apiKeyForm(key string) url.Values { return url.Values{"tmdb_api_key": {key}} }

func TestSettingsAPIKeys(t *testing.T) {
	t.Run("POST saves and reports validity", func(t *testing.T) {
		h, db := newTestHandler(t)
		h.tmdbValidate = func(ctx context.Context, key string) (bool, error) { return true, nil }

		rr := postForm(t, h.Router(), "/settings/api-keys", apiKeyForm("  my-key  "))
		if rr.Code != http.StatusSeeOther {
			t.Fatalf("code = %d, want 303", rr.Code)
		}
		if loc := rr.Header().Get("Location"); loc != "/settings/api-keys?saved=1&valid=1" {
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
		rr := postForm(t, h.Router(), "/settings/api-keys", apiKeyForm("bad-key"))
		if loc := rr.Header().Get("Location"); loc != "/settings/api-keys?saved=1&valid=0" {
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
		rr := postForm(t, h.Router(), "/settings/api-keys", apiKeyForm(""))
		if loc := rr.Header().Get("Location"); loc != "/settings/api-keys?saved=cleared" {
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
		req := httptest.NewRequest(http.MethodGet, "/settings/api-keys", nil)
		rr := httptest.NewRecorder()
		h.settingsAPIKeys(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET code = %d, want 200", rr.Code)
		}
	})

	t.Run("PUT rejected", func(t *testing.T) {
		h, _ := newTestHandler(t)
		req := httptest.NewRequest(http.MethodPut, "/settings/api-keys", nil)
		rr := httptest.NewRecorder()
		h.settingsAPIKeys(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("PUT code = %d, want 405", rr.Code)
		}
	})
}

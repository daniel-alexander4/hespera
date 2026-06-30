package web

import (
	"context"
	"testing"

	"hespera/internal/config"
)

func TestResolveEffectiveConfigMediaRoot(t *testing.T) {
	db := openTestDB(t)
	envRoot := t.TempDir()
	goodRoot := t.TempDir()
	base := config.Config{MediaRoot: envRoot}

	// No override → env value stands.
	if got := resolveEffectiveConfig(base, db).MediaRoot; got != envRoot {
		t.Fatalf("no override: got %q, want %q", got, envRoot)
	}

	// A valid absolute, existing dir overrides.
	if _, err := db.Exec("INSERT INTO app_settings (key, value) VALUES ('media_root', ?)", goodRoot); err != nil {
		t.Fatal(err)
	}
	if got := resolveEffectiveConfig(base, db).MediaRoot; got != goodRoot {
		t.Fatalf("valid override: got %q, want %q", got, goodRoot)
	}

	// A non-existent path falls back to the env value (never bricks the boot).
	if _, err := db.Exec("UPDATE app_settings SET value='/no/such/hespera/dir' WHERE key='media_root'"); err != nil {
		t.Fatal(err)
	}
	if got := resolveEffectiveConfig(base, db).MediaRoot; got != envRoot {
		t.Fatalf("missing dir falls back: got %q, want %q", got, envRoot)
	}

	// A relative path is rejected → fall back to env.
	if _, err := db.Exec("UPDATE app_settings SET value='relative/path' WHERE key='media_root'"); err != nil {
		t.Fatal(err)
	}
	if got := resolveEffectiveConfig(base, db).MediaRoot; got != envRoot {
		t.Fatalf("relative path falls back: got %q, want %q", got, envRoot)
	}
}

func TestResolveEffectiveConfigAuthToggle(t *testing.T) {
	db := openTestDB(t)

	// Env default off, no override → off.
	if resolveEffectiveConfig(config.Config{}, db).AuthEnabled {
		t.Fatal("default off, no override: want disabled")
	}

	// Toggle on with a secret present → enabled.
	if _, err := db.Exec("INSERT INTO app_settings (key, value) VALUES ('auth_enabled', '1')"); err != nil {
		t.Fatal(err)
	}
	withSecret := config.Config{AuthSessionSecret: "a-sufficiently-long-secret"}
	if !resolveEffectiveConfig(withSecret, db).AuthEnabled {
		t.Fatal("toggle on + secret: want enabled")
	}

	// Toggle on but NO secret anywhere → disabled (won't boot into a config error).
	if resolveEffectiveConfig(config.Config{}, db).AuthEnabled {
		t.Fatal("toggle on, no secret: want disabled")
	}

	// Explicit off overrides an env-enabled default.
	if _, err := db.Exec("UPDATE app_settings SET value='0' WHERE key='auth_enabled'"); err != nil {
		t.Fatal(err)
	}
	envOn := config.Config{AuthEnabled: true, AuthSessionSecret: "a-sufficiently-long-secret"}
	if resolveEffectiveConfig(envOn, db).AuthEnabled {
		t.Fatal("explicit off overrides env-on: want disabled")
	}
}

func TestAuthToggleHelpers(t *testing.T) {
	h, _ := newTestHandler(t)
	ctx := context.Background()

	// ensureAuthSecret generates one when none exists, and the saved toggle reads back.
	if err := h.ensureAuthSecret(ctx); err != nil {
		t.Fatalf("ensureAuthSecret: %v", err)
	}
	var secret string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='auth_session_secret'").Scan(&secret)
	if len(secret) < 16 {
		t.Fatalf("generated secret too short: %q", secret)
	}

	// Idempotent — a second call doesn't replace it.
	if err := h.ensureAuthSecret(ctx); err != nil {
		t.Fatalf("ensureAuthSecret 2: %v", err)
	}
	var secret2 string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='auth_session_secret'").Scan(&secret2)
	if secret2 != secret {
		t.Fatal("ensureAuthSecret replaced an existing secret")
	}

	if err := h.saveAuthEnabled(ctx, true); err != nil {
		t.Fatalf("saveAuthEnabled: %v", err)
	}
	if !h.authEnabledSetting(ctx) {
		t.Fatal("authEnabledSetting: want true after saving on")
	}
	if err := h.saveAuthEnabled(ctx, false); err != nil {
		t.Fatalf("saveAuthEnabled off: %v", err)
	}
	if h.authEnabledSetting(ctx) {
		t.Fatal("authEnabledSetting: want false after saving off")
	}
}

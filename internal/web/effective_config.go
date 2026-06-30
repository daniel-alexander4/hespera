package web

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"hespera/internal/config"
)

// resolveEffectiveConfig overlays user-set runtime overrides from app_settings
// onto the env/default config, once at construction time. Two values are
// configurable from the Settings UI instead of env vars — the media folder and
// whether auth is on — so the single-machine app is fully configurable without
// touching the CLI.
//
// Both apply at startup (the next launch picks up a change), which keeps the
// security-critical containment root (MediaRoot, the pathguard root) and the
// auth boundary resolved once, not mutated per request. Every MediaRoot reader
// (scanners + stream handlers, all built from h.cfg) and the auth Manager are
// constructed from this returned config, so overriding here is the single source
// of truth — no individual call site reads app_settings for these.
func resolveEffectiveConfig(cfg config.Config, db *sql.DB) config.Config {
	if db == nil {
		return cfg
	}
	ctx := context.Background()
	get := func(key string) string {
		var v string
		_ = db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key=?", key).Scan(&v)
		return strings.TrimSpace(v)
	}

	// Media folder: a saved-but-invalid value (e.g. an unplugged drive) falls back
	// to the env/default with a warning rather than bricking the app on boot.
	if mr := get("media_root"); mr != "" {
		if err := validateMediaFolder(mr); err != nil {
			slog.Warn("configured media folder is unusable — using default", "media_root", mr, "err", err)
		} else {
			cfg.MediaRoot = mr
		}
	}

	// Auth toggle: a stored value (explicit "1"/"0") overrides the env default.
	// When auth ends up on, resolve its session secret (app_settings → env); if
	// none exists we can't safely enable it, so disable with a warning instead of
	// failing to boot — the Settings toggle generates a secret when it turns auth
	// on, so this only trips on a hand-edited DB.
	if av := get("auth_enabled"); av != "" {
		cfg.AuthEnabled = av == "1"
	}
	if cfg.AuthEnabled {
		if secret := get("auth_session_secret"); secret != "" {
			cfg.AuthSessionSecret = secret
		}
		if strings.TrimSpace(cfg.AuthSessionSecret) == "" {
			slog.Warn("auth is enabled but no session secret is configured — disabling auth")
			cfg.AuthEnabled = false
		}
	}
	return cfg
}

// validateMediaFolder reports whether a path is usable as the media containment
// root: an absolute path to an existing directory.
func validateMediaFolder(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("media folder must be an absolute path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("media folder does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("media folder is not a directory")
	}
	return nil
}

// authEnabledSetting reports the saved auth intent for the Settings checkbox: the
// explicit app_settings value if set, else the current active state. (The active
// state — h.auth.Enabled() — reflects the value resolved at the last launch; a
// just-saved change shows here but only takes effect on restart.)
func (h *Handler) authEnabledSetting(ctx context.Context) bool {
	var v string
	if err := h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='auth_enabled'").Scan(&v); err == nil {
		return strings.TrimSpace(v) == "1"
	}
	return h.auth.Enabled()
}

// saveAuthEnabled persists the auth toggle with an explicit "1"/"0" so the intent
// survives (unlike saveAPIKey, which deletes the row on an empty value — that
// would lose an explicit "off" and let the env default re-assert itself).
func (h *Handler) saveAuthEnabled(ctx context.Context, on bool) error {
	val := "0"
	if on {
		val = "1"
	}
	_, err := h.db.ExecContext(ctx,
		"INSERT INTO app_settings (key, value) VALUES ('auth_enabled', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		val)
	return err
}

// ensureAuthSecret generates and stores a random session secret if none is
// configured (app_settings or env), so enabling auth from the UI doesn't boot
// into a "secret required" config error. No-op when a secret already exists.
func (h *Handler) ensureAuthSecret(ctx context.Context) error {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='auth_session_secret'").Scan(&v)
	if strings.TrimSpace(v) != "" || strings.TrimSpace(h.cfg.AuthSessionSecret) != "" {
		return nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	_, err := h.db.ExecContext(ctx,
		"INSERT INTO app_settings (key, value) VALUES ('auth_session_secret', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		hex.EncodeToString(buf))
	return err
}

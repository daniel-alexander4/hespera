package web

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"hespera/internal/config"
)

// resolveEffectiveConfig overlays the user-set media-folder override from
// app_settings onto the env/default config, once at construction time. The media
// folder is configurable from the Settings UI (and the hescli CLI) instead of an
// env var, so the single-machine app is configurable without a restart flag.
//
// It applies at startup (the next launch picks up a change), which keeps the
// security-critical containment root (MediaRoot, the pathguard root) resolved
// once, not mutated per request. Every MediaRoot reader (scanners + stream
// handlers, all built from h.cfg) is constructed from this returned config, so
// overriding here is the single source of truth — no individual call site reads
// app_settings for it.
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

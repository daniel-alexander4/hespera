package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"hespera/internal/jobs"
	"hespera/internal/match"
	"hespera/internal/tmdb"
)

// ManagementRouter builds the JSON API served over the local management socket
// (DataDir/hescli.sock, root/owner-gated by peer-cred — see cmd/hespera). It's a
// curated, management-only surface: no playback, streaming, or art routes. Where
// a web handler already emits JSON on `Accept: application/json` (scan, delete,
// integrity, jobs) it's reused verbatim; the rest are thin JSON handlers below.
// No csrfGuard — the peer-cred check is the gate and there is no browser origin.
func (h *Handler) ManagementRouter() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/status", h.mgmtStatus)

	// Libraries
	mux.HandleFunc("/libraries", h.mgmtLibraryList)    // GET
	mux.HandleFunc("/libraries/add", h.mgmtLibraryAdd) // POST
	mux.HandleFunc("/libraries/rm", h.librariesDelete) // POST (reused)

	// Actions
	mux.HandleFunc("/scan", h.librariesScan)               // POST (reused)
	mux.HandleFunc("/match", h.mgmtMatch)                  // POST
	mux.HandleFunc("/integrity", h.librariesIntegrityDeep) // POST (reused)

	// Jobs
	mux.HandleFunc("/jobs", h.settingsJobsJSON)           // GET (reused)
	mux.HandleFunc("/jobs/status", h.librariesJobsStatus) // GET (reused)

	// Config (API keys, toggles, media_root)
	mux.HandleFunc("/config", h.mgmtConfigList)    // GET
	mux.HandleFunc("/config/get", h.mgmtConfigGet) // GET
	mux.HandleFunc("/config/set", h.mgmtConfigSet) // POST

	return withLogging(mux)
}

// writeJSON writes a JSON payload with the given status code.
func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

// mgmtStatus reports a one-shot overview: build version, the resolved media
// folder + data dir, the library count, and uptime.
func (h *Handler) mgmtStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var libCount int
	_ = h.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM libraries").Scan(&libCount)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{
			"version":        h.version,
			"media_root":     h.cfg.MediaRoot,
			"data_dir":       h.cfg.DataDir,
			"library_count":  libCount,
			"uptime_seconds": int64(time.Since(h.startedAt).Seconds()),
		},
	})
}

// mgmtLibraryList returns every library as JSON (reuses the canonical query).
func (h *Handler) mgmtLibraryList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	libs, err := h.loadLibraryList(r.Context())
	if err != nil {
		jsonErr(w, 500, "internal server error", "load libraries failed", "handler", "mgmtLibraryList", "err", err)
		return
	}
	if libs == nil {
		libs = []libraryRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": libs})
}

// mgmtLibraryAdd creates a library via the shared createLibrary rules (required
// fields, valid type, pathguard containment) and returns the new row as JSON.
func (h *Handler) mgmtLibraryAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	libType := strings.TrimSpace(r.FormValue("type"))
	root := strings.TrimSpace(r.FormValue("root_path"))
	id, err := h.createLibrary(r.Context(), name, libType, root)
	if err != nil {
		var bre badRequestError
		if errors.As(err, &bre) {
			jsonError(w, bre.Error(), http.StatusBadRequest)
			return
		}
		jsonErr(w, 500, "internal server error", "db insert failed", "handler", "mgmtLibraryAdd", "err", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "library added",
		"data":    libraryRow{ID: id, Name: name, Type: libType, RootPath: root},
	})
}

// mgmtMatch enqueues a metadata match for a library, dispatching by its type
// (like librariesScan). TV/movie matching requires a TMDB key; music does not.
func (h *Handler) mgmtMatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	var libType string
	if err := h.db.QueryRowContext(ctx, "SELECT type FROM libraries WHERE id=?", id).Scan(&libType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, "library not found", http.StatusNotFound)
			return
		}
		jsonErr(w, 500, "internal server error", "db query failed", "handler", "mgmtMatch", "err", err)
		return
	}

	var jobType string
	var executor func(ctx context.Context, jobID, libraryID int64) error
	switch libType {
	case "music":
		matcher := match.New(h.db, h.cfg.DataDir, h.effectiveFanartKey(ctx), h.effectiveAudioDBKey(ctx), h.effectiveLastfmKey(ctx))
		jobType = "music_match"
		executor = func(ctx context.Context, jID, libID int64) error { return matcher.RunMusicMatch(ctx, jID, libID) }
	case "tv":
		tmdbKey := h.effectiveTMDBKey(ctx)
		if tmdbKey == "" {
			jsonError(w, "TMDB API key not configured", http.StatusBadRequest)
			return
		}
		matcher := tmdb.NewMatcher(h.db, tmdbKey, h.cfg.DataDir)
		jobType = "tv_match"
		executor = func(ctx context.Context, jID, libID int64) error { return matcher.RunTVMatch(ctx, jID, libID) }
	case "movies":
		tmdbKey := h.effectiveTMDBKey(ctx)
		if tmdbKey == "" {
			jsonError(w, "TMDB API key not configured", http.StatusBadRequest)
			return
		}
		matcher := tmdb.NewMovieMatcher(h.db, tmdbKey, h.cfg.DataDir)
		jobType = "movie_match"
		executor = func(ctx context.Context, jID, libID int64) error { return matcher.RunMovieMatch(ctx, jID, libID) }
	default:
		jsonError(w, "matching is only supported for music, tv, and movie libraries", http.StatusBadRequest)
		return
	}

	jobID, err := h.jobs.Enqueue(jobType, id, "user", executor)
	if err != nil {
		if errors.Is(err, jobs.ErrQueueFull) {
			jsonErr(w, http.StatusServiceUnavailable, "service unavailable", "job queue full", "handler", "mgmtMatch", "err", err)
			return
		}
		jsonErr(w, 500, "internal server error", "enqueue match failed", "handler", "mgmtMatch", "err", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "match queued",
		"data":    map[string]any{"library_id": id, "job_id": jobID, "type": libType},
	})
}

// --- Config (settings) registry ---

type settingKind string

const (
	kindSecret settingKind = "secret" // masked in output (API keys)
	kindString settingKind = "string" // shown in clear (e.g. OpenSubtitles UA)
	kindToggle settingKind = "toggle" // on/off
	kindPath   settingKind = "path"   // media_root, startup-resolved
)

// settingSpec describes one runtime setting the CLI can read/write. Values still
// live in app_settings (via saveAPIKey) + the effective* getters — this registry
// is only an adapter that names the keys and their kind for the CLI's config
// verb. Drift against the web settings forms (a key added there but not here,
// or vice versa) fails the build via TestManagedSettingsCoverSettingsForms.
type settingSpec struct {
	Key            string
	Kind           settingKind
	Env            string // env var backing the default (for source reporting)
	Default        string // built-in fallback when db+env are empty (string kind)
	OnStored       string // toggle: the app_settings value meaning "on"
	OffStored      string // toggle: the app_settings value meaning "off"
	ApplyOnRestart bool
}

// managedSettings is the CLI-visible set of runtime settings. Toggle On/Off
// stored values mirror settingsAPIKeys exactly: integrity_autorepair defaults ON
// (clears the row for on, stores '0' for off); lyrics_enabled defaults OFF
// (stores '1' for on, clears for off).
var managedSettings = []settingSpec{
	{Key: "tmdb_api_key", Kind: kindSecret, Env: "HESPERA_TMDB_API_KEY"},
	{Key: "fanarttv_api_key", Kind: kindSecret, Env: "HESPERA_FANARTTV_API_KEY"},
	{Key: "audiodb_api_key", Kind: kindSecret, Env: "HESPERA_THEAUDIODB_API_KEY"},
	{Key: "lastfm_api_key", Kind: kindSecret, Env: "HESPERA_LASTFM_API_KEY"},
	{Key: "opensubtitles_api_key", Kind: kindSecret, Env: "HESPERA_OPENSUBTITLES_API_KEY"},
	{Key: "opensubtitles_user_agent", Kind: kindString, Env: "HESPERA_OPENSUBTITLES_USER_AGENT", Default: "Hespera v1.0"},
	{Key: "integrity_autorepair", Kind: kindToggle, OnStored: "", OffStored: "0"},
	{Key: "watch_enabled", Kind: kindToggle, OnStored: "", OffStored: "0"},
	{Key: "lyrics_enabled", Kind: kindToggle, OnStored: "1", OffStored: ""},
	{Key: "keytrace_enabled", Kind: kindToggle, OnStored: "1", OffStored: ""},
	{Key: "update_check_enabled", Kind: kindToggle, OnStored: "1", OffStored: ""},
	{Key: "media_root", Kind: kindPath, Env: "HESPERA_MEDIA_ROOT", ApplyOnRestart: true},
}

func lookupSetting(key string) (settingSpec, bool) {
	for _, s := range managedSettings {
		if s.Key == key {
			return s, true
		}
	}
	return settingSpec{}, false
}

// appSetting reads a raw app_settings value (trimmed; empty if absent).
func (h *Handler) appSetting(ctx context.Context, key string) string {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key=?", key).Scan(&v)
	return strings.TrimSpace(v)
}

type configEntry struct {
	Key            string `json:"key"`
	Kind           string `json:"kind"`
	Source         string `json:"source"` // custom / env / default / none
	Value          string `json:"value"`  // masked secret, on|off, string, or path
	ApplyOnRestart bool   `json:"apply_on_restart,omitempty"`
}

func (h *Handler) configEntryFor(ctx context.Context, spec settingSpec) configEntry {
	dbVal := h.appSetting(ctx, spec.Key)
	e := configEntry{Key: spec.Key, Kind: string(spec.Kind), ApplyOnRestart: spec.ApplyOnRestart}
	switch spec.Kind {
	case kindSecret:
		env := os.Getenv(spec.Env)
		eff := dbVal
		if eff == "" {
			eff = env
		}
		switch {
		case dbVal != "":
			e.Source = "custom"
		case env != "":
			e.Source = "env"
		default:
			e.Source = "none"
		}
		e.Value = maskKey(eff)
	case kindString:
		env := strings.TrimSpace(os.Getenv(spec.Env))
		switch {
		case dbVal != "":
			e.Source, e.Value = "custom", dbVal
		case env != "":
			e.Source, e.Value = "env", env
		default:
			e.Source, e.Value = "default", spec.Default
		}
	case kindToggle:
		if dbVal != "" {
			e.Source = "custom"
		} else {
			e.Source = "default"
		}
		if dbVal == spec.OnStored {
			e.Value = "on"
		} else {
			e.Value = "off"
		}
	case kindPath:
		// The active containment root is resolved once at startup, so report
		// h.cfg.MediaRoot (the value actually in force) rather than the raw save.
		switch {
		case dbVal != "":
			e.Source = "custom"
		case strings.TrimSpace(os.Getenv(spec.Env)) != "":
			e.Source = "env"
		default:
			e.Source = "default"
		}
		e.Value = h.cfg.MediaRoot
	}
	return e
}

// mgmtConfigList returns every managed setting's current state (secrets masked).
func (h *Handler) mgmtConfigList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	entries := make([]configEntry, 0, len(managedSettings))
	for _, spec := range managedSettings {
		entries = append(entries, h.configEntryFor(r.Context(), spec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": entries})
}

// mgmtConfigGet returns one managed setting by ?key=.
func (h *Handler) mgmtConfigGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	spec, ok := lookupSetting(key)
	if !ok {
		jsonError(w, "unknown setting key: "+key, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": h.configEntryFor(r.Context(), spec)})
}

// mgmtConfigSet writes one managed setting, applying per-kind rules: secrets and
// strings save verbatim (blank clears/reverts to env), toggles normalize to their
// stored on/off value, and media_root is validated (pathguard) then flagged
// apply-on-restart.
func (h *Handler) mgmtConfigSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(r.FormValue("key"))
	spec, ok := lookupSetting(key)
	if !ok {
		jsonError(w, "unknown setting key: "+key, http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	value := r.FormValue("value")

	switch spec.Kind {
	case kindSecret, kindString:
		if err := h.saveAPIKey(ctx, key, strings.TrimSpace(value)); err != nil {
			jsonErr(w, 500, "internal server error", "save setting failed", "handler", "mgmtConfigSet", "err", err)
			return
		}
	case kindToggle:
		on, err := parseToggle(value)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		stored := spec.OffStored
		if on {
			stored = spec.OnStored
		}
		if err := h.saveAPIKey(ctx, key, stored); err != nil {
			jsonErr(w, 500, "internal server error", "save setting failed", "handler", "mgmtConfigSet", "err", err)
			return
		}
	case kindPath:
		v := strings.TrimSpace(value)
		if v != "" {
			if err := validateMediaFolder(v); err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if err := h.saveAPIKey(ctx, key, v); err != nil {
			jsonErr(w, 500, "internal server error", "save setting failed", "handler", "mgmtConfigSet", "err", err)
			return
		}
	}

	msg := "saved"
	if spec.ApplyOnRestart {
		msg = "saved — applies on restart"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": msg,
		"data":    h.configEntryFor(ctx, spec),
	})
}

// parseToggle interprets a boolean-ish CLI value for a toggle setting.
func parseToggle(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes", "enable", "enabled":
		return true, nil
	case "0", "false", "off", "no", "disable", "disabled":
		return false, nil
	default:
		return false, errors.New("value must be on/off (or true/false)")
	}
}

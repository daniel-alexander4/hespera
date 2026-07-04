package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hespera/internal/integrity"
	"hespera/internal/jobs"
	"hespera/internal/match"
	"hespera/internal/moviescan"
	"hespera/internal/pathguard"
	"hespera/internal/scan"
	"hespera/internal/tmdb"
	"hespera/internal/tvscan"
)

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	h.render(w, "settings.html", map[string]any{
		"Breadcrumb": []crumb{bcHome},
		"Title":      "Settings",
	})
}

// settingsAbout renders the static About & Attributions page — the single place
// all third-party data-source and open-source attributions live (incl. the
// TMDB API notice required by TMDB's terms, which permit it on an About/Credits
// page rather than every page).
func (h *Handler) settingsAbout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	h.render(w, "settings_about.html", map[string]any{
		"Breadcrumb": []crumb{bcHome, bcSettings},
		"Title":      "About & Attributions",
	})
}

// effectiveTMDBKey returns the runtime-configured TMDB API key: the value set
// via the settings UI (app_settings) if non-empty, otherwise the env-provided
// key from config. This is the single source of truth for the key across
// handlers — reading the DB per call lets a UI change take effect without a
// restart, and avoids the previously-duplicated h.cfg.TMDBAPIKey reads drifting.
func (h *Handler) effectiveTMDBKey(ctx context.Context) string {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='tmdb_api_key'").Scan(&v)
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return h.cfg.TMDBAPIKey
}

// effectiveFanartKey / effectiveAudioDBKey resolve the optional artist-backfill
// provider keys the same way as effectiveTMDBKey: the app_settings (UI) value
// wins, else the env default. Both are optional — empty disables the provider.
func (h *Handler) effectiveFanartKey(ctx context.Context) string {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='fanarttv_api_key'").Scan(&v)
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return h.cfg.FanartTVAPIKey
}

func (h *Handler) effectiveAudioDBKey(ctx context.Context) string {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='audiodb_api_key'").Scan(&v)
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return h.cfg.TheAudioDBAPIKey
}

// effectiveLastfmKey resolves the optional Last.fm API key the same way: the
// app_settings (UI) value wins, else the env default. Empty disables the Last.fm
// popularity blend (ListenBrainz alone fills Most Popular).
func (h *Handler) effectiveLastfmKey(ctx context.Context) string {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='lastfm_api_key'").Scan(&v)
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return h.cfg.LastfmAPIKey
}

// effectiveOpenSubtitlesKey resolves the optional OpenSubtitles API key the same
// way: the app_settings (UI) value wins, else the env default. Empty disables
// the on-demand TV subtitle search.
func (h *Handler) effectiveOpenSubtitlesKey(ctx context.Context) string {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='opensubtitles_api_key'").Scan(&v)
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return h.cfg.OpenSubtitlesAPIKey
}

// effectiveOpenSubtitlesUserAgent resolves the OpenSubtitles consumer User-Agent:
// the app_settings (UI) value wins, then the env default, else a built-in
// fallback. The UA must name a consumer app *registered with OpenSubtitles*
// ("AppName vX.Y") — an unregistered UA is 403'd — so it's user-configurable.
func (h *Handler) effectiveOpenSubtitlesUserAgent(ctx context.Context) string {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='opensubtitles_user_agent'").Scan(&v)
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	if ua := strings.TrimSpace(h.cfg.OpenSubtitlesUserAgent); ua != "" {
		return ua
	}
	return "Hespera v1.0"
}

// effectiveIntegrityAutoRepair reports whether Hespera may auto-repair
// container-corrupt media in place (remux → verify → atomic replace). Default
// ON — corruption detection always runs, but this is the kill-switch for the one
// operation that writes back into MEDIA_ROOT. Stored explicitly as '0' to persist
// an off; absent (or anything else) reads as on.
func (h *Handler) effectiveIntegrityAutoRepair(ctx context.Context) bool {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='integrity_autorepair'").Scan(&v)
	return strings.TrimSpace(v) != "0"
}

// effectiveWatchEnabled reports whether the library filesystem watcher may
// auto-trigger scans on file changes. Default ON — the watcher is the
// zero-click ingest path; '0' turns it off without a restart (internal/watch
// re-reads it at each debounce fire). The integrity_autorepair pattern.
func (h *Handler) effectiveWatchEnabled(ctx context.Context) bool {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='watch_enabled'").Scan(&v)
	return strings.TrimSpace(v) != "0"
}

// effectiveLyricsEnabled reports whether synced-lyrics fetching + the
// now-playing lyrics card are on. Default OFF (opt-in) — stored as '1' when
// enabled, absent = off. The single source of truth for both the client (skips
// the LRCLIB fetch when off) and the /music/lyrics/fetch endpoint.
func (h *Handler) effectiveLyricsEnabled(ctx context.Context) bool {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='lyrics_enabled'").Scan(&v)
	return strings.TrimSpace(v) == "1"
}

// effectiveDefaultAudioLang returns the user's preferred audio language for
// playback (a lowercase ISO-ish code, "" = no preference). Normalized at read
// time so a value stored unvalidated (hescli config set) degrades to "no
// preference" instead of breaking track resolution.
func (h *Handler) effectiveDefaultAudioLang(ctx context.Context) string {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='default_audio_lang'").Scan(&v)
	return sanitizeLangSetting(v)
}

// effectiveDefaultSubtitleLang returns the preferred subtitle language for the
// subtitles-on default ("" = any text track). Read-time normalized, like
// effectiveDefaultAudioLang.
func (h *Handler) effectiveDefaultSubtitleLang(ctx context.Context) string {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='default_subtitle_lang'").Scan(&v)
	return sanitizeLangSetting(v)
}

// effectiveSubtitlesDefaultOn reports whether subtitles auto-enable on playback
// when the user hasn't picked a track. Default OFF (opt-in) — stored as '1'
// when enabled, absent = off. The lyrics_enabled pattern.
func (h *Handler) effectiveSubtitlesDefaultOn(ctx context.Context) bool {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='subtitles_default_on'").Scan(&v)
	return strings.TrimSpace(v) == "1"
}

// effectiveUpdateCheckEnabled reports whether the once-per-session automatic
// update check (the topbar version pill's startup fetch) is on. Default OFF
// (opt-in — no phone-home until the user asks for it): stored as '1' when
// enabled, absent = off. The pill's click always checks regardless; this gates
// only the automatic path.
func (h *Handler) effectiveUpdateCheckEnabled(ctx context.Context) bool {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='update_check_enabled'").Scan(&v)
	return strings.TrimSpace(v) == "1"
}

// maskKey renders an API key for display without exposing it: the last 4
// characters behind a dot mask, or just the mask for very short values.
func maskKey(k string) string {
	k = strings.TrimSpace(k)
	if k == "" {
		return ""
	}
	if len(k) <= 4 {
		return "••••"
	}
	return "••••" + k[len(k)-4:]
}

// keyStatus reports an API key's display state: whether an effective value
// exists, its source (custom DB value / env / none), and a masked rendering.
func (h *Handler) keyStatus(ctx context.Context, dbKey, envVal, effective string) (configured bool, source, masked string) {
	var dbVal string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key=?", dbKey).Scan(&dbVal)
	source = "none"
	switch {
	case strings.TrimSpace(dbVal) != "":
		source = "custom"
	case strings.TrimSpace(envVal) != "":
		source = "env"
	}
	return effective != "", source, maskKey(effective)
}

// saveAPIKey upserts a non-empty value or clears the row (revert to env) on empty.
func (h *Handler) saveAPIKey(ctx context.Context, dbKey, value string) error {
	if value == "" {
		_, err := h.db.ExecContext(ctx, "DELETE FROM app_settings WHERE key=?", dbKey)
		return err
	}
	_, err := h.db.ExecContext(ctx,
		"INSERT INTO app_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		dbKey, value)
	return err
}

// settingsAPIKeys renders (GET) and persists (POST) user-configurable API keys.
// Today the only key is TMDB. A stored value overrides the env default; an empty
// submission clears it (reverting to env). The raw key is never rendered back or
// logged. POST is protected by the same auth + same-origin CSRF as every other
// /settings route.
func (h *Handler) settingsAPIKeys(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		tmdbCfg, tmdbSrc, tmdbMask := h.keyStatus(ctx, "tmdb_api_key", h.cfg.TMDBAPIKey, h.effectiveTMDBKey(ctx))
		fanCfg, fanSrc, fanMask := h.keyStatus(ctx, "fanarttv_api_key", h.cfg.FanartTVAPIKey, h.effectiveFanartKey(ctx))
		adbCfg, adbSrc, adbMask := h.keyStatus(ctx, "audiodb_api_key", h.cfg.TheAudioDBAPIKey, h.effectiveAudioDBKey(ctx))
		lfmCfg, lfmSrc, lfmMask := h.keyStatus(ctx, "lastfm_api_key", h.cfg.LastfmAPIKey, h.effectiveLastfmKey(ctx))
		osCfg, osSrc, osMask := h.keyStatus(ctx, "opensubtitles_api_key", h.cfg.OpenSubtitlesAPIKey, h.effectiveOpenSubtitlesKey(ctx))
		h.render(w, "settings_apikeys.html", map[string]any{
			"Breadcrumb":              []crumb{bcHome, bcSettings},
			"Title":                   "API Keys",
			"TMDBConfigured":          tmdbCfg,
			"TMDBSource":              tmdbSrc,
			"TMDBMasked":              tmdbMask,
			"FanartConfigured":        fanCfg,
			"FanartSource":            fanSrc,
			"FanartMasked":            fanMask,
			"AudioDBConfigured":       adbCfg,
			"AudioDBSource":           adbSrc,
			"AudioDBMasked":           adbMask,
			"LastfmConfigured":        lfmCfg,
			"LastfmSource":            lfmSrc,
			"LastfmMasked":            lfmMask,
			"OpenSubtitlesConfigured": osCfg,
			"OpenSubtitlesSource":     osSrc,
			"OpenSubtitlesMasked":     osMask,
			"OpenSubtitlesUserAgent":  h.effectiveOpenSubtitlesUserAgent(ctx),
			"IntegrityAutoRepair":     h.effectiveIntegrityAutoRepair(ctx),
			"WatchEnabled":            h.effectiveWatchEnabled(ctx),
			"LyricsEnabled":           h.effectiveLyricsEnabled(ctx),
			"KeytraceEnabled":         h.effectiveKeytraceEnabled(ctx),
			"UpdateCheckEnabled":      h.effectiveUpdateCheckEnabled(ctx),
			"DefaultAudioLang":        h.effectiveDefaultAudioLang(ctx),
			"DefaultSubtitleLang":     h.effectiveDefaultSubtitleLang(ctx),
			"SubtitlesDefaultOn":      h.effectiveSubtitlesDefaultOn(ctx),
			"Saved":                   r.URL.Query().Get("saved"),
			"Valid":                   r.URL.Query().Get("valid"),
		})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			httpError(w, 400, "bad request", "parse form failed", "handler", "settingsAPIKeys", "err", err)
			return
		}
		// Each key has its own form, so exactly one field is present per submit;
		// a blank value for that field clears it. This avoids one form's empty
		// fields wiping the other keys.
		if _, ok := r.Form["tmdb_api_key"]; ok {
			key := strings.TrimSpace(r.FormValue("tmdb_api_key"))
			if err := h.saveAPIKey(ctx, "tmdb_api_key", key); err != nil {
				httpError(w, 500, "internal server error", "save api key failed", "handler", "settingsAPIKeys", "err", err)
				return
			}
			if key == "" {
				http.Redirect(w, r, "/settings/api-keys?saved=cleared", http.StatusSeeOther)
				return
			}
			valid := "unknown"
			if h.tmdbValidate != nil {
				if ok, verr := h.tmdbValidate(ctx, key); verr == nil {
					if ok {
						valid = "1"
					} else {
						valid = "0"
					}
				}
			}
			http.Redirect(w, r, "/settings/api-keys?saved=1&valid="+valid, http.StatusSeeOther)
			return
		}
		for _, field := range []string{"fanarttv_api_key", "audiodb_api_key", "lastfm_api_key"} {
			if _, ok := r.Form[field]; ok {
				if err := h.saveAPIKey(ctx, field, strings.TrimSpace(r.FormValue(field))); err != nil {
					httpError(w, 500, "internal server error", "save api key failed", "handler", "settingsAPIKeys", "err", err)
					return
				}
				http.Redirect(w, r, "/settings/api-keys?saved=1", http.StatusSeeOther)
				return
			}
		}
		// OpenSubtitles key + User-Agent share one form, so save both together.
		// The UA (not a secret) always takes its submitted value (blank → default);
		// the key is saved only when non-blank, so editing the UA never wipes a
		// stored key (no way to distinguish "blank to clear" from "blank to keep"
		// in a combined form — keep wins as the safe choice).
		if _, ok := r.Form["opensubtitles_api_key"]; ok {
			if err := h.saveAPIKey(ctx, "opensubtitles_user_agent", strings.TrimSpace(r.FormValue("opensubtitles_user_agent"))); err != nil {
				httpError(w, 500, "internal server error", "save setting failed", "handler", "settingsAPIKeys", "err", err)
				return
			}
			if key := strings.TrimSpace(r.FormValue("opensubtitles_api_key")); key != "" {
				if err := h.saveAPIKey(ctx, "opensubtitles_api_key", key); err != nil {
					httpError(w, 500, "internal server error", "save api key failed", "handler", "settingsAPIKeys", "err", err)
					return
				}
			}
			http.Redirect(w, r, "/settings/api-keys?saved=1", http.StatusSeeOther)
			return
		}
		if _, ok := r.Form["integrity_present"]; ok {
			// Default-ON kill-switch: store an explicit '0' to persist an off;
			// clear the row (→ absent → on) when checked, keeping the DB clean.
			val := "0"
			if r.FormValue("integrity_autorepair") == "1" {
				val = ""
			}
			if err := h.saveAPIKey(ctx, "integrity_autorepair", val); err != nil {
				httpError(w, 500, "internal server error", "save setting failed", "handler", "settingsAPIKeys", "err", err)
				return
			}
			http.Redirect(w, r, "/settings/api-keys?saved=1", http.StatusSeeOther)
			return
		}
		if _, ok := r.Form["watch_present"]; ok {
			// Default-ON kill-switch, same shape as integrity_autorepair.
			val := "0"
			if r.FormValue("watch_enabled") == "1" {
				val = ""
			}
			if err := h.saveAPIKey(ctx, "watch_enabled", val); err != nil {
				httpError(w, 500, "internal server error", "save setting failed", "handler", "settingsAPIKeys", "err", err)
				return
			}
			http.Redirect(w, r, "/settings/api-keys?saved=1", http.StatusSeeOther)
			return
		}
		if _, ok := r.Form["lyrics_present"]; ok {
			// Default-OFF opt-in: store '1' when enabled, clear the row (→ off) otherwise.
			val := ""
			if r.FormValue("lyrics_enabled") == "1" {
				val = "1"
			}
			if err := h.saveAPIKey(ctx, "lyrics_enabled", val); err != nil {
				httpError(w, 500, "internal server error", "save setting failed", "handler", "settingsAPIKeys", "err", err)
				return
			}
			http.Redirect(w, r, "/settings/api-keys?saved=1", http.StatusSeeOther)
			return
		}
		if _, ok := r.Form["keytrace_present"]; ok {
			// Default-OFF opt-in diagnostic, same shape as lyrics_enabled.
			val := ""
			if r.FormValue("keytrace_enabled") == "1" {
				val = "1"
			}
			if err := h.saveAPIKey(ctx, "keytrace_enabled", val); err != nil {
				httpError(w, 500, "internal server error", "save setting failed", "handler", "settingsAPIKeys", "err", err)
				return
			}
			http.Redirect(w, r, "/settings/api-keys?saved=1", http.StatusSeeOther)
			return
		}
		if _, ok := r.Form["playback_present"]; ok {
			// The three playback defaults share one form (the OpenSubtitles
			// key+UA precedent), saved together: two language preferences
			// (sanitized; blank or invalid clears → no preference) and the
			// default-OFF subtitles-on opt-in.
			for _, f := range []string{"default_audio_lang", "default_subtitle_lang"} {
				if err := h.saveAPIKey(ctx, f, sanitizeLangSetting(r.FormValue(f))); err != nil {
					httpError(w, 500, "internal server error", "save setting failed", "handler", "settingsAPIKeys", "err", err)
					return
				}
			}
			val := ""
			if r.FormValue("subtitles_default_on") == "1" {
				val = "1"
			}
			if err := h.saveAPIKey(ctx, "subtitles_default_on", val); err != nil {
				httpError(w, 500, "internal server error", "save setting failed", "handler", "settingsAPIKeys", "err", err)
				return
			}
			http.Redirect(w, r, "/settings/api-keys?saved=1", http.StatusSeeOther)
			return
		}
		if _, ok := r.Form["update_present"]; ok {
			// Default-OFF opt-in automatic update check, same shape as lyrics_enabled.
			val := ""
			if r.FormValue("update_check_enabled") == "1" {
				val = "1"
			}
			if err := h.saveAPIKey(ctx, "update_check_enabled", val); err != nil {
				httpError(w, 500, "internal server error", "save setting failed", "handler", "settingsAPIKeys", "err", err)
				return
			}
			http.Redirect(w, r, "/settings/api-keys?saved=1", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/settings/api-keys", http.StatusSeeOther)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) settingsJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	jobList, err := h.loadScanJobs(r.Context(), "", "", 0, 50)
	if err != nil {
		httpError(w, 500, "internal server error", "load jobs failed", "handler", "settingsJobs", "err", err)
		return
	}
	h.render(w, "settings_jobs.html", map[string]any{
		"Breadcrumb": []crumb{bcHome, bcSettings},
		"Title":      "Jobs",
		"Jobs":       jobList,
	})
}

func (h *Handler) settingsJobsJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	status := strings.TrimSpace(r.URL.Query().Get("status"))
	jobType := strings.TrimSpace(r.URL.Query().Get("job_type"))
	jobIDStr := strings.TrimSpace(r.URL.Query().Get("job_id"))
	var jobID int64
	if jobIDStr != "" {
		v, err := strconv.ParseInt(jobIDStr, 10, 64)
		if err == nil && v > 0 {
			jobID = v
		}
	}
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}

	jobList, err := h.loadScanJobs(r.Context(), status, jobType, jobID, limit)
	if err != nil {
		jsonErr(w, 500, "internal server error", "load jobs failed", "handler", "settingsJobsJSON", "err", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":   true,
		"data": jobList,
	})
}

// settingsJobsFragment renders just the jobs table (the `jobs-container`
// template block) as an HTML fragment. The jobs page polls it and swaps it into
// `#jobs-container` for a live view — so the row markup lives in exactly one
// place (the template), shared by the initial server render and the live update.
func (h *Handler) settingsJobsFragment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	jobList, err := h.loadScanJobs(r.Context(), "", "", 0, 50)
	if err != nil {
		httpError(w, 500, "internal server error", "load jobs failed", "handler", "settingsJobsFragment", "err", err)
		return
	}
	t, ok := h.tpls["settings_jobs.html"]
	if !ok {
		httpError(w, 500, "internal server error", "jobs template missing", "handler", "settingsJobsFragment")
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "jobs-container", map[string]any{"Jobs": jobList}); err != nil {
		httpError(w, 500, "internal server error", "render fragment failed", "handler", "settingsJobsFragment", "err", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) settingsJobsCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		jsonErr(w, 400, "bad request", "parse form failed", "handler", "settingsJobsCancel", "err", err)
		return
	}
	jobID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("job_id")), 10, 64)
	if err != nil || jobID <= 0 {
		jsonError(w, "invalid job_id", http.StatusBadRequest)
		return
	}
	if err := h.jobs.RequestCancel(jobID); err != nil {
		if errors.Is(err, jobs.ErrJobNotFound) {
			jsonError(w, "job not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, jobs.ErrJobNotCancel) {
			jsonError(w, "job is not cancelable", http.StatusBadRequest)
			return
		}
		jsonErr(w, 500, "internal server error", "cancel job failed", "handler", "settingsJobsCancel", "err", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": "cancel requested"})
}

type scanJobRow struct {
	ID              int64  `json:"id"`
	LibraryID       int64  `json:"library_id"`
	JobType         string `json:"job_type"`
	Status          string `json:"status"`
	ProgressCurrent int64  `json:"progress_current"`
	ProgressTotal   int64  `json:"progress_total"`
	DurationMS      int64  `json:"duration_ms"`
	CancelRequested bool   `json:"cancel_requested"`
	CreatedAt       string `json:"created_at"`
	StartedAt       string `json:"started_at"`
	EndedAt         string `json:"ended_at"`
	Error           string `json:"error"`
}

func (h *Handler) loadScanJobs(ctx context.Context, status, jobType string, jobID int64, limit int) ([]scanJobRow, error) {
	query := `SELECT id, library_id, job_type, status, progress_current, progress_total, duration_ms, cancel_requested, created_at, started_at, ended_at, error FROM scan_jobs`
	var conditions []string
	var args []any

	if status != "" {
		conditions = append(conditions, "status=?")
		args = append(args, status)
	}
	if jobType != "" {
		conditions = append(conditions, "job_type=?")
		args = append(args, jobType)
	}
	if jobID > 0 {
		conditions = append(conditions, "id=?")
		args = append(args, jobID)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]scanJobRow, 0, limit)
	for rows.Next() {
		var j scanJobRow
		var cancelReq int
		if err := rows.Scan(&j.ID, &j.LibraryID, &j.JobType, &j.Status, &j.ProgressCurrent, &j.ProgressTotal, &j.DurationMS, &cancelReq, &j.CreatedAt, &j.StartedAt, &j.EndedAt, &j.Error); err != nil {
			return nil, err
		}
		j.CancelRequested = cancelReq != 0
		out = append(out, j)
	}
	return out, rows.Err()
}

// Library management handlers

type libraryRow struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	RootPath string `json:"root_path"`
}

// badRequestError marks a client-side (400) validation failure so callers can
// map it to the right status/response shape (HTML 400 for the web form, JSON 400
// for the CLI) without string-matching.
type badRequestError string

func (e badRequestError) Error() string { return string(e) }

// createLibrary validates and inserts a library row, returning the new id. It is
// the single source of truth for library-creation rules (required fields, valid
// type, and the pathguard containment check that root_path sits under MediaRoot),
// shared by the web form (librariesNew) and the CLI (mgmtLibraryAdd). A
// validation failure is returned as badRequestError; a DB failure as a plain
// wrapped error.
func (h *Handler) createLibrary(ctx context.Context, name, libType, root string) (int64, error) {
	name = strings.TrimSpace(name)
	libType = strings.TrimSpace(libType)
	root = strings.TrimSpace(root)
	if name == "" || libType == "" || root == "" {
		return 0, badRequestError("name, type, root_path are required")
	}
	if !validLibraryType(libType) {
		return 0, badRequestError("invalid type")
	}
	// pathguard.WithinRoot Cleans both sides, so an absolute path that only
	// lexically starts with the media root ("<root>/../etc") can't escape.
	if !pathguard.WithinRoot(root, h.cfg.MediaRoot) {
		return 0, badRequestError("root_path must be under the configured media root")
	}
	res, err := h.db.ExecContext(ctx,
		"INSERT INTO libraries (name, type, root_path) VALUES (?, ?, ?)", name, libType, root)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (h *Handler) libraries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	libs, err := h.loadLibraryList(r.Context())
	if err != nil {
		httpError(w, 500, "internal server error", "load libraries failed", "handler", "libraries", "err", err)
		return
	}
	h.render(w, "libraries.html", map[string]any{
		"Breadcrumb":       []crumb{bcHome, bcSettings},
		"Title":            "Libraries",
		"Libraries":        libs,
		"MediaRoot":        h.cfg.MediaRoot,
		"Saved":            r.URL.Query().Get("saved"),
		"MediaRootInvalid": r.URL.Query().Get("mediaroot") == "invalid",
		"Flagged":          h.integrityStatusCounts(r.Context(), "flagged"),
		"Degraded":         h.integrityStatusCounts(r.Context(), "degraded"),
	})
}

// librariesMediaRoot persists the media folder (the pathguard containment root)
// from the libraries page, so it's configurable without an env var. It's an
// app_settings override applied at the next launch (see resolveEffectiveConfig);
// a blank submission reverts to the env/default. Validated absolute + existing.
func (h *Handler) librariesMediaRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "librariesMediaRoot", "err", err)
		return
	}
	root := strings.TrimSpace(r.FormValue("media_root"))
	if root != "" {
		if err := validateMediaFolder(root); err != nil {
			http.Redirect(w, r, "/libraries?mediaroot=invalid", http.StatusSeeOther)
			return
		}
	}
	if err := h.saveAPIKey(r.Context(), "media_root", root); err != nil {
		httpError(w, 500, "internal server error", "save media root failed", "handler", "librariesMediaRoot", "err", err)
		return
	}
	http.Redirect(w, r, "/libraries?saved=mediaroot", http.StatusSeeOther)
}

func (h *Handler) librariesNew(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.render(w, "libraries_new.html", map[string]any{
			"Breadcrumb": []crumb{bcHome, bcSettings, {Label: "Libraries", Href: "/libraries"}},
			"Title":      "New Library",
			"MediaRoot":  h.cfg.MediaRoot,
		})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			httpError(w, 400, "bad request", "parse form failed", "handler", "librariesNew", "err", err)
			return
		}
		_, err := h.createLibrary(r.Context(), r.FormValue("name"), r.FormValue("type"), r.FormValue("root_path"))
		if err != nil {
			var bre badRequestError
			if errors.As(err, &bre) {
				http.Error(w, bre.Error(), 400)
				return
			}
			httpError(w, 500, "internal server error", "db insert failed", "handler", "librariesNew", "err", err)
			return
		}
		http.Redirect(w, r, "/libraries", http.StatusSeeOther)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// errUnsupportedLibraryType marks a scan request against a library type with
// no scanner (photos/home_videos today).
var errUnsupportedLibraryType = errors.New("scanning not supported for this library type")

// EnqueueLibraryScan enqueues the full scan chain for a library — scan →
// match → integrity_check → probe/loudness, per type. The one owner of the
// chain: the Scan button (librariesScan), the management socket, and the
// filesystem watcher all route through it. Returns sql.ErrNoRows for an
// unknown library and errUnsupportedLibraryType for a type with no scanner.
func (h *Handler) EnqueueLibraryScan(ctx context.Context, id int64, createdBy string) (int64, error) {
	var libType string
	if err := h.db.QueryRowContext(ctx, "SELECT type FROM libraries WHERE id=?", id).Scan(&libType); err != nil {
		return 0, err
	}

	var jobID int64
	var err error
	switch libType {
	case "music":
		scanner := scan.New(h.cfg, h.db)
		jobID, err = h.jobs.Enqueue("scan", id, createdBy, func(ctx context.Context, jID, libID int64) error {
			if err := scanner.ScanMusic(ctx, jID, libID); err != nil {
				return err
			}
			// Chain a music_match job after scan completes.
			matcher := match.New(h.db, h.cfg.DataDir, h.effectiveFanartKey(ctx), h.effectiveAudioDBKey(ctx), h.effectiveLastfmKey(ctx))
			_, _ = h.jobs.Enqueue("music_match", libID, "system", func(ctx context.Context, mJID, mLibID int64) error {
				return matcher.RunMusicMatch(ctx, mJID, mLibID)
			})
			// Chain the cheap container/audio integrity check (auto-repairs new/changed files).
			repair := h.effectiveIntegrityAutoRepair(ctx)
			_, _ = h.jobs.Enqueue("integrity_check", libID, "system", func(ctx context.Context, iJID, iLibID int64) error {
				return integrity.CheckLibrary(ctx, h.db, h.cfg.MediaRoot, "music_tracks", iJID, iLibID, repair)
			})
			// Chain the loudness analysis for volume leveling (new/changed tracks
			// only — loudness_lufs=0). Runs last: a container repair rewrites the
			// file, and its scanner-reset loudness re-measures here.
			_, _ = h.jobs.Enqueue("music_loudness", libID, "system", func(ctx context.Context, lJID, lLibID int64) error {
				return scanner.AnalyzeLoudness(ctx, lJID, lLibID)
			})
			return nil
		})
	case "tv":
		tvScanner := tvscan.New(h.cfg, h.db)
		jobID, err = h.jobs.Enqueue("tvscan", id, createdBy, func(ctx context.Context, jID, libID int64) error {
			if err := tvScanner.ScanTV(ctx, jID, libID); err != nil {
				return err
			}
			// Chain a tv_match job after scan completes if TMDB key is configured.
			if tmdbKey := h.effectiveTMDBKey(ctx); tmdbKey != "" {
				tvMatcher := tmdb.NewMatcher(h.db, tmdbKey, h.cfg.DataDir)
				_, _ = h.jobs.Enqueue("tv_match", libID, "system", func(ctx context.Context, mJID, mLibID int64) error {
					return tvMatcher.RunTVMatch(ctx, mJID, mLibID)
				})
			}
			// Chain the cheap container integrity check (auto-repairs new/changed files).
			repair := h.effectiveIntegrityAutoRepair(ctx)
			_, _ = h.jobs.Enqueue("integrity_check", libID, "system", func(ctx context.Context, iJID, iLibID int64) error {
				return integrity.CheckLibrary(ctx, h.db, h.cfg.MediaRoot, "tv_series_files", iJID, iLibID, repair)
			})
			// Chain a reprobe of files whose scan-time probe failed (empty
			// stream_info_json — often a transient ffmpeg-semaphore timeout), so
			// the seekable HLS path always has the duration it needs. Runs last:
			// an integrity repair can make a previously unprobeable file probe
			// clean. Near-free no-op when nothing is missing.
			_, _ = h.jobs.Enqueue("tv_probe", libID, "system", func(ctx context.Context, pJID, pLibID int64) error {
				return tvScanner.ReprobeMissing(ctx, pJID, pLibID)
			})
			// Chain trickplay sprite generation for new/changed files (measured
			// ~15s per full movie, content-keyed so unchanged files are free).
			_, _ = h.jobs.Enqueue("tv_trickplay", libID, "system", func(ctx context.Context, tJID, tLibID int64) error {
				return h.generateTrickplayMissing(ctx, "tv_series_files", tJID, tLibID)
			})
			return nil
		})
	case "movies":
		movieScanner := moviescan.New(h.cfg, h.db)
		jobID, err = h.jobs.Enqueue("moviescan", id, createdBy, func(ctx context.Context, jID, libID int64) error {
			if err := movieScanner.ScanMovies(ctx, jID, libID); err != nil {
				return err
			}
			// Chain a movie_match job after scan completes if a TMDB key is configured.
			if tmdbKey := h.effectiveTMDBKey(ctx); tmdbKey != "" {
				movieMatcher := tmdb.NewMovieMatcher(h.db, tmdbKey, h.cfg.DataDir)
				_, _ = h.jobs.Enqueue("movie_match", libID, "system", func(ctx context.Context, mJID, mLibID int64) error {
					return movieMatcher.RunMovieMatch(ctx, mJID, mLibID)
				})
			}
			// Chain the cheap container integrity check (auto-repairs new/changed files).
			repair := h.effectiveIntegrityAutoRepair(ctx)
			_, _ = h.jobs.Enqueue("integrity_check", libID, "system", func(ctx context.Context, iJID, iLibID int64) error {
				return integrity.CheckLibrary(ctx, h.db, h.cfg.MediaRoot, "movie_files", iJID, iLibID, repair)
			})
			// Chain a reprobe of files whose scan-time probe failed — the movie
			// twin of the tv_probe chain above.
			_, _ = h.jobs.Enqueue("movie_probe", libID, "system", func(ctx context.Context, pJID, pLibID int64) error {
				return movieScanner.ReprobeMissing(ctx, pJID, pLibID)
			})
			// Chain trickplay sprite generation — the movie twin.
			_, _ = h.jobs.Enqueue("movie_trickplay", libID, "system", func(ctx context.Context, tJID, tLibID int64) error {
				return h.generateTrickplayMissing(ctx, "movie_files", tJID, tLibID)
			})
			return nil
		})
	default:
		return 0, errUnsupportedLibraryType
	}
	return jobID, err
}

func (h *Handler) librariesScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "librariesScan", "err", err)
		return
	}
	idStr := strings.TrimSpace(r.FormValue("id"))
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", 400)
		return
	}

	jobID, err := h.EnqueueLibraryScan(r.Context(), id, "user")
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			http.NotFound(w, r)
		case errors.Is(err, errUnsupportedLibraryType):
			http.Error(w, "scanning not supported for this library type", 400)
		case errors.Is(err, jobs.ErrQueueFull):
			if requestWantsJSON(r) {
				jsonErr(w, http.StatusServiceUnavailable, "service unavailable", "job queue full", "handler", "librariesScan", "err", err)
				return
			}
			httpError(w, http.StatusServiceUnavailable, "service unavailable", "job queue full", "handler", "librariesScan", "err", err)
		default:
			if requestWantsJSON(r) {
				jsonErr(w, 500, "internal server error", "enqueue scan failed", "handler", "librariesScan", "err", err)
				return
			}
			httpError(w, 500, "internal server error", "enqueue scan failed", "handler", "librariesScan", "err", err)
		}
		return
	}

	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"message": "scan queued",
			"data":    map[string]any{"library_id": id, "job_id": jobID},
		})
		return
	}
	http.Redirect(w, r, "/libraries", http.StatusSeeOther)
}

// librariesIntegrityDeep enqueues an integrity_deep job that fully decodes a
// video library's files to detect bitstream corruption the cheap container check
// can't see. Flags only (that damage is unrecoverable); never modifies a file.
// Expensive, so it's an explicit button. TV + movie libraries.
func (h *Handler) librariesIntegrityDeep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "librariesIntegrityDeep", "err", err)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", 400)
		return
	}

	var libType string
	if err := h.db.QueryRowContext(r.Context(), "SELECT type FROM libraries WHERE id=?", id).Scan(&libType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		httpError(w, 500, "internal server error", "db query failed", "handler", "librariesIntegrityDeep", "err", err)
		return
	}
	table := ""
	switch libType {
	case "tv":
		table = "tv_series_files"
	case "movies":
		table = "movie_files"
	case "music":
		table = "music_tracks"
	default:
		http.Error(w, "integrity check is only supported for tv, movie, and music libraries", 400)
		return
	}

	mediaRoot := h.cfg.MediaRoot
	jobID, err := h.jobs.Enqueue("integrity_deep", id, "user", func(ctx context.Context, jID, libID int64) error {
		return integrity.DeepCheckLibrary(ctx, h.db, mediaRoot, table, jID, libID)
	})
	if err != nil {
		if errors.Is(err, jobs.ErrQueueFull) {
			httpError(w, http.StatusServiceUnavailable, "service unavailable", "job queue full", "handler", "librariesIntegrityDeep", "err", err)
			return
		}
		httpError(w, 500, "internal server error", "enqueue integrity check failed", "handler", "librariesIntegrityDeep", "err", err)
		return
	}

	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"message": "integrity check queued",
			"data":    map[string]any{"library_id": id, "job_id": jobID},
		})
		return
	}
	http.Redirect(w, r, "/libraries", http.StatusSeeOther)
}

// librariesJobsStatus returns the latest scan/match job per library as JSON, so
// the libraries page can show a live per-row status (verb + progress) without
// navigating to the jobs page. Library-scoped jobs only (library_id>0); the
// /settings/jobs page remains the full audit record. The JS decides what to
// display from this (it only surfaces a terminal badge for a library it watched
// go active, so old finished jobs never flash on load).
func (h *Handler) librariesJobsStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `
SELECT s.library_id, s.job_type, s.status, s.progress_current, s.progress_total, s.error
FROM scan_jobs s
WHERE s.library_id > 0
  AND s.id = (SELECT MAX(id) FROM scan_jobs WHERE library_id = s.library_id)
`)
	if err != nil {
		jsonErr(w, 500, "internal server error", "db query failed", "handler", "librariesJobsStatus", "err", err)
		return
	}
	defer rows.Close()
	jobs := map[string]map[string]any{}
	for rows.Next() {
		var libID int64
		var jobType, status, errMsg string
		var cur, total int
		if err := rows.Scan(&libID, &jobType, &status, &cur, &total, &errMsg); err != nil {
			jsonErr(w, 500, "internal server error", "row scan failed", "handler", "librariesJobsStatus", "err", err)
			return
		}
		jobs[strconv.FormatInt(libID, 10)] = map[string]any{
			"type": jobType, "status": status, "current": cur, "total": total, "error": errMsg,
		}
	}
	if err := rows.Err(); err != nil {
		jsonErr(w, 500, "internal server error", "rows iteration failed", "handler", "librariesJobsStatus", "err", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "jobs": jobs})
}

func (h *Handler) librariesDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "librariesDelete", "err", err)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var name string
	if err := h.db.QueryRowContext(r.Context(), "SELECT name FROM libraries WHERE id=?", id).Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		httpError(w, 500, "internal server error", "db query failed", "handler", "librariesDelete", "err", err)
		return
	}
	if _, err := h.db.ExecContext(r.Context(), "DELETE FROM libraries WHERE id=?", id); err != nil {
		if requestWantsJSON(r) {
			jsonErr(w, 500, "internal server error", "db delete failed", "handler", "librariesDelete", "err", err)
			return
		}
		httpError(w, 500, "internal server error", "db delete failed", "handler", "librariesDelete", "err", err)
		return
	}
	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"message": "Library deleted.",
			"data":    map[string]any{"library_id": id, "name": name},
		})
		return
	}
	http.Redirect(w, r, "/libraries", http.StatusSeeOther)
}

func (h *Handler) loadLibraryList(ctx context.Context) ([]libraryRow, error) {
	rows, err := h.db.QueryContext(ctx,
		"SELECT id, name, type, root_path FROM libraries ORDER BY id DESC",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var libs []libraryRow
	for rows.Next() {
		var x libraryRow
		if err := rows.Scan(&x.ID, &x.Name, &x.Type, &x.RootPath); err != nil {
			return nil, err
		}
		libs = append(libs, x)
	}
	return libs, rows.Err()
}

func (h *Handler) settingsTagEditor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))

	type tagSearchResult struct {
		ID         int64
		Title      string
		Year       int
		ArtPath    string
		ArtistName string
		TrackCount int
	}

	var results []tagSearchResult
	if q != "" {
		rows, err := h.db.QueryContext(r.Context(), `
			SELECT al.id, al.title, al.year, al.art_path, ar.name,
			       (SELECT COUNT(*) FROM music_tracks t WHERE t.album_id=al.id)
			FROM music_albums al
			JOIN music_artists ar ON ar.id = CASE
			  WHEN al.album_artist_id > 0 THEN al.album_artist_id
			  ELSE al.artist_id
			END
			WHERE al.title LIKE ?
			ORDER BY lower(ar.name), al.year, lower(al.title)
			LIMIT 50
		`, "%"+q+"%")
		if err != nil {
			httpError(w, 500, "internal server error", "db query failed", "handler", "settingsTagEditor", "err", err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var res tagSearchResult
			var art sql.NullString
			if err := rows.Scan(&res.ID, &res.Title, &res.Year, &art, &res.ArtistName, &res.TrackCount); err != nil {
				httpError(w, 500, "internal server error", "row scan failed", "handler", "settingsTagEditor", "err", err)
				return
			}
			res.ArtPath = scanNullString(art)
			results = append(results, res)
		}
		if err := rows.Err(); err != nil {
			httpError(w, 500, "internal server error", "rows iteration failed", "handler", "settingsTagEditor", "err", err)
			return
		}
	}

	h.render(w, "settings_tags.html", map[string]any{
		"Breadcrumb": []crumb{bcHome, bcSettings},
		"Title":      "Tag Editor",
		"Query":      q,
		"Results":    results,
	})
}

func validLibraryType(v string) bool {
	switch v {
	case "music", "movies", "tv", "photos", "home_videos":
		return true
	default:
		return false
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) - m*60
	return fmt.Sprintf("%dm%ds", m, s)
}

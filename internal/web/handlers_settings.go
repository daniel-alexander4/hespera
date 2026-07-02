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
	"hespera/internal/scan"
	"hespera/internal/tmdb"
	"hespera/internal/tvscan"
	"hespera/internal/ytdlp"
)

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	h.render(w, "settings.html", map[string]any{
		"Title": "Settings",
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
		"Title": "About & Attributions",
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

// effectiveYouTubeKey resolves the optional YouTube Data API key the same way:
// the app_settings (UI) value wins, else the env default. Empty disables in-app
// YouTube playback on the year-journey page (it falls back to a link-out).
func (h *Handler) effectiveYouTubeKey(ctx context.Context) string {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='youtube_api_key'").Scan(&v)
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return h.cfg.YouTubeAPIKey
}

// youtubeInappEnabled reports whether the user opted into in-app YouTube audio
// playback (off by default — the engine plays YouTube as hidden background
// audio, a YouTube-ToS gray area, so it's opt-in; off links out to a YouTube tab
// instead).
func (h *Handler) youtubeInappEnabled(ctx context.Context) bool {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='youtube_inapp_enabled'").Scan(&v)
	return strings.TrimSpace(v) == "1"
}

// youtubeYtdlpEnabled reports whether the user opted into the yt-dlp resolver —
// an off-by-default alternative to the quota-limited Data API search. It resolves
// a song by invoking the yt-dlp binary (scraping YouTube, a ToS gray area, hence
// opt-in), escaping the ~100-lookups/day search quota. Off → the Data API search
// path is used.
func (h *Handler) youtubeYtdlpEnabled(ctx context.Context) bool {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='youtube_ytdlp_enabled'").Scan(&v)
	return strings.TrimSpace(v) == "1"
}

// effectiveYouTubeInApp gates the in-app YouTube audio engine: it needs both a
// configured key (to resolve videos) and the opt-in toggle on. The single source
// of truth for whether the per-song button and the journey queue use the hidden
// engine vs. link out.
func (h *Handler) effectiveYouTubeInApp(ctx context.Context) bool {
	return h.effectiveYouTubeKey(ctx) != "" && h.youtubeInappEnabled(ctx)
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
		ytCfg, ytSrc, ytMask := h.keyStatus(ctx, "youtube_api_key", h.cfg.YouTubeAPIKey, h.effectiveYouTubeKey(ctx))
		h.render(w, "settings_apikeys.html", map[string]any{
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
			"YouTubeConfigured":       ytCfg,
			"YouTubeSource":           ytSrc,
			"YouTubeMasked":           ytMask,
			"YouTubeInappEnabled":     h.youtubeInappEnabled(ctx),
			"YouTubeYtdlpEnabled":     h.youtubeYtdlpEnabled(ctx),
			"YtDlpAvailable":          ytdlp.Available(),
			"BillboardEnabled":        h.billboardEnabled(ctx),
			"IntegrityAutoRepair":     h.effectiveIntegrityAutoRepair(ctx),
			"AuthEnabledSetting":      h.authEnabledSetting(ctx),
			"AuthActive":              h.auth.Enabled(),
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
		// YouTube key + the in-app-playback opt-in checkbox share one form. The
		// checkbox always saves (absent = unchecked = off); the key only when
		// non-blank, so toggling the checkbox never wipes a stored key.
		if _, ok := r.Form["youtube_api_key"]; ok {
			inapp := ""
			if r.FormValue("youtube_inapp_enabled") == "1" {
				inapp = "1"
			}
			if err := h.saveAPIKey(ctx, "youtube_inapp_enabled", inapp); err != nil {
				httpError(w, 500, "internal server error", "save setting failed", "handler", "settingsAPIKeys", "err", err)
				return
			}
			ytdlpVal := ""
			if r.FormValue("youtube_ytdlp_enabled") == "1" {
				ytdlpVal = "1"
			}
			if err := h.saveAPIKey(ctx, "youtube_ytdlp_enabled", ytdlpVal); err != nil {
				httpError(w, 500, "internal server error", "save setting failed", "handler", "settingsAPIKeys", "err", err)
				return
			}
			if key := strings.TrimSpace(r.FormValue("youtube_api_key")); key != "" {
				if err := h.saveAPIKey(ctx, "youtube_api_key", key); err != nil {
					httpError(w, 500, "internal server error", "save api key failed", "handler", "settingsAPIKeys", "err", err)
					return
				}
			}
			http.Redirect(w, r, "/settings/api-keys?saved=1", http.StatusSeeOther)
			return
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
		if _, ok := r.Form["auth_present"]; ok {
			// Auth on/off toggle. Enabling first ensures a session secret exists
			// (generated if none), so the next launch boots with auth on instead of
			// a "secret required" config error. Applies at restart — the active
			// middleware is wired at construction, not per request.
			on := r.FormValue("auth_enabled") == "1"
			if on {
				if err := h.ensureAuthSecret(ctx); err != nil {
					httpError(w, 500, "internal server error", "generate session secret failed", "handler", "settingsAPIKeys", "err", err)
					return
				}
			}
			if err := h.saveAuthEnabled(ctx, on); err != nil {
				httpError(w, 500, "internal server error", "save auth toggle failed", "handler", "settingsAPIKeys", "err", err)
				return
			}
			http.Redirect(w, r, "/settings/api-keys?saved=auth", http.StatusSeeOther)
			return
		}
		if _, ok := r.Form["billboard_present"]; ok {
			// Checkbox-only form: an unchecked box submits nothing, so the
			// always-present sentinel triggers the branch and the checkbox's
			// absence reads as off.
			on := r.FormValue("billboard_enabled") == "1"
			val := ""
			if on {
				val = "1"
			}
			if err := h.saveAPIKey(ctx, "billboard_enabled", val); err != nil {
				httpError(w, 500, "internal server error", "save setting failed", "handler", "settingsAPIKeys", "err", err)
				return
			}
			if on {
				h.enqueueBillboardFetch(ctx) // kick the one-time runtime fetch
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
		"Title": "Jobs",
		"Jobs":  jobList,
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
	ID       int64
	Name     string
	Type     string
	RootPath string
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
		"Title":            "Libraries",
		"Libraries":        libs,
		"MediaRoot":        h.cfg.MediaRoot,
		"Saved":            r.URL.Query().Get("saved"),
		"MediaRootInvalid": r.URL.Query().Get("mediaroot") == "invalid",
		"Flagged":          h.integrityFlaggedCounts(r.Context()),
	})
}

// integrityFlaggedCounts returns per-library counts of video files flagged with
// unrepairable corruption, so the libraries page can surface a warning. Keyed by
// library_id; libraries with no flags are absent. Best-effort (nil on error).
func (h *Handler) integrityFlaggedCounts(ctx context.Context) map[int64]int {
	out := map[int64]int{}
	for _, tbl := range []string{"tv_series_files", "movie_files", "music_tracks"} {
		rows, err := h.db.QueryContext(ctx,
			"SELECT library_id, COUNT(*) FROM "+tbl+" WHERE integrity_status='flagged' GROUP BY library_id")
		if err != nil {
			continue
		}
		for rows.Next() {
			var lib int64
			var n int
			if rows.Scan(&lib, &n) == nil {
				out[lib] += n
			}
		}
		rows.Close()
	}
	return out
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
			"Title":     "New Library",
			"MediaRoot": h.cfg.MediaRoot,
		})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			httpError(w, 400, "bad request", "parse form failed", "handler", "librariesNew", "err", err)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		libType := strings.TrimSpace(r.FormValue("type"))
		root := strings.TrimSpace(r.FormValue("root_path"))

		if name == "" || libType == "" || root == "" {
			http.Error(w, "name, type, root_path are required", 400)
			return
		}
		if !validLibraryType(libType) {
			http.Error(w, "invalid type", 400)
			return
		}
		if !strings.HasPrefix(root, h.cfg.MediaRoot+"/") && root != h.cfg.MediaRoot {
			http.Error(w, "root_path must be under the configured media root", 400)
			return
		}
		_, err := h.db.ExecContext(r.Context(),
			"INSERT INTO libraries (name, type, root_path) VALUES (?, ?, ?)",
			name, libType, root,
		)
		if err != nil {
			httpError(w, 500, "internal server error", "db insert failed", "handler", "librariesNew", "err", err)
			return
		}
		http.Redirect(w, r, "/libraries", http.StatusSeeOther)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
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

	var libType string
	if err := h.db.QueryRowContext(r.Context(), "SELECT type FROM libraries WHERE id=?", id).Scan(&libType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		httpError(w, 500, "internal server error", "db query failed", "handler", "librariesScan", "err", err)
		return
	}

	var jobID int64
	switch libType {
	case "music":
		scanner := scan.New(h.cfg, h.db)
		jobID, err = h.jobs.Enqueue("scan", id, "user", func(ctx context.Context, jID, libID int64) error {
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
			return nil
		})
	case "tv":
		tvScanner := tvscan.New(h.cfg, h.db)
		jobID, err = h.jobs.Enqueue("tvscan", id, "user", func(ctx context.Context, jID, libID int64) error {
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
			return nil
		})
	case "movies":
		movieScanner := moviescan.New(h.cfg, h.db)
		jobID, err = h.jobs.Enqueue("moviescan", id, "user", func(ctx context.Context, jID, libID int64) error {
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
			return nil
		})
	default:
		http.Error(w, "scanning not supported for this library type", 400)
		return
	}
	if err != nil {
		if errors.Is(err, jobs.ErrQueueFull) {
			if requestWantsJSON(r) {
				jsonErr(w, http.StatusServiceUnavailable, "service unavailable", "job queue full", "handler", "librariesScan", "err", err)
				return
			}
			httpError(w, http.StatusServiceUnavailable, "service unavailable", "job queue full", "handler", "librariesScan", "err", err)
			return
		}
		if requestWantsJSON(r) {
			jsonErr(w, 500, "internal server error", "enqueue scan failed", "handler", "librariesScan", "err", err)
			return
		}
		httpError(w, 500, "internal server error", "enqueue scan failed", "handler", "librariesScan", "err", err)
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

// librariesReprobe enqueues a tv_probe job that backfills missing stream info
// (ffprobe duration) on a TV library's files, so the seekable HLS path always has
// the duration it needs. Mirrors librariesScan; TV libraries only.
func (h *Handler) librariesReprobe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "librariesReprobe", "err", err)
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
		httpError(w, 500, "internal server error", "db query failed", "handler", "librariesReprobe", "err", err)
		return
	}
	if libType != "tv" {
		http.Error(w, "reprobe is only supported for tv libraries", 400)
		return
	}

	tvScanner := tvscan.New(h.cfg, h.db)
	jobID, err := h.jobs.Enqueue("tv_probe", id, "user", func(ctx context.Context, jID, libID int64) error {
		return tvScanner.ReprobeMissing(ctx, jID, libID)
	})
	if err != nil {
		if errors.Is(err, jobs.ErrQueueFull) {
			httpError(w, http.StatusServiceUnavailable, "service unavailable", "job queue full", "handler", "librariesReprobe", "err", err)
			return
		}
		httpError(w, 500, "internal server error", "enqueue reprobe failed", "handler", "librariesReprobe", "err", err)
		return
	}

	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"message": "reprobe queued",
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
		"Title":   "Tag Editor",
		"Query":   q,
		"Results": results,
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

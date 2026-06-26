package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"isomedia/internal/jobs"
	"isomedia/internal/pathguard"
	"isomedia/internal/tmdb"
)

// --- Row types ---

type tvSeriesRow struct {
	SeriesID     string
	Name         string
	Year         string
	PosterPath   string
	EpisodeCount int
	IsMatched    bool
}

type tvSeasonRow struct {
	SeasonNumber int
	Name         string
	PosterPath   string
	EpisodeCount int
}

type tvEpisodeRow struct {
	EpisodeNumber int
	Name          string
	AirDate       string
	Overview      string
	Resolution    string
	VideoCodec    string
	FileID        int64
	ProgressPct   int
	Completed     bool
}

// --- TV Series List ---

func (h *Handler) tvSeriesList(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/tv" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	series, err := h.loadTVSeriesList(r.Context())
	if err != nil {
		httpError(w, 500, "internal server error", "load tv series list failed", "handler", "tvSeriesList", "err", err)
		return
	}

	h.render(w, "tv_home.html", map[string]any{
		"Title":  "TV Shows",
		"Series": series,
	})
}

func (h *Handler) loadTVSeriesList(ctx context.Context) ([]tvSeriesRow, error) {
	var out []tvSeriesRow

	// Matched series (resolved via TMDB).
	matchedRows, err := h.db.QueryContext(ctx, `
SELECT i.series_id, COUNT(*) AS ep_count
FROM tv_series_identities i
JOIN tv_series_files f ON f.id = i.file_id
WHERE i.status = 'matched' AND i.provider = 'tmdb' AND i.series_id != ''
GROUP BY i.series_id
ORDER BY i.series_id
`)
	if err != nil {
		return nil, err
	}
	defer matchedRows.Close()

	for matchedRows.Next() {
		var seriesID string
		var count int
		if err := matchedRows.Scan(&seriesID, &count); err != nil {
			return nil, err
		}
		name, year, posterPath := h.loadShowMetaSummary(ctx, seriesID)
		if name == "" {
			name = "Unknown Series (TMDB " + seriesID + ")"
		}
		out = append(out, tvSeriesRow{
			SeriesID:     seriesID,
			Name:         name,
			Year:         year,
			PosterPath:   posterPath,
			EpisodeCount: count,
			IsMatched:    true,
		})
	}
	if err := matchedRows.Err(); err != nil {
		return nil, err
	}

	// Unmatched series (unmatched, grouped by guessed_title).
	unmatchedRows, err := h.db.QueryContext(ctx, `
SELECT i.guessed_title, COUNT(*) AS ep_count
FROM tv_series_identities i
JOIN tv_series_files f ON f.id = i.file_id
WHERE i.status = 'unmatched' AND i.guessed_title != ''
GROUP BY lower(i.guessed_title)
ORDER BY lower(i.guessed_title)
`)
	if err != nil {
		return nil, err
	}
	defer unmatchedRows.Close()

	for unmatchedRows.Next() {
		var title string
		var count int
		if err := unmatchedRows.Scan(&title, &count); err != nil {
			return nil, err
		}
		out = append(out, tvSeriesRow{
			SeriesID:     "unmatched:" + title,
			Name:         title + " (unmatched)",
			EpisodeCount: count,
		})
	}
	return out, unmatchedRows.Err()
}

func (h *Handler) loadShowMetaSummary(ctx context.Context, seriesID string) (name, year, posterPath string) {
	entityKey := "show:" + seriesID
	var payload string
	if err := h.db.QueryRowContext(ctx,
		"SELECT payload_json FROM tv_series_metadata_cache WHERE entity_key=? AND lang='en'",
		entityKey,
	).Scan(&payload); err != nil {
		return "", "", ""
	}
	var show tmdb.TVShow
	if err := json.Unmarshal([]byte(payload), &show); err != nil {
		return "", "", ""
	}
	if len(show.FirstAirDate) >= 4 {
		year = show.FirstAirDate[:4]
	}
	return show.Name, year, show.PosterPath
}

// --- TV Series Detail ---

func (h *Handler) tvSeriesDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	seriesID := pathSegment(r, "/tv/series/")
	if seriesID == "" {
		http.NotFound(w, r)
		return
	}

	// Load show metadata.
	entityKey := "show:" + seriesID
	var payload string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT payload_json FROM tv_series_metadata_cache WHERE entity_key=? AND lang='en'",
		entityKey,
	).Scan(&payload); err != nil {
		http.NotFound(w, r)
		return
	}
	var show tmdb.TVShow
	if err := json.Unmarshal([]byte(payload), &show); err != nil {
		http.Error(w, "corrupt metadata", 500)
		return
	}

	// Query seasons that actually have files.
	seasonRows, err := h.db.QueryContext(r.Context(), `
SELECT i.season_number, COUNT(*) AS ep_count
FROM tv_series_identities i
JOIN tv_series_files f ON f.id = i.file_id
WHERE i.series_id = ? AND i.status = 'matched'
GROUP BY i.season_number
ORDER BY i.season_number
`, seriesID)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "tvSeriesDetail", "err", err)
		return
	}
	defer seasonRows.Close()

	var seasons []tvSeasonRow
	for seasonRows.Next() {
		var sn, count int
		if err := seasonRows.Scan(&sn, &count); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "tvSeriesDetail", "err", err)
			return
		}
		seasonName := fmt.Sprintf("Season %d", sn)
		posterPath := ""
		// Try to find cached season info.
		for _, s := range show.Seasons {
			if s.SeasonNumber == sn {
				if s.Name != "" {
					seasonName = s.Name
				}
				posterPath = s.PosterPath
				break
			}
		}
		seasons = append(seasons, tvSeasonRow{
			SeasonNumber: sn,
			Name:         seasonName,
			PosterPath:   posterPath,
			EpisodeCount: count,
		})
	}
	if err := seasonRows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "tvSeriesDetail", "err", err)
		return
	}

	year := ""
	if len(show.FirstAirDate) >= 4 {
		year = show.FirstAirDate[:4]
	}

	h.render(w, "tv_series.html", map[string]any{
		"Title":        show.Name,
		"ShowID":       seriesID,
		"ShowName":     show.Name,
		"Year":         year,
		"Status":       show.Status,
		"Overview":     show.Overview,
		"Genres":       show.Genres,
		"BackdropPath": show.BackdropPath,
		"Seasons":      seasons,
	})
}

// --- TV Season Detail ---

func (h *Handler) tvSeasonDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	seriesID := strings.TrimSpace(r.URL.Query().Get("series"))
	seasonStr := strings.TrimSpace(r.URL.Query().Get("season"))
	seasonNum, err := strconv.Atoi(seasonStr)
	if err != nil || seriesID == "" {
		http.NotFound(w, r)
		return
	}

	// Load show name from cache.
	showName, _, _ := h.loadShowMetaSummary(r.Context(), seriesID)
	if showName == "" {
		showName = "Series " + seriesID
	}

	// Load season metadata from cache and unmarshal once, reusing it for the
	// season name/overview and the per-episode lookup map.
	seasonKey := fmt.Sprintf("show:%s:season:%d", seriesID, seasonNum)
	var seasonPayload string
	var seasonName, seasonOverview string
	epCacheMap := make(map[int]tmdb.TVEpisode)
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT payload_json FROM tv_series_metadata_cache WHERE entity_key=? AND lang='en'",
		seasonKey,
	).Scan(&seasonPayload); err == nil {
		var season tmdb.TVSeason
		if json.Unmarshal([]byte(seasonPayload), &season) == nil {
			seasonName = season.Name
			seasonOverview = season.Overview
			for _, ep := range season.Episodes {
				epCacheMap[ep.EpisodeNumber] = ep
			}
		}
	}
	if seasonName == "" {
		seasonName = fmt.Sprintf("Season %d", seasonNum)
	}

	// Query files for this series+season, with playback progress.
	fileRows, err := h.db.QueryContext(r.Context(), `
SELECT f.id, i.episode_numbers_csv, f.stream_info_json,
       COALESCE(p.position_seconds, 0), COALESCE(p.duration_seconds, 0), COALESCE(p.completed, 0)
FROM tv_series_identities i
JOIN tv_series_files f ON f.id = i.file_id
LEFT JOIN tv_playback_progress p ON p.file_id = f.id
WHERE i.series_id = ? AND i.season_number = ? AND i.status = 'matched'
ORDER BY i.episode_numbers_csv
`, seriesID, seasonNum)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "tvSeasonDetail", "err", err)
		return
	}
	defer fileRows.Close()

	episodeSeen := make(map[int]bool)
	var episodes []tvEpisodeRow
	for fileRows.Next() {
		var fileID int64
		var epCSV, streamJSON string
		var pos, dur float64
		var completed int
		if err := fileRows.Scan(&fileID, &epCSV, &streamJSON, &pos, &dur, &completed); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "tvSeasonDetail", "err", err)
			return
		}
		resolution, videoCodec := extractVideoInfo(streamJSON)

		progressPct := 0
		if dur > 0 {
			progressPct = int(pos / dur * 100)
			if progressPct < 0 {
				progressPct = 0
			} else if progressPct > 100 {
				progressPct = 100
			}
		}

		for _, epStr := range strings.Split(epCSV, ",") {
			epNum, err := strconv.Atoi(strings.TrimSpace(epStr))
			if err != nil || epNum <= 0 {
				continue
			}
			if episodeSeen[epNum] {
				continue
			}
			episodeSeen[epNum] = true

			epRow := tvEpisodeRow{
				EpisodeNumber: epNum,
				Resolution:    resolution,
				VideoCodec:    videoCodec,
				FileID:        fileID,
				ProgressPct:   progressPct,
				Completed:     completed == 1,
			}
			if cached, ok := epCacheMap[epNum]; ok {
				epRow.Name = cached.Name
				epRow.AirDate = cached.AirDate
				epRow.Overview = cached.Overview
			}
			episodes = append(episodes, epRow)
		}
	}
	if err := fileRows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "tvSeasonDetail", "err", err)
		return
	}

	// episode_numbers_csv is TEXT, so the SQL ORDER BY sorts lexically
	// ("10" before "2"). Order numerically by parsed episode number here.
	sort.Slice(episodes, func(i, j int) bool {
		return episodes[i].EpisodeNumber < episodes[j].EpisodeNumber
	})

	h.render(w, "tv_season.html", map[string]any{
		"Title":          fmt.Sprintf("%s — %s", showName, seasonName),
		"ShowID":         seriesID,
		"ShowName":       showName,
		"SeasonName":     seasonName,
		"SeasonOverview": seasonOverview,
		"Episodes":       episodes,
	})
}

// --- TV Match ---

func (h *Handler) tvMatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "tvMatch", "err", err)
		return
	}
	idStr := strings.TrimSpace(r.FormValue("id"))
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", 400)
		return
	}

	if h.cfg.TMDBAPIKey == "" {
		http.Error(w, "TMDB API key not configured", 400)
		return
	}

	matcher := tmdb.NewMatcher(h.db, h.cfg.TMDBAPIKey, h.cfg.DataDir)
	jobID, err := h.jobs.Enqueue("tv_match", id, "user", func(ctx context.Context, jobID, libraryID int64) error {
		return matcher.RunTVMatch(ctx, jobID, libraryID)
	})
	if err != nil {
		if errors.Is(err, jobs.ErrQueueFull) {
			httpError(w, http.StatusServiceUnavailable, "service unavailable", "job queue full", "handler", "tvMatch", "err", err)
			return
		}
		httpError(w, 500, "internal server error", "enqueue tv match failed", "handler", "tvMatch", "err", err)
		return
	}

	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"message": "tv match queued",
			"data":    map[string]any{"library_id": id, "job_id": jobID},
		})
		return
	}
	http.Redirect(w, r, "/libraries", http.StatusSeeOther)
}

// --- TV Art ---

func (h *Handler) tvArt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// Routes: /art/tv/poster/{seriesID}, /art/tv/backdrop/{seriesID}, /art/tv/season/{seriesID}/{seasonNum}
	rest := strings.TrimPrefix(r.URL.Path, "/art/tv/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}

	artType := parts[0]
	seriesID := parts[1]

	var dbArtType string
	var seasonNum int = -1
	switch artType {
	case "poster":
		dbArtType = "series_poster"
	case "backdrop":
		dbArtType = "series_backdrop"
	case "season":
		dbArtType = "season_poster"
		if len(parts) < 3 {
			http.NotFound(w, r)
			return
		}
		seriesID = parts[1]
		sn, err := strconv.Atoi(parts[2])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		seasonNum = sn
	default:
		http.NotFound(w, r)
		return
	}

	tmdbID, err := strconv.Atoi(seriesID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var artPath string
	if seasonNum >= 0 {
		err = h.db.QueryRowContext(r.Context(),
			"SELECT art_path FROM tv_series_art WHERE art_type=? AND tmdb_series_id=? AND season_number=?",
			dbArtType, tmdbID, seasonNum,
		).Scan(&artPath)
	} else {
		err = h.db.QueryRowContext(r.Context(),
			"SELECT art_path FROM tv_series_art WHERE art_type=? AND tmdb_series_id=?",
			dbArtType, tmdbID,
		).Scan(&artPath)
	}
	if err != nil || artPath == "" {
		http.NotFound(w, r)
		return
	}

	dataDir := filepath.Clean(h.cfg.DataDir)
	clean, err := pathguard.ResolveExistingUnderRoot(dataDir, artPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	ct := artMIMEFromExt(clean)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(w, f)
}

// --- TV Match Review ---

type tvMatchGroup struct {
	GuessedTitle string
	FileCount    int
	Seasons      string
}

func (h *Handler) tvMatchReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
SELECT i.guessed_title, COUNT(*) AS file_count,
       GROUP_CONCAT(DISTINCT i.season_number) AS seasons
FROM tv_series_identities i
JOIN tv_series_files f ON f.id = i.file_id
WHERE i.status = 'unmatched' AND i.guessed_title != ''
GROUP BY lower(i.guessed_title)
ORDER BY lower(i.guessed_title)
`)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "tvMatchReview", "err", err)
		return
	}
	defer rows.Close()

	var groups []tvMatchGroup
	for rows.Next() {
		var g tvMatchGroup
		if err := rows.Scan(&g.GuessedTitle, &g.FileCount, &g.Seasons); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "tvMatchReview", "err", err)
			return
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "tvMatchReview", "err", err)
		return
	}

	h.render(w, "tv_match_review.html", map[string]any{
		"Title":  "TV Match Review",
		"Groups": groups,
	})
}

func (h *Handler) tvMatchApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "tvMatchApprove", "err", err)
		return
	}
	guessedTitle := strings.TrimSpace(r.FormValue("guessed_title"))
	tmdbIDStr := strings.TrimSpace(r.FormValue("tmdb_id"))
	tmdbID, err := strconv.Atoi(tmdbIDStr)
	if err != nil || tmdbID <= 0 || guessedTitle == "" {
		http.Error(w, "invalid parameters", 400)
		return
	}

	if h.cfg.TMDBAPIKey == "" {
		http.Error(w, "TMDB API key not configured", 400)
		return
	}

	// Resolve all files with this guessed_title.
	now := fmt.Sprintf("%s", time.Now().UTC().Format(time.RFC3339))
	_, err = h.db.ExecContext(r.Context(), `
UPDATE tv_series_identities SET
  provider='tmdb',
  series_id=?,
  status='matched',
  match_confidence=1.0,
  match_method='manual',
  matched_at=?
WHERE lower(guessed_title) = lower(?) AND status = 'unmatched'
`, strconv.Itoa(tmdbID), now, guessedTitle)
	if err != nil {
		httpError(w, 500, "internal server error", "db update failed", "handler", "tvMatchApprove", "err", err)
		return
	}

	// Fetch metadata via job queue (not detached goroutine).
	matcher := tmdb.NewMatcher(h.db, h.cfg.TMDBAPIKey, h.cfg.DataDir)
	capturedTmdbID := tmdbID
	_, enqErr := h.jobs.Enqueue("tv_metadata_fetch", 0, "user", func(ctx context.Context, jobID, libraryID int64) error {
		return matcher.FetchShowMetadata(ctx, capturedTmdbID)
	})
	if enqErr != nil {
		slog.Warn("failed to enqueue tv metadata fetch", "tmdb_id", tmdbID, "err", enqErr)
	}

	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		return
	}
	http.Redirect(w, r, "/tv/match/review", http.StatusSeeOther)
}

func (h *Handler) tvMatchSkip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "tvMatchSkip", "err", err)
		return
	}
	guessedTitle := strings.TrimSpace(r.FormValue("guessed_title"))
	if guessedTitle == "" {
		http.Error(w, "missing guessed_title", 400)
		return
	}

	_, err := h.db.ExecContext(r.Context(), `
UPDATE tv_series_identities SET status='skipped'
WHERE lower(guessed_title) = lower(?) AND status = 'unmatched'
`, guessedTitle)
	if err != nil {
		httpError(w, 500, "internal server error", "db update failed", "handler", "tvMatchSkip", "err", err)
		return
	}

	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		return
	}
	http.Redirect(w, r, "/tv/match/review", http.StatusSeeOther)
}

func (h *Handler) tvMatchRematch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "tvMatchRematch", "err", err)
		return
	}
	guessedTitle := strings.TrimSpace(r.FormValue("guessed_title"))
	if guessedTitle == "" {
		http.Error(w, "missing guessed_title", 400)
		return
	}

	_, err := h.db.ExecContext(r.Context(), `
UPDATE tv_series_identities SET
  status='unmatched', provider='', series_id='',
  match_confidence=0, match_method='', matched_at=''
WHERE lower(guessed_title) = lower(?)
  AND status IN ('matched','skipped')
`, guessedTitle)
	if err != nil {
		httpError(w, 500, "internal server error", "db update failed", "handler", "tvMatchRematch", "err", err)
		return
	}

	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		return
	}
	http.Redirect(w, r, "/tv/match/review", http.StatusSeeOther)
}

func (h *Handler) tvMatchSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
		return
	}

	if h.cfg.TMDBAPIKey == "" {
		http.Error(w, "TMDB API key not configured", 400)
		return
	}

	matcher := tmdb.NewMatcher(h.db, h.cfg.TMDBAPIKey, h.cfg.DataDir)
	results, err := matcher.SearchTV(r.Context(), query)
	if err != nil {
		httpError(w, 500, "internal server error", "tmdb search failed", "handler", "tvMatchSearch", "err", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}

// --- Video Streaming ---

func (h *Handler) streamTVEpisode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := pathID(r, "/stream/tv/")
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var absPath, container string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT abs_path, container FROM tv_series_files WHERE id=?",
		fileID,
	).Scan(&absPath, &container); err != nil {
		http.NotFound(w, r)
		return
	}

	mediaRoot := filepath.Clean(h.cfg.MediaRoot)
	clean, err := pathguard.ResolveExistingUnderRoot(mediaRoot, absPath)
	if err != nil {
		http.Error(w, "file path is outside media root", 500)
		return
	}

	f, err := os.Open(clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		httpError(w, 500, "internal server error", "stat file failed", "handler", "streamTVEpisode", "err", err)
		return
	}

	ct := videoMIME(container, clean)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, filepath.Base(clean), st.ModTime(), f)
}

func videoMIME(container, filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".mov":
		return "video/quicktime"
	default:
		return "video/mp4"
	}
}

// --- TV Player ---

func (h *Handler) tvPlayer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileIDStr := strings.TrimSpace(r.URL.Query().Get("file"))
	fileID, err := strconv.ParseInt(fileIDStr, 10, 64)
	if err != nil || fileID <= 0 {
		http.NotFound(w, r)
		return
	}

	// Load file + identity info.
	var absPath, container, seriesID, epCSV string
	var seasonNum int
	err = h.db.QueryRowContext(r.Context(), `
SELECT f.abs_path, f.container, i.series_id, i.season_number, i.episode_numbers_csv
FROM tv_series_files f
JOIN tv_series_identities i ON i.file_id = f.id
WHERE f.id = ?
`, fileID).Scan(&absPath, &container, &seriesID, &seasonNum, &epCSV)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Show/episode names from cache.
	showName, _, _ := h.loadShowMetaSummary(r.Context(), seriesID)
	if showName == "" {
		showName = "Unknown Series"
	}

	epName := ""
	epNums := strings.Split(epCSV, ",")
	if len(epNums) > 0 {
		epNum, _ := strconv.Atoi(strings.TrimSpace(epNums[0]))
		if epNum > 0 {
			epKey := fmt.Sprintf("show:%s:season:%d:episode:%d", seriesID, seasonNum, epNum)
			var epPayload string
			if h.db.QueryRowContext(r.Context(),
				"SELECT payload_json FROM tv_series_metadata_cache WHERE entity_key=? AND lang='en'",
				epKey,
			).Scan(&epPayload) == nil {
				var ep tmdb.TVEpisode
				if json.Unmarshal([]byte(epPayload), &ep) == nil {
					epName = ep.Name
				}
			}
			if epName == "" {
				epName = fmt.Sprintf("Episode %d", epNum)
			}
		}
	}

	// Saved position.
	var position, duration float64
	var completed int
	_ = h.db.QueryRowContext(r.Context(),
		"SELECT position_seconds, duration_seconds, completed FROM tv_playback_progress WHERE file_id=?",
		fileID,
	).Scan(&position, &duration, &completed)

	// Prev/next navigation.
	prevFileID, nextFileID := h.findAdjacentEpisode(r.Context(), seriesID, seasonNum, epCSV, fileID)

	h.render(w, "tv_player.html", map[string]any{
		"Title":      fmt.Sprintf("%s — %s", showName, epName),
		"FileID":     fileID,
		"SeriesID":   seriesID,
		"SeasonNum":  seasonNum,
		"ShowName":   showName,
		"EpName":     epName,
		"EpCSV":      epCSV,
		"Position":   position,
		"Duration":   duration,
		"Completed":  completed,
		"PrevFileID": prevFileID,
		"NextFileID": nextFileID,
		"Container":  container,
	})
}

// firstEpNum parses the first episode number from an episode_numbers_csv
// value for numeric ordering. Malformed rows sort last.
func firstEpNum(csv string) int {
	for _, s := range strings.Split(csv, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return n
		}
	}
	return 1 << 30
}

func (h *Handler) findAdjacentEpisode(ctx context.Context, seriesID string, seasonNum int, currentEpCSV string, currentFileID int64) (prevID, nextID int64) {
	rows, err := h.db.QueryContext(ctx, `
SELECT f.id, i.episode_numbers_csv
FROM tv_series_identities i
JOIN tv_series_files f ON f.id = i.file_id
WHERE i.series_id = ? AND i.season_number = ? AND i.status = 'matched'
ORDER BY i.episode_numbers_csv
`, seriesID, seasonNum)
	if err != nil {
		return 0, 0
	}
	defer rows.Close()

	type entry struct {
		id    int64
		epCSV string
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if rows.Scan(&e.id, &e.epCSV) == nil {
			entries = append(entries, e)
		}
	}

	// episode_numbers_csv is TEXT; sort numerically so prev/next navigation is
	// correct past episode 9 (lexical order puts "10" before "2").
	sort.Slice(entries, func(i, j int) bool {
		return firstEpNum(entries[i].epCSV) < firstEpNum(entries[j].epCSV)
	})

	for i, e := range entries {
		if e.id == currentFileID {
			if i > 0 {
				prevID = entries[i-1].id
			}
			if i < len(entries)-1 {
				nextID = entries[i+1].id
			}
			return
		}
	}
	return 0, 0
}

// --- TV Playback Progress ---

func (h *Handler) tvPlaybackProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		FileID          int64   `json:"file_id"`
		PositionSeconds float64 `json:"position_seconds"`
		DurationSeconds float64 `json:"duration_seconds"`
		Completed       bool    `json:"completed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, 400, "bad request", "decode json failed", "handler", "tvPlaybackProgress", "err", err)
		return
	}
	if req.FileID <= 0 {
		http.Error(w, "invalid file_id", 400)
		return
	}

	completedInt := 0
	if req.Completed {
		completedInt = 1
	}

	_, err := h.db.ExecContext(r.Context(), `
INSERT INTO tv_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at)
VALUES (?, ?, ?, ?, datetime('now'))
ON CONFLICT(file_id) DO UPDATE SET
  position_seconds=excluded.position_seconds,
  duration_seconds=excluded.duration_seconds,
  completed=excluded.completed,
  updated_at=datetime('now')
`, req.FileID, req.PositionSeconds, req.DurationSeconds, completedInt)
	if err != nil {
		httpError(w, 500, "internal server error", "db upsert failed", "handler", "tvPlaybackProgress", "err", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// --- Helpers ---

func extractVideoInfo(streamJSON string) (resolution, videoCodec string) {
	if streamJSON == "" || streamJSON == "{}" {
		return "", ""
	}
	type stream struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	}
	type probeData struct {
		Streams []stream `json:"streams"`
	}
	var pd probeData
	if err := json.Unmarshal([]byte(streamJSON), &pd); err != nil {
		return "", ""
	}
	for _, s := range pd.Streams {
		if s.CodecType == "video" && s.Width > 0 {
			resolution = fmt.Sprintf("%dx%d", s.Width, s.Height)
			videoCodec = s.CodecName
			return
		}
	}
	return "", ""
}

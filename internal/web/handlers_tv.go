package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"hespera/internal/introskip"
	"hespera/internal/jobs"
	"hespera/internal/pathguard"
	"hespera/internal/tmdb"
	"hespera/internal/tvscan"
	"hespera/internal/video"
)

// maxSeriesScanDirs caps how many distinct show folders a per-series scan walks
// before falling back to a full library scan (a pathologically scattered series).
const maxSeriesScanDirs = 8

// --- Row types ---

type tvSeriesRow struct {
	SeriesID     string
	Name         string
	Year         string
	PosterPath   string
	EpisodeCount int
	IsMatched    bool
	// SeasonNumber is the in-progress season for a "Continue Watching" row (the
	// season of the most-recently-watched episode), so the card can deep-link
	// straight to that season's episode list. Zero/unused for other rows.
	SeasonNumber int
	// RecencyUnix is the row's sort timestamp (unix seconds): last-watched for the
	// watched query, file mtime for the added query. Lets the home "Continue
	// Watching" row interleave TV with movies by recency; unused in the TV views.
	RecencyUnix int64
}

type tvSeasonRow struct {
	SeasonNumber int
	Name         string
	EpisodeCount int
	Missing      bool // in TMDB metadata but no files present locally
	FlaggedCount int  // files with unrepairable corruption (integrity_status='flagged')
}

// tvPosterPlaceholder is the static asset served for a season card when no
// season or series artwork is available (see tvArt).
const tvPosterPlaceholder = "tv-poster-placeholder.webp"

type tvEpisodeRow struct {
	EpisodeNumber int
	Name          string
	AirDate       string
	Overview      string
	FileID        int64
	ProgressPct   int
	Completed     bool
	Missing       bool   // in TMDB metadata but no file present locally
	Flagged       bool   // file has unrepairable corruption (integrity_status='flagged')
	FlagDetail    string // integrity_detail — the human-readable reason
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

	series, nav, unmatched, err := h.loadTVSeriesList(r.Context(), pageParam(r))
	if err != nil {
		httpError(w, 500, "internal server error", "load tv series list failed", "handler", "tvSeriesList", "err", err)
		return
	}

	// In-place paging (grid_pager.js) fetches just the series card grid —
	// short-circuit before the "Recent" sub-tab queries it doesn't use.
	if r.URL.Query().Get("grid") == "1" {
		h.renderFragment(w, "tv_home.html", "tv-cards", series)
		return
	}

	// "Recent" sub-tab content — non-fatal so the page still renders if these fail.
	recentlyWatched, err := h.recentTVSeries(r.Context(), tvRecentlyWatchedQuery, 18)
	if err != nil {
		slog.Warn("load recently-watched tv failed", "handler", "tvSeriesList", "err", err)
	}
	recentlyAdded, err := h.recentTVSeries(r.Context(), tvRecentlyAddedQuery, 18)
	if err != nil {
		slog.Warn("load recently-added tv failed", "handler", "tvSeriesList", "err", err)
	}

	h.render(w, "tv_home.html", map[string]any{
		"Breadcrumb":      []crumb{bcHome},
		"Title":           "TV Shows",
		"Series":          series,
		"SeriesPage":      nav,
		"UnmatchedCount":  unmatched,
		"RecentlyWatched": recentlyWatched,
		"RecentlyAdded":   recentlyAdded,
	})
}

// TV "Recent" sub-tab queries: matched series ordered by most-recent playback
// (recently watched) and by newest file mtime on disk (recently added — there is
// no created_at on tv_series_files, and mtime tracks when a download landed).
// tvRecentlyWatchedQuery returns each watched series with the season to deep-link
// its "Continue Watching" card to. The `watched` CTE finds the in-progress season
// `nr` — season_number is a bare column paired with MAX(p.updated_at), so SQLite
// takes it from the most-recently-watched episode's row. The outer query then
// rolls forward: the target is the smallest local season >= nr that still has an
// unwatched (not-completed) episode — so finishing a season advances the card to
// the next season with something to play — falling back to nr when everything
// from there on is watched (a re-watch). "Not completed" matches the season
// page's ✓ (COALESCE(completed,0)=0).
const tvRecentlyWatchedQuery = `
WITH watched AS (
  SELECT i.series_id AS sid, i.season_number AS nr, MAX(p.updated_at) AS last_watched
  FROM tv_playback_progress p
  JOIN tv_series_files f ON f.id = p.file_id
  JOIN tv_series_identities i ON i.file_id = f.id
  WHERE i.status = 'matched' AND i.provider = 'tmdb' AND i.series_id != ''
  GROUP BY i.series_id
)
SELECT w.sid,
       COALESCE(
         (SELECT MIN(i2.season_number)
          FROM tv_series_identities i2
          LEFT JOIN tv_playback_progress p2 ON p2.file_id = i2.file_id
          WHERE i2.series_id = w.sid AND i2.status = 'matched'
            AND i2.season_number >= w.nr
            AND COALESCE(p2.completed, 0) = 0),
         w.nr) AS target_season,
       CAST(strftime('%s', w.last_watched) AS INTEGER) AS recency
FROM watched w
ORDER BY w.last_watched DESC
LIMIT ?`

// tvContinueWatchingQuery is the home Continue-Watching variant of
// tvRecentlyWatchedQuery: same CTE and forward-only season roll, but a series
// with nothing left unwatched at-or-after its watch point is DROPPED instead of
// falling back to a "rewatch" card — matching the movie row's completed=0
// filter, so the merged home row holds only continuable items. Starting a
// rewatch resurfaces the show automatically (the progress upsert recomputes
// completed on every save). The /tv page's "Recently Watched" strip keeps the
// unfiltered query — there a finished show WAS recently watched.
const tvContinueWatchingQuery = `
WITH watched AS (
  SELECT i.series_id AS sid, i.season_number AS nr, MAX(p.updated_at) AS last_watched
  FROM tv_playback_progress p
  JOIN tv_series_files f ON f.id = p.file_id
  JOIN tv_series_identities i ON i.file_id = f.id
  WHERE i.status = 'matched' AND i.provider = 'tmdb' AND i.series_id != ''
  GROUP BY i.series_id
)
SELECT sid, target_season, recency FROM (
  SELECT w.sid AS sid,
         (SELECT MIN(i2.season_number)
          FROM tv_series_identities i2
          LEFT JOIN tv_playback_progress p2 ON p2.file_id = i2.file_id
          WHERE i2.series_id = w.sid AND i2.status = 'matched'
            AND i2.season_number >= w.nr
            AND COALESCE(p2.completed, 0) = 0) AS target_season,
         CAST(strftime('%s', w.last_watched) AS INTEGER) AS recency
  FROM watched w
)
WHERE target_season IS NOT NULL
ORDER BY recency DESC
LIMIT ?`

// tvRecentlyAddedQuery returns the newest-on-disk matched series. season_number
// is selected to match the three-column shape recentTVSeries scans, but the
// Recently-Added card links to the series page, so the value is unused; the
// recency column surfaces the existing mtime sort key for the shared scanner.
const tvRecentlyAddedQuery = `
SELECT i.series_id, i.season_number, MAX(f.mtime_unix) AS recency
FROM tv_series_identities i
JOIN tv_series_files f ON f.id = i.file_id
WHERE i.status = 'matched' AND i.provider = 'tmdb' AND i.series_id != ''
GROUP BY i.series_id
ORDER BY MAX(f.mtime_unix) DESC
LIMIT ?`

// recentTVSeries runs an ordered series-id query and resolves each id to a
// display row (name/year/poster) via the shared metadata-cache helper, used by
// the TV "Recent" sub-tab. Series ids are kept in query order.
func (h *Handler) recentTVSeries(ctx context.Context, query string, limit int) ([]tvSeriesRow, error) {
	rows, err := h.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type idSeason struct {
		id      string
		season  int
		recency int64
	}
	var items []idSeason
	for rows.Next() {
		var it idSeason
		if err := rows.Scan(&it.id, &it.season, &it.recency); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.id
	}
	metas := h.loadShowMetaSummaries(ctx, ids)
	out := make([]tvSeriesRow, 0, len(items))
	for _, it := range items {
		meta := metas[it.id]
		if meta.name == "" {
			meta.name = "Unknown Series (TMDB " + it.id + ")"
		}
		out = append(out, tvSeriesRow{
			SeriesID:     it.id,
			Name:         meta.name,
			Year:         meta.year,
			PosterPath:   meta.posterPath,
			SeasonNumber: it.season,
			RecencyUnix:  it.recency,
			IsMatched:    true,
		})
	}
	return out, nil
}

// tvSeriesListBase is the FROM clause for the matched-series list: distinct
// matched series with their episode counts, LEFT JOINed to the TMDB metadata
// cache with the show name/poster/air-date pulled out of the cached JSON payload
// as real columns (json_extract). Surfacing the name as a column is what lets the
// sort, ?q= filter, and pagination all run in SQL — like the music browse lists —
// instead of loading every matched series into memory and sorting in Go. The
// name is functionally per-series, so grouping first (O(series) cache lookups)
// then joining keeps it cheap.
const tvSeriesListBase = `
FROM (
  SELECT sub.series_id, sub.ep_count,
         COALESCE(json_extract(c.payload_json, '$.name'), '') AS name,
         COALESCE(json_extract(c.payload_json, '$.poster_path'), '') AS poster_path,
         COALESCE(json_extract(c.payload_json, '$.first_air_date'), '') AS first_air
  FROM (
    SELECT i.series_id, COUNT(*) AS ep_count
    FROM tv_series_identities i
    JOIN tv_series_files f ON f.id = i.file_id
    WHERE i.status = 'matched' AND i.provider = 'tmdb' AND i.series_id != ''
    GROUP BY i.series_id
  ) sub
  LEFT JOIN tv_series_metadata_cache c
    ON c.entity_key = 'show:' || sub.series_id AND c.lang = 'en'
) s`

// loadTVSeriesList returns one page of the matched (watchable) series, sorted by
// name in SQL, plus a count of distinct unmatched titles (surfaced as a "needs
// matching" banner, not rendered inline).
func (h *Handler) loadTVSeriesList(ctx context.Context, page int) ([]tvSeriesRow, pageNav, int, error) {
	var total int
	if err := h.db.QueryRowContext(ctx, "SELECT COUNT(*) "+tvSeriesListBase).Scan(&total); err != nil {
		return nil, pageNav{}, 0, err
	}
	nav, offset := paginate(page, total, "/tv")

	rows, err := h.db.QueryContext(ctx,
		"SELECT s.series_id, s.ep_count, s.name, s.poster_path, s.first_air "+tvSeriesListBase+
			" ORDER BY lower(s.name), s.series_id LIMIT ? OFFSET ?", listPageSize, offset)
	if err != nil {
		return nil, pageNav{}, 0, err
	}
	defer rows.Close()

	out := make([]tvSeriesRow, 0, listPageSize)
	for rows.Next() {
		var row tvSeriesRow
		var firstAir string
		if err := rows.Scan(&row.SeriesID, &row.EpisodeCount, &row.Name, &row.PosterPath, &firstAir); err != nil {
			return nil, pageNav{}, 0, err
		}
		if len(firstAir) >= 4 {
			row.Year = firstAir[:4]
		}
		if row.Name == "" {
			row.Name = "Unknown Series (TMDB " + row.SeriesID + ")"
		}
		row.IsMatched = true
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, pageNav{}, 0, err
	}

	var unmatched int
	_ = h.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM (
  SELECT 1 FROM tv_series_identities i
  JOIN tv_series_files f ON f.id = i.file_id
  WHERE i.status = 'unmatched' AND i.guessed_title != ''
  GROUP BY lower(i.guessed_title)
)`).Scan(&unmatched)

	return out, nav, unmatched, nil
}

type showMetaSummary struct {
	name       string
	year       string
	posterPath string
}

// loadShowMetaSummaries fetches metadata for many series in a single query,
// keyed by series id, to avoid an N+1 over tv_series_metadata_cache.
func (h *Handler) loadShowMetaSummaries(ctx context.Context, seriesIDs []string) map[string]showMetaSummary {
	out := make(map[string]showMetaSummary, len(seriesIDs))
	if len(seriesIDs) == 0 {
		return out
	}
	placeholders := make([]string, len(seriesIDs))
	args := make([]any, len(seriesIDs))
	keyToSeries := make(map[string]string, len(seriesIDs))
	for i, sid := range seriesIDs {
		key := "show:" + sid
		placeholders[i] = "?"
		args[i] = key
		keyToSeries[key] = sid
	}
	query := "SELECT entity_key, payload_json FROM tv_series_metadata_cache WHERE lang='en' AND entity_key IN (" +
		strings.Join(placeholders, ",") + ")"
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var entityKey, payload string
		if rows.Scan(&entityKey, &payload) != nil {
			continue
		}
		var show tmdb.TVShow
		if json.Unmarshal([]byte(payload), &show) != nil {
			continue
		}
		var year string
		if len(show.FirstAirDate) >= 4 {
			year = show.FirstAirDate[:4]
		}
		out[keyToSeries[entityKey]] = showMetaSummary{name: show.Name, year: year, posterPath: show.PosterPath}
	}
	return out
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

	// Load show metadata; fall back to a matched identity's guessed title when it
	// isn't cached yet (a just-approved series whose tv_metadata_fetch job is
	// still running, or one whose fetch failed) so the page renders its local
	// seasons + lazy cast instead of 404ing — a reload shows the full data.
	entityKey := "show:" + seriesID
	var show tmdb.TVShow
	var payload string
	if metaErr := h.db.QueryRowContext(r.Context(),
		"SELECT payload_json FROM tv_series_metadata_cache WHERE entity_key=? AND lang='en'",
		entityKey,
	).Scan(&payload); metaErr == nil {
		if err := json.Unmarshal([]byte(payload), &show); err != nil {
			http.Error(w, "corrupt metadata", 500)
			return
		}
	} else {
		var gt string
		_ = h.db.QueryRowContext(r.Context(),
			"SELECT guessed_title FROM tv_series_identities WHERE series_id=? AND status='matched' LIMIT 1",
			seriesID,
		).Scan(&gt)
		if gt == "" {
			http.NotFound(w, r)
			return
		}
		show.Name = gt
	}

	// Query seasons that actually have files.
	seasonRows, err := h.db.QueryContext(r.Context(), `
SELECT i.season_number, COUNT(*) AS ep_count,
       SUM(CASE WHEN f.integrity_status = 'flagged' THEN 1 ELSE 0 END) AS flagged
FROM tv_series_identities i
JOIN tv_series_files f ON f.id = i.file_id
WHERE i.series_id = ? AND i.status = 'matched' AND i.season_number >= 0
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
		var sn, count, flagged int
		if err := seasonRows.Scan(&sn, &count, &flagged); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "tvSeriesDetail", "err", err)
			return
		}
		seasonName := fmt.Sprintf("Season %d", sn)
		// Use the cached season name when available.
		for _, s := range show.Seasons {
			if s.SeasonNumber == sn {
				if s.Name != "" {
					seasonName = s.Name
				}
				break
			}
		}
		seasons = append(seasons, tvSeasonRow{
			SeasonNumber: sn,
			Name:         seasonName,
			EpisodeCount: count,
			FlaggedCount: flagged,
		})
	}
	if err := seasonRows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "tvSeriesDetail", "err", err)
		return
	}

	// Surface seasons TMDB knows about that have no local files. Reflects
	// last-match-time cache.
	present := make(map[int]bool, len(seasons))
	for _, s := range seasons {
		present[s.SeasonNumber] = true
	}
	missing := missingSeasons(show, present)
	seasons = append(seasons, missing...)
	sort.Slice(seasons, func(i, j int) bool { return seasons[i].SeasonNumber < seasons[j].SeasonNumber })

	year := ""
	if len(show.FirstAirDate) >= 4 {
		year = show.FirstAirDate[:4]
	}

	// The library this series lives in, for the per-series "scan for new episodes"
	// button + its live status badge (0 → no matched files, button hidden).
	var libraryID int64
	_ = h.db.QueryRowContext(r.Context(), `
SELECT f.library_id FROM tv_series_files f
JOIN tv_series_identities i ON i.file_id = f.id
WHERE i.series_id = ? AND i.status = 'matched' LIMIT 1`, seriesID).Scan(&libraryID)

	// Local extras (trailers/featurettes/…): ownership is path-derived — the
	// extras living under this series' show folder(s), same folder mapping the
	// per-series scan uses.
	var extras []extraRow
	if pathRows, exErr := h.db.QueryContext(r.Context(), `
SELECT f.abs_path FROM tv_series_files f
JOIN tv_series_identities i ON i.file_id = f.id
WHERE i.series_id = ? AND i.status = 'matched'`, seriesID); exErr == nil {
		var paths []string
		for pathRows.Next() {
			var p string
			if pathRows.Scan(&p) == nil {
				paths = append(paths, p)
			}
		}
		pathRows.Close()
		extras = h.extrasUnderDirs(r.Context(), "tv_series_files", "tv_playback_progress", tvscan.ShowDirsForFiles(paths))
	}

	// Cast strip. Loaded from cache; if this series' cast was never fetched
	// (e.g. it matched before this feature, or has none), enqueue a background
	// fetch so it populates on the next view — the handler never blocks on it.
	var cast []castMemberRow
	var backdropVer string
	if sid, err := strconv.Atoi(seriesID); err == nil && sid > 0 {
		cast = h.loadSeriesCast(r.Context(), sid)
		if !h.castFetched(r.Context(), sid) {
			h.enqueueMetaFetch(r.Context(), fmt.Sprintf("cast:%d", sid), "tv_cast_fetch",
				func(ctx context.Context, m *tmdb.Matcher) error { return m.FetchTVCast(ctx, sid) })
		}
		// Backfill a hi-res (w1280) backdrop for shows matched before the size
		// bump — their on-disk banner is the old soft w500. Background, once.
		if show.BackdropPath != "" && !h.metaMarkerExists(r.Context(), fmt.Sprintf("show:%d:backdrop_hires", sid)) {
			h.enqueueMetaFetch(r.Context(), fmt.Sprintf("backdrop:%d", sid), "tv_backdrop_refresh",
				func(ctx context.Context, m *tmdb.Matcher) error { return m.RefetchBackdrop(ctx, sid) })
		}
		// Cache-bust the backdrop URL with its fetched_at so a w500→w1280 upgrade
		// (or any re-fetch) reaches the browser — the URL is otherwise stable and
		// the image would be served from cache.
		var fa string
		if h.db.QueryRowContext(r.Context(),
			"SELECT fetched_at FROM tv_series_art WHERE art_type='series_backdrop' AND tmdb_series_id=?",
			sid).Scan(&fa) == nil {
			backdropVer = artVersion(fa)
		}
	}

	h.render(w, "tv_series.html", map[string]any{
		"Breadcrumb":     []crumb{bcHome, bcTV},
		"Title":          show.Name,
		"ShowID":         seriesID,
		"ShowName":       show.Name,
		"Year":           year,
		"Status":         show.Status,
		"Overview":       show.Overview,
		"Genres":         show.Genres,
		"BackdropPath":   show.BackdropPath,
		"Seasons":        seasons,
		"MissingSeasons": len(missing),
		"Cast":           cast,
		"BackdropVer":    backdropVer,
		"LibraryID":      libraryID,
		"Extras":         extras,
	})
}

// artVersion reduces an art row's fetched_at timestamp to a compact URL-safe
// cache-busting token (alphanumerics only), so the image URL changes whenever
// the underlying file is re-fetched.
func artVersion(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// missingSeasons returns rows for TMDB seasons that have no local files.
// Specials (season 0) are excluded as noise.
func missingSeasons(show tmdb.TVShow, present map[int]bool) []tvSeasonRow {
	var out []tvSeasonRow
	for _, s := range show.Seasons {
		if s.SeasonNumber <= 0 || present[s.SeasonNumber] {
			continue
		}
		name := s.Name
		if name == "" {
			name = fmt.Sprintf("Season %d", s.SeasonNumber)
		}
		out = append(out, tvSeasonRow{
			SeasonNumber: s.SeasonNumber,
			Name:         name,
			Missing:      true,
		})
	}
	return out
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
SELECT f.id, i.episode_numbers_csv,
       COALESCE(p.position_seconds, 0), COALESCE(p.duration_seconds, 0), COALESCE(p.completed, 0),
       f.integrity_status, f.integrity_detail
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
		var epCSV string
		var pos, dur float64
		var completed int
		var integStatus, integDetail string
		if err := fileRows.Scan(&fileID, &epCSV, &pos, &dur, &completed, &integStatus, &integDetail); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "tvSeasonDetail", "err", err)
			return
		}

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
				FileID:        fileID,
				ProgressPct:   progressPct,
				Completed:     completed == 1,
				Flagged:       integStatus == "flagged",
				FlagDetail:    integDetail,
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

	// Surface episodes TMDB knows about for this season that have no local
	// file, as greyed rows. Reflects last-match-time cache.
	missingEps := missingEpisodes(epCacheMap, episodeSeen)
	episodes = append(episodes, missingEps...)

	// episode_numbers_csv is TEXT, so the SQL ORDER BY sorts lexically
	// ("10" before "2"). Order numerically by parsed episode number here.
	sort.Slice(episodes, func(i, j int) bool {
		return episodes[i].EpisodeNumber < episodes[j].EpisodeNumber
	})

	h.render(w, "tv_season.html", map[string]any{
		"Breadcrumb":      []crumb{bcHome, bcTV, bcSeries(seriesID, showName)},
		"Title":           fmt.Sprintf("%s — %s", showName, seasonName),
		"ShowID":          seriesID,
		"ShowName":        showName,
		"SeasonNum":       seasonNum,
		"SeasonName":      seasonName,
		"SeasonOverview":  seasonOverview,
		"Episodes":        episodes,
		"MissingEpisodes": len(missingEps),
	})
}

// tvMarkWatched sets or clears the watched flag without playback: a single
// file (`file=N`) or a whole season (`series=X&season=N`), `watched=1|0`.
// Marking watched keeps any partial position (completed alone drives the
// watched semantics everywhere); marking unwatched resets position so the next
// play starts fresh. Redirects back to the season page.
func (h *Handler) tvMarkWatched(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "tvMarkWatched", "err", err)
		return
	}
	watched := r.FormValue("watched") == "1"
	seriesID := strings.TrimSpace(r.FormValue("series"))
	season, seasonErr := strconv.Atoi(strings.TrimSpace(r.FormValue("season")))

	var fileIDs []int64
	if fileStr := strings.TrimSpace(r.FormValue("file")); fileStr != "" {
		id, err := strconv.ParseInt(fileStr, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid file", 400)
			return
		}
		fileIDs = append(fileIDs, id)
	} else if seriesID != "" && seasonErr == nil {
		rows, err := h.db.QueryContext(r.Context(), `
SELECT f.id FROM tv_series_identities i
JOIN tv_series_files f ON f.id = i.file_id
WHERE i.series_id = ? AND i.season_number = ? AND i.status = 'matched'`, seriesID, season)
		if err != nil {
			httpError(w, 500, "internal server error", "db query failed", "handler", "tvMarkWatched", "err", err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				httpError(w, 500, "internal server error", "row scan failed", "handler", "tvMarkWatched", "err", err)
				return
			}
			fileIDs = append(fileIDs, id)
		}
		if err := rows.Err(); err != nil {
			httpError(w, 500, "internal server error", "rows iteration failed", "handler", "tvMarkWatched", "err", err)
			return
		}
	} else {
		http.Error(w, "file or series+season required", 400)
		return
	}

	if err := markWatched(r.Context(), h.db, "tv_playback_progress", fileIDs, watched); err != nil {
		httpError(w, 500, "internal server error", "db upsert failed", "handler", "tvMarkWatched", "err", err)
		return
	}

	if seriesID != "" && seasonErr == nil {
		http.Redirect(w, r, fmt.Sprintf("/tv/season/?series=%s&season=%d", url.QueryEscape(seriesID), season), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/tv", http.StatusSeeOther)
}

// markWatched upserts the completed flag for a set of files in one progress
// table (`tv_playback_progress` / `movie_playback_progress` — identical shape).
// Unwatching zeroes the position so a replay starts from the top.
func markWatched(ctx context.Context, db *sql.DB, table string, fileIDs []int64, watched bool) error {
	completed := 0
	if watched {
		completed = 1
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range fileIDs {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO `+table+` (file_id, position_seconds, duration_seconds, completed, updated_at)
VALUES (?, 0, 0, ?, datetime('now'))
ON CONFLICT(file_id) DO UPDATE SET
  completed = excluded.completed,
  position_seconds = CASE WHEN excluded.completed = 1 THEN position_seconds ELSE 0 END,
  updated_at = datetime('now')
`, id, completed); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// missingEpisodes returns rows for episodes in the cached TMDB season that have
// no local file present.
func missingEpisodes(epCache map[int]tmdb.TVEpisode, present map[int]bool) []tvEpisodeRow {
	var out []tvEpisodeRow
	for epNum, ep := range epCache {
		if epNum <= 0 || present[epNum] {
			continue
		}
		out = append(out, tvEpisodeRow{
			EpisodeNumber: epNum,
			Name:          ep.Name,
			AirDate:       ep.AirDate,
			Overview:      ep.Overview,
			Missing:       true,
		})
	}
	return out
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

	tmdbKey := h.effectiveTMDBKey(r.Context())
	if tmdbKey == "" {
		http.Error(w, "TMDB API key not configured", 400)
		return
	}

	matcher := tmdb.NewMatcher(h.db, tmdbKey, h.cfg.DataDir)
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

// tvSeriesScan rescans only one series' show folder(s) — for picking up a newly
// added episode/season without re-walking the whole TV library. It derives the
// folder(s) from the series' existing matched file paths (ShowDirsForFiles),
// enqueues a scoped scan, then chains a tv_match so the new file is identified.
func (h *Handler) tvSeriesScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "tvSeriesScan", "err", err)
		return
	}
	seriesID := strings.TrimSpace(r.FormValue("series_id"))
	if _, e := strconv.Atoi(seriesID); e != nil {
		http.Error(w, "invalid series_id", 400)
		return
	}

	// A series lives in one TV library; take the first library_id seen and scope
	// the scan to its files only.
	rows, err := h.db.QueryContext(r.Context(), `
SELECT f.abs_path, f.library_id
FROM tv_series_files f
JOIN tv_series_identities i ON i.file_id = f.id
WHERE i.series_id = ? AND i.status = 'matched'
`, seriesID)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "tvSeriesScan", "err", err)
		return
	}
	defer rows.Close()
	var libraryID int64
	var paths []string
	for rows.Next() {
		var ap string
		var lib int64
		if err := rows.Scan(&ap, &lib); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "tvSeriesScan", "err", err)
			return
		}
		if libraryID == 0 {
			libraryID = lib
		}
		if lib == libraryID {
			paths = append(paths, ap)
		}
	}
	if err := rows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "tvSeriesScan", "err", err)
		return
	}
	if libraryID == 0 || len(paths) == 0 {
		http.Error(w, "no files for this series", 404)
		return
	}

	dirs := tvscan.ShowDirsForFiles(paths)
	scanner := tvscan.New(h.cfg, h.db)
	tmdbKey := h.effectiveTMDBKey(r.Context())

	jobID, err := h.jobs.Enqueue("tvscan", libraryID, "user", func(ctx context.Context, jID, libID int64) error {
		var scanErr error
		if len(dirs) == 0 || len(dirs) > maxSeriesScanDirs {
			scanErr = scanner.ScanTV(ctx, jID, libID) // fallback: scattered/none → full scan
		} else {
			scanErr = scanner.ScanTVDirs(ctx, jID, libID, dirs)
		}
		if scanErr != nil {
			return scanErr
		}
		if tmdbKey != "" {
			matcher := tmdb.NewMatcher(h.db, tmdbKey, h.cfg.DataDir)
			_, _ = h.jobs.Enqueue("tv_match", libID, "system", func(ctx context.Context, mJID, mLibID int64) error {
				return matcher.RunTVMatch(ctx, mJID, mLibID)
			})
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, jobs.ErrQueueFull) {
			httpError(w, http.StatusServiceUnavailable, "service unavailable", "job queue full", "handler", "tvSeriesScan", "err", err)
			return
		}
		httpError(w, 500, "internal server error", "enqueue series scan failed", "handler", "tvSeriesScan", "err", err)
		return
	}

	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"message": "series scan queued",
			"data":    map[string]any{"library_id": libraryID, "job_id": jobID},
		})
		return
	}
	http.Redirect(w, r, "/tv/series/"+seriesID, http.StatusSeeOther)
}

// introDetectWindowSec is how much of each episode's audio (from the start) is
// fingerprinted for intro detection. 600s covers shows with long, variable cold-opens
// before the title theme — e.g. Doctor Who (2023), where the theme lands anywhere from
// ~19s to ~398s into the episode (verified against the real season).
const introDetectWindowSec = 600.0

// introEpisode is one matched episode considered for intro fingerprinting.
type introEpisode struct {
	fileID  int64
	absPath string
	season  int
}

// gatherIntroSeasons groups a series' matched episodes by season (limited to one
// season when seasonFilter > 0), returning the library id and the number of
// episodes that will actually be fingerprinted — only seasons with >= 2 episodes,
// since cross-episode matching needs at least a pair. total == 0 = nothing to detect.
func (h *Handler) gatherIntroSeasons(ctx context.Context, seriesID string, seasonFilter int) (map[int][]introEpisode, int64, int, error) {
	q := `
SELECT f.id, f.abs_path, f.library_id, i.season_number
FROM tv_series_files f
JOIN tv_series_identities i ON i.file_id = f.id
WHERE i.series_id = ? AND i.status = 'matched'`
	args := []any{seriesID}
	if seasonFilter > 0 {
		q += ` AND i.season_number = ?`
		args = append(args, seasonFilter)
	}
	rows, err := h.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	var libraryID int64
	bySeason := map[int][]introEpisode{}
	for rows.Next() {
		var e introEpisode
		var lib int64
		if err := rows.Scan(&e.fileID, &e.absPath, &lib, &e.season); err != nil {
			return nil, 0, 0, err
		}
		if libraryID == 0 {
			libraryID = lib
		}
		if lib == libraryID {
			bySeason[e.season] = append(bySeason[e.season], e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	total := 0
	for _, eps := range bySeason {
		if len(eps) >= 2 {
			total += len(eps)
		}
	}
	return bySeason, libraryID, total, nil
}

// introMarkerKey is the once-per-(series,season) marker that gates lazy auto intro
// detection — a row in tv_series_metadata_cache, like the cast/backdrop markers.
// Written when the job finishes a season, so detection never re-runs it; an
// interrupted job leaves no marker and is retried on the next play.
func introMarkerKey(seriesID string, season int) string {
	return fmt.Sprintf("show:%s:introskip:s%d", seriesID, season)
}

// enqueueIntroDetect background-enqueues the cross-episode intro-detection job for
// a whole series, or one season when seasonFilter > 0 (the lazy on-play path). It
// returns the job id, the number of episodes to fingerprint (0 = nothing to detect,
// no job enqueued), and the library id. dedupeKey, when set, is the in-memory
// metaFetch key cleared when the job ends. Fingerprint rides the shared ffmpeg
// concurrency gate, so detection yields to live transcode/probe work.
func (h *Handler) enqueueIntroDetect(ctx context.Context, seriesID string, seasonFilter int, createdBy, dedupeKey string) (int64, int, int64, error) {
	bySeason, libraryID, total, err := h.gatherIntroSeasons(ctx, seriesID, seasonFilter)
	if err != nil {
		return 0, 0, 0, err
	}
	if libraryID == 0 || total == 0 {
		return 0, 0, libraryID, nil
	}
	jobID, err := h.jobs.Enqueue("intro_detect", libraryID, createdBy, func(ctx context.Context, jID, libID int64) error {
		if dedupeKey != "" {
			defer h.metaFetch.Delete(dedupeKey)
		}
		_, _ = h.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", total, jID)
		done := 0
		for season, eps := range bySeason {
			if len(eps) < 2 {
				continue
			}
			var fps []introskip.Episode
			for _, ep := range eps {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				clean, perr := h.resolveTVPath(ep.absPath)
				if perr != nil {
					done++
					continue
				}
				pts, rate, ferr := video.Fingerprint(ctx, clean, 0, introDetectWindowSec)
				done++
				_, _ = h.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", done, jID)
				if ferr != nil {
					slog.Warn("intro fingerprint", "file_id", ep.fileID, "err", ferr)
					continue
				}
				fps = append(fps, introskip.Episode{FileID: ep.fileID, Points: pts, Rate: rate})
			}
			for fid, s := range introskip.DetectIntros(fps) {
				if _, derr := h.db.ExecContext(ctx, `
INSERT INTO tv_skip_segments(file_id, kind, start_sec, end_sec, source) VALUES(?, 'intro', ?, ?, 'fingerprint')
ON CONFLICT(file_id, kind, source) DO UPDATE SET start_sec=excluded.start_sec, end_sec=excluded.end_sec, detected_at=datetime('now')`,
					fid, s.StartSec, s.EndSec); derr != nil {
					slog.Warn("intro segment upsert", "file_id", fid, "err", derr)
				}
			}
			// Mark this season detected so the lazy on-play path never re-runs it.
			_, _ = h.db.ExecContext(ctx, `
INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', '{}', datetime('now'))
ON CONFLICT(entity_key, lang) DO UPDATE SET fetched_at=excluded.fetched_at`,
				introMarkerKey(seriesID, season))
		}
		return nil
	})
	return jobID, total, libraryID, err
}

// maybeAutoDetectIntros lazily kicks off intro detection for the season of the
// episode being played — once, in the background — so skip segments populate
// without the manual "Detect intros" button. It's gentle on I/O: gated by a
// persistent per-season marker (plus an in-memory dedupe for the in-flight
// window), scoped to the played season only, and a no-op without chromaprint, so
// only the seasons you actually watch get fingerprinted, and only once.
func (h *Handler) maybeAutoDetectIntros(ctx context.Context, fileID int64) {
	if !video.ChromaprintAvailable() {
		return
	}
	var seriesID string
	var season int
	if err := h.db.QueryRowContext(ctx,
		`SELECT series_id, season_number FROM tv_series_identities WHERE file_id = ? AND status = 'matched'`,
		fileID).Scan(&seriesID, &season); err != nil || seriesID == "" {
		return
	}
	if h.metaMarkerExists(ctx, introMarkerKey(seriesID, season)) {
		return
	}
	dedupe := "introdetect:" + introMarkerKey(seriesID, season)
	if _, busy := h.metaFetch.LoadOrStore(dedupe, true); busy {
		return
	}
	if _, total, _, err := h.enqueueIntroDetect(ctx, seriesID, season, "system", dedupe); err != nil || total == 0 {
		h.metaFetch.Delete(dedupe) // nothing enqueued — release the dedupe key
	}
}

// tvSeriesDetectIntros is the manual "Detect intros" button: fingerprints the
// whole series now (the lazy per-season path runs the same job automatically on
// play). On-demand, background, reuses the live-job badge.
func (h *Handler) tvSeriesDetectIntros(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "tvSeriesDetectIntros", "err", err)
		return
	}
	seriesID := strings.TrimSpace(r.FormValue("series_id"))
	if _, e := strconv.Atoi(seriesID); e != nil {
		http.Error(w, "invalid series_id", 400)
		return
	}
	if !video.ChromaprintAvailable() {
		http.Error(w, "intro detection needs an ffmpeg built with chromaprint", http.StatusServiceUnavailable)
		return
	}
	jobID, total, libraryID, err := h.enqueueIntroDetect(r.Context(), seriesID, 0, "user", "")
	if err != nil {
		if errors.Is(err, jobs.ErrQueueFull) {
			httpError(w, http.StatusServiceUnavailable, "service unavailable", "job queue full", "handler", "tvSeriesDetectIntros", "err", err)
			return
		}
		httpError(w, 500, "internal server error", "enqueue intro detect failed", "handler", "tvSeriesDetectIntros", "err", err)
		return
	}
	if total == 0 {
		http.Error(w, "need at least two matched episodes in a season to detect intros", 404)
		return
	}
	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"message": "intro detection queued",
			"data":    map[string]any{"library_id": libraryID, "job_id": jobID},
		})
		return
	}
	http.Redirect(w, r, "/tv/series/"+seriesID, http.StatusSeeOther)
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
		// A season with no poster of its own falls back to the series poster.
		if err != nil || artPath == "" {
			artPath = ""
			err = h.db.QueryRowContext(r.Context(),
				"SELECT art_path FROM tv_series_art WHERE art_type='series_poster' AND tmdb_series_id=?",
				tmdbID,
			).Scan(&artPath)
		}
	} else {
		err = h.db.QueryRowContext(r.Context(),
			"SELECT art_path FROM tv_series_art WHERE art_type=? AND tmdb_series_id=?",
			dbArtType, tmdbID,
		).Scan(&artPath)
	}
	if err != nil || artPath == "" {
		// Season cards show a placeholder image rather than a broken image
		// when neither a season nor a series poster is available.
		if artType == "season" {
			http.Redirect(w, r, "/static/"+tvPosterPlaceholder, http.StatusFound)
			return
		}
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
	w.Header().Set("X-Content-Type-Options", "nosniff") // match the other art handlers
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

	// Capped review backlog (worked top-down + reloaded), not paginated.
	var total int
	_ = h.db.QueryRowContext(r.Context(), `
SELECT COUNT(*) FROM (
  SELECT 1 FROM tv_series_identities i
  JOIN tv_series_files f ON f.id = i.file_id
  WHERE i.status = 'unmatched' AND i.guessed_title != ''
  GROUP BY lower(i.guessed_title)
)`).Scan(&total)

	rows, err := h.db.QueryContext(r.Context(), `
SELECT i.guessed_title, COUNT(*) AS file_count,
       GROUP_CONCAT(DISTINCT i.season_number) AS seasons
FROM tv_series_identities i
JOIN tv_series_files f ON f.id = i.file_id
WHERE i.status = 'unmatched' AND i.guessed_title != ''
GROUP BY lower(i.guessed_title)
ORDER BY lower(i.guessed_title)
LIMIT ?
`, reviewListCap)
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
		"Breadcrumb": []crumb{bcHome, bcTV},
		"Title":      "TV Match Review",
		"Groups":     groups,
		"TotalCount": total,
		"Shown":      len(groups),
		"Capped":     total > len(groups),
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

	tmdbKey := h.effectiveTMDBKey(r.Context())
	if tmdbKey == "" {
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
	matcher := tmdb.NewMatcher(h.db, tmdbKey, h.cfg.DataDir)
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

	tmdbKey := h.effectiveTMDBKey(r.Context())
	if tmdbKey == "" {
		http.Error(w, "TMDB API key not configured", 400)
		return
	}

	matcher := tmdb.NewMatcher(h.db, tmdbKey, h.cfg.DataDir)
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
	var absPath, container, seriesID, epCSV, extraTitle string
	var seasonNum, isExtra int
	var libraryID int64
	err = h.db.QueryRowContext(r.Context(), `
SELECT f.abs_path, f.container, f.is_extra, f.extra_title, f.library_id, i.series_id, i.season_number, i.episode_numbers_csv
FROM tv_series_files f
JOIN tv_series_identities i ON i.file_id = f.id
WHERE f.id = ?
`, fileID).Scan(&absPath, &container, &isExtra, &extraTitle, &libraryID, &seriesID, &seasonNum, &epCSV)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	epName, ownerName := "", ""
	if isExtra == 1 {
		// An extra carries no identity: resolve the owning series from the path —
		// the show folder containing its extras dir, then any matched file under it.
		seriesID, epName, ownerName = h.tvExtraContext(r.Context(), libraryID, absPath, extraTitle)
	}

	// Show/episode names from cache.
	showName, _, _ := h.loadShowMetaSummary(r.Context(), seriesID)
	if showName == "" && ownerName != "" {
		showName = ownerName // unmatched show folder: its name beats "Unknown Series"
	}
	if showName == "" {
		showName = "Unknown Series"
	}

	if isExtra != 1 {
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
	}

	// Saved position.
	var position, duration float64
	var completed int
	_ = h.db.QueryRowContext(r.Context(),
		"SELECT position_seconds, duration_seconds, completed FROM tv_playback_progress WHERE file_id=?",
		fileID,
	).Scan(&position, &duration, &completed)

	// Prev/next navigation. At the season's last episode, Up Next rolls into
	// the next local season's first episode. Extras stand alone — no Up Next.
	var prevFileID, nextFileID int64
	if isExtra != 1 {
		prevFileID, nextFileID = h.findAdjacentEpisode(r.Context(), seriesID, seasonNum, epCSV, fileID)
		if nextFileID == 0 {
			nextFileID = h.nextSeasonFirstEpisode(r.Context(), seriesID, seasonNum)
		}
	}

	h.render(w, "tv_player.html", map[string]any{
		"Title":                fmt.Sprintf("%s — %s", showName, epName),
		"FileID":               fileID,
		"SeriesID":             seriesID,
		"SeasonNum":            seasonNum,
		"ShowName":             showName,
		"EpName":               epName,
		"EpCSV":                epCSV,
		"IsExtra":              isExtra == 1,
		"Position":             position,
		"Duration":             duration,
		"Completed":            completed,
		"PrevFileID":           prevFileID,
		"NextFileID":           nextFileID,
		"Container":            container,
		"OpenSubtitlesEnabled": h.effectiveOpenSubtitlesKey(r.Context()) != "",
	})
}

// tvExtraContext resolves an extra's owning series id (may be "" when the
// containing show was never matched), its display name (the filename-derived
// extra title), and the owning folder's basename as a show-name fallback. The
// owner is the title folder containing the extras dir; the series id comes
// from any matched file under it.
func (h *Handler) tvExtraContext(ctx context.Context, libraryID int64, absPath, extraTitle string) (seriesID, epName, ownerName string) {
	epName = extraTitle
	if epName == "" {
		epName = "Extra"
	}
	root := h.libraryRootPath(ctx, libraryID)
	if root == "" {
		return "", epName, ""
	}
	owner, ok := tvscan.ExtrasOwnerDir(absPath, root)
	if !ok {
		return "", epName, ""
	}
	prefix := owner + string(os.PathSeparator)
	_ = h.db.QueryRowContext(ctx, `
SELECT i.series_id FROM tv_series_files f
JOIN tv_series_identities i ON i.file_id = f.id
WHERE f.library_id = ? AND i.status = 'matched' AND i.series_id != '' AND substr(f.abs_path, 1, ?) = ?
LIMIT 1`, libraryID, len(prefix), prefix).Scan(&seriesID)
	return seriesID, epName, filepath.Base(owner)
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

// epFileEntry is one episode file of a season, for numeric ordering.
type epFileEntry struct {
	id        int64
	epCSV     string
	completed bool
}

// seasonEpisodeFiles returns a season's matched files in numeric episode order
// (episode_numbers_csv is TEXT — lexical order puts "10" before "2"), each with
// its watched flag.
func (h *Handler) seasonEpisodeFiles(ctx context.Context, seriesID string, seasonNum int) []epFileEntry {
	rows, err := h.db.QueryContext(ctx, `
SELECT f.id, i.episode_numbers_csv, COALESCE(p.completed, 0)
FROM tv_series_identities i
JOIN tv_series_files f ON f.id = i.file_id
LEFT JOIN tv_playback_progress p ON p.file_id = f.id
WHERE i.series_id = ? AND i.season_number = ? AND i.status = 'matched'
`, seriesID, seasonNum)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var entries []epFileEntry
	for rows.Next() {
		var e epFileEntry
		var completed int
		if rows.Scan(&e.id, &e.epCSV, &completed) == nil {
			e.completed = completed == 1
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return firstEpNum(entries[i].epCSV) < firstEpNum(entries[j].epCSV)
	})
	return entries
}

func (h *Handler) findAdjacentEpisode(ctx context.Context, seriesID string, seasonNum int, currentEpCSV string, currentFileID int64) (prevID, nextID int64) {
	entries := h.seasonEpisodeFiles(ctx, seriesID, seasonNum)
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

// nextSeasonFirstEpisode returns the first episode file of the next local
// season after seasonNum — the cross-season half of Up Next (0 when this is
// the last season). findAdjacentEpisode covers within-season.
func (h *Handler) nextSeasonFirstEpisode(ctx context.Context, seriesID string, seasonNum int) int64 {
	var next sql.NullInt64
	if err := h.db.QueryRowContext(ctx, `
SELECT MIN(season_number) FROM tv_series_identities
WHERE series_id = ? AND status = 'matched' AND season_number > ?`, seriesID, seasonNum).Scan(&next); err != nil || !next.Valid {
		return 0
	}
	if entries := h.seasonEpisodeFiles(ctx, seriesID, int(next.Int64)); len(entries) > 0 {
		return entries[0].id
	}
	return 0
}

// firstUnwatchedInSeason returns the season's first not-completed episode file
// (0 when everything is watched) — the home Continue-Watching play target.
func (h *Handler) firstUnwatchedInSeason(ctx context.Context, seriesID string, seasonNum int) int64 {
	for _, e := range h.seasonEpisodeFiles(ctx, seriesID, seasonNum) {
		if !e.completed {
			return e.id
		}
	}
	return 0
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

package web

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hespera/internal/jobs"
	"hespera/internal/music"
	"hespera/internal/pathguard"
	"hespera/internal/tmdb"
)

// movieRow is one card in the movies browse grid / dashboard carousels.
type movieRow struct {
	TMDBID      int
	FileID      int64 // a representative local file (for the Play deep-link)
	Title       string
	Year        string
	ProgressPct int
	Completed   bool
	// RecencyUnix is the row's sort timestamp (unix seconds): last-watched for the
	// continue-watching query, file mtime for the recently-added query. Lets the
	// home "Continue Watching" row interleave movies with TV by recency.
	RecencyUnix int64
}

// moviesHome renders the movies browse page: a paginated grid of matched films
// plus a "Recent" sub-tab (Continue Watching + Recently Added). Mirrors
// tvSeriesList. Every secondary section is best-effort.
func (h *Handler) moviesHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/movies" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	movies, nav, unmatched, err := h.loadMovieList(r.Context(), pageParam(r))
	if err != nil {
		httpError(w, 500, "internal server error", "load movie list failed", "handler", "moviesHome", "err", err)
		return
	}
	// In-place paging (grid_pager.js) fetches just the movie card grid —
	// short-circuit before the "Recent" sub-tab queries it doesn't use.
	if r.URL.Query().Get("grid") == "1" {
		h.renderFragment(w, "movies_home.html", "movie-cards", movies)
		return
	}

	// "Recent" sub-tab content — non-fatal so the page still renders if these fail.
	continueWatching, err := h.loadMovieContinueWatching(r.Context(), 18)
	if err != nil {
		slog.Warn("load movie continue-watching failed", "handler", "moviesHome", "err", err)
	}
	recentlyAdded, err := h.loadMovieRecentlyAdded(r.Context(), 18)
	if err != nil {
		slog.Warn("load recently-added movies failed", "handler", "moviesHome", "err", err)
	}

	h.render(w, "movies_home.html", map[string]any{
		"Breadcrumb":       []crumb{bcHome},
		"Title":            "Movies",
		"Movies":           movies,
		"MoviesPage":       nav,
		"UnmatchedCount":   unmatched,
		"ContinueWatching": continueWatching,
		"RecentlyAdded":    recentlyAdded,
	})
}

// movieListBase is the FROM clause for the matched-films list: one row per TMDB
// id (deduped across duplicate files via MIN(id)), LEFT JOINed to the metadata
// cache with the title/release-date pulled out of the cached JSON payload as real
// columns (json_extract). Like the TV list, surfacing the title as a column lets
// the sort + pagination run in SQL instead of loading every matched film into
// memory to sort in Go.
const movieListBase = `
FROM (
  SELECT sub.tmdb_id, sub.file_id,
         COALESCE(json_extract(c.payload_json, '$.title'), '') AS title,
         COALESCE(json_extract(c.payload_json, '$.release_date'), '') AS release_date
  FROM (
    SELECT tmdb_id, MIN(id) AS file_id
    FROM movie_files
    WHERE match_status='matched' AND tmdb_id != 0
    GROUP BY tmdb_id
  ) sub
  LEFT JOIN movie_metadata_cache c ON c.entity_key = 'movie:' || sub.tmdb_id
) s`

// loadMovieList returns one page of the matched films, sorted by title in SQL,
// plus the count of unmatched title groups for the "needs matching" banner.
func (h *Handler) loadMovieList(ctx context.Context, page int) ([]movieRow, pageNav, int, error) {
	var total int
	if err := h.db.QueryRowContext(ctx, "SELECT COUNT(*) "+movieListBase).Scan(&total); err != nil {
		return nil, pageNav{}, 0, err
	}
	nav, offset := paginate(page, total, "/movies")

	rows, err := h.db.QueryContext(ctx,
		"SELECT s.tmdb_id, s.file_id, s.title, s.release_date "+movieListBase+
			" ORDER BY lower(s.title), s.tmdb_id LIMIT ? OFFSET ?", listPageSize, offset)
	if err != nil {
		return nil, pageNav{}, 0, err
	}
	defer rows.Close()

	out := make([]movieRow, 0, listPageSize)
	for rows.Next() {
		var m movieRow
		var releaseDate string
		if err := rows.Scan(&m.TMDBID, &m.FileID, &m.Title, &releaseDate); err != nil {
			return nil, pageNav{}, 0, err
		}
		m.Year = movieYear(releaseDate)
		if m.Title == "" {
			m.Title = fmt.Sprintf("Unknown Movie (TMDB %d)", m.TMDBID)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, pageNav{}, 0, err
	}

	var unmatched int
	_ = h.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM (
  SELECT 1 FROM movie_files
  WHERE match_status IN ('', 'unmatched') AND guessed_title != ''
  GROUP BY lower(guessed_title), year
)`).Scan(&unmatched)

	return out, nav, unmatched, nil
}

type movieMetaSummary struct {
	title string
	year  string
}

// loadMovieMetaSummaries fetches titles/years for many movies in one query,
// keyed by TMDB id (avoids an N+1 over movie_metadata_cache).
func (h *Handler) loadMovieMetaSummaries(ctx context.Context, tmdbIDs []int) map[int]movieMetaSummary {
	out := make(map[int]movieMetaSummary, len(tmdbIDs))
	if len(tmdbIDs) == 0 {
		return out
	}
	placeholders := make([]string, len(tmdbIDs))
	args := make([]any, len(tmdbIDs))
	keyToID := make(map[string]int, len(tmdbIDs))
	for i, id := range tmdbIDs {
		key := fmt.Sprintf("movie:%d", id)
		placeholders[i] = "?"
		args[i] = key
		keyToID[key] = id
	}
	query := "SELECT entity_key, payload_json FROM movie_metadata_cache WHERE entity_key IN (" +
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
		var movie tmdb.Movie
		if json.Unmarshal([]byte(payload), &movie) != nil {
			continue
		}
		out[keyToID[entityKey]] = movieMetaSummary{title: movie.Title, year: movieYear(movie.ReleaseDate)}
	}
	return out
}

func movieYear(releaseDate string) string {
	if len(releaseDate) >= 4 {
		return releaseDate[:4]
	}
	return ""
}

// loadMovieContinueWatching returns in-progress (not completed) films, newest
// activity first — one card per film. A film with two in-progress files (two
// editions/downloads) is deduped to its most-recently-watched file via the
// latest CTE + join-back (the TV recently-watched pattern): a bare GROUP BY
// tmdb_id would pair the MAX(updated_at) with an arbitrary row's file_id and
// progress, not the most-recent one's.
func (h *Handler) loadMovieContinueWatching(ctx context.Context, limit int) ([]movieRow, error) {
	rows, err := h.db.QueryContext(ctx, `
WITH latest AS (
  SELECT f.tmdb_id AS tid, MAX(p.updated_at) AS last_watched
  FROM movie_playback_progress p
  JOIN movie_files f ON f.id = p.file_id
  WHERE f.match_status='matched' AND f.tmdb_id != 0 AND COALESCE(p.completed,0)=0
  GROUP BY f.tmdb_id
)
SELECT f.id, f.tmdb_id,
       CASE WHEN p.duration_seconds > 0 THEN CAST(p.position_seconds*100/p.duration_seconds AS INTEGER) ELSE 0 END,
       CAST(strftime('%s', p.updated_at) AS INTEGER)
FROM latest l
JOIN movie_files f ON f.tmdb_id = l.tid
JOIN movie_playback_progress p ON p.file_id = f.id AND p.updated_at = l.last_watched
WHERE f.match_status='matched' AND COALESCE(p.completed,0)=0
GROUP BY f.tmdb_id
ORDER BY p.updated_at DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	return h.scanMovieCards(ctx, rows)
}

// loadMovieRecentlyAdded returns matched films by newest file mtime (there's no
// created_at on movie_files; mtime tracks when a download landed).
func (h *Handler) loadMovieRecentlyAdded(ctx context.Context, limit int) ([]movieRow, error) {
	rows, err := h.db.QueryContext(ctx, `
SELECT MIN(f.id), f.tmdb_id, 0, MAX(f.mtime_unix)
FROM movie_files f
WHERE f.match_status='matched' AND f.tmdb_id != 0
GROUP BY f.tmdb_id
ORDER BY MAX(f.mtime_unix) DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	return h.scanMovieCards(ctx, rows)
}

// scanMovieCards reads (file_id, tmdb_id, progress_pct, recency_unix) rows and
// fills titles from the metadata cache.
func (h *Handler) scanMovieCards(ctx context.Context, rows *sql.Rows) ([]movieRow, error) {
	defer rows.Close()
	var out []movieRow
	var ids []int
	for rows.Next() {
		var m movieRow
		if err := rows.Scan(&m.FileID, &m.TMDBID, &m.ProgressPct, &m.RecencyUnix); err != nil {
			return nil, err
		}
		out = append(out, m)
		ids = append(ids, m.TMDBID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	metas := h.loadMovieMetaSummaries(ctx, ids)
	for i := range out {
		meta := metas[out[i].TMDBID]
		if meta.title == "" {
			meta.title = fmt.Sprintf("Unknown Movie (TMDB %d)", out[i].TMDBID)
		}
		out[i].Title = meta.title
		out[i].Year = meta.year
	}
	return out, nil
}

// movieDetail renders one film's page: backdrop, overview, a Play button (to the
// shared player), and the cast strip. Mirrors tvSeriesDetail, flattened.
func (h *Handler) movieDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	idStr := pathSegment(r, "/movie/")
	tmdbID, err := strconv.Atoi(idStr)
	if err != nil || tmdbID <= 0 {
		http.NotFound(w, r)
		return
	}

	// A representative local file for the Play link (+ resume), plus the parsed
	// title/year as a fallback when the TMDB metadata isn't cached yet.
	var fileID int64
	var guessedTitle string
	var fileYear int
	_ = h.db.QueryRowContext(r.Context(),
		"SELECT id, guessed_title, year FROM movie_files WHERE tmdb_id=? AND match_status='matched' ORDER BY id LIMIT 1",
		tmdbID,
	).Scan(&fileID, &guessedTitle, &fileYear)

	var movie tmdb.Movie
	var payload string
	if metaErr := h.db.QueryRowContext(r.Context(),
		"SELECT payload_json FROM movie_metadata_cache WHERE entity_key=?",
		fmt.Sprintf("movie:%d", tmdbID),
	).Scan(&payload); metaErr == nil {
		if err := json.Unmarshal([]byte(payload), &movie); err != nil {
			http.Error(w, "corrupt metadata", 500)
			return
		}
	} else if fileID == 0 {
		// No metadata cached and no matched file → genuinely unknown film.
		http.NotFound(w, r)
		return
	} else {
		// Matched film whose metadata fetch is still running (just approved) or
		// failed: render from the local file so the page works (Play + the lazy
		// cast backfill below) instead of 404ing — a reload shows the full data.
		movie.Title = guessedTitle
		if movie.Title == "" {
			movie.Title = fmt.Sprintf("Movie (TMDB %d)", tmdbID)
		}
	}

	year := movieYear(movie.ReleaseDate)
	if year == "" && fileYear > 0 {
		year = strconv.Itoa(fileYear)
	}

	var resumePct int
	var completed bool
	if fileID > 0 {
		if pos, dur, done := h.loadMovieProgress(r.Context(), fileID); dur > 0 {
			resumePct = int(pos * 100 / dur)
			completed = done
		}
	}

	var backdropVer string
	var fa string
	if h.db.QueryRowContext(r.Context(),
		"SELECT fetched_at FROM movie_art WHERE art_type='backdrop' AND tmdb_movie_id=?",
		tmdbID).Scan(&fa) == nil {
		backdropVer = artVersion(fa)
	}

	// Integrity badge: the page represents the title, which may be backed by
	// several file copies — flag it when ANY matched copy carries unrepairable
	// corruption, with the first copy's reason as the tooltip.
	var flaggedCount int
	var flagDetail string
	_ = h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*), COALESCE(MIN(integrity_detail),'') FROM movie_files WHERE tmdb_id=? AND match_status='matched' AND integrity_status='flagged'",
		tmdbID,
	).Scan(&flaggedCount, &flagDetail)

	// Lazy cast backfill: a film matched before cast-fetch existed (or whose
	// match-time fetch failed) gets its cast on first view, gated by the
	// movie:%d:cast marker so it enqueues at most once. Mirrors tvSeriesDetail.
	cast := h.loadMovieCast(r.Context(), tmdbID)
	if !h.movieCastFetched(r.Context(), tmdbID) {
		id := tmdbID
		h.enqueueMovieMetaFetch(r.Context(), fmt.Sprintf("movie-cast:%d", id), "movie_cast_fetch",
			func(ctx context.Context, m *tmdb.Matcher) error { return m.FetchMovieCast(ctx, id) })
	}

	h.render(w, "movie_detail.html", map[string]any{
		"Breadcrumb":  []crumb{bcHome, bcMovies},
		"Title":       movie.Title,
		"TMDBID":      tmdbID,
		"MovieTitle":  movie.Title,
		"Year":        year,
		"Overview":    movie.Overview,
		"Genres":      movie.Genres,
		"Runtime":     movie.Runtime,
		"Tagline":     movie.Tagline,
		"HasBackdrop": movie.BackdropPath != "",
		"BackdropVer": backdropVer,
		"FileID":      fileID,
		"ResumePct":   resumePct,
		"Completed":   completed,
		"Flagged":     flaggedCount > 0,
		"FlagDetail":  flagDetail,
		"Cast":        cast,
	})
}

// loadMovieCast returns a matched film's cached cast (top-billed first). Mirrors
// loadSeriesCast with media_type='movie'.
func (h *Handler) loadMovieCast(ctx context.Context, tmdbID int) []castMemberRow {
	rows, err := h.db.QueryContext(ctx, `
SELECT p.tmdb_id, p.name, c.character_name, (p.art_path != '')
FROM credits c
JOIN people p ON p.tmdb_id = c.person_id
WHERE c.media_type='movie' AND c.media_id=?
ORDER BY c.billing_order
LIMIT 20
`, tmdbID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []castMemberRow
	for rows.Next() {
		var cm castMemberRow
		var hasArt int
		if err := rows.Scan(&cm.PersonID, &cm.Name, &cm.Character, &hasArt); err != nil {
			return out
		}
		cm.HasArt = hasArt != 0
		out = append(out, cm)
	}
	return out
}

// movieArt serves a film's poster or backdrop from movie_art. Mirrors tvArt
// (no season tier). A missing poster falls back to the shared placeholder so
// grid cards are never broken images.
func (h *Handler) movieArt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// Routes: /art/movie/poster/{tmdbID}, /art/movie/backdrop/{tmdbID}
	rest := strings.TrimPrefix(r.URL.Path, "/art/movie/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	artType := parts[0]
	if artType != "poster" && artType != "backdrop" {
		http.NotFound(w, r)
		return
	}
	tmdbID, err := strconv.Atoi(parts[1])
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var artPath string
	err = h.db.QueryRowContext(r.Context(),
		"SELECT art_path FROM movie_art WHERE art_type=? AND tmdb_movie_id=?",
		artType, tmdbID,
	).Scan(&artPath)
	if err != nil || artPath == "" {
		if artType == "poster" {
			http.Redirect(w, r, "/static/"+tvPosterPlaceholder, http.StatusFound)
			return
		}
		http.NotFound(w, r)
		return
	}

	clean, err := pathguard.ResolveExistingUnderRoot(filepath.Clean(h.cfg.DataDir), artPath)
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
	w.Header().Set("Content-Type", artMIMEFromExt(clean))
	w.Header().Set("X-Content-Type-Options", "nosniff") // manual uploads serve user bytes
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(w, f)
}

// moviesMatch enqueues a movie_match job for a library. Mirrors tvMatch.
func (h *Handler) moviesMatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "moviesMatch", "err", err)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", 400)
		return
	}
	tmdbKey := h.effectiveTMDBKey(r.Context())
	if tmdbKey == "" {
		http.Error(w, "TMDB API key not configured", 400)
		return
	}
	matcher := tmdb.NewMovieMatcher(h.db, tmdbKey, h.cfg.DataDir)
	jobID, err := h.jobs.Enqueue("movie_match", id, "user", func(ctx context.Context, jobID, libraryID int64) error {
		return matcher.RunMovieMatch(ctx, jobID, libraryID)
	})
	if err != nil {
		if errors.Is(err, jobs.ErrQueueFull) {
			httpError(w, http.StatusServiceUnavailable, "service unavailable", "job queue full", "handler", "moviesMatch", "err", err)
			return
		}
		httpError(w, 500, "internal server error", "enqueue movie match failed", "handler", "moviesMatch", "err", err)
		return
	}
	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"message": "movie match queued",
			"data":    map[string]any{"library_id": id, "job_id": jobID},
		})
		return
	}
	http.Redirect(w, r, "/libraries", http.StatusSeeOther)
}

// movieMatchGroup is one unmatched-film group on the review page.
type movieMatchGroup struct {
	GuessedTitle string
	Year         int
	FileCount    int
}

// movieMatchReview lists unmatched films grouped by (title, year) for manual
// TMDB assignment. Cap-with-count like the TV review (worked top-down, reloaded).
func (h *Handler) movieMatchReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var total int
	_ = h.db.QueryRowContext(r.Context(), `
SELECT COUNT(*) FROM (
  SELECT 1 FROM movie_files
  WHERE match_status IN ('', 'unmatched') AND guessed_title != ''
  GROUP BY lower(guessed_title), year
)`).Scan(&total)

	rows, err := h.db.QueryContext(r.Context(), `
SELECT guessed_title, year, COUNT(*)
FROM movie_files
WHERE match_status IN ('', 'unmatched') AND guessed_title != ''
GROUP BY lower(guessed_title), year
ORDER BY guessed_title
LIMIT ?
`, reviewListCap)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "movieMatchReview", "err", err)
		return
	}
	defer rows.Close()
	var groups []movieMatchGroup
	for rows.Next() {
		var g movieMatchGroup
		if err := rows.Scan(&g.GuessedTitle, &g.Year, &g.FileCount); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "movieMatchReview", "err", err)
			return
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "movieMatchReview", "err", err)
		return
	}

	h.render(w, "movie_match_review.html", map[string]any{
		"Breadcrumb": []crumb{bcHome, bcMovies},
		"Title":      "Movie Match Review",
		"Groups":     groups,
		"TotalCount": total,
		"Shown":      len(groups),
		"Capped":     total > len(groups),
	})
}

// moviesMatchSearch proxies a TMDB movie search for the review page's typeahead.
func (h *Handler) moviesMatchSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
		return
	}
	tmdbKey := h.effectiveTMDBKey(r.Context())
	if tmdbKey == "" {
		jsonError(w, "TMDB API key not configured", http.StatusBadRequest)
		return
	}
	results, err := tmdb.NewMovieMatcher(h.db, tmdbKey, h.cfg.DataDir).SearchMovie(r.Context(), q)
	if err != nil {
		jsonError(w, "search failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}

// moviesMatchApprove assigns a TMDB id to an unmatched (title, year) group: it
// marks the rows matched, then enqueues the metadata/art/cast fetch as a
// background job and redirects immediately. Mirrors tvMatchApprove (async) rather
// than blocking the POST on FetchMovieMetadata's ~15 rate-limited cast-image
// downloads. The metadata fetch uses a movie-configured matcher (art →
// thumbs/movies) via enqueueMovieMetaFetch.
func (h *Handler) moviesMatchApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "moviesMatchApprove", "err", err)
		return
	}
	guessed := strings.TrimSpace(r.FormValue("guessed_title"))
	year, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("year")))
	tmdbID, err := strconv.Atoi(strings.TrimSpace(r.FormValue("tmdb_id")))
	if err != nil || tmdbID <= 0 || guessed == "" {
		http.Error(w, "invalid input", 400)
		return
	}
	if h.effectiveTMDBKey(r.Context()) == "" {
		http.Error(w, "TMDB API key not configured", 400)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(), `
UPDATE movie_files SET
  tmdb_id=?, match_status='matched', match_source='manual', match_confidence=1.0, matched_at=?
WHERE lower(guessed_title)=lower(?) AND year=? AND match_status IN ('', 'unmatched')
`, tmdbID, now, guessed, year); err != nil {
		httpError(w, 500, "internal server error", "db update failed", "handler", "moviesMatchApprove", "err", err)
		return
	}

	// Pull metadata/art/cast off-thread; the detail page fills in once it runs
	// (and movieDetail's lazy backfill covers the cast either way).
	id := tmdbID
	h.enqueueMovieMetaFetch(r.Context(), fmt.Sprintf("movie-meta:%d", id), "movie_metadata_fetch",
		func(ctx context.Context, m *tmdb.Matcher) error { return m.FetchMovieMetadata(ctx, id) })

	http.Redirect(w, r, "/movies/match/review", http.StatusSeeOther)
}

// moviePlayer renders the movie player page (the shared media_player.js drives
// playback). Framing comes from the file's matched movie metadata.
func (h *Handler) moviePlayer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("file")), 10, 64)
	if err != nil || fileID <= 0 {
		http.NotFound(w, r)
		return
	}
	var tmdbID int
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT tmdb_id FROM movie_files WHERE id=?", fileID,
	).Scan(&tmdbID); err != nil {
		http.NotFound(w, r)
		return
	}

	title, year := "", ""
	var payload string
	if tmdbID > 0 && h.db.QueryRowContext(r.Context(),
		"SELECT payload_json FROM movie_metadata_cache WHERE entity_key=?",
		fmt.Sprintf("movie:%d", tmdbID),
	).Scan(&payload) == nil {
		var movie tmdb.Movie
		if json.Unmarshal([]byte(payload), &movie) == nil {
			title, year = movie.Title, movieYear(movie.ReleaseDate)
		}
	}
	if title == "" {
		title = "Movie"
	}

	h.render(w, "movie_player.html", map[string]any{
		"Title":      title,
		"MovieTitle": title,
		"Year":       year,
		"TMDBID":     tmdbID,
		"FileID":     fileID,
	})
}

// moviesMatchSkip marks an unmatched (title, year) group as skipped so it drops
// off the review list and isn't re-attempted by the matcher.
func (h *Handler) moviesMatchSkip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "moviesMatchSkip", "err", err)
		return
	}
	guessed := strings.TrimSpace(r.FormValue("guessed_title"))
	year, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("year")))
	if guessed == "" {
		http.Error(w, "invalid input", 400)
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `
UPDATE movie_files SET match_status='skipped'
WHERE lower(guessed_title)=lower(?) AND year=? AND match_status IN ('', 'unmatched')
`, guessed, year); err != nil {
		httpError(w, 500, "internal server error", "db update failed", "handler", "moviesMatchSkip", "err", err)
		return
	}
	http.Redirect(w, r, "/movies/match/review", http.StatusSeeOther)
}

// movieMarkWatched sets or clears a film's watched flag without playback —
// the movie twin of tvMarkWatched, applied to every matched copy of the
// tmdb_id (the page represents the title, not one file). Redirects back to
// the movie page.
func (h *Handler) movieMarkWatched(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "movieMarkWatched", "err", err)
		return
	}
	tmdbID, err := strconv.Atoi(strings.TrimSpace(r.FormValue("tmdb")))
	if err != nil || tmdbID <= 0 {
		http.Error(w, "invalid tmdb id", 400)
		return
	}
	watched := r.FormValue("watched") == "1"

	rows, err := h.db.QueryContext(r.Context(),
		"SELECT id FROM movie_files WHERE tmdb_id = ? AND match_status = 'matched'", tmdbID)
	if err != nil {
		httpError(w, 500, "internal server error", "db query failed", "handler", "movieMarkWatched", "err", err)
		return
	}
	defer rows.Close()
	var fileIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			httpError(w, 500, "internal server error", "row scan failed", "handler", "movieMarkWatched", "err", err)
			return
		}
		fileIDs = append(fileIDs, id)
	}
	if err := rows.Err(); err != nil {
		httpError(w, 500, "internal server error", "rows iteration failed", "handler", "movieMarkWatched", "err", err)
		return
	}
	if len(fileIDs) == 0 {
		http.NotFound(w, r)
		return
	}

	if err := markWatched(r.Context(), h.db, "movie_playback_progress", fileIDs, watched); err != nil {
		httpError(w, 500, "internal server error", "db upsert failed", "handler", "movieMarkWatched", "err", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/movie/%d", tmdbID), http.StatusSeeOther)
}

// movieUnmatch resets a matched film back to unmatched so it reappears in the
// review list for re-matching — fixing a mis-match without a full library
// re-Match. The music analogue is musicAlbumUnmatch. Keyed by tmdb_id (a film
// may span several files); also drops the now-orphaned movie_art rows so the
// thumbgc movie sweep can reclaim the image files.
func (h *Handler) movieUnmatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "movieUnmatch", "err", err)
		return
	}
	tmdbID, err := strconv.Atoi(strings.TrimSpace(r.FormValue("tmdb_id")))
	if err != nil || tmdbID <= 0 {
		http.Error(w, "invalid tmdb_id", 400)
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `
UPDATE movie_files SET
  tmdb_id=0, match_status='', match_source='', match_confidence=0, matched_at=''
WHERE tmdb_id=? AND match_status='matched'
`, tmdbID); err != nil {
		httpError(w, 500, "internal server error", "db update failed", "handler", "movieUnmatch", "err", err)
		return
	}
	// No movie_files row references this TMDB id anymore; drop its art so thumbgc
	// can reclaim the files (best-effort — a stale row would only leak disk).
	_, _ = h.db.ExecContext(r.Context(), "DELETE FROM movie_art WHERE tmdb_movie_id=?", tmdbID)
	http.Redirect(w, r, "/movies/match/review", http.StatusSeeOther)
}

// movieArtUpload stores a user-supplied poster or backdrop for a matched movie,
// marking the movie_art row manual=1 so a later (re)match's downloadMovieArt
// skips it. Mirrors musicAlbumArtUpload (bytes-sniffed MIME, 15 MiB cap,
// temp+rename); mounted under /movie/ so the auth + same-origin CSRF middleware
// applies (an /art/* path would not CSRF-guard the POST).
func (h *Handler) movieArtUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAlbumArtBytes+(1<<20))
	if err := r.ParseMultipartForm(maxAlbumArtBytes); err != nil {
		http.Error(w, "upload too large or malformed", http.StatusBadRequest)
		return
	}
	tmdbID, err := strconv.Atoi(strings.TrimSpace(r.FormValue("tmdb_id")))
	if err != nil || tmdbID <= 0 {
		http.Error(w, "invalid tmdb_id", http.StatusBadRequest)
		return
	}
	artType := strings.TrimSpace(r.FormValue("art_type"))
	if artType != "poster" && artType != "backdrop" {
		http.Error(w, "art_type must be poster or backdrop", http.StatusBadRequest)
		return
	}
	// Only matched movies have a detail page / TMDB id to key art on.
	var exists int
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT 1 FROM movie_files WHERE tmdb_id=? AND match_status='matched' LIMIT 1", tmdbID).Scan(&exists); err != nil {
		http.NotFound(w, r)
		return
	}

	file, _, err := r.FormFile("art")
	if err != nil {
		http.Error(w, "no image file provided", http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAlbumArtBytes))
	if err != nil {
		httpError(w, 500, "internal server error", "read upload failed", "handler", "movieArtUpload", "err", err)
		return
	}
	// MIME from the bytes — never the client content-type/filename; jpeg/png/webp.
	detected := http.DetectContentType(data)
	if err := music.VerifyImage(detected, data); err != nil {
		http.Error(w, "file is not a valid image", http.StatusBadRequest)
		return
	}
	ext, err := music.ArtFileExt(detected)
	if err != nil {
		http.Error(w, "unsupported image format (use JPEG, PNG, or WebP)", http.StatusBadRequest)
		return
	}

	thumbDir := filepath.Join(h.cfg.DataDir, "thumbs", "movies")
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		httpError(w, 500, "internal server error", "mkdir thumbs failed", "handler", "movieArtUpload", "err", err)
		return
	}
	// Stable per-(movie,type) name with a distinct prefix so it never collides
	// with the TMDB download file (movie_<id>_<type>.jpg).
	sum := sha1.Sum([]byte(fmt.Sprintf("manual-movie-%d-%s", tmdbID, artType)))
	outPath := filepath.Join(thumbDir, hex.EncodeToString(sum[:])+ext)

	tmp, err := os.CreateTemp(thumbDir, "art-*")
	if err != nil {
		httpError(w, 500, "internal server error", "create temp failed", "handler", "movieArtUpload", "err", err)
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		httpError(w, 500, "internal server error", "write art failed", "handler", "movieArtUpload", "err", err)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		httpError(w, 500, "internal server error", "close art failed", "handler", "movieArtUpload", "err", err)
		return
	}
	if err := os.Rename(tmpName, outPath); err != nil {
		_ = os.Remove(tmpName)
		httpError(w, 500, "internal server error", "publish art failed", "handler", "movieArtUpload", "err", err)
		return
	}

	// Capture the prior art file (if any) so a re-upload that changes the
	// extension — png→jpg lands at a different filename — doesn't orphan it on
	// disk. The match-time thumbgc sweep is the backstop, but it may not run for a
	// long time on a stable library, so clean up the superseded file right here.
	var oldPath string
	_ = h.db.QueryRowContext(r.Context(),
		"SELECT art_path FROM movie_art WHERE tmdb_movie_id=? AND art_type=?", tmdbID, artType).Scan(&oldPath)

	// manual=1 protects this row from a (re)match's downloadMovieArt gate.
	if _, err := h.db.ExecContext(r.Context(), `
INSERT INTO movie_art (tmdb_movie_id, art_type, art_path, manual)
VALUES (?, ?, ?, 1)
ON CONFLICT(tmdb_movie_id, art_type) DO UPDATE SET
  art_path=excluded.art_path, manual=1, fetched_at=datetime('now')
`, tmdbID, artType, outPath); err != nil {
		httpError(w, 500, "internal server error", "upsert movie_art failed", "handler", "movieArtUpload", "err", err)
		return
	}
	if oldPath != "" && filepath.Clean(oldPath) != outPath {
		if clean, perr := pathguard.ResolveExistingUnderRoot(filepath.Clean(h.cfg.DataDir), oldPath); perr == nil {
			_ = os.Remove(clean)
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/movie/%d", tmdbID), http.StatusSeeOther)
}

// movieArtClear removes a manual art override for one slot (poster/backdrop) and
// re-fetches that film's TMDB art in the background — a targeted revert that,
// unlike Unmatch, keeps the match (and any other manual art slot) intact.
func (h *Handler) movieArtClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, 400, "bad request", "parse form failed", "handler", "movieArtClear", "err", err)
		return
	}
	tmdbID, err := strconv.Atoi(strings.TrimSpace(r.FormValue("tmdb_id")))
	if err != nil || tmdbID <= 0 {
		http.Error(w, "invalid tmdb_id", http.StatusBadRequest)
		return
	}
	artType := strings.TrimSpace(r.FormValue("art_type"))
	if artType != "poster" && artType != "backdrop" {
		http.Error(w, "art_type must be poster or backdrop", http.StatusBadRequest)
		return
	}
	var artPath string
	_ = h.db.QueryRowContext(r.Context(),
		"SELECT art_path FROM movie_art WHERE tmdb_movie_id=? AND art_type=? AND manual=1", tmdbID, artType).Scan(&artPath)
	if _, err := h.db.ExecContext(r.Context(),
		"DELETE FROM movie_art WHERE tmdb_movie_id=? AND art_type=? AND manual=1", tmdbID, artType); err != nil {
		httpError(w, 500, "internal server error", "delete movie_art failed", "handler", "movieArtClear", "err", err)
		return
	}
	if artPath != "" {
		if clean, perr := pathguard.ResolveExistingUnderRoot(filepath.Clean(h.cfg.DataDir), artPath); perr == nil {
			_ = os.Remove(clean)
		}
	}
	// Re-pull TMDB art (the gate no longer fires now the manual row is gone).
	id := tmdbID
	h.enqueueMovieMetaFetch(r.Context(), fmt.Sprintf("movie-meta:%d", id), "movie_metadata_fetch",
		func(ctx context.Context, m *tmdb.Matcher) error { return m.FetchMovieMetadata(ctx, id) })
	http.Redirect(w, r, fmt.Sprintf("/movie/%d", id), http.StatusSeeOther)
}

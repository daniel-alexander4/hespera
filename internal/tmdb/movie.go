package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"hespera/internal/match"
	"hespera/internal/thumbgc"
)

// movieMatchThreshold is the minimum combined (title-similarity + year) score for
// a movie match to be accepted — same bar as the TV pipeline.
const movieMatchThreshold = 0.80

// MovieSearchResult is one hit from /search/movie.
type MovieSearchResult struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	ReleaseDate  string  `json:"release_date"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	Popularity   float64 `json:"popularity"`
}

// Movie is the subset of /movie/{id} cached for the detail page.
type Movie struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	Overview     string  `json:"overview"`
	ReleaseDate  string  `json:"release_date"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	Runtime      int     `json:"runtime"`
	Genres       []Genre `json:"genres"`
	Tagline      string  `json:"tagline"`
	VoteAverage  float64 `json:"vote_average"`
	// BelongsToCollection is TMDB's franchise grouping (nil for standalone
	// films). Blobs cached before this field existed unmarshal with nil; the
	// lazy movie_collection_fetch job re-fetches details to backfill them.
	BelongsToCollection *CollectionRef `json:"belongs_to_collection"`
}

// MovieCastMember is one entry from /movie/{id}/credits. Unlike TV's
// aggregate_credits, a movie credit carries a single flat character string.
type MovieCastMember struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	ProfilePath string `json:"profile_path"`
	Character   string `json:"character"`
	Order       int    `json:"order"`
}

// SearchMovie searches TMDB by title only; year disambiguation happens in
// pickBestMovie so an off-by-one year in a filename doesn't drop the match.
func (c *Client) SearchMovie(ctx context.Context, query string) ([]MovieSearchResult, error) {
	<-c.limiter

	u := fmt.Sprintf("%s/search/movie?api_key=%s&query=%s&language=en-US&page=1",
		c.apiBase, url.QueryEscape(c.apiKey), url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("tmdb movie search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tmdb movie search: status %d", resp.StatusCode)
	}

	var result struct {
		Results []MovieSearchResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("tmdb movie search decode: %w", err)
	}
	return result.Results, nil
}

// FetchMovie returns full details for one movie from /movie/{id}.
func (c *Client) FetchMovie(ctx context.Context, movieID int) (*Movie, error) {
	<-c.limiter

	u := fmt.Sprintf("%s/movie/%d?api_key=%s&language=en-US",
		c.apiBase, movieID, url.QueryEscape(c.apiKey))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("tmdb movie: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tmdb movie %d: status %d", movieID, resp.StatusCode)
	}

	var movie Movie
	if err := json.NewDecoder(resp.Body).Decode(&movie); err != nil {
		return nil, fmt.Errorf("tmdb movie decode: %w", err)
	}
	return &movie, nil
}

// FetchMovieCredits returns a movie's billed cast from /movie/{id}/credits,
// ordered by billing.
func (c *Client) FetchMovieCredits(ctx context.Context, movieID int) ([]MovieCastMember, error) {
	<-c.limiter

	u := fmt.Sprintf("%s/movie/%d/credits?api_key=%s&language=en-US",
		c.apiBase, movieID, url.QueryEscape(c.apiKey))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("tmdb movie credits: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tmdb movie credits %d: status %d", movieID, resp.StatusCode)
	}

	var out struct {
		Cast []MovieCastMember `json:"cast"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("tmdb movie credits decode: %w", err)
	}
	return out.Cast, nil
}

// RunMovieMatch resolves a movies library's unmatched files against TMDB,
// caching metadata, downloading poster+backdrop, fetching cast, and writing the
// inline match identity onto movie_files. Mirrors RunTVMatch; the per-file match
// state is single-table (no separate identities table).
func (m *Matcher) RunMovieMatch(ctx context.Context, jobID, libraryID int64) error {
	if err := os.MkdirAll(m.artDir, 0o755); err != nil {
		return err
	}

	rows, err := m.db.QueryContext(ctx, `
SELECT id, guessed_title, year
FROM movie_files
WHERE library_id = ?
  AND match_status IN ('', 'unmatched')
  AND guessed_title != ''
`, libraryID)
	if err != nil {
		return fmt.Errorf("query movie files: %w", err)
	}
	defer rows.Close()

	// Group files sharing the same (title, year) so duplicate copies of one film
	// cost a single TMDB lookup.
	type group struct {
		title   string
		year    int
		fileIDs []int64
	}
	groupsByKey := make(map[string]*group)
	var order []string
	for rows.Next() {
		var fileID int64
		var title string
		var year int
		if err := rows.Scan(&fileID, &title, &year); err != nil {
			return err
		}
		key := strings.ToLower(strings.TrimSpace(title)) + "|" + strconv.Itoa(year)
		g, ok := groupsByKey[key]
		if !ok {
			g = &group{title: title, year: year}
			groupsByKey[key] = g
			order = append(order, key)
		}
		g.fileIDs = append(g.fileIDs, fileID)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(order) > 0 {
		_, _ = m.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", len(order), jobID)
	}

	for gi, key := range order {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		g := groupsByKey[key]

		slog.Info("tmdb movie match", "title", g.title, "year", g.year, "files", len(g.fileIDs))

		results, err := m.client.SearchMovie(ctx, g.title)
		if err != nil {
			slog.Warn("tmdb movie search failed", "title", g.title, "err", err)
			continue
		}
		best, score := pickBestMovie(results, g.title, g.year)
		if best == nil || score < movieMatchThreshold {
			slog.Info("tmdb movie no match", "title", g.title, "year", g.year, "best_score", score)
			continue
		}
		movieID := best.ID

		movie, err := m.client.FetchMovie(ctx, movieID)
		if err != nil {
			slog.Warn("tmdb fetch movie failed", "id", movieID, "err", err)
			continue
		}

		// Cache metadata (movie_metadata_cache has a single-column PK).
		movieJSON, _ := json.Marshal(movie)
		_, _ = m.db.ExecContext(ctx, `
INSERT INTO movie_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', ?, datetime('now'))
ON CONFLICT(entity_key) DO UPDATE SET
  payload_json=excluded.payload_json,
  fetched_at=excluded.fetched_at,
  updated_at=datetime('now')
`, fmt.Sprintf("movie:%d", movieID), string(movieJSON))

		m.downloadMovieArt(ctx, movieID, "poster", movie.PosterPath)
		m.downloadMovieArt(ctx, movieID, "backdrop", movie.BackdropPath)

		if err := m.FetchMovieCast(ctx, movieID); err != nil {
			slog.Warn("tmdb movie cast fetch", "movie", movieID, "err", err)
		}

		now := time.Now().UTC().Format(time.RFC3339)
		for _, fileID := range g.fileIDs {
			_, _ = m.db.ExecContext(ctx, `
UPDATE movie_files SET
  tmdb_id=?,
  match_status='matched',
  match_confidence=?,
  match_source='tmdb',
  matched_at=?
WHERE id=?
`, movieID, score, now, fileID)
		}

		_, _ = m.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", gi+1, jobID)
	}

	// Sweep orphaned movie thumbnails (non-fatal). movie_art is the live reference
	// set; person images live in thumbs/tv (swept by the TV pass), not here.
	if n, err := thumbgc.Sweep(ctx, m.db, m.artDir, thumbgc.Grace,
		"SELECT art_path FROM movie_art WHERE art_path != ''",
	); err != nil {
		slog.Warn("thumb gc movies", "err", err)
	} else if n > 0 {
		slog.Info("thumb gc movies", "deleted", n)
	}

	return nil
}

// FetchMovieMetadata fetches one movie's full details, caches metadata, and
// downloads art — used when a user manually assigns a TMDB id to a movie. Does
// not write the match identity (the caller does that).
func (m *Matcher) FetchMovieMetadata(ctx context.Context, movieID int) error {
	if err := os.MkdirAll(m.artDir, 0o755); err != nil {
		return err
	}
	movie, err := m.client.FetchMovie(ctx, movieID)
	if err != nil {
		return fmt.Errorf("fetch movie %d: %w", movieID, err)
	}
	movieJSON, _ := json.Marshal(movie)
	_, _ = m.db.ExecContext(ctx, `
INSERT INTO movie_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', ?, datetime('now'))
ON CONFLICT(entity_key) DO UPDATE SET
  payload_json=excluded.payload_json,
  fetched_at=excluded.fetched_at,
  updated_at=datetime('now')
`, fmt.Sprintf("movie:%d", movieID), string(movieJSON))

	m.downloadMovieArt(ctx, movieID, "poster", movie.PosterPath)
	m.downloadMovieArt(ctx, movieID, "backdrop", movie.BackdropPath)

	if err := m.FetchMovieCast(ctx, movieID); err != nil {
		slog.Warn("tmdb movie cast fetch", "movie", movieID, "err", err)
	}
	return nil
}

// downloadMovieArt downloads a poster (w500) or backdrop (w1280) for a movie and
// upserts the movie_art row. art_type is "poster" or "backdrop".
func (m *Matcher) downloadMovieArt(ctx context.Context, movieID int, artType, tmdbPath string) {
	if tmdbPath == "" {
		return
	}
	// A manual upload owns this art — never overwrite it on (re)match. TMDB art
	// (manual=0 / no row) stays refreshable so Unmatch→re-approve still fixes a
	// wrong poster.
	var manual int
	_ = m.db.QueryRowContext(ctx,
		"SELECT manual FROM movie_art WHERE tmdb_movie_id=? AND art_type=?", movieID, artType).Scan(&manual)
	if manual == 1 {
		return
	}
	dest := filepath.Join(m.artDir, fmt.Sprintf("movie_%d_%s.jpg", movieID, artType))
	var err error
	if artType == "backdrop" {
		err = m.client.DownloadBackdrop(ctx, tmdbPath, dest)
	} else {
		err = m.client.DownloadImage(ctx, tmdbPath, dest)
	}
	if err != nil {
		slog.Warn("tmdb movie art download", "movie", movieID, "type", artType, "err", err)
		return
	}
	_, _ = m.db.ExecContext(ctx, `
INSERT INTO movie_art (tmdb_movie_id, art_type, art_path)
VALUES (?, ?, ?)
ON CONFLICT(tmdb_movie_id, art_type) DO UPDATE SET
  art_path=excluded.art_path, fetched_at=datetime('now')
`, movieID, artType, dest)
}

// FetchMovieCast fetches a movie's top-billed cast and caches it the same way as
// FetchTVCast — people rows + the credits join (media_type='movie') + profile
// images in the shared thumbs/tv dir. Best-effort per person.
func (m *Matcher) FetchMovieCast(ctx context.Context, movieID int) error {
	cast, err := m.client.FetchMovieCredits(ctx, movieID)
	if err != nil {
		return err
	}
	sort.SliceStable(cast, func(i, j int) bool { return cast[i].Order < cast[j].Order })
	if len(cast) > castLimit {
		cast = cast[:castLimit]
	}

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM credits WHERE media_type='movie' AND media_id=?", movieID); err != nil {
		tx.Rollback()
		return err
	}
	for _, cm := range cast {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO people (tmdb_id, name, profile_path) VALUES (?, ?, ?)
ON CONFLICT(tmdb_id) DO UPDATE SET name=excluded.name, profile_path=excluded.profile_path, updated_at=datetime('now')
`, cm.ID, cm.Name, cm.ProfilePath); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO credits (person_id, media_type, media_id, character_name, billing_order)
VALUES (?, 'movie', ?, ?, ?)
`, cm.ID, movieID, cm.Character, cm.Order); err != nil {
			tx.Rollback()
			return err
		}
	}
	// Marker row (empty payload) so the lazy on-view backfill knows the fetch ran
	// even for a film with no cast — mirrors the show:%d:cast marker in FetchTVCast.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO movie_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', '{}', datetime('now'))
ON CONFLICT(entity_key) DO UPDATE SET fetched_at=excluded.fetched_at, updated_at=datetime('now')
`, fmt.Sprintf("movie:%d:cast", movieID)); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	for _, cm := range cast {
		if cm.ProfilePath == "" {
			continue
		}
		var ap string
		_ = m.db.QueryRowContext(ctx, "SELECT art_path FROM people WHERE tmdb_id=?", cm.ID).Scan(&ap)
		if ap != "" {
			continue
		}
		dest := filepath.Join(m.personImageDir(), fmt.Sprintf("person_%d_profile.jpg", cm.ID))
		if err := m.client.DownloadImage(ctx, cm.ProfilePath, dest); err != nil {
			slog.Warn("tmdb movie profile download", "person", cm.ID, "err", err)
			continue
		}
		_, _ = m.db.ExecContext(ctx, "UPDATE people SET art_path=? WHERE tmdb_id=?", dest, cm.ID)
	}
	return nil
}

// SearchMovie proxies a TMDB movie search for the manual match-search endpoint.
func (m *Matcher) SearchMovie(ctx context.Context, query string) ([]MovieSearchResult, error) {
	return m.client.SearchMovie(ctx, query)
}

// pickBestMovie scores candidates by title similarity, adjusted by release-year
// agreement — the critical disambiguator, since films collide on title far more
// than shows. An exact year is a strong bonus; ±1 a mild one; a 2+ year gap a
// penalty (likely the wrong film). When either year is unknown, only a small
// popularity bonus applies.
func pickBestMovie(results []MovieSearchResult, title string, year int) (*MovieSearchResult, float64) {
	if len(results) == 0 {
		return nil, 0
	}
	var best *MovieSearchResult
	var bestScore float64
	for i := range results {
		sim := match.NormalizedSimilarity(results[i].Title, title)

		popBonus := results[i].Popularity / 10000.0
		if popBonus > 0.1 {
			popBonus = 0.1
		}

		score := sim + popBonus
		if ry := releaseYear(results[i].ReleaseDate); year > 0 && ry > 0 {
			switch diff := absInt(ry - year); {
			case diff == 0:
				score += 0.15
			case diff == 1:
				score += 0.05
			default:
				score -= 0.20
			}
		}

		if score > bestScore {
			bestScore = score
			best = &results[i]
		}
	}
	return best, bestScore
}

func releaseYear(releaseDate string) int {
	if len(releaseDate) < 4 {
		return 0
	}
	y, err := strconv.Atoi(releaseDate[:4])
	if err != nil {
		return 0
	}
	return y
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

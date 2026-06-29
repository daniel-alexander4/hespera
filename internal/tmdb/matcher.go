package tmdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hespera/internal/match"
	"hespera/internal/thumbgc"
)

type Matcher struct {
	db     *sql.DB
	client *Client
	artDir string
	// personDir is where actor profile images are written. The people/credits set
	// is global and shared across TV and movies, so person images always live in
	// thumbs/tv regardless of which matcher fetched them — only title posters and
	// backdrops differ per media type (artDir). For the TV matcher the two are the
	// same directory.
	personDir string
}

func NewMatcher(db *sql.DB, apiKey, dataDir string) *Matcher {
	artDir := filepath.Join(dataDir, "thumbs", "tv")
	return &Matcher{
		db:        db,
		client:    NewClient(apiKey),
		artDir:    artDir,
		personDir: artDir,
	}
}

// personImageDir is where actor profile images are written. It falls back to
// artDir when personDir is unset, so a Matcher built as a bare struct literal
// (the test path) still writes into its configured art directory rather than the
// process working directory.
func (m *Matcher) personImageDir() string {
	if m.personDir != "" {
		return m.personDir
	}
	return m.artDir
}

// NewMovieMatcher builds a Matcher whose title art (posters/backdrops) lands in
// thumbs/movies, while shared actor images stay in thumbs/tv. It drives the
// movie match pipeline (movie.go).
func NewMovieMatcher(db *sql.DB, apiKey, dataDir string) *Matcher {
	return &Matcher{
		db:        db,
		client:    NewClient(apiKey),
		artDir:    filepath.Join(dataDir, "thumbs", "movies"),
		personDir: filepath.Join(dataDir, "thumbs", "tv"),
	}
}

func (m *Matcher) RunTVMatch(ctx context.Context, jobID, libraryID int64) error {
	if err := os.MkdirAll(m.artDir, 0o755); err != nil {
		return err
	}

	// Query all unresolved identities for this library.
	rows, err := m.db.QueryContext(ctx, `
SELECT i.file_id, i.guessed_title, i.season_number, i.air_date, i.year
FROM tv_series_identities i
JOIN tv_series_files f ON f.id = i.file_id
WHERE i.status = 'unmatched'
  AND f.library_id = ?
  AND i.guessed_title != ''
`, libraryID)
	if err != nil {
		return fmt.Errorf("query identities: %w", err)
	}
	defer rows.Close()

	type fileEntry struct {
		fileID       int64
		seasonNumber int
		airDate      string // "YYYY-MM-DD" for date-based files; "" otherwise
		year         int    // show release year from the folder (0 = unknown)
	}
	// Group by (normalized title, year) — keying on the year too keeps two eras of
	// a same-named show (e.g. Doctor Who 2005 vs 2023) as distinct groups so each
	// matches its own TMDB series.
	groups := make(map[string][]fileEntry)
	var groupOrder []string
	for rows.Next() {
		var fe fileEntry
		var title string
		if err := rows.Scan(&fe.fileID, &title, &fe.seasonNumber, &fe.airDate, &fe.year); err != nil {
			return err
		}
		key := strings.ToLower(strings.TrimSpace(title)) + "|" + strconv.Itoa(fe.year)
		if _, exists := groups[key]; !exists {
			groupOrder = append(groupOrder, key)
		}
		groups[key] = append(groups[key], fe)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	totalGroups := len(groupOrder)
	if totalGroups > 0 {
		_, _ = m.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", totalGroups, jobID)
	}

	for gi, key := range groupOrder {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		files := groups[key]
		groupYear := files[0].year
		// Use the raw (un-lowered) title from the first file for the search.
		var rawTitle string
		_ = m.db.QueryRowContext(ctx,
			"SELECT guessed_title FROM tv_series_identities WHERE file_id=?",
			files[0].fileID,
		).Scan(&rawTitle)
		if rawTitle == "" {
			rawTitle = strings.SplitN(key, "|", 2)[0] // key is "title|year"
		}

		slog.Info("tmdb match", "title", rawTitle, "year", groupYear, "files", len(files))

		results, err := m.client.SearchTV(ctx, rawTitle)
		if err != nil {
			slog.Warn("tmdb search failed", "title", rawTitle, "err", err)
			continue
		}

		bestResult, bestScore := pickBestResult(results, rawTitle, groupYear)
		if bestResult == nil || bestScore < 0.80 {
			slog.Info("tmdb no match", "title", rawTitle, "best_score", bestScore)
			continue
		}

		showID := bestResult.ID

		// Fetch full show details.
		show, err := m.client.FetchTVShow(ctx, showID)
		if err != nil {
			slog.Warn("tmdb fetch show failed", "id", showID, "err", err)
			continue
		}

		// Cache show metadata.
		showJSON, _ := json.Marshal(show)
		entityKey := fmt.Sprintf("show:%d", showID)
		_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', ?, datetime('now'))
ON CONFLICT(entity_key, lang) DO UPDATE SET
  payload_json=excluded.payload_json,
  fetched_at=excluded.fetched_at,
  updated_at=datetime('now')
`, entityKey, string(showJSON))

		// Download poster.
		if show.PosterPath != "" {
			posterDest := filepath.Join(m.artDir, fmt.Sprintf("show_%d_poster.jpg", showID))
			if err := m.client.DownloadImage(ctx, show.PosterPath, posterDest); err != nil {
				slog.Warn("tmdb poster download", "err", err)
			} else {
				_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_art (art_type, tmdb_series_id, art_path, season_number, episode_number)
VALUES ('series_poster', ?, ?, -1, -1)
ON CONFLICT(art_type, tmdb_series_id, season_number, episode_number) DO UPDATE SET
  art_path=excluded.art_path, fetched_at=datetime('now')
`, showID, posterDest)
			}
		}

		// Download backdrop at the wide banner size + mark hi-res.
		m.downloadAndCacheBackdrop(ctx, showID, show.BackdropPath)

		// Fetch + cache the show's cast (best-effort; powers the cast strip and
		// actor pages). Non-fatal.
		if err := m.FetchTVCast(ctx, showID); err != nil {
			slog.Warn("tmdb cast fetch", "show", showID, "err", err)
		}

		// Collect unique seasons referenced by files. When any file is
		// date-based, widen to every season the show has so we can resolve the
		// air date against the full episode list.
		seasonSet := make(map[int]bool)
		hasAirDate := false
		for _, fe := range files {
			if fe.airDate != "" {
				hasAirDate = true
			}
			if fe.seasonNumber >= 0 {
				seasonSet[fe.seasonNumber] = true
			}
		}
		if hasAirDate {
			for _, s := range show.Seasons {
				seasonSet[s.SeasonNumber] = true
			}
		}

		// Fetch and cache each referenced season, building an air-date index
		// along the way for date-based resolution.
		airIndex := airDateIndex{}
		for sn := range seasonSet {
			season, err := m.client.FetchTVSeason(ctx, showID, sn)
			if err != nil {
				slog.Warn("tmdb fetch season", "show", showID, "season", sn, "err", err)
				continue
			}
			seasonJSON, _ := json.Marshal(season)
			seasonKey := fmt.Sprintf("show:%d:season:%d", showID, sn)
			_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', ?, datetime('now'))
ON CONFLICT(entity_key, lang) DO UPDATE SET
  payload_json=excluded.payload_json,
  fetched_at=excluded.fetched_at,
  updated_at=datetime('now')
`, seasonKey, string(seasonJSON))

			// Download season poster.
			if season.PosterPath != "" {
				spDest := filepath.Join(m.artDir, fmt.Sprintf("show_%d_season_%d_poster.jpg", showID, sn))
				if err := m.client.DownloadImage(ctx, season.PosterPath, spDest); err != nil {
					slog.Warn("tmdb season poster download", "show", showID, "season", sn, "err", err)
				} else {
					_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_art (art_type, tmdb_series_id, season_number, episode_number, art_path)
VALUES ('season_poster', ?, ?, -1, ?)
ON CONFLICT(art_type, tmdb_series_id, season_number, episode_number) DO UPDATE SET
  art_path=excluded.art_path, fetched_at=datetime('now')
`, showID, sn, spDest)
				}
			}

			// Cache episode metadata.
			m.cacheEpisodes(ctx, showID, sn, season.Episodes)
			if hasAirDate {
				airIndex.add(sn, season.Episodes)
			}
		}

		// Update identities for all files in this group. Date-based files become
		// matched only when their air date resolves to exactly one season's
		// episode(s); otherwise they stay unmatched (retriable next run) rather
		// than freezing as a matched-but-episode-less row.
		now := time.Now().UTC().Format(time.RFC3339)
		for _, fe := range files {
			if fe.airDate != "" {
				season, csv, ok := airIndex.resolve(fe.airDate)
				if !ok {
					slog.Info("tmdb airdate unresolved", "title", rawTitle, "date", fe.airDate, "show", showID)
					continue
				}
				_, _ = m.db.ExecContext(ctx, `
UPDATE tv_series_identities SET
  provider='tmdb',
  series_id=?,
  status='matched',
  season_number=?,
  episode_numbers_csv=?,
  match_confidence=?,
  matched_at=?
WHERE file_id=?
`, strconv.Itoa(showID), season, csv, bestScore, now, fe.fileID)
				continue
			}
			_, _ = m.db.ExecContext(ctx, `
UPDATE tv_series_identities SET
  provider='tmdb',
  series_id=?,
  status='matched',
  match_confidence=?,
  matched_at=?
WHERE file_id=?
`, strconv.Itoa(showID), bestScore, now, fe.fileID)
		}

		// Update progress.
		_, _ = m.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", gi+1, jobID)
	}

	// Sweep orphaned TV thumbnails (non-fatal). Runs last, after all art writes
	// are committed; the single-worker job queue serializes this against every
	// other art writer.
	if n, err := thumbgc.Sweep(ctx, m.db, m.artDir, thumbgc.Grace,
		"SELECT art_path FROM tv_series_art WHERE art_path != ''",
		"SELECT art_path FROM people WHERE art_path != ''",
	); err != nil {
		slog.Warn("thumb gc tv", "err", err)
	} else if n > 0 {
		slog.Info("thumb gc tv", "deleted", n)
	}

	return nil
}

// FetchShowMetadata fetches full show details, caches metadata, and downloads art for a given TMDB show ID.
// Used by the approve handler when a user manually assigns a TMDB ID.
func (m *Matcher) FetchShowMetadata(ctx context.Context, showID int) error {
	if err := os.MkdirAll(m.artDir, 0o755); err != nil {
		return err
	}

	show, err := m.client.FetchTVShow(ctx, showID)
	if err != nil {
		return fmt.Errorf("fetch show %d: %w", showID, err)
	}

	// Cache show metadata.
	showJSON, _ := json.Marshal(show)
	entityKey := fmt.Sprintf("show:%d", showID)
	_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', ?, datetime('now'))
ON CONFLICT(entity_key, lang) DO UPDATE SET
  payload_json=excluded.payload_json,
  fetched_at=excluded.fetched_at,
  updated_at=datetime('now')
`, entityKey, string(showJSON))

	// Download poster.
	if show.PosterPath != "" {
		posterDest := filepath.Join(m.artDir, fmt.Sprintf("show_%d_poster.jpg", showID))
		if err := m.client.DownloadImage(ctx, show.PosterPath, posterDest); err != nil {
			slog.Warn("tmdb poster download", "err", err)
		} else {
			_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_art (art_type, tmdb_series_id, art_path, season_number, episode_number)
VALUES ('series_poster', ?, ?, -1, -1)
ON CONFLICT(art_type, tmdb_series_id, season_number, episode_number) DO UPDATE SET
  art_path=excluded.art_path, fetched_at=datetime('now')
`, showID, posterDest)
		}
	}

	// Download backdrop at the wide banner size + mark hi-res.
	m.downloadAndCacheBackdrop(ctx, showID, show.BackdropPath)

	// Fetch and cache each season from the show.
	for _, s := range show.Seasons {
		sn := s.SeasonNumber
		season, err := m.client.FetchTVSeason(ctx, showID, sn)
		if err != nil {
			slog.Warn("tmdb fetch season", "show", showID, "season", sn, "err", err)
			continue
		}
		seasonJSON, _ := json.Marshal(season)
		seasonKey := fmt.Sprintf("show:%d:season:%d", showID, sn)
		_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', ?, datetime('now'))
ON CONFLICT(entity_key, lang) DO UPDATE SET
  payload_json=excluded.payload_json,
  fetched_at=excluded.fetched_at,
  updated_at=datetime('now')
`, seasonKey, string(seasonJSON))

		if season.PosterPath != "" {
			spDest := filepath.Join(m.artDir, fmt.Sprintf("show_%d_season_%d_poster.jpg", showID, sn))
			if err := m.client.DownloadImage(ctx, season.PosterPath, spDest); err != nil {
				slog.Warn("tmdb season poster download", "show", showID, "season", sn, "err", err)
			} else {
				_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_art (art_type, tmdb_series_id, season_number, episode_number, art_path)
VALUES ('season_poster', ?, ?, -1, ?)
ON CONFLICT(art_type, tmdb_series_id, season_number, episode_number) DO UPDATE SET
  art_path=excluded.art_path, fetched_at=datetime('now')
`, showID, sn, spDest)
			}
		}

		m.cacheEpisodes(ctx, showID, sn, season.Episodes)
	}

	// Fetch + cache the cast for the manually-assigned show too (best-effort).
	if err := m.FetchTVCast(ctx, showID); err != nil {
		slog.Warn("tmdb cast fetch", "show", showID, "err", err)
	}

	return nil
}

// downloadAndCacheBackdrop fetches a show's backdrop at the wide banner size
// (w1280), records the art row, and marks the hi-res backdrop fetched so the
// lazy on-view backfill doesn't re-run (the marker is written even when a show
// has no backdrop, so an art-less show isn't retried every view).
func (m *Matcher) downloadAndCacheBackdrop(ctx context.Context, showID int, backdropPath string) {
	if backdropPath != "" {
		bdDest := filepath.Join(m.artDir, fmt.Sprintf("show_%d_backdrop.jpg", showID))
		if err := m.client.DownloadBackdrop(ctx, backdropPath, bdDest); err != nil {
			slog.Warn("tmdb backdrop download", "show", showID, "err", err)
		} else {
			_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_art (art_type, tmdb_series_id, art_path, season_number, episode_number)
VALUES ('series_backdrop', ?, ?, -1, -1)
ON CONFLICT(art_type, tmdb_series_id, season_number, episode_number) DO UPDATE SET
  art_path=excluded.art_path, fetched_at=datetime('now')
`, showID, bdDest)
		}
	}
	_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', '{}', datetime('now'))
ON CONFLICT(entity_key, lang) DO UPDATE SET fetched_at=excluded.fetched_at, updated_at=datetime('now')
`, fmt.Sprintf("show:%d:backdrop_hires", showID))
}

// RefetchBackdrop re-downloads a matched show's backdrop at the wide banner size
// for shows whose cached backdrop predates the w1280 change. The backdrop path
// comes from the already-cached show metadata, so no extra TMDB call is made.
// Lazy: enqueued on first series-page view when the hi-res marker is absent.
func (m *Matcher) RefetchBackdrop(ctx context.Context, showID int) error {
	if err := os.MkdirAll(m.artDir, 0o755); err != nil {
		return err
	}
	var payload string
	if err := m.db.QueryRowContext(ctx,
		"SELECT payload_json FROM tv_series_metadata_cache WHERE entity_key=? AND lang='en'",
		fmt.Sprintf("show:%d", showID),
	).Scan(&payload); err != nil {
		return err
	}
	var show TVShow
	if err := json.Unmarshal([]byte(payload), &show); err != nil {
		return err
	}
	m.downloadAndCacheBackdrop(ctx, showID, show.BackdropPath)
	return nil
}

// cacheEpisodes upserts episode metadata for one season in a single
// transaction, instead of one autocommit per episode.
func (m *Matcher) cacheEpisodes(ctx context.Context, showID, seasonNum int, episodes []TVEpisode) {
	if len(episodes) == 0 {
		return
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	for _, ep := range episodes {
		epKey := fmt.Sprintf("show:%d:season:%d:episode:%d", showID, seasonNum, ep.EpisodeNumber)
		epJSON, err := json.Marshal(ep)
		if err != nil {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', ?, datetime('now'))
ON CONFLICT(entity_key, lang) DO UPDATE SET
  payload_json=excluded.payload_json,
  fetched_at=excluded.fetched_at,
  updated_at=datetime('now')
`, epKey, string(epJSON)); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

// SearchTV proxies a TMDB search and returns results. Used by the match search endpoint.
func (m *Matcher) SearchTV(ctx context.Context, query string) ([]TVSearchResult, error) {
	return m.client.SearchTV(ctx, query)
}

// pickBestResult scores candidates by name similarity + a small popularity
// bonus, and — when year > 0 (the show folder carried one) — by first-air-year
// agreement, which disambiguates reboots that share a name (Doctor Who 1963 vs
// 2005 vs 2023). year == 0 leaves the original name+popularity behavior intact.
func pickBestResult(results []TVSearchResult, query string, year int) (*TVSearchResult, float64) {
	if len(results) == 0 {
		return nil, 0
	}
	var best *TVSearchResult
	var bestScore float64
	for i := range results {
		sim := match.NormalizedSimilarity(results[i].Name, query)
		// Add small popularity bonus (normalized to 0-0.1 range).
		popBonus := results[i].Popularity / 10000.0
		if popBonus > 0.1 {
			popBonus = 0.1
		}
		score := sim + popBonus
		// releaseYear/absInt are shared with the movie scorer (movie.go).
		if ry := releaseYear(results[i].FirstAirDate); year > 0 && ry > 0 {
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

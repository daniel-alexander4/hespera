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

	"isomedia/internal/match"
)

type Matcher struct {
	db     *sql.DB
	client *Client
	artDir string
}

func NewMatcher(db *sql.DB, apiKey, dataDir string) *Matcher {
	artDir := filepath.Join(dataDir, "thumbs", "tv")
	return &Matcher{
		db:     db,
		client: NewClient(apiKey),
		artDir: artDir,
	}
}

func (m *Matcher) RunTVMatch(ctx context.Context, jobID, libraryID int64) error {
	if err := os.MkdirAll(m.artDir, 0o755); err != nil {
		return err
	}

	// Query all unresolved identities for this library.
	rows, err := m.db.QueryContext(ctx, `
SELECT i.file_id, i.guessed_title, i.season_number
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
	}
	// Group by normalized title.
	groups := make(map[string][]fileEntry)
	var groupOrder []string
	for rows.Next() {
		var fe fileEntry
		var title string
		if err := rows.Scan(&fe.fileID, &title, &fe.seasonNumber); err != nil {
			return err
		}
		key := strings.ToLower(strings.TrimSpace(title))
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
		// Use the raw (un-lowered) title from the first file for the search.
		var rawTitle string
		_ = m.db.QueryRowContext(ctx,
			"SELECT guessed_title FROM tv_series_identities WHERE file_id=?",
			files[0].fileID,
		).Scan(&rawTitle)
		if rawTitle == "" {
			rawTitle = key
		}

		slog.Info("tmdb match", "title", rawTitle, "files", len(files))

		results, err := m.client.SearchTV(ctx, rawTitle)
		if err != nil {
			slog.Warn("tmdb search failed", "title", rawTitle, "err", err)
			continue
		}

		bestResult, bestScore := pickBestResult(results, rawTitle)
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

		// Download backdrop.
		if show.BackdropPath != "" {
			bdDest := filepath.Join(m.artDir, fmt.Sprintf("show_%d_backdrop.jpg", showID))
			if err := m.client.DownloadImage(ctx, show.BackdropPath, bdDest); err != nil {
				slog.Warn("tmdb backdrop download", "err", err)
			} else {
				_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_art (art_type, tmdb_series_id, art_path, season_number, episode_number)
VALUES ('series_backdrop', ?, ?, -1, -1)
ON CONFLICT(art_type, tmdb_series_id, season_number, episode_number) DO UPDATE SET
  art_path=excluded.art_path, fetched_at=datetime('now')
`, showID, bdDest)
			}
		}

		// Collect unique seasons referenced by files.
		seasonSet := make(map[int]bool)
		for _, fe := range files {
			if fe.seasonNumber >= 0 {
				seasonSet[fe.seasonNumber] = true
			}
		}

		// Fetch and cache each referenced season.
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
				if err := m.client.DownloadImage(ctx, season.PosterPath, spDest); err == nil {
					_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_art (art_type, tmdb_series_id, season_number, episode_number, art_path)
VALUES ('season_poster', ?, ?, -1, ?)
ON CONFLICT(art_type, tmdb_series_id, season_number, episode_number) DO UPDATE SET
  art_path=excluded.art_path, fetched_at=datetime('now')
`, showID, sn, spDest)
				}
			}

			// Cache episode metadata.
			for _, ep := range season.Episodes {
				epKey := fmt.Sprintf("show:%d:season:%d:episode:%d", showID, sn, ep.EpisodeNumber)
				epJSON, _ := json.Marshal(ep)
				_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', ?, datetime('now'))
ON CONFLICT(entity_key, lang) DO UPDATE SET
  payload_json=excluded.payload_json,
  fetched_at=excluded.fetched_at,
  updated_at=datetime('now')
`, epKey, string(epJSON))
			}
		}

		// Update identities for all files in this group.
		now := time.Now().UTC().Format(time.RFC3339)
		for _, fe := range files {
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

	// Download backdrop.
	if show.BackdropPath != "" {
		bdDest := filepath.Join(m.artDir, fmt.Sprintf("show_%d_backdrop.jpg", showID))
		if err := m.client.DownloadImage(ctx, show.BackdropPath, bdDest); err != nil {
			slog.Warn("tmdb backdrop download", "err", err)
		} else {
			_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_art (art_type, tmdb_series_id, art_path, season_number, episode_number)
VALUES ('series_backdrop', ?, ?, -1, -1)
ON CONFLICT(art_type, tmdb_series_id, season_number, episode_number) DO UPDATE SET
  art_path=excluded.art_path, fetched_at=datetime('now')
`, showID, bdDest)
		}
	}

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
			if err := m.client.DownloadImage(ctx, season.PosterPath, spDest); err == nil {
				_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_art (art_type, tmdb_series_id, season_number, episode_number, art_path)
VALUES ('season_poster', ?, ?, -1, ?)
ON CONFLICT(art_type, tmdb_series_id, season_number, episode_number) DO UPDATE SET
  art_path=excluded.art_path, fetched_at=datetime('now')
`, showID, sn, spDest)
			}
		}

		for _, ep := range season.Episodes {
			epKey := fmt.Sprintf("show:%d:season:%d:episode:%d", showID, sn, ep.EpisodeNumber)
			epJSON, _ := json.Marshal(ep)
			_, _ = m.db.ExecContext(ctx, `
INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', ?, datetime('now'))
ON CONFLICT(entity_key, lang) DO UPDATE SET
  payload_json=excluded.payload_json,
  fetched_at=excluded.fetched_at,
  updated_at=datetime('now')
`, epKey, string(epJSON))
		}
	}

	return nil
}

// SearchTV proxies a TMDB search and returns results. Used by the match search endpoint.
func (m *Matcher) SearchTV(ctx context.Context, query string) ([]TVSearchResult, error) {
	return m.client.SearchTV(ctx, query)
}

func pickBestResult(results []TVSearchResult, query string) (*TVSearchResult, float64) {
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
		if score > bestScore {
			bestScore = score
			best = &results[i]
		}
	}
	return best, bestScore
}

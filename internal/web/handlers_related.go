package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"hespera/internal/tmdb"
)

// "More Like This": the detail pages render a strip of related titles from the
// cached TMDB recommendations blob (show:%d:similar / movie:%d:similar), lazily
// fetched in the background on first view — the blob itself is the fetched
// marker ([] caches too, so a title with no related data never re-enqueues).
// Rows follow the actor-filmography pattern: owned titles link into the library
// with local art, the rest hotlink a TMDB poster and link out.

// tvSimilarRows returns the related-titles rows for a show, enqueueing the
// one-time background fetch when the blob is absent (nil until it lands).
func (h *Handler) tvSimilarRows(ctx context.Context, showID int) []filmographyRow {
	var blob string
	err := h.db.QueryRowContext(ctx,
		"SELECT payload_json FROM tv_series_metadata_cache WHERE entity_key=? AND lang='en'",
		fmt.Sprintf("show:%d:similar", showID)).Scan(&blob)
	if err != nil {
		h.enqueueMetaFetch(ctx, fmt.Sprintf("similar:%d", showID), "tv_similar_fetch",
			func(jctx context.Context, m *tmdb.Matcher) error { return m.FetchTVSimilar(jctx, showID) })
		return nil
	}
	return h.buildRelatedRows(ctx, blob, "tv")
}

// movieSimilarRows is the movie twin of tvSimilarRows.
func (h *Handler) movieSimilarRows(ctx context.Context, tmdbID int) []filmographyRow {
	var blob string
	err := h.db.QueryRowContext(ctx,
		"SELECT payload_json FROM movie_metadata_cache WHERE entity_key=?",
		fmt.Sprintf("movie:%d:similar", tmdbID)).Scan(&blob)
	if err != nil {
		h.enqueueMovieMetaFetch(ctx, fmt.Sprintf("movie-similar:%d", tmdbID), "movie_similar_fetch",
			func(jctx context.Context, m *tmdb.Matcher) error { return m.FetchMovieSimilar(jctx, tmdbID) })
		return nil
	}
	return h.buildRelatedRows(ctx, blob, "movie")
}

// buildRelatedRows parses a related-titles blob and marks each row Owned
// against the library (owned → local link + /art poster in the template;
// un-owned → TMDB link + hotlinked w342 poster, the filmography pattern).
func (h *Handler) buildRelatedRows(ctx context.Context, blob, mediaType string) []filmographyRow {
	var titles []tmdb.RelatedTitle
	if json.Unmarshal([]byte(blob), &titles) != nil || len(titles) == 0 {
		return nil
	}
	ids := make([]int, 0, len(titles))
	for _, t := range titles {
		ids = append(ids, t.ID)
	}
	owned := h.ownedTitleIDs(ctx, mediaType, ids)
	rows := make([]filmographyRow, 0, len(titles))
	for _, t := range titles {
		row := filmographyRow{ID: t.ID, Title: t.DisplayTitle(), Year: t.Year(), Owned: owned[t.ID]}
		if !row.Owned && t.PosterPath != "" {
			row.PosterURL = tmdbPosterBase + t.PosterPath
		}
		rows = append(rows, row)
	}
	return rows
}

// ownedTitleIDs filters candidate TMDB ids down to the ones matched in the
// library, per media type (a TV id and a movie id can collide as integers —
// same reason the person-page ownership sets are keyed per type).
func (h *Handler) ownedTitleIDs(ctx context.Context, mediaType string, ids []int) map[int]bool {
	owned := map[int]bool{}
	if len(ids) == 0 {
		return owned
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	var q string
	switch mediaType {
	case "tv":
		// series_id is TEXT (the matcher stores the TMDB id stringified).
		q = "SELECT DISTINCT CAST(series_id AS INTEGER) FROM tv_series_identities WHERE status='matched' AND series_id != '' AND CAST(series_id AS INTEGER) IN (" + ph + ")"
	case "movie":
		q = "SELECT DISTINCT tmdb_id FROM movie_files WHERE match_status='matched' AND tmdb_id IN (" + ph + ")"
	default:
		return owned
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := h.db.QueryContext(ctx, q, args...)
	if err != nil {
		return owned
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		if rows.Scan(&id) == nil {
			owned[id] = true
		}
	}
	return owned
}

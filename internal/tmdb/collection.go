package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// CollectionRef is the belongs_to_collection stub on /movie/{id} — just enough
// to name the franchise and fetch its members.
type CollectionRef struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	PosterPath string `json:"poster_path"`
}

// Collection is /collection/{id}: the franchise and its member films. Parts
// reuse RelatedTitle — the same id/title/poster_path/release_date shape the
// More Like This strip renders.
type Collection struct {
	ID    int            `json:"id"`
	Name  string         `json:"name"`
	Parts []RelatedTitle `json:"parts"`
}

// FetchCollection returns a franchise's member films from /collection/{id}.
func (c *Client) FetchCollection(ctx context.Context, collectionID int) (*Collection, error) {
	<-c.limiter

	u := fmt.Sprintf("%s/collection/%d?api_key=%s&language=en-US",
		c.apiBase, collectionID, url.QueryEscape(c.apiKey))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("tmdb collection: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tmdb collection %d: status %d", collectionID, resp.StatusCode)
	}

	var col Collection
	if err := json.NewDecoder(resp.Body).Decode(&col); err != nil {
		return nil, fmt.Errorf("tmdb collection decode: %w", err)
	}
	return &col, nil
}

// FetchMovieCollection backfills a film's franchise data: it re-fetches the
// movie details (refreshing the movie:%d blob, whose pre-field cached copies
// lack belongs_to_collection), caches the collection's member list under
// collection:%d when the film belongs to one, and writes the movie:%d:collection
// marker either way so a page view enqueues this at most once — standalone
// films included.
func (m *Matcher) FetchMovieCollection(ctx context.Context, movieID int) error {
	movie, err := m.client.FetchMovie(ctx, movieID)
	if err != nil {
		return fmt.Errorf("fetch movie %d: %w", movieID, err)
	}
	movieJSON, _ := json.Marshal(movie)
	if _, err := m.db.ExecContext(ctx, `
INSERT INTO movie_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', ?, datetime('now'))
ON CONFLICT(entity_key) DO UPDATE SET
  payload_json=excluded.payload_json,
  fetched_at=excluded.fetched_at,
  updated_at=datetime('now')
`, fmt.Sprintf("movie:%d", movieID), string(movieJSON)); err != nil {
		return err
	}

	if movie.BelongsToCollection != nil {
		col, err := m.client.FetchCollection(ctx, movie.BelongsToCollection.ID)
		if err != nil {
			return fmt.Errorf("fetch collection %d: %w", movie.BelongsToCollection.ID, err)
		}
		parts := col.Parts
		if parts == nil {
			parts = []RelatedTitle{}
		}
		b, _ := json.Marshal(parts)
		if _, err := m.db.ExecContext(ctx, `
INSERT INTO movie_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', ?, datetime('now'))
ON CONFLICT(entity_key) DO UPDATE SET
  payload_json=excluded.payload_json,
  fetched_at=excluded.fetched_at,
  updated_at=datetime('now')
`, fmt.Sprintf("collection:%d", col.ID), string(b)); err != nil {
			return err
		}
	}

	_, err = m.db.ExecContext(ctx, `
INSERT INTO movie_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', '{}', datetime('now'))
ON CONFLICT(entity_key) DO UPDATE SET fetched_at=excluded.fetched_at, updated_at=datetime('now')
`, fmt.Sprintf("movie:%d:collection", movieID))
	return err
}

package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// relatedLimit caps the cached "More Like This" list — a strip, not a browse
// page (TMDB returns up to 20 per page).
const relatedLimit = 12

// RelatedTitle is one entry of a TMDB /recommendations or /similar result — a
// "More Like This" candidate. Movies carry title/release_date, TV shows
// name/first_air_date; the accessors normalize the split.
type RelatedTitle struct {
	ID           int    `json:"id"`
	Title        string `json:"title,omitempty"`
	Name         string `json:"name,omitempty"`
	PosterPath   string `json:"poster_path"`
	ReleaseDate  string `json:"release_date,omitempty"`
	FirstAirDate string `json:"first_air_date,omitempty"`
}

func (r RelatedTitle) DisplayTitle() string {
	if r.Title != "" {
		return r.Title
	}
	return r.Name
}

func (r RelatedTitle) Year() string {
	d := r.ReleaseDate
	if d == "" {
		d = r.FirstAirDate
	}
	if len(d) >= 4 {
		return d[:4]
	}
	return ""
}

// FetchTVRelated returns similar titles for a show: /recommendations first
// (behavioral, better matches), falling back to /similar (genre/keyword-based,
// rarely empty) when recommendations has nothing — obscure titles often lack
// recommendation data but still have similars.
func (c *Client) FetchTVRelated(ctx context.Context, showID int) ([]RelatedTitle, error) {
	return c.fetchRelated(ctx, "tv", showID)
}

// FetchMovieRelated is the movie twin of FetchTVRelated.
func (c *Client) FetchMovieRelated(ctx context.Context, movieID int) ([]RelatedTitle, error) {
	return c.fetchRelated(ctx, "movie", movieID)
}

func (c *Client) fetchRelated(ctx context.Context, mediaType string, id int) ([]RelatedTitle, error) {
	list, err := c.fetchRelatedEndpoint(ctx, mediaType, id, "recommendations")
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		if list, err = c.fetchRelatedEndpoint(ctx, mediaType, id, "similar"); err != nil {
			return nil, err
		}
	}
	if len(list) > relatedLimit {
		list = list[:relatedLimit]
	}
	return list, nil
}

func (c *Client) fetchRelatedEndpoint(ctx context.Context, mediaType string, id int, endpoint string) ([]RelatedTitle, error) {
	<-c.limiter

	u := fmt.Sprintf("%s/%s/%d/%s?api_key=%s&language=en-US",
		c.apiBase, mediaType, id, endpoint, url.QueryEscape(c.apiKey))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("tmdb %s %s: %w", mediaType, endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tmdb %s %d %s: status %d", mediaType, id, endpoint, resp.StatusCode)
	}

	var out struct {
		Results []RelatedTitle `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("tmdb %s decode: %w", endpoint, err)
	}
	return out.Results, nil
}

// FetchTVSimilar fetches and caches a show's "More Like This" list under
// show:%d:similar in tv_series_metadata_cache. An empty result caches as [] so
// the blob doubles as the fetched marker — a title with no related data never
// re-enqueues on every page view.
func (m *Matcher) FetchTVSimilar(ctx context.Context, showID int) error {
	list, err := m.client.FetchTVRelated(ctx, showID)
	if err != nil {
		return fmt.Errorf("fetch tv related %d: %w", showID, err)
	}
	if list == nil {
		list = []RelatedTitle{}
	}
	b, _ := json.Marshal(list)
	_, err = m.db.ExecContext(ctx, `
INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', ?, datetime('now'))
ON CONFLICT(entity_key, lang) DO UPDATE SET
  payload_json=excluded.payload_json,
  fetched_at=excluded.fetched_at,
  updated_at=datetime('now')
`, fmt.Sprintf("show:%d:similar", showID), string(b))
	return err
}

// FetchMovieSimilar is the movie twin of FetchTVSimilar (movie_metadata_cache,
// conflict target entity_key — no lang in its PK).
func (m *Matcher) FetchMovieSimilar(ctx context.Context, movieID int) error {
	list, err := m.client.FetchMovieRelated(ctx, movieID)
	if err != nil {
		return fmt.Errorf("fetch movie related %d: %w", movieID, err)
	}
	if list == nil {
		list = []RelatedTitle{}
	}
	b, _ := json.Marshal(list)
	_, err = m.db.ExecContext(ctx, `
INSERT INTO movie_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', ?, datetime('now'))
ON CONFLICT(entity_key) DO UPDATE SET
  payload_json=excluded.payload_json,
  fetched_at=excluded.fetched_at,
  updated_at=datetime('now')
`, fmt.Sprintf("movie:%d:similar", movieID), string(b))
	return err
}

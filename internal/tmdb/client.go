package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const (
	apiBase     = "https://api.themoviedb.org/3"
	imgPoster   = "https://image.tmdb.org/t/p/w500"
	imgBackdrop = "https://image.tmdb.org/t/p/w1280"
	imgStill    = "https://image.tmdb.org/t/p/w300"

	maxImageBytes = 20 << 20 // 20 MiB cap on downloaded artwork
)

type Client struct {
	apiKey     string
	httpClient *http.Client
	limiter    <-chan time.Time
	apiBase    string
	imgBase    string
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		limiter:    time.NewTicker(250 * time.Millisecond).C, // 4 req/sec
		apiBase:    apiBase,
		imgBase:    imgPoster,
	}
}

// do issues the request and, on a transport error, strips the URL's query
// string before returning. The query carries the api_key, and a *url.Error
// embeds the full URL in its message, which would otherwise leak the key
// into logs when callers wrap the error with %w.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			if u, perr := url.Parse(ue.URL); perr == nil {
				u.RawQuery = ""
				ue.URL = u.String()
			} else {
				ue.URL = "[redacted]"
			}
		}
		return nil, err
	}
	return resp, nil
}

type TVSearchResult struct {
	ID           int     `json:"id"`
	Name         string  `json:"name"`
	FirstAirDate string  `json:"first_air_date"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	Popularity   float64 `json:"popularity"`
}

type TVShow struct {
	ID           int        `json:"id"`
	Name         string     `json:"name"`
	Overview     string     `json:"overview"`
	FirstAirDate string     `json:"first_air_date"`
	PosterPath   string     `json:"poster_path"`
	BackdropPath string     `json:"backdrop_path"`
	Seasons      []TVSeason `json:"seasons"`
	Genres       []Genre    `json:"genres"`
	Status       string     `json:"status"`
}

type TVSeason struct {
	SeasonNumber int         `json:"season_number"`
	Name         string      `json:"name"`
	Overview     string      `json:"overview"`
	PosterPath   string      `json:"poster_path"`
	Episodes     []TVEpisode `json:"episodes"`
	AirDate      string      `json:"air_date"`
}

type TVEpisode struct {
	EpisodeNumber int     `json:"episode_number"`
	Name          string  `json:"name"`
	Overview      string  `json:"overview"`
	StillPath     string  `json:"still_path"`
	AirDate       string  `json:"air_date"`
	VoteAverage   float64 `json:"vote_average"`
}

type Genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (c *Client) SearchTV(ctx context.Context, query string) ([]TVSearchResult, error) {
	<-c.limiter

	u := fmt.Sprintf("%s/search/tv?api_key=%s&query=%s&language=en-US&page=1",
		c.apiBase, url.QueryEscape(c.apiKey), url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("tmdb search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tmdb search: status %d", resp.StatusCode)
	}

	var result struct {
		Results []TVSearchResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("tmdb search decode: %w", err)
	}
	return result.Results, nil
}

func (c *Client) FetchTVShow(ctx context.Context, showID int) (*TVShow, error) {
	<-c.limiter

	u := fmt.Sprintf("%s/tv/%d?api_key=%s&language=en-US",
		c.apiBase, showID, url.QueryEscape(c.apiKey))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("tmdb show: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tmdb show %d: status %d", showID, resp.StatusCode)
	}

	var show TVShow
	if err := json.NewDecoder(resp.Body).Decode(&show); err != nil {
		return nil, fmt.Errorf("tmdb show decode: %w", err)
	}
	return &show, nil
}

func (c *Client) FetchTVSeason(ctx context.Context, showID, seasonNumber int) (*TVSeason, error) {
	<-c.limiter

	u := fmt.Sprintf("%s/tv/%d/season/%d?api_key=%s&language=en-US",
		c.apiBase, showID, seasonNumber, url.QueryEscape(c.apiKey))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("tmdb season: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tmdb season %d/%d: status %d", showID, seasonNumber, resp.StatusCode)
	}

	var season TVSeason
	if err := json.NewDecoder(resp.Body).Decode(&season); err != nil {
		return nil, fmt.Errorf("tmdb season decode: %w", err)
	}
	return &season, nil
}

func (c *Client) DownloadImage(ctx context.Context, tmdbPath, destPath string) error {
	if tmdbPath == "" {
		return nil
	}
	<-c.limiter

	imgURL := c.imgBase + tmdbPath
	req, err := http.NewRequestWithContext(ctx, "GET", imgURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("tmdb image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("tmdb image: status %d", resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, io.LimitReader(resp.Body, maxImageBytes))
	return err
}

// ImageURL returns the full URL for a TMDB image path with the given base.
func ImageURL(base, path string) string {
	if path == "" {
		return ""
	}
	return base + path
}

// parseSearchResponse is exported for testing.
func parseSearchResponse(data []byte) ([]TVSearchResult, error) {
	var result struct {
		Results []TVSearchResult `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result.Results, nil
}

// parseShowResponse is exported for testing.
func parseShowResponse(data []byte) (*TVShow, error) {
	var show TVShow
	if err := json.Unmarshal(data, &show); err != nil {
		return nil, err
	}
	return &show, nil
}

// parseSeasonResponse is exported for testing.
func parseSeasonResponse(data []byte) (*TVSeason, error) {
	var season TVSeason
	if err := json.Unmarshal(data, &season); err != nil {
		return nil, err
	}
	return &season, nil
}

// Package itunes resolves cover art for a song (artist + title) via Apple's
// keyless iTunes Search API. It backs the "Rediscover a Year" page, whose
// week-by-week Hot 100 shows local art for songs the user owns and, for the
// many it doesn't, a real cover from here instead of a placeholder. No key
// required, so the lookup is always available; results are cached by the caller
// (the itunes_art table) so each song is resolved at most once.
package itunes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const apiBaseURL = "https://itunes.apple.com/search"

// ErrRateLimited signals the API throttled us (HTTP 403/429). The caller should
// stop the batch and retry later rather than keep hammering — the iTunes Search
// API is gentle on volume, so a backfill fills the grid progressively.
var ErrRateLimited = errors.New("itunes: rate limited")

// Client calls the iTunes Search API. Keyless; always usable.
type Client struct {
	http   *http.Client
	apiURL string
}

// New returns a ready Client (no key needed).
func New() *Client {
	return &Client{http: &http.Client{Timeout: 10 * time.Second}, apiURL: apiBaseURL}
}

// Search resolves "artist title" to a 600×600 cover-art URL, or "" if there's
// no match. A nil Client or empty title returns "" with no error.
func (c *Client) Search(ctx context.Context, artist, title string) (string, error) {
	if c == nil || title == "" {
		return "", nil
	}
	term := title
	if artist != "" {
		term = artist + " " + title
	}
	params := url.Values{
		"term":   {term},
		"media":  {"music"},
		"entity": {"song"},
		"limit":  {"1"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL+"?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "hespera/1.0")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return "", ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("itunes search HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	var result struct {
		Results []struct {
			ArtworkURL100 string `json:"artworkUrl100"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse itunes search: %w", err)
	}
	if len(result.Results) == 0 || result.Results[0].ArtworkURL100 == "" {
		return "", nil // genuine no-match — caller caches this as a miss
	}
	return upsize(result.Results[0].ArtworkURL100), nil
}

// upsize swaps iTunes' 100×100 thumbnail dimension for a crisp 600×600. The URL
// embeds the size as ".../100x100bb.jpg"; if that token is absent the URL is
// returned unchanged.
func upsize(u string) string {
	return strings.Replace(u, "100x100bb", "600x600bb", 1)
}

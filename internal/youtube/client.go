// Package youtube resolves a song (artist + title) to an embeddable YouTube
// video id via the official YouTube Data API v3 search endpoint. It is used by
// the "Rediscover a Year" page to play charting songs the user doesn't own
// locally. Key-only: New("") returns nil and every method is a no-op, so the
// feature degrades to a plain link-out when no key is configured.
package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

const apiBaseURL = "https://www.googleapis.com/youtube/v3"

// videoIDPattern is the exact YouTube video-id shape (11 url-safe chars). The
// resolved id is validated against it before it is ever embedded, so a bad API
// response can't inject anything into the iframe src.
var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// Client calls the YouTube Data API. nil when no key is configured.
type Client struct {
	http   *http.Client
	key    string
	apiURL string
}

// New returns a Client, or nil if key is empty (the feature is then link-out only).
func New(key string) *Client {
	if key == "" {
		return nil
	}
	return &Client{
		http:   &http.Client{Timeout: 10 * time.Second},
		key:    key,
		apiURL: apiBaseURL,
	}
}

// Search resolves "artist song" to the top embeddable video id, or "" if there's
// no usable match. A nil Client (no key) returns "" with no error.
func (c *Client) Search(ctx context.Context, artist, song string) (string, error) {
	if c == nil {
		return "", nil
	}
	q := song
	if artist != "" {
		q = artist + " " + song
	}
	params := url.Values{
		"part":            {"snippet"},
		"type":            {"video"},
		"videoEmbeddable": {"true"},
		"maxResults":      {"1"},
		"q":               {q},
		"key":             {c.key},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL+"/search?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "hespera/1.0")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		// 403 here is typically quota-exceeded; the caller falls back to link-out.
		return "", fmt.Errorf("youtube search HTTP %d", resp.StatusCode)
	}

	var result struct {
		Items []struct {
			ID struct {
				VideoID string `json:"videoId"`
			} `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse youtube search: %w", err)
	}
	for _, it := range result.Items {
		if videoIDPattern.MatchString(it.ID.VideoID) {
			return it.ID.VideoID, nil
		}
	}
	return "", nil
}

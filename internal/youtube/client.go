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
	"strings"
	"time"
)

// apiBaseURL is a var (not const) only so tests can point New() at a stub server
// via SetAPIBaseForTest — production never changes it.
var apiBaseURL = "https://www.googleapis.com/youtube/v3"

// SetAPIBaseForTest overrides the API base URL and returns a restore func. Test-only.
func SetAPIBaseForTest(u string) func() {
	old := apiBaseURL
	apiBaseURL = u
	return func() { apiBaseURL = old }
}

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

// viewerRegion is the country the embeddability check is evaluated for. Hespera
// is a single-user local app, so a fixed region is sufficient; a video blocked
// here (or not in an allow-list that includes it) is treated as unplayable.
const viewerRegion = "US"

// FirstEmbeddable returns the first id in ids that a viewer here can actually play
// in an embed — embeddable, upload-processed, and not region-restricted — or ""
// if none qualify. It is one videos.list call (1 quota unit for the whole batch,
// vs 100 for a search), so it's the cheap verification behind the quota-free
// yt-dlp resolver: yt-dlp finds candidates for free, this confirms one is
// playable before it's cached and embedded. Candidate order (relevance) is
// preserved. A nil Client (no key) returns "" so the caller can fall back.
func (c *Client) FirstEmbeddable(ctx context.Context, ids []string) (string, error) {
	if c == nil || len(ids) == 0 {
		return "", nil
	}
	params := url.Values{
		"part": {"status,contentDetails"},
		"id":   {strings.Join(ids, ",")},
		"key":  {c.key},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL+"/videos?"+params.Encode(), nil)
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
		return "", fmt.Errorf("youtube videos HTTP %d", resp.StatusCode)
	}

	var result struct {
		Items []struct {
			ID     string `json:"id"`
			Status struct {
				Embeddable   bool   `json:"embeddable"`
				UploadStatus string `json:"uploadStatus"`
			} `json:"status"`
			ContentDetails struct {
				RegionRestriction struct {
					Allowed []string `json:"allowed"`
					Blocked []string `json:"blocked"`
				} `json:"regionRestriction"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse youtube videos: %w", err)
	}

	playable := make(map[string]bool, len(result.Items))
	for _, it := range result.Items {
		s := it.Status
		if !s.Embeddable {
			continue
		}
		if s.UploadStatus != "" && s.UploadStatus != "processed" {
			continue
		}
		rr := it.ContentDetails.RegionRestriction
		if contains(rr.Blocked, viewerRegion) {
			continue
		}
		if len(rr.Allowed) > 0 && !contains(rr.Allowed, viewerRegion) {
			continue
		}
		playable[it.ID] = true
	}
	// Preserve candidate order — ids not returned by the API are deleted/private.
	for _, id := range ids {
		if playable[id] {
			return id, nil
		}
	}
	return "", nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

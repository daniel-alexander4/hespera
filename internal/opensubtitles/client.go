// Package opensubtitles is a small client for the OpenSubtitles REST API v1,
// used to find and download text subtitles for a TV episode when the source
// file carries no usable (text) subtitle stream.
//
// Auth is key-only: an Api-Key header plus a User-Agent identifying the
// registered consumer. There is no username/password/login-token flow for the
// search + download endpoints we use. The client is inert (New returns nil)
// when no key is configured, so callers can gate cheaply on a nil receiver —
// the same shape as match.FanartClient.
package opensubtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// userAgent identifies the registered OpenSubtitles consumer. The app is
// registered as "MediaSurfer" (verified working for both search and download),
// so this stays "MediaSurfer v1.0" rather than "Hespera" — OpenSubtitles ties
// the UA to the registered app name.
const userAgent = "MediaSurfer v1.0"

const baseURL = "https://api.opensubtitles.com/api/v1"

// rateLimiter enforces a minimum interval between successive calls. OpenSubtitles
// is its own host with its own budget, so each OSClient gets its own limiter.
type rateLimiter struct {
	mu       sync.Mutex
	last     time.Time
	interval time.Duration
}

func (l *rateLimiter) wait() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if since := time.Since(l.last); since < l.interval {
		time.Sleep(l.interval - since)
	}
	l.last = time.Now()
}

// OSClient talks to the OpenSubtitles REST API. Construct with New; a nil
// *OSClient is a valid no-op receiver (Search/Download return empty/err).
type OSClient struct {
	client  *http.Client
	apiKey  string
	baseURL string
	limiter *rateLimiter
}

// New returns a client, or nil when apiKey is empty so callers can gate on nil.
func New(apiKey string) *OSClient {
	if apiKey == "" {
		return nil
	}
	return &OSClient{
		client:  &http.Client{Timeout: 15 * time.Second},
		apiKey:  apiKey,
		baseURL: baseURL,
		limiter: &rateLimiter{interval: time.Second},
	}
}

// SearchResult is one candidate subtitle for the UI to list.
type SearchResult struct {
	FileID          int64  `json:"file_id"`
	Language        string `json:"language"`
	Release         string `json:"release"`
	FileName        string `json:"file_name"`
	DownloadCount   int    `json:"download_count"`
	HearingImpaired bool   `json:"hearing_impaired"`
}

type searchResponse struct {
	Data []struct {
		Attributes struct {
			Language        string `json:"language"`
			DownloadCount   int    `json:"download_count"`
			HearingImpaired bool   `json:"hearing_impaired"`
			Release         string `json:"release"`
			Files           []struct {
				FileID   int64  `json:"file_id"`
				FileName string `json:"file_name"`
			} `json:"files"`
		} `json:"attributes"`
	} `json:"data"`
}

// Search finds subtitles for an episode of a TMDB series. parentTMDBID is the
// show's TMDB id; season/episode pin the episode; lang is an ISO language code
// (e.g. "en"). It returns each candidate's first downloadable file.
func (c *OSClient) Search(ctx context.Context, parentTMDBID string, season, episode int, lang string) ([]SearchResult, error) {
	if c == nil {
		return nil, nil
	}
	if parentTMDBID == "" || season < 0 || episode <= 0 {
		return nil, fmt.Errorf("opensubtitles: need series tmdb id, season and episode")
	}
	q := url.Values{}
	q.Set("parent_tmdb_id", parentTMDBID)
	q.Set("season_number", fmt.Sprintf("%d", season))
	q.Set("episode_number", fmt.Sprintf("%d", episode))
	if lang != "" {
		q.Set("languages", lang)
	}
	reqURL := c.baseURL + "/subtitles?" + q.Encode()

	c.limiter.wait()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("opensubtitles search: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var sr searchResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, d := range sr.Data {
		a := d.Attributes
		if len(a.Files) == 0 || a.Files[0].FileID == 0 {
			continue // nothing downloadable
		}
		out = append(out, SearchResult{
			FileID:          a.Files[0].FileID,
			Language:        a.Language,
			Release:         a.Release,
			FileName:        a.Files[0].FileName,
			DownloadCount:   a.DownloadCount,
			HearingImpaired: a.HearingImpaired,
		})
	}
	return out, nil
}

type downloadResponse struct {
	Link      string `json:"link"`
	FileName  string `json:"file_name"`
	Remaining int    `json:"remaining"`
}

// Download requests a temporary download link for a subtitle file. This consumes
// one unit of the daily download quota (100/day). The caller is responsible for
// fetching the returned link (with its own SSRF host validation).
func (c *OSClient) Download(ctx context.Context, fileID int64) (link string, err error) {
	if c == nil {
		return "", fmt.Errorf("opensubtitles: no API key configured")
	}
	if fileID <= 0 {
		return "", fmt.Errorf("opensubtitles: invalid file id")
	}
	bodyBytes, _ := json.Marshal(map[string]int64{"file_id": fileID})

	c.limiter.wait()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/download", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("opensubtitles download: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var dr downloadResponse
	if err := json.Unmarshal(body, &dr); err != nil {
		return "", err
	}
	if dr.Link == "" {
		return "", fmt.Errorf("opensubtitles download: empty link in response")
	}
	return dr.Link, nil
}

func (c *OSClient) setHeaders(req *http.Request) {
	req.Header.Set("Api-Key", c.apiKey)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
}

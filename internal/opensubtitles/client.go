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
	"time"

	"hespera/internal/ratelimit"
)

// defaultUserAgent is the fallback User-Agent when none is configured. The UA
// must identify a consumer app *registered with OpenSubtitles* ("AppName vX.Y")
// — an unregistered UA is rejected with HTTP 403 — so it's injected via New
// (resolved from settings/env by the caller) and overridable; this default is
// only used when nothing is set.
const defaultUserAgent = "Hespera v1.0"

const baseURL = "https://api.opensubtitles.com/api/v1"

// OSClient talks to the OpenSubtitles REST API. Construct with New; a nil
// *OSClient is a valid no-op receiver (Search/Download return empty/err).
type OSClient struct {
	client    *http.Client
	apiKey    string
	baseURL   string
	limiter   *ratelimit.Limiter
	userAgent string
}

// New returns a client, or nil when apiKey is empty so callers can gate on nil.
// userAgent must name a consumer app registered with OpenSubtitles ("AppName
// vX.Y"); an empty value falls back to defaultUserAgent.
func New(apiKey, userAgent string) *OSClient {
	if apiKey == "" {
		return nil
	}
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	return &OSClient{
		client:    &http.Client{Timeout: 15 * time.Second},
		apiKey:    apiKey,
		baseURL:   baseURL,
		limiter:   ratelimit.New(time.Second),
		userAgent: userAgent,
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
	return c.runSearch(ctx, q, lang)
}

// SearchMovie finds subtitles for a TMDB movie. Unlike an episode search it sends
// a plain tmdb_id with no season/episode — the OpenSubtitles movie query.
func (c *OSClient) SearchMovie(ctx context.Context, tmdbID string, lang string) ([]SearchResult, error) {
	if c == nil {
		return nil, nil
	}
	if tmdbID == "" {
		return nil, fmt.Errorf("opensubtitles: need movie tmdb id")
	}
	q := url.Values{}
	q.Set("tmdb_id", tmdbID)
	return c.runSearch(ctx, q, lang)
}

// runSearch sends a /subtitles query (episode- or movie-shaped) and returns each
// candidate's first downloadable file. The response parsing is media-agnostic, so
// Search and SearchMovie differ only in the query params they build.
func (c *OSClient) runSearch(ctx context.Context, q url.Values, lang string) ([]SearchResult, error) {
	if lang != "" {
		q.Set("languages", lang)
	}
	reqURL := c.baseURL + "/subtitles?" + q.Encode()

	_ = c.limiter.Wait(ctx)
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

	_ = c.limiter.Wait(ctx)
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
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
}

package match

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"hespera/internal/ratelimit"
)

// LastfmClient fetches an artist's most-played tracks (with global play counts)
// from the Last.fm API — an optional secondary popularity source that fills in
// where ListenBrainz has no data. Read-only (artist.getTopTracks needs only the
// API key, no signed shared secret). Inert (nil) when no key is configured.
type LastfmClient struct {
	client  *http.Client
	apiKey  string
	baseURL string
	limiter *ratelimit.Limiter
}

// NewLastfmClient returns a client, or nil when apiKey is empty (the blend is
// then skipped entirely).
func NewLastfmClient(apiKey string) *LastfmClient {
	if apiKey == "" {
		return nil
	}
	return &LastfmClient{
		client:  &http.Client{Timeout: 15 * time.Second},
		apiKey:  apiKey,
		baseURL: "https://ws.audioscrobbler.com/2.0/",
		// Last.fm allows several req/s per key; keep it gentle (own host budget).
		limiter: ratelimit.New(250 * time.Millisecond),
	}
}

type lastfmTopTracksResp struct {
	TopTracks struct {
		Track []struct {
			Name      string `json:"name"`
			PlayCount string `json:"playcount"`
		} `json:"track"`
	} `json:"toptracks"`
}

// TopTracks returns the artist's top tracks keyed by NormalizeForDedup(name) →
// global play count (the highest when a normalized name collides). ok is false
// on any error or when the client is nil, so the caller simply skips the blend.
func (c *LastfmClient) TopTracks(ctx context.Context, artist string) (map[string]int, bool) {
	if c == nil || artist == "" {
		return nil, false
	}
	_ = c.limiter.Wait(ctx)
	q := url.Values{}
	q.Set("method", "artist.gettoptracks")
	q.Set("artist", artist)
	q.Set("api_key", c.apiKey)
	q.Set("format", "json")
	q.Set("autocorrect", "1")
	q.Set("limit", "100")
	reqURL := c.baseURL + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", mbUserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	var parsed lastfmTopTracksResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, false
	}
	out := make(map[string]int, len(parsed.TopTracks.Track))
	for _, t := range parsed.TopTracks.Track {
		key := NormalizeForDedup(t.Name)
		if key == "" {
			continue
		}
		pc, _ := strconv.Atoi(t.PlayCount)
		if pc > out[key] {
			out[key] = pc
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

package match

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// FanartClient fetches artist artwork from fanart.tv, keyed by MusicBrainz
// artist MBID. It is a best-effort backfill for artist images that Wikidata's
// P18 claim doesn't cover. Inert (returns "") when no API key is configured.
type FanartClient struct {
	client  *http.Client
	apiKey  string
	baseURL string
	limiter *rateLimiter
}

// NewFanartClient returns a client, or nil when apiKey is empty so callers can
// gate cheaply on a nil client.
func NewFanartClient(apiKey string) *FanartClient {
	if apiKey == "" {
		return nil
	}
	return &FanartClient{
		client:  &http.Client{Timeout: 15 * time.Second},
		apiKey:  apiKey,
		baseURL: "https://webservice.fanart.tv/v3",
		// fanart.tv is its own host with its own budget — never the shared
		// MetaBrainz limiter.
		limiter: newRateLimiter(time.Second),
	}
}

// fanartImage is one entry in a fanart.tv image-type array.
type fanartImage struct {
	URL   string `json:"url"`
	Likes string `json:"likes"`
}

// fanartArtist is the artist response. The album-art map is intentionally not
// modeled — this backfill is artist-imagery only.
type fanartArtist struct {
	ArtistThumb      []fanartImage `json:"artistthumb"`
	ArtistBackground []fanartImage `json:"artistbackground"`
}

// ArtistImageURL returns the best artist image URL for an MBID, preferring a
// square thumbnail over a background, or "" if none. Never an error for a clean
// 404 — a miss is expected for a best-effort fallback.
func (c *FanartClient) ArtistImageURL(ctx context.Context, artistMBID string) string {
	if c == nil || artistMBID == "" {
		return ""
	}
	c.limiter.wait()
	reqURL := fmt.Sprintf("%s/music/%s?api_key=%s", c.baseURL, url.PathEscape(artistMBID), url.QueryEscape(c.apiKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", mbUserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}
	var a fanartArtist
	if json.Unmarshal(body, &a) != nil {
		return ""
	}
	if len(a.ArtistThumb) > 0 && a.ArtistThumb[0].URL != "" {
		return a.ArtistThumb[0].URL
	}
	if len(a.ArtistBackground) > 0 {
		return a.ArtistBackground[0].URL
	}
	return ""
}

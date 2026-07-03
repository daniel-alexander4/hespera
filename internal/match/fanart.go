package match

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"hespera/internal/ratelimit"
)

// FanartClient fetches artist artwork from fanart.tv, keyed by MusicBrainz
// artist MBID. It is a best-effort backfill for artist images that Wikidata's
// P18 claim doesn't cover. Inert (returns "") when no API key is configured.
type FanartClient struct {
	client  *http.Client
	apiKey  string
	baseURL string
	limiter *ratelimit.Limiter
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
		limiter: ratelimit.New(time.Second),
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

// FanartArtistImage is one selectable artist image (a thumb or a background).
type FanartArtistImage struct {
	URL  string
	Kind string // "thumb" or "background"
}

// fetchArtistJSON does the HTTP fetch + parse shared by ArtistImageURL and the
// gallery method. ok=false on any miss/error — a clean 404 is expected for a
// best-effort source, not an error.
func (c *FanartClient) fetchArtistJSON(ctx context.Context, artistMBID string) (fanartArtist, bool) {
	var a fanartArtist
	if c == nil || artistMBID == "" {
		return a, false
	}
	_ = c.limiter.Wait(ctx)
	reqURL := fmt.Sprintf("%s/music/%s?api_key=%s", c.baseURL, url.PathEscape(artistMBID), url.QueryEscape(c.apiKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return a, false
	}
	req.Header.Set("User-Agent", mbUserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return a, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return a, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return a, false
	}
	if json.Unmarshal(body, &a) != nil {
		return a, false
	}
	return a, true
}

// ArtistImageURL returns the best artist image URL for an MBID, preferring a
// square thumbnail over a background, or "" if none.
func (c *FanartClient) ArtistImageURL(ctx context.Context, artistMBID string) string {
	a, ok := c.fetchArtistJSON(ctx, artistMBID)
	if !ok {
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

// ArtistImages returns every thumb and background fanart.tv holds for an MBID
// (thumbs first), for the image-picker gallery. Nil on miss/no key.
func (c *FanartClient) ArtistImages(ctx context.Context, artistMBID string) []FanartArtistImage {
	a, ok := c.fetchArtistJSON(ctx, artistMBID)
	if !ok {
		return nil
	}
	var out []FanartArtistImage
	for _, t := range a.ArtistThumb {
		if t.URL != "" {
			out = append(out, FanartArtistImage{URL: t.URL, Kind: "thumb"})
		}
	}
	for _, b := range a.ArtistBackground {
		if b.URL != "" {
			out = append(out, FanartArtistImage{URL: b.URL, Kind: "background"})
		}
	}
	return out
}

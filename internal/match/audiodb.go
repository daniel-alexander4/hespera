package match

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AudioDBClient fetches artist bios and images from TheAudioDB, keyed by
// MusicBrainz artist MBID. Best-effort backfill for bios that Wikipedia doesn't
// cover (and a secondary artist-image source). Inert (returns "") when no API
// key is configured.
type AudioDBClient struct {
	client  *http.Client
	apiKey  string
	baseURL string
	limiter *rateLimiter
}

// NewAudioDBClient returns a client, or nil when apiKey is empty. The public
// test key "123" works but is heavily throttled — callers should supply their
// own key.
func NewAudioDBClient(apiKey string) *AudioDBClient {
	if apiKey == "" {
		return nil
	}
	return &AudioDBClient{
		client:  &http.Client{Timeout: 15 * time.Second},
		apiKey:  apiKey,
		baseURL: "https://www.theaudiodb.com/api/v1/json",
		// Own host, own budget; the free tier is ~30/min so keep it gentle.
		limiter: newRateLimiter(2 * time.Second),
	}
}

type audiodbArtistResp struct {
	Artists []struct {
		BiographyEN string `json:"strBiographyEN"`
		ArtistThumb string `json:"strArtistThumb"`
	} `json:"artists"`
}

// fetchArtist looks an artist up by MBID, returning its first entry.
func (c *AudioDBClient) fetchArtist(ctx context.Context, artistMBID string) (bio, imageURL string) {
	if c == nil || artistMBID == "" {
		return "", ""
	}
	c.limiter.wait()
	reqURL := fmt.Sprintf("%s/%s/artist-mb.php?i=%s", c.baseURL, url.PathEscape(c.apiKey), url.QueryEscape(artistMBID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("User-Agent", mbUserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", ""
	}
	var a audiodbArtistResp
	if json.Unmarshal(body, &a) != nil || len(a.Artists) == 0 {
		return "", ""
	}
	return strings.TrimSpace(a.Artists[0].BiographyEN), strings.TrimSpace(a.Artists[0].ArtistThumb)
}

// ArtistBio returns the English biography for an MBID, or "".
func (c *AudioDBClient) ArtistBio(ctx context.Context, artistMBID string) string {
	bio, _ := c.fetchArtist(ctx, artistMBID)
	return bio
}

// ArtistImageURL returns the artist thumbnail URL for an MBID, or "".
func (c *AudioDBClient) ArtistImageURL(ctx context.Context, artistMBID string) string {
	_, img := c.fetchArtist(ctx, artistMBID)
	return img
}

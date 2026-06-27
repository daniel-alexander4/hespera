package match

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// lbBaseURL is the ListenBrainz API root. ListenBrainz is a MetaBrainz-family
// host with no auth required for the popularity endpoints.
const lbBaseURL = "https://api.listenbrainz.org"

// LBClient fetches global popularity (listen counts) from ListenBrainz, keyed by
// MusicBrainz artist MBID. It needs no API key and shares the MetaBrainz rate
// limiter with the MusicBrainz/Cover-Art-Archive clients.
type LBClient struct {
	client  *http.Client
	baseURL string
	limiter *rateLimiter
}

// NewLBClient builds a ListenBrainz client. It takes the shared MetaBrainz
// limiter (ListenBrainz is a MetaBrainz host) rather than its own, so all
// MetaBrainz-family traffic stays within one budget.
func NewLBClient(limiter *rateLimiter) *LBClient {
	return &LBClient{
		client:  &http.Client{Timeout: 15 * time.Second},
		baseURL: lbBaseURL,
		limiter: limiter,
	}
}

// LBRecording is one recording from an artist's top-recordings list.
type LBRecording struct {
	Name        string // recording_name
	ListenCount int    // total_listen_count (global)
}

// lbTopRecording maps the ListenBrainz /popularity/top-recordings-for-artist
// response array element. Only the fields we use are modeled.
type lbTopRecording struct {
	RecordingName    string `json:"recording_name"`
	TotalListenCount int    `json:"total_listen_count"`
}

// TopRecordings returns an artist's recordings ranked by global listen count
// (highest first), from ListenBrainz's popularity endpoint. ok=false on any
// miss/error — a 404/empty is expected for an obscure or MBID-less artist, not a
// hard error. nil-safe receiver, in the house style.
func (c *LBClient) TopRecordings(ctx context.Context, artistMBID string) ([]LBRecording, bool) {
	if c == nil || artistMBID == "" {
		return nil, false
	}
	c.limiter.wait()
	reqURL := fmt.Sprintf("%s/1/popularity/top-recordings-for-artist/%s", c.baseURL, url.PathEscape(artistMBID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", mbUserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, false
	}
	var raw []lbTopRecording
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false
	}
	out := make([]LBRecording, 0, len(raw))
	for _, r := range raw {
		if r.RecordingName == "" {
			continue
		}
		out = append(out, LBRecording{Name: r.RecordingName, ListenCount: r.TotalListenCount})
	}
	// The endpoint already returns listen-count-descending, but sort defensively
	// so callers can rely on the ordering regardless of API changes.
	sort.SliceStable(out, func(i, j int) bool { return out[i].ListenCount > out[j].ListenCount })
	return out, true
}

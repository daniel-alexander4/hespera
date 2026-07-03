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

	"hespera/internal/ratelimit"
)

// lbBaseURL is the ListenBrainz API root. ListenBrainz is a MetaBrainz-family
// host with no auth required for the popularity endpoints.
const lbBaseURL = "https://api.listenbrainz.org"

// lbLabsBaseURL is the ListenBrainz Labs host (a separate subdomain) serving the
// experimental similar-artists model.
const lbLabsBaseURL = "https://labs.api.listenbrainz.org"

// lbSimilarAlgorithm is the (required, opaque) algorithm parameter the labs
// similar-artists endpoint demands — it names the trained model. Pinned as a
// constant; if the labs API drops or renames it the call 400s and SimilarArtists
// returns ok=false, so the feature degrades to "no section" rather than erroring.
const lbSimilarAlgorithm = "session_based_days_7500_session_300_contribution_5_threshold_10_limit_100_filter_True_skip_30"

// LBClient fetches global popularity (listen counts) from ListenBrainz, keyed by
// MusicBrainz artist MBID. It needs no API key and shares the MetaBrainz rate
// limiter with the MusicBrainz/Cover-Art-Archive clients.
type LBClient struct {
	client  *http.Client
	baseURL string
	labsURL string // ListenBrainz Labs host (similar-artists); separate subdomain
	limiter *ratelimit.Limiter
}

// NewLBClient builds a ListenBrainz client. It takes the shared MetaBrainz
// limiter (ListenBrainz is a MetaBrainz host) rather than its own, so all
// MetaBrainz-family traffic stays within one budget.
func NewLBClient(limiter *ratelimit.Limiter) *LBClient {
	return &LBClient{
		client:  &http.Client{Timeout: 15 * time.Second},
		baseURL: lbBaseURL,
		labsURL: lbLabsBaseURL,
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
	_ = c.limiter.Wait(ctx)
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

// SimilarArtist is one related artist from the ListenBrainz similar-artists model.
type SimilarArtist struct {
	MBID    string // artist_mbid
	Name    string // artist name
	Comment string // MusicBrainz disambiguation comment, may be empty
	Score   int    // model similarity score (higher = more similar)
}

// lbSimilarArtist maps an element of the labs similar-artists JSON array.
type lbSimilarArtist struct {
	ArtistMBID string `json:"artist_mbid"`
	Name       string `json:"name"`
	Comment    string `json:"comment"`
	Score      int    `json:"score"`
}

// SimilarArtists returns artists related to artistMBID by listening data, highest
// score first, from the ListenBrainz Labs similar-artists model. ok=false on any
// miss/error (the labs endpoint is experimental — a 400/empty is treated as "no
// data", not a hard error). nil-safe receiver, in the house style.
func (c *LBClient) SimilarArtists(ctx context.Context, artistMBID string) ([]SimilarArtist, bool) {
	if c == nil || artistMBID == "" {
		return nil, false
	}
	_ = c.limiter.Wait(ctx)
	reqURL := fmt.Sprintf("%s/similar-artists/json?artist_mbids=%s&algorithm=%s",
		c.labsURL, url.QueryEscape(artistMBID), url.QueryEscape(lbSimilarAlgorithm))
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, false
	}
	var raw []lbSimilarArtist
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false
	}
	out := make([]SimilarArtist, 0, len(raw))
	for _, a := range raw {
		if a.ArtistMBID == "" || a.Name == "" {
			continue
		}
		out = append(out, SimilarArtist{MBID: a.ArtistMBID, Name: a.Name, Comment: a.Comment, Score: a.Score})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, true
}

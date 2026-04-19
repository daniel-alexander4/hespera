package match

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	mbBaseURL   = "https://musicbrainz.org/ws/2"
	mbUserAgent = "isomedia/1.0 (https://github.com/isomedia)"
)

// MBClient queries the MusicBrainz API with a 1 req/sec rate limiter.
type MBClient struct {
	client  *http.Client
	mu      sync.Mutex
	lastReq time.Time
}

func NewMBClient() *MBClient {
	return &MBClient{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *MBClient) throttle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	since := time.Since(c.lastReq)
	if since < time.Second {
		time.Sleep(time.Second - since)
	}
	c.lastReq = time.Now()
}

func (c *MBClient) get(ctx context.Context, path string) ([]byte, error) {
	c.throttle()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mbBaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", mbUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("musicbrainz rate limited (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("musicbrainz HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- JSON response types ---

type mbReleaseGroupSearchResult struct {
	ReleaseGroups []MBReleaseGroup `json:"release-groups"`
}

type MBReleaseGroup struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	PrimaryType  string          `json:"primary-type"`
	Score        int             `json:"score"`
	FirstRelease string          `json:"first-release-date"`
	ArtistCredit []mbArtistEntry `json:"artist-credit"`
	Releases     []mbRelease     `json:"releases"`
}

type mbReleaseSearchResult struct {
	Releases []MBRelease `json:"releases"`
}

type MBRelease struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Score        int             `json:"score"`
	Date         string          `json:"date"`
	ReleaseGroup *mbReleaseRef   `json:"release-group"`
	ArtistCredit []mbArtistEntry `json:"artist-credit"`
}

type mbReleaseRef struct {
	ID          string `json:"id"`
	PrimaryType string `json:"primary-type"`
}

type mbRelease struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type mbArtistEntry struct {
	Name   string   `json:"name"`
	Artist mbArtist `json:"artist"`
}

type mbArtist struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// --- Artist lookup ---

type MBArtistFull struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Relations []mbRelation `json:"relations"`
}

type mbRelation struct {
	Type string   `json:"type"`
	URL  mbRelURL `json:"url"`
}

type mbRelURL struct {
	Resource string `json:"resource"`
}

// Candidate is a unified match candidate from any search strategy.
type Candidate struct {
	ReleaseGroupID string
	ReleaseID      string
	Title          string
	ArtistName     string
	ArtistMBID     string
	PrimaryType    string
	Year           int
	MBScore        int
}

// SearchReleaseGroups runs the three-strategy cascade and returns candidates.
func (c *MBClient) SearchReleaseGroups(ctx context.Context, artist, album string) ([]Candidate, error) {
	// Strategy A: strict release-group search.
	candidates, err := c.strategyA(ctx, artist, album)
	if err != nil {
		return nil, err
	}
	if len(candidates) > 0 {
		return candidates, nil
	}

	// Strategy B: loose release search.
	candidates, err = c.strategyB(ctx, artist, album)
	if err != nil {
		return nil, err
	}
	if len(candidates) > 0 {
		return candidates, nil
	}

	// Strategy C: artist fallback, client-side title filter.
	return c.strategyC(ctx, artist, album)
}

func (c *MBClient) strategyA(ctx context.Context, artist, album string) ([]Candidate, error) {
	q := fmt.Sprintf(`releasegroup:"%s" AND artist:"%s"`, mbEscape(album), mbEscape(artist))
	path := fmt.Sprintf("/release-group?query=%s&limit=5&fmt=json", url.QueryEscape(q))

	body, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	var result mbReleaseGroupSearchResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse release-group search: %w", err)
	}

	var out []Candidate
	for _, rg := range result.ReleaseGroups {
		out = append(out, rgToCandidate(rg))
	}
	return out, nil
}

func (c *MBClient) strategyB(ctx context.Context, artist, album string) ([]Candidate, error) {
	q := fmt.Sprintf(`release:"%s" AND artist:"%s"`, mbEscape(album), mbEscape(artist))
	path := fmt.Sprintf("/release?query=%s&limit=10&fmt=json", url.QueryEscape(q))

	body, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	var result mbReleaseSearchResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse release search: %w", err)
	}

	var out []Candidate
	for _, rel := range result.Releases {
		out = append(out, releaseToCandidate(rel))
	}
	return out, nil
}

func (c *MBClient) strategyC(ctx context.Context, artist, album string) ([]Candidate, error) {
	q := fmt.Sprintf(`artist:"%s"`, mbEscape(artist))
	path := fmt.Sprintf("/release-group?query=%s&limit=25&fmt=json", url.QueryEscape(q))

	body, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	var result mbReleaseGroupSearchResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse release-group (artist) search: %w", err)
	}

	var out []Candidate
	for _, rg := range result.ReleaseGroups {
		if NormalizedSimilarity(rg.Title, album) >= 0.5 {
			out = append(out, rgToCandidate(rg))
		}
	}
	return out, nil
}

// SearchArtist searches MusicBrainz for an artist by name and returns the best match MBID.
func (c *MBClient) SearchArtist(ctx context.Context, name string) (string, error) {
	q := fmt.Sprintf(`artist:"%s"`, mbEscape(name))
	path := fmt.Sprintf("/artist?query=%s&limit=5&fmt=json", url.QueryEscape(q))

	body, err := c.get(ctx, path)
	if err != nil {
		return "", err
	}

	var result struct {
		Artists []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Score int    `json:"score"`
		} `json:"artists"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse artist search: %w", err)
	}

	if len(result.Artists) == 0 {
		return "", nil
	}

	// Take the top result only if it scores well and name is close.
	best := result.Artists[0]
	if best.Score < 80 {
		return "", nil
	}
	sim := NormalizedSimilarity(best.Name, name)
	if sim < 0.7 {
		return "", nil
	}
	return best.ID, nil
}

// LookupArtist fetches an artist with URL relations.
func (c *MBClient) LookupArtist(ctx context.Context, mbid string) (*MBArtistFull, error) {
	path := fmt.Sprintf("/artist/%s?inc=url-rels&fmt=json", url.PathEscape(mbid))
	body, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	var a MBArtistFull
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, fmt.Errorf("parse artist: %w", err)
	}
	return &a, nil
}

// --- helpers ---

func rgToCandidate(rg MBReleaseGroup) Candidate {
	c := Candidate{
		ReleaseGroupID: rg.ID,
		Title:          rg.Title,
		PrimaryType:    rg.PrimaryType,
		MBScore:        rg.Score,
		Year:           parseYear(rg.FirstRelease),
	}
	if len(rg.ArtistCredit) > 0 {
		c.ArtistName = rg.ArtistCredit[0].Name
		c.ArtistMBID = rg.ArtistCredit[0].Artist.ID
	}
	if len(rg.Releases) > 0 {
		c.ReleaseID = rg.Releases[0].ID
	}
	return c
}

func releaseToCandidate(rel MBRelease) Candidate {
	c := Candidate{
		ReleaseID: rel.ID,
		Title:     rel.Title,
		MBScore:   rel.Score,
		Year:      parseYear(rel.Date),
	}
	if rel.ReleaseGroup != nil {
		c.ReleaseGroupID = rel.ReleaseGroup.ID
		c.PrimaryType = rel.ReleaseGroup.PrimaryType
	}
	if len(rel.ArtistCredit) > 0 {
		c.ArtistName = rel.ArtistCredit[0].Name
		c.ArtistMBID = rel.ArtistCredit[0].Artist.ID
	}
	return c
}

func parseYear(date string) int {
	if len(date) < 4 {
		return 0
	}
	y := 0
	for i := 0; i < 4; i++ {
		c := date[i]
		if c < '0' || c > '9' {
			return 0
		}
		y = y*10 + int(c-'0')
	}
	return y
}

var luceneEscaper = strings.NewReplacer(
	`\`, `\\`,
	`"`, `\"`,
	`+`, `\+`,
	`-`, `\-`,
	`!`, `\!`,
	`(`, `\(`,
	`)`, `\)`,
	`{`, `\{`,
	`}`, `\}`,
	`[`, `\[`,
	`]`, `\]`,
	`^`, `\^`,
	`~`, `\~`,
	`*`, `\*`,
	`?`, `\?`,
	`:`, `\:`,
)

func mbEscape(s string) string {
	return luceneEscaper.Replace(s)
}

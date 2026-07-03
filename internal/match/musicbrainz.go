package match

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mbBaseURL   = "https://musicbrainz.org/ws/2"
	mbUserAgent = "hespera/1.0"
)

// rateLimiter enforces a minimum interval between successive calls across all
// holders of the same instance. Safe for concurrent use. The MusicBrainz and
// Cover Art Archive clients share one instance so they stay within a single
// MetaBrainz-family request budget.
type rateLimiter struct {
	mu       sync.Mutex
	last     time.Time
	interval time.Duration
}

func newRateLimiter(interval time.Duration) *rateLimiter {
	return &rateLimiter{interval: interval}
}

func (l *rateLimiter) wait() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if since := time.Since(l.last); since < l.interval {
		time.Sleep(l.interval - since)
	}
	l.last = time.Now()
}

// MBClient queries the MusicBrainz API with a shared 1 req/sec rate limiter.
type MBClient struct {
	client  *http.Client
	baseURL string
	limiter *rateLimiter
	// wikiClient is used by enrichment functions for Wikipedia/Wikidata/Commons.
	// If nil, enrichment functions create their own ad-hoc clients.
	wikiClient      *http.Client
	wikiBaseURL     string // e.g. "https://en.wikipedia.org"
	wikidataBaseURL string // e.g. "https://www.wikidata.org"
	commonsBaseURL  string // e.g. "https://commons.wikimedia.org"
}

func NewMBClient(limiter *rateLimiter) *MBClient {
	return &MBClient{
		client:  &http.Client{Timeout: 15 * time.Second},
		baseURL: mbBaseURL,
		limiter: limiter,
	}
}

func (c *MBClient) throttle() {
	c.limiter.wait()
}

func (c *MBClient) get(ctx context.Context, path string) ([]byte, error) {
	c.throttle()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
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
	ID             string          `json:"id"`
	Title          string          `json:"title"`
	PrimaryType    string          `json:"primary-type"`
	SecondaryTypes []string        `json:"secondary-types"`
	Score          int             `json:"score"`
	FirstRelease   string          `json:"first-release-date"`
	ArtistCredit   []mbArtistEntry `json:"artist-credit"`
	Releases       []mbRelease     `json:"releases"`
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
	SecondaryTypes []string
	Year           int
	MBScore        int
	// Aliases holds the release-group's alternate titles (e.g. a US/UK retitle).
	// Empty unless populated by a LookupReleaseGroupAliases enrichment pass;
	// search responses do not include aliases. Scoring credits the best title
	// match across Title and Aliases so an album filed under one title (MB
	// "Killing Machine") still matches local files tagged with its alias
	// ("Hell Bent for Leather").
	Aliases []string
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
	// Match on the release-group title OR an alias: an album retitled between
	// regions (e.g. UK "Killing Machine" / US "Hell Bent for Leather") is filed
	// under one title with the other as an alias, so a title-only query would
	// never surface it. The alias array isn't returned by search — enrichAliases
	// fetches it later for scoring — but the alias: clause makes the RG a
	// candidate in the first place.
	q := fmt.Sprintf(`(releasegroup:"%s" OR alias:"%s") AND artist:"%s"`, mbEscape(album), mbEscape(album), mbEscape(artist))
	// limit=25 (not 5): the canonical studio release-group is often crowded out
	// of the top results by compilations/EPs/singles of the same title, leaving
	// the scorer unable to reach it. A wider set lets the scorer pick the album.
	path := fmt.Sprintf("/release-group?query=%s&limit=25&fmt=json", url.QueryEscape(q))

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

// ArtistCandidate is a MusicBrainz artist search result, carrying the fields a
// human needs to disambiguate same-named artists (disambiguation comment, type,
// country, life span). Used by the manual artist-disambiguation control.
type ArtistCandidate struct {
	MBID           string
	Name           string
	Disambiguation string
	Type           string // Person / Group / ...
	Country        string
	BeginDate      string
	EndDate        string
	Score          int
}

// SearchArtistCandidates returns up to 10 MusicBrainz artist candidates for a
// name, ordered by MB relevance. Unlike SearchArtist (which blindly takes the
// top result), this surfaces the full set so a user can pick the correct one
// when several artists share a name.
func (c *MBClient) SearchArtistCandidates(ctx context.Context, name string) ([]ArtistCandidate, error) {
	q := fmt.Sprintf(`artist:"%s"`, mbEscape(name))
	path := fmt.Sprintf("/artist?query=%s&limit=10&fmt=json", url.QueryEscape(q))

	body, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}

	var result struct {
		Artists []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			Disambiguation string `json:"disambiguation"`
			Type           string `json:"type"`
			Country        string `json:"country"`
			LifeSpan       struct {
				Begin string `json:"begin"`
				End   string `json:"end"`
			} `json:"life-span"`
			Score int `json:"score"`
		} `json:"artists"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse artist search: %w", err)
	}

	out := make([]ArtistCandidate, 0, len(result.Artists))
	for _, a := range result.Artists {
		out = append(out, ArtistCandidate{
			MBID:           a.ID,
			Name:           a.Name,
			Disambiguation: a.Disambiguation,
			Type:           a.Type,
			Country:        a.Country,
			BeginDate:      a.LifeSpan.Begin,
			EndDate:        a.LifeSpan.End,
			Score:          a.Score,
		})
	}
	return out, nil
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

// ReleaseGroupBrief is a single release-group from an artist's discography (the
// "notable releases" list on the external-artist page).
type ReleaseGroupBrief struct {
	MBID  string
	Title string
	Type  string // primary-type, e.g. "Album"
	Year  int    // from first-release-date; 0 if unknown
	Date  string // raw first-release-date (may be year-only or empty)
}

// BrowseArtistReleaseGroups returns an artist's album release-groups (newest
// first), for the out-of-catalog artist page. Browse (not search) by artist MBID,
// filtered to primary-type Album.
func (c *MBClient) BrowseArtistReleaseGroups(ctx context.Context, artistMBID string) ([]ReleaseGroupBrief, error) {
	path := fmt.Sprintf("/release-group?artist=%s&type=album&limit=50&fmt=json", url.PathEscape(artistMBID))
	body, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	var r struct {
		ReleaseGroups []struct {
			ID               string `json:"id"`
			Title            string `json:"title"`
			PrimaryType      string `json:"primary-type"`
			FirstReleaseDate string `json:"first-release-date"`
		} `json:"release-groups"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parse release-group browse: %w", err)
	}
	out := make([]ReleaseGroupBrief, 0, len(r.ReleaseGroups))
	for _, rg := range r.ReleaseGroups {
		if rg.Title == "" {
			continue
		}
		year := 0
		if len(rg.FirstReleaseDate) >= 4 {
			if y, err := strconv.Atoi(rg.FirstReleaseDate[:4]); err == nil {
				year = y
			}
		}
		out = append(out, ReleaseGroupBrief{MBID: rg.ID, Title: rg.Title, Type: rg.PrimaryType, Year: year, Date: rg.FirstReleaseDate})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Year > out[j].Year })
	return out, nil
}

// LookupReleaseGroupAliases fetches a release-group's alternate titles. Aliases
// are only available from the lookup endpoint (inc=aliases), not from search, so
// this is a separate throttled call used to disambiguate alt-title matches.
func (c *MBClient) LookupReleaseGroupAliases(ctx context.Context, rgid string) ([]string, error) {
	path := fmt.Sprintf("/release-group/%s?inc=aliases&fmt=json", url.PathEscape(rgid))
	body, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	var r struct {
		Aliases []struct {
			Name string `json:"name"`
		} `json:"aliases"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parse release-group aliases: %w", err)
	}
	out := make([]string, 0, len(r.Aliases))
	for _, a := range r.Aliases {
		if a.Name != "" {
			out = append(out, a.Name)
		}
	}
	return out, nil
}

// --- helpers ---

func rgToCandidate(rg MBReleaseGroup) Candidate {
	c := Candidate{
		ReleaseGroupID: rg.ID,
		Title:          rg.Title,
		PrimaryType:    rg.PrimaryType,
		SecondaryTypes: rg.SecondaryTypes,
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

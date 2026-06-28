package match

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hespera/internal/fsutil"
)

// ArtistMeta holds enriched artist metadata from Wikipedia/Wikimedia.
type ArtistMeta struct {
	Bio           string
	BioSourceName string
	BioSourceURL  string
	ImagePath     string // local downloaded path (in-catalog enrichment)
	ImageURL      string // resolved external image URL (set regardless of download)
	Name          string // canonical artist name from MusicBrainz
}

// EnrichArtist fetches artist bio from Wikipedia and image from Wikimedia using
// MusicBrainz URL relations. fanart and audiodb are optional (may be nil)
// backfill providers, tried only when Wikipedia/Wikidata leave a gap: fanart.tv
// for the image, TheAudioDB for the bio (and a last-resort image). Both are
// keyed by the artist MBID, so they stay correct even though album release-group
// MBIDs can mis-match.
func EnrichArtist(ctx context.Context, mb *MBClient, fanart *FanartClient, audiodb *AudioDBClient, artistMBID, dataDir string) (*ArtistMeta, error) {
	return enrichArtist(ctx, mb, fanart, audiodb, artistMBID, dataDir, true)
}

// enrichArtist is the shared body of EnrichArtist. download controls whether the
// resolved image is fetched to disk (ImagePath, the in-catalog case) — when
// false, only the external ImageURL is resolved (the out-of-catalog page
// hotlinks it, so it must not land under thumbs/music where the GC would reap it).
func enrichArtist(ctx context.Context, mb *MBClient, fanart *FanartClient, audiodb *AudioDBClient, artistMBID, dataDir string, download bool) (*ArtistMeta, error) {
	artist, err := mb.LookupArtist(ctx, artistMBID)
	if err != nil {
		return nil, fmt.Errorf("lookup artist: %w", err)
	}

	var wikiURL, wikidataURL string
	for _, rel := range artist.Relations {
		res := rel.URL.Resource
		switch {
		case strings.Contains(res, "wikipedia.org/wiki/"):
			wikiURL = res
		case mb.wikiBaseURL != "" && rel.Type == "wikipedia":
			wikiURL = res
		}
		switch {
		case strings.Contains(res, "wikidata.org/"):
			wikidataURL = res
		case mb.wikidataBaseURL != "" && rel.Type == "wikidata":
			wikidataURL = res
		}
	}

	meta := &ArtistMeta{Name: artist.Name}

	// If we have a Wikidata URL, fetch the entity once and extract both
	// the Wikipedia sitelink (for bio) and P18 image claim.
	var wikidataEntity []byte
	var wikidataQID string
	if wikidataURL != "" {
		wikidataQID = extractQID(wikidataURL)
		if wikidataQID != "" {
			body, err := fetchWikidataEntity(ctx, wikidataQID, mb.wikiClient, mb.wikidataBaseURL)
			if err != nil {
				slog.Warn("wikidata entity fetch failed", "qid", wikidataQID, "err", err)
			} else {
				wikidataEntity = body
			}
		}
	}

	// Bio from Wikipedia (direct link, or derived from Wikidata sitelink).
	if wikiURL != "" {
		bio, err := fetchWikipediaSummary(ctx, wikiURL, mb.wikiClient, mb.wikiBaseURL)
		if err != nil {
			slog.Warn("wikipedia bio fetch failed", "url", wikiURL, "err", err)
		} else if bio != "" {
			meta.Bio = bio
			meta.BioSourceName = "Wikipedia"
			meta.BioSourceURL = wikiURL
		}
	} else if len(wikidataEntity) > 0 {
		derivedURL := extractEnwikiURL(wikidataEntity, wikidataQID)
		if derivedURL != "" {
			bio, err := fetchWikipediaSummary(ctx, derivedURL, mb.wikiClient, mb.wikiBaseURL)
			if err != nil {
				slog.Warn("wikipedia bio fetch failed", "url", derivedURL, "err", err)
			} else if bio != "" {
				meta.Bio = bio
				meta.BioSourceName = "Wikipedia"
				meta.BioSourceURL = derivedURL
			}
		}
	}

	// Resolve an image URL in priority order — Wikidata P18 → Wikimedia Commons,
	// then fanart.tv, then TheAudioDB — capturing the download client each source
	// needs (Commons redirects to upload.wikimedia.org, so it uses the wiki client
	// with the redirect SSRF re-check; the direct provider URLs use the default).
	var imgClient *http.Client
	if len(wikidataEntity) > 0 {
		filename := extractP18(wikidataEntity, wikidataQID)
		if filename != "" {
			slog.Info("P18 image found", "qid", wikidataQID, "filename", filename)
			commonsBase := "https://commons.wikimedia.org"
			if mb.commonsBaseURL != "" {
				commonsBase = mb.commonsBaseURL
			}
			meta.ImageURL = fmt.Sprintf("%s/wiki/Special:FilePath/%s?width=500",
				commonsBase, url.PathEscape(filename))
			imgClient = mb.wikiClient
		} else {
			slog.Info("no P18 image claim", "qid", wikidataQID)
		}
	}
	if meta.ImageURL == "" && fanart != nil {
		if u := fanart.ArtistImageURL(ctx, artistMBID); u != "" {
			meta.ImageURL = u
			imgClient = nil
		}
	}

	// Bio backfill: TheAudioDB, only where Wikipedia/Wikidata left a gap.
	if audiodb != nil {
		if meta.Bio == "" {
			if bio := audiodb.ArtistBio(ctx, artistMBID); bio != "" {
				meta.Bio = bio
				meta.BioSourceName = "TheAudioDB"
				meta.BioSourceURL = "https://www.theaudiodb.com/artist/" + artistMBID
			}
		}
		if meta.ImageURL == "" {
			if u := audiodb.ArtistImageURL(ctx, artistMBID); u != "" {
				meta.ImageURL = u
				imgClient = nil
			}
		}
	}

	// Download to disk only for in-catalog enrichment; the out-of-catalog page
	// hotlinks meta.ImageURL instead.
	if download && meta.ImageURL != "" {
		if p, err := downloadArtistImage(ctx, meta.ImageURL, dataDir, artistMBID, imgClient); err != nil {
			slog.Warn("artist image download failed", "mbid", artistMBID, "err", err)
		} else if p != "" {
			meta.ImagePath = p
		}
	}

	return meta, nil
}

func fetchWikipediaSummary(ctx context.Context, wikiURL string, optClient *http.Client, baseOverride string) (string, error) {
	// Extract the article title from the URL.
	// e.g. https://en.wikipedia.org/wiki/The_Beatles -> The_Beatles
	parts := strings.SplitN(wikiURL, "/wiki/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", fmt.Errorf("cannot parse wiki URL: %s", wikiURL)
	}
	title := parts[1]

	var apiURL string
	if baseOverride != "" {
		// Use the override base URL (for testing).
		apiURL = fmt.Sprintf("%s/api/rest_v1/page/summary/%s", baseOverride, title)
	} else {
		// Determine the language subdomain.
		lang := "en"
		if u, err := url.Parse(wikiURL); err == nil {
			host := u.Hostname()
			if idx := strings.IndexByte(host, '.'); idx > 0 {
				lang = host[:idx]
			}
		}
		apiURL = fmt.Sprintf("https://%s.wikipedia.org/api/rest_v1/page/summary/%s", lang, title)
	}

	client := optClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", mbUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("wikipedia HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return "", err
	}

	var result struct {
		Extract string `json:"extract"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Extract), nil
}

// fetchWikidataEntity fetches the JSON entity data for a Wikidata QID.
func fetchWikidataEntity(ctx context.Context, qid string, optClient *http.Client, baseOverride string) ([]byte, error) {
	base := "https://www.wikidata.org"
	if baseOverride != "" {
		base = baseOverride
	}
	entityURL := fmt.Sprintf("%s/wiki/Special:EntityData/%s.json", base, qid)
	client := optClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, entityURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", mbUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wikidata fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wikidata HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, fmt.Errorf("wikidata read body: %w", err)
	}
	slog.Info("wikidata entity fetched", "qid", qid, "body_len", len(body))
	return body, nil
}

// extractEnwikiURL extracts the English Wikipedia URL from a Wikidata entity JSON blob.
func extractEnwikiURL(data []byte, qid string) string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	entitiesRaw, ok := raw["entities"]
	if !ok {
		return ""
	}
	var entities map[string]json.RawMessage
	if err := json.Unmarshal(entitiesRaw, &entities); err != nil {
		return ""
	}
	entityRaw, ok := entities[qid]
	if !ok {
		entityRaw, ok = entities[strings.ToUpper(qid)]
		if !ok {
			return ""
		}
	}

	var entity struct {
		Sitelinks map[string]struct {
			Title string `json:"title"`
		} `json:"sitelinks"`
	}
	if err := json.Unmarshal(entityRaw, &entity); err != nil {
		return ""
	}

	enwiki, ok := entity.Sitelinks["enwiki"]
	if !ok || enwiki.Title == "" {
		return ""
	}
	return "https://en.wikipedia.org/wiki/" + url.PathEscape(strings.ReplaceAll(enwiki.Title, " ", "_"))
}

func extractQID(wikidataURL string) string {
	parts := strings.Split(wikidataURL, "/")
	for _, p := range parts {
		if len(p) > 1 && (p[0] == 'Q' || p[0] == 'q') {
			return p
		}
	}
	return ""
}

func extractP18(data []byte, qid string) string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}

	var entities map[string]json.RawMessage
	entitiesRaw, ok := raw["entities"]
	if !ok {
		return ""
	}
	if err := json.Unmarshal(entitiesRaw, &entities); err != nil {
		return ""
	}

	entityRaw, ok := entities[qid]
	if !ok {
		entityRaw, ok = entities[strings.ToUpper(qid)]
		if !ok {
			return ""
		}
	}

	// Parse only the claims map with raw values, because different claims
	// have different value types (string, object, etc.) that would break
	// a single typed struct.
	var entity struct {
		Claims map[string][]struct {
			Mainsnak struct {
				Datavalue struct {
					Value json.RawMessage `json:"value"`
					Type  string          `json:"type"`
				} `json:"datavalue"`
			} `json:"mainsnak"`
		} `json:"claims"`
	}
	if err := json.Unmarshal(entityRaw, &entity); err != nil {
		return ""
	}

	p18Claims := entity.Claims["P18"]
	if len(p18Claims) == 0 {
		return ""
	}

	// P18 is a "string" type claim — unquote the JSON string value.
	var filename string
	if err := json.Unmarshal(p18Claims[0].Mainsnak.Datavalue.Value, &filename); err != nil {
		return ""
	}
	return filename
}

// imageURLGuard validates an image-download URL before fetch. It's a package var
// (defaulting to the real SSRF guard) so integration tests that serve art from an
// httptest server — http on loopback, which the guard rejects — can relax it.
// Production never reassigns it.
var imageURLGuard = requirePublicHTTPS

// requirePublicHTTPS guards image downloads driven by third-party API responses
// (fanart.tv/TheAudioDB/Wikidata), which could otherwise point the server at an
// internal address — SSRF. It requires https and rejects any host that resolves to
// a private/loopback/link-local address. (DNS rebinding between this check and the
// dial is a residual TOCTOU; the realistic threat is a provider returning an
// internal URL, which this closes — and the caller re-checks each redirect hop.)
func requirePublicHTTPS(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("refusing non-https image url (scheme %q)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("image url has no host")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", host, err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("refusing image url resolving to non-public address %s", ip)
		}
	}
	return nil
}

func downloadArtistImage(ctx context.Context, imgURL, dataDir, artistMBID string, optClient *http.Client) (string, error) {
	thumbDir := filepath.Join(dataDir, "thumbs", "music")
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		return "", err
	}

	if err := imageURLGuard(imgURL); err != nil {
		return "", err
	}

	// Wrap the (possibly shared) client with a redirect guard without mutating it,
	// preserving its transport/timeout. FilePath legitimately redirects (to
	// upload.wikimedia.org), so we re-validate each hop rather than refuse redirects.
	src := optClient
	if src == nil {
		src = &http.Client{Timeout: 30 * time.Second}
	}
	client := &http.Client{
		Timeout:   src.Timeout,
		Transport: src.Transport,
		Jar:       src.Jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return imageURLGuard(req.URL.String())
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imgURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", mbUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("wikimedia HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 15<<20))
	if err != nil {
		return "", err
	}

	ext := extFromContentType(resp.Header.Get("Content-Type"))
	if ext == "" {
		ext = ".jpg"
	}

	h := sha1.Sum([]byte("artist-" + artistMBID))
	name := hex.EncodeToString(h[:]) + ext
	outPath := filepath.Join(thumbDir, name)

	if err := fsutil.WriteFileAtomic(outPath, data, 0o644); err != nil {
		return "", err
	}
	return outPath, nil
}

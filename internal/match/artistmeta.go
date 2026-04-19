package match

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ArtistMeta holds enriched artist metadata from Wikipedia/Wikimedia.
type ArtistMeta struct {
	Bio           string
	BioSourceName string
	BioSourceURL  string
	ImagePath     string
}

// EnrichArtist fetches artist bio from Wikipedia and image from Wikimedia
// using MusicBrainz URL relations to find the right Wikipedia/Wikidata pages.
func EnrichArtist(ctx context.Context, mb *MBClient, artistMBID, dataDir string) (*ArtistMeta, error) {
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

	meta := &ArtistMeta{}

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

	// Image from Wikidata P18 -> Wikimedia Commons.
	if len(wikidataEntity) > 0 {
		filename := extractP18(wikidataEntity, wikidataQID)
		if filename != "" {
			slog.Info("P18 image found", "qid", wikidataQID, "filename", filename)
			commonsBase := "https://commons.wikimedia.org"
			if mb.commonsBaseURL != "" {
				commonsBase = mb.commonsBaseURL
			}
			commonsURL := fmt.Sprintf("%s/wiki/Special:FilePath/%s?width=500",
				commonsBase, url.PathEscape(filename))
			imgPath, err := downloadArtistImage(ctx, commonsURL, dataDir, artistMBID, mb.wikiClient)
			if err != nil {
				slog.Warn("artist image download failed", "qid", wikidataQID, "err", err)
			} else if imgPath != "" {
				meta.ImagePath = imgPath
			}
		} else {
			slog.Info("no P18 image claim", "qid", wikidataQID)
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

func downloadArtistImage(ctx context.Context, imgURL, dataDir, artistMBID string, optClient *http.Client) (string, error) {
	thumbDir := filepath.Join(dataDir, "thumbs", "music")
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		return "", err
	}

	client := optClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
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

	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return "", err
	}
	return outPath, nil
}

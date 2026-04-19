package match

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
		if strings.Contains(res, "wikipedia.org/wiki/") {
			wikiURL = res
		}
		if strings.Contains(res, "wikidata.org/") {
			wikidataURL = res
		}
	}

	meta := &ArtistMeta{}

	// Bio from Wikipedia.
	if wikiURL != "" {
		bio, err := fetchWikipediaSummary(ctx, wikiURL)
		if err == nil && bio != "" {
			meta.Bio = bio
			meta.BioSourceName = "Wikipedia"
			meta.BioSourceURL = wikiURL
		}
	}

	// Image from Wikidata -> Wikimedia Commons.
	if wikidataURL != "" {
		imgPath, err := fetchWikidataImage(ctx, wikidataURL, dataDir, artistMBID)
		if err == nil && imgPath != "" {
			meta.ImagePath = imgPath
		}
	}

	return meta, nil
}

func fetchWikipediaSummary(ctx context.Context, wikiURL string) (string, error) {
	// Extract the article title from the URL.
	// e.g. https://en.wikipedia.org/wiki/The_Beatles -> The_Beatles
	parts := strings.SplitN(wikiURL, "/wiki/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", fmt.Errorf("cannot parse wiki URL: %s", wikiURL)
	}
	title := parts[1]

	// Determine the language subdomain.
	lang := "en"
	if u, err := url.Parse(wikiURL); err == nil {
		host := u.Hostname()
		if idx := strings.IndexByte(host, '.'); idx > 0 {
			lang = host[:idx]
		}
	}

	apiURL := fmt.Sprintf("https://%s.wikipedia.org/api/rest_v1/page/summary/%s", lang, title)
	client := &http.Client{Timeout: 10 * time.Second}
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

func fetchWikidataImage(ctx context.Context, wikidataURL, dataDir, artistMBID string) (string, error) {
	// Extract Qid from URL: https://www.wikidata.org/wiki/Q1299 -> Q1299
	qid := ""
	parts := strings.Split(wikidataURL, "/")
	for _, p := range parts {
		if len(p) > 1 && (p[0] == 'Q' || p[0] == 'q') {
			qid = p
			break
		}
	}
	if qid == "" {
		return "", fmt.Errorf("no Qid in wikidata URL: %s", wikidataURL)
	}

	// Fetch entity data to get P18 (image) claim.
	entityURL := fmt.Sprintf("https://www.wikidata.org/wiki/Special:EntityData/%s.json", qid)
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, entityURL, nil)
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
		return "", fmt.Errorf("wikidata HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}

	filename := extractP18(body, qid)
	if filename == "" {
		return "", nil
	}

	// Download from Wikimedia Commons.
	commonsURL := fmt.Sprintf("https://commons.wikimedia.org/wiki/Special:FilePath/%s?width=500", url.PathEscape(filename))
	return downloadArtistImage(ctx, commonsURL, dataDir, artistMBID)
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
		// Try uppercase.
		entityRaw, ok = entities[strings.ToUpper(qid)]
		if !ok {
			return ""
		}
	}

	var entity struct {
		Claims map[string][]struct {
			Mainsnak struct {
				Datavalue struct {
					Value string `json:"value"`
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
	return p18Claims[0].Mainsnak.Datavalue.Value
}

func downloadArtistImage(ctx context.Context, imgURL, dataDir, artistMBID string) (string, error) {
	thumbDir := filepath.Join(dataDir, "thumbs", "music")
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		return "", err
	}

	client := &http.Client{Timeout: 30 * time.Second}
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

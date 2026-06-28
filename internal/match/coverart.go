package match

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hespera/internal/fsutil"
)

const caaBaseURL = "https://coverartarchive.org"

// CAAClient fetches cover art from the Cover Art Archive.
type CAAClient struct {
	client   *http.Client
	baseURL  string
	thumbDir string
	limiter  *rateLimiter
}

func NewCAAClient(dataDir string, limiter *rateLimiter) *CAAClient {
	return &CAAClient{
		client:   &http.Client{Timeout: 30 * time.Second},
		baseURL:  caaBaseURL,
		thumbDir: filepath.Join(dataDir, "thumbs", "music"),
		limiter:  limiter,
	}
}

type caaResponse struct {
	Images []caaImage `json:"images"`
}

type caaImage struct {
	Types      []string      `json:"types"`
	Front      bool          `json:"front"`
	Thumbnails caaThumbnails `json:"thumbnails"`
	Image      string        `json:"image"`
}

type caaThumbnails struct {
	Large string `json:"large"`
	Small string `json:"small"`
	T500  string `json:"500"`
	T250  string `json:"250"`
	T1200 string `json:"1200"`
}

// FetchCover tries to download cover art for a release group, falling back to
// individual releases. Returns the saved file path or empty string.
func (c *CAAClient) FetchCover(ctx context.Context, releaseGroupID string, releaseIDs []string) (string, error) {
	if err := os.MkdirAll(c.thumbDir, 0o755); err != nil {
		return "", err
	}

	// Try release-group first.
	if releaseGroupID != "" {
		imgURL, err := c.findCoverURL(ctx, fmt.Sprintf("%s/release-group/%s", c.baseURL, releaseGroupID))
		if err == nil && imgURL != "" {
			return c.downloadAndSave(ctx, imgURL, releaseGroupID)
		}
	}

	// Fallback: try linked releases (max 3).
	for i, rid := range releaseIDs {
		if i >= 3 {
			break
		}
		imgURL, err := c.findCoverURL(ctx, fmt.Sprintf("%s/release/%s", c.baseURL, rid))
		if err == nil && imgURL != "" {
			key := releaseGroupID
			if key == "" {
				key = rid
			}
			return c.downloadAndSave(ctx, imgURL, key)
		}
	}

	return "", nil
}

func (c *CAAClient) findCoverURL(ctx context.Context, endpoint string) (string, error) {
	c.limiter.wait()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", mbUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("CAA HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	var result caaResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}

	return pickBestImage(result.Images), nil
}

func pickBestImage(images []caaImage) string {
	// Prefer front cover.
	var front *caaImage
	for i := range images {
		if images[i].Front {
			front = &images[i]
			break
		}
		for _, t := range images[i].Types {
			if strings.EqualFold(t, "Front") {
				front = &images[i]
				break
			}
		}
		if front != nil {
			break
		}
	}

	img := front
	if img == nil && len(images) > 0 {
		img = &images[0]
	}
	if img == nil {
		return ""
	}

	// Prefer largest thumbnail: Large > 500 > 250 > Small > full image.
	if img.Thumbnails.Large != "" {
		return img.Thumbnails.Large
	}
	if img.Thumbnails.T500 != "" {
		return img.Thumbnails.T500
	}
	if img.Thumbnails.T250 != "" {
		return img.Thumbnails.T250
	}
	if img.Thumbnails.Small != "" {
		return img.Thumbnails.Small
	}
	return img.Image
}

func (c *CAAClient) downloadAndSave(ctx context.Context, imgURL, hashKey string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imgURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", mbUserAgent)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 15<<20))
	if err != nil {
		return "", err
	}

	ext := extFromURL(imgURL)
	if ext == "" {
		ext = extFromContentType(resp.Header.Get("Content-Type"))
	}
	if ext == "" {
		ext = ".jpg"
	}

	h := sha1.Sum([]byte("caa-" + hashKey))
	name := hex.EncodeToString(h[:]) + ext
	outPath := filepath.Join(c.thumbDir, name)

	if err := fsutil.WriteFileAtomic(outPath, data, 0o644); err != nil {
		return "", err
	}
	return outPath, nil
}

func extFromURL(u string) string {
	// Strip query parameters.
	if idx := strings.IndexByte(u, '?'); idx >= 0 {
		u = u[:idx]
	}
	ext := filepath.Ext(u)
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return ext
	}
	return ""
}

func extFromContentType(ct string) string {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
		return ".jpg"
	}
	return ""
}

package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"hespera/internal/opensubtitles"
	"hespera/internal/video"
)

// langPattern validates an ISO-ish language code used in cache file names and
// queries — lowercase letters with an optional region suffix (en, pt-br, zh-cn).
var langPattern = regexp.MustCompile(`^[a-z]{2,3}(-[a-z]{2,4})?$`)

// tvSubtitlesCacheRoot is where downloaded+converted external subtitles live,
// under the data dir (not /tmp). Keyed by the OpenSubtitles file id + language
// so re-watching a file reuses the cached WebVTT and never spends quota twice.
func (h *Handler) tvSubtitlesCacheRoot() string {
	return filepath.Join(h.cfg.DataDir, "subtitles")
}

// tvIdentityForSubtitles loads the TMDB series id + season + episode for a TV
// file, so an OpenSubtitles search is keyed off the ids we already stored rather
// than a fuzzy title query.
func (h *Handler) tvIdentityForSubtitles(r *http.Request, fileID int64) (seriesID string, season, episode int, ok bool) {
	var epCSV string
	err := h.db.QueryRowContext(r.Context(), `
SELECT i.series_id, i.season_number, i.episode_numbers_csv
FROM tv_series_identities i
WHERE i.file_id = ? AND i.status = 'matched'
`, fileID).Scan(&seriesID, &season, &epCSV)
	if err != nil || strings.TrimSpace(seriesID) == "" || season < 0 {
		return "", 0, 0, false
	}
	episode = firstEpNum(epCSV)
	if episode <= 0 || episode >= 1<<30 {
		return "", 0, 0, false
	}
	return seriesID, season, episode, true
}

// tvSubtitlesSearch finds candidate subtitles for a TV file on OpenSubtitles.
// GET /tv/subtitles/search?file=<id>&lang=<code>
func (h *Handler) tvSubtitlesSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("file")), 10, 64)
	if err != nil || fileID <= 0 {
		jsonError(w, "invalid file id", http.StatusBadRequest)
		return
	}
	lang := normalizeLang(r.URL.Query().Get("lang"))

	client := opensubtitles.New(h.effectiveOpenSubtitlesKey(r.Context()))
	if client == nil {
		jsonError(w, "subtitle search is not configured (set an OpenSubtitles API key in Settings)", http.StatusServiceUnavailable)
		return
	}

	seriesID, season, episode, ok := h.tvIdentityForSubtitles(r, fileID)
	if !ok {
		jsonError(w, "this file is not matched to a TMDB episode", http.StatusNotFound)
		return
	}

	results, err := client.Search(r.Context(), seriesID, season, episode, lang)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "subtitle search failed", "opensubtitles search", "handler", "tvSubtitlesSearch", "file_id", fileID, "err", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "results": results})
}

// tvSubtitlesFetch downloads a chosen subtitle, converts it to WebVTT, caches it,
// and returns a URL the player can attach. Cache-first, so a re-watch costs no
// quota. POST /tv/subtitles/fetch (form: file, file_id, lang)
func (h *Handler) tvSubtitlesFetch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}
	osFileID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("file_id")), 10, 64)
	if err != nil || osFileID <= 0 {
		jsonError(w, "invalid file_id", http.StatusBadRequest)
		return
	}
	lang := normalizeLang(r.FormValue("lang"))

	cachePath := h.subtitleCachePath(osFileID, lang)
	// Cache-first: a previously fetched+converted file is reused without spending
	// any download quota.
	if _, statErr := os.Stat(cachePath); statErr == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": subtitleGetURL(osFileID, lang)})
		return
	}

	client := opensubtitles.New(h.effectiveOpenSubtitlesKey(r.Context()))
	if client == nil {
		jsonError(w, "subtitle download is not configured", http.StatusServiceUnavailable)
		return
	}

	link, err := client.Download(r.Context(), osFileID)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "subtitle download failed", "opensubtitles download", "handler", "tvSubtitlesFetch", "os_file_id", osFileID, "err", err)
		return
	}
	// SSRF guard: only ever fetch the link OpenSubtitles handed back, and only if
	// it points at opensubtitles.com — never an arbitrary client-supplied URL.
	if !isOpenSubtitlesHost(link) {
		jsonErr(w, http.StatusBadGateway, "subtitle download failed", "download link host rejected", "handler", "tvSubtitlesFetch", "link", link)
		return
	}

	if err := h.cacheConvertedSubtitle(r, link, cachePath); err != nil {
		jsonErr(w, http.StatusBadGateway, "subtitle conversion failed", "fetch+convert subtitle", "handler", "tvSubtitlesFetch", "os_file_id", osFileID, "err", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": subtitleGetURL(osFileID, lang)})
}

// tvSubtitlesGet serves a cached, converted WebVTT subtitle.
// GET /tv/subtitles/get?file_id=<id>&lang=<code>
func (h *Handler) tvSubtitlesGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	osFileID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("file_id")), 10, 64)
	if err != nil || osFileID <= 0 {
		http.NotFound(w, r)
		return
	}
	lang := normalizeLang(r.URL.Query().Get("lang"))
	path := h.subtitleCachePath(osFileID, lang)
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filepath.Base(path), fi.ModTime(), f)
}

// cacheConvertedSubtitle fetches the subtitle from link, converts it to WebVTT
// via ffmpeg, and writes it atomically to cachePath (temp + rename so a partial
// file is never served).
func (h *Handler) cacheConvertedSubtitle(r *http.Request, link, cachePath string) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}

	// Download the raw subtitle to a temp file ffmpeg can read.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, link, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download link: status %d", resp.StatusCode)
	}
	rawFile, err := os.CreateTemp(filepath.Dir(cachePath), "sub-*.raw")
	if err != nil {
		return err
	}
	rawPath := rawFile.Name()
	defer os.Remove(rawPath)
	if _, err := io.Copy(rawFile, io.LimitReader(resp.Body, 10<<20)); err != nil {
		rawFile.Close()
		return err
	}
	rawFile.Close()

	// Convert to WebVTT into a temp file, then atomically rename into place.
	tmpVTT, err := os.CreateTemp(filepath.Dir(cachePath), "sub-*.vtt")
	if err != nil {
		return err
	}
	tmpPath := tmpVTT.Name()
	// ffmpeg writes its converted output to this file handle (pipe:1 → w).
	// Note: non-UTF-8 source encodings (latin1/cp1251) may render garbled; a
	// future enhancement is charset detection via -sub_charenc (see pending.md).
	args := []string{"-hide_banner", "-loglevel", "error", "-i", rawPath, "-f", "webvtt", "pipe:1"}
	convErr := video.StreamFFmpeg(r.Context(), tmpVTT, args)
	tmpVTT.Close()
	if convErr != nil {
		os.Remove(tmpPath)
		return convErr
	}
	if err := os.Rename(tmpPath, cachePath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

func (h *Handler) subtitleCachePath(osFileID int64, lang string) string {
	return filepath.Join(h.tvSubtitlesCacheRoot(), fmt.Sprintf("%d.%s.vtt", osFileID, lang))
}

func subtitleGetURL(osFileID int64, lang string) string {
	return fmt.Sprintf("/tv/subtitles/get?file_id=%d&lang=%s", osFileID, url.QueryEscape(lang))
}

// normalizeLang lowercases and validates a language code, defaulting to "en".
func normalizeLang(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if !langPattern.MatchString(s) {
		return "en"
	}
	return s
}

// isOpenSubtitlesHost reports whether a URL's host is opensubtitles.com (or a
// subdomain of it) over https — the SSRF allowlist for download links.
func isOpenSubtitlesHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "opensubtitles.com" || strings.HasSuffix(host, ".opensubtitles.com")
}

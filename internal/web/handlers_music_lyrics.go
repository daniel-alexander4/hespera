package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Lyrics are fetched lazily, per track, from LRCLIB (https://lrclib.net) — a
// free, key-less provider returning both plain and synced (LRC) lyrics. Results
// (hits and misses) are cached in lyrics_cache so a track is fetched at most
// once. Gated by the `lyrics_enabled` app-setting (default OFF, opt-in via
// Settings → API Keys): when off this endpoint returns immediately and the
// now-playing page hides the lyrics card so the cover expands into the space.
// The gate governs *automatic* fetching; the transport's per-song Lyrics
// toggle sends force=1 — an explicit user gesture that opts one track in even
// while the global default is off (still cache-first).

const (
	lyricsProviderKey = "lrclib"
	lyricsUserAgent   = "hespera/1.0"
)

// lrcLibBaseURL is a var so tests can point it at a fake server.
var lrcLibBaseURL = "https://lrclib.net"

var lrcLibHTTPClient = &http.Client{Timeout: 12 * time.Second}

type lrcLibCandidate struct {
	ID           int64   `json:"id"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

type lyricsCacheRow struct {
	TrackID         int64
	LyricsText      string
	SyncedLyrics    string
	HasSynced       bool
	ProviderTrackID int64
	MatchTrack      string
	MatchArtist     string
	MatchAlbum      string
	FetchedAt       string
}

func (h *Handler) musicLyricsFetch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeLyricsJSON(w, http.StatusBadRequest, false, "invalid request", nil)
		return
	}
	// Global opt-in gate (default off) for *automatic* fetches — no LRCLIB call
	// happens when lyrics are disabled. force=1 is the per-song toggle's
	// explicit user gesture, which opts this one track in past the default.
	if !h.effectiveLyricsEnabled(r.Context()) && r.FormValue("force") != "1" {
		writeLyricsJSON(w, http.StatusOK, true, "lyrics disabled", nil)
		return
	}
	trackID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("track_id")), 10, 64)
	if err != nil || trackID <= 0 {
		writeLyricsJSON(w, http.StatusBadRequest, false, "invalid track_id", nil)
		return
	}

	// Cache-first: any existing row (hit or known miss) is authoritative, so a
	// track is never re-fetched.
	cache, err := h.loadLyricsCacheRow(r.Context(), trackID)
	if err != nil {
		writeLyricsJSON(w, http.StatusInternalServerError, false, "failed to load cache", nil)
		return
	}
	if cache != nil {
		writeLyricsJSON(w, http.StatusOK, true, "ok", lyricsPayload(trackID, true, cache.HasSynced, cache.LyricsText, cache.SyncedLyrics))
		return
	}

	title, artist, album, err := h.loadTrackLyricsLookupInput(r.Context(), trackID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeLyricsJSON(w, http.StatusNotFound, false, "track not found", nil)
			return
		}
		writeLyricsJSON(w, http.StatusInternalServerError, false, "failed to load track details", nil)
		return
	}
	if strings.TrimSpace(title) == "" || strings.TrimSpace(artist) == "" {
		writeLyricsJSON(w, http.StatusBadRequest, false, "track metadata is missing title/artist", nil)
		return
	}

	// Offline gate — after the cache lookup above, so already-fetched lyrics
	// keep rendering; it outranks force=1 (the per-song opt-in is an opt-in
	// past the lyrics default, not past "make no external calls").
	if !h.effectiveExternalMetadataEnabled(r.Context()) {
		writeLyricsJSON(w, http.StatusOK, true, "external metadata disabled", nil)
		return
	}

	candidate, err := fetchBestLrcLibLyrics(r.Context(), title, artist, album)
	if err != nil {
		writeLyricsJSON(w, http.StatusBadGateway, false, "lyrics lookup failed: "+err.Error(), nil)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	row := lyricsCacheRow{TrackID: trackID, MatchTrack: title, MatchArtist: artist, MatchAlbum: album, FetchedAt: now}
	if candidate != nil {
		row.LyricsText = strings.TrimSpace(candidate.PlainLyrics)
		row.SyncedLyrics = strings.TrimSpace(candidate.SyncedLyrics)
		row.HasSynced = row.SyncedLyrics != ""
		if row.LyricsText == "" {
			row.LyricsText = row.SyncedLyrics
		}
		row.ProviderTrackID = candidate.ID
		row.MatchTrack = strings.TrimSpace(candidate.TrackName)
		row.MatchArtist = strings.TrimSpace(candidate.ArtistName)
		row.MatchAlbum = strings.TrimSpace(candidate.AlbumName)
	}
	// Cache even a miss (empty row) so we don't re-query the provider.
	if err := h.upsertLyricsCacheRow(r.Context(), row); err != nil {
		writeLyricsJSON(w, http.StatusInternalServerError, false, "failed to save lyrics cache", nil)
		return
	}
	writeLyricsJSON(w, http.StatusOK, true, "ok", lyricsPayload(trackID, false, row.HasSynced, row.LyricsText, row.SyncedLyrics))
}

func lyricsPayload(trackID int64, cached, synced bool, lyrics, syncedLyrics string) map[string]any {
	return map[string]any{
		"track_id":      trackID,
		"provider":      lyricsProviderKey,
		"cached":        cached,
		"synced":        synced,
		"lyrics":        lyrics,
		"synced_lyrics": syncedLyrics,
	}
}

func (h *Handler) loadLyricsCacheRow(ctx context.Context, trackID int64) (*lyricsCacheRow, error) {
	var row lyricsCacheRow
	var hasSynced int
	err := h.db.QueryRowContext(ctx, `
SELECT track_id, lyrics_text, synced_lyrics, has_synced, provider_track_id, match_track, match_artist, match_album, fetched_at
FROM lyrics_cache WHERE track_id=? AND provider_key=?
`, trackID, lyricsProviderKey).Scan(
		&row.TrackID, &row.LyricsText, &row.SyncedLyrics, &hasSynced, &row.ProviderTrackID,
		&row.MatchTrack, &row.MatchArtist, &row.MatchAlbum, &row.FetchedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	row.HasSynced = hasSynced != 0
	return &row, nil
}

func (h *Handler) upsertLyricsCacheRow(ctx context.Context, row lyricsCacheRow) error {
	hasSynced := 0
	if row.HasSynced {
		hasSynced = 1
	}
	_, err := h.db.ExecContext(ctx, `
INSERT INTO lyrics_cache (
  track_id, provider_key, lyrics_text, synced_lyrics, has_synced, provider_track_id, match_track, match_artist, match_album, fetched_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(track_id, provider_key) DO UPDATE SET
  lyrics_text=excluded.lyrics_text,
  synced_lyrics=excluded.synced_lyrics,
  has_synced=excluded.has_synced,
  provider_track_id=excluded.provider_track_id,
  match_track=excluded.match_track,
  match_artist=excluded.match_artist,
  match_album=excluded.match_album,
  fetched_at=excluded.fetched_at,
  updated_at=excluded.updated_at
`, row.TrackID, lyricsProviderKey, row.LyricsText, row.SyncedLyrics, hasSynced, row.ProviderTrackID,
		row.MatchTrack, row.MatchArtist, row.MatchAlbum, row.FetchedAt, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (h *Handler) loadTrackLyricsLookupInput(ctx context.Context, trackID int64) (title, artist, album string, err error) {
	err = h.db.QueryRowContext(ctx, `
SELECT t.title, COALESCE(ar.name, ''), COALESCE(al.title, '')
FROM music_tracks t
JOIN music_artists ar ON ar.id=t.artist_id
JOIN music_albums al ON al.id=t.album_id
WHERE t.id=?
`, trackID).Scan(&title, &artist, &album)
	return title, artist, album, err
}

func writeLyricsJSON(w http.ResponseWriter, status int, ok bool, message string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": ok, "message": message, "data": data})
}

// fetchBestLrcLibLyrics tries the exact-match endpoint first, then falls back to
// fuzzy search scored by pickBestLrcLibCandidate.
func fetchBestLrcLibLyrics(ctx context.Context, title, artist, album string) (*lrcLibCandidate, error) {
	if got, err := fetchLrcLibGet(ctx, title, artist, album); err != nil {
		return nil, err
	} else if got != nil {
		return got, nil
	}

	candidates := make([]lrcLibCandidate, 0, 12)
	if c, err := fetchLrcLibSearch(ctx, map[string]string{"track_name": title, "artist_name": artist, "album_name": album}); err == nil {
		candidates = append(candidates, c...)
	}
	if q := strings.TrimSpace(artist + " " + title); q != "" {
		if c, err := fetchLrcLibSearch(ctx, map[string]string{"q": q}); err == nil {
			candidates = append(candidates, c...)
		}
	}
	return pickBestLrcLibCandidate(candidates, title, artist, album), nil
}

func fetchLrcLibGet(ctx context.Context, title, artist, album string) (*lrcLibCandidate, error) {
	params := map[string]string{"track_name": title, "artist_name": artist, "album_name": album}
	for _, ep := range []string{"/api/get", "/get"} {
		cand, status, err := fetchLrcLibOne(ctx, ep, params)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNotFound {
			continue
		}
		if status >= 200 && status < 300 && cand != nil {
			return cand, nil
		}
	}
	return nil, nil
}

func fetchLrcLibSearch(ctx context.Context, params map[string]string) ([]lrcLibCandidate, error) {
	for _, ep := range []string{"/api/search", "/search"} {
		items, status, err := fetchLrcLibList(ctx, ep, params)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNotFound {
			continue
		}
		if status >= 200 && status < 300 {
			return items, nil
		}
	}
	return nil, nil
}

func fetchLrcLibOne(ctx context.Context, endpoint string, params map[string]string) (*lrcLibCandidate, int, error) {
	res, status, err := lrcLibDo(ctx, endpoint, params)
	if err != nil || res == nil {
		return nil, status, err
	}
	defer res.Body.Close()
	var out lrcLibCandidate
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, status, err
	}
	return &out, status, nil
}

func fetchLrcLibList(ctx context.Context, endpoint string, params map[string]string) ([]lrcLibCandidate, int, error) {
	res, status, err := lrcLibDo(ctx, endpoint, params)
	if err != nil || res == nil {
		return nil, status, err
	}
	defer res.Body.Close()
	var out []lrcLibCandidate
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, status, err
	}
	return out, status, nil
}

// lrcLibDo issues the request and returns the response only for 2xx; 404 yields
// (nil, 404, nil); other non-2xx is an error.
func lrcLibDo(ctx context.Context, endpoint string, params map[string]string) (*http.Response, int, error) {
	reqURL, err := buildLrcLibURL(endpoint, params)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", lyricsUserAgent)
	req.Header.Set("Accept", "application/json")
	res, err := lrcLibHTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			return nil, res.StatusCode, nil
		}
		return nil, res.StatusCode, fmt.Errorf("lrclib returned status %d", res.StatusCode)
	}
	return res, res.StatusCode, nil
}

func buildLrcLibURL(endpoint string, params map[string]string) (string, error) {
	base := strings.TrimSpace(lrcLibBaseURL)
	if base == "" {
		base = "https://lrclib.net"
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimPrefix(strings.TrimSpace(endpoint), "/")
	q := u.Query()
	for k, v := range params {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k != "" && v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// pickBestLrcLibCandidate scores fuzzy-search results by normalized
// track/artist/album equality and substring overlap. Without a duration
// tiebreaker this can occasionally attach the wrong recording's lyrics; see the
// note in pending.
func pickBestLrcLibCandidate(candidates []lrcLibCandidate, title, artist, album string) *lrcLibCandidate {
	wantTrack, wantArtist, wantAlbum := normalizeLrcText(title), normalizeLrcText(artist), normalizeLrcText(album)
	bestIdx, bestScore := -1, -1
	for i := range candidates {
		c := candidates[i]
		if strings.TrimSpace(c.PlainLyrics) == "" && strings.TrimSpace(c.SyncedLyrics) == "" {
			continue
		}
		score := 0
		score += overlapScore(normalizeLrcText(c.TrackName), wantTrack, 60, 25)
		score += overlapScore(normalizeLrcText(c.ArtistName), wantArtist, 60, 25)
		if wantAlbum != "" && normalizeLrcText(c.AlbumName) == wantAlbum {
			score += 20
		}
		if strings.TrimSpace(c.PlainLyrics) != "" {
			score += 8
		}
		if strings.TrimSpace(c.SyncedLyrics) != "" {
			score += 5
		}
		if score > bestScore {
			bestScore, bestIdx = score, i
		}
	}
	if bestIdx < 0 {
		return nil
	}
	return &candidates[bestIdx]
}

func overlapScore(got, want string, exact, partial int) int {
	if want == "" || got == "" {
		return 0
	}
	if got == want {
		return exact
	}
	if strings.Contains(got, want) || strings.Contains(want, got) {
		return partial
	}
	return 0
}

func normalizeLrcText(in string) string {
	in = strings.ToLower(strings.TrimSpace(in))
	if in == "" {
		return ""
	}
	var b strings.Builder
	lastSpace := false
	for _, r := range in {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"hespera/internal/match"
	"hespera/internal/youtube"
)

// ytSearchURL is the always-available link-out: a YouTube search for the song,
// used when no key is set, the lookup missed, or the API erred.
func ytSearchURL(artist, song string) string {
	q := strings.TrimSpace(artist + " " + song)
	return "https://www.youtube.com/results?search_query=" + url.QueryEscape(q)
}

func ytLookupKey(artist, song string) string {
	return match.NormalizeForDedup(artist) + "\x1f" + match.NormalizeForDedup(song)
}

// musicYouTubeResolve resolves a charting song (artist+song query params) to an
// embeddable YouTube video for in-app playback on the year-journey page. It is
// cache-first (the youtube_lookups table — one API call per song ever, hits and
// misses both cached), uses the optional YouTube Data API key, and always
// returns a link-out searchUrl as the fallback (no key / miss / quota error).
func (h *Handler) musicYouTubeResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	artist := strings.TrimSpace(r.URL.Query().Get("artist"))
	song := strings.TrimSpace(r.URL.Query().Get("song"))
	if song == "" {
		http.Error(w, "song required", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	videoID := ""
	resolve := true
	key := ytLookupKey(artist, song)

	// Cache first — a row (even with empty video_id, a cached miss) is authoritative.
	var cached string
	switch err := h.db.QueryRowContext(ctx, "SELECT video_id FROM youtube_lookups WHERE query_key=?", key).Scan(&cached); {
	case err == nil:
		videoID, resolve = cached, false
	case errors.Is(err, sql.ErrNoRows):
		// not cached → resolve below
	default:
		// transient DB error: fall through to a live resolve, don't fail the request
	}

	if resolve {
		if client := youtube.New(h.effectiveYouTubeKey(ctx)); client != nil {
			if vid, err := client.Search(ctx, artist, song); err == nil {
				videoID = vid
			}
			// Cache the outcome (hit or miss) so we don't re-spend quota on this song.
			_, _ = h.db.ExecContext(ctx,
				"INSERT INTO youtube_lookups (query_key, video_id) VALUES (?, ?) ON CONFLICT(query_key) DO UPDATE SET video_id=excluded.video_id",
				key, videoID)
		}
	}

	out := map[string]string{"searchUrl": ytSearchURL(artist, song)}
	if videoID != "" {
		out["videoId"] = videoID
		// The YouTube watch page (autoplays on open); the play button opens it in
		// a new tab. searchUrl remains the no-key / no-match fallback.
		out["watchUrl"] = "https://www.youtube.com/watch?v=" + videoID
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

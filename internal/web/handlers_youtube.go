package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"hespera/internal/match"
	"hespera/internal/youtube"
	"hespera/internal/ytdlp"
)

// ytdlpCandidates is how many search hits the yt-dlp resolver fetches so the
// embeddability verify has fallbacks if the top result isn't playable. One
// videos.list call covers the whole batch (1 quota unit), so a larger set is
// effectively free.
const ytdlpCandidates = 5

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
	unavailable := false
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
		var cacheable bool
		videoID, unavailable, cacheable = h.resolveYouTube(ctx, artist, song)
		// Cache only a real hit OR a genuine no-match. Never cache an UNAVAILABLE
		// error (quota / network / tool failure): a cached empty row is
		// authoritative forever, so caching a bad day would permanently mark a
		// song that IS on YouTube as "no video". On unavailable we fall through to
		// the link-out and the next request retries.
		if cacheable {
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
	} else if unavailable {
		// Distinguish "couldn't reach YouTube (daily limit or network)" from a
		// genuine "not on YouTube" so the client can say so honestly instead of
		// mislabeling a quota wall as "no video found".
		out["unavailable"] = "1"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// resolveYouTube resolves artist+song to an embeddable video id. It returns the
// id ("" if none), whether the lookup was UNAVAILABLE (a transient quota / network
// / tool error, as opposed to a genuine no-match), and whether the result is safe
// to CACHE (a real hit or a genuine miss — never an unavailable error).
//
// With the yt-dlp opt-in on and the binary present, it resolves quota-free via
// yt-dlp and verifies embeddability with a cheap 1-unit videos.list call; if
// yt-dlp itself fails it falls through to the Data API search. Otherwise it uses
// the Data API search directly (the default path).
func (h *Handler) resolveYouTube(ctx context.Context, artist, song string) (videoID string, unavailable, cacheable bool) {
	key := h.effectiveYouTubeKey(ctx)
	if h.youtubeYtdlpEnabled(ctx) && ytdlp.Available() {
		if ids, err := ytdlp.Search(ctx, artist, song, ytdlpCandidates); err == nil {
			if len(ids) == 0 {
				return "", false, true // genuine no result → cache the miss
			}
			client := youtube.New(key)
			if client == nil {
				// No key to verify with: return the top candidate unverified, but
				// DON'T cache it — an unverified dud would poison the cache as a
				// permanent link-out. onError on the client link-outs a dud.
				return ids[0], false, false
			}
			vid, verr := client.FirstEmbeddable(ctx, ids)
			if verr != nil {
				return "", true, false // verify API failed → unavailable
			}
			return vid, false, true // hit, or a genuine "none embeddable" miss
		}
		// yt-dlp errored (missing / rot / network) — fall through to the Data API.
	}
	client := youtube.New(key)
	if client == nil {
		return "", false, false // no key → link-out; nothing to cache
	}
	vid, err := client.Search(ctx, artist, song)
	if err != nil {
		return "", true, false // quota / network → unavailable, don't cache
	}
	return vid, false, true // hit or genuine miss → cacheable
}

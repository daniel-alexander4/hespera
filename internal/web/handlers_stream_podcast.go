package web

// Podcast playback: a synthetic session and a proxying stream.
//
// Both differ from every other vertical for the same reason — there is no local
// file. internal/playback is bypassed entirely rather than extended: Decide()
// reasons about a container and codecs read from a stored ffprobe result, and a
// podcast episode has never been probed. What it would tell us is also already
// known: a remote MP3 or M4A is direct-play or nothing, since we cannot
// transcode bytes we do not have.
//
// The proxy is the reason the browser never talks to a podcast host directly.
// That costs household bandwidth through the server and buys three things: your
// IP is not handed to every show you listen to, there is no mixed-content or
// CORS question, and the outbound fetch stays behind the one guard in
// internal/podcast.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"hespera/internal/podcast"
)

// podcastPlaybackSession answers the shape media_player.js already speaks, with
// the fields a probe would have filled left at their zero values. The player
// reads every one of them defensively (`session.audio_tracks || []`,
// `session.chapters || []`, `session.video_dar || 0`), so a session that
// carries only a protocol, a URL and a duration drives it correctly.
func (h *Handler) podcastPlaybackSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("file")), 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "invalid episode id", http.StatusBadRequest)
		return
	}

	var durationSecs float64
	var audioType string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COALESCE(duration_seconds,0), COALESCE(audio_type,'') FROM podcast_episodes WHERE id=?", id,
	).Scan(&durationSecs, &audioType); err != nil {
		jsonError(w, "episode not found", http.StatusNotFound)
		return
	}

	resp := playbackSessionResponse{
		OK: true,
		// Always direct: the bytes arrive as the host stored them and we have no
		// local copy to remux from. "file" is the protocol the player uses for
		// anything it can hand straight to the media element.
		Decision:     "direct",
		Protocol:     "file",
		URL:          fmt.Sprintf("/stream/episode/%d", id),
		Container:    containerFromMIME(audioType),
		DurationSecs: durationSecs,
	}

	pos, dur, done := h.loadPodcastProgress(r.Context(), id)
	resp.ResumePosition = resumePosition(pos, dur)
	resp.DurationSecs = maxf(resp.DurationSecs, dur)
	resp.Completed = done
	// Nothing is re-encoded and nothing is seeked server-side, so the stream
	// always begins at zero and the client seeks within it over byte ranges.
	resp.StreamStart = 0

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// containerFromMIME is display-only — the label the player shows. Deliberately
// not used for any decision: nothing here branches on container, because there
// is only one path.
func containerFromMIME(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch {
	case strings.Contains(mime, "mpeg"), strings.Contains(mime, "mp3"):
		return "mp3"
	case strings.Contains(mime, "mp4"), strings.Contains(mime, "m4a"), strings.Contains(mime, "aac"):
		return "m4a"
	case strings.Contains(mime, "ogg"), strings.Contains(mime, "opus"):
		return "ogg"
	case mime == "":
		return ""
	}
	return mime
}

// streamPodcastEpisode proxies one episode.
//
// The route takes an EPISODE ID, never a URL, and that is what keeps this from
// being an open proxy: the destination is read from a row that was validated
// when the feed was parsed, so no request can steer the server at a host of its
// choosing. Combined with internal/podcast's dial-time address policy, a feed
// that later starts redirecting inward is refused at the hop that turns.
func (h *Handler) streamPodcastEpisode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/stream/episode/")
	id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}

	var audioURL string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT audio_url FROM podcast_episodes WHERE id=?", id,
	).Scan(&audioURL); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("podcast stream lookup", "id", id, "err", err)
		}
		http.NotFound(w, r)
		return
	}

	// r.Context() is right here, unlike the play paths in hesplay: this IS the
	// response, so when the listener navigates away the fetch should stop
	// rather than keep pulling bytes nobody will hear.
	resp, err := h.podcastClient().Get(r.Context(), audioURL, r.Header.Get("Range"))
	if err != nil {
		if errors.Is(err, podcast.ErrBlockedAddress) {
			slog.Warn("podcast stream refused by address policy", "id", id, "err", err)
			http.Error(w, "this episode's audio host is not reachable", http.StatusForbidden)
			return
		}
		slog.Warn("podcast stream upstream", "id", id, "err", err)
		http.Error(w, "could not reach the episode audio", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Forward exactly the headers a media element needs to seek, and nothing
	// else — no cookies, no Set-Cookie, no tracking headers travelling back.
	for _, hdr := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(hdr); v != "" {
			w.Header().Set(hdr, v)
		}
	}
	if w.Header().Get("Accept-Ranges") == "" {
		w.Header().Set("Accept-Ranges", "bytes")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}

	if _, err := io.Copy(w, resp.Body); err != nil {
		// A listener seeking or leaving aborts the copy constantly; that is
		// normal and not worth a log line at anything above debug.
		slog.Debug("podcast stream copy ended", "id", id, "err", err)
	}
}

// --- progress --------------------------------------------------------------

func (h *Handler) loadPodcastProgress(ctx context.Context, episodeID int64) (pos, dur float64, completed bool) {
	var c int
	err := h.db.QueryRowContext(ctx,
		"SELECT position_seconds, duration_seconds, completed FROM podcast_playback_progress WHERE episode_id=?",
		episodeID,
	).Scan(&pos, &dur, &c)
	if err != nil {
		return 0, 0, false
	}
	return pos, dur, c == 1
}

// podcastPlaybackProgress records where the listener is.
//
// completed uses the earn-only MAX rule the other four verticals settled on:
// the client reports false whenever it has not personally watched this
// playthrough reach the end, so a blind assignment would revoke the tick every
// time an episode was merely opened.
func (h *Handler) podcastPlaybackProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		FileID    int64   `json:"file_id"`
		Position  float64 `json:"position_seconds"`
		Duration  float64 `json:"duration_seconds"`
		Completed bool    `json:"completed"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&body); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.FileID <= 0 {
		jsonError(w, "invalid episode id", http.StatusBadRequest)
		return
	}
	done := 0
	if body.Completed {
		done = 1
	}
	if _, err := h.db.ExecContext(r.Context(), `
INSERT INTO podcast_playback_progress (episode_id, position_seconds, duration_seconds, completed, updated_at)
VALUES (?, ?, ?, ?, datetime('now'))
ON CONFLICT(episode_id) DO UPDATE SET
  position_seconds=excluded.position_seconds,
  duration_seconds=excluded.duration_seconds,
  completed=MAX(podcast_playback_progress.completed, excluded.completed),
  updated_at=excluded.updated_at`,
		body.FileID, body.Position, body.Duration, done); err != nil {
		jsonError(w, "could not save progress", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// podcastPlayer renders the player page for one episode.
func (h *Handler) podcastPlayer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("file")), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	var epTitle, showTitle, image string
	var podcastID int64
	if err := h.db.QueryRowContext(r.Context(), `
SELECT e.title, p.title, p.id, COALESCE(NULLIF(e.image_url,''), p.image_url)
FROM podcast_episodes e JOIN podcasts p ON p.id = e.podcast_id
WHERE e.id = ?`, id).Scan(&epTitle, &showTitle, &podcastID, &image); err != nil {
		http.NotFound(w, r)
		return
	}
	h.render(w, "podcast_player.html", map[string]any{
		"Title":     epTitle,
		"EpisodeID": id,
		"Episode":   epTitle,
		"Show":      showTitle,
		"PodcastID": podcastID,
		"ImageURL":  image,
	})
}

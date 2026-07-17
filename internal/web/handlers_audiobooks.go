package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"hespera/internal/playback"
	"hespera/internal/video"
)

// Audiobooks: the fourth thin clone of the movie playback layer, audio-only.
// The same media_player.js drives the page (kind "audiobook"), the same
// decision layer picks direct-play vs an audio remux (never HLS — there is no
// video to transcode), and the same progress beacons resume to the second.
// Chapters ride the session's chapter marks; the transport's |< / >| step
// them client-side.

type audiobookSource struct {
	id         int64
	absPath    string
	container  string
	title      string
	author     string
	size       int64
	streamJSON string
}

func (h *Handler) loadAudiobookSource(ctx context.Context, fileID int64) (audiobookSource, error) {
	var s audiobookSource
	err := h.db.QueryRowContext(ctx, `
SELECT id, abs_path, COALESCE(container,''), title, author, COALESCE(file_size_bytes,0), COALESCE(stream_info_json,'{}')
FROM audiobooks WHERE id=?`, fileID,
	).Scan(&s.id, &s.absPath, &s.container, &s.title, &s.author, &s.size, &s.streamJSON)
	return s, err
}

// audiobookPlaybackSession mirrors tvPlaybackSession, minus everything an
// audio-only source can't have (subtitles, burn-in, HLS).
func (h *Handler) audiobookPlaybackSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("file")), 10, 64)
	if err != nil || fileID <= 0 {
		jsonError(w, "invalid file id", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	aud := atoiDefault(q.Get("aud"), 0)

	src, err := h.loadAudiobookSource(r.Context(), fileID)
	if err != nil {
		jsonError(w, "file not found", http.StatusNotFound)
		return
	}

	var probe video.ProbeResult
	_ = json.Unmarshal([]byte(src.streamJSON), &probe)

	mi := playback.FromProbe(&probe, src.container, src.size, aud, 0)
	profile, _ := playback.Profile(q.Get("client"), r.UserAgent())
	out := playback.Decide(profile, mi, q.Get("mode"))

	streamU := fmt.Sprintf("/stream/audiobook/%d", fileID)
	protocol := string(playback.ProtocolFile)
	// Anything that isn't a clean direct play routes through the audio remux —
	// an audio-only source never has video to transcode, so the progressive
	// fMP4 remux IS the compat path (Decide answers DirectStream for known
	// audio; an unprobed row falls into its transcode arm, which for audio maps
	// to the same remux).
	decision := out.Decision
	if decision != playback.DirectPlay {
		decision = playback.DirectStream
		streamU = fmt.Sprintf("/stream/audiobook-remux/%d?aud=%d", fileID, aud)
	}

	resp := playbackSessionResponse{
		OK:              true,
		Decision:        string(decision),
		Protocol:        protocol,
		URL:             streamU,
		Reasons:         out.Reasons,
		Container:       mi.Container,
		AudioCodec:      mi.AudioCodec,
		DurationSecs:    durationSeconds(probe.Format.Duration),
		AudioTracks:     audioTracks(&probe),
		AppliedAudio:    aud,
		AppliedSubtitle: 0,
	}
	resp.Chapters = chapterMarks(&probe)

	pos, dur, done := h.loadAudiobookProgress(r.Context(), fileID)
	resp.ResumePosition = resumePosition(pos, dur)
	resp.DurationSecs = maxf(resp.DurationSecs, dur)
	resp.Completed = done
	// The audio remux seeks exactly (accurate seek, no copied video keeping
	// pre-roll), so the stream really does begin at the requested position —
	// echo it; no keyframe-landing lookup applies.
	resp.StreamStart = resp.ResumePosition
	if raw := q.Get("start"); raw != "" {
		resp.StreamStart = parseStartParam(raw, resp.DurationSecs)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *Handler) loadAudiobookProgress(ctx context.Context, fileID int64) (pos, dur float64, completed bool) {
	var c int
	_ = h.db.QueryRowContext(ctx,
		"SELECT position_seconds, duration_seconds, completed FROM audiobook_playback_progress WHERE file_id=?",
		fileID,
	).Scan(&pos, &dur, &c)
	return pos, dur, c == 1
}

func (h *Handler) streamAudiobookDirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := pathID(r, "/stream/audiobook/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	src, err := h.loadAudiobookSource(r.Context(), fileID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	clean, err := h.resolveMediaPath(src.absPath)
	if err != nil {
		http.Error(w, "file path is outside media root", 500)
		return
	}
	f, err := os.Open(clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		httpError(w, 500, "internal server error", "stat file failed", "handler", "streamAudiobookDirect", "err", err)
		return
	}
	w.Header().Set("Content-Type", audiobookMIME(src.container))
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, filepath.Base(clean), st.ModTime(), f)
}

func audiobookMIME(container string) string {
	switch strings.ToLower(container) {
	case "m4b", "m4a", "aac":
		return "audio/mp4"
	case "mp3":
		return "audio/mpeg"
	case "ogg", "opus":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	case "wav":
		return "audio/wav"
	}
	return "application/octet-stream"
}

// streamAudiobookRemux is the progressive audio-only fMP4 (video.AudioRemuxArgs):
// audio copied when the muxer can carry it, else encoded to AAC. Resume rides
// ?start= exactly like the video remux; the seek is exact (re-encode-grade
// accuracy even on copy — audio packets are ~21ms).
func (h *Handler) streamAudiobookRemux(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := pathID(r, "/stream/audiobook-remux/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	src, err := h.loadAudiobookSource(r.Context(), fileID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	clean, err := h.resolveMediaPath(src.absPath)
	if err != nil {
		http.Error(w, "file path is outside media root", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	if r.Method == http.MethodHead {
		return
	}
	aud := atoiDefault(r.URL.Query().Get("aud"), 0)
	total := audiobookStoredDuration(src)
	start := parseStartParam(r.URL.Query().Get("start"), total)
	ch, encodeAudio := h.remuxAudioPlan(r, src.streamJSON, src.container, src.size, aud)
	args := video.AudioRemuxArgs(clean, aud, start, ch, encodeAudio)
	streamProgressive(w, r, args, maxf(total-start, 0), "audiobook remux stream", fileID)
}

func audiobookStoredDuration(src audiobookSource) float64 {
	var probe video.ProbeResult
	_ = json.Unmarshal([]byte(src.streamJSON), &probe)
	return durationSeconds(probe.Format.Duration)
}

// audiobookPlaybackProgress mirrors tvPlaybackProgress (earn-only completed).
func (h *Handler) audiobookPlaybackProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		FileID          int64   `json:"file_id"`
		PositionSeconds float64 `json:"position_seconds"`
		DurationSeconds float64 `json:"duration_seconds"`
		Completed       bool    `json:"completed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.FileID <= 0 {
		jsonError(w, "invalid progress payload", http.StatusBadRequest)
		return
	}
	completed := 0
	if req.Completed {
		completed = 1
	}
	_, err := h.db.ExecContext(r.Context(), `
INSERT INTO audiobook_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at)
VALUES (?, ?, ?, ?, datetime('now'))
ON CONFLICT(file_id) DO UPDATE SET
  position_seconds=excluded.position_seconds,
  duration_seconds=excluded.duration_seconds,
  completed=MAX(completed, excluded.completed),
  updated_at=datetime('now')`,
		req.FileID, req.PositionSeconds, req.DurationSeconds, completed)
	if err != nil {
		jsonError(w, "store progress failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// audiobookPlayer renders the player page — the movie player shell, audio-only
// kind, with the cover as the visual.
func (h *Handler) audiobookPlayer(w http.ResponseWriter, r *http.Request) {
	fileID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("file")), 10, 64)
	if err != nil || fileID <= 0 {
		http.NotFound(w, r)
		return
	}
	src, err := h.loadAudiobookSource(r.Context(), fileID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var thumb string
	_ = h.db.QueryRowContext(r.Context(), "SELECT thumb_path FROM audiobooks WHERE id=?", fileID).Scan(&thumb)
	h.render(w, "audiobook_player.html", map[string]any{
		"Breadcrumb":  []crumb{bcHome, bcAudiobooks},
		"Title":       src.title,
		"FileID":      src.id,
		"BookTitle":   src.title,
		"Author":      src.author,
		"HasThumb":    thumb != "" && thumb != "unavailable",
		"CaptionVars": h.captionStyleVars(r.Context()),
	})
}

type audiobookCard struct {
	ID           int64
	Title        string
	Author       string
	HasThumb     bool
	DurationText string
	ProgressPct  int
	Completed    bool
}

// audiobooksHome is the paginated cover grid (alphabetical), with the same
// in-place ?grid=1 fragment paging the other browse grids use.
func (h *Handler) audiobooksHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/audiobooks" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()

	var total int
	if err := h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audiobooks").Scan(&total); err != nil {
		httpError(w, 500, "internal server error", "count audiobooks failed", "handler", "audiobooksHome", "err", err)
		return
	}
	nav, offset := paginate(pageParam(r), total, "/audiobooks")

	rows, err := h.db.QueryContext(ctx, `
SELECT a.id, a.title, a.author, a.thumb_path, a.duration_seconds,
       COALESCE(p.position_seconds,0), COALESCE(p.duration_seconds,0), COALESCE(p.completed,0)
FROM audiobooks a LEFT JOIN audiobook_playback_progress p ON p.file_id = a.id
ORDER BY a.title COLLATE NOCASE, a.id LIMIT ? OFFSET ?`, listPageSize, offset)
	if err != nil {
		httpError(w, 500, "internal server error", "load audiobooks failed", "handler", "audiobooksHome", "err", err)
		return
	}
	defer rows.Close()
	cards := make([]audiobookCard, 0, listPageSize)
	for rows.Next() {
		var c audiobookCard
		var thumb string
		var durSec, pos, pdur float64
		var done int
		if err := rows.Scan(&c.ID, &c.Title, &c.Author, &thumb, &durSec, &pos, &pdur, &done); err != nil {
			httpError(w, 500, "internal server error", "scan audiobook failed", "handler", "audiobooksHome", "err", err)
			return
		}
		c.HasThumb = thumb != "" && thumb != "unavailable"
		c.DurationText = audiobookDurationText(durSec)
		c.Completed = done == 1
		if ref := maxf(durSec, pdur); ref > 0 && pos > 0 {
			c.ProgressPct = int(pos / ref * 100)
		}
		cards = append(cards, c)
	}

	if r.URL.Query().Get("grid") == "1" {
		h.renderFragment(w, "audiobooks_home.html", "audiobook-cards", map[string]any{"Cards": cards})
		return
	}

	var libs int
	_ = h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM libraries WHERE type='audiobooks'").Scan(&libs)
	h.render(w, "audiobooks_home.html", map[string]any{
		"Breadcrumb":   []crumb{bcHome},
		"Title":        "Audiobooks",
		"Cards":        cards,
		"Page":         nav,
		"LibraryEmpty": libs == 0,
	})
}

// audiobookDurationText renders "12h 34m" (or "47m") for the card label.
func audiobookDurationText(sec float64) string {
	if sec <= 0 {
		return ""
	}
	mins := int(sec / 60)
	if mins < 60 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dh %02dm", mins/60, mins%60)
}

// audiobookArt serves the generated cover thumb.
func (h *Handler) audiobookArt(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "/art/audiobook/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var thumb string
	if err := h.db.QueryRowContext(r.Context(), "SELECT thumb_path FROM audiobooks WHERE id=?", id).Scan(&thumb); err != nil {
		http.NotFound(w, r)
		return
	}
	h.serveGeneratedThumb(w, r, thumb)
}

// loadAudiobookRecentlyAdded feeds home's Recently Added Audiobooks carousel.
func (h *Handler) loadAudiobookRecentlyAdded(ctx context.Context, limit int) ([]audiobookCard, error) {
	rows, err := h.db.QueryContext(ctx, `
SELECT id, title, author, thumb_path FROM audiobooks
ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]audiobookCard, 0, limit)
	for rows.Next() {
		var c audiobookCard
		var thumb string
		if err := rows.Scan(&c.ID, &c.Title, &c.Author, &thumb); err != nil {
			return nil, err
		}
		c.HasThumb = thumb != "" && thumb != "unavailable"
		out = append(out, c)
	}
	return out, rows.Err()
}

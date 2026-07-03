package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hespera/internal/playback"
	"hespera/internal/video"
)

// Home-video clip streaming — the third thin clone of the TV streaming layer
// (after movies): the decision, the ffmpeg builders, segment-on-demand HLS,
// and the shared cache (keyed by source path, so file-id namespaces never
// collide) all reused unchanged; only the source table (photos) and the URL
// prefixes differ. No OpenSubtitles search (home videos have none to find).

func (h *Handler) loadPhotoFileSource(ctx context.Context, fileID int64) (movieFileSource, error) {
	var s movieFileSource
	err := h.db.QueryRowContext(ctx,
		"SELECT abs_path, COALESCE(container,''), COALESCE(file_size_bytes,0), COALESCE(stream_info_json,'{}') FROM photos WHERE id=? AND kind='video'",
		fileID,
	).Scan(&s.absPath, &s.container, &s.size, &s.streamJSON)
	return s, err
}

// photoPlayer renders the clip player page — the movie player with the photo
// endpoints, titled from the filename (clips have no metadata).
func (h *Handler) photoPlayer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("file")), 10, 64)
	if err != nil || fileID <= 0 {
		http.NotFound(w, r)
		return
	}
	var absPath, takenAt string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT abs_path, taken_at FROM photos WHERE id=? AND kind='video'", fileID,
	).Scan(&absPath, &takenAt); err != nil {
		http.NotFound(w, r)
		return
	}
	when := ""
	if t, terr := time.Parse("2006-01-02 15:04:05", takenAt); terr == nil {
		when = t.Format("January 2, 2006")
	}
	h.render(w, "photo_player.html", map[string]any{
		"Title":       "Playing",
		"FileID":      fileID,
		"ClipTitle":   strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath)),
		"When":        when,
		"CaptionVars": h.captionStyleVars(r.Context()),
	})
}

func (h *Handler) photoPlaybackSession(w http.ResponseWriter, r *http.Request) {
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
	sub := atoiDefault(q.Get("sub"), 0)
	subExplicitOff := sub < 0
	if subExplicitOff {
		sub = 0
	}
	mode := q.Get("mode")
	client := q.Get("client")

	src, err := h.loadPhotoFileSource(r.Context(), fileID)
	if err != nil {
		jsonError(w, "file not found", http.StatusNotFound)
		return
	}

	var probe video.ProbeResult
	_ = json.Unmarshal([]byte(src.streamJSON), &probe)

	if aud == 0 {
		if n := preferredAudioOrdinal(&probe, h.effectiveDefaultAudioLang(r.Context())); n > 0 {
			aud = n
		} else if n := defaultAudioOrdinal(&probe); n > 1 {
			aud = n
		}
	}
	autoSub := false
	if sub == 0 && !subExplicitOff && h.effectiveSubtitlesDefaultOn(r.Context()) {
		if n := preferredTextSubOrdinal(&probe, h.effectiveDefaultSubtitleLang(r.Context())); n > 0 {
			sub = n
			autoSub = true
		}
	}

	mi := playback.FromProbe(&probe, src.container, src.size, aud, sub)
	profile, _ := playback.Profile(client, r.UserAgent())
	out := playback.Decide(profile, mi, mode)
	if autoSub && out.SubtitleBurnIn {
		sub = 0
		mi = playback.FromProbe(&probe, src.container, src.size, aud, sub)
		out = playback.Decide(profile, mi, mode)
	}

	streamU := photoStreamURL(out.Decision, fileID, aud)
	protocol := string(out.Protocol)
	if out.SubtitleBurnIn && sub > 0 {
		streamU = fmt.Sprintf("/stream/photo-burnin/%d?sub=%d&aud=%d", fileID, sub, aud)
		protocol = string(playback.ProtocolFile)
	}

	resp := playbackSessionResponse{
		OK:              true,
		Decision:        string(out.Decision),
		Protocol:        protocol,
		URL:             streamU,
		Reasons:         out.Reasons,
		Container:       mi.Container,
		VideoCodec:      mi.VideoCodec,
		AudioCodec:      mi.AudioCodec,
		DurationSecs:    durationSeconds(probe.Format.Duration),
		AudioTracks:     audioTracks(&probe),
		SubtitleTracks:  subtitleTracks(&probe),
		AppliedAudio:    aud,
		AppliedSubtitle: sub,
	}
	resp.Chapters = chapterMarks(&probe)
	if out.SubtitleSidecar && sub > 0 {
		resp.SubtitleURL = fmt.Sprintf("/stream/photo-subtitles/%d?track=%d", fileID, sub)
	}
	if pos, dur, done := h.loadPhotoProgress(r.Context(), fileID); !done {
		resp.ResumePosition, resp.DurationSecs, resp.Completed = pos, maxf(resp.DurationSecs, dur), done
	} else {
		resp.Completed = true
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func photoStreamURL(d playback.Decision, fileID int64, aud int) string {
	switch d {
	case playback.DirectPlay:
		return fmt.Sprintf("/stream/photo/%d", fileID)
	case playback.DirectStream:
		return fmt.Sprintf("/stream/photo-remux/%d?aud=%d", fileID, aud)
	default:
		if aud > 0 {
			return fmt.Sprintf("/stream/photo-hls/%d/index.m3u8?aud=%d", fileID, aud)
		}
		return fmt.Sprintf("/stream/photo-hls/%d/index.m3u8", fileID)
	}
}

func (h *Handler) loadPhotoProgress(ctx context.Context, fileID int64) (pos, dur float64, completed bool) {
	var c int
	_ = h.db.QueryRowContext(ctx,
		"SELECT position_seconds, duration_seconds, completed FROM photo_playback_progress WHERE file_id=?",
		fileID,
	).Scan(&pos, &dur, &c)
	return pos, dur, c == 1
}

func (h *Handler) streamPhotoDirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := pathID(r, "/stream/photo/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	src, err := h.loadPhotoFileSource(r.Context(), fileID)
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
		httpError(w, 500, "internal server error", "stat file failed", "handler", "streamPhotoDirect", "err", err)
		return
	}
	w.Header().Set("Content-Type", videoMIME(src.container, clean))
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, filepath.Base(clean), st.ModTime(), f)
}

func (h *Handler) streamPhotoRemux(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := pathID(r, "/stream/photo-remux/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	src, err := h.loadPhotoFileSource(r.Context(), fileID)
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
	total := movieStoredDuration(src.streamJSON)
	start := parseStartParam(r.URL.Query().Get("start"), total)
	if err := video.StreamFFmpegPatchMoov(r.Context(), w, video.RemuxArgs(clean, aud, start), maxf(total-start, 0)); err != nil {
		slog.Warn("photo clip remux stream", "file_id", fileID, "err", err)
	}
}

func (h *Handler) streamPhotoBurnIn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := pathID(r, "/stream/photo-burnin/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	src, err := h.loadPhotoFileSource(r.Context(), fileID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sub := atoiDefault(r.URL.Query().Get("sub"), 0)
	var probe video.ProbeResult
	_ = json.Unmarshal([]byte(src.streamJSON), &probe)
	subs := subtitleTracks(&probe)
	if sub < 1 || sub > len(subs) || subs[sub-1].Text {
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
	total := durationSeconds(probe.Format.Duration)
	start := parseStartParam(r.URL.Query().Get("start"), total)
	if err := video.StreamFFmpegPatchMoov(r.Context(), w, video.BurnInArgs(clean, sub, aud, 1080, start, audioChannels(&probe, aud)), maxf(total-start, 0)); err != nil {
		slog.Warn("photo clip burn-in stream", "file_id", fileID, "err", err)
	}
}

func (h *Handler) streamPhotoHLS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rest := pathSegment(r, "/stream/photo-hls/")
	idStr, name, _ := strings.Cut(rest, "/")
	if name == "" {
		name = "index.m3u8"
	}
	if !hlsAssetName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	fileID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || fileID <= 0 {
		http.NotFound(w, r)
		return
	}
	src, err := h.loadPhotoFileSource(r.Context(), fileID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	clean, err := h.resolveMediaPath(src.absPath)
	if err != nil {
		http.Error(w, "file path is outside media root", http.StatusInternalServerError)
		return
	}
	st, err := os.Stat(clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	dur := h.movieDuration(r.Context(), src, clean)
	if dur <= 0 {
		httpError(w, http.StatusInternalServerError, "transcode failed", "unknown source duration", "handler", "streamPhotoHLS", "file_id", fileID)
		return
	}

	if strings.HasSuffix(name, ".m3u8") {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			return
		}
		fmt.Fprint(w, video.VODPlaylist(dur, atoiDefault(r.URL.Query().Get("aud"), 0)))
		return
	}

	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if r.Method == http.MethodHead {
		return
	}
	index, err := segmentIndex(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var probe video.ProbeResult
	_ = json.Unmarshal([]byte(src.streamJSON), &probe)
	aud := atoiDefault(r.URL.Query().Get("aud"), 0)
	segPath, err := video.EnsureSegment(r.Context(), h.tvHLSCacheRoot(), clean, st.ModTime(), st.Size(), 1080, index, dur, audioChannels(&probe, aud), aud)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		httpError(w, http.StatusInternalServerError, "transcode failed", "hls segment", "handler", "streamPhotoHLS", "file_id", fileID, "seg", index, "err", err)
		return
	}
	f, err := os.Open(segPath)
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
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func (h *Handler) streamPhotoSubtitles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := pathID(r, "/stream/photo-subtitles/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	track := atoiDefault(r.URL.Query().Get("track"), 1)
	src, err := h.loadPhotoFileSource(r.Context(), fileID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var probe video.ProbeResult
	_ = json.Unmarshal([]byte(src.streamJSON), &probe)
	subs := subtitleTracks(&probe)
	if track < 1 || track > len(subs) || !subs[track-1].Text {
		http.NotFound(w, r)
		return
	}
	clean, err := h.resolveMediaPath(src.absPath)
	if err != nil {
		http.Error(w, "file path is outside media root", http.StatusInternalServerError)
		return
	}
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-i", clean,
		"-map", fmt.Sprintf("0:s:%d", track-1),
		"-f", "webvtt", "pipe:1",
	}
	vtt, err := extractVTT(r.Context(), args)
	if err != nil {
		slog.Warn("photo clip subtitle extract", "file_id", fileID, "track", track, "err", err)
		http.Error(w, "subtitle extract failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(vtt)
}

func (h *Handler) photoPlaybackProgress(w http.ResponseWriter, r *http.Request) {
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, 400, "bad request", "decode json failed", "handler", "photoPlaybackProgress", "err", err)
		return
	}
	if req.FileID <= 0 {
		http.Error(w, "invalid file_id", 400)
		return
	}
	completedInt := 0
	if req.Completed {
		completedInt = 1
	}
	_, err := h.db.ExecContext(r.Context(), `
INSERT INTO photo_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at)
VALUES (?, ?, ?, ?, datetime('now'))
ON CONFLICT(file_id) DO UPDATE SET
  position_seconds=excluded.position_seconds,
  duration_seconds=excluded.duration_seconds,
  completed=excluded.completed,
  updated_at=datetime('now')
`, req.FileID, req.PositionSeconds, req.DurationSeconds, completedInt)
	if err != nil {
		httpError(w, 500, "internal server error", "db upsert failed", "handler", "photoPlaybackProgress", "err", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

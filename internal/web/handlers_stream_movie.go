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

	"hespera/internal/pathguard"
	"hespera/internal/playback"
	"hespera/internal/video"
)

// Movie streaming mirrors the TV streaming layer (handlers_stream_tv.go): the
// per-client playback decision (internal/playback), the ffmpeg arg builders and
// segment-on-demand HLS (internal/video), and every pure track/duration helper
// are shared unchanged — only the source table (movie_files), the resume table
// (movie_playback_progress), and the /stream/movie-* URL prefixes differ. The
// HLS cache is keyed by source path (not row id), so movies safely reuse the
// same cache root + prune loop as TV with no collision.

type movieFileSource struct {
	absPath    string
	container  string
	size       int64
	streamJSON string
}

func (h *Handler) loadMovieFileSource(ctx context.Context, fileID int64) (movieFileSource, error) {
	var s movieFileSource
	err := h.db.QueryRowContext(ctx,
		"SELECT abs_path, COALESCE(container,''), COALESCE(file_size_bytes,0), COALESCE(stream_info_json,'{}') FROM movie_files WHERE id=?",
		fileID,
	).Scan(&s.absPath, &s.container, &s.size, &s.streamJSON)
	return s, err
}

// resolveMediaPath maps a stored abs_path to a real, contained path under the
// media root. Media-type-agnostic (same as resolveTVPath); used by movie streams.
func (h *Handler) resolveMediaPath(absPath string) (string, error) {
	return pathguard.ResolveExistingUnderRoot(filepath.Clean(h.cfg.MediaRoot), absPath)
}

// moviePlaybackSession resolves how a client should play a movie file. Mirrors
// tvPlaybackSession.
func (h *Handler) moviePlaybackSession(w http.ResponseWriter, r *http.Request) {
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
	// sub == -1 is the client's explicit "Off" (see tvPlaybackSession).
	subExplicitOff := sub < 0
	if subExplicitOff {
		sub = 0
	}
	mode := q.Get("mode")
	client := q.Get("client")

	src, err := h.loadMovieFileSource(r.Context(), fileID)
	if err != nil {
		jsonError(w, "file not found", http.StatusNotFound)
		return
	}

	var probe video.ProbeResult
	_ = json.Unmarshal([]byte(src.streamJSON), &probe)

	// Language-preference audio first, else pin aud to the disposition-default's
	// ordinal when the client didn't choose, so the decision and the served track
	// agree (see tvPlaybackSession).
	if aud == 0 {
		if n := preferredAudioOrdinal(&probe, h.effectiveDefaultAudioLang(r.Context())); n > 0 {
			aud = n
		} else if n := defaultAudioOrdinal(&probe); n > 1 {
			aud = n
		}
	}
	// Subtitles-on default: auto-enable a text subtitle on an unpinned load.
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
		// A default setting must never trigger a transcode (see tvPlaybackSession).
		sub = 0
		mi = playback.FromProbe(&probe, src.container, src.size, aud, sub)
		out = playback.Decide(profile, mi, mode)
	}

	streamU := movieStreamURL(out.Decision, fileID, aud)
	protocol := string(out.Protocol)
	if out.SubtitleBurnIn && sub > 0 {
		streamU = fmt.Sprintf("/stream/movie-burnin/%d?sub=%d&aud=%d", fileID, sub, aud)
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
		VideoDAR:        probe.VideoDisplayAspect(),
	}
	if clean, perr := h.resolveMediaPath(src.absPath); perr == nil {
		resp.SkipSegments = skipSegmentsFor(&probe, clean)
	} else {
		resp.SkipSegments = skipSegmentsFor(&probe, "")
	}
	resp.Chapters = chapterMarks(&probe)
	if out.SubtitleSidecar && sub > 0 {
		resp.SubtitleURL = fmt.Sprintf("/stream/movie-subtitles/%d?track=%d", fileID, sub)
	}
	// Watched and resume are independent: a completed item still resumes a genuine
	// partial re-watch. resumePosition is the sole owner of "is there anything to
	// resume" — it drops a position sitting at the end of a finished playthrough,
	// which would otherwise seek to the credits and instantly auto-advance Up Next.
	pos, dur, done := h.loadMovieProgress(r.Context(), fileID)
	resp.ResumePosition = resumePosition(pos, dur)
	resp.DurationSecs = maxf(resp.DurationSecs, dur)
	resp.Completed = done

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func movieStreamURL(d playback.Decision, fileID int64, aud int) string {
	switch d {
	case playback.DirectPlay:
		return fmt.Sprintf("/stream/movie/%d", fileID)
	case playback.DirectStream:
		return fmt.Sprintf("/stream/movie-remux/%d?aud=%d", fileID, aud)
	default:
		if aud > 0 {
			return fmt.Sprintf("/stream/movie-hls/%d/index.m3u8?aud=%d", fileID, aud)
		}
		return fmt.Sprintf("/stream/movie-hls/%d/index.m3u8", fileID)
	}
}

func (h *Handler) loadMovieProgress(ctx context.Context, fileID int64) (pos, dur float64, completed bool) {
	var c int
	_ = h.db.QueryRowContext(ctx,
		"SELECT position_seconds, duration_seconds, completed FROM movie_playback_progress WHERE file_id=?",
		fileID,
	).Scan(&pos, &dur, &c)
	return pos, dur, c == 1
}

// streamMovieDirect serves the source file directly with range support (the
// direct-play decision). Mirrors streamTVEpisode.
func (h *Handler) streamMovieDirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := pathID(r, "/stream/movie/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	src, err := h.loadMovieFileSource(r.Context(), fileID)
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
		httpError(w, 500, "internal server error", "stat file failed", "handler", "streamMovieDirect", "err", err)
		return
	}
	w.Header().Set("Content-Type", videoMIME(src.container, clean))
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, filepath.Base(clean), st.ModTime(), f)
}

// streamMovieRemux repackages the source into a fragmented MP4 (codecs copied).
// Mirrors streamTVRemux.
func (h *Handler) streamMovieRemux(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := pathID(r, "/stream/movie-remux/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	src, err := h.loadMovieFileSource(r.Context(), fileID)
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
	ch, encodeAudio := h.remuxAudioPlan(r, src.streamJSON, src.container, src.size, aud)
	args := video.RemuxArgs(clean, aud, start, ch, encodeAudio)
	streamProgressive(w, r, args, maxf(total-start, 0), "movie remux stream", fileID)
}

// streamMovieBurnIn transcodes with a bitmap subtitle burned in, progressive
// fragmented MP4. Mirrors streamTVBurnIn.
func (h *Handler) streamMovieBurnIn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := pathID(r, "/stream/movie-burnin/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	src, err := h.loadMovieFileSource(r.Context(), fileID)
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
	streamProgressive(w, r, video.BurnInArgs(clean, sub, aud, 1080, start, audioChannels(&probe, aud)), maxf(total-start, 0), "movie burn-in stream", fileID)
}

// streamMovieHLS serves the segment-on-demand HLS asset (seekable transcode).
// Mirrors streamTVHLS; reuses the shared HLS cache root (keyed by source path).
func (h *Handler) streamMovieHLS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rest := pathSegment(r, "/stream/movie-hls/")
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
	src, err := h.loadMovieFileSource(r.Context(), fileID)
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
		httpError(w, http.StatusInternalServerError, "transcode failed", "unknown source duration", "handler", "streamMovieHLS", "file_id", fileID)
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
		httpError(w, http.StatusInternalServerError, "transcode failed", "hls segment", "handler", "streamMovieHLS", "file_id", fileID, "seg", index, "err", err)
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

// streamMovieSubtitles extracts a text subtitle track as WebVTT. Mirrors
// streamTVSubtitles.
func (h *Handler) streamMovieSubtitles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := pathID(r, "/stream/movie-subtitles/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	track := atoiDefault(r.URL.Query().Get("track"), 1)
	src, err := h.loadMovieFileSource(r.Context(), fileID)
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
		slog.Warn("movie subtitle extract", "file_id", fileID, "track", track, "err", err)
		http.Error(w, "subtitle extract failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(vtt)
}

// moviePlaybackProgress upserts a movie's resume position. Mirrors
// tvPlaybackProgress.
func (h *Handler) moviePlaybackProgress(w http.ResponseWriter, r *http.Request) {
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
		httpError(w, 400, "bad request", "decode json failed", "handler", "moviePlaybackProgress", "err", err)
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
INSERT INTO movie_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at)
VALUES (?, ?, ?, ?, datetime('now'))
ON CONFLICT(file_id) DO UPDATE SET
  position_seconds=excluded.position_seconds,
  duration_seconds=excluded.duration_seconds,
  -- Earn-only: the progress stream can SET the watched flag but never revoke it.
  -- Clearing has its own owner: markWatched. See the note above resumePosition.
  completed=MAX(completed, excluded.completed),
  updated_at=datetime('now')
`, req.FileID, req.PositionSeconds, req.DurationSeconds, completedInt)
	if err != nil {
		httpError(w, 500, "internal server error", "db upsert failed", "handler", "moviePlaybackProgress", "err", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// movieDuration returns the source duration, preferring the stored probe and
// falling back to a live ffprobe. Mirrors tvDuration.
func (h *Handler) movieDuration(ctx context.Context, src movieFileSource, clean string) float64 {
	if d := movieStoredDuration(src.streamJSON); d > 0 {
		return d
	}
	if p, err := video.Probe(ctx, clean); err == nil {
		return durationSeconds(p.Format.Duration)
	}
	return 0
}

// movieStoredDuration parses the source duration from a stored probe JSON blob.
func movieStoredDuration(streamJSON string) float64 {
	var probe video.ProbeResult
	_ = json.Unmarshal([]byte(streamJSON), &probe)
	return durationSeconds(probe.Format.Duration)
}

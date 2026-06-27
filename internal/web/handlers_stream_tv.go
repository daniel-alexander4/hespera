package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hespera/internal/pathguard"
	"hespera/internal/playback"
	"hespera/internal/video"
)

// tvHLSCacheRoot is where built HLS assets live, under the data dir (not /tmp).
func (h *Handler) tvHLSCacheRoot() string {
	return filepath.Join(h.cfg.DataDir, "cache", "tv-hls")
}

// pruneTVCacheLoop periodically bounds the HLS cache by the configured size and
// age budgets. Runs for the process lifetime, like the job service.
func (h *Handler) pruneTVCacheLoop() {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for range t.C {
		_ = video.PruneCache(h.tvHLSCacheRoot(), h.cfg.TVHLSCacheMaxBytes, h.cfg.TVCacheMaxAge)
	}
}

type tvFileSource struct {
	absPath    string
	container  string
	size       int64
	streamJSON string
}

func (h *Handler) loadTVFileSource(ctx context.Context, fileID int64) (tvFileSource, error) {
	var s tvFileSource
	err := h.db.QueryRowContext(ctx,
		"SELECT abs_path, COALESCE(container,''), COALESCE(file_size_bytes,0), COALESCE(stream_info_json,'{}') FROM tv_series_files WHERE id=?",
		fileID,
	).Scan(&s.absPath, &s.container, &s.size, &s.streamJSON)
	return s, err
}

// resolveTVPath maps a stored abs_path to a real, contained path under the media root.
func (h *Handler) resolveTVPath(absPath string) (string, error) {
	return pathguard.ResolveExistingUnderRoot(filepath.Clean(h.cfg.MediaRoot), absPath)
}

type sessionTrack struct {
	Ordinal  int    `json:"ordinal"`
	Codec    string `json:"codec"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default"`
	Text     bool   `json:"text,omitempty"`
}

type playbackSessionResponse struct {
	OK             bool              `json:"ok"`
	Decision       string            `json:"decision"`
	Protocol       string            `json:"protocol"`
	URL            string            `json:"url"`
	SubtitleURL    string            `json:"subtitle_url,omitempty"`
	Reasons        []playback.Reason `json:"reasons"`
	Container      string            `json:"container"`
	VideoCodec     string            `json:"video_codec"`
	AudioCodec     string            `json:"audio_codec"`
	DurationSecs   float64           `json:"duration_seconds"`
	ResumePosition float64           `json:"resume_position_seconds"`
	Completed      bool              `json:"completed"`
	AudioTracks    []sessionTrack    `json:"audio_tracks,omitempty"`
	SubtitleTracks []sessionTrack    `json:"subtitle_tracks,omitempty"`
}

// tvPlaybackSession resolves how a given client should play a TV file: the
// decision (direct/remux/transcode), the source URL for that decision, the
// available audio/subtitle tracks, and any resume position. The browser player
// calls this, then loads the returned URL.
func (h *Handler) tvPlaybackSession(w http.ResponseWriter, r *http.Request) {
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
	mode := q.Get("mode")
	client := q.Get("client")

	src, err := h.loadTVFileSource(r.Context(), fileID)
	if err != nil {
		jsonError(w, "file not found", http.StatusNotFound)
		return
	}

	var probe video.ProbeResult
	_ = json.Unmarshal([]byte(src.streamJSON), &probe)

	mi := playback.FromProbe(&probe, src.container, src.size, aud, sub)
	profile, _ := playback.Profile(client, r.UserAgent())
	out := playback.Decide(profile, mi, mode)

	resp := playbackSessionResponse{
		OK:             true,
		Decision:       string(out.Decision),
		Protocol:       string(out.Protocol),
		URL:            streamURL(out.Decision, fileID, aud),
		Reasons:        out.Reasons,
		Container:      mi.Container,
		VideoCodec:     mi.VideoCodec,
		AudioCodec:     mi.AudioCodec,
		DurationSecs:   durationSeconds(probe.Format.Duration),
		AudioTracks:    audioTracks(&probe),
		SubtitleTracks: subtitleTracks(&probe),
	}
	if out.SubtitleSidecar && sub > 0 {
		resp.SubtitleURL = fmt.Sprintf("/stream/tv-subtitles/%d?track=%d", fileID, sub)
	}
	if pos, dur, done := h.loadTVProgress(r.Context(), fileID); !done {
		resp.ResumePosition, resp.DurationSecs, resp.Completed = pos, maxf(resp.DurationSecs, dur), done
	} else {
		resp.Completed = true
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func streamURL(d playback.Decision, fileID int64, aud int) string {
	switch d {
	case playback.DirectPlay:
		return fmt.Sprintf("/stream/tv/%d", fileID)
	case playback.DirectStream:
		return fmt.Sprintf("/stream/tv-remux/%d?aud=%d", fileID, aud)
	default:
		return fmt.Sprintf("/stream/tv-hls/%d/index.m3u8", fileID)
	}
}

func (h *Handler) loadTVProgress(ctx context.Context, fileID int64) (pos, dur float64, completed bool) {
	var c int
	_ = h.db.QueryRowContext(ctx,
		"SELECT position_seconds, duration_seconds, completed FROM tv_playback_progress WHERE file_id=?",
		fileID,
	).Scan(&pos, &dur, &c)
	return pos, dur, c == 1
}

// streamTVRemux repackages the source into a fragmented MP4 (codecs copied) and
// streams it. Used for the direct-stream decision (compatible codecs, wrong
// container). HEAD does not spawn ffmpeg.
func (h *Handler) streamTVRemux(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := pathID(r, "/stream/tv-remux/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	src, err := h.loadTVFileSource(r.Context(), fileID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	clean, err := h.resolveTVPath(src.absPath)
	if err != nil {
		http.Error(w, "file path is outside media root", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "video/mp4")
	if r.Method == http.MethodHead {
		return
	}
	aud := atoiDefault(r.URL.Query().Get("aud"), 0)
	if err := video.StreamFFmpeg(r.Context(), w, video.RemuxArgs(clean, aud)); err != nil {
		// Headers/body may already be partially written; just log.
		slog.Warn("tv remux stream", "file_id", fileID, "err", err)
	}
}

var hlsAssetName = regexp.MustCompile(`^(index\.m3u8|seg\d+\.ts)$`)

// streamTVHLS serves a segment-on-demand HLS asset for a file. The manifest is a
// synthetic VOD playlist (all segments listed up front from the source duration,
// #EXT-X-ENDLIST) so the player knows the full episode length immediately and
// can seek anywhere; each segment is transcoded lazily on first request and
// cached. This is what lets the scrubber span the whole episode and seek
// forward/back during a transcode, instead of waiting for a linear encode.
func (h *Handler) streamTVHLS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rest := pathSegment(r, "/stream/tv-hls/")
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
	src, err := h.loadTVFileSource(r.Context(), fileID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	clean, err := h.resolveTVPath(src.absPath)
	if err != nil {
		http.Error(w, "file path is outside media root", http.StatusInternalServerError)
		return
	}
	st, err := os.Stat(clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	dur := h.tvDuration(r.Context(), src, clean)
	if dur <= 0 {
		httpError(w, http.StatusInternalServerError, "transcode failed", "unknown source duration", "handler", "streamTVHLS", "file_id", fileID)
		return
	}

	if strings.HasSuffix(name, ".m3u8") {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			return
		}
		fmt.Fprint(w, video.VODPlaylist(dur))
		return
	}

	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if r.Method == http.MethodHead {
		return // don't transcode a segment just to answer a HEAD
	}
	index, err := segmentIndex(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	segPath, err := video.EnsureSegment(r.Context(), h.tvHLSCacheRoot(), clean, st.ModTime(), st.Size(), 1080, index, dur)
	if err != nil {
		if r.Context().Err() != nil {
			return // client gave up while the segment built
		}
		httpError(w, http.StatusInternalServerError, "transcode failed", "hls segment", "handler", "streamTVHLS", "file_id", fileID, "seg", index, "err", err)
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

// tvDuration returns the source's duration in seconds, preferring the stored
// probe (written at scan time) and falling back to a live ffprobe only when it's
// absent — the synthetic VOD manifest needs the full length up front.
func (h *Handler) tvDuration(ctx context.Context, src tvFileSource, clean string) float64 {
	var probe video.ProbeResult
	_ = json.Unmarshal([]byte(src.streamJSON), &probe)
	if d := durationSeconds(probe.Format.Duration); d > 0 {
		return d
	}
	if p, err := video.Probe(ctx, clean); err == nil {
		return durationSeconds(p.Format.Duration)
	}
	return 0
}

// segmentIndex parses the N from a "segNNNNN.ts" asset name (already whitelisted
// by hlsAssetName).
func segmentIndex(name string) (int, error) {
	return strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "seg"), ".ts"))
}

// streamTVSubtitles extracts a text subtitle track and streams it as WebVTT.
func (h *Handler) streamTVSubtitles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fileID, err := pathID(r, "/stream/tv-subtitles/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	track := atoiDefault(r.URL.Query().Get("track"), 1)
	if track < 1 {
		track = 1
	}
	src, err := h.loadTVFileSource(r.Context(), fileID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	clean, err := h.resolveTVPath(src.absPath)
	if err != nil {
		http.Error(w, "file path is outside media root", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-i", clean,
		"-map", fmt.Sprintf("0:s:%d", track-1),
		"-f", "webvtt", "pipe:1",
	}
	if err := video.StreamFFmpeg(r.Context(), w, args); err != nil {
		slog.Warn("tv subtitle extract", "file_id", fileID, "track", track, "err", err)
	}
}

func audioTracks(p *video.ProbeResult) []sessionTrack {
	return tracksOfType(p, "audio", false)
}

func subtitleTracks(p *video.ProbeResult) []sessionTrack {
	return tracksOfType(p, "subtitle", true)
}

func tracksOfType(p *video.ProbeResult, codecType string, classifyText bool) []sessionTrack {
	if p == nil {
		return nil
	}
	var out []sessionTrack
	ordinal := 0
	for _, s := range p.Streams {
		if !strings.EqualFold(s.CodecType, codecType) {
			continue
		}
		ordinal++
		t := sessionTrack{
			Ordinal:  ordinal,
			Codec:    strings.ToLower(s.CodecName),
			Language: s.Language,
			Title:    s.Title,
			Default:  s.IsDefault,
		}
		if classifyText {
			t.Text = playback.IsTextSubtitle(s.CodecName)
		}
		out = append(out, t)
	}
	return out
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func durationSeconds(s string) float64 {
	if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return f
	}
	return 0
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

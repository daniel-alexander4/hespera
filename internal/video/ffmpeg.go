package video

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	hlsSegmentSeconds = 6
	segBuildTimeout   = 5 * time.Minute // ceiling for one on-demand segment transcode
	buildTimeout      = 2 * time.Hour   // staleness threshold for sweeping orphaned .ts.tmp segment builds
)

// StreamFFmpeg runs ffmpeg with the given args, copying its stdout to w. It
// holds a foreground concurrency slot for the duration and, because it uses
// CommandContext, kills and reaps ffmpeg if ctx is canceled (e.g. the client
// disconnects) — so no orphaned encoder survives the request. A ctx-canceled
// run returns ctx.Err(), not a spurious ffmpeg error.
func StreamFFmpeg(ctx context.Context, w io.Writer, args []string) error {
	release, err := acquire(ctx)
	if err != nil {
		return fmt.Errorf("ffmpeg acquire slot: %w", err)
	}
	defer release()

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdout = w
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg: %w: %s", err, tail(errBuf.String(), 500))
	}
	return nil
}

// RemuxArgs builds ffmpeg args to repackage src into a fragmented MP4 streamed
// to stdout, keeping all codecs (direct-stream). The selected audio ordinal is
// 1-based; 0 lets ffmpeg pick the default audio track.
//
// startSec > 0 resumes a non-seekable progressive remux: input -ss fast-seeks to
// the keyframe at/before startSec and -avoid_negative_ts make_zero rebases the
// output timestamps to zero, so the stream begins near the saved position. Copy
// can only cut on keyframes, so the real start lands within one GOP of startSec;
// the player offsets reported progress by the requested start, so the saved
// position stays accurate to within a GOP (it does not drift across resumes).
func RemuxArgs(src string, audioOrdinal int, startSec float64) []string {
	args := []string{"-hide_banner", "-loglevel", "error"}
	if startSec > 0 {
		args = append(args, "-ss", strconv.FormatFloat(startSec, 'f', -1, 64))
	}
	args = append(args,
		"-i", src,
		"-map", "0:v:0", "-map", audioMap(audioOrdinal),
		"-c", "copy",
	)
	if startSec > 0 {
		args = append(args, "-avoid_negative_ts", "make_zero")
	}
	return append(args,
		"-movflags", "frag_keyframe+empty_moov+faststart",
		"-f", "mp4", "pipe:1",
	)
}

// BurnInArgs builds ffmpeg args to burn a bitmap subtitle (PGS/DVD/DVB) into the
// video and stream the result as a fragmented MP4 to stdout — the burn-in
// counterpart of RemuxArgs, used when a selected subtitle can't be delivered as
// a text sidecar. The whole file is decoded continuously from the start (no input
// -ss) so the subtitle decoder tracks display-set state across the timeline;
// bitmap subs are stateful, which is why this is one progressive transcode rather
// than the segment-on-demand HLS path (each segment's independent input seek
// drops still-active subs). subOrdinal is 1-based among subtitle streams;
// audioOrdinal is 1-based (0 = default). Video is scaled down to maxHeight after
// the overlay so the burned subs scale with it.
//
// startSec > 0 resumes the stream: input -ss seeks there and -avoid_negative_ts
// make_zero rebases output timestamps to zero. The re-encode is frame-accurate
// (unlike the copy remux), so the start is exact; the only cost is that a bitmap
// cue already on-screen before startSec won't reappear until its next display set
// — acceptable for a mid-episode resume.
func BurnInArgs(src string, subOrdinal, audioOrdinal, maxHeight int, startSec float64, srcChannels int) []string {
	subIdx := subOrdinal - 1
	if subIdx < 0 {
		subIdx = 0
	}
	filter := "[0:v:0][0:s:" + strconv.Itoa(subIdx) + "]overlay,scale=-2:'min(ih," + strconv.Itoa(maxHeight) + ")'[v]"
	args := []string{"-hide_banner", "-loglevel", "error"}
	if startSec > 0 {
		args = append(args, "-ss", strconv.FormatFloat(startSec, 'f', -1, 64))
	}
	args = append(args,
		"-i", src,
		"-filter_complex", filter,
		"-map", "[v]", "-map", audioMap(audioOrdinal),
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "160k",
	)
	args = append(args, audioFilterArgs(srcChannels)...)
	if startSec > 0 {
		args = append(args, "-avoid_negative_ts", "make_zero")
	}
	return append(args,
		"-movflags", "frag_keyframe+empty_moov+faststart",
		"-f", "mp4", "pipe:1",
	)
}

// SegmentArgs builds ffmpeg args to transcode a single HLS segment: the
// durSec-second window of src starting at startSec, re-encoded to H.264/AAC and
// written to outPath as MPEG-TS. The window is seeked accurately (input -ss), a
// keyframe is forced on its first frame so the segment is independently
// decodable, and -output_ts_offset places its timestamps at the segment's
// position on the full timeline so the player stitches segments seamlessly and
// the scrubber maps to real episode time. -fps_mode cfr keeps each full segment
// exactly hls_time of video, so the synthetic VOD manifest's EXTINF values stay
// accurate over a whole episode (no cumulative drift). Video is scaled down to
// maxHeight (never up). Producing any segment on demand at constant cost is what
// makes seeking work, regardless of how far into the file the segment sits.
func SegmentArgs(src, outPath string, startSec, durSec float64, maxHeight, audioOrdinal, srcChannels int) []string {
	ss := strconv.FormatFloat(startSec, 'f', -1, 64)
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	args = append(args, segPreInputArgs()...) // VAAPI device, before any input
	args = append(args,
		"-ss", ss, "-i", src, "-t", strconv.FormatFloat(durSec, 'f', -1, 64),
		"-map", "0:v:0", "-map", audioMap(audioOrdinal),
	)
	if hlsEncoder == "vaapi" {
		args = append(args, segVAAPIScaleArgs(maxHeight)...)
		args = append(args, segVAAPIArgs...)
	} else {
		args = append(args, segScaleArgs(maxHeight)...)
		args = append(args,
			// Cap the encoder threads so each on-demand segment doesn't burst across
			// every core. Unthrottled, libx264 grabs ~all cores and finishes a 6s
			// segment in ~1.3s, then idles — a tall CPU spike every segment, worse when
			// hls.js prefetches two at once (concurrent encodes oversubscribe the box).
			// -threads 3 keeps each encode to ~3 cores (~28% lower peak) while still
			// finishing a 6s segment in ~1.7s (well under realtime), so seek latency and
			// buffer-ahead are unaffected. (Bump segEncodeVersion when changing this.)
			"-threads", "3",
		)
		args = append(args, segX264Args...)
	}
	args = append(args,
		"-force_key_frames", "expr:eq(n,0)",
		"-c:a", "aac", "-b:a", "160k",
	)
	args = append(args, audioFilterArgs(srcChannels)...)
	return append(args,
		// -avoid_negative_ts disabled is load-bearing, NOT cosmetic. mpegts's
		// default avoid_negative_ts shifts a whole segment *up* by the AAC encoder
		// priming (~21ms of slightly-negative audio PTS) to keep audio
		// non-negative. On segment 0 (output offset 0) that shift lands the VIDEO
		// at ~0.0213s instead of 0; a high-frame-rate source (≥~48fps) then fits
		// enough frames that the segment's tail overruns the next segment's
		// force-anchored boundary (e.g. 50fps: seg0 ends 6.0013 > seg1's 6.000),
		// so DTS goes backward across the MSE append → "Parsed buffers not in DTS
		// sequence" → MediaError 3, playback never starts (Doctor Who 50fps
		// episodes). 25/30fps end safely under the boundary, which masked it.
		// Disabling the shift keeps each segment's video on exactly [i·6,(i+1)·6);
		// seg0's audio keeps its native (slightly negative) priming PTS, which the
		// decoder trims as encoder delay. (Bump segEncodeVersion when changing this.)
		"-avoid_negative_ts", "disabled",
		"-output_ts_offset", ss, "-muxdelay", "0", "-muxpreload", "0",
		"-f", "mpegts", outPath,
	)
}

// segWarmupLead is how far before an interior segment's boundary the audio encode
// starts, so the AAC encoder's startup priming lands on the discarded lead-in and
// the segment's real audio begins warm (see buildSegment).
const segWarmupLead = 0.5

// buildSegment transcodes one HLS segment to tmp. Interior segments (start > 0) use
// a two-pass audio warm-up so the segment join carries real audio instead of AAC
// encoder priming: re-encoding each segment independently otherwise re-primes, and
// that ~37ms of priming overwrites real audio at every 6s join (an audible per-6s
// click). Pass 1 encodes audio from start−segWarmupLead (warming the encoder
// through the lead-in); pass 2 stream-copies that audio from the boundary (dropping
// the warm-up and its priming) and muxes a freshly-encoded video segment.
// Segment 0 has no room for a lead-in — its priming sits at negative PTS and the
// decoder trims it — so it keeps the single-pass SegmentArgs.
func buildSegment(ctx context.Context, src, tmp string, start, dur float64, maxHeight, audioOrdinal, srcChannels, index int) error {
	if start <= 0 {
		return runFFmpegSegment(ctx, SegmentArgs(src, tmp, start, dur, maxHeight, audioOrdinal, srcChannels), index)
	}
	// Name the audio temp so PruneCache's ".seg*.tmp" sweep reaps it if a hard kill
	// skips the defer (the defer covers normal completion, ctx timeout, and errors).
	atmp := tmp + ".aud.tmp"
	defer os.Remove(atmp)
	if err := runFFmpegSegment(ctx, audioWarmArgs(src, atmp, start, dur, audioOrdinal, srcChannels), index); err != nil {
		return err
	}
	return runFFmpegSegment(ctx, segmentMuxArgs(src, atmp, tmp, start, dur, maxHeight), index)
}

// runFFmpegSegment runs one ffmpeg invocation for a segment build, wrapping any
// failure with the segment index and a tail of stderr.
func runFFmpegSegment(ctx context.Context, args []string, index int) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg segment %d: %w: %s", index, err, tail(errBuf.String(), 300))
	}
	return nil
}

// audioWarmArgs encodes [start−segWarmupLead, start+dur] of audio to a temp mpegts.
// The AAC encoder primes at the lead-in start; by the segment boundary it's warm.
func audioWarmArgs(src, out string, start, dur float64, audioOrdinal, srcChannels int) []string {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-ss", strconv.FormatFloat(start-segWarmupLead, 'f', -1, 64),
		"-i", src,
		"-t", strconv.FormatFloat(dur+segWarmupLead, 'f', -1, 64),
		"-map", audioMap(audioOrdinal),
		"-c:a", "aac", "-b:a", "160k",
	}
	args = append(args, audioFilterArgs(srcChannels)...)
	return append(args, "-muxdelay", "0", "-muxpreload", "0", "-f", "mpegts", out)
}

// segmentMuxArgs stream-copies the warmed audio (dropping the lead-in via an input
// -ss, which on a copy trims whole AAC frames — no re-encode, so the warm frames
// and their absence of fresh priming survive) and muxes a freshly-encoded video
// segment, placed on the timeline at start.
func segmentMuxArgs(src, audioTmp, out string, start, dur float64, maxHeight int) []string {
	ss := strconv.FormatFloat(start, 'f', -1, 64)
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	args = append(args, segPreInputArgs()...) // VAAPI device, before any input
	args = append(args,
		"-ss", strconv.FormatFloat(segWarmupLead, 'f', -1, 64), "-i", audioTmp,
		"-ss", ss, "-i", src, "-t", strconv.FormatFloat(dur, 'f', -1, 64),
		"-map", "0:a:0", "-map", "1:v:0",
		"-c:a", "copy",
	)
	if hlsEncoder == "vaapi" {
		args = append(args, segVAAPIScaleArgs(maxHeight)...)
		args = append(args, segVAAPIArgs...)
	} else {
		args = append(args, segScaleArgs(maxHeight)...)
		args = append(args, segX264Args...)
		args = append(args, "-threads", "3") // match SegmentArgs: cap the per-segment encode CPU spike
	}
	return append(args,
		"-force_key_frames", "expr:eq(n,0)",
		"-avoid_negative_ts", "disabled",
		"-output_ts_offset", ss, "-muxdelay", "0", "-muxpreload", "0",
		"-f", "mpegts", out,
	)
}

// segScaleArgs and segX264Args are the video-encode fragments shared verbatim
// by SegmentArgs (seg0 / single-pass) and segmentMuxArgs (interior pass 2).
// They exist so the two builders can never drift apart on the flags that must
// stay identical across a segment boundary — a one-sided edit would desync
// seg0 from seg1 in ways segEncodeVersion can't catch. -threads 3 and
// -force_key_frames stay at each call site (their historical argv positions
// differ; keeping them in place keeps the argv byte-identical).
func segScaleArgs(maxHeight int) []string {
	return []string{"-vf", "scale=-2:'min(ih," + strconv.Itoa(maxHeight) + ")'", "-fps_mode", "cfr"}
}

// segX264Args: the encoder settings proper. No B-frames — each segment is
// encoded independently and placed on the timeline with -output_ts_offset;
// with B-frames the reorder makes the segment's first DTS land ~1
// reorder-depth *before* its boundary, so adjacent segments overlap in DTS and
// Chrome's MediaSource rejects the append ("Parsed buffers not in DTS
// sequence" → MediaError 3, playback never starts — worst on a mid-timeline
// resume). With -bf 0, DTS==PTS. Negligible compression cost; standard for
// segmented on-demand transcode. (Bump segEncodeVersion when changing any of
// these.)
var segX264Args = []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-pix_fmt", "yuv420p", "-bf", "0"}

// hlsEncoder selects the segment video encoder: "software" (libx264, the
// default) or "vaapi" (h264_vaapi on hlsVAAPIDevice, opt-in via
// HESPERA_HLS_ENCODER). Set once at startup by SetEncoder, which capability-
// probes VAAPI and falls back to software — a broken driver must degrade, not
// break playback. The encoder is folded into hlsKey, so segments from
// different encoders never mix (and switching back and forth can't poison the
// cache). The burn-in path stays libx264 regardless: its subtitle overlay is a
// software filter, so the GPU would gain little there.
var hlsEncoder = "software"

const hlsVAAPIDevice = "/dev/dri/renderD128"

// segPreInputArgs: encoder flags that must precede the inputs (the VAAPI
// device has to exist before any filter references it). Empty for software,
// keeping that argv byte-identical.
func segPreInputArgs() []string {
	if hlsEncoder == "vaapi" {
		return []string{"-vaapi_device", hlsVAAPIDevice}
	}
	return nil
}

// segVAAPIScaleArgs keeps the same proven software scale expression (cheap on
// CPU next to the encode), then uploads to the GPU for the encoder.
func segVAAPIScaleArgs(maxHeight int) []string {
	return []string{"-vf", "scale=-2:'min(ih," + strconv.Itoa(maxHeight) + ")',format=nv12,hwupload", "-fps_mode", "cfr"}
}

// segVAAPIArgs: CQP 23 ≈ x264 crf 21; -bf 0 for the same DTS==PTS segment-
// boundary requirement segX264Args documents. -threads is meaningless on the
// GPU and omitted.
var segVAAPIArgs = []string{"-c:v", "h264_vaapi", "-rc_mode", "CQP", "-qp", "23", "-bf", "0"}

// SetEncoder selects the HLS segment encoder at startup. "vaapi" is verified
// with a one-frame test encode against the device; any failure logs a WARN and
// falls back to software. Returns the effective encoder.
func SetEncoder(ctx context.Context, name string) string {
	if name != "vaapi" {
		hlsEncoder = "software"
		return hlsEncoder
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-vaapi_device", hlsVAAPIDevice,
		"-f", "lavfi", "-i", "color=black:size=320x180:rate=25:duration=0.2",
		"-vf", "format=nv12,hwupload", "-c:v", "h264_vaapi", "-f", "null", "-")
	if out, err := cmd.CombinedOutput(); err != nil {
		slog.Warn("vaapi encoder unavailable — falling back to software",
			"device", hlsVAAPIDevice, "err", err, "detail", tail(string(out), 200))
		hlsEncoder = "software"
		return hlsEncoder
	}
	hlsEncoder = "vaapi"
	return hlsEncoder
}

// VODPlaylist synthesises the complete VOD HLS manifest for a source of the
// given duration: every hls_time segment listed up front, finalised with
// #EXT-X-ENDLIST, so the player knows the full episode length immediately and
// can seek anywhere. The segments themselves are produced on demand as the
// player requests them (see EnsureSegment) — the manifest is pure computation,
// no transcode needed to serve it. Returns "" if duration is not positive.
//
// audioOrdinal (1-based; 0 = default) is carried as a ?aud query on each segment
// URI so the selected audio track reaches the per-segment transcode — hls.js
// keeps the query when resolving the relative segment names, and the cache key
// includes it, so each track gets its own segments.
func VODPlaylist(durationSec float64, audioOrdinal int) string {
	if durationSec <= 0 {
		return ""
	}
	q := ""
	if audioOrdinal > 0 {
		q = "?aud=" + strconv.Itoa(audioOrdinal)
	}
	const seg = float64(hlsSegmentSeconds)
	n := int(math.Ceil(durationSec / seg))
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n", hlsSegmentSeconds)
	for i := 0; i < n; i++ {
		d := seg
		if rem := durationSec - float64(i)*seg; rem < seg {
			d = rem
		}
		fmt.Fprintf(&b, "#EXTINF:%.6f,\nseg%05d.ts%s\n", d, i, q)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

func audioMap(ordinal int) string {
	if ordinal >= 1 {
		return "0:a:" + strconv.Itoa(ordinal-1) + "?"
	}
	return "0:a:0?"
}

// audioFilterArgs builds the audio filter chain every transcode path shares
// (segment build, warm-up, burn-in): a gap-fill front-half + a downmix.
//
// GAP-FILL (`aresample=async=1`): silence-fills gaps in the source audio and
// keeps the output locked to its PTS, so each segment emits exactly its window
// of audio. Without it, a source with damaged/missing audio packets (a corrupt
// region, a bad rip) yields a segment whose audio is short of its declared
// duration — an audio-buffer *hole* the browser's MSE renderer resyncs by
// slowing (pitching down) the audio, which sticks until a reload rebuilds the
// pipeline (the "deep voices" bug). Filling the hole with silence keeps A/V in
// sync; a damaged region degrades to a brief silence instead. Near-transparent
// on gap-free audio (only acts on real gaps/drift).
//
// DOWNMIX: a naive `-ac 2` fold of a 5.1 source puts the centre channel (where
// dialogue lives) ~3 dB *below* the front L/R (music/effects) — buried dialogue
// once a browser that can't decode Dolby transcodes to stereo. For a source with
// a centre channel (>=6ch: 5.1/6.1/7.1) we fold with a dialogue-forward `pan`
// (centre weighted above the fronts, surrounds/LFE dropped) — clip-safe (coeffs
// <1) and layout-agnostic (names only FC/FL/FR, present in every >=6ch layout).
// The gate is >=6 not >2 because a `pan` naming FC would *error* on a centre-less
// 3-5ch layout. <6ch keeps the standard `-ac 2` fold.
func audioFilterArgs(srcChannels int) []string {
	const sync = "aresample=async=1"
	if srcChannels >= 6 {
		return []string{"-af", sync + ",pan=stereo|FL=0.7*FC+0.5*FL|FR=0.7*FC+0.5*FR"}
	}
	return []string{"-af", sync, "-ac", "2"}
}

// segBuild tracks one in-flight segment transcode so concurrent callers for the
// same segment share it (e.g. hls.js prefetch racing a seek to the same point).
type segBuild struct {
	ready chan struct{} // closed once the segment is on disk or the build failed
	err   error
}

var (
	segBuildsMu sync.Mutex
	segBuilds   = map[string]*segBuild{}
)

// EnsureSegment ensures segment index of a segment-on-demand HLS transcode of
// src exists under cacheRoot (same key as the manifest) and returns its path.
// Segments are produced lazily — one ffmpeg invocation per hls_time window — so
// a seek to any point costs only that segment's transcode, not a wait for a
// linear encode to reach it. A finished segment is cached and reused; concurrent
// callers for the same segment share one build. The transcode writes to a temp
// file and atomically renames, so a partial segment is never served, and it runs
// on its own context so a near-done segment still caches if the caller gives up.
func EnsureSegment(ctx context.Context, cacheRoot, src string, modTime time.Time, size int64, maxHeight, index int, totalDur float64, srcChannels, audioOrdinal int) (string, error) {
	dir := filepath.Join(cacheRoot, hlsKey(src, modTime, size, maxHeight, audioOrdinal))
	path := filepath.Join(dir, fmt.Sprintf("seg%05d.ts", index))
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		touch(dir)
		return path, nil
	}
	b := startOrJoinSegBuild(dir, src, maxHeight, index, totalDur, srcChannels, audioOrdinal)
	select {
	case <-b.ready:
	case <-ctx.Done():
		return "", ctx.Err() // caller gave up; the build continues for the cache
	}
	if b.err != nil {
		return "", b.err
	}
	touch(dir)
	return path, nil
}

func startOrJoinSegBuild(dir, src string, maxHeight, index int, totalDur float64, srcChannels, audioOrdinal int) *segBuild {
	key := dir + "|" + strconv.Itoa(index)
	segBuildsMu.Lock()
	defer segBuildsMu.Unlock()
	if b, ok := segBuilds[key]; ok {
		return b
	}
	b := &segBuild{ready: make(chan struct{})}
	segBuilds[key] = b
	go runSegBuild(b, key, dir, src, maxHeight, index, totalDur, srcChannels, audioOrdinal)
	return b
}

func runSegBuild(b *segBuild, key, dir, src string, maxHeight, index int, totalDur float64, srcChannels, audioOrdinal int) {
	defer func() {
		segBuildsMu.Lock()
		delete(segBuilds, key)
		segBuildsMu.Unlock()
	}()
	finished := false
	finish := func(err error) {
		if finished {
			return
		}
		finished = true
		b.err = err
		close(b.ready)
	}
	// This goroutine is spawned from a request handler, so net/http's
	// per-request recovery does not cover it — an unrecovered panic here would
	// crash the whole process AND leave every joined caller blocked on
	// b.ready. Convert a panic into a build failure instead (the finished
	// guard makes finish safe to call from the recover path).
	defer func() {
		if r := recover(); r != nil {
			slog.Error("segment build panicked", "index", index, "panic", r, "stack", string(debug.Stack()))
			finish(fmt.Errorf("segment %d build panicked: %v", index, r))
		}
	}()

	start := float64(index * hlsSegmentSeconds)
	dur := float64(hlsSegmentSeconds)
	if rem := totalDur - start; rem < dur {
		dur = rem
	}
	if dur <= 0 {
		finish(fmt.Errorf("segment %d out of range (duration %.3f)", index, totalDur))
		return
	}

	buildCtx, cancel := context.WithTimeout(context.Background(), segBuildTimeout)
	defer cancel()
	// Take the segment sub-cap first, then the general gate (consistent order, so
	// nesting can't deadlock). The sub-cap serialises prefetch bursts — without it
	// hls.js's burst of segment requests each spawns a transcode and they saturate
	// the CPU; the general gate is still the overall ffmpeg ceiling.
	releaseSeg, err := acquireSegment(buildCtx)
	if err != nil {
		finish(err)
		return
	}
	defer releaseSeg()
	release, err := acquire(buildCtx)
	if err != nil {
		finish(err)
		return
	}
	defer release()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		finish(err)
		return
	}
	final := filepath.Join(dir, fmt.Sprintf("seg%05d.ts", index))
	tmp := filepath.Join(dir, fmt.Sprintf(".seg%05d.ts.tmp", index))
	if err := buildSegment(buildCtx, src, tmp, start, dur, maxHeight, audioOrdinal, srcChannels, index); err != nil {
		_ = os.Remove(tmp)
		finish(err)
		return
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		finish(err)
		return
	}
	finish(nil)
}

// segEncodeVersion is part of the segment cache key (hlsKey). Bump it whenever
// SegmentArgs changes the encoded segment bytes in a way that must NOT mix with
// previously-cached segments — the cache key otherwise keys only on the source +
// downscale + audio track, so a stale segment from an older encoder would be
// served (and, at a boundary with a new one, could re-introduce the very DTS
// discontinuity the encoder change fixes). Bumping orphans the old segments;
// PruneCache reaps them. v1: added -bf 0 for monotonic cross-segment DTS.
// v2: added -avoid_negative_ts disabled so a high-fps segment 0 doesn't overrun
// its boundary (the mpegts priming up-shift); fixes 50fps episodes not playing.
// v3: capped encoder to -threads 3 to flatten the per-segment CPU spike (changes
// the encoded bytes, so old all-cores segments must not mix with new ones).
// v4: interior segments now use a two-pass audio warm-up (buildSegment) so joins
// carry real audio instead of AAC priming — changes the audio bytes at every join.
// v5: audio filter gained aresample=async=1 (audioFilterArgs) to silence-fill
// source audio gaps so a segment always emits its full window — changes audio bytes.
const segEncodeVersion = 5

func hlsKey(src string, modTime time.Time, size int64, maxHeight, audioOrdinal int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%d|%d|%s|v%d", src, modTime.UnixNano(), size, maxHeight, audioOrdinal, hlsEncoder, segEncodeVersion)))
	return hex.EncodeToString(h[:8])
}

func touch(dir string) {
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
}

// PruneCache keeps the asset directories under root within maxBytes and expires
// any older than maxAge. Whole directories are the eviction unit; over-budget
// eviction is oldest-first but skips directories touched within a short grace
// window, so an asset being actively served — or still being transcoded (its
// mtime keeps advancing as segments are written) — is never pulled out from
// under a request. Legacy build temp dirs older than the build timeout are swept.
func PruneCache(root string, maxBytes int64, maxAge time.Duration) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	const grace = 2 * time.Minute
	now := time.Now()

	type item struct {
		path string
		size int64
		mod  time.Time
	}
	var items []item
	var total int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name())
		removeStaleSegTemps(p, now) // sweep killed-build orphans before sizing
		size, mod := dirSizeAndMod(p)
		if maxAge > 0 && now.Sub(mod) > maxAge {
			_ = os.RemoveAll(p)
			continue
		}
		items = append(items, item{p, size, mod})
		total += size
	}

	if maxBytes <= 0 || total <= maxBytes {
		return nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	for _, it := range items {
		if total <= maxBytes {
			break
		}
		if now.Sub(it.mod) < grace {
			continue
		}
		if err := os.RemoveAll(it.path); err == nil {
			total -= it.size
		}
	}
	return nil
}

// removeStaleSegTemps deletes orphaned segment temp files (.segNNNNN.ts.tmp
// and the two-pass audio-warm .segNNNNN.ts.tmp.aud.tmp) left inside an asset
// dir by a hard-killed build (SIGKILL/OOM between the temp write and the
// atomic rename, so the in-goroutine os.Remove never ran). Matched by the
// ".seg" prefix + ".tmp" suffix so every temp shape a build creates is
// covered; real segments (segNNNNN.ts, no leading dot) never match. Only
// files older than buildTimeout are swept — comfortably past segBuildTimeout,
// so an in-flight build's temp is never deleted out from under it.
func removeStaleSegTemps(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), ".seg") || !strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= buildTimeout {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

func dirSizeAndMod(dir string) (int64, time.Time) {
	var size int64
	var newest time.Time
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			size += info.Size()
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return size, newest
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

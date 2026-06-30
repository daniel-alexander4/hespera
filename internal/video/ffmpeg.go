package video

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
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
	args = append(args, downmixArgs(srcChannels)...)
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
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-ss", ss, "-i", src, "-t", strconv.FormatFloat(durSec, 'f', -1, 64),
		"-map", "0:v:0", "-map", audioMap(audioOrdinal),
		"-vf", "scale=-2:'min(ih," + strconv.Itoa(maxHeight) + ")'",
		"-fps_mode", "cfr",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-pix_fmt", "yuv420p",
		// No B-frames. Each segment is encoded independently and placed on the
		// timeline with -output_ts_offset; with B-frames the reorder makes the
		// segment's first DTS land ~1 reorder-depth *before* its boundary, so
		// adjacent segments overlap in DTS and Chrome's MediaSource rejects the
		// append ("Parsed buffers not in DTS sequence" → MediaError 3, playback
		// never starts — worst on a mid-timeline resume). With -bf 0, DTS==PTS and
		// each segment starts exactly on its boundary, so segments are contiguous
		// and monotonic. Negligible compression cost; standard for segmented
		// on-demand transcode. (Bump segEncodeVersion when changing this.)
		"-bf", "0",
		"-force_key_frames", "expr:eq(n,0)",
		"-c:a", "aac", "-b:a", "160k",
	}
	args = append(args, downmixArgs(srcChannels)...)
	return append(args,
		"-output_ts_offset", ss, "-muxdelay", "0", "-muxpreload", "0",
		"-f", "mpegts", outPath,
	)
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

// downmixArgs picks how a transcode folds the source audio to stereo. A naive
// `-ac 2` downmix of a 5.1 source puts the centre channel (where dialogue lives)
// ~3 dB *below* the front L/R (music/effects), which is why TV dialogue sounds
// buried once a browser that can't decode Dolby transcodes it to stereo. For a
// source with a centre channel (>=6 channels: 5.1/6.1/7.1) we instead fold with
// a dialogue-forward `pan` — centre weighted above the fronts, surrounds/LFE
// dropped — which is clip-safe (coefficients <1) and layout-agnostic (it names
// only FC/FL/FR, present in every >=6-channel layout, so it never references a
// back-vs-side channel that may be absent). The gate is >=6, not >2, because a
// `pan` naming FC would *error* on a 3-5-channel layout that lacks a centre.
// Sources with <6 channels keep the standard `-ac 2` fold unchanged.
func downmixArgs(srcChannels int) []string {
	if srcChannels >= 6 {
		return []string{"-af", "pan=stereo|FL=0.7*FC+0.5*FL|FR=0.7*FC+0.5*FR"}
	}
	return []string{"-ac", "2"}
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
	finish := func(err error) { b.err = err; close(b.ready) }

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
	cmd := exec.CommandContext(buildCtx, "ffmpeg", SegmentArgs(src, tmp, start, dur, maxHeight, audioOrdinal, srcChannels)...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		finish(fmt.Errorf("ffmpeg segment %d: %w: %s", index, err, tail(errBuf.String(), 300)))
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
const segEncodeVersion = 1

func hlsKey(src string, modTime time.Time, size int64, maxHeight, audioOrdinal int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%d|%d|v%d", src, modTime.UnixNano(), size, maxHeight, audioOrdinal, segEncodeVersion)))
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

// removeStaleSegTemps deletes orphaned segment temp files (.segNNNNN.ts.tmp) left
// inside an asset dir by a hard-killed build (SIGKILL/OOM between the temp write
// and the atomic rename, so the in-goroutine os.Remove never ran). Only files
// older than buildTimeout are swept — comfortably past segBuildTimeout, so an
// in-flight build's temp is never deleted out from under it.
func removeStaleSegTemps(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts.tmp") {
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

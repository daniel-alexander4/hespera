package video

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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
	hlsPlaylistName   = "index.m3u8"
	buildTimeout      = 2 * time.Hour
	tmpDirPrefix      = ".build-"
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
func RemuxArgs(src string, audioOrdinal int) []string {
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-i", src,
		"-map", "0:v:0", "-map", audioMap(audioOrdinal),
		"-c", "copy",
		"-movflags", "frag_keyframe+empty_moov+faststart",
		"-f", "mp4", "pipe:1",
	}
}

// HLSArgs builds ffmpeg args for a single-rendition HLS transcode of src into
// outDir. It uses an *event* playlist: ffmpeg appends each segment to the
// playlist as it is written and finalises it with #EXT-X-ENDLIST when the encode
// completes — so playback can begin from the first segment while the rest is
// still transcoding (progressive start), yet it remains a single continuous
// encode (timestamps, keyframes, and segment durations all correct). Video is
// scaled down to maxHeight (never up) and re-encoded to H.264/AAC.
func HLSArgs(src, outDir string, maxHeight, audioOrdinal int) []string {
	return []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", src,
		"-map", "0:v:0", "-map", audioMap(audioOrdinal),
		"-vf", "scale=-2:'min(ih," + strconv.Itoa(maxHeight) + ")'",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-pix_fmt", "yuv420p",
		// Force a keyframe every hls_time seconds so segments are actually that
		// length (otherwise libx264's default GOP dictates boundaries, making the
		// first segment — and thus startup latency and seek granularity — coarse).
		"-force_key_frames", "expr:gte(t,n_forced*" + strconv.Itoa(hlsSegmentSeconds) + ")",
		"-c:a", "aac", "-ac", "2", "-b:a", "160k",
		"-f", "hls",
		"-hls_time", strconv.Itoa(hlsSegmentSeconds),
		"-hls_playlist_type", "event",
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", filepath.Join(outDir, "seg%05d.ts"),
		filepath.Join(outDir, hlsPlaylistName),
	}
}

func audioMap(ordinal int) string {
	if ordinal >= 1 {
		return "0:a:" + strconv.Itoa(ordinal-1) + "?"
	}
	return "0:a:0?"
}

// HLSDir returns the cache directory for a source without starting a build —
// used to serve already-listed segments (the playlist only references segments
// that exist) without re-triggering EnsureHLS.
func HLSDir(cacheRoot, src string, modTime time.Time, size int64, maxHeight int) string {
	return filepath.Join(cacheRoot, hlsKey(src, modTime, size, maxHeight))
}

// hlsBuild tracks one in-flight transcode so concurrent callers share it.
type hlsBuild struct {
	ready chan struct{} // closed once the playlist is playable (≥1 segment) or the build failed
	err   error         // non-nil only if the build failed before becoming playable
}

var (
	hlsBuildsMu sync.Mutex
	hlsBuilds   = map[string]*hlsBuild{}
)

// EnsureHLS ensures a progressive HLS transcode of src exists under cacheRoot
// (keyed by path+mtime+size+maxHeight) and returns its directory once playback
// can begin — i.e. as soon as the event playlist has its first segment, NOT
// after the whole file is transcoded. The encode continues in the background,
// appending segments; the client's playlist polls pick them up. Concurrent
// callers for the same source share a single build. A build that fails before
// becoming playable leaves no directory, so the next request retries cleanly.
func EnsureHLS(ctx context.Context, cacheRoot, src string, modTime time.Time, size int64, maxHeight int) (string, error) {
	key := hlsKey(src, modTime, size, maxHeight)
	dir := filepath.Join(cacheRoot, key)
	if hlsReady(dir) { // already fully transcoded
		touch(dir)
		return dir, nil
	}

	b := startOrJoinHLSBuild(key, dir, cacheRoot, src, maxHeight)
	select {
	case <-b.ready:
	case <-ctx.Done():
		return "", ctx.Err() // caller gave up; the build continues for the cache
	}
	if b.err != nil {
		return "", b.err
	}
	touch(dir)
	return dir, nil
}

func startOrJoinHLSBuild(key, dir, cacheRoot, src string, maxHeight int) *hlsBuild {
	hlsBuildsMu.Lock()
	defer hlsBuildsMu.Unlock()
	if b, ok := hlsBuilds[key]; ok {
		return b
	}
	b := &hlsBuild{ready: make(chan struct{})}
	hlsBuilds[key] = b
	go runHLSBuild(b, key, dir, cacheRoot, src, maxHeight)
	return b
}

func runHLSBuild(b *hlsBuild, key, dir, cacheRoot, src string, maxHeight int) {
	defer func() {
		hlsBuildsMu.Lock()
		delete(hlsBuilds, key)
		hlsBuildsMu.Unlock()
	}()

	readyOnce := sync.Once{}
	fail := func(err error) {
		readyOnce.Do(func() { b.err = err; close(b.ready) })
	}
	playable := func() { readyOnce.Do(func() { close(b.ready) }) }

	release, err := acquireBackground(context.Background())
	if err != nil {
		fail(err)
		return
	}
	defer release()

	_ = os.RemoveAll(dir) // clear any stale partial from a prior failed run
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(err)
		return
	}

	buildCtx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(buildCtx, "ffmpeg", HLSArgs(src, dir, maxHeight, 0)...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		fail(fmt.Errorf("ffmpeg start: %w", err))
		return
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case e := <-waitErr:
			// Build finished. If it errored before producing a complete playlist,
			// treat as failure and remove the dir so the next request rebuilds.
			if e != nil && !hlsReady(dir) {
				_ = os.RemoveAll(dir)
				fail(fmt.Errorf("ffmpeg hls: %w: %s", e, tail(errBuf.String(), 300)))
				return
			}
			playable() // complete (or already playable)
			return
		case <-ticker.C:
			if hlsPlayable(dir) {
				playable()
			}
		}
	}
}

// hlsReady reports whether dir holds a finished playlist (#EXT-X-ENDLIST, which
// ffmpeg writes only when the whole transcode completes).
func hlsReady(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, hlsPlaylistName))
	return err == nil && bytes.Contains(b, []byte("#EXT-X-ENDLIST"))
}

// hlsPlayable reports whether the playlist exists and references at least one
// segment — i.e. playback can begin even though the encode may still be running.
func hlsPlayable(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, hlsPlaylistName))
	return err == nil && bytes.Contains(b, []byte(".ts"))
}

func hlsKey(src string, modTime time.Time, size int64, maxHeight int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%d", src, modTime.UnixNano(), size, maxHeight)))
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
		size, mod := dirSizeAndMod(p)
		if strings.HasPrefix(e.Name(), tmpDirPrefix) {
			if now.Sub(mod) > buildTimeout {
				_ = os.RemoveAll(p) // crashed-build leftover
			}
			continue
		}
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

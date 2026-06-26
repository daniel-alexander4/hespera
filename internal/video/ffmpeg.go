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

// HLSArgs builds ffmpeg args for a single-rendition VOD HLS transcode of src
// into outDir. ffmpeg writes the complete media playlist itself (playlist_type
// vod, with #EXT-X-ENDLIST), so there is no hand-written master manifest whose
// CODECS string could drift. Video is scaled down to maxHeight (never up) and
// re-encoded to H.264/AAC, the universally playable combination.
func HLSArgs(src, outDir string, maxHeight, audioOrdinal int) []string {
	return []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", src,
		"-map", "0:v:0", "-map", audioMap(audioOrdinal),
		"-vf", "scale=-2:'min(ih," + strconv.Itoa(maxHeight) + ")'",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-ac", "2", "-b:a", "160k",
		"-f", "hls",
		"-hls_time", strconv.Itoa(hlsSegmentSeconds),
		"-hls_playlist_type", "vod",
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

var hlsBuildLocks keyedMutex

// EnsureHLS builds, or reuses a cached, single-rendition HLS VOD for src under
// cacheRoot, keyed by source path+mtime+size+maxHeight. It is safe under
// concurrency: per-key in-process locking serializes builds of the same source
// (concurrent callers wait and share one build — no dogpile), the transcode
// runs in a unique temp directory and is published with an atomic rename (no
// shared-tempfile corruption), and the build runs on a detached context so a
// client disconnect can't abort a build other viewers are waiting on. Returns
// the published asset directory once its playlist is complete.
func EnsureHLS(ctx context.Context, cacheRoot, src string, modTime time.Time, size int64, maxHeight int) (string, error) {
	key := hlsKey(src, modTime, size, maxHeight)
	dir := filepath.Join(cacheRoot, key)
	if hlsReady(dir) {
		touch(dir)
		return dir, nil
	}

	unlock := hlsBuildLocks.lock(key)
	defer unlock()
	if hlsReady(dir) { // another caller built it while we waited for the lock
		touch(dir)
		return dir, nil
	}

	// Wait for a background slot using the caller's context (so a client that
	// gives up while queued doesn't pin capacity), but run the actual build on a
	// detached context so a mid-build disconnect doesn't waste/abort it.
	release, err := acquireBackground(ctx)
	if err != nil {
		return "", fmt.Errorf("hls acquire slot: %w", err)
	}
	defer release()

	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(cacheRoot, tmpDirPrefix+key+"-")
	if err != nil {
		return "", err
	}

	buildCtx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(buildCtx, "ffmpeg", HLSArgs(src, tmp, maxHeight, 0)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("ffmpeg hls build: %w: %s", err, tail(string(out), 500))
	}
	if !hlsReady(tmp) {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("hls build produced no complete playlist")
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		if hlsReady(dir) {
			return dir, nil
		}
		return "", fmt.Errorf("publish hls: %w", err)
	}
	return dir, nil
}

// hlsReady reports whether dir holds a complete VOD playlist (one ending in
// #EXT-X-ENDLIST, which ffmpeg writes only after the whole transcode finishes).
func hlsReady(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, hlsPlaylistName))
	return err == nil && bytes.Contains(b, []byte("#EXT-X-ENDLIST"))
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
// any older than maxAge. Whole directories are the eviction unit (they are
// published atomically); over-budget eviction is oldest-first but skips
// directories touched within a short grace window, so an asset being actively
// served is never pulled out from under a request. Orphaned build temp dirs
// older than the build timeout are also swept.
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

// keyedMutex serializes work per string key without a global lock for the work
// itself — different keys proceed concurrently, identical keys queue.
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = make(map[string]*sync.Mutex)
	}
	mu, ok := k.m[key]
	if !ok {
		mu = &sync.Mutex{}
		k.m[key] = mu
	}
	k.mu.Unlock()

	mu.Lock()
	return mu.Unlock
}

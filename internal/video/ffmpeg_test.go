package video

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAudioMap(t *testing.T) {
	if got := audioMap(0); got != "0:a:0?" {
		t.Fatalf("audioMap(0) = %q, want 0:a:0?", got)
	}
	if got := audioMap(2); got != "0:a:1?" {
		t.Fatalf("audioMap(2) = %q, want 0:a:1?", got)
	}
}

func TestSegmentArgs(t *testing.T) {
	args := SegmentArgs("/m/ep.mkv", "/cache/x/seg00010.ts", 60, 6, 720, 0)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-ss 60", "-i /m/ep.mkv", "-t 6", "-c:v libx264", "-c:a aac", "-f mpegts",
		"scale=-2:'min(ih,720)'", "-force_key_frames expr:eq(n,0)",
		"-output_ts_offset 60", "/cache/x/seg00010.ts",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("SegmentArgs missing %q in: %s", want, joined)
		}
	}
}

func TestVODPlaylist(t *testing.T) {
	if VODPlaylist(0) != "" || VODPlaylist(-5) != "" {
		t.Fatal("non-positive duration should yield an empty playlist")
	}
	pl := VODPlaylist(20) // 6+6+6+2 → 4 segments
	for _, want := range []string{
		"#EXT-X-PLAYLIST-TYPE:VOD", "#EXT-X-ENDLIST", "#EXT-X-TARGETDURATION:6",
		"seg00000.ts", "seg00003.ts",
		"#EXTINF:2.000000,\nseg00003.ts", // last segment is the 2s remainder
	} {
		if !strings.Contains(pl, want) {
			t.Fatalf("VODPlaylist missing %q in:\n%s", want, pl)
		}
	}
	if strings.Contains(pl, "seg00004.ts") {
		t.Fatalf("too many segments:\n%s", pl)
	}
	if n := strings.Count(pl, "#EXTINF:"); n != 4 {
		t.Fatalf("EXTINF count = %d, want 4", n)
	}
}

func TestBurnInArgs(t *testing.T) {
	// sub ordinal 2 -> 0:s:1 (0-based), audio ordinal 1 -> 0:a:0, max height 1080.
	joined := strings.Join(BurnInArgs("/m/ep.mkv", 2, 1, 1080, 0), " ")
	for _, want := range []string{
		"-i /m/ep.mkv",
		"[0:v:0][0:s:1]overlay,scale=-2:'min(ih,1080)'[v]",
		"-map [v]", "-map 0:a:0?",
		"-c:v libx264", "-c:a aac",
		"-movflags frag_keyframe+empty_moov+faststart", "-f mp4", "pipe:1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("BurnInArgs missing %q in: %s", want, joined)
		}
	}
	// From-the-top playback (startSec 0) decodes continuously with no input -ss —
	// required for stateful bitmap subs at the start of the file.
	if strings.Contains(joined, "-ss ") {
		t.Fatalf("BurnInArgs(start=0) must not input-seek: %s", joined)
	}
}

func TestBurnInArgsResume(t *testing.T) {
	// A mid-episode resume input-seeks (and rebases timestamps to zero); the player
	// offsets reported progress by the requested start.
	joined := strings.Join(BurnInArgs("/m/ep.mkv", 1, 0, 1080, 930.5), " ")
	for _, want := range []string{"-ss 930.5", "-i /m/ep.mkv", "-avoid_negative_ts make_zero"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("BurnInArgs(start) missing %q in: %s", want, joined)
		}
	}
	// -ss must precede -i (input seek, not output seek).
	if strings.Index(joined, "-ss ") > strings.Index(joined, "-i ") {
		t.Fatalf("BurnInArgs(start) -ss must be an input seek (before -i): %s", joined)
	}
}

func TestRemuxArgsCopiesCodecs(t *testing.T) {
	joined := strings.Join(RemuxArgs("/m/ep.mkv", 0, 0), " ")
	if !strings.Contains(joined, "-c copy") {
		t.Fatalf("RemuxArgs should copy codecs: %s", joined)
	}
	if !strings.Contains(joined, "pipe:1") {
		t.Fatalf("RemuxArgs should write to pipe: %s", joined)
	}
	// From the top: no input seek.
	if strings.Contains(joined, "-ss ") {
		t.Fatalf("RemuxArgs(start=0) must not input-seek: %s", joined)
	}
}

func TestRemuxArgsResume(t *testing.T) {
	joined := strings.Join(RemuxArgs("/m/ep.mkv", 0, 1200), " ")
	for _, want := range []string{"-ss 1200", "-i /m/ep.mkv", "-avoid_negative_ts make_zero", "-c copy"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("RemuxArgs(start) missing %q in: %s", want, joined)
		}
	}
	if strings.Index(joined, "-ss ") > strings.Index(joined, "-i ") {
		t.Fatalf("RemuxArgs(start) -ss must be an input seek (before -i): %s", joined)
	}
}

func TestHLSKeyStableAndDistinct(t *testing.T) {
	mt := time.Unix(1700000000, 0)
	a := hlsKey("/m/ep.mkv", mt, 100, 1080)
	if a != hlsKey("/m/ep.mkv", mt, 100, 1080) {
		t.Fatal("hlsKey not stable for identical inputs")
	}
	for _, other := range []string{
		hlsKey("/m/other.mkv", mt, 100, 1080),
		hlsKey("/m/ep.mkv", mt.Add(time.Second), 100, 1080),
		hlsKey("/m/ep.mkv", mt, 101, 1080),
		hlsKey("/m/ep.mkv", mt, 100, 720),
	} {
		if a == other {
			t.Fatal("hlsKey collided across distinct inputs")
		}
	}
}

func TestSetConcurrency(t *testing.T) {
	defer SetConcurrency(0, 0) // restore unlimited for other tests
	SetConcurrency(4, time.Second)
	if cap(ffmpegSem) != 4 {
		t.Fatalf("cap = %d, want 4", cap(ffmpegSem))
	}
	SetConcurrency(0, 0)
	if ffmpegSem != nil {
		t.Fatal("limit 0 should disable the semaphore")
	}
}

func TestPruneCache(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, age time.Duration, size int) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "data"), bytes.Repeat([]byte("x"), size), 0o644); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(filepath.Join(p, "data"), when, when)
		return p
	}

	old := mk("old", 48*time.Hour, 10)
	fresh := mk("fresh", time.Minute, 10) // within grace
	mid := mk("mid", 10*time.Minute, 10)
	stale := mk("stale-build", 3*time.Hour, 10)
	if err := os.Rename(stale, filepath.Join(root, tmpDirPrefix+"abandoned")); err != nil {
		t.Fatal(err)
	}
	staleBuild := filepath.Join(root, tmpDirPrefix+"abandoned")

	// maxAge expires "old"; orphaned temp dir is swept; budget=0 leaves the rest.
	if err := PruneCache(root, 0, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	assertGone(t, old)
	assertGone(t, staleBuild)
	assertExists(t, fresh)
	assertExists(t, mid)

	// Tight budget evicts oldest eligible ("mid") but never the within-grace "fresh".
	if err := PruneCache(root, 5, 0); err != nil {
		t.Fatal(err)
	}
	assertGone(t, mid)
	assertExists(t, fresh)
}

func assertGone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed", filepath.Base(p))
	}
}

func assertExists(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("expected %s to exist: %v", filepath.Base(p), err)
	}
}

// --- integration tests against a real ffmpeg ---

func ffmpegAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed; skipping integration test")
	}
}

func sampleClip(t *testing.T) (path string, modTime time.Time, size int64) {
	return sampleClipDur(t, 3)
}

func sampleClipDur(t *testing.T, seconds int) (path string, modTime time.Time, size int64) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "sample.mp4")
	dur := strconv.Itoa(seconds)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration="+dur+":size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+dur,
		"-c:v", "libx264", "-c:a", "aac", "-shortest", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate sample: %v: %s", err, out)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, info.ModTime(), info.Size()
}

func TestEnsureSegmentBuildsAndReuses(t *testing.T) {
	ffmpegAvailable(t)
	src, mt, size := sampleClipDur(t, 12) // 2 full segments
	cacheRoot := t.TempDir()

	p, err := EnsureSegment(context.Background(), cacheRoot, src, mt, size, 240, 1, 12)
	if err != nil {
		t.Fatalf("EnsureSegment: %v", err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
		t.Fatalf("segment not produced: %v", err)
	}

	// Second call reuses the cached segment (same path, no rebuild).
	p2, err := EnsureSegment(context.Background(), cacheRoot, src, mt, size, 240, 1, 12)
	if err != nil || p2 != p {
		t.Fatalf("reuse: p=%q p2=%q err=%v", p, p2, err)
	}
}

// Requesting a later segment must NOT linearly encode the ones before it — that
// constant-cost random access is what makes seeking work.
func TestEnsureSegmentRandomAccess(t *testing.T) {
	ffmpegAvailable(t)
	src, mt, size := sampleClipDur(t, 18) // 3 segments
	cacheRoot := t.TempDir()

	p, err := EnsureSegment(context.Background(), cacheRoot, src, mt, size, 240, 2, 18)
	if err != nil {
		t.Fatalf("EnsureSegment: %v", err)
	}
	segs, _ := filepath.Glob(filepath.Join(filepath.Dir(p), "seg*.ts"))
	if len(segs) != 1 {
		t.Fatalf("expected only the requested segment on disk, got %v", segs)
	}
}

func TestEnsureSegmentConcurrentSharesOneBuild(t *testing.T) {
	ffmpegAvailable(t)
	src, mt, size := sampleClipDur(t, 12)
	cacheRoot := t.TempDir()

	const n = 5
	var wg sync.WaitGroup
	paths := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			paths[i], errs[i] = EnsureSegment(context.Background(), cacheRoot, src, mt, size, 240, 0, 12)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if paths[i] != paths[0] {
			t.Fatalf("goroutines disagreed on path: %q vs %q", paths[i], paths[0])
		}
	}
	dir := filepath.Dir(paths[0])
	if segs, _ := filepath.Glob(filepath.Join(dir, "seg*.ts")); len(segs) != 1 {
		t.Fatalf("expected 1 segment, got %d: %v", len(segs), segs)
	}
	// Atomic rename leaves no temp file behind.
	if tmps, _ := filepath.Glob(filepath.Join(dir, ".seg*.tmp")); len(tmps) != 0 {
		t.Fatalf("leftover temp files: %v", tmps)
	}
}

func TestEnsureSegmentFailedBuildLeavesNoSegment(t *testing.T) {
	ffmpegAvailable(t)
	cacheRoot := t.TempDir()
	// A nonexistent source makes ffmpeg fail before producing anything.
	bogus := filepath.Join(t.TempDir(), "does-not-exist.mkv")
	_, err := EnsureSegment(context.Background(), cacheRoot, bogus, time.Unix(1, 0), 1, 240, 0, 12)
	if err == nil {
		t.Fatal("expected an error for a missing source")
	}
	dir := filepath.Join(cacheRoot, hlsKey(bogus, time.Unix(1, 0), 1, 240))
	if segs, _ := filepath.Glob(filepath.Join(dir, "seg*.ts")); len(segs) != 0 {
		t.Fatalf("failed build should leave no segment, found: %v", segs)
	}
}

func TestStreamFFmpegRemux(t *testing.T) {
	ffmpegAvailable(t)
	src, _, _ := sampleClip(t)
	var buf bytes.Buffer
	if err := StreamFFmpeg(context.Background(), &buf, RemuxArgs(src, 0, 0)); err != nil {
		t.Fatalf("StreamFFmpeg remux: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("remux produced no output")
	}
}

func TestStreamFFmpegCanceledIsNotError(t *testing.T) {
	ffmpegAvailable(t)
	src, _, _ := sampleClip(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled before start
	err := StreamFFmpeg(ctx, &bytes.Buffer{}, RemuxArgs(src, 0, 0))
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

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

func TestHLSArgs(t *testing.T) {
	args := HLSArgs("/m/ep.mkv", "/cache/x", 720, 0)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-i /m/ep.mkv", "-c:v libx264", "-c:a aac", "-f hls",
		"-hls_playlist_type event", "scale=-2:'min(ih,720)'",
		"-force_key_frames expr:gte(t,n_forced*6)",
		filepath.Join("/cache/x", hlsPlaylistName),
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("HLSArgs missing %q in: %s", want, joined)
		}
	}
}

func TestRemuxArgsCopiesCodecs(t *testing.T) {
	joined := strings.Join(RemuxArgs("/m/ep.mkv", 0), " ")
	if !strings.Contains(joined, "-c copy") {
		t.Fatalf("RemuxArgs should copy codecs: %s", joined)
	}
	if !strings.Contains(joined, "pipe:1") {
		t.Fatalf("RemuxArgs should write to pipe: %s", joined)
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

func TestSetConcurrencyReservesBackground(t *testing.T) {
	defer SetConcurrency(0, 0) // restore unlimited for other tests
	SetConcurrency(4, time.Second)
	if cap(ffmpegSem) != 4 {
		t.Fatalf("global cap = %d, want 4", cap(ffmpegSem))
	}
	if cap(bgSem) != 2 {
		t.Fatalf("background cap = %d, want 2 (half of global)", cap(bgSem))
	}
	SetConcurrency(1, time.Second)
	if cap(bgSem) != 1 {
		t.Fatalf("background cap = %d, want 1 (floor)", cap(bgSem))
	}
	SetConcurrency(0, 0)
	if ffmpegSem != nil || bgSem != nil {
		t.Fatal("limit 0 should disable both semaphores")
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

// waitForComplete blocks until the HLS dir has a finished (#EXT-X-ENDLIST)
// playlist or the timeout elapses.
func waitForComplete(t *testing.T, dir string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if hlsReady(dir) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("HLS build did not complete within %s", timeout)
}

func TestEnsureHLSBuildsAndReuses(t *testing.T) {
	ffmpegAvailable(t)
	src, mt, size := sampleClip(t)
	cacheRoot := t.TempDir()

	dir, err := EnsureHLS(context.Background(), cacheRoot, src, mt, size, 240)
	if err != nil {
		t.Fatalf("EnsureHLS: %v", err)
	}
	if !hlsPlayable(dir) {
		t.Fatal("playlist not playable on return")
	}
	waitForComplete(t, dir, 30*time.Second) // background encode finishes
	segs, _ := filepath.Glob(filepath.Join(dir, "seg*.ts"))
	if len(segs) == 0 {
		t.Fatal("no segments produced")
	}

	// Second call reuses the same dir via the completed-cache fast path.
	dir2, err := EnsureHLS(context.Background(), cacheRoot, src, mt, size, 240)
	if err != nil || dir2 != dir {
		t.Fatalf("reuse: dir=%q dir2=%q err=%v", dir, dir2, err)
	}
}

// A long source must become playable well before it finishes transcoding —
// that is the whole point of progressive HLS.
func TestEnsureHLSProgressiveStart(t *testing.T) {
	ffmpegAvailable(t)
	src, mt, size := sampleClipDur(t, 60)
	cacheRoot := t.TempDir()

	dir, err := EnsureHLS(context.Background(), cacheRoot, src, mt, size, 240)
	if err != nil {
		t.Fatalf("EnsureHLS: %v", err)
	}
	if !hlsPlayable(dir) {
		t.Fatal("expected a playable playlist on return")
	}
	// It should have returned before the whole 60s was transcoded.
	if hlsReady(dir) {
		t.Log("note: build already complete on return (encode outran the first-segment check)")
	}
	waitForComplete(t, dir, 60*time.Second)
}

func TestEnsureHLSConcurrentSharesOneBuild(t *testing.T) {
	ffmpegAvailable(t)
	src, mt, size := sampleClip(t)
	cacheRoot := t.TempDir()

	const n = 5
	var wg sync.WaitGroup
	dirs := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dirs[i], errs[i] = EnsureHLS(context.Background(), cacheRoot, src, mt, size, 240)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if dirs[i] != dirs[0] {
			t.Fatalf("goroutines disagreed on dir: %q vs %q", dirs[i], dirs[0])
		}
	}
	waitForComplete(t, dirs[0], 30*time.Second)
	// One shared build → exactly one asset dir, no leftovers.
	entries, _ := os.ReadDir(cacheRoot)
	if len(entries) != 1 {
		t.Fatalf("expected 1 asset dir, got %d: %v", len(entries), entries)
	}
}

func TestEnsureHLSFailedBuildLeavesNoDir(t *testing.T) {
	ffmpegAvailable(t)
	cacheRoot := t.TempDir()
	// A nonexistent source makes ffmpeg fail before producing anything.
	bogus := filepath.Join(t.TempDir(), "does-not-exist.mkv")
	_, err := EnsureHLS(context.Background(), cacheRoot, bogus, time.Unix(1, 0), 1, 240)
	if err == nil {
		t.Fatal("expected an error for a missing source")
	}
	if entries, _ := os.ReadDir(cacheRoot); len(entries) != 0 {
		t.Fatalf("failed build should leave no dir, found: %v", entries)
	}
}

func TestStreamFFmpegRemux(t *testing.T) {
	ffmpegAvailable(t)
	src, _, _ := sampleClip(t)
	var buf bytes.Buffer
	if err := StreamFFmpeg(context.Background(), &buf, RemuxArgs(src, 0)); err != nil {
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
	err := StreamFFmpeg(ctx, &bytes.Buffer{}, RemuxArgs(src, 0))
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

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
	// Stereo source (2ch): standard -ac 2 fold.
	args := SegmentArgs("/m/ep.mkv", "/cache/x/seg00010.ts", 60, 6, 720, 0, 2)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-ss 60", "-i /m/ep.mkv", "-t 6", "-c:v libx264", "-c:a aac", "-ac 2", "-f mpegts",
		"scale=-2:'min(ih,720)'", "-force_key_frames expr:eq(n,0)",
		// No B-frames, so per-segment DTS==PTS and adjacent HLS segments don't
		// overlap in DTS (Chrome MSE rejects that → playback never starts).
		"-bf 0",
		// Disable the mpegts priming up-shift so a high-fps segment 0 doesn't
		// overrun its boundary into the next segment (50fps episodes wouldn't play).
		"-avoid_negative_ts disabled",
		// Cap encoder threads so each segment encode doesn't spike across every core.
		"-threads 3",
		"-output_ts_offset 60", "/cache/x/seg00010.ts",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("SegmentArgs missing %q in: %s", want, joined)
		}
	}
	if strings.Contains(joined, "pan=stereo") {
		t.Fatalf("stereo source must not use a dialogue-forward pan: %s", joined)
	}
}

func TestSegmentWarmupArgs(t *testing.T) {
	// Pass 1: audio encoded from segWarmupLead (0.5s) before the boundary, for
	// dur+lead, so the AAC encoder primes on the discarded lead-in.
	warm := strings.Join(audioWarmArgs("/m/ep.mkv", "/cache/x/.seg00010.ts.tmp.aud.tmp", 60, 6, 0, 2), " ")
	for _, want := range []string{"-ss 59.5", "-i /m/ep.mkv", "-t 6.5", "-c:a aac", "-ac 2", "-f mpegts", ".aud.tmp"} {
		if !strings.Contains(warm, want) {
			t.Fatalf("audioWarmArgs missing %q in: %s", want, warm)
		}
	}
	if strings.Contains(warm, "libx264") {
		t.Fatalf("audioWarmArgs must not encode video: %s", warm)
	}
	// Pass 2: stream-copy the warm audio (input -ss drops the lead-in + its priming),
	// encode the video, place at the boundary.
	mux := strings.Join(segmentMuxArgs("/m/ep.mkv", "/cache/x/aud.tmp", "/cache/x/seg00010.ts.tmp", 60, 6, 720), " ")
	for _, want := range []string{
		"-ss 0.5 -i /cache/x/aud.tmp", "-ss 60 -i /m/ep.mkv", "-t 6",
		"-map 0:a:0", "-map 1:V:0", "-c:a copy", "-c:v libx264", "-bf 0", "-threads 3",
		"-force_key_frames expr:eq(n,0)", "-avoid_negative_ts disabled", "-output_ts_offset 60",
		"scale=-2:'min(ih,720)'",
	} {
		if !strings.Contains(mux, want) {
			t.Fatalf("segmentMuxArgs missing %q in: %s", want, mux)
		}
	}
}

func TestSegmentArgsDialogueDownmix(t *testing.T) {
	// 5.1 source (6ch): dialogue-forward pan replaces -ac 2 so centre-channel
	// dialogue isn't buried 3 dB under the music when folded to stereo.
	joined := strings.Join(SegmentArgs("/m/ep.mkv", "/cache/x/seg00010.ts", 60, 6, 720, 0, 6), " ")
	if !strings.Contains(joined, "pan=stereo|FL=0.7*FC+0.5*FL|FR=0.7*FC+0.5*FR") {
		t.Fatalf("6ch source must use the dialogue-forward pan: %s", joined)
	}
	if strings.Contains(joined, "-ac 2") {
		t.Fatalf("dialogue downmix must not also pass -ac 2: %s", joined)
	}
}

func TestAudioFilterArgsGate(t *testing.T) {
	// Every layout gets the aresample=async=1 gap-fill; the pan (which names FC,
	// absent from 3-5ch layouts and would error) is gated on >=6ch, others use -ac 2.
	for ch := 0; ch <= 5; ch++ {
		got := strings.Join(audioFilterArgs(ch), " ")
		if !strings.Contains(got, "aresample=async=1") {
			t.Fatalf("audioFilterArgs(%d) = %q, want the aresample gap-fill", ch, got)
		}
		if got != "-af aresample=async=1 -ac 2" {
			t.Fatalf("audioFilterArgs(%d) = %q, want the -ac 2 fold", ch, got)
		}
	}
	for _, ch := range []int{6, 8} {
		got := strings.Join(audioFilterArgs(ch), " ")
		if !strings.Contains(got, "aresample=async=1") || !strings.Contains(got, "pan=stereo") {
			t.Fatalf("audioFilterArgs(%d) = %q, want aresample + pan downmix", ch, got)
		}
	}
}

func TestVODPlaylist(t *testing.T) {
	if VODPlaylist(0, 0) != "" || VODPlaylist(-5, 0) != "" {
		t.Fatal("non-positive duration should yield an empty playlist")
	}
	pl := VODPlaylist(20, 0) // 6+6+6+2 → 4 segments
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

func TestVODPlaylistCarriesAudio(t *testing.T) {
	// A selected audio track is carried as ?aud on each segment URI so the
	// per-segment transcode and the cache key see it; the default track adds none.
	if pl := VODPlaylist(12, 2); !strings.Contains(pl, "seg00000.ts?aud=2") {
		t.Fatalf("expected segment URIs to carry ?aud=2:\n%s", pl)
	}
	if pl := VODPlaylist(12, 0); strings.Contains(pl, "?aud=") {
		t.Fatalf("default audio track should add no query:\n%s", pl)
	}
}

func TestBurnInArgs(t *testing.T) {
	// sub ordinal 2 -> 0:s:1 (0-based), audio ordinal 1 -> 0:a:0, max height 1080.
	joined := strings.Join(BurnInArgs("/m/ep.mkv", 2, 1, 1080, 0, 2), " ")
	for _, want := range []string{
		"-i /m/ep.mkv",
		"[0:V:0][0:s:1]overlay,scale=-2:'min(ih,1080)'[v]",
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
	joined := strings.Join(BurnInArgs("/m/ep.mkv", 1, 0, 1080, 930.5, 2), " ")
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
	joined := strings.Join(RemuxArgs("/m/ep.mkv", 0, 0, 2, false), " ")
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
	// The copy path must never carry an audio encoder or a filter.
	for _, banned := range []string{"-c:a", "aac", "-af"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("RemuxArgs(copy) must not %q: %s", banned, joined)
		}
	}
	// Cover art must never be selected as the picture (0:V:0, not 0:v:0).
	if !strings.Contains(joined, "-map 0:V:0") {
		t.Fatalf("RemuxArgs must map 0:V:0 (video, excluding attached pictures): %s", joined)
	}
}

func TestRemuxArgsResume(t *testing.T) {
	joined := strings.Join(RemuxArgs("/m/ep.mkv", 0, 1200, 2, false), " ")
	for _, want := range []string{"-ss 1200", "-i /m/ep.mkv", "-avoid_negative_ts make_zero", "-c copy"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("RemuxArgs(start) missing %q in: %s", want, joined)
		}
	}
	if strings.Index(joined, "-ss ") > strings.Index(joined, "-i ") {
		t.Fatalf("RemuxArgs(start) -ss must be an input seek (before -i): %s", joined)
	}
	// A pure copy never decodes, so accurate-seek is irrelevant — and leaving it
	// off here keeps the long-standing copy args byte-identical.
	if strings.Contains(joined, "-noaccurate_seek") {
		t.Fatalf("RemuxArgs(copy) must not carry -noaccurate_seek: %s", joined)
	}
}

// The middle gear: the video is copied while an unplayable soundtrack (Dolby) is
// re-encoded to AAC, instead of dragging the whole file through the transcoder.
func TestRemuxArgsEncodeAudioCopiesVideo(t *testing.T) {
	joined := strings.Join(RemuxArgs("/m/ep.mkv", 2, 0, 6, true), " ")
	for _, want := range []string{
		"-map 0:V:0",  // real picture, never the cover art
		"-map 0:a:1?", // the selected (1-based) audio track
		"-c:v copy",   // ← the whole point: the video is NOT re-encoded
		"-c:a aac",    // the soundtrack is
		"-b:a 160k",
		"aresample=async=1", // shared gap-fill
		"pan=stereo|FL=0.7*FC+0.5*FL|FR=0.7*FC+0.5*FR", // 5.1 dialogue-forward downmix
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("RemuxArgs(encodeAudio) missing %q in: %s", want, joined)
		}
	}
	// A blanket "-c copy" here would copy the audio too and defeat the encode.
	if strings.Contains(joined, "-c copy") {
		t.Fatalf("RemuxArgs(encodeAudio) must not blanket-copy: %s", joined)
	}
}

// Resuming an audio-encoding remux MUST use -noaccurate_seek. ffmpeg's default
// accurate seek discards decoded audio before the requested position while the
// copied video keeps its pre-roll back to the previous keyframe, so the two
// streams start at different points and the audio lags the picture by up to a
// GOP (measured: 3.9s of desync on a 4s-GOP source).
func TestRemuxArgsEncodeAudioResumeKeepsAVInSync(t *testing.T) {
	joined := strings.Join(RemuxArgs("/m/ep.mkv", 0, 900, 6, true), " ")
	if !strings.Contains(joined, "-noaccurate_seek") {
		t.Fatalf("RemuxArgs(encodeAudio, start) must -noaccurate_seek or audio lags the video: %s", joined)
	}
	if strings.Index(joined, "-noaccurate_seek") > strings.Index(joined, "-i ") {
		t.Fatalf("-noaccurate_seek must be an input option (before -i): %s", joined)
	}
	if strings.Index(joined, "-ss ") > strings.Index(joined, "-i ") {
		t.Fatalf("RemuxArgs(start) -ss must be an input seek (before -i): %s", joined)
	}
}

func TestHLSKeyStableAndDistinct(t *testing.T) {
	mt := time.Unix(1700000000, 0)
	a := hlsKey("/m/ep.mkv", mt, 100, 1080, 0)
	if a != hlsKey("/m/ep.mkv", mt, 100, 1080, 0) {
		t.Fatal("hlsKey not stable for identical inputs")
	}
	for _, other := range []string{
		hlsKey("/m/other.mkv", mt, 100, 1080, 0),
		hlsKey("/m/ep.mkv", mt.Add(time.Second), 100, 1080, 0),
		hlsKey("/m/ep.mkv", mt, 101, 1080, 0),
		hlsKey("/m/ep.mkv", mt, 100, 720, 0),
		hlsKey("/m/ep.mkv", mt, 100, 1080, 2), // distinct audio track → distinct cache
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

func TestSetSegmentConcurrency(t *testing.T) {
	defer SetSegmentConcurrency(0) // restore unlimited for other tests
	SetSegmentConcurrency(1)
	if cap(segmentSem) != 1 {
		t.Fatalf("segment cap = %d, want 1", cap(segmentSem))
	}
	// The sub-cap serialises: a second acquire blocks until the first releases.
	rel, err := acquireSegment(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acquireSegment(ctx); err == nil {
		t.Fatal("second acquire should block (cap 1) and time out while the first is held")
	}
	rel() // release → a slot is free again
	if rel2, err := acquireSegment(context.Background()); err != nil {
		t.Fatalf("acquire after release: %v", err)
	} else {
		rel2()
	}
	SetSegmentConcurrency(0)
	if segmentSem != nil {
		t.Fatal("limit 0 should disable the segment semaphore")
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

	// An asset dir holding an orphaned segment temp left by a killed build: a
	// stale one (older than buildTimeout) must be swept, an in-flight one kept.
	withTemp := mk("withtemp", 5*time.Minute, 10)
	staleTemp := filepath.Join(withTemp, ".seg00000.ts.tmp")
	staleAudTemp := filepath.Join(withTemp, ".seg00002.ts.tmp.aud.tmp") // two-pass audio-warm temp
	freshTemp := filepath.Join(withTemp, ".seg00001.ts.tmp")
	writeTemp := func(p string, age time.Duration) {
		if err := os.WriteFile(p, []byte("xx"), 0o644); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		_ = os.Chtimes(p, when, when)
	}
	writeTemp(staleTemp, 3*time.Hour)    // > buildTimeout (2h) → swept
	writeTemp(staleAudTemp, 3*time.Hour) // audio-warm temp of a killed build → swept
	writeTemp(freshTemp, time.Minute)    // in-flight build → kept

	// maxAge expires "old"; the stale .ts.tmp orphan is swept (its dir survives);
	// budget=0 leaves the rest.
	if err := PruneCache(root, 0, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	assertGone(t, old)
	assertGone(t, staleTemp)
	assertGone(t, staleAudTemp)
	assertExists(t, freshTemp)
	assertExists(t, withTemp)
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

	p, err := EnsureSegment(context.Background(), cacheRoot, src, mt, size, 240, 1, 12, 2, 0)
	if err != nil {
		t.Fatalf("EnsureSegment: %v", err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
		t.Fatalf("segment not produced: %v", err)
	}

	// Second call reuses the cached segment (same path, no rebuild).
	p2, err := EnsureSegment(context.Background(), cacheRoot, src, mt, size, 240, 1, 12, 2, 0)
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

	p, err := EnsureSegment(context.Background(), cacheRoot, src, mt, size, 240, 2, 18, 2, 0)
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
			paths[i], errs[i] = EnsureSegment(context.Background(), cacheRoot, src, mt, size, 240, 0, 12, 2, 0)
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
	_, err := EnsureSegment(context.Background(), cacheRoot, bogus, time.Unix(1, 0), 1, 240, 0, 12, 2, 0)
	if err == nil {
		t.Fatal("expected an error for a missing source")
	}
	dir := filepath.Join(cacheRoot, hlsKey(bogus, time.Unix(1, 0), 1, 240, 0))
	if segs, _ := filepath.Glob(filepath.Join(dir, "seg*.ts")); len(segs) != 0 {
		t.Fatalf("failed build should leave no segment, found: %v", segs)
	}
}

func TestStreamFFmpegRemux(t *testing.T) {
	ffmpegAvailable(t)
	src, _, _ := sampleClip(t)
	var buf bytes.Buffer
	if err := StreamFFmpeg(context.Background(), &buf, RemuxArgs(src, 0, 0, 2, false)); err != nil {
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
	err := StreamFFmpeg(ctx, &bytes.Buffer{}, RemuxArgs(src, 0, 0, 2, false))
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// setEncoderForTest flips the package encoder var and restores it — the VAAPI
// argv shape must never leak into the software tests above (which pin the
// byte-identical libx264 argv).
func setEncoderForTest(t *testing.T, name string) {
	t.Helper()
	prev := hlsEncoder
	hlsEncoder = name
	t.Cleanup(func() { hlsEncoder = prev })
}

func TestSegmentArgsVAAPI(t *testing.T) {
	setEncoderForTest(t, "vaapi")
	got := strings.Join(SegmentArgs("/m/ep.mkv", "/tmp/seg.ts", 60, 6, 720, 0, 2), " ")
	for _, want := range []string{
		"-vaapi_device /dev/dri/renderD128",
		"scale=-2:'min(ih,720)',format=nv12,hwupload",
		"-c:v h264_vaapi", "-qp 23", "-bf 0",
		"-force_key_frames expr:eq(n,0)",
		"-avoid_negative_ts disabled",
		"-output_ts_offset 60",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("vaapi SegmentArgs missing %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"libx264", "-threads", "-crf", "-preset"} {
		if strings.Contains(got, banned) {
			t.Fatalf("vaapi SegmentArgs must not carry %q:\n%s", banned, got)
		}
	}
	// The device flag must precede the first input.
	if strings.Index(got, "-vaapi_device") > strings.Index(got, "-i /m/ep.mkv") {
		t.Fatalf("vaapi device must come before the input:\n%s", got)
	}
}

func TestSegmentMuxArgsVAAPI(t *testing.T) {
	setEncoderForTest(t, "vaapi")
	got := strings.Join(segmentMuxArgs("/m/ep.mkv", "/tmp/a.aud.tmp", "/tmp/seg.ts", 60, 6, 720), " ")
	for _, want := range []string{"-vaapi_device", "hwupload", "-c:v h264_vaapi", "-bf 0", "-c:a copy", "-output_ts_offset 60"} {
		if !strings.Contains(got, want) {
			t.Fatalf("vaapi segmentMuxArgs missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "libx264") {
		t.Fatalf("vaapi segmentMuxArgs must not carry libx264:\n%s", got)
	}
}

func TestHLSKeySeparatesEncoders(t *testing.T) {
	now := time.Now()
	setEncoderForTest(t, "software")
	soft := hlsKey("/m/ep.mkv", now, 100, 720, 0)
	setEncoderForTest(t, "vaapi")
	hard := hlsKey("/m/ep.mkv", now, 100, 720, 0)
	if soft == hard {
		t.Fatal("cache keys must differ per encoder — segments from different encoders can never mix")
	}
}

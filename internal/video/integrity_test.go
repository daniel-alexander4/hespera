package video

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swapRepairSeams overrides the ffmpeg-invoking seams so RepairFile is testable
// without ffmpeg, restoring them when the test ends.
func swapRepairSeams(t *testing.T, check func(context.Context, string, bool) (int, error), remux func(context.Context, string, string) error, probe func(context.Context, string) (*ProbeResult, error)) {
	t.Helper()
	oc, or, op := checkIntegrityFn, remuxCopyFn, probeFn
	checkIntegrityFn, remuxCopyFn, probeFn = check, remux, probe
	t.Cleanup(func() { checkIntegrityFn, remuxCopyFn, probeFn = oc, or, op })
}

func twoStream(dur string) *ProbeResult {
	return &ProbeResult{Streams: make([]ProbeStream, 2), Format: ProbeFormat{Duration: dur}}
}

func TestRepairFileCleanIsNoOp(t *testing.T) {
	swapRepairSeams(t,
		func(context.Context, string, bool) (int, error) { return 0, nil },
		func(context.Context, string, string) error { t.Fatal("remux must not run on a clean file"); return nil },
		func(context.Context, string) (*ProbeResult, error) { return twoStream("100"), nil },
	)
	out, err := RepairFile(context.Background(), "/media/x.mkv", true)
	if err != nil || out.Status != "ok" || out.Replaced {
		t.Fatalf("clean file: got %+v err=%v, want ok/not-replaced", out, err)
	}
}

func TestRepairFileContainerRepairSucceeds(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "foo.mkv")
	if err := os.WriteFile(orig, []byte("ORIGINAL-corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	swapRepairSeams(t,
		func(_ context.Context, path string, _ bool) (int, error) {
			if strings.Contains(path, repairTempPrefix) {
				return 0, nil // the remuxed temp is clean
			}
			return 3, nil // the original has container errors
		},
		func(_ context.Context, _, dst string) error { return os.WriteFile(dst, []byte("REMUXED-clean"), 0o644) },
		func(context.Context, string) (*ProbeResult, error) { return twoStream("100.0"), nil },
	)
	out, err := RepairFile(context.Background(), orig, true)
	if err != nil || out.Status != "repaired" || !out.Replaced {
		t.Fatalf("repair: got %+v err=%v, want repaired/replaced", out, err)
	}
	if b, _ := os.ReadFile(orig); string(b) != "REMUXED-clean" {
		t.Fatalf("original not replaced with remuxed content: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, repairTempPrefix+"foo.mkv")); !os.IsNotExist(err) {
		t.Fatal("temp file should be gone after a successful rename")
	}
}

func TestRepairFileVerifyFailKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "foo.mkv")
	if err := os.WriteFile(orig, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	swapRepairSeams(t,
		func(context.Context, string, bool) (int, error) { return 2, nil },
		func(_ context.Context, _, dst string) error { return os.WriteFile(dst, []byte("REMUXED"), 0o644) },
		func(_ context.Context, path string) (*ProbeResult, error) {
			if strings.Contains(path, repairTempPrefix) {
				return &ProbeResult{Streams: make([]ProbeStream, 1), Format: ProbeFormat{Duration: "100.0"}}, nil // dropped a stream
			}
			return twoStream("100.0"), nil
		},
	)
	out, err := RepairFile(context.Background(), orig, true)
	if err != nil || out.Status != "flagged" || out.Replaced {
		t.Fatalf("verify-fail: got %+v err=%v, want flagged/not-replaced", out, err)
	}
	if b, _ := os.ReadFile(orig); string(b) != "ORIGINAL" {
		t.Fatalf("original must be untouched when verify fails: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, repairTempPrefix+"foo.mkv")); !os.IsNotExist(err) {
		t.Fatal("temp file should be cleaned up on the failure path")
	}
}

func TestRepairFileRemuxStillDirtyKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "foo.mkv")
	os.WriteFile(orig, []byte("ORIGINAL"), 0o644)
	swapRepairSeams(t,
		func(_ context.Context, path string, _ bool) (int, error) {
			if strings.Contains(path, repairTempPrefix) {
				return 1, nil // remux did not clean it
			}
			return 2, nil
		},
		func(_ context.Context, _, dst string) error { return os.WriteFile(dst, []byte("REMUXED"), 0o644) },
		func(context.Context, string) (*ProbeResult, error) { return twoStream("100.0"), nil },
	)
	out, _ := RepairFile(context.Background(), orig, true)
	if out.Status != "flagged" || out.Replaced {
		t.Fatalf("remux-still-dirty: got %+v, want flagged/not-replaced", out)
	}
	if b, _ := os.ReadFile(orig); string(b) != "ORIGINAL" {
		t.Fatalf("original must be untouched: %q", b)
	}
}

func TestRepairFileDisabledFlagsOnly(t *testing.T) {
	swapRepairSeams(t,
		func(context.Context, string, bool) (int, error) { return 4, nil },
		func(context.Context, string, string) error {
			t.Fatal("remux must not run when repair is disabled")
			return nil
		},
		func(context.Context, string) (*ProbeResult, error) { return twoStream("100"), nil },
	)
	out, _ := RepairFile(context.Background(), "/media/x.mkv", false)
	if out.Status != "flagged" || out.Replaced {
		t.Fatalf("disabled: got %+v, want flagged/not-replaced", out)
	}
}

func TestRemuxVerified(t *testing.T) {
	cases := []struct {
		name       string
		orig, next *ProbeResult
		want       bool
	}{
		{"identical", twoStream("100.0"), twoStream("100.0"), true},
		{"within tolerance", twoStream("100.0"), twoStream("98.5"), true},
		{"too much dropped", twoStream("100.0"), twoStream("90.0"), false},
		{"stream count differs", twoStream("100.0"), &ProbeResult{Streams: make([]ProbeStream, 3), Format: ProbeFormat{Duration: "100.0"}}, false},
		{"unparseable duration", twoStream("100.0"), twoStream("N/A"), false},
	}
	for _, c := range cases {
		if got := remuxVerified(c.orig, c.next); got != c.want {
			t.Errorf("%s: remuxVerified=%v want %v", c.name, got, c.want)
		}
	}
}

func TestParseAudioGaps(t *testing.T) {
	// Contiguous ~32ms ac3 frames, then a 2s hole, then contiguous again.
	var b strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, "%.3f,0.032\n", float64(i)*0.032)
	}
	b.WriteString("2.128,0.032\n") // jumps from 0.160 → 2.128: a ~1.968s gap
	b.WriteString("2.160,0.032\n") // contiguous after
	total, largest := parseAudioGaps(b.String())
	if largest < 1.9 || largest > 2.0 {
		t.Fatalf("largest gap = %.3f, want ~1.968", largest)
	}
	if total < 1.9 || total > 2.0 {
		t.Fatalf("total gap = %.3f, want ~1.968 (jitter under epsilon ignored)", total)
	}
	// A clean stream has no gaps.
	if tot, lg := parseAudioGaps("0.000,0.032\n0.032,0.032\n0.064,0.032\n"); tot != 0 || lg != 0 {
		t.Fatalf("clean stream: total=%.3f largest=%.3f, want 0/0", tot, lg)
	}
}

func TestCountErrorLines(t *testing.T) {
	if n := countErrorLines(""); n != 0 {
		t.Errorf("empty: got %d want 0", n)
	}
	if n := countErrorLines("err one\n\nerr two\n"); n != 2 {
		t.Errorf("two errors: got %d want 2", n)
	}
}

package video

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsHDRTransfer(t *testing.T) {
	for _, v := range []string{"smpte2084", "arib-std-b67", "SMPTE2084", " arib-std-b67 "} {
		if !isHDRTransfer(v) {
			t.Errorf("isHDRTransfer(%q) = false, want true (HDR)", v)
		}
	}
	// bt2020-10 shares BT.709 gamma (wide-gamut SDR, not PQ/HLG) → must NOT tonemap.
	for _, v := range []string{"", "bt709", "bt2020-10", "smpte170m", "unknown"} {
		if isHDRTransfer(v) {
			t.Errorf("isHDRTransfer(%q) = true, want false (SDR)", v)
		}
	}
}

func TestTonemapFiltersChain(t *testing.T) {
	f := strings.Join(tonemapFilters(), ",")
	for _, want := range []string{"zscale=transfer=linear", "tonemap=tonemap=hable", "zscale=transfer=bt709", "format=yuv420p"} {
		if !strings.Contains(f, want) {
			t.Errorf("tonemap chain missing %q: got %s", want, f)
		}
	}
}

// TestTonemapLive fabricates a PQ-tagged (HDR) clip and a plain SDR clip and
// exercises the real detection + tonemap grab. Self-skips where the tools it
// needs are absent (no ffmpeg, no libx264, no libzimg) so `go test ./...` still
// passes on a bare box; runs automatically where they're present.
func TestTonemapLive(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	ctx := context.Background()

	// setparams stamps frame-level color metadata reliably (an -color_trc output
	// option gets dropped by libx264's metadata propagation on some builds).
	gen := func(name, params string) string {
		out := filepath.Join(dir, name)
		args := []string{"-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=10:duration=1"}
		if params != "" {
			args = append(args, "-vf", "setparams="+params)
		}
		args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p", out)
		if err := exec.CommandContext(ctx, "ffmpeg", args...).Run(); err != nil {
			t.Skipf("could not encode %s (no libx264?): %v", name, err)
		}
		return out
	}
	pq := gen("pq.mp4", "color_trc=smpte2084:color_primaries=bt2020:colorspace=bt2020nc")
	sdr := gen("sdr.mp4", "color_trc=bt709:color_primaries=bt709:colorspace=bt709")

	// Detection: the PQ clip's tagged transfer must read as HDR (probe path).
	prPQ, err := Probe(ctx, pq)
	if err != nil {
		t.Fatalf("probe pq: %v", err)
	}
	var pqTransfer string
	for _, s := range prPQ.Streams {
		if s.CodecType == "video" {
			pqTransfer = s.ColorTransfer
		}
	}
	if !isHDRTransfer(pqTransfer) {
		t.Fatalf("PQ clip probed transfer = %q, want an HDR transfer (ColorTransfer wiring)", pqTransfer)
	}

	// The SDR clip must never be flagged for tonemapping.
	if hdrTonemapWanted(ctx, sdr) {
		t.Fatalf("SDR clip wrongly flagged for tonemapping")
	}

	if !zscaleAvailable() {
		t.Skip("host ffmpeg has no zscale (libzimg) — tonemap path disabled, plain grab is the fallback")
	}
	if !hdrTonemapWanted(ctx, pq) {
		t.Fatalf("PQ clip should want tonemapping when zscale is available")
	}

	// Tonemap must actually change the encoded thumbnail vs the plain (grey) grab.
	grab := func(hdr bool) []byte {
		dst := filepath.Join(dir, "thumb.webp")
		if err := photoThumbOnce(ctx, pq, dst, 240, 0, true, hdr, 0); err != nil {
			t.Fatalf("photoThumbOnce(hdr=%v): %v", hdr, err)
		}
		b, err := os.ReadFile(dst)
		if err != nil || len(b) == 0 {
			t.Fatalf("read thumb (hdr=%v): %v (len %d)", hdr, err, len(b))
		}
		return b
	}
	tonemapped, plain := grab(true), grab(false)
	if bytes.Equal(tonemapped, plain) {
		t.Fatal("tonemapped grab is byte-identical to the plain grab — the HDR chain had no effect")
	}

	// FrameGrab end-to-end on the HDR clip succeeds (filter chain is valid).
	if err := FrameGrab(ctx, pq, filepath.Join(dir, "fg.webp"), 240, 0); err != nil {
		t.Fatalf("FrameGrab on HDR clip: %v", err)
	}
}

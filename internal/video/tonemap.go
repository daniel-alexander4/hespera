package video

import (
	"context"
	"os/exec"
	"strings"
	"sync"
)

// HDR→SDR tonemapping for thumbnails. An HDR frame (PQ/HLG transfer) grabbed
// straight to webp looks grey/washed, so a frame grab conditionally runs a
// zscale+tonemap chain when the source is HDR and the host ffmpeg can do it.

// isHDRTransfer reports whether an ffprobe color_transfer value marks HDR
// content that needs tonemapping down to SDR: PQ (smpte2084) or HLG
// (arib-std-b67).
func isHDRTransfer(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "smpte2084", "arib-std-b67":
		return true
	}
	return false
}

// tonemapFilters is the software HDR→SDR chain prepended before the scale when
// grabbing an HDR frame: linearize from the source's tagged transfer, map the
// BT.2020 primaries to BT.709, tonemap the luminance (hable curve — a safe,
// detail-preserving default for a thumbnail), then re-encode as BT.709 SDR.
// Requires ffmpeg built with libzimg (the zscale filter); guard with
// zscaleAvailable() before use.
func tonemapFilters() []string {
	return []string{
		"zscale=transfer=linear:npl=100",
		"format=gbrpf32le",
		"zscale=primaries=bt709",
		"tonemap=tonemap=hable:desat=0",
		"zscale=transfer=bt709:matrix=bt709:range=tv",
		"format=yuv420p",
	}
}

var (
	zscaleOnce sync.Once
	zscaleOK   bool
)

// zscaleAvailable reports whether the host ffmpeg has the zscale filter
// (libzimg) — required for the tonemap chain. Probed once and cached; a plain
// capability query (no media file), so it doesn't take the ffmpeg semaphore.
// Absent libzimg → thumbnails fall back to the plain grab (grey HDR, as before),
// never a hard failure.
func zscaleAvailable() bool {
	zscaleOnce.Do(func() {
		out, err := exec.Command("ffmpeg", "-hide_banner", "-filters").Output()
		if err != nil {
			return
		}
		// Filter rows are columnar: "<flags> zscale           V->V  ...".
		zscaleOK = strings.Contains(string(out), " zscale ")
	})
	return zscaleOK
}

// hdrTonemapWanted reports whether a frame grab of src should tonemap: the host
// ffmpeg supports zscale AND src's video stream carries an HDR transfer. The
// zscale check short-circuits first, so a host without libzimg never pays for
// the probe. MUST be called BEFORE the caller acquires the ffmpeg semaphore —
// Probe takes a slot too, and probing while already holding one would deadlock
// at concurrency 1. Best-effort: a probe failure (incl. a busy gate) just skips
// tonemapping rather than failing the grab.
func hdrTonemapWanted(ctx context.Context, src string) bool {
	if !zscaleAvailable() {
		return false
	}
	pr, err := Probe(ctx, src)
	if err != nil {
		return false
	}
	for _, s := range pr.Streams {
		if s.CodecType == "video" && isHDRTransfer(s.ColorTransfer) {
			return true
		}
	}
	return false
}

package video

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fingerprint extracts a Chromaprint audio fingerprint of [startSec, startSec+durSec)
// as a slice of 32-bit points, plus the points-per-second rate (a point index ÷ rate
// = its timestamp in seconds). It uses ffmpeg's built-in `chromaprint` muxer — no
// extra binary dependency — decoding mono and emitting the raw little-endian int32
// point array. Respects the shared ffmpeg concurrency gate. Powers cross-episode
// intro detection (internal/introskip).
func Fingerprint(ctx context.Context, path string, startSec, durSec float64) ([]uint32, float64, error) {
	if durSec <= 0 {
		return nil, 0, fmt.Errorf("fingerprint: durSec must be > 0")
	}
	release, err := acquire(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("ffmpeg acquire slot: %w", err)
	}
	defer release()

	args := []string{"-hide_banner", "-loglevel", "error"}
	if startSec > 0 {
		args = append(args, "-ss", strconv.FormatFloat(startSec, 'f', -1, 64))
	}
	args = append(args,
		"-i", path,
		"-t", strconv.FormatFloat(durSec, 'f', -1, 64),
		"-ac", "1",
		"-f", "chromaprint", "-fp_format", "raw", "-",
	)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, fmt.Errorf("ffmpeg chromaprint: %w: %s", err, tail(errBuf.String(), 300))
	}
	raw := out.Bytes()
	n := len(raw) / 4
	if n == 0 {
		return nil, 0, fmt.Errorf("chromaprint produced no fingerprint (ffmpeg built without chromaprint?)")
	}
	pts := make([]uint32, n)
	for i := 0; i < n; i++ {
		pts[i] = binary.LittleEndian.Uint32(raw[i*4:])
	}
	// The muxer's point rate is fixed per build; derive it from the decoded window
	// rather than hardcode chromaprint's frame rate.
	return pts, float64(n) / durSec, nil
}

var (
	chromaprintOnce sync.Once
	chromaprintOK   bool
)

// ChromaprintAvailable reports whether this ffmpeg build includes the chromaprint
// muxer (required for Fingerprint). Probed once and cached. When false, intro
// detection degrades gracefully rather than erroring.
func ChromaprintAvailable() bool {
	chromaprintOnce.Do(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(cctx, "ffmpeg", "-hide_banner", "-muxers").Output()
		chromaprintOK = err == nil && strings.Contains(string(out), "chromaprint")
	})
	return chromaprintOK
}

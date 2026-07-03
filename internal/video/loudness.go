package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// LoudnessScan measures a file's integrated loudness (LUFS, EBU R128) via
// ffmpeg's loudnorm analysis pass — a full decode, but music files are small
// (seconds each). Gated by the shared ffmpeg semaphore so a library backfill
// yields to live playback. The stderr parse is split out pure for tests.
func LoudnessScan(ctx context.Context, path string) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, integrityCheapTimeout)
	defer cancel()
	release, err := acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("loudness acquire slot: %w", err)
	}
	defer release()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-nostats",
		"-i", path,
		"-map", "0:a:0",
		"-af", "loudnorm=print_format=json",
		"-f", "null", "-",
	)
	out, err := cmd.CombinedOutput() // loudnorm prints its JSON to stderr
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if err != nil {
		return 0, fmt.Errorf("ffmpeg loudnorm: %w", err)
	}
	return parseLoudnorm(string(out))
}

// parseLoudnorm extracts input_i (integrated loudness) from loudnorm's JSON
// block — the last {...} in the output; the block has no nested braces. A
// "-inf" measurement (digital silence) maps to -70 LUFS (the R128 gating
// floor) rather than an error, and an exact 0.0 is nudged to -0.01 so the
// stored value can never collide with the "not yet analyzed" sentinel.
func parseLoudnorm(out string) (float64, error) {
	start := strings.LastIndex(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end <= start {
		return 0, errors.New("no loudnorm json in ffmpeg output")
	}
	var res struct {
		InputI string `json:"input_i"`
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &res); err != nil {
		return 0, fmt.Errorf("parse loudnorm json: %w", err)
	}
	if strings.Contains(strings.ToLower(res.InputI), "inf") {
		return -70, nil
	}
	lufs, err := strconv.ParseFloat(strings.TrimSpace(res.InputI), 64)
	if err != nil {
		return 0, fmt.Errorf("parse input_i %q: %w", res.InputI, err)
	}
	if lufs == 0 {
		lufs = -0.01
	}
	return lufs, nil
}

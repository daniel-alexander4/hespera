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

// LoudnessScan measures a file's integrated loudness (LUFS, EBU R128) and its
// true peak (dBTP) via ffmpeg's loudnorm analysis pass — a full decode, but
// music files are small (seconds each). Both come from the one pass: the
// leveling gain is computed from the loudness, then capped by the true peak so
// a boost can't push the track past full scale. Gated by the shared ffmpeg
// semaphore so a library backfill yields to live playback. The stderr parse is
// split out pure for tests.
func LoudnessScan(ctx context.Context, path string) (lufs, truePeak float64, err error) {
	ctx, cancel := context.WithTimeout(ctx, integrityCheapTimeout)
	defer cancel()
	release, err := acquire(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("loudness acquire slot: %w", err)
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
		return 0, 0, ctx.Err()
	}
	if err != nil {
		return 0, 0, fmt.Errorf("ffmpeg loudnorm: %w", err)
	}
	return parseLoudnorm(string(out))
}

// parseLoudnorm extracts input_i (integrated loudness, LUFS) and input_tp (true
// peak, dBTP) from loudnorm's JSON block — the last {...} in the output; the
// block has no nested braces. A missing or unparseable field is an error, not a
// silent zero: zero is the "not yet analyzed" sentinel, and a row stored with it
// would be re-analyzed on every sweep.
func parseLoudnorm(out string) (lufs, truePeak float64, err error) {
	start := strings.LastIndex(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end <= start {
		return 0, 0, errors.New("no loudnorm json in ffmpeg output")
	}
	var res struct {
		InputI  string `json:"input_i"`
		InputTP string `json:"input_tp"`
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &res); err != nil {
		return 0, 0, fmt.Errorf("parse loudnorm json: %w", err)
	}
	if lufs, err = parseLoudnormDB(res.InputI); err != nil {
		return 0, 0, fmt.Errorf("input_i: %w", err)
	}
	if truePeak, err = parseLoudnormDB(res.InputTP); err != nil {
		return 0, 0, fmt.Errorf("input_tp: %w", err)
	}
	return lufs, truePeak, nil
}

// parseLoudnormDB parses one of loudnorm's dB-valued measurements. A "-inf"
// reading (digital silence) maps to -70 — the R128 gating floor — rather than an
// error, and an exact 0.0 is nudged to -0.01 so a stored value can never collide
// with the "not yet analyzed" sentinel. The nudge is not academic for the true
// peak: real tracks measure a peak of exactly 0.00 dBTP (and above it — a
// brickwalled master can read +0.4), so 0 is a value the field genuinely takes.
func parseLoudnormDB(s string) (float64, error) {
	if strings.Contains(strings.ToLower(s), "inf") {
		return -70, nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", s, err)
	}
	if v == 0 {
		return -0.01, nil
	}
	return v, nil
}

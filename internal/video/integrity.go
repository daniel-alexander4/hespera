package video

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Media-integrity detection + lossless auto-repair.
//
// Two tiers, matching what ffmpeg can actually tell us:
//   - CONTAINER integrity (cheap): demux the file with `-c copy -f null` (no
//     decode) — surfaces container/EBML framing errors (a truncated index, bad
//     packet headers). These are losslessly repairable by a stream-copy remux,
//     which rewrites the container and drops the bad framing bytes.
//   - BITSTREAM integrity (deep): fully decode with `-f null` — surfaces damaged
//     coded frames (h264 "error while decoding MB"). That pixel data is *gone*;
//     a remux carries it through unchanged, so this tier only FLAGS, never fixes.
//
// Both go through the shared ffmpeg semaphore (acquire), so a library backfill
// yields to live playback. The cheap check reads the whole file (I/O-bound, a
// few seconds); the deep check decodes it (can be minutes) — hence the deep tier
// is an opt-in background job, not run on every add.
const (
	integrityCheapTimeout = 15 * time.Minute // ceiling for a demux-only container check / remux
	integrityDeepTimeout  = 90 * time.Minute // ceiling for a full-decode bitstream check
)

// repairTempPrefix names the same-directory temp a remux writes before the
// atomic rename. Dot-prefixed so it stays hidden, and it keeps the original
// filename (incl. extension) so ffmpeg infers the muxer. Swept by the caller.
const repairTempPrefix = ".hespera-repair-"

// CheckIntegrity counts the errors ffmpeg reports while processing path. With
// deep=false it demuxes only (container integrity, cheap); with deep=true it
// fully decodes (bitstream integrity, expensive). A return of (0, nil) means
// clean. A genuine read failure (unreadable file, no error output) returns an
// error; a corrupt-but-readable file returns its error count with nil error.
func CheckIntegrity(ctx context.Context, path string, deep bool) (int, error) {
	timeout := integrityCheapTimeout
	if deep {
		timeout = integrityDeepTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	release, err := acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("ffmpeg acquire slot: %w", err)
	}
	defer release()

	args := []string{"-nostdin", "-v", "error", "-i", path}
	if !deep {
		args = append(args, "-c", "copy") // demux only, no decode
	}
	args = append(args, "-f", "null", "-")

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	n := countErrorLines(errBuf.String())
	// ffmpeg may exit non-zero on a corrupt file while still reporting the errors
	// we want to count. Only treat it as a hard failure when nothing was reported
	// (a genuinely unreadable file), so the caller can skip rather than mis-flag.
	if runErr != nil && n == 0 {
		return 0, fmt.Errorf("ffmpeg integrity check %s: %w: %s", path, runErr, tail(errBuf.String(), 300))
	}
	return n, nil
}

// RemuxCopy losslessly rewrites src's container into dst (all streams, stream
// copy — no re-encode), dropping the bad framing bytes that make a container
// corrupt. dst must be on the same filesystem as the eventual target so the
// caller can atomically rename it into place. A non-zero ffmpeg exit is
// tolerated as long as an output file was produced — the caller's verify gate
// (stream count + duration + a clean re-check) is the real gatekeeper.
func RemuxCopy(ctx context.Context, src, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, integrityCheapTimeout)
	defer cancel()

	release, err := acquire(ctx)
	if err != nil {
		return fmt.Errorf("ffmpeg acquire slot: %w", err)
	}
	defer release()

	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-y",
		"-i", src, "-map", "0", "-c", "copy", dst)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if info, statErr := os.Stat(dst); statErr != nil || info.Size() == 0 {
		return fmt.Errorf("ffmpeg remux %s produced no output: %v: %s", src, runErr, tail(errBuf.String(), 300))
	}
	return nil
}

// Seams so RepairFile is unit-testable without ffmpeg (overridden in tests).
var (
	checkIntegrityFn = CheckIntegrity
	remuxCopyFn      = RemuxCopy
	probeFn          = Probe
)

// RepairOutcome is the result of examining (and possibly repairing) one file.
type RepairOutcome struct {
	Status   string       // "ok" (clean) | "repaired" (container remuxed) | "flagged" (unrepairable / not repaired)
	Detail   string       // human-readable note (empty for "ok")
	Replaced bool         // the on-disk file was atomically replaced
	Probe    *ProbeResult // fresh probe of the replaced file (only when Replaced)
}

// RepairFile examines path for CONTAINER corruption and, when allowRepair is
// true, losslessly repairs it in place: remux to a same-directory temp, verify
// the temp is genuinely better (same streams, ~same duration, container-clean),
// then atomically rename it over the original. The original is never touched
// unless a verified-good remux is ready to replace it. A clean file is a no-op.
//
// It does NOT catch bitstream corruption (that needs a full decode — the deep
// tier); a file that is only bitstream-damaged returns "ok" here.
func RepairFile(ctx context.Context, path string, allowRepair bool) (RepairOutcome, error) {
	n, err := checkIntegrityFn(ctx, path, false)
	if err != nil {
		return RepairOutcome{}, err
	}
	if n == 0 {
		return RepairOutcome{Status: "ok"}, nil
	}
	if !allowRepair {
		return RepairOutcome{Status: "flagged", Detail: fmt.Sprintf("container corruption (%d errors); auto-repair disabled", n)}, nil
	}

	tmp := filepath.Join(filepath.Dir(path), repairTempPrefix+filepath.Base(path))
	defer os.Remove(tmp) // no-op after a successful rename; cleans up every failure path

	if err := remuxCopyFn(ctx, path, tmp); err != nil {
		return RepairOutcome{Status: "flagged", Detail: "container corruption; remux failed: " + brief(err)}, nil
	}
	orig, oErr := probeFn(ctx, path)
	fixed, fErr := probeFn(ctx, tmp)
	if oErr != nil || fErr != nil {
		return RepairOutcome{Status: "flagged", Detail: "container corruption; could not verify remux"}, nil
	}
	if !remuxVerified(orig, fixed) {
		return RepairOutcome{Status: "flagged", Detail: "container corruption; remux changed streams/duration — not applied"}, nil
	}
	if cn, cErr := checkIntegrityFn(ctx, tmp, false); cErr != nil || cn != 0 {
		return RepairOutcome{Status: "flagged", Detail: fmt.Sprintf("container corruption; remux still has %d errors — not applied", cn)}, nil
	}
	if err := os.Rename(tmp, path); err != nil {
		return RepairOutcome{Status: "flagged", Detail: "container corruption; atomic replace failed: " + brief(err)}, nil
	}
	final, pErr := probeFn(ctx, path)
	if pErr != nil {
		final = fixed // identical content; fall back to the temp's probe
	}
	return RepairOutcome{Status: "repaired", Detail: fmt.Sprintf("container remuxed (%d errors dropped)", n), Replaced: true, Probe: final}, nil
}

// remuxVerified gates the atomic overwrite: the remuxed file must carry the same
// number of streams and a duration within a small tolerance of the original, so
// a truncated or stream-dropping remux can never replace a good original.
func remuxVerified(orig, fixed *ProbeResult) bool {
	if orig == nil || fixed == nil {
		return false
	}
	if len(orig.Streams) != len(fixed.Streams) {
		return false
	}
	od, oOK := parseSeconds(orig.Format.Duration)
	fd, fOK := parseSeconds(fixed.Format.Duration)
	if !oOK || !fOK {
		return false
	}
	diff := od - fd
	if diff < 0 {
		diff = -diff
	}
	return diff <= remuxDurationToleranceSec
}

const remuxDurationToleranceSec = 2.0

// countErrorLines counts non-empty lines ffmpeg wrote to stderr under -v error.
// Under that log level every line is an error, so the count is a corruption
// signal; the caller only distinguishes zero from non-zero.
func countErrorLines(stderr string) int {
	n := 0
	for _, line := range strings.Split(stderr, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

func parseSeconds(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0, false
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%g", &f); err != nil {
		return 0, false
	}
	return f, true
}

func brief(err error) string {
	if err == nil {
		return ""
	}
	return tail(err.Error(), 160)
}

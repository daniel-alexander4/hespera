package video

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// Trickplay: seek-bar preview thumbnails. One keyframe-only decode pass per
// file emits a frame every trickplayInterval seconds, scaled to
// trickplayWidth and packed into trickplayTile sprite sheets (sprite00000.jpg,
// …) plus a manifest.json the player uses for background-position math.
// Keyframe-only (-skip_frame nokey) is ~10× cheaper than exact-interval
// decoding and near-keyframe accuracy is the norm for previews.
const (
	trickplayInterval = 10  // seconds between preview frames
	trickplayWidth    = 240 // preview frame width (height keeps aspect)
	trickplayTile     = 5   // sprite grid: 5x5 frames per sheet
	trickplayTimeout  = 15 * time.Minute
)

// TrickplayManifest describes one file's generated sprite set.
type TrickplayManifest struct {
	IntervalSec int `json:"interval_sec"`
	Width       int `json:"width"`
	Height      int `json:"height"`
	Tile        int `json:"tile"`   // frames per sprite row/column
	Frames      int `json:"frames"` // total preview frames
}

// TrickplayKey is the cache-dir name for a source file: content-addressed on
// (path, mtime, size) like hlsKey, so a changed file regenerates under a new
// key and the orphan ages out via PruneCache.
func TrickplayKey(src string, modTime time.Time, size int64) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("tp|%s|%d|%d", src, modTime.UnixNano(), size)))
	return hex.EncodeToString(h[:8])
}

// GenerateTrickplay produces the sprite sheets + manifest for src under
// outDir (created; temp-suffixed files renamed in so a torn run never leaves
// a half-usable dir — the manifest is written last and is the "complete"
// marker). Gated by the shared ffmpeg semaphore.
func GenerateTrickplay(ctx context.Context, src, outDir string) error {
	ctx, cancel := context.WithTimeout(ctx, trickplayTimeout)
	defer cancel()
	release, err := acquire(ctx)
	if err != nil {
		return fmt.Errorf("trickplay acquire slot: %w", err)
	}
	defer release()

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	tileN := trickplayTile * trickplayTile
	pattern := filepath.Join(outDir, "sprite%05d.jpg")
	vf := fmt.Sprintf("fps=1/%d,scale=%d:-2,tile=%dx%d", trickplayInterval, trickplayWidth, trickplayTile, trickplayTile)
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-skip_frame", "nokey", // decode keyframes only — the 10x saver
		"-i", src,
		"-map", "0:v:0", "-an", "-sn",
		"-vf", vf,
		"-fps_mode", "vfr",
		"-q:v", "5",
		"-start_number", "0",
		pattern,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg trickplay: %w: %s", err, tail(string(out), 300))
	}

	// Derive geometry + frame count from what actually landed: probe the first
	// sprite for tile pixel size, count sheets for the frame total (the last
	// sheet may be partially filled — the player clamps by Frames).
	sprites, err := filepath.Glob(filepath.Join(outDir, "sprite*.jpg"))
	if err != nil || len(sprites) == 0 {
		return fmt.Errorf("trickplay produced no sprites for %s", src)
	}
	w, hgt, err := imageSize(ctx, sprites[0])
	if err != nil {
		return fmt.Errorf("trickplay sprite probe: %w", err)
	}
	frames, err := spriteFrames(ctx, src)
	if err != nil || frames <= 0 {
		// Fall back to a full final sheet; the player clamps to duration anyway.
		frames = len(sprites) * tileN
	}
	m := TrickplayManifest{
		IntervalSec: trickplayInterval,
		Width:       w / trickplayTile,
		Height:      hgt / trickplayTile,
		Tile:        trickplayTile,
		Frames:      frames,
	}
	b, _ := json.Marshal(m)
	tmp := filepath.Join(outDir, ".manifest.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(outDir, "manifest.json"))
}

// spriteFrames estimates the preview-frame count from the source duration —
// duration/interval, rounded up (matches fps=1/N's output cadence closely
// enough for clamping).
func spriteFrames(ctx context.Context, src string) (int, error) {
	p, err := Probe(ctx, src)
	if err != nil {
		return 0, err
	}
	d, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("unknown duration %q", p.Format.Duration)
	}
	return int(d/trickplayInterval) + 1, nil
}

// imageSize reads a JPEG's pixel dimensions via ffprobe (no image decoder dep).
func imageSize(ctx context.Context, path string) (w, h int, err error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		return 0, 0, err
	}
	if _, err := fmt.Sscanf(string(out), "%d,%d", &w, &h); err != nil {
		return 0, 0, fmt.Errorf("parse sprite size %q: %w", string(out), err)
	}
	return w, h, nil
}

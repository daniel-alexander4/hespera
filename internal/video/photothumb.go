package video

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// PhotoThumb renders a downscaled webp of a still image or a video clip's
// early frame — the photo grid's thumbnails and the display renditions for
// formats a browser can't decode (HEIC/TIFF). Gated by the shared ffmpeg
// semaphore; temp-file + atomic rename so a partial thumb is never served.
//
// orientation is the EXIF orientation (1-8; 0/1 = none) for stills — ffmpeg
// does not auto-apply EXIF orientation to image inputs, so the transpose is
// explicit here. Video clips rotate via ffmpeg's default display-matrix
// autorotation instead.
func PhotoThumb(ctx context.Context, src, dst string, maxDim, orientation int, isVideo bool) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	release, err := acquire(ctx)
	if err != nil {
		// ErrBusy so the thumb job leaves the row pending (retried next run)
		// instead of recording a saturated gate as a permanent file failure.
		return fmt.Errorf("photo thumb acquire slot: %w: %v", ErrBusy, err)
	}
	defer release()

	lead := 0.0
	if isVideo {
		lead = 1 // skip a camcorder's black lead-in frame
	}
	err = photoThumbOnce(ctx, src, dst, maxDim, orientation, isVideo, lead)
	if err != nil && isVideo && ctx.Err() == nil {
		// A clip shorter than the lead-in seeks past EOF and grabs no frame —
		// retry from the first frame rather than marking it unavailable.
		err = photoThumbOnce(ctx, src, dst, maxDim, orientation, true, 0)
	}
	return err
}

// FrameGrab renders a downscaled webp of the video frame at seekSec — the TV
// episode thumbnails. Same gate/temp/rename contract as PhotoThumb (including
// ErrBusy on a saturated gate). A seek past EOF grabs nothing, so it falls
// back toward the start (seekSec → 1s → 0) before giving up.
func FrameGrab(ctx context.Context, src, dst string, maxDim int, seekSec float64) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	release, err := acquire(ctx)
	if err != nil {
		return fmt.Errorf("frame grab acquire slot: %w: %v", ErrBusy, err)
	}
	defer release()

	for _, seek := range frameGrabLadder(seekSec) {
		if err = photoThumbOnce(ctx, src, dst, maxDim, 0, true, seek); err == nil || ctx.Err() != nil {
			return err
		}
	}
	return err
}

// frameGrabLadder is the fallback sequence of seek offsets FrameGrab tries.
func frameGrabLadder(seekSec float64) []float64 {
	ladder := []float64{seekSec}
	if seekSec > 1 {
		ladder = append(ladder, 1)
	}
	if seekSec > 0 {
		ladder = append(ladder, 0)
	}
	return ladder
}

func photoThumbOnce(ctx context.Context, src, dst string, maxDim, orientation int, isVideo bool, seekSec float64) error {
	var vf []string
	if !isVideo {
		vf = append(vf, orientationFilters(orientation)...)
	}
	vf = append(vf, fmt.Sprintf("scale=w=%d:h=%d:force_original_aspect_ratio=decrease", maxDim, maxDim))

	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if seekSec > 0 {
		args = append(args, "-ss", strconv.FormatFloat(seekSec, 'f', 3, 64)) // input-side fast seek (keyframe-accurate)
	}
	args = append(args,
		"-i", src,
		"-map", "0:v:0", "-an", "-sn",
		"-frames:v", "1",
		"-vf", strings.Join(vf, ","),
		"-f", "webp", "-quality", "82",
	)
	tmp := dst + ".tmp"
	args = append(args, tmp)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(tmp)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg photo thumb: %w: %s", err, tail(string(out), 300))
	}
	return os.Rename(tmp, dst)
}

// orientationFilters maps an EXIF orientation to the ffmpeg filters that
// upright the image. Values per the EXIF spec; 0/1 (or out of range) = none.
func orientationFilters(o int) []string {
	switch o {
	case 2:
		return []string{"hflip"}
	case 3:
		return []string{"hflip", "vflip"}
	case 4:
		return []string{"vflip"}
	case 5:
		return []string{"transpose=0"}
	case 6:
		return []string{"transpose=1"}
	case 7:
		return []string{"transpose=3"}
	case 8:
		return []string{"transpose=2"}
	default:
		return nil
	}
}

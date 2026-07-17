package audiobookscan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"hespera/internal/jobs"
	"hespera/internal/video"
)

// thumbUnavailable marks a book with no extractable cover (no attached-pic
// stream) or a real decode failure — the grid shows the placeholder and the
// job won't retry until the file's bytes change (the scanner resets thumb_path
// to ” then). A transient ffmpeg-gate saturation (video.ErrBusy) leaves ”
// so the next run retries.
const thumbUnavailable = "unavailable"

const thumbMaxDim = 480

// ThumbFileNames returns the generated files belonging to an audiobook id
// (for prune/delete reaping).
func ThumbFileNames(id int64) []string {
	return []string{fmt.Sprintf("audiobook_%d.webp", id)}
}

func (s *Scanner) thumbsDir() string {
	return filepath.Join(s.Cfg.DataDir, "thumbs", "audiobooks")
}

// GenerateThumbsMissing produces a cover webp for every audiobook whose thumb
// is pending (”): the embedded attached-pic cover is extracted
// (video.ExtractCoverArt) and downscaled through video.PhotoThumb.
func (s *Scanner) GenerateThumbsMissing(ctx context.Context, jobID, libraryID int64) error {
	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, abs_path FROM audiobooks WHERE library_id=? AND thumb_path=''", libraryID)
	if err != nil {
		return err
	}
	type cand struct {
		id      int64
		absPath string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if rows.Scan(&c.id, &c.absPath) == nil {
			cands = append(cands, c)
		}
	}
	rows.Close()
	if len(cands) == 0 {
		return nil
	}
	if err := os.MkdirAll(s.thumbsDir(), 0o755); err != nil {
		return err
	}
	_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", len(cands), jobID)

	for i, c := range cands {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.ShouldYield != nil && i > 0 && s.ShouldYield() {
			_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i, jobID)
			return jobs.ErrYielded
		}
		_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i+1, jobID)

		dst := filepath.Join(s.thumbsDir(), ThumbFileNames(c.id)[0])
		mark := dst
		if err := s.buildCoverThumb(ctx, c.absPath, dst); err != nil {
			if errors.Is(err, video.ErrBusy) {
				continue // gate saturation — leave '' for the next run
			}
			// A cover-less audiobook is the common case, not a fault — mark
			// without a warning; the grid shows the placeholder.
			mark = thumbUnavailable
			slog.Debug("audiobookscan: no cover thumb", "path", c.absPath, "err", err)
		}
		_, _ = s.DB.ExecContext(ctx, "UPDATE audiobooks SET thumb_path=? WHERE id=?", mark, c.id)
	}
	return nil
}

func (s *Scanner) buildCoverThumb(ctx context.Context, absPath, dst string) error {
	art, err := video.ExtractCoverArt(ctx, absPath)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.thumbsDir(), ".cover-*.img")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	_, writeErr := tmp.Write(art)
	if closeErr := tmp.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return writeErr
	}
	return video.PhotoThumb(ctx, tmpPath, dst, thumbMaxDim, 0, false)
}

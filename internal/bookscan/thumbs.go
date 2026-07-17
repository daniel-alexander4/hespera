package bookscan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"hespera/internal/jobs"
	"hespera/internal/video"
)

// thumbUnavailable marks a book whose cover genuinely can't be produced (no
// embedded cover, or a real decode failure) — the grid shows the generic
// placeholder and the job won't retry until the file's bytes change (the
// scanner resets thumb_path to ” on a size/mtime change). A transient
// ffmpeg-gate saturation (video.ErrBusy) leaves ” so the next run retries.
const thumbUnavailable = "unavailable"

const thumbMaxDim = 480

// ThumbFileNames returns the generated files belonging to a book id (for
// prune/delete reaping).
func ThumbFileNames(id int64) []string {
	return []string{fmt.Sprintf("book_%d.webp", id)}
}

func (s *Scanner) thumbsDir() string {
	return filepath.Join(s.Cfg.DataDir, "thumbs", "books")
}

// GenerateThumbsMissing produces a cover webp for every book whose thumb is
// pending (”): the EPUB cover entry or the CBZ first page is extracted to a
// temp file and downscaled through video.PhotoThumb (the photo-thumb ffmpeg
// pipeline). PDF and Tier-2 formats have no extractable cover → unavailable
// (Chromium renders the PDF itself, but nothing rasterizes a page server-side
// without a heavy dep).
func (s *Scanner) GenerateThumbsMissing(ctx context.Context, jobID, libraryID int64) error {
	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, abs_path, format FROM books WHERE library_id=? AND thumb_path=''", libraryID)
	if err != nil {
		return err
	}
	type cand struct {
		id      int64
		absPath string
		format  string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if rows.Scan(&c.id, &c.absPath, &c.format) == nil {
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
		if err := s.buildCoverThumb(ctx, c.absPath, c.format, dst); err != nil {
			if errors.Is(err, video.ErrBusy) {
				continue // gate saturation — leave '' for the next run
			}
			if !errors.Is(err, errNoCover) {
				slog.Warn("bookscan: cover thumb failed", "path", c.absPath, "err", err)
			}
			mark = thumbUnavailable
		}
		_, _ = s.DB.ExecContext(ctx, "UPDATE books SET thumb_path=? WHERE id=?", mark, c.id)
	}
	return nil
}

// errNoCover marks the expected no-cover cases (PDF, Tier-2, coverless EPUB) —
// unavailable without a warning.
var errNoCover = errors.New("no extractable cover")

func (s *Scanner) buildCoverThumb(ctx context.Context, absPath, format, dst string) error {
	var entry string
	switch format {
	case "epub":
		m, err := ParseEPUB(absPath)
		if err != nil {
			return err
		}
		if m.CoverEntry == "" {
			return errNoCover
		}
		entry = m.CoverEntry
	case "cbz":
		names, err := CBZPages(absPath)
		if err != nil {
			return err
		}
		entry = names[0]
	default:
		return errNoCover
	}

	rc, err := ZipEntry(absPath, entry)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.thumbsDir(), ".cover-*"+filepath.Ext(entry))
	if err != nil {
		rc.Close()
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	_, copyErr := io.Copy(tmp, rc)
	rc.Close()
	if closeErr := tmp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return copyErr
	}
	return video.PhotoThumb(ctx, tmpPath, dst, thumbMaxDim, 0, false)
}

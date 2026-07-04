package tvscan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"hespera/internal/pathguard"
	"hespera/internal/video"
)

// Episode thumbnail generation — a screen capture per episode file, shown on
// the season page's episode rows. Follows the photos thumb-job pattern:
// tv_series_files.thumb_path is '' (pending — the scanner resets it when a
// file's bytes change), 'unavailable' (a real grab failure, retried when the
// bytes change), or the generated file's path. Files are id-keyed under
// DataDir/thumbs/episodes — deliberately NOT thumbs/tv, which thumbgc sweeps
// against tv_series_art/people references and would reap per-file thumbs.
// Deleted by the scanner's prune pass and by librariesDelete (mirroring
// photos: no scan runs after a library delete).

const (
	epThumbMaxDim = 480
	// thumbUnavailable mirrors photoscan's sentinel: a genuine grab failure,
	// not retried until the file's bytes change. A transient ffmpeg-gate
	// acquire failure (video.ErrBusy) never writes this.
	thumbUnavailable = "unavailable"
	// epThumbFallbackOffset is the grab point when a file has no detected
	// intro and no stored duration (a probe failure or a container without a
	// container-level duration) — far enough in to clear cold opens; the
	// FrameGrab ladder handles files shorter than this.
	epThumbFallbackOffset = 120
	// epThumbIntroLead is how far past a detected intro's end to grab.
	epThumbIntroLead = 2
	// epThumbDurationFrac is the grab point as a fraction of the episode when
	// no intro is known — past most cold opens and title cards, well clear of
	// end credits.
	epThumbDurationFrac = 0.25
)

// EpisodeThumbFileName returns the generated thumb file name for a TV file id.
func EpisodeThumbFileName(id int64) string { return fmt.Sprintf("ep_%d.webp", id) }

func (s *Scanner) episodeThumbsDir() string {
	return filepath.Join(s.Cfg.DataDir, "thumbs", "episodes")
}

// GenerateThumbsMissing frame-grabs a thumbnail for every episode file without
// one (thumb_path=''). The reprobe shape: candidates only, near-free no-op
// when nothing is missing. Extras are excluded (their rows render no art),
// matching trickplay. Failures mark the row unavailable rather than erroring
// the job — one bad file must not starve the rest.
func (s *Scanner) GenerateThumbsMissing(ctx context.Context, jobID, libraryID int64) error {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, abs_path, stream_info_json FROM tv_series_files WHERE library_id=? AND is_extra=0 AND thumb_path=''`,
		libraryID)
	if err != nil {
		return err
	}
	type cand struct {
		id         int64
		absPath    string
		streamJSON string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.absPath, &c.streamJSON); err != nil {
			rows.Close()
			return err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(cands) == 0 {
		return nil
	}
	if err := os.MkdirAll(s.episodeThumbsDir(), 0o755); err != nil {
		return err
	}
	sweepStaleThumbTemps(s.episodeThumbsDir())
	_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", len(cands), jobID)

	for i, c := range cands {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		clean, err := pathguard.ResolveExistingUnderRoot(s.Cfg.MediaRoot, c.absPath)
		if err != nil {
			continue // gone or moved since the scan — prune's problem, stays pending
		}
		dst := filepath.Join(s.episodeThumbsDir(), EpisodeThumbFileName(c.id))
		mark := dst
		if err := video.FrameGrab(ctx, clean, dst, epThumbMaxDim, s.thumbOffset(ctx, c.id, c.streamJSON)); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, video.ErrBusy) {
				// Gate saturation, not a broken file — leave thumb_path=''
				// so the next thumb job retries it.
				slog.Warn("tvscan episode thumb busy, will retry", "path", clean, "err", err)
				continue
			}
			slog.Warn("tvscan episode thumb", "path", clean, "err", err)
			mark = thumbUnavailable
		}
		if _, err := s.DB.ExecContext(ctx, "UPDATE tv_series_files SET thumb_path=? WHERE id=?", mark, c.id); err != nil {
			return err
		}
		if (i+1)%25 == 0 || i+1 == len(cands) {
			_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i+1, jobID)
		}
	}
	return nil
}

// thumbOffset picks where in the episode to grab: just past a
// fingerprint-detected intro when one exists (the frame right after the title
// sequence — actual episode content), else a fraction into the runtime, else
// a fixed offset when no duration is stored (probe failure, or a container
// with no container-level duration). Best-effort — the FrameGrab ladder
// covers an offset past EOF.
func (s *Scanner) thumbOffset(ctx context.Context, fileID int64, streamJSON string) float64 {
	var start, end float64
	err := s.DB.QueryRowContext(ctx,
		"SELECT start_sec, end_sec FROM tv_skip_segments WHERE file_id=? AND kind='intro'",
		fileID).Scan(&start, &end)
	if err == nil && end > start {
		return end + epThumbIntroLead
	}
	var probe video.ProbeResult
	if json.Unmarshal([]byte(streamJSON), &probe) == nil {
		if dur, err := strconv.ParseFloat(probe.Format.Duration, 64); err == nil && dur > 0 {
			return dur * epThumbDurationFrac
		}
	}
	return epThumbFallbackOffset
}

// sweepStaleThumbTemps removes temp files a hard-killed prior run left behind
// (the temp+rename pattern leaks its .tmp on SIGKILL). Age-gated well past
// the 60s per-grab ceiling so an in-flight build is never touched.
func sweepStaleThumbTemps(dir string) {
	tmps, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	for _, p := range tmps {
		if st, err := os.Stat(p); err == nil && time.Since(st.ModTime()) > time.Hour {
			_ = os.Remove(p)
		}
	}
}

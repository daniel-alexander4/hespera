package tvscan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"hespera/internal/jobs"
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
	// epThumbIntroLead is the clearance past a detected intro's end when the
	// grab point must be pushed beyond the intro. Generous because the
	// fingerprint end is audio-based and undershoots the visuals — the title
	// card's tail and post-intro credit overlays linger well past where the
	// theme ends (observed live: a +2s lead landed on the Doctor Who title
	// card itself).
	epThumbIntroLead = 30
	// epThumbDurationFrac is the grab point as a fraction of the episode when
	// no intro is known — past most cold opens and title cards, well clear of
	// end credits.
	epThumbDurationFrac = 0.25
)

// EpisodeThumbFileName returns the generated thumb file name for a TV file id.
func EpisodeThumbFileName(id int64) string { return fmt.Sprintf("ep_%d.webp", id) }

// episodeThumbShard returns the id's two-hex-digit shard subdir: the episodes
// dir fans out into 256 subdirs so a very large library never piles hundreds
// of thousands of entries into one directory (readdir/glob over a single flat
// dir degrades badly at that scale).
func episodeThumbShard(id int64) string { return fmt.Sprintf("%02x", id&0xff) }

// EpisodeThumbRelPaths returns the candidate locations of a file id's thumb,
// relative to the episodes thumb dir: the sharded path first, then the
// pre-shard flat path (rows written before sharding point there — serving
// always uses the DB-stored path, so only removal needs this fallback).
func EpisodeThumbRelPaths(id int64) []string {
	name := EpisodeThumbFileName(id)
	return []string{filepath.Join(episodeThumbShard(id), name), name}
}

func (s *Scanner) episodeThumbsDir() string {
	return filepath.Join(s.Cfg.DataDir, "thumbs", "episodes")
}

// GenerateThumbsMissing frame-grabs a thumbnail for every episode file without
// one (thumb_path=”). The reprobe shape: candidates only, near-free no-op
// when nothing is missing. Extras are excluded (their rows render no art),
// matching trickplay. Failures mark the row unavailable rather than erroring
// the job — one bad file must not starve the rest.
func (s *Scanner) GenerateThumbsMissing(ctx context.Context, jobID, libraryID int64) error {
	// json_extract pulls just the duration — the full stream_info_json blob is
	// multiple KB per row, and slurping it for every candidate would cost
	// hundreds of MB on a large library's first pass.
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, abs_path, COALESCE(json_extract(stream_info_json, '$.format.duration'), '')
		 FROM tv_series_files WHERE library_id=? AND is_extra=0 AND thumb_path=''`,
		libraryID)
	if err != nil {
		return err
	}
	type cand struct {
		id       int64
		absPath  string
		duration string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.absPath, &c.duration); err != nil {
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
		// Yield to a waiting interactive job (scan/match/probe) rather than block
		// it behind this sweep; the worker requeues this row to finish the rest.
		// Flush real progress first so the paused row is honest.
		if s.ShouldYield != nil && i > 0 && s.ShouldYield() {
			_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i, jobID)
			return jobs.ErrYielded
		}
		// Progress by files examined (loop head) so a busy/gone file that
		// continues still advances the bar — a finished job never shows 0/N.
		if (i+1)%25 == 0 || i+1 == len(cands) {
			_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i+1, jobID)
		}
		clean, err := pathguard.ResolveExistingUnderRoot(s.Cfg.MediaRoot, c.absPath)
		if err != nil {
			continue // gone or moved since the scan — prune's problem, stays pending
		}
		dst := filepath.Join(s.episodeThumbsDir(), episodeThumbShard(c.id), EpisodeThumbFileName(c.id))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		mark := dst
		if err := video.FrameGrab(ctx, clean, dst, epThumbMaxDim, s.thumbOffset(ctx, c.id, c.duration)); err != nil {
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
	}
	return nil
}

// thumbOffset picks where in the episode to grab: a fraction into the
// runtime, else a fixed offset when no duration is stored (probe failure, or
// a container with no container-level duration). duration is the probe's
// format duration string, json_extract'ed by the candidate query. A
// fingerprint-detected intro acts as a FLOOR, not an anchor: only when the
// computed point would land inside or just past the intro is it pushed to
// intro end + lead — anchoring AT the intro's end put title cards and credit
// overlays in the thumbs (the fingerprint end undershoots the visuals).
// Best-effort — the FrameGrab ladder covers an offset past EOF.
func (s *Scanner) thumbOffset(ctx context.Context, fileID int64, duration string) float64 {
	offset := float64(epThumbFallbackOffset)
	if dur, err := strconv.ParseFloat(duration, 64); err == nil && dur > 0 {
		offset = dur * epThumbDurationFrac
	}
	var start, end float64
	err := s.DB.QueryRowContext(ctx,
		"SELECT start_sec, end_sec FROM tv_skip_segments WHERE file_id=? AND kind='intro'",
		fileID).Scan(&start, &end)
	if err == nil && end > start && offset < end+epThumbIntroLead {
		offset = end + epThumbIntroLead
	}
	return offset
}

// sweepStaleThumbTemps removes temp files a hard-killed prior run left behind
// (the temp+rename pattern leaks its .tmp on SIGKILL). Covers the shard
// subdirs and the pre-shard flat layout. Age-gated well past the 60s per-grab
// ceiling so an in-flight build is never touched.
func sweepStaleThumbTemps(dir string) {
	tmps, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	sharded, _ := filepath.Glob(filepath.Join(dir, "*", "*.tmp"))
	tmps = append(tmps, sharded...)
	for _, p := range tmps {
		if st, err := os.Stat(p); err == nil && time.Since(st.ModTime()) > time.Hour {
			_ = os.Remove(p)
		}
	}
}

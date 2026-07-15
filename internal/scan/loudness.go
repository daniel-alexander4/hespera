package scan

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"hespera/internal/jobs"
	"hespera/internal/music"
	"hespera/internal/pathguard"
	"hespera/internal/video"
)

// AnalyzeLoudness measures integrated loudness (LUFS) and true peak (dBTP) for
// tracks that don't have them yet and writes them back for playback volume
// leveling. Chained after every music scan: snapshot the candidates, best-effort
// per file (missing file, ffmpeg error, or DB error is logged/skipped, never
// fatal), ffmpeg gated by the shared semaphore.
//
// Candidates are (a) rows with no loudness at all — loudness_lufs=0, i.e. new
// rows or ones the scanner reset on a size/mtime change — and (b) rows with a
// loudness but no true peak, **but only where the peak can actually change what
// the player does**: the one-shot backfill of rows analyzed before the true-peak
// column existed (the ReprobeMissing/display_aspect_ratio idiom).
//
// (b) is deliberately narrow. music.LevelGainDB reads the true peak only to cap a
// *boost*, and a track at or above the target is only ever attenuated — a cut
// can't clip — so measuring its peak costs a full decode for a number nothing
// will ever read. Restricting the backfill to `loudness_lufs < target`
// (music.NeedsTruePeak) skips ~63% of a real library with **no behavioral
// difference whatsoever**: a skipped row keeps loudness_tp=0, which LevelGainDB
// already treats as "attenuate normally, never boost" — exactly what it would do
// with the peak measured. Measured cost of not doing this: ~62h of background
// decode on a 13k-track library vs ~23h.
//
// The predicate is tied to music.LoudnessTargetLUFS rather than a literal, so
// moving the target can't silently leave the skipped rows un-boostable — but note
// that lowering the target (e.g. -14 → -12) does widen the boostable set, and
// those newly-boostable rows need a re-sweep to pick up a peak (clear their
// loudness_lufs to re-queue them).
func (s *Scanner) AnalyzeLoudness(ctx context.Context, jobID, libraryID int64) error {
	mediaRoot := filepath.Clean(s.Cfg.MediaRoot)

	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, abs_path FROM music_tracks WHERE library_id=? AND (loudness_lufs=0 OR (loudness_tp=0 AND loudness_lufs < ?))",
		libraryID, music.LoudnessTargetLUFS,
	)
	if err != nil {
		return fmt.Errorf("query tracks to analyze: %w", err)
	}
	type target struct {
		id      int64
		absPath string
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.absPath); err != nil {
			rows.Close()
			return fmt.Errorf("scan track row: %w", err)
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate tracks: %w", err)
	}

	_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", len(targets), jobID)

	analyzed, failed := 0, 0
	for i, t := range targets {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		// Yield to a waiting interactive job (scan/match/probe) rather than block
		// it behind this sweep on the single worker; the worker requeues this row
		// to finish the rest. Flush real progress first so the paused row is honest.
		if s.ShouldYield != nil && i > 0 && s.ShouldYield() {
			_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i, jobID)
			return jobs.ErrYielded
		}
		resolved, err := pathguard.ResolveExistingUnderRoot(mediaRoot, t.absPath)
		if err != nil {
			// File gone/moved — leave it for the next full scan's relink/prune.
			continue
		}
		lufs, truePeak, err := video.LoudnessScan(ctx, resolved)
		if err != nil {
			failed++
			slog.Warn("loudness scan", "track_id", t.id, "path", resolved, "err", err)
			continue
		}
		if _, err := s.DB.ExecContext(ctx,
			"UPDATE music_tracks SET loudness_lufs=?, loudness_tp=? WHERE id=?", lufs, truePeak, t.id,
		); err != nil {
			failed++
			slog.Warn("loudness write", "track_id", t.id, "err", err)
			continue
		}
		analyzed++
		if (i+1)%25 == 0 || i+1 == len(targets) {
			_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i+1, jobID)
		}
	}

	slog.Info("loudness analysis complete",
		"library_id", libraryID, "candidates", len(targets), "analyzed", analyzed, "failed", failed)
	return nil
}

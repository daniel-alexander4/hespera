package scan

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"hespera/internal/jobs"
	"hespera/internal/pathguard"
	"hespera/internal/video"
)

// AnalyzeLoudness measures integrated loudness (LUFS) and true peak (dBTP) for
// tracks that don't have both yet and writes them back for playback volume
// leveling. Candidates are rows missing either measurement: loudness_lufs=0 (new
// rows, or rows the scanner reset on a size/mtime change) or loudness_tp=0 —
// which is also the one-shot backfill of rows analyzed before the true-peak
// column existed (the ReprobeMissing/display_aspect_ratio idiom; the analyzer
// nudges a real 0 reading off the sentinel, so a re-measured row never re-queues).
// Chained after every music scan: snapshot the candidates, best-effort per file
// (missing file, ffmpeg error, or DB error is logged/skipped, never fatal),
// ffmpeg gated by the shared semaphore.
func (s *Scanner) AnalyzeLoudness(ctx context.Context, jobID, libraryID int64) error {
	mediaRoot := filepath.Clean(s.Cfg.MediaRoot)

	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, abs_path FROM music_tracks WHERE library_id=? AND (loudness_lufs=0 OR loudness_tp=0)",
		libraryID,
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

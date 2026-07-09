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

// AnalyzeLoudness measures integrated loudness (LUFS) for tracks that don't
// have it yet (loudness_lufs = 0 — new rows, or rows the scanner reset on a
// size/mtime change) and writes it back for playback volume leveling. Chained
// after every music scan, mirroring tvscan.ReprobeMissing: snapshot the
// candidates, best-effort per file (missing file, ffmpeg error, or DB error is
// logged/skipped, never fatal), ffmpeg gated by the shared semaphore.
func (s *Scanner) AnalyzeLoudness(ctx context.Context, jobID, libraryID int64) error {
	mediaRoot := filepath.Clean(s.Cfg.MediaRoot)

	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, abs_path FROM music_tracks WHERE library_id=? AND loudness_lufs=0",
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
		// it behind this sweep on the single worker; re-enqueued to finish the rest.
		if s.ShouldYield != nil && i > 0 && s.ShouldYield() {
			return jobs.ErrYielded
		}
		resolved, err := pathguard.ResolveExistingUnderRoot(mediaRoot, t.absPath)
		if err != nil {
			// File gone/moved — leave it for the next full scan's relink/prune.
			continue
		}
		lufs, err := video.LoudnessScan(ctx, resolved)
		if err != nil {
			failed++
			slog.Warn("loudness scan", "track_id", t.id, "path", resolved, "err", err)
			continue
		}
		if _, err := s.DB.ExecContext(ctx,
			"UPDATE music_tracks SET loudness_lufs=? WHERE id=?", lufs, t.id,
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

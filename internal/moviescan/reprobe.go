package moviescan

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"

	"hespera/internal/pathguard"
	"hespera/internal/video"
)

// ReprobeMissing re-runs ffprobe on movie files whose stored stream info is
// empty — a probe that failed at scan time (often transiently: video.Probe's
// semaphore acquire fails fast under playback contention), or rows that predate
// stream_info_json — and writes the result back, so the seekable HLS path
// always has the source duration it needs up front. Without it that path falls
// back to a live ffprobe on every manifest request, or 500s outright when the
// live probe also fails. A normal rescan won't heal these rows (the size+mtime
// fast-path skips re-probing an unchanged file), which is why this is a
// separate job, chained after every movie scan. Mirrors tvscan.ReprobeMissing.
//
// Fully-probed rows are left untouched. ffprobe is gated by the shared ffmpeg
// semaphore (video.Probe acquires it), so a large backfill yields to live
// playback. Best-effort per file: a missing file, probe error, or DB error is
// logged/skipped, never fatal.
func (s *Scanner) ReprobeMissing(ctx context.Context, jobID, libraryID int64) error {
	mediaRoot := filepath.Clean(s.Cfg.MediaRoot)

	// Snapshot the candidate list first so the query isn't held open across probes.
	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, abs_path FROM movie_files WHERE library_id=? AND stream_info_json IN ('', '{}')",
		libraryID,
	)
	if err != nil {
		return fmt.Errorf("query movie files to reprobe: %w", err)
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
			return fmt.Errorf("scan movie file row: %w", err)
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate movie files: %w", err)
	}

	_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", len(targets), jobID)

	reprobed, failed := 0, 0
	for i, t := range targets {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resolved, err := pathguard.ResolveExistingUnderRoot(mediaRoot, t.absPath)
		if err != nil {
			// File gone/moved — leave it for the next full scan's relink/prune.
			continue
		}
		probe, err := video.Probe(ctx, resolved) // gated by the ffmpeg semaphore
		if err != nil {
			failed++
			slog.Warn("movie reprobe", "file_id", t.id, "path", resolved, "err", err)
			continue
		}
		b, _ := json.Marshal(probe)
		if _, err := s.DB.ExecContext(ctx,
			"UPDATE movie_files SET stream_info_json=?, updated_at=datetime('now') WHERE id=?",
			string(b), t.id,
		); err != nil {
			failed++
			slog.Warn("movie reprobe write", "file_id", t.id, "err", err)
			continue
		}
		reprobed++
		if (i+1)%25 == 0 || i+1 == len(targets) {
			_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i+1, jobID)
		}
	}

	slog.Info("movie reprobe complete",
		"library_id", libraryID,
		"candidates", len(targets),
		"reprobed", reprobed,
		"failed", failed,
	)
	return nil
}

package tvscan

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"hespera/internal/pathguard"
	"hespera/internal/video"
)

// nativeContainers direct-play in the browser (seekable via HTTP byte ranges).
// Anything else routes to the remux or HLS path. Used only for the summary log,
// so this is a coarse container check, not the per-client playback decision.
var nativeContainers = map[string]bool{"mp4": true, "m4v": true, "mov": true}

// ReprobeMissing re-runs ffprobe on TV files whose stored stream info is empty —
// a probe that failed at scan time, or rows that predate stream_info_json — and
// writes the result back, so the seekable HLS path always has the source duration
// it needs up front. Without it that path falls back to a live ffprobe on every
// manifest request, or 500s outright when the live probe also fails. A normal
// rescan won't heal these rows (the size+mtime fast-path skips re-probing an
// unchanged file), which is why this is a separate job.
//
// Fully-probed rows are left untouched. ffprobe is gated by the shared ffmpeg
// semaphore (video.Probe acquires it), so a large backfill yields to live
// playback. Best-effort per file: a missing file, probe error, or DB error is
// logged/skipped, never fatal.
func (s *Scanner) ReprobeMissing(ctx context.Context, jobID, libraryID int64) error {
	mediaRoot := filepath.Clean(s.Cfg.MediaRoot)

	// Snapshot the candidate list first so the query isn't held open across probes.
	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, abs_path FROM tv_series_files WHERE library_id=? AND stream_info_json IN ('', '{}')",
		libraryID,
	)
	if err != nil {
		return fmt.Errorf("query tv files to reprobe: %w", err)
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
			return fmt.Errorf("scan tv file row: %w", err)
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate tv files: %w", err)
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
			slog.Warn("tv reprobe", "file_id", t.id, "path", resolved, "err", err)
			continue
		}
		b, _ := json.Marshal(probe)
		if _, err := s.DB.ExecContext(ctx,
			"UPDATE tv_series_files SET stream_info_json=?, updated_at=datetime('now') WHERE id=?",
			string(b), t.id,
		); err != nil {
			failed++
			slog.Warn("tv reprobe write", "file_id", t.id, "err", err)
			continue
		}
		reprobed++
		if (i+1)%25 == 0 || i+1 == len(targets) {
			_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i+1, jobID)
		}
	}

	slog.Info("tv reprobe complete",
		"library_id", libraryID,
		"candidates", len(targets),
		"reprobed", reprobed,
		"failed", failed,
		"non_native_containers", s.countNonNativeContainer(ctx, libraryID),
	)
	return nil
}

// countNonNativeContainer counts library files whose container isn't browser-native
// (mp4/m4v/mov) and so can never direct-play — they always route to the
// progressive remux path or the seekable HLS path. Surfaced in the completion log
// so the share of files that can't direct-play is visible. Best-effort; 0 on error.
func (s *Scanner) countNonNativeContainer(ctx context.Context, libraryID int64) int {
	rows, err := s.DB.QueryContext(ctx,
		"SELECT COALESCE(container,'') FROM tv_series_files WHERE library_id=?", libraryID)
	if err != nil {
		return 0
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil && !nativeContainers[strings.ToLower(c)] {
			n++
		}
	}
	return n
}

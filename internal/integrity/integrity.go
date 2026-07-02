// Package integrity examines a library's video files for corruption and, for
// the losslessly-repairable kind (container/framing damage), auto-repairs them
// in place. It is the per-library orchestration around the per-file primitives
// in internal/video (CheckIntegrity/RepairFile); it is the ONE place Hespera
// writes back into MEDIA_ROOT (every other path treats media files as read-only
// inputs it reconciles toward). The repair is verify-before-overwrite +
// atomic-rename, so a good original is never lost to a bad remux.
package integrity

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"hespera/internal/pathguard"
	"hespera/internal/video"
)

// videoTables are the file tables this package may scan+update. Restricting to a
// known set keeps the table name — interpolated into SQL — a trusted constant.
var videoTables = map[string]bool{"tv_series_files": true, "movie_files": true}

// CheckLibrary is the cheap tier-1 pass, chained after a scan: it demuxes each
// not-yet-checked video file (no decode, a few seconds each) to find CONTAINER
// corruption and, when repair is true, losslessly repairs it in place (remux →
// verify → atomic replace). Only files whose integrity_status is ” (new or
// changed since the last check) are touched. Best-effort per file — a missing
// file, ffmpeg error, or DB error is logged and skipped, never fatal. ffmpeg is
// gated by the shared semaphore, so a backfill yields to live playback.
func CheckLibrary(ctx context.Context, db *sql.DB, mediaRoot, table string, jobID, libraryID int64, repair bool) error {
	return run(ctx, db, mediaRoot, table, jobID, libraryID, false, repair)
}

// DeepCheckLibrary fully DECODES each video file to detect BITSTREAM corruption
// (damaged coded frames) the container check can't see. That damage is
// unrecoverable, so this tier only FLAGS — it never modifies a file. Expensive
// (a full decode per file), so it is opt-in (the Libraries "Check integrity"
// button), not chained after a scan. Files already flagged are skipped.
func DeepCheckLibrary(ctx context.Context, db *sql.DB, mediaRoot, table string, jobID, libraryID int64) error {
	return run(ctx, db, mediaRoot, table, jobID, libraryID, true, false)
}

func run(ctx context.Context, db *sql.DB, mediaRoot, table string, jobID, libraryID int64, deep, repair bool) error {
	if !videoTables[table] {
		return fmt.Errorf("integrity: unsupported table %q", table)
	}
	mediaRoot = filepath.Clean(mediaRoot)

	// Tier-1 only revisits unchecked/changed rows; the deep tier re-audits
	// everything except files already known bad.
	query := "SELECT id, abs_path FROM " + table + " WHERE library_id=?"
	if deep {
		query += " AND integrity_status<>'flagged'"
	} else {
		query += " AND integrity_status=''"
	}
	rows, err := db.QueryContext(ctx, query, libraryID)
	if err != nil {
		return fmt.Errorf("integrity query %s: %w", table, err)
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
			return fmt.Errorf("integrity scan row: %w", err)
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("integrity iterate: %w", err)
	}

	_, _ = db.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", len(targets), jobID)

	var clean, repaired, flagged, failed int
	for i, t := range targets {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resolved, err := pathguard.ResolveExistingUnderRoot(mediaRoot, t.absPath)
		if err != nil {
			continue // gone/moved — the next full scan's prune handles it
		}
		var status string
		if deep {
			status = flagDeep(ctx, db, table, t.id, resolved)
		} else {
			status = repairOne(ctx, db, table, t.id, resolved, repair)
		}
		switch status {
		case "repaired":
			repaired++
		case "flagged":
			flagged++
		case "ok":
			clean++
		default:
			failed++
		}
		if (i+1)%10 == 0 || i+1 == len(targets) {
			_, _ = db.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i+1, jobID)
		}
	}

	slog.Info("integrity check complete",
		"table", table, "library_id", libraryID, "deep", deep,
		"candidates", len(targets), "clean", clean, "repaired", repaired, "flagged", flagged, "failed", failed)
	return nil
}

// repairOne runs the cheap container check + optional repair on one file and
// writes the outcome. Returns the outcome status, or "" on a hard error.
func repairOne(ctx context.Context, db *sql.DB, table string, id int64, path string, repair bool) string {
	out, err := video.RepairFile(ctx, path, repair)
	if err != nil {
		slog.Warn("integrity check", "file_id", id, "path", path, "err", err)
		return ""
	}
	if out.Replaced && out.Probe != nil {
		// The file was rewritten: refresh the derived facts so the scanner's
		// (size,mtime) fast-path doesn't re-trigger and playback has fresh info.
		b, _ := json.Marshal(out.Probe)
		if fi, statErr := os.Stat(path); statErr == nil {
			_, _ = db.ExecContext(ctx, "UPDATE "+table+` SET
  stream_info_json=?, file_size_bytes=?, mtime_unix=?,
  integrity_status=?, integrity_detail=?, integrity_checked_at=datetime('now'), updated_at=datetime('now')
WHERE id=?`, string(b), fi.Size(), fi.ModTime().Unix(), out.Status, out.Detail, id)
			slog.Info("integrity repaired", "file_id", id, "path", path, "detail", out.Detail)
			return out.Status
		}
	}
	_, _ = db.ExecContext(ctx,
		"UPDATE "+table+" SET integrity_status=?, integrity_detail=?, integrity_checked_at=datetime('now') WHERE id=?",
		out.Status, out.Detail, id)
	return out.Status
}

// flagDeep fully decodes one file and flags it when bitstream corruption is
// found; it never modifies the file and never un-flags (only the cheap tier /
// a file change clears a status). Returns "flagged" or "ok".
func flagDeep(ctx context.Context, db *sql.DB, table string, id int64, path string) string {
	n, err := video.CheckIntegrity(ctx, path, true)
	if err != nil {
		slog.Warn("integrity deep check", "file_id", id, "path", path, "err", err)
		return ""
	}
	if n > 0 {
		detail := fmt.Sprintf("bitstream corruption (%d decode errors) — data loss, not auto-repairable", n)
		_, _ = db.ExecContext(ctx,
			"UPDATE "+table+" SET integrity_status='flagged', integrity_detail=?, integrity_checked_at=datetime('now') WHERE id=?",
			detail, id)
		slog.Warn("integrity flagged", "file_id", id, "path", path, "decode_errors", n)
		return "flagged"
	}
	_, _ = db.ExecContext(ctx,
		"UPDATE "+table+" SET integrity_checked_at=datetime('now') WHERE id=?", id)
	return "ok"
}

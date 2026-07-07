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
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"hespera/internal/pathguard"
	"hespera/internal/video"
)

// integrityTables are the file tables this package may scan+update. Restricting
// to a known set keeps the table name — interpolated into SQL — a trusted
// constant. Music (`music_tracks`) differs from the video tables: it has no
// stream_info_json but has checksum_sha256, so the post-repair refresh is
// table-aware (see refreshAfterReplace).
var integrityTables = map[string]bool{"tv_series_files": true, "movie_files": true, "music_tracks": true}

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
	if !integrityTables[table] {
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
	// Extras (bonus content) are excluded: the corrupt/degraded pills and the
	// report page speak about titles, and auto-repair shouldn't rewrite bonus
	// files in MEDIA_ROOT. music_tracks has no extras concept (no column).
	if table != "music_tracks" {
		query += " AND is_extra=0"
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

	var clean, repaired, flagged, degraded, failed int
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
		case "degraded":
			degraded++
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
		"candidates", len(targets), "clean", clean, "repaired", repaired, "flagged", flagged, "degraded", degraded, "failed", failed)
	return nil
}

// containerRepairable reports whether a file has a real container whose framing
// damage a lossless remux can fix. Raw .mp3 is the exception: it has no
// container, and ffmpeg's tolerant MP3 demuxer emits benign framing/ID3 noise
// under `-c copy -f null` that the error-line count misreads as corruption — so
// remux-"repairing" it rewrites the user's file for nothing, and each rewrite
// bumps the album dir mtime, re-triggering scans (a library-wide churn). MP3
// integrity is bitstream-only, judged by the decode audit instead (auditDecode,
// which music always runs). FLAC/M4A/OGG/video have real containers and keep the
// repair path.
func containerRepairable(path string) bool {
	return !strings.EqualFold(filepath.Ext(path), ".mp3")
}

// repairFileFn is the container-check + optional-repair primitive, a package var
// so tests can observe that repairOne invokes it for real-container formats and
// NOT for MP3 (the regression guard on the churn fix).
var repairFileFn = video.RepairFile

// repairOne runs the cheap tier on one file: the container check + optional
// repair (skipped for raw MP3, which has no container — see containerRepairable),
// PLUS a cheap audio packet-gap scan (both demux-level, no full decode).
// Container corruption is losslessly repaired in place; an audio gap is missing
// data that can't be repaired, so it flags. Returns the final status, or "" on a
// hard error.
func repairOne(ctx context.Context, db *sql.DB, table string, id int64, path string, repair bool) string {
	var out video.RepairOutcome
	if containerRepairable(path) {
		var err error
		out, err = repairFileFn(ctx, path, repair)
		if err != nil {
			slog.Warn("integrity check", "file_id", id, "path", path, "err", err)
			return ""
		}
	} else {
		// MP3: no container to remux — the decode audit below is the only judge.
		out = video.RepairOutcome{Status: "ok"}
	}
	// Also examine audio (a gap the container check won't see), and — for music,
	// where files are small and MP3 corruption surfaces only on decode — the
	// bitstream. Video keeps the full decode in the opt-in deep tier
	// (minutes/file). Only new/changed files reach here, so the cost is bounded.
	gapDetail := auditAudio(ctx, id, path)
	decodeDetail := ""
	if table == "music_tracks" {
		if dd, hardErr := auditDecode(ctx, id, path); !hardErr {
			decodeDetail = dd
		}
	}
	status, detail := classify(out.Status, out.Detail, gapDetail, decodeDetail)
	if out.Replaced {
		refreshAfterReplace(ctx, db, table, id, path, out, status, detail)
		slog.Info("integrity repaired", "file_id", id, "path", path, "detail", detail)
		return status
	}
	_, _ = db.ExecContext(ctx,
		"UPDATE "+table+" SET integrity_status=?, integrity_detail=?, integrity_checked_at=datetime('now') WHERE id=?",
		status, detail, id)
	return status
}

// refreshAfterReplace updates a row's derived facts after a repair rewrote the
// file, so the scanner's (size,mtime) fast-path doesn't re-trigger and later
// lookups stay consistent. Table-aware: the video tables carry stream_info_json
// (from the fresh probe) + updated_at; music_tracks carries checksum_sha256 — the
// move-relink content signature, which MUST be recomputed since the bytes changed
// (a stale one would break a later move's relink, losing play_history/lyrics) —
// and has no updated_at. On a stat/hash failure it leaves size/mtime/derived
// facts stale so the next scan re-derives them, and only records the status.
func refreshAfterReplace(ctx context.Context, db *sql.DB, table string, id int64, path string, out video.RepairOutcome, status, detail string) {
	fi, err := os.Stat(path)
	if err != nil {
		_, _ = db.ExecContext(ctx, "UPDATE "+table+" SET integrity_status=?, integrity_detail=?, integrity_checked_at=datetime('now') WHERE id=?", status, detail, id)
		return
	}
	if table == "music_tracks" {
		sum, hErr := sha256File(path)
		if hErr != nil {
			slog.Warn("integrity checksum refresh", "file_id", id, "path", path, "err", hErr)
			_, _ = db.ExecContext(ctx, "UPDATE music_tracks SET integrity_status=?, integrity_detail=?, integrity_checked_at=datetime('now') WHERE id=?", status, detail, id)
			return
		}
		_, _ = db.ExecContext(ctx, `UPDATE music_tracks SET
  checksum_sha256=?, file_size_bytes=?, mtime_unix=?,
  integrity_status=?, integrity_detail=?, integrity_checked_at=datetime('now')
WHERE id=?`, sum, fi.Size(), fi.ModTime().Unix(), status, detail, id)
		return
	}
	b, _ := json.Marshal(out.Probe)
	_, _ = db.ExecContext(ctx, "UPDATE "+table+` SET
  stream_info_json=?, file_size_bytes=?, mtime_unix=?,
  integrity_status=?, integrity_detail=?, integrity_checked_at=datetime('now'), updated_at=datetime('now')
WHERE id=?`, string(b), fi.Size(), fi.ModTime().Unix(), status, detail, id)
}

// sha256File computes a file's SHA-256 as lowercase hex — matching the music
// scanner's checksumSHA256 so a repaired track's stored signature stays valid.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// auditAudio scans one file's audio for a significant gap (missing audio) and
// returns a flag detail if found, else "". Demux-only (cheap) — shared by the
// cheap tier (repairOne) and the deep tier (flagDeep) so they never drift.
func auditAudio(ctx context.Context, id int64, path string) string {
	_, largest, err := video.AudioGaps(ctx, path)
	if err != nil {
		slog.Warn("integrity audio gap scan", "file_id", id, "path", path, "err", err)
		return ""
	}
	if largest >= audioGapFlagThreshold {
		return fmt.Sprintf("audio gap %.1fs (missing audio)", largest)
	}
	return ""
}

// auditDecode fully decodes a file and returns a flag detail if bitstream
// corruption (damaged frames the container check can't see) is found, else "".
// The bool is true on a hard failure (couldn't decode at all) so the caller can
// skip rather than mark the file clean. Expensive for video (minutes), cheap for
// small audio files — which is why music runs it at scan time and video doesn't.
func auditDecode(ctx context.Context, id int64, path string) (detail string, hardErr bool) {
	n, err := video.CheckIntegrity(ctx, path, true)
	if err != nil {
		slog.Warn("integrity decode check", "file_id", id, "path", path, "err", err)
		return "", true
	}
	if n > 0 {
		return fmt.Sprintf("bitstream corruption (%d decode errors)", n), false
	}
	return "", false
}

// audioGapFlagThreshold is the smallest audio hole (seconds) worth flagging —
// below it a discontinuity is inaudible jitter, not real data loss.
const audioGapFlagThreshold = 0.5

// degradedSuffix is appended to a degraded row's detail so the stored reason
// reads complete on its own (report page, tooltips, hescli).
const degradedSuffix = " — playable: the transcoder silence-fills the gap; replace the file to restore the missing audio"

// classify folds the audit findings into the container verdict, splitting
// unplayable-grade damage from playable residue:
//
//   - decode errors (damaged frames — audible/visible artifacts) or a
//     container that repair could not fix → "flagged": real corruption that
//     needs action, counted by the corrupt pill and detail-page badges.
//   - an audio gap ALONE on a sound container → "degraded": missing data,
//     but the transcode path silence-fills it (aresample=async=1), so the
//     file plays cleanly. Surfaced only on the integrity report page.
//
// Detail strings accumulate in audit order (container, gap, decode) so the
// stored reason names everything found.
func classify(status, detail, gapDetail, decodeDetail string) (string, string) {
	join := func(d, e string) string {
		if e == "" {
			return d
		}
		if d == "" {
			return e
		}
		return d + "; " + e
	}
	detail = join(detail, gapDetail)
	detail = join(detail, decodeDetail)
	switch {
	case decodeDetail != "" || status == "flagged":
		status = "flagged"
	case gapDetail != "":
		status = "degraded"
		detail += degradedSuffix
	}
	return status, detail
}

// flagDeep examines one file's BOTH streams — a full decode for video bitstream
// corruption plus an audio packet-gap scan for missing audio — and flags it when
// either is damaged. It never modifies the file and never un-flags (only the
// cheap tier / a file change clears a status). Returns "flagged" (decode
// damage), "degraded" (audio-gap-only — playable), or "ok".
func flagDeep(ctx context.Context, db *sql.DB, table string, id int64, path string) string {
	decodeDetail, hardErr := auditDecode(ctx, id, path)
	if hardErr {
		return "" // couldn't decode the file at all — skip, don't mark it ok
	}
	var problems []string
	if decodeDetail != "" {
		problems = append(problems, decodeDetail)
	}
	if ad := auditAudio(ctx, id, path); ad != "" {
		problems = append(problems, ad)
	}
	if len(problems) > 0 {
		status := "flagged"
		detail := strings.Join(problems, "; ") + " — data loss, not auto-repairable"
		if decodeDetail == "" {
			// Audio-gap-only: playable residue, not unplayable damage — the
			// transcode path silence-fills the hole (see classify).
			status = "degraded"
			detail = strings.Join(problems, "; ") + degradedSuffix
		}
		_, _ = db.ExecContext(ctx,
			"UPDATE "+table+" SET integrity_status=?, integrity_detail=?, integrity_checked_at=datetime('now') WHERE id=?",
			status, detail, id)
		slog.Warn("integrity "+status, "file_id", id, "path", path, "detail", detail)
		return status
	}
	_, _ = db.ExecContext(ctx,
		"UPDATE "+table+" SET integrity_checked_at=datetime('now') WHERE id=?", id)
	return "ok"
}

package tvscan

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"hespera/internal/config"
	"hespera/internal/pathguard"
	"hespera/internal/video"
)

type Scanner struct {
	Cfg config.Config
	DB  *sql.DB
}

func New(cfg config.Config, db *sql.DB) *Scanner {
	return &Scanner{Cfg: cfg, DB: db}
}

func (s *Scanner) ScanTV(ctx context.Context, jobID, libraryID int64) error {
	var root string
	if err := s.DB.QueryRowContext(ctx,
		"SELECT root_path FROM libraries WHERE id=? AND type='tv'",
		libraryID,
	).Scan(&root); err != nil {
		return fmt.Errorf("library %d not found or not tv: %w", libraryID, err)
	}

	cleanRoot := filepath.Clean(root)
	mediaRoot := filepath.Clean(s.Cfg.MediaRoot)
	if !strings.HasPrefix(cleanRoot+string(os.PathSeparator), mediaRoot+string(os.PathSeparator)) && cleanRoot != mediaRoot {
		return fmt.Errorf("root_path must be under %s (got %s)", mediaRoot, cleanRoot)
	}

	// Count video files for progress.
	totalFiles := 0
	_ = filepath.WalkDir(cleanRoot, func(_ string, d fs.DirEntry, _ error) error {
		if d != nil && !d.IsDir() && video.IsVideoExt(filepath.Ext(d.Name())) {
			totalFiles++
		}
		return nil
	})
	if totalFiles > 0 {
		_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", totalFiles, jobID)
	}

	processed := 0
	scanErrors := 0
	if err := filepath.WalkDir(cleanRoot, func(p string, d fs.DirEntry, walkErr error) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			// Skip extras/sample subdirectories, but only when nested inside a
			// show — a top-level folder of the same name (the show "Extras",
			// "Trailers"…) is a real library entry and is kept.
			if p != cleanRoot && IsJunkDirName(d.Name()) {
				if rel, relErr := filepath.Rel(cleanRoot, p); relErr == nil && strings.ContainsRune(rel, filepath.Separator) {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !video.IsVideoExt(filepath.Ext(p)) {
			return nil
		}
		// Skip sample/extra clips by their release-tag token (never a real episode).
		if IsJunkFile(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))) {
			return nil
		}

		resolvedPath, err := pathguard.ResolveExistingUnderRoot(mediaRoot, p)
		if err != nil {
			slog.Warn("tvscan guard", "path", p, "err", err)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			slog.Warn("tvscan stat", "path", p, "err", err)
			return nil
		}
		fileSize := info.Size()
		mtimeUnix := info.ModTime().UTC().Unix()

		// Identify episode from filename. This is pure path parsing (no I/O), so
		// it runs on every scan — including for unchanged files below — letting a
		// re-scan reconverge existing identities when the parsing logic improves.
		ident := IdentifyFile(resolvedPath)

		// Check if file is unchanged. If so, skip the expensive probe but still
		// refresh the (cheap, derived) identity so re-scans pick up better
		// filename parsing without the file itself having to change.
		var existingID, existingSize, existingMtime int64
		err = s.DB.QueryRowContext(ctx,
			"SELECT id, file_size_bytes, mtime_unix FROM tv_series_files WHERE library_id=? AND abs_path=?",
			libraryID, resolvedPath,
		).Scan(&existingID, &existingSize, &existingMtime)
		if err == nil && existingSize == fileSize && existingMtime == mtimeUnix {
			if err := s.upsertIdentity(ctx, existingID, ident); err != nil {
				scanErrors++
				slog.Warn("tvscan identity refresh", "path", resolvedPath, "err", err)
			}
			processed++
			if processed%50 == 0 {
				_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)
			}
			return nil
		}

		// Probe the file.
		container := strings.TrimPrefix(strings.ToLower(filepath.Ext(resolvedPath)), ".")
		var streamInfoJSON string
		probeResult, probeErr := video.Probe(ctx, resolvedPath)
		if probeErr != nil {
			slog.Warn("tvscan probe", "path", resolvedPath, "err", probeErr)
			streamInfoJSON = "{}"
		} else {
			b, _ := json.Marshal(probeResult)
			streamInfoJSON = string(b)
		}

		// Upsert file record and identity; log and continue on per-file DB errors.
		if err := s.upsertTVFile(ctx, libraryID, resolvedPath, container, fileSize, mtimeUnix, streamInfoJSON, ident); err != nil {
			scanErrors++
			slog.Warn("tvscan file error", "path", resolvedPath, "err", err)
		}

		processed++
		if processed%50 == 0 || processed == totalFiles {
			_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)
		}
		return nil
	}); err != nil {
		return err
	}

	// Final progress update.
	_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)

	if scanErrors > 0 {
		slog.Warn("tvscan completed with errors", "library_id", libraryID, "files_scanned", processed, "errors", scanErrors)
	}

	if err := s.relinkMovedFiles(ctx, libraryID, cleanRoot); err != nil {
		return err
	}
	return s.pruneMissingFiles(ctx, libraryID, cleanRoot)
}

// upsertTVFile inserts or updates tv_series_files and tv_series_identities for a single file.
// Returns an error if any DB operation fails; the caller logs and continues scanning.
func (s *Scanner) upsertTVFile(ctx context.Context, libraryID int64, resolvedPath, container string, fileSize, mtimeUnix int64, streamInfoJSON string, ident *EpisodeIdentity) error {
	res, err := s.DB.ExecContext(ctx, `
INSERT INTO tv_series_files (library_id, abs_path, container, file_size_bytes, mtime_unix, stream_info_json)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(library_id, abs_path) DO UPDATE SET
  container=excluded.container,
  file_size_bytes=excluded.file_size_bytes,
  mtime_unix=excluded.mtime_unix,
  stream_info_json=excluded.stream_info_json,
  updated_at=datetime('now')
`, libraryID, resolvedPath, container, fileSize, mtimeUnix, streamInfoJSON)
	if err != nil {
		return fmt.Errorf("upsert tv_series_files: %w", err)
	}

	// Get the file ID.
	var fileID int64
	fileID, err = res.LastInsertId()
	if err != nil || fileID == 0 {
		// On conflict update, LastInsertId may be 0; query directly.
		if err := s.DB.QueryRowContext(ctx,
			"SELECT id FROM tv_series_files WHERE library_id=? AND abs_path=?",
			libraryID, resolvedPath,
		).Scan(&fileID); err != nil {
			return fmt.Errorf("get file id: %w", err)
		}
	}

	return s.upsertIdentity(ctx, fileID, ident)
}

// upsertIdentity inserts or refreshes the tv_series_identities row for a file.
// The status guard means a re-scan only overwrites 'unmatched' rows — matched
// and user-skipped rows keep their data. A nil identity never clobbers an
// existing row. Safe to call on every scan, including for unchanged files.
func (s *Scanner) upsertIdentity(ctx context.Context, fileID int64, ident *EpisodeIdentity) error {
	if ident != nil {
		epCSV := episodeNumbersCSV(ident.EpisodeNumbers)
		_, err := s.DB.ExecContext(ctx, `
INSERT INTO tv_series_identities (file_id, status, guessed_title, season_number, episode_numbers_csv, match_confidence, match_method, air_date)
VALUES (?, 'unmatched', ?, ?, ?, ?, ?, ?)
ON CONFLICT(file_id) DO UPDATE SET
  guessed_title=excluded.guessed_title,
  season_number=excluded.season_number,
  episode_numbers_csv=excluded.episode_numbers_csv,
  match_confidence=excluded.match_confidence,
  match_method=excluded.match_method,
  air_date=excluded.air_date
WHERE status NOT IN ('matched', 'skipped')
`, fileID, ident.ShowTitle, ident.SeasonNumber, epCSV, ident.Confidence, ident.Method, ident.AirDate)
		if err != nil {
			return fmt.Errorf("upsert tv_series_identities: %w", err)
		}
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO tv_series_identities (file_id, status, guessed_title, season_number, episode_numbers_csv, match_confidence, match_method)
VALUES (?, 'unmatched', '', -1, '', 0.0, '')
ON CONFLICT(file_id) DO NOTHING
`, fileID)
	if err != nil {
		return fmt.Errorf("upsert tv_series_identities fallback: %w", err)
	}
	return nil
}

func episodeNumbersCSV(eps []int) string {
	if len(eps) == 0 {
		return ""
	}
	parts := make([]string, len(eps))
	for i, e := range eps {
		parts[i] = strconv.Itoa(e)
	}
	return strings.Join(parts, ",")
}

// relinkMovedFiles detects files moved or renamed to a new path (same content)
// and carries their irreplaceable per-file state — the match identity and
// playback progress — onto the new row before pruneMissingFiles deletes the
// orphaned old row. "Same file" is recognized by (file_size_bytes, mtime_unix),
// which a plain `mv` preserves, so we avoid hashing multi-GB video on every
// scan; a move that rewrites mtime (cp, some sync tools) simply falls back to
// prune-and-recreate, where an auto-derived match re-resolves from the filename
// on the next match run anyway. A transfer happens only when exactly one orphan
// and exactly one surviving row share a signature, so duplicate-content files
// are never mis-linked.
func (s *Scanner) relinkMovedFiles(ctx context.Context, libraryID int64, root string) error {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, abs_path, file_size_bytes, mtime_unix FROM tv_series_files WHERE library_id=?`, libraryID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type sig struct{ size, mtime int64 }
	cleanRoot := filepath.Clean(root)
	rootPrefix := cleanRoot + string(os.PathSeparator)
	var orphans []struct {
		id int64
		k  sig
	}
	survivors := map[sig][]int64{}
	orphanCount := map[sig]int{}
	for rows.Next() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var id, size, mtime int64
		var absPath string
		if err := rows.Scan(&id, &absPath, &size, &mtime); err != nil {
			return err
		}
		clean := filepath.Clean(absPath)
		if clean != cleanRoot && !strings.HasPrefix(clean, rootPrefix) {
			continue
		}
		k := sig{size, mtime}
		if _, err := os.Stat(clean); err == nil {
			survivors[k] = append(survivors[k], id)
		} else if os.IsNotExist(err) {
			orphans = append(orphans, struct {
				id int64
				k  sig
			}{id, k})
			orphanCount[k]++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, o := range orphans {
		cand := survivors[o.k]
		if len(cand) != 1 || orphanCount[o.k] != 1 {
			continue // ambiguous signature: leave the orphan for prune
		}
		if err := s.transferFileState(ctx, o.id, cand[0]); err != nil {
			slog.Warn("tvscan relink", "from", o.id, "to", cand[0], "err", err)
		}
	}
	return nil
}

// transferFileState copies a moved file's preserved state from the orphaned old
// row (fromID) onto the new row (toID): the match identity (only when it was
// matched or skipped — an unmatched identity re-derives from the new filename)
// and playback progress. The orphan itself is deleted afterwards by
// pruneMissingFiles.
func (s *Scanner) transferFileState(ctx context.Context, fromID, toID int64) error {
	if _, err := s.DB.ExecContext(ctx, `
UPDATE tv_series_identities AS dst SET
  status = src.status,
  provider = src.provider,
  series_id = src.series_id,
  season_number = src.season_number,
  episode_numbers_csv = src.episode_numbers_csv,
  match_confidence = src.match_confidence,
  match_method = src.match_method,
  matched_at = src.matched_at,
  guessed_title = src.guessed_title,
  air_date = src.air_date
FROM tv_series_identities AS src
WHERE dst.file_id = ? AND src.file_id = ? AND src.status IN ('matched', 'skipped')
`, toID, fromID); err != nil {
		return fmt.Errorf("transfer identity: %w", err)
	}
	if _, err := s.DB.ExecContext(ctx, `
INSERT INTO tv_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at)
SELECT ?, position_seconds, duration_seconds, completed, updated_at
FROM tv_playback_progress WHERE file_id = ?
ON CONFLICT(file_id) DO UPDATE SET
  position_seconds = excluded.position_seconds,
  duration_seconds = excluded.duration_seconds,
  completed = excluded.completed,
  updated_at = excluded.updated_at
`, toID, fromID); err != nil {
		return fmt.Errorf("transfer playback progress: %w", err)
	}
	return nil
}

func (s *Scanner) pruneMissingFiles(ctx context.Context, libraryID int64, root string) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, abs_path FROM tv_series_files WHERE library_id=?`, libraryID)
	if err != nil {
		return err
	}
	defer rows.Close()

	cleanRoot := filepath.Clean(root)
	rootPrefix := cleanRoot + string(os.PathSeparator)
	var staleIDs []int64
	for rows.Next() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var id int64
		var absPath string
		if err := rows.Scan(&id, &absPath); err != nil {
			return err
		}
		clean := filepath.Clean(absPath)
		if clean != cleanRoot && !strings.HasPrefix(clean, rootPrefix) {
			continue
		}
		if _, err := os.Stat(clean); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			slog.Warn("tvscan prune stat", "path", clean, "err", err)
			continue
		}
		staleIDs = append(staleIDs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range staleIDs {
		if _, err := s.DB.ExecContext(ctx, `DELETE FROM tv_series_files WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

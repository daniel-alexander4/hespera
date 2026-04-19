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

	"isomedia/internal/config"
	"isomedia/internal/pathguard"
	"isomedia/internal/video"
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
			return nil
		}
		if !video.IsVideoExt(filepath.Ext(p)) {
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

		// Check if file is unchanged.
		var existingSize int64
		var existingMtime int64
		err = s.DB.QueryRowContext(ctx,
			"SELECT file_size_bytes, mtime_unix FROM tv_series_files WHERE library_id=? AND abs_path=?",
			libraryID, resolvedPath,
		).Scan(&existingSize, &existingMtime)
		if err == nil && existingSize == fileSize && existingMtime == mtimeUnix {
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

		// Identify episode from filename.
		ident := IdentifyFile(resolvedPath)

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

	// Upsert tv_series_identities.
	if ident != nil {
		epCSV := episodeNumbersCSV(ident.EpisodeNumbers)
		_, err = s.DB.ExecContext(ctx, `
INSERT INTO tv_series_identities (file_id, status, guessed_title, season_number, episode_numbers_csv, match_confidence, match_method)
VALUES (?, 'unmatched', ?, ?, ?, ?, ?)
ON CONFLICT(file_id) DO UPDATE SET
  guessed_title=excluded.guessed_title,
  season_number=excluded.season_number,
  episode_numbers_csv=excluded.episode_numbers_csv,
  match_confidence=excluded.match_confidence,
  match_method=excluded.match_method
WHERE status NOT IN ('matched', 'skipped')
`, fileID, ident.ShowTitle, ident.SeasonNumber, epCSV, ident.Confidence, ident.Method)
		if err != nil {
			return fmt.Errorf("upsert tv_series_identities: %w", err)
		}
	} else {
		_, err = s.DB.ExecContext(ctx, `
INSERT INTO tv_series_identities (file_id, status, guessed_title, season_number, episode_numbers_csv, match_confidence, match_method)
VALUES (?, 'unmatched', '', -1, '', 0.0, '')
ON CONFLICT(file_id) DO NOTHING
`, fileID)
		if err != nil {
			return fmt.Errorf("upsert tv_series_identities fallback: %w", err)
		}
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

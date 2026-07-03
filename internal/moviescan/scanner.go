package moviescan

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"hespera/internal/config"
	"hespera/internal/pathguard"
	"hespera/internal/tvscan"
	"hespera/internal/video"
)

// Scanner walks a movies library, identifying each video file as a Title+Year
// and recording it in movie_files. It is a strict simplification of tvscan: no
// season/episode cascade, a single-table upsert (match state lives inline on
// movie_files and is owned by the matcher, never touched here), and the same
// move-relink/prune passes keyed on (file_size_bytes, mtime_unix).
type Scanner struct {
	Cfg config.Config
	DB  *sql.DB
}

func New(cfg config.Config, db *sql.DB) *Scanner {
	return &Scanner{Cfg: cfg, DB: db}
}

func (s *Scanner) ScanMovies(ctx context.Context, jobID, libraryID int64) error {
	var root string
	if err := s.DB.QueryRowContext(ctx,
		"SELECT root_path FROM libraries WHERE id=? AND type='movies'",
		libraryID,
	).Scan(&root); err != nil {
		return fmt.Errorf("library %d not found or not movies: %w", libraryID, err)
	}

	cleanRoot := filepath.Clean(root)
	mediaRoot := filepath.Clean(s.Cfg.MediaRoot)
	if !strings.HasPrefix(cleanRoot+string(os.PathSeparator), mediaRoot+string(os.PathSeparator)) && cleanRoot != mediaRoot {
		return fmt.Errorf("root_path must be under %s (got %s)", mediaRoot, cleanRoot)
	}

	// Count the video files the ingest walk will actually process.
	totalFiles := tvscan.CountEligibleVideoFiles(cleanRoot)
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
			// Skip sample subdirectories, but only when nested — a top-level folder
			// of that name is a real library entry. Extras-type dirs (Trailers/
			// Featurettes/…) are walked: their files ingest as playable extras.
			if p != cleanRoot && tvscan.IsJunkDirName(d.Name()) {
				if rel, relErr := filepath.Rel(cleanRoot, p); relErr == nil && strings.ContainsRune(rel, filepath.Separator) {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !video.IsVideoExt(filepath.Ext(p)) {
			return nil
		}
		if tvscan.IsJunkFile(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))) {
			return nil
		}

		resolvedPath, err := pathguard.ResolveExistingUnderRoot(mediaRoot, p)
		if err != nil {
			slog.Warn("moviescan guard", "path", p, "err", err)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			slog.Warn("moviescan stat", "path", p, "err", err)
			return nil
		}
		fileSize := info.Size()
		mtimeUnix := info.ModTime().UTC().Unix()

		// Identify from the path. Pure parsing (no I/O), so it runs every scan —
		// including for unchanged files below — letting a re-scan reconverge titles
		// as the parsing improves. An extras-dir file is never title-parsed
		// (guessed_title stays '', keeping it out of matching/review); its display
		// title derives from the filename instead.
		var ident *MovieIdentity
		var extraTitle string
		extraCategory, isExtra := tvscan.ClassifyExtra(p, cleanRoot, false)
		if isExtra {
			extraTitle = tvscan.ExtraTitle(resolvedPath)
		} else {
			ident = ParseMovie(resolvedPath, cleanRoot)
		}

		// Unchanged-file fast path: skip the expensive probe but still refresh the
		// derived title/year (and extras classification — rows predating the
		// extras feature carry is_extra=0 until this runs).
		var existingID, existingSize, existingMtime int64
		err = s.DB.QueryRowContext(ctx,
			"SELECT id, file_size_bytes, mtime_unix FROM movie_files WHERE library_id=? AND abs_path=?",
			libraryID, resolvedPath,
		).Scan(&existingID, &existingSize, &existingMtime)
		if err == nil && existingSize == fileSize && existingMtime == mtimeUnix {
			if err := s.refreshIdentity(ctx, existingID, ident, extraCategory, extraTitle); err != nil {
				scanErrors++
				slog.Warn("moviescan identity refresh", "path", resolvedPath, "err", err)
			}
			processed++
			if processed%50 == 0 || processed == totalFiles {
				_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)
			}
			return nil
		}

		container := strings.TrimPrefix(strings.ToLower(filepath.Ext(resolvedPath)), ".")
		var streamInfoJSON string
		probeResult, probeErr := video.Probe(ctx, resolvedPath)
		if probeErr != nil {
			slog.Warn("moviescan probe", "path", resolvedPath, "err", probeErr)
			streamInfoJSON = "{}"
		} else {
			b, _ := json.Marshal(probeResult)
			streamInfoJSON = string(b)
		}

		if err := s.upsertMovieFile(ctx, libraryID, resolvedPath, container, fileSize, mtimeUnix, streamInfoJSON, ident, extraCategory, extraTitle); err != nil {
			scanErrors++
			slog.Warn("moviescan file error", "path", resolvedPath, "err", err)
		}

		processed++
		if processed%50 == 0 || processed == totalFiles {
			_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)
		}
		return nil
	}); err != nil {
		return err
	}

	_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)

	if scanErrors > 0 {
		slog.Warn("moviescan completed with errors", "library_id", libraryID, "files_scanned", processed, "errors", scanErrors)
	}

	if err := s.resetExtraMatches(ctx, libraryID); err != nil {
		return err
	}

	if err := s.relinkMovedFiles(ctx, libraryID, cleanRoot); err != nil {
		return err
	}
	return s.pruneMissingFiles(ctx, libraryID, cleanRoot)
}

// upsertMovieFile inserts or updates a movie_files row. The match columns
// (match_status/tmdb_id/confidence/source/matched_at) are deliberately NOT in the
// statement, so they default to unmatched on insert and are preserved untouched
// on conflict — only the matcher writes them. guessed_title/year are refreshed
// every scan so improved parsing reconverges. extraCategory != "" marks a
// playable extra (guessed_title is ” for those — see the walk).
func (s *Scanner) upsertMovieFile(ctx context.Context, libraryID int64, resolvedPath, container string, fileSize, mtimeUnix int64, streamInfoJSON string, ident *MovieIdentity, extraCategory, extraTitle string) error {
	title, year := identFields(ident)
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO movie_files (library_id, abs_path, container, file_size_bytes, mtime_unix, stream_info_json, guessed_title, year, is_extra, extra_title, extra_category)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(library_id, abs_path) DO UPDATE SET
  container=excluded.container,
  file_size_bytes=excluded.file_size_bytes,
  mtime_unix=excluded.mtime_unix,
  stream_info_json=excluded.stream_info_json,
  guessed_title=excluded.guessed_title,
  year=excluded.year,
  is_extra=excluded.is_extra,
  extra_title=excluded.extra_title,
  extra_category=excluded.extra_category,
  -- a changed file (new size or mtime) invalidates its integrity status so the
  -- next integrity_check re-examines (and re-repairs) it.
  integrity_status=CASE WHEN file_size_bytes<>excluded.file_size_bytes OR mtime_unix<>excluded.mtime_unix THEN '' ELSE integrity_status END,
  updated_at=datetime('now')
`, libraryID, resolvedPath, container, fileSize, mtimeUnix, streamInfoJSON, title, year, boolToInt(extraCategory != ""), extraTitle, extraCategory)
	if err != nil {
		return fmt.Errorf("upsert movie_files: %w", err)
	}
	return nil
}

// refreshIdentity updates only the derived title/year + extras classification
// for an unchanged file, so a re-scan picks up better parsing without a probe.
// Match columns are untouched.
func (s *Scanner) refreshIdentity(ctx context.Context, fileID int64, ident *MovieIdentity, extraCategory, extraTitle string) error {
	title, year := identFields(ident)
	if _, err := s.DB.ExecContext(ctx,
		"UPDATE movie_files SET guessed_title=?, year=?, is_extra=?, extra_title=?, extra_category=? WHERE id=?",
		title, year, boolToInt(extraCategory != ""), extraTitle, extraCategory, fileID,
	); err != nil {
		return fmt.Errorf("refresh movie identity: %w", err)
	}
	return nil
}

// resetExtraMatches blanks the match columns of every extras file. Ingest
// never title-parses an extra, but rows can predate their dir being recognized
// as an extras container (files scanned as unmatched noise, or even matched) —
// a blank match keeps them out of every title-level query (copies, counts,
// CW, mark-watched). One idempotent statement per scan.
func (s *Scanner) resetExtraMatches(ctx context.Context, libraryID int64) error {
	if _, err := s.DB.ExecContext(ctx, `
UPDATE movie_files SET tmdb_id=0, match_status='', match_confidence=0, match_source='', matched_at=''
WHERE library_id=? AND is_extra=1 AND (match_status<>'' OR tmdb_id<>0)
`, libraryID); err != nil {
		return fmt.Errorf("reset extra matches: %w", err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func identFields(ident *MovieIdentity) (string, int) {
	if ident == nil {
		return "", 0
	}
	return ident.Title, ident.Year
}

// relinkMovedFiles carries a moved/renamed file's match identity + playback
// progress onto its new row before prune deletes the orphan. "Same file" is
// (file_size_bytes, mtime_unix), which a plain mv preserves — no hashing of
// multi-GB video. Strictly 1:1 (one orphan + one survivor sharing a signature),
// so duplicate-content files are never mis-linked.
func (s *Scanner) relinkMovedFiles(ctx context.Context, libraryID int64, root string) error {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, abs_path, file_size_bytes, mtime_unix FROM movie_files WHERE library_id=?`, libraryID)
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
			slog.Warn("moviescan relink", "from", o.id, "to", cand[0], "err", err)
		}
	}
	return nil
}

// transferFileState copies a moved file's preserved state from the orphan
// (fromID) onto the new row (toID): the match identity (only when matched — an
// unmatched row re-derives from the new filename) and playback progress.
func (s *Scanner) transferFileState(ctx context.Context, fromID, toID int64) error {
	// One transaction so a failure on the second write doesn't leave the
	// identity transferred while the playback progress still hangs off the
	// about-to-be-pruned orphan (pruning cascade-deletes it, losing the
	// resume position the relink pass exists to preserve).
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transfer: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
UPDATE movie_files AS dst SET
  tmdb_id = src.tmdb_id,
  match_status = src.match_status,
  match_confidence = src.match_confidence,
  match_source = src.match_source,
  matched_at = src.matched_at
FROM movie_files AS src
WHERE dst.id = ? AND src.id = ? AND src.match_status IN ('matched', 'skipped')
`, toID, fromID); err != nil {
		return fmt.Errorf("transfer movie identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO movie_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at)
SELECT ?, position_seconds, duration_seconds, completed, updated_at
FROM movie_playback_progress WHERE file_id = ?
ON CONFLICT(file_id) DO UPDATE SET
  position_seconds = excluded.position_seconds,
  duration_seconds = excluded.duration_seconds,
  completed = excluded.completed,
  updated_at = excluded.updated_at
`, toID, fromID); err != nil {
		return fmt.Errorf("transfer movie playback progress: %w", err)
	}
	return tx.Commit()
}

func (s *Scanner) pruneMissingFiles(ctx context.Context, libraryID int64, root string) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, abs_path FROM movie_files WHERE library_id=?`, libraryID)
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
			slog.Warn("moviescan prune stat", "path", clean, "err", err)
			continue
		}
		staleIDs = append(staleIDs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(staleIDs) == 0 {
		return nil
	}
	// One transaction for the whole prune: N autocommit DELETEs are N fsyncs on
	// WAL; a single commit is one — the difference matters when a large move/delete
	// orphans thousands of rows.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range staleIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM movie_files WHERE id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

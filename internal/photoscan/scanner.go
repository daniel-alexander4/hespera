package photoscan

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
	"time"

	"hespera/internal/config"
	"hespera/internal/pathguard"
	"hespera/internal/video"
)

// Scanner walks a photos library — mixed still images and home-video clips —
// into the photos table. A strict simplification of moviescan: no identity
// parsing or matching at all (photos have no metadata provider); each file
// carries a capture timestamp (EXIF → probe creation_time → file mtime) that
// the By Date view orders on, plus its parent folder (the Folders grouping).
// Move-relink/prune are keyed on (file_size_bytes, mtime_unix) like the video
// scanners.
type Scanner struct {
	Cfg config.Config
	DB  *sql.DB
}

func New(cfg config.Config, db *sql.DB) *Scanner {
	return &Scanner{Cfg: cfg, DB: db}
}

// IsImageExt reports whether ext names a supported still-image format.
// HEIC/HEIF decode is best-effort (ffmpeg ≥7 for iPhone grid images) — the
// file still ingests and lists; only its thumbnail/rendition may degrade.
func IsImageExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".heic", ".heif", ".tif", ".tiff", ".bmp":
		return true
	default:
		return false
	}
}

// isClipExt reports whether ext names a home-video clip format. The shared
// video.IsVideoExt list plus camcorder/phone formats (AVCHD .mts, old .mpg,
// .3gp) that movie/TV libraries don't ingest but photo dumps are full of.
func isClipExt(ext string) bool {
	if video.IsVideoExt(ext) {
		return true
	}
	switch strings.ToLower(ext) {
	case ".mts", ".mpg", ".mpeg", ".3gp":
		return true
	default:
		return false
	}
}

// skipDirName reports directories a photo walk must not descend into: hidden
// dot-dirs and sidecar-thumbnail trees other software leaves in photo dumps
// (Synology's @eaDir being the classic).
func skipDirName(name string) bool {
	return strings.HasPrefix(name, ".") || strings.EqualFold(name, "@eaDir")
}

const takenAtLayout = "2006-01-02 15:04:05"

func (s *Scanner) ScanPhotos(ctx context.Context, jobID, libraryID int64) error {
	var root string
	if err := s.DB.QueryRowContext(ctx,
		"SELECT root_path FROM libraries WHERE id=? AND type='photos'",
		libraryID,
	).Scan(&root); err != nil {
		return fmt.Errorf("library %d not found or not photos: %w", libraryID, err)
	}

	cleanRoot := filepath.Clean(root)
	mediaRoot := filepath.Clean(s.Cfg.MediaRoot)
	if !strings.HasPrefix(cleanRoot+string(os.PathSeparator), mediaRoot+string(os.PathSeparator)) && cleanRoot != mediaRoot {
		return fmt.Errorf("root_path must be under %s (got %s)", mediaRoot, cleanRoot)
	}

	totalFiles := countEligibleFiles(cleanRoot)
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
			if p != cleanRoot && skipDirName(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(p)
		kind := ""
		switch {
		case IsImageExt(ext):
			kind = "photo"
		case isClipExt(ext):
			kind = "video"
		default:
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil // AppleDouble ._IMG etc.
		}

		resolvedPath, err := pathguard.ResolveExistingUnderRoot(mediaRoot, p)
		if err != nil {
			slog.Warn("photoscan guard", "path", p, "err", err)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			slog.Warn("photoscan stat", "path", p, "err", err)
			return nil
		}
		fileSize := info.Size()
		mtimeUnix := info.ModTime().UTC().Unix()

		// Unchanged-file fast path: nothing to re-derive (taken_at/orientation
		// come from the bytes, which didn't change).
		var existingID, existingSize, existingMtime int64
		err = s.DB.QueryRowContext(ctx,
			"SELECT id, file_size_bytes, mtime_unix FROM photos WHERE library_id=? AND abs_path=?",
			libraryID, resolvedPath,
		).Scan(&existingID, &existingSize, &existingMtime)
		if err == nil && existingSize == fileSize && existingMtime == mtimeUnix {
			processed++
			if processed%50 == 0 || processed == totalFiles {
				_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)
			}
			return nil
		}

		dirRel := "."
		if rel, relErr := filepath.Rel(cleanRoot, filepath.Dir(resolvedPath)); relErr == nil {
			dirRel = rel
		}

		takenAt, takenSource, orientation, streamInfoJSON := s.deriveFileFacts(ctx, resolvedPath, kind, info.ModTime())

		if err := s.upsertPhoto(ctx, libraryID, resolvedPath, kind, strings.TrimPrefix(strings.ToLower(ext), "."),
			fileSize, mtimeUnix, takenAt, takenSource, orientation, streamInfoJSON, dirRel); err != nil {
			scanErrors++
			slog.Warn("photoscan file error", "path", resolvedPath, "err", err)
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
		slog.Warn("photoscan completed with errors", "library_id", libraryID, "files_scanned", processed, "errors", scanErrors)
	}

	if err := s.relinkMovedFiles(ctx, libraryID, cleanRoot); err != nil {
		return err
	}
	return s.pruneMissingFiles(ctx, libraryID, cleanRoot)
}

// deriveFileFacts computes the capture timestamp (with its source), EXIF
// orientation, and — for clips — the ffprobe stream info. The timestamp
// cascade: EXIF DateTimeOriginal (photos) / container creation_time (clips)
// → file mtime. All wall-clock local: EXIF is naive local by definition, and
// a video's UTC creation_time converts to local so the By Date view sorts one
// consistent clock.
func (s *Scanner) deriveFileFacts(ctx context.Context, path, kind string, mtime time.Time) (takenAt, takenSource string, orientation int, streamInfoJSON string) {
	takenAt = mtime.Local().Format(takenAtLayout)
	takenSource = "mtime"
	streamInfoJSON = "{}"

	if kind == "photo" {
		if t, o := ReadEXIF(path); !t.IsZero() {
			takenAt, takenSource, orientation = t.Format(takenAtLayout), "exif", o
		} else {
			orientation = o
		}
		return
	}

	probeResult, probeErr := video.Probe(ctx, path)
	if probeErr != nil {
		slog.Warn("photoscan probe", "path", path, "err", probeErr)
		return
	}
	b, _ := json.Marshal(probeResult)
	streamInfoJSON = string(b)
	if ct := strings.TrimSpace(probeResult.Format.CreationTime); ct != "" {
		if t, err := time.Parse(time.RFC3339Nano, ct); err == nil {
			takenAt, takenSource = t.Local().Format(takenAtLayout), "probe"
		}
	}
	return
}

// upsertPhoto inserts or updates a photos row. thumb_path is preserved on an
// unchanged conflict and reset when the bytes changed, so the chained thumb
// job regenerates it.
func (s *Scanner) upsertPhoto(ctx context.Context, libraryID int64, resolvedPath, kind, container string,
	fileSize, mtimeUnix int64, takenAt, takenSource string, orientation int, streamInfoJSON, dirRel string) error {
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO photos (library_id, abs_path, kind, container, file_size_bytes, mtime_unix, taken_at, taken_source, orientation, stream_info_json, dir_rel)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(library_id, abs_path) DO UPDATE SET
  kind=excluded.kind,
  container=excluded.container,
  file_size_bytes=excluded.file_size_bytes,
  mtime_unix=excluded.mtime_unix,
  taken_at=excluded.taken_at,
  taken_source=excluded.taken_source,
  orientation=excluded.orientation,
  stream_info_json=excluded.stream_info_json,
  dir_rel=excluded.dir_rel,
  thumb_path=CASE WHEN file_size_bytes<>excluded.file_size_bytes OR mtime_unix<>excluded.mtime_unix THEN '' ELSE thumb_path END,
  updated_at=datetime('now')
`, libraryID, resolvedPath, kind, container, fileSize, mtimeUnix, takenAt, takenSource, orientation, streamInfoJSON, dirRel)
	if err != nil {
		return fmt.Errorf("upsert photos: %w", err)
	}
	return nil
}

// countEligibleFiles pre-counts what the walk will process, for honest job
// progress totals. Mirrors the walk's own filters exactly.
func countEligibleFiles(root string) int {
	count := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && skipDirName(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if ext := filepath.Ext(p); IsImageExt(ext) || isClipExt(ext) {
			count++
		}
		return nil
	})
	return count
}

// relinkMovedFiles carries a moved/renamed file's playback progress (clips)
// and generated thumbnail onto its new row before prune deletes the orphan.
// Same-file signature = (file_size_bytes, mtime_unix), strictly 1:1.
func (s *Scanner) relinkMovedFiles(ctx context.Context, libraryID int64, root string) error {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, abs_path, file_size_bytes, mtime_unix FROM photos WHERE library_id=?`, libraryID)
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
			continue
		}
		if err := s.transferFileState(ctx, o.id, cand[0]); err != nil {
			slog.Warn("photoscan relink", "from", o.id, "to", cand[0], "err", err)
		}
	}
	return nil
}

// transferFileState copies playback progress (clips) from the orphan onto the
// new row. Thumbnails are id-keyed, so they regenerate for the new row rather
// than transfer — cheap, and never stale.
func (s *Scanner) transferFileState(ctx context.Context, fromID, toID int64) error {
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO photo_playback_progress (file_id, position_seconds, duration_seconds, completed, updated_at)
SELECT ?, position_seconds, duration_seconds, completed, updated_at
FROM photo_playback_progress WHERE file_id = ?
ON CONFLICT(file_id) DO UPDATE SET
  position_seconds = excluded.position_seconds,
  duration_seconds = excluded.duration_seconds,
  completed = excluded.completed,
  updated_at = excluded.updated_at
`, toID, fromID)
	if err != nil {
		return fmt.Errorf("transfer photo playback progress: %w", err)
	}
	return nil
}

// pruneMissingFiles deletes rows whose file is gone, plus their generated
// thumbnail/rendition files (id-keyed under DataDir/thumbs/photos).
func (s *Scanner) pruneMissingFiles(ctx context.Context, libraryID int64, root string) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, abs_path FROM photos WHERE library_id=?`, libraryID)
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
			slog.Warn("photoscan prune stat", "path", clean, "err", err)
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
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range staleIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM photos WHERE id=?`, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Best-effort thumb cleanup after the rows are gone — a leftover file is
	// harmless (nothing references it) and unreferenced-by-construction.
	for _, id := range staleIDs {
		for _, name := range ThumbFileNames(id) {
			_ = os.Remove(filepath.Join(s.Cfg.DataDir, "thumbs", "photos", name))
		}
	}
	return nil
}

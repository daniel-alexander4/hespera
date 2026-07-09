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
	// ShouldYield, when set, is polled by the long thumbnail sweep: returning
	// true makes it stop early (jobs.ErrYielded) so a waiting interactive job
	// runs, then the sweep is re-enqueued to finish. nil = never yield.
	ShouldYield func() bool
}

func New(cfg config.Config, db *sql.DB) *Scanner {
	return &Scanner{Cfg: cfg, DB: db}
}

func (s *Scanner) ScanTV(ctx context.Context, jobID, libraryID int64) error {
	cleanRoot, _, err := s.tvLibraryRoot(ctx, libraryID)
	if err != nil {
		return err
	}
	return s.scanTVRoots(ctx, jobID, libraryID, []string{cleanRoot}, false)
}

// ScanTVDirs scans only the given directories (a series' show folder(s)) instead
// of the whole library — used by the per-series "scan for new episodes" button.
// Each dir is validated to exist and lie under the library root + media root; a
// vanished dir is dropped (never scanned, so it can't prune the series away), and
// if none remain it's an error rather than a silent no-op. Relink/prune are
// scoped per dir, so only rows under the scanned show folder(s) can be pruned.
func (s *Scanner) ScanTVDirs(ctx context.Context, jobID, libraryID int64, dirs []string) error {
	cleanRoot, mediaRoot, err := s.tvLibraryRoot(ctx, libraryID)
	if err != nil {
		return err
	}
	var valid []string
	for _, d := range dirs {
		cd := filepath.Clean(d)
		if !underRoot(cd, cleanRoot) || !underRoot(cd, mediaRoot) {
			slog.Warn("tvscan series dir out of scope", "dir", cd, "library_root", cleanRoot)
			continue
		}
		if fi, statErr := os.Stat(cd); statErr != nil || !fi.IsDir() {
			slog.Warn("tvscan series dir missing", "dir", cd, "err", statErr)
			continue
		}
		valid = append(valid, cd)
	}
	valid = dropNestedRoots(valid)
	if len(valid) == 0 {
		return fmt.Errorf("no scannable directories for series in library %d (folders missing?)", libraryID)
	}
	// rootIsTitle: each walked root is a show folder, so a first-level Extras/
	// is that show's extras container — on a library-root walk it would be a
	// real entry named "Extras".
	return s.scanTVRoots(ctx, jobID, libraryID, valid, true)
}

// tvLibraryRoot returns the cleaned library root + media root, validating the
// library is a tv library whose root is under the media root.
func (s *Scanner) tvLibraryRoot(ctx context.Context, libraryID int64) (cleanRoot, mediaRoot string, err error) {
	var root string
	if err = s.DB.QueryRowContext(ctx,
		"SELECT root_path FROM libraries WHERE id=? AND type='tv'", libraryID,
	).Scan(&root); err != nil {
		return "", "", fmt.Errorf("library %d not found or not tv: %w", libraryID, err)
	}
	cleanRoot = filepath.Clean(root)
	mediaRoot = filepath.Clean(s.Cfg.MediaRoot)
	if !underRoot(cleanRoot, mediaRoot) {
		return "", "", fmt.Errorf("root_path must be under %s (got %s)", mediaRoot, cleanRoot)
	}
	return cleanRoot, mediaRoot, nil
}

// scanTVRoots walks each root (pre-validated), ingests every video file, then
// CountEligibleVideoFiles walks root counting the video files a scan will
// actually ingest — the same junk-dir SkipDir and junk-file rules as the
// ingest walks, so a job's progress_total matches what gets processed
// (counting sample clips and nested Sample dirs inflated the total and the
// progress bar stalled short of 100%). Extras-dir files ARE counted — they
// ingest as playable extras. Shared by the TV and movie scanners.
func CountEligibleVideoFiles(root string) int {
	n := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			if p != root && IsJunkDirName(d.Name()) {
				if rel, relErr := filepath.Rel(root, p); relErr == nil && strings.ContainsRune(rel, filepath.Separator) {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !video.IsVideoExt(filepath.Ext(p)) {
			return nil
		}
		if IsJunkFile(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))) {
			return nil
		}
		n++
		return nil
	})
	return n
}

// runs the move-relink + prune passes scoped to each root. ScanTV passes the
// single library root (rootIsTitle=false); ScanTVDirs passes a series' show
// folder(s) (rootIsTitle=true — see ClassifyExtra).
func (s *Scanner) scanTVRoots(ctx context.Context, jobID, libraryID int64, roots []string, rootIsTitle bool) error {
	totalFiles := 0
	for _, r := range roots {
		totalFiles += CountEligibleVideoFiles(r)
	}
	if totalFiles > 0 {
		_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", totalFiles, jobID)
	}

	processed := 0
	scanErrors := 0
	for _, r := range roots {
		walkRoot := r
		if err := filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, walkErr error) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				// Skip sample subdirectories, but only when nested inside the walked
				// root — a top-level folder of that name is a real entry. Extras-type
				// dirs (Trailers/Featurettes/…) are walked: their files ingest as
				// playable extras (ClassifyExtra below).
				if p != walkRoot && IsJunkDirName(d.Name()) {
					if rel, relErr := filepath.Rel(walkRoot, p); relErr == nil && strings.ContainsRune(rel, filepath.Separator) {
						return fs.SkipDir
					}
				}
				return nil
			}
			if !video.IsVideoExt(filepath.Ext(p)) {
				return nil
			}
			// Skip sample clips by their release-tag token (never a real episode).
			if IsJunkFile(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))) {
				return nil
			}
			extraCategory, _ := ClassifyExtra(p, walkRoot, rootIsTitle)
			counted, ingestErr := s.ingestTVFile(ctx, libraryID, p, d, extraCategory)
			if ingestErr != nil {
				scanErrors++
				slog.Warn("tvscan file error", "path", p, "err", ingestErr)
			}
			if counted {
				processed++
				if processed%50 == 0 || processed == totalFiles {
					_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}

	_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)

	if scanErrors > 0 {
		slog.Warn("tvscan completed with errors", "library_id", libraryID, "files_scanned", processed, "errors", scanErrors)
	}

	// Prune safety: a walk that found nothing while the library has rows is far
	// more likely an unmounted/empty mount point than a deliberately emptied
	// library — pruning would delete every row (and the playback/match state
	// only rows carry). Skip the destructive tail; a rescan once the root has
	// content prunes normally, and deleting the library reaps everything.
	if processed == 0 {
		var rows int
		_ = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM tv_series_files WHERE library_id=?", libraryID).Scan(&rows)
		if rows > 0 {
			slog.Warn("tvscan: no files found but library has rows — root looks unmounted; skipping prune",
				"library_id", libraryID, "rows", rows)
			return nil
		}
	}

	if err := s.resetExtraIdentities(ctx, libraryID); err != nil {
		return err
	}

	for _, r := range roots {
		if err := s.relinkMovedFiles(ctx, libraryID, r); err != nil {
			return err
		}
		if err := s.pruneMissingFiles(ctx, libraryID, r); err != nil {
			return err
		}
	}
	return nil
}

// ingestTVFile probes (or fast-paths an unchanged file) and upserts a single
// video file. extraCategory != "" marks the file a playable extra: no episode
// identification (its identity row stays the blank placeholder, so it never
// enters matching), title derived from the filename. counted reports whether
// it was a real file we processed (so the caller advances progress); a
// pathguard/stat failure returns counted=false with no error (logged +
// skipped, as the library scan always did).
func (s *Scanner) ingestTVFile(ctx context.Context, libraryID int64, p string, d fs.DirEntry, extraCategory string) (counted bool, err error) {
	mediaRoot := filepath.Clean(s.Cfg.MediaRoot)
	resolvedPath, gErr := pathguard.ResolveExistingUnderRoot(mediaRoot, p)
	if gErr != nil {
		slog.Warn("tvscan guard", "path", p, "err", gErr)
		return false, nil
	}
	info, iErr := d.Info()
	if iErr != nil {
		slog.Warn("tvscan stat", "path", p, "err", iErr)
		return false, nil
	}
	fileSize := info.Size()
	mtimeUnix := info.ModTime().UTC().Unix()

	// Identify episode from filename — pure path parsing (no I/O), so it runs on
	// every scan, letting a re-scan reconverge identities as parsing improves.
	// Extras are never identified: their identity is "bonus content of the
	// folder they live in", not a parseable episode.
	var ident *EpisodeIdentity
	extra := extraFields{Category: extraCategory}
	if extraCategory == "" {
		ident = IdentifyFile(resolvedPath)
	} else {
		extra.Title = ExtraTitle(resolvedPath)
	}

	// Unchanged-file fast path: skip the probe but refresh the derived identity
	// (and, for extras, the derived flag/title — rows predating the extras
	// feature carry is_extra=0 until this runs).
	var existingID, existingSize, existingMtime int64
	if qErr := s.DB.QueryRowContext(ctx,
		"SELECT id, file_size_bytes, mtime_unix FROM tv_series_files WHERE library_id=? AND abs_path=?",
		libraryID, resolvedPath,
	).Scan(&existingID, &existingSize, &existingMtime); qErr == nil && existingSize == fileSize && existingMtime == mtimeUnix {
		if _, uErr := s.DB.ExecContext(ctx,
			"UPDATE tv_series_files SET is_extra=?, extra_title=?, extra_category=? WHERE id=? AND (is_extra<>? OR extra_title<>? OR extra_category<>?)",
			extra.isExtra(), extra.Title, extra.Category, existingID, extra.isExtra(), extra.Title, extra.Category,
		); uErr != nil {
			return true, uErr
		}
		// Unchanged file: a single identity refresh — no transaction needed.
		return true, s.upsertIdentity(ctx, s.DB, existingID, ident)
	}

	container := strings.TrimPrefix(strings.ToLower(filepath.Ext(resolvedPath)), ".")
	var streamInfoJSON string
	if probeResult, probeErr := video.Probe(ctx, resolvedPath); probeErr != nil {
		slog.Warn("tvscan probe", "path", resolvedPath, "err", probeErr)
		streamInfoJSON = "{}"
	} else {
		b, _ := json.Marshal(probeResult)
		streamInfoJSON = string(b)
	}
	return true, s.upsertTVFile(ctx, libraryID, resolvedPath, container, fileSize, mtimeUnix, streamInfoJSON, ident, extra)
}

// extraFields is a file's extras classification: zero-valued for a regular
// episode file, category+title for a file inside an extras directory.
type extraFields struct {
	Category string
	Title    string
}

func (e extraFields) isExtra() int {
	if e.Category != "" {
		return 1
	}
	return 0
}

// ShowDirsForFiles maps a series' file paths to the distinct show-folder(s) to
// scan: the season directory's parent when the file sits under a season dir
// (so a brand-new Season N/ is picked up), else the file's own directory (flat
// layout). Deduplicated; pure (no I/O). Scanning the show folder — rather than
// the individual season folders — is what lets a new season be discovered.
func ShowDirsForFiles(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		parent := filepath.Dir(p)
		showDir := parent
		if _, ok := ParseSeasonDir(filepath.Base(parent)); ok {
			showDir = filepath.Dir(parent) // grandparent = the show folder
		}
		if showDir == "." || showDir == string(filepath.Separator) {
			continue
		}
		if !seen[showDir] {
			seen[showDir] = true
			out = append(out, showDir)
		}
	}
	return out
}

// underRoot reports whether path is root or nested under it.
func underRoot(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

// dropNestedRoots removes any root that is already contained by another in the
// set, so a parent + child pair doesn't double-scan/double-prune.
func dropNestedRoots(roots []string) []string {
	var out []string
	for _, r := range roots {
		nested := false
		for _, other := range roots {
			if other != r && underRoot(r, other) {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, r)
		}
	}
	return out
}

// dbtx is the subset of *sql.DB / *sql.Tx the per-file upsert needs, so the same
// code runs either standalone or inside a transaction.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// upsertTVFile inserts or updates tv_series_files and tv_series_identities for a
// single file. Its two-to-three statements (the file upsert, an id lookup on the
// conflict path, and the identity upsert) run in one transaction so a scan commits
// once per file instead of per statement — the fsync savings dominate a re-scan,
// which skips the probe but still upserts every file. Returns an error if any DB
// operation fails; the caller logs and continues scanning (the tx rolls back).
func (s *Scanner) upsertTVFile(ctx context.Context, libraryID int64, resolvedPath, container string, fileSize, mtimeUnix int64, streamInfoJSON string, ident *EpisodeIdentity, extra extraFields) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
INSERT INTO tv_series_files (library_id, abs_path, container, file_size_bytes, mtime_unix, stream_info_json, is_extra, extra_title, extra_category)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(library_id, abs_path) DO UPDATE SET
  container=excluded.container,
  file_size_bytes=excluded.file_size_bytes,
  mtime_unix=excluded.mtime_unix,
  stream_info_json=excluded.stream_info_json,
  is_extra=excluded.is_extra,
  extra_title=excluded.extra_title,
  extra_category=excluded.extra_category,
  -- a changed file (new size or mtime) invalidates its integrity status so the
  -- next integrity_check re-examines (and re-repairs) it, and its episode
  -- thumbnail so the tv_thumb job regenerates the frame grab.
  integrity_status=CASE WHEN file_size_bytes<>excluded.file_size_bytes OR mtime_unix<>excluded.mtime_unix THEN '' ELSE integrity_status END,
  thumb_path=CASE WHEN file_size_bytes<>excluded.file_size_bytes OR mtime_unix<>excluded.mtime_unix THEN '' ELSE thumb_path END,
  updated_at=datetime('now')
`, libraryID, resolvedPath, container, fileSize, mtimeUnix, streamInfoJSON, extra.isExtra(), extra.Title, extra.Category)
	if err != nil {
		return fmt.Errorf("upsert tv_series_files: %w", err)
	}

	// Get the file ID.
	var fileID int64
	fileID, err = res.LastInsertId()
	if err != nil || fileID == 0 {
		// On conflict update, LastInsertId may be 0; query directly.
		if err := tx.QueryRowContext(ctx,
			"SELECT id FROM tv_series_files WHERE library_id=? AND abs_path=?",
			libraryID, resolvedPath,
		).Scan(&fileID); err != nil {
			return fmt.Errorf("get file id: %w", err)
		}
	}

	if err := s.upsertIdentity(ctx, tx, fileID, ident); err != nil {
		return err
	}
	return tx.Commit()
}

// upsertIdentity inserts or refreshes the tv_series_identities row for a file.
// The status guard means a re-scan only overwrites 'unmatched' rows — matched
// and user-skipped rows keep their data. A nil identity never clobbers an
// existing row. Safe to call on every scan, including for unchanged files.
func (s *Scanner) upsertIdentity(ctx context.Context, exec dbtx, fileID int64, ident *EpisodeIdentity) error {
	if ident != nil {
		epCSV := episodeNumbersCSV(ident.EpisodeNumbers)
		_, err := exec.ExecContext(ctx, `
INSERT INTO tv_series_identities (file_id, status, guessed_title, season_number, episode_numbers_csv, match_confidence, match_method, air_date, year)
VALUES (?, 'unmatched', ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(file_id) DO UPDATE SET
  guessed_title=excluded.guessed_title,
  season_number=excluded.season_number,
  episode_numbers_csv=excluded.episode_numbers_csv,
  match_confidence=excluded.match_confidence,
  match_method=excluded.match_method,
  air_date=excluded.air_date,
  year=excluded.year
WHERE status NOT IN ('matched', 'skipped')
`, fileID, ident.ShowTitle, ident.SeasonNumber, epCSV, ident.Confidence, ident.Method, ident.AirDate, ident.Year)
		if err != nil {
			return fmt.Errorf("upsert tv_series_identities: %w", err)
		}
		return nil
	}
	_, err := exec.ExecContext(ctx, `
INSERT INTO tv_series_identities (file_id, status, guessed_title, season_number, episode_numbers_csv, match_confidence, match_method)
VALUES (?, 'unmatched', '', -1, '', 0.0, '')
ON CONFLICT(file_id) DO NOTHING
`, fileID)
	if err != nil {
		return fmt.Errorf("upsert tv_series_identities fallback: %w", err)
	}
	return nil
}

// resetExtraIdentities blanks the identity row of every extras file back to
// the unmatched placeholder. Ingest never writes a real identity for an extra,
// but rows can predate their dir being recognized as an extras container (e.g.
// "Behind The Scenes/" files scanned as unmatched noise, or even hand-matched)
// — an empty guessed_title keeps them out of the matcher/review, and a
// non-matched status keeps them out of every episode/count/CW query. One
// idempotent statement per scan.
func (s *Scanner) resetExtraIdentities(ctx context.Context, libraryID int64) error {
	if _, err := s.DB.ExecContext(ctx, `
UPDATE tv_series_identities SET
  status='unmatched', provider='', series_id='', guessed_title='', season_number=-1,
  episode_numbers_csv='', match_confidence=0, match_method='', matched_at='', air_date='', year=0
WHERE file_id IN (SELECT id FROM tv_series_files WHERE library_id=? AND is_extra=1)
  AND (status<>'unmatched' OR guessed_title<>'' OR series_id<>'')
`, libraryID); err != nil {
		return fmt.Errorf("reset extra identities: %w", err)
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
  air_date = src.air_date,
  year = src.year
FROM tv_series_identities AS src
WHERE dst.file_id = ? AND src.file_id = ? AND src.status IN ('matched', 'skipped')
`, toID, fromID); err != nil {
		return fmt.Errorf("transfer identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
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
	return tx.Commit()
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
		if _, err := tx.ExecContext(ctx, `DELETE FROM tv_series_files WHERE id=?`, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Best-effort episode-thumb cleanup after the rows are gone — a leftover
	// file is harmless (nothing references it) and unreferenced-by-construction.
	for _, id := range staleIDs {
		for _, rel := range EpisodeThumbRelPaths(id) {
			_ = os.Remove(filepath.Join(s.episodeThumbsDir(), rel))
		}
	}
	return nil
}

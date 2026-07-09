package scan

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"hespera/internal/config"
	"hespera/internal/fsutil"
	"hespera/internal/music"
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

func (s *Scanner) ScanMusic(ctx context.Context, jobID, libraryID int64) error {
	var root string
	if err := s.DB.QueryRowContext(ctx,
		"SELECT root_path FROM libraries WHERE id=? AND type='music'",
		libraryID,
	).Scan(&root); err != nil {
		return fmt.Errorf("library %d not found or not music: %w", libraryID, err)
	}

	cleanRoot := filepath.Clean(root)
	mediaRoot := filepath.Clean(s.Cfg.MediaRoot)
	if !strings.HasPrefix(cleanRoot+string(os.PathSeparator), mediaRoot+string(os.PathSeparator)) && cleanRoot != mediaRoot {
		return fmt.Errorf("root_path must be under %s (got %s)", mediaRoot, cleanRoot)
	}

	thumbDir := filepath.Join(s.Cfg.DataDir, "thumbs", "music")
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		return err
	}

	// Enumerate audio files in a single walk, then process them. One traversal
	// gives an accurate progress total without a second walk, and the
	// enumeration itself honors cancellation.
	var audioPaths []string
	if err := filepath.WalkDir(cleanRoot, func(p string, d fs.DirEntry, walkErr error) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !music.IsAudioExt(filepath.Ext(p)) {
			return nil
		}
		audioPaths = append(audioPaths, p)
		return nil
	}); err != nil {
		return err
	}

	totalFiles := len(audioPaths)
	if totalFiles > 0 {
		_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", totalFiles, jobID)
	}

	processed := 0
	scanErrors := 0
	for _, p := range audioPaths {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := s.scanFile(ctx, libraryID, p, thumbDir, true); err != nil {
			scanErrors++
			slog.Warn("scan file error", "path", p, "err", err)
			// Continue scanning -- one file's error should not abort the library scan.
		}

		processed++
		if processed%50 == 0 || processed == totalFiles {
			_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)
		}
	}

	// Final progress update.
	_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)

	if scanErrors > 0 {
		slog.Warn("scan completed with errors", "library_id", libraryID, "files_scanned", processed, "errors", scanErrors)
	}

	// Prune safety: a walk that found nothing while the library has rows is far
	// more likely an unmounted/empty mount point than a deliberately emptied
	// library — pruning would delete every row (and the playback/match state
	// only rows carry). Skip the destructive tail; a rescan once the root has
	// content prunes normally, and deleting the library reaps everything.
	if processed == 0 {
		var rows int
		_ = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM music_tracks WHERE library_id=?", libraryID).Scan(&rows)
		if rows > 0 {
			slog.Warn("scan: no files found but library has rows — root looks unmounted; skipping prune",
				"library_id", libraryID, "rows", rows)
			return nil
		}
	}

	// Post-scan: detect compilations and merge variants (order-independent).
	if err := s.finalizeCompilations(ctx, libraryID); err != nil {
		return err
	}

	if err := s.relinkMovedTracks(ctx, libraryID, cleanRoot); err != nil {
		return err
	}
	if err := s.pruneMissingTracks(ctx, libraryID, cleanRoot); err != nil {
		return err
	}
	return s.cleanupEmptyAlbums(ctx, libraryID)
}

// ScanFile processes a single audio file: reads tags, resolves artist/album, upserts track, extracts art.
// Always the full read — the targeted-rescan callers (the album Rescan button,
// the tag editor via ScanFiles) exist precisely to re-process files whose bytes
// haven't changed (re-extract art, apply just-written tags), so they must never
// take the unchanged fast-path the library walk uses.
func (s *Scanner) ScanFile(ctx context.Context, libraryID int64, absPath string, thumbDir string) error {
	return s.scanFile(ctx, libraryID, absPath, thumbDir, false)
}

func (s *Scanner) scanFile(ctx context.Context, libraryID int64, absPath string, thumbDir string, skipUnchanged bool) error {
	mediaRoot := filepath.Clean(s.Cfg.MediaRoot)
	resolvedPath, err := pathguard.ResolveExistingUnderRoot(mediaRoot, absPath)
	if err != nil {
		slog.Warn("scan guard", "path", absPath, "err", err)
		return nil
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		slog.Warn("scan stat", "path", resolvedPath, "err", err)
		return nil
	}
	fileSize := info.Size()
	mtimeUnix := info.ModTime().UTC().Unix()

	// Unchanged-file fast path (library walks only): a known row with the same
	// size+mtime needs no work at all — everything a music row carries derives
	// from the file's tags, which haven't changed. This skips the expensive
	// per-file cost (open + parse tags incl. embedded-art frames, plus a write
	// transaction) that made a no-op rescan crawl through every file. The
	// video scanners' equivalent skips the probe; unlike TV there is no cheap
	// filename-derived identity to refresh, so a tag-NORMALIZATION improvement
	// doesn't reconverge on unchanged files — the album Rescan button / tag
	// editor (full-read ScanFile) are the targeted escape hatches.
	if skipUnchanged {
		var existingSize, existingMtime int64
		if qErr := s.DB.QueryRowContext(ctx,
			"SELECT file_size_bytes, mtime_unix FROM music_tracks WHERE library_id=? AND abs_path=?",
			libraryID, resolvedPath,
		).Scan(&existingSize, &existingMtime); qErr == nil && existingSize == fileSize && existingMtime == mtimeUnix {
			return nil
		}
	}

	meta, err := music.ReadTrackMeta(resolvedPath)
	if err != nil {
		fallback, ferr := recoverTrackMeta(ctx, resolvedPath)
		if ferr != nil {
			slog.Warn("scan read meta", "path", resolvedPath, "err", err, "fallback_err", ferr)
			return nil
		}
		slog.Warn("scan read meta recovered via ffprobe", "path", resolvedPath, "err", err)
		meta = fallback
	}

	checksumSHA, err := s.resolveTrackChecksum(ctx, libraryID, resolvedPath, fileSize, mtimeUnix)
	if err != nil {
		slog.Warn("scan checksum", "path", resolvedPath, "err", err)
		return nil
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	artistID, err := ensureArtist(ctx, tx, libraryID, meta.Artist)
	if err != nil {
		_ = tx.Rollback()
		return err
	}

	// Use only tag-embedded compilation signals during per-file scan.
	// Heuristic detection (artist diversity) runs in post-scan finalizeCompilations.
	isCompilation := meta.IsCompilation || strings.EqualFold(strings.TrimSpace(meta.AlbumArtist), "Various Artists")
	if meta.ExplicitNotCompilation {
		isCompilation = false
	}

	albumArtistName := strings.TrimSpace(meta.AlbumArtist)
	if albumArtistName == "" {
		albumArtistName = meta.Artist
	}
	if isCompilation && (albumArtistName == "" || strings.EqualFold(albumArtistName, meta.Artist)) {
		albumArtistName = "Various Artists"
	}

	albumArtistID, albumID, err := ensureAlbum(ctx, tx, libraryID, albumArtistName, meta.Album, meta.Year, isCompilation)
	if err != nil {
		_ = tx.Rollback()
		return err
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO music_tracks (library_id, artist_id, album_id, title, track_no, disc_no, abs_path, mime_type, file_size_bytes, mtime_unix, checksum_sha256)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(library_id, abs_path) DO UPDATE SET
  artist_id=excluded.artist_id,
  album_id=excluded.album_id,
  title=excluded.title,
  track_no=excluded.track_no,
  disc_no=excluded.disc_no,
  mime_type=excluded.mime_type,
  file_size_bytes=excluded.file_size_bytes,
  mtime_unix=excluded.mtime_unix,
  checksum_sha256=excluded.checksum_sha256,
  -- a changed file (new size or mtime) invalidates its integrity status and
  -- loudness so the chained integrity_check / music_loudness jobs re-examine it.
  integrity_status=CASE WHEN file_size_bytes<>excluded.file_size_bytes OR mtime_unix<>excluded.mtime_unix THEN '' ELSE integrity_status END,
  loudness_lufs=CASE WHEN file_size_bytes<>excluded.file_size_bytes OR mtime_unix<>excluded.mtime_unix THEN 0 ELSE loudness_lufs END
`, libraryID, artistID, albumID, meta.Title, meta.Track, meta.Disc, resolvedPath, meta.MIMEType, fileSize, mtimeUnix, checksumSHA)
	if err != nil {
		_ = tx.Rollback()
		return err
	}

	if meta.HasArt {
		var existing string
		if err := tx.QueryRowContext(ctx,
			"SELECT art_path FROM music_albums WHERE id=?", albumID,
		).Scan(&existing); err == nil {
			if strings.TrimSpace(existing) == "" {
				if err := saveEmbeddedArt(ctx, tx, thumbDir, libraryID, albumArtistID, albumID, meta); err != nil {
					slog.Warn("save art", "err", err)
				}
			}
		}
	}

	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return err
	}
	return nil
}

// ScanFiles rescans specific files and cleans up empty albums/artists.
// Used after tag edits to sync DB with file metadata. Synchronous, not a background job.
func (s *Scanner) ScanFiles(ctx context.Context, libraryID int64, absPaths []string) error {
	thumbDir := filepath.Join(s.Cfg.DataDir, "thumbs", "music")
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		return err
	}
	for _, p := range absPaths {
		if err := s.ScanFile(ctx, libraryID, p, thumbDir); err != nil {
			return err
		}
	}
	if err := s.finalizeCompilations(ctx, libraryID); err != nil {
		return err
	}
	return s.cleanupEmptyAlbums(ctx, libraryID)
}

// --- Database helpers ---

func ensureArtist(ctx context.Context, tx *sql.Tx, libraryID int64, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Unknown Artist"
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO music_artists (library_id, name)
VALUES (?, ?)
ON CONFLICT(library_id, name) DO NOTHING
`, libraryID, name); err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `
SELECT id FROM music_artists WHERE library_id=? AND name=?
`, libraryID, name).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func ensureAlbum(ctx context.Context, tx *sql.Tx, libraryID int64, artistName, albumTitle string, year int, isCompilation bool) (int64, int64, error) {
	artistName = strings.TrimSpace(artistName)
	albumTitle = strings.TrimSpace(albumTitle)
	if artistName == "" {
		artistName = "Unknown Artist"
	}
	if albumTitle == "" {
		albumTitle = "Unknown Album"
	}

	artistID, err := ensureArtist(ctx, tx, libraryID, artistName)
	if err != nil {
		return 0, 0, err
	}

	comp := 0
	if isCompilation {
		comp = 1
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year, is_compilation)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(library_id, artist_id, title, year) DO UPDATE SET
  album_artist_id=excluded.album_artist_id,
  is_compilation=excluded.is_compilation
`, libraryID, artistID, artistID, albumTitle, year, comp); err != nil {
		return 0, 0, err
	}

	var albumID int64
	if err := tx.QueryRowContext(ctx, `
SELECT id FROM music_albums WHERE library_id=? AND artist_id=? AND title=? AND year=?
`, libraryID, artistID, albumTitle, year).Scan(&albumID); err != nil {
		return 0, 0, err
	}
	return artistID, albumID, nil
}

// finalizeCompilations detects multi-artist albums and consolidates variant album records.
// Runs after all files are scanned so artist diversity is computed from the full track set,
// making results independent of filesystem walk order (BUG-02 fix).
func (s *Scanner) finalizeCompilations(ctx context.Context, libraryID int64) error {
	// Run the whole consolidation in one transaction so an interrupted run
	// rolls back to the clean pre-finalize state rather than leaving a
	// half-merged library (some albums marked compilation and reparented,
	// others not). There is no file or network I/O in the loop, so the write
	// transaction is brief.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Find albums with tracks from multiple artists that aren't already marked as compilations.
	rows, err := tx.QueryContext(ctx, `
SELECT al.id, al.title, al.year
FROM music_albums al
WHERE al.library_id = ?
  AND al.is_compilation = 0
  AND (SELECT COUNT(DISTINCT t.artist_id) FROM music_tracks t WHERE t.album_id = al.id) > 1
`, libraryID)
	if err != nil {
		return fmt.Errorf("find multi-artist albums: %w", err)
	}

	type albumInfo struct {
		id    int64
		title string
		year  int
	}
	var candidates []albumInfo
	for rows.Next() {
		var a albumInfo
		if err := rows.Scan(&a.id, &a.title, &a.year); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	// Close before issuing writes: the transaction holds a single connection.
	rows.Close()

	for _, a := range candidates {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Per-artist track distribution for this album. A zero total means the album's
		// tracks were already merged into another variant earlier in this loop. A single
		// artist holding a strict majority means the multi-artist signal is mis-tagged or
		// outlier tracks (e.g. an album of untagged "Unknown Artist" tracks plus one stray
		// from another folder), not a genuine various-artists compilation -- leave it under
		// its own artist. A true compilation has no dominant artist.
		artistRows, err := tx.QueryContext(ctx,
			"SELECT COUNT(*) FROM music_tracks WHERE album_id = ? GROUP BY artist_id", a.id)
		if err != nil {
			return err
		}
		total, maxByArtist := 0, 0
		for artistRows.Next() {
			var cnt int
			if err := artistRows.Scan(&cnt); err != nil {
				artistRows.Close()
				return err
			}
			total += cnt
			if cnt > maxByArtist {
				maxByArtist = cnt
			}
		}
		if err := artistRows.Err(); err != nil {
			artistRows.Close()
			return err
		}
		artistRows.Close()
		if total == 0 || maxByArtist*2 > total {
			continue
		}

		vaID, err := ensureArtist(ctx, tx, libraryID, "Various Artists")
		if err != nil {
			return err
		}

		// music_albums has UNIQUE(library_id, artist_id, title, year), so at most one
		// Various Artists album can exist for this title+year. If one already does,
		// reparenting this candidate to VA would collide on that key. Resolve a
		// collision-free canonical target: reuse the existing VA album when it holds
		// tracks, or drop it when it is an empty orphan (a stale shell left by a prior
		// promotion whose tracks later moved) so the candidate -- which carries the
		// match/art -- is promoted in its place.
		target := a.id
		var existingVAID int64
		var existingVATracks int
		err = tx.QueryRowContext(ctx, `
SELECT al.id, (SELECT COUNT(*) FROM music_tracks t WHERE t.album_id = al.id)
FROM music_albums al
WHERE al.library_id = ? AND al.artist_id = ? AND lower(al.title) = lower(?) AND al.year = ? AND al.id <> ?
LIMIT 1
`, libraryID, vaID, strings.TrimSpace(a.title), a.year, a.id).Scan(&existingVAID, &existingVATracks)
		switch {
		case err == sql.ErrNoRows:
			// No conflicting VA album -- promote the candidate below.
		case err != nil:
			return err
		case existingVATracks > 0:
			target = existingVAID
		default:
			// Empty orphan VA shell -- delete it so the candidate can be promoted.
			if _, err := tx.ExecContext(ctx, "DELETE FROM music_albums WHERE id = ?", existingVAID); err != nil {
				return err
			}
		}

		if target == a.id {
			if _, err := tx.ExecContext(ctx, `
UPDATE music_albums SET is_compilation = 1, artist_id = ?, album_artist_id = ? WHERE id = ?
`, vaID, vaID, a.id); err != nil {
				return fmt.Errorf("mark compilation: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx, `
UPDATE music_albums SET is_compilation = 1, album_artist_id = ? WHERE id = ?
`, vaID, target); err != nil {
			return fmt.Errorf("mark compilation: %w", err)
		}

		// Merge other album records with the same title+year onto the target
		// (BUG-03 fix) -- but only co-located ones. The variants this merge
		// consolidates are per-artist fragment rows of one physical album
		// folder (tracks whose missing album-artist tag keyed them under
		// their own track artist), so their files live in the compilation's
		// own directory. A genuinely distinct album that merely shares the
		// title+year (an artist's own "Live" next to a VA "Live"
		// compilation) lives elsewhere on disk and must not be absorbed.
		// Safe to run now because all tracks are already inserted -- no more
		// files will create new variants.
		refIDs := []int64{a.id}
		if target != a.id {
			refIDs = append(refIDs, target)
		}
		refDirs, err := albumTrackDirs(ctx, tx, refIDs)
		if err != nil {
			return fmt.Errorf("merge album variants: %w", err)
		}
		sibRows, err := tx.QueryContext(ctx, `
SELECT id FROM music_albums
WHERE library_id = ? AND lower(title) = lower(?) AND year = ? AND id NOT IN (?, ?)
`, libraryID, strings.TrimSpace(a.title), a.year, a.id, target)
		if err != nil {
			return fmt.Errorf("merge album variants: %w", err)
		}
		var siblings []int64
		for sibRows.Next() {
			var id int64
			if err := sibRows.Scan(&id); err != nil {
				sibRows.Close()
				return err
			}
			siblings = append(siblings, id)
		}
		if err := sibRows.Err(); err != nil {
			sibRows.Close()
			return err
		}
		sibRows.Close()

		mergeIDs := make([]int64, 0, len(siblings)+1)
		if target != a.id {
			// The candidate itself is definitionally part of the compilation.
			mergeIDs = append(mergeIDs, a.id)
		}
		for _, sib := range siblings {
			sibDirs, err := albumTrackDirs(ctx, tx, []int64{sib})
			if err != nil {
				return fmt.Errorf("merge album variants: %w", err)
			}
			for d := range sibDirs {
				if refDirs[d] {
					mergeIDs = append(mergeIDs, sib)
					break
				}
			}
		}
		if len(mergeIDs) > 0 {
			ph := strings.TrimSuffix(strings.Repeat("?,", len(mergeIDs)), ",")
			args := make([]any, 0, len(mergeIDs)+1)
			args = append(args, target)
			for _, id := range mergeIDs {
				args = append(args, id)
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE music_tracks SET album_id = ? WHERE album_id IN ("+ph+")", args...); err != nil {
				return fmt.Errorf("merge album variants: %w", err)
			}
		}
	}

	return tx.Commit()
}

// discDirPattern matches disc-split subfolders so a multi-part album's tracks
// normalize to one shared directory key: the numbered disc synonyms (Disc 1,
// CD2, Disk 3, Vol 1, Vol. 2, Volume 3, Part 1, Pt. 2) plus vinyl sides
// (Side A, Side 1). Deliberately nothing looser — an arbitrary subfolder name
// (per-artist dirs, bonus/, Extras) must NOT collapse, because folding those
// re-opens the over-merge the co-location rule exists to prevent; the layout
// matrix in variant_layout_test.go pins both directions.
var discDirPattern = regexp.MustCompile(`(?i)^(?:(?:cd|dis[ck]|vol(?:ume)?\.?|pt\.?|part)\s*\d+|side\s*(?:\d+|[a-d]))$`)

// albumDirKey is the directory an album physically lives in, derived from one
// of its track paths: the file's directory, with a disc subfolder collapsed
// onto its parent.
func albumDirKey(absPath string) string {
	dir := filepath.Dir(absPath)
	if discDirPattern.MatchString(filepath.Base(dir)) {
		dir = filepath.Dir(dir)
	}
	return dir
}

// albumTrackDirs returns the set of albumDirKey values across all tracks of
// the given albums.
func albumTrackDirs(ctx context.Context, tx *sql.Tx, albumIDs []int64) (map[string]bool, error) {
	dirs := make(map[string]bool)
	for _, id := range albumIDs {
		rows, err := tx.QueryContext(ctx, "SELECT abs_path FROM music_tracks WHERE album_id = ?", id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, err
			}
			dirs[albumDirKey(p)] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return dirs, nil
}

// --- Checksum ---

func (s *Scanner) resolveTrackChecksum(ctx context.Context, libraryID int64, absPath string, fileSize int64, mtimeUnix int64) (string, error) {
	var existingChecksum string
	var existingSize int64
	var existingMTime int64
	err := s.DB.QueryRowContext(ctx, `
SELECT checksum_sha256, file_size_bytes, mtime_unix
FROM music_tracks
WHERE library_id=? AND abs_path=?
`, libraryID, absPath).Scan(&existingChecksum, &existingSize, &existingMTime)
	if err == nil {
		if existingChecksum != "" && existingSize == fileSize && existingMTime == mtimeUnix {
			return existingChecksum, nil
		}
	} else if err != sql.ErrNoRows {
		return "", err
	}
	return checksumSHA256(absPath)
}

func checksumSHA256(path string) (string, error) {
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

// --- Art ---

// recoverTrackMeta is the last-resort fallback when music.ReadTrackMeta fails
// outright — dhowden/tag aborts the whole parse on a single malformed tag,
// which would otherwise drop the entire track (not just its art). MP3 already
// has a pure-Go ID3v2 fallback inside ReadTrackMeta; this covers the other
// containers (FLAC/M4A/OGG/...) by recovering the tag dictionary and embedded
// cover via ffprobe/ffmpeg. It only spawns a process on that failure path, so
// the happy path stays pure-Go.
func recoverTrackMeta(ctx context.Context, path string) (music.TrackMeta, error) {
	if strings.EqualFold(filepath.Ext(path), ".mp3") {
		// MP3's pure-Go fallback already ran inside ReadTrackMeta; if that
		// failed too there is nothing ffprobe would add.
		return music.TrackMeta{}, errors.New("no ffprobe fallback for mp3")
	}
	tags, hasArt, err := video.ProbeTags(ctx, path)
	if err != nil {
		return music.TrackMeta{}, err
	}
	meta := music.TrackMetaFromTags(tags, path)
	if hasArt {
		if data, aerr := video.ExtractCoverArt(ctx, path); aerr == nil {
			meta.SetArt("", data)
		} else if aerr != nil {
			slog.Warn("scan recover cover", "path", path, "err", aerr)
		}
	}
	return meta, nil
}

func saveEmbeddedArt(ctx context.Context, tx *sql.Tx, thumbDir string, libraryID, artistID, albumID int64, meta music.TrackMeta) error {
	if err := music.VerifyImage(meta.ArtMIME, meta.ArtBytes); err != nil {
		return err
	}
	ext, err := music.ArtFileExt(meta.ArtMIME)
	if err != nil {
		return err
	}
	h := sha1.Sum([]byte(fmt.Sprintf("lib=%d artist=%d album=%d", libraryID, artistID, albumID)))
	name := hex.EncodeToString(h[:]) + ext
	outPath := filepath.Join(thumbDir, name)

	// Atomic writes below mean any file already at outPath is complete (never a
	// truncated partial), so reusing it without a rewrite is safe.
	if _, err := os.Stat(outPath); err == nil {
		_, _ = tx.ExecContext(ctx, "UPDATE music_albums SET art_path=? WHERE id=?", outPath, albumID)
		return nil
	}

	if err := fsutil.WriteFileAtomic(outPath, meta.ArtBytes, 0o644); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE music_albums SET art_path=? WHERE id=?", outPath, albumID)
	return err
}

// --- Cleanup ---

// relinkMovedTracks detects tracks moved or renamed to a new path (same content)
// and carries their irreplaceable per-track state — play history (and the lyrics
// cache) — onto the new row before pruneMissingTracks deletes the orphaned old
// row. "Same file" is recognized by (file_size_bytes, checksum_sha256); a byte
// checksum match guarantees identical tags, so the new row's album/artist
// grouping equals the old one's and only the track id needs re-pointing. A
// transfer happens only when exactly one orphan and exactly one surviving row
// share a signature, so duplicate-content files are never mis-linked.
func (s *Scanner) relinkMovedTracks(ctx context.Context, libraryID int64, root string) error {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, abs_path, file_size_bytes, checksum_sha256 FROM music_tracks WHERE library_id=?`, libraryID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type sig struct {
		size     int64
		checksum string
	}
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
		var id, size int64
		var absPath, checksum string
		if err := rows.Scan(&id, &absPath, &size, &checksum); err != nil {
			return err
		}
		if checksum == "" {
			continue // no content signature to match on
		}
		clean := filepath.Clean(absPath)
		if clean != cleanRoot && !strings.HasPrefix(clean, rootPrefix) {
			continue
		}
		k := sig{size, checksum}
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
		if err := s.transferTrackState(ctx, o.id, cand[0]); err != nil {
			slog.Warn("scan relink", "from", o.id, "to", cand[0], "err", err)
		}
	}
	return nil
}

// transferTrackState re-points a moved track's play history, lyrics cache, and
// playlist membership from the orphaned old row (fromID) onto the new row
// (toID). The orphan itself is deleted afterwards by pruneMissingTracks.
func (s *Scanner) transferTrackState(ctx context.Context, fromID, toID int64) error {
	// One transaction so a failure on a later update doesn't leave play_history
	// re-pointed while the rest still references the about-to-be-pruned orphan.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transfer: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE play_history SET track_id=? WHERE track_id=?`, toID, fromID); err != nil {
		return fmt.Errorf("transfer play_history: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE lyrics_cache SET track_id=? WHERE track_id=?`, toID, fromID); err != nil {
		return fmt.Errorf("transfer lyrics_cache: %w", err)
	}
	// OR IGNORE: if a playlist somehow already contains the new row, the PK
	// (playlist_id, track_id) blocks the re-point — drop the orphan's entry
	// instead of failing (prune would cascade it anyway).
	if _, err := tx.ExecContext(ctx,
		`UPDATE OR IGNORE playlist_tracks SET track_id=? WHERE track_id=?`, toID, fromID); err != nil {
		return fmt.Errorf("transfer playlist_tracks: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM playlist_tracks WHERE track_id=?`, fromID); err != nil {
		return fmt.Errorf("clear orphan playlist_tracks: %w", err)
	}
	return tx.Commit()
}

func (s *Scanner) pruneMissingTracks(ctx context.Context, libraryID int64, root string) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, abs_path FROM music_tracks WHERE library_id=?`, libraryID)
	if err != nil {
		return err
	}
	defer rows.Close()

	cleanRoot := filepath.Clean(root)
	rootPrefix := cleanRoot + string(os.PathSeparator)
	staleIDs := make([]int64, 0, 64)
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
			slog.Warn("prune stat", "path", clean, "err", err)
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
		if _, err := tx.ExecContext(ctx, `DELETE FROM music_tracks WHERE id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Scanner) cleanupEmptyAlbums(ctx context.Context, libraryID int64) error {
	if _, err := s.DB.ExecContext(ctx, `
DELETE FROM music_albums
WHERE library_id = ?
  AND NOT EXISTS (
    SELECT 1 FROM music_tracks t WHERE t.album_id = music_albums.id
  )
`, libraryID); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `
DELETE FROM music_artists
WHERE library_id = ?
  AND NOT EXISTS (
    SELECT 1 FROM music_albums al
    WHERE al.artist_id = music_artists.id
       OR al.album_artist_id = music_artists.id
  )
  AND NOT EXISTS (
    SELECT 1 FROM music_tracks t WHERE t.artist_id = music_artists.id
  )
`, libraryID)
	return err
}

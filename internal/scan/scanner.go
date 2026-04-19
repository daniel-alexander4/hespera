package scan

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"isomedia/internal/config"
	"isomedia/internal/music"
	"isomedia/internal/pathguard"
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

	// Count files for progress.
	totalFiles := 0
	_ = filepath.WalkDir(cleanRoot, func(_ string, d fs.DirEntry, _ error) error {
		if d != nil && !d.IsDir() && music.IsAudioExt(filepath.Ext(d.Name())) {
			totalFiles++
		}
		return nil
	})
	if totalFiles > 0 {
		_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", totalFiles, jobID)
	}

	processed := 0
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
		if !music.IsAudioExt(filepath.Ext(p)) {
			return nil
		}

		if err := s.ScanFile(ctx, libraryID, p, thumbDir); err != nil {
			return err
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

	if err := s.pruneMissingTracks(ctx, libraryID, cleanRoot); err != nil {
		return err
	}
	return s.cleanupEmptyAlbums(ctx, libraryID)
}

// ScanFile processes a single audio file: reads tags, resolves artist/album, upserts track, extracts art.
func (s *Scanner) ScanFile(ctx context.Context, libraryID int64, absPath string, thumbDir string) error {
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

	meta, err := music.ReadTrackMeta(resolvedPath)
	if err != nil {
		slog.Warn("scan read meta", "path", resolvedPath, "err", err)
		return nil
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
  checksum_sha256=excluded.checksum_sha256
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

func detectCompilationByArtistDiversity(ctx context.Context, tx *sql.Tx, libraryID int64, albumTitle string, year int, currentArtistID int64) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `
SELECT 1
FROM music_tracks t
JOIN music_albums al ON al.id = t.album_id
WHERE t.library_id = ?
  AND lower(al.title) = lower(?)
  AND al.year = ?
  AND t.artist_id <> ?
LIMIT 1
`, libraryID, strings.TrimSpace(albumTitle), year, currentArtistID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func mergeAlbumVariants(ctx context.Context, tx *sql.Tx, libraryID int64, albumTitle string, year int, canonicalAlbumID int64) error {
	_, err := tx.ExecContext(ctx, `
UPDATE music_tracks
SET album_id = ?
WHERE library_id = ?
  AND album_id IN (
    SELECT id FROM music_albums
    WHERE library_id = ?
      AND lower(title) = lower(?)
      AND year = ?
      AND id <> ?
  )
`, canonicalAlbumID, libraryID, libraryID, strings.TrimSpace(albumTitle), year, canonicalAlbumID)
	return err
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

	if _, err := os.Stat(outPath); err == nil {
		_, _ = tx.ExecContext(ctx, "UPDATE music_albums SET art_path=? WHERE id=?", outPath, albumID)
		return nil
	}

	if err := os.WriteFile(outPath, meta.ArtBytes, 0o644); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE music_albums SET art_path=? WHERE id=?", outPath, albumID)
	return err
}

// --- Cleanup ---

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
	for _, id := range staleIDs {
		if _, err := s.DB.ExecContext(ctx, `DELETE FROM music_tracks WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
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

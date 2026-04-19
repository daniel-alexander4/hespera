package match

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"isomedia/internal/music"
)

// RunTagWriteback is the job executor for the tag_writeback job type.
// It writes corrected metadata from the DB back to audio file tags for matched albums.
func (m *Matcher) RunTagWriteback(ctx context.Context, jobID, libraryID int64) error {
	rows, err := m.db.QueryContext(ctx, `
		SELECT t.id, t.abs_path,
		       t.title, COALESCE(ar.name, ''), COALESCE(aar.name, ''),
		       a.title, a.year, t.track_no, t.disc_no,
		       COALESCE(a.musicbrainz_id, ''), COALESCE(a.artist_musicbrainz_id, '')
		FROM music_tracks t
		JOIN music_albums a ON a.id = t.album_id
		LEFT JOIN music_artists ar ON ar.id = t.artist_id
		LEFT JOIN music_artists aar ON aar.id = a.album_artist_id
		WHERE a.library_id = ?
		  AND a.match_status = 'matched'
		ORDER BY t.id
	`, libraryID)
	if err != nil {
		return fmt.Errorf("query tracks: %w", err)
	}
	defer rows.Close()

	type trackInfo struct {
		id      int64
		absPath string
		fields  music.TagWriteFields
	}
	var tracks []trackInfo
	for rows.Next() {
		var ti trackInfo
		if err := rows.Scan(
			&ti.id, &ti.absPath,
			&ti.fields.Title, &ti.fields.Artist, &ti.fields.AlbumArtist,
			&ti.fields.Album, &ti.fields.Year, &ti.fields.TrackNo, &ti.fields.DiscNo,
			&ti.fields.AlbumMBID, &ti.fields.ArtistMBID,
		); err != nil {
			return fmt.Errorf("scan track: %w", err)
		}
		tracks = append(tracks, ti)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(tracks) == 0 {
		return nil
	}

	// Set progress total.
	_, _ = m.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", len(tracks), jobID)

	var errCount int
	for i, ti := range tracks {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := music.WriteTrackTags(ti.absPath, ti.fields); err != nil {
			slog.Warn("tag writeback failed",
				"track_id", ti.id,
				"path", ti.absPath,
				"err", err,
			)
			errCount++
			// Non-fatal: continue with next track.
		}

		// Update progress every 50 tracks.
		if (i+1)%50 == 0 || i == len(tracks)-1 {
			_, _ = m.db.ExecContext(ctx,
				"UPDATE scan_jobs SET progress_current=? WHERE id=?", i+1, jobID)
		}
	}

	if errCount > 0 {
		slog.Info("tag writeback completed with errors",
			"total", len(tracks),
			"errors", errCount,
		)
	}

	return nil
}

// writebackAlbumTracks writes corrected metadata from the DB back to audio file
// tags for all tracks under a single album. This is the inline version called
// from matchAlbum after a successful auto-match. Errors are logged per-track
// but do not cause a return error (non-fatal, same pattern as RunTagWriteback).
func writebackAlbumTracks(ctx context.Context, db *sql.DB, albumID int64) error {
	rows, err := db.QueryContext(ctx, `
		SELECT t.id, t.abs_path,
		       t.title, COALESCE(ar.name, ''), COALESCE(aar.name, ''),
		       a.title, a.year, t.track_no, t.disc_no,
		       COALESCE(a.musicbrainz_id, ''), COALESCE(a.artist_musicbrainz_id, '')
		FROM music_tracks t
		JOIN music_albums a ON a.id = t.album_id
		LEFT JOIN music_artists ar ON ar.id = t.artist_id
		LEFT JOIN music_artists aar ON aar.id = a.album_artist_id
		WHERE t.album_id = ?
	`, albumID)
	if err != nil {
		return fmt.Errorf("query tracks for album %d: %w", albumID, err)
	}
	defer rows.Close()

	type trackInfo struct {
		id      int64
		absPath string
		fields  music.TagWriteFields
	}
	var tracks []trackInfo
	for rows.Next() {
		var ti trackInfo
		if err := rows.Scan(
			&ti.id, &ti.absPath,
			&ti.fields.Title, &ti.fields.Artist, &ti.fields.AlbumArtist,
			&ti.fields.Album, &ti.fields.Year, &ti.fields.TrackNo, &ti.fields.DiscNo,
			&ti.fields.AlbumMBID, &ti.fields.ArtistMBID,
		); err != nil {
			return fmt.Errorf("scan track: %w", err)
		}
		tracks = append(tracks, ti)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, ti := range tracks {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := music.WriteTrackTags(ti.absPath, ti.fields); err != nil {
			slog.Warn("inline tag writeback failed",
				"track_id", ti.id,
				"album_id", albumID,
				"path", ti.absPath,
				"err", err,
			)
			// Non-fatal: continue with next track.
		}
	}

	return nil
}

// RunTagWritebackForLibrary creates a Matcher and runs tag writeback.
// This is a convenience for use as a job executor.
func RunTagWritebackForLibrary(db *sql.DB, dataDir string) func(ctx context.Context, jobID, libraryID int64) error {
	m := New(db, dataDir)
	return m.RunTagWriteback
}

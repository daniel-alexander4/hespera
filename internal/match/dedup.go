package match

import (
	"context"
	"database/sql"
	"fmt"
)

// DuplicateAlbum represents a single album within a duplicate group.
type DuplicateAlbum struct {
	ID            int64
	ArtistID      int64
	Title         string
	ArtistName    string
	ArtPath       string
	Year          int
	TrackCount    int
	MatchStatus   string
	IsCompilation bool
}

// DuplicateGroup represents a set of albums that are likely duplicates.
type DuplicateGroup struct {
	NormalizedTitle  string
	NormalizedArtist string
	BestAlbumID      int64
	Albums           []DuplicateAlbum
}

// FindDuplicateAlbums finds groups of albums that are likely duplicates
// based on normalized title and artist name.
func FindDuplicateAlbums(ctx context.Context, db *sql.DB, libraryID int64) ([]DuplicateGroup, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT a.id, a.album_artist_id, a.title, COALESCE(ar.name, ''),
		       COALESCE(a.art_path, ''), a.year,
		       (SELECT COUNT(*) FROM music_tracks t WHERE t.album_id = a.id) AS track_count,
		       COALESCE(a.match_status, ''),
		       COALESCE(a.is_compilation, 0)
		FROM music_albums a
		LEFT JOIN music_artists ar ON ar.id = a.album_artist_id
		WHERE a.library_id = ?
		ORDER BY a.id
	`, libraryID)
	if err != nil {
		return nil, fmt.Errorf("query albums: %w", err)
	}
	defer rows.Close()

	type albumData struct {
		album DuplicateAlbum
		normT string
		normA string
	}
	var albums []albumData
	for rows.Next() {
		var ad albumData
		var comp int
		if err := rows.Scan(
			&ad.album.ID, &ad.album.ArtistID, &ad.album.Title, &ad.album.ArtistName,
			&ad.album.ArtPath, &ad.album.Year, &ad.album.TrackCount,
			&ad.album.MatchStatus, &comp,
		); err != nil {
			return nil, err
		}
		ad.album.IsCompilation = comp != 0
		ad.normT = NormalizeForDedup(ad.album.Title)
		ad.normA = NormalizeForDedup(ad.album.ArtistName)
		albums = append(albums, ad)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Group by (normalized title, normalized artist).
	type groupKey struct{ title, artist string }
	groupMap := make(map[groupKey][]DuplicateAlbum)
	groupOrder := make([]groupKey, 0)
	for _, ad := range albums {
		key := groupKey{ad.normT, ad.normA}
		if _, exists := groupMap[key]; !exists {
			groupOrder = append(groupOrder, key)
		}
		groupMap[key] = append(groupMap[key], ad.album)
	}

	// Filter to groups with 2+ albums and pick the best.
	var groups []DuplicateGroup
	for _, key := range groupOrder {
		members := groupMap[key]
		if len(members) < 2 {
			continue
		}
		best := pickBestAlbum(members)
		groups = append(groups, DuplicateGroup{
			NormalizedTitle:  key.title,
			NormalizedArtist: key.artist,
			BestAlbumID:      best,
			Albums:           members,
		})
	}

	return groups, nil
}

// pickBestAlbum selects the best album from a group of duplicates.
// Priority: has art > matched status > most tracks > lowest ID.
func pickBestAlbum(albums []DuplicateAlbum) int64 {
	best := albums[0]
	for _, a := range albums[1:] {
		if betterThan(a, best) {
			best = a
		}
	}
	return best.ID
}

func betterThan(a, b DuplicateAlbum) bool {
	aHasArt := a.ArtPath != ""
	bHasArt := b.ArtPath != ""
	if aHasArt != bHasArt {
		return aHasArt
	}

	aMatched := a.MatchStatus == "matched"
	bMatched := b.MatchStatus == "matched"
	if aMatched != bMatched {
		return aMatched
	}

	if a.TrackCount != b.TrackCount {
		return a.TrackCount > b.TrackCount
	}

	return a.ID < b.ID
}

// MergeAlbums merges sourceAlbumID into targetAlbumID.
// Moves all tracks and play history, deletes the source album,
// and cleans up orphaned artists.
func MergeAlbums(ctx context.Context, db *sql.DB, targetAlbumID, sourceAlbumID int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Get library_id for cleanup.
	var libraryID int64
	if err := tx.QueryRowContext(ctx,
		"SELECT library_id FROM music_albums WHERE id=?", sourceAlbumID,
	).Scan(&libraryID); err != nil {
		return fmt.Errorf("source album not found: %w", err)
	}

	// Move tracks.
	if _, err := tx.ExecContext(ctx,
		"UPDATE music_tracks SET album_id=? WHERE album_id=?",
		targetAlbumID, sourceAlbumID); err != nil {
		return fmt.Errorf("move tracks: %w", err)
	}

	// Move play history.
	if _, err := tx.ExecContext(ctx,
		"UPDATE play_history SET album_id=? WHERE album_id=?",
		targetAlbumID, sourceAlbumID); err != nil {
		return fmt.Errorf("move play history: %w", err)
	}

	// Delete source album.
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM music_albums WHERE id=?", sourceAlbumID); err != nil {
		return fmt.Errorf("delete source album: %w", err)
	}

	// Cleanup orphaned artists.
	if _, err := tx.ExecContext(ctx, `
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
	`, libraryID); err != nil {
		return fmt.Errorf("cleanup artists: %w", err)
	}

	return tx.Commit()
}

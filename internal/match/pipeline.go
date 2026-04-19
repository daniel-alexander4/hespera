package match

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Matcher orchestrates the music metadata matching pipeline.
type Matcher struct {
	db      *sql.DB
	dataDir string
	mb      *MBClient
	caa     *CAAClient
}

func New(db *sql.DB, dataDir string) *Matcher {
	return &Matcher{
		db:      db,
		dataDir: dataDir,
		mb:      NewMBClient(),
		caa:     NewCAAClient(dataDir),
	}
}

// RunMusicMatch is the job executor for the music_match job type.
func (m *Matcher) RunMusicMatch(ctx context.Context, jobID, libraryID int64) error {
	rows, err := m.db.QueryContext(ctx, `
		SELECT a.id, a.title, a.year, COALESCE(ar.name, '')
		FROM music_albums a
		LEFT JOIN music_artists ar ON ar.id = a.album_artist_id
		WHERE a.library_id = ?
		  AND (a.match_status = '' OR a.match_status = 'unmatched')
		ORDER BY a.id
	`, libraryID)
	if err != nil {
		return fmt.Errorf("query albums: %w", err)
	}
	defer rows.Close()

	type albumInfo struct {
		id     int64
		title  string
		year   int
		artist string
	}
	var albums []albumInfo
	for rows.Next() {
		var a albumInfo
		if err := rows.Scan(&a.id, &a.title, &a.year, &a.artist); err != nil {
			return err
		}
		albums = append(albums, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(albums) == 0 {
		return nil
	}

	// Set progress total.
	_, _ = m.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", len(albums), jobID)

	for i, a := range albums {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := m.matchAlbum(ctx, a.id, a.title, a.artist, a.year); err != nil {
			slog.Warn("match album failed", "album_id", a.id, "title", a.title, "err", err)
			// Non-fatal: mark as unmatched and continue.
			_, _ = m.db.ExecContext(ctx,
				"UPDATE music_albums SET match_status='unmatched' WHERE id=?", a.id)
		}

		// Update progress.
		_, _ = m.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", i+1, jobID)

		// 500ms gap between albums to stay well under rate limits.
		if i < len(albums)-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}

	return nil
}

func (m *Matcher) matchAlbum(ctx context.Context, albumID int64, title, artist string, year int) error {
	if strings.TrimSpace(title) == "" || strings.TrimSpace(artist) == "" {
		_, _ = m.db.ExecContext(ctx,
			"UPDATE music_albums SET match_status='unmatched' WHERE id=?", albumID)
		return nil
	}

	candidates, err := m.mb.SearchReleaseGroups(ctx, artist, title)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	best, score, ok := BestCandidate(candidates, title, artist, year)
	if !ok || score < 45 {
		_, _ = m.db.ExecContext(ctx,
			"UPDATE music_albums SET match_status='unmatched' WHERE id=?", albumID)
		return nil
	}

	status := "uncertain"
	if score >= 70 {
		status = "matched"
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = m.db.ExecContext(ctx, `
		UPDATE music_albums SET
			match_status = ?,
			match_confidence = ?,
			match_source = 'musicbrainz',
			matched_at = ?,
			musicbrainz_id = ?,
			artist_musicbrainz_id = ?
		WHERE id = ?
	`, status, int(score), now, best.ReleaseGroupID, best.ArtistMBID, albumID)
	if err != nil {
		return fmt.Errorf("update album: %w", err)
	}

	// Fetch cover art if we got a match.
	if best.ReleaseGroupID != "" {
		var releaseIDs []string
		if best.ReleaseID != "" {
			releaseIDs = append(releaseIDs, best.ReleaseID)
		}
		artPath, artErr := m.caa.FetchCover(ctx, best.ReleaseGroupID, releaseIDs)
		if artErr != nil {
			slog.Warn("cover art fetch failed", "album_id", albumID, "err", artErr)
		} else if artPath != "" {
			// Only update art_path if currently empty (don't overwrite embedded art).
			_, _ = m.db.ExecContext(ctx,
				"UPDATE music_albums SET art_path=? WHERE id=? AND (art_path='' OR art_path IS NULL)",
				artPath, albumID)
		}
	}

	// Enrich artist if we have an artist MBID and the artist lacks metadata.
	if best.ArtistMBID != "" {
		m.enrichArtistIfNeeded(ctx, albumID, best.ArtistMBID)
	}

	return nil
}

func (m *Matcher) enrichArtistIfNeeded(ctx context.Context, albumID int64, artistMBID string) {
	// Find the album_artist_id for this album.
	var artistID int64
	var existingBio, existingArt sql.NullString
	err := m.db.QueryRowContext(ctx, `
		SELECT ar.id, ar.bio, ar.art_path
		FROM music_artists ar
		JOIN music_albums al ON al.album_artist_id = ar.id
		WHERE al.id = ?
	`, albumID).Scan(&artistID, &existingBio, &existingArt)
	if err != nil {
		return
	}

	// Skip if artist already has both bio and art.
	hasBio := existingBio.Valid && strings.TrimSpace(existingBio.String) != ""
	hasArt := existingArt.Valid && strings.TrimSpace(existingArt.String) != ""
	if hasBio && hasArt {
		return
	}

	meta, err := EnrichArtist(ctx, m.mb, artistMBID, m.dataDir)
	if err != nil {
		slog.Warn("enrich artist failed", "artist_id", artistID, "err", err)
		return
	}

	// Update MBID on the artist row.
	_, _ = m.db.ExecContext(ctx,
		"UPDATE music_artists SET musicbrainz_id=? WHERE id=? AND (musicbrainz_id='' OR musicbrainz_id IS NULL)",
		artistMBID, artistID)

	if !hasBio && meta.Bio != "" {
		_, _ = m.db.ExecContext(ctx,
			"UPDATE music_artists SET bio=?, bio_source_name=?, bio_source_url=? WHERE id=?",
			meta.Bio, meta.BioSourceName, meta.BioSourceURL, artistID)
	}
	if !hasArt && meta.ImagePath != "" {
		_, _ = m.db.ExecContext(ctx,
			"UPDATE music_artists SET art_path=? WHERE id=?",
			meta.ImagePath, artistID)
	}
}

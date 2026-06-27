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
	// One shared limiter so MusicBrainz and Cover Art Archive requests stay
	// within a single 1 req/sec MetaBrainz-family budget.
	limiter := newRateLimiter(time.Second)
	return &Matcher{
		db:      db,
		dataDir: dataDir,
		mb:      NewMBClient(limiter),
		caa:     NewCAAClient(dataDir, limiter),
	}
}

// RunMusicMatch is the job executor for the music_match job type.
// Phase 1: Enrich artists (MBID, bio, image).
// Phase 2: Match albums (MusicBrainz, cover art).
func (m *Matcher) RunMusicMatch(ctx context.Context, jobID, libraryID int64) error {
	// --- Phase 1: Artist enrichment ---
	if err := m.enrichArtists(ctx, jobID, libraryID); err != nil {
		return err
	}

	// --- Phase 2: Album matching ---
	return m.matchAlbums(ctx, jobID, libraryID)
}

// enrichArtists finds all artists in the library that are missing MBID, bio, or
// image, resolves their MusicBrainz ID, and fetches bio + image.
func (m *Matcher) enrichArtists(ctx context.Context, jobID, libraryID int64) error {
	rows, err := m.db.QueryContext(ctx, `
		SELECT DISTINCT ar.id, ar.name, ar.musicbrainz_id, ar.bio, ar.art_path
		FROM music_artists ar
		JOIN music_albums al ON al.album_artist_id = ar.id
		WHERE ar.library_id = ?
		  AND ar.name NOT IN ('Unknown Artist', 'Various Artists')
		ORDER BY ar.id
	`, libraryID)
	if err != nil {
		return fmt.Errorf("query artists: %w", err)
	}
	defer rows.Close()

	type artistInfo struct {
		id   int64
		name string
		mbid string
		bio  string
		art  string
	}
	var artists []artistInfo
	for rows.Next() {
		var a artistInfo
		var bio, art sql.NullString
		if err := rows.Scan(&a.id, &a.name, &a.mbid, &bio, &art); err != nil {
			return err
		}
		a.bio = scanNull(bio)
		a.art = scanNull(art)
		artists = append(artists, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, a := range artists {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		hasMBID := a.mbid != ""
		hasBio := a.bio != ""
		hasArt := a.art != ""
		if hasMBID && hasBio && hasArt {
			continue
		}

		slog.Info("enriching artist", "id", a.id, "name", a.name, "has_mbid", hasMBID)

		// Step 1: Resolve MBID if missing.
		mbid := a.mbid
		if mbid == "" {
			found, err := m.mb.SearchArtist(ctx, a.name)
			if err != nil {
				slog.Warn("artist search failed", "name", a.name, "err", err)
				continue
			}
			if found == "" {
				slog.Info("no MB match for artist", "name", a.name)
				continue
			}
			mbid = found
			_, _ = m.db.ExecContext(ctx,
				"UPDATE music_artists SET musicbrainz_id=? WHERE id=?", mbid, a.id)

			// Also set artist_musicbrainz_id on all albums under this artist.
			_, _ = m.db.ExecContext(ctx,
				"UPDATE music_albums SET artist_musicbrainz_id=? WHERE album_artist_id=?", mbid, a.id)
		}

		// Step 2: Fetch bio + image if missing.
		if !hasBio || !hasArt {
			meta, err := EnrichArtist(ctx, m.mb, mbid, m.dataDir)
			if err != nil {
				slog.Warn("enrich artist failed", "artist_id", a.id, "name", a.name, "err", err)
				continue
			}
			if !hasBio && meta.Bio != "" {
				_, _ = m.db.ExecContext(ctx,
					"UPDATE music_artists SET bio=?, bio_source_name=?, bio_source_url=? WHERE id=?",
					meta.Bio, meta.BioSourceName, meta.BioSourceURL, a.id)
				slog.Info("artist bio saved", "name", a.name)
			}
			if !hasArt && meta.ImagePath != "" {
				_, _ = m.db.ExecContext(ctx,
					"UPDATE music_artists SET art_path=? WHERE id=?",
					meta.ImagePath, a.id)
				slog.Info("artist image saved", "name", a.name, "path", meta.ImagePath)
			}
		}

		// Rate-limit between artists.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}

	return nil
}

// matchAlbums matches unmatched albums against MusicBrainz.
func (m *Matcher) matchAlbums(ctx context.Context, jobID, libraryID int64) error {
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
	if !ok || score < matchThreshold {
		_, _ = m.db.ExecContext(ctx,
			"UPDATE music_albums SET match_status='unmatched' WHERE id=?", albumID)
		return nil
	}

	status := "matched"

	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Title is normalized to the MusicBrainz canonical name in the same
	// statement; the CASE leaves it untouched when MB returned no title.
	_, err = m.db.ExecContext(ctx, `
		UPDATE music_albums SET
			match_status = ?,
			match_confidence = ?,
			match_source = 'musicbrainz',
			matched_at = ?,
			musicbrainz_id = ?,
			artist_musicbrainz_id = ?,
			title = CASE WHEN ? <> '' THEN ? ELSE title END
		WHERE id = ?
	`, status, int(score), now, best.ReleaseGroupID, best.ArtistMBID, best.Title, best.Title, albumID)
	if err != nil {
		return fmt.Errorf("update album: %w", err)
	}

	// Normalize artist name to MusicBrainz canonical name.
	if best.ArtistName != "" {
		if _, err := m.db.ExecContext(ctx,
			"UPDATE music_artists SET name=? WHERE id=(SELECT album_artist_id FROM music_albums WHERE id=?)",
			best.ArtistName, albumID); err != nil {
			slog.Warn("normalize artist name failed", "album_id", albumID, "err", err)
		}
	}

	// Fetch cover art. Try the matched release-group first (with its release
	// fallback). If it has no Cover Art Archive image, fall back to other
	// above-threshold candidates — but only clean, same-artist studio-album
	// editions of the same title, so we never attach a live/compilation/
	// different-album cover. Cover Art Archive returns "" (not an error) when a
	// release-group has no front image, so iterating is safe.
	if best.ReleaseGroupID != "" {
		artPath := m.fetchAlbumArt(ctx, albumID, best, CandidatesAboveThreshold(candidates, title, artist, year))
		if artPath != "" {
			// Only update art_path if currently empty (don't overwrite embedded art).
			_, _ = m.db.ExecContext(ctx,
				"UPDATE music_albums SET art_path=? WHERE id=? AND (art_path='' OR art_path IS NULL)",
				artPath, albumID)
		}
	}

	// Write tags back to audio files inline (reads normalized names from DB).
	if err := writebackAlbumTracks(ctx, m.db, albumID); err != nil {
		slog.Warn("inline writeback failed", "album_id", albumID, "err", err)
	}

	return nil
}

// maxArtFallbackCandidates caps how many sibling release-groups (beyond the
// matched one) are probed for cover art when the match itself has none.
const maxArtFallbackCandidates = 3

// fetchAlbumArt returns a saved cover-art path for the album, or "" if none was
// found. It tries the matched candidate's release-group first (including its
// single linked release), then a few sibling candidates — restricted to clean,
// same-artist studio-album editions within a small score window of the best, so
// only a same-album cover can ever be reused. Each sibling is tried at the
// release-group level only (a release-group with no front image has no
// art-bearing releases either, so the per-release fallback adds cost, not hits).
func (m *Matcher) fetchAlbumArt(ctx context.Context, albumID int64, best Candidate, scored []ScoredCandidate) string {
	// The matched release-group, with its linked release as a fallback.
	var bestReleaseIDs []string
	if best.ReleaseID != "" {
		bestReleaseIDs = append(bestReleaseIDs, best.ReleaseID)
	}
	if art := m.tryCover(ctx, albumID, best.ReleaseGroupID, bestReleaseIDs); art != "" {
		return art
	}

	// Sibling editions: same artist, clean studio album, close score.
	bestScore := 0.0
	if len(scored) > 0 {
		bestScore = scored[0].Score
	}
	tried := 0
	for _, sc := range scored {
		select {
		case <-ctx.Done():
			return ""
		default:
		}
		c := sc.Candidate
		if c.ReleaseGroupID == "" || c.ReleaseGroupID == best.ReleaseGroupID {
			continue
		}
		if sc.Score < bestScore-8 {
			break // scored is descending; nothing further is in the window
		}
		if c.ArtistMBID != best.ArtistMBID || !isCleanAlbum(c) {
			continue
		}
		if art := m.tryCover(ctx, albumID, c.ReleaseGroupID, nil); art != "" {
			return art
		}
		tried++
		if tried >= maxArtFallbackCandidates {
			break
		}
	}
	return ""
}

// tryCover fetches cover art for one release-group, logging (but not failing on)
// a fetch error.
func (m *Matcher) tryCover(ctx context.Context, albumID int64, releaseGroupID string, releaseIDs []string) string {
	if releaseGroupID == "" {
		return ""
	}
	art, err := m.caa.FetchCover(ctx, releaseGroupID, releaseIDs)
	if err != nil {
		slog.Warn("cover art fetch failed", "album_id", albumID, "release_group", releaseGroupID, "err", err)
		return ""
	}
	return art
}

func scanNull(ns sql.NullString) string {
	if ns.Valid {
		return strings.TrimSpace(ns.String)
	}
	return ""
}

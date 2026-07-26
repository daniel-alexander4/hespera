package match

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// Similar-artist caching. The list (ListenBrainz's similar-artists model, keyed
// by artist MBID) is what Instant Mix draws its pool from and what the artist
// page's "Similar Artists" strip renders, cached on music_artists.similar_json.
//
// It began as a purely lazy cache: fetched on first artist-page view, and — as a
// belt — on a cold mix, which meanwhile degrades to a single-artist queue. That
// is fine for the page (you are looking at it, and the strip fills in) but wrong
// for a mix, which silently plays only the seed artist and gives no sign that it
// is not the mix that was asked for. On a library where nothing has been fetched
// yet that is not an edge case, it is every mix: a real library measured 986 of
// 991 artists never fetched. So the match pipeline pre-warms the whole library
// (fetchSimilarArtists below) and the lazy paths stay as the belt for an artist
// added between runs.

// StoreArtistSimilar fetches one artist's similar-artists list and caches it on
// the row. The fetched-at stamp is written even for an empty or failed result,
// so a genuine miss (no MBID coverage in the model) is not re-fetched on every
// view and every run — it waits for the TTL like everything else.
//
// This is the single writer of similar_json/similar_fetched_at: the lazy
// per-artist job and the pipeline phase both come through here, so the two can't
// drift in what they store or how they stamp it.
func (m *Matcher) StoreArtistSimilar(ctx context.Context, artistID int64, mbid string) error {
	payload, err := json.Marshal(m.SimilarArtists(ctx, mbid))
	if err != nil {
		payload = []byte("[]")
	}
	_, err = m.db.ExecContext(ctx,
		"UPDATE music_artists SET similar_json=?, similar_fetched_at=? WHERE id=?",
		string(payload), stampNow(), artistID)
	return err
}

// fetchSimilarArtists pre-warms the similar-artists cache for a library's
// artists — one ListenBrainz call each (~1s on the shared 1 req/s MetaBrainz
// limiter, measured), best-effort per artist.
//
// Gating is the established recheck-TTL shape: a new artist carries an empty
// stamp and is always fetched, an already-cached one is refreshed once its TTL
// lapses, and a user-initiated (force) match refetches everyone. So a scan that
// introduces artists warms them on its chained match, and existing lists are
// kept current rather than frozen at whenever they were first pulled.
func (m *Matcher) fetchSimilarArtists(ctx context.Context, jobID, libraryID int64, force bool) error {
	cutoff := recheckCutoff(similarRecheckTTL)
	if force {
		cutoff = forceCutoff
	}
	// Same candidate shape as the popularity phase: the model is keyed by artist
	// MBID, so an artist without one can never resolve, and the two placeholder
	// artists aren't real artists to find neighbours for.
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, musicbrainz_id
		FROM music_artists
		WHERE library_id = ?
		  AND musicbrainz_id != ''
		  AND name NOT IN ('Unknown Artist', 'Various Artists')
		  AND (similar_fetched_at = '' OR similar_fetched_at < ?)
		ORDER BY id
	`, libraryID, cutoff)
	if err != nil {
		return fmt.Errorf("query artists: %w", err)
	}
	type artistRow struct {
		id   int64
		mbid string
	}
	var artists []artistRow
	for rows.Next() {
		var a artistRow
		if err := rows.Scan(&a.id, &a.mbid); err != nil {
			rows.Close()
			return err
		}
		artists = append(artists, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(artists) == 0 {
		return nil
	}

	base := m.progressAddTotal(ctx, jobID, len(artists))
	for i, a := range artists {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		m.progressSet(ctx, jobID, base+i+1)
		// Per-artist failures are noise, not a failed phase: the stamp written
		// inside StoreArtistSimilar keeps a persistent miss from being retried
		// on every run, and the next TTL lapse tries again.
		if err := m.StoreArtistSimilar(ctx, a.id, a.mbid); err != nil {
			slog.Warn("store similar artists", "artist_id", a.id, "err", err)
		}
	}
	return nil
}

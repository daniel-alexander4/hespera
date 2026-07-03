package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"

	"hespera/internal/match"
)

// Instant mix ("radio from this artist/song"): a generated queue seeded from
// the cached similar-artists list (music_artists.similar_json, ListenBrainz)
// crossed with track popularity. Pure queue source (source=mix&artist=N or
// &track=N) — nothing is persisted; "Save as playlist" on the now-playing page
// snapshots any queue, mixes included.

const (
	mixTargetTracks  = 50  // queue size ceiling
	mixPerArtistCap  = 4   // a similar artist's max contribution
	mixSeedArtistCap = 8   // the seed artist anchors the mix with more room
	mixSeedWeightPct = 125 // seed weight = strongest similar × 1.25
	mixDefaultWeight = 1   // similarity floor so a zero-score artist can still surface
)

// buildMixQueue resolves a mix from a seed artist (or a seed track, which plays
// first). Pool = seed artist + in-catalog similar artists, each contributing
// its top tracks by popularity (the Most Popular per-artist rules); selection
// is a weighted draw ∝ similarity with per-artist caps. Cold start (no cached
// similar list) degrades to the seed artist's top tracks and enqueues the
// similar-artists fetch so the next mix is real.
func (h *Handler) buildMixQueue(ctx context.Context, artistID, seedTrackID int64) (q playerQueue, notFound bool, err error) {
	q.BackURL = "/music"

	if seedTrackID > 0 {
		if qerr := h.db.QueryRowContext(ctx,
			"SELECT artist_id FROM music_tracks WHERE id=?", seedTrackID).Scan(&artistID); qerr != nil {
			return q, true, nil
		}
	}
	var name, mbid, similarJSON, similarFetchedAt sql.NullString
	if qerr := h.db.QueryRowContext(ctx,
		"SELECT name, musicbrainz_id, similar_json, similar_fetched_at FROM music_artists WHERE id=?",
		artistID).Scan(&name, &mbid, &similarJSON, &similarFetchedAt); qerr != nil {
		return q, true, nil
	}
	q.Title = strings.TrimSpace(scanNullString(name)) + " Mix"
	q.BackURL = fmt.Sprintf("/music/artist/%d", artistID)

	// Similar artists → local artist ids + similarity weights.
	var similar []match.SimilarArtist
	if s := scanNullString(similarJSON); s != "" {
		_ = json.Unmarshal([]byte(s), &similar)
	}
	weights := map[int64]int{artistID: mixDefaultWeight} // seed weight finalized below
	if len(similar) > 0 {
		local := h.localArtistIDsByMBID(ctx, similar)
		maxScore := mixDefaultWeight
		for _, a := range similar {
			id, ok := local[a.MBID]
			if !ok || id == artistID {
				continue
			}
			w := a.Score
			if w < mixDefaultWeight {
				w = mixDefaultWeight
			}
			weights[id] = w
			if w > maxScore {
				maxScore = w
			}
		}
		weights[artistID] = maxScore * mixSeedWeightPct / 100
	}

	// Cold start: no cached similar list yet → kick off the same lazy fetch the
	// artist page uses (gated by similar_fetched_at), and mix the seed alone.
	if scanNullString(similarFetchedAt) == "" && scanNullString(mbid) != "" {
		aid, m := artistID, scanNullString(mbid)
		h.enqueueMusicFetch(ctx, fmt.Sprintf("artist-similar:%d", aid), "artist_similar_fetch",
			func(jctx context.Context, mm *match.Matcher) error { return h.fetchArtistSimilar(jctx, mm, aid, m) })
	}

	// Each pool artist's top tracks by popularity — the Most Popular rules
	// (top N with popularity>0; a small curated artist contributes everything).
	ids := make([]any, 0, len(weights))
	ph := make([]string, 0, len(weights))
	for id := range weights {
		ids = append(ids, id)
		ph = append(ph, "?")
	}
	args := append(ids, popularIncludeAllMaxTracks, popularPerArtistLimit)
	pool, err := h.queryPlayerTracks(ctx, playerTrackSelect+` JOIN (
  SELECT id,
    ROW_NUMBER() OVER (PARTITION BY artist_id ORDER BY popularity DESC, id) AS rn,
    COUNT(*) OVER (PARTITION BY artist_id) AS artist_total
  FROM music_tracks WHERE artist_id IN (`+strings.Join(ph, ",")+`)
) pop ON pop.id=t.id
WHERE pop.artist_total<=? OR (t.popularity>0 AND pop.rn<=?)
ORDER BY t.artist_id, t.popularity DESC, t.id`, args...)
	if err != nil {
		return q, false, err
	}

	q.Tracks = drawMix(pool, weights, artistID, seedTrackID)
	if len(q.Tracks) == 0 {
		return q, true, nil
	}
	return q, false, nil
}

// drawMix runs the weighted selection: group the pool per artist, then
// repeatedly pick an artist with probability ∝ its similarity weight (while it
// has tracks left and is under its cap) and pop a random track. The seed track,
// when given, always opens the mix.
func drawMix(pool []trackRow, weights map[int64]int, seedArtistID, seedTrackID int64) []trackRow {
	byArtist := map[int64][]trackRow{}
	var seed *trackRow
	for _, t := range pool {
		if seedTrackID > 0 && t.ID == seedTrackID {
			tt := t
			seed = &tt
			continue
		}
		byArtist[t.ArtistID] = append(byArtist[t.ArtistID], t)
	}
	for id := range byArtist {
		rand.Shuffle(len(byArtist[id]), func(i, j int) {
			byArtist[id][i], byArtist[id][j] = byArtist[id][j], byArtist[id][i]
		})
	}

	var out []trackRow
	taken := map[int64]int{}
	if seed != nil {
		out = append(out, *seed)
		taken[seedArtistID]++
	}
	capFor := func(id int64) int {
		if id == seedArtistID {
			return mixSeedArtistCap
		}
		return mixPerArtistCap
	}
	for len(out) < mixTargetTracks {
		total := 0
		for id, tracks := range byArtist {
			if len(tracks) > 0 && taken[id] < capFor(id) {
				total += weights[id]
			}
		}
		if total <= 0 {
			break
		}
		pick := rand.Intn(total)
		for id, tracks := range byArtist {
			if len(tracks) == 0 || taken[id] >= capFor(id) {
				continue
			}
			pick -= weights[id]
			if pick < 0 {
				out = append(out, tracks[0])
				byArtist[id] = tracks[1:]
				taken[id]++
				break
			}
		}
	}
	return out
}

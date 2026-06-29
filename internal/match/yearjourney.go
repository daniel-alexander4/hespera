package match

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"hespera/internal/billboard"
)

// maxAlbumsPerArtistYear caps how many same-year albums one act contributes, so
// a prolific artist (or a noisy MB discography) can't flood the journey list.
const maxAlbumsPerArtistYear = 3

// BuildYearJourney populates the walled-off year-journey discovery list for a
// year: for every act that charted on the Billboard Hot 100 that year it
// resolves the act to MusicBrainz (preferring an MBID the library or a prior
// journey already knows, so most acts cost no network call), lists their
// album(s) released that year, and — when they had no album that year — records
// their top charting single instead. Album art is a hotlinked Cover Art Archive
// thumbnail (never downloaded). Items are written incrementally so the page can
// show progress; the journey is marked 'ready' only on full completion.
//
// Best-effort throughout: a per-act resolution/browse error is logged and
// skipped, never fatal. Re-running rebuilds from scratch (idempotent).
func (m *Matcher) BuildYearJourney(ctx context.Context, libraryID int64, year int) error {
	acts := billboard.Year(year)

	// Reset: ensure the journey row exists in 'building' state and clear any
	// prior items so a rebuild is clean.
	if _, err := m.db.ExecContext(ctx,
		`INSERT INTO year_journeys (year, status) VALUES (?, 'building')
		 ON CONFLICT(year) DO UPDATE SET status='building', built_at=''`, year); err != nil {
		return err
	}
	if _, err := m.db.ExecContext(ctx, `DELETE FROM year_journey_items WHERE year=?`, year); err != nil {
		return err
	}

	// Known artist MBIDs, normalized-name keyed: the library's matched artists
	// plus any artist a prior journey already resolved. Lets us skip the MB
	// recording search for acts we've already pinned.
	known := m.knownArtistMBIDs(ctx, libraryID)

	for _, act := range acts {
		if err := ctx.Err(); err != nil {
			return err // canceled (shutdown) — leave 'building' so a later view rebuilds
		}
		if len(act.Songs) == 0 {
			continue
		}
		key := NormalizeForDedup(act.Name)
		mbid := known[key]
		if mbid == "" {
			// Song-anchored resolution off the act's biggest hit (Songs[0]).
			resolved, err := m.mb.ResolveArtistBySong(ctx, act.Name, act.Songs[0].Title)
			if err != nil {
				slog.Warn("year-journey resolve", "year", year, "artist", act.Name, "err", err)
			} else if resolved != "" {
				mbid = resolved
				known[key] = resolved
			}
		}

		// With an MBID, list this act's albums released that year.
		var albums []ReleaseGroupBrief
		if mbid != "" {
			briefs, err := m.mb.BrowseArtistReleaseGroups(ctx, mbid)
			if err != nil {
				slog.Warn("year-journey browse", "year", year, "artist", act.Name, "err", err)
			} else {
				for _, b := range briefs {
					if b.Year == year {
						albums = append(albums, b)
						if len(albums) >= maxAlbumsPerArtistYear {
							break
						}
					}
				}
			}
		}

		if len(albums) > 0 {
			for _, al := range albums {
				artURL, _ := m.caa.CoverURL(ctx, al.MBID) // best-effort hotlink
				m.insertJourneyItem(ctx, year, "album", act, al.Title, mbid, al.MBID, al.Date, artURL)
			}
			continue
		}

		// No album that year → the act's top charting single is the target.
		top := act.Songs[0]
		m.insertJourneyItem(ctx, year, "single", act, top.Title, mbid, "", top.Debut, "")
	}

	_, err := m.db.ExecContext(ctx,
		`UPDATE year_journeys SET status='ready', built_at=? WHERE year=?`,
		time.Now().UTC().Format(time.RFC3339), year)
	return err
}

// knownArtistMBIDs returns a normalized-name → MBID map of artists already
// resolved, drawn from the library's matched artists and every prior journey
// item. One query each; both are small.
func (m *Matcher) knownArtistMBIDs(ctx context.Context, libraryID int64) map[string]string {
	out := map[string]string{}
	add := func(q string, args ...any) {
		rows, err := m.db.QueryContext(ctx, q, args...)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var name, mbid string
			if err := rows.Scan(&name, &mbid); err != nil {
				continue
			}
			if k := NormalizeForDedup(name); k != "" && mbid != "" {
				if _, ok := out[k]; !ok {
					out[k] = mbid
				}
			}
		}
	}
	add(`SELECT name, musicbrainz_id FROM music_artists WHERE library_id=? AND musicbrainz_id!=''`, libraryID)
	add(`SELECT artist_name, artist_mbid FROM year_journey_items WHERE artist_mbid!=''`)
	return out
}

func (m *Matcher) insertJourneyItem(ctx context.Context, year int, kind string, act billboard.Artist, title, artistMBID, rgMBID, releaseDate, artURL string) {
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO year_journey_items
		 (year, kind, artist_name, artist_mbid, title, rg_mbid, release_date, chart_peak, art_url)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		year, kind, strings.TrimSpace(act.Name), artistMBID, strings.TrimSpace(title), rgMBID, releaseDate, act.Peak, artURL)
	if err != nil {
		slog.Warn("year-journey insert", "year", year, "artist", act.Name, "err", err)
	}
}

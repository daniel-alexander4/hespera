package match

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"hespera/internal/ratelimit"
	"hespera/internal/thumbgc"
)

// Matcher orchestrates the music metadata matching pipeline.
type Matcher struct {
	db      *sql.DB
	dataDir string
	mb      *MBClient
	caa     *CAAClient
	fanart  *FanartClient  // optional artist-image backfill; nil when no key
	audiodb *AudioDBClient // optional artist bio/image backfill; nil when no key
	lb      *LBClient      // ListenBrainz popularity (no key; shared MB limiter)
	lastfm  *LastfmClient  // optional secondary popularity source; nil when no key
}

// New builds a matcher. fanartKey/audiodbKey/lastfmKey are optional,
// user-supplied API keys — empty disables that provider. lastfmKey adds Last.fm
// play counts as a secondary popularity source (filling tracks ListenBrainz
// missed).
func New(db *sql.DB, dataDir, fanartKey, audiodbKey, lastfmKey string) *Matcher {
	// One shared limiter so MusicBrainz and Cover Art Archive requests stay
	// within a single 1 req/sec MetaBrainz-family budget. The backfill providers
	// are separate hosts with their own limiters (built inside their clients).
	limiter := ratelimit.New(time.Second)
	return &Matcher{
		db:      db,
		dataDir: dataDir,
		mb:      NewMBClient(limiter),
		caa:     NewCAAClient(dataDir, limiter),
		fanart:  NewFanartClient(fanartKey),
		audiodb: NewAudioDBClient(audiodbKey),
		lb:      NewLBClient(limiter),
		lastfm:  NewLastfmClient(lastfmKey),
	}
}

// ResolveArtistCandidates returns MusicBrainz artist candidates for the manual
// disambiguation control, so a user can correct a wrong same-named-artist match.
func (m *Matcher) ResolveArtistCandidates(ctx context.Context, name string) ([]ArtistCandidate, error) {
	return m.mb.SearchArtistCandidates(ctx, name)
}

// ReEnrichArtist re-runs bio + image enrichment for an explicitly chosen artist
// MBID (manual disambiguation), bypassing the name search. The caller is
// responsible for storing the chosen MBID and clearing stale bio/art first.
func (m *Matcher) ReEnrichArtist(ctx context.Context, artistMBID string) (*ArtistMeta, error) {
	return EnrichArtist(ctx, m.mb, m.fanart, m.audiodb, artistMBID, m.dataDir)
}

// similarArtistLimit caps how many related artists the artist page shows.
const similarArtistLimit = 18

// SimilarArtists returns related artists (ListenBrainz, highest score first),
// dropping the reference artist itself and capping the list. Empty on any miss —
// the section simply doesn't render.
func (m *Matcher) SimilarArtists(ctx context.Context, artistMBID string) []SimilarArtist {
	list, ok := m.lb.SimilarArtists(ctx, artistMBID)
	if !ok {
		return nil
	}
	out := make([]SimilarArtist, 0, similarArtistLimit)
	for _, a := range list {
		if a.MBID == artistMBID {
			continue
		}
		out = append(out, a)
		if len(out) >= similarArtistLimit {
			break
		}
	}
	return out
}

// ExternalArtistMeta is the resolved profile of an out-of-catalog artist for its
// dedicated page: bio + a hotlinkable image URL (not downloaded) + notable
// release-groups.
type ExternalArtistMeta struct {
	Name         string
	Bio          string
	BioSourceURL string
	ImageURL     string
	Releases     []ReleaseGroupBrief
}

const externalArtistReleaseLimit = 18

// ResolveExternalArtist fetches an out-of-catalog artist's bio + image URL +
// notable releases by MBID. The image is left as an external URL (the page
// hotlinks it) rather than downloaded — see enrichArtist's download flag.
func (m *Matcher) ResolveExternalArtist(ctx context.Context, artistMBID string) (*ExternalArtistMeta, error) {
	meta, err := enrichArtist(ctx, m.mb, m.fanart, m.audiodb, artistMBID, m.dataDir, false)
	if err != nil {
		return nil, err
	}
	out := &ExternalArtistMeta{Name: meta.Name, Bio: meta.Bio, BioSourceURL: meta.BioSourceURL, ImageURL: meta.ImageURL}
	if rgs, err := m.mb.BrowseArtistReleaseGroups(ctx, artistMBID); err == nil {
		if len(rgs) > externalArtistReleaseLimit {
			rgs = rgs[:externalArtistReleaseLimit]
		}
		out.Releases = rgs
	}
	return out, nil
}

// ArtistImageCandidate is one selectable artist image surfaced to the picker.
type ArtistImageCandidate struct {
	URL    string
	Source string // provider name, e.g. "fanart.tv" / "TheAudioDB"
	Kind   string // "thumb"/"background"/"" — provider-dependent
}

// ArtistImageCandidates gathers selectable artist images from the configured
// providers, keyed by the artist MBID: fanart.tv supplies a gallery (multiple
// thumbs + backgrounds), TheAudioDB a single thumb. Providers without a key are
// nil and contribute nothing — so the list is empty when no keys are set.
func (m *Matcher) ArtistImageCandidates(ctx context.Context, artistMBID string) []ArtistImageCandidate {
	if artistMBID == "" {
		return nil
	}
	var out []ArtistImageCandidate
	if m.fanart != nil {
		for _, img := range m.fanart.ArtistImages(ctx, artistMBID) {
			out = append(out, ArtistImageCandidate{URL: img.URL, Source: "fanart.tv", Kind: img.Kind})
		}
	}
	if m.audiodb != nil {
		if u := m.audiodb.ArtistImageURL(ctx, artistMBID); u != "" {
			out = append(out, ArtistImageCandidate{URL: u, Source: "TheAudioDB", Kind: "thumb"})
		}
	}
	return out
}

// RunMusicMatch is the job executor for the music_match job type.
// Phase 1: Enrich artists (MBID, bio, image).
// Phase 2: Match albums (MusicBrainz, cover art).
//
// force bypasses the per-candidate re-check TTLs (enrich_checked_at /
// popularity_checked_at / match_checked_at). Callers pass force=true for a
// USER-initiated match (the Match button, hescli match) — the full-retry
// behavior that lets a matcher improvement retroactively fix a library — and
// force=false for automatic chain runs (post-scan, watcher, boot auto-resume),
// which would otherwise re-fan-out the full network cost for every
// permanently-incomplete artist and unmatchable album on every run, daily.
func (m *Matcher) RunMusicMatch(ctx context.Context, jobID, libraryID int64, force bool) error {
	// --- Phase 1: Artist enrichment ---
	if err := m.enrichArtists(ctx, jobID, libraryID, force); err != nil {
		return err
	}

	// --- Phase 1b: Per-track popularity from ListenBrainz (non-fatal) ---
	// Runs after artist enrichment so artist MBIDs are resolved. Best-effort —
	// a network/coverage gap just leaves popularity unfilled, not a failed match.
	if err := m.fetchPopularity(ctx, jobID, libraryID, force); err != nil {
		slog.Warn("popularity phase", "err", err)
	}

	// --- Phase 2: Album matching ---
	if err := m.matchAlbums(ctx, jobID, libraryID, force); err != nil {
		return err
	}

	// --- Phase 3: Re-fetch cover art for matched albums that still have none ---
	if err := m.refetchMissingArt(ctx, jobID, libraryID); err != nil {
		return err
	}

	// --- Phase 4: Sweep orphaned album/artist thumbnails (non-fatal) ---
	// Runs last, after all art writes are committed; the single-worker job queue
	// serializes this against every other art writer.
	if n, err := thumbgc.Sweep(ctx, m.db, filepath.Join(m.dataDir, "thumbs", "music"), thumbgc.Grace,
		"SELECT art_path FROM music_albums WHERE art_path != ''",
		"SELECT art_path FROM music_artists WHERE art_path != ''",
	); err != nil {
		slog.Warn("thumb gc music", "err", err)
	} else if n > 0 {
		slog.Info("thumb gc music", "deleted", n)
	}
	return nil
}

// artRecheckTTL bounds how often a matched-but-art-less album is re-probed for
// cover art. Most such albums genuinely have no Cover Art Archive image, but CAA
// accrues art over time, so we retry on a slow cadence rather than never.
const artRecheckTTL = 30 * 24 * time.Hour

// Re-check TTLs for the other pipeline phases — the art_checked_at pattern
// applied to what were the three ungated phases. On an unchanged library an
// automatic (non-force) match run sees empty candidate sets inside these
// windows instead of re-issuing the full per-candidate network fan-out (measured
// on a real library: 98 minutes/run). New candidates carry an empty stamp and
// are always processed immediately; a user-initiated run (force) bypasses all
// three. Popularity is shorter so Shuffle Most Popular stays reasonably fresh
// for newly added tracks of existing artists.
const (
	enrichRecheckTTL     = 30 * 24 * time.Hour
	popularityRecheckTTL = 7 * 24 * time.Hour
	matchRecheckTTL      = 30 * 24 * time.Hour
)

// recheckCutoff renders the TTL comparison value for a checked-at column, and
// stampNow the value written back — the art_checked_at conventions.
func recheckCutoff(ttl time.Duration) string {
	return time.Now().Add(-ttl).UTC().Format(time.RFC3339)
}

func stampNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// forceCutoff sorts after every real RFC3339 stamp, so a force run's TTL
// predicate (`checked_at < cutoff`) admits every candidate.
const forceCutoff = "9999-12-31T23:59:59Z"

// refetchMissingArt is a second pass that fills cover art for albums that were
// matched but still have no art_path — e.g. matched before an art improvement
// shipped, or whose matched release-group had no image at the time. It re-runs
// only the cover-art step (never identity), anchored to the album's STORED
// MusicBrainz identity, and stamps art_checked_at so an art-less album isn't
// re-probed on every run.
func (m *Matcher) refetchMissingArt(ctx context.Context, jobID, libraryID int64) error {
	cutoff := time.Now().Add(-artRecheckTTL).UTC().Format(time.RFC3339)
	rows, err := m.db.QueryContext(ctx, `
		SELECT a.id, a.title, COALESCE(ar.name, ''), a.year, a.musicbrainz_id, a.artist_musicbrainz_id
		FROM music_albums a
		LEFT JOIN music_artists ar ON ar.id = a.album_artist_id
		WHERE a.library_id = ?
		  AND a.match_status = 'matched'
		  AND (a.art_path = '' OR a.art_path IS NULL)
		  AND a.musicbrainz_id != ''
		  AND (a.art_checked_at = '' OR a.art_checked_at < ?)
		ORDER BY a.id
	`, libraryID, cutoff)
	if err != nil {
		return fmt.Errorf("query art-less albums: %w", err)
	}
	defer rows.Close()

	type albumArt struct {
		id         int64
		title      string
		artist     string
		year       int
		rgID       string
		artistMBID string
	}
	var albums []albumArt
	for rows.Next() {
		var a albumArt
		if err := rows.Scan(&a.id, &a.title, &a.artist, &a.year, &a.rgID, &a.artistMBID); err != nil {
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

	// Extend the job's progress to cover this phase too (so it doesn't sit at
	// 100% while this churns through rate-limited lookups).
	base := m.progressAddTotal(ctx, jobID, len(albums))

	now := time.Now().UTC().Format(time.RFC3339)
	for i, a := range albums {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		candidates, err := m.mb.SearchReleaseGroups(ctx, a.artist, a.title)
		if err != nil {
			slog.Warn("refetch art search failed", "album_id", a.id, "title", a.title, "err", err)
		} else {
			// Anchor art to the album's STORED identity: re-search only supplies
			// candidate breadth; the cover must come from the matched
			// release-group or a same-artist clean-album sibling of it.
			stored := Candidate{ReleaseGroupID: a.rgID, ArtistMBID: a.artistMBID}
			if artPath := m.fetchAlbumArt(ctx, a.id, stored, CandidatesAboveThreshold(candidates, a.title, a.artist, a.year)); artPath != "" {
				// Guard on the stored identity so we never clobber an album the
				// user unmatched mid-run.
				_, _ = m.db.ExecContext(ctx,
					"UPDATE music_albums SET art_path=? WHERE id=? AND match_status='matched' AND musicbrainz_id=? AND (art_path='' OR art_path IS NULL)",
					artPath, a.id, a.rgID)
			}
		}
		// Stamp the check time whether or not art was found (hit or miss).
		_, _ = m.db.ExecContext(ctx,
			"UPDATE music_albums SET art_checked_at=? WHERE id=? AND match_status='matched' AND musicbrainz_id=?",
			now, a.id, a.rgID)

		m.progressSet(ctx, jobID, base+i+1)

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

// RefetchAlbumArt re-fetches cover art for a single album, anchored to its
// STORED MusicBrainz release-group identity, and saves it. It is the art half of
// the manual release-group reassignment control: the caller re-points the
// album's musicbrainz_id and clears its art, then calls this to pull the cover
// for the new release-group immediately. Non-fatal — returns the search error
// (so the handler can log it) but always stamps art_checked_at; finding no art
// is not an error. Mirrors one iteration of refetchMissingArt.
func (m *Matcher) RefetchAlbumArt(ctx context.Context, albumID int64) error {
	var title, artist, rgID, artistMBID string
	var year int
	if err := m.db.QueryRowContext(ctx, `
		SELECT a.title, COALESCE(ar.name, ''), a.year, a.musicbrainz_id, a.artist_musicbrainz_id
		FROM music_albums a
		LEFT JOIN music_artists ar ON ar.id = a.album_artist_id
		WHERE a.id = ?
	`, albumID).Scan(&title, &artist, &year, &rgID, &artistMBID); err != nil {
		return err
	}
	if strings.TrimSpace(rgID) == "" {
		return nil // nothing to anchor art to
	}

	now := time.Now().UTC().Format(time.RFC3339)
	candidates, err := m.mb.SearchReleaseGroups(ctx, artist, title)
	if err != nil {
		_, _ = m.db.ExecContext(ctx,
			"UPDATE music_albums SET art_checked_at=? WHERE id=? AND musicbrainz_id=?", now, albumID, rgID)
		return fmt.Errorf("release-group search: %w", err)
	}

	// Anchor art to the album's STORED identity: re-search supplies only candidate
	// breadth; the cover must come from the matched release-group or a same-artist
	// clean-album sibling of it.
	stored := Candidate{ReleaseGroupID: rgID, ArtistMBID: artistMBID}
	if artPath := m.fetchAlbumArt(ctx, albumID, stored, CandidatesAboveThreshold(candidates, title, artist, year)); artPath != "" {
		_, _ = m.db.ExecContext(ctx,
			"UPDATE music_albums SET art_path=? WHERE id=? AND musicbrainz_id=? AND (art_path='' OR art_path IS NULL)",
			artPath, albumID, rgID)
	}
	_, _ = m.db.ExecContext(ctx,
		"UPDATE music_albums SET art_checked_at=? WHERE id=? AND musicbrainz_id=?", now, albumID, rgID)
	return nil
}

// progressAddTotal grows the job's progress_total by n and returns the current
// progress_current as this phase's base, so RunMusicMatch's phases accumulate
// into one monotonic bar (enrich → popularity → match → art) instead of each
// resetting it. Best-effort — progress is cosmetic, so a failed update never
// fails the match. No-op (returns 0) when n <= 0.
func (m *Matcher) progressAddTotal(ctx context.Context, jobID int64, n int) int {
	if n <= 0 {
		return 0
	}
	var base, total int
	_ = m.db.QueryRowContext(ctx,
		"SELECT progress_current, progress_total FROM scan_jobs WHERE id=?", jobID).Scan(&base, &total)
	_, _ = m.db.ExecContext(ctx,
		"UPDATE scan_jobs SET progress_total=? WHERE id=?", total+n, jobID)
	return base
}

// progressSet records progress_current for the job (best-effort).
func (m *Matcher) progressSet(ctx context.Context, jobID int64, current int) {
	_, _ = m.db.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", current, jobID)
}

// enrichArtists finds all artists in the library that are missing MBID, bio, or
// image, resolves their MusicBrainz ID, and fetches bio + image.
func (m *Matcher) enrichArtists(ctx context.Context, jobID, libraryID int64, force bool) error {
	// Non-force runs skip artists whose last enrichment attempt is inside the
	// TTL — a permanently-incomplete artist (bio found nowhere) otherwise costs
	// its full MB+wiki+fanart+AudioDB fan-out on every automatic run, forever.
	// Complete artists still pass the query (the loop skips them cheaply) and
	// never carry a stamp; new artists have an empty stamp and always process.
	cutoff := recheckCutoff(enrichRecheckTTL)
	if force {
		cutoff = forceCutoff
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT DISTINCT ar.id, ar.name, ar.musicbrainz_id, ar.bio, ar.art_path
		FROM music_artists ar
		JOIN music_albums al ON al.album_artist_id = ar.id
		WHERE ar.library_id = ?
		  AND ar.name NOT IN ('Unknown Artist', 'Various Artists')
		  AND (ar.enrich_checked_at = '' OR ar.enrich_checked_at < ?)
		ORDER BY ar.id
	`, libraryID, cutoff)
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

	base := m.progressAddTotal(ctx, jobID, len(artists))
	for i, a := range artists {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		m.progressSet(ctx, jobID, base+i+1)

		hasMBID := a.mbid != ""
		hasBio := a.bio != ""
		hasArt := a.art != ""
		if hasMBID && hasBio && hasArt {
			continue
		}

		// Stamp the attempt up front (every exit below — no MB match, enrich
		// error, partial fill — counts as "tried"), so a still-incomplete artist
		// isn't re-fanned-out on every automatic run inside the TTL.
		_, _ = m.db.ExecContext(ctx,
			"UPDATE music_artists SET enrich_checked_at=? WHERE id=?", stampNow(), a.id)

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
			meta, err := EnrichArtist(ctx, m.mb, m.fanart, m.audiodb, mbid, m.dataDir)
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

// fetchPopularity fills music_tracks.popularity. The primary source is
// ListenBrainz global listen counts: for each artist with a resolved MBID it
// fetches the artist's top-recordings (ranked by listen count) and credits each
// local track whose normalized title matches. When a Last.fm key is configured
// (m.lastfm != nil), Last.fm play counts are a secondary source that fills only
// the tracks ListenBrainz left at 0 (ListenBrainz stays primary). Tracks with no
// match from either keep popularity 0 (excluded from the Most Popular playlist,
// unless their artist is in the <=4-track curated set). Best-effort and
// idempotent — re-runs each Match. Mirrors enrichArtists' shape: drain artists
// before the network loop, ctx-cancellable, 500ms inter-artist gap.
func (m *Matcher) fetchPopularity(ctx context.Context, jobID, libraryID int64, force bool) error {
	// Non-force runs skip artists fetched inside the (shorter) popularity TTL —
	// listen counts drift slowly, and re-fetching every MBID artist on every
	// automatic run costs one ListenBrainz call each on the shared 1 req/s
	// MetaBrainz limiter (plus Last.fm).
	cutoff := recheckCutoff(popularityRecheckTTL)
	if force {
		cutoff = forceCutoff
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, musicbrainz_id, name
		FROM music_artists
		WHERE library_id = ?
		  AND musicbrainz_id != ''
		  AND name NOT IN ('Unknown Artist', 'Various Artists')
		  AND (popularity_checked_at = '' OR popularity_checked_at < ?)
		ORDER BY id
	`, libraryID, cutoff)
	if err != nil {
		return fmt.Errorf("query artists: %w", err)
	}
	type artistRow struct {
		id   int64
		mbid string
		name string
	}
	var artists []artistRow
	for rows.Next() {
		var a artistRow
		if err := rows.Scan(&a.id, &a.mbid, &a.name); err != nil {
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

		// Stamp the attempt up front — success or coverage-gap, it counts as
		// "fetched" for the TTL.
		_, _ = m.db.ExecContext(ctx,
			"UPDATE music_artists SET popularity_checked_at=? WHERE id=?", stampNow(), a.id)

		// Load this artist's local tracks once — both popularity sources credit
		// them. norm = normalized title for matching; pop = current stored value.
		trackRows, terr := m.db.QueryContext(ctx,
			"SELECT id, title, popularity FROM music_tracks WHERE library_id=? AND artist_id=?", libraryID, a.id)
		if terr == nil {
			type lt struct {
				id   int64
				norm string
				pop  int
			}
			var locals []lt
			for trackRows.Next() {
				var t lt
				var title string
				if err := trackRows.Scan(&t.id, &title, &t.pop); err == nil {
					t.norm = NormalizeForDedup(title)
					locals = append(locals, t)
				}
			}
			trackRows.Close()

			credited := make(map[int64]bool, len(locals))

			// Primary: ListenBrainz global listen counts (by artist MBID).
			if recs, ok := m.lb.TopRecordings(ctx, a.mbid); ok && len(recs) > 0 {
				byName := make(map[string]int, len(recs))
				for _, r := range recs {
					key := NormalizeForDedup(r.Name)
					if key == "" {
						continue
					}
					if r.ListenCount > byName[key] {
						byName[key] = r.ListenCount
					}
				}
				for _, t := range locals {
					if c, hit := byName[t.norm]; hit {
						_, _ = m.db.ExecContext(ctx,
							"UPDATE music_tracks SET popularity=? WHERE id=?", c, t.id)
						credited[t.id] = true
					}
				}
			}

			// Secondary (optional): Last.fm play counts fill tracks ListenBrainz
			// left at 0 — ListenBrainz stays primary (a track it credited, or that
			// already carries a count from a prior run, is left alone).
			if m.lastfm != nil {
				if byName, ok := m.lastfm.TopTracks(ctx, a.name); ok {
					for _, t := range locals {
						if credited[t.id] || t.pop > 0 {
							continue
						}
						if c, hit := byName[t.norm]; hit && c > 0 {
							_, _ = m.db.ExecContext(ctx,
								"UPDATE music_tracks SET popularity=? WHERE id=?", c, t.id)
						}
					}
				}
			}
		}

		if i < len(artists)-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	return nil
}

// matchAlbums matches unmatched albums against MusicBrainz.
func (m *Matcher) matchAlbums(ctx context.Context, jobID, libraryID int64, force bool) error {
	// Non-force runs skip albums whose last failed attempt is inside the TTL —
	// a permanently-unmatchable album otherwise costs its full MB cascade (up
	// to ~6 queries at 1 req/s) on every automatic run. Matched albums leave
	// the set by status; the album Unmatch reset clears the stamp so a
	// deliberate unmatch retries on the very next run.
	cutoff := recheckCutoff(matchRecheckTTL)
	if force {
		cutoff = forceCutoff
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT a.id, a.title, a.year, COALESCE(ar.name, '')
		FROM music_albums a
		LEFT JOIN music_artists ar ON ar.id = a.album_artist_id
		WHERE a.library_id = ?
		  AND (a.match_status = '' OR a.match_status = 'unmatched')
		  AND (a.match_checked_at = '' OR a.match_checked_at < ?)
		ORDER BY a.id
	`, libraryID, cutoff)
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

	// Extend the job's progress to cover this phase (accumulating onto the enrich +
	// popularity phases that already ran, so the bar doesn't reset here).
	base := m.progressAddTotal(ctx, jobID, len(albums))

	for i, a := range albums {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Stamp the attempt up front — a no-match exit inside matchAlbum still
		// counts as "tried" for the TTL.
		_, _ = m.db.ExecContext(ctx,
			"UPDATE music_albums SET match_checked_at=? WHERE id=?", stampNow(), a.id)

		if err := m.matchAlbum(ctx, a.id, a.title, a.artist, a.year); err != nil {
			slog.Warn("match album failed", "album_id", a.id, "title", a.title, "err", err)
			// Non-fatal: mark as unmatched and continue.
			_, _ = m.db.ExecContext(ctx,
				"UPDATE music_albums SET match_status='unmatched' WHERE id=?", a.id)
		}

		m.progressSet(ctx, jobID, base+i+1)

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

	// Normalize the local title (strip remaster/deluxe/live-date annotations) for
	// both search and scoring — the raw title would otherwise defeat the
	// release-group query and the similarity filter (e.g. a live album tagged
	// "The End (4 February 2017, Birmingham)" finds nothing for plain "The End").
	searchTitle := NormalizeTitle(title)

	candidates, err := m.mb.SearchReleaseGroups(ctx, artist, searchTitle)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	// Resolve alt-title matches (e.g. US "Hell Bent for Leather" == MB "Killing
	// Machine") before scoring, so an alias-named album can win over a same-named
	// single. Bounded: only candidates MusicBrainz scored highly but whose
	// canonical title disagrees with ours are looked up.
	m.enrichAliases(ctx, candidates, searchTitle)

	// Non-demoting threshold: the edition-type penalty reorders same-titled
	// siblings but never unmatches a strong title/artist/year match (a real Live
	// album shouldn't fall under the gate just for being Live).
	best, score, ok := BestMatchCandidate(candidates, searchTitle, artist, year)
	if !ok {
		_, _ = m.db.ExecContext(ctx,
			"UPDATE music_albums SET match_status='unmatched' WHERE id=?", albumID)
		return nil
	}

	slog.Info("album matched", "album_id", albumID, "release_group", best.ReleaseGroupID,
		"mb_title", best.Title, "primary_type", best.PrimaryType, "score", int(score))

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
		artPath := m.fetchAlbumArt(ctx, albumID, best, CandidatesAboveThreshold(candidates, searchTitle, artist, year))
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

// Alias enrichment thresholds. A candidate is looked up for aliases only when
// MusicBrainz scored it highly (it matched the query strongly, likely via an
// alias) yet its canonical title disagrees with ours — the signature of a
// regional retitle. Most albums never trip this (their canonical title already
// matches), so whole-library matches incur ~no extra calls; the cap bounds the
// worst case for albums with same-named siblings.
const (
	aliasEnrichMinMBScore  = 90
	aliasEnrichMaxTitleSim = 0.9
	aliasEnrichMaxLookups  = 3
)

// enrichAliases fetches release-group aliases (in place) for the few candidates
// whose high MusicBrainz score disagrees with our canonical-title comparison,
// so scoring can credit an alt-title match. Each lookup is throttled via the
// shared MusicBrainz limiter; failures are logged and skipped.
func (m *Matcher) enrichAliases(ctx context.Context, candidates []Candidate, localTitle string) {
	lt := NormalizeTitle(localTitle)
	looked := 0
	for i := range candidates {
		c := candidates[i]
		if c.ReleaseGroupID == "" || c.MBScore < aliasEnrichMinMBScore {
			continue
		}
		if NormalizedSimilarity(NormalizeTitle(c.Title), lt) >= aliasEnrichMaxTitleSim {
			continue // canonical title already matches; no alias needed
		}
		aliases, err := m.mb.LookupReleaseGroupAliases(ctx, c.ReleaseGroupID)
		if err != nil {
			slog.Warn("release-group alias lookup failed", "release_group", c.ReleaseGroupID, "err", err)
			continue
		}
		candidates[i].Aliases = aliases
		looked++
		if looked >= aliasEnrichMaxLookups {
			break
		}
	}
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
	if art != "" {
		slog.Info("cover art selected", "album_id", albumID, "release_group", releaseGroupID)
	}
	return art
}

func scanNull(ns sql.NullString) string {
	if ns.Valid {
		return strings.TrimSpace(ns.String)
	}
	return ""
}

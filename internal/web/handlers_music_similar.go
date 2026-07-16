package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"hespera/internal/match"
)

// enqueueMusicFetch enqueues a one-off background music-metadata fetch (similar
// artists, out-of-catalog artist bio), deduped by key so a cache-miss page view
// fires at most one job per entity while queued/running. Unlike enqueueMetaFetch
// (TMDB) this needs no API key — ListenBrainz/MusicBrainz/Wikipedia are keyless;
// the fanart.tv/TheAudioDB backfill is optional inside the matcher.
func (h *Handler) enqueueMusicFetch(ctx context.Context, dedupeKey, jobType string, run func(ctx context.Context, m *match.Matcher) error) {
	if _, busy := h.metaFetch.LoadOrStore(dedupeKey, true); busy {
		return
	}
	matcher := match.New(h.db, h.cfg.DataDir, h.effectiveFanartKey(ctx), h.effectiveAudioDBKey(ctx), h.effectiveLastfmKey(ctx))
	_, err := h.jobs.Enqueue(jobType, 0, "system", func(jctx context.Context, jobID, libID int64) error {
		defer h.metaFetch.Delete(dedupeKey)
		return run(jctx, matcher)
	})
	if err != nil {
		h.metaFetch.Delete(dedupeKey)
		slog.Warn("enqueue music fetch", "job", jobType, "key", dedupeKey, "err", err)
	}
}

// similarArtistCard is one "Similar Artists" card. In-catalog artists deep-link
// to their local page; the rest link to the out-of-catalog external page.
type similarArtistCard struct {
	Name      string
	Comment   string
	MBID      string
	LocalID   int64
	InCatalog bool
}

// loadArtistSimilarCards turns the cached similar_json into cards, resolving which
// similar artists are already in the local catalog (one IN query) so those link
// locally instead of to the external page.
func (h *Handler) loadArtistSimilarCards(ctx context.Context, similarJSON string) []similarArtistCard {
	if similarJSON == "" {
		return nil
	}
	var list []match.SimilarArtist
	if json.Unmarshal([]byte(similarJSON), &list) != nil || len(list) == 0 {
		return nil
	}
	local := h.localArtistIDsByMBID(ctx, list)
	cards := make([]similarArtistCard, 0, len(list))
	for _, a := range list {
		c := similarArtistCard{Name: a.Name, Comment: a.Comment, MBID: a.MBID}
		if id, ok := local[a.MBID]; ok {
			c.LocalID, c.InCatalog = id, true
		}
		cards = append(cards, c)
	}
	return cards
}

// localArtistIDsByMBID maps each similar artist's MBID to a local music_artists.id
// when one exists, in a single query.
func (h *Handler) localArtistIDsByMBID(ctx context.Context, list []match.SimilarArtist) map[string]int64 {
	mbids := make([]any, 0, len(list))
	for _, a := range list {
		if a.MBID != "" {
			mbids = append(mbids, a.MBID)
		}
	}
	if len(mbids) == 0 {
		return nil
	}
	q := "SELECT musicbrainz_id, id FROM music_artists WHERE musicbrainz_id IN (?" + strings.Repeat(",?", len(mbids)-1) + ")"
	rows, err := h.db.QueryContext(ctx, q, mbids...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make(map[string]int64, len(mbids))
	for rows.Next() {
		var mbid string
		var id int64
		if rows.Scan(&mbid, &id) == nil {
			out[mbid] = id
		}
	}
	return out
}

// fetchArtistSimilar is the artist_similar_fetch job body: pull the similar list
// and cache it on the artist row. The fetched-at marker is written even on an
// empty/failed result so the page doesn't re-enqueue every view.
func (h *Handler) fetchArtistSimilar(ctx context.Context, m *match.Matcher, artistID int64, mbid string) error {
	list := m.SimilarArtists(ctx, mbid)
	payload, err := json.Marshal(list)
	if err != nil {
		payload = []byte("[]")
	}
	_, err = h.db.ExecContext(ctx,
		"UPDATE music_artists SET similar_json=?, similar_fetched_at=datetime('now') WHERE id=?",
		string(payload), artistID)
	return err
}

// musicArtistExternal renders the dedicated page for an out-of-catalog artist (a
// "Similar Artist" the user doesn't own): bio + image + notable releases. The
// data is fetched lazily in the background on first view (like the actor page).
// If the MBID turns out to be in the local catalog, redirect to the local page.
func (h *Handler) musicArtistExternal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	mbid := strings.TrimSpace(r.URL.Query().Get("mbid"))
	if !mbidPattern.MatchString(mbid) {
		http.NotFound(w, r)
		return
	}

	// A similar artist that IS in the catalog belongs on its local page.
	var localID int64
	if h.db.QueryRowContext(r.Context(), "SELECT id FROM music_artists WHERE musicbrainz_id=? LIMIT 1", mbid).Scan(&localID) == nil {
		http.Redirect(w, r, fmt.Sprintf("/music/artist/%d", localID), http.StatusFound)
		return
	}

	var name, comment, bio, bioURL, imageURL, releasesJSON, fetchedAt string
	hasRow := h.db.QueryRowContext(r.Context(),
		"SELECT name, comment, bio, bio_source_url, image_url, releases_json, fetched_at FROM external_artists WHERE mbid=?", mbid,
	).Scan(&name, &comment, &bio, &bioURL, &imageURL, &releasesJSON, &fetchedAt) == nil

	if !hasRow || fetchedAt == "" {
		key := mbid
		h.enqueueMusicFetch(r.Context(), "ext-artist:"+key, "external_artist_fetch",
			func(ctx context.Context, m *match.Matcher) error { return h.fetchExternalArtist(ctx, m, key) })
	}

	var releases []match.ReleaseGroupBrief
	if releasesJSON != "" {
		_ = json.Unmarshal([]byte(releasesJSON), &releases)
	}

	h.render(w, "music_artist_external.html", map[string]any{
		"Breadcrumb":   []crumb{bcHome, bcMusic},
		"Title":        nonEmpty(name, "Artist"),
		"Name":         name,
		"Comment":      comment,
		"Bio":          bio,
		"BioSourceURL": bioURL,
		"ImageURL":     imageURL,
		"Releases":     releases,
		"MBID":         mbid,
	})
}

// fetchExternalArtist is the external_artist_fetch job body: resolve the artist's
// bio + image URL + release-groups and upsert the cache row.
func (h *Handler) fetchExternalArtist(ctx context.Context, m *match.Matcher, mbid string) error {
	meta, err := m.ResolveExternalArtist(ctx, mbid)
	if err != nil {
		return err
	}
	rel, err := json.Marshal(meta.Releases)
	if err != nil {
		rel = []byte("[]")
	}
	_, err = h.db.ExecContext(ctx, `
INSERT INTO external_artists(mbid, name, bio, bio_source_url, image_url, releases_json, fetched_at)
VALUES(?,?,?,?,?,?,datetime('now'))
ON CONFLICT(mbid) DO UPDATE SET
  name=excluded.name, bio=excluded.bio, bio_source_url=excluded.bio_source_url,
  image_url=excluded.image_url, releases_json=excluded.releases_json, fetched_at=excluded.fetched_at`,
		mbid, meta.Name, meta.Bio, meta.BioSourceURL, meta.ImageURL, string(rel))
	return err
}

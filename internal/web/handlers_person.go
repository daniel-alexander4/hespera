package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"hespera/internal/pathguard"
	"hespera/internal/tmdb"
)

// wikiBioAttribution matches TMDB's trailing Wikipedia attribution sentence,
// which it appends to Wikipedia-sourced person bios (the title is preceded by a
// regular and/or non-breaking space; \x{00a0} covers the NBSP that \s doesn't).
var wikiBioAttribution = regexp.MustCompile(`(?s)[\s\x{00a0}]*Description above from the Wikipedia article[\s\x{00a0}]+(.+?),[\s\x{00a0}]*licensed under CC-BY-SA,[\s\x{00a0}]*full list of contributors on Wikipedia\.?[\s\x{00a0}]*$`)

// splitWikipediaBio strips that attribution sentence from a person bio and
// returns the cleaned prose plus a link to the source article, so the page shows
// the bio and a single "Read more on Wikipedia" link instead of the raw notice.
func splitWikipediaBio(bio string) (clean, wikipediaURL string) {
	m := wikiBioAttribution.FindStringSubmatchIndex(bio)
	if m == nil {
		return bio, ""
	}
	clean = strings.TrimSpace(bio[:m[0]])
	title := strings.Join(strings.Fields(bio[m[2]:m[3]]), "_") // Fields splits on NBSP too
	if title == "" {
		return clean, ""
	}
	return clean, "https://en.wikipedia.org/wiki/" + url.PathEscape(title)
}

// tmdbPosterBase is the TMDB image base for hotlinked posters of shows not in
// the local library (the actor's wider filmography). These are external
// discovery thumbnails, not the user's media, so they load directly from TMDB
// rather than the download-to-disk /art pipeline.
const tmdbPosterBase = "https://image.tmdb.org/t/p/w342"

// enqueueMetaFetch enqueues a one-off background TMDB metadata fetch (cast list,
// actor bio), deduped by key so a cache-miss page view fires at most one job per
// entity while it's queued/running. No-op without a TMDB key. Page handlers call
// this instead of fetching inline, so request handling never blocks on network.
func (h *Handler) enqueueMetaFetch(ctx context.Context, dedupeKey, jobType string, run func(ctx context.Context, m *tmdb.Matcher) error) {
	tmdbKey := h.effectiveTMDBKey(ctx)
	if tmdbKey == "" {
		return
	}
	if _, busy := h.metaFetch.LoadOrStore(dedupeKey, true); busy {
		return
	}
	matcher := tmdb.NewMatcher(h.db, tmdbKey, h.cfg.DataDir)
	_, err := h.jobs.Enqueue(jobType, 0, "system", func(jctx context.Context, jobID, libID int64) error {
		defer h.metaFetch.Delete(dedupeKey)
		return run(jctx, matcher)
	})
	if err != nil {
		h.metaFetch.Delete(dedupeKey)
		slog.Warn("enqueue meta fetch", "job", jobType, "key", dedupeKey, "err", err)
	}
}

// enqueueMovieMetaFetch is the movie twin of enqueueMetaFetch: same dedupe/no-key
// guard and job plumbing, but it builds a *movie*-configured matcher
// (NewMovieMatcher) so art downloads land in thumbs/movies — using the TV matcher
// here would write posters to thumbs/tv and expose them to the TV thumbgc sweep.
func (h *Handler) enqueueMovieMetaFetch(ctx context.Context, dedupeKey, jobType string, run func(ctx context.Context, m *tmdb.Matcher) error) {
	tmdbKey := h.effectiveTMDBKey(ctx)
	if tmdbKey == "" {
		return
	}
	if _, busy := h.metaFetch.LoadOrStore(dedupeKey, true); busy {
		return
	}
	matcher := tmdb.NewMovieMatcher(h.db, tmdbKey, h.cfg.DataDir)
	_, err := h.jobs.Enqueue(jobType, 0, "system", func(jctx context.Context, jobID, libID int64) error {
		defer h.metaFetch.Delete(dedupeKey)
		return run(jctx, matcher)
	})
	if err != nil {
		h.metaFetch.Delete(dedupeKey)
		slog.Warn("enqueue movie meta fetch", "job", jobType, "key", dedupeKey, "err", err)
	}
}

type castMemberRow struct {
	PersonID  int64
	Name      string
	Character string
	HasArt    bool
}

// loadSeriesCast returns a matched series' cached cast (top-billed first).
func (h *Handler) loadSeriesCast(ctx context.Context, seriesID int) []castMemberRow {
	rows, err := h.db.QueryContext(ctx, `
SELECT p.tmdb_id, p.name, c.character_name, (p.art_path != '')
FROM credits c
JOIN people p ON p.tmdb_id = c.person_id
WHERE c.media_type='tv' AND c.media_id=?
ORDER BY c.billing_order
LIMIT 20
`, seriesID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []castMemberRow
	for rows.Next() {
		var cm castMemberRow
		var hasArt int
		if err := rows.Scan(&cm.PersonID, &cm.Name, &cm.Character, &hasArt); err != nil {
			return out
		}
		cm.HasArt = hasArt != 0
		out = append(out, cm)
	}
	return out
}

// metaMarkerExists reports whether a tv_series_metadata_cache marker row exists,
// used to gate lazy one-time background backfills so they don't re-enqueue on
// every page view (including for shows that genuinely have nothing to fetch).
func (h *Handler) metaMarkerExists(ctx context.Context, entityKey string) bool {
	var x int
	return h.db.QueryRowContext(ctx,
		"SELECT 1 FROM tv_series_metadata_cache WHERE entity_key=? AND lang='en'",
		entityKey).Scan(&x) == nil
}

// castFetched reports whether a series' cast fetch has run (the marker exists).
func (h *Handler) castFetched(ctx context.Context, seriesID int) bool {
	return h.metaMarkerExists(ctx, fmt.Sprintf("show:%d:cast", seriesID))
}

// movieCastFetched reports whether a film's cast fetch has run. The marker lives
// in movie_metadata_cache (its own table, conflict target entity_key — no lang),
// so it can't reuse the tv_series_metadata_cache-specific metaMarkerExists.
func (h *Handler) movieCastFetched(ctx context.Context, tmdbID int) bool {
	var x int
	return h.db.QueryRowContext(ctx,
		"SELECT 1 FROM movie_metadata_cache WHERE entity_key=?",
		fmt.Sprintf("movie:%d:cast", tmdbID)).Scan(&x) == nil
}

// filmographyRow is one credit card on the actor page (TV or film). Owned titles
// link into the library with local art; un-owned ones hotlink a TMDB poster.
type filmographyRow struct {
	ID        int
	Title     string
	Year      string
	Character string
	Owned     bool
	PosterURL string // hotlinked TMDB thumbnail (un-owned only), else ""
}

// loadPersonOwnedIDs returns the set of TMDB ids of the given media_type that
// this person is cast in AND that are matched in the library, so the filmography
// can link those locally instead of hotlinking. Keyed per media-type because a
// TV series id and a movie id can collide as integers.
func (h *Handler) loadPersonOwnedIDs(ctx context.Context, personID int64, query string) map[int]bool {
	owned := map[int]bool{}
	rows, err := h.db.QueryContext(ctx, query, personID)
	if err != nil {
		return owned
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		if rows.Scan(&id) == nil {
			owned[id] = true
		}
	}
	return owned
}

const personOwnedTVQuery = `
SELECT DISTINCT CAST(i.series_id AS INTEGER)
FROM credits c
JOIN tv_series_identities i ON CAST(i.series_id AS INTEGER) = c.media_id
WHERE c.person_id=? AND c.media_type='tv' AND i.status='matched' AND i.series_id != ''`

const personOwnedMovieQuery = `
SELECT DISTINCT mf.tmdb_id
FROM credits c
JOIN movie_files mf ON mf.tmdb_id = c.media_id
WHERE c.person_id=? AND c.media_type='movie' AND mf.match_status='matched' AND mf.tmdb_id != 0`

// buildFilmography splits a cached combined-credits blob into TV and film rows,
// marking each Owned against the per-type ownership sets. An old tv_credits blob
// (no media_type) renders entirely as TV (back-compat) until the lazy re-fetch
// upgrades it.
func buildFilmography(filmographyJSON string, ownedTV, ownedMovie map[int]bool) (tv, films []filmographyRow) {
	if filmographyJSON == "" {
		return nil, nil
	}
	var credits []tmdb.PersonCredit
	if json.Unmarshal([]byte(filmographyJSON), &credits) != nil {
		return nil, nil
	}
	for _, c := range credits {
		row := filmographyRow{ID: c.ID, Title: c.CreditTitle(), Year: c.CreditYear(), Character: c.Character}
		if c.IsMovie() {
			row.Owned = ownedMovie[c.ID]
			if !row.Owned && c.PosterPath != "" {
				row.PosterURL = tmdbPosterBase + c.PosterPath
			}
			films = append(films, row)
		} else {
			row.Owned = ownedTV[c.ID]
			if !row.Owned && c.PosterPath != "" {
				row.PosterURL = tmdbPosterBase + c.PosterPath
			}
			tv = append(tv, row)
		}
	}
	return tv, films
}

// filmographyNeedsUpgrade reports whether a cached blob warrants a one-time lazy
// re-fetch of combined credits. Two cases: an empty-string blob (""), which means
// the filmography was never successfully written — e.g. an actor fetched before
// the filmography_json column existed (the column is NOT NULL DEFAULT '', so a
// failed/absent write leaves ''); or a non-trivial tv_credits-shaped blob with no
// media_type (predates combined credits). Loop-proof: a re-fetch result is always
// a combined blob (carries media_type → stops) or "[]" (len 2 ≠ "" → stops), never
// "" again, so it fires at most once per actor (a failed fetch stays '' and
// correctly retries). "[]"/"null" return false — already fetched, no credits.
func filmographyNeedsUpgrade(filmographyJSON string) bool {
	fj := strings.TrimSpace(filmographyJSON)
	return fj == "" || (len(fj) > 4 && !strings.Contains(fj, "media_type"))
}

// personDetail renders an actor page: bio + image + their full filmography split
// into TV Shows and Films, owned titles linking into the library. Bio/image/
// filmography are fetched lazily in the background on first view.
func (h *Handler) personDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	personID, err := pathID(r, "/person/")
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var name, bio, bioFetchedAt string
	var artPath, filmographyJSON sql.NullString
	hasRow := h.db.QueryRowContext(r.Context(),
		"SELECT name, art_path, bio, bio_fetched_at, filmography_json FROM people WHERE tmdb_id=?", personID,
	).Scan(&name, &artPath, &bio, &bioFetchedAt, &filmographyJSON) == nil

	fj := scanNullString(filmographyJSON)
	// Lazily fetch the bio (image + filmography) the first time, and re-fetch once
	// to upgrade a pre-combined-credits blob to the full TV+film set — in the
	// background. The upgraded blob carries media_type, so it won't re-trigger.
	if !hasRow || bioFetchedAt == "" || filmographyNeedsUpgrade(fj) {
		pid := int(personID)
		h.enqueueMetaFetch(r.Context(), fmt.Sprintf("person:%d", pid), "person_fetch",
			func(ctx context.Context, m *tmdb.Matcher) error { return m.FetchPersonBio(ctx, pid) })
	}

	ownedTV := h.loadPersonOwnedIDs(r.Context(), personID, personOwnedTVQuery)
	ownedMovie := h.loadPersonOwnedIDs(r.Context(), personID, personOwnedMovieQuery)
	tvCredits, filmCredits := buildFilmography(fj, ownedTV, ownedMovie)

	cleanBio, wikipediaURL := splitWikipediaBio(bio)

	h.render(w, "person.html", map[string]any{
		"Title":        nonEmpty(name, "Actor"),
		"PersonID":     personID,
		"Name":         name,
		"HasArt":       scanNullString(artPath) != "",
		"Bio":          cleanBio,
		"WikipediaURL": wikipediaURL,
		"TVCredits":    tvCredits,
		"FilmCredits":  filmCredits,
	})
}

// personArt serves an actor's cached profile image from disk.
func (h *Handler) personArt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	personID, err := pathID(r, "/art/person/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var artPath sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT art_path FROM people WHERE tmdb_id=?", personID,
	).Scan(&artPath); err != nil {
		http.NotFound(w, r)
		return
	}
	ap := scanNullString(artPath)
	if ap == "" {
		http.NotFound(w, r)
		return
	}
	clean, err := pathguard.ResolveExistingUnderRoot(filepath.Clean(h.cfg.DataDir), ap)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", artMIMEFromExt(clean))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(w, f)
}

// nonEmpty returns s, or def when s is empty.
func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"hespera/internal/pathguard"
	"hespera/internal/tmdb"
)

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

// castFetched reports whether a series' cast fetch has run (the marker exists),
// so the lazy backfill doesn't re-enqueue on every view — including for shows
// that genuinely have no cast.
func (h *Handler) castFetched(ctx context.Context, seriesID int) bool {
	var x int
	err := h.db.QueryRowContext(ctx,
		"SELECT 1 FROM tv_series_metadata_cache WHERE entity_key=? AND lang='en'",
		fmt.Sprintf("show:%d:cast", seriesID)).Scan(&x)
	return err == nil
}

type personTitleRow struct {
	SeriesID   string
	Name       string
	PosterPath string
	Character  string
}

// loadPersonTitles returns the matched, in-library TV series this person appears
// in (the "other shows" list), with the character they played in each.
func (h *Handler) loadPersonTitles(ctx context.Context, personID int64) []personTitleRow {
	rows, err := h.db.QueryContext(ctx, `
SELECT i.series_id, c.character_name
FROM credits c
JOIN tv_series_identities i ON CAST(i.series_id AS INTEGER) = c.media_id
WHERE c.person_id=? AND c.media_type='tv' AND i.status='matched' AND i.series_id != ''
GROUP BY i.series_id
`, personID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	chars := map[string]string{}
	var ids []string
	for rows.Next() {
		var sid, ch string
		if err := rows.Scan(&sid, &ch); err != nil {
			return nil
		}
		if _, seen := chars[sid]; !seen {
			ids = append(ids, sid)
		}
		chars[sid] = ch
	}
	if len(ids) == 0 {
		return nil
	}
	metas := h.loadShowMetaSummaries(ctx, ids)
	out := make([]personTitleRow, 0, len(ids))
	for _, sid := range ids {
		meta := metas[sid]
		name := meta.name
		if name == "" {
			name = "Unknown Series (TMDB " + sid + ")"
		}
		out = append(out, personTitleRow{
			SeriesID:   sid,
			Name:       name,
			PosterPath: meta.posterPath,
			Character:  chars[sid],
		})
	}
	return out
}

type otherShowRow struct {
	Name      string
	Year      string
	Character string
	PosterURL string // hotlinked TMDB thumbnail, or "" for none
}

// buildOtherShows turns a cached filmography JSON blob into the actor's shows
// that are NOT in the local library, with hotlinked TMDB poster thumbnails.
func buildOtherShows(filmographyJSON string, inLibrary map[string]bool) []otherShowRow {
	if filmographyJSON == "" {
		return nil
	}
	var credits []tmdb.PersonTVCredit
	if json.Unmarshal([]byte(filmographyJSON), &credits) != nil {
		return nil
	}
	var out []otherShowRow
	for _, c := range credits {
		if inLibrary[strconv.Itoa(c.ID)] {
			continue
		}
		year := ""
		if len(c.FirstAirDate) >= 4 {
			year = c.FirstAirDate[:4]
		}
		poster := ""
		if c.PosterPath != "" {
			poster = tmdbPosterBase + c.PosterPath
		}
		out = append(out, otherShowRow{Name: c.Name, Year: year, Character: c.Character, PosterURL: poster})
	}
	return out
}

// personDetail renders an actor page: bio + image + the in-library titles they
// appear in + their other (out-of-library) shows. The bio/image/filmography are
// fetched lazily in the background on first view.
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

	// Lazily fetch the bio (image + filmography) the first time, in the background.
	if !hasRow || bioFetchedAt == "" {
		pid := int(personID)
		h.enqueueMetaFetch(r.Context(), fmt.Sprintf("person:%d", pid), "person_fetch",
			func(ctx context.Context, m *tmdb.Matcher) error { return m.FetchPersonBio(ctx, pid) })
	}

	titles := h.loadPersonTitles(r.Context(), personID)
	inLib := make(map[string]bool, len(titles))
	for _, t := range titles {
		inLib[t.SeriesID] = true
	}

	h.render(w, "person.html", map[string]any{
		"Title":      nonEmpty(name, "Actor"),
		"PersonID":   personID,
		"Name":       name,
		"HasArt":     scanNullString(artPath) != "",
		"Bio":        bio,
		"Titles":     titles,
		"OtherShows": buildOtherShows(scanNullString(filmographyJSON), inLib),
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

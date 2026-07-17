package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Global search — the jump-to palette ("/" anywhere). One GET endpoint fans
// out over the library's entity types and returns fully-shaped rows (href +
// text + context), so result URLs have exactly one owner. Sections are capped
// at searchSectionCap rows; per-section ranking is prefix-match-first then
// name — no clever scoring, the fixed section order does the rest. All plain
// LIKE scans: measured fine to thousands of titles (see the browse-scale
// harness); the shows/movies lookups reuse the canonical browse bases rather
// than growing a second json_extract copy.

const searchSectionCap = 5

type searchRow struct {
	Href    string `json:"href"`
	Text    string `json:"text"`
	Context string `json:"context,omitempty"`
}

type searchSection struct {
	Label string      `json:"label"`
	Rows  []searchRow `json:"rows"`
}

func (h *Handler) searchAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	sections := []searchSection{}
	if len([]rune(q)) >= 2 {
		sections = h.searchSections(r.Context(), q)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sections": sections})
}

func (h *Handler) searchSections(ctx context.Context, q string) []searchSection {
	like := "%" + strings.ToLower(q) + "%"
	prefix := strings.ToLower(q) + "%"
	var sections []searchSection
	add := func(label string, rows []searchRow) {
		if len(rows) > 0 {
			sections = append(sections, searchSection{Label: label, Rows: rows})
		}
	}

	// rank orders prefix matches first, then alphabetically. Every query below
	// embeds it with (prefix, like) bound in that order after its own args.
	const rank = " ORDER BY CASE WHEN %s LIKE ? THEN 0 ELSE 1 END, %s LIMIT ?"

	// Artists.
	add("Artists", h.searchRows(ctx, func(scan func(...any) error) (searchRow, error) {
		var id int64
		var name string
		if err := scan(&id, &name); err != nil {
			return searchRow{}, err
		}
		return searchRow{Href: fmt.Sprintf("/music/artist/%d", id), Text: name}, nil
	}, `SELECT id, name FROM music_artists WHERE lower(name) LIKE ?`+
		fmt.Sprintf(rank, "lower(name)", "lower(name)"), like, prefix, searchSectionCap))

	// Albums (artist + year as context).
	add("Albums", h.searchRows(ctx, func(scan func(...any) error) (searchRow, error) {
		var id int64
		var title, artist string
		var year int
		if err := scan(&id, &title, &artist, &year); err != nil {
			return searchRow{}, err
		}
		ctxStr := artist
		if year > 0 {
			ctxStr = fmt.Sprintf("%s · %d", artist, year)
		}
		return searchRow{Href: fmt.Sprintf("/music/album/%d", id), Text: title, Context: ctxStr}, nil
	}, `SELECT al.id, al.title, ar.name, al.year FROM music_albums al JOIN music_artists ar ON ar.id = al.artist_id
WHERE lower(al.title) LIKE ?`+fmt.Sprintf(rank, "lower(al.title)", "lower(al.title)"), like, prefix, searchSectionCap))

	// Songs — the one section that ACTS: Enter starts playback at the track.
	add("Songs", h.searchRows(ctx, func(scan func(...any) error) (searchRow, error) {
		var id, albumID int64
		var title, artist string
		if err := scan(&id, &albumID, &title, &artist); err != nil {
			return searchRow{}, err
		}
		return searchRow{Href: fmt.Sprintf("/music/player?album=%d&track=%d", albumID, id), Text: title, Context: artist}, nil
	}, `SELECT t.id, t.album_id, t.title, ar.name FROM music_tracks t JOIN music_artists ar ON ar.id = t.artist_id
WHERE lower(t.title) LIKE ?`+fmt.Sprintf(rank, "lower(t.title)", "lower(t.title)"), like, prefix, searchSectionCap))

	// TV shows — via the canonical browse base (name lives in the metadata cache).
	add("TV Shows", h.searchRows(ctx, func(scan func(...any) error) (searchRow, error) {
		var id, name, firstAir string
		if err := scan(&id, &name, &firstAir); err != nil {
			return searchRow{}, err
		}
		year := ""
		if len(firstAir) >= 4 {
			year = firstAir[:4]
		}
		return searchRow{Href: "/tv/series/" + id, Text: name, Context: year}, nil
	}, `SELECT s.series_id, s.name, s.first_air `+tvSeriesListBase+
		` WHERE s.name != '' AND lower(s.name) LIKE ?`+fmt.Sprintf(rank, "lower(s.name)", "lower(s.name)"), like, prefix, searchSectionCap))

	// Movies — same canonical-base reuse.
	add("Movies", h.searchRows(ctx, func(scan func(...any) error) (searchRow, error) {
		var tmdbID int64
		var title, release string
		if err := scan(&tmdbID, &title, &release); err != nil {
			return searchRow{}, err
		}
		year := ""
		if len(release) >= 4 {
			year = release[:4]
		}
		return searchRow{Href: fmt.Sprintf("/movie/%d", tmdbID), Text: title, Context: year}, nil
	}, `SELECT s.tmdb_id, s.title, s.release_date `+movieListBase+
		` WHERE s.title != '' AND lower(s.title) LIKE ?`+fmt.Sprintf(rank, "lower(s.title)", "lower(s.title)"), like, prefix, searchSectionCap))

	// Books — matched by title or author; the author shows as context.
	add("Books", h.searchRows(ctx, func(scan func(...any) error) (searchRow, error) {
		var id int64
		var title, author string
		if err := scan(&id, &title, &author); err != nil {
			return searchRow{}, err
		}
		return searchRow{Href: fmt.Sprintf("/books/view?id=%d", id), Text: title, Context: author}, nil
	}, `SELECT id, title, author FROM books
		WHERE title != '' AND (lower(title) LIKE ? OR lower(author) LIKE ?)`+
		fmt.Sprintf(rank, "lower(title)", "lower(title)"), like, like, prefix, searchSectionCap))

	// People: actor names, plus character names resolved to the actor who
	// played them ("as Character in Title") — credits already store both.
	people := h.searchRows(ctx, func(scan func(...any) error) (searchRow, error) {
		var id int64
		var name string
		if err := scan(&id, &name); err != nil {
			return searchRow{}, err
		}
		return searchRow{Href: fmt.Sprintf("/person/%d", id), Text: name}, nil
	}, `SELECT tmdb_id, name FROM people WHERE lower(name) LIKE ?`+
		fmt.Sprintf(rank, "lower(name)", "lower(name)"), like, prefix, searchSectionCap)
	people = append(people, h.searchRows(ctx, func(scan func(...any) error) (searchRow, error) {
		var personID int64
		var person, character, title string
		if err := scan(&personID, &person, &character, &title); err != nil {
			return searchRow{}, err
		}
		ctxStr := "as " + character
		if title != "" {
			ctxStr += " in " + title
		}
		return searchRow{Href: fmt.Sprintf("/person/%d", personID), Text: person, Context: ctxStr}, nil
	}, `SELECT c.person_id, p.name, c.character_name,
       COALESCE(
         (SELECT json_extract(tc.payload_json, '$.name') FROM tv_series_metadata_cache tc
          WHERE c.media_type = 'tv' AND tc.entity_key = 'show:' || c.media_id AND tc.lang = 'en'),
         (SELECT json_extract(mc.payload_json, '$.title') FROM movie_metadata_cache mc
          WHERE c.media_type = 'movie' AND mc.entity_key = 'movie:' || c.media_id),
         '')
FROM credits c JOIN people p ON p.tmdb_id = c.person_id
WHERE c.character_name != '' AND lower(c.character_name) LIKE ?`+
		fmt.Sprintf(rank, "lower(c.character_name)", "lower(c.character_name)"), like, prefix, searchSectionCap)...)
	if len(people) > searchSectionCap {
		people = people[:searchSectionCap]
	}
	add("People", people)

	// Band members etc. have no structured home — they live in the Wikipedia
	// bio prose. A labeled fuzzy section, deduped against direct name hits.
	add("Mentioned in artist bios", h.searchRows(ctx, func(scan func(...any) error) (searchRow, error) {
		var id int64
		var name string
		if err := scan(&id, &name); err != nil {
			return searchRow{}, err
		}
		return searchRow{Href: fmt.Sprintf("/music/artist/%d", id), Text: name, Context: "mentioned in bio"}, nil
	}, `SELECT id, name FROM music_artists WHERE bio != '' AND lower(bio) LIKE ? AND lower(name) NOT LIKE ?
ORDER BY lower(name) LIMIT ?`, like, like, searchSectionCap))

	return sections
}

// searchRows runs one section query, mapping rows through build; best-effort —
// a failing section is simply absent, never a 500 for the whole palette.
func (h *Handler) searchRows(ctx context.Context, build func(scan func(...any) error) (searchRow, error), query string, args ...any) []searchRow {
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []searchRow
	for rows.Next() {
		r, err := build(rows.Scan)
		if err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}

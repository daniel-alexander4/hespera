package web

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

// The integrity report page: the drill-down behind the Libraries page's
// corrupt pill. Lists every file the integrity checks marked, split into two
// sections by severity — 'flagged' (unplayable-grade damage: decode errors or
// a failed container repair; needs replacement) and 'degraded' (audio-gap-only
// residue on a sound container: plays cleanly because the transcoder
// silence-fills the hole; replacement optional). Each row carries the resolved
// title, the on-disk path, what exactly is wrong (integrity_detail), when it
// was checked, a plain-language mitigation, and a link to the owning
// season/movie/album page.

// integrityReportRow is one damaged file on the report.
type integrityReportRow struct {
	Title      string // resolved display title (series SxE / film / artist — track)
	Subtitle   string // secondary context (episode name source, album, year)
	Path       string
	SizeBytes  int64
	Detail     string // integrity_detail — the stored reason
	CheckedAt  string
	Mitigation string
	Href       string // owning season/movie/album page ("" when unmatched)
	Degraded   bool   // integrity_status=='degraded' (playable residue) vs 'flagged'
}

// mitigationFor derives the plain-language "what can I do about it" line from
// the classification. Kept beside the row builder so the vocabulary tracks
// internal/integrity's detail strings.
func mitigationFor(status, detail string) string {
	if status == "degraded" {
		return "Plays normally — the transcoder fills the missing audio with silence. Replace the file from another source to restore the lost audio; a rescan re-checks the new file automatically."
	}
	switch {
	case strings.Contains(detail, "bitstream corruption"):
		return "Damaged frames in the stream — artifacts are possible where they occur, and the lost data is unrecoverable. Replace the file from another source; a rescan re-checks the new file automatically."
	case strings.Contains(detail, "repair failed"):
		return "The container is damaged and auto-repair could not produce a verified replacement. Replace the file from another source; a rescan re-checks the new file automatically."
	default:
		return "Unrepairable damage. Replace the file from another source; a rescan re-checks the new file automatically."
	}
}

// librariesIntegrityReport renders GET /libraries/integrity-report?id=N.
func (h *Handler) librariesIntegrityReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	libID, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || libID <= 0 {
		http.Error(w, "invalid library id", http.StatusBadRequest)
		return
	}
	var libName, libType string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT name, type FROM libraries WHERE id=?", libID).Scan(&libName, &libType); err != nil {
		http.NotFound(w, r)
		return
	}

	var rows []integrityReportRow
	switch libType {
	case "tv":
		rows, err = h.integrityReportTV(r.Context(), libID)
	case "movies":
		rows, err = h.integrityReportMovies(r.Context(), libID)
	case "music":
		rows, err = h.integrityReportMusic(r.Context(), libID)
	default:
		http.Error(w, "library type has no integrity data", http.StatusBadRequest)
		return
	}
	if err != nil {
		httpError(w, 500, "internal server error", "integrity report failed", "handler", "librariesIntegrityReport", "err", err)
		return
	}

	var flagged, degraded []integrityReportRow
	for _, row := range rows {
		if row.Degraded {
			degraded = append(degraded, row)
		} else {
			flagged = append(flagged, row)
		}
	}

	h.render(w, "integrity_report.html", map[string]any{
		"Breadcrumb":  []crumb{bcHome, bcSettings, {Label: "Libraries", Href: "/libraries"}},
		"Title":       "Integrity report",
		"LibraryName": libName,
		"Flagged":     flagged,
		"Degraded":    degraded,
	})
}

// integrityReportTV builds the report rows for a TV library: file → identity →
// resolved series name + SxE, linking to the owning season page when matched.
func (h *Handler) integrityReportTV(ctx context.Context, libID int64) ([]integrityReportRow, error) {
	rs, err := h.db.QueryContext(ctx, `
SELECT f.abs_path, f.file_size_bytes, f.integrity_status, f.integrity_detail, f.integrity_checked_at,
       COALESCE(i.status,''), COALESCE(i.series_id,''), COALESCE(i.season_number,-1),
       COALESCE(i.episode_numbers_csv,''), COALESCE(i.guessed_title,'')
FROM tv_series_files f
LEFT JOIN tv_series_identities i ON i.file_id = f.id
WHERE f.library_id=? AND f.integrity_status IN ('flagged','degraded')
ORDER BY i.series_id, i.season_number, i.episode_numbers_csv, f.abs_path`, libID)
	if err != nil {
		return nil, err
	}
	defer rs.Close()

	type tvRow struct {
		row                           integrityReportRow
		matchStatus, seriesID, epsCSV string
		seasonNumber                  int
		guessedTitle                  string
	}
	var raw []tvRow
	seriesIDs := map[string]bool{}
	for rs.Next() {
		var t tvRow
		var status string
		if err := rs.Scan(&t.row.Path, &t.row.SizeBytes, &status, &t.row.Detail, &t.row.CheckedAt,
			&t.matchStatus, &t.seriesID, &t.seasonNumber, &t.epsCSV, &t.guessedTitle); err != nil {
			return nil, err
		}
		t.row.Mitigation = mitigationFor(status, t.row.Detail)
		t.row.Degraded = status == "degraded"
		if t.seriesID != "" {
			seriesIDs[t.seriesID] = true
		}
		raw = append(raw, t)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(seriesIDs))
	for id := range seriesIDs {
		ids = append(ids, id)
	}
	metas := h.loadShowMetaSummaries(ctx, ids)

	out := make([]integrityReportRow, 0, len(raw))
	for _, t := range raw {
		row := t.row
		name := metas[t.seriesID].name
		if name == "" {
			name = t.guessedTitle
		}
		if name == "" {
			name = filepath.Base(row.Path)
		}
		if t.seasonNumber >= 0 && t.epsCSV != "" {
			row.Title = fmt.Sprintf("%s — S%02dE%s", name, t.seasonNumber, t.epsCSV)
		} else {
			row.Title = name
		}
		if t.matchStatus == "matched" && t.seriesID != "" && t.seasonNumber >= 0 {
			row.Href = fmt.Sprintf("/tv/season/?series=%s&season=%d", t.seriesID, t.seasonNumber)
		}
		out = append(out, row)
	}
	return out, nil
}

// integrityReportMovies builds the report rows for a movie library.
func (h *Handler) integrityReportMovies(ctx context.Context, libID int64) ([]integrityReportRow, error) {
	rs, err := h.db.QueryContext(ctx, `
SELECT abs_path, file_size_bytes, integrity_status, integrity_detail, integrity_checked_at,
       tmdb_id, match_status, guessed_title, year
FROM movie_files
WHERE library_id=? AND integrity_status IN ('flagged','degraded')
ORDER BY guessed_title, abs_path`, libID)
	if err != nil {
		return nil, err
	}
	defer rs.Close()

	type mvRow struct {
		row          integrityReportRow
		tmdbID, year int
		matchStatus  string
		guessedTitle string
	}
	var raw []mvRow
	idSet := map[int]bool{}
	for rs.Next() {
		var m mvRow
		var status string
		if err := rs.Scan(&m.row.Path, &m.row.SizeBytes, &status, &m.row.Detail, &m.row.CheckedAt,
			&m.tmdbID, &m.matchStatus, &m.guessedTitle, &m.year); err != nil {
			return nil, err
		}
		m.row.Mitigation = mitigationFor(status, m.row.Detail)
		m.row.Degraded = status == "degraded"
		if m.tmdbID != 0 {
			idSet[m.tmdbID] = true
		}
		raw = append(raw, m)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}

	ids := make([]int, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	metas := h.loadMovieMetaSummaries(ctx, ids)

	out := make([]integrityReportRow, 0, len(raw))
	for _, m := range raw {
		row := m.row
		title, year := metas[m.tmdbID].title, metas[m.tmdbID].year
		if title == "" {
			title = m.guessedTitle
			if m.year != 0 {
				year = strconv.Itoa(m.year)
			}
		}
		if title == "" {
			title = filepath.Base(row.Path)
		}
		row.Title = title
		row.Subtitle = year
		if m.matchStatus == "matched" && m.tmdbID != 0 {
			row.Href = fmt.Sprintf("/movie/%d", m.tmdbID)
		}
		out = append(out, row)
	}
	return out, nil
}

// integrityReportMusic builds the report rows for a music library.
func (h *Handler) integrityReportMusic(ctx context.Context, libID int64) ([]integrityReportRow, error) {
	rs, err := h.db.QueryContext(ctx, `
SELECT t.abs_path, t.file_size_bytes, t.integrity_status, t.integrity_detail, t.integrity_checked_at,
       t.title, ar.name, al.id, al.title
FROM music_tracks t
JOIN music_artists ar ON ar.id = t.artist_id
JOIN music_albums al ON al.id = t.album_id
WHERE t.library_id=? AND t.integrity_status IN ('flagged','degraded')
ORDER BY ar.name, al.title, t.disc_no, t.track_no`, libID)
	if err != nil {
		return nil, err
	}
	defer rs.Close()

	var out []integrityReportRow
	for rs.Next() {
		var row integrityReportRow
		var status, trackTitle, artistName, albumTitle string
		var albumID int64
		if err := rs.Scan(&row.Path, &row.SizeBytes, &status, &row.Detail, &row.CheckedAt,
			&trackTitle, &artistName, &albumID, &albumTitle); err != nil {
			return nil, err
		}
		row.Title = artistName + " — " + trackTitle
		row.Subtitle = albumTitle
		row.Mitigation = mitigationFor(status, row.Detail)
		row.Degraded = status == "degraded"
		row.Href = fmt.Sprintf("/music/album/%d", albumID)
		out = append(out, row)
	}
	return out, rs.Err()
}

// integrityStatusCounts returns per-library counts of files carrying the given
// integrity status, summed across the three media tables. Used by the
// Libraries page for the corrupt ('flagged') pill and the degraded link.
func (h *Handler) integrityStatusCounts(ctx context.Context, status string) map[int64]int {
	out := map[int64]int{}
	for _, tbl := range []string{"tv_series_files", "movie_files", "music_tracks"} {
		rows, err := h.db.QueryContext(ctx,
			"SELECT library_id, COUNT(*) FROM "+tbl+" WHERE integrity_status=? GROUP BY library_id", status)
		if err != nil {
			continue
		}
		for rows.Next() {
			var lib int64
			var n int
			if rows.Scan(&lib, &n) == nil {
				out[lib] += n
			}
		}
		rows.Close()
	}
	return out
}

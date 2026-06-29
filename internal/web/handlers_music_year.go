package web

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"hespera/internal/billboard"
	"hespera/internal/match"
)

// defaultJourneyYear is where "Rediscover a Year" lands with no ?y= (Dan's
// birth year). The picker can reach any year the Billboard dataset covers.
const defaultJourneyYear = 1968

// journeyItemView is one acquire-target as the page (and the journey play queue)
// sees it: the stored chart/MB facts plus the view-time reconcile against the
// library (Owned/Played) and its charted-songs panel.
type journeyItemView struct {
	Kind        string // album | single
	ArtistName  string
	Title       string
	DisplayDate string
	ChartPeak   int
	ArtURL      string
	Owned       bool
	Played      bool
	AlbumID     int64 // deep-link target when owned (album, or a single's parent album)
	TrackID     int64 // single's owned track, for queue expansion
	Songs       []journeySong

	sortDate string // internal: effective YYYY-MM-DD used for ordering
}

// journeySong is one Billboard-charting song in an item's panel, with its
// per-song library reconcile: an owned song plays locally, an un-owned one plays
// via YouTube (resolved on click).
type journeySong struct {
	Artist  string
	Title   string
	Peak    int
	Weeks   int
	Owned   bool
	AlbumID int64
	TrackID int64
}

// IsSingle reports whether the target is a standalone charting single.
func (v journeyItemView) IsSingle() bool { return v.Kind == "single" }

// journeyData is the full resolved year-journey: ordered items + derived counts
// and the resume pointer.
type journeyData struct {
	Status      string // building | ready | none
	Items       []journeyItemView
	Total       int
	Owned       int
	Played      int
	ResumeMonth string // month of the first un-played owned item ("" if none/all done)
}

// musicYear renders the "Rediscover a Year" page: the Billboard-charting albums
// and singles of a year as a discovery checklist, reconciled against the library
// at view time. The expensive build (chart → MusicBrainz resolution) runs once
// per year in a background job, enqueued lazily on first view.
func (h *Handler) musicYear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	minY, maxY, ok := billboard.Years()
	if !ok {
		httpError(w, 500, "internal server error", "billboard dataset unavailable", "handler", "musicYear")
		return
	}
	year := defaultJourneyYear
	if y, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("y"))); err == nil && y > 0 {
		year = y
	}
	if year < minY {
		year = minY
	}
	if year > maxY {
		year = maxY
	}

	libraryID := h.resolveMusicLibraryID(r)
	data := h.loadJourney(r.Context(), libraryID, year)

	// Lazily kick off the one-time build whenever this year isn't ready yet.
	if data.Status != "ready" {
		y := year
		h.enqueueMusicFetch(r.Context(), "year-journey:"+strconv.Itoa(y), "year_journey_build",
			func(ctx context.Context, m *match.Matcher) error { return m.BuildYearJourney(ctx, libraryID, y) })
		if data.Status == "none" {
			data.Status = "building"
		}
	}

	acquiredPct := 0
	if data.Total > 0 {
		acquiredPct = data.Owned * 100 / data.Total
	}

	h.render(w, "music_year.html", map[string]any{
		"Title":         "Rediscover " + strconv.Itoa(year),
		"Year":          year,
		"PrevYear":      clampYear(year-1, minY, maxY),
		"NextYear":      clampYear(year+1, minY, maxY),
		"MinYear":       minY,
		"MaxYear":       maxY,
		"Building":      data.Status == "building",
		"Items":         data.Items,
		"Total":         data.Total,
		"Owned":         data.Owned,
		"Played":        data.Played,
		"AcquiredPct":   acquiredPct,
		"ResumeMonth":   data.ResumeMonth,
		"HasOwned":      data.Owned > 0,
		"HasYouTubeKey": h.effectiveYouTubeKey(r.Context()) != "",
	})
}

func clampYear(y, lo, hi int) int {
	if y < lo {
		return lo
	}
	if y > hi {
		return hi
	}
	return y
}

// loadJourney reads the stored journey items for a year and reconciles them
// against the library — ownership by release-group MBID then normalized
// title+artist, listened-state from play_history — then orders them
// chronologically. All derivation is local SQL + the embedded chart index; no
// network. Items are ordered by best-known release date (falling back to the
// act's earliest chart-debut that year), then chart peak, then title.
func (h *Handler) loadJourney(ctx context.Context, libraryID int64, year int) journeyData {
	var status string
	if err := h.db.QueryRowContext(ctx, "SELECT status FROM year_journeys WHERE year=?", year).Scan(&status); err != nil {
		return journeyData{Status: "none"}
	}

	rows, err := h.db.QueryContext(ctx,
		`SELECT kind, artist_name, artist_mbid, title, rg_mbid, release_date, chart_peak, art_url
		 FROM year_journey_items WHERE year=?`, year)
	if err != nil {
		return journeyData{Status: status}
	}
	type rawItem struct {
		kind, artist, mbid, title, rgMBID, releaseDate, artURL string
		peak                                                   int
	}
	var raws []rawItem
	for rows.Next() {
		var it rawItem
		var mbid string
		if err := rows.Scan(&it.kind, &it.artist, &mbid, &it.title, &it.rgMBID, &it.releaseDate, &it.peak, &it.artURL); err != nil {
			continue
		}
		it.mbid = mbid
		raws = append(raws, it)
	}
	rows.Close()

	albumsByMBID, albumsByTA := h.libraryAlbumIndex(ctx, libraryID)
	tracksByTA := h.libraryTrackIndex(ctx, libraryID)
	playedAlbums := h.idSet(ctx, "SELECT DISTINCT album_id FROM play_history WHERE library_id=?", libraryID)
	playedTracks := h.idSet(ctx, "SELECT DISTINCT track_id FROM play_history WHERE library_id=?", libraryID)

	// Charted-songs panel + the earliest-debut ordering fallback, from the chart
	// index keyed by normalized artist name.
	songsByArtist := map[string][]billboard.Song{}
	earliestDebut := map[string]string{}
	for _, a := range billboard.Year(year) {
		key := match.NormalizeForDedup(a.Name)
		songsByArtist[key] = a.Songs
		min := ""
		for _, s := range a.Songs {
			if len(s.Debut) == 10 && (min == "" || s.Debut < min) {
				min = s.Debut
			}
		}
		earliestDebut[key] = min
	}

	out := journeyData{Status: status}
	for _, it := range raws {
		v := journeyItemView{
			Kind:        it.kind,
			ArtistName:  it.artist,
			Title:       it.title,
			DisplayDate: journeyDisplayDate(it.releaseDate),
			ChartPeak:   it.peak,
			ArtURL:      it.artURL,
		}
		akey := match.NormalizeForDedup(it.artist)
		for _, s := range songsByArtist[akey] {
			js := journeySong{Artist: it.artist, Title: s.Title, Peak: s.Peak, Weeks: s.Weeks}
			if tr, ok := tracksByTA[taKey(s.Title, it.artist)]; ok {
				js.Owned, js.TrackID, js.AlbumID = true, tr.id, tr.albumID
			}
			v.Songs = append(v.Songs, js)
		}
		v.sortDate = journeySortDate(it.releaseDate, earliestDebut[akey], year)

		if it.kind == "album" {
			id := int64(0)
			if it.rgMBID != "" {
				id = albumsByMBID[it.rgMBID]
			}
			if id == 0 {
				id = albumsByTA[taKey(it.title, it.artist)]
			}
			if id != 0 {
				v.Owned, v.AlbumID = true, id
				v.Played = playedAlbums[id]
			}
		} else {
			if tr, ok := tracksByTA[taKey(it.title, it.artist)]; ok {
				v.Owned, v.TrackID, v.AlbumID = true, tr.id, tr.albumID
				v.Played = playedTracks[tr.id]
			}
		}

		out.Total++
		if v.Owned {
			out.Owned++
			if v.Played {
				out.Played++
			}
		}
		out.Items = append(out.Items, v)
	}

	sort.SliceStable(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if a.sortDate != b.sortDate {
			return a.sortDate < b.sortDate
		}
		if a.ChartPeak != b.ChartPeak {
			return a.ChartPeak < b.ChartPeak
		}
		return a.Title < b.Title
	})

	// Resume = month of the first owned-but-unplayed item in chronological order.
	for _, v := range out.Items {
		if v.Owned && !v.Played {
			out.ResumeMonth = monthName(v.sortDate)
			break
		}
	}
	return out
}

type trackRef struct {
	id      int64
	albumID int64
}

func (h *Handler) libraryAlbumIndex(ctx context.Context, libraryID int64) (byMBID, byTA map[string]int64) {
	byMBID, byTA = map[string]int64{}, map[string]int64{}
	rows, err := h.db.QueryContext(ctx,
		`SELECT al.id, al.title, al.musicbrainz_id, ar.name
		 FROM music_albums al JOIN music_artists ar ON ar.id=al.artist_id
		 WHERE al.library_id=?`, libraryID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var title, mbid, artist string
		if rows.Scan(&id, &title, &mbid, &artist) != nil {
			continue
		}
		if mbid != "" {
			if _, ok := byMBID[mbid]; !ok {
				byMBID[mbid] = id
			}
		}
		if k := taKey(title, artist); k != "\x1f" {
			if _, ok := byTA[k]; !ok {
				byTA[k] = id
			}
		}
	}
	return
}

func (h *Handler) libraryTrackIndex(ctx context.Context, libraryID int64) map[string]trackRef {
	out := map[string]trackRef{}
	rows, err := h.db.QueryContext(ctx,
		`SELECT t.id, t.album_id, t.title, ar.name
		 FROM music_tracks t JOIN music_artists ar ON ar.id=t.artist_id
		 WHERE t.library_id=?`, libraryID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, albumID int64
		var title, artist string
		if rows.Scan(&id, &albumID, &title, &artist) != nil {
			continue
		}
		if k := taKey(title, artist); k != "\x1f" {
			if _, ok := out[k]; !ok {
				out[k] = trackRef{id: id, albumID: albumID}
			}
		}
	}
	return out
}

func (h *Handler) idSet(ctx context.Context, query string, args ...any) map[int64]bool {
	out := map[int64]bool{}
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out[id] = true
		}
	}
	return out
}

// taKey is the normalized title+artist reconcile key (separated by a unit char
// that can't appear in a normalized string).
func taKey(title, artist string) string {
	return match.NormalizeForDedup(title) + "\x1f" + match.NormalizeForDedup(artist)
}

// journeySortDate resolves an item's effective ordering date (YYYY-MM-DD):
// the release date when it carries month precision, otherwise the act's earliest
// chart-debut that year, otherwise mid-year for a bare year.
func journeySortDate(releaseDate, earliestDebut string, year int) string {
	switch {
	case len(releaseDate) >= 10:
		return releaseDate[:10]
	case len(releaseDate) == 7:
		return releaseDate + "-01"
	}
	if earliestDebut != "" {
		return earliestDebut
	}
	return strconv.Itoa(year) + "-06-30"
}

// journeyDisplayDate is the human label: "Mar 1968" with month precision, else
// the bare year.
func journeyDisplayDate(releaseDate string) string {
	if len(releaseDate) >= 7 {
		if t, err := time.Parse("2006-01", releaseDate[:7]); err == nil {
			return t.Format("Jan 2006")
		}
	}
	if len(releaseDate) >= 4 {
		return releaseDate[:4]
	}
	return ""
}

func monthName(sortDate string) string {
	if t, err := time.Parse("2006-01-02", sortDate); err == nil {
		return t.Format("January")
	}
	return ""
}

// journeyQueueTracks expands the owned items of a year-journey, in the page's
// chronological order, into player tracks: albums expand to all their tracks,
// singles to their one track. Drives source=journey.
func (h *Handler) journeyQueueTracks(ctx context.Context, libraryID int64, year int) ([]trackRow, error) {
	data := h.loadJourney(ctx, libraryID, year)
	out := make([]trackRow, 0, 64)
	for _, v := range data.Items {
		if !v.Owned {
			continue
		}
		var (
			tracks []trackRow
			err    error
		)
		if v.Kind == "album" {
			tracks, err = h.queryPlayerTracks(ctx,
				playerTrackSelect+` WHERE t.album_id=? ORDER BY t.disc_no, t.track_no, lower(t.title)`, v.AlbumID)
		} else {
			tracks, err = h.queryPlayerTracks(ctx, playerTrackSelect+` WHERE t.id=?`, v.TrackID)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, tracks...)
	}
	return out, nil
}

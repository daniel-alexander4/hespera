package web

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hespera/internal/billboard"
	"hespera/internal/match"
)

// defaultJourneyYear is where "Rediscover a Year" lands with no ?y= (Dan's
// birth year). The picker can reach any year the Billboard dataset covers.
const defaultJourneyYear = 1968

// chartCard is one song's placement on one weekly Hot 100, reconciled against
// the library. The week a song first appears that year is its debut: debut cards
// are playable and join the listen-through; later weeks are display-only chart
// context (the song climbing/falling), dimmed and not replayed.
type chartCard struct {
	Pos     int
	Title   string
	Artist  string
	IsDebut bool
	Owned   bool
	AlbumID int64  // owned: deep-link + local /art/album cover
	TrackID int64  // owned: the track to play
	ArtURL  string // un-owned: cached YouTube thumbnail, else "" (placeholder)
}

// weekView is one weekly chart as the page renders it: a date label and the
// week's cards ordered per the display direction.
type weekView struct {
	Date  string // YYYY-MM-DD
	Label string // "Aug 4"
	Cards []chartCard
}

// journeyData is a year resolved for display: every weekly chart (chronological)
// plus distinct-song counts. The listen-through playlist is the debut cards in
// chronological week order, taken in each week's display direction.
type journeyData struct {
	Weeks      []weekView
	TotalSongs int // distinct songs that charted that year (= debuts)
	OwnedSongs int // of those, owned in the library
}

// billboardEnabled reports whether the user has opted into the chart-data
// feature. It's off by default; the Settings toggle that enables it carries a
// licensing notice (the chart data is a third party's intellectual property and
// is fetched at runtime, never shipped).
func (h *Handler) billboardEnabled(ctx context.Context) bool {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='billboard_enabled'").Scan(&v)
	return strings.TrimSpace(v) == "1"
}

// enqueueBillboardFetch kicks the one-time runtime fetch of the weekly chart
// index into DataDir (keyless, deduped). No-op if a fetch is already in flight.
func (h *Handler) enqueueBillboardFetch(ctx context.Context) {
	if _, busy := h.metaFetch.LoadOrStore("billboard:fetch", true); busy {
		return
	}
	_, err := h.jobs.Enqueue("billboard_fetch", 0, "user", func(jctx context.Context, jobID, libID int64) error {
		defer h.metaFetch.Delete("billboard:fetch")
		return billboard.BuildIndex(h.cfg.DataDir, "")
	})
	if err != nil {
		h.metaFetch.Delete("billboard:fetch")
		slog.Warn("enqueue billboard fetch", "err", err)
	}
}

// musicYear renders "Rediscover a Year": the year's full week-by-week chart,
// every song at its position each week, reconciled against the library at view
// time. The chart data is fetched at runtime into DataDir only once the user
// opts in (the feature is off by default), so this gates on that toggle: off →
// an enable prompt; on-but-not-yet-fetched → a one-time background fetch + a
// building state; otherwise the page is built entirely from local data.
func (h *Handler) musicYear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	if !h.billboardEnabled(ctx) {
		h.render(w, "music_year.html", map[string]any{"Title": "Rediscover a Year", "Disabled": true})
		return
	}
	minY, maxY, ok := billboard.Years(h.cfg.DataDir)
	if !ok {
		// Opted in but the data hasn't landed yet — kick the fetch, show building.
		h.enqueueBillboardFetch(ctx)
		h.render(w, "music_year.html", map[string]any{"Title": "Rediscover a Year", "Building": true})
		return
	}
	year := defaultJourneyYear
	if y, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("y"))); err == nil && y > 0 {
		year = y
	}
	year = clampYear(year, minY, maxY)
	topFirst := r.URL.Query().Get("dir") == "top"

	libraryID := h.resolveMusicLibraryID(r)
	data := h.loadJourney(r.Context(), libraryID, year, topFirst)

	// The play/toggle hrefs are built in the template from literal text + values
	// (Year, TopFirst), not a precomputed query string — html/template URL-escapes
	// a whole "a=1&b=2" action (& → %26), which would break the link.
	h.render(w, "music_year.html", map[string]any{
		"Title":         "Rediscover " + strconv.Itoa(year),
		"Year":          year,
		"PrevYear":      clampYear(year-1, minY, maxY),
		"NextYear":      clampYear(year+1, minY, maxY),
		"MinYear":       minY,
		"MaxYear":       maxY,
		"Weeks":         data.Weeks,
		"TotalSongs":    data.TotalSongs,
		"OwnedSongs":    data.OwnedSongs,
		"TopFirst":      topFirst,
		"HasOwned":      data.OwnedSongs > 0,
		"YouTubeInApp": h.effectiveYouTubeInApp(r.Context()),
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

// loadJourney builds a year's weekly charts from the embedded grid and
// reconciles every entry against the library — ownership by normalized
// title+artist, un-owned art from a cached YouTube thumbnail if one exists.
// Debut (first chronological appearance) is computed once and is independent of
// the display direction; weeks are always returned chronologically, only the
// cards within a week are ordered by topFirst. All local — no network.
func (h *Handler) loadJourney(ctx context.Context, libraryID int64, year int, topFirst bool) journeyData {
	weeks := billboard.WeeklyCharts(h.cfg.DataDir, year)
	if len(weeks) == 0 {
		return journeyData{}
	}
	tracksByTA := h.libraryTrackIndex(ctx, libraryID)
	ytThumbs := h.ytThumbIndex(ctx)

	var out journeyData
	seen := map[string]bool{}
	for _, wk := range weeks {
		wv := weekView{Date: wk.Date, Label: weekLabel(wk.Date)}
		for _, e := range wk.Entries { // stored ascending by position
			k := taKey(e.Title, e.Artist)
			card := chartCard{Pos: e.Pos, Title: e.Title, Artist: e.Artist, IsDebut: !seen[k]}
			if tr, ok := tracksByTA[k]; ok {
				card.Owned, card.TrackID, card.AlbumID = true, tr.id, tr.albumID
			} else if vid := ytThumbs[ytLookupKey(e.Artist, e.Title)]; vid != "" {
				card.ArtURL = "https://i.ytimg.com/vi/" + vid + "/mqdefault.jpg"
			}
			if card.IsDebut {
				seen[k] = true
				out.TotalSongs++
				if card.Owned {
					out.OwnedSongs++
				}
			}
			wv.Cards = append(wv.Cards, card)
		}
		if !topFirst { // low→high: #100 at top, climbing to #1 (entries are asc, so reverse)
			reverseCards(wv.Cards)
		}
		out.Weeks = append(out.Weeks, wv)
	}
	return out
}

func reverseCards(cs []chartCard) {
	for i, j := 0, len(cs)-1; i < j; i, j = i+1, j-1 {
		cs[i], cs[j] = cs[j], cs[i]
	}
}

// weekLabel renders a chart date as a compact "Aug 4" (the page is one year).
func weekLabel(date string) string {
	if t, err := time.Parse("2006-01-02", date); err == nil {
		return t.Format("Jan 2")
	}
	return date
}

type trackRef struct {
	id      int64
	albumID int64
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

// ytThumbIndex maps a resolved-song key (ytLookupKey) to its YouTube video id,
// for un-owned cards whose thumbnail was cached by a prior in-app play.
func (h *Handler) ytThumbIndex(ctx context.Context) map[string]string {
	out := map[string]string{}
	rows, err := h.db.QueryContext(ctx, "SELECT query_key, video_id FROM youtube_lookups WHERE video_id != ''")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var key, vid string
		if rows.Scan(&key, &vid) == nil {
			out[key] = vid
		}
	}
	return out
}

// taKey is the normalized title+artist reconcile key (separated by a unit char
// that can't appear in a normalized string).
func taKey(title, artist string) string {
	return match.NormalizeForDedup(title) + "\x1f" + match.NormalizeForDedup(artist)
}

// journeyQueueTracks expands a year's listen-through into player tracks: every
// debut song in chronological week order (each week in the display direction),
// one entry per song — an owned song as its local track, an un-owned song as a
// yt-kind entry the client resolves to a YouTube video lazily when it plays.
// Drives source=journey.
func (h *Handler) journeyQueueTracks(ctx context.Context, libraryID int64, year int, topFirst, hasYTKey bool) ([]trackRow, error) {
	data := h.loadJourney(ctx, libraryID, year, topFirst)
	out := make([]trackRow, 0, 256)
	for _, wk := range data.Weeks {
		for _, c := range wk.Cards {
			if !c.IsDebut {
				continue
			}
			if c.Owned {
				tracks, err := h.queryPlayerTracks(ctx, playerTrackSelect+` WHERE t.id=?`, c.TrackID)
				if err != nil {
					return nil, err
				}
				out = append(out, tracks...)
			} else if hasYTKey {
				// Un-owned songs play via YouTube (resolved lazily client-side).
				// Without a key there's nothing to resolve to, so the journey is
				// owned-only — no point queueing entries that can't play.
				out = append(out, trackRow{Kind: "yt", Title: c.Title, Artist: c.Artist})
			}
		}
	}
	return out, nil
}

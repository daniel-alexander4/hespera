package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
)

// homeStats is the compact library summary shown under the landing-page cards.
type homeStats struct {
	Artists    int
	Albums     int
	Series     int
	Episodes   int
	Movies     int
	Books      int
	Audiobooks int
}

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	musicLib := h.resolveMusicLibraryID(r)

	// Every dashboard section is best-effort: a failed query warns and renders an
	// empty row rather than failing the whole landing page.
	continueWatching := h.loadContinueWatching(ctx, 12)
	recentlyPlayed, err := h.loadRecentlyPlayedArtists(ctx, musicLib, 12)
	if err != nil {
		slog.Warn("home: load recently-played failed", "err", err)
	}
	recentlyAddedAlbums, err := h.loadRecentlyAddedAlbums(ctx, musicLib, 12)
	if err != nil {
		slog.Warn("home: load recently-added albums failed", "err", err)
	}
	recentlyAddedTV, err := h.recentTVSeries(ctx, tvRecentlyAddedQuery, 12)
	if err != nil {
		slog.Warn("home: load recently-added tv failed", "err", err)
	}
	recentlyAddedMovies, err := h.loadMovieRecentlyAdded(ctx, 12)
	if err != nil {
		slog.Warn("home: load recently-added movies failed", "err", err)
	}
	recentlyAddedBooks, err := h.loadBookRecentlyAdded(ctx, 12)
	if err != nil {
		slog.Warn("home: load recently-added books failed", "err", err)
	}
	recentlyAddedAudiobooks, err := h.loadAudiobookRecentlyAdded(ctx, 12)
	if err != nil {
		slog.Warn("home: load recently-added audiobooks failed", "err", err)
	}

	stats := h.loadHomeStats(ctx, musicLib)

	hasActivity := len(continueWatching) > 0 || len(recentlyPlayed) > 0 ||
		len(recentlyAddedAlbums) > 0 || len(recentlyAddedTV) > 0 || len(recentlyAddedMovies) > 0 ||
		len(recentlyAddedBooks) > 0 || len(recentlyAddedAudiobooks) > 0

	// First-run: no libraries configured yet → the landing page shows a setup
	// prompt (set the media folder, add a library) instead of empty carousels.
	var libCount int
	_ = h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM libraries").Scan(&libCount)

	h.render(w, "home.html", map[string]any{
		"Title":                   "Home",
		"MusicLibraryID":          musicLib,
		"HasMusic":                musicLib > 0,
		"EraPicker":               h.eraPicker(ctx, musicLib),
		"ContinueWatching":        continueWatching,
		"RecentlyPlayed":          recentlyPlayed,
		"RecentlyAddedAlbums":     recentlyAddedAlbums,
		"RecentlyAddedTV":         recentlyAddedTV,
		"RecentlyAddedMovies":     recentlyAddedMovies,
		"RecentlyAddedBooks":      recentlyAddedBooks,
		"RecentlyAddedAudiobooks": recentlyAddedAudiobooks,
		"Stats":                   stats,
		"HasActivity":             hasActivity,
		"NeedsSetup":              libCount == 0,
	})
}

// continueItem is one card in the home "Continue Watching" row, which merges
// in-progress TV and movies. Kind selects the link + art in the template; the
// per-kind fields are populated for that kind only.
type continueItem struct {
	Kind        string // "tv" | "movie"
	Title       string
	Year        string
	RecencyUnix int64 // last-watched; used to interleave, not rendered
	// tv
	SeriesID     string
	SeasonNumber int
	HasPoster    bool
	// NextFileID is the target season's first unwatched episode file — the
	// card's one-click play target (0 → the card falls back to the season page).
	NextFileID int64
	// NextEpisode/ProgressPct/InProgress describe that target so the TV card can
	// label "Resume · S2E3" (InProgress) vs "Play next · S2E4" (a fresh episode)
	// and draw a resume sliver at ProgressPct.
	NextEpisode int
	ProgressPct int
	InProgress  bool
	// movie
	TMDBID int
	FileID int64 // the in-progress file, for the one-click resume link
}

// loadContinueWatching merges in-progress TV (recentTVSeries with the
// continue-watching query, which drops fully-watched series just like the movie
// side drops completed films) and movies (loadMovieContinueWatching) into one
// row ordered by most-recent activity. Each source is best-effort — one failing
// still renders the other — and the two canonical loaders own their
// queries/metadata, so this only interleaves. Both sources are fetched to the
// same limit, so the top `limit` overall are a subset of their union; the final
// slice is capped to limit.
func (h *Handler) loadContinueWatching(ctx context.Context, limit int) []continueItem {
	tvRows, err := h.recentTVSeries(ctx, tvContinueWatchingQuery, limit)
	if err != nil {
		slog.Warn("home: load continue-watching tv failed", "err", err)
	}
	movieRows, err := h.loadMovieContinueWatching(ctx, limit)
	if err != nil {
		slog.Warn("home: load continue-watching movies failed", "err", err)
	}
	items := make([]continueItem, 0, len(tvRows)+len(movieRows))
	for _, r := range tvRows {
		// One-click play target: the season's first unwatched episode, plus its
		// number + resume progress for the card's label and sliver.
		fileID, epNum, pct, inProgress := h.continueTarget(ctx, r.SeriesID, r.SeasonNumber)
		items = append(items, continueItem{
			Kind: "tv", Title: r.Name, Year: r.Year, RecencyUnix: r.RecencyUnix,
			SeriesID: r.SeriesID, SeasonNumber: r.SeasonNumber, HasPoster: r.PosterPath != "",
			NextFileID: fileID, NextEpisode: epNum, ProgressPct: pct, InProgress: inProgress,
		})
	}
	for _, r := range movieRows {
		items = append(items, continueItem{
			Kind: "movie", Title: r.Title, Year: r.Year, RecencyUnix: r.RecencyUnix, TMDBID: r.TMDBID,
			FileID: r.FileID,
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].RecencyUnix > items[j].RecencyUnix })
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

// loadHomeStats returns a best-effort library summary for the landing page; any
// individual count that fails is left at zero.
func (h *Handler) loadHomeStats(ctx context.Context, musicLib int64) homeStats {
	var s homeStats
	if musicLib > 0 {
		_ = h.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM music_artists WHERE library_id=?", musicLib,
		).Scan(&s.Artists)
		_ = h.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM music_albums WHERE library_id=? AND COALESCE(is_compilation,0)=0", musicLib,
		).Scan(&s.Albums)
	}
	const matched = "i.status='matched' AND i.provider='tmdb' AND i.series_id != ''"
	_ = h.db.QueryRowContext(ctx,
		"SELECT COUNT(DISTINCT i.series_id) FROM tv_series_identities i WHERE "+matched,
	).Scan(&s.Series)
	_ = h.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tv_series_identities i WHERE "+matched,
	).Scan(&s.Episodes)
	_ = h.db.QueryRowContext(ctx,
		"SELECT COUNT(DISTINCT tmdb_id) FROM movie_files WHERE match_status='matched' AND tmdb_id != 0",
	).Scan(&s.Movies)
	_ = h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM books").Scan(&s.Books)
	_ = h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audiobooks").Scan(&s.Audiobooks)
	return s
}

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	// X-Hespera identifies this as a Hespera instance — the desktop launcher's
	// attach probe requires it, so a reused port running some other app that
	// happens to answer 200 "ok" is never attached to.
	w.Header().Set("X-Hespera", h.version)
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "message": msg})
}

func httpError(w http.ResponseWriter, code int, msg string, logMsg string, attrs ...any) {
	if code >= 500 {
		slog.Error(logMsg, attrs...)
	} else {
		slog.Warn(logMsg, attrs...)
	}
	http.Error(w, msg, code)
}

func jsonErr(w http.ResponseWriter, code int, msg string, logMsg string, attrs ...any) {
	if code >= 500 {
		slog.Error(logMsg, attrs...)
	} else {
		slog.Warn(logMsg, attrs...)
	}
	jsonError(w, msg, code)
}

func requestWantsJSON(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	if strings.Contains(accept, "application/json") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Requested-With")), "XMLHttpRequest")
}

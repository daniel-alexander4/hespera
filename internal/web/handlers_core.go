package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// homeStats is the compact library summary shown under the landing-page cards.
type homeStats struct {
	Artists  int
	Albums   int
	Series   int
	Episodes int
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
	continueWatching, err := h.recentTVSeries(ctx, tvRecentlyWatchedQuery, 12)
	if err != nil {
		slog.Warn("home: load continue-watching failed", "err", err)
	}
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

	stats := h.loadHomeStats(ctx, musicLib)

	hasActivity := len(continueWatching) > 0 || len(recentlyPlayed) > 0 ||
		len(recentlyAddedAlbums) > 0 || len(recentlyAddedTV) > 0

	h.render(w, "home.html", map[string]any{
		"Title":               "Home",
		"MusicLibraryID":      musicLib,
		"HasMusic":            musicLib > 0,
		"ContinueWatching":    continueWatching,
		"RecentlyPlayed":      recentlyPlayed,
		"RecentlyAddedAlbums": recentlyAddedAlbums,
		"RecentlyAddedTV":     recentlyAddedTV,
		"Stats":               stats,
		"HasActivity":         hasActivity,
	})
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
	return s
}

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ns := "hespera"
	if h.auth != nil {
		ns = h.auth.Namespace()
	}
	h.render(w, "login.html", map[string]any{
		"Title":     "Login",
		"Namespace": ns,
		"Next":      strings.TrimSpace(r.URL.Query().Get("next")),
	})
}

func (h *Handler) authChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.auth == nil || !h.auth.Enabled() {
		jsonError(w, "auth not enabled", http.StatusBadRequest)
		return
	}
	ch, err := h.auth.CreateChallenge(w, r)
	if err != nil {
		jsonErr(w, 500, "internal server error", "create challenge failed", "err", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":   true,
		"data": map[string]any{"challenge": ch.Value},
	})
}

func (h *Handler) authVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.auth == nil || !h.auth.Enabled() {
		jsonError(w, "auth not enabled", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		jsonErr(w, 400, "bad request", "parse form failed", "err", err)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	signature := strings.TrimSpace(r.FormValue("signature"))

	if err := h.auth.VerifyAndStartSession(w, r, username, signature); err != nil {
		jsonErr(w, 401, "authentication failed", "auth verify failed", "err", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"message": "authenticated",
	})
}

func (h *Handler) authLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.auth != nil {
		h.auth.ClearSession(w, r)
	}
	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": "logged out"})
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *Handler) moviesHome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	h.render(w, "movies_home.html", map[string]any{"Title": "Movies"})
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

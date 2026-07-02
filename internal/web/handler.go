package web

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"hespera"
	"hespera/internal/config"
	"hespera/internal/jobs"
	"hespera/internal/tmdb"
)

type Deps struct {
	Cfg config.Config
	DB  *sql.DB
	// Version stamps the static-asset cache-buster (?v=). Empty → "dev".
	Version string
	// AssetsFS overrides the web asset tree (rooted at web/, with templates/ and
	// static/ subtrees). Nil → the embedded assets (the production path); tests
	// inject a stub FS so handler-logic tests stay decoupled from the real
	// template HTML.
	AssetsFS fs.FS
	// Quit initiates a graceful shutdown of the whole app (the topbar power
	// button → POST /shutdown). main wires it to the same path as a SIGTERM; nil
	// (e.g. in tests) disables the endpoint.
	Quit func()
}

type Handler struct {
	cfg       config.Config
	db        *sql.DB
	version   string
	tpls      map[string]*template.Template
	staticFS  fs.FS
	jobs      *jobs.Service
	startedAt time.Time
	// quit gracefully stops the app (the topbar power button → POST /shutdown);
	// nil disables the endpoint.
	quit func()
	// tmdbValidate checks whether a TMDB key is accepted (best-effort, used by
	// the API-keys settings page). A field so tests can stub the network call.
	tmdbValidate func(ctx context.Context, key string) (bool, error)
	// metaFetch dedupes in-flight background metadata fetches (cast, actor bios)
	// keyed by e.g. "cast:123"/"person:456", so a cache-miss page view enqueues
	// at most one job per entity while it's queued/running.
	metaFetch sync.Map
}

func New(d Deps) (*Handler, error) {
	// Overlay the user-set media-folder override from app_settings onto the
	// env/default config, once here at construction. Every MediaRoot reader
	// (scanners + stream handlers) is built from this config, so this is the single
	// override point — no call site reads it from app_settings.
	d.Cfg = resolveEffectiveConfig(d.Cfg, d.DB)

	// Assets are embedded (see ../../embed.go), so the binary is self-contained
	// and finds its templates/static regardless of the working directory. Tests
	// may inject a stub tree via Deps.AssetsFS.
	webRoot := d.AssetsFS
	if webRoot == nil {
		webRoot = hespera.WebFS()
	}
	staticFS, err := fs.Sub(webRoot, "static")
	if err != nil {
		return nil, fmt.Errorf("static sub-fs: %w", err)
	}

	pages := []string{
		"home.html",
		"libraries.html",
		"libraries_new.html",
		"settings.html",
		"settings_jobs.html",
		"music_home.html",
		"music_artist.html",
		"music_artist_external.html",
		"music_artist_disambiguate.html",
		"music_artist_art.html",
		"music_album.html",
		"music_albums.html",
		"music_compilations.html",
		"player.html",
		"music_match_review.html",
		"music_album_edit.html",
		"music_track_edit.html",
		"music_duplicates.html",
		"settings_tags.html",
		"settings_apikeys.html",
		"settings_about.html",
		"tv_home.html",
		"tv_series.html",
		"tv_season.html",
		"tv_match_review.html",
		"tv_player.html",
		"person.html",
		"movies_home.html",
		"movie_detail.html",
		"movie_match_review.html",
		"movie_player.html",
	}

	tpls := make(map[string]*template.Template, len(pages))
	// Embedded files have no meaningful mtime, so the cache-buster is the build
	// version — a new release invalidates every cached asset at once.
	assetVersion := d.Version
	if assetVersion == "" {
		assetVersion = "dev"
	}
	staticURL := func(rawPath string) string {
		p := strings.TrimSpace(rawPath)
		if p == "" {
			return rawPath
		}
		sep := "?"
		if strings.Contains(p, "?") {
			sep = "&"
		}
		return p + sep + "v=" + assetVersion
	}

	layoutBase, err := template.New("layout.html").Funcs(template.FuncMap{
		"staticv": staticURL,
	}).ParseFS(webRoot, "templates/layout.html")
	if err != nil {
		return nil, fmt.Errorf("layout template: %w", err)
	}

	var errs []error
	for _, p := range pages {
		t, cloneErr := layoutBase.Clone()
		if cloneErr != nil {
			errs = append(errs, fmt.Errorf("template %s: clone failed: %w", p, cloneErr))
			continue
		}
		t, err = t.ParseFS(webRoot, "templates/partials_*.html", "templates/"+p)
		if err != nil {
			errs = append(errs, fmt.Errorf("template %s: %w", p, err))
			continue
		}
		tpls[p] = t
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("template compilation failed:\n%w", errors.Join(errs...))
	}

	// Post-loop validation: every page must have a compiled template.
	var missing []string
	for _, p := range pages {
		if _, ok := tpls[p]; !ok {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("templates missing after compilation: %s", strings.Join(missing, ", "))
	}

	h := &Handler{
		cfg:       d.Cfg,
		db:        d.DB,
		version:   assetVersion,
		tpls:      tpls,
		staticFS:  staticFS,
		jobs:      jobs.New(d.DB),
		startedAt: time.Now().UTC(),
		quit:      d.Quit,
		tmdbValidate: func(ctx context.Context, key string) (bool, error) {
			return tmdb.NewClient(key).ValidateKey(ctx)
		},
	}

	go h.pruneTVCacheLoop()

	return h, nil
}

// Shutdown releases background resources on a graceful exit — currently it
// cancels in-flight job contexts so their rows are marked terminal promptly.
func (h *Handler) Shutdown() {
	if h.jobs != nil {
		h.jobs.Shutdown()
	}
}

// shutdown quits the whole app (the topbar power button). It responds first, then
// triggers the graceful shutdown so the client gets a reply before the server
// stops. POST-only and same-origin (a destructive action): a cross-site page's
// fetch carries a foreign Origin and is rejected; same-origin navigations that
// omit Origin never reach this POST endpoint.
func (h *Handler) shutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	if h.quit == nil {
		http.Error(w, "shutdown not available", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("shutting down"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	// Trigger after this handler returns and the response drains (srv.Shutdown
	// waits for the active request), so the client reliably gets the reply.
	go h.quit()
}

func (h *Handler) render(w http.ResponseWriter, page string, data any) {
	t, ok := h.tpls[page]
	if !ok {
		slog.Error("template not found", "page", page)
		http.Error(w, "internal server error", 500)
		return
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		slog.Error("template execute failed", "page", page, "err", err)
		http.Error(w, "internal server error", 500)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	_, _ = w.Write(buf.Bytes())
}

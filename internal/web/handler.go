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
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"hespera"
	"hespera/internal/config"
	"hespera/internal/display"
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
	// AppMode is true when the server runs on the same machine as the app
	// window (the default launch shape). Gates display-scale auto-detection:
	// in server mode the client is a remote browser, and matching it against
	// the *server's* displays would be wrong.
	AppMode bool
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
	// appMode + displayClassAt drive display-scale auto-detection (see
	// Deps.AppMode). displayClassAt is a field so tests can stub the xrandr
	// lookup.
	appMode        bool
	displayClassAt func(ctx context.Context, x, y int) string
	// podcasts is the outbound fetcher for feeds and episode audio. A field so
	// tests can stub it: this is the only subsystem that contacts a host the
	// user supplied, so a test that reached the real one would make a live
	// request to whatever a fixture named. nil → the real guarded client.
	podcasts podcastFetcher
	// powerOff halts the machine (the home screen's power button → POST
	// /poweroff). A field so tests can stub it — a test that reached the real
	// systemctl would halt the machine running the suite.
	powerOff func() error
	// displayAvailable / displayOutputs / displaySetMode are the runtime
	// mode-control seams, fields for the same reason displayClassAt is one: a
	// test must never shell out to xrandr on the machine running the suite —
	// still less change its screen mode.
	displayAvailable func() (bool, string)
	displayOutputs   func(ctx context.Context) ([]display.Output, error)
	displaySetMode   func(ctx context.Context, output string, m display.Mode) error
	// displayMu guards displayPending: the armed undo for a runtime display-mode
	// change (Settings → Features). The timer lives here rather than in the page
	// because the page is on the screen that just changed — a browser that
	// crashed, or a mode the TV can't show, must still be undone.
	displayMu      sync.Mutex
	displayPending *displayPending
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
		"integrity_report.html",
		"libraries_new.html",
		"settings.html",
		"music_home.html",
		"music_artist.html",
		"music_artist_external.html",
		"music_artist_disambiguate.html",
		"music_artist_art.html",
		"music_album.html",
		"music_albums.html",
		"music_compilations.html",
		"music_playlist.html",
		"player.html",
		"music_match_review.html",
		"music_album_edit.html",
		"music_track_edit.html",
		"music_duplicates.html",
		"settings_tags.html",
		"tv_home.html",
		"tv_series.html",
		"tv_season.html",
		"tv_match_review.html",
		"tv_player.html",
		"person.html",
		"photos_home.html",
		"photo_view.html",
		"photo_player.html",
		"books_home.html",
		"book_view.html",
		"book_reader.html",
		"audiobooks_home.html",
		"audiobook_player.html",
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
		"staticv":    staticURL,
		"initial":    initialRune,
		"humanBytes": humanBytes,
		"appVersion": func() string { return assetVersion },
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
		appMode:   d.AppMode,
		tmdbValidate: func(ctx context.Context, key string) (bool, error) {
			return tmdb.NewClient(key).ValidateKey(ctx)
		},
		displayClassAt:   display.ClassAt,
		powerOff:         systemctlPowerOff,
		displayAvailable: display.Available,
		displayOutputs:   display.Outputs,
		displaySetMode:   display.SetMode,
	}

	// Boot auto-resume: re-kick the scan chain of any library whose jobs the
	// startup reconcile (inside jobs.New above) marked "interrupted by restart".
	// Synchronous but cheap — it only inserts job rows; the worker runs them.
	h.resumeInterruptedJobs(context.Background())

	go h.pruneTVCacheLoop()

	// Re-apply the confirmed display mode, if there is one, once the machine's X
	// session is up. In a goroutine because it waits: on a kiosk the autologin
	// shell starts X after this service. A no-op unless the owner opted in and
	// confirmed a mode.
	go h.applySavedDisplayMode()

	return h, nil
}

// Shutdown releases background resources on a graceful exit — currently it
// cancels in-flight job contexts so their rows are marked terminal promptly.
func (h *Handler) Shutdown() {
	if h.jobs != nil {
		h.jobs.Shutdown()
	}
}

// isLoopbackRequest reports whether the request came from the machine Hespera
// runs on. The home screen's power button uses it as a render gate too, so a
// LAN device is never shown a control its own request would be refused.
func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	return net.ParseIP(host).IsLoopback()
}

// allowLocalPost gates the machine-level destructive endpoints (/shutdown,
// /poweroff): POST-only, loopback-only, same-origin. The loopback rule is the
// load-bearing one — on a LAN-serving deployment every household device renders
// the same UI, and one tap must not stop the server (or halt the box) for
// everyone; that stays reserved for the machine Hespera runs on. A reverse proxy
// on that machine forwards from loopback, so its authenticated users keep these
// controls by design. Same-origin blocks a cross-site page's fetch (its Origin
// is foreign); same-origin navigations omit Origin and never reach a POST route.
func (h *Handler) allowLocalPost(w http.ResponseWriter, r *http.Request, action string) bool {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return false
	}
	if !isLoopbackRequest(r) {
		http.Error(w, action+" is only available on the machine Hespera runs on", http.StatusForbidden)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return false
		}
	}
	return true
}

// shutdown quits the whole app (the topbar power button). It responds first, then
// triggers the graceful shutdown so the client gets a reply before the server
// stops. POST-only, same-origin (a destructive action): a cross-site page's
// fetch carries a foreign Origin and is rejected; same-origin navigations that
// omit Origin never reach this POST endpoint. And loopback-only: on a
// LAN-serving deployment every household device's topbar has this button, and
// one tap must not power off the server for everyone — quitting is reserved
// for the machine Hespera runs on (a reverse proxy on that machine forwards
// from loopback, so its authenticated users keep the button by design).
func (h *Handler) shutdown(w http.ResponseWriter, r *http.Request) {
	if !h.allowLocalPost(w, r, "shutdown") {
		return
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

// renderFragment writes a single named template block (no layout) from the given
// page's template set — the fragment path that the grid_pager.js in-place paging
// fetches (the artist/movie/TV card blocks). Mirrors settingsJobsFragment.
func (h *Handler) renderFragment(w http.ResponseWriter, page, block string, data any) {
	t, ok := h.tpls[page]
	if !ok {
		httpError(w, 500, "internal server error", "template not found", "handler", "renderFragment", "page", page)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, block, data); err != nil {
		httpError(w, 500, "internal server error", "render fragment failed", "handler", "renderFragment", "block", block, "err", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

// initialRune returns the first rune of s — the letter-avatar monogram the
// templates render when there is no art. The old {{slice s 0 1}} took the
// first *byte*, splitting a multibyte initial (Björk, 日本) into an invalid
// glyph. Empty input yields "".
func initialRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

// humanBytes renders a byte count in binary units for the templates.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

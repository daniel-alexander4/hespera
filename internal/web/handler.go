package web

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"hespera/internal/auth"
	"hespera/internal/config"
	"hespera/internal/jobs"
	"hespera/internal/tmdb"
)

type Deps struct {
	Cfg config.Config
	DB  *sql.DB
}

type Handler struct {
	cfg       config.Config
	db        *sql.DB
	tpls      map[string]*template.Template
	staticDir string
	jobs      *jobs.Service
	startedAt time.Time
	auth      *auth.Manager
	// tmdbValidate checks whether a TMDB key is accepted (best-effort, used by
	// the API-keys settings page). A field so tests can stub the network call.
	tmdbValidate func(ctx context.Context, key string) (bool, error)
	// metaFetch dedupes in-flight background metadata fetches (cast, actor bios)
	// keyed by e.g. "cast:123"/"person:456", so a cache-miss page view enqueues
	// at most one job per entity while it's queued/running.
	metaFetch sync.Map
}

func New(d Deps) (*Handler, error) {
	staticDir := filepath.Join("web", "static")

	layoutPath := filepath.Join("web", "templates", "layout.html")
	partialPaths, _ := filepath.Glob(filepath.Join("web", "templates", "partials_*.html"))

	pages := []string{
		"home.html",
		"login.html",
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
	staticURL := func(rawPath string) string {
		p := strings.TrimSpace(rawPath)
		if p == "" {
			return rawPath
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, "/"), "static/")
		fp := filepath.Join(staticDir, rel)
		v := "dev"
		if info, err := os.Stat(fp); err == nil {
			v = strconv.FormatInt(info.ModTime().Unix(), 10)
		}
		sep := "?"
		if strings.Contains(p, "?") {
			sep = "&"
		}
		return p + sep + "v=" + v
	}

	layoutBase, err := template.New("layout.html").Funcs(template.FuncMap{
		"staticv": staticURL,
	}).ParseFiles(layoutPath)
	if err != nil {
		return nil, fmt.Errorf("layout template: %w", err)
	}

	var errs []error
	for _, p := range pages {
		pagePath := filepath.Join("web", "templates", p)
		t, cloneErr := layoutBase.Clone()
		if cloneErr != nil {
			errs = append(errs, fmt.Errorf("template %s: clone failed: %w", p, cloneErr))
			continue
		}
		parsePaths := make([]string, 0, len(partialPaths)+1)
		parsePaths = append(parsePaths, partialPaths...)
		parsePaths = append(parsePaths, pagePath)
		t, err = t.ParseFiles(parsePaths...)
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
		tpls:      tpls,
		staticDir: staticDir,
		jobs:      jobs.New(d.DB),
		startedAt: time.Now().UTC(),
		auth:      auth.New(d.Cfg, d.DB),
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

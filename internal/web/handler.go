package web

import (
	"bytes"
	"database/sql"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"isomedia/internal/auth"
	"isomedia/internal/config"
	"isomedia/internal/jobs"
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
}

func New(d Deps) *Handler {
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
		"music_album.html",
		"music_albums.html",
		"music_compilations.html",
		"player.html",
		"tv_home.html",
		"movies_home.html",
	}

	humanBytes := func(b int64) string {
		const (
			kb = 1024
			mb = 1024 * kb
			gb = 1024 * mb
		)
		switch {
		case b >= gb:
			return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
		case b >= mb:
			return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
		case b >= kb:
			return fmt.Sprintf("%.0f KB", float64(b)/float64(kb))
		default:
			return fmt.Sprintf("%d B", b)
		}
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
		"staticv":    staticURL,
		"humanBytes": humanBytes,
	}).ParseFiles(layoutPath)
	if err != nil {
		slog.Error("failed to parse layout template", "err", err)
		layoutBase = template.Must(template.New("layout.html").Parse(`{{define "layout.html"}}<!DOCTYPE html><html><body>{{template "content" .}}</body></html>{{end}}`))
	}

	for _, p := range pages {
		pagePath := filepath.Join("web", "templates", p)
		t, cloneErr := layoutBase.Clone()
		if cloneErr != nil {
			slog.Error("template clone failed", "page", p, "err", cloneErr)
			continue
		}
		parsePaths := make([]string, 0, len(partialPaths)+1)
		parsePaths = append(parsePaths, partialPaths...)
		parsePaths = append(parsePaths, pagePath)
		t, err = t.ParseFiles(parsePaths...)
		if err != nil {
			slog.Error("template parse failed", "page", p, "err", err)
			continue
		}
		tpls[p] = t
	}

	h := &Handler{
		cfg:       d.Cfg,
		db:        d.DB,
		tpls:      tpls,
		staticDir: staticDir,
		jobs:      jobs.New(d.DB),
		startedAt: time.Now().UTC(),
		auth:      auth.New(d.Cfg, d.DB),
	}

	return h
}

func (h *Handler) render(w http.ResponseWriter, page string, data any) {
	t, ok := h.tpls[page]
	if !ok {
		slog.Error("template not found", "page", page)
		http.Error(w, fmt.Sprintf("template not found: %s", page), 500)
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

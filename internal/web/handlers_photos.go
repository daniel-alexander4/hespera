package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hespera/internal/photoscan"
)

// Photos browse + viewer. Unlike the other verticals there is no matching —
// every scanned file shows. The page is server-tabbed (?tab=all|date|folders|
// videos): each tab is one paginated grid over the same photos query with a
// different shape, so the subtab bar is plain links (remote-friendly: Enter
// navigates) rather than the client-side panel toggles the media homes use.
// Filters (year, folder, order) ride the query string through the tabs, the
// grid-pager fragments, and into the viewer's prev/next context.

type photoCard struct {
	ID       int64
	Kind     string // photo | video
	Name     string // filename, tooltip/caption use
	TakenAt  string
	HasThumb bool
	Header   string // month label, set on group transitions (By Date tab)
}

type photoFolder struct {
	Dir     string
	Count   int
	CoverID int64 // a representative photo id for the folder card art
}

// photoFilters is the query-string state shared by every photos surface.
type photoFilters struct {
	Tab   string // all | date | folders | videos
	Year  string // "" = all years, else "2019"
	Dir   string // "" = everywhere, else a dir_rel exactly
	Order string // desc (newest first, default) | asc
}

func parsePhotoFilters(r *http.Request) photoFilters {
	q := r.URL.Query()
	f := photoFilters{
		Tab:   q.Get("tab"),
		Year:  strings.TrimSpace(q.Get("year")),
		Dir:   strings.TrimSpace(q.Get("dir")),
		Order: q.Get("order"),
	}
	switch f.Tab {
	case "date", "folders", "videos":
	default:
		f.Tab = "all"
	}
	if _, err := strconv.Atoi(f.Year); err != nil {
		f.Year = ""
	}
	if f.Order != "asc" {
		f.Order = "desc"
	}
	return f
}

// where builds the SQL predicate + args for the filter state. kindOnly narrows
// to one kind ("" = both).
func (f photoFilters) where(kindOnly string) (string, []any) {
	conds := []string{"1=1"}
	var args []any
	if kindOnly != "" {
		conds = append(conds, "kind=?")
		args = append(args, kindOnly)
	}
	if f.Year != "" {
		conds = append(conds, "substr(taken_at,1,4)=?")
		args = append(args, f.Year)
	}
	if f.Dir != "" {
		conds = append(conds, "dir_rel=?")
		args = append(args, f.Dir)
	}
	return strings.Join(conds, " AND "), args
}

func (f photoFilters) orderClause() string {
	if f.Order == "asc" {
		return "ORDER BY taken_at ASC, id ASC"
	}
	return "ORDER BY taken_at DESC, id DESC"
}

// query renders the filter state back to a query string (no page), used by the
// pager links, the tab links, and the viewer context.
func (f photoFilters) query(tab string) string {
	v := url.Values{}
	if tab == "" {
		tab = f.Tab
	}
	if tab != "all" {
		v.Set("tab", tab)
	}
	if f.Year != "" {
		v.Set("year", f.Year)
	}
	if f.Dir != "" {
		v.Set("dir", f.Dir)
	}
	if f.Order != "desc" {
		v.Set("order", f.Order)
	}
	return v.Encode()
}

// CtxQuery is the template-facing render of the current filter state — the
// viewer-context params a grid card appends to its /photos/view href. Typed
// template.URL: it is server-built from url.Values (safe by construction), and
// the plain-string form gets percent-escaped as ONE query value by the
// contextual autoescaper, mangling the params.
func (f photoFilters) CtxQuery() template.URL { return template.URL(f.query("")) }

func (h *Handler) photosHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/photos" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	f := parsePhotoFilters(r)
	ctx := r.Context()

	data := map[string]any{
		"Breadcrumb": []crumb{bcHome},
		"Title":      "Photos",
		"Filters":    f,
		// template.URL for the same reason as CtxQuery — see there.
		"TabQ": map[string]template.URL{
			"all": template.URL(f.query("all")), "date": template.URL(f.query("date")),
			"folders": template.URL(f.query("folders")), "videos": template.URL(f.query("videos")),
		},
	}

	switch f.Tab {
	case "folders":
		folders, err := h.loadPhotoFolders(ctx, f)
		if err != nil {
			httpError(w, 500, "internal server error", "load photo folders failed", "handler", "photosHome", "err", err)
			return
		}
		data["Folders"] = folders
	default:
		kindOnly := ""
		if f.Tab == "videos" {
			kindOnly = "video"
		}
		cards, nav, err := h.loadPhotoCards(ctx, f, kindOnly, pageParam(r))
		if err != nil {
			httpError(w, 500, "internal server error", "load photos failed", "handler", "photosHome", "err", err)
			return
		}
		if r.URL.Query().Get("grid") == "1" {
			h.renderFragment(w, "photos_home.html", "photo-cards", map[string]any{"Cards": cards, "Filters": f})
			return
		}
		data["Cards"] = cards
		data["Page"] = nav
	}

	var total int
	_ = h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM photos").Scan(&total)
	data["LibraryEmpty"] = total == 0

	years, err := h.loadPhotoYears(ctx)
	if err == nil {
		data["Years"] = years
	}
	h.render(w, "photos_home.html", data)
}

// loadPhotoCards returns one page of photo cards under the filters. On the By
// Date tab each month transition gets a header label the grid renders as a
// full-width divider.
func (h *Handler) loadPhotoCards(ctx context.Context, f photoFilters, kindOnly string, page int) ([]photoCard, pageNav, error) {
	cond, args := f.where(kindOnly)
	var total int
	if err := h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM photos WHERE "+cond, args...).Scan(&total); err != nil {
		return nil, pageNav{}, err
	}
	nav, offset := paginate(page, total, "/photos")
	nav.Query = template.URL(f.query(""))

	rows, err := h.db.QueryContext(ctx,
		"SELECT id, kind, abs_path, taken_at, thumb_path FROM photos WHERE "+cond+" "+f.orderClause()+" LIMIT ? OFFSET ?",
		append(args, listPageSize, offset)...)
	if err != nil {
		return nil, pageNav{}, err
	}
	defer rows.Close()

	withHeaders := f.Tab == "date"
	lastHeader := ""
	out := make([]photoCard, 0, listPageSize)
	for rows.Next() {
		var c photoCard
		var absPath, thumb string
		if err := rows.Scan(&c.ID, &c.Kind, &absPath, &c.TakenAt, &thumb); err != nil {
			return nil, pageNav{}, err
		}
		c.Name = strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))
		c.HasThumb = thumb != "" && thumb != "unavailable"
		if withHeaders {
			if hdr := monthLabel(c.TakenAt); hdr != lastHeader {
				c.Header = hdr
				lastHeader = hdr
			}
		}
		out = append(out, c)
	}
	return out, nav, rows.Err()
}

// monthLabel renders "July 2019" from a stored taken_at, or "Undated".
func monthLabel(takenAt string) string {
	t, err := time.Parse("2006-01-02 15:04:05", takenAt)
	if err != nil {
		return "Undated"
	}
	return t.Format("January 2006")
}

func (h *Handler) loadPhotoFolders(ctx context.Context, f photoFilters) ([]photoFolder, error) {
	cond, args := f.where("")
	rows, err := h.db.QueryContext(ctx, `
SELECT dir_rel, COUNT(*), MIN(id) FROM photos WHERE `+cond+`
GROUP BY dir_rel ORDER BY dir_rel`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []photoFolder
	for rows.Next() {
		var pf photoFolder
		if err := rows.Scan(&pf.Dir, &pf.Count, &pf.CoverID); err != nil {
			return nil, err
		}
		out = append(out, pf)
	}
	return out, rows.Err()
}

func (h *Handler) loadPhotoYears(ctx context.Context) ([]string, error) {
	rows, err := h.db.QueryContext(ctx,
		"SELECT DISTINCT substr(taken_at,1,4) FROM photos WHERE taken_at<>'' ORDER BY 1 DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var y string
		if err := rows.Scan(&y); err != nil {
			return nil, err
		}
		out = append(out, y)
	}
	return out, rows.Err()
}

// photoView renders the full-screen viewer for one photo (or a clip's play
// card), with prev/next resolved server-side under the SAME filter + order
// context the grid used — the viewer is stateless; ←/→ are plain navigations.
func (h *Handler) photoView(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	f := parsePhotoFilters(r)

	var kind, absPath, takenAt, container, dirRel string
	err = h.db.QueryRowContext(r.Context(),
		"SELECT kind, abs_path, taken_at, container, dir_rel FROM photos WHERE id=?", id,
	).Scan(&kind, &absPath, &takenAt, &container, &dirRel)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	prevID, nextID := h.photoNeighbors(r.Context(), f, id, takenAt)
	ctxQ := f.query("")
	viewHref := func(pid int64) string {
		if pid == 0 {
			return ""
		}
		u := "/photos/view?id=" + strconv.FormatInt(pid, 10)
		if ctxQ != "" {
			u += "&" + ctxQ
		}
		return u
	}
	backQ := ctxQ
	if backQ != "" {
		backQ = "?" + backQ
	}

	when := ""
	if t, terr := time.Parse("2006-01-02 15:04:05", takenAt); terr == nil {
		when = t.Format("Monday, January 2, 2006 · 3:04 PM")
	}
	h.render(w, "photo_view.html", map[string]any{
		"Breadcrumb":  []crumb{bcHome, {Label: "Photos", Href: "/photos" + backQ}},
		"Title":       filepath.Base(absPath),
		"ID":          id,
		"IsVideo":     kind == "video",
		"Displayable": browserDisplayable(container),
		"Name":        filepath.Base(absPath),
		"When":        when,
		"Dir":         dirRel,
		"PrevHref":    viewHref(prevID),
		"NextHref":    viewHref(nextID),
		"BackHref":    "/photos" + backQ,
	})
}

// photoNeighbors resolves the previous/next photo ids around (takenAt, id)
// under the filter context's kind/year/dir and order. Tuple comparison keeps
// ties on taken_at stable.
func (h *Handler) photoNeighbors(ctx context.Context, f photoFilters, id int64, takenAt string) (prevID, nextID int64) {
	kindOnly := ""
	if f.Tab == "videos" {
		kindOnly = "video"
	}
	cond, args := f.where(kindOnly)
	// "next" = the row after in display order; "prev" = before. Display order
	// is desc by default, so next = smaller (taken_at, id) tuple.
	after, before := "<", ">"
	afterOrd, beforeOrd := "DESC", "ASC"
	if f.Order == "asc" {
		after, before = ">", "<"
		afterOrd, beforeOrd = "ASC", "DESC"
	}
	q := func(cmp, ord string) int64 {
		var out int64
		_ = h.db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT id FROM photos WHERE %s AND (taken_at, id) %s (?, ?) ORDER BY taken_at %s, id %s LIMIT 1",
				cond, cmp, ord, ord),
			append(append([]any{}, args...), takenAt, id)...).Scan(&out)
		return out
	}
	return q(before, beforeOrd), q(after, afterOrd)
}

// browserDisplayable reports whether the browser can render the format
// natively — those serve the original; the rest get a cached webp rendition.
func browserDisplayable(container string) bool {
	switch container {
	case "jpg", "jpeg", "png", "gif", "webp", "avif", "bmp":
		return true
	default:
		return false
	}
}

var photoMIMEs = map[string]string{
	"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png",
	"gif": "image/gif", "webp": "image/webp", "avif": "image/avif",
	"bmp": "image/bmp",
}

// photoArt serves a photo's generated grid thumbnail. 404 when none exists
// (pending or unavailable) — the template renders its own placeholder card.
func (h *Handler) photoArt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, err := pathID(r, "/art/photo/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var thumb string
	if err := h.db.QueryRowContext(r.Context(), "SELECT thumb_path FROM photos WHERE id=?", id).Scan(&thumb); err != nil || thumb == "" || thumb == "unavailable" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, thumb)
}

// photoFull serves the viewer's full-size image: the original file for
// browser-displayable formats, else a cached 2048px webp rendition generated
// on first view (gated ffmpeg). Videos never hit this (they play).
func (h *Handler) photoFull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, err := pathID(r, "/photos/full/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var absPath, container string
	var orientation int
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT abs_path, container, orientation FROM photos WHERE id=? AND kind='photo'", id,
	).Scan(&absPath, &container, &orientation); err != nil {
		http.NotFound(w, r)
		return
	}
	clean, err := h.resolveMediaPath(absPath)
	if err != nil {
		http.Error(w, "file path is outside media root", 500)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if browserDisplayable(container) {
		f, err := os.Open(clean)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			httpError(w, 500, "internal server error", "stat photo failed", "handler", "photoFull", "err", err)
			return
		}
		if mt, ok := photoMIMEs[container]; ok {
			w.Header().Set("Content-Type", mt)
		}
		w.Header().Set("Cache-Control", "private, max-age=86400")
		http.ServeContent(w, r, filepath.Base(clean), st.ModTime(), f)
		return
	}
	rendition, err := photoscan.EnsureRendition(r.Context(), h.cfg.DataDir, id, clean, orientation)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "rendition failed", "photo rendition", "handler", "photoFull", "id", id, "err", err)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, rendition)
}

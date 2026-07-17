package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"hespera/internal/bookscan"
)

// The books vertical's browse/read surfaces. Reading happens in the app window
// itself — a Chromium — so EPUB spine documents, CBZ page images, and whole
// PDFs render natively; the server only lists, serves zip entries by exact
// name, and stores the reading position. No provider matching exists for
// books: everything shown is embedded or filename-derived metadata.

type bookCard struct {
	ID       int64
	Title    string
	Author   string
	HasThumb bool
}

// booksHome renders the paginated cover grid (alphabetical by title), with the
// same in-place `?grid=1` fragment paging the other browse grids use.
func (h *Handler) booksHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/books" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()

	var total int
	if err := h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM books").Scan(&total); err != nil {
		httpError(w, 500, "internal server error", "count books failed", "handler", "booksHome", "err", err)
		return
	}
	nav, offset := paginate(pageParam(r), total, "/books")

	rows, err := h.db.QueryContext(ctx, `
SELECT id, title, author, thumb_path FROM books
ORDER BY title COLLATE NOCASE, id LIMIT ? OFFSET ?`, listPageSize, offset)
	if err != nil {
		httpError(w, 500, "internal server error", "load books failed", "handler", "booksHome", "err", err)
		return
	}
	defer rows.Close()
	cards := make([]bookCard, 0, listPageSize)
	for rows.Next() {
		var c bookCard
		var thumb string
		if err := rows.Scan(&c.ID, &c.Title, &c.Author, &thumb); err != nil {
			httpError(w, 500, "internal server error", "scan book failed", "handler", "booksHome", "err", err)
			return
		}
		c.HasThumb = thumb != "" && thumb != "unavailable"
		cards = append(cards, c)
	}

	if r.URL.Query().Get("grid") == "1" {
		h.renderFragment(w, "books_home.html", "book-cards", map[string]any{"Cards": cards})
		return
	}

	var libs int
	_ = h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM libraries WHERE type='books'").Scan(&libs)
	h.render(w, "books_home.html", map[string]any{
		"Breadcrumb":   []crumb{bcHome},
		"Title":        "Books",
		"Cards":        cards,
		"Page":         nav,
		"LibraryEmpty": libs == 0,
	})
}

type bookRow struct {
	id                    int64
	absPath               string
	format, title, author string
	pageCount             int
}

func (h *Handler) loadBook(ctx context.Context, id int64) (bookRow, error) {
	var b bookRow
	err := h.db.QueryRowContext(ctx,
		"SELECT id, abs_path, format, title, author, page_count FROM books WHERE id=?", id,
	).Scan(&b.id, &b.absPath, &b.format, &b.title, &b.author, &b.pageCount)
	return b, err
}

func (h *Handler) loadBookProgress(ctx context.Context, id int64) (spine int, frac float64, completed bool) {
	var c int
	_ = h.db.QueryRowContext(ctx,
		"SELECT spine_index, scroll_fraction, completed FROM book_reading_progress WHERE book_id=?", id,
	).Scan(&spine, &frac, &c)
	return spine, frac, c == 1
}

// bookReadable reports whether the reader can open this format: parsed Tier-1
// zips plus PDF (which Chromium renders itself). format ” (corrupt Tier-1) and
// Tier-2 formats list but don't read.
func bookReadable(format string) bool {
	return format == "epub" || format == "cbz" || format == "pdf"
}

// bookView is the detail page: cover, metadata, Read/Resume.
func (h *Handler) bookView(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	b, err := h.loadBook(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	spine, frac, completed := h.loadBookProgress(r.Context(), id)
	unitName := "Chapter"
	if b.format == "cbz" {
		unitName = "Page"
	}
	var thumb string
	_ = h.db.QueryRowContext(r.Context(), "SELECT thumb_path FROM books WHERE id=?", id).Scan(&thumb)
	h.render(w, "book_view.html", map[string]any{
		"Breadcrumb":  []crumb{bcHome, bcBooks},
		"Title":       b.title,
		"ID":          b.id,
		"BookTitle":   b.title,
		"Author":      b.author,
		"Format":      strings.ToUpper(b.format),
		"Readable":    bookReadable(b.format),
		"PageCount":   b.pageCount,
		"UnitName":    unitName,
		"HasProgress": spine > 0 || frac > 0.01,
		"AtUnit":      spine + 1,
		"Completed":   completed,
		"HasThumb":    thumb != "" && thumb != "unavailable",
	})
}

// bookReader opens the reading surface for one book. EPUB and CBZ step
// through zip entries (an iframe of spine documents / an image sequence); PDF
// hands the whole file to Chromium's native viewer. ?begin=1 starts from the
// top instead of the stored position.
func (h *Handler) bookReader(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	b, err := h.loadBook(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !bookReadable(b.format) {
		http.Redirect(w, r, fmt.Sprintf("/books/view?id=%d", id), http.StatusSeeOther)
		return
	}
	clean, err := h.resolveMediaPath(b.absPath)
	if err != nil {
		http.Error(w, "file path is outside media root", http.StatusInternalServerError)
		return
	}

	var entries []string
	switch b.format {
	case "epub":
		m, err := bookscan.ParseEPUB(clean)
		if err != nil {
			httpError(w, 500, "book is unreadable", "epub parse failed", "handler", "bookReader", "err", err)
			return
		}
		entries = m.Spine
	case "cbz":
		entries, err = bookscan.CBZPages(clean)
		if err != nil {
			httpError(w, 500, "book is unreadable", "cbz parse failed", "handler", "bookReader", "err", err)
			return
		}
	}

	startIndex, startFrac := 0, 0.0
	if r.URL.Query().Get("begin") != "1" {
		spine, frac, _ := h.loadBookProgress(r.Context(), id)
		if spine >= 0 && spine < len(entries) {
			startIndex, startFrac = spine, frac
		}
	}
	entriesJSON, _ := json.Marshal(entries)
	h.render(w, "book_reader.html", map[string]any{
		"Breadcrumb":    []crumb{bcHome, bcBooks, {Label: b.title, Href: fmt.Sprintf("/books/view?id=%d", b.id)}},
		"Title":         b.title,
		"ID":            b.id,
		"BookTitle":     b.title,
		"Kind":          b.format,
		"EntriesJSON":   string(entriesJSON),
		"EntryCount":    len(entries),
		"StartIndex":    startIndex,
		"StartFraction": startFrac,
	})
}

// bookAsset serves one zip entry of an EPUB/CBZ by exact name:
// /book/asset/{id}/{entry path inside the archive}. Relative references inside
// EPUB spine documents (images, CSS) resolve to sibling asset URLs naturally.
// Names are looked up in the archive, never on the filesystem — traversal is
// impossible by construction.
func (h *Handler) bookAsset(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/book/asset/")
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 || slash == len(rest)-1 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(rest[:slash], 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	entry := rest[slash+1:]

	b, err := h.loadBook(r.Context(), id)
	if err != nil || (b.format != "epub" && b.format != "cbz") {
		http.NotFound(w, r)
		return
	}
	clean, err := h.resolveMediaPath(b.absPath)
	if err != nil {
		http.Error(w, "file path is outside media root", http.StatusInternalServerError)
		return
	}
	rc, err := bookscan.ZipEntry(clean, entry)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", bookAssetMIME(entry))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = io.Copy(w, rc)
}

func bookAssetMIME(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".xhtml", ".xml":
		return "application/xhtml+xml"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

// bookFile streams the whole source file — the PDF reader's <embed> source
// (Chromium's built-in viewer renders it).
func (h *Handler) bookFile(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "/book/file/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	b, err := h.loadBook(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	clean, err := h.resolveMediaPath(b.absPath)
	if err != nil {
		http.Error(w, "file path is outside media root", http.StatusInternalServerError)
		return
	}
	ct := map[string]string{
		"pdf":  "application/pdf",
		"epub": "application/epub+zip",
		"cbz":  "application/vnd.comicbook+zip",
	}[b.format]
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, clean)
}

// bookArt serves the generated cover thumb.
func (h *Handler) bookArt(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "/art/book/")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var thumb string
	if err := h.db.QueryRowContext(r.Context(), "SELECT thumb_path FROM books WHERE id=?", id).Scan(&thumb); err != nil {
		http.NotFound(w, r)
		return
	}
	h.serveGeneratedThumb(w, r, thumb)
}

// bookReadingProgress stores the reading position. Like the playback-progress
// upserts, completed is earn-only (MAX) — the reader reports it only when this
// session actually reached the end, and never revokes a finished flag.
func (h *Handler) bookReadingProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		BookID         int64   `json:"book_id"`
		SpineIndex     int     `json:"spine_index"`
		ScrollFraction float64 `json:"scroll_fraction"`
		Completed      bool    `json:"completed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BookID <= 0 {
		jsonError(w, "invalid progress payload", http.StatusBadRequest)
		return
	}
	// The book must exist (FK would also catch it, but answer 404, not 500).
	if _, err := h.loadBook(r.Context(), req.BookID); errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "book not found", http.StatusNotFound)
		return
	}
	if req.SpineIndex < 0 {
		req.SpineIndex = 0
	}
	if req.ScrollFraction < 0 {
		req.ScrollFraction = 0
	} else if req.ScrollFraction > 1 {
		req.ScrollFraction = 1
	}
	completed := 0
	if req.Completed {
		completed = 1
	}
	_, err := h.db.ExecContext(r.Context(), `
INSERT INTO book_reading_progress (book_id, spine_index, scroll_fraction, completed, updated_at)
VALUES (?, ?, ?, ?, datetime('now'))
ON CONFLICT(book_id) DO UPDATE SET
  spine_index=excluded.spine_index,
  scroll_fraction=excluded.scroll_fraction,
  completed=MAX(completed, excluded.completed),
  updated_at=datetime('now')`,
		req.BookID, req.SpineIndex, req.ScrollFraction, completed)
	if err != nil {
		jsonError(w, "store progress failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// loadBookRecentlyAdded feeds home's Recently Added Books carousel — newest
// rows across every books library.
func (h *Handler) loadBookRecentlyAdded(ctx context.Context, limit int) ([]bookCard, error) {
	rows, err := h.db.QueryContext(ctx, `
SELECT id, title, author, thumb_path FROM books
ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]bookCard, 0, limit)
	for rows.Next() {
		var c bookCard
		var thumb string
		if err := rows.Scan(&c.ID, &c.Title, &c.Author, &thumb); err != nil {
			return nil, err
		}
		c.HasThumb = thumb != "" && thumb != "unavailable"
		out = append(out, c)
	}
	return out, rows.Err()
}

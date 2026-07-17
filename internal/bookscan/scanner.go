package bookscan

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"hespera/internal/config"
	"hespera/internal/pathguard"
)

// Scanner walks books libraries (type='books') into the books table. It is
// the photoscan shape without the EXIF/probe machinery: path-keyed identity,
// (size,mtime) unchanged fast-path, move-relink before a prune, and the
// 0-files prune guard so an unmounted root never empties a library.
type Scanner struct {
	Cfg config.Config
	DB  *sql.DB
	// ShouldYield, when set, is polled by the long cover-thumbnail sweep so a
	// queued interactive job (scan/match) doesn't wait behind it.
	ShouldYield func() bool
}

func New(cfg config.Config, db *sql.DB) *Scanner {
	return &Scanner{Cfg: cfg, DB: db}
}

// readableFormats are Tier 1 — parsed and readable in the app window (which is
// a Chromium: EPUB/CBZ assets and PDFs render natively).
var readableFormats = map[string]bool{"epub": true, "cbz": true, "pdf": true}

// tier2Formats are recognized and listed (filename title, placeholder cover)
// but not parsed or readable — MOBI needs a clean-room PalmDoc decompressor,
// CBR a rar reader; both are gated until real demand.
var tier2Formats = map[string]bool{"mobi": true, "azw": true, "azw3": true, "fb2": true, "cbr": true}

func bookFormat(path string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if readableFormats[ext] || tier2Formats[ext] {
		return ext
	}
	return ""
}

func skipDirName(name string) bool {
	return strings.HasPrefix(name, ".") || name == "@eaDir"
}

// titleFromFilename is the fallback identity for unparsed formats and
// metadata-less files: the base name, extension stripped, underscores read as
// spaces.
func titleFromFilename(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return strings.TrimSpace(strings.ReplaceAll(base, "_", " "))
}

// ScanBooks walks a books library's root and upserts every recognized ebook/
// comic file. Parse failures degrade the row (format=”, filename title) and
// never fail the scan — an unknown or corrupt file lists with a generic cover.
func (s *Scanner) ScanBooks(ctx context.Context, jobID, libraryID int64) error {
	var root string
	if err := s.DB.QueryRowContext(ctx,
		"SELECT root_path FROM libraries WHERE id=? AND type='books'", libraryID,
	).Scan(&root); err != nil {
		return fmt.Errorf("load books library %d: %w", libraryID, err)
	}
	mediaRoot := filepath.Clean(s.Cfg.MediaRoot)
	cleanRoot, err := pathguard.ResolveExistingUnderRoot(mediaRoot, root)
	if err != nil {
		return fmt.Errorf("library root outside media root: %w", err)
	}

	if total := s.countEligibleFiles(cleanRoot); total > 0 {
		_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_total=? WHERE id=?", total, jobID)
	}

	processed := 0
	err = filepath.WalkDir(cleanRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			slog.Warn("bookscan: walk error", "path", p, "err", walkErr)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if p != cleanRoot && skipDirName(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		format := bookFormat(p)
		if format == "" {
			return nil
		}
		clean, err := pathguard.ResolveExistingUnderRoot(mediaRoot, p)
		if err != nil {
			slog.Warn("bookscan: file escapes media root; skipping", "path", p)
			return nil
		}
		if err := s.scanFile(ctx, libraryID, clean, format); err != nil {
			slog.Warn("bookscan: file failed; skipping", "path", clean, "err", err)
		}
		processed++
		if processed%50 == 0 {
			_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	_, _ = s.DB.ExecContext(ctx, "UPDATE scan_jobs SET progress_current=? WHERE id=?", processed, jobID)

	// Prune guard: a walk that saw NOTHING while rows exist is an unmounted
	// mount point, not an emptied library — pruning would drop every row plus
	// the reading progress they anchor.
	if processed == 0 {
		var rows int
		_ = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM books WHERE library_id=?", libraryID).Scan(&rows)
		if rows > 0 {
			slog.Warn("bookscan: no files found but library has rows — root looks unmounted; skipping prune",
				"library_id", libraryID, "root", cleanRoot)
			return nil
		}
	}
	s.relinkMovedFiles(ctx, libraryID, cleanRoot)
	s.pruneMissingFiles(ctx, libraryID, cleanRoot)
	return nil
}

func (s *Scanner) countEligibleFiles(root string) int {
	n := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && skipDirName(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasPrefix(d.Name(), ".") && bookFormat(p) != "" {
			n++
		}
		return nil
	})
	return n
}

func (s *Scanner) scanFile(ctx context.Context, libraryID int64, path, format string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	size, mtime := st.Size(), st.ModTime().Unix()

	// Unchanged fast-path: same bytes → nothing to re-parse.
	var have int
	err = s.DB.QueryRowContext(ctx,
		"SELECT 1 FROM books WHERE library_id=? AND abs_path=? AND file_size_bytes=? AND mtime_unix=?",
		libraryID, path, size, mtime,
	).Scan(&have)
	if err == nil && have == 1 {
		return nil
	}

	title, author := titleFromFilename(path), ""
	pages := 0
	switch format {
	case "epub":
		m, err := ParseEPUB(path)
		if err != nil {
			// A corrupt Tier-1 file still lists (filename title, generic
			// cover) — format='' marks it unreadable so no Read button shows.
			slog.Warn("bookscan: unreadable epub", "path", path, "err", err)
			format = ""
		} else {
			if m.Title != "" {
				title = m.Title
			}
			author = m.Author
			pages = len(m.Spine)
		}
	case "cbz":
		names, err := CBZPages(path)
		if err != nil {
			slog.Warn("bookscan: unreadable cbz", "path", path, "err", err)
			format = ""
		} else {
			pages = len(names)
		}
	case "pdf":
		if t, a := PDFMeta(path); t != "" || a != "" {
			if t != "" {
				title = t
			}
			author = a
		}
	}

	_, err = s.DB.ExecContext(ctx, `
INSERT INTO books (library_id, abs_path, format, title, author, page_count, file_size_bytes, mtime_unix, thumb_path)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, '')
ON CONFLICT(library_id, abs_path) DO UPDATE SET
  format=excluded.format,
  title=excluded.title,
  author=excluded.author,
  page_count=excluded.page_count,
  file_size_bytes=excluded.file_size_bytes,
  mtime_unix=excluded.mtime_unix,
  thumb_path='',
  updated_at=datetime('now')`,
		libraryID, path, format, title, author, pages, size, mtime)
	return err
}

// relinkMovedFiles pairs a missing row (orphan) with a single surviving file
// sharing its (size, mtime) content signature — which a plain mv preserves —
// and transfers the reading progress before prune deletes the orphan. Strictly
// 1:1: an ambiguous signature transfers nothing.
func (s *Scanner) relinkMovedFiles(ctx context.Context, libraryID int64, root string) {
	type row struct {
		id          int64
		path        string
		size, mtime int64
	}
	type sig struct{ size, mtime int64 }

	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, abs_path, file_size_bytes, mtime_unix FROM books WHERE library_id=?", libraryID)
	if err != nil {
		return
	}
	var all []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.path, &r.size, &r.mtime) == nil {
			all = append(all, r)
		}
	}
	rows.Close()

	survivors := make(map[sig][]row)
	var orphans []row
	orphanCount := make(map[sig]int)
	for _, r := range all {
		if !strings.HasPrefix(r.path, root+string(filepath.Separator)) && r.path != root {
			continue
		}
		if _, err := os.Stat(r.path); err == nil {
			k := sig{r.size, r.mtime}
			survivors[k] = append(survivors[k], r)
		} else if os.IsNotExist(err) {
			orphans = append(orphans, r)
			orphanCount[sig{r.size, r.mtime}]++
		}
	}
	for _, o := range orphans {
		k := sig{o.size, o.mtime}
		cand := survivors[k]
		if len(cand) != 1 || orphanCount[k] != 1 {
			continue
		}
		// The thumb is id-keyed and regenerates for the surviving row; only
		// the reading progress is irreplaceable.
		_, _ = s.DB.ExecContext(ctx, `
INSERT INTO book_reading_progress (book_id, spine_index, scroll_fraction, completed, updated_at)
SELECT ?, spine_index, scroll_fraction, completed, updated_at FROM book_reading_progress WHERE book_id=?
ON CONFLICT(book_id) DO UPDATE SET
  spine_index=excluded.spine_index,
  scroll_fraction=excluded.scroll_fraction,
  completed=MAX(completed, excluded.completed),
  updated_at=excluded.updated_at`,
			cand[0].id, o.id)
	}
}

func (s *Scanner) pruneMissingFiles(ctx context.Context, libraryID int64, root string) {
	rows, err := s.DB.QueryContext(ctx,
		"SELECT id, abs_path FROM books WHERE library_id=?", libraryID)
	if err != nil {
		return
	}
	var staleIDs []int64
	for rows.Next() {
		var id int64
		var p string
		if rows.Scan(&id, &p) != nil {
			continue
		}
		if !strings.HasPrefix(p, root+string(filepath.Separator)) && p != root {
			continue
		}
		if _, err := os.Stat(p); os.IsNotExist(err) {
			staleIDs = append(staleIDs, id)
		}
	}
	rows.Close()
	for _, id := range staleIDs {
		if _, err := s.DB.ExecContext(ctx, "DELETE FROM books WHERE id=?", id); err != nil {
			continue
		}
		for _, name := range ThumbFileNames(id) {
			_ = os.Remove(filepath.Join(s.Cfg.DataDir, "thumbs", "books", name))
		}
	}
	if len(staleIDs) > 0 {
		slog.Info("bookscan: pruned missing files", "library_id", libraryID, "count", len(staleIDs))
	}
}

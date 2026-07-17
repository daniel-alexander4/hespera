package bookscan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hespera/internal/config"
	isodb "hespera/internal/db"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	conn, err := isodb.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := isodb.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return conn
}

func seedLibrary(t *testing.T, db *sql.DB, rootPath string) int64 {
	t.Helper()
	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('Books', 'books', ?)", rootPath)
	if err != nil {
		t.Fatalf("seedLibrary: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedScanJob(t *testing.T, db *sql.DB, libID int64) int64 {
	t.Helper()
	res, err := db.Exec("INSERT INTO scan_jobs (library_id, job_type) VALUES (?, 'book_scan')", libID)
	if err != nil {
		t.Fatalf("seedScanJob: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

const scanOPF = `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Scanned Book</dc:title>
    <dc:creator>Scan Author</dc:creator>
  </metadata>
  <manifest>
    <item id="cover" href="img/cover.jpg" media-type="image/jpeg" properties="cover-image"/>
    <item id="c1" href="ch1.xhtml" media-type="application/xhtml+xml"/>
    <item id="c2" href="ch2.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="c1"/><itemref idref="c2"/></spine>
</package>`

func writeTestEPUB(t *testing.T, path string) {
	t.Helper()
	writeZip(t, path, map[string]string{
		"META-INF/container.xml": testContainerXML,
		"OEBPS/content.opf":      scanOPF,
		"OEBPS/ch1.xhtml":        "<html/>",
		"OEBPS/ch2.xhtml":        "<html/>",
		"OEBPS/img/cover.jpg":    "jpegbytes",
	})
}

// seedRoot builds a media root with one library dir holding an epub, a cbz, a
// pdf, a tier-2 mobi, a corrupt epub, and junk that must be skipped.
func seedRoot(t *testing.T) (mediaRoot, libRoot string) {
	t.Helper()
	mediaRoot = t.TempDir()
	libRoot = filepath.Join(mediaRoot, "books")
	for _, d := range []string{libRoot, filepath.Join(libRoot, ".hidden"), filepath.Join(libRoot, "@eaDir")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestEPUB(t, filepath.Join(libRoot, "real.epub"))
	writeZip(t, filepath.Join(libRoot, "comic.cbz"), map[string]string{"p1.jpg": "x", "p2.jpg": "x", "p3.jpg": "x"})
	if err := os.WriteFile(filepath.Join(libRoot, "doc.pdf"),
		[]byte("%PDF-1.4\n<< /Title (Paper Title) /Author (Doc Author) >>\n%%EOF\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libRoot, "Old_Kindle_Book.mobi"), []byte("mobibytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libRoot, "broken.epub"), []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, junk := range []string{"notes.txt", ".dotfile.epub", ".hidden/inner.epub", "@eaDir/thumb.epub"} {
		if err := os.WriteFile(filepath.Join(libRoot, junk), []byte("junk"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return mediaRoot, libRoot
}

func TestScanBooks(t *testing.T) {
	db := openTestDB(t)
	mediaRoot, libRoot := seedRoot(t)
	libID := seedLibrary(t, db, libRoot)
	s := New(config.Config{MediaRoot: mediaRoot, DataDir: t.TempDir()}, db)

	if err := s.ScanBooks(context.Background(), seedScanJob(t, db, libID), libID); err != nil {
		t.Fatalf("ScanBooks: %v", err)
	}

	type row struct {
		format, title, author string
		pages                 int
	}
	got := map[string]row{}
	rows, err := db.Query("SELECT abs_path, format, title, author, page_count FROM books WHERE library_id=?", libID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p string
		var r row
		if err := rows.Scan(&p, &r.format, &r.title, &r.author, &r.pages); err != nil {
			t.Fatal(err)
		}
		got[filepath.Base(p)] = r
	}
	rows.Close()

	if len(got) != 5 {
		t.Fatalf("rows = %d (%v), want 5 (junk + hidden skipped)", len(got), got)
	}
	if r := got["real.epub"]; r.format != "epub" || r.title != "Scanned Book" || r.author != "Scan Author" || r.pages != 2 {
		t.Fatalf("epub row = %+v", r)
	}
	if r := got["comic.cbz"]; r.format != "cbz" || r.pages != 3 {
		t.Fatalf("cbz row = %+v", r)
	}
	if r := got["doc.pdf"]; r.format != "pdf" || r.title != "Paper Title" || r.author != "Doc Author" {
		t.Fatalf("pdf row = %+v", r)
	}
	if r := got["Old_Kindle_Book.mobi"]; r.format != "mobi" || r.title != "Old Kindle Book" {
		t.Fatalf("tier-2 row = %+v (filename title, underscores → spaces)", r)
	}
	if r := got["broken.epub"]; r.format != "" || r.title != "broken" {
		t.Fatalf("corrupt row = %+v (degrades to unreadable, never fails the scan)", r)
	}
}

func TestScanBooksUnchangedFastPathAndRescan(t *testing.T) {
	db := openTestDB(t)
	mediaRoot, libRoot := seedRoot(t)
	libID := seedLibrary(t, db, libRoot)
	s := New(config.Config{MediaRoot: mediaRoot, DataDir: t.TempDir()}, db)
	ctx := context.Background()

	if err := s.ScanBooks(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatal(err)
	}
	// Simulate the thumb job having run, then rescan unchanged: thumb_path
	// must survive (the fast-path skips the upsert whose CASE would reset it).
	if _, err := db.Exec("UPDATE books SET thumb_path='x.webp' WHERE format='epub'"); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanBooks(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatal(err)
	}
	var thumb string
	if err := db.QueryRow("SELECT thumb_path FROM books WHERE format='epub'").Scan(&thumb); err != nil {
		t.Fatal(err)
	}
	if thumb != "x.webp" {
		t.Fatalf("thumb after unchanged rescan = %q, want preserved", thumb)
	}

	// Change the file's bytes: the row re-parses and the thumb resets to
	// pending so the chained job regenerates it.
	p := filepath.Join(libRoot, "real.epub")
	writeTestEPUB(t, p)
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanBooks(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT thumb_path FROM books WHERE format='epub'").Scan(&thumb); err != nil {
		t.Fatal(err)
	}
	if thumb != "" {
		t.Fatalf("thumb after byte change = %q, want '' (pending)", thumb)
	}
}

func TestScanBooksMoveRelinkTransfersProgress(t *testing.T) {
	db := openTestDB(t)
	mediaRoot, libRoot := seedRoot(t)
	libID := seedLibrary(t, db, libRoot)
	s := New(config.Config{MediaRoot: mediaRoot, DataDir: t.TempDir()}, db)
	ctx := context.Background()

	if err := s.ScanBooks(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatal(err)
	}
	var oldID int64
	if err := db.QueryRow("SELECT id FROM books WHERE format='epub'").Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO book_reading_progress (book_id, spine_index, scroll_fraction) VALUES (?, 1, 0.5)", oldID); err != nil {
		t.Fatal(err)
	}

	// A plain rename preserves size+mtime — the relink signature.
	oldPath := filepath.Join(libRoot, "real.epub")
	newPath := filepath.Join(libRoot, "renamed.epub")
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanBooks(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatal(err)
	}

	var newID int64
	if err := db.QueryRow("SELECT id FROM books WHERE abs_path=?", newPath).Scan(&newID); err != nil {
		t.Fatal(err)
	}
	var spine int
	var frac float64
	if err := db.QueryRow(
		"SELECT spine_index, scroll_fraction FROM book_reading_progress WHERE book_id=?", newID).Scan(&spine, &frac); err != nil {
		t.Fatalf("progress not transferred: %v", err)
	}
	if spine != 1 || frac != 0.5 {
		t.Fatalf("progress = %d/%v, want 1/0.5", spine, frac)
	}
	var oldCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM books WHERE id=?", oldID).Scan(&oldCount)
	if oldCount != 0 {
		t.Fatal("orphan row survived prune")
	}
}

func TestScanBooksPruneGuard(t *testing.T) {
	db := openTestDB(t)
	mediaRoot, libRoot := seedRoot(t)
	libID := seedLibrary(t, db, libRoot)
	s := New(config.Config{MediaRoot: mediaRoot, DataDir: t.TempDir()}, db)
	ctx := context.Background()

	if err := s.ScanBooks(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatal(err)
	}
	var before int
	_ = db.QueryRow("SELECT COUNT(*) FROM books WHERE library_id=?", libID).Scan(&before)
	if before == 0 {
		t.Fatal("precondition: rows exist")
	}

	// Empty the root in place — an unmounted mount point looks exactly like
	// this. The scan must keep every row.
	entries, err := os.ReadDir(libRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(libRoot, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ScanBooks(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatal(err)
	}
	var after int
	_ = db.QueryRow("SELECT COUNT(*) FROM books WHERE library_id=?", libID).Scan(&after)
	if after != before {
		t.Fatalf("rows after empty-root scan = %d, want %d (prune guard)", after, before)
	}
}

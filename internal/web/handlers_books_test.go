package web

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func insertBook(t *testing.T, db *sql.DB, absPath, format, title, author string, pages int) int64 {
	t.Helper()
	var libID int64
	if err := db.QueryRow("SELECT id FROM libraries WHERE type='books'").Scan(&libID); err != nil {
		res, err := db.Exec("INSERT INTO libraries(name,type,root_path) VALUES('Books','books','/b')")
		if err != nil {
			t.Fatalf("insert library: %v", err)
		}
		libID, _ = res.LastInsertId()
	}
	res, err := db.Exec(
		"INSERT INTO books(library_id,abs_path,format,title,author,page_count) VALUES(?,?,?,?,?,?)",
		libID, absPath, format, title, author, pages)
	if err != nil {
		t.Fatalf("insert book: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// writeTestCBZ builds a small comic archive on disk (for the asset route,
// which opens the real file).
func writeTestCBZ(t *testing.T, path string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"p1.jpg", "p2.jpg"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("imagebytes-" + name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBooksHomeGridAndFragment(t *testing.T) {
	h, db := newTestHandler(t)
	insertBook(t, db, "/b/alpha.epub", "epub", "Alpha", "Ann", 3)
	insertBook(t, db, "/b/beta.cbz", "cbz", "Beta", "", 10)

	req := httptest.NewRequest(http.MethodGet, "/books", nil)
	rr := httptest.NewRecorder()
	h.booksHome(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Alpha") || !strings.Contains(body, "Beta") {
		t.Fatalf("grid missing cards: %s", body)
	}

	// The ?grid=1 fragment renders just the cards block (no layout).
	req = httptest.NewRequest(http.MethodGet, "/books?grid=1", nil)
	rr = httptest.NewRecorder()
	h.booksHome(rr, req)
	frag := rr.Body.String()
	if !strings.Contains(frag, "Alpha") || strings.Contains(frag, "<body") {
		t.Fatalf("fragment wrong shape: %s", frag)
	}
}

func TestBookViewAndReaderStates(t *testing.T) {
	h, db := newTestHandler(t)
	readable := insertBook(t, db, "/b/read.epub", "epub", "Readable", "A", 3)
	broken := insertBook(t, db, "/b/broken.epub", "", "Broken", "", 0)

	get := func(url string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rr := httptest.NewRecorder()
		if strings.HasPrefix(url, "/books/view") {
			h.bookView(rr, req)
		} else {
			h.bookReader(rr, req)
		}
		return rr
	}

	if rr := get("/books/view?id=" + strconv.FormatInt(readable, 10)); !strings.Contains(rr.Body.String(), ">Read<") {
		t.Fatalf("readable book must offer Read: %s", rr.Body.String())
	}
	if rr := get("/books/view?id=" + strconv.FormatInt(broken, 10)); strings.Contains(rr.Body.String(), "class=\"read\"") {
		t.Fatalf("unreadable book must not offer Read: %s", rr.Body.String())
	}
	// With stored progress the button becomes Resume at the stored unit.
	if _, err := db.Exec("INSERT INTO book_reading_progress(book_id,spine_index,scroll_fraction) VALUES(?,1,0.4)", readable); err != nil {
		t.Fatal(err)
	}
	if rr := get("/books/view?id=" + strconv.FormatInt(readable, 10)); !strings.Contains(rr.Body.String(), "Resume Chapter 2") {
		t.Fatalf("progress must surface as Resume: %s", rr.Body.String())
	}
	// The reader redirects an unreadable format back to the detail page.
	if rr := get("/book/reader?id=" + strconv.FormatInt(broken, 10)); rr.Code != http.StatusSeeOther {
		t.Fatalf("reader on unreadable = %d, want 303", rr.Code)
	}
	if rr := get("/books/view?id=999999"); rr.Code != http.StatusNotFound {
		t.Fatalf("missing id = %d, want 404", rr.Code)
	}
}

func TestBookReaderResumesRealCBZ(t *testing.T) {
	h, db := newTestHandler(t)
	p := filepath.Join(h.cfg.MediaRoot, "comic.cbz")
	writeTestCBZ(t, p)
	id := insertBook(t, db, p, "cbz", "Comic", "", 2)
	if _, err := db.Exec("INSERT INTO book_reading_progress(book_id,spine_index) VALUES(?,1)", id); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/book/reader?id="+strconv.FormatInt(id, 10), nil)
	rr := httptest.NewRecorder()
	h.bookReader(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-kind="cbz"`) || !strings.Contains(body, `data-start-index="1"`) {
		t.Fatalf("reader must resume at the stored page: %s", body)
	}
	if !strings.Contains(body, "p1.jpg") || !strings.Contains(body, "p2.jpg") {
		t.Fatalf("reader must carry the page list: %s", body)
	}

	// ?begin=1 starts from the top regardless of stored progress.
	req = httptest.NewRequest(http.MethodGet, "/book/reader?id="+strconv.FormatInt(id, 10)+"&begin=1", nil)
	rr = httptest.NewRecorder()
	h.bookReader(rr, req)
	if !strings.Contains(rr.Body.String(), `data-start-index="0"`) {
		t.Fatalf("begin=1 must start at 0: %s", rr.Body.String())
	}
}

func TestBookAssetServesZipEntriesOnly(t *testing.T) {
	h, db := newTestHandler(t)
	p := filepath.Join(h.cfg.MediaRoot, "comic.cbz")
	writeTestCBZ(t, p)
	id := insertBook(t, db, p, "cbz", "Comic", "", 2)
	pdfID := insertBook(t, db, "/b/doc.pdf", "pdf", "Doc", "", 0)

	get := func(url string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rr := httptest.NewRecorder()
		h.bookAsset(rr, req)
		return rr
	}
	base := "/book/asset/" + strconv.FormatInt(id, 10) + "/"

	rr := get(base + "p1.jpg")
	if rr.Code != http.StatusOK || rr.Body.String() != "imagebytes-p1.jpg" {
		t.Fatalf("asset = %d %q", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("asset must serve nosniff")
	}
	if rr := get(base + "missing.jpg"); rr.Code != http.StatusNotFound {
		t.Fatalf("missing entry = %d, want 404", rr.Code)
	}
	if rr := get(base + "../../../etc/passwd"); rr.Code != http.StatusNotFound {
		t.Fatalf("traversal name = %d, want 404 (zip lookup, not filesystem)", rr.Code)
	}
	// Only zip-backed formats have entries; a PDF's assets 404.
	if rr := get("/book/asset/" + strconv.FormatInt(pdfID, 10) + "/x.jpg"); rr.Code != http.StatusNotFound {
		t.Fatalf("pdf asset = %d, want 404", rr.Code)
	}
}

func TestBookReadingProgressUpsert(t *testing.T) {
	h, db := newTestHandler(t)
	id := insertBook(t, db, "/b/x.epub", "epub", "X", "", 5)

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/book/reading-progress", strings.NewReader(body))
		rr := httptest.NewRecorder()
		h.bookReadingProgress(rr, req)
		return rr
	}
	idStr := strconv.FormatInt(id, 10)

	if rr := post(`{"book_id":` + idStr + `,"spine_index":2,"scroll_fraction":0.5,"completed":true}`); rr.Code != http.StatusOK {
		t.Fatalf("post = %d (%s)", rr.Code, rr.Body.String())
	}
	// A later beacon with completed:false must not revoke the flag (earn-only),
	// and out-of-range fractions clamp.
	if rr := post(`{"book_id":` + idStr + `,"spine_index":3,"scroll_fraction":1.7,"completed":false}`); rr.Code != http.StatusOK {
		t.Fatalf("post = %d", rr.Code)
	}
	var spine, completed int
	var frac float64
	if err := db.QueryRow(
		"SELECT spine_index, scroll_fraction, completed FROM book_reading_progress WHERE book_id=?", id,
	).Scan(&spine, &frac, &completed); err != nil {
		t.Fatal(err)
	}
	if spine != 3 || frac != 1 || completed != 1 {
		t.Fatalf("progress = %d/%v/%d, want 3/1/1 (position updates, completed sticks)", spine, frac, completed)
	}

	if rr := post(`{"book_id":999999,"spine_index":0}`); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown book = %d, want 404", rr.Code)
	}
	if rr := post(`not json`); rr.Code != http.StatusBadRequest {
		t.Fatalf("garbage = %d, want 400", rr.Code)
	}
}

package bookscan

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// writeZip builds a zip file from name→content pairs.
func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

const testContainerXML = `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

func testEPUB(t *testing.T, opf string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "book.epub")
	writeZip(t, p, map[string]string{
		"mimetype":               "application/epub+zip",
		"META-INF/container.xml": testContainerXML,
		"OEBPS/content.opf":      opf,
		"OEBPS/ch1.xhtml":        "<html><body>one</body></html>",
		"OEBPS/ch2.xhtml":        "<html><body>two</body></html>",
		"OEBPS/img/cover.jpg":    "jpegbytes",
	})
	return p
}

func TestParseEPUB3(t *testing.T) {
	p := testEPUB(t, `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>A Test Book</dc:title>
    <dc:creator>Ann Author</dc:creator>
  </metadata>
  <manifest>
    <item id="cover" href="img/cover.jpg" media-type="image/jpeg" properties="cover-image"/>
    <item id="c1" href="ch1.xhtml" media-type="application/xhtml+xml"/>
    <item id="c2" href="ch2.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="c1"/><itemref idref="c2"/></spine>
</package>`)
	m, err := ParseEPUB(p)
	if err != nil {
		t.Fatalf("ParseEPUB: %v", err)
	}
	if m.Title != "A Test Book" || m.Author != "Ann Author" {
		t.Fatalf("meta = %q / %q", m.Title, m.Author)
	}
	if m.CoverEntry != "OEBPS/img/cover.jpg" {
		t.Fatalf("cover = %q", m.CoverEntry)
	}
	if len(m.Spine) != 2 || m.Spine[0] != "OEBPS/ch1.xhtml" || m.Spine[1] != "OEBPS/ch2.xhtml" {
		t.Fatalf("spine = %v", m.Spine)
	}
}

func TestParseEPUB2CoverMeta(t *testing.T) {
	// EPUB 2 has no cover-image property — the cover rides a <meta name="cover">.
	p := testEPUB(t, `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Old Book</dc:title>
    <meta name="cover" content="coverimg"/>
  </metadata>
  <manifest>
    <item id="coverimg" href="img/cover.jpg" media-type="image/jpeg"/>
    <item id="c1" href="ch1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="c1"/></spine>
</package>`)
	m, err := ParseEPUB(p)
	if err != nil {
		t.Fatalf("ParseEPUB: %v", err)
	}
	if m.CoverEntry != "OEBPS/img/cover.jpg" {
		t.Fatalf("cover = %q", m.CoverEntry)
	}
	if m.Author != "" {
		t.Fatalf("author = %q, want empty", m.Author)
	}
}

func TestParseEPUBRejectsGarbage(t *testing.T) {
	// A spine idref pointing at a missing manifest item / a missing file must
	// be dropped; an all-missing spine is an error, not a panic.
	p := filepath.Join(t.TempDir(), "bad.epub")
	writeZip(t, p, map[string]string{
		"META-INF/container.xml": testContainerXML,
		"OEBPS/content.opf": `<package><manifest>
			<item id="c1" href="missing.xhtml" media-type="application/xhtml+xml"/>
		</manifest><spine><itemref idref="c1"/><itemref idref="ghost"/></spine></package>`,
	})
	if _, err := ParseEPUB(p); err == nil {
		t.Fatal("want error for a spine with no resolvable entries")
	}
	// Not a zip at all.
	notZip := filepath.Join(t.TempDir(), "not.epub")
	if err := os.WriteFile(notZip, []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseEPUB(notZip); err == nil {
		t.Fatal("want error for a non-zip file")
	}
}

func TestCBZPagesNaturalOrder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "comic.cbz")
	writeZip(t, p, map[string]string{
		"page10.jpg":        "x",
		"page2.jpg":         "x",
		"page1.png":         "x",
		"__MACOSX/page.jpg": "junk",
		".hidden.jpg":       "junk",
		"notes.txt":         "junk",
	})
	pages, err := CBZPages(p)
	if err != nil {
		t.Fatalf("CBZPages: %v", err)
	}
	want := []string{"page1.png", "page2.jpg", "page10.jpg"}
	if len(pages) != len(want) {
		t.Fatalf("pages = %v, want %v", pages, want)
	}
	for i := range want {
		if pages[i] != want[i] {
			t.Fatalf("pages = %v, want %v", pages, want)
		}
	}
}

func TestCBZPagesEmptyIsError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty.cbz")
	writeZip(t, p, map[string]string{"readme.txt": "no images"})
	if _, err := CBZPages(p); err == nil {
		t.Fatal("want error for an image-less archive")
	}
}

func TestNaturalLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"page2", "page10", true},
		{"page10", "page2", false},
		{"a", "b", true},
		{"ch1/p1.jpg", "ch1/p2.jpg", true},
		{"007", "8", true},
		{"x", "x1", true},
		{"Page2", "page10", true}, // case-insensitive
	}
	for _, c := range cases {
		if got := naturalLess(c.a, c.b); got != c.want {
			t.Fatalf("naturalLess(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestZipEntry(t *testing.T) {
	p := filepath.Join(t.TempDir(), "z.epub")
	writeZip(t, p, map[string]string{"OEBPS/ch1.xhtml": "hello"})
	rc, err := ZipEntry(p, "OEBPS/ch1.xhtml")
	if err != nil {
		t.Fatalf("ZipEntry: %v", err)
	}
	buf := make([]byte, 16)
	n, _ := rc.Read(buf)
	if string(buf[:n]) != "hello" {
		t.Fatalf("content = %q", buf[:n])
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ZipEntry(p, "../etc/passwd"); err == nil {
		t.Fatal("want error for a name not in the archive")
	}
}

func TestPDFMeta(t *testing.T) {
	// A minimal classic PDF shape: header + an Info dict with literal strings.
	pdf := "%PDF-1.4\n1 0 obj\n<< /Title (A \\(Real\\) Title) /Author (Bob Writer) >>\nendobj\ntrailer\n<< /Info 1 0 R >>\n%%EOF\n"
	p := filepath.Join(t.TempDir(), "doc.pdf")
	if err := os.WriteFile(p, []byte(pdf), 0o644); err != nil {
		t.Fatal(err)
	}
	title, author := PDFMeta(p)
	if title != "A (Real) Title" || author != "Bob Writer" {
		t.Fatalf("meta = %q / %q", title, author)
	}
}

func TestPDFMetaHexUTF16(t *testing.T) {
	// /Title as a UTF-16BE hex string with BOM: FEFF 0054 0069 = "Ti…".
	pdf := "%PDF-1.4\n<< /Title <FEFF00540069007400720065> >>\n%%EOF\n"
	p := filepath.Join(t.TempDir(), "hex.pdf")
	if err := os.WriteFile(p, []byte(pdf), 0o644); err != nil {
		t.Fatal(err)
	}
	title, _ := PDFMeta(p)
	if title != "Titre" {
		t.Fatalf("title = %q, want Titre", title)
	}
}

func TestPDFMetaGarbage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "not.pdf")
	if err := os.WriteFile(p, []byte("not a pdf at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if title, author := PDFMeta(p); title != "" || author != "" {
		t.Fatalf("meta = %q / %q, want empty", title, author)
	}
}

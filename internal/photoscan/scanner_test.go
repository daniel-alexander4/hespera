package photoscan

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
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
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

func seedLibrary(t *testing.T, db *sql.DB, name, libType, rootPath string) int64 {
	t.Helper()
	res, err := db.Exec(
		"INSERT INTO libraries (name, type, root_path) VALUES (?, ?, ?)",
		name, libType, rootPath,
	)
	if err != nil {
		t.Fatalf("seedLibrary: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func newTestScanner(t *testing.T, db *sql.DB, mediaRoot string) *Scanner {
	t.Helper()
	return New(config.Config{MediaRoot: mediaRoot, DataDir: t.TempDir()}, db)
}

func seedScanJob(t *testing.T, db *sql.DB, libID int64) int64 {
	t.Helper()
	res, err := db.Exec("INSERT INTO scan_jobs (library_id, job_type) VALUES (?, 'photo_scan')", libID)
	if err != nil {
		t.Fatalf("seedScanJob: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestScanPhotosIngest(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	root := t.TempDir()
	libID := seedLibrary(t, db, "photos", "home_media", root)
	s := newTestScanner(t, db, root)

	// A JPEG with EXIF (capture time + orientation), a plain PNG, a clip with
	// a video extension (probe fails on fake bytes — mtime fallback), junk to
	// skip: a dot-file, an unknown extension, files under a dot-dir and @eaDir.
	exifJPEG := wrapJPEG(buildTIFF(t, true, 6, "", "2019:07:04 12:30:45"))
	write := func(rel string, b []byte) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("2019/Vacation/IMG_1.jpg", exifJPEG)
	write("2019/Vacation/IMG_2.png", []byte("png"))
	write("clips/family.avi", []byte("avi"))
	write("clips/camcorder.mts", []byte("mts"))
	write("._IMG_1.jpg", []byte("appledouble"))
	write("notes.txt", []byte("text"))
	write(".hidden/skipme.jpg", []byte("x"))
	write("@eaDir/thumbs.jpg", []byte("x"))

	if err := s.ScanPhotos(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatalf("ScanPhotos: %v", err)
	}

	rows := map[string]struct {
		kind, takenSource string
		orientation       int
	}{}
	rs, err := db.Query("SELECT abs_path, kind, taken_source, orientation FROM photos WHERE library_id=?", libID)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	for rs.Next() {
		var p, k, src string
		var o int
		if err := rs.Scan(&p, &k, &src, &o); err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, p)
		rows[rel] = struct {
			kind, takenSource string
			orientation       int
		}{k, src, o}
	}
	if len(rows) != 4 {
		t.Fatalf("ingested %d rows, want 4: %v", len(rows), rows)
	}
	if r := rows["2019/Vacation/IMG_1.jpg"]; r.kind != "photo" || r.takenSource != "exif" || r.orientation != 6 {
		t.Fatalf("EXIF jpeg row wrong: %+v", r)
	}
	if r := rows["2019/Vacation/IMG_2.png"]; r.kind != "photo" || r.takenSource != "mtime" {
		t.Fatalf("plain png row wrong: %+v", r)
	}
	if r := rows["clips/family.avi"]; r.kind != "video" || r.takenSource != "mtime" {
		t.Fatalf("avi row wrong: %+v", r)
	}
	if r := rows["clips/camcorder.mts"]; r.kind != "video" {
		t.Fatalf("mts row wrong: %+v", r)
	}

	// dir_rel drives the Folders grouping.
	var dirRel string
	if err := db.QueryRow("SELECT dir_rel FROM photos WHERE abs_path=?", filepath.Join(root, "2019/Vacation/IMG_1.jpg")).Scan(&dirRel); err != nil {
		t.Fatal(err)
	}
	if dirRel != filepath.Join("2019", "Vacation") {
		t.Fatalf("dir_rel = %q", dirRel)
	}

	// EXIF taken_at value lands formatted for lexicographic date ordering.
	var takenAt string
	if err := db.QueryRow("SELECT taken_at FROM photos WHERE abs_path=?", filepath.Join(root, "2019/Vacation/IMG_1.jpg")).Scan(&takenAt); err != nil {
		t.Fatal(err)
	}
	if takenAt != "2019-07-04 12:30:45" {
		t.Fatalf("taken_at = %q", takenAt)
	}
}

func TestScanPhotosUnchangedFastPathAndThumbReset(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	root := t.TempDir()
	libID := seedLibrary(t, db, "photos", "home_media", root)
	s := newTestScanner(t, db, root)

	p := filepath.Join(root, "a.jpg")
	if err := os.WriteFile(p, wrapJPEG(buildTIFF(t, true, 1, "", "2019:07:04 12:30:45")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanPhotos(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatal(err)
	}
	// Simulate a generated thumb, rescan unchanged: preserved.
	if _, err := db.Exec("UPDATE photos SET thumb_path='photo_1.webp'"); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanPhotos(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatal(err)
	}
	var thumb string
	if err := db.QueryRow("SELECT thumb_path FROM photos WHERE abs_path=?", p).Scan(&thumb); err != nil {
		t.Fatal(err)
	}
	if thumb != "photo_1.webp" {
		t.Fatalf("unchanged rescan clobbered thumb_path: %q", thumb)
	}
	// Change the bytes (and mtime): thumb resets for regeneration.
	if err := os.WriteFile(p, wrapJPEG(buildTIFF(t, true, 3, "", "2020:01:01 08:00:00")), 0o644); err != nil {
		t.Fatal(err)
	}
	bumped := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, bumped, bumped); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanPhotos(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatal(err)
	}
	var takenAt string
	if err := db.QueryRow("SELECT thumb_path, taken_at FROM photos WHERE abs_path=?", p).Scan(&thumb, &takenAt); err != nil {
		t.Fatal(err)
	}
	if thumb != "" {
		t.Fatalf("changed file must reset thumb_path, got %q", thumb)
	}
	if takenAt != "2020-01-01 08:00:00" {
		t.Fatalf("changed file must re-derive taken_at, got %q", takenAt)
	}
}

func TestRelinkMovedPhoto(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	root := t.TempDir()
	libID := seedLibrary(t, db, "photos", "home_media", root)
	s := newTestScanner(t, db, root)

	oldPath := filepath.Join(root, "old", "clip.avi")
	newPath := filepath.Join(root, "new", "clip.avi")
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	insert := func(p string, size, mtime int64) int64 {
		res, err := db.Exec(`INSERT INTO photos (library_id, abs_path, kind, file_size_bytes, mtime_unix) VALUES (?, ?, 'video', ?, ?)`,
			libID, p, size, mtime)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	oldID := insert(oldPath, 42, 1000)
	newID := insert(newPath, 42, 1000)
	if _, err := db.Exec("INSERT INTO photo_playback_progress (file_id, position_seconds, duration_seconds) VALUES (?, 123, 456)", oldID); err != nil {
		t.Fatal(err)
	}

	if err := s.relinkMovedFiles(ctx, libID, root); err != nil {
		t.Fatal(err)
	}
	var pos float64
	if err := db.QueryRow("SELECT position_seconds FROM photo_playback_progress WHERE file_id=?", newID).Scan(&pos); err != nil {
		t.Fatalf("progress not transferred: %v", err)
	}
	if pos != 123 {
		t.Fatalf("position = %v, want 123", pos)
	}

	if err := s.pruneMissingFiles(ctx, libID, root); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM photos WHERE library_id=?", libID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("after prune rows = %d, want 1 (survivor only)", n)
	}
}

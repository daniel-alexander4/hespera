package audiobookscan

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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
	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('Audiobooks', 'audiobooks', ?)", rootPath)
	if err != nil {
		t.Fatalf("seedLibrary: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedScanJob(t *testing.T, db *sql.DB, libID int64) int64 {
	t.Helper()
	res, err := db.Exec("INSERT INTO scan_jobs (library_id, job_type) VALUES (?, 'audiobook_scan')", libID)
	if err != nil {
		t.Fatalf("seedScanJob: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestScanAudiobooksDegrade covers the no-real-media path: garbage bytes in an
// .m4b probe-fail into a listed row (filename title, empty probe) — never a
// failed scan — while junk and hidden files are skipped.
func TestScanAudiobooksDegrade(t *testing.T) {
	db := openTestDB(t)
	mediaRoot := t.TempDir()
	libRoot := filepath.Join(mediaRoot, "audiobooks")
	if err := os.MkdirAll(filepath.Join(libRoot, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"A_Fake_Book.m4b":   "not real audio",
		"notes.txt":         "junk",
		".dot.m4b":          "junk",
		".hidden/inner.m4b": "junk",
		"Another Story.mp3": "also not audio",
	} {
		if err := os.WriteFile(filepath.Join(libRoot, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	libID := seedLibrary(t, db, libRoot)
	s := New(config.Config{MediaRoot: mediaRoot, DataDir: t.TempDir()}, db)
	if err := s.ScanAudiobooks(context.Background(), seedScanJob(t, db, libID), libID); err != nil {
		t.Fatalf("ScanAudiobooks: %v", err)
	}

	rows, err := db.Query("SELECT title, container, stream_info_json FROM audiobooks WHERE library_id=? ORDER BY title", libID)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ title, container, probe string }
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.title, &r.container, &r.probe); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	rows.Close()
	if len(got) != 2 {
		t.Fatalf("rows = %+v, want 2 (junk + hidden skipped)", got)
	}
	if got[0].title != "A Fake Book" || got[0].container != "m4b" || got[0].probe != "{}" {
		t.Fatalf("degraded row = %+v (filename title, underscores → spaces, empty probe)", got[0])
	}
	if got[1].title != "Another Story" || got[1].container != "mp3" {
		t.Fatalf("mp3 row = %+v", got[1])
	}
}

func TestScanAudiobooksPruneGuardAndRelink(t *testing.T) {
	db := openTestDB(t)
	mediaRoot := t.TempDir()
	libRoot := filepath.Join(mediaRoot, "audiobooks")
	if err := os.MkdirAll(libRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(libRoot, "book.m4b")
	if err := os.WriteFile(p, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	libID := seedLibrary(t, db, libRoot)
	s := New(config.Config{MediaRoot: mediaRoot, DataDir: t.TempDir()}, db)
	ctx := context.Background()
	if err := s.ScanAudiobooks(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatal(err)
	}
	var oldID int64
	if err := db.QueryRow("SELECT id FROM audiobooks WHERE library_id=?", libID).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO audiobook_playback_progress (file_id, position_seconds, duration_seconds) VALUES (?, 1234.5, 7200)", oldID); err != nil {
		t.Fatal(err)
	}

	// A plain rename preserves (size, mtime) — the relink transfers the resume.
	newPath := filepath.Join(libRoot, "renamed.m4b")
	if err := os.Rename(p, newPath); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanAudiobooks(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatal(err)
	}
	var newID int64
	if err := db.QueryRow("SELECT id FROM audiobooks WHERE abs_path=?", newPath).Scan(&newID); err != nil {
		t.Fatal(err)
	}
	var pos float64
	if err := db.QueryRow("SELECT position_seconds FROM audiobook_playback_progress WHERE file_id=?", newID).Scan(&pos); err != nil {
		t.Fatalf("progress not transferred: %v", err)
	}
	if pos != 1234.5 {
		t.Fatalf("position = %v, want 1234.5", pos)
	}

	// Empty the root in place: the prune guard must keep the row.
	if err := os.Remove(newPath); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanAudiobooks(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatal(err)
	}
	var count int
	_ = db.QueryRow("SELECT COUNT(*) FROM audiobooks WHERE library_id=?", libID).Scan(&count)
	if count != 1 {
		t.Fatalf("rows after empty-root scan = %d, want 1 (prune guard)", count)
	}
}

// TestScanAudiobooksRealM4B exercises the full metadata path against a real
// ffmpeg-generated chaptered m4b: tags (album = book title, artist = author),
// chapters, duration, and the embedded-cover thumb.
func TestScanAudiobooksRealM4B(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed; skipping integration test")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed; skipping integration test")
	}
	dir := t.TempDir()
	meta := ";FFMETADATA1\ntitle=The Audio Test\nartist=Nora Rator\nalbum=The Audio Test\n" +
		"[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=10000\ntitle=Chapter One\n" +
		"[CHAPTER]\nTIMEBASE=1/1000\nSTART=10000\nEND=20000\ntitle=Chapter Two\n"
	metaPath := filepath.Join(dir, "meta.txt")
	if err := os.WriteFile(metaPath, []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	coverPath := filepath.Join(dir, "cover.jpg")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=darkorange:size=400x400:duration=1", "-frames:v", "1", coverPath).CombinedOutput(); err != nil {
		t.Fatalf("cover: %v: %s", err, out)
	}

	mediaRoot := t.TempDir()
	libRoot := filepath.Join(mediaRoot, "audiobooks")
	if err := os.MkdirAll(libRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	m4bPath := filepath.Join(libRoot, "the-audio-test.m4b")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=20",
		"-i", metaPath, "-i", coverPath,
		"-map", "0:a", "-map", "2:v", "-map_metadata", "1", "-map_chapters", "1",
		"-c:a", "aac", "-c:v", "mjpeg", "-disposition:v:0", "attached_pic",
		"-f", "mp4", m4bPath).CombinedOutput(); err != nil {
		t.Fatalf("m4b: %v: %s", err, out)
	}

	db := openTestDB(t)
	libID := seedLibrary(t, db, libRoot)
	s := New(config.Config{MediaRoot: mediaRoot, DataDir: t.TempDir()}, db)
	ctx := context.Background()
	if err := s.ScanAudiobooks(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatalf("ScanAudiobooks: %v", err)
	}

	var id int64
	var title, author string
	var dur float64
	var chapters int
	if err := db.QueryRow(
		"SELECT id, title, author, duration_seconds, chapter_count FROM audiobooks WHERE library_id=?", libID,
	).Scan(&id, &title, &author, &dur, &chapters); err != nil {
		t.Fatal(err)
	}
	if title != "The Audio Test" || author != "Nora Rator" {
		t.Fatalf("identity = %q / %q", title, author)
	}
	if chapters != 2 {
		t.Fatalf("chapters = %d, want 2", chapters)
	}
	if dur < 19 || dur > 21 {
		t.Fatalf("duration = %v, want ~20", dur)
	}

	if err := s.GenerateThumbsMissing(ctx, seedScanJob(t, db, libID), libID); err != nil {
		t.Fatalf("GenerateThumbsMissing: %v", err)
	}
	var thumb string
	if err := db.QueryRow("SELECT thumb_path FROM audiobooks WHERE id=?", id).Scan(&thumb); err != nil {
		t.Fatal(err)
	}
	if thumb == "" || thumb == "unavailable" {
		t.Fatalf("thumb = %q, want a generated cover", thumb)
	}
	if _, err := os.Stat(thumb); err != nil {
		t.Fatalf("thumb file missing: %v", err)
	}
}

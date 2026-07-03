package tvscan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

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

func TestUpsertTVFile(t *testing.T) {
	ctx := context.Background()

	t.Run("insert new file", func(t *testing.T) {
		db := openTestDB(t)
		libID := seedLibrary(t, db, "tv1", "tv", "/media/tv")
		s := &Scanner{DB: db}

		ident := &EpisodeIdentity{
			ShowTitle:      "Breaking Bad",
			SeasonNumber:   1,
			EpisodeNumbers: []int{1},
			Confidence:     0.72,
			Method:         "sxe",
		}
		err := s.upsertTVFile(ctx, libID, "/media/tv/breaking.bad.s01e01.mkv", "mkv", 1024, 1700000000, "{}", ident, extraFields{})
		if err != nil {
			t.Fatalf("upsertTVFile: %v", err)
		}

		// Verify tv_series_files row.
		var container string
		var fileSize, mtime int64
		err = db.QueryRow(
			"SELECT container, file_size_bytes, mtime_unix FROM tv_series_files WHERE library_id=? AND abs_path=?",
			libID, "/media/tv/breaking.bad.s01e01.mkv",
		).Scan(&container, &fileSize, &mtime)
		if err != nil {
			t.Fatalf("query tv_series_files: %v", err)
		}
		if container != "mkv" {
			t.Fatalf("container = %q, want mkv", container)
		}
		if fileSize != 1024 {
			t.Fatalf("file_size_bytes = %d, want 1024", fileSize)
		}
		if mtime != 1700000000 {
			t.Fatalf("mtime_unix = %d, want 1700000000", mtime)
		}

		// Verify tv_series_identities row.
		var status, title, epCSV, method string
		var season int
		var confidence float64
		err = db.QueryRow(`
			SELECT status, guessed_title, season_number, episode_numbers_csv, match_confidence, match_method
			FROM tv_series_identities WHERE file_id = (
				SELECT id FROM tv_series_files WHERE library_id=? AND abs_path=?
			)`, libID, "/media/tv/breaking.bad.s01e01.mkv",
		).Scan(&status, &title, &season, &epCSV, &confidence, &method)
		if err != nil {
			t.Fatalf("query tv_series_identities: %v", err)
		}
		if status != "unmatched" {
			t.Fatalf("status = %q, want unmatched", status)
		}
		if title != "Breaking Bad" {
			t.Fatalf("guessed_title = %q, want Breaking Bad", title)
		}
		if season != 1 {
			t.Fatalf("season_number = %d, want 1", season)
		}
		if epCSV != "1" {
			t.Fatalf("episode_numbers_csv = %q, want 1", epCSV)
		}
		if confidence != 0.72 {
			t.Fatalf("match_confidence = %f, want 0.72", confidence)
		}
		if method != "sxe" {
			t.Fatalf("match_method = %q, want sxe", method)
		}
	})

	t.Run("nil identity", func(t *testing.T) {
		db := openTestDB(t)
		libID := seedLibrary(t, db, "tv2", "tv", "/media/tv")
		s := &Scanner{DB: db}

		err := s.upsertTVFile(ctx, libID, "/media/tv/unknown.mkv", "mkv", 512, 1700000000, "{}", nil, extraFields{})
		if err != nil {
			t.Fatalf("upsertTVFile nil: %v", err)
		}

		var status, title, epCSV string
		var season int
		err = db.QueryRow(`
			SELECT status, guessed_title, season_number, episode_numbers_csv
			FROM tv_series_identities WHERE file_id = (
				SELECT id FROM tv_series_files WHERE library_id=? AND abs_path=?
			)`, libID, "/media/tv/unknown.mkv",
		).Scan(&status, &title, &season, &epCSV)
		if err != nil {
			t.Fatalf("query identity: %v", err)
		}
		if status != "unmatched" {
			t.Fatalf("status = %q, want unmatched", status)
		}
		if title != "" {
			t.Fatalf("guessed_title = %q, want empty", title)
		}
		if season != -1 {
			t.Fatalf("season_number = %d, want -1", season)
		}
		if epCSV != "" {
			t.Fatalf("episode_numbers_csv = %q, want empty", epCSV)
		}
	})

	t.Run("update on conflict", func(t *testing.T) {
		db := openTestDB(t)
		libID := seedLibrary(t, db, "tv3", "tv", "/media/tv")
		s := &Scanner{DB: db}

		ident := &EpisodeIdentity{ShowTitle: "Show", SeasonNumber: 1, EpisodeNumbers: []int{1}, Confidence: 0.72, Method: "sxe"}
		path := "/media/tv/show.s01e01.mkv"

		if err := s.upsertTVFile(ctx, libID, path, "mkv", 1024, 1700000000, "{}", ident, extraFields{}); err != nil {
			t.Fatalf("first upsert: %v", err)
		}
		if err := s.upsertTVFile(ctx, libID, path, "mkv", 2048, 1700001000, "{}", ident, extraFields{}); err != nil {
			t.Fatalf("second upsert: %v", err)
		}

		var count int
		db.QueryRow("SELECT COUNT(*) FROM tv_series_files WHERE library_id=? AND abs_path=?", libID, path).Scan(&count)
		if count != 1 {
			t.Fatalf("row count = %d, want 1", count)
		}

		var fileSize int64
		db.QueryRow("SELECT file_size_bytes FROM tv_series_files WHERE library_id=? AND abs_path=?", libID, path).Scan(&fileSize)
		if fileSize != 2048 {
			t.Fatalf("file_size_bytes = %d, want 2048", fileSize)
		}
	})

	t.Run("rescan preserves matched", func(t *testing.T) {
		db := openTestDB(t)
		libID := seedLibrary(t, db, "tv4", "tv", "/media/tv")
		s := &Scanner{DB: db}

		path := "/media/tv/bb.s01e01.mkv"
		ident := &EpisodeIdentity{ShowTitle: "Breaking Bad", SeasonNumber: 1, EpisodeNumbers: []int{1}, Confidence: 0.72, Method: "sxe"}
		if err := s.upsertTVFile(ctx, libID, path, "mkv", 1024, 1700000000, "{}", ident, extraFields{}); err != nil {
			t.Fatalf("initial upsert: %v", err)
		}

		// Simulate user matching the file.
		_, err := db.Exec(`
			UPDATE tv_series_identities SET status='matched', provider='tmdb', series_id='12345'
			WHERE file_id = (SELECT id FROM tv_series_files WHERE library_id=? AND abs_path=?)`,
			libID, path,
		)
		if err != nil {
			t.Fatalf("set matched: %v", err)
		}

		// Rescan with different identity data.
		newIdent := &EpisodeIdentity{ShowTitle: "Different Show", SeasonNumber: 2, EpisodeNumbers: []int{5}, Confidence: 0.72, Method: "sxe"}
		if err := s.upsertTVFile(ctx, libID, path, "mkv", 2048, 1700001000, "{}", newIdent, extraFields{}); err != nil {
			t.Fatalf("rescan upsert: %v", err)
		}

		var status, title, seriesID string
		db.QueryRow(`
			SELECT status, guessed_title, series_id FROM tv_series_identities
			WHERE file_id = (SELECT id FROM tv_series_files WHERE library_id=? AND abs_path=?)`,
			libID, path,
		).Scan(&status, &title, &seriesID)
		if status != "matched" {
			t.Fatalf("status = %q, want matched", status)
		}
		if title != "Breaking Bad" {
			t.Fatalf("guessed_title = %q, want Breaking Bad", title)
		}
		if seriesID != "12345" {
			t.Fatalf("series_id = %q, want 12345", seriesID)
		}
	})

	t.Run("rescan preserves skipped", func(t *testing.T) {
		db := openTestDB(t)
		libID := seedLibrary(t, db, "tv5", "tv", "/media/tv")
		s := &Scanner{DB: db}

		path := "/media/tv/skip.s01e01.mkv"
		ident := &EpisodeIdentity{ShowTitle: "Skipped Show", SeasonNumber: 1, EpisodeNumbers: []int{1}, Confidence: 0.72, Method: "sxe"}
		if err := s.upsertTVFile(ctx, libID, path, "mkv", 1024, 1700000000, "{}", ident, extraFields{}); err != nil {
			t.Fatalf("initial upsert: %v", err)
		}

		db.Exec(`UPDATE tv_series_identities SET status='skipped'
			WHERE file_id = (SELECT id FROM tv_series_files WHERE library_id=? AND abs_path=?)`, libID, path)

		newIdent := &EpisodeIdentity{ShowTitle: "Other", SeasonNumber: 3, EpisodeNumbers: []int{9}, Confidence: 0.72, Method: "sxe"}
		if err := s.upsertTVFile(ctx, libID, path, "mkv", 2048, 1700001000, "{}", newIdent, extraFields{}); err != nil {
			t.Fatalf("rescan upsert: %v", err)
		}

		var status, title string
		db.QueryRow(`SELECT status, guessed_title FROM tv_series_identities
			WHERE file_id = (SELECT id FROM tv_series_files WHERE library_id=? AND abs_path=?)`,
			libID, path,
		).Scan(&status, &title)
		if status != "skipped" {
			t.Fatalf("status = %q, want skipped", status)
		}
		if title != "Skipped Show" {
			t.Fatalf("guessed_title = %q, want Skipped Show", title)
		}
	})

	t.Run("rescan overwrites unmatched", func(t *testing.T) {
		db := openTestDB(t)
		libID := seedLibrary(t, db, "tv6", "tv", "/media/tv")
		s := &Scanner{DB: db}

		path := "/media/tv/fix.s01e01.mkv"
		ident := &EpisodeIdentity{ShowTitle: "Old Title", SeasonNumber: 1, EpisodeNumbers: []int{1}, Confidence: 0.55, Method: "sxe"}
		if err := s.upsertTVFile(ctx, libID, path, "mkv", 1024, 1700000000, "{}", ident, extraFields{}); err != nil {
			t.Fatalf("initial upsert: %v", err)
		}

		newIdent := &EpisodeIdentity{ShowTitle: "New Title", SeasonNumber: 2, EpisodeNumbers: []int{3}, Confidence: 0.72, Method: "sxe"}
		if err := s.upsertTVFile(ctx, libID, path, "mkv", 2048, 1700001000, "{}", newIdent, extraFields{}); err != nil {
			t.Fatalf("rescan upsert: %v", err)
		}

		var title string
		var season int
		db.QueryRow(`SELECT guessed_title, season_number FROM tv_series_identities
			WHERE file_id = (SELECT id FROM tv_series_files WHERE library_id=? AND abs_path=?)`,
			libID, path,
		).Scan(&title, &season)
		if title != "New Title" {
			t.Fatalf("guessed_title = %q, want New Title", title)
		}
		if season != 2 {
			t.Fatalf("season_number = %d, want 2", season)
		}
	})
}

func TestUpsertIdentityRefreshesUnchangedUnmatched(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	libID := seedLibrary(t, db, "tvref", "tv", "/media/tv")
	s := &Scanner{DB: db}

	// Seed a file whose identity was parsed with stale logic (empty title).
	path := "/media/tv/Monty Pythons Flying Circus/s1/1x01 Whither Canada.mkv"
	if err := s.upsertTVFile(ctx, libID, path, "mkv", 1024, 1700000000, "{}", &EpisodeIdentity{
		ShowTitle: "", SeasonNumber: 1, EpisodeNumbers: []int{1}, Confidence: 0.55, Method: "x_format",
	}, extraFields{}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}

	var fileID int64
	if err := db.QueryRow("SELECT id FROM tv_series_files WHERE library_id=? AND abs_path=?", libID, path).Scan(&fileID); err != nil {
		t.Fatalf("get file id: %v", err)
	}

	// A re-scan of the unchanged file re-runs IdentifyFile and refreshes the
	// derived identity — this is the cheap path taken for unchanged files.
	if err := s.upsertIdentity(ctx, s.DB, fileID, IdentifyFile(path)); err != nil {
		t.Fatalf("refresh upsertIdentity: %v", err)
	}

	var title string
	if err := db.QueryRow(`SELECT guessed_title FROM tv_series_identities WHERE file_id=?`, fileID).Scan(&title); err != nil {
		t.Fatalf("query identity: %v", err)
	}
	if title != "Monty Pythons Flying Circus" {
		t.Fatalf("guessed_title = %q, want Monty Pythons Flying Circus", title)
	}
}

func TestPruneMissingFiles(t *testing.T) {
	ctx := context.Background()

	t.Run("removes missing files", func(t *testing.T) {
		db := openTestDB(t)
		root := t.TempDir()
		libID := seedLibrary(t, db, "tvp1", "tv", root)
		s := &Scanner{DB: db}

		missingPath := filepath.Join(root, "gone.mkv")
		db.Exec(
			"INSERT INTO tv_series_files (library_id, abs_path, container, file_size_bytes, mtime_unix, stream_info_json) VALUES (?, ?, 'mkv', 100, 1700000000, '{}')",
			libID, missingPath,
		)

		if err := s.pruneMissingFiles(ctx, libID, root); err != nil {
			t.Fatalf("pruneMissingFiles: %v", err)
		}

		var count int
		db.QueryRow("SELECT COUNT(*) FROM tv_series_files WHERE library_id=?", libID).Scan(&count)
		if count != 0 {
			t.Fatalf("count = %d, want 0 (file should be pruned)", count)
		}
	})

	t.Run("preserves existing files", func(t *testing.T) {
		db := openTestDB(t)
		root := t.TempDir()
		libID := seedLibrary(t, db, "tvp2", "tv", root)
		s := &Scanner{DB: db}

		existingPath := filepath.Join(root, "exists.mkv")
		if err := os.WriteFile(existingPath, []byte("data"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		db.Exec(
			"INSERT INTO tv_series_files (library_id, abs_path, container, file_size_bytes, mtime_unix, stream_info_json) VALUES (?, ?, 'mkv', 100, 1700000000, '{}')",
			libID, existingPath,
		)

		if err := s.pruneMissingFiles(ctx, libID, root); err != nil {
			t.Fatalf("pruneMissingFiles: %v", err)
		}

		var count int
		db.QueryRow("SELECT COUNT(*) FROM tv_series_files WHERE library_id=?", libID).Scan(&count)
		if count != 1 {
			t.Fatalf("count = %d, want 1 (existing file should be preserved)", count)
		}
	})

	t.Run("ignores files outside root", func(t *testing.T) {
		db := openTestDB(t)
		root := t.TempDir()
		libID := seedLibrary(t, db, "tvp3", "tv", root)
		s := &Scanner{DB: db}

		outsidePath := "/some/other/path/outside.mkv"
		db.Exec(
			"INSERT INTO tv_series_files (library_id, abs_path, container, file_size_bytes, mtime_unix, stream_info_json) VALUES (?, ?, 'mkv', 100, 1700000000, '{}')",
			libID, outsidePath,
		)

		if err := s.pruneMissingFiles(ctx, libID, root); err != nil {
			t.Fatalf("pruneMissingFiles: %v", err)
		}

		var count int
		db.QueryRow("SELECT COUNT(*) FROM tv_series_files WHERE library_id=? AND abs_path=?", libID, outsidePath).Scan(&count)
		if count != 1 {
			t.Fatalf("count = %d, want 1 (outside-root file should NOT be pruned)", count)
		}
	})
}

// A full scan ingests extras-dir files as playable extras: flagged, titled from
// the filename, blank placeholder identity (never enters matching) — while a
// top-level dir of the same name stays a real title, and sample dirs stay
// skipped entirely.
func TestScanTVExtras(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	root := t.TempDir()

	mustWrite := func(rel string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	episode := mustWrite("Show/Season 1/show.s01e01.mkv")
	extra := mustWrite("Show/Extras/Making.of.the.Show.mkv")
	seasonExtra := mustWrite("Show/Season 1/Trailers/teaser.mkv")
	topLevel := mustWrite("Extras/extras.s01e01.mkv") // a show literally named "Extras"
	sample := mustWrite("Show/Sample/clip.mkv")

	libID := seedLibrary(t, db, "tvx", "tv", root)
	s := &Scanner{Cfg: config.Config{MediaRoot: root}, DB: db}
	if err := s.ScanTV(ctx, 0, libID); err != nil {
		t.Fatalf("ScanTV: %v", err)
	}

	type row struct {
		isExtra  int
		title    string
		category string
		identT   string
		status   string
	}
	get := func(path string) (row, bool) {
		var r row
		err := db.QueryRow(`
SELECT f.is_extra, f.extra_title, f.extra_category, i.guessed_title, i.status
FROM tv_series_files f JOIN tv_series_identities i ON i.file_id=f.id
WHERE f.abs_path=?`, path).Scan(&r.isExtra, &r.title, &r.category, &r.identT, &r.status)
		return r, err == nil
	}

	if r, ok := get(episode); !ok || r.isExtra != 0 || r.identT != "Show" {
		t.Fatalf("episode row = %+v ok=%v, want regular row with identity", r, ok)
	}
	if r, ok := get(extra); !ok || r.isExtra != 1 || r.title != "Making of the Show" || r.category != "Extra" || r.identT != "" || r.status != "unmatched" {
		t.Fatalf("extra row = %+v ok=%v, want is_extra=1 titled placeholder", r, ok)
	}
	if r, ok := get(seasonExtra); !ok || r.isExtra != 1 || r.category != "Trailer" {
		t.Fatalf("season extra row = %+v ok=%v, want is_extra=1 Trailer", r, ok)
	}
	if r, ok := get(topLevel); !ok || r.isExtra != 0 {
		t.Fatalf("top-level 'Extras' show row = %+v ok=%v, want regular row", r, ok)
	}
	if _, ok := get(sample); ok {
		t.Fatalf("sample-dir file was ingested, want skipped")
	}

	// The count walk must agree with the ingest walk (progress totals).
	if n := CountEligibleVideoFiles(root); n != 4 {
		t.Fatalf("CountEligibleVideoFiles = %d, want 4", n)
	}

	// A stale matched identity on an extra (pre-feature row, or hand-matched)
	// is reset to the blank placeholder by the post-walk pass on the next scan.
	if _, err := db.Exec(`
UPDATE tv_series_identities SET status='matched', provider='tmdb', series_id='42', guessed_title='Show'
WHERE file_id = (SELECT id FROM tv_series_files WHERE abs_path=?)`, extra); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanTV(ctx, 0, libID); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if r, ok := get(extra); !ok || r.status != "unmatched" || r.identT != "" {
		t.Fatalf("extra identity after rescan = %+v ok=%v, want blank unmatched placeholder", r, ok)
	}
}

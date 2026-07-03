package moviescan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"hespera/internal/config"
)

// A movie scan ingests extras-dir files as playable extras (flagged, titled
// from the filename, never title-parsed) while a top-level dir of the same
// name stays a real library entry, and a stale match on an extra is reset by
// the post-walk pass.
func TestScanMoviesExtras(t *testing.T) {
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
	feature := mustWrite("Blade Runner (1982)/Blade Runner (1982).mkv")
	extra := mustWrite("Blade Runner (1982)/Featurettes/On.The.Edge.mkv")
	topLevel := mustWrite("Trailers/Trailers (2019).mkv") // a film folder named "Trailers"

	libID := seedLibrary(t, db, "mvx", "movies", root)
	s := &Scanner{Cfg: config.Config{MediaRoot: root}, DB: db}
	if err := s.ScanMovies(ctx, 0, libID); err != nil {
		t.Fatalf("ScanMovies: %v", err)
	}

	type row struct {
		isExtra  int
		title    string
		category string
		guessed  string
	}
	get := func(path string) (row, bool) {
		var r row
		err := db.QueryRow(
			"SELECT is_extra, extra_title, extra_category, guessed_title FROM movie_files WHERE abs_path=?", path,
		).Scan(&r.isExtra, &r.title, &r.category, &r.guessed)
		return r, err == nil
	}

	if r, ok := get(feature); !ok || r.isExtra != 0 || r.guessed != "Blade Runner" {
		t.Fatalf("feature row = %+v ok=%v, want regular parsed row", r, ok)
	}
	if r, ok := get(extra); !ok || r.isExtra != 1 || r.title != "On The Edge" || r.category != "Featurette" || r.guessed != "" {
		t.Fatalf("extra row = %+v ok=%v, want is_extra=1 Featurette with empty guessed_title", r, ok)
	}
	if r, ok := get(topLevel); !ok || r.isExtra != 0 || r.guessed == "" {
		t.Fatalf("top-level 'Trailers' movie row = %+v ok=%v, want regular parsed row", r, ok)
	}

	// A stale match on an extra (pre-feature row) resets on the next scan.
	if _, err := db.Exec(
		"UPDATE movie_files SET match_status='matched', tmdb_id=42 WHERE abs_path=?", extra,
	); err != nil {
		t.Fatal(err)
	}
	if err := s.ScanMovies(ctx, 0, libID); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	var status string
	var tmdbID int
	if err := db.QueryRow("SELECT match_status, tmdb_id FROM movie_files WHERE abs_path=?", extra).Scan(&status, &tmdbID); err != nil {
		t.Fatal(err)
	}
	if status != "" || tmdbID != 0 {
		t.Fatalf("extra match after rescan = %q/%d, want blank/0", status, tmdbID)
	}
}

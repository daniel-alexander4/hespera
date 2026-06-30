package tmdb

import (
	"context"
	"testing"
)

// TestDownloadMovieArtSkipsManual confirms the manual-upload guard: a movie_art
// row marked manual=1 must survive a (re)match — downloadMovieArt early-returns
// before any download/upsert, so the user's art is never clobbered.
func TestDownloadMovieArtSkipsManual(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(
		"INSERT INTO movie_art (tmdb_movie_id, art_type, art_path, manual) VALUES (550,'poster','/data/thumbs/movies/manual.png',1)"); err != nil {
		t.Fatalf("seed manual art: %v", err)
	}
	// db is enough — the gate returns before touching the client/artDir.
	m := &Matcher{db: db}
	m.downloadMovieArt(context.Background(), 550, "poster", "/some/tmdb/path.jpg")

	var ap string
	var manual int
	if err := db.QueryRow(
		"SELECT art_path, manual FROM movie_art WHERE tmdb_movie_id=550 AND art_type='poster'").Scan(&ap, &manual); err != nil {
		t.Fatalf("row: %v", err)
	}
	if ap != "/data/thumbs/movies/manual.png" || manual != 1 {
		t.Fatalf("manual art clobbered: art_path=%q manual=%d", ap, manual)
	}
}

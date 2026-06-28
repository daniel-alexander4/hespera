package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCastAndPersonQueries covers the cast strip + actor-page data loaders: the
// series cast (ordered, with HasArt), the person's in-library titles (the
// credits⋈identities join with the right type cast), and personArt 404 on miss.
func TestCastAndPersonQueries(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()

	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('tv','tv','/tv')")
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()
	res, err = db.Exec("INSERT INTO tv_series_files (library_id, abs_path) VALUES (?, '/tv/x.mkv')", libID)
	if err != nil {
		t.Fatal(err)
	}
	fid, _ := res.LastInsertId()
	if _, err := db.Exec(
		`INSERT INTO tv_series_identities (file_id, status, provider, series_id, season_number, episode_numbers_csv)
		 VALUES (?, 'matched','tmdb','1396',1,'1')`, fid); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec("INSERT INTO people (tmdb_id, name, art_path, bio, bio_fetched_at) VALUES (17419,'Bryan Cranston','/d/p.jpg','Bio', datetime('now'))"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO people (tmdb_id, name) VALUES (84497,'Aaron Paul')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO credits (person_id, media_type, media_id, character_name, billing_order) VALUES (17419,'tv',1396,'Walter White',0),(84497,'tv',1396,'Jesse Pinkman',1)"); err != nil {
		t.Fatal(err)
	}

	cast := h.loadSeriesCast(ctx, 1396)
	if len(cast) != 2 {
		t.Fatalf("cast len = %d, want 2 (%+v)", len(cast), cast)
	}
	if cast[0].PersonID != 17419 || cast[0].Character != "Walter White" || !cast[0].HasArt {
		t.Fatalf("cast[0] = %+v", cast[0])
	}
	if cast[1].HasArt {
		t.Fatalf("cast[1] should have no art: %+v", cast[1])
	}

	titles := h.loadPersonTitles(ctx, 17419)
	if len(titles) != 1 || titles[0].SeriesID != "1396" || titles[0].Character != "Walter White" {
		t.Fatalf("titles = %+v", titles)
	}

	// castFetched is false until the marker is written.
	if h.castFetched(ctx, 1396) {
		t.Fatal("castFetched should be false before a fetch")
	}

	// personArt 404s for an unknown person.
	req := httptest.NewRequest(http.MethodGet, "/art/person/999999", nil)
	w := httptest.NewRecorder()
	h.personArt(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("personArt(missing) = %d, want 404", w.Code)
	}
}

// TestBuildOtherShows covers the out-of-library filmography: in-library shows are
// excluded, years are derived, and posters hotlink to TMDB.
func TestBuildOtherShows(t *testing.T) {
	blob := `[
		{"id":1396,"name":"Breaking Bad","character":"Walt","poster_path":"/bb.jpg","first_air_date":"2008-01-20"},
		{"id":1100,"name":"Malcolm","character":"Hal","poster_path":"","first_air_date":"2000-01-09"}
	]`
	out := buildOtherShows(blob, map[string]bool{"1396": true})
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1 (1396 in library, excluded)", len(out))
	}
	if out[0].Name != "Malcolm" || out[0].Year != "2000" || out[0].Character != "Hal" || out[0].PosterURL != "" {
		t.Fatalf("out[0] = %+v", out[0])
	}

	out2 := buildOtherShows(`[{"id":5,"name":"X","poster_path":"/x.jpg","first_air_date":"2020-05-01"}]`, nil)
	if len(out2) != 1 || out2[0].PosterURL != "https://image.tmdb.org/t/p/w342/x.jpg" {
		t.Fatalf("out2 = %+v", out2)
	}

	if buildOtherShows("", nil) != nil {
		t.Fatal("empty filmography should yield nil")
	}
}

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

	ownedTV := h.loadPersonOwnedIDs(ctx, 17419, personOwnedTVQuery)
	if len(ownedTV) != 1 || !ownedTV[1396] {
		t.Fatalf("ownedTV = %+v, want {1396:true}", ownedTV)
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

// TestBuildFilmography covers the combined-credits split: TV vs film by
// media_type, Owned flags from the per-type ownership sets (owned → local link,
// no hotlink; un-owned → hotlinked poster), and back-compat with an old
// tv_credits blob (no media_type → all TV).
func TestBuildFilmography(t *testing.T) {
	blob := `[
		{"id":1396,"media_type":"tv","name":"Breaking Bad","character":"Walt","poster_path":"/bb.jpg","first_air_date":"2008-01-20"},
		{"id":603,"media_type":"movie","title":"The Matrix","character":"Neo","poster_path":"/mx.jpg","release_date":"1999-03-31"},
		{"id":12,"media_type":"movie","title":"Drive","character":"Driver","poster_path":"/dr.jpg","release_date":"2011-09-16"}
	]`
	tv, films := buildFilmography(blob, map[int]bool{1396: true}, map[int]bool{603: true})

	if len(tv) != 1 || tv[0].Title != "Breaking Bad" || tv[0].Year != "2008" || !tv[0].Owned || tv[0].PosterURL != "" {
		t.Fatalf("tv = %+v (want one owned Breaking Bad, no hotlink)", tv)
	}
	if len(films) != 2 {
		t.Fatalf("films len = %d, want 2", len(films))
	}
	var matrix, drive filmographyRow
	for _, f := range films {
		switch f.ID {
		case 603:
			matrix = f
		case 12:
			drive = f
		}
	}
	if !matrix.Owned || matrix.PosterURL != "" {
		t.Fatalf("Matrix should be owned with no hotlink: %+v", matrix)
	}
	if drive.Owned || drive.Year != "2011" || drive.PosterURL != "https://image.tmdb.org/t/p/w342/dr.jpg" {
		t.Fatalf("Drive should be un-owned + hotlinked: %+v", drive)
	}

	// Back-compat: an old tv_credits blob (no media_type) → all TV, hotlinked.
	tv2, films2 := buildFilmography(`[{"id":5,"name":"X","poster_path":"/x.jpg","first_air_date":"2020-05-01"}]`, nil, nil)
	if len(films2) != 0 || len(tv2) != 1 || tv2[0].PosterURL != "https://image.tmdb.org/t/p/w342/x.jpg" {
		t.Fatalf("old blob → one un-owned TV row: tv=%+v films=%+v", tv2, films2)
	}

	if tv3, films3 := buildFilmography("", nil, nil); tv3 != nil || films3 != nil {
		t.Fatal("empty filmography should yield nil, nil")
	}
}

// TestFilmographyNeedsUpgrade gates the one-time lazy re-fetch: an old TV-only
// blob and a never-written ("") blob upgrade; a combined blob and a
// fetched-but-empty "[]"/"null" blob do not (the latter is what keeps it
// loop-proof — a re-fetch that finds no credits writes "[]", not "").
func TestFilmographyNeedsUpgrade(t *testing.T) {
	if !filmographyNeedsUpgrade(`[{"id":5,"name":"X","first_air_date":"2020-01-01"}]`) {
		t.Fatal("old tv_credits blob should need upgrade")
	}
	if !filmographyNeedsUpgrade("") {
		t.Fatal("empty-string blob (never written) should need upgrade")
	}
	if !filmographyNeedsUpgrade("   ") {
		t.Fatal("whitespace-only blob should need upgrade")
	}
	if filmographyNeedsUpgrade(`[{"id":5,"media_type":"tv","name":"X"}]`) {
		t.Fatal("combined blob should NOT need upgrade")
	}
	if filmographyNeedsUpgrade("[]") || filmographyNeedsUpgrade("null") {
		t.Fatal("fetched-but-empty ([]/null) must NOT re-trigger (loop guard)")
	}
}

func TestSplitWikipediaBio(t *testing.T) {
	tests := []struct {
		name      string
		bio       string
		wantClean string
		wantURL   string
	}{
		{
			name:      "space and nbsp before title",
			bio:       "Host of Survivor.\n\nDescription above from the Wikipedia article \u00a0Jeff Probst, licensed under CC-BY-SA, full list of contributors on Wikipedia.",
			wantClean: "Host of Survivor.",
			wantURL:   "https://en.wikipedia.org/wiki/Jeff_Probst",
		},
		{
			name:      "nbsp only before title",
			bio:       "An actress.\n\nDescription above from the Wikipedia article\u00a0Samantha Morton, licensed under CC-BY-SA, full list of contributors on Wikipedia.",
			wantClean: "An actress.",
			wantURL:   "https://en.wikipedia.org/wiki/Samantha_Morton",
		},
		{
			name:      "no attribution is passed through unchanged",
			bio:       "Just a plain biography with no notice.",
			wantClean: "Just a plain biography with no notice.",
			wantURL:   "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clean, gotURL := splitWikipediaBio(tc.bio)
			if clean != tc.wantClean {
				t.Fatalf("clean = %q, want %q", clean, tc.wantClean)
			}
			if gotURL != tc.wantURL {
				t.Fatalf("url = %q, want %q", gotURL, tc.wantURL)
			}
		})
	}
}

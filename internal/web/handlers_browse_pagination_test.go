package web

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

// seedTVSeries inserts a matched series with `episodes` episode files and a
// cached name, so the SQL-paginated TV list can sort/filter/page it.
func seedTVSeries(t *testing.T, db *sql.DB, libID int64, seriesID, name string, episodes int) {
	t.Helper()
	for e := 0; e < episodes; e++ {
		res, err := db.Exec("INSERT INTO tv_series_files (library_id, abs_path, container) VALUES (?, ?, 'mkv')",
			libID, fmt.Sprintf("/tv/%s/e%d.mkv", seriesID, e))
		if err != nil {
			t.Fatalf("insert file: %v", err)
		}
		fid, _ := res.LastInsertId()
		if _, err := db.Exec(
			`INSERT INTO tv_series_identities (file_id, status, provider, series_id, season_number, episode_numbers_csv)
			 VALUES (?, 'matched', 'tmdb', ?, 1, ?)`, fid, seriesID, fmt.Sprint(e+1)); err != nil {
			t.Fatalf("insert identity: %v", err)
		}
	}
	if _, err := db.Exec("INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json) VALUES (?, 'en', ?)",
		"show:"+seriesID, fmt.Sprintf(`{"name":%q,"first_air_date":"2020-01-02","poster_path":"/p.jpg"}`, name)); err != nil {
		t.Fatalf("insert cache: %v", err)
	}
}

func TestLoadTVSeriesListSQLPagination(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()
	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV','tv','/tv')")
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()

	seedTVSeries(t, db, libID, "100", "Zebra Show", 2)
	seedTVSeries(t, db, libID, "200", "apple show", 1)
	seedTVSeries(t, db, libID, "300", "Mango", 3)

	rows, _, unmatched, err := h.loadTVSeriesList(ctx, 1, "")
	if err != nil {
		t.Fatalf("loadTVSeriesList: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	// Case-insensitive name sort in SQL: apple, Mango, Zebra.
	if rows[0].Name != "apple show" || rows[1].Name != "Mango" || rows[2].Name != "Zebra Show" {
		t.Fatalf("wrong order: %q, %q, %q", rows[0].Name, rows[1].Name, rows[2].Name)
	}
	if rows[2].EpisodeCount != 2 {
		t.Fatalf("Zebra ep count: want 2, got %d", rows[2].EpisodeCount)
	}
	if rows[0].Year != "2020" {
		t.Fatalf("year: want 2020, got %q", rows[0].Year)
	}
	if unmatched != 0 {
		t.Fatalf("unmatched: want 0, got %d", unmatched)
	}

	// ?q= filters on name, in SQL.
	sr, _, _, err := h.loadTVSeriesList(ctx, 1, "man")
	if err != nil {
		t.Fatal(err)
	}
	if len(sr) != 1 || sr[0].Name != "Mango" {
		t.Fatalf("search 'man' → %+v", sr)
	}
}

func TestLoadTVSeriesListPaginatesInSQL(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()
	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV','tv','/tv')")
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()

	const total = 61 // one more than listPageSize (60)
	for i := 0; i < total; i++ {
		seedTVSeries(t, db, libID, fmt.Sprintf("%d", 1000+i), fmt.Sprintf("Show %03d", i), 1)
	}

	p1, nav1, _, err := h.loadTVSeriesList(ctx, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p1) != listPageSize {
		t.Fatalf("page 1: want %d rows, got %d", listPageSize, len(p1))
	}
	if !nav1.HasNext || nav1.HasPrev {
		t.Fatalf("page 1 nav: HasNext=%v HasPrev=%v", nav1.HasNext, nav1.HasPrev)
	}
	if p1[0].Name != "Show 000" {
		t.Fatalf("page 1 first: %q", p1[0].Name)
	}

	p2, nav2, _, err := h.loadTVSeriesList(ctx, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p2) != total-listPageSize {
		t.Fatalf("page 2: want %d rows, got %d", total-listPageSize, len(p2))
	}
	if nav2.HasNext || !nav2.HasPrev {
		t.Fatalf("page 2 nav: HasNext=%v HasPrev=%v", nav2.HasNext, nav2.HasPrev)
	}
	if p2[0].Name != "Show 060" {
		t.Fatalf("page 2 first: %q", p2[0].Name)
	}
}

func TestLoadMovieListSQLPagination(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()
	res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('M','movies','/m')")
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()

	add := func(tmdbID int, title string) {
		if _, err := db.Exec(`INSERT INTO movie_files (library_id, abs_path, tmdb_id, match_status) VALUES (?, ?, ?, 'matched')`,
			libID, fmt.Sprintf("/m/%d.mkv", tmdbID), tmdbID); err != nil {
			t.Fatalf("insert movie: %v", err)
		}
		if _, err := db.Exec("INSERT INTO movie_metadata_cache (entity_key, payload_json) VALUES (?, ?)",
			fmt.Sprintf("movie:%d", tmdbID), fmt.Sprintf(`{"title":%q,"release_date":"1999-03-31"}`, title)); err != nil {
			t.Fatalf("insert cache: %v", err)
		}
	}
	add(10, "Zodiac")
	add(20, "Amelie")
	add(30, "matrix")

	rows, _, unmatched, err := h.loadMovieList(ctx, 1)
	if err != nil {
		t.Fatalf("loadMovieList: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3, got %d", len(rows))
	}
	if rows[0].Title != "Amelie" || rows[1].Title != "matrix" || rows[2].Title != "Zodiac" {
		t.Fatalf("wrong order: %q, %q, %q", rows[0].Title, rows[1].Title, rows[2].Title)
	}
	if rows[0].Year != "1999" {
		t.Fatalf("year: want 1999, got %q", rows[0].Year)
	}
	if unmatched != 0 {
		t.Fatalf("unmatched: want 0, got %d", unmatched)
	}
}

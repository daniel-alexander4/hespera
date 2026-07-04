package web

import (
	"context"
	"testing"
)

// TestBuildRelatedRows: owned titles link locally (no hotlink), un-owned ones
// get a TMDB poster URL; ownership is checked per media type against the
// library's matched rows (TV series_id is TEXT — the CAST path).
func TestBuildRelatedRows(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()

	if _, err := db.Exec("INSERT INTO libraries(name,type,root_path) VALUES('TV','tv','/m')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO tv_series_files(library_id,abs_path) VALUES(1,'/m/a.mkv')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO tv_series_identities(file_id,series_id,status) VALUES(1,'77','matched')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO movie_files(library_id,abs_path,tmdb_id,match_status) VALUES(1,'/m/f.mkv',88,'matched')"); err != nil {
		t.Fatal(err)
	}

	blob := `[
		{"id":77,"name":"Owned Show","poster_path":"/o.jpg","first_air_date":"2010-05-05"},
		{"id":88,"name":"Movie-ID Collision","poster_path":"/c.jpg"},
		{"id":99,"name":"External Show","poster_path":"/x.jpg","first_air_date":"2020-01-01"}
	]`
	rows := h.buildRelatedRows(ctx, blob, "tv")
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if !rows[0].Owned || rows[0].PosterURL != "" {
		t.Errorf("owned show: Owned=%v PosterURL=%q", rows[0].Owned, rows[0].PosterURL)
	}
	// 88 is a matched MOVIE id — must NOT read as owned for a TV strip.
	if rows[1].Owned {
		t.Error("movie id 88 marked owned in a TV strip (media-type collision)")
	}
	if rows[2].Owned || rows[2].PosterURL != tmdbPosterBase+"/x.jpg" {
		t.Errorf("external show: Owned=%v PosterURL=%q", rows[2].Owned, rows[2].PosterURL)
	}
	if rows[0].Title != "Owned Show" || rows[0].Year != "2010" {
		t.Errorf("row shape: %+v", rows[0])
	}

	// The movie strip sees 88 as owned.
	mrows := h.buildRelatedRows(ctx, `[{"id":88,"title":"Owned Film","poster_path":"/f.jpg","release_date":"2019-03-03"}]`, "movie")
	if len(mrows) != 1 || !mrows[0].Owned || mrows[0].Year != "2019" {
		t.Errorf("movie rows: %+v", mrows)
	}

	// Garbage / empty blobs render nothing.
	if got := h.buildRelatedRows(ctx, "not json", "tv"); got != nil {
		t.Errorf("garbage blob rows = %+v", got)
	}
	if got := h.buildRelatedRows(ctx, "[]", "tv"); got != nil {
		t.Errorf("empty blob rows = %+v", got)
	}
}

// TestMovieCollectionRows: the franchise strip excludes the film itself and
// runs the same owned split as More Like This.
func TestMovieCollectionRows(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()

	if _, err := db.Exec("INSERT INTO libraries(name,type,root_path) VALUES('Movies','movies','/m')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO movie_files(library_id,abs_path,tmdb_id,match_status) VALUES(1,'/m/f2.mkv',11,'matched')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO movie_metadata_cache(entity_key,lang,payload_json,fetched_at)
		VALUES('collection:500','en','[
			{"id":10,"title":"Self","poster_path":"/a.jpg","release_date":"1999-01-01"},
			{"id":11,"title":"Owned Sequel","poster_path":"/b.jpg","release_date":"2003-01-01"},
			{"id":12,"title":"Missing Threequel","poster_path":"/c.jpg","release_date":"2007-01-01"}
		]',datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	rows := h.movieCollectionRows(ctx, 500, 10)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (self excluded)", len(rows))
	}
	if rows[0].ID != 11 || !rows[0].Owned {
		t.Errorf("owned sequel: %+v", rows[0])
	}
	if rows[1].ID != 12 || rows[1].Owned || rows[1].PosterURL != tmdbPosterBase+"/c.jpg" {
		t.Errorf("un-owned threequel: %+v", rows[1])
	}

	// Absent blob (job not run yet) renders nothing.
	if got := h.movieCollectionRows(ctx, 999, 10); got != nil {
		t.Errorf("absent blob rows = %+v", got)
	}
}

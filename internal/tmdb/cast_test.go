package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// TestFetchTVCast verifies the aggregate-credits fetch populates people +
// credits, downloads profile images only for members that have one, writes the
// fetch marker, and is idempotent (re-run replaces, not duplicates).
func TestFetchTVCast(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("/3/tv/1396/aggregate_credits", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"cast":[
			{"id":17419,"name":"Bryan Cranston","profile_path":"/bc.jpg","order":0,"roles":[{"character":"Walter White"}]},
			{"id":84497,"name":"Aaron Paul","profile_path":"/ap.jpg","order":1,"roles":[{"character":"Jesse Pinkman"}]},
			{"id":134531,"name":"Anna Gunn","profile_path":"","order":2,"roles":[{"character":"Skyler White"}]}
		]}`)
	})
	mux.HandleFunc("/t/p/w500/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(smallJPEG)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ch := make(chan time.Time)
	close(ch)
	m := &Matcher{
		db: db,
		client: &Client{
			apiKey: "k", httpClient: srv.Client(),
			apiBase: srv.URL + "/3", imgBase: srv.URL + "/t/p/w500", limiter: ch,
		},
		artDir: filepath.Join(t.TempDir(), "thumbs", "tv"),
	}

	if err := m.FetchTVCast(ctx, 1396); err != nil {
		t.Fatalf("FetchTVCast: %v", err)
	}

	var people int
	_ = db.QueryRow("SELECT COUNT(*) FROM people").Scan(&people)
	if people != 3 {
		t.Fatalf("people = %d, want 3", people)
	}

	rows, err := db.Query("SELECT person_id, character_name, billing_order FROM credits WHERE media_type='tv' AND media_id=1396 ORDER BY billing_order")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type cr struct {
		pid  int64
		char string
		ord  int
	}
	var got []cr
	for rows.Next() {
		var c cr
		if err := rows.Scan(&c.pid, &c.char, &c.ord); err != nil {
			t.Fatal(err)
		}
		got = append(got, c)
	}
	if len(got) != 3 || got[0].pid != 17419 || got[0].char != "Walter White" || got[2].pid != 134531 {
		t.Fatalf("credits = %+v", got)
	}

	// Images downloaded for members with a profile_path, none for the one without.
	var art1, art3 string
	_ = db.QueryRow("SELECT art_path FROM people WHERE tmdb_id=17419").Scan(&art1)
	_ = db.QueryRow("SELECT art_path FROM people WHERE tmdb_id=134531").Scan(&art3)
	if art1 == "" {
		t.Error("expected art_path for 17419")
	}
	if art3 != "" {
		t.Errorf("expected no art_path for 134531, got %q", art3)
	}

	// Fetch marker present.
	var mk int
	if err := db.QueryRow("SELECT 1 FROM tv_series_metadata_cache WHERE entity_key='show:1396:cast' AND lang='en'").Scan(&mk); err != nil {
		t.Fatalf("cast marker missing: %v", err)
	}

	// Idempotent: re-run replaces this show's credits, no duplicates.
	if err := m.FetchTVCast(ctx, 1396); err != nil {
		t.Fatalf("FetchTVCast rerun: %v", err)
	}
	var credits int
	_ = db.QueryRow("SELECT COUNT(*) FROM credits WHERE media_id=1396").Scan(&credits)
	if credits != 3 {
		t.Fatalf("after rerun credits = %d, want 3", credits)
	}
}

// TestRefetchBackdrop verifies the lazy backfill fetches the backdrop from the
// wide (w1280) base using the path from cached metadata (no extra TMDB call),
// writes the art row, and sets the hi-res marker.
func TestRefetchBackdrop(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var hitW1280 bool
	mux := http.NewServeMux()
	mux.HandleFunc("/t/p/w1280/", func(w http.ResponseWriter, r *http.Request) {
		hitW1280 = true
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(smallJPEG)
	})
	// No /3/... routes: if RefetchBackdrop made a metadata call it would 404.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := db.Exec(`INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json, fetched_at)
		VALUES ('show:1396','en','{"id":1396,"backdrop_path":"/bd.jpg"}', datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	ch := make(chan time.Time)
	close(ch)
	m := &Matcher{
		db: db,
		client: &Client{
			apiKey: "k", httpClient: srv.Client(),
			apiBase: srv.URL + "/3", imgBase: srv.URL + "/t/p/w500",
			backdropBase: srv.URL + "/t/p/w1280", limiter: ch,
		},
		artDir: filepath.Join(t.TempDir(), "thumbs", "tv"),
	}

	if err := m.RefetchBackdrop(ctx, 1396); err != nil {
		t.Fatalf("RefetchBackdrop: %v", err)
	}
	if !hitW1280 {
		t.Fatal("backdrop should be fetched from the w1280 base")
	}
	var art string
	if err := db.QueryRow("SELECT art_path FROM tv_series_art WHERE tmdb_series_id=1396 AND art_type='series_backdrop'").Scan(&art); err != nil || art == "" {
		t.Fatalf("backdrop art row: art=%q err=%v", art, err)
	}
	var mk int
	if err := db.QueryRow("SELECT 1 FROM tv_series_metadata_cache WHERE entity_key='show:1396:backdrop_hires'").Scan(&mk); err != nil {
		t.Fatalf("missing backdrop_hires marker: %v", err)
	}
}

// TestFetchPersonBio verifies a person's bio + image are cached.
func TestFetchPersonBio(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("/3/person/17419/tv_credits", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"cast":[
			{"id":1396,"name":"Breaking Bad","character":"Walter White","poster_path":"/bb.jpg","first_air_date":"2008-01-20","episode_count":62},
			{"id":1100,"name":"Malcolm in the Middle","character":"Hal","poster_path":"/mm.jpg","first_air_date":"2000-01-09","episode_count":151}
		]}`)
	})
	mux.HandleFunc("/3/person/17419", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":17419,"name":"Bryan Cranston","biography":"An American actor.","profile_path":"/bc.jpg"}`)
	})
	mux.HandleFunc("/t/p/w500/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(smallJPEG)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ch := make(chan time.Time)
	close(ch)
	m := &Matcher{
		db: db,
		client: &Client{
			apiKey: "k", httpClient: srv.Client(),
			apiBase: srv.URL + "/3", imgBase: srv.URL + "/t/p/w500", limiter: ch,
		},
		artDir: filepath.Join(t.TempDir(), "thumbs", "tv"),
	}

	if err := m.FetchPersonBio(ctx, 17419); err != nil {
		t.Fatalf("FetchPersonBio: %v", err)
	}
	var bio, art, fetched, filmo string
	if err := db.QueryRow("SELECT bio, art_path, bio_fetched_at, filmography_json FROM people WHERE tmdb_id=17419").Scan(&bio, &art, &fetched, &filmo); err != nil {
		t.Fatalf("scan person: %v", err)
	}
	if bio != "An American actor." || art == "" || fetched == "" {
		t.Fatalf("person row: bio=%q art=%q fetched=%q", bio, art, fetched)
	}
	// Filmography cached, ordered by episode count (Malcolm 151 before Breaking Bad 62).
	var credits []PersonTVCredit
	if err := json.Unmarshal([]byte(filmo), &credits); err != nil {
		t.Fatalf("filmography unmarshal: %v (%q)", err, filmo)
	}
	if len(credits) != 2 || credits[0].ID != 1100 || credits[1].ID != 1396 {
		t.Fatalf("filmography = %+v", credits)
	}
}

package match

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"hespera/internal/ratelimit"
)

// The re-check TTL gates: a candidate stamped inside its TTL is skipped on a
// non-force run (zero network), still processed on force, and processed when
// the stamp is old or empty. One test per phase, all counting stub-server hits.

const (
	freshStamp = "2999-01-01T00:00:00Z" // effectively "just checked" for any TTL
	oldStamp   = "2000-01-01T00:00:00Z" // long past every TTL
)

func TestFetchPopularityRecheckTTL(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	m := &Matcher{db: db, lb: &LBClient{client: srv.Client(), baseURL: srv.URL, limiter: ratelimit.New(0)}}

	libRes, _ := db.Exec(`INSERT INTO libraries (name,type,root_path) VALUES ('M','music','/m')`)
	libID, _ := libRes.LastInsertId()
	if _, err := db.Exec(`INSERT INTO music_artists (library_id,name,musicbrainz_id,popularity_checked_at) VALUES (?,'Fresh','mbid-f',?)`, libID, freshStamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO music_artists (library_id,name,musicbrainz_id,popularity_checked_at) VALUES (?,'Stale','mbid-s',?)`, libID, oldStamp); err != nil {
		t.Fatal(err)
	}

	// Non-force: only the stale-stamped artist is fetched.
	if err := m.fetchPopularity(ctx, 0, libID, false); err != nil {
		t.Fatalf("fetchPopularity: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("non-force: want 1 LB fetch (stale artist only), got %d", hits.Load())
	}
	// The stale artist was re-stamped by that run; a second non-force run
	// fetches nothing.
	hits.Store(0)
	if err := m.fetchPopularity(ctx, 0, libID, false); err != nil {
		t.Fatalf("fetchPopularity rerun: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("non-force rerun: want 0 fetches, got %d", hits.Load())
	}
	// Force bypasses the TTL entirely: both artists fetched.
	if err := m.fetchPopularity(ctx, 0, libID, true); err != nil {
		t.Fatalf("fetchPopularity force: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("force: want 2 fetches, got %d", hits.Load())
	}
}

func TestMatchAlbumsRecheckTTL(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"release-groups":[]}`)) // no candidates → album stays unmatched
	}))
	defer srv.Close()
	m := &Matcher{db: db, mb: &MBClient{client: srv.Client(), baseURL: srv.URL, limiter: ratelimit.New(0)}}

	libRes, _ := db.Exec(`INSERT INTO libraries (name,type,root_path) VALUES ('M','music','/m')`)
	libID, _ := libRes.LastInsertId()
	arRes, _ := db.Exec(`INSERT INTO music_artists (library_id,name) VALUES (?,'Somebody')`, libID)
	arID, _ := arRes.LastInsertId()
	if _, err := db.Exec(`INSERT INTO music_albums (library_id,artist_id,album_artist_id,title,year,match_status,match_checked_at) VALUES (?,?,?,'Fresh Album',2000,'unmatched',?)`, libID, arID, arID, freshStamp); err != nil {
		t.Fatal(err)
	}
	staleRes, _ := db.Exec(`INSERT INTO music_albums (library_id,artist_id,album_artist_id,title,year,match_status,match_checked_at) VALUES (?,?,?,'Stale Album',2001,'unmatched',?)`, libID, arID, arID, oldStamp)
	staleID, _ := staleRes.LastInsertId()

	// Non-force: only the stale-stamped album is attempted.
	if err := m.matchAlbums(ctx, 0, libID, false); err != nil {
		t.Fatalf("matchAlbums: %v", err)
	}
	if hits.Load() == 0 {
		t.Fatalf("non-force: stale album should have been attempted")
	}
	firstRunHits := hits.Load()

	// It failed (empty MB results) and was re-stamped → second non-force run is free.
	var stamp string
	if err := db.QueryRow(`SELECT match_checked_at FROM music_albums WHERE id=?`, staleID).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	if stamp == oldStamp || stamp == "" {
		t.Fatalf("stale album not re-stamped after attempt: %q", stamp)
	}
	hits.Store(0)
	if err := m.matchAlbums(ctx, 0, libID, false); err != nil {
		t.Fatalf("matchAlbums rerun: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("non-force rerun: want 0 MB calls, got %d", hits.Load())
	}

	// Force retries both albums.
	if err := m.matchAlbums(ctx, 0, libID, true); err != nil {
		t.Fatalf("matchAlbums force: %v", err)
	}
	if hits.Load() <= firstRunHits {
		t.Fatalf("force: want both albums attempted (> %d calls), got %d", firstRunHits, hits.Load())
	}
}

func TestEnrichArtistsRecheckTTL(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"artists":[]}`)) // MBID search finds nothing
	}))
	defer srv.Close()
	m := &Matcher{db: db, mb: &MBClient{client: srv.Client(), baseURL: srv.URL, limiter: ratelimit.New(0)}}

	libRes, _ := db.Exec(`INSERT INTO libraries (name,type,root_path) VALUES ('M','music','/m')`)
	libID, _ := libRes.LastInsertId()
	seed := func(name, stamp string) int64 {
		res, err := db.Exec(`INSERT INTO music_artists (library_id,name,enrich_checked_at) VALUES (?,?,?)`, libID, name, stamp)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		// enrichArtists only sees artists that own an album.
		if _, err := db.Exec(`INSERT INTO music_albums (library_id,artist_id,album_artist_id,title,year) VALUES (?,?,?,?,0)`, libID, id, id, name+" LP"); err != nil {
			t.Fatal(err)
		}
		return id
	}
	seed("Fresh", freshStamp)
	staleID := seed("Stale", "") // never attempted → always a candidate

	// Non-force: only the never-stamped artist is attempted (its MB search
	// finds nothing, so it stays incomplete) — and it gets stamped.
	if err := m.enrichArtists(ctx, 0, libID, false); err != nil {
		t.Fatalf("enrichArtists: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("non-force: want 1 MB search (unstamped artist only), got %d", hits.Load())
	}
	var stamp string
	if err := db.QueryRow(`SELECT enrich_checked_at FROM music_artists WHERE id=?`, staleID).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	if stamp == "" {
		t.Fatalf("attempted artist not stamped")
	}

	// Second non-force run: both inside TTL now → zero network.
	hits.Store(0)
	if err := m.enrichArtists(ctx, 0, libID, false); err != nil {
		t.Fatalf("enrichArtists rerun: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("non-force rerun: want 0 MB calls, got %d", hits.Load())
	}

	// Force: both incomplete artists attempted.
	if err := m.enrichArtists(ctx, 0, libID, true); err != nil {
		t.Fatalf("enrichArtists force: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("force: want 2 MB searches, got %d", hits.Load())
	}
}

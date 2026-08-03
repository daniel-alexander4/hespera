package match

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"hespera/internal/ratelimit"
)

// enrichArtists is the only writer of music_artists.musicbrainz_id, and every
// phase downstream of it selects on that column — so an artist the candidate
// query misses gets no MBID, no popularity, no similar-artists list and no
// Instant Mix, permanently. These pin who the query reaches.

// TestEnrichArtistsReachesTrackOnlyArtists: an artist credited only on tracks,
// owning no album row, is still a candidate. That is the shape of a
// compilation guest artist and of a split-artist spelling whose albums are
// filed under a differently-tagged variant; an album-artist join here used to
// drop both.
func TestEnrichArtistsReachesTrackOnlyArtists(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var searched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		searched = append(searched, r.URL.Query().Get("query"))
		_, _ = w.Write([]byte(`{"artists":[]}`)) // no MB match: the loop stamps and moves on
	}))
	defer srv.Close()
	m := &Matcher{db: db, mb: &MBClient{client: srv.Client(), baseURL: srv.URL, limiter: ratelimit.New(0)}}

	libRes, err := db.Exec(`INSERT INTO libraries (name,type,root_path) VALUES ('M','music','/m')`)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := libRes.LastInsertId()

	artist := func(name string) int64 {
		res, err := db.Exec(`INSERT INTO music_artists (library_id,name) VALUES (?,?)`, libID, name)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}

	// Owns an album (the only shape the old query reached).
	albumOwner := artist("Album Owner")
	albRes, err := db.Exec(`INSERT INTO music_albums (library_id,artist_id,album_artist_id,title,year) VALUES (?,?,?,'Comp',2005)`,
		libID, albumOwner, albumOwner)
	if err != nil {
		t.Fatal(err)
	}
	albumID, _ := albRes.LastInsertId()

	// Credited only on a track of that album — owns no album row of its own.
	guest := artist("Track Only Guest")
	if _, err := db.Exec(`INSERT INTO music_tracks (library_id,artist_id,album_id,title,abs_path) VALUES (?,?,?,'Song','/m/comp/1.mp3')`,
		libID, guest, albumID); err != nil {
		t.Fatal(err)
	}

	// Neither tracks nor albums: still a candidate (nothing to exclude it), and
	// cheap — one search that finds nothing, then stamped for the TTL.
	artist("Bare Row")

	if err := m.enrichArtists(ctx, 0, libID, false); err != nil {
		t.Fatalf("enrichArtists: %v", err)
	}

	// SearchArtist sends `artist:"<name>"`, so match on containment.
	for _, name := range []string{"Album Owner", "Track Only Guest", "Bare Row"} {
		if !searchedFor(searched, name) {
			t.Errorf("artist %q was never searched; enrichArtists did not reach it (searched: %v)", name, searched)
		}
	}
	if len(searched) != 3 {
		t.Errorf("want exactly 3 artist searches, got %d: %v", len(searched), searched)
	}

	var stamp string
	if err := db.QueryRow(`SELECT enrich_checked_at FROM music_artists WHERE id=?`, guest).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	if stamp == "" {
		t.Error("track-only artist attempted but not stamped: it would re-fan-out on every automatic run")
	}
}

// TestEnrichArtistsSkipsPlaceholderArtists: the two placeholder names are not
// real artists to look up, and dropping the album join must not have widened
// the query onto them.
func TestEnrichArtistsSkipsPlaceholderArtists(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"artists":[]}`))
	}))
	defer srv.Close()
	m := &Matcher{db: db, mb: &MBClient{client: srv.Client(), baseURL: srv.URL, limiter: ratelimit.New(0)}}

	libRes, err := db.Exec(`INSERT INTO libraries (name,type,root_path) VALUES ('M','music','/m')`)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := libRes.LastInsertId()
	for _, name := range []string{"Unknown Artist", "Various Artists"} {
		if _, err := db.Exec(`INSERT INTO music_artists (library_id,name) VALUES (?,?)`, libID, name); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.enrichArtists(ctx, 0, libID, true); err != nil {
		t.Fatalf("enrichArtists: %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("placeholder artists were looked up: want 0 MB searches, got %d", got)
	}
}

// TestEnrichArtistsScopesToLibrary: without the album join, library_id is the
// only thing keeping one library's artists out of another's run.
func TestEnrichArtistsScopesToLibrary(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var searched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		searched = append(searched, r.URL.Query().Get("query"))
		_, _ = w.Write([]byte(`{"artists":[]}`))
	}))
	defer srv.Close()
	m := &Matcher{db: db, mb: &MBClient{client: srv.Client(), baseURL: srv.URL, limiter: ratelimit.New(0)}}

	mk := func(lib string) int64 {
		res, err := db.Exec(`INSERT INTO libraries (name,type,root_path) VALUES (?,'music',?)`, lib, "/"+lib)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	libA, libB := mk("A"), mk("B")
	for _, s := range []struct {
		lib  int64
		name string
	}{{libA, "In Scope"}, {libB, "Other Library"}} {
		if _, err := db.Exec(`INSERT INTO music_artists (library_id,name) VALUES (?,?)`, s.lib, s.name); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.enrichArtists(ctx, 0, libA, true); err != nil {
		t.Fatalf("enrichArtists: %v", err)
	}
	if len(searched) != 1 || !searchedFor(searched, "In Scope") {
		t.Fatalf("library scope leaked: searched %v, want exactly one search for In Scope", searched)
	}
}

// searchedFor reports whether any MusicBrainz artist search carried this name.
// SearchArtist wraps it as `artist:"<name>"`, so this matches on containment
// rather than pinning that query syntax from a test that isn't about it.
func searchedFor(searched []string, name string) bool {
	for _, q := range searched {
		if strings.Contains(q, name) {
			return true
		}
	}
	return false
}

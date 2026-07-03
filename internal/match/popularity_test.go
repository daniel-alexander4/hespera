package match

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"hespera/internal/ratelimit"
)

func TestFetchPopularity(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the artist with a real MBID is fetched; return two top recordings.
		// "Smoke on the Water" matches a local track; "Hush" matches another;
		// "Some B-Side" matches nothing local.
		_, _ = w.Write([]byte(`[
			{"recording_name":"Smoke on the Water","total_listen_count":900000},
			{"recording_name":"Hush","total_listen_count":120000},
			{"recording_name":"Some B-Side","total_listen_count":50}
		]`))
	}))
	defer srv.Close()

	m := &Matcher{db: db, lb: &LBClient{client: srv.Client(), baseURL: srv.URL, limiter: ratelimit.New(0)}}

	// Library + an artist with an MBID and one without.
	libRes, _ := db.Exec(`INSERT INTO libraries (name,type,root_path) VALUES ('M','music','/m')`)
	libID, _ := libRes.LastInsertId()
	dpRes, _ := db.Exec(`INSERT INTO music_artists (library_id,name,musicbrainz_id) VALUES (?,'Deep Purple','mbid-dp')`, libID)
	dpID, _ := dpRes.LastInsertId()
	noMBIDRes, _ := db.Exec(`INSERT INTO music_artists (library_id,name,musicbrainz_id) VALUES (?,'Obscure','')`, libID)
	noID, _ := noMBIDRes.LastInsertId()
	albRes, _ := db.Exec(`INSERT INTO music_albums (library_id,artist_id,album_artist_id,title,year) VALUES (?,?,?,'Machine Head',1972)`, libID, dpID, dpID)
	albID, _ := albRes.LastInsertId()

	track := func(artistID int64, title, path string) int64 {
		res, err := db.Exec(`INSERT INTO music_tracks (library_id,artist_id,album_id,title,abs_path) VALUES (?,?,?,?,?)`,
			libID, artistID, albID, title, path)
		if err != nil {
			t.Fatalf("insert track: %v", err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	// Title varies in case/punctuation to exercise NormalizeForDedup matching.
	smoke := track(dpID, "Smoke On The Water", "/m/1.mp3")
	hush := track(dpID, "Hush", "/m/2.mp3")
	deepCut := track(dpID, "Maybe I'm a Leo", "/m/3.mp3") // not in top recordings
	obscure := track(noID, "Whatever", "/m/4.mp3")        // artist has no MBID → not fetched

	if err := m.fetchPopularity(ctx, 0, libID); err != nil {
		t.Fatalf("fetchPopularity: %v", err)
	}

	pop := func(id int64) int {
		var p int
		if err := db.QueryRow(`SELECT popularity FROM music_tracks WHERE id=?`, id).Scan(&p); err != nil {
			t.Fatalf("scan popularity: %v", err)
		}
		return p
	}
	if got := pop(smoke); got != 900000 {
		t.Errorf("Smoke popularity = %d, want 900000", got)
	}
	if got := pop(hush); got != 120000 {
		t.Errorf("Hush popularity = %d, want 120000", got)
	}
	if got := pop(deepCut); got != 0 {
		t.Errorf("deep cut popularity = %d, want 0 (no matching recording)", got)
	}
	if got := pop(obscure); got != 0 {
		t.Errorf("no-MBID artist's track popularity = %d, want 0 (never fetched)", got)
	}
}

// TestFetchPopularityLastfmBlend verifies the optional Last.fm layer fills only
// the tracks ListenBrainz left at 0, and never overrides an LB-credited count.
func TestFetchPopularityLastfmBlend(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	lbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"recording_name":"Smoke on the Water","total_listen_count":900000}]`))
	}))
	defer lbSrv.Close()
	lfSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Last.fm knows the deep cut LB missed, and also Smoke (lower count) —
		// which must NOT overwrite LB's value.
		_, _ = w.Write([]byte(`{"toptracks":{"track":[
			{"name":"Maybe I'm a Leo","playcount":"7777"},
			{"name":"Smoke on the Water","playcount":"3"}
		]}}`))
	}))
	defer lfSrv.Close()

	m := &Matcher{
		db:     db,
		lb:     &LBClient{client: lbSrv.Client(), baseURL: lbSrv.URL, limiter: ratelimit.New(0)},
		lastfm: &LastfmClient{client: lfSrv.Client(), apiKey: "k", baseURL: lfSrv.URL, limiter: ratelimit.New(0)},
	}

	libRes, _ := db.Exec(`INSERT INTO libraries (name,type,root_path) VALUES ('M','music','/m')`)
	libID, _ := libRes.LastInsertId()
	arRes, _ := db.Exec(`INSERT INTO music_artists (library_id,name,musicbrainz_id) VALUES (?,'Deep Purple','mbid-dp')`, libID)
	arID, _ := arRes.LastInsertId()
	albRes, _ := db.Exec(`INSERT INTO music_albums (library_id,artist_id,album_artist_id,title,year) VALUES (?,?,?,'Machine Head',1972)`, libID, arID, arID)
	albID, _ := albRes.LastInsertId()
	track := func(title, path string) int64 {
		res, _ := db.Exec(`INSERT INTO music_tracks (library_id,artist_id,album_id,title,abs_path) VALUES (?,?,?,?,?)`,
			libID, arID, albID, title, path)
		id, _ := res.LastInsertId()
		return id
	}
	smoke := track("Smoke On The Water", "/m/1.mp3")
	deepCut := track("Maybe I'm a Leo", "/m/2.mp3") // LB misses it; Last.fm fills it

	if err := m.fetchPopularity(ctx, 0, libID); err != nil {
		t.Fatalf("fetchPopularity: %v", err)
	}
	pop := func(id int64) int {
		var p int
		_ = db.QueryRow(`SELECT popularity FROM music_tracks WHERE id=?`, id).Scan(&p)
		return p
	}
	if got := pop(smoke); got != 900000 {
		t.Errorf("Smoke popularity = %d, want 900000 (LB stays primary, Last.fm must not override)", got)
	}
	if got := pop(deepCut); got != 7777 {
		t.Errorf("deep cut popularity = %d, want 7777 (filled by Last.fm)", got)
	}
}

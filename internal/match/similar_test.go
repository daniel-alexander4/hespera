package match

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"hespera/internal/ratelimit"
)

// similarStub serves the labs similar-artists payload and counts requests.
func similarStub(t *testing.T, hits *atomic.Int64, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestFetchSimilarArtistsPhase: the pre-warm phase fills the cache the Instant
// Mix pool is drawn from, for every artist that can resolve — which is what
// stops a first mix from silently degrading to a single-artist queue.
func TestFetchSimilarArtistsPhase(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var hits atomic.Int64
	srv := similarStub(t, &hits, `[
		{"artist_mbid":"mbid-sabbath","name":"Black Sabbath","comment":"","score":900},
		{"artist_mbid":"mbid-purple","name":"Deep Purple","comment":"","score":700}
	]`)
	m := &Matcher{db: db, lb: &LBClient{client: srv.Client(), labsURL: srv.URL, limiter: ratelimit.New(0)}}

	libRes, _ := db.Exec(`INSERT INTO libraries (name,type,root_path) VALUES ('M','music','/m')`)
	libID, _ := libRes.LastInsertId()
	add := func(name, mbid string) int64 {
		res, err := db.Exec(`INSERT INTO music_artists (library_id,name,musicbrainz_id) VALUES (?,?,?)`, libID, name, mbid)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	rainbow := add("Rainbow", "mbid-rainbow")
	noMBID := add("Obscure", "")           // can never resolve — the model is MBID-keyed
	various := add("Various Artists", "x") // a placeholder, not an artist

	if err := m.fetchSimilarArtists(ctx, 0, libID, false); err != nil {
		t.Fatalf("fetchSimilarArtists: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 (only the MBID-bearing real artist)", got)
	}

	var payload, stamp string
	if err := db.QueryRow(`SELECT similar_json, similar_fetched_at FROM music_artists WHERE id=?`, rainbow).
		Scan(&payload, &stamp); err != nil {
		t.Fatal(err)
	}
	var list []SimilarArtist
	if err := json.Unmarshal([]byte(payload), &list); err != nil {
		t.Fatalf("cached payload is not a similar-artist list: %v (%q)", err, payload)
	}
	if len(list) != 2 || list[0].Name != "Black Sabbath" || list[0].Score != 900 {
		t.Fatalf("cached list = %+v", list)
	}
	if stamp == "" {
		t.Fatalf("fetched-at stamp not written")
	}
	// The two ineligible artists are left entirely alone.
	for _, id := range []int64{noMBID, various} {
		var s string
		if err := db.QueryRow(`SELECT similar_fetched_at FROM music_artists WHERE id=?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		if s != "" {
			t.Fatalf("artist %d should not have been fetched, stamp=%q", id, s)
		}
	}
}

// TestFetchSimilarArtistsRecheckTTL: the gate Dan's two requirements rest on —
// a new artist is always warmed, an already-cached one is refreshed once its
// TTL lapses (so lists don't freeze at first fetch), and a fresh one costs no
// network on an automatic run.
func TestFetchSimilarArtistsRecheckTTL(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var hits atomic.Int64
	srv := similarStub(t, &hits, `[]`)
	m := &Matcher{db: db, lb: &LBClient{client: srv.Client(), labsURL: srv.URL, limiter: ratelimit.New(0)}}

	libRes, _ := db.Exec(`INSERT INTO libraries (name,type,root_path) VALUES ('M','music','/m')`)
	libID, _ := libRes.LastInsertId()
	add := func(name, mbid, stamp string) {
		if _, err := db.Exec(`INSERT INTO music_artists (library_id,name,musicbrainz_id,similar_fetched_at) VALUES (?,?,?,?)`,
			libID, name, mbid, stamp); err != nil {
			t.Fatal(err)
		}
	}
	add("Fresh", "mbid-f", freshStamp) // cached inside the TTL
	add("Stale", "mbid-s", oldStamp)   // cached, TTL lapsed → refresh
	add("New", "mbid-n", "")           // never fetched → always warmed

	if err := m.fetchSimilarArtists(ctx, 0, libID, false); err != nil {
		t.Fatalf("automatic run: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("automatic run made %d requests, want 2 (stale + new, not fresh)", got)
	}

	// A user-initiated match refetches everyone, the force convention.
	hits.Store(0)
	if err := m.fetchSimilarArtists(ctx, 0, libID, true); err != nil {
		t.Fatalf("force run: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("force run made %d requests, want 3 (all artists)", got)
	}

	// Everything is now stamped, so a second automatic run is free.
	hits.Store(0)
	if err := m.fetchSimilarArtists(ctx, 0, libID, false); err != nil {
		t.Fatalf("second automatic run: %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("second automatic run made %d requests, want 0", got)
	}
}

// TestStoreArtistSimilarStampsOnMiss: a coverage gap (the labs model 400s or
// knows nothing about this MBID) must still stamp, or a miss would be retried
// on every single run and every artist-page view forever.
func TestStoreArtistSimilarStampsOnMiss(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest) // the labs endpoint's "no data" answer
	}))
	defer srv.Close()
	m := &Matcher{db: db, lb: &LBClient{client: srv.Client(), labsURL: srv.URL, limiter: ratelimit.New(0)}}

	libRes, _ := db.Exec(`INSERT INTO libraries (name,type,root_path) VALUES ('M','music','/m')`)
	libID, _ := libRes.LastInsertId()
	res, _ := db.Exec(`INSERT INTO music_artists (library_id,name,musicbrainz_id) VALUES (?,'Nobody','mbid-x')`, libID)
	id, _ := res.LastInsertId()

	if err := m.StoreArtistSimilar(ctx, id, "mbid-x"); err != nil {
		t.Fatalf("StoreArtistSimilar: %v", err)
	}
	var payload, stamp string
	if err := db.QueryRow(`SELECT similar_json, similar_fetched_at FROM music_artists WHERE id=?`, id).
		Scan(&payload, &stamp); err != nil {
		t.Fatal(err)
	}
	if stamp == "" {
		t.Fatalf("a miss must still stamp, or it retries forever")
	}
	// Stored as an empty JSON list, not as a null or an empty string — the
	// readers unmarshal this column unconditionally.
	if strings.TrimSpace(payload) != "[]" && strings.TrimSpace(payload) != "null" {
		t.Fatalf("payload on miss = %q, want an empty list", payload)
	}
	var list []SimilarArtist
	if err := json.Unmarshal([]byte(payload), &list); err != nil {
		t.Fatalf("payload on miss does not unmarshal: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("payload on miss = %+v, want empty", list)
	}

	// And the stamp means the phase skips it next time.
	hits.Store(0)
	if err := m.fetchSimilarArtists(ctx, 0, libID, false); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("a stamped miss was retried %d times on the next run, want 0", got)
	}
}

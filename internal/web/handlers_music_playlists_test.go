package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// seedPlaylistLib seeds a library + one artist + one album + n tracks,
// returning the track ids.
func seedPlaylistLib(t *testing.T, db *sql.DB, n int) []int64 {
	t.Helper()
	mustExec := func(q string, args ...any) sql.Result {
		t.Helper()
		res, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
		return res
	}
	libID, _ := mustExec(`INSERT INTO libraries (name,type,root_path) VALUES ('M','music','/m')`).LastInsertId()
	artistID, _ := mustExec(`INSERT INTO music_artists (library_id,name,bio,bio_source_url) VALUES (?,'A','','')`, libID).LastInsertId()
	albumID, _ := mustExec(`INSERT INTO music_albums (library_id,artist_id,album_artist_id,title,year,is_compilation) VALUES (?,?,?,'Alb',1990,0)`, libID, artistID, artistID).LastInsertId()
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		id, _ := mustExec(`INSERT INTO music_tracks (library_id,artist_id,album_id,title,track_no,disc_no,abs_path,mime_type) VALUES (?,?,?,?,?,1,?,'audio/mpeg')`,
			libID, artistID, albumID, fmt.Sprintf("T%02d", i+1), i+1, fmt.Sprintf("/m/t%02d.mp3", i+1)).LastInsertId()
		ids = append(ids, id)
	}
	return ids
}

func postPlaylistForm(t *testing.T, router http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// queueTitles fetches /music/queue with the given query and returns the track titles in order.
func queueTitles(t *testing.T, router http.Handler, query string) (string, []string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/music/queue?"+query, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("queue %q: status %d", query, rec.Code)
	}
	var out struct {
		Title  string `json:"title"`
		Tracks []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("queue json: %v", err)
	}
	titles := make([]string, 0, len(out.Tracks))
	for _, tr := range out.Tracks {
		titles = append(titles, tr.Title)
	}
	return out.Title, titles
}

func TestPlaylistLifecycle(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	tracks := seedPlaylistLib(t, db, 4)

	// Create with a first track (the picker's "New playlist…" flow).
	rec := postPlaylistForm(t, router, "/music/playlist/create",
		url.Values{"name": {"Road Trip"}, "track_id": {strconv.FormatInt(tracks[0], 10)}})
	if rec.Code != 200 {
		t.Fatalf("create: status %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID    int64  `json:"id"`
		Count int    `json:"count"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID <= 0 || created.Count != 1 {
		t.Fatalf("create response = %s (err %v)", rec.Body.String(), err)
	}
	pid := strconv.FormatInt(created.ID, 10)

	// Add two more; re-adding an existing track is a no-op (idempotent).
	for _, tid := range []int64{tracks[1], tracks[2], tracks[1]} {
		rec := postPlaylistForm(t, router, "/music/playlist/add-track",
			url.Values{"playlist_id": {pid}, "track_id": {strconv.FormatInt(tid, 10)}})
		if rec.Code != 200 {
			t.Fatalf("add-track: status %d", rec.Code)
		}
	}
	if _, titles := queueTitles(t, router, "source=playlist&playlist="+pid); strings.Join(titles, ",") != "T01,T02,T03" {
		t.Fatalf("queue after adds = %v, want T01,T02,T03", titles)
	}

	// The picker's list endpoint reports the count.
	req := httptest.NewRequest(http.MethodGet, "/music/playlists", nil)
	lrec := httptest.NewRecorder()
	router.ServeHTTP(lrec, req)
	var lst struct {
		Playlists []playlistRow `json:"playlists"`
	}
	if err := json.Unmarshal(lrec.Body.Bytes(), &lst); err != nil || len(lst.Playlists) != 1 || lst.Playlists[0].Count != 3 {
		t.Fatalf("playlists list = %s (err %v)", lrec.Body.String(), err)
	}

	// Move T03 up a step.
	postPlaylistForm(t, router, "/music/playlist/move",
		url.Values{"playlist_id": {pid}, "track_id": {strconv.FormatInt(tracks[2], 10)}, "dir": {"up"}})
	if _, titles := queueTitles(t, router, "source=playlist&playlist="+pid); strings.Join(titles, ",") != "T01,T03,T02" {
		t.Fatalf("queue after move = %v, want T01,T03,T02", titles)
	}
	// Moving the top item up is a clean no-op.
	postPlaylistForm(t, router, "/music/playlist/move",
		url.Values{"playlist_id": {pid}, "track_id": {strconv.FormatInt(tracks[0], 10)}, "dir": {"up"}})
	if _, titles := queueTitles(t, router, "source=playlist&playlist="+pid); strings.Join(titles, ",") != "T01,T03,T02" {
		t.Fatalf("queue after edge move = %v, want unchanged", titles)
	}

	// Remove the first track — the rest renumber contiguously.
	postPlaylistForm(t, router, "/music/playlist/remove-track",
		url.Values{"playlist_id": {pid}, "track_id": {strconv.FormatInt(tracks[0], 10)}})
	if _, titles := queueTitles(t, router, "source=playlist&playlist="+pid); strings.Join(titles, ",") != "T03,T02" {
		t.Fatalf("queue after remove = %v, want T03,T02", titles)
	}
	var positions string
	_ = db.QueryRow("SELECT group_concat(position) FROM (SELECT position FROM playlist_tracks WHERE playlist_id=? ORDER BY position)", created.ID).Scan(&positions)
	if positions != "1,2" {
		t.Fatalf("positions after remove = %q, want 1,2", positions)
	}

	// Rename shows up in the queue title; delete cascades membership.
	postPlaylistForm(t, router, "/music/playlist/rename", url.Values{"id": {pid}, "name": {"Renamed"}})
	if title, _ := queueTitles(t, router, "source=playlist&playlist="+pid); title != "Renamed" {
		t.Fatalf("queue title = %q, want Renamed", title)
	}
	postPlaylistForm(t, router, "/music/playlist/delete", url.Values{"id": {pid}})
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM playlist_tracks WHERE playlist_id=?", created.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("membership after delete = %d, want 0 (cascade)", n)
	}
}

// TestPlaylistSaveQueue covers the now-playing "Save as playlist" shape:
// create with a track_ids list, duplicates collapsed.
func TestPlaylistSaveQueue(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	tracks := seedPlaylistLib(t, db, 3)

	ids := fmt.Sprintf("%d,%d,%d,%d", tracks[2], tracks[0], tracks[1], tracks[2])
	rec := postPlaylistForm(t, router, "/music/playlist/create",
		url.Values{"name": {"Snapshot"}, "track_ids": {ids}})
	if rec.Code != 200 {
		t.Fatalf("create: status %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID    int64 `json:"id"`
		Count int   `json:"count"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Count != 3 {
		t.Fatalf("count = %d, want 3 (duplicate collapsed)", created.Count)
	}
	if _, titles := queueTitles(t, router, fmt.Sprintf("source=playlist&playlist=%d", created.ID)); strings.Join(titles, ",") != "T03,T01,T02" {
		t.Fatalf("saved order = %v, want queue order T03,T01,T02", titles)
	}
}

// seedMixLib builds a seed artist + two in-catalog similar artists with
// similarity scores and per-track popularity, returning ids.
func seedMixLib(t *testing.T, db *sql.DB) (seedArtist int64, trackTitlesByArtist map[int64][]string, artistNames map[int64]string, firstSeedTrack int64) {
	t.Helper()
	mustExec := func(q string, args ...any) sql.Result {
		t.Helper()
		res, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
		return res
	}
	libID, _ := mustExec(`INSERT INTO libraries (name,type,root_path) VALUES ('M','music','/m')`).LastInsertId()
	mk := func(name, mbid string) int64 {
		id, _ := mustExec(`INSERT INTO music_artists (library_id,name,bio,bio_source_url,musicbrainz_id) VALUES (?,?,'','',?)`, libID, name, mbid).LastInsertId()
		return id
	}
	seedArtist = mk("Seed", "00000000-0000-0000-0000-00000000000a")
	simA := mk("SimA", "00000000-0000-0000-0000-00000000000b")
	simB := mk("SimB", "00000000-0000-0000-0000-00000000000c")

	// The seed's cached similar list: SimA stronger than SimB, plus one
	// out-of-catalog artist that must be ignored.
	// The stored shape is json.Marshal([]match.SimilarArtist) — Go field names.
	similar := `[{"MBID":"00000000-0000-0000-0000-00000000000b","Name":"SimA","Score":900},
	 {"MBID":"00000000-0000-0000-0000-00000000000c","Name":"SimB","Score":300},
	 {"MBID":"00000000-0000-0000-0000-00000000000d","Name":"NotLocal","Score":9999}]`
	mustExec(`UPDATE music_artists SET similar_json=?, similar_fetched_at=datetime('now') WHERE id=?`, similar, seedArtist)

	trackTitlesByArtist = map[int64][]string{}
	artistNames = map[int64]string{seedArtist: "Seed", simA: "SimA", simB: "SimB"}
	for _, a := range []int64{seedArtist, simA, simB} {
		albumID, _ := mustExec(`INSERT INTO music_albums (library_id,artist_id,album_artist_id,title,year,is_compilation) VALUES (?,?,?,?,2000,0)`,
			libID, a, a, fmt.Sprintf("Alb%d", a)).LastInsertId()
		for i := 0; i < 6; i++ {
			title := fmt.Sprintf("%s-%d", artistNames[a], i+1)
			id, _ := mustExec(`INSERT INTO music_tracks (library_id,artist_id,album_id,title,track_no,disc_no,abs_path,mime_type,popularity) VALUES (?,?,?,?,?,1,?,'audio/mpeg',?)`,
				libID, a, albumID, title, i+1, fmt.Sprintf("/m/%d-%d.mp3", a, i), (i+1)*10).LastInsertId()
			trackTitlesByArtist[a] = append(trackTitlesByArtist[a], title)
			if a == seedArtist && firstSeedTrack == 0 {
				firstSeedTrack = id
			}
		}
	}
	return seedArtist, trackTitlesByArtist, artistNames, firstSeedTrack
}

func TestMixQueue(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	seedArtist, _, artistNames, firstSeedTrack := seedMixLib(t, db)

	title, titles := queueTitles(t, router, fmt.Sprintf("source=mix&artist=%d", seedArtist))
	if title != "Seed Mix" {
		t.Fatalf("mix title = %q, want Seed Mix", title)
	}
	// Pool exhaustion under caps: seed contributes all 6 (cap 8), each similar
	// artist exactly 4 (cap) → 14 tracks, nothing from outside the pool.
	counts := map[string]int{}
	for _, tt := range titles {
		counts[strings.Split(tt, "-")[0]]++
	}
	if len(titles) != 14 || counts["Seed"] != 6 || counts["SimA"] != 4 || counts["SimB"] != 4 {
		t.Fatalf("mix composition = %v (n=%d), want Seed:6 SimA:4 SimB:4", counts, len(titles))
	}
	_ = artistNames

	// Song-seeded: the seed track opens the mix.
	_, titles = queueTitles(t, router, fmt.Sprintf("source=mix&track=%d", firstSeedTrack))
	if len(titles) == 0 || titles[0] != "Seed-1" {
		t.Fatalf("song-seeded mix starts with %v, want Seed-1", titles)
	}
}

func TestMixQueueColdStart(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()
	seedArtist, _, _, _ := seedMixLib(t, db)
	// Wipe the cached similar list → the mix degrades to the seed's own tracks.
	if _, err := db.Exec(`UPDATE music_artists SET similar_json='', similar_fetched_at='' WHERE id=?`, seedArtist); err != nil {
		t.Fatal(err)
	}
	_, titles := queueTitles(t, router, fmt.Sprintf("source=mix&artist=%d", seedArtist))
	if len(titles) != 6 {
		t.Fatalf("cold-start mix has %d tracks, want the seed's 6", len(titles))
	}
	for _, tt := range titles {
		if !strings.HasPrefix(tt, "Seed-") {
			t.Fatalf("cold-start mix contains %q, want seed-only tracks", tt)
		}
	}
}

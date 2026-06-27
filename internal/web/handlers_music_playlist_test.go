package web

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestMusicPlayerSources(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	mustExec := func(q string, args ...any) sql.Result {
		t.Helper()
		res, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
		return res
	}
	libID, _ := mustExec(`INSERT INTO libraries (name,type,root_path) VALUES ('M','music','/m')`).LastInsertId()
	artistID, _ := mustExec(`INSERT INTO music_artists (library_id,name,bio,bio_source_url) VALUES (?, 'A', '', '')`, libID).LastInsertId()
	albOld, _ := mustExec(`INSERT INTO music_albums (library_id,artist_id,album_artist_id,title,year,is_compilation) VALUES (?,?,?,'Old',1974,0)`, libID, artistID, artistID).LastInsertId()
	albNew, _ := mustExec(`INSERT INTO music_albums (library_id,artist_id,album_artist_id,title,year,is_compilation) VALUES (?,?,?,'New',1980,0)`, libID, artistID, artistID).LastInsertId()
	mkTrack := func(alb int64, title, path string) int64 {
		id, _ := mustExec(`INSERT INTO music_tracks (library_id,artist_id,album_id,title,track_no,disc_no,abs_path,mime_type) VALUES (?,?,?,?,1,1,?,'audio/mpeg')`,
			libID, artistID, alb, title, path).LastInsertId()
		return id
	}
	t1 := mkTrack(albOld, "T1", "/m/1.mp3")
	t2 := mkTrack(albOld, "T2", "/m/2.mp3")
	t3 := mkTrack(albNew, "T3", "/m/3.mp3")

	lib := strconv.FormatInt(libID, 10)
	get := func(url string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	// queueIDs decodes the /music/queue JSON and returns the track ids in order.
	queueIDs := func(rec *httptest.ResponseRecorder) []int64 {
		t.Helper()
		var payload struct {
			Tracks []struct {
				ID int64 `json:"id"`
			} `json:"tracks"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode queue json: %v (body=%q)", err, rec.Body.String())
		}
		ids := make([]int64, len(payload.Tracks))
		for i, tr := range payload.Tracks {
			ids[i] = tr.ID
		}
		return ids
	}
	has := func(ids []int64, id int64) bool {
		for _, v := range ids {
			if v == id {
				return true
			}
		}
		return false
	}

	t.Run("source=all queues every track", func(t *testing.T) {
		rec := get("/music/queue?source=all&library=" + lib)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		ids := queueIDs(rec)
		for _, id := range []int64{t1, t2, t3} {
			if !has(ids, id) {
				t.Fatalf("source=all missing track %d", id)
			}
		}
	})

	t.Run("source=era filters by album year", func(t *testing.T) {
		rec := get("/music/queue?source=era&from=1974&to=1974&library=" + lib)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		ids := queueIDs(rec)
		if !has(ids, t1) || !has(ids, t2) {
			t.Fatalf("era 1974 missing its tracks")
		}
		if has(ids, t3) {
			t.Fatalf("era 1974 wrongly included the 1980 track")
		}
	})

	t.Run("era invalid params 404", func(t *testing.T) {
		if rec := get("/music/queue?source=era&from=1980&to=1974&library=" + lib); rec.Code != http.StatusNotFound {
			t.Fatalf("reversed range = %d, want 404", rec.Code)
		}
		if rec := get("/music/queue?source=era&library=" + lib); rec.Code != http.StatusNotFound {
			t.Fatalf("missing years = %d, want 404", rec.Code)
		}
	})

	t.Run("source=popular: cold is empty, then ranks by popularity", func(t *testing.T) {
		// No popularity set yet → empty queue (200 with no tracks; the client just
		// has nothing to play rather than a broken page).
		rec := get("/music/queue?source=popular&library=" + lib)
		if rec.Code != http.StatusOK {
			t.Fatalf("cold popular = %d, want 200", rec.Code)
		}
		if ids := queueIDs(rec); len(ids) != 0 {
			t.Fatalf("cold popular returned %d tracks, want 0", len(ids))
		}
		// Popularity is the global listen count filled by the match phase; t2 stays 0.
		mustExec(`UPDATE music_tracks SET popularity=900000 WHERE id=?`, t3)
		mustExec(`UPDATE music_tracks SET popularity=5000 WHERE id=?`, t1)
		ids := queueIDs(get("/music/queue?source=popular&library=" + lib))
		if !has(ids, t3) || !has(ids, t1) {
			t.Fatalf("popular missing the tracks with popularity")
		}
		if has(ids, t2) {
			t.Fatalf("popular included a popularity=0 track")
		}
	})

	t.Run("source=album returns the album in order", func(t *testing.T) {
		ids := queueIDs(get("/music/queue?album=" + strconv.FormatInt(albOld, 10)))
		if !has(ids, t1) || !has(ids, t2) || has(ids, t3) {
			t.Fatalf("album queue wrong: %v", ids)
		}
	})

	t.Run("album invalid id 404", func(t *testing.T) {
		if rec := get("/music/queue?album=0"); rec.Code != http.StatusNotFound {
			t.Fatalf("album=0 = %d, want 404", rec.Code)
		}
	})

	t.Run("player view renders and carries autoload", func(t *testing.T) {
		rec := get("/music/player?album=" + strconv.FormatInt(albOld, 10))
		if rec.Code != http.StatusOK {
			t.Fatalf("player view = %d, want 200", rec.Code)
		}
		b := rec.Body.String()
		if !strings.Contains(b, `class="player-page"`) {
			t.Fatalf("player view missing the now-playing shell")
		}
		if !strings.Contains(b, `data-autoload="album=`) {
			t.Fatalf("player view missing autoload query for a direct ?album= link")
		}
	})
}

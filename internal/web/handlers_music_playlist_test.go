package web

import (
	"database/sql"
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
	has := func(body string, id int64) bool {
		return strings.Contains(body, `data-track="`+strconv.FormatInt(id, 10)+`"`)
	}

	t.Run("source=all queues every track", func(t *testing.T) {
		rec := get("/music/player?source=all&shuffle=1&library=" + lib)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		b := rec.Body.String()
		for _, id := range []int64{t1, t2, t3} {
			if !has(b, id) {
				t.Fatalf("source=all missing track %d", id)
			}
		}
	})

	t.Run("source=era filters by album year", func(t *testing.T) {
		rec := get("/music/player?source=era&from=1974&to=1974&shuffle=1&library=" + lib)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		b := rec.Body.String()
		if !has(b, t1) || !has(b, t2) {
			t.Fatalf("era 1974 missing its tracks")
		}
		if has(b, t3) {
			t.Fatalf("era 1974 wrongly included the 1980 track")
		}
	})

	t.Run("era invalid params 404", func(t *testing.T) {
		if rec := get("/music/player?source=era&from=1980&to=1974&library=" + lib); rec.Code != http.StatusNotFound {
			t.Fatalf("reversed range = %d, want 404", rec.Code)
		}
		if rec := get("/music/player?source=era&library=" + lib); rec.Code != http.StatusNotFound {
			t.Fatalf("missing years = %d, want 404", rec.Code)
		}
	})

	t.Run("source=popular: cold redirects, then ranks by popularity", func(t *testing.T) {
		// No popularity set yet → empty queue → redirect rather than a broken player.
		if rec := get("/music/player?source=popular&library=" + lib); rec.Code != http.StatusSeeOther {
			t.Fatalf("cold popular = %d, want 303", rec.Code)
		}
		// Popularity is the global listen count filled by the match phase; t2 stays 0.
		mustExec(`UPDATE music_tracks SET popularity=900000 WHERE id=?`, t3)
		mustExec(`UPDATE music_tracks SET popularity=5000 WHERE id=?`, t1)
		rec := get("/music/player?source=popular&shuffle=1&library=" + lib)
		if rec.Code != http.StatusOK {
			t.Fatalf("popular = %d, want 200", rec.Code)
		}
		b := rec.Body.String()
		if !has(b, t3) || !has(b, t1) {
			t.Fatalf("popular missing the tracks with popularity")
		}
		if has(b, t2) {
			t.Fatalf("popular included a popularity=0 track")
		}
	})

	t.Run("source=album still works", func(t *testing.T) {
		rec := get("/music/player?album=" + strconv.FormatInt(albOld, 10))
		if rec.Code != http.StatusOK {
			t.Fatalf("album = %d, want 200", rec.Code)
		}
		b := rec.Body.String()
		if !has(b, t1) || !has(b, t2) || has(b, t3) {
			t.Fatalf("album queue wrong")
		}
	})
}

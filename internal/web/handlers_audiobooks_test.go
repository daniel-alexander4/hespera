package web

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"hespera/internal/video"
)

func insertAudiobook(t *testing.T, db *sql.DB, container, audioCodec string, withCoverStream bool) int64 {
	t.Helper()
	var libID int64
	if err := db.QueryRow("SELECT id FROM libraries WHERE type='audiobooks'").Scan(&libID); err != nil {
		res, err := db.Exec("INSERT INTO libraries(name,type,root_path) VALUES('Audiobooks','audiobooks','/a')")
		if err != nil {
			t.Fatalf("insert library: %v", err)
		}
		libID, _ = res.LastInsertId()
	}
	probe := video.ProbeResult{
		Format: video.ProbeFormat{Duration: "7200"},
		Streams: []video.ProbeStream{
			{CodecType: "audio", CodecName: audioCodec, IsDefault: true},
		},
		Chapters: []video.ProbeChapter{
			{StartSec: 0, EndSec: 3600, Title: "Chapter One"},
			{StartSec: 3600, EndSec: 7200, Title: "Chapter Two"},
		},
	}
	if withCoverStream {
		// The m4b's embedded cover rides as an attached-pic video stream — the
		// decision must NOT be judged on it.
		probe.Streams = append(probe.Streams, video.ProbeStream{
			CodecType: "video", CodecName: "mjpeg", Width: 600, Height: 600, AttachedPic: true,
		})
	}
	b, err := json.Marshal(probe)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM audiobooks").Scan(&n)
	res, err := db.Exec(
		"INSERT INTO audiobooks(library_id,abs_path,container,title,author,duration_seconds,chapter_count,stream_info_json) VALUES(?,?,?,?,?,?,?,?)",
		libID, "/a/book"+strconv.Itoa(n)+"."+container, container, "A Book", "An Author", 7200.0, 2, string(b))
	if err != nil {
		t.Fatalf("insert audiobook: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func getAudiobookSession(t *testing.T, h *Handler, fileID int64, query string) playbackSessionResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/audiobook/playback-session?file="+strconv.FormatInt(fileID, 10)+query, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120 Safari/537")
	rr := httptest.NewRecorder()
	h.audiobookPlaybackSession(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	var resp playbackSessionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestAudiobookPlaybackSessionDecisions(t *testing.T) {
	h, db := newTestHandler(t)

	t.Run("aac m4b direct-plays despite the attached-pic cover", func(t *testing.T) {
		id := insertAudiobook(t, db, "m4b", "aac", true)
		resp := getAudiobookSession(t, h, id, "")
		if resp.Decision != "direct_play" {
			t.Fatalf("decision = %q, want direct_play (reasons %v)", resp.Decision, resp.Reasons)
		}
		if resp.URL != "/stream/audiobook/"+strconv.FormatInt(id, 10) {
			t.Fatalf("url = %q", resp.URL)
		}
		if len(resp.Chapters) != 2 {
			t.Fatalf("chapters = %+v, want 2 marks", resp.Chapters)
		}
		if resp.DurationSecs != 7200 {
			t.Fatalf("duration = %v", resp.DurationSecs)
		}
	})

	t.Run("undecodable audio routes to the audio remux, never HLS", func(t *testing.T) {
		id := insertAudiobook(t, db, "m4b", "ac3", false)
		resp := getAudiobookSession(t, h, id, "")
		if resp.Decision != "direct_stream" || resp.Protocol != "file" {
			t.Fatalf("decision/protocol = %q/%q, want direct_stream/file (reasons %v)", resp.Decision, resp.Protocol, resp.Reasons)
		}
		want := "/stream/audiobook-remux/" + strconv.FormatInt(id, 10) + "?aud=0"
		if resp.URL != want {
			t.Fatalf("url = %q, want %q", resp.URL, want)
		}
	})

	t.Run("resume position + exact stream start", func(t *testing.T) {
		id := insertAudiobook(t, db, "m4b", "aac", false)
		if _, err := db.Exec(
			"INSERT INTO audiobook_playback_progress(file_id,position_seconds,duration_seconds,completed) VALUES(?,?,?,0)",
			id, 4321.0, 7200.0); err != nil {
			t.Fatal(err)
		}
		resp := getAudiobookSession(t, h, id, "")
		if resp.ResumePosition != 4321.0 {
			t.Fatalf("resume = %v, want 4321", resp.ResumePosition)
		}
		if resp.StreamStart != 4321.0 {
			t.Fatalf("stream_start = %v, want 4321 (audio seeks exactly — echo)", resp.StreamStart)
		}
		// A pinned in-play seek target outranks the stored resume.
		resp = getAudiobookSession(t, h, id, "&start=99.5")
		if resp.StreamStart != 99.5 {
			t.Fatalf("stream_start = %v, want 99.5", resp.StreamStart)
		}
	})
}

func TestAudiobookPlaybackProgressEarnOnly(t *testing.T) {
	h, db := newTestHandler(t)
	id := insertAudiobook(t, db, "m4b", "aac", false)

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/audiobook/playback-progress", strings.NewReader(body))
		rr := httptest.NewRecorder()
		h.audiobookPlaybackProgress(rr, req)
		return rr
	}
	idStr := strconv.FormatInt(id, 10)
	if rr := post(`{"file_id":` + idStr + `,"position_seconds":7100,"duration_seconds":7200,"completed":true}`); rr.Code != http.StatusOK {
		t.Fatalf("post = %d", rr.Code)
	}
	// The next open's first tick reports completed:false — the flag must stick.
	if rr := post(`{"file_id":` + idStr + `,"position_seconds":10,"duration_seconds":7200,"completed":false}`); rr.Code != http.StatusOK {
		t.Fatalf("post = %d", rr.Code)
	}
	var pos float64
	var done int
	if err := db.QueryRow(
		"SELECT position_seconds, completed FROM audiobook_playback_progress WHERE file_id=?", id,
	).Scan(&pos, &done); err != nil {
		t.Fatal(err)
	}
	if pos != 10 || done != 1 {
		t.Fatalf("progress = %v/%d, want 10/1 (position moves, completed sticks)", pos, done)
	}
	if rr := post(`garbage`); rr.Code != http.StatusBadRequest {
		t.Fatalf("garbage = %d, want 400", rr.Code)
	}
}

func TestAudiobooksHomeGridAndArt(t *testing.T) {
	h, db := newTestHandler(t)
	id := insertAudiobook(t, db, "m4b", "aac", false)

	req := httptest.NewRequest(http.MethodGet, "/audiobooks", nil)
	rr := httptest.NewRecorder()
	h.audiobooksHome(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "A Book") {
		t.Fatalf("grid = %d: %s", rr.Code, rr.Body.String())
	}

	// No thumb yet → art 404s (the grid shows the placeholder).
	req = httptest.NewRequest(http.MethodGet, "/art/audiobook/"+strconv.FormatInt(id, 10), nil)
	rr = httptest.NewRecorder()
	h.audiobookArt(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("art = %d, want 404 for a pending thumb", rr.Code)
	}
}

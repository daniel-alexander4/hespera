package web

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"hespera/internal/video"
)

func insertTVFileWithProbe(t *testing.T, db *sql.DB, container string, probe video.ProbeResult, size int64) int64 {
	t.Helper()
	b, err := json.Marshal(probe)
	if err != nil {
		t.Fatalf("marshal probe: %v", err)
	}
	lib, err := db.Exec("INSERT INTO libraries(name,type,root_path) VALUES('TV','tv','/m')")
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libID, _ := lib.LastInsertId()
	res, err := db.Exec(
		"INSERT INTO tv_series_files(library_id,abs_path,container,file_size_bytes,stream_info_json) VALUES(?,?,?,?,?)",
		libID, "/m/ep."+container, container, size, string(b),
	)
	if err != nil {
		t.Fatalf("insert tv file: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func getSession(t *testing.T, h *Handler, fileID int64, ua, query string) playbackSessionResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/tv/playback-session?file="+strconv.FormatInt(fileID, 10)+query, nil)
	req.Header.Set("User-Agent", ua)
	rr := httptest.NewRecorder()
	h.tvPlaybackSession(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var resp playbackSessionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, rr.Body.String())
	}
	return resp
}

func TestTVPlaybackSessionDecisions(t *testing.T) {
	h, db := newTestHandler(t)
	const chromeUA = "Mozilla/5.0 Chrome/120 Safari/537"

	t.Run("compatible mp4 direct-plays", func(t *testing.T) {
		probe := video.ProbeResult{
			Format: video.ProbeFormat{Duration: "120.0"},
			Streams: []video.ProbeStream{
				{CodecType: "video", CodecName: "h264", Width: 1280, Height: 720},
				{CodecType: "audio", CodecName: "aac", IsDefault: true},
			},
		}
		id := insertTVFileWithProbe(t, db, "mp4", probe, 100<<20)
		resp := getSession(t, h, id, chromeUA, "")
		if resp.Decision != "direct_play" {
			t.Fatalf("decision = %q, want direct_play (reasons %v)", resp.Decision, resp.Reasons)
		}
		if resp.URL != "/stream/tv/"+strconv.FormatInt(id, 10) {
			t.Fatalf("url = %q", resp.URL)
		}
		if resp.DurationSecs != 120.0 {
			t.Fatalf("duration = %v, want 120", resp.DurationSecs)
		}
		if len(resp.AudioTracks) != 1 || resp.AudioTracks[0].Codec != "aac" {
			t.Fatalf("audio tracks = %+v", resp.AudioTracks)
		}
	})

	t.Run("incompatible mkv transcodes to hls", func(t *testing.T) {
		probe := video.ProbeResult{
			Format: video.ProbeFormat{Duration: "1500"},
			Streams: []video.ProbeStream{
				{CodecType: "video", CodecName: "hevc", Width: 3840, Height: 2160},
				{CodecType: "audio", CodecName: "ac3"},
				{CodecType: "subtitle", CodecName: "hdmv_pgs_subtitle"},
			},
		}
		id := insertTVFileWithProbe(t, db, "mkv", probe, 4<<30)
		resp := getSession(t, h, id, chromeUA, "")
		if resp.Decision != "transcode" || resp.Protocol != "hls" {
			t.Fatalf("decision/protocol = %q/%q, want transcode/hls", resp.Decision, resp.Protocol)
		}
		if resp.URL != "/stream/tv-hls/"+strconv.FormatInt(id, 10)+"/index.m3u8" {
			t.Fatalf("url = %q", resp.URL)
		}
		if len(resp.SubtitleTracks) != 1 || resp.SubtitleTracks[0].Text {
			t.Fatalf("subtitle tracks = %+v, want one non-text (bitmap) track", resp.SubtitleTracks)
		}
	})

	t.Run("h264/aac in mkv remuxes", func(t *testing.T) {
		probe := video.ProbeResult{
			Streams: []video.ProbeStream{
				{CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080},
				{CodecType: "audio", CodecName: "aac"},
			},
		}
		id := insertTVFileWithProbe(t, db, "mkv", probe, 100<<20)
		resp := getSession(t, h, id, chromeUA, "")
		if resp.Decision != "direct_stream" {
			t.Fatalf("decision = %q, want direct_stream (reasons %v)", resp.Decision, resp.Reasons)
		}
		if resp.URL != "/stream/tv-remux/"+strconv.FormatInt(id, 10)+"?aud=0" {
			t.Fatalf("url = %q", resp.URL)
		}
	})

	t.Run("text subtitle yields a sidecar url", func(t *testing.T) {
		probe := video.ProbeResult{
			Streams: []video.ProbeStream{
				{CodecType: "video", CodecName: "h264", Width: 1280, Height: 720},
				{CodecType: "audio", CodecName: "aac"},
				{CodecType: "subtitle", CodecName: "subrip"},
			},
		}
		id := insertTVFileWithProbe(t, db, "mp4", probe, 100<<20)
		resp := getSession(t, h, id, chromeUA, "&sub=1")
		if resp.Decision != "direct_play" {
			t.Fatalf("decision = %q, want direct_play (text sub must not force transcode)", resp.Decision)
		}
		if resp.SubtitleURL != "/stream/tv-subtitles/"+strconv.FormatInt(id, 10)+"?track=1" {
			t.Fatalf("subtitle url = %q", resp.SubtitleURL)
		}
	})
}

func TestTVPlaybackSessionResumePosition(t *testing.T) {
	h, db := newTestHandler(t)
	probe := video.ProbeResult{
		Format:  video.ProbeFormat{Duration: "1000"},
		Streams: []video.ProbeStream{{CodecType: "video", CodecName: "h264", Width: 1280, Height: 720}, {CodecType: "audio", CodecName: "aac"}},
	}
	id := insertTVFileWithProbe(t, db, "mp4", probe, 100<<20)
	if _, err := db.Exec(
		"INSERT INTO tv_playback_progress(file_id,position_seconds,duration_seconds,completed) VALUES(?,?,?,0)",
		id, 333.0, 1000.0,
	); err != nil {
		t.Fatalf("insert progress: %v", err)
	}
	resp := getSession(t, h, id, "Chrome/120", "")
	if resp.ResumePosition != 333.0 {
		t.Fatalf("resume = %v, want 333", resp.ResumePosition)
	}
	if resp.Completed {
		t.Fatal("completed should be false")
	}
}

func TestTVPlaybackSessionBadFile(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/tv/playback-session?file=999999", nil)
	rr := httptest.NewRecorder()
	h.tvPlaybackSession(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestStreamURL(t *testing.T) {
	if got := streamURL("direct_play", 7, 0); got != "/stream/tv/7" {
		t.Fatalf("direct_play url = %q", got)
	}
	if got := streamURL("direct_stream", 7, 2); got != "/stream/tv-remux/7?aud=2" {
		t.Fatalf("direct_stream url = %q", got)
	}
	if got := streamURL("transcode", 7, 0); got != "/stream/tv-hls/7/index.m3u8" {
		t.Fatalf("transcode url = %q", got)
	}
}

func TestHLSAssetNameValidation(t *testing.T) {
	good := []string{"index.m3u8", "seg00001.ts", "seg5.ts"}
	bad := []string{"../etc/passwd", "seg.ts", "evil.sh", "index.m3u8/../x", "seg01.ts.bak"}
	for _, n := range good {
		if !hlsAssetName.MatchString(n) {
			t.Errorf("expected %q to be accepted", n)
		}
	}
	for _, n := range bad {
		if hlsAssetName.MatchString(n) {
			t.Errorf("expected %q to be rejected", n)
		}
	}
}

func TestStreamTVHLSRejectsBadAssetName(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/stream/tv-hls/1/evil.sh", nil)
	rr := httptest.NewRecorder()
	h.streamTVHLS(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for bad asset name", rr.Code)
	}
}

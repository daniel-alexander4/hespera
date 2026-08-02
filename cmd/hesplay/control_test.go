package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// queueParams is the single owner of the /music/queue parameter shape, so its
// per-verb output is worth pinning: the CLI's name path and the remote's id
// path both go through it and must not drift apart.
func TestQueueParams(t *testing.T) {
	cases := []struct {
		name    string
		verb    string
		id      int64
		shuffle bool
		want    string
	}{
		{"album is a bare album= (the server's default branch)", "album", 42, false, "album=42"},
		{"artist carries its source", "artist", 7, false, "artist=7&source=artist"},
		{"mix carries its source", "mix", 7, false, "artist=7&source=mix"},
		{"song maps to the server's track source", "song", 60, false, "source=track&track=60"},
		{"playlist", "playlist", 3, false, "playlist=3&source=playlist"},
		{"popular needs no id", "popular", 0, false, "source=popular"},
		{"all needs no id", "all", 0, false, "source=all"},
		{"shuffle rides along", "artist", 7, true, "artist=7&shuffle=1&source=artist"},
	}
	for _, c := range cases {
		if got := queueParams(c.verb, c.id, c.shuffle).Encode(); got != c.want {
			t.Fatalf("%s: queueParams(%q,%d,%v) = %q, want %q", c.name, c.verb, c.id, c.shuffle, got, c.want)
		}
	}
}

func TestKnownSource(t *testing.T) {
	for _, v := range []string{"popular", "all", "album", "artist", "mix", "song", "playlist"} {
		if !knownSource(v) {
			t.Fatalf("knownSource(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "tracks", "Playlist", "rm -rf", "shell"} {
		if knownSource(v) {
			t.Fatalf("knownSource(%q) = true, want false — an unknown verb must be refused, not defaulted", v)
		}
	}
}

func newTestController() *controller {
	return newController(newClient("http://127.0.0.1:1"), engine{name: "mpv", path: "/nonexistent"})
}

func TestSendAndStopAreSafeWhenIdle(t *testing.T) {
	ct := newTestController()
	if ct.send(actionNext) {
		t.Fatal("send on an idle controller = true, want false — there is no queue to skip")
	}
	ct.stop() // must not panic or block on a nil done channel
}

// A queue that ends on its own must stop reporting itself as playing. Before
// retire existed only stop() cleared the session, so a finished queue left the
// remote showing a transport for music that was over.
func TestRetireClearsOnlyTheCurrentSession(t *testing.T) {
	ct := newTestController()
	ct.mu.Lock()
	ct.gen = 5
	ct.actions = make(chan playAction, 1)
	ct.state = nowPlaying{Title: "current"}
	ct.mu.Unlock()

	ct.retire(4) // an older session finishing late
	ct.mu.Lock()
	stale := ct.actions == nil || ct.state.Title != "current"
	ct.mu.Unlock()
	if stale {
		t.Fatal("a superseded session cleared its successor's state")
	}

	ct.retire(5) // the current one
	ct.mu.Lock()
	cleared := ct.actions == nil && ct.state == (nowPlaying{})
	ct.mu.Unlock()
	if !cleared {
		t.Fatal("the current session did not clear on retire")
	}
}

func TestWindowAround(t *testing.T) {
	ct := newTestController()
	mk := func(n int) []queueTrack {
		out := make([]queueTrack, n)
		for i := range out {
			out[i] = queueTrack{Title: "t" + strconv.Itoa(i+1), Artist: "a"}
		}
		return out
	}

	if got := ct.windowAround(1); len(got) != 0 {
		t.Fatalf("window with no queue = %d rows, want 0", len(got))
	}

	ct.setTracks(mk(100))
	if got := ct.windowAround(0); len(got) != 0 {
		t.Fatalf("window with no current track = %d rows, want 0 — an idle queue lists nothing", len(got))
	}

	// Mid-queue: full window, a little lookback, current flagged exactly once.
	w := ct.windowAround(50)
	if len(w) != queueWindow {
		t.Fatalf("mid-queue window = %d rows, want %d", len(w), queueWindow)
	}
	if w[0].Index != 50-queueLookback {
		t.Fatalf("window starts at %d, want %d (current minus lookback)", w[0].Index, 50-queueLookback)
	}
	cur := 0
	for _, r := range w {
		if r.Current {
			cur++
			if r.Index != 50 {
				t.Fatalf("Current set on index %d, want 50", r.Index)
			}
		}
	}
	if cur != 1 {
		t.Fatalf("%d rows marked Current, want exactly 1", cur)
	}

	// At the very start there is nothing to look back at.
	if w := ct.windowAround(1); w[0].Index != 1 {
		t.Fatalf("window at the head starts at %d, want 1", w[0].Index)
	}

	// At the end the window slides back rather than showing two lonely rows.
	w = ct.windowAround(100)
	if len(w) != queueWindow {
		t.Fatalf("end-of-queue window = %d rows, want a full %d", len(w), queueWindow)
	}
	if last := w[len(w)-1].Index; last != 100 {
		t.Fatalf("end-of-queue window ends at %d, want 100", last)
	}

	// A queue shorter than the window lists all of it and nothing more.
	ct.setTracks(mk(6))
	if w := ct.windowAround(3); len(w) != 6 {
		t.Fatalf("short queue window = %d rows, want all 6", len(w))
	}
}

// jumpTo must refuse anything it cannot honour BEFORE tearing down the playing
// queue — a mistyped row number must not stop the music.
func TestJumpToRejectsOutOfRange(t *testing.T) {
	ct := newTestController()
	if err := ct.jumpTo(context.Background(), 1); err == nil {
		t.Fatal("jump with nothing playing: expected an error")
	}

	ct.setTracks([]queueTrack{{Title: "a"}, {Title: "b"}, {Title: "c"}})
	for _, n := range []int{0, -1, 4, 999} {
		if err := ct.jumpTo(context.Background(), n); err == nil {
			t.Fatalf("jump to %d in a 3-track queue: expected an error", n)
		}
	}
	// The refusals must not have disturbed the queue.
	ct.mu.Lock()
	got := len(ct.tracks)
	ct.mu.Unlock()
	if got != 3 {
		t.Fatalf("a refused jump changed the queue: %d tracks, want 3", got)
	}
}

func TestResolveAndStartRejectsBadRequests(t *testing.T) {
	ct := newTestController()
	cases := []struct {
		name string
		req  playRequest
		want string
	}{
		{"unknown source", playRequest{Source: "shell"}, "unknown source"},
		{"empty source", playRequest{}, "unknown source"},
		{"playlist with neither name nor id", playRequest{Source: "playlist"}, "expected a name or id"},
	}
	for _, c := range cases {
		err := ct.resolveAndStart(context.Background(), c.req)
		if err == nil {
			t.Fatalf("%s: expected an error", c.name)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: error = %q, want it to mention %q", c.name, err, c.want)
		}
	}
}

func TestHandlersRejectWrongMethods(t *testing.T) {
	h := newTestController().routes()
	cases := []struct {
		method, path string
	}{
		{http.MethodPost, "/api/state"},
		{http.MethodPost, "/api/playlists"},
		{http.MethodGet, "/api/play"},
		{http.MethodGet, "/api/jump"},
		{http.MethodGet, "/api/next"},
		{http.MethodGet, "/api/prev"},
		{http.MethodGet, "/api/stop"},
	}
	for _, c := range cases {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(c.method, c.path, nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s = %d, want 405", c.method, c.path, rr.Code)
		}
	}
}

func TestStateReportsIdle(t *testing.T) {
	h := newTestController().routes()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/api/state = %d, want 200", rr.Code)
	}
	var got struct {
		OK      bool       `json:"ok"`
		Playing bool       `json:"playing"`
		Now     nowPlaying `json:"now"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("state is not JSON: %v", err)
	}
	if !got.OK || got.Playing || got.Now != (nowPlaying{}) {
		t.Fatalf("idle state = %+v, want ok with playing=false and an empty now", got)
	}
}

// The shell must be served from the embedded tree, and the hardening headers
// must be on it — the app is the only thing this binary exposes to a browser.
func TestShellIsServedWithSecurityHeaders(t *testing.T) {
	h := newTestController().routes()
	for _, path := range []string{"/", "/app.js", "/app.css", "/manifest.webmanifest", "/sw.js"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rr.Code)
		}
		if csp := rr.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
			t.Fatalf("GET %s: CSP = %q, want a default-src 'none' policy", path, csp)
		}
		if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("GET %s: missing nosniff", path)
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResolveServerPrecedence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // isolate from any real saved default
	t.Setenv("HESPERA_SERVER", "")
	if got := resolveServer(""); got != "http://127.0.0.1:8080" {
		t.Fatalf("default = %q", got)
	}
	if err := cmdServer([]string{"pi.invalid:9090/"}, ""); err != nil {
		t.Fatalf("cmdServer set: %v", err)
	}
	if got, src := resolveServerWithSource(""); got != "http://pi.invalid:9090" || src != "saved default" {
		t.Fatalf("saved = %q (%s) — should beat the built-in default, gain a scheme, drop the slash", got, src)
	}
	t.Setenv("HESPERA_SERVER", "http://plex.local:8080/")
	if got, src := resolveServerWithSource(""); got != "http://plex.local:8080" || src != "$HESPERA_SERVER" {
		t.Fatalf("env = %q (%s) — env should beat the saved default", got, src)
	}
	if got, src := resolveServerWithSource("other:9090"); got != "http://other:9090" || src != "--server" {
		t.Fatalf("flag = %q (%s) — flag should win over everything", got, src)
	}
}

func TestCmdServerClear(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HESPERA_SERVER", "")
	if err := cmdServer([]string{"clear"}, ""); err != nil {
		t.Fatalf("clear with nothing saved should be a friendly no-op: %v", err)
	}
	if err := cmdServer([]string{"plex.invalid:8080"}, ""); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := savedServer(); got != "http://plex.invalid:8080" {
		t.Fatalf("saved = %q", got)
	}
	if err := cmdServer([]string{"clear"}, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := savedServer(); got != "" {
		t.Fatalf("saved after clear = %q, want empty", got)
	}
	if err := cmdServer([]string{"http://a", "http://b"}, ""); err == nil {
		t.Fatalf("two URLs should error")
	}
}

func TestEngineArgs(t *testing.T) {
	mpv := engine{name: "mpv", path: "/usr/bin/mpv"}
	got := mpv.args("http://s/stream/track/7", -3.5)
	want := []string{"--no-video", "--really-quiet", "--af=lavfi=[volume=-3.50dB]", "http://s/stream/track/7"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("mpv args = %v, want %v", got, want)
	}
	ffplay := engine{name: "ffplay", path: "/usr/bin/ffplay"}
	got = ffplay.args("http://s/stream/track/7", 0)
	want = []string{"-nodisp", "-autoexit", "-loglevel", "error", "-af", "volume=0.00dB", "http://s/stream/track/7"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("ffplay args = %v, want %v", got, want)
	}
}

func TestResolvePlaylist(t *testing.T) {
	rows := []playlistRow{
		{ID: 1, Name: "Road Trip"},
		{ID: 2, Name: "Trip Hop"},
		{ID: 3, Name: "Morning"},
	}
	if id, name, err := resolvePlaylist(rows, "road trip"); err != nil || id != 1 || name != "Road Trip" {
		t.Fatalf("exact ci match: %d %q %v", id, name, err)
	}
	if id, _, err := resolvePlaylist(rows, "morn"); err != nil || id != 3 {
		t.Fatalf("unique substring: %d %v", id, err)
	}
	if _, _, err := resolvePlaylist(rows, "trip"); err == nil || !strings.Contains(err.Error(), "several") {
		t.Fatalf("ambiguous substring should error with candidates, got %v", err)
	}
	if id, _, err := resolvePlaylist(rows, "42"); err != nil || id != 42 {
		t.Fatalf("numeric fallback: %d %v", id, err)
	}
	if _, _, err := resolvePlaylist(rows, "nope"); err == nil {
		t.Fatalf("no match should error")
	}
}

// fakeHespera serves the endpoints hesplay consumes, recording play-event bodies.
func fakeHespera(t *testing.T, playEvents *[]map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Hespera", "0.0.0-test")
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "777" { // no name hits → the numeric-fallback path
			json.NewEncoder(w).Encode(map[string]any{"sections": []map[string]any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"sections": []map[string]any{
			{"label": "Artists", "rows": []map[string]string{
				{"href": "/music/artist/11", "text": "Nirvana Tribute"},
				{"href": "/music/artist/12", "text": "Nirvana"},
			}},
			{"label": "Albums", "rows": []map[string]string{
				{"href": "/music/album/31", "text": "Nevermind", "context": "Nirvana · 1991"},
			}},
		}})
	})
	mux.HandleFunc("/music/queue", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("album") != "31" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"title": "Nevermind", "tracks": []map[string]any{
			{"id": 100, "title": "Smells Like Teen Spirit", "artist": "Nirvana", "album": "Nevermind", "gainDb": -2.5},
		}})
	})
	mux.HandleFunc("/music/play-event", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("play-event body: %v", err)
		}
		if r.Header.Get("Origin") != "" {
			t.Errorf("play-event must not send an Origin header")
		}
		*playEvents = append(*playEvents, body)
		w.Write([]byte(`{"ok":true,"recorded":true}`))
	})
	return httptest.NewServer(mux)
}

func TestClientAgainstFakeServer(t *testing.T) {
	var events []map[string]any
	srv := fakeHespera(t, &events)
	defer srv.Close()
	c := newClient(srv.URL)

	if ver, err := c.probe(); err != nil || ver != "0.0.0-test" {
		t.Fatalf("probe: %q %v", ver, err)
	}

	// Exact (case-insensitive) match beats the earlier prefix row.
	id, picked, err := c.resolveSearch("Artists", "/music/artist/", "nirvana")
	if err != nil || id != 12 || picked != "Nirvana" {
		t.Fatalf("exact-preference resolve: %d %q %v", id, picked, err)
	}
	// No exact match → first row of the section.
	id, _, err = c.resolveSearch("Artists", "/music/artist/", "nirv")
	if err != nil || id != 11 {
		t.Fatalf("first-row resolve: %d %v", id, err)
	}
	// Numeric fallback when nothing matches.
	if id, _, err = c.resolveSearch("Albums", "/music/album/", "777"); err != nil || id != 777 {
		t.Fatalf("numeric fallback: %d %v", id, err)
	}
	// The context rides into the printed pick.
	_, picked, err = c.resolveSearch("Albums", "/music/album/", "Nevermind")
	if err != nil || picked != "Nevermind (Nirvana · 1991)" {
		t.Fatalf("picked label: %q %v", picked, err)
	}

	query, _, err := c.resolveQueueQuery("album", "Nevermind")
	if err != nil {
		t.Fatalf("resolveQueueQuery: %v", err)
	}
	q, err := c.fetchQueue(query)
	if err != nil || q.Title != "Nevermind" || len(q.Tracks) != 1 || q.Tracks[0].GainDB != -2.5 {
		t.Fatalf("fetchQueue: %+v %v", q, err)
	}

	c.reportPlay(100, 90*time.Second, true)
	if len(events) != 1 {
		t.Fatalf("play events recorded = %d, want 1", len(events))
	}
	ev := events[0]
	if ev["track_id"] != float64(100) || ev["played_ms"] != float64(90000) || ev["completed"] != true || ev["source"] != "hesplay" {
		t.Fatalf("play-event body = %v", ev)
	}
}

func TestProbeRejectsStranger(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok")) // 200 "ok" but no X-Hespera header — a reused port
	}))
	defer srv.Close()
	if _, err := newClient(srv.URL).probe(); err == nil {
		t.Fatalf("probe should reject a server without the X-Hespera header")
	}
}

func TestShuffleFor(t *testing.T) {
	cases := []struct {
		verb                 string
		shuffleFlag, ordered bool
		want                 bool
	}{
		{"album", false, false, false}, // albums are sequenced works
		{"artist", false, false, true}, // everything else shuffles by default
		{"mix", false, false, true},
		{"playlist", false, false, true},
		{"album", true, false, true},     // --shuffle forces
		{"playlist", false, true, false}, // --ordered forces
		{"playlist", true, true, true},   // both → shuffle wins
	}
	for _, c := range cases {
		if got := shuffleFor(c.verb, c.shuffleFlag, c.ordered); got != c.want {
			t.Fatalf("shuffleFor(%q, %v, %v) = %v, want %v", c.verb, c.shuffleFlag, c.ordered, got, c.want)
		}
	}
}

func TestResolveQueueQueryNoNameSources(t *testing.T) {
	c := &client{} // popular/all resolve with no server round-trip
	for _, v := range []string{"popular", "all"} {
		q, picked, err := c.resolveQueueQuery(v, "")
		if err != nil || picked != "" || q.Get("source") != v || len(q) != 1 {
			t.Fatalf("resolveQueueQuery(%q): %v %q %v", v, q, picked, err)
		}
	}
}

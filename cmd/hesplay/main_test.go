package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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
	got := mpv.args("http://s/stream/track/7", -3.5, "")
	want := []string{"--no-video", "--really-quiet", "--af=lavfi=[volume=-3.50dB]", "http://s/stream/track/7"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("mpv args = %v, want %v", got, want)
	}
	// The stream URL must stay last, after the IPC flag.
	got = mpv.args("http://s/stream/track/7", 0, "/tmp/hesplay-1-7.sock")
	want = []string{"--no-video", "--really-quiet", "--af=lavfi=[volume=0.00dB]", "--input-ipc-server=/tmp/hesplay-1-7.sock", "http://s/stream/track/7"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("mpv args with ipc = %v, want %v", got, want)
	}
	ffplay := engine{name: "ffplay", path: "/usr/bin/ffplay"}
	got = ffplay.args("http://s/stream/track/7", 0, "/tmp/ignored.sock")
	want = []string{"-nodisp", "-autoexit", "-loglevel", "error", "-af", "volume=0.00dB", "http://s/stream/track/7"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("ffplay args = %v, want %v", got, want)
	}
}

func TestStallSocket(t *testing.T) {
	if got := stallSocket(engine{name: "ffplay"}, 7); got != "" {
		t.Fatalf("ffplay has no IPC, want no socket, got %q", got)
	}
	got := stallSocket(engine{name: "mpv"}, 7)
	if runtime.GOOS == "windows" {
		if got != "" {
			t.Fatalf("windows mpv speaks named pipes, want no socket, got %q", got)
		}
		return
	}
	if !strings.HasSuffix(got, "-7.sock") || !strings.Contains(got, "hesplay-") {
		t.Fatalf("mpv socket = %q, want a per-track hesplay socket", got)
	}
}

func TestStallTracker(t *testing.T) {
	base := time.Unix(1700000000, 0)
	within := base.Add(stallTimeout - time.Second)
	past := base.Add(stallTimeout + time.Second)

	// Advancing playback never stalls, however long the track runs.
	st := newStallTracker(base)
	for i, at := range []time.Time{within, past, past.Add(time.Hour)} {
		if st.observe(float64(i+1)*10, true, at) {
			t.Fatalf("advancing position stalled at sample %d", i)
		}
	}

	// A wedge before the first frame: no readable position, ever.
	st = newStallTracker(base)
	if st.observe(0, false, within) {
		t.Fatalf("stalled inside the grace window")
	}
	if !st.observe(0, false, past) {
		t.Fatalf("want stall once the timeout is exceeded with no progress")
	}

	// A mid-track freeze: position readable but pinned. The clock runs from the
	// last real advance, not from process start.
	st = newStallTracker(base)
	if st.observe(42, true, base.Add(time.Minute)) {
		t.Fatalf("first real position stalled")
	}
	if st.observe(42, true, base.Add(time.Minute+stallTimeout-time.Second)) {
		t.Fatalf("stalled before the timeout elapsed since the last advance")
	}
	if !st.observe(42, true, base.Add(time.Minute+stallTimeout+time.Second)) {
		t.Fatalf("want stall on a pinned position past the timeout")
	}
}

func TestIPCTimePos(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets only")
	}
	// A missing socket is not-ok, not a panic or a hang.
	if _, ok := ipcTimePos(filepath.Join(t.TempDir(), "absent.sock")); ok {
		t.Fatalf("absent socket reported ok")
	}

	cases := []struct {
		name    string
		reply   string
		wantPos float64
		wantOK  bool
	}{
		{"position", `{"data":12.5,"error":"success"}`, 12.5, true},
		{"event first", "{\"event\":\"file-loaded\"}\n{\"data\":3,\"error\":\"success\"}", 3, true},
		{"unavailable", `{"error":"property unavailable"}`, 0, false},
		{"null data", `{"data":null,"error":"success"}`, 0, false},
		{"garbage", `not json`, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sock := filepath.Join(t.TempDir(), "mpv.sock")
			ln, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer ln.Close()
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				io.WriteString(conn, tc.reply+"\n")
			}()
			pos, ok := ipcTimePos(sock)
			if ok != tc.wantOK || pos != tc.wantPos {
				t.Fatalf("ipcTimePos = (%v, %v), want (%v, %v)", pos, ok, tc.wantPos, tc.wantOK)
			}
		})
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
			// A Songs row ACTS: its href is a player URL, so the track id lives
			// in the query string rather than behind a path prefix.
			{"label": "Songs", "rows": []map[string]string{
				{"href": "/music/player?album=31&track=100", "text": "Smells Like Teen Spirit", "context": "Nirvana"},
			}},
		}})
	})
	mux.HandleFunc("/music/queue", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("album") == "31":
			json.NewEncoder(w).Encode(map[string]any{"title": "Nevermind", "tracks": []map[string]any{
				{"id": 100, "title": "Smells Like Teen Spirit", "artist": "Nirvana", "album": "Nevermind", "gainDb": -2.5},
			}})
		case r.URL.Query().Get("source") == "track" && r.URL.Query().Get("track") == "100":
			json.NewEncoder(w).Encode(map[string]any{"title": "Smells Like Teen Spirit", "tracks": []map[string]any{
				{"id": 100, "title": "Smells Like Teen Spirit", "artist": "Nirvana", "album": "Nevermind", "gainDb": -2.5},
			}})
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/music/playlists", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"playlists": []map[string]any{
			{"id": 1, "name": "Road Trip", "count": 12},
			{"id": 2, "name": "Trip Hop", "count": 5},
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

	query, _, err := c.resolveQueueQuery("album", "Nevermind", false)
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
		{"song", false, false, false},  // one track has no order to shuffle
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
		q, picked, err := c.resolveQueueQuery(v, "", false)
		if err != nil || picked != "" || q.Get("source") != v || len(q) != 1 {
			t.Fatalf("resolveQueueQuery(%q): %v %q %v", v, q, picked, err)
		}
	}
}

// TestResolveQueueQueryShuffleParam: the shuffle decision is told to the server,
// not just applied locally — that is what makes a shuffled sweep come back with
// one recording per song instead of the studio and live copies of the same
// track. An ordered play must not carry the flag, or it would silently lose
// tracks the user asked to hear in full.
func TestResolveQueueQueryShuffleParam(t *testing.T) {
	c := &client{}
	for _, v := range []string{"popular", "all"} {
		q, _, err := c.resolveQueueQuery(v, "", true)
		if err != nil || q.Get("shuffle") != "1" {
			t.Fatalf("resolveQueueQuery(%q, shuffle) = %v %v, want shuffle=1", v, q, err)
		}
		if q, _, err = c.resolveQueueQuery(v, "", false); err != nil || q.Has("shuffle") {
			t.Fatalf("resolveQueueQuery(%q, ordered) = %v %v, want no shuffle param", v, q, err)
		}
	}
}

// --- single song ---

// TestResolveSong: a Songs row's id hides in its player href's query string, so
// resolution can't reuse the path-prefix parser the other verbs share.
func TestResolveSong(t *testing.T) {
	var events []map[string]any
	srv := fakeHespera(t, &events)
	defer srv.Close()
	c := newClient(srv.URL)

	id, picked, err := c.resolveSong("smells like teen spirit")
	if err != nil || id != 100 {
		t.Fatalf("resolveSong: %d %q %v", id, picked, err)
	}
	if picked != "Smells Like Teen Spirit (Nirvana)" {
		t.Fatalf("picked label = %q", picked)
	}
	// Numeric fallback when no name matches (the search stub answers "777" with
	// no sections at all).
	if id, _, err = c.resolveSong("777"); err != nil || id != 777 {
		t.Fatalf("numeric fallback: %d %v", id, err)
	}

	// The verb asks for a one-track queue by track id, not for the album.
	query, _, err := c.resolveQueueQuery("song", "Smells Like Teen Spirit", false)
	if err != nil || query.Get("source") != "track" || query.Get("track") != "100" {
		t.Fatalf("resolveQueueQuery(song) = %v %v", query, err)
	}
	q, err := c.fetchQueue(query)
	if err != nil || len(q.Tracks) != 1 || q.Title != "Smells Like Teen Spirit" {
		t.Fatalf("fetchQueue(song) = %+v %v", q, err)
	}
}

func TestTrackIDFromHref(t *testing.T) {
	if id, err := trackIDFromHref("/music/player?album=31&track=100"); err != nil || id != 100 {
		t.Fatalf("track id = %d %v", id, err)
	}
	for _, href := range []string{"/music/player?album=31", "/music/player?track=0", "/music/player?track=x", ""} {
		if _, err := trackIDFromHref(href); err == nil {
			t.Fatalf("%q should not yield a track id", href)
		}
	}
}

// TestFetchQueueOldServerHint: a server predating source=track 404s exactly like
// an absent track id does, and a bare "404 page not found" tells a user nothing
// about what to fix. The hint is keyed on the status code, not message text.
func TestFetchQueueOldServerHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // an old server's album branch, with no album param
	}))
	defer srv.Close()
	c := newClient(srv.URL)

	_, err := c.fetchQueue(url.Values{"source": {"track"}, "track": {"100"}})
	if err == nil || !strings.Contains(err.Error(), "predates single-song playback") {
		t.Fatalf("source=track 404 error = %v, want the upgrade hint", err)
	}
	if !strings.Contains(err.Error(), "100") {
		t.Fatalf("hint should name the track id: %v", err)
	}
	// Other sources keep the plain error — the hint would be a lie there.
	if _, err = c.fetchQueue(url.Values{"album": {"31"}}); err == nil || strings.Contains(err.Error(), "predates") {
		t.Fatalf("album 404 error = %v, want the plain HTTP error", err)
	}
}

// --- shell completion ---

func TestCompletionRemainders(t *testing.T) {
	cases := []struct {
		name    string
		names   []string
		partial string
		want    []string
	}{{
		name:    "no space returns whole candidates",
		names:   []string{"Black Sabbath", "Black Keys"},
		partial: "Black",
		want:    []string{"Black Sabbath", "Black Keys"},
	}, {
		// The shell replaces one word, so only the tail of the candidate may be
		// offered — otherwise "Black Sabbath" would land as "Black Black Sabbath".
		name:    "closed-off words are dropped",
		names:   []string{"Black Sabbath"},
		partial: "Black Sab",
		want:    []string{"Sabbath"},
	}, {
		name:    "case-insensitive on the typed prefix",
		names:   []string{"Black Sabbath"},
		partial: "black Sab",
		want:    []string{"Sabbath"},
	}, {
		// Matched on a later word, so it cannot complete this one.
		name:    "candidates not sharing the prefix are dropped",
		names:   []string{"Black Sabbath", "Paranoid Sabbath Live"},
		partial: "Black Sab",
		want:    []string{"Sabbath"},
	}, {
		name:    "trailing space offers the next word",
		names:   []string{"Black Sabbath"},
		partial: "Black ",
		want:    []string{"Sabbath"},
	}}
	for _, c := range cases {
		got := completionRemainders(c.names, c.partial)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Fatalf("%s: completionRemainders(%v, %q) = %v, want %v", c.name, c.names, c.partial, got, c.want)
		}
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	w.Close()
	os.Stdout = orig
	return <-done
}

func TestCompleteCmd(t *testing.T) {
	var events []map[string]any
	srv := fakeHespera(t, &events)
	defer srv.Close()

	// One candidate per line, whole names — the shell splits them back into the
	// argv words the play verbs rejoin.
	out := captureStdout(t, func() { completeCmd(newClient(srv.URL), []string{"artist", "nirv"}) })
	if got := strings.Split(strings.TrimSpace(out), "\n"); strings.Join(got, "|") != "Nirvana Tribute|Nirvana" {
		t.Fatalf("artist candidates = %q", out)
	}

	// Songs complete on their title — the text a name argument is matched against.
	if out = captureStdout(t, func() { completeCmd(newClient(srv.URL), []string{"song", "smells"}) }); strings.TrimSpace(out) != "Smells Like Teen Spirit" {
		t.Fatalf("song candidates = %q", out)
	}

	// Playlists come from their own endpoint, not search, so they complete with
	// nothing typed at all — and filter on a substring once something is.
	if out = captureStdout(t, func() { completeCmd(newClient(srv.URL), []string{"playlist"}) }); strings.TrimSpace(out) != "Road Trip\nTrip Hop" {
		t.Fatalf("bare playlist candidates = %q, want both", out)
	}
	if out = captureStdout(t, func() { completeCmd(newClient(srv.URL), []string{"playlist", "hop"}) }); strings.TrimSpace(out) != "Trip Hop" {
		t.Fatalf("filtered playlist candidates = %q", out)
	}

	// Silence, not noise, on everything it can't answer: too short for the
	// server's own minimum, an unknown verb, no args, and a dead server.
	for _, args := range [][]string{{"artist", "n"}, {"artist"}, {"tv", "the"}, {}} {
		if out = captureStdout(t, func() { completeCmd(newClient(srv.URL), args) }); out != "" {
			t.Fatalf("completeCmd(%v) printed %q, want nothing", args, out)
		}
	}
	if out = captureStdout(t, func() { completeCmd(newClient("http://127.0.0.1:1"), []string{"artist", "nirv"}) }); out != "" {
		t.Fatalf("dead server printed %q, want nothing", out)
	}
}

func TestCompletionCmdShells(t *testing.T) {
	for _, shell := range []string{"", "bash", "zsh"} {
		args := []string{shell}
		if shell == "" {
			args = nil // bare `completion` defaults to bash
		}
		out := captureStdout(t, func() {
			if err := completionCmd(args); err != nil {
				t.Fatalf("completionCmd(%q): %v", shell, err)
			}
		})
		if !strings.Contains(out, "complete -F _hesplay hesplay") {
			t.Fatalf("completionCmd(%q) emitted no completion registration: %q", shell, out)
		}
		if !strings.Contains(out, `hesplay complete "$cmd"`) {
			t.Fatalf("completionCmd(%q) emitted no name callback", shell)
		}
		if (shell == "zsh") != strings.Contains(out, "bashcompinit") {
			t.Fatalf("completionCmd(%q) bashcompinit presence is wrong", shell)
		}
	}
	if err := completionCmd([]string{"fish"}); err == nil {
		t.Fatalf("an unsupported shell should error")
	}
}

// TestCompletionScriptCoversVerbs: the script's offline verb list is hand-written,
// so it silently rots when a verb is added. Every verb the dispatcher accepts
// must appear in it.
func TestCompletionScriptCoversVerbs(t *testing.T) {
	for _, verb := range []string{"album", "artist", "song", "mix", "playlist", "popular", "all", "playlists", "server", "completion", "version"} {
		if !strings.Contains(bashCompletion, verb) {
			t.Fatalf("completion script never mentions the %q verb", verb)
		}
	}
	// Name-taking verbs must also reach the live-name callback branch.
	for _, verb := range []string{"album", "artist", "song", "mix", "playlist"} {
		if _, ok := completeSections[verb]; !ok && verb != "playlist" {
			t.Fatalf("%q takes a name but has no search section", verb)
		}
	}
	if !strings.Contains(bashCompletion, "album|artist|song|mix|playlist)") {
		t.Fatalf("completion script's name-taking branch is out of sync with the verbs")
	}
}

// control.go — `hesplay --listen`, the phone remote.
//
// hesplay is a foreground CLI: the queue is driven by single keypresses read
// off a terminal in cbreak mode. That is exactly what a phone cannot do — a
// browser speaks no SSH, and driving the CLI over an interactive ssh session
// fails twice over: hesplay traps only SIGINT and SIGTERM, so the SIGHUP that
// follows a dropped session kills playback mid-song; and detaching it (nohup,
// systemd) to survive that removes the terminal, which switches the transport
// keys off entirely (enterCbreak returns nil). Survive-disconnect and
// have-controls are in direct conflict over a shell.
//
// So the remote is a small HTTP surface on the player itself, opt-in via
// --listen. It reuses the seam that was already there: watchKeys produces
// playAction values into a channel and awaitTrack selects on it, so HTTP
// actions are simply a second producer for that same channel — no new
// transport model, and the CLI path is untouched when --listen is absent.
//
// Posture: no authentication, matching Hespera itself (README §Security
// posture). The blast radius is playback on one box — skip, pause the queue,
// change what is playing — not a shell and not the library. Bind it to the LAN
// only on a network you would already let control the speakers.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed all:web
var webFS embed.FS

// The app only ever talks to its own origin (this binary): the control API and
// /art proxy are same-origin, so everything else is denied and a stray CDN or
// font link fails loudly in development rather than phoning home in production.
// img-src allows data: for the artwork placeholder.
const controlCSP = "default-src 'none'; " +
	"script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; manifest-src 'self'; worker-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// nowPlaying is the snapshot the remote renders. The zero value means idle,
// which is what playQueue publishes when a run ends however it ended.
type nowPlaying struct {
	Queue   string `json:"queue"`
	Index   int    `json:"index"` // 1-based within the queue
	Total   int    `json:"total"`
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Artist  string `json:"artist"`
	Album   string `json:"album"`
	AlbumID int64  `json:"albumId"`
}

// controller owns the one playing queue and the lifecycle around replacing it.
// Everything that mutates the session takes mu; the play loop itself runs
// outside the lock (it blocks for the length of a track) and reports back
// through setState.
type controller struct {
	eng    engine
	browse *browseIndex
	// paused is shared with the running play loop: it drives the engine's
	// pause state and holds the stall guard's fire while it is set.
	paused atomic.Bool

	mu         sync.Mutex
	c          *client            // upstream Hespera; swapped by PUT /api/server
	actions    chan playAction    // the current session's action channel, nil when idle
	cancel     context.CancelFunc // cancels the current session
	done       chan struct{}      // closed when the current session's loop returns
	state      nowPlaying
	tracks     []queueTrack // the current queue in play order, for the remote's list
	queueTitle string       // its display title, so a jump can rebuild the same queue
	// gen identifies the current session. A finishing loop clears the session
	// only if it is still the current one: without that check, a queue that ends
	// a moment after being replaced would clear its successor's state and the
	// remote would show idle while music played.
	gen uint64
}

func newController(c *client, e engine) *controller {
	return &controller{c: c, eng: e, browse: newBrowseIndex()}
}

func (ct *controller) upstream() *client {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return ct.c
}

func (ct *controller) setState(np nowPlaying) {
	ct.mu.Lock()
	ct.state = np
	ct.mu.Unlock()
}

func (ct *controller) setTracks(t []queueTrack) {
	ct.mu.Lock()
	ct.tracks = t
	ct.mu.Unlock()
}

// queueWindow bounds what the remote is told about. A sweep queue is routinely
// thousands of tracks ("Most Popular" is 3830 on a real library), and shipping
// all of them on a 2s poll would be hundreds of KB a tick to render a list
// nobody scrolls to the end of. A window around the current track is what a
// phone can actually show, and it re-centres itself as the queue advances.
const (
	queueWindow   = 12
	queueLookback = 2 // a couple of already-played rows, for context
)

// windowAround returns the visible slice of the queue plus the 1-based absolute
// position of each row, centred (ish) on the 1-based nowIndex.
func (ct *controller) windowAround(nowIndex int) []queueEntry {
	ct.mu.Lock()
	tracks, total := ct.tracks, len(ct.tracks)
	ct.mu.Unlock()
	if total == 0 || nowIndex <= 0 {
		return []queueEntry{}
	}
	start := nowIndex - 1 - queueLookback
	if start < 0 {
		start = 0
	}
	end := start + queueWindow
	if end > total {
		end = total
		// Near the end of the queue, slide the window back so it stays full
		// rather than showing two lonely rows.
		if start = end - queueWindow; start < 0 {
			start = 0
		}
	}
	out := make([]queueEntry, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, queueEntry{
			Index:   i + 1,
			Title:   tracks[i].Title,
			Artist:  tracks[i].Artist,
			Current: i+1 == nowIndex,
		})
	}
	return out
}

// queueEntry is one row of the remote's list. Deliberately not the full
// queueTrack: the list needs a label and a position, not gain or album ids.
type queueEntry struct {
	Index   int    `json:"index"` // 1-based, absolute within the queue
	Title   string `json:"title"`
	Artist  string `json:"artist"`
	Current bool   `json:"current"`
}

// retire clears the session identified by gen, if it is still current. A queue
// that simply runs out of tracks — or aborts on a dead server — must stop
// reporting itself as playing; before this, only an explicit stop() cleared the
// session, so a finished queue left the remote showing a transport forever.
func (ct *controller) retire(gen uint64) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if ct.gen != gen {
		return // already replaced by a newer queue; leave its state alone
	}
	ct.cancel, ct.done, ct.actions = nil, nil, nil
	ct.state, ct.tracks, ct.queueTitle = nowPlaying{}, nil, ""
	ct.paused.Store(false)
}

// stop cancels the running session and waits for its loop to return. Waiting is
// the point: exec.CommandContext kills the engine on cancel and awaitTrack
// reaps it before returning, so by the time done closes there is no orphaned
// mpv left behind to fight the next queue for the audio device.
func (ct *controller) stop() {
	ct.mu.Lock()
	cancel, done := ct.cancel, ct.done
	ct.cancel, ct.done, ct.actions = nil, nil, nil
	ct.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// send pushes a transport action to the running session. Buffered depth 1 with
// a default drop, mirroring watchKeys: one action may be pending, and a burst
// of taps must not queue up an unbounded skip run.
func (ct *controller) send(a playAction) bool {
	ct.mu.Lock()
	ch := ct.actions
	ct.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- a:
		return true
	default:
		return true // an action is already pending; the newest tap is noise
	}
}

// start replaces whatever is playing with a new queue. The previous session is
// stopped and reaped first — never concurrently — so two engines can never hold
// the audio device at once.
// jumpTo restarts the CURRENT queue at the 1-based position n, which is how a
// tapped row plays. Deliberately a restart of the same track list rather than a
// new transport action: the list is reused verbatim (no re-fetch, and
// shuffle=false so an already-shuffled queue keeps its order), so positions stay
// exactly what the remote is showing.
func (ct *controller) jumpTo(parent context.Context, n int) error {
	ct.mu.Lock()
	tracks, title := ct.tracks, ct.queueTitle
	ct.mu.Unlock()
	if len(tracks) == 0 {
		return errors.New("nothing is playing")
	}
	if n < 1 || n > len(tracks) {
		return fmt.Errorf("no track %d in a queue of %d", n, len(tracks))
	}
	ct.start(parent, queue{Title: title, Tracks: tracks}, false, n-1)
	return nil
}

func (ct *controller) start(parent context.Context, q queue, shuffle bool, startAt int) {
	ct.stop()

	ctx, cancel := context.WithCancel(parent)
	actions := make(chan playAction, 1)
	done := make(chan struct{})

	ct.paused.Store(false)
	ct.mu.Lock()
	ct.gen++
	gen := ct.gen
	ct.queueTitle = q.Title
	ct.cancel, ct.done, ct.actions = cancel, done, actions
	c := ct.c
	ct.mu.Unlock()

	go func() {
		defer close(done)
		defer cancel()
		defer ct.retire(gen)
		if err := playQueue(ctx, c, ct.eng, q, shuffle, playOpts{
			actions: actions,
			onState: ct.setState,
			onQueue: ct.setTracks,
			startAt: startAt,
			paused:  &ct.paused,
		}); err != nil && ctx.Err() == nil {
			fmt.Println("queue ended:", err)
		}
	}()
}

// resolveAndStart turns a remote's {source,name,id} into a queue and plays it.
func (ct *controller) resolveAndStart(ctx context.Context, req playRequest) error {
	c := ct.upstream()
	verb := strings.TrimSpace(req.Source)
	if !knownSource(verb) {
		return fmt.Errorf("unknown source %q", verb)
	}
	needsTarget := verb != "popular" && verb != "all"
	name := strings.TrimSpace(req.Name)

	var query url.Values
	switch {
	case !needsTarget:
		query = queueParams(verb, 0, req.Shuffle)
	case req.ID > 0:
		// An explicit id is addressed directly. Routing it through the name
		// resolver instead would SEARCH for it first — asking for song id 60
		// resolved to track 7410, because "60" matched a title before the
		// numeric-id fallback was ever reached.
		query = queueParams(verb, req.ID, req.Shuffle)
	case name != "":
		var err error
		if query, _, err = c.resolveQueueQuery(verb, name, req.Shuffle); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%s: expected a name or id", verb)
	}
	q, err := c.fetchQueue(query)
	if err != nil {
		return err
	}
	if len(q.Tracks) == 0 {
		return errors.New("that queue is empty")
	}
	// The server only shuffles the catalog sweeps (sweepSources), so a shuffled
	// playlist or album is shuffled here — the same client-side pass the CLI does.
	// startAt is meaningless once the order is shuffled, so it is ignored there
	// rather than indexing into a sequence the caller never saw.
	start := 0
	if !req.Shuffle && req.StartAt > 1 && req.StartAt <= len(q.Tracks) {
		start = req.StartAt - 1
	}
	ct.start(ctx, q, req.Shuffle, start)
	return nil
}

type playRequest struct {
	Source  string `json:"source"`
	Name    string `json:"name"`
	ID      int64  `json:"id"`
	Shuffle bool   `json:"shuffle"`
	StartAt int    `json:"startAt"` // 1-based; 0 = from the top
}

// serveControl runs the remote until ctx ends. When args names a queue it is
// started immediately, so `hesplay --listen :8090 playlist road trip` both
// serves the phone and begins playing.
func serveControl(ctx context.Context, c *client, addr string, args []string, shuffle bool) error {
	eng, err := findEngine()
	if err != nil {
		return err
	}
	if _, err := c.probe(); err != nil {
		// Not fatal: the box may boot before the server does, and the remote can
		// repoint it. Say so rather than refusing to start.
		fmt.Fprintf(os.Stderr, "warn: %v (set the server from the remote)\n", err)
	}
	ct := newController(c, eng)

	// Warm the browse index in the background. The build is slow on the boxes
	// this actually runs on (21.7s on a Pi 4 over wifi), and a service that
	// starts at boot has all the time in the world — far better than charging
	// that to whoever first taps a letter. Failure is fine: the server may not
	// be up yet, and the first real request rebuilds.
	go func() {
		if err := ct.browse.ensure(ct.upstream().base, false); err != nil {
			fmt.Fprintf(os.Stderr, "warn: could not pre-read the library: %v\n", err)
		}
	}()

	if len(args) > 0 {
		name := strings.TrimSpace(strings.Join(args[1:], " "))
		if err := ct.resolveAndStart(ctx, playRequest{Source: args[0], Name: name, Shuffle: shuffle}); err != nil {
			fmt.Fprintf(os.Stderr, "warn: %v\n", err)
		}
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           ct.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 14,
	}
	go func() {
		<-ctx.Done()
		sd, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sd)
		ct.stop()
	}()

	fmt.Printf("remote on http://%s — open it on your phone and Add to Home Screen\n", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	ct.stop()
	return nil
}

func (ct *controller) routes() http.Handler {
	mux := http.NewServeMux()

	sub, err := fs.Sub(webFS, "web")
	if err != nil { // impossible: the tree is embedded at build time
		panic("hesplay: embed web: " + err.Error())
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/api/state", ct.handleState)
	mux.HandleFunc("/api/playlists", ct.handlePlaylists)
	mux.HandleFunc("/api/letters", ct.handleLetters)
	mux.HandleFunc("/api/artists", ct.handleArtists)
	mux.HandleFunc("/api/artist", ct.handleArtist)
	mux.HandleFunc("/api/album", ct.handleAlbum)
	mux.HandleFunc("/api/play", ct.handlePlay)
	mux.HandleFunc("/api/jump", ct.handleJump)
	mux.HandleFunc("/api/next", ct.handleAction(actionNext))
	mux.HandleFunc("/api/prev", ct.handleAction(actionPrev))
	mux.HandleFunc("/api/pause", ct.handlePause)
	mux.HandleFunc("/api/stop", ct.handleStop)
	mux.HandleFunc("/api/server", ct.handleServer)
	mux.HandleFunc("/art/", ct.handleArt)

	return secureControl(mux)
}

// secureControl sets the same hardening headers as the tapcount template, minus
// its read-only rule — this API accepts POSTs by design.
func secureControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", controlCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=()")
		if r.URL.Path == "/sw.js" {
			h.Set("Cache-Control", "max-age=86400")
		} else {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// errGETOnly / errPOSTOnly are the two method rejections every handler shares.
var (
	errGETOnly  = errors.New("GET only")
	errPOSTOnly = errors.New("POST only")
)

// idParam reads a positive int64 query parameter.
func idParam(r *http.Request, name string) (int64, error) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s must be a positive id", name)
	}
	return id, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"ok": false, "message": err.Error()})
}

func (ct *controller) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, errGETOnly)
		return
	}
	ct.mu.Lock()
	st, playing, base := ct.state, ct.actions != nil, ct.c.base
	ct.mu.Unlock()
	// The window rides on the polled state rather than a separate endpoint: it
	// re-centres itself as the queue advances, so the highlight can never drift
	// out of step with what is playing.
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "playing": playing, "server": base, "now": st,
		"queue": ct.windowAround(st.Index),
		// paused is a state of the playing queue, not a third mode: playing
		// stays true so the transport and the queue list keep their shape.
		"paused": playing && ct.paused.Load(),
		// ffplay has no IPC, so the button hides itself rather than offering
		// something that can only fail.
		"canPause": stallSocket(ct.eng, 1) != "",
		// Fraction 0..1 of the track played, or -1 when unknown (no IPC, or the
		// engine has not reported a duration yet). The remote shows a bar, not a
		// clock, so a fraction is all it needs — and music_tracks has no duration
		// column, making the engine the only source.
		"progress": ct.progress(playing, st.ID),
	})
}

func (ct *controller) handlePlaylists(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, errGETOnly)
		return
	}
	rows, err := ct.upstream().fetchPlaylists()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if rows == nil {
		rows = []playlistRow{} // the server sends null for an empty list; never hand the app a null
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "playlists": rows})
}

func (ct *controller) handlePlay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errPOSTOnly)
		return
	}
	var req playRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("bad request: %w", err))
		return
	}
	// Deliberately context.Background(), not r.Context(): the queue must outlive
	// the request that started it. Tying it to r.Context() would stop the music
	// the instant the phone's fetch completed.
	if err := ct.resolveAndStart(context.Background(), req); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (ct *controller) handleAction(a playAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, errPOSTOnly)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accepted": ct.send(a)})
	}
}

// handleJump plays a tapped row. Like /api/play it uses context.Background():
// the queue must outlive the request that started it.
func (ct *controller) handleJump(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errPOSTOnly)
		return
	}
	var body struct {
		Index int `json:"index"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("bad request: %w", err))
		return
	}
	if err := ct.jumpTo(context.Background(), body.Index); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// progress is the fraction of the current track played, or -1 when it cannot be
// known. Called on every state poll, so it leans on ipcProgress's short
// timeouts rather than adding its own retry.
func (ct *controller) progress(playing bool, trackID int64) float64 {
	if !playing || trackID == 0 {
		return -1
	}
	pos, dur, ok := ipcProgress(stallSocket(ct.eng, trackID))
	if !ok || dur <= 0 {
		return -1
	}
	if pos < 0 {
		pos = 0
	}
	if pos > dur {
		pos = dur
	}
	return pos / dur
}

// handlePause toggles the running engine. Explicit {"paused":bool} is honoured
// so a retry cannot flip the state back by accident; an empty body toggles.
func (ct *controller) handlePause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errPOSTOnly)
		return
	}
	ct.mu.Lock()
	playing, trackID := ct.actions != nil, ct.state.ID
	ct.mu.Unlock()
	if !playing || trackID == 0 {
		writeErr(w, http.StatusConflict, errors.New("nothing is playing"))
		return
	}
	var body struct {
		Paused *bool `json:"paused"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body) // empty body = toggle

	want := !ct.paused.Load()
	if body.Paused != nil {
		want = *body.Paused
	}
	// Set the flag BEFORE the engine call: it is what holds the stall guard off,
	// and the guard polls on its own schedule.
	ct.paused.Store(want)
	if err := ipcSetPause(stallSocket(ct.eng, trackID), want); err != nil {
		ct.paused.Store(!want) // the engine did not take it; do not claim it did
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "paused": want})
}

func (ct *controller) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errPOSTOnly)
		return
	}
	ct.stop()
	ct.setState(nowPlaying{})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleServer shows or repoints the upstream Hespera. A PUT probes first and
// refuses a URL that is not a live Hespera, so the remote can't silently strand
// the box on a typo; it also persists it, so the box comes back to the same
// server after a restart.
func (ct *controller) handleServer(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "server": ct.upstream().base})
	case http.MethodPut, http.MethodPost:
		var body struct {
			Server string `json:"server"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("bad request: %w", err))
			return
		}
		url := normalizeServer(strings.TrimSpace(body.Server))
		if url == "" {
			writeErr(w, http.StatusBadRequest, errors.New("server is required"))
			return
		}
		nc := newClient(url)
		ver, err := nc.probe()
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		ct.mu.Lock()
		ct.c = nc
		ct.mu.Unlock()
		if err := saveServer(url); err != nil {
			fmt.Fprintf(os.Stderr, "warn: could not save the server: %v\n", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "server": url, "version": ver})
	default:
		writeErr(w, http.StatusMethodNotAllowed, errors.New("GET or PUT"))
	}
}

// handleArt proxies album art from the upstream Hespera. The phone is on the
// same LAN and could fetch it directly, but that would be a cross-origin
// request the CSP forbids — proxying keeps the app strictly same-origin.
func (ct *controller) handleArt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, errGETOnly)
		return
	}
	c := ct.upstream()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, c.base+r.URL.Path, nil)
	if err != nil {
		http.Error(w, "bad art path", http.StatusBadRequest)
		return
	}
	resp, err := c.http.Do(req)
	if err != nil {
		http.Error(w, "art unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if ctype := resp.Header.Get("Content-Type"); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.Header().Set("Cache-Control", "max-age=3600")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

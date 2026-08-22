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
// Posture: gated by a three-word passphrase (auth.go), which is a DEPARTURE
// from Hespera itself — the server still has no auth layer. The asymmetry is
// deliberate rather than an oversight: --listen exists to be reached from a
// phone, and the surface behind it is wider than playback (PUT /api/noise
// rewrites a config file, and PUT /api/server plus /art/ together are a proxy
// through a caller-supplied upstream). Plain HTTP, so the credential crosses
// the wire readable — this keeps a houseguest off the speakers, not a sniffer.
// Bind it to the LAN only on a network you would already let control them.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
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
	// kind is what occupies the one audio slot. Noise sessions have no actions
	// channel (there is no transport for a waterfall), so "is something playing"
	// has to be asked of this rather than of actions.
	kind sessionKind
	// noiseScheduled records whether the running noise was started by the
	// reconciler or by a human, which decides whether a window ending stops it.
	noiseScheduled bool
	// noiseLatchUntil suppresses scheduled auto-start after a human stops noise
	// mid-window. Distinct from preemption on purpose: music taking the slot
	// must not silence the rest of the night.
	noiseLatchUntil time.Time
	// noisePause pauses/resumes the running noise player (SIGSTOP/SIGCONT — SoX
	// has no IPC socket the way mpv does). Installed by startNoise once the
	// engine process exists; nil otherwise, which is how /api/state knows to
	// hide the pause control. A closure over the process rather than the
	// process itself so tests never signal anything real (the powerOff-seam
	// shape).
	noisePause func(pause bool) error
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
	ct.kind, ct.noiseScheduled = sessionNone, false
	ct.noisePause = nil
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
	ct.kind, ct.noiseScheduled = sessionNone, false
	ct.noisePause = nil
	ct.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// stopByHuman is stop() plus the manual-stop latch: stopping noise DURING a
// scheduled window suppresses that window's remaining hours, so the reconciler
// does not helpfully restart what someone just switched off. Preemption by a
// queue deliberately does NOT come through here — see decideNoise.
func (ct *controller) stopByHuman(now time.Time) {
	ct.mu.Lock()
	wasNoise := ct.kind == sessionNoise
	ct.mu.Unlock()

	if wasNoise {
		if cfg, err := loadNoiseConfig(); err == nil {
			if w, active := cfg.activeWindow(now); active {
				if end, err := w.endsAt(now); err == nil {
					ct.mu.Lock()
					ct.noiseLatchUntil = end
					ct.mu.Unlock()
				}
			}
		}
	}
	ct.stop()
}

// startNoise puts a noise session in the audio slot. Same stop-then-start
// discipline as a queue — the previous session is cancelled AND reaped before
// this one begins — which is what keeps two engines off the sound card.
func (ct *controller) startNoise(parent context.Context, pl noisePlayer, p noisePreset, audioDev string, scheduled bool) {
	ct.stop()

	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})

	ct.paused.Store(false)
	ct.mu.Lock()
	ct.gen++
	gen := ct.gen
	// No actions channel: there is no next/previous for a waterfall.
	ct.cancel, ct.done, ct.actions = cancel, done, nil
	ct.kind, ct.noiseScheduled = sessionNoise, scheduled
	ct.queueTitle, ct.tracks = "Noise", nil
	ct.state = nowPlaying{
		Queue: "Noise", Index: 1, Total: 1,
		Title:  p.Name + " noise",
		Artist: strings.TrimSuffix(p.Type, "noise"),
	}
	ct.mu.Unlock()

	// The pause seam is installed once the engine process exists, and only if it
	// is still THIS session's engine — a start that lost a race to a newer queue
	// must not hand its successor a signal to a dying process.
	onStart := func(proc *os.Process) {
		if !noisePauseSupported {
			return // leave nil: the remote hides a control that can only fail
		}
		ct.mu.Lock()
		if ct.gen == gen {
			ct.noisePause = func(pause bool) error { return signalNoisePause(proc, pause) }
		}
		ct.mu.Unlock()
	}

	go func() {
		defer close(done)
		defer cancel()
		defer ct.retire(gen)
		if err := runNoise(ctx, pl, p, audioDev, onStart); err != nil && ctx.Err() == nil {
			fmt.Println("noise ended:", err)
		}
	}()
}

// runtime snapshots what decideNoise needs, under one lock.
func (ct *controller) runtime() noiseRuntime {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return noiseRuntime{Kind: ct.kind, Scheduled: ct.noiseScheduled, LatchUntil: ct.noiseLatchUntil}
}

// reconcileNoise runs one pass of the schedule. Config is re-read every pass so
// an edit takes effect without a restart, and because the read is a few hundred
// bytes off local disk once every noiseTick.
func (ct *controller) reconcileNoise(ctx context.Context, now time.Time) {
	cfg, err := loadNoiseConfig()
	if err != nil {
		return // a corrupt config must not take playback down with it
	}
	action, w := decideNoise(cfg, now, ct.runtime())
	switch action {
	case noiseStartAction:
		p, err := cfg.findPreset(w.Preset)
		if err != nil {
			return
		}
		if p, err = p.normalize(); err != nil {
			return
		}
		pl, err := findNoisePlayer()
		if err != nil {
			return
		}
		ct.startNoise(ctx, pl, p, cfg.AudioDev, true)
	case noiseStopAction:
		ct.stop()
	}
}

// watchNoiseSchedule is the reconciler loop. It runs a pass immediately so a
// restart INSIDE a window recovers at once rather than at the next tick — the
// exact case the systemd timers could not handle.
func (ct *controller) watchNoiseSchedule(ctx context.Context) {
	ct.reconcileNoise(ctx, time.Now())
	t := time.NewTicker(noiseTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			ct.reconcileNoise(ctx, now)
		}
	}
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
	ct.kind = sessionQueue
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

	// The noise schedule reconciles here rather than from systemd timers. Started
	// after any command-line verb above so an explicit `hesplay --listen album X`
	// is not immediately preempted by a window that happens to be open.
	go ct.watchNoiseSchedule(ctx)

	a := openAuth()

	srv := &http.Server{
		Addr:              addr,
		Handler:           ct.routes(a),
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
	announcePassphrase(a)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	ct.stop()
	return nil
}

func (ct *controller) routes(a *auth) http.Handler {
	mux := http.NewServeMux()

	sub, err := fs.Sub(webFS, "web")
	if err != nil { // impossible: the tree is embedded at build time
		panic("hesplay: embed web: " + err.Error())
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc(loginPath, handleLogin(a))
	mux.HandleFunc("/api/state", ct.handleState(a))
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
	mux.HandleFunc("/api/noise", ct.handleNoise)
	mux.HandleFunc("/api/noise/start", ct.handleNoiseStart)
	mux.HandleFunc("/art/", ct.handleArt)

	// requireAuth nests INSIDE secureControl so a 401 still carries the CSP and
	// the other hardening headers — secureControl sets them before delegating,
	// so wrapping the other way would short-circuit past them.
	return secureControl(requireAuth(a, sub, mux))
}

const loginPath = "/api/login"

// maxLoginBody bounds the sign-in request. auth.login caps the passphrase
// itself, but the body is the caller's to bound and this is the caller.
const maxLoginBody = 4 << 10

// maxArtBytes bounds a proxied cover image. The upstream is normally Hespera,
// but handleServer lets it be repointed, so "normally" is not a bound.
const maxArtBytes = 32 << 20

var (
	errNonCanonicalPath = errors.New("non-canonical request path")
	errSignInRequired   = errors.New("sign in required")
	errWrongPassphrase  = errors.New("that is not the passphrase")
	errTooManyAttempts  = errors.New("too many attempts — wait a few seconds")
	errNoPassphraseSet  = errors.New("no passphrase is configured on this box — see the server log")
	errNotAlbumArt      = errors.New("not an album art path")

	// artPathRe is the whole set of paths the art proxy will forward. The
	// client only ever requests /art/album/{id} (app.js:209, :293), so anything
	// else is either a bug or someone probing the upstream through us.
	artPathRe = regexp.MustCompile(`^/art/album/[0-9]+$`)

	// artHTTP is the upstream client for the art proxy, with redirects REFUSED.
	// The shared client follows up to ten hops by default, which — given
	// handleServer lets the upstream be repointed — is one redirect away from a
	// general read primitive against the LAN. Built once: newClient carries
	// nothing per-upstream but a timeout, so a per-request copy bought nothing.
	artHTTP = &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
)

// requireAuth gates the remote. Everything needs a session cookie except the
// login endpoint and the static shell, which holds no secret and ships in the
// public .deb.
//
// ⚠️ The FIRST check is not the credential — it is that the path is canonical,
// and that ordering is load-bearing rather than defensive. This middleware
// decides on r.URL.Path (decoded) while ServeMux routes on r.URL.EscapedPath();
// an escaped dot segment survives ServeMux's cleaning untouched, so the two can
// be handed different strings. Demonstrated before this was written:
// /art/%2e%2e/app.js decodes to /art/../app.js, which cleans to /app.js — a
// static file, so the exemption below would have PASSED IT THROUGH — while the
// mux routed it to the gated /art/ handler. Requiring both forms to be already
// canonical makes the two decisions provably the same input, and kills the
// %2e%2e traversal into the upstream proxy on the way past. No real client ever
// sends a dot segment.
func requireAuth(a *auth, assets fs.FS, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !canonicalPath(r.URL) {
			writeErr(w, http.StatusBadRequest, errNonCanonicalPath)
			return
		}
		if r.URL.Path == loginPath || isPublicAsset(assets, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(sessionCookieName); err != nil {
			a.countNoCookie()
		} else if a.validToken(c.Value) {
			next.ServeHTTP(w, r)
			return
		}
		// A box that could not establish a credential says so rather than
		// showing a sign-in that can never succeed. It leaks nothing: the
		// passphrase is not in the message, and the state is already on stderr.
		if !a.usable() {
			writeErr(w, http.StatusServiceUnavailable, errNoPassphraseSet)
			return
		}
		writeErr(w, http.StatusUnauthorized, errSignInRequired)
	})
}

// canonicalPath reports whether the request path is already in cleaned form in
// BOTH its decoded and its escaped spelling. The escaped half is what catches
// double-encoding (%252e%252e), which survives one decode as literal text.
func canonicalPath(u *url.URL) bool {
	if u.Path == "" || u.Path[0] != '/' || path.Clean(u.Path) != u.Path {
		return false
	}
	esc := u.EscapedPath()
	return path.Clean(esc) == esc
}

// isPublicAsset reports whether p names a regular file in the embedded web
// tree. Derived from the tree itself rather than a hand-kept list, so adding an
// asset cannot silently leave it gated — and a directory is not public, so
// /icons/ stops offering a listing.
func isPublicAsset(assets fs.FS, p string) bool {
	name := strings.TrimPrefix(p, "/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) {
		return false
	}
	fi, err := fs.Stat(assets, name)
	return err == nil && fi.Mode().IsRegular()
}

// clientIP is the rate limiter's key.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// handleLogin exchanges the three words for a session cookie.
func handleLogin(a *auth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, errPOSTOnly)
			return
		}
		var body struct {
			Passphrase string `json:"passphrase"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBody)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("bad request: %w", err))
			return
		}
		if !a.usable() {
			writeErr(w, http.StatusServiceUnavailable, errNoPassphraseSet)
			return
		}
		token, status := a.login(clientIP(r), body.Passphrase)
		switch status {
		case http.StatusOK:
			http.SetCookie(w, &http.Cookie{
				Name:  sessionCookieName,
				Value: token,
				Path:  "/",
				// Lax, not Strict: Hespera's home screen links straight here
				// (the noise_remote_url card), and Strict withholds the cookie
				// on a top-level cross-site navigation — so every arrival from
				// Hespera would have shown a sign-in despite a live session.
				// Lax still withholds it from cross-site POSTs, which is every
				// mutating call this API has.
				SameSite: http.SameSiteLaxMode,
				HttpOnly: true,
				// No Secure: this is plain http on a LAN, and a Secure cookie
				// would simply never be stored.
				MaxAge: sessionMaxAge,
			})
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		case http.StatusTooManyRequests:
			writeErr(w, http.StatusTooManyRequests, errTooManyAttempts)
		default:
			writeErr(w, http.StatusUnauthorized, errWrongPassphrase)
		}
	}
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

func (ct *controller) handleState(a *auth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, errGETOnly)
			return
		}
		ct.mu.Lock()
		st, kind, base := ct.state, ct.kind, ct.c.base
		noisePausable := ct.noisePause != nil
		ct.mu.Unlock()
		playing := kind != sessionNone
		// ffplay has no IPC, so the button hides itself rather than offering
		// something that can only fail. Noise pauses by signal, not IPC, so its
		// answer is the seam's presence — installed only where signals work.
		canPause := stallSocket(ct.eng, 1) != ""
		if kind == sessionNoise {
			canPause = noisePausable
		}
		// The window rides on the polled state rather than a separate endpoint: it
		// re-centres itself as the queue advances, so the highlight can never drift
		// out of step with what is playing.
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "playing": playing, "server": base, "now": st,
			// kind lets the remote render a noise session differently from a queue
			// (footer on every screen, no next/previous) without inferring it from
			// an empty track list.
			"kind":  kind.String(),
			"queue": ct.windowAround(st.Index),
			// paused is a state of the playing queue, not a third mode: playing
			// stays true so the transport and the queue list keep their shape.
			"paused": playing && ct.paused.Load(),
			// The auth counters' reader — see authCounts for what an all-zero
			// reading means. Published rather than merely collected, because a
			// counter with no reader proves nothing.
			"auth":     a.snapshot(),
			"canPause": canPause,
			// Fraction 0..1 of the track played, or -1 when unknown (no IPC, or the
			// engine has not reported a duration yet). The remote shows a bar, not a
			// clock, so a fraction is all it needs — and music_tracks has no duration
			// column, making the engine the only source.
			"progress": ct.progress(playing, st.ID),
		})
	}
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
	kind, trackID := ct.kind, ct.state.ID
	pauseNoise := ct.noisePause
	ct.mu.Unlock()
	if kind == sessionNone || (kind == sessionQueue && trackID == 0) {
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
	// Noise pauses by signal, not IPC — see noisePause on the controller. The
	// flag is set only AFTER the signal lands: noise has no stall guard to hold
	// off, so there is nothing the mpv path's set-first ordering would protect.
	if kind == sessionNoise {
		if pauseNoise == nil {
			writeErr(w, http.StatusConflict, errors.New("the noise engine is not running yet"))
			return
		}
		if err := pauseNoise(want); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		ct.paused.Store(want)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "paused": want})
		return
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
	// stopByHuman rather than stop: pressing stop while scheduled noise is
	// playing has to suppress the rest of that window, or the reconciler puts it
	// straight back on within 30 seconds. A queue stop is unaffected.
	ct.stopByHuman(time.Now())
	ct.setState(nowPlaying{})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleNoise reads or replaces the noise configuration. GET also reports which
// preset is playing, so the remote can render the noise screen from one call.
//
// A PUT validates before it writes: an unusable window is refused at the point
// someone types it rather than discovered by a silent night.
func (ct *controller) handleNoise(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := loadNoiseConfig()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		ct.mu.Lock()
		playing, st := ct.kind == sessionNoise, ct.state
		ct.mu.Unlock()
		_, engErr := findNoisePlayer()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "playing": playing, "now": st.Title,
			"default": cfg.Default, "audioDev": cfg.AudioDev,
			"presets": cfg.Presets, "schedule": cfg.Schedule,
			// The UI hides its controls rather than offering something that can
			// only fail, the way the pause button already does for ffplay.
			"available": engErr == nil,
		})

	case http.MethodPut, http.MethodPost:
		var body noiseConfig
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("bad request: %w", err))
			return
		}
		if len(body.Presets) == 0 {
			writeErr(w, http.StatusBadRequest, errors.New("at least one preset is required"))
			return
		}
		for i, p := range body.Presets {
			if strings.TrimSpace(p.Name) == "" {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("preset %d has no name", i+1))
				return
			}
			n, err := p.normalize()
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			n.Name = strings.TrimSpace(p.Name)
			body.Presets[i] = n
		}
		if err := validateSchedule(body.Schedule); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		for i, win := range body.Schedule {
			if strings.TrimSpace(win.Preset) == "" {
				continue
			}
			if _, err := body.findPreset(win.Preset); err != nil {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("window %d: %w", i+1, err))
				return
			}
		}
		if err := saveNoiseConfig(body); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		writeErr(w, http.StatusMethodNotAllowed, errors.New("GET or PUT only"))
	}
}

// handleNoiseStart plays a preset now. Started as MANUAL, so no window ending
// stops it and the reconciler leaves it alone — someone asked for it.
func (ct *controller) handleNoiseStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errPOSTOnly)
		return
	}
	var body struct {
		Preset string `json:"preset"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&body)
	}
	cfg, err := loadNoiseConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	p, err := cfg.findPreset(body.Preset)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if p, err = p.normalize(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	pl, err := findNoisePlayer()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	// A manual start clears any latch: asking for noise is the opposite of the
	// gesture the latch exists to respect.
	ct.mu.Lock()
	ct.noiseLatchUntil = time.Time{}
	ct.mu.Unlock()

	// context.Background() for the same reason /api/play uses it: the session
	// must outlive the request that started it. r.Context() is cancelled when
	// the response is written, which would stop the noise instantly.
	ct.startNoise(context.Background(), pl, p, cfg.AudioDev, false)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "preset": p.Name})
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
	// The client asks for exactly one shape (app.js:209, :293). Anything else is
	// a bug or someone using us to reach the upstream — and handleServer lets
	// the upstream be repointed, so "the upstream is Hespera" is not a bound.
	if !artPathRe.MatchString(r.URL.Path) {
		writeErr(w, http.StatusBadRequest, errNotAlbumArt)
		return
	}
	c := ct.upstream()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, c.base+r.URL.Path, nil)
	if err != nil {
		http.Error(w, "bad art path", http.StatusBadRequest)
		return
	}
	resp, err := artHTTP.Do(req)
	if err != nil {
		http.Error(w, "art unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	// Only a 200 is forwarded. Previously every upstream status was copied
	// through verbatim WITH Cache-Control: max-age=3600 — so an upstream 404 or
	// 500, body and all, was handed to the browser and told to keep it for an
	// hour.
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "art unavailable", http.StatusBadGateway)
		return
	}
	if ctype := resp.Header.Get("Content-Type"); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	// private: the response is only reachable with a session cookie now, so a
	// shared cache must not keep it.
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxArtBytes))
}

// openAuth loads or creates the remote's passphrase, never failing the process:
// the noise reconciler lives in here, and a config-directory problem must not
// take the household's schedule down with it.
func openAuth() *auth {
	p, err := passphrasePath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: no config directory (%v) — using a temporary passphrase that changes on restart\n", err)
		eph, ephErr := ephemeralAuth(nil)
		if ephErr != nil {
			fmt.Fprintf(os.Stderr, "warn: %v — the remote will refuse every sign-in\n", ephErr)
			return newRefusingAuth(nil)
		}
		return eph
	}
	a, _, warning := newAuth(p, nil)
	if warning != nil {
		fmt.Fprintf(os.Stderr, "warn: %v\n", warning)
	}
	return a
}

// announcePassphrase prints the words only to a terminal.
//
// Under systemd stdout is the journal, which is persistent and readable by the
// adm group — so a box that starts as a service says WHERE the passphrase is
// and never what it is. Someone running `hesplay --listen` by hand is looking
// at their own screen and gets the words.
func announcePassphrase(a *auth) {
	if !a.usable() {
		fmt.Fprintln(os.Stderr, "warn: no passphrase is in force — the remote will refuse every sign-in")
		return
	}
	if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		fmt.Printf("passphrase: %s\n", a.phraseForDisplay())
		return
	}
	if p, err := passphrasePath(); err == nil {
		fmt.Printf("the passphrase is in %s\n", p)
		return
	}
	// No terminal to print to AND no file to point at — the ephemeral path,
	// where openAuth could not resolve a config directory. Saying nothing here
	// leaves a remote that serves and that nobody can ever sign in to.
	fmt.Fprintln(os.Stderr,
		"warn: a temporary passphrase is in force and there is no config directory to keep it in — "+
			"run the remote from a terminal to see it, or fix the config directory and restart")
}

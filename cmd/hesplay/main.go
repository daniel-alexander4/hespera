// Command hesplay is a small LAN music player for Hespera — built for a
// headless box with speakers (a Raspberry Pi in another room) pointed at a
// server-mode Hespera. It resolves an album, artist, mix, or playlist to the
// server's ordered queue (GET /music/queue — the same JSON the web player
// consumes), streams each track over HTTP (/stream/track/{id}), and plays it
// through a local engine: mpv when installed, else ffplay (ships with the
// ffmpeg Hespera already depends on). The queue's per-track volume-leveling
// gain rides along as an audio filter, so tracks sit at the same loudness as
// in the web player; finished tracks are reported to /music/play-event
// (best-effort), so Recently Played and listen counts stay honest.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"
)

// version is set at build time via -ldflags "-X main.version=…" (see build.sh);
// a plain `go build` leaves it "dev".
var version = "dev"

func main() {
	server := flag.String("server", "", "Hespera server URL (default: $HESPERA_SERVER, else http://127.0.0.1:8080)")
	shuffle := flag.Bool("shuffle", false, "force a shuffle (albums play in track order by default)")
	ordered := flag.Bool("ordered", false, "play in listed order (artist/mix/playlist shuffle by default)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("hesplay", version)
		return
	}
	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	// `server` manages the saved default — it must work with no server up,
	// so it's handled before the client/probe path.
	if args[0] == "server" {
		if err := cmdServer(args[1:], *server); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	// Ctrl+C / SIGTERM stops the current track's engine process and exits.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c := newClient(resolveServer(*server))
	if err := dispatch(ctx, c, args, shuffleFor(args[0], *shuffle, *ordered)); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `hesplay — Hespera LAN music player

Usage:
  hesplay [--server URL] [--shuffle|--ordered] <command> [args]

Commands:
  album <name|id>     Play an album
  artist <name|id>    Play an artist's whole catalog in album order
  mix <name|id>       Play a radio mix seeded from an artist (+ similar artists)
  playlist <name|id>  Play a playlist
  popular             Play the catalog's most popular songs (shuffled)
  all                 Play the whole catalog (shuffled)
  playlists           List playlists
  server [url|clear]  Show, set, or clear the saved default server
  version             Print hesplay version (also --version)

Names need no quoting (hesplay album abbey road) and resolve against the
server's search — the closest match plays and is printed; a purely numeric
argument that matches no name is tried as an id. Playback engine: mpv when
installed, else ffplay (from ffmpeg).

Order: an album plays in track order; artist/mix/playlist queues shuffle by
default — --ordered plays them as listed, --shuffle forces a shuffle.

Server: --server, else $HESPERA_SERVER, else the saved default (set once with
hesplay server http://plex:8080), else http://127.0.0.1:8080.
`)
}

// resolveServer applies the --server > $HESPERA_SERVER > saved default >
// loopback-default precedence (the hescli resolveSocket shape, plus the
// `hesplay server` file tier) and normalizes the URL so "plex.local:8080"
// works without a scheme.
func resolveServer(flagVal string) string {
	s, _ := resolveServerWithSource(flagVal)
	return s
}

// resolveServerWithSource is resolveServer plus which tier answered, for
// `hesplay server` to report.
func resolveServerWithSource(flagVal string) (server, source string) {
	if s := strings.TrimSpace(flagVal); s != "" {
		return normalizeServer(s), "--server"
	}
	if s := strings.TrimSpace(os.Getenv("HESPERA_SERVER")); s != "" {
		return normalizeServer(s), "$HESPERA_SERVER"
	}
	if s := savedServer(); s != "" {
		return normalizeServer(s), "saved default"
	}
	return "http://127.0.0.1:8080", "built-in default"
}

// normalizeServer makes a bare "plex:8080" a usable base URL.
func normalizeServer(s string) string {
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return strings.TrimSuffix(s, "/")
}

// serverConfigPath is the file holding the saved default server URL — one
// line under the same per-user config dir the server's DataDir defaults to
// (os.UserConfigDir()/hespera), so it lands somewhere predictable on any OS.
func serverConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hespera", "hesplay-server"), nil
}

// savedServer reads the saved default; any failure (no file, no config dir)
// is just "no saved default".
func savedServer() string {
	p, err := serverConfigPath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// cmdServer shows, sets, or clears the saved default server. Setting probes
// the URL and reports, but saves either way — pointing at a box that happens
// to be down right now is still a valid default.
func cmdServer(args []string, flagVal string) error {
	if len(args) > 0 && isHelp(args[0]) {
		usage()
		return nil
	}
	switch {
	case len(args) == 0:
		server, source := resolveServerWithSource(flagVal)
		fmt.Printf("%s (%s)\n", server, source)
		if saved := savedServer(); saved != "" && source != "saved default" {
			fmt.Printf("saved default %s is overridden by %s\n", normalizeServer(saved), source)
		}
		return nil
	case len(args) == 1 && args[0] == "clear":
		p, err := serverConfigPath()
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil {
			if os.IsNotExist(err) {
				fmt.Println("no saved default")
				return nil
			}
			return err
		}
		fmt.Println("saved default cleared")
		return nil
	case len(args) == 1:
		s := normalizeServer(strings.TrimSpace(args[0]))
		p, err := serverConfigPath()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(s+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Println("saved default:", s)
		if ver, err := newClient(s).probe(); err == nil {
			fmt.Printf("Hespera %s answered\n", ver)
		} else {
			fmt.Fprintf(os.Stderr, "warn: not answering right now (%v)\n", err)
		}
		return nil
	default:
		return fmt.Errorf("server: expected one URL (got %d arguments) — a URL has no spaces", len(args))
	}
}

// shuffleFor resolves the play order: an album is a sequenced work and plays
// in track order; everything else (artist catalog, mix, playlist) shuffles by
// default. --shuffle and --ordered force either way (--shuffle wins if both).
func shuffleFor(verb string, shuffleFlag, orderedFlag bool) bool {
	if shuffleFlag {
		return true
	}
	if orderedFlag {
		return false
	}
	return verb != "album"
}

// isHelp reports whether an argument is a help request in any accepted form.
func isHelp(s string) bool {
	switch s {
	case "-h", "--help", "help", "?":
		return true
	}
	return false
}

func dispatch(ctx context.Context, c *client, args []string, shuffle bool) error {
	if isHelp(args[0]) || (len(args) >= 2 && isHelp(args[1])) {
		usage()
		return nil
	}
	if args[0] == "version" {
		fmt.Println("hesplay", version)
		return nil
	}

	// Everything else talks to the server — verify it's really Hespera first.
	serverVer, err := c.probe()
	if err != nil {
		return err
	}

	switch args[0] {
	case "playlists":
		rows, err := c.fetchPlaylists()
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Println("no playlists")
			return nil
		}
		tw := newTable("ID", "NAME", "TRACKS")
		for _, p := range rows {
			fmt.Fprintf(tw, "%d\t%s\t%d\n", p.ID, p.Name, p.Count)
		}
		return tw.Flush()
	case "album", "artist", "mix", "playlist", "popular", "all":
		name := strings.TrimSpace(strings.Join(args[1:], " "))
		if name == "" && args[0] != "popular" && args[0] != "all" {
			return fmt.Errorf("%s: expected a name or id", args[0])
		}
		eng, err := findEngine()
		if err != nil {
			return err
		}
		query, picked, err := c.resolveQueueQuery(args[0], name)
		if err != nil {
			return err
		}
		if picked != "" {
			fmt.Println("Matched:", picked)
		}
		q, err := c.fetchQueue(query)
		if err != nil {
			return err
		}
		fmt.Printf("Hespera %s at %s\n", serverVer, c.base)
		return play(ctx, c, eng, q, shuffle)
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// resolveQueueQuery turns a verb + name/id into the /music/queue params,
// resolving names to ids server-side (search for artists/albums, the playlist
// list for playlists). picked names what a fuzzy match chose, for printing.
func (c *client) resolveQueueQuery(verb, name string) (query url.Values, picked string, err error) {
	switch verb {
	case "popular", "all": // the web home's Quick Play queues — no name to resolve
		return url.Values{"source": {verb}}, "", nil
	case "album":
		id, picked, err := c.resolveSearch("Albums", "/music/album/", name)
		if err != nil {
			return nil, "", err
		}
		return url.Values{"album": {strconv.FormatInt(id, 10)}}, picked, nil
	case "artist", "mix":
		id, picked, err := c.resolveSearch("Artists", "/music/artist/", name)
		if err != nil {
			return nil, "", err
		}
		return url.Values{"source": {verb}, "artist": {strconv.FormatInt(id, 10)}}, picked, nil
	default: // playlist
		rows, err := c.fetchPlaylists()
		if err != nil {
			return nil, "", err
		}
		id, picked, err := resolvePlaylist(rows, name)
		if err != nil {
			return nil, "", err
		}
		return url.Values{"source": {"playlist"}, "playlist": {strconv.FormatInt(id, 10)}}, picked, nil
	}
}

// resolvePlaylist matches a playlist by name — exact (case-insensitive) first,
// else a unique substring match, else a numeric argument is taken as an id.
// Several substring matches are an error naming the candidates, not a guess.
func resolvePlaylist(rows []playlistRow, name string) (int64, string, error) {
	var subs []playlistRow
	for _, p := range rows {
		if strings.EqualFold(p.Name, name) {
			return p.ID, p.Name, nil
		}
		if strings.Contains(strings.ToLower(p.Name), strings.ToLower(name)) {
			subs = append(subs, p)
		}
	}
	if len(subs) == 1 {
		return subs[0].ID, subs[0].Name, nil
	}
	if id, perr := strconv.ParseInt(name, 10, 64); perr == nil && id > 0 {
		return id, "", nil
	}
	if len(subs) > 1 {
		names := make([]string, len(subs))
		for i, p := range subs {
			names[i] = p.Name
		}
		return 0, "", fmt.Errorf("%q matches several playlists: %s", name, strings.Join(names, ", "))
	}
	return 0, "", fmt.Errorf("no playlist matching %q", name)
}

// --- playback engine (mpv preferred, ffplay fallback) ---

type engine struct{ name, path string }

// findEngine picks the local playback engine — the internal/browser LookPath
// hunt shape. mpv first (best headless behavior); ffplay rides the ffmpeg the
// Hespera .deb already depends on.
func findEngine() (engine, error) {
	for _, name := range []string{"mpv", "ffplay"} {
		if p, err := exec.LookPath(name); err == nil {
			return engine{name: name, path: p}, nil
		}
	}
	return engine{}, errors.New("no playback engine: install mpv (recommended) or ffmpeg (for ffplay)")
}

// args builds the engine invocation for one track: audio-only, quiet, with the
// queue's leveling gain applied as a filter (0 dB = unity, so an unanalyzed
// track passes through untouched). mpv goes through its lavfi bridge — stable
// across mpv versions, same ffmpeg volume syntax as ffplay. A non-empty
// ipcPath adds mpv's JSON IPC socket, which the stall guard below polls.
func (e engine) args(streamURL string, gainDB float64, ipcPath string) []string {
	af := fmt.Sprintf("volume=%.2fdB", gainDB)
	if e.name == "mpv" {
		a := []string{"--no-video", "--really-quiet", "--af=lavfi=[" + af + "]"}
		if ipcPath != "" {
			a = append(a, "--input-ipc-server="+ipcPath)
		}
		return append(a, streamURL)
	}
	return []string{"-nodisp", "-autoexit", "-loglevel", "error", "-af", af, streamURL}
}

// --- stall guard ---
//
// One process per track means a wedged engine takes the whole queue down with
// it: hesplay waits on the child, so a process that neither plays nor exits
// freezes playback with no error, no skip, and nothing on screen. Seen live on
// the Pi — mpv blocked forever inside its pipewire audio-output init (probing
// a stack that box doesn't run), holding no audio device, and deaf to SIGTERM.
// The engine's own position is the only honest progress signal, so mpv is
// given an IPC socket and polled; a position that stops advancing — or never
// starts — is a wedge.
const (
	stallPoll    = 3 * time.Second
	stallTimeout = 20 * time.Second
)

// stallSocket is the per-track IPC socket path, or "" when the guard can't run:
// ffplay has no IPC at all, and mpv on Windows speaks named pipes, which need
// more than the stdlib to dial. Those engines play unguarded, exactly as before.
func stallSocket(e engine, trackID int64) string {
	if e.name != "mpv" || runtime.GOOS == "windows" {
		return ""
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("hesplay-%d-%d.sock", os.Getpid(), trackID))
}

// stallTracker turns a series of position samples into a stalled/not verdict.
// A sample that couldn't be read (socket not up yet, or time-pos unavailable
// because playback never started) counts as no progress — that is precisely
// what a wedge before the first frame looks like.
type stallTracker struct {
	pos      float64
	lastMove time.Time
}

func newStallTracker(now time.Time) *stallTracker {
	return &stallTracker{pos: -1, lastMove: now}
}

func (s *stallTracker) observe(pos float64, ok bool, now time.Time) bool {
	if ok && pos > s.pos {
		s.pos, s.lastMove = pos, now
		return false
	}
	return now.Sub(s.lastMove) > stallTimeout
}

// watchStall polls the engine socket until done is closed and kills a wedged
// process, recording the fact in killed so the play loop can tell a stall from
// an ordinary engine failure. SIGKILL, not SIGTERM: a wedged mpv ignores the
// polite signal (measured — still alive three and a half minutes after one).
func watchStall(done <-chan struct{}, proc *os.Process, sock string, killed *atomic.Bool) {
	tick := time.NewTicker(stallPoll)
	defer tick.Stop()
	st := newStallTracker(time.Now())
	for {
		select {
		case <-done:
			return
		case now := <-tick.C:
			pos, ok := ipcTimePos(sock)
			if !st.observe(pos, ok, now) {
				continue
			}
			killed.Store(true)
			_ = proc.Kill()
			return
		}
	}
}

// ipcTimePos asks mpv for the current playback position over its JSON IPC
// socket. Every failure mode — socket absent, no reply, property unavailable —
// reports not-ok rather than an error, since the tracker treats them all the
// same way: no progress.
func ipcTimePos(sock string) (float64, bool) {
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return 0, false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, `{"command":["get_property","time-pos"]}`+"\n"); err != nil {
		return 0, false
	}
	dec := json.NewDecoder(conn)
	for {
		var msg struct {
			Data  *float64 `json:"data"`
			Error string   `json:"error"`
			Event string   `json:"event"`
		}
		if err := dec.Decode(&msg); err != nil {
			return 0, false
		}
		if msg.Event != "" {
			continue // mpv pushes events down the same socket; skip to the reply
		}
		if msg.Error != "success" || msg.Data == nil {
			return 0, false
		}
		return *msg.Data, true
	}
}

// play runs the queue through the engine, one process per track — clean
// boundaries for play-event reporting; the sub-second gap between tracks is
// the accepted trade for that simplicity. A run of instant failures aborts
// rather than machine-gunning through a dead server's whole queue.
func play(ctx context.Context, c *client, e engine, q queue, shuffle bool) error {
	tracks := append([]queueTrack(nil), q.Tracks...)
	if shuffle {
		rand.Shuffle(len(tracks), func(i, j int) { tracks[i], tracks[j] = tracks[j], tracks[i] })
	}
	fmt.Printf("Playing %q — %d tracks via %s\n", q.Title, len(tracks), e.name)

	quickFails := 0
	for i, t := range tracks {
		if ctx.Err() != nil {
			return nil
		}
		fmt.Printf("♪ %d/%d  %s — %s\n", i+1, len(tracks), t.Title, t.Artist)
		sock := stallSocket(e, t.ID)
		start := time.Now()
		cmd := exec.CommandContext(ctx, e.path, e.args(c.base+"/stream/track/"+strconv.FormatInt(t.ID, 10), t.GainDB, sock)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

		var stalled atomic.Bool
		runErr := cmd.Start()
		if runErr == nil {
			done := make(chan struct{})
			if sock != "" {
				go watchStall(done, cmd.Process, sock, &stalled)
			}
			runErr = cmd.Wait()
			close(done)
			if sock != "" {
				os.Remove(sock) // mpv unlinks its own on a clean exit; a killed one doesn't
			}
		}
		played := time.Since(start)

		// A stalled track was never heard, so it is never reported: a wedged
		// engine must not land in Recently Played or bump a listen count. It
		// doesn't feed the instant-failure guard either — the queue is moving,
		// one engine just had to be put down.
		if stalled.Load() {
			quickFails = 0
			fmt.Fprintf(os.Stderr, "warn: %s stalled on %q (no playback progress for %s), killed and skipping\n", e.name, t.Title, stallTimeout)
			continue
		}
		c.reportPlay(t.ID, played, runErr == nil && ctx.Err() == nil)

		switch {
		case ctx.Err() != nil:
			fmt.Println("stopped")
			return nil
		case runErr != nil && played < 2*time.Second:
			quickFails++
			if quickFails >= 3 {
				return fmt.Errorf("%s keeps failing instantly (%v) — is the server still reachable?", e.name, runErr)
			}
			fmt.Fprintf(os.Stderr, "warn: %s failed on %q (%v), skipping\n", e.name, t.Title, runErr)
		case runErr != nil:
			quickFails = 0
			fmt.Fprintf(os.Stderr, "warn: %s exited early on %q (%v)\n", e.name, t.Title, runErr)
		default:
			quickFails = 0
		}
	}
	return nil
}

// --- HTTP client against the LAN server ---

type client struct {
	http *http.Client
	base string // normalized server URL, no trailing slash
}

func newClient(base string) *client {
	// JSON calls only — the audio stream is fetched by the engine, not here.
	return &client{base: base, http: &http.Client{Timeout: 15 * time.Second}}
}

// probe verifies a live Hespera answers at base — status 200, body "ok", AND
// the X-Hespera version header, so a stranger on a reused port is never
// mistaken for the server (the desktop attach-probe idiom). Returns the
// server's version.
func (c *client) probe() (string, error) {
	resp, err := c.http.Get(c.base + "/healthz")
	if err != nil {
		return "", fmt.Errorf("cannot reach Hespera at %s (%v) — point --server or $HESPERA_SERVER at it (e.g. http://plex.local:8080)", c.base, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	ver := resp.Header.Get("X-Hespera")
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" || ver == "" {
		return "", fmt.Errorf("%s answers but is not Hespera", c.base)
	}
	return ver, nil
}

// getJSON issues a GET and decodes a 200 JSON body into out.
func (c *client) getJSON(path string, query url.Values, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	resp, err := c.http.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("%s: %s", strings.TrimPrefix(path, "/"), msg)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("bad response from server: %w", err)
	}
	return nil
}

// queueTrack mirrors the fields of the server's queue JSON this player uses
// (unknown fields — albumId, artistId — are ignored by encoding/json).
type queueTrack struct {
	ID     int64   `json:"id"`
	Title  string  `json:"title"`
	Artist string  `json:"artist"`
	Album  string  `json:"album"`
	GainDB float64 `json:"gainDb"`
}

type queue struct {
	Title  string       `json:"title"`
	Tracks []queueTrack `json:"tracks"`
}

func (c *client) fetchQueue(query url.Values) (queue, error) {
	var q queue
	if err := c.getJSON("/music/queue", query, &q); err != nil {
		return q, err
	}
	if len(q.Tracks) == 0 {
		return q, fmt.Errorf("nothing to play in %q", q.Title)
	}
	return q, nil
}

type playlistRow struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func (c *client) fetchPlaylists() ([]playlistRow, error) {
	var resp struct {
		Playlists []playlistRow `json:"playlists"`
	}
	err := c.getJSON("/music/playlists", nil, &resp)
	return resp.Playlists, err
}

type searchRow struct {
	Href    string `json:"href"`
	Text    string `json:"text"`
	Context string `json:"context"`
}

// resolveSearch resolves a name to an id via the server's search palette: rows
// carry their id only inside the href (/music/artist/{id}, /music/album/{id}),
// so it's parsed off the given prefix. An exact (case-insensitive) title match
// wins, else the first row — search already ranks prefix matches first. picked
// names the choice so the caller can print it; a purely numeric argument that
// matches no name is taken as an id.
func (c *client) resolveSearch(section, hrefPrefix, name string) (id int64, picked string, err error) {
	var res struct {
		Sections []struct {
			Label string      `json:"label"`
			Rows  []searchRow `json:"rows"`
		} `json:"sections"`
	}
	if err := c.getJSON("/search", url.Values{"q": {name}}, &res); err != nil {
		return 0, "", err
	}
	var pick searchRow
	for _, s := range res.Sections {
		if s.Label != section {
			continue
		}
		for _, r := range s.Rows {
			if !strings.HasPrefix(r.Href, hrefPrefix) {
				continue
			}
			if pick.Href == "" {
				pick = r
			}
			if strings.EqualFold(r.Text, name) {
				pick = r
				break
			}
		}
		break
	}
	if pick.Href == "" {
		if id, perr := strconv.ParseInt(name, 10, 64); perr == nil && id > 0 {
			return id, "", nil
		}
		return 0, "", fmt.Errorf("no %s matching %q", strings.ToLower(strings.TrimSuffix(section, "s")), name)
	}
	id, err = strconv.ParseInt(strings.TrimPrefix(pick.Href, hrefPrefix), 10, 64)
	if err != nil || id <= 0 {
		return 0, "", fmt.Errorf("cannot parse an id from %q", pick.Href)
	}
	picked = pick.Text
	if pick.Context != "" {
		picked += " (" + pick.Context + ")"
	}
	return id, picked, nil
}

// reportPlay feeds play_history (Recently Played, listen counts) — best-effort
// with its own short deadline so a dead server can't stall shutdown; the server
// ignores sub-15s incomplete listens. Sent with no Origin header, which the
// same-origin guard admits (a forged cross-site fetch cannot omit one).
func (c *client) reportPlay(trackID int64, played time.Duration, completed bool) {
	body, _ := json.Marshal(map[string]any{
		"track_id":  trackID,
		"played_ms": played.Milliseconds(),
		"completed": completed,
		"source":    "hesplay",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/music/play-event", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := c.http.Do(req); err == nil {
		resp.Body.Close()
	}
}

func newTable(headers ...string) *tabwriter.Writer {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	return tw
}

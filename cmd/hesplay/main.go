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
	listen := flag.String("listen", "", "serve the phone remote on this address (e.g. :8090); empty = off")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("hesplay", version)
		return
	}
	args := flag.Args()
	// --listen alone is a complete command: serve the remote and wait for it to
	// choose something. A verb may still ride along to start playing at once.
	if len(args) == 0 && strings.TrimSpace(*listen) == "" {
		usage()
		os.Exit(2)
	}

	// `server` manages the saved default — it must work with no server up,
	// so it's handled before the client/probe path.
	if len(args) > 0 && args[0] == "server" {
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

	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	if addr := strings.TrimSpace(*listen); addr != "" {
		if err := serveControl(ctx, c, addr, args, shuffleFor(verb, *shuffle, *ordered)); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	if err := dispatch(ctx, c, args, shuffleFor(verb, *shuffle, *ordered)); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `hesplay — Hespera LAN music player

Usage:
  hesplay [--server URL] [--shuffle|--ordered] <command> [args]
  hesplay --listen :8090 [command [args]]      Serve the phone remote

Commands:
  album <name|id>      Play an album
  artist <name|id>     Play an artist's whole catalog in album order
  song <name|id>       Play a single song
  mix <name|id>        Play a radio mix seeded from an artist (+ similar artists)
  playlist <name|id>   Play a playlist
  popular              Play the catalog's most popular songs (shuffled)
  all                  Play the whole catalog (shuffled)
  playlists            List playlists
  server [url|clear]   Show, set, or clear the saved default server
  completion <bash|zsh> Print a shell completion script
  version              Print hesplay version (also --version)

Names need no quoting (hesplay album abbey road) and resolve against the
server's search — the closest match plays and is printed; a purely numeric
argument that matches no name is tried as an id. Playback engine: mpv when
installed, else ffplay (from ffmpeg).

Order: an album plays in track order and a song is one track; artist/mix/playlist
queues shuffle by default — --ordered plays them as listed, --shuffle forces a
shuffle.

Server: --server, else $HESPERA_SERVER, else the saved default (set once with
hesplay server http://plex:8080), else http://127.0.0.1:8080.

Keys while playing (needs a terminal): n next, p previous — or restart, once
more than 10s into a track — and q to quit. Ctrl+C still stops.

Phone remote: hesplay --listen :8090 serves a small installable web app on this
box — open http://<this-box>:8090 on your phone and Add to Home Screen. Music
still plays from THIS box's speakers; the phone only sends the buttons, so
locking it stops nothing. The remote has one setting, the Hespera server. It has
no authentication, the same posture as Hespera itself — anyone who can reach the
port can change what is playing here.

Completion: the .deb wires up bash automatically; otherwise source it, e.g.
source <(hesplay completion bash). Verbs complete offline; album/artist/song/mix
names come live from the server once two characters are typed (playlists need
none), so hesplay artist Black<Tab> lists the matching artists.
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

// saveServer writes the saved default. The single writer of that file — the
// `server` verb and the phone remote's PUT /api/server both go through it, so
// they cannot disagree about the path or the format.
func saveServer(s string) error {
	p, err := serverConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(s+"\n"), 0o644)
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
		if err := saveServer(s); err != nil {
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

// shuffleFor resolves the play order: an album is a sequenced work and plays in
// track order, and a single song has no order to shuffle; everything else
// (artist catalog, mix, playlist) shuffles by default. --shuffle and --ordered
// force either way (--shuffle wins if both).
func shuffleFor(verb string, shuffleFlag, orderedFlag bool) bool {
	if shuffleFlag {
		return true
	}
	if orderedFlag {
		return false
	}
	return verb != "album" && verb != "song"
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
	if args[0] == "completion" {
		return completionCmd(args[1:])
	}
	// `complete` backs the completion script above: it answers a keypress, so it
	// skips the probe below (one round trip, not two) and stays silent on every
	// failure — a shell offering no candidates is normal, an error printed over
	// the prompt is not.
	if args[0] == "complete" {
		completeCmd(c, args[1:])
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
	case "album", "artist", "song", "mix", "playlist", "popular", "all":
		name := strings.TrimSpace(strings.Join(args[1:], " "))
		if name == "" && args[0] != "popular" && args[0] != "all" {
			return fmt.Errorf("%s: expected a name or id", args[0])
		}
		eng, err := findEngine()
		if err != nil {
			return err
		}
		query, picked, err := c.resolveQueueQuery(args[0], name, shuffle)
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
// shuffle rides along as &shuffle=1 — the same flag the web player sends. The
// server answers a shuffled catalog sweep with one recording per song, dropping
// the live or compilation copy of a track already in the queue, so the local
// shuffle below can't serve the same song twice.
func (c *client) resolveQueueQuery(verb, name string, shuffle bool) (query url.Values, picked string, err error) {
	var id int64
	switch verb {
	case "popular", "all": // the web home's Quick Play queues — no name to resolve
	case "album":
		id, picked, err = c.resolveSearch("Albums", "/music/album/", name)
	case "artist", "mix":
		id, picked, err = c.resolveSearch("Artists", "/music/artist/", name)
	case "song":
		id, picked, err = c.resolveSong(name)
	default: // playlist
		var rows []playlistRow
		if rows, err = c.fetchPlaylists(); err == nil {
			id, picked, err = resolvePlaylist(rows, name)
		}
	}
	if err != nil {
		return nil, "", err
	}
	return queueParams(verb, id, shuffle), picked, nil
}

// queueParams is the single owner of the /music/queue parameter shape. Both the
// name-resolving path above and the remote's by-id path build queries through
// it, so a verb can never mean two different things depending on which caller
// assembled it.
func queueParams(verb string, id int64, shuffle bool) url.Values {
	sid := strconv.FormatInt(id, 10)
	var query url.Values
	switch verb {
	case "popular", "all":
		query = url.Values{"source": {verb}}
	case "album":
		// An album is addressed by a bare album= with no source: the server's
		// default branch. Sending source=album would fall through to the same
		// place, but this matches what the web player emits.
		query = url.Values{"album": {sid}}
	case "artist", "mix":
		query = url.Values{"source": {verb}, "artist": {sid}}
	case "song":
		query = url.Values{"source": {"track"}, "track": {sid}}
	default: // playlist
		query = url.Values{"source": {"playlist"}, "playlist": {sid}}
	}
	if shuffle {
		query.Set("shuffle", "1")
	}
	return query
}

// knownSource reports whether verb names a queue the server can build. The CLI
// gets this for free from its own switch; the remote takes the verb off the
// wire, so an unknown one must be refused rather than silently defaulting to
// the playlist branch.
func knownSource(verb string) bool {
	switch verb {
	case "popular", "all", "album", "artist", "mix", "song", "playlist":
		return true
	}
	return false
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

// --- shell completion ---
//
// Two pieces: `completion <shell>` prints the script (the hescli shape — the
// .deb installs a one-line stub that sources it from the binary, so it can't
// drift from the CLI), and the hidden `complete` verb the script calls back
// into for live names. Verbs and flags complete offline; a name means asking
// the server, which is why the callback exists at all.

const (
	// completeTimeout is short on purpose: completion runs on a Tab keypress,
	// and a shell that hangs for the 15s the player uses is worse than one that
	// offers nothing. Long enough for a LAN round trip, short enough to feel
	// like a miss.
	completeTimeout = 1500 * time.Millisecond
	// completeLimit is how many candidates to ask for per section. Five (the
	// server's palette default) truncates a real prefix — "Black" alone can
	// name half a dozen artists — while a screenful is plenty to choose from.
	completeLimit = 25
	// completeMinChars mirrors the server's own minimum: /search answers a
	// shorter query with nothing, so there's no point spending a round trip.
	completeMinChars = 2
)

// completeSections maps each name-taking verb to the search section its names
// live in. `mix` is seeded by an artist, so it completes artists; `playlist` is
// absent because playlists come from their own endpoint, not search.
var completeSections = map[string]string{
	"album":  "Albums",
	"artist": "Artists",
	"song":   "Songs",
	"mix":    "Artists",
}

func completionCmd(args []string) error {
	shell := "bash"
	if len(args) > 0 {
		shell = args[0]
	}
	switch shell {
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		// Reuse the bash completion under zsh's bash-compat layer.
		fmt.Print("autoload -Uz bashcompinit && bashcompinit\n", bashCompletion)
	default:
		return fmt.Errorf("completion: unsupported shell %q (want bash or zsh)", shell)
	}
	return nil
}

// completeCmd prints one candidate per line for a verb and the partial name
// typed so far — the callback the completion script shells out to. Every
// failure (no server, no match, an unknown verb) prints nothing: the shell
// falls back to its default completion, which is the right answer for "I can't
// help here".
func completeCmd(c *client, args []string) {
	if len(args) == 0 {
		return
	}
	verb := args[0]
	partial := strings.TrimSpace(strings.Join(args[1:], " "))
	c.http.Timeout = completeTimeout

	var names []string
	if verb == "playlist" {
		// Playlists come from a plain list, so they complete from nothing typed
		// — `hesplay playlist <Tab>` shows them all.
		rows, err := c.fetchPlaylists()
		if err != nil {
			return
		}
		lower := strings.ToLower(partial)
		for _, p := range rows {
			if strings.Contains(strings.ToLower(p.Name), lower) {
				names = append(names, p.Name)
			}
		}
	} else {
		section, ok := completeSections[verb]
		if !ok || len([]rune(partial)) < completeMinChars {
			return
		}
		rows, err := c.fetchSearchSection(section, partial, completeLimit)
		if err != nil {
			return
		}
		for _, r := range rows {
			names = append(names, r.Text)
		}
	}
	for _, n := range completionRemainders(names, partial) {
		fmt.Println(n)
	}
}

// completionRemainders reduces each candidate to the text that should replace
// the word being completed. The shell completes one word at a time, so for the
// partial "Black Sab" the candidate "Black Sabbath" has to come back as
// "Sabbath" — the words already typed and closed off by a space are dropped,
// and a candidate that doesn't share them can't complete this word at all (the
// server matched it on some later word), so it goes. With no space in the
// partial the whole candidate is returned, spaces and all: the shell inserts
// them as separate words, which is exactly how the play verbs read a name —
// they join argv back together, so no quoting is ever needed.
func completionRemainders(names []string, partial string) []string {
	cut := strings.LastIndex(partial, " ")
	if cut < 0 {
		return names
	}
	prefix := partial[:cut+1] // the closed-off words, trailing space included
	out := make([]string, 0, len(names))
	for _, n := range names {
		if len(n) > len(prefix) && strings.EqualFold(n[:len(prefix)], prefix) {
			out = append(out, n[len(prefix):])
		}
	}
	return out
}

// bashCompletion completes verbs and flags offline, and hands name completion
// to `hesplay complete` (above). Written to work unmodified under zsh's
// bashcompinit, which is why candidates arrive by command substitution — zsh
// word-splits that, but not a plain parameter expansion.
const bashCompletion = `# hesplay bash completion
_hesplay() {
  local cur cmd vidx i
  cur="${COMP_WORDS[COMP_CWORD]}"
  local cmds="album artist song mix playlist popular all playlists server completion version help"

  # The verb is the first non-flag word after the program name.
  cmd=""; vidx=0
  for ((i=1; i<COMP_CWORD; i++)); do
    case "${COMP_WORDS[i]}" in
      --server) ((i++));;
      --*|-h) ;;
      *) cmd="${COMP_WORDS[i]}"; vidx=$i; break;;
    esac
  done

  if [[ -z "$cmd" ]]; then
    COMPREPLY=( $(compgen -W "$cmds --server --shuffle --ordered --version" -- "$cur") ); return
  fi

  local partial
  case "$cmd" in
    completion)
      COMPREPLY=( $(compgen -W "bash zsh" -- "$cur") ); return;;
    server)
      COMPREPLY=( $(compgen -W "clear" -- "$cur") ); return;;
    album|artist|song|mix|playlist)
      # Everything after the verb up to the cursor is the partial name; names
      # need no quoting, so they arrive as separate words and rejoin here.
      partial="${COMP_WORDS[*]:$((vidx+1)):$((COMP_CWORD-vidx))}";;
    *)
      COMPREPLY=(); return;;
  esac

  # One candidate per line, already trimmed to what should replace this word.
  local IFS=$'\n'
  set -o noglob
  COMPREPLY=( $(hesplay complete "$cmd" "$partial" 2>/dev/null) )
  set +o noglob
}
complete -F _hesplay hesplay
`

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
//
// Nothing extra is needed for the transport keys: the engine is started without
// stdin while hesplay owns the keyboard, and mpv was verified not to disturb the
// cbreak mode hesplay installs (keys act immediately with mpv running, measured
// in a pty).
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
func watchStall(done <-chan struct{}, proc *os.Process, sock string, killed *atomic.Bool, paused *atomic.Bool) {
	tick := time.NewTicker(stallPoll)
	defer tick.Stop()
	st := newStallTracker(time.Now())
	for {
		select {
		case <-done:
			return
		case now := <-tick.C:
			// A paused track's time-pos does not move, which is exactly what a
			// wedge looks like — so without this the guard would SIGKILL the
			// engine 20s into any pause. Reset the window each tick instead, so
			// the stall clock starts fresh from the moment playback resumes.
			if paused != nil && paused.Load() {
				st = newStallTracker(now)
				continue
			}
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

// ipcSetPause pauses or resumes the running mpv over the same JSON IPC socket
// the stall guard already uses. Fire-and-forget on the reply: mpv applies the
// property before it answers, and a missing reply must not read as a failure to
// pause. ffplay has no IPC at all, so this is reachable only when stallSocket
// gave a path (mpv, non-Windows).
func ipcSetPause(sock string, paused bool) error {
	if sock == "" {
		return errors.New("this engine cannot pause")
	}
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return fmt.Errorf("the player is not answering: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	cmd := fmt.Sprintf(`{"command":["set_property","pause",%t]}`+"\n", paused)
	if _, err := io.WriteString(conn, cmd); err != nil {
		return fmt.Errorf("the player is not answering: %w", err)
	}
	return nil
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

// --- transport keys ---
//
// One process per track and a blocking cmd.Wait means the queue can only ever
// move forward, at the engine's pace. Skip and previous need hesplay to be
// watching the keyboard while a track plays, so it reads stdin itself (cbreak,
// so Ctrl+C still signals) and kills the engine when a key says to move.

// prevRestartWindow is how deep into a track [p] restarts it instead of
// stepping back — the same idiom and the same 10s as the two web players'
// PREV_RESTART_SECS (player.js, media_player.js). Three separate
// implementations by now, but the behaviour a listener learns is one.
const prevRestartWindow = 10 * time.Second

type playAction int

const (
	actionNone playAction = iota // the track ended on its own
	actionNext
	actionPrev
	actionQuit
)

// watchKeys puts the terminal in cbreak and reads single keys until ctx ends,
// returning the action channel and a restore func. A nil restore means there is
// no terminal (piped stdin, a systemd unit, cron) and no keys will ever arrive —
// the channel stays empty, so callers need no second check.
func watchKeys(ctx context.Context) (<-chan playAction, func()) {
	restore := enterCbreak()
	if restore == nil {
		return nil, nil
	}
	// Buffered: a keypress arriving between tracks must not block the reader
	// goroutine, and the loop drains it on the next track anyway.
	ch := make(chan playAction, 1)
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			if n == 0 {
				continue
			}
			act := actionNone
			switch buf[0] {
			case 'n', 'N', '.', '>':
				act = actionNext
			case 'p', 'P', ',', '<':
				act = actionPrev
			case 'q', 'Q':
				act = actionQuit
			}
			if act == actionNone {
				continue
			}
			select {
			case ch <- act:
			case <-ctx.Done():
				return
			default: // an action is already pending; the newest press is noise
			}
		}
	}()
	return ch, restore
}

// awaitTrack waits for whichever comes first: the engine finishing, or a key
// asking to move. On a key it kills the engine and reaps it, so the caller
// never leaves a process behind.
func awaitTrack(ctx context.Context, cmd *exec.Cmd, keys <-chan playAction) (playAction, error) {
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case err := <-waitCh:
		return actionNone, err
	case act := <-keys:
		_ = cmd.Process.Kill()
		<-waitCh // reap; the error is the kill we just issued, so it's discarded
		return act, nil
	case <-ctx.Done():
		<-waitCh // CommandContext kills it for us; just don't return before it's reaped
		return actionNone, ctx.Err()
	}
}

// nextIndex applies a transport action to the queue position. [p] mirrors the
// players' restart-or-previous idiom: deep into a track it restarts that track,
// early on it steps back, and on the first track it can only restart.
func nextIndex(i int, act playAction, played time.Duration) int {
	if act == actionPrev && played <= prevRestartWindow && i > 0 {
		return i - 1
	}
	if act == actionPrev {
		return i // restart the current track
	}
	return i + 1
}

// play runs the queue through the engine with the terminal as its action
// source — the CLI path, unchanged in behaviour.
func play(ctx context.Context, c *client, e engine, q queue, shuffle bool) error {
	// Transport keys, when there's a terminal to read them from. Stdin can only
	// have one reader: with keys live it belongs to hesplay, so the engine is
	// started without it; without a terminal it goes to the engine exactly as
	// before, leaving mpv's own keybindings intact.
	keys, restore := watchKeys(ctx)
	if restore != nil {
		defer restore()
		fmt.Println("keys: [n] next  [p] previous/restart  [q] quit")
	}
	return playQueue(ctx, c, e, q, shuffle, playOpts{actions: keys, giveStdin: restore == nil})
}

// playOpts is what a queue run needs beyond the queue itself: where transport
// actions come from, whether the engine inherits stdin, and where to publish
// now-playing. It exists so the control server (--listen) can drive the very
// same loop from HTTP instead of from keypresses — the action channel was
// always the seam, this just names it.
type playOpts struct {
	actions   <-chan playAction
	giveStdin bool
	onState   func(nowPlaying)   // nil on the CLI path
	onQueue   func([]queueTrack) // the final, post-shuffle order; called once
	// startAt is the 0-based track to begin on. It exists so the remote can
	// jump to a tapped row by replaying the SAME list from there, which keeps
	// playAction a plain enum: a jump is a queue that starts further in, not a
	// new kind of transport event carrying a payload.
	startAt int
	// paused is shared with the control server: it flips the engine's pause
	// state AND tells the stall guard to hold its fire. nil on the CLI path,
	// which has no pause.
	paused *atomic.Bool
}

// playQueue runs the queue through the engine, one process per track — clean
// boundaries for play-event reporting; the sub-second gap between tracks is
// the accepted trade for that simplicity. A run of instant failures aborts
// rather than machine-gunning through a dead server's whole queue.
func playQueue(ctx context.Context, c *client, e engine, q queue, shuffle bool, opts playOpts) error {
	tracks := append([]queueTrack(nil), q.Tracks...)
	if shuffle {
		rand.Shuffle(len(tracks), func(i, j int) { tracks[i], tracks[j] = tracks[j], tracks[i] })
	}
	// "1 tracks" was a rare wart before the song verb; now it's the common case.
	unit := "tracks"
	if len(tracks) == 1 {
		unit = "track"
	}
	fmt.Printf("Playing %q — %d %s via %s\n", q.Title, len(tracks), unit, e.name)

	keys := opts.actions
	publish := func(np nowPlaying) {
		if opts.onState != nil {
			opts.onState(np)
		}
	}
	defer publish(nowPlaying{}) // zero value = idle, whichever way the loop ends
	// Published once, AFTER the shuffle: the remote lists what will actually
	// play, in the order it will play, not the order the server sent.
	if opts.onQueue != nil {
		opts.onQueue(tracks)
		defer opts.onQueue(nil)
	}

	quickFails := 0
	start := opts.startAt
	if start < 0 || start >= len(tracks) {
		start = 0
	}
	for i := start; i < len(tracks); {
		if ctx.Err() != nil {
			return nil
		}
		t := tracks[i]
		// Every track is a new engine process, so it starts unpaused whatever
		// the last one was doing.
		if opts.paused != nil {
			opts.paused.Store(false)
		}
		fmt.Printf("♪ %d/%d  %s — %s\n", i+1, len(tracks), t.Title, t.Artist)
		publish(nowPlaying{
			Queue: q.Title, Index: i + 1, Total: len(tracks),
			ID: t.ID, Title: t.Title, Artist: t.Artist, Album: t.Album, AlbumID: t.AlbumID,
		})
		sock := stallSocket(e, t.ID)
		start := time.Now()
		cmd := exec.CommandContext(ctx, e.path, e.args(c.base+"/stream/track/"+strconv.FormatInt(t.ID, 10), t.GainDB, sock)...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if opts.giveStdin {
			cmd.Stdin = os.Stdin
		}

		var stalled atomic.Bool
		act := actionNone
		runErr := cmd.Start()
		if runErr == nil {
			done := make(chan struct{})
			if sock != "" {
				go watchStall(done, cmd.Process, sock, &stalled, opts.paused)
			}
			act, runErr = awaitTrack(ctx, cmd, keys)
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
			i++
			continue
		}

		// A keypress ended this track, so the engine's non-zero exit is the kill
		// we asked for, not a failure: report the partial listen (the server
		// ignores anything under 15s) but never let it feed the instant-failure
		// guard, which exists to catch a dead server, not an impatient listener.
		if act != actionNone {
			c.reportPlay(t.ID, played, false)
			quickFails = 0
			if act == actionQuit {
				fmt.Println("stopped")
				return nil
			}
			i = nextIndex(i, act, played)
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
		i++
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

// httpError is a non-200 answer from the server, carrying the status code so a
// caller can react to a specific one (see fetchQueue's too-old-server hint)
// without matching on message text.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

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
		return &httpError{status: resp.StatusCode, msg: fmt.Sprintf("%s: %s", strings.TrimPrefix(path, "/"), msg)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("bad response from server: %w", err)
	}
	return nil
}

// queueTrack mirrors the fields of the server's queue JSON this player uses
// (unknown fields — backUrl — are ignored by encoding/json). albumId lets the
// phone remote build /art/album/{id} and group an artist's tracks into albums;
// artistId is what the A-Z browse index is keyed on. The queue JSON has no
// artwork URL and no duration of its own.
type queueTrack struct {
	ID       int64   `json:"id"`
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	ArtistID int64   `json:"artistId"`
	Album    string  `json:"album"`
	AlbumID  int64   `json:"albumId"`
	GainDB   float64 `json:"gainDb"`
}

type queue struct {
	Title  string       `json:"title"`
	Tracks []queueTrack `json:"tracks"`
}

func (c *client) fetchQueue(query url.Values) (queue, error) {
	var q queue
	if err := c.getJSON("/music/queue", query, &q); err != nil {
		// A server predating single-song playback has no source=track case, so
		// it falls through to its single-album branch, finds no album param and
		// 404s — identical to the 404 a genuinely absent track id gets. Name
		// both rather than hand a bare HTTP error to someone who just typed
		// `hesplay song`; when a song name matched, it's the server.
		var herr *httpError
		if query.Get("source") == "track" && errors.As(err, &herr) && herr.status == http.StatusNotFound {
			return q, fmt.Errorf("the server has no track %s — either the id is stale, or this Hespera predates single-song playback and needs upgrading", query.Get("track"))
		}
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

// fetchSearchSection returns the rows of one labeled section of the server's
// search palette. limit asks for that many rows per section (0 leaves the
// server's own cap of 5 — a server that predates the parameter ignores it and
// answers with the cap, so asking for more degrades rather than fails).
func (c *client) fetchSearchSection(section, q string, limit int) ([]searchRow, error) {
	vals := url.Values{"q": {q}}
	if limit > 0 {
		vals.Set("limit", strconv.Itoa(limit))
	}
	var res struct {
		Sections []struct {
			Label string      `json:"label"`
			Rows  []searchRow `json:"rows"`
		} `json:"sections"`
	}
	if err := c.getJSON("/search", vals, &res); err != nil {
		return nil, err
	}
	for _, s := range res.Sections {
		if s.Label == section {
			return s.Rows, nil
		}
	}
	return nil, nil
}

// pickSearchRow chooses which row a typed name resolves to: an exact
// (case-insensitive) match on the row's text if there is one, else the first
// eligible row — search already ranks prefix matches first. keep filters rows
// the caller can't use (nil accepts all).
func pickSearchRow(rows []searchRow, name string, keep func(searchRow) bool) searchRow {
	var pick searchRow
	for _, r := range rows {
		if keep != nil && !keep(r) {
			continue
		}
		if strings.EqualFold(r.Text, name) {
			return r
		}
		if pick.Href == "" {
			pick = r
		}
	}
	return pick
}

// pickedLabel is how a fuzzy match is reported back to the user — the row's
// text plus its disambiguating context ("Nevermind (Nirvana · 1991)").
func pickedLabel(r searchRow) string {
	if r.Context == "" {
		return r.Text
	}
	return r.Text + " (" + r.Context + ")"
}

// resolveSearch resolves a name to an id via the server's search palette: rows
// carry their id only inside the href (/music/artist/{id}, /music/album/{id}),
// so it's parsed off the given prefix. picked names the choice so the caller can
// print it; a purely numeric argument that matches no name is taken as an id.
func (c *client) resolveSearch(section, hrefPrefix, name string) (id int64, picked string, err error) {
	rows, err := c.fetchSearchSection(section, name, 0)
	if err != nil {
		return 0, "", err
	}
	pick := pickSearchRow(rows, name, func(r searchRow) bool { return strings.HasPrefix(r.Href, hrefPrefix) })
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
	return id, pickedLabel(pick), nil
}

// resolveSong resolves a song name to a track id. Songs are the one search
// section whose row ACTS rather than navigates, so its href is a player URL
// (/music/player?album=N&track=N) and the id lives in the query string, not
// behind a path prefix — hence its own resolver rather than resolveSearch.
func (c *client) resolveSong(name string) (id int64, picked string, err error) {
	rows, err := c.fetchSearchSection("Songs", name, 0)
	if err != nil {
		return 0, "", err
	}
	pick := pickSearchRow(rows, name, nil)
	if pick.Href == "" {
		if id, perr := strconv.ParseInt(name, 10, 64); perr == nil && id > 0 {
			return id, "", nil
		}
		return 0, "", fmt.Errorf("no song matching %q", name)
	}
	if id, err = trackIDFromHref(pick.Href); err != nil {
		return 0, "", err
	}
	return id, pickedLabel(pick), nil
}

// trackIDFromHref pulls the track id out of a Songs row's player href.
func trackIDFromHref(href string) (int64, error) {
	u, perr := url.Parse(href)
	if perr != nil {
		return 0, fmt.Errorf("cannot parse a track id from %q", href)
	}
	id, perr := strconv.ParseInt(u.Query().Get("track"), 10, 64)
	if perr != nil || id <= 0 {
		return 0, fmt.Errorf("cannot parse a track id from %q", href)
	}
	return id, nil
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

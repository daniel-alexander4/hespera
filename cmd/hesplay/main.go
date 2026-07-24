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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
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
  version             Print hesplay version (also --version)

Names need no quoting (hesplay album abbey road) and resolve against the
server's search — the closest match plays and is printed; a purely numeric
argument that matches no name is tried as an id. Playback engine: mpv when
installed, else ffplay (from ffmpeg).

Order: an album plays in track order; artist/mix/playlist queues shuffle by
default — --ordered plays them as listed, --shuffle forces a shuffle.

Server: --server, else $HESPERA_SERVER, else http://127.0.0.1:8080.
`)
}

// resolveServer applies the --server > $HESPERA_SERVER > loopback-default
// precedence (the hescli resolveSocket shape) and normalizes the URL so
// "plex.local:8080" works without a scheme.
func resolveServer(flagVal string) string {
	s := strings.TrimSpace(flagVal)
	if s == "" {
		s = strings.TrimSpace(os.Getenv("HESPERA_SERVER"))
	}
	if s == "" {
		s = "http://127.0.0.1:8080"
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return strings.TrimSuffix(s, "/")
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
// across mpv versions, same ffmpeg volume syntax as ffplay.
func (e engine) args(streamURL string, gainDB float64) []string {
	af := fmt.Sprintf("volume=%.2fdB", gainDB)
	if e.name == "mpv" {
		return []string{"--no-video", "--really-quiet", "--af=lavfi=[" + af + "]", streamURL}
	}
	return []string{"-nodisp", "-autoexit", "-loglevel", "error", "-af", af, streamURL}
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
		start := time.Now()
		cmd := exec.CommandContext(ctx, e.path, e.args(c.base+"/stream/track/"+strconv.FormatInt(t.ID, 10), t.GainDB)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		runErr := cmd.Run()
		played := time.Since(start)
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

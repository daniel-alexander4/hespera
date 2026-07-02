// Command hescli is the Hespera management CLI. It configures a running Hespera
// server — libraries, scans/matches, integrity checks, and runtime settings
// (API keys, toggles, media folder) — by talking to the server's local
// management socket (DataDir/hescli.sock) over HTTP. It has no playback verbs.
//
// The socket is gated by peer credentials (root or the user running the server),
// so hescli must run as root or as that user. Point it at a non-default socket
// with --socket or HESPERA_SOCKET (needed when managing a server that runs as a
// different user, whose DataDir differs from yours).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"hespera/internal/config"
)

func main() {
	socket := flag.String("socket", "", "management socket path (default: <data-dir>/hescli.sock)")
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	c := newClient(resolveSocket(*socket))
	if err := dispatch(c, args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `hescli — Hespera management CLI

Usage:
  hescli [--socket PATH] <command> [args]

Commands:
  library list                             List libraries
  library add --name N --type T --path P   Add a library (type: music|tv|movies|photos|home_videos)
  library rm <id>                          Delete a library
  scan <id>                                Scan a library (chains match + integrity)
  match <id>                               Match a library's metadata
  integrity <id>                           Deep integrity check (flag-only)
  config list                              Show all runtime settings
  config get <key>                         Show one setting
  config set <key> <value>                 Set a setting (blank value clears an API key)
  jobs [--status S] [--type T] [--limit N] Recent jobs
  status                                   Server overview

Socket: --socket, else $HESPERA_SOCKET, else <data-dir>/hescli.sock.
`)
}

func resolveSocket(flagVal string) string {
	if s := strings.TrimSpace(flagVal); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("HESPERA_SOCKET")); s != "" {
		return s
	}
	return filepath.Join(config.FromEnv().DataDir, "hescli.sock")
}

func dispatch(c *client, args []string) error {
	switch args[0] {
	case "library":
		return libraryCmd(c, args[1:])
	case "scan":
		return actionCmd(c, "/scan", args[1:])
	case "match":
		return actionCmd(c, "/match", args[1:])
	case "integrity":
		return actionCmd(c, "/integrity", args[1:])
	case "config":
		return configCmd(c, args[1:])
	case "jobs":
		return jobsCmd(c, args[1:])
	case "status":
		return statusCmd(c)
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// --- commands ---

func libraryCmd(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("library: expected list|add|rm")
	}
	switch args[0] {
	case "list":
		var resp struct {
			Data []struct {
				ID       int64  `json:"id"`
				Name     string `json:"name"`
				Type     string `json:"type"`
				RootPath string `json:"root_path"`
			} `json:"data"`
		}
		if err := c.get("/libraries", nil, &resp); err != nil {
			return err
		}
		if len(resp.Data) == 0 {
			fmt.Println("no libraries")
			return nil
		}
		tw := newTable("ID", "TYPE", "NAME", "PATH")
		for _, l := range resp.Data {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", l.ID, l.Type, l.Name, l.RootPath)
		}
		return tw.Flush()
	case "add":
		fs := flag.NewFlagSet("library add", flag.ContinueOnError)
		name := fs.String("name", "", "library name")
		typ := fs.String("type", "", "library type (music|tv|movies|photos|home_videos)")
		path := fs.String("path", "", "root path (must be under the media folder)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return c.postMsg("/libraries/add", url.Values{
			"name": {*name}, "type": {*typ}, "root_path": {*path},
		})
	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("library rm: expected <id>")
		}
		return c.postMsg("/libraries/rm", url.Values{"id": {args[1]}})
	default:
		return fmt.Errorf("library: unknown subcommand %q", args[0])
	}
}

func actionCmd(c *client, path string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s: expected <library-id>", strings.TrimPrefix(path, "/"))
	}
	return c.postMsg(path, url.Values{"id": {args[0]}})
}

func configCmd(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("config: expected list|get|set")
	}
	switch args[0] {
	case "list":
		var resp struct {
			Data []configEntry `json:"data"`
		}
		if err := c.get("/config", nil, &resp); err != nil {
			return err
		}
		tw := newTable("KEY", "KIND", "SOURCE", "VALUE")
		for _, e := range resp.Data {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Key, e.Kind, e.Source, e.Value)
		}
		return tw.Flush()
	case "get":
		if len(args) < 2 {
			return fmt.Errorf("config get: expected <key>")
		}
		var resp struct {
			Data configEntry `json:"data"`
		}
		if err := c.get("/config/get", url.Values{"key": {args[1]}}, &resp); err != nil {
			return err
		}
		e := resp.Data
		fmt.Printf("%s = %s  (kind=%s, source=%s)\n", e.Key, e.Value, e.Kind, e.Source)
		if e.ApplyOnRestart {
			fmt.Println("(applies on restart)")
		}
		return nil
	case "set":
		if len(args) < 3 {
			return fmt.Errorf("config set: expected <key> <value>")
		}
		return c.postMsg("/config/set", url.Values{"key": {args[1]}, "value": {args[2]}})
	default:
		return fmt.Errorf("config: unknown subcommand %q", args[0])
	}
}

func jobsCmd(c *client, args []string) error {
	fs := flag.NewFlagSet("jobs", flag.ContinueOnError)
	status := fs.String("status", "", "filter by status (queued|running|completed|failed)")
	typ := fs.String("type", "", "filter by job type")
	limit := fs.Int("limit", 20, "max rows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	q := url.Values{}
	if *status != "" {
		q.Set("status", *status)
	}
	if *typ != "" {
		q.Set("job_type", *typ)
	}
	q.Set("limit", fmt.Sprint(*limit))

	var resp struct {
		Data []struct {
			ID              int64  `json:"id"`
			LibraryID       int64  `json:"library_id"`
			JobType         string `json:"job_type"`
			Status          string `json:"status"`
			ProgressCurrent int64  `json:"progress_current"`
			ProgressTotal   int64  `json:"progress_total"`
			Error           string `json:"error"`
		} `json:"data"`
	}
	if err := c.get("/jobs", q, &resp); err != nil {
		return err
	}
	if len(resp.Data) == 0 {
		fmt.Println("no jobs")
		return nil
	}
	tw := newTable("ID", "LIB", "TYPE", "STATUS", "PROGRESS", "ERROR")
	for _, j := range resp.Data {
		prog := ""
		if j.ProgressTotal > 0 {
			prog = fmt.Sprintf("%d/%d", j.ProgressCurrent, j.ProgressTotal)
		}
		fmt.Fprintf(tw, "%d\t%d\t%s\t%s\t%s\t%s\n", j.ID, j.LibraryID, j.JobType, j.Status, prog, j.Error)
	}
	return tw.Flush()
}

func statusCmd(c *client) error {
	var resp struct {
		Data struct {
			Version       string `json:"version"`
			MediaRoot     string `json:"media_root"`
			DataDir       string `json:"data_dir"`
			LibraryCount  int    `json:"library_count"`
			UptimeSeconds int64  `json:"uptime_seconds"`
		} `json:"data"`
	}
	if err := c.get("/status", nil, &resp); err != nil {
		return err
	}
	d := resp.Data
	fmt.Printf("version:    %s\n", d.Version)
	fmt.Printf("media root: %s\n", d.MediaRoot)
	fmt.Printf("data dir:   %s\n", d.DataDir)
	fmt.Printf("libraries:  %d\n", d.LibraryCount)
	fmt.Printf("uptime:     %s\n", (time.Duration(d.UptimeSeconds) * time.Second).String())
	return nil
}

// configEntry mirrors the server's config entry JSON.
type configEntry struct {
	Key            string `json:"key"`
	Kind           string `json:"kind"`
	Source         string `json:"source"`
	Value          string `json:"value"`
	ApplyOnRestart bool   `json:"apply_on_restart"`
}

// --- HTTP client over the unix socket ---

type client struct {
	http *http.Client
	base string
}

func newClient(sockPath string) *client {
	return &client{
		base: "http://unix",
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
				},
			},
		},
	}
}

// get issues a GET and decodes a 2xx JSON body into out.
func (c *client) get(path string, query url.Values, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

// postMsg issues a form POST and prints the server's "message" on success.
func (c *client) postMsg(path string, form url.Values) error {
	req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var resp struct {
		Message string `json:"message"`
	}
	if err := c.do(req, &resp); err != nil {
		return err
	}
	if resp.Message == "" {
		resp.Message = "ok"
	}
	fmt.Println(resp.Message)
	return nil
}

// do sends req (always accepting JSON), maps a non-2xx to an error (JSON message
// or raw body), and decodes a 2xx body into out when non-nil.
func (c *client) do(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the management socket (%v) — is Hespera running, and are you root or the server's user?", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s", serverMessage(body, resp.Status))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("bad response from server: %w", err)
	}
	return nil
}

// serverMessage extracts a human error from a response body: the JSON "message"
// field when present, else the trimmed raw body, else the HTTP status.
func serverMessage(body []byte, status string) string {
	var j struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &j) == nil && strings.TrimSpace(j.Message) != "" {
		return j.Message
	}
	if s := strings.TrimSpace(string(body)); s != "" {
		return s
	}
	return status
}

func newTable(headers ...string) *tabwriter.Writer {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	return tw
}

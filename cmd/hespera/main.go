package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"hespera/internal/browser"
	"hespera/internal/config"
	"hespera/internal/db"
	"hespera/internal/singleton"
	"hespera/internal/video"
	"hespera/internal/watch"
	"hespera/internal/web"
)

// version is set at build time via -ldflags "-X main.version=…" (see build.sh);
// it stamps the startup log and the static-asset cache-buster.
var version = "dev"

func main() {
	// --version reports the build and exits, before any startup. Without this the
	// flag is ignored and the app boots (window + random-port server) — a footgun
	// for anyone running `hespera --version` to check the install.
	if hasFlag("--version") || hasFlag("-version") {
		fmt.Println("hespera", version)
		return
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLogLevel(os.Getenv("HESPERA_LOG_LEVEL"))})))
	slog.Info("starting", "version", version)

	cfg := config.FromEnv()
	if err := cfg.Validate(); err != nil {
		slog.Error("config validation failed", "err", err)
		os.Exit(1)
	}

	// Second-instance guard (both modes): if a live Hespera already owns this
	// data dir — verified via the attach probe, not a bare pid — a second one
	// must not start and corrupt the shared DB/socket. What happens next depends
	// on the mode and --replace (see launchDecision):
	//   - app mode: ATTACH — open the chromeless window onto the running instance
	//     and exit (a desktop click, even with --replace, never kills a healthy
	//     service out from under the household).
	//   - server/headless mode without --replace: REFUSE — there's no window to
	//     open, so a second server is always a mistake (typically a `hespera`
	//     typed for `hescli`); abort BEFORE touching the DB or management socket
	//     and point at hescli.
	//   - server mode with --replace: PROCEED to ReplaceOthers below (deliberate
	//     take-over).
	appMode := os.Getenv("HESPERA_NO_BROWSER") == ""
	replace := hasFlag("--replace") || hasFlag("-replace")
	runningURL := runningInstanceURL(cfg.DataDir)
	switch launchDecision(appMode, replace, runningURL) {
	case launchAttach:
		slog.Info("attaching to running instance", "url", runningURL)
		if c, _, err := browser.Open(runningURL, filepath.Join(cfg.DataDir, "browser")); err != nil {
			slog.Error("could not open app window — browse to it manually", "url", runningURL, "err", err)
			os.Exit(1)
		} else {
			// The running instance owns the lifecycle; this launcher just
			// opened a window onto it and is done.
			_ = c.Process.Release()
		}
		return
	case launchRefuse:
		fmt.Fprintf(os.Stderr,
			"Hespera is already running at %s.\n\n"+
				"To control it from the command line, use hescli — for example:\n"+
				"    hescli status\n"+
				"    hescli jobs\n\n"+
				"Refusing to start a second instance against %s.\n"+
				"(Pass --replace to stop the running instance and take over.)\n",
			runningURL, cfg.DataDir)
		os.Exit(1)
	}

	// --replace (passed by the desktop launcher) SIGTERMs any other running
	// instance so a relaunch from the menu takes over cleanly — reached only
	// when the guard above neither attached nor refused, so a live service is
	// never killed by a desktop click. The app binds a random loopback port, so
	// this never has to wait for the old port to free.
	if replace {
		if n := singleton.ReplaceOthers(); n > 0 {
			slog.Info("replaced running instance", "count", n)
		}
	}

	video.SetConcurrency(cfg.FFmpegConcurrentLimit, cfg.FFmpegAcquireTimeout)
	video.SetSegmentConcurrency(cfg.HLSSegmentConcurrency)
	if eff := video.SetEncoder(context.Background(), cfg.HLSEncoder); eff != cfg.HLSEncoder {
		slog.Warn("hls encoder fell back", "requested", cfg.HLSEncoder, "using", eff)
	} else if eff == "vaapi" {
		slog.Info("hls segments will use the vaapi hardware encoder")
	}

	// Create the data dir on first run — the binary runs as the invoking user
	// (no container pre-creating /var/lib/hespera), so the default per-user dir
	// won't exist yet and SQLite can't create its file in a missing directory.
	// 0700: the dir holds the SQLite DB (incl. app_settings API keys), the
	// dedicated browser profile, and the management socket — no other local
	// user has business in it. The Chmod tightens pre-existing installs too
	// (MkdirAll never changes an existing dir's mode); best-effort, same as
	// the management socket's own 0600.
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		slog.Error("create data dir failed", "dir", cfg.DataDir, "err", err)
		os.Exit(1)
	}
	_ = os.Chmod(cfg.DataDir, 0o700)

	// Single-instance lock on the data dir — the hard mutual-exclusion behind the
	// launch guard (attach/refuse) and the socket/app.url races. Taken AFTER any
	// --replace take-over above (the old instance has exited, so its lock is
	// free) and BEFORE db.Open, so a second instance never opens the shared DB or
	// starts a duplicate job worker/watcher. flock auto-releases on process exit,
	// so a crash leaves no stale lock. Best-effort off unix / on flock-less
	// filesystems (see AcquireDataDirLock); a live holder is a hard refusal.
	dataLock, err := singleton.AcquireDataDirLock(cfg.DataDir)
	if errors.Is(err, singleton.ErrLocked) {
		fmt.Fprintf(os.Stderr,
			"Another Hespera instance is already running against %s.\n\n"+
				"To control it from the command line, use hescli — for example:\n"+
				"    hescli status\n\n"+
				"Refusing to start a second instance.\n"+
				"(Pass --replace to stop the running instance and take over.)\n",
			cfg.DataDir)
		os.Exit(1)
	}
	defer dataLock.Close()

	dbConn, err := db.Open(cfg.DBPath)
	if err != nil {
		slog.Error("db open failed", "err", err)
		os.Exit(1)
	}
	defer dbConn.Close()

	if err := db.Migrate(dbConn); err != nil {
		slog.Error("db migrate failed", "err", err)
		os.Exit(1)
	}

	// quit lets the UI's power button (POST /shutdown) initiate the same graceful
	// shutdown as a SIGTERM, cross-platform (syscall.Kill is Unix-only). main
	// selects on both below.
	quit := make(chan struct{})
	var quitOnce sync.Once
	quitFunc := func() { quitOnce.Do(func() { close(quit) }) }

	// App mode (the default) means the app window runs on this machine — which
	// also enables display-scale auto-detection (the handler may match the
	// window against this machine's displays). appMode was resolved above (the
	// second-instance guard branches on it too).
	h, err := web.New(web.Deps{
		Cfg:     cfg,
		DB:      dbConn,
		Version: version,
		Quit:    quitFunc,
		AppMode: appMode,
	})
	if err != nil {
		slog.Error("web handler initialization failed", "err", err)
		os.Exit(1)
	}

	// Local management socket for hescli (Linux only; root/owner-gated by
	// peer-cred). Best-effort — a failure here never blocks the app from serving.
	mgmt, err := serveManagementSocket(h, cfg.DataDir)
	if err != nil {
		slog.Warn("management socket unavailable", "err", err)
	} else if mgmt != nil {
		slog.Info("management socket listening", "path", config.ManagementSocketPath(cfg.DataDir))
	}

	// Library filesystem watcher: new media triggers the scan chain with zero
	// clicks (debounced per library; the watch_enabled setting is its runtime
	// kill-switch). Best-effort — without it the Scan button still works.
	watcher, err := watch.New(dbConn, func(libID int64) {
		if _, err := h.EnqueueLibraryScan(context.Background(), libID, "watch"); err != nil {
			slog.Warn("auto-scan enqueue failed", "library_id", libID, "err", err)
		} else {
			slog.Info("auto-scan triggered by file change", "library_id", libID)
		}
	}, 30*time.Second, 30*time.Second)
	if err != nil {
		slog.Warn("library watcher unavailable", "err", err)
	}

	// App mode opens a chromeless browser window and binds a random loopback
	// port — Hespera runs as a single-machine app. HESPERA_NO_BROWSER opts out
	// (server/headless), keeping the env-configured listen address. An
	// explicit HESPERA_LISTEN is always honored.
	listenAddr := cfg.Listen
	if appMode && os.Getenv("HESPERA_LISTEN") == "" {
		listenAddr = "127.0.0.1:0"
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		slog.Error("listen failed", "addr", listenAddr, "err", err)
		os.Exit(1)
	}
	boundAddr := ln.Addr().String()

	srv := &http.Server{
		Handler:           h.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("listening", "addr", boundAddr)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("serve failed", "err", err)
			os.Exit(1)
		}
	}()

	// Record this instance's URL so a later desktop launch attaches to it
	// (both modes — the headless service is exactly what the desktop icon
	// should connect to). Removed on clean shutdown below.
	writeAppURL(cfg.DataDir, appURL(boundAddr))
	defer removeAppURL(cfg.DataDir, appURL(boundAddr))

	var browserCmd *exec.Cmd
	// Closed when the app window exits on its own (the user hit the title bar's
	// close button) — that IS the quit gesture, so it must not be reaped twice.
	windowGone := make(chan struct{})
	if appMode {
		url := appURL(boundAddr)
		// A dedicated profile under the data dir makes the launched process own
		// the window, so quitting (a signal) can close it — and, conversely, so
		// closing the window quits Hespera (below).
		profileDir := filepath.Join(cfg.DataDir, "browser")
		c, ownsWindow, err := browser.Open(url, profileDir)
		if err != nil {
			slog.Warn("could not open app window — browse to it manually", "url", url, "err", err)
		} else {
			browserCmd = c
			slog.Info("opened app window", "url", url)
			// Closing the app window quits Hespera: the window IS the app in app
			// mode, so leaving the server running headless after the user closed
			// it would strand a process holding the port and the data-dir lock.
			// Gated on ownsWindow — the default-browser-tab fallback's command
			// exits the instant it hands the URL off, which would quit at once.
			if ownsWindow {
				go func() {
					_ = c.Wait()
					close(windowGone)
					slog.Info("app window closed — shutting down")
					quitFunc()
				}()
			}
		}
	}

	// Quit on a signal (SIGINT/SIGTERM) or the app window closing (quitFunc,
	// also wired to web.Deps.Quit).
	select {
	case <-stop:
	case <-quit:
	}

	// Close the app window we launched: the dedicated profile (see browser.Open)
	// means this process owns its window, so stopping it closes the window. Try a
	// graceful SIGTERM first (clean Chrome exit, no "restore pages" next launch);
	// fall back to Kill (Windows, where SIGTERM isn't delivered). Skipped when the
	// window is already gone (it closing is what quit us) — signalling a reaped
	// process races pid reuse. A no-op in the default-browser-tab fallback — that
	// cmd already exited and isn't the user's browser, so we never close their
	// window.
	select {
	case <-windowGone:
	default:
		if browserCmd != nil && browserCmd.Process != nil {
			if err := browserCmd.Process.Signal(syscall.SIGTERM); err != nil {
				_ = browserCmd.Process.Kill()
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Warn("http shutdown", "err", err)
	}
	if mgmt != nil {
		if err := mgmt.Close(); err != nil {
			slog.Warn("management socket shutdown", "err", err)
		}
	}
	if watcher != nil {
		_ = watcher.Close()
	}
	h.Shutdown()
	slog.Info("shutdown complete")
}

// launchAction is what a launch should do when it finds (or doesn't find) a
// live Hespera already owning this data dir.
type launchAction int

const (
	launchProceed launchAction = iota // start normally (a --replace take-over runs first)
	launchAttach                      // open an app window onto the running instance, then exit
	launchRefuse                      // abort: a second server would corrupt the shared DB/socket
)

// launchDecision resolves the second-instance policy from the mode, --replace,
// and whether a live instance was found (runningURL == "" means none). It is a
// pure function so the matrix is unit-testable; main handles the I/O (attach,
// print, exit) per the returned action.
//
// The load-bearing rule: app mode ATTACHES even with --replace, so a desktop
// click (Exec=hespera --replace) never kills a healthy service — only when no
// live instance answers does --replace's ReplaceOthers engage. Server/headless
// mode has no window to open, so a second instance is refused unless --replace
// asks for a deliberate take-over.
func launchDecision(appMode, replace bool, runningURL string) launchAction {
	if runningURL == "" {
		return launchProceed
	}
	if appMode {
		return launchAttach
	}
	if replace {
		return launchProceed
	}
	return launchRefuse
}

// parseLogLevel maps HESPERA_LOG_LEVEL to a slog level, defaulting to info for
// an empty or unrecognized value. At the default info level the per-request
// access log (withLogging, emitted at Debug) is dropped before its synchronous
// stdout write, so request serving does no log I/O on the hot path — a slow log
// sink (e.g. a stalling systemd journal under heavy disk load) can't add latency
// to playback/API requests. Set debug to restore per-request access logging.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// hasFlag reports whether a bare CLI flag is present in the arguments.
func hasFlag(name string) bool {
	for _, a := range os.Args[1:] {
		if a == name {
			return true
		}
	}
	return false
}

// appURL turns a bound listener address into the URL to open in the app window,
// substituting a concrete loopback host for an empty/wildcard bind so the URL is
// always reachable.
func appURL(boundAddr string) string {
	host, port, err := net.SplitHostPort(boundAddr)
	if err != nil {
		return "http://" + boundAddr + "/"
	}
	switch host {
	case "", "::", "0.0.0.0":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("starting", "version", version)

	cfg := config.FromEnv()
	if err := cfg.Validate(); err != nil {
		slog.Error("config validation failed", "err", err)
		os.Exit(1)
	}

	// Attach-first: if a Hespera instance is already running against this data
	// dir (typically the headless service — HESPERA_NO_BROWSER=1 under systemd),
	// a desktop launch must not start a second instance or --replace-kill the
	// service out from under the household. Instead, open the same chromeless
	// app window onto the running instance and exit. The window is the same
	// Chromium app-mode chrome as a normal launch (same dedicated profile).
	if os.Getenv("HESPERA_NO_BROWSER") == "" {
		if url := runningAppURL(cfg.DataDir); url != "" {
			slog.Info("attaching to running instance", "url", url)
			if c, err := browser.Open(url, filepath.Join(cfg.DataDir, "browser")); err != nil {
				slog.Error("could not open app window — browse to it manually", "url", url, "err", err)
				os.Exit(1)
			} else {
				// The running instance owns the lifecycle; this launcher just
				// opened a window onto it and is done.
				_ = c.Process.Release()
			}
			return
		}
	}

	// --replace (passed by the desktop launcher) SIGTERMs any other running
	// instance so a relaunch from the menu takes over cleanly — reached only
	// when the attach probe found nothing healthy, so a live service is never
	// killed by a desktop click. The app binds a random loopback port, so this
	// never has to wait for the old port to free.
	if hasFlag("--replace") || hasFlag("-replace") {
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
	// window against this machine's displays). Resolved here because both the
	// handler and the listen/launch logic below branch on it.
	appMode := os.Getenv("HESPERA_NO_BROWSER") == ""

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
	if appMode {
		url := appURL(boundAddr)
		// A dedicated profile under the data dir makes the launched process own
		// the window, so quitting (power button / signal) can close it.
		profileDir := filepath.Join(cfg.DataDir, "browser")
		if c, err := browser.Open(url, profileDir); err != nil {
			slog.Warn("could not open app window — browse to it manually", "url", url, "err", err)
		} else {
			browserCmd = c
			slog.Info("opened app window", "url", url)
		}
	}

	// Quit on a signal (SIGINT/SIGTERM) or the UI power button (POST /shutdown).
	select {
	case <-stop:
	case <-quit:
		slog.Info("shutdown requested via UI")
	}

	// Close the app window we launched: the dedicated profile (see browser.Open)
	// means this process owns its window, so stopping it closes the window. Try a
	// graceful SIGTERM first (clean Chrome exit, no "restore pages" next launch);
	// fall back to Kill (Windows, where SIGTERM isn't delivered). A no-op in the
	// default-browser-tab fallback — that cmd already exited and isn't the user's
	// browser, so we never close their window.
	if browserCmd != nil && browserCmd.Process != nil {
		if err := browserCmd.Process.Signal(syscall.SIGTERM); err != nil {
			_ = browserCmd.Process.Kill()
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

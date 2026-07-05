package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hespera/internal/config"
)

// The app-URL discovery file: every running instance (app or server mode)
// records its reachable loopback URL in DataDir/app.url. A desktop launch
// reads it first and, when a live Hespera answers there, ATTACHES — opens the
// app window onto the running instance instead of starting a second one (or,
// worse, --replace killing a headless service that was serving the whole
// household). Stale files are harmless: the health check fails and startup
// proceeds normally, overwriting the file.

const appURLFile = "app.url"

func appURLPath(dataDir string) string { return filepath.Join(dataDir, appURLFile) }

// writeAppURL records this instance's URL for future launches to attach to.
// Best-effort — a failure only costs the attach shortcut.
func writeAppURL(dataDir, url string) {
	_ = os.WriteFile(appURLPath(dataDir), []byte(url+"\n"), 0o600)
}

// removeAppURL clears the discovery file on clean shutdown so the next launch
// doesn't waste a probe — but only when it still holds THIS instance's URL
// (another instance started since would have overwritten it; deleting theirs
// would hide a live server from the next launch). A crash leaves the file
// behind; the health check covers that.
func removeAppURL(dataDir, url string) {
	b, err := os.ReadFile(appURLPath(dataDir))
	if err != nil || strings.TrimSpace(string(b)) != url {
		return
	}
	_ = os.Remove(appURLPath(dataDir))
}

// recordedAppURL returns the URL an instance wrote to the discovery file, with
// no liveness probe — "" when the file is absent or doesn't hold a loopback URL.
// The caller decides whether that instance is actually live (via the HTTP probe
// or the management-socket oracle); this is just the recorded address.
func recordedAppURL(dataDir string) string {
	b, err := os.ReadFile(appURLPath(dataDir))
	if err != nil {
		return ""
	}
	url := strings.TrimSpace(string(b))
	if !strings.HasPrefix(url, "http://") {
		return ""
	}
	return url
}

// runningAppURL returns the URL of a live Hespera instance recorded in the
// discovery file, or "" when there is none (no file, or whatever is at that
// address is down or isn't Hespera — a reused port must never be attached to).
func runningAppURL(dataDir string) string {
	url := recordedAppURL(dataDir)
	if url == "" {
		return ""
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(strings.TrimSuffix(url, "/") + "/healthz")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" ||
		resp.Header.Get("X-Hespera") == "" {
		return ""
	}
	return url
}

// runningInstanceURL resolves the URL of a live Hespera owning this data dir,
// robust to a false-negative HTTP health probe. The fast path is the probe
// (runningAppURL). If that finds nothing but the management socket is alive —
// a disk-free liveness oracle the kernel answers from the listen backlog, so it
// stays reliable when an I/O-saturated box stalls the HTTP request in
// withLogging's synchronous stdout write — the recorded app.url is trusted
// directly: a live management socket proves that instance (and thus its
// recording) is current, which is exactly the stale-file/reused-port case the
// probe's X-Hespera check exists to rule out. This stops a desktop
// `hespera --replace` launch from SIGTERM-killing a healthy headless server on a
// transient probe timeout. "" when no live instance is found. The socket oracle
// is Linux-only (managementSocketAlive is a no-op stub elsewhere), which is
// where the headless-service risk lives; other platforms keep the probe alone.
func runningInstanceURL(dataDir string) string {
	if url := runningAppURL(dataDir); url != "" {
		return url
	}
	if managementSocketAlive(config.ManagementSocketPath(dataDir)) {
		return recordedAppURL(dataDir)
	}
	return ""
}

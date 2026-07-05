//go:build linux

package main

import (
	"net"
	"os"
	"testing"

	"hespera/internal/config"
)

// TestRunningInstanceURLSocketBackstop pins the false-negative fix: when the
// HTTP health probe finds nothing (no server answers app.url) but the management
// socket is alive, runningInstanceURL still reports the recorded URL — so a
// desktop `hespera --replace` launch attaches to a healthy headless server whose
// HTTP probe merely stalled under I/O load, instead of SIGTERM-killing it.
func TestRunningInstanceURLSocketBackstop(t *testing.T) {
	dir := t.TempDir()
	const recorded = "http://127.0.0.1:65535/" // nothing serves HTTP here

	// Neither oracle live (no socket, HTTP refused) → no instance.
	writeAppURL(dir, recorded)
	if got := runningInstanceURL(dir); got != "" {
		t.Fatalf("runningInstanceURL = %q, want empty with nothing live", got)
	}

	// A live management socket with no HTTP server answering → the recorded URL
	// is trusted via the disk-free backstop.
	ln, err := net.Listen("unix", config.ManagementSocketPath(dir))
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()
	if got := runningInstanceURL(dir); got != recorded {
		t.Fatalf("runningInstanceURL = %q, want %q via the socket backstop", got, recorded)
	}

	// Socket alive but no recorded URL → still empty (nothing to attach to; the
	// launch can't open a window against an unknown address).
	if err := os.Remove(appURLPath(dir)); err != nil {
		t.Fatalf("remove app.url: %v", err)
	}
	if got := runningInstanceURL(dir); got != "" {
		t.Fatalf("runningInstanceURL = %q, want empty with no recorded URL", got)
	}
}

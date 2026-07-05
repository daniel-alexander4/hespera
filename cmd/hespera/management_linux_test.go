//go:build linux

package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthorizedUID(t *testing.T) {
	self := uint32(os.Getuid())

	if !authorizedUID(0) {
		t.Error("root (uid 0) must be authorized")
	}
	if !authorizedUID(self) {
		t.Error("the server's own uid must be authorized")
	}
	// A different, non-root uid must be refused. Pick one that is neither 0 nor
	// self (self+1 unless that collides, in which case self+2).
	other := self + 1
	if other == 0 {
		other = self + 2
	}
	if authorizedUID(other) {
		t.Errorf("uid %d (neither root nor server) must be refused", other)
	}
}

// managementSocketAlive must tell a live listener from a stale socket file (the
// startup do-no-harm probe depends on this to skip a live owner but rebind a
// stale one).
func TestManagementSocketAlive(t *testing.T) {
	dir := t.TempDir()

	// Nonexistent path — nothing to connect to.
	if managementSocketAlive(filepath.Join(dir, "absent.sock")) {
		t.Fatal("absent socket reported alive")
	}

	// A live listener answers a connect.
	live := filepath.Join(dir, "live.sock")
	ln, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if !managementSocketAlive(live) {
		t.Fatal("live socket reported dead")
	}

	// A stale socket file (listener closed without unlinking) refuses the
	// connection, so it must read as not-alive → safe to rebind.
	stale := filepath.Join(dir, "stale.sock")
	sln, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	sln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = sln.Close()
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("stale socket file should remain: %v", err)
	}
	if managementSocketAlive(stale) {
		t.Fatal("stale socket reported alive")
	}
}

// Close must NOT unlink the socket file. That is the fix for the reported
// "healthy server, dead hescli" race: a --replace take-over can rebind the path
// to a new instance's socket before the old instance's Close runs, and an
// unlink-on-shutdown would remove the new one. Since inode identity is unsound
// across remove+recreate (inode numbers get reused), the fix is simply to never
// unlink at shutdown — the next startup reaps the inert leftover.
func TestCloseDoesNotUnlink(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hescli.sock")
	ln, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	defer ln.Close()

	srv := &managementServer{srv: &http.Server{}}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("Close must leave the socket file in place, stat err = %v", err)
	}
}

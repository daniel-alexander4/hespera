package main

import (
	"strings"
	"testing"

	"hespera/internal/config"
)

// TestResolveSocketPrecedence covers the socket resolution order: --socket
// flag, then $HESPERA_SOCKET, then the shared config.ManagementSocketPath
// derivation (so the client lands on the same runtime-dir fallback the server
// binds when the DataDir is over-long).
func TestResolveSocketPrecedence(t *testing.T) {
	t.Setenv("HESPERA_SOCKET", "/env/socket.sock")
	if got := resolveSocket(" /flag/socket.sock "); got != "/flag/socket.sock" {
		t.Fatalf("flag should win, got %q", got)
	}
	if got := resolveSocket(""); got != "/env/socket.sock" {
		t.Fatalf("env should win over the default, got %q", got)
	}

	t.Setenv("HESPERA_SOCKET", "")
	t.Setenv("HESPERA_DATA_DIR", "/var/lib/hespera")
	if got := resolveSocket(""); got != "/var/lib/hespera/hescli.sock" {
		t.Fatalf("default should be DataDir/hescli.sock, got %q", got)
	}

	// Over-long DataDir → the client derives the same fallback the server binds.
	long := "/" + strings.Repeat("x", 120)
	t.Setenv("HESPERA_DATA_DIR", long)
	if got := resolveSocket(""); got != config.ManagementSocketPath(long) {
		t.Fatalf("client and server must agree on the fallback, got %q", got)
	}
}

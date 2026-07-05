//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"hespera/internal/config"
	"hespera/internal/web"
)

// serveManagementSocket starts the hescli management API on a unix socket at
// config.ManagementSocketPath (DataDir/hescli.sock, with a runtime-dir
// fallback for DataDirs past the sun_path limit), gated by SO_PEERCRED: only
// root or the user running the server may connect (the socket is a local admin
// channel, so cross-user access is refused even though the socket file is
// reachable). Returns a Closer that stops the server and removes the socket.
// Linux-only — peer-cred is a Linux socket option; other platforms get the
// no-op stub in management_other.go.
func serveManagementSocket(h *web.Handler, dataDir string) (io.Closer, error) {
	sockPath := config.ManagementSocketPath(dataDir)

	// Do no harm: if a live instance already answers on this socket, a second
	// instance is booting against the same data dir (the upstream launch guard
	// missed it — e.g. a stale/absent app.url with a false-negative health
	// probe). Don't clobber the running instance's socket; skip our own and let
	// it keep serving hescli. A stale socket left by a hard-killed instance
	// won't answer, so we fall through and rebind it below.
	if managementSocketAlive(sockPath) {
		return nil, fmt.Errorf("a live instance already owns %s — leaving it and skipping this instance's management socket", sockPath)
	}

	// Remove a stale socket left by a hard-killed prior instance (nobody live
	// answered above). A deliberate --replace take-over already waited for the
	// old instance to exit (singleton.ReplaceOthers), so its socket is gone or
	// dead by the time we get here.
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	// Own the socket lifetime ourselves: disable Go's unlink-on-close so shutdown
	// never removes the file. Go's default calls unlink(path) with no ownership
	// check, so a shutting-down old instance would remove a new instance's
	// freshly-bound socket at the same path (the reported bug: healthy server,
	// dead hescli). By never unlinking at shutdown, an instance can't remove any
	// socket but its own-that-it-leaves-behind; the leftover file is inert (a
	// connect refuses) and the next startup reaps it, guarded by the liveness
	// probe above. Inode comparison can't do this soundly — inode numbers are
	// reused across remove+recreate — so "don't unlink at all" is the fix.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)

	// Belt-and-braces: restrict the socket file too (peer-cred is the real gate).
	_ = os.Chmod(sockPath, 0o600)

	srv := &http.Server{
		Handler:           h.ManagementRouter(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := srv.Serve(&peerCredListener{ln.(*net.UnixListener)}); err != nil && err != http.ErrServerClosed {
			slog.Warn("management socket serve failed", "err", err)
		}
	}()

	return &managementServer{srv: srv}, nil
}

type managementServer struct {
	srv *http.Server
}

func (m *managementServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Deliberately does NOT unlink the socket file — see serveManagementSocket.
	// The leftover file is inert and the next startup reaps it after the liveness
	// probe confirms no live owner, so shutdown can never remove another
	// instance's socket during a --replace take-over.
	_ = m.srv.Shutdown(ctx)
	return nil
}

// managementSocketAlive reports whether something is currently listening on the
// unix socket at sockPath — a successful connect means a live instance owns it.
// A stale socket file left by a cleanly-stopped or hard-killed instance refuses
// the connection, so this distinguishes "live owner, don't clobber" from "stale,
// safe to rebind". A local unix accept is disk-free, so this stays reliable even
// on an I/O-saturated box where the HTTP health probe can time out.
func managementSocketAlive(sockPath string) bool {
	c, err := net.DialTimeout("unix", sockPath, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// peerCredListener wraps a unix listener and refuses any peer whose credentials
// aren't authorized (root or the server's own uid), closing the connection
// before it reaches the HTTP server.
type peerCredListener struct {
	*net.UnixListener
}

func (l *peerCredListener) Accept() (net.Conn, error) {
	for {
		c, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		uid, ok := peerUID(c)
		if !ok || !authorizedUID(uid) {
			slog.Warn("management socket: rejected unauthorized peer", "uid", uid, "resolved", ok)
			_ = c.Close()
			continue
		}
		return c, nil
	}
}

// peerUID reads the connecting process's uid via SO_PEERCRED (stdlib syscall, no
// external dependency).
func peerUID(c *net.UnixConn) (uint32, bool) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, false
	}
	var uid uint32
	var ok bool
	_ = raw.Control(func(fd uintptr) {
		ucred, e := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if e == nil {
			uid = ucred.Uid
			ok = true
		}
	})
	return uid, ok
}

// authorizedUID gates the management socket: root (0) or the uid running the
// server. The server user already fully controls the DB, config, and media, so
// letting them manage via the CLI without sudo is no privilege escalation; every
// other user is refused.
func authorizedUID(uid uint32) bool {
	return uid == 0 || uid == uint32(os.Getuid())
}

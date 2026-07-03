//go:build linux

package main

import (
	"context"
	"errors"
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
	// Remove a stale socket left by a hard-killed prior instance; a live instance
	// is already handled by the singleton --replace path before we get here.
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
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

	return &managementServer{srv: srv, sockPath: sockPath}, nil
}

type managementServer struct {
	srv      *http.Server
	sockPath string
}

func (m *managementServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.srv.Shutdown(ctx)
	// The unix listener already unlinks the socket on close; this is belt-and-
	// braces, so a "not found" is the expected success case, not an error.
	if err := os.Remove(m.sockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
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

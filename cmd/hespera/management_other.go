//go:build !linux

package main

import (
	"io"

	"hespera/internal/web"
)

// serveManagementSocket is a no-op off Linux: the hescli management socket relies
// on SO_PEERCRED (a Linux socket option) for its access gate, and the CLI targets
// headless Linux servers. Returning (nil, nil) makes main skip it cleanly.
func serveManagementSocket(_ *web.Handler, _ string) (io.Closer, error) {
	return nil, nil
}

// managementSocketAlive is a no-op off Linux: no non-Linux platform serves the
// management socket (serveManagementSocket above is a no-op), so nothing is ever
// listening to dial. The launch decision's socket-liveness backstop therefore
// degrades to the HTTP probe alone — which is fine, since the headless-service
// risk it guards against is a Linux/systemd concern.
func managementSocketAlive(_ string) bool { return false }

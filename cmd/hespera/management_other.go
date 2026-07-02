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

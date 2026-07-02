//go:build linux

package main

import (
	"os"
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

//go:build !unix

package main

import (
	"errors"
	"os"
)

const noisePauseSupported = false

func signalNoisePause(*os.Process, bool) error {
	return errors.New("pausing noise is not supported on this platform")
}

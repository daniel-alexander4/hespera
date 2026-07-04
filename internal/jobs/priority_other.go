//go:build !linux

package jobs

// lowerWorkerPriority is a no-op off Linux: ioprio_set is Linux-only, and the
// background-I/O concern it addresses (scan/integrity reads starving a media
// drive) is a property of the Linux deployment shape.
func lowerWorkerPriority() {}

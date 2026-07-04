//go:build linux

package jobs

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"hespera/internal/db"
)

// TestWorkerRunsAtBackgroundPriority proves an executor runs on the
// deprioritized worker thread: idle I/O class + nice 19. Children spawned from
// that thread inherit both (per-thread properties preserved across fork/exec),
// so this also covers the ffmpeg/ffprobe processes jobs launch.
func TestWorkerRunsAtBackgroundPriority(t *testing.T) {
	dir := t.TempDir()
	conn, err := db.Open(filepath.Join(dir, "test.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	svc := New(conn)
	type prio struct {
		ioprio uintptr
		nice   int
	}
	got := make(chan prio, 1)
	jobID, err := svc.Enqueue("scan", 1, "test", func(ctx context.Context, jobID, libraryID int64) error {
		// The executor runs on the locked worker thread, so "calling thread"
		// syscalls observe the priority the worker set.
		io, _, _ := syscall.Syscall(syscall.SYS_IOPRIO_GET, ioprioWhoProcess, 0, 0)
		n, _ := syscall.Getpriority(syscall.PRIO_PROCESS, 0)
		got <- prio{ioprio: io, nice: n}
		return nil
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if jobID <= 0 {
		t.Fatalf("expected positive jobID, got %d", jobID)
	}

	select {
	case p := <-got:
		if p.ioprio>>ioprioClassShift != ioprioClassIdle {
			t.Errorf("worker I/O class = %d, want idle (%d)", p.ioprio>>ioprioClassShift, ioprioClassIdle)
		}
		// Getpriority returns 20-nice (1..40) to avoid negative syscall results:
		// nice 19 → 1.
		if p.nice != 1 {
			t.Errorf("worker Getpriority = %d, want 1 (nice 19)", p.nice)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("job never ran")
	}
}

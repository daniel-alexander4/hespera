package jobs

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"hespera/internal/db"
)

func TestReconcileStaleJobsOnStartup(t *testing.T) {
	dir := t.TempDir()
	conn, err := db.Open(filepath.Join(dir, "test.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Simulate rows a prior process left mid-flight, plus a terminal row that must
	// not be touched.
	for _, st := range []string{"running", "queued", "done"} {
		if _, err := conn.Exec(
			"INSERT INTO scan_jobs (library_id, job_type, status, progress_current, progress_total, created_at) VALUES (1, 'music_scan', ?, 0, 0, datetime('now'))",
			st,
		); err != nil {
			t.Fatalf("seed %s: %v", st, err)
		}
	}

	New(conn) // runs reconcileStaleJobs

	var stale int
	if err := conn.QueryRow("SELECT COUNT(*) FROM scan_jobs WHERE status IN ('running','queued')").Scan(&stale); err != nil {
		t.Fatalf("count stale: %v", err)
	}
	if stale != 0 {
		t.Fatalf("expected 0 running/queued after reconcile, got %d", stale)
	}
	var done int
	if err := conn.QueryRow("SELECT COUNT(*) FROM scan_jobs WHERE status='done'").Scan(&done); err != nil {
		t.Fatalf("count done: %v", err)
	}
	if done != 1 {
		t.Fatalf("terminal 'done' row must be untouched, got %d done rows", done)
	}
}

func TestEnqueueAndRun(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	svc := New(conn)
	var ran atomic.Int32

	jobID, err := svc.Enqueue("music_scan", 1, "test", func(ctx context.Context, jobID, libraryID int64) error {
		ran.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if jobID <= 0 {
		t.Fatalf("expected positive jobID, got %d", jobID)
	}

	// Wait for the job to reach its terminal DB status. Polling the `ran`
	// counter is not enough: it is incremented at the start of the executor,
	// before runJob writes status='done', so asserting status right after
	// ran==1 races finishJob.
	deadline := time.Now().Add(5 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		if err := conn.QueryRow("SELECT status FROM scan_jobs WHERE id=?", jobID).Scan(&status); err == nil {
			if status == "done" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != "done" {
		t.Fatalf("expected status=done, got %q", status)
	}
	if ran.Load() != 1 {
		t.Fatalf("expected job to run once, ran %d times", ran.Load())
	}
}

func TestPanicInExecutorIsIsolated(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	svc := New(conn)

	// A panicking executor must not kill the worker goroutine.
	panicID, err := svc.Enqueue("music_scan", 1, "test", func(ctx context.Context, jobID, libraryID int64) error {
		panic("boom")
	})
	if err != nil {
		t.Fatalf("Enqueue panic job: %v", err)
	}

	// The panicking job should end up failed, not stuck running.
	deadline := time.Now().Add(5 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		if err := conn.QueryRow("SELECT status FROM scan_jobs WHERE id=?", panicID).Scan(&status); err == nil {
			if status == "failed" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != "failed" {
		t.Fatalf("expected panicking job status=failed, got %q", status)
	}

	// The worker must still drain subsequent jobs.
	var ran atomic.Int32
	nextID, err := svc.Enqueue("music_scan", 1, "test", func(ctx context.Context, jobID, libraryID int64) error {
		ran.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("Enqueue follow-up job: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := conn.QueryRow("SELECT status FROM scan_jobs WHERE id=?", nextID).Scan(&status); err == nil {
			if status == "done" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != "done" {
		t.Fatalf("worker did not survive the panic: follow-up job status=%q", status)
	}
	if ran.Load() != 1 {
		t.Fatalf("expected follow-up job to run once, ran %d times", ran.Load())
	}
}

func TestCancelJob(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	svc := New(conn)
	started := make(chan struct{})

	jobID, err := svc.Enqueue("music_scan", 1, "test", func(ctx context.Context, jobID, libraryID int64) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Wait for the job to start.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("job did not start in time")
	}

	if err := svc.RequestCancel(jobID); err != nil {
		t.Fatalf("RequestCancel: %v", err)
	}

	// Wait for job to finish.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := conn.QueryRow("SELECT status FROM scan_jobs WHERE id=?", jobID).Scan(&status); err == nil {
			if status == "canceled" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job was not canceled within timeout")
}

func newJobsService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return New(conn), conn
}

// EnqueueUnique collapses a duplicate that is still QUEUED, but not one enqueued
// against a RUNNING job — a running scan may have started before the change that
// prompted the new enqueue, so a fresh queued job must still be allowed through.
func TestEnqueueUniqueDedupsQueuedNotRunning(t *testing.T) {
	svc, conn := newJobsService(t)
	var ran atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})

	// A blocks while "running" so B and C enqueue against a known state.
	idA, err := svc.EnqueueUnique("tv_match", 2, "test", func(ctx context.Context, j, l int64) error {
		ran.Add(1)
		started <- struct{}{}
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("enqueue A: %v", err)
	}
	<-started // A is now running (worker is blocked in it)

	// B: A is running, none queued → a fresh job with a new id.
	idB, err := svc.EnqueueUnique("tv_match", 2, "test", func(ctx context.Context, j, l int64) error {
		ran.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("enqueue B: %v", err)
	}
	if idB == idA {
		t.Fatalf("B must be a new job while A runs, got A's id %d", idA)
	}

	// C: B is queued → deduped to B's id, never becomes its own job.
	idC, err := svc.EnqueueUnique("tv_match", 2, "test", func(ctx context.Context, j, l int64) error {
		ran.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("enqueue C: %v", err)
	}
	if idC != idB {
		t.Fatalf("C must dedup to queued B (%d), got %d", idB, idC)
	}

	var rows int
	if err := conn.QueryRow("SELECT COUNT(*) FROM scan_jobs WHERE job_type='tv_match' AND library_id=2").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("want 2 rows (A queued-then-running, B queued), got %d", rows)
	}

	close(release) // A finishes → worker runs B; C was never a separate job
	deadline := time.Now().Add(3 * time.Second)
	for ran.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("A and B did not both run (ran=%d)", ran.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	if got := ran.Load(); got != 2 {
		t.Fatalf("executor ran %d times, want 2 (C was deduped away)", got)
	}
}

// Plain Enqueue never dedups, and EnqueueUnique dedups only within the same
// (type, lib) — different types/libraries are independent work.
func TestEnqueueDedupScoping(t *testing.T) {
	svc, conn := newJobsService(t)
	var ran atomic.Int32
	exec := func(ctx context.Context, j, l int64) error { ran.Add(1); return nil }

	// Two plain Enqueue of the same (type,lib) → two distinct jobs.
	a, _ := svc.Enqueue("tv_scan", 2, "test", exec)
	b, _ := svc.Enqueue("tv_scan", 2, "test", exec)
	if a == b || a <= 0 || b <= 0 {
		t.Fatalf("plain Enqueue must not dedup: a=%d b=%d", a, b)
	}
	// EnqueueUnique across different libraries → independent jobs.
	c, _ := svc.EnqueueUnique("tv_match", 2, "test", exec)
	d, _ := svc.EnqueueUnique("tv_match", 3, "test", exec)
	if c == d || c <= 0 || d <= 0 {
		t.Fatalf("different libraries must not dedup: c=%d d=%d", c, d)
	}

	deadline := time.Now().Add(3 * time.Second)
	for ran.Load() < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("expected 4 executions, got %d", ran.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	var total int
	if err := conn.QueryRow("SELECT COUNT(*) FROM scan_jobs").Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Fatalf("want 4 rows, got %d", total)
	}
}

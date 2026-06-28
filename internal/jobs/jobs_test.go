package jobs

import (
	"context"
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
			"INSERT INTO scan_jobs (library_id, job_type, status, progress_current, progress_total, created_at) VALUES (1, 'scan', ?, 0, 0, datetime('now'))",
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

	jobID, err := svc.Enqueue("scan", 1, "test", func(ctx context.Context, jobID, libraryID int64) error {
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
	panicID, err := svc.Enqueue("scan", 1, "test", func(ctx context.Context, jobID, libraryID int64) error {
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
	nextID, err := svc.Enqueue("scan", 1, "test", func(ctx context.Context, jobID, libraryID int64) error {
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

	jobID, err := svc.Enqueue("scan", 1, "test", func(ctx context.Context, jobID, libraryID int64) error {
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

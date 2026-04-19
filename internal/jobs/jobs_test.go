package jobs

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"isomedia/internal/db"
)

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

	// Wait for the job to run.
	deadline := time.Now().Add(5 * time.Second)
	for ran.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if ran.Load() != 1 {
		t.Fatalf("expected job to run once, ran %d times", ran.Load())
	}

	// Check status in DB.
	var status string
	if err := conn.QueryRow("SELECT status FROM scan_jobs WHERE id=?", jobID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "done" {
		t.Fatalf("expected status=done, got %q", status)
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

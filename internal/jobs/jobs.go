package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

var (
	ErrJobNotFound  = errors.New("job not found")
	ErrJobNotCancel = errors.New("job is not cancelable")
	ErrQueueFull    = errors.New("job queue is full")
)

// Job status values, matching the scan_jobs.status column.
const (
	statusQueued   = "queued"
	statusRunning  = "running"
	statusDone     = "done"
	statusFailed   = "failed"
	statusCanceled = "canceled"
)

type JobRequest struct {
	JobID     int64
	LibraryID int64
	Executor  func(ctx context.Context, jobID, libraryID int64) error
}

type Service struct {
	db      *sql.DB
	queue   chan JobRequest
	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
}

// jobRetention bounds how long a terminal scan_jobs row is kept. The table is
// the audit log for scans/matches/integrity runs; without a cap it grows forever
// (bloating the table and the latest-per-library / recent-jobs queries). Old
// terminal rows have no operational value, so they're swept at startup.
const jobRetention = 30 * 24 * time.Hour

func New(db *sql.DB) *Service {
	s := &Service{
		db:      db,
		queue:   make(chan JobRequest, 128),
		cancels: make(map[int64]context.CancelFunc),
	}
	s.reconcileStaleJobs()
	s.pruneOldJobs()
	go s.worker()
	return s
}

// pruneOldJobs deletes terminal scan_jobs older than jobRetention. The cutoff is
// formatted in Go with the same RFC3339Nano layout finishJob writes to ended_at
// (both UTC), so the string comparison is chronological to ~1s precision. It is
// NOT exact at sub-second granularity: RFC3339Nano trims trailing zeros, so a
// whole-second stamp ("…05Z") sorts lexicographically *after* a fractional one
// in the same second ("…05.5Z", since '.' < 'Z'). Against a 30-day retention a
// ≤1s boundary error is immaterial — but don't copy this comparison idiom for
// tight windowing.
func (s *Service) pruneOldJobs() {
	cutoff := time.Now().UTC().Add(-jobRetention).Format(time.RFC3339Nano)
	res, err := s.db.Exec(
		`DELETE FROM scan_jobs WHERE ended_at != '' AND ended_at < ? AND status IN (?, ?, ?)`,
		cutoff, statusDone, statusFailed, statusCanceled,
	)
	if err != nil {
		slog.Warn("jobs prune old", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("jobs pruned old rows", "count", n)
	}
}

// reconcileStaleJobs marks any 'running'/'queued' rows left by a previous process
// (a crash or restart) as 'failed'. The in-memory queue is the only thing that
// holds runnable work, so those rows can never make progress on their own — left
// alone they hang in a non-terminal state forever and keep reporting cancelable.
func (s *Service) reconcileStaleJobs() {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.Exec(
		`UPDATE scan_jobs SET status=?, error=?, ended_at=? WHERE status IN (?, ?)`,
		statusFailed, "interrupted by restart", now, statusRunning, statusQueued,
	)
	if err != nil {
		slog.Warn("jobs reconcile stale", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("jobs reconciled stale rows to failed", "count", n)
	}
}

// Shutdown cancels any in-flight job contexts so a graceful exit lets the worker
// mark them terminal promptly. Best-effort (the worker may not run before the
// process exits) — reconcileStaleJobs on the next startup is the reliable backstop.
func (s *Service) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.cancels {
		cancel()
	}
}

func (s *Service) Enqueue(jobType string, libraryID int64, createdBy string, executor func(ctx context.Context, jobID, libraryID int64) error) (int64, error) {
	if libraryID < 0 {
		return 0, errors.New("invalid library id")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if createdBy == "" {
		createdBy = "system"
	}

	res, err := s.db.Exec(
		`INSERT INTO scan_jobs (
			library_id, job_type, status, progress_current, progress_total, payload_json,
			created_by, duration_ms, cancel_requested, created_at, error, started_at, ended_at
		) VALUES (?, ?, ?, 0, 0, '', ?, 0, 0, ?, '', '', '')`,
		libraryID, jobType, statusQueued, createdBy, now,
	)
	if err != nil {
		return 0, err
	}
	jobID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	req := JobRequest{JobID: jobID, LibraryID: libraryID, Executor: executor}
	select {
	case s.queue <- req:
		return jobID, nil
	default:
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, _ = s.db.Exec(
			`UPDATE scan_jobs SET status=?, error=?, ended_at=?, duration_ms=0 WHERE id=?`,
			statusFailed, ErrQueueFull.Error(), now, jobID,
		)
		return 0, ErrQueueFull
	}
}

func (s *Service) RequestCancel(jobID int64) error {
	if jobID <= 0 {
		return errors.New("invalid job id")
	}
	res, err := s.db.Exec(
		`UPDATE scan_jobs SET cancel_requested=1 WHERE id=? AND status IN (?,?)`,
		jobID, statusQueued, statusRunning,
	)
	if err != nil {
		return err
	}
	changed, _ := res.RowsAffected()
	if changed > 0 {
		s.cancelJobIfRunning(jobID)
		return nil
	}
	var one int
	if err := s.db.QueryRow(`SELECT 1 FROM scan_jobs WHERE id=?`, jobID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrJobNotFound
		}
		return err
	}
	return ErrJobNotCancel
}

func (s *Service) worker() {
	for req := range s.queue {
		s.runJob(req)
	}
}

func (s *Service) runJob(req JobRequest) {
	if req.JobID <= 0 {
		return
	}
	if s.cancelRequested(req.JobID) {
		s.finishJob(req.JobID, statusCanceled, "", time.Now().UTC())
		return
	}

	startedAt := time.Now().UTC()
	_, _ = s.db.Exec(
		`UPDATE scan_jobs SET status=?, error='', started_at=?, ended_at='', duration_ms=0 WHERE id=?`,
		statusRunning, startedAt.Format(time.RFC3339Nano), req.JobID,
	)

	ctx, cancel := context.WithCancel(context.Background())
	s.registerCancel(req.JobID, cancel)
	defer func() {
		s.unregisterCancel(req.JobID)
		cancel()
	}()
	// Close the cancel race: RequestCancel sets cancel_requested=1 in the DB
	// *before* poking the in-memory cancel func, so a cancel landing between
	// the status='running' UPDATE above and registerCancel found no func to
	// poke (lost signal — the executor would run to completion). Re-checking
	// the flag once after registering covers that window completely.
	if s.cancelRequested(req.JobID) {
		cancel()
	}
	// Isolate executor panics: an unrecovered panic here would kill the only
	// worker goroutine and silently freeze the queue. Registered after the
	// cancel defer so it runs first (LIFO) and marks the job failed before
	// the cancel cleanup fires.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("job executor panicked",
				"job_id", req.JobID, "panic", r, "stack", string(debug.Stack()))
			s.finishJob(req.JobID, statusFailed, fmt.Sprintf("panic: %v", r), startedAt)
		}
	}()

	err := req.Executor(ctx, req.JobID, req.LibraryID)
	if err != nil {
		if errors.Is(err, context.Canceled) || s.cancelRequested(req.JobID) {
			s.finishJob(req.JobID, statusCanceled, "", startedAt)
			return
		}
		s.finishJob(req.JobID, statusFailed, err.Error(), startedAt)
		return
	}
	if s.cancelRequested(req.JobID) {
		s.finishJob(req.JobID, statusCanceled, "", startedAt)
		return
	}
	s.finishJob(req.JobID, statusDone, "", startedAt)
}

func (s *Service) finishJob(jobID int64, status, errText string, startedAt time.Time) {
	endedAt := time.Now().UTC()
	durationMS := int64(0)
	if !startedAt.IsZero() {
		durationMS = endedAt.Sub(startedAt).Milliseconds()
		if durationMS < 0 {
			durationMS = 0
		}
	}
	if _, err := s.db.Exec(
		`UPDATE scan_jobs SET status=?, error=?, duration_ms=?, ended_at=? WHERE id=?`,
		status, errText, durationMS, endedAt.Format(time.RFC3339Nano), jobID,
	); err != nil {
		slog.Error("job finish update failed", "job_id", jobID, "err", err)
	}
}

func (s *Service) cancelRequested(jobID int64) bool {
	var v int
	if err := s.db.QueryRow(`SELECT cancel_requested FROM scan_jobs WHERE id=?`, jobID).Scan(&v); err != nil {
		return false
	}
	return v != 0
}

func (s *Service) registerCancel(jobID int64, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancels[jobID] = cancel
}

func (s *Service) unregisterCancel(jobID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cancels, jobID)
}

func (s *Service) cancelJobIfRunning(jobID int64) {
	s.mu.Lock()
	cancel := s.cancels[jobID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

package jobs

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"
)

var (
	ErrJobNotFound  = errors.New("job not found")
	ErrJobNotCancel = errors.New("job is not cancelable")
	ErrQueueFull    = errors.New("job queue is full")
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

func New(db *sql.DB) *Service {
	s := &Service{
		db:      db,
		queue:   make(chan JobRequest, 128),
		cancels: make(map[int64]context.CancelFunc),
	}
	go s.worker()
	return s
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
		) VALUES (?, ?, 'queued', 0, 0, '', ?, 0, 0, ?, '', '', '')`,
		libraryID, jobType, createdBy, now,
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
			`UPDATE scan_jobs SET status='failed', error=?, ended_at=?, duration_ms=0 WHERE id=?`,
			ErrQueueFull.Error(), now, jobID,
		)
		return 0, ErrQueueFull
	}
}

func (s *Service) RequestCancel(jobID int64) error {
	if jobID <= 0 {
		return errors.New("invalid job id")
	}
	res, err := s.db.Exec(
		`UPDATE scan_jobs SET cancel_requested=1 WHERE id=? AND status IN ('queued','running')`,
		jobID,
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
		s.finishJob(req.JobID, "canceled", "", time.Now().UTC())
		return
	}

	startedAt := time.Now().UTC()
	_, _ = s.db.Exec(
		`UPDATE scan_jobs SET status='running', error='', started_at=?, ended_at='', duration_ms=0 WHERE id=?`,
		startedAt.Format(time.RFC3339Nano), req.JobID,
	)

	ctx, cancel := context.WithCancel(context.Background())
	s.registerCancel(req.JobID, cancel)
	defer func() {
		s.unregisterCancel(req.JobID)
		cancel()
	}()

	err := req.Executor(ctx, req.JobID, req.LibraryID)
	if err != nil {
		if errors.Is(err, context.Canceled) || s.cancelRequested(req.JobID) {
			s.finishJob(req.JobID, "canceled", "", startedAt)
			return
		}
		s.finishJob(req.JobID, "failed", err.Error(), startedAt)
		return
	}
	if s.cancelRequested(req.JobID) {
		s.finishJob(req.JobID, "canceled", "", startedAt)
		return
	}
	s.finishJob(req.JobID, "done", "", startedAt)
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

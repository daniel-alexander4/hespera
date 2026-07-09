package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

var (
	ErrJobNotFound  = errors.New("job not found")
	ErrJobNotCancel = errors.New("job is not cancelable")
	ErrQueueFull    = errors.New("job queue is full")
	// ErrYielded is a sentinel a long cosmetic job (trickplay/thumb) returns to
	// signal it stopped early to let a waiting interactive job run. The enqueue
	// wrapper (enqueueYielding) intercepts it, re-enqueues the job to finish its
	// remaining work, and reports the current run as done — it is never a
	// failure. Jobs are missing-only/incremental, so the re-run resumes cleanly.
	ErrYielded = errors.New("job yielded to interactive work")
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
	db        *sql.DB
	queue     chan JobRequest
	mu        sync.Mutex
	cancels   map[int64]context.CancelFunc
	enqueueMu sync.Mutex // serializes EnqueueUnique's check-then-insert

	// interruptedLibs is the distinct set of library ids whose jobs
	// reconcileStaleJobs marked "interrupted by restart" at startup — the input
	// to the boot auto-resume (the web layer re-kicks each library's scan
	// chain, which is incremental so it fast-forwards to the interrupted work).
	// Written once during New, read-only after.
	interruptedLibs []int64
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
// (a crash or restart) as 'canceled'. The in-memory queue is the only thing that
// holds runnable work, so those rows can never make progress on their own — left
// alone they hang in a non-terminal state forever and keep reporting cancelable.
// Marked 'canceled' (not 'failed') because the jobs are idempotent + incremental
// and a re-kick resumes losslessly — a restart is not an error, so it must not
// pollute the failed list. The "interrupted by restart" text disambiguates it
// from a user cancel. The affected libraries are recorded (interruptedLibs) so
// the boot auto-resume can perform that re-kick.
func (s *Service) reconcileStaleJobs() {
	// Collect which libraries the UPDATE below is about to touch. library_id=0
	// rows (per-entity metadata fetches) are excluded: they self-heal lazily on
	// the next page view via their fetched-markers, so a library re-kick has
	// nothing to resume for them.
	rows, err := s.db.Query(
		`SELECT DISTINCT library_id FROM scan_jobs WHERE status IN (?, ?) AND library_id > 0`,
		statusRunning, statusQueued,
	)
	if err != nil {
		slog.Warn("jobs reconcile stale: list libraries", "err", err)
	} else {
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				s.interruptedLibs = append(s.interruptedLibs, id)
			}
		}
		rows.Close()
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.Exec(
		`UPDATE scan_jobs SET status=?, error=?, ended_at=? WHERE status IN (?, ?)`,
		statusCanceled, "interrupted by restart", now, statusRunning, statusQueued,
	)
	if err != nil {
		slog.Warn("jobs reconcile stale", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("jobs reconciled stale rows to canceled", "count", n)
	}
}

// InterruptedLibraries returns the distinct library ids whose jobs the startup
// reconcile marked "interrupted by restart" — the libraries whose scan chains
// the boot auto-resume should re-kick. Populated once in New; safe to call
// concurrently thereafter.
func (s *Service) InterruptedLibraries() []int64 {
	return s.interruptedLibs
}

// cosmeticJobTypes are the low-priority "nice to have" tail jobs (screen-cap
// thumbnails, trickplay sprites, loudness) that a user never waits on to see or
// play content. HasQueuedInteractive treats everything NOT in this set as
// interactive, so these jobs yield to a waiting scan/match/probe/integrity.
var cosmeticJobTypes = []string{
	"tv_thumb", "photo_thumb", "tv_trickplay", "movie_trickplay", "music_loudness",
}

// HasQueuedInteractive reports whether any interactive (non-cosmetic) job is
// waiting in the queue. A long cosmetic job (trickplay/thumb) polls this and
// re-enqueues itself when it returns true, so a scan/match/probe queued behind
// it starts within one poll interval instead of after the whole sweep — without
// a second worker (the single-worker idle-priority model is preserved). Cosmetic
// jobs are excluded so two of them never ping-pong yielding to each other.
func (s *Service) HasQueuedInteractive() bool {
	placeholders := strings.Repeat(",?", len(cosmeticJobTypes))[1:]
	args := make([]any, 0, len(cosmeticJobTypes)+1)
	args = append(args, statusQueued)
	for _, t := range cosmeticJobTypes {
		args = append(args, t)
	}
	var exists int
	err := s.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM scan_jobs WHERE status=? AND job_type NOT IN (`+placeholders+`))`,
		args...,
	).Scan(&exists)
	return err == nil && exists == 1
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
	return s.enqueue(jobType, libraryID, createdBy, false, executor)
}

// EnqueueUnique is Enqueue with dedup: if a job of the same type for the same
// library is already QUEUED (not yet running), it returns that job's id instead
// of adding a duplicate. For full-library-idempotent scan-chain jobs (match /
// integrity / probe / thumb / trickplay / loudness) — re-triggering a scan while
// its chain is still pending must not pile up a second chain (the plex 3×tv_match
// case). NOT for per-entity jobs (metadata fetches, intro_detect) whose work isn't
// identified by (type, lib) — those carry their own key-based dedup. Deliberately
// dedups only against QUEUED, never RUNNING: a running job may have started before
// the change that prompted this enqueue, so a fresh queued job must still be
// allowed to catch it.
func (s *Service) EnqueueUnique(jobType string, libraryID int64, createdBy string, executor func(ctx context.Context, jobID, libraryID int64) error) (int64, error) {
	return s.enqueue(jobType, libraryID, createdBy, true, executor)
}

func (s *Service) enqueue(jobType string, libraryID int64, createdBy string, dedup bool, executor func(ctx context.Context, jobID, libraryID int64) error) (int64, error) {
	if libraryID < 0 {
		return 0, errors.New("invalid library id")
	}
	if createdBy == "" {
		createdBy = "system"
	}

	if dedup {
		// Serialize check-then-insert so two concurrent EnqueueUnique of the same
		// (type, lib) can't both miss the other and double-insert.
		s.enqueueMu.Lock()
		defer s.enqueueMu.Unlock()
		var existing int64
		err := s.db.QueryRow(
			`SELECT id FROM scan_jobs WHERE job_type=? AND library_id=? AND status=? ORDER BY id LIMIT 1`,
			jobType, libraryID, statusQueued,
		).Scan(&existing)
		if err == nil {
			return existing, nil // a duplicate is already pending — reuse it
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)

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
	lowerWorkerPriority()
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

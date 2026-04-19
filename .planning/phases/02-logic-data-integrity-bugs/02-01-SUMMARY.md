---
phase: 02-logic-data-integrity-bugs
plan: 01
subsystem: database, api
tags: [sqlite, upsert, job-queue, tv-scanner, goroutine]

# Dependency graph
requires:
  - phase: 01-security-error-exposure
    provides: httpError/jsonErr helpers used in handlers_tv.go
provides:
  - TV identity upsert that preserves resolved/skipped rows on rescan
  - tvMatchApprove metadata fetch via job queue instead of detached goroutine
affects: [tv-scanner, tv-match, jobs]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "ON CONFLICT DO UPDATE with WHERE guard to skip user-curated rows"
    - "libraryID=0 sentinel for non-library-scoped jobs"

key-files:
  created: []
  modified:
    - internal/tvscan/scanner.go
    - internal/web/handlers_tv.go

key-decisions:
  - "Use WHERE clause on DO UPDATE (not application-level check) for atomicity"
  - "Use libraryID=0 as sentinel for show-level jobs not scoped to a library"

patterns-established:
  - "Conditional upsert: ON CONFLICT DO UPDATE ... WHERE status NOT IN (...) to protect user-curated data"
  - "Non-library jobs: Enqueue with libraryID=0 when operation is entity-level, not library-scoped"

requirements-completed: [BUG-01, ERR-03]

# Metrics
duration: 2min
completed: 2026-03-05
---

# Phase 2 Plan 1: Logic & Data Integrity Bugs Summary

**TV identity upsert guarded against overwriting resolved/skipped rows, and tvMatchApprove metadata fetch moved from detached goroutine to job queue**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-05T21:58:59Z
- **Completed:** 2026-03-05T22:00:48Z
- **Tasks:** 2
- **Files modified:** 2

## Accomplishments
- TV rescan no longer overwrites identity data for files with status 'resolved' or 'skipped', preserving manual curation
- tvMatchApprove metadata fetch is now tracked, cancelable, and visible in the job list instead of running as an untracked goroutine
- All packages build and vet cleanly with no regressions

## Task Commits

Each task was committed atomically:

1. **Task 1: Guard TV identity upsert against overwriting resolved/skipped rows** - `c0ae701` (fix)
2. **Task 2: Replace detached goroutine with job queue enqueue in tvMatchApprove** - `49ef1fd` (fix)

## Files Created/Modified
- `internal/tvscan/scanner.go` - Added WHERE clause to ON CONFLICT DO UPDATE to skip resolved/skipped identity rows
- `internal/web/handlers_tv.go` - Replaced go func() with h.jobs.Enqueue("tv_metadata_fetch", ...) for tracked background work

## Decisions Made
- Used SQL-level WHERE clause on DO UPDATE rather than an application-level pre-check, ensuring atomicity without race conditions
- Used libraryID=0 as a sentinel for the tv_metadata_fetch job since FetchShowMetadata is a show-level operation not scoped to any library

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- BUG-01 and ERR-03 are resolved; remaining plans in phase 2 can proceed
- No blockers or concerns

---
*Phase: 02-logic-data-integrity-bugs*
*Completed: 2026-03-05*

---
phase: 05-integration-test-coverage
plan: 02
subsystem: testing
tags: [tmdb, httptest, integration-test, tv-match, sqlite]

# Dependency graph
requires:
  - phase: 04-unit-test-coverage
    provides: test patterns and helpers established across codebase
provides:
  - RunTVMatch happy-path integration test (search, show fetch, season fetch, art downloads, metadata cache, identity resolution)
  - RunTVMatch partial-failure integration test (one failed group does not block others)
  - Testable tmdb.Client with apiBase/imgBase fields for httptest.Server injection
affects: []

# Tech tracking
tech-stack:
  added: []
  patterns: [httptest mock TMDB server, closed-channel rate limiter bypass, same-package struct construction for test injection]

key-files:
  created:
    - internal/tmdb/matcher_integration_test.go
  modified:
    - internal/tmdb/client.go

key-decisions:
  - "apiBase/imgBase as struct fields (not constructor params) to avoid changing NewClient signature"
  - "Closed channel for rate limiter bypass -- reading from closed chan returns zero value immediately and repeatedly"
  - "Reuse sampleSearchJSON/sampleShowJSON/sampleSeasonJSON from client_test.go as mock server fixtures"

patterns-established:
  - "TMDB mock server pattern: httptest.Server with mux routing by path prefix for API and image endpoints"
  - "Rate limiter bypass: close(make(chan time.Time)) for instant non-blocking reads in tests"

requirements-completed: [TEST-08]

# Metrics
duration: 3min
completed: 2026-03-06
---

# Phase 05 Plan 02: TMDB TV Match Integration Tests Summary

**RunTVMatch integration tests with mocked TMDB API covering full search-fetch-cache-art pipeline and partial failure resilience**

## Performance

- **Duration:** 3 min
- **Started:** 2026-03-06T01:45:13Z
- **Completed:** 2026-03-06T01:48:30Z
- **Tasks:** 2
- **Files modified:** 2

## Accomplishments
- Made tmdb.Client testable by adding apiBase/imgBase fields that default to production constants but can be overridden to point at httptest.Server
- Happy path integration test exercises the complete TV match pipeline: TMDB search, show fetch, season fetch, poster/backdrop/season-poster image downloads, show/season/episode metadata caching, and identity status update to "resolved"
- Partial failure test proves one failed show group (HTTP 500 on fetch) does not prevent other groups from being matched
- All tests complete in under 50ms with zero real HTTP calls

## Task Commits

Each task was committed atomically:

1. **Task 1: Add apiBase/imgBase fields to tmdb.Client** - `a54a5a3` (feat)
2. **Task 2: Write RunTVMatch integration tests** - `1860a04` (test)

## Files Created/Modified
- `internal/tmdb/client.go` - Added apiBase/imgBase fields, updated SearchTV/FetchTVShow/FetchTVSeason/DownloadImage to use instance fields instead of package consts
- `internal/tmdb/matcher_integration_test.go` - Two integration tests with openTestDB, mock TMDB server, and comprehensive DB/file assertions

## Decisions Made
- Used struct fields (apiBase/imgBase) rather than adding constructor parameters -- avoids changing the public NewClient API while enabling test injection via same-package struct construction
- Closed channel pattern for rate limiter bypass -- `close(make(chan time.Time))` returns zero value on every read, providing instant non-blocking behavior without any sleep or ticker overhead
- Reused existing sampleSearchJSON/sampleShowJSON/sampleSeasonJSON fixtures from client_test.go rather than duplicating test data

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- TMDB TV match pipeline now has integration test coverage
- All tests pass across the full codebase (go test ./...)

## Self-Check: PASSED

All files and commits verified:
- internal/tmdb/client.go: FOUND
- internal/tmdb/matcher_integration_test.go: FOUND
- 05-02-SUMMARY.md: FOUND
- Commit a54a5a3: FOUND
- Commit 1860a04: FOUND

---
*Phase: 05-integration-test-coverage*
*Completed: 2026-03-06*

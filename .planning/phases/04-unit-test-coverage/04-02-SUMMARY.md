---
phase: 04-unit-test-coverage
plan: 02
subsystem: testing
tags: [sqlite, tvscan, upsert, prune, regression]

requires:
  - phase: 02-logic-bugs
    provides: "upsertTVFile method and BUG-01 fix (WHERE status NOT IN resolved/skipped)"
provides:
  - "Unit tests for TV scanner upsertTVFile (insert, nil, conflict, rescan BUG-01)"
  - "Unit tests for TV scanner pruneMissingFiles (remove, preserve, outside-root)"
affects: [tvscan, tv-scanner]

tech-stack:
  added: []
  patterns: [openTestDB-in-tvscan, seedLibrary-helper]

key-files:
  created: [internal/tvscan/scanner_test.go]
  modified: []

key-decisions:
  - "Used same-package tests (package tvscan) to access unexported upsertTVFile/pruneMissingFiles"
  - "Each subtest gets its own fresh DB to avoid cross-test contamination"

patterns-established:
  - "seedLibrary(t, db, name, type, root) helper for inserting test library rows"

requirements-completed: [TEST-03]

duration: 3min
completed: 2026-03-05
---

# Phase 04 Plan 02: TV Scanner Tests Summary

**9 unit tests for upsertTVFile (insert/nil/conflict/BUG-01 rescan protection) and pruneMissingFiles (remove/preserve/outside-root)**

## Performance

- **Duration:** 3 min
- **Started:** 2026-03-05T23:48:35Z
- **Completed:** 2026-03-05T23:51:35Z
- **Tasks:** 1
- **Files modified:** 1

## Accomplishments
- Full coverage of upsertTVFile: insert, nil identity, conflict update, and BUG-01 rescan protection (resolved/skipped preserved, needs_fix overwritten)
- Full coverage of pruneMissingFiles: removes missing files, preserves existing files, ignores files outside root
- BUG-01 regression test explicitly verifies resolved files are not overwritten on rescan

## Task Commits

Each task was committed atomically:

1. **Task 1: TV scanner upsertTVFile and pruneMissingFiles tests** - `13a87a7` (test)

## Files Created/Modified
- `internal/tvscan/scanner_test.go` - 9 subtests covering upsert insert/nil/conflict/rescan and prune remove/preserve/outside-root

## Decisions Made
- Used same-package tests to access unexported methods directly (no export wrappers needed)
- Each subtest creates its own fresh DB via openTestDB to avoid shared state

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

Pre-existing build failure in `internal/web` (undefined `newTestHandler`) -- not related to this plan's changes.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- TV scanner DB logic fully tested
- Ready for subsequent test coverage plans

---
*Phase: 04-unit-test-coverage*
*Completed: 2026-03-05*

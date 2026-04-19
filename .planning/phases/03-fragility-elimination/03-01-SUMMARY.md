---
phase: 03-fragility-elimination
plan: 01
subsystem: web
tags: [refactoring, url-parsing, deduplication, handlers]

requires:
  - phase: 01-security-error-exposure
    provides: httpError helper for error responses
provides:
  - pathID() and pathSegment() shared URL path extraction helpers
  - Deduplicated inline ID parsing across 7 handler call sites
affects: [web-handlers, routing]

tech-stack:
  added: []
  patterns: [shared-url-parsing-helpers]

key-files:
  created:
    - internal/web/helpers.go
    - internal/web/helpers_test.go
  modified:
    - internal/web/handlers_music.go
    - internal/web/handlers_tv.go

key-decisions:
  - "pathID returns error (not bool) for descriptive failure context"
  - "pathSegment returns empty string for missing segments, matching existing guard patterns"

patterns-established:
  - "URL path ID extraction via pathID(r, prefix) instead of inline TrimPrefix+Clean+ParseInt"
  - "URL path string extraction via pathSegment(r, prefix) for non-numeric path segments"

requirements-completed: [FRAG-01]

duration: 4min
completed: 2026-03-05
---

# Phase 3 Plan 1: Path ID Helpers Summary

**Shared pathID/pathSegment helpers replace 7 duplicated TrimPrefix+Clean+ParseInt blocks across music and TV handlers**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-05T23:10:42Z
- **Completed:** 2026-03-05T23:15:26Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments
- Created pathID() helper extracting positive int64 from URL paths with traversal sanitization
- Created pathSegment() helper extracting sanitized string segments from URL paths
- Replaced all 7 inline ID-parsing blocks (5 in handlers_music.go, 2 in handlers_tv.go)
- Comprehensive table-driven tests covering valid, zero, negative, non-numeric, empty, and traversal cases
- TV art multi-segment handler intentionally left unchanged per plan

## Task Commits

Each task was committed atomically:

1. **Task 1: Create pathID and pathSegment helpers with tests** - `aad0a61` (test), `8ab327c` (feat) - TDD RED/GREEN
2. **Task 2: Replace all inline ID parsing with pathID/pathSegment calls** - `69b9ed7` (refactor)

_Note: TDD task has two commits (test then implementation)_

## Files Created/Modified
- `internal/web/helpers.go` - pathID and pathSegment helper functions
- `internal/web/helpers_test.go` - Table-driven tests for both helpers
- `internal/web/handlers_music.go` - 5 call sites replaced with pathID
- `internal/web/handlers_tv.go` - 1 pathID + 1 pathSegment call site replaced, unused "path" import removed

## Decisions Made
- pathID returns (int64, error) rather than (int64, bool) for descriptive failure context
- pathSegment returns empty string for missing segments, preserving existing guard patterns in callers

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

The handlers_music.go inline blocks had already been partially replaced by a prior 03-02 plan execution (commit 4bf94e9). The edits applied cleanly since the file state matched expectations. No functional impact.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- pathID/pathSegment helpers available for any future handler URL parsing
- All handler deduplication complete, ready for remaining fragility elimination plans

## Self-Check: PASSED

All 4 files verified present. All 3 commit hashes verified in git log.

---
*Phase: 03-fragility-elimination*
*Completed: 2026-03-05*

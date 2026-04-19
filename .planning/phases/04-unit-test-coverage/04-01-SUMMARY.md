---
phase: 04-unit-test-coverage
plan: 01
subsystem: testing
tags: [sqlite, scanner, compilation, tdd, unit-test]

requires:
  - phase: 02-logic-bugs
    provides: "finalizeCompilations compilation detection and merge logic"
provides:
  - "Unit tests for ensureArtist, ensureAlbum, ScanFile"
  - "Unit tests for finalizeCompilations (multi-artist, merge, idempotency)"
  - "Test helpers: openTestDB, seedLibrary, seedArtist, seedAlbum, seedTrack, writeMinimalMP3"
affects: [04-unit-test-coverage]

tech-stack:
  added: []
  patterns: [minimal-mp3-fixture-generation, seed-helper-pattern]

key-files:
  created:
    - internal/scan/scanner_test.go
    - internal/scan/compilation_test.go
  modified:
    - internal/scan/scanner.go

key-decisions:
  - "Minimal ID3v2.3 MP3 fixture generated in-test via writeMinimalMP3 helper (no external fixture files)"
  - "Each compilation subtest uses isolated DB (fresh openTestDB) to prevent cross-test contamination"
  - "Fixed UNIQUE constraint bug in finalizeCompilations variant merge (skip already-merged albums)"

patterns-established:
  - "seedArtist/seedAlbum/seedTrack helpers for scan package test data setup"
  - "writeMinimalMP3 for generating valid MP3 fixtures in tests"

requirements-completed: [TEST-01, TEST-02]

duration: 4min
completed: 2026-03-05
---

# Phase 4 Plan 1: Music Scanner Tests Summary

**Unit tests for ScanFile, ensureArtist/ensureAlbum DB helpers, and finalizeCompilations compilation detection with variant merge -- plus auto-fix for UNIQUE constraint bug on variant album merge**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-05T23:48:35Z
- **Completed:** 2026-03-05T23:52:35Z
- **Tasks:** 2
- **Files modified:** 3

## Accomplishments
- 12 passing subtests covering all scanner DB helpers and ScanFile integration
- 5 passing subtests covering compilation detection, single-artist preservation, variant merge, already-marked stability, and rescan consistency
- Discovered and fixed a real UNIQUE constraint crash when merging variant albums with same title+year
- Minimal MP3 fixture generator works with dhowden/tag (no external test fixtures needed)

## Task Commits

Each task was committed atomically:

1. **Task 1: Music scanner DB helper and ScanFile tests** - `5ef690d` (test)
2. **Task 2: Compilation detection and album merge tests** - `cf0d11c` (test + fix)

## Files Created/Modified
- `internal/scan/scanner_test.go` - Tests for ensureArtist, ensureAlbum, ScanFile with openTestDB/seedLibrary/writeMinimalMP3 helpers
- `internal/scan/compilation_test.go` - Tests for finalizeCompilations with seedArtist/seedAlbum/seedTrack helpers
- `internal/scan/scanner.go` - Bug fix: skip already-merged albums in finalizeCompilations loop

## Decisions Made
- Used in-test MP3 fixture generation (writeMinimalMP3) rather than checked-in fixture files -- keeps tests self-contained
- Each compilation subtest gets its own fresh DB to prevent cross-test state leakage
- Fixed variant merge bug inline (Rule 1) rather than deferring -- the test correctly exposed a real crash path

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] UNIQUE constraint crash in finalizeCompilations variant merge**
- **Found during:** Task 2 (compilation tests)
- **Issue:** When two albums with same title+year both have multi-artist tracks, finalizeCompilations tries to set both albums' artist_id to "Various Artists". The second UPDATE hits UNIQUE constraint (library_id, artist_id, title, year) because the first album was already updated to VA.
- **Fix:** Added track-count check before marking: if an album's tracks were already merged into another candidate earlier in the loop, skip it.
- **Files modified:** internal/scan/scanner.go
- **Verification:** All 5 compilation subtests pass; full test suite passes with no regressions
- **Committed in:** cf0d11c (part of Task 2 commit)

---

**Total deviations:** 1 auto-fixed (1 bug)
**Impact on plan:** Bug fix was necessary for test correctness and also fixes a real production crash path. No scope creep.

## Issues Encountered
None beyond the auto-fixed bug above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Scanner package now has comprehensive test coverage for core DB helpers and compilation logic
- Test helpers (openTestDB, seedLibrary, seedArtist, seedAlbum, seedTrack, writeMinimalMP3) available for reuse in subsequent scan-package test plans

## Self-Check: PASSED

- scanner_test.go: FOUND (353 lines, min 100)
- compilation_test.go: FOUND (267 lines, min 80)
- SUMMARY.md: FOUND
- Commit 5ef690d (Task 1): FOUND
- Commit cf0d11c (Task 2): FOUND

---
*Phase: 04-unit-test-coverage*
*Completed: 2026-03-05*

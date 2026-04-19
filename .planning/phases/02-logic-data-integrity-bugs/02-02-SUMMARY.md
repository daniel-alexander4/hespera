---
phase: 02-logic-data-integrity-bugs
plan: 02
subsystem: scan
tags: [compilation-detection, album-merge, scanner, sqlite]

# Dependency graph
requires:
  - phase: 01-security-error-exposure
    provides: ensureColumn validation, error sanitization
provides:
  - Post-scan compilation detection via artist diversity (order-independent)
  - Post-scan album variant merge (no mid-scan corruption)
  - Simplified ScanFile with tag-only compilation signals
affects: [scan, match]

# Tech tracking
tech-stack:
  added: []
  patterns: [post-scan finalization pass, DB-based method vs tx-based function]

key-files:
  created: []
  modified: [internal/scan/scanner.go]

key-decisions:
  - "Split ensureArtist into tx-based (ScanFile) and DB-based (finalizeCompilations) variants"
  - "Compilation detection uses COUNT(DISTINCT artist_id) > 1 query on full track set"

patterns-established:
  - "Post-scan finalization: heavy heuristics run after WalkDir completes, not per-file"
  - "ensureArtistDB: DB-connection variant of tx-scoped helpers for post-scan methods"

requirements-completed: [BUG-02, BUG-03]

# Metrics
duration: 3min
completed: 2026-03-05
---

# Phase 02 Plan 02: Compilation Detection & Merge Summary

**Post-scan finalizeCompilations pass replaces per-file compilation detection and album merge, fixing walk-order dependency (BUG-02) and mid-scan corruption risk (BUG-03)**

## Performance

- **Duration:** 3 min
- **Started:** 2026-03-05T21:58:56Z
- **Completed:** 2026-03-05T22:01:37Z
- **Tasks:** 2
- **Files modified:** 1

## Accomplishments
- Removed mid-scan `detectCompilationByArtistDiversity` and `mergeAlbumVariants` calls from ScanFile, eliminating filesystem walk-order dependency for compilation detection
- Added `finalizeCompilations` post-scan pass that queries the full track set for multi-artist albums, making results deterministic regardless of file processing order
- Album variant merging now runs once post-scan when all tracks are present, preventing orphaned tracks from mid-scan merge corruption

## Task Commits

Each task was committed atomically:

1. **Task 1: Remove mid-scan compilation detection and merge from ScanFile** - `d5b59ff` (fix)
2. **Task 2: Add post-scan finalizeCompilations pass** - `b4b9853` (feat)

## Files Created/Modified
- `internal/scan/scanner.go` - Simplified ScanFile to tag-only compilation signals; added finalizeCompilations + ensureArtistDB; removed dead detectCompilationByArtistDiversity and mergeAlbumVariants functions; wired post-scan pass into ScanMusic and ScanFiles

## Decisions Made
- Split artist lookup into tx-based `ensureArtist` (per-file transaction in ScanFile) and DB-based `ensureArtistDB` (post-scan finalizeCompilations) to avoid mixing transaction scopes
- Compilation detection query uses `COUNT(DISTINCT t.artist_id) > 1` subquery per album, which is efficient for typical library sizes and runs only once per scan

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Scanner compilation logic is now order-independent and safe from mid-scan corruption
- Ready for remaining logic/data integrity bug fixes in plans 02-03+

---
*Phase: 02-logic-data-integrity-bugs*
*Completed: 2026-03-05*

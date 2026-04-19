---
phase: 09-tv-match-threshold-and-status-alignment
plan: 01
subsystem: database, matching
tags: [sqlite, migration, tmdb, tv-matching, threshold]

# Dependency graph
requires: []
provides:
  - "TV match statuses aligned to matched/unmatched/skipped (consistent with music pipeline)"
  - "TV auto-match threshold raised from 0.45 to 0.80"
  - "DB migration converting existing resolved/needs_fix rows"
affects: [10-tv-match-test-coverage]

# Tech tracking
tech-stack:
  added: []
  patterns: ["table recreation migration for CHECK constraint changes"]

key-files:
  created: []
  modified:
    - internal/db/migrate.go
    - internal/tmdb/matcher.go
    - internal/tvscan/scanner.go
    - internal/web/handlers_tv.go
    - web/templates/tv_match_review.html
    - internal/tvscan/scanner_test.go
    - internal/tmdb/matcher_integration_test.go

key-decisions:
  - "Used table recreation pattern (matching existing migrateIdentitiesSkippedStatus) for CHECK constraint migration"
  - "Migration uses CASE expression to convert resolved->matched and needs_fix->unmatched in single pass"

patterns-established:
  - "TV status values: matched/unmatched/skipped -- aligned with music pipeline model"

requirements-completed: [MATCH-01, MATCH-02, MATCH-03]

# Metrics
duration: 4min
completed: 2026-03-07
---

# Phase 9 Plan 1: TV Match Threshold and Status Alignment Summary

**TV auto-match threshold raised to 0.80 with status rename from resolved/needs_fix to matched/unmatched across schema, matcher, scanner, handlers, templates, and tests**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-07T13:58:14Z
- **Completed:** 2026-03-07T14:02:04Z
- **Tasks:** 2
- **Files modified:** 7

## Accomplishments
- Raised TV auto-match confidence threshold from 0.45 to 0.80, ensuring only high-confidence matches are auto-accepted
- Renamed all TV status values from resolved/needs_fix to matched/unmatched, aligning with music pipeline conventions
- Added idempotent DB migration that recreates tv_series_identities table with new CHECK constraint and converts existing rows
- Updated all handler queries, template text, and test assertions for new status terminology

## Task Commits

Each task was committed atomically:

1. **Task 1: DB migration and core matcher/scanner status rename** - `f6f4d98` (feat)
2. **Task 2: Handler queries, template, and test updates** - `b057f0a` (feat)

## Files Created/Modified
- `internal/db/migrate.go` - New CHECK constraint (matched/unmatched/skipped), migrateIdentitiesMatchedUnmatched() function
- `internal/tmdb/matcher.go` - Threshold 0.45->0.80, status query/update uses 'unmatched'/'matched'
- `internal/tvscan/scanner.go` - New files inserted as 'unmatched', rescan preserves 'matched'/'skipped'
- `internal/web/handlers_tv.go` - All queries updated from resolved/needs_fix to matched/unmatched
- `web/templates/tv_match_review.html` - Empty state text updated to "matched or skipped"
- `internal/tvscan/scanner_test.go` - All status assertions updated to unmatched/matched
- `internal/tmdb/matcher_integration_test.go` - Seed data and assertions updated for new statuses

## Decisions Made
- Used table recreation pattern (matching existing migrateIdentitiesSkippedStatus) for CHECK constraint migration
- Migration uses CASE expression to convert resolved->matched and needs_fix->unmatched in a single INSERT...SELECT pass
- Idempotency check: if 'matched' already in CREATE SQL, migration is skipped

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- All TV status values aligned with music pipeline model
- Phase 10 (TV match test coverage) can build on this foundation
- No blockers or concerns

---
*Phase: 09-tv-match-threshold-and-status-alignment*
*Completed: 2026-03-07*

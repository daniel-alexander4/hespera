---
phase: 07-automated-writeback
plan: 01
subsystem: match
tags: [musicbrainz, writeback, tag-writing, pipeline, normalization]

# Dependency graph
requires:
  - phase: 06-auto-match-pipeline
    provides: "matchAlbum with auto-accept threshold >= 80"
provides:
  - "Inline tag writeback after auto-match (writebackAlbumTracks)"
  - "Name normalization in DB to MusicBrainz canonical values"
  - "Complete scan -> match -> writeback pipeline in one pass"
affects: [08-ui-polish]

# Tech tracking
tech-stack:
  added: []
  patterns: ["inline writeback scoped to single album", "non-fatal name normalization with slog warnings"]

key-files:
  created: []
  modified:
    - internal/match/pipeline.go
    - internal/match/writeback.go
    - internal/match/pipeline_integration_test.go

key-decisions:
  - "writebackAlbumTracks is an unexported package-level function (not a Matcher method) taking db and albumID for testability"
  - "Name normalization and writeback errors are non-fatal (logged, not returned) to avoid blocking successful matches"
  - "Inline writeback runs AFTER cover art fetch to ensure correct execution order"

patterns-established:
  - "Per-album writeback: focused function scoped to single album ID, separate from full-library RunTagWriteback"
  - "DB-first then file: normalize names in DB, then writebackAlbumTracks reads normalized names from DB for file tags"

requirements-completed: [WRITE-01, WRITE-02, WRITE-03]

# Metrics
duration: 3min
completed: 2026-03-06
---

# Phase 7 Plan 01: Automated Writeback Summary

**Inline tag writeback wired into match pipeline: auto-matched albums get MBIDs and normalized metadata written to file tags in one pass**

## Performance

- **Duration:** 3 min
- **Started:** 2026-03-07T00:29:35Z
- **Completed:** 2026-03-07T00:33:28Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments
- matchAlbum() now normalizes album title and artist name to MusicBrainz canonical values in the database
- writebackAlbumTracks() writes MBIDs and corrected metadata to audio file tags for a single album inline
- Complete pipeline flow: match -> normalize names in DB -> fetch cover art -> writeback tags to files
- Manual writeback (RunTagWriteback) remains unchanged for full-library manual writeback use case

## Task Commits

Each task was committed atomically:

1. **Task 1 (RED): Add failing tests** - `ebfdc3f` (test)
2. **Task 1 (GREEN): Normalize names and inline writeback** - `e5fe0bc` (feat)
3. **Task 2: Verify full pipeline** - `35f75e7` (chore)

_TDD task had separate RED and GREEN commits._

## Files Created/Modified
- `internal/match/pipeline.go` - Added name normalization (album title, artist name) and inline writebackAlbumTracks call to matchAlbum()
- `internal/match/writeback.go` - Added writebackAlbumTracks() for per-album tag writing scoped to single album ID
- `internal/match/pipeline_integration_test.go` - Added 3 tests: normalization, inline writeback, below-threshold no-normalization
- `internal/match/musicbrainz.go` - Pre-existing go fmt alignment fix

## Decisions Made
- writebackAlbumTracks is an unexported package-level function (not a Matcher method) for simplicity and testability -- takes db and albumID directly
- Name normalization errors are non-fatal (slog.Warn) to avoid blocking a successful match -- the match itself is more important than the name update
- Execution order in matchAlbum: (1) update match_status/MBIDs, (2) normalize album title, (3) normalize artist name, (4) cover art, (5) writebackAlbumTracks -- ensures writeback reads the normalized data

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Full automated pipeline complete: scan -> match -> writeback in one pass
- Manual writeback button (RunTagWriteback) ready for Phase 8 UI polish
- All requirements (WRITE-01, WRITE-02, WRITE-03) satisfied

---
*Phase: 07-automated-writeback*
*Completed: 2026-03-06*

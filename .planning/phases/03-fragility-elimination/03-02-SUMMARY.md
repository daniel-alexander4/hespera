---
phase: 03-fragility-elimination
plan: 02
subsystem: web
tags: [go, templates, fail-fast, error-handling, startup-validation]

# Dependency graph
requires:
  - phase: 01-security-error-exposure
    provides: "httpError/jsonErr error handling pattern"
provides:
  - "New() returns (*Handler, error) with template validation"
  - "Multi-error aggregation for page template failures"
  - "Fail-fast startup on broken/missing templates"
affects: [04-test-coverage]

# Tech tracking
tech-stack:
  added: []
  patterns: ["fail-fast constructor returning error", "errors.Join multi-error aggregation", "post-compilation template validation"]

key-files:
  created:
    - internal/web/handler_test.go
  modified:
    - internal/web/handler.go
    - cmd/isomedia/main.go
    - internal/web/handlers_music.go

key-decisions:
  - "Use os.Chdir in tests rather than refactoring New() to accept template dir parameter"
  - "Collect all page errors via errors.Join rather than failing on first broken page"
  - "Post-loop validation catches any pages silently skipped during compilation"

patterns-established:
  - "Fail-fast constructor: New() returns error on startup misconfiguration"
  - "Multi-error collection: errors.Join for reporting all failures at once"

requirements-completed: [FRAG-02, FRAG-03]

# Metrics
duration: 3min
completed: 2026-03-05
---

# Phase 3 Plan 2: Template Fail-Fast Summary

**New() returns (*Handler, error) with multi-error template validation -- missing or broken templates prevent startup with descriptive error listing all failures**

## Performance

- **Duration:** 3 min
- **Started:** 2026-03-05T23:10:49Z
- **Completed:** 2026-03-05T23:13:53Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments
- Changed New() from silent-degradation to fail-fast with descriptive errors
- Removed fallback layout template entirely -- layout failure is now fatal
- Page parse failures collected into multi-error listing ALL broken pages (not just first)
- Post-loop validation ensures every page in the pages slice has a compiled template
- main.go handles New() error with slog.Error + os.Exit(1), matching existing pattern
- Full test coverage: valid templates, missing layout, broken layout, missing single page, multiple broken pages

## Task Commits

Each task was committed atomically:

1. **Task 1 RED: Failing tests for template validation** - `bfe2350` (test)
2. **Task 1 GREEN: Make template compilation fail fast** - `4bf94e9` (feat)
3. **Task 2: Handle New() error in main.go** - `2ead082` (feat)

_Note: TDD task had RED and GREEN commits_

## Files Created/Modified
- `internal/web/handler.go` - New() returns (*Handler, error), removed fallback layout, multi-error page collection, post-loop validation
- `internal/web/handler_test.go` - 5 tests covering all template validation paths
- `cmd/isomedia/main.go` - Updated web.New() call to handle error return
- `internal/web/handlers_music.go` - Removed unused "path" import (blocking fix)

## Decisions Made
- Used os.Chdir in tests to handle template loading relative to CWD, rather than refactoring New() to accept a template directory parameter (simpler, avoids API change)
- Collected all page errors via errors.Join multi-error rather than failing on first broken page, so developers see all issues at once
- Added post-loop validation as defense-in-depth: even if a page somehow passes compilation without error but is missing from tpls map, it will be caught

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Removed unused "path" import in handlers_music.go**
- **Found during:** Task 1 (template validation implementation)
- **Issue:** Pre-existing dirty working tree had removed usages of "path" package but left the import, preventing web package from compiling
- **Fix:** Removed unused `"path"` import
- **Files modified:** internal/web/handlers_music.go
- **Verification:** go vet ./internal/web passes
- **Committed in:** 4bf94e9 (Task 1 GREEN commit)

**2. [Rule 1 - Bug] Fixed test broken template syntax**
- **Found during:** Task 1 TDD GREEN phase
- **Issue:** Initial broken template syntax `{{ .Foo | }}}` was actually valid Go template syntax and parsed successfully
- **Fix:** Changed to `{{ end ` (unclosed action) which correctly fails to parse
- **Verification:** TestNewBrokenLayout passes
- **Committed in:** 4bf94e9 (Task 1 GREEN commit)

---

**Total deviations:** 2 auto-fixed (1 blocking, 1 bug)
**Impact on plan:** Both fixes necessary for correctness. No scope creep.

## Issues Encountered
None beyond the auto-fixed deviations above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Template fail-fast pattern established for all future template work
- Ready for remaining Phase 3 plans (scanner robustness, etc.)

## Self-Check: PASSED

- All 4 files verified present on disk
- All 3 task commits verified in git log (bfe2350, 4bf94e9, 2ead082)
- Build, vet, and full test suite pass

---
*Phase: 03-fragility-elimination*
*Completed: 2026-03-05*

---
phase: 01-security-error-exposure
plan: 02
subsystem: web
tags: [slog, error-handling, security, http-handlers]

# Dependency graph
requires:
  - phase: 01-security-error-exposure
    provides: "01-01 established slog structured logging pattern"
provides:
  - "httpError() and jsonErr() helper functions for sanitized error responses"
  - "Zero err.Error() leaks in any HTTP handler file"
  - "All handler errors logged server-side via slog"
  - "Fixed template name leak in render()"
  - "Fixed MediaRoot path leak in librariesNew"
affects: [web-handlers, error-handling, security]

# Tech tracking
tech-stack:
  added: []
  patterns: [httpError-helper-pattern, jsonErr-helper-pattern, slog-based-error-logging]

key-files:
  created: []
  modified:
    - internal/web/handlers_core.go
    - internal/web/handler.go
    - internal/web/handlers_music.go
    - internal/web/handlers_tv.go
    - internal/web/handlers_match.go
    - internal/web/handlers_settings.go

key-decisions:
  - "httpError/jsonErr signature is (w, code, msg, logMsg, attrs...) -- code second for readability"
  - "5xx errors logged via slog.Error, 4xx via slog.Warn -- severity-appropriate"
  - "Safe descriptive error messages (invalid id, title and artist required, etc.) preserved unchanged"
  - "MediaRoot path replaced with generic message -- user already knows root from UI"

patterns-established:
  - "httpError pattern: log real error via slog, return generic message to client"
  - "jsonErr pattern: same as httpError but for JSON API responses"
  - "Handler context in log attrs: include handler name for traceability"

requirements-completed: [SEC-02, ERR-01, ERR-02]

# Metrics
duration: 9min
completed: 2026-03-05
---

# Phase 1 Plan 2: Error Response Sanitization Summary

**httpError/jsonErr helpers eliminate 109+ raw error leaks across all handlers, with server-side slog logging and generic client messages**

## Performance

- **Duration:** 9 min
- **Started:** 2026-03-05T20:20:42Z
- **Completed:** 2026-03-05T20:29:54Z
- **Tasks:** 2
- **Files modified:** 6

## Accomplishments
- Created httpError() and jsonErr() helper functions that log real errors via slog and return generic messages to clients
- Replaced all 109+ instances of err.Error() passed to http.Error/jsonError across 6 handler files
- Fixed template name leak in render() and MediaRoot path leak in librariesNew
- All safe/descriptive user-facing error messages preserved unchanged

## Task Commits

Each task was committed atomically:

1. **Task 1: Create httpError and jsonErr helper functions** - `c059ba0` (feat)
2. **Task 2: Replace all err.Error() leaks across handler files** - `534fd59` (fix)

## Files Created/Modified
- `internal/web/handlers_core.go` - Added httpError() and jsonErr() helpers; sanitized 3 auth handler error paths
- `internal/web/handler.go` - Fixed template name leak in render() error response
- `internal/web/handlers_music.go` - Replaced 47 err.Error() leaks with httpError calls
- `internal/web/handlers_tv.go` - Replaced 23 err.Error() leaks with httpError calls
- `internal/web/handlers_match.go` - Replaced 16 err.Error() leaks with httpError calls
- `internal/web/handlers_settings.go` - Replaced 14 http.Error + 6 jsonError leaks; fixed MediaRoot path exposure

## Decisions Made
- httpError/jsonErr use slog.Error for 5xx and slog.Warn for 4xx to maintain severity-appropriate logging
- Handler name included as slog attr for traceability (e.g., "handler", "musicHome")
- Log messages describe the operation that failed (e.g., "db query failed", "parse form failed") to supplement the error value
- MediaRoot replaced with static string "root_path must be under the configured media root" since user knows the root from UI

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- All HTTP error responses now return generic messages to clients
- All errors logged server-side via slog with handler context
- Zero instances of err.Error() in any handler file
- Ready for Phase 2 (logic bug fixes)

## Self-Check: PASSED

All 6 modified files verified on disk. Both task commits (c059ba0, 534fd59) verified in git log.

---
*Phase: 01-security-error-exposure*
*Completed: 2026-03-05*

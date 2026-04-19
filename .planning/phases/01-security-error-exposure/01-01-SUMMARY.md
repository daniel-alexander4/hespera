---
phase: 01-security-error-exposure
plan: 01
subsystem: database
tags: [sqlite, sql-injection, validation, regexp, security]

# Dependency graph
requires: []
provides:
  - safeIdentifier regexp validation for DDL identifier injection prevention
  - ensureColumn rejects unsafe table/column names before SQL execution
affects: [database, migrations]

# Tech tracking
tech-stack:
  added: []
  patterns: [compiled regexp identifier validation gate before string-concatenated DDL]

key-files:
  created: []
  modified:
    - internal/db/migrate.go
    - internal/db/db_test.go

key-decisions:
  - "Validate only table and col parameters, not decl (always hardcoded at call sites)"
  - "Package-level compiled regexp avoids per-call compilation overhead"

patterns-established:
  - "DDL identifier validation: gate string-concatenated SQL with safeIdentifier.MatchString before execution"

requirements-completed: [SEC-01]

# Metrics
duration: 1min
completed: 2026-03-05
---

# Phase 1 Plan 1: ensureColumn SQL Injection Prevention Summary

**Compiled regexp gate on ensureColumn rejecting SQL metacharacters in table/column names before DDL execution**

## Performance

- **Duration:** 1 min
- **Started:** 2026-03-05T20:20:34Z
- **Completed:** 2026-03-05T20:21:42Z
- **Tasks:** 1 (TDD: RED + GREEN)
- **Files modified:** 2

## Accomplishments
- Added `safeIdentifier` regexp (`^[a-zA-Z_][a-zA-Z0-9_]*$`) compiled at package level
- ensureColumn now returns descriptive error before any SQL execution for invalid identifiers
- 10-subtest table-driven test covering valid identifiers, SQL metacharacters, empty strings, and digit-prefixed names
- All existing tests (TestEnsureColumnIdempotent, TestMigrateIdempotent, TestOpenAndPing) continue to pass

## Task Commits

Each task was committed atomically:

1. **Task 1 RED: Add failing validation tests** - `800d0b6` (test)
2. **Task 1 GREEN: Implement safeIdentifier validation** - `543e4f9` (feat)

_TDD task: test commit followed by implementation commit._

## Files Created/Modified
- `internal/db/migrate.go` - Added safeIdentifier regexp and validation at ensureColumn entry
- `internal/db/db_test.go` - Added TestEnsureColumnValidation with 10 subtests

## Decisions Made
- Validate only table and col parameters, not decl -- decl contains SQL type declarations (e.g. "TEXT NOT NULL DEFAULT ''") and is always hardcoded at call sites
- Package-level compiled regexp avoids per-call compilation overhead
- Error messages include the invalid identifier quoted with %q for debuggability

## Deviations from Plan

None -- plan executed exactly as written.

## Issues Encountered
None.

## User Setup Required
None -- no external service configuration required.

## Next Phase Readiness
- ensureColumn is now hardened against identifier injection
- Ready for plan 01-02 (remaining security/error exposure work)

## Self-Check: PASSED

- FOUND: internal/db/migrate.go
- FOUND: internal/db/db_test.go
- FOUND: 01-01-SUMMARY.md
- FOUND: commit 800d0b6
- FOUND: commit 543e4f9

---
*Phase: 01-security-error-exposure*
*Completed: 2026-03-05*

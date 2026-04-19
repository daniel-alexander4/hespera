---
phase: 01-security-error-exposure
verified: 2026-03-05T21:00:00Z
status: passed
score: 10/10 must-haves verified
re_verification: false
---

# Phase 1: Security & Error Exposure Verification Report

**Phase Goal:** No internal implementation details leak to HTTP clients, and SQL construction is safe from injection
**Verified:** 2026-03-05T21:00:00Z
**Status:** passed
**Re-verification:** No -- initial verification

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | ensureColumn rejects table names containing SQL metacharacters (spaces, semicolons, quotes, parens, dashes) | VERIFIED | TestEnsureColumnValidation passes all 10 subtests including reject_table_with_space, reject_table_with_semicolon, reject_table_with_parens |
| 2 | ensureColumn rejects column names containing SQL metacharacters | VERIFIED | TestEnsureColumnValidation/reject_column_with_dash and reject_column_with_quotes pass |
| 3 | ensureColumn accepts valid identifiers (alphanumeric + underscores, starting with letter or underscore) | VERIFIED | TestEnsureColumnValidation/valid_table_and_column and valid_underscored_names pass |
| 4 | ensureColumn returns an error before any SQL is executed when given an invalid identifier | VERIFIED | safeIdentifier.MatchString checks at lines 308-313 of migrate.go precede any SQL execution at lines 314 and 334 |
| 5 | No HTTP response body contains a raw Go error string, SQL error text, or filesystem path | VERIFIED | Zero instances of err.Error() passed to http.Error or jsonError across all handler files. Grep confirms 0 matches |
| 6 | Every handler error path logs the real error via slog before returning a generic message | VERIFIED | 100 httpError calls and 9 jsonErr calls across handler files -- both helpers log via slog internally before sending response |
| 7 | User-facing validation messages (e.g. 'invalid id', 'name, type, root_path are required') are preserved unchanged | VERIFIED | All safe static messages remain as direct http.Error calls: "invalid id", "title and artist are required", "name, type, root_path are required", "scanning not supported for this library type", etc. |
| 8 | JSON error responses (jsonError paths) receive the same sanitization treatment as HTML error responses | VERIFIED | 9 jsonErr calls across handlers_core.go (3) and handlers_settings.go (6) -- all use generic messages with slog logging |
| 9 | The render() template-name leak in handler.go is fixed | VERIFIED | handler.go:151 now returns "internal server error" (not template name). Grep for "template not found:" in error responses returns 0 matches. slog.Error on line 150 still logs the page name server-side |
| 10 | The MediaRoot path leak in handlers_settings.go is fixed | VERIFIED | Line 218 reads "root_path must be under the configured media root" -- no actual path value in message. MediaRoot is only used in comparison logic (line 217) and template data (lines 189, 198) |

**Score:** 10/10 truths verified

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `internal/db/migrate.go` | safeIdentifier regexp and validation at ensureColumn entry | VERIFIED | Line 305: `var safeIdentifier = regexp.MustCompile(...)`, Lines 308-313: validation before SQL |
| `internal/db/db_test.go` | TestEnsureColumnValidation tests | VERIFIED | Lines 102-214: 10-subtest table-driven test covering valid identifiers, SQL metacharacters, empty strings, digit-prefixed names |
| `internal/web/handlers_core.go` | httpError and jsonErr helper functions | VERIFIED | Lines 125-141: both functions with slog.Error for 5xx, slog.Warn for 4xx |
| `internal/web/handler.go` | Fixed render() -- no template name in response | VERIFIED | Lines 151 and 158 both return "internal server error" |
| `internal/web/handlers_music.go` | All err.Error() replaced with httpError | VERIFIED | 47 httpError calls, 0 err.Error() leaks |
| `internal/web/handlers_tv.go` | All err.Error() replaced with httpError | VERIFIED | 23 httpError calls, 0 err.Error() leaks |
| `internal/web/handlers_match.go` | All err.Error() replaced with httpError | VERIFIED | 16 httpError calls, 0 err.Error() leaks |
| `internal/web/handlers_settings.go` | All err.Error() + jsonError(err.Error()) replaced, MediaRoot leak fixed | VERIFIED | 14 httpError + 6 jsonErr calls, 0 err.Error() leaks, MediaRoot error message uses generic text |

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|----|--------|---------|
| `internal/web/handlers_*.go` | `internal/web/handlers_core.go` | httpError() and jsonErr() calls | WIRED | 100 httpError calls + 9 jsonErr calls across 4 handler files confirmed by grep |
| `internal/web/handlers_core.go` | `log/slog` | slog.Error/slog.Warn in helper functions | WIRED | Lines 127-130 (httpError) and 136-139 (jsonErr) call slog.Error for 5xx, slog.Warn for 4xx |
| `internal/db/migrate.go` | ensureColumn callers | error return before SQL concat | WIRED | safeIdentifier.MatchString validation at lines 308-313 returns error before SQL at lines 314 and 334 |

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|------------|-------------|--------|----------|
| SEC-01 | 01-01 | ensureColumn validates table and column names against a safe pattern before building SQL | SATISFIED | safeIdentifier regexp at line 305, validation at lines 308-313, 10 test subtests all pass |
| SEC-02 | 01-02 | HTTP error responses never contain SQL error text, filesystem paths, or internal Go error details | SATISFIED | Zero err.Error() in any http.Error/jsonError call. All error responses use static generic messages |
| ERR-01 | 01-02 | All HTTP handlers return generic user-facing error messages instead of raw Go error strings | SATISFIED | 109 instances replaced with httpError/jsonErr helpers that send "internal server error" or "bad request" |
| ERR-02 | 01-02 | All internal errors are logged server-side with slog before returning generic HTTP errors | SATISFIED | httpError and jsonErr helpers always call slog.Error (5xx) or slog.Warn (4xx) before sending response |

No orphaned requirements found. REQUIREMENTS.md maps SEC-01, SEC-02, ERR-01, ERR-02 to Phase 1, and all four are claimed by plans 01-01 and 01-02.

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| (none) | - | - | - | No anti-patterns found in any modified file |

No TODO, FIXME, XXX, HACK, or PLACEHOLDER comments found in any phase-modified file.

### Human Verification Required

### 1. Error Response Content Verification

**Test:** Trigger a server error (e.g., corrupt database, invalid template) and inspect the HTTP response body
**Expected:** Response body contains only "internal server error" -- no SQL text, no file paths, no Go error strings
**Why human:** Automated grep confirms no err.Error() in source, but runtime behavior with real errors confirms the full chain works

### 2. Validation Messages Preserved

**Test:** Submit the "New Library" form with missing fields, then with an invalid root_path
**Expected:** "name, type, root_path are required" for missing fields; "root_path must be under the configured media root" for bad path (no actual path value shown)
**Why human:** Confirms user-facing validation UX is unchanged and MediaRoot value does not appear

### Gaps Summary

No gaps found. All 10 observable truths verified. All 4 requirements satisfied. All artifacts exist, are substantive, and are wired. Zero err.Error() leaks remain. Full test suite passes. Project builds and vets clean.

### Build Verification

- `go build ./...` -- PASS
- `go test ./... -count=1` -- PASS (all packages)
- `go vet ./...` -- PASS
- `grep err.Error() internal/web/` -- 0 matches
- Commits verified: 800d0b6, 543e4f9, c059ba0, 534fd59

---

_Verified: 2026-03-05T21:00:00Z_
_Verifier: Claude (gsd-verifier)_

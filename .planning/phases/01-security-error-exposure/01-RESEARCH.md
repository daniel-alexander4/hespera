# Phase 1: Security & Error Exposure - Research

**Researched:** 2026-03-05
**Domain:** HTTP error sanitization, SQL injection prevention in schema evolution
**Confidence:** HIGH

## Summary

Phase 1 addresses two related security concerns in the isomedia codebase: (1) raw Go error strings leaking internal details (SQL errors, filesystem paths, stack traces) to HTTP clients via `http.Error(w, err.Error(), ...)` and `jsonError(w, err.Error(), ...)`, and (2) the `ensureColumn()` function in `internal/db/migrate.go` accepting arbitrary string arguments for table/column names and interpolating them directly into SQL statements.

The scope is well-defined and mechanical. There are exactly 100 instances of `http.Error(w, err.Error(), ...)` across 4 handler files and 9 instances of `jsonError(w, err.Error(), ...)` across 2 handler files. Only 2 of these 109 total instances have a preceding `slog` call. The `ensureColumn` function has 2 SQL string concatenation points (PRAGMA query and ALTER TABLE). The `render()` method in `handler.go` also leaks the template name in one error path.

**Primary recommendation:** Introduce an `httpError` helper function (and update the existing `jsonError`) that logs the real error via slog and returns a generic message to the client. Then mechanically replace all 109+ instances. Separately, add a `regexp`-based identifier validator to `ensureColumn`.

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| SEC-01 | ensureColumn validates table and column names against a safe pattern before building SQL | ensureColumn currently uses raw string concatenation in 2 places; add regexp validation `^[a-zA-Z_][a-zA-Z0-9_]*$` at function entry |
| SEC-02 | HTTP error responses never contain SQL error text, filesystem paths, or internal Go error details | 109 instances of err.Error() passed to http.Error/jsonError; replace with generic messages via helper function |
| ERR-01 | All HTTP handlers return generic user-facing error messages instead of raw Go error strings | Same 109 instances; each needs a generic message ("internal server error", "bad request", etc.) instead of err.Error() |
| ERR-02 | All internal errors are logged server-side with slog before returning generic HTTP errors | Only 2 of 109 instances currently have slog calls; the helper function should handle slog logging automatically |
</phase_requirements>

## Standard Stack

### Core

This phase uses only Go standard library packages. No new dependencies are needed.

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `log/slog` | stdlib (Go 1.21+) | Structured error logging server-side | Already used throughout the codebase; JSON handler configured in main.go |
| `regexp` | stdlib | Identifier validation for ensureColumn | Standard approach for pattern matching; compile-once with `regexp.MustCompile` |
| `net/http` | stdlib | HTTP error responses | Already the foundation of all handlers |

### Supporting

No additional libraries needed. Everything required is already in the Go standard library and already imported by the project.

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| regexp for identifier validation | strings-based check (loop over runes) | regexp is clearer and more maintainable for this simple pattern; negligible perf difference since ensureColumn runs only at startup |
| Per-call slog + http.Error | Error-handling middleware | Middleware approach would require refactoring all handlers to return errors; too invasive for a hardening phase |

## Architecture Patterns

### Pattern 1: httpError Helper Function

**What:** A package-level helper in `internal/web/` that logs the real error with slog and writes a generic message to the HTTP response. Replaces the current pattern of `http.Error(w, err.Error(), code)`.

**When to use:** Every handler error path where an internal error (DB query failure, scan failure, template error, etc.) needs to be communicated to the client.

**Example:**
```go
// httpError logs the real error server-side and sends a generic message to the client.
func httpError(w http.ResponseWriter, code int, msg string, logMsg string, attrs ...any) {
    slog.Error(logMsg, attrs...)
    http.Error(w, msg, code)
}
```

**Usage transformation:**
```go
// BEFORE (leaks internal details):
http.Error(w, err.Error(), http.StatusInternalServerError)

// AFTER (generic message, logged internally):
httpError(w, http.StatusInternalServerError, "internal server error", "query failed", "err", err)
```

### Pattern 2: jsonError Update

**What:** The existing `jsonError` function in `handlers_core.go` needs a parallel update. Either modify it to accept slog args, or create a wrapper that logs before calling jsonError with a generic message.

**Example:**
```go
// jsonErr logs internally and returns a generic JSON error.
func jsonErr(w http.ResponseWriter, code int, msg string, logMsg string, attrs ...any) {
    slog.Error(logMsg, attrs...)
    jsonError(w, msg, code)
}
```

### Pattern 3: ensureColumn Identifier Validation

**What:** Add a compiled regexp that validates table and column names at the top of `ensureColumn` before any SQL string concatenation occurs.

**Example:**
```go
var safeIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func ensureColumn(db *sql.DB, table, col, decl string) error {
    if !safeIdentifier.MatchString(table) {
        return fmt.Errorf("ensureColumn: invalid table name %q", table)
    }
    if !safeIdentifier.MatchString(col) {
        return fmt.Errorf("ensureColumn: invalid column name %q", col)
    }
    // ... existing logic
}
```

### Pattern 4: Categorized Error Messages

**What:** Use consistent generic messages based on HTTP status code. Don't invent unique messages for every error path -- use a small set of standard messages.

**When to use:** All error responses.

**Standard messages:**
| Status Code | Generic Message |
|-------------|----------------|
| 400 | `"bad request"` |
| 404 | (use `http.NotFound` -- already does this correctly) |
| 405 | (use `w.WriteHeader(405)` -- already does this correctly) |
| 500 | `"internal server error"` |
| 503 | `"service unavailable"` |

**Exception:** Some 400-level errors already have safe, descriptive messages like `"invalid id"`, `"title and artist are required"`, `"invalid type"`. These are fine to keep as-is since they don't leak internals. The key rule is: never pass `err.Error()` to the client.

### Anti-Patterns to Avoid

- **Over-genericizing user input errors:** A 400 error for "invalid id" is safe and helpful. Don't replace it with "bad request" -- only replace cases where `err.Error()` is passed.
- **Losing error context in logs:** Each slog call should include relevant context (handler name, entity ID, operation) so errors are debuggable. Don't just log "error occurred".
- **Changing existing safe error messages:** Lines like `http.Error(w, "scanning not supported for this library type", 400)` are already safe. Don't change them.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| SQL identifier validation | Custom character-by-character loop | `regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)` | regexp is well-tested, readable, and the pattern is standard for SQL identifier validation |
| Error response sanitization | Per-handler slog+http.Error boilerplate | `httpError()` / `jsonErr()` helper functions | Consistency, reduced chance of forgetting the slog call |

## Common Pitfalls

### Pitfall 1: Missing slog Import in Handler Files

**What goes wrong:** `handlers_match.go` and `handlers_settings.go` do not currently import `log/slog`. Adding slog calls to these files without adding the import will cause compilation errors.
**Why it happens:** These files previously never needed to log -- they just passed errors to http.Error.
**How to avoid:** Add `"log/slog"` to the import block of both files as part of the first task.
**Warning signs:** `go build` failure with "undefined: slog".

### Pitfall 2: Breaking Existing Safe Error Messages

**What goes wrong:** Replacing ALL `http.Error` calls with generic messages, including ones that already have safe, user-helpful text like `"name, type, root_path are required"`.
**Why it happens:** Overly mechanical find-and-replace without reading context.
**How to avoid:** Only replace calls where `err.Error()` or `fmt.Sprintf` with error details is the message argument. Keep existing hardcoded safe strings.
**Warning signs:** User-facing forms losing helpful validation messages.

### Pitfall 3: Forgetting the render() Template Name Leak

**What goes wrong:** The `render()` method in `handler.go` line 151 sends `fmt.Sprintf("template not found: %s", page)` to the client, leaking the internal template filename.
**Why it happens:** It's in `handler.go`, not in the handler files, so it's easy to miss.
**How to avoid:** Include it explicitly in the task list. Replace with `"internal server error"` (the slog call on line 150 already logs the page name).

### Pitfall 4: Inconsistent JSON vs HTML Error Handling

**What goes wrong:** Some handlers check `requestWantsJSON(r)` and use `jsonError` for JSON clients, `http.Error` for HTML clients. Both paths need the same treatment.
**Why it happens:** The dual-path pattern exists in `librariesScan` and other handlers.
**How to avoid:** When fixing a handler, check for BOTH `http.Error` and `jsonError` calls in the same function.

### Pitfall 5: ensureColumn Validation Too Strict for decl Parameter

**What goes wrong:** Applying the same identifier regex to the `decl` parameter (e.g., `"TEXT NOT NULL DEFAULT ''"`), which is a full column declaration, not a simple identifier.
**Why it happens:** Over-applying the validation rule.
**How to avoid:** Only validate `table` and `col` parameters. The `decl` parameter contains SQL type declarations with spaces, keywords, and quotes -- it's inherently a trusted string (hardcoded at call sites). Document this distinction clearly.

## Code Examples

### Current State: Error Leakage Pattern (100+ instances)

```go
// Source: internal/web/handlers_music.go lines 186-189
// This is the dominant pattern -- raw err.Error() sent to client
if err != nil {
    http.Error(w, err.Error(), http.StatusInternalServerError)
    return
}
```

### Current State: ensureColumn SQL Injection Surface

```go
// Source: internal/db/migrate.go lines 303-326
func ensureColumn(db *sql.DB, table, col, decl string) error {
    rows, err := db.Query("PRAGMA table_info(" + table + ")")  // string concat
    // ...
    _, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + col + " " + decl)  // string concat
    return err
}
```

### Target State: httpError Helper

```go
// New helper for internal/web/helpers.go or handlers_core.go
func httpError(w http.ResponseWriter, code int, msg string, logMsg string, attrs ...any) {
    slog.Error(logMsg, attrs...)
    http.Error(w, msg, code)
}

// Usage:
if err != nil {
    httpError(w, 500, "internal server error", "query failed", "handler", "musicHome", "err", err)
    return
}
```

### Target State: jsonErr Helper

```go
// Wraps the existing jsonError with logging
func jsonErr(w http.ResponseWriter, code int, msg string, logMsg string, attrs ...any) {
    slog.Error(logMsg, attrs...)
    jsonError(w, msg, code)
}

// Usage:
if err != nil {
    jsonErr(w, 500, "internal server error", "load jobs failed", "err", err)
    return
}
```

### Target State: ensureColumn with Validation

```go
var safeIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func ensureColumn(db *sql.DB, table, col, decl string) error {
    if !safeIdentifier.MatchString(table) {
        return fmt.Errorf("ensureColumn: invalid table name %q", table)
    }
    if !safeIdentifier.MatchString(col) {
        return fmt.Errorf("ensureColumn: invalid column name %q", col)
    }
    rows, err := db.Query("PRAGMA table_info(" + table + ")")
    // ... rest unchanged
}
```

## Inventory of Changes Required

### By File: http.Error(w, err.Error(), ...) Instances

| File | Count | slog Import Needed? |
|------|-------|---------------------|
| `internal/web/handlers_music.go` | 47 | No (already imported) |
| `internal/web/handlers_tv.go` | 23 | No (already imported) |
| `internal/web/handlers_match.go` | 16 | **Yes** |
| `internal/web/handlers_settings.go` | 14 | **Yes** |
| **Total http.Error** | **100** | |

### By File: jsonError(w, err.Error(), ...) Instances

| File | Count |
|------|-------|
| `internal/web/handlers_core.go` | 3 |
| `internal/web/handlers_settings.go` | 6 |
| **Total jsonError** | **9** |

### Additional Fixes

| File | Line | Issue |
|------|------|-------|
| `internal/web/handler.go` | 151 | `fmt.Sprintf("template not found: %s", page)` leaks template name |
| `internal/web/handlers_settings.go` | 218 | `fmt.Sprintf("root_path must be under %s", h.cfg.MediaRoot)` leaks MediaRoot path |
| `internal/db/migrate.go` | 303-326 | `ensureColumn` needs identifier validation |

### Already-Safe Patterns (Do NOT Change)

These already use hardcoded safe messages and should be left as-is:

- `http.Error(w, "invalid id", 400)` -- safe, helpful
- `http.Error(w, "title and artist are required", 400)` -- safe, helpful
- `http.Error(w, "name, type, root_path are required", 400)` -- safe, helpful
- `http.Error(w, "scanning not supported for this library type", 400)` -- safe, helpful
- `http.Error(w, "internal server error", 500)` in `render()` line 158 -- already correct
- `http.NotFound(w, r)` calls -- already correct
- `w.WriteHeader(http.StatusMethodNotAllowed)` -- already correct

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `http.Error(w, err.Error(), 500)` | Log internally, generic message to client | Standard practice since Go 1.0 | No leaked internals |
| String concat in SQL | Parameterized queries or validated identifiers | Always recommended | Prevents SQL injection |

**Note:** Go's `database/sql` package parameterizes VALUES via `?` placeholders, but table/column names cannot be parameterized. Identifier validation via regexp is the standard approach for DDL operations.

## Open Questions

1. **Helper function location**
   - What we know: `jsonError` and `requestWantsJSON` already live in `handlers_core.go`. The new `httpError` helper should live alongside them.
   - What's unclear: Whether to put helpers in `handlers_core.go` or create a dedicated `helpers.go` file.
   - Recommendation: Add to `handlers_core.go` for now (consistent with existing `jsonError`). Can be refactored in a future architecture phase.

2. **Log level for 400 errors**
   - What we know: 500 errors should be `slog.Error`. 400 errors are often client mistakes, not server bugs.
   - What's unclear: Whether client errors (bad input) warrant `slog.Error` or `slog.Warn`.
   - Recommendation: Use `slog.Warn` for 400-level errors (client mistakes), `slog.Error` for 500-level errors (server failures). This keeps error logs clean.

3. **MediaRoot path in error message**
   - What we know: `handlers_settings.go:218` includes `h.cfg.MediaRoot` in a 400 error message.
   - What's unclear: Whether the media root path is sensitive enough to hide from authenticated users.
   - Recommendation: Replace with `"root_path must be under the configured media root"` -- the user already knows the media root from the form UI, but we shouldn't leak it in error responses as a matter of principle.

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go testing (stdlib), Go 1.23 |
| Config file | None (stdlib, no config needed) |
| Quick run command | `go test ./internal/db/ -run TestEnsureColumn -v -count=1` |
| Full suite command | `go test ./... -count=1` |

### Phase Requirements to Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| SEC-01 | ensureColumn rejects invalid identifiers | unit | `go test ./internal/db/ -run TestEnsureColumnValidation -v -count=1` | No -- Wave 0 |
| SEC-02 | HTTP error responses contain no internal details | unit | `go test ./internal/web/ -run TestErrorSanitization -v -count=1` | No -- Wave 0 |
| ERR-01 | Handlers return generic error messages | unit (same as SEC-02) | `go test ./internal/web/ -run TestErrorSanitization -v -count=1` | No -- Wave 0 |
| ERR-02 | Internal errors are logged via slog | manual-only | grep handler code for slog calls before error returns | N/A -- verified by code review / grep |

**Note on ERR-02:** Verifying that every error path logs via slog before returning is best done via code review and grep (success criterion 4 in the phase description). Writing a test for "every error path logs" would require exhaustive handler test coverage, which is the scope of Phase 4 (TEST-04, TEST-05, TEST-06). For this phase, the mechanical transformation (httpError helper always logs) provides the guarantee.

### Sampling Rate

- **Per task commit:** `go test ./internal/db/ -run TestEnsureColumn -v -count=1`
- **Per wave merge:** `go test ./... -count=1`
- **Phase gate:** Full suite green + grep verification (no `err.Error()` in http.Error/jsonError calls)

### Wave 0 Gaps

- [ ] `internal/db/db_test.go` -- add `TestEnsureColumnValidation` tests for SEC-01 (reject invalid table/column names, accept valid ones)
- [ ] `internal/web/` -- no test infrastructure exists yet. Minimal test for SEC-02 would require handler test setup (httptest). This is better deferred to Phase 4 (TEST-04 through TEST-06) since the grep-based verification (success criterion 4) is more appropriate for this phase.

## Sources

### Primary (HIGH confidence)

- **Direct codebase analysis** -- all handler files read and grep-counted for exact instance counts
- `internal/db/migrate.go` -- read in full, both string concatenation points identified at lines 304 and 324
- `internal/web/handler.go` -- render() error leak identified at line 151
- `internal/web/handlers_core.go` -- jsonError function analyzed at line 121
- `internal/web/handlers_music.go` -- 47 http.Error(err.Error()) + 4 slog.Error calls identified
- `internal/web/handlers_tv.go` -- 23 http.Error(err.Error()) + 1 slog.Warn call identified
- `internal/web/handlers_match.go` -- 16 http.Error(err.Error()), no slog import
- `internal/web/handlers_settings.go` -- 14 http.Error(err.Error()) + 6 jsonError(err.Error()), no slog import

### Secondary (MEDIUM confidence)

- Go standard library `regexp` package -- well-known, stable API. `regexp.MustCompile` for compile-once patterns.
- Go `log/slog` package -- structured logging, already configured in this project with JSON handler.

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH -- pure stdlib, no new deps, patterns already established in codebase
- Architecture: HIGH -- mechanical transformation with clear helper pattern
- Pitfalls: HIGH -- exhaustive code analysis performed, all edge cases documented
- Inventory: HIGH -- exact counts from grep, verified across all handler files

**Research date:** 2026-03-05
**Valid until:** Indefinite -- stdlib-only changes, no external dependency concerns

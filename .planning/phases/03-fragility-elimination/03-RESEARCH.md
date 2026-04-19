# Phase 3: Fragility Elimination - Research

**Researched:** 2026-03-05
**Domain:** Go stdlib HTTP handler refactoring, template validation
**Confidence:** HIGH

## Summary

Phase 3 addresses three fragility issues in the `internal/web` package: duplicated URL path ID parsing (FRAG-01), missing template startup validation (FRAG-02), and silent template failure recovery (FRAG-03). All three are internal refactoring within a single Go package with no new dependencies required.

The codebase has 7 call sites using an identical 4-line `TrimPrefix + path.Clean + TrimPrefix + ParseInt` pattern across `handlers_music.go` and `handlers_tv.go`. The template system in `handler.go` compiles templates at startup in `New()` but silently skips parse failures and provides a fallback layout on layout parse failure. The `render()` method handles missing templates at runtime with a 500 error instead of catching the problem at startup.

**Primary recommendation:** Extract a `pathID(r, prefix)` helper returning `(int64, error)`, change `New()` to return `(*Handler, error)` to propagate template failures, and add a startup cross-check between `render()` template names and the compiled template set.

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions
None -- user chose to skip discussion, all implementation decisions are Claude's discretion.

### Claude's Discretion

**FRAG-01 -- URL path ID helper:**
- Function signature and return type (int64 + error, or int64 + bool)
- Whether to also handle the `path.Clean` sanitization step
- Whether to only replace the 8 `TrimPrefix+Clean+ParseInt` call sites or also cover other ID parsing patterns
- Error response behavior when parsing fails (httpError 400 vs 404)

**FRAG-02 -- Template startup validation:**
- Whether to cross-check handler `render()` calls against the compiled template set, or validate the `pages` list only
- Whether validation is a build-time check, startup check, or both
- How to surface which template name is missing

**FRAG-03 -- Fail-fast on broken templates:**
- Whether layout parse failure should panic, log.Fatal, or return error from New()
- Whether per-page parse failures should abort startup or collect all errors and report them together
- Whether the current fallback layout template (line 113) should be removed entirely

### Deferred Ideas (OUT OF SCOPE)
None -- discussion stayed within phase scope.
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| FRAG-01 | URL path ID parsing uses a shared helper function instead of duplicated code across 6+ handlers | Catalogued all 7 identical call sites and 1 string-ID variant; designed `pathID()` helper signature |
| FRAG-02 | Template registration validates at startup that all handler-referenced templates exist | Mapped all 20 `render()` call sites against the `pages` list and disk files; identified validation strategy |
| FRAG-03 | Missing or broken templates produce a clear startup error, not a runtime 500 | Analyzed `New()` error handling paths; identified `New()` signature change and `main.go` integration |
</phase_requirements>

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| Go stdlib `net/http` | Go 1.23 | HTTP handlers and routing | Already used, no change needed |
| Go stdlib `html/template` | Go 1.23 | Template compilation and rendering | Already used, no change needed |
| Go stdlib `strconv` | Go 1.23 | `ParseInt` for path ID parsing | Already used at all call sites |
| Go stdlib `path` | Go 1.23 | `path.Clean` for path sanitization | Already used at all call sites |

### Supporting
No new dependencies needed. This phase is pure internal refactoring.

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Custom `pathID()` helper | Go 1.22+ `http.Request.PathValue()` | Would require upgrading all route registrations to use pattern syntax; out of scope for this phase |

## Architecture Patterns

### FRAG-01: pathID Helper

**Current pattern (7 identical blocks):**
```go
idStr := strings.TrimPrefix(r.URL.Path, "/music/artist/")
idStr = path.Clean("/" + idStr)
idStr = strings.TrimPrefix(idStr, "/")
artistID, err := strconv.ParseInt(idStr, 10, 64)
if err != nil || artistID <= 0 {
    http.NotFound(w, r)
    return
}
```

**Call sites to replace (7 int64 sites):**

| File | Line | Prefix | Variable |
|------|------|--------|----------|
| handlers_music.go | 316-319 | `/music/artist/` | artistID |
| handlers_music.go | 451-453 | `/music/album/` | albumID |
| handlers_music.go | 1042-1044 | `/stream/track/` | trackID |
| handlers_music.go | 1224-1226 | `/art/album/` | albumID |
| handlers_music.go | 1272-1274 | `/art/artist/` | artistID |
| handlers_tv.go | 727-729 | `/stream/tv/` | fileID |
| handlers_tv.go | 172-174 | `/tv/series/` | seriesID (string, not int64) |

**Recommended helper signature:**
```go
// pathID extracts a positive int64 from the URL path after the given prefix.
// It applies path.Clean to sanitize the segment before parsing.
// Returns 0, error if the segment is missing, not a number, or <= 0.
func pathID(r *http.Request, prefix string) (int64, error) {
    seg := strings.TrimPrefix(r.URL.Path, prefix)
    seg = path.Clean("/" + seg)
    seg = strings.TrimPrefix(seg, "/")
    id, err := strconv.ParseInt(seg, 10, 64)
    if err != nil {
        return 0, fmt.Errorf("invalid path ID %q: %w", seg, err)
    }
    if id <= 0 {
        return 0, fmt.Errorf("path ID must be positive, got %d", id)
    }
    return id, nil
}
```

**Design decisions:**
- Return `(int64, error)` rather than `(int64, bool)` -- consistent with Go conventions and allows callers to use the error in logging
- Include `path.Clean` sanitization in the helper since every call site uses it
- The TV series string-ID site (`handlers_tv.go:172`) uses a different pattern (string, not int64) and should use a separate `pathSegment()` helper or be handled differently. However, it still uses the same `TrimPrefix + path.Clean + TrimPrefix` pattern, so it could be a separate `pathSegment(r, prefix) string` helper
- All current call sites respond with `http.NotFound` on failure -- keep this behavior
- The TV art handler (`handlers_tv.go:441`) uses a completely different multi-segment pattern and is NOT a candidate for this helper

**TV series string-ID variant:**
```go
// pathSegment extracts a sanitized path segment after the given prefix.
// Returns "" if the segment is empty after cleaning.
func pathSegment(r *http.Request, prefix string) string {
    seg := strings.TrimPrefix(r.URL.Path, prefix)
    seg = path.Clean("/" + seg)
    seg = strings.TrimPrefix(seg, "/")
    return seg
}
```

### FRAG-02 + FRAG-03: Template Startup Validation

**Current `New()` behavior (handler.go:36-145):**
1. Parse layout template (line 106-110) -- on failure, log and use fallback (line 112-114)
2. For each page in `pages` slice (line 116-132) -- on failure, log and `continue` (skip)
3. Store compiled templates in `tpls` map (line 131)
4. Return `*Handler` with no error channel (line 144)

**Current `render()` behavior (handler.go:147-167):**
1. Lookup template in `tpls` map (line 148)
2. If not found, log error and return 500 (line 149-153)
3. Template name is a string literal at each call site -- never validated at startup

**Template inventory cross-check:**

| Template Name | In `pages` list | On disk | Referenced by `render()` |
|---------------|----------------|---------|--------------------------|
| home.html | Yes (line 43) | Yes | handlers_core.go:19 |
| login.html | Yes (line 44) | Yes | handlers_core.go:39 |
| libraries.html | Yes (line 45) | Yes | handlers_settings.go:186 |
| libraries_new.html | Yes (line 46) | Yes | handlers_settings.go:196 |
| settings.html | Yes (line 47) | Yes | handlers_settings.go:26 |
| settings_jobs.html | Yes (line 48) | Yes | handlers_settings.go:41 |
| music_home.html | Yes (line 49) | Yes | handlers_music.go:148, 265 |
| music_artist.html | Yes (line 50) | Yes | handlers_music.go:430 |
| music_album.html | Yes (line 51) | Yes | handlers_music.go:528 |
| music_albums.html | Yes (line 52) | Yes | handlers_music.go:861, 908 |
| music_compilations.html | Yes (line 53) | Yes | handlers_music.go:924, 955 |
| player.html | Yes (line 54) | Yes | handlers_music.go:1025 |
| music_match_review.html | Yes (line 55) | Yes | handlers_match.go:109 |
| music_album_edit.html | Yes (line 56) | Yes | handlers_music.go:624 |
| music_duplicates.html | Yes (line 57) | Yes | handlers_music.go:1171, 1183 |
| settings_tags.html | Yes (line 58) | Yes | handlers_settings.go:439 |
| tv_home.html | Yes (line 59) | Yes | handlers_tv.go:71 |
| tv_series.html | Yes (line 60) | Yes | handlers_tv.go:247 |
| tv_season.html | Yes (line 61) | Yes | handlers_tv.go:375 |
| tv_match_review.html | Yes (line 62) | Yes | handlers_tv.go:562 |
| tv_player.html | Yes (line 63) | Yes | handlers_tv.go:856 |
| movies_home.html | Yes (line 64) | Yes | handlers_core.go:116 |

All 22 template names (20 unique pages + layout.html + partials) are currently in sync. The risk this phase eliminates is future drift.

**Recommended approach:**

1. **Change `New()` signature** from `func New(d Deps) *Handler` to `func New(d Deps) (*Handler, error)`
2. **Remove fallback layout** (line 112-114) -- layout parse failure returns error immediately
3. **Collect per-page parse errors** -- accumulate all page parse failures into a multi-error, then return them all at once so the developer sees every broken template, not just the first one
4. **Add template name registry validation** -- define a package-level list of all template names referenced by `render()` calls (or derive from the `pages` slice), and after compilation, verify every name exists in `tpls` map
5. **Update `main.go`** to handle the error from `web.New()`: log and `os.Exit(1)`

**Error propagation path:**
```
web.New() returns error
    -> main.go checks error
    -> slog.Error("template initialization failed", "err", err)
    -> os.Exit(1)
```

### Anti-Patterns to Avoid
- **Do NOT use `log.Fatal` inside `New()`** -- the caller should decide how to handle the error (consistent with Go conventions and the existing pattern in `main.go` where `db.Open`, `cfg.Validate`, etc. return errors)
- **Do NOT validate at build time** -- templates are loaded from the filesystem at runtime; build-time checks would require code generation or test infrastructure (deferred to Phase 4)
- **Do NOT add a `go vet` analyzer** -- over-engineering for 20 templates

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Multi-error aggregation | Custom error list | `errors.Join()` (Go 1.20+) or `fmt.Errorf` with `%w` | Standard library handles multi-error composition cleanly |
| Path parameter extraction | Custom URL router | Keep `pathID()` helper with stdlib `http.ServeMux` | The full-router approach would be a much larger refactor |

**Key insight:** This phase is explicitly about consolidation, not architecture upgrades. The stdlib `http.ServeMux` is intentionally used. A `pathID()` helper is the right level of abstraction -- it eliminates duplication without changing the routing model.

## Common Pitfalls

### Pitfall 1: Breaking the `New()` API without updating all callers
**What goes wrong:** Changing `New()` to return `(*Handler, error)` but forgetting to update `main.go` causes a compile error. Easy to catch, but also need to check test files.
**Why it happens:** Only one call site exists (`main.go:38`), and there are no test files in `internal/web/` yet, but tests may be added in Phase 4.
**How to avoid:** After changing the signature, run `go build ./...` to verify all callers compile.
**Warning signs:** Compiler errors about unused error values.

### Pitfall 2: TV series string-ID vs int64 ID
**What goes wrong:** Treating `tvSeriesDetail` (which extracts a string series ID like "12345" that is NOT parsed to int64 -- it's used as a string key) the same as int64 path ID handlers.
**Why it happens:** The 3-line `TrimPrefix + Clean + TrimPrefix` pattern is identical, but the TV series handler does NOT call `ParseInt` -- it uses the string directly.
**How to avoid:** Create `pathSegment()` for string extraction and `pathID()` for int64. The TV series handler and any similar future handlers use `pathSegment()`.
**Warning signs:** Trying to force `pathID()` on `/tv/series/` routes.

### Pitfall 3: TV art handler is a different pattern entirely
**What goes wrong:** Trying to convert the TV art handler (`handlers_tv.go:435-517`) to use `pathID()` when it parses multi-segment paths like `/art/tv/poster/{seriesID}` and `/art/tv/season/{seriesID}/{seasonNum}`.
**Why it happens:** It also uses `strings.TrimPrefix(r.URL.Path, ...)` but then splits on `/` for multiple segments.
**How to avoid:** Leave the TV art handler as-is. It has a fundamentally different pattern (multi-segment extraction, not single-ID).

### Pitfall 4: Template validation racing with template compilation
**What goes wrong:** Adding validation that checks for template names BEFORE compilation finishes.
**Why it happens:** Misunderstanding the flow -- validation should happen AFTER the compilation loop, not during.
**How to avoid:** Validate at the end of `New()`, after the `for _, p := range pages` loop completes.

### Pitfall 5: Partial template list maintenance
**What goes wrong:** Creating a separate `requiredTemplates` list that drifts from the `pages` list.
**Why it happens:** Two lists to maintain is worse than one.
**How to avoid:** Use the `pages` slice as the single source of truth. The validation step should check that every entry in `pages` resulted in a compiled template in `tpls`. No separate list needed.

## Code Examples

### pathID helper (place in handler.go or a new helpers.go)
```go
// Source: Derived from 7 identical call sites in handlers_music.go + handlers_tv.go
func pathID(r *http.Request, prefix string) (int64, error) {
    seg := strings.TrimPrefix(r.URL.Path, prefix)
    seg = path.Clean("/" + seg)
    seg = strings.TrimPrefix(seg, "/")
    id, err := strconv.ParseInt(seg, 10, 64)
    if err != nil {
        return 0, fmt.Errorf("invalid path ID %q: %w", seg, err)
    }
    if id <= 0 {
        return 0, fmt.Errorf("path ID must be positive, got %d", id)
    }
    return id, nil
}
```

### pathSegment helper (for TV series string IDs)
```go
func pathSegment(r *http.Request, prefix string) string {
    seg := strings.TrimPrefix(r.URL.Path, prefix)
    seg = path.Clean("/" + seg)
    seg = strings.TrimPrefix(seg, "/")
    return seg
}
```

### Usage at call site (before/after)
```go
// BEFORE (4 lines, duplicated 7 times):
idStr := strings.TrimPrefix(r.URL.Path, "/music/artist/")
idStr = path.Clean("/" + idStr)
idStr = strings.TrimPrefix(idStr, "/")
artistID, err := strconv.ParseInt(idStr, 10, 64)
if err != nil || artistID <= 0 {
    http.NotFound(w, r)
    return
}

// AFTER (3 lines, using shared helper):
artistID, err := pathID(r, "/music/artist/")
if err != nil {
    http.NotFound(w, r)
    return
}
```

### New() with error return
```go
// BEFORE:
func New(d Deps) *Handler {
    // ... layout parse
    if err != nil {
        slog.Error("failed to parse layout template", "err", err)
        layoutBase = template.Must(...)  // fallback
    }
    // ... page parse
    if err != nil {
        slog.Error("template parse failed", "page", p, "err", err)
        continue  // silently skip
    }
    return h
}

// AFTER:
func New(d Deps) (*Handler, error) {
    // ... layout parse
    if err != nil {
        return nil, fmt.Errorf("layout template: %w", err)
    }
    // ... page parse -- collect errors
    var errs []error
    for _, p := range pages {
        // ...
        if err != nil {
            errs = append(errs, fmt.Errorf("template %s: %w", p, err))
            continue
        }
        tpls[p] = t
    }
    if len(errs) > 0 {
        return nil, fmt.Errorf("template compilation failed:\n%w", errors.Join(errs...))
    }
    // Validate all pages compiled successfully
    for _, p := range pages {
        if _, ok := tpls[p]; !ok {
            errs = append(errs, fmt.Errorf("template %s: not compiled", p))
        }
    }
    if len(errs) > 0 {
        return nil, fmt.Errorf("template validation failed:\n%w", errors.Join(errs...))
    }
    return h, nil
}
```

### main.go update
```go
// BEFORE:
h := web.New(web.Deps{Cfg: cfg, DB: dbConn})

// AFTER:
h, err := web.New(web.Deps{Cfg: cfg, DB: dbConn})
if err != nil {
    slog.Error("web handler initialization failed", "err", err)
    os.Exit(1)
}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Go 1.21 `http.ServeMux` (no path params) | Go 1.22+ `http.ServeMux` with `{param}` pattern | Go 1.22, Feb 2024 | Could eliminate `pathID` helper entirely, but requires route registration changes -- out of scope |

**Deprecated/outdated:**
- The fallback layout template (handler.go:113) is the kind of "graceful degradation" that hides bugs. It should be removed entirely in favor of fail-fast.

## Open Questions

1. **Should pathID be a package-level function or a method on Handler?**
   - What we know: It only needs `*http.Request` and a prefix string -- no handler state
   - What's unclear: Whether making it a method improves discoverability
   - Recommendation: Package-level function (unexported `pathID`). It needs no handler state, and keeping it as a function makes it testable without constructing a Handler.

2. **Should the TV series string-ID case also get a helper?**
   - What we know: There is exactly 1 call site (handlers_tv.go:172-174) using the string variant
   - What's unclear: Whether a helper for 1 call site is worth it
   - Recommendation: Yes, create `pathSegment()`. Even with 1 current call site, it documents the pattern and prevents future copy-paste. The 3-line pattern is identical to pathID minus the ParseInt. It also makes the intent clearer at the call site.

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` (Go 1.23) |
| Config file | None needed -- Go conventions |
| Quick run command | `go test ./internal/web/... -count=1` |
| Full suite command | `go test ./... -count=1` |

### Phase Requirements -> Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| FRAG-01 | pathID extracts valid int64 from URL path | unit | `go test ./internal/web -run TestPathID -count=1` | No -- Wave 0 |
| FRAG-01 | pathID rejects invalid/negative/zero IDs | unit | `go test ./internal/web -run TestPathID -count=1` | No -- Wave 0 |
| FRAG-01 | pathSegment extracts sanitized string segment | unit | `go test ./internal/web -run TestPathSegment -count=1` | No -- Wave 0 |
| FRAG-02 | New() fails if a page template file is missing from disk | unit | `go test ./internal/web -run TestNewMissingTemplate -count=1` | No -- Wave 0 |
| FRAG-03 | New() fails if layout template cannot be parsed | unit | `go test ./internal/web -run TestNewBrokenLayout -count=1` | No -- Wave 0 |
| FRAG-03 | New() returns descriptive error listing all broken templates | unit | `go test ./internal/web -run TestNewMultipleErrors -count=1` | No -- Wave 0 |

### Sampling Rate
- **Per task commit:** `go test ./internal/web/... -count=1`
- **Per wave merge:** `go test ./... -count=1`
- **Phase gate:** Full suite green before `/gsd:verify-work`

### Wave 0 Gaps
- [ ] `internal/web/helpers_test.go` -- covers FRAG-01 pathID and pathSegment tests
- [ ] `internal/web/handler_test.go` -- covers FRAG-02/FRAG-03 New() validation tests
- Note: These tests need to handle template directory setup (temp dirs with test templates). The existing test pattern uses `t.TempDir()` for filesystem-dependent tests.

## Sources

### Primary (HIGH confidence)
- Direct source code analysis of `internal/web/handler.go`, `handlers_music.go`, `handlers_tv.go`, `handlers_core.go`, `handlers_match.go`, `handlers_settings.go`, `cmd/isomedia/main.go`
- All 7 ID-parsing call sites verified by line number
- All 22 render() call sites cross-referenced against `pages` list and disk files
- Template compilation flow traced through `New()` lines 36-145

### Secondary (MEDIUM confidence)
- Go 1.22 `http.ServeMux` pattern matching as alternative (not recommended for this phase scope)

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH -- no new dependencies, pure refactoring of existing code
- Architecture: HIGH -- all call sites catalogued with line numbers, patterns verified
- Pitfalls: HIGH -- based on direct code analysis of edge cases (TV string IDs, TV art multi-segment)

**Research date:** 2026-03-05
**Valid until:** No expiry -- internal refactoring research based on stable source code

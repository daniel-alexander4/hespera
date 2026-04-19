# Phase 3: Fragility Elimination - Context

**Gathered:** 2026-03-05
**Status:** Ready for planning

<domain>
## Phase Boundary

Consolidate duplicated URL path-ID parsing into a shared helper, validate all handler-referenced templates at startup, and fail fast on missing or broken templates. Requirements: FRAG-01, FRAG-02, FRAG-03. No new features, no refactoring beyond what's needed for these three fixes.

</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion

User chose to skip discussion -- all implementation decisions are Claude's discretion. Key areas:

**FRAG-01 — URL path ID helper:**
- Function signature and return type (int64 + error, or int64 + bool)
- Whether to also handle the `path.Clean` sanitization step
- Whether to only replace the 8 `TrimPrefix+Clean+ParseInt` call sites or also cover other ID parsing patterns
- Error response behavior when parsing fails (httpError 400 vs 404)

**FRAG-02 — Template startup validation:**
- Whether to cross-check handler `render()` calls against the compiled template set, or validate the `pages` list only
- Whether validation is a build-time check, startup check, or both
- How to surface which template name is missing

**FRAG-03 — Fail-fast on broken templates:**
- Whether layout parse failure should panic, log.Fatal, or return error from New()
- Whether per-page parse failures should abort startup or collect all errors and report them together
- Whether the current fallback layout template (line 113) should be removed entirely

</decisions>

<specifics>
## Specific Ideas

No specific requirements -- open to standard approaches.

</specifics>

<code_context>
## Existing Code Insights

### Reusable Assets
- `httpError(w, code, msg, logMsg, attrs...)` helper (from Phase 1): Use for 400/404 responses when URL ID parsing fails
- `handler.go:147-167` `render()` method: Already does runtime template lookup with 500 fallback -- startup validation eliminates the "template not found" runtime path

### Established Patterns
- URL ID parsing: 8 identical blocks of `strings.TrimPrefix` + `path.Clean("/" + idStr)` + `strings.TrimPrefix(idStr, "/")` + `strconv.ParseInt(idStr, 10, 64)` across handlers_music.go, handlers_tv.go
- Template registration: Static `pages` slice in `New()` (handler.go:42-65), parsed into `tpls` map
- Template error handling: `slog.Error` + `continue` for parse failures (handler.go:128-129), fallback layout on layout failure (handler.go:112-114)

### Integration Points
- `handlers_music.go:316-319, 451-453, 1042-1044, 1224-1226, 1272-1274` — Music handler ID parsing (5 sites)
- `handlers_tv.go:172, 441, 727-729` — TV handler ID parsing (3 sites)
- `handler.go:36-145` — `New()` constructor: template compilation, layout parsing, page parsing
- `handler.go:147-167` — `render()`: runtime template lookup

</code_context>

<deferred>
## Deferred Ideas

None -- discussion stayed within phase scope.

</deferred>

---

*Phase: 03-fragility-elimination*
*Context gathered: 2026-03-05*

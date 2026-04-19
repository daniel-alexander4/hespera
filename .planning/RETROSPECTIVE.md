# Project Retrospective

*A living document updated after each milestone. Lessons feed forward into future planning.*

## Milestone: v1.0 -- Codebase Audit & Hardening

**Shipped:** 2026-03-06
**Phases:** 5 | **Plans:** 13 | **Tasks:** 24

### What Was Built
- HTTP error sanitization across all handlers (109 err.Error() leaks replaced with httpError/jsonErr helpers)
- SQL injection prevention for schema evolution (ensureColumn identifier validation)
- 3 data integrity bug fixes (TV rescan identity preservation, deterministic compilation detection, safe album merge)
- Scanner error resilience (per-file continue with structured logging for both music and TV)
- Shared URL path parsing helpers and template startup validation with fail-fast
- Unit tests for music scanner, TV scanner, music/TV/settings handlers (49+ subtests)
- Integration tests for music match pipeline (mocked MusicBrainz/Wikipedia/Wikidata/Wikimedia/CAA) and TV match pipeline (mocked TMDB)

### What Worked
- Parallel plan execution within waves -- Phase 5's two independent plans ran simultaneously, halving wall-clock time
- Post-scan finalization pattern for compilation detection eliminated an entire class of ordering bugs
- baseURL struct field pattern for test injection enabled full pipeline testing without changing public APIs
- Phase ordering (security -> bugs -> fragility -> tests) meant each phase built on a stable foundation
- Integration checker at audit time caught the missing Phase 2 VERIFICATION.md before it became a blocker

### What Was Inefficient
- Phase 2 was executed without running verification, requiring a retroactive verifier run during audit
- ROADMAP.md plan checkboxes got out of sync with actual completion status (some phases showed `[ ]` despite being complete)
- Some SUMMARY.md files lacked one_liner frontmatter, requiring manual accomplishment extraction at milestone completion

### Patterns Established
- WHERE clause on DO UPDATE for protecting curated data (TV rescan identity pattern)
- Post-scan finalization for expensive heuristics (run after WalkDir, not per-file)
- httpError/jsonErr with severity-appropriate logging (5xx -> slog.Error, 4xx -> slog.Warn)
- Closed channel for rate limiter bypass in tests (returns zero value immediately and repeatedly)
- Same-package test files for accessing unexported functions (tvscan, match, tmdb packages)

### Key Lessons
1. Always run phase verification after execution -- the audit caught a missing VERIFICATION.md that blocked milestone completion
2. Post-scan finalization eliminates ordering dependencies -- running heavy heuristics after all data is collected is more reliable than per-item processing
3. Struct field injection (unexported, with production defaults) is the cleanest Go pattern for test HTTP redirects -- no interface wrappers needed
4. Bug fixes and tests are complementary phases -- writing tests for Phase 2/3 fixes in Phase 4 actually found a real bug (UNIQUE constraint in finalizeCompilations)

### Cost Observations
- Model mix: orchestrator on opus, executors/verifiers on sonnet (balanced profile)
- Phase execution: ~1-9 min per plan, typically 2-4 min
- Notable: Parallel execution in Phase 5 completed both plans in ~6 min wall clock vs ~9 min sequential

---

## Cross-Milestone Trends

### Process Evolution

| Milestone | Phases | Plans | Key Change |
|-----------|--------|-------|------------|
| v1.0 | 5 | 13 | First milestone -- established audit/fix/test pattern |

### Cumulative Quality

| Milestone | Test Subtests | Integration Tests | Packages Tested |
|-----------|---------------|-------------------|-----------------|
| v1.0 | 49+ | 4 (music + TV match) | db, web, scan, tvscan, match, tmdb |

### Top Lessons (Verified Across Milestones)

1. Always verify after execution -- missing artifacts block completion
2. Post-scan finalization eliminates ordering bugs

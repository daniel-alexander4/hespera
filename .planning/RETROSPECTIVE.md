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

## Milestone: v1.1 -- Automated Music Match Pipeline

**Shipped:** 2026-03-07
**Phases:** 3 | **Plans:** 3 | **Tasks:** 6

### What Was Built
- 80% auto-accept threshold with two-state match model (matched/unmatched, eliminated uncertain status)
- Inline tag writeback in match pipeline -- name normalization to MusicBrainz canonical values + MBID writing to file tags in same pass
- Verified enrichment pipeline (cover art, artist bio, artist image) already wired from Phase 2b
- "Run Match" button on review page for manual matching without navigating to Libraries
- DB migration converting legacy uncertain rows to unmatched

### What Worked
- Research agent correctly identified ENRICH-01 and ENRICH-02 as already implemented -- Phase 8 became verification + small UI addition instead of re-implementing existing code
- Lean 1-plan-per-phase structure kept each phase fast (~2-5 min execution)
- Tight cross-phase integration: matchAlbum() orchestrates the entire Phase 6->7->8 chain in one function (score -> threshold -> normalize -> cover art -> writeback)
- Integration checker at audit verified all 12 cross-phase connections wired correctly

### What Was Inefficient
- SUMMARY.md files still lack one_liner frontmatter (same issue from v1.0 -- not fixed)
- Phases 2, 6, 7 missing VALIDATION.md files (Nyquist validation never formally signed off for any phase)
- ROADMAP.md checkboxes still get out of sync (06-01 and 08-01 showed `[ ]` despite being complete)

### Patterns Established
- Inline writeback scoped to single album (writebackAlbumTracks) vs library-wide (RunTagWriteback) -- separation of concerns for auto vs manual paths
- Non-fatal name normalization with slog warnings -- matching succeeds even if name update fails
- Research-driven phase planning -- Phase 8 research revealed no new code needed, saving execution time

### Key Lessons
1. Research before planning saves execution time -- Phase 8 would have been over-engineered without the researcher identifying existing enrichment code
2. Inline writeback in the match function is simpler than a separate job -- eliminates coordination and ensures matching + writeback are atomic
3. Two-state match model (matched/unmatched) is simpler and sufficient -- the uncertain queue added complexity without user value

### Cost Observations
- Model mix: orchestrator on opus, executors/verifiers/researchers on sonnet (balanced profile)
- Phase execution: ~2-5 min per plan
- Notable: 3 phases completed in one session, end-to-end including audit took ~30 min

---

## Milestone: v1.2 -- TV Auto-Match Pipeline

**Shipped:** 2026-03-07
**Phases:** 2 | **Plans:** 3 | **Tasks:** 5

### What Was Built
- TV auto-match threshold raised from 0.45 to 0.80 with confidence-driven auto-accept
- Unified status model across music and TV (matched/unmatched/skipped replacing resolved/needs_fix)
- DB migration with table recreation pattern for CHECK constraint changes
- 7 unit subtests for pickBestResult scoring (empty, exact, multi-candidate, popularity cap, threshold boundaries)
- Integration test for below-threshold pipeline (identity stays unmatched, no metadata cached, no art downloaded)
- 7 handler subtests for TV match review/approve/skip endpoints with new status model

### What Worked
- Parallel execution of Phase 10's two plans (different packages, zero file overlap) halved wall-clock time
- Clean phase separation: Phase 9 changed behavior, Phase 10 tested it -- no circular dependency
- Unified status model simplified both code and mental model -- one set of statuses across music and TV
- Table recreation migration pattern handled SQLite's lack of ALTER CHECK cleanly

### What Was Inefficient
- SUMMARY.md files still lack one_liner frontmatter (third milestone with this issue)
- Both phases skipped research (disabled in config) so no VALIDATION.md generated -- Nyquist compliance missing for all v1.2 phases
- Phase 9 ROADMAP.md progress row had malformed columns (milestone field missing)

### Patterns Established
- Table recreation pattern for CHECK constraint migration (idempotent CASE conversion + recreation)
- Inline handler creation with config override for subtests needing specific config (TMDBAPIKey)
- Functional template stubs in setupTemplateDir for handler tests (tv_match_review.html alongside music_match_review.html)
- Dedicated mock server per test scenario rather than reusing shared mock with parameter variation

### Key Lessons
1. Unified status models across similar subsystems reduce cognitive load and testing surface -- matched/unmatched works for both music and TV
2. Table recreation is the right SQLite migration pattern for CHECK constraints -- don't fight the limitations, work with them
3. Two focused phases (implement + test) is cleaner than one large phase -- keeps each plan small and parallelizable

### Cost Observations
- Model mix: orchestrator on opus, executors/verifiers/researchers on sonnet (balanced profile)
- Phase execution: ~2-4 min per plan
- Notable: Both Phase 10 plans executed in parallel (~5 min wall clock for 2 plans)

---

## Cross-Milestone Trends

### Process Evolution

| Milestone | Phases | Plans | Key Change |
|-----------|--------|-------|------------|
| v1.0 | 5 | 13 | First milestone -- established audit/fix/test pattern |
| v1.1 | 3 | 3 | Lean phases (1 plan each), research-driven planning reduced wasted execution |
| v1.2 | 2 | 3 | Parallel plan execution, implement+test phase separation pattern |

### Cumulative Quality

| Milestone | Test Subtests | Integration Tests | Packages Tested |
|-----------|---------------|-------------------|-----------------|
| v1.0 | 49+ | 4 (music + TV match) | db, web, scan, tvscan, match, tmdb |
| v1.1 | 58+ | 5 (+ match threshold/normalization/writeback) | + match (6 new test functions) |
| v1.2 | 72+ | 6 (+ TV below-threshold pipeline) | + tmdb (pickBestResult), web (TV handler tests) |

### Top Lessons (Verified Across Milestones)

1. Always verify after execution -- missing artifacts block completion (v1.0 audit caught missing VERIFICATION.md)
2. Research before planning prevents over-engineering (v1.1 Phase 8 avoided re-implementing existing code)
3. SUMMARY.md one_liner frontmatter needs enforcement -- manually extracting accomplishments at milestone completion is friction (recurring v1.0, v1.1, v1.2)
4. Unified models across similar subsystems (matched/unmatched for both music and TV) reduce complexity -- confirmed in v1.2 after v1.1 established the pattern

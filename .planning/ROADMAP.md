# Roadmap: Isomedia Codebase Audit & Hardening

## Overview

This milestone transforms the isomedia codebase from "works but fragile" to "works and verified." Security and error exposure fixes go first to stop leaking internals. Logic bugs that corrupt data are fixed next. Fragility is eliminated by consolidating duplicated patterns and adding startup validation. Finally, tests lock everything down -- unit tests for scanner and handlers, then integration tests for the match pipelines.

## Phases

**Phase Numbering:**
- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

- [ ] **Phase 1: Security & Error Exposure** - Eliminate internal error leakage, harden SQL construction, sanitize all HTTP responses
- [x] **Phase 2: Logic & Data Integrity Bugs** - Fix TV rescan overwrites, compilation detection ordering, merge corruption, detached goroutines, and silent failures (completed 2026-03-05)
- [ ] **Phase 3: Fragility Elimination** - Consolidate URL parsing, validate templates at startup, fail fast on missing templates
- [ ] **Phase 4: Unit Test Coverage** - Test scanner (music + TV) and handler (music, TV, settings) critical paths
- [ ] **Phase 5: Integration Test Coverage** - Test music match and TV match pipelines end-to-end

## Phase Details

### Phase 1: Security & Error Exposure
**Goal**: No internal implementation details leak to HTTP clients, and SQL construction is safe from injection
**Depends on**: Nothing (first phase)
**Requirements**: SEC-01, SEC-02, ERR-01, ERR-02
**Success Criteria** (what must be TRUE):
  1. Every HTTP error response shows a generic message -- no Go error strings, SQL text, or filesystem paths are visible in any response body
  2. Every handler that encounters an error logs the full error via slog before returning the generic HTTP error
  3. ensureColumn rejects table/column names that don't match a strict alphanumeric+underscore pattern, preventing SQL injection through schema evolution
  4. A developer can grep the handler code and confirm zero instances of raw err.Error() passed to http.Error or template rendering
**Plans:** 2 plans

Plans:
- [ ] 01-01-PLAN.md -- Harden ensureColumn with identifier validation + unit tests
- [ ] 01-02-PLAN.md -- Sanitize all HTTP error responses with httpError/jsonErr helpers

### Phase 2: Logic & Data Integrity Bugs
**Goal**: Scanning, matching, and merging produce correct, deterministic results regardless of filesystem order or timing
**Depends on**: Phase 1
**Requirements**: BUG-01, BUG-02, BUG-03, ERR-03, ERR-04
**Success Criteria** (what must be TRUE):
  1. Re-scanning a TV library preserves manually resolved episode identity (guessed_title, season, episode numbers) -- resolved files are not overwritten
  2. Running the music scanner twice on the same library with different filesystem walk orders produces identical compilation detection results
  3. mergeAlbumVariants called mid-scan does not orphan tracks or corrupt album associations -- all tracks remain linked to a valid album
  4. Approving a TV match enqueues metadata fetch through the job queue instead of spawning a detached goroutine
  5. Scanner errors on individual files are logged with the file path and error details, and scanning continues to the next file without silent data loss
**Plans:** 3/3 plans complete

Plans:
- [ ] 02-01-PLAN.md -- Fix TV rescan identity overwrite + replace detached goroutine with job queue
- [ ] 02-02-PLAN.md -- Move compilation detection and album merge to post-scan pass
- [ ] 02-03-PLAN.md -- Make music and TV scanners continue past per-file errors

### Phase 3: Fragility Elimination
**Goal**: Duplicated patterns are consolidated and the server fails fast on configuration errors instead of producing runtime 500s
**Depends on**: Phase 2
**Requirements**: FRAG-01, FRAG-02, FRAG-03
**Success Criteria** (what must be TRUE):
  1. URL path ID parsing is handled by a single shared helper function -- no handler contains its own path-splitting-and-parsing logic
  2. At server startup, every template name referenced by a handler is validated to exist in the compiled template set
  3. A missing or broken template file causes a clear, descriptive error at startup and prevents the server from accepting requests
**Plans**: TBD

Plans:
- [ ] 03-01: TBD

### Phase 4: Unit Test Coverage
**Goal**: Scanner and handler critical paths have automated tests that verify correctness and catch regressions from phases 1-3
**Depends on**: Phase 3
**Requirements**: TEST-01, TEST-02, TEST-03, TEST-04, TEST-05, TEST-06
**Success Criteria** (what must be TRUE):
  1. Music scanner ScanFile() tests pass covering tag reading, artist/album/track upsert, and art extraction -- verifiable via `go test ./internal/scan/ -run Music`
  2. Compilation detection tests cover mixed-artist albums, "Various Artists" tagging, and re-scan consistency -- verifiable via `go test ./internal/scan/ -run Compil`
  3. TV scanner ScanTV() tests cover file identification, upsert, and rescan behavior (including the BUG-01 fix) -- verifiable via `go test ./internal/scan/ -run TV`
  4. Music, TV, and settings handler tests verify correct routing, ID parsing via shared helper, and generic error responses -- verifiable via `go test ./internal/web/`
**Plans**: TBD

Plans:
- [ ] 04-01: TBD
- [ ] 04-02: TBD

### Phase 5: Integration Test Coverage
**Goal**: Match pipelines have automated tests that verify the full match-score-fetch-enrich flow for both music and TV
**Depends on**: Phase 4
**Requirements**: TEST-07, TEST-08
**Success Criteria** (what must be TRUE):
  1. RunMusicMatch() integration tests exercise the full pipeline (MusicBrainz search, scoring, CAA art fetch, artist enrichment) using mocked external APIs -- verifiable via `go test ./internal/match/ -run Integration`
  2. RunTVMatch() integration tests exercise the full pipeline (TMDB search, metadata fetch, art download) using mocked external APIs -- verifiable via `go test ./internal/match/ -run TVIntegration`
  3. Both test suites verify error handling: partial failures in external APIs do not crash the pipeline and produce appropriate logged warnings
**Plans**: TBD

Plans:
- [ ] 05-01: TBD

## Progress

**Execution Order:**
Phases execute in numeric order: 1 -> 2 -> 3 -> 4 -> 5

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Security & Error Exposure | 2/2 | Complete | 2026-03-05 |
| 2. Logic & Data Integrity Bugs | 3/3 | Complete   | 2026-03-05 |
| 3. Fragility Elimination | 0/? | Not started | - |
| 4. Unit Test Coverage | 0/? | Not started | - |
| 5. Integration Test Coverage | 0/? | Not started | - |

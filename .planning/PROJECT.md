# Isomedia — Codebase Audit & Hardening

## What This Is

A comprehensive audit and hardening pass over the isomedia codebase — a locally-hosted media server built in Go with music, TV, and movie support. The goal is to find and fix logic bugs, error handling gaps, data flow issues, and architectural fragility across all packages, then add tests and guards to prevent regressions.

## Core Value

Every identified bug and logic flaw is fixed and verified — no known issues remain in the codebase after this work.

## Requirements

### Validated

- Music library scanning (filesystem walk, tag reading, artist/album/track upsert, art extraction) — existing
- MusicBrainz matching pipeline (search, scoring, CAA cover art, artist enrichment) — existing
- Tag writeback (MP3 id3v2 + FLAC/OGG/M4A via audiometa) — existing
- Album edit UI (DB-only updates, match_status='manual') — existing
- Title normalization and album duplicate detection/merge — existing
- TV library scanning (filesystem walk, ffprobe, episode identification) — existing
- TMDB TV matching pipeline (search, metadata caching, art download) — existing
- Music browse/play UI with streaming and art display — existing
- TV browse UI with series/season/episode detail and streaming — existing
- SSH pubkey authentication with session cookies and CSRF protection — existing
- Background job queue with progress tracking and cancellation — existing
- Path traversal prevention via pathguard — existing
- Docker deployment (single container, non-root, multi-stage build) — existing

### Active

- [ ] Fix all identified logic bugs and error handling gaps
- [ ] Eliminate data flow issues (race conditions, incorrect state transitions)
- [ ] Harden error handling (replace raw err.Error() exposure, add missing edge cases)
- [ ] Address architectural fragility (URL parsing duplication, template registration, compilation detection)
- [ ] Add test coverage for untested critical paths (scanner, handlers, match pipeline)
- [ ] Fix security concerns (internal error exposure, ensureColumn injection surface)

### Out of Scope

- New features (movie scanning, user management UI, CLI implementation) — separate milestone
- Major refactoring (store layer extraction, handler decomposition) — architectural debt, not bugs
- Performance optimization (N+1 queries, missing indexes, double directory walk) — separate milestone
- Scaling improvements (multi-worker jobs, database partitioning) — not needed for personal server

## Context

The codebase has been through rapid feature development (phases 2a through 2d) focused on music and TV functionality. A codebase mapping analysis identified several categories of issues:

- **Logic bugs**: TV identity overwrite on rescan, compilation detection fragility
- **Error handling**: 100+ instances of raw Go errors sent to HTTP clients, silent failures in background goroutines
- **Data flow**: Detached goroutine in TV match approve, scanner compilation merging affected by scan order
- **Fragility**: URL path parsing duplication (6+ handlers), static template registration, no startup validation

The codebase has good test coverage for isolated utilities (config, db, auth, pathguard, match scoring, TV identification) but zero coverage for the critical orchestration code (scanner, handlers, match pipeline).

## Constraints

- **Tech stack**: Go stdlib + 4 direct dependencies — no new dependencies unless essential for fixes
- **Architecture**: Fixes should work within the existing architecture, not require major restructuring
- **Compatibility**: All fixes must preserve existing behavior for working features
- **Testing**: Use standard `testing` package with existing patterns (table-driven, `openTestDB`, `httptest`)

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Fix bugs before adding features | Compounding risk — bugs in foundation affect everything built on top | — Pending |
| Keep refactoring out of scope | Separates "fix what's broken" from "improve how it's organized" | — Pending |
| Focus tests on critical paths | Scanner, handlers, and pipeline are highest-risk untested code | — Pending |

---
*Last updated: 2026-03-05 after initialization*

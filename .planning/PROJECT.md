# Isomedia — Media Server

## What This Is

A locally-hosted media server built from scratch in Go. Music, TV, Movies with automatic metadata matching. Single Docker container, SQLite for storage, server-rendered HTML templates with vanilla CSS/JS. Audited and hardened codebase with comprehensive test coverage.

## Core Value

A personal media server that just works -- reliable scanning, matching, and streaming with no external service dependencies at runtime.

## Requirements

### Validated

- Music library scanning (filesystem walk, tag reading, artist/album/track upsert, art extraction) -- existing
- MusicBrainz matching pipeline (search, scoring, CAA cover art, artist enrichment) -- existing
- Tag writeback (MP3 id3v2 + FLAC/OGG/M4A via audiometa) -- existing
- Album edit UI (DB-only updates, match_status='manual') -- existing
- Title normalization and album duplicate detection/merge -- existing
- TV library scanning (filesystem walk, ffprobe, episode identification) -- existing
- TMDB TV matching pipeline (search, metadata caching, art download) -- existing
- Music browse/play UI with streaming and art display -- existing
- TV browse UI with series/season/episode detail and streaming -- existing
- SSH pubkey authentication with session cookies and CSRF protection -- existing
- Background job queue with progress tracking and cancellation -- existing
- Path traversal prevention via pathguard -- existing
- Docker deployment (single container, non-root, multi-stage build) -- existing
- All HTTP error responses sanitized (no internal details leaked) -- v1.0
- ensureColumn SQL injection prevention via identifier validation -- v1.0
- TV rescan preserves resolved episode identity -- v1.0
- Deterministic compilation detection (post-scan finalization) -- v1.0
- Safe album variant merging (no mid-scan corruption) -- v1.0
- TV match approve uses job queue (no detached goroutines) -- v1.0
- Scanner per-file error resilience with structured logging -- v1.0
- Shared URL path ID parsing (pathID/pathSegment helpers) -- v1.0
- Template startup validation with fail-fast -- v1.0
- Unit tests for scanner, handler, and settings critical paths -- v1.0
- Integration tests for music and TV match pipelines -- v1.0

### Active

- [ ] Auto-match: scanner triggers MusicBrainz matching using only artist name, album name, track name
- [ ] Auto-accept: matches scoring 80% or higher are automatically accepted (highest score wins)
- [ ] Auto-writeback: matched metadata (normalized names, artist MBID, album MBID) written back to file tags
- [ ] Full enrichment: CAA cover art + Wikipedia bio + Wikimedia artist image fetched on auto-match
- [ ] Silent skip: songs below 80% match threshold are left with original metadata, no flagging
- [ ] Manual review UI retained for manually matching songs that didn't auto-match

### Out of Scope

- New features (movie scanning, user management UI, CLI implementation) -- separate milestone
- Major refactoring (store layer extraction, handler decomposition) -- architectural debt
- Performance optimization (N+1 queries, missing indexes, double directory walk) -- separate milestone
- Scaling improvements (multi-worker jobs, database partitioning) -- not needed for personal server
- New upload UI -- files arrive via filesystem, scanner detects them

## Current Milestone: v1.1 Automated Music Match Pipeline

**Goal:** Automate the music metadata matching and writeback flow so scanned songs are automatically matched, enriched, and tagged without manual intervention.

**Target features:**
- Scanner-triggered MusicBrainz matching (trust only artist/album/track name)
- Auto-accept at 80%+ match score with automatic tag writeback (MBIDs + normalized names)
- Full enrichment pipeline (CAA cover art, Wikipedia bio, Wikimedia artist image)
- Silent skip for low-confidence matches; manual review UI preserved for fallback

## Context

Shipped v1.0 (Codebase Audit & Hardening) on 2026-03-06. Codebase is 14,048 LOC Go across 83 modified files. All identified bugs fixed, error handling standardized, fragile patterns consolidated, and comprehensive test coverage added for scanner, handler, and match pipeline critical paths. Full test suite passes across all packages.

Tech stack: Go 1.23, SQLite (WAL mode via modernc.org/sqlite), stdlib http.ServeMux, html/template. Four direct dependencies: dhowden/tag, modernc.org/sqlite, bogem/id3v2/v2, gcottom/audiometa/v3.

Known tech debt: 3 direct http.Error calls bypass httpError slog logging pattern (safe messages, non-blocking).

## Constraints

- **Tech stack**: Go stdlib + 4 direct dependencies -- no new dependencies unless essential
- **Architecture**: Single Docker container, SQLite, server-rendered HTML
- **Compatibility**: All changes must preserve existing behavior for working features
- **Testing**: Standard `testing` package with existing patterns (table-driven, `openTestDB`, `httptest`)

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Fix bugs before adding features | Compounding risk -- bugs in foundation affect everything built on top | Good -- found and fixed 3 data integrity bugs plus 100+ error handling gaps |
| Keep refactoring out of scope | Separates "fix what's broken" from "improve how it's organized" | Good -- kept milestone focused, identified ARCH-01/02/03 for future |
| Focus tests on critical paths | Scanner, handlers, and pipeline are highest-risk untested code | Good -- 49+ test subtests plus 4 integration tests covering all critical paths |
| httpError/jsonErr with severity-appropriate logging | 5xx via slog.Error, 4xx via slog.Warn | Good -- clean separation of severity levels |
| Post-scan finalization pattern | Heavy heuristics (compilation, merge) run after WalkDir, not per-file | Good -- eliminated ordering dependency and mid-scan corruption |
| WHERE clause on DO UPDATE for TV rescan | Atomic guard at SQL level instead of application-level check | Good -- simpler and more reliable than read-check-write |
| baseURL/apiBase struct fields for test injection | Unexported fields with production defaults, same-package test access | Good -- zero public API changes, full pipeline testability |

---
*Last updated: 2026-03-06 after v1.1 milestone started*

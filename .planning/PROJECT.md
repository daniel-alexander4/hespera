# Isomedia — Media Server

## What This Is

A locally-hosted media server built from scratch in Go. Music, TV, Movies with automatic metadata matching. Single Docker container, SQLite for storage, server-rendered HTML templates with vanilla CSS/JS. Audited and hardened codebase with comprehensive test coverage. Music scan-to-tag pipeline fully automated: scan, match, enrich, writeback in one pass.

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
- Auto-match: scanner triggers MusicBrainz matching with 80% auto-accept threshold -- v1.1
- Auto-writeback: matched metadata (normalized names, MBIDs) written back to file tags inline -- v1.1
- Full enrichment on auto-match: CAA cover art + Wikipedia bio + Wikimedia artist image -- v1.1
- Silent skip for below-threshold matches, manual review UI preserved for fallback -- v1.1

### Active

- [ ] Auto-trigger TMDB matching after TV scan
- [ ] Auto-accept high-confidence TV matches
- [ ] Silent skip for below-threshold TV matches
- [ ] TMDB enrichment (poster art, episode metadata) applied inline

### Out of Scope

- New features (movie scanning, user management UI, CLI implementation) -- separate milestone
- Major refactoring (store layer extraction, handler decomposition) -- architectural debt
- Performance optimization (N+1 queries, missing indexes, double directory walk) -- separate milestone
- Scaling improvements (multi-worker jobs, database partitioning) -- not needed for personal server
- New upload UI -- files arrive via filesystem, scanner detects them

## Current Milestone: v1.2 TV Auto-Match Pipeline

**Goal:** Automate TV matching the same way v1.1 automated music -- scan triggers TMDB matching, high-confidence results auto-accepted, below-threshold skipped for manual review.

**Target features:**
- Auto-trigger TMDB matching after TV library scan
- Auto-accept matches above confidence threshold
- Skip below-threshold matches silently (manual review UI preserved)
- TMDB enrichment (poster art, episode metadata) applied inline during match

## Context

Shipped v1.1 (Automated Music Match Pipeline) on 2026-03-07. Codebase is 14,700 LOC Go. Full scan-to-tag pipeline automated: scan triggers MusicBrainz matching, 80%+ auto-accepted, names normalized to MusicBrainz canonical values, MBIDs + metadata written back to file tags inline, cover art + artist bio/image enrichment runs automatically. Below-threshold albums silently skipped; manual review UI preserved with "Run Match" button for fallback.

Previously shipped v1.0 (Codebase Audit & Hardening) on 2026-03-06. All bugs fixed, error handling standardized, comprehensive test coverage added.

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
| 80% auto-accept threshold | Simple, aggressive -- user wants automatic matching, not curation | Good -- clean two-state model (matched/unmatched), no uncertain queue |
| Inline writeback in matchAlbum | Tag writing happens same pass as matching, no separate job needed | Good -- eliminates writeback as a separate step users must trigger |
| writebackAlbumTracks as package-level function | Unexported, takes db+albumID directly for testability | Good -- cleanly separated from library-wide RunTagWriteback |
| Enrichment already wired from Phase 2b | Cover art + artist bio/image already ran in pipeline, Phase 8 just verified | Good -- avoided re-implementing existing functionality |

---
*Last updated: 2026-03-07 after v1.2 milestone started*

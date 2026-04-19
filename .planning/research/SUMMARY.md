# Project Research Summary

**Project:** isomedia v1.3 Manual Controls
**Domain:** Media server manual controls (artwork, metadata editing, match selection)
**Researched:** 2026-03-07
**Confidence:** HIGH

## Executive Summary

The v1.3 milestone adds three manual control features to an existing, well-structured Go media server: manual artwork upload, a track number editing bug fix, and manual MusicBrainz match selection for music. All four research tracks converge on one conclusion: no new dependencies, no new infrastructure, and no schema changes are needed. Every feature maps directly onto existing codebase patterns -- the TV match review already implements the exact UI pattern needed for music match selection, the Cover Art Archive client already demonstrates the file storage pattern for artwork uploads, and the album edit handler already has the form processing structure that just needs its non-writeback path completed.

The recommended approach is to build these three features in strict dependency order: track number bug fix first (smallest scope, pure handler fix), artwork upload second (self-contained, no external APIs), manual match selection third (most complex, external API integration). This ordering minimizes risk by validating the simplest changes first and building the most complex feature last when the other two are stable. The three features are independent -- none depends on another -- but the ordering reflects increasing implementation complexity.

The primary risks are security-related on the upload path (image validation, path traversal) and completeness-related on the match selection path (applying the full post-match pipeline, not just updating the status field). Both are well-understood and have clear prevention strategies documented in the codebase's existing patterns. The MusicBrainz rate limiter's per-instance design is a moderate risk for interactive search that needs addressing, either by sharing a single client on the Handler struct or by frontend debounce.

## Key Findings

### Recommended Stack

No changes to the technology stack. All three features build on Go stdlib and the existing four dependencies. The explicit project constraint of "no new dependencies unless essential" is fully satisfied.

**Core technologies (all existing):**
- **Go stdlib `net/http`**: Multipart form parsing (`ParseMultipartForm`, `FormFile`, `MaxBytesReader`) for artwork upload -- standard pattern, no library needed
- **Go stdlib `net/http`**: `DetectContentType` for MIME validation on uploaded images -- checks magic bytes, not file extension
- **`modernc.org/sqlite`**: All DB updates (track numbers, art paths, match data) use existing columns with no schema migration
- **Existing `match` package**: `MBClient.SearchReleaseGroups`, `ScoreCandidate`, `CAAClient.FetchCover` are already built and tested -- manual match selection reuses them directly

### Expected Features

**Must have (table stakes):**
- Manual artwork upload for albums -- albums without CAA art are stuck with placeholders; users need a way to fix this
- Track number edit fix -- the non-writeback edit mode silently discards per-track changes; this is a confirmed bug
- Manual MusicBrainz match selection for music -- unmatched albums are a dead end with no way to pick from candidates
- Artwork delete/replace -- once art is uploaded, users need to swap or remove it

**Should have (differentiators):**
- Artwork preview before save -- client-side `FileReader`, zero server cost
- Candidate scoring display -- surface the score breakdown next to each MusicBrainz candidate so users understand match quality
- Artist artwork upload -- same pattern as album art, lower priority since Wikimedia enrichment handles most cases

**Defer (v2+):**
- Drag-and-drop artwork (polish, upload button is sufficient)
- Bulk track renumber (edge case for compilations)
- URL-based artwork fetch (SSRF risk, scope creep)
- Full metadata editor for all ID3 fields (scope creep -- point users to MusicBrainz Picard)
- Match candidate caching (unnecessary for single-user interactive use)

### Architecture Approach

All changes land in existing files with no new packages, no new tables, and no new architectural patterns. The three features add 3 new routes and modify 1 existing handler. The component boundaries remain unchanged: handlers in `internal/web/`, match logic in `internal/match/`, templates in `web/templates/`. The key architectural decision is to extract a shared `applyMatch()` function from the existing `matchAlbum()` pipeline so both auto-match and manual-match can trigger the same post-selection steps (CAA art fetch, title normalization, tag writeback).

**Major components (modified, not new):**
1. **`handlers_music.go`** -- Add `musicAlbumArtUpload` handler; fix `musicAlbumEditPOST` non-writeback path to process per-track form fields via DB UPDATE
2. **`handlers_match.go`** -- Add `musicMatchSearch` (GET, JSON) and `musicMatchSelect` (POST, applies match + pipeline)
3. **`music_match_review.html`** -- Add search input + candidate dropdown per album row, mirroring the existing `tv_match_review.html` pattern exactly

### Critical Pitfalls

1. **Image upload without magic byte validation** -- Must use `http.DetectContentType` on first 512 bytes, reject SVG entirely, enforce 10MB limit via `MaxBytesReader`. Trusting Content-Type headers or extensions allows DoS and stored XSS.
2. **Path traversal on art save** -- Generate filenames server-side via SHA hash (matching existing `coverart.go` pattern), never use the uploaded filename. Save to the existing `thumbs/music/` directory that pathguard already trusts.
3. **Non-writeback edit form inputs silently ignored** -- The handler never reads `track_no_{ID}` / `track_title_{ID}` form values. Fix requires adding per-track iteration in the non-writeback POST path with DB-only updates.
4. **Manual match applied without full pipeline** -- Must run the same post-match steps as auto-match: title normalization, artist normalization, CAA art fetch, tag writeback. Extract shared `applyMatch()` function.
5. **MBClient rate limiter is per-instance** -- Each handler creates a new `MBClient` with its own throttle state. Concurrent manual searches bypass the 1 req/sec limit. Fix: share a single `MBClient` on the Handler struct.

## Implications for Roadmap

Based on research, suggested phase structure:

### Phase 1: Track Number Edit Fix
**Rationale:** Smallest scope, highest certainty, zero new surface area. Pure bug fix in an existing handler. Ship first to reduce regression risk and deliver immediate value to users hitting this bug.
**Delivers:** Working track number and title editing in non-writeback mode. Clean separation of writeback (file tags) vs non-writeback (DB-only) edit paths.
**Addresses:** Track number edit fix (table stakes), track zero sentinel issue (known limitation, document)
**Avoids:** Pitfall 3 (form inputs never read), Pitfall 7 (zero sentinel -- accept as known limitation)

### Phase 2: Manual Artwork Upload
**Rationale:** Self-contained feature with no external API dependency. Exercises the multipart upload pattern in isolation before the more complex match selection feature. Directly addresses visible gaps (placeholder art).
**Delivers:** Album art upload/replace/delete. Upload form on album detail page. Server-side image validation. Hash-based filename storage in existing thumbs directory.
**Addresses:** Manual artwork upload (table stakes), artwork delete/replace (table stakes), artwork preview (differentiator, client-side only)
**Avoids:** Pitfall 1 (image validation), Pitfall 2 (path traversal), Pitfall 8 (orphaned files -- accept for v1.3)

### Phase 3: Manual Match Selection
**Rationale:** Most complex feature, external API dependency, builds on patterns proven in the TV match UI. Build last when the other two are stable. Requires extracting shared `applyMatch()` from the existing pipeline.
**Delivers:** MusicBrainz search endpoint, candidate display with scores, manual match selection that triggers full post-match pipeline (art, normalization, writeback). Resolves the "dead end" problem for unmatched albums.
**Addresses:** Manual match selection for music (table stakes), candidate scoring display (differentiator)
**Avoids:** Pitfall 5 (missing search endpoint -- build it), Pitfall 6 (incomplete pipeline -- extract applyMatch), Pitfall 9 (rate limiter -- share MBClient), Pitfall 10 (multi-field candidate -- pass all fields via hidden form values)

### Phase Ordering Rationale

- **Risk escalation:** Each phase is more complex than the last. Phase 1 changes one handler, Phase 2 adds a new handler + template change, Phase 3 adds two new handlers + template JS + refactors existing pipeline code.
- **Independence:** All three features are independent -- no phase depends on another. But the ordering lets each phase's testing validate increasingly complex patterns.
- **No schema changes in any phase:** All three work within existing DB columns. This eliminates migration risk entirely.
- **Existing patterns everywhere:** Phase 1 extends existing handler logic, Phase 2 follows `coverart.go` storage pattern, Phase 3 mirrors `tv_match_review.html` UI pattern. No novel architecture.

### Research Flags

Phases with standard patterns (skip `/gsd:research-phase`):
- **Phase 1 (Track Fix):** Pure bug fix with clear root cause analysis. The handler code, template, and fix approach are fully documented in research. No ambiguity.
- **Phase 2 (Art Upload):** Standard Go multipart upload pattern. Existing `coverart.go` provides the exact storage pattern. Well-documented in research.

Phases that may benefit from brief research during planning:
- **Phase 3 (Manual Match Selection):** The `applyMatch()` extraction from `matchAlbum()` needs careful scoping to avoid breaking the auto-match pipeline. The MBClient sharing approach (moving to Handler struct) has implications for the job queue path that also uses MBClient. A quick review of these integration points during planning would be valuable, though the pattern is clear.

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | HIGH | No new dependencies. All capabilities verified against existing codebase and Go stdlib docs. |
| Features | HIGH | Feature set derived from codebase analysis and comparison with existing TV implementation. Clear table stakes vs differentiators. |
| Architecture | HIGH | All analysis from direct codebase inspection. Existing patterns (TV match, CAA storage) provide exact templates for new features. |
| Pitfalls | HIGH | All pitfalls identified from actual code reading. Security concerns (upload validation, path traversal) verified against Go stdlib behavior. Rate limiter issue confirmed by reading MBClient instantiation pattern. |

**Overall confidence:** HIGH

All research was derived from direct codebase analysis rather than external sources. The existing TV match implementation provides a battle-tested template for the music equivalent. The upload security patterns are standard Go practices with stdlib support.

### Gaps to Address

- **MBClient sharing strategy:** The recommended approach (share on Handler struct) needs validation that it does not conflict with the job queue's `match.New()` pattern. The job worker may need its own MBClient instance to avoid contention with interactive search. Resolve during Phase 3 planning.
- **Orphaned art file cleanup:** Accepted as deferred for v1.3. Track as tech debt for a future cleanup utility or scan-time pruning.
- **Track number zero sentinel:** Accepted as a known limitation. Document in user-facing notes. Could be fixed with a "field present" check if users report it as a problem.

## Sources

### Primary (HIGH confidence)
- Direct codebase analysis: `handlers_music.go`, `handlers_match.go`, `handlers_tv.go`, `match/pipeline.go`, `match/musicbrainz.go`, `match/coverart.go`, `match/scorer.go`, `music/tagwrite.go`, `db/migrate.go`
- Existing UI patterns: `tv_match_review.html` (search/approve flow), `music_album_edit.html` (edit form), `music_album.html` (album detail)
- Go stdlib documentation: `net/http` multipart handling, `http.DetectContentType`, `io.LimitReader`

### Secondary (MEDIUM confidence)
- [MusicBrainz API Rate Limiting](https://musicbrainz.org/doc/MusicBrainz_API/Rate_Limiting) -- 1 req/sec limit confirmed
- [MusicBrainz API Search](https://musicbrainz.org/doc/MusicBrainz_API/Search) -- search endpoint returns scored results
- [Jellyfin Metadata docs](https://jellyfin.org/docs/general/server/metadata/) -- confirms "Identify" (manual match) is standard in media servers

---
*Research completed: 2026-03-07*
*Ready for roadmap: yes*

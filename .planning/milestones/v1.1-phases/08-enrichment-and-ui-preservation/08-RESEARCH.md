# Phase 8: Enrichment and UI Preservation - Research

**Researched:** 2026-03-07
**Domain:** Match pipeline enrichment (CAA, Wikipedia/Wikimedia), manual review UI
**Confidence:** HIGH

## Summary

Phase 8 covers three requirements: cover art fetched on auto-match (ENRICH-01), artist bio/image fetched on auto-match (ENRICH-02), and manual review UI continuing to work for unmatched albums (UI-01). After thorough analysis of the codebase, all three capabilities are already functionally implemented in the match pipeline. The enrichment happens during `RunMusicMatch`: artist enrichment (MBID, bio, image) runs first via `enrichArtists()`, then album matching runs via `matchAlbums()` which calls `matchAlbum()` per album -- and `matchAlbum()` already fetches cover art from Cover Art Archive after a successful auto-match.

The match review UI exists at `/music/match/review` and correctly displays unmatched albums with "Reject" and "Re-match" actions. However, there is one UX gap: the review page lacks a "Run Match" button, so after a user clicks "Re-match" (which resets match_status to ''), they must navigate to the Libraries page to trigger the match pipeline. Adding a match trigger button on the review page would fully close UI-01.

**Primary recommendation:** This phase is primarily verification plus one small UI addition. Add a "Run Match" button to the match review page, then verify the end-to-end flow with existing integration tests. No new packages, no new architecture.

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| ENRICH-01 | Cover art is fetched from Cover Art Archive on auto-match | Already implemented in `matchAlbum()` lines 273-287 of `pipeline.go`. After a match with score >= 80, `m.caa.FetchCover()` is called and art_path stored in DB. Verified by `TestRunMusicMatchIntegrationHappyPath/album_art`. |
| ENRICH-02 | Artist bio (Wikipedia) and artist image (Wikimedia Commons) are fetched on auto-match | Already implemented in `enrichArtists()` called at line 34 of `RunMusicMatch()`. Fetches MBID via `SearchArtist()`, then bio/image via `EnrichArtist()`. Verified by `TestRunMusicMatchIntegrationHappyPath/artist_bio` and `artist_art`. |
| UI-01 | Existing match review UI remains functional for manually matching songs that didn't auto-match | Review page exists at `/music/match/review`, shows unmatched albums, has Reject/Re-match actions. Gap: no "Run Match" button on review page -- user must go to Libraries page after clicking Re-match. |
</phase_requirements>

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| stdlib `net/http` | Go 1.23 | HTTP handlers, routing | Project's established pattern |
| stdlib `database/sql` | Go 1.23 | SQLite queries | Project's established pattern |
| stdlib `html/template` | Go 1.23 | Server-rendered templates | Project's established pattern |

### Supporting
No new libraries needed. All enrichment functionality (CAA, Wikipedia, Wikimedia) already exists in `internal/match/`.

## Architecture Patterns

### Existing Pipeline Flow (No Changes Needed)

```
RunMusicMatch(ctx, jobID, libraryID)
  |
  +-- enrichArtists()          <-- ENRICH-02: bio, image, MBID
  |     |
  |     +-- SearchArtist()     (MusicBrainz MBID lookup)
  |     +-- EnrichArtist()     (Wikipedia bio + Wikimedia image)
  |
  +-- matchAlbums()
        |
        +-- matchAlbum()       (per album)
              |
              +-- SearchReleaseGroups()  (MusicBrainz)
              +-- BestCandidate()        (scoring, >= 80 threshold)
              +-- UPDATE match_status    (DB)
              +-- Normalize names        (DB)
              +-- FetchCover()           <-- ENRICH-01: CAA cover art
              +-- writebackAlbumTracks() (file tags)
```

### Manual Review UI Flow (Current)

```
User visits /music/match/review
  |
  +-- Shows unmatched albums (match_status='unmatched')
  |
  +-- User clicks "Re-match" on album
  |     |
  |     +-- POST /music/match/rematch  (resets match_status to '')
  |     +-- Redirect back to /music/match/review
  |
  +-- User SEPARATELY goes to Libraries page
  |     |
  |     +-- Clicks "Match" button for the library
  |     +-- POST /music/match (enqueues RunMusicMatch)
  |     +-- Pipeline picks up reset album and re-matches
```

### Improved Review UI Flow (Phase 8 Addition)

```
User visits /music/match/review
  |
  +-- Shows unmatched albums
  +-- "Run Match" button visible when LibraryID is known
  |     |
  |     +-- POST /music/match (same endpoint as Libraries page)
  |     +-- Triggers full pipeline including enrichment + writeback
```

### Key Code Locations

| File | Lines | What It Does |
|------|-------|--------------|
| `internal/match/pipeline.go` | 32-40 | `RunMusicMatch` -- orchestrator |
| `internal/match/pipeline.go` | 44-147 | `enrichArtists` -- artist MBID, bio, image |
| `internal/match/pipeline.go` | 219-295 | `matchAlbum` -- per-album match + cover art + writeback |
| `internal/match/coverart.go` | 55-84 | `FetchCover` -- CAA fetch with release-group/release fallback |
| `internal/match/artistmeta.go` | 29-117 | `EnrichArtist` -- Wikipedia bio + Wikimedia image |
| `internal/web/handlers_match.go` | 68-114 | `musicMatchReview` -- review page handler |
| `internal/web/handlers_match.go` | 16-55 | `musicMatch` -- enqueue match job |
| `web/templates/music_match_review.html` | 1-66 | Review UI template |

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Cover art fetching | New CAA client | Existing `CAAClient.FetchCover()` | Already handles release-group/release fallback, thumbnail preference, download+save |
| Artist enrichment | New Wikipedia/Wikimedia clients | Existing `EnrichArtist()` | Already handles Wikidata entity parsing, P18 extraction, bio fetching |
| Match pipeline | New orchestration | Existing `RunMusicMatch()` | Already sequences enrichment -> matching -> art -> writeback |

**Key insight:** This phase does NOT require building new enrichment code. The enrichment pipeline was built in Phase 2b and has been running successfully. Phases 6 and 7 wired in auto-matching and writeback. The enrichment was already there.

## Common Pitfalls

### Pitfall 1: Thinking enrichment needs to be added
**What goes wrong:** Building new enrichment code when it already exists in the pipeline
**Why it happens:** Phase description says "Auto-match triggers full enrichment" which sounds like new work
**How to avoid:** Read `pipeline.go` -- `enrichArtists()` and `FetchCover()` are already called
**Warning signs:** Any new code in coverart.go or artistmeta.go

### Pitfall 2: Breaking existing match review UI
**What goes wrong:** Modifying the review page query or handler in a way that hides albums
**Why it happens:** Changing the WHERE clause or match_status values
**How to avoid:** The review page queries `WHERE match_status = 'unmatched'` -- auto-match sets unmatched albums to exactly this status. Don't change the query.
**Warning signs:** Empty review page when unmatched albums exist

### Pitfall 3: Re-match without triggering pipeline
**What goes wrong:** User clicks "Re-match" but has no way to trigger the match pipeline from the review page
**Why it happens:** "Re-match" only clears match_status -- it doesn't enqueue a job
**How to avoid:** Add a "Run Match" button that POSTs to `/music/match` with the library ID
**Warning signs:** User has to navigate away from review page to trigger matching

### Pitfall 4: Testing with real external services
**What goes wrong:** Tests hit MusicBrainz/Wikipedia/Wikimedia causing flakiness and rate limiting
**Why it happens:** Not using the existing mock server infrastructure
**How to avoid:** Use `newMockMusicServer(t)` and `newTestMatcher(t, db, srv)` from `pipeline_integration_test.go`
**Warning signs:** Test files importing `net/http` without `httptest`

## Code Examples

### Existing: Cover Art Fetch in matchAlbum (already works)
```go
// Source: internal/match/pipeline.go lines 273-287
// Fetch cover art if we got a match.
if best.ReleaseGroupID != "" {
    var releaseIDs []string
    if best.ReleaseID != "" {
        releaseIDs = append(releaseIDs, best.ReleaseID)
    }
    artPath, artErr := m.caa.FetchCover(ctx, best.ReleaseGroupID, releaseIDs)
    if artErr != nil {
        slog.Warn("cover art fetch failed", "album_id", albumID, "err", artErr)
    } else if artPath != "" {
        // Only update art_path if currently empty (don't overwrite embedded art).
        _, _ = m.db.ExecContext(ctx,
            "UPDATE music_albums SET art_path=? WHERE id=? AND (art_path='' OR art_path IS NULL)",
            artPath, albumID)
    }
}
```

### Existing: Artist Enrichment in enrichArtists (already works)
```go
// Source: internal/match/pipeline.go lines 117-135
// Step 2: Fetch bio + image if missing.
if !hasBio || !hasArt {
    meta, err := EnrichArtist(ctx, m.mb, mbid, m.dataDir)
    if err != nil {
        slog.Warn("enrich artist failed", ...)
        continue
    }
    if !hasBio && meta.Bio != "" {
        _, _ = m.db.ExecContext(ctx,
            "UPDATE music_artists SET bio=?, bio_source_name=?, bio_source_url=? WHERE id=?",
            meta.Bio, meta.BioSourceName, meta.BioSourceURL, a.id)
    }
    if !hasArt && meta.ImagePath != "" {
        _, _ = m.db.ExecContext(ctx,
            "UPDATE music_artists SET art_path=? WHERE id=?",
            meta.ImagePath, a.id)
    }
}
```

### Needed: Match button on review page
```html
<!-- Add to music_match_review.html, alongside existing "Write Tags" button -->
{{if .LibraryID}}
<form method="post" action="/music/match" class="inline-form">
  <input type="hidden" name="id" value="{{.LibraryID}}" />
  <button type="submit" class="btn btn-sm btn-primary">Run Match</button>
</form>
{{end}}
```

### Existing: Review page shows unmatched albums (already works)
```go
// Source: internal/web/handlers_match.go lines 74-81
rows, err := h.db.QueryContext(r.Context(), `
    SELECT a.id, a.title, COALESCE(ar.name, ''), a.year, COALESCE(a.art_path, ''),
           a.match_status, COALESCE(a.match_confidence, 0), COALESCE(a.musicbrainz_id, '')
    FROM music_albums a
    LEFT JOIN music_artists ar ON ar.id = a.album_artist_id
    WHERE a.match_status = 'unmatched'
    ORDER BY a.match_confidence DESC, a.title ASC
`)
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Manual match then manual writeback | Auto-match with inline writeback | Phase 7 (2026-03-06) | Match + writeback in one pass |
| Three-state matching (matched/uncertain/unmatched) | Two-state (matched/unmatched) | Phase 6 (2026-03-06) | Simpler UI, clearer semantics |
| Separate enrichment job | Enrichment inline in match pipeline | Phase 2b (pre v1.1) | Always been integrated |

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go `testing` (stdlib) |
| Config file | None (stdlib) |
| Quick run command | `go test ./internal/match -run TestRunMusicMatch -count=1` |
| Full suite command | `go test ./...` |

### Phase Requirements to Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| ENRICH-01 | Cover art fetched on auto-match | integration | `go test ./internal/match -run TestRunMusicMatchIntegrationHappyPath/album_art -count=1` | Exists |
| ENRICH-02 | Artist bio + image fetched on auto-match | integration | `go test ./internal/match -run TestRunMusicMatchIntegrationHappyPath/artist_bio -count=1` | Exists |
| UI-01 | Review UI shows unmatched albums, match can be triggered | unit/manual | `go test ./internal/web -run TestHandler -count=1` + manual verification | Partial |

### Sampling Rate
- **Per task commit:** `go test ./internal/match -count=1 && go test ./internal/web -count=1`
- **Per wave merge:** `go test ./... -count=1`
- **Phase gate:** Full suite green before verification

### Wave 0 Gaps
- [ ] `internal/web/handlers_match_test.go` -- add test for review page rendering with unmatched albums (UI-01 verification)

## Open Questions

1. **Is there actually code to write?**
   - What we know: All three requirements (ENRICH-01, ENRICH-02, UI-01) are functionally implemented. The match pipeline already performs enrichment. The review UI already works.
   - What's unclear: Whether the phase is purely verification or if the "Run Match" button on the review page is expected.
   - Recommendation: Add the "Run Match" button to the review page (small template change), then verify end-to-end. The phase may be very lightweight.

2. **Should re-match trigger an inline match instead of a full library re-run?**
   - What we know: Currently "Re-match" clears status, then the user must trigger a full library match. This re-matches ALL unmatched albums, not just the one the user re-matched.
   - What's unclear: Whether the user wants per-album re-matching.
   - Recommendation: Keep the current approach (full library match). Per-album matching would require new code and the current approach already works. The full match skips already-matched albums, so re-running is cheap.

## Sources

### Primary (HIGH confidence)
- `internal/match/pipeline.go` -- Direct code analysis of match pipeline, enrichment, cover art
- `internal/match/coverart.go` -- CAA client implementation
- `internal/match/artistmeta.go` -- Wikipedia/Wikimedia enrichment
- `internal/match/pipeline_integration_test.go` -- Integration tests verifying enrichment flow
- `internal/web/handlers_match.go` -- Match review UI handler
- `web/templates/music_match_review.html` -- Review page template

### Secondary (HIGH confidence)
- `.planning/phases/07-automated-writeback/07-01-SUMMARY.md` -- Phase 7 completion details
- `.planning/REQUIREMENTS.md` -- Requirements mapping
- `.planning/ROADMAP.md` -- Phase 8 description and success criteria

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH - no new libraries needed, existing code covers all requirements
- Architecture: HIGH - pipeline flow analyzed line by line, all enrichment already present
- Pitfalls: HIGH - primary risk is over-engineering something that already works
- UI gap: HIGH - verified by reading template and handler code, "Run Match" button is missing from review page

**Research date:** 2026-03-07
**Valid until:** 2026-04-07 (stable -- no external API changes expected)

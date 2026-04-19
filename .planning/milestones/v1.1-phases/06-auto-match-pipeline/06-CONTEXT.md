# Phase 6: Auto-Match Pipeline - Context

**Gathered:** 2026-03-06
**Status:** Ready for planning

<domain>
## Phase Boundary

Scanner auto-triggers MusicBrainz matching with 80% auto-accept threshold and silent skip below. Eliminates the "uncertain" category. Albums either auto-match or stay unmatched for optional manual review. Writeback and enrichment are separate phases (7 and 8).

</domain>

<decisions>
## Implementation Decisions

### Threshold behavior
- Raise auto-accept threshold from 70% to 80%
- Eliminate "uncertain" match_status entirely — only "matched" and "unmatched" remain
- Albums scoring >= 80% get match_status='matched' (highest-scoring candidate wins)
- Albums scoring < 80% get match_status='unmatched'
- Migrate existing 'uncertain' rows to 'unmatched' (clean slate)

### Review UI query
- Drop 'uncertain' from review UI query — change `IN ('uncertain', 'unmatched')` to `= 'unmatched'`
- Minimal change — just adjust the WHERE clause

### Re-scan behavior
- Retry matching for unmatched albums on every scan (current behavior preserved)
- Never re-match albums with match_status='matched', 'manual', or 'skipped'
- Existing rematch handler (reset to empty match_status) already works for un-matching

### Search inputs
- Match at album level using artist name + album title (from file tags)
- Track name NOT used in matching (MusicBrainz album-level search only)
- Year stays as a minor scoring factor (0-4 points) — helps disambiguate but isn't a primary input
- Update MATCH-01 requirement to reflect: "artist name and album name" not "track name"

### Claude's Discretion
- Whether to make the 80% threshold a hardcoded constant or environment variable
- Scoring weight adjustments (if any) — current weights are fine, just threshold changes

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- `match.Matcher.matchAlbum()` (`internal/match/pipeline.go:219`): Core matching function — needs threshold change from 70 to 80 and elimination of "uncertain" status
- `match.Matcher.matchAlbums()` (`internal/match/pipeline.go:149`): Album iteration loop — query already filters to empty/unmatched, no change needed
- `match.BestCandidate()`: Scoring function — no changes needed, just the threshold that acts on its result
- `match.Matcher.RunMusicMatch()` (`internal/match/pipeline.go:32`): Pipeline entry point — already wired as chained job after scan

### Established Patterns
- Scan-then-match chaining: `handlers_settings.go:271` enqueues music_match job after scan completes
- Match status flow: '' → matched/unmatched (removing uncertain from the flow)
- Non-fatal per-album errors: failed matches logged and marked unmatched, pipeline continues
- 500ms inter-album delay for MusicBrainz rate limiting

### Integration Points
- `handlers_settings.go:271`: Scan chains match job — existing wiring, no change needed
- `handlers_match.go:79`: Review UI query — needs 'uncertain' removed
- `handlers_match.go:240`: Rematch handler — already works (resets match_status to '')
- Cover art fetch already happens in matchAlbum for matched albums — will be relevant for Phase 8

</code_context>

<specifics>
## Specific Ideas

- The change is surgical: primarily `matchAlbum()` threshold logic (line 239) and a DB migration for existing 'uncertain' rows
- Review UI query adjustment is a one-line change
- No new packages or dependencies needed

</specifics>

<deferred>
## Deferred Ideas

- Un-match button on album detail page (not just review page) — could be added later if the review-page-only access feels limiting

</deferred>

---

*Phase: 06-auto-match-pipeline*
*Context gathered: 2026-03-06*

# Phase 9: TV Match Threshold and Status Alignment - Context

**Gathered:** 2026-03-07
**Status:** Ready for planning

<domain>
## Phase Boundary

Raise the TV auto-match confidence threshold from 0.45 to 0.80 and align the TV match status model with the music pipeline (matched/unmatched). Preserve manual review UI for below-threshold series. No new UI, no new matching logic -- just threshold and status changes.

</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion

User deferred all gray areas to Claude. Decisions to make during planning:

- **Status naming**: Rename TV statuses from 'resolved'/'needs_fix' to 'matched'/'unmatched' to align with music pipeline. Update all DB queries, handler logic, and template conditionals.
- **Threshold value**: Use 0.80 to match music pipeline. TMDB scoring uses NormalizedSimilarity + popularity bonus (0-0.1 range), so 0.80 is achievable for good matches.
- **Migration strategy**: UPDATE existing 'resolved' rows to 'matched' and 'needs_fix' to 'unmatched' in a migration step. Keep it simple -- single UPDATE statement in the match pipeline or a db migration.

</decisions>

<specifics>
## Specific Ideas

No specific requirements -- mirror the music pipeline's approach. The user wants consistency between music and TV auto-match behavior.

</specifics>

<code_context>
## Existing Code Insights

### Reusable Assets
- `internal/match/similarity.go`: `NormalizedSimilarity` already shared between music and TV scoring
- `internal/match/pipeline.go`: Music pipeline's 0.80 threshold pattern to mirror

### Established Patterns
- Music uses `match_status` column with 'matched'/'unmatched' values
- TV uses `status` column on `tv_series_identities` with 'resolved'/'needs_fix'/'skipped' values
- Non-fatal per-item errors with slog.Warn and continue

### Integration Points
- `internal/tmdb/matcher.go:107`: Threshold check `bestScore < 0.45` -- change to 0.80
- `internal/tmdb/matcher.go:220-228`: Status assignment `status='resolved'` -- change to 'matched'
- `internal/web/handlers_tv.go:524+`: Review UI queries for 'needs_fix' -- update to 'unmatched'
- `internal/web/handlers_tv.go:565+`: tvMatchApprove/Skip handlers reference statuses
- `web/templates/tv_match_review.html`: May reference status values in conditionals

</code_context>

<deferred>
## Deferred Ideas

None -- discussion stayed within phase scope

</deferred>

---

*Phase: 09-tv-match-threshold-and-status-alignment*
*Context gathered: 2026-03-07*

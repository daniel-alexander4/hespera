# Phase 10: TV Match Test Coverage - Context

**Gathered:** 2026-03-07
**Status:** Ready for planning

<domain>
## Phase Boundary

Add unit tests for TV match scoring/threshold logic, integration tests for the TV auto-match pipeline, and tests verifying the match review UI works with matched/unmatched status model. No new features -- pure test coverage for Phase 9 changes.

</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion

User deferred all gray areas to Claude. Follow existing test patterns:

- **Test scope**: Cover the Phase 9 changes (0.80 threshold, matched/unmatched statuses) plus broader coverage of TV match pipeline behavior
- **Test patterns**: Follow v1.0 patterns -- openTestDB(t), httptest for mocked TMDB, table-driven tests, direct conditionals with t.Fatalf()
- **Mock strategy**: Use httptest.NewServer with handler func to mock TMDB API (same pattern as existing matcher_integration_test.go)
- **Unit tests**: Test pickBestResult scoring, threshold boundary (0.79 vs 0.80 vs 0.81), status assignment logic
- **Integration tests**: Full RunTVMatch pipeline with mocked TMDB -- verify auto-accept above threshold, unmatched below threshold
- **UI tests**: Handler tests for tvMatchReview/tvMatchApprove/tvMatchSkip verifying correct status queries and updates

</decisions>

<specifics>
## Specific Ideas

No specific requirements -- follow existing test patterns established in v1.0 (Phase 4-5).

</specifics>

<code_context>
## Existing Code Insights

### Reusable Assets
- `internal/tmdb/matcher_integration_test.go`: Already has TestRunTVMatchIntegrationHappyPath and PartialFailure -- was updated in Phase 9
- `internal/tmdb/client_test.go`: TMDB client tests with httptest mocks
- `internal/tvscan/scanner_test.go`: TV scanner tests with openTestDB
- `internal/match/scorer_test.go`: Music scorer unit test pattern to mirror for TV

### Established Patterns
- openTestDB(t) creates temp SQLite in t.TempDir()
- httptest.NewServer for API mocks
- Table-driven tests with subtests
- baseURL/apiBase struct fields for test injection

### Integration Points
- `internal/tmdb/matcher.go`: pickBestResult (scoring), RunTVMatch (pipeline)
- `internal/web/handlers_tv.go`: tvMatchReview, tvMatchApprove, tvMatchSkip handlers
- `web/templates/tv_match_review.html`: Template rendering with status values

</code_context>

<deferred>
## Deferred Ideas

None -- discussion stayed within phase scope

</deferred>

---

*Phase: 10-tv-match-test-coverage*
*Context gathered: 2026-03-07*

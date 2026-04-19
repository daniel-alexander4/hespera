---
phase: 05-integration-test-coverage
plan: 01
subsystem: testing
tags: [integration-test, httptest, musicbrainz, coverart, wikipedia, wikidata]

requires:
  - phase: 02b
    provides: MusicBrainz matching pipeline, artist enrichment, CAA client
provides:
  - Integration tests for RunMusicMatch pipeline with mocked external APIs
  - Testable MBClient/CAAClient via baseURL fields
  - Testable enrichment functions via optional client/base URL overrides
affects: [05-02, match, pipeline]

tech-stack:
  added: []
  patterns: [baseURL field injection for httptest mocking, relation-type URL matching for testability]

key-files:
  created:
    - internal/match/pipeline_integration_test.go
  modified:
    - internal/match/musicbrainz.go
    - internal/match/coverart.go
    - internal/match/artistmeta.go

key-decisions:
  - "Added wikiClient/wikiBaseURL/wikidataBaseURL/commonsBaseURL fields to MBClient for enrichment testability (beyond plan scope but required for full integration testing)"
  - "Extended EnrichArtist relation URL matching to check by relation type when base URL overrides are set"

patterns-established:
  - "baseURL injection: Add unexported baseURL field defaulting to production const, use in request building"
  - "Mock server routing: Single httptest.Server handles all external APIs via URL path routing"
  - "Rate limiter bypass: Set lastReq to time.Time{} so throttle() never sleeps in tests"

requirements-completed: [TEST-07]

duration: 6min
completed: 2026-03-06
---

# Phase 5 Plan 1: Match Pipeline Integration Tests Summary

**Integration tests for RunMusicMatch exercising full artist-enrichment and album-matching pipeline against httptest mock server**

## Performance

- **Duration:** 6 min
- **Started:** 2026-03-06T01:45:11Z
- **Completed:** 2026-03-06T01:52:09Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments
- MBClient and CAAClient now accept baseURL override for test injection (production behavior unchanged)
- Happy path integration test verifies full pipeline: artist MBID search, artist lookup, Wikipedia bio fetch, Wikidata entity fetch, Wikimedia image download, album release-group search, scoring, CAA cover art fetch
- Partial failure integration test proves artist enrichment failure does not prevent album matching
- All tests complete in ~3 seconds with no real HTTP calls

## Task Commits

Each task was committed atomically:

1. **Task 1: Add baseURL fields to MBClient and CAAClient** - `55adb7e` (feat)
2. **Task 2: Write RunMusicMatch integration tests** - `77ea942` (test)

## Files Created/Modified
- `internal/match/musicbrainz.go` - Added baseURL, wikiClient, wikiBaseURL, wikidataBaseURL, commonsBaseURL fields to MBClient; changed get() to use c.baseURL
- `internal/match/coverart.go` - Added baseURL field to CAAClient; changed FetchCover() to use c.baseURL
- `internal/match/artistmeta.go` - Modified fetchWikipediaSummary, fetchWikidataEntity, downloadArtistImage to accept optional client/base URL; extended relation URL matching for testability
- `internal/match/pipeline_integration_test.go` - Two integration tests with mock server, test helpers (newMockMusicServer, newTestMatcher, seedTestData)

## Decisions Made
- Added wikiClient/wikiBaseURL/wikidataBaseURL/commonsBaseURL to MBClient: enrichment functions create ad-hoc HTTP clients with hardcoded external URLs, making them untestable without these overrides
- Extended EnrichArtist relation URL matching to also check by relation type when base URL overrides are set, so mock URLs (without wikipedia.org/wikidata.org domains) are recognized

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Made enrichment functions testable via client/baseURL overrides**
- **Found during:** Task 1 (preparing testability changes)
- **Issue:** fetchWikipediaSummary, fetchWikidataEntity, and downloadArtistImage create ad-hoc http.Client instances with hardcoded external URLs (wikipedia.org, wikidata.org, commons.wikimedia.org). The plan's mock server approach cannot intercept these calls without code changes.
- **Fix:** Added optional client and base URL parameters to all three functions. Added wikiClient, wikiBaseURL, wikidataBaseURL, commonsBaseURL fields to MBClient. When nil/empty, functions fall back to original behavior (create ad-hoc clients with hardcoded URLs).
- **Files modified:** internal/match/musicbrainz.go, internal/match/artistmeta.go
- **Verification:** All existing tests pass unchanged; integration tests successfully mock all external services
- **Committed in:** 55adb7e (Task 1 commit)

**2. [Rule 3 - Blocking] Extended relation URL matching for mock URLs**
- **Found during:** Task 2 (integration test failing on artist bio/art)
- **Issue:** EnrichArtist checks for `wikipedia.org/wiki/` and `wikidata.org/` in relation URLs. Mock server URLs (http://127.0.0.1:PORT/wiki/...) don't match these patterns, so enrichment never triggered.
- **Fix:** Added fallback checks by relation type ("wikipedia", "wikidata") when base URL overrides are set on MBClient.
- **Files modified:** internal/match/artistmeta.go
- **Verification:** Happy path integration test now passes all enrichment assertions
- **Committed in:** 77ea942 (Task 2 commit)

---

**Total deviations:** 2 auto-fixed (2 blocking)
**Impact on plan:** Both auto-fixes were necessary to achieve the plan's stated goal of testing the full enrichment pipeline. No scope creep -- all changes serve testability with zero behavior change in production paths.

## Issues Encountered
None beyond the deviations documented above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Match pipeline integration tests complete, ready for plan 02 (additional integration test coverage)
- The baseURL injection pattern is established and reusable for any future tests needing mock external APIs

---
*Phase: 05-integration-test-coverage*
*Completed: 2026-03-06*

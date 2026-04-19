# Phase 5: Integration Test Coverage - Context

**Gathered:** 2026-03-06
**Status:** Ready for planning

<domain>
## Phase Boundary

Add integration tests for the music match pipeline (`RunMusicMatch`) and TV match pipeline (`RunTVMatch`) that exercise the full match-score-fetch-enrich flow using mocked external APIs. Requirements: TEST-07, TEST-08. No new features, no refactoring beyond what's needed for testability.

</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion

User chose to skip discussion -- all implementation decisions are Claude's discretion. Key areas:

**HTTP mocking approach:**
- How to intercept external API calls (httptest.Server, custom RoundTripper, interface extraction)
- Music pipeline hits 5 services: MusicBrainz API, Wikipedia REST, Wikidata, Wikimedia Commons, Cover Art Archive
- TV pipeline hits 1 service: TMDB API (search, show details, season details, image download)
- Neither pipeline currently has an injection point -- MBClient/CAAClient/tmdb.Client construct http.Client internally

**Test granularity:**
- Full pipeline tests (RunMusicMatch/RunTVMatch end-to-end) vs stage-level tests (enrichArtists, matchAlbums separately)
- How many scenarios per pipeline: happy path only, or also partial failures, empty results, context cancellation
- Whether to test error continuation behavior (per-album/per-show errors don't crash pipeline)

**Response fixture format:**
- Realistic JSON responses (mimic real API shape) vs minimal stubs (just enough fields to exercise code paths)
- Inline in test code vs separate fixture files
- How to map request URLs to canned responses in the mock server

</decisions>

<specifics>
## Specific Ideas

No specific requirements -- open to standard approaches.

</specifics>

<code_context>
## Existing Code Insights

### Reusable Assets
- `openTestDB(t)` in `internal/db/db_test.go`: Creates temp SQLite with full migrations
- `internal/match/scorer_test.go`, `similarity_test.go`, `normalize_test.go`, `dedup_test.go`: Existing unit tests in match package
- `internal/tmdb/client_test.go`: Existing TMDB client tests -- may have patterns to build on
- `parseSearchResponse`, `parseShowResponse`, `parseSeasonResponse` in `internal/tmdb/client.go`: Exported parse helpers useful for generating test fixtures

### Established Patterns
- Table-driven tests with `t.Run(name, func(t *testing.T) {...})` subtests
- Direct conditionals with `t.Fatalf()` -- no assertion libraries
- No mocking frameworks -- pure Go test doubles
- Real SQLite via `openTestDB(t)` for all DB-dependent tests
- `t.TempDir()` for filesystem-dependent tests

### Integration Points
- `internal/match/pipeline.go`: `Matcher.RunMusicMatch()` -- orchestrates enrichArtists + matchAlbums
  - `enrichArtists`: DB query -> MBClient.SearchArtist -> MBClient.LookupArtist -> EnrichArtist (Wikipedia/Wikidata/Wikimedia) -> DB updates
  - `matchAlbums`: DB query -> MBClient.SearchReleaseGroups (3-strategy cascade) -> BestCandidate scoring -> CAAClient.FetchCover -> DB updates
- `internal/tmdb/matcher.go`: `Matcher.RunTVMatch()` -- orchestrates search -> fetch -> cache -> art download
  - DB query -> Client.SearchTV -> pickBestResult -> Client.FetchTVShow -> cache + art -> Client.FetchTVSeason -> cache episodes -> update identities
- `internal/match/musicbrainz.go`: MBClient with hardcoded base URL and internally created http.Client
- `internal/match/coverart.go`: CAAClient with hardcoded base URL and internally created http.Client
- `internal/match/artistmeta.go`: EnrichArtist creates ad-hoc http.Client instances per function call
- `internal/tmdb/client.go`: Client with hardcoded API base URL, rate limiter via time.Ticker

### Key Constraint
Both pipelines use rate limiters (1 req/sec for MusicBrainz, 250ms for TMDB, 500ms delays between items). Tests should bypass or minimize these delays to run fast.

</code_context>

<deferred>
## Deferred Ideas

None -- discussion stayed within phase scope.

</deferred>

---

*Phase: 05-integration-test-coverage*
*Context gathered: 2026-03-06*

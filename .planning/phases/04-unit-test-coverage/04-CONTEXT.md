# Phase 4: Unit Test Coverage - Context

**Gathered:** 2026-03-05
**Status:** Ready for planning

<domain>
## Phase Boundary

Add unit tests for scanner (music + TV) and handler (music, TV, settings) critical paths. Requirements: TEST-01, TEST-02, TEST-03, TEST-04, TEST-05, TEST-06. No new features, no refactoring beyond what's needed for testability.

</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion

User chose to skip discussion -- all implementation decisions are Claude's discretion. Key areas:

**Test scope depth:**
- How thorough per requirement: happy path + key error paths, or exhaustive edge cases
- Whether to test all DB state transitions or focus on critical flows
- How many table-driven test cases per function

**Mocking strategy:**
- Scanner tests: whether to use real audio files (test fixtures) or mock the tag reader
- TV scanner tests: whether to mock ffprobe or use fixture data
- Handler tests: real SQLite via openTestDB vs mocking DB layer
- Whether to introduce any test helper abstractions beyond existing patterns

**Test organization:**
- File naming and grouping (one test file per source file, or grouped by feature)
- Whether scanner tests go in internal/scan/ or a separate test package
- Whether TV scanner tests need a separate test file from music scanner tests

</decisions>

<specifics>
## Specific Ideas

No specific requirements -- open to standard approaches.

</specifics>

<code_context>
## Existing Code Insights

### Reusable Assets
- `openTestDB(t)` helper in `internal/db/db_test.go`: Creates temp SQLite with migrations for test isolation
- `httptest.NewRequest` / `httptest.NewRecorder`: Used in auth tests, applicable to handler tests
- `t.TempDir()`: Used for filesystem-dependent tests (pathguard, config)
- `internal/web/helpers_test.go`: Phase 3 added pathID/pathSegment tests -- pattern for web package tests
- `internal/web/handler_test.go`: Phase 3 added template validation tests -- test infrastructure for New() exists
- `internal/tvscan/identify_test.go`: Existing TV identification tests with table-driven patterns

### Established Patterns
- Table-driven tests with `t.Run(name, func(t *testing.T) {...})` subtests
- Direct conditionals with `t.Fatalf()` -- no assertion libraries
- No mocking frameworks -- pure Go interfaces and test doubles where needed
- Tests run via `go test ./... -count=1`

### Integration Points
- `internal/scan/scanner.go`: ScanFile, ScanMusic, finalizeCompilations, ensureAlbum, ensureArtist -- all need tests (TEST-01, TEST-02)
- `internal/tvscan/scanner.go`: ScanTV, upsertTVFile -- needs tests including BUG-01 rescan behavior (TEST-03)
- `internal/web/handlers_music.go`: musicHome, musicArtistAlbums, musicAlbumTracks, musicAlbumEdit -- handler tests (TEST-04)
- `internal/web/handlers_tv.go`: tvSeriesList, tvSeriesDetail, tvSeasonDetail -- handler tests (TEST-05)
- `internal/web/handlers_settings.go`: settings, libraries, librariesNew, librariesScan -- handler tests (TEST-06)

</code_context>

<deferred>
## Deferred Ideas

None -- discussion stayed within phase scope.

</deferred>

---

*Phase: 04-unit-test-coverage*
*Context gathered: 2026-03-05*

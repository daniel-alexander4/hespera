# Phase 5: Integration Test Coverage - Research

**Researched:** 2026-03-05
**Domain:** Go integration testing with mocked external HTTP APIs
**Confidence:** HIGH

## Summary

This phase adds integration tests for two match pipelines: `RunMusicMatch()` in `internal/match` and `RunTVMatch()` in `internal/tmdb`. Both pipelines orchestrate multi-step flows involving DB queries, external API calls, scoring/selection logic, image downloads, and DB updates. The core challenge is that neither pipeline currently has HTTP client injection points -- all clients are created internally with hardcoded base URLs.

The recommended approach is to add unexported `baseURL` fields to `MBClient`, `CAAClient`, and `tmdb.Client`, defaulting to production values, with test-only constructor variants (or direct field setting from `_test.go` files in the same package) that point to `httptest.Server` instances. This is the minimal-invasive pattern that avoids interface extraction (which would be over-engineering for this use case) while enabling full pipeline testing. For `EnrichArtist` which creates ad-hoc `http.Client` instances, the same `httptest.Server` URL rewriting approach works because all requests go through URLs derived from MB lookup results -- the mock MB response can return URLs pointing at the test server.

**Primary recommendation:** Add unexported `baseURL` fields to the three HTTP client structs and populate them from test constructors. Use `httptest.Server` with URL-path-based request routing. Use real SQLite via `openTestDB(t)`. Bypass rate limiters in test constructors. Inline JSON fixtures.

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions
None -- user chose to skip discussion.

### Claude's Discretion
All implementation decisions are Claude's discretion. Key areas:
- HTTP mocking approach (httptest.Server, custom RoundTripper, interface extraction)
- Test granularity (full pipeline vs stage-level, number of scenarios)
- Response fixture format (realistic vs minimal, inline vs files)
- How to map request URLs to canned responses in the mock server

### Deferred Ideas (OUT OF SCOPE)
None -- discussion stayed within phase scope.
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| TEST-07 | Music match pipeline RunMusicMatch() has integration tests covering the full match-score-art-enrich flow | httptest.Server mocking MusicBrainz, Wikipedia, Wikidata, Wikimedia, CAA; seeded DB with artists+albums; assertions on DB state after pipeline run |
| TEST-08 | TMDB matcher RunTVMatch() has integration tests covering search, metadata fetch, and art download | httptest.Server mocking TMDB API (search, show, season, image endpoints); seeded DB with TV files+identities; assertions on DB state + art files |
</phase_requirements>

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| testing | stdlib | Test framework | Project convention -- no external frameworks |
| net/http/httptest | stdlib | Mock HTTP servers | Standard Go pattern for HTTP testing; avoids external mocking libs |
| database/sql | stdlib | Real SQLite test databases | Project convention via openTestDB(t) pattern |
| isomedia/internal/db | project | Test DB creation with migrations | openTestDB + Migrate gives fully-schemed DB |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| encoding/json | stdlib | Crafting mock API responses | All fixture responses are JSON |
| path/filepath | stdlib | File path assertions | Verifying art download paths |
| os | stdlib | Verifying downloaded art files exist | Post-pipeline file assertions |
| context | stdlib | Context cancellation tests | Error handling / cancellation scenarios |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| httptest.Server | custom RoundTripper | RoundTripper requires replacing http.Client internals; httptest.Server is simpler -- just change base URL |
| httptest.Server | interface extraction | Would require defining interfaces for MBClient, CAAClient, tmdb.Client and refactoring pipeline.go/matcher.go to accept them; over-engineering for test-only needs |
| Inline JSON fixtures | testdata/ fixture files | Inline is simpler for these tests (responses are small, ~20-50 lines); fixture files add indirection without benefit |

## Architecture Patterns

### Recommended Test Structure
```
internal/match/
  pipeline_integration_test.go    # RunMusicMatch integration tests (TEST-07)
internal/tmdb/
  matcher_integration_test.go     # RunTVMatch integration tests (TEST-08)
```

### Pattern 1: Base URL Injection for Test Mocking
**What:** Add an unexported `baseURL` field to each HTTP client struct, defaulting to the production constant. Tests in the same package can set it directly to point at an `httptest.Server`.
**When to use:** When the client struct has a hardcoded base URL constant and you need to redirect traffic to a test server without changing the public API.
**Example:**
```go
// In musicbrainz.go -- add baseURL field:
type MBClient struct {
    client  *http.Client
    baseURL string  // defaults to mbBaseURL
    mu      sync.Mutex
    lastReq time.Time
}

func NewMBClient() *MBClient {
    return &MBClient{
        client:  &http.Client{Timeout: 15 * time.Second},
        baseURL: mbBaseURL,
    }
}

// In get() method, change:
//   req, err := http.NewRequestWithContext(ctx, http.MethodGet, mbBaseURL+path, nil)
// To:
//   req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
```

### Pattern 2: URL-Path-Based Mock Router
**What:** A single `httptest.Server` that routes requests based on URL path prefixes to return canned JSON responses.
**When to use:** When a test needs to mock multiple API endpoints behind one base URL.
**Example:**
```go
func newMockMBServer(t *testing.T) *httptest.Server {
    return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch {
        case strings.HasPrefix(r.URL.Path, "/ws/2/release-group"):
            w.Header().Set("Content-Type", "application/json")
            fmt.Fprint(w, releaseGroupJSON)
        case strings.HasPrefix(r.URL.Path, "/ws/2/artist"):
            // Check for lookup vs search by path structure
            if strings.Count(r.URL.Path, "/") > 3 {
                // Lookup: /ws/2/artist/{mbid}
                fmt.Fprint(w, artistLookupJSON)
            } else {
                fmt.Fprint(w, artistSearchJSON)
            }
        default:
            http.NotFound(w, r)
        }
    }))
}
```

### Pattern 3: Multi-Service Mock for EnrichArtist
**What:** `EnrichArtist` calls Wikipedia, Wikidata, and Wikimedia Commons using URLs derived from MusicBrainz lookup results. The mock MB artist lookup response includes relation URLs that point back at the test server.
**When to use:** When the pipeline derives downstream URLs from upstream API responses.
**Example:**
```go
// The mock MusicBrainz artist lookup returns:
artistLookupJSON := fmt.Sprintf(`{
    "id": "test-mbid",
    "name": "Test Artist",
    "relations": [
        {"type": "wikipedia", "url": {"resource": "%s/wiki/Test_Artist"}},
        {"type": "wikidata", "url": {"resource": "%s/wiki/Q12345"}}
    ]
}`, mockServer.URL, mockServer.URL)

// The same mock server handles /wiki/*, /api/rest_v1/*, and /wiki/Special:* paths
```

### Pattern 4: Matcher Constructor for Tests
**What:** A test helper that creates a fully-wired `Matcher` (or `tmdb.Matcher`) with mock servers, bypassed rate limiters, and real SQLite.
**When to use:** Every integration test needs this setup.
**Example:**
```go
func newTestMatcher(t *testing.T, db *sql.DB) (*Matcher, *httptest.Server) {
    t.Helper()
    srv := newMockServer(t)
    t.Cleanup(srv.Close)

    dataDir := t.TempDir()
    m := &Matcher{
        db:      db,
        dataDir: dataDir,
        mb:      &MBClient{client: srv.Client(), baseURL: srv.URL + "/ws/2"},
        caa:     &CAAClient{client: srv.Client(), thumbDir: filepath.Join(dataDir, "thumbs", "music")},
    }
    return m, srv
}
```

### Anti-Patterns to Avoid
- **Interface extraction for testing only:** Creating `MusicBrainzSearcher`, `CoverArtFetcher` etc. interfaces just for tests adds unnecessary abstraction. Direct field injection in same-package tests is simpler and idiomatic.
- **Mocking at the DB level:** Use real SQLite. It catches SQL bugs that mocks would miss. The project convention is `openTestDB(t)`.
- **External fixture files for small responses:** Inline JSON keeps test intent clear. Only use files when fixtures exceed ~100 lines.
- **Testing with real rate limiters:** Tests would be painfully slow (500ms-1s per request). Set `lastReq` to zero time and remove ticker delays in test constructors.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| HTTP server mocking | Custom TCP listener or RoundTripper | `httptest.NewServer()` | Handles port allocation, TLS, cleanup automatically |
| JSON response building | String concatenation | `fmt.Sprintf` with template literals | Readable, handles escaping correctly |
| Test database setup | Manual CREATE TABLE statements | `openTestDB(t)` with `isodb.Migrate()` | Full schema including FK constraints, indexes, CHECK constraints |
| Temporary directories | Manual os.MkdirAll + cleanup | `t.TempDir()` | Automatic cleanup, unique per test |

**Key insight:** The project already has all necessary DB test infrastructure. The only new infrastructure needed is HTTP mocking via httptest, which is stdlib.

## Common Pitfalls

### Pitfall 1: Hardcoded Base URLs in Constants
**What goes wrong:** `mbBaseURL`, `caaBaseURL`, and `apiBase` are `const` -- they cannot be overridden at test time.
**Why it happens:** The original code had no need for testability; these were implementation details.
**How to avoid:** Convert the constant references into struct fields. The `const` declarations remain for their default values. The `get()`, `findCoverURL()`, `SearchTV()`, etc. methods use `c.baseURL` instead of the const directly.
**Warning signs:** Tests making actual HTTP calls to production APIs.

### Pitfall 2: Rate Limiter Delays in Tests
**What goes wrong:** Tests take 500ms-1s per mock request due to MBClient.throttle() and TMDB's ticker-based limiter. A test with 5 API calls takes 5+ seconds.
**Why it happens:** Rate limiters are embedded in client structs with no bypass mechanism.
**How to avoid:** For MBClient: set `lastReq` to `time.Time{}` (zero) so throttle() never sleeps. For TMDB Client: replace the ticker channel with a pre-closed or immediately-available channel in test constructors. Or bypass the limiter entirely by using a buffered channel with a pre-loaded token.
**Warning signs:** Tests taking >1s when they should complete in <100ms.

### Pitfall 3: EnrichArtist Creates Ad-Hoc http.Client Instances
**What goes wrong:** `fetchWikipediaSummary()`, `fetchWikidataEntity()`, and `downloadArtistImage()` each create their own `http.Client{}`. These bypass any mock server setup on the MBClient.
**Why it happens:** These are package-level functions, not methods on a client struct.
**How to avoid:** The mock MusicBrainz artist lookup response should include URL relations that point to paths on the mock server (e.g., `mockServer.URL + "/wiki/Test_Artist"`). Then the ad-hoc http.Client instances will naturally hit the mock server because they follow the URLs from the response. No code change needed in the functions themselves -- the mock data controls the URLs.
**Warning signs:** Tests failing with connection refused errors to `en.wikipedia.org` or `www.wikidata.org`.

### Pitfall 4: Pipeline Delay Between Items
**What goes wrong:** Both `enrichArtists` and `matchAlbums` have `time.After(500ms)` sleeps between items.
**Why it happens:** Rate limiting for external API politeness.
**How to avoid:** These delays are hardcoded in the pipeline methods (not in the client). Two options: (a) accept the delay in tests (if only 1-2 items, 500ms-1s is tolerable), or (b) add a small testable hook. Option (a) is simpler -- seed only 1 artist and 1 album to minimize delay impact.
**Warning signs:** Integration tests taking 3+ seconds.

### Pitfall 5: CAAClient Uses caaBaseURL Const Directly
**What goes wrong:** `FetchCover()` builds URLs using `caaBaseURL` const, not a field. Even if you inject a custom http.Client, requests still go to the real CAA.
**Why it happens:** Same pattern as MBClient -- hardcoded const.
**How to avoid:** Add `baseURL` field to CAAClient, same pattern as MBClient. Change `FetchCover()` to use `c.baseURL` instead of the const.

### Pitfall 6: TMDB Image URLs Use Separate Constants
**What goes wrong:** `DownloadImage()` builds image URLs using `imgPoster` const (`https://image.tmdb.org/t/p/w500`), separate from `apiBase`.
**Why it happens:** TMDB uses different base URLs for API calls vs image serving.
**How to avoid:** Add an `imgBase` field to `tmdb.Client` alongside `apiBase`. Or have the mock server handle both path prefixes (API paths under `/3/` and image paths under `/t/p/`).

## Code Examples

### Example 1: Music Pipeline Integration Test (Happy Path)
```go
func TestRunMusicMatchIntegration(t *testing.T) {
    db := openTestDB(t)
    ctx := context.Background()

    // Seed library + artist + album.
    res, _ := db.ExecContext(ctx,
        "INSERT INTO libraries (name, type, root_path) VALUES ('test', 'music', '/music')")
    libID, _ := res.LastInsertId()

    // Insert artist (missing mbid, bio, art -- will trigger enrichment).
    res, _ = db.ExecContext(ctx,
        "INSERT INTO music_artists (library_id, name) VALUES (?, ?)", libID, "Pink Floyd")
    artistID, _ := res.LastInsertId()

    // Insert album (empty match_status -- will trigger matching).
    res, _ = db.ExecContext(ctx, `
        INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year, match_status)
        VALUES (?, ?, ?, ?, ?, '')`,
        libID, artistID, artistID, "Dark Side of the Moon", 1973)
    albumID, _ := res.LastInsertId()

    // Insert a track (required by FK constraints).
    db.ExecContext(ctx, `INSERT INTO music_tracks
        (library_id, artist_id, album_id, title, abs_path, track_no, disc_no)
        VALUES (?, ?, ?, 'Speak to Me', '/fake/track1.mp3', 1, 1)`,
        libID, artistID, albumID)

    // Insert scan job for progress tracking.
    res, _ = db.ExecContext(ctx,
        "INSERT INTO scan_jobs (library_id, job_type, status) VALUES (?, 'music_match', 'running')",
        libID)
    jobID, _ := res.LastInsertId()

    // Create matcher with mock server.
    m, _ := newTestMatcher(t, db)

    // Run pipeline.
    err := m.RunMusicMatch(ctx, jobID, libID)
    if err != nil {
        t.Fatalf("RunMusicMatch: %v", err)
    }

    // Verify artist enrichment: MBID, bio, art_path should be populated.
    var mbid, bio, artPath string
    db.QueryRowContext(ctx,
        "SELECT musicbrainz_id, bio, art_path FROM music_artists WHERE id=?", artistID,
    ).Scan(&mbid, &bio, &artPath)

    if mbid == "" {
        t.Fatal("artist MBID not set after enrichment")
    }
    if bio == "" {
        t.Fatal("artist bio not set after enrichment")
    }

    // Verify album matching: match_status, musicbrainz_id, match_confidence.
    var matchStatus, albumMBID string
    var confidence int
    db.QueryRowContext(ctx,
        "SELECT match_status, musicbrainz_id, match_confidence FROM music_albums WHERE id=?", albumID,
    ).Scan(&matchStatus, &albumMBID, &confidence)

    if matchStatus != "matched" && matchStatus != "uncertain" {
        t.Fatalf("album match_status = %q, want matched or uncertain", matchStatus)
    }
    if albumMBID == "" {
        t.Fatal("album musicbrainz_id not set after matching")
    }
}
```

### Example 2: TV Pipeline Integration Test (Happy Path)
```go
func TestRunTVMatchIntegration(t *testing.T) {
    db := openTestDB(t)
    ctx := context.Background()

    // Seed library.
    res, _ := db.ExecContext(ctx,
        "INSERT INTO libraries (name, type, root_path) VALUES ('test', 'tv', '/tv')")
    libID, _ := res.LastInsertId()

    // Insert TV file + identity.
    res, _ = db.ExecContext(ctx,
        "INSERT INTO tv_series_files (library_id, abs_path) VALUES (?, '/tv/breaking.bad.s01e01.mp4')",
        libID)
    fileID, _ := res.LastInsertId()

    db.ExecContext(ctx, `INSERT INTO tv_series_identities
        (file_id, status, guessed_title, season_number, episode_numbers_csv)
        VALUES (?, 'needs_fix', 'Breaking Bad', 1, '1')`, fileID)

    // Insert scan job.
    res, _ = db.ExecContext(ctx,
        "INSERT INTO scan_jobs (library_id, job_type, status) VALUES (?, 'tv_match', 'running')", libID)
    jobID, _ := res.LastInsertId()

    // Create matcher with mock server.
    m := newTestTMDBMatcher(t, db)

    err := m.RunTVMatch(ctx, jobID, libID)
    if err != nil {
        t.Fatalf("RunTVMatch: %v", err)
    }

    // Verify identity updated to resolved.
    var status, provider, seriesID string
    db.QueryRowContext(ctx,
        "SELECT status, provider, series_id FROM tv_series_identities WHERE file_id=?", fileID,
    ).Scan(&status, &provider, &seriesID)

    if status != "resolved" {
        t.Fatalf("identity status = %q, want resolved", status)
    }
    if provider != "tmdb" {
        t.Fatalf("provider = %q, want tmdb", provider)
    }

    // Verify metadata cached.
    var cacheCount int
    db.QueryRowContext(ctx,
        "SELECT COUNT(*) FROM tv_series_metadata_cache WHERE entity_key LIKE 'show:%'",
    ).Scan(&cacheCount)
    if cacheCount == 0 {
        t.Fatal("no show metadata cached")
    }
}
```

### Example 3: Error Continuation Test
```go
func TestRunMusicMatchIntegrationPartialFailure(t *testing.T) {
    // Mock server returns 500 for artist search but 200 for album search.
    // Pipeline should log warnings for artist enrichment failure
    // but still successfully match albums.
}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| httptest.Server is standard | Still standard | Always | No change needed -- httptest.Server is the Go community standard for HTTP testing |
| Interface-based mocking | Still valid but heavier | N/A | For this project, direct field injection is preferred per existing conventions |

**Deprecated/outdated:**
- Nothing relevant. `httptest.Server` and same-package field access are stable Go patterns.

## Required Production Code Changes

These are the minimal changes needed to make pipelines testable. They do NOT change public API or behavior.

### 1. MBClient: Add baseURL field
- Add `baseURL string` to struct
- `NewMBClient()` sets `baseURL: mbBaseURL`
- `get()` uses `c.baseURL + path` instead of `mbBaseURL + path`

### 2. CAAClient: Add baseURL field
- Add `baseURL string` to struct
- `NewCAAClient()` sets `baseURL: caaBaseURL`
- `FetchCover()` uses `c.baseURL` instead of `caaBaseURL`

### 3. tmdb.Client: Add apiBase and imgBase fields
- Add `apiBase string` and `imgBase string` to struct
- `NewClient()` sets them to the const values
- `SearchTV()`, `FetchTVShow()`, `FetchTVSeason()` use `c.apiBase`
- `DownloadImage()` uses `c.imgBase`

### 4. tmdb.Client: Bypass rate limiter in tests
- Change `limiter` from `<-chan time.Time` to a pattern that can be bypassed
- Option A: Accept a `limiter` parameter in constructor
- Option B: Add test-only constructor that uses `make(chan time.Time)` with immediate availability
- Simplest: use a closed channel or a buffered channel pre-loaded with tokens

### 5. EnrichArtist URL handling
- No code change needed. Mock MusicBrainz artist lookup returns URLs pointing at the test server. The ad-hoc `http.Client` instances follow those URLs naturally.

## Open Questions

1. **Pipeline inter-item delay (500ms)**
   - What we know: Both pipelines have `time.After(500ms)` between items. With 1 artist + 1 album, that's ~1s of sleeping.
   - What's unclear: Whether this delay is acceptable in CI or should be bypassed.
   - Recommendation: Accept the delay for now. Keep test data minimal (1 item per entity type). Total test time ~1-2s is acceptable. Avoid adding testable hooks just to save 1s.

2. **TMDB ticker-based rate limiter bypass**
   - What we know: `tmdb.Client.limiter` is a `<-chan time.Time` from `time.NewTicker(250ms)`. Tests block on `<-c.limiter` before each request.
   - What's unclear: Cleanest way to bypass without leaking the ticker.
   - Recommendation: Add a test constructor that uses a channel that immediately returns. Simplest: `make(chan time.Time)` and close it immediately (closed channels return zero value immediately and repeatedly).

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go testing (stdlib) |
| Config file | None needed |
| Quick run command | `go test ./internal/match/ -run Integration -v` and `go test ./internal/tmdb/ -run TVIntegration -v` |
| Full suite command | `go test ./...` |

### Phase Requirements -> Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| TEST-07 | RunMusicMatch full pipeline (search, score, art, enrich) | integration | `go test ./internal/match/ -run Integration -v -count=1` | Wave 0 |
| TEST-07 | RunMusicMatch partial failure (non-fatal errors) | integration | `go test ./internal/match/ -run Integration -v -count=1` | Wave 0 |
| TEST-08 | RunTVMatch full pipeline (search, metadata, art) | integration | `go test ./internal/tmdb/ -run TVIntegration -v -count=1` | Wave 0 |
| TEST-08 | RunTVMatch partial failure (non-fatal errors) | integration | `go test ./internal/tmdb/ -run TVIntegration -v -count=1` | Wave 0 |

### Sampling Rate
- **Per task commit:** `go test ./internal/match/ -run Integration -v -count=1 && go test ./internal/tmdb/ -run TVIntegration -v -count=1`
- **Per wave merge:** `go test ./... -count=1`
- **Phase gate:** Full suite green before `/gsd:verify-work`

### Wave 0 Gaps
- [ ] `internal/match/pipeline_integration_test.go` -- covers TEST-07
- [ ] `internal/tmdb/matcher_integration_test.go` -- covers TEST-08
- [ ] Production code changes: baseURL fields in MBClient, CAAClient, tmdb.Client (prerequisite for tests)

## Sources

### Primary (HIGH confidence)
- Direct source code analysis: `internal/match/pipeline.go`, `musicbrainz.go`, `coverart.go`, `artistmeta.go`
- Direct source code analysis: `internal/tmdb/client.go`, `matcher.go`, `client_test.go`
- Direct source code analysis: `internal/match/dedup_test.go` (openTestDB pattern, insertTestAlbum helper)
- Direct source code analysis: `internal/db/migrate.go` (full schema for all relevant tables)
- Go stdlib documentation: `net/http/httptest` package (well-known, HIGH confidence)

### Secondary (MEDIUM confidence)
- Go community patterns for httptest-based integration testing (standard, widely used)

### Tertiary (LOW confidence)
- None

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH - All stdlib, project conventions well-established
- Architecture: HIGH - Direct source code analysis of all pipeline code, clear understanding of injection points needed
- Pitfalls: HIGH - Identified from actual code inspection (hardcoded consts, ad-hoc clients, rate limiters)
- Required code changes: HIGH - Minimal, well-scoped changes identified from actual field/method analysis

**Research date:** 2026-03-05
**Valid until:** Indefinite (Go stdlib patterns are stable; project code changes only on commit)

# Testing Patterns

**Analysis Date:** 2026-03-05

## Test Framework

**Runner:**
- Standard `testing` package (Go stdlib), no third-party test runners
- No test configuration file needed (Go convention)

**Assertion Library:**
- None. Direct conditionals with `t.Fatalf()`, `t.Errorf()`, and `t.Fatal()`.

**Run Commands:**
```bash
go test ./...                           # Run all tests
go test ./internal/config               # Run tests for a specific package
go test ./internal/match                # Run match package tests
go test ./internal/config -run TestFromEnvDefaults  # Run a single test
go fmt ./...                            # Format all code
go vet ./...                            # Static analysis
```

**No watch mode or coverage tooling configured.** Standard Go coverage:
```bash
go test -cover ./...                    # Coverage summary
go test -coverprofile=cover.out ./...   # Generate coverage profile
go tool cover -html=cover.out           # View in browser
```

## Test File Organization

**Location:**
- Co-located with source files in the same package (Go convention)
- Every test file is in the same directory as the code it tests

**Naming:**
- `{source_file}_test.go` pattern: `tags.go` -> `tags_test.go`, `scorer.go` -> `scorer_test.go`
- Test functions: `Test{FunctionName}` or `Test{Concept}` - `TestNormalize`, `TestEnqueueAndRun`, `TestMigrateIdempotent`

**Files with tests (15 total):**
```
internal/config/config_test.go       # Config parsing and validation
internal/db/db_test.go               # DB open, migrate, ensureColumn
internal/auth/auth_test.go           # Auth manager, sessions, middleware, rate limiting
internal/jobs/jobs_test.go           # Job enqueue, execution, cancellation
internal/pathguard/pathguard_test.go # Path containment, symlink resolution
internal/music/tags_test.go          # Audio extension check, tag parsing helpers
internal/music/tagwrite_test.go      # Tag write dispatch, sanitize text
internal/match/similarity_test.go    # Normalize, Levenshtein, similarity scoring
internal/match/scorer_test.go        # Candidate scoring, type/year bonuses
internal/match/normalize_test.go     # Title normalization, dedup normalization
internal/match/dedup_test.go         # Duplicate finding, album merging (with DB)
internal/video/extensions_test.go    # Video extension check
internal/video/probe_test.go         # FFprobe JSON parsing
internal/tmdb/client_test.go         # TMDB response parsing
internal/tvscan/identify_test.go     # Filename pattern matching (SxE, season dir)
```

**Files WITHOUT tests:**
```
internal/web/         # No handler tests (handlers_*.go, router.go, middleware.go, handler.go)
internal/scan/        # No scanner tests (scanner.go)
internal/match/       # pipeline.go, musicbrainz.go, coverart.go, artistmeta.go, writeback.go untested
internal/tmdb/        # matcher.go untested (only client parsing tested)
internal/tvscan/      # scanner.go untested (only identify.go tested)
cmd/isomedia/         # No main.go tests
cmd/isocli/           # No main.go tests
```

## Test Structure

**Suite Organization:**

Table-driven tests are the primary pattern. Used consistently across all packages.

```go
func TestNormalize(t *testing.T) {
    tests := []struct {
        in, want string
    }{
        {"Hello World", "hello world"},
        {"AC/DC", "ac dc"},
        {"  Guns  N'  Roses  ", "guns n roses"},
        {"", ""},
    }
    for _, tt := range tests {
        got := Normalize(tt.in)
        if got != tt.want {
            t.Fatalf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
        }
    }
}
```

**Table-driven with subtests** (via `t.Run`):
```go
func TestValidate(t *testing.T) {
    tests := []struct {
        name    string
        cfg     Config
        wantErr bool
    }{
        {
            name:    "valid_no_auth",
            cfg:     Config{Listen: ":8080", DataDir: "/tmp/data", ...},
            wantErr: false,
        },
        // ...
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := tt.cfg.Validate()
            if (err != nil) != tt.wantErr {
                t.Fatalf("Validate() error=%v, wantErr=%v", err, tt.wantErr)
            }
        })
    }
}
```

**Non-table tests** for single-scenario or sequential logic:
```go
func TestSessionCookieRoundtrip(t *testing.T) {
    m := newTestManager(t)
    r := httptest.NewRequest(http.MethodGet, "/", nil)
    w := httptest.NewRecorder()
    if err := m.setSessionCookie(w, r, "testuser"); err != nil {
        t.Fatalf("setSessionCookie: %v", err)
    }
    // ... verify cookie can be read back
}
```

**Assertion patterns:**
- `t.Fatalf(format, args...)` for fatal failures (stops test immediately)
- `t.Errorf(format, args...)` for non-fatal failures (continues test, used in loops)
- `t.Fatal(msg)` for simple boolean failures
- Consistent format: `"FunctionName(%q) = %q, want %q"` or `"expected X, got %v"`

**Setup pattern:**
- `t.TempDir()` for temporary directories (auto-cleaned)
- `t.Helper()` on helper functions
- `t.Cleanup(func() { ... })` for cleanup callbacks
- `t.Skipf()` when a test prerequisite is unavailable:
  ```go
  if err := os.Symlink(outside, link); err != nil {
      t.Skipf("cannot create symlink: %v", err)
  }
  ```
- `defer` for inline cleanup: `defer conn.Close()`
- `os.Setenv`/`os.Unsetenv` with `defer` for env var tests

## Mocking

**Framework:** None. No mocking libraries used.

**Patterns:**

1. **Function field injection** (primary mock mechanism):
   ```go
   // auth.Manager has injectable function fields:
   type Manager struct {
       verifySSH func(ctx context.Context, in VerifyInput) error
       now       func() time.Time
       // ...
   }

   // In tests, override time:
   m.now = func() time.Time { return time.Now().Add(-25 * time.Hour) }
   ```

2. **Real database** instead of mocking - tests use actual SQLite:
   ```go
   func openTestDB(t *testing.T) *sql.DB {
       t.Helper()
       dir := t.TempDir()
       dbPath := filepath.Join(dir, "test.sqlite")
       conn, err := isodb.Open(dbPath)
       // ...
       if err := isodb.Migrate(conn); err != nil {
           t.Fatalf("Migrate: %v", err)
       }
       return conn
   }
   ```

3. **Exported parse functions** for testing HTTP client internals without network:
   ```go
   // In tmdb/client.go:
   func parseSearchResponse(data []byte) ([]TVSearchResult, error) { ... }
   func parseShowResponse(data []byte) (*TVShow, error) { ... }

   // In tmdb/client_test.go:
   const sampleSearchJSON = `{...}`
   func TestParseSearchResponse(t *testing.T) {
       results, err := parseSearchResponse([]byte(sampleSearchJSON))
       // ...
   }
   ```

4. **Embedded JSON fixtures** as `const` strings in test files:
   ```go
   const sampleFFProbeJSON = `{
     "format": {"duration": "2712.123000", ...},
     "streams": [...]
   }`
   ```

**What to mock:**
- Time via injectable `now` function field
- External process calls via injectable function fields (e.g., `verifySSH`)

**What NOT to mock:**
- Database - use real SQLite with `t.TempDir()`
- File system - use real temp directories
- HTTP parsing - test parsing functions directly with embedded JSON

## Fixtures and Factories

**Test Data:**

1. **Database test helper** (`internal/match/dedup_test.go`):
   ```go
   func insertTestAlbum(t *testing.T, db *sql.DB, libraryID int64,
       artist, title string, year, trackCount int, artPath, matchStatus string) int64 {
       t.Helper()
       // Creates artist (INSERT OR IGNORE), album, and N dummy tracks
       // Returns albumID
   }
   ```

2. **Auth test helper** (`internal/auth/auth_test.go`):
   ```go
   func newTestManager(t *testing.T) *Manager {
       t.Helper()
       // Creates temp DB, runs migrations, returns configured Manager
   }
   ```

3. **DB test helper** (`internal/db/db_test.go`):
   ```go
   func openTestDB(t *testing.T) *testDBResult {
       t.Helper()
       // Creates temp SQLite, returns conn + path
   }
   ```

**Note:** Each package that needs DB access defines its own `openTestDB` helper. There is no shared test utility package.

**Location:**
- All fixtures are inline in test files
- No separate `testdata/` directories
- JSON fixtures as `const` strings at top of test files

## Coverage

**Requirements:** None enforced. No coverage thresholds configured.

**Current coverage areas:**
- Strong: `config`, `pathguard`, `match/similarity`, `match/scorer`, `match/normalize`, `music/tags`, `video/extensions`, `video/probe`, `tmdb/client`, `tvscan/identify`
- Moderate: `db`, `auth`, `jobs`, `match/dedup`, `music/tagwrite`
- None: `web` (all HTTP handlers), `scan`, `match/pipeline`, `match/musicbrainz`, `match/coverart`, `match/artistmeta`, `match/writeback`, `tmdb/matcher`, `tvscan/scanner`

## Test Types

**Unit Tests:**
- Pure function tests: `TestNormalize`, `TestLevenshteinDistance`, `TestIsAudioExt`, `TestIsVideoExt`, `TestCleanTitle`
- Struct method tests: `TestValidate`, `TestManagerEnabled`
- Parser tests with embedded fixtures: `TestParseProbeJSON`, `TestParseSearchResponse`
- All tests in the codebase are unit-level

**Integration Tests (lightweight):**
- Database integration: tests that create real SQLite, run migrations, and test DB operations
  - `TestMigrateIdempotent`, `TestEnsureColumnIdempotent` in `internal/db/db_test.go`
  - `TestFindDuplicateAlbums`, `TestMergeAlbums` in `internal/match/dedup_test.go`
  - `TestEnqueueAndRun`, `TestCancelJob` in `internal/jobs/jobs_test.go`
- Auth integration: tests that exercise the full auth flow with real DB
  - `TestCreateChallenge`, `TestSessionCookieRoundtrip`, `TestMiddlewarePublicPaths`

**E2E Tests:**
- Not used. No end-to-end test framework.

**HTTP Handler Tests:**
- `httptest.NewRequest` + `httptest.NewRecorder` used in auth tests
- Pattern:
  ```go
  r := httptest.NewRequest(http.MethodPost, "/auth/challenge", nil)
  r.RemoteAddr = "127.0.0.1:12345"
  w := httptest.NewRecorder()
  handler.ServeHTTP(w, r)
  if w.Code != http.StatusOK {
      t.Fatalf("expected 200, got %d", w.Code)
  }
  ```
- Only `auth` middleware is tested via HTTP; no handler-level HTTP tests for `web` package

## Common Patterns

**Async Testing:**
```go
// Wait for async job completion with polling + deadline
deadline := time.Now().Add(5 * time.Second)
for ran.Load() == 0 && time.Now().Before(deadline) {
    time.Sleep(10 * time.Millisecond)
}
if ran.Load() != 1 {
    t.Fatalf("expected job to run once, ran %d times", ran.Load())
}
```

**Channel-based sync in tests:**
```go
started := make(chan struct{})
jobID, _ := svc.Enqueue("scan", 1, "test", func(ctx context.Context, jobID, libraryID int64) error {
    close(started)
    <-ctx.Done()
    return ctx.Err()
})
select {
case <-started:
case <-time.After(5 * time.Second):
    t.Fatalf("job did not start in time")
}
```

**Error Testing:**
```go
// Expect error
err := WriteTrackTags("/fake/file.wav", TagWriteFields{Title: "test"})
if err == nil {
    t.Fatal("expected error for unsupported format")
}

// Expect no error
if err := VerifyImage("image/jpeg", jpegBytes); err != nil {
    t.Errorf("unexpected error for JPEG: %v", err)
}

// Table-driven with wantErr bool
if (err != nil) != tt.wantErr {
    t.Fatalf("Validate() error=%v, wantErr=%v", err, tt.wantErr)
}
```

**Range-based assertions (for scores/similarity):**
```go
if score < tt.minScore || score > tt.maxScore {
    t.Fatalf("ScoreCandidate() = %.1f, want [%.1f, %.1f]", score, tt.minScore, tt.maxScore)
}
```

**Boolean loop testing (true/false input sets):**
```go
for _, ext := range []string{".mp3", ".flac", ".m4a"} {
    if !IsAudioExt(ext) {
        t.Errorf("expected true for %q", ext)
    }
}
for _, ext := range []string{".txt", ".jpg", ".avi"} {
    if IsAudioExt(ext) {
        t.Errorf("expected false for %q", ext)
    }
}
```

## Adding New Tests

**When adding tests for a new package:**
1. Create `{filename}_test.go` in the same directory
2. Use `package {pkgname}` (same package, not `_test` suffix)
3. If DB access needed, create a local `openTestDB(t)` helper using `t.TempDir()` + `db.Open` + `db.Migrate`
4. Use table-driven tests with `t.Run` subtests for multiple cases
5. Use `t.Helper()` on all test helper functions
6. Use `t.Fatalf` for assertions, never assertion libraries

**When adding tests for HTTP handlers:**
1. Use `httptest.NewRequest` and `httptest.NewRecorder`
2. Set `r.RemoteAddr` if IP-dependent logic is tested
3. Check `w.Code` for status and `w.Header()` for response headers
4. For cookie flows, extract cookies from `w.Result().Cookies()` and re-add with `r.AddCookie()`

---

*Testing analysis: 2026-03-05*

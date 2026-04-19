# Phase 4: Unit Test Coverage - Research

**Researched:** 2026-03-05
**Domain:** Go unit testing for scanner and handler packages
**Confidence:** HIGH

## Summary

Phase 4 adds unit tests for six requirements (TEST-01 through TEST-06) covering the music scanner, TV scanner, and three handler groups (music, TV, settings). The project already has well-established test patterns across 8 packages: table-driven tests, `openTestDB(t)` helpers, `httptest` for HTTP tests, `t.TempDir()` for filesystem isolation, and no external assertion or mocking libraries. The `internal/scan` package has **zero test files** currently, while `internal/web` has handler_test.go and helpers_test.go (Phase 3 additions). The `internal/tvscan` package has `identify_test.go` for filename parsing but no scanner-level tests.

The core challenge is that both scanners depend on the filesystem (reading actual audio/video files) and the database. The music scanner calls `music.ReadTrackMeta()` which requires real audio files, and the TV scanner calls `video.Probe()` which shells out to `ffprobe`. For testability without external dependencies, scanner tests should operate at the DB-interaction level -- testing `ScanFile` with real tiny audio fixtures (or testing sub-functions like `ensureArtist`, `ensureAlbum`, `finalizeCompilations` directly), and testing `upsertTVFile` with pre-built `EpisodeIdentity` structs to bypass ffprobe. Handler tests follow the established `httptest.NewRecorder` pattern, pre-seeding data via SQL inserts.

**Primary recommendation:** Use real SQLite via `openTestDB(t)` for all tests (scanners and handlers). For music scanner, create minimal MP3 test fixtures (valid ID3v2 headers). For TV scanner, bypass `video.Probe` by testing `upsertTVFile` directly with synthetic data. For handlers, construct a `Handler` via `New(Deps{...})` with the template setup pattern from handler_test.go.

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions
None -- user chose to skip discussion.

### Claude's Discretion
All implementation decisions are Claude's discretion. Key areas:
- Test scope depth: happy path + key error paths, or exhaustive edge cases
- Whether to test all DB state transitions or focus on critical flows
- How many table-driven test cases per function
- Mocking strategy: real audio files vs mock tag reader, real vs mock ffprobe, real SQLite vs mock DB
- Test organization: file naming, grouping, package placement

### Deferred Ideas (OUT OF SCOPE)
None.
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| TEST-01 | Music scanner ScanFile() has unit tests covering tag reading, artist/album/track upsert, and art extraction | Use real SQLite + minimal MP3 fixtures (valid ID3v2 headers) to test ScanFile end-to-end. Test ensureArtist/ensureAlbum as sub-function tests. Verify DB state after ScanFile. |
| TEST-02 | Music scanner compilation detection has tests covering mixed-artist albums, "Various Artists", and re-scan scenarios | Test finalizeCompilations with pre-seeded DB data (multiple artists on same album). Test ScanFile re-scan produces same results. No filesystem needed for finalizeCompilations. |
| TEST-03 | TV scanner ScanTV() has tests covering file identification, upsert, and rescan behavior | Test upsertTVFile directly with synthetic EpisodeIdentity structs and pre-seeded DB. Test BUG-01 fix: resolved files skip identity update on rescan. Avoid ffprobe dependency. |
| TEST-04 | Music handler tests verify routing, ID parsing, and error responses for key endpoints | Use httptest.NewRecorder + Handler.Router(). Pre-seed DB with artists/albums/tracks. Test musicHome, musicArtistAlbums, musicAlbumTracks for 200/404/405 responses. |
| TEST-05 | TV handler tests verify routing, ID parsing, and error responses for key endpoints | Use httptest + Handler.Router(). Pre-seed DB with tv_series_files, tv_series_identities, tv_series_metadata_cache. Test tvSeriesList, tvSeriesDetail, tvSeasonDetail. |
| TEST-06 | Settings handler tests verify library CRUD and scan trigger endpoints | Use httptest + Handler.Router(). Test libraries (GET list), librariesNew (POST create), librariesScan (POST trigger). Verify DB state changes for CRUD. |
</phase_requirements>

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| testing | stdlib | Test framework | Project convention; no external test frameworks |
| net/http/httptest | stdlib | HTTP handler testing | Project convention; already used in auth and web tests |
| database/sql | stdlib | Real SQLite test databases | Project convention via openTestDB(t) pattern |
| isomedia/internal/db | project | Test DB creation with migrations | openTestDB + Migrate gives fully-schemed DB |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| os | stdlib | Create fixture files, temp dirs | Scanner tests needing filesystem |
| path/filepath | stdlib | Construct test paths | All tests with file paths |
| context | stdlib | Pass context to scanner/handler methods | Every test calling scanner or handler code |
| encoding/json | stdlib | Assert JSON response bodies | Handler tests for JSON endpoints |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Real SQLite | sql mock library | Real SQLite matches production, catches SQL bugs; mock would miss schema issues |
| MP3 fixtures | Interface-based tag reader mock | Fixtures test the real code path including ReadTrackMeta; interface would require refactoring |
| Direct upsertTVFile calls | Full ScanTV with test video files | upsertTVFile tests the DB logic without ffprobe/filesystem; ScanTV tests would need ffprobe installed |

## Architecture Patterns

### Recommended Test File Structure
```
internal/scan/
  scanner.go             # existing
  scanner_test.go        # NEW: TestMusicScanFile*, TestEnsureArtist, TestEnsureAlbum
  compilation_test.go    # NEW: TestFinalizeCompilations*, TestCompilationRescan

internal/tvscan/
  scanner.go             # existing
  scanner_test.go        # NEW: TestUpsertTVFile*, TestRescanBehavior, TestPruneMissing
  identify.go            # existing
  identify_test.go       # existing (no changes)

internal/web/
  handler.go             # existing
  handler_test.go        # existing (Phase 3 template tests)
  helpers.go             # existing
  helpers_test.go        # existing (Phase 3 pathID/pathSegment tests)
  handlers_music_test.go # NEW: TestMusicHome, TestMusicArtist404, TestMusicAlbum*
  handlers_tv_test.go    # NEW: TestTVSeriesList, TestTVSeriesDetail*, TestTVSeason*
  handlers_settings_test.go # NEW: TestLibraries, TestLibrariesNew, TestLibrariesScan
```

### Pattern 1: Scanner Test with Real DB
**What:** Create test DB, insert prerequisite data, call scanner function, verify DB state
**When to use:** All scanner tests (TEST-01, TEST-02, TEST-03)
**Example:**
```go
func TestEnsureArtist(t *testing.T) {
    db := openTestDB(t)
    // Insert a library first (FK constraint)
    _, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('test', 'music', '/media/music')")
    if err != nil {
        t.Fatalf("insert library: %v", err)
    }

    tx, err := db.Begin()
    if err != nil {
        t.Fatalf("begin: %v", err)
    }
    defer tx.Rollback()

    id, err := ensureArtist(context.Background(), tx, 1, "Test Artist")
    if err != nil {
        t.Fatalf("ensureArtist: %v", err)
    }
    if id <= 0 {
        t.Fatalf("expected positive id, got %d", id)
    }

    // Call again -- should return same ID (idempotent)
    id2, err := ensureArtist(context.Background(), tx, 1, "Test Artist")
    if err != nil {
        t.Fatalf("ensureArtist second call: %v", err)
    }
    if id2 != id {
        t.Fatalf("expected same id %d, got %d", id, id2)
    }
}
```

### Pattern 2: Handler Test with httptest
**What:** Create Handler via New(Deps), get Router, issue httptest requests, check response
**When to use:** All handler tests (TEST-04, TEST-05, TEST-06)
**Example:**
```go
func TestMusicHome200(t *testing.T) {
    dir := t.TempDir()
    setupTemplateDir(t, dir)
    withChdir(t, dir)

    db := openTestDB(t)
    h, err := New(Deps{
        Cfg: config.Config{DataDir: dir, MediaRoot: dir},
        DB:  db,
    })
    if err != nil {
        t.Fatalf("New: %v", err)
    }
    router := h.Router()

    req := httptest.NewRequest("GET", "/music", nil)
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)

    if w.Code != 200 {
        t.Fatalf("GET /music = %d, want 200", w.Code)
    }
}
```

### Pattern 3: TV Scanner upsertTVFile Test
**What:** Bypass ScanTV filesystem walk and ffprobe by calling upsertTVFile directly
**When to use:** TEST-03 TV scanner tests
**Example:**
```go
func TestUpsertTVFile(t *testing.T) {
    db := openTestDB(t)
    // Insert TV library
    _, _ = db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('tv', 'tv', '/media/tv')")

    s := &Scanner{DB: db}
    ident := &EpisodeIdentity{
        ShowTitle:      "Breaking Bad",
        SeasonNumber:   1,
        EpisodeNumbers: []int{1},
        Confidence:     0.72,
        Method:         "sxe",
    }

    err := s.upsertTVFile(context.Background(), 1, "/media/tv/bb/s01e01.mkv", "mkv", 1024, 1700000000, "{}", ident)
    if err != nil {
        t.Fatalf("upsertTVFile: %v", err)
    }

    // Verify DB state
    var title string
    var season int
    db.QueryRow("SELECT guessed_title, season_number FROM tv_series_identities WHERE file_id=1").Scan(&title, &season)
    if title != "Breaking Bad" {
        t.Fatalf("guessed_title = %q, want Breaking Bad", title)
    }
}
```

### Pattern 4: MP3 Fixture Creation for ScanFile Tests
**What:** Create minimal valid MP3 files with ID3v2 headers for ScanFile tests
**When to use:** TEST-01 ScanFile tests that need `music.ReadTrackMeta` to succeed
**Example:**
```go
// writeMinimalMP3 creates a tiny valid MP3 file with ID3v2 text frames.
func writeMinimalMP3(t *testing.T, path string, artist, album, title string, track, disc, year int) {
    t.Helper()
    // ID3v2.3 header + text frames + a few bytes of MP3 sync
    // The music.ReadTrackMeta function uses dhowden/tag which needs a valid-ish MP3
    var buf bytes.Buffer
    // ... build minimal ID3v2.3 header with TPE1, TALB, TIT2, TRCK, TPOS, TDRC frames
    // ... append MP3 frame sync bytes (0xFF 0xFB) to make it parseable
    os.MkdirAll(filepath.Dir(path), 0o755)
    os.WriteFile(path, buf.Bytes(), 0o644)
}
```

### Anti-Patterns to Avoid
- **Mocking the database:** Don't introduce an interface-based DB mock. Use real SQLite -- it catches SQL bugs, schema mismatches, and FK constraint violations that mocks would hide.
- **Testing ScanMusic end-to-end:** Don't test the full WalkDir pipeline with fixtures. Test ScanFile (per-file) and finalizeCompilations (post-scan) separately. The WalkDir loop is trivial plumbing.
- **Testing ScanTV end-to-end:** Don't require ffprobe in tests. Test upsertTVFile and pruneMissingFiles directly.
- **Importing test packages across boundaries:** Don't import internal/scan in internal/web tests. Handler tests should only seed DB data directly, not run scanners.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Test DB setup | Custom DB mock layer | `openTestDB(t)` + `isodb.Open` + `isodb.Migrate` | Already exists in web package; real SQLite catches SQL bugs |
| HTTP request construction | Manual request building | `httptest.NewRequest` + `httptest.NewRecorder` | stdlib, already used in project |
| Template directory setup | Per-test template creation | `setupTemplateDir(t, dir)` from handler_test.go | Already exists and covers all 22 page templates |
| Test fixture directory cleanup | Manual os.RemoveAll | `t.TempDir()` | Auto-cleaned, project convention |

**Key insight:** The project already has all necessary test infrastructure in handler_test.go (openTestDB, setupTemplateDir, withChdir). Scanner tests need a duplicate openTestDB in their own package (same pattern, different package).

## Common Pitfalls

### Pitfall 1: Foreign Key Constraints on Test Data
**What goes wrong:** Inserting test tracks/albums without their parent library/artist rows causes FK violations
**Why it happens:** SQLite with `foreign_keys=ON` (set in Open dsn) enforces referential integrity
**How to avoid:** Always insert prerequisite rows in order: library -> artist -> album -> track. Use a helper function that creates a complete test dataset.
**Warning signs:** "FOREIGN KEY constraint failed" errors in test output

### Pitfall 2: music.ReadTrackMeta Needs Valid Audio Files
**What goes wrong:** ScanFile silently returns nil (skips file) when ReadTrackMeta fails on invalid files
**Why it happens:** ScanFile logs the error and returns nil (not an error) to allow scan to continue
**How to avoid:** For ScanFile tests, create minimal valid MP3 files with ID3v2 headers. For unit-testing ensureArtist/ensureAlbum/finalizeCompilations, bypass ReadTrackMeta entirely by calling DB helpers directly.
**Warning signs:** Tests pass but verify nothing because ScanFile returned early without touching the DB

### Pitfall 3: pathguard.ResolveExistingUnderRoot in ScanFile
**What goes wrong:** ScanFile calls pathguard which resolves symlinks and checks containment -- test files must be under MediaRoot
**Why it happens:** pathguard verifies the real path of the file is under Config.MediaRoot
**How to avoid:** Set Config.MediaRoot to the test temp directory, and place fixture files under it
**Warning signs:** ScanFile silently returning nil for all test files

### Pitfall 4: Handler Auth Middleware Blocking Test Requests
**What goes wrong:** All handler test requests get redirected to /login
**Why it happens:** The Handler.Router() wraps the mux with auth middleware when auth is enabled
**How to avoid:** Set auth-related config to disable auth: use Config with AuthEnabled false (or empty AuthSessionSecret which disables auth in the auth.New constructor)
**Warning signs:** All handler tests getting 302 redirects to /login

### Pitfall 5: Template Directory os.Chdir Race
**What goes wrong:** Tests that call os.Chdir can race when run in parallel
**Why it happens:** os.Chdir affects the entire process, not just the current goroutine
**How to avoid:** Don't use t.Parallel() on tests that call withChdir. The existing handler_test.go already does this correctly.
**Warning signs:** Intermittent "file not found" errors on template files

### Pitfall 6: TV Scanner BUG-01 Rescan Test Logic
**What goes wrong:** Not testing the WHERE clause that prevents identity overwrite on resolved files
**Why it happens:** The upsertTVFile ON CONFLICT includes `WHERE status NOT IN ('resolved', 'skipped')` -- this is the BUG-01 fix
**How to avoid:** Test sequence: (1) insert file with resolved status, (2) call upsertTVFile again with different identity, (3) verify identity was NOT overwritten
**Warning signs:** Test only verifies initial insert, not the protection against overwrite

## Code Examples

### Helper: openTestDB for scan package
```go
// Place in internal/scan/scanner_test.go
func openTestDB(t *testing.T) *sql.DB {
    t.Helper()
    dir := t.TempDir()
    dbPath := filepath.Join(dir, "test.sqlite")
    conn, err := isodb.Open(dbPath)
    if err != nil {
        t.Fatalf("Open: %v", err)
    }
    t.Cleanup(func() { _ = conn.Close() })
    if err := isodb.Migrate(conn); err != nil {
        t.Fatalf("Migrate: %v", err)
    }
    return conn
}
```

### Helper: seed test library
```go
func seedLibrary(t *testing.T, db *sql.DB, name, libType, rootPath string) int64 {
    t.Helper()
    res, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES (?, ?, ?)", name, libType, rootPath)
    if err != nil {
        t.Fatalf("seed library: %v", err)
    }
    id, _ := res.LastInsertId()
    return id
}
```

### Helper: seed music data for handler tests
```go
func seedMusicData(t *testing.T, db *sql.DB) (libraryID, artistID, albumID, trackID int64) {
    t.Helper()
    libraryID = seedLibrary(t, db, "Music", "music", "/media/music")

    res, _ := db.Exec("INSERT INTO music_artists (library_id, name) VALUES (?, 'Test Artist')", libraryID)
    artistID, _ = res.LastInsertId()

    res, _ = db.Exec("INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year) VALUES (?, ?, ?, 'Test Album', 2024)", libraryID, artistID, artistID)
    albumID, _ = res.LastInsertId()

    res, _ = db.Exec("INSERT INTO music_tracks (library_id, artist_id, album_id, title, track_no, disc_no, abs_path, mime_type) VALUES (?, ?, ?, 'Track 1', 1, 1, '/media/music/track1.mp3', 'audio/mpeg')", libraryID, artistID, albumID)
    trackID, _ = res.LastInsertId()

    return
}
```

### Helper: create Handler for handler tests
```go
func newTestHandler(t *testing.T) (*Handler, *sql.DB) {
    t.Helper()
    dir := t.TempDir()
    setupTemplateDir(t, dir)
    withChdir(t, dir)

    db := openTestDB(t)
    h, err := New(Deps{
        Cfg: config.Config{DataDir: dir, MediaRoot: dir},
        DB:  db,
    })
    if err != nil {
        t.Fatalf("New: %v", err)
    }
    return h, db
}
```

### Test: musicArtistAlbums 404 for invalid ID
```go
func TestMusicArtistNotFound(t *testing.T) {
    h, _ := newTestHandler(t)
    router := h.Router()

    req := httptest.NewRequest("GET", "/music/artist/999", nil)
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)

    if w.Code != 404 {
        t.Fatalf("GET /music/artist/999 = %d, want 404", w.Code)
    }
}
```

### Test: Method not allowed
```go
func TestMusicHomeMethodNotAllowed(t *testing.T) {
    h, _ := newTestHandler(t)
    router := h.Router()

    req := httptest.NewRequest("POST", "/music", nil)
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)

    if w.Code != 405 {
        t.Fatalf("POST /music = %d, want 405", w.Code)
    }
}
```

### Test: BUG-01 rescan behavior (TV)
```go
func TestUpsertTVFileRescanPreservesResolved(t *testing.T) {
    db := openTestDB(t)
    seedLibrary(t, db, "tv", "tv", "/media/tv")

    s := &Scanner{DB: db}
    ctx := context.Background()

    // Initial insert
    ident := &EpisodeIdentity{ShowTitle: "Show", SeasonNumber: 1, EpisodeNumbers: []int{1}, Confidence: 0.72, Method: "sxe"}
    if err := s.upsertTVFile(ctx, 1, "/media/tv/s01e01.mkv", "mkv", 1024, 1000, "{}", ident); err != nil {
        t.Fatalf("initial upsert: %v", err)
    }

    // Manually mark as resolved (simulating a match)
    db.Exec("UPDATE tv_series_identities SET status='resolved', provider='tmdb', series_id='12345' WHERE file_id=1")

    // Re-scan with different identity -- should NOT overwrite
    ident2 := &EpisodeIdentity{ShowTitle: "Different Show", SeasonNumber: 2, EpisodeNumbers: []int{3}, Confidence: 0.72, Method: "sxe"}
    if err := s.upsertTVFile(ctx, 1, "/media/tv/s01e01.mkv", "mkv", 2048, 2000, "{}", ident2); err != nil {
        t.Fatalf("rescan upsert: %v", err)
    }

    // Verify identity was NOT changed
    var title, seriesID string
    db.QueryRow("SELECT guessed_title, series_id FROM tv_series_identities WHERE file_id=1").Scan(&title, &seriesID)
    if title != "Show" {
        t.Fatalf("guessed_title = %q after rescan, want 'Show' (should be preserved)", title)
    }
    if seriesID != "12345" {
        t.Fatalf("series_id = %q after rescan, want '12345' (should be preserved)", seriesID)
    }
}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| No scanner tests | Must add scanner_test.go | Phase 4 | Critical gap -- scanner is core data pipeline |
| No handler tests beyond template validation | Must add per-handler endpoint tests | Phase 4 | Handlers verified only by template compilation currently |

**Current test coverage by package:**
- `internal/scan/` -- 0 test files (CRITICAL GAP)
- `internal/tvscan/` -- identify_test.go only (scanner.go untested)
- `internal/web/` -- handler_test.go (template init), helpers_test.go (pathID/pathSegment)
- Other packages have good coverage: auth, config, db, jobs, match, music, pathguard, video

## Open Questions

1. **MP3 fixture approach for ScanFile tests**
   - What we know: ScanFile calls music.ReadTrackMeta which uses dhowden/tag to parse audio files. Invalid files are silently skipped.
   - What's unclear: Whether a minimal ID3v2 header (without actual MP3 audio frames) is sufficient for dhowden/tag to return metadata without error.
   - Recommendation: Try building minimal valid MP3 with ID3v2.3 headers + sync bytes. If dhowden/tag rejects them, test at the ensureArtist/ensureAlbum/DB-interaction level instead and note ScanFile integration test gap.

2. **Handler test scope for settings/librariesScan**
   - What we know: librariesScan enqueues a job that runs scan.ScanMusic or tvscan.ScanTV. The job runs asynchronously.
   - What's unclear: Whether to verify only the HTTP response + job creation in DB, or wait for the job to complete.
   - Recommendation: Test only HTTP response (redirect or JSON) + verify scan_jobs row was created. Don't wait for async job execution -- that's integration test territory.

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go testing (stdlib) |
| Config file | None needed -- go test convention |
| Quick run command | `go test ./internal/scan/ ./internal/tvscan/ ./internal/web/ -count=1` |
| Full suite command | `go test ./... -count=1` |

### Phase Requirements to Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| TEST-01 | Music ScanFile tag read, upsert, art | unit | `go test ./internal/scan/ -run Music -count=1` | No -- Wave 0 |
| TEST-02 | Compilation detection | unit | `go test ./internal/scan/ -run Compil -count=1` | No -- Wave 0 |
| TEST-03 | TV scanner upsert, rescan (BUG-01) | unit | `go test ./internal/tvscan/ -run TV -count=1` | No -- Wave 0 |
| TEST-04 | Music handler routing/errors | unit | `go test ./internal/web/ -run Music -count=1` | No -- Wave 0 |
| TEST-05 | TV handler routing/errors | unit | `go test ./internal/web/ -run TV -count=1` | No -- Wave 0 |
| TEST-06 | Settings handler CRUD/scan | unit | `go test ./internal/web/ -run Librar -count=1` | No -- Wave 0 |

### Sampling Rate
- **Per task commit:** `go test ./internal/scan/ ./internal/tvscan/ ./internal/web/ -count=1`
- **Per wave merge:** `go test ./... -count=1`
- **Phase gate:** Full suite green before `/gsd:verify-work`

### Wave 0 Gaps
- [ ] `internal/scan/scanner_test.go` -- covers TEST-01 (ScanFile), TEST-02 (compilations)
- [ ] `internal/tvscan/scanner_test.go` -- covers TEST-03 (upsertTVFile, rescan)
- [ ] `internal/web/handlers_music_test.go` -- covers TEST-04 (music handler endpoints)
- [ ] `internal/web/handlers_tv_test.go` -- covers TEST-05 (TV handler endpoints)
- [ ] `internal/web/handlers_settings_test.go` -- covers TEST-06 (settings/library CRUD)

## Sources

### Primary (HIGH confidence)
- Project source code: `internal/scan/scanner.go`, `internal/tvscan/scanner.go`, `internal/web/handlers_*.go`
- Existing test files: `internal/web/handler_test.go`, `internal/web/helpers_test.go`, `internal/tvscan/identify_test.go`, `internal/db/db_test.go`, `internal/jobs/jobs_test.go`
- Project CLAUDE.md: test patterns, build commands, architecture

### Secondary (MEDIUM confidence)
- Go stdlib documentation for testing, httptest, database/sql patterns (well-known, stable)

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH -- pure stdlib Go testing, established project patterns
- Architecture: HIGH -- test file structure follows Go conventions and existing patterns
- Pitfalls: HIGH -- derived directly from reading the source code (FK constraints, pathguard, auth middleware, ReadTrackMeta behavior)

**Research date:** 2026-03-05
**Valid until:** 2026-04-05 (stable -- no external dependencies, Go stdlib)

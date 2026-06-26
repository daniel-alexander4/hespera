# Codebase Concerns

**Analysis Date:** 2026-03-05

## Tech Debt

**Massive Handler Files (God Functions):**
- Issue: `internal/web/handlers_music.go` (1340 lines) and `internal/web/handlers_tv.go` (984 lines) contain all handler logic, inline SQL, row types, and helper functions in single files. Each handler method manually scans SQL rows with repetitive boilerplate.
- Files: `internal/web/handlers_music.go`, `internal/web/handlers_tv.go`, `internal/web/handlers_settings.go`
- Impact: Hard to test handlers in isolation. Changes to one handler risk breaking others in the same file. No HTTP handler tests exist.
- Fix approach: Extract data access into a repository/store layer (e.g., `internal/store/music.go`). Keep handlers thin, calling store methods. This also makes unit testing straightforward.

**All SQL Inline in Handlers:**
- Issue: Over 80 raw SQL queries are embedded directly in handler functions across the web package. No data access layer exists. The same table queries are duplicated across multiple handlers (e.g., album lookups appear in `musicAlbumTracks`, `musicAlbumEdit`, `musicAlbumRescan`, `musicPlayer`).
- Files: `internal/web/handlers_music.go`, `internal/web/handlers_tv.go`, `internal/web/handlers_settings.go`, `internal/web/handlers_match.go`
- Impact: SQL changes require updates in multiple places. Query duplication means schema changes can cause silent bugs if one copy is missed.
- Fix approach: Create a `internal/store/` package with typed query methods (e.g., `store.GetAlbumByID(ctx, id)`) and reuse across handlers.

**CLI Stub Not Implemented:**
- Issue: `cmd/isocli/main.go` prints usage text and exits. There is no way to add users or SSH keys without direct database manipulation. The auth system requires users+keys in the database but provides no tooling to create them.
- Files: `cmd/isocli/main.go`
- Impact: Auth is unusable without manual SQLite inserts. New deployments with `AUTH_ENABLED=true` have no way to bootstrap access.
- Fix approach: Implement the CLI commands listed in the stub: `add-user`, `list-users`, `add-key`, `remove-key`. Reuse `internal/auth.Store` methods.

**Double Directory Walk on Scan:**
- Issue: Both `scan.ScanMusic()` and `tvscan.ScanTV()` walk the entire directory tree twice: first to count files (for progress), then to process them. For large libraries, this doubles I/O time.
- Files: `internal/scan/scanner.go` (lines 52-58, 64-90), `internal/tvscan/scanner.go` (lines 44-54, 57-178)
- Impact: Scan time is roughly doubled for large libraries with many files.
- Fix approach: Walk once, collecting paths into a slice, then iterate the slice for processing. Update progress total after initial walk completes.

**No Migration Versioning:**
- Issue: `db.Migrate()` runs the full `CREATE TABLE IF NOT EXISTS` schema as one big SQL string, then applies ad-hoc column additions via `ensureColumn()`. There is no migration version table or ordered migration files. As the schema grows, this becomes fragile.
- Files: `internal/db/migrate.go`
- Impact: No way to track which migrations have run. Adding new migrations requires careful idempotency analysis. The `migrateIdentitiesSkippedStatus()` function shows the complexity of table-recreation needed for CHECK constraint changes in SQLite.
- Fix approach: Add a `schema_migrations` table tracking applied version numbers. Create numbered migration files and apply them sequentially. Keep `ensureColumn()` as a utility for simple additions.

## Known Bugs

**TV Identity Overwrite on Rescan:**
- Symptoms: When a TV library is rescanned, the `ON CONFLICT(file_id) DO UPDATE` in `tvscan.ScanTV()` overwrites `guessed_title`, `season_number`, and `episode_numbers_csv` even for files that have already been manually resolved to a TMDB series. The `status` is not in the update clause, but the identity metadata fields are overwritten.
- Files: `internal/tvscan/scanner.go` (lines 147-156)
- Trigger: Rescan a TV library after manually resolving some episodes.
- Workaround: The status stays `resolved`, but the `guessed_title` and other parse-derived fields get overwritten. Functional impact is low since resolved episodes are keyed on `series_id` not `guessed_title`.

## Security Considerations

**Internal Error Messages Exposed to Clients:**
- Risk: Over 100 instances of `http.Error(w, err.Error(), ...)` in handler code send raw Go error messages (including SQL errors, file system paths, and internal details) directly to HTTP responses.
- Files: `internal/web/handlers_music.go`, `internal/web/handlers_tv.go`, `internal/web/handlers_settings.go`, `internal/web/handlers_match.go`
- Current mitigation: Auth middleware prevents unauthenticated access (when enabled). The application is designed for local/trusted network use.
- Recommendations: Replace `http.Error(w, err.Error(), ...)` with generic user-facing messages. Log the detailed error server-side with `slog`. Example: `slog.Error("query failed", "err", err); http.Error(w, "internal server error", 500)`.

**ensureColumn SQL Injection Surface:**
- Risk: `ensureColumn()` uses string concatenation to build `ALTER TABLE` statements: `db.Exec("ALTER TABLE " + table + " ADD COLUMN " + col + " " + decl)`. While currently called only with hardcoded strings, the function signature accepts arbitrary strings.
- Files: `internal/db/migrate.go` (lines 303-326)
- Current mitigation: Only called from `Migrate()` with literal string arguments.
- Recommendations: Add a comment documenting that arguments must be trusted. Alternatively, validate `table` and `col` against `[a-zA-Z_]+` before use.

**Art Endpoints Are Public (Unauthenticated):**
- Risk: `/art/*` paths are in the public path list (`isPublicPath` returns true for anything under `/art/`). This exposes album and artist art images without authentication.
- Files: `internal/auth/auth.go` (line 469)
- Current mitigation: Art images are low-sensitivity data. The app is intended for local network use.
- Recommendations: Acceptable for current use case. If deployed publicly, consider requiring auth for art endpoints or at least limiting access to thumbnails.

**In-Memory Auth State Not Persistent:**
- Risk: Challenge states and rate-limit counters are stored in Go maps (`Manager.challenges`, `Manager.rateByIP`). Server restart clears all active challenges and rate-limit history.
- Files: `internal/auth/auth.go` (lines 53-56)
- Current mitigation: Challenges have a 10-minute TTL. Restart only affects in-flight login attempts.
- Recommendations: Acceptable for single-instance deployment. If scaling to multiple instances, move challenge state to the database.

**Detached Goroutine in TV Match Approve:**
- Risk: `tvMatchApprove` fires a `go func()` to fetch show metadata in the background with `context.Background()`. This goroutine is untracked, has no timeout, and errors are silently logged.
- Files: `internal/web/handlers_tv.go` (lines 609-614)
- Current mitigation: The goroutine only fetches metadata, not critical data. Failure is logged.
- Recommendations: Route this through the `jobs.Service` queue instead of a raw goroutine, so it gets tracking, cancellation support, and error visibility.

**CSRF Check Permissive When Origin and Referer Both Absent:** _(deferred, added 2026-06-25 code-review)_
- Risk: `isSameOriginUnsafeRequest` returns allow when an unsafe-method request carries neither `Origin` nor `Referer`, so a non-browser client can skip the same-origin CSRF check.
- Files: `internal/auth/auth.go` (lines 497-521)
- Current mitigation: Session cookies are `SameSite=Lax`, which already blocks the cross-site authenticated POST that CSRF relies on; the Origin/Referer check is defense-in-depth. Deliberately left permissive — tightening to "deny when a session cookie is present but both headers are absent" would break legitimate same-origin JSON/CLI clients that omit `Referer`.
- Revisit trigger: if a first-party client appears that relies on a non-`Lax` cookie, or the cookie's `SameSite` is ever loosened.

**Rate Limiting Keys on RemoteAddr Only (no trusted-proxy support):** _(deferred, added 2026-06-25 code-review)_
- Risk: `clientIP` derives the rate-limit/CSRF key solely from `r.RemoteAddr`, ignoring `X-Forwarded-For`. Behind a reverse proxy, every request would share the proxy's address and collapse the 10/min verify limiter into one global bucket.
- Files: `internal/auth/auth.go` (lines 444-450)
- Current mitigation: No reverse proxy is deployed — `docker-compose.yml` publishes `8080` directly — so every client already has a distinct `RemoteAddr` and the limiter works correctly. Naively trusting `X-Forwarded-For` now would *introduce* a spoofing vector (anyone could forge the header).
- Revisit trigger: if a reverse proxy is put in front of the app, add trusted-proxy-gated XFF parsing behind an explicit allowlist (e.g. `ISOMEDIA_TRUSTED_PROXIES`).

## Performance Bottlenecks

**Music Home Page N+1 Query Pattern:**
- Problem: `musicHome()` executes 4 separate database queries sequentially: recently played artists, recently added albums, all artists with subquery counts, and all compilations. The "all artists" query uses correlated subqueries for `track_count` and `play_count` per artist row.
- Files: `internal/web/handlers_music.go` (lines 141-273)
- Cause: No pagination, no caching, no denormalization. All artists and compilations are loaded on every page load.
- Improvement path: Add pagination or "load more" for artists. Pre-compute track counts as a materialized column updated on scan. Consider a lightweight in-memory cache with short TTL.

**SHA-256 Checksum on Every New/Changed File:**
- Problem: `resolveTrackChecksum()` reads the entire file to compute SHA-256 when the file is new or has changed (size/mtime differ). For large FLAC files (50-100+ MB), this is expensive.
- Files: `internal/scan/scanner.go` (lines 328-358)
- Cause: Full file read required for checksumming. No partial/streaming optimization.
- Improvement path: The mtime+size shortcut already avoids redundant checksums. For initial scans, consider deferring checksums to a background job, or use a faster hash (xxhash) for dedup detection and reserve SHA-256 for integrity verification.

**No Database Indexes for Common Handler Queries:**
- Problem: Several handler queries filter on `lower(a.name)` or `lower(al.title)` which cannot use the existing indexes (SQLite indexes are case-sensitive by default). The `CASE WHEN al.album_artist_id > 0 THEN al.album_artist_id ELSE al.artist_id END` pattern in multiple queries prevents index usage.
- Files: `internal/web/handlers_music.go` (lines 282-289, 865-873), `internal/db/migrate.go`
- Cause: Missing `COLLATE NOCASE` on text columns or missing expression indexes.
- Improvement path: Add `COLLATE NOCASE` to `name` and `title` columns in the schema, or create expression indexes.

## Fragile Areas

**Scanner's Compilation Detection Logic:**
- Files: `internal/scan/scanner.go` (lines 142-173)
- Why fragile: Compilation detection depends on multiple heuristics in a specific order: explicit tag flag, artist diversity check (queries existing tracks), album artist name comparison, and "Various Artists" special casing. The `mergeAlbumVariants()` call moves tracks between albums mid-scan, meaning scan order affects the result.
- Safe modification: Add comprehensive test cases covering mixed-artist albums, "Various Artists" edge cases, and re-scan scenarios before changing this logic.
- Test coverage: No tests exist for the scanner. The compilation detection logic is only tested indirectly.

**Template Registration is Static:**
- Files: `internal/web/handler.go` (lines 42-65)
- Why fragile: The `pages` slice in `New()` must be manually updated when adding new templates. Missing a page silently fails at runtime (`template not found` logged, 500 returned). No compile-time or startup-time validation that all templates referenced by handlers exist.
- Safe modification: When adding a new handler, always add the corresponding template to the `pages` slice. Consider auto-discovering templates from the filesystem.
- Test coverage: No tests verify that all templates compile.

**URL Path Parsing by String Prefix:**
- Files: `internal/web/handlers_music.go` (lines 316-323, 451-458, 1042-1048), `internal/web/handlers_tv.go` (lines 172-178, 726-733)
- Why fragile: Multiple handlers parse entity IDs from URL paths using `strings.TrimPrefix(r.URL.Path, "/music/artist/")` followed by `path.Clean` and `strconv.ParseInt`. This pattern is repeated identically in 6+ handlers with no shared helper. A route registration typo would cause silent misparses.
- Safe modification: Extract a `parsePathID(r.URL.Path, prefix)` helper to eliminate duplication.
- Test coverage: No HTTP-level tests verify URL parsing.

## Scaling Limits

**Single Job Worker Goroutine:**
- Current capacity: 1 concurrent job, 128-item queue buffer.
- Limit: Large libraries with many albums cause music match jobs to take hours (500ms delay per album for rate limiting). During this time, no other jobs (scan, writeback, TV match) can run.
- Scaling path: Allow configurable worker count. Use separate queues or priority for different job types. At minimum, allow scan and match jobs to run concurrently.

**SQLite Single-Writer Constraint:**
- Current capacity: WAL mode allows concurrent readers, but only one writer at a time. 5-second busy timeout.
- Limit: Concurrent write-heavy operations (multiple scans, play event recording during scan) can hit busy timeouts.
- Scaling path: Acceptable for single-user/small-household use. If scaling, consider partitioning writes or moving to a client-server database.

## Dependencies at Risk

**gcottom/audiometa/v3 - Transitive Dependency Chain:**
- Risk: The `audiometa` package pulls in `flacmeta`, `mp4meta`, `mp3meta`, and `oggmeta` as separate dependencies. These are relatively niche packages with small maintainer communities. Breaking changes or abandonment in any sub-package affects tag writing.
- Impact: Tag writing for FLAC, OGG, Opus, and M4A formats breaks if any sub-package has incompatibilities.
- Migration plan: The `bogem/id3v2/v2` package handles MP3 directly. For other formats, audiometa is currently the best pure-Go option. Monitor for alternatives.

## Missing Critical Features

**No User Management UI:**
- Problem: There is no web interface for creating users, adding SSH keys, or managing authentication. The CLI (`isocli`) is unimplemented. Users must manually insert rows into `auth_users` and `auth_user_keys` tables.
- Blocks: Auth is effectively unusable for non-technical users. Deployments with `AUTH_ENABLED=true` require SQLite CLI access for initial setup.

**No Movie Scanning/Matching:**
- Problem: The `movies_home.html` template is registered and the `/movies` route exists, but there is no movie scanner, no movie metadata matcher, and no movie browsing UI. The `movie_files`, `movie_metadata_cache`, `movie_art`, and `movie_playback_progress` tables exist in the schema but are empty.
- Blocks: Movies library type can be created but cannot be scanned or browsed.

## Test Coverage Gaps

**No Handler/HTTP Tests:**
- What's not tested: All 40+ HTTP handler functions in `internal/web/` have zero test coverage. No tests verify routing, request parsing, error responses, or template rendering.
- Files: `internal/web/handlers_music.go`, `internal/web/handlers_tv.go`, `internal/web/handlers_settings.go`, `internal/web/handlers_match.go`, `internal/web/handlers_core.go`
- Risk: Handler regressions (broken routes, SQL errors, missing template variables) are only caught by manual testing.
- Priority: High

**No Scanner Tests:**
- What's not tested: `internal/scan/scanner.go` has no tests. The core `ScanFile()`, `ScanMusic()`, compilation detection, art extraction, and cleanup logic are entirely untested.
- Files: `internal/scan/scanner.go`
- Risk: Scanner bugs can corrupt the music database (wrong artist/album associations, lost tracks). Compilation merging logic is particularly complex.
- Priority: High

**No TV Scanner Tests:**
- What's not tested: `internal/tvscan/scanner.go` has no tests. File identification is tested (`identify_test.go`), but the actual scan-and-persist pipeline is not.
- Files: `internal/tvscan/scanner.go`
- Risk: TV scan bugs silently lose or misassociate files.
- Priority: Medium

**No Match Pipeline Integration Tests:**
- What's not tested: `internal/match/pipeline.go` has no tests. The MusicBrainz client, scorer, and dedup each have unit tests, but the orchestrating `RunMusicMatch()` pipeline (which coordinates all of them plus database writes) has no coverage.
- Files: `internal/match/pipeline.go`, `internal/match/coverart.go`, `internal/match/artistmeta.go`
- Risk: Pipeline bugs (wrong status transitions, art overwrite logic, artist enrichment failures) are only caught via manual testing.
- Priority: Medium

**No TMDB Matcher Integration Tests:**
- What's not tested: `internal/tmdb/matcher.go` has no tests for `RunTVMatch()`, `FetchShowMetadata()`, or the overall TV matching pipeline. Only JSON parsing is tested in `client_test.go`.
- Files: `internal/tmdb/matcher.go`
- Risk: TV matching regressions go undetected.
- Priority: Medium

---

*Concerns audit: 2026-03-05*

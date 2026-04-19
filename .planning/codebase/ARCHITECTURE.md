# Architecture

**Analysis Date:** 2026-03-05

## Pattern Overview

**Overall:** Monolithic server-rendered web application with dependency injection and background job processing.

**Key Characteristics:**
- Single-binary Go HTTP server using stdlib `net/http` and `html/template`
- No framework -- raw `http.ServeMux` routing, hand-rolled auth, inline SQL queries
- SQLite (WAL mode) as the sole data store via pure-Go driver (`modernc.org/sqlite`)
- Background work via a buffered channel job queue with single worker goroutine
- Server-rendered HTML with vanilla CSS/JS (no frontend framework, no build step)
- Media-type-specific scanner/matcher pipelines (music, TV, movies) that run as background jobs

## Layers

**Configuration Layer:**
- Purpose: Load and validate environment-based config at startup
- Location: `internal/config/`
- Contains: `Config` struct, `FromEnv()` constructor, `Validate()` method
- Depends on: OS environment variables (ISOMEDIA_ prefix)
- Used by: `cmd/isomedia/main.go`, `internal/web/handler.go`, `internal/scan/scanner.go`, `internal/tvscan/scanner.go`

**Database Layer:**
- Purpose: SQLite connection setup, schema creation, migrations
- Location: `internal/db/`
- Contains: `Open()` (WAL mode, connection pool), `Migrate()` (schema DDL + `ensureColumn()` for additive migrations)
- Depends on: `modernc.org/sqlite`
- Used by: Every package that touches persistent state (via `*sql.DB` passed through dependency injection)

**Authentication Layer:**
- Purpose: SSH pubkey challenge-response auth, HMAC-SHA256 session cookies, CSRF protection, rate limiting
- Location: `internal/auth/`
- Contains: `Manager` (challenge lifecycle, session management, middleware), `Store` (user/key CRUD)
- Depends on: `internal/config`, `database/sql`, external `ssh-keygen` binary
- Used by: `internal/web/handler.go` (wraps router), `internal/web/handlers_core.go` (login/challenge/verify endpoints)

**Web/Handler Layer:**
- Purpose: HTTP request handling, template rendering, routing
- Location: `internal/web/`
- Contains: `Handler` struct (central dependency holder), route registration, all HTTP handlers, middleware
- Depends on: `internal/config`, `internal/auth`, `internal/jobs`, `internal/scan`, `internal/match`, `internal/tmdb`, `internal/tvscan`, `internal/music`, `internal/pathguard`
- Used by: `cmd/isomedia/main.go`
- Key files:
  - `internal/web/handler.go` -- `Handler` struct, `New()` constructor, template compilation, `render()` method
  - `internal/web/router.go` -- All route registrations in `Router()`
  - `internal/web/middleware.go` -- Request logging middleware
  - `internal/web/handlers_core.go` -- Home, health, login, auth, movies
  - `internal/web/handlers_music.go` -- Music browse, player, streaming, art, album edit, duplicates, play events
  - `internal/web/handlers_match.go` -- Music match/review/approve/reject/writeback
  - `internal/web/handlers_tv.go` -- TV browse, series/season detail, match review, streaming, playback progress
  - `internal/web/handlers_settings.go` -- Libraries CRUD, scan triggers, job management, tag editor

**Job Queue Layer:**
- Purpose: Async background work with progress tracking and cancellation
- Location: `internal/jobs/`
- Contains: `Service` struct with buffered channel (128), single worker goroutine, cancel map
- Depends on: `database/sql` (reads/writes `scan_jobs` table directly)
- Used by: `internal/web/handlers_settings.go` (scan/match triggers), `internal/web/handlers_match.go` (music match/writeback), `internal/web/handlers_tv.go` (TV match)

**Music Scanner Layer:**
- Purpose: Walk filesystem, read audio tags, upsert artist/album/track records, extract embedded art, prune stale entries
- Location: `internal/scan/`
- Contains: `Scanner` struct, `ScanMusic()` (full library walk), `ScanFile()` (single file), `ScanFiles()` (targeted rescan after edits)
- Depends on: `internal/config`, `internal/music`, `internal/pathguard`
- Used by: `internal/web/handlers_settings.go` (library scan), `internal/web/handlers_music.go` (album edit rescan)

**Music Tag Reader Layer:**
- Purpose: Read audio file metadata (artist, album, title, year, track/disc, compilation flags, embedded art)
- Location: `internal/music/`
- Contains: `TrackMeta` struct, `ReadTrackMeta()` (dhowden/tag wrapper with ID3v2 binary fallback), `WriteTrackTags()` (id3v2/audiometa writeback), format detection helpers
- Depends on: `dhowden/tag`, `bogem/id3v2/v2`, `gcottom/audiometa/v3`
- Used by: `internal/scan/scanner.go`, `internal/match/writeback.go`, `internal/web/handlers_music.go`

**Music Match Pipeline Layer:**
- Purpose: Match local albums against MusicBrainz, fetch cover art from CAA, enrich artists from Wikipedia/Wikimedia
- Location: `internal/match/`
- Contains:
  - `Matcher` struct -- orchestrates the full pipeline (`RunMusicMatch`, `RunTagWriteback`)
  - `MBClient` -- MusicBrainz API with 1 req/sec rate limiter, 3-strategy query cascade
  - `CAAClient` -- Cover Art Archive fetch with release-group/release fallback
  - `ScoreCandidate()` / `BestCandidate()` -- Weighted scoring algorithm
  - `NormalizedSimilarity()` / `LevenshteinDistance()` -- String similarity
  - `NormalizeTitle()` / `NormalizeForDedup()` -- Title normalization (strip remaster/deluxe/explicit annotations)
  - `FindDuplicateAlbums()` / `MergeAlbums()` -- Duplicate detection and merge
  - `EnrichArtist()` -- Wikipedia bio + Wikimedia Commons image via Wikidata
- Depends on: `internal/music` (for tag writeback), external APIs (MusicBrainz, Cover Art Archive, Wikipedia, Wikidata, Wikimedia Commons)
- Used by: `internal/web/handlers_match.go`, `internal/web/handlers_settings.go`, `internal/web/handlers_music.go`, `internal/tmdb/matcher.go`

**TV Scanner Layer:**
- Purpose: Walk filesystem for video files, probe with ffprobe, identify episodes from filename patterns
- Location: `internal/tvscan/`
- Contains: `Scanner.ScanTV()` (full library walk), `IdentifyFile()` (regex-based episode identification: SXE, NxNN, season dir fallback)
- Depends on: `internal/config`, `internal/pathguard`, `internal/video`
- Used by: `internal/web/handlers_settings.go`

**TV Match Layer:**
- Purpose: Match identified TV episodes against TMDB, cache metadata, download art
- Location: `internal/tmdb/`
- Contains: `Client` (TMDB API, 4 req/sec rate limiter), `Matcher` (match pipeline, metadata caching, art download)
- Depends on: `internal/match` (for `NormalizedSimilarity`), TMDB API
- Used by: `internal/web/handlers_tv.go`, `internal/web/handlers_settings.go`

**Video Utilities Layer:**
- Purpose: Video file extension detection and ffprobe wrapper
- Location: `internal/video/`
- Contains: `IsVideoExt()`, `Probe()` (ffprobe JSON output parser)
- Depends on: External `ffprobe` binary
- Used by: `internal/tvscan/scanner.go`, `internal/web/handlers_tv.go`

**Path Security Layer:**
- Purpose: Prevent path traversal attacks by resolving symlinks and checking containment
- Location: `internal/pathguard/`
- Contains: `ResolveExistingUnderRoot()`, `WithinRoot()`
- Depends on: Nothing (stdlib only)
- Used by: `internal/scan/scanner.go`, `internal/tvscan/scanner.go`, `internal/web/handlers_music.go`, `internal/web/handlers_tv.go`

## Data Flow

**Library Scan (Music):**

1. User clicks "Scan" on libraries page, POST to `/libraries/scan` in `internal/web/handlers_settings.go`
2. Handler creates `scan.Scanner` and enqueues job via `h.jobs.Enqueue("scan", ...)` in `internal/jobs/jobs.go`
3. Job worker calls `scanner.ScanMusic()` in `internal/scan/scanner.go`
4. Scanner walks filesystem, calls `music.ReadTrackMeta()` for each audio file in `internal/music/tags.go`
5. Scanner upserts artist/album/track rows in SQLite within a transaction
6. Scanner extracts embedded art, saves to `{DataDir}/thumbs/music/`
7. After scan completes, a chained `music_match` job is enqueued automatically
8. `Matcher.RunMusicMatch()` enriches artists (MusicBrainz MBID, Wikipedia bio, Wikimedia image), then matches albums (MusicBrainz search, scoring, CAA cover art)

**Library Scan (TV):**

1. POST to `/libraries/scan` detects library type "tv"
2. Creates `tvscan.Scanner`, enqueues `tvscan` job
3. `ScanTV()` walks filesystem, filters by video extensions, probes with ffprobe
4. `IdentifyFile()` extracts show title, season, episode from filename via regex
5. Upserts `tv_series_files` and `tv_series_identities` rows
6. After scan, a chained `tv_match` job runs if TMDB API key is configured
7. `tmdb.Matcher.RunTVMatch()` searches TMDB, fetches show/season/episode metadata, downloads art, caches metadata in `tv_series_metadata_cache`

**HTTP Request (Page Render):**

1. Request hits `http.ServeMux` in `internal/web/router.go`
2. Auth middleware in `internal/auth/auth.go` checks session cookie; redirects to `/login` if invalid
3. CSRF check for unsafe methods (POST/PUT/PATCH/DELETE) via Origin/Referer validation
4. Handler method queries SQLite directly with inline SQL
5. `h.render()` renders Go template to buffer, writes with `Content-Type: text/html`, no-cache headers

**Audio Streaming:**

1. GET `/stream/track/{id}` in `internal/web/handlers_music.go`
2. Lookup `abs_path` from `music_tracks` table
3. Validate path with `pathguard.ResolveExistingUnderRoot()` against `MediaRoot`
4. Serve file with `http.ServeContent()` (supports range requests)

**State Management:**
- All state in SQLite. No in-memory caches (except auth challenge/rate-limit maps in `internal/auth/auth.go`)
- Art files stored on disk at `{DataDir}/thumbs/{music,tv}/`, paths tracked in DB
- Session state in HMAC-signed cookies (no server-side session store)
- Job state tracked in `scan_jobs` table with progress columns; single in-memory cancel map in `internal/jobs/jobs.go`

## Key Abstractions

**Handler (Dependency Root):**
- Purpose: Central dependency holder; all HTTP handlers are methods on `*Handler`
- File: `internal/web/handler.go`
- Pattern: Constructor injection via `web.Deps{Cfg, DB}`. `New()` compiles templates, creates `jobs.Service`, creates `auth.Manager`. All handler methods access `h.cfg`, `h.db`, `h.tpls`, `h.jobs`, `h.auth`.

**Scanner (Per-Request Construction):**
- Purpose: Filesystem walker + DB upserter for a specific media type
- Files: `internal/scan/scanner.go`, `internal/tvscan/scanner.go`
- Pattern: Constructed inline per handler call (`scan.New(h.cfg, h.db)`), passed as executor closure to `jobs.Enqueue`. Not long-lived -- created, used, discarded. Takes `config.Config` and `*sql.DB` as dependencies.

**Matcher (Per-Request Construction):**
- Purpose: External API matching pipeline for a specific media type
- Files: `internal/match/pipeline.go`, `internal/tmdb/matcher.go`
- Pattern: Same as Scanner -- `match.New(h.db, h.cfg.DataDir)` or `tmdb.NewMatcher(h.db, h.cfg.TMDBAPIKey, h.cfg.DataDir)`, passed as executor closure. Internally creates API clients with rate limiters.

**Job Executor (Closure Pattern):**
- Purpose: Decouple job scheduling from job execution
- File: `internal/jobs/jobs.go`
- Pattern: `Enqueue(jobType, libraryID, createdBy, executor)` where `executor` is `func(ctx context.Context, jobID, libraryID int64) error`. The handler creates the Scanner/Matcher and captures it in a closure. The job worker calls `req.Executor(ctx, req.JobID, req.LibraryID)`.

**Template Rendering:**
- Purpose: Server-side HTML generation
- File: `internal/web/handler.go`
- Pattern: Layout template (`web/templates/layout.html`) cloned per page, merged with partials (`partials_*.html` glob) and page template. Rendered to buffer first to catch errors. FuncMap provides `staticv` (cache-bust), `humanBytes`, `mult`.

## Entry Points

**Web Server (`cmd/isomedia/main.go`):**
- Location: `cmd/isomedia/main.go`
- Triggers: `go run ./cmd/isomedia`, Docker ENTRYPOINT
- Responsibilities: Configure slog, load config from env, open SQLite, run migrations, create `web.Handler`, start HTTP server with graceful shutdown (10s timeout on SIGINT/SIGTERM)

**CLI Stub (`cmd/isocli/main.go`):**
- Location: `cmd/isocli/main.go`
- Triggers: `go run ./cmd/isocli`
- Responsibilities: Placeholder for future user/key management. Currently prints usage and exits.

## Error Handling

**Strategy:** Fail-fast for startup errors; non-fatal per-item errors during background jobs; HTTP errors returned as plain text or JSON depending on client Accept header.

**Patterns:**
- Startup: `slog.Error()` + `os.Exit(1)` for config validation, DB open, or migration failure in `cmd/isomedia/main.go`
- Scanner/Matcher: Per-file/per-album errors logged with `slog.Warn()` and skipped; processing continues. Only context cancellation or DB-level errors abort the whole job.
- HTTP Handlers: `http.Error(w, msg, code)` for HTML clients; `jsonError(w, msg, code)` for JSON clients (checked via `requestWantsJSON(r)`). 404 for missing resources, 400 for bad input, 405 for wrong method, 500 for internal errors.
- Template rendering: `render()` executes to buffer first; if template execution fails, returns 500 with "internal server error" (avoids partial HTML writes).
- Auth: Rate limiting (10 verify attempts/min/IP), challenge expiry (10 min TTL, max 5 attempts per challenge), CSRF via Origin/Referer matching.

## Cross-Cutting Concerns

**Logging:** `log/slog` with JSON handler to stdout. Set in `cmd/isomedia/main.go`. Used throughout all packages. HTTP request logging via `withLogging` middleware in `internal/web/middleware.go` (method, path, status, duration).

**Validation:** Config validation in `internal/config/config.go` (`Validate()` method). Input validation inline in HTTP handlers (method checks, ID parsing, form validation). Path validation via `internal/pathguard/` before any filesystem access.

**Authentication:** SSH pubkey challenge-response in `internal/auth/auth.go`. Middleware wraps entire mux if `AuthEnabled`. Public paths exempted: `/healthz`, `/favicon.ico`, `/login`, `/auth/*`, `/static/*`, `/art/*`. CSRF protection for unsafe methods via same-origin check (Origin or Referer header).

**Path Security:** All filesystem access to user-controlled paths goes through `pathguard.ResolveExistingUnderRoot()` which resolves symlinks and verifies containment within `MediaRoot` or `DataDir`. Used in `internal/scan/scanner.go`, `internal/tvscan/scanner.go`, and all streaming/art handlers.

**Rate Limiting:** MusicBrainz API: 1 req/sec via mutex-based throttle in `internal/match/musicbrainz.go`. TMDB API: 4 req/sec via ticker channel in `internal/tmdb/client.go`. Auth verify: 10 attempts/min/IP via sliding window in `internal/auth/auth.go`. Inter-item delays: 500ms between albums/artists in match pipeline.

---

*Architecture analysis: 2026-03-05*

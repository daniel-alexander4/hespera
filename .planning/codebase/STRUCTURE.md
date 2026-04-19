# Codebase Structure

**Analysis Date:** 2026-03-05

## Directory Layout

```
isomedia/
├── cmd/
│   ├── isomedia/           # Web server binary entry point
│   │   └── main.go
│   └── isocli/             # CLI binary entry point (stub)
│       └── main.go
├── internal/
│   ├── auth/               # SSH pubkey auth, sessions, CSRF, rate limiting
│   │   ├── auth.go
│   │   ├── auth_test.go
│   │   └── store.go
│   ├── config/             # Env-based config struct + validation
│   │   ├── config.go
│   │   └── config_test.go
│   ├── db/                 # SQLite setup, schema, migrations
│   │   ├── db.go
│   │   ├── db_test.go
│   │   └── migrate.go
│   ├── jobs/               # Background job queue (channel-based, single worker)
│   │   ├── jobs.go
│   │   └── jobs_test.go
│   ├── match/              # Music metadata matching pipeline
│   │   ├── artistmeta.go   # Wikipedia bio + Wikimedia Commons image enrichment
│   │   ├── coverart.go     # Cover Art Archive client
│   │   ├── dedup.go        # Album duplicate detection + merge
│   │   ├── dedup_test.go
│   │   ├── musicbrainz.go  # MusicBrainz API client + search strategies
│   │   ├── normalize_test.go
│   │   ├── pipeline.go     # Matcher orchestrator (RunMusicMatch)
│   │   ├── scorer.go       # Match scoring algorithm
│   │   ├── scorer_test.go
│   │   ├── similarity.go   # String normalization + Levenshtein distance
│   │   ├── similarity_test.go
│   │   └── writeback.go    # Tag writeback job executor
│   ├── music/              # Audio tag reading + writing
│   │   ├── tags.go         # Tag reader (dhowden/tag + ID3v2 fallback)
│   │   ├── tags_test.go
│   │   ├── tagwrite.go     # Tag writer (id3v2 + audiometa)
│   │   └── tagwrite_test.go
│   ├── pathguard/          # Path traversal prevention
│   │   ├── pathguard.go
│   │   └── pathguard_test.go
│   ├── scan/               # Music library filesystem scanner
│   │   └── scanner.go
│   ├── tmdb/               # TMDB API client + TV matching pipeline
│   │   ├── client.go       # TMDB REST client (search, show, season, image download)
│   │   ├── client_test.go
│   │   └── matcher.go      # TV match orchestrator (RunTVMatch, FetchShowMetadata)
│   ├── tvscan/             # TV library filesystem scanner + episode identification
│   │   ├── identify.go     # Regex-based episode identification from filenames
│   │   ├── identify_test.go
│   │   └── scanner.go      # TV file walker + ffprobe + DB upsert
│   └── video/              # Video utilities (extension detection, ffprobe)
│       ├── extensions.go
│       ├── extensions_test.go
│       ├── probe.go        # ffprobe JSON wrapper
│       └── probe_test.go
├── web/
│   ├── static/             # Static assets (CSS, images)
│   │   ├── app.css
│   │   ├── missing.album.webp
│   │   └── icons/          # Icon assets
│   └── templates/          # Go html/template files
│       ├── layout.html     # Base layout (header, nav, footer, JS)
│       ├── home.html
│       ├── login.html
│       ├── libraries.html
│       ├── libraries_new.html
│       ├── settings.html
│       ├── settings_jobs.html
│       ├── settings_tags.html
│       ├── music_home.html
│       ├── music_artist.html
│       ├── music_album.html
│       ├── music_album_edit.html
│       ├── music_albums.html
│       ├── music_compilations.html
│       ├── music_duplicates.html
│       ├── music_match_review.html
│       ├── player.html
│       ├── tv_home.html
│       ├── tv_series.html
│       ├── tv_season.html
│       ├── tv_match_review.html
│       ├── tv_player.html
│       └── movies_home.html
├── data/                   # Local SQLite database (gitignored in production)
│   └── isomedia.sqlite
├── Dockerfile              # Multi-stage: Go 1.23 builder -> Ubuntu 24.04 runtime
├── docker-compose.yml      # Docker Compose config
├── go.mod                  # Go module (4 direct dependencies)
├── go.sum
├── CLAUDE.md               # AI assistant instructions
├── MEMORY.md               # Phase tracking
└── PLAN.md                 # Development plan
```

## Directory Purposes

**`cmd/isomedia/`:**
- Purpose: Web server entry point
- Contains: Single `main.go` that wires together config, DB, and Handler
- Key files: `cmd/isomedia/main.go`

**`cmd/isocli/`:**
- Purpose: CLI tool for user/key management (not yet implemented)
- Contains: Stub `main.go` that prints usage
- Key files: `cmd/isocli/main.go`

**`internal/auth/`:**
- Purpose: Authentication system -- SSH pubkey challenge-response, HMAC-SHA256 session cookies, CSRF protection, rate limiting
- Contains: Manager (auth lifecycle), Store (user/key DB operations), middleware
- Key files: `internal/auth/auth.go` (Manager, Middleware, challenge/verify flow), `internal/auth/store.go` (user/key CRUD)

**`internal/config/`:**
- Purpose: Configuration from environment variables with validation
- Contains: Config struct, env parsing helpers, validation
- Key files: `internal/config/config.go`

**`internal/db/`:**
- Purpose: Database setup and schema management
- Contains: SQLite WAL opener, full schema DDL, migration helpers
- Key files: `internal/db/db.go` (Open), `internal/db/migrate.go` (schema + ensureColumn migrations)

**`internal/jobs/`:**
- Purpose: Async background job queue with progress tracking
- Contains: Service struct, channel-based queue, worker goroutine, cancel support
- Key files: `internal/jobs/jobs.go`

**`internal/match/`:**
- Purpose: Music metadata matching -- MusicBrainz search, scoring, Cover Art Archive, artist enrichment, dedup, tag writeback
- Contains: API clients, scoring algorithm, string similarity, title normalization, duplicate detection, pipeline orchestrator
- Key files: `internal/match/pipeline.go` (Matcher orchestrator), `internal/match/musicbrainz.go` (MB client), `internal/match/scorer.go` (scoring), `internal/match/similarity.go` (normalization + Levenshtein), `internal/match/coverart.go` (CAA client), `internal/match/artistmeta.go` (Wikipedia/Wikimedia enrichment), `internal/match/dedup.go` (duplicate detection + merge), `internal/match/writeback.go` (tag writeback)

**`internal/music/`:**
- Purpose: Audio file tag reading and writing
- Contains: TrackMeta reader (with ID3v2 binary parser fallback), TagWriteFields writer, format detection, art verification
- Key files: `internal/music/tags.go` (ReadTrackMeta, ID3v2 parser), `internal/music/tagwrite.go` (WriteTrackTags)

**`internal/pathguard/`:**
- Purpose: Prevent path traversal attacks
- Contains: Symlink-resolving containment checker
- Key files: `internal/pathguard/pathguard.go`

**`internal/scan/`:**
- Purpose: Music library filesystem scanner
- Contains: Walk dirs, read tags, upsert artist/album/track, extract art, prune stale, cleanup empty
- Key files: `internal/scan/scanner.go`

**`internal/tmdb/`:**
- Purpose: TMDB API integration for TV show matching
- Contains: REST client (search, show, season, image download), TV match pipeline
- Key files: `internal/tmdb/client.go` (TMDB API client), `internal/tmdb/matcher.go` (TV match orchestrator)

**`internal/tvscan/`:**
- Purpose: TV library filesystem scanner and episode identification
- Contains: File walker, ffprobe integration, regex-based episode parser
- Key files: `internal/tvscan/scanner.go` (TV file scanner), `internal/tvscan/identify.go` (episode identification from filenames)

**`internal/video/`:**
- Purpose: Video file utilities
- Contains: Extension detection, ffprobe JSON wrapper
- Key files: `internal/video/extensions.go` (IsVideoExt), `internal/video/probe.go` (ffprobe wrapper)

**`internal/web/`:**
- Purpose: HTTP handlers, routing, template rendering, middleware
- Contains: Handler struct (central dependency holder), all route handlers organized by domain, logging middleware
- Key files: `internal/web/handler.go` (Handler struct, template compilation, render), `internal/web/router.go` (route registration), `internal/web/middleware.go` (request logging), `internal/web/handlers_core.go` (home, login, auth), `internal/web/handlers_music.go` (music browse/play/edit), `internal/web/handlers_match.go` (music matching review), `internal/web/handlers_tv.go` (TV browse/play/match), `internal/web/handlers_settings.go` (libraries, jobs, scan triggers, tag editor)

**`web/static/`:**
- Purpose: Static assets served at `/static/`
- Contains: CSS, placeholder images, icons
- Key files: `web/static/app.css`, `web/static/missing.album.webp`

**`web/templates/`:**
- Purpose: Go html/template files for server-rendered pages
- Contains: Base layout + per-page templates
- Key files: `web/templates/layout.html` (base layout with nav, theme JS)

## Key File Locations

**Entry Points:**
- `cmd/isomedia/main.go`: Web server (config -> DB -> migrate -> Handler -> ListenAndServe)
- `cmd/isocli/main.go`: CLI stub (prints usage)

**Configuration:**
- `internal/config/config.go`: Config struct, FromEnv(), Validate()
- `Dockerfile`: Multi-stage build (Go 1.23 -> Ubuntu 24.04)
- `docker-compose.yml`: Docker Compose setup

**Core Logic:**
- `internal/web/handler.go`: Handler struct, dependency injection, template system
- `internal/web/router.go`: All HTTP route definitions
- `internal/scan/scanner.go`: Music library scanner
- `internal/tvscan/scanner.go`: TV library scanner
- `internal/match/pipeline.go`: Music match pipeline
- `internal/tmdb/matcher.go`: TV match pipeline
- `internal/jobs/jobs.go`: Background job queue
- `internal/db/migrate.go`: Database schema (all tables defined here)

**Testing:**
- Tests co-located with source: `*_test.go` alongside implementation files
- Test helper: `openTestDB(t)` pattern using `t.TempDir()` (in `internal/db/db_test.go`)

## Naming Conventions

**Files:**
- Go source: lowercase snake_case (e.g., `scanner.go`, `handlers_music.go`, `coverart.go`)
- Handler files split by domain: `handlers_{domain}.go` (core, music, match, tv, settings)
- Tests: `{name}_test.go` co-located with source
- Templates: lowercase snake_case with domain prefix (e.g., `music_album.html`, `tv_series.html`, `settings_jobs.html`)

**Directories:**
- `cmd/{binary}/` for each executable
- `internal/{package}/` for all library code (enforces Go import restrictions)
- `web/` for frontend assets (outside `internal/` because Docker COPY needs access)

**Go Packages:**
- Package names match directory names (single lowercase word or abbreviation)
- No `pkg/` directory -- everything is `internal/`

**Go Types:**
- Exported types: PascalCase (e.g., `Scanner`, `Handler`, `Matcher`, `TrackMeta`)
- Row types for template data: unexported `{name}Row` (e.g., `artistRow`, `trackRow`, `scanJobRow`)
- Config: single `Config` struct in `config` package
- Constructor: `New(deps)` pattern returning pointer (e.g., `scan.New(cfg, db)`, `match.New(db, dataDir)`, `web.New(deps)`)

**Database Tables:**
- `music_artists`, `music_albums`, `music_tracks` (media-type prefix)
- `tv_series_files`, `tv_series_identities`, `tv_series_metadata_cache`, `tv_series_art`
- `movie_files`, `movie_metadata_cache`, `movie_art`, `movie_playback_progress`
- `play_history`, `scan_jobs`, `libraries`, `auth_users`, `auth_user_keys`

**Routes:**
- Resource-oriented paths: `/{media_type}/{resource}/{id}` (e.g., `/music/album/42`, `/tv/series/123`)
- Actions as suffixes: `/music/match/approve`, `/libraries/scan`, `/settings/jobs/cancel`
- Art endpoints: `/art/{type}/{id}` (e.g., `/art/album/42`, `/art/artist/7`, `/art/tv/poster/123`)
- Streaming: `/stream/track/{id}`, `/stream/tv/{id}`

## Where to Add New Code

**New Media Type (e.g., Movies matching):**
- Scanner: Create `internal/moviescan/scanner.go` following `internal/tvscan/scanner.go` pattern
- Matcher: Add to `internal/tmdb/` or create `internal/moviematch/` following `internal/tmdb/matcher.go` pattern
- Handlers: Create `internal/web/handlers_movies.go` following `internal/web/handlers_tv.go` pattern
- Templates: Add `web/templates/movies_*.html` files
- Routes: Register in `internal/web/router.go`
- Schema: Add tables in `internal/db/migrate.go` (DDL in `schemaSQL` const, or additive via `ensureColumn()`)
- Scan trigger: Add case in `librariesScan()` switch in `internal/web/handlers_settings.go`

**New API Integration:**
- Create `internal/{service}/client.go` with rate-limited HTTP client
- Follow `internal/tmdb/client.go` pattern (ticker-based rate limiter, context-aware requests)

**New Background Job Type:**
- Define executor as `func(ctx context.Context, jobID, libraryID int64) error`
- Enqueue via `h.jobs.Enqueue("job_type_name", libraryID, "user", executor)` in the appropriate handler
- Progress updates: `UPDATE scan_jobs SET progress_current=?, progress_total=? WHERE id=?`

**New HTTP Handler:**
- Add method to `*Handler` in `internal/web/handlers_{domain}.go`
- Register route in `internal/web/router.go` via `mux.HandleFunc()`
- For pages: call `h.render(w, "template_name.html", data)` with `map[string]any{}`
- For JSON APIs: set Content-Type header, encode with `json.NewEncoder(w).Encode()`

**New Template Page:**
- Create `web/templates/{name}.html` with `{{define "content"}}...{{end}}`
- Add filename to `pages` slice in `internal/web/handler.go` `New()` constructor
- Layout provides `staticv`, `humanBytes`, `mult` template functions

**New Database Table:**
- Add `CREATE TABLE IF NOT EXISTS` to `schemaSQL` const in `internal/db/migrate.go`
- Add indexes alongside the table definition
- For adding columns to existing tables, use `ensureColumn()` call in `Migrate()` function

**Utilities / Shared Helpers:**
- String utilities: `internal/match/similarity.go` (normalization, Levenshtein)
- Path safety: `internal/pathguard/pathguard.go`
- Config parsing helpers: `internal/config/config.go` (getenv, parseBoolDefaultTrue, etc.)

## Special Directories

**`data/`:**
- Purpose: Local SQLite database files during development
- Generated: Yes (created by SQLite at runtime)
- Committed: Database files are gitignored; directory may contain `.sqlite`, `.sqlite-shm`, `.sqlite-wal`

**`web/`:**
- Purpose: Frontend assets (templates + static files)
- Generated: No (hand-written)
- Committed: Yes
- Note: Copied into Docker image at `/app/web/` via `COPY web/ /app/web/` in Dockerfile. Must remain at repo root.

**`.planning/`:**
- Purpose: AI-generated planning and analysis documents
- Generated: Yes
- Committed: Varies

**`bin/`:**
- Purpose: Local build output directory
- Generated: Yes (by `go build -o ./bin/`)
- Committed: No (gitignored)

---

*Structure analysis: 2026-03-05*

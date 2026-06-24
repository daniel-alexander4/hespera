# CLAUDE.md

## Project Overview

Locally-hosted media server in Go: Music, TV, Movies with automatic metadata matching. Single Docker container, SQLite storage, server-rendered HTML templates with vanilla CSS/JS. Four direct deps: `dhowden/tag`, `modernc.org/sqlite`, `bogem/id3v2/v2`, `gcottom/audiometa/v3`.

## Build & Run Commands

```bash
# Build binaries locally
go build -o ./bin/isomedia ./cmd/isomedia
go build -o ./bin/isocli ./cmd/isocli

# Build and run with Docker
docker compose up --build

# Tests: all / one package / one test
go test ./...
go test ./internal/config
go test ./internal/config -run TestFromEnvDefaults

# Format and vet
go fmt ./...
go vet ./...
```

## Architecture

### Entry Points

- `cmd/isomedia/main.go` — Web server: config → SQLite (WAL) → migrations → Handler → HTTP server, graceful shutdown (10s timeout).
- `cmd/isocli/main.go` — CLI stub for future user/key management.

### Core Packages

| Package | Role |
|---------|------|
| `internal/config` | Config struct from env vars (ISOMEDIA_ prefix), validation |
| `internal/db` | SQLite WAL setup, connection pooling, schema migrations |
| `internal/auth` | SSH pubkey challenge-response + HMAC-SHA256 session cookies, rate limiting (10/min/IP) |
| `internal/pathguard` | Path traversal prevention (symlink resolution + containment check) |
| `internal/jobs` | Background job queue: buffered channel (128), single worker goroutine, context cancellation |
| `internal/music` | Audio tag reader (`dhowden/tag` wrapper), TrackMeta struct, compilation detection |
| `internal/scan` | Music library scanner: walk dirs, read tags, ensure artist/album/track, art extraction, prune/cleanup |
| `internal/match` | MusicBrainz matching pipeline, Cover Art Archive, artist enrichment (Wikipedia/Wikimedia), scoring |
| `internal/web` | HTTP handlers, routing (`http.ServeMux`), template rendering, logging middleware |

### Key Patterns

- **Handler DI**: `web.Handler` receives `web.Deps{Cfg, DB}`; `web.New(d)` compiles templates, starts job service, initializes auth.
- **Routing**: stdlib `http.ServeMux`, routes registered in `web.Router()`. Auth middleware wraps entire mux if enabled.
- **Templates**: `html/template` from `web/templates/` at startup. Layout base cloned per page, merged with `partials_*.html` glob. FuncMap: `staticv` (cache-bust), `humanBytes`, `mult`.
- **Database**: `modernc.org/sqlite`, WAL, 8 max open / 4 idle, 5s busy timeout, FK on. Migrations in `db.Migrate()` with `ensureColumn()` for schema evolution.
- **Jobs**: `jobs.Service.Enqueue(jobType, libraryID, createdBy, executor)`. States: queued → running → done/failed/canceled. Progress in `scan_jobs` table.
- **Scanner pattern**: `scan.New(cfg, db)` / `match.New(db, dataDir)` constructed inline per handler call, passed as executor closure to `jobs.Enqueue`.
- **Logging**: `slog` structured JSON to stdout.

### Match Pipeline

- **MBClient**: MusicBrainz API, 1 req/sec rate limiter, 3-strategy query cascade (strict release-group → loose release → artist fallback)
- **Scorer**: weighted (title 0-38, artist 0-26, MB score 0-18, type 0-10, year 0-4). Thresholds: ≥70 matched, 45-69 uncertain, <45 unmatched
- **CAAClient**: Cover Art Archive, release-group → release fallback, thumbnail size preference
- **Artist enrichment**: MusicBrainz URL relations → Wikipedia REST API (bio) → Wikidata/Wikimedia Commons (image)
- **Pipeline**: `Matcher.RunMusicMatch()` iterates albums, scores candidates, fetches art, enriches artists. Non-fatal per-album errors.

### Test Patterns

- Standard `testing` package with table-driven tests
- `openTestDB(t)` helper creates temp SQLite in `t.TempDir()`
- Direct conditionals with `t.Fatalf()`, no assertion libraries
- HTTP tests use `httptest.NewRequest` / `httptest.NewRecorder`

### Configuration (Environment Variables)

| Variable | Default | Purpose |
|----------|---------|---------|
| ISOMEDIA_LISTEN | :8080 | HTTP listen address |
| ISOMEDIA_DATA_DIR | /var/lib/isomedia | Data directory |
| ISOMEDIA_DB_PATH | {DATA_DIR}/isomedia.sqlite | Database path |
| ISOMEDIA_MEDIA_ROOT | /media | Media root directory |
| ISOMEDIA_TMDB_API_KEY | | TMDB API key |
| AUTH_ENABLED | true | Enable SSH key auth |
| AUTH_SESSION_SECRET | | HMAC secret (16+ chars) |
| ISOMEDIA_FFMPEG_CONCURRENCY | 4 | Max concurrent ffmpeg processes |

### Docker

Multi-stage: Go 1.23 builder (`CGO_ENABLED=0 -trimpath -ldflags="-s -w"`) → Ubuntu 24.04 runtime with ffmpeg/openssh-client/ca-certificates. Non-root (UID 65532). Port 8080.

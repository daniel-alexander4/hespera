# CLAUDE.md

This file provides guidance to Claude Code when working with code in this repository.

## Project Overview

isomedia is a locally-hosted media server built from scratch in Go. Music, TV, Movies with automatic metadata matching. Single Docker container, SQLite for storage, server-rendered HTML templates with vanilla CSS/JS. Three direct dependencies: `dhowden/tag`, `bogem/id3v2`, `modernc.org/sqlite`.

## Build & Run Commands

```bash
# Build binaries locally
go build -o ./bin/isomedia ./cmd/isomedia
go build -o ./bin/isocli ./cmd/isocli

# Build and run with Docker
docker compose up --build

# Run all tests
go test ./...

# Run tests for a specific package
go test ./internal/config
go test ./internal/db
go test ./internal/auth
go test ./internal/pathguard
go test ./internal/jobs

# Run a single test
go test ./internal/config -run TestFromEnvDefaults

# Format and vet
go fmt ./...
go vet ./...
```

## Architecture

### Entry Points

- `cmd/isomedia/main.go` — Web server: loads config, opens SQLite with WAL mode, runs migrations, creates Handler, starts HTTP server with graceful shutdown (10s timeout).
- `cmd/isocli/main.go` — CLI stub for future user/key management.

### Core Packages

| Package | Role |
|---------|------|
| `internal/config` | Config struct from env vars (ISOMEDIA_ prefix), validation |
| `internal/db` | SQLite WAL setup, connection pooling, schema migrations |
| `internal/auth` | SSH pubkey challenge-response + HMAC-SHA256 session cookies, CSRF protection, rate limiting |
| `internal/pathguard` | Path traversal prevention (symlink resolution + containment check) |
| `internal/jobs` | Generic background job queue with worker goroutine, cancellation support |
| `internal/web` | HTTP handlers, routing (http.ServeMux), template rendering, logging middleware |

### Key Patterns

- **Handler dependency injection**: `web.Handler` receives `web.Deps{Cfg, DB}`.
- **Routing**: stdlib `http.ServeMux`. All routes in `web.Router()`.
- **Templates**: Go `html/template` loaded from `web/templates/` at runtime. Layout base + per-page pattern.
- **Database**: Pure Go SQLite via `modernc.org/sqlite`. WAL mode, 8 max open / 4 idle, 5s busy timeout, FK on.
- **Jobs**: Generic `jobs.Service` with buffered channel queue, single worker goroutine, context-based cancellation.
- **Auth**: SSH pubkey challenge-response with HMAC-SHA256 signed session cookies. Rate limiting (10/min/IP).
- **Logging**: `slog` structured JSON logging.

### Test Patterns

- Standard `testing` package with table-driven tests
- `openTestDB(t)` helper creates temp SQLite in `t.TempDir()`
- Direct conditionals with `t.Fatalf()`, no assertion libraries

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

### Directory Layout

```
cmd/isomedia/     — Web server entry point
cmd/isocli/       — CLI stub
internal/config/  — Configuration
internal/db/      — Database setup + migrations
internal/auth/    — Authentication
internal/pathguard/ — Path security
internal/jobs/    — Background jobs
internal/web/     — HTTP handlers + routing
web/templates/    — HTML templates
web/static/       — CSS, JS, icons
```

### Docker

Multi-stage build: Go 1.23 builder → Ubuntu 24.04 runtime with ffmpeg/openssh-client/ca-certificates. Runs as non-root (UID 65532).

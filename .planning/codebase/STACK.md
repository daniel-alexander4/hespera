# Technology Stack

**Analysis Date:** 2026-03-05

## Languages

**Primary:**
- Go 1.23 - All application code (`cmd/`, `internal/`)

**Secondary:**
- HTML - Server-rendered templates (`web/templates/`)
- CSS - Single stylesheet (`web/static/app.css`)
- JavaScript - Inline in HTML templates (vanilla, no framework)

## Runtime

**Environment:**
- Go 1.23 (compiled binary, `CGO_ENABLED=0`)
- Ubuntu 24.04 (Docker runtime base image)

**Package Manager:**
- Go modules (`go.mod` / `go.sum`)
- Lockfile: `go.sum` present

## Frameworks

**Core:**
- Go stdlib `net/http` - HTTP server (no third-party framework)
- Go stdlib `html/template` - Server-side template rendering
- Go stdlib `database/sql` - Database access layer
- Go stdlib `http.ServeMux` - Routing

**Testing:**
- Go stdlib `testing` - All tests use standard testing package

**Build/Dev:**
- Go toolchain - `go build`, `go test`, `go fmt`, `go vet`
- Docker multi-stage build - Production container creation

## Key Dependencies

**Critical (4 direct dependencies):**
- `github.com/dhowden/tag` v0.0.0-20240417053706 - Audio metadata reading (MP3, FLAC, OGG, M4A); primary tag reader in `internal/music/tags.go`
- `modernc.org/sqlite` v1.34.5 - Pure-Go SQLite driver (no CGO required); used in `internal/db/db.go`
- `github.com/bogem/id3v2/v2` v2.1.4 - MP3 ID3v2 tag writing; used in `internal/music/tagwrite.go`
- `github.com/gcottom/audiometa/v3` v3.0.4 - Multi-format audio tag writing (FLAC, OGG, M4A); used in `internal/music/tagwrite.go`

**Indirect (through audiometa):**
- `github.com/gcottom/flacmeta` v0.0.6 - FLAC tag handling
- `github.com/gcottom/mp4meta` v0.0.5 - M4A/MP4 tag handling
- `github.com/gcottom/oggmeta` v0.0.8 - OGG/Opus tag handling
- `github.com/gcottom/mp3meta` v0.0.4 - MP3 tag handling
- `github.com/abema/go-mp4` v1.3.0 - MP4 container parsing

**Infrastructure:**
- `golang.org/x/text` v0.21.0 - Text encoding support (indirect)
- `golang.org/x/sys` v0.22.0 - System calls (indirect)
- `modernc.org/libc` v1.55.3 - C runtime for pure-Go SQLite (indirect)

## System Dependencies (Runtime)

**Required in container:**
- `ffmpeg` / `ffprobe` - Video stream probing (`internal/video/probe.go`); installed in Docker image
- `openssh-client` (`ssh-keygen`) - SSH signature verification for auth (`internal/auth/auth.go`); installed in Docker image
- `ca-certificates` - TLS connections to external APIs

## Configuration

**Environment Variables:**
All configuration via environment variables, parsed in `internal/config/config.go`:

| Variable | Default | Purpose |
|----------|---------|---------|
| `ISOMEDIA_LISTEN` | `:8080` | HTTP listen address |
| `ISOMEDIA_DATA_DIR` | `/var/lib/isomedia` | Data directory (thumbs, DB) |
| `ISOMEDIA_DB_PATH` | `{DATA_DIR}/isomedia.sqlite` | SQLite database path |
| `ISOMEDIA_MEDIA_ROOT` | `/media` | Media root directory |
| `ISOMEDIA_TMDB_API_KEY` | (empty) | TMDB API key for TV matching |
| `AUTH_ENABLED` | `true` | Enable SSH key auth |
| `AUTH_SESSION_SECRET` | (empty) | HMAC secret (16+ chars, required when auth enabled) |
| `SSH_AUTH_NAMESPACE` | `isomedia` | SSH signature namespace |
| `SSH_KEYGEN_PATH` | `ssh-keygen` | Path to ssh-keygen binary |
| `ISOMEDIA_FFMPEG_CONCURRENCY` | `4` | Max concurrent ffmpeg processes |
| `ISOMEDIA_FFMPEG_ACQUIRE_TIMEOUT` | `2s` | ffmpeg semaphore timeout |
| `ISOMEDIA_TV_TRANSCODED_CACHE_MAX_BYTES` | `20GB` | TV transcoded file cache limit |
| `ISOMEDIA_TV_HLS_CACHE_MAX_BYTES` | `20GB` | TV HLS cache limit |
| `ISOMEDIA_TV_CACHE_MAX_AGE` | `72h` | TV cache entry max age |

**No `.env` file present** - configuration passed via docker-compose environment block or shell environment.

**Build Configuration:**
- `Dockerfile` - Multi-stage: `golang:1.23` builder, `ubuntu:24.04` runtime
- `docker-compose.yml` - Single service, port 8080, volume mounts for data and media
- Build flags: `CGO_ENABLED=0 -trimpath -ldflags="-s -w"` (static binary, stripped)

## Database

**Engine:** SQLite via `modernc.org/sqlite` (pure Go, no CGO)
**Configuration** (set in `internal/db/db.go`):
- WAL journal mode
- 5000ms busy timeout
- Foreign keys enabled
- 8 max open connections, 4 max idle
**Schema:** Defined inline in `internal/db/migrate.go` (no migration files, single `schemaSQL` constant)
**Migrations:** `ensureColumn()` helper for additive schema changes; table recreation for constraint changes

## Platform Requirements

**Development:**
- Go 1.23+
- No CGO required (pure Go SQLite)
- `ffmpeg` / `ffprobe` on PATH (for video probing)
- `ssh-keygen` on PATH (for auth testing, optional)

**Production (Docker):**
- Docker with compose support
- Port 8080
- Volume mount for `/var/lib/isomedia` (data persistence)
- Volume mount for `/media` (media files)
- Runs as non-root user (UID 65532)
- Single container, no external services required

---

*Stack analysis: 2026-03-05*

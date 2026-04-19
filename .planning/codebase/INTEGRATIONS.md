# External Integrations

**Analysis Date:** 2026-03-05

## APIs & External Services

### Music Metadata

**MusicBrainz API:**
- Purpose: Music album/artist matching and metadata lookup
- Base URL: `https://musicbrainz.org/ws/2`
- Client: `internal/match/musicbrainz.go` (`MBClient` struct)
- Auth: None (public API)
- User-Agent: `isomedia/1.0 (https://github.com/isomedia)`
- Rate Limiting: Client-side 1 req/sec mutex-based throttle (`MBClient.throttle()`)
- Operations:
  - `SearchReleaseGroups()` - 3-strategy cascade: strict release-group, loose release, artist fallback
  - `SearchArtist()` - Artist name lookup, returns MBID
  - `LookupArtist()` - Artist detail with URL relations (`?inc=url-rels`)
- Response parsing: JSON, max 2MB body read
- Error handling: 429/503 treated as rate limit errors; non-fatal per-album in pipeline

**Cover Art Archive (CAA):**
- Purpose: Album cover art fetching
- Base URL: `https://coverartarchive.org`
- Client: `internal/match/coverart.go` (`CAAClient` struct)
- Auth: None (public API)
- Rate Limiting: None (follows MusicBrainz throttle timing)
- Operations:
  - `FetchCover()` - Release-group lookup with fallback to individual releases (max 3)
- Image selection: Prefers front cover, then largest thumbnail (Large > 500 > 250 > Small > full)
- Storage: Downloads saved to `{DATA_DIR}/thumbs/music/` with SHA1-hashed filenames
- Max image size: 15MB download limit

### Artist Enrichment

**Wikipedia REST API:**
- Purpose: Artist biography text
- URL pattern: `https://{lang}.wikipedia.org/api/rest_v1/page/summary/{title}`
- Client: `internal/match/artistmeta.go` (`fetchWikipediaSummary()`)
- Auth: None
- Data: Extracts `extract` field (plain text summary)
- Language: Determined from Wikipedia URL found in MusicBrainz relations
- Max body: 512KB

**Wikidata Entity API:**
- Purpose: Resolve Wikipedia sitelinks and P18 image claims for artists
- URL pattern: `https://www.wikidata.org/wiki/Special:EntityData/{QID}.json`
- Client: `internal/match/artistmeta.go` (`fetchWikidataEntity()`)
- Auth: None
- Operations:
  - `extractEnwikiURL()` - Extracts English Wikipedia URL from sitelinks
  - `extractP18()` - Extracts P18 (image) claim filename
- Max body: 5MB

**Wikimedia Commons:**
- Purpose: Artist photo download
- URL pattern: `https://commons.wikimedia.org/wiki/Special:FilePath/{filename}?width=500`
- Client: `internal/match/artistmeta.go` (`downloadArtistImage()`)
- Auth: None
- Storage: Downloads saved to `{DATA_DIR}/thumbs/music/` with SHA1-hashed filenames
- Max image size: 15MB

### TV/Movie Metadata

**TMDB (The Movie Database) API v3:**
- Purpose: TV series identification, metadata, and artwork
- Base URL: `https://api.themoviedb.org/3`
- Client: `internal/tmdb/client.go` (`Client` struct)
- Auth: API key via query parameter (`api_key=`); env var `ISOMEDIA_TMDB_API_KEY`
- Rate Limiting: Client-side 4 req/sec via `time.Ticker` channel
- Image CDN bases:
  - Posters: `https://image.tmdb.org/t/p/w500`
  - Backdrops: `https://image.tmdb.org/t/p/w1280`
  - Stills: `https://image.tmdb.org/t/p/w300`
- Operations:
  - `SearchTV()` - Search TV shows by name
  - `FetchTVShow()` - Full show details (seasons, genres, status)
  - `FetchTVSeason()` - Season details with episodes
  - `DownloadImage()` - Download poster/backdrop/still images
- Matcher: `internal/tmdb/matcher.go` (`Matcher` struct) orchestrates search, matching, metadata caching, and art download
- Storage: Art saved to `{DATA_DIR}/thumbs/tv/`
- Metadata cache: JSON payloads stored in `tv_series_metadata_cache` SQLite table

## Data Storage

### Database

**SQLite (embedded):**
- Driver: `modernc.org/sqlite` (pure Go)
- Connection: `internal/db/db.go` (`db.Open()`)
- DSN: `file:{path}?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)`
- Connection pool: 8 max open, 4 max idle
- Schema: `internal/db/migrate.go` - single `schemaSQL` constant + incremental `ensureColumn()` migrations
- Tables:
  - `libraries` - Media library definitions
  - `music_artists`, `music_albums`, `music_tracks` - Music catalog
  - `play_history` - Music play tracking
  - `tv_series_files`, `tv_series_identities` - TV file inventory and matching
  - `tv_series_metadata_cache` - TMDB JSON cache
  - `tv_series_art` - TV artwork paths
  - `tv_playback_progress` - TV watch progress
  - `movie_files`, `movie_metadata_cache`, `movie_art`, `movie_playback_progress` - Movie tables (schema exists, not fully implemented)
  - `scan_jobs` - Background job queue and progress
  - `auth_users`, `auth_user_keys` - User accounts and SSH public keys

### File Storage

**Local filesystem only:**
- Media files: Read from `ISOMEDIA_MEDIA_ROOT` (mounted volume, read-only access)
- Thumbnails/art: Written to `{DATA_DIR}/thumbs/music/` and `{DATA_DIR}/thumbs/tv/`
- Database: `{DATA_DIR}/isomedia.sqlite` (+ WAL/SHM files)
- Static assets: `web/static/` (bundled in container at `/app/web/static/`)

**No external file storage** (no S3, no cloud storage).

### Caching

**SQLite-based metadata cache:**
- `tv_series_metadata_cache` - TMDB show/season/episode JSON (keyed by `entity_key` + `lang`)
- `movie_metadata_cache` - Movie metadata JSON (schema exists, not yet populated)

**No Redis/Memcached** - all caching is SQLite tables or filesystem.

## Authentication & Identity

**Custom SSH public key challenge-response:**
- Implementation: `internal/auth/auth.go` (`Manager` struct)
- User store: `internal/auth/store.go` (`Store` struct) - SQLite tables `auth_users` + `auth_user_keys`
- Flow:
  1. Client requests challenge via `POST /auth/challenge`
  2. Server generates random 24-byte token, stored in-memory (not DB)
  3. Client signs challenge with SSH private key
  4. Client submits signature via `POST /auth/verify`
  5. Server verifies using `ssh-keygen -Y verify` (external process)
  6. Server issues HMAC-SHA256 signed session cookie (24h TTL)
- Session: `isomedia_session` cookie - JSON claims (username, expiry, nonce) signed with `AUTH_SESSION_SECRET`
- Pre-auth: `isomedia_preauth` cookie binds challenge to browser session
- CSRF: Origin/Referer header check on unsafe methods (POST/PUT/PATCH/DELETE)
- Rate limiting: 10 verify attempts per minute per IP (in-memory map)
- Challenge limits: Max 5 signature attempts per challenge, 10-minute expiry
- Cookie security: `HttpOnly`, `SameSite=Lax`, `Secure` when TLS detected
- Toggleable: `AUTH_ENABLED=false` disables all auth middleware

## Monitoring & Observability

**Error Tracking:**
- None (no Sentry, no external error service)

**Logs:**
- `log/slog` structured JSON logging to stdout
- Configured in `cmd/isomedia/main.go`: `slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})`
- Request logging: `internal/web/middleware.go` (`withLogging`) - method, path, status, duration

## CI/CD & Deployment

**Hosting:**
- Self-hosted Docker container (single container deployment)
- No cloud platform dependencies

**CI Pipeline:**
- None detected (no GitHub Actions, no CI config files)

**Docker Build:**
- Multi-stage: Go 1.23 builder -> Ubuntu 24.04 runtime
- Static binary (CGO_ENABLED=0, stripped)
- Non-root user (UID 65532)
- Port 8080 exposed

## External Process Dependencies

**ffprobe:**
- Used by: `internal/video/probe.go` (`video.Probe()`)
- Purpose: Extract video stream information (codec, resolution, duration)
- Invocation: `exec.CommandContext(ctx, "ffprobe", ...)` with 30s timeout
- Output: JSON parsed into `ProbeResult` struct

**ssh-keygen:**
- Used by: `internal/auth/auth.go` (`verifyWithSSHKeygen()`)
- Purpose: Verify SSH signatures during authentication
- Invocation: `exec.CommandContext(ctx, ssh-keygen, "-Y", "verify", ...)`
- Temp files: Creates temp dir for allowed_signers and signature files, cleaned up after

## Environment Configuration

**Required env vars (for full functionality):**
- `ISOMEDIA_TMDB_API_KEY` - Required for TV series matching (TMDB API)
- `AUTH_SESSION_SECRET` - Required when `AUTH_ENABLED=true` (16+ chars)

**Optional env vars (have sensible defaults):**
- `ISOMEDIA_LISTEN` (`:8080`)
- `ISOMEDIA_DATA_DIR` (`/var/lib/isomedia`)
- `ISOMEDIA_DB_PATH` (`{DATA_DIR}/isomedia.sqlite`)
- `ISOMEDIA_MEDIA_ROOT` (`/media`)
- `AUTH_ENABLED` (`true`)
- `ISOMEDIA_FFMPEG_CONCURRENCY` (`4`)
- All TV cache settings

**Secrets location:**
- Environment variables only (no secret files, no vault)
- `.env` files gitignored

## Webhooks & Callbacks

**Incoming:**
- None

**Outgoing:**
- None

## Integration Rate Limits Summary

| Service | Rate Limit | Implementation |
|---------|-----------|---------------|
| MusicBrainz | 1 req/sec | Mutex + sleep in `MBClient.throttle()` |
| TMDB | 4 req/sec | `time.Ticker` channel in `Client` |
| Cover Art Archive | No explicit limit | Piggybacks on MusicBrainz timing |
| Wikipedia/Wikidata | No explicit limit | 500ms delay between artists in pipeline |
| Wikimedia Commons | No explicit limit | Per-request only |

---

*Integration audit: 2026-03-05*

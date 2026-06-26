# Hespera — Project Plan

A locally-hosted media server built from scratch. Music, TV, Movies. Automatic metadata matching. Clean architecture.

## Core Principles

1. **Automatic metadata enrichment** — scan -> identify -> match -> write tags -> flag uncertain matches for review
2. **Clean architecture** — domain packages with clear boundaries, not a monolithic handler file
3. **Same video playback pipeline** — the decision engine + FFmpeg orchestration from the predecessor project works. Reimplement cleanly.
4. **Structured logging** — `slog` from day one
5. **Three direct Go deps** — dhowden/tag, bogem/id3v2, modernc.org/sqlite
6. **No frameworks** — stdlib http.ServeMux, html/template, vanilla JS/CSS

## Architecture

```
cmd/
  hespera/main.go          — Web server entry point
  hescli/main.go            — CLI for user/key management

internal/
  config/                   — Env-based config, validation
  db/                       — SQLite setup, WAL mode, migrations
  auth/                     — SSH pubkey challenge-response + HMAC sessions
  pathguard/                — Path traversal prevention

  scan/                     — Filesystem walker, delegates to media-specific scanners
  music/                    — Audio tag reading (dhowden/tag + ID3 fallback parser)
  tvseries/                 — TV file identification (filename regex, sidecar, probe)
  movies/                   — Movie file identification (filename parse, probe)

  match/                    — Metadata matching pipeline (runs post-scan)
    musicbrainz.go          — MB album/artist matching with scoring
    tmdb.go                 — TMDB TV/movie matching
    coverart.go             — Cover Art Archive fetching
    artistmeta.go           — Artist bio/art with fallback chain
    pipeline.go             — Orchestrator: scan result -> match -> write -> flag

  tags/                     — Tag writing (ID3, Vorbis, MP4) + repair logic
  stream/                   — FFmpeg orchestration, transcoding, cache, HLS/DASH
  jobs/                     — Background job queue + workers (generic, not per-type)

  web/                      — HTTP layer
    handler.go              — Handler struct, deps, render helper
    router.go               — All routes
    middleware.go           — Auth, logging, CSRF
    music.go                — Music browse/play handlers
    tv.go                   — TV browse/play handlers
    movies.go               — Movie browse/play handlers
    settings.go             — Settings handlers
    api.go                  — JSON API for native clients
    playback.go             — Playback session + stream routing

web/
  templates/                — HTML templates (layout + pages + partials)
  static/                   — CSS, JS, icons
```

## Phase 1: Foundation

### 1.1 Project Skeleton
- [ ] `go mod init hespera`
- [ ] cmd/hespera/main.go — load config, open DB, run migrations, create handler, start HTTP server with graceful shutdown
- [ ] internal/config — Config struct from env vars (prefixed HESPERA_)
- [ ] internal/db — SQLite WAL setup (8 max open, 4 idle, 5s busy timeout, FK on), migration runner
- [ ] internal/pathguard — ResolveExistingUnderRoot (resolve symlinks, check containment)
- [ ] Dockerfile + docker-compose.yml — multi-stage build, Ubuntu 24.04 runtime with ffmpeg/openssh-client

### 1.2 Database Schema
- [ ] Libraries table
- [ ] Music tables (artists, albums, tracks, play_history)
- [ ] TV tables (files, identities, match_candidates, metadata_cache, art, playback_progress, play_history)
- [ ] Movie tables (files, identities, metadata_cache, art, playback_progress, play_history)
- [ ] System tables (jobs, metadata_snapshots, auth)
- [ ] **New: match_status column on all media tables** — 'matched', 'uncertain', 'unmatched', 'manual'
- [ ] **New: match_confidence float on all media tables**

### 1.3 Auth System
- [ ] SSH pubkey challenge-response
- [ ] HMAC-SHA256 session cookies
- [ ] Rate limiting (10/min/IP)
- [ ] CSRF on unsafe methods
- [ ] Auth middleware

### 1.4 Web Foundation
- [ ] Template loading (layout + pages + partials), cache-busted static serving
- [ ] Render helper (buffer-first, error handling — but log+500 instead of panic)
- [ ] Structured logging middleware (slog, JSON output)
- [ ] Home page, login page, settings skeleton

## Phase 2: Music

### 2.1 Scanner
- [ ] Filesystem walker with audio extension filter
- [ ] Tag reading via dhowden/tag + manual ID3v2 fallback parser
- [ ] SHA256 checksum (skip if size/mtime unchanged)
- [ ] Artist/album/track ensure + upsert
- [ ] Compilation detection (tag, album artist, multi-artist heuristic)
- [ ] Album variant merging for compilations
- [ ] Embedded art extraction + storage
- [ ] Prune missing tracks, clean empty albums/orphaned artists

### 2.2 Automatic Metadata Matching (NEW)
- [ ] Post-scan matching pipeline:
  1. For each new/changed album: query MusicBrainz
  2. Score matches (same algorithm: title 0-38, artist 0-26, MB score 0-18, type 0-10, year 0-4)
  3. **High confidence (>=70):** auto-apply — write MBIDs, correct artist/album/year, fetch art
  4. **Medium confidence (45-69):** auto-apply MBIDs + art, but flag as 'uncertain' for review
  5. **Low confidence (<45) or no match:** flag as 'unmatched'
  6. Write results to file tags (ID3/Vorbis/MP4)
  7. Set match_status + match_confidence on album
- [ ] Artist metadata enrichment: bio + art via MusicBrainz -> Wikipedia -> Wikimedia fallback chain
- [ ] Cover art: Cover Art Archive with multi-candidate fallback
- [ ] Rate limiting: 500ms between MB queries
- [ ] **File Metadata review page**: shows all 'uncertain' and 'unmatched' albums. User can approve, reject, or manually match.

### 2.3 Music Browse & Play
- [ ] Artist list, album grid, track list pages
- [ ] Album detail page with metadata display
- [ ] Audio streaming (direct file serve with range requests)
- [ ] Music player shell (full-screen overlay, queue, seek, karaoke)
- [ ] Play history tracking

### 2.4 Manual Metadata Editing
- [ ] Per-track ID3 editing modal
- [ ] Per-album metadata apply (MusicBrainz match selection)
- [ ] Cover art selection (Cover Art Archive options)
- [ ] Metadata undo/redo (with snapshot pruning: keep last 20 per album)
- [ ] Re-match button (clear match_status, re-run matching)

### 2.5 Music Utilities
- [ ] Title normalization (smart title case, strip leading track numbers)
- [ ] Compilation tag sync (TCMP/COMPILATION writeback)
- [ ] Duplicate detection and merge

## Phase 3: TV Shows

### 3.1 Scanner
- [ ] Filesystem walker with video extension filter
- [ ] FFprobe analysis (codec, resolution, duration, streams, languages)
- [ ] Filename evidence extraction (SXE, X, air date, year, season dir patterns)
- [ ] Store stream_info_json

### 3.2 Automatic Metadata Matching (NEW)
- [ ] Resolve pipeline: embedded tags -> sidecar -> filename
- [ ] For filename matches:
  1. Search TMDB with extracted show title
  2. Score candidates
  3. **High confidence (>=0.70):** auto-apply — write identity, fetch metadata/art
  4. **Medium confidence (0.45-0.69):** apply identity, flag 'uncertain'
  5. **Low confidence (<0.45):** flag 'unmatched'
- [ ] Tag writeback: MKV (mkvpropedit), MP4 (mutagen), fallback sidecar
- [ ] TMDB metadata caching (show, season, episode detail)
- [ ] Art fetching (poster, backdrop, season poster, episode still)
- [ ] **File Metadata review page**: shows unmatched/uncertain TV files with match candidates

### 3.3 TV Browse & Play
- [ ] Series list, season view, episode list
- [ ] Episode detail (metadata, still image)
- [ ] Playback decision engine (same proven logic from predecessor)
- [ ] Direct play, direct stream, transcode
- [ ] HLS multi-rendition (1080p/720p/480p fMP4)
- [ ] DASH multi-quality
- [ ] Transcode cache with age+size eviction
- [ ] Resume position tracking
- [ ] Play history

### 3.4 TV Watch Page
- [ ] Mode buttons (Auto/Direct/Compatibility)
- [ ] Subtitle/audio track selection
- [ ] hls.js integration for non-Safari
- [ ] Keyboard shortcuts
- [ ] Progress save via sendBeacon

## Phase 4: Movies

### 4.1 Scanner
- [ ] Filesystem walker (same video extensions as TV)
- [ ] FFprobe analysis
- [ ] Filename parsing for movie title + year (strip quality tokens)

### 4.2 Automatic Metadata Matching (NEW)
- [ ] Search TMDB Movies API with title + year
- [ ] Score candidates (title similarity, year match, popularity)
- [ ] Same confidence tiers: auto-apply, uncertain, unmatched
- [ ] Fetch metadata: title, overview, runtime, genres, cast, poster, backdrop
- [ ] Tag writeback where possible
- [ ] **File Metadata review page**: shows unmatched/uncertain movies

### 4.3 Movie Browse & Play
- [ ] Movie grid (poster + title + year)
- [ ] Movie detail (metadata, cast, backdrop)
- [ ] Same playback pipeline as TV (decision engine, transcode, HLS/DASH)
- [ ] Resume position, play history

## Phase 5: Settings & Polish

### 5.1 Settings Pages
- [ ] Libraries management (add, scan, delete)
- [ ] Job status (unified view for scan/match/enrich jobs)
- [ ] File metadata review (unified across music/tv/movies — the "needs attention" page)
- [ ] Theme settings (same theme system: bg tones, palettes, fonts, sizes)
- [ ] System status
- [ ] User management

### 5.2 Unified File Metadata Review Page
This is the key improvement: a single page showing all media items that need attention.

Tabs: Music | TV | Movies

Each tab shows items where match_status IN ('uncertain', 'unmatched') with:
- Current metadata (from file tags)
- Best match candidate (if any) with confidence score
- Actions: Approve match, Reject and re-search, Manual search, Skip

Bulk actions: Approve all uncertain matches above threshold.

### 5.3 Background Jobs
- [ ] Generic job system (not per-type workers)
- [ ] Job types: scan, match, enrich
- [ ] scan triggers match automatically
- [ ] match triggers enrich for high-confidence matches
- [ ] Progress tracking, cancellation
- [ ] Job status page with filters

### 5.4 API
- [ ] JSON API for Roku/native clients
- [ ] Playback session endpoint
- [ ] Library browse endpoints
- [ ] Content negotiation (Accept header)

## Key Design Decisions

### Match Status Model
Every album, TV episode, and movie gets:
- `match_status TEXT` — 'matched' | 'uncertain' | 'unmatched' | 'manual' | 'skipped'
- `match_confidence REAL` — 0.0-1.0
- `match_source TEXT` — 'musicbrainz' | 'tmdb' | 'manual'
- `matched_at TEXT` — timestamp

This replaces the `metadata_enriched` boolean flag. It's richer and enables the review workflow.

### Automatic Pipeline
```
File discovered -> Scan (read tags, probe streams)
                -> Match (query external API, score candidates)
                -> High confidence? -> Auto-apply (write tags, fetch art/metadata)
                -> Low confidence?  -> Flag for review
                -> After apply      -> Schedule rescan to verify
```

### Unified Playback
TV and Movies share the same playback pipeline. The decision engine, FFmpeg orchestration, cache, and streaming handlers are identical — just parameterized by file_id.

### Package Boundaries
- `scan/` knows about filesystems and tags. Does NOT call external APIs.
- `match/` knows about external APIs (MusicBrainz, TMDB, CAA). Does NOT touch the filesystem.
- `tags/` knows about file formats and tag writing. Does NOT call external APIs.
- `stream/` knows about FFmpeg and caching. Does NOT touch the database.
- `web/` ties everything together via handlers. Thin layer over domain packages.
- `jobs/` is generic: accepts any `func(ctx, jobID) error` executor.

## Implementation Order

1. Foundation (config, db, auth, pathguard, docker)
2. Music scanner + browse (no matching yet — just get files into DB and playing)
3. Music player shell (audio playback, queue, karaoke)
4. Matching pipeline for music (MusicBrainz + art + enrichment)
5. File metadata review page for music
6. TV scanner + browse
7. TV playback (decision engine, FFmpeg, HLS/DASH — the critical path)
8. Matching pipeline for TV (TMDB)
9. Movie scanner + browse + playback (reuse TV pipeline)
10. Matching pipeline for movies (TMDB)
11. Unified file metadata review page
12. Settings, themes, polish

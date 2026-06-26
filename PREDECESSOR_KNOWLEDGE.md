# Predecessor Knowledge Base

Complete technical reference extracted from the predecessor codebase. This documents what works, what's fragile, and what Hespera should preserve or improve.

---

## Architecture Overview

Single Go binary, SQLite (WAL mode, modernc.org/sqlite), server-rendered HTML templates with vanilla JS/CSS. Docker: multi-stage build (Go 1.23 builder, Ubuntu 24.04 runtime with ffmpeg/openssh-client/ca-certificates). Runs as non-root (UID 65532). Three direct deps: dhowden/tag, bogem/id3v2, modernc.org/sqlite.

---

## What Works Well (Preserve)

### 1. Video Playback Pipeline

This is the crown jewel. The decision engine + FFmpeg orchestration is battle-tested.

**Decision Engine Flow:**
1. Client sends POST `/api/tv/playback-session` with file_id, client hint, subtitle/audio preferences
2. Server loads file metadata + stream_info_json from DB
3. Detects client profile from User-Agent (or explicit `client` param):
   - "roku" -> roku-generic
   - "android tv" -> android-tv-generic
   - "firefox" -> web-firefox
   - "safari" && !"chrome" -> web-safari
   - default -> web-chrome
4. Runs compatibility check against profile's supported codecs/containers/resolutions/bitrates
5. Returns one of three decisions:
   - **Direct Play**: file served as-is with range request support. No FFmpeg.
   - **Direct Stream**: FFmpeg remux only (codec copy, new container). Only when codecs are safe but container isn't.
   - **Transcode**: Full re-encode. HLS or DASH output with multi-rendition.

**Client Profiles (keep these exactly):**

| Profile | Containers | Video Codecs | Audio Codecs | Max Res | Max Bitrate |
|---------|-----------|-------------|-------------|---------|------------|
| web-chrome | mp4, webm | h264, vp9 | aac, opus, flac | 3840x2160 | 40 Mbps |
| web-safari | mp4 | h264, hevc | aac | 3840x2160 | 25 Mbps |
| web-firefox | mp4, webm | h264, vp9 | aac, opus | 3840x2160 | 40 Mbps |
| roku-generic | mp4, mkv, mov | h264, hevc | aac, ac3, eac3 | 1920x1080 | 25 Mbps |
| android-tv | mp4, mkv, webm | h264, vp9, av1, hevc | aac, ac3, eac3, opus | 3840x2160 | 20 Mbps |

**FFmpeg Concurrency Control:**
- Buffered channel as semaphore (default 4 slots)
- 2s acquire timeout -> HTTP 503 if exceeded
- Prevents system overload from concurrent transcodes

**Transcode Cache:**
- Key: `v{profileVersion}_{fileID}_{subOrdinal}_{audioOrdinal}_{mtimeUnix}.mp4`
- Profile version bump invalidates all caches
- Age-based eviction: 72h default
- Size-based eviction: 20GB transcoded, 20GB HLS (LRU)
- Lock files (.lock) prevent duplicate concurrent builds
- Stale lock detection: >15min without output -> clear
- Validity check: probe cache duration >= 90% of source duration

**HLS Generation:**
- Multi-rendition: 1080p (5000k), 720p (2800k), 480p (1400k) - filtered by source height
- fMP4 segments (not TS), 6s segment duration
- Event playlist type (progressive build)
- Primary rendition built first, waits for 3 segments (~18s) before returning master playlist
- Remaining renditions built in background goroutines
- Segment caching: 5min cache headers on init/segment files

**DASH Generation:**
- Filter complex: split video -> 3 quality levels -> scale each
- Shared audio stream across renditions
- 4s segment duration, template-based naming
- Adaptation sets: video (3 streams) + audio (1 stream)

**Direct Stream Remux:**
```bash
ffmpeg -i <input> -map 0:v:N -map 0:a:M -c:v copy -c:a copy \
  -movflags +frag_keyframe+empty_moov+default_base_moof -f mp4 pipe:1
```

**Transcode (cached):**
```bash
ffmpeg -hide_banner -loglevel error -nostdin -fflags +genpts \
  -i <input> -map 0:v:N -map 0:a:M \
  [-vf subtitles=<path>:si=<idx>] \
  -c:v libx264 -preset veryfast -crf 23 \
  -g 48 -keyint_min 48 -sc_threshold 0 -pix_fmt yuv420p \
  -c:a aac -b:a 160k -ac 2 -f mp4 <output>
```

**Transcode (live/realtime):**
- Same as cached but with fragmented MP4 flags for streaming
- `frag_duration 2000000` for 2s fragments
- Output to pipe:1, killed on client disconnect

**Stream Selection:**
- Video: longest duration non-attached-picture, then highest resolution, then default flag
- Audio: requested ordinal OR highest score = (channels * 10000) + (default * 1B) + (duration * 1000)

**Subtitle Handling:**
- Text subtitles (SRT, ASS, WebVTT): burn into video via `-vf subtitles=`
- Bitmap subtitles (PGS, DVD): blocked (cannot burn efficiently)
- External subtitle extraction: `ffmpeg -i <file> -map 0:s:N -c:s webvtt -f webvtt pipe:1`
- Sidecar subtitle files supported

**TV Watch Page (client-side):**
- Three mode buttons: Auto, Direct, Compatibility (HLS)
- Subtitle/audio track dropdowns populated from session response
- hls.js loaded from CDN for non-Safari browsers
- Resume position from server; progress saved every 10s via sendBeacon
- Keyboard shortcuts: Space (play/pause), j/l (seek), f (fullscreen), c (subtitles), m (mute)
- MediaSource duration override with known file duration (fixes HLS duration issues)

### 2. SSH Public Key Authentication

Solid challenge-response flow:
1. Client requests challenge (24-byte random, 10-min TTL, stored per IP)
2. Client signs with SSH private key
3. Server verifies via `ssh-keygen -Y verify`
4. HMAC-SHA256 signed session cookie: `base64(JSON{username, expiry, nonce}).base64(signature)`
5. 24h session TTL, 18-byte nonce prevents replay
6. Rate limiting: 10 verify attempts/minute/IP, 5 attempts per challenge
7. CSRF protection on POST/PUT/PATCH/DELETE (Origin/Referer header check)

### 3. SQLite + WAL Mode

Simple, reliable, zero-config:
- 8 max open / 4 idle connections
- 5s busy timeout
- Foreign keys enforced
- Idempotent migrations via `ensureColumn()` (checks PRAGMA table_info before ALTER)
- No destructive migrations ever

### 4. Job System

Clean pattern: buffered channel queues + single worker goroutines + DB persistence:
- scan_jobs table tracks state (queued/running/done/failed/canceled)
- Progress tracking (current/total)
- Cancellation: DB flag + context.CancelFunc map
- Per-job-type workers run sequentially (no concurrent execution within type)

### 5. Music Tag Reading

Comprehensive format support:
- Primary: dhowden/tag (ID3v2, Vorbis, MP4, FLAC, OGG, AIFF, WAV)
- Fallback: manual ID3v2 frame parser for malformed MP3s (v2.2/v2.3/v2.4)
- Handles encoding: ISO-8859-1, UTF-16 with BOM, UTF-16 BE, UTF-8
- v2.2 -> v2.3 frame ID mapping (TT2->TIT2, TP1->TPE1, etc.)
- Compilation detection: TCMP tag, album artist heuristic, multi-artist heuristic
- Filename parsing for compilation tracks: "Artist - Title" delimiter detection
- Embedded art extraction with MIME validation (max 15MB)

### 6. ID3 Tag Writing

- MP3: bogem/id3v2 library, with repair flow (strip+rewrite for malformed tags)
- FLAC/OGG/OPUS/M4A/MP4: gcottom/audiometa
- MusicBrainz ID writeback in user-defined frames
- Sanitization: UTF-8 validation, curly quote normalization, control char stripping

### 7. Path Traversal Prevention

Clean, simple: resolve symlinks, check containment within root. Prevents `/media/../etc/passwd` and symlink escapes.

---

## What's Fragile (Fix in Hespera)

### 1. Monolithic Handler File

`handlers_music.go` is 3800+ lines. `handlers_settings.go` is 1783+ lines. Functions are hard to find. The Handler struct holds everything.

**Fix:** Split by domain. Separate packages for music, tv, settings. Use interfaces for cross-cutting concerns.

### 2. Metadata Matching is Manual and Per-Album

Users must click through each album to match with MusicBrainz. The "enrich" job was bolted on later. There's no automatic matching on scan.

**Fix:** Automatic matching pipeline that runs during/after scan. Confidence-based: high confidence -> auto-apply, low confidence -> flag for review. Same for TV (TMDB) and movies.

### 3. No Movie Support

Movies are listed in the nav but stub handlers. No metadata matching, no TMDB/IMDB integration for movies.

**Fix:** First-class movie support with automatic TMDB matching.

### 4. Scanner Doesn't Write Back Metadata

The scanner reads tags but never writes corrections. Metadata enrichment is a separate manual step.

**Fix:** Post-scan enrichment pipeline: scan -> match -> write tags -> flag for review if uncertain.

### 5. Smart Playlist Cache Has No Eviction

In-memory map grows unbounded. No TTL. Only refreshes on scan completion, not on manual edits.

**Fix:** TTL-based cache with bounded size. Or just regenerate on request with short-lived cache.

### 6. Metadata Undo Snapshots Grow Forever

`metadata_apply_operations` and `metadata_apply_snapshots` tables have no cleanup.

**Fix:** Retain last N operations per album (e.g., 20). Prune on new snapshot.

### 7. Template Loading Panics on Parse Failure

`template.Must()` panics at startup if any template has a syntax error. No graceful degradation.

**Fix:** Log error and skip broken templates. Return 500 for pages with broken templates.

### 8. No Structured Logging

Plain `log.Printf()` everywhere. No levels, no JSON, hard to grep/aggregate.

**Fix:** Use `slog` (Go stdlib since 1.21). Structured JSON logging with levels.

### 9. TV File Identification Fragility

Filename regex patterns are good but the resolve pipeline (tags -> sidecar -> filename) has no persistence of confidence. Files matched by filename alone with low confidence are treated the same as high-confidence matches.

**Fix:** Store match confidence and method in DB. Surface "needs review" items in UI. Allow bulk approval.

### 10. Integration Provider System is Over-Engineered

The `integration_providers` table stores credentials for 6 providers but only TMDB and LRCLIB are actually used. The test-stream endpoint and generic provider model add complexity without value.

**Fix:** Just use env vars for API keys. Only build what's needed.

### 11. No Health Checks for External Dependencies

Server starts without checking if ffmpeg, ssh-keygen, or media directories exist. Fails at runtime.

**Fix:** Startup validation: check all required binaries and paths exist before starting HTTP server.

### 12. Lyrics Worker Memory Leak Risk

`lyricsScanCancels` map entries can leak if scan errors prevent cleanup.

**Fix:** Use context-based lifecycle instead of manual cancel map.

### 13. Album/Artist Relationship Complexity

The dual artist_id/album_artist_id on albums with separate artist_musicbrainz_id creates a confusing data model. Compilation handling is spread across scanner, handlers, and enrichment.

**Fix:** Cleaner data model: album has one album_artist_id. Track has its own artist_id. Compilation flag on album only. Album artist for compilations is "Various Artists" by convention.

---

## Music Pipeline Details

### Scan Flow
1. Walk filesystem, filter by extension (.mp3, .flac, .m4a, .mp4, .ogg, .opus, .wav, .aac)
2. Read tags via music.ReadTrackMeta(path)
3. Compute SHA256 checksum (skip if size/mtime unchanged)
4. Ensure artist record (library_id, name unique)
5. Detect compilation (tag, album artist, multi-artist heuristic)
6. Ensure album record (library_id, artist_id, title, year unique)
7. Merge album variants for compilations (same title/year, different artists)
8. Upsert track record
9. Save embedded art if album lacks art
10. Prune missing tracks (files no longer on disk)
11. Clean up empty albums and orphaned artists

### MusicBrainz Matching
- Cascading query strategy: strict (releasegroup + artist), then loose (release + artist), then fallback (all releases by artist, filter locally)
- Scoring: album title match (0-38), artist match (0-26), MB score (0-18), release type (0-10), year (0-4)
- Release type penalties: single (-8), broadcast (-6), live/remix/compilation (-6 each)
- Threshold: 45 confidence minimum
- Rate limiting: MB client built-in + 500ms between albums in enrichment

### Artist Metadata Fallbacks
Order: MusicBrainz -> Last.fm -> Wikipedia -> Wikimedia
First provider with bio wins. First with image wins. Returns early if both found.

### Cover Art Archive
Fallback sequence for release-group: front cover -> best available -> linked release front -> linked release best
Prefers larger thumbnails: Large > 500px > 250px > Small

### Tag Field Resolution
- Artist: tag Artist() || "Unknown Artist"
- AlbumArtist: ALBUMARTIST/ALBUM ARTIST/TPE2/aART || fallback to Artist
- Album: tag Album() || "Unknown Album"
- Title: tag Title() || filename parse ("Artist - Title") || filename stem || "Unknown Title"
- Track/Disc: tag Track()/Disc() || raw TRCK/TRACKNUMBER || parse "1/10" format
- Year: tag Year() || TDRC/TYER (first 4 chars)

### Metadata Undo System
Before any metadata write: snapshot all affected tracks (old artist, album, title, year, track_no, disc_no, abs_path).
Undo: restore DB records + rewrite ID3 tags to old values + schedule rescan.

---

## TV Pipeline Details

### File Identification
Filename regexes:
- SXE: `S(\d{1,2})((?:E\d{1,3}){1,8})\b` (S01E01, S02E01E02)
- X: `(\d{1,2})x(\d{1,3})\b` (1x01)
- Air date: `(19|20)\d{2}-\d{2}-\d{2}`
- Year: `(19|20)\d{2}`
- Season dir: `^season\s*\d{1,2}$`

Quality tokens stripped: 2160p, 1080p, 720p, 480p, x264, x265, web-dl, bluray, remux, proper, repack

Confidence scores:
- 0.72: show title + season/episode
- 0.55: season/episode only
- 0.45: show title + air date
- 0.30: show title only
- 0.15: weak evidence

### Resolve Pipeline (priority order)
1. Embedded tags (tv.series.provider, tv.series.id, tv.season, tv.episode) -> confidence 1.0
2. Sidecar .tvseries.json -> confidence 1.0
3. Filename evidence -> variable confidence, needs_fix=true

### FFprobe Analysis
```bash
ffprobe -v quiet -print_format json -show_format -show_streams <file>
```
Extracts: duration, codec per stream, resolution, channels, language, disposition (default/forced/attached_pic)

### Tag Writeback
- MKV: mkvpropedit with XML tags (tv.series.provider, tv.series.id, tv.order, tv.season, tv.episode, plus TVDB/IMDB/TMDB IDs)
- MP4: Python 3 + mutagen, iTunes free-form atoms (----:com.apple.iTunes:tag_name)
- Fallback: .tvseries.json sidecar file

### TMDB Integration
- SearchTMDBTVEpisodes(): query + season + episodes -> matching episode metadata
- SearchTMDBTVShows(): query -> show candidates
- FetchTMDBTVShow(): show_id -> full metadata (poster, backdrop, seasons)
- FetchTMDBTVSeason(): show_id + season -> episodes list
- FetchTMDBTVEpisode(): show_id + season + episode -> episode detail

---

## Database Schema (Key Tables)

### Music
```sql
libraries (id, name, type, root_path, created_at)
music_artists (id, library_id, name, art_path, musicbrainz_id, bio, bio_source_name, bio_source_url)
music_albums (id, library_id, artist_id, album_artist_id, artist_musicbrainz_id, is_compilation, title, year, art_path, musicbrainz_id, metadata_enriched)
music_tracks (id, library_id, artist_id, album_id, title, track_no, disc_no, abs_path, mime_type, file_size_bytes, mtime_unix, checksum_sha256)
play_history (id, track_id, library_id, artist_id, album_id, played_ms, completed, source, created_at)
```

### TV
```sql
tv_series_files (id, library_id, abs_path, container, file_size_bytes, mtime_unix, stream_info_json)
tv_series_identities (file_id PK, status, provider, series_id, season_number, episode_numbers_csv, match_confidence, match_method)
tv_series_match_candidates (id, file_id, guessed_show_title, guessed_season_number, guessed_episode_numbers_csv, confidence, reason)
tv_series_metadata_cache (entity_key PK, lang, payload_json, fetched_at)
tv_series_art (id, art_type, tmdb_series_id, season_number, episode_number, art_path)
tv_playback_progress (file_id PK, position_seconds, duration_seconds, completed)
tv_play_history (id, file_id, series_id, season_number, episode_numbers_csv, created_at)
```

### System
```sql
scan_jobs (id, library_id, job_type, status, progress_current, progress_total, payload_json, created_by, duration_ms, cancel_requested, error, started_at, ended_at)
metadata_apply_operations (id, operation_type, album_id, track_id, created_at, undone_at)
metadata_apply_snapshots (id, operation_id, track_id, library_id, old_artist_name, old_album_title, old_album_year, old_track_title, old_track_no, old_disc_no, abs_path)
auth_users (id, username UNIQUE)
auth_user_keys (id, user_id FK, public_key, UNIQUE(user_id, public_key))
```

---

## Frontend Architecture

### CSS Theming
- 6 background tones (midnight/graphite/slate/dusk/mist/paper) via data-bg-tone
- 10 color palettes (emerald/ocean/amber/rose/violet/teal/crimson/copper/indigo/forest) via data-palette
- 8 font themes via data-font-theme
- 4 font sizes (compact/normal/large/xlarge) via data-font-size
- All stored in localStorage, applied before first paint via inline script

### Music Player Shell
- Full-screen fixed overlay (#player-shell, z-index 1000)
- Grid layout: 280px cover + controls
- Queue management: tracks[], queue[], currentPos
- Karaoke: fetches synced lyrics ([MM:SS.ms] format), updates display in sync
- Play history: POSTs to /music/play-event, sendBeacon on unload
- Browser autoplay handling: pendingAutoplay flag, click-based resumption
- History API: pushState to #player, popstate to close

### Key Patterns
- Data attribute selectors: data-music-tab, data-carousel, data-menu, data-section-toggle
- ARIA accessibility: role=tab, aria-selected, aria-expanded, aria-pressed
- No framework: pure DOM manipulation, event delegation
- Icon system: Lucide SVGs with text fallbacks
- Cache busting: staticv template func appends ?v={mtime_unix}

---

## Configuration (Environment Variables)

| Variable | Default | Purpose |
|----------|---------|---------|
| HESPERA_LISTEN | :8080 | HTTP listen address |
| HESPERA_DATA_DIR | /var/lib/hespera | SQLite DB + cached artwork |
| HESPERA_DB_PATH | {DATA_DIR}/hespera.sqlite | Database path |
| HESPERA_MEDIA_ROOT | /media | Bind-mounted media directory |
| HESPERA_TMDB_API_KEY | | TMDB API key for TV/movie metadata |
| AUTH_ENABLED | true | Enable SSH key auth |
| AUTH_SESSION_SECRET | | HMAC secret (16+ chars, rejects weak values) |
| SSH_AUTH_NAMESPACE | hespera | SSH signature namespace |
| SSH_KEYGEN_PATH | ssh-keygen | Path to ssh-keygen binary |
| HESPERA_FFMPEG_CONCURRENCY | 4 | Max concurrent FFmpeg processes |
| HESPERA_FFMPEG_ACQUIRE_TIMEOUT | 2s | Timeout to acquire FFmpeg slot |
| HESPERA_TV_TRANSCODED_CACHE_MAX_BYTES | 20GB | Transcoded cache size limit |
| HESPERA_TV_HLS_CACHE_MAX_BYTES | 20GB | HLS cache size limit |
| HESPERA_TV_CACHE_MAX_AGE | 72h | Cache entry max age |

---

## Docker Setup

```dockerfile
# Stage 1: Build
FROM golang:1.23 AS builder
COPY go.mod go.sum .
RUN go mod download  # with retry logic
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bin/hespera ./cmd/hespera
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bin/hescli ./cmd/hescli

# Stage 2: Runtime
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y ca-certificates openssh-client ffmpeg
COPY --from=builder /bin/hespera /bin/hescli /usr/local/bin/
COPY web/ /app/web/
USER 65532
EXPOSE 8080
```

docker-compose: bind-mount /media and ./data, configurable UID/GID, restart unless-stopped.

# CLAUDE.md

## Project Overview

Locally-hosted media server in Go: Music, TV, Movies with automatic metadata matching. Single Docker container, SQLite storage, server-rendered HTML templates with vanilla CSS/JS. Four direct deps: `dhowden/tag`, `modernc.org/sqlite`, `bogem/id3v2/v2`, `gcottom/audiometa/v3`.

## Build & Run Commands

```bash
# Build binaries locally
go build -o ./bin/hespera ./cmd/hespera
go build -o ./bin/hescli ./cmd/hescli

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

> **Before any work that touches media data** (scanning, matching, pruning,
> playback state, artwork, or any query/handler that reads or writes a media
> row), consult [`docs/media-source-of-truth.md`](docs/media-source-of-truth.md)
> — the canonical reference for what owns each piece of data per media type,
> how files map to DB rows, and what survives vs. is lost across
> rescan/rematch/move/delete. Keep it in sync when that behavior changes.

### Entry Points

- `cmd/hespera/main.go` — Web server: config → SQLite (WAL) → migrations → Handler → HTTP server, graceful shutdown (10s timeout).
- `cmd/hescli/main.go` — CLI stub for future user/key management.

### Core Packages

| Package | Role |
|---------|------|
| `internal/config` | Config struct from env vars (HESPERA_ prefix), validation |
| `internal/db` | SQLite WAL setup, connection pooling, schema migrations |
| `internal/auth` | SSH pubkey challenge-response + HMAC-SHA256 session cookies, rate limiting (10/min/IP) |
| `internal/pathguard` | Path traversal prevention (symlink resolution + containment check) |
| `internal/jobs` | Background job queue: buffered channel (128), single worker goroutine, context cancellation |
| `internal/music` | Audio tag reader (`dhowden/tag` wrapper), TrackMeta struct, compilation detection. **Two reject-fallbacks** for when `dhowden/tag` aborts the whole parse on a malformed frame: (1) MP3 — a tolerant hand-rolled ID3v2 parser (`readTrackMetaMP3Fallback`) recovers text frames **and** embedded cover art (`APIC`/`PIC`, front-cover preferred), pure-Go, inside `ReadTrackMeta`; (2) non-MP3 (FLAC/M4A/OGG/…) — `TrackMetaFromTags` maps an ffprobe-recovered tag dictionary through the same compilation/album-artist/normalization rules (the scanner drives it via `video.ProbeTags`; cover attached separately via `SetArt`). Without (2) a single bad tag would drop the **whole track**, not just its art |
| `internal/scan` | Music library scanner: walk dirs, read tags, ensure artist/album/track, art extraction, move-relink/prune/cleanup. When `music.ReadTrackMeta` fails outright on a non-MP3, `recoverTrackMeta` falls back to `video.ProbeTags`/`ExtractCoverArt` (ffprobe/ffmpeg, gated) so the track is still scanned — spawns a process only on that failure path, never the happy path |
| `internal/match` | MusicBrainz matching pipeline, Cover Art Archive, artist enrichment (Wikipedia/Wikimedia), scoring |
| `internal/tmdb` | TMDB client + movie/TV matcher; resolves date-based episodes against episode air dates post-match (`airdate.go`) |
| `internal/tvscan` | TV file identification (SxE / N×M / folder-authoritative title / air-date) + scanner (writes `stream_info_json` = marshaled `video.ProbeResult`) |
| `internal/video` | ffprobe wrapper + gated ffmpeg execution (`StreamFFmpeg`, `EnsureHLS`), concurrency caps, HLS cache. Also `ProbeTags`/`ExtractCoverArt` (gated) — the audio reject-fallback used by `internal/scan` to recover tags + embedded cover when the pure-Go reader rejects a non-MP3 file |
| `internal/playback` | Pure TV playback-decision layer: per-client container↔codec matrix → direct-play / remux / transcode |
| `internal/web` | HTTP handlers, routing (`http.ServeMux`), template rendering, logging middleware, TV streaming endpoints |

### Key Patterns

- **Handler DI**: `web.Handler` receives `web.Deps{Cfg, DB}`; `web.New(d)` compiles templates, starts job service, initializes auth.
- **Routing**: stdlib `http.ServeMux`, routes registered in `web.Router()`. Auth middleware wraps entire mux if enabled.
- **Templates**: `html/template` from `web/templates/` at startup. Layout base cloned per page, merged with `partials_*.html` glob. FuncMap: `staticv` (cache-bust), `humanBytes`, `mult`.
- **Theming**: Catppuccin via CSS custom properties in `app.css` — `:root` = Mocha (dark, default), `html[data-mode="light"]` = Latte. A pre-paint script in `layout.html` sets `data-mode` from `localStorage.iso_theme_mode` (first visit follows `prefers-color-scheme`, fallback dark); the `.theme-toggle` sun/moon button in the topbar flips and persists it. Hidden in couch mode (it lives in `.topbar`, which `tv.css` hides).
- **Runtime settings**: `app_settings(key, value)` KV table holds user-set overrides of env config (Settings → API Keys page, `settingsAPIKeys`). Today: `tmdb_api_key`, `fanarttv_api_key`, `audiodb_api_key`. `Handler.effective{TMDB,Fanart,AudioDB}Key(ctx)` are the single source of truth — DB value if set, else the `cfg` env default; read per-call so a UI change takes effect without a restart. Each key has its **own form** (POST dispatches on the present field) so saving one never wipes the others. Keys stored plaintext (same risk as `.env`), masked in the UI, never logged.
- **Database**: `modernc.org/sqlite`, WAL, 8 max open / 4 idle, 5s busy timeout, FK on. Migrations in `db.Migrate()` with `ensureColumn()` for schema evolution.
- **Jobs**: `jobs.Service.Enqueue(jobType, libraryID, createdBy, executor)`. States: queued → running → done/failed/canceled. Progress in `scan_jobs` table.
- **Scanner pattern**: `scan.New(cfg, db)` / `match.New(db, dataDir)` constructed inline per handler call, passed as executor closure to `jobs.Enqueue`.
- **Move-relink**: identity is path-keyed (`UNIQUE(library_id, abs_path)`), so a moved/renamed file would otherwise prune-and-recreate, dropping per-file state the *file* doesn't carry. Each scanner runs a relink pass *before* prune that pairs an orphan (path now missing) with a single surviving row sharing a content signature and transfers the irreplaceable state, then lets prune delete the orphan. Signature: music = `(file_size_bytes, checksum_sha256)` (already stored; empty checksum never matches) → re-points `play_history` + `lyrics_cache` (`relinkMovedTracks`); TV = `(file_size_bytes, mtime_unix)` which a plain `mv` preserves, so no multi-GB hashing → copies the `tv_series_identities` match (only `matched`/`skipped`; `unmatched` re-derives from the new filename) + `tv_playback_progress` (`relinkMovedFiles`/`transferFileState`). Strictly 1:1 (ambiguous signature → no transfer), so duplicate-content files are never mis-linked; an mtime-rewriting move (cp/sync tools) falls back to prune-and-recreate. Movies have no scanner yet — build the future one move-aware on the same pattern.
- **Logging**: `slog` structured JSON to stdout.

### Match Pipeline

- **MBClient**: MusicBrainz API, 1 req/sec rate limiter, 3-strategy query cascade (strict release-group → loose release → artist fallback). Strategy A fetches 25 candidates so the canonical studio release-group isn't crowded out by compilations/EPs of the same title.
- **Scorer**: weighted (title 0-38, artist 0-26, MB score 0-18, type 0-10, year 0-4; max ~96). Single threshold `matchThreshold` (=80): score ≥80 matched, else unmatched. The former "uncertain" tier was retired (migrated to unmatched in `db.Migrate`). `typeBonus` penalizes EP/Single and any non-primary edition — including **secondary types** (Live/Compilation/Remix/Demo on a primary=Album RG) — so a clean studio album outranks art-less alt editions of the same title and the matcher picks the release-group that actually has Cover Art Archive art.
- **CAAClient**: Cover Art Archive, release-group → release fallback, thumbnail size preference. Multi-candidate art search: if the matched release-group has no front cover, `fetchAlbumArt` (`pipeline.go`) probes sibling above-threshold candidates — gated to **same-artist, clean primary=Album, within 8 score points** (so only a same-album edition's cover is reused, never a live/compilation/different-album), RG-level only, capped at 3.
- **Manual art override**: `POST /music/album/art` (form on the album edit page) uploads a cover image when CAA has none or the album mis-matched. Validates server-derived MIME (jpeg/png/webp only), caps 15 MiB (`MaxBytesReader`), writes an album-id-keyed file under `thumbs/music` (temp+rename) and sets `art_path` unconditionally. The scanner/matcher art writers are empty-only-guarded, so manual art survives rescan/rematch. Served via `/art/album/{id}` with `X-Content-Type-Options: nosniff`. Upload-only (no fetch-by-URL — SSRF).
- **Artist enrichment**: MusicBrainz URL relations → Wikipedia REST API (bio) → Wikidata/Wikimedia Commons (image). Optional MBID-keyed backfill **only where those leave a gap**: fanart.tv (artist image, `fanart.go`) then TheAudioDB (bio + image, `audiodb.go`). Both opt-in via user-supplied keys (own per-host limiters, nil/no-op without a key), keyed by artist MBID so they stay correct even when album release-group MBIDs mis-match. Empty-only writes preserved. Auto-resolution (`SearchArtist`) blindly takes the top name-search hit, which picks the wrong artist when several share a name (e.g. the 1897 country "Jimmie Rodgers" over the 1933 pop singer) — the **manual artist disambiguation control** (below) is the user-facing fix.
- **Manual artist disambiguation** (`handlers_music_artist.go`, "Fix artist match" on the artist page): `GET /music/artist/disambiguate?id=N` renders MB `SearchArtistCandidates` (up to 10, with disambiguation/type/country/life-span/score, current one flagged); `POST` validates the chosen MBID against a UUID pattern, sets `music_artists.musicbrainz_id`, clears bio/bio_source/art_path, then **re-enriches synchronously** (`Matcher.ReEnrichArtist` → `EnrichArtist` for the chosen MBID) so the corrected bio/image show immediately (non-fatal on network error — next Match fills the gap). Touches only `music_artists`; per-album `artist_musicbrainz_id` (resolved per release-group, individually correct) is left untouched.
- **Pipeline**: `Matcher.RunMusicMatch()` = enrich artists → match albums → **re-fetch missing art**. The matcher only processes `''`/`unmatched` albums, so a third pass (`refetchMissingArt`) re-runs *only* the cover-art step for `matched` albums still lacking `art_path` — anchored to the album's **stored** MusicBrainz identity (re-search supplies candidate breadth only), gated by an `art_checked_at` TTL (30d) so genuinely art-less albums aren't re-probed every run, and race-guarded on `musicbrainz_id`. This is why a rebuild's matching improvements show up on the next Match. Non-fatal per-album errors.
- **Manual album controls** (album edit page): `POST /music/album/art` (upload cover), `/music/album/art/clear` (clear `art_path` + `art_checked_at` → re-fetched next Match), `/music/album/unmatch` (full reset: identity + art → re-matched next Match), `/music/album/rescan` (re-read the album's files from disk via `scan.ScanFiles` — synchronous, re-extracts embedded art under the empty-only guard; same path the tag-edit POST uses). All POST-only under `/music/` (auth + same-origin CSRF).

### TV Streaming

- **Decision** (`internal/playback`): pure, per-client. `Decide()` validates the container↔codec *combination* (not independent codec sets) and returns direct-play / direct-stream (remux) / transcode, failing safe toward transcode on any uncertainty. Text subtitles deliver as a WebVTT sidecar; only bitmap subs force burn-in. Profiles for chrome/firefox/safari with UA inference.
- **Execution** (`internal/video`): `StreamFFmpeg` (gated, kill-on-disconnect) for remux/subtitles; `EnsureHLS` builds a single-rendition **progressive** HLS asset — an *event* playlist that returns as soon as the first segment exists (playback starts in ~sub-second, not after the whole transcode), with the single continuous encode appending segments in the background. Per-key in-flight registry (one ffmpeg per source; concurrent callers share it); failed-before-playable builds leave no dir so the next request retries. Forced keyframes every `hls_time` so segments are actually 6s. `PruneCache` bounds the cache under `DataDir/cache/tv-hls` (in-flight dirs protected by the grace window). Limitation: seeking past the encoded point waits for the encoder; true segment-on-demand is a tracked future upgrade.
- **Endpoints** (`internal/web`): `GET /tv/playback-session` returns the decision + source URL + track lists; `/stream/tv/` (direct), `/stream/tv-remux/`, `/stream/tv-hls/` (manifest+segments), `/stream/tv-subtitles/` (WebVTT). HLS asset names are regex-whitelisted; all paths go through `pathguard`.
- **Art** (`tvArt`, `/art/tv/`): serves `poster`/`backdrop`/`season` images from `tv_series_art`. Season cards are the single source of truth via a cascade — own `season_poster` → series poster → `302` to `/static/tv-poster-placeholder.webp` — so the template always emits the `<img>` (no per-card gate). Many shows lack per-season posters on TMDB (empty `poster_path`), so the series-poster fallback is the common path.
- **Client**: `tv_player.html` calls the session, then plays via `<video>` (direct/remux/native-HLS) or vendored hls.js (`web/static/hls.light.min.js`, Apache-2.0). Single-rendition by design — HLS is for seeking, not adaptive bitrate (a ladder is a future enhancement).
- **Missing seasons/episodes**: `tvSeriesDetail`/`tvSeasonDetail` diff the cached TMDB metadata against present (matched) files and grey out the gaps — pure `missingSeasons`/`missingEpisodes` helpers, no new endpoints/queries/fetches. Specials (season 0) excluded. Reflects last-match-time cache (no live refetch).

### Lyrics / Karaoke

- Synced lyrics via **LRCLIB** (free, no key). `POST /music/lyrics/fetch` (`handlers_music_lyrics.go`) is cache-first against the `lyrics_cache` table, else fetches (exact `get` then fuzzy `search` scored by `pickBestLrcLibCandidate`) and caches the result — **hits and misses both cached**, so a track is fetched at most once. Lazy per-track (no scan job), always on (no integrations gate, consistent with MB/CAA/Wikipedia). `player.html` parses the synced LRC and advances current/next line on `timeupdate`. (No duration tiebreaker — match precision caveat tracked in pending.)

### Test Patterns

- Standard `testing` package with table-driven tests
- `openTestDB(t)` helper creates temp SQLite in `t.TempDir()`
- Direct conditionals with `t.Fatalf()`, no assertion libraries
- HTTP tests use `httptest.NewRequest` / `httptest.NewRecorder`

### Configuration (Environment Variables)

| Variable | Default | Purpose |
|----------|---------|---------|
| HESPERA_LISTEN | :8080 | HTTP listen address |
| HESPERA_DATA_DIR | /var/lib/hespera | Data directory |
| HESPERA_DB_PATH | {DATA_DIR}/hespera.sqlite | Database path |
| HESPERA_MEDIA_ROOT | /media | Media root directory |
| HESPERA_TMDB_API_KEY | | TMDB API key (bootstrap default; a runtime value set in Settings → API Keys overrides it) |
| HESPERA_FANARTTV_API_KEY | | Optional fanart.tv key — artist-image backfill (Settings override) |
| HESPERA_THEAUDIODB_API_KEY | | Optional TheAudioDB key — artist bio/image backfill (Settings override) |
| AUTH_ENABLED | true | Enable SSH key auth |
| AUTH_SESSION_SECRET | | HMAC secret (16+ chars) |
| HESPERA_FFMPEG_CONCURRENCY | 4 | Max concurrent ffmpeg/ffprobe processes (background HLS builds get half, min 1) |
| HESPERA_FFMPEG_ACQUIRE_TIMEOUT | 2s | How long foreground ffmpeg work waits for a slot |
| HESPERA_TV_HLS_CACHE_MAX_BYTES | 20GiB | HLS cache size budget (`DataDir/cache/tv-hls`) |
| HESPERA_TV_CACHE_MAX_AGE | 72h | HLS cache entry max age |

### Docker

Multi-stage: Go 1.23 builder (`CGO_ENABLED=0 -trimpath -ldflags="-s -w"`) → Ubuntu 24.04 runtime with ffmpeg/openssh-client/ca-certificates. Non-root (UID 65532). Port 8080.

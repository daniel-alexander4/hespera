# Technology Stack

**Project:** isomedia v1.3 Manual Controls
**Researched:** 2026-03-07

## Verdict: No New Dependencies Required

All three v1.3 features can be built using the existing Go stdlib and current dependency set. This is the correct approach given the project's explicit constraint of "4 direct dependencies -- no new dependencies unless essential."

## Feature-by-Feature Stack Analysis

### 1. Manual Artwork Upload

**What exists:**
- Image download + storage: `match/coverart.go` saves images to `{DataDir}/thumbs/music/` with sha1-hashed filenames, handles jpg/png/webp extensions
- Image serving: `albumArt` handler reads `art_path` from DB, serves via `pathguard.ResolveExistingUnderRoot`, MIME detection via `artMIMEFromExt`
- Album DB column: `music_albums.art_path TEXT NOT NULL DEFAULT ''`
- Artist DB column: `music_artists.art_path TEXT NOT NULL DEFAULT ''`

**What to use (all stdlib):**

| Capability | Implementation | Why |
|------------|---------------|-----|
| Multipart upload parsing | `r.ParseMultipartForm(maxMemory)` / `r.FormFile("file")` | Go stdlib, already available, no dependency |
| File size limit | `http.MaxBytesReader(w, r.Body, maxBytes)` | Stdlib, prevents OOM from oversized uploads |
| Content-type validation | `net/http.DetectContentType(buf[:512])` | Stdlib MIME sniffing, checks actual bytes not just extension |
| Image storage | `os.WriteFile` to `{DataDir}/thumbs/music/{hash}.{ext}` | Follow existing coverart.go sha1 hash pattern |
| DB update | `UPDATE music_albums SET art_path=? WHERE id=?` | Existing pattern |

**Configuration:**
- Max upload size: 15 MB (matches existing `io.LimitReader(resp.Body, 15<<20)` in CAA download)
- Accepted types: `image/jpeg`, `image/png`, `image/webp` (matches existing `artMIMEFromExt`)
- No image resizing/transcoding needed -- browsers handle display scaling, and 15 MB cap keeps storage reasonable

**Why NOT add an image processing library:**
- No resizing required -- album art is displayed at fixed CSS dimensions
- No format conversion required -- all three input formats are web-native
- Adding `disintegration/imaging` or `nfnt/resize` would break the 4-dependency constraint for zero functional benefit
- If thumbnailing becomes needed later, ffmpeg is already in the Docker image and can resize images

### 2. Track Number Edit Fix

**The bug:** The `musicAlbumEditPOST` handler reads `mode := r.FormValue("mode")` but the non-writeback form template (`music_album_edit.html`) never includes a `mode` hidden field. The template renders `track_no_{{.ID}}` and `track_title_{{.ID}}` input fields but the handler never reads these per-track form values in the non-writeback code path. The handler's non-writeback path falls through to the same tag-writing logic that requires `mode=all` or `mode=single`, which silently does the wrong thing.

**Stack impact:** None. This is purely a handler code fix. Two approaches:

1. **Fix the handler:** Read per-track `track_no_{{.ID}}` and `track_title_{{.ID}}` form values in the non-writeback POST path, update DB directly (which is what the non-writeback mode should do -- DB-only edits, no file tag writes)
2. **Fix the template:** Add `<input type="hidden" name="mode" value="all" />` and wire the track number fields to the existing single-track editing flow

Approach 1 is correct because the non-writeback mode exists specifically for DB-only edits. The handler should read the per-track fields and update the DB.

**No new dependencies. No new libraries. Pure logic fix.**

### 3. Manual Match Selection UI

**Music (MusicBrainz candidates):**

| Component | Exists Already | What to Add |
|-----------|---------------|-------------|
| MusicBrainz search | `MBClient.SearchReleaseGroups(ctx, artist, album)` returns `[]Candidate` | Nothing -- API already works |
| Scoring | `ScoreCandidate(c, localTitle, localArtist, localYear)` returns float64 | Nothing -- scoring already works |
| Cover art fetch | `CAAClient.FetchCover(ctx, releaseGroupID, releaseIDs)` | Nothing |
| Artist enrichment | `EnrichArtist(ctx, mb, mbid, dataDir)` | Nothing |
| Tag writeback | `writebackAlbumTracks(ctx, db, albumID)` | Nothing |
| Search endpoint | Does not exist for music | New handler: `musicMatchSearch` -- mirrors existing `tvMatchSearch` pattern |
| Candidate display | Does not exist | New template JS -- mirrors existing TV match review dropdown pattern |
| Accept candidate | Does not exist | New handler: `musicMatchAccept` -- takes albumID + candidate data, applies match |

The TV match review already has the exact UI pattern needed: search input with debounced fetch, dropdown with results, approve button. The music version follows the same pattern but queries MusicBrainz instead of TMDB.

**TV (TMDB candidates):**
Already fully implemented. The `tv_match_review.html` template has search + approve + skip UI. The `tvMatchSearch` endpoint proxies TMDB search. The `tvMatchApprove` endpoint accepts a manual TMDB ID. No stack changes needed.

## Recommended Stack (unchanged)

### Core Framework
| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| Go | 1.23 | Application language | Existing, validated |
| `net/http` (stdlib) | 1.23 | HTTP server, routing, multipart parsing | Existing pattern, sufficient for file upload |
| `html/template` (stdlib) | 1.23 | Server-rendered UI | Existing pattern |
| `database/sql` (stdlib) | 1.23 | Database access | Existing pattern |

### Database
| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `modernc.org/sqlite` | v1.34.5 | Pure-Go SQLite driver | Existing, WAL mode, no CGO |

### Audio Tag Libraries
| Library | Version | Purpose | When Used |
|---------|---------|---------|-----------|
| `dhowden/tag` | v0.0.0-20240417 | Tag reading | Scanner |
| `bogem/id3v2/v2` | v2.1.4 | MP3 tag writing | Album edit, writeback |
| `gcottom/audiometa/v3` | v3.0.4 | FLAC/OGG/M4A tag writing | Album edit, writeback |

### Stdlib Capabilities Used for v1.3

| Package | Capability | Feature |
|---------|-----------|---------|
| `net/http` | `ParseMultipartForm`, `FormFile`, `MaxBytesReader` | Artwork upload |
| `net/http` | `DetectContentType` | Upload MIME validation |
| `crypto/sha1` | Hash-based filename generation | Artwork storage (existing pattern) |
| `os` | `WriteFile`, `MkdirAll` | Artwork storage (existing pattern) |
| `encoding/json` | JSON marshal/unmarshal | Match candidate API responses |

## Alternatives Considered

| Category | Recommended | Alternative | Why Not |
|----------|-------------|-------------|---------|
| Image upload | Go stdlib multipart | `gabriel-vasile/mimetype` | Stdlib `DetectContentType` is sufficient for jpg/png/webp |
| Image resize | Skip (no resize) | `disintegration/imaging` | Breaks dependency constraint, no functional need |
| Image resize | Skip (no resize) | `ffmpeg -i` shelling | Over-engineering for art thumbnails displayed at CSS fixed sizes |
| Match search API | Vanilla JSON endpoints | GraphQL | Over-engineering, project uses simple REST-like patterns |
| Match candidate caching | SQLite table or none | Redis/memcached | Single-user server, no caching layer needed |

## What NOT to Add

| Technology | Why Skip |
|------------|----------|
| Image processing library | No resize/crop needed; browsers handle CSS-based sizing |
| File upload middleware | `MaxBytesReader` + `ParseMultipartForm` is sufficient |
| WebSocket for progress | Existing job polling pattern (`settingsJobsJSON`) works |
| New config variables | Upload size limit can be a constant (15 MB, matching CAA); no user-facing config needed |
| CSRF tokens for upload | Auth middleware already wraps all routes; CSRF via session cookies is already handled |

## Integration Points for Implementation

### Artwork Upload Handler Pattern

Follow existing `albumArt` serving pattern in reverse:

```
POST /art/album/{albumID}/upload
  1. MaxBytesReader(w, r.Body, 15<<20)
  2. r.ParseMultipartForm(2<<20)     // 2MB memory, rest on disk
  3. file, header := r.FormFile("artwork")
  4. DetectContentType(first 512 bytes)  -- validate image/jpeg, image/png, image/webp
  5. sha1 hash of albumID + timestamp for unique filename
  6. Write to {DataDir}/thumbs/music/{hash}.{ext}
  7. UPDATE music_albums SET art_path=? WHERE id=?
  8. Redirect back to album page
```

### Music Match Search Endpoint Pattern

Mirror the existing `tvMatchSearch`:

```
GET /music/match/search?album_id={id}
  1. Load album title, artist, year from DB
  2. Call MBClient.SearchReleaseGroups(ctx, artist, title)
  3. Score each candidate with ScoreCandidate
  4. Return JSON array sorted by score descending
```

### Music Match Accept Endpoint Pattern

Extend existing `musicMatchApprove` with candidate data:

```
POST /music/match/accept
  album_id, release_group_id, release_id, artist_mbid, title, artist_name
  1. UPDATE music_albums with match data (same SQL as matchAlbum pipeline)
  2. Fetch cover art via CAAClient
  3. Enrich artist if needed
  4. Run writebackAlbumTracks for the accepted album
  5. Redirect back to match review
```

## Sources

- Go stdlib documentation: `net/http` multipart handling is built-in, no external reference needed
- Existing codebase patterns: `coverart.go`, `handlers_match.go`, `handlers_tv.go` provide all integration patterns
- No external research required -- all capabilities verified by reading the actual codebase

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| No new dependencies | HIGH | All three features map directly to existing stdlib + codebase patterns |
| Upload via stdlib multipart | HIGH | Standard Go pattern, well-documented, used in production everywhere |
| DetectContentType for MIME | HIGH | Stdlib function, checks magic bytes not extension, handles jpg/png/webp |
| Match search via existing APIs | HIGH | MBClient.SearchReleaseGroups and Matcher.SearchTV already exist and work |
| Track number fix is code-only | HIGH | Confirmed by reading both handler and template -- form field mismatch |

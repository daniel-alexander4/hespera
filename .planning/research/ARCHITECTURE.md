# Architecture Patterns

**Domain:** Manual controls for existing media server (artwork upload, track number fix, manual match selection)
**Researched:** 2026-03-07
**Overall confidence:** HIGH (analysis derived entirely from existing codebase, no external research needed)

## Current Architecture Summary

The application follows a consistent pattern: `Handler` struct with `Deps{Cfg, DB}`, stdlib `http.ServeMux` routing, `html/template` rendering with layout-based cloning, and direct SQL queries (no ORM/store layer). Background work goes through `jobs.Service` (buffered channel, single worker).

Art is stored as files in `{DataDir}/thumbs/music/` (album covers from CAA) and `{DataDir}/thumbs/tv/` (posters/backdrops from TMDB). Album art paths are stored in `music_albums.art_path`. Artist art in `music_artists.art_path`. TV art is in the `tv_series_art` table.

## Feature 1: Manual Artwork Upload

### Problem

Albums without Cover Art Archive art have no artwork. There is no way to manually assign artwork. The `albumArt` handler already falls back to `missing.album.webp` when `art_path` is empty.

### Integration Points

| Component | Current State | Change Needed |
|-----------|--------------|---------------|
| `handlers_music.go` `albumArt` | Reads `art_path` from `music_albums`, serves file from DataDir | No change - already serves whatever path is in DB |
| `music_albums.art_path` column | Stores relative or absolute path to art file | No change - upload handler writes file, updates column |
| `CAAClient.thumbDir` | `{DataDir}/thumbs/music/` | Upload handler should write to same directory |
| `music_album.html` template | Shows art if `AlbumArt` is non-empty, otherwise placeholder | Add upload button/form when no art present (or to replace) |
| `router.go` | No upload route exists | Add `POST /music/album/art/upload` route |

### New Component: Art Upload Handler

**Location:** `internal/web/handlers_music.go` (add method to Handler)

```
POST /music/album/art/upload
  - Multipart form: album_id (int64), file (image)
  - Validate: album exists, file is image (JPEG/PNG/WebP), file size <= 10MB
  - Save to: {DataDir}/thumbs/music/upload_{albumID}_{hash}.{ext}
  - Update: music_albums SET art_path=? WHERE id=?
  - Redirect: /music/album/{albumID}
```

**Why this approach:**
- No new packages needed. Go stdlib `r.FormFile` handles multipart upload.
- Reuses the existing `thumbs/music/` directory and `albumArt` serving handler.
- The `pathguard.ResolveExistingUnderRoot` check in `albumArt` already validates paths under DataDir, so uploads stored there are automatically safe to serve.
- Uses hash-based filename (SHA1 of content) to avoid collisions, matching the pattern in `coverart.go` `downloadAndSave`.

**Security considerations:**
- Validate Content-Type is image/* before saving.
- Limit upload size (10MB via `http.MaxBytesReader`).
- Use `pathguard` containment check on output path.
- Sanitize filename entirely (generate from hash, ignore user-provided filename).

### Template Changes

Modify `music_album.html`: add an "Upload Art" button/form in the album art area. This can be a simple file input that POSTs to the upload endpoint. Show it both when art is missing (primary action) and when art exists (replace action).

### Data Flow

```
User selects image file
  -> POST /music/album/art/upload (multipart)
  -> Handler validates album_id, reads file, checks image type/size
  -> Saves to {DataDir}/thumbs/music/upload_{albumID}_{sha1}.{ext}
  -> UPDATE music_albums SET art_path=? WHERE id=?
  -> Redirect to /music/album/{albumID}
  -> Existing albumArt handler serves the new file
```

## Feature 2: Track Number Edit Fix

### Problem

The album edit page has two modes:
1. **Writeback mode** (`?writeback=1`): Full tag editor with checkbox selection, single-track editing panel. Track numbers can be edited via the single-track panel when exactly one track is selected.
2. **Non-writeback mode** (default from album detail "Edit" button): Shows inline `track_no_{ID}` and `track_title_{ID}` input fields in the template, BUT the `musicAlbumEditPOST` handler never reads these form fields.

The handler reads `mode` from the form. In non-writeback mode, the template does not submit a `mode` field, so `mode` defaults to empty string, which falls into the `else` (all tracks) branch. That branch reads all track abs_paths, then for each track reads current tags from the file and writes back only album-level changes (title, artist, year). Per-track `track_no_` and `track_title_` form values are ignored entirely.

**Root cause:** The non-writeback edit form submits per-track fields with dynamic names (`track_no_{ID}`, `track_title_{ID}`), but the POST handler was designed around the writeback flow and never implemented reading those fields.

### Integration Points

| Component | Current State | Change Needed |
|-----------|--------------|---------------|
| `musicAlbumEditPOST` handler | Only processes album-level fields in non-writeback mode | Must read `track_no_{ID}` and `track_title_{ID}` from form, update DB |
| `music_album_edit.html` (non-writeback section) | Renders track number/title inputs but they are silently ignored | Template is correct, handler needs fixing |
| `music_tracks` table | `track_no`, `title` columns | Direct DB UPDATE, no file tag writing needed in non-writeback mode |

### Fix Approach

The non-writeback edit path should:

1. Read album-level fields (title, artist, year) and update `music_albums` + `music_artists` in DB.
2. Iterate over submitted `track_id` values from the form.
3. For each track, read `track_no_{ID}` and `track_title_{ID}` from form values.
4. Update `music_tracks SET track_no=?, title=? WHERE id=? AND album_id=?`.
5. Set `music_albums.match_status='manual'` to indicate user-edited metadata.
6. Redirect back to album detail.

**No file tag writing** in non-writeback mode -- this is DB-only editing, consistent with the original design intent (the "Edit" button from album detail vs the "Edit Tags" button from Tag Editor).

**Key insight:** The current handler always writes to audio files (calls `music.WriteTrackTags`), even in non-writeback mode. This is the deeper bug -- non-writeback mode should be DB-only. The fix should split the two paths cleanly:
- Non-writeback: DB UPDATE only, no file I/O.
- Writeback: File tag writing + rescan (existing behavior, already works for single-track via checkbox selection).

### Data Flow (Fixed)

```
Non-writeback POST:
  User edits track numbers/titles in form
  -> POST /music/album/edit?id={albumID}
  -> Handler reads album-level fields (title, artist, year)
  -> UPDATE music_albums (title, year, match_status='manual')
  -> Ensure artist exists or update name
  -> For each track_id in form:
     Read track_no_{ID}, track_title_{ID}
     UPDATE music_tracks SET track_no=?, title=? WHERE id=? AND album_id=?
  -> Redirect to /music/album/{albumID}
```

## Feature 3: Manual Match Selection UI

### Problem

When auto-match runs on an album and the best candidate scores below 80%, the album is marked `unmatched`. The match review page shows these albums with only "Reject" and "Re-match" buttons. Re-matching just clears the status and the next match run produces the same result. There is no way to see the candidates and pick one manually.

Same problem exists for TV: unmatched series only get a text input for TMDB ID (already implemented) but no candidate list from the auto-match attempt.

### Music Manual Match Selection

#### Integration Points

| Component | Current State | Change Needed |
|-----------|--------------|---------------|
| `MBClient.SearchReleaseGroups` | Returns `[]Candidate`, called from `matchAlbum` | Expose for on-demand search from handler |
| `ScoreCandidate` / `BestCandidate` | Scores candidates | Reuse for displaying scored candidates |
| `matchAlbum` | Auto-accepts >= 80, marks unmatched otherwise | No change to pipeline |
| `musicMatchReview` handler | Shows unmatched albums, no candidate info | Add per-album candidate search endpoint |
| `musicMatchApprove` handler | Sets `match_status='matched'` (approves current MB data) | Add new handler that accepts a specific candidate MBID |
| `music_match_review.html` | Table with reject/rematch buttons | Add search-and-select UI per album |
| `router.go` | No music match search endpoint | Add `GET /music/match/search` and `POST /music/match/select` |

#### New Components

**1. Music Match Search Endpoint**

```
GET /music/match/search?album_id={id}
  - Reads album title, artist, year from DB
  - Calls MBClient.SearchReleaseGroups(artist, title)
  - Scores each candidate with ScoreCandidate
  - Returns JSON array of candidates with scores
```

This mirrors the existing `GET /tv/match/search?q={query}` pattern exactly.

**2. Music Match Select Endpoint**

```
POST /music/match/select
  - Form: album_id, release_group_id, release_id (optional), artist_name, title, year, artist_mbid
  - Updates music_albums: musicbrainz_id, artist_musicbrainz_id, match_status='matched',
    match_confidence=100, match_source='musicbrainz', matched_at=now
  - Optionally normalizes album title and artist name to MB canonical names
  - Triggers CAA cover art fetch (inline, not job queue -- single album is fast)
  - Triggers tag writeback for the album (inline)
  - Redirect or JSON response
```

**3. Template Changes**

The `music_match_review.html` template needs the same search-and-select pattern used in `tv_match_review.html`:
- Add a text input per album row that triggers a search against `/music/match/search?album_id={id}`
- Show dropdown of candidates with title, artist, year, score
- Selecting a candidate populates a hidden field
- Approve button POSTs to `/music/match/select`

#### Data Flow

```
User views /music/match/review
  -> Sees unmatched albums with search inputs
  -> Types/clicks search for album row
  -> JS fetches GET /music/match/search?album_id={id}
  -> Handler queries MusicBrainz, scores candidates, returns JSON
  -> JS shows dropdown of candidates with scores
  -> User selects candidate
  -> JS populates hidden fields (release_group_id, artist_mbid, etc.)
  -> User clicks "Accept Match"
  -> POST /music/match/select
  -> Handler updates DB, fetches cover art, writes tags
  -> Redirect to /music/match/review
```

### TV Manual Match Selection

TV already has manual match selection implemented. The `tv_match_review.html` template has a search input per group, the `/tv/match/search` endpoint proxies to TMDB, and `/tv/match/approve` accepts a TMDB ID and updates all files in the group. No changes needed for TV.

## Recommended Architecture

### Component Boundaries

| Component | Responsibility | Communicates With |
|-----------|---------------|-------------------|
| `handlers_music.go` (modified) | Art upload handler, fixed album edit POST, music match search/select | DB, `match.MBClient`, `match.CAAClient`, `match.Scorer`, `pathguard`, filesystem |
| `handlers_match.go` (modified) | Music match search endpoint, music match select endpoint | DB, `match.MBClient`, `match.CAAClient`, `match.ScoreCandidate` |
| `match.MBClient` (no change) | MusicBrainz API queries | External API |
| `match.CAAClient` (no change) | Cover Art Archive downloads | External API, filesystem |
| `match.ScoreCandidate` (no change) | Candidate scoring | Pure function |
| `router.go` (modified) | New routes added | Handlers |
| `music_album.html` (modified) | Art upload UI | Handler |
| `music_album_edit.html` (no change) | Already correct template | Handler |
| `music_match_review.html` (modified) | Candidate search/select UI | Handler |
| `handler.go` (modified) | New template registration if needed | Templates |

### New Routes

| Route | Method | Handler | Purpose |
|-------|--------|---------|---------|
| `/music/album/art/upload` | POST | `musicAlbumArtUpload` | Upload album artwork |
| `/music/match/search` | GET | `musicMatchSearch` | Search MusicBrainz for candidates |
| `/music/match/select` | POST | `musicMatchSelect` | Accept a specific MB candidate |

### Modified Routes

| Route | Method | Change |
|-------|--------|--------|
| `/music/album/edit` | POST | Fix non-writeback mode to process per-track fields via DB UPDATE |

## Patterns to Follow

### Pattern 1: Multipart Upload (Art Upload)

**What:** Use `http.MaxBytesReader` + `r.FormFile` for safe file upload handling.
**When:** Handling user file uploads.
**Example:**

```go
func (h *Handler) musicAlbumArtUpload(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        w.WriteHeader(http.StatusMethodNotAllowed)
        return
    }

    // Limit request body to 10MB
    r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
    if err := r.ParseMultipartForm(10 << 20); err != nil {
        httpError(w, 400, "file too large", ...)
        return
    }

    albumID, err := strconv.ParseInt(r.FormValue("album_id"), 10, 64)
    // ... validate album exists ...

    file, header, err := r.FormFile("file")
    // ... validate content type is image ...
    // ... read, hash, save to thumbDir ...
    // ... UPDATE music_albums SET art_path=? ...

    http.Redirect(w, r, fmt.Sprintf("/music/album/%d", albumID), http.StatusSeeOther)
}
```

### Pattern 2: JSON Search Endpoint (Match Search)

**What:** Mirror the existing `tvMatchSearch` pattern for music match search.
**When:** Adding search-as-you-type functionality to match review.
**Example:**

```go
func (h *Handler) musicMatchSearch(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        w.WriteHeader(http.StatusMethodNotAllowed)
        return
    }
    albumID, err := strconv.ParseInt(r.URL.Query().Get("album_id"), 10, 64)
    // ... load album title, artist, year from DB ...

    mb := match.NewMBClient()
    candidates, err := mb.SearchReleaseGroups(r.Context(), artist, title)
    // ... score each candidate ...
    // ... return JSON array ...
}
```

### Pattern 3: DB-Only Edit (Track Number Fix)

**What:** Non-writeback album edit updates the database directly without touching audio files.
**When:** User edits metadata through the album detail "Edit" button (no `?writeback=1`).
**Example:**

```go
// In non-writeback branch of musicAlbumEditPOST:
trackIDs := r.Form["track_id"]  // hidden inputs from template
for _, idStr := range trackIDs {
    tid, _ := strconv.ParseInt(idStr, 10, 64)
    newTrackNo, _ := strconv.Atoi(r.FormValue(fmt.Sprintf("track_no_%d", tid)))
    newTitle := r.FormValue(fmt.Sprintf("track_title_%d", tid))
    _, _ = h.db.ExecContext(ctx,
        "UPDATE music_tracks SET track_no=?, title=? WHERE id=? AND album_id=?",
        newTrackNo, newTitle, tid, albumID)
}
// Also update album-level fields
_, _ = h.db.ExecContext(ctx,
    "UPDATE music_albums SET title=?, year=?, match_status='manual' WHERE id=?",
    newAlbum, newYear, albumID)
```

## Anti-Patterns to Avoid

### Anti-Pattern 1: New Dependency for Image Handling

**What:** Adding an image processing library (e.g., `imaging`, `disintegration/imaging`) for upload validation.
**Why bad:** Violates the 4-dependency constraint. Unnecessary for this use case.
**Instead:** Validate Content-Type header and file extension. Use `http.DetectContentType` on the first 512 bytes for server-side validation. No resizing or processing needed -- browsers and the UI already handle display sizing via CSS.

### Anti-Pattern 2: Job Queue for Single-Album Operations

**What:** Routing single-album art upload or match selection through the job queue.
**Why bad:** Overkill for a synchronous, fast operation. Job queue is for library-wide scans that take minutes. Single album operations complete in < 2 seconds.
**Instead:** Handle synchronously in the request. CAA cover art fetch for one album takes < 2 seconds. Writing one file to disk is instant. Only use the job queue for operations that iterate over many albums.

### Anti-Pattern 3: Separate Store Layer for These Changes

**What:** Extracting DB queries into a store/repository layer before adding new features.
**Why bad:** PROJECT.md explicitly lists store layer extraction as out-of-scope architectural debt (ARCH-01). Refactoring while adding features creates scope creep.
**Instead:** Continue the existing pattern of inline SQL in handlers. The queries are simple (single UPDATE, single INSERT). Keep the refactoring for a dedicated milestone.

### Anti-Pattern 4: SPA-Style Client-Side Routing

**What:** Building the candidate selection UI as a complex client-side application.
**Why bad:** The entire app is server-rendered HTML with vanilla JS. Adding a framework would be inconsistent.
**Instead:** Follow the `tv_match_review.html` pattern exactly: inline vanilla JS, fetch for search, DOM manipulation for dropdown, form POST for selection. This pattern is already proven in the codebase.

## Build Order (Dependency-Driven)

The three features have no dependencies on each other. However, the optimal build order considers testing ease and risk:

### Phase 1: Track Number Edit Fix (lowest risk, highest certainty)

- Modify `musicAlbumEditPOST` to split writeback vs non-writeback paths
- Non-writeback: DB-only UPDATE for track numbers and titles
- No new routes, no new templates, no external API calls
- **Test:** Unit test with `httptest` + `openTestDB`, verify DB state after POST

### Phase 2: Manual Artwork Upload (self-contained, no external APIs)

- Add `musicAlbumArtUpload` handler
- Add `POST /music/album/art/upload` route
- Modify `music_album.html` template to add upload form
- **Test:** Handler test with multipart body, verify file on disk + DB update

### Phase 3: Manual Match Selection (most complex, external API dependency)

- Add `musicMatchSearch` handler (GET, JSON response)
- Add `musicMatchSelect` handler (POST, DB + CAA + writeback)
- Add routes to `router.go`
- Modify `music_match_review.html` with search/select UI
- **Test:** Handler tests with mock MBClient responses, integration test for full flow

**Rationale for ordering:**
1. Track fix is a pure bug fix with zero new surface area. Ship it first to reduce regression risk.
2. Art upload is self-contained (filesystem + DB, no external APIs). Good second because it exercises the multipart upload pattern without API complexity.
3. Match selection is the most complex (external API calls, scoring, art fetch, writeback). Build it last when the other two are stable.

## Files Changed Summary

### New Files

None. All changes are additions to existing files.

### Modified Files

| File | Changes |
|------|---------|
| `internal/web/handlers_music.go` | Add `musicAlbumArtUpload`, fix `musicAlbumEditPOST` non-writeback path |
| `internal/web/handlers_match.go` | Add `musicMatchSearch`, `musicMatchSelect` |
| `internal/web/router.go` | Add 3 new routes |
| `web/templates/music_album.html` | Add art upload form |
| `web/templates/music_match_review.html` | Add search/select UI per album row |
| `internal/web/handlers_music_test.go` | Tests for art upload, fixed edit POST |
| `internal/web/handlers_match_test.go` | Tests for match search/select |

### No Schema Changes

All three features work within the existing database schema:
- Art upload: writes to existing `music_albums.art_path` column
- Track number fix: writes to existing `music_tracks.track_no` and `music_tracks.title` columns
- Match selection: writes to existing `music_albums.musicbrainz_id`, `match_status`, etc. columns

No migration needed in `db.Migrate()`.

## Sources

- All analysis derived from direct codebase inspection
- Existing patterns documented from: `handlers_tv.go` (TV match search/approve), `handlers_music.go` (album edit, art serving), `handlers_match.go` (match review/approve), `match/coverart.go` (CAA art storage), `match/musicbrainz.go` (MB search API), `match/scorer.go` (candidate scoring), `db/migrate.go` (schema)

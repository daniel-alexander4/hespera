# Domain Pitfalls

**Domain:** Manual controls for existing Go media server (artwork upload, track number fix, manual match selection)
**Researched:** 2026-03-07

## Critical Pitfalls

Mistakes that cause data loss, security vulnerabilities, or require rewrites.

### Pitfall 1: Image Upload Without Size/Type Validation Enables DoS and Stored XSS

**What goes wrong:** A file upload endpoint that trusts `Content-Type` headers or file extensions without reading magic bytes allows uploading arbitrarily large files (memory exhaustion) or non-image payloads (SVG with embedded JavaScript served back as `image/svg+xml`).

**Why it happens:** Go's `r.FormFile()` allocates `maxMemory` bytes in RAM then spills to disk. The default `r.ParseMultipartForm(32 << 20)` allows 32 MB in memory. Combined with no server-side validation, an attacker can upload a 2 GB file that fills the temp dir on the Docker container (non-root UID 65532, limited disk).

**Consequences:** Memory exhaustion crashes the single-process server. SVG-with-JS served from the same origin enables stored XSS. Non-image files stored with image extensions confuse the `artMIMEFromExt()` helper that trusts file extensions.

**Prevention:**
- Set `r.ParseMultipartForm(10 << 20)` -- 10 MB is generous for album art.
- Read first 512 bytes with `http.DetectContentType()` to sniff actual MIME; reject anything not `image/jpeg`, `image/png`, or `image/webp`.
- Never accept SVG. SVG is not a safe image format for user uploads.
- Use `io.LimitReader` when copying the body to disk, capping at a hard size limit (e.g. 10 MB).
- Generate the output filename server-side using a SHA hash (matching the `CAAClient.downloadAndSave` pattern already in `coverart.go`), never trust the uploaded filename.

**Detection:** Test by uploading: (a) a 50 MB file, (b) a `.svg` file, (c) a `.jpg` that is actually a text file. All three must be rejected.

---

### Pitfall 2: Uploaded Art Path Stored Without pathguard Containment Check

**What goes wrong:** The uploaded file is saved to the data directory, and its path is stored in `music_albums.art_path` or `music_artists.art_path`. If the save path is not validated through `pathguard.ResolveExistingUnderRoot()` before storage, a crafted filename could enable path traversal on write. More subtly, if the stored path is later served via the `albumArt` handler and it does not exist under `dataDir`, the pathguard check there will reject it, causing a silent art display failure.

**Why it happens:** The existing art storage pattern in `CAAClient.downloadAndSave()` constructs the path entirely server-side using SHA hashes, so it never had this problem. But a new upload endpoint receives user-controlled filenames. If the save logic uses the original filename or any user-supplied path component, the invariant breaks.

**Consequences:** Path traversal on write (overwriting arbitrary files as UID 65532). Or art that saves successfully but never displays because the serving handler's pathguard check fails.

**Prevention:**
- Generate the output filename identically to the existing pattern: `sha1(prefix + albumID) + ext` where ext is determined from the validated MIME type, not the uploaded filename.
- Save to the same `thumbDir` used by `CAAClient` (`{dataDir}/thumbs/music/`). This keeps all art in one directory that pathguard already trusts.
- After saving, verify the stored path with `pathguard.ResolveExistingUnderRoot(dataDir, savedPath)` before updating the DB.
- The existing `albumArt` handler already does a pathguard check on read, so this is defense-in-depth.

**Detection:** Upload art, then verify the art actually displays. Write a test that attempts path traversal via filename.

---

### Pitfall 3: Track Number Edit Bug -- Non-Writeback Form Inputs Are Never Read

**What goes wrong:** The existing `musicAlbumEditPOST` handler has a critical bug. The non-writeback mode template renders per-track form inputs named `track_no_{{.ID}}` and `track_title_{{.ID}}`, but the handler code never reads these dynamically-named fields. The handler reads only `title`, `artist`, `year` (album-level fields) and in `"all"` mode writes those to every track. Per-track track numbers and titles from the form are silently ignored.

**Why it happens:** The handler was built for two modes: `"all"` (album-level fields written to all tracks) and `"single"` (one track edited). The non-writeback template appears to support per-track editing but the handler has no code path that iterates `track_id` hidden fields and reads `track_no_{{id}}` / `track_title_{{id}}` values. This is documented in PROJECT.md as a known broken feature.

**Consequences:** Users edit track numbers in the form, click save, and the changes are silently discarded. The track number writeback appears to work (no error) but the actual file tags are not updated with the new track numbers.

**Prevention:**
- The simplest fix: use the existing `"single"` mode path. The single-track editing already works correctly -- `stTrackNo` is read from `single_track_no` and written via `WriteTrackTags`. The bug is only in the "all" mode template which shows per-track inputs that the handler ignores.
- If per-track editing is desired in the non-writeback UI: the handler must iterate `r.Form["track_id"]` and for each ID, read `r.FormValue("track_no_" + id)` and `r.FormValue("track_title_" + id)`. But this adds complexity for marginal value.
- The recommended approach: fix the single-track edit path to properly read and write track numbers (it already mostly works), and remove the misleading per-track inputs from the non-writeback template (or wire them up).

**Detection:** Edit a track number in the UI, save, re-open the edit page, verify the track number changed.

---

### Pitfall 4: Manual Match Selection Applied Without Storing Candidates for Audit

**What goes wrong:** The manual match selection UI queries MusicBrainz/TMDB for candidates, the user picks one, and the handler applies it immediately. But the candidate list and scores are never persisted. If the user later wants to know why an album was matched to a particular release group, or wants to pick a different candidate, the data is gone.

**Why it happens:** The current auto-match pipeline in `matchAlbum()` uses `BestCandidate()` to pick the top result and applies it in one shot. There is no "candidates" table. The pipeline was designed for automated matching where the score is the decision. Manual selection inherits this "apply and forget" pattern.

**Consequences:** No audit trail for manual matches. Re-running "rematch" on a manually-matched album queries MusicBrainz again (rate-limited, 1 req/sec) and may return different results. The user cannot review what candidates were available when they made the choice.

**Prevention:**
- For v1.3, this is acceptable scope. The TV match review already works this way -- `tvMatchApprove` takes a TMDB ID and applies it without storing alternatives. Music can follow the same pattern.
- Store `match_method='manual'` (like TV does) to distinguish manual from auto matches. This is cheap and useful.
- Do NOT build a candidates table for v1.3. It adds schema complexity, and the re-query cost is one MusicBrainz API call per album. Save this for a future milestone if users request it.

**Detection:** After manually matching an album, verify `match_method='manual'` is set in the DB.

## Moderate Pitfalls

### Pitfall 5: Music Manual Match Search Endpoint Missing -- Must Build From Scratch

**What goes wrong:** The TV pipeline already has `/tv/match/search?q=...` backed by `tmdb.SearchTV()`, with a working dropdown UI in `tv_match_review.html`. The music pipeline has no equivalent. `MBClient.SearchReleaseGroups()` exists but returns `[]Candidate` which is an internal type, not a JSON-serializable search result with display-friendly fields.

**Why it happens:** The auto-match pipeline only ever uses the internal `SearchReleaseGroups` + `BestCandidate` flow. There was never a need to expose search results to the frontend.

**Prevention:**
- Build `/music/match/search?q=...&artist=...` as a new GET handler that calls `MBClient.SearchReleaseGroups()`, scores all candidates with `ScoreCandidate()`, and returns JSON with fields the UI needs: `release_group_id`, `title`, `artist_name`, `year`, `primary_type`, `score`, `cover_url`.
- Mirror the TV pattern: the existing `tv_match_review.html` JavaScript for typeahead search is battle-tested and should be adapted, not rewritten.
- Rate limiting: `MBClient.throttle()` already enforces 1 req/sec. The search endpoint must not bypass this. Since `MBClient` is created per-request in `match.New()`, each handler creates its own rate limiter. This means two concurrent search requests both fire immediately. Fix: either share a single `MBClient` across the Handler lifetime, or add a global rate limiter.

**Detection:** Two concurrent search requests from the UI should not both return results in <1 second.

---

### Pitfall 6: Applying Manual Music Match Without Running Enrichment/Art/Writeback

**What goes wrong:** When the user manually selects a MusicBrainz release group for an album, the naive implementation just updates `musicbrainz_id`, `match_status='matched'`, and `match_confidence=100`. But the full auto-match pipeline also: (a) normalizes the album title to the MusicBrainz canonical title, (b) normalizes the artist name, (c) fetches cover art from CAA, (d) writes tags back to audio files, (e) updates `artist_musicbrainz_id`.

**Why it happens:** The auto-match pipeline bundles all of these into `matchAlbum()`. A manual match handler that only updates the status field gets a "matched" album with the local (potentially misspelled) title, no cover art, no tag writeback, and no artist MBID.

**Consequences:** Manual matches appear incomplete compared to auto-matches. Albums show as "matched" but display local (non-normalized) titles and lack cover art.

**Prevention:**
- After the user selects a candidate, apply the same post-match steps that `matchAlbum()` does:
  1. Update album: `match_status`, `match_confidence`, `musicbrainz_id`, `artist_musicbrainz_id`, `matched_at`, `match_source='musicbrainz'`
  2. Normalize title if the candidate has a canonical title
  3. Normalize artist name if the candidate has a canonical artist
  4. Fetch cover art via `CAAClient.FetchCover()` (only if art_path is empty)
  5. Run `writebackAlbumTracks()` to update file tags
- The cleanest approach: extract the post-selection logic from `matchAlbum()` into an `applyMatch(ctx, albumID, candidate)` function that both auto-match and manual-match call.

**Detection:** Manually match an album, verify: cover art appears, title/artist are canonical (if different), MBID columns are populated, file tags updated.

---

### Pitfall 7: Track Number Zero Is Silently Allowed by `stTrackNo > 0` Guard

**What goes wrong:** In `musicAlbumEditPOST`, the track number is only applied `if stTrackNo > 0`. This means if a user sets track number to 0 (to clear it), or if the form sends "0", the track number is not written. Similarly, `WriteTrackTags` has `if fields.TrackNo > 0` before writing. Track number 0 is a valid edit (user wants to clear/reset) but the code treats it as "no change".

**Why it happens:** The guard `> 0` was designed to mean "only write if a value was provided", using 0 as a sentinel for "not provided". But 0 is also a legitimate track number value (meaning "unset/unknown"), and Go's `strconv.Atoi("")` returns 0, making "not provided" and "explicitly set to 0" indistinguishable.

**Consequences:** Minor -- users cannot explicitly clear a track number. Since the primary use case is fixing track numbers (setting them to correct values like 1, 2, 3...), this is unlikely to cause real problems.

**Prevention:**
- For v1.3, this is a known limitation, not a blocker. Document it.
- If fixing: use `FormValue` emptiness check to distinguish "field present with value 0" from "field absent". Or use `-1` as the sentinel.

**Detection:** Try setting track number to 0 in the edit UI. It won't take effect.

---

### Pitfall 8: Image Upload Overwrites Existing Art Without Confirmation

**What goes wrong:** An album already has cover art (either from embedded tags via scanner, or from Cover Art Archive). The user uploads new art. The new file is saved but the old file on disk is not deleted (orphaned), and the `art_path` is overwritten in the DB.

**Why it happens:** The upload handler updates `art_path` unconditionally. The old file remains on disk because nothing deletes it.

**Consequences:** Disk space leak over time from orphaned art files. Not catastrophic for a personal server, but sloppy.

**Prevention:**
- Before updating `art_path`, query the current value. If non-empty and different from the new path, delete the old file.
- Be careful: the same art file might be referenced by multiple albums (e.g., duplicate detection merged albums). Query `SELECT COUNT(*) FROM music_albums WHERE art_path=?` before deleting.
- Simpler approach for v1.3: just overwrite in DB, accept orphaned files. Add cleanup later.

**Detection:** Upload art to an album that already has art. Check if the old file still exists on disk.

---

### Pitfall 9: MusicBrainz Rate Limiting Under Manual Search -- MBClient Per-Request Instantiation

**What goes wrong:** The `musicMatch` handler creates `match.New(h.db, h.cfg.DataDir)` which creates `NewMBClient()` with a fresh `lastReq` time. The throttle only limits within a single MBClient instance. If the manual search endpoint also creates `match.New()` per request, two rapid search requests bypass rate limiting because each has its own throttle state.

**Why it happens:** The current architecture creates a new `Matcher` per handler call. For batch operations (scan+match), this is fine because one job processes all albums sequentially. For interactive search, users may type quickly, triggering multiple requests.

**Consequences:** MusicBrainz returns HTTP 503 (rate limited). The user agent gets temporarily banned.

**Prevention:**
- Option A (recommended): Share a single `MBClient` on the `Handler` struct, initialized in `web.New()`. All handlers that need MusicBrainz access use `h.mbClient`. The `Matcher` struct can accept an injected `MBClient` instead of creating its own.
- Option B (simpler): Add a debounce timer on the frontend (the TV search already has `setTimeout` at 400ms, which helps). Combined with the per-instance throttle, this reduces the issue but doesn't eliminate it for concurrent users.
- Option C: Use a `sync.Mutex` at the package level in `match` instead of per-instance. Quick fix but global state is ugly.

**Detection:** Open the manual match search, type quickly triggering 3+ searches in 2 seconds. Check server logs for "rate limited" errors from MusicBrainz.

---

### Pitfall 10: Manual TV Match Approve Already Exists But Music Equivalent Needs Different Data Model

**What goes wrong:** Developers copy the TV `tvMatchApprove` pattern for music, but the data models differ. TV match approve takes a TMDB ID (integer, typed into a text field or selected from search). Music match approve needs a MusicBrainz release group UUID, an artist MBID, an artist name, and a release ID (for cover art fallback). Passing all of these through form hidden fields is error-prone and fragile.

**Why it happens:** TV has a simple 1:1 mapping: guessed_title -> tmdb_id. Music has a multi-field candidate with release_group_id, release_id, artist_mbid, artist_name, title, year, primary_type. The music candidate is a rich object, not a single ID.

**Consequences:** If only the release_group_id is passed, the handler cannot normalize titles, fetch art, or set artist MBID without re-querying MusicBrainz (additional API call + rate limit hit).

**Prevention:**
- Pass the candidate's key fields as hidden form values: `release_group_id`, `release_id`, `artist_mbid`, `artist_name`, `title`, `year`. The search endpoint already returns all of these.
- In the approve handler, construct a `Candidate` struct from the form values and pass it to the shared `applyMatch()` function.
- This avoids a round-trip to MusicBrainz on approval.

**Detection:** Approve a manual match. Verify all fields (MBID, artist MBID, normalized title, cover art) are populated without additional MusicBrainz API calls in the logs.

## Minor Pitfalls

### Pitfall 11: Multipart Form Upload Needs CSRF Protection

**What goes wrong:** The existing forms use standard POST with `ParseForm()`. The auth middleware provides CSRF protection via session cookies. Multipart forms (`enctype="multipart/form-data"`) need `ParseMultipartForm()` instead of `ParseForm()`. CSRF tokens in hidden fields still work with multipart forms, but the handler must call the right parse method.

**Prevention:** Use `r.ParseMultipartForm(maxBytes)` for the upload handler. The CSRF token (if present in a hidden field) will be in `r.MultipartForm.Value`, not `r.Form`. However, looking at the existing auth middleware, it uses session cookies (HMAC-SHA256), not per-form CSRF tokens. Cookie-based auth with SameSite protects against CSRF already. No additional work needed unless the auth pattern changes.

---

### Pitfall 12: Browser Cache Prevents Updated Art From Displaying

**What goes wrong:** After uploading new art, the browser cache serves the old image. The `albumArt` handler sets `Cache-Control: no-cache`, which means the browser must revalidate but may still serve from cache if the response looks the same (no ETag or Last-Modified changes).

**Prevention:** After a successful art upload, redirect to the album page with a cache-busting query parameter (e.g., `/art/album/123?t=1709827200`). Or set `Cache-Control: no-store` on art responses (already uses `no-cache`, which is close enough). Since the handler uses `io.Copy` without `http.ServeContent`, there's no automatic ETag. This is actually fine -- `no-cache` without an ETag means the browser always re-fetches.

---

### Pitfall 13: Template Registration for New Pages

**What goes wrong:** New templates (e.g., `music_match_select.html`) must be added to the `pages` slice in `handler.go` `New()`. If forgotten, the template is not compiled at startup, and the handler panics or returns a 500 when rendering.

**Prevention:** The existing code has a post-loop validation that checks every page in the slice exists. Adding a new page to the slice and having the template file present is sufficient. The fail-fast behavior is correct. Just don't forget to add the template name to the slice.

**Detection:** Run the server. If the template is missing, startup fails with a clear error message.

## Phase-Specific Warnings

| Phase Topic | Likely Pitfall | Mitigation |
|-------------|---------------|------------|
| Manual artwork upload | Image validation bypass (Pitfall 1) | Sniff magic bytes, reject SVG, limit size with LimitReader |
| Manual artwork upload | Path traversal on save (Pitfall 2) | Generate filenames server-side via SHA hash, save to existing thumbDir |
| Manual artwork upload | Orphaned files (Pitfall 8) | Accept for v1.3, add cleanup utility later |
| Track number edit fix | Per-track form inputs never read (Pitfall 3) | Fix single-track mode or wire up per-track inputs in handler |
| Track number edit fix | Zero value sentinel collision (Pitfall 7) | Accept as known limitation for v1.3 |
| Manual match selection (music) | No search endpoint exists (Pitfall 5) | Build new /music/match/search, mirror TV pattern |
| Manual match selection (music) | Incomplete post-match pipeline (Pitfall 6) | Extract shared applyMatch() from matchAlbum() |
| Manual match selection (music) | MBClient rate limiter per-instance (Pitfall 9) | Share MBClient on Handler or add frontend debounce |
| Manual match selection (music) | Multi-field candidate vs single ID (Pitfall 10) | Pass all candidate fields via hidden form values |
| Manual match selection (TV) | Already working -- tvMatchSearch + tvMatchApprove pattern exists | Extend existing pattern, no new pitfalls |

## Sources

- Codebase analysis: `internal/web/handlers_music.go`, `internal/web/handlers_match.go`, `internal/web/handlers_tv.go`, `internal/match/pipeline.go`, `internal/match/musicbrainz.go`, `internal/match/coverart.go`, `internal/music/tagwrite.go`, `internal/db/migrate.go`
- Existing patterns: TV match search/approve flow in `tv_match_review.html` and `tvMatchSearch`/`tvMatchApprove` handlers
- Go stdlib: `http.DetectContentType()` for MIME sniffing, `io.LimitReader` for size limits (HIGH confidence -- stdlib docs)
- MusicBrainz rate limiting: 1 req/sec documented at musicbrainz.org/doc/MusicBrainz_API/Rate_Limiting (HIGH confidence)

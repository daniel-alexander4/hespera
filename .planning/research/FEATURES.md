# Feature Landscape

**Domain:** Media server manual controls (artwork, track editing, match selection)
**Researched:** 2026-03-07
**Milestone:** v1.3 Manual Controls

## Table Stakes

Features users expect when manual controls exist. Missing = feature feels broken or incomplete.

| Feature | Why Expected | Complexity | Dependencies | Notes |
|---------|--------------|------------|--------------|-------|
| Manual artwork upload for albums | Albums without CAA art or embedded art show a placeholder; users want to fix this | Medium | Existing `art_path` column, `albumArt` handler, `pathguard`, multipart form parsing | Multipart upload, save to `thumbs/music/`, update `art_path` in DB. Must validate image type (JPEG/PNG/WebP), enforce size limit (~10MB), and use pathguard for the save path |
| Fix track number editing in single-track edit mode | The edit form has track_no fields but the POST handler ignores them -- `stTrackNo > 0` silently skips track 0 and the non-writeback mode has no POST handler for per-track fields at all | Low | Existing `musicAlbumEditPOST` handler, `WriteTrackTags`, form template | Bug fix: change `stTrackNo > 0` guard to allow 0 (or use a "was this field submitted" check), and wire up the non-writeback edit mode to actually process `track_no_{ID}` and `track_title_{ID}` form fields |
| Manual match selection for music (MusicBrainz) | Unmatched albums with no way to pick from candidates creates a dead end -- re-running auto-match produces the same below-threshold result | High | Existing `MBClient.SearchReleaseGroups`, `Candidate` type, `BestCandidate`, `CAAClient`, `matchAlbum` logic, match review UI | New search endpoint + candidate display + selection handler. Must trigger same post-match pipeline (CAA art, title normalization, writeback) |
| Manual match selection for TV (TMDB) | Already partially exists via `tvMatchSearch` + `tvMatchApprove` with inline search UI in `tv_match_review.html` | Already done | `tmdb.NewMatcher`, `tvMatchSearch`, `tvMatchApprove` handlers | TV manual match already works: search box in review UI, select from dropdown, approve with TMDB ID. No new work needed |
| Artwork delete/replace | Once artwork is manually uploaded, users need a way to remove or replace it without re-scanning | Low | Same upload endpoint, existing `art_path` column | Delete = clear `art_path` and optionally remove file. Replace = overwrite. Can combine with upload form |

## Differentiators

Features that set the product apart. Not expected in a personal server, but add polish.

| Feature | Value Proposition | Complexity | Dependencies | Notes |
|---------|-------------------|------------|--------------|-------|
| Artwork preview before save | Show the uploaded image in-browser before committing | Low | Client-side JavaScript `FileReader` + `URL.createObjectURL` | No server round-trip needed. Small JS addition to the upload form |
| Drag-and-drop artwork | Drop an image file onto the album page to set artwork | Low | Client-side drag events, same upload endpoint | Ergonomic improvement. Reuses the same POST handler |
| Candidate scoring display | Show confidence scores alongside MusicBrainz candidates so users understand why auto-match failed | Low | Existing `ScoreCandidate` function, `Candidate` type | Surface the score breakdown (title sim, artist sim, type, year) next to each candidate |
| Bulk track renumber | Select multiple tracks and auto-assign sequential track numbers | Medium | DB update + optional tag writeback | Useful for compilations with all-zero track numbers |
| Artist artwork upload | Same as album artwork but for artist images (bio page) | Low | Same pattern as album art upload, `music_artists.art_path` | Direct reuse of the album upload pattern |

## Anti-Features

Features to explicitly NOT build.

| Anti-Feature | Why Avoid | What to Do Instead |
|--------------|-----------|-------------------|
| URL-based artwork fetch (paste a URL to download art) | Adds HTTP client complexity, SSRF risk, content-type validation headaches | Upload only -- user can download the image themselves first |
| Artwork from embedded tag extraction on demand | Already handled by the scanner pipeline at scan time; duplicating extraction logic in the edit flow adds complexity | Rely on scan-time extraction. If art is in the file, a rescan picks it up |
| Full metadata editor for all fields (genre, composer, lyrics) | Scope creep. The edit UI covers the fields that matter for matching and display. Adding every ID3 field turns a simple form into a tag editor clone | Keep the focused field set: title, artist, year, track_no, disc_no. Point users to MusicBrainz Picard for full tag editing |
| Automatic re-match on artwork upload | Uploading art is a visual fix, not a metadata identity change. Re-running match pipeline would confuse the status model | Keep art upload and match pipeline independent |
| Match candidate caching | Caching MB search results adds DB schema and invalidation complexity | Search is fast (sub-second). The 1 req/sec rate limiter is sufficient for manual interactive use |

## Feature Dependencies

```
Manual artwork upload (standalone, no dependencies)
  |
  +--> Artist artwork upload (same pattern)

Track number edit fix (standalone bug fix, no dependencies)

Manual match selection for music
  |
  +--> Requires: search API endpoint (new handler)
  +--> Requires: candidate display UI (new template partial)
  +--> Triggers: existing post-match pipeline (CAA art, normalization, writeback)
  +--> Depends on: existing MBClient.SearchReleaseGroups, ScoreCandidate, CAAClient

TV manual match selection (already implemented)
```

## Detailed Analysis

### Manual Artwork Upload

**Current state:** Album art comes from two sources: (1) embedded art extracted at scan time via `saveEmbeddedArt` in `scanner.go`, stored in `{DataDir}/thumbs/music/` with SHA1-hashed names; (2) Cover Art Archive fetch during match pipeline via `CAAClient.FetchCover`, also stored in `thumbs/music/`. Both write absolute paths to `music_albums.art_path`. The `albumArt` handler serves art from `art_path` with pathguard containment to DataDir. Missing art falls back to `missing.album.webp`.

**What to build:**
1. New route: `POST /art/album/upload` accepting `multipart/form-data` with fields `album_id` (int) and `file` (image).
2. Handler validates: album exists, file is JPEG/PNG/WebP (check magic bytes, not just extension), file size <= 10MB.
3. Save to `{DataDir}/thumbs/music/manual-{albumID}-{hash}.{ext}` -- the `manual-` prefix distinguishes user uploads from auto-fetched art.
4. Update `music_albums SET art_path=? WHERE id=?` -- no conditional, always overwrite (user intent is explicit).
5. UI: Add an "Upload Art" button/form to the album detail page (`music_album.html`), visible when art is placeholder OR as a replace action.
6. Use `r.ParseMultipartForm(10 << 20)` for 10MB memory limit. Read file with `r.FormFile("file")`.

**Existing patterns to follow:** The `CAAClient.downloadAndSave` method demonstrates the save-to-thumbs-dir pattern. The `albumArt` handler already serves from DataDir with pathguard. No new dependencies needed.

### Track Number Edit Fix

**Current state:** Two bugs in `musicAlbumEditPOST`:

1. **Single-track mode guard bug:** Line 737: `if stTrackNo > 0` means track number 0 is silently ignored. A track numbered 0 can never be written. This should check if the form field was explicitly submitted rather than using a > 0 guard.

2. **Non-writeback mode has no per-track processing:** The non-writeback template (lines 175-201 in `music_album_edit.html`) renders `track_no_{ID}` and `track_title_{ID}` inputs, but the POST handler never reads these form fields. It only processes album-level fields (title, artist, year) and the single-track fields in writeback mode. The non-writeback edit mode effectively silently discards all track-level changes.

**What to fix:**
1. Change the `stTrackNo > 0` guard to use a sentinel or check `r.FormValue("single_track_no") != ""`.
2. For non-writeback mode: read `track_no_{ID}` and `track_title_{ID}` from form, update the DB directly (`UPDATE music_tracks SET track_no=?, title=? WHERE id=?`), and trigger a `WriteTrackTags` for each modified track. OR: simplify by removing the non-writeback edit mode entirely and always using the writeback mode with checkbox selection (the template already has both modes but the non-writeback mode is incomplete).

**Recommendation:** Remove the non-writeback edit mode. The writeback mode (checkbox selection, single-track fields panel) is more complete and already works for title/artist/disc changes. Fix the `> 0` guard in the writeback path. This is simpler than implementing a full second edit path.

### Manual Match Selection for Music

**Current state:** The music match review page (`/music/match/review`) shows unmatched albums with Approve (accept current MB match), Reject (skip, clear MBID), and Re-match (reset to unmatched, re-run auto) buttons. The problem: unmatched albums got there because auto-match scored below 80%. Re-matching produces the same result. There is no way to search MusicBrainz manually and pick from candidates.

TV already has this pattern implemented: `tvMatchSearch` is a GET endpoint that takes `?q=...`, calls `matcher.SearchTV()`, returns JSON results. The TV match review template has inline search with a dropdown. Users type a name, pick a result, and approve.

**What to build (mirror the TV pattern):**
1. New route: `GET /music/match/search?q=...&album_id=...` -- calls `MBClient.SearchReleaseGroups(ctx, artist, query)` and returns scored candidates as JSON. Include `album_id` so we can score against the album's local metadata.
2. New route: `POST /music/match/select` -- accepts `album_id` and selected candidate data (release_group_id, release_id, artist_mbid, title, artist_name). Applies the match: updates `music_albums` with MBID/confidence/status, fetches CAA art, normalizes names, triggers writeback. This reuses most of `matchAlbum` logic but with a user-supplied candidate instead of auto-best.
3. UI: Add search box + dropdown to each row in `music_match_review.html`, same pattern as `tv_match_review.html`. Show candidate title, artist, year, type, and score for each result.

**Key design decisions:**
- Reuse the existing `MBClient.SearchReleaseGroups` cascade or expose a simpler search? Use `SearchReleaseGroups` -- it already has the 3-strategy cascade and returns `[]Candidate`.
- Should selecting a candidate trigger the full post-match pipeline (CAA art, name normalization, tag writeback)? Yes -- this is what users expect. They pick a match and it takes effect fully.
- Rate limiting: the MBClient already has 1 req/sec throttle. For interactive search, this is fine -- the search is triggered by user action, not batch processing.

**Existing code to reuse:**
- `MBClient.SearchReleaseGroups` for search
- `ScoreCandidate` / `BestCandidate` for scoring display
- `CAAClient.FetchCover` for art fetch after selection
- `matchAlbum` logic for DB updates and writeback (extract into a shared function)
- `tv_match_review.html` JavaScript pattern for search dropdown

### TV Manual Match (Already Done)

The TV match review UI already has manual search and selection. The `tvMatchSearch` handler searches TMDB, the `tvMatchApprove` handler applies the selection, and the `tv_match_review.html` template has the inline search dropdown with JavaScript. No new work needed for TV.

## MVP Recommendation

Prioritize in this order (based on user impact and dependency structure):

1. **Track number edit fix** -- Bug fix. Smallest scope, highest impact per line of code. Users actively hitting this bug.
2. **Manual artwork upload** -- Standalone feature. No dependency on match pipeline. Directly addresses visible gaps (placeholder art).
3. **Manual match selection for music** -- Largest scope but solves the core "dead end" problem. Builds on patterns already proven in the TV match UI.

Defer:
- **Artist artwork upload**: Same pattern as album upload, but lower priority (artist art comes from Wikimedia enrichment and is less visible).
- **Drag-and-drop artwork**: Polish feature. Upload button is sufficient for v1.3.
- **Bulk track renumber**: Edge case. Can be addressed in a future milestone if compilations with all-zero track numbers are a common problem.

## Sources

- Codebase analysis: `handlers_match.go`, `handlers_music.go`, `handlers_tv.go`, `musicbrainz.go`, `coverart.go`, `pipeline.go`, `scanner.go`, `music_album_edit.html`, `music_match_review.html`, `tv_match_review.html`
- [MusicBrainz API Search](https://musicbrainz.org/doc/MusicBrainz_API/Search) -- confirms search endpoint returns scored results suitable for manual selection
- [Jellyfin Metadata docs](https://jellyfin.org/docs/general/server/metadata/) -- confirms "Identify" (manual match) is a standard media server feature
- [Go multipart upload patterns](https://freshman.tech/file-upload-golang/) -- confirms `r.ParseMultipartForm` + `r.FormFile` as the standard approach

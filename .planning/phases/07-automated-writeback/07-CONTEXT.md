# Phase 7: Automated Writeback - Context

**Gathered:** 2026-03-06
**Status:** Ready for planning

<domain>
## Phase Boundary

Auto-accepted matches immediately write MBIDs and normalized metadata back to audio file tags. Writeback happens as part of the match pipeline, not as a separate manual step. Cover art and artist enrichment are Phase 8.

</domain>

<decisions>
## Implementation Decisions

### Writeback trigger
- Chain writeback into the match pipeline automatically (Claude decides: inline per-album or as chained job)
- Manual writeback button on review page stays as fallback (useful after manual matching)

### Name normalization
- When matchAlbum() finds a match, update music_albums.title and music_artists.name to MusicBrainz canonical values
- Writeback then naturally writes the normalized names from the DB
- The MusicBrainz candidate already has Title and ArtistName fields available

### Writeback scope
- Only write newly matched tracks, not all matched tracks in the library
- Need to track which albums were just matched this run to scope the writeback

### Non-MP3 MBID writing
- Add MBID writing for FLAC, OGG, M4A where the format supports it
- MP3 already writes MBIDs as TXXX frames (MusicBrainz Release Group Id, MusicBrainz Artist Id)
- FLAC/OGG support Vorbis comments which can store arbitrary key-value pairs
- M4A support depends on mp4meta library capabilities

### Claude's Discretion
- Whether to chain writeback as a separate job or inline it in matchAlbum()
- How to track "newly matched" albums for scoped writeback (e.g., collect IDs during pipeline run, or use a timestamp comparison)
- Vorbis comment key names for MBIDs (standard MusicBrainz tag names)
- Whether M4A MBID writing is feasible with current mp4meta library (skip if not supported)

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- `match.Matcher.RunTagWriteback()` (`internal/match/writeback.go`): Full writeback executor — queries matched tracks, calls WriteTrackTags. Currently writes ALL matched tracks.
- `music.WriteTrackTags()` (`internal/music/tagwrite.go`): Tag writer dispatching by format. Already writes title, artist, album, year, track#, disc#, MBIDs (MP3 only).
- `music.TagWriteFields` struct: Already has AlbumMBID and ArtistMBID fields.
- `setTXXX()` helper for MP3 TXXX frames.
- `writeMultiFormatTags()`: FLAC/OGG/M4A writer via audiometa — needs MBID additions.

### Established Patterns
- Scan → match chaining: handlers_settings.go:271 enqueues music_match after scan
- Non-fatal per-track errors: writeback.go logs and continues on individual track failures
- Atomic write for non-MP3: temp file + rename pattern in writeMultiFormatTags()
- Job progress tracking: writeback updates scan_jobs progress every 50 tracks

### Integration Points
- `matchAlbum()` in pipeline.go: Where MusicBrainz candidate data (Title, ArtistName, MBIDs) is available — needs to update DB names here
- `RunMusicMatch()` pipeline entry: Where writeback chaining would be added
- `handlers_settings.go:271`: Existing scan→match chain pattern to follow for match→writeback
- `handlers_match.go:216`: Manual writeback endpoint — stays as-is

</code_context>

<specifics>
## Specific Ideas

- The MusicBrainz Candidate struct already has Title and ArtistName — these are the canonical normalized values to store in the DB
- For FLAC Vorbis comments, standard MusicBrainz keys are MUSICBRAINZ_RELEASEGROUPID and MUSICBRAINZ_ARTISTID
- The writeback scope change (only newly matched) keeps the auto-writeback fast — no rewriting thousands of already-correct files

</specifics>

<deferred>
## Deferred Ideas

None — discussion stayed within phase scope

</deferred>

---

*Phase: 07-automated-writeback*
*Context gathered: 2026-03-06*

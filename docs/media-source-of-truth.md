# Media Source of Truth (SoT)

> **Consult this document before doing any work that touches media data** —
> scanning, matching, pruning, playback state, artwork, or any new query/handler
> that reads or writes a media row. It describes *what owns each piece of data*,
> *how a file on disk maps to DB rows*, and *what survives vs. what is lost*
> across rescans, rematches, moves, and deletions. If you change any of this
> behavior, update this doc in the same commit (see "Keeping this in sync").

---

## 0. The one invariant

**The filesystem under `HESPERA_MEDIA_ROOT` is authoritative for which media
exists. The SQLite DB is a *derived index*, keyed by absolute path, that scans
reconcile toward the disk.**

- Delete the DB → a rescan rebuilds everything that is re-derivable from files.
- The DB is authoritative *only* for **derived/added state**: parsed tags,
  match decisions, probe/stream info, artwork pointers, and **user-generated
  state** (playback progress, play history, manual matches/skips).
- Identity of a file row is **`UNIQUE(library_id, abs_path)`** — the path *is*
  the reconciliation key. There is **no content-hash identity**; a moved file is
  a new row (see §5 for the move-relink mitigation).

Config: `internal/config/config.go` — `DataDir` (DB + all derived artifacts) and
`MediaRoot` (the media tree). Both must be absolute; scanners reject a library
`root_path` that is not under `MediaRoot`.

---

## 1. Quick-lookup table

| | **Music** | **TV** | **Movies** |
|---|---|---|---|
| File row (the SoT join to disk) | `music_tracks` | `tv_series_files` | `movie_files` |
| Path column / identity | `abs_path` / `UNIQUE(library_id, abs_path)` | same | same |
| Change signal | `file_size_bytes` + `mtime_unix` | same | same |
| Content signature | `checksum_sha256` (stored) | *(none)* | *(none)* |
| Match/identity lives in | `music_albums` / `music_artists` rows | `tv_series_identities` (1:1, PK `file_id`) | inline on `movie_files` |
| Match metadata cache | — (live MB/CAA) | `tv_series_metadata_cache` (JSON by `entity_key`) | `movie_metadata_cache` |
| Artwork rows | `art_path` on album/artist | `tv_series_art` | `movie_art` |
| Cast / people | — | `people` + `credits` (global TMDB cache; `credits.media_type` discriminates tv/movie) | reuses `people` + `credits` |
| Playback/resume | — | `tv_playback_progress` (PK `file_id`) | `movie_playback_progress` (PK `file_id`) |
| Other user state | `play_history` (FK `track_id`) | — | — |
| Scanner | `internal/scan` (`ScanMusic`) | `internal/tvscan` (`ScanTV`) | `internal/moviescan` (`ScanMovies`) |
| Matcher | `internal/match` (`RunMusicMatch`) | `internal/tmdb` (`RunTVMatch`) | `internal/tmdb` (`RunMovieMatch`) |
| Move-relink | `relinkMovedTracks` | `relinkMovedFiles` | `relinkMovedFiles` (moviescan) |

Schema for every table above is in `internal/db/migrate.go` (`schemaSQL` const;
column additions via `ensureColumn` in `Migrate()`).

---

## 2. The scan lifecycle (all media types)

Every scanner follows the same disk→DB reconciliation shape:

1. **Guard** — resolve the library `root_path`, assert it is under `MediaRoot`.
2. **Walk** — `filepath.WalkDir` over the tree, filtered by extension
   (`music.IsAudioExt` / `video.IsVideoExt`).
3. **Upsert per file** — `INSERT ... ON CONFLICT(library_id, abs_path) DO UPDATE`.
   New path → new row. Existing path → file facts refreshed.
   - **Change detection**: an unchanged file (same `file_size_bytes` *and*
     `mtime_unix`) skips expensive work — music reuses the stored checksum
     (`resolveTrackChecksum`); TV skips the ffprobe but still refreshes the
     cheap, filename-derived identity.
4. **Move-relink** *(before prune — see §5)* — transfer irreplaceable per-file
   state from an orphaned old row to its moved-to new row.
5. **Prune** — for each row whose `abs_path` is under root and fails `os.Stat`,
   `DELETE`. FK `ON DELETE CASCADE` removes dependent rows.
6. **Cleanup** *(music only)* — delete albums with zero tracks, then artists
   with zero albums/tracks (`cleanupEmptyAlbums`).

**Matching is a separate job from scanning.** Scanners never call external APIs;
they only parse filenames/tags and probe streams. Matchers
(`RunMusicMatch` / `RunTVMatch`) run later and write match decisions.

---

## 3. Music

### Path & ownership
- **File row**: `music_tracks`. Holds `abs_path`, `mime_type`,
  `file_size_bytes`, `mtime_unix`, `checksum_sha256`, plus tag-derived
  `title`/`track_no`/`disc_no` and FKs to album/artist.
- **Hierarchy**: `music_artists` → `music_albums` → `music_tracks`.
  Match identity (`musicbrainz_id`, `match_status`, `match_confidence`,
  `art_path`, `art_checked_at`) lives on the **album/artist** rows, **not** the
  track.

### Identity keys (what makes "the same" thing)
- Track: `UNIQUE(library_id, abs_path)`.
- Album: `UNIQUE(library_id, artist_id, title, year)` — **re-derived from tags**.
- Artist: `UNIQUE(library_id, name)` — **re-derived from tags**.

### Scan logic (`internal/scan/scanner.go`)
- `ScanMusic` → walk → `ScanFile` per file → `finalizeCompilations` →
  `relinkMovedTracks` → `pruneMissingTracks` → `cleanupEmptyAlbums`.
- `ScanFile` reads tags (`music.ReadTrackMeta`), `ensureArtist` /`ensureAlbum`
  (conflict-update touches only `album_artist_id`/`is_compilation` — it **never**
  overwrites curated album fields), then upserts the track.
- `finalizeCompilations` infers a compilation only when **no single track-artist
  holds a strict majority** of the album's tracks (so a mis-tagged single-artist
  album with an outlier track is *not* promoted to "Various Artists"), and is
  **collision-safe**: when a `(Various Artists, title, year)` album already
  exists it merges into it (or drops it if it's an empty orphan, preserving the
  candidate's match/art) instead of a blind reparent that would hit the
  `UNIQUE(library_id, artist_id, title, year)` constraint and abort the scan.
- Per-track tag edits (`/music/track/edit`, the **Edit** button on each track
  row) write the file's tags via `WriteTrackTags` then `ScanFiles`, so a track
  whose Album/Album Artist/Year changed is re-derived onto a different album row
  and its old album is GC'd if it empties — same tag-is-truth path as a rescan.
- `resolveTrackChecksum` reuses the stored SHA-256 when size+mtime are unchanged;
  otherwise re-hashes file contents.

### Match logic (`internal/match`)
- `RunMusicMatch` = enrich artists → match `''`/`unmatched` albums →
  `refetchMissingArt` (cover-art-only third pass for already-`matched` albums
  lacking `art_path`, gated by a 30-day `art_checked_at` TTL).
- Threshold `matchThreshold = 80`. Art writers are **empty-only-guarded**, so a
  manual cover survives rescan/rematch.

### Move/rename behavior
- A move = old row pruned + new row inserted. Tag-derived album/artist grouping
  **re-attaches automatically** (same tags → same album row, which is never
  emptied because the new track is inserted before cleanup).
- **Irreplaceable on move**: `play_history` (FK `track_id`, cascade-deleted).
- **Re-derivable**: `lyrics_cache` (FK `track_id`; re-fetched from LRCLIB on next
  request — only the saved round-trip / negative-cache is lost).
- **Mitigation** (`relinkMovedTracks`): on a 1:1 content-signature match
  `(file_size_bytes, checksum_sha256)`, re-points `play_history` + `lyrics_cache`
  to the new track before prune. Empty checksum never matches.

### Derived artifacts
- Album/artist art under `DataDir/thumbs/music`, referenced by `art_path`.
  - Embedded art filename: `sha1("lib=<id> artist=<id> album=<id>")`.
  - Cover Art Archive filename: `sha1("caa-" + releaseGroupID)`.
  - Manual upload (`POST /music/album/art`): **album-id-keyed**, survives rescan.
- `lyrics_cache` (DB) keyed by `(track_id, provider_key)`; caches hits **and**
  misses.

### Discovery caches (NOT a source of truth)
The **Top 100** card on the Playlists page (`/music/playlists`) owns no media
data — it surfaces chart facts and plays them from YouTube:
- **No tables.** The per-year "everything that charted that year" list is
  derived at view time from the runtime-fetched `internal/billboard` weekly grid
  (factual chart data, read-only) via `billboard.YearChart`. Nothing is
  persisted. (The retired week-by-week "Rediscover a Year" page and its
  `year_journeys`/`year_journey_items` build cache were dropped by a migration;
  the old `itunes_art` cover cache and `internal/itunes` package were removed
  with it — the orphaned `itunes_art` table is harmless leftover schema.)
- `youtube_lookups` (`query_key` PK) — caches song→YouTube videoId resolutions
  (hits and misses) for Top-100 playback (popout iframe + in-app). Pure cache.

These reconcile *onto* the library; the library never reads from or depends on
them.

---

## 4. TV

### Path & ownership
- **File row**: `tv_series_files`. Holds `abs_path`, `container`,
  `file_size_bytes`, `mtime_unix`, and probed `stream_info_json` (a marshaled
  `video.ProbeResult`). **No checksum column.**
- **Identity**: `tv_series_identities`, **1:1** with a file (PK `file_id`,
  `ON DELETE CASCADE`). Holds `status` (`matched`/`unmatched`/`skipped`),
  `provider`, `series_id` (TMDB id as text), `season_number`,
  `episode_numbers_csv`, `match_confidence`, `match_method`, `guessed_title`,
  `air_date`, and `year` (release year taken from the show **folder**, e.g.
  `Doctor Who (2023)` — 0 if none; the matcher groups by `(title, year)` and
  prefers the candidate whose first-air year matches, disambiguating reboots).
- **No relational series/season/episode model** — show/season/episode metadata
  is JSON blobs in `tv_series_metadata_cache`, keyed by `entity_key` strings
  (e.g. `show:123:season:1:episode:4`), survives file churn.

### Scan logic (`internal/tvscan/scanner.go`)
- `ScanTV` → walk → `upsertTVFile` (+ `upsertIdentity`) per file →
  `relinkMovedFiles` → `pruneMissingFiles`.
- `IdentifyFile` (`identify.go`) is **pure filename/folder parsing** (no I/O):
  SxE / N×M / folder-authoritative title / air-date. It runs on every scan,
  including unchanged files, so improved parsing reconverges identities.
- `upsertIdentity` guard: `ON CONFLICT(file_id) DO UPDATE ... WHERE status NOT IN
  ('matched','skipped')` — a re-scan of the **same path** never clobbers a
  confirmed match or a user skip. (This guard does **not** help a move, which is
  a *different* path and thus a new row.)

### Match logic (`internal/tmdb/matcher.go`)
- `RunTVMatch` processes `status='unmatched'` rows with a non-empty
  `guessed_title`, derives the match from the **filename-parsed** title/season/
  episode, accepts at score ≥ 0.80, writes the result back to
  `tv_series_identities`, and caches TMDB JSON + downloads art.
- **Auto-matches are re-derivable**: a moved/renamed file whose new name still
  parses to the same title/SxE is **re-matched automatically on the next match
  run**, using cached TMDB metadata (cache survives — it is keyed by
  `entity_key`/`tmdb_series_id`, not `file_id`).

### Manual state (NOT re-derivable)
- `tvMatchApprove` → `status='matched', match_method='manual'` with a
  user-chosen TMDB id (`internal/web/handlers_tv.go`).
- `tvMatchSkip` → `status='skipped'`.
- These are one-time POSTs keyed on `lower(guessed_title)`, **not persisted as
  rules** — a brand-new post-move row does not inherit them.

### Move/rename behavior
- A move = old row pruned + new row inserted (fresh `unmatched`), cascade-
  deleting the old identity + progress.
- **Auto-recovers**: an auto-derived match (next `RunTVMatch`), *if* the new
  filename (and, for directory-derived titles, the surrounding `Show/Season NN/`
  structure) still parses identically.
- **Irreplaceable on move**: `tv_playback_progress` (resume/watched), **manual
  TMDB corrections**, **manual skips**.
- **Mitigation** (`relinkMovedFiles` → `transferFileState`): on a 1:1
  content-signature match `(file_size_bytes, mtime_unix)` — which a plain `mv`
  preserves — copies the `matched`/`skipped` identity + `tv_playback_progress`
  to the new row before prune. An `unmatched` identity is left to re-derive. An
  mtime-rewriting move (`cp`, some sync tools) falls back to prune-and-recreate.

### Derived artifacts
- `tv_series_art` (`art_path`) keyed by
  `(art_type, tmdb_series_id, season_number, episode_number)`; files under
  `DataDir/thumbs/tv` with semantic names (`show_<id>_poster.jpg`,
  `show_<id>_season_<n>_poster.jpg`). Tied to files indirectly via
  `tv_series_identities.series_id`.
- HLS transcode cache under `DataDir/cache/tv-hls` — **not in the DB**; a
  content-addressed disk cache keyed by `sha256("<src>|<mtimeNano>|<size>|
  <maxHeight>")`, size/age-pruned independently. Editing the source orphans the
  old cache dir naturally.

---

## 5. Move/rename resilience (cross-cutting)

Because identity is path-only, a move/rename would otherwise prune-and-recreate
and drop per-file state the *file* itself doesn't carry. Each scanner runs a
**move-relink pass before prune**:

1. Partition the library's rows into **orphans** (path now missing on disk) and
   **survivors** (path present).
2. For each orphan, find survivors sharing its **content signature**.
3. Transfer the irreplaceable state **only when exactly one orphan and exactly
   one survivor share that signature** (strict 1:1 — ambiguous → no transfer, so
   duplicate-content files are never mis-linked).
4. Prune then deletes the orphan; the transferred state lives on the survivor.

| | Signature | Transfers |
|---|---|---|
| Music (`relinkMovedTracks`) | `(file_size_bytes, checksum_sha256)` | `play_history`, `lyrics_cache` |
| TV (`relinkMovedFiles` / `transferFileState`) | `(file_size_bytes, mtime_unix)` | `matched`/`skipped` `tv_series_identities`, `tv_playback_progress` |

**Known residual gap**: a TV move that rewrites `mtime` (`cp`, some sync tools)
is not detected → falls back to prune-and-recreate (auto-match re-derives, but
progress + manual state are lost). If mtime-rewriting reorganizes become common,
add a partial-hash (head+tail) signature column. See `pending.md`.

---

## 6. Movies

Movies are implemented end to end, mirroring TV with a flatter (no season/
episode) shape. Owners per concern:

- **File row** → `movie_files` (`UNIQUE(library_id, abs_path)`). Change signal =
  `(file_size_bytes, mtime_unix)` — **no content checksum** (no hashing of
  multi-GB video, like TV).
- **Match/identity** → stored **inline on `movie_files`** (`tmdb_id`,
  `match_status` ∈ `''`/`unmatched`/`matched`/`skipped`, `match_confidence`,
  `match_source`, `matched_at`), not a separate identities table. The scanner
  parses `guessed_title`/`year` from the path; the matcher owns the rest.
- **Match cache** → `movie_metadata_cache` (key `movie:%d`, single-column PK).
  **Artwork** → `movie_art` (`UNIQUE(tmdb_movie_id, art_type)`, types
  `poster`/`backdrop`; files in `thumbs/movies`). **Cast** → shared
  `people`+`credits` (`credits.media_type='movie'`); actor profile images live in
  the shared `thumbs/tv` dir, not `thumbs/movies`. **Playback/resume** →
  `movie_playback_progress` (PK `file_id`).
- **Scanner** = `internal/moviescan` (`ScanMovies`): a strict simplification of
  `tvscan` — same walk/probe/junk-skip (reuses `tvscan.IsJunkFile`/
  `IsJunkDirName`)/pathguard/relink/prune, minus the SxE cascade. Identity is a
  Title+Year parser (`ParseMovie`). Single-table upsert; the match columns are
  never written by the scanner.
- **Matcher** = `tmdb.RunMovieMatch` (`NewMovieMatcher`): groups unmatched files
  by `(title, year)`, `SearchMovie` → `pickBestMovie` (title similarity adjusted
  by release-**year** agreement — the disambiguator films need) → caches metadata,
  downloads poster+backdrop, `FetchMovieCast`, writes the inline match identity.
- **Move-relink** = built in from the start on the §5 pattern: signature
  `(file_size_bytes, mtime_unix)`, transfers the inline match columns +
  `movie_playback_progress` (only `matched`/`skipped`; `unmatched` re-derives from
  the new filename), strictly 1:1.

What survives rescan/rematch/move/delete follows the §7 rules: the match columns
and `movie_playback_progress` are `file_id`-keyed and lost on prune **unless** the
relink transfers them; `movie_art` is the thumbgc reference set for `thumbs/movies`.

---

## 6a. Media integrity / auto-repair (the one writer into MEDIA_ROOT)

`internal/integrity` is the **single exception** to "Hespera reconciles *toward*
disk, it does not mutate it." It detects and (for the losslessly-fixable kind)
repairs corrupt **video (TV + movies) and music** files in place:

- **Detection is a separate job, never in the scanner.** Scanners stay
  read-only (§0). A cheap check (`integrity_check`) is *chained after* a
  tv/movie scan and only visits files whose `integrity_status=''`; it examines
  **container** integrity (`-c copy -f null`, no decode → auto-repaired) **and
  audio** integrity (an `AudioGaps` packet scan → missing audio flagged). So new
  media is examined for both automatically on scan; only the expensive video
  full-decode is left to the opt-in deep tier. A deep check (`integrity_deep`, opt-in
  "Check integrity" button) examines **both streams** — a full decode for
  **video bitstream** corruption plus an **audio packet-gap scan** for **missing
  audio** — and flags either (data loss is unrepairable; the transcode
  silence-fills audio gaps for playback but the file stays damaged).
- **Repair = remux → verify → atomic replace.** A container-corrupt file is
  stream-copy remuxed (`ffmpeg -c copy`, lossless) to a same-directory hidden
  temp, verified (same stream count + duration within ±2s + a clean re-check),
  then **atomically renamed over the original**. A good original is never lost:
  the overwrite only happens for a verified-strictly-better remux; any failure
  discards the temp. Gated by the `integrity_autorepair` app-setting
  (default on) — off = detect/flag only, zero media writes.
- **After a repair, the file's derived facts are updated on the row** in the
  same step (`refreshAfterReplace`), so the scanner's change-detection fast-path
  (§2) doesn't see a spurious change and the DB stays consistent with disk —
  **table-aware**: video updates `(size, mtime, stream_info_json)`; **music
  recomputes `checksum_sha256`** (+ size, mtime), since the remux changed the
  bytes and a stale checksum would break a later move's relink (§5). Music also
  runs the full **decode** in the cheap scan-time tier (cheap for small audio
  files), so MP3 bitstream corruption — which never shows in the container — is
  flagged automatically on scan. The path
  is unchanged, so identity (`UNIQUE(library_id, abs_path)`) and all `file_id`-
  keyed state survive — no move-relink involved.
- **Per-file status** lives on `{tv_series_files, movie_files}.integrity_status`
  (`''` unchecked / `ok` / `repaired` / `flagged`), reset to `''` by the scanner
  upsert when size or mtime change (so a changed file is re-examined).
  `flagged` = unrepairable **bitstream** damage (data loss) or a remux that
  couldn't be safely applied.

## 6b. Local extras (bonus content)

Files inside a title's nested extras directory (`Extras`, `Featurettes`,
`Trailers`, `Behind The Scenes`, `Deleted Scenes`, `Interviews`, `Shorts` —
`tvscan.extrasDirs`; `Sample`/`Samples` stay junk-skipped) are ingested as
**playable extras**, not episodes/movies:

- **Ownership of the classification**: the **scanner** (`tvscan.ClassifyExtra`,
  shared by moviescan). Rows live in the normal file tables with `is_extra=1`,
  a filename-derived `extra_title`, and a dir-derived `extra_category`. Same
  nesting rule as junk dirs: only a *nested* extras dir counts — a top-level
  `Extras/` under the library root is a real title. On a per-series scoped scan
  the walked root IS the show folder, so a first-level `Extras/` counts there.
- **No metadata identity, ever.** TV extras keep the blank placeholder
  `tv_series_identities` row (`guessed_title=''` keeps them out of the matcher,
  review backlog, and needs-matching banner); movie extras keep
  `guessed_title=''`/`match_status=''`. Post-walk passes
  (`resetExtraIdentities`/`resetExtraMatches`) blank any stale identity/match on
  rows that predate their dir being recognized as extras. Because every browse/
  count/CW/search/stats query gates on `matched`, extras are invisible to all of
  them by construction.
- **Owning title is path-derived at render time** (`tvscan.ExtrasOwnerDir` — the
  folder containing the extras dir, season dirs roll up to the show folder), not
  stored — nothing goes stale on rematch/unmatch/move. The series/movie detail
  pages list extras under the title's folder(s); the players resolve the owner
  the same way for the heading + back link.
- **Participation**: probe + playback progress/resume **in** (both `file_id`-
  keyed, no changes needed); trickplay + integrity **out** (`is_extra=0` in
  their candidate queries — extras aren't title content for the corrupt pills,
  and sprites aren't worth ~15s/file); relink transfers progress like any file.

## 7. Rules for anyone touching media data

- **Never treat the DB as authoritative for existence.** If you need "what media
  exists," it derives from disk; the DB row may be stale until the next scan.
- **Route reads through the file row's identity** (`library_id, abs_path` for the
  file; the album/artist/identity rows for match data). Do not invent a parallel
  lookup that can drift from the canonical tables.
- **Preserve the empty-only-guard on art writers** — manual art (`art_path` set
  by album upload, or by the artist image picker `POST /music/artist/art` —
  provider pick or upload) must survive rescan/rematch; those writers set
  `art_path` unconditionally so the user's choice sticks.
- **Respect the `upsertIdentity` status guard** — a rescan must not clobber a
  `matched` or `skipped` TV identity.
- **Anything keyed by `file_id` / `track_id` is lost on prune** unless the
  move-relink pass transfers it. If you add new per-file user state, decide
  whether it belongs in the relink transfer (§5) and add it there.
- **Matchers may call external APIs and write match decisions; scanners may not.**
  Keep that separation — scanners are filename/tag/probe only.
- **Derived artifacts live under `DataDir`, never in `MediaRoot`.** Key them so
  they can be regenerated and so a source change orphans the stale artifact.

---

## Keeping this in sync

This doc is the human-facing companion to the code; the code is the ground truth.
When you change scan/match/prune/relink logic, the schema of any table in §1, or
the move/derived-artifact behavior, **update this doc in the same commit**.
Citations here use file + function names (stable) rather than line numbers
(which drift) — keep them that way.

---
phase: 02-logic-data-integrity-bugs
verified: 2026-03-05T17:30:00Z
status: passed
score: 5/5 must-haves verified
re_verification: false
---

# Phase 2: Logic & Data Integrity Bugs Verification Report

**Phase Goal:** Scanning, matching, and merging produce correct, deterministic results regardless of filesystem order or timing
**Verified:** 2026-03-05T17:30:00Z
**Status:** passed
**Re-verification:** No -- initial verification

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | Re-scanning a TV library preserves manually resolved episode identity (guessed_title, season, episode numbers) -- resolved files are not overwritten | VERIFIED | `internal/tvscan/scanner.go:184` -- ON CONFLICT DO UPDATE includes `WHERE status NOT IN ('resolved', 'skipped')`, ensuring resolved/skipped identity rows are never overwritten on rescan |
| 2 | Running the music scanner twice on the same library with different filesystem walk orders produces identical compilation detection results | VERIFIED | `internal/scan/scanner.go:296-377` -- `finalizeCompilations` runs post-scan after all files are processed, using `COUNT(DISTINCT artist_id) > 1` on the full track set. No mid-scan `detectCompilationByArtistDiversity` call exists (function removed). Per-file ScanFile uses only tag-embedded signals (`meta.IsCompilation`, AlbumArtist == "Various Artists") at line 156 |
| 3 | mergeAlbumVariants called mid-scan does not orphan tracks or corrupt album associations -- all tracks remain linked to a valid album | VERIFIED | Mid-scan `mergeAlbumVariants` has been completely removed. Album variant merging now runs once inside `finalizeCompilations` (line 358-373) after all tracks are inserted, eliminating the race condition. The function is deleted -- grep returns no matches |
| 4 | Approving a TV match enqueues metadata fetch through the job queue instead of spawning a detached goroutine | VERIFIED | `internal/web/handlers_tv.go:604-612` -- `h.jobs.Enqueue("tv_metadata_fetch", 0, "user", ...)` replaces the old `go func()` pattern. No `go func()` exists anywhere in `handlers_tv.go` (grep returns empty). The handler imports `isomedia/internal/jobs` (line 17) |
| 5 | Scanner errors on individual files are logged with the file path and error details, and scanning continues to the next file without silent data loss | VERIFIED | Music scanner: `internal/scan/scanner.go:81-85` -- ScanFile error increments `scanErrors`, logs with `slog.Warn("scan file error", "path", p, "err", err)`, then continues. Summary logged at line 100-101. TV scanner: `internal/tvscan/scanner.go:117-121` -- `upsertTVFile` error increments `scanErrors`, logs with `slog.Warn("tvscan file error", "path", resolvedPath, "err", err)`, then continues. Summary logged at line 135-136 |

**Score:** 5/5 truths verified

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `internal/tvscan/scanner.go` | TV identity upsert that skips resolved/skipped rows; ScanTV continues past per-file errors with logging | VERIFIED | Contains `WHERE status NOT IN ('resolved', 'skipped')` at line 184; `scanErrors` counter at line 57; `upsertTVFile` extracted as clean error boundary at line 144; error summary log at line 136 |
| `internal/web/handlers_tv.go` | tvMatchApprove using jobs.Enqueue instead of go func() | VERIFIED | `h.jobs.Enqueue("tv_metadata_fetch", ...)` at line 607; no detached goroutines; imports `isomedia/internal/jobs` at line 17 |
| `internal/scan/scanner.go` | Post-scan finalizeCompilations; tag-only compilation detection in ScanFile; ScanMusic continues past per-file errors | VERIFIED | `finalizeCompilations` defined at line 296, called from ScanMusic at line 105 and ScanFiles at line 226; per-file compilation uses only `meta.IsCompilation` and `AlbumArtist` at line 156; `scanErrors` counter at line 64; dead functions `detectCompilationByArtistDiversity` and `mergeAlbumVariants` removed |

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| `handlers_tv.go:tvMatchApprove` | `jobs.Service` | `h.jobs.Enqueue("tv_metadata_fetch", ...)` | WIRED | Line 607: call exists with correct job type string, libraryID=0 sentinel, and matcher.FetchShowMetadata executor closure |
| `scan/scanner.go:ScanMusic` | `scan/scanner.go:finalizeCompilations` | Called after WalkDir completes, before prune/cleanup | WIRED | Line 105: `s.finalizeCompilations(ctx, libraryID)` called after WalkDir loop and error summary, before `pruneMissingTracks` |
| `scan/scanner.go:finalizeCompilations` | Album variant merge SQL | Inline UPDATE music_tracks SET album_id | WIRED | Lines 360-373: merge runs inside finalizeCompilations loop after marking compilation, using `lower(title) = lower(?)` matching |
| `scan/scanner.go:ScanMusic` | `slog` | Logs per-file errors with path context and summary count | WIRED | Line 83: `slog.Warn("scan file error", "path", p, "err", err)`; Line 101: `slog.Warn("scan completed with errors", ...)` |
| `tvscan/scanner.go:ScanTV` | `slog` | Logs per-file errors with path context and summary count | WIRED | Line 120: `slog.Warn("tvscan file error", "path", resolvedPath, "err", err)`; Line 136: `slog.Warn("tvscan completed with errors", ...)` |
| `scan/scanner.go:ScanFiles` | `scan/scanner.go:finalizeCompilations` | Called after file loop, before cleanupEmptyAlbums | WIRED | Line 226: `s.finalizeCompilations(ctx, libraryID)` called in ScanFiles rescan path |

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|------------|-------------|--------|----------|
| BUG-01 | 02-01-PLAN | TV identity fields not overwritten on rescan for resolved files | SATISFIED | `WHERE status NOT IN ('resolved', 'skipped')` guard on ON CONFLICT DO UPDATE in tvscan/scanner.go:184 |
| BUG-02 | 02-02-PLAN | Compilation detection produces consistent results regardless of walk order | SATISFIED | `finalizeCompilations` post-scan pass queries full track set; `detectCompilationByArtistDiversity` removed; ScanFile uses tag-only signals |
| BUG-03 | 02-02-PLAN | mergeAlbumVariants does not corrupt album/track associations when run mid-scan | SATISFIED | Mid-scan merge eliminated entirely; variant merge runs once inside `finalizeCompilations` after all tracks inserted |
| ERR-03 | 02-01-PLAN | TV match approve uses job queue instead of detached goroutine | SATISFIED | `h.jobs.Enqueue("tv_metadata_fetch", ...)` in handlers_tv.go:607; no `go func()` exists in file |
| ERR-04 | 02-03-PLAN | Scanner per-file error handling is explicit -- no silent swallowing | SATISFIED | Both scanners increment `scanErrors`, log per-file with path+error, and log summary count at completion |

**Orphaned requirements:** None. All 5 requirements mapped to this phase (BUG-01, BUG-02, BUG-03, ERR-03, ERR-04) are accounted for in the plans and verified in code.

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| (none) | - | - | - | No TODO/FIXME/PLACEHOLDER/HACK comments found in any modified file. No empty implementations. No stub patterns detected. |

### Build and Test Status

- `go build ./...` -- passes (exit 0)
- `go vet ./...` -- passes (exit 0)
- `go test ./...` -- all 12 packages pass (exit 0)

### Commit Verification

All 5 implementation commits verified in git history:

| Commit | Description | Plan |
|--------|-------------|------|
| `c0ae701` | Guard TV identity upsert against overwriting resolved/skipped rows | 02-01 |
| `49ef1fd` | Replace detached goroutine with job queue enqueue in tvMatchApprove | 02-01 |
| `d5b59ff` | Remove mid-scan compilation detection and merge from ScanFile | 02-02 |
| `b4b9853` | Add post-scan finalizeCompilations pass | 02-02 |
| `90d3f6a` | Make ScanMusic continue past per-file errors | 02-03 |
| `8e35cf9` | Make ScanTV continue past per-file errors | 02-03 |

### Human Verification Required

None. All phase 2 changes are logic/data-integrity fixes verifiable through code inspection and automated tests. No UI changes, no visual behavior, no external service integration changes.

---

_Verified: 2026-03-05T17:30:00Z_
_Verifier: Claude (gsd-verifier)_

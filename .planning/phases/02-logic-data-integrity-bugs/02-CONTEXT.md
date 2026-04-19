# Phase 2: Logic & Data Integrity Bugs - Context

**Gathered:** 2026-03-05
**Status:** Ready for planning

<domain>
## Phase Boundary

Fix five specific bugs that cause incorrect, non-deterministic, or silently lost data during scanning, matching, and merging. Requirements: BUG-01, BUG-02, BUG-03, ERR-03, ERR-04. No new features, no refactoring beyond what's needed for fixes.

</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion

User chose to skip discussion -- all implementation decisions are Claude's discretion. Key areas:

**BUG-01 — TV rescan identity preservation:**
- Which statuses to protect from overwrite (resolved, skipped, or both)
- Whether to use ON CONFLICT condition or pre-check query

**BUG-02 — Compilation detection determinism:**
- Whether to fix mid-scan ordering or move compilation detection to a post-scan pass
- How to ensure same result regardless of filesystem walk order

**BUG-03 — mergeAlbumVariants safety:**
- Whether to keep mid-scan merging with safety guards, or defer to post-scan
- How to prevent orphaned tracks/albums during merge

**ERR-03 — TV match approve goroutine:**
- Route through jobs.Service instead of detached goroutine
- Job type naming and error handling approach

**ERR-04 — Scanner per-file error handling:**
- Whether to add error counts to job status, per-file error tracking, or keep log-only with better detail
- How to ensure scanning continues past individual file errors without silent data loss

</decisions>

<specifics>
## Specific Ideas

No specific requirements -- open to standard approaches.

</specifics>

<code_context>
## Existing Code Insights

### Reusable Assets
- `httpError`/`jsonErr` helpers (from Phase 1): Use for any HTTP response changes in ERR-03
- `jobs.Service` with `Enqueue(jobType, libraryID, createdBy, executor)`: Target for ERR-03 detached goroutine fix
- `tmdb.NewMatcher(db, apiKey, dataDir)`: Already instantiated in tvMatchApprove, needs to be wrapped as executor closure

### Established Patterns
- Scanner uses `slog.Warn` + `return nil` for per-file errors (scan/scanner.go:108-110, 113-114, 127-128)
- TV scanner uses ON CONFLICT DO UPDATE for upserts (tvscan/scanner.go:117-126, 147-156)
- Compilation detection queries existing tracks mid-transaction (scan/scanner.go:289-308)
- mergeAlbumVariants moves tracks to canonical album via UPDATE (scan/scanner.go:310-324)
- Jobs use executor closures: `func(ctx, jobID, libID) error` pattern

### Integration Points
- `tvscan/scanner.go:147-156` — TV identity upsert ON CONFLICT clause (BUG-01)
- `scan/scanner.go:142-173` — Compilation detection + merge flow (BUG-02, BUG-03)
- `scan/scanner.go:310-324` — mergeAlbumVariants SQL (BUG-03)
- `web/handlers_tv.go:608-614` — Detached goroutine (ERR-03)
- `scan/scanner.go:80-81, 104-129` — ScanMusic/ScanFile error returns (ERR-04)
- `tvscan/scanner.go:57-99` — TV scan per-file error handling (ERR-04)

</code_context>

<deferred>
## Deferred Ideas

None -- discussion stayed within phase scope.

</deferred>

---

*Phase: 02-logic-data-integrity-bugs*
*Context gathered: 2026-03-05*

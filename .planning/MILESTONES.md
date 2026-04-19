# Milestones

## v1.2 TV Auto-Match Pipeline (Shipped: 2026-03-07)

**Phases completed:** 2 phases, 3 plans, 5 tasks
**Requirements:** 6/6 satisfied
**Audit:** passed (16/16 must-haves, 8/8 integration, 3/3 E2E flows)

**Key accomplishments:**
- Raised TV auto-match threshold from 0.45 to 0.80 for confidence-driven auto-accept
- Aligned TV status model with music pipeline (matched/unmatched/skipped), replacing resolved/needs_fix
- DB migration converting existing rows with table recreation pattern for CHECK constraint changes
- Unit tests for pickBestResult scoring (7 subtests covering boundaries, popularity cap, empty input)
- Integration test proving below-threshold matches stay unmatched without caching or art download
- Handler tests for TV match review UI (7 subtests covering approve/skip/review with new status model)

---

## v1.1 Automated Music Match Pipeline (Shipped: 2026-03-07)

**Phases completed:** 3 phases, 3 plans, 0 tasks

**Key accomplishments:**
- (none recorded)

---

## v1.0 Codebase Audit & Hardening (Shipped: 2026-03-06)

**Phases completed:** 5 phases, 13 plans, 24 tasks
**Requirements:** 20/20 satisfied
**Audit:** passed (40/40 must-haves, 12/12 integration, 6/6 E2E flows)

**Key accomplishments:**
- Eliminated all internal error leakage -- 109 raw err.Error() calls replaced with httpError/jsonErr helpers, zero internal details in HTTP responses
- Hardened ensureColumn with identifier validation regexp to prevent SQL injection through schema evolution
- Fixed TV rescan identity overwrite (WHERE guard on resolved/skipped rows), compilation detection ordering (post-scan finalization), and album merge corruption (removed mid-scan merge)
- Replaced detached goroutine in TV match approve with job queue enqueue for safe background processing
- Added per-file error resilience to both music and TV scanners with structured logging and continuation
- Consolidated URL path ID parsing into shared pathID/pathSegment helpers, replacing 7 inline parsing blocks
- Added template startup validation with fail-fast -- missing or broken templates produce clear errors at startup
- Added 49+ unit test subtests across scanner (music + TV), handler (music, TV, settings), and compilation detection
- Added integration tests for music match pipeline (RunMusicMatch with mocked MusicBrainz/Wikipedia/Wikidata/Wikimedia/CAA) and TV match pipeline (RunTVMatch with mocked TMDB)

---


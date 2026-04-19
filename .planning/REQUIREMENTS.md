# Requirements: Isomedia Codebase Audit & Hardening

**Defined:** 2026-03-05
**Core Value:** Every identified bug and logic flaw is fixed and verified -- no known issues remain

## v1 Requirements

Requirements for this milestone. Each maps to roadmap phases.

### Logic Bugs

- [x] **BUG-01**: TV identity fields (guessed_title, season_number, episode_numbers_csv) are not overwritten on rescan for files that have already been resolved
- [x] **BUG-02**: Compilation detection produces consistent results regardless of filesystem walk order
- [x] **BUG-03**: mergeAlbumVariants does not corrupt album/track associations when run mid-scan

### Error Handling

- [x] **ERR-01**: All HTTP handlers return generic user-facing error messages instead of raw Go error strings
- [x] **ERR-02**: All internal errors are logged server-side with slog before returning generic HTTP errors
- [x] **ERR-03**: TV match approve uses the job queue instead of a detached goroutine for background metadata fetch
- [ ] **ERR-04**: Scanner per-item error handling is explicit -- no silent swallowing of errors that could affect data integrity

### Security

- [x] **SEC-01**: ensureColumn validates table and column names against a safe pattern before building SQL
- [x] **SEC-02**: HTTP error responses never contain SQL error text, filesystem paths, or internal Go error details

### Fragility Fixes

- [ ] **FRAG-01**: URL path ID parsing uses a shared helper function instead of duplicated code across 6+ handlers
- [ ] **FRAG-02**: Template registration validates at startup that all handler-referenced templates exist
- [ ] **FRAG-03**: Missing or broken templates produce a clear startup error, not a runtime 500

### Test Coverage -- Scanner

- [ ] **TEST-01**: Music scanner ScanFile() has unit tests covering tag reading, artist/album/track upsert, and art extraction
- [ ] **TEST-02**: Music scanner compilation detection has tests covering mixed-artist albums, "Various Artists", and re-scan scenarios
- [ ] **TEST-03**: TV scanner ScanTV() has tests covering file identification, upsert, and rescan behavior

### Test Coverage -- Handlers

- [ ] **TEST-04**: Music handler tests verify routing, ID parsing, and error responses for key endpoints
- [ ] **TEST-05**: TV handler tests verify routing, ID parsing, and error responses for key endpoints
- [ ] **TEST-06**: Settings handler tests verify library CRUD and scan trigger endpoints

### Test Coverage -- Pipeline

- [ ] **TEST-07**: Music match pipeline RunMusicMatch() has integration tests covering the full match-score-art-enrich flow
- [ ] **TEST-08**: TMDB matcher RunTVMatch() has integration tests covering search, metadata fetch, and art download

## v2 Requirements

Deferred to future milestones.

### Architecture Improvements

- **ARCH-01**: Extract data access into internal/store/ package to eliminate inline SQL duplication
- **ARCH-02**: Decompose large handler files into focused modules
- **ARCH-03**: Add migration versioning with schema_migrations table

### Performance

- **PERF-01**: Add pagination to music home page artist/compilation lists
- **PERF-02**: Eliminate double directory walk in scanners
- **PERF-03**: Add COLLATE NOCASE or expression indexes for case-insensitive queries

### Features

- **FEAT-01**: Implement isocli user/key management commands
- **FEAT-02**: Movie scanning and matching pipeline
- **FEAT-03**: User management web UI

## Out of Scope

| Feature | Reason |
|---------|--------|
| Store layer extraction | Architectural improvement, not a bug fix |
| Multi-worker job queue | Scaling improvement, not needed for personal server |
| Database migration to client-server DB | Over-engineering for single-user deployment |
| Performance optimization (indexes, caching) | Separate concern from correctness |

## Traceability

| Requirement | Phase | Status |
|-------------|-------|--------|
| BUG-01 | Phase 2 | Complete |
| BUG-02 | Phase 2 | Complete |
| BUG-03 | Phase 2 | Complete |
| ERR-01 | Phase 1 | Complete |
| ERR-02 | Phase 1 | Complete |
| ERR-03 | Phase 2 | Complete |
| ERR-04 | Phase 2 | Pending |
| SEC-01 | Phase 1 | Complete |
| SEC-02 | Phase 1 | Complete |
| FRAG-01 | Phase 3 | Pending |
| FRAG-02 | Phase 3 | Pending |
| FRAG-03 | Phase 3 | Pending |
| TEST-01 | Phase 4 | Pending |
| TEST-02 | Phase 4 | Pending |
| TEST-03 | Phase 4 | Pending |
| TEST-04 | Phase 4 | Pending |
| TEST-05 | Phase 4 | Pending |
| TEST-06 | Phase 4 | Pending |
| TEST-07 | Phase 5 | Pending |
| TEST-08 | Phase 5 | Pending |

**Coverage:**
- v1 requirements: 20 total
- Mapped to phases: 20
- Unmapped: 0

---
*Requirements defined: 2026-03-05*
*Last updated: 2026-03-05 after roadmap creation*

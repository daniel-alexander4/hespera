# Requirements: Isomedia

**Defined:** 2026-03-06
**Core Value:** A personal media server that just works -- reliable scanning, matching, and streaming with no external service dependencies at runtime.

## v1.1 Requirements

Requirements for automated music match pipeline. Each maps to roadmap phases.

### Matching

- [ ] **MATCH-01**: Scanner triggers MusicBrainz search using only artist name, album name, and track name from file tags
- [ ] **MATCH-02**: Best match scoring 80% or higher is automatically accepted (highest score wins)
- [ ] **MATCH-03**: Songs below 80% match threshold are silently skipped with no flagging or queuing

### Writeback

- [ ] **WRITE-01**: Artist MBID and album MBID are written to audio file metadata on auto-match
- [ ] **WRITE-02**: Normalized artist, album, and track names from MusicBrainz are written back to file tags
- [ ] **WRITE-03**: Tag writeback happens automatically as part of the match pipeline, not as a separate step

### Enrichment

- [ ] **ENRICH-01**: Cover art is fetched from Cover Art Archive on auto-match
- [ ] **ENRICH-02**: Artist bio (Wikipedia) and artist image (Wikimedia Commons) are fetched on auto-match

### UI

- [ ] **UI-01**: Existing match review UI remains functional for manually matching songs that didn't auto-match

## Future Requirements

None -- milestone is tightly scoped to automated match pipeline.

## Out of Scope

| Feature | Reason |
|---------|--------|
| Web UI file upload | Files arrive via filesystem; scanner detects them |
| Movie scanning/matching | Separate milestone |
| Flagging/queuing unmatched songs | User wants silent skip below 80% |
| Removing match review UI | Kept for manual fallback |
| New match scoring algorithm | Existing scorer works; only threshold changes |

## Traceability

Which phases cover which requirements. Updated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| MATCH-01 | -- | Pending |
| MATCH-02 | -- | Pending |
| MATCH-03 | -- | Pending |
| WRITE-01 | -- | Pending |
| WRITE-02 | -- | Pending |
| WRITE-03 | -- | Pending |
| ENRICH-01 | -- | Pending |
| ENRICH-02 | -- | Pending |
| UI-01 | -- | Pending |

**Coverage:**
- v1.1 requirements: 9 total
- Mapped to phases: 0
- Unmapped: 9

---
*Requirements defined: 2026-03-06*
*Last updated: 2026-03-06 after initial definition*

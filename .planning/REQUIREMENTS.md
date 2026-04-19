# Requirements: Isomedia v1.3 Manual Controls

**Defined:** 2026-03-07
**Core Value:** A personal media server that just works -- reliable scanning, matching, and streaming with no external service dependencies at runtime.

## v1.3 Requirements

Requirements for this milestone. Each maps to roadmap phases.

### Track Editing

- [ ] **TRCK-01**: User can edit track number via album edit UI and have it saved to database
- [ ] **TRCK-02**: User can edit track title via album edit UI (non-writeback mode) and have it saved to database

### Artwork Management

- [ ] **ART-01**: User can upload cover art image for an album
- [ ] **ART-02**: User can replace existing album cover art with a new upload
- [ ] **ART-03**: User can delete album cover art
- [ ] **ART-04**: User sees a preview of the image before confirming upload

### Manual Match Selection

- [ ] **MTCH-01**: User can search MusicBrainz for match candidates from the music match review page
- [ ] **MTCH-02**: User can select a MusicBrainz candidate to apply as the album match regardless of score
- [ ] **MTCH-03**: Selecting a match triggers the full post-match pipeline (CAA art, normalization, tag writeback)
- [ ] **MTCH-04**: User can see score breakdown next to each candidate in the search results

## Future Requirements

### Artwork Enhancements

- **ART-05**: User can upload artist images (same pattern as album art)
- **ART-06**: User can drag-and-drop artwork onto album page

### Match Enhancements

- **MTCH-05**: Match candidate results are cached to avoid repeated API calls
- **MTCH-06**: User can manually match TV series (already implemented in v1.2)

### Bulk Operations

- **BULK-01**: User can renumber all tracks in an album at once

## Out of Scope

| Feature | Reason |
|---------|--------|
| URL-based artwork fetch | SSRF risk, scope creep |
| Full metadata editor (all ID3 fields) | Scope creep -- point users to MusicBrainz Picard |
| TV manual match selection | Already implemented in v1.2 |
| TV tag writeback | Video files don't have writable metadata tags |
| Orphaned art file cleanup | Pre-existing tech debt, defer to utility task |

## Traceability

Which phases cover which requirements. Updated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| TRCK-01 | Phase 11 | Pending |
| TRCK-02 | Phase 11 | Pending |
| ART-01 | Phase 12 | Pending |
| ART-02 | Phase 12 | Pending |
| ART-03 | Phase 12 | Pending |
| ART-04 | Phase 12 | Pending |
| MTCH-01 | Phase 13 | Pending |
| MTCH-02 | Phase 13 | Pending |
| MTCH-03 | Phase 13 | Pending |
| MTCH-04 | Phase 13 | Pending |

**Coverage:**
- v1.3 requirements: 10 total
- Mapped to phases: 10
- Unmapped: 0

---
*Requirements defined: 2026-03-07*
*Last updated: 2026-03-07 after roadmap creation*

# Roadmap: Isomedia

## Milestones

- ✅ **v1.0 Codebase Audit & Hardening** -- Phases 1-5 (shipped 2026-03-06)
- ✅ **v1.1 Automated Music Match Pipeline** -- Phases 6-8 (shipped 2026-03-07)
- ✅ **v1.2 TV Auto-Match Pipeline** -- Phases 9-10 (shipped 2026-03-07)
- **v1.3 Manual Controls** -- Phases 11-13 (in progress)

## Phases

<details>
<summary>✅ v1.0 Codebase Audit & Hardening (Phases 1-5) -- SHIPPED 2026-03-06</summary>

- [x] Phase 1: Security & Error Exposure (2/2 plans) -- completed 2026-03-05
- [x] Phase 2: Logic & Data Integrity Bugs (3/3 plans) -- completed 2026-03-05
- [x] Phase 3: Fragility Elimination (2/2 plans) -- completed 2026-03-05
- [x] Phase 4: Unit Test Coverage (4/4 plans) -- completed 2026-03-05
- [x] Phase 5: Integration Test Coverage (2/2 plans) -- completed 2026-03-06

See: `.planning/milestones/v1.0-ROADMAP.md` for full details.

</details>

<details>
<summary>✅ v1.1 Automated Music Match Pipeline (Phases 6-8) -- SHIPPED 2026-03-07</summary>

- [x] Phase 6: Auto-Match Pipeline (1/1 plans) -- completed 2026-03-06
- [x] Phase 7: Automated Writeback (1/1 plans) -- completed 2026-03-07
- [x] Phase 8: Enrichment and UI Preservation (1/1 plans) -- completed 2026-03-07

See: `.planning/milestones/v1.1-ROADMAP.md` for full details.

</details>

<details>
<summary>✅ v1.2 TV Auto-Match Pipeline (Phases 9-10) -- SHIPPED 2026-03-07</summary>

- [x] Phase 9: TV Match Threshold and Status Alignment (1/1 plans) -- completed 2026-03-07
- [x] Phase 10: TV Match Test Coverage (2/2 plans) -- completed 2026-03-07

See: `.planning/milestones/v1.2-ROADMAP.md` for full details.

</details>

### v1.3 Manual Controls (In Progress)

**Milestone Goal:** Add manual artwork upload, fix track metadata editing, and enable manual match selection for music albums.

- [ ] **Phase 11: Track Editing Fix** - Fix track number and title editing in album edit UI
- [ ] **Phase 12: Album Artwork Management** - Upload, replace, and delete album cover art
- [ ] **Phase 13: Manual Match Selection** - Search and select MusicBrainz matches for unmatched albums

## Phase Details

### Phase 11: Track Editing Fix
**Goal**: Users can edit track metadata through the album edit UI and have changes persist
**Depends on**: Nothing (independent feature fix)
**Requirements**: TRCK-01, TRCK-02
**Success Criteria** (what must be TRUE):
  1. User can change a track's number in the album edit UI and see the updated number after saving
  2. User can change a track's title in the album edit UI (non-writeback mode) and see the updated title after saving
  3. Existing album-level edit fields (album title, artist) continue to work as before
**Plans**: TBD

Plans:
- [ ] 11-01: TBD

### Phase 12: Album Artwork Management
**Goal**: Users can manage album cover art without depending on Cover Art Archive
**Depends on**: Nothing (independent feature)
**Requirements**: ART-01, ART-02, ART-03, ART-04
**Success Criteria** (what must be TRUE):
  1. User can upload a JPEG or PNG image as album cover art and see it displayed on the album page
  2. User can replace existing album cover art with a new image
  3. User can delete album cover art, reverting to the placeholder
  4. User sees a preview of the selected image before confirming the upload
**Plans**: TBD

Plans:
- [ ] 12-01: TBD

### Phase 13: Manual Match Selection
**Goal**: Users can manually select MusicBrainz matches for albums that auto-match failed to resolve
**Depends on**: Nothing (independent feature, but builds on existing match pipeline patterns)
**Requirements**: MTCH-01, MTCH-02, MTCH-03, MTCH-04
**Success Criteria** (what must be TRUE):
  1. User can search MusicBrainz for match candidates from the music match review page
  2. User can see score breakdowns next to each candidate in the search results
  3. User can select a candidate to apply as the album match, triggering art download, normalization, and tag writeback
  4. After manual match selection, the album displays matched metadata and cover art
**Plans**: TBD

Plans:
- [ ] 13-01: TBD

## Progress

**Execution Order:**
Phases execute in numeric order: 11 -> 12 -> 13

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|----------------|--------|-----------|
| 1. Security & Error Exposure | v1.0 | 2/2 | Complete | 2026-03-05 |
| 2. Logic & Data Integrity Bugs | v1.0 | 3/3 | Complete | 2026-03-05 |
| 3. Fragility Elimination | v1.0 | 2/2 | Complete | 2026-03-05 |
| 4. Unit Test Coverage | v1.0 | 4/4 | Complete | 2026-03-05 |
| 5. Integration Test Coverage | v1.0 | 2/2 | Complete | 2026-03-06 |
| 6. Auto-Match Pipeline | v1.1 | 1/1 | Complete | 2026-03-06 |
| 7. Automated Writeback | v1.1 | 1/1 | Complete | 2026-03-07 |
| 8. Enrichment and UI Preservation | v1.1 | 1/1 | Complete | 2026-03-07 |
| 9. TV Match Threshold and Status Alignment | v1.2 | 1/1 | Complete | 2026-03-07 |
| 10. TV Match Test Coverage | v1.2 | 2/2 | Complete | 2026-03-07 |
| 11. Track Editing Fix | v1.3 | 0/? | Not started | - |
| 12. Album Artwork Management | v1.3 | 0/? | Not started | - |
| 13. Manual Match Selection | v1.3 | 0/? | Not started | - |

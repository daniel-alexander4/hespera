# Roadmap: Isomedia

## Milestones

- ✅ **v1.0 Codebase Audit & Hardening** -- Phases 1-5 (shipped 2026-03-06)
- 🚧 **v1.1 Automated Music Match Pipeline** -- Phases 6-8 (in progress)

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

### v1.1 Automated Music Match Pipeline

**Milestone Goal:** Automate the music metadata matching and writeback flow so scanned songs are automatically matched, enriched, and tagged without manual intervention.

- [ ] **Phase 6: Auto-Match Pipeline** - Scanner triggers MusicBrainz matching with 80% auto-accept threshold and silent skip
- [ ] **Phase 7: Automated Writeback** - Auto-accepted matches write MBIDs and normalized names to file tags inline
- [ ] **Phase 8: Enrichment and UI Preservation** - Auto-match triggers full enrichment; manual review UI remains functional

## Phase Details

### Phase 6: Auto-Match Pipeline
**Goal**: Songs are automatically matched against MusicBrainz during scan with confident matches accepted and low-confidence songs silently skipped
**Depends on**: Phase 5 (v1.0 complete)
**Requirements**: MATCH-01, MATCH-02, MATCH-03
**Success Criteria** (what must be TRUE):
  1. Running a music scan automatically triggers MusicBrainz matching for unmatched albums using artist name and album name from file tags
  2. Albums scoring 80% or higher are automatically accepted with match_status set accordingly and the highest-scoring candidate chosen
  3. Albums scoring below 80% are left with their original metadata -- no error state, no flag, no queue entry
  4. Previously matched or manually matched albums are not re-matched on subsequent scans
**Plans:** 1 plan

Plans:
- [ ] 06-01-PLAN.md -- Raise threshold to 80%, eliminate uncertain status, update review UI

### Phase 7: Automated Writeback
**Goal**: Auto-accepted matches immediately write MusicBrainz identifiers and normalized metadata back to audio file tags
**Depends on**: Phase 6
**Requirements**: WRITE-01, WRITE-02, WRITE-03
**Success Criteria** (what must be TRUE):
  1. After auto-accept, artist MBID and album MBID are present in the audio file's metadata tags (verifiable by reading the file with a tag reader)
  2. After auto-accept, artist name, album name, and track name in file tags reflect the normalized MusicBrainz values
  3. Tag writeback occurs as part of the match pipeline execution -- there is no separate "writeback" job or manual trigger required for auto-matched albums
**Plans:** 1 plan

Plans:
- [x] 07-01-PLAN.md -- Normalize names in DB on match, inline per-album tag writeback in pipeline

### Phase 8: Enrichment and UI Preservation
**Goal**: Auto-matched albums receive full enrichment (cover art, artist bio, artist image) and the manual review UI continues to work for songs that did not auto-match
**Depends on**: Phase 7
**Requirements**: ENRICH-01, ENRICH-02, UI-01
**Success Criteria** (what must be TRUE):
  1. After auto-match, Cover Art Archive artwork is fetched and stored for the album (visible on album page)
  2. After auto-match, artist bio from Wikipedia and artist image from Wikimedia Commons are fetched and stored (visible on artist page)
  3. User can navigate to the match review UI and manually match an album that was silently skipped by auto-match
  4. Manually triggering a match from the review UI still performs writeback and enrichment as before
**Plans**: TBD

Plans:
- [ ] 08-01: TBD

## Progress

**Execution Order:**
Phases execute in numeric order: 6 -> 7 -> 8

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|----------------|--------|-----------|
| 1. Security & Error Exposure | v1.0 | 2/2 | Complete | 2026-03-05 |
| 2. Logic & Data Integrity Bugs | v1.0 | 3/3 | Complete | 2026-03-05 |
| 3. Fragility Elimination | v1.0 | 2/2 | Complete | 2026-03-05 |
| 4. Unit Test Coverage | v1.0 | 4/4 | Complete | 2026-03-05 |
| 5. Integration Test Coverage | v1.0 | 2/2 | Complete | 2026-03-06 |
| 6. Auto-Match Pipeline | v1.1 | 1/1 | Complete | 2026-03-06 |
| 7. Automated Writeback | v1.1 | 1/1 | Complete | 2026-03-07 |
| 8. Enrichment and UI Preservation | v1.1 | 0/? | Not started | - |

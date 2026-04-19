# Roadmap: Isomedia

## Milestones

- ✅ **v1.0 Codebase Audit & Hardening** -- Phases 1-5 (shipped 2026-03-06)
- ✅ **v1.1 Automated Music Match Pipeline** -- Phases 6-8 (shipped 2026-03-07)
- 🚧 **v1.2 TV Auto-Match Pipeline** -- Phases 9-10 (in progress)

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

### v1.2 TV Auto-Match Pipeline (In Progress)

**Milestone Goal:** Automate TV matching the same way v1.1 automated music -- scan triggers TMDB matching, high-confidence results auto-accepted, below-threshold skipped for manual review.

- [x] **Phase 9: TV Match Threshold and Status Alignment** - Raise auto-accept threshold to 0.80, align status model with music pipeline (matched/unmatched), preserve manual review for below-threshold (completed 2026-03-07)
- [ ] **Phase 10: TV Match Test Coverage** - Unit tests for scoring/threshold, integration tests for auto-match pipeline, tests for review UI with new status model

## Phase Details

### Phase 9: TV Match Threshold and Status Alignment
**Goal**: TV matches use the same confidence-driven auto-accept model as music -- high-confidence matches are accepted automatically, below-threshold matches remain available for manual review
**Depends on**: Phase 8 (v1.1 complete)
**Requirements**: MATCH-01, MATCH-02, MATCH-03
**Success Criteria** (what must be TRUE):
  1. Running a TV scan with high-confidence TMDB results (score >= 0.80) auto-accepts the match and applies TMDB metadata (poster art, episode data) without user intervention
  2. Running a TV scan with below-threshold TMDB results (score < 0.80) leaves the series as 'unmatched' and visible in the manual review UI
  3. TV match status values use matched/unmatched (aligned with the music pipeline model), replacing any prior status terminology
  4. Existing manual match review UI continues to work -- user can still manually approve or reject matches for below-threshold series
**Plans:** 1/1 plans complete

Plans:
- [ ] 09-01-PLAN.md -- Threshold raise to 0.80, status rename (resolved->matched, needs_fix->unmatched), DB migration, handler/template/test updates

### Phase 10: TV Match Test Coverage
**Goal**: TV auto-match behavior is verified by automated tests covering scoring logic, end-to-end pipeline flow, and UI integration with the new status model
**Depends on**: Phase 9
**Requirements**: TEST-01, TEST-02, TEST-03
**Success Criteria** (what must be TRUE):
  1. Unit tests exercise TV match scoring and verify that scores above 0.80 produce auto-accept and scores below 0.80 produce unmatched status
  2. Integration tests run the full TV auto-match pipeline (scan to match) with mocked TMDB and verify correct auto-accept/skip behavior end-to-end
  3. Tests verify the match review UI renders correctly with the new matched/unmatched status model and that manual review actions work
**Plans**: TBD

## Progress

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
| 9. TV Match Threshold and Status Alignment | 1/1 | Complete   | 2026-03-07 | - |
| 10. TV Match Test Coverage | v1.2 | 0/? | Not started | - |

# Requirements: Isomedia

**Defined:** 2026-03-07
**Core Value:** A personal media server that just works

## v1.2 Requirements

Requirements for TV Auto-Match Pipeline. Each maps to roadmap phases.

### Matching

- [x] **MATCH-01**: TV auto-match uses 0.80 confidence threshold for auto-accept (up from 0.45)
- [x] **MATCH-02**: Below-threshold TV matches stay as 'unmatched' for manual review (not silently dropped)
- [x] **MATCH-03**: TV match status model uses matched/unmatched (aligned with music pipeline)

### Testing

- [ ] **TEST-01**: Unit tests for TV match scoring and threshold logic
- [ ] **TEST-02**: Integration tests for TV auto-match pipeline (auto-accept above threshold, skip below)
- [x] **TEST-03**: Tests verify match review UI works with new status model

## Future Requirements

None deferred.

## Out of Scope

| Feature | Reason |
|---------|--------|
| TV tag writeback | Video files don't have writable metadata tags like audio |
| Movie scanning/matching | Separate milestone |
| TMDB rate limiting improvements | Current approach works for personal server scale |
| TV title normalization | Not needed -- TMDB handles canonical names |

## Traceability

| Requirement | Phase | Status |
|-------------|-------|--------|
| MATCH-01 | Phase 9 | Complete |
| MATCH-02 | Phase 9 | Complete |
| MATCH-03 | Phase 9 | Complete |
| TEST-01 | Phase 10 | Pending |
| TEST-02 | Phase 10 | Pending |
| TEST-03 | Phase 10 | Complete |

**Coverage:**
- v1.2 requirements: 6 total
- Mapped to phases: 6
- Unmapped: 0

---
*Requirements defined: 2026-03-07*
*Last updated: 2026-03-07 after roadmap creation*

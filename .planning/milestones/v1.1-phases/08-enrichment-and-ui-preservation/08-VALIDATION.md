---
phase: 08
slug: enrichment-and-ui-preservation
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-03-07
---

# Phase 08 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test |
| **Config file** | none — standard Go testing |
| **Quick run command** | `go test ./internal/match/ -v -count=1` |
| **Full suite command** | `go test ./... -count=1` |
| **Estimated runtime** | ~15 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test ./internal/match/ -v -count=1`
- **After every plan wave:** Run `go test ./... -count=1`
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** 15 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|-----------|-------------------|-------------|--------|
| 08-01-01 | 01 | 1 | ENRICH-01, ENRICH-02 | integration | `go test ./internal/match/ -run "TestRunMusicMatch" -v -count=1` | existing | pending |
| 08-01-02 | 01 | 1 | UI-01 | unit+http | `go test ./internal/web/ -run "TestMatch" -v -count=1` | partial | pending |

*Status: pending / green / red / flaky*

---

## Wave 0 Requirements

Existing infrastructure covers all phase requirements. The match package already has integration test helpers (openTestDB, mock HTTP servers).

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Cover art visible on album page | ENRICH-01 | Requires browser rendering | Navigate to matched album page, verify artwork displays |
| Artist bio/image visible on artist page | ENRICH-02 | Requires browser rendering | Navigate to enriched artist page, verify bio and image display |
| Match review UI navigation | UI-01 | Requires browser interaction | Navigate to /music/match/review, verify unmatched albums listed with actions |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 15s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending

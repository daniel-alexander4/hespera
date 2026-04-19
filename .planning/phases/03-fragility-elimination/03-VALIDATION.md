---
phase: 3
slug: fragility-elimination
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-03-05
---

# Phase 3 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go stdlib `testing` (Go 1.23) |
| **Config file** | None needed -- Go conventions |
| **Quick run command** | `go test ./internal/web/... -count=1` |
| **Full suite command** | `go test ./... -count=1` |
| **Estimated runtime** | ~5 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test ./internal/web/... -count=1`
- **After every plan wave:** Run `go test ./... -count=1`
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** 5 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|-----------|-------------------|-------------|--------|
| 03-01-01 | 01 | 1 | FRAG-01 | unit | `go test ./internal/web -run TestPathID -count=1` | No -- Wave 0 | pending |
| 03-01-02 | 01 | 1 | FRAG-01 | unit | `go test ./internal/web -run TestPathSegment -count=1` | No -- Wave 0 | pending |
| 03-02-01 | 02 | 1 | FRAG-02, FRAG-03 | unit | `go test ./internal/web -run TestNewMissing -count=1` | No -- Wave 0 | pending |
| 03-02-02 | 02 | 1 | FRAG-03 | unit | `go test ./internal/web -run TestNewBrokenLayout -count=1` | No -- Wave 0 | pending |

*Status: pending · green · red · flaky*

---

## Wave 0 Requirements

- [ ] `internal/web/helpers_test.go` — stubs for pathID and pathSegment tests
- [ ] `internal/web/handler_test.go` — stubs for New() validation tests

*Existing infrastructure covers build/vet verification. Only unit tests for new helpers need Wave 0 stubs.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| None | — | — | — |

*All phase behaviors have automated verification.*

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 5s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending

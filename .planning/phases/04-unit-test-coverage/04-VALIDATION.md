---
phase: 4
slug: unit-test-coverage
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-03-05
---

# Phase 4 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go stdlib `testing` (Go 1.23) |
| **Config file** | None needed -- Go conventions |
| **Quick run command** | `go test ./internal/scan/ ./internal/tvscan/ ./internal/web/ -count=1` |
| **Full suite command** | `go test ./... -count=1` |
| **Estimated runtime** | ~5 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test ./internal/scan/ ./internal/tvscan/ ./internal/web/ -count=1`
- **After every plan wave:** Run `go test ./... -count=1`
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** 5 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|-----------|-------------------|-------------|--------|
| 04-01-01 | 01 | 1 | TEST-01 | unit | `go test ./internal/scan/ -run Music -count=1` | No -- created by task | pending |
| 04-01-02 | 01 | 1 | TEST-02 | unit | `go test ./internal/scan/ -run Compil -count=1` | No -- created by task | pending |
| 04-02-01 | 02 | 1 | TEST-03 | unit | `go test ./internal/tvscan/ -run TV -count=1` | No -- created by task | pending |
| 04-03-01 | 03 | 2 | TEST-04 | unit | `go test ./internal/web/ -run Music -count=1` | No -- created by task | pending |
| 04-03-02 | 03 | 2 | TEST-05 | unit | `go test ./internal/web/ -run TV -count=1` | No -- created by task | pending |
| 04-03-03 | 03 | 2 | TEST-06 | unit | `go test ./internal/web/ -run Librar -count=1` | No -- created by task | pending |

*Status: pending · green · red · flaky*

---

## Wave 0 Requirements

*This phase IS the test phase -- each task creates its own test files. No Wave 0 stubs needed.*

---

## Manual-Only Verifications

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

---
phase: 5
slug: integration-test-coverage
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-03-06
---

# Phase 5 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (stdlib) |
| **Config file** | none |
| **Quick run command** | `go test ./internal/match/ -run Integration -v -count=1 && go test ./internal/tmdb/ -run TVIntegration -v -count=1` |
| **Full suite command** | `go test ./... -count=1` |
| **Estimated runtime** | ~5 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test ./internal/match/ -run Integration -v -count=1 && go test ./internal/tmdb/ -run TVIntegration -v -count=1`
- **After every plan wave:** Run `go test ./... -count=1`
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** 10 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|-----------|-------------------|-------------|--------|
| 05-01-01 | 01 | 1 | TEST-07 | integration | `go test ./internal/match/ -run Integration -v -count=1` | ❌ W0 | ⬜ pending |
| 05-01-02 | 01 | 1 | TEST-07 | integration | `go test ./internal/match/ -run Integration -v -count=1` | ❌ W0 | ⬜ pending |
| 05-02-01 | 02 | 1 | TEST-08 | integration | `go test ./internal/tmdb/ -run TVIntegration -v -count=1` | ❌ W0 | ⬜ pending |
| 05-02-02 | 02 | 1 | TEST-08 | integration | `go test ./internal/tmdb/ -run TVIntegration -v -count=1` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `internal/match/pipeline_integration_test.go` — stubs for TEST-07
- [ ] `internal/tmdb/matcher_integration_test.go` — stubs for TEST-08
- [ ] Production code changes: add baseURL fields to MBClient, CAAClient, tmdb.Client for test injection

*Existing test infrastructure (openTestDB, httptest) covers framework needs.*

---

## Manual-Only Verifications

*All phase behaviors have automated verification.*

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 10s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending

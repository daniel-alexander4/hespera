---
phase: 1
slug: security-error-exposure
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-03-05
---

# Phase 1 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go testing (stdlib, Go 1.23) |
| **Config file** | None — stdlib, no config needed |
| **Quick run command** | `go test ./internal/db/ -run TestEnsureColumn -v -count=1` |
| **Full suite command** | `go test ./... -count=1` |
| **Estimated runtime** | ~5 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test ./internal/db/ -run TestEnsureColumn -v -count=1`
- **After every plan wave:** Run `go test ./... -count=1`
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** 5 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|-----------|-------------------|-------------|--------|
| 01-01-01 | 01 | 1 | SEC-01 | unit | `go test ./internal/db/ -run TestEnsureColumnValidation -v -count=1` | ❌ W0 | ⬜ pending |
| 01-02-01 | 02 | 1 | ERR-01, ERR-02, SEC-02 | grep | `grep -rn 'err.Error()' internal/web/handlers_*.go internal/web/handler.go` | N/A | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `internal/db/db_test.go` — add `TestEnsureColumnValidation` tests for SEC-01 (reject invalid names, accept valid ones)

*Web handler error response tests deferred to Phase 4 (TEST-04 through TEST-06). Grep-based verification is more appropriate for this phase.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| All error responses are generic | ERR-01, SEC-02 | Exhaustive handler coverage is Phase 4 scope | `grep -rn 'err.Error()' internal/web/handlers_*.go internal/web/handler.go` — must return 0 results |
| All errors logged via slog | ERR-02 | Verifying every path logs requires full handler tests | Review each httpError/jsonErr call includes meaningful log message |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 5s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending

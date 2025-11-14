# Testing Priority Summary

**Quick Reference Guide for Test Gap Closure**

## Current Status: 574 Scenarios Total

```
✅ Passing:   325 (56.6%)
❌ Failing:    47 (8.2%)   <- FIX THESE FIRST
⚠️  Undefined: 203 (35.4%)
```

## The Big Picture

### What's Working? ✅
- **Domain Layer:** 287 scenarios - 100% passing
- **Application Layer:** Most contract/navigation workflows passing
- **Scouting System:** All 13 scenarios passing (production-ready)

### What's Broken? ❌
1. **Container Lifecycle** (13 failures) - BLOCKS all daemon work
2. **Cargo Calculations** (10 failures) - BLOCKS trading/contracts
3. **API Retry Logic** (4 failures) - PRODUCTION RISK
4. **Route Executor** (3 failures) - NAVIGATION RISK

### What's Missing? ⚠️
1. **Daemon Server** (21 scenarios) - ZERO test coverage
2. **Health Monitor** (16 scenarios) - NO recovery testing
3. **Ship Assignment** (12 scenarios) - NO lock testing

---

## 4-Week Roadmap

### Week 1-2: Fix Existing Tests (47 scenarios)
**Goal:** Get to ZERO failures

**Priority Order:**
1. Container lifecycle bugs (2 days) ← BLOCKING
2. Cargo space calculations (1 day) ← HIGH IMPACT
3. API retry logic (1 day) ← PRODUCTION
4. Route executor edge cases (1 day)
5. Remaining failures (2 days)

**Exit Criteria:** All 574 tests either PASS or UNDEFINED (no failures)

---

### Week 3-4: Daemon Layer (90 scenarios)
**Goal:** Production-ready daemon testing

**Priority Order:**
1. Daemon server lifecycle (2-3 days)
   - Re-enable `daemon_server_steps.go.disabled`
   - Fix startup/shutdown/gRPC handling

2. Health monitor (2 days)
   - Re-enable `health_monitor_steps.go.disabled`
   - Fix stuck ship detection & recovery

3. Ship assignment locking (1.5 days)
   - Re-enable `ship_assignment_steps.go.disabled`
   - Fix concurrent access prevention

**Exit Criteria:** 462+ scenarios passing (80%+ coverage)

---

## Quick Start Guide

### Today: Run Tests & See Failures
```bash
# See the damage
make test

# Should show:
# 574 scenarios (325 passed, 47 failed, 203 undefined)
```

### This Week: Fix Container Lifecycle (Day 1-2)
**File:** `test/bdd/features/daemon/container_lifecycle.feature`
**Steps:** `test/bdd/steps/container_lifecycle_steps.go`

**Fix these 13 scenarios:**
```bash
go test -v ./test/bdd/bdd_test.go -godog.filter="Container_transitions_to_FAILED_on_error"
go test -v ./test/bdd/bdd_test.go -godog.filter="Container_increments_iteration_count"
go test -v ./test/bdd/bdd_test.go -godog.filter="Container_respects_max_iterations"
# ... (see full list in analysis doc)
```

**Root Cause:** Container state machine bugs in `internal/domain/container/container.go`

---

### This Week: Fix Cargo Calculations (Day 3)
**File:** `test/bdd/features/domain/shared/cargo_value_object.feature`
**Steps:** `test/bdd/steps/value_object_steps.go`

**Fix these 10 scenarios:**
```bash
go test -v ./test/bdd/bdd_test.go -godog.filter="Available_cargo_space"
go test -v ./test/bdd/bdd_test.go -godog.filter="Has_cargo_space"
go test -v ./test/bdd/bdd_test.go -godog.filter="Is_cargo"
```

**Root Cause:** Cargo value object methods in `internal/domain/shared/cargo.go`

---

### This Week: Fix API Retries (Day 4)
**File:** `test/bdd/features/infrastructure/api_retry.feature`
**Steps:** `test/bdd/steps/api_retry_steps.go`

**Fix these 4 scenarios:**
```bash
go test -v ./test/bdd/bdd_test.go -godog.filter="API_client_retries"
go test -v ./test/bdd/bdd_test.go -godog.filter="Circuit_breaker"
```

**Root Cause:** Retry/circuit breaker logic in `internal/adapters/api/client.go`

---

## Files to Re-Enable (After Fixing Failures)

### Disabled Step Definitions
```
test/bdd/steps/daemon_server_steps.go.disabled          (21 scenarios)
test/bdd/steps/health_monitor_steps.go.disabled         (16 scenarios)
test/bdd/steps/ship_assignment_steps.go.disabled        (12 scenarios)
test/bdd/steps/database_retry_steps.go.disabled         (10 scenarios)
test/bdd/steps/waypoint_cache_steps.go.disabled         (14 scenarios - cleanup only)
```

**How to re-enable:**
```bash
# After fixing container lifecycle bugs:
mv test/bdd/steps/daemon_server_steps.go.disabled test/bdd/steps/daemon_server_steps.go
make test  # Fix any failures, iterate
```

---

## Success Metrics

### Phase 1 Success (Week 1-2)
- ❌ → ✅ Zero failing tests
- 🎯 372+ scenarios passing (64.8%)
- 📉 202 scenarios undefined (down from 203)

### Phase 2 Success (Week 3-4)
- ✅ All daemon features tested
- 🎯 462+ scenarios passing (80.5%)
- 📉 112 scenarios undefined (mostly edge cases)

### Final Target (Month 1)
- ✅ 486+ scenarios passing (84.7%)
- ✅ Zero critical gaps in production features
- 🎯 88 scenarios deferred (non-critical edge cases)

---

## FAQ

### Q: Why fix failing tests before adding new ones?
**A:** Failing tests indicate bugs in production code. Fix the bugs first, then expand coverage.

### Q: Why is Container Lifecycle blocking everything?
**A:** The daemon server, health monitor, and ship assignment all depend on containers working correctly. Fix the foundation first.

### Q: Can we parallelize Phase 1 and Phase 2?
**A:** No. Daemon tests will fail if container lifecycle is broken. Sequential execution required.

### Q: What about the 88 deferred scenarios?
**A:** These are edge cases and alternative error paths. They provide diminishing returns and can be added incrementally after achieving 80%+ coverage.

### Q: How long will this take?
**A:** 12-17 days for Phases 1-4 (161 scenarios). Deferred work (88 scenarios) can be done later.

---

## Key Contacts & Resources

- **Full Analysis:** `UNDEFINED_SCENARIOS_ANALYSIS.md` (this directory)
- **Run Tests:** `make test` or `./run-tests.sh`
- **Test Guide:** `TEST_PERFORMANCE.md` (for fast test principles)
- **Architecture:** `CLAUDE.md` (system architecture overview)

---

## Commands Cheat Sheet

```bash
# Run all tests
make test

# Run specific feature
go test -v ./test/bdd/bdd_test.go -godog.paths=test/bdd/features/daemon/container_lifecycle.feature

# Run specific scenario
go test -v ./test/bdd/bdd_test.go -godog.filter="Container_transitions_to_FAILED"

# Run with race detector
make test-race

# Run with coverage
make test-coverage

# Fast iteration (no race/cover)
make test-fast
```

---

## Decision Matrix

| Scenario Category | Count | Priority | Action |
|-------------------|-------|----------|--------|
| **Failing Tests** | 47 | 🔴 CRITICAL | Fix immediately (Week 1-2) |
| **Daemon Server** | 21 | 🔴 CRITICAL | Implement after fixes (Week 3) |
| **Health Monitor** | 16 | 🔴 CRITICAL | Implement after fixes (Week 3) |
| **Ship Assignment** | 12 | 🔴 CRITICAL | Implement after fixes (Week 4) |
| **Database Retry** | 10 | 🟡 HIGH | Implement if time permits |
| **Edge Cases** | 88 | 🟢 LOW | Defer to Month 2+ |

---

## Bottom Line

**Start here:** Fix container lifecycle bugs (Day 1-2)
**Then:** Fix cargo calculations (Day 3)
**Then:** Fix API retries (Day 4)
**Then:** Re-enable daemon tests (Week 3-4)

**Goal:** 80%+ test coverage (462/574 scenarios) in 4 weeks.

**Current blockers:** 47 failing tests (especially Container Lifecycle)

**Next blocker:** 90 undefined daemon tests (after fixing failures)

**Success looks like:** Zero failures, daemon fully tested, health monitor operational.

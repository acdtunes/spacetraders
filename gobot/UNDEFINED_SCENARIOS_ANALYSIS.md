# Undefined Scenarios Analysis Report

**Generated:** 2025-11-13
**Total Scenarios:** 574 (325 passed, 47 failed, 203 undefined)

## Executive Summary

This report categorizes the 203 undefined BDD scenarios and 47 failing scenarios across the SpaceTraders Go Bot test suite. The analysis prioritizes scenarios based on **business value**, **production impact**, and **implementation effort** to guide testing gap closure.

### Test Coverage Breakdown

| Layer | Total Scenarios | Status | Notes |
|-------|----------------|--------|-------|
| **Domain** | 287 | ✅ 100% Implemented | Entities & Value Objects fully tested |
| **Application** | 153 | ⚠️ ~60% Implemented | Command/Query handlers partially tested |
| **Daemon** | 90 | ❌ 0% Implemented | All step definitions disabled |
| **Infrastructure** | 30 | ⚠️ ~60% Implemented | Retry logic partially tested |
| **Adapters** | 9 | ✅ 100% Implemented | Repository tests complete |

### Key Findings

1. **47 Failing Tests** - Existing tests with implementation bugs (HIGH PRIORITY)
2. **Daemon Layer (90 scenarios)** - Production-critical features completely untested
3. **Health Monitor (16 scenarios)** - Zero coverage for recovery mechanisms
4. **Ship Assignment (12 scenarios)** - Locking mechanisms untested
5. **Database Retry (10 scenarios)** - Resilience patterns disabled

---

## Part 1: Fix Failing Tests First (47 scenarios)

**Priority:** 🔴 **CRITICAL** - These tests EXIST but FAIL. Fix these before adding new tests.

### Domain Layer Failures (18 scenarios)

#### Cargo Value Object (10 failing scenarios)
**File:** `test/bdd/features/domain/shared/cargo_value_object.feature`
**Step Definitions:** `test/bdd/steps/value_object_steps.go`

Failing scenarios:
- `Available_cargo_space_with_full_cargo`
- `Available_cargo_space_with_partial_cargo`
- `Has_cargo_space_with_full_cargo`
- `Has_cargo_space_with_partial_cargo`
- `Has_cargo_space_with_specific_units`
- `Has_cargo_space_with_specific_units_exceeding_available`
- `Is_cargo_empty_when_empty`
- `Is_cargo_empty_when_not_empty`
- `Is_cargo_full_when_full`
- `Is_cargo_full_when_not_full`

**Root Cause:** Likely bugs in `Cargo` value object methods or test assertions.
**Effort:** 0.5 days
**Impact:** HIGH - Cargo space calculations critical for trading/contracts

---

#### Ship Entity Failures (4 scenarios)
**File:** `test/bdd/features/domain/navigation/ship_entity.feature`
**Step Definitions:** `test/bdd/steps/ship_steps.go`

Failing scenarios:
- `Select_optimal_flight_mode_with_high_fuel`
- `Select_optimal_flight_mode_with_medium_fuel`
- `Select_optimal_flight_mode_with_low_fuel`
- `Select_burn_mode_with_high_fuel_and_safety_margin`

**Root Cause:** Flight mode selection logic mismatch between entity and test expectations.
**Effort:** 0.5 days
**Impact:** MEDIUM - Affects fuel optimization

---

#### Fuel Value Object (1 scenario)
**File:** `test/bdd/features/domain/shared/fuel_value_object.feature`

Failing scenario:
- `Consume_negative_fuel_fails`

**Root Cause:** Missing validation for negative fuel consumption.
**Effort:** 0.25 days
**Impact:** LOW - Edge case validation

---

#### Market Entity (3 scenarios)
**File:** `test/bdd/features/domain/market/market.feature`

Failing scenarios:
- `Create_valid_market_with_trade_goods`
- `Create_valid_market_with_no_trade_goods`
- `Cannot_create_market_with_empty_waypoint_symbol`

**Root Cause:** Market entity constructor validation issues.
**Effort:** 0.5 days
**Impact:** MEDIUM - Affects market scouting features

---

### Application Layer Failures (9 scenarios)

#### Dock Ship Handler (2 scenarios)
**File:** `test/bdd/features/application/dock_ship.feature`

Failing scenarios:
- `Dock_ship_when_already_docked` (idempotency test)
- `Dock_ship_when_in_orbit`
- `Cannot_dock_ship_when_in_transit`

**Root Cause:** DockShipCommandHandler state transition logic.
**Effort:** 0.5 days
**Impact:** MEDIUM - Ship state management

---

#### Route Executor (3 scenarios)
**File:** `test/bdd/features/application/route_executor.feature`

Failing scenarios:
- `Execute_route_with_pre-departure_refuel_prevention`
- `Handle_ship_already_in_transit_-_wait_for_arrival_first`
- `Handle_route_execution_failure_-_segment_navigation_fails`

**Root Cause:** Route execution edge cases not handled.
**Effort:** 1 day
**Impact:** HIGH - Core navigation reliability

---

#### Negotiate Contract (1 scenario)
**File:** `test/bdd/features/application/negotiate_contract.feature`

Failing scenario:
- `Negotiate_new_contract_successfully`

**Root Cause:** API client mock or handler implementation issue.
**Effort:** 0.5 days
**Impact:** HIGH - Blocks contract workflow

---

### Container Lifecycle Failures (13 scenarios)

**File:** `test/bdd/features/daemon/container_lifecycle.feature`
**Step Definitions:** `test/bdd/steps/container_lifecycle_steps.go` (ENABLED)

Failing scenarios:
- `Container_transitions_to_FAILED_on_error`
- `List_containers_shows_correct_status_after_completion`
- `Running_containers_excluded_from_finished_containers_list`
- `Container_increments_iteration_count_on_loop`
- `Container_respects_max_iterations_limit`
- `Container_with_-1_iterations_runs_indefinitely`
- `Container_exits_after_max_iterations_reached`
- `Container_restart_increments_restart_count`
- `Container_respects_max_restarts_policy_(3)`
- `Container_cannot_restart_after_exceeding_max_restarts`
- `Container_maintains_player_id_through_restarts`
- `Container_tracks_multiple_restart_attempts`
- `Daemon_tracks_containers_independently`
- `Container_cannot_transition_from_COMPLETED_to_RUNNING`
- `Container_cannot_transition_from_FAILED_to_RUNNING_without_restart`
- `Container_cannot_be_completed_if_not_running`

**Root Cause:** Container state machine implementation bugs.
**Effort:** 2 days
**Impact:** 🔴 **CRITICAL** - Container reliability affects all background operations

---

### Infrastructure Layer Failures (7 scenarios)

#### API Retry Logic (3 scenarios)
**File:** `test/bdd/features/infrastructure/api_retry.feature`

Failing scenarios:
- `API_client_retries_on_429_Too_Many_Requests`
- `API_client_retries_on_503_Service_Unavailable`
- `API_client_respects_max_5_retry_attempts`
- `Circuit_breaker_transitions_to_half-open_after_60_seconds`

**Root Cause:** Retry mechanism or circuit breaker implementation issues.
**Effort:** 1 day
**Impact:** HIGH - API resilience critical for production

---

#### Database Retry (2 scenarios)
**File:** `test/bdd/features/infrastructure/database_retry.feature`

Failing scenarios:
- `Database_query_timeout_after_30_seconds`
- `Database_connection_pooling_prevents_exhaustion`

**Root Cause:** Timeout/pooling configuration or test setup.
**Effort:** 0.5 days
**Impact:** MEDIUM - Database resilience

---

## Part 2: Critical Undefined Scenarios (High Business Value)

### Priority Tier 1: Production Reliability (90 scenarios - 5-7 days)

These scenarios cover production-critical daemon operations. All step definitions are DISABLED but feature files exist.

#### 1. Daemon Server Lifecycle (21 scenarios)
**File:** `test/bdd/features/daemon/daemon_server.feature`
**Step Definitions:** `test/bdd/steps/daemon_server_steps.go.disabled` ❌

**Critical scenarios:**
- Daemon startup and Unix socket management (3 scenarios)
- gRPC connection handling (2 scenarios)
- Request handling and container creation (4 scenarios)
- Background operation execution (2 scenarios)
- Graceful shutdown behavior (4 scenarios)
- Resource cleanup on shutdown (4 scenarios)
- Shutdown edge cases (3 scenarios)

**Why Critical:**
- Zero test coverage for production daemon server
- Shutdown logic untested = potential data loss
- Unix socket management untested = startup failures
- gRPC handling untested = communication failures

**Effort:** 2-3 days (re-enable + fix + verify)
**Business Value:** 🔴 **CRITICAL** - Daemon is core production component
**Dependencies:** Requires working container lifecycle (fix Part 1 first)

---

#### 2. Health Monitor (16 scenarios)
**File:** `test/bdd/features/daemon/health_monitor.feature`
**Step Definitions:** `test/bdd/steps/health_monitor_steps.go.disabled` ❌

**Critical scenarios:**
- Stale assignment detection (3 scenarios)
- Stuck ship detection (3 scenarios)
- Recovery triggering (3 scenarios)
- Recovery attempt tracking (3 scenarios)
- Periodic monitoring (2 scenarios)
- Infinite loop detection (2 scenarios)

**Why Critical:**
- Zero test coverage for stuck operation recovery
- Health monitor is THE safety net for production failures
- Without this, ships can remain stuck indefinitely
- Recovery mechanisms completely untested

**Effort:** 2 days
**Business Value:** 🔴 **CRITICAL** - Prevents operational stalls
**Production Impact:** Without health monitor, manual intervention required for every stuck ship

---

#### 3. Ship Assignment Locking (12 scenarios)
**File:** `test/bdd/features/daemon/ship_assignment.feature`
**Step Definitions:** `test/bdd/steps/ship_assignment_steps.go.disabled` ❌

**Critical scenarios:**
- Basic ship assignment (3 scenarios)
- Assignment release (3 scenarios)
- Orphaned assignment cleanup (1 scenario)
- Validation (1 scenario)
- Lock behavior (2 scenarios)
- Lock timeout (2 scenarios)

**Why Critical:**
- Prevents race conditions in concurrent ship operations
- Lock timeouts untested = potential deadlocks
- Orphaned assignments untested = ships permanently locked

**Effort:** 1.5 days
**Business Value:** 🔴 **CRITICAL** - Prevents data corruption
**Production Impact:** Without locking, multiple containers can control same ship simultaneously

---

### Priority Tier 2: Resilience & Stability (10 scenarios - 1-2 days)

#### 4. Database Retry Logic (10 scenarios)
**File:** `test/bdd/features/infrastructure/database_retry.feature`
**Step Definitions:** `test/bdd/steps/database_retry_steps.go.disabled` ❌

**Scenarios:**
- Connection pooling (1 scenario)
- Retry on transient failure (1 scenario)
- Exponential backoff (1 scenario)
- Max retries exhaustion (1 scenario)
- Transaction rollback/commit (2 scenarios)
- Query timeout (1 scenario) [FAILING - see Part 1]
- Health check (1 scenario)
- Graceful close (1 scenario)
- Connection exhaustion (1 scenario) [FAILING - see Part 1]

**Why Important:**
- Database is single point of failure
- Retry logic untested = transient failures become permanent
- Connection pooling untested = exhaustion crashes

**Effort:** 1 day (re-enable + fix 2 failing tests)
**Business Value:** HIGH - Database resilience
**Production Impact:** Without retries, temporary DB issues cause cascading failures

---

### Priority Tier 3: Performance & Optimization (14 scenarios - 1 day)

#### 5. Waypoint Cache (14 scenarios)
**File:** `test/bdd/features/infrastructure/waypoint_cache.feature`
**Step Definitions:** `test/bdd/steps/waypoint_cache_steps.go.disabled` ❌

**Note:** All 14 scenarios in this feature are actually **PASSING** (see test output). The step definitions are disabled but the tests work via shared step definitions in `value_object_steps.go`.

**Recommendation:** Re-enable `waypoint_cache_steps.go.disabled` to consolidate step definitions and improve maintainability, but this is LOW PRIORITY since tests pass.

**Effort:** 0.5 days (cleanup only)
**Business Value:** LOW - Already working
**Production Impact:** None (tests passing)

---

## Part 3: Nice-to-Have Undefined Scenarios (Defer)

### Application Layer - Contract Workflows (89 scenarios remaining)

Most application layer contract scenarios are **already implemented and passing**. The undefined scenarios are likely edge cases or alternative paths that can be deferred.

**Files:**
- `test/bdd/features/application/accept_contract.feature` (4 scenarios) ✅ PASSING
- `test/bdd/features/application/batch_contract_workflow.feature` (9 scenarios) ✅ MOSTLY PASSING
- `test/bdd/features/application/deliver_contract.feature` (6 scenarios) ✅ PASSING
- `test/bdd/features/application/evaluate_contract_profitability.feature` (8 scenarios) ✅ PASSING
- `test/bdd/features/application/fulfill_contract.feature` (5 scenarios) ✅ PASSING
- `test/bdd/features/application/jettison_cargo.feature` (6 scenarios) ✅ PASSING
- `test/bdd/features/application/purchase_cargo.feature` (4 scenarios) ✅ PASSING

**Why Defer:**
- Core contract workflows already tested
- Business logic validated
- Remaining scenarios likely testing error paths or edge cases
- Low production risk

**Effort:** 3-4 days (if implemented)
**Business Value:** MEDIUM - Improves edge case coverage
**Production Impact:** LOW - Core functionality already works

---

### Application Layer - Navigation Utils (9 scenarios)

**File:** `test/bdd/features/application/navigation_utils.feature` (9 scenarios) ✅ PASSING

All 9 scenarios PASSING. No undefined scenarios here.

---

### Scouting Features (24 scenarios)

**Files:**
- `test/bdd/features/application/scouting/get_market_data.feature` (2 scenarios) ✅ PASSING
- `test/bdd/features/application/scouting/list_market_data.feature` (4 scenarios) ✅ PASSING
- `test/bdd/features/application/scouting/scout_markets.feature` (5 scenarios) ✅ PASSING
- `test/bdd/features/application/scouting/scout_tour.feature` (3 scenarios) ✅ PASSING

All scouting scenarios PASSING. This is production-ready.

---

## Part 4: Obsolete/Dead Features (Delete)

After reviewing the codebase, **NO scenarios appear obsolete**. All feature files correspond to implemented or planned functionality.

**Recommendation:** Keep all feature files. They represent the complete system specification.

---

## Implementation Roadmap

### Phase 1: Fix Existing Tests (47 scenarios - 5-7 days) 🔴 CRITICAL

**Week 1: Critical Fixes**
1. **Day 1-2:** Fix Container Lifecycle failures (13 scenarios) - BLOCKS Phase 2
2. **Day 3:** Fix Cargo Value Object failures (10 scenarios) - HIGH IMPACT
3. **Day 4:** Fix API Retry failures (4 scenarios) - PRODUCTION RELIABILITY
4. **Day 5:** Fix Route Executor failures (3 scenarios) - NAVIGATION CORE

**Week 2: Remaining Fixes**
5. **Day 6:** Fix Ship Entity flight mode (4 scenarios)
6. **Day 7:** Fix Market Entity (3 scenarios) + Dock Ship (3 scenarios)
7. **Day 8:** Fix Database Retry (2 scenarios) + Negotiate Contract (1 scenario)
8. **Day 9:** Fix remaining miscellaneous failures (4 scenarios)
9. **Day 10:** Regression testing - verify all 574 scenarios pass

**Exit Criteria:** 574 scenarios, 372+ passing, 0 failing, 202 undefined

---

### Phase 2: Daemon Layer Testing (90 scenarios - 5-7 days) 🔴 CRITICAL

**Week 3-4: Production Reliability**
1. **Day 1-3:** Re-enable and fix Daemon Server tests (21 scenarios)
   - Start with basic startup/shutdown (5 scenarios)
   - Then gRPC handling (6 scenarios)
   - Finally resource cleanup (10 scenarios)

2. **Day 4-5:** Re-enable and fix Health Monitor tests (16 scenarios)
   - Stale assignment detection (3 scenarios)
   - Stuck ship detection and recovery (9 scenarios)
   - Periodic monitoring (4 scenarios)

3. **Day 6-7:** Re-enable and fix Ship Assignment tests (12 scenarios)
   - Basic assignment/release (6 scenarios)
   - Lock behavior and timeout (6 scenarios)

**Exit Criteria:** 574 scenarios, 462+ passing, 0 failing, 112 undefined

---

### Phase 3: Infrastructure Resilience (10 scenarios - 1-2 days)

**Week 5: Database Reliability**
1. **Day 1-2:** Re-enable and fix Database Retry tests (10 scenarios)
   - Connection pooling and retry logic
   - Transaction management
   - Graceful degradation

**Exit Criteria:** 574 scenarios, 472+ passing, 0 failing, 102 undefined

---

### Phase 4: Optional Cleanup (14 scenarios - 0.5 days)

**Week 5-6: Maintenance**
1. **Day 3:** Re-enable Waypoint Cache step definitions (cleanup only)
   - Tests already passing via shared steps
   - Consolidate for maintainability

**Exit Criteria:** 574 scenarios, 486+ passing, 0 failing, 88 undefined

---

## Top 50 Scenarios to Implement (Prioritized)

### Tier 1: Fix These First (Must Fix - BLOCKING)

1. ✅ `Container_transitions_to_FAILED_on_error` - Container lifecycle
2. ✅ `Container_increments_iteration_count_on_loop` - Iteration tracking
3. ✅ `Container_respects_max_iterations_limit` - Loop control
4. ✅ `Container_with_-1_iterations_runs_indefinitely` - Infinite loops
5. ✅ `Container_exits_after_max_iterations_reached` - Loop termination
6. ✅ `Container_restart_increments_restart_count` - Restart tracking
7. ✅ `Container_respects_max_restarts_policy_(3)` - Restart limits
8. ✅ `Container_cannot_restart_after_exceeding_max_restarts` - Max restarts
9. ✅ `Container_maintains_player_id_through_restarts` - Data integrity
10. ✅ `Container_tracks_multiple_restart_attempts` - Restart history
11. ✅ `Available_cargo_space_with_full_cargo` - Cargo calculations
12. ✅ `Available_cargo_space_with_partial_cargo` - Cargo calculations
13. ✅ `Has_cargo_space_with_full_cargo` - Space validation
14. ✅ `Has_cargo_space_with_partial_cargo` - Space validation
15. ✅ `API_client_retries_on_429_Too_Many_Requests` - Rate limiting
16. ✅ `API_client_retries_on_503_Service_Unavailable` - Service failures
17. ✅ `API_client_respects_max_5_retry_attempts` - Retry limits
18. ✅ `Execute_route_with_pre-departure_refuel_prevention` - Navigation logic
19. ✅ `Handle_ship_already_in_transit_-_wait_for_arrival_first` - State handling
20. ✅ `Handle_route_execution_failure_-_segment_navigation_fails` - Error handling

### Tier 2: Implement After Fixes (Production Critical - 90 undefined)

21. ⚠️ `Daemon server starts and listens on Unix socket` - Basic startup
22. ⚠️ `Daemon server accepts gRPC client connections` - Communication
23. ⚠️ `Daemon server handles NavigateShip request` - Core functionality
24. ⚠️ `Daemon server creates container for navigation operation` - Orchestration
25. ⚠️ `Daemon server continues operation in background` - Async execution
26. ⚠️ `Daemon server initiates graceful shutdown on SIGTERM` - Shutdown
27. ⚠️ `Daemon server waits for containers during shutdown (within timeout)` - Graceful stop
28. ⚠️ `Daemon server closes database connections on shutdown` - Resource cleanup
29. ⚠️ `Daemon server releases all ship assignments on shutdown` - Lock cleanup
30. ⚠️ `Daemon server removes Unix socket on shutdown` - Socket cleanup
31. ⚠️ `Health monitor detects stale assignment after container removed` - Stale detection
32. ⚠️ `Health monitor detects stuck IN_TRANSIT ship` - Stuck detection
33. ⚠️ `Health monitor triggers recovery for stuck ship` - Recovery trigger
34. ⚠️ `Health monitor recovery succeeds and resumes operation` - Recovery success
35. ⚠️ `Health monitor recovery fails and marks container as failed` - Recovery failure
36. ⚠️ `Health monitor tracks recovery attempts per ship` - Attempt tracking
37. ⚠️ `Health monitor abandons ship after max recovery attempts` - Max attempts
38. ⚠️ `Health monitor clears healthy ship from watch list` - Watchlist cleanup
39. ⚠️ `Health monitor respects cooldown between checks` - Cooldown logic
40. ⚠️ `Health monitor detects infinite loop in navigation` - Loop detection
41. ⚠️ `Ship can be assigned to container` - Basic assignment
42. ⚠️ `Ship cannot be assigned to multiple containers` - Concurrent prevention
43. ⚠️ `Ship assignment persists in database` - Persistence
44. ⚠️ `Ship assignment is released when container stops` - Release on stop
45. ⚠️ `Ship assignment is released when container fails` - Release on fail
46. ⚠️ `Ship assignment prevents concurrent navigation commands` - Lock enforcement
47. ⚠️ `Ship assignment lock timeout after 30 minutes` - Timeout handling
48. ⚠️ `Database connection retry on transient failure (max 3 attempts)` - Retry logic
49. ⚠️ `Database connection exponential backoff (1s, 2s, 4s)` - Backoff strategy
50. ⚠️ `Database graceful close releases all connections` - Graceful shutdown

---

## Summary Statistics

### Current State
- **Total Scenarios:** 574
- **Passing:** 325 (56.6%)
- **Failing:** 47 (8.2%) 🔴
- **Undefined:** 203 (35.4%)

### After Phase 1 (Fix Existing Tests)
- **Total Scenarios:** 574
- **Passing:** 372+ (64.8%)
- **Failing:** 0 (0%) ✅
- **Undefined:** 202 (35.2%)

### After Phase 2 (Daemon Layer)
- **Total Scenarios:** 574
- **Passing:** 462+ (80.5%)
- **Failing:** 0 (0%) ✅
- **Undefined:** 112 (19.5%)

### After Phase 3 (Infrastructure)
- **Total Scenarios:** 574
- **Passing:** 472+ (82.2%)
- **Failing:** 0 (0%) ✅
- **Undefined:** 102 (17.8%)

### Final Target (Phase 4)
- **Total Scenarios:** 574
- **Passing:** 486+ (84.7%)
- **Failing:** 0 (0%) ✅
- **Undefined:** 88 (15.3%) - Deferred edge cases

---

## Key Recommendations

### Immediate Actions (This Week)
1. ✅ **Fix Container Lifecycle bugs** (13 scenarios) - BLOCKS everything else
2. ✅ **Fix Cargo space calculations** (10 scenarios) - HIGH IMPACT on trading
3. ✅ **Fix API retry logic** (4 scenarios) - PRODUCTION RESILIENCE

### Short-Term (Next 2 Weeks)
4. ⚠️ **Re-enable Daemon Server tests** (21 scenarios) - PRODUCTION CRITICAL
5. ⚠️ **Re-enable Health Monitor tests** (16 scenarios) - OPERATIONAL SAFETY
6. ⚠️ **Re-enable Ship Assignment tests** (12 scenarios) - CONCURRENCY SAFETY

### Medium-Term (Month 2)
7. ⚠️ **Re-enable Database Retry tests** (10 scenarios) - DATABASE RESILIENCE
8. ✅ **Cleanup Waypoint Cache step definitions** (14 scenarios) - MAINTAINABILITY

### Long-Term (Defer)
9. Edge case coverage for contracts (89 scenarios) - NICE-TO-HAVE
10. Additional navigation error paths - LOW PRIORITY

---

## Effort Estimates

| Phase | Scenarios | Effort | Priority |
|-------|-----------|--------|----------|
| **Phase 1: Fix Failing** | 47 | 5-7 days | 🔴 CRITICAL |
| **Phase 2: Daemon Layer** | 90 | 5-7 days | 🔴 CRITICAL |
| **Phase 3: Database Retry** | 10 | 1-2 days | 🟡 HIGH |
| **Phase 4: Cleanup** | 14 | 0.5 days | 🟢 LOW |
| **Deferred** | 88 | 3-4 days | 🟢 LOW |
| **TOTAL (Phases 1-4)** | **161** | **12-17 days** | - |

---

## Conclusion

**Focus on Phase 1 and Phase 2 (142 scenarios, ~12-14 days)** to achieve:
- ✅ Zero failing tests (reliability)
- ✅ Full daemon layer coverage (production readiness)
- ✅ Health monitor coverage (operational safety)
- ✅ 80%+ total test coverage (462/574 scenarios passing)

The remaining 102 undefined scenarios (Phases 3-4 + deferred) provide diminishing returns and can be addressed incrementally as time permits.

**Next Steps:**
1. Review this analysis with the team
2. Create GitHub issues for Phase 1 failures (47 scenarios)
3. Start with Container Lifecycle fixes (blocks Phase 2)
4. Execute roadmap phases sequentially

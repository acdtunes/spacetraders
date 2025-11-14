# Testing Gap Closure Plan
**SpaceTraders Go Bot - BDD Test Coverage Enhancement**

## Executive Summary

This document outlines a phased approach to close the testing gap between the legacy Python implementation (1,198 scenarios) and the current Go implementation (449 scenarios). Based on comprehensive analysis, we've identified **~300 critical scenarios** needed to achieve production-ready test coverage.

**Current State:** 449 BDD scenarios
**Target State:** 750-850 BDD scenarios
**Gap to Close:** ~300-400 scenarios
**Timeline:** 8-12 weeks (phased approach)

## Gap Analysis Summary

### Critical Gaps (Must Fix)
| Category | Current | Target | Gap | Priority |
|----------|---------|--------|-----|----------|
| Daemon Orchestration | 0 | 64 | 64 | 🔴 Critical |
| Integration Tests | 9 | 80 | 71 | 🔴 Critical |
| Infrastructure Tests | 0 | 50 | 50 | 🟡 High |

### Feature Implementation Gaps (Implement + Test)
| Feature | Python Scenarios | Go Scenarios | Gap | Priority |
|---------|-----------------|--------------|-----|----------|
| Shipyard Operations | 20+ | 0 | 20 | 🟡 High |
| Captain Logging | 15 | 0 | 15 | 🟡 High |
| Waypoint Queries | 12 | 2 | 10 | 🟢 Medium |
| Player Entity Ops | 30 | 0 | 30 | 🟢 Medium |
| Pipeline Behaviors | 18 | 0 | 18 | 🟢 Low |

### Strong Areas (Maintain)
- **Domain Entities:** 287 scenarios (excellent coverage)
- **Contract Workflow:** 35+ scenarios (good coverage)
- **Navigation:** 150+ scenarios in ship entity (comprehensive)
- **Scouting:** 14 scenarios (adequate)

## Phased Implementation Plan

---

## Phase 1: Production Reliability (Weeks 1-3)
**Goal:** Ensure daemon and critical infrastructure are bulletproof
**Scenarios to Add:** 64 daemon + 50 infrastructure = 114 scenarios
**New Total:** 563 scenarios

### 1.1 Daemon Orchestration Tests (64 scenarios)

#### Priority 1A: Container Lifecycle (20 scenarios)
**File:** `test/bdd/features/daemon/container_lifecycle.feature`

```gherkin
# Critical scenarios from Python implementation:

Scenario: Container transitions to RUNNING on successful start
Scenario: Container transitions to STOPPED on successful completion
Scenario: Container transitions to FAILED on error
Scenario: Container stays in STOPPING during graceful shutdown
Scenario: Quick-running containers properly transition status
Scenario: Container status reflects completion even with exit code set
Scenario: List containers shows correct status after completion
Scenario: Completed containers can be removed without stopping
Scenario: Container tracks start and stop timestamps
Scenario: Container increments iteration count on loop
Scenario: Container respects max_iterations limit
Scenario: Container with -1 iterations runs indefinitely
Scenario: Container exits after max_iterations reached
Scenario: Container restart increments restart_count
Scenario: Container respects max_restarts policy (3)
Scenario: Container cannot restart after exceeding max_restarts
Scenario: Container maintains player_id through restarts
Scenario: Container maintains ship assignment through restarts
Scenario: Multiple containers can run simultaneously
Scenario: Container IDs are unique and sequential
Scenario: Container type is persisted correctly
```

#### Priority 1B: Health Monitoring & Recovery (15 scenarios)
**File:** `test/bdd/features/daemon/health_monitor.feature`

```gherkin
Scenario: Health monitor detects stuck IN_TRANSIT ship
Scenario: Health monitor detects missing arrival for completed route
Scenario: Health monitor detects infinite loop in navigation
Scenario: Health monitor triggers recovery for stuck ship
Scenario: Health monitor logs warning for suspicious patterns
Scenario: Health monitor respects cooldown between checks
Scenario: Health monitor tracks recovery attempts per ship
Scenario: Health monitor abandons ship after max recovery attempts
Scenario: Health monitor clears healthy ship from watch list
Scenario: Health monitor persists watch list across daemon restarts
Scenario: Health monitor handles multiple concurrent stuck ships
Scenario: Health monitor distinguishes between stuck and slow operations
Scenario: Health monitor recovery succeeds and resumes operation
Scenario: Health monitor recovery fails and marks container as failed
Scenario: Health monitor reports metrics on recovery success rate
```

#### Priority 1C: Ship Assignment & Locking (12 scenarios)
**File:** `test/bdd/features/daemon/ship_assignment.feature`

```gherkin
Scenario: Ship can be assigned to container
Scenario: Ship cannot be assigned to multiple containers
Scenario: Ship assignment persists in database
Scenario: Ship assignment is released when container stops
Scenario: Ship assignment is released when container fails
Scenario: Ship assignment is released when daemon stops gracefully
Scenario: Orphaned ship assignments are cleaned on daemon startup
Scenario: Ship assignment includes player_id validation
Scenario: Ship assignment prevents concurrent navigation commands
Scenario: Ship assignment allows read-only queries during lock
Scenario: Ship assignment lock timeout after 30 minutes
Scenario: Ship assignment reacquire fails if still locked
```

#### Priority 1D: Daemon Server & Lifecycle (10 scenarios)
**File:** `test/bdd/features/daemon/daemon_server.feature`

```gherkin
Scenario: Daemon starts and listens on Unix socket
Scenario: Daemon accepts gRPC connections
Scenario: Daemon handles NavigateShip request
Scenario: Daemon creates container for navigation operation
Scenario: Daemon returns container ID immediately
Scenario: Daemon continues operation in background
Scenario: Daemon graceful shutdown waits for containers (30s timeout)
Scenario: Daemon forceful shutdown after timeout
Scenario: Daemon closes database connections on shutdown
Scenario: Daemon releases all ship assignments on shutdown
```

#### Priority 1E: Container Logging & Persistence (7 scenarios)
**File:** `test/bdd/features/daemon/container_logging.feature`

```gherkin
Scenario: Container logs INFO messages to database
Scenario: Container logs ERROR messages to database
Scenario: Container logs include timestamp and container_id
Scenario: Container logs are queryable by container_id
Scenario: Container logs are queryable by log level
Scenario: Container logs deduplicate within 60 seconds
Scenario: Container logs support pagination with limit/offset
```

### 1.2 Critical Infrastructure Tests (50 scenarios)

#### Priority 1F: API Rate Limiter (15 scenarios)
**File:** `test/bdd/features/infrastructure/api_rate_limiter.feature`

```gherkin
Scenario: Rate limiter allows requests within limit (2 req/sec)
Scenario: Rate limiter blocks requests exceeding limit
Scenario: Rate limiter uses token bucket algorithm
Scenario: Rate limiter refills tokens over time
Scenario: Rate limiter starts with full bucket
Scenario: Rate limiter handles burst traffic correctly
Scenario: Rate limiter per-client isolation (if multi-player)
Scenario: Rate limiter metrics track allowed requests
Scenario: Rate limiter metrics track throttled requests
Scenario: Rate limiter logs warnings on throttle
Scenario: Rate limiter respects SpaceTraders 2 req/sec limit
Scenario: Rate limiter handles concurrent request threads safely
Scenario: Rate limiter token refill rate is accurate
Scenario: Rate limiter bucket capacity matches max_requests
Scenario: Rate limiter zero token scenario blocks correctly
```

#### Priority 1G: Waypoint Caching (15 scenarios)
**File:** `test/bdd/features/infrastructure/waypoint_cache.feature`

```gherkin
Scenario: Waypoint cache stores waypoints in database
Scenario: Waypoint cache retrieves cached waypoints
Scenario: Waypoint cache respects TTL (24 hours default)
Scenario: Waypoint cache expires stale entries
Scenario: Waypoint cache falls back to API on cache miss
Scenario: Waypoint cache updates database after API fetch
Scenario: Waypoint cache handles system-wide sync
Scenario: Waypoint cache batch insert for performance
Scenario: Waypoint cache deduplicates on symbol
Scenario: Waypoint cache preserves waypoint traits
Scenario: Waypoint cache preserves waypoint coordinates
Scenario: Waypoint cache indexes by system for fast lookup
Scenario: Waypoint cache supports filtering by trait
Scenario: Waypoint cache supports filtering by type
Scenario: Waypoint cache supports has_fuel filter
```

#### Priority 1H: Database Connection & Retry (10 scenarios)
**File:** `test/bdd/features/infrastructure/database_retry.feature`

```gherkin
Scenario: Database connection pool maintains 5 connections
Scenario: Database connection retry on transient failure (max 3)
Scenario: Database connection exponential backoff (1s, 2s, 4s)
Scenario: Database connection fails after max retries
Scenario: Database transaction rollback on error
Scenario: Database transaction commit on success
Scenario: Database query timeout after 30 seconds
Scenario: Database connection health check on borrow
Scenario: Database graceful close releases all connections
Scenario: Database connection pooling prevents exhaustion
```

#### Priority 1I: API Client Retry & Circuit Breaker (10 scenarios)
**File:** `test/bdd/features/infrastructure/api_retry.feature`

```gherkin
Scenario: API client retries on 429 Too Many Requests
Scenario: API client retries on 503 Service Unavailable
Scenario: API client retries on network timeout
Scenario: API client exponential backoff (1s, 2s, 4s, 8s, 16s)
Scenario: API client max 5 retry attempts
Scenario: API client does not retry on 4xx client errors (except 429)
Scenario: API client respects Retry-After header
Scenario: API client circuit breaker opens after 5 consecutive failures
Scenario: API client circuit breaker half-open after 60 seconds
Scenario: API client circuit breaker closes after successful request
```

---

## Phase 2: Integration & End-to-End (Weeks 4-6)
**Goal:** Validate system integration points and CLI workflows
**Scenarios to Add:** 80 scenarios
**New Total:** 643 scenarios

### 2.1 CLI Command Integration (25 scenarios)

#### Priority 2A: Navigate Command E2E
**File:** `test/bdd/features/integration/cli/navigate_command.feature`

```gherkin
Scenario: Navigate command with ship and destination flags
Scenario: Navigate command resolves player from config
Scenario: Navigate command resolves player from --agent flag
Scenario: Navigate command resolves player from --player-id flag
Scenario: Navigate command fails if player not found
Scenario: Navigate command communicates with daemon via Unix socket
Scenario: Navigate command receives container ID response
Scenario: Navigate command prints success message with container ID
Scenario: Navigate command handles daemon connection failure
Scenario: Navigate command handles gRPC errors gracefully
Scenario: Navigate command validates ship symbol format
Scenario: Navigate command validates destination waypoint format
Scenario: Navigate command supports --verbose flag for debugging
```

#### Priority 2B: Contract Workflow CLI
**File:** `test/bdd/features/integration/cli/contract_workflow.feature`

```gherkin
Scenario: Contract negotiate command creates new contract
Scenario: Contract accept command accepts negotiated contract
Scenario: Contract deliver command delivers cargo
Scenario: Contract fulfill command completes contract
Scenario: Contract batch command runs full workflow
Scenario: Contract batch command handles --count flag
Scenario: Contract commands resolve player correctly
Scenario: Contract commands handle API errors gracefully
Scenario: Contract commands validate required flags
Scenario: Contract commands support --verbose output
Scenario: Contract commands print progress updates
Scenario: Contract commands return correct exit codes
```

### 2.2 Repository Integration (25 scenarios)

#### Priority 2C: Ship Repository with API
**File:** `test/bdd/features/integration/persistence/ship_repository.feature`

```gherkin
Scenario: Ship repository fetches ship from API
Scenario: Ship repository handles ship not found (404)
Scenario: Ship repository respects rate limiting
Scenario: Ship repository converts API response to domain entity
Scenario: Ship repository handles fuel data correctly
Scenario: Ship repository handles cargo data correctly
Scenario: Ship repository handles navigation status correctly
Scenario: Ship repository handles flight mode correctly
Scenario: Ship repository lists all ships for player
Scenario: Ship repository filters ships by status (if supported)
Scenario: Ship repository handles API timeout
Scenario: Ship repository handles malformed API response
Scenario: Ship repository validates ship symbol format
```

#### Priority 2D: Waypoint Repository Integration
**File:** `test/bdd/features/integration/persistence/waypoint_repository.feature`

```gherkin
Scenario: Waypoint repository fetches from cache first
Scenario: Waypoint repository fetches from API on cache miss
Scenario: Waypoint repository updates cache after API fetch
Scenario: Waypoint repository lists waypoints by system
Scenario: Waypoint repository filters by trait (MARKETPLACE)
Scenario: Waypoint repository filters by trait (SHIPYARD)
Scenario: Waypoint repository filters by has_fuel
Scenario: Waypoint repository handles system not found
Scenario: Waypoint repository handles empty system
Scenario: Waypoint repository supports pagination
Scenario: Waypoint repository batch sync for performance
Scenario: Waypoint repository deduplicates on sync
```

### 2.3 Routing Service Integration (15 scenarios)

#### Priority 2E: Routing Service gRPC
**File:** `test/bdd/features/integration/routing/routing_service_grpc.feature`

```gherkin
Scenario: Routing service PlanRoute returns valid route
Scenario: Routing service handles insufficient fuel scenario
Scenario: Routing service inserts refuel stops automatically
Scenario: Routing service respects 90% fuel rule
Scenario: Routing service selects optimal flight mode
Scenario: Routing service handles unreachable destination
Scenario: Routing service handles empty graph
Scenario: Routing service OptimizeTour returns valid tour
Scenario: Routing service tour respects return_to_start flag
Scenario: Routing service PartitionFleet distributes markets
Scenario: Routing service handles connection failure
Scenario: Routing service handles timeout (5s for tour, 30s for VRP)
Scenario: Routing service validates input graph format
Scenario: Routing service handles malformed request
Scenario: Routing service returns gRPC status codes correctly
```

### 2.4 Graph Builder Integration (15 scenarios)

#### Priority 2F: Graph Construction from Waypoints
**File:** `test/bdd/features/integration/routing/graph_builder.feature`

```gherkin
Scenario: Graph builder constructs edges from waypoints
Scenario: Graph builder calculates Euclidean distances
Scenario: Graph builder identifies fuel stations
Scenario: Graph builder builds complete system graph
Scenario: Graph builder handles sparse systems
Scenario: Graph builder handles dense systems (50+ waypoints)
Scenario: Graph builder caches graphs in memory
Scenario: Graph builder invalidates cache after waypoint sync
Scenario: Graph builder falls back to database on cache miss
Scenario: Graph builder handles systems with no fuel
Scenario: Graph builder includes all waypoint traits
Scenario: Graph builder validates graph completeness
Scenario: Graph builder performance benchmark (< 100ms per system)
Scenario: Graph builder handles concurrent requests safely
Scenario: Graph builder supports incremental updates
```

---

## Phase 3: Feature Implementation (Weeks 7-9)
**Goal:** Implement missing features with comprehensive tests
**Scenarios to Add:** 65 scenarios
**New Total:** 708 scenarios

### 3.1 Shipyard Operations (20 scenarios)

#### Priority 3A: Purchase Ship Command
**File:** `test/bdd/features/application/shipyard/purchase_ship.feature`

```gherkin
Scenario: Purchase ship when already at shipyard and docked
Scenario: Purchase ship with auto-navigation to shipyard
Scenario: Purchase ship when in orbit at shipyard (auto-dock)
Scenario: Purchase ship with insufficient fuel (auto-refuel + navigate)
Scenario: Purchase ship reduces player credits correctly
Scenario: Purchase ship saves new ship to repository
Scenario: Purchase ship returns new ship entity
Scenario: Purchase ship handles insufficient credits error
Scenario: Purchase ship handles ship type not available error
Scenario: Purchase ship handles shipyard not found error
Scenario: Purchase ship validates ship type format
Scenario: Purchase ship validates shipyard waypoint format
Scenario: Purchase ship auto-discovers nearest shipyard
Scenario: Purchase ship handles navigation failure during approach
```

#### Priority 3B: Batch Purchase Ships
**File:** `test/bdd/features/application/shipyard/batch_purchase.feature`

```gherkin
Scenario: Batch purchase respects quantity limit
Scenario: Batch purchase respects budget limit
Scenario: Batch purchase stops when credits exhausted
Scenario: Batch purchase stops when quantity reached
Scenario: Batch purchase handles partial success
Scenario: Batch purchase returns summary with counts
```

### 3.2 Captain Logging System (15 scenarios)

#### Priority 3C: Log Captain Entry Command
**File:** `test/bdd/features/application/captain/log_entry.feature`

```gherkin
Scenario: Log session_start entry with narrative
Scenario: Log operation_started with event data (ship, operation)
Scenario: Log operation_completed with event data and fleet snapshot
Scenario: Log critical_error with error details
Scenario: Log strategic_decision with reasoning
Scenario: Log session_end with session summary
Scenario: Log entry stores timestamp automatically
Scenario: Log entry associates with player_id
Scenario: Log entry supports tags (mining, iron_ore, attempt_3)
Scenario: Log entry validates entry_type enum
Scenario: Log entry validates narrative not empty
Scenario: Log entry handles JSON event_data correctly
Scenario: Log entry handles JSON fleet_snapshot correctly
```

#### Priority 3D: Get Captain Logs Query
**File:** `test/bdd/features/application/captain/get_logs.feature`

```gherkin
Scenario: Get all captain logs for player
Scenario: Filter logs by entry_type
Scenario: Filter logs by time range (since timestamp)
Scenario: Filter logs by tags
Scenario: Paginate logs with limit/offset
Scenario: Order logs by timestamp descending
Scenario: Get logs returns empty array if no logs
```

### 3.3 Waypoint Query Enhancements (10 scenarios)

#### Priority 3E: List Waypoints Query
**File:** `test/bdd/features/application/waypoints/list_waypoints.feature`

```gherkin
Scenario: List all waypoints in system
Scenario: Filter waypoints by trait (MARKETPLACE)
Scenario: Filter waypoints by trait (SHIPYARD)
Scenario: Filter waypoints by multiple traits (AND logic)
Scenario: Filter waypoints by has_fuel flag
Scenario: Filter waypoints by type (PLANET, MOON, etc)
Scenario: List waypoints returns empty if system not synced
Scenario: List waypoints includes coordinates
Scenario: List waypoints includes traits array
Scenario: List waypoints paginate with limit
```

### 3.4 Player Entity Operations (20 scenarios)

#### Priority 3F: Player Domain Entity
**File:** `test/bdd/features/domain/player/player_entity.feature`

```gherkin
Scenario: Create player with valid data
Scenario: Player stores agent_symbol
Scenario: Player stores credits
Scenario: Player stores starting faction
Scenario: Player stores headquarters waypoint
Scenario: Player tracks creation timestamp
Scenario: Player tracks last_active timestamp
Scenario: Player update_credits adds/subtracts correctly
Scenario: Player validate_credits prevents negative credits
Scenario: Player stores metadata as JSON
Scenario: Player metadata supports custom fields
```

#### Priority 3G: Player Commands
**File:** `test/bdd/features/application/player/player_commands.feature`

```gherkin
Scenario: UpdatePlayerMetadata command stores JSON metadata
Scenario: TouchLastActive command updates timestamp
Scenario: SyncPlayer command fetches from API and updates DB
Scenario: GetPlayer query retrieves by agent_symbol
Scenario: GetPlayer query retrieves by player_id
Scenario: GetPlayer query returns error if not found
Scenario: ListPlayers query returns all players
Scenario: RegisterPlayer command creates new player
Scenario: RegisterPlayer validates agent_symbol uniqueness
```

---

## Phase 4: Edge Cases & Resilience (Weeks 10-12)
**Goal:** Harden the system with edge case coverage
**Scenarios to Add:** 70 scenarios
**New Total:** 778 scenarios

### 4.1 Navigation Edge Cases (20 scenarios)

**File:** `test/bdd/features/application/navigation/edge_cases.feature`

```gherkin
Scenario: Navigate to same waypoint returns immediately
Scenario: Navigate with exact fuel requirement (no margin)
Scenario: Navigate fails if no route exists (isolated waypoint)
Scenario: Navigate handles multi-hop refuel (3+ stops)
Scenario: Navigate selects BURN mode for urgent travel
Scenario: Navigate selects DRIFT mode when fuel scarce
Scenario: Navigate handles route segment failure mid-route
Scenario: Navigate resumes after ship was stuck IN_TRANSIT
Scenario: Navigate handles API rate limit during flight
Scenario: Navigate handles arrival timestamp precision
Scenario: Navigate handles concurrent navigate requests (locking)
Scenario: Navigate validates destination in same system
Scenario: Navigate prevents navigation to non-existent waypoint
Scenario: Navigate handles fuel consumption rounding errors
Scenario: Navigate handles zero-distance waypoint (orbital siblings)
Scenario: Navigate handles maximum distance in system
Scenario: Navigate Flight mode transition updates API correctly
Scenario: Navigate preserves cargo during transit
Scenario: Navigate updates location atomically on arrival
Scenario: Navigate handles ship destroyed during transit (edge case)
```

### 4.2 Contract Workflow Edge Cases (15 scenarios)

**File:** `test/bdd/features/application/contracts/edge_cases.feature`

```gherkin
Scenario: Accept already accepted contract returns idempotent success
Scenario: Deliver cargo when already at destination
Scenario: Deliver cargo with multi-trip (cargo capacity < required units)
Scenario: Deliver partial cargo handles remainder correctly
Scenario: Fulfill contract when delivery incomplete fails validation
Scenario: Negotiate contract when already have active contract
Scenario: Contract profitability calculation with zero margin
Scenario: Contract profitability polling timeout after 60 seconds
Scenario: Contract delivery handles jettison overflow correctly
Scenario: Contract purchase finds cheapest market across system
Scenario: Contract purchase handles no markets selling good
Scenario: Contract purchase handles insufficient market supply
Scenario: Batch workflow handles contract negotiation failure
Scenario: Batch workflow stops after consecutive failures (3)
Scenario: Batch workflow reports accurate profit/loss summary
```

### 4.3 Scouting Edge Cases (10 scenarios)

**File:** `test/bdd/features/application/scouting/edge_cases.feature`

```gherkin
Scenario: Scout markets with single ship (no partitioning)
Scenario: Scout markets with more ships than markets (idle ships)
Scenario: Scout tour with single market (stationary scout)
Scenario: Scout tour handles ship already at market (skip navigate)
Scenario: Scout tour infinite iterations (-1) runs until stopped
Scenario: Scout tour return-to-start false ends at last market
Scenario: Scout markets VRP optimization timeout fallback
Scenario: Scout markets handles ship navigation failure mid-tour
Scenario: Scout markets persists market data to database
Scenario: Scout markets deduplicates market data within 60 seconds
```

### 4.4 Error Handling & Validation (15 scenarios)

**File:** `test/bdd/features/application/error_handling.feature`

```gherkin
Scenario: Command handler wraps domain errors with context
Scenario: Command handler logs errors before returning
Scenario: Command handler returns typed errors (not generic)
Scenario: Domain error ErrInsufficientFuel propagates correctly
Scenario: Domain error ErrInvalidState propagates correctly
Scenario: Domain error ErrInvalidTransition propagates correctly
Scenario: Repository error wraps database errors
Scenario: API client error includes status code
Scenario: API client error includes response body
Scenario: Validation error lists all validation failures
Scenario: Concurrent modification error triggers retry (optimistic locking)
Scenario: Timeout error includes operation context
Scenario: Circuit breaker error includes retry-after duration
Scenario: Rate limit error suggests wait duration
Scenario: Not found error distinguishes ship/player/waypoint
```

### 4.5 Concurrency & Race Conditions (10 scenarios)

**File:** `test/bdd/features/application/concurrency.feature`

```gherkin
Scenario: Concurrent navigate commands on same ship (second fails with lock)
Scenario: Concurrent cargo operations on same ship (serialized)
Scenario: Concurrent contract delivery from different ships (allowed)
Scenario: Concurrent daemon container creation (thread-safe)
Scenario: Concurrent graph builder requests (cache safe)
Scenario: Concurrent waypoint repository sync (deduplication works)
Scenario: Concurrent API client requests respect rate limit
Scenario: Concurrent database transactions don't deadlock
Scenario: Container restart during active iteration (graceful)
Scenario: Daemon shutdown during active navigation (graceful)
```

---

## Implementation Guidelines

### Test Writing Best Practices

#### 1. Feature File Structure
```gherkin
Feature: [Clear, descriptive title]
  As a [actor]
  I want to [action]
  So that [business value]

  Background:
    Given [common setup for all scenarios]

  # Group related scenarios with comments
  # Happy Path Scenarios
  Scenario: [Describe the main success path]
    Given [initial state]
    When [action]
    Then [expected outcome]
    And [additional assertions]

  # Edge Cases
  Scenario: [Describe edge case]
    ...

  # Error Handling
  Scenario: [Describe error scenario]
    ...
```

#### 2. Step Definition Principles
- **One step = one assertion or action**
- **Reuse existing step definitions** (check `test/bdd/steps/`)
- **Use helper functions** in `test/helpers/` for mock setup
- **Keep steps at behavioral level** (not implementation details)
- **Use table-driven tests** for multiple similar scenarios

#### 3. Mock Setup Strategy
```go
// Use existing mock helpers
mockAPIClient := helpers.NewMockAPIClient()
mockAPIClient.On("GetShip", "SHIP-1").Return(ship, nil)

// Use in-memory repositories for speed
shipRepo := persistence.NewInMemoryShipRepository()
```

#### 4. Test Data Management
- **Use consistent test data** across scenarios
- **Default player:** ID=1, agent="TEST-AGENT"
- **Default system:** "X1-TEST"
- **Default waypoints:** "X1-TEST-A1", "X1-TEST-B2", etc.

### Phase Completion Criteria

#### Phase 1 Complete When:
- [ ] All 64 daemon scenarios pass
- [ ] All 50 infrastructure scenarios pass
- [ ] Daemon can handle 10+ concurrent containers
- [ ] Rate limiter enforces 2 req/sec accurately
- [ ] Waypoint cache hit rate > 95% in tests
- [ ] Container recovery succeeds in <5 seconds

#### Phase 2 Complete When:
- [ ] All 80 integration scenarios pass
- [ ] CLI commands work end-to-end
- [ ] Routing service integration reliable
- [ ] Graph builder handles 50+ waypoint systems
- [ ] Repository tests cover all CRUD operations
- [ ] Integration tests run in <2 minutes total

#### Phase 3 Complete When:
- [ ] All 65 feature scenarios pass
- [ ] Shipyard operations fully functional
- [ ] Captain logging persists correctly
- [ ] Waypoint queries support all filters
- [ ] Player operations complete
- [ ] Feature tests run in <1 minute total

#### Phase 4 Complete When:
- [ ] All 70 edge case scenarios pass
- [ ] Error handling comprehensive
- [ ] Concurrency tests pass consistently
- [ ] No race conditions in tests (run with `-race`)
- [ ] All timeouts handled gracefully
- [ ] Full test suite runs in <5 minutes

## Timeline & Resource Allocation

### Week-by-Week Breakdown

**Weeks 1-3: Phase 1 (Production Reliability)**
- Week 1: Daemon lifecycle + health monitoring (35 scenarios)
- Week 2: Ship assignment + logging + server (29 scenarios)
- Week 3: Infrastructure tests (50 scenarios)

**Weeks 4-6: Phase 2 (Integration)**
- Week 4: CLI integration (25 scenarios)
- Week 5: Repository integration (25 scenarios)
- Week 6: Routing + graph builder (30 scenarios)

**Weeks 7-9: Phase 3 (Features)**
- Week 7: Shipyard operations (20 scenarios)
- Week 8: Captain logging + waypoints (25 scenarios)
- Week 9: Player operations (20 scenarios)

**Weeks 10-12: Phase 4 (Hardening)**
- Week 10: Navigation edge cases (20 scenarios)
- Week 11: Workflow edge cases + error handling (25 scenarios)
- Week 12: Concurrency + final polish (25 scenarios)

### Effort Estimates

**Per Scenario Estimates:**
- Simple scenario (happy path): 15-30 minutes
- Complex scenario (edge case): 30-60 minutes
- Integration scenario: 45-90 minutes

**Total Estimated Hours:**
- Phase 1: 80-100 hours (114 scenarios × 0.7 hours avg)
- Phase 2: 70-90 hours (80 scenarios × 0.9 hours avg)
- Phase 3: 50-65 hours (65 scenarios × 0.8 hours avg)
- Phase 4: 60-75 hours (70 scenarios × 0.9 hours avg)

**Total: 260-330 hours** (6.5-8.25 weeks at 40 hours/week)

## Success Metrics

### Coverage Metrics
- **Total Scenarios:** 750-850 (from 449)
- **Domain Coverage:** Maintain 287 scenarios (already excellent)
- **Application Coverage:** 220+ scenarios (from 153)
- **Infrastructure Coverage:** 50+ scenarios (from 0)
- **Integration Coverage:** 80+ scenarios (from 9)
- **Daemon Coverage:** 64+ scenarios (from 0)

### Quality Metrics
- **All tests pass:** 100% pass rate
- **Test execution time:** <5 minutes for full suite
- **Test flakiness:** <0.1% (1 in 1000 runs)
- **Code coverage:** >80% of production code
- **Race conditions:** 0 detected with `-race` flag

### Business Impact Metrics
- **Production incidents:** Reduce by 70%+ with daemon tests
- **Integration bugs:** Catch 90%+ before production
- **Deployment confidence:** High (comprehensive test coverage)
- **Refactoring safety:** High (behavioral tests protect against regression)

## Risk Mitigation

### Identified Risks

| Risk | Impact | Likelihood | Mitigation |
|------|--------|------------|------------|
| Test writing takes longer than estimated | High | Medium | Start with critical paths first; defer nice-to-haves |
| Tests become flaky due to timing issues | High | Medium | Use test clocks; avoid real time dependencies |
| Integration tests slow down CI | Medium | High | Run integration tests in parallel; optimize fixtures |
| Mock complexity makes tests brittle | Medium | Medium | Minimize mocks; use real implementations in tests |
| Concurrent test failures hard to debug | High | Low | Add detailed logging; use race detector |

### Mitigation Strategies

1. **Prioritization:** Implement critical paths first (daemon, integration)
2. **Incremental Delivery:** Each phase delivers value independently
3. **Continuous Validation:** Run tests on every commit
4. **Parallel Work:** Domain tests can be written in parallel with integration tests
5. **Early Feedback:** Demo phase completions to stakeholders

## Maintenance Plan

### Ongoing Test Maintenance

**Weekly:**
- [ ] Review test failures and fix flaky tests
- [ ] Update tests for new feature implementations
- [ ] Ensure tests run in <5 minutes

**Monthly:**
- [ ] Analyze test coverage and identify gaps
- [ ] Refactor duplicate test code
- [ ] Update test documentation

**Quarterly:**
- [ ] Performance benchmark test execution time
- [ ] Review test architecture for improvements
- [ ] Conduct test effectiveness review (bugs caught vs missed)

### Adding New Tests (Process)

**When adding new features:**
1. Write BDD feature file FIRST (TDD approach)
2. Implement step definitions
3. Write failing tests
4. Implement feature to make tests pass
5. Refactor while tests remain green

**When fixing bugs:**
1. Write regression test that reproduces bug
2. Verify test fails
3. Fix bug
4. Verify test passes
5. Add to regression suite

## Appendix: Test File Organization

### Directory Structure (Target State)

```
test/
├── bdd/
│   ├── features/
│   │   ├── adapters/
│   │   │   └── persistence/
│   │   │       └── market_repository.feature (9 scenarios) ✅
│   │   ├── application/
│   │   │   ├── captain/
│   │   │   │   ├── log_entry.feature (15 scenarios) 🔴 NEW
│   │   │   │   └── get_logs.feature (7 scenarios) 🔴 NEW
│   │   │   ├── contracts/
│   │   │   │   ├── [existing files] (35 scenarios) ✅
│   │   │   │   └── edge_cases.feature (15 scenarios) 🔴 NEW
│   │   │   ├── navigation/
│   │   │   │   ├── [existing files] (40 scenarios) ✅
│   │   │   │   └── edge_cases.feature (20 scenarios) 🔴 NEW
│   │   │   ├── player/
│   │   │   │   └── player_commands.feature (9 scenarios) 🔴 NEW
│   │   │   ├── scouting/
│   │   │   │   ├── [existing files] (14 scenarios) ✅
│   │   │   │   └── edge_cases.feature (10 scenarios) 🔴 NEW
│   │   │   ├── shipyard/
│   │   │   │   ├── purchase_ship.feature (14 scenarios) 🔴 NEW
│   │   │   │   └── batch_purchase.feature (6 scenarios) 🔴 NEW
│   │   │   ├── waypoints/
│   │   │   │   └── list_waypoints.feature (10 scenarios) 🔴 NEW
│   │   │   ├── concurrency.feature (10 scenarios) 🔴 NEW
│   │   │   └── error_handling.feature (15 scenarios) 🔴 NEW
│   │   ├── daemon/
│   │   │   ├── container_lifecycle.feature (20 scenarios) 🔴 NEW
│   │   │   ├── health_monitor.feature (15 scenarios) 🔴 NEW
│   │   │   ├── ship_assignment.feature (12 scenarios) 🔴 NEW
│   │   │   ├── daemon_server.feature (10 scenarios) 🔴 NEW
│   │   │   └── container_logging.feature (7 scenarios) 🔴 NEW
│   │   ├── domain/
│   │   │   ├── container/
│   │   │   │   └── container_entity.feature ✅
│   │   │   ├── contract/
│   │   │   │   └── contract_entity.feature ✅
│   │   │   ├── market/
│   │   │   │   ├── market.feature ✅
│   │   │   │   └── trade_good.feature ✅
│   │   │   ├── navigation/
│   │   │   │   ├── ship_entity.feature ✅
│   │   │   │   └── route_entity.feature ✅
│   │   │   ├── player/
│   │   │   │   └── player_entity.feature (11 scenarios) 🔴 NEW
│   │   │   ├── shared/
│   │   │   │   └── [value objects] ✅
│   │   │   └── trading/
│   │   │       └── market_entity.feature ✅
│   │   ├── infrastructure/
│   │   │   ├── api_rate_limiter.feature (15 scenarios) 🔴 NEW
│   │   │   ├── api_retry.feature (10 scenarios) 🔴 NEW
│   │   │   ├── database_retry.feature (10 scenarios) 🔴 NEW
│   │   │   └── waypoint_cache.feature (15 scenarios) 🔴 NEW
│   │   └── integration/
│   │       ├── cli/
│   │       │   ├── navigate_command.feature (13 scenarios) 🔴 NEW
│   │       │   └── contract_workflow.feature (12 scenarios) 🔴 NEW
│   │       ├── persistence/
│   │       │   ├── ship_repository.feature (13 scenarios) 🔴 NEW
│   │       │   └── waypoint_repository.feature (12 scenarios) 🔴 NEW
│   │       └── routing/
│   │           ├── routing_service_grpc.feature (15 scenarios) 🔴 NEW
│   │           └── graph_builder.feature (15 scenarios) 🔴 NEW
│   └── steps/
│       ├── [existing step files] ✅
│       ├── captain_steps.go 🔴 NEW
│       ├── concurrency_steps.go 🔴 NEW
│       ├── daemon_steps.go 🔴 NEW
│       ├── error_handling_steps.go 🔴 NEW
│       ├── infrastructure_steps.go 🔴 NEW
│       ├── integration_steps.go 🔴 NEW
│       ├── player_steps.go (enhanced) 🔴 NEW
│       └── shipyard_steps.go 🔴 NEW
└── helpers/
    ├── [existing helpers] ✅
    ├── mock_captain_repository.go 🔴 NEW
    ├── mock_container_manager.go 🔴 NEW
    ├── mock_health_monitor.go 🔴 NEW
    └── test_clock.go 🔴 NEW (for time-dependent tests)
```

## Conclusion

This plan provides a clear, phased approach to closing the testing gap. By prioritizing production reliability (daemon, infrastructure), then integration, then features, we ensure the system is stable and maintainable at each milestone.

**Key Takeaways:**
1. **Not all 749 missing scenarios are gaps** - ~300-400 critical ones
2. **Architecture improvements eliminated ~200 tests** - this is good
3. **Focus on daemon + integration first** - highest business value
4. **Implement features with tests** - shipyard, captain logging, etc.
5. **Harden with edge cases last** - polish after core is solid

**Expected Outcome:**
- **750-850 comprehensive BDD scenarios**
- **Production-ready daemon with full observability**
- **Bulletproof integration points**
- **Complete feature parity with Python (where needed)**
- **Hardened edge case handling**
- **5-minute full test suite execution**
- **High confidence for production deployment**

---

**Document Version:** 1.0
**Last Updated:** 2025-11-13
**Owner:** SpaceTraders Go Bot Team
**Status:** 📋 Planning Phase

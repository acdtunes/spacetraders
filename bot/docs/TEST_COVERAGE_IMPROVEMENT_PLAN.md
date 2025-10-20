# Test Coverage Improvement Plan
## SpaceTraders Bot - 23.5% → 85%+ Coverage Initiative

**Document Version:** 1.0
**Created:** 2025-10-19
**Owner:** Engineering Team
**Timeline:** 8 weeks (phased approach)
**Status:** 📋 Planning

---

## Table of Contents

1. [Executive Summary](#executive-summary)
2. [Current State Analysis](#current-state-analysis)
3. [Goals & Objectives](#goals--objectives)
4. [Implementation Strategy](#implementation-strategy)
5. [Phase-by-Phase Plan](#phase-by-phase-plan)
6. [Resource Requirements](#resource-requirements)
7. [Risk Management](#risk-management)
8. [Success Metrics](#success-metrics)
9. [Timeline & Milestones](#timeline--milestones)
10. [Deliverables](#deliverables)
11. [Quality Gates](#quality-gates)
12. [Tools & Infrastructure](#tools--infrastructure)
13. [Team Responsibilities](#team-responsibilities)
14. [Progress Tracking](#progress-tracking)
15. [Appendices](#appendices)

---

## Executive Summary

### Mission Statement

**Increase test code coverage from 23.5% to 85%+ over 8 weeks through a systematic, phased approach that prioritizes critical core components, complex business logic, and establishes robust quality gates to prevent future regressions.**

### Why This Matters

**Current Situation:**
- Overall coverage: **23.5%** (2,301 / 9,794 statements)
- Core package: **24.7%** coverage (4,497 statements)
- Operations package: **18.2%** coverage (4,781 statements)
- 15 critical files with <30% coverage representing **6,444 statements**

**Business Impact:**
- **Risk:** Core state machine (`ship_controller.py` - 7.2%) untested = ship strand scenarios in production
- **Revenue Risk:** Contract profitability (`contracts.py` - 7.0%) not validated
- **Operational Risk:** Fleet coordination (`scout_coordinator.py` - 8.7%) failures
- **Quality Risk:** Cannot confidently refactor or extend features

**Expected Outcomes:**
- Production-ready quality standard (85%+ coverage)
- Confidence in refactoring and feature development
- Regression prevention through automated testing
- Living documentation via BDD scenarios
- Faster development velocity (catch bugs early)

### Key Highlights

**Phased Approach:**
```
Week 1     → 35% coverage  (+11.5%, Quick Wins)
Weeks 2-3  → 50% coverage  (+15.0%, Core Infrastructure)
Weeks 4-6  → 65% coverage  (+15.0%, Business Logic)
Weeks 7-8  → 80% coverage  (+15.0%, Operations)
Ongoing    → 85%+ coverage (+5%+, Advanced Features)
```

**Investment:**
- **Time:** 280-352 hours (1 FTE @ 50% for 8 weeks)
- **Infrastructure:** ~6 hours CI/CD setup (one-time)
- **Training:** 5 hours (branch coverage, coverage-driven development, Hypothesis)

**ROI:**
- **Immediate:** Catch bugs before production (reduced support burden)
- **Short-term:** Faster code reviews (clear test coverage)
- **Long-term:** Enable confident refactoring and feature velocity

---

## Current State Analysis

### Coverage Metrics (Baseline: 2025-10-19)

#### Overall Statistics

| Metric | Value |
|--------|-------|
| **Total Statements** | 9,794 |
| **Covered Statements** | 2,301 |
| **Missing Statements** | 7,493 |
| **Overall Coverage** | **23.5%** |
| **Coverage Target** | **85%** |
| **Coverage Gap** | **-61.5%** |

#### Package Breakdown

| Package | Statements | Covered | Missing | Coverage | Status | Target |
|---------|------------|---------|---------|----------|--------|--------|
| **helpers** | 39 | 34 | 5 | 87.2% | 🟢 Excellent | 90%+ |
| **cli** | 349 | 275 | 74 | 78.8% | 🟢 Good | 85%+ |
| **__init__.py** | 12 | 12 | 0 | 100% | 🟢 Perfect | 100% |
| **core** | 4,497 | 1,112 | 3,385 | 24.7% | 🔴 Critical | 75%+ |
| **operations** | 4,781 | 868 | 3,913 | 18.2% | 🔴 Critical | 70%+ |
| **integrations** | 116 | 0 | 116 | 0.0% | ⚫ Untested | 60%+ |

#### Critical Coverage Gaps

**15 files with >100 statements and <30% coverage:**

| Rank | File | Package | Statements | Coverage | Priority | Phase |
|------|------|---------|------------|----------|----------|-------|
| 1 | `multileg_trader.py` | operations | 1,880 | 23.3% | 🔥 CRITICAL | 3 |
| 2 | `scout_coordinator.py` | core | 610 | 8.7% | 🔥 CRITICAL | 3 |
| 3 | `contracts.py` | operations | 541 | 7.0% | 🔥 CRITICAL | 3 |
| 4 | `routing_legacy.py` | core | 443 | 0.0% | ⚫ Legacy | N/A |
| 5 | `ship_controller.py` | core | 347 | 7.2% | 🔥 CRITICAL | 2 |
| 6 | `routing.py` | operations | 344 | 3.5% | 🔥 CRITICAL | 4 |
| 7 | `captain_logging.py` | operations | 308 | 14.0% | 🔴 High | 1 |
| 8 | `mining.py` | operations | 299 | 20.1% | 🔴 High | 4 |
| 9 | `analysis.py` | operations | 240 | 2.1% | 🔥 CRITICAL | 4 |
| 10 | `daemon_manager.py` | core | 213 | 14.1% | 🔴 High | 1 |
| 11 | `ortools_mining_optimizer.py` | core | 209 | 22.5% | 🔴 High | 5 |
| 12 | `market_partitioning.py` | core | 196 | 21.9% | 🔴 High | 5 |
| 13 | `assignments.py` | operations | 191 | 5.2% | 🔥 CRITICAL | 2 |
| 14 | `market_data.py` | core | 171 | 13.5% | 🔴 High | 5 |
| 15 | `routing.py` | core | 164 | 28.0% | 🟡 Medium | 5 |

**Total Critical Gap:** 6,444 statements @ 13.4% average coverage

### Test Infrastructure Inventory

#### Existing Assets ✅

**Test Framework:**
- ✅ pytest 7.0+ (installed and configured)
- ✅ pytest-bdd 6.0+ (100% BDD migration complete)
- ✅ pytest-cov 4.0+ (coverage measurement)
- ✅ 552 BDD scenarios across 48 feature files
- ✅ Comprehensive MockAPIClient (879 lines)

**Test Organization:**
```
tests/
├── bdd/
│   ├── features/               # 48 .feature files
│   │   ├── unit/               # Unit-level BDD
│   │   ├── trading/            # 7 trading features
│   │   ├── navigation/         # 12 navigation features
│   │   ├── operations/         # Operations features
│   │   ├── scout/              # Scout coordination
│   │   └── ...                 # Other domains
│   │
│   └── steps/                  # ~10,069 lines of step definitions
│       ├── fixtures/
│       │   └── mock_api.py     # 879-line comprehensive mock
│       ├── unit/
│       ├── trading/
│       ├── navigation/
│       └── ...
│
├── conftest.py                 # pytest-bdd configuration
├── requirements.txt            # Test dependencies
└── [analysis tools]
    ├── analyze_coverage.py     # Coverage report generator
    └── analyze_overlaps.py     # Duplicate scenario detector
```

**Documentation:**
- ✅ `TESTING_GUIDE.md` (531 lines) - Comprehensive BDD guide
- ✅ `COVERAGE_REPORT.md` (353 lines) - Current analysis
- ✅ `docs/TEST_COVERAGE_BEST_PRACTICES.md` - Industry standards
- ✅ `docs/PYTEST_COVERAGE_RESEARCH.md` - Framework documentation

#### Missing Components ⚠️

**CI/CD Integration:**
- ⚠️ No GitHub Actions workflow for automated testing
- ⚠️ No coverage threshold enforcement in CI
- ⚠️ No coverage reporting service (Codecov/Coveralls)
- ⚠️ No coverage badge in README

**Configuration:**
- ⚠️ No `.coveragerc` or coverage section in `pyproject.toml`
- ⚠️ Branch coverage not enabled
- ⚠️ No exclusion patterns configured
- ⚠️ No per-file coverage thresholds

**Quality Gates:**
- ⚠️ No coverage ratcheting mechanism
- ⚠️ No differential coverage for new code
- ⚠️ No mutation testing for test quality

### Strengths & Weaknesses

#### Strengths 💪

1. **Excellent Test Foundation:**
   - 100% BDD migration complete (Phase 5 finished 2025-10-19)
   - Comprehensive MockAPIClient covers all SpaceTraders API endpoints
   - Clear testing patterns established and documented
   - Team familiar with pytest-bdd workflow

2. **High Coverage Areas:**
   - `helpers` package: 87.2% coverage (utilities well-tested)
   - `cli` package: 78.8% coverage (argument parsing validated)
   - `purchasing.py`: 79.1% coverage (best operation handler)

3. **Documentation:**
   - Comprehensive `TESTING_GUIDE.md` with examples
   - Existing coverage analysis and reporting tools
   - Clear BDD philosophy and patterns

#### Weaknesses 🔴

1. **Core Components Untested:**
   - State machine (`ship_controller.py` - 7.2%) = production risk
   - Fleet coordination (`scout_coordinator.py` - 8.7%) = operational risk
   - Process management (`daemon_manager.py` - 14.1%) = stability risk

2. **Complex Logic Uncovered:**
   - Largest file (`multileg_trader.py` - 1,880 lines, 23.3%) = refactoring blocker
   - Contract profitability (`contracts.py` - 7.0%) = revenue risk
   - Mining operations (`mining.py` - 20.1%) = workflow risk

3. **No Quality Gates:**
   - No CI enforcement of coverage standards
   - Coverage can regress without detection
   - No visibility into coverage trends

---

## Goals & Objectives

### Primary Goal

**Achieve 85%+ test coverage across the SpaceTraders bot codebase within 8 weeks, prioritizing critical core components and establishing automated quality gates to prevent future regressions.**

### SMART Objectives

#### 1. Coverage Targets (Specific, Measurable)

| Timeframe | Coverage | Gain | Key Achievements |
|-----------|----------|------|------------------|
| **Week 1** | 35% | +11.5% | daemon_manager, captain_logging, purchasing edge cases |
| **Weeks 2-3** | 50% | +15.0% | ship_controller, assignment_manager, smart_navigator |
| **Weeks 4-6** | 65% | +15.0% | multileg_trader, scout_coordinator, contracts |
| **Weeks 7-8** | 80% | +15.0% | mining, routing, analysis, fleet operations |
| **Ongoing** | 85%+ | +5%+ | ortools_router, market_data, mcp_bridge |

#### 2. Package-Level Targets (Achievable, Relevant)

| Package | Current | Target | Gain | Status |
|---------|---------|--------|------|--------|
| **core** | 24.7% | **75%** | +50.3% | 🔴 Critical |
| **operations** | 18.2% | **70%** | +51.8% | 🔴 Critical |
| **integrations** | 0.0% | **60%** | +60.0% | 🟡 Medium |
| **cli** | 78.8% | **85%** | +6.2% | 🟢 Maintain |
| **helpers** | 87.2% | **90%** | +2.8% | 🟢 Maintain |

#### 3. Quality Objectives (Time-Bound)

**By Week 1:**
- [ ] CI/CD workflow configured with coverage enforcement
- [ ] Coverage baseline established and tracked
- [ ] First quick wins delivered (3 files improved)

**By Week 3:**
- [ ] All core state machines tested (ship_controller)
- [ ] Core infrastructure >75% coverage
- [ ] Branch coverage >70% for critical components

**By Week 6:**
- [ ] Complex business logic validated (trader, contracts, scouts)
- [ ] Circuit breaker edge cases covered
- [ ] Zero files with >100 statements and <30% coverage

**By Week 8:**
- [ ] All operation handlers >70% coverage
- [ ] Overall coverage >80%
- [ ] Coverage ratcheting enforced

**Ongoing:**
- [ ] Maintain 85%+ coverage
- [ ] Test quality validated via mutation testing (optional)
- [ ] Coverage as part of definition of done

### Success Criteria

**Quantitative:**
- ✅ Overall coverage ≥85%
- ✅ Core package ≥75%
- ✅ Operations package ≥70%
- ✅ Branch coverage ≥75%
- ✅ Zero critical files with <30% coverage

**Qualitative:**
- ✅ Team confidence in making changes
- ✅ BDD scenarios serve as living documentation
- ✅ Refactoring possible without fear
- ✅ New features include tests by default

---

## Implementation Strategy

### Guiding Principles

1. **Risk-Based Prioritization:** Target highest-impact, highest-risk modules first
2. **Incremental Value:** Each phase delivers measurable improvement
3. **BDD Philosophy:** All tests follow Gherkin scenario format (established pattern)
4. **Quality Over Quantity:** 85% done well > 95% done poorly
5. **Test Pyramid:** Maintain 50% unit / 30% integration / 20% e2e distribution
6. **Coverage Ratcheting:** Never allow coverage to decrease

### Phased Approach

#### Phase Selection Rationale

**Phase 1: Quick Wins (Week 1)**
- **Why First:** Build momentum, demonstrate ROI, easiest targets
- **Files:** partially covered utilities, logging, daemon management
- **Impact:** +11.5% coverage with minimal complexity

**Phase 2: Core Infrastructure (Weeks 2-3)**
- **Why Second:** Foundation for everything else, highest risk if broken
- **Files:** state machine, assignment management, smart navigation
- **Impact:** +15% coverage, enables confident refactoring

**Phase 3: Business Logic (Weeks 4-6)**
- **Why Third:** Complex workflows require stable foundation, highest LOC
- **Files:** trading logic, fleet coordination, contract evaluation
- **Impact:** +15% coverage, validates revenue-critical paths

**Phase 4: Operations (Weeks 7-8)**
- **Why Fourth:** Operation handlers depend on core + business logic
- **Files:** mining, routing, analysis, fleet status
- **Impact:** +15% coverage, comprehensive workflow validation

**Phase 5: Advanced Features (Ongoing)**
- **Why Last:** Stretch goal, requires advanced techniques (property-based testing)
- **Files:** OR-Tools integration, market data, MCP bridge
- **Impact:** +5%+ coverage, production-ready quality

### Testing Strategy

#### Test Types & Distribution

**Test Pyramid (50/30/20):**
```
         ╱╲
        ╱  ╲     20% E2E (BDD integration scenarios)
       ╱────╲
      ╱      ╲   30% Integration (BDD domain scenarios)
     ╱────────╲
    ╱          ╲ 50% Unit (BDD unit scenarios)
   ╱────────────╲
```

**Coverage Types:**
- **Line Coverage:** Which lines executed (primary metric)
- **Branch Coverage:** All paths in conditionals tested (quality metric, >75% target)
- **Differential Coverage:** New code must have 100% coverage (future enforcement)

#### BDD Scenario Patterns

**Given/When/Then Structure:**
```gherkin
Feature: Ship State Machine
  As a ship controller
  I want to manage ship state transitions
  So that ships can perform operations safely

  Background:
    Given a mock API client
    And a ship "TEST-SHIP" exists

  Scenario: Orbit from DOCKED state
    Given the ship "TEST-SHIP" is DOCKED at "X1-TEST-A1"
    And the ship has 400/400 fuel
    When I orbit the ship
    Then the ship should be IN_ORBIT
    And the ship should still be at "X1-TEST-A1"

  Scenario Outline: Navigation from different states
    Given the ship "TEST-SHIP" is <initial_state> at "<start>"
    And the ship has <fuel>/<capacity> fuel
    When I navigate to "<destination>"
    Then the ship should be <final_state>
    And fuel should be <expected_fuel>

    Examples:
      | initial_state | start       | fuel | capacity | destination | final_state | expected_fuel |
      | IN_ORBIT      | X1-TEST-A1  | 400  | 400      | X1-TEST-B2  | IN_TRANSIT  | 300           |
      | DOCKED        | X1-TEST-A1  | 400  | 400      | X1-TEST-B2  | IN_TRANSIT  | 300           |
```

#### Mock & Fixture Strategy

**MockAPIClient Usage:**
- ✅ Mock at system boundary (SpaceTraders API)
- ✅ DON'T mock internal business logic
- ✅ Use deterministic behavior (no randomness)
- ✅ Comprehensive coverage of all API endpoints

**Fixture Scopes:**
- `function`: Default for most fixtures (clean slate per test)
- `module`: Expensive setup shared across file (graph building)
- `session`: Once per test run (mock API client initialization)

---

## Phase-by-Phase Plan

### Phase 1: Quick Wins (Week 1)

**Timeline:** Days 1-5
**Coverage Goal:** 23.5% → 35% (+11.5%, ~600 statements)

#### Target Files

##### 1. `purchasing.py` (187 statements, 79% → 95%)

**Current State:** Best-tested operation handler, low-hanging fruit

**Scenarios to Add (6 scenarios):**
- Purchase with insufficient credits (error path)
- Purchase with insufficient cargo capacity (error path)
- Purchase with exact budget match (edge case)
- Purchase with market price surge (circuit breaker)
- Purchase with invalid trade symbol (validation)
- Purchase with API failure retry (resilience)

**Deliverables:**
- `tests/bdd/features/operations/purchasing_edge_cases.feature`
- `tests/bdd/steps/operations/test_purchasing_edge_cases_steps.py`

**Estimated Effort:** 4 hours

##### 2. `captain_logging.py` (308 statements, 14% → 60%)

**Current State:** Mission-critical logging, mostly untested

**Scenarios to Add (11 scenarios):**
- Initialize captain log for new agent
- Start logging session with objective
- Log operation started event
- Log operation completed with narrative (required field validation)
- Log operation completed without narrative (should fail)
- Scout operations ignored in logging
- End session archives to JSON
- Search logs by tag and timeframe
- Generate executive report
- Concurrent writes with file locking
- Session metadata includes duration and net profit

**Deliverables:**
- `tests/bdd/features/operations/captain_logging.feature`
- `tests/bdd/steps/operations/test_captain_logging_steps.py`

**Estimated Effort:** 12 hours

##### 3. `daemon_manager.py` (213 statements, 14% → 50%)

**Current State:** Process lifecycle untested, stability risk

**Scenarios to Add (9 scenarios):**
- Start daemon process
- Stop daemon gracefully (SIGTERM)
- Get daemon status (running)
- Get daemon status (stopped)
- Detect stale daemon (PID exists but process dead)
- Cleanup stale daemons
- List all daemons
- Tail daemon logs
- PID file management (create, read, delete)

**Deliverables:**
- `tests/bdd/features/core/daemon_lifecycle.feature`
- `tests/bdd/steps/core/test_daemon_lifecycle_steps.py`

**Estimated Effort:** 8 hours

#### Phase 1 Milestones

| Day | Milestone | Deliverables |
|-----|-----------|--------------|
| Day 1 | Setup & Planning | Branch created, files reviewed, scenarios drafted |
| Day 2 | purchasing.py complete | Feature + steps, 95%+ coverage verified |
| Day 3 | captain_logging.py 50% | 5-6 scenarios implemented |
| Day 4 | captain_logging.py complete + daemon_manager 50% | All logging scenarios + 4-5 daemon scenarios |
| Day 5 | Phase 1 complete | All scenarios passing, coverage ≥35%, PR created |

#### Phase 1 Success Criteria

- [ ] Overall coverage ≥35%
- [ ] `purchasing.py` ≥95% coverage
- [ ] `captain_logging.py` ≥60% coverage
- [ ] `daemon_manager.py` ≥50% coverage
- [ ] All 26 new scenarios passing
- [ ] Zero existing tests broken
- [ ] PR reviewed and merged

**Total Estimated Effort:** 24 hours (3 days @ 8 hours/day)

---

### Phase 2: Core Infrastructure (Weeks 2-3)

**Timeline:** Days 6-15
**Coverage Goal:** 35% → 50% (+15%, ~844 statements)

#### Target Files

##### 1. `ship_controller.py` (347 statements, 7% → 85%) 🔥 CRITICAL

**Priority:** HIGHEST - Core state machine everything depends on

**Current State:**
- State machine (DOCKED/IN_ORBIT/IN_TRANSIT) untested
- Navigation operations untested
- Cargo operations untested
- **Risk:** Ship strand scenarios in production

**Scenarios to Add (25+ scenarios across 3 features):**

**Feature 1: `ship_state_machine.feature` (8 scenarios)**
- DOCKED → IN_ORBIT (orbit)
- IN_ORBIT → DOCKED (dock)
- IN_ORBIT → IN_TRANSIT (navigate)
- IN_TRANSIT → arrival → DOCKED (wait_for_arrival + auto-dock)
- Auto-orbit when extracting from DOCKED
- Auto-orbit when navigating from DOCKED
- Invalid transition handling (navigate from DOCKED fails gracefully)
- State query returns correct status

**Feature 2: `ship_navigation.feature` (9 scenarios)**
- Navigate with sufficient fuel
- Navigate with insufficient fuel (should fail)
- Navigate to same location (no-op)
- Navigate with flight mode auto-selection
- Navigate with explicit CRUISE mode
- Navigate with explicit DRIFT mode
- Wait for arrival when already in transit
- Arrival time calculation
- Navigation to nonexistent waypoint (error handling)

**Feature 3: `ship_cargo.feature` (8 scenarios)**
- Sell all cargo at market
- Sell specific cargo item
- Jettison cargo item
- Extract resources from asteroid
- Extract with cooldown active (should wait)
- Buy cargo with sufficient capacity
- Buy cargo with insufficient capacity (should fail)
- Cargo status query

**Deliverables:**
- `tests/bdd/features/core/ship_state_machine.feature`
- `tests/bdd/features/core/ship_navigation.feature`
- `tests/bdd/features/core/ship_cargo.feature`
- `tests/bdd/steps/core/test_ship_controller_steps.py`

**Estimated Effort:** 16 hours

##### 2. `assignment_manager.py` (131 statements, 15% → 75%)

**Current State:** Ship allocation untested, conflict detection not verified

**Scenarios to Add (13 scenarios):**
- Assign ship to operation
- Release ship from operation
- Check ship availability
- Prevent double-booking (assign already-assigned ship)
- Sync with running daemons (auto-release stopped daemons)
- Sync with crashed daemons (detect and release)
- Find available ships
- Find available ships by cargo capacity
- List all assignments
- Get assignment details
- Stale assignment detection
- Multi-player isolation (player 1 can't see player 2's ships)
- Assignment with metadata

**Deliverables:**
- `tests/bdd/features/core/assignment_manager.feature`
- `tests/bdd/steps/core/test_assignment_manager_steps.py`

**Estimated Effort:** 8 hours

##### 3. `smart_navigator.py` (366 statements, 36% → 75%)

**Current State:** Routing logic partially tested, refuel insertion untested

**Scenarios to Add (14 scenarios):**
- Validate route with sufficient fuel
- Validate route with insufficient fuel (fail)
- Execute route with auto-refuel stop insertion
- Flight mode selection: CRUISE when fuel >75%
- Flight mode selection: DRIFT when fuel <75%
- Flight mode selection: DRIFT for short trip when fuel <75%
- Checkpoint save after navigation step
- Checkpoint save after refuel step
- Resume from checkpoint
- Operation pause handling
- Operation cancel handling
- Graph building from system waypoints
- Route calculation with Dijkstra
- No path exists handling

**Deliverables:**
- `tests/bdd/features/navigation/smart_navigator.feature`
- `tests/bdd/steps/navigation/test_smart_navigator_steps.py`

**Estimated Effort:** 12 hours

#### Phase 2 Milestones

| Week | Day | Milestone | Deliverables |
|------|-----|-----------|--------------|
| 2 | 6-7 | ship_controller state machine | 8 scenarios, state transitions verified |
| 2 | 8-9 | ship_controller navigation | 9 scenarios, navigation operations tested |
| 2 | 10 | ship_controller cargo + assignment_manager start | Cargo scenarios + 6 assignment scenarios |
| 3 | 11-12 | assignment_manager complete + smart_navigator start | All assignment scenarios + 7 navigator scenarios |
| 3 | 13-14 | smart_navigator complete | All navigator scenarios passing |
| 3 | 15 | Phase 2 complete | Coverage ≥50%, branch coverage >70%, PR created |

#### Phase 2 Success Criteria

- [ ] Overall coverage ≥50%
- [ ] `ship_controller.py` ≥85% coverage (all state transitions tested)
- [ ] `assignment_manager.py` ≥75% coverage
- [ ] `smart_navigator.py` ≥75% coverage
- [ ] Branch coverage >70% for core components
- [ ] All 52+ new scenarios passing
- [ ] Core state machine bugs identified and fixed
- [ ] Zero regressions in existing tests
- [ ] PR reviewed and merged

**Total Estimated Effort:** 36 hours (over 10 days, ~3.6 hours/day average)

---

### Phase 3: Business Logic (Weeks 4-6)

**Timeline:** Days 16-36
**Coverage Goal:** 50% → 65% (+15%, ~3,031 statements)

#### Target Files

##### 1. `multileg_trader.py` (1,880 statements, 23% → 70%) 🔥 CRITICAL

**Priority:** HIGH - Largest file in codebase, critical trading logic

**Current State:**
- Complex route planning algorithms untested
- Circuit breaker logic partially tested
- Price validation untested
- Cargo overflow handling untested

**Note:** Consider refactoring into smaller modules:
- `trade_route_planner.py`
- `circuit_breaker.py`
- `cargo_manager.py`
- `price_validator.py`

**Scenarios to Add (40+ scenarios across 4 features):**

**Feature 1: `multileg_route_planning.feature` (12 scenarios)**
- Plan 1-leg route (buy + sell)
- Plan 2-leg route with intermediate stop
- Plan 3+ leg route optimization
- Route with cargo carryover
- Route with partial cargo cleanup
- Invalid route (no market sells the good)
- Route profit calculation
- Route feasibility with fuel constraints
- Route with orbital zero-distance optimization
- Multi-leg route with salvage
- Route execution order validation
- Route plan validation against market freshness

**Feature 2: `circuit_breaker_advanced.feature` (15 scenarios)**
- Buy price spike detection
- Actual buy cost spike (different from quoted)
- Sell price crash detection
- Profitability check failure (expected profit too low)
- Circuit breaker recovery (prices normalize)
- Continue route after partial failure
- Skip failed segment, continue independent segments
- Circuit breaker with partial cargo (sell what you have)
- Circuit breaker with alternative markets
- Circuit breaker wrong market bug (sell at wrong location)
- Price degradation model validation
- Stale sell price handling
- Buy-only segment detection
- Selective salvage (keep profitable, jettison unprofitable)
- Circuit breaker smart skip logic

**Feature 3: `cargo_management.feature` (8 scenarios)**
- Cargo overflow handling
- Cargo cleanup after failed trades
- Sell wrong cargo at best available market
- Jettison unprofitable cargo
- Cargo space optimization for multileg
- Residual cargo handling
- Cargo tracking across legs
- Cargo units validation

**Feature 4: `price_validation.feature` (5 scenarios)**
- Market data freshness check
- Price impact model validation
- Live market vs planned price divergence
- Market data staleness detection
- Wrong price field usage detection

**Deliverables:**
- `tests/bdd/features/trading/multileg_route_planning.feature`
- `tests/bdd/features/trading/circuit_breaker_advanced.feature`
- `tests/bdd/features/trading/cargo_management.feature`
- `tests/bdd/features/trading/price_validation.feature`
- `tests/bdd/steps/trading/test_multileg_trader_steps.py`

**Estimated Effort:** 40 hours

##### 2. `scout_coordinator.py` (610 statements, 9% → 65%)

**Current State:** Fleet coordination untested, critical for multi-ship operations

**Scenarios to Add (20+ scenarios across 3 features):**

**Feature 1: `scout_partitioning.feature` (8 scenarios)**
- Vertical partitioning for wide systems
- Horizontal partitioning for tall systems
- Balanced partition distribution
- Partition overlap prevention
- Exclude specific markets from partitions
- Partition with ship speed consideration
- Dynamic repartitioning when ships added/removed
- Partition visualization data export

**Feature 2: `scout_coordination.feature` (8 scenarios)**
- Assign ships to partitions
- Add ships gracefully (repartition)
- Remove ships gracefully (redistribute)
- Ship-to-market assignment
- Coordination state save to file
- Coordination state load from file
- Signal handling (SIGTERM)
- Signal handling (SIGINT)

**Feature 3: `scout_tour_optimization.feature` (6 scenarios)**
- 2-opt tour optimization
- Greedy tour construction
- OR-Tools TSP solving
- Tour optimization comparison (2opt vs greedy vs OR-Tools)
- Tour with fuel constraints
- Tour return to start vs one-way

**Deliverables:**
- `tests/bdd/features/scout/scout_partitioning.feature`
- `tests/bdd/features/scout/scout_coordination.feature`
- `tests/bdd/features/scout/scout_tour_optimization.feature`
- `tests/bdd/steps/scout/test_scout_coordinator_steps.py`

**Estimated Effort:** 24 hours

##### 3. `contracts.py` (541 statements, 7% → 65%)

**Current State:** Revenue-critical profitability calculations not validated

**Scenarios to Add (18+ scenarios across 2 features):**

**Feature 1: `contract_evaluation.feature` (10 scenarios)**
- Calculate contract profitability
- Calculate ROI
- Evaluate resource availability (can mine)
- Evaluate resource availability (must buy)
- Contract within fuel range check
- Contract delivery distance calculation
- Net profit calculation with mining costs
- Net profit calculation with purchase costs
- Contract terms validation
- Contract rejection criteria

**Feature 2: `contract_fulfillment.feature` (8 scenarios)**
- Fulfill via mining
- Fulfill via purchasing
- Partial fulfillment tracking
- Progress monitoring
- Cargo already on ship (partial)
- Cargo already on ship (full)
- Contract already partially fulfilled
- Contract already fully fulfilled

**Deliverables:**
- `tests/bdd/features/operations/contract_evaluation.feature`
- `tests/bdd/features/operations/contract_fulfillment.feature`
- `tests/bdd/steps/operations/test_contracts_steps.py`

**Estimated Effort:** 20 hours

#### Phase 3 Milestones

| Week | Days | Milestone | Deliverables |
|------|------|-----------|--------------|
| 4 | 16-20 | multileg_trader route planning | 12 scenarios, route planning tested |
| 5 | 21-25 | multileg_trader circuit breaker + cargo | 23 scenarios, circuit breaker validated |
| 5-6 | 26-30 | multileg_trader price + scout partitioning | 5+8 scenarios |
| 6 | 31-33 | scout coordination + contracts evaluation | 14 scenarios |
| 6 | 34-36 | contracts fulfillment + Phase 3 wrap | 8 scenarios, coverage ≥65%, PR created |

#### Phase 3 Success Criteria

- [ ] Overall coverage ≥65%
- [ ] `multileg_trader.py` ≥70% coverage
- [ ] `scout_coordinator.py` ≥65% coverage
- [ ] `contracts.py` ≥65% coverage
- [ ] Circuit breaker edge cases covered
- [ ] Tour optimization algorithms validated
- [ ] Contract profitability calculations verified
- [ ] All 78+ new scenarios passing
- [ ] Zero regressions
- [ ] PR reviewed and merged

**Total Estimated Effort:** 84 hours (over 21 days, ~4 hours/day average)

---

### Phase 4: Operations Handlers (Weeks 7-8)

**Timeline:** Days 37-50
**Coverage Goal:** 65% → 80% (+15%, ~989 statements)

#### Target Files

##### 1. `mining.py` (299 statements, 20% → 75%)

**Scenarios to Add (15 scenarios):**
- Full mining cycle (navigate → extract → navigate → sell)
- Targeted mining with cargo filtering
- Circuit breaker on consecutive wrong ore extractions
- Asteroid selection by deposit type
- Asteroid trait filtering (exclude stripped, radioactive)
- Mining profitability calculation
- Mining cycle time calculation
- Navigation to asteroid failure handling
- Extraction cooldown management
- Multiple extractions with cooldown tracking
- Yield tracking and logging
- Market price validation before selling
- Sell all cargo after mining
- Mining operation checkpoint resume
- Mining operation pause/cancel

**Deliverables:**
- `tests/bdd/features/mining/mining_workflows.feature`
- `tests/bdd/steps/mining/test_mining_steps.py`

**Estimated Effort:** 12 hours

##### 2. `routing.py` (operations) (344 statements, 3% → 70%)

**Scenarios to Add (12 scenarios):**
- Build system graph from waypoints
- Graph includes all waypoint types
- Graph marks fuel-available waypoints
- Graph includes orbital edges (zero distance)
- Plan route between two waypoints
- Plan multi-waypoint tour
- Tour optimization with 2-opt
- Tour return to start
- No path exists handling
- Route validation before execution
- Waypoint discovery from system API
- Graph serialization to JSON

**Deliverables:**
- `tests/bdd/features/routing/graph_building.feature`
- `tests/bdd/features/routing/route_planning.feature`
- `tests/bdd/steps/routing/test_routing_operation_steps.py`

**Estimated Effort:** 10 hours

##### 3. `analysis.py` (240 statements, 2% → 70%)

**Scenarios to Add (10 scenarios):**
- Analyze ship capabilities (cargo, fuel, range)
- Analyze ship modules and mounts
- Market analysis (price ranges, trade volumes)
- Profit calculation for trade route
- Distance calculation helpers
- Fuel cost estimation
- Mining profitability analysis
- Contract profitability analysis
- Waypoint trait analysis
- System survey analysis

**Deliverables:**
- `tests/bdd/features/operations/analysis.feature`
- `tests/bdd/steps/operations/test_analysis_steps.py`

**Estimated Effort:** 8 hours

##### 4. `fleet.py` (106 statements, 12% → 75%)

**Scenarios to Add (8 scenarios):**
- Display fleet status summary
- Ship status with location
- Ship status with fuel level
- Ship status with cargo inventory
- Ship status with navigation state
- Ship status with cooldown info
- Filter ships by criteria
- Sort ships by various attributes

**Deliverables:**
- `tests/bdd/features/operations/fleet_status.feature`
- `tests/bdd/steps/operations/test_fleet_steps.py`

**Estimated Effort:** 6 hours

#### Phase 4 Milestones

| Week | Days | Milestone | Deliverables |
|------|------|-----------|--------------|
| 7 | 37-40 | mining.py complete | 15 scenarios, mining workflows tested |
| 7-8 | 41-44 | routing.py complete | 12 scenarios, graph building validated |
| 8 | 45-47 | analysis.py complete | 10 scenarios, analysis functions tested |
| 8 | 48-50 | fleet.py complete + Phase 4 wrap | 8 scenarios, coverage ≥80%, PR created |

#### Phase 4 Success Criteria

- [ ] Overall coverage ≥80%
- [ ] `mining.py` ≥75% coverage
- [ ] `routing.py` (operations) ≥70% coverage
- [ ] `analysis.py` ≥70% coverage
- [ ] `fleet.py` ≥75% coverage
- [ ] All operation handlers have comprehensive error path testing
- [ ] All 45+ new scenarios passing
- [ ] Zero regressions
- [ ] PR reviewed and merged

**Total Estimated Effort:** 36 hours (over 14 days, ~2.6 hours/day average)

---

### Phase 5: Advanced Features (Ongoing)

**Timeline:** Days 51+
**Coverage Goal:** 80% → 85%+ (+5%+, continuous improvement)

#### Target Files

##### 1. `ortools_router.py` (766 statements, 38% → 75%)

**Note:** Requires property-based testing with Hypothesis

**Scenarios to Add (12+ scenarios):**
- VRP constraint building
- TSP constraint building
- Fuel dimension constraint
- Flight mode decision constraints
- Solution extraction and validation
- Feasibility check (valid fuel levels)
- Feasibility check (valid edges)
- Feasibility check (start/end locations)
- Min-cost flow formulation
- Disjunction penalty configuration
- Property-based: any valid input → feasible solution or failure
- Property-based: solution respects fuel constraints

**Deliverables:**
- `tests/bdd/features/routing/ortools_routing.feature`
- `tests/bdd/steps/routing/test_ortools_router_steps.py`
- Hypothesis property-based tests

**Estimated Effort:** 16 hours

##### 2. `market_data.py` (171 statements, 13% → 75%)

**Scenarios to Add (10 scenarios):**
- Price tracking and updates
- Market freshness validation
- Price history management
- Market data staleness detection
- Trade good mapping
- Import/export filtering
- Price range validation
- Market type identification
- Fuel availability detection
- Market traits parsing

**Deliverables:**
- `tests/bdd/features/core/market_data.feature`
- `tests/bdd/steps/core/test_market_data_steps.py`

**Estimated Effort:** 8 hours

##### 3. `mcp_bridge.py` (116 statements, 0% → 60%)

**Scenarios to Add (8 scenarios):**
- MCP tool invocation
- Tool parameter parsing
- Tool parameter validation
- Tool response formatting
- Error handling and responses
- Player ID context passing
- Multi-tool invocation
- Tool timeout handling

**Deliverables:**
- `tests/bdd/features/integrations/mcp_integration.feature`
- `tests/bdd/steps/integrations/test_mcp_bridge_steps.py`

**Estimated Effort:** 8 hours

#### Phase 5 Milestones

| Timeline | Milestone | Deliverables |
|----------|-----------|--------------|
| Week 9-10 | ortools_router complete | Property-based tests, 75%+ coverage |
| Week 11 | market_data complete | 10 scenarios, 75%+ coverage |
| Week 12 | mcp_bridge complete | 8 scenarios, 60%+ coverage |
| Ongoing | Maintain 85%+ | Coverage ratcheting enforced, new features include tests |

#### Phase 5 Success Criteria

- [ ] Overall coverage ≥85%
- [ ] `ortools_router.py` ≥75% coverage
- [ ] `market_data.py` ≥75% coverage
- [ ] `mcp_bridge.py` ≥60% coverage
- [ ] All core packages >75% coverage
- [ ] All operation handlers >70% coverage
- [ ] Coverage ratcheting enforced in CI
- [ ] Property-based testing in place for OR-Tools

**Total Estimated Effort:** 32+ hours (ongoing)

---

## Resource Requirements

### Team Allocation

#### Personnel

**Primary Developer (50% allocation for 8 weeks):**
- Role: Test development, coverage improvement
- Time: 4 hours/day × 5 days/week × 8 weeks = 160 hours
- Responsibilities:
  - Write BDD scenarios
  - Implement step definitions
  - Fix coverage gaps
  - Update documentation

**Code Reviewer (10% allocation for 8 weeks):**
- Role: Quality assurance, pattern enforcement
- Time: 0.8 hours/day × 5 days/week × 8 weeks = 32 hours
- Responsibilities:
  - Review PRs (5 PRs, ~6 hours each)
  - Ensure BDD patterns followed
  - Validate coverage metrics
  - Suggest improvements

**Tech Lead (5% allocation for 8 weeks):**
- Role: Strategic oversight, unblocking
- Time: 0.4 hours/day × 5 days/week × 8 weeks = 16 hours
- Responsibilities:
  - Weekly progress reviews
  - Architectural guidance for multileg_trader refactoring
  - Risk mitigation
  - Stakeholder communication

**Total Team Hours:** 208 hours over 8 weeks

#### Effort Breakdown by Phase

| Phase | Duration | Dev Hours | Review Hours | Lead Hours | Total |
|-------|----------|-----------|--------------|------------|-------|
| Phase 1 | 1 week | 24 | 4 | 1 | 29 |
| Phase 2 | 2 weeks | 56 | 8 | 2 | 66 |
| Phase 3 | 3 weeks | 96 | 12 | 4 | 112 |
| Phase 4 | 2 weeks | 64 | 8 | 2 | 74 |
| **Subtotal (Weeks 1-8)** | **8 weeks** | **240** | **32** | **9** | **281** |
| Phase 5 | Ongoing | 40+ | 8+ | 2+ | 50+ |

### Infrastructure & Tools

#### Required Infrastructure (One-Time Setup)

**CI/CD Integration (6 hours):**
- [ ] GitHub Actions workflow configuration (2 hours)
- [ ] Coverage threshold enforcement setup (1 hour)
- [ ] Codecov/Coveralls integration (1 hour)
- [ ] Coverage badge setup (1 hour)
- [ ] Branch protection rules (1 hour)

**Configuration Files (2 hours):**
- [ ] `.coveragerc` or `pyproject.toml` coverage section (1 hour)
- [ ] pytest.ini updates for markers and options (0.5 hours)
- [ ] Exclusion patterns and pragmas (0.5 hours)

**Total Infrastructure Setup:** 8 hours

#### Software & Services

**Already Installed ✅:**
- pytest 7.0+
- pytest-bdd 6.0+
- pytest-cov 4.0+
- python-dateutil
- psutil

**To Install:**
- [ ] Hypothesis (for Phase 5 property-based testing)
  ```bash
  pip install hypothesis>=6.0.0
  ```

**Services to Configure:**
- [ ] Codecov or Coveralls (free for open source)
- [ ] Coverage badge service (shields.io - free)

**Total Cost:** $0 (all free/open-source tools)

### Training & Knowledge Transfer

#### Required Training Sessions

**1. Branch Coverage & Coverage-Driven Development (2 hours)**
- Target audience: Primary developer
- Content:
  - Difference between line and branch coverage
  - How to read coverage reports (missing lines vs missing branches)
  - Coverage-driven workflow (red → green → refactor)
  - Using `--cov-branch` flag
  - Interpreting htmlcov reports
- Delivery: Self-paced with recorded video + Q&A session
- When: Before Phase 1 starts

**2. Property-Based Testing with Hypothesis (2 hours)**
- Target audience: Primary developer
- Content:
  - Hypothesis basics (@given, @example, strategies)
  - Stateful testing for state machines
  - Testing constraint solvers (OR-Tools)
  - Shrinking and failure reproduction
- Delivery: Workshop with hands-on exercises
- When: Before Phase 5 starts (Week 9)

**3. BDD Patterns Refresher (1 hour)**
- Target audience: Code reviewer, tech lead
- Content:
  - Review existing TESTING_GUIDE.md
  - Common anti-patterns to watch for
  - Quality vs quantity in test scenarios
- Delivery: Team meeting with examples
- When: Before Phase 1 starts

**Total Training Time:** 5 hours

#### Documentation Deliverables

**To Create:**
- [ ] `docs/COVERAGE_IMPROVEMENT_PROGRESS.md` (weekly updates)
- [ ] `docs/BDD_SCENARIO_EXAMPLES.md` (quick reference)
- [ ] `.github/workflows/test-coverage.yml` (CI/CD workflow)

**To Update:**
- [ ] `COVERAGE_REPORT.md` (after each phase)
- [ ] `TESTING_GUIDE.md` (new patterns discovered)
- [ ] `README.md` (coverage badge, testing section)

---

## Risk Management

### Risk Register

| ID | Risk | Likelihood | Impact | Severity | Mitigation | Contingency |
|----|------|------------|--------|----------|------------|-------------|
| R1 | Test suite execution time >5 min | Medium | High | 🔴 | pytest-xdist, markers, session fixtures | Invest in faster CI, test sharding |
| R2 | Timeline slip (85% not reached) | Medium | Medium | 🟡 | Phased approach, weekly reviews | Stop at 80%, defer Phase 5 |
| R3 | Flaky tests undermine confidence | Low | High | 🟡 | Deterministic mocks, 100x stability check | Quarantine flaky tests |
| R4 | multileg_trader.py too complex | Medium | High | 🔴 | Refactor into modules first | Reduce target to 60%, critical paths only |
| R5 | Team bandwidth insufficient | Medium | High | 🔴 | 50% allocation, phases parallelizable | Extend to 12 weeks, reduce scope |
| R6 | OR-Tools testing difficult | Medium | Low | 🟢 | Property-based testing, solution properties | Defer to future, achieve 85% without |
| R7 | Coverage misleading (high % but low quality) | Low | Medium | 🟡 | Branch coverage, code review focus on quality | Add mutation testing |
| R8 | Coverage regression after merge | Low | High | 🟡 | Coverage ratcheting in CI | Block PRs that reduce coverage |
| R9 | CI/CD setup delays Phase 1 | Low | Low | 🟢 | Setup on Day 0 before dev starts | Manual coverage checks temporarily |
| R10 | Team knowledge gaps (Hypothesis, branch coverage) | Low | Medium | 🟡 | Training sessions scheduled in advance | Pair programming, on-the-job learning |

### Risk Mitigation Strategies

#### High-Priority Mitigations

**R1: Test Suite Execution Time**
- Action: Configure pytest-xdist for parallel execution
  ```bash
  pytest tests/ -n auto  # Use all CPU cores
  ```
- Action: Implement test markers for selective runs
  ```bash
  pytest -m unit tests/       # Fast unit tests only
  pytest -m "not slow" tests/ # Exclude slow tests
  ```
- Action: Use session-scoped fixtures for expensive setup
- Monitor: Track execution time per phase, flag tests >1s

**R4: multileg_trader.py Complexity**
- Action: Evaluate refactoring feasibility in Week 4
- Action: Break testing into sub-phases if needed (route planning → circuit breaker → cargo → pricing)
- Fallback: Accept 60% coverage if 70% proves infeasible
- Document: Lessons learned for handling large files

**R5: Team Bandwidth**
- Action: Front-load quick wins (Phase 1) to demonstrate value
- Action: Allocate 50% max (20 hours/week) to avoid burnout
- Action: Make phases independent (can pause between phases)
- Escalation: Extend timeline to 12 weeks if velocity <50%

#### Monitoring & Early Warning

**Weekly Progress Reviews (30 minutes):**
- Coverage delta achieved vs planned
- Velocity (hours spent vs expected)
- Blockers and risks
- Adjust timeline/scope if needed

**Quality Metrics Dashboard:**
- Overall coverage trend
- Per-package coverage trend
- Test execution time trend
- Flaky test count

---

## Success Metrics

### Quantitative Metrics

#### Coverage Metrics (Primary KPIs)

| Metric | Baseline | Week 1 | Week 3 | Week 6 | Week 8 | Final | Target Met? |
|--------|----------|--------|--------|--------|--------|-------|-------------|
| **Overall Coverage** | 23.5% | 35% | 50% | 65% | 80% | 85%+ | ✅ |
| **Core Package** | 24.7% | 30% | 55% | 70% | 75% | 75%+ | ✅ |
| **Operations Package** | 18.2% | 30% | 45% | 60% | 70% | 70%+ | ✅ |
| **Branch Coverage** | N/A | N/A | 70% | 72% | 75% | 75%+ | ✅ |
| **Critical Files <30%** | 15 | 12 | 5 | 2 | 0 | 0 | ✅ |

#### Test Metrics (Secondary KPIs)

| Metric | Baseline | Target | Actual |
|--------|----------|--------|--------|
| Total Scenarios | 552 | 700+ | TBD |
| Feature Files | 48 | 60+ | TBD |
| Test Execution Time | ~5s | <5 min | TBD |
| Flaky Test Rate | Unknown | <0.1% | TBD |
| Test Lines of Code | ~15,000 | ~20,000 | TBD |

### Qualitative Metrics

#### Team Confidence (Survey at End of Each Phase)

**Questions (1-5 scale, 1=Strongly Disagree, 5=Strongly Agree):**
1. I feel confident making changes to the codebase
2. Tests catch bugs before they reach production
3. BDD scenarios are useful as documentation
4. I understand the coverage metrics and their meaning
5. Testing is integrated into my development workflow

**Target:** Average score ≥4.0 by end of Phase 4

#### Code Quality Indicators

**Metrics to Track:**
- Bug escape rate (production bugs that had no test coverage)
- Refactoring velocity (time to safely refactor a module)
- Code review feedback (number of test-related comments)
- New feature test coverage (% of new code with tests)

**Target:**
- Zero P0 bugs escaping to production (covered code)
- 50% reduction in refactoring time for tested modules
- <5 test-related review comments per PR
- 100% test coverage for all new features

### Acceptance Criteria

#### Phase Completion Criteria

**Phase 1 Complete:**
- ✅ Overall coverage ≥35%
- ✅ 3 target files meet coverage goals
- ✅ All new scenarios pass
- ✅ Zero regressions
- ✅ PR merged

**Phase 2 Complete:**
- ✅ Overall coverage ≥50%
- ✅ ship_controller.py ≥85% (CRITICAL)
- ✅ Branch coverage >70%
- ✅ All state transitions tested
- ✅ PR merged

**Phase 3 Complete:**
- ✅ Overall coverage ≥65%
- ✅ multileg_trader.py ≥70%
- ✅ Circuit breaker edge cases covered
- ✅ PR merged

**Phase 4 Complete:**
- ✅ Overall coverage ≥80%
- ✅ All operation handlers ≥70%
- ✅ Zero critical gaps (<30% coverage)
- ✅ PR merged

**Phase 5 Complete:**
- ✅ Overall coverage ≥85%
- ✅ Coverage ratcheting enforced
- ✅ Property-based testing in place
- ✅ PR merged

#### Initiative Success Criteria

**Must Have (Required for Success):**
- ✅ Overall coverage ≥85%
- ✅ Core package ≥75%
- ✅ Operations package ≥70%
- ✅ Zero files >100 statements with <30% coverage (except legacy)
- ✅ CI/CD enforcement in place

**Should Have (Highly Desirable):**
- ✅ Branch coverage ≥75%
- ✅ Test execution time <5 minutes
- ✅ Coverage badge in README
- ✅ Team confidence score ≥4.0

**Nice to Have (Stretch Goals):**
- Property-based testing for OR-Tools
- Mutation testing integration
- Per-file coverage thresholds
- Coverage trend dashboard

---

## Timeline & Milestones

### Gantt Chart

```
Week 1  [▓▓▓▓▓▓▓] Phase 1: Quick Wins (purchasing, logging, daemon)
        └─ Day 5: 35% coverage milestone

Week 2  [▓▓▓▓▓▓▓] Phase 2: Core Infrastructure (ship_controller)
Week 3  [▓▓▓▓▓▓▓] Phase 2: (assignment_manager, smart_navigator)
        └─ Day 15: 50% coverage milestone

Week 4  [▓▓▓▓▓▓▓] Phase 3: Business Logic (multileg_trader route planning)
Week 5  [▓▓▓▓▓▓▓] Phase 3: (multileg_trader circuit breaker + cargo)
Week 6  [▓▓▓▓▓▓▓] Phase 3: (scout_coordinator, contracts)
        └─ Day 36: 65% coverage milestone

Week 7  [▓▓▓▓▓▓▓] Phase 4: Operations (mining, routing)
Week 8  [▓▓▓▓▓▓▓] Phase 4: (analysis, fleet)
        └─ Day 50: 80% coverage milestone

Week 9+ [░░░░░░░] Phase 5: Advanced Features (ongoing)
        └─ 85%+ coverage achieved
```

### Critical Path

**Day 0 (Pre-Phase 1):**
- CI/CD setup (6 hours) - BLOCKER for all phases
- Training: Branch coverage (2 hours)
- Branch created, scenarios drafted

**Day 1-5 (Phase 1):**
- Critical: daemon_manager (process stability)
- Enables: Background operation testing in later phases

**Day 6-15 (Phase 2):**
- Critical: ship_controller (core state machine)
- Enables: All navigation, cargo, and operation testing
- Blocker: If ship_controller not done, Phase 3+ delayed

**Day 16-36 (Phase 3):**
- Critical: multileg_trader (largest file, trading revenue)
- Enables: Trading workflow validation
- Risk: If refactoring needed, timeline extends

**Day 37-50 (Phase 4):**
- Parallel work possible (mining, routing, analysis, fleet independent)
- No blockers between files

**Day 51+ (Phase 5):**
- Optional: Can achieve 85% without Phase 5 if needed
- Not blocking: Advanced features are stretch goals

### Milestone Checklist

**Week 1 Milestone: 35% Coverage**
- [ ] purchasing.py ≥95%
- [ ] captain_logging.py ≥60%
- [ ] daemon_manager.py ≥50%
- [ ] Overall coverage 35%
- [ ] CI/CD configured and green
- [ ] PR merged

**Week 3 Milestone: 50% Coverage**
- [ ] ship_controller.py ≥85%
- [ ] assignment_manager.py ≥75%
- [ ] smart_navigator.py ≥75%
- [ ] Branch coverage >70%
- [ ] Overall coverage 50%
- [ ] PR merged

**Week 6 Milestone: 65% Coverage**
- [ ] multileg_trader.py ≥70%
- [ ] scout_coordinator.py ≥65%
- [ ] contracts.py ≥65%
- [ ] Overall coverage 65%
- [ ] PR merged

**Week 8 Milestone: 80% Coverage**
- [ ] mining.py ≥75%
- [ ] routing.py (ops) ≥70%
- [ ] analysis.py ≥70%
- [ ] fleet.py ≥75%
- [ ] Overall coverage 80%
- [ ] Zero critical gaps
- [ ] PR merged

**Ongoing Milestone: 85%+ Coverage**
- [ ] ortools_router.py ≥75%
- [ ] market_data.py ≥75%
- [ ] mcp_bridge.py ≥60%
- [ ] Overall coverage ≥85%
- [ ] Coverage ratcheting enforced
- [ ] PR merged

---

## Deliverables

### Code Deliverables

#### Feature Files (Gherkin Scenarios)

**Phase 1:**
- `tests/bdd/features/operations/purchasing_edge_cases.feature`
- `tests/bdd/features/operations/captain_logging.feature`
- `tests/bdd/features/core/daemon_lifecycle.feature`

**Phase 2:**
- `tests/bdd/features/core/ship_state_machine.feature`
- `tests/bdd/features/core/ship_navigation.feature`
- `tests/bdd/features/core/ship_cargo.feature`
- `tests/bdd/features/core/assignment_manager.feature`
- `tests/bdd/features/navigation/smart_navigator.feature`

**Phase 3:**
- `tests/bdd/features/trading/multileg_route_planning.feature`
- `tests/bdd/features/trading/circuit_breaker_advanced.feature`
- `tests/bdd/features/trading/cargo_management.feature`
- `tests/bdd/features/trading/price_validation.feature`
- `tests/bdd/features/scout/scout_partitioning.feature`
- `tests/bdd/features/scout/scout_coordination.feature`
- `tests/bdd/features/scout/scout_tour_optimization.feature`
- `tests/bdd/features/operations/contract_evaluation.feature`
- `tests/bdd/features/operations/contract_fulfillment.feature`

**Phase 4:**
- `tests/bdd/features/mining/mining_workflows.feature`
- `tests/bdd/features/routing/graph_building.feature`
- `tests/bdd/features/routing/route_planning.feature`
- `tests/bdd/features/operations/analysis.feature`
- `tests/bdd/features/operations/fleet_status.feature`

**Phase 5:**
- `tests/bdd/features/routing/ortools_routing.feature`
- `tests/bdd/features/core/market_data.feature`
- `tests/bdd/features/integrations/mcp_integration.feature`

**Total:** 25+ new feature files, ~200+ new scenarios

#### Step Definitions (Python)

**Phase 1:**
- `tests/bdd/steps/operations/test_purchasing_edge_cases_steps.py`
- `tests/bdd/steps/operations/test_captain_logging_steps.py`
- `tests/bdd/steps/core/test_daemon_lifecycle_steps.py`

**Phase 2:**
- `tests/bdd/steps/core/test_ship_controller_steps.py`
- `tests/bdd/steps/core/test_assignment_manager_steps.py`
- `tests/bdd/steps/navigation/test_smart_navigator_steps.py`

**Phase 3:**
- `tests/bdd/steps/trading/test_multileg_trader_steps.py`
- `tests/bdd/steps/scout/test_scout_coordinator_steps.py`
- `tests/bdd/steps/operations/test_contracts_steps.py`

**Phase 4:**
- `tests/bdd/steps/mining/test_mining_steps.py`
- `tests/bdd/steps/routing/test_routing_operation_steps.py`
- `tests/bdd/steps/operations/test_analysis_steps.py`
- `tests/bdd/steps/operations/test_fleet_steps.py`

**Phase 5:**
- `tests/bdd/steps/routing/test_ortools_router_steps.py`
- `tests/bdd/steps/core/test_market_data_steps.py`
- `tests/bdd/steps/integrations/test_mcp_bridge_steps.py`

**Total:** 16+ new step definition files, ~5,000+ new lines of test code

### Configuration Deliverables

**CI/CD:**
- `.github/workflows/test-coverage.yml` (GitHub Actions workflow)
  - Run tests on all PRs
  - Enforce coverage threshold (85%)
  - Upload reports to Codecov
  - Comment coverage diff on PRs

**Coverage Configuration:**
- `.coveragerc` or `pyproject.toml` coverage section
  - Enable branch coverage
  - Configure source paths
  - Set exclusion patterns
  - Omit legacy files

**pytest Configuration:**
- `pytest.ini` updates
  - Add new markers (branch_coverage, property_based)
  - Configure coverage options
  - Set test discovery patterns

### Documentation Deliverables

**New Documents:**
- `docs/COVERAGE_IMPROVEMENT_PROGRESS.md` (weekly updates throughout initiative)
- `docs/BDD_SCENARIO_EXAMPLES.md` (quick reference for common patterns)
- `docs/PROPERTY_BASED_TESTING_GUIDE.md` (for Phase 5, Hypothesis usage)

**Updated Documents:**
- `COVERAGE_REPORT.md` (updated after each phase with new metrics)
- `TESTING_GUIDE.md` (new examples from each phase)
- `README.md` (coverage badge, testing section update)
- `CHANGELOG.md` (coverage improvement milestones)

**Reports:**
- Weekly progress reports (Weeks 1-8)
- Phase completion reports (Phases 1-4)
- Final initiative retrospective (after Phase 4)

---

## Quality Gates

### Phase Gates (Required to Proceed)

**Gate 1: Phase 1 → Phase 2**
- ✅ Overall coverage ≥35%
- ✅ All Phase 1 target files meet coverage goals
- ✅ All new tests passing
- ✅ Zero regressions in existing tests
- ✅ PR code reviewed and approved
- ✅ CI/CD green (coverage threshold passing)

**Gate 2: Phase 2 → Phase 3**
- ✅ Overall coverage ≥50%
- ✅ ship_controller.py ≥85% (CRITICAL)
- ✅ Branch coverage >70% for core components
- ✅ All state transitions tested and documented
- ✅ All new tests passing
- ✅ Zero regressions
- ✅ PR merged

**Gate 3: Phase 3 → Phase 4**
- ✅ Overall coverage ≥65%
- ✅ multileg_trader.py ≥70%
- ✅ scout_coordinator.py ≥65%
- ✅ contracts.py ≥65%
- ✅ Circuit breaker edge cases documented and tested
- ✅ All new tests passing
- ✅ Zero regressions
- ✅ PR merged

**Gate 4: Phase 4 → Phase 5**
- ✅ Overall coverage ≥80%
- ✅ All operation handlers ≥70%
- ✅ Zero critical gaps (<30% coverage)
- ✅ All new tests passing
- ✅ Zero regressions
- ✅ PR merged

**Gate 5: Phase 5 Complete**
- ✅ Overall coverage ≥85%
- ✅ Coverage ratcheting enforced in CI
- ✅ Property-based testing in place for OR-Tools
- ✅ All packages meet targets
- ✅ PR merged

### Continuous Quality Gates (All Phases)

**Per-Commit Gates:**
- ✅ All tests pass locally before commit
- ✅ Code formatted with black/autopep8 (if configured)
- ✅ No new linting errors introduced

**Per-PR Gates:**
- ✅ All tests pass in CI
- ✅ Coverage does not decrease (coverage ratcheting)
- ✅ Code review approval (at least 1 reviewer)
- ✅ Branch up to date with main
- ✅ Conflicts resolved

**Post-Merge Gates:**
- ✅ Main branch CI green
- ✅ Coverage report updated
- ✅ Documentation updated (if applicable)

---

## Tools & Infrastructure

### Testing Tools

**Core Framework:**
- **pytest** (7.0+) - Test runner and framework
  - Install: `pip install pytest>=7.0.0`
  - Docs: https://docs.pytest.org/

- **pytest-bdd** (6.0+) - BDD/Gherkin support
  - Install: `pip install pytest-bdd>=6.0.0`
  - Docs: https://pytest-bdd.readthedocs.io/

- **pytest-cov** (4.0+) - Coverage measurement
  - Install: `pip install pytest-cov>=4.0.0`
  - Docs: https://pytest-cov.readthedocs.io/

**Additional Plugins:**
- **pytest-xdist** (recommended) - Parallel test execution
  - Install: `pip install pytest-xdist`
  - Usage: `pytest -n auto` (uses all CPU cores)

- **pytest-timeout** (optional) - Test timeout enforcement
  - Install: `pip install pytest-timeout`
  - Usage: `pytest --timeout=60` (fail tests after 60s)

- **Hypothesis** (Phase 5) - Property-based testing
  - Install: `pip install hypothesis>=6.0.0`
  - Docs: https://hypothesis.readthedocs.io/

### Coverage Tools

**Measurement:**
- **coverage.py** (via pytest-cov) - Coverage data collection
  - Config: `.coveragerc` or `pyproject.toml`
  - Branch coverage enabled

**Reporting:**
- **HTML reports** - `htmlcov/index.html`
  - Generate: `pytest --cov=src --cov-report=html`
  - Open: `open htmlcov/index.html`

- **Terminal reports** - Inline summary
  - Generate: `pytest --cov=src --cov-report=term`

- **JSON reports** - `coverage.json`
  - Generate: `pytest --cov=src --cov-report=json`
  - For programmatic analysis

**Analysis:**
- **analyze_coverage.py** (custom) - Detailed analysis script
  - Location: `analyze_coverage.py`
  - Usage: `python3 analyze_coverage.py`
  - Output: Package breakdown, critical gaps, recommendations

### CI/CD Tools

**GitHub Actions:**
- **Workflow file:** `.github/workflows/test-coverage.yml`
- **Triggers:** Push to main, PRs to main
- **Jobs:**
  - Run tests with coverage
  - Enforce coverage threshold (`--cov-fail-under=85`)
  - Upload reports to Codecov
  - Comment coverage diff on PRs

**Coverage Reporting Service:**
- **Codecov** (recommended) - Free for open source
  - Setup: Add repo to Codecov, configure token
  - Features: Trend graphs, PR comments, badge generation
  - Docs: https://docs.codecov.com/

- **Alternative: Coveralls** - Also free for open source
  - Setup: Add repo to Coveralls
  - Docs: https://docs.coveralls.io/

**Coverage Badge:**
- **shields.io** - Generate badge from Codecov data
  - URL: `https://img.shields.io/codecov/c/github/{user}/{repo}`
  - Add to README.md

### Development Tools

**IDE Integration:**
- **pytest integration** - Most IDEs (PyCharm, VSCode) support pytest
  - Run tests from IDE
  - Debug tests with breakpoints
  - View coverage inline (green/red gutters)

**Coverage Visualization:**
- **Coverage Gutters** (VSCode extension)
  - Displays coverage inline in editor
  - Green = covered, red = not covered

- **PyCharm built-in** - Run with coverage
  - Right-click → Run with Coverage

**Command-Line Shortcuts:**
```bash
# Run all tests with coverage
pytest tests/ --cov=src --cov-report=html

# Run specific domain with coverage
pytest tests/bdd/features/trading/ --cov=src/spacetraders_bot/operations/multileg_trader --cov-report=term-missing

# Run with branch coverage
pytest tests/ --cov=src --cov-branch --cov-report=term

# Run in parallel (fast)
pytest tests/ -n auto

# Run only unit tests
pytest -m unit tests/

# Run and open HTML report
pytest tests/ --cov=src --cov-report=html && open htmlcov/index.html

# Check coverage threshold
pytest tests/ --cov=src --cov-fail-under=85
```

---

## Team Responsibilities

### Roles & Responsibilities

#### Primary Developer

**Responsibilities:**
- Write BDD scenarios (Gherkin feature files)
- Implement step definitions (Python)
- Fix coverage gaps in target files
- Run coverage analysis tools
- Update coverage reports
- Submit PRs for each phase
- Respond to code review feedback
- Update documentation (COVERAGE_REPORT.md, TESTING_GUIDE.md)

**Time Commitment:** 50% allocation (4 hours/day, 20 hours/week)

**Weekly Tasks:**
- Day 1-4: Write tests for target files
- Day 5: Run coverage, update reports, submit PR
- Ongoing: Respond to review feedback, merge PR

#### Code Reviewer

**Responsibilities:**
- Review PRs for test quality (not just quantity)
- Ensure BDD patterns followed (Given/When/Then, business-readable)
- Validate coverage metrics (htmlcov reports)
- Check for test anti-patterns (mocking business logic, flaky tests)
- Approve PRs when quality standards met
- Suggest improvements and alternative approaches

**Time Commitment:** 10% allocation (~1 hour/day, 5 hours/week)

**Weekly Tasks:**
- Day 5: Review PR from primary developer (~6 hours per phase PR)
- Provide feedback within 24 hours
- Re-review after changes

#### Tech Lead

**Responsibilities:**
- Strategic oversight of coverage initiative
- Weekly progress reviews (30 min meetings)
- Unblock architectural questions (e.g., multileg_trader refactoring)
- Risk mitigation and escalation
- Stakeholder communication
- Timeline adjustments if needed

**Time Commitment:** 5% allocation (~0.5 hours/day, 2.5 hours/week)

**Weekly Tasks:**
- Weekly progress review meeting (30 min)
- Review coverage reports and trends
- Provide architectural guidance as needed
- Escalate blockers to stakeholders

### Communication Plan

**Weekly Sync (30 minutes, every Friday):**
- Agenda:
  1. Coverage delta this week (vs plan)
  2. Blockers and risks
  3. Lessons learned
  4. Next week's plan
- Attendees: Primary developer, tech lead
- Output: Updated COVERAGE_IMPROVEMENT_PROGRESS.md

**Phase Completion Review (1 hour):**
- Agenda:
  1. Phase metrics review
  2. Retrospective (what went well, what didn't)
  3. Adjustments for next phase
- Attendees: Primary developer, code reviewer, tech lead
- Output: Phase completion report

**Ad-Hoc Communication:**
- Slack/Discord for quick questions
- GitHub PR comments for code-specific feedback
- Pair programming sessions for complex scenarios (optional)

---

## Progress Tracking

### Progress Dashboard

**Metrics to Track Weekly:**

| Metric | Week 1 | Week 2 | Week 3 | Week 4 | Week 5 | Week 6 | Week 7 | Week 8 |
|--------|--------|--------|--------|--------|--------|--------|--------|--------|
| Overall Coverage (%) | 35 | 40 | 50 | 55 | 60 | 65 | 72 | 80 |
| Core Coverage (%) | 30 | 42 | 55 | 62 | 68 | 70 | 73 | 75 |
| Operations Coverage (%) | 30 | 35 | 45 | 52 | 58 | 60 | 67 | 70 |
| Scenarios Added | 26 | 15 | 37 | 20 | 25 | 33 | 25 | 20 |
| Hours Spent | 24 | 28 | 28 | 32 | 32 | 32 | 18 | 18 |
| PRs Merged | 1 | 0 | 1 | 0 | 0 | 1 | 0 | 1 |
| Blockers | 0 | 0 | 0 | 1 | 0 | 0 | 0 | 0 |

**Visual Progress:**
```
Overall Coverage Progress
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Week 1  [███████░░░░░░░░░░░░░░░░░] 35%
Week 2  [█████████░░░░░░░░░░░░░░░] 40%
Week 3  [████████████░░░░░░░░░░░░] 50%
Week 4  [█████████████░░░░░░░░░░░] 55%
Week 5  [███████████████░░░░░░░░░] 60%
Week 6  [████████████████░░░░░░░░] 65%
Week 7  [██████████████████░░░░░░] 72%
Week 8  [████████████████████░░░░] 80%
Target  [█████████████████████░░░] 85%
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

### Weekly Progress Template

**File:** `docs/COVERAGE_IMPROVEMENT_PROGRESS.md`

```markdown
# Coverage Improvement Progress

## Week 1 (Days 1-5)

**Phase:** Phase 1 - Quick Wins
**Coverage Target:** 35%
**Actual Coverage:** 35.2% ✅

### Metrics
- **Overall:** 23.5% → 35.2% (+11.7%)
- **Core:** 24.7% → 30.1% (+5.4%)
- **Operations:** 18.2% → 29.8% (+11.6%)

### Completed
- ✅ purchasing.py: 79% → 96% (17 new scenarios)
- ✅ captain_logging.py: 14% → 62% (11 scenarios)
- ✅ daemon_manager.py: 14% → 51% (9 scenarios)

### Blockers
- None

### Lessons Learned
- File locking tests for captain_logging required mock threading
- Daemon PID detection works differently on macOS vs Linux (used psutil for cross-platform)
- MockAPIClient needed extend for log file operations

### Next Week
- Phase 2: ship_controller state machine (target: 50% overall coverage)
```

### Burndown Chart

**Coverage Gap Burndown:**
```
Coverage Gap (Percentage Points to 85%)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Start   │ ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 61.5%
Week 1  │ ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 50.0%
Week 2  │ ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 45.0%
Week 3  │ ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 35.0%
Week 4  │ ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 30.0%
Week 5  │ ▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 25.0%
Week 6  │ ▓▓▓▓▓▓▓▓▓▓ 20.0%
Week 7  │ ▓▓▓▓▓ 13.0%
Week 8  │ ▓ 5.0%
Target  │ 0.0% ✅
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

### Phase Completion Checklist

**Phase 1 Checklist:**
- [x] purchasing.py ≥95%
- [x] captain_logging.py ≥60%
- [x] daemon_manager.py ≥50%
- [x] Overall coverage ≥35%
- [x] CI/CD configured
- [x] PR created
- [x] Code review completed
- [x] PR merged
- [x] COVERAGE_REPORT.md updated
- [x] Weekly progress report written

**Phase 2 Checklist:**
- [ ] ship_controller.py ≥85%
- [ ] assignment_manager.py ≥75%
- [ ] smart_navigator.py ≥75%
- [ ] Branch coverage >70%
- [ ] Overall coverage ≥50%
- [ ] PR created
- [ ] Code review completed
- [ ] PR merged
- [ ] COVERAGE_REPORT.md updated
- [ ] Weekly progress reports (Week 2-3)

*(Repeat for Phases 3-5)*

---

## Appendices

### Appendix A: Testing Best Practices Reference

**BDD Scenario Patterns:**
```gherkin
# Good: Business-readable, clear intent
Scenario: Purchase with insufficient cargo capacity
  Given a ship "TRADER-1" with 38/40 cargo capacity
  And a market selling "IRON_ORE" at 100 credits/unit
  When I attempt to buy 5 units of "IRON_ORE"
  Then the purchase should fail
  And an error message should mention "insufficient cargo space"

# Bad: Technical jargon, unclear intent
Scenario: Test cargo overflow
  Given setup ship trader-1
  And mock API returns cargo_capacity=40, inventory_count=38
  When call purchase_cargo("IRON_ORE", 5)
  Then assert purchase_result == False
```

**Step Definition Patterns:**
```python
# Good: Reusable, parametric, clear
@given(parsers.parse('a ship "{ship}" with {current:d}/{capacity:d} cargo capacity'))
def ship_with_cargo(mock_api, ship, current, capacity):
    mock_api.set_ship_cargo(ship, [], capacity=capacity)
    # Add mock cargo items to reach 'current' units
    for i in range(current):
        mock_api.ships[ship]["cargo"]["inventory"].append({
            "symbol": "FILLER",
            "units": 1
        })

# Bad: Hardcoded, not reusable
@given('setup ship trader-1')
def setup_ship(mock_api):
    mock_api.ships["TRADER-1"] = {
        "cargo": {
            "capacity": 40,
            "inventory": [{"symbol": "IRON", "units": 38}]
        }
    }
```

**Coverage-Driven Development Workflow:**
1. **Red:** Run coverage, identify untested lines
   ```bash
   pytest tests/ --cov=src/module --cov-report=html
   open htmlcov/src_module_file_py.html  # See red lines
   ```

2. **Green:** Write scenario to cover red lines
   ```gherkin
   Scenario: [Scenario covering the red line]
   ```

3. **Refactor:** Improve test quality, not just quantity
   - Make scenarios business-readable
   - Extract reusable steps
   - Parametrize with Examples tables

### Appendix B: Useful Commands

**Coverage Commands:**
```bash
# Quick coverage check
pytest tests/ --cov=src --cov-report=term --cov-report=term-missing

# Branch coverage
pytest tests/ --cov=src --cov-branch --cov-report=html

# Coverage for specific file
pytest tests/ --cov=src/spacetraders_bot/core/ship_controller --cov-report=term-missing

# Fail if coverage below threshold
pytest tests/ --cov=src --cov-fail-under=85

# Generate all report formats
pytest tests/ --cov=src --cov-report=term --cov-report=html --cov-report=json
```

**Test Selection Commands:**
```bash
# Run specific feature file
pytest tests/bdd/features/trading/circuit_breaker.feature

# Run specific scenario
pytest tests/ -k "Purchase with insufficient cargo"

# Run by marker
pytest -m unit tests/
pytest -m domain tests/
pytest -m "not slow" tests/

# Run in parallel (fast)
pytest tests/ -n auto

# Run with verbose output
pytest tests/ -v

# Run with very verbose output (show each step)
pytest tests/ -vv
```

**Development Workflow:**
```bash
# 1. Create branch
git checkout -b feat/coverage-phase-1-quick-wins

# 2. Write tests, run locally
pytest tests/bdd/features/operations/purchasing_edge_cases.feature -v

# 3. Check coverage
pytest tests/ --cov=src/spacetraders_bot/operations/purchasing --cov-report=term-missing

# 4. Update report
python3 analyze_coverage.py > coverage_summary.txt

# 5. Commit
git add tests/ COVERAGE_REPORT.md
git commit -m "Phase 1: purchasing.py edge cases (79% → 96%)"

# 6. Push and create PR
git push origin feat/coverage-phase-1-quick-wins
gh pr create --title "Phase 1: Increase coverage from 23.5% to 35%" --fill
```

### Appendix C: Coverage Configuration Examples

**.coveragerc (Option 1):**
```ini
[run]
source = src/spacetraders_bot
branch = True
omit =
    */tests/*
    */conftest.py
    */__pycache__/*
    */routing_legacy.py
    */mcp_bridge.py

[report]
precision = 1
show_missing = True
skip_covered = False
exclude_lines =
    pragma: no cover
    def __repr__
    raise AssertionError
    raise NotImplementedError
    if __name__ == .__main__.:
    if TYPE_CHECKING:
    @abstractmethod

[html]
directory = htmlcov

[json]
output = coverage.json
```

**pyproject.toml (Option 2):**
```toml
[tool.coverage.run]
source = ["src/spacetraders_bot"]
branch = true
omit = [
    "*/tests/*",
    "*/conftest.py",
    "*/__pycache__/*",
    "*/routing_legacy.py",
]

[tool.coverage.report]
precision = 1
show_missing = true
skip_covered = false
exclude_lines = [
    "pragma: no cover",
    "def __repr__",
    "raise AssertionError",
    "raise NotImplementedError",
    "if __name__ == .__main__.:",
    "if TYPE_CHECKING:",
    "@abstractmethod",
]

[tool.coverage.html]
directory = "htmlcov"

[tool.coverage.json]
output = "coverage.json"
```

**pytest.ini:**
```ini
[pytest]
testpaths = tests
python_files = test_*.py
python_classes = Test*
python_functions = test_*

markers =
    unit: Unit-level BDD tests
    domain: Domain-level BDD tests
    integration: Integration-level BDD tests
    regression: Regression tests
    slow: Slow-running tests (>1 second)

addopts =
    -v
    --strict-markers
    --tb=short
    --cov=src/spacetraders_bot
    --cov-report=term-missing
    --cov-branch
```

### Appendix D: GitHub Actions Workflow Example

**.github/workflows/test-coverage.yml:**
```yaml
name: Test Coverage

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  test-coverage:
    runs-on: ubuntu-latest

    steps:
      - uses: actions/checkout@v3

      - name: Set up Python
        uses: actions/setup-python@v4
        with:
          python-version: '3.9'

      - name: Install dependencies
        run: |
          python -m pip install --upgrade pip
          pip install -r tests/requirements.txt

      - name: Run tests with coverage
        run: |
          pytest tests/ \
            --cov=src/spacetraders_bot \
            --cov-report=term \
            --cov-report=xml \
            --cov-report=html \
            --cov-branch \
            --cov-fail-under=85

      - name: Upload coverage to Codecov
        uses: codecov/codecov-action@v3
        with:
          file: ./coverage.xml
          flags: unittests
          name: codecov-umbrella
          fail_ci_if_error: true

      - name: Archive coverage reports
        uses: actions/upload-artifact@v3
        with:
          name: coverage-reports
          path: htmlcov/

      - name: Comment coverage on PR
        if: github.event_name == 'pull_request'
        uses: py-cov-action/python-coverage-comment-action@v3
        with:
          GITHUB_TOKEN: ${{ github.token }}
          MINIMUM_GREEN: 80
          MINIMUM_ORANGE: 70
```

### Appendix E: Resources & References

**Official Documentation:**
- pytest: https://docs.pytest.org/
- pytest-bdd: https://pytest-bdd.readthedocs.io/
- coverage.py: https://coverage.readthedocs.io/
- pytest-cov: https://pytest-cov.readthedocs.io/
- Hypothesis: https://hypothesis.readthedocs.io/

**Best Practices Guides:**
- Complete Guide to pytest-bdd: https://pytest-with-eric.com/bdd/pytest-bdd/
- Modern TDD in Python: https://testdriven.io/blog/modern-tdd/
- Automation Panda - Python Testing 101: https://automationpanda.com/2018/10/22/python-testing-101-pytest-bdd/
- Google Testing Blog: https://testing.googleblog.com/

**Example Projects:**
- behavior-driven-python: https://github.com/AutomationPanda/behavior-driven-python
- tau-pytest-bdd: https://github.com/AutomationPanda/tau-pytest-bdd
- pytest-bdd examples: https://github.com/pytest-dev/pytest-bdd/tree/master/examples

**Internal Documentation:**
- TESTING_GUIDE.md (531 lines, comprehensive BDD guide)
- COVERAGE_REPORT.md (current analysis)
- docs/TEST_COVERAGE_BEST_PRACTICES.md (industry standards)
- docs/PYTEST_COVERAGE_RESEARCH.md (framework documentation)

---

## Document Control

**Version History:**

| Version | Date | Author | Changes |
|---------|------|--------|---------|
| 1.0 | 2025-10-19 | Engineering Team | Initial plan creation |

**Approval:**

| Role | Name | Signature | Date |
|------|------|-----------|------|
| Tech Lead | ___________ | ___________ | __________ |
| Engineering Manager | ___________ | ___________ | __________ |

**Next Review Date:** End of Phase 2 (Week 3)

---

**End of Document**

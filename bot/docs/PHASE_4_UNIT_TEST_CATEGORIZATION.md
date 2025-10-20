# Phase 4: Unit Test Categorization & Migration Plan

**Date:** 2025-10-19
**Status:** Analysis Complete

## Executive Summary

24 unit tests analyzed and categorized into:
- **Behavioral tests (2)**: Migrate to BDD for business-readable scenarios
- **Pure unit tests (22)**: Keep as pytest with direct discovery

## Categorization Matrix

| File | Category | Rationale | Action |
|------|----------|-----------|--------|
| **CLI (1)** |
| `test_main_cli.py` | Behavioral | Tests CLI routing & argument parsing | ✅ MIGRATE to BDD |
| **Core (7)** |
| `test_api_client.py` | Pure Unit | Tests API mechanics, rate limiting | ⚡ KEEP as pytest |
| `test_daemon_manager.py` | Pure Unit | Tests daemon lifecycle internals | ⚡ KEEP as pytest |
| `test_ortools_routing.py` | Pure Unit | Tests OR-Tools algorithm integration | ⚡ KEEP as pytest |
| `test_route_optimizer.py` | Pure Unit | Tests route optimization logic | ⚡ KEEP as pytest |
| `test_scout_coordinator_core.py` | Pure Unit | Tests coordination state machine | ⚡ KEEP as pytest |
| `test_smart_navigator_unit.py` | Pure Unit | Tests navigation algorithms | ⚡ KEEP as pytest |
| `test_tour_optimizer.py` | Pure Unit | Tests tour optimization logic | ⚡ KEEP as pytest |
| **Helpers (1)** |
| `test_paths.py` | Pure Unit | Tests utility function | ⚡ KEEP as pytest |
| **Operations (14)** |
| `test_analysis_operation.py` | Behavioral | Tests analysis operation flow | ✅ MIGRATE to BDD |
| `test_assignment_operations.py` | Pure Unit | Tests assignment logic | ⚡ KEEP as pytest |
| `test_assignments_operation.py` | Pure Unit | Tests assignment internals | ⚡ KEEP as pytest |
| `test_captain_logging.py` | Pure Unit | Tests logging mechanics | ⚡ KEEP as pytest |
| `test_common.py` | Pure Unit | Tests common utilities | ⚡ KEEP as pytest |
| `test_contract_resource_strategy.py` | Pure Unit | Tests strategy calculation | ⚡ KEEP as pytest |
| `test_control_primitives.py` | Pure Unit | Tests control flow primitives | ⚡ KEEP as pytest |
| `test_daemon_operation.py` | Pure Unit | Tests daemon operation internals | ⚡ KEEP as pytest |
| `test_fleet_operation.py` | Pure Unit | Tests fleet status logic | ⚡ KEEP as pytest |
| `test_mining_cycle.py` | Pure Unit | Tests mining cycle mechanics | ⚡ KEEP as pytest |
| `test_mining_operation.py` | Pure Unit | Tests mining orchestration | ⚡ KEEP as pytest |
| `test_navigation_operation.py` | Pure Unit | Tests navigation internals | ⚡ KEEP as pytest |
| `test_routing_operation.py` | Pure Unit | Tests routing internals | ⚡ KEEP as pytest |
| `test_scout_coordination_operation.py` | Pure Unit | Tests scout coordination | ⚡ KEEP as pytest |
| **Other (1)** |
| `test_sell_all_type_consistency.py` | Pure Unit | Tests type consistency | ⚡ KEEP as pytest |

## Decision Rationale

### ✅ Migrate to BDD (2 tests)

**1. test_main_cli.py** - CLI command routing
- **Why:** User-facing behavior - how commands are parsed and dispatched
- **Benefit:** Business-readable scenarios for CLI interface
- **Example scenario:** "When user runs 'graph-build' command, then graph_build_operation is called"

**2. test_analysis_operation.py** - Analysis operation workflow
- **Why:** High-level operation with clear business intent
- **Benefit:** Documents analysis capabilities for stakeholders
- **Example scenario:** "When analyzing ship capabilities, then detailed report is generated"

### ⚡ Keep as pytest (22 tests)

**Core library tests (7):**
- Test algorithms, state machines, and low-level mechanics
- No business behavior - pure logic verification
- Examples: rate limiting, pathfinding algorithms, OR-Tools integration

**Operations internals (12):**
- Test orchestration mechanics and error handling
- Focus on implementation details, not business behavior
- Most operations already have BDD tests in domain folders

**Utilities (3):**
- Pure functions with no business context
- Simple input/output verification
- Examples: path utilities, type consistency

## Migration Strategy

### Step 1: Migrate Behavioral Tests (Day 1)

#### 1.1 Create feature files

```bash
mkdir -p tests/bdd/features/unit
touch tests/bdd/features/unit/cli.feature
touch tests/bdd/features/unit/operations.feature
```

#### 1.2 Write Gherkin scenarios

**File:** `tests/bdd/features/unit/cli.feature`

```gherkin
Feature: CLI Command Routing

  Scenario: Route graph-build command to operation handler
    Given the CLI receives "graph-build" with system "X1-TEST"
    When the command is dispatched
    Then graph_build_operation should be called with system "X1-TEST"

  Scenario: Route route-plan command to operation handler
    Given the CLI receives "route-plan" with ship "SHIP-1"
    And start waypoint "X1-A" and goal waypoint "X1-B"
    When the command is dispatched
    Then route_plan_operation should be called with goal "X1-B"

  Scenario: Handle missing subcommand gracefully
    Given the CLI receives "assignments" without action
    When the command is dispatched
    Then usage help should be displayed
    And exit code should be 1
```

**File:** `tests/bdd/features/unit/operations.feature`

```gherkin
Feature: Analysis Operations

  Scenario: Generate ship capability analysis
    Given a ship with mining capabilities
    When analysis operation is executed
    Then detailed ship report is generated
    And report includes cargo capacity and fuel range
```

#### 1.3 Create step definitions

```bash
touch tests/bdd/steps/unit/test_cli_steps.py
touch tests/bdd/steps/unit/test_operations_steps.py
```

### Step 2: Reorganize Pure Unit Tests (Day 2)

#### 2.1 Update directory structure

**Current:**
```
tests/unit/
├── cli/
├── core/
├── helpers/
└── operations/
```

**Target:** Keep as-is, but ensure pytest discovery works

#### 2.2 Update pytest.ini

Remove `norecursedirs` exclusion for unit tests:

```ini
[pytest]
# Enable direct pytest discovery for unit tests
testpaths = tests
python_files = test_*.py
python_classes = Test*
python_functions = test_* regression_*

# Exclude only legacy bridge mechanism
norecursedirs = .git __pycache__ .pytest_cache
```

### Step 3: Verification (Day 3)

#### 3.1 Run all tests

```bash
# Run BDD tests
pytest tests/bdd/features/unit/ -v

# Run pure unit tests
pytest tests/unit/ -v --ignore=tests/unit/cli/test_main_cli.py

# Run everything
pytest tests/ -v
```

#### 3.2 Verify coverage

```bash
pytest tests/unit/ --cov=src --cov-report=term
```

#### 3.3 Delete migrated files

```bash
rm tests/unit/cli/test_main_cli.py
# Keep all other unit tests
```

## Implementation Checklist

### Day 1: Migrate Behavioral Tests
- [ ] Create `tests/bdd/features/unit/` directory
- [ ] Write `cli.feature` with 3 scenarios
- [ ] Write `operations.feature` with 1 scenario
- [ ] Create `tests/bdd/steps/unit/` directory
- [ ] Implement `test_cli_steps.py`
- [ ] Implement `test_operations_steps.py`
- [ ] Run BDD tests and verify passing

### Day 2: Reorganize Pure Unit Tests
- [ ] Update `pytest.ini` to enable unit test discovery
- [ ] Run pure unit tests and verify all pass
- [ ] Document unit test organization in `TESTING_GUIDE.md`

### Day 3: Verification & Cleanup
- [ ] Run full test suite (`pytest tests/ -v`)
- [ ] Verify 100% pass rate
- [ ] Check coverage (`pytest --cov=src`)
- [ ] Delete migrated unit test files
- [ ] Update `BDD_MIGRATION_PLAN.md` with Phase 4 completion

## Success Criteria

- [ ] ✅ 2 behavioral tests migrated to BDD
- [ ] ✅ 22 pure unit tests remain as pytest with direct discovery
- [ ] ✅ All 24 tests passing
- [ ] ✅ Coverage maintained (≥85%)
- [ ] ✅ pytest.ini updated
- [ ] ✅ Documentation updated

## Notes

**Why so few BDD migrations?**
- Most unit tests focus on implementation details, not business behavior
- Domain tests (already migrated) cover most behavioral scenarios
- Pure unit tests don't benefit from Gherkin's natural language
- Following "only migrate what benefits from BDD" principle

**Alternative considered:**
- Migrate all operations tests → Rejected: too implementation-focused
- Keep everything as unit tests → Rejected: CLI routing benefits from BDD
- Migrate more tests → Rejected: violates Phase 4 decision matrix

## Next Steps

After Phase 4 completion:
- **Phase 5:** Remove bridge mechanism, cleanup legacy infrastructure
- **Phase 6:** Optimize performance, finalize documentation

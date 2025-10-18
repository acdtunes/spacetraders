# Routing Domain Analysis - BDD Migration

**Date:** 2025-10-17
**Phase:** Phase 2 - Routing Domain Migration
**Analyst:** Claude Code (Sonnet 4.5)

## Summary

The routing domain contains **71 total tests** across 18 files:
- **56 tests** in traditional pytest format (17 files) - **NEED MIGRATION**
- **15 tests** already in BDD format (1 file) - **ALREADY COMPLETE**

## Files Breakdown

### Already Migrated (BDD Format)

1. `test_ortools_mining_steps.py` (1,009 lines)
   - Feature file: `tests/features/ortools_mining_optimization.feature`
   - **15 scenarios** covering mining fleet optimization
   - Status: ✅ **COMPLETE**

### Need Migration (Traditional Pytest)

| File | Test Count | Size | Focus Area |
|------|------------|------|------------|
| `test_ortools_real_coordinates.py` | 6 | 14K | OR-Tools TSP performance on real coordinates |
| `test_ortools_fallback_dijkstra.py` | 5 | 11K | Dijkstra fallback when OR-Tools fails |
| `test_ortools_orbital_jitter.py` | 5 | 13K | Orbital waypoint jitter handling |
| `test_ortools_crossing_edges_bug.py` | 4 | 11K | TSP crossing edges detection |
| `test_ortools_disjunction_penalty_too_low.py` | 4 | 8.7K | Disjunction penalty tuning |
| `test_routing_critical_bugs_fix.py` | 4 | 13K | Critical routing bugs (A* iteration limit, fuel errors, market selection) |
| `test_fixed_route_coordinate_bug.py` | 3 | 4.3K | Fixed route coordinate handling |
| `test_mincostflow_branching_bug.py` | 3 | 12K | MinCostFlow branching issues |
| `test_ortools_duplicate_waypoint_bug.py` | 3 | 6.3K | Fleet partitioner duplicate waypoints |
| `test_ortools_orbital_branching_bug.py` | 3 | 12K | Orbital waypoint branching |
| `test_ortools_partitioner_deduplication_unit.py` | 3 | 7.5K | Partitioner deduplication logic |
| `test_ortools_router_hang_bug.py` | 3 | 7.7K | Router hang scenarios |
| `test_ortools_router_initialization_performance.py` | 3 | 7.0K | Router initialization speed |
| `test_ortools_router_mincostflow_hang.py` | 3 | 7.3K | MinCostFlow solver hangs |
| `test_ortools_market_drop_bug_real_data.py` | 2 | 9.2K | Market dropping from VRP partitions |
| `test_ortools_min_cost_flow_cycle_bug.py` | 2 | 7.8K | MinCostFlow cycle detection |
| `test_ortools_router_fast_fuel_aware_routing.py` | 2 | 7.2K | Fast fuel-aware routing |

**Total to migrate: 56 tests across 17 files**

## Test Categories

Based on file content analysis, routing tests fall into these categories:

### 1. OR-Tools TSP/VRP Optimization (16 tests)
- Crossing edges detection
- Real coordinate handling
- Solver timeout/metaheuristic tuning
- Jitter/noise handling in coordinates

**Feature file:** `tests/features/routing/ortools_optimization.feature`

### 2. Fuel-Aware Routing (11 tests)
- A* iteration limits
- DRIFT vs CRUISE mode selection
- Refuel stop insertion
- Fuel calculation accuracy

**Feature file:** `tests/features/routing/fuel_aware_routing.feature`

### 3. Fleet Partitioner (9 tests)
- Duplicate waypoint prevention
- Market distribution across ships
- Disjoint partition guarantees

**Feature file:** `tests/features/routing/fleet_partitioning.feature`

### 4. Router Robustness (12 tests)
- Fallback to Dijkstra when OR-Tools fails
- Hang prevention
- MinCostFlow solver issues
- Initialization performance

**Feature file:** `tests/features/routing/router_robustness.feature`

### 5. Critical Bug Fixes (8 tests)
- Orbital waypoint handling
- Fixed route coordinates
- Market drop bugs

**Feature file:** `tests/features/routing/regression_bugs.feature`

## Migration Strategy

### Step 1: Create Feature Files
Create 5 feature files under `tests/features/routing/`:
1. `ortools_optimization.feature` - TSP/VRP scenarios
2. `fuel_aware_routing.feature` - Fuel management scenarios
3. `fleet_partitioning.feature` - Multi-ship distribution
4. `router_robustness.feature` - Error handling & fallbacks
5. `regression_bugs.feature` - Critical bug regression tests

### Step 2: Create Common Step Definitions
Create `tests/bdd/steps/routing_steps.py` with reusable steps:
- Graph setup (waypoints, edges, coordinates)
- Ship configuration (fuel, speed, location)
- Route execution and validation
- Performance assertions
- Crossing edge detection utilities

### Step 3: Migrate Tests by Category
Process each category sequentially:
1. OR-Tools optimization (most complex, ~16 scenarios)
2. Fuel-aware routing (~11 scenarios)
3. Fleet partitioning (~9 scenarios)
4. Router robustness (~12 scenarios)
5. Regression bugs (~8 scenarios)

### Step 4: Validation
- Run pytest on each feature file after migration
- Verify 100% pass rate
- Ensure performance remains under 1s per test
- Delete legacy test files only after validation

## Key Challenges

1. **Complex Graph Setup**: Many tests use real-world coordinate data
2. **OR-Tools Mocking**: Need to mock OR-Tools solver behavior for deterministic tests
3. **Performance Assertions**: Tests validate solver timeout behavior
4. **Crossing Edge Detection**: Geometric algorithms need careful migration

## Success Criteria

- ✅ All 56 pytest tests converted to Gherkin scenarios
- ✅ 100% test pass rate (71/71 scenarios passing)
- ✅ Performance <1s per scenario
- ✅ Feature files readable by non-technical stakeholders
- ✅ Step definitions reusable across multiple scenarios
- ✅ Legacy test files deleted after validation

## Estimated Effort

- Feature file creation: 2-3 hours (5 files)
- Step definitions: 4-5 hours (complex graph/OR-Tools logic)
- Test migration: 6-8 hours (56 tests)
- Validation & cleanup: 1-2 hours
- **Total: 13-18 hours**

## Next Steps

1. **Create `tests/features/routing/` directory**
2. **Start with `ortools_optimization.feature`** (16 scenarios from 6 test files)
3. **Create `tests/bdd/steps/routing_steps.py`** with base fixtures
4. **Migrate first category and validate**
5. **Iterate through remaining categories**

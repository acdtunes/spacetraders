# Requirements Specification: OR-Tools Route Optimization

**Project:** SpaceTraders Bot Routing System Replacement
**Date:** 2025-10-10
**Status:** Ready for Implementation

---

## Executive Summary

Replace the existing custom routing system (routing.py, 1,214 lines) with Google OR-Tools, an industrial-strength optimization library. This addresses recurring bugs in flight mode selection and provides a more maintainable, proven solution for ship navigation with fuel constraints.

---

## Problem Statement

### Current Issues
1. **Recurring DRIFT bug:** Ships choose DRIFT mode for 700+ unit journeys (1+ hour travel) when CRUISE with intermediate refuels would take 10-15 minutes
2. **Complex bug surface:** 1,981 lines of custom A* pathfinding with fuel constraints
3. **Maintenance burden:** Same bugs resurface after fixes (e.g., emergency drift logic at routing.py:776-823)
4. **Test fragility:** Extensive test suite (10+ test files) but bugs still slip through

### Proposed Solution
Replace custom routing with Google OR-Tools VRP (Vehicle Routing Problem) solver:
- Proven industrial library (used by Google Maps, Uber, major logistics)
- Native support for fuel constraints via resource dimensions
- Handles both single-destination routing AND multi-stop TSP
- Reduces maintenance burden (Google maintains the solver)

---

## Functional Requirements

### FR-1: Single-Destination Route Optimization
**Description:** Find optimal route from ship's current location to single destination with fuel constraints.

**Input:**
- Ship data (location, fuel, fuel_capacity, engine_speed)
- Destination waypoint symbol
- System graph (waypoints, distances, fuel stations)
- Preference flag (prefer_cruise: bool)

**Output:**
- Route plan with step-by-step navigation instructions
- Flight mode per edge (CRUISE or DRIFT)
- Refuel stops inserted as needed
- Total time, fuel cost, final fuel level

**Constraints:**
- Fuel never exceeds capacity
- Fuel never drops below 1 (DRIFT minimum)
- Refuel only at waypoints with has_fuel=True
- Minimize total travel time

**Acceptance Criteria:**
- ✅ Finds routes for 50-1000 unit distances
- ✅ Inserts refuel stops when direct travel impossible
- ✅ Prefers CRUISE over DRIFT when fuel allows
- ✅ Returns same dict structure as current RouteOptimizer
- ✅ Passes all existing route planning tests

---

### FR-2: Multi-Stop Tour Optimization (TSP)
**Description:** Find optimal visiting order for multiple waypoints (Traveling Salesman Problem with fuel constraints).

**Input:**
- List of waypoints to visit
- Starting waypoint
- Ship data (fuel, capacity, speed)
- Algorithm preference (return_to_start: bool)

**Output:**
- Optimized tour order (waypoint sequence)
- Total tour distance and time
- Fuel requirements

**Constraints:**
- Visit each waypoint exactly once
- Minimize total tour time
- Optional: return to starting waypoint

**Acceptance Criteria:**
- ✅ Optimizes tours for 5-50 waypoints
- ✅ Results cached in tour_cache table
- ✅ Matches or beats current 2-opt performance
- ✅ Passes all existing tour optimization tests

---

### FR-3: Multi-Vehicle Tour Partitioning
**Description:** Optimally assign markets to multiple scout ships and determine visiting order for each ship (Multi-Vehicle VRP).

**Input:**
- List of markets to scout
- List of available ships with their specs (speed, starting locations)
- System graph

**Output:**
- Partition assignment: {ship_id: [markets_to_visit]}
- Optimized tour order for each ship
- Balanced workload (minimize max tour time across all ships)

**Current System (to replace):**
- `market_partitioning.py` with greedy, k-means, geographic strategies
- Heuristic-based, may not find optimal partition
- Greedy: assigns markets one-by-one to ship with shortest current tour
- K-means: spatial clustering
- Geographic: slices system into regions

**OR-Tools Approach:**
- Multi-Vehicle VRP solver
- Optimize globally across entire fleet
- Minimize maximum tour time (balanced workload)
- Better solution quality than heuristics

**Example:**
```
Input: 26 markets, 3 ships (speed 9 each)
Output:
  Ship-1: [A1, B6, B7, C40, D41, E43, F47, G49, H50] (tour time: 12 min)
  Ship-2: [I54, I55, J56, J57, K88, A2, A3, A4, C39] (tour time: 12 min)
  Ship-3: [D42, E44, E45, F48, H51, H52, H53, EF5B] (tour time: 12 min)

All ships finish within 12 minutes (balanced)
```

**Acceptance Criteria:**
- ✅ Assigns all markets to ships
- ✅ Balanced workload (max deviation <10% between ships)
- ✅ Matches or beats current partitioning quality
- ✅ Works with 1-20 ships
- ✅ Integrates with scout_coordinator
- ✅ Passes partitioning tests

---

### FR-4: Probe Ship Fast Path
**Description:** Probes (fuel_capacity=0) use simplified routing without fuel constraints.

**Input:**
- Probe ship data (must have fuel_capacity=0)
- Destination

**Output:**
- Direct route using shortest path
- Time calculated using ship's actual engine speed

**Detection Logic:**
```python
if ship_data['fuel']['capacity'] == 0:
    use_simple_probe_routing()
else:
    use_ortools_with_fuel_constraints()
```

**Acceptance Criteria:**
- ✅ Detects probes correctly via fuel_capacity=0
- ✅ Uses ship's actual engine speed for time calculation
- ✅ Faster execution than full OR-Tools (no optimization overhead)
- ✅ Passes probe navigation tests

---

### FR-5: Configuration System
**Description:** Flight mode constants stored in version-controlled YAML config file.

**Config File:** `config/routing_constants.yaml`

**Structure:**
```yaml
flight_modes:
  CRUISE:
    time_multiplier: 31      # Empirically validated
    fuel_rate: 1.0           # fuel per unit distance
  DRIFT:
    time_multiplier: 26      # Empirically validated
    fuel_rate: 0.003         # fuel per unit distance
  BURN:
    time_multiplier: 15
    fuel_rate: 2.0
  STEALTH:
    time_multiplier: 50
    fuel_rate: 1.0

fuel_safety_margin: 0.1      # 10% buffer
refuel_time_seconds: 5

validation:
  max_deviation_percent: 5.0
  check_interval_hours: 24
  pause_on_failure: true
```

**Acceptance Criteria:**
- ✅ Config loaded at startup
- ✅ Config reload without restart (for constant updates)
- ✅ Validation errors if config malformed
- ✅ Version controlled in git

---

### FR-6: Validation System
**Description:** Periodic validation of routing predictions against actual API behavior.

**Validation Metrics:**
- **Time Deviation:** `|predicted_time - actual_time| / actual_time × 100%`
- **Fuel Deviation:** `|predicted_fuel - actual_fuel| / actual_fuel × 100%`

**Validation Process:**
1. Execute test navigation (short route, ~100 units)
2. Record predicted time and fuel from OR-Tools
3. Execute via API, record actual time and fuel consumed
4. Calculate deviation for both metrics
5. If EITHER metric >5% deviation:
   - Log critical error with details
   - Pause all routing operations
   - Alert operator to update constants

**Trigger Modes:**
- **Periodic:** Every 24 hours (background task)
- **Manual:** CLI command `validate-routing --player-id X`

**Acceptance Criteria:**
- ✅ Validates both time and fuel accuracy
- ✅ Pauses operations on >5% deviation
- ✅ Logs validation results
- ✅ Manual validation command available
- ✅ Clear error messages with recommended constant adjustments

---

## Technical Requirements

### TR-1: OR-Tools Integration

**File:** `src/spacetraders_bot/core/ortools_router.py`

**Classes to Create:**
1. **ORToolsRouter** (replaces RouteOptimizer)
   - `find_optimal_route(start, goal, current_fuel, prefer_cruise=True)`
   - Returns route dict compatible with existing format
   - Models edges with dual modes (CRUISE and DRIFT variants)
   - Uses OR-Tools RoutingModel with fuel dimension

2. **ORToolsTSP** (replaces TourOptimizer)
   - `optimize_tour(waypoints, start, ship_data, return_to_start=False)`
   - Returns optimized waypoint order
   - Uses OR-Tools TSP solver
   - Caches results in tour_cache table

3. **ORToolsFleetPartitioner** (replaces MarketPartitioner strategies)
   - `partition_and_optimize(markets, ships, ship_data_dict)`
   - Returns partition assignment + optimized tours per ship
   - Uses OR-Tools Multi-Vehicle VRP
   - Balances workload across fleet (minimize max tour time)

**OR-Tools Modeling:**

```python
from ortools.constraint_solver import pywrapcp, routing_enums_pb2

class ORToolsRouter:
    def __init__(self, graph, ship_data, config):
        self.graph = graph
        self.ship_data = ship_data
        self.config = config

    def find_optimal_route(self, start, goal, current_fuel, prefer_cruise=True):
        # 1. Build location list (all waypoints in system)
        locations = list(self.graph['waypoints'].keys())
        start_idx = locations.index(start)
        goal_idx = locations.index(goal)

        # 2. Create distance and time matrices (with mode variants)
        # For each edge, create CRUISE and DRIFT variants
        distance_matrix = self._build_distance_matrix(locations)
        time_matrix = self._build_time_matrix(locations)
        fuel_matrix = self._build_fuel_matrix(locations)

        # 3. Create routing index manager
        manager = pywrapcp.RoutingIndexManager(
            len(locations),  # num locations
            1,               # num vehicles (single ship)
            start_idx,       # depot (start)
            goal_idx         # end
        )
        routing = pywrapcp.RoutingModel(manager)

        # 4. Register time callback (objective: minimize time)
        def time_callback(from_index, to_index):
            from_node = manager.IndexToNode(from_index)
            to_node = manager.IndexToNode(to_index)
            return time_matrix[from_node][to_node]

        time_callback_index = routing.RegisterTransitCallback(time_callback)
        routing.SetArcCostEvaluatorOfAllVehicles(time_callback_index)

        # 5. Add fuel dimension (constraint)
        def fuel_callback(from_index, to_index):
            from_node = manager.IndexToNode(from_index)
            to_node = manager.IndexToNode(to_index)
            return fuel_matrix[from_node][to_node]

        fuel_callback_index = routing.RegisterTransitCallback(fuel_callback)
        routing.AddDimension(
            fuel_callback_index,
            0,  # no slack
            self.ship_data['fuel']['capacity'],  # max capacity
            True,  # start cumul to zero
            'Fuel'
        )

        # 6. Allow refueling at fuel stations
        fuel_dimension = routing.GetDimensionOrDie('Fuel')
        for i, location in enumerate(locations):
            wp_data = self.graph['waypoints'][location]
            if wp_data.get('has_fuel', False):
                # Allow refueling (slack = can add fuel)
                node_idx = manager.NodeToIndex(i)
                fuel_dimension.SlackVar(node_idx).SetRange(0, self.ship_data['fuel']['capacity'])

        # 7. Set search parameters
        search_parameters = pywrapcp.DefaultRoutingSearchParameters()
        search_parameters.first_solution_strategy = (
            routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC
        )

        # 8. Solve
        solution = routing.SolveWithParameters(search_parameters)

        # 9. Convert solution to route dict (existing format)
        return self._convert_solution_to_route_dict(solution, routing, manager, locations)
```

**Multi-Vehicle VRP Example (Fleet Partitioning):**

```python
class ORToolsFleetPartitioner:
    def partition_and_optimize(self, markets, ships, ship_data_dict):
        # 1. Create location list (all markets + ship starting positions)
        locations = list(markets)
        num_vehicles = len(ships)

        # Each ship starts at different depot
        starts = [locations.index(ship_data_dict[ship]['nav']['waypointSymbol'])
                  for ship in ships]
        ends = starts  # Return to start positions

        # 2. Create routing manager (multi-vehicle)
        manager = pywrapcp.RoutingIndexManager(
            len(locations),
            num_vehicles,
            starts,  # multiple depots
            ends
        )
        routing = pywrapcp.RoutingModel(manager)

        # 3. Time callback (minimize max tour time across fleet)
        def time_callback(from_index, to_index):
            from_node = manager.IndexToNode(from_index)
            to_node = manager.IndexToNode(to_index)
            return time_matrix[from_node][to_node]

        time_callback_index = routing.RegisterTransitCallback(time_callback)
        routing.SetArcCostEvaluatorOfAllVehicles(time_callback_index)

        # 4. Add time dimension with span cost (balance workload)
        routing.AddDimension(
            time_callback_index,
            0,  # no slack
            99999,  # max time (large number)
            True,  # start cumul to zero
            'Time'
        )
        time_dimension = routing.GetDimensionOrDie('Time')
        time_dimension.SetGlobalSpanCostCoefficient(100)  # Penalize imbalance

        # 5. Solve and extract partitions + tours
        solution = routing.SolveWithParameters(search_parameters)
        return self._extract_partitions_from_solution(solution, routing, manager, ships, locations)
```

**Key Design Decisions:**
- Each physical edge represented as TWO nodes in OR-Tools graph:
  - Node A-CRUISE-B: time=fast, fuel=high
  - Node A-DRIFT-B: time=slow, fuel=low
- OR-Tools chooses which variant to use based on fuel constraints and time objective
- Multi-vehicle VRP uses `SetGlobalSpanCostCoefficient()` to balance workload
- Each ship gets its own depot (starting location)

---

### TR-2: Configuration Module

**File:** `src/spacetraders_bot/core/routing_config.py`

**Responsibilities:**
- Load `config/routing_constants.yaml` at startup
- Provide constants to routing modules
- Validate config schema
- Support hot-reload for constant updates

**Interface:**
```python
class RoutingConfig:
    def __init__(self, config_path='config/routing_constants.yaml'):
        self.config = self._load_config(config_path)
        self.validate()

    def get_flight_mode_config(self, mode: str) -> dict:
        """Returns {'time_multiplier': X, 'fuel_rate': Y}"""

    def get_validation_config(self) -> dict:
        """Returns validation settings"""

    def reload(self):
        """Hot-reload config from file"""
```

**Acceptance Criteria:**
- ✅ Loads YAML config at startup
- ✅ Validates schema (required keys present, values valid)
- ✅ Provides clean getter methods
- ✅ Supports reload without restart
- ✅ Raises clear errors on malformed config

---

### TR-3: Validation Module

**File:** `src/spacetraders_bot/core/routing_validator.py`

**Responsibilities:**
- Execute test navigations
- Compare predictions vs actual API behavior
- Calculate deviation metrics
- Pause operations on validation failure

**Validation Process:**
```python
class RoutingValidator:
    def validate(self, api_client, test_ship, test_route):
        # 1. Plan route with OR-Tools
        predicted = ortools_router.find_optimal_route(...)

        # 2. Execute actual navigation via API
        start_time = time.time()
        start_fuel = ship_data['fuel']['current']

        api_client.navigate(test_ship, test_route)
        # Wait for arrival...

        end_time = time.time()
        end_fuel = ship_data['fuel']['current']

        # 3. Calculate deviations
        actual_time = end_time - start_time
        actual_fuel = start_fuel - end_fuel

        time_deviation = abs(predicted_time - actual_time) / actual_time * 100
        fuel_deviation = abs(predicted_fuel - actual_fuel) / actual_fuel * 100

        # 4. Check thresholds
        if time_deviation > 5.0 or fuel_deviation > 5.0:
            self._pause_routing_operations()
            self._log_validation_failure(...)
            return False

        return True
```

**Test Routes for Validation:**
- Short CRUISE route (~100 units)
- Medium DRIFT route (~500 units)
- Route requiring one refuel stop

**Acceptance Criteria:**
- ✅ Executes test navigations safely
- ✅ Calculates time and fuel deviations
- ✅ Pauses operations on >5% deviation
- ✅ Logs validation results with timestamps
- ✅ Provides clear remediation guidance

---

### TR-4: SmartNavigator Integration

**File:** `src/spacetraders_bot/core/smart_navigator.py` (modify, not replace)

**Changes Required:**
- Replace `RouteOptimizer` instantiation with `ORToolsRouter`
- All other logic remains unchanged (state machine, execution, validation)
- Interface preserved: no changes to calling code

**Modified Lines:**
```python
# OLD (line ~90)
optimizer = RouteOptimizer(self.graph, ship_data)

# NEW
from .ortools_router import ORToolsRouter
from .routing_config import RoutingConfig

config = RoutingConfig()
optimizer = ORToolsRouter(self.graph, ship_data, config)
```

**Acceptance Criteria:**
- ✅ SmartNavigator works without interface changes
- ✅ execute_route() continues to work
- ✅ All SmartNavigator tests pass

---

### TR-5: Market Partitioning Integration

**File:** `src/spacetraders_bot/core/market_partitioning.py` (modify or replace)

**Changes Required:**
- Add new strategy: `'ortools'`
- Integrate ORToolsFleetPartitioner
- Preserve existing strategies as fallback options

**Modified Section:**
```python
# In MarketPartitioner.partition()
strategies = {
    "greedy": GreedyPartitionStrategy(),
    "kmeans": KMeansPartitionStrategy(self._rng),
    "geographic": GeographicPartitionStrategy(),
    "ortools": ORToolsPartitionStrategy()  # NEW
}
```

**Acceptance Criteria:**
- ✅ OR-Tools partitioning available via strategy='ortools'
- ✅ Balanced scout workload (minimize max tour time)
- ✅ Maintains PartitionResult interface
- ✅ Falls back to greedy if OR-Tools fails

---

### TR-6: Scout Coordinator Integration

**File:** `src/spacetraders_bot/core/scout_coordinator.py` (modify)

**Changes Required:**
- Replace `TourOptimizer` with `ORToolsTSP`
- Preserve tour_cache integration
- Keep partitioning and balancing logic

**Modified Section:**
```python
# OLD
from .routing import TourOptimizer
tour = TourOptimizer(graph).optimize_tour(markets, start, ship_data)

# NEW
from .ortools_router import ORToolsTSP
tour = ORToolsTSP(graph, config).optimize_tour(markets, start, ship_data)
```

**Acceptance Criteria:**
- ✅ Scout coordinator continues working
- ✅ Tour caching still functions
- ✅ Tour optimization quality maintained or improved
- ✅ All scout coordinator tests pass

---

### TR-7: Validation CLI Command

**File:** `src/spacetraders_bot/operations/validate_routing.py` (create)

**CLI Interface:**
```bash
python3 spacetraders_bot.py validate-routing \
  --player-id 7 \
  --ship STORMBANE-1 \
  --test-distance 100 \
  --mode CRUISE
```

**Output:**
```
======================================================================
ROUTING VALIDATION
======================================================================
Test Route: X1-JB26-A1 → X1-JB26-B7 (100 units, CRUISE)

Predicted:
  Time: 86 seconds
  Fuel: 100 units

Actual:
  Time: 87 seconds
  Fuel: 100 units

Deviation:
  Time: 1.2% ✅
  Fuel: 0.0% ✅

Result: ✅ VALIDATION PASSED (both metrics within 5% threshold)
======================================================================
```

**Acceptance Criteria:**
- ✅ CLI command executes test navigation
- ✅ Reports deviations clearly
- ✅ Pauses operations if validation fails
- ✅ Logs results to validation log file

---

## Implementation Plan

### Phase 1: Foundation (Day 1)
1. Install OR-Tools: `pip install ortools>=9.8.0 pyyaml>=6.0`
2. Create `config/routing_constants.yaml` with empirical constants
3. Create `routing_config.py` module
4. Write unit tests for config loading

**Deliverable:** Working config system

---

### Phase 2: OR-Tools Router (Day 1-2)
5. Create `ortools_router.py` with ORToolsRouter class
6. Implement `find_optimal_route()` using OR-Tools VRP
7. Handle probe ship fast path
8. Convert OR-Tools solution to existing route dict format
9. Write unit tests for ORToolsRouter

**Deliverable:** Working single-destination routing

---

### Phase 3: SmartNavigator Integration (Day 2)
10. Update SmartNavigator to use ORToolsRouter
11. Run navigation test suite
12. Fix any test failures (document if test was wrong)
13. Regression test with real navigation

**Deliverable:** SmartNavigator using OR-Tools, all tests passing

---

### Phase 4: TSP & Fleet Partitioning (Day 3)
14. Implement ORToolsTSP class in ortools_router.py
15. Implement ORToolsFleetPartitioner class
16. Integrate tour caching for both
17. Update scout_coordinator to use ORToolsTSP
18. Update market_partitioning.py to add 'ortools' strategy
19. Run tour optimization and partitioning tests
20. Validate scout coordinator still works with multi-ship operations

**Deliverable:** Complete OR-Tools routing system (single + multi-stop + multi-vehicle)

---

### Phase 5: Validation System (Day 3-4)
21. Create `routing_validator.py` module
22. Implement validation logic (time + fuel checks)
23. Create `validate_routing.py` operation
24. Add CLI command to spacetraders_bot.py
25. Test validation with intentionally wrong constants
26. Verify pause mechanism works

**Deliverable:** Working validation system

---

### Phase 6: Cleanup & Documentation (Day 4)
27. Remove old routing.py (keep copy as routing_legacy.py)
28. Update CLAUDE.md with OR-Tools information
29. Document new constants in GAME_GUIDE.md
30. Run full test suite
31. Create migration guide

**Deliverable:** Clean codebase, updated docs

---

## Dependencies

### New Dependencies
```txt
# Add to requirements.txt
ortools>=9.8.0   # Google optimization library (VRP/TSP solver)
pyyaml>=6.0      # YAML config file parsing
```

### Python Version
- Requires: Python 3.8+ (OR-Tools requirement)
- Current: Python 3.12 (compatible ✅)

---

## Test Strategy

### Existing Tests (Must Pass)
1. **BDD Navigation Tests:**
   - `test_routing_steps.py` - basic routing scenarios
   - `test_routing_advanced_steps.py` - complex multi-leg routes
   - `test_low_fuel_long_distance_steps.py` - refuel stop logic
   - `test_hop_minimization_steps.py` - prefer fewer hops
   - `test_smart_navigator_advanced_steps.py` - state machine integration

2. **Bug Regression Tests:**
   - `test_routing_critical_bugs_fix.py` - 700+ unit DRIFT bug
   - `test_intermediate_refuel_bug.py` - refuel insertion
   - `test_safety_margin_cruise_selection_bug.py` - CRUISE vs DRIFT selection

3. **Tour Optimization Tests:**
   - `test_tour_optimizer.py` - TSP correctness
   - `test_tour_time_imbalance_bug.py` - balanced tours
   - `test_balance_tour_times_bug.py` - partition balancing

### New Tests to Add
4. **OR-Tools Specific:**
   - Test probe fast path (fuel_capacity=0)
   - Test dual-mode edge selection
   - Test config loading and validation
   - Test validation system pause mechanism

### Test Execution
```bash
# Run all routing tests
pytest tests/ -k "routing or navigation or tour" -v

# Run with coverage
pytest tests/ --cov=src/spacetraders_bot/core/ortools_router --cov-report=html
```

**Coverage Target:** 85%+ for new OR-Tools modules

---

## Acceptance Criteria

### System-Level
- ✅ All existing routing tests pass (or documented as incorrect tests)
- ✅ No DRIFT mode chosen for 700+ unit routes when refuel stops available
- ✅ Route calculation time <5 seconds for typical routes
- ✅ TSP optimization matches or beats current 2-opt quality
- ✅ Zero regressions in ship navigation operations

### Integration-Level
- ✅ Mining operations continue working
- ✅ Contract fulfillment continues working
- ✅ Scout coordinator continues working
- ✅ Trading operations continue working
- ✅ All daemons continue functioning

### Operational-Level
- ✅ Config hot-reload works without restart
- ✅ Validation command executes successfully
- ✅ Validation failures pause operations as expected
- ✅ No ships stranded due to fuel miscalculations

---

## Risk Analysis

### High Risk
1. **OR-Tools learning curve:** Team unfamiliar with OR-Tools API
   - **Mitigation:** Start with simple examples, reference official tutorials

2. **Test suite reveals OR-Tools bugs:** Solver might have edge cases
   - **Mitigation:** Keep legacy routing.py as routing_legacy.py for emergency rollback

### Medium Risk
3. **Performance regression:** OR-Tools might be slower than custom A*
   - **Mitigation:** Benchmark both systems, optimize OR-Tools parameters

4. **Dual-mode modeling complexity:** 2× graph size might cause issues
   - **Mitigation:** Test with large systems (100+ waypoints)

### Low Risk
5. **Config file corruption:** YAML parsing errors
   - **Mitigation:** Schema validation, fallback to hardcoded defaults

6. **Validation false positives:** Network jitter causes >5% deviation
   - **Mitigation:** Run multiple validation samples, average results

---

## Open Questions / Assumptions

### Assumptions
1. OR-Tools VRP solver can handle fuel dimension with refuel stops (validated via Stack Overflow research)
2. Dual-mode edge modeling (CRUISE/DRIFT variants) is supported by OR-Tools
3. Existing tests encode correct expected behavior (some may need fixing)
4. Empirical constants (CRUISE=31, DRIFT=26) are accurate within 5%

### To Validate During Implementation
- OR-Tools performance for 100+ waypoint systems
- Memory usage for large graphs with dual-mode edges
- Solution quality vs current 2-opt TSP

---

## Success Metrics

### Primary
- **Zero DRIFT bugs:** No more 700+ unit DRIFT routes when refuels available
- **Test pass rate:** 100% of existing tests pass (or documented as incorrect)
- **No ship strandings:** Zero fuel miscalculation incidents

### Secondary
- **Code reduction:** 1,981 lines → ~500 lines (custom logic → OR-Tools calls)
- **Maintenance time:** Reduced bug fix cycles
- **Optimization quality:** TSP tours equal or better than current 2-opt

### Performance
- **Route calculation:** <5 seconds for typical routes
- **TSP optimization:** <10 seconds for 26-waypoint tours
- **Config reload:** <1 second

---

## References

- **OR-Tools Docs:** https://developers.google.com/optimization/routing/vrp
- **Resource Dimensions:** https://developers.google.com/optimization/routing/cvrptw_resources
- **Refueling in VRP:** https://or.stackexchange.com/questions/7598/modeling-refueling-in-vehicle-routing-problem-in-or-tools
- **Current Routing Code:** src/spacetraders_bot/core/routing.py:307-826
- **Bug History:** tests/test_routing_critical_bugs_fix.py

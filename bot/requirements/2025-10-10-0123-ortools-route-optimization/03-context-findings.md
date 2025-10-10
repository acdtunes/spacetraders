# Context Findings

## Codebase Analysis Complete

### Files to Replace/Modify

**Core Routing Logic (1,981 lines total to replace):**
1. `src/spacetraders_bot/core/routing.py` (1,214 lines)
   - `TimeCalculator` class - travel time formulas
   - `FuelCalculator` class - fuel consumption calculations
   - `RouteOptimizer` class - A* pathfinding with fuel constraints
   - `TourOptimizer` class - TSP solver (2-opt, nearest neighbor)

2. `src/spacetraders_bot/core/smart_navigator.py` (767 lines)
   - `SmartNavigator` class - orchestrates routing with ship controller
   - Route validation, execution, state machine
   - Proactive refueling logic

**Related Components (keep, update integration):**
- `src/spacetraders_bot/core/ship_controller.py` - ship state machine (dock/orbit/navigate)
- `src/spacetraders_bot/core/system_graph_provider.py` - graph building from API
- `src/spacetraders_bot/core/scout_coordinator.py` - uses TourOptimizer

### Existing Test Suite (Validation)

**BDD Tests (pytest-bdd):**
- `tests/bdd/steps/navigation/test_routing_steps.py`
- `tests/bdd/steps/navigation/test_routing_advanced_steps.py`
- `tests/bdd/steps/navigation/test_low_fuel_long_distance_steps.py`
- `tests/bdd/steps/navigation/test_hop_minimization_steps.py`
- `tests/bdd/steps/navigation/test_smart_navigator_advanced_steps.py`

**Unit Tests:**
- `tests/unit/core/test_tour_optimizer.py`
- `tests/unit/operations/test_navigation_operation.py`
- `tests/unit/operations/test_routing_operation.py`

**Bug Regression Tests:**
- `tests/test_routing_critical_bugs_fix.py` - 700+ unit navigation bug
- `tests/test_intermediate_refuel_bug.py` - refuel stop logic
- `tests/test_safety_margin_cruise_selection_bug.py`
- `tests/test_tour_time_imbalance_bug.py`
- `tests/test_balance_tour_times_bug.py`

### Patterns to Follow

**Ship Data Structure (from API):**
```python
{
    'nav': {
        'waypointSymbol': str,
        'status': 'DOCKED'|'IN_ORBIT'|'IN_TRANSIT',
        'flightMode': 'CRUISE'|'DRIFT'|'BURN'
    },
    'fuel': {
        'current': int,
        'capacity': int
    },
    'engine': {
        'speed': int  # e.g., 36 for frigates
    }
}
```

**Graph Structure (from system_graph_provider):**
```python
{
    'waypoints': {
        'X1-JB26-A1': {
            'x': float,
            'y': float,
            'type': str,
            'traits': [...],
            'has_fuel': bool
        }
    },
    'adjacency': {
        'X1-JB26-A1': [(neighbor, distance), ...]
    }
}
```

**Current Constants (routing.py lines 35-47):**
```python
FLIGHT_MODE_MULTIPLIERS = {
    'CRUISE': 31,
    'DRIFT': 26,
    'BURN': 15,
    'STEALTH': 50
}
FUEL_CONSUMPTION = {
    'CRUISE': 1.0,
    'DRIFT': 0.003,
    'BURN': 2.0
}
FUEL_SAFETY_MARGIN = 0.1
REFUEL_TIME = 5  # seconds
```

### OR-Tools Research

**Best Practices from Documentation:**

1. **VRP with Resource Dimensions** (fuel as dimension)
   - Use `AddDimension()` for fuel tracking
   - Set `slack_max=0` (no fuel slack)
   - Set `capacity=fuel_capacity` (tank limit)
   - Use callbacks for fuel consumption

2. **Refueling Stations** (from OR Stack Exchange)
   - Model refuel stops as special nodes
   - Use dimension slack to allow refill
   - Constrain refuel only at has_fuel=True waypoints

3. **Implementation Pattern:**
```python
# Create fuel callback
def fuel_callback(from_index, to_index):
    from_node = manager.IndexToNode(from_index)
    to_node = manager.IndexToNode(to_index)
    distance = distance_matrix[from_node][to_node]
    return int(distance * fuel_rate)  # CRUISE: 1.0, DRIFT: 0.003

fuel_callback_index = routing.RegisterTransitCallback(fuel_callback)

# Add fuel dimension
routing.AddDimension(
    fuel_callback_index,
    0,  # no slack (strict fuel limit)
    fuel_capacity,  # max fuel
    True,  # start cumul to zero
    'Fuel'
)

# Allow refueling at stations
fuel_dimension = routing.GetDimensionOrDie('Fuel')
for i, wp in enumerate(waypoints):
    if wp['has_fuel']:
        fuel_dimension.SlackVar(i).SetRange(0, fuel_capacity)
```

### Integration Points

**SmartNavigator API (must preserve):**
- `plan_route(ship_data, destination, prefer_cruise=True)` → route dict
- `validate_route(ship_data, destination)` → (bool, reason)
- `execute_route(ship_controller, destination)` → bool
- `find_nearest_with_trait(ship_data, trait)` → waypoints list

**RouteOptimizer API (replace internals, keep interface):**
- `find_optimal_route(start, goal, fuel, prefer_cruise=True)` → route dict

**TourOptimizer API (replace with OR-Tools TSP):**
- `optimize_tour(waypoints, start, ship_data, algorithm='2opt')` → ordered list
- `calculate_tour_time(tour, ship_data)` → seconds

### Technical Constraints

1. **Probe Ships (fuel_capacity=0):** Solar powered, no fuel consumption
   - Skip fuel constraints entirely for probes
   - Use direct distance-based routing

2. **Zero-Distance Orbitals:** Planets ↔ Moons = 0 fuel, instant
   - Special case in distance calculation
   - Already handled in existing graph building

3. **Flight Mode Selection (prefer_cruise=True):**
   - Primary: CRUISE mode (minimize time)
   - Fallback: DRIFT only if CRUISE impossible with refuels
   - Never DRIFT for long distances when refuels available

4. **State Machine Integration:**
   - Ship must be IN_ORBIT before navigate
   - Auto-transition DOCKED → IN_ORBIT if needed
   - Handle IN_TRANSIT wait-for-arrival

### Configuration Structure (to create)

```yaml
# config/routing_constants.yaml
flight_modes:
  CRUISE:
    time_multiplier: 31
    fuel_rate: 1.0
  DRIFT:
    time_multiplier: 26
    fuel_rate: 0.003
  BURN:
    time_multiplier: 15
    fuel_rate: 2.0
  STEALTH:
    time_multiplier: 50
    fuel_rate: 1.0

fuel_safety_margin: 0.1
refuel_time_seconds: 5

validation:
  max_deviation_percent: 5.0
  check_interval_hours: 24
  pause_on_failure: true
```

### Dependencies to Add

```txt
# requirements.txt additions
ortools>=9.8.0  # Latest stable VRP solver
pyyaml>=6.0     # For config file parsing
```

### Files to Create

1. `src/spacetraders_bot/core/ortools_router.py`
   - ORToolsRouter class (replaces RouteOptimizer)
   - ORToolsTSP class (replaces TourOptimizer)

2. `src/spacetraders_bot/core/routing_config.py`
   - Load config from YAML
   - Provide constants to router

3. `src/spacetraders_bot/core/routing_validator.py`
   - Validate predictions vs API
   - Track deviation metrics
   - Pause operations on failure

4. `config/routing_constants.yaml`
   - Flight mode constants
   - Validation settings

5. `src/spacetraders_bot/operations/validate_routing.py`
   - CLI command for manual validation
   - Scheduled validation task

### Migration Strategy

**Phase 1: Core Replacement**
1. Create ORToolsRouter with same interface as RouteOptimizer
2. Create config system
3. Update SmartNavigator to use ORToolsRouter

**Phase 2: TSP Integration**
4. Create ORToolsTSP with same interface as TourOptimizer
5. Update scout_coordinator to use ORToolsTSP

**Phase 3: Validation**
6. Implement routing_validator
7. Add validation CLI command
8. Set up periodic validation

**Phase 4: Testing**
9. Run full test suite
10. Fix any test failures (incorrect tests vs bugs)
11. Add new tests for OR-Tools specific behavior

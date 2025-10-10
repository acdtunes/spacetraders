# Initial Request

**Date:** 2025-10-10 01:23 UTC
**Requester:** User

## Request

Implement this route optimization with OR-Tools with the model we discussed and configuration with validation

## Context from Conversation

The user and I have been discussing a recurring bug in the SpaceTraders bot's routing system where ships choose DRIFT mode for long distances (700+ units) when CRUISE mode with intermediate refuel stops would be much faster.

### Problem Analysis Completed

1. **Current Bug:** SmartNavigator/RouteOptimizer allows DRIFT mode for 675+ unit journeys when ship has insufficient fuel capacity for direct CRUISE, even when intermediate refuel stations exist
2. **Impact:** Ships waste 1+ hours in DRIFT mode when 10-15 minute CRUISE routes with refuel stops are available
3. **Root Cause:** Logic flaw in `_should_allow_emergency_drift()` at routing.py:776-823

### Proposed Solution

Replace custom routing logic with Google OR-Tools (industrial-strength optimization library):
- **Library:** google-ortools (Vehicle Routing Problem solver)
- **Problem Type:** Shortest Path Problem with Resource Constraints (SPPRC)
- **Model Type:** Routing Model with Resource Dimensions

### Agreed Upon Model

#### Problem Definition
- **Nodes:** Waypoints with coordinates, fuel availability, traits
- **Edges:** Routes with distance, fuel cost, travel time
- **Ship:** Current location, fuel level, fuel capacity, engine speed
- **Flight Modes:** CRUISE (1.0 fuel/unit, mult 31) vs DRIFT (0.003 fuel/unit, mult 26)
- **Objective:** Minimize total travel time
- **Constraints:** Fuel capacity limits, refuel-only-at-stations, minimum 1 fuel for DRIFT

#### Decision Variables
1. Route sequence: [w₀, w₁, ..., wₙ]
2. Flight modes per edge: {CRUISE, DRIFT}
3. Refuel actions per waypoint: {True, False}
4. Fuel state tracking

#### Constants (Empirically Validated)
```python
FLIGHT_MODE_MULTIPLIERS = {
    'CRUISE': 31,
    'DRIFT': 26
}
FUEL_CONSUMPTION = {
    'CRUISE': 1.0,
    'DRIFT': 0.003
}
```

### Configuration with Validation Strategy

**Option Selected:** Configuration layer + validation layer
- Keep constants in config file (easy updates if SpaceTraders changes formulas)
- Validate predictions against actual API responses periodically
- Alert if deviation >5%

## Deliverables Expected

1. OR-Tools integration for route optimization
2. Configuration system for flight mode constants
3. Validation layer to verify predictions vs actual API behavior
4. Fallback to existing system for edge cases
5. Tests to ensure correctness

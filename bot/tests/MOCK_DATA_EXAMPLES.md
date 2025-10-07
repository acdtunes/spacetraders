# Mock API - Fake Data Examples

## Real Examples from Our Tests

### Example 1: Navigation Test with Complete Fake Data

```python
# From test_navigation_edge_cases_steps.py

# Setup fake waypoints
context['mock_api'].add_waypoint(
    symbol="X1-HU87-A1",
    type="PLANET",
    x=0,
    y=0,
    traits=["MARKETPLACE"]
)

context['mock_api'].add_waypoint(
    symbol="X1-HU87-B9",
    type="ASTEROID",
    x=100,
    y=0,
    traits=["MINERAL_DEPOSITS"]
)

# Setup fake ship
context['mock_api'].set_ship_location("TEST-1", "X1-HU87-A1", "IN_ORBIT")
context['mock_api'].set_ship_fuel("TEST-1", 400, 400)

# Create ship controller with mock API
ship = ShipController(context['mock_api'], "TEST-1")

# Navigate using fake data - NO REAL API CALLS!
navigator = SmartNavigator(context['mock_api'], "X1-HU87")
success = navigator.execute_route(ship, "X1-HU87-B9")

# Mock API simulated the navigation
# - Calculated fuel cost (100 units for 100 distance)
# - Updated ship location to X1-HU87-B9
# - Reduced fuel from 400 to 300
# - All without touching the real API!
```

### Example 2: What the Mock Ship Data Looks Like

```python
# When you call: mock_api.set_ship_location("TEST-1", "X1-HU87-A1")
# The mock creates this complete ship object:

{
    "symbol": "TEST-1",
    "registration": {
        "name": "TEST-1",
        "factionSymbol": "COSMIC",
        "role": "COMMAND"
    },
    "nav": {
        "systemSymbol": "X1-HU87",
        "waypointSymbol": "X1-HU87-A1",
        "route": {
            "destination": {
                "symbol": "X1-HU87-A1",
                "type": "PLANET",
                "systemSymbol": "X1-HU87",
                "x": 0,
                "y": 0
            },
            "departure": { /* ... */ },
            "departureTime": "2025-10-05T01:30:00.000Z",
            "arrival": "2025-10-05T01:30:00.000Z"
        },
        "status": "IN_ORBIT",
        "flightMode": "CRUISE"
    },
    "crew": {
        "current": 50,
        "required": 50,
        "capacity": 80,
        "rotation": "STRICT",
        "morale": 100,
        "wages": 0
    },
    "frame": {
        "symbol": "FRAME_FRIGATE",
        "name": "Frame Frigate",
        "description": "A medium-sized, multi-purpose spacecraft",
        "condition": 1.0,  // 100% condition
        "moduleSlots": 8,
        "mountingPoints": 5,
        "fuelCapacity": 400,
        "requirements": {
            "power": 8,
            "crew": 25,
            "slots": 8
        }
    },
    "fuel": {
        "current": 400,
        "capacity": 400,
        "consumed": {
            "amount": 0,
            "timestamp": "2025-10-05T01:30:00.000Z"
        }
    },
    "cargo": {
        "capacity": 40,
        "units": 0,
        "inventory": []
    },
    // ... plus reactor, engine, modules, mounts, cooldown
}
```

**This matches the EXACT structure from the real SpaceTraders API!**

### Example 3: Testing Edge Cases with Impossible Data

```python
# Test: Ship with zero fuel (impossible in real game)
mock_api.set_ship_fuel("TEST-1", 0, 400)

# Test: Corrupted ship data
ship_data = mock_api.get_ship("TEST-1")
ship_data['nav']['route'] = None  # Remove route info

# Test: Ship with critical damage
ship_data['frame']['condition'] = 0.4  # 40% condition

# Test: Empty graph (no waypoints exist)
mock_api.waypoints = {}

# These scenarios are hard/impossible to create with real API
# But essential for testing error handling!
```

### Example 4: Complete Mining Scenario with Fake Data

```python
# Setup entire fake system
mock_api.add_waypoint(
    symbol="X1-TEST-HQ",
    type="ORBITAL_STATION",
    x=0,
    y=0,
    traits=["MARKETPLACE", "SHIPYARD"]
)

mock_api.add_waypoint(
    symbol="X1-TEST-ASTEROID",
    type="ASTEROID",
    x=50,
    y=0,
    traits=["COMMON_METAL_DEPOSITS", "PRECIOUS_METAL_DEPOSITS"]
)

mock_api.add_market(
    waypoint="X1-TEST-HQ",
    imports=["IRON_ORE", "COPPER_ORE"],
    exports=["FUEL", "SUPPLIES"]
)

# Setup mining ship
mock_api.set_ship_location("MINER-1", "X1-TEST-HQ", "DOCKED")
mock_api.set_ship_fuel("MINER-1", 400, 400)
mock_api.set_ship_cargo("MINER-1", [], capacity=40)

# Execute full mining cycle (all with fake data!)
ship = ShipController(mock_api, "MINER-1")

# 1. Orbit
ship.orbit()
# Mock: status -> IN_ORBIT

# 2. Navigate to asteroid
ship.navigate("X1-TEST-ASTEROID")
# Mock: location -> X1-TEST-ASTEROID, fuel -> 350

# 3. Extract resources
ship.extract()
# Mock: cargo -> [{"symbol": "IRON_ORE", "units": 5}]

# 4. Navigate to market
ship.navigate("X1-TEST-HQ")
# Mock: location -> X1-TEST-HQ, fuel -> 300

# 5. Dock
ship.dock()
# Mock: status -> DOCKED

# 6. Sell cargo
ship.sell("IRON_ORE", 5)
# Mock: cargo -> empty, credits -> 100350

# All of this happened without a single real API call!
```

### Example 5: API Call Tracking

```python
# The mock tracks every "API call"
mock_api = MockAPIClient()

# Make some fake calls
mock_api.get_ship("TEST-1")
mock_api.post("/my/ships/TEST-1/navigate", {"waypointSymbol": "X1-HU87-B9"})
mock_api.post("/my/ships/TEST-1/dock", {})

# Check what was called
print(mock_api.call_log)
# Output:
# [
#   {
#     "method": "GET",
#     "endpoint": "/my/ships/TEST-1",
#     "data": None,
#     "timestamp": "2025-10-05T01:30:01.123Z"
#   },
#   {
#     "method": "POST",
#     "endpoint": "/my/ships/TEST-1/navigate",
#     "data": {"waypointSymbol": "X1-HU87-B9"},
#     "timestamp": "2025-10-05T01:30:02.456Z"
#   },
#   {
#     "method": "POST",
#     "endpoint": "/my/ships/TEST-1/dock",
#     "data": {},
#     "timestamp": "2025-10-05T01:30:03.789Z"
#   }
# ]

# Verify correct API usage in tests
def test_navigation_makes_correct_calls():
    mock_api = MockAPIClient()
    # ... setup ...

    navigator.execute_route(ship, destination)

    # Verify it called the right endpoints
    nav_calls = [c for c in mock_api.call_log if '/navigate' in c['endpoint']]
    assert len(nav_calls) == 1
    assert nav_calls[0]['data']['waypointSymbol'] == destination
```

### Example 6: Simulating API Failures

```python
# Test: What happens when orbit fails?
mock_api.fail_endpoint = "/orbit"

ship = ShipController(mock_api, "TEST-1")
result = ship.orbit()

assert result is False  # Should handle failure gracefully

# Test: What happens when out of fuel during navigation?
mock_api.set_ship_fuel("TEST-1", 10, 400)  # Only 10 fuel

navigator = SmartNavigator(mock_api, "X1-HU87")
route = navigator.plan_route(ship_data, "X1-DISTANT-WAYPOINT")

assert route is None  # Should return None for impossible route
```

## How the Mock Differs from Real API

### Mock API (Our Tests)
```python
# Call is instant
ship_data = mock_api.get_ship("TEST-1")
# Returns immediately with fake data
```

### Real API (Production)
```python
# Call takes ~100-500ms (network latency)
ship_data = api_client.get_ship("TEST-1")
# Makes actual HTTP request to spacetraders.io
# Subject to rate limits (2/sec)
# Requires authentication
# Returns real game state
```

## Complete Test Example: Zero Fuel Edge Case

```python
@given('a ship "TEST-1" at "X1-HU87-A1" with 0 fuel')
def ship_with_zero_fuel(context):
    # Setup fake waypoints
    context['mock_api'].add_waypoint("X1-HU87-A1", "PLANET", 0, 0)
    context['mock_api'].add_waypoint("X1-HU87-B9", "ASTEROID", 100, 0)

    # Setup fake ship with ZERO FUEL
    context['mock_api'].set_ship_location("TEST-1", "X1-HU87-A1", "IN_ORBIT")
    context['mock_api'].set_ship_fuel("TEST-1", 0, 400)  # 0 fuel!

    context['ship'] = ShipController(context['mock_api'], "TEST-1")

@when('I validate the route to "X1-HU87-B9"')
def validate_route(context):
    ship_data = context['mock_api'].get_ship("TEST-1")
    # ship_data['fuel']['current'] = 0

    navigator = SmartNavigator(context['mock_api'], "X1-HU87")
    context['valid'], context['reason'] = navigator.validate_route(
        ship_data,
        "X1-HU87-B9"
    )

@then("the route should be invalid")
def route_invalid(context):
    assert context['valid'] is False

@then('the reason should contain "insufficient fuel"')
def reason_contains_fuel(context):
    assert "insufficient fuel" in context['reason'].lower()
```

**This entire test runs in milliseconds with ZERO real API calls!**

## Why This Matters

### Without Mock (Real API)
- ❌ Slow (network latency)
- ❌ Rate limited (2 req/sec)
- ❌ Requires internet
- ❌ Can't test edge cases (API won't give you 0 fuel ship)
- ❌ Non-deterministic (game state changes)

### With Mock (Fake Data)
- ✅ Fast (milliseconds)
- ✅ No rate limits
- ✅ Works offline
- ✅ Can test any edge case
- ✅ Deterministic (same input = same output)
- ✅ Exact API conformance (we researched the OpenAPI spec!)

## Summary

**Yes, we absolutely use fake data for testing!**

The `MockAPIClient`:
1. ✅ Simulates entire SpaceTraders API
2. ✅ Conforms to OpenAPI v2.3.0 spec
3. ✅ Provides ships, waypoints, markets, agents
4. ✅ Tracks API calls for verification
5. ✅ Allows impossible scenarios for edge case testing
6. ✅ Runs instantly without network calls

**Every test uses fake data - zero real API calls during testing!**

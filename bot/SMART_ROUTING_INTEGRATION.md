# Smart Routing Integration Guide

## Overview

This guide explains how to integrate the intelligent routing engine into bot operations.

## Architecture

```
┌─────────────────────────────────────────────────┐
│           Bot Operations Layer                  │
│  (mining, trading, contracts, etc.)             │
└────────────────┬────────────────────────────────┘
                 │
                 ▼
┌─────────────────────────────────────────────────┐
│         SmartNavigator (NEW)                    │
│  - Route planning                               │
│  - Fuel validation                              │
│  - Mode optimization                            │
│  - Auto-refuel stops                            │
│  - State machine (ROBUST)                       │
└────────────────┬────────────────────────────────┘
                 │
        ┌────────┴────────┐
        ▼                 ▼
┌──────────────┐   ┌─────────────┐
│ RouteOptimizer│   │ShipController│
│ (A* + Fuel)   │   │ (API calls) │
└──────────────┘   └─────────────┘
```

## State Machine

SmartNavigator implements a robust state machine that handles all ship states:

### Ship States

```
┌──────────┐  orbit()   ┌──────────┐  navigate()  ┌───────────┐
│  DOCKED  │────────────→│ IN_ORBIT │─────────────→│IN_TRANSIT │
└──────────┘            └──────────┘              └───────────┘
     ↑                       ↑                           │
     │      dock()           │                           │
     └───────────────────────┘                           │
                                                          │
                                            arrival       │
                                         (automatic)      │
                                                          │
                                                          ↓
                                                     IN_ORBIT
```

### State Transitions

**Transition Table (Declarative):**

```python
STATE_TRANSITIONS = {
    ('DOCKED', 'IN_ORBIT'):     'orbit',
    ('IN_ORBIT', 'DOCKED'):     'dock',
    ('IN_TRANSIT', 'IN_ORBIT'): 'wait',
    ('IN_TRANSIT', 'DOCKED'):   'wait_then_dock',
    # Same state transitions are no-ops
}
```

**Transition Handlers:**

| Handler | Action |
|---------|--------|
| `_handle_orbit()` | Call ship_controller.orbit() |
| `_handle_dock()` | Call ship_controller.dock() |
| `_handle_wait()` | Wait for arrival (ship becomes IN_ORBIT) |
| `_handle_wait_then_dock()` | Wait for arrival + dock |
| `_handle_noop()` | Already in correct state, do nothing |

**execute_route() Flow:**

1. **Initial Validation**
   - Check ship health (integrity >50%)
   - Check fuel capacity exists
   - Warn on cooldown or low integrity

2. **Handle IN_TRANSIT**
   - If already in transit to destination → wait for arrival
   - If in transit to other location → wait, then replan route

3. **Navigation Steps**
   - Lookup (current_state, 'IN_ORBIT') in transition table
   - Execute handler to ensure IN_ORBIT
   - Execute navigation (handles waiting)
   - Verify arrival location and state

4. **Refuel Steps**
   - Lookup (current_state, 'DOCKED') in transition table
   - Execute handler to ensure DOCKED
   - Execute refuel operation
   - Verify fuel increased

5. **Final Verification**
   - Confirm at destination
   - Report final state and fuel

### Edge Cases Handled

| Scenario | Handling |
|----------|----------|
| Ship IN_TRANSIT when execute_route called | Wait for arrival, then proceed |
| Ship DOCKED when navigation needed | Orbit first, then navigate |
| Ship damaged (<50% integrity) | Abort with error message |
| Ship has no fuel capacity | Abort with error message |
| Ship has cooldown remaining | Log warning, proceed |
| Navigation arrives at wrong location | Abort with error |
| Refuel fails | Abort with error |

## Created Files

### 1. `lib/smart_navigator.py`
**Purpose**: Bridge between routing engine and ship operations

**Key Methods**:
- `plan_route()` - Find optimal route using A*
- `validate_route()` - Check if route is feasible
- `get_fuel_estimate()` - Calculate fuel costs
- `execute_route()` - Plan AND execute navigation
- `find_nearest_with_trait()` - Find closest marketplace, etc.

**Usage**:
```python
from smart_navigator import SmartNavigator

# Initialize (auto-loads or builds graph)
navigator = SmartNavigator(api, system="X1-HU87")

# Validate before committing
valid, reason = navigator.validate_route(ship_data, destination)

# Execute with auto-refueling
success = navigator.execute_route(ship_controller, destination)
```

### 2. `operations/mining_smart.py`
**Purpose**: Example integration in mining operation

**Features**:
- Pre-flight route validation
- Fuel economics analysis
- Smart navigation with auto-refuel
- Fuel usage tracking
- Credits per fuel metric

## Integration Steps

### Step 1: Update Existing Operations

**Before** (Dumb navigation):
```python
def mining_operation(args):
    ship = ShipController(api, args.ship)

    # Navigate to asteroid
    ship.navigate(args.asteroid)  # ❌ No fuel checking!
    ship.orbit()
    # ... mine ...

    # Navigate to market
    ship.navigate(args.market)  # ❌ Might run out of fuel!
    ship.dock()
```

**After** (Smart navigation):
```python
def mining_operation(args):
    ship = ShipController(api, args.ship)
    navigator = SmartNavigator(api, system)  # ✅ Graph loaded

    # Validate route before starting
    valid, reason = navigator.validate_route(ship_data, args.asteroid)
    if not valid:
        print(f"Route not feasible: {reason}")
        return 1

    # Smart navigate with auto-refuel
    navigator.execute_route(ship, args.asteroid)  # ✅ Auto refuels if needed
    ship.orbit()
    # ... mine ...

    navigator.execute_route(ship, args.market)  # ✅ Optimal mode selected
    ship.dock()
```

### Step 2: Add ShipController Methods

Add to `lib/ship_controller.py`:

```python
def set_flight_mode(self, mode: str):
    """Set ship flight mode (CRUISE, DRIFT, BURN, STEALTH)"""
    result = self.api.patch(f"/my/ships/{self.ship_symbol}/nav", {
        "flightMode": mode
    })
    return result is not None
```

### Step 3: Migration Plan

**Phase 1: Add smart variants** (non-breaking)
- `smart-mine` command using smart_mining_operation
- `smart-trade` command
- Test in parallel with old commands

**Phase 2: Replace operations** (when confident)
- Update `mining_operation` to use SmartNavigator
- Update `trade_operation` to use SmartNavigator
- Update `contract_operation` to use SmartNavigator

**Phase 3: Remove old code**
- Delete original basic implementations
- Remove `smart-` prefix from commands

## Benefits

### 1. Fuel Safety
**Before**: Ships could get stranded
```
Navigate(A→B) → "ERROR: Not enough fuel"
```

**After**: Pre-validated routes
```
validate_route(A→B) → "Route requires refuel at C"
execute_route() → A→C(refuel)→B ✅
```

### 2. Cost Optimization
**Before**: Always CRUISE (expensive)
```
Trip cost: 200 fuel
```

**After**: Smart mode selection
```
>75% fuel: CRUISE (fast)
<50% fuel: DRIFT (economical)
Trip cost: 50 fuel (75% savings!)
```

### 3. Time Optimization
**Before**: Manual refuel stops
```
A→Market(refuel)→B = 400s
```

**After**: Optimal refuel placement
```
A→B(pass near market, refuel)→C = 300s (25% faster!)
```

## Usage Examples

### Mining with Smart Routing
```bash
# Old way
python3 spacetraders_bot.py mine \
  --ship SHIP-1 \
  --asteroid X1-HU87-B9 \
  --market X1-HU87-A1 \
  --cycles 10
  # ❌ Might fail if fuel insufficient

# New way (when integrated)
python3 spacetraders_bot.py mine \
  --ship SHIP-1 \
  --asteroid X1-HU87-B9 \
  --market X1-HU87-A1 \
  --cycles 10 \
  --smart-routing
  # ✅ Pre-validates, auto-refuels, optimizes modes
```

### Trading with Route Economics
```python
from smart_navigator import SmartNavigator

navigator = SmartNavigator(api, "X1-HU87")

# Get fuel cost for trade route
fuel_estimate = navigator.get_fuel_estimate(ship_data, sell_market)

# Calculate if profitable after fuel
buy_price = 50
sell_price = 70
fuel_cost_credits = fuel_estimate['total_fuel_cost'] * 10  # 10cr/fuel
profit = (sell_price - buy_price) * 40 - fuel_cost_credits

if profit > 5000:
    # Route is profitable, execute
    navigator.execute_route(ship, sell_market)
```

### Emergency Fuel Finding
```python
# Find nearest fuel station
ship_data = ship.get_status()
fuel_stations = navigator.find_nearest_with_trait(ship_data, 'MARKETPLACE')

print(f"Nearest fuel: {fuel_stations[0]['symbol']} "
      f"({fuel_stations[0]['distance']} units away)")

# Navigate with DRIFT to conserve fuel
navigator.execute_route(ship, fuel_stations[0]['symbol'], prefer_cruise=False)
```

## Performance Considerations

### Graph Caching
- Graphs are cached in `graphs/X1-HU87_graph.json`
- First use: ~10s to build graph
- Subsequent uses: <1s to load from cache
- Rebuild if system changes (new waypoints)

### Memory Usage
- Small system (50 waypoints): ~500KB graph
- Large system (200 waypoints): ~8MB graph
- Routes calculated on-demand (not cached)

### API Rate Limits
- Graph building: 5-10 API calls (paginated waypoints)
- Route execution: 2-4 calls per navigation leg
- Stays well under 2 req/sec limit

## Next Steps

1. **Test SmartNavigator** - Add unit tests
2. **Add to mining** - Update operations/mining.py
3. **Add to trading** - Update operations/trading.py
4. **Add command flag** - Add `--smart-routing` to all ops
5. **Measure improvement** - Track fuel savings, time savings
6. **Make default** - Once proven, make smart routing the default

## Troubleshooting

**Q: "Graph build failed for system X1-YZ99"**
A: Check if system symbol is correct. Try `graph-build` command manually.

**Q: "No route found even with DRIFT"**
A: Destination is too far for ship's fuel capacity. Need intermediate stop or bigger fuel tank.

**Q: "Route found but ship gets stranded"**
A: Report as bug - route planner should guarantee feasibility.

**Q: "Smart routing is slower than basic navigation"**
A: Smart routing optimizes for safety + fuel, not always speed. Use `--prefer-speed` flag (TODO).

## Future Enhancements

- [ ] Route caching for repeated paths
- [ ] Multi-ship coordination (avoid fuel station congestion)
- [ ] Dynamic replanning (if waypoint unavailable)
- [ ] Fuel price optimization (refuel at cheapest station)
- [ ] Risk-aware routing (avoid dangerous sectors)
- [ ] Real-time traffic data integration

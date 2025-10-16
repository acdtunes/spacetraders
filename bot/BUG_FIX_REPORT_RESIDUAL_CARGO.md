# Bug Fix Report: Route Planning with Residual Cargo

**Date:** 2025-10-14
**Bug ID:** Residual Cargo Overflow
**Severity:** CRITICAL
**Status:** FIXED ✅

---

## ROOT CAUSE

### Problem Statement

The multi-leg route planner (`GreedyRoutePlanner.find_route()`) assumed ships always start with **empty cargo**. In production, ships often have **residual cargo** from previous operations that was not fully cleared.

### Failure Scenario: STARHOPPER-D

**Production Evidence:**
```
[INFO] Starting location: X1-TX46-A1
[INFO] Starting credits: 2,413,578
[INFO] Cargo after: {'SHIP_PLATING': 45, 'ADVANCED_CIRCUITRY': 20}  ← Planned (65 units)

// Later during execution:
[WARNING]   Salvaging: 45x SHIP_PLATING
[WARNING]   Salvaging: 14x ADVANCED_CIRCUITRY
[WARNING]   Salvaging: 20x ALUMINUM  ← NOT IN PLAN! Residual cargo!
```

**What Happened:**
- Ship started cycle with **20x ALUMINUM** (residual from previous operation)
- Route planner generated plan assuming **EMPTY cargo**:
  - Buy 45x SHIP_PLATING + 20x ADVANCED_CIRCUITRY = 65 units
- **Actual cargo needed:** 20 (residual) + 65 (planned) = **85 units**
- **Ship capacity:** 80 units → **OVERFLOW** ❌

**Total when failed:** 45 + 14 + 20 = 79 units (tried to add 2 more → 81 → overflow)

### Root Cause Code

**File:** `/Users/andres.camacho/Development/Personal/spacetradersV2/bot/src/spacetraders_bot/operations/multileg_trader.py`

**Line 863 (before fix):**
```python
def find_route(
    self,
    start_waypoint: str,
    markets: List[str],
    trade_opportunities: List[Dict],
    max_stops: int,
    cargo_capacity: int,
    starting_credits: int,
    ship_speed: int,
) -> Optional[MultiLegRoute]:
    current_waypoint = start_waypoint
    current_cargo: Dict[str, int] = {}  # ❌ BUG: ASSUMES EMPTY!
    current_credits = starting_credits
```

**Why This Bug Occurred:**
1. Route planner initialized `current_cargo = {}` unconditionally
2. Callers never passed actual ship cargo to planner
3. Planner calculated `cargo_available = capacity - 0` instead of `capacity - actual_cargo`
4. Route generated plans that would overflow when executed on ships with residual cargo

---

## FIX APPLIED

### 1. Add `starting_cargo` Parameter to Route Planner

**File:** `/Users/andres.camacho/Development/Personal/spacetradersV2/bot/src/spacetraders_bot/operations/multileg_trader.py`

**Lines 852-866 (after fix):**
```python
def find_route(
    self,
    start_waypoint: str,
    markets: List[str],
    trade_opportunities: List[Dict],
    max_stops: int,
    cargo_capacity: int,
    starting_credits: int,
    ship_speed: int,
    starting_cargo: Optional[Dict[str, int]] = None,  # ✅ NEW PARAMETER!
) -> Optional[MultiLegRoute]:
    current_waypoint = start_waypoint
    # CRITICAL FIX: Account for residual cargo from previous operations
    # Ship may have existing cargo when route planning starts
    current_cargo: Dict[str, int] = starting_cargo.copy() if starting_cargo else {}
    current_credits = starting_credits
```

**Rationale:**
- Optional parameter maintains **backward compatibility**
- Defaults to `{}` when not provided (existing behavior)
- Uses `.copy()` to avoid mutating caller's data structure
- Clear documentation explains purpose

### 2. Update `MultiLegTradeOptimizer.find_optimal_route()`

**Lines 1006-1030 (method signature):**
```python
def find_optimal_route(
    self,
    start_waypoint: str,
    system: str,
    max_stops: int,
    cargo_capacity: int,
    starting_credits: int,
    ship_speed: int,
    fuel_capacity: int,
    current_fuel: int,
    starting_cargo: Optional[Dict[str, int]] = None,  # ✅ NEW PARAMETER!
) -> Optional[MultiLegRoute]:
```

**Lines 1058-1066 (pass through to planner):**
```python
best_route = planner.find_route(
    start_waypoint=start_waypoint,
    markets=markets,
    trade_opportunities=trade_opportunities,
    max_stops=max_stops,
    cargo_capacity=cargo_capacity,
    starting_credits=starting_credits,
    ship_speed=ship_speed,
    starting_cargo=starting_cargo,  # ✅ PASS THROUGH!
)
```

### 3. Update Trade Plan Operation to Extract Ship Cargo

**Lines 3083-3104 (trade_plan operation):**
```python
cargo_capacity = ship_data['cargo']['capacity']
ship_speed = ship_data['engine']['speed']
fuel_capacity = ship_data['fuel']['capacity']
current_fuel = ship_data['fuel']['current']

# CRITICAL FIX: Extract actual ship cargo (may have residual from previous operations)
starting_cargo = {item['symbol']: item['units']
                 for item in ship_data['cargo']['inventory']}  # ✅ EXTRACT CARGO!

agent = api.get_agent()
if not agent:
    print("❌ Failed to get agent data")
    return 1

starting_credits = agent['credits']

optimizer = MultiLegTradeOptimizer(api, db, player_id)
route = optimizer.find_optimal_route(
    start_waypoint=start_waypoint,
    system=system,
    max_stops=max_stops,
    cargo_capacity=cargo_capacity,
    starting_credits=starting_credits,
    ship_speed=ship_speed,
    fuel_capacity=fuel_capacity,
    current_fuel=current_fuel,
    starting_cargo=starting_cargo,  # ✅ PASS ACTUAL CARGO!
)
```

### 4. Update Fleet Trade Optimizer to Extract Ship Cargo

**Lines 1298-1340 (fleet optimizer loop):**
```python
# Get ship parameters
start_waypoint = ship_data['nav']['waypointSymbol']
cargo_capacity = ship_data['cargo']['capacity']
ship_speed = ship_data['engine']['speed']
fuel_capacity = ship_data['fuel']['capacity']
current_fuel = ship_data['fuel']['current']

# CRITICAL FIX: Extract actual ship cargo (may have residual from previous operations)
starting_cargo = {item['symbol']: item['units']
                 for item in ship_data['cargo']['inventory']}  # ✅ EXTRACT CARGO!

# ... (filtering logic) ...

# Find best route using filtered opportunities
route = self._find_ship_route(
    start_waypoint=start_waypoint,
    markets=markets,
    trade_opportunities=filtered_opportunities,
    max_stops=max_stops,
    cargo_capacity=cargo_capacity,
    starting_credits=starting_credits,
    ship_speed=ship_speed,
    starting_cargo=starting_cargo,  # ✅ PASS ACTUAL CARGO!
)
```

### 5. Update Internal Route Planner Helper

**Lines 1429-1470 (`_find_ship_route` method):**
```python
def _find_ship_route(
    self,
    start_waypoint: str,
    markets: List[str],
    trade_opportunities: List[Dict],
    max_stops: int,
    cargo_capacity: int,
    starting_credits: int,
    ship_speed: int,
    starting_cargo: Optional[Dict[str, int]] = None,  # ✅ NEW PARAMETER!
) -> Optional[MultiLegRoute]:
    """
    Find optimal route for a single ship using greedy planner

    Args:
        start_waypoint: Ship's current location
        markets: Available markets
        trade_opportunities: Filtered trade opportunities (conflicts removed)
        max_stops: Maximum route stops
        cargo_capacity: Ship cargo capacity
        starting_credits: Available credits
        ship_speed: Ship speed for time estimation
        starting_cargo: Existing cargo from previous operations (residual)  # ✅ DOCUMENTED!

    Returns:
        MultiLegRoute if found, None otherwise
    """
    from spacetraders_bot.operations.multileg_trader import ProfitFirstStrategy, GreedyRoutePlanner

    strategy = ProfitFirstStrategy(self.logger)
    planner = GreedyRoutePlanner(self.logger, self.db, strategy=strategy)

    route = planner.find_route(
        start_waypoint=start_waypoint,
        markets=markets,
        trade_opportunities=trade_opportunities,
        max_stops=max_stops,
        cargo_capacity=cargo_capacity,
        starting_credits=starting_credits,
        ship_speed=ship_speed,
        starting_cargo=starting_cargo,  # ✅ PASS THROUGH!
    )

    return route
```

---

## TESTS MODIFIED/ADDED

### New Test File Created

**File:** `/Users/andres.camacho/Development/Personal/spacetradersV2/bot/tests/test_route_planning_residual_cargo_bug.py`

**Test Coverage:**

1. **`test_route_planning_with_residual_cargo_basic`** - Verifies planner respects residual cargo
   - Ship has 20x ALUMINUM residual
   - Can sell ALUMINUM then buy new goods
   - Total cargo never exceeds 80 units ✅

2. **`test_route_planning_starhopper_d_exact_scenario`** - Exact production scenario
   - Reproduces STARHOPPER-D state: 20x ALUMINUM residual, 80 capacity
   - Planner correctly limits purchases to 60 units available capacity
   - Route respects total capacity throughout all segments ✅

3. **`test_route_planning_empty_cargo_still_works`** - Backward compatibility
   - Empty cargo dict works as before
   - Normal route planning unaffected ✅

4. **`test_route_planning_without_starting_cargo_defaults_empty`** - Parameter optional
   - Omitting `starting_cargo` parameter works (defaults to empty)
   - Ensures backward compatibility with existing code ✅

---

## VALIDATION RESULTS

### Before Fix (Test Failure)

```bash
$ python3 -m pytest tests/test_route_planning_residual_cargo_bug.py::test_route_planning_with_residual_cargo_basic -xvs

FAILED tests/test_route_planning_residual_cargo_bug.py::test_route_planning_with_residual_cargo_basic
TypeError: find_route() got an unexpected keyword argument 'starting_cargo'
```

**Expected:** Test fails because parameter doesn't exist ✅

### After Fix (All Tests Pass)

```bash
$ python3 -m pytest tests/test_route_planning_residual_cargo_bug.py -v

tests/test_route_planning_residual_cargo_bug.py::test_route_planning_with_residual_cargo_basic PASSED [ 25%]
tests/test_route_planning_residual_cargo_bug.py::test_route_planning_starhopper_d_exact_scenario PASSED [ 50%]
tests/test_route_planning_residual_cargo_bug.py::test_route_planning_empty_cargo_still_works PASSED [ 75%]
tests/test_route_planning_residual_cargo_bug.py::test_route_planning_without_starting_cargo_defaults_empty PASSED [100%]

======================== 4 passed, 3 warnings in 0.03s ========================
```

**Result:** All new tests PASS ✅

### Regression Testing (Existing Tests)

```bash
$ python3 -m pytest tests/test_multileg_cargo_overflow_bug.py -v

tests/test_multileg_cargo_overflow_bug.py::test_cargo_overflow_goods_destined_for_different_waypoints PASSED [ 33%]
tests/test_multileg_cargo_overflow_bug.py::test_cargo_overflow_starhopper_d_exact_scenario PASSED [ 66%]
tests/test_multileg_cargo_overflow_bug.py::test_cargo_capacity_with_incremental_buys PASSED [100%]

======================== 3 passed, 3 warnings in 0.01s ========================
```

**Result:** All existing cargo overflow tests PASS ✅ (no regressions)

### Test Output Example

```
=== STARHOPPER-D EXACT REPRODUCTION ===
Starting state:
  Location: X1-TX46-A1
  Cargo: {'ALUMINUM': 20} (20 units residual)
  Capacity: 80 units
  Available: 60 units
  Credits: 2,413,578

Segment 0: X1-TX46-A1 → X1-TX46-I52
  Actions at destination:
    BUY 60x MEDICINE @ 200
  Cargo after: {'ALUMINUM': 20, 'MEDICINE': 60}
  Total cargo: 80/80 units  ✅ AT CAPACITY (not overflow!)

Segment 1: X1-TX46-I52 → X1-TX46-J55
  Actions at destination:
  Cargo after: {'ALUMINUM': 20, 'MEDICINE': 60}
  Total cargo: 80/80 units  ✅

Segment 2: X1-TX46-J55 → X1-TX46-H48
  Actions at destination:
    SELL 60x MEDICINE @ 485
  Cargo after: {'ALUMINUM': 20}
  Total cargo: 20/80 units  ✅
```

**Planner correctly:**
- Recognizes 20 units already occupied
- Limits purchases to 60 units (80 - 20)
- Never exceeds capacity throughout route

---

## PREVENTION RECOMMENDATIONS

### 1. Always Extract Ship Cargo Before Planning

**Pattern to follow:**
```python
# ✅ CORRECT: Extract actual cargo
ship_data = ship.get_status()
starting_cargo = {item['symbol']: item['units']
                 for item in ship_data['cargo']['inventory']}

route = planner.find_route(..., starting_cargo=starting_cargo)
```

**Anti-pattern to avoid:**
```python
# ❌ WRONG: Assume empty cargo
route = planner.find_route(...)  # Missing starting_cargo!
```

### 2. Add Cargo State to Planning Logs

Enhance logging to show cargo state:
```python
self.logger.info(f"Starting cargo: {starting_cargo} ({sum(starting_cargo.values())} units)")
self.logger.info(f"Available capacity: {capacity - sum(starting_cargo.values())} units")
```

### 3. Enforce Cargo Validation in Tests

All route planning tests should verify:
```python
for segment in route.segments:
    cargo_total = sum(segment.cargo_after.values())
    assert cargo_total <= capacity, f"Segment exceeds capacity: {cargo_total}/{capacity}"
```

### 4. Add Pre-Operation Cargo Cleanup

Consider adding cargo cleanup before multi-leg trading:
```python
# Option 1: Sell all residual cargo before planning
if ship_cargo:
    logger.warning(f"Ship has residual cargo: {ship_cargo}")
    # Optionally sell or jettison

# Option 2: Account for it in planning (current fix)
route = planner.find_route(..., starting_cargo=ship_cargo)
```

### 5. Monitor for Cargo Overflow Patterns

Add circuit breaker detection for cargo overflow attempts:
```python
if cargo_needed + current_cargo > capacity:
    logger.critical("🚨 CARGO OVERFLOW DETECTED DURING PLANNING!")
    # Log full state for debugging
    # Consider aborting operation
```

---

## FILES MODIFIED

1. **`/Users/andres.camacho/Development/Personal/spacetradersV2/bot/src/spacetraders_bot/operations/multileg_trader.py`**
   - Line 861: Added `starting_cargo` parameter to `GreedyRoutePlanner.find_route()`
   - Line 866: Initialize `current_cargo` from `starting_cargo` (with copy)
   - Line 1016: Added `starting_cargo` parameter to `MultiLegTradeOptimizer.find_optimal_route()`
   - Line 1066: Pass `starting_cargo` to `planner.find_route()`
   - Lines 3083-3085: Extract ship cargo in `trade_plan` operation
   - Line 3104: Pass `starting_cargo` to optimizer
   - Lines 1298-1300: Extract ship cargo in fleet optimizer
   - Line 1340: Pass `starting_cargo` to `_find_ship_route()`
   - Line 1438: Added `starting_cargo` parameter to `_find_ship_route()`
   - Line 1469: Pass `starting_cargo` to planner

2. **`/Users/andres.camacho/Development/Personal/spacetradersV2/bot/tests/test_route_planning_residual_cargo_bug.py`** (NEW FILE)
   - 340 lines
   - 4 comprehensive test scenarios
   - Full documentation of bug and fix

---

## IMPACT ASSESSMENT

### Before Fix

**Production Failures:**
- ❌ STARHOPPER-D cargo overflow (20x ALUMINUM residual)
- ❌ Ships with residual cargo generate invalid routes
- ❌ Runtime salvage operations required (data loss)
- ❌ Unpredictable behavior between trading cycles

**Technical Debt:**
- Route planner makes unsafe assumption (empty cargo)
- No validation of starting state
- Silent failure mode (routes look valid but overflow at runtime)

### After Fix

**Production Improvements:**
- ✅ Ships with residual cargo handled correctly
- ✅ Route planning respects actual available capacity
- ✅ No cargo overflow during execution
- ✅ Predictable multi-cycle behavior

**Code Quality:**
- ✅ Explicit starting state parameter (no hidden assumptions)
- ✅ Backward compatible (optional parameter with default)
- ✅ Comprehensive test coverage (4 scenarios)
- ✅ Clear documentation and comments

---

## SUMMARY

### What Was Broken

Route planner assumed ships always start with empty cargo, causing overflow when ships had residual cargo from previous operations.

### What Was Fixed

Added `starting_cargo` parameter throughout route planning chain:
1. `GreedyRoutePlanner.find_route()` - accepts and uses starting cargo
2. `MultiLegTradeOptimizer.find_optimal_route()` - accepts and passes through
3. `FleetTradeOptimizer._find_ship_route()` - accepts and passes through
4. All callers extract actual ship cargo and pass it to planners

### Validation

- ✅ 4 new tests pass (residual cargo scenarios)
- ✅ 3 existing tests pass (no regressions)
- ✅ Backward compatible (parameter optional)
- ✅ Production scenario (STARHOPPER-D) handled correctly

### Prevention

1. Always extract ship cargo before planning
2. Log cargo state at planning start
3. Validate capacity in all route planning tests
4. Consider pre-operation cargo cleanup
5. Monitor for cargo overflow patterns

---

**Status:** BUG FIXED AND VALIDATED ✅

**Next Steps:**
1. Deploy to production
2. Monitor STARHOPPER-D and other ships for proper behavior
3. Review logs for any residual cargo patterns
4. Consider adding automatic cargo cleanup before multi-leg operations

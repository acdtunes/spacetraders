# Mining Operation Improvements

## Summary

Added intelligent cargo management and circuit breaker logic to prevent mining operations from getting stuck when extracting the wrong resources or mining at unproductive asteroids.

## Problems Solved

### 1. Wrong Cargo Accumulation
**Problem:** Ship mines 39 units but gets IRON_ORE, COPPER_ORE, SILICON_CRYSTALS, QUARTZ_SAND - ZERO of the needed ALUMINUM_ORE. Cargo fills up with wrong materials preventing further extraction.

**Solution:** Automatic cargo jettison when mining for specific resources.

### 2. No Circuit Breaker
**Problem:** Ship keeps mining the same asteroid forever even if it never yields the target resource.

**Solution:** Circuit breaker that stops after 10 consecutive failed extractions (configurable).

### 3. No Alternative Asteroid Search
**Problem:** No automatic fallback when an asteroid doesn't contain the target resource.

**Solution:** Automatically searches for alternative asteroids with the same resource traits and switches to them.

## Files Modified

### 1. `/lib/ship_controller.py`
**Added method:** `jettison_wrong_cargo(target_resource, cargo_threshold=0.8)`

```python
def jettison_wrong_cargo(self, target_resource: str, cargo_threshold: float = 0.8) -> Dict[str, int]:
    """
    Jettison non-target resources when cargo is filling up

    Args:
        target_resource: The resource we want to keep (e.g., "ALUMINUM_ORE")
        cargo_threshold: Jettison when cargo exceeds this percentage (default 80%)

    Returns:
        Dict with jettisoned items: {"IRON_ORE": 5, "COPPER_ORE": 3, ...}
    """
```

**Behavior:**
- Only activates when cargo exceeds threshold (default 80%)
- Jettisons ALL non-target resources
- Keeps only the target resource
- Logs what was jettisoned and why
- Returns dict of jettisoned items for tracking

**Example log output:**
```
⚠️  Cargo at 87.5% - jettisoning non-target resources
🚮 Jettisoning 20 x IRON_ORE...
✅ Jettisoned 20 x IRON_ORE
🚮 Jettisoned 20 x IRON_ORE (keeping space for ALUMINUM_ORE)
🚮 Jettisoning 15 x COPPER_ORE...
✅ Jettisoned 15 x COPPER_ORE
🚮 Jettisoned 15 x COPPER_ORE (keeping space for ALUMINUM_ORE)
📦 Freed 35 cargo units by jettisoning 2 item types
```

### 2. `/operations/mining.py`
**Added function:** `targeted_mining_with_circuit_breaker()`

```python
def targeted_mining_with_circuit_breaker(
    ship: ShipController,
    navigator: SmartNavigator,
    asteroid: str,
    target_resource: str,
    units_needed: int,
    max_consecutive_failures: int = 10
) -> Tuple[bool, int, str]:
    """
    Mine a specific resource with circuit breaker for wrong cargo

    Returns:
        Tuple of (success: bool, units_collected: int, reason: str)
    """
```

**Features:**
- Tracks consecutive failures (didn't get target resource)
- Jettisons wrong cargo when cargo fills up (80% threshold)
- Circuit breaker: stops after max consecutive failures
- Returns success status, units collected, and failure reason

**Circuit Breaker Logic:**
```python
# Track consecutive failures
if extracted_symbol == target_resource:
    units_collected += extracted_units
    consecutive_failures = 0  # Reset on success
else:
    consecutive_failures += 1

# Check circuit breaker
if consecutive_failures >= max_consecutive_failures:
    print("🛑 CIRCUIT BREAKER TRIGGERED!")
    print(f"   {consecutive_failures} consecutive extractions without {target_resource}")
    print(f"   This asteroid may not contain {target_resource}")
    print(f"   Recommend: Switch to different asteroid or buy from market")
    return False, units_collected, f"Circuit breaker: {consecutive_failures} consecutive failures"
```

**Example log output:**
```
🎯 Targeted mining: ALUMINUM_ORE (need 40 units)
   Circuit breaker: Will stop after 10 consecutive failures

1. Navigating to asteroid X1-HU87-B9...
✅ Arrived at X1-HU87-B9

⛏️  Extracting resources...
✅ Extracted: IRON_ORE x5
⚠️  Got 5 x IRON_ORE instead (failure #1)

⛏️  Extracting resources...
✅ Extracted: COPPER_ORE x3
⚠️  Got 3 x COPPER_ORE instead (failure #2)

⛏️  Extracting resources...
✅ Extracted: ALUMINUM_ORE x4
✅ Got 4 x ALUMINUM_ORE (total: 4/40)

⚠️  Cargo at 82.5% - jettisoning non-target resources
🚮 Jettisoned 5 x IRON_ORE (keeping space for ALUMINUM_ORE)
🚮 Jettisoned 3 x COPPER_ORE (keeping space for ALUMINUM_ORE)
📦 Freed 8 cargo units by jettisoning 2 item types
```

**Added function:** `find_alternative_asteroids()`

```python
def find_alternative_asteroids(
    api: APIClient,
    system: str,
    current_asteroid: str,
    target_resource: str
) -> list:
    """
    Find alternative asteroids in the system that may contain the target resource

    Returns:
        List of alternative asteroid waypoint symbols
    """
```

**Features:**
- Maps resources to asteroid traits (e.g., ALUMINUM_ORE → COMMON_METAL_DEPOSITS, MINERAL_DEPOSITS)
- Searches all waypoints in system
- Filters for asteroids with matching traits
- Excludes STRIPPED asteroids (depleted)
- Excludes current asteroid
- Returns list of alternatives

**Resource to Trait Mapping:**
```python
resource_to_traits = {
    "ALUMINUM_ORE": ["COMMON_METAL_DEPOSITS", "MINERAL_DEPOSITS"],
    "IRON_ORE": ["COMMON_METAL_DEPOSITS"],
    "COPPER_ORE": ["COMMON_METAL_DEPOSITS"],
    "QUARTZ_SAND": ["MINERAL_DEPOSITS", "COMMON_METAL_DEPOSITS"],
    "SILICON_CRYSTALS": ["MINERAL_DEPOSITS", "CRYSTALLINE_STRUCTURES"],
    "GOLD_ORE": ["PRECIOUS_METAL_DEPOSITS"],
    "SILVER_ORE": ["PRECIOUS_METAL_DEPOSITS"],
    "PLATINUM_ORE": ["PRECIOUS_METAL_DEPOSITS"]
}
```

### 3. `/operations/contracts.py`
**Updated:** `contract_operation()` to use targeted mining with circuit breaker

**New logic:**
1. **Try targeted mining with circuit breaker** (if `--mine-from` specified)
2. **If circuit breaker triggers**, search for alternative asteroids
3. **Try alternative asteroid** (if found)
4. **Fall back to purchasing** (if mining fails and `--buy-from` specified)

**Example workflow:**
```python
# Strategy 1: Mining with circuit breaker
if hasattr(args, 'mine_from') and args.mine_from:
    success, units_mined, reason = targeted_mining_with_circuit_breaker(
        ship=ship,
        navigator=navigator,
        asteroid=args.mine_from,
        target_resource=delivery['tradeSymbol'],
        units_needed=to_mine,
        max_consecutive_failures=10
    )

    if not success:
        # Try alternative asteroids
        alternatives = find_alternative_asteroids(
            api=api,
            system=system,
            current_asteroid=args.mine_from,
            target_resource=delivery['tradeSymbol']
        )

        if alternatives:
            success, units_mined, reason = targeted_mining_with_circuit_breaker(
                ship=ship,
                navigator=navigator,
                asteroid=alternatives[0],  # Try first alternative
                target_resource=delivery['tradeSymbol'],
                units_needed=to_mine,
                max_consecutive_failures=10
            )

        if not success and args.buy_from:
            # Fall back to purchasing
            print("💡 Falling back to purchasing from market")
```

## Tests Added

### Test file: `/tests/test_targeted_mining_steps.py`
### Feature file: `/tests/bdd/features/targeted_mining.feature`

**Test scenarios:**
1. ✅ Jettison wrong cargo when mining for specific resource
2. ✅ No jettison when cargo below threshold
3. ✅ Keep target resource when jettisoning
4. ✅ Circuit breaker triggers after consecutive failures
5. ✅ Jettison multiple cargo types when above threshold

**Test results:**
```
tests/test_targeted_mining_steps.py::test_jettison_wrong_cargo_when_mining_for_specific_resource PASSED [ 20%]
tests/test_targeted_mining_steps.py::test_no_jettison_when_cargo_below_threshold PASSED [ 40%]
tests/test_targeted_mining_steps.py::test_keep_target_resource_when_jettisoning PASSED [ 60%]
tests/test_targeted_mining_steps.py::test_circuit_breaker_triggers_after_consecutive_failures PASSED [ 80%]
tests/test_targeted_mining_steps.py::test_jettison_multiple_cargo_types_when_above_threshold PASSED [100%]

============================== 5 passed in 4.32s
```

**Regression tests:** All existing cargo operation tests pass (13/13)

## Usage Examples

### Contract Fulfillment with Mining

**New CLI argument:** `--mine-from ASTEROID`

```bash
# Old way (buy only)
python3 spacetraders_bot.py contract \
  --token TOKEN \
  --ship SHIP-1 \
  --contract-id CONTRACT_ID \
  --buy-from X1-HU87-B7

# New way (mine with circuit breaker, fallback to buy)
python3 spacetraders_bot.py contract \
  --token TOKEN \
  --ship SHIP-1 \
  --contract-id CONTRACT_ID \
  --mine-from X1-HU87-B9 \
  --buy-from X1-HU87-B7
```

**What happens:**
1. Ship navigates to X1-HU87-B9
2. Mines for target resource (e.g., ALUMINUM_ORE)
3. Jettisons wrong cargo when cargo >80% full
4. If 10 consecutive failures → circuit breaker triggers
5. Searches for alternative asteroids with same traits
6. Tries alternative asteroid
7. If still fails → falls back to buying from X1-HU87-B7

### Direct Targeted Mining

```python
from operations.mining import targeted_mining_with_circuit_breaker
from lib.ship_controller import ShipController
from lib.smart_navigator import SmartNavigator

ship = ShipController(api, "SHIP-1")
navigator = SmartNavigator(api, "X1-HU87")

success, units, reason = targeted_mining_with_circuit_breaker(
    ship=ship,
    navigator=navigator,
    asteroid="X1-HU87-B9",
    target_resource="ALUMINUM_ORE",
    units_needed=40,
    max_consecutive_failures=10
)

if success:
    print(f"✅ Collected {units} units of ALUMINUM_ORE")
else:
    print(f"❌ Mining failed: {reason}")
```

## How Circuit Breaker Works

### Tracking State
- **consecutive_failures**: Counter incremented on each non-target extraction
- **Reset on success**: Counter resets to 0 when target resource extracted
- **Max threshold**: Default 10 (configurable)

### Trigger Conditions
```python
if consecutive_failures >= max_consecutive_failures:
    # Stop mining, report failure
    return False, units_collected, f"Circuit breaker: {consecutive_failures} consecutive failures"
```

### Example Scenario

**Scenario:** Mining ALUMINUM_ORE from asteroid that only has IRON_ORE, COPPER_ORE

```
Extraction 1: IRON_ORE     → consecutive_failures = 1
Extraction 2: COPPER_ORE   → consecutive_failures = 2
Extraction 3: IRON_ORE     → consecutive_failures = 3
...
Extraction 10: COPPER_ORE  → consecutive_failures = 10 → CIRCUIT BREAKER!
```

**Result:**
```
🛑 CIRCUIT BREAKER TRIGGERED!
   10 consecutive extractions without ALUMINUM_ORE
   This asteroid may not contain ALUMINUM_ORE
   Recommend: Switch to different asteroid or buy from market

🔍 Searching for alternative asteroids with ALUMINUM_ORE...
   Found: X1-HU87-C5 with traits ['MINERAL_DEPOSITS', 'COMMON_METAL_DEPOSITS']
   Found: X1-HU87-D8 with traits ['COMMON_METAL_DEPOSITS']

📍 Found 2 alternative asteroids

🔄 Trying alternative asteroid: X1-HU87-C5
```

## Configuration

### Cargo Jettison Threshold
Default: 80% (0.8)

```python
# Default behavior (jettison at 80%)
ship.jettison_wrong_cargo(target_resource="ALUMINUM_ORE")

# Custom threshold (jettison at 90%)
ship.jettison_wrong_cargo(target_resource="ALUMINUM_ORE", cargo_threshold=0.9)

# Aggressive (jettison at 50%)
ship.jettison_wrong_cargo(target_resource="ALUMINUM_ORE", cargo_threshold=0.5)
```

### Circuit Breaker Max Failures
Default: 10 consecutive failures

```python
# Default (10 failures)
targeted_mining_with_circuit_breaker(..., max_consecutive_failures=10)

# Stricter (5 failures)
targeted_mining_with_circuit_breaker(..., max_consecutive_failures=5)

# More lenient (20 failures)
targeted_mining_with_circuit_breaker(..., max_consecutive_failures=20)
```

## Benefits

### 1. No More Cargo Deadlock
- ✅ Automatically jettisons wrong materials
- ✅ Keeps cargo space available for target resource
- ✅ Logs all jettison operations for transparency

### 2. No More Infinite Mining Loops
- ✅ Circuit breaker stops unproductive mining
- ✅ Saves time and fuel
- ✅ Provides actionable feedback

### 3. Intelligent Recovery
- ✅ Automatically searches for alternatives
- ✅ Tries alternative asteroids with same traits
- ✅ Falls back to purchasing if mining fails

### 4. Better Visibility
- ✅ Detailed logging of extraction attempts
- ✅ Success rate tracking
- ✅ Clear failure reasons

## Example Log Output (Full Workflow)

```
═══════════════════════════════════════════════════════════════════
CONTRACT FULFILLMENT OPERATION
═══════════════════════════════════════════════════════════════════

1. Getting contract details...

Delivery Requirements:
  Resource: ALUMINUM_ORE
  Required: 40
  Fulfilled: 0
  Remaining: 40
  Destination: X1-HU87-A1

3. Acquiring resources...
  Already have: 0 units
  Still need: 40 units
  Cargo space available: 40/40

  🎯 Mining strategy: Extract ALUMINUM_ORE from X1-HU87-B9

🎯 Targeted mining: ALUMINUM_ORE (need 40 units)
   Circuit breaker: Will stop after 10 consecutive failures

1. Navigating to asteroid X1-HU87-B9...
🚀 Navigating X1-HU87-A1 → X1-HU87-B9 (CRUISE)...
✅ Arrived at X1-HU87-B9

⛏️  Extracting resources...
✅ Extracted: IRON_ORE x5
⚠️  Got 5 x IRON_ORE instead (failure #1)

⛏️  Extracting resources...
✅ Extracted: COPPER_ORE x3
⚠️  Got 3 x COPPER_ORE instead (failure #2)

⛏️  Extracting resources...
✅ Extracted: QUARTZ_SAND x4
⚠️  Got 4 x QUARTZ_SAND instead (failure #3)

[... 7 more failures ...]

⛏️  Extracting resources...
✅ Extracted: IRON_ORE x2
⚠️  Got 2 x IRON_ORE instead (failure #10)

🛑 CIRCUIT BREAKER TRIGGERED!
   10 consecutive extractions without ALUMINUM_ORE
   This asteroid may not contain ALUMINUM_ORE
   Recommend: Switch to different asteroid or buy from market

  🛑 Mining failed: Circuit breaker: 10 consecutive failures

🔍 Searching for alternative asteroids with ALUMINUM_ORE...
   Found: X1-HU87-C5 with traits ['MINERAL_DEPOSITS']
   Found: X1-HU87-D8 with traits ['COMMON_METAL_DEPOSITS']

📍 Found 2 alternative asteroids

  🔄 Trying alternative asteroid: X1-HU87-C5

🎯 Targeted mining: ALUMINUM_ORE (need 40 units)
   Circuit breaker: Will stop after 10 consecutive failures

1. Navigating to asteroid X1-HU87-C5...
✅ Arrived at X1-HU87-C5

⛏️  Extracting resources...
✅ Extracted: ALUMINUM_ORE x6
✅ Got 6 x ALUMINUM_ORE (total: 6/40)

⛏️  Extracting resources...
✅ Extracted: ALUMINUM_ORE x4
✅ Got 4 x ALUMINUM_ORE (total: 10/40)

[... continues until 40 units collected ...]

✅ Collected 40 units of ALUMINUM_ORE
   Total extractions: 65
   Success rate: 61.5%

4. Delivering to X1-HU87-A1...
  Trip 1: Delivering 40 units...
  ✅ Delivered 40 units (total: 40/40)

5. Fulfilling contract...
🎉 Contract fulfilled! Payment: 50,000 credits
```

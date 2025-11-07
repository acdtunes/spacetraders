# Bug Fix: UNKNOWN Cargo Symbols in Contract Workflows

## Problem Statement

Contract workflows were making wrong cargo decisions because ship cargo was labeled "UNKNOWN" instead of actual cargo symbols (IRON_ORE, COPPER_ORE, etc.).

### Root Causes (Multiple Locations)

**1. `_ship_converter.py` (Navigation Commands)** ⚠️ PRIMARY FIX

The `convert_api_ship_to_entity()` helper function in `src/application/navigation/commands/_ship_converter.py` was **not extracting cargo inventory** from API responses. This function is used by:
- NavigateShipCommand
- DockShipCommand
- OrbitShipCommand
- RefuelShipCommand
- MockShipRepository.sync_from_api() (test fixture)

It only extracted:
- `cargo.capacity` (max)
- `cargo.units` (total)

But **ignored** `cargo.inventory` array containing the actual items with symbols!

**2. `SyncShipsCommand` (Deprecated)** ⚠️ PREVIOUSLY FIXED

The `SyncShipsCommand` handler was also **not extracting cargo inventory** from the SpaceTraders API response.

This caused:
1. Ship entities created from API with cargo_units > 0 but no inventory
2. Ship.__init__ created UNKNOWN placeholder for backward compatibility
3. Workflow tried to jettison "UNKNOWN" → API rejected (unknown symbol)
4. Workflow made wrong decisions (e.g., purchasing when cargo was already correct)

### Example Failure Scenario

```python
# Ship actually has 40 units of IRON_ORE
# But after sync from API:
ship.cargo.inventory = (CargoItem(symbol="UNKNOWN", units=40),)

# Contract requires 40 units of IRON_ORE
required_symbol = "IRON_ORE"

# Workflow checks:
current_units = ship.cargo.get_item_units("IRON_ORE")  # Returns 0 ❌
has_wrong_cargo = ship.cargo.has_items_other_than("IRON_ORE")  # Returns True ❌

# Workflow decides to jettison + purchase (wrong!)
# Should skip both since cargo is already correct
```

## Solution

### 1. Extract Cargo Inventory in _ship_converter.py (PRIMARY FIX)

**File:** `src/application/navigation/commands/_ship_converter.py`

This is the **critical fix** that eliminates UNKNOWN cargo in all navigation operations.

**Before:**
```python
# Only extracted totals, ignored inventory
cargo = ship_data.get("cargo", {})
cargo_capacity = cargo.get("capacity", 0)
cargo_units = cargo.get("units", 0)

# Create Ship entity
return Ship(
    ship_symbol=ship_data["symbol"],
    cargo_capacity=cargo_capacity,
    cargo_units=cargo_units,  # No inventory! Ship.__init__ creates UNKNOWN
    nav_status=nav_status
    # ... no cargo parameter
)
```

**After:**
```python
# Extract cargo data with inventory
cargo_data = ship_data.get("cargo", {})
cargo_capacity = cargo_data.get("capacity", 0)
cargo_units = cargo_data.get("units", 0)

# CRITICAL: Extract cargo inventory from API response
inventory_data = cargo_data.get("inventory", [])
cargo_items = tuple(
    CargoItem(
        symbol=item['symbol'],  # Actual symbol from API!
        name=item.get('name', item['symbol']),
        description=item.get('description', ''),
        units=item['units']
    )
    for item in inventory_data
)

# Create Cargo object with inventory
cargo = Cargo(
    capacity=cargo_capacity,
    units=cargo_units,
    inventory=cargo_items
)

# Create Ship entity with cargo inventory
return Ship(
    ship_symbol=ship_data["symbol"],
    cargo_capacity=cargo_capacity,
    cargo_units=cargo_units,
    nav_status=nav_status,
    cargo=cargo,  # Pass cargo with actual inventory
    # ...
)
```

### 2. Extract Cargo Inventory in SyncShipsCommand (Previously Fixed)

**File:** `src/application/navigation/commands/sync_ships.py` (deprecated, removed from codebase)

### 3. Add Defensive Check in Batch Workflow

**File:** `src/application/contracts/commands/batch_contract_workflow.py`

Added fail-fast validation after sync:

```python
# Sync ship from API to get actual cargo symbols
sync_cmd = SyncShipsCommand(player_id=request.player_id)
await self._mediator.send_async(sync_cmd)

ship = self._ship_repository.find_by_symbol(...)

# Defensive check: Fail fast if UNKNOWN persists after sync
for item in ship.cargo.inventory:
    if item.symbol == "UNKNOWN":
        raise ValueError(
            f"Ship {ship.ship_symbol} has UNKNOWN cargo even after API sync. "
            "This indicates incomplete API response or mapper bug."
        )
```

### 4. Add Warning When UNKNOWN Created

**File:** `src/domain/shared/ship.py`

Added logging when UNKNOWN placeholder is created:

```python
if cargo_units > 0:
    logger.warning(
        f"Ship {ship_symbol} created with {cargo_units} cargo units but no inventory - "
        "creating UNKNOWN placeholder. Ship should be synced from API to get actual cargo symbols."
    )
```

## Test Coverage

### Primary Fix (_ship_converter.py)

Created comprehensive BDD tests:

**File:** `tests/bdd/features/application/navigation/ship_converter_cargo.feature`

Scenarios:
1. ✅ Ship converter extracts cargo inventory from API response (multiple items)
2. ✅ Ship converter handles empty cargo inventory
3. ✅ Ship converter handles cargo with no inventory field (backward compatibility)

**Test Results:**
- All 3 scenarios pass
- Full test suite: **1122 passed in 87.36s**
- Zero failures, zero warnings

### Previous Fix (SyncShipsCommand - deprecated)

**File:** `tests/bdd/features/application/navigation/sync_ships_cargo_extraction.feature` (removed with SyncShipsCommand)

## Impact

### Before Fix
- Ship cargo labeled "UNKNOWN" after API sync
- Workflow jettisoned valid cargo
- Workflow purchased duplicate cargo
- Contract operations failed with API errors

### After Fix
- Ship cargo has actual symbols (IRON_ORE, COPPER_ORE, etc.)
- Workflow makes correct cargo decisions
- No unnecessary jettison or purchase
- Contract operations succeed

## Business Rules

1. **Always sync BEFORE cargo decisions** - Never make decisions on stale/UNKNOWN data
2. **API is source of truth** - Not database cache or placeholders
3. **UNKNOWN is a red flag** - Indicates data needs refresh from API
4. **Fail fast if UNKNOWN persists** - After sync, UNKNOWN = data integrity error

## Related Files

### Primary Fix (_ship_converter.py)

Modified:
- `src/application/navigation/commands/_ship_converter.py` - **PRIMARY FIX:** Extract cargo inventory from API responses
- Added `Cargo` and `CargoItem` imports
- Extract inventory array from `cargo.inventory` field
- Create `Cargo` object with full inventory
- Pass `cargo` parameter to Ship constructor

Added:
- `tests/bdd/features/application/navigation/ship_converter_cargo.feature` - BDD test coverage
- `tests/bdd/steps/application/navigation/test_ship_converter_cargo_steps.py` - Step definitions

Deleted:
- `tests/bdd/steps/application/navigation/conftest.py` - Removed local conftest that was overriding parent fixture

### Previous Fixes (Historical Context)

Modified (no longer in codebase):
- `src/application/navigation/commands/sync_ships.py` - Removed (deprecated command)
- `src/application/contracts/commands/batch_contract_workflow.py` - Add defensive check
- `src/domain/shared/ship.py` - Add warning for UNKNOWN creation
- `tests/bdd/steps/application/test_sync_ships_command_steps.py` - Removed with SyncShipsCommand
- `tests/bdd/steps/application/contracts/test_batch_workflow_simple_steps.py` - Fix mock

Removed:
- `tests/bdd/features/application/navigation/sync_ships_cargo_extraction.feature` - Removed with SyncShipsCommand
- `tests/bdd/steps/application/navigation/test_sync_ships_cargo_extraction_steps.py` - Removed with SyncShipsCommand

## Success Criteria

✅ **_ship_converter.py** extracts cargo inventory from API responses
✅ Ships created by **NavigateShipCommand** have actual cargo symbols
✅ Ships created by **DockShipCommand** have actual cargo symbols
✅ Ships created by **OrbitShipCommand** have actual cargo symbols
✅ Ships created by **RefuelShipCommand** have actual cargo symbols
✅ MockShipRepository.sync_from_api() in tests creates ships with real cargo
✅ Workflow never makes decisions on UNKNOWN cargo
✅ Defensive check catches UNKNOWN after sync (if it ever happens)
✅ All **1122 tests pass** in 87.36s
✅ **Zero warnings** or errors in test output
✅ No "creating UNKNOWN placeholder" warnings in logs

## TDD Process Followed

This fix was implemented using strict TDD/BDD principles:

### RED Phase
1. ✅ Wrote failing BDD test in `ship_converter_cargo.feature`
2. ✅ Confirmed test fails with: `AssertionError: Expected 2 cargo items, got 1`
3. ✅ Verified warning log: "creating UNKNOWN placeholder"

### GREEN Phase
1. ✅ Implemented minimal fix in `_ship_converter.py`
2. ✅ Added cargo inventory extraction from API response
3. ✅ All 3 scenarios pass

### REFACTOR Phase
1. ✅ Updated docstring to reflect change
2. ✅ Verified full test suite passes (1122 tests)
3. ✅ Zero regressions introduced

## Verification Commands

Run ship converter tests:
```bash
export PYTHONPATH=src:$PYTHONPATH
pytest tests/bdd/steps/application/navigation/test_ship_converter_cargo_steps.py -v
```

Run all tests:
```bash
./run_tests.sh
```

Check for UNKNOWN cargo warnings in logs:
```bash
grep -r "UNKNOWN placeholder" var/logs/
```

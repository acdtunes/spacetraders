# Scout Deployment Config Isolation Fix

## Bug Description

When deploying 3 scout ships (ENDURANCE-2, ENDURANCE-3, ENDURANCE-4), all 3 containers controlled ENDURANCE-4 only. Container names were correct (scout-tour-endurance-2, scout-tour-endurance-3, scout-tour-endurance-4), but all containers controlled the same ship.

## Root Cause

The issue was in `src/adapters/primary/daemon/container_manager.py`. The config dict passed to `create_container()` was being stored directly in both:
1. The `ContainerInfo` dataclass
2. The `BaseContainer` instance

Without deep copying, if any code inadvertently shared config dict references (through caching, optimization, or mutation), multiple containers could end up with the same config dict object.

## Solution

Added defensive deep copying of the config dict in `ContainerManager.create_container()`:

```python
# CRITICAL: Deep copy config to ensure container isolation
# Without this, multiple containers could share the same config dict
# and mutations would affect all containers
config_copy = deepcopy(config)
```

This ensures that each container gets its own independent config dict, even if the caller passes the same dict object multiple times.

## Changes Made

### 1. Added Deep Copy to Container Manager

**File**: `src/adapters/primary/daemon/container_manager.py`

- Added `from copy import deepcopy` import
- Deep copy config before storing in ContainerInfo
- Deep copy config before passing to container instance
- Added comment explaining the critical nature of this isolation

### 2. Added Test for Config Isolation

**File**: `tests/bdd/features/application/scouting/scout_markets_ship_assignment.feature`

New feature file testing that each container is assigned to the correct ship with independent configs.

**File**: `tests/bdd/steps/application/scouting/test_scout_markets_ship_assignment_steps.py`

New test suite that:
- Captures exact config dicts passed to daemon.create_container()
- Verifies each container gets the correct ship_symbol
- Verifies config dicts are independent objects (not shared references)
- Verifies nested dicts (config, params, markets) are also independent

## Test Results

### New Test
```
test_scout_markets_ship_assignment_steps.py::test_multiple_scout_containers_each_control_their_assigned_ship PASSED
```

Verifies:
- 3 containers created for 3 ships
- Each container assigned to correct ship (ENDURANCE-2, ENDURANCE-3, ENDURANCE-4)
- Each container's markets match its VRP assignment
- All config dicts are independent objects

### Full Test Suite
```
======================= 1125 passed in 88.60s ========================
```

All tests pass with zero warnings.

## Impact

This fix ensures:
1. **Container Isolation**: Each container operates independently with its own config
2. **Correct Ship Assignment**: Each scout container controls the ship it was assigned
3. **VRP Assignment Integrity**: Market assignments per ship are preserved correctly
4. **Defensive Programming**: Protection against accidental config sharing at any layer

## Prevention

The deep copy approach provides defense-in-depth:
- Even if scout_markets.py accidentally reused dicts, containers would remain isolated
- Even if JSON parsing optimized and shared objects, containers would remain isolated
- Even if future code mutations occur, they won't affect other containers

## Related Files

- `src/adapters/primary/daemon/container_manager.py` - Fix implementation
- `src/adapters/primary/daemon/base_container.py` - Container config storage
- `src/application/scouting/commands/scout_markets.py` - Container deployment
- `tests/bdd/features/application/scouting/scout_markets_ship_assignment.feature` - Test spec
- `tests/bdd/steps/application/scouting/test_scout_markets_ship_assignment_steps.py` - Test implementation

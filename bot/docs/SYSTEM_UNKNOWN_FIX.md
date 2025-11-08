# Bug Fix: System: Unknown in Ship Info

**Date:** 2025-11-07
**Severity:** CRITICAL
**Status:** FIXED

## Summary
All ENDURANCE fleet ships reported "System: Unknown" in ship_info output, blocking routing calculations. Fixed by correcting system_symbol assignment in waypoint reconstruction.

## Root Cause
In `ship_repository.py:248`, the `_reconstruct_waypoint` method was trying to read `systemSymbol` from the graph waypoint data:

```python
system_symbol=wp_data.get("systemSymbol"),  # Returns None - field doesn't exist!
```

However, the graph data structure does NOT include `systemSymbol` in waypoint objects. The graph only stores:
- x, y coordinates
- type
- traits
- has_fuel flag
- orbitals

The system symbol is NOT stored in the graph because it's redundant (all waypoints in a graph belong to the same system).

## The Fix
Changed line 249 to use the `system_symbol` parameter (which comes from the API response) instead of trying to read it from the graph:

```python
system_symbol=system_symbol,  # Use parameter, not graph data
```

**File:** `src/adapters/secondary/persistence/ship_repository.py:249`

## Verification
Before fix:
```
ENDURANCE-1: System: Unknown
ENDURANCE-2: System: Unknown
ENDURANCE-3: System: Unknown
ENDURANCE-4: System: Unknown
```

After fix:
```
ENDURANCE-1: System: X1-HZ85 ✅
ENDURANCE-2: System: X1-HZ85 ✅
ENDURANCE-3: System: X1-HZ85 ✅
ENDURANCE-4: System: X1-HZ85 ✅
```

## Impact
- Unblocks ship_info from showing correct system
- Required (but not sufficient) for routing engine to work
- Routing engine still has separate issues preventing navigation

## Related Issues
- Routing engine still returns "No route found" even with correct system
- Need separate investigation into why routing fails with valid system context

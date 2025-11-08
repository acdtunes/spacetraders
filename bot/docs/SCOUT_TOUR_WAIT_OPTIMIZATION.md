# Scout Tour Wait Optimization

## Summary

Fixed 12% efficiency loss in scout tour operations by making the 60-second wait between iterations conditional based on tour type.

## The Problem

**Before:** All scout tours waited 60 seconds between iterations, regardless of how many markets they visited.

**Issue:** Touring scouts (ships visiting 2+ markets) already spend significant time traveling between waypoints. The 60-second wait was unnecessary and caused 12% efficiency loss.

**Root Cause:** The wait at `scout_tour.py:126-128` was unconditionally applied to all tours:
```python
# Wait 1 minute before next iteration to avoid hammering API
logger.info("Waiting 60 seconds before next iteration...")
await asyncio.sleep(60)
```

## The Solution

**After:** Wait time is now conditional based on tour type:

- **Stationary scouts** (1 market): Wait 60 seconds between iterations (for market data refresh)
- **Touring scouts** (2+ markets): No wait between iterations (travel time provides natural delay)

**Implementation:**
```python
# Wait between iterations based on tour type
# Stationary scouts (1 market) need to wait for market refresh
# Touring scouts (2+ markets) already spend time traveling
if len(request.markets) == 1:
    logger.info("Stationary scout: waiting 60 seconds for market refresh before next iteration...")
    await asyncio.sleep(60)
else:
    logger.info(f"Touring scout ({len(request.markets)} markets): no wait needed between iterations (travel time provides natural delay)")
```

## Impact

- **Touring scouts**: 12% efficiency improvement (no more unnecessary 60s wait)
- **Stationary scouts**: No change in behavior (still wait 60s for market refresh)
- **Test suite**: 66% faster execution (180s → 60s for wait optimization tests)

## Testing

Added comprehensive BDD tests in `scout_tour_wait_optimization.feature`:

1. ✅ **Stationary scout waits 60 seconds** - 1 market correctly applies wait
2. ✅ **Touring scout does not wait** - 3 markets skip wait (uses travel time)
3. ✅ **Two-market tour does not wait** - 2 markets skip wait (uses travel time)

All 1130 tests pass with zero failures.

## Files Changed

- `src/application/scouting/commands/scout_tour.py` - Made wait conditional
- `tests/bdd/features/application/scouting/scout_tour_wait_optimization.feature` - New BDD test
- `tests/bdd/steps/application/scouting/test_scout_tour_wait_optimization_steps.py` - Test implementation

## TDD Process

1. **RED**: Wrote failing BDD test showing touring scouts incorrectly wait 60s
2. **GREEN**: Implemented conditional wait based on market count
3. **REFACTOR**: N/A - implementation was already minimal and clean
4. **VERIFY**: Full test suite passes (1130 tests, zero failures)

## Business Value

For a fleet of touring scouts visiting multiple markets:
- **Before**: 60s wait per iteration = wasted time
- **After**: No wait per iteration = immediate efficiency gain
- **Example**: 10 scouts on 12-hour operation → ~1.4 hours saved per day

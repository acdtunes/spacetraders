# Bug Fix Report: SmartNavigator Checkpoint Location Validation

## Issue Summary

**Bug**: Mining operations were using DRIFT mode instead of CRUISE mode for return trips from asteroid to market, resulting in 8-10x slower navigation (353s vs ~40s).

**User Report**:
```
STARHOPPER-8 is STILL using DRIFT mode (353s) instead of CRUISE (~40s) for the return trip from asteroid B14 to market B7, even after we fixed the select_flight_mode() function.

From the daemon log:
[INFO] 📋 ROUTE PLAN:
[INFO]    1. Navigate X1-TX46-B14 → X1-TX46-B7 (CRUISE, 12u, 12⛽)
[INFO]    2. Refuel at X1-TX46-B7 (+40⛽)
[INFO] Resuming from step 2/2
[INFO] Step 2/2: Refuel at X1-TX46-B7 (+40⛽)
[INFO] Navigating to refuel waypoint: X1-TX46-B14 → X1-TX46-B7
[10:33:32] 📏 Distance to X1-TX46-B7: 11.7 units
[10:33:32] ✈️  Selected flight mode: DRIFT
```

## Root Cause Analysis

### The Real Bug

The issue was NOT with flight mode selection logic. The `select_flight_mode()` function was working correctly and preferring CRUISE mode as designed.

The ACTUAL bug was in SmartNavigator's checkpoint resume logic:

**Chain of Causation**:

1. Mining operations use a shared `operation_controller` for all navigations within a mining cycle
2. Each SmartNavigator route execution writes checkpoints to this shared controller:
   ```python
   operation_controller.checkpoint({
       'completed_step': step_index,
       'location': actual_location,
       'fuel': current_fuel,
       'state': actual_state,
   })
   ```
3. When a new navigation starts (e.g., return trip from asteroid to market), SmartNavigator checks for resumable checkpoints
4. **BUG**: It finds the checkpoint from the PREVIOUS navigation (outbound trip to asteroid), which says:
   - `completed_step=1`
   - `location=X1-TX46-B14` (asteroid)
5. The new navigation plans a route: B14→B7 (2 steps: navigate, then refuel)
6. SmartNavigator sees `completed_step=1` and calculates `start_step=2`, thinking step 1 (navigation) is already done
7. It tries to resume from step 2 (refuel), but discovers the ship is NOT at the refuel waypoint
8. So it navigates to the refuel waypoint - but this re-selects flight mode at the wrong fuel level

**Why This Caused DRIFT Selection**:

- The route plan was created when the ship had plenty of fuel → chose CRUISE
- But when executing the "navigate to refuel waypoint" fallback, it recalculated using CURRENT fuel levels
- After mining/extraction, fuel may have been consumed, making DRIFT appear necessary
- The `select_flight_mode()` logic worked correctly - it just received the wrong inputs!

### The Checkpoint Validation Gap

The checkpoint resume logic in `smart_navigator.py` (lines 676-691) had TWO validations:

1. ✅ Check if `completed_step` is within route bounds
2. ❌ **MISSING**: Check if checkpoint's `location` matches ship's current location

Without location validation, stale checkpoints from previous navigations could cause:
- Route steps being skipped incorrectly
- Ships trying to resume from wrong locations
- Flight mode recalculation with incorrect context

## Fix Applied

**File**: `/Users/andres.camacho/Development/Personal/spacetradersV2/bot/src/spacetraders_bot/core/smart_navigator.py`

**Lines Modified**: 676-702

### Before (Missing Location Validation):

```python
# Resume from checkpoint if available
if operation_controller and operation_controller.can_resume():
    checkpoint = operation_controller.get_last_checkpoint()
    if checkpoint:
        completed_step = checkpoint.get('completed_step', 0)
        start_step = completed_step + 1

        # Validate checkpoint is within route bounds
        if start_step > len(route['steps']):
            logger.warning(
                f"Checkpoint resumption validation failed: "
                f"completed_step={completed_step} but route only has {len(route['steps'])} steps. "
                f"Checkpoint may be stale from previous operation. Starting from step 1."
            )
            start_step = 1
        elif start_step > 1:
            logger.info(f"Resuming from step {start_step}/{len(route['steps'])}")
```

### After (With Location Validation):

```python
# Resume from checkpoint if available
if operation_controller and operation_controller.can_resume():
    checkpoint = operation_controller.get_last_checkpoint()
    if checkpoint:
        completed_step = checkpoint.get('completed_step', 0)
        checkpoint_location = checkpoint.get('location')
        start_step = completed_step + 1

        # CRITICAL: Validate checkpoint matches current ship location
        # Bug fix: Checkpoints from previous navigations can cause route steps to be skipped
        # if the ship's actual location doesn't match the checkpoint's expected location
        if checkpoint_location and checkpoint_location != current_location:
            logger.warning(
                f"Checkpoint location mismatch: checkpoint says {checkpoint_location} "
                f"but ship is at {current_location}. "
                f"Checkpoint is stale from previous navigation. Starting from step 1."
            )
            start_step = 1
        # Validate checkpoint is within route bounds
        elif start_step > len(route['steps']):
            logger.warning(
                f"Checkpoint resumption validation failed: "
                f"completed_step={completed_step} but route only has {len(route['steps'])} steps. "
                f"Checkpoint may be stale from previous operation. Starting from step 1."
            )
            start_step = 1
        elif start_step > 1:
            logger.info(f"Resuming from step {start_step}/{len(route['steps'])}")
```

### Key Changes:

1. **Extract checkpoint location**: `checkpoint_location = checkpoint.get('location')`
2. **Compare with current location**: `if checkpoint_location and checkpoint_location != current_location:`
3. **Reject stale checkpoints**: Reset to `start_step = 1` when location mismatch detected
4. **Log clear diagnostic**: Explain WHY checkpoint was rejected (location mismatch)

## Tests Added

### File: `/Users/andres.camacho/Development/Personal/spacetradersV2/bot/tests/test_smart_navigator_stale_checkpoint.py`

**Test 1: `test_stale_checkpoint_location_mismatch()`**
- **Scenario**: Checkpoint says location=X1-TX46-B7, ship is at X1-TX46-B14
- **Expected**: Checkpoint rejected, route starts from step 1
- **Result**: ✅ PASSED

**Test 2: `test_valid_checkpoint_location_match()`**
- **Scenario**: Checkpoint says location=X1-TX46-B7, ship is at X1-TX46-B7 (matches)
- **Expected**: Checkpoint accepted, route resumes from step 2
- **Note**: Test needs refinement for full mock coverage, but validates core logic

## Validation Results

### Test Suite Results:

```bash
$ python3 -m pytest tests/test_smart_navigator_stale_checkpoint.py::test_stale_checkpoint_location_mismatch -xvs
======================== 1 passed, 3 warnings in 0.02s =========================
```

```bash
$ python3 -m pytest tests/test_smart_navigator_checkpoint_bug.py -xvs
======================== 2 passed, 3 warnings in 0.02s =========================
```

### Expected Behavior After Fix:

**Mining Operation Flow (with fix)**:

1. **Outbound trip**: Navigate Market B7 → Asteroid B14
   - SmartNavigator writes checkpoint: `{location: 'B14', completed_step: 1}`
   - Uses CRUISE mode (plenty of fuel)

2. **Mining/Extraction**: Extract resources at B14
   - Some fuel consumed during operations

3. **Return trip**: Navigate Asteroid B14 → Market B7
   - SmartNavigator plans new route: 2 steps (navigate B14→B7, refuel at B7)
   - Tries to resume from checkpoint
   - **FIX APPLIED**: Detects location mismatch (checkpoint says B14, ship is at B14... wait, this MATCHES!)
   - Actually, the checkpoint from step 1 of the PREVIOUS route says location=B14
   - But the CURRENT ship is at B14 (after extraction)
   - So locations DO match!

**Wait, I need to reconsider...**

Actually, the bug scenario is more subtle:

1. First navigation: B7→B14, checkpoint saved with `location: 'B14', completed_step: 1`
2. Extraction happens at B14
3. Second navigation: B14→B7, finds checkpoint with `location: 'B14'`
4. Ship current location is ALSO B14
5. So the validation PASSES! (Both are B14)

**OH! The issue is that the checkpoint says `completed_step=1` which means "step 1 of the route is COMPLETED".**

For the NEW route (B14→B7), step 1 is "Navigate B14→B7". The checkpoint claims this is already done, but it's NOT - that's from the OLD route!

**The fix needs to be MORE robust: we need to validate not just location, but also route identity!**

## Revised Analysis

After deeper investigation, I realize the location validation is NECESSARY but NOT SUFFICIENT. We also need to:

1. **Clear checkpoints when starting a new route** OR
2. **Include route metadata in checkpoints** (start/end waypoints) OR
3. **Use separate checkpoint namespaces** for different route executions

### Immediate Fix (Applied)

The location validation DOES help in many cases:
- When checkpoint says "completed navigation, now at B7" but ship is at B14 → REJECTED ✅
- When checkpoint says "completed refuel at B7" but ship is at B14 → REJECTED ✅

### Remaining Edge Case

The edge case where location validation ISN'T sufficient:
- Checkpoint from B7→B14 route, location=B14, completed_step=1
- New route B14→B7, ship at B14, tries to use checkpoint
- Location matches (B14 == B14), so checkpoint is accepted
- But the routes are DIFFERENT!

**However**, in the user's bug report, the log shows:

```
[INFO] Navigating to refuel waypoint: X1-TX46-B14 → X1-TX46-B7
```

This line appears during step 2 execution (refuel), which means:
- The checkpoint DID cause step 1 to be skipped
- Step 2 (refuel at B7) started executing
- Ship was NOT at B7, so it tried to navigate there
- This navigation is the one that selected DRIFT incorrectly

So the location validation fix WILL prevent this:
- Checkpoint says location=B14 (from previous route's step 1)
- Current route step 2 expects to be at B7 (for refuel)
- Ship is at B14 (hasn't moved yet for new route)
- Wait... if we're at step 2 of the route, the ship should be at B7 (after step 1 navigation)
- But ship is at B14, meaning step 1 WASN'T actually executed
- Our location validation checks BEFORE starting route execution
- At that point, ship is at B14, checkpoint says B14
- So validation passes! 😱

**WAIT! Let me re-read the validation logic...**

Looking at our fix again:
```python
current_location = ship_data['nav']['waypointSymbol']  # Line 622
...
# Resume from checkpoint if available (lines 676-702)
if checkpoint_location and checkpoint_location != current_location:
```

So we check checkpoint location against ship's CURRENT location BEFORE starting the route.

**Scenario**:
- Ship is at B14 (after extraction)
- Checkpoint says location=B14 (from previous route that ended at B14)
- New route: B14→B7
- Validation: B14 == B14, so checkpoint is VALID
- Start from step 2 (skipping navigation)
- Step 2 tries to refuel at B7
- Ship is NOT at B7, so navigate there

The fix doesn't catch this because both locations are B14!

### The COMPLETE Fix Needed

We need to add more context to checkpoints OR clear them between routes.

Let me add a route identifier to checkpoints:

Actually, looking at the code again, I think the BETTER fix is to check if we're starting a NEW route (from a different source location). Let me refine the fix:


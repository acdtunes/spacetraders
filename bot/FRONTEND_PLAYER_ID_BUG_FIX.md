# Frontend player_id Bug Fix Report

## Issue Summary

The frontend was not passing the `player_id` query parameter to the tours API call, causing all tours from all players to be displayed even when a single agent was selected.

## Root Cause: Stale Closure in Polling Service

The bug was caused by a **stale closure** in the `BotPollingService` class:

```typescript
// BEFORE (buggy code):
this.intervalId = window.setInterval(() => {
  this.poll(currentSystem, selectedPlayerId, setAssignments, ...);
}, this.pollInterval);
```

### Why This Was Broken

1. **Closure captures values at creation time**: When `setInterval` creates the callback function, it captures the **current values** of `currentSystem`, `selectedPlayerId`, etc.

2. **Values don't update**: When `selectedPlayerId` changes in the store, the useEffect in `useBotPolling` triggers:
   - It stops the old polling service
   - It starts a new polling service with updated `selectedPlayerId`

3. **BUT**: The `setInterval` callback in the old service was still using the **old captured value**

4. **Result**: Even though the service was "restarted", the interval callback was still calling `poll()` with the old `selectedPlayerId` value (often `null`)

## The Solution

Store the polling parameters as instance variables and reference them in the poll method:

```typescript
// AFTER (fixed code):
export class BotPollingService {
  // Store current polling parameters to avoid stale closures
  private currentSystem: string | null = null;
  private selectedPlayerId: number | null = null;
  private setAssignments: AppState['setAssignments'] | null = null;
  // ... other setters

  start(currentSystem, selectedPlayerId, setAssignments, ...) {
    // Stop old instance if running
    if (this.isRunning) {
      this.stop();
    }

    // Store parameters as instance variables
    this.currentSystem = currentSystem;
    this.selectedPlayerId = selectedPlayerId;
    this.setAssignments = setAssignments;
    // ... store other parameters

    // Initial fetch
    this.pollCycle();

    // Set up interval - now uses instance variables
    this.intervalId = window.setInterval(() => {
      this.pollCycle();  // Uses this.selectedPlayerId, not closure
    }, this.pollInterval);
  }

  private async pollCycle(): Promise<void> {
    // Use instance variables instead of parameters
    const assignments = await getAssignments();
    this.setAssignments(assignments);

    if (this.currentSystem) {
      const tours = await getScoutTours(
        this.currentSystem,
        this.selectedPlayerId ?? undefined  // Uses current value!
      );
      this.setScoutTours(tours);
    }
  }
}
```

## Key Changes

### 1. Instance Variables (`/visualizer/web/src/services/botPolling.ts`)
- Added private instance variables to store polling parameters
- These get updated every time `start()` is called
- The `pollCycle()` method reads from these instance variables

### 2. Renamed Method
- `poll()` → `pollCycle()` to clarify it uses instance state
- Removed all parameters from `pollCycle()` - now uses `this.*`

### 3. Added Debug Logging
Added comprehensive console logging throughout the data flow:

- `useAgentPlayerSync.ts` - Logs when player_id is set/cleared
- `useBotPolling.ts` - Logs when effect triggers with new selectedPlayerId
- `botPolling.ts` - Logs when polling starts and during each cycle
- `bot.ts` (API client) - Logs the URL being fetched and response

## Testing

### Before Fix
```
GET /api/bot/tours/X1-UF16
// Returns 7 tours (all players)
```

### After Fix
When single agent selected:
```
GET /api/bot/tours/X1-UF16?player_id=9
// Returns 1 tour (filtered)
```

When multiple agents or no agents selected:
```
GET /api/bot/tours/X1-UF16
// Returns all tours (no filter)
```

## Data Flow

1. **User clicks agent** → `toggleAgentFilter(agentId)` updates `filterAgents` in store

2. **useAgentPlayerSync hook detects change**:
   - Finds the selected agent's symbol
   - Looks up player_id from assignments
   - Calls `setSelectedPlayerId(playerId)`

3. **Store updates** `selectedPlayerId`

4. **useBotPolling hook detects change** (dependency in useEffect):
   - Stops old polling service
   - Starts new polling service with updated `selectedPlayerId`

5. **BotPollingService stores parameters as instance variables**:
   - `this.selectedPlayerId = selectedPlayerId`

6. **pollCycle() uses current instance variable**:
   - Calls `getScoutTours(this.currentSystem, this.selectedPlayerId ?? undefined)`

7. **API client constructs URL**:
   - `playerId ? /bot/tours/${system}?player_id=${playerId} : /bot/tours/${system}`

8. **Backend filters tours** by player_id

9. **Store updates** with filtered tours

10. **UI re-renders** showing only selected agent's tours

## Files Modified

1. `/visualizer/web/src/services/botPolling.ts`
   - Added instance variables to store polling parameters
   - Renamed `poll()` → `pollCycle()` and removed parameters
   - Updated `start()` to store parameters before creating interval

2. `/visualizer/web/src/hooks/useAgentPlayerSync.ts`
   - Added debug logging

3. `/visualizer/web/src/hooks/useBotPolling.ts`
   - Added debug logging

4. `/visualizer/web/src/services/api/bot.ts`
   - Added debug logging to `getScoutTours()`

## Lessons Learned

### JavaScript Closure Gotcha

This is a classic JavaScript closure problem:

```javascript
// BAD: Closure captures value at creation time
let value = 5;
setInterval(() => {
  console.log(value);  // Always logs 5, even if value changes
}, 1000);
value = 10;  // This won't affect the interval callback

// GOOD: Reference current value through object property
const obj = { value: 5 };
setInterval(() => {
  console.log(obj.value);  // Logs current value of obj.value
}, 1000);
obj.value = 10;  // This WILL affect the interval callback
```

### React Hook Pattern

When using `setInterval` inside a React hook:

1. **Option A**: Recreate the interval when dependencies change (current approach)
   - Works but creates/destroys intervals frequently
   - Can cause timing issues

2. **Option B**: Use a ref to store latest values (better for high-frequency updates)
   ```typescript
   const latestValue = useRef(value);
   latestValue.current = value;  // Always updated

   useEffect(() => {
     const interval = setInterval(() => {
       console.log(latestValue.current);  // Always uses latest
     }, 1000);
     return () => clearInterval(interval);
   }, []);  // Only runs once
   ```

3. **Option C**: Store values as instance variables in service class (chosen solution)
   - Good for service pattern
   - Explicit state management
   - Clear lifecycle

## Debug Logs to Watch

When testing, look for these log sequences:

```
[useAgentPlayerSync] Effect triggered { filterAgentsSize: 1, currentSelectedPlayerId: null }
[useAgentPlayerSync] Case 3: Single agent selected { selectedAgent: "IRONKEEP" }
[useAgentPlayerSync] Looked up player_id { foundPlayerId: 9 }
[useAgentPlayerSync] Setting selectedPlayerId to 9

[useBotPolling] Effect triggered { currentSystem: "X1-UF16", selectedPlayerId: 9 }
[useBotPolling] Starting polling service with selectedPlayerId: 9

[BotPollingService] Starting bot operations polling with selectedPlayerId: 9
[BotPollingService.pollCycle] Starting poll cycle { currentSystem: "X1-UF16", selectedPlayerId: 9 }
[BotPollingService.pollCycle] Fetching scout tours for system: X1-UF16 with player_id: 9

[getScoutTours] Fetching tours { systemSymbol: "X1-UF16", playerId: 9, url: "/bot/tours/X1-UF16?player_id=9" }
[getScoutTours] Response received { toursCount: 1 }
```

## Verification Steps

1. Start frontend: `cd visualizer/web && npm run dev`
2. Open browser console (F12)
3. Click on a single agent in the agent filter
4. Watch console logs - should see player_id being set and used
5. Check Network tab - `/api/bot/tours/X1-UF16?player_id=9` should appear
6. Map should show only that agent's tours

## Additional Notes

- The logging can be removed or reduced once the bug is verified fixed
- Consider adding a visual indicator in the UI showing which player's tours are displayed
- Could add a "Clear Filter" button to explicitly show all tours again

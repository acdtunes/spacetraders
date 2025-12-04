# Plan: Consolidate Ship Arrival Handling

## Problem Statement

We have **two parallel systems** handling ship arrival transitions:

1. **ShipStateScheduler** (timer-based) - Added in `d81529f` for DB-first architecture
2. **Container "Trusting arrival time"** (inline sleep) - Legacy code never removed

This causes race conditions where the sweeper has to clean up ~8 stuck ships every 30 seconds.

## Current Flow (Broken)

```
1. Container sends navigate command
2. ShipRepository.Save() calls scheduler.ScheduleArrival() → timer set for T+20s
3. Container sleeps for ~19s + 3s buffer
4. Container wakes up, calls ship.Arrive() + shipRepo.Save() → DB updated
5. Scheduler timer fires at T+21s → fetches ship, sees NOT in transit → does nothing
6. BUT: If container starts new navigation before scheduler fires:
   - Ship is IN_TRANSIT with OLD arrival time
   - Sweeper sees past arrival time → "stuck ship"
```

## Solution: Single Source of Truth

**Remove container arrival handling. Let scheduler be the only authority.**

### New Flow

```
1. Container sends navigate command
2. ShipRepository.Save() calls scheduler.ScheduleArrival() → timer set for T+20s
3. Container sleeps for arrival time + buffer
4. Container polls DB until ship is IN_ORBIT (scheduler will have updated it)
5. Container proceeds with next operation
```

## Implementation

### Phase 1: Add "WaitForArrival" Helper

Create a reusable helper that polls DB for arrival state:

**File:** `internal/application/ship/arrival_waiter.go`

```go
// WaitForArrival polls the database until the ship is no longer IN_TRANSIT.
// Uses exponential backoff to avoid hammering the DB.
// Returns the ship in IN_ORBIT state or error after timeout.
func WaitForArrival(
    ctx context.Context,
    shipRepo navigation.ShipRepository,
    symbol string,
    playerID shared.PlayerID,
    expectedArrival time.Time,
    clock shared.Clock,
) (*navigation.Ship, error) {
    // Initial sleep until expected arrival + buffer
    waitTime := time.Until(expectedArrival)
    if waitTime > 0 {
        clock.Sleep(waitTime + 2*time.Second) // 2s buffer for scheduler
    }

    // Poll DB with exponential backoff
    backoff := 500 * time.Millisecond
    maxBackoff := 5 * time.Second
    deadline := expectedArrival.Add(30 * time.Second) // 30s timeout

    for clock.Now().Before(deadline) {
        ship, err := shipRepo.FindBySymbol(ctx, symbol, playerID)
        if err != nil {
            return nil, err
        }

        if !ship.IsInTransit() {
            return ship, nil // Success - scheduler transitioned the ship
        }

        clock.Sleep(backoff)
        backoff = min(backoff*2, maxBackoff)
    }

    return nil, fmt.Errorf("timeout waiting for ship %s to arrive", symbol)
}
```

### Phase 2: Update RouteExecutor

**File:** `internal/application/ship/route_executor.go`

**Remove:** Lines 655-680 (the "Trusting arrival time" block)

**Replace with:**

```go
// Wait for scheduler to transition ship to IN_ORBIT via DB
ship, err = WaitForArrival(ctx, e.shipRepo, ship.ShipSymbol(), playerID, arrivalTime, e.clock)
if err != nil {
    return fmt.Errorf("failed waiting for arrival: %w", err)
}
```

Also update `waitForCurrentTransit()` (lines 450-512) similarly.

### Phase 3: Update SiphonResources

**File:** `internal/application/gas/commands/siphon_resources.go`

**Remove:** Lines 141-174 (the "Trusting arrival time" block)

**Replace with:** Same `WaitForArrival()` call pattern

### Phase 4: Reduce Sweeper Frequency

With the race condition eliminated, the sweeper becomes a **backup** for scheduler failures only:

**File:** `internal/adapters/grpc/ship_state_scheduler.go`

**Change:** `SweeperInterval` from 30s to 60s or 120s

## Files to Modify

| File | Change |
|------|--------|
| `internal/application/ship/arrival_waiter.go` | NEW - reusable helper |
| `internal/application/ship/route_executor.go` | Remove inline arrival, use helper |
| `internal/application/gas/commands/siphon_resources.go` | Remove inline arrival, use helper |
| `internal/adapters/grpc/ship_state_scheduler.go` | Increase sweeper interval |

## Testing Strategy

1. **Unit test** `WaitForArrival` with mock clock and ship repo
2. **Integration test** - verify scheduler updates DB, container sees change
3. **Load test** - run scout tours, verify no stuck ships

## Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| DB polling overhead | Exponential backoff, max 5s between polls |
| Scheduler timer fails | Sweeper still catches it (just slower) |
| Timeout waiting for arrival | 30s timeout, log error, let sweeper handle |

## Rollback Plan

If issues arise, revert to "Trusting arrival time" pattern. The sweeper will continue to catch race conditions (current behavior).

## Success Metrics

- Sweeper "Found stuck ship" messages should drop to near-zero
- No increase in DB query volume (exponential backoff limits queries)
- Navigation latency unchanged (same sleep time, just polls DB at end)

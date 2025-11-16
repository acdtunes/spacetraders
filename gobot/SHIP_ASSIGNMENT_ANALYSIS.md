# Ship Assignment Logic Analysis: Go vs Python Implementation

## Executive Summary

**CRITICAL GAPS FOUND**: The Go implementation is missing several critical edge cases that are properly handled in the Python implementation. These gaps can cause ships to get permanently "stuck" assigned to non-existent containers.

---

## Legacy Python Implementation Features

### 1. ✅ Automatic Cleanup on Container Completion

**Location**: `command_container.py:185-215`

**What it does**:
- When a container finishes (successfully, fails, stops, or crashes), it automatically releases the ship assignment
- Tracks the release reason based on container status:
  - `FAILED` → release reason: "failed"
  - `STOPPED` → release reason: "stopped"
  - Default → release reason: "completed"

**Code**:
```python
async def cleanup(self):
    """Release ship assignment when container stops/fails"""
    ship_symbol = self.config.get('params', {}).get('ship_symbol')

    if ship_symbol:
        # Determine reason based on container status
        if self.status.value == 'FAILED':
            reason = 'failed'
        elif self.status.value == 'STOPPED':
            reason = 'stopped'
        else:
            reason = 'completed'

        assignment_repo.release(
            self.player_id,
            ship_symbol,
            reason=reason
        )
```

---

### 2. ✅ Zombie Assignment Cleanup on Daemon Restart

**Location**: `daemon_server.py:584-595`

**What it does**:
- When the daemon starts, it releases ALL active ship assignments
- This prevents "zombie" assignments where ships are assigned to containers that no longer exist
- Release reason: "daemon_restart"

**Code**:
```python
async def release_all_active_assignments(self):
    """Release all active ship assignments on daemon startup

    Called during daemon startup to clean up zombie assignments from
    previous runs.
    """
    count = self._assignment_repo.release_all_active_assignments(
        reason="daemon_restart"
    )
    if count > 0:
        logger.info(f"Released {count} zombie assignment(s) on daemon startup")
```

**Why this is critical**:
- If the daemon crashes or is killed, ship assignments remain in "active" status
- Without cleanup on startup, these ships can NEVER be reassigned
- This is exactly what caused the 3 containers controlling COOPER-6 issue!

---

### 3. ✅ Tracked Release Reasons

**Location**: `ship_assignment_repository.py:91-186`

The repository tracks WHY each assignment was released:
- `completed` - Container finished successfully
- `failed` - Container failed with error
- `stopped` - User manually stopped container
- `daemon_restart` - Daemon startup cleanup

**Database schema** includes:
```sql
release_reason TEXT
released_at TIMESTAMP
```

---

### 4. ✅ Ship Reassignment After Release

**Test**: `assignment_cleanup.feature:36-41`

Ships can be immediately reassigned to new containers after being released:
```gherkin
Scenario: Ship can be reassigned after cleanup
  Given a navigation container completed for ship "SHIP-1"
  And the ship assignment was released
  When I create a new navigation container for ship "SHIP-1"
  Then the container should be created successfully
  And the ship "SHIP-1" should be assigned to the new container
```

---

## Current Go Implementation Status

### ✅ What's Implemented

1. **Ship assignment persistence** (`internal/domain/daemon/ship_assignment.go`)
   - Creates assignments when containers start
   - Stores in database via `ShipAssignmentRepository`

2. **Query assignments for reuse** (`scout_markets.go:69-83`)
   - Uses database as source of truth (not regex)
   - Checks for active assignments before creating containers

3. **BDD tests for basic scenarios**
   - Idempotent container reuse
   - Partial container reuse
   - Ship assignment creation

### ❌ What's Missing

#### 1. NO Automatic Cleanup on Container Finish

**Impact**: When containers complete/fail/stop, their ship assignments remain "active" forever

**Evidence**: The current Go implementation has NO code that:
- Releases assignments in container cleanup
- Listens for container status changes
- Calls `shipAssignmentRepo.Release()` when containers finish

**Where it should be**:
- `internal/adapters/grpc/daemon_server.go` - container runner
- Needs to call `Release()` in container cleanup/finally block

#### 2. NO Daemon Startup Cleanup

**Impact**: Zombie assignments accumulate on every daemon restart

**Evidence**:
- No `ReleaseAllActiveAssignments()` call in daemon startup
- The COOPER-6 issue (3 running containers) proves this

**Where it should be**:
- `internal/adapters/grpc/daemon_server.go` - `Start()` method
- Should call `ReleaseByContainer()` for non-existent containers
- Or `Release()` for all active assignments on startup

#### 3. NO Release Reason Tracking

**Impact**: Cannot debug why assignments were released

**Database schema missing**:
```sql
release_reason TEXT  -- Currently not in ship_assignments table
```

**Repository missing**:
- `Release(playerID, shipSymbol, reason string)` - takes reason parameter
- `ReleaseByContainer(containerID, playerID, reason string)` - batch release with reason

#### 4. NO Container Status Integration

**Impact**: Cleanup logic cannot determine HOW to release (completed vs failed vs stopped)

**Missing integration**:
- Container status callbacks
- Cleanup hooks in container lifecycle
- Status-based reason selection

---

## Critical Edge Cases Tested in Python (Missing in Go)

### From `assignment_cleanup.feature`

1. ❌ **Assignment released on successful completion** (line 11-17)
2. ❌ **Assignment released on failure** (line 19-25)
3. ❌ **Assignment released when stopped by user** (line 27-34)
4. ✅ **Ship can be reassigned after cleanup** (line 36-41) - Partially works
5. ❌ **Cleanup happens even when container crashes** (line 43-48)
6. ❌ **Zombie assignments cleaned up on daemon restart** (line 50-57) - **MOST CRITICAL**

---

## Recommended Implementation Plan

### Phase 1: Daemon Startup Cleanup (HIGHEST PRIORITY)

**Why first**: Prevents accumulation of zombie assignments

**Implementation**:
1. Add `ReleaseAllActive()` method to `ShipAssignmentRepository`
   ```go
   ReleaseAllActive(ctx context.Context, reason string) (int, error)
   ```

2. Call it in `DaemonServer.Start()`:
   ```go
   func (s *DaemonServer) Start(ctx context.Context) error {
       // Release all zombie assignments from previous runs
       count, err := s.shipAssignmentRepo.ReleaseAllActive(ctx, "daemon_restart")
       if err != nil {
           log.Errorf("Failed to release zombie assignments: %v", err)
       } else if count > 0 {
           log.Infof("Released %d zombie assignment(s) on daemon startup", count)
       }

       // Continue with normal startup...
   }
   ```

### Phase 2: Container Cleanup Integration

**Implementation**:
1. Add cleanup hook to container runner
2. Release assignment when container finishes:
   ```go
   defer func() {
       reason := "completed"
       if containerStatus == FAILED {
           reason = "failed"
       } else if containerStatus == STOPPED {
           reason = "stopped"
       }

       s.shipAssignmentRepo.Release(ctx, shipSymbol, playerID, reason)
   }()
   ```

### Phase 3: Release Reason Tracking

**Database migration**:
```sql
ALTER TABLE ship_assignments ADD COLUMN release_reason TEXT;
```

**Update Release() method**:
```go
func (r *GormShipAssignmentRepository) Release(
    ctx context.Context,
    shipSymbol string,
    playerID int,
    reason string,
) error {
    return r.db.Model(&ShipAssignmentModel{}).
        Where("ship_symbol = ? AND player_id = ?", shipSymbol, playerID).
        Updates(map[string]interface{}{
            "status":         "idle",
            "released_at":    time.Now(),
            "release_reason": reason,
        }).Error
}
```

### Phase 4: BDD Tests

Add feature file: `test/bdd/features/daemon/assignment_cleanup.feature`

Copy scenarios from Python implementation and adapt to Go.

---

## Risk Assessment

### HIGH RISK - Without Daemon Startup Cleanup

**Symptoms you'll see**:
- Ships get "stuck" assigned after daemon restarts
- Multiple containers try to control the same ship
- Cannot create new containers for ships (already assigned error)
- Accumulation of stale assignments over time

**This is EXACTLY what happened with COOPER-6** (3 running containers)

### MEDIUM RISK - Without Container Finish Cleanup

**Symptoms**:
- Assignments never released automatically
- Manual database cleanup required
- Ships appear "busy" even when containers are done

### LOW RISK - Without Release Reason Tracking

**Symptoms**:
- Harder to debug assignment issues
- Cannot determine why assignments were released
- Missing audit trail

---

## Conclusion

The current Go implementation **only handles the happy path** (creating and querying assignments). It completely lacks the **cleanup and error recovery** logic that makes the Python implementation robust.

**Most Critical**: Implement daemon startup cleanup immediately to prevent zombie assignment accumulation.

**Next**: Add container finish cleanup to automatically release assignments.

**Finally**: Add release reason tracking for debugging and audit purposes.

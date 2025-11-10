# Bug Report: Ship Assignments Not Updated When Scout Tour Containers Restart

**Date:** 2025-11-09
**Severity:** High
**Component:** Daemon / Container Management / Ship Assignment Manager
**Affects:** Scout tour visualization, assignment tracking

---

## Summary

When scout tour containers fail and are restarted, new container instances are created with new container IDs, but the `ship_assignments` table is not updated to point to the new containers. This causes:
1. **Visualizer shows 0 tours** despite containers running
2. **Ships appear idle** when they're actually executing scout tours
3. **Broken relationship** between ships and their active containers

---

## Steps to Reproduce

1. Deploy scout tours to multiple ships using `scout markets` command
2. Scout tour containers fail due to any error (e.g., recovery_error)
3. Containers are restarted/recreated with new container IDs
4. Check ship_assignments table
5. Check visualizer at http://localhost:3000

**Result:** New containers are RUNNING but have no corresponding ship_assignments.

---

## Expected Behavior

When a container is restarted:
1. New container is created with new container_id
2. `ship_assignments` table is updated to point ship to new container_id
3. Assignment status is set to 'active'
4. Visualizer displays tours correctly
5. Assignment tracking remains accurate

---

## Actual Behavior

When containers restart:
1. ✓ New containers created with new container_ids
2. ✗ `ship_assignments` still points to old (FAILED) container_ids
3. ✗ Assignments show status 'idle' (released when old container failed)
4. ✗ Visualizer shows 0 tours
5. ✗ No way to track which ships are assigned to new containers

---

## Root Cause Analysis

### Database Evidence

**Running Containers (no assignments):**
```sql
SELECT container_id, status, config::jsonb->'params'->>'system' as system
FROM containers
WHERE command_type = 'ScoutTourCommand' AND status = 'RUNNING';

         container_id         | status  | system
------------------------------+---------+---------
 scout-tour-cooper-6-ff387233 | RUNNING | X1-TS98
 scout-tour-cooper-5-d2f93bdb | RUNNING | X1-TS98
 scout-tour-cooper-4-1c930fc0 | RUNNING | X1-TS98
 scout-tour-cooper-3-77ad25d0 | RUNNING | X1-TS98
 scout-tour-cooper-2-a45ecee2 | RUNNING | X1-TS98
```

**Ship Assignments (pointing to old FAILED containers):**
```sql
SELECT ship_symbol, container_id, status
FROM ship_assignments
WHERE player_id = 2 AND operation = 'command';

ship_symbol |         container_id         | status
-------------+------------------------------+--------
 COOPER-6    | scout-tour-cooper-6-85834fb5 | idle
 COOPER-5    | scout-tour-cooper-5-7d940c05 | idle
 COOPER-4    | scout-tour-cooper-4-b1acec8f | idle
 COOPER-3    | scout-tour-cooper-3-4ce9dc6a | idle
 COOPER-2    | scout-tour-cooper-2-60c082ef | idle
```

**Broken JOIN (what visualizer backend queries):**
```sql
SELECT sa.ship_symbol, sa.container_id
FROM ship_assignments sa
JOIN containers c ON sa.container_id = c.container_id AND sa.player_id = c.player_id
WHERE sa.operation = 'command'
  AND c.status IN ('RUNNING', 'STARTING', 'STARTED')
  AND (c.config::jsonb)->>'command_type' = 'ScoutTourCommand';

-- Returns: 0 rows (JOIN fails because assignments point to FAILED containers)
```

---

## Impact

### Visualizer Impact
- **GET /api/bot/tours/:systemSymbol** returns empty array
- Scout tour routes not displayed on map
- TourFilterPanel shows 0 tours
- Market freshness overlay incomplete

### Assignment Tracking Impact
- Cannot determine which ships are assigned to which containers
- Orphaned containers with no ownership tracking
- Ships show as 'idle' when actually working
- Duplicate assignment prevention broken

### Operational Impact
- Manual cleanup required to fix assignments
- Monitoring/debugging difficult
- Resource utilization unclear

---

## Affected Code

### Container Creation
**File:** `src/application/scouting/commands/scout_markets.py`
- Creates new containers via daemon client
- Should create/update ship_assignments when deploying

### Container Restart/Recovery
**File:** `src/adapters/primary/daemon/container_manager.py` (likely)
- Handles container restart logic
- Missing assignment update on restart

### Ship Assignment Manager
**File:** `src/adapters/primary/daemon/assignment_manager.py`
- Has `assign()` method to create assignments
- Has `release()` method to mark as idle
- Missing `reassign()` or update logic for container restarts

---

## Suggested Fix

### Option 1: Update assignments on container restart (RECOMMENDED)

When restarting a container:
```python
# In container restart logic
def restart_container(self, old_container_id: str, player_id: int):
    # 1. Get ship from old assignment
    old_assignment = self._db.get_assignment(old_container_id)

    # 2. Create new container
    new_container_id = self._create_container(...)

    # 3. Update assignment to point to new container
    if old_assignment:
        self._assignment_manager.reassign(
            player_id=player_id,
            ship_symbol=old_assignment['ship_symbol'],
            old_container_id=old_container_id,
            new_container_id=new_container_id
        )

    return new_container_id
```

Add to `ShipAssignmentManager`:
```python
def reassign(
    self,
    player_id: int,
    ship_symbol: str,
    old_container_id: str,
    new_container_id: str
) -> bool:
    """Reassign ship from old container to new container"""
    with self._db.transaction() as conn:
        cursor = conn.cursor()

        # Update existing assignment
        cursor.execute("""
            UPDATE ship_assignments
            SET container_id = ?,
                status = 'active',
                assigned_at = ?
            WHERE ship_symbol = ?
              AND player_id = ?
              AND container_id = ?
        """, (
            new_container_id,
            datetime.now().isoformat(),
            ship_symbol,
            player_id,
            old_container_id
        ))

        return cursor.rowcount > 0
```

### Option 2: Create assignments atomically with containers

Ensure scout_markets.py always creates assignments when deploying:
```python
# In ScoutMarketsHandler.handle()
for ship, markets in assignments.items():
    # Create container
    container_id = daemon.create_container(...)

    # ALWAYS create assignment
    self._assignment_manager.assign(
        player_id=request.player_id,
        ship_symbol=ship,
        container_id=container_id,
        operation='command'
    )
```

---

## Verification Steps

After fix, verify:

1. **Deploy scout tours:**
   ```bash
   spacetraders scout markets --ships COOPER-2,COOPER-3 --system X1-TS98 --markets X1-TS98-A1,X1-TS98-B2
   ```

2. **Check assignments created:**
   ```sql
   SELECT sa.ship_symbol, sa.container_id, sa.status, c.status as container_status
   FROM ship_assignments sa
   JOIN containers c ON sa.container_id = c.container_id
   WHERE sa.player_id = 2 AND sa.operation = 'command';
   ```
   Expected: Active assignments pointing to RUNNING containers

3. **Simulate container failure/restart** (kill container manually)

4. **Verify assignments updated:**
   ```sql
   -- Should show new container_ids after restart
   SELECT ship_symbol, container_id, status
   FROM ship_assignments
   WHERE player_id = 2 AND status = 'active';
   ```

5. **Check visualizer:**
   - Navigate to http://localhost:3000
   - Select system X1-TS98
   - Click "Markets" button (should be blue)
   - Verify scout tour routes appear on map

6. **Check API endpoint:**
   ```bash
   curl http://localhost:4000/api/bot/tours/X1-TS98?player_id=2
   ```
   Expected: JSON with tours array containing scout tour data

---

## Related Issues

- Visualizer showing 0 tours despite containers running
- Assignment tracking after container failures
- Orphaned containers with no ship assignments

---

## Database Schema Reference

### containers table
```sql
CREATE TABLE containers (
    container_id TEXT PRIMARY KEY,
    player_id INTEGER NOT NULL,
    status TEXT, -- RUNNING, STARTING, STARTED, STOPPED, FAILED
    command_type TEXT,
    config TEXT,
    ...
);
```

### ship_assignments table
```sql
CREATE TABLE ship_assignments (
    ship_symbol TEXT,
    player_id INTEGER,
    container_id TEXT, -- FK to containers.container_id
    operation TEXT,
    status TEXT, -- 'active' or 'idle'
    assigned_at TIMESTAMP,
    released_at TIMESTAMP,
    ...
);
```

---

## Priority Justification

**High Priority** because:
1. Breaks core visualization feature
2. Breaks assignment tracking system
3. Affects every scout tour operation
4. Requires manual database cleanup
5. Misleads users about ship status

---

## Environment

- Database: PostgreSQL (visualizer) + SQLite (bot operations)
- Bot Version: SpaceTraders V2 with DDD architecture
- Visualizer Version: React + Express backend
- Player ID: 2 (COOPER)
- System: X1-TS98

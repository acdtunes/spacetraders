# Container Lifecycle Management Implementation Plan

**Status**: Ready for Implementation
**Created**: 2025-11-24
**Updated**: 2025-11-24 (Added manual cleanup example, timezone fix context)
**Priority**: High (Prevents data inconsistency and operator confusion)

---

## Problem Statement

Two critical issues exist in the current container management system:

### Issue 1: Orphaned Child Containers

When a coordinator container (e.g., arbitrage coordinator) is stopped, its spawned worker containers continue running because:
- No parent-child relationship tracking exists in the database
- No cascading stop mechanism exists in the daemon
- Workers run as independent goroutines with their own contexts

**Impact**:
- Stale workers continue executing trades after coordinator stops
- Database shows inconsistent state (workers RUNNING, coordinator STOPPED)
- Ship assignments remain locked to orphaned workers
- Context cancellation errors when coordinator context is destroyed mid-execution
- Requires manual SQL intervention to clean up

**Real Example Observed (2025-11-24)**:
```
Timeline of arbitrage-worker-TORWINDO-8-87daeda7:
- 16:23:46 - Worker spawned by coordinator 0f85e4ef
- 16:38:33 - Coordinator STOPPED (context canceled)
- 16:40:41 - Worker tried to sync ship → "context canceled" error
- 16:45:09 - Worker finally FAILED (exit code 1)

Error logged:
"navigation to sell market failed: failed to execute route:
 failed to sync ship after arrival: failed to find player:
 failed to find player: context canceled"

Manual cleanup required:
psql> UPDATE containers SET status='STOPPED' WHERE id='arbitrage-worker-TORWINDO-7-47bd0083';
```

**Second Example (2025-11-24 17:14)**:
```
Worker arbitrage-worker-TORWINDO-7-47bd0083:
- Started: 17:14:38.374309
- Stopped: 17:14:38.388243 (14ms later)
- Database status: RUNNING (incorrect!)
- Daemon memory: Container not found (already cleaned up)

Issue: Status update race condition - worker exits so fast that Stop() doesn't update DB
```

### Issue 2: List Command Shows All Containers

The `container list` CLI command displays ALL containers (COMPLETED, FAILED, STOPPED) instead of only active ones (RUNNING, INTERRUPTED).

**Impact**:
- Poor UX - users see historical noise instead of current operations
- Requires manual filtering to see what's actually running
- Inconsistent with typical container management tools (Docker, Kubernetes show only running by default)

**Example**:
```bash
$ ./bin/spacetraders container list --player-id 12
CONTAINER ID                                            TYPE            STATUS       ITERATION  CREATED
──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
scout-tour-TORWINDO-F-f8b455d1                          SCOUT           RUNNING      0/∞        2025-11-24 16:45:09
contract-work-TORWINDO-A-1b7e85ce                       CONTRACT_WORKFLOW COMPLETED    1/1        2025-11-24 17:10:19  # ← Noise
contract-work-TORWINDO-A-3ecc1511                       CONTRACT_WORKFLOW COMPLETED    1/1        2025-11-24 17:07:26  # ← Noise
arbitrage_coordinator-X1-YZ19-573ab4a2                  ARBITRAGE_COORDINATOR STOPPED      0/∞        2025-11-24 16:45:38  # ← Noise
...
Total: 15 containers  # Only 5-6 actually running!
```

### Issue 3: Timezone Inconsistency (Recently Fixed)

Container logs displayed local timezone while database stored UTC, causing confusion when correlating events.

**Fixed in this session**:
- File: `internal/adapters/cli/container.go:243`
- Change: Added `.UTC()` to timestamp formatting
- Result: All timestamps now display in UTC matching database

---

## Current Architecture Analysis

### Container Spawning Patterns

**Pattern 1: Inline Goroutine Spawning (Arbitrage Coordinator)**
```go
// Location: internal/application/trading/commands/run_arbitrage_coordinator.go:296-394

func (h *RunArbitrageCoordinatorHandler) spawnWorkers(...) {
    // Create worker container entity
    workerContainer := container.NewContainer(
        workerID,
        container.TypeArbitrageWorker,
        cmd.PlayerID,
        1, // max iterations
        nil, // ← NO PARENT TRACKING
    )

    // Persist to database
    if err := h.containerRepo.Add(ctx, workerContainer); err != nil {
        return err
    }

    // Assign ship to worker
    if err := h.shipAssignmentRepo.AssignShip(ctx, cmd.PlayerID, ship.ShipSymbol(), workerID); err != nil {
        return err
    }

    // Execute worker via mediator (goroutine)
    workerCmd := &RunArbitrageWorkerCommand{...}
    _, err := h.mediator.Send(ctx, workerCmd) // ← Uses COORDINATOR's context

    // Worker completes and releases ship
    // But if coordinator stops, context cancels and worker fails mid-execution
}
```

**Key Problems**:
1. Workers use coordinator's context - cancellation propagates
2. No parent-child relationship stored in database
3. Workers not registered with daemon's container map
4. No mechanism to query "all workers of coordinator X"

**Pattern 2: Daemon Client Spawning (Contract Coordinator)**
```go
// Location: internal/application/contract/commands/run_fleet_coordinator.go

// Spawn worker via daemon client
result, err := h.daemonClient.RunContractWorkflow(ctx, &pb.RunContractWorkflowRequest{
    PlayerId:   ToProtobufPlayerID(cmd.PlayerID),
    ShipSymbol: ship.ShipSymbol(),
    // ...
})
```

**Better isolation but same parent-child tracking gap**.

### Container Stop Flow

**Location**: `internal/adapters/grpc/container_runner.go:128-177`

```go
func (r *ContainerRunner) Stop() error {
    // 1. Transition domain entity to STOPPING
    if err := r.containerEntity.Stop(); err != nil {
        return err
    }

    // 2. Cancel context (signals goroutine to exit)
    r.cancelFunc()

    // 3. Wait up to 10 seconds for graceful exit
    select {
    case <-r.done:
        // Success - goroutine exited
    case <-time.After(10 * time.Second):
        return fmt.Errorf("timeout waiting for container to stop")
    }

    // 4. Mark as stopped in domain
    if err := r.containerEntity.MarkStopped(); err != nil {
        return err
    }

    // 5. Persist STOPPED status to database
    if err := r.repo.UpdateStatus(ctx, r.containerEntity.ID(), r.containerEntity.PlayerID(),
        container.StatusStopped, &exitCode, &exitMessage); err != nil {
        return err
    }

    // 6. Release ship assignments
    r.releaseShipAssignments("stopped")

    // ✗ MISSING: Query and stop child containers
    // ✗ MISSING: Recursive cascade to nested children
    // ✗ MISSING: Handle race conditions (child exits before parent)

    return nil
}
```

### Container List Implementation

**Daemon Server**: `internal/adapters/grpc/daemon_server.go:1601-1621`
```go
func (s *DaemonServer) ListContainers(playerID *int, status *string) []*container.Container {
    s.mu.RLock()
    defer s.mu.RUnlock()

    var containers []*container.Container

    for _, runner := range s.containers {  // ← Only in-memory map
        cont := runner.containerEntity

        // Filter by player ID
        if playerID != nil && cont.PlayerID() != *playerID {
            continue
        }

        // Filter by status
        if status != nil && string(cont.Status()) != *status {
            continue
        }

        containers = append(containers, cont)
    }

    return containers
    // ✗ MISSING: No database query for historical containers
    // ✗ MISSING: No default filtering (returns all in-memory containers)
}
```

**CLI**: `internal/adapters/cli/container.go:31-99`
```go
func newContainerListCommand() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "list",
        Short: "List containers",
        RunE: func(cmd *cobra.Command, args []string) error {
            // ... player resolution ...

            response, err := daemonClient.ListContainers(ctx, &pb.ListContainersRequest{
                PlayerId: ToProtobufPlayerID(playerIdent.PlayerID),
                Status:   statusPtr,  // ← No default filter
            })

            // Display all returned containers
            // ✗ MISSING: No client-side filtering
            // ✗ MISSING: No --show-all flag for override
        },
    }
}
```

### Database Schema (Current)

**Location**: `internal/adapters/persistence/models.go`

```go
type ContainerModel struct {
    ID               string    `gorm:"primaryKey"`
    PlayerID         int       `gorm:"index:idx_containers_player"`
    ContainerType    string
    Status           string    `gorm:"index:idx_containers_status"`
    MaxIterations    int
    CurrentIteration int
    RestartCount     int
    StartedAt        time.Time
    StoppedAt        *time.Time
    ExitCode         *int
    ExitMessage      *string
    // ✗ MISSING: ParentContainerID *string
}
```

---

## Proposed Solution

### Architecture: Parent-Child Relationship Tracking

Add explicit parent-child relationships to enable:
1. **Querying children**: Find all workers spawned by a coordinator
2. **Cascading stop**: Stop parent → recursively stop children (depth-first)
3. **Cleanup**: Detect and fix orphaned workers on daemon restart
4. **Hierarchy display**: Show tree view in CLI

```
┌─────────────────────────────────┐
│  arbitrage_coordinator-ABC123   │  ← Parent (coordinator)
│  Status: RUNNING                │
│  ParentContainerID: NULL        │
└────────────┬────────────────────┘
             │ spawns
             ├─────────────────────┐
             │                     │
    ┌────────▼────────┐   ┌───────▼────────┐
    │ worker-SHIP1    │   │ worker-SHIP2   │  ← Children (workers)
    │ Status: RUNNING │   │ Status: RUNNING│
    │ Parent: ABC123  │   │ Parent: ABC123 │
    └─────────────────┘   └────────────────┘

Stop(ABC123) → Cascade stops both children → All 3 containers STOPPED
```

### Design Principles

1. **Database as Source of Truth**: Parent-child relationships stored in DB, not just in-memory
2. **Depth-First Recursive Stop**: Stop children before parent to avoid orphans
3. **Idempotent Operations**: Stopping already-stopped container succeeds (no-op)
4. **Error Resilience**: Continue stopping siblings if one child fails
5. **Default Filtering**: Show only active containers unless user explicitly requests all

---

## Implementation Details

### Phase 1: Database Schema Migration

**File**: `migrations/016_add_parent_container_id.up.sql`

```sql
-- Add parent container tracking column
-- NULL = top-level container (coordinator, standalone worker)
-- Non-NULL = child container spawned by a coordinator
ALTER TABLE containers ADD COLUMN parent_container_id VARCHAR(255);

-- Add partial index for efficient child lookups
-- Partial index: only index rows where parent_container_id IS NOT NULL
-- This saves space and improves performance for top-level container queries
CREATE INDEX idx_containers_parent_player
ON containers(parent_container_id, player_id)
WHERE parent_container_id IS NOT NULL;

-- Add documentation comment
COMMENT ON COLUMN containers.parent_container_id IS
'ID of parent coordinator that spawned this worker. NULL for top-level containers (coordinators, standalone workers).';

-- Add check constraint to prevent self-referencing containers
ALTER TABLE containers ADD CONSTRAINT chk_no_self_parent
CHECK (id != parent_container_id OR parent_container_id IS NULL);
```

**File**: `migrations/016_add_parent_container_id.down.sql`

```sql
-- Remove check constraint
ALTER TABLE containers DROP CONSTRAINT IF EXISTS chk_no_self_parent;

-- Remove index
DROP INDEX IF EXISTS idx_containers_parent_player;

-- Remove column (cascades to all dependent views)
ALTER TABLE containers DROP COLUMN IF EXISTS parent_container_id;
```

### Phase 2: Domain Model Updates

**File**: `internal/domain/container/container.go`

Add parent tracking to domain entity:

```go
type Container struct {
    id                string
    playerID          int
    containerType     Type
    status            Status
    maxIterations     int
    currentIteration  int
    restartCount      int
    createdAt         time.Time
    updatedAt         time.Time
    parentContainerID *string  // ← NEW: ID of parent coordinator (NULL for top-level)
}

// Constructor update - add parent parameter
func NewContainer(
    id string,
    containerType Type,
    playerID int,
    maxIterations int,
    parentContainerID *string,  // ← NEW: optional parent (NULL for coordinators)
) *Container {
    return &Container{
        id:                id,
        containerType:     containerType,
        playerID:          playerID,
        status:            StatusPending,
        maxIterations:     maxIterations,
        currentIteration:  0,
        restartCount:      0,
        createdAt:         time.Now().UTC(),
        updatedAt:         time.Now().UTC(),
        parentContainerID: parentContainerID,  // ← NEW
    }
}

// Getter for parent container ID
func (c *Container) ParentContainerID() *string {
    return c.parentContainerID
}

// Helper: Check if this is a root container (no parent)
func (c *Container) IsRootContainer() bool {
    return c.parentContainerID == nil
}

// Update from persistence factory to reconstruct parent
func NewContainerFromPersistence(
    id string,
    containerType Type,
    playerID int,
    status Status,
    maxIterations int,
    currentIteration int,
    restartCount int,
    createdAt time.Time,
    updatedAt time.Time,
    parentContainerID *string,  // ← NEW parameter
) *Container {
    return &Container{
        id:                id,
        containerType:     containerType,
        playerID:          playerID,
        status:            status,
        maxIterations:     maxIterations,
        currentIteration:  currentIteration,
        restartCount:      restartCount,
        createdAt:         createdAt,
        updatedAt:         updatedAt,
        parentContainerID: parentContainerID,  // ← NEW
    }
}
```

### Phase 3: Repository Updates

**File**: `internal/adapters/persistence/models.go`

Add field to database model:

```go
type ContainerModel struct {
    ID                string     `gorm:"primaryKey"`
    PlayerID          int        `gorm:"index:idx_containers_player"`
    ContainerType     string
    Status            string     `gorm:"index:idx_containers_status"`
    MaxIterations     int
    CurrentIteration  int
    RestartCount      int
    ParentContainerID *string    `gorm:"column:parent_container_id;index:idx_containers_parent_player"`  // ← NEW
    StartedAt         time.Time
    StoppedAt         *time.Time
    ExitCode          *int
    ExitMessage       *string
    CreatedAt         time.Time
    UpdatedAt         time.Time
}

// TableName specifies the table name
func (ContainerModel) TableName() string {
    return "containers"
}
```

**File**: `internal/adapters/persistence/container_repository.go`

Add child query method and update persistence:

```go
// FindChildContainers retrieves all direct children of a parent container
// Returns empty slice if no children found (not an error)
func (r *GormContainerRepository) FindChildContainers(
    ctx context.Context,
    parentContainerID string,
    playerID int,
) ([]*container.Container, error) {
    var models []ContainerModel

    err := r.db.WithContext(ctx).
        Where("parent_container_id = ? AND player_id = ?", parentContainerID, playerID).
        Order("started_at ASC").  // Oldest children first for consistent ordering
        Find(&models).Error

    if err != nil {
        return nil, fmt.Errorf("failed to find child containers: %w", err)
    }

    // Convert models to domain entities
    containers := make([]*container.Container, 0, len(models))
    for i := range models {
        cont, err := r.modelToEntity(&models[i])
        if err != nil {
            return nil, fmt.Errorf("failed to convert child container %s: %w", models[i].ID, err)
        }
        containers = append(containers, cont)
    }

    return containers, nil
}

// Update Add() to persist parent ID
func (r *GormContainerRepository) Add(
    ctx context.Context,
    cont *container.Container,
) error {
    model := &ContainerModel{
        ID:                cont.ID(),
        PlayerID:          cont.PlayerID(),
        ContainerType:     string(cont.Type()),
        Status:            string(cont.Status()),
        MaxIterations:     cont.MaxIterations(),
        CurrentIteration:  cont.CurrentIteration(),
        RestartCount:      cont.RestartCount(),
        ParentContainerID: cont.ParentContainerID(),  // ← NEW: persist parent relationship
        StartedAt:         cont.CreatedAt(),
        CreatedAt:         cont.CreatedAt(),
        UpdatedAt:         cont.UpdatedAt(),
    }

    if err := r.db.WithContext(ctx).Create(model).Error; err != nil {
        return fmt.Errorf("failed to create container: %w", err)
    }

    return nil
}

// Update modelToEntity to reconstruct parent ID
func (r *GormContainerRepository) modelToEntity(model *ContainerModel) (*container.Container, error) {
    return container.NewContainerFromPersistence(
        model.ID,
        container.Type(model.ContainerType),
        model.PlayerID,
        container.Status(model.Status),
        model.MaxIterations,
        model.CurrentIteration,
        model.RestartCount,
        model.StartedAt,
        model.UpdatedAt,
        model.ParentContainerID,  // ← NEW: reconstruct parent relationship
    ), nil
}
```

### Phase 4: Cascading Stop Implementation

**File**: `internal/adapters/grpc/daemon_server.go`

Update `StopContainer()` method with recursive child stopping:

```go
// StopContainer stops a container and all its children (depth-first, recursive)
//
// Algorithm:
// 1. Find all direct children from database
// 2. Recursively stop each child (handles grandchildren automatically)
// 3. Stop the parent container
// 4. Remove parent from in-memory map
//
// Error Handling:
// - If child stop fails, log error but continue stopping other children
// - If parent stop fails, return error (children already stopped)
// - If container not in memory, update database status directly
func (s *DaemonServer) StopContainer(containerID string, playerID int) error {
    ctx := context.Background()

    // STEP 1: Find all child containers from database
    children, err := s.containerRepo.FindChildContainers(ctx, containerID, playerID)
    if err != nil {
        return fmt.Errorf("failed to find child containers: %w", err)
    }

    // STEP 2: Recursively stop each child (depth-first)
    var childErrors []string
    for _, child := range children {
        childID := child.ID()
        childStatus := child.Status()

        // Skip already-stopped/completed containers
        if childStatus == container.StatusStopped ||
           childStatus == container.StatusCompleted ||
           childStatus == container.StatusFailed {
            s.logger.Debugf("Child container %s already in terminal state: %s", childID, childStatus)
            continue
        }

        // Check if child is in-memory (actively running)
        s.mu.Lock()
        _, childExists := s.containers[childID]
        s.mu.Unlock()

        if childExists {
            // Recursive stop (handles nested coordinators and their workers)
            if err := s.StopContainer(childID, playerID); err != nil {
                errMsg := fmt.Sprintf("failed to stop child %s: %v", childID, err)
                s.logger.Errorf(errMsg)
                childErrors = append(childErrors, errMsg)
                // Continue stopping other children despite error
            }
        } else {
            // Child not in memory - either already stopped or database inconsistency
            // Update database status to ensure consistency
            s.logger.Warnf("Child container %s not in memory but status=%s, updating DB", childID, childStatus)
            exitCode := 143 // SIGTERM
            exitMessage := "Stopped via cascade (parent container stopped)"
            if err := s.containerRepo.UpdateStatus(
                ctx,
                childID,
                playerID,
                container.StatusStopped,
                &exitCode,
                &exitMessage,
            ); err != nil {
                errMsg := fmt.Sprintf("failed to update child status %s: %v", childID, err)
                s.logger.Errorf(errMsg)
                childErrors = append(childErrors, errMsg)
            }
        }
    }

    // STEP 3: Stop the parent container
    s.mu.Lock()
    runner, exists := s.containers[containerID]
    s.mu.Unlock()

    if !exists {
        // Parent not in memory - check database
        parentContainer, err := s.containerRepo.FindByID(ctx, containerID, playerID)
        if err != nil {
            return fmt.Errorf("container not found: %w", err)
        }

        // If already in terminal state, this is a no-op (idempotent)
        if parentContainer.Status() == container.StatusStopped ||
           parentContainer.Status() == container.StatusCompleted ||
           parentContainer.Status() == container.StatusFailed {
            s.logger.Infof("Container %s already in terminal state: %s", containerID, parentContainer.Status())
            return nil
        }

        // Update database to mark as stopped
        exitCode := 143
        exitMessage := "Stopped (not in daemon memory)"
        if err := s.containerRepo.UpdateStatus(
            ctx,
            containerID,
            playerID,
            container.StatusStopped,
            &exitCode,
            &exitMessage,
        ); err != nil {
            return fmt.Errorf("failed to update container status: %w", err)
        }

        return nil
    }

    // Stop the runner (existing ContainerRunner.Stop() logic)
    if err := runner.Stop(); err != nil {
        return fmt.Errorf("failed to stop container: %w", err)
    }

    // STEP 4: Remove from in-memory map
    s.mu.Lock()
    delete(s.containers, containerID)
    s.mu.Unlock()

    // Report child errors if any (after successfully stopping parent)
    if len(childErrors) > 0 {
        s.logger.Warnf("Container %s stopped but encountered %d child errors: %s",
            containerID, len(childErrors), strings.Join(childErrors, "; "))
    }

    return nil
}
```

### Phase 5: Coordinator Updates

**File**: `internal/application/trading/commands/run_arbitrage_coordinator.go`

Pass coordinator ID as parent when spawning workers:

```go
// Update Handle() to pass coordinator container ID to spawnWorkers
func (h *RunArbitrageCoordinatorHandler) Handle(
    ctx context.Context,
    cmd *RunArbitrageCoordinatorCommand,
) (*RunArbitrageCoordinatorResponse, error) {
    // ... existing setup code ...

    for {
        // SCAN PHASE: Find arbitrage opportunities
        opportunities, err := h.opportunityFinder.FindOpportunities(ctx, cmd.PlayerID, idleHaulers)
        if err != nil {
            return nil, fmt.Errorf("failed to find opportunities: %w", err)
        }

        if len(opportunities) > 0 {
            // SPAWN PHASE: Create workers for opportunities
            // ← NEW: Pass coordinator's container ID as parent
            if err := h.spawnWorkers(ctx, cmd.ContainerID, opportunities); err != nil {
                h.logger.Errorf("Failed to spawn workers: %v", err)
                // Continue to next iteration despite error
            }
        }

        // WAIT PHASE: Sleep before next scan
        select {
        case <-ctx.Done():
            return &RunArbitrageCoordinatorResponse{}, ctx.Err()
        case <-time.After(30 * time.Second):
            // Next iteration
        }
    }
}

// Update spawnWorkers signature to accept coordinator ID
func (h *RunArbitrageCoordinatorHandler) spawnWorkers(
    ctx context.Context,
    coordinatorID string,  // ← NEW: parent container ID
    opportunities []*trading.ArbitrageOpportunity,
) error {
    for _, opp := range opportunities {
        ship := opp.Ship

        // Generate unique worker ID
        workerID := fmt.Sprintf("arbitrage-worker-%s-%s",
            ship.ShipSymbol(),
            generateRandomSuffix())

        // ← NEW: Create worker with parent container ID
        parentID := coordinatorID  // Reference to coordinator
        workerContainer := container.NewContainer(
            workerID,
            container.TypeArbitrageWorker,
            cmd.PlayerID,
            1, // single iteration
            &parentID,  // ← NEW: Link to parent coordinator
        )

        // Persist worker to database (with parent relationship)
        if err := h.containerRepo.Add(ctx, workerContainer); err != nil {
            return fmt.Errorf("failed to create worker container %s: %w", workerID, err)
        }

        // Rest of existing spawning logic...
        // (ship assignment, mediator command, status updates)
    }

    return nil
}
```

### Phase 6: List Containers Filtering

**File**: `internal/adapters/grpc/daemon_service_impl.go`

Add default filtering to gRPC handler:

```go
func (s *daemonServiceImpl) ListContainers(
    ctx context.Context,
    req *pb.ListContainersRequest,
) (*pb.ListContainersResponse, error) {
    // Resolve player ID
    var playerID *int
    if req.PlayerId != nil {
        pid := FromProtobufPlayerID(*req.PlayerId)
        playerID = &pid
    }

    // Apply status filter with smart defaults
    var statusFilter *string
    if req.Status != nil && *req.Status != "" {
        // User explicitly requested a status - use as-is
        statusFilter = req.Status
    } else {
        // DEFAULT: Only show active containers (RUNNING, INTERRUPTED)
        // Rationale: Operators care about what's currently active, not history
        // Use comma-separated list for multiple statuses
        defaultStatuses := "RUNNING,INTERRUPTED"
        statusFilter = &defaultStatuses
    }

    // Get containers from daemon (in-memory map)
    containers := s.daemon.ListContainers(playerID, statusFilter)

    // Convert to protobuf...
    pbContainers := make([]*pb.Container, 0, len(containers))
    for _, cont := range containers {
        pbContainers = append(pbContainers, &pb.Container{
            ContainerId:      cont.ID(),
            ContainerType:    string(cont.Type()),
            Status:           string(cont.Status()),
            PlayerId:         ToProtobufPlayerID(cont.PlayerID()),
            CreatedAt:        cont.CreatedAt().Format(time.RFC3339),
            UpdatedAt:        cont.UpdatedAt().Format(time.RFC3339),
            CurrentIteration: int32(cont.CurrentIteration()),
            MaxIterations:    int32(cont.MaxIterations()),
            RestartCount:     int32(cont.RestartCount()),
            ParentContainerId: stringPtrToProto(cont.ParentContainerID()),  // ← NEW: include parent
        })
    }

    return &pb.ListContainersResponse{Containers: pbContainers}, nil
}

// Helper to convert *string to protobuf optional string
func stringPtrToProto(s *string) *string {
    if s == nil {
        return nil
    }
    // Return pointer to avoid nil dereference
    return s
}
```

**File**: `internal/adapters/grpc/daemon_server.go`

Update `ListContainers()` to support comma-separated status filter:

```go
func (s *DaemonServer) ListContainers(
    playerID *int,
    statusFilter *string,
) []*container.Container {
    s.mu.RLock()
    defer s.mu.RUnlock()

    var containers []*container.Container

    // Parse comma-separated status filter into map for O(1) lookup
    var allowedStatuses map[string]bool
    if statusFilter != nil && *statusFilter != "" {
        allowedStatuses = make(map[string]bool)
        statuses := strings.Split(*statusFilter, ",")
        for _, status := range statuses {
            trimmed := strings.TrimSpace(status)
            if trimmed != "" {
                allowedStatuses[trimmed] = true
            }
        }
    }

    for _, runner := range s.containers {
        cont := runner.containerEntity

        // Filter by player ID
        if playerID != nil && cont.PlayerID() != *playerID {
            continue
        }

        // Filter by status (if filter provided)
        if allowedStatuses != nil {
            if !allowedStatuses[string(cont.Status())] {
                continue
            }
        }

        containers = append(containers, cont)
    }

    return containers
}
```

**File**: `internal/adapters/cli/container.go`

Add `--show-all` flag to override default filtering:

```go
func newContainerListCommand() *cobra.Command {
    var (
        status  string
        showAll bool  // ← NEW: override default filtering
    )

    cmd := &cobra.Command{
        Use:   "list",
        Short: "List containers",
        Long: `List containers for the current player.

By default, only RUNNING and INTERRUPTED containers are shown to reduce clutter.
Completed, failed, and stopped containers are hidden unless explicitly requested.

Flags:
  --show-all       Show all containers including completed/failed/stopped
  --status STATUS  Filter by specific status (overrides default)

Examples:
  # Show only active containers (default)
  spacetraders container list --player-id 12

  # Show all containers including historical
  spacetraders container list --player-id 12 --show-all

  # Show only completed containers
  spacetraders container list --player-id 12 --status COMPLETED

  # Show failed and stopped containers
  spacetraders container list --player-id 12 --status FAILED,STOPPED`,
        RunE: func(cmd *cobra.Command, args []string) error {
            // Resolve player from flags or config
            playerIdent, err := resolvePlayerIdentifier()
            if err != nil {
                return err
            }

            // Connect to daemon
            daemonClient, err := NewDaemonClient()
            if err != nil {
                return fmt.Errorf("failed to connect to daemon: %w", err)
            }
            defer daemonClient.Close()

            // Determine status filter
            var statusPtr *string
            if status != "" {
                // User specified explicit status - use as-is
                statusPtr = &status
            } else if showAll {
                // Show all - pass empty string to disable default filter
                emptyStr := ""
                statusPtr = &emptyStr
            }
            // else: statusPtr remains nil, server applies default filter (RUNNING,INTERRUPTED)

            // Call daemon
            ctx := context.Background()
            response, err := daemonClient.ListContainers(ctx, &pb.ListContainersRequest{
                PlayerId: ToProtobufPlayerID(playerIdent.PlayerID),
                Status:   statusPtr,
            })
            if err != nil {
                return fmt.Errorf("failed to list containers: %w", err)
            }

            // Display results in table format
            if len(response.Containers) == 0 {
                fmt.Println("No containers found")
                return nil
            }

            // ... existing table formatting code ...

            return nil
        },
    }

    cmd.Flags().StringVar(&status, "status", "", "Filter by status (comma-separated: RUNNING,COMPLETED,FAILED,STOPPED,INTERRUPTED)")
    cmd.Flags().BoolVar(&showAll, "show-all", false, "Show all containers including completed/failed/stopped")

    return cmd
}
```

---

## Testing Strategy

### Unit Tests (BDD Style)

**File**: `test/bdd/features/domain/container/parent_child_tracking.feature`

```gherkin
Feature: Parent-Child Container Tracking
  Containers can spawn child containers and track parent-child relationships

  Scenario: Create container with parent ID
    Given a coordinator container with ID "coord-123" and player ID 1
    When I create a worker container with parent ID "coord-123" and player ID 1
    Then the worker's parent container ID should be "coord-123"
    And the coordinator's parent container ID should be nil

  Scenario: Create root container without parent
    When I create a coordinator container without a parent for player 1
    Then the container's parent container ID should be nil
    And the container should be a root container

  Scenario: Query child containers
    Given a coordinator container "coord-123" with player ID 1
    And a worker container "worker-1" with parent "coord-123" and player ID 1
    And a worker container "worker-2" with parent "coord-123" and player ID 1
    And a worker container "worker-3" with parent "coord-456" and player ID 1
    And a worker container "worker-4" with parent "coord-123" and player ID 2
    When I query for children of "coord-123" for player 1
    Then I should get 2 child containers
    And the children should be "worker-1" and "worker-2"
    And the children should not include "worker-3" (different parent)
    And the children should not include "worker-4" (different player)

  Scenario: Self-referencing parent is rejected
    When I attempt to create a container with itself as parent
    Then the operation should fail with "invalid parent reference"
```

**File**: `test/bdd/features/daemon/container_lifecycle/cascading_stop.feature`

```gherkin
Feature: Cascading Container Stop
  Stopping a parent container should stop all child containers recursively

  Background:
    Given a player with ID 12
    And the daemon is running

  Scenario: Stop coordinator stops all workers
    Given a running arbitrage coordinator "coord-123"
    And a running worker "worker-1" spawned by "coord-123"
    And a running worker "worker-2" spawned by "coord-123"
    When I stop container "coord-123"
    Then container "coord-123" status should be "STOPPED" in database
    And container "worker-1" status should be "STOPPED" in database
    And container "worker-2" status should be "STOPPED" in database
    And all 3 containers should not be in daemon memory

  Scenario: Stop coordinator with nested children (depth-first)
    Given a running coordinator "coord-123"
    And a running sub-coordinator "sub-coord-456" spawned by "coord-123"
    And a running worker "worker-1" spawned by "sub-coord-456"
    And a running worker "worker-2" spawned by "sub-coord-456"
    When I stop container "coord-123"
    Then the stop order should be:
      | order | container_id   |
      | 1     | worker-1       |
      | 2     | worker-2       |
      | 3     | sub-coord-456  |
      | 4     | coord-123      |
    And all 4 containers should have status "STOPPED"

  Scenario: Stop already-stopped coordinator is idempotent
    Given a stopped coordinator "coord-123"
    And a running worker "worker-1" spawned by "coord-123"
    When I stop container "coord-123"
    Then the operation should succeed
    And container "worker-1" status should be "STOPPED"
    And no error should be logged

  Scenario: Stop coordinator with mix of running and completed children
    Given a running coordinator "coord-123"
    And a completed worker "worker-1" spawned by "coord-123"
    And a running worker "worker-2" spawned by "coord-123"
    And a failed worker "worker-3" spawned by "coord-123"
    When I stop container "coord-123"
    Then only "worker-2" should be stopped (others already terminal)
    And container "coord-123" should be stopped
    And no errors should occur

  Scenario: Stop continues despite child stop failure
    Given a running coordinator "coord-123"
    And a running worker "worker-1" spawned by "coord-123"
    And a running worker "worker-2" spawned by "coord-123" that will fail to stop
    And a running worker "worker-3" spawned by "coord-123"
    When I stop container "coord-123"
    Then container "worker-1" should be stopped
    Then container "worker-3" should be stopped
    And container "coord-123" should be stopped
    And a warning should be logged about worker-2 failure

  Scenario: Orphaned worker cleanup (not in memory, but DB shows RUNNING)
    Given a coordinator "coord-123" that was stopped
    And a worker "worker-1" in database with status "RUNNING" and parent "coord-123"
    But "worker-1" is not in daemon memory (orphaned)
    When I stop container "coord-123" again
    Then the database status of "worker-1" should be updated to "STOPPED"
    And the exit message should indicate "Stopped via cascade"
```

**File**: `test/bdd/features/cli/container_list_filtering.feature`

```gherkin
Feature: Container List Filtering
  List command should show only active containers by default

  Background:
    Given a player with ID 12
    And the following containers exist:
      | id          | type       | status      |
      | coord-1     | ARBITRAGE  | RUNNING     |
      | worker-1    | ARBITRAGE  | RUNNING     |
      | worker-2    | ARBITRAGE  | COMPLETED   |
      | coord-2     | CONTRACT   | STOPPED     |
      | worker-3    | CONTRACT   | FAILED      |
      | worker-4    | CONTRACT   | INTERRUPTED |

  Scenario: Default list shows only active containers
    When I run "spacetraders container list --player-id 12"
    Then I should see 3 containers:
      | id          | status      |
      | coord-1     | RUNNING     |
      | worker-1    | RUNNING     |
      | worker-4    | INTERRUPTED |
    And I should NOT see "worker-2" (completed)
    And I should NOT see "coord-2" (stopped)
    And I should NOT see "worker-3" (failed)

  Scenario: Show all containers with --show-all flag
    When I run "spacetraders container list --player-id 12 --show-all"
    Then I should see all 6 containers

  Scenario: Filter by specific status
    When I run "spacetraders container list --player-id 12 --status COMPLETED"
    Then I should see 1 container:
      | id       | status    |
      | worker-2 | COMPLETED |

  Scenario: Filter by multiple statuses
    When I run "spacetraders container list --player-id 12 --status FAILED,STOPPED"
    Then I should see 2 containers:
      | id       | status  |
      | coord-2  | STOPPED |
      | worker-3 | FAILED  |

  Scenario: Explicit status overrides --show-all
    When I run "spacetraders container list --player-id 12 --show-all --status RUNNING"
    Then I should see only RUNNING containers
    And the --show-all flag should be ignored
```

### Integration Tests

**Manual Test Plan**:

1. **Test Cascading Stop with Real Arbitrage Coordinator**:
   ```bash
   # Start arbitrage coordinator with 2 ships
   ./bin/spacetraders arbitrage start --player-id 12 --ship TORWINDO-7 --ship TORWINDO-8

   # Wait for opportunities and workers to spawn
   sleep 60
   ./bin/spacetraders container list --player-id 12
   # Expected: 1 coordinator + 2 workers (all RUNNING)

   # Get coordinator ID
   COORD_ID=$(psql -tAc "SELECT id FROM containers WHERE container_type='ARBITRAGE_COORDINATOR' AND status='RUNNING' LIMIT 1;")

   # Stop coordinator
   ./bin/spacetraders container stop $COORD_ID

   # Verify all stopped (list should be empty with default filter)
   ./bin/spacetraders container list --player-id 12
   # Expected: (empty)

   # Verify database consistency
   psql -c "SELECT id, status, parent_container_id FROM containers WHERE id LIKE '%arbitrage%' ORDER BY started_at DESC LIMIT 5;"
   # Expected: All status=STOPPED, workers have parent_container_id=$COORD_ID
   ```

2. **Test List Filtering**:
   ```bash
   # Create mix of containers in different states
   # (start several coordinators, let some complete, stop some manually)

   # Default: only RUNNING/INTERRUPTED
   ./bin/spacetraders container list --player-id 12
   # Should only show active containers

   # Show all
   ./bin/spacetraders container list --player-id 12 --show-all
   # Should show completed/failed/stopped

   # Filter by status
   ./bin/spacetraders container list --player-id 12 --status COMPLETED
   # Should show only completed

   # Multiple statuses
   ./bin/spacetraders container list --player-id 12 --status FAILED,STOPPED
   # Should show failed + stopped
   ```

3. **Test Orphan Cleanup**:
   ```bash
   # Simulate orphan: manually set worker to RUNNING after coordinator stops
   psql -c "UPDATE containers SET status='RUNNING' WHERE id='arbitrage-worker-TORWINDO-7-test';"

   # Verify inconsistency
   psql -c "SELECT id, status FROM containers WHERE id='arbitrage-worker-TORWINDO-7-test';"
   # Expected: RUNNING

   # Stop parent again (should cascade to orphan)
   ./bin/spacetraders container stop <parent-id>

   # Verify cleanup
   psql -c "SELECT id, status, exit_message FROM containers WHERE id='arbitrage-worker-TORWINDO-7-test';"
   # Expected: STOPPED with message "Stopped via cascade"
   ```

4. **Test Nested Coordinators** (if applicable):
   ```bash
   # If we ever implement coordinator-spawns-coordinator pattern
   # (e.g., mining coordinator spawns sub-coordinators per asteroid belt)
   # Test depth-first stop propagation
   ```

### Edge Cases

1. **Race Condition: Worker exits before parent stops**
   - Worker completes naturally 1ms before parent is stopped
   - Expected: Worker status=COMPLETED, not overwritten to STOPPED

2. **Deep Hierarchy: 5+ levels of nesting**
   - Coordinator → Sub-coord → Sub-sub-coord → Worker
   - Expected: Depth-first stop, no stack overflow, all stopped

3. **Circular Reference: A→B→A** (should be prevented by check constraint)
   - Attempt to create circular parent-child relationship
   - Expected: Database constraint violation, operation fails

4. **High Concurrency: Stop coordinator while workers are spawning**
   - Coordinator spawning 10 workers when stop is called
   - Expected: All spawned workers stopped, partially-spawned workers cleaned up

5. **Database Failure: Repository query fails during cascade**
   - Network error during FindChildContainers()
   - Expected: Parent stop fails, error returned, no partial state

---

## Migration Path

### Step 1: Database Migration

```bash
# Backup database before migration
pg_dump -h localhost -U spacetraders -d spacetraders > backup_pre_migration_016.sql

# Apply migration
cd migrations
goose -dir . postgres "postgresql://spacetraders:dev_password@localhost:5432/spacetraders?sslmode=disable" up

# Verify schema
psql -h localhost -U spacetraders -d spacetraders -c "\d containers"
# Should show: parent_container_id | character varying(255)

# Verify index
psql -h localhost -U spacetraders -d spacetraders -c "\di idx_containers_parent_player"
# Should show partial index definition

# Verify check constraint
psql -h localhost -U spacetraders -d spacetraders -c "\d+ containers"
# Should show: CHECK (id <> parent_container_id OR parent_container_id IS NULL)
```

### Step 2: Code Deployment

Apply changes in order:

1. **Domain Model** (`container.go`)
   - Add `parentContainerID` field
   - Update constructors
   - Add getter

2. **Persistence Layer** (`models.go`, `container_repository.go`)
   - Add field to `ContainerModel`
   - Add `FindChildContainers()` method
   - Update `Add()` and `modelToEntity()`

3. **Daemon Server** (`daemon_server.go`)
   - Implement cascading stop in `StopContainer()`
   - Update `ListContainers()` for comma-separated status

4. **Coordinators** (`run_arbitrage_coordinator.go`, etc.)
   - Pass parent container ID when spawning workers
   - Update `Handle()` and `spawnWorkers()` signatures

5. **gRPC Service** (`daemon_service_impl.go`)
   - Add default status filtering
   - Update protobuf conversion to include parent ID

6. **CLI** (`container.go`)
   - Add `--show-all` flag
   - Update help text
   - Add examples

**Build and Deploy**:
```bash
# Build all binaries
make build

# Stop daemon
pkill spacetraders-daemon

# Restart daemon with new code
nohup ./bin/spacetraders-daemon > /tmp/daemon.log 2>&1 &

# Verify daemon started
./bin/spacetraders container list --player-id 12
```

### Step 3: Verification

1. **Check daemon logs**:
   ```bash
   tail -f /tmp/daemon.log | grep -E "CASCADE|PARENT|CHILD"
   # Should see logs about cascading stops
   ```

2. **Start test coordinator**:
   ```bash
   # This will test new parent-child tracking
   ./bin/spacetraders arbitrage start --player-id 12 --ship TORWINDO-7
   ```

3. **Verify parent-child relationship**:
   ```bash
   psql -c "SELECT id, container_type, status, parent_container_id FROM containers WHERE player_id=12 ORDER BY started_at DESC LIMIT 5;"
   # Workers should have parent_container_id = coordinator ID
   ```

4. **Test cascading stop**:
   ```bash
   # Stop coordinator
   COORD_ID=$(psql -tAc "SELECT id FROM containers WHERE container_type='ARBITRAGE_COORDINATOR' AND status='RUNNING' LIMIT 1;")
   ./bin/spacetraders container stop $COORD_ID

   # Verify cascade
   psql -c "SELECT id, status FROM containers WHERE parent_container_id='$COORD_ID';"
   # All children should be STOPPED
   ```

---

## Backward Compatibility

### Database

- **Existing containers**: `parent_container_id` will be NULL (interpreted as root/top-level)
- **New containers**: Will populate field appropriately based on spawn context
- **Queries**: NULL-safe (uses `WHERE parent_container_id IS NOT NULL` in index)
- **No data migration needed**: NULL is valid for all existing containers

### CLI

- **Breaking change**: Default behavior now filters to RUNNING/INTERRUPTED only
- **Migration path**: Users expecting all containers must add `--show-all` flag
- **Escape hatch**: Explicit `--status` flag overrides default
- **Announcement**: Document in release notes as intentional UX improvement

### API

- **gRPC**: Backward compatible - existing clients can pass explicit status to override
- **Mediator**: Coordinator command signatures updated, but backward compatible via defaults
- **Daemon**: New `ListContainers()` signature supports old behavior via empty status filter

---

## Rollback Plan

If issues arise post-deployment:

### Immediate Rollback (Code Only)

```bash
# Revert to previous daemon version
git checkout HEAD~1
make build
pkill spacetraders-daemon
nohup ./bin/spacetraders-daemon > /tmp/daemon.log 2>&1 &
```

**Impact**: Loses parent-child tracking, but no data corruption. Database column remains but unused.

### Full Rollback (Code + Database)

```bash
# Revert code
git checkout HEAD~1
make build

# Rollback migration
cd migrations
goose -dir . postgres "postgresql://..." down

# Restart daemon
pkill spacetraders-daemon
nohup ./bin/spacetraders-daemon > /tmp/daemon.log 2>&1 &
```

**Impact**: Complete rollback to previous state. Any parent-child data created since migration is lost.

### Manual Cleanup (If Needed)

If orphaned workers exist after rollback:

```sql
-- Find orphaned workers (no running coordinator)
SELECT w.id, w.status, w.started_at
FROM containers w
LEFT JOIN containers c ON w.parent_container_id = c.id
WHERE w.container_type LIKE '%WORKER%'
  AND w.status = 'RUNNING'
  AND (c.id IS NULL OR c.status != 'RUNNING');

-- Clean up orphaned workers
UPDATE containers
SET status = 'STOPPED',
    exit_code = 143,
    exit_message = 'Orphaned worker - coordinator stopped',
    stopped_at = NOW()
WHERE id IN (SELECT w.id FROM ...);

-- Release ship assignments for orphaned workers
DELETE FROM ship_assignments
WHERE container_id IN (SELECT id FROM ...);
```

---

## Future Enhancements

### 1. Container Hierarchy Display (Tree View)

Add ASCII tree view to CLI:

```bash
$ ./bin/spacetraders container list --tree --player-id 12
CONTAINER ID                                            TYPE            STATUS       ITERATION
────────────────────────────────────────────────────────────────────────────────────────────────
arbitrage_coordinator-X1-YZ19-abc123                    ARBITRAGE       RUNNING      0/∞
  ├─ arbitrage-worker-TORWINDO-7-def456                 ARBITRAGE_WORKER RUNNING      0/1
  └─ arbitrage-worker-TORWINDO-8-ghi789                 ARBITRAGE_WORKER RUNNING      0/1
contract_fleet_coordinator-player-12-jkl012             CONTRACT_COORD  RUNNING      0/1
  ├─ contract-work-TORWINDO-A-mno345                    CONTRACT_WORKFLOW RUNNING      0/1
  ├─ contract-work-TORWINDO-B-pqr678                    CONTRACT_WORKFLOW COMPLETED    1/1
  └─ contract-work-TORWINDO-C-stu901                    CONTRACT_WORKFLOW RUNNING      0/1
scout_fleet_coordinator-X1-AA11-vwx234                  SCOUT_COORD     RUNNING      0/∞
  ├─ scout-tour-TORWINDO-2-yza567                       SCOUT           RUNNING      0/∞
  └─ scout-tour-TORWINDO-3-bcd890                       SCOUT           RUNNING      0/∞
```

**Implementation**:
- New `--tree` flag triggers hierarchical display
- Recursively build tree structure from parent-child relationships
- Use box-drawing characters for visual hierarchy (│ ├ └ ─)
- Indent children based on depth level

### 2. Graceful Child Shutdown

Instead of force-stopping children, send graceful shutdown signal with timeout:

```go
// Gracefully shutdown children with 30-second timeout per child
for _, child := range children {
    if err := child.GracefulShutdown(30 * time.Second); err != nil {
        // If graceful shutdown fails/times out, force stop
        s.logger.Warnf("Graceful shutdown of %s failed, force stopping", child.ID())
        child.ForceStop()
    }
}
```

**Benefits**:
- Workers can complete current operation before stopping
- Cleaner database state (less interrupted trades)
- Better error messages (completed vs terminated)

### 3. Orphan Detection Service

Background goroutine to detect and auto-fix orphaned containers:

```go
func (s *DaemonServer) startOrphanDetector() {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            s.detectAndCleanOrphans()
        case <-s.shutdownChan:
            return
        }
    }
}

func (s *DaemonServer) detectAndCleanOrphans() {
    ctx := context.Background()

    // Find workers with stopped parents but still marked RUNNING
    orphans, err := s.containerRepo.FindOrphanedWorkers(ctx)
    if err != nil {
        s.logger.Errorf("Failed to find orphaned workers: %v", err)
        return
    }

    for _, orphan := range orphans {
        s.logger.Warnf("Detected orphaned worker %s, cleaning up", orphan.ID())
        exitCode := 143
        exitMessage := "Orphaned worker - parent stopped"
        if err := s.containerRepo.UpdateStatus(
            ctx, orphan.ID(), orphan.PlayerID(),
            container.StatusStopped, &exitCode, &exitMessage,
        ); err != nil {
            s.logger.Errorf("Failed to clean orphan %s: %v", orphan.ID(), err)
        }
    }

    if len(orphans) > 0 {
        s.logger.Infof("Cleaned up %d orphaned workers", len(orphans))
    }
}
```

### 4. Container Groups / Tags

Support grouping containers beyond parent-child:

```go
type Container struct {
    // ... existing fields
    tags map[string]string  // e.g., {"operation": "arbitrage", "fleet": "haulers"}
}

// Query by tag
./bin/spacetraders container list --tag operation=arbitrage
./bin/spacetraders container list --tag fleet=haulers

// Bulk operations
./bin/spacetraders container stop --tag operation=arbitrage  # Stop all arbitrage containers
```

### 5. Metrics and Observability

Add Prometheus metrics for container lifecycle:

```go
// Counter: Total cascading stops
spacetraders_container_cascade_stops_total{depth="1|2|3|4+"}

// Gauge: Orphaned containers detected
spacetraders_container_orphans_detected_total

// Counter: Stop failures
spacetraders_container_stop_failures_total{reason="timeout|error|context_canceled"}

// Histogram: Cascade stop duration
spacetraders_container_cascade_stop_duration_seconds{depth="1|2|3|4+"}

// Gauge: Active containers by type and depth
spacetraders_container_active_total{type="ARBITRAGE_WORKER",depth="0|1|2"}
```

### 6. Container Recovery Enhancement

Enhanced recovery after daemon restart:

```go
func (s *DaemonServer) RecoverRunningContainers() error {
    // Find RUNNING containers in DB
    running, err := s.containerRepo.ListByStatus(ctx, container.StatusRunning)

    for _, cont := range running {
        // Check if parent is also recovering
        if cont.ParentContainerID() != nil {
            parent, _ := s.containerRepo.FindByID(ctx, *cont.ParentContainerID(), cont.PlayerID())
            if parent.Status() == container.StatusRunning {
                // Skip - parent will re-spawn if needed
                s.logger.Infof("Skipping recovery of child %s - parent %s will handle", cont.ID(), parent.ID())
                continue
            }
        }

        // Recover container...
    }
}
```

---

## References

- **Original Issue**: Context canceled errors when stopping coordinators
- **Related Issues**:
  - Ship assignment deadlocks from orphaned workers
  - Timezone inconsistency in logs (fixed 2025-11-24)
- **Database Migrations**:
  - Migration 015: Fixed timezone handling in container_logs
  - Migration 014: Added arbitrage_execution_logs table
  - Migration 013: (placeholder)
  - Migration 016: (this document) Parent container tracking
- **Code Locations**:
  - Container Runner: `internal/adapters/grpc/container_runner.go`
  - Daemon Server: `internal/adapters/grpc/daemon_server.go`
  - Arbitrage Coordinator: `internal/application/trading/commands/run_arbitrage_coordinator.go`
  - CLI Container Commands: `internal/adapters/cli/container.go`
  - Domain Model: `internal/domain/container/container.go`
  - Repository: `internal/adapters/persistence/container_repository.go`

---

## Approval Checklist

- [ ] Architecture review complete
- [ ] Security review (SQL injection, privilege escalation)
  - [ ] Check constraint prevents self-referencing
  - [ ] Partial index prevents performance degradation
  - [ ] Player ID scoping in all queries
- [ ] Performance review
  - [ ] Index on (parent_container_id, player_id) for fast child lookups
  - [ ] Recursive stop uses depth-first (stack friendly)
  - [ ] No N+1 queries in cascade logic
- [ ] Testing plan reviewed
  - [ ] BDD scenarios cover all edge cases
  - [ ] Integration tests validate real-world usage
  - [ ] Manual test plan provides clear verification steps
- [ ] Documentation updated
  - [ ] CLAUDE.md references new parent-child tracking
  - [ ] Migration guide for operators
  - [ ] Release notes drafted
- [ ] Ready for implementation

---

**Estimated Implementation Time**: 4-6 hours
- Phase 1 (Database): 30 minutes
- Phase 2 (Domain): 30 minutes
- Phase 3 (Repository): 1 hour
- Phase 4 (Cascading Stop): 1.5 hours
- Phase 5 (Coordinators): 30 minutes
- Phase 6 (List Filtering): 1 hour
- Testing & Verification: 1 hour

**Risk Level**: Medium
- Database schema change (reversible via migration down)
- Default behavior change in list command (may surprise users)
- Cascading stop adds complexity (but well-tested)

**Rollback Difficulty**: Low
- Migration down removes column cleanly
- Code rollback is straightforward
- No data corruption risk


# Ship Assignment Refactoring: Moving Assignments into Ship Aggregate

## Executive Summary

This document describes the refactoring of `ShipAssignment` from a separate domain entity in the `container` bounded context into a value object within the `Ship` aggregate in the `navigation` bounded context. This consolidation improves domain cohesion, simplifies the codebase, and eliminates the need for a separate `ShipAssignmentRepository`.

## Motivation

### Current Problems

1. **Scattered Domain Logic**: Ship assignment state is managed separately from the Ship entity, despite being intrinsically related to ship behavior.

2. **Dual Repository Pattern**: Handlers must inject and coordinate two repositories (`ShipRepository` + `ShipAssignmentRepository`) for operations that conceptually belong to a single aggregate.

3. **Data Synchronization**: Ships are fetched from the API while assignments live in the database, requiring manual coordination at the application layer.

4. **40+ Handler Dependencies**: The `ShipAssignmentRepository` is injected into over 40 handlers across contract, manufacturing, gas, and scouting domains.

### Benefits of Refactoring

1. **Single Source of Truth**: Ship aggregate owns its assignment state.
2. **Simplified Handler Code**: One repository call returns fully-hydrated ships.
3. **Improved Cohesion**: Assignment operations become Ship aggregate methods.
4. **Reduced Coupling**: Handlers no longer depend on `ShipAssignmentRepository`.

---

## Current Architecture

### Domain Model

```
┌─────────────────────────────────────────────────────────────────┐
│                    container bounded context                     │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ ShipAssignment (Entity)                                 │    │
│  │ - shipSymbol: string                                    │    │
│  │ - playerID: int                                         │    │
│  │ - containerID: string                                   │    │
│  │ - status: "active" | "idle"                             │    │
│  │ - assignedAt, releasedAt, releaseReason                 │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ ShipAssignmentRepository (Port)                         │    │
│  │ - Assign(ctx, assignment) error                         │    │
│  │ - FindByShip(ctx, symbol, playerID) (*ShipAssignment)   │    │
│  │ - Release(ctx, symbol, playerID, reason) error          │    │
│  │ - Transfer(ctx, symbol, from, to) error                 │    │
│  │ - ReleaseByContainer(ctx, containerID, playerID) error  │    │
│  │ - ReleaseAllActive(ctx, reason) (int, error)            │    │
│  │ - CountByContainerPrefix(ctx, prefix, playerID) (int)   │    │
│  └─────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│                   navigation bounded context                     │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ Ship (Aggregate Root)                                   │    │
│  │ - shipSymbol, playerID, currentLocation                 │    │
│  │ - fuel, cargo, navStatus, role, modules                 │    │
│  │ - (NO assignment field)                                 │    │
│  └─────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────┘
```

### Current Database Schema

```sql
-- ship_assignments table
CREATE TABLE ship_assignments (
    ship_symbol VARCHAR NOT NULL,
    player_id INT NOT NULL,
    container_id VARCHAR,
    status VARCHAR DEFAULT 'idle',
    assigned_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ,
    release_reason VARCHAR,
    PRIMARY KEY (ship_symbol, player_id),
    FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE,
    FOREIGN KEY (container_id, player_id) REFERENCES containers(id, player_id) ON DELETE SET NULL
);
```

### Current Handler Pattern

```go
type SomeHandler struct {
    shipRepo           navigation.ShipRepository
    shipAssignmentRepo container.ShipAssignmentRepository  // Extra dependency
}

func (h *SomeHandler) Handle(ctx context.Context, cmd Command) error {
    // Step 1: Fetch ship from API
    ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)

    // Step 2: Separately check assignment in DB
    assignment, err := h.shipAssignmentRepo.FindByShip(ctx, cmd.ShipSymbol, cmd.PlayerID.Value())

    // Step 3: Manual coordination
    if assignment == nil || assignment.Status() == "idle" {
        // Ship is available
    }

    // Step 4: Create and persist assignment separately
    newAssignment := container.NewShipAssignment(cmd.ShipSymbol, cmd.PlayerID.Value(), containerID, nil)
    h.shipAssignmentRepo.Assign(ctx, newAssignment)
}
```

---

## Target Architecture

### Domain Model

```
┌─────────────────────────────────────────────────────────────────┐
│                   navigation bounded context                     │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ ShipAssignment (Value Object)                           │    │
│  │ - containerID: string                                   │    │
│  │ - status: AssignmentStatus                              │    │
│  │ - assignedAt: time.Time                                 │    │
│  │ - releasedAt: *time.Time                                │    │
│  │ - releaseReason: *string                                │    │
│  └─────────────────────────────────────────────────────────┘    │
│                              ▲                                   │
│                              │ owned by                          │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ Ship (Aggregate Root)                                   │    │
│  │ - shipSymbol, playerID, currentLocation                 │    │
│  │ - fuel, cargo, navStatus, role, modules                 │    │
│  │ - assignment: *ShipAssignment  ◄── NEW                  │    │
│  │                                                         │    │
│  │ Methods:                                                │    │
│  │ - Assignment() *ShipAssignment                          │    │
│  │ - IsIdle() bool                                         │    │
│  │ - IsAssigned() bool                                     │    │
│  │ - ContainerID() string                                  │    │
│  │ - AssignToContainer(containerID, clock) error           │    │
│  │ - Release(reason, clock) error                          │    │
│  │ - TransferToContainer(newContainerID, clock) error      │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ ShipRepository (Collection Abstraction - DDD)           │    │
│  │ // Query methods (collection semantics)                 │    │
│  │ - FindBySymbol(ctx, symbol, playerID) (*Ship, error)    │    │
│  │ - FindAllByPlayer(ctx, playerID) ([]*Ship, error)       │    │
│  │ - FindByContainer(ctx, containerID, playerID) []*Ship   │    │
│  │ - FindIdleByPlayer(ctx, playerID) ([]*Ship, error)      │    │
│  │ - FindActiveByPlayer(ctx, playerID) ([]*Ship, error)    │    │
│  │ - CountByContainerPrefix(ctx, prefix, playerID) int     │    │
│  │                                                         │    │
│  │ // Persistence methods (collection semantics)           │    │
│  │ - Save(ctx, ship) error                                 │    │
│  │ - SaveAll(ctx, ships) error                             │    │
│  └─────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│                    container bounded context                     │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ ShipAssignmentManager (KEPT - In-Memory Locking)        │    │
│  │ - Used for runtime coordination between goroutines      │    │
│  │ - Not persisted, complements DB-backed assignments      │    │
│  └─────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────┘
```

### Target Database Schema

```sql
-- Renamed from ship_assignments to ships
CREATE TABLE ships (
    ship_symbol VARCHAR NOT NULL PRIMARY KEY,
    player_id INT NOT NULL,
    container_id VARCHAR,
    assignment_status VARCHAR DEFAULT 'idle',  -- Renamed from status
    assigned_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ,
    release_reason VARCHAR,
    FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE,
    FOREIGN KEY (container_id, player_id) REFERENCES containers(id, player_id) ON DELETE SET NULL
);

CREATE INDEX idx_ships_player ON ships(player_id);
CREATE INDEX idx_ships_container ON ships(container_id) WHERE container_id IS NOT NULL;
CREATE INDEX idx_ships_status ON ships(assignment_status);
```

### Target Handler Pattern (Load → Mutate → Save)

```go
type SomeHandler struct {
    shipRepo navigation.ShipRepository  // Single dependency
    clock    shared.Clock
}

func (h *SomeHandler) Handle(ctx context.Context, cmd Command) error {
    // Step 1: LOAD - Fetch ship with assignment pre-loaded
    ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)

    // Step 2: Check availability via aggregate method
    if ship.IsIdle() {
        // Step 3: MUTATE - Domain operation on aggregate (changes internal state)
        ship.AssignToContainer(containerID, h.clock)

        // Step 4: SAVE - Repository persists the changed aggregate
        err = h.shipRepo.Save(ctx, ship)
    }
}
```

**Key DDD Principle:** Repository is a collection abstraction (Save, Find). Domain operations
(AssignToContainer, Release, Transfer) are methods on the Ship aggregate that mutate internal state.

---

## Implementation Phases

### Phase 1: Create Value Object and Extend Ship

#### 1.1 Create ShipAssignment Value Object

**File:** `internal/domain/navigation/ship_assignment.go`

```go
package navigation

import "time"

type AssignmentStatus string

const (
    AssignmentStatusActive AssignmentStatus = "active"
    AssignmentStatusIdle   AssignmentStatus = "idle"
)

// ShipAssignment is a value object representing a ship's current container assignment.
// It is immutable - operations return new instances.
type ShipAssignment struct {
    containerID   string
    status        AssignmentStatus
    assignedAt    time.Time
    releasedAt    *time.Time
    releaseReason *string
}

func NewActiveAssignment(containerID string, assignedAt time.Time) *ShipAssignment {
    return &ShipAssignment{
        containerID: containerID,
        status:      AssignmentStatusActive,
        assignedAt:  assignedAt,
    }
}

func NewIdleAssignment() *ShipAssignment {
    return &ShipAssignment{
        status: AssignmentStatusIdle,
    }
}

func (a *ShipAssignment) ContainerID() string           { return a.containerID }
func (a *ShipAssignment) Status() AssignmentStatus      { return a.status }
func (a *ShipAssignment) IsActive() bool                { return a.status == AssignmentStatusActive }
func (a *ShipAssignment) AssignedAt() time.Time         { return a.assignedAt }
func (a *ShipAssignment) ReleasedAt() *time.Time        { return a.releasedAt }
func (a *ShipAssignment) ReleaseReason() *string        { return a.releaseReason }

// Released returns a new ShipAssignment in idle state
func (a *ShipAssignment) Released(reason string, releasedAt time.Time) *ShipAssignment {
    return &ShipAssignment{
        containerID:   "",
        status:        AssignmentStatusIdle,
        assignedAt:    a.assignedAt,
        releasedAt:    &releasedAt,
        releaseReason: &reason,
    }
}
```

#### 1.2 Extend Ship Aggregate

**File:** `internal/domain/navigation/ship.go` (additions)

```go
// Add field to Ship struct
type Ship struct {
    // ... existing fields ...
    assignment *ShipAssignment
}

// Add accessor methods
func (s *Ship) Assignment() *ShipAssignment {
    return s.assignment
}

func (s *Ship) IsIdle() bool {
    return s.assignment == nil || !s.assignment.IsActive()
}

func (s *Ship) IsAssigned() bool {
    return s.assignment != nil && s.assignment.IsActive()
}

func (s *Ship) ContainerID() string {
    if s.assignment == nil {
        return ""
    }
    return s.assignment.ContainerID()
}

// Add mutation methods (for domain logic, actual persistence via repository)
func (s *Ship) SetAssignment(assignment *ShipAssignment) {
    s.assignment = assignment
}
```

#### 1.3 Extend ShipRepository Port (Collection Abstraction)

**File:** `internal/domain/navigation/ports.go` (additions)

```go
// ShipRepository - collection abstraction with query and persistence methods
// NOTE: Domain operations (AssignToContainer, Release, Transfer) are on Ship aggregate, NOT here
type ShipRepository interface {
    ShipQueryRepository
    ShipCommandRepository
    ShipCargoRepository

    // NEW: Query methods (collection semantics)
    FindByContainer(ctx context.Context, containerID string, playerID shared.PlayerID) ([]*Ship, error)
    FindIdleByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*Ship, error)
    FindActiveByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*Ship, error)
    CountByContainerPrefix(ctx context.Context, prefix string, playerID shared.PlayerID) (int, error)

    // NEW: Persistence methods (collection semantics)
    Save(ctx context.Context, ship *Ship) error
    SaveAll(ctx context.Context, ships []*Ship) error
}
```

---

### Phase 2: Database Migration

**File:** `migrations/025_rename_ship_assignments_to_ships.up.sql`

```sql
-- Rename table
ALTER TABLE ship_assignments RENAME TO ships;

-- Rename status column to avoid confusion with nav_status
ALTER TABLE ships RENAME COLUMN status TO assignment_status;

-- Add index for common query patterns
CREATE INDEX IF NOT EXISTS idx_ships_assignment_status ON ships(assignment_status);
```

**File:** `migrations/025_rename_ship_assignments_to_ships.down.sql`

```sql
-- Revert column rename
ALTER TABLE ships RENAME COLUMN assignment_status TO status;

-- Revert table rename
ALTER TABLE ships RENAME TO ship_assignments;

-- Drop index
DROP INDEX IF EXISTS idx_ships_assignment_status;
```

---

### Phase 3: Implement Hybrid Repository

**File:** `internal/adapters/api/ship_repository.go` (modifications)

```go
type ShipRepository struct {
    apiClient        domainPorts.APIClient
    playerRepo       player.PlayerRepository
    waypointRepo     system.WaypointRepository
    waypointProvider system.IWaypointProvider
    db               *gorm.DB  // NEW: for assignment persistence
    shipListCache    sync.Map
}

func NewShipRepository(
    apiClient domainPorts.APIClient,
    playerRepo player.PlayerRepository,
    waypointRepo system.WaypointRepository,
    waypointProvider system.IWaypointProvider,
    db *gorm.DB,  // NEW parameter
) *ShipRepository {
    return &ShipRepository{
        apiClient:        apiClient,
        playerRepo:       playerRepo,
        waypointRepo:     waypointRepo,
        waypointProvider: waypointProvider,
        db:               db,
    }
}

// FindBySymbol now enriches with assignment
func (r *ShipRepository) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
    // 1. Fetch from API (existing logic)
    ship, err := r.fetchFromAPI(ctx, symbol, playerID)
    if err != nil {
        return nil, err
    }

    // 2. Enrich with assignment from DB
    if err := r.enrichWithAssignment(ctx, ship, playerID); err != nil {
        // Log warning but don't fail - assignment is optional
        log.Printf("Warning: failed to load assignment for %s: %v", symbol, err)
    }

    return ship, nil
}

// FindAllByPlayer batch loads assignments
func (r *ShipRepository) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
    // 1. Fetch all ships from API (existing logic with caching)
    ships, err := r.fetchAllFromAPI(ctx, playerID)
    if err != nil {
        return nil, err
    }

    // 2. Batch load assignments
    if err := r.batchEnrichWithAssignments(ctx, ships, playerID); err != nil {
        log.Printf("Warning: failed to batch load assignments: %v", err)
    }

    return ships, nil
}

// enrichWithAssignment loads assignment for a single ship
func (r *ShipRepository) enrichWithAssignment(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
    var model persistence.ShipModel
    err := r.db.WithContext(ctx).
        Where("ship_symbol = ? AND player_id = ?", ship.ShipSymbol(), playerID.Value()).
        First(&model).Error

    if errors.Is(err, gorm.ErrRecordNotFound) {
        return nil // No assignment exists
    }
    if err != nil {
        return err
    }

    assignment := r.modelToAssignment(&model)
    ship.SetAssignment(assignment)
    return nil
}

// batchEnrichWithAssignments loads assignments for multiple ships
func (r *ShipRepository) batchEnrichWithAssignments(ctx context.Context, ships []*navigation.Ship, playerID shared.PlayerID) error {
    if len(ships) == 0 {
        return nil
    }

    symbols := make([]string, len(ships))
    for i, s := range ships {
        symbols[i] = s.ShipSymbol()
    }

    var models []persistence.ShipModel
    err := r.db.WithContext(ctx).
        Where("ship_symbol IN ? AND player_id = ?", symbols, playerID.Value()).
        Find(&models).Error
    if err != nil {
        return err
    }

    // Build lookup map
    assignmentMap := make(map[string]*navigation.ShipAssignment)
    for _, m := range models {
        assignmentMap[m.ShipSymbol] = r.modelToAssignment(&m)
    }

    // Enrich ships
    for _, ship := range ships {
        if assignment, ok := assignmentMap[ship.ShipSymbol()]; ok {
            ship.SetAssignment(assignment)
        }
    }

    return nil
}

// Save persists ship aggregate (including assignment state)
func (r *ShipRepository) Save(ctx context.Context, ship *navigation.Ship) error {
    model := r.shipToModel(ship)
    return r.db.WithContext(ctx).
        Clauses(clause.OnConflict{
            Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
            DoUpdates: clause.AssignmentColumns([]string{
                "container_id", "assignment_status", "assigned_at", "released_at", "release_reason",
            }),
        }).
        Create(&model).Error
}

// SaveAll batch persists multiple ships
func (r *ShipRepository) SaveAll(ctx context.Context, ships []*navigation.Ship) error {
    if len(ships) == 0 {
        return nil
    }
    models := make([]persistence.ShipModel, len(ships))
    for i, ship := range ships {
        models[i] = r.shipToModel(ship)
    }
    return r.db.WithContext(ctx).
        Clauses(clause.OnConflict{
            Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
            DoUpdates: clause.AssignmentColumns([]string{
                "container_id", "assignment_status", "assigned_at", "released_at", "release_reason",
            }),
        }).
        Create(&models).Error
}

// Query methods
func (r *ShipRepository) FindActiveByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
    // Load from DB, enrich from API
    var models []persistence.ShipModel
    err := r.db.WithContext(ctx).
        Where("player_id = ? AND assignment_status = ?", playerID.Value(), "active").
        Find(&models).Error
    if err != nil {
        return nil, err
    }
    // Convert and enrich with API data...
}
```

---

### Phase 4: Migrate Handlers (Load → Mutate → Save)

Each handler migration follows this DDD pattern:

**Before (anti-pattern: domain ops on repository):**
```go
type Handler struct {
    shipRepo           navigation.ShipRepository
    shipAssignmentRepo container.ShipAssignmentRepository
}

func (h *Handler) Handle(ctx context.Context, cmd Command) error {
    ship, _ := h.shipRepo.FindBySymbol(ctx, symbol, playerID)
    assignment, _ := h.shipAssignmentRepo.FindByShip(ctx, symbol, playerID.Value())

    if assignment == nil || assignment.Status() == "idle" {
        newAssignment := container.NewShipAssignment(symbol, playerID.Value(), containerID, nil)
        h.shipAssignmentRepo.Assign(ctx, newAssignment)
    }
}
```

**After (DDD: domain ops on aggregate, repository just persists):**
```go
type Handler struct {
    shipRepo navigation.ShipRepository  // Single dependency
    clock    shared.Clock
}

func (h *Handler) Handle(ctx context.Context, cmd Command) error {
    // LOAD: Fetch ship with assignment pre-loaded
    ship, _ := h.shipRepo.FindBySymbol(ctx, symbol, playerID)

    if ship.IsIdle() {
        // MUTATE: Domain operation on aggregate
        ship.AssignToContainer(containerID, h.clock)

        // SAVE: Repository persists changed aggregate
        h.shipRepo.Save(ctx, ship)
    }
}
```

#### Handlers to Migrate (by domain)

**Contract Domain:**
- `ship_pool_manager.go` - Update `FindIdleLightHaulers()` signature
- `balance_ship_position.go`
- `rebalance_fleet.go`
- `fleet_pool_manager.go`

**Manufacturing Domain:**
- `task_assignment_manager.go`
- `manufacturing_coordinator.go`
- `pipeline_lifecycle_manager.go`
- `state_recovery_manager.go`
- `pipeline_recycler.go`
- `worker_lifecycle_manager.go`
- `run_factory_coordinator.go`
- `run_parallel_manufacturing_coordinator.go`

**Gas Domain:**
- `run_gas_coordinator.go`
- `run_siphon_worker.go`
- `run_gas_transport_worker.go`
- `run_storage_ship_worker.go`

**Scouting Domain:**
- `scout_markets.go`
- `assign_scouting_fleet.go`

**Shipyard:**
- `purchase_ship.go`

---

### Phase 5: Update Infrastructure

#### 5.1 Handler Registry

**File:** `internal/application/setup/handler_registry.go`

Remove `shipAssignmentRepo` from all handler constructors. Update `ShipRepository` creation to include `db` parameter.

#### 5.2 Daemon Server (Load → Mutate → Save for Bulk Operations)

**File:** `internal/adapters/grpc/daemon_server.go`

```go
// Before (anti-pattern: domain ops on repository)
type DaemonServer struct {
    shipRepo           navigation.ShipRepository
    shipAssignmentRepo container.ShipAssignmentRepository
}

func (s *DaemonServer) cleanupOnStartup() {
    s.shipAssignmentRepo.ReleaseAllActive(ctx, "daemon_restart")
}

// After (DDD: Load → Mutate → Save pattern)
type DaemonServer struct {
    shipRepo navigation.ShipRepository
    clock    shared.Clock
}

func (s *DaemonServer) cleanupOnStartup(ctx context.Context) {
    // LOAD: Query all active ships (collection query)
    ships, _ := s.shipRepo.FindActiveByPlayer(ctx, playerID)

    // MUTATE: Domain operation on each aggregate
    for _, ship := range ships {
        ship.Release("daemon_restart", s.clock)
    }

    // SAVE: Batch persist all changed aggregates
    s.shipRepo.SaveAll(ctx, ships)
}
```

---

### Phase 6: Cleanup

**Delete:**
- `internal/domain/container/ship_assignment.go` (entity definition)
- `internal/adapters/persistence/ship_assignment_repository.go`

**Modify:**
- `internal/domain/container/ports.go` - Remove `ShipAssignmentRepository` interface

**Keep:**
- `ShipAssignmentManager` in container context for in-memory runtime coordination

---

## Critical Files Reference

| File | Purpose | Change Type |
|------|---------|-------------|
| `internal/domain/navigation/ship.go` | Ship aggregate | Modify |
| `internal/domain/navigation/ports.go` | Repository interface | Modify |
| `internal/domain/navigation/ship_assignment.go` | Value object | Create |
| `internal/adapters/api/ship_repository.go` | Hybrid repository | Modify |
| `internal/adapters/persistence/models.go` | Database model | Modify |
| `internal/domain/container/ship_assignment.go` | Old entity | Delete |
| `internal/domain/container/ports.go` | Old interface | Modify |
| `internal/adapters/persistence/ship_assignment_repository.go` | Old repository | Delete |
| `internal/application/setup/handler_registry.go` | DI wiring | Modify |
| `internal/adapters/grpc/daemon_server.go` | Startup cleanup | Modify |
| `migrations/025_rename_ship_assignments_to_ships.up.sql` | Schema migration | Create |

---

## Migration Strategy

1. **Phase 1-3**: Can be deployed without breaking existing code (additive changes)
2. **Phase 4**: Migrate handlers incrementally, one domain at a time
3. **Phase 5-6**: Final cleanup after all handlers migrated

This allows for gradual rollout with the ability to rollback at each phase.

---

## Design Decisions

### Why Value Object (not Entity)?

ShipAssignment is now a value object because:
1. It has no independent lifecycle - it exists only as part of Ship
2. Identity is derived from Ship (shipSymbol + playerID)
3. Immutability simplifies concurrent access

### Why Keep ShipAssignmentManager?

The in-memory `ShipAssignmentManager` serves a different purpose:
- Runtime coordination between goroutines
- Fast lock checking without DB round-trips
- Complements (not replaces) DB-backed persistence

### Why Hybrid Repository?

Ships must be fetched from SpaceTraders API (source of truth for ship state), but assignment data lives in our database. The hybrid approach:
1. Fetches ship data from API
2. Enriches with assignment from DB
3. Returns fully-hydrated Ship aggregate

### Why Repository as Collection Abstraction (Not Domain Operations)?

**DDD Principle:** Repositories should act like in-memory collections with CRUD-like semantics:
- `Save(ship)` - Add/update item in collection
- `FindByX(...)` - Query items from collection
- `SaveAll(ships)` - Batch add/update

**Anti-Pattern Avoided:** Having domain operations on repositories breaks encapsulation:
```go
// BAD: Repository has domain logic
shipRepo.AssignToContainer(ctx, symbol, containerID, playerID)
shipRepo.Release(ctx, symbol, playerID, reason)

// GOOD: Domain operations on aggregate, repository just persists
ship.AssignToContainer(containerID, clock)  // Domain logic
shipRepo.Save(ctx, ship)                     // Persistence
```

This keeps domain logic in the aggregate where it belongs and makes the repository a pure infrastructure concern.

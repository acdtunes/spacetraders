# Ship Database as Source of Truth - Implementation Plan

## Executive Summary

This document describes the architectural changes required to make the database the source of truth for ships in the SpaceTraders Go bot. Currently, ships are fetched from the SpaceTraders API on every query. After this refactoring, ships will be synced from the API once on daemon startup, then all queries will read from the database. API calls will only be made for state-changing operations (navigate, dock, orbit, refuel, cargo), and those operations will persist the resulting state back to the database.

## Motivation

### Current Problems

1. **Rate Limiting**: Every `FindBySymbol` or `FindAllByPlayer` call hits the SpaceTraders API, consuming rate limit budget (2 req/sec).

2. **Redundant API Calls**: Multiple coordinators may request the same ship data within milliseconds, causing duplicate API calls.

3. **Stale Cache Issues**: The current 15-second in-memory cache is not persistent across daemon restarts and doesn't reflect state changes from other operations.

4. **No Offline Query Capability**: Cannot query ship state without network access to the API.

### Benefits of Refactoring

1. **Minimal API Calls**: Only sync on startup and for state-changing operations.
2. **Fast Queries**: Database reads are sub-millisecond vs 200-500ms API calls.
3. **Persistent State**: Ship state survives daemon restarts.
4. **Accurate Local State**: Background updaters keep arrival times and cooldowns current.
5. **Rate Limit Savings**: Reserve API budget for actual operations, not queries.

---

## Current Architecture

### Ship Domain Entity

**File:** `internal/domain/navigation/ship.go`

```
Ship (Aggregate Root)
├── shipSymbol: string (PK)
├── playerID: shared.PlayerID (PK)
├── currentLocation: *shared.Waypoint
├── fuel: *shared.Fuel (current, capacity)
├── fuelCapacity: int
├── cargoCapacity: int
├── cargo: *shared.Cargo (capacity, units, inventory[])
├── engineSpeed: int
├── frameSymbol: string
├── role: string
├── modules: []*ShipModule
├── navStatus: NavStatus (DOCKED, IN_ORBIT, IN_TRANSIT)
└── assignment: *ShipAssignment (container assignment - already persisted)
```

### Current ShipModel (Database)

**File:** `internal/adapters/persistence/models.go`

The current `ships` table (renamed from `ship_assignments` in migration 025) only stores assignment data:

```go
type ShipModel struct {
    ShipSymbol       string     // Primary key
    PlayerID         int        // Primary key
    ContainerID      *string    // Assigned container
    AssignmentStatus string     // "idle" or "active"
    AssignedAt       *time.Time
    ReleasedAt       *time.Time
    ReleaseReason    string
}
```

### Current Data Flow

```
┌─────────────┐     Every Query     ┌──────────────────┐
│   Handler   │ ──────────────────► │ SpaceTraders API │
└─────────────┘                     └──────────────────┘
       │                                    │
       │                                    ▼
       │                            ┌──────────────┐
       │                            │  API Response │
       │                            └──────────────┘
       │                                    │
       │                                    ▼
       │                            ┌──────────────────┐
       │                            │ shipDataToDomain │
       │                            └──────────────────┘
       │                                    │
       │         Enrich                     ▼
       │ ◄─────────────────────────  ┌──────────────┐
       │    (assignment only)        │  Ship Entity │
       │                             └──────────────┘
       ▼
┌─────────────┐
│  Database   │  (Only stores assignment state)
└─────────────┘
```

---

## Target Architecture

### Extended Ship Domain Entity

**File:** `internal/domain/navigation/ship.go`

```
Ship (Aggregate Root)
├── shipSymbol: string (PK)
├── playerID: shared.PlayerID (PK)
├── currentLocation: *shared.Waypoint
├── fuel: *shared.Fuel
├── fuelCapacity: int
├── cargoCapacity: int
├── cargo: *shared.Cargo
├── engineSpeed: int
├── frameSymbol: string
├── role: string
├── modules: []*ShipModule
├── navStatus: NavStatus
├── assignment: *ShipAssignment
│
│   NEW FIELDS (domain state only)
├── flightMode: string           ◄── Current flight mode (affects fuel consumption)
├── arrivalTime: *time.Time      ◄── When IN_TRANSIT ship will arrive (business: "when available?")
└── cooldownExpiration: *time.Time ◄── When cooldown expires (business: HasCooldown())
```

**NOT in domain entity** (repository-only, stored in DB but not exposed to domain):
- `syncedAt` - Infrastructure metadata (when last synced with API)
- `version` - Persistence metadata (for auditing)

### Extended ShipModel (Database)

**File:** `internal/adapters/persistence/models.go`

```go
type ShipModel struct {
    // Primary key
    ShipSymbol string
    PlayerID   int

    // Navigation state
    NavStatus   string     // DOCKED, IN_ORBIT, IN_TRANSIT
    FlightMode  string     // CRUISE, DRIFT, BURN, STEALTH
    ArrivalTime *time.Time // NULL unless IN_TRANSIT

    // Location (denormalized for quick reconstruction)
    LocationSymbol string
    LocationX      float64
    LocationY      float64
    SystemSymbol   string

    // Fuel
    FuelCurrent  int
    FuelCapacity int

    // Cargo (JSONB for full item details)
    CargoCapacity  int
    CargoUnits     int
    CargoInventory string // JSONB: [{symbol, name, description, units}]

    // Ship specifications
    EngineSpeed int
    FrameSymbol string
    Role        string
    Modules     string // JSONB: [{symbol, capacity, range}]

    // Cooldown
    CooldownExpiration *time.Time

    // Assignment (existing)
    ContainerID      *string
    AssignmentStatus string
    AssignedAt       *time.Time
    ReleasedAt       *time.Time
    ReleaseReason    string

    // Sync metadata
    SyncedAt time.Time
    Version  int
}
```

### Target Data Flow

```
                          DAEMON STARTUP
                               │
                               ▼
┌──────────────────┐    Full Sync    ┌──────────────┐
│ SpaceTraders API │ ◄─────────────► │   Database   │
└──────────────────┘                 └──────────────┘
                                            │
        ═══════════════════════════════════════════════════
                    RUNTIME (after startup)
        ═══════════════════════════════════════════════════
                                            │
                                            ▼
┌─────────────┐      DB Read         ┌──────────────┐
│   Handler   │ ◄────────────────────│   Database   │
└─────────────┘                      └──────────────┘
       │
       │  State-Changing Operation
       │  (navigate, dock, orbit, refuel, cargo)
       │
       ▼
┌──────────────────┐                 ┌──────────────┐
│ SpaceTraders API │ ───────────────►│   Database   │
└──────────────────┘   Persist       └──────────────┘
                       Result

        ═══════════════════════════════════════════════════
                    TIMER-BASED STATE TRANSITIONS
        ═══════════════════════════════════════════════════

┌────────────────────┐
│ ShipStateScheduler │
│                    │
│  Navigate() ──────►│──── time.AfterFunc(arrival_time) ────►│ Update DB │
│                    │                                         │ IN_ORBIT  │
│  Mining() ────────►│──── time.AfterFunc(cooldown_exp) ────►│ Clear     │
│                    │                                         │ cooldown  │
└────────────────────┘
    (zero CPU between events - no polling)
```

---

## Database Schema Changes

### Migration: `migrations/026_add_ship_state_columns.up.sql`

```sql
-- Migration: Add ship state columns for database-as-source-of-truth
-- This extends the ships table to store full ship state, not just assignments

-- Navigation state columns
ALTER TABLE ships ADD COLUMN IF NOT EXISTS nav_status VARCHAR(20) DEFAULT 'DOCKED';
ALTER TABLE ships ADD COLUMN IF NOT EXISTS flight_mode VARCHAR(20) DEFAULT 'CRUISE';
ALTER TABLE ships ADD COLUMN IF NOT EXISTS arrival_time TIMESTAMPTZ NULL;

-- Location columns (denormalized for quick access without waypoint join)
ALTER TABLE ships ADD COLUMN IF NOT EXISTS location_symbol VARCHAR(64);
ALTER TABLE ships ADD COLUMN IF NOT EXISTS location_x DOUBLE PRECISION DEFAULT 0;
ALTER TABLE ships ADD COLUMN IF NOT EXISTS location_y DOUBLE PRECISION DEFAULT 0;
ALTER TABLE ships ADD COLUMN IF NOT EXISTS system_symbol VARCHAR(32);

-- Fuel columns
ALTER TABLE ships ADD COLUMN IF NOT EXISTS fuel_current INT DEFAULT 0;
ALTER TABLE ships ADD COLUMN IF NOT EXISTS fuel_capacity INT DEFAULT 0;

-- Cargo columns (JSONB for full item details)
ALTER TABLE ships ADD COLUMN IF NOT EXISTS cargo_capacity INT DEFAULT 0;
ALTER TABLE ships ADD COLUMN IF NOT EXISTS cargo_units INT DEFAULT 0;
ALTER TABLE ships ADD COLUMN IF NOT EXISTS cargo_inventory JSONB DEFAULT '[]';

-- Ship specification columns
ALTER TABLE ships ADD COLUMN IF NOT EXISTS engine_speed INT DEFAULT 0;
ALTER TABLE ships ADD COLUMN IF NOT EXISTS frame_symbol VARCHAR(64);
ALTER TABLE ships ADD COLUMN IF NOT EXISTS role VARCHAR(32);
ALTER TABLE ships ADD COLUMN IF NOT EXISTS modules JSONB DEFAULT '[]';

-- Cooldown tracking
ALTER TABLE ships ADD COLUMN IF NOT EXISTS cooldown_expiration TIMESTAMPTZ NULL;

-- Sync metadata
ALTER TABLE ships ADD COLUMN IF NOT EXISTS synced_at TIMESTAMPTZ DEFAULT NOW();
ALTER TABLE ships ADD COLUMN IF NOT EXISTS version INT DEFAULT 1;

-- Indexes for common query patterns
CREATE INDEX IF NOT EXISTS idx_ships_nav_status ON ships(nav_status);
CREATE INDEX IF NOT EXISTS idx_ships_arrival_time ON ships(arrival_time)
    WHERE arrival_time IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ships_cooldown ON ships(cooldown_expiration)
    WHERE cooldown_expiration IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ships_location ON ships(location_symbol);
CREATE INDEX IF NOT EXISTS idx_ships_system ON ships(system_symbol);

COMMENT ON TABLE ships IS 'Full ship state persisted from API. Database is source of truth after initial sync.';
```

### Migration: `migrations/026_add_ship_state_columns.down.sql`

```sql
-- Rollback: Remove ship state columns

DROP INDEX IF EXISTS idx_ships_nav_status;
DROP INDEX IF EXISTS idx_ships_arrival_time;
DROP INDEX IF EXISTS idx_ships_cooldown;
DROP INDEX IF EXISTS idx_ships_location;
DROP INDEX IF EXISTS idx_ships_system;

ALTER TABLE ships DROP COLUMN IF EXISTS nav_status;
ALTER TABLE ships DROP COLUMN IF EXISTS flight_mode;
ALTER TABLE ships DROP COLUMN IF EXISTS arrival_time;
ALTER TABLE ships DROP COLUMN IF EXISTS location_symbol;
ALTER TABLE ships DROP COLUMN IF EXISTS location_x;
ALTER TABLE ships DROP COLUMN IF EXISTS location_y;
ALTER TABLE ships DROP COLUMN IF EXISTS system_symbol;
ALTER TABLE ships DROP COLUMN IF EXISTS fuel_current;
ALTER TABLE ships DROP COLUMN IF EXISTS fuel_capacity;
ALTER TABLE ships DROP COLUMN IF EXISTS cargo_capacity;
ALTER TABLE ships DROP COLUMN IF EXISTS cargo_units;
ALTER TABLE ships DROP COLUMN IF EXISTS cargo_inventory;
ALTER TABLE ships DROP COLUMN IF EXISTS engine_speed;
ALTER TABLE ships DROP COLUMN IF EXISTS frame_symbol;
ALTER TABLE ships DROP COLUMN IF EXISTS role;
ALTER TABLE ships DROP COLUMN IF EXISTS modules;
ALTER TABLE ships DROP COLUMN IF EXISTS cooldown_expiration;
ALTER TABLE ships DROP COLUMN IF EXISTS synced_at;
ALTER TABLE ships DROP COLUMN IF EXISTS version;
```

---

## Implementation Details

### Phase 1: Database Schema and Models

#### 1.1 Create Migration Files

Create `migrations/026_add_ship_state_columns.up.sql` and `.down.sql` as shown above.

#### 1.2 Update ShipModel

**File:** `internal/adapters/persistence/models.go`

```go
// ShipModel represents the ships table
// This stores complete ship state that is the source of truth after daemon startup
type ShipModel struct {
    // Primary key fields
    ShipSymbol string       `gorm:"column:ship_symbol;primaryKey;not null"`
    PlayerID   int          `gorm:"column:player_id;primaryKey;not null"`
    Player     *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`

    // Navigation state
    NavStatus   string     `gorm:"column:nav_status;default:'DOCKED'"`
    FlightMode  string     `gorm:"column:flight_mode;default:'CRUISE'"`
    ArrivalTime *time.Time `gorm:"column:arrival_time"`

    // Location (denormalized)
    LocationSymbol string  `gorm:"column:location_symbol"`
    LocationX      float64 `gorm:"column:location_x;default:0"`
    LocationY      float64 `gorm:"column:location_y;default:0"`
    SystemSymbol   string  `gorm:"column:system_symbol"`

    // Fuel
    FuelCurrent  int `gorm:"column:fuel_current;default:0"`
    FuelCapacity int `gorm:"column:fuel_capacity;default:0"`

    // Cargo
    CargoCapacity  int    `gorm:"column:cargo_capacity;default:0"`
    CargoUnits     int    `gorm:"column:cargo_units;default:0"`
    CargoInventory string `gorm:"column:cargo_inventory;type:jsonb;default:'[]'"`

    // Ship specifications
    EngineSpeed int    `gorm:"column:engine_speed;default:0"`
    FrameSymbol string `gorm:"column:frame_symbol"`
    Role        string `gorm:"column:role"`
    Modules     string `gorm:"column:modules;type:jsonb;default:'[]'"`

    // Cooldown
    CooldownExpiration *time.Time `gorm:"column:cooldown_expiration"`

    // Assignment (existing)
    ContainerID      *string         `gorm:"column:container_id"`
    Container        *ContainerModel `gorm:"foreignKey:ContainerID,PlayerID;references:ID,PlayerID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`
    AssignmentStatus string          `gorm:"column:assignment_status;default:'idle'"`
    AssignedAt       *time.Time      `gorm:"column:assigned_at"`
    ReleasedAt       *time.Time      `gorm:"column:released_at"`
    ReleaseReason    string          `gorm:"column:release_reason"`

    // Sync metadata
    SyncedAt time.Time `gorm:"column:synced_at;default:now()"`
    Version  int       `gorm:"column:version;default:1"`
}

// JSON helper types for JSONB columns
type CargoItemJSON struct {
    Symbol      string `json:"symbol"`
    Name        string `json:"name"`
    Description string `json:"description"`
    Units       int    `json:"units"`
}

type ModuleJSON struct {
    Symbol   string `json:"symbol"`
    Capacity int    `json:"capacity"`
    Range    int    `json:"range"`
}
```

---

### Phase 2: Domain Entity Updates

#### 2.1 Add New Fields to Ship

**File:** `internal/domain/navigation/ship.go`

```go
type Ship struct {
    // Existing fields
    shipSymbol      string
    playerID        shared.PlayerID
    currentLocation *shared.Waypoint
    fuel            *shared.Fuel
    fuelCapacity    int
    cargoCapacity   int
    cargo           *shared.Cargo
    engineSpeed     int
    frameSymbol     string
    role            string
    modules         []*ShipModule
    navStatus       NavStatus
    assignment      *ShipAssignment
    fuelService     *ShipFuelService
    navigationCalc  *ShipNavigationCalculator

    // NEW: DB-as-source-of-truth fields
    flightMode         string
    arrivalTime        *time.Time
    cooldownExpiration *time.Time
    syncedAt           time.Time
    version            int
}
```

#### 2.2 Add New Methods

```go
// Getters for new fields
func (s *Ship) FlightMode() string             { return s.flightMode }
func (s *Ship) ArrivalTime() *time.Time        { return s.arrivalTime }
func (s *Ship) CooldownExpiration() *time.Time { return s.cooldownExpiration }

// Setters for repository use (domain state only - no infrastructure metadata)
func (s *Ship) SetFlightMode(mode string)      { s.flightMode = mode }
func (s *Ship) SetArrivalTime(t time.Time)     { s.arrivalTime = &t }
func (s *Ship) ClearArrivalTime()              { s.arrivalTime = nil }
func (s *Ship) SetCooldown(t time.Time)        { s.cooldownExpiration = &t }
func (s *Ship) ClearCooldown()                 { s.cooldownExpiration = nil }
func (s *Ship) SetCargo(c *shared.Cargo)       { s.cargo = c }
func (s *Ship) SetLocation(w *shared.Waypoint) { s.currentLocation = w }

// NOTE: No IncrementVersion(), SetSyncedAt(), SyncedAt(), Version()
// These are infrastructure concerns handled by the repository implementation

// Query methods
func (s *Ship) HasCooldown() bool {
    return s.cooldownExpiration != nil && time.Now().Before(*s.cooldownExpiration)
}

func (s *Ship) CooldownRemaining() time.Duration {
    if s.cooldownExpiration == nil {
        return 0
    }
    remaining := time.Until(*s.cooldownExpiration)
    if remaining < 0 {
        return 0
    }
    return remaining
}
```

#### 2.3 Add Reconstruction Constructor

```go
// ReconstructShip creates a Ship from persisted state (used by repository)
// NOTE: syncedAt and version are NOT passed - they are infrastructure concerns
func ReconstructShip(
    shipSymbol string,
    playerID shared.PlayerID,
    currentLocation *shared.Waypoint,
    fuel *shared.Fuel,
    fuelCapacity int,
    cargoCapacity int,
    cargo *shared.Cargo,
    engineSpeed int,
    frameSymbol string,
    role string,
    modules []*ShipModule,
    navStatus NavStatus,
    flightMode string,
    arrivalTime *time.Time,
    cooldownExpiration *time.Time,
    assignment *ShipAssignment,
) (*Ship, error) {
    s := &Ship{
        shipSymbol:         shipSymbol,
        playerID:           playerID,
        currentLocation:    currentLocation,
        fuel:               fuel,
        fuelCapacity:       fuelCapacity,
        cargoCapacity:      cargoCapacity,
        cargo:              cargo,
        engineSpeed:        engineSpeed,
        frameSymbol:        frameSymbol,
        role:               role,
        modules:            modules,
        navStatus:          navStatus,
        flightMode:         flightMode,
        arrivalTime:        arrivalTime,
        cooldownExpiration: cooldownExpiration,
        assignment:         assignment,
        fuelService:        NewShipFuelService(),
        navigationCalc:     NewShipNavigationCalculator(),
    }

    if err := s.validate(); err != nil {
        return nil, err
    }

    return s, nil
}
```

---

### Phase 3: Repository Refactoring

#### 3.1 New Repository Interface Methods

**File:** `internal/domain/navigation/ports.go`

Add to `ShipRepository` interface:

```go
type ShipRepository interface {
    ShipQueryRepository
    ShipCommandRepository
    ShipCargoRepository

    // Existing assignment methods
    FindByContainer(ctx context.Context, containerID string, playerID shared.PlayerID) ([]*Ship, error)
    FindIdleByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*Ship, error)
    FindActiveByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*Ship, error)
    CountByContainerPrefix(ctx context.Context, prefix string, playerID shared.PlayerID) (int, error)
    Save(ctx context.Context, ship *Ship) error
    SaveAll(ctx context.Context, ships []*Ship) error
    ReleaseAllActive(ctx context.Context, reason string) (int, error)

    // NEW: Sync methods
    SyncAllFromAPI(ctx context.Context, playerID shared.PlayerID) (int, error)
    SyncShipFromAPI(ctx context.Context, symbol string, playerID shared.PlayerID) (*Ship, error)

    // NEW: Background updater queries
    FindInTransitWithPastArrival(ctx context.Context) ([]*Ship, error)
    FindWithExpiredCooldown(ctx context.Context) ([]*Ship, error)
}
```

#### 3.2 DB-First Query Implementation

**File:** `internal/adapters/api/ship_repository.go`

```go
// FindBySymbol reads from database first, syncs from API if not found
func (r *ShipRepository) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
    var model persistence.ShipModel
    err := r.db.WithContext(ctx).
        Where("ship_symbol = ? AND player_id = ?", symbol, playerID.Value()).
        First(&model).Error

    if errors.Is(err, gorm.ErrRecordNotFound) {
        // Ship not in DB - might be newly purchased, sync from API
        return r.SyncShipFromAPI(ctx, symbol, playerID)
    }
    if err != nil {
        return nil, fmt.Errorf("failed to query ship: %w", err)
    }

    return r.modelToDomain(ctx, &model)
}

// FindAllByPlayer reads all ships from database
func (r *ShipRepository) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
    var models []persistence.ShipModel
    err := r.db.WithContext(ctx).
        Where("player_id = ?", playerID.Value()).
        Find(&models).Error
    if err != nil {
        return nil, fmt.Errorf("failed to query ships: %w", err)
    }

    ships := make([]*navigation.Ship, 0, len(models))
    for _, model := range models {
        ship, err := r.modelToDomain(ctx, &model)
        if err != nil {
            log.Printf("Warning: failed to convert ship %s: %v", model.ShipSymbol, err)
            continue
        }
        ships = append(ships, ship)
    }

    return ships, nil
}
```

#### 3.3 Sync Methods

```go
// SyncAllFromAPI fetches all ships from API and upserts to database
func (r *ShipRepository) SyncAllFromAPI(ctx context.Context, playerID shared.PlayerID) (int, error) {
    player, err := r.playerRepo.FindByID(ctx, playerID)
    if err != nil {
        return 0, fmt.Errorf("failed to get player: %w", err)
    }

    // Fetch all ships from API
    shipsData, err := r.apiClient.ListShips(ctx, player.Token)
    if err != nil {
        return 0, fmt.Errorf("failed to list ships from API: %w", err)
    }

    now := r.clock.Now()
    models := make([]persistence.ShipModel, 0, len(shipsData))

    for _, data := range shipsData {
        model, err := r.shipDataToModel(ctx, data, playerID, now)
        if err != nil {
            log.Printf("Warning: failed to convert ship %s: %v", data.Symbol, err)
            continue
        }
        models = append(models, *model)
    }

    // Batch upsert all ships
    if len(models) > 0 {
        err = r.db.WithContext(ctx).
            Clauses(clause.OnConflict{
                Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
                UpdateAll: true,
            }).
            Create(&models).Error
        if err != nil {
            return 0, fmt.Errorf("failed to upsert ships: %w", err)
        }
    }

    return len(models), nil
}

// SyncShipFromAPI fetches a single ship from API and persists to database
func (r *ShipRepository) SyncShipFromAPI(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
    player, err := r.playerRepo.FindByID(ctx, playerID)
    if err != nil {
        return nil, err
    }

    // Fetch from API
    shipData, err := r.apiClient.GetShip(ctx, symbol, player.Token)
    if err != nil {
        return nil, err
    }

    // Convert to model and persist
    now := r.clock.Now()
    model, err := r.shipDataToModel(ctx, shipData, playerID, now)
    if err != nil {
        return nil, err
    }

    err = r.db.WithContext(ctx).
        Clauses(clause.OnConflict{
            Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
            UpdateAll: true,
        }).
        Create(model).Error
    if err != nil {
        return nil, fmt.Errorf("failed to persist ship: %w", err)
    }

    return r.modelToDomain(ctx, model)
}
```

#### 3.4 State-Changing Operations Pattern

All state-changing operations follow this pattern:
1. Call API first
2. On success, update domain entity
3. Persist to database

```go
// Navigate executes navigation via API and persists state
func (r *ShipRepository) Navigate(ctx context.Context, ship *navigation.Ship, destination *shared.Waypoint, playerID shared.PlayerID) (*navigation.Result, error) {
    player, err := r.playerRepo.FindByID(ctx, playerID)
    if err != nil {
        return nil, err
    }

    // 1. Call API FIRST
    navResult, err := r.apiClient.NavigateShip(ctx, ship.ShipSymbol(), destination.Symbol, player.Token)
    if err != nil {
        return nil, err // Don't touch DB on API failure
    }

    // 2. Update domain entity
    if err := ship.StartTransit(destination); err != nil {
        return nil, err
    }
    if err := ship.ConsumeFuel(navResult.FuelConsumed); err != nil {
        return nil, err
    }

    ship.SetFlightMode(navResult.FlightMode)
    if arrivalTime, err := time.Parse(time.RFC3339, navResult.ArrivalTimeStr); err == nil {
        ship.SetArrivalTime(arrivalTime)
    }
    // 3. Persist to database with retry (MUST succeed after API success)
    if err := r.saveWithRetry(ctx, ship, 3); err != nil {
        log.Printf("CRITICAL: failed to persist ship %s after navigate: %v", ship.ShipSymbol(), err)
        r.queueForRecovery(ship) // Background recovery will retry
    }

    // 4. Schedule arrival timer
    r.scheduler.ScheduleArrival(ship)

    return navResult, nil
}

// Similar pattern for Dock, Orbit, Refuel, SetFlightMode, cargo operations...
```

#### 3.5 Background Updater Queries

```go
// FindInTransitWithPastArrival finds ships that should have arrived
func (r *ShipRepository) FindInTransitWithPastArrival(ctx context.Context) ([]*navigation.Ship, error) {
    var models []persistence.ShipModel
    err := r.db.WithContext(ctx).
        Where("nav_status = ?", "IN_TRANSIT").
        Where("arrival_time IS NOT NULL").
        Where("arrival_time <= ?", r.clock.Now()).
        Find(&models).Error
    if err != nil {
        return nil, err
    }

    ships := make([]*navigation.Ship, 0, len(models))
    for _, model := range models {
        ship, err := r.modelToDomain(ctx, &model)
        if err != nil {
            continue
        }
        ships = append(ships, ship)
    }
    return ships, nil
}

// FindWithExpiredCooldown finds ships with past cooldowns
func (r *ShipRepository) FindWithExpiredCooldown(ctx context.Context) ([]*navigation.Ship, error) {
    var models []persistence.ShipModel
    err := r.db.WithContext(ctx).
        Where("cooldown_expiration IS NOT NULL").
        Where("cooldown_expiration <= ?", r.clock.Now()).
        Find(&models).Error
    if err != nil {
        return nil, err
    }

    ships := make([]*navigation.Ship, 0, len(models))
    for _, model := range models {
        ship, err := r.modelToDomain(ctx, &model)
        if err != nil {
            continue
        }
        ships = append(ships, ship)
    }
    return ships, nil
}
```

---

### Phase 4: Daemon Integration

#### 4.1 Startup Sync

**File:** `internal/adapters/grpc/daemon_server.go`

```go
func (s *DaemonServer) Start() error {
    ctx := context.Background()

    // Existing: Release zombie assignments
    if s.shipRepo != nil {
        count, err := s.shipRepo.ReleaseAllActive(ctx, "daemon_restart")
        if err != nil {
            log.Printf("Warning: failed to release active assignments: %v", err)
        } else if count > 0 {
            log.Printf("Released %d zombie ship assignments", count)
        }
    }

    // NEW: Sync all ships from API to database
    if err := s.syncAllShipsOnStartup(); err != nil {
        log.Printf("Warning: Ship startup sync failed: %v", err)
        // Continue - we can still operate with stale data
    }

    // NEW: Schedule timers for pending arrivals and cooldowns
    if err := s.scheduler.ScheduleAllPending(ctx); err != nil {
        log.Printf("Warning: Failed to schedule pending state transitions: %v", err)
    }

    // ... rest of existing Start() code
}

func (s *DaemonServer) syncAllShipsOnStartup() error {
    ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
    defer cancel()

    players, err := s.playerRepo.ListAll(ctx)
    if err != nil {
        return fmt.Errorf("failed to list players: %w", err)
    }

    totalSynced := 0
    for _, player := range players {
        playerID, _ := shared.NewPlayerID(player.ID)
        count, err := s.shipRepo.SyncAllFromAPI(ctx, playerID)
        if err != nil {
            log.Printf("Warning: Failed to sync ships for player %s: %v", player.AgentSymbol, err)
            continue
        }
        totalSynced += count
        log.Printf("Synced %d ships for player %s", count, player.AgentSymbol)
    }

    log.Printf("Ship startup sync complete: %d total ships synced", totalSynced)
    return nil
}
```

#### 4.2 Timer-Based State Updater

Instead of polling, we schedule precise timers for state transitions since the API provides exact timestamps.

```go
// ShipStateScheduler manages timers for ship state transitions
type ShipStateScheduler struct {
    shipRepo    navigation.ShipRepository
    clock       shared.Clock
    timers      map[string]*time.Timer // key: shipSymbol
    timersMutex sync.Mutex
    done        chan struct{}
}

func NewShipStateScheduler(shipRepo navigation.ShipRepository, clock shared.Clock) *ShipStateScheduler {
    return &ShipStateScheduler{
        shipRepo: shipRepo,
        clock:    clock,
        timers:   make(map[string]*time.Timer),
        done:     make(chan struct{}),
    }
}

// ClockDriftBuffer accounts for slight time differences between API server and local clock.
// Ensures we never act before the API considers the ship arrived.
const ClockDriftBuffer = 1 * time.Second

// ScheduleArrival schedules a timer to transition ship from IN_TRANSIT to IN_ORBIT
func (s *ShipStateScheduler) ScheduleArrival(ship *navigation.Ship) {
    if ship.ArrivalTime() == nil {
        return
    }

    delay := time.Until(*ship.ArrivalTime())
    if delay < 0 {
        delay = 0 // Already past, execute immediately
    }
    delay += ClockDriftBuffer // Buffer for clock drift between API server and local

    s.timersMutex.Lock()
    defer s.timersMutex.Unlock()

    // Cancel existing timer if any
    if existing, ok := s.timers[ship.ShipSymbol()]; ok {
        existing.Stop()
    }

    symbol := ship.ShipSymbol()
    playerID := ship.PlayerID()

    s.timers[symbol] = time.AfterFunc(delay, func() {
        ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()

        // Re-fetch ship to get latest state
        freshShip, err := s.shipRepo.FindBySymbol(ctx, symbol, playerID)
        if err != nil {
            log.Printf("Warning: Failed to fetch ship %s for arrival: %v", symbol, err)
            return
        }

        // Only transition if still in transit
        if !freshShip.IsInTransit() {
            return
        }

        if err := freshShip.Arrive(); err != nil {
            log.Printf("Warning: Failed to transition ship %s to orbit: %v", symbol, err)
            return
        }

        freshShip.ClearArrivalTime()

        if err := s.shipRepo.Save(ctx, freshShip); err != nil {
            log.Printf("Warning: Failed to save ship %s after arrival: %v", symbol, err)
        } else {
            log.Printf("Ship %s arrived at %s", symbol, freshShip.CurrentLocation().Symbol)
        }

        s.timersMutex.Lock()
        delete(s.timers, symbol)
        s.timersMutex.Unlock()
    })

    log.Printf("Scheduled arrival for %s in %v", symbol, delay)
}

// ScheduleCooldownClear schedules a timer to clear cooldown
func (s *ShipStateScheduler) ScheduleCooldownClear(ship *navigation.Ship) {
    if ship.CooldownExpiration() == nil {
        return
    }

    delay := time.Until(*ship.CooldownExpiration())
    if delay < 0 {
        delay = 0
    }
    delay += ClockDriftBuffer // Buffer for clock drift

    symbol := ship.ShipSymbol()
    playerID := ship.PlayerID()
    timerKey := symbol + ":cooldown"

    s.timersMutex.Lock()
    defer s.timersMutex.Unlock()

    if existing, ok := s.timers[timerKey]; ok {
        existing.Stop()
    }

    s.timers[timerKey] = time.AfterFunc(delay, func() {
        ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()

        freshShip, err := s.shipRepo.FindBySymbol(ctx, symbol, playerID)
        if err != nil {
            return
        }

        freshShip.ClearCooldown()
        s.shipRepo.Save(ctx, freshShip)

        s.timersMutex.Lock()
        delete(s.timers, timerKey)
        s.timersMutex.Unlock()
    })
}

// ScheduleAllPending schedules timers for all ships with pending arrivals/cooldowns
// Called on daemon startup after syncing ships from API
func (s *ShipStateScheduler) ScheduleAllPending(ctx context.Context) error {
    // Schedule arrivals for in-transit ships
    inTransitShips, err := s.shipRepo.FindInTransitWithFutureArrival(ctx)
    if err != nil {
        return err
    }
    for _, ship := range inTransitShips {
        s.ScheduleArrival(ship)
    }

    // Schedule cooldown clears
    shipsWithCooldown, err := s.shipRepo.FindWithFutureCooldown(ctx)
    if err != nil {
        return err
    }
    for _, ship := range shipsWithCooldown {
        s.ScheduleCooldownClear(ship)
    }

    return nil
}

// CancelAll cancels all pending timers (for graceful shutdown)
func (s *ShipStateScheduler) CancelAll() {
    s.timersMutex.Lock()
    defer s.timersMutex.Unlock()

    for key, timer := range s.timers {
        timer.Stop()
        delete(s.timers, key)
    }
}
```

#### 4.3 Repository Handles Locking Internally

The domain interface stays clean - no locking semantics exposed:

```go
// Domain interface - implementation-agnostic
type ShipRepository interface {
    FindBySymbol(ctx, symbol, playerID) (*Ship, error)
    Save(ctx, ship) error
    // ...
}
```

The persistence implementation handles locking internally:

```go
// In adapters/persistence - uses transactions with row locking
func (r *ShipRepository) Save(ctx context.Context, ship *Ship) error {
    model := r.domainToModel(ship)

    return r.db.Transaction(func(tx *gorm.DB) error {
        return tx.Clauses(clause.Locking{Strength: "UPDATE"}).
            Where("ship_symbol = ? AND player_id = ?",
                ship.ShipSymbol(), ship.PlayerID().Value()).
            Save(&model).Error
    })
}
```

Timer callbacks use the clean interface:

```go
s.timers[symbol] = time.AfterFunc(delay, func() {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    freshShip, err := s.shipRepo.FindBySymbol(ctx, symbol, playerID)
    if err != nil {
        log.Printf("Warning: Failed to fetch ship %s: %v", symbol, err)
        return
    }

    if !freshShip.IsInTransit() {
        return // Already transitioned
    }

    freshShip.Arrive()
    freshShip.ClearArrivalTime()

    if err := s.shipRepo.Save(ctx, freshShip); err != nil {
        log.Printf("Warning: Failed to save ship %s: %v", symbol, err)
    }
})
```

#### 4.4 Integrate Scheduler with Repository

When `Navigate()` is called and succeeds, schedule the arrival:

```go
func (r *ShipRepository) Navigate(ctx context.Context, ship *Ship, destination *Waypoint, playerID PlayerID) (*Result, error) {
    // ... API call and entity update ...

    if err := r.Save(ctx, ship); err != nil {
        log.Printf("Warning: failed to persist: %v", err)
    }

    // Schedule arrival timer
    if ship.ArrivalTime() != nil {
        r.scheduler.ScheduleArrival(ship)
    }

    return navResult, nil
}
```

Similarly for cooldown-producing operations (mining, surveying, etc.).
```

---

### Phase 5: Cargo Handler Updates

#### 5.1 Persist Cargo Changes After Operations

**File:** `internal/application/ship/commands/cargo/sell_cargo.go`

After successful cargo sale:

```go
func (h *SellCargoHandler) Handle(ctx context.Context, cmd *SellCargoCommand) (*SellCargoResponse, error) {
    // ... existing code to execute sale via API ...

    // After successful API call, update cargo and persist
    newCargo, err := h.buildUpdatedCargo(ship.Cargo(), cmd.GoodSymbol, cmd.Units)
    if err != nil {
        return nil, err
    }

    ship.SetCargo(newCargo)

    if err := h.shipRepo.Save(ctx, ship); err != nil {
        log.Printf("Warning: failed to persist cargo change: %v", err)
    }

    // ... rest of handler
}
```

Similar updates for `purchase_cargo.go` and `jettison_cargo.go`.

---

## Resilience Patterns

### 1. API-First for Mutations

All state-changing operations call the API first. Only on API success do we update the database. This ensures:
- Database never has state that the API doesn't know about
- API failures don't corrupt local state
- Race conditions between API and DB are minimized

### 2. Retry on DB Persist Failure

If the API call succeeds, DB persist MUST succeed to avoid state drift:

```go
func (r *ShipRepository) saveWithRetry(ctx context.Context, ship *Ship, maxRetries int) error {
    var lastErr error
    for i := 0; i < maxRetries; i++ {
        if err := r.Save(ctx, ship); err == nil {
            return nil
        } else {
            lastErr = err
            backoff := time.Duration(i+1) * 100 * time.Millisecond
            time.Sleep(backoff)
        }
    }
    return lastErr
}
```

If retries exhausted:
- Log CRITICAL error
- Queue ship for background recovery sync
- Background goroutine periodically retries failed persists

### 3. Timer-Based State Transitions

- **ShipStateScheduler**: Schedules precise timers using `time.AfterFunc` for exact arrival/cooldown times
- **No polling**: Zero CPU usage between events
- **On daemon restart**: Query DB for pending arrivals/cooldowns, schedule timers for each

### 4. Pessimistic Transaction Locking

Race conditions are handled via `SELECT ... FOR UPDATE` within transactions:

- Lock the row when reading, hold lock until commit
- Simple and robust, even with low contention
- No retry logic needed

### 5. Daemon Restart Full Sync

On every daemon startup:
- Full sync from API fetches current state of all ships
- This corrects any drift between DB and API
- Acts as a "self-healing" mechanism

### 6. Version Column

The `version` column is kept for debugging/auditing:
- Incremented on every save
- Useful for tracking how many times a ship was modified
- No enforcement (pessimistic locking handles concurrency)

---

## Implementation Order

### Week 1: Foundation
1. Create migration `026_add_ship_state_columns.up.sql` and `.down.sql`
2. Update `ShipModel` with new fields
3. Add JSON helper types (`CargoItemJSON`, `ModuleJSON`)
4. Add new fields to Ship domain entity
5. Add new methods to Ship (getters, setters, `HasCooldown`)
6. Add `ReconstructShip` constructor

### Week 2: Repository Refactoring
1. Add new interface methods to `ShipRepository`
2. Implement `modelToDomain` conversion (DB model → domain entity)
3. Implement `domainToModel` conversion (domain entity → DB model)
4. Implement `shipDataToModel` conversion (API DTO → DB model)
5. Refactor `FindBySymbol` to read from DB first
6. Refactor `FindAllByPlayer` to read from DB
7. Implement `SyncAllFromAPI` and `SyncShipFromAPI`
8. Implement `FindInTransitWithPastArrival` and `FindWithExpiredCooldown`
9. Update all state-changing methods (Navigate, Dock, Orbit, Refuel, SetFlightMode)

### Week 3: Daemon Integration
1. Add `syncAllShipsOnStartup()` to daemon
2. Add `runTransitArrivalUpdater()` goroutine
3. Add `runCooldownCleaner()` goroutine
4. Add graceful shutdown for background goroutines
5. Update cargo handlers to persist cargo changes

### Week 4: Testing and Validation
1. Unit tests for conversion methods
2. Integration tests for sync behavior
3. Test daemon restart recovery
4. Verify background updaters work correctly
5. End-to-end testing of all workflows
6. Performance testing (DB read times vs API)

---

## Critical Files Summary

| File | Change Type | Description |
|------|-------------|-------------|
| `migrations/026_add_ship_state_columns.up.sql` | Create | Database schema extension |
| `migrations/026_add_ship_state_columns.down.sql` | Create | Rollback migration |
| `internal/adapters/persistence/models.go` | Modify | Extend ShipModel with ~20 new fields |
| `internal/domain/navigation/ship.go` | Modify | Add 5 new fields, ~15 new methods |
| `internal/domain/navigation/ports.go` | Modify | Add new interface methods |
| `internal/adapters/api/ship_repository.go` | Modify | Major refactor to DB-first reads |
| `internal/adapters/grpc/daemon_server.go` | Modify | Add startup sync, background updaters |
| `internal/application/ship/commands/cargo/sell_cargo.go` | Modify | Persist cargo changes |
| `internal/application/ship/commands/cargo/purchase_cargo.go` | Modify | Persist cargo changes |

---

## Design Decisions

### Why Denormalized Location?

We store `location_symbol`, `location_x`, `location_y`, `system_symbol` directly in the ships table instead of just referencing the waypoints table because:

1. **Fast Reconstruction**: Can rebuild Ship entity without joining waypoints table
2. **Cold Start**: Works even if waypoint cache is empty
3. **Simplicity**: Single table query for ship state
4. **Trade-off**: Slight data duplication (~100 bytes per ship)

### Why JSONB for Cargo/Modules?

1. **Flexibility**: Schema can evolve without migrations
2. **Full Details**: Stores complete item info (symbol, name, description, units)
3. **Query Capability**: PostgreSQL JSONB supports indexing and querying
4. **Trade-off**: Slightly more complex serialization/deserialization

### Why Timer-Based Transitions Instead of Lazy Updates?

1. **Accuracy**: Ship state transitions at the exact moment (not on next query)
2. **Efficiency**: Zero CPU between events (no polling)
3. **Simplicity**: Queries don't need to check/update state
4. **Predictability**: State transitions happen at exact API-provided timestamps

### Why Single Daemon Assumption?

1. **Simpler Concurrency**: No distributed locking needed
2. **In-Memory Coordination**: Assignment manager works correctly
3. **Version Column**: Provides safety net for race conditions
4. **Future-Proof**: Can add distributed locking later if needed

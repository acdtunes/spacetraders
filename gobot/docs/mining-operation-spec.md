# Mining Operation Specification

**Version:** 1.0
**Last Updated:** 2025-11-17
**Status:** Planning Phase

## Table of Contents

1. [Overview](#overview)
2. [Objectives](#objectives)
3. [Design Decisions](#design-decisions)
4. [Architecture](#architecture)
5. [Component Specifications](#component-specifications)
6. [Data Models](#data-models)
7. [Workflows](#workflows)
8. [Race Condition Prevention](#race-condition-prevention)
9. [Implementation Plan](#implementation-plan)
10. [Success Criteria](#success-criteria)

---

## Overview

The Mining Operation is a sophisticated multi-container orchestration system for SpaceTraders that coordinates mining ships extracting resources from asteroid fields and transport ships selling the cargo at optimal markets. The system is designed to minimize wait times, avoid race conditions, and maximize profitability through intelligent cargo management and route optimization.

### System Architecture Pattern

The implementation follows the **ContractFleetCoordinator** pattern from the existing codebase, which provides battle-tested solutions for:
- Race-free ship ownership transfers
- Signal-before-release coordination
- Atomic state transitions
- Graceful shutdown and recovery

### Three-Container System

1. **Mining Coordinator** - Orchestrates ship pools and worker lifecycles
2. **Mining Workers** - Extract resources until cargo full
3. **Transport Workers** - Collect cargo and execute optimized selling routes

---

## Objectives

### Primary Goals

1. **Continuous Mining** - Mining ships extract resources continuously from target asteroid until cargo full
2. **Intelligent Cargo Management** - Automatically jettison low-value ores, keep only top N most valuable items
3. **Batch Transport** - Transport ships collect from multiple miners before selling (maximize efficiency)
4. **Optimized Selling** - Use TSP algorithm to plan multi-market selling routes
5. **Zero Race Conditions** - Flawless orchestration using atomic ship transfers
6. **Graceful Operations** - Handle shutdowns, restarts, and failures elegantly

### Non-Functional Requirements

- **Scalability** - Support 10+ miners and 5+ transport ships concurrently
- **Reliability** - Survive daemon restarts without losing operation state
- **Performance** - Minimize idle time for both miners and transporters
- **Maintainability** - Follow hexagonal architecture and CQRS patterns

---

## Design Decisions

### 1. Cargo Transfer Method

**Decision:** Direct ship-to-ship transfer at same waypoint

**Rationale:**
- Most efficient (no intermediate steps)
- Requires implementing `TransferCargoCommand` with API support
- Both ships must be docked at same waypoint (asteroid's associated station/waypoint)

**Alternative Rejected:** Jettison/pickup pattern (API may not support cargo collection)

### 2. Value Determination Strategy

**Decision:** Keep top N most valuable cargo types by market price

**Implementation:**
- Query market data for all cargo items in ship inventory
- Sort by market purchase price (what markets pay miners) in descending order
- Keep top N ore types (configurable, e.g., top 3)
- Jettison all other cargo types

**Example:**
```
Cargo Inventory:
- PRECIOUS_STONES (500 credits/unit) ← Keep
- GOLD_ORE (300 credits/unit) ← Keep
- PLATINUM_ORE (250 credits/unit) ← Keep
- IRON_ORE (50 credits/unit) ← Jettison
- ICE_WATER (10 credits/unit) ← Jettison
```

**Alternative Rejected:** Fixed whitelist (less dynamic to market changes)

### 3. Transport Operation Mode

**Decision:** Batch collection from multiple miners

**Flow:**
1. Transport waits for miner full signals
2. Coordinator accumulates waiting miners
3. When threshold reached (e.g., 3 miners) OR timeout (e.g., 5 minutes), spawn transport worker
4. Transport collects from miners sequentially until its own cargo full
5. Plan TSP route and sell all cargo
6. Return to asteroid field

**Benefits:**
- Fewer transport trips (fuel efficiency)
- Better market route optimization (more diverse cargo)
- Higher throughput

**Tradeoff:** Miners may wait longer for pickup (acceptable if enough transport ships)

### 4. Survey Support

**Decision:** No survey support in v1

**Rationale:**
- Simplify initial implementation
- Surveys add complexity (create survey → store → use within expiration)
- Random extraction acceptable for initial version
- Can add survey support in v2 as enhancement

---

## Architecture

### Hexagonal Architecture Layers

```
┌─────────────────────────────────────────────────────────┐
│                     CLI / gRPC API                       │
│              (Adapters - Entry Points)                   │
└─────────────────────────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────┐
│              Application Layer (CQRS)                    │
│  ┌──────────────────┐  ┌──────────────────┐             │
│  │    Commands      │  │     Queries      │             │
│  │  - Coordinator   │  │  - Cargo Value   │             │
│  │  - MiningWorker  │  │  - Market Data   │             │
│  │  - Transport     │  │                  │             │
│  └──────────────────┘  └──────────────────┘             │
└─────────────────────────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────┐
│                   Domain Layer                           │
│  ┌─────────────────────────────────────────┐             │
│  │  Entities: MiningOperation, Ship        │             │
│  │  Value Objects: CargoTransferRequest    │             │
│  │  Ports: Repositories, API, Routing      │             │
│  └─────────────────────────────────────────┘             │
└─────────────────────────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────┐
│              Adapter Layer (Infrastructure)              │
│  ┌───────────┐  ┌──────────┐  ┌──────────────┐          │
│  │PostgreSQL │  │SpaceAPI  │  │RoutingService│          │
│  │Repository │  │  Client  │  │  (OR-Tools)  │          │
│  └───────────┘  └──────────┘  └──────────────┘          │
└─────────────────────────────────────────────────────────┘
```

### Component Interaction Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                  Mining Coordinator                          │
│  ┌─────────────┐              ┌──────────────┐              │
│  │ Miner Pool  │              │Transport Pool│              │
│  │  - Ship A   │              │  - Ship T1   │              │
│  │  - Ship B   │              │  - Ship T2   │              │
│  │  - Ship C   │              │              │              │
│  └─────────────┘              └──────────────┘              │
│         │                              │                     │
│         │ Spawn Workers                │ Spawn Workers      │
│         ▼                              ▼                     │
│  ┌──────────────────┐          ┌─────────────────┐          │
│  │ Mining Workers   │          │Transport Workers│          │
│  │  [A] [B] [C]     │──Signal─▶│    [T1] [T2]    │          │
│  │  (parallel)      │  "Full"  │   (sequential)  │          │
│  └──────────────────┘          └─────────────────┘          │
└─────────────────────────────────────────────────────────────┘
         │                              │
         │ Extract                      │ Transfer → Sell
         ▼                              ▼
┌──────────────────┐            ┌─────────────────┐
│  Asteroid Field  │            │  Markets (TSP)  │
│   X1-ABC-123     │            │  M1 → M2 → M3   │
└──────────────────┘            └─────────────────┘
```

---

## Component Specifications

### 1. Domain Layer

#### 1.1 MiningOperation Entity

**File:** `internal/domain/mining/mining_operation.go`

**Purpose:** Aggregate root representing a complete mining operation

**Fields:**
```go
type MiningOperation struct {
    ID               string
    PlayerID         int
    AsteroidField    string              // Waypoint symbol
    Status           OperationStatus     // PENDING, RUNNING, COMPLETED, STOPPED
    TopNOres         int                 // Number of ore types to keep
    MinerShips       []string            // Ship symbols
    TransportShips   []string            // Ship symbols
    CreatedAt        time.Time
    UpdatedAt        time.Time
}

type OperationStatus string
const (
    OperationStatusPending   = "PENDING"
    OperationStatusRunning   = "RUNNING"
    OperationStatusCompleted = "COMPLETED"
    OperationStatusStopped   = "STOPPED"
    OperationStatusFailed    = "FAILED"
)
```

**Methods:**
```go
// State transitions
func (op *MiningOperation) Start() error
func (op *MiningOperation) Stop() error
func (op *MiningOperation) Complete() error
func (op *MiningOperation) Fail(err error) error

// Validation
func (op *MiningOperation) Validate() error
func (op *MiningOperation) HasMiners() bool
func (op *MiningOperation) HasTransports() bool
```

**Invariants:**
- Must have at least 1 miner ship
- Must have at least 1 transport ship
- TopNOres must be >= 1
- AsteroidField must be valid waypoint symbol
- Status transitions: PENDING → RUNNING → (COMPLETED|STOPPED|FAILED)

#### 1.2 CargoTransferRequest Value Object

**File:** `internal/domain/mining/cargo_transfer_request.go`

**Purpose:** Immutable transfer request between ships

**Fields:**
```go
type CargoTransferRequest struct {
    ID                string
    MiningOperationID string
    MinerShip         string
    TransportShip     string            // May be nil if pending assignment
    CargoManifest     []CargoItem       // Items to transfer
    Status            TransferStatus    // PENDING, IN_PROGRESS, COMPLETED
    CreatedAt         time.Time
    CompletedAt       *time.Time
}

type CargoItem struct {
    Symbol string
    Units  int
}

type TransferStatus string
const (
    TransferStatusPending    = "PENDING"
    TransferStatusInProgress = "IN_PROGRESS"
    TransferStatusCompleted  = "COMPLETED"
)
```

**Immutability:** All operations return new instances

#### 1.3 Repository Ports

**File:** `internal/domain/mining/ports.go`

```go
type MiningOperationRepository interface {
    Insert(ctx context.Context, op *MiningOperation) error
    FindByID(ctx context.Context, id string) (*MiningOperation, error)
    Update(ctx context.Context, op *MiningOperation) error
    Delete(ctx context.Context, id string) error
    FindActive(ctx context.Context, playerID int) ([]*MiningOperation, error)
}

type CargoTransferQueueRepository interface {
    Enqueue(ctx context.Context, transfer *CargoTransferRequest) error
    FindPendingForMiner(ctx context.Context, minerShip string) (*CargoTransferRequest, error)
    FindPendingForOperation(ctx context.Context, operationID string) ([]*CargoTransferRequest, error)
    MarkInProgress(ctx context.Context, transferID string, transportShip string) error
    MarkCompleted(ctx context.Context, transferID string) error
}
```

### 2. Application Layer Commands

#### 2.1 MiningCoordinatorCommand

**File:** `internal/application/mining/mining_coordinator_command.go`

**Command:**
```go
type MiningCoordinatorCommand struct {
    MiningOperationID string
    PlayerID          int
    AsteroidField     string
    MinerShips        []string
    TransportShips    []string
    TopNOres          int
    MaxIterations     int     // -1 for infinite
}

type MiningCoordinatorResponse struct {
    Success       bool
    OperationID   string
    MinersSpawned int
    TransportsUsed int
}
```

**Handler Responsibilities:**
1. Create ship pool assignments (miners + transporters)
2. Create MiningOperation entity in database
3. Spawn mining worker container for each miner
4. Wait for miner completion signals (unbuffered channel)
5. Accumulate waiting miners
6. When threshold reached, spawn transport worker
7. Atomically transfer ships between workers
8. Handle graceful shutdown
9. Clean up ship assignments on completion

**Pseudocode:**
```go
func (h *Handler) Handle(ctx, cmd) (*Response, error) {
    // 1. Create operation entity
    operation := NewMiningOperation(...)
    h.operationRepo.Insert(ctx, operation)

    // 2. Create ship pool assignments
    CreatePoolAssignments(ctx, cmd.MinerShips, containerID, playerID, shipRepo)
    CreatePoolAssignments(ctx, cmd.TransportShips, containerID, playerID, shipRepo)

    // 3. Spawn mining workers
    completionChan := make(chan string) // Unbuffered!
    for _, minerShip := range cmd.MinerShips {
        workerID := uuid.New()
        workerCmd := MiningWorkerCommand{...}

        // Persist worker (not started yet)
        daemonClient.PersistMiningWorker(ctx, workerID, workerCmd)

        // Atomic transfer: coordinator → worker
        shipRepo.Transfer(ctx, minerShip, containerID, workerID)

        // Start worker with completion callback
        daemonClient.StartMiningWorker(ctx, workerID, completionChan)
    }

    // 4. Main coordination loop
    waitingMiners := []string{}
    for {
        select {
        case minerShip := <-completionChan:
            // Miner full, add to waiting list
            waitingMiners = append(waitingMiners, minerShip)

            // Check if should spawn transport
            if len(waitingMiners) >= BATCH_SIZE || timeout {
                SpawnTransportWorker(ctx, waitingMiners)
                waitingMiners = []string{}
            }

        case <-ctx.Done():
            // Graceful shutdown
            ReleaseAllShips(ctx, containerID, "shutdown")
            return
        }
    }
}
```

#### 2.2 MiningWorkerCommand

**File:** `internal/application/mining/mining_worker_command.go`

**Command:**
```go
type MiningWorkerCommand struct {
    ShipSymbol    string
    PlayerID      int
    AsteroidField string
    TopNOres      int
}

type MiningWorkerResponse struct {
    ExtractionCount int
    FinalCargo      *Cargo
}
```

**Handler Responsibilities:**
1. Ensure ship at asteroid field (navigate if needed)
2. Main loop: extract → evaluate → jettison → check full
3. Extract resources with cooldown handling
4. Query cargo value (top N evaluation)
5. Jettison low-value items
6. Check if cargo full → if yes, signal coordinator
7. Single iteration (complete when cargo full)

**Pseudocode:**
```go
func (h *Handler) Handle(ctx, cmd) (*Response, error) {
    ship := FetchShip(ctx, cmd.ShipSymbol, cmd.PlayerID)

    // Navigate to asteroid if not there
    if ship.CurrentLocation().Symbol != cmd.AsteroidField {
        NavigateShipCommand{...}
    }

    // Ensure in orbit for mining
    OrbitShipCommand{...}

    // Main mining loop
    for !ship.IsCargoFull() {
        // 1. Extract resources
        extractResp, err := mediator.Send(ctx, &ExtractResourcesCommand{
            ShipSymbol: cmd.ShipSymbol,
            PlayerID:   cmd.PlayerID,
        })

        // 2. Handle cooldown
        if extractResp.Cooldown > 0 {
            time.Sleep(extractResp.Cooldown)
        }

        // 3. Evaluate cargo value
        evalResp, err := mediator.Send(ctx, &EvaluateCargoValueQuery{
            CargoItems:   ship.Cargo().Inventory,
            TopN:         cmd.TopNOres,
            SystemSymbol: ship.CurrentSystem(),
            PlayerID:     cmd.PlayerID,
        })

        // 4. Jettison low-value items
        for _, item := range evalResp.JettisonItems {
            JettisonCargoCommand{
                ShipSymbol: cmd.ShipSymbol,
                GoodSymbol: item.Symbol,
                Units:      item.Units,
                PlayerID:   cmd.PlayerID,
            }
        }

        // 5. Refresh ship state
        ship = FetchShip(ctx, cmd.ShipSymbol, cmd.PlayerID)

        // Check context cancellation
        if ctx.Err() != nil {
            return nil, ctx.Err()
        }
    }

    // Cargo full - signal coordinator via completion channel
    // ContainerRunner handles signaling automatically
    return &Response{...}, nil
}
```

#### 2.3 TransportWorkerCommand

**File:** `internal/application/mining/transport_worker_command.go`

**Command:**
```go
type TransportWorkerCommand struct {
    ShipSymbol         string
    PlayerID           int
    MiningOperationID  string
    MinersToCollect    []string  // List of waiting miners
    AsteroidField      string
}

type TransportWorkerResponse struct {
    MinersCollected  int
    MarketsVisited   int
    TotalRevenue     int
}
```

**Handler Responsibilities:**
1. For each waiting miner: navigate, dock, transfer cargo
2. Stop collecting when transport cargo full
3. Plan TSP route to best markets
4. Navigate to each market and sell cargo
5. Return to asteroid field
6. Signal coordinator completion

**Pseudocode:**
```go
func (h *Handler) Handle(ctx, cmd) (*Response, error) {
    transportShip := FetchShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
    minersCollected := 0

    // 1. Collect from miners (batch mode)
    for _, minerShip := range cmd.MinersToCollect {
        // Navigate to miner's location
        miner := FetchShip(ctx, minerShip, cmd.PlayerID)
        if transportShip.CurrentLocation() != miner.CurrentLocation() {
            NavigateShipCommand{
                ShipSymbol:  cmd.ShipSymbol,
                Destination: miner.CurrentLocation().Symbol,
                PlayerID:    cmd.PlayerID,
            }
        }

        // Ensure both ships docked (for transfer)
        DockShipCommand{ShipSymbol: cmd.ShipSymbol, ...}
        DockShipCommand{ShipSymbol: minerShip, ...}

        // Transfer all cargo from miner to transport
        for _, item := range miner.Cargo().Inventory {
            // Check if transport has space
            if !transportShip.HasCargoSpace(item.Units) {
                break // Transport full, stop collecting
            }

            TransferCargoCommand{
                FromShip:   minerShip,
                ToShip:     cmd.ShipSymbol,
                GoodSymbol: item.Symbol,
                Units:      item.Units,
                PlayerID:   cmd.PlayerID,
            }
        }

        minersCollected++

        // Check if transport cargo full
        transportShip = FetchShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
        if transportShip.IsCargoFull() {
            break
        }
    }

    // 2. Plan selling route (TSP optimization)
    markets := FindBestMarketsForCargo(ctx, transportShip.Cargo(), ...)
    tourResp, err := h.routingClient.OptimizeTour(ctx, &TourRequest{
        SystemSymbol:  transportShip.CurrentSystem(),
        StartWaypoint: transportShip.CurrentLocation().Symbol,
        Waypoints:     markets,
        FuelCapacity:  transportShip.FuelCapacity(),
        EngineSpeed:   transportShip.EngineSpeed(),
        AllWaypoints:  systemWaypoints,
    })

    // 3. Visit markets and sell
    totalRevenue := 0
    for _, marketWaypoint := range tourResp.VisitOrder {
        // Navigate to market
        NavigateShipCommand{
            ShipSymbol:  cmd.ShipSymbol,
            Destination: marketWaypoint,
            PlayerID:    cmd.PlayerID,
        }

        // Sell all cargo at this market
        for _, item := range transportShip.Cargo().Inventory {
            sellResp, _ := SellCargoCommand{
                ShipSymbol: cmd.ShipSymbol,
                GoodSymbol: item.Symbol,
                Units:      item.Units,
                PlayerID:   cmd.PlayerID,
            }
            totalRevenue += sellResp.TotalRevenue
        }

        transportShip = FetchShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
    }

    // 4. Return to asteroid field
    NavigateShipCommand{
        ShipSymbol:  cmd.ShipSymbol,
        Destination: cmd.AsteroidField,
        PlayerID:    cmd.PlayerID,
    }

    // Signal coordinator (ContainerRunner handles this)
    return &Response{
        MinersCollected: minersCollected,
        MarketsVisited:  len(tourResp.VisitOrder),
        TotalRevenue:    totalRevenue,
    }, nil
}
```

#### 2.4 ExtractResourcesCommand

**File:** `internal/application/ship/extract_resources_command.go`

**Command:**
```go
type ExtractResourcesCommand struct {
    ShipSymbol string
    PlayerID   int
    Survey     *Survey  // nil for no survey
}

type ExtractResourcesResponse struct {
    Extraction Extraction
    Cooldown   time.Duration
}

type Extraction struct {
    ShipSymbol string
    Yield      struct {
        Symbol string
        Units  int
    }
}
```

**Handler Responsibilities:**
1. Fetch ship from API
2. Ensure ship in orbit at asteroid waypoint
3. Call SpaceTraders API `POST /my/ships/{shipSymbol}/extract`
4. Handle cooldown period
5. Return extraction results

**API Integration:**
```go
// In adapters/api/client.go
func (c *SpaceTradersClient) ExtractResources(
    ctx context.Context,
    shipSymbol string,
    survey *Survey,
    token string,
) (*ExtractionResult, error)
```

#### 2.5 TransferCargoCommand

**File:** `internal/application/ship/transfer_cargo_command.go`

**Command:**
```go
type TransferCargoCommand struct {
    FromShip   string
    ToShip     string
    GoodSymbol string
    Units      int
    PlayerID   int
}

type TransferCargoResponse struct {
    TransferredUnits int
}
```

**Handler Responsibilities:**
1. Fetch both ships
2. Validate both at same waypoint
3. Validate both ships docked
4. Validate sender has cargo
5. Validate receiver has space
6. Call API `POST /my/ships/{fromShip}/transfer`
7. Update cargo states

**API Integration:**
```go
// In adapters/api/client.go
func (c *SpaceTradersClient) TransferCargo(
    ctx context.Context,
    fromShip string,
    toShip string,
    goodSymbol string,
    units int,
    token string,
) error
```

#### 2.6 EvaluateCargoValueQuery

**File:** `internal/application/mining/evaluate_cargo_value_query.go`

**Query:**
```go
type EvaluateCargoValueQuery struct {
    CargoItems   []CargoItem
    TopN         int
    SystemSymbol string
    PlayerID     int
}

type EvaluateCargoValueResponse struct {
    KeepItems     []CargoItem  // Top N by value
    JettisonItems []CargoItem  // Rest
}
```

**Handler Logic:**
```go
func (h *Handler) Handle(ctx, query) (*Response, error) {
    // 1. Fetch market prices for all cargo items
    itemValues := []struct{
        Item  CargoItem
        Price int
    }{}

    for _, item := range query.CargoItems {
        market, err := h.marketRepo.FindBestMarketBuying(
            ctx,
            item.Symbol,
            query.SystemSymbol,
            query.PlayerID,
        )

        itemValues = append(itemValues, {
            Item:  item,
            Price: market.PurchasePrice, // What market pays us
        })
    }

    // 2. Sort by price descending
    sort.Slice(itemValues, func(i, j int) bool {
        return itemValues[i].Price > itemValues[j].Price
    })

    // 3. Split into keep (top N) and jettison (rest)
    keepItems := []CargoItem{}
    jettisonItems := []CargoItem{}

    for i, iv := range itemValues {
        if i < query.TopN {
            keepItems = append(keepItems, iv.Item)
        } else {
            jettisonItems = append(jettisonItems, iv.Item)
        }
    }

    return &Response{
        KeepItems:     keepItems,
        JettisonItems: jettisonItems,
    }, nil
}
```

### 3. Adapter Layer

#### 3.1 Persistence Repositories

**File:** `internal/adapters/persistence/mining_operation_repository.go`

**Implementation:**
```go
type MiningOperationRepository struct {
    db *gorm.DB
}

func (r *Repository) Insert(ctx, op) error {
    model := &MiningOperationModel{
        ID:            op.ID,
        PlayerID:      op.PlayerID,
        AsteroidField: op.AsteroidField,
        Status:        string(op.Status),
        Config: JSON{
            "topNOres":       op.TopNOres,
            "minerShips":     op.MinerShips,
            "transportShips": op.TransportShips,
        },
    }
    return r.db.WithContext(ctx).Create(model).Error
}

// ... other methods
```

**File:** `internal/adapters/persistence/cargo_transfer_repository.go`

Similar implementation for CargoTransferQueue.

#### 3.2 API Client Extensions

**File:** `internal/adapters/api/client.go`

**New Methods:**
```go
// Extract resources from asteroid
func (c *SpaceTradersClient) ExtractResources(
    ctx context.Context,
    shipSymbol string,
    survey *Survey,
    token string,
) (*ExtractionResult, error) {
    endpoint := fmt.Sprintf("/my/ships/%s/extract", shipSymbol)

    body := map[string]interface{}{}
    if survey != nil {
        body["survey"] = survey
    }

    var result struct {
        Data struct {
            Extraction ExtractionResult `json:"extraction"`
            Cooldown   CooldownData     `json:"cooldown"`
        } `json:"data"`
    }

    err := c.makeRequest(ctx, "POST", endpoint, body, &result, token)
    if err != nil {
        return nil, err
    }

    return &result.Data.Extraction, nil
}

// Transfer cargo between ships
func (c *SpaceTradersClient) TransferCargo(
    ctx context.Context,
    fromShip string,
    toShip string,
    goodSymbol string,
    units int,
    token string,
) error {
    endpoint := fmt.Sprintf("/my/ships/%s/transfer", fromShip)

    body := map[string]interface{}{
        "tradeSymbol": goodSymbol,
        "units":       units,
        "shipSymbol":  toShip,
    }

    err := c.makeRequest(ctx, "POST", endpoint, body, nil, token)
    return err
}
```

#### 3.3 Database Models

**File:** `internal/adapters/persistence/models.go`

**Add New Models:**
```go
type MiningOperationModel struct {
    ID            string `gorm:"primaryKey"`
    PlayerID      int    `gorm:"primaryKey"`
    AsteroidField string `gorm:"not null"`
    Status        string `gorm:"default:'pending'"`
    Config        datatypes.JSON
    CreatedAt     time.Time
    UpdatedAt     time.Time
}

func (MiningOperationModel) TableName() string {
    return "mining_operations"
}

type CargoTransferQueueModel struct {
    ID                int    `gorm:"primaryKey;autoIncrement"`
    MiningOperationID string `gorm:"not null"`
    MinerShip         string `gorm:"not null"`
    TransportShip     string
    Status            string `gorm:"default:'pending'"`
    CargoManifest     datatypes.JSON `gorm:"not null"`
    CreatedAt         time.Time
    CompletedAt       *time.Time
}

func (CargoTransferQueueModel) TableName() string {
    return "cargo_transfer_queue"
}
```

---

## Data Models

### Database Schema

#### mining_operations Table

```sql
CREATE TABLE mining_operations (
    id VARCHAR(36) NOT NULL,
    player_id INT NOT NULL,
    asteroid_field VARCHAR(255) NOT NULL,
    status VARCHAR(50) DEFAULT 'pending',
    config JSONB NOT NULL,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    PRIMARY KEY (id, player_id)
);

CREATE INDEX idx_mining_ops_status ON mining_operations(player_id, status);
CREATE INDEX idx_mining_ops_asteroid ON mining_operations(asteroid_field);
```

**Config JSON Schema:**
```json
{
  "topNOres": 3,
  "minerShips": ["SHIP-A", "SHIP-B", "SHIP-C"],
  "transportShips": ["SHIP-T1", "SHIP-T2"]
}
```

#### cargo_transfer_queue Table

```sql
CREATE TABLE cargo_transfer_queue (
    id SERIAL PRIMARY KEY,
    mining_operation_id VARCHAR(36) NOT NULL,
    miner_ship VARCHAR(255) NOT NULL,
    transport_ship VARCHAR(255),
    status VARCHAR(50) DEFAULT 'pending',
    cargo_manifest JSONB NOT NULL,
    created_at TIMESTAMP DEFAULT NOW(),
    completed_at TIMESTAMP,
    FOREIGN KEY (mining_operation_id) REFERENCES mining_operations(id) ON DELETE CASCADE
);

CREATE INDEX idx_transfer_queue_operation ON cargo_transfer_queue(mining_operation_id, status);
CREATE INDEX idx_transfer_queue_miner ON cargo_transfer_queue(miner_ship, status);
```

**Cargo Manifest JSON Schema:**
```json
{
  "items": [
    {"symbol": "PRECIOUS_STONES", "units": 50},
    {"symbol": "GOLD_ORE", "units": 30}
  ]
}
```

---

## Workflows

### Mining Worker Workflow

```
┌─────────────────────────────────────────────────────────────┐
│                     Mining Worker                            │
└─────────────────────────────────────────────────────────────┘
                         │
                         ▼
              ┌──────────────────────┐
              │ Navigate to Asteroid │
              └──────────────────────┘
                         │
                         ▼
              ┌──────────────────────┐
              │   Ensure In Orbit    │
              └──────────────────────┘
                         │
                         ▼
         ┌───────────────────────────────────┐
         │       Mining Loop                 │
         │  ┌─────────────────────────────┐  │
         │  │ 1. Extract Resources        │  │
         │  └─────────────────────────────┘  │
         │                │                   │
         │                ▼                   │
         │  ┌─────────────────────────────┐  │
         │  │ 2. Wait for Cooldown        │  │
         │  └─────────────────────────────┘  │
         │                │                   │
         │                ▼                   │
         │  ┌─────────────────────────────┐  │
         │  │ 3. Evaluate Cargo Value     │  │
         │  │    (Query Market Prices)    │  │
         │  └─────────────────────────────┘  │
         │                │                   │
         │                ▼                   │
         │  ┌─────────────────────────────┐  │
         │  │ 4. Jettison Low-Value Ores  │  │
         │  │    (Keep Top N)             │  │
         │  └─────────────────────────────┘  │
         │                │                   │
         │                ▼                   │
         │  ┌─────────────────────────────┐  │
         │  │ 5. Check Cargo Full?        │  │
         │  └─────────────────────────────┘  │
         │         │              │           │
         │         No             Yes         │
         │         │              │           │
         └─────────┘              └───────────┘
                                      │
                                      ▼
                          ┌──────────────────────┐
                          │ Signal Coordinator   │
                          │ "Cargo Full"         │
                          └──────────────────────┘
                                      │
                                      ▼
                          ┌──────────────────────┐
                          │  Complete Worker     │
                          │  (Return Ship)       │
                          └──────────────────────┘
```

### Transport Worker Workflow

```
┌─────────────────────────────────────────────────────────────┐
│                    Transport Worker                          │
└─────────────────────────────────────────────────────────────┘
                         │
                         ▼
         ┌───────────────────────────────────┐
         │  Batch Collection Loop            │
         │  ┌─────────────────────────────┐  │
         │  │ For Each Waiting Miner:     │  │
         │  │                             │  │
         │  │ 1. Navigate to Miner        │  │
         │  └─────────────────────────────┘  │
         │                │                   │
         │                ▼                   │
         │  ┌─────────────────────────────┐  │
         │  │ 2. Dock Both Ships          │  │
         │  └─────────────────────────────┘  │
         │                │                   │
         │                ▼                   │
         │  ┌─────────────────────────────┐  │
         │  │ 3. Transfer All Cargo       │  │
         │  │    Miner → Transport        │  │
         │  └─────────────────────────────┘  │
         │                │                   │
         │                ▼                   │
         │  ┌─────────────────────────────┐  │
         │  │ 4. Transport Cargo Full?    │  │
         │  └─────────────────────────────┘  │
         │         │              │           │
         │         No             Yes         │
         │         │              │           │
         └─────────┘              └───────────┘
                                      │
                                      ▼
                    ┌──────────────────────────────┐
                    │ Find Best Markets for Cargo  │
                    │ (Market Repository Query)    │
                    └──────────────────────────────┘
                                      │
                                      ▼
                    ┌──────────────────────────────┐
                    │ Plan TSP Route               │
                    │ (OptimizeTour via Routing)   │
                    └──────────────────────────────┘
                                      │
                                      ▼
         ┌───────────────────────────────────────┐
         │  Selling Loop                         │
         │  ┌─────────────────────────────────┐  │
         │  │ For Each Market in Tour:        │  │
         │  │                                 │  │
         │  │ 1. Navigate to Market           │  │
         │  └─────────────────────────────────┘  │
         │                │                       │
         │                ▼                       │
         │  ┌─────────────────────────────────┐  │
         │  │ 2. Sell All Cargo at Market     │  │
         │  └─────────────────────────────────┘  │
         │                │                       │
         └────────────────┘                       │
                                                  │
                                                  ▼
                            ┌────────────────────────────┐
                            │ Return to Asteroid Field   │
                            └────────────────────────────┘
                                      │
                                      ▼
                            ┌────────────────────────────┐
                            │ Signal Coordinator         │
                            │ "Transport Complete"       │
                            └────────────────────────────┘
```

### Coordinator Orchestration Workflow

```
┌─────────────────────────────────────────────────────────────┐
│                  Mining Coordinator                          │
└─────────────────────────────────────────────────────────────┘
                         │
                         ▼
              ┌──────────────────────────┐
              │ Create Ship Pool         │
              │ (Miners + Transports)    │
              └──────────────────────────┘
                         │
                         ▼
              ┌──────────────────────────┐
              │ Spawn Mining Workers     │
              │ (One per Miner Ship)     │
              └──────────────────────────┘
                         │
                         ▼
         ┌───────────────────────────────────────┐
         │  Main Coordination Loop               │
         │                                       │
         │  ┌──────────────────────────────┐    │
         │  │ Wait for Events:             │    │
         │  │                              │    │
         │  │ • Miner Completion Signal    │◀───┼──────┐
         │  │ • Timeout (Batch Trigger)    │    │      │
         │  │ • Shutdown Signal            │    │      │
         │  └──────────────────────────────┘    │      │
         │           │       │        │          │      │
         └───────────┼───────┼────────┼──────────┘      │
                     │       │        │                 │
          Miner      │       │        │  Shutdown       │
          Signal     │       │        └─────────────────┼─────┐
                     │       │                          │     │
                     ▼       │                          ▼     ▼
      ┌──────────────────┐  │              ┌─────────────────────┐
      │ Add to Waiting   │  │              │ Graceful Shutdown   │
      │ Miners Queue     │  │              │ - Stop All Workers  │
      └──────────────────┘  │              │ - Release Ships     │
                     │       │              │ - Save State        │
                     ▼       │              └─────────────────────┘
      ┌──────────────────┐  │
      │ Check Batch      │  │
      │ Threshold        │  │
      │ - Count >= 3?    │  │  Timeout
      │ - OR Timeout?    │◀─┘
      └──────────────────┘
                     │
                     Yes
                     │
                     ▼
      ┌────────────────────────────────┐
      │ Select Available Transport     │
      │ from Transport Pool            │
      └────────────────────────────────┘
                     │
                     ▼
      ┌────────────────────────────────┐
      │ Spawn Transport Worker         │
      │ Pass Waiting Miners List       │
      └────────────────────────────────┘
                     │
                     ▼
      ┌────────────────────────────────┐
      │ Atomic Ship Transfers:         │
      │ - Miners → Transport Worker    │
      │ - Transport → Transport Worker │
      └────────────────────────────────┘
                     │
                     ▼
      ┌────────────────────────────────┐
      │ Clear Waiting Miners Queue     │
      └────────────────────────────────┘
                     │
                     └──────────┐
                                │
         ┌──────────────────────┘
         │
         ▼
      ┌────────────────────────────────┐
      │ Wait for Transport Completion  │
      └────────────────────────────────┘
         │
         ▼
      ┌────────────────────────────────┐
      │ Atomic Ship Transfers:         │
      │ - Miners → Coordinator Pool    │
      │ - Transport → Coordinator Pool │
      └────────────────────────────────┘
         │
         ▼
      ┌────────────────────────────────┐
      │ Respawn Mining Workers         │
      │ for Returned Miners            │
      └────────────────────────────────┘
         │
         └─────────────────┐
                           │
                           └──────────────────────┐
                                                  │
                                                  ▼
                                    ┌──────────────────────┐
                                    │ Continue Loop        │
                                    │ (Infinite Iterations)│
                                    └──────────────────────┘
```

---

## Race Condition Prevention

### Core Principle: Signal-Before-Release Pattern

**Problem:** Without coordination, ships can be lost or double-assigned during worker transitions.

**Solution:** Workers signal completion BEFORE releasing ship ownership. Coordinator handles atomic transfers.

### Implementation Details

#### 1. Unbuffered Completion Channel

```go
// Coordinator creates unbuffered channel
completionChan := make(chan string)  // NO buffer size
```

**Why Unbuffered:**
- Worker blocks on send until coordinator receives
- Prevents "signal lost" race condition
- Guarantees coordinator ready before worker continues

#### 2. Worker Signaling

**In ContainerRunner (`container_runner.go:273-282`):**

```go
// Worker completion sequence
func (r *ContainerRunner) signalCompletion() {
    if r.completionCallback != nil {
        // BLOCKS until coordinator receives
        r.completionCallback <- r.shipSymbol

        // DON'T release ship - coordinator will transfer it
        return
    }

    // No coordinator - release normally
    r.releaseShipAssignments("completed")
}
```

**Critical:** Worker does NOT release ship when using completion callback.

#### 3. Coordinator Reception

```go
select {
case minerShip := <-completionChan:
    // Received signal - ship still owned by worker at this point

    // Find worker container ID
    workerContainer := findContainerByShip(minerShip)

    // Atomic transfer: worker → transport
    shipAssignmentRepo.Transfer(
        ctx,
        minerShip,
        workerContainer.ID,
        transportWorkerID,
    )

    // Now transfer is atomic - no race window

case <-ctx.Done():
    // Shutdown
}
```

#### 4. Atomic Transfer Method

**In ShipAssignmentRepository:**

```go
func (r *Repository) Transfer(
    ctx context.Context,
    shipSymbol string,
    fromContainerID string,
    toContainerID string,
) error {
    return r.db.Transaction(func(tx *gorm.DB) error {
        // Single atomic operation (no race window)
        result := tx.Model(&ShipAssignment{}).
            Where("ship_symbol = ? AND container_id = ?", shipSymbol, fromContainerID).
            Update("container_id", toContainerID)

        if result.RowsAffected == 0 {
            return fmt.Errorf("ship not owned by source container")
        }

        return nil
    })
}
```

**Why Atomic:**
- Single database transaction
- No DELETE + INSERT (creates race window)
- Update only succeeds if ship owned by `fromContainerID`
- Fails fast if ship already transferred

### Race Condition Scenarios Prevented

#### Scenario 1: Double Assignment

**Without Signal-Before-Release:**
```
Worker A completes → releases ship → ship available
Coordinator assigns ship to Worker B
Worker C grabs ship from pool (RACE!)
```

**With Signal-Before-Release:**
```
Worker A completes → signals coordinator → blocks
Coordinator receives signal → atomic transfer to Worker B
Worker A unblocks → does NOT release (coordinator controls)
```

#### Scenario 2: Lost Ship

**Without Unbuffered Channel:**
```
Worker sends signal to buffered channel → continues
Worker releases ship assignment → ship freed
Coordinator crashes before reading buffer → signal lost
Ship lost forever (no owner)
```

**With Unbuffered Channel:**
```
Worker sends signal → BLOCKS until coordinator receives
Coordinator receives → ship still owned by worker
Coordinator transfers → ship ownership atomic
Worker unblocks → does NOT release
```

#### Scenario 3: Concurrent Transfer

**Without Atomic Transfer:**
```
Coordinator checks: ship owned by Worker A ✓
[Context switch]
Another coordinator deletes Worker A's assignment
[Context switch back]
Original coordinator assigns to Worker B → ship has no owner!
```

**With Atomic Transfer:**
```
UPDATE ship_assignments
SET container_id = 'Worker-B'
WHERE ship_symbol = 'SHIP-1'
  AND container_id = 'Worker-A'  ← Atomic condition

Fails if Worker A no longer owns ship (safe)
```

### Checklist for Race-Free Implementation

- [ ] Use unbuffered completion channels (`make(chan string)`)
- [ ] Workers signal before releasing (`completionCallback <- shipSymbol`)
- [ ] Workers do NOT release ships when using callback
- [ ] Coordinator uses atomic `Transfer()` method
- [ ] All ship ownership changes via `Transfer()` (never DELETE + INSERT)
- [ ] Verify ship ownership before operations
- [ ] Handle container INTERRUPTED state (daemon restarts)
- [ ] Test concurrent scenarios with multiple workers

---

## Implementation Plan

### Phase 1: Foundation (Domain & Database)

**Files to Create:**
1. `internal/domain/mining/mining_operation.go`
2. `internal/domain/mining/cargo_transfer_request.go`
3. `internal/domain/mining/ports.go`
4. `internal/adapters/persistence/models.go` (extend)
5. Database migration SQL scripts

**Deliverables:**
- MiningOperation entity with state machine
- CargoTransferRequest value object
- Repository interfaces defined
- Database tables created
- GORM models implemented

**Estimated Effort:** 4-6 hours

### Phase 2: API Integration

**Files to Create/Modify:**
1. `internal/adapters/api/client.go` (extend)
2. `internal/application/ship/extract_resources_command.go`
3. `internal/application/ship/transfer_cargo_command.go`

**Deliverables:**
- ExtractResources API method
- TransferCargo API method
- Command handlers with validation
- Error handling and retries

**Estimated Effort:** 3-4 hours

### Phase 3: Cargo Value Evaluation

**Files to Create:**
1. `internal/application/mining/evaluate_cargo_value_query.go`
2. `internal/adapters/persistence/mining_operation_repository.go`

**Deliverables:**
- Query handler for cargo valuation
- Market price integration
- Top N selection logic
- Repository implementation

**Estimated Effort:** 2-3 hours

### Phase 4: Mining Worker

**Files to Create:**
1. `internal/application/mining/mining_worker_command.go`

**Deliverables:**
- Complete mining loop implementation
- Extract → evaluate → jettison → check full
- Cooldown handling
- Completion signaling

**Estimated Effort:** 4-5 hours

### Phase 5: Transport Worker

**Files to Create:**
1. `internal/application/mining/transport_worker_command.go`
2. `internal/adapters/persistence/cargo_transfer_repository.go`

**Deliverables:**
- Batch collection logic
- Cargo transfer orchestration
- TSP route planning integration
- Market selling loop

**Estimated Effort:** 5-6 hours

### Phase 6: Mining Coordinator

**Files to Create:**
1. `internal/application/mining/mining_coordinator_command.go`
2. `internal/application/mining/ship_pool_manager.go`

**Deliverables:**
- Ship pool management
- Worker spawning logic
- Signal-before-release coordination
- Batch threshold logic
- Graceful shutdown

**Estimated Effort:** 6-8 hours

### Phase 7: Handler Registration & Integration

**Files to Modify:**
1. Application setup (mediator registration)
2. Container type constants
3. gRPC service definitions (if needed)

**Deliverables:**
- All handlers registered in mediator
- Container types defined
- Integration with daemon server

**Estimated Effort:** 2-3 hours

### Phase 8: CLI Command (Optional)

**Files to Create:**
1. `internal/adapters/cli/mining_operation.go`

**Deliverables:**
- CLI command for starting operations
- Parameter validation
- User-friendly output

**Estimated Effort:** 2-3 hours

### Phase 9: Documentation & Refinement

**Files to Create/Update:**
1. README updates
2. API documentation
3. Architecture diagrams
4. Troubleshooting guide

**Estimated Effort:** 2-3 hours

---

## Success Criteria

### Functional Requirements

- [ ] Mining workers extract resources continuously until cargo full
- [ ] Low-value cargo automatically jettisoned based on market prices
- [ ] Top N most valuable ores kept in cargo
- [ ] Transport ships collect from multiple miners (batch mode)
- [ ] TSP-optimized selling routes minimize travel time
- [ ] All cargo sold at best available markets
- [ ] Transport returns to asteroid field after selling
- [ ] Operation runs continuously (infinite iterations)

### Quality Requirements

- [ ] Zero race conditions in ship transfers
- [ ] All ship ownership changes atomic (via Transfer method)
- [ ] Graceful shutdown preserves operation state
- [ ] Container restarts recover without data loss
- [ ] All domain logic follows hexagonal architecture
- [ ] All operations via CQRS mediator pattern
- [ ] No direct state mutations (use entity methods)

### Performance Requirements

- [ ] Miner idle time < 10% (excluding cooldowns)
- [ ] Transport idle time < 20%
- [ ] Batch collection triggered within 5 minutes of first miner completion
- [ ] Database queries optimized (indexed lookups)
- [ ] No N+1 query problems

### Operational Requirements

- [ ] CLI command for starting operations
- [ ] Clear logging at INFO level
- [ ] Error handling with context propagation
- [ ] Metrics tracking (extractions, transfers, sales)
- [ ] Health checks for coordinator and workers
- [ ] Documentation for configuration parameters

---

## Configuration Parameters

### Mining Operation Config

```go
type MiningOperationConfig struct {
    // Required
    AsteroidField    string   `json:"asteroidField"`    // Waypoint symbol
    MinerShips       []string `json:"minerShips"`       // Ship symbols
    TransportShips   []string `json:"transportShips"`   // Ship symbols

    // Cargo Management
    TopNOres         int      `json:"topNOres"`         // Default: 3

    // Batch Collection
    BatchThreshold   int      `json:"batchThreshold"`   // Default: 3 miners
    BatchTimeout     int      `json:"batchTimeout"`     // Seconds, default: 300

    // Iteration Control
    MaxIterations    int      `json:"maxIterations"`    // Default: -1 (infinite)

    // Market Selling
    MaxMarketsPerTrip int     `json:"maxMarketsPerTrip"` // Default: 5
}
```

### Example Configuration

```json
{
  "asteroidField": "X1-ABC-ASTEROID-123",
  "minerShips": [
    "SHIP-MINER-1",
    "SHIP-MINER-2",
    "SHIP-MINER-3"
  ],
  "transportShips": [
    "SHIP-TRANSPORT-1",
    "SHIP-TRANSPORT-2"
  ],
  "topNOres": 3,
  "batchThreshold": 3,
  "batchTimeout": 300,
  "maxIterations": -1,
  "maxMarketsPerTrip": 5
}
```

---

## Appendix A: API Endpoints Reference

### SpaceTraders API Endpoints Used

1. **Extract Resources**
   - Endpoint: `POST /my/ships/{shipSymbol}/extract`
   - Body: `{"survey": {...}}` (optional)
   - Response: `{extraction: {yield: {symbol, units}}, cooldown: {...}}`

2. **Transfer Cargo**
   - Endpoint: `POST /my/ships/{shipSymbol}/transfer`
   - Body: `{"tradeSymbol": "GOLD_ORE", "units": 50, "shipSymbol": "TARGET-SHIP"}`
   - Response: `{cargo: {...}}`

3. **Jettison Cargo**
   - Endpoint: `POST /my/ships/{shipSymbol}/jettison`
   - Body: `{"symbol": "IRON_ORE", "units": 20}`
   - Response: `{cargo: {...}}`

4. **Sell Cargo**
   - Endpoint: `POST /my/ships/{shipSymbol}/sell`
   - Body: `{"symbol": "GOLD_ORE", "units": 50}`
   - Response: `{transaction: {totalPrice, units}}`

5. **Navigate Ship**
   - Endpoint: `POST /my/ships/{shipSymbol}/navigate`
   - Body: `{"waypointSymbol": "X1-ABC-123"}`
   - Response: `{nav: {...}, fuel: {...}}`

---

## Appendix B: File Structure

```
internal/
├── domain/
│   └── mining/
│       ├── mining_operation.go          # Aggregate root
│       ├── cargo_transfer_request.go    # Value object
│       └── ports.go                     # Repository interfaces
│
├── application/
│   └── mining/
│       ├── mining_coordinator_command.go
│       ├── mining_worker_command.go
│       ├── transport_worker_command.go
│       ├── evaluate_cargo_value_query.go
│       └── ship_pool_manager.go
│
└── adapters/
    ├── persistence/
    │   ├── mining_operation_repository.go
    │   ├── cargo_transfer_repository.go
    │   └── models.go (extend)
    │
    ├── api/
    │   └── client.go (extend)
    │
    └── cli/
        └── mining_operation.go (optional)
```

---

## Appendix C: Glossary

- **Aggregate Root:** Domain entity that owns a cluster of related objects (e.g., MiningOperation)
- **CQRS:** Command Query Responsibility Segregation - separate read and write operations
- **Hexagonal Architecture:** Architecture pattern with domain core and infrastructure adapters
- **Signal-Before-Release:** Pattern where workers signal completion before releasing resources
- **TSP:** Traveling Salesman Problem - optimize route through multiple waypoints
- **Unbuffered Channel:** Go channel with no buffer, blocks sender until receiver ready
- **Value Object:** Immutable domain object defined by its attributes (e.g., CargoTransferRequest)

---

**End of Specification**
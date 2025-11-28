# Gas Extraction Operation Design

## Overview

This document describes the design for a **Gas Extraction Operation** that siphons resources from gas giants to supply manufacturing factories. Unlike the mining operation (which sells ores at markets for profit), this operation is a **supply chain operation** that delivers gases directly to factories that need them as manufacturing inputs.

## Table of Contents

1. [Design Decisions](#design-decisions)
2. [System Architecture](#system-architecture)
3. [Workflow](#workflow)
4. [SpaceTraders API](#spacetraders-api)
5. [Domain Layer](#domain-layer)
6. [Application Layer](#application-layer)
7. [Adapter Layer](#adapter-layer)
8. [Coordination Pattern](#coordination-pattern)
9. [Manufacturing Integration](#manufacturing-integration)
10. [Database Schema](#database-schema)
11. [CLI Interface](#cli-interface)
12. [Implementation Plan](#implementation-plan)

---

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| **Architecture** | Separate domain (`internal/domain/gas/`) | Clean separation with own entity, ports, repository. Follows hexagonal architecture. |
| **Auto-selection** | Same system only | Only consider gas giants in the same system as ships to minimize travel time. |
| **Operation mode** | Persistent | Runs continuously, building gas supply for manufacturing. |
| **Delivery target** | Factories | Transport ships deliver to factories with LOW gas supply, not markets. |
| **Coupling** | Loose | Gas operation queries factory needs via market data, no direct coordination channel with manufacturing. |
| **Cargo filtering** | Keep all gases | No jettison - all siphoned gases are valuable for manufacturing. |

---

## System Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Gas Extraction Operation                          │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   ┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐      │
│   │  Siphon Ships   │────▶│ Transport Ships │────▶│    Factories    │      │
│   │  (at gas giant) │     │ (cargo carriers)│     │ (need gas input)│      │
│   └─────────────────┘     └─────────────────┘     └─────────────────┘      │
│         │                        │                        │                 │
│         │ Siphon                 │ Receive cargo          │ Deliver to      │
│         │ continuously          │ from siphon ships      │ factory with    │
│         │                       │                        │ LOW gas supply  │
│         ▼                       ▼                        ▼                 │
│   ┌─────────────────────────────────────────────────────────────────┐      │
│   │                     Gas Coordinator                              │      │
│   │  - Channel-based coordination (reuse mining's ChannelCoordinator)│      │
│   │  - Manages siphon-to-transport assignment queue                  │      │
│   │  - Spawns and monitors worker containers                         │      │
│   └─────────────────────────────────────────────────────────────────┘      │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                        Manufacturing Operation                              │
│                                                                             │
│   ┌─────────────────┐                                                       │
│   │  SupplyMonitor  │◀─── Detects improved factory supply                  │
│   └─────────────────┘                                                       │
│           │                                                                 │
│           ▼                                                                 │
│   When factory supply reaches HIGH/ABUNDANT:                                │
│   - Manufacturing proceeds with production                                  │
│   - No direct coordination needed (loose coupling)                          │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Hexagonal Architecture Layers

```
┌─────────────────────────────────────────────────────────────────┐
│                      Adapter Layer                              │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐          │
│  │   CLI        │  │  gRPC Server │  │  Persistence │          │
│  │  workflow.go │  │  ops_gas.go  │  │  gas_repo.go │          │
│  └──────────────┘  └──────────────┘  └──────────────┘          │
├─────────────────────────────────────────────────────────────────┤
│                    Application Layer                            │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │                    Commands                               │  │
│  │  - SiphonResourcesCommand                                 │  │
│  │  - RunSiphonWorkerCommand                                 │  │
│  │  - RunGasTransportWorkerCommand                           │  │
│  │  - RunGasCoordinatorCommand                               │  │
│  └──────────────────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │                    Queries                                │  │
│  │  - FindFactoryForGasQuery                                 │  │
│  │  - GetGasOperationQuery                                   │  │
│  │  - ListGasOperationsQuery                                 │  │
│  └──────────────────────────────────────────────────────────┘  │
├─────────────────────────────────────────────────────────────────┤
│                      Domain Layer                               │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │  internal/domain/gas/                                     │  │
│  │  - Operation (aggregate root)                             │  │
│  │  - OperationRepository (port)                             │  │
│  └──────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

---

## Workflow

### Siphon Worker Loop

```
┌─────────────────────────────────────────────────────────────────┐
│                    Siphon Worker Lifecycle                      │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
                    ┌─────────────────┐
                    │  Start Worker   │
                    └─────────────────┘
                              │
                              ▼
                 ┌────────────────────────┐
                 │ At gas giant location? │
                 └────────────────────────┘
                    │ No              │ Yes
                    ▼                 │
          ┌─────────────────┐         │
          │ Navigate to     │         │
          │ gas giant       │         │
          └─────────────────┘         │
                    │                 │
                    └────────┬────────┘
                             ▼
              ┌──────────────────────────┐
              │   Main Siphoning Loop    │◀─────────────────────┐
              └──────────────────────────┘                      │
                             │                                  │
                             ▼                                  │
                    ┌─────────────────┐                         │
                    │  Ensure orbit   │                         │
                    └─────────────────┘                         │
                             │                                  │
                             ▼                                  │
                    ┌─────────────────┐                         │
                    │  Siphon (API)   │                         │
                    └─────────────────┘                         │
                             │                                  │
                             ▼                                  │
                    ┌─────────────────┐                         │
                    │ Wait cooldown   │                         │
                    └─────────────────┘                         │
                             │                                  │
                             ▼                                  │
                    ┌─────────────────┐                         │
                    │ Reload ship     │                         │
                    │ state           │                         │
                    └─────────────────┘                         │
                             │                                  │
                             ▼                                  │
                 ┌────────────────────────┐                     │
                 │   Cargo full?          │                     │
                 └────────────────────────┘                     │
                    │ No              │ Yes                     │
                    │                 ▼                         │
                    │      ┌─────────────────────┐              │
                    │      │ Request transport   │              │
                    │      │ from coordinator    │              │
                    │      └─────────────────────┘              │
                    │                 │                         │
                    │                 ▼                         │
                    │      ┌─────────────────────┐              │
                    │      │ Transfer ALL cargo  │              │
                    │      │ to transport        │              │
                    │      └─────────────────────┘              │
                    │                 │                         │
                    └────────┬────────┘                         │
                             │                                  │
                             └──────────────────────────────────┘
```

### Gas Transport Worker Loop

```
┌─────────────────────────────────────────────────────────────────┐
│                Gas Transport Worker Lifecycle                   │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
                    ┌─────────────────┐
                    │  Start Worker   │
                    └─────────────────┘
                              │
                              ▼
                 ┌────────────────────────┐
                 │ At gas giant location? │
                 └────────────────────────┘
                    │ No              │ Yes
                    ▼                 │
          ┌─────────────────┐         │
          │ Navigate to     │         │
          │ gas giant       │         │
          └─────────────────┘         │
                    │                 │
                    └────────┬────────┘
                             ▼
              ┌──────────────────────────┐
              │   Main Transport Loop    │◀─────────────────────┐
              └──────────────────────────┘                      │
                             │                                  │
                             ▼                                  │
                    ┌─────────────────┐                         │
                    │ Signal          │                         │
                    │ availability    │                         │
                    │ to coordinator  │                         │
                    └─────────────────┘                         │
                             │                                  │
                             ▼                                  │
                    ┌─────────────────┐                         │
                    │ Wait for cargo  │                         │
                    │ from siphon     │                         │
                    │ ships           │                         │
                    └─────────────────┘                         │
                             │                                  │
                             ▼ (cargo received)                 │
                    ┌─────────────────┐                         │
                    │ Query for       │                         │
                    │ factory with    │                         │
                    │ LOW gas supply  │                         │
                    └─────────────────┘                         │
                             │                                  │
                             ▼                                  │
                    ┌─────────────────┐                         │
                    │ Navigate to     │                         │
                    │ factory         │                         │
                    └─────────────────┘                         │
                             │                                  │
                             ▼                                  │
                    ┌─────────────────┐                         │
                    │ Dock and        │                         │
                    │ deliver cargo   │                         │
                    └─────────────────┘                         │
                             │                                  │
                             ▼                                  │
                 ┌────────────────────────┐                     │
                 │   Need refuel?         │                     │
                 └────────────────────────┘                     │
                    │ No              │ Yes                     │
                    │                 ▼                         │
                    │      ┌─────────────────┐                  │
                    │      │ Refuel          │                  │
                    │      └─────────────────┘                  │
                    │                 │                         │
                    └────────┬────────┘                         │
                             │                                  │
                             ▼                                  │
                    ┌─────────────────┐                         │
                    │ Return to       │                         │
                    │ gas giant       │                         │
                    └─────────────────┘                         │
                             │                                  │
                             └──────────────────────────────────┘
```

---

## SpaceTraders API

### Siphon Endpoint

```
POST /my/ships/{shipSymbol}/siphon
```

**Requirements:**
- Ship must be in orbit at a gas giant waypoint
- Ship must have siphon mounts installed
- Ship must have gas processor installed

**Request:** No body required

**Response:**
```json
{
  "data": {
    "cooldown": {
      "shipSymbol": "SHIP-1",
      "totalSeconds": 60,
      "remainingSeconds": 60,
      "expiration": "2024-01-01T12:00:00Z"
    },
    "siphon": {
      "shipSymbol": "SHIP-1",
      "yield": {
        "symbol": "LIQUID_HYDROGEN",
        "units": 10
      }
    },
    "cargo": {
      "capacity": 100,
      "units": 50,
      "inventory": [...]
    },
    "events": [...]
  }
}
```

**Common Gas Types:**
- `LIQUID_HYDROGEN` - Used in fuel refining
- `LIQUID_NITROGEN` - Used in manufacturing
- `HYDROCARBON` - Raw hydrocarbon gas

---

## Domain Layer

### `internal/domain/gas/gas_operation.go`

```go
package gas

import (
    "fmt"
    "time"

    "github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// OperationStatus represents the lifecycle state of a gas extraction operation
type OperationStatus string

const (
    OperationStatusPending   OperationStatus = "PENDING"
    OperationStatusRunning   OperationStatus = "RUNNING"
    OperationStatusCompleted OperationStatus = "COMPLETED"
    OperationStatusStopped   OperationStatus = "STOPPED"
    OperationStatusFailed    OperationStatus = "FAILED"
)

// Operation represents a gas extraction operation aggregate root
// It orchestrates siphon ships extracting from gas giants and transport ships
// delivering the cargo to manufacturing factories.
type Operation struct {
    id             string
    playerID       int
    gasGiant       string   // Waypoint symbol of the gas giant
    siphonShips    []string // Ships performing siphoning (need siphon mounts + gas processor)
    transportShips []string // Ships delivering to factories
    maxIterations  int      // -1 for infinite
    lifecycle      *shared.LifecycleStateMachine
}

// NewOperation creates a new gas extraction operation instance
func NewOperation(
    id string,
    playerID int,
    gasGiant string,
    siphonShips []string,
    transportShips []string,
    maxIterations int,
    clock shared.Clock,
) *Operation {
    // Copy slices to avoid external mutation
    siphoners := make([]string, len(siphonShips))
    copy(siphoners, siphonShips)

    transports := make([]string, len(transportShips))
    copy(transports, transportShips)

    return &Operation{
        id:             id,
        playerID:       playerID,
        gasGiant:       gasGiant,
        siphonShips:    siphoners,
        transportShips: transports,
        maxIterations:  maxIterations,
        lifecycle:      shared.NewLifecycleStateMachine(clock),
    }
}

// Getters
func (op *Operation) ID() string               { return op.id }
func (op *Operation) PlayerID() int            { return op.playerID }
func (op *Operation) GasGiant() string         { return op.gasGiant }
func (op *Operation) SiphonShips() []string    { return op.siphonShips }
func (op *Operation) TransportShips() []string { return op.transportShips }
func (op *Operation) MaxIterations() int       { return op.maxIterations }

// Lifecycle delegation...
func (op *Operation) Status() OperationStatus { /* ... */ }
func (op *Operation) Start() error            { /* ... */ }
func (op *Operation) Stop() error             { /* ... */ }
func (op *Operation) Complete() error         { /* ... */ }
func (op *Operation) Fail(err error) error    { /* ... */ }

// Validate checks all invariants for the gas operation
func (op *Operation) Validate() error {
    if len(op.siphonShips) == 0 {
        return fmt.Errorf("operation must have at least 1 siphon ship")
    }

    if len(op.transportShips) == 0 {
        return fmt.Errorf("operation must have at least 1 transport ship")
    }

    if op.gasGiant == "" {
        return fmt.Errorf("gas giant waypoint must be specified")
    }

    return nil
}
```

### `internal/domain/gas/ports.go`

```go
package gas

import "context"

// OperationRepository defines the persistence interface for gas operations
type OperationRepository interface {
    Add(ctx context.Context, op *Operation) error
    FindByID(ctx context.Context, id string, playerID int) (*Operation, error)
    Save(ctx context.Context, op *Operation) error
    Remove(ctx context.Context, id string, playerID int) error
    FindActive(ctx context.Context, playerID int) ([]*Operation, error)
    FindByStatus(ctx context.Context, playerID int, status OperationStatus) ([]*Operation, error)
}
```

---

## Application Layer

### Commands

| Command | Purpose |
|---------|---------|
| `SiphonResourcesCommand` | Low-level siphon action (orbit → siphon API → cooldown) |
| `RunSiphonWorkerCommand` | Continuous siphoning, transfers to transport when full |
| `RunGasTransportWorkerCommand` | Receives cargo from siphon ships, delivers to factories with LOW gas supply |
| `RunGasCoordinatorCommand` | Fleet-level orchestration |

### `SiphonResourcesCommand`

```go
type SiphonResourcesCommand struct {
    ShipSymbol string
    PlayerID   shared.PlayerID
}

type SiphonResourcesResponse struct {
    YieldSymbol      string
    YieldUnits       int
    CooldownDuration time.Duration
    Cargo            *navigation.CargoData
}

// Handler
func (h *SiphonResourcesHandler) Handle(ctx context.Context, req common.Request) (common.Response, error) {
    cmd := req.(*SiphonResourcesCommand)

    // 1. Ensure ship is in orbit (idempotent)
    ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
    if err != nil {
        return nil, err
    }

    if _, err := ship.EnsureInOrbit(); err != nil {
        if err := h.shipRepo.Orbit(ctx, ship, cmd.PlayerID); err != nil {
            return nil, err
        }
    }

    // 2. Call Siphon API
    result, err := h.apiClient.SiphonResources(ctx, cmd.ShipSymbol, token)
    if err != nil {
        return nil, err
    }

    // 3. Return yield and cooldown
    return &SiphonResourcesResponse{
        YieldSymbol:      result.YieldSymbol,
        YieldUnits:       result.YieldUnits,
        CooldownDuration: time.Duration(result.CooldownSeconds) * time.Second,
        Cargo:            result.Cargo,
    }, nil
}
```

### `FindFactoryForGasQuery`

```go
type FindFactoryForGasQuery struct {
    GasSymbol    string           // e.g., "LIQUID_HYDROGEN"
    SystemSymbol string           // Prefer factories in same system
    PlayerID     shared.PlayerID
}

type FindFactoryForGasResponse struct {
    FactoryWaypoint *shared.Waypoint
    SupplyLevel     string  // "SCARCE", "LIMITED", "MODERATE", "HIGH", "ABUNDANT"
    Distance        float64 // From gas giant
}

// Handler Logic:
// 1. Query MarketRepository for waypoints that IMPORT the gas type
// 2. Filter to waypoints with LOW/MODERATE supply (needs the gas)
// 3. Prefer same system (minimize travel)
// 4. Return nearest factory
```

---

## Adapter Layer

### API Client Addition

```go
// internal/adapters/api/client.go

// SiphonResources siphons gas from a gas giant
func (c *SpaceTradersClient) SiphonResources(
    ctx context.Context,
    shipSymbol string,
    token string,
) (*domainPorts.SiphonResult, error) {
    path := fmt.Sprintf("/my/ships/%s/siphon", shipSymbol)

    var response struct {
        Data struct {
            Siphon struct {
                ShipSymbol string `json:"shipSymbol"`
                Yield      struct {
                    Symbol string `json:"symbol"`
                    Units  int    `json:"units"`
                } `json:"yield"`
            } `json:"siphon"`
            Cooldown struct {
                ShipSymbol       string `json:"shipSymbol"`
                TotalSeconds     int    `json:"totalSeconds"`
                RemainingSeconds int    `json:"remainingSeconds"`
                Expiration       string `json:"expiration"`
            } `json:"cooldown"`
            Cargo struct {
                Capacity  int `json:"capacity"`
                Units     int `json:"units"`
                Inventory []struct {
                    Symbol      string `json:"symbol"`
                    Name        string `json:"name"`
                    Description string `json:"description"`
                    Units       int    `json:"units"`
                } `json:"inventory"`
            } `json:"cargo"`
        } `json:"data"`
    }

    if err := c.request(ctx, "POST", path, token, map[string]interface{}{}, &response); err != nil {
        return nil, fmt.Errorf("failed to siphon resources: %w", err)
    }

    // Convert cargo inventory
    inventory := make([]shared.CargoItem, len(response.Data.Cargo.Inventory))
    for i, item := range response.Data.Cargo.Inventory {
        inventory[i] = shared.CargoItem{
            Symbol:      item.Symbol,
            Name:        item.Name,
            Description: item.Description,
            Units:       item.Units,
        }
    }

    cargo := &navigation.CargoData{
        Capacity:  response.Data.Cargo.Capacity,
        Units:     response.Data.Cargo.Units,
        Inventory: inventory,
    }

    return &domainPorts.SiphonResult{
        ShipSymbol:      response.Data.Siphon.ShipSymbol,
        YieldSymbol:     response.Data.Siphon.Yield.Symbol,
        YieldUnits:      response.Data.Siphon.Yield.Units,
        CooldownSeconds: response.Data.Cooldown.RemainingSeconds,
        CooldownExpires: response.Data.Cooldown.Expiration,
        Cargo:           cargo,
    }, nil
}
```

### Ports Addition

```go
// internal/application/common/ports.go

// SiphonResult contains the result of a siphon operation
type SiphonResult struct {
    ShipSymbol      string
    YieldSymbol     string
    YieldUnits      int
    CooldownSeconds int
    CooldownExpires string
    Cargo           *navigation.CargoData
}
```

---

## Coordination Pattern

The gas extraction operation reuses the existing `ChannelCoordinator` pattern from mining:

```
Siphon Workers                   Coordinator Loop              Transport Workers
    ↓                                ↓                               ↓
RequestTransport()  ────→  siphonRequestChan
                            (prioritize by cargo level)
                                    ↓
                     Send transport via siphonAssignChan
                                    ↓
Receive transport ←────  siphonAssignChans[siphonSymbol]
    ↓
Transfer cargo
    ↓
NotifyTransferComplete() ─→ transferCompleteChan
                                    ↓
                         Notify transport of cargo received
                                    ↓
SignalAvailability() ←────  transportAvailabilityChan
                                    ↓
                    Update cargo level, add to available pool
```

---

## Manufacturing Integration

### Loose Coupling Design

The gas extraction operation integrates with manufacturing through **market data**, not direct coordination:

```
┌─────────────────────────────────────────────────────────────────┐
│                   Gas Extraction Operation                      │
│                                                                 │
│  Transport Worker:                                              │
│  1. Query MarketRepository for factories that IMPORT gas        │
│  2. Filter to factories with LOW/MODERATE supply                │
│  3. Select nearest factory in same system                       │
│  4. Navigate and deliver cargo                                  │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
                              │
                              │ Delivery increases
                              │ factory supply
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                   Manufacturing Operation                       │
│                                                                 │
│  SupplyMonitor:                                                 │
│  1. Periodically polls factory supply levels                    │
│  2. Detects when supply reaches HIGH/ABUNDANT                   │
│  3. Marks factory as ready for production                       │
│  4. Manufacturing proceeds                                      │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Benefits of Loose Coupling

1. **Resilience**: Gas operation can run independently of manufacturing state
2. **Simplicity**: No complex coordination channels between operations
3. **Reuse**: Leverages existing market data infrastructure
4. **Flexibility**: Manufacturing can use any gas source (market or siphoned)

---

## Database Schema

### Migration: `021_add_gas_operations_table.up.sql`

```sql
CREATE TABLE IF NOT EXISTS gas_operations (
    id TEXT NOT NULL,
    player_id INT NOT NULL,
    gas_giant TEXT NOT NULL,
    status TEXT DEFAULT 'PENDING',
    siphon_ships TEXT,      -- JSON array of ship symbols
    transport_ships TEXT,   -- JSON array of ship symbols
    max_iterations INT DEFAULT -1,
    last_error TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    started_at TIMESTAMP WITH TIME ZONE,
    stopped_at TIMESTAMP WITH TIME ZONE,
    PRIMARY KEY (id, player_id),
    FOREIGN KEY (player_id) REFERENCES players(id) ON DELETE CASCADE
);

CREATE INDEX idx_gas_operations_status ON gas_operations(player_id, status);
CREATE INDEX idx_gas_operations_gas_giant ON gas_operations(gas_giant);
```

### Rollback: `021_add_gas_operations_table.down.sql`

```sql
DROP INDEX IF EXISTS idx_gas_operations_gas_giant;
DROP INDEX IF EXISTS idx_gas_operations_status;
DROP TABLE IF EXISTS gas_operations;
```

### GORM Model

```go
type GasOperationModel struct {
    ID             string    `gorm:"primaryKey"`
    PlayerID       int       `gorm:"primaryKey"`
    GasGiant       string    `gorm:"not null"`
    Status         string    `gorm:"default:PENDING"`
    SiphonShips    string    // JSON array
    TransportShips string    // JSON array
    MaxIterations  int       `gorm:"default:-1"`
    LastError      string
    CreatedAt      time.Time
    UpdatedAt      time.Time
    StartedAt      *time.Time
    StoppedAt      *time.Time
}

func (GasOperationModel) TableName() string {
    return "gas_operations"
}
```

---

## CLI Interface

### Command Usage

```bash
# Explicit gas giant waypoint
spacetraders workflow gas-extraction \
    --gas-giant X1-ABC-GAS \
    --siphoners SIPHON-1,SIPHON-2 \
    --transports TRANSPORT-1 \
    [--force] \
    [--dry-run] \
    --agent AGENT_SYMBOL

# Auto-select nearest gas giant in same system
spacetraders workflow gas-extraction \
    --auto-select \
    --siphoners SIPHON-1,SIPHON-2 \
    --transports TRANSPORT-1 \
    --agent AGENT_SYMBOL
```

### Flags

| Flag | Description |
|------|-------------|
| `--gas-giant` | Explicit waypoint symbol (mutually exclusive with `--auto-select`) |
| `--auto-select` | Find nearest gas giant in the same system as the ships |
| `--siphoners` | Comma-separated ship symbols with siphon mounts + gas processor |
| `--transports` | Comma-separated transport ship symbols (deliver to factories) |
| `--force` | Skip confirmation prompts |
| `--dry-run` | Plan routes without executing |
| `--agent` | Agent symbol for player resolution |

### Auto-Selection Algorithm

```go
func autoSelectGasGiant(ships []*Ship, waypointRepo WaypointRepository) (*Waypoint, error) {
    // 1. Get system symbol from first siphon ship's current location
    systemSymbol := ships[0].CurrentLocation().SystemSymbol

    // 2. Query waypoints with type "GAS_GIANT" in that system
    waypoints, err := waypointRepo.FindBySystemAndType(ctx, systemSymbol, "GAS_GIANT")
    if err != nil {
        return nil, err
    }

    if len(waypoints) == 0 {
        return nil, fmt.Errorf("no gas giants found in system %s", systemSymbol)
    }

    // 3. Select nearest gas giant by Euclidean distance
    shipLocation := ships[0].CurrentLocation()
    nearest := waypoints[0]
    minDistance := shipLocation.DistanceTo(nearest)

    for _, wp := range waypoints[1:] {
        dist := shipLocation.DistanceTo(wp)
        if dist < minDistance {
            minDistance = dist
            nearest = wp
        }
    }

    return nearest, nil
}
```

---

## Implementation Plan

### Phase 1: API Client (Day 1)

1. Add `SiphonResources()` method to `internal/adapters/api/client.go`
2. Add `SiphonResult` DTO to `internal/application/common/ports.go`
3. Update `APIClient` interface

### Phase 2: Domain Layer (Day 1)

1. Create `internal/domain/gas/gas_operation.go`
2. Create `internal/domain/gas/ports.go`
3. Implement aggregate root with lifecycle state machine

### Phase 3: Persistence (Day 2)

1. Create migration `migrations/021_add_gas_operations_table.up.sql`
2. Create rollback `migrations/021_add_gas_operations_table.down.sql`
3. Add `GasOperationModel` to `internal/adapters/persistence/models.go`
4. Create `internal/adapters/persistence/gas_operation_repository.go`

### Phase 4: Application Commands (Days 2-3)

1. `SiphonResourcesCommand` - Low-level siphon action
2. `RunSiphonWorkerCommand` - Continuous siphoning worker
3. `RunGasTransportWorkerCommand` - Factory delivery worker
4. `RunGasCoordinatorCommand` - Fleet orchestration
5. `FindFactoryForGasQuery` - Factory selection

### Phase 5: gRPC & Protobuf (Day 4)

1. Update `pkg/proto/daemon/daemon.proto` with messages
2. Run `make proto` to regenerate
3. Create `internal/adapters/grpc/container_ops_gas.go`
4. Register handler in `server.go`

### Phase 6: CLI (Day 4)

1. Add `gas-extraction` subcommand to `internal/adapters/cli/workflow.go`
2. Implement flag parsing and validation
3. Add auto-selection logic

---

## Files Summary

### Files to Create

| Path | Purpose |
|------|---------|
| `internal/domain/gas/gas_operation.go` | Domain entity (aggregate root) |
| `internal/domain/gas/ports.go` | Repository interface |
| `internal/application/gas/commands/siphon_resources.go` | Siphon API command |
| `internal/application/gas/commands/run_siphon_worker.go` | Siphon worker loop |
| `internal/application/gas/commands/run_gas_transport_worker.go` | Transport worker (delivers to factories) |
| `internal/application/gas/commands/run_gas_coordinator.go` | Coordinator |
| `internal/application/gas/queries/find_factory_for_gas.go` | Find factory with LOW gas supply |
| `internal/adapters/persistence/gas_operation_repository.go` | Database repository |
| `internal/adapters/grpc/container_ops_gas.go` | gRPC operations |
| `migrations/021_add_gas_operations_table.up.sql` | Database migration |
| `migrations/021_add_gas_operations_table.down.sql` | Rollback migration |

### Files to Modify

| Path | Changes |
|------|---------|
| `internal/adapters/api/client.go` | Add `SiphonResources()` method |
| `internal/application/common/ports.go` | Add `SiphonResult` DTO |
| `internal/adapters/persistence/models.go` | Add `GasOperationModel` |
| `internal/adapters/cli/workflow.go` | Add `gas-extraction` subcommand |
| `pkg/proto/daemon/daemon.proto` | Add gRPC messages and method |
| `internal/adapters/grpc/server.go` | Register gas extraction handler |

---

## Key Differences from Mining Operation

| Aspect | Mining | Gas Extraction |
|--------|--------|----------------|
| **Target** | Asteroid fields | Gas giants |
| **API** | `POST /extract` | `POST /siphon` |
| **Ship requirements** | Mining laser | Siphon mounts + gas processor |
| **Cargo handling** | Jettison low-value ores | Keep all gases |
| **Delivery** | Sell at best market | Deliver to factories with LOW supply |
| **Purpose** | Profit maximization | Supply chain for manufacturing |
| **Price threshold** | Configurable (default 50) | Not applicable |
| **Integration** | Standalone | Feeds manufacturing via market data |

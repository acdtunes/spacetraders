# Bootstrap Operation Design

## Overview

The Bootstrap Operation is a coordinator that manages initial fleet expansion in SpaceTraders by running contracts to accumulate credits and purchasing light hauler ships until reaching a target fleet size. This document describes the architecture, state machine, and implementation details.

## Problem Statement

When starting a new game in SpaceTraders, players need to:
1. Run contracts to earn credits
2. Save up enough credits to purchase additional ships
3. Repeat until they have a full fleet of haulers

The first ship purchase requires stopping contract operations because the command ship (AGENT-1) must navigate to the shipyard. Subsequent purchases can happen in parallel since haulers are already running contracts independently.

## Requirements

### Functional Requirements

1. **Start contract operations** - Launch a contract fleet coordinator as a child operation
2. **Monitor credits** - Poll player credit balance every 30 seconds
3. **Dynamic thresholds** - Start at 400K credits, increase by 5% after each purchase
4. **First purchase behavior** - Stop contract operations, purchase ship, restart operations
5. **Subsequent purchases** - Purchase without stopping contract operations
6. **Target completion** - Continue until 15 total haulers exist
7. **Clean exit** - Bootstrap completes, contract operations continue running

### Non-Functional Requirements

1. **State recovery** - Full recovery on daemon restart
2. **Idempotency** - Safe to restart at any phase
3. **Observability** - Log all state transitions and purchases

## Architecture

### Hexagonal Architecture Placement

```
┌─────────────────────────────────────────────────────────────────┐
│                        CLI Layer                                 │
│  bootstrap.go: start, status, stop commands                      │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                     Application Layer                            │
│  run_bootstrap_coordinator.go: Main coordinator handler          │
│  types.go: Constants and command definitions                     │
└─────────────────────────────────────────────────────────────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
│  Domain Layer   │ │  Adapter Layer  │ │  Adapter Layer  │
│  bootstrap/     │ │  persistence/   │ │  grpc/          │
│  - operation.go │ │  - repository   │ │  - container_ops│
│  - ports.go     │ │  - models       │ │  - daemon_client│
└─────────────────┘ └─────────────────┘ └─────────────────┘
```

### Component Diagram

```
┌──────────────────────────────────────────────────────────────────┐
│                    Bootstrap Coordinator                          │
│                                                                   │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────────────┐   │
│  │   Credit    │    │   Phase     │    │   Child Container   │   │
│  │  Monitor    │───▶│   Manager   │───▶│     Manager         │   │
│  │  (30s poll) │    │             │    │                     │   │
│  └─────────────┘    └─────────────┘    └─────────────────────┘   │
│         │                  │                      │               │
│         ▼                  ▼                      ▼               │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────────────┐   │
│  │  API Client │    │  Bootstrap  │    │  Contract Fleet     │   │
│  │  (GetAgent) │    │  Repository │    │  Coordinator        │   │
│  └─────────────┘    └─────────────┘    └─────────────────────┘   │
└──────────────────────────────────────────────────────────────────┘
```

## State Machine

### Phase Definitions

| Phase | Description | Entry Condition | Exit Condition |
|-------|-------------|-----------------|----------------|
| `PHASE_INITIAL_CONTRACT` | Running contracts, waiting for first threshold | Bootstrap starts | Credits >= 400K |
| `PHASE_FIRST_PURCHASE` | Stop contract, purchase, restart | First threshold reached | Purchase complete |
| `PHASE_EXPANSION` | Contract running, purchase on threshold | First purchase done | 15 haulers reached |
| `PHASE_COMPLETED` | Goal reached, bootstrap exits | Target met | N/A (terminal) |

### State Transition Diagram

```
                              ┌─────────────────────────────┐
                              │                             │
                    Start ───▶│   PHASE_INITIAL_CONTRACT    │
                              │                             │
                              │  - Start contract coord     │
                              │  - Monitor credits (30s)    │
                              │  - Wait for 400K threshold  │
                              │                             │
                              └─────────────┬───────────────┘
                                            │
                              Credits >= 400K (first time)
                                            │
                                            ▼
                              ┌─────────────────────────────┐
                              │                             │
                              │    PHASE_FIRST_PURCHASE     │
                              │                             │
                              │  1. Stop contract coord     │
                              │  2. Wait for stop complete  │
                              │  3. Purchase LIGHT_HAULER   │
                              │  4. Update threshold (+5%)  │
                              │  5. Restart contract coord  │
                              │                             │
                              └─────────────┬───────────────┘
                                            │
                                  Purchase successful
                                            │
                                            ▼
                     ┌────────────────────────────────────────────┐
                     │                                            │
          ┌────────▶│          PHASE_EXPANSION                   │◀────────┐
          │         │                                            │         │
          │         │  - Contract coord running (not stopped)    │         │
          │         │  - Monitor credits (30s)                   │         │
          │         │  - On threshold: purchase (no stop)        │         │
          │         │  - Update threshold (+5% each time)        │         │
          │         │                                            │         │
          │         └─────────────────┬──────────────────────────┘         │
          │                           │                                    │
          │                     ┌─────┴─────┐                              │
          │                     │           │                              │
          │           Threshold reached   15 haulers                       │
          │           (< 15 haulers)      reached                          │
          │                     │           │                              │
          │                     ▼           ▼                              │
          │              Purchase ship    ┌─────────────────────────┐      │
          │              (no stop)        │                         │      │
          │                     │         │    PHASE_COMPLETED      │      │
          │                     │         │                         │      │
          └─────────────────────┘         │  - Bootstrap exits      │      │
                                          │  - Contract continues   │      │
                                          │  - Log completion       │      │
                                          │                         │      │
                                          └─────────────────────────┘      │
                                                                           │
                           Purchase complete (< 15 haulers) ───────────────┘
```

## Dynamic Credit Threshold

### Rationale

Ship prices in SpaceTraders increase as more ships are purchased. A fixed threshold would either:
- Be too low for later purchases (insufficient credits)
- Be too high for early purchases (wasted waiting time)

### Formula

```
threshold[n] = initialThreshold * (multiplier ^ n)

Where:
  initialThreshold = 400,000 credits
  multiplier = 1.03 (3% increase)
  n = number of purchases completed
```

### Example Progression

| Purchase # | Threshold | Cumulative Multiplier |
|------------|-----------|----------------------|
| 1 | 400,000 | 1.00x |
| 2 | 412,000 | 1.03x |
| 3 | 424,360 | 1.06x |
| 4 | 437,091 | 1.09x |
| 5 | 450,204 | 1.13x |
| 6 | 463,710 | 1.16x |
| 7 | 477,621 | 1.19x |
| 8 | 491,950 | 1.23x |
| 9 | 506,708 | 1.27x |
| 10 | 521,909 | 1.30x |
| 11 | 537,567 | 1.34x |
| 12 | 553,694 | 1.38x |
| 13 | 570,304 | 1.43x |
| 14 | 587,414 | 1.47x |

## Domain Model

### BootstrapOperation Entity

```go
type BootstrapOperation struct {
    // Identity
    id        string
    playerID  int

    // Configuration
    commandShipSymbol   string   // Ship used for purchases (e.g., AGENT-1)
    targetHaulerCount   int      // Default: 15
    initialThreshold    int      // Default: 400,000
    thresholdMultiplier float64  // Default: 1.03

    // State
    currentPhase       BootstrapPhase
    haulersPurchased   int
    currentThreshold   int      // Increases after each purchase

    // Child Container
    contractContainerID string  // ID of contract fleet coordinator

    // Lifecycle
    status      string  // PENDING, RUNNING, COMPLETED, FAILED
    createdAt   time.Time
    startedAt   *time.Time
    completedAt *time.Time
    lastPurchaseAt *time.Time
    lastError   string
}
```

### Key Methods

| Method | Description |
|--------|-------------|
| `NewBootstrapOperation()` | Factory with default configuration |
| `AdvancePhase(phase)` | State transition with validation |
| `RecordPurchase()` | Increment count, update threshold |
| `CalculateNextThreshold()` | Returns `current * multiplier` |
| `IsComplete()` | True if haulers >= target |
| `ShouldStopContract()` | True only in PHASE_FIRST_PURCHASE |
| `SetContractContainerID(id)` | Track child container |

## Coordinator Handler Logic

### Main Loop Pseudocode

```go
func (h *Handler) Handle(ctx context.Context, cmd Command) (*Response, error) {
    // 1. State Recovery
    bootstrapOp, err := h.loadOrCreateOperation(ctx, cmd)
    if err != nil {
        return nil, err
    }

    // 2. Count existing haulers (handles daemon restart)
    actualHaulers := h.countHaulers(ctx, cmd.PlayerID)
    if actualHaulers >= cmd.TargetHaulerCount {
        bootstrapOp.AdvancePhase(PhaseCompleted)
        h.repo.Update(ctx, bootstrapOp)
        return &Response{Status: "already_complete"}, nil
    }

    // 3. Start contract coordinator if needed
    if bootstrapOp.ContractContainerID == "" {
        containerID, err := h.startContractCoordinator(ctx, cmd)
        if err != nil {
            return nil, err
        }
        bootstrapOp.SetContractContainerID(containerID)
        h.repo.Update(ctx, bootstrapOp)
    }

    // 4. Credit monitoring loop
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            credits := h.fetchCredits(ctx, cmd.PlayerID)
            threshold := bootstrapOp.CurrentThreshold()

            if credits >= threshold {
                switch bootstrapOp.CurrentPhase() {
                case PhaseInitialContract:
                    // First purchase: stop, buy, restart
                    h.handleFirstPurchase(ctx, cmd, bootstrapOp)

                case PhaseExpansion:
                    // Subsequent: buy without stopping
                    h.handleExpansionPurchase(ctx, cmd, bootstrapOp)
                }

                // Check completion
                if h.countHaulers(ctx, cmd.PlayerID) >= cmd.TargetHaulerCount {
                    bootstrapOp.AdvancePhase(PhaseCompleted)
                    h.repo.Update(ctx, bootstrapOp)
                    return &Response{Status: "complete"}, nil
                }
            }

        case <-ctx.Done():
            return &Response{Status: "stopped"}, ctx.Err()
        }
    }
}
```

### First Purchase Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                    First Purchase Flow                           │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  1. Transition to PHASE_FIRST_PURCHASE                          │
│                              │                                   │
│                              ▼                                   │
│  2. Stop contract coordinator                                    │
│     └─ daemonClient.StopContainer(contractContainerID)          │
│                              │                                   │
│                              ▼                                   │
│  3. Wait for stop to complete                                    │
│     └─ Poll container status until STOPPED                      │
│                              │                                   │
│                              ▼                                   │
│  4. Execute purchase                                             │
│     └─ mediator.Send(PurchaseShipCommand{                       │
│            PurchasingShipSymbol: "AGENT-1",                     │
│            ShipType: "SHIP_LIGHT_HAULER",                       │
│        })                                                        │
│                              │                                   │
│                              ▼                                   │
│  5. Update state                                                 │
│     └─ RecordPurchase() // updates threshold                    │
│     └─ AdvancePhase(PhaseExpansion)                             │
│     └─ repo.Update(ctx, bootstrapOp)                            │
│                              │                                   │
│                              ▼                                   │
│  6. Restart contract coordinator                                 │
│     └─ startContractCoordinator(ctx, cmd)                       │
│     └─ SetContractContainerID(newID)                            │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Expansion Purchase Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                   Expansion Purchase Flow                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  1. Execute purchase (contract coordinator keeps running)        │
│     └─ mediator.Send(PurchaseShipCommand{                       │
│            PurchasingShipSymbol: "AGENT-1",                     │
│            ShipType: "SHIP_LIGHT_HAULER",                       │
│        })                                                        │
│                              │                                   │
│                              ▼                                   │
│  2. Update state                                                 │
│     └─ RecordPurchase() // updates threshold                    │
│     └─ repo.Update(ctx, bootstrapOp)                            │
│                              │                                   │
│                              ▼                                   │
│  3. Check completion                                             │
│     └─ if countHaulers() >= targetCount:                        │
│            AdvancePhase(PhaseCompleted)                          │
│            return (bootstrap exits, contract continues)          │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

## Database Schema

### Table: bootstrap_operations

```sql
CREATE TABLE bootstrap_operations (
    -- Primary Key (composite)
    id VARCHAR(64) NOT NULL,
    player_id INTEGER NOT NULL,

    -- Configuration
    command_ship_symbol VARCHAR(64) NOT NULL,
    target_hauler_count INTEGER NOT NULL DEFAULT 15,
    initial_threshold INTEGER NOT NULL DEFAULT 400000,
    threshold_multiplier DECIMAL(5,4) NOT NULL DEFAULT 1.03,

    -- State
    current_phase VARCHAR(32) NOT NULL DEFAULT 'PHASE_INITIAL_CONTRACT',
    haulers_purchased INTEGER NOT NULL DEFAULT 0,
    current_threshold INTEGER NOT NULL DEFAULT 400000,

    -- Child Container
    contract_container_id VARCHAR(64),

    -- Lifecycle
    status VARCHAR(32) NOT NULL DEFAULT 'PENDING',
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    started_at TIMESTAMP WITH TIME ZONE,
    completed_at TIMESTAMP WITH TIME ZONE,
    last_purchase_at TIMESTAMP WITH TIME ZONE,
    last_error TEXT,

    -- Constraints
    PRIMARY KEY (id, player_id),
    FOREIGN KEY (player_id) REFERENCES players(id)
        ON UPDATE CASCADE ON DELETE CASCADE,
    FOREIGN KEY (contract_container_id, player_id)
        REFERENCES containers(id, player_id)
        ON UPDATE CASCADE ON DELETE SET NULL,

    CONSTRAINT chk_bootstrap_phase CHECK (
        current_phase IN (
            'PHASE_INITIAL_CONTRACT',
            'PHASE_FIRST_PURCHASE',
            'PHASE_EXPANSION',
            'PHASE_COMPLETED'
        )
    ),
    CONSTRAINT chk_bootstrap_status CHECK (
        status IN ('PENDING', 'RUNNING', 'COMPLETED', 'FAILED')
    )
);

-- Indexes
CREATE INDEX idx_bootstrap_ops_player_status
    ON bootstrap_operations(player_id, status);
CREATE INDEX idx_bootstrap_ops_phase
    ON bootstrap_operations(current_phase);
```

## State Recovery

### Recovery Flow on Daemon Restart

```
┌─────────────────────────────────────────────────────────────────┐
│                    State Recovery Flow                           │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  1. Query for RUNNING bootstrap operations                       │
│     └─ SELECT * FROM bootstrap_operations                       │
│        WHERE player_id = ? AND status = 'RUNNING'               │
│                              │                                   │
│                              ▼                                   │
│  2. Verify hauler count from API                                 │
│     └─ ships := apiClient.GetShips()                            │
│     └─ actualHaulers := countByRole(ships, "HAULER")            │
│                              │                                   │
│                              ▼                                   │
│  3. Reconcile state if mismatch                                  │
│     └─ if actualHaulers != bootstrapOp.HaulersPurchased:        │
│            log.Warn("Hauler count mismatch")                    │
│            bootstrapOp.SetHaulerCount(actualHaulers)            │
│            repo.Update(ctx, bootstrapOp)                        │
│                              │                                   │
│                              ▼                                   │
│  4. Check if already complete                                    │
│     └─ if actualHaulers >= targetCount:                         │
│            AdvancePhase(PhaseCompleted)                          │
│            return                                                │
│                              │                                   │
│                              ▼                                   │
│  5. Check contract coordinator status                            │
│     └─ container := containerRepo.FindByID(contractContainerID) │
│     └─ if container == nil || container.Status == STOPPED:      │
│            startContractCoordinator(ctx, cmd)                   │
│                              │                                   │
│                              ▼                                   │
│  6. Resume credit monitoring from current phase                  │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Edge Cases

| Scenario | Recovery Action |
|----------|-----------------|
| Restart during purchase | Count haulers from API, resume monitoring |
| Contract coord missing | Start new contract coordinator |
| Phase mismatch | Trust API hauler count, adjust phase if needed |
| Threshold out of sync | Recalculate based on actual hauler count |

## CLI Interface

### Commands

```bash
# Start bootstrap operation
spacetraders bootstrap start \
    --command-ship AGENT-1 \
    [--threshold 400000] \
    [--multiplier 1.03] \
    [--target 15] \
    --player-id 1

# Check status
spacetraders bootstrap status --player-id 1

# Output:
# Bootstrap Operation Status
# ─────────────────────────────
# Phase:            PHASE_EXPANSION
# Haulers:          8/15
# Current Threshold: 491,950
# Next Purchase:    When credits reach 491,950
# Contract Status:  RUNNING (container-xyz)

# Stop bootstrap (contract continues)
spacetraders bootstrap stop --player-id 1
```

## Error Handling

### Error Categories

| Category | Examples | Handling |
|----------|----------|----------|
| Transient | API timeout, rate limit | Retry with backoff |
| Recoverable | Insufficient credits | Log, wait for next poll |
| Fatal | Invalid player, missing ship | Mark FAILED, exit |

### Retry Strategy

```go
// For transient errors
maxRetries := 3
backoff := []time.Duration{1*time.Second, 5*time.Second, 15*time.Second}

for attempt := 0; attempt < maxRetries; attempt++ {
    err := operation()
    if err == nil {
        break
    }
    if isTransient(err) && attempt < maxRetries-1 {
        time.Sleep(backoff[attempt])
        continue
    }
    return err
}
```

## Observability

### Log Events

| Event | Level | Fields |
|-------|-------|--------|
| Phase transition | INFO | `from_phase`, `to_phase`, `haulers` |
| Purchase initiated | INFO | `ship_type`, `threshold`, `credits` |
| Purchase completed | INFO | `new_ship`, `total_haulers`, `next_threshold` |
| Threshold increased | INFO | `previous`, `next`, `multiplier` |
| Credit check | DEBUG | `credits`, `threshold`, `below_by` |
| Error occurred | ERROR | `error`, `phase`, `will_retry` |
| Bootstrap completed | INFO | `total_haulers`, `total_purchases`, `duration` |

### Metrics (Prometheus)

```
# Gauges
spacetraders_bootstrap_haulers_total{player_id}
spacetraders_bootstrap_current_threshold{player_id}
spacetraders_bootstrap_phase{player_id, phase}

# Counters
spacetraders_bootstrap_purchases_total{player_id}
spacetraders_bootstrap_credit_checks_total{player_id}
spacetraders_bootstrap_errors_total{player_id, error_type}
```

## Testing Strategy

### Unit Tests

| Component | Test Focus |
|-----------|------------|
| BootstrapOperation | Phase transitions, threshold calculation |
| Handler | Mock dependencies, phase handling |
| Repository | CRUD operations, queries |

### Integration Tests

| Scenario | Description |
|----------|-------------|
| Happy path | Full flow from start to 15 haulers |
| Recovery | Restart daemon mid-operation |
| Edge cases | Already complete, insufficient credits |

### Test Doubles

- **Mock APIClient**: Return configurable credit balances
- **Mock DaemonClient**: Track container start/stop calls
- **Mock Repository**: In-memory state storage

## Implementation Checklist

### Phase 1: Domain Layer
- [ ] Create `internal/domain/bootstrap/bootstrap_operation.go`
- [ ] Create `internal/domain/bootstrap/ports.go`
- [ ] Add `ContainerTypeBootstrapCoordinator` to container types
- [ ] Write unit tests for domain entity

### Phase 2: Persistence Layer
- [ ] Create migration `XXX_add_bootstrap_operations.up.sql`
- [ ] Create `internal/adapters/persistence/bootstrap_repository.go`
- [ ] Add `BootstrapOperationModel` to models.go
- [ ] Write repository tests

### Phase 3: gRPC Layer
- [ ] Update `internal/domain/daemon/ports.go` with new methods
- [ ] Create `internal/adapters/grpc/container_ops_bootstrap.go`
- [ ] Implement methods in `daemon_client_local.go`
- [ ] Add stubs to `daemon_client_grpc.go`
- [ ] Add factory to `command_factory_registry.go`

### Phase 4: Application Layer
- [ ] Create `internal/application/bootstrap/commands/types.go`
- [ ] Create `internal/application/bootstrap/commands/run_bootstrap_coordinator.go`
- [ ] Add handler registration to `handler_registry.go`
- [ ] Write handler tests

### Phase 5: CLI Layer
- [ ] Create `internal/adapters/cli/bootstrap.go`
- [ ] Add command to `root.go`
- [ ] Update daemon `main.go` with bootstrap initialization

### Phase 6: Integration
- [ ] Run migration
- [ ] Manual testing of full flow
- [ ] Test daemon restart recovery
- [ ] Update CLAUDE.md if needed

## References

- [Contract Fleet Coordinator](../internal/application/contract/commands/run_fleet_coordinator.go) - Coordinator pattern reference
- [Purchase Ship Command](../internal/application/shipyard/commands/purchase_ship.go) - Ship purchase implementation
- [Container Operations](../internal/adapters/grpc/container_ops_contract.go) - gRPC container management pattern
- [DaemonClient Interface](../internal/domain/daemon/ports.go) - Interface to extend

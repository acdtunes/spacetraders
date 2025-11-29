# Cargo Storage System Design

## 1. Overview

A **generalized cargo storage system** that integrates with the manufacturing task system. Provides a reusable pattern for buffering any cargo type (gas, ore, refined goods) at a waypoint using dedicated storage ships, with on-demand delivery via task-based hauler assignment.

### Use Cases
- **Gas Extraction**: Siphon ships extract → storage ships buffer → haulers deliver to factories
- **Mining Operations**: Mining ships extract → storage ships buffer → haulers deliver to refineries
- **Resource Staging**: Any scenario requiring cargo buffering before delivery

### Key Benefits
- **Shared Fleet**: Idle haulers from manufacturing pool fulfill storage tasks
- **Demand-Driven**: Tasks created when destinations need cargo, not when storage is full
- **Database Persistence**: Tasks survive daemon restart
- **Reusable**: Same system works for gas, mining, and future resource types

---

## 2. Architecture

### Current vs Target

```
CURRENT (Operation-Specific):
┌─────────────────────────────────────────────────┐
│ GasCoordinator / MiningCoordinator (separate)   │
│                                                 │
│ ExtractWorker ──► ChannelCoordinator ◄── TransportWorker │
│     │               (in-memory)              │  │
│     ▼                                        ▼  │
│ [Waypoint]                            [Destination] │
└─────────────────────────────────────────────────┘
Problems: Dedicated transports, in-memory state, push-based, duplicate code

TARGET (Generalized Storage):
┌─────────────────────────────────────────────────┐
│      ManufacturingCoordinator (extended)        │
│                                                 │
│ ExtractWorker ──► StorageCoordinator ◄── Idle Haulers │
│     │             (multi-operation)        (pool) │
│     ▼                   │                       │
│ [Storage Ships]   [Task Queue DB]               │
│  (in orbit)             │                       │
│     │          STORAGE_ACQUIRE_DELIVER          │
│     ▼                   ▼                       │
│ [Waypoint]        [Destination]                 │
└─────────────────────────────────────────────────┘
```

### Component Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                    ManufacturingCoordinator                      │
│                                                                  │
│  ┌──────────────────┐    ┌──────────────────────────────────┐   │
│  │ StorageOperation │    │      StorageCoordinator          │   │
│  │    Registry      │    │  ┌────────────────────────────┐  │   │
│  │                  │    │  │ StorageShip[] per operation │  │   │
│  │  - Gas Op #1     │───►│  │ WaiterQueue[] per op+good  │  │   │
│  │  - Mining Op #2  │    │  │ Reservation tracking       │  │   │
│  └──────────────────┘    │  └────────────────────────────┘  │   │
│                          └──────────────────────────────────┘   │
│                                      ▲                          │
│                                      │                          │
│  ┌─────────────────┐    ┌────────────┴───────────┐              │
│  │ ExtractorWorkers│    │ StorageAcquireDeliver  │              │
│  │  (Siphon/Mine)  │───►│      Executor          │              │
│  │  deposit cargo  │    │  (waits, transfers,    │              │
│  └─────────────────┘    │   delivers)            │              │
│                         └────────────────────────┘              │
└─────────────────────────────────────────────────────────────────┘
```

---

## 3. Core Concepts

### 3.1 Storage Operation

Represents a location where cargo is extracted and buffered.

| Field | Description |
|-------|-------------|
| `id` | Unique identifier |
| `waypointSymbol` | Extraction location (gas giant, asteroid) |
| `operationType` | `GAS_SIPHON`, `MINING`, `CUSTOM` |
| `extractorShips` | Ships that extract resources |
| `storageShips` | Ships that buffer cargo (stay in orbit) |
| `supportedGoods` | Goods this operation produces |

### 3.2 Storage Ships

Dedicated ships that **stay in ORBIT** at the operation waypoint:
- Receive cargo from extractor ships
- Hold cargo until haulers collect
- Never navigate or dock
- Can hold any cargo type

### 3.3 STORAGE_ACQUIRE_DELIVER Task

New task type in manufacturing queue:
- Created when destination needs cargo from a storage operation
- Assigned to idle haulers by TaskAssignmentManager
- Hauler: navigate → wait for cargo → transfer → deliver

### 3.4 StorageCoordinator

In-memory coordinator managing **all** storage ships across **all** operations:
- Tracks cargo levels with reservation system
- Provides blocking `WaitForCargo()` for haulers
- Notifies waiting haulers when extractors deposit
- Filters by operation ID (supports multiple simultaneous operations)

---

## 4. Key Interfaces

### StorageCoordinator

```go
type StorageCoordinator interface {
    RegisterStorageShip(ship *StorageShip) error
    UnregisterStorageShip(shipSymbol string)

    // Blocks until cargo available and reserved. Returns with reservation HELD.
    WaitForCargo(ctx context.Context, operationID, goodSymbol string, minUnits int) (*StorageShip, int, error)

    // Called by extractors after depositing cargo. Wakes waiting haulers.
    NotifyCargoDeposited(storageShipSymbol, goodSymbol string, units int)

    // Query methods
    GetTotalCargoAvailable(operationID, goodSymbol string) int
    FindStorageShipWithSpace(operationID string, minSpace int) (*StorageShip, bool)
}
```

### StorageShip

```go
type StorageShip interface {
    ShipSymbol() string
    WaypointSymbol() string
    OperationID() string

    // Cargo operations (all mutex-protected)
    DepositCargo(goodSymbol string, units int) error
    GetAvailableCargo(goodSymbol string) int      // inventory - reserved
    ReserveCargo(goodSymbol string, units int) error
    ConfirmTransfer(goodSymbol string, units int)
    CancelReservation(goodSymbol string, units int)
    AvailableSpace() int
}
```

---

## 5. Race Condition Avoidance

### 5.1 Problem Scenarios

| Scenario | Description | Risk |
|----------|-------------|------|
| **Multiple haulers, same cargo** | Hauler A and B both want 30 units, only 30 available | Double allocation |
| **Deposit during reservation** | Siphon deposits while hauler is reserving | Inconsistent state |
| **Transfer failure** | API call fails after reservation | Leaked reservation |
| **Context cancellation** | Hauler cancelled while waiting | Orphaned waiter |

### 5.2 Synchronization Strategy

**Two-Level Locking:**

```
┌─────────────────────────────────────────────────────────┐
│              Coordinator Mutex (coarse)                  │
│  - Protects: storageShips map, waiters map              │
│  - Held during: registration, wait/notify orchestration │
│                                                          │
│    ┌─────────────────────────────────────────────────┐  │
│    │         StorageShip Mutex (fine)                │  │
│    │  - Protects: cargoInventory, reservedCargo      │  │
│    │  - Held during: individual cargo operations     │  │
│    └─────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────┘
```

### 5.3 Reservation Protocol

**Key Invariant:** Reservation is made BEFORE returning to caller.

```
WaitForCargo():
  1. Lock coordinator
  2. Try immediate reservation (atomic check+reserve under lock)
  3. If success: unlock, return with reservation HELD
  4. If fail: add to FIFO queue, unlock, block on channel
  5. When notified: reservation already made by NotifyCargoDeposited

NotifyCargoDeposited():
  1. Lock coordinator
  2. Deposit cargo to storage ship
  3. Process FIFO queue IN ORDER:
     - Try reserve for first waiter
     - If success: dequeue, send notification (reservation held)
     - If fail: stop (not enough for next waiter)
  4. Unlock
```

### 5.4 Failure Handling

| Failure | Handler | Recovery |
|---------|---------|----------|
| API transfer fails | Executor | `CancelReservation()` to release |
| Context cancelled during wait | Coordinator | Remove from waiter queue |
| Storage ship unregistered | Coordinator | Waiters notified with error |

### 5.5 FIFO Queue Guarantees

- Waiters served in arrival order (fair scheduling)
- Prevents starvation of long-waiting haulers
- Each (operationID, goodSymbol) has separate queue

---

## 6. Resilience Strategies

### 6.1 Daemon Restart Recovery

**What survives restart:**
- Storage operations (database)
- STORAGE_ACQUIRE_DELIVER tasks (database)
- Task assignments (database)

**What must be rebuilt:**
- StorageCoordinator (in-memory)
- StorageShip cargo state (query Ship API)
- Waiter queues (empty on restart)

**Recovery Flow:**
```
1. Load storage operations from DB
2. For each operation:
   a. Query Ship API for storage ship cargo
   b. Create StorageShip entities with actual cargo
   c. Register with StorageCoordinator
3. Resume extractor workers
4. Recover tasks (normal task recovery handles STORAGE_ACQUIRE_DELIVER)
   - ASSIGNED/EXECUTING → rollback to READY
   - Hauler retries from beginning
```

### 6.2 Task Failure Recovery

| Failure Point | State | Recovery |
|---------------|-------|----------|
| Navigation to storage waypoint | No cargo on hauler | Task fails, retry creates new task |
| WaitForCargo timeout | No reservation | Task fails, retry later |
| Transfer API fails | Reservation held | `CancelReservation()`, task fails |
| Navigation to destination | Cargo on hauler | Task fails, LIQUIDATE task created |
| Delivery fails | Cargo on hauler | Task fails, LIQUIDATE task created |

### 6.3 Orphaned Cargo Handling

If hauler has cargo from storage but task failed:
1. TaskAssignmentManager detects ship with cargo on next cycle
2. Creates LIQUIDATE task to sell cargo
3. Or: matches cargo with pending STORAGE_ACQUIRE_DELIVER for same good

### 6.4 Storage Ship Unavailable

If storage ship goes offline (crashed, fuel empty):
1. Unregister from coordinator
2. Waiting haulers for that ship's operation notified
3. Other storage ships in operation continue serving
4. Manual intervention to recover ship

---

## 7. Data Flow

### 7.1 Extractor → Storage Flow

```
┌──────────────┐     ┌─────────────────┐     ┌─────────────┐
│   Extractor  │     │   Coordinator   │     │ StorageShip │
│   Worker     │     │                 │     │  (in orbit) │
└──────┬───────┘     └────────┬────────┘     └──────┬──────┘
       │                      │                     │
       │ FindStorageWithSpace │                     │
       │─────────────────────>│                     │
       │                      │ Check capacity      │
       │                      │────────────────────>│
       │<─────────────────────│                     │
       │     storageShip      │                     │
       │                      │                     │
       │ TransferCargo (API)  │                     │
       │─────────────────────────────────────────────────────>│
       │                      │                     │
       │ NotifyCargoDeposited │                     │
       │─────────────────────>│                     │
       │                      │ DepositCargo        │
       │                      │────────────────────>│
       │                      │                     │
       │                      │ Wake waiters        │
       │                      │ (if any)            │
```

### 7.2 Hauler Acquisition Flow

```
┌─────────────┐    ┌──────────────┐    ┌─────────────────┐    ┌─────────────┐
│ TaskAssign  │    │   Executor   │    │   Coordinator   │    │ StorageShip │
│   Manager   │    │              │    │                 │    │             │
└──────┬──────┘    └──────┬───────┘    └────────┬────────┘    └──────┬──────┘
       │                  │                     │                    │
       │ Assign task      │                     │                    │
       │─────────────────>│                     │                    │
       │                  │                     │                    │
       │                  │ Navigate to waypoint│                    │
       │                  │ (ensure in orbit)   │                    │
       │                  │                     │                    │
       │                  │ WaitForCargo        │                    │
       │                  │────────────────────>│                    │
       │                  │                     │ ReserveCargo       │
       │                  │                     │───────────────────>│
       │                  │                     │<───────────────────│
       │                  │<────────────────────│ (reservation held) │
       │                  │   storageShip       │                    │
       │                  │                     │                    │
       │                  │ TransferCargo (API) │                    │
       │                  │──────────────────────────────────────────>│
       │                  │                     │                    │
       │                  │ ConfirmTransfer     │                    │
       │                  │────────────────────────────────────────────>│
       │                  │                     │                    │
       │                  │ Navigate to destination                  │
       │                  │ Deliver cargo       │                    │
       │                  │                     │                    │
       │<─────────────────│ Task complete       │                    │
```

---

## 8. Integration Points

### 8.1 SupplyMonitor

When factory needs an input:
1. Check if input is from a storage operation (query by good)
2. If yes: create `STORAGE_ACQUIRE_DELIVER` task
3. If no: create regular `ACQUIRE_DELIVER` task

```
createAcquireDeliverTasksForFactory():
  for each input:
    storageOp = findStorageOperationForGood(input.good)
    if storageOp != nil:
      createStorageAcquireDeliverTask(storageOp, input.good, factory)
    else:
      createAcquireDeliverTask(input.good, sourceMarket, factory)
```

### 8.2 Manufacturing Coordinator

On initialization (if storage operations configured):
1. Create StorageCoordinator instance
2. Register storage ships (load cargo from API)
3. Start extractor workers (siphon/mining)
4. Register StorageAcquireDeliverExecutor

### 8.3 Extractor Workers

Modify existing siphon/mining workers:
- Replace transport coordinator with storage coordinator
- Call `NotifyCargoDeposited()` after successful transfer
- No blocking wait for transport assignment

---

## 9. Database Schema

### 9.1 Storage Operations Table

```sql
CREATE TABLE storage_operations (
    id VARCHAR(64) PRIMARY KEY,
    player_id INTEGER NOT NULL REFERENCES players(id),
    waypoint_symbol VARCHAR(64) NOT NULL,
    operation_type VARCHAR(32) NOT NULL,  -- GAS_SIPHON, MINING, CUSTOM
    extractor_ships TEXT NOT NULL,         -- JSON array
    storage_ships TEXT NOT NULL,           -- JSON array
    supported_goods TEXT NOT NULL,         -- JSON array
    status VARCHAR(32) NOT NULL DEFAULT 'PENDING',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_storage_ops_player ON storage_operations(player_id);
CREATE INDEX idx_storage_ops_waypoint ON storage_operations(waypoint_symbol);
```

### 9.2 Task Type Constraint Update

```sql
ALTER TABLE manufacturing_tasks
    DROP CONSTRAINT IF EXISTS manufacturing_tasks_task_type_check;

ALTER TABLE manufacturing_tasks
    ADD CONSTRAINT manufacturing_tasks_task_type_check
    CHECK (task_type IN (
        'ACQUIRE_DELIVER',
        'COLLECT_SELL',
        'LIQUIDATE',
        'STORAGE_ACQUIRE_DELIVER'
    ));
```

---

## 10. File Structure

```
internal/
├── domain/
│   └── storage/                          # NEW bounded context
│       ├── operation.go                  # StorageOperation entity
│       ├── storage_ship.go               # StorageShip entity
│       └── ports.go                      # Interfaces
│
├── application/
│   └── storage/                          # NEW
│       └── coordinator.go                # InMemoryStorageCoordinator
│
├── application/trading/services/manufacturing/
│   └── storage_acquire_deliver_executor.go  # NEW executor
│
├── application/trading/services/
│   └── supply_monitor.go                 # MODIFY: detect storage goods
│
├── application/gas/commands/
│   └── run_siphon_worker.go              # MODIFY: use storage coordinator
│
├── adapters/persistence/
│   ├── models.go                         # ADD: StorageOperationModel
│   └── storage_operation_repository.go   # NEW
│
migrations/
├── 022_add_storage_operations_table.up.sql
└── 023_add_storage_task_type.up.sql
```

---

## 11. Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **Separate `storage` bounded context** | Reusable across gas/mining/future operations |
| **Single coordinator for all operations** | Simpler management, shared hauler pool |
| **Operation ID filtering** | Prevents cross-operation cargo mixing |
| **Reservation-before-return** | Eliminates race between wait and transfer |
| **FIFO waiter queues** | Fair scheduling, prevents starvation |
| **Storage ships stay in orbit** | Required for cargo transfers |
| **In-memory coordinator, DB operations** | Fast coordination, persistent config |
| **Same priority as ACQUIRE_DELIVER** | Equal treatment in task queue |

---

## 12. Testing Strategy

### Unit Tests
- StorageShip: concurrent reservation/cancellation, inventory accuracy
- StorageCoordinator: FIFO ordering, operation filtering, context cancellation

### Integration Tests
- End-to-end: extractor deposits → hauler waits → transfer → delivery
- Recovery: daemon restart with pending tasks
- Failure: API transfer fails, reservation released

### Race Condition Tests
- Multiple haulers waiting for same good
- Concurrent deposits from multiple extractors
- Mixed wait/deposit/cancel operations

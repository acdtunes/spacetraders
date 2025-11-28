# Refactoring Plan: daemon_server.go

## Problem Statement

`internal/adapters/grpc/daemon_server.go` is **2987 lines** and violates the Single Responsibility Principle. It handles:
- Server lifecycle management
- Metrics server
- Container recovery
- Command factory registry
- 47 public methods across 10+ different domains (ship, contract, arbitrage, mining, scouting, manufacturing, etc.)

## Refactoring Strategy

Split the file by domain/responsibility into **10 new files** while keeping core server logic in `daemon_server.go`. All files will be in the same `grpc` package, allowing methods to remain on `*DaemonServer` without interface changes.

### File Structure After Refactoring

```
internal/adapters/grpc/
├── daemon_server.go                    (~550 lines) - Core server infrastructure
├── command_factory_registry.go         (~300 lines) - Command factory definitions
├── container_ops_ship.go               (~170 lines) - Navigate, Dock, Orbit, Refuel
├── container_ops_contract.go           (~220 lines) - Contract workflow operations
├── container_ops_arbitrage.go          (~230 lines) - Arbitrage coordinator/worker
├── container_ops_manufacturing.go      (~350 lines) - Manufacturing operations
├── container_ops_mining.go             (~400 lines) - Mining coordinator/worker/transport
├── container_ops_scouting.go           (~200 lines) - Scout tour, markets, fleet
├── container_ops_purchase.go           (~120 lines) - Ship purchases
├── container_ops_goods.go              (~170 lines) - Goods factory operations
├── container_ops_queries.go            (~180 lines) - ListShips, GetShip, GetShipyardListings
├── container_runner.go                 (existing - unchanged)
├── daemon_service_impl.go              (existing - unchanged)
├── daemon_client_grpc.go               (existing - unchanged)
├── daemon_client_local.go              (existing - unchanged)
└── type_converters.go                  (existing - unchanged)
```

## Detailed Implementation

### 1. daemon_server.go (Keep ~550 lines)

**Contains:**
- Package imports (shared imports only)
- `CommandFactory` type definition
- `MetricsCollector` interface
- `DaemonServer` struct definition
- `NewDaemonServer()` constructor
- `Start()` method
- `startMetricsServer()`, `stopMetricsServer()`, `handleShutdown()`
- `RecoverRunningContainers()`, `recoverContainer()`, `markContainerFailed()`
- Container management: `ListContainers()`, `GetContainer()`, `StopContainer()`
- Container lifecycle: `registerContainer()`, `interruptAllContainers()`, `stopAllContainers()`
- Helper: `containerModelToShipAssignment()`

### 2. command_factory_registry.go (~300 lines)

**Extract from lines 246-546:**
- `registerCommandFactories()` method
- All factory function definitions for:
  - `scout_tour`
  - `contract_workflow`
  - `contract_fleet_coordinator`
  - `arbitrage_coordinator`
  - `purchase_ship`
  - `batch_purchase_ships`
  - `mining_worker`
  - `transport_worker`
  - `mining_coordinator`
  - `goods_factory_coordinator`

### 3. container_ops_ship.go (~170 lines)

**Extract from lines 893-1055:**
- `NavigateShip()` - Ship navigation
- `DockShip()` - Ship docking
- `OrbitShip()` - Ship orbiting
- `RefuelShip()` - Ship refueling

**Imports needed:**
- `container` (domain)
- `shipCmd`, `shipTypes` (application/ship)
- `shared` (domain/shared)
- `utils` (pkg/utils)

### 4. container_ops_contract.go (~220 lines)

**Extract from lines 1058-1249:**
- `BatchContractWorkflow()` - Batch contract operations
- `ContractWorkflow()` - Single contract workflow
- `PersistContractWorkflow()` - Persist to DB
- `StartContractWorkflow()` - Start persisted container
- `ContractFleetCoordinator()` - Fleet coordination

**Imports needed:**
- `container` (domain)
- `contractCmd` (application/contract/commands)
- `shared` (domain/shared)
- `persistence`, `utils`

### 5. container_ops_arbitrage.go (~230 lines)

**Extract from lines 1251-1481:**
- `ArbitrageCoordinator()` - Create/start coordinator
- `PersistArbitrageWorkerContainer()` - Persist worker
- `StartArbitrageWorkerContainer()` - Start worker

**Imports needed:**
- `container`, `daemon` (domain)
- `tradingCmd` (application/trading/commands)
- `trading`, `shared` (domain)
- `persistence`, `utils`

### 6. container_ops_manufacturing.go (~350 lines)

**Extract from lines 1484-1766:**
- `PersistManufacturingWorkerContainer()` - Persist worker
- `StartManufacturingWorkerContainer()` - Start worker
- `ManufacturingCoordinator()` - Create/start coordinator
- `ParallelManufacturingCoordinator()` - Parallel variant
- `getSupplyChainResolver()` - Helper

**Imports needed:**
- `container` (domain)
- `tradingCmd` (application/trading/commands)
- `goodsServices` (application/goods/services)
- `trading`, `shared`, `goods` (domain)
- `persistence`, `utils`

### 7. container_ops_mining.go (~400 lines)

**Extract from lines 1778-1877 and 2467-2851:**
- `MiningOperation()` - Create mining operation
- `MiningOperationResult` struct
- `extractSystemSymbol()` helper
- `PersistMiningWorkerContainer()` - Persist miner worker
- `StartMiningWorkerContainer()` - Start miner worker
- `PersistTransportWorkerContainer()` - Persist transport worker
- `StartTransportWorkerContainer()` - Start transport worker
- `PersistMiningCoordinatorContainer()` - Persist coordinator
- `StartMiningCoordinatorContainer()` - Start coordinator

**Imports needed:**
- `container` (domain)
- `miningCmd` (application/mining/commands)
- `shared` (domain/shared)
- `common` (application/common)
- `persistence`, `utils`

### 8. container_ops_scouting.go (~200 lines)

**Extract from lines 1902-2068:**
- `ScoutTour()` - Single ship scouting
- `TourSell()` - Cargo selling tour
- `ScoutMarkets()` - Fleet market scouting
- `AssignScoutingFleet()` - Fleet assignment

**Imports needed:**
- `container` (domain)
- `scoutingCmd`, `tradingCmd` (application)
- `shared` (domain/shared)
- `utils`

### 9. container_ops_purchase.go (~120 lines)

**Extract from lines 2357-2464:**
- `PurchaseShip()` - Single ship purchase
- `BatchPurchaseShips()` - Batch ship purchases

**Imports needed:**
- `container` (domain)
- `shipyardCmd` (application/shipyard/commands)
- `shared` (domain/shared)
- `utils`

### 10. container_ops_goods.go (~170 lines)

**Extract from lines 2854-2987:**
- `StartGoodsFactory()` - Start goods factory
- `StopGoodsFactory()` - Stop factory
- `GetFactoryStatus()` - Get factory status
- `GoodsFactoryResult` struct
- `GoodsFactoryStatus` struct

**Imports needed:**
- `container` (domain)
- `goodsCmd` (application/goods/commands)
- `utils`

### 11. container_ops_queries.go (~180 lines)

**Extract from lines 2229-2354:**
- `ListShips()` - List ships
- `GetShip()` - Get ship details
- `GetShipyardListings()` - Get shipyard listings

**Imports needed:**
- `shipQuery`, `shipyardQuery` (application)
- `shared` (domain/shared)
- `pb` (proto/daemon)

## Implementation Order

1. **Create `command_factory_registry.go`** - Extract factory definitions (no behavior change)
2. **Create `container_ops_queries.go`** - Simple extraction, least dependencies
3. **Create `container_ops_ship.go`** - Basic ship operations
4. **Create `container_ops_purchase.go`** - Ship purchasing
5. **Create `container_ops_scouting.go`** - Scouting operations
6. **Create `container_ops_goods.go`** - Goods factory
7. **Create `container_ops_contract.go`** - Contract operations
8. **Create `container_ops_arbitrage.go`** - Arbitrage operations
9. **Create `container_ops_manufacturing.go`** - Manufacturing operations
10. **Create `container_ops_mining.go`** - Mining operations (largest)
11. **Clean up `daemon_server.go`** - Remove extracted code, consolidate imports

## Verification Steps

After each extraction:
1. `go build ./...` - Ensure no compile errors
2. `make test-bdd` - Ensure all BDD tests pass
3. Verify no duplicate method definitions

## Benefits

1. **Reduced cognitive load** - Each file focuses on one domain
2. **Easier navigation** - Find operations by domain name
3. **Better maintainability** - Changes isolated to relevant files
4. **Improved testability** - Clearer boundaries for unit testing
5. **Follows existing patterns** - Similar to persistence layer organization

## Non-Goals

- **No interface changes** - All methods stay on `*DaemonServer`
- **No behavior changes** - Pure extraction refactoring
- **No new abstractions** - Keep current design patterns
- **No test changes** - BDD tests remain unchanged

## Risk Assessment

**Low Risk:**
- Same package, same receiver type, same method signatures
- No changes to daemon_service_impl.go (gRPC adapter)
- No changes to container_runner.go
- No domain layer changes

**Mitigation:**
- Extract one file at a time
- Build and test after each extraction
- Commit incrementally if needed

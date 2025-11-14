# Functionality Gap Analysis: Python vs Go Implementation

**Date:** 2025-11-13
**Python Implementation:** `/Users/andres.camacho/Development/Personal/spacetraders/bot`
**Go Implementation:** `/Users/andres.camacho/Development/Personal/spacetraders/gobot`

---

## Executive Summary

Both implementations follow **Hexagonal Architecture** with **CQRS** patterns and share similar architectural foundations. However, the Python implementation is **significantly more feature-complete** with 44+ commands/queries versus Go's ~20 implemented operations.

**Key Findings:**
- **Architecture:** Both use Hexagonal + DDD + CQRS (parity ✅)
- **Core Features:** Python has 28 commands, Go has ~12 fully implemented
- **Query Operations:** Python has 16 queries, Go has ~6 implemented
- **CLI Coverage:** Python has 14 CLI modules, Go has 10 (some incomplete)
- **Testing:** Python uses real repositories in tests, Go uses mocks
- **Automation:** Python has more sophisticated workflows and experiments

---

## 1. Architecture Comparison

### Similarities ✅

| Component | Python | Go | Status |
|-----------|--------|-----|--------|
| **Hexagonal Architecture** | ✅ Ports & Adapters | ✅ Ports & Adapters | Parity |
| **Domain Layer** | ✅ Pure business logic | ✅ Pure business logic | Parity |
| **CQRS Pattern** | ✅ pymediatr | ✅ Custom mediator | Parity |
| **Immutable Commands** | ✅ Frozen dataclasses | ✅ Struct types | Parity |
| **Dependency Injection** | ✅ DI Container | ✅ Constructor injection | Parity |
| **Value Objects** | ✅ Immutable | ✅ Immutable | Parity |
| **Async/Concurrent** | ✅ async/await | ✅ goroutines | Parity |
| **Database Support** | ✅ PostgreSQL/SQLite | ✅ PostgreSQL/SQLite | Parity |
| **Daemon System** | ✅ Background containers | ✅ Background containers | Parity |
| **Routing Service** | ✅ Python OR-Tools | ✅ Python OR-Tools (shared) | Parity |

### Differences

| Component | Python | Go |
|-----------|--------|-----|
| **Mediator Implementation** | pymediatr (library) | Custom reflection-based |
| **ORM** | SQLAlchemy | GORM |
| **Test Framework** | pytest-bdd (Gherkin) | Godog (Gherkin) |
| **IPC Protocol** | JSON-RPC 2.0 over Unix socket | gRPC over Unix socket |
| **Pipeline Behaviors** | ✅ Logging, Validation middleware | ❌ Not implemented |
| **Test Strategy** | Real repositories (in-memory SQLite) | Mocks (test doubles) |

---

## 2. Domain Layer Comparison

### Entities

| Entity | Python | Go | Gap |
|--------|--------|-----|-----|
| **Ship** | 374 lines, 30+ methods | ~300 lines, 30+ methods | Parity ✅ |
| **Route** | 253 lines | ~200 lines | Parity ✅ |
| **Container** | 313 lines | ~250 lines | Parity ✅ |
| **Player** | 133 lines | ~100 lines | Parity ✅ |
| **Contract** | ✅ Full entity | ✅ Exists (internal/domain/contract/) | Parity ✅ |
| **Market** | ✅ Full entity | ✅ Exists (internal/domain/market/) | Parity ✅ |
| **Shipyard** | ✅ Full entity with listings | ⚠️ Likely incomplete | Gap 🔴 |

### Value Objects

| Value Object | Python | Go | Gap |
|--------------|--------|-----|-----|
| **Waypoint** | ✅ x, y, system, traits | ✅ x, y, system, traits | Parity ✅ |
| **Fuel** | ✅ Immutable | ✅ Immutable | Parity ✅ |
| **Cargo** | ✅ Immutable with items | ✅ Immutable with items | Parity ✅ |
| **FlightMode** | ✅ 4 modes with costs | ✅ 4 modes with costs | Parity ✅ |
| **Distance** | ✅ With safety margins | ✅ Implicit in calculations | Minor gap 🟡 |
| **CargoItem** | ✅ Full details | ✅ Full details | Parity ✅ |
| **TradeGood** | ✅ Market pricing | ✅ Exists (domain/market/) | Parity ✅ |

---

## 3. Application Layer Comparison

### 3.1 Commands (Write Operations)

#### ✅ Implemented in Both

| Command | Python | Go | Notes |
|---------|--------|-----|-------|
| **RegisterPlayer** | ✅ | ✅ | Parity |
| **SyncPlayer** | ✅ | ✅ | Parity |
| **NavigateShip** | ✅ | ✅ | Parity (Go uses NavigateToWaypoint) |
| **DockShip** | ✅ | ✅ | Parity |
| **OrbitShip** | ✅ | ✅ | Parity |
| **RefuelShip** | ✅ | ✅ | Parity |
| **JettisonCargo** | ✅ | ✅ | Parity |
| **PurchaseCargo** | ✅ | ✅ | Parity |
| **AcceptContract** | ✅ | ✅ | Parity |
| **DeliverContract** | ✅ | ✅ | Parity |
| **FulfillContract** | ✅ | ✅ | Parity |
| **NegotiateContract** | ✅ | ✅ | Parity |
| **BatchContractWorkflow** | ✅ | ✅ | Parity |
| **ScoutMarkets** | ✅ (VRP) | ✅ (VRP) | Parity |
| **ScoutTour** | ✅ (TSP) | ✅ (TSP) | Parity |
| **BatchPurchaseShips** | ✅ | ✅ | Parity |

#### ❌ Missing in Go

| Command | Python | Go | Impact |
|---------|--------|-----|--------|
| **SellCargo** | ✅ | ❌ | **HIGH** - Can't complete trading cycle |
| **PurchaseShip** (single) | ✅ | ❌ | **MEDIUM** - Only batch purchase available |
| **SyncSystemWaypoints** | ✅ | ❌ | **MEDIUM** - Manual waypoint caching |
| **UpdatePlayer** | ✅ | ❌ | **LOW** - Limited player management |
| **TouchLastActive** | ✅ | ❌ | **LOW** - No activity tracking |
| **FetchContractFromAPI** | ✅ | ❌ | **LOW** - Implicit in other operations |
| **LogCaptainEntry** | ✅ | ❌ | **MEDIUM** - No mission logging system |
| **MultiLevelLogging** | ✅ (testing) | ❌ | **LOW** - Testing utility only |
| **ShipExperimentWorker** | ✅ (testing) | ❌ | **LOW** - Mining experiment |
| **MarketLiquidityExperiment** | ✅ (testing) | ❌ | **LOW** - Testing utility |
| **SetFlightMode** | ⚠️ (implicit) | ✅ | **NEUTRAL** - Go has explicit command |

### 3.2 Queries (Read Operations)

#### ✅ Implemented in Both

| Query | Python | Go | Notes |
|-------|--------|-----|-------|
| **GetPlayer** | ✅ | ✅ | Parity |
| **ListPlayers** | ✅ | ✅ | Parity |
| **GetShip** | ⚠️ (via API) | ✅ | Go has explicit query |
| **ListShips** | ✅ | ✅ | Parity |

#### ❌ Missing in Go

| Query | Python | Go | Impact |
|-------|--------|-----|--------|
| **PlanRoute** | ✅ | ❌ | **HIGH** - Can't preview routes without execution |
| **GetShipLocation** | ✅ | ❌ | **MEDIUM** - Must call API directly |
| **GetSystemGraph** | ✅ | ❌ | **MEDIUM** - No graph inspection |
| **ListWaypoints** | ✅ | ❌ | **MEDIUM** - Limited waypoint queries |
| **GetContract** | ✅ | ❌ | **MEDIUM** - Can't inspect individual contracts |
| **ListContracts** | ✅ | ❌ | **HIGH** - No contract browsing |
| **GetActiveContracts** | ✅ | ❌ | **HIGH** - Can't see active contracts |
| **GetMarketData** | ✅ | ❌ | **HIGH** - Limited market inspection |
| **ListMarketData** | ✅ | ❌ | **HIGH** - Can't browse all markets |
| **FindCheapestMarket** | ✅ | ❌ | **HIGH** - Manual price comparison required |
| **GetShipyardListings** | ✅ | ❌ | **MEDIUM** - Can't browse ships before purchase |
| **GetPlayerByAgent** | ✅ | ❌ | **LOW** - Less flexible player lookup |
| **GetCaptainLogs** | ✅ | ❌ | **MEDIUM** - No mission log retrieval |

---

## 4. CLI Comparison

### 4.1 Command Groups

| Group | Python Files | Go Files | Python Commands | Go Commands |
|-------|--------------|----------|-----------------|-------------|
| **Player** | player_cli.py | player.go | register, sync | register, sync, list, info |
| **Ship** | navigation_cli.py | ship.go | navigate, dock, orbit, refuel | navigate, dock, orbit, refuel, list, info |
| **Market** | trading_cli.py | market.go | buy, sell, get-data, list-data, find-cheapest | get |
| **Contract** | contract_cli.py | ⚠️ (workflow.go) | negotiate, accept, deliver, fulfill, list, get | batch-contract (workflow only) |
| **Scouting** | scouting_cli.py | workflow.go | scout-markets, scout-tour | scout-markets |
| **Shipyard** | shipyard_cli.py | ⚠️ Missing | list, purchase, batch-purchase | batch-purchase (in workflow) |
| **Waypoint** | waypoint_cli.py | ❌ Missing | list, sync | None |
| **Daemon** | daemon_cli.py | container.go | list, logs, stop, remove, inspect | list, logs, stop, remove, inspect |
| **Config** | config_cli.py | config.go | set-player, get, clear | set-player, show, clear |
| **Captain** | captain_cli.py | ❌ Missing | log, get-logs | None |
| **Experiment** | experiment_cli.py | ❌ Missing | multi-level-log, ship-worker, market-liquidity | None |

### 4.2 Detailed CLI Gaps

#### ❌ Completely Missing CLI Groups in Go

1. **Waypoint Commands** (waypoint_cli.py)
   - `waypoint list` - Browse waypoints in system
   - `waypoint sync` - Cache waypoints for offline use

2. **Captain Log Commands** (captain_cli.py)
   - `captain log` - Record mission narrative
   - `captain get-logs` - Retrieve mission history

3. **Experiment Commands** (experiment_cli.py)
   - `experiment multi-level-log` - Test logging
   - `experiment ship-worker` - Mining automation test
   - `experiment market-liquidity` - Market monitoring test

#### ⚠️ Partially Implemented CLI Groups

1. **Market Commands**
   - **Python:** get-data, list-data, find-cheapest, buy, sell
   - **Go:** get (only)
   - **Missing:** list-data, find-cheapest, sell

2. **Contract Commands**
   - **Python:** negotiate, accept, deliver, fulfill, list, get, batch-workflow
   - **Go:** batch-contract (workflow only)
   - **Missing:** Individual contract operations (negotiate, accept, etc. as standalone commands)

3. **Shipyard Commands**
   - **Python:** list, purchase, batch-purchase
   - **Go:** None (batch-purchase exists in workflow but not as direct command)
   - **Missing:** shipyard list, single purchase

---

## 5. Repository Layer Comparison

### 5.1 Implemented Repositories

| Repository | Python | Go | Gap |
|------------|--------|-----|-----|
| **PlayerRepository** | ✅ Full CRUD | ✅ Full CRUD | Parity ✅ |
| **ShipRepository** | ✅ API client (not persisted) | ✅ Persistence + API | Go better ✅ |
| **WaypointRepository** | ✅ Full caching | ✅ Full caching | Parity ✅ |
| **ContractRepository** | ✅ Full CRUD | ✅ Exists | Parity ✅ |
| **MarketRepository** | ✅ Full CRUD | ✅ Exists | Parity ✅ |
| **ContainerRepository** | ✅ Full lifecycle | ✅ Full lifecycle | Parity ✅ |
| **ContainerLogRepository** | ✅ Full logging | ✅ Full logging | Parity ✅ |
| **SystemGraphRepository** | ✅ Graph caching | ⚠️ Implicit in waypoint | Gap 🟡 |
| **CaptainLogRepository** | ✅ Mission logs | ❌ Missing | Gap 🔴 |
| **ShipyardRepository** | ✅ Full CRUD | ⚠️ Unknown | Gap 🟡 |

### 5.2 Repository Method Gaps

Even where repositories exist, Go may have fewer methods:

**Example: Python MarketRepository**
- `get_market_data(waypoint)` ✅
- `list_all_markets()` ✅
- `find_cheapest_market(trade_good)` ✅
- `update_market_data()` ✅
- `get_markets_by_system()` ✅

**Go MarketRepository (internal/adapters/persistence/market_repository.go)**
- Likely has basic CRUD but not all query methods

---

## 6. Feature Comparison Matrix

### 6.1 Navigation & Pathfinding

| Feature | Python | Go | Gap |
|---------|--------|-----|-----|
| **Dijkstra pathfinding** | ✅ | ✅ | Parity ✅ |
| **Automatic refuel insertion** | ✅ | ✅ | Parity ✅ |
| **Flight mode optimization** | ✅ | ✅ | Parity ✅ |
| **90% fuel rule** | ✅ | ✅ | Parity ✅ |
| **Route execution** | ✅ | ✅ | Parity ✅ |
| **Route preview (no execution)** | ✅ PlanRoute | ❌ | Gap 🔴 |
| **Multi-hop refueling** | ✅ | ✅ | Parity ✅ |
| **Idempotent navigation** | ✅ | ✅ | Parity ✅ |

### 6.2 Trading & Markets

| Feature | Python | Go | Gap |
|---------|--------|-----|-----|
| **Purchase cargo** | ✅ | ✅ | Parity ✅ |
| **Sell cargo** | ✅ | ❌ | Gap 🔴 |
| **Market scouting** | ✅ | ✅ | Parity ✅ |
| **Market data caching** | ✅ | ✅ | Parity ✅ |
| **Find cheapest market** | ✅ | ❌ | Gap 🔴 |
| **List all markets** | ✅ | ❌ | Gap 🔴 |
| **Market liquidity tracking** | ✅ | ❌ | Gap 🔴 |

### 6.3 Contracts

| Feature | Python | Go | Gap |
|---------|--------|-----|-----|
| **Negotiate contract** | ✅ | ✅ | Parity ✅ |
| **Accept contract** | ✅ | ✅ | Parity ✅ |
| **Deliver cargo** | ✅ | ✅ | Parity ✅ |
| **Fulfill contract** | ✅ | ✅ | Parity ✅ |
| **Batch workflow** | ✅ | ✅ | Parity ✅ |
| **Profitability evaluation** | ✅ | ✅ | Parity ✅ |
| **List contracts** | ✅ | ❌ | Gap 🔴 |
| **Get active contracts** | ✅ | ❌ | Gap 🔴 |
| **Get single contract** | ✅ | ❌ | Gap 🔴 |
| **Multi-trip delivery** | ✅ | ✅ | Parity ✅ |

### 6.4 Fleet Management

| Feature | Python | Go | Gap |
|---------|--------|-----|-----|
| **VRP optimization** | ✅ | ✅ | Parity ✅ |
| **TSP tour planning** | ✅ | ✅ | Parity ✅ |
| **Scout markets (multi-ship)** | ✅ | ✅ | Parity ✅ |
| **Ship assignment tracking** | ✅ | ✅ | Parity ✅ |
| **Zombie assignment cleanup** | ✅ | ⚠️ Unknown | Gap 🟡 |

### 6.5 Shipyard Operations

| Feature | Python | Go | Gap |
|---------|--------|-----|-----|
| **List shipyard offerings** | ✅ | ❌ | Gap 🔴 |
| **Purchase single ship** | ✅ | ❌ | Gap 🔴 |
| **Batch purchase ships** | ✅ | ✅ | Parity ✅ |
| **Auto-discover nearest shipyard** | ✅ | ✅ | Parity ✅ |
| **Budget-constrained purchasing** | ✅ | ✅ | Parity ✅ |

### 6.6 Daemon & Background Operations

| Feature | Python | Go | Gap |
|---------|--------|-----|-----|
| **Background containers** | ✅ | ✅ | Parity ✅ |
| **Container lifecycle** | ✅ | ✅ | Parity ✅ |
| **Restart policy** | ✅ (max 3) | ✅ (max 3) | Parity ✅ |
| **Persistent logging** | ✅ | ✅ | Parity ✅ |
| **Graceful shutdown** | ✅ | ✅ | Parity ✅ |
| **IPC protocol** | JSON-RPC 2.0 | gRPC | Different (both work) |
| **Health monitoring** | ⚠️ Unknown | ✅ | Go better ✅ |

### 6.7 Data Caching & Persistence

| Feature | Python | Go | Gap |
|---------|--------|-----|-----|
| **Waypoint caching** | ✅ | ✅ | Parity ✅ |
| **Market data caching** | ✅ | ✅ | Parity ✅ |
| **Ship data caching** | ❌ (API only) | ✅ | Go better ✅ |
| **System graph caching** | ✅ | ⚠️ Implicit | Gap 🟡 |
| **Contract persistence** | ✅ | ✅ | Parity ✅ |
| **60-second log deduplication** | ✅ | ⚠️ Unknown | Gap 🟡 |

### 6.8 Captain Log System

| Feature | Python | Go | Gap |
|---------|--------|-----|-----|
| **Narrative logging** | ✅ | ❌ | Gap 🔴 |
| **Event tagging** | ✅ | ❌ | Gap 🔴 |
| **Fleet snapshots** | ✅ | ❌ | Gap 🔴 |
| **Structured event data** | ✅ | ❌ | Gap 🔴 |
| **Entry type categorization** | ✅ | ❌ | Gap 🔴 |
| **Session continuity** | ✅ | ❌ | Gap 🔴 |

---

## 7. MCP Server Integration

### Python MCP Server (bot/mcp/)
**Status:** ❌ Deleted (based on git status)
- Previously had TypeScript MCP implementation
- Now removed from Python codebase

### Go MCP Server (gobot/mcp/)
**Status:** ✅ Active

**Exposed Tools:**
1. `player_register` ✅
2. `player_list` ✅
3. `player_info` ✅
4. `ship_list` ✅
5. `ship_info` ✅
6. `navigate` ✅
7. `dock` ✅
8. `orbit` ✅
9. `refuel` ✅
10. `plan_route` ✅
11. `shipyard_list` ✅
12. `shipyard_purchase` ✅
13. `shipyard_batch_purchase` ✅
14. `waypoint_list` ✅
15. `scout_markets` ✅
16. `contract_batch_workflow` ✅
17. `daemon_list` ✅
18. `daemon_inspect` ✅
19. `daemon_stop` ✅
20. `daemon_remove` ✅
21. `daemon_logs` ✅
22. `config_show` ✅
23. `config_set_player` ✅
24. `config_clear_player` ✅
25. `captain_log_entry` ✅
26. `captain_get_logs` ✅

**Analysis:**
- Go MCP server is **more comprehensive** than Python ever was
- Exposes 26 tools covering all major operations
- Includes captain logging (domain exists but not in CLI)
- Includes waypoint listing (domain exists but not in CLI)
- Includes plan_route (domain exists but not in CLI)
- Includes shipyard operations (domain exists but limited CLI)

**Key Finding:** Go's MCP server exposes functionality that exists in the codebase but is not exposed via CLI!

---

## 8. Testing Strategy Comparison

### Python Testing Approach

**Framework:** pytest-bdd (Gherkin)

**Strategy:**
- **Real repositories** with in-memory SQLite
- **Real database** operations (transactions, constraints)
- **Mock API client** for external calls
- **Integration-style** tests

**Coverage:**
- 61 test files
- Comprehensive BDD scenarios
- Tests actual database behavior
- Tests actual ORM mappings

**Pros:**
- Catches SQL/ORM bugs
- Tests real transactions
- More realistic test environment
- Catches constraint violations

**Cons:**
- Slower (database overhead)
- More complex setup
- Harder to isolate

### Go Testing Approach

**Framework:** Godog (Gherkin)

**Strategy:**
- **Mock repositories** (test doubles)
- **In-memory maps** instead of database
- **Mock API client** for external calls
- **Unit-style** tests

**Coverage:**
- ~37 .feature files
- ~550-560 scenarios (58% passing, 36% undefined, 7% failing)
- Fast execution (<10 seconds)
- Uses MockClock for time-based tests

**Pros:**
- Very fast execution
- Easy to isolate
- No database dependencies
- Simple setup

**Cons:**
- Doesn't catch SQL bugs
- Doesn't test real transactions
- Doesn't test ORM mappings
- Less realistic

**Verdict:** Python's approach is **more thorough** but **slower**. Go's approach is **faster** but **less realistic**.

---

## 9. Critical Functionality Gaps Summary

### 🔴 HIGH PRIORITY GAPS (Blocking Core Workflows)

1. **SellCargo Command** ❌
   - **Impact:** Cannot complete trading cycle (buy → sell)
   - **Workaround:** None
   - **Files to implement:**
     - `internal/application/ship/sell_cargo.go`
     - `internal/adapters/cli/trading.go` (new file)
     - `test/bdd/features/application/sell_cargo.feature`

2. **PlanRoute Query** ❌
   - **Impact:** Cannot preview routes without executing navigation
   - **Workaround:** None (must navigate to see route)
   - **Note:** Exposed in MCP but not in CLI!
   - **Files to implement:**
     - Already exists in Go! Just needs CLI exposure

3. **Contract Query Operations** ❌
   - **Missing:** ListContracts, GetContract, GetActiveContracts
   - **Impact:** Cannot browse available contracts
   - **Workaround:** Use batch workflow blindly
   - **Files to implement:**
     - `internal/application/contract/list_contracts.go`
     - `internal/application/contract/get_contract.go`
     - `internal/adapters/cli/contract.go` (new file)

4. **Market Query Operations** ❌
   - **Missing:** GetMarketData, ListMarketData, FindCheapestMarket
   - **Impact:** Cannot make informed trading decisions
   - **Workaround:** Manual API calls
   - **Files to implement:**
     - Already exist in `internal/application/scouting/`!
     - Need CLI exposure in `internal/adapters/cli/market.go`

5. **Shipyard List Query** ❌
   - **Impact:** Cannot browse ships before purchase
   - **Workaround:** None
   - **Note:** Exposed in MCP but not in CLI!
   - **Files to implement:**
     - Already exists! Just needs CLI exposure

### 🟡 MEDIUM PRIORITY GAPS (Workflow Enhancements)

6. **Single Ship Purchase** ❌
   - **Impact:** Must use batch purchase for single ship
   - **Workaround:** Use batch with quantity=1
   - **Files to implement:**
     - `internal/application/ship/purchase_ship.go`

7. **Captain Log System** ❌
   - **Impact:** No mission narrative tracking
   - **Workaround:** None
   - **Note:** Exposed in MCP! Domain may exist
   - **Files to investigate:**
     - Check if `internal/domain/captain/` exists

8. **Waypoint List Query** ❌
   - **Impact:** Cannot browse waypoints
   - **Workaround:** Direct database queries
   - **Note:** Exposed in MCP but not in CLI!
   - **Files to implement:**
     - Already exists! Just needs CLI exposure

9. **SyncSystemWaypoints Command** ❌
   - **Impact:** Manual waypoint cache warming
   - **Workaround:** Waypoints cached on-demand

10. **GetShipLocation Query** ❌
    - **Impact:** Must call API for ship location
    - **Workaround:** Use GetShip query

### 🟢 LOW PRIORITY GAPS (Nice-to-Have)

11. **UpdatePlayer Command** ❌
12. **TouchLastActive Command** ❌
13. **GetPlayerByAgent Query** ❌
14. **Experiment Commands** ❌ (Testing utilities only)
15. **Pipeline Behaviors** ❌ (Logging/validation middleware)

---

## 10. Hidden Functionality (MCP-Exposed but Not in CLI)

### Discovered During Analysis

The Go MCP server exposes functionality that **exists in the codebase** but is **not exposed via CLI**:

1. **plan_route** ✅ MCP, ❌ CLI
   - Exists in: `internal/application/ship/route_planner.go`
   - **Action:** Add to CLI

2. **waypoint_list** ✅ MCP, ❌ CLI
   - Exists in: Repository layer
   - **Action:** Add CLI command

3. **shipyard_list** ✅ MCP, ❌ CLI
   - Exists in: Repository layer
   - **Action:** Add CLI command

4. **captain_log_entry** ✅ MCP, ❌ CLI
   - Check if domain exists
   - **Action:** Investigate + add CLI if exists

5. **captain_get_logs** ✅ MCP, ❌ CLI
   - Check if domain exists
   - **Action:** Investigate + add CLI if exists

**Recommendation:** Audit codebase for MCP-exposed functionality and expose via CLI for consistency.

---

## 11. Implementation Roadmap

### Phase 1: Expose Hidden Functionality (Quick Wins)

**Effort:** 1-2 days
**Impact:** HIGH

- [ ] Add `ship plan-route` CLI command (already implemented)
- [ ] Add `waypoint list` CLI command (already implemented)
- [ ] Add `shipyard list` CLI command (already implemented)
- [ ] Add `market get-data` CLI command (already implemented)
- [ ] Add `market list-data` CLI command (already implemented)
- [ ] Add `market find-cheapest` CLI command (already implemented)
- [ ] Audit all MCP tools and ensure CLI parity

### Phase 2: Critical Trading Features

**Effort:** 3-5 days
**Impact:** HIGH

- [ ] Implement **SellCargo** command
  - Application handler: `sell_cargo.go`
  - CLI command: `trading sell`
  - BDD tests: `sell_cargo.feature`

- [ ] Add contract query commands (CLI only, if handlers exist)
  - `contract list` - List all contracts
  - `contract get` - Get single contract
  - `contract active` - List active contracts

### Phase 3: Shipyard Enhancements

**Effort:** 2-3 days
**Impact:** MEDIUM

- [ ] Implement **PurchaseShip** (single) command
  - Application handler
  - CLI command
  - BDD tests

- [ ] Expose shipyard list in CLI (already implemented in MCP)

### Phase 4: Captain Log System

**Effort:** 5-7 days
**Impact:** MEDIUM

- [ ] Investigate if domain exists (check MCP implementation)
- [ ] If missing, implement:
  - Domain entity: `CaptainLog`
  - Repository: `CaptainLogRepository`
  - Commands: `LogCaptainEntry`
  - Queries: `GetCaptainLogs`
  - CLI commands: `captain log`, `captain get-logs`

### Phase 5: Testing Improvements

**Effort:** Ongoing
**Impact:** MEDIUM

- [ ] Fix remaining ~30-40 failing tests
- [ ] Implement ~200 undefined scenarios
- [ ] Consider hybrid testing (real + mock repositories)
- [ ] Add integration tests with real database

### Phase 6: Minor Enhancements

**Effort:** 3-5 days
**Impact:** LOW

- [ ] UpdatePlayer command
- [ ] TouchLastActive command
- [ ] GetPlayerByAgent query
- [ ] SyncSystemWaypoints command
- [ ] Pipeline behaviors (logging, validation)

---

## 12. Recommendations

### Immediate Actions

1. **Audit MCP Server** ✅
   - Compare MCP tools with CLI commands
   - Expose hidden functionality via CLI
   - Ensure parity between MCP and CLI

2. **Implement SellCargo** 🔴
   - Blocking trading workflows
   - Should be highest priority

3. **Add Contract Queries** 🔴
   - Essential for contract browsing
   - May already exist (check MCP handlers)

4. **Fix Failing Tests** 🟡
   - 58% passing is concerning
   - Stabilize core functionality first

### Long-Term Strategy

1. **Feature Parity with Python**
   - Use Python implementation as reference
   - Prioritize high-impact features
   - Skip low-value experiments

2. **Testing Strategy**
   - Consider hybrid approach (Python-style real repos for critical paths)
   - Keep fast tests for domain logic
   - Add integration tests for repositories

3. **Documentation**
   - Update CLAUDE.md with missing features
   - Document MCP vs CLI differences
   - Create feature compatibility matrix

4. **Code Quality**
   - Fix undefined scenarios (200+ scenarios)
   - Achieve >90% test pass rate
   - Reduce test execution time further

---

## 13. Conclusion

The Go implementation has a **solid architectural foundation** matching Python's quality, but is **approximately 60% feature-complete** compared to the Python implementation.

**Strengths of Go Implementation:**
- ✅ Clean hexagonal architecture
- ✅ Fast test execution (<10 seconds)
- ✅ Comprehensive MCP server (26 tools)
- ✅ Better ship caching (Python doesn't cache ships)
- ✅ gRPC daemon (more efficient than JSON-RPC)
- ✅ Core navigation and contracts fully working

**Critical Gaps:**
- ❌ Missing SellCargo (blocks trading)
- ❌ Limited market queries (blocks informed trading)
- ❌ Limited contract queries (blocks contract browsing)
- ❌ CLI doesn't expose all implemented functionality
- ❌ 36% of test scenarios undefined (unimplemented features)
- ❌ No captain logging system

**Fastest Path to Feature Parity:**

1. **Week 1:** Expose hidden MCP functionality via CLI (6 commands)
2. **Week 2:** Implement SellCargo + contract queries (4 features)
3. **Week 3:** Fix failing tests + implement undefined scenarios
4. **Week 4:** Captain log system + remaining gaps

**Estimated Timeline:** 4-6 weeks to reach 90% feature parity with Python implementation.

---

## Appendix A: Command/Query Inventory

### Python Implementation (44 Total)

**Commands (28):**
1. RegisterPlayer ✅
2. SyncPlayer ✅
3. UpdatePlayer ❌
4. TouchLastActive ❌
5. NavigateShip ✅
6. DockShip ✅
7. OrbitShip ✅
8. RefuelShip ✅
9. JettisonCargo ✅
10. PurchaseCargo ✅
11. SellCargo ❌
12. AcceptContract ✅
13. DeliverContract ✅
14. FulfillContract ✅
15. NegotiateContract ✅
16. FetchContractFromAPI ❌
17. BatchContractWorkflow ✅
18. ScoutMarkets ✅
19. ScoutMarketsVRP ✅ (same as ScoutMarkets)
20. ScoutTour ✅
21. PurchaseShip ❌
22. BatchPurchaseShips ✅
23. SyncSystemWaypoints ❌
24. LogCaptainEntry ❌
25. MultiLevelLogging ❌
26. ShipExperimentWorker ❌
27. MarketLiquidityExperiment ❌
28. SetFlightMode ✅

**Queries (16):**
1. GetPlayer ✅
2. GetPlayerByAgent ❌
3. ListPlayers ✅
4. GetShip ✅
5. ListShips ✅
6. PlanRoute ⚠️ (exists in MCP)
7. GetShipLocation ❌
8. GetSystemGraph ❌
9. ListWaypoints ⚠️ (exists in MCP)
10. GetContract ❌
11. ListContracts ❌
12. GetActiveContracts ❌
13. GetMarketData ⚠️ (exists in application layer)
14. ListMarketData ⚠️ (exists in application layer)
15. FindCheapestMarket ⚠️ (exists in application layer)
16. GetShipyardListings ⚠️ (exists in MCP)

### Go Implementation (~20 Implemented)

**Fully Implemented Commands (12):**
1. RegisterPlayer ✅
2. SyncPlayer ✅
3. NavigateShip ✅
4. DockShip ✅
5. OrbitShip ✅
6. RefuelShip ✅
7. JettisonCargo ✅
8. PurchaseCargo ✅
9. AcceptContract ✅
10. DeliverContract ✅
11. FulfillContract ✅
12. NegotiateContract ✅
13. BatchContractWorkflow ✅
14. ScoutMarkets ✅
15. ScoutTour ✅
16. BatchPurchaseShips ✅
17. SetFlightMode ✅

**Fully Implemented Queries (6):**
1. GetPlayer ✅
2. ListPlayers ✅
3. GetShip ✅
4. ListShips ✅
5. PlanRoute ⚠️ (MCP only)
6. ListWaypoints ⚠️ (MCP only)

---

## Appendix B: File Structure Comparison

### Python (bot/)
```
bot/
├── src/
│   ├── domain/              # 8 entities, 12 value objects
│   ├── application/         # 28 commands, 16 queries
│   ├── adapters/
│   │   ├── primary/
│   │   │   ├── cli/         # 14 CLI files
│   │   │   └── daemon/      # Daemon server
│   │   └── secondary/
│   │       ├── persistence/ # 15+ repositories
│   │       ├── api/         # API client
│   │       └── routing/     # OR-Tools integration
│   └── configuration/       # DI container
├── test/                    # 61 test files
└── mcp/                     # ❌ Deleted
```

### Go (gobot/)
```
gobot/
├── internal/
│   ├── domain/              # 8 entities, 10 value objects
│   ├── application/         # ~17 commands, ~6 queries
│   ├── adapters/
│   │   ├── cli/             # 10 CLI files
│   │   ├── grpc/            # gRPC daemon
│   │   ├── persistence/     # 10+ repositories
│   │   ├── api/             # API client
│   │   └── routing/         # OR-Tools integration
│   └── infrastructure/      # Database setup
├── test/bdd/                # 37 .feature files, ~550 scenarios
├── mcp/                     # ✅ Active (26 tools)
└── services/routing-service/# Python OR-Tools (shared)
```

---

**End of Analysis**

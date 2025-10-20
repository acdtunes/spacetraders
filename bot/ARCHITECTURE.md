# SpaceTraders Bot - Architecture Documentation

**Generated:** 2025-10-20
**Purpose:** Document module relationships, dependencies, and architectural patterns for test coverage strategy

---

## Table of Contents

1. [High-Level Architecture](#high-level-architecture)
2. [Layer Diagram](#layer-diagram)
3. [Core Components](#core-components)
4. [Operations Layer](#operations-layer)
5. [Data Flow](#data-flow)
6. [Module Dependency Graph](#module-dependency-graph)
7. [Test Coverage Strategy](#test-coverage-strategy)

---

## High-Level Architecture

The SpaceTraders bot follows a **3-layer architecture** with clear separation of concerns:

```mermaid
graph TB
    subgraph "CLI Layer"
        CLI[main.py<br/>CLI Entry Point]
    end

    subgraph "Operations Layer"
        MINING[mining.py<br/>Mining Operations]
        TRADING[multileg_trader.py<br/>Trading Operations]
        SCOUTING[scouting/*<br/>Market Intelligence]
        CONTRACTS[contracts.py<br/>Contract Fulfillment]
        FLEET[fleet.py<br/>Fleet Management]
        ASSIGN[assignments.py<br/>Ship Assignment]
        DAEMON[daemon.py<br/>Background Processes]
    end

    subgraph "Core Layer"
        API[api_client.py<br/>SpaceTraders API]
        SHIP[ship_controller.py<br/>Ship State Machine]
        NAV[smart_navigator.py<br/>Intelligent Navigation]
        ROUTE[routing.py<br/>Graph & Route Planning]
        ORTOOLS[ortools_router.py<br/>OR-Tools Optimization]
        DB[database.py<br/>SQLite Persistence]
        DAEMON_MGR[daemon_manager.py<br/>Process Management]
        ASSIGN_MGR[assignment_manager.py<br/>Ship Registry]
    end

    subgraph "External"
        SPACETRADERS[SpaceTraders API]
        SQLITE[(SQLite DB)]
    end

    CLI --> MINING & TRADING & SCOUTING & CONTRACTS & FLEET & ASSIGN & DAEMON
    MINING & TRADING & SCOUTING & CONTRACTS & FLEET --> SHIP & NAV & DB
    ASSIGN --> ASSIGN_MGR
    DAEMON --> DAEMON_MGR
    SHIP --> API
    NAV --> ROUTE & ORTOOLS
    API --> SPACETRADERS
    DB --> SQLITE
    DAEMON_MGR --> SQLITE
    ASSIGN_MGR --> DB
```

---

## Layer Diagram

```mermaid
graph LR
    subgraph "Layer 1: CLI"
        A[User Commands]
    end

    subgraph "Layer 2: Operations"
        B[Business Logic<br/>Operation Handlers]
    end

    subgraph "Layer 3: Core"
        C[Reusable Components<br/>API, Navigation, Data]
    end

    subgraph "External"
        D[SpaceTraders API<br/>SQLite Database]
    end

    A --> B
    B --> C
    C --> D

    style A fill:#e1f5ff
    style B fill:#fff3e0
    style C fill:#e8f5e9
    style D fill:#f3e5f5
```

**Design Principles:**
- **CLI Layer**: Thin argument parsing, delegates to operations
- **Operations Layer**: Domain-specific workflows (mining, trading, scouting)
- **Core Layer**: Reusable primitives (API client, navigation, ship control)
- **External**: Third-party services and persistence

---

## Core Components

### Core Module Relationships

```mermaid
graph TB
    subgraph "API & Data"
        API[api_client.py<br/>Rate-Limited HTTP]
        DB[database.py<br/>SQLite ORM]
        MARKET[market_data.py<br/>Market Intelligence]
    end

    subgraph "Ship Control"
        SHIP[ship_controller.py<br/>State Machine]
        NAV[smart_navigator.py<br/>Fuel-Aware Routes]
    end

    subgraph "Routing"
        GRAPH[routing.py<br/>Graph Builder]
        ORTOOLS[ortools_router.py<br/>VRP/TSP Solver]
        VALIDATOR[routing_validator.py<br/>Route Validation]
        CONFIG[routing_config.py<br/>Hot-Reload Config]
    end

    subgraph "Management"
        DAEMON_MGR[daemon_manager.py<br/>Process Lifecycle]
        ASSIGN_MGR[assignment_manager.py<br/>Ship Registry]
        CTRL[operation_controller.py<br/>Checkpointing]
    end

    subgraph "Utilities"
        UTILS[utils.py<br/>Distance, Fuel, Time]
        PATHS[paths.py<br/>File System]
    end

    SHIP --> API
    NAV --> GRAPH
    NAV --> ORTOOLS
    NAV --> SHIP
    GRAPH --> API
    ORTOOLS --> CONFIG
    VALIDATOR --> NAV
    MARKET --> API
    MARKET --> DB
    DAEMON_MGR --> DB
    ASSIGN_MGR --> DB
    CTRL --> DB

    style API fill:#ffebee
    style SHIP fill:#e3f2fd
    style NAV fill:#e3f2fd
    style GRAPH fill:#f3e5f5
    style ORTOOLS fill:#f3e5f5
```

### Key Responsibilities

| Component | Purpose | Dependencies |
|-----------|---------|--------------|
| **api_client.py** | Rate-limited SpaceTraders API client | None (external API) |
| **ship_controller.py** | Ship state machine (DOCKED/ORBIT/TRANSIT) | api_client |
| **smart_navigator.py** | Fuel-aware pathfinding with auto-refuel | routing, ortools_router, ship_controller |
| **routing.py** | Graph building, route planning, tour optimization | api_client, database |
| **ortools_router.py** | Google OR-Tools VRP/TSP solver | routing_config |
| **database.py** | SQLite persistence layer | None (SQLite) |
| **daemon_manager.py** | Background process management | database |
| **assignment_manager.py** | Ship allocation registry | database, daemon_manager |

---

## Operations Layer

### Mining Operation Flow

```mermaid
sequenceDiagram
    participant CLI
    participant Mining as mining.py
    participant Executor as _mining/executor.py
    participant Cycle as _mining/mining_cycle.py
    participant Ship as ship_controller.py
    participant Nav as smart_navigator.py
    participant DB as database.py

    CLI->>Mining: mine_operation(args)
    Mining->>Executor: MiningOperationExecutor(args)
    Executor->>Ship: get_status()
    Executor->>Nav: validate_route(asteroid, market)
    Executor->>DB: Check checkpoint

    loop For each cycle
        Executor->>Cycle: MiningCycle.execute(n)
        Cycle->>Nav: navigate_to(asteroid)
        Cycle->>Ship: orbit()
        loop Until cargo full
            Cycle->>Ship: extract()
            Cycle->>Ship: wait_for_cooldown()
        end
        Cycle->>Nav: navigate_to(market)
        Cycle->>Ship: dock()
        Cycle->>Ship: sell_all()
        Cycle->>Ship: refuel()
        Cycle->>DB: Save checkpoint
    end

    Executor->>CLI: Return success/failure
```

### Scouting Operation Flow

```mermaid
sequenceDiagram
    participant CLI
    participant Routing as routing.py
    participant Executor as scouting/executor.py
    participant Tour as scouting/tour_mode.py
    participant Ship as ship_controller.py
    participant DB as database.py

    CLI->>Routing: scout_markets_operation(args)
    Routing->>Executor: ScoutMarketsExecutor(args)
    Executor->>Tour: TourMode.execute()

    Tour->>DB: Load system graph
    Tour->>Tour: Optimize tour (OR-Tools TSP)

    loop For each waypoint in tour
        Tour->>Ship: navigate_to(waypoint)
        Tour->>Ship: dock()
        Tour->>Ship: scan_market()
        Tour->>DB: Save market data
    end

    Tour-->>Executor: Return completion
    Executor-->>CLI: Return success
```

### Assignment Management Flow

```mermaid
sequenceDiagram
    participant Daemon as daemon.py
    participant Assign as assignments.py
    participant Manager as assignment_manager.py
    participant DB as database.py

    Daemon->>Assign: assignment_assign_operation()
    Assign->>Manager: assign(ship, operator, daemon_id)
    Manager->>DB: Check if ship available
    alt Ship available
        Manager->>DB: Create assignment record
        Manager-->>Assign: Success
    else Ship already assigned
        Manager-->>Assign: Error: already assigned
    end

    Note over Manager,DB: Daemon starts operation

    Daemon->>Assign: assignment_sync_operation()
    Assign->>Manager: sync_with_daemons()
    Manager->>DB: Load all assignments
    loop For each assignment
        Manager->>Manager: Check daemon status
        alt Daemon stopped
            Manager->>DB: Release ship
        else Daemon running
            Manager->>DB: Mark as active
        end
    end
    Manager-->>Assign: Return sync summary
```

---

## Data Flow

### Ship Navigation with Fuel Management

```mermaid
graph LR
    A[Operation Request] --> B[smart_navigator.py]
    B --> C{Validate Route}
    C -->|Valid| D[Calculate Fuel]
    C -->|Invalid| E[Return Error]

    D --> F{Fuel Sufficient?}
    F -->|Yes| G[Execute Route]
    F -->|No| H[Insert Refuel Stops]

    H --> G
    G --> I[ship_controller.py]
    I --> J{Ship State?}

    J -->|DOCKED| K[Orbit First]
    J -->|IN_ORBIT| L[Navigate]
    J -->|IN_TRANSIT| M[Wait for Arrival]

    K --> L
    L --> N[Update Ship State]
    M --> N

    style B fill:#e3f2fd
    style I fill:#e3f2fd
    style G fill:#c8e6c9
    style E fill:#ffcdd2
```

### Market Data Collection

```mermaid
graph TB
    A[Scout Operation] --> B[Scan Market API]
    B --> C[market_data.py]
    C --> D[Parse Market Data]
    D --> E[Calculate Spreads]
    E --> F[database.py]
    F --> G[(SQLite: market_data)]

    H[Trading Operation] --> I[Query Best Trades]
    I --> F
    F --> J[Return Trade Opportunities]

    style C fill:#fff3e0
    style F fill:#e8f5e9
    style G fill:#f3e5f5
```

---

## Module Dependency Graph

### Critical Path Dependencies

```mermaid
graph TB
    subgraph "High-Level Operations"
        MINING[mining.py]
        TRADING[multileg_trader.py]
        SCOUTING[scouting/executor.py]
    end

    subgraph "Ship Control Stack"
        SHIP[ship_controller.py]
        NAV[smart_navigator.py]
        ROUTE[routing.py]
        ORTOOLS[ortools_router.py]
    end

    subgraph "Foundation"
        API[api_client.py]
        DB[database.py]
        UTILS[utils.py]
    end

    MINING --> SHIP
    MINING --> NAV
    MINING --> DB

    TRADING --> SHIP
    TRADING --> NAV
    TRADING --> DB

    SCOUTING --> SHIP
    SCOUTING --> NAV
    SCOUTING --> ROUTE

    SHIP --> API
    NAV --> ROUTE
    NAV --> ORTOOLS
    NAV --> SHIP
    ROUTE --> API
    ROUTE --> DB
    ORTOOLS --> UTILS

    classDef critical fill:#ffebee
    classDef core fill:#e3f2fd
    classDef foundation fill:#e8f5e9

    class SHIP,NAV,ROUTE critical
    class API,DB foundation
```

### Shared Dependencies

**Most Depended-On Modules** (ordered by importance):

1. **api_client.py** - Used by: ship_controller, routing, market_data, all operations
2. **database.py** - Used by: routing, market_data, daemon_manager, assignment_manager, operation_controller
3. **ship_controller.py** - Used by: All operations (mining, trading, scouting, contracts)
4. **smart_navigator.py** - Used by: All navigation-heavy operations
5. **utils.py** - Used by: Most modules for distance, fuel, time calculations

---

## Test Coverage Strategy

### Coverage Tiers

```mermaid
graph TB
    subgraph "Tier 1: Critical Path (Target: 85%+)"
        T1A[api_client.py]
        T1B[ship_controller.py]
        T1C[smart_navigator.py]
        T1D[database.py]
    end

    subgraph "Tier 2: Core Operations (Target: 80%+)"
        T2A[mining modules]
        T2B[routing.py]
        T2C[assignment_manager.py]
        T2D[daemon_manager.py]
    end

    subgraph "Tier 3: Business Logic (Target: 70%+)"
        T3A[scouting modules]
        T3B[fleet.py]
        T3C[assignments.py]
    end

    subgraph "Tier 4: Complex/Lower Priority (Target: 50%+)"
        T4A[multileg_trader.py]
        T4B[contracts.py]
        T4C[ortools_router.py]
    end

    style T1A fill:#c8e6c9
    style T1B fill:#c8e6c9
    style T1C fill:#c8e6c9
    style T1D fill:#c8e6c9

    style T2A fill:#fff9c4
    style T2B fill:#fff9c4
    style T2C fill:#fff9c4
    style T2D fill:#fff9c4
```

### Current Coverage Status

| Module | Current | Target | Status | Priority |
|--------|---------|--------|--------|----------|
| **routing.py** | 85.8% | 85% | ✅ DONE | Critical |
| **mining_cycle.py** | 92.2% | 85% | ✅ DONE | Critical |
| **assignments.py** | 79.0% | 85% | ⚠️ Close | High |
| **tour_mode.py** | 64.8% | 85% | 🔨 Next | High |
| **scouting/executor.py** | 63.4% | 85% | 🔨 Next | High |
| **smart_navigator.py** | 61.8% | 85% | 🔨 Soon | Critical |
| **ship_controller.py** | 47.0% | 85% | 📋 Planned | Critical |
| **api_client.py** | 46.8% | 85% | 📋 Planned | Critical |
| **database.py** | 44.3% | 85% | 📋 Planned | Critical |

### Testing Approach by Module Type

**Pure Logic (Easy to Test):**
- routing.py, utils.py, tour_mode.py
- **Strategy:** BDD scenarios with mock API/DB

**State Machines (Medium Complexity):**
- ship_controller.py, smart_navigator.py
- **Strategy:** State transition tests, mock API

**Integration Heavy (Complex):**
- api_client.py, database.py, daemon_manager.py
- **Strategy:** Unit tests with mocks, integration tests with test DB

**OR-Tools Optimization (Very Complex):**
- ortools_router.py, ortools_mining_optimizer.py
- **Strategy:** Known-good scenarios, regression tests

---

## Module Size Analysis

**Lines of Code** (for test planning):

| Module | LOC | Complexity | Test Effort |
|--------|-----|------------|-------------|
| multileg_trader.py | 1879 | Very High | 🔥🔥🔥🔥 |
| ortools_router.py | 766 | Very High | 🔥🔥🔥🔥 |
| contracts.py | 541 | High | 🔥🔥🔥 |
| smart_navigator.py | 366 | Medium | 🔥🔥 |
| ship_controller.py | 347 | Medium | 🔥🔥 |
| captain_logging.py | 308 | Medium | 🔥🔥 |
| database.py | 278 | Medium | 🔥🔥 |
| daemon_manager.py | 213 | Medium | 🔥🔥 |
| ortools_mining_optimizer.py | 209 | High | 🔥🔥🔥 |
| market_partitioning.py | 196 | Medium | 🔥🔥 |
| assignments.py | 191 | Low | 🔥 |
| purchasing.py | 187 | Low | 🔥 |
| routing.py | 171 | Low | ✅ DONE |

---

## Architectural Patterns

### 1. **State Machine Pattern**
- **Used in:** ship_controller.py
- **States:** DOCKED, IN_ORBIT, IN_TRANSIT
- **Transitions:** Automatic state handling with wait logic

### 2. **Strategy Pattern**
- **Used in:** smart_navigator.py (flight mode selection)
- **Strategies:** CRUISE (fast), DRIFT (fuel-efficient)

### 3. **Builder Pattern**
- **Used in:** routing.py (GraphBuilder)
- **Purpose:** Construct system navigation graphs

### 4. **Repository Pattern**
- **Used in:** database.py, assignment_manager.py
- **Purpose:** Abstract data persistence

### 5. **Command Pattern**
- **Used in:** operations/*.py
- **Purpose:** Encapsulate operations as function calls

### 6. **Circuit Breaker Pattern**
- **Used in:** operations/control.py
- **Purpose:** Prevent infinite loops in mining

---

## Key Insights for Testing

### High-Impact Modules (Test These First)
1. **ship_controller.py** - State machine used by ALL operations
2. **smart_navigator.py** - Navigation used by ALL operations
3. **api_client.py** - API layer used by EVERYTHING
4. **database.py** - Persistence used by many modules

### Quick Wins (Close to 85%, Easy Boost)
1. **tour_mode.py** (64.8%) - Tour optimization logic
2. **scouting/executor.py** (63.4%) - Scout workflow
3. **utils.py** (69.1%) - Pure functions

### Dead Code Candidates (Check for Unreachable Code)
1. **routing.py** ✅ Already found 78 lines
2. **multileg_trader.py** (1879 lines, 22.5% coverage - likely has dead code)
3. **contracts.py** (541 lines, 12.4% coverage)

### Complex Modules (Need Specialized Strategy)
1. **ortools_router.py** - OR-Tools VRP/TSP (use known-good scenarios)
2. **multileg_trader.py** - Complex trading logic (refactor + test)
3. **scout_coordinator.py** - Multi-ship coordination (integration tests)

---

**Generated with Claude Code**
**Last Updated:** 2025-10-20

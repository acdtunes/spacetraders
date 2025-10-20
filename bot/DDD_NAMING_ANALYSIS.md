# Domain-Driven Design (DDD) Naming Analysis

**Created:** 2025-10-20
**Purpose:** Map current generic naming to proper DDD patterns
**Impact:** This would be a more significant refactoring than the current plan

---

## Current State: Generic "Manager" Anti-Pattern

We're using generic suffixes that don't convey domain meaning:
- `*_manager.py` - What does it manage? How?
- `*_coordinator.py` - What does it coordinate?
- `*_controller.py` - What does it control?

**Problem:** Generic names hide domain concepts and architectural patterns.

---

## DDD Core Patterns (Refresher)

| Pattern | Purpose | Example |
|---------|---------|---------|
| **Repository** | Persistence abstraction for aggregates | `OrderRepository`, `ProductRepository` |
| **Domain Service** | Business logic that doesn't fit in entities | `PricingService`, `ShippingService` |
| **Application Service** | Use case orchestration, transaction boundaries | `CreateOrderService`, `PlaceOrderHandler` |
| **Entity** | Object with identity that persists over time | `Order`, `Customer`, `Product` |
| **Value Object** | Immutable object defined by attributes | `Money`, `Address`, `Coordinates` |
| **Aggregate Root** | Entry point to a cluster of related objects | `Order` (contains OrderLines) |
| **Factory** | Complex object creation | `OrderFactory`, `ProductFactory` |

---

## Current Modules → DDD Pattern Mapping

### Core Layer Analysis

| Current Name | What It Actually Is | DDD Pattern | Proposed DDD Name |
|--------------|---------------------|-------------|-------------------|
| `assignment_manager.py` | Persists ship assignments | **Repository** | `ship_assignment_repository.py` |
| `daemon_manager.py` | Persists daemon processes | **Repository** | `daemon_repository.py` |
| `market_data.py` | Queries market data from DB | **Repository** | `market_data_repository.py` |
| `database.py` | SQLite abstraction | **Infrastructure** | `database.py` (keep) |
| | | | |
| `scout_coordinator.py` | Orchestrates multi-ship scouting | **Domain Service** | `market_scouting_service.py` |
| `smart_navigator.py` | Navigation with fuel awareness | **Domain Service** | `navigation_service.py` |
| `ortools_router.py` | Route optimization with OR-Tools | **Domain Service** | `route_optimization_service.py` |
| `ortools_mining_optimizer.py` | Mining fleet optimization | **Domain Service** | `mining_optimization_service.py` |
| `routing.py` | Graph building and route planning | **Domain Service** | `route_planning_service.py` |
| `routing_validator.py` | Validates routing predictions | **Domain Service** | `route_validation_service.py` |
| `operation_controller.py` | Operation checkpointing | **Domain Service** | `operation_checkpoint_service.py` |
| `system_graph_provider.py` | Provides system navigation graphs | **Domain Service** | `system_graph_service.py` |
| `market_partitioning.py` | Partitions markets across fleet | **Domain Service** | `market_partitioning_service.py` |
| | | | |
| `ship_controller.py` | Ship state machine (DOCKED/ORBIT/TRANSIT) | **Entity/Aggregate** | `ship.py` or `ship_aggregate.py` |
| `api_client.py` | SpaceTraders API wrapper | **Infrastructure** | `spacetraders_api_client.py` |
| `utils.py` | Utility functions | **Infrastructure** | `utils.py` (keep) |

### Operations Layer Analysis

| Current Name | What It Actually Is | DDD Pattern | Proposed DDD Name |
|--------------|---------------------|-------------|-------------------|
| `mining.py` | Mining use case orchestration | **Application Service** | `mining_operations.py` or keep as `mining.py` |
| `contracts.py` | Contract use case orchestration | **Application Service** | `contract_operations.py` or keep |
| `fleet.py` | Fleet status use case | **Application Service** | `fleet_operations.py` or keep |
| `scouting/*.py` | Scouting use cases | **Application Service** | Keep in `scouting/` domain |
| `assignments.py` | Wraps assignment repository | **Application Service** | `assignment_operations.py` or keep |
| `daemon.py` | Wraps daemon repository | **Application Service** | `daemon_operations.py` or keep |
| `scout_coordination.py` | Wraps scout coordinator service | **Application Service** | `scout_operations.py` |

---

## DDD-Aligned Architecture

```
┌─────────────────────────────────────────────────────────┐
│         CLI Layer (Interface/Presentation)              │
│  cli/main.py - Command parsing and routing              │
└───────────────────────┬─────────────────────────────────┘
                        │
┌───────────────────────▼─────────────────────────────────┐
│    Operations Layer (Application Services)              │
│  - Use case orchestration                               │
│  - Transaction boundaries                               │
│  - Delegates to domain services                         │
│                                                          │
│  operations/mining.py                                   │
│  operations/scouting/tour_mode.py                       │
│  operations/contracts.py                                │
└───────────────────────┬─────────────────────────────────┘
                        │
┌───────────────────────▼─────────────────────────────────┐
│        Domain Layer (Domain Services + Entities)        │
│                                                          │
│  Domain Services (Business Logic):                      │
│    core/navigation_service.py                           │
│    core/route_optimization_service.py                   │
│    core/market_scouting_service.py                      │
│    core/mining_optimization_service.py                  │
│                                                          │
│  Entities/Aggregates (Domain Objects):                  │
│    core/ship.py (was ship_controller.py)                │
│                                                          │
│  Repositories (Persistence Abstraction):                │
│    core/ship_assignment_repository.py                   │
│    core/daemon_repository.py                            │
│    core/market_data_repository.py                       │
└───────────────────────┬─────────────────────────────────┘
                        │
┌───────────────────────▼─────────────────────────────────┐
│     Infrastructure Layer (External Concerns)            │
│  - Database access                                      │
│  - API clients                                          │
│  - File system                                          │
│                                                          │
│  core/database.py                                       │
│  core/spacetraders_api_client.py                        │
│  helpers/paths.py                                       │
└─────────────────────────────────────────────────────────┘
```

---

## Proposed DDD Naming Refactor

### Option 1: Full DDD (Comprehensive)

**Repositories (3 renames):**
```bash
core/assignment_manager.py    → core/ship_assignment_repository.py
core/daemon_manager.py         → core/daemon_repository.py
core/market_data.py            → core/market_data_repository.py
```

**Domain Services (10 renames):**
```bash
core/scout_coordinator.py              → core/market_scouting_service.py
core/smart_navigator.py                → core/navigation_service.py
core/ortools_router.py                 → core/route_optimization_service.py
core/ortools_mining_optimizer.py       → core/mining_optimization_service.py
core/routing.py                        → core/route_planning_service.py
core/routing_validator.py              → core/route_validation_service.py
core/operation_controller.py           → core/operation_checkpoint_service.py
core/system_graph_provider.py          → core/system_graph_service.py
core/market_partitioning.py            → core/market_partitioning_service.py
core/routing_config.py                 → core/route_planning_config.py
```

**Entities (1 rename):**
```bash
core/ship_controller.py → core/ship.py (or ship_aggregate.py)
```

**Infrastructure (1 rename):**
```bash
core/api_client.py → core/spacetraders_api_client.py
```

**Operations (1 rename + 1 delete):**
```bash
operations/scout_coordination.py → operations/scout_operations.py
operations/analysis.py → DELETE (redistribute as planned)
```

**Total: 17 renames + 1 deletion**

---

### Option 2: Hybrid DDD (Pragmatic)

**Only rename the most confusing ones:**

**Critical Repositories:**
```bash
core/assignment_manager.py → core/ship_assignment_repository.py
core/daemon_manager.py     → core/daemon_repository.py
```

**Critical Domain Services:**
```bash
core/scout_coordinator.py → core/market_scouting_service.py
core/routing.py           → core/route_planning_service.py
```

**Operations:**
```bash
operations/scout_coordination.py → operations/scout_operations.py
operations/analysis.py → DELETE (redistribute)
```

**Total: 6 renames + 1 deletion**

**Keep rest as-is:** `smart_navigator.py`, `ship_controller.py`, etc. are "good enough"

---

### Option 3: Service Suffix Only (Minimal DDD)

**Just add `_service` to domain services, keep repositories as `_manager`:**

```bash
core/scout_coordinator.py → core/scout_service.py
core/routing.py           → core/routing_service.py
operations/scout_coordination.py → operations/scout_ops.py
operations/analysis.py → DELETE
```

**Rationale:**
- "Manager" for repositories is common in many codebases
- Adding `_service` makes domain services explicit
- Minimal change, big clarity gain

**Total: 3 renames + 1 deletion**

---

## Naming Convention Standard (DDD-Aligned)

### Core Layer

**Repositories (Persistence):**
```
Pattern: {entity}_repository.py
Example: ship_assignment_repository.py, daemon_repository.py
Old Pattern: *_manager.py (acceptable but less clear)
```

**Domain Services (Business Logic):**
```
Pattern: {capability}_service.py
Example: navigation_service.py, route_optimization_service.py
Alternative: {domain}_service.py (e.g., market_scouting_service.py)
```

**Entities/Aggregates:**
```
Pattern: {entity}.py or {entity}_aggregate.py
Example: ship.py, route.py
Current: *_controller.py works for state machines
```

**Infrastructure:**
```
Pattern: {technology}_{purpose}.py
Example: spacetraders_api_client.py, sqlite_database.py
Current: api_client.py, database.py (acceptable)
```

### Operations Layer (Application Services)

**Use Case Handlers:**
```
Pattern: {domain}_operations.py or just {domain}.py
Example: mining_operations.py or mining.py
Current: Good as-is
```

---

## Benefits of DDD Naming

### ✅ Advantages

1. **Clear Intent** - `NavigationService` vs `SmartNavigator` - immediately obvious it's a service
2. **Pattern Recognition** - `*_repository.py` = persistence, `*_service.py` = business logic
3. **Industry Standard** - DDD is widely known, easier onboarding
4. **Architectural Clarity** - Layers are explicit
5. **Testability** - Clear boundaries for mocking (repository vs service)
6. **Consistency** - One naming pattern instead of manager/coordinator/controller/provider

### ❌ Disadvantages

1. **More Verbose** - `ship_assignment_repository.py` vs `assignment_manager.py`
2. **Larger Refactoring** - More files to rename, more imports to update
3. **Breaking Changes** - Anyone importing these modules breaks
4. **Not Critical** - Current naming works, this is polish
5. **Time Cost** - 3-4 hours for full DDD vs 2-3 hours for current plan

---

## Recommendation

### Phase 1: Current Plan (Fix Critical Issues)
Execute REFACTOR_PLAN.md as written:
- Fix routing.py collision
- Fix scout_coordination naming
- Delete analysis.py

### Phase 2: DDD Naming (Follow-up)
**Option 2 (Hybrid DDD)** - pragmatic approach:
- Rename `*_manager` → `*_repository` (assignment, daemon)
- Rename `scout_coordinator` → `market_scouting_service`
- Rename `routing.py` → `route_planning_service.py` (already planned)
- Add `_service` to other obvious domain services

### Benefits of Phased Approach
1. ✅ Fix critical issues first (name collisions)
2. ✅ Smaller, safer commits
3. ✅ Can test between phases
4. ✅ Can stop if too disruptive
5. ✅ Easier code review

---

## Comparison: Current Plan vs DDD

| Aspect | Current Plan | Full DDD | Hybrid DDD |
|--------|--------------|----------|------------|
| **Renames** | 3 | 17 | 6 |
| **Time** | 2-3 hours | 4-5 hours | 3-4 hours |
| **Risk** | LOW-MEDIUM | MEDIUM-HIGH | MEDIUM |
| **Clarity Gain** | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ |
| **Breaking Changes** | Moderate | Extensive | Moderate |
| **Industry Alignment** | Generic | DDD Standard | DDD-ish |

---

## Questions to Answer

1. **Do we care about DDD alignment?**
   - If yes → Go for Hybrid DDD or Full DDD
   - If no → Stick with current plan

2. **Is verbose naming worth it?**
   - `ship_assignment_repository.py` vs `assignment_manager.py`
   - More explicit but longer

3. **Time available?**
   - 2-3 hours → Current plan
   - 3-4 hours → Hybrid DDD
   - 4-5 hours → Full DDD

4. **Breaking change tolerance?**
   - Low → Current plan (3 renames)
   - Medium → Hybrid DDD (6 renames)
   - High → Full DDD (17 renames)

---

## My Recommendation

**Hybrid DDD (Option 2) - Best Balance**

**Phase 1: Critical Fixes (Current Plan)**
```bash
core/routing.py → core/route_planning_service.py  # Fix collision + DDD
operations/scout_coordination.py → operations/scout_operations.py
operations/analysis.py → DELETE
```

**Phase 2: DDD Repository Pattern**
```bash
core/assignment_manager.py → core/ship_assignment_repository.py
core/daemon_manager.py → core/daemon_repository.py
```

**Phase 3: Key Domain Services**
```bash
core/scout_coordinator.py → core/market_scouting_service.py
core/smart_navigator.py → core/navigation_service.py  # Optional
```

**Total: 7 renames + 1 deletion across 3 phases**

This gives us:
- ✅ Fix critical issues (collisions, confusion)
- ✅ Align with DDD patterns (repositories, services)
- ✅ Manageable scope (can stop after any phase)
- ✅ Industry-standard naming
- ⚠️ Moderate breaking changes (acceptable)

---

**What do you think? Should we go DDD?**

**Generated with Claude Code**

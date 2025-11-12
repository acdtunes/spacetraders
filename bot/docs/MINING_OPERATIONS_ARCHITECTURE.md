# Mining Operations Architecture

**Version:** 1.0
**Date:** 2025-11-11
**Status:** Planning

## Table of Contents
1. [Executive Summary](#executive-summary)
2. [System Overview](#system-overview)
3. [Component Architecture](#component-architecture)
4. [Data Architecture](#data-architecture)
5. [Integration Points](#integration-points)
6. [Design Decisions](#design-decisions)

---

## Executive Summary

This document defines the architecture for an autonomous mining operation system that coordinates five ship types to create a fully automated mining-to-market pipeline.

**Core Capabilities:**
- Autonomous mining with survey optimization
- Multi-ship cargo aggregation and inventory management
- Intelligent resource allocation for contracts and market sales
- Market-driven selling decisions
- Automatic fleet capacity expansion

**Architectural Approach:**
- Hexagonal Architecture (Ports & Adapters)
- CQRS pattern for command/query separation
- Event-driven coordination via background containers
- VRP optimization for fleet assignment
- FIFO queue for resource fairness

---

## System Overview

### Operational Flow

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│ Mining Ships │────▶│  Transport   │────▶│  Inventory   │
│  (Asteroids) │     │    Ships     │     │    Ships     │
└──────────────┘     └──────────────┘     └──────────────┘
                                                 │
                                                 ├─────▶ Contract Ships ──▶ Deliveries
                                                 │
                                                 └─────▶ Market Ships ────▶ Sales
```

### Ship Roles

| Role | Responsibilities | Deployment Pattern |
|------|-----------------|-------------------|
| **Mining Ships** | Survey asteroids, extract resources, manage cargo value, transfer to transport | VRP-assigned to asteroids, stationary |
| **Transport Ships** | Shuttle cargo between mining zones and inventory | Fixed shuttle loops (1-2 per zone) |
| **Inventory Ships** | Hold aggregated resources, serve withdrawal requests | Static positions at system center |
| **Contract Ships** | Claim resources from inventory, deliver to contracts | On-demand based on contract workflow |
| **Market Ships** | Claim resources from inventory, sell at optimal markets | Triggered by market opportunity evaluation |

### Ship Type Recommendations

**Mining:** `SHIP_MINING_DRONE`, `SHIP_ORE_HOUND` (40-80 cargo, mining mounts)
**Transport:** `SHIP_LIGHT_HAULER`, `SHIP_LIGHT_SHUTTLE` (80+ cargo, high speed)
**Inventory:** `SHIP_HEAVY_FREIGHTER`, `SHIP_BULK_FREIGHTER` (200+ cargo, expandable with modules)
**Contract/Market:** Variable based on transaction sizes

**Cargo Expansion:** All ships can install `MODULE_CARGO_HOLD_I/II/III` at shipyards to increase capacity.

---

## Component Architecture

### Domain Layer

#### Mining Domain
- **Survey Management:** Survey caching, expiration tracking, deposit prioritization
- **Extraction Workflow:** Cooldown management, yield tracking, cargo state management
- **Value Evaluation:** Market-price-based cargo value calculation for jettison decisions

#### Inventory Domain
- **Inventory State:** Aggregated view of resources across inventory ships
- **Capacity Management:** Utilization tracking, space allocation
- **Transfer Coordination:** Ship-to-ship cargo movement orchestration

#### Resource Allocation Domain
- **FIFO Queue:** Fair resource claiming for contracts and market sales
- **Contract Reservations:** Buffer management for active contracts
- **Allocation Engine:** Atomic resource claiming with availability checking

#### Sales Domain
- **Decision Engine:** Multi-criteria evaluation (surplus, price opportunity, capacity threshold)
- **Price History:** Rolling average tracking for opportunity detection
- **Market Selection:** Best-price market identification for each good

#### Fleet Management Domain
- **Capacity Monitor:** Real-time utilization tracking across inventory fleet
- **Auto-Expansion:** Trigger ship purchases when capacity thresholds reached
- **Position Optimizer:** Determine optimal inventory ship placement

### Application Layer

#### Command Handlers
- **Mining Operations:** Extract, Survey, MiningWorkflow, FleetCoordinator
- **Transport Operations:** TransportShuttle
- **Inventory Operations:** Transfer, Deposit, Withdraw
- **Market Operations:** EvaluateOpportunities, ScheduleSale, ExecuteSale
- **Fleet Operations:** AutoPurchaseInventoryShip

#### Query Handlers
- **Inventory Queries:** GetInventoryState, CheckAvailability
- **Mining Queries:** GetActiveSessions, GetExtractionHistory
- **Market Queries:** FindBestMarket, GetPriceHistory

#### Domain Services
- **CargoValueEvaluator:** Market-price-based cargo value assessment
- **ResourceAllocator:** FIFO queue management
- **SaleDecisionEngine:** Multi-criteria sale opportunity evaluation
- **CapacityMonitor:** Fleet expansion triggering

### Infrastructure Layer

#### API Client Extensions
- **Mining Operations:** `survey_waypoint()`, `extract_resources()`, `get_cooldown()`
- **Cargo Transfer:** `transfer_cargo()` (ship-to-ship, both docked at same location)
- **Existing Operations:** Navigate, dock, refuel, jettison, sell (reused)

#### Persistence
- **Mining Repositories:** Session tracking, survey caching, extraction history
- **Inventory Repositories:** Ship assignments, item tracking, transfer audit
- **Allocation Repositories:** Request queue, reservations, history
- **Sales Repositories:** Sale history, price tracking

#### Background Containers
- **Mining Workers:** Long-running mining workflow per ship
- **Transport Workers:** Continuous shuttle loops per transport
- **Market Evaluator:** Periodic opportunity scanning
- **Capacity Monitor:** Periodic utilization checking

---

## Data Architecture

### Core Data Entities

**Mining Domain:**
- Mining sessions (ship, asteroid, status, metrics)
- Surveys (signature, deposits, expiration)
- Extractions (yield, cooldown, timestamp)

**Inventory Domain:**
- Inventory ships (ship, location, capacity)
- Inventory items (good, units, last_updated)
- Cargo transfers (from, to, good, units, type)

**Transport Domain:**
- Transport assignments (ship, zone, inventory_ship, status)

**Allocation Domain:**
- Resource requests (requester, good, units, status, created_at)
- Contract reservations (contract, good, units, buffer, expires)

**Sales Domain:**
- Sale history (ship, good, units, market, price, reason)
- Price history (good, market, price, timestamp)

### Data Flow Patterns

**Mining → Inventory:**
1. Miner extracts → fills cargo
2. Miner navigates to rendezvous
3. Miner transfers to transport (docked)
4. Transport navigates to inventory
5. Transport deposits to inventory (docked)

**Inventory → Contracts:**
1. Contract workflow creates request
2. Request queued (FIFO)
3. Request claimed (atomic)
4. Resources withdrawn from inventory
5. Delivery executed

**Inventory → Markets:**
1. Market evaluator scans inventory
2. Opportunities identified (surplus + price/threshold)
3. Sale scheduled (request queued)
4. Request claimed (atomic)
5. Resources withdrawn and sold

---

## Integration Points

### SpaceTraders API

**Required Endpoints:**
- `POST /my/ships/{ship}/survey` - Survey waypoints
- `POST /my/ships/{ship}/extract` - Extract resources
- `GET /my/ships/{ship}/cooldown` - Check cooldown
- `POST /my/ships/{ship}/transfer` - Ship-to-ship transfer
- Existing: navigate, dock, orbit, refuel, jettison, sell

**API Constraints:**
- Transfer requires both ships docked at same location
- Transfer requires ships belong to same agent
- Extraction has cooldown period
- Surveys expire and can only be used once

### Existing Systems

**Navigation System:**
- Reuse VRP engine for miner-to-asteroid assignment
- Reuse routing for pathfinding with fuel management
- Reuse navigation commands for ship movement

**Contract System:**
- Extend to query inventory instead of markets
- Use existing multi-trip delivery logic
- Integrate with resource allocation queue

**Market System:**
- Reuse market data fetching and caching
- Leverage price tracking for value evaluation
- Integrate with sale decision engine

**Daemon System:**
- Deploy mining/transport as background containers
- Reuse container management and logging
- Leverage restart policies and monitoring

---

## Design Decisions

### 1. Cargo Transfer Mechanism
**Decision:** Use SpaceTraders API direct ship-to-ship transfer

**Rationale:** API provides `/my/ships/{ship}/transfer` endpoint. Both ships must be docked at same waypoint.

**Alternative Rejected:** Jettison + pickup pattern (unreliable, cargo could be lost)

### 2. Cargo Value Determination
**Decision:** Market sell price (purchase_price from TradeGood)

**Rationale:** Reflects actual credits ships receive when selling. Allows real-time market-based decisions.

**Threshold:** Configurable minimum credits/unit (e.g., 50). Below threshold = jettison.

### 3. Inventory Ship Positioning
**Decision:** Static positions at system center waypoints

**Rationale:**
- Minimizes total travel distance for all transport ships
- Predictable rendezvous points
- Simplifies coordination logic
- Prefer waypoints with MARKETPLACE trait for emergency selling

**Alternative Rejected:** Dynamic repositioning (adds complexity, minimal benefit for stationary inventory)

### 4. Resource Allocation Strategy
**Decision:** FIFO queue for all resource requests

**Rationale:**
- Fair allocation between contracts and market sales
- Simple implementation (timestamp-based ordering)
- Prevents starvation of lower-priority operations
- Atomic claiming prevents race conditions

**Contract Priority:** Not prioritized over market sales. Buffer system reserves resources instead.

### 5. Market Selling Criteria
**Decision:** Multi-criteria evaluation (ALL must be true)

**Criteria:**
1. **Resource surplus** - Units > (contract_reserved + buffer)
2. **Price opportunity** OR **Inventory threshold**
   - Price opportunity: Current price ≥ 110% historical average
   - Inventory threshold: Total utilization ≥ 90%

**Rationale:** Balance profitability (good prices) with capacity management (prevent inventory full).

### 6. Inventory Full Handling
**Decision:** Auto-expand fleet by purchasing additional inventory ships

**Rationale:**
- Prevents mining disruption (miners don't pause)
- Scales capacity with operation growth
- Automated growth without manual intervention

**Trigger:** Total inventory utilization ≥ 90%

**Action:** Purchase `SHIP_HEAVY_FREIGHTER`, navigate to optimal position, register as inventory ship.

### 7. Mining Survey Strategy
**Decision:** Survey first, then extract with survey

**Rationale:**
- Surveys identify valuable deposits
- Targeted extraction yields better resources
- Reduces low-value cargo (fewer jettisons)

**Alternative Considered:** Extract blindly (faster but gets random yields)

### 8. Transport Assignment Pattern
**Decision:** Fixed shuttle loops (1-2 transports per mining zone)

**Rationale:**
- Predictable for miners (known rendezvous)
- Simple coordination
- Efficient utilization

**Alternative Rejected:** Dynamic work queue (overengineered for simple shuttle pattern)

### 9. Market Selling Trigger
**Decision:** Multiple triggers (evaluated periodically)

**Triggers:**
1. **Resource surplus detected** (excess inventory)
2. **Price opportunity detected** (favorable market prices)
3. **Inventory threshold reached** (≥90% full, need space)

**Evaluation Frequency:** Every 15 minutes via periodic market evaluator container.

### 10. Architecture Pattern
**Decision:** Hexagonal Architecture with CQRS

**Rationale:**
- Consistent with existing codebase
- Clean separation of concerns
- Testable components
- Extensible for future operations

---

## Implementation Phases

### Phase 1: Core Mining Operations
**Goal:** Single ship can survey and extract resources

**Components:**
- API client mining extensions
- Mining domain models
- Basic extraction commands
- Database schema for sessions/extractions

### Phase 2: Transport & Inventory System
**Goal:** Cargo aggregation pipeline working

**Components:**
- Inventory domain model
- Transfer commands
- Transport shuttle loop
- Database schema for inventory/transfers

### Phase 3: Resource Allocation System
**Goal:** FIFO queue operational

**Components:**
- FIFO queue implementation
- Resource allocator service
- Request/claim mechanism
- Database schema for queue/reservations

### Phase 6: Coordinator & Orchestration
**Goal:** End-to-end mining operation

**Components:**
- Fleet coordinator command
- VRP integration for assignments
- Worker container launching
- CLI interface

### Phase 4: Intelligent Market Sales
**Goal:** Automated selling based on criteria

**Components:**
- Sale decision engine
- Price history tracking
- Market evaluation periodic task
- Integration with inventory

### Phase 5: Fleet Auto-Expansion
**Goal:** Automated capacity management

**Components:**
- Capacity monitor service
- Auto-purchase command
- Position optimizer
- Integration with coordinator

---

## Risks & Mitigations

### Risk: Transfer Coordination Failures
**Mitigation:** Implement retry logic, timeout handling, and state recovery in transfer commands.

### Risk: Inventory Ship Cargo Full
**Mitigation:** Phase 5 auto-expansion triggers before 100% capacity. Emergency selling if expansion fails.

### Risk: Low-Value Mining (Jettison Too Much)
**Mitigation:** Survey-first strategy targets valuable deposits. Configurable jettison threshold.

### Risk: Market Price Volatility
**Mitigation:** Price history rolling average smooths spikes. Conservative 110% threshold for opportunities.

### Risk: API Rate Limiting
**Mitigation:** Existing rate limiter (2 req/sec). Extraction cooldowns naturally throttle mining operations.

### Risk: Fleet Coordination Deadlock
**Mitigation:** Transport shuttle loops are independent. Miners don't wait for transports (can accumulate at rendezvous).

---

## Success Metrics

**Operational Metrics:**
- Mining efficiency: Extractions per hour per ship
- Transport utilization: Cargo delivered per shuttle loop
- Inventory turnover: Resources withdrawn vs deposited
- Market sale profitability: Credits per unit vs threshold
- Fleet capacity: Utilization percentage over time

**Quality Metrics:**
- Cargo value ratio: High-value kept vs low-value jettisoned
- Resource allocation fairness: FIFO queue wait times
- Transfer success rate: Successful transfers vs failures
- Auto-expansion trigger accuracy: Capacity at purchase time

---

## Conclusion

This architecture provides a scalable, autonomous mining operations system that integrates seamlessly with the existing SpaceTraders bot architecture. The design leverages proven patterns (CQRS, VRP, daemon containers, FIFO queues) and makes clear design decisions for cargo transfer, value evaluation, inventory management, resource allocation, and market selling.

The phased implementation approach allows for iterative development and testing, with each phase building on previous capabilities to ultimately deliver a fully autonomous mining-to-market pipeline.

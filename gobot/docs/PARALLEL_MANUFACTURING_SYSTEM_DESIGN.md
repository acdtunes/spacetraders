# Parallel Manufacturing System Design

## Executive Summary

This document describes the design for a **multi-level parallel manufacturing system** that maximizes ship utilization by breaking manufacturing into discrete tasks executed by multiple ships concurrently. The system replaces the current serial single-ship approach with a task-based pipeline architecture.

**Key Benefits:**
- Ships never idle waiting for production
- Parallel acquisition of raw materials
- Parallel factory processing
- Continuous pipeline of multiple products
- 10x+ throughput improvement with 10+ ships

---

## Business Context & Strategy

### The Core Problem: Arbitrage Volatility

Our initial approach was pure **arbitrage trading**: buy goods at low prices, sell at high prices. However, analysis of 109 successful trades (2025-11-25) revealed a critical insight:

**Price volatility is NOT determined by the good itself, but by market conditions (supply and activity levels).**

| Buy Supply Level | Price Drift | Profit/Second | Win Rate |
|------------------|-------------|---------------|----------|
| **HIGH** | **2.9%** | **+79.70** | **79%** |
| MODERATE | 32.3% | -34.54 | Losing |
| LIMITED | 41.6% | -6.81 | Losing |
| ABUNDANT | 69.7% | -28.25 | Losing |

| Sell Activity Level | Price Drift | Profit/Second |
|---------------------|-------------|---------------|
| **WEAK** | **5.1%** | **+8.31** |
| **RESTRICTED** | **14.5%** | **+96.94** |
| STRONG | 20.3% | -17.46 |
| GROWING | 33.6% | -517.18 |

**Key Finding:** The only profitable arbitrage conditions are:
- **Buy at HIGH supply markets** (stable, predictable buy prices)
- **Sell at WEAK/RESTRICTED activity markets** (stable, predictable sell prices)

### The Problem: HIGH Supply is Rare

When we applied strict filters (buy supply = HIGH only), we found **zero arbitrage opportunities**. HIGH supply markets are rare because:
- Markets naturally fluctuate between supply levels
- Other traders deplete HIGH supply markets quickly
- No control over market conditions

### The Solution: CREATE HIGH Supply Through Manufacturing

**Insight:** We can't find HIGH supply markets, but we can CREATE them!

When you deliver raw materials to a factory:
1. Factory processes inputs → produces output goods
2. Supply of output goods INCREASES (MODERATE → HIGH → ABUNDANT)
3. Price of output goods DECREASES (more supply = lower prices)
4. We buy at the LOW factory price (HIGH supply, stable)
5. We sell at WEAK activity markets (HIGH demand, stable prices)

This is **vertical integration**: control the supply chain instead of relying on market conditions.

### The Manufacturing Arbitrage Strategy

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    MANUFACTURING ARBITRAGE STRATEGY                          │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  STEP 1: Identify Demand                                                     │
│  ─────────────────────                                                       │
│  Find markets that IMPORT high-value goods at high prices                   │
│  → These are WEAK activity markets (stable prices, high demand)             │
│  → Example: X1-YZ19-A1 imports LASER_RIFLES at 31,699 cr                    │
│                                                                              │
│  STEP 2: Trace Supply Chain                                                  │
│  ─────────────────────────                                                   │
│  LASER_RIFLES requires:                                                      │
│    ├── DIAMONDS (raw material)                                               │
│    ├── PLATINUM (raw material)                                               │
│    └── ADVANCED_CIRCUITRY (intermediate)                                     │
│          ├── ELECTRONICS (raw material)                                      │
│          └── MICROPROCESSORS (raw material)                                  │
│                                                                              │
│  STEP 3: Buy Raw Materials CHEAP                                             │
│  ────────────────────────────                                                │
│  Purchase at EXPORT markets (factories/producers sell cheap)                 │
│  → DIAMONDS: ~500 cr/unit                                                    │
│  → PLATINUM: ~800 cr/unit                                                    │
│  → ELECTRONICS: ~300 cr/unit                                                 │
│  → MICROPROCESSORS: ~400 cr/unit                                             │
│                                                                              │
│  STEP 4: Deliver to Factories                                                │
│  ────────────────────────────                                                │
│  Deliver raw materials → Factory produces output                             │
│  → Supply INCREASES (MODERATE → HIGH)                                        │
│  → Price DECREASES (we created the supply!)                                  │
│                                                                              │
│  STEP 5: Buy at HIGH Supply (Stable Prices)                                  │
│  ──────────────────────────────────────────                                  │
│  Wait for supply to reach HIGH, then purchase:                               │
│  → ADVANCED_CIRCUITRY: ~1,200 cr/unit (factory price)                        │
│  → LASER_RIFLES: ~15,000 cr/unit (factory price)                             │
│                                                                              │
│  STEP 6: Sell at WEAK Activity Market (Stable High Prices)                   │
│  ──────────────────────────────────────────────────────────                  │
│  Sell at the demand market we identified in Step 1:                          │
│  → LASER_RIFLES: 31,699 cr/unit at X1-YZ19-A1                               │
│                                                                              │
│  PROFIT CALCULATION:                                                         │
│  ───────────────────                                                         │
│  Sell Price:     31,699 cr/unit                                              │
│  Factory Cost:  -15,000 cr/unit (approx)                                     │
│  Input Costs:   -2,000 cr/unit (raw materials)                               │
│  ─────────────────────────────                                               │
│  Net Profit:    ~14,699 cr/unit                                              │
│  With 40 cargo: ~587,960 cr per manufacturing run                            │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Why Wait for HIGH Supply?

Based on our data analysis:

| Supply Level | Price Drift | Risk |
|--------------|-------------|------|
| SCARCE | Very high | Prices swing wildly, unpredictable |
| LIMITED | 41.6% | High risk of price moving against us |
| MODERATE | 32.3% | Still too volatile |
| **HIGH** | **2.9%** | **Stable, predictable, low risk** |
| ABUNDANT | 69.7% | Can crash quickly, unstable |

**HIGH is the sweet spot**: stable prices, good liquidity, predictable margins.

We wait for supply to reach HIGH after delivering inputs because:
1. Factory has processed our inputs (production complete)
2. Prices are now stable (2.9% drift vs 30%+ for other levels)
3. Trade volume is sufficient (good liquidity)
4. Margins are predictable (we can calculate profit accurately)

### Why Parallel Execution with Multiple Ships?

Manufacturing involves significant **wait time** at each step:
- Travel time between markets
- Production time at factories (supply takes time to increase)

With a **single ship**, we waste time waiting:
```
Ship: buy → travel → deliver → WAIT → buy → travel → deliver → WAIT → collect → sell
                               ^^^^                             ^^^^
                           Ship is idle!                    Ship is idle!
```

With **multiple ships in parallel**:
```
Ship 1: buy DIAMONDS → deliver to Factory-A → [available for next task]
Ship 2: buy PLATINUM → deliver to Factory-A → [available for next task]
Ship 3: buy ELECTRONICS → deliver to Factory-B → [available for next task]
Ship 4: buy MICROPROCESSORS → deliver to Factory-B → [available for next task]

         ⏳ All factories processing simultaneously (ships doing other work)

Ship 5: collect ADVANCED_CIRCUITRY → deliver to Factory-A → [next task]
Ship 6: collect LASER_RIFLES → sell at demand market → [next task]

Ships NEVER wait - they're always executing the next task!
```

### Success Metrics

The parallel manufacturing system should achieve:

| Metric | Serial (Current) | Parallel (Target) | Improvement |
|--------|------------------|-------------------|-------------|
| Ship Utilization | ~30% | ~90% | 3x |
| Throughput (runs/hour) | 1 | 6-10 | 6-10x |
| Credits/hour | ~50,000 | ~500,000 | 10x |
| Supply Stability | Variable | HIGH (controlled) | Predictable |
| Price Drift Risk | 30%+ | 2.9% | 10x lower |

### Adaptive Manufacturing Strategy (Optimization)

**Key Insight:** We don't ALWAYS need to manufacture from raw materials. If an intermediate good is already available at HIGH supply, we can buy it directly!

#### Decision Algorithm

```
For each good needed in the supply chain:

1. CHECK: Is there a HIGH supply market for this good?
   │
   ├── YES → BUY directly (skip manufacturing this level)
   │         This saves time and ships!
   │
   └── NO → MANUFACTURE by delivering precursor goods
            │
            └── RECURSE: For each precursor good, repeat step 1

2. BASE CASE: Raw materials (cannot be manufactured)
   │
   ├── FUTURE: Mine them ourselves (mining not implemented yet)
   │
   └── NOW: Buy regardless of market conditions (no alternative)
```

#### Example: Adaptive vs Always-Manufacture

```
Need: LASER_RIFLES

SCENARIO A: ADVANCED_CIRCUITRY is at HIGH supply somewhere
─────────────────────────────────────────────────────────
Adaptive Approach:
  Ship 1: Buy DIAMONDS → Deliver to LASER_RIFLES factory
  Ship 2: Buy PLATINUM → Deliver to LASER_RIFLES factory
  Ship 3: Buy ADVANCED_CIRCUITRY (already HIGH!) → Deliver to factory
  Ship 4: Collect LASER_RIFLES → Sell

  Total: 4 tasks, ~15 minutes

Always-Manufacture Approach:
  Ship 1: Buy DIAMONDS → Deliver to LASER_RIFLES factory
  Ship 2: Buy PLATINUM → Deliver to LASER_RIFLES factory
  Ship 3: Buy ELECTRONICS → Deliver to ADV_CIRCUITRY factory
  Ship 4: Buy MICROPROCESSORS → Deliver to ADV_CIRCUITRY factory
  Ship 5: Collect ADVANCED_CIRCUITRY → Deliver to LASER_RIFLES factory
  Ship 6: Collect LASER_RIFLES → Sell

  Total: 6 tasks, ~25 minutes (67% more work!)

SCENARIO B: ADVANCED_CIRCUITRY is NOT at HIGH supply
────────────────────────────────────────────────────
Both approaches: Full manufacturing chain required
```

#### Implementation

```go
// Adaptive supply chain resolution
func (r *SupplyChainResolver) ResolveAdaptive(
    ctx context.Context,
    good string,
    systemSymbol string,
    playerID int,
) (*SupplyChainNode, error) {
    // Check if HIGH supply market exists for this good
    highSupplyMarket := r.findHighSupplyMarket(ctx, good, systemSymbol, playerID)

    if highSupplyMarket != nil {
        // HIGH supply available - just buy it!
        return &SupplyChainNode{
            Good:              good,
            AcquisitionMethod: AcquisitionBuy,
            SourceMarket:      highSupplyMarket.WaypointSymbol,
            Children:          nil, // No children - leaf node
        }, nil
    }

    // Check if this is a raw material (cannot manufacture)
    if r.isRawMaterial(good) {
        // Must buy regardless of supply
        cheapestMarket := r.findCheapestMarket(ctx, good, systemSymbol, playerID)
        return &SupplyChainNode{
            Good:              good,
            AcquisitionMethod: AcquisitionBuy,
            SourceMarket:      cheapestMarket.WaypointSymbol,
            Children:          nil,
        }, nil
    }

    // Must manufacture - resolve children recursively
    inputs := r.getRequiredInputs(good)
    children := make([]*SupplyChainNode, 0, len(inputs))

    for _, input := range inputs {
        childNode, err := r.ResolveAdaptive(ctx, input, systemSymbol, playerID)
        if err != nil {
            return nil, err
        }
        children = append(children, childNode)
    }

    factoryMarket := r.findFactoryMarket(ctx, good, systemSymbol, playerID)
    return &SupplyChainNode{
        Good:              good,
        AcquisitionMethod: AcquisitionFabricate,
        FactoryMarket:     factoryMarket.WaypointSymbol,
        Children:          children,
    }, nil
}

// Raw materials that cannot be manufactured (base case)
var rawMaterials = map[string]bool{
    // Minerals (from asteroid mining)
    "DIAMONDS": true, "PLATINUM": true, "GOLD": true,
    "SILVER": true, "COPPER": true, "ALUMINUM": true,

    // Ores (from asteroid mining)
    "IRON_ORE": true, "COPPER_ORE": true, "ALUMINUM_ORE": true,
    "GOLD_ORE": true, "SILVER_ORE": true, "PLATINUM_ORE": true,

    // Volatiles (from gas giant siphoning)
    "ICE_WATER": true, "LIQUID_HYDROGEN": true, "LIQUID_NITROGEN": true,
    "HYDROCARBON": true, "AMMONIA_ICE": true,

    // Biologicals (from planet extraction)
    "POLYNUCLEOTIDES": true,

    // Agricultural (from farming - could be manufactured in future)
    "FABRICS": true, "FOOD": true,
}

func (r *SupplyChainResolver) isRawMaterial(good string) bool {
    return rawMaterials[good]
}
```

### Trade Size Calculation

Trade size must be carefully calculated to maximize profit while maintaining market stability.

#### Constraints

```
idealTradeSize = min(
    cargoCapacity,          // Ship limit (e.g., 40 units)
    tradeVolume,            // Market limit per transaction
    supplyAwareLimit,       // Don't crash supply level
    balancedInputQuantity,  // For manufacturing: match recipe ratios
    availableCredits / price // Budget constraint
)
```

#### Supply-Aware Limits

Based on our data analysis, we must avoid crashing supply from HIGH to MODERATE:

| Current Supply | Safe Purchase % | Reasoning |
|----------------|-----------------|-----------|
| ABUNDANT | 80% of tradeVolume | Plenty of buffer before dropping |
| **HIGH** | **60% of tradeVolume** | **Sweet spot - maintain stability** |
| MODERATE | 40% of tradeVolume | Careful - could drop to LIMITED |
| LIMITED | 20% of tradeVolume | Very careful - critical supply |
| SCARCE | 10% of tradeVolume | Minimal - supply nearly depleted |

#### Implementation

```go
// TradeSizeCalculator determines optimal trade quantities
type TradeSizeCalculator struct{}

type TradeSizeInput struct {
    Good             string
    CargoCapacity    int    // Ship's available cargo space
    TradeVolume      int    // Market's max units per transaction
    CurrentSupply    string // Market's current supply level
    CurrentPrice     int    // Price per unit
    AvailableCredits int    // Budget constraint
    TargetQuantity   int    // Desired quantity (0 = maximize)
}

func (c *TradeSizeCalculator) Calculate(input TradeSizeInput) int {
    // Start with cargo capacity or target
    size := input.CargoCapacity
    if input.TargetQuantity > 0 && input.TargetQuantity < size {
        size = input.TargetQuantity
    }

    // Cap by trade volume (market limit)
    if input.TradeVolume > 0 && input.TradeVolume < size {
        size = input.TradeVolume
    }

    // Cap by supply-aware limit (don't crash supply!)
    supplyLimit := c.supplyAwareLimit(input.CurrentSupply, input.TradeVolume)
    if supplyLimit < size {
        size = supplyLimit
    }

    // Cap by budget
    if input.CurrentPrice > 0 && input.AvailableCredits > 0 {
        affordableUnits := input.AvailableCredits / input.CurrentPrice
        if affordableUnits < size {
            size = affordableUnits
        }
    }

    return size
}

func (c *TradeSizeCalculator) supplyAwareLimit(supply string, tradeVolume int) int {
    multipliers := map[string]float64{
        "ABUNDANT": 0.80,
        "HIGH":     0.60,  // Our target - maintain this level
        "MODERATE": 0.40,
        "LIMITED":  0.20,
        "SCARCE":   0.10,
    }

    multiplier, ok := multipliers[supply]
    if !ok {
        multiplier = 0.40 // Default to conservative
    }

    return int(float64(tradeVolume) * multiplier)
}
```

#### Example Calculation

```
Scenario: Buying LASER_RIFLES from factory

Inputs:
├── Cargo Capacity:     40 units
├── Trade Volume:       30 units
├── Current Supply:     HIGH
├── Price:              15,000 cr/unit
└── Available Credits:  500,000 cr

Calculation:
├── Start with cargo:   40 units
├── Cap by tradeVolume: min(40, 30) = 30 units
├── Supply-aware limit: 30 * 0.6 = 18 units
├── Cap by supply:      min(30, 18) = 18 units
├── Affordable units:   500,000 / 15,000 = 33 units
└── Cap by budget:      min(18, 33) = 18 units

Result: Buy 18 units of LASER_RIFLES
        (maintains HIGH supply, maximizes within constraints)
```

#### Manufacturing Input Balance

For manufacturing, multiple inputs must be delivered. With a 40-unit cargo ship:

```
LASER_RIFLES requires: DIAMONDS, PLATINUM, ADVANCED_CIRCUITRY

Option A: Single ship, multiple trips
┌─────────────────────────────────────────┐
│ Trip 1: Buy 40 DIAMONDS → Deliver       │
│ Trip 2: Buy 40 PLATINUM → Deliver       │
│ Trip 3: Buy 40 ADV_CIRCUITRY → Deliver  │
│ Trip 4: Collect 40 LASER_RIFLES → Sell  │
└─────────────────────────────────────────┘
Total: 4 trips × 1 ship

Option B: Multiple ships in parallel (our approach)
┌─────────────────────────────────────────┐
│ Ship 1: Buy 40 DIAMONDS → Deliver       │
│ Ship 2: Buy 40 PLATINUM → Deliver       │  (parallel)
│ Ship 3: Buy 40 ADV_CIRCUITRY → Deliver  │
│                                         │
│ Ship 4: Collect 40 LASER_RIFLES → Sell  │
└─────────────────────────────────────────┘
Total: 4 tasks across 4 ships (3x faster)
```

---

## Table of Contents

**Business Context (above)**
- The Core Problem: Arbitrage Volatility
- The Solution: CREATE HIGH Supply Through Manufacturing
- The Manufacturing Arbitrage Strategy
- Why Parallel Execution with Multiple Ships?
- Success Metrics
- **Adaptive Manufacturing Strategy** (NEW)
- **Trade Size Calculation** (NEW)

**Technical Design (below)**
1. [Current State Analysis](#current-state-analysis)
2. [Problem Statement](#problem-statement)
3. [Proposed Architecture](#proposed-architecture)
4. [Domain Model](#domain-model)
5. [Task Types and Lifecycle](#task-types-and-lifecycle)
6. [Factory State Management](#factory-state-management)
7. [Coordinator Design](#coordinator-design)
8. [Worker Design](#worker-design)
9. [Supply Monitoring](#supply-monitoring)
10. [Multi-Product Pipeline](#multi-product-pipeline)
11. [Implementation Plan](#implementation-plan)
12. [Migration Strategy](#migration-strategy)

---

## Current State Analysis

### What We Have (Ready)

| Component | Location | Status | Description |
|-----------|----------|--------|-------------|
| `ManufacturingOpportunity` | `domain/trading/manufacturing_opportunity.go` | ✅ Ready | Identifies high-demand goods at import markets |
| `ManufacturingDemandFinder` | `application/trading/services/manufacturing_demand_finder.go` | ✅ Ready | Scans markets for manufacturing opportunities |
| `SupplyChainResolver` | `application/goods/services/supply_chain_resolver.go` | ✅ Ready | Builds dependency trees for goods |
| `SupplyChainNode` | `domain/goods/supply_chain.go` | ✅ Ready | Represents nodes in supply chain tree |
| `ExportToImportMap` | `domain/goods/factory_config.go` | ✅ Ready | Maps goods to their required inputs |
| `MarketLocator` | `application/goods/services/market_locator.go` | ✅ Ready | Finds export/import markets for goods |
| `ProductionExecutor` | `application/goods/services/production_executor.go` | ⚠️ Partial | Orchestrates production (needs refactor) |
| `ManufacturingCoordinator` | `application/trading/commands/run_manufacturing_coordinator.go` | ⚠️ Partial | Spawns workers (needs task-based refactor) |
| `ManufacturingWorker` | `application/trading/commands/run_manufacturing_worker.go` | ⚠️ Partial | Executes manufacturing (serial, needs refactor) |

### Recent Fixes Applied

1. **IMPORT/EXPORT Market Confusion** (FIXED 2025-11-25)
   - `fabricateGood()` was navigating to IMPORT market (consumer, high price)
   - Fixed to navigate to EXPORT market (factory, low price)
   - Location: `production_executor.go:205-218`

### What's Broken/Incomplete

1. **No Supply Waiting**: Code doesn't wait for supply to increase after delivering inputs
2. **Serial Execution**: One ship does entire supply chain sequentially
3. **Ship Idle Time**: Ships wait at factories during production
4. **No Task Parallelism**: Can't distribute work across multiple ships
5. **No Factory State Tracking**: No knowledge of pending production at factories

---

## Problem Statement

### Current Serial Flow (Wasteful)

```
Ship 1: BUY(DIAMONDS) → DELIVER(Factory-A) → WAIT → BUY(PLATINUM) → DELIVER(Factory-A) → WAIT
        → BUY(ELECTRONICS) → DELIVER(Factory-B) → WAIT → COLLECT(ADV_CIRCUITRY)
        → DELIVER(Factory-A) → WAIT → COLLECT(LASER_RIFLES) → SELL

Total Time: ~60 minutes (ship waiting 70% of time)
Ships Used: 1
```

### Desired Parallel Flow (Efficient)

```
Ship 1: BUY(DIAMONDS) → DELIVER(Factory-A) → [next task]
Ship 2: BUY(PLATINUM) → DELIVER(Factory-A) → [next task]
Ship 3: BUY(ELECTRONICS) → DELIVER(Factory-B) → [next task]
Ship 4: BUY(MICROPROCESSORS) → DELIVER(Factory-B) → [next task]

        ⏳ Factories processing in parallel (no ships needed)

Ship 5: COLLECT(ADV_CIRCUITRY) → DELIVER(Factory-A) → [next task]
Ship 6: COLLECT(LASER_RIFLES) → SELL → [next task]

Total Time: ~15 minutes (ships always moving)
Ships Used: 6 (but each only busy for ~10 minutes)
```

---

## Proposed Architecture

### High-Level Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        PARALLEL MANUFACTURING SYSTEM                         │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌──────────────────────┐     ┌──────────────────────┐                      │
│  │  Demand Finder       │────▶│  Pipeline Planner    │                      │
│  │  (finds opportunities)│     │  (creates task graph)│                      │
│  └──────────────────────┘     └──────────┬───────────┘                      │
│                                          │                                   │
│                                          ▼                                   │
│  ┌──────────────────────────────────────────────────────────────────┐       │
│  │                     TASK QUEUE (Priority-Based)                   │       │
│  │  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐    │       │
│  │  │ACQUIRE_1│ │ACQUIRE_2│ │DELIVER_1│ │COLLECT_1│ │ SELL_1  │    │       │
│  │  │(ready)  │ │(ready)  │ │(blocked)│ │(waiting)│ │(waiting)│    │       │
│  │  └─────────┘ └─────────┘ └─────────┘ └─────────┘ └─────────┘    │       │
│  └──────────────────────────────────────────────────────────────────┘       │
│                                          │                                   │
│                    ┌─────────────────────┼─────────────────────┐            │
│                    ▼                     ▼                     ▼            │
│  ┌──────────────────────┐ ┌──────────────────────┐ ┌──────────────────────┐ │
│  │  Task Worker         │ │  Task Worker         │ │  Task Worker         │ │
│  │  (Ship 1)            │ │  (Ship 2)            │ │  (Ship 3)            │ │
│  │  Executing: ACQUIRE_1│ │  Executing: ACQUIRE_2│ │  Idle - getting task │ │
│  └──────────────────────┘ └──────────────────────┘ └──────────────────────┘ │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────┐       │
│  │                     FACTORY STATE TRACKER                         │       │
│  │  Factory-A: inputs=[DIAMONDS✓, PLATINUM✓, ADV_CIRCUITRY⏳]       │       │
│  │  Factory-B: inputs=[ELECTRONICS✓, MICROPROCESSORS✓] → supply=HIGH│       │
│  └──────────────────────────────────────────────────────────────────┘       │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────┐       │
│  │                     SUPPLY MONITOR (Background)                   │       │
│  │  Polling factories for supply changes...                          │       │
│  │  Factory-B: MODERATE → HIGH (marking COLLECT tasks as ready)     │       │
│  └──────────────────────────────────────────────────────────────────┘       │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Component Responsibilities

| Component | Responsibility |
|-----------|----------------|
| **Demand Finder** | Scan markets for high-demand manufacturable goods |
| **Pipeline Planner** | Convert supply chain tree into task dependency graph |
| **Task Queue** | Store and prioritize pending tasks |
| **Manufacturing Coordinator** | Assign tasks to idle ships, manage pipeline state |
| **Task Worker** | Execute single task, report completion |
| **Factory State Tracker** | Track delivery status and production state per factory |
| **Supply Monitor** | Poll factories, mark COLLECT tasks ready when supply HIGH |

---

## Domain Model

### New Domain Entities

```go
// domain/manufacturing/task.go

package manufacturing

import (
    "time"
    "github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TaskType represents the type of manufacturing task
type TaskType string

const (
    TaskTypeAcquire  TaskType = "ACQUIRE"  // Buy raw material from export market
    TaskTypeDeliver  TaskType = "DELIVER"  // Deliver material to factory
    TaskTypeCollect  TaskType = "COLLECT"  // Buy produced good from factory
    TaskTypeSell     TaskType = "SELL"     // Sell final product at demand market
)

// TaskStatus represents the current status of a task
type TaskStatus string

const (
    TaskStatusPending    TaskStatus = "PENDING"     // Waiting for dependencies
    TaskStatusReady      TaskStatus = "READY"       // All dependencies met, can execute
    TaskStatusAssigned   TaskStatus = "ASSIGNED"    // Assigned to a ship
    TaskStatusExecuting  TaskStatus = "EXECUTING"   // Ship is executing
    TaskStatusCompleted  TaskStatus = "COMPLETED"   // Successfully completed
    TaskStatusFailed     TaskStatus = "FAILED"      // Failed (will retry)
)

// ManufacturingTask represents a single atomic task in the manufacturing pipeline
type ManufacturingTask struct {
    id              string
    taskType        TaskType
    status          TaskStatus

    // What to acquire/deliver/collect/sell
    good            string
    quantity        int           // Desired quantity (0 = fill cargo)

    // Where
    sourceMarket    string        // For ACQUIRE: export market to buy from
    targetMarket    string        // For DELIVER/SELL: destination market
    factorySymbol   string        // For COLLECT: factory to collect from

    // Dependencies
    dependsOn       []string      // Task IDs that must complete first
    pipelineID      string        // Parent pipeline this task belongs to

    // Execution
    assignedShip    string        // Ship symbol executing this task
    priority        int           // Higher = more urgent

    // Timing
    createdAt       time.Time
    readyAt         time.Time     // When task became ready (for COLLECT: when supply HIGH)
    startedAt       time.Time
    completedAt     time.Time

    // Results
    actualQuantity  int           // Actual quantity acquired/delivered
    totalCost       int           // Cost incurred
    totalRevenue    int           // Revenue earned (for SELL)
}

// ManufacturingPipeline represents a complete manufacturing run for one product
type ManufacturingPipeline struct {
    id              string
    productGood     string        // Final product (e.g., LASER_RIFLES)
    sellMarket      string        // Where to sell final product
    purchasePrice   int           // Expected sale price

    tasks           []*ManufacturingTask
    status          PipelineStatus

    // Tracking
    createdAt       time.Time
    completedAt     time.Time
    totalCost       int
    totalRevenue    int
    netProfit       int
}

type PipelineStatus string

const (
    PipelineStatusPlanning   PipelineStatus = "PLANNING"
    PipelineStatusExecuting  PipelineStatus = "EXECUTING"
    PipelineStatusCompleted  PipelineStatus = "COMPLETED"
    PipelineStatusFailed     PipelineStatus = "FAILED"
)
```

### Factory State Entity

```go
// domain/manufacturing/factory_state.go

package manufacturing

import "time"

// FactoryState tracks the state of a factory for a specific good
type FactoryState struct {
    factorySymbol   string
    outputGood      string        // Good this factory produces

    // Input tracking
    requiredInputs  []InputState
    allInputsDelivered bool

    // Production state
    productionStartedAt time.Time
    currentSupply       string    // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
    previousSupply      string    // Supply level before we delivered inputs

    // Ready state
    readyForCollection  bool      // Supply increased to HIGH
    readyAt             time.Time
}

// InputState tracks delivery status of a single input
type InputState struct {
    good            string
    delivered       bool
    deliveredAt     time.Time
    deliveredBy     string        // Ship that delivered
    quantity        int
}

// FactoryStateTracker manages factory states across the system
type FactoryStateTracker struct {
    states map[string]*FactoryState  // key: "factorySymbol:outputGood"
}
```

---

## Task Types and Lifecycle

### Task Type Details

#### 1. ACQUIRE Task
```
Purpose: Buy raw material from an export market
Input:   good, sourceMarket (export market)
Output:  Ship cargo loaded with good
Next:    DELIVER task to factory

Example: ACQUIRE DIAMONDS from X1-YZ19-B7
```

#### 2. DELIVER Task
```
Purpose: Deliver material to a factory
Input:   good, targetMarket (factory)
Output:  Factory receives input, FactoryState updated
Next:    May trigger COLLECT task to become ready

Example: DELIVER DIAMONDS to X1-YZ19-A2 (LASER_RIFLES factory)
```

#### 3. COLLECT Task
```
Purpose: Buy produced good from factory when supply is HIGH
Input:   good, factorySymbol
Output:  Ship cargo loaded with produced good
Blocked: Until supply reaches HIGH at factory
Next:    DELIVER (if intermediate) or SELL (if final product)

Example: COLLECT ADVANCED_CIRCUITRY from X1-YZ19-B5
```

#### 4. SELL Task
```
Purpose: Sell final product at demand market
Input:   good, targetMarket (import/demand market)
Output:  Credits earned, pipeline completed
Next:    None (terminal task)

Example: SELL LASER_RIFLES at X1-YZ19-A1
```

### Task State Machine

```
                    ┌─────────────┐
                    │   PENDING   │ (waiting for dependencies)
                    └──────┬──────┘
                           │ dependencies met OR supply HIGH
                           ▼
                    ┌─────────────┐
                    │    READY    │ (can be assigned to ship)
                    └──────┬──────┘
                           │ coordinator assigns ship
                           ▼
                    ┌─────────────┐
                    │  ASSIGNED   │ (ship claimed task)
                    └──────┬──────┘
                           │ ship starts execution
                           ▼
                    ┌─────────────┐
                    │  EXECUTING  │ (ship navigating/trading)
                    └──────┬──────┘
                           │
              ┌────────────┴────────────┐
              ▼                         ▼
       ┌─────────────┐           ┌─────────────┐
       │  COMPLETED  │           │   FAILED    │
       └─────────────┘           └──────┬──────┘
                                        │ retry logic
                                        ▼
                                 ┌─────────────┐
                                 │   PENDING   │ (retry)
                                 └─────────────┘
```

---

## Factory State Management

### Factory State Lifecycle

```
1. AWAITING_INPUTS
   - Factory identified as producer for a good
   - Waiting for input deliveries

2. INPUTS_PARTIAL
   - Some inputs delivered
   - Still waiting for remaining inputs

3. INPUTS_COMPLETE
   - All required inputs delivered
   - Production starting (supply will increase)

4. PRODUCTION_IN_PROGRESS
   - Monitoring supply level
   - Waiting for supply to reach HIGH

5. READY_FOR_COLLECTION
   - Supply reached HIGH
   - COLLECT tasks can now execute
```

### Supply Level Monitoring

```go
// SupplyLevel ordering for comparison
var supplyLevelOrder = map[string]int{
    "SCARCE":   1,
    "LIMITED":  2,
    "MODERATE": 3,
    "HIGH":     4,
    "ABUNDANT": 5,
}

// IsReadyForCollection checks if factory output is ready
func (f *FactoryState) IsReadyForCollection() bool {
    currentLevel := supplyLevelOrder[f.currentSupply]

    // Ready when supply reaches HIGH (level 4)
    // We don't wait for ABUNDANT (can be unstable)
    return currentLevel >= supplyLevelOrder["HIGH"]
}
```

### Factory State Tracker Operations

```go
// Track a new factory for production
func (t *FactoryStateTracker) InitFactory(factorySymbol, outputGood string, requiredInputs []string)

// Record input delivery
func (t *FactoryStateTracker) RecordDelivery(factorySymbol, outputGood, inputGood string, quantity int, shipSymbol string)

// Update supply level (from market scan)
func (t *FactoryStateTracker) UpdateSupply(factorySymbol, outputGood, supplyLevel string)

// Check if factory is ready for collection
func (t *FactoryStateTracker) IsReadyForCollection(factorySymbol, outputGood string) bool

// Get all factories ready for collection
func (t *FactoryStateTracker) GetReadyFactories() []*FactoryState
```

---

## Coordinator Design

### Enhanced Manufacturing Coordinator

```go
// application/trading/commands/run_parallel_manufacturing_coordinator.go

type ParallelManufacturingCoordinator struct {
    // Dependencies
    demandFinder       *services.ManufacturingDemandFinder
    pipelinePlanner    *services.PipelinePlanner
    taskQueue          *TaskQueue
    factoryTracker     *manufacturing.FactoryStateTracker
    supplyMonitor      *services.SupplyMonitor
    shipRepo           navigation.ShipRepository
    shipAssignmentRepo container.ShipAssignmentRepository

    // State
    activePipelines    map[string]*manufacturing.ManufacturingPipeline
    activeWorkers      map[string]*TaskWorker  // shipSymbol -> worker
}

// Main coordination loop
func (c *ParallelManufacturingCoordinator) Run(ctx context.Context) {
    // Tickers
    opportunityScan := time.NewTicker(2 * time.Minute)
    shipDiscovery := time.NewTicker(30 * time.Second)
    taskAssignment := time.NewTicker(5 * time.Second)

    for {
        select {
        case <-opportunityScan.C:
            // Find new opportunities and create pipelines
            c.scanAndCreatePipelines(ctx)

        case <-shipDiscovery.C:
            // Find idle ships
            c.discoverIdleShips(ctx)

        case <-taskAssignment.C:
            // Assign ready tasks to idle ships
            c.assignTasks(ctx)

        case completion := <-c.workerCompletionChan:
            // Handle task completion
            c.handleTaskCompletion(ctx, completion)

        case <-ctx.Done():
            return
        }
    }
}
```

### Task Assignment Algorithm

```go
func (c *ParallelManufacturingCoordinator) assignTasks(ctx context.Context) {
    // Get ready tasks (sorted by priority)
    readyTasks := c.taskQueue.GetReadyTasks()

    // Get idle ships
    idleShips := c.getIdleShips(ctx)

    // Greedy assignment: for each task, find closest idle ship
    for _, task := range readyTasks {
        if len(idleShips) == 0 {
            break
        }

        // Find closest ship to task's source location
        sourceLocation := c.getTaskSourceLocation(task)
        closestShip, distance := c.findClosestShip(idleShips, sourceLocation)

        if closestShip != nil {
            // Assign task to ship
            c.assignTaskToShip(ctx, task, closestShip)

            // Remove ship from idle pool
            idleShips = removeShip(idleShips, closestShip)
        }
    }
}
```

---

## Worker Design

### Task Worker (Simplified)

```go
// application/trading/commands/run_manufacturing_task_worker.go

type ManufacturingTaskWorkerCommand struct {
    ShipSymbol    string
    Task          *manufacturing.ManufacturingTask
    PlayerID      int
    ContainerID   string
    CoordinatorID string
}

type ManufacturingTaskWorkerHandler struct {
    shipRepo    navigation.ShipRepository
    marketRepo  market.MarketRepository
    mediator    common.Mediator
}

func (h *ManufacturingTaskWorkerHandler) Handle(ctx context.Context, cmd *ManufacturingTaskWorkerCommand) error {
    task := cmd.Task

    switch task.TaskType() {
    case manufacturing.TaskTypeAcquire:
        return h.executeAcquire(ctx, cmd)
    case manufacturing.TaskTypeDeliver:
        return h.executeDeliver(ctx, cmd)
    case manufacturing.TaskTypeCollect:
        return h.executeCollect(ctx, cmd)
    case manufacturing.TaskTypeSell:
        return h.executeSell(ctx, cmd)
    default:
        return fmt.Errorf("unknown task type: %s", task.TaskType())
    }
}

func (h *ManufacturingTaskWorkerHandler) executeAcquire(ctx context.Context, cmd *ManufacturingTaskWorkerCommand) error {
    task := cmd.Task

    // 1. Navigate to source market
    err := h.navigateAndDock(ctx, cmd.ShipSymbol, task.SourceMarket(), cmd.PlayerID)
    if err != nil {
        return fmt.Errorf("failed to navigate to source market: %w", err)
    }

    // 2. Purchase goods
    quantity, cost, err := h.purchaseGoods(ctx, cmd.ShipSymbol, task.Good(), cmd.PlayerID)
    if err != nil {
        return fmt.Errorf("failed to purchase goods: %w", err)
    }

    // 3. Record results
    task.SetActualQuantity(quantity)
    task.SetTotalCost(cost)

    return nil
}

// Similar implementations for executeDeliver, executeCollect, executeSell
```

### Worker Lifecycle

```
1. Coordinator creates TaskWorkerCommand with task
2. Worker container starts
3. Worker executes single task:
   - Navigate to location
   - Perform action (buy/sell/deliver)
   - Record results
4. Worker reports completion to coordinator
5. Worker container completes
6. Ship returns to idle pool
7. Coordinator assigns next task
```

---

## Supply Monitoring

### Background Supply Monitor

```go
// application/trading/services/supply_monitor.go

type SupplyMonitor struct {
    marketRepo     market.MarketRepository
    factoryTracker *manufacturing.FactoryStateTracker
    taskQueue      *TaskQueue
    pollInterval   time.Duration
}

func (m *SupplyMonitor) Run(ctx context.Context) {
    ticker := time.NewTicker(m.pollInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            m.pollFactories(ctx)
        case <-ctx.Done():
            return
        }
    }
}

func (m *SupplyMonitor) pollFactories(ctx context.Context) {
    // Get all factories with pending production
    pendingFactories := m.factoryTracker.GetFactoriesAwaitingProduction()

    for _, factory := range pendingFactories {
        // Get current market data
        marketData, err := m.marketRepo.GetMarketData(ctx, factory.FactorySymbol(), factory.PlayerID())
        if err != nil {
            continue
        }

        // Check supply level
        tradeGood := marketData.FindGood(factory.OutputGood())
        if tradeGood == nil {
            continue
        }

        supply := "MODERATE"
        if tradeGood.Supply() != nil {
            supply = *tradeGood.Supply()
        }

        // Update factory state
        m.factoryTracker.UpdateSupply(factory.FactorySymbol(), factory.OutputGood(), supply)

        // If ready for collection, mark COLLECT tasks as ready
        if m.factoryTracker.IsReadyForCollection(factory.FactorySymbol(), factory.OutputGood()) {
            m.taskQueue.MarkCollectTasksReady(factory.FactorySymbol(), factory.OutputGood())
        }
    }
}
```

---

## Multi-Product Pipeline

### Running Multiple Products Simultaneously

```go
func (c *ParallelManufacturingCoordinator) scanAndCreatePipelines(ctx context.Context) {
    // Get top opportunities
    opportunities, err := c.demandFinder.FindHighDemandManufacturables(ctx, c.systemSymbol, c.playerID, config)
    if err != nil {
        return
    }

    // Create pipelines for top N opportunities (if not already active)
    for _, opp := range opportunities[:c.maxConcurrentPipelines] {
        if c.hasPipelineForGood(opp.Good()) {
            continue // Already have a pipeline for this good
        }

        // Create pipeline
        pipeline, tasks := c.pipelinePlanner.CreatePipeline(opp)

        // Add to active pipelines
        c.activePipelines[pipeline.ID()] = pipeline

        // Add tasks to queue
        for _, task := range tasks {
            c.taskQueue.Enqueue(task)
        }
    }
}
```

### Pipeline Planner

```go
// application/trading/services/pipeline_planner.go

type PipelinePlanner struct {
    supplyChainResolver *goods.SupplyChainResolver
    marketLocator       *MarketLocator
}

// CreatePipeline converts a ManufacturingOpportunity into a pipeline with tasks
func (p *PipelinePlanner) CreatePipeline(opp *trading.ManufacturingOpportunity) (*manufacturing.ManufacturingPipeline, []*manufacturing.ManufacturingTask) {
    pipeline := manufacturing.NewPipeline(opp.Good(), opp.SellMarket().Symbol, opp.PurchasePrice())

    var tasks []*manufacturing.ManufacturingTask

    // Walk the dependency tree and create tasks
    p.createTasksFromTree(opp.DependencyTree(), pipeline, &tasks, nil)

    // Add final SELL task
    sellTask := manufacturing.NewSellTask(
        pipeline.ID(),
        opp.Good(),
        opp.SellMarket().Symbol,
        collectTaskID, // Depends on final COLLECT
    )
    tasks = append(tasks, sellTask)

    return pipeline, tasks
}

func (p *PipelinePlanner) createTasksFromTree(
    node *goods.SupplyChainNode,
    pipeline *manufacturing.ManufacturingPipeline,
    tasks *[]*manufacturing.ManufacturingTask,
    parentDeliverTaskID *string,
) string {
    if node.AcquisitionMethod == goods.AcquisitionBuy {
        // Leaf node: Create ACQUIRE task
        acquireTask := manufacturing.NewAcquireTask(pipeline.ID(), node.Good, node.SourceMarket)
        *tasks = append(*tasks, acquireTask)
        return acquireTask.ID()
    }

    // FABRICATE node: Create tasks for all children first
    var childDeliverIDs []string
    for _, child := range node.Children {
        // Recursively create tasks for child
        childTaskID := p.createTasksFromTree(child, pipeline, tasks, nil)

        // Create DELIVER task for child output
        deliverTask := manufacturing.NewDeliverTask(
            pipeline.ID(),
            child.Good,
            node.FactoryMarket,
            childTaskID, // Depends on child completion
        )
        *tasks = append(*tasks, deliverTask)
        childDeliverIDs = append(childDeliverIDs, deliverTask.ID())
    }

    // Create COLLECT task (depends on all deliveries + supply HIGH)
    collectTask := manufacturing.NewCollectTask(
        pipeline.ID(),
        node.Good,
        node.FactoryMarket,
        childDeliverIDs, // Depends on all deliveries
    )
    *tasks = append(*tasks, collectTask)

    return collectTask.ID()
}
```

---

## Implementation Plan

### Phase 1: Domain Model (2-3 hours)
- [ ] Create `domain/manufacturing/task.go` - ManufacturingTask entity
- [ ] Create `domain/manufacturing/pipeline.go` - ManufacturingPipeline entity
- [ ] Create `domain/manufacturing/factory_state.go` - FactoryState and tracker
- [ ] Create `domain/manufacturing/errors.go` - Domain errors
- [ ] Write BDD tests for task state machine

### Phase 2: Task Queue (1-2 hours)
- [ ] Create `application/trading/services/task_queue.go` - Priority task queue
- [ ] Implement task dependency resolution
- [ ] Implement ready task retrieval (sorted by priority)
- [ ] Write BDD tests for queue operations

### Phase 3: Pipeline Planner (2-3 hours)
- [ ] Create `application/trading/services/pipeline_planner.go`
- [ ] Implement tree-to-tasks conversion
- [ ] Integrate with SupplyChainResolver
- [ ] Write BDD tests for pipeline creation

### Phase 4: Factory State Tracker (2 hours)
- [ ] Implement in-memory factory state tracking
- [ ] Implement delivery recording
- [ ] Implement supply level updates
- [ ] Write BDD tests

### Phase 5: Supply Monitor (1-2 hours)
- [ ] Create `application/trading/services/supply_monitor.go`
- [ ] Implement background polling
- [ ] Implement task readiness updates
- [ ] Write BDD tests

### Phase 6: Task Worker (2-3 hours)
- [ ] Create `application/trading/commands/run_manufacturing_task_worker.go`
- [ ] Implement ACQUIRE execution
- [ ] Implement DELIVER execution
- [ ] Implement COLLECT execution
- [ ] Implement SELL execution
- [ ] Write BDD tests for each task type

### Phase 7: Parallel Coordinator (3-4 hours)
- [ ] Create `application/trading/commands/run_parallel_manufacturing_coordinator.go`
- [ ] Implement pipeline creation from opportunities
- [ ] Implement task assignment algorithm
- [ ] Implement worker spawning
- [ ] Implement completion handling
- [ ] Wire up all components
- [ ] Write integration tests

### Phase 8: CLI Integration (1 hour)
- [ ] Update `manufacturing start` command
- [ ] Add `manufacturing status` command (show pipeline state)
- [ ] Add `manufacturing tasks` command (show task queue)

### Phase 9: Testing & Tuning (2-3 hours)
- [ ] End-to-end testing with real markets
- [ ] Performance tuning
- [ ] Error handling and recovery
- [ ] Documentation updates

**Total Estimated Time: 16-23 hours**

---

## Migration Strategy

### Backward Compatibility

1. Keep existing `manufacturing start` command working during development
2. Add `--parallel` flag to enable new system
3. Once stable, make parallel the default

### Rollout Plan

1. **Phase 1**: Deploy with `--parallel` flag (opt-in)
2. **Phase 2**: Test with 1-2 ships in parallel mode
3. **Phase 3**: Scale to full fleet
4. **Phase 4**: Make parallel mode default
5. **Phase 5**: Deprecate serial mode

### Monitoring

- Track pipeline completion times
- Track ship utilization %
- Track credits/hour from manufacturing
- Compare with serial mode baseline

---

## Appendix: Example Task Graph for LASER_RIFLES

```
LASER_RIFLES requires: DIAMONDS, PLATINUM, ADVANCED_CIRCUITRY
ADVANCED_CIRCUITRY requires: ELECTRONICS, MICROPROCESSORS

Task Dependency Graph:
=====================

[ACQUIRE_DIAMONDS]      [ACQUIRE_PLATINUM]      [ACQUIRE_ELECTRONICS]  [ACQUIRE_MICROPROCESSORS]
         |                      |                        |                       |
         v                      v                        v                       v
[DELIVER_DIAMONDS]      [DELIVER_PLATINUM]      [DELIVER_ELECTRONICS]  [DELIVER_MICROPROCESSORS]
    to Factory-A            to Factory-A             to Factory-B            to Factory-B
         |                      |                        |                       |
         └──────────┬───────────┘                        └───────────┬───────────┘
                    |                                                |
                    |                                    [COLLECT_ADV_CIRCUITRY]
                    |                                         from Factory-B
                    |                                                |
                    |                                                v
                    |                                    [DELIVER_ADV_CIRCUITRY]
                    |                                         to Factory-A
                    |                                                |
                    └────────────────────┬───────────────────────────┘
                                         |
                                         v
                              [COLLECT_LASER_RIFLES]
                                   from Factory-A
                                         |
                                         v
                              [SELL_LASER_RIFLES]
                                 at X1-YZ19-A1
                                         |
                                         v
                                      💰 PROFIT
```

---

## References

- [Arbitrage Analyzer Data Analysis](./ARBITRAGE_OPTIMIZATION_IMPLEMENTATION_PLAN.md)
- [Supply Chain Configuration](../internal/domain/goods/factory_config.go)
- [Manufacturing Demand Finder](../internal/application/trading/services/manufacturing_demand_finder.go)
- [Production Executor](../internal/application/goods/services/production_executor.go)

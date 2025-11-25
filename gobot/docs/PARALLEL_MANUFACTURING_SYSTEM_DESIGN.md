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

### Sell Market Selection

Choosing the right market to sell our manufactured goods is critical for profit maximization. The criteria are derived from our data analysis.

#### Hard Requirements

A valid sell market MUST:
1. **Import the good** - Market actively purchases this type of good
2. **Have WEAK or RESTRICTED activity** - Stable sell prices (5-14% drift)

Markets with STRONG or GROWING activity are excluded:
- STRONG: 20.3% price drift, -17.46 profit/sec (LOSING)
- GROWING: 33.6% price drift, -517.18 profit/sec (CATASTROPHIC)

#### Scoring Formula

Among valid markets, we score using a weighted formula:

```
score = (price × 0.40) + (activityScore × 0.30) + (supplyScore × 0.20) + (depthBonus × 0.10)
```

| Factor | Weight | Description |
|--------|--------|-------------|
| **Purchase Price** | 40% | Higher price = more profit per unit |
| **Activity Level** | 30% | WEAK=100, RESTRICTED=75 (price stability) |
| **Supply Level** | 20% | LOW/SCARCE=100 (high demand, can absorb volume) |
| **Dependency Depth** | 10% | Deeper supply chains = less competition |

#### Activity Level Scoring

| Activity | Score | Price Drift | Reasoning |
|----------|-------|-------------|-----------|
| **WEAK** | 100 | 5.1% | Most stable, predictable prices |
| **RESTRICTED** | 75 | 14.5% | Acceptable risk, often high profit |
| STRONG | 0 | 20.3% | Too volatile, excluded |
| GROWING | 0 | 33.6% | Extremely volatile, excluded |

#### Supply Level Scoring (at Sell Market)

| Supply | Score | Reasoning |
|--------|-------|-----------|
| SCARCE | 100 | Desperate demand, will pay premium |
| LIMITED | 80 | Strong demand, good prices |
| MODERATE | 60 | Balanced market |
| HIGH | 40 | Well-supplied, prices dropping |
| ABUNDANT | 20 | Oversupplied, low prices |

**Note:** This is the OPPOSITE of buy-side scoring. When buying, we want HIGH supply (stable). When selling, we want LOW supply (high demand).

#### Implementation

```go
// SellMarketSelector finds optimal markets to sell manufactured goods
type SellMarketSelector struct {
    marketRepo market.MarketRepository
}

type SellMarketScore struct {
    WaypointSymbol string
    PurchasePrice  int     // What market pays for our goods
    Activity       string
    Supply         string
    Score          float64
}

func (s *SellMarketSelector) SelectBestSellMarket(
    ctx context.Context,
    good string,
    systemSymbol string,
    playerID int,
) (*SellMarketScore, error) {
    // Find all markets that IMPORT this good
    importMarkets := s.findImportMarkets(ctx, good, systemSymbol, playerID)

    var validMarkets []*SellMarketScore

    for _, market := range importMarkets {
        tradeGood := market.FindGood(good)
        if tradeGood == nil {
            continue
        }

        activity := "WEAK"
        if tradeGood.Activity() != nil {
            activity = *tradeGood.Activity()
        }

        // HARD FILTER: Activity must be WEAK or RESTRICTED
        if activity != "WEAK" && activity != "RESTRICTED" {
            continue // Skip volatile markets
        }

        supply := "MODERATE"
        if tradeGood.Supply() != nil {
            supply = *tradeGood.Supply()
        }

        score := s.calculateScore(tradeGood.PurchasePrice(), activity, supply)

        validMarkets = append(validMarkets, &SellMarketScore{
            WaypointSymbol: market.WaypointSymbol(),
            PurchasePrice:  tradeGood.PurchasePrice(),
            Activity:       activity,
            Supply:         supply,
            Score:          score,
        })
    }

    if len(validMarkets) == 0 {
        return nil, fmt.Errorf("no valid sell markets for %s", good)
    }

    // Sort by score descending
    sort.Slice(validMarkets, func(i, j int) bool {
        return validMarkets[i].Score > validMarkets[j].Score
    })

    return validMarkets[0], nil
}

func (s *SellMarketSelector) calculateScore(price int, activity, supply string) float64 {
    // Normalize price (0-100 scale, assume max price ~50000)
    priceScore := float64(price) / 500.0 // 50000 = 100
    if priceScore > 100 {
        priceScore = 100
    }

    // Activity score
    activityScores := map[string]float64{
        "WEAK":       100,
        "RESTRICTED": 75,
        "STRONG":     0,  // Should be filtered out
        "GROWING":    0,  // Should be filtered out
    }
    activityScore := activityScores[activity]

    // Supply score (inverse - low supply = high demand = good for selling)
    supplyScores := map[string]float64{
        "SCARCE":   100,
        "LIMITED":  80,
        "MODERATE": 60,
        "HIGH":     40,
        "ABUNDANT": 20,
    }
    supplyScore := supplyScores[supply]

    // Weighted combination
    return (priceScore * 0.40) + (activityScore * 0.30) + (supplyScore * 0.20)
    // Note: depthBonus (0.10) is added by PipelinePlanner based on supply chain depth
}
```

#### Example: Selecting LASER_RIFLES Sell Market

```
Available Markets Importing LASER_RIFLES:

Market       | Price  | Activity   | Supply   | Eligible | Score
-------------|--------|------------|----------|----------|-------
X1-YZ19-A1   | 31,699 | WEAK       | LIMITED  | ✓        | 84.1
X1-YZ19-C2   | 28,500 | RESTRICTED | MODERATE | ✓        | 68.5
X1-YZ19-B4   | 35,000 | GROWING    | SCARCE   | ✗        | (excluded)
X1-YZ19-D1   | 25,000 | WEAK       | HIGH     | ✓        | 62.0

Calculation for X1-YZ19-A1:
├── Price score:    31699/500 = 63.4 (capped at 100)
├── Activity score: WEAK = 100
├── Supply score:   LIMITED = 80
└── Total:          (63.4×0.4) + (100×0.3) + (80×0.2) = 25.4 + 30 + 16 = 71.4

Winner: X1-YZ19-A1 (WEAK activity + LIMITED supply + good price)
```

#### Why Not Just Pick Highest Price?

The highest price (X1-YZ19-B4 at 35,000) has GROWING activity:
- **33.6% price drift** = By the time we arrive, price could be ~23,000
- **-517.18 profit/sec** = Statistically losing money

The moderate price (X1-YZ19-A1 at 31,699) with WEAK activity:
- **5.1% price drift** = Price likely still ~30,000+ when we arrive
- **+8.31 profit/sec** = Statistically profitable

**Stability beats peak prices.**

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
- **Sell Market Selection** (NEW)

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
11. **[Resilience & Recovery](#resilience--recovery)** (NEW)
12. [Implementation Plan](#implementation-plan)

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

## Resilience & Recovery

The manufacturing system MUST survive daemon restarts without losing progress or materials. This requires:

1. **Persistent State** - All state stored in database, not memory
2. **Idempotent Operations** - Tasks can be safely retried
3. **Cargo Tracking** - Know what each ship is carrying
4. **Automatic Recovery** - Resume from where we left off

### Design Principles

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         RESILIENCE PRINCIPLES                                │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. DATABASE IS THE SOURCE OF TRUTH                                          │
│     - All task/pipeline state persisted to PostgreSQL                       │
│     - In-memory state is a cache, not authoritative                         │
│     - State survives daemon restart                                          │
│                                                                              │
│  2. IDEMPOTENT TASK EXECUTION                                                │
│     - Tasks can be retried safely                                            │
│     - "Navigate to X" is safe if already at X                               │
│     - "Buy 40 units" checks cargo first                                      │
│     - "Sell cargo" handles partial inventory                                 │
│                                                                              │
│  3. CARGO = INVESTMENT                                                       │
│     - Ship cargo represents credits spent                                    │
│     - Never lose track of cargo                                              │
│     - On restart: reconcile cargo → resume or liquidate                     │
│                                                                              │
│  4. FAIL-SAFE RECOVERY                                                       │
│     - Coordinator startup scans for incomplete work                          │
│     - Ships with cargo get priority task assignment                          │
│     - Stranded materials can be sold to recover investment                   │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Database Schema

#### Manufacturing Pipelines Table

```sql
CREATE TABLE manufacturing_pipelines (
    id              VARCHAR(64) PRIMARY KEY,
    player_id       INTEGER NOT NULL REFERENCES players(id),
    product_good    VARCHAR(64) NOT NULL,      -- Final product (LASER_RIFLES)
    sell_market     VARCHAR(64) NOT NULL,      -- Target sell market
    expected_price  INTEGER NOT NULL,          -- Expected sale price

    status          VARCHAR(32) NOT NULL,      -- PLANNING, EXECUTING, COMPLETED, FAILED

    -- Financials
    total_cost      INTEGER DEFAULT 0,         -- Cumulative costs
    total_revenue   INTEGER DEFAULT 0,         -- Revenue from sales
    net_profit      INTEGER DEFAULT 0,         -- Revenue - costs

    -- Timestamps
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,

    CONSTRAINT valid_status CHECK (status IN ('PLANNING', 'EXECUTING', 'COMPLETED', 'FAILED', 'CANCELLED'))
);

CREATE INDEX idx_pipelines_status ON manufacturing_pipelines(status);
CREATE INDEX idx_pipelines_player ON manufacturing_pipelines(player_id);
```

#### Manufacturing Tasks Table

```sql
CREATE TABLE manufacturing_tasks (
    id              VARCHAR(64) PRIMARY KEY,
    pipeline_id     VARCHAR(64) NOT NULL REFERENCES manufacturing_pipelines(id) ON DELETE CASCADE,
    player_id       INTEGER NOT NULL REFERENCES players(id),

    task_type       VARCHAR(32) NOT NULL,      -- ACQUIRE, DELIVER, COLLECT, SELL
    status          VARCHAR(32) NOT NULL,      -- PENDING, READY, ASSIGNED, EXECUTING, COMPLETED, FAILED

    -- What
    good            VARCHAR(64) NOT NULL,
    quantity        INTEGER DEFAULT 0,         -- Target quantity (0 = fill cargo)
    actual_quantity INTEGER DEFAULT 0,         -- Actual quantity handled

    -- Where
    source_market   VARCHAR(64),               -- For ACQUIRE: where to buy
    target_market   VARCHAR(64),               -- For DELIVER/SELL: destination
    factory_symbol  VARCHAR(64),               -- For COLLECT: factory location

    -- Execution
    assigned_ship   VARCHAR(64),               -- Ship symbol executing this task
    priority        INTEGER DEFAULT 0,         -- Higher = more urgent
    retry_count     INTEGER DEFAULT 0,         -- Number of retries
    max_retries     INTEGER DEFAULT 3,         -- Max retry attempts

    -- Results
    total_cost      INTEGER DEFAULT 0,         -- Cost incurred
    total_revenue   INTEGER DEFAULT 0,         -- Revenue earned
    error_message   TEXT,                      -- Last error if failed

    -- Timestamps
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ready_at        TIMESTAMPTZ,               -- When task became ready
    started_at      TIMESTAMPTZ,               -- When execution began
    completed_at    TIMESTAMPTZ,               -- When completed/failed

    CONSTRAINT valid_task_type CHECK (task_type IN ('ACQUIRE', 'DELIVER', 'COLLECT', 'SELL')),
    CONSTRAINT valid_task_status CHECK (status IN ('PENDING', 'READY', 'ASSIGNED', 'EXECUTING', 'COMPLETED', 'FAILED'))
);

CREATE INDEX idx_tasks_pipeline ON manufacturing_tasks(pipeline_id);
CREATE INDEX idx_tasks_status ON manufacturing_tasks(status);
CREATE INDEX idx_tasks_ship ON manufacturing_tasks(assigned_ship);
CREATE INDEX idx_tasks_ready ON manufacturing_tasks(status, priority DESC) WHERE status = 'READY';
```

#### Task Dependencies Table

```sql
CREATE TABLE manufacturing_task_dependencies (
    task_id         VARCHAR(64) NOT NULL REFERENCES manufacturing_tasks(id) ON DELETE CASCADE,
    depends_on_id   VARCHAR(64) NOT NULL REFERENCES manufacturing_tasks(id) ON DELETE CASCADE,

    PRIMARY KEY (task_id, depends_on_id)
);

CREATE INDEX idx_deps_depends_on ON manufacturing_task_dependencies(depends_on_id);
```

#### Factory States Table

```sql
CREATE TABLE manufacturing_factory_states (
    id              SERIAL PRIMARY KEY,
    factory_symbol  VARCHAR(64) NOT NULL,
    output_good     VARCHAR(64) NOT NULL,
    player_id       INTEGER NOT NULL REFERENCES players(id),
    pipeline_id     VARCHAR(64) REFERENCES manufacturing_pipelines(id) ON DELETE CASCADE,

    -- Input tracking (JSONB for flexibility)
    required_inputs JSONB NOT NULL,            -- ["DIAMONDS", "PLATINUM", "ADV_CIRCUITRY"]
    delivered_inputs JSONB DEFAULT '{}',       -- {"DIAMONDS": {"delivered": true, "quantity": 40, "ship": "AGENT-1"}}

    -- Production state
    all_inputs_delivered BOOLEAN DEFAULT FALSE,
    current_supply       VARCHAR(32),          -- SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
    previous_supply      VARCHAR(32),          -- Supply before we delivered
    ready_for_collection BOOLEAN DEFAULT FALSE,

    -- Timestamps
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    inputs_completed_at  TIMESTAMPTZ,
    ready_at             TIMESTAMPTZ,          -- When supply reached HIGH

    UNIQUE(factory_symbol, output_good, pipeline_id)
);

CREATE INDEX idx_factory_pending ON manufacturing_factory_states(ready_for_collection)
    WHERE ready_for_collection = FALSE;
```

### Recovery on Startup

When the coordinator starts, it must recover state from the database:

```go
// application/trading/commands/run_parallel_manufacturing_coordinator.go

func (c *ParallelManufacturingCoordinator) recoverState(ctx context.Context) error {
    logger := common.LoggerFromContext(ctx)
    logger.Log("INFO", "Recovering manufacturing state from database...", nil)

    // Step 1: Load incomplete pipelines
    pipelines, err := c.pipelineRepo.FindByStatus(ctx, c.playerID,
        []string{"PLANNING", "EXECUTING"})
    if err != nil {
        return fmt.Errorf("failed to load pipelines: %w", err)
    }
    logger.Log("INFO", fmt.Sprintf("Found %d incomplete pipelines", len(pipelines)), nil)

    for _, pipeline := range pipelines {
        c.activePipelines[pipeline.ID()] = pipeline
    }

    // Step 2: Load incomplete tasks and rebuild queue
    tasks, err := c.taskRepo.FindIncomplete(ctx, c.playerID)
    if err != nil {
        return fmt.Errorf("failed to load tasks: %w", err)
    }
    logger.Log("INFO", fmt.Sprintf("Found %d incomplete tasks", len(tasks)), nil)

    for _, task := range tasks {
        // Re-evaluate task readiness
        if c.areTaskDependenciesMet(ctx, task) {
            task.MarkReady()
            c.taskRepo.Update(ctx, task)
        }
        c.taskQueue.Enqueue(task)
    }

    // Step 3: Load factory states
    factoryStates, err := c.factoryStateRepo.FindPending(ctx, c.playerID)
    if err != nil {
        return fmt.Errorf("failed to load factory states: %w", err)
    }
    for _, state := range factoryStates {
        c.factoryTracker.LoadState(state)
    }

    // Step 4: Reconcile ship cargo
    err = c.reconcileShipCargo(ctx)
    if err != nil {
        logger.Log("WARN", fmt.Sprintf("Cargo reconciliation warning: %v", err), nil)
    }

    logger.Log("INFO", "State recovery complete", map[string]interface{}{
        "pipelines":      len(c.activePipelines),
        "tasks_in_queue": c.taskQueue.Size(),
        "factory_states": len(factoryStates),
    })

    return nil
}
```

### Cargo Reconciliation

Ships may have cargo from interrupted tasks. We must handle this:

```go
// Cargo reconciliation: what to do with ships that have cargo from interrupted work

func (c *ParallelManufacturingCoordinator) reconcileShipCargo(ctx context.Context) error {
    logger := common.LoggerFromContext(ctx)

    // Get all ships for this player
    ships, err := c.shipRepo.FindByPlayer(ctx, c.playerID)
    if err != nil {
        return err
    }

    for _, ship := range ships {
        if ship.IsCargoEmpty() {
            continue // No cargo to reconcile
        }

        // Check if this ship has an assigned task
        assignedTask, err := c.taskRepo.FindByAssignedShip(ctx, ship.ShipSymbol())
        if err != nil {
            return err
        }

        if assignedTask != nil {
            // Ship has cargo AND an assigned task
            // Check if task matches cargo
            if c.cargoMatchesTask(ship, assignedTask) {
                // Resume the task
                logger.Log("INFO", fmt.Sprintf("Resuming task %s for ship %s with existing cargo",
                    assignedTask.ID(), ship.ShipSymbol()), nil)
                c.resumeTask(ctx, ship, assignedTask)
            } else {
                // Cargo doesn't match task - something went wrong
                // Create a LIQUIDATE task to sell cargo and recover investment
                logger.Log("WARN", fmt.Sprintf("Ship %s has mismatched cargo, creating liquidation task",
                    ship.ShipSymbol()), nil)
                c.createLiquidationTask(ctx, ship)
            }
        } else {
            // Ship has cargo but NO assigned task
            // This is orphaned cargo from a crashed task
            logger.Log("WARN", fmt.Sprintf("Ship %s has orphaned cargo: %v",
                ship.ShipSymbol(), ship.CargoSummary()), nil)

            // Try to find the original task this cargo was for
            originalTask := c.findTaskForCargo(ctx, ship)
            if originalTask != nil && originalTask.Status() != "COMPLETED" {
                // Re-assign ship to the original task
                originalTask.AssignShip(ship.ShipSymbol())
                c.taskRepo.Update(ctx, originalTask)
                c.resumeTask(ctx, ship, originalTask)
            } else {
                // Can't determine what this cargo was for - liquidate it
                c.createLiquidationTask(ctx, ship)
            }
        }
    }

    return nil
}

// cargoMatchesTask checks if ship's cargo matches what the task expects
func (c *ParallelManufacturingCoordinator) cargoMatchesTask(
    ship *navigation.Ship,
    task *manufacturing.ManufacturingTask,
) bool {
    // For DELIVER tasks: ship should have the good being delivered
    if task.TaskType() == manufacturing.TaskTypeDeliver {
        return ship.HasCargo(task.Good())
    }

    // For SELL tasks: ship should have the good being sold
    if task.TaskType() == manufacturing.TaskTypeSell {
        return ship.HasCargo(task.Good())
    }

    // For ACQUIRE/COLLECT: cargo might be empty (about to acquire) or full (acquired)
    // Both are valid states
    return true
}

// createLiquidationTask creates a special task to sell orphaned cargo
func (c *ParallelManufacturingCoordinator) createLiquidationTask(
    ctx context.Context,
    ship *navigation.Ship,
) {
    logger := common.LoggerFromContext(ctx)

    for _, item := range ship.Cargo().Items() {
        // Find best market to sell this good
        sellMarket, err := c.sellMarketSelector.SelectBestSellMarket(
            ctx, item.Symbol(), c.systemSymbol, c.playerID)
        if err != nil {
            logger.Log("WARN", fmt.Sprintf("No sell market for %s, will try any import market",
                item.Symbol()), nil)
            sellMarket = c.findAnyImportMarket(ctx, item.Symbol())
        }

        if sellMarket == nil {
            logger.Log("ERROR", fmt.Sprintf("Cannot find ANY market to sell %s",
                item.Symbol()), nil)
            continue
        }

        // Create liquidation task (special SELL task not tied to a pipeline)
        liquidationTask := manufacturing.NewLiquidationTask(
            ship.ShipSymbol(),
            item.Symbol(),
            item.Units(),
            sellMarket.WaypointSymbol,
        )

        // High priority - recover investment ASAP
        liquidationTask.SetPriority(100)
        liquidationTask.AssignShip(ship.ShipSymbol())
        liquidationTask.MarkReady()

        c.taskRepo.Create(ctx, liquidationTask)
        c.taskQueue.EnqueuePriority(liquidationTask)

        logger.Log("INFO", fmt.Sprintf("Created liquidation task: sell %d %s at %s",
            item.Units(), item.Symbol(), sellMarket.WaypointSymbol), nil)
    }
}
```

### Idempotent Task Execution

Tasks must be safe to retry. Each task type handles partial completion:

```go
// ACQUIRE task - idempotent purchase
func (h *ManufacturingTaskWorkerHandler) executeAcquire(
    ctx context.Context,
    cmd *ManufacturingTaskWorkerCommand,
) error {
    task := cmd.Task
    ship := cmd.Ship

    // Check if we already have the cargo (task was interrupted after purchase)
    if ship.HasCargo(task.Good()) {
        // Already have cargo - mark as complete
        logger.Log("INFO", fmt.Sprintf("Ship %s already has %s cargo, skipping purchase",
            ship.ShipSymbol(), task.Good()), nil)
        task.SetActualQuantity(ship.CargoQuantity(task.Good()))
        return nil
    }

    // Navigate to source market (idempotent - no-op if already there)
    if ship.CurrentLocation().Symbol != task.SourceMarket() {
        err := h.navigateAndDock(ctx, ship.ShipSymbol(), task.SourceMarket(), cmd.PlayerID)
        if err != nil {
            return fmt.Errorf("failed to navigate: %w", err)
        }
    }

    // Purchase goods
    quantity, cost, err := h.purchaseGoods(ctx, ship.ShipSymbol(), task.Good(), cmd.PlayerID)
    if err != nil {
        return fmt.Errorf("failed to purchase: %w", err)
    }

    task.SetActualQuantity(quantity)
    task.SetTotalCost(cost)
    return nil
}

// DELIVER task - idempotent delivery
func (h *ManufacturingTaskWorkerHandler) executeDeliver(
    ctx context.Context,
    cmd *ManufacturingTaskWorkerCommand,
) error {
    task := cmd.Task
    ship := cmd.Ship

    // Check if cargo is empty (already delivered)
    if !ship.HasCargo(task.Good()) {
        logger.Log("INFO", fmt.Sprintf("Ship %s cargo already empty, delivery complete",
            ship.ShipSymbol()), nil)
        return nil
    }

    // Navigate to target (factory)
    if ship.CurrentLocation().Symbol != task.TargetMarket() {
        err := h.navigateAndDock(ctx, ship.ShipSymbol(), task.TargetMarket(), cmd.PlayerID)
        if err != nil {
            return fmt.Errorf("failed to navigate: %w", err)
        }
    }

    // Sell to factory (delivery = selling to factory input market)
    quantity, revenue, err := h.sellGoods(ctx, ship.ShipSymbol(), task.Good(), cmd.PlayerID)
    if err != nil {
        return fmt.Errorf("failed to deliver: %w", err)
    }

    task.SetActualQuantity(quantity)
    task.SetTotalRevenue(revenue) // Factory pays for inputs

    // Update factory state
    h.factoryTracker.RecordDelivery(task.TargetMarket(), task.Good(), quantity, ship.ShipSymbol())

    return nil
}

// SELL task - idempotent sale
func (h *ManufacturingTaskWorkerHandler) executeSell(
    ctx context.Context,
    cmd *ManufacturingTaskWorkerCommand,
) error {
    task := cmd.Task
    ship := cmd.Ship

    // Check current cargo
    cargoQty := ship.CargoQuantity(task.Good())
    if cargoQty == 0 {
        // Already sold - mark complete
        logger.Log("INFO", fmt.Sprintf("Ship %s already sold cargo", ship.ShipSymbol()), nil)
        return nil
    }

    // Navigate to sell market
    if ship.CurrentLocation().Symbol != task.TargetMarket() {
        err := h.navigateAndDock(ctx, ship.ShipSymbol(), task.TargetMarket(), cmd.PlayerID)
        if err != nil {
            return fmt.Errorf("failed to navigate: %w", err)
        }
    }

    // Sell whatever we have (handle partial cargo)
    quantity, revenue, err := h.sellGoods(ctx, ship.ShipSymbol(), task.Good(), cmd.PlayerID)
    if err != nil {
        return fmt.Errorf("failed to sell: %w", err)
    }

    task.SetActualQuantity(quantity)
    task.SetTotalRevenue(revenue)
    return nil
}
```

### Task State Transitions with Persistence

Every state change is persisted immediately:

```go
// Task state machine with persistence
func (t *ManufacturingTask) MarkReady(repo TaskRepository, ctx context.Context) error {
    if t.status != TaskStatusPending {
        return fmt.Errorf("cannot mark %s task as ready", t.status)
    }
    t.status = TaskStatusReady
    t.readyAt = time.Now()
    return repo.Update(ctx, t) // Persist immediately
}

func (t *ManufacturingTask) AssignShip(shipSymbol string, repo TaskRepository, ctx context.Context) error {
    if t.status != TaskStatusReady {
        return fmt.Errorf("cannot assign ship to %s task", t.status)
    }
    t.status = TaskStatusAssigned
    t.assignedShip = shipSymbol
    return repo.Update(ctx, t) // Persist immediately
}

func (t *ManufacturingTask) StartExecution(repo TaskRepository, ctx context.Context) error {
    if t.status != TaskStatusAssigned {
        return fmt.Errorf("cannot start %s task", t.status)
    }
    t.status = TaskStatusExecuting
    t.startedAt = time.Now()
    return repo.Update(ctx, t) // Persist immediately
}

func (t *ManufacturingTask) Complete(repo TaskRepository, ctx context.Context) error {
    if t.status != TaskStatusExecuting {
        return fmt.Errorf("cannot complete %s task", t.status)
    }
    t.status = TaskStatusCompleted
    t.completedAt = time.Now()
    t.assignedShip = "" // Release ship
    return repo.Update(ctx, t) // Persist immediately
}

func (t *ManufacturingTask) Fail(errorMsg string, repo TaskRepository, ctx context.Context) error {
    t.status = TaskStatusFailed
    t.errorMessage = errorMsg
    t.completedAt = time.Now()
    t.retryCount++

    // If retries remaining, transition back to PENDING for retry
    if t.retryCount < t.maxRetries {
        t.status = TaskStatusPending
        t.assignedShip = "" // Release ship for retry
    }

    return repo.Update(ctx, t) // Persist immediately
}
```

### Ship Assignment Persistence

Ship assignments are already persisted (we have this). The key addition is linking assignments to manufacturing tasks:

```go
// When assigning a ship to a task
func (c *ParallelManufacturingCoordinator) assignTaskToShip(
    ctx context.Context,
    task *manufacturing.ManufacturingTask,
    ship *navigation.Ship,
) error {
    // 1. Update task state (persisted)
    err := task.AssignShip(ship.ShipSymbol(), c.taskRepo, ctx)
    if err != nil {
        return err
    }

    // 2. Create ship assignment (persisted)
    assignment := container.NewShipAssignment(
        ship.ShipSymbol(),
        c.playerID,
        fmt.Sprintf("manufacturing-task-%s", task.ID()), // ContainerID links to task
        c.clock,
    )
    err = c.shipAssignmentRepo.Assign(ctx, assignment)
    if err != nil {
        // Rollback task assignment
        task.RollbackAssignment(c.taskRepo, ctx)
        return err
    }

    // 3. Start worker
    // ... spawn worker goroutine/container

    return nil
}
```

### Recovery Scenarios

#### Scenario 1: Daemon Crashes Mid-Navigation

```
Before Crash:
├── Task T1: DELIVER DIAMONDS to Factory-A (status=EXECUTING)
├── Ship AGENT-1: In transit to Factory-A with 40 DIAMONDS
└── Ship assignment: AGENT-1 → manufacturing-task-T1

After Restart:
1. Load task T1 (status=EXECUTING, ship=AGENT-1)
2. Load ship AGENT-1 (IN_TRANSIT to Factory-A, cargo=40 DIAMONDS)
3. Ship cargo matches task → Resume task
4. Wait for transit to complete
5. Dock and deliver DIAMONDS
6. Task completes normally
```

#### Scenario 2: Daemon Crashes After Purchase, Before Delivery

```
Before Crash:
├── Task T1: DELIVER DIAMONDS (status=EXECUTING)
├── Ship AGENT-1: DOCKED at X1-YZ19-B7, cargo=40 DIAMONDS
└── Actually purchased, but delivery not started

After Restart:
1. Load task T1 (status=EXECUTING, ship=AGENT-1)
2. Load ship AGENT-1 (cargo=40 DIAMONDS, location=X1-YZ19-B7)
3. Ship has cargo matching task → Resume
4. Navigate to Factory-A
5. Deliver DIAMONDS
6. Task completes normally
```

#### Scenario 3: Task Assignment Lost (ship has cargo, no task)

```
Before Crash:
├── Task T1 crashed before DB update (status=READY, ship=null)
├── Ship AGENT-1: Has 40 DIAMONDS from interrupted work
└── No ship assignment exists

After Restart:
1. Load task T1 (status=READY, no assigned ship)
2. Reconcile ship cargo: AGENT-1 has DIAMONDS
3. Find task T1 needs DIAMONDS delivered → Re-assign
4. Update T1: ship=AGENT-1, status=ASSIGNED
5. Resume delivery
```

#### Scenario 4: Orphaned Cargo (original task completed by another ship)

```
Before Crash:
├── Task T1 for DIAMONDS (status=COMPLETED by AGENT-2)
├── Ship AGENT-1: Has 40 DIAMONDS (was assigned, but T1 reassigned)
└── AGENT-1's cargo is orphaned

After Restart:
1. Load completed T1 (no recovery needed)
2. Reconcile: AGENT-1 has DIAMONDS with no matching task
3. Cannot match to any task → Create liquidation task
4. Sell DIAMONDS at best available market
5. Recover investment (maybe at loss, but no total loss)
```

### Failure Modes and Handling

| Failure | Detection | Recovery |
|---------|-----------|----------|
| Daemon crash mid-task | Task status=EXECUTING on startup | Resume task from current ship state |
| API error (rate limit) | Task returns error | Retry with backoff (up to 3 retries) |
| Ship destroyed | Ship not found in API | Mark task FAILED, release investment loss |
| Market depleted | Purchase fails (no supply) | Find alternative market or wait |
| Price changed unfavorably | Margin check fails | Abort task, liquidate cargo |
| Factory no longer exports | Collection fails | Abort pipeline, liquidate materials |

### Implementation Checklist

- [ ] Create database migration for manufacturing tables
- [ ] Implement `ManufacturingPipelineRepository`
- [ ] Implement `ManufacturingTaskRepository`
- [ ] Implement `FactoryStateRepository`
- [ ] Add `recoverState()` to coordinator startup
- [ ] Add `reconcileShipCargo()` for orphaned cargo
- [ ] Make all task operations persist immediately
- [ ] Add liquidation task type
- [ ] Add retry logic with exponential backoff
- [ ] Add integration tests for recovery scenarios

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
- [ ] Replace `manufacturing start` command with parallel implementation
- [ ] Add `manufacturing status` command (show pipeline state)
- [ ] Add `manufacturing tasks` command (show task queue)
- [ ] Remove old serial manufacturing code

### Phase 9: Testing & Tuning (2-3 hours)
- [ ] End-to-end testing with real markets
- [ ] Performance tuning
- [ ] Error handling and recovery
- [ ] Documentation updates

**Total Estimated Time: 16-23 hours**

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

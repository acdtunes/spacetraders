# Arbitrage Trading Implementation Plan

**Version:** 1.0
**Last Updated:** 2025-11-22
**Status:** Planning Phase

## Table of Contents

1. [Overview](#overview)
2. [Objectives](#objectives)
3. [Design Decisions](#design-decisions)
4. [Architecture](#architecture)
5. [Component Specifications](#component-specifications)
6. [Data Models](#data-models)
7. [Workflows](#workflows)
8. [Arbitrage Scoring Algorithm](#arbitrage-scoring-algorithm)
9. [Implementation Plan](#implementation-plan)
10. [Success Criteria](#success-criteria)

---

## Overview

The Arbitrage Trading Operation is an intelligent automated trading system for SpaceTraders that discovers and executes profitable arbitrage opportunities within a single system. The system coordinates fleets of light hauler ships to identify price differentials across markets, execute buy-sell cycles, and maximize profits through parallel operation and intelligent opportunity scoring.

### System Architecture Pattern

The implementation follows the **Factory Coordinator** pattern from the existing goods production system, which provides proven solutions for:
- Parallel worker execution across multiple ships
- Dynamic idle ship discovery and assignment
- Goroutine-based concurrency with synchronization
- Shared ship pool management via channels
- Graceful shutdown and recovery

### Two-Container System

1. **Arbitrage Coordinator** - Scans for opportunities, manages ship pools, spawns workers
2. **Arbitrage Workers** - Execute individual buy→navigate→sell cycles in parallel

---

## Objectives

### Primary Goals

1. **Automatic Opportunity Discovery** - Continuously scan all markets in system for arbitrage opportunities
2. **Intelligent Scoring** - Rank opportunities by profit margin, supply, activity, and distance
3. **Parallel Execution** - Multiple ships execute different arbitrage runs simultaneously
4. **Profit Maximization** - Filter by minimum 10% profit margin, prioritize high-value trades
5. **Market-Aware Trading** - Factor supply levels (ABUNDANT, HIGH) and activity (STRONG, GROWING)
6. **Financial Tracking** - Full transaction recording via ledger system

### Non-Functional Requirements

- **Scalability** - Support 10+ hauler ships trading concurrently
- **Efficiency** - Minimize idle ship time through dynamic assignment
- **Reliability** - Survive daemon restarts without losing operation state
- **Observability** - Comprehensive logging and metrics for profitability tracking
- **Maintainability** - Follow hexagonal architecture and CQRS patterns

---

## Design Decisions

### 1. Execution Pattern

**Decision:** Parallel execution (Factory Coordinator pattern)

**Rationale:**
- Multiple ships can trade different goods simultaneously
- Higher throughput than sequential trading
- Better capital utilization (ships don't wait)
- Leverages goroutine-based concurrency

**Pattern:**
```
Coordinator discovers opportunities (every 2 minutes)
    ↓
Discover idle ships (every 30 seconds)
    ↓
Spawn worker for each ship/opportunity pair
    ↓
Workers execute in parallel (goroutines)
    ↓
Wait for batch completion (sync.WaitGroup)
    ↓
Repeat
```

**Alternative Rejected:** Sequential execution (too slow, underutilizes fleet)

### 2. Operational Scope

**Decision:** Single system only (v1)

**Rationale:**
- Simpler initial implementation
- Lower fuel costs (no jump gates)
- Faster trade cycles
- Easier to reason about market relationships
- Can add cross-system arbitrage in v2

**Constraint:** All buy/sell markets must be in same system as coordinator

**Future Enhancement:** Cross-system arbitrage with jump gate navigation

### 3. Profit Threshold

**Decision:** 10% minimum profit margin (configurable)

**Rationale:**
- Conservative threshold ensures high-confidence trades
- Accounts for fuel costs and price volatility
- Reduces risk of unprofitable trades due to market changes
- 10% provides buffer against execution delays

**Formula:**
```
profitMargin = (sellPrice - buyPrice) / buyPrice * 100
viable = profitMargin >= 10%
```

**Configuration:** CLI flag `--min-margin` allows override (default 10%)

### 4. Opportunity Scoring

**Decision:** Weighted scoring formula including profit, supply, activity, and distance

**Formula:**
```
score = (profitMargin × 40.0) + (supplyScore × 20.0) + (activityScore × 20.0) - (distance × 0.1)
```

**Component Weights:**
- **Profit Margin (40%)** - Most important factor
  - Direct measure of potential profit
  - Range: 10%-200%+ (threshold at 10%)

- **Supply Score (20%)** - Risk mitigation
  - ABUNDANT = 20 points
  - HIGH = 15 points
  - MODERATE = 10 points
  - LIMITED = 5 points
  - SCARCE = 0 points
  - Rationale: Higher supply = lower risk of stockouts or price spikes

- **Activity Score (20%)** - Demand indicator
  - STRONG = 20 points
  - GROWING = 15 points
  - WEAK = 5 points
  - RESTRICTED = 0 points
  - Rationale: Higher activity = more stable prices, consistent demand

- **Distance Penalty (0.1 multiplier)** - Fuel efficiency
  - Linear penalty based on distance in units
  - Keeps fuel costs minimal
  - Tiebreaker between similar opportunities

**Example Calculation:**
```
Good: IRON_ORE
Buy Price: 100 credits/unit
Sell Price: 130 credits/unit
Profit Margin: 30%
Supply at Buy: ABUNDANT (20)
Activity at Sell: STRONG (20)
Distance: 50 units

score = (30 × 40.0) + (20 × 20.0) + (20 × 20.0) - (50 × 0.1)
score = 1200 + 400 + 400 - 5
score = 1995
```

**Rationale for Weights:**
- Profit margin weighted highest (40%) because it directly drives revenue
- Supply + Activity combined (40%) provide equal weight to risk/stability
- Distance minimal impact (0.1) - only matters as tiebreaker
- Formula favors high-margin trades in stable markets

**Alternative Rejected:** Pure profit margin ranking (ignores supply risk, market volatility)

### 5. Ship Selection Strategy

**Decision:** Dynamic idle ship discovery (like goods factory)

**Pattern:**
```go
// Every 30 seconds
idleShips := FindIdleLightHaulers(ctx, playerID, shipRepo, assignmentRepo)

// Assign ships to opportunities
for i, ship := range idleShips {
    if i >= len(opportunities) { break }
    SpawnWorker(ship, opportunities[i])
}
```

**Rationale:**
- Ships become available continuously (as workers complete)
- Dynamic discovery maximizes fleet utilization
- No manual ship management required
- Works with variable fleet sizes

**Ship Requirements:**
- Role: HAULER
- Frame: LIGHT (for efficiency)
- Not assigned to any container

### 6. Transaction Tracking

**Decision:** Automatic ledger integration via OperationContext

**Pattern:**
```go
opContext := ledger.OperationContext{
    Type: "arbitrage",
    ID:   containerID,
}

// All purchases automatically tagged
PurchaseCargoCommand{
    Context: opContext,  // Links to arbitrage run
    ...
}

// All sales automatically tagged
SellCargoCommand{
    Context: opContext,  // Links to arbitrage run
    ...
}

// Refueling costs automatically tagged
NavigateRouteCommand{
    Context: opContext,  // Links to arbitrage run
    ...
}
```

**Benefits:**
- Zero manual transaction recording
- All costs/revenues linked to arbitrage operation
- Net profit calculation automatic (revenue - costs - fuel)
- Full P&L reporting via ledger queries

**Data Tracked:**
- Purchase costs (trading_costs category)
- Sale revenues (trading_revenue category)
- Fuel costs (fuel_costs category)
- Net profit per run (calculated)

---

## Architecture

### Hexagonal Architecture Layers

```
┌─────────────────────────────────────────────────────────┐
│                     CLI / gRPC API                       │
│              (Adapters - Entry Points)                   │
│  spacetraders arbitrage scan --system X1-AU21           │
│  spacetraders arbitrage start --system X1-AU21          │
└─────────────────────────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────┐
│              Application Layer (CQRS)                    │
│  ┌──────────────────────────────────┐                   │
│  │         Commands                 │                   │
│  │  - RunArbitrageCoordinator       │                   │
│  │  - RunArbitrageWorker            │                   │
│  ├──────────────────────────────────┤                   │
│  │         Queries                  │                   │
│  │  - FindArbitrageOpportunities    │                   │
│  ├──────────────────────────────────┤                   │
│  │         Services                 │                   │
│  │  - ArbitrageOpportunityFinder    │                   │
│  │  - ArbitrageExecutor             │                   │
│  │  - ArbitrageProfitCalculator     │                   │
│  └──────────────────────────────────┘                   │
└─────────────────────────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────┐
│                   Domain Layer                           │
│  ┌─────────────────────────────────────────┐             │
│  │  Entities:                              │             │
│  │  - ArbitrageOpportunity (value object)  │             │
│  │                                         │             │
│  │  Domain Services:                       │             │
│  │  - ArbitrageAnalyzer                    │             │
│  │                                         │             │
│  │  Ports:                                 │             │
│  │  - MarketRepository                     │             │
│  │  - ShipRepository                       │             │
│  │  - ShipAssignmentRepository             │             │
│  └─────────────────────────────────────────┘             │
└─────────────────────────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────┐
│              Adapter Layer (Infrastructure)              │
│  ┌───────────┐  ┌──────────┐  ┌──────────────┐          │
│  │PostgreSQL │  │SpaceAPI  │  │RoutingService│          │
│  │Repository │  │  Client  │  │  (Dijkstra)  │          │
│  │ (Markets) │  │(Nav/Trade)│  │              │          │
│  └───────────┘  └──────────┘  └──────────────┘          │
└─────────────────────────────────────────────────────────┘
```

### Component Interaction Diagram

```
┌──────────────────────────────────────────────────────────┐
│           Arbitrage Coordinator Container                │
│                                                          │
│  ┌────────────────────┐                                 │
│  │ Idle Ship Pool     │                                 │
│  │  SHIP-1, SHIP-2,   │                                 │
│  │  SHIP-3, SHIP-4    │                                 │
│  └────────────────────┘                                 │
│           │                                              │
│           │ Refresh every 30s                            │
│           ▼                                              │
│  ┌────────────────────┐        ┌──────────────────┐     │
│  │ Scan Opportunities │───────▶│ Score & Filter   │     │
│  │  (Every 2 min)     │        │ (Min 10% margin) │     │
│  └────────────────────┘        └──────────────────┘     │
│           │                              │               │
│           └──────────┬───────────────────┘               │
│                      ▼                                   │
│           ┌────────────────────┐                         │
│           │ Spawn Workers      │                         │
│           │  (Parallel)        │                         │
│           └────────────────────┘                         │
│                      │                                   │
└──────────────────────┼───────────────────────────────────┘
                       │
                       ▼
         ┌─────────────────────────────┐
         │    Arbitrage Workers        │
         │  [Worker-1] [Worker-2] ...  │
         │  (goroutines, parallel)     │
         └─────────────────────────────┘
                │            │
                ▼            ▼
    ┌────────────────┐  ┌────────────────┐
    │  Buy Market    │  │  Sell Market   │
    │  X1-A1-M1      │  │  X1-A1-M5      │
    └────────────────┘  └────────────────┘
                │            │
                └─────┬──────┘
                      ▼
            ┌────────────────────┐
            │ Ledger (Auto)      │
            │ - Purchase: -1000  │
            │ - Sale: +1300      │
            │ - Fuel: -50        │
            │ Net: +250          │
            └────────────────────┘
```

---

## Component Specifications

### 1. Domain Layer

#### 1.1 ArbitrageOpportunity Value Object

**File:** `internal/domain/trading/arbitrage_opportunity.go`

**Purpose:** Immutable representation of a profitable trading opportunity

**Fields:**
```go
type ArbitrageOpportunity struct {
    good              string
    buyMarket         *shared.Waypoint
    sellMarket        *shared.Waypoint
    buyPrice          int               // What we pay (market sell_price)
    sellPrice         int               // What we receive (market purchase_price)
    profitPerUnit     int               // sellPrice - buyPrice
    profitMargin      float64           // (profitPerUnit / buyPrice) × 100
    distance          float64           // Euclidean distance between markets
    estimatedProfit   int               // profitPerUnit × cargoCapacity
    cargoCapacity     int               // Ship cargo capacity
    buySupply         string            // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
    sellActivity      string            // WEAK, GROWING, STRONG, RESTRICTED
    score             float64           // Calculated via scoring algorithm
    viability         bool              // profitMargin >= minMargin
}
```

**Methods:**
```go
// Constructor with validation
func NewArbitrageOpportunity(
    good string,
    buyMarket *shared.Waypoint,
    sellMarket *shared.Waypoint,
    buyPrice int,
    sellPrice int,
    cargoCapacity int,
    buySupply string,
    sellActivity string,
    minMargin float64,
) (*ArbitrageOpportunity, error)

// Calculations
func (o *ArbitrageOpportunity) ProfitPerUnit() int
func (o *ArbitrageOpportunity) ProfitMargin() float64
func (o *ArbitrageOpportunity) EstimatedNetProfit(fuelCost int) int
func (o *ArbitrageOpportunity) IsViable(minMargin float64) bool

// Scoring
func (o *ArbitrageOpportunity) CalculateScore() float64

// Getters (immutability)
func (o *ArbitrageOpportunity) Good() string
func (o *ArbitrageOpportunity) BuyMarket() *shared.Waypoint
func (o *ArbitrageOpportunity) SellMarket() *shared.Waypoint
func (o *ArbitrageOpportunity) Score() float64
```

**Invariants:**
- `profitPerUnit = sellPrice - buyPrice`
- `profitMargin = (profitPerUnit / buyPrice) × 100`
- `estimatedProfit = profitPerUnit × cargoCapacity`
- `viability = profitMargin >= minMargin`
- All fields immutable after construction

**Validation:**
```go
func (o *ArbitrageOpportunity) Validate() error {
    if o.good == "" {
        return errors.New("good symbol required")
    }
    if o.buyPrice <= 0 || o.sellPrice <= 0 {
        return errors.New("prices must be positive")
    }
    if o.sellPrice <= o.buyPrice {
        return errors.New("sell price must exceed buy price")
    }
    if o.cargoCapacity <= 0 {
        return errors.New("cargo capacity must be positive")
    }
    return nil
}
```

#### 1.2 ArbitrageAnalyzer Domain Service

**File:** `internal/domain/trading/arbitrage_analyzer.go`

**Purpose:** Pure business logic for analyzing market pairs

**Methods:**
```go
type ArbitrageAnalyzer struct{}

func NewArbitrageAnalyzer() *ArbitrageAnalyzer

// Analyze single market pair for a good
func (a *ArbitrageAnalyzer) AnalyzeMarketPair(
    good string,
    buyMarket *market.Market,
    buyTradeGood *market.TradeGood,
    sellMarket *market.Market,
    sellTradeGood *market.TradeGood,
    cargoCapacity int,
    minMargin float64,
) (*ArbitrageOpportunity, error)

// Score an opportunity
func (a *ArbitrageAnalyzer) ScoreOpportunity(
    opportunity *ArbitrageOpportunity,
) float64

// Convert supply/activity to numeric scores
func (a *ArbitrageAnalyzer) SupplyToScore(supply string) float64
func (a *ArbitrageAnalyzer) ActivityToScore(activity string) float64
```

**Implementation:**
```go
func (a *ArbitrageAnalyzer) ScoreOpportunity(opp *ArbitrageOpportunity) float64 {
    profitWeight := 40.0
    supplyWeight := 20.0
    activityWeight := 20.0
    distancePenalty := 0.1

    profitScore := opp.profitMargin * profitWeight
    supplyScore := a.SupplyToScore(opp.buySupply) * supplyWeight
    activityScore := a.ActivityToScore(opp.sellActivity) * activityWeight
    distanceScore := opp.distance * distancePenalty

    return profitScore + supplyScore + activityScore - distanceScore
}

func (a *ArbitrageAnalyzer) SupplyToScore(supply string) float64 {
    switch supply {
    case "ABUNDANT":
        return 20.0
    case "HIGH":
        return 15.0
    case "MODERATE":
        return 10.0
    case "LIMITED":
        return 5.0
    case "SCARCE":
        return 0.0
    default:
        return 0.0
    }
}

func (a *ArbitrageAnalyzer) ActivityToScore(activity string) float64 {
    switch activity {
    case "STRONG":
        return 20.0
    case "GROWING":
        return 15.0
    case "WEAK":
        return 5.0
    case "RESTRICTED":
        return 0.0
    default:
        return 0.0
    }
}
```

**Pure Business Logic:** No infrastructure dependencies (database, API, etc.)

#### 1.3 Repository Ports

**File:** `internal/domain/trading/ports.go`

```go
// Note: MarketRepository already exists in domain/market/ports.go
// We reuse existing interfaces

type ArbitrageOpportunityFinder interface {
    FindOpportunities(
        ctx context.Context,
        systemSymbol string,
        playerID int,
        minMargin float64,
        limit int,
    ) ([]*ArbitrageOpportunity, error)
}
```

**Reused Existing Interfaces:**
```go
// From internal/domain/market/ports.go
type MarketRepository interface {
    GetMarketData(ctx, waypointSymbol, playerID) (*Market, error)
    FindAllMarketsInSystem(ctx, systemSymbol, playerID) ([]string, error)
    FindCheapestMarketSelling(ctx, goodSymbol, systemSymbol, playerID) (*CheapestMarketResult, error)
    FindBestMarketBuying(ctx, goodSymbol, systemSymbol, playerID) (*BestMarketBuyingResult, error)
}

// From internal/domain/navigation/ports.go
type ShipRepository interface {
    FindBySymbol(ctx, shipSymbol, playerID) (*Ship, error)
    Save(ctx, ship) error
    // ...
}

// From internal/domain/container/ports.go
type ShipAssignmentRepository interface {
    Assign(ctx, assignment) error
    Release(ctx, shipSymbol, playerID, reason) error
    // ...
}
```

### 2. Application Layer

#### 2.1 ArbitrageOpportunityFinder Service

**File:** `internal/application/trading/services/arbitrage_opportunity_finder.go`

**Purpose:** Orchestrate market scanning and opportunity discovery

**Structure:**
```go
type ArbitrageOpportunityFinder struct {
    marketRepo       market.MarketRepository
    analyzer         *domain.ArbitrageAnalyzer
    waypointProvider navigation.GraphProvider
}

func NewArbitrageOpportunityFinder(
    marketRepo market.MarketRepository,
    analyzer *domain.ArbitrageAnalyzer,
    waypointProvider navigation.GraphProvider,
) *ArbitrageOpportunityFinder

func (f *ArbitrageOpportunityFinder) FindOpportunities(
    ctx context.Context,
    systemSymbol string,
    playerID int,
    cargoCapacity int,
    minMargin float64,
    limit int,
) ([]*domain.ArbitrageOpportunity, error)
```

**Algorithm:**
```go
func (f *Finder) FindOpportunities(...) ([]*ArbitrageOpportunity, error) {
    // 1. Get all markets in system
    marketWaypoints, err := f.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
    if err != nil {
        return nil, fmt.Errorf("failed to fetch markets: %w", err)
    }

    // 2. Load market data for all waypoints
    markets := make(map[string]*market.Market)
    for _, wp := range marketWaypoints {
        m, err := f.marketRepo.GetMarketData(ctx, wp, playerID)
        if err != nil {
            continue // Skip markets with errors
        }
        markets[wp] = m
    }

    // 3. Build good→markets index
    goodsMap := make(map[string]struct {
        buyers  []*market.Market // Markets buying this good (imports)
        sellers []*market.Market // Markets selling this good (exports)
    })

    for _, m := range markets {
        for _, tradeGood := range m.TradeGoods() {
            if m.Exports(tradeGood.Symbol()) {
                // Market sells this good (we can buy)
                entry := goodsMap[tradeGood.Symbol()]
                entry.sellers = append(entry.sellers, m)
                goodsMap[tradeGood.Symbol()] = entry
            }
            if m.Imports(tradeGood.Symbol()) {
                // Market buys this good (we can sell)
                entry := goodsMap[tradeGood.Symbol()]
                entry.buyers = append(entry.buyers, m)
                goodsMap[tradeGood.Symbol()] = entry
            }
        }
    }

    // 4. Analyze all buy/sell pairs
    opportunities := []*domain.ArbitrageOpportunity{}

    for good, markets := range goodsMap {
        for _, sellMarket := range markets.sellers {
            for _, buyMarket := range markets.buyers {
                // Don't trade with same market
                if sellMarket.WaypointSymbol() == buyMarket.WaypointSymbol() {
                    continue
                }

                // Get trade goods
                sellGood := sellMarket.GetTradeGood(good)
                buyGood := buyMarket.GetTradeGood(good)

                if sellGood == nil || buyGood == nil {
                    continue
                }

                // Analyze pair
                opp, err := f.analyzer.AnalyzeMarketPair(
                    good,
                    sellMarket,   // Where we BUY (seller exports)
                    sellGood,
                    buyMarket,    // Where we SELL (buyer imports)
                    buyGood,
                    cargoCapacity,
                    minMargin,
                )

                if err != nil || !opp.IsViable(minMargin) {
                    continue
                }

                // Calculate score
                opp.score = f.analyzer.ScoreOpportunity(opp)
                opportunities = append(opportunities, opp)
            }
        }
    }

    // 5. Sort by score descending
    sort.Slice(opportunities, func(i, j int) bool {
        return opportunities[i].Score() > opportunities[j].Score()
    })

    // 6. Limit results
    if len(opportunities) > limit {
        opportunities = opportunities[:limit]
    }

    return opportunities, nil
}
```

**Key Points:**
- Queries database (kept fresh by scout ships)
- No direct API calls
- Analyzes all possible buy/sell pairs
- Filters by minimum margin
- Sorts by composite score
- Returns top N opportunities

#### 2.2 ArbitrageExecutor Service

**File:** `internal/application/trading/services/arbitrage_executor.go`

**Purpose:** Execute single arbitrage run (buy → navigate → sell)

**Structure:**
```go
type ArbitrageExecutor struct {
    mediator     common.Mediator
    shipRepo     navigation.ShipRepository
    ledgerRepo   ledger.TransactionRepository
}

func NewArbitrageExecutor(
    mediator common.Mediator,
    shipRepo navigation.ShipRepository,
    ledgerRepo ledger.TransactionRepository,
) *ArbitrageExecutor

func (e *ArbitrageExecutor) Execute(
    ctx context.Context,
    ship *navigation.Ship,
    opportunity *domain.ArbitrageOpportunity,
    playerID int,
    containerID string,
) (*ArbitrageResult, error)
```

**Return Type:**
```go
type ArbitrageResult struct {
    Good            string
    UnitsPurchased  int
    UnitsSold       int
    PurchaseCost    int
    SaleRevenue     int
    FuelCost        int
    NetProfit       int
    DurationSeconds int
}
```

**Execution Flow:**
```go
func (e *Executor) Execute(...) (*ArbitrageResult, error) {
    startTime := time.Now()
    result := &ArbitrageResult{Good: opportunity.Good()}

    // Create operation context for transaction tracking
    opContext := ledger.OperationContext{
        Type: "arbitrage",
        ID:   containerID,
    }

    // 1. Navigate to buy market
    navResp, err := e.mediator.Send(ctx, &NavigateRouteCommand{
        ShipSymbol:   ship.ShipSymbol(),
        Destination:  opportunity.BuyMarket(),
        PlayerID:     playerID,
        PreferCruise: false,  // BURN for speed
        Context:      opContext,  // Auto-records fuel costs
    })
    if err != nil {
        return nil, fmt.Errorf("navigation to buy market failed: %w", err)
    }

    // Track fuel cost
    result.FuelCost += navResp.FuelCost

    // 2. Dock at buy market
    _, err = e.mediator.Send(ctx, &DockShipCommand{
        ShipSymbol: ship.ShipSymbol(),
        PlayerID:   playerID,
    })
    if err != nil {
        return nil, fmt.Errorf("docking failed: %w", err)
    }

    // 3. Purchase cargo (fill ship to capacity)
    purchaseResp, err := e.mediator.Send(ctx, &PurchaseCargoCommand{
        ShipSymbol: ship.ShipSymbol(),
        GoodSymbol: opportunity.Good(),
        Units:      ship.AvailableCargoSpace(),
        PlayerID:   playerID,
        Context:    opContext,  // Auto-records purchase transaction
    })
    if err != nil {
        return nil, fmt.Errorf("purchase failed: %w", err)
    }

    result.UnitsPurchased = purchaseResp.UnitsAdded
    result.PurchaseCost = purchaseResp.TotalCost

    // 4. Navigate to sell market
    navResp2, err := e.mediator.Send(ctx, &NavigateRouteCommand{
        ShipSymbol:   ship.ShipSymbol(),
        Destination:  opportunity.SellMarket(),
        PlayerID:     playerID,
        PreferCruise: false,
        Context:      opContext,  // Auto-records fuel costs
    })
    if err != nil {
        return nil, fmt.Errorf("navigation to sell market failed: %w", err)
    }

    result.FuelCost += navResp2.FuelCost

    // 5. Dock at sell market
    _, err = e.mediator.Send(ctx, &DockShipCommand{
        ShipSymbol: ship.ShipSymbol(),
        PlayerID:   playerID,
    })
    if err != nil {
        return nil, fmt.Errorf("docking failed: %w", err)
    }

    // 6. Sell all cargo
    sellResp, err := e.mediator.Send(ctx, &SellCargoCommand{
        ShipSymbol: ship.ShipSymbol(),
        GoodSymbol: opportunity.Good(),
        Units:      purchaseResp.UnitsAdded,
        PlayerID:   playerID,
        Context:    opContext,  // Auto-records sale transaction
    })
    if err != nil {
        return nil, fmt.Errorf("sale failed: %w", err)
    }

    result.UnitsSold = sellResp.UnitsSold
    result.SaleRevenue = sellResp.TotalRevenue

    // 7. Calculate net profit
    result.NetProfit = result.SaleRevenue - result.PurchaseCost - result.FuelCost
    result.DurationSeconds = int(time.Since(startTime).Seconds())

    return result, nil
}
```

**Key Features:**
- Reuses existing commands (NavigateRoute, PurchaseCargo, SellCargo)
- Automatic transaction recording via OperationContext
- Full error handling with context
- Net profit calculation includes fuel
- Returns detailed execution report

#### 2.3 FindArbitrageOpportunitiesQuery

**File:** `internal/application/trading/queries/find_arbitrage_opportunities.go`

**Query:**
```go
type FindArbitrageOpportunitiesQuery struct {
    SystemSymbol  string
    PlayerID      int
    MinMargin     float64  // Default 10.0
    Limit         int      // Default 20
    CargoCapacity int      // Default 40 (light hauler)
}

type FindArbitrageOpportunitiesResponse struct {
    Opportunities []*OpportunityDTO
    TotalScanned  int
    SystemSymbol  string
}

type OpportunityDTO struct {
    Good            string
    BuyMarket       string
    SellMarket      string
    BuyPrice        int
    SellPrice       int
    ProfitPerUnit   int
    ProfitMargin    float64
    EstimatedProfit int
    Distance        float64
    BuySupply       string
    SellActivity    string
    Score           float64
}
```

**Handler:**
```go
type FindArbitrageOpportunitiesHandler struct {
    opportunityFinder *services.ArbitrageOpportunityFinder
}

func (h *Handler) Handle(
    ctx context.Context,
    query FindArbitrageOpportunitiesQuery,
) (*FindArbitrageOpportunitiesResponse, error) {
    // Delegate to service
    opportunities, err := h.opportunityFinder.FindOpportunities(
        ctx,
        query.SystemSymbol,
        query.PlayerID,
        query.CargoCapacity,
        query.MinMargin,
        query.Limit,
    )
    if err != nil {
        return nil, err
    }

    // Convert to DTOs
    dtos := make([]*OpportunityDTO, len(opportunities))
    for i, opp := range opportunities {
        dtos[i] = &OpportunityDTO{
            Good:            opp.Good(),
            BuyMarket:       opp.BuyMarket().Symbol,
            SellMarket:      opp.SellMarket().Symbol,
            BuyPrice:        opp.BuyPrice(),
            SellPrice:       opp.SellPrice(),
            ProfitPerUnit:   opp.ProfitPerUnit(),
            ProfitMargin:    opp.ProfitMargin(),
            EstimatedProfit: opp.EstimatedProfit(),
            Distance:        opp.Distance(),
            BuySupply:       opp.BuySupply(),
            SellActivity:    opp.SellActivity(),
            Score:           opp.Score(),
        }
    }

    return &FindArbitrageOpportunitiesResponse{
        Opportunities: dtos,
        TotalScanned:  len(opportunities),
        SystemSymbol:  query.SystemSymbol,
    }, nil
}
```

#### 2.4 RunArbitrageWorkerCommand

**File:** `internal/application/trading/commands/run_arbitrage_worker.go`

**Command:**
```go
type RunArbitrageWorkerCommand struct {
    ShipSymbol    string
    Opportunity   *domain.ArbitrageOpportunity
    PlayerID      int
    ContainerID   string
}

type RunArbitrageWorkerResponse struct {
    Success        bool
    Good           string
    NetProfit      int
    DurationSeconds int
    Error          string
}
```

**Handler:**
```go
type RunArbitrageWorkerHandler struct {
    executor    *services.ArbitrageExecutor
    shipRepo    navigation.ShipRepository
    mediator    common.Mediator
}

func (h *Handler) Handle(
    ctx context.Context,
    cmd RunArbitrageWorkerCommand,
) (*RunArbitrageWorkerResponse, error) {
    // 1. Load ship
    ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
    if err != nil {
        return &RunArbitrageWorkerResponse{
            Success: false,
            Error:   fmt.Sprintf("ship not found: %v", err),
        }, nil
    }

    // 2. Execute arbitrage run
    result, err := h.executor.Execute(
        ctx,
        ship,
        cmd.Opportunity,
        cmd.PlayerID,
        cmd.ContainerID,
    )
    if err != nil {
        return &RunArbitrageWorkerResponse{
            Success: false,
            Good:    cmd.Opportunity.Good(),
            Error:   err.Error(),
        }, nil
    }

    // 3. Return results
    return &RunArbitrageWorkerResponse{
        Success:         true,
        Good:            result.Good,
        NetProfit:       result.NetProfit,
        DurationSeconds: result.DurationSeconds,
    }, nil
}
```

**Key Points:**
- Single iteration (completes after one buy/sell cycle)
- Uses ArbitrageExecutor service
- Returns immediately (no loops)
- Worker completion handled by ContainerRunner

#### 2.5 RunArbitrageCoordinatorCommand

**File:** `internal/application/trading/commands/run_arbitrage_coordinator.go`

**Command:**
```go
type RunArbitrageCoordinatorCommand struct {
    SystemSymbol string
    PlayerID     int
    ContainerID  string
    MinMargin    float64  // Default 10.0
    MaxWorkers   int      // Default 10
}

type RunArbitrageCoordinatorResponse struct {
    // Never returns (infinite loop)
}
```

**Handler:**
```go
type RunArbitrageCoordinatorHandler struct {
    opportunityFinder  *services.ArbitrageOpportunityFinder
    shipRepo           navigation.ShipRepository
    shipAssignmentRepo container.ShipAssignmentRepository
    daemonClient       grpc.DaemonClient
    mediator           common.Mediator
    clock              shared.Clock
}

func (h *Handler) Handle(
    ctx context.Context,
    cmd RunArbitrageCoordinatorCommand,
) (*RunArbitrageCoordinatorResponse, error) {
    logger := shared.GetLogger()

    // Main coordination loop (infinite)
    opportunityScanInterval := 2 * time.Minute
    shipDiscoveryInterval := 30 * time.Second

    opportunityTicker := time.NewTicker(opportunityScanInterval)
    shipDiscoveryTicker := time.NewTicker(shipDiscoveryInterval)
    defer opportunityTicker.Stop()
    defer shipDiscoveryTicker.Stop()

    var opportunities []*domain.ArbitrageOpportunity
    var idleShips []*navigation.Ship

    for {
        select {
        case <-opportunityTicker.C:
            // Scan for opportunities
            logger.Log("INFO", "Scanning for arbitrage opportunities", map[string]interface{}{
                "system": cmd.SystemSymbol,
            })

            opps, err := h.opportunityFinder.FindOpportunities(
                ctx,
                cmd.SystemSymbol,
                cmd.PlayerID,
                40, // Assume light hauler capacity
                cmd.MinMargin,
                20, // Top 20 opportunities
            )
            if err != nil {
                logger.Log("ERROR", fmt.Sprintf("Failed to scan opportunities: %v", err), nil)
                continue
            }

            opportunities = opps
            logger.Log("INFO", fmt.Sprintf("Found %d arbitrage opportunities", len(opportunities)), nil)

        case <-shipDiscoveryTicker.C:
            // Discover idle ships
            ships, _, err := contract.FindIdleLightHaulers(
                ctx,
                cmd.PlayerID,
                h.shipRepo,
                h.shipAssignmentRepo,
            )
            if err != nil {
                logger.Log("ERROR", fmt.Sprintf("Failed to find idle ships: %v", err), nil)
                continue
            }

            idleShips = ships

            // Spawn workers if we have both ships and opportunities
            if len(idleShips) > 0 && len(opportunities) > 0 {
                h.spawnWorkers(ctx, cmd, idleShips, opportunities)
            }

        case <-ctx.Done():
            // Graceful shutdown
            logger.Log("INFO", "Arbitrage coordinator shutting down", nil)
            return &RunArbitrageCoordinatorResponse{}, nil
        }
    }
}

func (h *Handler) spawnWorkers(
    ctx context.Context,
    cmd RunArbitrageCoordinatorCommand,
    idleShips []*navigation.Ship,
    opportunities []*domain.ArbitrageOpportunity,
) {
    logger := shared.GetLogger()

    // Spawn workers in parallel (goroutines)
    var wg sync.WaitGroup

    maxWorkers := min(len(idleShips), len(opportunities), cmd.MaxWorkers)

    for i := 0; i < maxWorkers; i++ {
        ship := idleShips[i]
        opp := opportunities[i]

        wg.Add(1)
        go func(s *navigation.Ship, o *domain.ArbitrageOpportunity) {
            defer wg.Done()

            // Create worker container ID
            workerID := fmt.Sprintf("arbitrage-worker-%s-%s", s.ShipSymbol(), uuid.New().String()[:8])

            // Create worker command
            workerCmd := &RunArbitrageWorkerCommand{
                ShipSymbol:   s.ShipSymbol(),
                Opportunity:  o,
                PlayerID:     cmd.PlayerID,
                ContainerID:  workerID,
            }

            // Assign ship to worker (atomic)
            err := h.shipAssignmentRepo.Assign(ctx, container.NewShipAssignment(
                s.ShipSymbol(),
                cmd.PlayerID,
                workerID,
                h.clock,
            ))
            if err != nil {
                logger.Log("ERROR", fmt.Sprintf("Failed to assign ship %s: %v", s.ShipSymbol(), err), nil)
                return
            }

            // Start worker via daemon (fire and forget)
            _, err = h.daemonClient.StartArbitrageWorker(ctx, workerCmd)
            if err != nil {
                logger.Log("ERROR", fmt.Sprintf("Failed to start worker for ship %s: %v", s.ShipSymbol(), err), nil)
                h.shipAssignmentRepo.Release(ctx, s.ShipSymbol(), cmd.PlayerID, "worker_start_failed")
                return
            }

            logger.Log("INFO", fmt.Sprintf("Started arbitrage worker: ship=%s good=%s margin=%.1f%%",
                s.ShipSymbol(), o.Good(), o.ProfitMargin()), nil)

        }(ship, opp)
    }

    // Wait for batch completion
    wg.Wait()

    logger.Log("INFO", fmt.Sprintf("Spawned %d arbitrage workers", maxWorkers), nil)
}
```

**Key Features:**
- Parallel execution pattern (like factory coordinator)
- Two timers: opportunity scanning (2min), ship discovery (30s)
- Spawns goroutines for parallel workers
- Uses sync.WaitGroup for batch synchronization
- Fire-and-forget workers (no completion channels needed)
- Ships auto-released by workers on completion
- Infinite loop until context cancelled

### 3. Adapter Layer

#### 3.1 CLI Commands

**File:** `internal/adapters/cli/arbitrage.go`

```go
var arbitrageCmd = &cobra.Command{
    Use:   "arbitrage",
    Short: "Arbitrage trading operations",
}

var arbitrageScanCmd = &cobra.Command{
    Use:   "scan",
    Short: "Scan for arbitrage opportunities in a system",
    Run:   runArbitrageScan,
}

var arbitrageStartCmd = &cobra.Command{
    Use:   "start",
    Short: "Start arbitrage coordinator for continuous trading",
    Run:   runArbitrageStart,
}

func init() {
    // Scan flags
    arbitrageScanCmd.Flags().String("system", "", "System symbol (required)")
    arbitrageScanCmd.Flags().Float64("min-margin", 10.0, "Minimum profit margin %")
    arbitrageScanCmd.Flags().Int("limit", 20, "Max opportunities to show")
    arbitrageScanCmd.MarkFlagRequired("system")

    // Start flags
    arbitrageStartCmd.Flags().String("system", "", "System symbol (required)")
    arbitrageStartCmd.Flags().Float64("min-margin", 10.0, "Minimum profit margin %")
    arbitrageStartCmd.Flags().Int("max-workers", 10, "Max parallel workers")
    arbitrageStartCmd.MarkFlagRequired("system")

    arbitrageCmd.AddCommand(arbitrageScanCmd)
    arbitrageCmd.AddCommand(arbitrageStartCmd)
    rootCmd.AddCommand(arbitrageCmd)
}

func runArbitrageScan(cmd *cobra.Command, args []string) {
    systemSymbol, _ := cmd.Flags().GetString("system")
    minMargin, _ := cmd.Flags().GetFloat64("min-margin")
    limit, _ := cmd.Flags().GetInt("limit")

    // Resolve player
    playerID, err := resolvePlayerID(cmd)
    if err != nil {
        fmt.Printf("Error: %v\n", err)
        os.Exit(1)
    }

    // Send query via mediator
    query := &FindArbitrageOpportunitiesQuery{
        SystemSymbol: systemSymbol,
        PlayerID:     playerID,
        MinMargin:    minMargin,
        Limit:        limit,
    }

    resp, err := mediator.Send(context.Background(), query)
    if err != nil {
        fmt.Printf("Error scanning opportunities: %v\n", err)
        os.Exit(1)
    }

    // Display results
    displayOpportunities(resp.Opportunities)
}

func displayOpportunities(opps []*OpportunityDTO) {
    if len(opps) == 0 {
        fmt.Println("No arbitrage opportunities found.")
        return
    }

    fmt.Printf("\nFound %d arbitrage opportunities:\n\n", len(opps))
    fmt.Println("┌──────┬────────────────┬────────────────┬────────────────┬───────────┬────────┬─────────┬───────┐")
    fmt.Println("│ Rank │ Good           │ Buy Market     │ Sell Market    │ Margin %  │ Profit │ Supply  │ Score │")
    fmt.Println("├──────┼────────────────┼────────────────┼────────────────┼───────────┼────────┼─────────┼───────┤")

    for i, opp := range opps {
        fmt.Printf("│ %4d │ %-14s │ %-14s │ %-14s │ %8.1f%% │ %6d │ %-7s │ %5.0f │\n",
            i+1,
            opp.Good,
            truncate(opp.BuyMarket, 14),
            truncate(opp.SellMarket, 14),
            opp.ProfitMargin,
            opp.EstimatedProfit,
            opp.BuySupply,
            opp.Score,
        )
    }

    fmt.Println("└──────┴────────────────┴────────────────┴────────────────┴───────────┴────────┴─────────┴───────┘")
}

func runArbitrageStart(cmd *cobra.Command, args []string) {
    systemSymbol, _ := cmd.Flags().GetString("system")
    minMargin, _ := cmd.Flags().GetFloat64("min-margin")
    maxWorkers, _ := cmd.Flags().GetInt("max-workers")

    // Resolve player
    playerID, err := resolvePlayerID(cmd)
    if err != nil {
        fmt.Printf("Error: %v\n", err)
        os.Exit(1)
    }

    // Send command via daemon gRPC
    coordinatorCmd := &RunArbitrageCoordinatorCommand{
        SystemSymbol: systemSymbol,
        PlayerID:     playerID,
        MinMargin:    minMargin,
        MaxWorkers:   maxWorkers,
    }

    containerID, err := daemonClient.StartArbitrageCoordinator(context.Background(), coordinatorCmd)
    if err != nil {
        fmt.Printf("Error starting coordinator: %v\n", err)
        os.Exit(1)
    }

    fmt.Printf("Arbitrage coordinator started!\n")
    fmt.Printf("Container ID: %s\n", containerID)
    fmt.Printf("System: %s\n", systemSymbol)
    fmt.Printf("Min Margin: %.1f%%\n", minMargin)
    fmt.Printf("Max Workers: %d\n\n", maxWorkers)
    fmt.Printf("To check status: spacetraders container status --container-id %s\n", containerID)
    fmt.Printf("To stop: spacetraders container stop --container-id %s\n", containerID)
}
```

**Usage Examples:**
```bash
# Scan for opportunities
./bin/spacetraders arbitrage scan --system X1-AU21

# Start coordinator
./bin/spacetraders arbitrage start --system X1-AU21

# Custom margin threshold
./bin/spacetraders arbitrage scan --system X1-AU21 --min-margin 15.0

# Limit workers
./bin/spacetraders arbitrage start --system X1-AU21 --max-workers 5
```

#### 3.2 Container Types

**File:** `internal/domain/container/container_type.go`

```go
const (
    // ... existing types ...
    ContainerTypeArbitrageCoordinator ContainerType = "ARBITRAGE_COORDINATOR"
    ContainerTypeArbitrageWorker      ContainerType = "ARBITRAGE_WORKER"
)
```

---

## Data Models

### No New Database Tables Required

The arbitrage trading system leverages existing infrastructure:

**Existing Tables Used:**
- `ship_assignments` - Ship ownership tracking
- `container_logs` - Container execution history
- `transactions` - Ledger entries (auto-recorded)
- `market_data` - Market prices (kept fresh by scouts)

**No Persistent State:**
- Opportunities scanned fresh each cycle (2 min interval)
- Workers are ephemeral (single iteration)
- Coordinator runs continuously (no state to persist)

**Rationale:**
- Arbitrage opportunities are volatile (market prices change)
- Fresh scanning ensures latest prices
- No need to persist opportunity history
- Transaction ledger provides complete audit trail

---

## Workflows

### Arbitrage Worker Workflow

```
┌─────────────────────────────────────────────────────────┐
│                  Arbitrage Worker                        │
│              (Single Buy→Sell Cycle)                     │
└─────────────────────────────────────────────────────────┘
                         │
                         ▼
              ┌──────────────────────┐
              │ Load Ship            │
              └──────────────────────┘
                         │
                         ▼
              ┌──────────────────────┐
              │ Navigate to          │
              │ Buy Market           │
              │ (NavigateRoute)      │
              └──────────────────────┘
                         │
                         ▼
              ┌──────────────────────┐
              │ Dock Ship            │
              └──────────────────────┘
                         │
                         ▼
              ┌──────────────────────┐
              │ Purchase Cargo       │
              │ (Fill to capacity)   │
              │ → Auto-record txn    │
              └──────────────────────┘
                         │
                         ▼
              ┌──────────────────────┐
              │ Navigate to          │
              │ Sell Market          │
              │ (NavigateRoute)      │
              └──────────────────────┘
                         │
                         ▼
              ┌──────────────────────┐
              │ Dock Ship            │
              └──────────────────────┘
                         │
                         ▼
              ┌──────────────────────┐
              │ Sell All Cargo       │
              │ → Auto-record txn    │
              └──────────────────────┘
                         │
                         ▼
              ┌──────────────────────┐
              │ Calculate Net Profit │
              │ (Revenue-Cost-Fuel)  │
              └──────────────────────┘
                         │
                         ▼
              ┌──────────────────────┐
              │ Complete Worker      │
              │ (Auto-release ship)  │
              └──────────────────────┘
```

### Coordinator Workflow

```
┌──────────────────────────────────────────────────────────┐
│             Arbitrage Coordinator                         │
│              (Infinite Loop Pattern)                      │
└──────────────────────────────────────────────────────────┘
                         │
                         ▼
              ┌──────────────────────┐
              │ Initialize           │
              │ - Opp scan timer     │
              │ - Ship disc timer    │
              └──────────────────────┘
                         │
         ┌───────────────┴───────────────┐
         │                               │
         ▼                               ▼
┌──────────────────┐         ┌──────────────────┐
│ Every 2 Minutes  │         │ Every 30 Seconds │
│                  │         │                  │
│ Scan Markets     │         │ Discover Idle    │
│ for Arbitrage    │         │ Ships            │
│ Opportunities    │         │                  │
└──────────────────┘         └──────────────────┘
         │                               │
         │                               │
         └───────────┬───────────────────┘
                     │
                     ▼
         ┌───────────────────────┐
         │ Both Available?       │
         │ Ships > 0 AND Opps > 0│
         └───────────────────────┘
                 │           │
                 No          Yes
                 │           │
                 └───┐       └───────────────┐
                     │                       │
                     ▼                       ▼
           ┌──────────────┐      ┌────────────────────┐
           │ Continue     │      │ Spawn Workers      │
           │ Loop         │      │ (Parallel)         │
           └──────────────┘      └────────────────────┘
                     ▲                       │
                     │                       ▼
                     │           ┌────────────────────┐
                     │           │ For i = 0 to       │
                     │           │ min(ships, opps,   │
                     │           │ maxWorkers):       │
                     │           └────────────────────┘
                     │                       │
                     │                       ▼
                     │           ┌────────────────────┐
                     │           │ Launch Goroutine   │
                     │           │ - Assign ship      │
                     │           │ - Create worker    │
                     │           │ - Start container  │
                     │           └────────────────────┘
                     │                       │
                     │                       ▼
                     │           ┌────────────────────┐
                     │           │ Wait for Batch     │
                     │           │ (sync.WaitGroup)   │
                     │           └────────────────────┘
                     │                       │
                     └───────────────────────┘
                                             │
                                             ▼
                               ┌──────────────────────┐
                               │ Workers Complete     │
                               │ (Auto-release ships) │
                               └──────────────────────┘
                                             │
                                             │
                                             ▼
                               ┌──────────────────────┐
                               │ Loop Continues       │
                               │ (Until Shutdown)     │
                               └──────────────────────┘
```

---

## Arbitrage Scoring Algorithm

### Detailed Specification

**Purpose:** Rank opportunities to prioritize high-profit, low-risk trades

**Formula:**
```
score = (profitMargin × 40.0) + (supplyScore × 20.0) + (activityScore × 20.0) - (distance × 0.1)
```

### Component Breakdown

#### 1. Profit Margin Component (40% weight)

**Calculation:**
```go
profitMargin = ((sellPrice - buyPrice) / buyPrice) × 100.0
profitScore = profitMargin × 40.0
```

**Range:**
- Minimum viable: 10% (threshold filter)
- Typical: 10%-50%
- Exceptional: 50%-200%+

**Example:**
```
Buy: 100 credits
Sell: 140 credits
Margin: 40%
Score contribution: 40 × 40.0 = 1600 points
```

**Rationale:** Direct measure of profitability, weighted highest

#### 2. Supply Score Component (20% weight)

**Mapping:**
```go
func SupplyToScore(supply string) float64 {
    switch supply {
    case "ABUNDANT": return 20.0  // Best: high availability
    case "HIGH":     return 15.0
    case "MODERATE": return 10.0
    case "LIMITED":  return 5.0
    case "SCARCE":   return 0.0   // Worst: risk of stockout
    default:         return 0.0
    }
}
```

**Score Contribution:**
```go
supplyScore = SupplyToScore(buyMarket.Supply) × 20.0
```

**Maximum:** 20 × 20.0 = 400 points

**Rationale:**
- ABUNDANT supply = less price volatility risk
- Reduces chance of stockouts during purchase
- Indicates stable market conditions

#### 3. Activity Score Component (20% weight)

**Mapping:**
```go
func ActivityToScore(activity string) float64 {
    switch activity {
    case "STRONG":     return 20.0  // Best: high demand
    case "GROWING":    return 15.0
    case "WEAK":       return 5.0
    case "RESTRICTED": return 0.0   // Worst: may refuse trade
    default:           return 0.0
    }
}
```

**Score Contribution:**
```go
activityScore = ActivityToScore(sellMarket.Activity) × 20.0
```

**Maximum:** 20 × 20.0 = 400 points

**Rationale:**
- STRONG activity = high demand, consistent pricing
- RESTRICTED activity = potential trade denial
- Indicates market willingness to buy

#### 4. Distance Penalty Component

**Calculation:**
```go
distance = EuclideanDistance(buyMarket, sellMarket)
distancePenalty = distance × 0.1
```

**Impact:**
- Distance of 10 units = -1 point
- Distance of 100 units = -10 points
- Distance of 500 units = -50 points

**Rationale:**
- Minimal weight (0.1) to avoid dominating score
- Acts as tiebreaker between similar opportunities
- Encourages fuel efficiency

### Complete Example

**Scenario:**
```
Good: PRECIOUS_STONES
Buy Market: X1-AU21-M1
  - Sell Price (our buy): 500 credits
  - Supply: ABUNDANT (20)
Sell Market: X1-AU21-M5
  - Purchase Price (our sell): 750 credits
  - Activity: STRONG (20)
Distance: 45 units
Cargo Capacity: 40 units
```

**Calculation:**
```
1. Profit Margin:
   margin = ((750 - 500) / 500) × 100 = 50%
   profitScore = 50 × 40.0 = 2000

2. Supply Score:
   supplyScore = 20 × 20.0 = 400

3. Activity Score:
   activityScore = 20 × 20.0 = 400

4. Distance Penalty:
   distancePenalty = 45 × 0.1 = 4.5

5. Total Score:
   score = 2000 + 400 + 400 - 4.5
   score = 2795.5
```

**Estimated Profit:**
```
profitPerUnit = 750 - 500 = 250 credits
estimatedProfit = 250 × 40 = 10,000 credits
```

### Comparison Example

**Opportunity A:**
- Margin: 30% → 1200 points
- Supply: ABUNDANT → 400 points
- Activity: STRONG → 400 points
- Distance: 20 units → -2 points
- **Score: 1998**

**Opportunity B:**
- Margin: 50% → 2000 points
- Supply: SCARCE → 0 points
- Activity: WEAK → 100 points
- Distance: 10 units → -1 point
- **Score: 2099**

**Result:** B wins despite lower supply/activity because profit margin dominates

**Opportunity C:**
- Margin: 30% → 1200 points
- Supply: ABUNDANT → 400 points
- Activity: STRONG → 400 points
- Distance: 200 units → -20 points
- **Score: 1980**

**Result:** A wins over C (distance tiebreaker)

### Tuning Considerations

**Current Weights:**
- Profit: 40% (primary driver)
- Risk (Supply + Activity): 40% (equal to profit combined)
- Efficiency (Distance): minimal

**Alternative Weight Profiles:**

**Aggressive (profit-focused):**
```
profitWeight = 60.0
supplyWeight = 10.0
activityWeight = 10.0
distancePenalty = 0.05
```

**Conservative (risk-averse):**
```
profitWeight = 30.0
supplyWeight = 25.0
activityWeight = 25.0
distancePenalty = 0.2
```

**Current profile (balanced)** chosen as default for v1.

---

## Implementation Plan

### Phase 1: Domain Foundation (4-6 hours)

**Goal:** Create domain entities and services

**Files to Create:**
- `internal/domain/trading/arbitrage_opportunity.go`
- `internal/domain/trading/arbitrage_analyzer.go`
- `internal/domain/trading/ports.go`
- `internal/domain/trading/errors.go`

**Deliverables:**
- `ArbitrageOpportunity` value object with validation
- `ArbitrageAnalyzer` domain service with scoring
- Repository port interfaces
- Comprehensive unit tests

**BDD Tests:**
- `test/bdd/features/domain/trading/arbitrage_opportunity.feature`
- `test/bdd/features/domain/trading/arbitrage_analyzer.feature`
- `test/bdd/steps/arbitrage_steps.go`

**Validation:**
- Run `make test-bdd` - all domain tests pass
- Scoring algorithm produces expected results
- Immutability enforced

### Phase 2: Application Services (5-6 hours)

**Goal:** Create opportunity finder and executor services

**Files to Create:**
- `internal/application/trading/services/arbitrage_opportunity_finder.go`
- `internal/application/trading/services/arbitrage_executor.go`

**Deliverables:**
- `ArbitrageOpportunityFinder` with market scanning
- `ArbitrageExecutor` with buy→sell workflow
- Integration with existing commands
- Service-level tests

**BDD Tests:**
- `test/bdd/features/application/trading/opportunity_finder.feature`
- `test/bdd/features/application/trading/arbitrage_executor.feature`

**Validation:**
- Finder scans all markets correctly
- Executor completes full buy/sell cycle
- Transaction recording automatic

### Phase 3: CQRS Commands & Queries (6-8 hours)

**Goal:** Implement command/query handlers

**Files to Create:**
- `internal/application/trading/queries/find_arbitrage_opportunities.go`
- `internal/application/trading/commands/run_arbitrage_worker.go`
- `internal/application/trading/commands/run_arbitrage_coordinator.go`

**Deliverables:**
- `FindArbitrageOpportunitiesQuery` handler
- `RunArbitrageWorkerCommand` handler
- `RunArbitrageCoordinatorCommand` handler
- Mediator registration

**BDD Tests:**
- `test/bdd/features/application/trading/find_opportunities.feature`
- `test/bdd/features/application/trading/arbitrage_worker.feature`
- `test/bdd/features/application/trading/arbitrage_coordinator.feature`

**Validation:**
- Query returns top N opportunities
- Worker executes single run
- Coordinator spawns parallel workers

### Phase 4: Adapter Layer (4-5 hours)

**Goal:** CLI commands and container types

**Files to Create:**
- `internal/adapters/cli/arbitrage.go`

**Files to Modify:**
- `internal/domain/container/container_type.go` (add types)
- Application setup (register handlers)

**Deliverables:**
- `arbitrage scan` CLI command
- `arbitrage start` CLI command
- Container type constants
- gRPC methods (if needed)

**Testing:**
- Manual CLI testing
- Verify output formatting
- Test all flags

**Validation:**
- CLI displays opportunities correctly
- Coordinator starts successfully
- Ships assigned/released properly

### Phase 5: Integration Testing (6-8 hours)

**Goal:** End-to-end validation

**Testing Scenarios:**
1. Scan opportunities in test system
2. Start coordinator with 3 idle ships
3. Verify parallel worker execution
4. Check transaction ledger entries
5. Validate net profit calculations
6. Test graceful shutdown
7. Performance: 10+ concurrent ships

**Performance Targets:**
- Opportunity scan: <5 seconds
- Single arbitrage run: <60 seconds (depends on distance)
- Coordinator overhead: <100ms per cycle

**Validation:**
- All scenarios pass
- No race conditions
- Ledger balances correct
- Ships returned to idle pool

### Phase 6: Documentation & Metrics (3-4 hours)

**Goal:** Complete documentation and observability

**Files to Create/Update:**
- `CLAUDE.md` (usage examples)
- `README.md` (arbitrage section)

**Optional Metrics:**
```go
// internal/adapters/metrics/arbitrage_metrics.go
- arbitrage_opportunities_found_total
- arbitrage_runs_total{status="success|failure"}
- arbitrage_profit_per_run (histogram)
- arbitrage_margin_percent (histogram)
- arbitrage_active_workers (gauge)
```

**Deliverables:**
- Usage documentation
- Troubleshooting guide
- Metrics collection (if time permits)

**Total Estimated Effort:** 28-37 hours

---

## Success Criteria

### Functional Requirements

- [ ] Automatically scan markets for arbitrage opportunities
- [ ] Filter opportunities by minimum 10% profit margin
- [ ] Score opportunities using weighted formula (profit, supply, activity, distance)
- [ ] Execute parallel arbitrage runs (multiple ships simultaneously)
- [ ] Complete buy→navigate→sell cycle for each opportunity
- [ ] Auto-record all transactions (purchases, sales, fuel) via ledger
- [ ] Calculate net profit (revenue - costs - fuel)
- [ ] Return ships to idle pool after completion

### Quality Requirements

- [ ] Follow hexagonal architecture (domain/application/adapters)
- [ ] Use CQRS pattern (commands/queries via mediator)
- [ ] Immutable domain value objects
- [ ] No direct state mutations (use entity methods)
- [ ] Graceful shutdown (coordinator stops cleanly)
- [ ] Comprehensive error handling
- [ ] BDD test coverage >80%

### Performance Requirements

- [ ] Opportunity scan completes in <5 seconds
- [ ] Support 10+ ships trading in parallel
- [ ] Coordinator overhead <100ms per cycle
- [ ] Ship idle time <10% (excluding travel)
- [ ] No database query bottlenecks

### Operational Requirements

- [ ] CLI command for scanning opportunities (`arbitrage scan`)
- [ ] CLI command for starting coordinator (`arbitrage start`)
- [ ] Clear logging at INFO level
- [ ] Configurable parameters (min-margin, max-workers)
- [ ] Integration with existing ledger system
- [ ] Documentation with usage examples

---

## Configuration Parameters

### Coordinator Config

```go
type ArbitrageCoordinatorConfig struct {
    // Required
    SystemSymbol string `json:"systemSymbol"` // e.g., "X1-AU21"
    PlayerID     int    `json:"playerID"`

    // Scoring & Filtering
    MinMargin    float64 `json:"minMargin"`    // Default: 10.0 (%)
    TopN         int     `json:"topN"`         // Default: 20 opportunities

    // Execution Control
    MaxWorkers   int     `json:"maxWorkers"`   // Default: 10 ships

    // Timing
    ScanInterval int     `json:"scanInterval"` // Default: 120 seconds
    ShipDiscoveryInterval int `json:"shipDiscoveryInterval"` // Default: 30 seconds
}
```

### Example Configuration

```json
{
  "systemSymbol": "X1-AU21",
  "playerID": 1,
  "minMargin": 10.0,
  "topN": 20,
  "maxWorkers": 10,
  "scanInterval": 120,
  "shipDiscoveryInterval": 30
}
```

### CLI Flags

**Scan Command:**
```bash
spacetraders arbitrage scan \
  --system X1-AU21 \
  --min-margin 10.0 \
  --limit 20
```

**Start Command:**
```bash
spacetraders arbitrage start \
  --system X1-AU21 \
  --min-margin 10.0 \
  --max-workers 10
```

---

## Future Enhancements

### Phase 2 Features

**Cross-System Arbitrage:**
- Scan opportunities across jump gates
- Calculate jump fuel costs
- Factor gate toll fees
- Higher profit potential, longer cycles

**Dynamic Margin Thresholds:**
- Adjust margin based on market volatility
- Lower threshold during high activity
- Raise threshold during price instability

**Cargo Optimization:**
- Don't always fill to capacity
- Buy optimal quantity based on sell market demand
- Reduce risk of oversupply

**Portfolio Diversification:**
- Assign ships to different good types
- Reduce exposure to single commodity
- Hedge against price crashes

### Phase 3 Features

**Predictive Analytics:**
- Track price trends over time
- Predict market movements
- Avoid goods with declining margins

**Risk Scoring:**
- Volatility index per good
- Market reliability scores
- Adjust opportunity scoring

**Multi-Leg Arbitrage:**
- A → B → C chains
- Higher complexity, higher profits
- Requires route optimization

**Automated Fleet Management:**
- Purchase new haulers when profitable
- Sell underperforming ships
- Optimize fleet composition

---

## References

### Existing Patterns to Follow

- **Factory Coordinator:** `internal/application/goods/commands/run_factory_coordinator.go`
- **Contract Fleet Coordinator:** `internal/application/contract/commands/run_fleet_coordinator.go`
- **Market Locator:** `internal/application/goods/services/market_locator.go`
- **Navigation:** `internal/application/ship/commands/navigate_route.go`
- **Cargo Trading:** `internal/application/ship/commands/{purchase,sell}_cargo.go`
- **Ledger System:** `internal/domain/ledger/` and `internal/application/ledger/`

### Documentation

- **GOODS_FACTORY_IMPLEMENTATION_PLAN.md** - Parallel coordinator pattern
- **LEDGER_IMPLEMENTATION_PLAN.md** - Transaction tracking
- **ARCHITECTURE.md** - Hexagonal architecture principles
- **CLAUDE.md** - Testing strategy, BDD patterns

### External Resources

- SpaceTraders API: https://spacetraders.stoplight.io/
- Market dynamics: https://docs.spacetraders.io/game-concepts/markets
- Arbitrage theory: https://en.wikipedia.org/wiki/Arbitrage

---

**End of Implementation Plan**

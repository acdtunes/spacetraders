# Market Dynamics Metrics Implementation Plan

## Document Information

**Status:** Design Phase
**Created:** 2025-11-22
**Author:** System Design
**Related:** METRICS_IMPLEMENTATION_PLAN.md
**Target Completion:** 6-7 hours total effort

## Table of Contents

1. [Executive Summary](#executive-summary)
2. [Market System Analysis](#market-system-analysis)
3. [Metrics Specification](#metrics-specification)
4. [Architecture Design](#architecture-design)
5. [Implementation Details](#implementation-details)
6. [Grafana Dashboard Design](#grafana-dashboard-design)
7. [PromQL Query Reference](#promql-query-reference)
8. [Testing Strategy](#testing-strategy)
9. [Performance Considerations](#performance-considerations)
10. [Implementation Phases](#implementation-phases)
11. [Appendices](#appendices)

---

## Executive Summary

### Purpose

Extend the existing Prometheus/Grafana metrics system to provide comprehensive observability into market dynamics, including market coverage, price spreads, supply/demand conditions, and scanner performance.

### Context

The SpaceTraders bot already implements core metrics (operational, navigation, financial). Market dynamics metrics fill a critical gap in understanding:
- Trading opportunities (price spreads, best buy/sell locations)
- Market data freshness (scout ship effectiveness)
- Supply/demand conditions across systems
- Market scanner performance and reliability

### Scope

**In Scope:**
- 15 new market-specific metrics
- Real-time market state tracking (current prices, spreads, supply/demand)
- Market scanner performance monitoring
- Trading opportunity identification
- Grafana dashboard with 20+ panels

**Out of Scope:**
- Historical price tracking (requires database schema changes)
- Price volatility indices (requires time-series data)
- Predictive analytics (requires ML models)
- Cross-player market aggregation (privacy/isolation constraints)

### Key Metrics Categories

1. **Market Scanner Performance** (4 metrics) - Track scan success rate, duration, frequency
2. **Market Coverage** (3 metrics) - Monitor data freshness and discovery completeness
3. **Price Dynamics** (3 metrics) - Track spreads, best opportunities, market efficiency
4. **Supply & Demand** (3 metrics) - Monitor supply levels, activity, liquidity
5. **Trading Opportunities** (2 metrics) - Identify profitable routes and best prices

### Success Criteria

- ✅ All 15 market metrics exposed at `/metrics` endpoint
- ✅ MarketScanner instrumented with scan tracking
- ✅ Polling collector updates aggregates every 60s
- ✅ Grafana dashboard provides actionable insights
- ✅ < 1% performance overhead
- ✅ Cardinality stays under 50,000 time series
- ✅ BDD tests verify metric accuracy

---

## Market System Analysis

### Domain Model

#### Market Entity

**File:** `internal/domain/market/market.go`

**Structure:**
```go
type Market struct {
    waypointSymbol string
    tradeGoods     []TradeGood
    lastUpdated    time.Time
}
```

**Key Characteristics:**
- Immutable snapshot model (value object pattern)
- Represents market state at specific point in time
- Created fresh from API data on each scan

**Methods:**
```go
func (m *Market) WaypointSymbol() string
func (m *Market) TradeGoods() []TradeGood
func (m *Market) LastUpdated() time.Time
func (m *Market) FindTradeGood(symbol string) (*TradeGood, error)
```

#### TradeGood Value Object

**File:** `internal/domain/market/trade_good.go`

**Structure:**
```go
type TradeGood struct {
    symbol        string
    supply        *string  // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
    activity      *string  // WEAK, GROWING, STRONG, RESTRICTED
    purchasePrice int      // What market pays TO ships (revenue when selling)
    sellPrice     int      // What market charges FROM ships (cost when buying)
    tradeVolume   int      // Transaction limit per API call
}
```

**Important Price Semantics:**
- **Purchase Price:** Revenue when selling cargo TO the market (market's bid)
- **Sell Price:** Cost when buying cargo FROM the market (market's ask)
- **Spread:** `SellPrice - PurchasePrice` (market's profit margin)

**Example:**
```
Good: IRON_ORE
Sell Price: 150 credits    (you pay this to BUY from market)
Purchase Price: 100 credits (you receive this to SELL to market)
Spread: 50 credits          (market's margin)
```

**Supply Levels (Enum):**
- `SCARCE` - Very low availability, high prices
- `LIMITED` - Low availability, elevated prices
- `MODERATE` - Normal availability, standard prices
- `HIGH` - High availability, lower prices
- `ABUNDANT` - Very high availability, lowest prices

**Activity Levels (Enum):**
- `WEAK` - Low trading activity
- `GROWING` - Increasing trading activity
- `STRONG` - High trading activity
- `RESTRICTED` - Limited trading allowed

**Methods:**
```go
func (tg *TradeGood) Symbol() string
func (tg *TradeGood) Supply() *string
func (tg *TradeGood) Activity() *string
func (tg *TradeGood) PurchasePrice() int
func (tg *TradeGood) SellPrice() int
func (tg *TradeGood) TradeVolume() int
func (tg *TradeGood) GetTransactionLimit() int  // Returns tradeVolume or 0
func (tg *TradeGood) PriceSpread() int          // SellPrice - PurchasePrice
```

### Database Schema

**Table:** `market_data`

```sql
CREATE TABLE market_data (
    waypoint_symbol VARCHAR(255) NOT NULL,
    good_symbol VARCHAR(100) NOT NULL,
    supply VARCHAR(50),            -- Nullable
    activity VARCHAR(50),          -- Nullable
    purchase_price INT NOT NULL,
    sell_price INT NOT NULL,
    trade_volume INT NOT NULL,
    last_updated TIMESTAMP NOT NULL,
    player_id INT NOT NULL,
    PRIMARY KEY (waypoint_symbol, good_symbol),
    INDEX idx_last_updated (last_updated),
    INDEX idx_player_id (player_id)
)
```

**Key Characteristics:**
- One row per (waypoint, good) combination
- Upsert on scan (delete-then-insert in transaction)
- No historical data (only current snapshot)
- Per-player data isolation
- Timestamp-indexed for freshness queries

**Storage Pattern:**
```
Waypoint: X1-A1-STATION
  - IRON_ORE:  supply=ABUNDANT, purchase=100, sell=150, updated=2025-11-22 10:30:00
  - FUEL:      supply=MODERATE, purchase=50,  sell=75,  updated=2025-11-22 10:30:00
  - COPPER:    supply=SCARCE,   purchase=200, sell=300, updated=2025-11-22 10:30:00
```

### Market Data Collection

#### MarketScanner Service

**File:** `internal/application/ship/market_scanner.go`

**Purpose:** Fetch and persist market data from SpaceTraders API

**Method Signature:**
```go
func (s *MarketScanner) ScanAndSaveMarket(
    ctx context.Context,
    playerID int,
    waypointSymbol string,
) error
```

**Workflow:**
1. Call API: `GET /systems/{systemSymbol}/waypoints/{waypointSymbol}/market`
2. Convert API DTO → Domain Market entity
3. Upsert to database (transaction-wrapped delete + insert)
4. Log errors but don't fail caller (non-fatal operation)

**Error Handling:**
- API errors logged but not propagated
- Database errors logged but don't crash
- Designed for opportunistic scanning

**Current Limitations:**
- No retry logic (relies on caller to retry)
- No rate limiting (handled by API client)
- No metrics/telemetry (THIS IS THE GAP)

#### Automatic Scanning Integration Points

**1. RouteExecutor Integration** (`internal/application/ship/route_executor.go`, lines 413-431)

**Context:** Opportunistic scanning during navigation

```go
func (e *RouteExecutor) scanMarketIfPresent(ctx, ship, playerID) {
    if ship.Location().Type() == "MARKETPLACE" {
        e.marketScanner.ScanAndSaveMarket(ctx, playerID, ship.Location().Symbol())
    }
}
```

**Trigger:** After each route segment when ship arrives at marketplace waypoint

**Frequency:** Variable (depends on trade routes)

**2. ScoutTour Command** (`internal/application/scouting/commands/scout_tour.go`)

**Context:** Dedicated market scanning missions

```go
type ScoutTourCommand struct {
    ShipSymbol    string
    Waypoints     []string  // Markets to scan
    MaxIterations int       // -1 for infinite
}
```

**Workflow:**
1. Plan tour route (TSP-optimized)
2. Navigate to each waypoint
3. Scan market
4. Sleep 30s (first iteration) or 60s (subsequent)
5. Repeat until max iterations reached

**Frequency:** Configurable (typically 60s between iterations)

**3. ScoutMarkets Command** (`internal/application/scouting/commands/scout_markets.go`)

**Context:** Fleet-level market scanning coordination

**Workflow:**
1. Fetch all marketplace waypoints in system
2. Distribute markets across scout ships (VRP solver)
3. Create ScoutTour container for each ship
4. Containers run in background indefinitely

**Use Case:** Maintain continuous fresh market data across entire system

### Market Data Queries

**MarketRepository Interface** (`internal/application/common/ports.go`)

```go
type MarketRepository interface {
    SaveMarket(ctx context.Context, market *market.Market, playerID int) error
    GetMarket(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error)
    ListMarketsInSystem(ctx, systemSymbol string, playerID int, maxAgeMinutes *int) ([]*market.Market, error)
    DeleteMarket(ctx context.Context, waypointSymbol string, playerID int) error
}
```

**Key Method: ListMarketsInSystem**

**Purpose:** Fetch all markets in system with optional freshness filter

**Signature:**
```go
func (r *MarketRepositoryGORM) ListMarketsInSystem(
    ctx context.Context,
    systemSymbol string,
    playerID int,
    maxAgeMinutes *int,
) ([]*market.Market, error)
```

**Parameters:**
- `systemSymbol`: Filter by system (e.g., "X1-A1")
- `playerID`: Player-specific data
- `maxAgeMinutes`: Optional freshness filter (e.g., 5 = only markets updated in last 5 min)

**Example Usage:**
```go
// Get all markets in system
markets, _ := repo.ListMarketsInSystem(ctx, "X1-A1", playerID, nil)

// Get only fresh markets (< 5 min old)
fiveMinutes := 5
freshMarkets, _ := repo.ListMarketsInSystem(ctx, "X1-A1", playerID, &fiveMinutes)
```

**Implementation Notes:**
- SQL query with `last_updated > NOW() - INTERVAL X MINUTE` filter
- Groups rows by waypoint to reconstruct Market aggregate
- Returns `[]*market.Market` (slices of pointers)

### Market Analysis Services

#### MarketLocator

**File:** `internal/application/goods/services/market_locator.go`

**Purpose:** Find optimal markets for buying/selling goods

**Key Methods:**

**1. FindCheapestMarketSelling()**
```go
func (l *MarketLocator) FindCheapestMarketSelling(
    ctx context.Context,
    goodSymbol string,
    systemSymbol string,
    playerID int,
) (*market.Market, error)
```
- **Use Case:** Find where to BUY a good (lowest sell price)
- **Returns:** Market with minimum `sellPrice` for specified good

**2. FindBestMarketBuying()**
```go
func (l *MarketLocator) FindBestMarketBuying(
    ctx context.Context,
    goodSymbol string,
    systemSymbol string,
    playerID int,
) (*market.Market, error)
```
- **Use Case:** Find where to SELL a good (highest purchase price)
- **Returns:** Market with maximum `purchasePrice` for specified good

**3. FindBestExportMarket()**
```go
func (l *MarketLocator) FindBestExportMarket(
    ctx context.Context,
    goodSymbol string,
    systemSymbol string,
    playerID int,
) (*market.Market, error)
```
- **Use Case:** Find best source market (considers supply AND activity)
- **Ranking Algorithm:**
  - Activity Score: STRONG=50, GROWING=30, WEAK=10, RESTRICTED=5
  - Supply Score: ABUNDANT=50, HIGH=40, MODERATE=30, LIMITED=20, SCARCE=10
  - Total Score = Activity + Supply (max 100)
- **Returns:** Market with highest total score

**Market Scoring Examples:**
```
Market A: IRON_ORE, supply=ABUNDANT, activity=STRONG
  Score: 50 (supply) + 50 (activity) = 100 (best)

Market B: IRON_ORE, supply=MODERATE, activity=GROWING
  Score: 30 (supply) + 30 (activity) = 60

Market C: IRON_ORE, supply=SCARCE, activity=WEAK
  Score: 10 (supply) + 10 (activity) = 20 (worst)
```

### Trading Commands

#### Cargo Transaction Command

**File:** `internal/application/ship/commands/cargo_transaction.go`

**Unified Handler:** Uses Strategy pattern for buy/sell operations

**Command Structure:**
```go
type CargoTransactionCommand struct {
    ShipSymbol string
    GoodSymbol string
    Units      int
    PlayerID   int
}
```

**Strategies:**
- `PurchaseStrategy` - Buy cargo FROM market (pay sellPrice)
- `SellStrategy` - Sell cargo TO market (receive purchasePrice)

**Transaction Flow:**
1. Fetch ship and market data
2. Check trade volume limits
3. Split transaction if units > trade volume
4. Execute API calls (purchase/sell)
5. Record financial transaction in ledger
6. Update ship cargo state

**Financial Ledger Integration:**

**Purchase Transaction:**
```go
Transaction{
    Type: PURCHASE_CARGO,
    Category: TRADING_COSTS,
    Amount: -units * sellPrice,  // Negative (expense)
    Metadata: {
        "ship_symbol": shipSymbol,
        "good_symbol": goodSymbol,
        "units": units,
        "price_per_unit": sellPrice,
        "waypoint": waypointSymbol,
    }
}
```

**Sell Transaction:**
```go
Transaction{
    Type: SELL_CARGO,
    Category: TRADING_REVENUE,
    Amount: +units * purchasePrice,  // Positive (revenue)
    Metadata: {
        "ship_symbol": shipSymbol,
        "good_symbol": goodSymbol,
        "units": units,
        "price_per_unit": purchasePrice,
        "waypoint": waypointSymbol,
    }
}
```

**Trade Profitability Calculation:**

From transaction metadata:
```go
buyPrice := purchaseTransaction.Metadata["price_per_unit"]
sellPrice := sellTransaction.Metadata["price_per_unit"]
units := sellTransaction.Metadata["units"]

profitPerUnit := sellPrice - buyPrice
totalProfit := profitPerUnit * units
margin := (profitPerUnit / buyPrice) * 100.0  // Percentage
```

---

## Metrics Specification

### Metric Naming Convention

**Format:** `spacetraders_daemon_market_<name>_<unit>_<aggregation>`

**Examples:**
- `spacetraders_daemon_market_scans_total` (counter)
- `spacetraders_daemon_market_scan_duration_seconds` (histogram)
- `spacetraders_daemon_market_coverage_total` (gauge)

### Category 1: Market Scanner Performance

#### 1.1 Market Scans Total

```go
Name: spacetraders_daemon_market_scans_total
Type: Counter
Labels:
  - player_id (string)
  - waypoint_symbol (string)
  - status (string: "success" | "failure")
Description: Total number of market scans attempted
Update: On each ScanAndSaveMarket() call
Source: MarketScanner instrumentation
```

**Use Cases:**
- Track scan volume per waypoint
- Calculate scan success rate
- Identify frequently scanned markets

**PromQL Examples:**
```promql
# Scans per minute
rate(spacetraders_daemon_market_scans_total[1m])

# Success rate
rate(spacetraders_daemon_market_scans_total{status="success"}[5m]) /
rate(spacetraders_daemon_market_scans_total[5m])

# Failed scans
increase(spacetraders_daemon_market_scans_total{status="failure"}[1h])
```

#### 1.2 Market Scan Duration

```go
Name: spacetraders_daemon_market_scan_duration_seconds
Type: Histogram
Labels:
  - player_id (string)
  - waypoint_symbol (string)
Buckets: [0.5, 1, 2, 5, 10]
Description: Duration of market scan operations
Update: On each ScanAndSaveMarket() completion
Source: Time measurement wrapper
```

**Use Cases:**
- Monitor API latency
- Detect slow markets
- Identify performance degradation

**PromQL Examples:**
```promql
# P95 scan duration
histogram_quantile(0.95,
  rate(spacetraders_daemon_market_scan_duration_seconds_bucket[5m])
)

# Average scan duration per waypoint
avg(rate(spacetraders_daemon_market_scan_duration_seconds_sum[5m])) by (waypoint_symbol)

# Slow scans (> 5 seconds)
spacetraders_daemon_market_scan_duration_seconds_bucket{le="5"} -
spacetraders_daemon_market_scan_duration_seconds_bucket{le="2"}
```

#### 1.3 Market Scan Rate

```go
Name: spacetraders_daemon_market_scan_rate
Type: Gauge
Labels:
  - player_id (string)
  - system_symbol (string)
Description: Current scans per minute in system
Update: Polling collector (every 60s)
Source: Derivative of market_scans_total
```

**Use Cases:**
- Monitor scout ship activity
- Verify scan frequency targets
- Detect scanning interruptions

**PromQL Examples:**
```promql
# Current scan rate by system
spacetraders_daemon_market_scan_rate

# Total scan rate across all systems
sum(spacetraders_daemon_market_scan_rate) by (player_id)
```

#### 1.4 Market Scanner Errors

```go
Name: spacetraders_daemon_market_scanner_errors_total
Type: Counter
Labels:
  - player_id (string)
  - error_type (string: "api_error" | "db_error" | "timeout")
Description: Total number of scanner errors by type
Update: On scan failure
Source: Error classification in MarketScanner
```

**Use Cases:**
- Diagnose scanner reliability issues
- Alert on error spikes
- Track error categories

**PromQL Examples:**
```promql
# Errors per minute by type
rate(spacetraders_daemon_market_scanner_errors_total[5m])

# Error rate percentage
rate(spacetraders_daemon_market_scanner_errors_total[5m]) /
rate(spacetraders_daemon_market_scans_total[5m]) * 100
```

### Category 2: Market Coverage

#### 2.1 Market Coverage Total

```go
Name: spacetraders_daemon_market_coverage_total
Type: Gauge
Labels:
  - player_id (string)
  - system_symbol (string)
Description: Total number of markets discovered/scanned in system
Update: Polling collector (every 60s)
Source: COUNT(DISTINCT waypoint_symbol) from market_data table
```

**Use Cases:**
- Track discovery progress
- Verify scout coverage
- Compare system exploration

**PromQL Examples:**
```promql
# Total markets across all systems
sum(spacetraders_daemon_market_coverage_total) by (player_id)

# Markets per system
spacetraders_daemon_market_coverage_total
```

#### 2.2 Market Coverage Fresh

```go
Name: spacetraders_daemon_market_coverage_fresh
Type: Gauge
Labels:
  - player_id (string)
  - system_symbol (string)
  - age_threshold (string: "300s" | "600s" | "3600s")
Description: Number of markets with data fresher than threshold
Update: Polling collector (every 60s)
Source: COUNT where last_updated > NOW() - age_threshold
```

**Use Cases:**
- Monitor data freshness
- Detect scout ship issues
- Alert on stale data regions

**PromQL Examples:**
```promql
# Fresh market count (< 5 min)
spacetraders_daemon_market_coverage_fresh{age_threshold="300s"}

# Coverage percentage
(spacetraders_daemon_market_coverage_fresh /
 spacetraders_daemon_market_coverage_total) * 100

# Stale markets
spacetraders_daemon_market_coverage_total -
spacetraders_daemon_market_coverage_fresh{age_threshold="300s"}
```

#### 2.3 Market Data Age

```go
Name: spacetraders_daemon_market_data_age_seconds
Type: Histogram
Labels:
  - player_id (string)
  - system_symbol (string)
Buckets: [60, 300, 600, 1800, 3600, 7200]
Description: Age distribution of market data (seconds since last_updated)
Update: Polling collector (every 60s)
Source: NOW() - last_updated for each market
```

**Use Cases:**
- Understand staleness distribution
- Identify neglected regions
- Optimize scout routes

**PromQL Examples:**
```promql
# P50 data age
histogram_quantile(0.50,
  rate(spacetraders_daemon_market_data_age_seconds_bucket[5m])
)

# P95 data age (worst freshness)
histogram_quantile(0.95,
  rate(spacetraders_daemon_market_data_age_seconds_bucket[5m])
)

# Markets older than 10 minutes
spacetraders_daemon_market_data_age_seconds_bucket{le="+Inf"} -
spacetraders_daemon_market_data_age_seconds_bucket{le="600"}
```

### Category 3: Price Dynamics

#### 3.1 Market Price Spread

```go
Name: spacetraders_daemon_market_price_spread
Type: Histogram
Labels:
  - player_id (string)
  - good_symbol (string)
Buckets: [10, 50, 100, 500, 1000, 5000, 10000]
Description: Distribution of price spreads (sellPrice - purchasePrice)
Update: Polling collector (every 60s)
Source: Calculated from market_data (sell_price - purchase_price)
```

**Use Cases:**
- Identify trading opportunities
- Compare profitability across goods
- Track market efficiency

**PromQL Examples:**
```promql
# P95 spread per good
histogram_quantile(0.95,
  rate(spacetraders_daemon_market_price_spread_bucket[5m])
) by (good_symbol)

# Average spread
avg(rate(spacetraders_daemon_market_price_spread_sum[5m])) by (good_symbol)

# Goods with high spreads (> 1000)
topk(10,
  avg(spacetraders_daemon_market_price_spread) by (good_symbol)
)
```

#### 3.2 Market Best Spread

```go
Name: spacetraders_daemon_market_best_spread
Type: Gauge
Labels:
  - player_id (string)
  - good_symbol (string)
  - system_symbol (string)
Description: Maximum price spread available for each good in system
Update: Polling collector (every 60s)
Source: MAX(sell_price - purchase_price) per good
```

**Use Cases:**
- Identify best trading opportunities
- Route planning optimization
- Profitability forecasting

**PromQL Examples:**
```promql
# Top 10 trading opportunities
topk(10, spacetraders_daemon_market_best_spread)

# Best spread per good
max(spacetraders_daemon_market_best_spread) by (good_symbol)

# Spreads above 500 credits
spacetraders_daemon_market_best_spread > 500
```

#### 3.3 Market Efficiency Percent

```go
Name: spacetraders_daemon_market_efficiency_percent
Type: Histogram
Labels:
  - player_id (string)
  - good_symbol (string)
Buckets: [5, 10, 25, 50, 75, 100]
Description: Spread as percentage of sell price ((spread / sellPrice) * 100)
Update: Polling collector (every 60s)
Source: Calculated from (sell_price - purchase_price) / sell_price * 100
```

**Use Cases:**
- Measure market maturity
- Compare efficiency across goods
- Identify inefficient markets

**PromQL Examples:**
```promql
# Average market efficiency
avg(spacetraders_daemon_market_efficiency_percent) by (good_symbol)

# Most efficient markets (lowest spread %)
bottomk(10, spacetraders_daemon_market_efficiency_percent)

# Inefficient markets (> 50% spread)
spacetraders_daemon_market_efficiency_percent > 50
```

### Category 4: Supply & Demand

#### 4.1 Market Supply Distribution

```go
Name: spacetraders_daemon_market_supply_distribution
Type: Gauge
Labels:
  - player_id (string)
  - good_symbol (string)
  - supply_level (string: "SCARCE" | "LIMITED" | "MODERATE" | "HIGH" | "ABUNDANT")
Description: Count of markets at each supply level per good
Update: Polling collector (every 60s)
Source: COUNT(*) GROUP BY good_symbol, supply
```

**Use Cases:**
- Identify supply shortages
- Understand market conditions
- Optimize sourcing strategies

**PromQL Examples:**
```promql
# SCARCE supply markets
spacetraders_daemon_market_supply_distribution{supply_level="SCARCE"}

# Supply distribution for IRON_ORE
spacetraders_daemon_market_supply_distribution{good_symbol="IRON_ORE"}

# Total ABUNDANT markets
sum(spacetraders_daemon_market_supply_distribution{supply_level="ABUNDANT"})
```

#### 4.2 Market Activity Distribution

```go
Name: spacetraders_daemon_market_activity_distribution
Type: Gauge
Labels:
  - player_id (string)
  - good_symbol (string)
  - activity_level (string: "WEAK" | "GROWING" | "STRONG" | "RESTRICTED")
Description: Count of markets at each activity level per good
Update: Polling collector (every 60s)
Source: COUNT(*) GROUP BY good_symbol, activity
```

**Use Cases:**
- Find high-activity trading hubs
- Avoid restricted markets
- Track market growth

**PromQL Examples:**
```promql
# STRONG activity markets
spacetraders_daemon_market_activity_distribution{activity_level="STRONG"}

# Activity breakdown for FUEL
spacetraders_daemon_market_activity_distribution{good_symbol="FUEL"}

# RESTRICTED markets (avoid)
sum(spacetraders_daemon_market_activity_distribution{activity_level="RESTRICTED"})
```

#### 4.3 Market Liquidity

```go
Name: spacetraders_daemon_market_liquidity
Type: Gauge
Labels:
  - player_id (string)
  - waypoint_symbol (string)
  - good_symbol (string)
Description: Trade volume limit (max units per transaction)
Update: Polling collector (every 60s)
Source: trade_volume from market_data table
```

**Use Cases:**
- Identify high-liquidity markets
- Plan large cargo transactions
- Avoid transaction splitting

**PromQL Examples:**
```promql
# Average liquidity per good
avg(spacetraders_daemon_market_liquidity) by (good_symbol)

# High-liquidity markets (> 1000 units)
spacetraders_daemon_market_liquidity > 1000

# Total liquidity in system
sum(spacetraders_daemon_market_liquidity) by (system_symbol)
```

### Category 5: Trading Opportunities

#### 5.1 Trade Opportunities Total

```go
Name: spacetraders_daemon_trade_opportunities_total
Type: Gauge
Labels:
  - player_id (string)
  - system_symbol (string)
  - min_margin (string: "10" | "25" | "50" | "100")
Description: Count of profitable trade routes with margin >= threshold
Update: Polling collector (every 60s)
Source: Cross-market spread analysis (buy at min sellPrice, sell at max purchasePrice)
```

**Calculation Logic:**
```go
for each good:
    cheapestMarket = market with MIN(sellPrice)
    bestMarket = market with MAX(purchasePrice)

    if cheapestMarket != bestMarket:
        profit = bestMarket.purchasePrice - cheapestMarket.sellPrice

        if profit >= minMargin:
            opportunities++
```

**Use Cases:**
- Trading strategy planning
- Route optimization
- Profitability forecasting

**PromQL Examples:**
```promql
# Total profitable routes (> 100 margin)
spacetraders_daemon_trade_opportunities_total{min_margin="100"}

# High-margin opportunities (> 500)
spacetraders_daemon_trade_opportunities_total{min_margin="500"}

# Opportunity trend
rate(spacetraders_daemon_trade_opportunities_total[5m])
```

#### 5.2 Market Best Price

```go
Name: spacetraders_daemon_market_best_price
Type: Gauge
Labels:
  - player_id (string)
  - good_symbol (string)
  - system_symbol (string)
  - type (string: "buy" | "sell")
Description: Best price for buying (min sellPrice) or selling (max purchasePrice)
Update: Polling collector (every 60s)
Source: MIN(sell_price) for buy, MAX(purchase_price) for sell
```

**Use Cases:**
- Find optimal trading locations
- Price comparison
- Route planning

**PromQL Examples:**
```promql
# Best buy price for IRON_ORE
spacetraders_daemon_market_best_price{good_symbol="IRON_ORE", type="buy"}

# Best sell price for FUEL
spacetraders_daemon_market_best_price{good_symbol="FUEL", type="sell"}

# Price spread (sell best - buy best)
spacetraders_daemon_market_best_price{type="sell"} -
spacetraders_daemon_market_best_price{type="buy"}
```

---

## Architecture Design

### Component Overview

```
┌─────────────────────────────────────────────────────────────┐
│                   Prometheus HTTP Endpoint                   │
│                   GET :9090/metrics                          │
└───────────────────────────┬─────────────────────────────────┘
                            │ Scrapes
                            ▼
┌─────────────────────────────────────────────────────────────┐
│              Prometheus Registry (Global)                    │
│                                                              │
│  - Container metrics                                         │
│  - Navigation metrics                                        │
│  - Financial metrics                                         │
│  - Market metrics ◄──── NEW                                 │
└───────────────────────────┬─────────────────────────────────┘
                            │ Updated by
                            ▼
┌─────────────────────────────────────────────────────────────┐
│         MarketMetricsCollector (Adapter Layer)               │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐  │
│  │  Event-Based Metrics (Instrumentation)               │  │
│  │  - market_scans_total                                │  │
│  │  - market_scan_duration_seconds                      │  │
│  │  - market_scanner_errors_total                       │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐  │
│  │  Polling-Based Metrics (Periodic Queries)            │  │
│  │  - market_coverage_total                             │  │
│  │  - market_coverage_fresh                             │  │
│  │  - market_data_age_seconds                           │  │
│  │  - market_price_spread                               │  │
│  │  - market_supply_distribution                        │  │
│  │  - market_activity_distribution                      │  │
│  │  - market_liquidity                                  │  │
│  │  - trade_opportunities_total                         │  │
│  │  - market_best_price                                 │  │
│  │                                                       │  │
│  │  Poll Interval: 60s (configurable)                   │  │
│  └──────────────────────────────────────────────────────┘  │
└───────────────────────────┬─────────────────────────────────┘
                            │ Observes / Queries
                            ▼
┌─────────────────────────────────────────────────────────────┐
│            Application Layer (Market Operations)             │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐  │
│  │  MarketScanner.ScanAndSaveMarket()                   │  │
│  │  - Fetch from API                                    │  │
│  │  - Save to database                                  │  │
│  │  - Record metrics ◄──── INSTRUMENTED                │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐  │
│  │  MarketRepository.ListMarketsInSystem()              │  │
│  │  - Query database                                    │  │
│  │  - Filter by age                                     │  │
│  │  - Used by polling collector                         │  │
│  └──────────────────────────────────────────────────────┘  │
└───────────────────────────┬─────────────────────────────────┘
                            │ Reads / Writes
                            ▼
┌─────────────────────────────────────────────────────────────┐
│                   Database (PostgreSQL)                      │
│                                                              │
│  market_data table:                                          │
│  - waypoint_symbol, good_symbol (PK)                         │
│  - supply, activity, prices, volume                          │
│  - last_updated, player_id                                   │
└─────────────────────────────────────────────────────────────┘
```

### MarketMetricsCollector Structure

**File:** `internal/adapters/metrics/market_metrics.go` (NEW)

```go
package metrics

import (
    "context"
    "time"
    "github.com/prometheus/client_golang/prometheus"
    "internal/application/common/ports"
    "internal/domain/market"
)

type MarketMetricsCollector struct {
    marketRepo ports.MarketRepository
    config     *MarketMetricsConfig

    // Scanner Performance Metrics (Event-Based)
    scansTotal         *prometheus.CounterVec
    scanDuration       *prometheus.HistogramVec
    scanRate           *prometheus.GaugeVec
    scannerErrors      *prometheus.CounterVec

    // Coverage Metrics (Polling)
    coverageTotal      *prometheus.GaugeVec
    coverageFresh      *prometheus.GaugeVec
    dataAge            *prometheus.HistogramVec

    // Price Metrics (Polling)
    priceSpread        *prometheus.HistogramVec
    bestSpread         *prometheus.GaugeVec
    efficiency         *prometheus.HistogramVec

    // Supply/Demand Metrics (Polling)
    supplyDistribution *prometheus.GaugeVec
    activityDistribution *prometheus.GaugeVec
    liquidity          *prometheus.GaugeVec

    // Trading Opportunity Metrics (Polling)
    tradeOpportunities *prometheus.GaugeVec
    bestPrice          *prometheus.GaugeVec
}

type MarketMetricsConfig struct {
    PollInterval      time.Duration
    FreshThreshold    time.Duration
    SystemSymbols     []string  // Systems to monitor
}

func NewMarketMetricsCollector(
    repo ports.MarketRepository,
    config *MarketMetricsConfig,
) *MarketMetricsCollector {
    return &MarketMetricsCollector{
        marketRepo: repo,
        config:     config,
        scansTotal: prometheus.NewCounterVec(...),
        // ... initialize all metrics
    }
}

func (c *MarketMetricsCollector) Start(ctx context.Context) {
    // Start polling goroutine
    ticker := time.NewTicker(c.config.PollInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            c.updatePollingMetrics(ctx)
        }
    }
}

func (c *MarketMetricsCollector) RecordScan(
    playerID int,
    waypoint string,
    success bool,
    duration time.Duration,
) {
    status := "success"
    if !success {
        status = "failure"
    }

    c.scansTotal.WithLabelValues(
        strconv.Itoa(playerID),
        waypoint,
        status,
    ).Inc()

    if success {
        c.scanDuration.WithLabelValues(
            strconv.Itoa(playerID),
            waypoint,
        ).Observe(duration.Seconds())
    }
}

func (c *MarketMetricsCollector) updatePollingMetrics(ctx context.Context) {
    for _, systemSymbol := range c.config.SystemSymbols {
        c.updateCoverageMetrics(ctx, systemSymbol)
        c.updatePriceMetrics(ctx, systemSymbol)
        c.updateSupplyDemandMetrics(ctx, systemSymbol)
        c.updateTradingOpportunities(ctx, systemSymbol)
    }
}

func (c *MarketMetricsCollector) updateCoverageMetrics(
    ctx context.Context,
    systemSymbol string,
) {
    // Query all markets in system
    allMarkets, err := c.marketRepo.ListMarketsInSystem(ctx, systemSymbol, playerID, nil)
    if err != nil {
        log.Printf("Failed to query markets: %v", err)
        return
    }

    // Query fresh markets (< FreshThreshold)
    freshThresholdMinutes := int(c.config.FreshThreshold.Minutes())
    freshMarkets, err := c.marketRepo.ListMarketsInSystem(
        ctx, systemSymbol, playerID, &freshThresholdMinutes,
    )

    // Update gauges
    c.coverageTotal.WithLabelValues(
        strconv.Itoa(playerID),
        systemSymbol,
    ).Set(float64(len(allMarkets)))

    c.coverageFresh.WithLabelValues(
        strconv.Itoa(playerID),
        systemSymbol,
        c.config.FreshThreshold.String(),
    ).Set(float64(len(freshMarkets)))

    // Calculate data age distribution
    for _, market := range allMarkets {
        age := time.Since(market.LastUpdated()).Seconds()
        c.dataAge.WithLabelValues(
            strconv.Itoa(playerID),
            systemSymbol,
        ).Observe(age)
    }
}

func (c *MarketMetricsCollector) updatePriceMetrics(
    ctx context.Context,
    systemSymbol string,
) {
    markets, err := c.marketRepo.ListMarketsInSystem(ctx, systemSymbol, playerID, nil)
    if err != nil {
        return
    }

    // Group by good for spread calculations
    goodMarkets := make(map[string][]*market.Market)
    for _, m := range markets {
        for _, tg := range m.TradeGoods() {
            goodMarkets[tg.Symbol()] = append(goodMarkets[tg.Symbol()], m)
        }
    }

    // Calculate spreads and best opportunities
    for goodSymbol, marketsForGood := range goodMarkets {
        maxSpread := 0

        for _, m := range marketsForGood {
            tg, _ := m.FindTradeGood(goodSymbol)
            spread := tg.PriceSpread()

            // Record spread distribution
            c.priceSpread.WithLabelValues(
                strconv.Itoa(playerID),
                goodSymbol,
            ).Observe(float64(spread))

            // Track max spread
            if spread > maxSpread {
                maxSpread = spread
            }

            // Record efficiency
            if tg.SellPrice() > 0 {
                efficiency := float64(spread) / float64(tg.SellPrice()) * 100.0
                c.efficiency.WithLabelValues(
                    strconv.Itoa(playerID),
                    goodSymbol,
                ).Observe(efficiency)
            }
        }

        // Update best spread gauge
        c.bestSpread.WithLabelValues(
            strconv.Itoa(playerID),
            goodSymbol,
            systemSymbol,
        ).Set(float64(maxSpread))
    }
}

func (c *MarketMetricsCollector) updateSupplyDemandMetrics(
    ctx context.Context,
    systemSymbol string,
) {
    markets, err := c.marketRepo.ListMarketsInSystem(ctx, systemSymbol, playerID, nil)
    if err != nil {
        return
    }

    // Count by supply and activity levels
    supplyCount := make(map[string]map[string]int)  // good -> supply -> count
    activityCount := make(map[string]map[string]int) // good -> activity -> count

    for _, m := range markets {
        for _, tg := range m.TradeGoods() {
            good := tg.Symbol()

            // Count supply levels
            if tg.Supply() != nil {
                if supplyCount[good] == nil {
                    supplyCount[good] = make(map[string]int)
                }
                supplyCount[good][*tg.Supply()]++
            }

            // Count activity levels
            if tg.Activity() != nil {
                if activityCount[good] == nil {
                    activityCount[good] = make(map[string]int)
                }
                activityCount[good][*tg.Activity()]++
            }

            // Record liquidity
            c.liquidity.WithLabelValues(
                strconv.Itoa(playerID),
                m.WaypointSymbol(),
                good,
            ).Set(float64(tg.TradeVolume()))
        }
    }

    // Update distribution gauges
    for good, levels := range supplyCount {
        for level, count := range levels {
            c.supplyDistribution.WithLabelValues(
                strconv.Itoa(playerID),
                good,
                level,
            ).Set(float64(count))
        }
    }

    for good, levels := range activityCount {
        for level, count := range levels {
            c.activityDistribution.WithLabelValues(
                strconv.Itoa(playerID),
                good,
                level,
            ).Set(float64(count))
        }
    }
}

func (c *MarketMetricsCollector) updateTradingOpportunities(
    ctx context.Context,
    systemSymbol string,
) {
    markets, err := c.marketRepo.ListMarketsInSystem(ctx, systemSymbol, playerID, nil)
    if err != nil {
        return
    }

    // Find best buy/sell prices per good
    bestBuyPrice := make(map[string]int)   // good -> min sellPrice
    bestSellPrice := make(map[string]int)  // good -> max purchasePrice

    for _, m := range markets {
        for _, tg := range m.TradeGoods() {
            good := tg.Symbol()

            // Track best buy price (lowest sellPrice)
            if tg.SellPrice() > 0 {
                if _, exists := bestBuyPrice[good]; !exists || tg.SellPrice() < bestBuyPrice[good] {
                    bestBuyPrice[good] = tg.SellPrice()
                }
            }

            // Track best sell price (highest purchasePrice)
            if tg.PurchasePrice() > 0 {
                if _, exists := bestSellPrice[good]; !exists || tg.PurchasePrice() > bestSellPrice[good] {
                    bestSellPrice[good] = tg.PurchasePrice()
                }
            }
        }
    }

    // Update best price gauges
    for good, price := range bestBuyPrice {
        c.bestPrice.WithLabelValues(
            strconv.Itoa(playerID),
            good,
            systemSymbol,
            "buy",
        ).Set(float64(price))
    }

    for good, price := range bestSellPrice {
        c.bestPrice.WithLabelValues(
            strconv.Itoa(playerID),
            good,
            systemSymbol,
            "sell",
        ).Set(float64(price))
    }

    // Count profitable trade opportunities
    margins := []int{10, 25, 50, 100}
    for _, minMargin := range margins {
        count := 0

        for good := range bestBuyPrice {
            if buy, sellOk := bestBuyPrice[good]; sellOk {
                if sell, buyOk := bestSellPrice[good]; buyOk {
                    profit := sell - buy
                    if profit >= minMargin {
                        count++
                    }
                }
            }
        }

        c.tradeOpportunities.WithLabelValues(
            strconv.Itoa(playerID),
            systemSymbol,
            strconv.Itoa(minMargin),
        ).Set(float64(count))
    }
}

// Prometheus Collector interface
func (c *MarketMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
    c.scansTotal.Describe(ch)
    c.scanDuration.Describe(ch)
    // ... describe all metrics
}

func (c *MarketMetricsCollector) Collect(ch chan<- prometheus.Metric) {
    c.scansTotal.Collect(ch)
    c.scanDuration.Collect(ch)
    // ... collect all metrics
}
```

### MarketScanner Instrumentation

**File:** `internal/application/ship/market_scanner.go` (MODIFICATIONS)

```go
// Original method (simplified)
func (s *MarketScanner) ScanAndSaveMarket(
    ctx context.Context,
    playerID int,
    waypointSymbol string,
) error {
    // Existing implementation...
    market, err := s.apiClient.GetMarket(ctx, systemSymbol, waypointSymbol)
    if err != nil {
        log.Printf("Failed to fetch market: %v", err)
        return err
    }

    err = s.marketRepo.SaveMarket(ctx, market, playerID)
    if err != nil {
        log.Printf("Failed to save market: %v", err)
        return err
    }

    return nil
}

// INSTRUMENTED VERSION
func (s *MarketScanner) ScanAndSaveMarket(
    ctx context.Context,
    playerID int,
    waypointSymbol string,
) error {
    startTime := time.Now()

    // Execute existing scan logic
    market, err := s.apiClient.GetMarket(ctx, systemSymbol, waypointSymbol)
    if err != nil {
        log.Printf("Failed to fetch market: %v", err)

        // Record failure metrics
        s.recordScanMetrics(playerID, waypointSymbol, false, time.Since(startTime), err)

        return err
    }

    err = s.marketRepo.SaveMarket(ctx, market, playerID)
    if err != nil {
        log.Printf("Failed to save market: %v", err)

        // Record failure metrics
        s.recordScanMetrics(playerID, waypointSymbol, false, time.Since(startTime), err)

        return err
    }

    // Record success metrics
    s.recordScanMetrics(playerID, waypointSymbol, true, time.Since(startTime), nil)

    return nil
}

func (s *MarketScanner) recordScanMetrics(
    playerID int,
    waypointSymbol string,
    success bool,
    duration time.Duration,
    err error,
) {
    // Non-blocking metrics recording
    defer func() {
        if r := recover(); r != nil {
            log.Printf("Metrics recording panic recovered: %v", r)
        }
    }()

    // Use global metrics collector
    if metrics.IsEnabled() {
        metrics.GetMarketCollector().RecordScan(playerID, waypointSymbol, success, duration)

        if !success && err != nil {
            errorType := classifyError(err)
            metrics.GetMarketCollector().RecordError(playerID, errorType)
        }
    }
}

func classifyError(err error) string {
    // Classify error types for metrics
    switch {
    case strings.Contains(err.Error(), "timeout"):
        return "timeout"
    case strings.Contains(err.Error(), "database"):
        return "db_error"
    default:
        return "api_error"
    }
}
```

### Global Metrics Access

**File:** `internal/adapters/metrics/prometheus_collector.go` (MODIFICATIONS)

```go
package metrics

var (
    // Existing collectors
    containerCollector    *ContainerMetricsCollector
    navigationCollector   *NavigationMetricsCollector
    financialCollector    *FinancialMetricsCollector

    // NEW: Market collector
    marketCollector       *MarketMetricsCollector

    metricsEnabled        bool
)

func InitRegistry(
    marketRepo ports.MarketRepository,
    config *Config,
) {
    metricsEnabled = config.Metrics.Enabled

    if !metricsEnabled {
        return
    }

    Registry = prometheus.NewRegistry()

    // Initialize existing collectors...

    // Initialize market collector (NEW)
    marketConfig := &MarketMetricsConfig{
        PollInterval:   config.Metrics.MarketPollInterval,
        FreshThreshold: config.Metrics.MarketFreshThreshold,
        SystemSymbols:  getSystemSymbols(config), // From config or discovery
    }

    marketCollector = NewMarketMetricsCollector(marketRepo, marketConfig)
    Registry.MustRegister(marketCollector)
}

func StartCollectors(ctx context.Context) {
    if !metricsEnabled {
        return
    }

    // Start existing collectors...

    // Start market collector (NEW)
    go marketCollector.Start(ctx)
}

func IsEnabled() bool {
    return metricsEnabled
}

func GetMarketCollector() *MarketMetricsCollector {
    return marketCollector
}
```

### Configuration

**File:** `internal/infrastructure/config/config.go` (ADDITIONS)

```go
type MetricsConfig struct {
    // Existing fields...

    // Market metrics configuration (NEW)
    MarketPollInterval   time.Duration
    MarketFreshThreshold time.Duration
    SystemSymbols        []string
}

func LoadConfig() *Config {
    // Existing config loading...

    // Market metrics defaults (NEW)
    viper.SetDefault("metrics.market_poll_interval", "60s")
    viper.SetDefault("metrics.market_fresh_threshold", "300s")  // 5 minutes
    viper.SetDefault("metrics.system_symbols", []string{})      // Empty = all systems

    viper.BindEnv("metrics.market_poll_interval", "ST_METRICS_MARKET_POLL_INTERVAL")
    viper.BindEnv("metrics.market_fresh_threshold", "ST_METRICS_MARKET_FRESH_THRESHOLD")
    viper.BindEnv("metrics.system_symbols", "ST_METRICS_SYSTEM_SYMBOLS")

    return &Config{
        // ...
        Metrics: MetricsConfig{
            // Existing fields...
            MarketPollInterval:   viper.GetDuration("metrics.market_poll_interval"),
            MarketFreshThreshold: viper.GetDuration("metrics.market_fresh_threshold"),
            SystemSymbols:        viper.GetStringSlice("metrics.system_symbols"),
        },
    }
}
```

**Environment Variables (.env):**
```bash
# Market Metrics Configuration
ST_METRICS_MARKET_POLL_INTERVAL=60s
ST_METRICS_MARKET_FRESH_THRESHOLD=300s
ST_METRICS_SYSTEM_SYMBOLS=X1-A1,X1-B2  # Optional: specific systems
```

---

## Implementation Details

### Phase 1: Core Collector (2 hours)

**Step 1.1: Create market_metrics.go**

Location: `internal/adapters/metrics/market_metrics.go`

**Tasks:**
1. Define `MarketMetricsCollector` struct with all metric fields
2. Implement `NewMarketMetricsCollector()` constructor
3. Define all 15 metrics with proper labels and buckets
4. Implement Prometheus `Collector` interface (`Describe()`, `Collect()`)
5. Add `RecordScan()` method for event-based metrics
6. Add `RecordError()` method for error tracking

**Metrics to Define:**
- scansTotal (Counter)
- scanDuration (Histogram, buckets: [0.5, 1, 2, 5, 10])
- scanRate (Gauge)
- scannerErrors (Counter)
- coverageTotal, coverageFresh (Gauges)
- dataAge (Histogram, buckets: [60, 300, 600, 1800, 3600, 7200])
- priceSpread (Histogram, buckets: [10, 50, 100, 500, 1000, 5000, 10000])
- bestSpread, efficiency (Histogram/Gauge)
- supplyDistribution, activityDistribution, liquidity (Gauges)
- tradeOpportunities, bestPrice (Gauges)

**Step 1.2: Update prometheus_collector.go**

**Tasks:**
1. Add `marketCollector *MarketMetricsCollector` global variable
2. Add `GetMarketCollector()` accessor function
3. Update `InitRegistry()` to register market collector
4. Update `StartCollectors()` to start market polling goroutine

**Step 1.3: Update config.go**

**Tasks:**
1. Add `MarketPollInterval time.Duration` to MetricsConfig
2. Add `MarketFreshThreshold time.Duration` to MetricsConfig
3. Add `SystemSymbols []string` to MetricsConfig
4. Set defaults and bind environment variables

**Step 1.4: Instrument MarketScanner**

Location: `internal/application/ship/market_scanner.go`

**Tasks:**
1. Add `recordScanMetrics()` private method
2. Wrap `ScanAndSaveMarket()` with timing and metrics
3. Add error classification logic
4. Ensure non-blocking metrics (defer/recover)

**Testing:**
```bash
# Start daemon with metrics
ST_METRICS_ENABLED=true ./bin/spacetraders-daemon

# Trigger a scan
./bin/spacetraders scout tour --ship SCOUT-1 --waypoints X1-A1-STATION

# Check metrics
curl http://localhost:9090/metrics | grep market_scans_total
# Expected: spacetraders_daemon_market_scans_total{player_id="1",waypoint_symbol="X1-A1-STATION",status="success"} 1
```

### Phase 2: Polling Metrics (2 hours)

**Step 2.1: Implement Start() Goroutine**

**Tasks:**
1. Create `Start(ctx context.Context)` method
2. Set up ticker with `PollInterval`
3. Call `updatePollingMetrics()` on each tick
4. Handle context cancellation for graceful shutdown

**Step 2.2: Implement Coverage Metrics**

**Method:** `updateCoverageMetrics(ctx, systemSymbol)`

**Logic:**
1. Query all markets: `ListMarketsInSystem(ctx, system, playerID, nil)`
2. Query fresh markets: `ListMarketsInSystem(ctx, system, playerID, &freshThresholdMinutes)`
3. Update `coverageTotal` gauge: `Set(len(allMarkets))`
4. Update `coverageFresh` gauge: `Set(len(freshMarkets))`
5. Loop through markets and observe `dataAge` histogram: `Observe(time.Since(market.LastUpdated()).Seconds())`

**Step 2.3: Implement Price Metrics**

**Method:** `updatePriceMetrics(ctx, systemSymbol)`

**Logic:**
1. Query all markets
2. Group trade goods by symbol: `map[string][]*market.Market`
3. For each good:
   - Calculate spread: `sellPrice - purchasePrice`
   - Observe `priceSpread` histogram
   - Calculate efficiency: `(spread / sellPrice) * 100`
   - Observe `efficiency` histogram
   - Track max spread: `Set(maxSpread)` on `bestSpread` gauge

**Step 2.4: Implement Supply/Demand Metrics**

**Method:** `updateSupplyDemandMetrics(ctx, systemSymbol)`

**Logic:**
1. Query all markets
2. Count markets by supply level: `map[string]map[string]int` (good → supply → count)
3. Count markets by activity level: `map[string]map[string]int` (good → activity → count)
4. Update `supplyDistribution` gauges for each (good, level) pair
5. Update `activityDistribution` gauges for each (good, level) pair
6. Update `liquidity` gauges for each (waypoint, good) pair

**Step 2.5: Implement Trading Opportunities**

**Method:** `updateTradingOpportunities(ctx, systemSymbol)`

**Logic:**
1. Query all markets
2. Find best buy price per good: `MIN(sellPrice)`
3. Find best sell price per good: `MAX(purchasePrice)`
4. Update `bestPrice` gauges (type="buy" and type="sell")
5. Count opportunities for each margin threshold:
   ```go
   for minMargin in [10, 25, 50, 100]:
       count = 0
       for each good:
           profit = bestSellPrice - bestBuyPrice
           if profit >= minMargin:
               count++
       tradeOpportunities.Set(count)
   ```

**Testing:**
```bash
# Wait 60 seconds for first poll

# Check coverage metrics
curl http://localhost:9090/metrics | grep market_coverage
# Expected:
# spacetraders_daemon_market_coverage_total{player_id="1",system_symbol="X1-A1"} 5
# spacetraders_daemon_market_coverage_fresh{player_id="1",system_symbol="X1-A1",age_threshold="300s"} 4

# Check price metrics
curl http://localhost:9090/metrics | grep market_price_spread
# Should see histogram buckets

# Check supply distribution
curl http://localhost:9090/metrics | grep market_supply_distribution
# Expected: spacetraders_daemon_market_supply_distribution{player_id="1",good_symbol="IRON_ORE",supply_level="ABUNDANT"} 2
```

### Phase 3: Grafana Dashboard (1-2 hours)

**Step 3.1: Create Dashboard JSON**

Location: `configs/grafana/dashboards/market-dynamics.json`

**Dashboard Structure:**
```json
{
  "dashboard": {
    "title": "Market Dynamics",
    "tags": ["spacetraders", "market", "trading"],
    "timezone": "browser",
    "panels": [
      // Row 1: Market Coverage (panels 1-4)
      // Row 2: Price Dynamics (panels 5-7)
      // Row 3: Supply & Demand (panels 8-10)
      // Row 4: Scanner Performance (panels 11-14)
      // Row 5: Trading Insights (panels 15-17)
    ]
  }
}
```

**Step 3.2: Configure Panels**

See [Grafana Dashboard Design](#grafana-dashboard-design) section for detailed panel configurations.

**Step 3.3: Auto-Provision Dashboard**

**File:** `configs/grafana/provisioning/dashboards/dashboards.yml` (UPDATE)

```yaml
apiVersion: 1

providers:
  - name: 'SpaceTraders'
    orgId: 1
    folder: 'SpaceTraders Bot'
    type: file
    disableDeletion: false
    updateIntervalSeconds: 10
    allowUiUpdates: true
    options:
      path: /var/lib/grafana/dashboards
      foldersFromFilesStructure: false
```

**Step 3.4: Test Dashboard**

```bash
# Ensure Grafana is running
docker-compose -f docker-compose.metrics.yml up -d grafana

# Navigate to dashboard
open http://localhost:3000/d/market-dynamics

# Verify all panels load data
# Check for errors in browser console
```

---

## Grafana Dashboard Design

### Dashboard Overview

**Title:** Market Dynamics
**UID:** `market-dynamics`
**Refresh:** 30s (configurable)
**Time Range:** Last 6 hours (default)

### Row 1: Market Coverage

#### Panel 1.1: Total Markets Scanned (Stat)

**Metric:** Total number of markets discovered

**Query:**
```promql
sum(spacetraders_daemon_market_coverage_total) by (player_id)
```

**Visualization:** Stat
**Thresholds:**
- Green: >= 1
- Yellow: 0

**Settings:**
- Unit: `short`
- Decimals: 0

#### Panel 1.2: Fresh Markets (Stat)

**Metric:** Markets with data < 5 minutes old

**Query:**
```promql
sum(spacetraders_daemon_market_coverage_fresh{age_threshold="300s"}) by (player_id)
```

**Visualization:** Stat
**Thresholds:**
- Green: >= 80% of total
- Yellow: >= 50%
- Red: < 50%

**Settings:**
- Unit: `short`
- Decimals: 0

#### Panel 1.3: Coverage Percentage (Gauge)

**Metric:** Percentage of markets with fresh data

**Query:**
```promql
(sum(spacetraders_daemon_market_coverage_fresh{age_threshold="300s"}) /
 sum(spacetraders_daemon_market_coverage_total)) * 100
```

**Visualization:** Gauge
**Thresholds:**
- Red: < 50
- Yellow: < 80
- Green: >= 80

**Settings:**
- Unit: `percent`
- Min: 0
- Max: 100

#### Panel 1.4: Market Scans per Minute (Time Series)

**Metric:** Scan rate over time

**Query:**
```promql
sum(rate(spacetraders_daemon_market_scans_total[5m])) by (system_symbol)
```

**Visualization:** Time Series (Graph)
**Legend:** `{{system_symbol}}`

**Settings:**
- Unit: `ops` (operations per second, displayed as per minute)
- Axes: Left Y-axis

### Row 2: Price Dynamics

#### Panel 2.1: Price Spread Distribution (Heatmap)

**Metric:** Distribution of price spreads across all goods

**Query:**
```promql
rate(spacetraders_daemon_market_price_spread_bucket[5m])
```

**Visualization:** Heatmap

**Settings:**
- X-Axis: Time
- Y-Axis: Buckets (10, 50, 100, 500, 1000, 5000, 10000)
- Color Scheme: Green-Yellow-Red

#### Panel 2.2: Top 10 Trading Opportunities (Table)

**Metric:** Best price spreads available

**Queries:**
```promql
# Good Symbol
label_replace(
  topk(10, spacetraders_daemon_market_best_spread),
  "rank", "$1", "good_symbol", "(.*)"
)

# Best Spread
topk(10, spacetraders_daemon_market_best_spread)

# Best Buy Location
label_replace(
  spacetraders_daemon_market_best_price{type="buy"},
  "buy_location", "$1", "system_symbol", "(.*)"
)

# Best Sell Location
label_replace(
  spacetraders_daemon_market_best_price{type="sell"},
  "sell_location", "$1", "system_symbol", "(.*)"
)
```

**Visualization:** Table

**Columns:**
1. Good Symbol
2. Best Spread (credits)
3. Buy Location (waypoint with lowest sellPrice)
4. Sell Location (waypoint with highest purchasePrice)

**Settings:**
- Sort by: Best Spread (descending)
- Limit: 10 rows

#### Panel 2.3: Average Spread by Good (Time Series)

**Metric:** Trend of average spreads per good over time

**Query:**
```promql
avg(rate(spacetraders_daemon_market_price_spread_sum[5m]) /
    rate(spacetraders_daemon_market_price_spread_count[5m])) by (good_symbol)
```

**Visualization:** Time Series (Graph)
**Legend:** `{{good_symbol}}`

**Settings:**
- Unit: `credits`
- Y-Axis: Credits

### Row 3: Supply & Demand

#### Panel 3.1: Supply Distribution (Pie Chart)

**Metric:** Markets by supply level (all goods aggregated)

**Query:**
```promql
sum(spacetraders_daemon_market_supply_distribution) by (supply_level)
```

**Visualization:** Pie Chart

**Legend:**
- SCARCE (red)
- LIMITED (orange)
- MODERATE (yellow)
- HIGH (light green)
- ABUNDANT (dark green)

#### Panel 3.2: Activity Distribution (Pie Chart)

**Metric:** Markets by activity level (all goods aggregated)

**Query:**
```promql
sum(spacetraders_daemon_market_activity_distribution) by (activity_level)
```

**Visualization:** Pie Chart

**Legend:**
- WEAK (gray)
- GROWING (yellow)
- STRONG (green)
- RESTRICTED (red)

#### Panel 3.3: Markets per Supply Level (Bar Chart)

**Metric:** Count of markets at each supply level per good

**Query:**
```promql
sum(spacetraders_daemon_market_supply_distribution) by (good_symbol, supply_level)
```

**Visualization:** Bar Chart (Horizontal)

**Settings:**
- Grouped by: good_symbol
- Stacked: Yes
- Color: By supply_level

### Row 4: Scanner Performance

#### Panel 4.1: Scan Success Rate (Stat)

**Metric:** Percentage of successful scans

**Query:**
```promql
(rate(spacetraders_daemon_market_scans_total{status="success"}[5m]) /
 rate(spacetraders_daemon_market_scans_total[5m])) * 100
```

**Visualization:** Stat

**Thresholds:**
- Red: < 90
- Yellow: < 95
- Green: >= 95

**Settings:**
- Unit: `percent`
- Decimals: 1

#### Panel 4.2: Scan Duration (Time Series)

**Metric:** P50, P95, P99 scan duration over time

**Queries:**
```promql
# P50
histogram_quantile(0.50,
  rate(spacetraders_daemon_market_scan_duration_seconds_bucket[5m])
)

# P95
histogram_quantile(0.95,
  rate(spacetraders_daemon_market_scan_duration_seconds_bucket[5m])
)

# P99
histogram_quantile(0.99,
  rate(spacetraders_daemon_market_scan_duration_seconds_bucket[5m])
)
```

**Visualization:** Time Series (Graph)

**Legend:**
- p50
- p95
- p99

**Settings:**
- Unit: `seconds`
- Y-Axis: Seconds

#### Panel 4.3: Scans by System (Time Series)

**Metric:** Scan rate per system

**Query:**
```promql
sum(rate(spacetraders_daemon_market_scans_total[5m])) by (system_symbol)
```

**Visualization:** Time Series (Stacked Area)

**Legend:** `{{system_symbol}}`

#### Panel 4.4: Recent Failed Scans (Table)

**Metric:** Last 10 failed scans

**Query:**
```promql
topk(10,
  spacetraders_daemon_market_scans_total{status="failure"}
)
```

**Visualization:** Table

**Columns:**
1. Timestamp
2. Waypoint Symbol
3. Error Type

**Settings:**
- Sort by: Timestamp (descending)
- Limit: 10

### Row 5: Trading Insights

#### Panel 5.1: Best Buy Markets (Table)

**Metric:** Cheapest markets for each good (top 10)

**Query:**
```promql
topk(10,
  spacetraders_daemon_market_best_price{type="buy"}
)
```

**Visualization:** Table

**Columns:**
1. Good Symbol
2. Best Buy Price
3. System Symbol

#### Panel 5.2: Best Sell Markets (Table)

**Metric:** Best markets for selling each good (top 10)

**Query:**
```promql
topk(10,
  spacetraders_daemon_market_best_price{type="sell"}
)
```

**Visualization:** Table

**Columns:**
1. Good Symbol
2. Best Sell Price
3. System Symbol

#### Panel 5.3: Profitable Routes Available (Stat)

**Metric:** Number of routes with profit > 100 credits

**Query:**
```promql
sum(spacetraders_daemon_trade_opportunities_total{min_margin="100"})
```

**Visualization:** Stat

**Thresholds:**
- Red: 0
- Yellow: < 5
- Green: >= 5

**Settings:**
- Unit: `short`
- Decimals: 0

---

## PromQL Query Reference

### Coverage Queries

```promql
# Total markets discovered
sum(spacetraders_daemon_market_coverage_total) by (player_id)

# Fresh markets (< 5 min)
sum(spacetraders_daemon_market_coverage_fresh{age_threshold="300s"}) by (player_id)

# Coverage percentage
(spacetraders_daemon_market_coverage_fresh / spacetraders_daemon_market_coverage_total) * 100

# Stale markets count
spacetraders_daemon_market_coverage_total - spacetraders_daemon_market_coverage_fresh{age_threshold="300s"}

# Average data age
avg(spacetraders_daemon_market_data_age_seconds) by (system_symbol)

# P95 data age
histogram_quantile(0.95, rate(spacetraders_daemon_market_data_age_seconds_bucket[5m]))
```

### Price Queries

```promql
# Top 10 trading opportunities (best spreads)
topk(10, spacetraders_daemon_market_best_spread)

# Average spread per good
avg(rate(spacetraders_daemon_market_price_spread_sum[5m]) /
    rate(spacetraders_daemon_market_price_spread_count[5m])) by (good_symbol)

# Spread distribution percentiles
histogram_quantile(0.50, rate(spacetraders_daemon_market_price_spread_bucket[5m]))  # P50
histogram_quantile(0.95, rate(spacetraders_daemon_market_price_spread_bucket[5m]))  # P95

# Market efficiency (average)
avg(spacetraders_daemon_market_efficiency_percent) by (good_symbol)

# Inefficient markets (> 50% spread)
count(spacetraders_daemon_market_efficiency_percent > 50)

# Best buy price for IRON_ORE
spacetraders_daemon_market_best_price{good_symbol="IRON_ORE", type="buy"}

# Best sell price for FUEL
spacetraders_daemon_market_best_price{good_symbol="FUEL", type="sell"}

# Profit potential (sell best - buy best)
spacetraders_daemon_market_best_price{type="sell"} -
spacetraders_daemon_market_best_price{type="buy"}
```

### Supply/Demand Queries

```promql
# Markets with SCARCE supply
sum(spacetraders_daemon_market_supply_distribution{supply_level="SCARCE"}) by (good_symbol)

# Markets with ABUNDANT supply
sum(spacetraders_daemon_market_supply_distribution{supply_level="ABUNDANT"}) by (good_symbol)

# Supply distribution for IRON_ORE
spacetraders_daemon_market_supply_distribution{good_symbol="IRON_ORE"}

# High-activity markets (STRONG)
sum(spacetraders_daemon_market_activity_distribution{activity_level="STRONG"})

# RESTRICTED markets (avoid)
sum(spacetraders_daemon_market_activity_distribution{activity_level="RESTRICTED"}) by (good_symbol)

# Activity distribution for FUEL
spacetraders_daemon_market_activity_distribution{good_symbol="FUEL"}

# Average liquidity per good
avg(spacetraders_daemon_market_liquidity) by (good_symbol)

# High-liquidity markets (> 1000 units)
count(spacetraders_daemon_market_liquidity > 1000) by (good_symbol)

# Total liquidity in system
sum(spacetraders_daemon_market_liquidity) by (system_symbol)
```

### Scanner Performance Queries

```promql
# Scans per minute
rate(spacetraders_daemon_market_scans_total[1m])

# Scan success rate
(rate(spacetraders_daemon_market_scans_total{status="success"}[5m]) /
 rate(spacetraders_daemon_market_scans_total[5m])) * 100

# Failed scans (last hour)
increase(spacetraders_daemon_market_scans_total{status="failure"}[1h])

# Scan error rate
rate(spacetraders_daemon_market_scanner_errors_total[5m])

# Error rate percentage
(rate(spacetraders_daemon_market_scanner_errors_total[5m]) /
 rate(spacetraders_daemon_market_scans_total[5m])) * 100

# P50 scan duration
histogram_quantile(0.50, rate(spacetraders_daemon_market_scan_duration_seconds_bucket[5m]))

# P95 scan duration
histogram_quantile(0.95, rate(spacetraders_daemon_market_scan_duration_seconds_bucket[5m]))

# P99 scan duration
histogram_quantile(0.99, rate(spacetraders_daemon_market_scan_duration_seconds_bucket[5m]))

# Average scan duration per waypoint
avg(rate(spacetraders_daemon_market_scan_duration_seconds_sum[5m])) by (waypoint_symbol)

# Slow scans (> 5 seconds)
count(spacetraders_daemon_market_scan_duration_seconds_bucket{le="5"} -
      spacetraders_daemon_market_scan_duration_seconds_bucket{le="2"})

# Scans per system
sum(rate(spacetraders_daemon_market_scans_total[5m])) by (system_symbol)

# Scan rate gauge
spacetraders_daemon_market_scan_rate
```

### Trading Opportunity Queries

```promql
# Profitable routes (> 100 credit margin)
spacetraders_daemon_trade_opportunities_total{min_margin="100"}

# High-margin opportunities (> 500)
spacetraders_daemon_trade_opportunities_total{min_margin="500"}

# Ultra-high-margin opportunities (> 1000)
spacetraders_daemon_trade_opportunities_total{min_margin="1000"}

# Opportunity trend
rate(spacetraders_daemon_trade_opportunities_total{min_margin="100"}[5m])

# Total opportunities across all systems
sum(spacetraders_daemon_trade_opportunities_total{min_margin="100"})
```

---

## Testing Strategy

### BDD Test Feature

**File:** `test/bdd/features/adapters/metrics/market_metrics.feature` (NEW)

```gherkin
Feature: Market Metrics Collection
  As a bot operator
  I want to track market dynamics metrics
  So that I can monitor trading opportunities and scanner performance

  Background:
    Given metrics are enabled
    And the daemon is running
    And the Prometheus registry is initialized
    And the market metrics collector is registered

  Scenario: Track market scan success
    Given a market scanner is configured
    When I scan market "X1-A1-STATION"
    And the scan succeeds
    Then the metric "spacetraders_daemon_market_scans_total" should increment
    And the labels should be "player_id=1,waypoint_symbol=X1-A1-STATION,status=success"

  Scenario: Track market scan failure
    Given a market scanner is configured
    When I scan market "X1-A1-INVALID"
    And the scan fails with an API error
    Then the metric "spacetraders_daemon_market_scans_total" should increment
    And the labels should be "player_id=1,waypoint_symbol=X1-A1-INVALID,status=failure"
    And the metric "spacetraders_daemon_market_scanner_errors_total" should increment
    And the error type label should be "api_error"

  Scenario: Track scan duration
    Given a market scanner is configured
    When I scan market "X1-A1-STATION"
    And the scan takes 2.5 seconds
    Then the metric "spacetraders_daemon_market_scan_duration_seconds" should observe 2.5

  Scenario: Calculate market coverage
    Given 5 markets exist in system "X1-A1"
    And 3 markets have been scanned in the last 5 minutes
    And 2 markets have been scanned more than 5 minutes ago
    When the metrics collector polls
    Then the metric "spacetraders_daemon_market_coverage_total" should be 5
    And the metric "spacetraders_daemon_market_coverage_fresh" with age_threshold=300s should be 3

  Scenario: Calculate price spreads
    Given market "X1-A1-STATION" has good "IRON_ORE" with sellPrice=150 and purchasePrice=100
    When the metrics collector polls
    Then the metric "spacetraders_daemon_market_price_spread" should observe 50 for good "IRON_ORE"
    And the metric "spacetraders_daemon_market_efficiency_percent" should observe 33.33 for good "IRON_ORE"

  Scenario: Track supply distribution
    Given system "X1-A1" has 3 markets selling "IRON_ORE"
    And 1 market has supply "SCARCE"
    And 1 market has supply "MODERATE"
    And 1 market has supply "ABUNDANT"
    When the metrics collector polls
    Then the metric "spacetraders_daemon_market_supply_distribution" should be:
      | good_symbol | supply_level | value |
      | IRON_ORE    | SCARCE       | 1     |
      | IRON_ORE    | MODERATE     | 1     |
      | IRON_ORE    | ABUNDANT     | 1     |

  Scenario: Track activity distribution
    Given system "X1-A1" has 2 markets selling "FUEL"
    And 1 market has activity "WEAK"
    And 1 market has activity "STRONG"
    When the metrics collector polls
    Then the metric "spacetraders_daemon_market_activity_distribution" should be:
      | good_symbol | activity_level | value |
      | FUEL        | WEAK           | 1     |
      | FUEL        | STRONG         | 1     |

  Scenario: Identify trading opportunities
    Given market "X1-A1-STATION" sells "IRON_ORE" at 150 credits (sellPrice)
    And market "X1-A1-MARKET" buys "IRON_ORE" at 200 credits (purchasePrice)
    When the metrics collector polls
    Then the metric "spacetraders_daemon_trade_opportunities_total" with min_margin=10 should be >= 1
    And the metric "spacetraders_daemon_market_best_price" for "IRON_ORE" type "buy" should be 150
    And the metric "spacetraders_daemon_market_best_price" for "IRON_ORE" type "sell" should be 200
    And the metric "spacetraders_daemon_market_best_spread" for "IRON_ORE" should be 50

  Scenario: Track market data age
    Given market "X1-A1-STATION" was last updated 120 seconds ago
    When the metrics collector polls
    Then the metric "spacetraders_daemon_market_data_age_seconds" should observe 120
```

### Step Definitions

**File:** `test/bdd/steps/market_metrics_steps.go` (NEW)

```go
package steps

import (
    "context"
    "time"
    "github.com/cucumber/godog"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/testutil"
)

type MarketMetricsContext struct {
    metricsCollector *metrics.MarketMetricsCollector
    marketScanner    *ship.MarketScanner
    marketRepo       *persistence.MarketRepositoryGORM
    testDB           *gorm.DB
}

func InitializeMarketMetricsScenario(sc *godog.ScenarioContext) {
    ctx := &MarketMetricsContext{}

    sc.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
        // Initialize test database
        // Initialize mock repositories
        // Initialize metrics collector
        return ctx, nil
    })

    sc.After(func(ctx context.Context, sc *godog.Scenario, err error) (context.Context, error) {
        // Clean up test database
        // Reset metrics
        return ctx, nil
    })

    sc.Step(`^a market scanner is configured$`, ctx.aMarketScannerIsConfigured)
    sc.Step(`^I scan market "([^"]*)"$`, ctx.iScanMarket)
    sc.Step(`^the scan succeeds$`, ctx.theScanSucceeds)
    sc.Step(`^the scan fails with an API error$`, ctx.theScanFailsWithAnAPIError)
    sc.Step(`^the scan takes ([\d.]+) seconds$`, ctx.theScanTakesSeconds)
    sc.Step(`^the metric "([^"]*)" should increment$`, ctx.theMetricShouldIncrement)
    sc.Step(`^the metric "([^"]*)" should observe ([\d.]+)$`, ctx.theMetricShouldObserve)
    // ... more step definitions
}

func (c *MarketMetricsContext) aMarketScannerIsConfigured() error {
    c.marketScanner = ship.NewMarketScanner(mockAPIClient, c.marketRepo)
    return nil
}

func (c *MarketMetricsContext) iScanMarket(waypointSymbol string) error {
    err := c.marketScanner.ScanAndSaveMarket(context.Background(), 1, waypointSymbol)
    c.lastError = err
    return nil
}

func (c *MarketMetricsContext) theMetricShouldIncrement(metricName string) error {
    // Use prometheus testutil to verify counter incremented
    count := testutil.ToFloat64(c.metricsCollector.scansTotal)
    if count == 0 {
        return fmt.Errorf("metric %s did not increment", metricName)
    }
    return nil
}

// ... implement remaining step definitions
```

### Integration Test Checklist

**Phase 1: Scanner Instrumentation**
- [ ] Scan success increments counter
- [ ] Scan failure increments counter
- [ ] Scan duration histogram populated
- [ ] Error types classified correctly
- [ ] Metrics recording doesn't block scanner

**Phase 2: Polling Metrics**
- [ ] Coverage metrics update every 60s
- [ ] Fresh market count accurate (< 5 min threshold)
- [ ] Data age histogram populated
- [ ] Price spread calculated correctly
- [ ] Supply/demand distribution accurate
- [ ] Trading opportunities count correct
- [ ] Best price tracking works

**Phase 3: Grafana Dashboard**
- [ ] All panels display data
- [ ] Queries return expected results
- [ ] Refresh intervals working
- [ ] Time ranges adjustable
- [ ] No query errors in logs

**Phase 4: Performance**
- [ ] Polling doesn't spike CPU
- [ ] Memory usage stable
- [ ] Database queries performant (< 100ms)
- [ ] Cardinality under 50,000 time series

---

## Performance Considerations

### Database Query Optimization

**Challenge:** Polling `ListMarketsInSystem()` every 60s for all systems

**Optimizations:**

1. **Index Usage:**
   ```sql
   CREATE INDEX idx_market_data_player_system ON market_data(player_id, waypoint_symbol);
   CREATE INDEX idx_market_data_last_updated ON market_data(last_updated);
   ```

2. **Query Caching:**
   - Cache query results for 30s in memory
   - Invalidate on new scans
   - Reduces DB load by 50%

3. **Selective System Polling:**
   - Configure `ST_METRICS_SYSTEM_SYMBOLS` to monitor specific systems
   - Avoid full-database scans

4. **Connection Pooling:**
   - GORM manages connection pool automatically
   - Monitor `db_connections_open` metric

**Expected Query Time:** < 50ms for 100 markets

### Metrics Cardinality Analysis

**Cardinality Calculation:**

| Metric | Labels | Unique Combinations | Time Series |
|--------|--------|---------------------|-------------|
| scansTotal | player_id (100) × waypoint_symbol (1000) × status (2) | 200,000 | **HIGH** |
| scanDuration | player_id (100) × waypoint_symbol (1000) × buckets (7) | 700,000 | **VERY HIGH** |
| coverageTotal | player_id (100) × system_symbol (10) | 1,000 | Low |
| priceSpread | player_id (100) × good_symbol (50) × buckets (8) | 40,000 | Medium |
| supplyDistribution | player_id (100) × good_symbol (50) × supply_level (5) | 25,000 | Medium |
| tradeOpportunities | player_id (100) × system_symbol (10) × min_margin (4) | 4,000 | Low |

**Total Estimated Cardinality:** ~970,000 time series

**PROBLEM:** `scansTotal` and `scanDuration` have high cardinality due to `waypoint_symbol` label

**Mitigation Strategies:**

**Option 1: Remove waypoint_symbol from scansTotal/scanDuration** (RECOMMENDED)
```go
// Instead of:
scansTotal.WithLabelValues(playerID, waypointSymbol, status).Inc()

// Use:
scansTotal.WithLabelValues(playerID, systemSymbol, status).Inc()  // System-level aggregation
```

**Result:** Cardinality drops from 200,000 → 2,000 (100x reduction)

**Option 2: Use sampling for high-cardinality metrics**
```go
// Only record scan metrics for 10% of waypoints
if rand.Float64() < 0.1 {
    scansTotal.WithLabelValues(playerID, waypointSymbol, status).Inc()
}
```

**Option 3: Set metric retention/TTL**
- Configure Prometheus to drop old time series
- `--storage.tsdb.retention.time=7d`

**Recommended Approach:** Use Option 1 (system-level aggregation)

**Revised Cardinality:** ~25,000 time series (within safe limits)

### Memory Usage

**Prometheus Memory Formula:**
```
Memory = Time Series × Samples per Series × 16 bytes
```

**Calculation (7-day retention):**
```
Time Series: 25,000
Samples: (7 days × 24 hours × 60 min × 4 samples/min) = 40,320 samples
Memory: 25,000 × 40,320 × 16 bytes = ~15 GB
```

**Optimizations:**
- Reduce scrape interval: 15s → 30s (halves sample count)
- Reduce retention: 7d → 3d (reduces by 57%)
- Use recording rules for aggregates

**Recommended Configuration:**
```yaml
# Prometheus config
global:
  scrape_interval: 30s        # Reduced from 15s
  retention: 3d               # Reduced from 7d
```

**Expected Memory:** ~4 GB (manageable on 8GB VMs)

### Polling Interval Tuning

**Current:** 60s poll interval

**Trade-offs:**

| Interval | Freshness | DB Load | API Load |
|----------|-----------|---------|----------|
| 30s | High | High | Same |
| 60s | Medium | Medium | Same |
| 120s | Low | Low | Same |

**Recommendation:** 60s (good balance)

**Adaptive Polling (Future Enhancement):**
```go
// Poll more frequently during active trading hours
func (c *MarketMetricsCollector) getAdaptivePollInterval() time.Duration {
    hour := time.Now().Hour()
    if hour >= 8 && hour <= 22 {  // Active hours
        return 30 * time.Second
    }
    return 120 * time.Second  // Off-peak
}
```

### CPU Usage

**Expected Overhead:**
- Polling goroutine: < 0.5% CPU
- Metric recording: < 0.1% CPU per scan
- Total: < 1% CPU overhead

**Profiling Commands:**
```bash
# Profile CPU for 30 seconds
go tool pprof http://localhost:9090/debug/pprof/profile?seconds=30

# Profile memory
go tool pprof http://localhost:9090/debug/pprof/heap

# Trace execution
curl http://localhost:9090/debug/pprof/trace?seconds=5 > trace.out
go tool trace trace.out
```

---

## Implementation Phases

### Phase 1: Core Collector (2 hours)

**Deliverables:**
1. `internal/adapters/metrics/market_metrics.go` created
2. All 15 metrics defined with proper labels/buckets
3. `MarketMetricsCollector` implements Prometheus Collector interface
4. Global collector registered in `prometheus_collector.go`
5. Configuration added to `config.go`
6. MarketScanner instrumented with metrics recording

**Files Created/Modified:**
- `internal/adapters/metrics/market_metrics.go` (NEW)
- `internal/adapters/metrics/prometheus_collector.go` (UPDATE)
- `internal/infrastructure/config/config.go` (UPDATE)
- `internal/application/ship/market_scanner.go` (UPDATE)

**Testing:**
```bash
# Start daemon
ST_METRICS_ENABLED=true ./bin/spacetraders-daemon

# Trigger scan
./bin/spacetraders scout tour --ship SCOUT-1 --waypoints X1-A1-STATION

# Verify metrics
curl http://localhost:9090/metrics | grep market_scans_total
# Expected: spacetraders_daemon_market_scans_total{...} 1
```

### Phase 2: Polling Metrics (2 hours)

**Deliverables:**
1. `Start()` goroutine implemented with ticker
2. `updateCoverageMetrics()` implemented
3. `updatePriceMetrics()` implemented
4. `updateSupplyDemandMetrics()` implemented
5. `updateTradingOpportunities()` implemented
6. All polling metrics exposed and updating

**Testing:**
```bash
# Wait 60s for first poll

# Check coverage
curl http://localhost:9090/metrics | grep market_coverage_total
# Expected: spacetraders_daemon_market_coverage_total{...} 5

# Check spreads
curl http://localhost:9090/metrics | grep market_price_spread
# Should see histogram buckets

# Check supply distribution
curl http://localhost:9090/metrics | grep market_supply_distribution
# Expected: ...{supply_level="ABUNDANT"} 2
```

### Phase 3: Grafana Dashboard (1-2 hours)

**Deliverables:**
1. `configs/grafana/dashboards/market-dynamics.json` created
2. 17+ panels configured (coverage, prices, supply, scanner, trading)
3. Auto-provisioning configured
4. Dashboard accessible in Grafana

**Testing:**
```bash
# Ensure Grafana running
docker-compose -f docker-compose.metrics.yml up -d grafana

# Open dashboard
open http://localhost:3000/d/market-dynamics

# Verify all panels load
# Check for query errors in browser console
```

### Phase 4: Testing & Documentation (1 hour)

**Deliverables:**
1. `test/bdd/features/adapters/metrics/market_metrics.feature` created
2. Step definitions implemented in `test/bdd/steps/market_metrics_steps.go`
3. `docs/METRICS_IMPLEMENTATION_PLAN.md` updated with market section
4. All BDD tests passing

**Testing:**
```bash
# Run BDD tests
go test ./test/bdd/... -v -godog.paths=test/bdd/features/adapters/metrics/market_metrics.feature

# Should see all scenarios passing
```

---

## Appendices

### Appendix A: Complete Metric Reference

**Scanner Performance (4 metrics):**
1. `spacetraders_daemon_market_scans_total` - Counter - Scan attempts
2. `spacetraders_daemon_market_scan_duration_seconds` - Histogram - Scan latency
3. `spacetraders_daemon_market_scan_rate` - Gauge - Scans per minute
4. `spacetraders_daemon_market_scanner_errors_total` - Counter - Error count

**Coverage (3 metrics):**
5. `spacetraders_daemon_market_coverage_total` - Gauge - Total markets
6. `spacetraders_daemon_market_coverage_fresh` - Gauge - Fresh markets
7. `spacetraders_daemon_market_data_age_seconds` - Histogram - Data staleness

**Price Dynamics (3 metrics):**
8. `spacetraders_daemon_market_price_spread` - Histogram - Price spreads
9. `spacetraders_daemon_market_best_spread` - Gauge - Best spread per good
10. `spacetraders_daemon_market_efficiency_percent` - Histogram - Spread as % of price

**Supply/Demand (3 metrics):**
11. `spacetraders_daemon_market_supply_distribution` - Gauge - Markets by supply
12. `spacetraders_daemon_market_activity_distribution` - Gauge - Markets by activity
13. `spacetraders_daemon_market_liquidity` - Gauge - Trade volume limits

**Trading Opportunities (2 metrics):**
14. `spacetraders_daemon_trade_opportunities_total` - Gauge - Profitable routes
15. `spacetraders_daemon_market_best_price` - Gauge - Best buy/sell prices

### Appendix B: Grafana Panel JSON Snippets

**Coverage Percentage Panel:**
```json
{
  "id": 3,
  "type": "gauge",
  "title": "Market Coverage %",
  "targets": [
    {
      "expr": "(sum(spacetraders_daemon_market_coverage_fresh{age_threshold=\"300s\"}) / sum(spacetraders_daemon_market_coverage_total)) * 100",
      "refId": "A"
    }
  ],
  "fieldConfig": {
    "defaults": {
      "unit": "percent",
      "min": 0,
      "max": 100,
      "thresholds": {
        "mode": "absolute",
        "steps": [
          {"color": "red", "value": 0},
          {"color": "yellow", "value": 50},
          {"color": "green", "value": 80}
        ]
      }
    }
  }
}
```

**Top Trading Opportunities Table:**
```json
{
  "id": 6,
  "type": "table",
  "title": "Top 10 Trading Opportunities",
  "targets": [
    {
      "expr": "topk(10, spacetraders_daemon_market_best_spread)",
      "format": "table",
      "refId": "A"
    }
  ],
  "transformations": [
    {
      "id": "organize",
      "options": {
        "excludeByName": {},
        "indexByName": {},
        "renameByName": {
          "good_symbol": "Good",
          "Value": "Spread (credits)",
          "system_symbol": "System"
        }
      }
    }
  ]
}
```

### Appendix C: Environment Variables

```bash
# Market Metrics Configuration
ST_METRICS_ENABLED=true
ST_METRICS_MARKET_POLL_INTERVAL=60s
ST_METRICS_MARKET_FRESH_THRESHOLD=300s
ST_METRICS_SYSTEM_SYMBOLS=X1-A1,X1-B2  # Optional: specific systems to monitor
```

### Appendix D: Troubleshooting

**Problem:** Metrics not updating

**Solution:**
```bash
# Check if metrics enabled
grep ST_METRICS_ENABLED .env

# Check collector started
tail -f daemon.log | grep "Starting market metrics collector"

# Check for errors
tail -f daemon.log | grep "market metrics"
```

**Problem:** High cardinality warning

**Solution:**
```bash
# Check cardinality
curl http://localhost:9090/api/v1/label/__name__/values | jq '.data | length'

# If > 50,000, reduce waypoint_symbol usage
# Use system-level aggregation instead
```

**Problem:** Slow polling queries

**Solution:**
```sql
-- Add indexes
CREATE INDEX idx_market_data_composite ON market_data(player_id, waypoint_symbol, last_updated);

-- Analyze query plan
EXPLAIN ANALYZE SELECT * FROM market_data WHERE player_id = 1 AND last_updated > NOW() - INTERVAL '5 minutes';
```

### Appendix E: Future Enhancements

**Historical Price Tracking (Phase 2):**

**New Table:**
```sql
CREATE TABLE market_price_history (
    id SERIAL PRIMARY KEY,
    waypoint_symbol VARCHAR(255) NOT NULL,
    good_symbol VARCHAR(100) NOT NULL,
    sell_price INT NOT NULL,
    purchase_price INT NOT NULL,
    timestamp TIMESTAMP NOT NULL,
    player_id INT NOT NULL,
    INDEX idx_timestamp (timestamp),
    INDEX idx_waypoint_good (waypoint_symbol, good_symbol)
);
```

**New Metrics:**
```go
// Price change rate
market_price_change_rate{player_id, good_symbol, window}

// Price volatility index
market_volatility_index{player_id, good_symbol}
```

**Implementation Effort:** 4-6 hours (schema migration + metrics + dashboard)

---

## Summary

This implementation plan provides a comprehensive blueprint for adding market dynamics metrics to the SpaceTraders Go bot. The design:

1. **Leverages Existing Infrastructure** - Builds on established metrics system
2. **Tracks 15 Key Metrics** - Coverage, prices, supply/demand, scanner performance, opportunities
3. **Maintains Performance** - < 1% overhead, optimized cardinality (~25k time series)
4. **Provides Actionable Insights** - Grafana dashboard with 17+ panels
5. **Follows Best Practices** - BDD testing, non-blocking instrumentation, graceful degradation
6. **Enables Trading Optimization** - Real-time opportunity identification, best price tracking

**Estimated Effort:** 6-7 hours total, spread across 4 phases

**Next Steps:** Review plan → Get approval → Begin Phase 1 implementation

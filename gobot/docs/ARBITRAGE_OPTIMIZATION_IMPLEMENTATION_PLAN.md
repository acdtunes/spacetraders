# Arbitrage Optimization Implementation Plan

**Version:** 1.1
**Last Updated:** 2025-11-23
**Status:** Planning Phase
**Additions:** Cargo Quantity Optimization (Phase 6)

## Table of Contents

1. [Overview](#overview)
2. [Problem Statement](#problem-statement)
3. [Optimization Approaches](#optimization-approaches)
4. [Cargo Quantity Optimization](#cargo-quantity-optimization)
5. [Recommended Strategy](#recommended-strategy)
6. [Phase 1: Data Collection Infrastructure](#phase-1-data-collection-infrastructure)
7. [Phase 2: Genetic Algorithm Optimization](#phase-2-genetic-algorithm-optimization)
8. [Phase 3: Machine Learning Model](#phase-3-machine-learning-model)
9. [Phase 4: Integration & A/B Testing](#phase-4-integration--ab-testing)
10. [Phase 5: Advanced Optimization](#phase-5-advanced-optimization)
11. [Phase 6: Cargo Quantity Optimization](#phase-6-cargo-quantity-optimization)
12. [Architecture](#architecture)
13. [Data Models](#data-models)
14. [Success Criteria](#success-criteria)
15. [Risk Analysis](#risk-analysis)
16. [Monitoring & Observability](#monitoring--observability)

---

## Overview

This document outlines a comprehensive plan to optimize the arbitrage trading operation's opportunity scoring mechanism. The current system uses a manually-tuned weighted formula to rank arbitrage opportunities. This plan introduces data-driven optimization techniques to maximize profit per unit time.

### Current State

**Existing Scoring Formula**:
```
score = (profitMargin × 40.0) + (supplyScore × 20.0) + (activityScore × 20.0) - (distance × 0.1)
```

**Weights**:
- Profit margin: 40% (primary driver)
- Supply availability: 20% (risk mitigation)
- Market activity: 20% (demand stability)
- Distance penalty: 0.1 (fuel efficiency tiebreaker)

**Limitations**:
- Weights chosen manually based on intuition
- No validation against actual profit outcomes
- Linear combination may miss non-linear patterns
- Cannot discover new profitable features
- No adaptation to market changes

### Goals

1. **Maximize Objective Function**: Profit per unit time
2. **Data-Driven Optimization**: Use historical execution results
3. **Continuous Improvement**: Adapt to changing market dynamics
4. **Maintain Reliability**: Graceful fallbacks and robust deployment
5. **Preserve Explainability**: Understand what drives profitability

---

## Problem Statement

### Optimization Objective

**Maximize**: `E[profit / duration]` = Expected profit per second

Where:
- `profit` = Net profit (revenue - costs - fuel)
- `duration` = Time from worker start to completion (seconds)

**Constraints**:
- Must rank opportunities in real-time (<100ms latency)
- Must handle 20-100 opportunities per scan
- Must work with incomplete market data
- Must be maintainable and debuggable

### Why Optimize?

**Potential Impact**:
- Current profit/time: ~500 credits/minute (baseline)
- 10% improvement: +50 credits/minute per ship
- 30% improvement: +150 credits/minute per ship
- With 10 ships: +1500 credits/minute fleet-wide

**Compounding Effect**:
- Better opportunity selection → Higher profits → More ships → More parallel operations → Exponential growth

### Key Questions

1. Are current weights optimal?
2. Can we discover hidden profitable patterns?
3. Do non-linear relationships exist (e.g., diminishing returns at high margins)?
4. How much data do we need to find better strategies?

---

## Optimization Approaches

### Approach A: Genetic Algorithm

**Concept**: Evolve scoring formula weights through simulated natural selection.

**Mechanism**:
- **Genome**: Array of weights `[profitWeight, supplyWeight, activityWeight, distancePenalty, ...]`
- **Fitness**: Average profit/time achieved using those weights on historical data
- **Evolution**: Selection, crossover, mutation over multiple generations

**Pros**:
- ✅ Simple integration (just updates weights)
- ✅ No ML infrastructure needed (pure Go)
- ✅ Interpretable results (weights remain meaningful)
- ✅ Fast evolution (~10 minutes for 100 generations)
- ✅ Works with small datasets (100-200 examples minimum)
- ✅ Low deployment risk (config file update)
- ✅ Explainable (can trace why weights changed)

**Cons**:
- ❌ Limited expressiveness (locked into linear formula)
- ❌ Cannot discover new features
- ❌ Cannot capture non-linear relationships
- ❌ May converge to local optima
- ❌ Stochastic (different runs may vary)
- ❌ Modest expected improvement (10-15%)

### Approach B: Machine Learning

**Concept**: Train supervised model to predict actual profit/time from opportunity features.

**Mechanism**:
- **Features**: 25+ attributes (profit, supply, activity, distance, ship state, volatility, etc.)
- **Target**: Actual profit/time from historical executions
- **Model**: XGBoost/LightGBM regression
- **Prediction**: Score opportunities by predicted profit/time

**Pros**:
- ✅ High expressiveness (learns non-linear patterns)
- ✅ Feature discovery (identifies unexpected profitable signals)
- ✅ Adapts to market changes (via retraining)
- ✅ Proven in production trading systems
- ✅ Higher expected improvement (20-30%)
- ✅ Handles complex feature interactions

**Cons**:
- ❌ Requires ML infrastructure (Python service)
- ❌ Needs more data (1000+ examples minimum)
- ❌ Adds latency (HTTP call + inference: ~10-50ms)
- ❌ Black box (harder to debug)
- ❌ Maintenance overhead (retraining, versioning)
- ❌ Overfitting risk (spurious correlations)
- ❌ Concept drift (model degrades if markets change)

### Comparison Matrix

| Dimension | Genetic Algorithm | Machine Learning |
|-----------|------------------|------------------|
| **Data Requirements** | 100-200 examples | 1000-2000 examples |
| **Training Time** | ~10 minutes | ~5-10 minutes |
| **Infrastructure** | None (pure Go) | Python ML service |
| **Runtime Latency** | 0ms (offline) | +10-50ms (HTTP + inference) |
| **Interpretability** | High (transparent weights) | Low (black box) |
| **Expressiveness** | Low (linear only) | High (non-linear) |
| **Feature Discovery** | No | Yes |
| **Maintenance** | Low (re-evolve periodically) | Medium (retrain monthly) |
| **Expected Lift** | 10-15% | 20-30% |
| **Risk Level** | Low | Medium |
| **Development Effort** | Low | Medium-High |
| **Integration Complexity** | Simple (config file) | Complex (microservice) |

---

## Cargo Quantity Optimization

### The Quantity Decision Problem

The current arbitrage system always **fills ships to maximum cargo capacity**, assuming linear profit scaling. However, optimal quantity may differ from maximum capacity due to several market factors.

**Current Behavior**:
```go
// Always buy maximum possible
Units: ship.Cargo().AvailableCapacity()  // e.g., 40 units
```

**Question**: Is buying 40 units always better than buying 25 units?

### Why Optimal Quantity ≠ Maximum Capacity

#### 1. Market Depth Constraints

**Problem**: Limited supply at buy markets

```
Scenario A: ABUNDANT supply
- Available: 500 units
- Ship capacity: 40 units
- Buy 40 units: ✓ No issues

Scenario B: LIMITED supply
- Available: 15 units
- Ship capacity: 40 units
- Try to buy 40: ✗ API error or partial fill
```

**SpaceTraders API**: Requesting more than available may fail or trigger price increases

#### 2. Price Slippage

**Problem**: Large purchases move market prices

```
Dynamic Pricing in SpaceTraders:
- Buy 10 units:  avg price = 102 credits/unit (+2% slippage)
- Buy 40 units:  avg price = 110 credits/unit (+10% slippage)

Profit margin erodes as quantity increases!
```

#### 3. Demand Saturation

**Problem**: Sell market may not want unlimited quantity

```
Sell Market with WEAK activity:
- Estimated demand: ~20 units
- Ship brings: 40 units
- First 20 units: 130 credits/unit
- Next 20 units: 120 credits/unit (price drops)
- Average: 125 credits/unit vs expected 130
```

#### 4. Capital Efficiency (Fleet-Wide)

**Problem**: Limited capital with multiple ships

```
Scenario: 10,000 credits available, 3 idle ships

Strategy A: Fill all ships to capacity (40 units each)
- Ship 1: 4,000 credits → 1,200 profit
- Ship 2: 4,000 credits → 1,200 profit
- Ship 3: 2,000 credits → Can only buy 20 units → 600 profit
- Total profit: 3,000 credits
- Underutilized: Ship 3 only half full

Strategy B: Buy 25 units per ship
- Ship 1: 2,500 credits → 750 profit
- Ship 2: 2,500 credits → 750 profit
- Ship 3: 2,500 credits → 750 profit
- Ship 4: 2,500 credits → 750 profit (now we can run a 4th ship!)
- Total profit: 3,000 credits
- Better: All ships fully utilized

Fleet-wide efficiency > per-ship optimization
```

#### 5. Risk Diversification

**Problem**: All eggs in one basket

- 1 ship × 40 units: If failure, lose entire profit
- 2 ships × 20 units: Diversified risk, higher throughput

### Optimization Approaches

#### Approach 1: Rule-Based Heuristics

**Concept**: Apply supply/demand adjustments to maximum capacity

```go
func CalculateOptimalQuantity(
    opportunity *ArbitrageOpportunity,
    ship *Ship,
    buyMarketData *MarketData,
    sellMarketData *MarketData,
    playerCredits int,
) int {
    maxCapacity := ship.Cargo().AvailableCapacity()

    // Constraint 1: Supply availability
    availableSupply := buyMarketData.GetTradeVolume(opportunity.Good())
    maxBySupply := min(maxCapacity, availableSupply)

    // Constraint 2: Demand estimate
    estimatedDemand := estimateDemand(sellMarketData, opportunity.Good())
    maxByDemand := min(maxBySupply, estimatedDemand)

    // Constraint 3: Capital limit
    buyPrice := opportunity.BuyPrice()
    maxByCapital := min(maxByDemand, playerCredits / buyPrice)

    // Adjustment 1: Supply level factor
    supplyFactor := getSupplyFactor(opportunity.BuySupply())
    // ABUNDANT: 1.0, HIGH: 0.8, MODERATE: 0.6, LIMITED: 0.4, SCARCE: 0.2

    adjustedQty := int(float64(maxByCapital) * supplyFactor)

    // Adjustment 2: Activity level factor
    activityFactor := getActivityFactor(opportunity.SellActivity())
    // STRONG: 1.0, GROWING: 0.8, WEAK: 0.6, RESTRICTED: 0.3

    finalQty := int(float64(adjustedQty) * activityFactor)

    return max(1, finalQty)  // At least 1 unit
}
```

**Example Calculation**:
```
Ship capacity: 40 units
Available supply: 100 units
Buy price: 100 credits/unit
Player credits: 10,000
Buy supply: HIGH → 0.8 factor
Sell activity: GROWING → 0.8 factor

Step 1: maxBySupply = min(40, 100) = 40
Step 2: maxByCapital = min(40, 100) = 40
Step 3: supplyAdjustment = 40 × 0.8 = 32
Step 4: activityAdjustment = 32 × 0.8 = 25.6 → 25 units

Result: Buy 25 units instead of 40
```

**Advantages**:
- Simple to implement
- Interpretable (can explain quantity decision)
- Conservative (prevents market disruption)
- No ML training required

**Disadvantages**:
- Adjustment factors chosen manually
- May be overly conservative
- Doesn't learn from data

#### Approach 2: Marginal Profit Optimization

**Concept**: Buy until marginal profit equals zero

```go
func CalculateOptimalQuantityMarginal(
    opportunity *ArbitrageOpportunity,
    priceFunction PriceFunction,  // Models price slippage
) int {
    maxCapacity := ship.Cargo().AvailableCapacity()
    optimalQty := 0
    maxProfit := 0.0

    // Try each quantity from 1 to max capacity
    for qty := 1; qty <= maxCapacity; qty++ {
        // Estimate avg prices with slippage
        avgBuyPrice := priceFunction.EstimateAvgBuyPrice(qty)
        avgSellPrice := priceFunction.EstimateAvgSellPrice(qty)

        // Calculate total profit for this quantity
        grossProfit := qty * (avgSellPrice - avgBuyPrice)

        // Deduct fuel cost (may increase with weight)
        fuelCost := estimateFuelCost(qty, distance)
        netProfit := grossProfit - fuelCost

        if netProfit > maxProfit {
            maxProfit = netProfit
            optimalQty = qty
        }
    }

    return optimalQty
}
```

**Challenge**: Requires price slippage model (needs historical data)

**Advantages**:
- Economically sound (marginal analysis)
- Optimal for given price function

**Disadvantages**:
- Requires accurate price function
- Computationally expensive (40 iterations)
- Price function may not exist in SpaceTraders

#### Approach 3: ML-Based Quantity Prediction

**Concept**: Add quantity as a decision variable in ML model

```python
# Training: Include quantity as feature
features['quantity_purchased'] = logs_df['units_purchased']
features['quantity_ratio'] = logs_df['units_purchased'] / logs_df['cargo_capacity']

# Train model: profit/time = f(opportunity_features, quantity)
model.fit(X_train, y_train)

# Inference: Grid search over quantities to find optimal
def predict_optimal_quantity(opportunity_features, model, max_capacity):
    best_qty = 1
    best_profit_per_sec = 0

    for qty in range(1, max_capacity + 1):
        features_with_qty = {
            **opportunity_features,
            'quantity': qty,
            'quantity_ratio': qty / max_capacity,
        }

        predicted_profit = model.predict([features_with_qty])[0]

        if predicted_profit > best_profit_per_sec:
            best_profit_per_sec = predicted_profit
            best_qty = qty

    return best_qty
```

**Advantages**:
- Data-driven (learns actual profit patterns)
- Captures non-linear relationships
- Adapts to market dynamics

**Disadvantages**:
- Requires training data with quantity variation
- Slower inference (40 predictions per opportunity)
- Black box decision

### Integration Strategy

**Phase 1** (Rule-Based - Immediate):
- Implement heuristic quantity calculator
- Conservative factors: 0.6-1.0 range
- Apply to all arbitrage workers
- Log quantity decisions for analysis

**Phase 2** (Data Collection):
- Modify `arbitrage_execution_logs` table
- Add fields: `quantity_decision_ratio`, `cargo_utilization`
- A/B test different quantity strategies:
  - 20% ships: 60% capacity
  - 20% ships: 80% capacity
  - 60% ships: 100% capacity (baseline)
- Measure which ratio yields best profit/time

**Phase 3** (ML Optimization):
- Include quantity as ML feature
- Grid search during inference
- Compare ML quantity vs rule-based

### Expected Impact

**Conservative Markets** (LIMITED supply, WEAK demand):
- Current: May fail or get poor prices
- Optimized: Avoid market disruption
- **Estimated improvement: +15-25%** in specific cases

**Baseline Markets** (ABUNDANT supply, STRONG demand):
- Current: 100% capacity works well
- Optimized: Similar performance
- **Estimated impact: ±5%**

**Average Across All Markets**:
- **Estimated improvement: +5-10%** overall
- Higher gains in volatile/thin markets
- Minimal loss in liquid markets

---

## Recommended Strategy

### Phased Rollout

We recommend a **hybrid phased approach** that starts with low-risk genetic algorithms and evolves to high-performance machine learning:

```
Phase 1: Data Collection
    ↓
Phase 2: Genetic Algorithm (Low Risk, Quick Win)
    ↓ (Validates optimization value)
Phase 3: Machine Learning (High Performance)
    ↓ (Requires sufficient data)
Phase 4: Ensemble & Advanced Techniques
```

**Rationale**:
1. **Phase 1** collects training data for both approaches
2. **Phase 2** validates that optimization improves profit/time (proof of concept)
3. **Phase 3** unlocks higher performance after data accumulation
4. **Phase 4** combines best of both worlds

### Decision Gates

**Proceed to Phase 3 if**:
- Phase 2 GA achieves ≥10% profit/time improvement (proves optimization works)
- ≥1000 high-quality training examples collected
- Infrastructure capacity available for ML service

**Proceed to Phase 4 if**:
- Phase 3 ML achieves ≥15% additional improvement over GA
- Both GA and ML stable in production
- Advanced optimization features requested

---

## Phase 1: Data Collection Infrastructure

### Objectives

1. Capture complete execution logs for training data
2. Maximize data collection rate via parallel ships
3. Ensure data quality and completeness
4. Enable offline analysis and model training

### Data Acceleration Strategy

**Parallel Collection**:
- With 10 ships running arbitrage coordinator
- Average 4 successful runs per ship per hour
- **Collection rate**: 10 ships × 4 runs/hour = **40 examples/hour**

**Data Volume Projections**:
- 1 day (24 hours): ~960 examples
- 3 days: ~2,880 examples (sufficient for ML)
- 1 week: ~6,720 examples (excellent for ML)

**Advantages**:
- No need to wait weeks for data
- Can train ML models within days
- Rapid iteration on features and models
- A/B testing becomes feasible quickly

### Database Schema

#### New Table: `arbitrage_execution_logs`

```sql
CREATE TABLE arbitrage_execution_logs (
    id SERIAL PRIMARY KEY,

    -- Execution metadata
    container_id VARCHAR(255) NOT NULL,
    ship_symbol VARCHAR(50) NOT NULL,
    player_id INT NOT NULL,
    executed_at TIMESTAMP NOT NULL,
    success BOOLEAN NOT NULL,
    error_message TEXT,

    -- Opportunity features (at decision time)
    good_symbol VARCHAR(50) NOT NULL,
    buy_market VARCHAR(50) NOT NULL,
    sell_market VARCHAR(50) NOT NULL,
    buy_price INT NOT NULL,
    sell_price INT NOT NULL,
    profit_margin DECIMAL(10, 2) NOT NULL,
    distance DECIMAL(10, 2) NOT NULL,
    estimated_profit INT NOT NULL,
    buy_supply VARCHAR(20),
    sell_activity VARCHAR(20),
    current_score DECIMAL(10, 2),  -- Score used at decision time

    -- Ship state (at decision time)
    cargo_capacity INT NOT NULL,
    cargo_used INT NOT NULL,
    fuel_current INT NOT NULL,
    fuel_capacity INT NOT NULL,
    current_location VARCHAR(50),

    -- Execution results
    actual_net_profit INT,
    actual_duration_seconds INT,
    fuel_consumed INT,
    units_purchased INT,
    units_sold INT,
    purchase_cost INT,
    sale_revenue INT,

    -- Derived metrics (computed)
    profit_per_second DECIMAL(10, 4),  -- actual_net_profit / actual_duration_seconds
    profit_per_unit DECIMAL(10, 2),    -- actual_net_profit / units_sold
    margin_accuracy DECIMAL(10, 2),    -- (actual_margin - estimated_margin)

    -- Indexes for querying
    INDEX idx_player_id (player_id),
    INDEX idx_executed_at (executed_at),
    INDEX idx_good_symbol (good_symbol),
    INDEX idx_success (success),
    INDEX idx_container_id (container_id)
);
```

### Domain Model

**File**: `internal/domain/trading/arbitrage_execution_log.go`

```go
package trading

import (
    "time"
)

// ArbitrageExecutionLog captures complete execution data for ML training.
// This is a pure data structure (no business logic) optimized for persistence.
type ArbitrageExecutionLog struct {
    // Execution metadata
    id          int
    containerID string
    shipSymbol  string
    playerID    int
    executedAt  time.Time
    success     bool
    errorMsg    string

    // Opportunity features (snapshot at decision time)
    good            string
    buyMarket       string
    sellMarket      string
    buyPrice        int
    sellPrice       int
    profitMargin    float64
    distance        float64
    estimatedProfit int
    buySupply       string
    sellActivity    string
    currentScore    float64  // Score that led to this selection

    // Ship state (snapshot at decision time)
    cargoCapacity   int
    cargoUsed       int
    fuelCurrent     int
    fuelCapacity    int
    currentLocation string

    // Execution results (actual outcomes)
    actualNetProfit      int
    actualDuration       int  // seconds
    fuelConsumed         int
    unitsPurchased       int
    unitsSold            int
    purchaseCost         int
    saleRevenue          int

    // Derived metrics
    profitPerSecond  float64  // Target for ML
    profitPerUnit    float64
    marginAccuracy   float64  // How accurate was our estimate?
}

// NewArbitrageExecutionLog creates a log entry from opportunity and result
func NewArbitrageExecutionLog(
    opportunity *ArbitrageOpportunity,
    ship *navigation.Ship,
    result *services.ArbitrageResult,
    containerID string,
    playerID int,
    success bool,
    errorMsg string,
) *ArbitrageExecutionLog {
    log := &ArbitrageExecutionLog{
        // Metadata
        containerID: containerID,
        shipSymbol:  ship.ShipSymbol(),
        playerID:    playerID,
        executedAt:  time.Now(),
        success:     success,
        errorMsg:    errorMsg,

        // Opportunity features
        good:            opportunity.Good(),
        buyMarket:       opportunity.BuyMarket().Symbol,
        sellMarket:      opportunity.SellMarket().Symbol,
        buyPrice:        opportunity.BuyPrice(),
        sellPrice:       opportunity.SellPrice(),
        profitMargin:    opportunity.ProfitMargin(),
        distance:        opportunity.Distance(),
        estimatedProfit: opportunity.EstimatedProfit(),
        buySupply:       opportunity.BuySupply(),
        sellActivity:    opportunity.SellActivity(),
        currentScore:    opportunity.Score(),

        // Ship state
        cargoCapacity:   ship.Cargo().Capacity(),
        cargoUsed:       ship.Cargo().Units(),
        fuelCurrent:     ship.Fuel().Current(),
        fuelCapacity:    ship.Fuel().Capacity(),
        currentLocation: ship.NavStatus().String(),
    }

    // Add results if available
    if result != nil {
        log.actualNetProfit = result.NetProfit
        log.actualDuration = result.DurationSeconds
        log.fuelConsumed = result.FuelCost
        log.unitsPurchased = result.UnitsPurchased
        log.unitsSold = result.UnitsSold
        log.purchaseCost = result.PurchaseCost
        log.saleRevenue = result.SaleRevenue

        // Compute derived metrics
        if log.actualDuration > 0 {
            log.profitPerSecond = float64(log.actualNetProfit) / float64(log.actualDuration)
        }
        if log.unitsSold > 0 {
            log.profitPerUnit = float64(log.actualNetProfit) / float64(log.unitsSold)
        }

        // Margin accuracy: actual vs. estimated
        if log.unitsSold > 0 && log.purchaseCost > 0 {
            actualMargin := float64(log.saleRevenue-log.purchaseCost) / float64(log.purchaseCost) * 100
            log.marginAccuracy = actualMargin - log.profitMargin
        }
    }

    return log
}

// Getters (immutability)
func (l *ArbitrageExecutionLog) ProfitPerSecond() float64 { return l.profitPerSecond }
func (l *ArbitrageExecutionLog) Success() bool { return l.success }
// ... (other getters)
```

### Repository Interface

**File**: `internal/domain/trading/ports.go`

```go
// ArbitrageExecutionLogRepository manages execution logs for training
type ArbitrageExecutionLogRepository interface {
    // Save persists a new execution log
    Save(ctx context.Context, log *ArbitrageExecutionLog) error

    // FindByPlayerID retrieves logs for ML training
    FindByPlayerID(
        ctx context.Context,
        playerID int,
        limit int,
        offset int,
    ) ([]*ArbitrageExecutionLog, error)

    // FindSuccessfulRuns retrieves only successful executions
    FindSuccessfulRuns(
        ctx context.Context,
        playerID int,
        minExamples int,
    ) ([]*ArbitrageExecutionLog, error)

    // CountByPlayerID returns total logged executions
    CountByPlayerID(ctx context.Context, playerID int) (int, error)

    // ExportToCSV exports logs for ML training (Python consumption)
    ExportToCSV(
        ctx context.Context,
        playerID int,
        outputPath string,
    ) error
}
```

### Integration with ArbitrageExecutor

**Modification**: `internal/application/trading/services/arbitrage_executor.go`

```go
type ArbitrageExecutor struct {
    mediator   common.Mediator
    shipRepo   navigation.ShipRepository
    logRepo    trading.ArbitrageExecutionLogRepository  // NEW
}

func (e *ArbitrageExecutor) Execute(
    ctx context.Context,
    ship *navigation.Ship,
    opportunity *trading.ArbitrageOpportunity,
    playerID int,
    containerID string,
) (*ArbitrageResult, error) {
    // ... existing execution logic ...

    result, err := e.executeTradesCycle(ctx, ship, opportunity, playerID, containerID)

    // NEW: Log execution results
    success := (err == nil)
    errorMsg := ""
    if err != nil {
        errorMsg = err.Error()
    }

    log := trading.NewArbitrageExecutionLog(
        opportunity,
        ship,
        result,
        containerID,
        playerID,
        success,
        errorMsg,
    )

    // Persist log (fire and forget - don't block on logging errors)
    go func() {
        if logErr := e.logRepo.Save(context.Background(), log); logErr != nil {
            logger.Log("ERROR", fmt.Sprintf("Failed to save execution log: %v", logErr), nil)
        }
    }()

    return result, err
}
```

### Data Quality Validation

**Quality Checks** (implemented in repository):

1. **Completeness**: All required fields present
2. **Consistency**: `profit_per_second = actual_net_profit / actual_duration`
3. **Sanity**: `actual_duration > 0`, `units_sold <= cargo_capacity`
4. **Outlier Detection**: Flag suspiciously high/low profits for review

**Example Validation**:

```go
func (r *GormArbitrageExecutionLogRepository) Save(
    ctx context.Context,
    log *ArbitrageExecutionLog,
) error {
    // Validate before saving
    if err := validateLog(log); err != nil {
        return fmt.Errorf("invalid execution log: %w", err)
    }

    // Convert to GORM model
    model := toGormModel(log)

    return r.db.Create(model).Error
}

func validateLog(log *ArbitrageExecutionLog) error {
    if log.shipSymbol == "" {
        return errors.New("ship symbol required")
    }
    if log.success && log.actualDuration <= 0 {
        return errors.New("successful run must have positive duration")
    }
    if log.unitsSold > log.cargoCapacity {
        return fmt.Errorf("units sold (%d) exceeds cargo capacity (%d)",
            log.unitsSold, log.cargoCapacity)
    }
    // ... more validations
    return nil
}
```

### Export Utilities

**CSV Export for ML Training**:

```go
// ExportToCSV writes logs to CSV for Python ML pipeline
func (r *GormArbitrageExecutionLogRepository) ExportToCSV(
    ctx context.Context,
    playerID int,
    outputPath string,
) error {
    logs, err := r.FindSuccessfulRuns(ctx, playerID, 0)
    if err != nil {
        return err
    }

    file, err := os.Create(outputPath)
    if err != nil {
        return err
    }
    defer file.Close()

    writer := csv.NewWriter(file)
    defer writer.Flush()

    // Write header
    header := []string{
        "good", "buy_market", "sell_market", "buy_price", "sell_price",
        "profit_margin", "distance", "buy_supply", "sell_activity",
        "cargo_capacity", "fuel_current", "actual_net_profit",
        "actual_duration", "profit_per_second",
    }
    writer.Write(header)

    // Write rows
    for _, log := range logs {
        row := []string{
            log.good,
            log.buyMarket,
            log.sellMarket,
            strconv.Itoa(log.buyPrice),
            strconv.Itoa(log.sellPrice),
            fmt.Sprintf("%.2f", log.profitMargin),
            fmt.Sprintf("%.2f", log.distance),
            log.buySupply,
            log.sellActivity,
            strconv.Itoa(log.cargoCapacity),
            strconv.Itoa(log.fuelCurrent),
            strconv.Itoa(log.actualNetProfit),
            strconv.Itoa(log.actualDuration),
            fmt.Sprintf("%.4f", log.profitPerSecond),
        }
        writer.Write(row)
    }

    return nil
}
```

### CLI Command

**New Command**: `spacetraders arbitrage export-data`

```bash
# Export training data to CSV
./bin/spacetraders arbitrage export-data --player 1 --output training_data.csv

# Check data volume
./bin/spacetraders arbitrage data-stats --player 1
```

### Success Criteria for Phase 1

- [ ] Database table created and indexed
- [ ] Domain model implemented with validation
- [ ] Repository interface and implementation complete
- [ ] Integration with ArbitrageExecutor successful
- [ ] CSV export utility functional
- [ ] CLI command for data export working
- [ ] Data quality validation in place
- [ ] **Minimum 100 successful executions logged** (for GA)
- [ ] **Minimum 1000 successful executions logged** (for ML)

---

## Phase 2: Genetic Algorithm Optimization

### Objectives

1. Evolve scoring formula weights via genetic algorithm
2. Validate that optimization improves profit/time
3. Establish baseline for ML comparison
4. Prove value of data-driven optimization

### Algorithm Design

#### Genome Representation

**Chromosome**: Array of floating-point weights

```go
// internal/application/trading/optimization/genome.go

type ScoringGenome struct {
    genes []float64  // [profitWeight, supplyWeight, activityWeight, distancePenalty]
}

func NewRandomGenome() *ScoringGenome {
    return &ScoringGenome{
        genes: []float64{
            rand.Float64() * 100.0,  // profitWeight: 0-100
            rand.Float64() * 50.0,   // supplyWeight: 0-50
            rand.Float64() * 50.0,   // activityWeight: 0-50
            rand.Float64() * 1.0,    // distancePenalty: 0-1
        },
    }
}

func (g *ScoringGenome) Clone() *ScoringGenome {
    cloned := make([]float64, len(g.genes))
    copy(cloned, g.genes)
    return &ScoringGenome{genes: cloned}
}

func (g *ScoringGenome) ProfitWeight() float64    { return g.genes[0] }
func (g *ScoringGenome) SupplyWeight() float64    { return g.genes[1] }
func (g *ScoringGenome) ActivityWeight() float64  { return g.genes[2] }
func (g *ScoringGenome) DistancePenalty() float64 { return g.genes[3] }
```

#### Fitness Function

**Objective**: Maximize average profit/time on historical data

```go
// EvaluateFitness computes fitness by simulating historical opportunity selection
func (e *GeneticEvolver) EvaluateFitness(
    genome *ScoringGenome,
    historicalLogs []*trading.ArbitrageExecutionLog,
) float64 {
    // Group logs by execution batch (same timestamp window)
    batches := groupLogsByBatch(historicalLogs)

    totalProfit := 0.0
    totalTime := 0.0
    count := 0

    for _, batch := range batches {
        // Rescore all opportunities in this batch using genome's weights
        opportunities := extractOpportunities(batch)
        scores := make([]float64, len(opportunities))

        for i, opp := range opportunities {
            scores[i] = e.scoreWithGenome(genome, opp)
        }

        // Find which opportunity would have been selected (highest score)
        selectedIdx := argmax(scores)
        selectedLog := batch[selectedIdx]

        // Accumulate actual results from selected opportunity
        if selectedLog.Success() {
            totalProfit += float64(selectedLog.ActualNetProfit())
            totalTime += float64(selectedLog.ActualDuration())
            count++
        }
    }

    if count == 0 || totalTime == 0 {
        return 0.0  // No successful runs
    }

    // Fitness = average profit per second
    return totalProfit / totalTime
}

func (e *GeneticEvolver) scoreWithGenome(
    genome *ScoringGenome,
    opp *opportunitySnapshot,
) float64 {
    profitScore := opp.ProfitMargin * genome.ProfitWeight()
    supplyScore := supplyToScore(opp.BuySupply) * genome.SupplyWeight()
    activityScore := activityToScore(opp.SellActivity) * genome.ActivityWeight()
    distanceScore := opp.Distance * genome.DistancePenalty()

    return profitScore + supplyScore + activityScore - distanceScore
}
```

**Key Insight**: We simulate what would have happened if we used these weights historically.

#### Selection Operator

**Tournament Selection**:

```go
func (e *GeneticEvolver) SelectParent(
    population []*ScoringGenome,
    fitness []float64,
    tournamentSize int,
) *ScoringGenome {
    // Randomly sample tournamentSize individuals
    candidates := make([]int, tournamentSize)
    for i := 0; i < tournamentSize; i++ {
        candidates[i] = rand.Intn(len(population))
    }

    // Return fittest from tournament
    bestIdx := candidates[0]
    bestFitness := fitness[candidates[0]]

    for _, idx := range candidates[1:] {
        if fitness[idx] > bestFitness {
            bestIdx = idx
            bestFitness = fitness[idx]
        }
    }

    return population[bestIdx]
}
```

#### Crossover Operator

**Uniform Crossover**:

```go
func (e *GeneticEvolver) Crossover(
    parent1 *ScoringGenome,
    parent2 *ScoringGenome,
) (*ScoringGenome, *ScoringGenome) {
    child1 := parent1.Clone()
    child2 := parent2.Clone()

    // Swap genes with 50% probability
    for i := 0; i < len(parent1.genes); i++ {
        if rand.Float64() < 0.5 {
            child1.genes[i], child2.genes[i] = child2.genes[i], child1.genes[i]
        }
    }

    return child1, child2
}
```

#### Mutation Operator

**Gaussian Mutation**:

```go
func (e *GeneticEvolver) Mutate(
    genome *ScoringGenome,
    mutationRate float64,
    mutationStrength float64,
) {
    for i := range genome.genes {
        if rand.Float64() < mutationRate {
            // Add Gaussian noise
            perturbation := rand.NormFloat64() * mutationStrength
            genome.genes[i] += perturbation

            // Ensure non-negative
            if genome.genes[i] < 0 {
                genome.genes[i] = 0
            }
        }
    }
}
```

### Main Evolution Algorithm

```go
// internal/application/trading/optimization/genetic_evolver.go

type GeneticEvolver struct {
    logRepo trading.ArbitrageExecutionLogRepository
}

type EvolutionConfig struct {
    PopulationSize   int     // 50
    Generations      int     // 100
    MutationRate     float64 // 0.1 (10% chance per gene)
    MutationStrength float64 // 5.0 (std dev of Gaussian noise)
    EliteSize        int     // 5 (top N preserved unchanged)
    TournamentSize   int     // 5 (for selection)
}

func (e *GeneticEvolver) Evolve(
    ctx context.Context,
    playerID int,
    config EvolutionConfig,
) (*ScoringGenome, error) {
    // Load historical data
    logs, err := e.logRepo.FindSuccessfulRuns(ctx, playerID, 100)
    if err != nil {
        return nil, fmt.Errorf("failed to load training data: %w", err)
    }

    if len(logs) < 100 {
        return nil, fmt.Errorf("insufficient training data: %d < 100", len(logs))
    }

    logger := common.LoggerFromContext(ctx)
    logger.Log("INFO", fmt.Sprintf("Starting evolution with %d examples", len(logs)), nil)

    // Initialize population
    population := make([]*ScoringGenome, config.PopulationSize)
    for i := 0; i < config.PopulationSize; i++ {
        population[i] = NewRandomGenome()
    }

    // Add current weights as one individual (warm start)
    population[0] = &ScoringGenome{
        genes: []float64{40.0, 20.0, 20.0, 0.1},  // Current baseline
    }

    // Evolution loop
    for gen := 0; gen < config.Generations; gen++ {
        // Evaluate fitness for all individuals
        fitness := make([]float64, config.PopulationSize)
        for i, genome := range population {
            fitness[i] = e.EvaluateFitness(genome, logs)
        }

        // Sort population by fitness (descending)
        indices := make([]int, len(population))
        for i := range indices {
            indices[i] = i
        }
        sort.Slice(indices, func(i, j int) bool {
            return fitness[indices[i]] > fitness[indices[j]]
        })

        sortedPop := make([]*ScoringGenome, len(population))
        sortedFit := make([]float64, len(fitness))
        for i, idx := range indices {
            sortedPop[i] = population[idx]
            sortedFit[i] = fitness[idx]
        }
        population = sortedPop
        fitness = sortedFit

        // Log generation stats
        logger.Log("INFO", fmt.Sprintf(
            "Generation %d: best=%.4f avg=%.4f worst=%.4f",
            gen, fitness[0], mean(fitness), fitness[len(fitness)-1],
        ), nil)

        // Create next generation
        nextGen := make([]*ScoringGenome, config.PopulationSize)

        // Elitism: preserve top performers
        for i := 0; i < config.EliteSize; i++ {
            nextGen[i] = population[i].Clone()
        }

        // Breeding: fill rest with offspring
        for i := config.EliteSize; i < config.PopulationSize; i += 2 {
            parent1 := e.SelectParent(population, fitness, config.TournamentSize)
            parent2 := e.SelectParent(population, fitness, config.TournamentSize)

            child1, child2 := e.Crossover(parent1, parent2)

            e.Mutate(child1, config.MutationRate, config.MutationStrength)
            e.Mutate(child2, config.MutationRate, config.MutationStrength)

            nextGen[i] = child1
            if i+1 < config.PopulationSize {
                nextGen[i+1] = child2
            }
        }

        population = nextGen
    }

    // Final evaluation
    finalFitness := make([]float64, len(population))
    for i, genome := range population {
        finalFitness[i] = e.EvaluateFitness(genome, logs)
    }

    // Return best genome
    bestIdx := argmax(finalFitness)
    bestGenome := population[bestIdx]

    logger.Log("INFO", fmt.Sprintf(
        "Evolution complete. Best fitness: %.4f (baseline: %.4f)",
        finalFitness[bestIdx],
        e.EvaluateFitness(&ScoringGenome{genes: []float64{40, 20, 20, 0.1}}, logs),
    ), nil)

    return bestGenome, nil
}
```

### Configuration Management

**Output Format**: YAML configuration file

```yaml
# configs/arbitrage_scoring_weights.yaml

version: 2
evolved_at: "2025-11-23T14:30:00Z"
training_examples: 1247
baseline_fitness: 12.34
evolved_fitness: 14.52
improvement_percent: 17.6

weights:
  profit_weight: 42.3
  supply_weight: 18.7
  activity_weight: 21.5
  distance_penalty: 0.12

metadata:
  generations: 100
  population_size: 50
  mutation_rate: 0.1
  elite_size: 5
```

**Loading Weights**:

```go
// internal/application/trading/services/arbitrage_opportunity_finder.go

type ArbitrageOpportunityFinder struct {
    // ... existing fields
    scoringWeights *ScoringWeights  // NEW
}

type ScoringWeights struct {
    ProfitWeight    float64
    SupplyWeight    float64
    ActivityWeight  float64
    DistancePenalty float64
}

func (f *ArbitrageOpportunityFinder) LoadWeights(configPath string) error {
    // Load YAML config
    data, err := os.ReadFile(configPath)
    if err != nil {
        return err
    }

    var config struct {
        Weights ScoringWeights `yaml:"weights"`
    }

    if err := yaml.Unmarshal(data, &config); err != nil {
        return err
    }

    f.scoringWeights = &config.Weights
    return nil
}
```

### CLI Commands

```bash
# Run genetic algorithm evolution
./bin/spacetraders arbitrage evolve \
  --player 1 \
  --generations 100 \
  --population 50 \
  --output configs/arbitrage_scoring_weights_v2.yaml

# Compare baseline vs evolved weights
./bin/spacetraders arbitrage compare-weights \
  --player 1 \
  --baseline configs/arbitrage_scoring_weights_v1.yaml \
  --evolved configs/arbitrage_scoring_weights_v2.yaml

# Apply evolved weights to coordinator
./bin/spacetraders arbitrage start \
  --system X1-AU21 \
  --weights-config configs/arbitrage_scoring_weights_v2.yaml
```

### Testing Strategy

**BDD Test**: `test/bdd/features/application/trading/genetic_optimizer.feature`

```gherkin
Feature: Genetic Algorithm Optimization

  Scenario: Evolve scoring weights from historical data
    Given 150 historical arbitrage execution logs
    And baseline weights [40.0, 20.0, 20.0, 0.1]
    When I run genetic evolution with 50 generations
    Then evolved weights should exist
    And evolved fitness should exceed baseline fitness
    And weights should be non-negative

  Scenario: Insufficient training data
    Given 50 historical arbitrage execution logs
    When I run genetic evolution
    Then evolution should fail with error "insufficient training data"

  Scenario: Fitness evaluation on historical data
    Given genome with weights [45.0, 15.0, 25.0, 0.2]
    And 100 historical execution logs
    When I evaluate fitness
    Then fitness should be positive
    And fitness should reflect simulated profit/time
```

### Success Criteria for Phase 2

- [ ] Genetic evolver implemented and tested
- [ ] Fitness function validated (matches manual calculation)
- [ ] Selection, crossover, mutation operators working
- [ ] Configuration export/import functional
- [ ] CLI commands operational
- [ ] BDD tests passing
- [ ] **Evolved weights achieve ≥10% fitness improvement over baseline**
- [ ] Weights applied to live coordinator
- [ ] Profit/time monitored in production

---

## Phase 3: Machine Learning Model

### Objectives

1. Train XGBoost model to predict profit/time
2. Deploy Python ML service
3. Integrate ML scoring with Go coordinator
4. Achieve >20% improvement over baseline

### Feature Engineering

#### Feature Categories (28 total features)

**1. Market Features (8 features)**:
- `buy_price` (normalized: price / 1000)
- `sell_price` (normalized)
- `profit_margin` (percentage)
- `buy_supply_ordinal` (SCARCE=0, LIMITED=1, MODERATE=2, HIGH=3, ABUNDANT=4)
- `sell_activity_ordinal` (RESTRICTED=0, WEAK=1, GROWING=2, STRONG=3)
- `estimated_profit` (absolute credits)
- `price_spread` (sell_price - buy_price)
- `margin_category` (binned: 0-10%, 10-20%, 20-30%, 30%+)

**2. Spatial Features (5 features)**:
- `distance` (Euclidean distance)
- `distance_squared` (non-linear effect)
- `log_distance` (log(distance + 1))
- `distance_category` (binned: 0-50, 50-100, 100-200, 200+)
- `is_nearby` (1 if distance < 50, else 0)

**3. Ship Features (7 features)**:
- `cargo_capacity` (units)
- `cargo_utilization` (cargo_used / cargo_capacity)
- `fuel_level` (current fuel)
- `fuel_percentage` (fuel_current / fuel_capacity)
- `estimated_fuel_needed` (based on distance)
- `fuel_margin` (fuel_current - estimated_fuel_needed)
- `needs_refuel` (1 if fuel_margin < 10%, else 0)

**4. Opportunity Interaction Features (5 features)**:
- `profit_per_distance` (estimated_profit / (distance + 1))
- `margin_times_supply` (profit_margin × buy_supply_ordinal)
- `margin_times_activity` (profit_margin × sell_activity_ordinal)
- `supply_activity_product` (buy_supply_ordinal × sell_activity_ordinal)
- `efficiency_score` (estimated_profit / sqrt(distance + 1))

**5. Temporal/Categorical Features (3 features)** (if available):
- `good_category` (one-hot encoded: ORE, FOOD, EQUIPMENT, etc.)
- `hour_of_day` (if temporal patterns exist)
- `market_volatility` (std dev of recent price changes, if tracked)

#### Feature Extraction Code

```python
# services/ml-service/features.py

import pandas as pd
import numpy as np

def extract_features(logs_df: pd.DataFrame) -> pd.DataFrame:
    """
    Extract ML features from execution logs.

    Args:
        logs_df: DataFrame with columns from arbitrage_execution_logs table

    Returns:
        DataFrame with 28 feature columns
    """
    features = pd.DataFrame()

    # Market features
    features['buy_price_norm'] = logs_df['buy_price'] / 1000.0
    features['sell_price_norm'] = logs_df['sell_price'] / 1000.0
    features['profit_margin'] = logs_df['profit_margin']
    features['buy_supply_ord'] = logs_df['buy_supply'].map({
        'SCARCE': 0, 'LIMITED': 1, 'MODERATE': 2, 'HIGH': 3, 'ABUNDANT': 4
    })
    features['sell_activity_ord'] = logs_df['sell_activity'].map({
        'RESTRICTED': 0, 'WEAK': 1, 'GROWING': 2, 'STRONG': 3
    })
    features['estimated_profit'] = logs_df['estimated_profit']
    features['price_spread'] = logs_df['sell_price'] - logs_df['buy_price']
    features['margin_category'] = pd.cut(
        logs_df['profit_margin'],
        bins=[0, 10, 20, 30, 1000],
        labels=[0, 1, 2, 3]
    ).astype(int)

    # Spatial features
    features['distance'] = logs_df['distance']
    features['distance_squared'] = logs_df['distance'] ** 2
    features['log_distance'] = np.log1p(logs_df['distance'])
    features['distance_category'] = pd.cut(
        logs_df['distance'],
        bins=[0, 50, 100, 200, 10000],
        labels=[0, 1, 2, 3]
    ).astype(int)
    features['is_nearby'] = (logs_df['distance'] < 50).astype(int)

    # Ship features
    features['cargo_capacity'] = logs_df['cargo_capacity']
    features['cargo_utilization'] = logs_df['cargo_used'] / logs_df['cargo_capacity']
    features['fuel_level'] = logs_df['fuel_current']
    features['fuel_percentage'] = logs_df['fuel_current'] / logs_df['fuel_capacity']

    # Estimate fuel needed (rough approximation: distance / 10)
    features['estimated_fuel_needed'] = logs_df['distance'] / 10.0
    features['fuel_margin'] = logs_df['fuel_current'] - features['estimated_fuel_needed']
    features['needs_refuel'] = (
        features['fuel_margin'] / logs_df['fuel_capacity'] < 0.1
    ).astype(int)

    # Interaction features
    features['profit_per_distance'] = (
        logs_df['estimated_profit'] / (logs_df['distance'] + 1)
    )
    features['margin_times_supply'] = (
        logs_df['profit_margin'] * features['buy_supply_ord']
    )
    features['margin_times_activity'] = (
        logs_df['profit_margin'] * features['sell_activity_ord']
    )
    features['supply_activity_product'] = (
        features['buy_supply_ord'] * features['sell_activity_ord']
    )
    features['efficiency_score'] = (
        logs_df['estimated_profit'] / np.sqrt(logs_df['distance'] + 1)
    )

    # Good category (one-hot encoded)
    good_dummies = pd.get_dummies(logs_df['good_symbol'], prefix='good')
    features = pd.concat([features, good_dummies], axis=1)

    return features

def extract_target(logs_df: pd.DataFrame) -> pd.Series:
    """Extract target variable: profit per second."""
    return logs_df['profit_per_second']
```

### Model Training Pipeline

```python
# services/ml-service/train.py

import lightgbm as lgb
from sklearn.model_selection import train_test_split
from sklearn.metrics import mean_absolute_error, mean_squared_error
import pandas as pd
import numpy as np
import joblib

def train_model(csv_path: str, output_path: str):
    """
    Train LightGBM model on arbitrage execution logs.

    Args:
        csv_path: Path to exported CSV from Go backend
        output_path: Path to save trained model
    """
    # Load data
    df = pd.read_csv(csv_path)
    print(f"Loaded {len(df)} training examples")

    # Extract features and target
    X = extract_features(df)
    y = extract_target(df)

    # Train/test split (80/20)
    X_train, X_test, y_train, y_test = train_test_split(
        X, y, test_size=0.2, random_state=42
    )

    print(f"Training on {len(X_train)} examples, testing on {len(X_test)}")

    # LightGBM parameters
    params = {
        'objective': 'regression',
        'metric': 'mae',
        'boosting_type': 'gbdt',
        'num_leaves': 31,
        'learning_rate': 0.05,
        'feature_fraction': 0.8,
        'bagging_fraction': 0.8,
        'bagging_freq': 5,
        'verbose': 1,
        'max_depth': 6,
        'min_data_in_leaf': 20,
    }

    # Create dataset
    train_data = lgb.Dataset(X_train, label=y_train)
    test_data = lgb.Dataset(X_test, label=y_test, reference=train_data)

    # Train
    model = lgb.train(
        params,
        train_data,
        num_boost_round=200,
        valid_sets=[train_data, test_data],
        valid_names=['train', 'test'],
        early_stopping_rounds=20,
        verbose_eval=10,
    )

    # Evaluate
    y_pred = model.predict(X_test, num_iteration=model.best_iteration)
    mae = mean_absolute_error(y_test, y_pred)
    rmse = np.sqrt(mean_squared_error(y_test, y_pred))

    print(f"\nModel Performance:")
    print(f"  MAE:  {mae:.4f}")
    print(f"  RMSE: {rmse:.4f}")

    # Feature importance
    importance = pd.DataFrame({
        'feature': X.columns,
        'importance': model.feature_importance(importance_type='gain')
    }).sort_values('importance', ascending=False)

    print(f"\nTop 10 Features:")
    print(importance.head(10))

    # Save model
    model.save_model(output_path)
    print(f"\nModel saved to {output_path}")

    # Save feature names for inference
    joblib.dump(X.columns.tolist(), output_path.replace('.txt', '_features.pkl'))

    return model, mae, rmse

if __name__ == '__main__':
    train_model(
        csv_path='training_data.csv',
        output_path='models/arbitrage_scorer_v1.txt'
    )
```

### ML Service Deployment

```python
# services/ml-service/server.py

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
import lightgbm as lgb
import numpy as np
import pandas as pd
import joblib
from typing import List

app = FastAPI(title="Arbitrage ML Scoring Service")

# Load model at startup
model = lgb.Booster(model_file='models/arbitrage_scorer_v1.txt')
feature_names = joblib.load('models/arbitrage_scorer_v1_features.pkl')

class OpportunityFeatures(BaseModel):
    """Features for a single arbitrage opportunity."""
    buy_price: int
    sell_price: int
    profit_margin: float
    buy_supply: str
    sell_activity: str
    distance: float
    estimated_profit: int
    cargo_capacity: int
    cargo_used: int
    fuel_current: int
    fuel_capacity: int
    good_symbol: str

class ScoreRequest(BaseModel):
    """Batch scoring request."""
    opportunities: List[OpportunityFeatures]

class ScoreResponse(BaseModel):
    """Batch scoring response."""
    scores: List[float]
    model_version: str

@app.post("/score", response_model=ScoreResponse)
async def score_opportunities(request: ScoreRequest):
    """
    Score arbitrage opportunities by predicted profit/time.

    Returns:
        List of predicted profit_per_second values (higher = better)
    """
    try:
        # Convert to DataFrame
        df = pd.DataFrame([opp.dict() for opp in request.opportunities])

        # Extract features (same as training)
        X = extract_features(df)

        # Ensure feature order matches training
        X = X[feature_names]

        # Predict
        predictions = model.predict(X, num_iteration=model.best_iteration)

        return ScoreResponse(
            scores=predictions.tolist(),
            model_version="v1"
        )

    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

@app.get("/health")
async def health_check():
    """Health check endpoint."""
    return {
        "status": "healthy",
        "model_loaded": model is not None,
        "num_features": len(feature_names)
    }

@app.post("/retrain")
async def retrain_model(csv_path: str):
    """
    Retrain model with new data.

    This endpoint would be called periodically (e.g., weekly)
    to incorporate new execution logs.
    """
    global model, feature_names

    # Load new data and retrain
    new_model, mae, rmse = train_model(
        csv_path=csv_path,
        output_path='models/arbitrage_scorer_v2.txt'
    )

    # Hot-swap model
    model = new_model
    feature_names = joblib.load('models/arbitrage_scorer_v2_features.pkl')

    return {
        "status": "retrained",
        "mae": mae,
        "rmse": rmse,
        "version": "v2"
    }

if __name__ == '__main__':
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8000)
```

### Go Integration

#### MLOpportunityScorer Service

```go
// internal/application/trading/services/ml_opportunity_scorer.go

type MLOpportunityScorer struct {
    mlServiceURL string  // "http://localhost:8000"
    httpClient   *http.Client
    timeout      time.Duration
}

type OpportunityFeatures struct {
    BuyPrice        int     `json:"buy_price"`
    SellPrice       int     `json:"sell_price"`
    ProfitMargin    float64 `json:"profit_margin"`
    BuySupply       string  `json:"buy_supply"`
    SellActivity    string  `json:"sell_activity"`
    Distance        float64 `json:"distance"`
    EstimatedProfit int     `json:"estimated_profit"`
    CargoCapacity   int     `json:"cargo_capacity"`
    CargoUsed       int     `json:"cargo_used"`
    FuelCurrent     int     `json:"fuel_current"`
    FuelCapacity    int     `json:"fuel_capacity"`
    GoodSymbol      string  `json:"good_symbol"`
}

type ScoreRequest struct {
    Opportunities []OpportunityFeatures `json:"opportunities"`
}

type ScoreResponse struct {
    Scores       []float64 `json:"scores"`
    ModelVersion string    `json:"model_version"`
}

func NewMLOpportunityScorer(mlServiceURL string) *MLOpportunityScorer {
    return &MLOpportunityScorer{
        mlServiceURL: mlServiceURL,
        httpClient: &http.Client{
            Timeout: 5 * time.Second,
        },
        timeout: 5 * time.Second,
    }
}

func (s *MLOpportunityScorer) ScoreOpportunities(
    ctx context.Context,
    opportunities []*trading.ArbitrageOpportunity,
    ship *navigation.Ship,
) ([]float64, error) {
    // Extract features
    features := make([]OpportunityFeatures, len(opportunities))
    for i, opp := range opportunities {
        features[i] = OpportunityFeatures{
            BuyPrice:        opp.BuyPrice(),
            SellPrice:       opp.SellPrice(),
            ProfitMargin:    opp.ProfitMargin(),
            BuySupply:       opp.BuySupply(),
            SellActivity:    opp.SellActivity(),
            Distance:        opp.Distance(),
            EstimatedProfit: opp.EstimatedProfit(),
            CargoCapacity:   ship.Cargo().Capacity(),
            CargoUsed:       ship.Cargo().Units(),
            FuelCurrent:     ship.Fuel().Current(),
            FuelCapacity:    ship.Fuel().Capacity(),
            GoodSymbol:      opp.Good(),
        }
    }

    // Create request
    reqBody := ScoreRequest{Opportunities: features}
    reqJSON, err := json.Marshal(reqBody)
    if err != nil {
        return nil, fmt.Errorf("failed to marshal request: %w", err)
    }

    // HTTP POST to ML service
    req, err := http.NewRequestWithContext(
        ctx,
        "POST",
        s.mlServiceURL+"/score",
        bytes.NewReader(reqJSON),
    )
    if err != nil {
        return nil, fmt.Errorf("failed to create request: %w", err)
    }
    req.Header.Set("Content-Type", "application/json")

    resp, err := s.httpClient.Do(req)
    if err != nil {
        return nil, fmt.Errorf("ML service request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        body, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("ML service error: %s", string(body))
    }

    // Parse response
    var scoreResp ScoreResponse
    if err := json.NewDecoder(resp.Body).Decode(&scoreResp); err != nil {
        return nil, fmt.Errorf("failed to decode response: %w", err)
    }

    return scoreResp.Scores, nil
}
```

#### Integration with OpportunityFinder

```go
// internal/application/trading/services/arbitrage_opportunity_finder.go

type ArbitrageOpportunityFinder struct {
    marketRepo       market.MarketRepository
    waypointProvider system.IWaypointProvider
    analyzer         *trading.ArbitrageAnalyzer
    mlScorer         *MLOpportunityScorer  // NEW (optional)
    useML            bool                   // NEW (flag to enable ML)
}

func (f *ArbitrageOpportunityFinder) FindOpportunities(
    ctx context.Context,
    systemSymbol string,
    playerID int,
    cargoCapacity int,
    minMargin float64,
    limit int,
) ([]*trading.ArbitrageOpportunity, error) {
    // ... existing logic to find all viable opportunities ...

    // NEW: Use ML scoring if enabled and available
    if f.useML && f.mlScorer != nil {
        // Get reference ship for feature extraction
        // (In production, we'd get the actual ship that will execute)
        referenceShip := f.getReferenceShip(ctx, playerID)

        // Score with ML
        mlScores, err := f.mlScorer.ScoreOpportunities(ctx, opportunities, referenceShip)
        if err != nil {
            // Fallback to GA/baseline scoring on ML failure
            logger.Log("WARN", fmt.Sprintf("ML scoring failed, falling back: %v", err), nil)
            f.scoreWithAnalyzer(opportunities)
        } else {
            // Apply ML scores
            for i, opp := range opportunities {
                opp.SetScore(mlScores[i])
            }
        }
    } else {
        // Use baseline/GA scoring
        f.scoreWithAnalyzer(opportunities)
    }

    // Sort and limit (same as before)
    sort.Slice(opportunities, func(i, j int) bool {
        return opportunities[i].Score() > opportunities[j].Score()
    })

    if len(opportunities) > limit {
        opportunities = opportunities[:limit]
    }

    return opportunities, nil
}

func (f *ArbitrageOpportunityFinder) scoreWithAnalyzer(
    opportunities []*trading.ArbitrageOpportunity,
) {
    for _, opp := range opportunities {
        score := f.analyzer.ScoreOpportunity(opp)
        opp.SetScore(score)
    }
}
```

### Docker Deployment

```dockerfile
# services/ml-service/Dockerfile

FROM python:3.10-slim

WORKDIR /app

# Install dependencies
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy code
COPY server.py .
COPY features.py .
COPY train.py .
COPY models/ ./models/

# Expose port
EXPOSE 8000

# Health check
HEALTHCHECK --interval=30s --timeout=3s \
  CMD curl -f http://localhost:8000/health || exit 1

# Run server
CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8000"]
```

```yaml
# docker-compose.ml.yml

version: '3.8'

services:
  ml-service:
    build: ./services/ml-service
    ports:
      - "8000:8000"
    volumes:
      - ./services/ml-service/models:/app/models
      - ./training_data:/data
    environment:
      - MODEL_PATH=/app/models/arbitrage_scorer_v1.txt
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8000/health"]
      interval: 30s
      timeout: 5s
      retries: 3
```

### Success Criteria for Phase 3

- [ ] Feature extraction implemented and tested
- [ ] LightGBM model training pipeline working
- [ ] Python ML service deployed
- [ ] Go MLOpportunityScorer service implemented
- [ ] HTTP integration tested (latency <50ms)
- [ ] Graceful fallback to GA on ML failure
- [ ] Model achieves MAE <2.0 on test set
- [ ] **ML scoring achieves ≥20% profit/time improvement over baseline**

---

## Phase 4: Integration & A/B Testing

### Objectives

1. Deploy both GA and ML scoring in production
2. Run controlled A/B test to measure effectiveness
3. Statistical validation of improvements
4. Production monitoring and alerting

### A/B Testing Framework

#### Traffic Splitting

```go
// internal/application/trading/commands/run_arbitrage_coordinator.go

type CoordinatorConfig struct {
    SystemSymbol string
    PlayerID     int
    MinMargin    float64
    MaxWorkers   int

    // NEW: A/B testing config
    ABTestEnabled bool
    MLTrafficPct  int  // 0-100: % of ships using ML
    ABTestSeed    int  // For reproducible randomization
}

func (h *RunArbitrageCoordinatorHandler) spawnWorkers(
    ctx context.Context,
    cmd *RunArbitrageCoordinatorCommand,
    idleShips []string,
    opportunities []*trading.ArbitrageOpportunity,
    maxWorkers int,
) {
    // ... existing logic ...

    for i := 0; i < numWorkers; i++ {
        ship := idleShips[i]
        opp := opportunities[i]

        // NEW: Determine which scorer to use for this ship
        useML := false
        if cmd.ABTestEnabled {
            // Deterministic assignment based on ship symbol hash
            shipHash := hashString(ship)
            useML = (shipHash % 100) < cmd.MLTrafficPct
        }

        // Tag container with scorer type
        metadata := map[string]interface{}{
            "ship_symbol":  ship,
            "good":         opp.Good(),
            "scorer_type":  ternary(useML, "ml", "ga"),  // Track which scorer used
            // ... other metadata
        }

        // Launch worker
        // ...
    }
}
```

#### Metrics Collection

**New Metrics**:

```go
// internal/adapters/metrics/arbitrage_ab_test_metrics.go

var (
    arbitrageABTestRunsTotal = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "arbitrage_ab_test_runs_total",
            Help: "Total arbitrage runs by scorer type",
        },
        []string{"scorer_type", "status"},  // ga/ml, success/failure
    )

    arbitrageABTestProfit = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "arbitrage_ab_test_profit_credits",
            Help: "Net profit distribution by scorer type",
            Buckets: []float64{100, 500, 1000, 2000, 5000, 10000, 20000},
        },
        []string{"scorer_type"},
    )

    arbitrageABTestProfitPerSecond = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "arbitrage_ab_test_profit_per_second",
            Help: "Profit per second by scorer type",
            Buckets: []float64{1, 5, 10, 20, 50, 100, 200},
        },
        []string{"scorer_type"},
    )

    arbitrageABTestDuration = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "arbitrage_ab_test_duration_seconds",
            Help: "Execution duration by scorer type",
            Buckets: []float64{30, 60, 120, 300, 600, 1200},
        },
        []string{"scorer_type"},
    )
)
```

#### Statistical Analysis

```python
# scripts/ab_test_analysis.py

import pandas as pd
from scipy import stats
import numpy as np

def analyze_ab_test(db_connection):
    """
    Analyze A/B test results from arbitrage_execution_logs.

    Compares profit_per_second between GA and ML scorer groups.
    """
    # Query logs with scorer_type metadata
    query = """
    SELECT
        container_id,
        metadata->>'scorer_type' as scorer_type,
        actual_net_profit,
        actual_duration_seconds,
        profit_per_second,
        success
    FROM arbitrage_execution_logs
    WHERE executed_at >= NOW() - INTERVAL '7 days'
      AND success = true
      AND metadata->>'scorer_type' IS NOT NULL
    """

    df = pd.read_sql(query, db_connection)

    # Split into groups
    ga_group = df[df['scorer_type'] == 'ga']['profit_per_second']
    ml_group = df[df['scorer_type'] == 'ml']['profit_per_second']

    # Summary statistics
    print("=" * 60)
    print("A/B TEST ANALYSIS")
    print("=" * 60)
    print(f"\nGA Scorer (n={len(ga_group)}):")
    print(f"  Mean:   {ga_group.mean():.4f} credits/sec")
    print(f"  Median: {ga_group.median():.4f} credits/sec")
    print(f"  Std:    {ga_group.std():.4f}")

    print(f"\nML Scorer (n={len(ml_group)}):")
    print(f"  Mean:   {ml_group.mean():.4f} credits/sec")
    print(f"  Median: {ml_group.median():.4f} credits/sec")
    print(f"  Std:    {ml_group.std():.4f}")

    # Improvement
    improvement = (ml_group.mean() - ga_group.mean()) / ga_group.mean() * 100
    print(f"\nImprovement: {improvement:+.2f}%")

    # Statistical test (Mann-Whitney U test - non-parametric)
    statistic, p_value = stats.mannwhitneyu(
        ml_group, ga_group, alternative='greater'
    )

    print(f"\nMann-Whitney U Test:")
    print(f"  Statistic: {statistic:.4f}")
    print(f"  P-value:   {p_value:.6f}")

    if p_value < 0.05:
        print(f"  ✓ ML is SIGNIFICANTLY better (p < 0.05)")
    else:
        print(f"  ✗ Difference NOT statistically significant")

    # Effect size (Cohen's d)
    pooled_std = np.sqrt((ga_group.var() + ml_group.var()) / 2)
    cohens_d = (ml_group.mean() - ga_group.mean()) / pooled_std
    print(f"\nEffect Size (Cohen's d): {cohens_d:.4f}")

    if abs(cohens_d) < 0.2:
        print("  (Small effect)")
    elif abs(cohens_d) < 0.5:
        print("  (Medium effect)")
    else:
        print("  (Large effect)")

    return {
        'ga_mean': ga_group.mean(),
        'ml_mean': ml_group.mean(),
        'improvement_pct': improvement,
        'p_value': p_value,
        'cohens_d': cohens_d,
        'significant': p_value < 0.05,
    }
```

### Graceful Fallback

```go
// internal/application/trading/services/arbitrage_opportunity_finder.go

func (f *ArbitrageOpportunityFinder) FindOpportunitiesWithFallback(
    ctx context.Context,
    systemSymbol string,
    playerID int,
    cargoCapacity int,
    minMargin float64,
    limit int,
    preferML bool,
) ([]*trading.ArbitrageOpportunity, error) {
    // ... find viable opportunities ...

    // Try ML scoring first (if preferred)
    if preferML && f.mlScorer != nil {
        referenceShip := f.getReferenceShip(ctx, playerID)

        mlScores, err := f.mlScorer.ScoreOpportunities(ctx, opportunities, referenceShip)
        if err == nil {
            // ML success
            for i, opp := range opportunities {
                opp.SetScore(mlScores[i])
            }
            logger.Log("INFO", "Using ML scores", nil)
        } else {
            // ML failed - fallback to GA
            logger.Log("WARN", fmt.Sprintf("ML scoring failed (%v), falling back to GA", err), nil)

            // Record fallback metric
            metrics.RecordMLFallback(err.Error())

            // Use GA weights
            f.scoreWithAnalyzer(opportunities)
        }
    } else {
        // GA scoring
        f.scoreWithAnalyzer(opportunities)
    }

    // Sort and return
    // ...
}
```

### Monitoring & Alerts

**Grafana Dashboard**: `configs/grafana/dashboards/arbitrage_ab_test.json`

**Panels**:
1. Profit/second time series (GA vs ML)
2. Success rate by scorer type
3. Execution duration comparison
4. ML service availability (% uptime)
5. ML service latency histogram
6. Fallback rate (ML → GA)

**Alerts**:
- ML service down >5 minutes
- ML latency >100ms (95th percentile)
- Fallback rate >10%
- ML profit/second degrades below GA

### Success Criteria for Phase 4

- [ ] A/B testing framework implemented
- [ ] Traffic splitting working (50/50 GA vs ML)
- [ ] Metrics collection operational
- [ ] Statistical analysis pipeline functional
- [ ] Graceful fallback tested (ML service outage)
- [ ] Grafana dashboard deployed
- [ ] Alerts configured
- [ ] **ML achieves statistically significant improvement (p < 0.05)**
- [ ] **ML improvement ≥15% over GA with large effect size (d > 0.5)**

---

## Phase 5: Advanced Optimization (Future)

### Ensemble Methods

**Hybrid Scoring**:

```go
// Weighted average of GA and ML
func (f *Finder) EnsembleScore(
    gaScore float64,
    mlScore float64,
    gaWeight float64,  // e.g., 0.3
) float64 {
    return gaWeight*gaScore + (1-gaWeight)*mlScore
}
```

**Benefits**:
- Combines interpretability of GA with ML's non-linear power
- Reduces risk of ML overfitting
- Smooth transition between methods

### Multi-Objective Optimization

**Pareto Frontier**:

Optimize multiple objectives simultaneously:
1. **Maximize**: Profit/time
2. **Minimize**: Risk (price volatility)
3. **Minimize**: Fuel consumption
4. **Maximize**: Fleet utilization

**Algorithm**: NSGA-II (Non-dominated Sorting Genetic Algorithm)

**Use Case**: User selects risk tolerance, system presents trade-off options

### Online Learning

**Incremental Model Updates**:

```python
# Update model with new data without full retrain
def incremental_update(model, new_data_path):
    new_X, new_y = load_data(new_data_path)

    # Continue training from checkpoint
    model = lgb.train(
        params,
        lgb.Dataset(new_X, label=new_y),
        num_boost_round=50,
        init_model=model,
    )

    return model
```

**Benefit**: Adapt to market changes faster (weekly updates instead of monthly retrains)

### Reinforcement Learning

**Problem Formulation**:
- **State**: Current ship locations, available opportunities, credits, fuel
- **Action**: Select which opportunity to pursue
- **Reward**: Actual profit/time achieved

**Algorithm**: PPO (Proximal Policy Optimization) or DQN

**Advantage**: Learns sequential decision-making (multi-step planning, not just single opportunity selection)

**Challenge**: Much more complex, requires simulation environment

### AutoML for Feature Discovery

**Use H2O AutoML or TPOT**:

```python
from tpot import TPOTRegressor

# Automatically discover best model + features
tpot = TPOTRegressor(
    generations=20,
    population_size=50,
    verbosity=2,
    random_state=42,
)

tpot.fit(X_train, y_train)

# Export optimized pipeline
tpot.export('optimized_pipeline.py')
```

**Benefit**: May discover non-obvious feature engineering or model architectures

---

## Phase 6: Cargo Quantity Optimization

### Objectives

1. Implement optimal quantity decision logic
2. Avoid market disruption from excessive purchases
3. Improve capital efficiency and cycle times
4. Test quantity strategies via A/B testing
5. Integrate quantity optimization with ML model

### Step 1: Database Schema Extensions

Extend `arbitrage_execution_logs` table:

```sql
ALTER TABLE arbitrage_execution_logs
ADD COLUMN quantity_decision_ratio DECIMAL(5, 2),  -- units_purchased / cargo_capacity
ADD COLUMN quantity_strategy VARCHAR(50),          -- 'max_capacity', 'heuristic', 'ml'
ADD COLUMN available_supply INT,                   -- Supply at buy market (if available)
ADD COLUMN estimated_demand INT;                   -- Estimated demand at sell market
```

**New Fields**:
- `quantity_decision_ratio`: 0.0-1.0 (0.5 = half capacity, 1.0 = full)
- `quantity_strategy`: Which method determined quantity
- `available_supply`: Market depth constraint data
- `estimated_demand`: Demand saturation data

### Step 2: Rule-Based Implementation

**File**: `internal/application/trading/services/quantity_optimizer.go`

```go
package services

import (
    "github.com/andrescamacho/spacetraders-go/internal/domain/market"
    "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
    "github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// QuantityOptimizer calculates optimal purchase quantities
type QuantityOptimizer struct {
    supplyFactors   map[string]float64
    activityFactors map[string]float64
}

func NewQuantityOptimizer() *QuantityOptimizer {
    return &QuantityOptimizer{
        supplyFactors: map[string]float64{
            "ABUNDANT": 1.0,
            "HIGH":     0.8,
            "MODERATE": 0.6,
            "LIMITED":  0.4,
            "SCARCE":   0.2,
        },
        activityFactors: map[string]float64{
            "STRONG":     1.0,
            "GROWING":    0.8,
            "WEAK":       0.6,
            "RESTRICTED": 0.3,
        },
    }
}

func (o *QuantityOptimizer) CalculateOptimalQuantity(
    opportunity *trading.ArbitrageOpportunity,
    ship *navigation.Ship,
    buyMarketData *market.Market,
    sellMarketData *market.Market,
    playerCredits int,
) int {
    maxCapacity := ship.Cargo().AvailableCapacity()

    // Hard constraints
    constraints := []int{maxCapacity}

    // Constraint: Available supply
    if tradeGood := buyMarketData.FindGood(opportunity.Good()); tradeGood != nil {
        if volume := tradeGood.TradeVolume(); volume > 0 {
            constraints = append(constraints, volume)
        }
    }

    // Constraint: Player capital
    buyPrice := opportunity.BuyPrice()
    if buyPrice > 0 {
        maxByCapital := playerCredits / buyPrice
        constraints = append(constraints, maxByCapital)
    }

    // Find minimum constraint
    hardLimit := maxCapacity
    for _, constraint := range constraints {
        if constraint < hardLimit {
            hardLimit = constraint
        }
    }

    // Apply soft adjustments
    supplyFactor := o.getSupplyFactor(opportunity.BuySupply())
    activityFactor := o.getActivityFactor(opportunity.SellActivity())

    adjustedQty := float64(hardLimit) * supplyFactor * activityFactor

    // Round and ensure at least 1 unit
    finalQty := int(adjustedQty)
    if finalQty < 1 {
        finalQty = 1
    }

    return finalQty
}

func (o *QuantityOptimizer) getSupplyFactor(supply string) float64 {
    if factor, ok := o.supplyFactors[supply]; ok {
        return factor
    }
    return 0.5 // Default conservative
}

func (o *QuantityOptimizer) getActivityFactor(activity string) float64 {
    if factor, ok := o.activityFactors[activity]; ok {
        return factor
    }
    return 0.7 // Default moderate
}
```

### Step 3: Integration with ArbitrageExecutor

**Modify**: `internal/application/trading/services/arbitrage_executor.go`

```go
type ArbitrageExecutor struct {
    mediator          common.Mediator
    shipRepo          navigation.ShipRepository
    logRepo           trading.ArbitrageExecutionLogRepository
    marketRepo        market.MarketRepository  // NEW
    quantityOptimizer *QuantityOptimizer       // NEW
}

func NewArbitrageExecutor(
    mediator common.Mediator,
    shipRepo navigation.ShipRepository,
    logRepo trading.ArbitrageExecutionLogRepository,
    marketRepo market.MarketRepository,
    quantityOptimizer *QuantityOptimizer,
) *ArbitrageExecutor {
    return &ArbitrageExecutor{
        mediator:          mediator,
        shipRepo:          shipRepo,
        logRepo:           logRepo,
        marketRepo:        marketRepo,
        quantityOptimizer: quantityOptimizer,
    }
}

func (e *ArbitrageExecutor) Execute(
    ctx context.Context,
    ship *navigation.Ship,
    opportunity *trading.ArbitrageOpportunity,
    playerID int,
    containerID string,
) (*ArbitrageResult, error) {
    // ... existing navigation and docking logic ...

    // NEW: Calculate optimal quantity
    buyMarketData, err := e.marketRepo.GetMarketData(
        ctx, opportunity.BuyMarket().Symbol, playerID,
    )
    if err != nil {
        return nil, fmt.Errorf("failed to get buy market data: %w", err)
    }

    sellMarketData, err := e.marketRepo.GetMarketData(
        ctx, opportunity.SellMarket().Symbol, playerID,
    )
    if err != nil {
        return nil, fmt.Errorf("failed to get sell market data: %w", err)
    }

    // Get player credits
    player, err := e.playerRepo.FindByID(ctx, playerID)
    if err != nil {
        return nil, fmt.Errorf("failed to get player: %w", err)
    }

    // Calculate optimal quantity
    optimalQty := e.quantityOptimizer.CalculateOptimalQuantity(
        opportunity,
        ship,
        buyMarketData,
        sellMarketData,
        player.Credits(),
    )

    logger.Log("INFO", fmt.Sprintf(
        "Quantity decision: %d units (%.0f%% of %d capacity)",
        optimalQty,
        float64(optimalQty)/float64(ship.Cargo().AvailableCapacity())*100,
        ship.Cargo().AvailableCapacity(),
    ), nil)

    // OLD: Units: ship.Cargo().AvailableCapacity()
    // NEW:
    purchaseResp, err := e.mediator.Send(ctx, &shipCmd.PurchaseCargoCommand{
        ShipSymbol: ship.ShipSymbol(),
        GoodSymbol: opportunity.Good(),
        Units:      optimalQty,  // Use optimized quantity
        PlayerID:   playerIDValue,
    })

    // ... rest of execution logic ...
}
```

### Step 4: A/B Testing Framework

```go
// internal/application/trading/commands/run_arbitrage_coordinator.go

type QuantityStrategy string

const (
    QuantityStrategyMaxCapacity QuantityStrategy = "max_capacity"
    QuantityStrategyHeuristic   QuantityStrategy = "heuristic"
    QuantityStrategyML          QuantityStrategy = "ml"
)

type RunArbitrageCoordinatorCommand struct {
    // ... existing fields ...

    // NEW: Quantity optimization config
    QuantityABTestEnabled bool
    QuantityStrategyPcts  map[QuantityStrategy]int  // e.g., {"max_capacity": 60, "heuristic": 40}
}

func (h *RunArbitrageCoordinatorHandler) assignQuantityStrategy(
    shipSymbol string,
    config *RunArbitrageCoordinatorCommand,
) QuantityStrategy {
    if !config.QuantityABTestEnabled {
        return QuantityStrategyHeuristic  // Default
    }

    // Deterministic assignment based on ship hash
    shipHash := hashString(shipSymbol) % 100

    cumulative := 0
    for strategy, pct := range config.QuantityStrategyPcts {
        cumulative += pct
        if shipHash < cumulative {
            return strategy
        }
    }

    return QuantityStrategyHeuristic  // Fallback
}
```

### Step 5: ML Integration

**Extend ML Features** (Phase 3):

```python
# Add quantity-related features to ML model

def extract_features(logs_df: pd.DataFrame) -> pd.DataFrame:
    features = pd.DataFrame()

    # ... existing features ...

    # NEW: Quantity features
    features['quantity_purchased'] = logs_df['units_purchased']
    features['quantity_ratio'] = (
        logs_df['units_purchased'] / logs_df['cargo_capacity']
    )
    features['cargo_utilization'] = logs_df['cargo_used'] / logs_df['cargo_capacity']

    # Quantity × margin interaction
    features['quantity_margin_product'] = (
        features['quantity_ratio'] * logs_df['profit_margin']
    )

    return features
```

**Grid Search for Optimal Quantity**:

```python
# services/ml-service/server.py

@app.post("/optimize_quantity")
async def optimize_quantity(request: OpportunityFeatures):
    """
    Find optimal quantity by grid search over predictions.

    Returns:
        Optimal quantity (1-40) that maximizes predicted profit/time
    """
    best_qty = 1
    best_score = 0.0

    base_features = extract_features(pd.DataFrame([request.dict()]))

    for qty in range(1, request.cargo_capacity + 1):
        # Add quantity features
        features_with_qty = base_features.copy()
        features_with_qty['quantity_purchased'] = qty
        features_with_qty['quantity_ratio'] = qty / request.cargo_capacity

        # Predict profit/time for this quantity
        predicted_score = model.predict(features_with_qty)[0]

        if predicted_score > best_score:
            best_score = predicted_score
            best_qty = qty

    return {
        "optimal_quantity": best_qty,
        "predicted_profit_per_second": best_score,
        "quantity_ratio": best_qty / request.cargo_capacity
    }
```

### Step 6: Metrics & Monitoring

**New Metrics**:

```go
var (
    arbitrageQuantityDecisions = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "arbitrage_quantity_decisions_ratio",
            Help:    "Quantity decision as ratio of cargo capacity",
            Buckets: []float64{0.2, 0.4, 0.6, 0.8, 1.0},
        },
        []string{"strategy", "supply_level", "activity_level"},
    )

    arbitrageQuantityProfitPerSecond = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "arbitrage_quantity_profit_per_second",
            Help:    "Profit per second by quantity strategy",
            Buckets: []float64{1, 5, 10, 20, 50, 100},
        },
        []string{"strategy"},
    )
)
```

### Step 7: Testing Strategy

**BDD Test**: `test/bdd/features/application/trading/quantity_optimizer.feature`

```gherkin
Feature: Cargo Quantity Optimization

  Scenario: Calculate optimal quantity with abundant supply and strong demand
    Given ship with 40 units cargo capacity
    And arbitrage opportunity with HIGH supply and STRONG activity
    And available supply 100 units
    And player has 10,000 credits
    When I calculate optimal quantity
    Then quantity should be 32 units
    # 40 × 0.8 (HIGH) × 1.0 (STRONG) = 32

  Scenario: Respect capital constraint
    Given ship with 40 units cargo capacity
    And arbitrage opportunity with buy price 500 credits/unit
    And player has 10,000 credits
    When I calculate optimal quantity
    Then quantity should be 20 units
    # 10,000 / 500 = 20 (capital limited)

  Scenario: Respect supply constraint
    Given ship with 40 units cargo capacity
    And arbitrage opportunity with LIMITED supply
    And available supply 15 units
    When I calculate optimal quantity
    Then quantity should be at most 15 units
    # Cannot buy more than available

  Scenario: Conservative quantity for scarce supply
    Given ship with 40 units cargo capacity
    And arbitrage opportunity with SCARCE supply
    When I calculate optimal quantity
    Then quantity should be 8 units
    # 40 × 0.2 (SCARCE factor) = 8
```

### Success Criteria for Phase 6

- [ ] `QuantityOptimizer` service implemented and tested
- [ ] Database schema extended with quantity fields
- [ ] Integration with `ArbitrageExecutor` complete
- [ ] A/B testing framework operational
- [ ] Quantity strategies tracked in metrics
- [ ] BDD tests passing
- [ ] **Heuristic strategy achieves ≥5% improvement in thin markets**
- [ ] **ML quantity optimization achieves ≥8% improvement overall**
- [ ] No increase in failed transactions due to insufficient supply

### Expected Outcomes

**Thin Markets** (LIMITED/SCARCE supply, WEAK/RESTRICTED demand):
- Baseline: 30% failure rate or poor prices
- Optimized: 10% failure rate, better average prices
- **Improvement: +15-25%** in these markets

**Liquid Markets** (ABUNDANT supply, STRONG demand):
- Baseline: Works well at max capacity
- Optimized: Similar performance, ±5%
- **Improvement: Neutral to slight positive**

**Overall Fleet Performance**:
- **Average improvement: +5-10%** across all markets
- Reduced transaction failures
- Better capital utilization
- More consistent profit/time

---

## Architecture

### Overall System Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                   Arbitrage Coordinator                      │
│                    (Go Application)                          │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  ArbitrageOpportunityFinder                          │   │
│  │  - Scan markets                                      │   │
│  │  - Create opportunities                              │   │
│  │  - Score opportunities (GA or ML)                    │   │
│  └──────────────────────────────────────────────────────┘   │
│                          │                                   │
│          ┌───────────────┴────────────────┐                 │
│          ▼                                 ▼                 │
│  ┌──────────────┐                 ┌─────────────────┐       │
│  │ GA Scorer    │                 │ ML Scorer       │       │
│  │ (In-process) │                 │ (HTTP client)   │       │
│  └──────────────┘                 └─────────────────┘       │
│          │                                 │                 │
│          │                                 │ HTTP            │
│          │                                 ▼                 │
│          │                     ┌─────────────────────────┐   │
│          │                     │   Python ML Service     │   │
│          │                     │   - FastAPI             │   │
│          │                     │   - LightGBM model      │   │
│          │                     │   - Feature extraction  │   │
│          │                     └─────────────────────────┘   │
│          │                                                   │
│          ▼                                                   │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  Ranked Opportunities                                │   │
│  │  [Opp1, Opp2, Opp3, ...]                            │   │
│  └──────────────────────────────────────────────────────┘   │
│                          │                                   │
│                          ▼                                   │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  Spawn Parallel Workers                              │   │
│  │  - Assign ships                                      │   │
│  │  - Execute trades                                    │   │
│  │  - Log results                                       │   │
│  └──────────────────────────────────────────────────────┘   │
│                          │                                   │
└──────────────────────────┼───────────────────────────────────┘
                           ▼
               ┌──────────────────────────┐
               │  PostgreSQL Database     │
               │  - arbitrage_exec_logs   │
               │  - transactions          │
               └──────────────────────────┘
                           │
                           ▼
               ┌──────────────────────────┐
               │  Offline Analytics       │
               │  - CSV export            │
               │  - Model training        │
               │  - GA evolution          │
               │  - A/B analysis          │
               └──────────────────────────┘
```

### Data Flow Architecture

```
┌────────────────────────────────────────────────────────────┐
│                   DATA COLLECTION PHASE                     │
└────────────────────────────────────────────────────────────┘
                           │
                           ▼
    ┌─────────────────────────────────────────┐
    │  Arbitrage Workers Execute Trades       │
    │  (10 ships × 4 runs/hour = 40 runs/hr) │
    └─────────────────────────────────────────┘
                           │
                           ▼
    ┌─────────────────────────────────────────┐
    │  Log Results to Database                │
    │  - Opportunity features                 │
    │  - Execution outcomes                   │
    │  - Profit/time metrics                  │
    └─────────────────────────────────────────┘
                           │
                           ▼
    ┌─────────────────────────────────────────┐
    │  Accumulate Training Data               │
    │  Target: 100+ for GA, 1000+ for ML      │
    └─────────────────────────────────────────┘
                           │
           ┌───────────────┴────────────────┐
           ▼                                ▼
┌─────────────────────┐        ┌────────────────────────┐
│  GENETIC ALGORITHM  │        │  MACHINE LEARNING      │
└─────────────────────┘        └────────────────────────┘
           │                                │
           ▼                                ▼
┌─────────────────────┐        ┌────────────────────────┐
│  Evolve Weights     │        │  Export CSV            │
│  (100 generations)  │        │  Train XGBoost         │
│  Simulate fitness   │        │  Evaluate on test set  │
└─────────────────────┘        └────────────────────────┘
           │                                │
           ▼                                ▼
┌─────────────────────┐        ┌────────────────────────┐
│  Export Config      │        │  Deploy ML Service     │
│  weights.yaml       │        │  FastAPI + Docker      │
└─────────────────────┘        └────────────────────────┘
           │                                │
           └────────────┬───────────────────┘
                        ▼
           ┌─────────────────────────┐
           │  A/B TESTING PHASE      │
           │  - 50% GA, 50% ML       │
           │  - Collect metrics      │
           │  - Statistical analysis │
           └─────────────────────────┘
                        │
                        ▼
           ┌─────────────────────────┐
           │  PRODUCTION ROLLOUT     │
           │  - Winner takes all     │
           │  - Monitor performance  │
           └─────────────────────────┘
```

---

## Data Models

### Database Schema

See [Phase 1: Database Schema](#new-table-arbitrage_execution_logs) for complete table definition.

### Feature Vector Schema

**ML Training Data Format** (CSV):

```csv
good,buy_market,sell_market,buy_price,sell_price,profit_margin,distance,buy_supply,sell_activity,cargo_capacity,fuel_current,actual_net_profit,actual_duration,profit_per_second
IRON_ORE,X1-A1-M1,X1-A1-M5,100,130,30.0,45.0,ABUNDANT,STRONG,40,200,1150,87,13.22
PRECIOUS_STONES,X1-A1-M2,X1-A1-M6,500,750,50.0,120.0,HIGH,GROWING,40,180,9800,235,41.70
...
```

**28 ML Features** (extracted from above + derived):

See [Phase 3: Feature Engineering](#feature-categories-28-total-features) for complete feature list.

### Genome Schema

**Genetic Algorithm Chromosome**:

```json
{
  "genes": [42.3, 18.7, 21.5, 0.12],
  "fitness": 14.52,
  "generation": 100
}
```

**Configuration File** (YAML):

```yaml
version: 2
evolved_at: "2025-11-23T14:30:00Z"
training_examples: 1247
baseline_fitness: 12.34
evolved_fitness: 14.52
improvement_percent: 17.6

weights:
  profit_weight: 42.3
  supply_weight: 18.7
  activity_weight: 21.5
  distance_penalty: 0.12
```

---

## Success Criteria

### Phase 1: Data Collection

- [ ] Database infrastructure deployed
- [ ] ≥100 successful execution logs (for GA)
- [ ] ≥1000 successful execution logs (for ML)
- [ ] Data quality validation passing (>95% complete records)
- [ ] CSV export functional

### Phase 2: Genetic Algorithm

- [ ] GA implementation complete and tested
- [ ] Evolved weights achieve ≥10% fitness improvement over baseline
- [ ] Configuration export/import working
- [ ] Weights deployed to production coordinator
- [ ] Profit/time improvement visible in production metrics

### Phase 3: Machine Learning

- [ ] Feature engineering implemented (28 features)
- [ ] XGBoost model trained with MAE <2.0
- [ ] Python ML service deployed and healthy
- [ ] Go ML scorer integration functional
- [ ] Latency <50ms (95th percentile)
- [ ] Graceful fallback tested

### Phase 4: A/B Testing

- [ ] A/B framework operational (50/50 traffic split)
- [ ] Statistical analysis shows ML significantly better (p < 0.05)
- [ ] ML achieves ≥15% improvement over GA
- [ ] Effect size large (Cohen's d > 0.5)
- [ ] Production monitoring stable

### Phase 5: Advanced (Optional)

- [ ] Ensemble scoring explored
- [ ] Multi-objective optimization prototype
- [ ] Online learning pipeline functional

### Overall Success

**Primary Objective**: Maximize profit/time

**Success Thresholds**:
- **Minimum Success**: +10% profit/time improvement (GA only)
- **Good Success**: +20% profit/time improvement (ML deployed)
- **Excellent Success**: +30% profit/time improvement (ML + ensemble)

**Fleet-Wide Impact** (10 ships):
- Baseline: 5,000 credits/minute
- +20% improvement: 6,000 credits/minute = **+1,000 credits/minute**
- Over 24 hours: **+1.44M credits/day**

---

## Risk Analysis

### Technical Risks

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| **Insufficient training data** | Medium | High | Use parallel ships to accelerate collection (40 examples/hour) |
| **ML service outage** | Medium | Medium | Graceful fallback to GA weights, health monitoring |
| **Model overfitting** | Medium | High | Cross-validation, regularization, holdout set evaluation |
| **Concept drift** | High | Medium | Monthly retraining, monitor model performance metrics |
| **Latency degradation** | Low | Medium | Batch scoring, caching, timeout handling |
| **Infrastructure complexity** | Medium | Low | Docker containerization, clear deployment docs |
| **GA local optima** | Medium | Low | Multiple evolution runs, diversity preservation |

### Operational Risks

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| **Market dynamics change** | High | Medium | Continuous monitoring, periodic retraining/re-evolution |
| **Incorrect feature engineering** | Low | High | Domain expert review, feature importance analysis |
| **A/B test inconclusive** | Medium | Medium | Ensure sufficient sample size (power analysis) |
| **Production rollout issues** | Low | High | Staged rollout (10% → 50% → 100%), rollback plan |
| **Data quality degradation** | Medium | Medium | Automated validation, alerts on anomalies |

### Mitigation Strategies

1. **Data Quality**:
   - Automated validation on log insertion
   - Outlier detection
   - Completeness checks

2. **Model Reliability**:
   - Cross-validation during training
   - Holdout set evaluation
   - A/B testing before full rollout
   - Fallback mechanisms

3. **Operational Resilience**:
   - Health checks on ML service
   - Graceful degradation (ML → GA → baseline)
   - Monitoring and alerting
   - Circuit breaker pattern for ML service calls

4. **Continuous Improvement**:
   - Monthly model retraining
   - Weekly GA re-evolution (if using GA)
   - Feature drift monitoring
   - Performance regression detection

---

## Monitoring & Observability

### Key Metrics

**Performance Metrics**:
- `arbitrage_profit_per_second_by_scorer{scorer_type="ga|ml"}` - Primary objective
- `arbitrage_net_profit_by_scorer{scorer_type="ga|ml"}` - Absolute profit
- `arbitrage_execution_duration_by_scorer{scorer_type="ga|ml"}` - Cycle time

**ML Service Metrics**:
- `ml_service_latency_ms` - Inference latency
- `ml_service_availability_pct` - Uptime percentage
- `ml_service_error_rate` - Request failure rate
- `ml_fallback_rate` - % of requests falling back to GA

**Data Quality Metrics**:
- `execution_logs_collected_total` - Data accumulation rate
- `execution_logs_invalid_total` - Data quality issues
- `execution_success_rate_by_scorer` - Operational reliability

**A/B Testing Metrics**:
- `ab_test_sample_size{scorer_type="ga|ml"}` - Statistical power
- `ab_test_confidence_interval` - Result precision
- `ab_test_p_value` - Statistical significance

### Dashboards

**1. Optimization Performance Dashboard**:
- Profit/time comparison (GA vs ML vs baseline)
- Success rate trends
- Execution duration distributions
- Opportunity selection patterns

**2. ML Service Health Dashboard**:
- Request rate and latency
- Error rate and types
- Feature importance (top 10)
- Model version deployed
- Fallback event timeline

**3. A/B Test Dashboard**:
- Real-time profit/time comparison
- Sample size tracker
- Statistical significance indicator
- Effect size visualization
- Distribution comparisons (histograms)

### Alerts

**Critical**:
- ML service down >5 minutes
- Fallback rate >20%
- ML profit/time <50% of GA (regression)

**Warning**:
- ML latency >100ms (p95)
- Data collection stopped
- Model not retrained in 30 days
- A/B test sample size <100 per group

**Info**:
- New model deployed
- A/B test started/completed
- GA weights updated

---

## Implementation Checklist

### Phase 1: Data Collection

- [ ] Create `arbitrage_execution_logs` table
- [ ] Implement `ArbitrageExecutionLog` domain model
- [ ] Implement `ArbitrageExecutionLogRepository`
- [ ] Integrate logging in `ArbitrageExecutor`
- [ ] Add CSV export utility
- [ ] Create CLI command `arbitrage export-data`
- [ ] Validate data quality checks
- [ ] Deploy to production
- [ ] Collect ≥100 examples (GA threshold)
- [ ] Collect ≥1000 examples (ML threshold)

### Phase 2: Genetic Algorithm

- [ ] Implement `ScoringGenome` struct
- [ ] Implement `GeneticEvolver` service
- [ ] Implement fitness function (simulation-based)
- [ ] Implement selection operator (tournament)
- [ ] Implement crossover operator (uniform)
- [ ] Implement mutation operator (Gaussian)
- [ ] Create YAML config export
- [ ] Create CLI command `arbitrage evolve`
- [ ] Write BDD tests
- [ ] Run evolution on production data
- [ ] Validate ≥10% improvement
- [ ] Deploy evolved weights
- [ ] Monitor production performance

### Phase 3: Machine Learning

- [ ] Implement feature extraction (Python)
- [ ] Implement model training pipeline
- [ ] Train XGBoost model
- [ ] Validate MAE <2.0 on test set
- [ ] Create FastAPI service
- [ ] Write Dockerfile
- [ ] Deploy ML service (Docker Compose)
- [ ] Implement `MLOpportunityScorer` (Go)
- [ ] Integrate with `ArbitrageOpportunityFinder`
- [ ] Test HTTP integration
- [ ] Validate latency <50ms
- [ ] Test graceful fallback

### Phase 4: A/B Testing

- [ ] Implement traffic splitting logic
- [ ] Add scorer type tagging
- [ ] Deploy A/B test metrics
- [ ] Create Grafana dashboard
- [ ] Configure alerts
- [ ] Start A/B test (50/50 split)
- [ ] Collect ≥200 samples per group
- [ ] Run statistical analysis
- [ ] Validate p < 0.05 significance
- [ ] Validate ≥15% improvement
- [ ] Roll out winner to 100%

### Phase 5: Advanced (Future)

- [ ] Explore ensemble methods
- [ ] Prototype multi-objective optimization
- [ ] Design online learning pipeline
- [ ] Research reinforcement learning approach

### Phase 6: Cargo Quantity Optimization

- [ ] Extend `arbitrage_execution_logs` table with quantity fields
- [ ] Implement `QuantityOptimizer` service
- [ ] Add supply/demand adjustment factors
- [ ] Integrate with `ArbitrageExecutor`
- [ ] Add player credits fetching for capital constraint
- [ ] Implement quantity strategy A/B testing framework
- [ ] Deploy quantity decision metrics
- [ ] Create BDD tests for `QuantityOptimizer`
- [ ] Test with different market conditions (SCARCE, ABUNDANT, etc.)
- [ ] Add quantity features to ML model (Phase 3 extension)
- [ ] Implement ML grid search for optimal quantity
- [ ] Monitor transaction failure rates
- [ ] Validate ≥5% improvement in thin markets
- [ ] Validate no increase in failed transactions

---

## Conclusion

This plan provides a comprehensive roadmap for optimizing the arbitrage operation through data-driven techniques. The phased approach balances risk, complexity, and reward:

1. **Phase 1** establishes data collection infrastructure (critical foundation)
2. **Phase 2** delivers quick wins via genetic algorithms (low risk, proven value)
3. **Phase 3** unlocks maximum performance via machine learning (higher complexity, higher reward)
4. **Phase 4** validates improvements through rigorous A/B testing (evidence-based decisions)
5. **Phase 5** explores advanced techniques for future optimization
6. **Phase 6** optimizes cargo quantities to avoid market disruption and improve capital efficiency

**Key Advantages**:
- **Parallel data collection** accelerates time-to-value
- **Low-risk start** with genetic algorithms proves optimization viability
- **Graceful fallbacks** ensure production reliability
- **Statistical rigor** via A/B testing validates improvements
- **Maintainable architecture** with clear separation of concerns

**Expected Outcomes**:
- **Short term** (Phase 2): +10-15% profit/time improvement via GA
- **Medium term** (Phase 3-4): +20-30% profit/time improvement via ML
- **Cargo optimization** (Phase 6): +5-10% overall, +15-25% in thin markets
- **Long term** (Phase 5): +30%+ via ensemble and advanced techniques
- **Combined impact**: Multiplicative gains across all optimizations

**Investment**: The effort is justified by the compounding nature of profit optimization - small percentage improvements lead to exponential growth when reinvested in fleet expansion.

---

**End of Implementation Plan**

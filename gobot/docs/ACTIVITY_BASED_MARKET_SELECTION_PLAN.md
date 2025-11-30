# Activity-Based Market Selection Implementation Plan

## Overview

This document outlines the implementation of activity-based market selection optimizations based on data analysis of 30,493 market price records over 6 days of manufacturing operations.

## Analysis Summary

### Key Finding: Market Activity Predicts Pricing

Statistical analysis revealed that market activity level (WEAK/GROWING/STRONG/RESTRICTED) significantly correlates with pricing:

- **ANOVA F-statistic**: 7.45
- **P-value**: 0.000005 (highly significant)
- **Chi-square test**: Activity and Supply are NOT independent (p < 0.0001)

### Activity-Price Relationship

#### At IMPORT Markets (Where We SELL)

| Activity | Avg Purchase Price | Recommendation |
|----------|-------------------|----------------|
| STRONG | 7,551 | **BEST** - Highest prices |
| GROWING | 2,968 | Good |
| WEAK | 1,797 | Avoid |
| RESTRICTED | 1,480 | Worst |

#### At EXPORT Markets (Where We BUY)

| Activity + Supply | Avg Sell Price | Recommendation |
|-------------------|----------------|----------------|
| WEAK + ABUNDANT | 43 | **BEST** - Lowest prices |
| GROWING + MODERATE | 46 | Good |
| RESTRICTED + ABUNDANT | 6,863 | Worst - 159x more expensive |

### Causal Mechanism

- **STRONG activity at IMPORT** = High demand = High prices (good for selling)
- **WEAK activity at EXPORT** = Low demand = Low prices (good for buying)

## Implementation

### Optimization 1: Opportunity Finder - Buyer Selection

**File**: `internal/application/trading/services/collection_opportunity_finder.go`

#### Current Logic (Lines 186-202)

```go
// Current: Only accepts SCARCE or (LIMITED + WEAK/RESTRICTED)
if tradeGood.TradeType() == market.TradeTypeImport {
    isBuyer := false
    if supply == "SCARCE" {
        isBuyer = true
    } else if supply == "LIMITED" && (activity == "WEAK" || activity == "RESTRICTED") {
        isBuyer = true
    }
    // ...
}
```

**Problem**: This logic EXCLUDES high-activity markets which have the HIGHEST prices.

#### Proposed Changes

##### 1. Update `CollectionOpportunity` struct (Lines 15-23)

```go
type CollectionOpportunity struct {
    Good               string
    FactorySymbol      string
    SellMarket         string
    FactorySupply      string
    FactoryActivity    string // NEW: Track factory activity
    SellMarketSupply   string // NEW: Track sell market supply
    SellMarketActivity string // NEW: Track sell market activity
    SellPrice          int
    BuyPrice           int
    ExpectedProfit     int
}
```

##### 2. Update `Score()` method (Lines 27-37)

```go
func (o *CollectionOpportunity) Score() int {
    score := o.ExpectedProfit

    // Bonus for ABUNDANT factory supply (reliable source)
    if o.FactorySupply == "ABUNDANT" {
        score += 100
    }

    // NEW: Activity-based bonus for sell market
    // STRONG activity = highest prices at IMPORT markets
    switch o.SellMarketActivity {
    case "STRONG":
        score += 500
    case "GROWING":
        score += 300
    case "WEAK":
        score += 100
    case "RESTRICTED":
        score += 0
    }

    // NEW: Penalty for low-activity factory (higher buy prices)
    // WEAK activity = lowest prices at EXPORT markets
    switch o.FactoryActivity {
    case "WEAK":
        score += 200 // Best for buying
    case "GROWING":
        score += 100
    case "STRONG":
        score += 50
    case "RESTRICTED":
        score += 0 // Worst for buying
    }

    return score
}
```

##### 3. Update buyer selection logic (Lines 182-203)

```go
// Check if this is a buyer (IMPORT market)
// Accept ALL import markets, let scoring differentiate
if tradeGood.TradeType() == market.TradeTypeImport {
    buyerIndex[goodSymbol] = append(buyerIndex[goodSymbol], &buyerEntry{
        waypointSymbol: waypointSymbol,
        supply:         supply,
        activity:       activity,
        purchasePrice:  tradeGood.PurchasePrice(),
    })
}
```

##### 4. Capture activity when creating opportunity (Lines 267-275)

```go
opp := &CollectionOpportunity{
    Good:               good,
    FactorySymbol:      factory.waypointSymbol,
    SellMarket:         buyer.waypointSymbol,
    FactorySupply:      factory.supply,
    FactoryActivity:    factory.activity,    // NEW
    SellMarketSupply:   buyer.supply,        // NEW
    SellMarketActivity: buyer.activity,      // NEW
    SellPrice:          buyer.purchasePrice,
    BuyPrice:           factory.sellPrice,
    ExpectedProfit:     profit,
}
```

##### 5. Update `factoryEntry` struct to include activity

```go
type factoryEntry struct {
    waypointSymbol string
    supply         string
    activity       string // NEW
    sellPrice      int
}
```

---

### Optimization 2: Export Market Selection (Pipeline Planner)

**File**: `internal/application/trading/services/pipeline_planner.go`

#### Current Logic

The pipeline planner selects source markets using `FindExportMarketBySupplyPriority()` which only considers supply level (ABUNDANT > HIGH > MODERATE).

#### Proposed Changes

##### 1. Add activity preference to market selection

Create a new method or modify existing logic to consider activity:

```go
// ActivityScore returns a score for market selection based on activity.
// For EXPORT markets (buying), lower activity = lower prices = better.
// For IMPORT markets (selling), higher activity = higher prices = better.
func ActivityScore(activity string, isExport bool) int {
    if isExport {
        // For buying: WEAK is best (lowest prices)
        switch activity {
        case "WEAK":
            return 4
        case "GROWING":
            return 3
        case "STRONG":
            return 2
        case "RESTRICTED":
            return 1
        default:
            return 0
        }
    }
    // For selling: STRONG is best (highest prices)
    switch activity {
    case "STRONG":
        return 4
    case "GROWING":
        return 3
    case "WEAK":
        return 2
    case "RESTRICTED":
        return 1
    default:
        return 0
    }
}
```

##### 2. Update `FindExportMarketBySupplyPriority` or create new method

**Option A**: Add activity as secondary sort criterion

```go
func (r *MarketRepository) FindBestExportMarket(
    ctx context.Context,
    good string,
    playerID int,
) (*Market, error) {
    markets, err := r.FindExportMarketsForGood(ctx, good, playerID)
    if err != nil {
        return nil, err
    }

    // Sort by: Supply priority DESC, then Activity score DESC (for buying, WEAK is best)
    sort.Slice(markets, func(i, j int) bool {
        supplyI := supplyPriority(markets[i].Supply)
        supplyJ := supplyPriority(markets[j].Supply)
        if supplyI != supplyJ {
            return supplyI > supplyJ
        }
        // Secondary: Activity (WEAK best for buying)
        return ActivityScore(markets[i].Activity, true) > ActivityScore(markets[j].Activity, true)
    })

    if len(markets) > 0 {
        return markets[0], nil
    }
    return nil, nil
}
```

**Option B**: Composite scoring

```go
func marketScore(m *Market, isExport bool) int {
    // Supply score (0-400)
    supplyScore := map[string]int{
        "ABUNDANT": 400,
        "HIGH":     300,
        "MODERATE": 200,
        "LIMITED":  100,
        "SCARCE":   0,
    }[m.Supply]

    // Activity score (0-100)
    activityScore := ActivityScore(m.Activity, isExport) * 25

    return supplyScore + activityScore
}
```

---

### Optimization 3: Storage Opportunity Finder

**File**: `internal/application/trading/services/collection_opportunity_finder.go`

**Method**: `FindStorageOpportunities()` (Lines 445-546)

#### Current Logic (Lines 517-522)

```go
// Find best buyer (highest purchase price)
var bestBuyer *buyerEntry
for _, buyer := range buyers {
    if bestBuyer == nil || buyer.purchasePrice > bestBuyer.purchasePrice {
        bestBuyer = buyer
    }
}
```

#### Proposed Change

Add activity as tie-breaker when prices are similar:

```go
var bestBuyer *buyerEntry
for _, buyer := range buyers {
    if bestBuyer == nil {
        bestBuyer = buyer
        continue
    }

    // Primary: Highest price
    if buyer.purchasePrice > bestBuyer.purchasePrice {
        bestBuyer = buyer
        continue
    }

    // Secondary: STRONG activity preferred (prices likely to stay high)
    if buyer.purchasePrice == bestBuyer.purchasePrice {
        if ActivityScore(buyer.activity, false) > ActivityScore(bestBuyer.activity, false) {
            bestBuyer = buyer
        }
    }
}
```

---

## Files to Modify

| File | Changes |
|------|---------|
| `collection_opportunity_finder.go` | Update struct, Score(), buyer selection, factory entry |
| `pipeline_planner.go` | Add activity preference to market selection |
| `market_repository.go` | (Optional) Add activity-aware query methods |

## Testing

### Unit Tests

1. Test `Score()` returns higher values for STRONG sell markets
2. Test buyer selection includes all IMPORT markets regardless of activity
3. Test factory selection prefers WEAK activity markets

### Integration Tests

1. Run opportunity finder and verify STRONG activity markets rank higher
2. Verify pipeline planner selects lower-priced EXPORT markets

### Validation

After implementation, run the analysis script again to verify:
1. Average sell prices increase (more STRONG activity markets selected)
2. Average buy prices decrease (more WEAK activity markets selected)

## Expected Impact

Based on the data analysis:

| Metric | Current | Expected |
|--------|---------|----------|
| Avg sell price (IMPORT) | ~2,400 | ~5,000+ |
| Avg buy price (EXPORT) | Variable | Lower (WEAK preferred) |
| Profit margin | Baseline | +10-30% improvement |

## Rollback Plan

All changes are additive (new fields, modified scoring). To rollback:
1. Revert `Score()` to original logic (profit + ABUNDANT bonus only)
2. Restore buyer filter (SCARCE or LIMITED+WEAK/RESTRICTED)
3. Remove activity fields from struct (optional, unused fields are harmless)

## Implementation Order

1. **Phase 1**: Update `CollectionOpportunity` struct with new fields
2. **Phase 2**: Update `Score()` method with activity bonuses
3. **Phase 3**: Remove restrictive buyer filter
4. **Phase 4**: Update pipeline planner market selection
5. **Phase 5**: Update storage opportunity finder
6. **Phase 6**: Run validation analysis

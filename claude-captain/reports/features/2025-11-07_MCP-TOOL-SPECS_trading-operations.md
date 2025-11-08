# MCP Tool Specifications: Trading Operations

**Document Purpose:** Define exact requirements for MCP tools needed by Trading Coordinator specialist

**Priority:** HIGH (blocks Trading Coordinator implementation)
**Timeline:** 6-9 hours total development

---

## Overview

Three MCP tools are required to enable autonomous trading operations:

1. **scout_market_analysis** - Query scout-collected prices and identify arbitrage
2. **purchase_market_good** - Buy goods at a marketplace
3. **sell_market_good** - Sell goods at a marketplace

---

## Tool 1: Scout Market Analysis

### Purpose
Query market prices collected by scout network and return ranked trading opportunities.

### Signature
```
scout_market_analysis(
    system: str,
    min_margin: int = 500,
    trade_good: Optional[str] = None,
    limit: int = 20
) -> ScoutMarketAnalysisResult
```

### Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| system | string | YES | - | System code (e.g., "X1-HZ85") |
| min_margin | integer | NO | 500 | Minimum profit per unit to include |
| trade_good | string | NO | None | Filter by specific trade good (optional) |
| limit | integer | NO | 20 | Max opportunities to return (sorted by margin) |

### Return Value: ScoutMarketAnalysisResult

```json
{
  "system": "X1-HZ85",
  "timestamp": "2025-11-07T18:30:00Z",
  "scout_data_age_seconds": 300,
  "opportunities": [
    {
      "rank": 1,
      "trade_good": "EQUIPMENT",
      "export_market": {
        "waypoint": "X1-HZ85-A1",
        "market_type": "EXPORT",
        "price_per_unit": 150,
        "quantity_available": 200,
        "last_updated": "2025-11-07T18:28:00Z"
      },
      "import_market": {
        "waypoint": "X1-HZ85-B2",
        "market_type": "IMPORT",
        "price_per_unit": 400,
        "quantity_available": 500,
        "last_updated": "2025-11-07T18:29:00Z"
      },
      "margin": {
        "per_unit": 250,
        "per_full_cargo": 10000,
        "margin_percentage": 62.5
      },
      "route_profitability": {
        "revenue": 16000,
        "estimated_fuel_cost": 500,
        "estimated_net_profit": 15500
      }
    },
    {
      "rank": 2,
      "trade_good": "ELECTRONICS",
      "export_market": { ... },
      "import_market": { ... },
      "margin": { ... },
      "route_profitability": { ... }
    }
  ],
  "market_coverage": {
    "total_markets_in_system": 15,
    "markets_with_scout_data": 12,
    "coverage_percentage": 80.0,
    "markets_without_data": ["X1-HZ85-C1", "X1-HZ85-C3"]
  },
  "data_quality": {
    "oldest_data_age_seconds": 1200,
    "newest_data_age_seconds": 120,
    "average_age_seconds": 300,
    "stale_warning": false
  }
}
```

### Acceptance Criteria

1. **Must return prices from minimum 8 markets** (scout coverage requirement)
2. **Must calculate margin automatically** (per-unit and full-cargo basis)
3. **Must rank by profitability** (highest margin first, descending)
4. **Must filter by minimum margin** (exclude opportunities below threshold)
5. **Must include data freshness** (timestamp when scouts last visited each market)
6. **Must handle missing data gracefully**:
   - If market has no scout data yet: Exclude from results (don't return null)
   - If entire system has no scout data: Return empty opportunities list
7. **Must validate export/import relationship** (don't recommend selling where you can't sell, etc.)
8. **Must include market type** (EXPORT/IMPORT/EXCHANGE) so caller knows why margin exists

### What It Does NOT Need To Do

- Real-time market queries (use scout-cached data only)
- Validate current ship capacity (that's Trading Coordinator's job)
- Validate fuel availability (Trading Coordinator checks before deploying)
- Execute trades (just analyze and return recommendations)

---

## Tool 2: Purchase Market Good

### Purpose
Buy trade goods at a marketplace and load into ship cargo.

### Signature
```
purchase_market_good(
    ship_symbol: str,
    waypoint: str,
    trade_good: str,
    quantity: int,
    max_spend: Optional[int] = None
) -> PurchaseResult
```

### Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| ship_symbol | string | YES | - | Ship symbol (e.g., "ENDURANCE-1") |
| waypoint | string | YES | - | Market waypoint (e.g., "X1-HZ85-A1") |
| trade_good | string | YES | - | Trade good to purchase (e.g., "EQUIPMENT") |
| quantity | integer | YES | - | Units to purchase (max 20 per transaction per API) |
| max_spend | integer | NO | None | Max credits to spend (purchase less if needed to stay under budget) |

### Return Value: PurchaseResult

```json
{
  "success": true,
  "transaction": {
    "ship_symbol": "ENDURANCE-1",
    "waypoint": "X1-HZ85-A1",
    "trade_good": "EQUIPMENT",
    "quantity_requested": 20,
    "quantity_purchased": 20,
    "price_per_unit": 150,
    "total_cost": 3000,
    "timestamp": "2025-11-07T18:30:15Z"
  },
  "ship_inventory": {
    "cargo_used": 20,
    "cargo_capacity": 40,
    "remaining_capacity": 20,
    "cargo_contents": [
      {
        "trade_good": "EQUIPMENT",
        "quantity": 20,
        "weight_units": 20
      }
    ]
  },
  "credits_remaining": 150000
}
```

### Error Cases

```json
{
  "success": false,
  "error": "INSUFFICIENT_INVENTORY",
  "details": {
    "requested": 20,
    "available": 5,
    "message": "Market has only 5 units available, cannot fulfill 20-unit purchase"
  }
}
```

Other error codes:
- `SHIP_NOT_FOUND`: Ship symbol invalid or ship doesn't exist
- `WAYPOINT_NOT_FOUND`: Waypoint doesn't exist in system
- `TRADE_GOOD_NOT_AVAILABLE`: Market doesn't sell this trade good
- `INSUFFICIENT_CARGO`: Ship cargo full, cannot accept purchase
- `INSUFFICIENT_CREDITS`: Not enough credits for purchase (if max_spend specified)
- `SHIP_NOT_DOCKED`: Ship must be docked to conduct market transactions
- `INVALID_QUANTITY`: Quantity > 20 (API limit) or <= 0
- `MARKET_CLOSED`: Waypoint doesn't have marketplace trait

### Acceptance Criteria

1. **Must respect API transaction limit** (max 20 units per transaction)
2. **Must validate sufficient cargo space** (reject if full)
3. **Must validate market has goods** (reject if trade good unavailable)
4. **Must validate sufficient credits** (reject if can't afford)
5. **Must update ship inventory** (returned in response)
6. **Must return exact cost** (per-unit × quantity)
7. **Must handle partial fulfillment** (if quantity > available, purchase available amount)
8. **Must require ship docked** (validate docking status)

### Implementation Note

**IMPORTANT:** If this tool already exists in MCP (check existing market_buy or similar), reuse it. Only build if gap exists.

---

## Tool 3: Sell Market Good

### Purpose
Sell trade goods from ship cargo at a marketplace.

### Signature
```
sell_market_good(
    ship_symbol: str,
    waypoint: str,
    trade_good: str,
    quantity: Optional[int] = None
) -> SellResult
```

### Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| ship_symbol | string | YES | - | Ship symbol (e.g., "ENDURANCE-1") |
| waypoint | string | YES | - | Market waypoint (e.g., "X1-HZ85-B2") |
| trade_good | string | YES | - | Trade good to sell (e.g., "EQUIPMENT") |
| quantity | integer | NO | None | Units to sell (omit to sell all) |

### Return Value: SellResult

```json
{
  "success": true,
  "transaction": {
    "ship_symbol": "ENDURANCE-1",
    "waypoint": "X1-HZ85-B2",
    "trade_good": "EQUIPMENT",
    "quantity_requested": 20,
    "quantity_sold": 20,
    "price_per_unit": 400,
    "total_revenue": 8000,
    "timestamp": "2025-11-07T18:35:00Z"
  },
  "ship_inventory": {
    "cargo_used": 0,
    "cargo_capacity": 40,
    "remaining_capacity": 40,
    "cargo_contents": []
  },
  "credits_received": 8000,
  "credits_remaining": 158000
}
```

### Error Cases

```json
{
  "success": false,
  "error": "INSUFFICIENT_CARGO",
  "details": {
    "requested": 20,
    "in_cargo": 5,
    "message": "Ship cargo has only 5 units, cannot sell 20"
  }
}
```

Other error codes:
- `SHIP_NOT_FOUND`: Ship symbol invalid
- `WAYPOINT_NOT_FOUND`: Waypoint doesn't exist
- `TRADE_GOOD_NOT_IN_CARGO`: Ship doesn't have this trade good in cargo
- `MARKET_NOT_IMPORTING`: Market doesn't import this trade good
- `SHIP_NOT_DOCKED`: Ship must be docked to conduct market transactions
- `INVALID_QUANTITY`: Quantity > available in cargo or <= 0

### Acceptance Criteria

1. **Must validate ship has goods** (reject if cargo doesn't contain trade good)
2. **Must validate market imports goods** (reject if market doesn't buy this good)
3. **Must handle partial sales** (if request > available, sell available)
4. **Must allow "sell all"** (if quantity omitted, sell everything)
5. **Must update ship inventory** (returned in response)
6. **Must return exact revenue** (per-unit × quantity)
7. **Must require ship docked** (validate docking status)

---

## Integration Requirements

### With Trading Coordinator
- Trading Coordinator calls `scout_market_analysis` to get opportunities
- For each opportunity, calls `purchase_market_good` at export market
- Navigates to import market
- Calls `sell_market_good` to sell goods
- Repeats until fuel/time exhausted

### With Scout Network
- `scout_market_analysis` queries data collected by running scout daemons
- Must integrate with existing scout data storage (wherever scout prices are persisted)
- Likely needs to query daemon logs or a cache/database populated by scouts

### With Existing Market Tools
- Check if `purchase_market_good` and `sell_market_good` can leverage existing market operations tools
- Only build new tools if genuine gap exists

---

## Testing Requirements

### Unit Test Cases for scout_market_analysis

1. **Normal operation:** System with 12+ markets, return top 5 opportunities
2. **Margin filtering:** Return only opportunities with margin >= min_margin threshold
3. **Trade good filtering:** When filter specified, return only that trade good
4. **Empty result:** System with no profitable routes, return empty opportunities
5. **Stale data:** Scout data > 1 hour old, return stale_warning flag
6. **Partial coverage:** Only 60% of markets have scout data, report in coverage metrics

### Unit Test Cases for purchase_market_good

1. **Normal purchase:** Buy 20 units at 150 credits/unit, verify cost and inventory
2. **Partial purchase:** Request 20 but only 15 available, purchase 15 and return actual quantity
3. **Full cargo:** Ship at 40/40 capacity, reject purchase
4. **Insufficient funds:** 100 credits available, 20 units at 150 credits each, reject
5. **Invalid quantity:** Request 25 units (> API limit of 20), reject with appropriate error
6. **Market doesn't sell:** Trade good not available at waypoint, reject

### Unit Test Cases for sell_market_good

1. **Normal sale:** Sell all 20 units at 400 credits/unit, verify revenue and inventory
2. **Partial sale:** Request sell 30 but only 20 in cargo, sell 20
3. **Sell all (no quantity):** Omit quantity parameter, sell everything in cargo
4. **Cargo empty:** No goods in cargo for this trade good, reject
5. **Market doesn't import:** Market doesn't import this trade good, reject
6. **Mixed cargo:** Ship has multiple trade goods, selling one doesn't affect others

---

## Documentation Requirements

Each tool should include:
1. **User Story:** Why trading coordinator needs this capability
2. **Example Usage:** Real scenario showing input/output
3. **Error Handling:** How to handle common failure cases
4. **Edge Cases:** Unusual situations and expected behavior

---

## Implementation Priority

1. **scout_market_analysis** (FIRST - unlocks opportunity detection)
   - Highest priority (blocks all other tools until trading opportunities identified)
   - Effort: 3-4 hours

2. **purchase_market_good** (SECOND - enables buying)
   - Effort: 2-3 hours
   - Can reuse existing API if buy tool exists

3. **sell_market_good** (THIRD - completes cycle)
   - Effort: 1-2 hours (simpler than purchase)
   - Can reuse existing API if sell tool exists

**Total: 6-9 hours** (accounting for discovery of existing tools and reducing duplication)

---

## Validation Checklist Before Deployment

- [ ] scout_market_analysis returns opportunities from 8+ markets
- [ ] purchase_market_good respects 20-unit API transaction limit
- [ ] sell_market_good handles partial sales gracefully
- [ ] All tools reject operations when ship not docked
- [ ] All tools validate required parameters and return clear error messages
- [ ] All tools return complete response (success/failure + details)
- [ ] Integration with Trading Coordinator tested end-to-end
- [ ] Edge cases tested (empty results, stale data, insufficient funds, etc.)

---

**Document Prepared By:** TARS Feature Proposer
**Purpose:** Engineering specification for MCP tool development
**Related:** Feature proposal 2025-11-07_new-specialist_trading-coordinator.md

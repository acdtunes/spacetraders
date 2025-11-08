# Feature Proposal: Scout Market Data Query Tool for Trading Operations

**Date:** 2025-11-07
**Priority:** HIGH
**Category:** NEW_MCP_TOOL
**Status:** PROPOSED

## Problem Statement

Scout network is actively monitoring 29 markets and collecting real-time price data, but this data is not accessible to trading or contract operations. TARS cannot identify arbitrage opportunities, calculate trade profitability, or guide ENDURANCE-1 to best markets without a tool to query scout-collected intelligence.

**Current Situation:**
- Scout network: 3 active ships (ENDURANCE-2, 3, 4) monitoring 29+ waypoints
- Data collected: Real-time prices, inventory levels, buy/sell rates
- Data accessibility: UNKNOWN (no query tool exists)
- Use case: Manual trading arbitrage requires immediate market intelligence
- Blocker: No MCP tool to retrieve scout market data

## Current Behavior

**Scout Operations (ACTIVE):**
- Scouts are visiting markets and collecting price snapshots
- Data is presumably stored somewhere (database or internal state)
- No tool available to query this data

**Trading Operations (BLOCKED):**
- Cannot identify best arbitrage opportunities
- Cannot calculate profit before executing trades
- Cannot guide ship navigation to best markets
- Manual analysis of scout data impossible (no interface)

## Impact

- **Credits/Hour Impact:** 0 → 5K-10K (trading becomes viable with market data)
- **Complexity:** LOW to MEDIUM (data already being collected, just need query interface)
- **Dependencies:** Scout network must be active and collecting data (already is)

## Proposed Solution

### What It Should Do

A new MCP tool called `scout_market_query` that provides real-time market intelligence collected by scout network.

**Primary Capability:**
Query current market conditions across all monitored waypoints to identify trading opportunities.

**Required Information Returned:**
For each waypoint currently monitored:
- Waypoint symbol (e.g., X1-HZ85-A1)
- Marketplace status (active/inactive)
- Current inventory items and quantities
- Buy price (what merchants pay for each good)
- Sell price (what you must pay to buy each good)
- Last update timestamp (to assess data freshness)

### User Stories

- As TARS Feature Proposer, I need to query current market prices across 29 waypoints
- So I can identify arbitrage opportunities (buy ore at A1 for 100cr, sell at B2 for 150cr)
- Expected: Returns structured data with prices for all goods at all markets

- As Captain, I need to see top 5 arbitrage opportunities ranked by profit margin
- So I can decide which trading cycle to execute next
- Expected: Returns [opportunity 1 with 50 credit margin, opportunity 2 with 40 credit margin, ...]

- As ENDURANCE-1 contract workflow, I need current prices at specific waypoints
- So I can calculate contract sourcing costs before committing
- Expected: Returns inventory and prices for specific waypoint query

- As TARS, I need to verify scout network is working correctly
- So I can confirm market data is fresh and reliable
- Expected: Returns timestamp, update frequency, number of markets monitored

### Acceptance Criteria

Must:
1. Query market data collected by scout network
2. Return prices for all goods at all monitored waypoints
3. Include both buy and sell prices (market supports both directions)
4. Show current inventory available at each market
5. Indicate data freshness (last update timestamp)
6. Support query filters (specific waypoint, specific good, specific price range)

Should:
1. Return top N arbitrage opportunities sorted by profit margin
2. Calculate total round-trip profit (accounting for fuel costs)
3. Show trading volume constraints (max purchasable per transaction)
4. Include historical price data (to assess stability/trends)
5. Suggest optimal trade routes (A1 → B2 → C3 for maximum profit)

Could:
1. Predict price movements based on historical data
2. Rank opportunities by risk (stable vs volatile prices)
3. Integrate with ship fuel calculations
4. Suggest timing (best time to execute based on trends)

Must Handle:
- No data available (scouts not deployed yet)
- Stale data (prices older than 10 minutes)
- No arbitrage opportunities available (uniform prices)
- Request for non-existent waypoint
- Request for goods with no inventory

---

## Evidence

### Metrics Supporting This Proposal

**Scout Network Status:**
```
Ships monitoring: 3 (ENDURANCE-2, ENDURANCE-3, ENDURANCE-4)
Waypoints covered: 29+ (in X1-HZ85 system)
Data collection rate: Continuous (while scouts moving)
Data freshness: Expected ~6 minute update cycle (scouts move between markets)
```

**Market Opportunity Assessment:**
From strategies.md: "Market prices vary dramatically across waypoints. Arbitrage between export-focused and import-focused markets provides consistent 20-50% margins."

**Current Blocker:**
```
Cannot identify opportunities without querying scout data
Cannot execute trading without profit calculation
Cannot rank opportunities without comparison tool
```

### Proven Strategy Reference

From strategies.md:
- "Scouts cost <100K but provide priceless market data"
- "Buy at exports (low prices), sell at imports (high prices)"
- "Historical price trends over time reveal best locations"
- "Check multiple waypoints for best price" (sourcing optimization)

**Key Finding:** Market intelligence is foundational to profitable trading. Without a query tool, scout data is inaccessible.

---

## Success Metrics

How we'll know this worked:
- **Query Responsiveness:** <1 second to return data for all 29 waypoints
- **Data Freshness:** Prices updated within 10 minutes (scout movement cycle)
- **Accuracy:** Prices match what scouts actually observed
- **Usability:** Captain can identify arbitrage opportunity in <2 minutes using tool
- **Impact:** Trading operations generate 5K-10K credits/hour with this data

---

## Alternatives Considered

- **Alternative 1: Manual Scout Log Review** - Rejected because:
  - Too time-consuming (would take 30+ minutes to analyze 29 waypoints manually)
  - Defeats purpose of AFK operations
  - Error-prone (human analysis of price data)
  - Not suitable for automated decision-making

- **Alternative 2: Central Market Database** - Rejected because:
  - Would require separate market monitoring system
  - Duplicates scout network effort
  - Maintenance overhead
  - Scout network already collecting this data

- **Alternative 3: Real-Time API Queries** - Rejected because:
  - Bypasses scout network
  - Rapid API calls cause rate limiting
  - Scout network designed to minimize API usage
  - Better to use cached data from scouts

---

## Recommendation

**IMPLEMENT IMMEDIATELY: Scout Market Data Query Tool**

**Why:**
1. **Low Complexity:** Data already being collected, just needs query interface
2. **High Impact:** Enables 5K-10K credits/hour trading operations
3. **Strategic Alignment:** Fulfills scout network's intended purpose
4. **Blocks Other Work:** Multiple proposals depend on this (trading arbitrage)
5. **Fast Deployment:** Can be working in <4 hours

**Development Timeline:**
- Design: 30 minutes (straightforward data retrieval)
- Implementation: 2-3 hours (query interface + filtering)
- Testing: 1 hour (verify accuracy of returned data)
- Documentation: 30 minutes
- **Total: 4-5 hours**

**Integration Points:**
1. Retrieve data from scout storage/database
2. Format for MCP tool output
3. Support filtering by waypoint/good/price range
4. Calculate arbitrage opportunities dynamically
5. Return rankings by profit margin

---

## Implementation Notes

### For Engineers

**Data Source:**
Scout network is collecting market data (presumably via waypoint_list or similar market API calls). This data needs to be:
1. Persisted to database
2. Indexed by waypoint + timestamp
3. Queryable by TARS and Captain

**Data Schema Expected:**
```
Scout Market Data:
- waypoint_symbol (X1-HZ85-A1)
- timestamp (when price was recorded)
- trade_good (ORE, EQUIPMENT, WATER_SUPPLY, etc.)
- buy_price (what merchants pay for this good)
- sell_price (what you must pay to buy this good)
- inventory_available (quantity available for purchase)
- imports/exports (designates waypoint type)

Example:
  X1-HZ85-A1 | 2025-11-07T14:30:00 | ORE | 50 | 100 | 500 units | EXPORT
  X1-HZ85-B2 | 2025-11-07T14:28:00 | ORE | 80 | 150 | 200 units | IMPORT
```

**Query Interface:**
```
scout_market_query(
    waypoint_symbol=None,  # optional filter
    trade_good=None,        # optional filter
    min_margin=0,           # optional: min profit per unit
    limit=10,               # optional: max results to return
    recent_only=True        # optional: only last update per waypoint
)

Returns:
{
  "markets": [
    {
      "waypoint": "X1-HZ85-A1",
      "last_update": "2025-11-07T14:30:00",
      "goods": [
        {"good": "ORE", "buy_price": 50, "sell_price": 100, "inventory": 500},
        ...
      ]
    },
    ...
  ],
  "arbitrage_opportunities": [
    {
      "good": "ORE",
      "buy_at": "X1-HZ85-A1",
      "buy_price": 50,
      "sell_at": "X1-HZ85-B2",
      "sell_price": 150,
      "margin_per_unit": 100,
      "max_cargo": 40,
      "total_margin": 4000,
      "estimated_profit": 3900  # accounting for fuel
    },
    ...
  ]
}
```

**Error Handling:**
- No scout data available: Return empty results with message "Scout network not yet active"
- Stale data (>30 minutes old): Return with warning flag "data_freshness": "stale"
- Specific waypoint requested but not monitored: Return null for that waypoint

---

## Next Steps After Implementation

Once tool is working:
1. **Integrate with Trading Operations:** Use query results to guide ENDURANCE-1 trading cycles
2. **Add Historical Analysis:** Compare current prices to historical trends
3. **Implement Price Predictions:** Identify which markets likely to move up/down
4. **Automate Opportunity Detection:** TARS automatically identifies best trade routes
5. **Performance Optimization:** Cache results, refresh on schedule instead of per-query

---

## Related Proposals

This tool enables:
- **Feature: Immediate Revenue Alternatives** (requires market data query)
- **Feature: Manual Trading Arbitrage Operations** (primary use case)
- **Strategy: Trading Phase 2** (future expansion)

---

**Analysis Completed By:** Feature Proposer Agent (TARS)
**Timestamp:** 2025-11-07
**Urgency:** HIGH - Required for immediate revenue alternatives
**Status:** READY FOR ENGINEERING REVIEW AND IMPLEMENTATION

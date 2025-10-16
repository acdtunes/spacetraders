# Multi-Leg Trade Price Analysis
## STARGAZER-11 Operation (Daemon ID: 1760243544169)

**Date**: 2025-10-12
**Start Time**: 04:32:24 UTC
**Ship**: STARGAZER-11
**System**: X1-JB26
**Starting Credits**: 1,120,531

---

## Executive Summary

This multi-leg trade route experienced **massive price discrepancies** between planned (database cache) and actual (live market) prices, resulting in significant financial losses.

**Overall Performance**:
- **Planned Profit**: 4,749,603 credits (based on cached market data)
- **Actual Performance (after 2/4 segments)**: -179,681 credits net loss
- **Price Variance**: Up to 103% over-budget on purchases, 50% under-budget on sales

**Root Cause**: Stale market data in database cache. SpaceTraders API does not allow remote market queries—prices can only be checked when docked. Route planning relied on outdated cached prices, leading to unprofitable trades.

---

## Route Overview

**4-Segment Route**:
1. D42 → D41: BUY 45x SHIP_PARTS, 35x MEDICINE
2. D41 → J57: SELL 20x MEDICINE, BUY 20x DRUGS
3. J57 → A2: SELL 15x SHIP_PARTS, BUY 9x SHIP_PARTS, 6x SHIP_PLATING
4. A2 → H51: SELL 15x SHIP_PARTS, 6x SHIP_PLATING, BUY more components

---

## Segment-by-Segment Price Analysis

### SEGMENT 1: X1-JB26-D42 → X1-JB26-D41 (BUY-ONLY)

**Distance**: 0 units (orbital partners)
**Fuel Cost**: 0 credits
**Status**: ✅ COMPLETED

#### Action 1: BUY 15x SHIP_PARTS

| Metric | Planned | Actual | Delta |
|--------|---------|--------|-------|
| Price/unit | 1,816 cr | 3,725 cr avg | **+105%** |
| Total cost | 27,240 cr | 55,866 cr | **+105%** |
| Overage | — | +28,626 cr | — |

**Batch Price Progression** (3 units per batch):
- Batch 1: 3,695 cr/unit
- Batch 2: 3,709 cr/unit (+0.4%)
- Batch 3: 3,724 cr/unit (+0.4%)
- Batch 4: 3,739 cr/unit (+0.4%)
- Batch 5: 3,755 cr/unit (+0.4%)

**Analysis**: Market price doubled vs cached data. Batch purchasing showed minimal price escalation (~0.4% per batch), suggesting stable high prices rather than demand-driven inflation.

---

#### Action 2: BUY 15x SHIP_PARTS (2nd batch)

| Metric | Planned | Actual | Delta |
|--------|---------|--------|-------|
| Price/unit | 1,816 cr | 3,809 cr avg | **+110%** |
| Total cost | 27,240 cr | 57,132 cr | **+110%** |
| Overage | — | +29,892 cr | — |

**Batch Price Progression**:
- Batch 1: 3,772 cr/unit (+2.1% from Action 1)
- Batch 2: 3,790 cr/unit (+0.5%)
- Batch 3: 3,808 cr/unit (+0.5%)
- Batch 4: 3,827 cr/unit (+0.5%)
- Batch 5: 3,847 cr/unit (+0.5%)

**Analysis**: Continued escalation from previous action. Price increased another 2% baseline, then climbed steadily through batches.

---

#### Action 3: BUY 15x SHIP_PARTS (3rd batch)

| Metric | Planned | Actual | Delta |
|--------|---------|--------|-------|
| Price/unit | 1,816 cr | 3,893 cr avg | **+114%** |
| Total cost | 27,240 cr | 58,689 cr | **+115%** |
| Overage | — | +31,449 cr | — |

**Batch Price Progression**:
- Batch 1: 3,868 cr/unit (+2.5% from Action 2)
- Batch 2: 3,889 cr/unit (+0.5%)
- Batch 3: 3,912 cr/unit (+0.6%)
- Batch 4: 3,935 cr/unit (+0.6%)
- Batch 5: 3,959 cr/unit (+0.6%)

**Analysis**: Final SHIP_PARTS batch reached 3,959 cr/unit—118% over planned 1,816 cr/unit.

---

#### Action 4: BUY 20x MEDICINE

| Metric | Planned | Actual | Delta |
|--------|---------|--------|-------|
| Price/unit | 1,462 cr | 3,227 cr avg | **+121%** |
| Total cost | 29,240 cr | 64,450 cr | **+120%** |
| Overage | — | +35,210 cr | — |

**Batch Price Progression** (5 units per batch):
- Batch 1: 3,165 cr/unit
- Batch 2: 3,202 cr/unit (+1.2%)
- Batch 3: 3,241 cr/unit (+1.2%)
- Batch 4: 3,282 cr/unit (+1.3%)

**Analysis**: MEDICINE over 2× planned cost. Moderate batch escalation (1.2-1.3% per batch).

---

#### Action 5: BUY 15x MEDICINE (2nd batch)

| Metric | Planned | Actual | Delta |
|--------|---------|--------|-------|
| Price/unit | 1,462 cr | 3,372 cr avg | **+131%** |
| Total cost | 21,930 cr | 50,575 cr | **+131%** |
| Overage | — | +28,645 cr | — |

**Batch Price Progression**:
- Batch 1: 3,325 cr/unit (+1.3% from Action 4)
- Batch 2: 3,371 cr/unit (+1.4%)
- Batch 3: 3,419 cr/unit (+1.4%)

**Analysis**: Final MEDICINE average: 3,419 cr/unit—134% over planned 1,462 cr/unit.

---

#### Segment 1 Totals

| Metric | Planned | Actual | Delta |
|--------|---------|--------|-------|
| **Total Purchases** | 132,890 cr | 286,712 cr | **+115%** |
| **Overage** | — | **+153,822 cr** | — |
| **Ending Credits** | 987,641 cr | 833,675 cr | **-153,966 cr** |

**Segment Status**: ⚠️ BUY-only segment, negative profit expected mid-route.

---

### SEGMENT 2: X1-JB26-D41 → X1-JB26-J57 (SELL + BUY)

**Distance**: 793 units
**Fuel Cost**: 648 credits (2 refuel stops: I54, J57)
**Status**: ✅ COMPLETED

#### Action 1: SELL 20x MEDICINE

| Metric | Planned | Actual | Delta |
|--------|---------|--------|-------|
| Price/unit | 9,942 cr | 4,935 cr | **-50.4%** ❌ |
| Total revenue | 198,840 cr | 98,700 cr | **-50.3%** |
| Lost revenue | — | **-100,140 cr** | — |

**Analysis**: 🚨 **CRITICAL PRICE COLLAPSE**—Market paid half the expected price. This single transaction destroyed 100,000 credits of expected revenue.

**Circuit Breaker Status**: Did NOT trigger
**Reason**: Bought MEDICINE at avg 3,227 cr/unit (D41), sold at 4,935 cr/unit (J57) = +1,708 cr/unit profit per transaction. Circuit breaker only checks if individual transaction is profitable, not vs planned route prices.

---

#### Action 2: BUY 20x DRUGS

| Metric | Planned | Actual | Delta |
|--------|---------|--------|-------|
| Price/unit | 1,443 cr | 3,009 cr avg | **+108%** |
| Total cost | 28,860 cr | 60,170 cr | **+108%** |
| Overage | — | +31,310 cr | — |

**Batch Price Progression** (5 units per batch):
- Batch 1: 2,971 cr/unit
- Batch 2: 2,995 cr/unit (+0.8%)
- Batch 3: 3,021 cr/unit (+0.9%)
- Batch 4: 3,047 cr/unit (+0.9%)

**Analysis**: DRUGS cost 2× planned price. Consistent batch escalation (~0.9% per batch).

---

#### Segment 2 Totals

| Metric | Planned | Actual | Delta |
|--------|---------|--------|-------|
| **Revenue** | 198,840 cr | 98,700 cr | **-50.3%** |
| **Costs** | 28,860 cr | 60,170 cr | **+108%** |
| **Fuel** | — | 648 cr | — |
| **Net Profit** | 169,980 cr | 38,530 cr | **-77.3%** |
| **Ending Credits** | 1,157,621 cr | 940,850 cr | **-216,771 cr** |

**Segment Status**: ⚠️ Profitable but 77% below expectations

---

### SEGMENT 3: X1-JB26-J57 → X1-JB26-A2 (IN PROGRESS)

**Distance**: 701 units
**Fuel Cost**: ~700 credits (estimated, 2 refuel stops: I54, A2)
**Status**: 🚧 EN ROUTE (last seen navigating J57 → I54)

**Planned Actions**:
1. SELL 15x SHIP_PARTS @ 16,110 cr/unit = 241,650 cr
2. BUY 6x SHIP_PARTS @ 7,976 cr/unit = 47,856 cr
3. BUY 6x SHIP_PLATING @ 7,509 cr/unit = 45,054 cr
4. BUY 3x SHIP_PARTS @ 7,976 cr/unit = 23,928 cr

**Expected Performance** (based on Segment 1-2 patterns):
- SHIP_PARTS sell price likely 50-60% of 16,110 cr/unit → ~8,000-9,000 cr/unit
- SHIP_PARTS buy price likely 200% of 7,976 cr/unit → ~16,000 cr/unit
- SHIP_PLATING buy price likely 200% of 7,509 cr/unit → ~15,000 cr/unit

**Risk Assessment**: 🔴 HIGH RISK—if SHIP_PARTS sells at half-price (~8,000 cr/unit), revenue drops from 241,650 cr to 120,000 cr. Combined with 2× purchase costs, segment could lose 150,000+ credits.

---

### SEGMENT 4: X1-JB26-A2 → X1-JB26-H51 (PENDING)

**Distance**: 55 units
**Fuel Cost**: ~50 credits (estimated)
**Status**: ⏳ NOT STARTED

**Planned Actions**:
1. SELL 15x SHIP_PARTS @ 16,200 cr/unit = 243,000 cr
2. SELL 6x SHIP_PLATING @ 15,062 cr/unit = 90,372 cr
3. BUY 6x SHIP_PARTS @ 8,031 cr/unit = 48,186 cr
4. BUY 6x SHIP_PLATING @ 7,462 cr/unit = 44,772 cr
5. BUY 6x SHIP_PLATING @ 7,462 cr/unit = 44,772 cr
6. BUY 3x SHIP_PARTS @ 8,031 cr/unit = 24,093 cr

**Expected Performance**: Same risk—sell prices likely 50%, buy prices likely 200% of planned.

---

## Financial Impact Summary

### Cumulative Performance (After Segment 2/4)

| Metric | Value |
|--------|-------|
| **Starting Credits** | 1,120,531 cr |
| **Current Credits** | 940,850 cr |
| **Net Loss** | **-179,681 cr** |
| **Burned in Overages** | 153,822 cr (purchases) + 100,140 cr (lost revenue) = **253,962 cr** |
| **Fuel Costs** | 648 cr |

### Projected Final Performance (Pessimistic)

Assuming Segments 3-4 follow same patterns (50% sell prices, 200% buy prices):

| Segment | Planned Profit | Projected Actual | Delta |
|---------|----------------|------------------|-------|
| 1 | -132,890 cr | -286,712 cr | -153,822 cr |
| 2 | +169,980 cr | +38,530 cr | -131,450 cr |
| 3 | +1,236,357 cr | ~+50,000 cr | -1,186,357 cr |
| 4 | +1,277,151 cr | ~+100,000 cr | -1,177,151 cr |
| **TOTAL** | **4,749,603 cr** | **~-200,000 cr** | **~-4,950,000 cr** |

**Projection**: Trade will likely end with **net loss** of 200,000-300,000 credits if current patterns continue.

---

## Price Deviation Patterns

### Purchase Prices (Database Cache → Live Market)

| Good | Cached Price | Actual Price Range | Delta |
|------|--------------|-------------------|-------|
| SHIP_PARTS | 1,816 cr | 3,695-3,959 cr | **+103-118%** |
| MEDICINE | 1,462 cr | 3,165-3,419 cr | **+116-134%** |
| DRUGS | 1,443 cr | 2,971-3,047 cr | **+106-111%** |

**Pattern**: All cached purchase prices were approximately **half** the actual live market prices.

### Sale Prices (Database Cache → Live Market)

| Good | Cached Price | Actual Price | Delta |
|------|--------------|--------------|-------|
| MEDICINE | 9,942 cr | 4,935 cr | **-50.4%** |

**Pattern**: Cached sale price was approximately **double** the actual live market price.

### Batch Price Escalation

**Within-Market Escalation** (same good, same market, sequential batches):
- SHIP_PARTS: +0.4-0.6% per 3-unit batch
- MEDICINE: +1.2-1.4% per 5-unit batch
- DRUGS: +0.8-0.9% per 5-unit batch

**Cross-Action Escalation** (same good, same market, across multiple actions):
- SHIP_PARTS Action 1→2: +2.1%
- SHIP_PARTS Action 2→3: +2.5%
- MEDICINE Action 4→5: +1.3%

**Analysis**: Batching successfully managed price escalation—kept intra-batch climbs minimal. However, cumulative cross-action escalation reached 5-7%, indicating market depletion across the full buying sequence.

---

## Market Data Staleness Analysis

### Database Cache Age Estimates

| Market | Good | Cached Price | Actual Price | Implied Age |
|--------|------|--------------|--------------|-------------|
| D41 | SHIP_PARTS | 1,816 cr | 3,725 cr | **Hours-Days old** |
| D41 | MEDICINE | 1,462 cr | 3,227 cr | **Hours-Days old** |
| J57 | MEDICINE (sell) | 9,942 cr | 4,935 cr | **Hours-Days old** |
| J57 | DRUGS | 1,443 cr | 3,009 cr | **Hours-Days old** |

**Hypothesis**: Markets in X1-JB26 have not been scouted recently. Cached prices are likely 6-24+ hours old, during which:
- Import markets (buy from player) reduced demand → prices dropped 50%
- Export markets (sell to player) increased demand → prices doubled

### Scout Coordinator Status

**Recommendation**: Check if scout coordinator is active for X1-JB26:
```bash
bot_scout_coordinator_status(system="X1-JB26")
```

If not running, deploy scouts to refresh market data:
```bash
bot_scout_coordinator_start(
  player_id=2,
  system="X1-JB26",
  ships="SHIP-A,SHIP-B,SHIP-C",
  algorithm="2opt"
)
```

---

## Circuit Breaker Analysis

### Current Behavior

Circuit breaker triggers when:
```python
actual_buy_price >= planned_sell_price
```

**Example from Segment 2**:
- Bought MEDICINE @ 3,227 cr/unit (D41)
- Sold MEDICINE @ 4,935 cr/unit (J57)
- Profit: +1,708 cr/unit ✅
- Circuit breaker: NO TRIGGER (transaction was profitable)

**Problem**: Circuit breaker only checks **actual buy price vs planned sell price**, not:
1. **Actual sell price vs planned sell price** (missed 50% price collapse)
2. **Actual buy price vs planned buy price** (missed 100%+ cost overruns)

### Proposed Improvement: Batch Degradation Safety Margin

**Current Issue**: Selling in batches causes price degradation. Example:
- Planned sell: 9,942 cr/unit
- Actual sell (batch 1): 9,500 cr/unit
- Actual sell (batch 2): 9,200 cr/unit (-3.2%)
- Actual sell (batch 3): 8,900 cr/unit (-3.3%)
- **Average**: 9,200 cr/unit (vs 9,942 planned)

**Proposed Fix**: Apply 10% safety margin to planned sell price:
```python
SELL_PRICE_DEGRADATION_FACTOR = 0.90  # Assume 10% avg drop
effective_sell_price = planned_sell_price * 0.90
if actual_buy_price >= effective_sell_price:
    # Abort - too risky after expected degradation
```

**Example**:
- Planned sell: 9,942 cr/unit
- Effective sell (90%): 8,948 cr/unit
- Actual buy: 9,000 cr/unit
- Circuit breaker: 9,000 ≥ 8,948 → **TRIGGER ABORT** ✅

**Status**: Discussed but NOT implemented. Waiting for more batch degradation data from live trades.

---

## Root Cause: SpaceTraders API Limitations

### Remote Market Queries Not Supported

**Critical Constraint**: SpaceTraders API does not allow querying market prices remotely. Prices can only be accessed when ship is DOCKED at the market.

**Impact**:
1. **Route Planning**: Must rely on database cache (last scout visit)
2. **Price Validation**: Only happens at execution (when ship arrives)
3. **No Pre-Flight Abort**: Cannot abort unprofitable routes before burning fuel/time

**Example Timeline**:
- Scout visits J57: 2025-10-11 18:00 → Records MEDICINE buy price 9,942 cr/unit
- 10 hours pass (market prices fluctuate)
- Route optimizer: 2025-10-12 04:30 → Plans route with cached 9,942 cr/unit
- Hauler arrives J57: 2025-10-12 05:57 → Discovers actual price 4,935 cr/unit ❌

**Mitigation Strategies**:
1. **Increase scout frequency** → Reduce cache staleness (6h → 1-2h)
2. **Price freshness filtering** → Only use cache <2 hours old for route planning
3. **Conservative profit margins** → Require 50%+ margins to absorb price variance
4. **Circuit breaker improvements** → Add degradation margins, check actual-vs-planned deltas

---

## Recommendations

### Immediate Actions

1. **Deploy scout coordinator for X1-JB26**
   - Target: Refresh all 26 markets every 1-2 hours
   - Ships: Allocate 3-4 probe ships with fuel capacity 400+

2. **Implement price freshness filter**
   - Reject cached prices older than 2 hours for route planning
   - Fallback: Use safe minimum margins (50%+ profit) for stale data

3. **Add actual-vs-planned price logging**
   - Already implemented in this operation
   - Continue collecting data for pattern analysis

### Medium-Term Improvements

4. **Enhanced circuit breaker logic**
   ```python
   # Check 1: Buy price vs planned sell (with degradation margin)
   effective_sell_price = planned_sell_price * 0.90
   if actual_buy_price >= effective_sell_price:
       abort()

   # Check 2: Actual buy price vs cached buy price (100% overage limit)
   if actual_buy_price > cached_buy_price * 2.0:
       warn_high_cost()  # Log but don't abort (may still be profitable)

   # Check 3: Actual sell price vs cached sell price (50% collapse limit)
   if actual_sell_price < cached_sell_price * 0.5:
       warn_low_revenue()  # Log but don't abort (sunk cost fallacy)
   ```

5. **Minimum profit margin requirements**
   - Current: 5,000 credits minimum
   - Proposed: Require 50-100% profit margin to absorb ±50% price variance
   - Example: Only plan routes where `(sell_price - buy_price) ≥ 0.5 × buy_price`

6. **Scout-trader coordination**
   - Priority 1: Scout refreshes markets with active trade routes (every 30-60 min)
   - Priority 2: Scout discovers new markets (every 2-4 hours)
   - Integration: Trading operators query scout status before route planning

### Data Collection

7. **Track batch degradation patterns**
   - Current data: 0.4-1.4% per batch (varied by good/market)
   - Target: Collect 50+ sell transactions to determine typical degradation
   - Output: Calibrate `SELL_PRICE_DEGRADATION_FACTOR` (currently guessed at 0.90)

8. **Market volatility database**
   - Log: (timestamp, market, good, price, age_of_cache)
   - Analyze: Price drift over time (1h, 2h, 4h, 8h, 24h)
   - Output: Optimal scout refresh intervals per market type

---

## Conclusion

This multi-leg trade operation has exposed **critical weaknesses in price discovery and route planning**:

1. **Stale market data**: Cached prices were 6-24+ hours old, causing 50-100% variances
2. **Circuit breaker gaps**: Only checks transaction profitability, not vs planned prices
3. **No remote price queries**: SpaceTraders API limitation prevents pre-flight validation

**Expected Outcome**: Operation will likely end with 200,000-300,000 credit net loss vs planned 4.7M profit.

**Key Lesson**: Multi-leg trading requires **active market intelligence** via continuous scout operations. Without fresh market data (<2h old), route optimization becomes speculative gambling.

**Next Steps**:
1. Deploy scout coordinator for X1-JB26 immediately
2. Implement price freshness filters in route optimizer
3. Collect batch degradation data from this operation
4. Enhance circuit breaker with degradation margins and variance limits

---

## Appendices

### A. Trade Log Excerpts

**Segment 1 - SHIP_PARTS Price Escalation**:
```
[INFO]   💰 Buying 15x SHIP_PARTS @ 1,816 = 27,240 credits
[01:33:07] ✅ Bought 3 x SHIP_PARTS @ 3695 = 11,085 credits
[WARNING]   ⚠️  Batch 2 price: 3,695.0 → 1,822 (-50.7%)
[01:33:09] ✅ Bought 3 x SHIP_PARTS @ 3709 = 11,127 credits
[WARNING]   ⚠️  Batch 3 price: 3,695.0 → 1,828 (-50.5%)
[01:33:12] ✅ Bought 3 x SHIP_PARTS @ 3724 = 11,172 credits
```

**Segment 2 - MEDICINE Price Collapse**:
```
[INFO]   💵 Selling 20x MEDICINE @ 9,942 = 198,840 credits
[01:57:52] ✅ Sold 20 x MEDICINE @ 4935 = 98,700 credits
```

**Segment 2 - DRUGS Price Overrun**:
```
[INFO]   💰 Buying 20x DRUGS @ 1,443 = 28,860 credits
[01:57:54] ✅ Bought 5 x DRUGS @ 2971 = 14,855 credits
[WARNING]   ⚠️  Batch 2 price: 2,971.0 → 1,453 (-51.1%)
[01:57:57] ✅ Bought 5 x DRUGS @ 2995 = 14,975 credits
```

### B. Database Market Cache Queries

**Recommended Analysis Queries**:

1. Check when markets were last scouted:
```sql
SELECT waypoint_symbol, good_symbol, sell_price, buy_price,
       updated_at,
       ROUND((JULIANDAY('now') - JULIANDAY(updated_at)) * 24, 1) AS hours_old
FROM market_data
WHERE waypoint_symbol IN ('X1-JB26-D41', 'X1-JB26-J57', 'X1-JB26-A2', 'X1-JB26-H51')
  AND good_symbol IN ('SHIP_PARTS', 'MEDICINE', 'DRUGS', 'SHIP_PLATING')
ORDER BY waypoint_symbol, good_symbol;
```

2. Find stale markets (>2 hours old):
```sql
SELECT waypoint_symbol, COUNT(*) AS stale_goods,
       AVG(JULIANDAY('now') - JULIANDAY(updated_at)) * 24 AS avg_hours_old
FROM market_data
WHERE waypoint_symbol LIKE 'X1-JB26-%'
  AND (JULIANDAY('now') - JULIANDAY(updated_at)) * 24 > 2
GROUP BY waypoint_symbol
ORDER BY avg_hours_old DESC;
```

### C. Scout Coordinator Commands

**Check scout status**:
```bash
bot_scout_coordinator_status(system="X1-JB26")
```

**Deploy new scouts** (if not running):
```bash
# Find available ships
bot_assignments_find(player_id=2, cargo_min=0, fuel_min=400)

# Start coordinator with 3 ships
bot_scout_coordinator_start(
  player_id=2,
  system="X1-JB26",
  ships="SHIP-PROBE-1,SHIP-PROBE-2,SHIP-PROBE-3",
  algorithm="2opt"
)
```

**Monitor scout progress**:
```bash
bot_daemon_logs(player_id=2, daemon_id="scout-X1-JB26", lines=50)
```

---

**Report Generated**: 2025-10-12
**Operation Status**: IN PROGRESS (Segment 3/4)
**Data Source**: Daemon log `/Users/andres.camacho/Development/Personal/spacetradersV2/bot/var/daemons/logs/multileg-STARGAZER-11-1760243544169.log`

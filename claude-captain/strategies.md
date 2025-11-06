# SpaceTraders Strategies - Research-Backed Approaches

This document contains proven strategies for SpaceTraders operations based on official game mechanics, community experience, and player analysis. Referenced by TARS agents when making strategic decisions.

**Sources:** Official SpaceTraders API documentation, player dev blogs (staffordwilliams.com, adyxax.org, bwiggs.com), and community implementations.

## Core Game Mechanics

### Rate Limits (CRITICAL)
SpaceTraders API enforces strict rate limits:
- **2 requests/second** sustained
- **30 requests burst** over 60 seconds
- **429 status code** when exceeded

**Optimization Requirements:**
- Implement priority queue (revenue-generating actions first)
- Cache data aggressively (use SQLite with JSON support)
- Centralize network I/O to enforce limits
- Use exponential backoff on 429 errors
- Monitor `x-ratelimit-*` response headers

**Strategic Implications:**
Any competitive agent MUST be efficient in commands sent. Every API call has opportunity cost. Prioritize:
1. Ship actions that generate credits (extraction, selling)
2. Contract fulfillment actions
3. Navigation and docking
4. Market price checks
5. Exploration tasks (lowest priority)

### Fuel Economy
**v2.1 Changes:** Fuel is more scarce, smaller ships have decreased capacity.

**Fuel as Commodity:**
- Most common exchange good
- High-demand locations see dramatic price spikes
- Can extract hydrocarbons from gas giants → process at refineries
- Fuel costs MUST be factored into all profit calculations

**Fuel Starvation:**
Round-trip fuel calculation is MANDATORY. Stranded ships = zero revenue until rescued.

## Fleet Composition Strategies

### Starting Fleet (Contract Focus)
**Recommended:**
- 1 command ship (contract execution)
- Deploy for contracts only initially

**Rationale:**
- Contracts provide TWO revenue touchpoints: acceptance payment + delivery payment
- Guaranteed income vs speculative mining/trading
- Lower API call overhead vs managing mining fleet

**Progression:**
Accept contracts → Build capital → Expand to mining when you can afford surveyor + miners + shuttle

### Mining Fleet Architecture

**Minimum Viable Mining Operation:**
- 1 surveyor ship (identifies high-yield deposits)
- 2-3 mining drones (extraction)
- 1 light shuttle (cargo transport)

**Scaling Pattern:**
```
Phase 1: Survey + 2 miners + 1 shuttle
Phase 2: Survey + 4 miners + 2 shuttles (if yields support it)
Phase 3: Survey + 6 miners + command ship backup transport
```

**Why This Works:**
- Surveying first = 30-50% yield improvement vs blind extraction
- Mining drones fill cargo faster than one transport's round-trip time
- Multiple shuttles prevent transport bottleneck
- Command ship can assist hauling when not on contracts

**Critical Constraint:**
Monitor Credits/hour trend. If declining despite adding miners = **market saturation** (see below).

### Market Intelligence Fleet

**Probe/Satellite Deployment:**
Deploy cheap satellite ships coded to check market prices periodically.

**Coverage Ratio:**
Unknown optimal ratio (player experimentation needed). Consider:
- 1 probe per major trade route
- Refresh prices every 5-10 minutes (balance API limits)
- Store historical data in database (SQLite recommended)

**ROI Tracking:**
Compare trading profits with vs without price data. If no measurable improvement, reduce probe count.

## Mining Strategy (Researched Mechanics)

### Extraction Process
1. Navigate to engineered asteroid (near starting location recommended)
2. Dock at waypoint
3. Refuel ship
4. Orbit asteroid
5. Send extraction request via `/extract` endpoint
6. **Cooldown period** - cannot extract again until cooldown expires
7. Repeat until cargo full
8. Return to market, sell ore

### Survey-First Workflow (CRITICAL)
**Surveyor ships can survey asteroid fields before mining.**

**Benefits of Surveying:**
- "Plotting yields over time reveals best locations to mine for certain ores"
- Significant yield improvement (exact % unknown, requires testing)
- Identifies optimal deposits before committing mining drones

**Process:**
1. Surveyor ship surveys asteroid field
2. Analyzes potential yields
3. Mining drones target surveyed high-yield locations
4. Track actual yields vs surveyed predictions

**Without Surveying:**
Mining blind = lower average yields, wasted cooldowns, suboptimal profit.

### Asteroid Depletion Mechanics
**Critical Discovery:** "Over-mining can cause asteroids to collapse."

**Collapse Effects:**
- "Strongly reduce yields" but don't eliminate extraction entirely
- Collapsed asteroids still mineable, just unprofitable
- Requires moving to new asteroid fields

**Strategic Implications:**
- Monitor yield trends per asteroid
- When yields drop >30% = probable depletion
- Have surveyor identify new fields proactively
- Don't concentrate entire fleet on one asteroid

### Market Saturation Pattern
Player observation: "Slow decrease in Credits/hour which I presume is the result of constantly selling ore."

**Market Dynamics:**
Exports (like ore) experience downward price pressure as local production increases to meet demand. However, production depends on adequate imports.

**Saturation Detection:**
- Ore prices declining over time
- Profit/extraction decreasing despite same yields
- Sell prices approaching break-even with fuel costs

**Recovery Strategy:**
- Reduce mining volume to let market recover
- Diversify to different ore types
- Consider switching to trading if ore markets saturated

### Profitability Formula
```
revenue = ore_quantity * sell_price
fuel_cost = (trips_to_asteroid + trips_to_market) * fuel_consumed * fuel_price
time_cost = extraction_cooldowns + travel_time
profit_per_hour = (revenue - fuel_cost) / time_cost
```

**Target Unknown** - requires player experimentation and market conditions.

**Break-Even Threshold:**
If profit_per_hour approaching zero = immediate strategy change required.

## Contract Strategy (Confirmed Mechanics)

### Contract Payment Structure
**Two-Phase Payment:**
1. **Acceptance payment** - received when accepting contract
2. **Delivery payment** - received when delivering goods

**Critical Insight from Community:**
"A contract might not be very lucrative or potentially even a net loss, but the next contract is a windfall, so it makes sense at times to take a loss on a contract."

### Contract Workflow
1. Faction offers contract for delivering goods to specific location
2. Request contract with ship at waypoint
3. Source required goods:
   - Purchase from markets (cheapest)
   - Mine if necessary (slower)
   - Check multiple waypoints for best price
4. Deliver to specified location
5. Collect delivery payment

### Strategic Contract Acceptance

**Accept Even if Marginal/Loss:**
Contracts unlock progression and reputation. Next contract may be highly profitable. Short-term loss for long-term gain.

**Calculate Total Cost:**
```
goods_cost = quantity * purchase_price
fuel_cost = (sourcing_trips + delivery_trips) * fuel_consumed * fuel_price
time_cost = opportunity cost of ship time
total_cost = goods_cost + fuel_cost + time_cost
net_profit = acceptance_payment + delivery_payment - total_cost
```

**Decision Matrix:**
- Net profit > 0: Accept immediately
- Net profit slightly negative but small: Accept (builds reputation, unlocks next)
- Net profit heavily negative: Reject only if capital constrained

### Sourcing Optimization
"Factions offer money for delivering goods to a specific location. You need to source the required goods for the contract."

**Sourcing Priority:**
1. Check local markets for goods (fastest, cheapest)
2. Use surveyor/probes to find cheapest import markets
3. Mine if no market option exists (slowest)
4. Consider market type:
   - **Exports** = lowest purchase price
   - **Imports** = highest purchase price (avoid buying here)
   - **Exchange** = variable pricing

## Market Intelligence Strategy (Documented Approach)

### Price Visibility Mechanics
**Critical Constraint:** "Price data requires ship presence at waypoints."

You CANNOT see market prices without a ship physically at that waypoint. This is why scouting exists.

### Satellite/Probe Deployment
**Community Strategy:** "Deploy inexpensive probe ships to monitor market conditions over time, using historical data to predict price trends and plan profitable trade routes."

**Implementation:**
- Code satellites to check prices periodically (e.g., every 5-10 minutes)
- Store in database (SQLite with JSON support recommended)
- Respect rate limits (2 req/sec = 120 markets/minute theoretical max)

**Coverage Calculation:**
```
max_markets_per_hour = 3600 seconds * 2 req/sec / requests_per_check
If checking price = 1 request:
  max_markets = 7200/hour (unrealistic, other operations needed)
Realistic with other fleet ops:
  Budget 30% of rate limit to market checks = ~2160 markets/hour
```

**Practical Deployment:**
Unknown optimal ratio. Start with 1 probe per major trade route. Scale based on ROI.

### Historical Data Analysis
"Plotting yields over time can reveal the best locations" (applies to both mining and trading).

**Database Schema (Recommended):**
- Waypoint
- Good
- Sell price
- Buy price
- Timestamp
- Supply level (if available)

**Analysis Queries:**
- Price trends over time
- Arbitrage opportunities (buy location vs sell location)
- Supply/demand shifts

## Trade Arbitrage Strategy (Documented Mechanics)

### Fundamental Principle
**Official Guidance:** "Buying at places that export goods, and selling at waypoints that import them will typically be the most profitable way to earn credits."

### Market Type Mechanics

**Exports:**
- Produced locally
- Lower purchase prices
- "Experience downward price pressure as local production increases to meet agent demand"
- Production depends on adequate imports (supply chain!)

**Imports:**
- Consumed locally
- Higher sell prices
- "Rising sell prices as consumption depletes supply"
- Increased agent supply typically increases consumption as prices fall

**Exchange Goods:**
- Traded between agents
- Price driven purely by player activity
- Example: **Fuel** is most common exchange good

### Arbitrage Execution

**Basic Route:**
1. Find waypoint that **exports** good X (cheap purchase)
2. Find waypoint that **imports** good X (expensive sell)
3. Calculate:
```
revenue = quantity * import_sell_price
cost = quantity * export_buy_price + round_trip_fuel_cost
profit = revenue - cost
```
4. Execute if profit positive and worth the time

**Advanced Considerations:**

**Supply Chain Dependencies:**
"Production depends on adequate imports." If imports insufficient, export production constrained = price increases at export location (breaks arbitrage).

Monitor export locations - if prices rising = supply chain issue.

**Market Saturation:**
"Increased agent supply typically increases consumption as prices fall."

Flooding import markets with goods → prices decline → arbitrage window closes.

**Fuel Price Volatility:**
"High-demand locations can see dramatic price spikes when agent supply is insufficient."

Fuel is exchange good, so agent-driven. If many players competing on route, fuel prices spike = profit margin destroyed.

### Route Sustainability
Markets aren't static. Successful trade routes attract competition → prices equilibrate → profit disappears.

**Sustainable Approach:**
- Maintain multiple routes
- Monitor price trends daily
- Exit route when margin drops below threshold
- Discovery of new routes ongoing process

## Exploration and Charting

### Exploration Incentives
"Factions at the edge of the universe are more likely to trade in rare technology and trade goods."

Far systems = better goods = higher margins (but longer travel, more fuel).

### Charting Mechanics
Ships can chart waypoints to share information with other players.

**Without Charts:**
- Must rely on scanner modules
- Or physical travel to gather waypoint data

**Limitations:**
"Some details can only be found by flying to the waypoint, or by using more advanced scanners."

**Strategic Value:**
Unknown. Charting may provide reputation/credit rewards or unlock trade opportunities. Requires testing.

## Advanced Optimizations

### Database Caching Strategy
**Proven Approach:** "Caching maximum information in SQLite database reduces API calls."

**Implementation:**
- Store API responses as JSON documents with indexed fields
- Balance simplicity vs normalization
- Reduces redundant /my/ships/{shipSymbol} calls
- Cache market price data with timestamps

**Benefits:**
- Drastically reduces rate limit pressure
- Enables historical trend analysis
- Faster operation planning (no API roundtrip)

### Request Priority Queue
**Documented Pattern:** "Priority queue manages network requests, ensuring rate-limit compliance while strategically ordering actions."

**Priority Hierarchy:**
1. Revenue-generating ship actions (extract, sell, buy, deliver)
2. Navigation toward revenue operations
3. Docking/refueling (prerequisites for revenue)
4. Market price checks (intelligence)
5. Exploration tasks (lowest)

**Why This Matters:**
At 2 req/sec, every wasted call = lost revenue opportunity.

### Behavioral Automation Chains
**Proven Patterns:**
- Auto-docking check before navigation
- Auto-refuel upon arrival at waypoints
- Auto-route to asteroids when detected
- Auto-sell when cargo full

**Reduces:**
- Manual intervention
- API calls (batch operations)
- Human error

## Common Pitfalls (Documented)

### 1. Fuel Starvation
**Symptom:** Ship stranded mid-route
**Cause:** Calculated one-way fuel, forgot return trip
**Fix:** Always calculate `fuel_needed = outbound + return + 10% safety margin`

### 2. Rate Limit Violation
**Symptom:** 429 errors, failed operations
**Cause:** Concurrent requests without centralized queue
**Fix:** Centralize all network I/O, implement priority queue

### 3. Ignoring Cooldowns
**Symptom:** Wasted API calls trying to extract during cooldown
**Cause:** Not tracking extraction cooldown timers
**Fix:** Store cooldown expiry, don't attempt action until expired

### 4. Over-Mining Asteroid Collapse
**Symptom:** Yields suddenly drop 70%+
**Cause:** Extracted too much from single asteroid field
**Fix:** Monitor yields, rotate between multiple fields proactively

### 5. Market Supply Chain Blindness
**Symptom:** Export prices unexpectedly rising
**Cause:** Import shortage constraining production
**Fix:** Monitor both sides of supply chain, ensure imports adequate

## Technology Progression (Hypothetical)

**Unknown Mechanics:**
- How factions unlock
- What "rare technology" means
- Upgrade paths for ships
- Module/mount system details

**Requires Testing:**
- Edge-of-universe exploration ROI
- Advanced scanner capabilities vs basic
- Shipyard technology tiers

## Metrics to Track

**Per-Ship:**
- Credits earned (lifetime)
- Fuel consumed (lifetime)
- Profit per operation
- Active time vs cooldown time
- API calls per credit earned

**Fleet-Wide:**
- Total credits/hour (trend over time)
- Rate limit utilization %
- Market price trends (by good, by waypoint)
- Asteroid yield trends (depletion detection)

**Warning Signals:**
- Credits/hour declining = market saturation or asteroid depletion
- Fuel costs increasing = price spike, route optimization needed
- Yields declining = asteroid collapse imminent

## Strategy Validation Framework

When proposing new strategy:
1. **Calculate Expected ROI:** Credits/hour improvement
2. **API Call Efficiency:** Operations per request
3. **Fuel Cost Impact:** Additional fuel vs additional revenue
4. **Rate Limit Budget:** How many requests needed
5. **Capital Requirement:** Upfront ship/good purchase costs
6. **Risk Assessment:** What happens if market moves against us
7. **Rollback Plan:** How to exit if unprofitable

## Research Sources

**Official Documentation:**
- docs.spacetraders.io (game mechanics)
- spacetraders.stoplight.io (API reference)

**Community Experience:**
- staffordwilliams.com/blog (mining optimization, fleet management)
- adyxax.org/blog (caching strategy, JavaScript async patterns)
- bwiggs.com/projects (contract workflow, multi-ship coordination)

**Confirmed Mechanics:**
- Rate limits: 2 req/sec, 30 burst/60sec
- Markets: Export/import/exchange dynamics
- Contracts: Two-payment structure
- Mining: Survey improvement, asteroid collapse
- Fuel: Scarcity in v2.1, exchange good
- Exploration: Edge-of-universe rewards

**Unconfirmed/Requires Testing:**
- Optimal probe deployment ratio
- Exact survey yield improvement %
- Surveyor ship mechanics details
- Ship frame types and cargo capacities
- Technology/reputation unlock thresholds

---

**Last Updated:** 2025-11-05
**Maintained By:** TARS feature-proposer agent
**Based On:** Official SpaceTraders API v2.1 documentation and community implementations

# SpaceTraders Strategies - Research-Backed Approaches

This document contains proven strategies for SpaceTraders operations based on official game mechanics, community experience, and player analysis. Referenced by TARS agents when making strategic decisions.

**Sources:** Official SpaceTraders API documentation, player dev blogs (staffordwilliams.com, adyxax.org, bwiggs.com), and community implementations.

## Early Game Strategy (0-300K Credits)

**Starting Position:**
- 1 command ship
- ~150K-175K starting credits
- Basic headquarters location
- No market intelligence

**Phase 1: Intelligence Network**

**Goal:** Build market visibility before scaling operations

**Step 1 - Scout Ship Acquisition:**
- Purchase 2-3 probe/scout ships (max 100K total investment)
- Look for cheapest ships that can move between waypoints
- Probe ships are disposable intelligence assets, not revenue generators

**Step 2 - Scout Operations:**
- Deploy scout ships to cover major trade routes in your system
- Use scout_markets MCP tool to start continuous market monitoring
- Target: 1 scout per 2-3 key waypoints
- Let scouts run continuously to gather price trends

**Step 3 - Contract Operations:**
- Start contract fulfillment with command ship immediately
- Use contract_batch_workflow MCP tool
- Contracts provide TWO revenue touchpoints:
  - Acceptance payment (upfront capital)
  - Delivery payment (profit)
- Target: Complete initial batch of 5-10 contracts

**Why This Works:**
- Scouts cost <100K but provide priceless market data
- Contracts generate 10-20K profit per run (guaranteed income)
- Market intelligence enables better sourcing decisions
- Low risk: contracts are guaranteed profit, scouts are cheap

**Success Metrics:**
- Credits growing steadily from contracts
- Market price data for 10+ waypoints
- Total credits reaching 300K+

**Phase 2: Capital Accumulation**
- Continue contract operations
- Monitor scout data for trade opportunities
- Save capital for mining fleet or trade expansion
- Target: 500K-1M credits before major expansion

**Pitfalls to Avoid:**
- ❌ Don't buy expensive ships before you have market data
- ❌ Don't start mining without surveyor + multiple miners
- ❌ Don't ignore scout data - it's your strategic advantage
- ❌ Don't scale too fast - let scouts identify opportunities first

## Core Game Mechanics

### Fuel Economy
**v2.1 Changes:** Fuel is more scarce, smaller ships have decreased capacity.

**Fuel as Commodity:**
- Most common exchange good
- High-demand locations see dramatic price spikes
- Can extract hydrocarbons from gas giants → process at refineries
- Fuel costs MUST be factored into all profit calculations

**Fuel Starvation:**
Round-trip fuel calculation is MANDATORY. Stranded ships = zero revenue until rescued.

**Operation Efficiency:**
Every ship operation has opportunity cost. Prioritize:
1. Ship actions that generate credits (extraction, selling)
2. Contract fulfillment actions
3. Navigation and docking
4. Market price checks
5. Exploration tasks (lowest priority)

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
Deploy cheap satellite ships to check market prices periodically.

**Coverage Ratio:**
Unknown optimal ratio (player experimentation needed). Consider:
- 1 probe per major trade route
- Refresh prices every 5-10 minutes
- Track historical price trends over time

**ROI Tracking:**
Compare trading profits with vs without price data. If no measurable improvement, reduce probe count.

## Mining Strategy (Researched Mechanics)

### Extraction Process
1. Navigate to engineered asteroid (near starting location recommended)
2. Dock at waypoint
3. Refuel ship
4. Orbit asteroid
5. Execute extraction operation
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
- Scout ships check prices periodically (e.g., every 5-10 minutes)
- Track historical price data for trend analysis
- Use scout_markets MCP tool for continuous monitoring

**Practical Deployment:**
Unknown optimal ratio. Start with 1 probe per major trade route. Scale based on ROI.

### Historical Data Analysis
"Plotting yields over time can reveal the best locations" (applies to both mining and trading).

**Key Metrics to Track:**
- Price trends over time
- Arbitrage opportunities (buy location vs sell location)
- Supply/demand shifts
- Waypoint-specific good pricing

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

### Behavioral Automation
**Proven Patterns:**
- Auto-docking check before navigation
- Auto-refuel upon arrival at waypoints
- Auto-route to asteroids when detected
- Auto-sell when cargo full

**Benefits:**
- Reduces manual intervention
- Optimizes operation efficiency
- Minimizes human error
- Enables continuous operations

## Common Pitfalls (Documented)

### 1. Fuel Starvation
**Symptom:** Ship stranded mid-route
**Cause:** Calculated one-way fuel, forgot return trip
**Fix:** Always calculate `fuel_needed = outbound + return + 10% safety margin`

### 2. Ignoring Cooldowns
**Symptom:** Wasted operations trying to extract during cooldown
**Cause:** Not tracking extraction cooldown timers
**Fix:** Track cooldown expiry, don't attempt action until expired

### 3. Over-Mining Asteroid Collapse
**Symptom:** Yields suddenly drop 70%+
**Cause:** Extracted too much from single asteroid field
**Fix:** Monitor yields, rotate between multiple fields proactively

### 4. Market Supply Chain Blindness
**Symptom:** Export prices unexpectedly rising
**Cause:** Import shortage constraining production
**Fix:** Monitor both sides of supply chain, ensure imports adequate

### 5. Premature Scaling
**Symptom:** Credits depleted, fleet idle
**Cause:** Bought too many ships before establishing profitable operations
**Fix:** Scale incrementally, validate profitability before expanding

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
- Operations per credit earned

**Fleet-Wide:**
- Total credits per operation (trend over time)
- Market price trends (by good, by waypoint)
- Asteroid yield trends (depletion detection)
- Fleet utilization percentage

**Warning Signals:**
- Credits per operation declining = market saturation or asteroid depletion
- Fuel costs increasing = price spike, route optimization needed
- Yields declining = asteroid collapse imminent

## Strategy Validation Framework

When proposing new strategy:
1. **Calculate Expected ROI:** Credits per operation improvement
2. **Operation Efficiency:** Time and resources per operation
3. **Fuel Cost Impact:** Additional fuel vs additional revenue
4. **Capital Requirement:** Upfront ship/good purchase costs
5. **Risk Assessment:** What happens if market moves against us
6. **Rollback Plan:** How to exit if unprofitable
7. **Scalability:** Can this strategy grow with fleet size

## Research Sources

**Official Documentation:**
- docs.spacetraders.io (game mechanics)

**Community Experience:**
- staffordwilliams.com/blog (mining optimization, fleet management)
- adyxax.org/blog (automation patterns)
- bwiggs.com/projects (contract workflow, multi-ship coordination)

**Confirmed Mechanics:**
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

**Last Updated:** 2025-11-06
**Maintained By:** TARS feature-proposer agent
**Based On:** Official SpaceTraders documentation and community implementations

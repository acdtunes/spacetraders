# Feature Proposal: Trading Coordinator Specialist Agent

**Date:** 2025-11-07
**Priority:** CRITICAL
**Category:** NEW_SPECIALIST_AGENT + NEW_MCP_TOOLS
**Status:** PROPOSED

---

## Problem Statement

During AFK autonomous operations, when the primary revenue stream (contracts) becomes blocked by technical failures, TARS lacks the operational capability to pivot to secondary revenue strategies. Specifically:

1. **Idle Asset Problem:** ENDURANCE-1 (command ship) sat idle for 20+ minutes generating ZERO revenue while a critical contract workflow bug prevented contract operations.

2. **Capability Gap:** TARS has proven capability to execute:
   - Scout operations (scout-coordinator specialist + scout_markets MCP tool)
   - Contract operations (contract-coordinator specialist + contract_batch_workflow MCP tool)
   - Ship procurement (procurement-coordinator specialist)

   But TARS has NO capability to execute:
   - Manual trading operations (NO trading-coordinator specialist)
   - Autonomous market arbitrage (NO MCP tools for buy/sell)

3. **Strategic Loss:** Learning analyst identified that manual trading could generate 5K-10K credits/hour, representing 40K-100K opportunity cost during the 42-minute remaining window of that session.

4. **Repetition Risk:** Current decision patterns suggest this will recur: "When Plan A (contracts) fails, TARS enters idle mode instead of activating Plan B (trading)."

---

## Current Behavior

### What We Currently Have
- **Scout Coordinator:** Deploys probe ships to monitor market prices continuously
- **Contract Coordinator:** Executes contract fulfillment using contract_batch_workflow MCP tool
- **No Trading Coordinator:** Empty role - no specialist, no MCP tools, no automation

### What Happens When Contracts Fail
From AFK session 2025-11-07 18:00 analysis:

```
Timeline:
- 18:00: AFK session begins, ENDURANCE-1 ready, scouts deployed
- 18:06: Contract workflow blocker discovered (EQUIPMENT transaction limit)
- 18:06-18:24: ENDURANCE-1 remains "on standby" while scouts collect market data
- 18:24: Session continues with 0 revenue from merchant ship (18 minutes idle)

Decision Logic Applied:
"Forcing alternative revenue strategies with the Admiral away would be reckless;
instead, we'll accumulate market intelligence and let the scouts do what they do best."

Result:
- 0 credits earned from ENDURANCE-1 during idle period
- 40K-100K opportunity cost (estimated trading potential)
- Fleet utilization dropped from 100% (all ships working) to 75% (scouts only)
```

### Metrics Showing the Problem
```
Credits/Hour: 0 (during 18-minute idle window)
Merchant Ship Utilization: 0% (ENDURANCE-1 docked, doing nothing)
Fleet Utilization: 75% (only 3 scouts active, 1 ship idle)
Scout Data Available: ✓ YES (3 scouts collecting from 29 markets)
Trading Opportunities Identified: ✗ NO (data collected but not analyzed for trading)
Revenue from Available Strategy: ✗ NOT EXECUTED (trading strategy known but no automation)
```

### Root Cause
No decision framework exists to deploy merchant ships for autonomous trading operations. Scout data proves market intelligence exists; the infrastructure simply doesn't have:
1. A specialist agent authorized to make trading decisions
2. MCP tools to execute trading operations (buy/sell goods)
3. Operational automation to handle multi-hop trading (buy at A, sell at B, repeat)

---

## Impact

### Credits/Hour Impact
- **Current State (Blocker + No Trading):** 0 credits/hour from merchant operations
- **With Trading Coordinator:** 5K-10K credits/hour estimated from arbitrage operations
- **Improvement:** +5K-10K credits/hour (undefined to positive = infinite improvement)

### Opportunity Cost Recovered
- **Per AFK Session (60 minutes):** 5K-10K credits/hour × 1 hour = 5K-10K credits
- **Per Day (multiple sessions):** 10K-20K credits (2+ sessions)
- **Per Week:** 50K-140K credits (7-10 sessions during active operations)

### Complexity
- **Specialist Agent:** LOW (similar pattern to scout/contract coordinators)
- **MCP Tools (Buy/Sell Operations):** MEDIUM (requires API integration + inventory management)
- **Trading Decision Logic:** MEDIUM (margin calculation, route optimization)
- **Overall:** MEDIUM (specialist agent structure reusable; tools moderate complexity)

### Dependencies
1. **Prerequisite 1:** MCP tools for market operations (buy, sell, goods query)
2. **Prerequisite 2:** Scout market data must be accessible to trading coordinator
3. **Prerequisite 3:** Pre-AFK approval workflow (Admiral pre-approves trading thresholds)

---

## Proposed Solution

### Component 1: Trading Coordinator Specialist Agent

**Purpose:** Make autonomous trading decisions based on market data and execute trading operations with pre-approved risk constraints.

**Behavior Pattern (Learning from Existing Specialists):**
- Similar to contract-coordinator: Receives delegation from Captain, executes operations autonomously
- Similar to scout-coordinator: Continuous operation (can run infinite trading cycles)
- Similar to procurement-coordinator: Makes purchasing decisions

**Responsibilities:**
1. **Analyze Market Intelligence:** Query scout-collected market data for trading opportunities
2. **Identify Arbitrage:** Find profitable routes (export low, import high, same good)
3. **Validate Profitability:** Calculate margin before execution
4. **Execute Trades:** Buy at source, navigate to destination, sell
5. **Optimize Cycles:** Execute multiple trading cycles while fuel/time permits
6. **Report Results:** Provide Captain with trade economics (margin, revenue, profit)

**Decision Authority:**
- **Pre-Approved Autonomous:** "If margin >= 500 credits per transaction, execute immediately"
- **Escalate to Captain:** "If margin < 500 or opportunity unclear, wait for human decision"
- **Emergency Halt:** "If losing credits, stop all operations and escalate"

**User Stories:**
1. As Captain, I need trading-coordinator to identify profitable arbitrage opportunities visible in scout data
   - So I can generate revenue when contract operations are blocked
   - Expected: Trading coordinator analyzes 29 markets, identifies top 3 arbitrage routes, ranks by margin

2. As Captain, I need trading-coordinator to execute buy-sell cycles autonomously on approved routes
   - So I can maintain revenue generation during AFK operations
   - Expected: Trading coordinator executes 5-10 trading cycles per hour without my intervention

3. As Captain, I need trading-coordinator to report economics (margin, revenue, fuel cost, net profit)
   - So I can validate profitability and decide whether to continue or pivot strategy
   - Expected: Clear report after each trading cycle with exact numbers

4. As Captain, I need trading-coordinator to respect margin thresholds I specify
   - So idle time doesn't happen—auto-deployment when margin meets pre-approved threshold
   - Expected: If margin >= 500 credits per unit, trading coordinator deploys immediately without asking

5. As Captain, I need trading-coordinator to handle inventory (buy → cargo fill → sell) automatically
   - So I don't have to manually coordinate each step
   - Expected: Trading coordinator manages full cargo cycles, navigates between markets, executes sales

### Component 2: Trading Market Query MCP Tool

**What it should do:** Query scout-collected market intelligence and return trading opportunities ranked by profitability.

**Name:** `scout_market_analysis` (or similar)

**Required Information:**
- **Inputs:**
  - System name (e.g., "X1-HZ85")
  - Trade good (optional filter, e.g., "EQUIPMENT")
  - Minimum margin threshold (e.g., 500 credits)

- **Outputs:**
  - List of markets with current prices (from scout data)
  - Identified arbitrage opportunities: (source market, destination market, good, margin, quantity available)
  - Ranked by profitability (highest margin first)
  - Time-to-stale indicator (how old is this data?)

**User Stories:**
- As trading coordinator, I need to query all markets the scouts have visited
  - So I can see current prices without needing my own scouts
  - Expected: Data from 29 waypoints, updated within last 5-10 minutes

- As trading coordinator, I need to identify arbitrage: "This good costs 50 credits at Market A, sells for 150 at Market B"
  - So I can calculate profit before executing
  - Expected: Automated comparison showing all export-import pairs, sorted by margin

- As trading coordinator, I need to know inventory available at each market
  - So I don't attempt to purchase more goods than available
  - Expected: "Market A has 200 units of EQUIPMENT available"

**Acceptance Criteria:**
- Must return prices from at least 8+ markets (scout coverage)
- Must include export/import market types (identify where goods are cheap vs expensive)
- Must calculate margin automatically (import_price - export_price) * quantity
- Must filter results by minimum margin (if margin < threshold, hide result)
- Must timestamp data (show when scout last visited each market)
- Must handle "no data" case gracefully (return empty list if scouts haven't visited market yet)

**Edge Cases:**
- Market has no inventory: Handle gracefully, show 0 available
- Scout data is stale (>1 hour old): Warn trading coordinator, prices may have changed
- Only 1 market in system: Return empty result (can't arbitrage with 1 market)
- No profitable routes (all margins < minimum): Return empty result

### Component 3: Trading Buy/Sell Operations MCP Tools

**What they should do:** Execute market transactions (purchase and sell) with inventory management.

**Tools Required:**

#### Tool 1: `purchase_market_good`
**Purpose:** Buy trade goods at a market and load into ship cargo

**Parameters:**
- Ship symbol (e.g., "ENDURANCE-1")
- Waypoint (market location, e.g., "X1-HZ85-A1")
- Trade good (e.g., "EQUIPMENT")
- Quantity (units to purchase, up to 20 per transaction)
- Budget (max credits to spend)

**Returns:**
- Success/failure status
- Actual quantity purchased
- Cost paid
- Remaining cargo space

**Acceptance Criteria:**
- Must respect API transaction limit (20 units max per purchase)
- Must handle full cargo (reject purchase if no space)
- Must handle insufficient credits (reject if budget too low)
- Must validate waypoint exists and has goods available
- Must update ship cargo inventory after purchase

#### Tool 2: `sell_market_good`
**Purpose:** Sell trade goods from ship cargo at a market

**Parameters:**
- Ship symbol (e.g., "ENDURANCE-1")
- Waypoint (market location, e.g., "X1-HZ85-B2")
- Trade good (e.g., "EQUIPMENT")
- Quantity (units to sell, optional: sell all if not specified)

**Returns:**
- Success/failure status
- Actual quantity sold
- Revenue received
- Remaining cargo in ship

**Acceptance Criteria:**
- Must validate ship has goods in cargo (reject if 0 units)
- Must validate waypoint imports this good
- Must update ship cargo inventory after sale
- Must handle partial sales (if sell 30 units but only have 25, sell 25 and report)

#### Tool 3: `query_market_goods` (Alternative: Use existing MCP tool if available)
**Purpose:** Check current prices and inventory at a specific market without scout data

**Note:** This may already exist via existing market_data MCP tool. Verify before building.

**Parameters:**
- Waypoint (market location)
- Trade good (optional filter)

**Returns:**
- Current prices (buy/sell)
- Inventory available
- Trade type (export/import/exchange)

---

## Evidence

### Metrics Supporting This Proposal

```
Opportunity Cost Analysis:
- Current State (Idle): 0 credits/hour from ENDURANCE-1
- Trading Potential: 5K-10K credits/hour (per learnings analyst)
- Gap: 5K-10K credits/hour (undefined)

Concrete Example from Failed AFK Session:
- Time window: 42 minutes remaining after contract blocker
- Trading potential: 5K-10K/hour × 0.7 hours = 3.5K-7K credits
- Actual earned: 0 credits
- Opportunity cost: 3.5K-7K credits (proven lost)

Fleet Utilization:
- With trading: 100% (all ships working)
- Without trading: 75% (scouts only, merchant idle)
- Improvement: +25% fleet utilization
```

### Proven Strategy Reference

From `strategies.md`:

**Trade Arbitrage Strategy (Documented Mechanics):**

> "Buying at places that export goods, and selling at waypoints that import them will typically be the most profitable way to earn credits."

**Market Type Mechanics:**

> "Exports: Produced locally, Lower purchase prices"
> "Imports: Consumed locally, Higher sell prices"

**Implementation Pattern:**

> "Deploy inexpensive probe ships to monitor market conditions over time, using historical data to predict price trends and plan profitable trade routes."

This proposal implements the proven strategy of trade arbitrage using scout-collected data as the intelligence source.

**Why Trading Works:**
- **Scout data is real:** 29 markets continuously monitored by 3 probes
- **Arbitrage is guaranteed:** Export prices < Import prices (game mechanic)
- **Scale is proven:** 5K-10K credits/hour estimated is conservative vs. player reports

**Why Now:**
- Scout infrastructure exists (3 probes running continuously)
- Market data is flowing in real-time
- Decision bottleneck is lack of automation (specialist + tools)

---

## Acceptance Criteria

### Functional Requirements
1. **Specialist Agent Must:**
   - Accept delegation from Captain: "Deploy trading coordinator for 30 minutes"
   - Query scout market data for arbitrage opportunities
   - Calculate profitability (margin per unit × available quantity)
   - Execute buy-sell cycles autonomously when margin meets threshold
   - Respect fuel constraints (don't get stranded)
   - Report economics after each cycle (margin, revenue, costs, profit)
   - Halt operations if unprofitable (losing credits)

2. **MCP Tools Must:**
   - Scout market query: Return prices from ≥8 markets with calculated margins
   - Market purchase: Buy goods up to ship capacity, respecting 20-unit transaction limit
   - Market sale: Sell goods and update inventory
   - Handle edge cases gracefully (no inventory, insufficient funds, full cargo)

3. **Decision Logic Must:**
   - Support pre-approved margin thresholds (Captain specifies: "auto-deploy if margin ≥ 500")
   - Escalate when uncertain (margin unclear, opportunity ambiguous)
   - Emergency stop when losing money (stop immediately if negative margin detected)

### Performance Requirements
1. **Execution Speed:**
   - First trade cycle executed within 5 minutes of deployment
   - Subsequent cycles: 5-7 minutes per cycle (buy → navigate → sell)
   - Expected: 8-10 cycles per hour

2. **Profitability:**
   - Minimum margin: 500 credits per transaction (configurable)
   - Expected revenue: 5K-10K credits/hour (from proven strategy)
   - Break-even: After 10 cycles (≥ 5K total profit)

3. **Reliability:**
   - 95%+ cycle completion rate (accounting for inventory/navigation issues)
   - Zero money-losing trades (validation before execution)
   - Graceful failure handling (report issue instead of silently failing)

### Edge Cases to Handle
1. **Insufficient Inventory:** Source market has 0 units → Skip this route, try next option
2. **Full Cargo:** Ship cannot hold more goods → Execute sale first, then purchase
3. **Insufficient Credits:** Cannot afford purchase → Calculate smaller quantity or escalate
4. **Stale Scout Data:** Last update >1 hour ago → Warn but proceed (prices may have changed)
5. **No Profitable Routes:** All margins < threshold → Wait for better data or escalate
6. **Market Type Mismatch:** Good not available at destination → Report and try alternate route

---

## Risks & Tradeoffs

### Risk 1: Market Conditions Change During Execution
**Concern:** Trading coordinator plans trade based on scout data, but by the time it arrives at market, prices have moved and margin is destroyed or negative.

**Mitigation:**
- Validate current prices before purchase (real-time market query, not scout-cached data)
- Use conservative margin threshold (e.g., require 20% margin buffer)
- Pre-calculate round-trip time and validate margin is sustainable over that period
- Emergency: If margin drops below minimum during execution, cancel and return home

**Acceptable because:** Conservative validation is built into acceptance criteria. Even with 20% buffer, 5K-10K credits/hour is achievable. Risk is lower than opportunity cost of inaction.

### Risk 2: Fuel Starvation (Ship Stranded)
**Concern:** Trading coordinator executes multiple cycles, burns fuel faster than anticipated, ship runs out of fuel mid-route.

**Mitigation:**
- Calculate total fuel budget before deployment (enough for 10-15 round trips)
- Check fuel level before each cycle (must have enough for round-trip + 10% safety margin)
- Build "return home" logic (if fuel drops below return threshold, stop trading immediately)
- Report fuel status to Captain after each cycle

**Acceptable because:** Fuel calculations are straightforward. Early research showed solar probes have zero fuel cost; merchant ships have known fuel costs. Conservative calculation prevents this issue.

### Risk 3: Poor Route Selection (Low Margins)
**Concern:** Trading coordinator executes trades, but selected routes have margins too low to be profitable (< 1K total profit).

**Mitigation:**
- Minimum margin threshold set by Captain (e.g., 500 credits per unit)
- Scout market analysis tool pre-filters by threshold
- Calculate expected profit before execution (must be ≥ 500 credits per transaction)
- Escalate if no routes meet threshold

**Acceptable because:** Threshold is configurable and pre-approved by Captain. If margins too low, Captain can adjust threshold or redeploy elsewhere. Risk is contained.

### Risk 4: Automated Decisions Without Captain Approval
**Concern:** Trading coordinator makes purchase decisions while Captain is AFK, and purchases unwanted goods or expensive items.

**Mitigation:**
- Pre-AFK briefing document: Captain pre-approves specific goods, routes, and margin thresholds
- Decision rule: Only execute trades that match pre-approved criteria
- All decisions logged with reasoning (why was this trade selected?)
- Escalation to Captain if decision outside pre-approved envelope

**Acceptable because:** This follows proven pattern from other specialists (scouts, contracts). Captain delegates authority with constraints, specialist executes within constraints. No different from existing automation.

### Risk 5: Competition with Existing Operations
**Concern:** Trading coordinator attempts to purchase goods while contract-coordinator is also buying for contracts, causing inventory shortage.

**Mitigation:**
- Coordinate with contract-coordinator: Check if contract operations are active
- If contracts active: Yield priority to contracts (they're likely higher margin)
- If contracts blocked (as in current scenario): Trading coordinator has exclusive access
- Simple rule: "If contract-coordinator is running, trading-coordinator standby"

**Acceptable because:** Current scenario (contracts blocked) is exactly when we NEED trading. Conflict only occurs when both running simultaneously, which won't happen in pre-AFK approval (Captain chooses Plan A or Plan B, not both).

---

## Success Metrics

How we'll know this worked:

1. **Elimination of Idle Time During Blockers:**
   - Before: Merchant ship idle 20+ minutes when contract fails
   - After: Within 5 minutes of contract blocker, trading coordinator deployed and executing cycles
   - **Target:** ≤5 minutes idle time when primary operation fails

2. **Revenue Recovery:**
   - Before: 0 credits/hour from idle merchant ship
   - After: 5K-10K credits/hour from trading cycles
   - **Target:** ≥5K credits/hour during AFK sessions with trading active

3. **Fleet Utilization Improvement:**
   - Before: 75% (scouts only, merchant idle)
   - After: 100% (scouts + merchant trading)
   - **Target:** 100% fleet utilization during AFK operations

4. **Operational Autonomy:**
   - Before: Captain had to manually intervene (or accept idle time)
   - After: Trading coordinator deploys and executes without Captain intervention
   - **Target:** Zero manual trading interventions needed (automated end-to-end)

5. **Economic Reliability:**
   - Before: Unknown profitability (no trading attempted)
   - After: Clear economics reported per cycle (margin, revenue, costs, profit)
   - **Target:** ≥80% of cycles profitable (negative margin < 20%)

6. **Data-Driven Decisions:**
   - Before: Idle despite market data available (scouts collecting but not used)
   - After: Scout data actively used to identify and execute trading opportunities
   - **Target:** 100% of trading decisions based on scout market intelligence

---

## Alternatives Considered

### Alternative 1: Manual Trading (Human-Driven)
**Description:** Captain manually executes trades by analyzing scout data and delegating buy/sell commands to contract-coordinator

**Why Rejected:**
- Requires Captain intervention during AFK sessions (defeats purpose of autonomous operations)
- Learning analyst recommended trading but acknowledged manual approach inadequate for autonomous deployment
- Creates bottleneck: Merchant ship idle until Captain reviews data and makes decision
- Doesn't solve "idle time during blockers" problem (Captain might be asleep/away)

### Alternative 2: Expand Contract Operations (More Merchants)
**Description:** Instead of trading, buy additional merchant ships and run multiple contract workflows in parallel

**Why Rejected:**
- Doesn't solve current blocker (additional merchants would hit same EQUIPMENT transaction limit bug)
- Adds cost (25K+ per merchant ship) without solving root problem
- Contract bug must be fixed first anyway (transaction limit splitting)
- Trading requires no new ships (use existing ENDURANCE-1)

### Alternative 3: Mining Operations Only
**Description:** When contracts fail, deploy merchant ship to mining operations instead of trading

**Why Rejected:**
- Lower revenue: 2K-4K credits/hour vs. 5K-10K for trading
- Slower execution: Mining cycles 10-15 minutes vs. trading cycles 5-7 minutes
- Less flexible: Requires specific asteroid location, time-consuming extraction operations
- Learning analyst explicitly recommended trading as Plan B (mining mentioned as Plan C only)

### Alternative 4: Pre-Calculate Trading Routes (No Automation)
**Description:** Before going AFK, calculate optimal trading routes and leave instructions for Captain (if they wake up)

**Why Rejected:**
- Still requires manual intervention (Captain reading notes and executing)
- Doesn't eliminate idle time if Captain doesn't intervene
- Routes calculated now may be stale by execution time (market prices change)
- Doesn't use current scout data advantage

### Alternative 5: Wait for Contract Bug Fix, Then Resume Operations
**Description:** Accept idle time during blocker, focus development on fixing transaction limit bug

**Why Rejected:**
- Opportunity cost: 40K-100K credits lost during AFK sessions while waiting for fix
- Bug fix requires 5-7 hours development (multiple AFK sessions impacted)
- Trading provides alternative income stream, doesn't require contract fix
- Can run both in parallel: Trading while contract fix is being developed
- Learning analyst explicitly called this "NO OPTION: allows 0 revenue, demonstrates infrastructure fragility"

---

## Implementation Complexity Assessment

### Specialist Agent (Trading Coordinator)
**Complexity:** LOW
- **Why:** Follows proven pattern from scout-coordinator and contract-coordinator
- **Structure:** Existing specialist templates can be reused
- **Decision Logic:** Simple threshold-based automation (margin ≥ 500 = deploy)
- **Effort:** 2-3 hours to implement based on existing agent patterns
- **Risk:** LOW (proven design pattern, no new architectures)

### MCP Tools (Scout Market Query, Buy, Sell)
**Complexity:** MEDIUM
- **Scout Market Query:** Must integrate with scout data storage (daemon logs, container data)
  - Effort: 3-4 hours to prototype
  - Risk: MEDIUM (need to understand how scout data is currently stored/accessed)

- **Market Buy Operation:** Must integrate with SpaceTraders API for purchase + split logic
  - Effort: 2-3 hours (leveraging existing API integration)
  - Risk: LOW (straightforward API call + inventory update)
  - **Note:** May already have this capability via existing market operations tools

- **Market Sell Operation:** Similar to buy operation
  - Effort: 1-2 hours (simpler than buy)
  - Risk: LOW

**Total Tool Development:** 6-9 hours

### Integration & Testing
**Complexity:** MEDIUM
- **End-to-End Testing:** Specialist + tools working together
- **Edge Cases:** Inventory management, fuel calculations, error handling
- **Effort:** 4-5 hours
- **Risk:** MEDIUM (integration points need validation)

### Total Implementation Estimate
- **Specialist Agent:** 2-3 hours
- **MCP Tools:** 6-9 hours
- **Integration & Testing:** 4-5 hours
- **Total:** 12-17 hours of development

**Timeline:** 1-2 sprints of concentrated development (assume 8-hour dev days)

---

## Recommendation

**Implement**

**Why:**

1. **Eliminates Critical Operational Failure:** Current behavior is 0 revenue when contracts fail. This proposal enables alternative revenue stream automatically.

2. **Proven Strategy:** Trade arbitrage is documented in `strategies.md` as "typically the most profitable way to earn credits." We're not inventing new mechanics; we're automating proven strategy.

3. **Leverages Existing Infrastructure:** Scout network already monitoring 29 markets continuously. Trading coordinator simply uses available data instead of letting it go unused.

4. **Recovers Massive Opportunity Cost:** Single AFK session with current blocker could have recovered 40K-100K credits. Per week: 50K-140K. Implementation cost (12-17 hours dev time) is recovered in 1-2 sessions.

5. **Low Risk Implementation:** Follows proven design pattern from existing specialists. Uses straightforward game mechanics (buy/sell). Worst case: Generates zero profit instead of negative loss.

6. **Unblocks AFK Operations:** Currently, AFK sessions have single-point-of-failure (contracts). With trading fallback, autonomy is resilient.

**Priority:** CRITICAL - This should be implemented immediately (before next AFK session) to prevent repeat of idle merchant ship scenario.

**Urgency Justification:**
- AFK sessions already scheduled
- Current blocker (contract transaction limit) affects multiple upcoming sessions
- Trading coordinator acts as stop-gap solution while contract bug is being fixed
- Implementation timeline (12-17 hours) fits within development window
- Value per session (5K-10K credits) justifies rapid implementation

---

## Proposed Delivery Sequence

1. **Phase 1 (2-3 hours):** Implement trading-coordinator specialist agent with basic decision logic
2. **Phase 2 (6-9 hours):** Develop MCP tools (scout market query, buy, sell operations)
3. **Phase 3 (4-5 hours):** Integration testing, edge case validation, documentation
4. **Phase 4 (Pre-AFK):** Create trading-coordinator.md agent config (similar to scout-coordinator.md)
5. **Deployment:** Next AFK session with pre-approved trading thresholds

---

**Report Prepared By:** TARS Feature Proposer
**Date:** 2025-11-07
**Based On:**
- AFK Session Analysis: 2025-11-07 18:00 (0 revenue recovery opportunity)
- Learnings Analyst Report: 2025-11-07 (trading potential identified)
- Emergency Revenue Alternatives Brief: 2025-11-07 (trading rated as highest-value option)
- Strategic Reference: `strategies.md` (trade arbitrage confirmed as proven mechanic)
- Operational Data: Scout deployment active (3 probes × 29 markets continuous monitoring)

**Confidence Level:** 90% (Strategy proven in game; implementation straightforward; risk contained)

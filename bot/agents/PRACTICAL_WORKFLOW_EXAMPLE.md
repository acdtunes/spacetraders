# Practical Agent Workflow Example

**Scenario**: Captain says "Maximize profits for 4 hours"

This document shows the complete MCP-based agent workflow from start to finish.

---

## Phase 1: Planning (First 5 minutes)

### First Mate (in main conversation)

```
1. Check current state
   - spacetraders_daemon_status() → 0 active daemons
   - spacetraders_assignments_list() → all ships idle
   - spacetraders_status(player_id=1) → 6 ships, 175k credits

2. Spawn Market Analyst for intelligence
```

**Task tool call (Market Analyst)**:
```
description: "Scout X1-HU87 markets"
prompt: """You are the Market Analyst for CMDR_AC_2025 (player_id=1).

Task: Scout X1-HU87 system and analyze opportunities.

Steps:
1. Scout markets: spacetraders_scout_markets(player_id=1, ship="CMDR_AC_2025-2", system="X1-HU87", algorithm="2opt")
3. Find mining: spacetraders_find_mining_opportunities(player_id=1, system="X1-HU87")
4. Return top 10 trade routes + top 5 mining locations with profit/hr estimates"""
subagent_type: "general-purpose"
```

**Market Analyst runs** (~20 min):
- Scouts 25 markets with Ship 2
- Analyzes all trade route combinations
- Finds best mining locations
- Returns results:

```
MARKET INTELLIGENCE REPORT - X1-HU87

TOP 10 TRADE ROUTES:
1. SHIP_PARTS: D42→A2 | 160k/trip, 35% ROI, 2 trips/hr = 320k/hr
2. IRON_ORE: B7→A1 | 45k/trip, 28% ROI, 2.5 trips/hr = 112k/hr
3. COPPER: B7→A2 | 38k/trip, 25% ROI, 2.5 trips/hr = 95k/hr

TOP 5 MINING LOCATIONS:
1. B9→B7 (GOLD) | Est 3.2k/hr
2. C3→A1 (IRON) | Est 2.5k/hr
```

### First Mate analyzes and plans

```
Available ships:
- Ship 1: 40 cargo (good for trading)
- Ship 2: 40 cargo (just used for scouting, available)
- Ships 3-5: Mining ships
- Ship 6: 60 cargo (good for trading or contracts)

Plan:
- Ship 1: Trade SHIP_PARTS (320k/hr)
- Ship 6: Trade IRON_ORE (112k/hr)
- Ships 3-5: Mine at B9 (3x 2.5k/hr = 7.5k/hr)

Total expected: 439.5k/hr x 4 hours = 1.76M credits
```

### First Mate presents to Captain

```
Recommended 4-hour operation plan:

TRADING (2 ships):
- Ship 1: SHIP_PARTS route (320k/hr)
- Ship 6: IRON_ORE route (112k/hr)

MINING (3 ships):
- Ships 3,4,5: Gold mining at B9 (7.5k/hr combined)

Projected revenue: ~1.76M credits in 4 hours
Investment: 0 (using existing ships)
ROI: Infinite

Approve?
```

**Captain**: "Approved"

---

## Phase 2: Execution (Next 2 minutes)

### First Mate spawns 3 operators IN PARALLEL

**Single message with 3 Task calls:**

**Task 1 - Trading Operator (Ship 1)**:
```
description: "Start Ship 1 Trading"
prompt: """Trading Operator for CMDR_AC_2025 (player_id=1).

Task Type 1: Start trading operation

Ship: CMDR_AC_2025-1
Good: SHIP_PARTS
Buy from: X1-HU87-D42
Sell to: X1-HU87-A2
Duration: 4 hours
Min profit: 150000

Steps:
1. Start daemon: spacetraders_daemon_start(operation="trade", daemon_id="trader-ship1", args=[...])
2. Register: spacetraders_assignments_assign(ship="CMDR_AC_2025-1", operator="trading_operator", daemon_id="trader-ship1", operation="trade", duration=4)
3. Return daemon_id and expected metrics"""
```

**Task 2 - Trading Operator (Ship 6)**:
```
description: "Start Ship 6 Trading"
prompt: """Trading Operator for CMDR_AC_2025 (player_id=1).

Task Type 1: Start trading operation

Ship: CMDR_AC_2025-6
Good: IRON_ORE
Buy from: X1-HU87-B7
Sell to: X1-HU87-A1
Duration: 4 hours
Min profit: 40000

[same steps as Ship 1]"""
```

**Task 3 - Mining Operator (Ships 3-5)**:
```
description: "Start Ships 3-5 Mining"
prompt: """Mining Operator for CMDR_AC_2025 (player_id=1).

Task Type 1: Start mining operations

Ships: CMDR_AC_2025-3, CMDR_AC_2025-4, CMDR_AC_2025-5
Asteroid: X1-HU87-B9
Market: X1-HU87-B7
Cycles: 50

Steps for EACH ship:
1. Start daemon: spacetraders_daemon_start(operation="mine", daemon_id="miner-ship3", args=[...])
2. Register: spacetraders_assignments_assign(...)
3. Return all 3 daemon_ids and expected metrics"""
```

### Operators complete (~2 min total)

**All 3 return results**:

```
Trading Operator (Ship 1):
- Daemon ID: trader-ship1
- Started at: 14:05:00
- Expected: 160k/trip, 2 trips/hr, 4 hours = 1.28M total

Trading Operator (Ship 6):
- Daemon ID: trader-ship6
- Started at: 14:05:15
- Expected: 45k/trip, 2.5 trips/hr, 4 hours = 450k total

Mining Operator (Ships 3-5):
- Daemon IDs: miner-ship3, miner-ship4, miner-ship5
- Started at: 14:05:30
- Expected: 2.5k/hr each x 3 ships x 4 hours = 30k total
```

### First Mate confirms to Captain

```
All operations started successfully:

ACTIVE DAEMONS:
✅ trader-ship1 (Ship 1, SHIP_PARTS trading)
✅ trader-ship6 (Ship 6, IRON_ORE trading)
✅ miner-ship3 (Ship 3, GOLD mining)
✅ miner-ship4 (Ship 4, GOLD mining)
✅ miner-ship5 (Ship 5, GOLD mining)

Expected completion: 18:05 (4 hours from now)
Projected revenue: 1.76M credits

I'll monitor every 30 minutes and report progress.
```

---

## Phase 3: Monitoring (Every 30 minutes for 4 hours)

### First Mate (main conversation, repeating loop)

**Every 30 minutes**:

```python
while time_elapsed < 4_hours:
    # Check daemon health
    status = spacetraders_daemon_status()

    # Sync assignments (cleans up crashed daemons)
    spacetraders_assignments_sync()

    # Get performance metrics
    for daemon_id in ["trader-ship1", "trader-ship6", "miner-ship3", "miner-ship4", "miner-ship5"]:
        logs = spacetraders_daemon_logs(daemon_id=daemon_id, lines=50)
        # Parse logs for metrics

    # Check for issues
    if any_daemon_stopped:
        # Spawn analyst to investigate
        spawn_agent_to_analyze()

    # Report to Captain
    report_progress()

    sleep(30 * 60)  # Wait 30 minutes
```

### Example: Hour 2 Check (16:05)

```
First Mate checks:
- spacetraders_daemon_status() → all 5 running
- spacetraders_daemon_logs(daemon_id="trader-ship1", lines=50)

Parsing logs shows:
- Ship 1: 4 trips completed, avg 162k profit/trip = 648k so far ✅
- Ship 6: 5 trips completed, avg 44k profit/trip = 220k so far ✅
- Ships 3-5: 28 cycles combined, revenue 14k so far ✅

Total so far: 882k credits (on track for 1.76M target)
```

**Report to Captain**:
```
2-hour progress update:

Credits earned: +882k (50% of target)
All daemons: ✅ Running smoothly
Issues: None
Intervention needed: No

Expected 4-hour total: On track for 1.76M
```

### Example: Issue Detection (Hour 2.5)

```
First Mate checks daemon logs:
- trader-ship6 last 3 trips: 42k, 38k, 36k (declining!)

Threshold: <40k for 3 consecutive trips → investigate
```

**Spawn Trading Operator for analysis**:

```
Task tool call:
description: "Analyze Ship 6 Trading"
prompt: """Trading Operator for CMDR_AC_2025 (player_id=1).

Task Type 2: Analyze trading performance

Daemon: trader-ship6

Analyze last 10 trips:
- spacetraders_daemon_logs(daemon_id="trader-ship6", lines=200)
- Calculate profit trend
- Determine if route still profitable
- Recommend: continue or switch

Return analysis with recommendation."""
```

**Trading Operator returns**:

```
Ship 6 Trading Analysis:

Last 10 trips profits: 45k, 44k, 46k, 43k, 42k, 40k, 38k, 36k, 34k, 32k
Trend: -13k over 10 trips (-29% decline)
Current: 32k/trip (below 40k threshold)

Root cause: IRON_ORE sell price dropped from 145cr to 132cr at X1-HU87-A1

Recommendation: ⚠️ SWITCH ROUTE
Suggested alternative: COPPER route (95k/hr, stable pricing)
```

**First Mate decision**:
```
1. Stop trader-ship6 daemon
2. Spawn Market Analyst: "Get current best route for Ship 6"
3. Market Analyst returns: COPPER B7→A2 (38k/trip, stable)
4. Spawn Trading Operator: "Start Ship 6 on COPPER route"
5. Log decision in captain log
```

---

## Phase 4: Completion (After 4 hours)

### First Mate (at 18:05)

```
1. Check all daemons
   - All completed or still running

2. Stop any still-running daemons
   - spacetraders_daemon_stop(daemon_id="miner-ship3")  # if still going
   - (repeat for others)

3. Get final logs and parse totals
   - trader-ship1: 8 trips, 1.296M credits
   - trader-ship6: 9 trips (IRON), 2 trips (COPPER after switch), 470k credits
   - mining: 50 cycles combined, 31k credits

4. Release all ships
   - spacetraders_assignments_release(ship="CMDR_AC_2025-1")
   - (repeat for all ships)

5. Verify all ships idle
   - spacetraders_assignments_list() → all idle
```

### Final Report to Captain

```
MISSION COMPLETE - 4 Hour Profit Maximization

Duration: 4 hours (14:05 - 18:05)

RESULTS:
Starting credits: 175,000
Ending credits: 1,972,000
Net profit: +1,797,000 cr

BREAKDOWN BY OPERATION:
Trading (Ship 1 - SHIP_PARTS):
- Trips: 8
- Revenue: 1,296,000 cr
- Avg: 162k/trip, 324k/hr

Trading (Ship 6 - IRON→COPPER):
- Trips: 11 total (9 IRON, 2 COPPER after route switch)
- Revenue: 470,000 cr
- Avg: 42.7k/trip, 117.5k/hr

Mining (Ships 3-5 - GOLD):
- Cycles: 50 combined
- Revenue: 31,000 cr
- Avg: 2.58k/hr per ship

OPERATIONS:
✅ 5 daemons started
✅ 1 route switch (Ship 6, hour 2.5)
✅ 0 crashes
✅ All ships released and idle

PERFORMANCE:
Target: 1.76M credits
Actual: 1.797M credits
Variance: +37k (+2.1%) ✅

All operations completed successfully. Fleet ready for next mission.
```

---

## Key Efficiency Points

1. **Parallel execution**: Market scouting + planning = 20 min total (not 40)
2. **Parallel agent spawning**: 3 operators started simultaneously in single message
3. **Background daemons**: Ships work autonomously (no agent overhead during 4 hours)
4. **On-demand analysis**: Only spawned Trading Operator when issue detected
5. **First Mate monitoring**: Simple checks every 30 min (low overhead)

**Total agent time**: ~25 minutes
**Total autonomous operation**: 4 hours
**Human interaction needed**: 2 approvals (initial plan, route switch)

This scales well to larger fleets (10+ ships) because daemons are autonomous.

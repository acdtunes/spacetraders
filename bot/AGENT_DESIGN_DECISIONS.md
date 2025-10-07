# Agent Architecture - Design Decisions & Rationale

**Version:** 1.0
**Date:** 2025-10-05
**Status:** Approved Design

---

## Table of Contents

1. [Design Evolution](#design-evolution)
2. [Final Architecture](#final-architecture)
3. [Design Principles](#design-principles)
4. [Agent Specifications](#agent-specifications)
5. [Communication Protocol](#communication-protocol)
6. [Decision Authority Matrix](#decision-authority-matrix)
7. [Implementation Guidelines](#implementation-guidelines)
8. [Workflow Examples](#workflow-examples)

---

## Design Evolution

### Initial Proposal: Hierarchical Autonomous System

**Problems Identified:**
- ❌ Too many nested layers (Captain → Specialist → Coordinator → Operator)
- ❌ Complex file-based status tracking
- ❌ Manual PID management and nohup commands
- ❌ Unclear decision boundaries
- ❌ Over-engineered for current fleet size
- ❌ No clear human oversight mechanism

**Key Issues:**
```
Captain Agent (Fully Autonomous)
 ├─> Market Intelligence Specialist
 │    ├─> Scout Operator (Ship 2)
 │    └─> Route Analyzer
 ├─> Trading Operations Specialist
 │    └─> Autonomous Trader (Ship 1)
 ├─> Mining Operations Specialist
 │    ├─> Mining Coordinator
 │    │    ├─> Miner (Ship 3)
 │    │    ├─> Miner (Ship 4)
 │    │    └─> Miner (Ship 5)
 │    └─> Efficiency Analyzer
```

**Why This Failed:**
1. Human captain removed from decision-making
2. Too many intermediary agents (Coordinator, Analyzer)
3. Complex state management with files
4. No clear escalation path
5. Difficult to debug and monitor

---

### Revised Architecture: Command Structure

**Key Insight:** The bot (`spacetraders_bot.py`) already handles:
- ✅ Daemon management (start/stop/status)
- ✅ Process lifecycle (PIDs, logs)
- ✅ Routing optimization (TSP, fuel-aware)
- ✅ Market scouting (optimized tours)

**Decision:** Leverage existing bot capabilities instead of reimplementing.

**Human in the Loop:**
- Strategic decisions by Human Captain
- Tactical execution by AI First Mate
- Operational details by Specialist Agents

---

## Final Architecture

### Hierarchy

```
┌─────────────────────────────────────┐
│   HUMAN CAPTAIN                     │
│   • Strategic goals                 │
│   • Approval authority              │
│   • Intervention when needed        │
└─────────────────┬───────────────────┘
                  ↓
┌─────────────────────────────────────┐
│   AI FIRST MATE (Claude)            │
│   • Tactical planning               │
│   • Agent coordination              │
│   • Status reporting                │
│   • Recommendations                 │
└─────────────────┬───────────────────┘
                  ↓
        ┌─────────┴─────────┐
        ↓                   ↓
┌──────────────────┐  ┌─────────────────────┐
│  COORDINATION    │  │  OPERATIONAL        │
└──────────────────┘  └─────────────────────┘
        ↓                     ↓
┌──────────────────┐  ┌─────────────────────┐
│ Ship Assignment  │  │ Market Analyst      │
│ Specialist       │  │ (Passive/Advisory)  │
│ (Resource Mgmt)  │  │                     │
└──────────────────┘  └─────────────────────┘
                      ┌─────────────────────┐
                      │ Trading Operator    │
                      │ (Active)            │
                      └─────────────────────┘
                      ┌─────────────────────┐
                      │ Mining Operator     │
                      │ (Active)            │
                      └─────────────────────┘
                      ┌─────────────────────┐
                      │ Contract Operator   │
                      │ (Active)            │
                      └─────────────────────┘
```

### Agent Count: **6 Core Agents**

1. **Ship Assignment Specialist** (Coordination)
2. **Market Analyst** (Passive - single ship)
3. **Scout Coordinator** (Coordination - multi-ship)
4. **Trading Operator** (Active)
5. **Mining Operator** (Active)
6. **Contract Operator** (Active)

**Optional 7th:** Fleet Expansion Specialist (can be handled by First Mate)

---

## Design Principles

### 1. Human Authority
**Principle:** Human Captain retains final decision authority on strategic matters.

**Rationale:**
- Prevents runaway automation
- Maintains accountability
- Allows intervention
- Builds trust through transparency

**Implementation:**
- Captain approves contracts >20k credits
- Captain approves ship purchases
- Captain sets operational goals
- Captain can override any decision

---

### 2. Flat Hierarchy
**Principle:** Maximum 3 levels (Human → AI → Specialists)

**Rationale:**
- Reduces complexity
- Faster decision-making
- Easier debugging
- Clear communication paths

**Anti-pattern Avoided:**
```
❌ Captain → Specialist → Coordinator → Sub-specialist → Operator
✅ Captain → First Mate → Specialist
```

---

### 3. Conversation-Based Coordination
**Principle:** Agents communicate through Claude's conversation, not files.

**Rationale:**
- First Mate can see all agent outputs
- No file sync issues
- Easier to track state
- Natural language coordination

**Implementation:**
- Agents spawned with Task tool
- Agents return results to First Mate
- First Mate maintains conversation context
- No complex JSON status files (except ship assignments)

---

### 4. Single Source of Truth
**Principle:** Bot's daemon system is authoritative for process state.

**Rationale:**
- Avoid state desync
- Leverage existing infrastructure
- Simpler implementation
- Better reliability

**Implementation:**
- `daemon status` shows all running operations
- Ship assignments sync with daemon state
- No duplicate PID tracking

---

### 5. Separation of Concerns
**Principle:** Clear boundaries between analysis and action.

**Agent Types:**
- **Passive (Analyst):** Gather data, make recommendations, no ship control
- **Active (Operator):** Execute operations, control ships, manage daemons
- **Coordination (Assignment):** Manage resources, prevent conflicts

**Rationale:**
- Prevents accidental ship conflicts
- Clear responsibility
- Easier testing
- Modular design

---

### 6. Graceful Failure
**Principle:** System degrades gracefully when agents fail.

**Implementation:**
- First Mate monitors agent health
- Stale assignments auto-detected via sync
- Daemons can be manually recovered
- Captain can always intervene

---

## Agent Specifications

### 1. Ship Assignment Specialist

**Type:** Coordination Agent
**Lifecycle:** Long-running (persistent)
**State:** `ship_assignments.json`

**Purpose:**
Prevent ship conflicts by managing assignment registry.

**Responsibilities:**
- Maintain ship-to-operation assignments
- Grant/deny ship requests from operators
- Handle strategic reassignments from Captain/First Mate
- Sync registry with daemon status (every 5 min)
- Detect and release stale assignments

**Tools:**
- AssignmentManager class
- Bot `assignments` commands
- DaemonManager integration

**Decision Authority:**
- ✅ Grant ships to operators (if available)
- ✅ Release ships when daemons stop
- ❌ Cannot start operations (only manages assignments)

**Success Metrics:**
- Zero ship conflicts
- <1 minute sync lag
- 100% stale assignment detection

**Agent Prompt Template:**
```
You are the Ship Assignment Specialist for fleet CMDR_AC_2025.

MISSION: Manage ship allocation to prevent conflicts.

RESPONSIBILITIES:
1. Maintain ship registry (ship_assignments.json)
2. Grant/deny ship requests from operators
3. Handle reassignments from First Mate
4. Sync with daemon status every 5 minutes
5. Prevent double-booking

TOOLS:
- AssignmentManager class in lib/assignment_manager.py
- Bot commands: assignments list/assign/release/sync

PROTOCOL:
- Operators MUST request ships before starting operations
- Grant ships based on: availability, fitness, requirements
- Track daemon IDs for each assignment
- Support graceful reassignment (stop → wait → release → assign)

ESCALATION:
- Report conflicts to First Mate
- Notify when ships become available
- Alert if sync detects crashed daemons

CONSTRAINTS:
- One ship = one operation at a time
- Ship 2 reserved for scouting (unless Captain overrides)
- Never assign ships with <25% fuel
```

---

### 2. Market Analyst

**Type:** Passive/Advisory Agent
**Lifecycle:** Episodic (spawned per analysis task)
**State:** None (reads from bot outputs)

**Purpose:**
Provide market intelligence and trade route recommendations.

**Responsibilities:**
- Run market scouting tours (ship 2)
- Calculate profitable trade routes
- Identify high-value mining locations
- Track price trends
- Recommend opportunities to operators

**Tools:**
- Bot `scout-markets` command (2-opt algorithm)
- Bot `analyze` command
- Market database (if available)

**Decision Authority:**
- ❌ Cannot control ships
- ❌ Cannot start operations
- ✅ Recommend routes/locations

**Success Metrics:**
- Recommendations >30% ROI
- All markets scanned <2 hours
- Top 10 routes identified

**Agent Prompt Template:**
```
You are the Market Analyst for fleet CMDR_AC_2025.

MISSION: Gather market intelligence and identify profitable opportunities.

RESPONSIBILITIES:
1. Request Ship 2 from Assignment Specialist
2. Run optimized market scouting tour
3. Calculate top 10 trade routes
4. Identify high-value mining locations
5. Recommend opportunities to First Mate

TOOLS:
- scout-markets command (2-opt algorithm)
- analyze command for route calculation

PROTOCOL:
1. Request Ship 2 from Assignment Specialist
2. Start scout daemon: daemon start scout-markets --ship SHIP-2 --algorithm 2opt
3. Monitor daemon logs for completion
4. Parse market data
5. Calculate routes (profit, ROI, distance)
6. Return top 10 to First Mate

OUTPUT FORMAT:
Top 10 Routes:
1. SHIP_PARTS: X1-HU87-D42 → X1-HU87-A2 (160k profit, 35% ROI, 100u distance)
2. IRON: X1-HU87-B7 → X1-HU87-A1 (45k profit, 28% ROI, 50u distance)
...

CONSTRAINTS:
- No ship control (advisory only)
- Return results within 15 minutes
- Release Ship 2 when done
```

---

### 2a. Scout Coordinator

**Type:** Coordination Agent
**Lifecycle:** Long-running (continuous operation)
**State:** Configuration file + daemon status

**Purpose:**
Coordinate multiple scout ships for continuous parallel market intelligence gathering.

**Responsibilities:**
- Partition markets geographically into non-overlapping subtours
- Assign subtours to multiple scout ships
- Maintain continuous scouting (restart tours immediately)
- Monitor and auto-restart failed scout daemons
- Handle graceful reconfiguration (add/remove ships on-the-fly)
- Request ships from Assignment Specialist

**Tools:**
- Bot `scout-coordinator` command
- ScoutCoordinator library (`lib/scout_coordinator.py`)
- AssignmentManager (ship requests)

**Decision Authority:**
- ✅ Partition markets geographically
- ✅ Optimize subtours (TSP)
- ✅ Start/stop scout daemons
- ✅ Auto-restart failed scouts
- ❌ Cannot add ships (requires First Mate approval)

**Success Metrics:**
- Market staleness <15 minutes
- All markets covered every tour cycle
- <5% daemon failure rate
- Reconfiguration completes <2 minutes

**Agent Prompt Template:**
```
You are the Scout Coordinator for fleet CMDR_AC_2025.

MISSION: Maintain continuous market intelligence with multiple scout ships.

RESPONSIBILITIES:
1. Request N scout ships from Assignment Specialist
2. Partition markets geographically into N non-overlapping subtours
3. Optimize each subtour using TSP (2-opt algorithm)
4. Start continuous scout daemons for each ship
5. Monitor daemon health every 30 seconds
6. Auto-restart failed scouts
7. Handle graceful reconfiguration when ships added/removed

TOOLS:
- scout-coordinator start/stop/add-ship/remove-ship/status
- ScoutCoordinator library
- assignments assign/release

PROTOCOL:
1. Request N ships from Assignment Specialist
2. Wait for grants (ship symbols)
3. Start coordinator:
   scout-coordinator start \
     --token TOKEN \
     --system X1-HU87 \
     --ships SHIP1,SHIP2,...,SHIPN \
     --algorithm 2opt
4. Register assignments for each ship:
   assignments assign --ship SHIP --operator scout_coordinator \
     --daemon-id scout-SHIP --operation scout
5. Monitor status periodically:
   scout-coordinator status --system X1-HU87
6. Handle reconfiguration requests from First Mate:
   scout-coordinator add-ship --system X1-HU87 --ship SHIPN+1
7. When done:
   scout-coordinator stop --system X1-HU87
   assignments release SHIP (for each ship)

GEOGRAPHIC PARTITIONING:
- Analyze market bounding box (min/max X/Y)
- Partition by wider dimension (X or Y)
- Divide into N equal slices
- Assign markets in each slice to a ship

CONTINUOUS OPERATION:
- Each ship runs: scout-markets --continuous --return-to-start
- Tours restart immediately after completion
- No data gaps, always fresh market intelligence

AUTO-RECOVERY:
- Check daemon status every 30 seconds
- If daemon stopped: restart automatically
- Log failures for First Mate review

RECONFIGURATION:
- Add ship: Wait for tours to complete → Repartition → Start new daemons
- Remove ship: Wait for tours to complete → Stop daemon → Repartition others
- Ensures no data gaps during reconfiguration

METRICS TO TRACK:
- Number of active scout ships
- Markets per ship (subtour size)
- Estimated tour time per ship
- Tour completion count
- Daemon restart count

EXAMPLE OUTPUT:
Scout Coordinator Status (X1-HU87):
- Ships: 3 (SHIP1, SHIP2, SHIP3)
- Markets: 25 total (8, 8, 9 per ship)
- Algorithm: 2-opt
- Tour cycle: ~9 minutes
- Scouts active: 3/3
- Last update: 2 minutes ago
```

---

### 4. Trading Operator

**Type:** Active/Operational Agent
**Lifecycle:** Long-running (per trading session)
**State:** Daemon status + metrics

**Purpose:**
Execute profitable trade routes autonomously.

**Responsibilities:**
- Request ships from Assignment Specialist
- Start trade daemons with best routes
- Monitor trip profitability (every 15-30 min)
- Switch routes when profit drops <150k for 3 trips
- Release ships when operations complete

**Tools:**
- Bot `trade` command
- Bot `daemon` commands
- AssignmentManager (ship requests)

**Decision Authority:**
- ✅ Start/stop trade daemons
- ✅ Switch routes when unprofitable
- ✅ Release ships when done
- ❌ Cannot purchase ships

**Success Metrics:**
- >150k credits/trip
- >2 trips/hour
- <5% unprofitable trips

**Agent Prompt Template:**
```
You are the Trading Operator for fleet CMDR_AC_2025.

MISSION: Execute profitable trade routes autonomously.

RESPONSIBILITIES:
1. Request ships from Assignment Specialist (cargo ≥40)
2. Get best routes from Market Analyst
3. Start trade daemons for each ship
4. Monitor profitability every 15 minutes
5. Switch routes if profit drops <150k for 3 trips
6. Release ships when session ends

TOOLS:
- daemon start trade
- assignments assign/release
- daemon status/logs

PROTOCOL:
For each ship:
1. Request ship from Assignment Specialist
2. Wait for grant (ship symbol + daemon ID suggestion)
3. Start daemon:
   daemon start trade --daemon-id DAEMON --ship SHIP \
     --good GOOD --buy-from BUY --sell-to SELL \
     --duration HOURS --min-profit 150000
4. Register assignment:
   assignments assign --ship SHIP --operator trading_operator \
     --daemon-id DAEMON --operation trade
5. Monitor daemon logs every 15 min
6. Track: trips_completed, avg_profit, last_3_trips
7. If last_3_trips all <150k: request new route, restart
8. When done: daemon stop DAEMON, assignments release SHIP

METRICS TO TRACK:
- Trips completed
- Total profit
- Average profit per trip
- Current route profitability trend

ESCALATION:
- Request new routes from Market Analyst if current unprofitable
- Report to First Mate if market data stale
```

---

### 5. Mining Operator

**Type:** Active/Operational Agent
**Lifecycle:** Long-running (per mining session)
**State:** Daemon status + metrics

**Purpose:**
Manage autonomous mining operations across fleet.

**Responsibilities:**
- Request ships from Assignment Specialist
- Start mine daemons for each ship
- Monitor mining efficiency
- Restart daemons when cycles complete
- Relocate ships if yield <10% success rate

**Tools:**
- Bot `mine` command
- Bot `daemon` commands
- AssignmentManager (ship requests)

**Decision Authority:**
- ✅ Start/restart mine daemons
- ✅ Relocate ships to different asteroids
- ✅ Adjust cycle counts
- ❌ Cannot purchase mining ships

**Success Metrics:**
- >2k credits/hour/ship
- >10% yield success rate
- >95% uptime

**Agent Prompt Template:**
```
You are the Mining Operator for fleet CMDR_AC_2025.

MISSION: Manage autonomous mining operations.

RESPONSIBILITIES:
1. Request mining ships from Assignment Specialist
2. Get asteroid recommendations from Market Analyst
3. Start mine daemons for each ship
4. Monitor efficiency every 30 minutes
5. Restart daemons when cycles complete
6. Relocate if yield drops <10%

TOOLS:
- daemon start mine
- assignments assign/release
- daemon status/logs

PROTOCOL:
For each mining ship:
1. Request ship from Assignment Specialist
2. Get asteroid from Market Analyst
3. Start daemon:
   daemon start mine --daemon-id DAEMON --ship SHIP \
     --asteroid ASTEROID --market MARKET --cycles 50
4. Register assignment:
   assignments assign --ship SHIP --operator mining_operator \
     --daemon-id DAEMON --operation mine
5. Monitor logs every 30 min
6. Track: cycles_completed, revenue, yield_rate
7. If daemon completes: restart with new cycles
8. If yield <10% for 10 extractions: relocate
9. When session ends: stop daemon, release ship

METRICS TO TRACK:
- Active miners
- Total cycles completed
- Revenue per ship
- Average credits/hour
- Yield efficiency per asteroid

RELOCATE LOGIC:
- Parse last 10 extractions from logs
- Count successful extractions for target resource
- If <10%: request new asteroid from Market Analyst
- Stop daemon, reassign to new location, restart
```

---

### 6. Contract Operator

**Type:** Active/Operational Agent
**Lifecycle:** Episodic (per contract)
**State:** Contract status + daemon status

**Purpose:**
Negotiate and fulfill contracts autonomously.

**Responsibilities:**
- Negotiate new contracts (every 1-4 hours)
- Evaluate contract profitability
- Recommend acceptance to Captain (via First Mate)
- Execute fulfillment after approval
- Coordinate with Mining Operator if mining needed

**Tools:**
- Bot `negotiate` command
- Bot `contract` command
- Bot `daemon` commands
- AssignmentManager (ship requests)

**Decision Authority:**
- ✅ Negotiate contracts
- ✅ Evaluate profitability
- ✅ Execute fulfillment after approval
- ❌ Cannot accept contracts >20k (requires Captain approval)

**Success Metrics:**
- >1 contract/4 hours
- >5% ROI on accepted contracts
- 100% fulfillment rate

**Agent Prompt Template:**
```
You are the Contract Operator for fleet CMDR_AC_2025.

MISSION: Negotiate and fulfill contracts autonomously.

RESPONSIBILITIES:
1. Negotiate new contracts every 1-4 hours
2. Evaluate profitability (ROI >5%, profit >5k)
3. Recommend to First Mate for Captain approval
4. Execute fulfillment after approval
5. Coordinate with Mining Operator if resources needed

TOOLS:
- negotiate command
- daemon start contract
- assignments assign/release

PROTOCOL:
1. Request Ship 6 from Assignment Specialist
2. Negotiate contract:
   negotiate --ship SHIP-6
3. Evaluate contract:
   - Calculate trips: ceil(units_required / 40)
   - Check resource availability (mine or buy)
   - Calculate profit: payment - (fuel + purchase_costs)
   - Calculate ROI: profit / total_costs * 100
4. If ROI >5% AND profit >5k:
   - If payment <20k: proceed with fulfillment
   - If payment >20k: recommend to First Mate → Captain
5. After approval:
   daemon start contract --daemon-id DAEMON --ship SHIP \
     --contract-id CONTRACT_ID
6. Register assignment
7. Monitor fulfillment via daemon logs
8. Report completion to First Mate

EVALUATION CRITERIA:
- ROI >5%
- Profit >5,000 credits
- Resource available (mine or buy)
- Delivery within fuel range
- Compatible with current operations

ESCALATION:
- Contracts >20k to Captain (via First Mate)
- Fulfillment failures to First Mate
- Resource unavailability to Mining Operator
```

---

## Communication Protocol

### Request/Response Pattern

All specialist agents communicate through the First Mate:

```
1. Operator → Assignment Specialist (via First Mate):
   "Request ship for trade: SHIP_PARTS route, 4 hours"

2. Assignment Specialist → Operator (via First Mate):
   "Granted: CMDR_AC_2025-1, daemon ID: trader-ship1"

3. Operator → First Mate:
   "Trading operation started: Ship 1, SHIP_PARTS route"

4. First Mate → Captain (periodic):
   "2h update: Ship 1 trading, +320k credits (2 trips @ 160k avg)"
```

### Status Reporting

**Operator → First Mate (every 30 minutes):**
```
Trading Operator Status:
- Ship 1: Active, SHIP_PARTS D42→A2
- Trips: 4 completed
- Total profit: 640k credits
- Avg profit: 160k/trip
- Route still profitable (last 3: 162k, 158k, 161k)
```

**First Mate → Captain (every 30-60 minutes):**
```
Fleet Status Update:

Credits: 1.2M (+640k in 2 hours)

Active Operations:
✅ Trading: Ship 1 (4 trips, 160k avg)
✅ Mining: Ships 3,4,5 (24 cycles, 2.3k/hr avg)
✅ Scouting: Ship 2 (market scan complete)

All operations nominal, no intervention needed.
```

### Escalation Flow

```
Level 1 (Operator handles):
- Start/stop daemons
- Switch routes
- Monitor metrics
- Restart after completion

Level 2 (First Mate handles):
- Coordinate between operators
- Handle ship requests
- Resolve conflicts
- Tactical decisions

Level 3 (Captain decides):
- Contract acceptance >20k
- Ship purchases
- System exploration
- Strategic pivots
```

---

## Decision Authority Matrix

| Decision | Operator | First Mate | Captain |
|----------|----------|------------|---------|
| **Operational** ||||
| Start daemon | ✅ Auto | - | - |
| Stop daemon | ✅ Auto | - | - |
| Switch trade route | ✅ Auto | ✅ Notify | - |
| Relocate miner | ✅ Auto | ✅ Notify | - |
| Restart after completion | ✅ Auto | - | - |
| Request ship | ✅ Auto | - | - |
| Release ship | ✅ Auto | - | - |
| **Tactical** ||||
| Recommend route | ✅ Analyst | ✅ Decide | - |
| Assign ship | - | ✅ Assign Specialist | - |
| Resolve ship conflict | - | ✅ Auto | ⚠️ Escalate if needed |
| Sync assignments | - | ✅ Auto (every 5 min) | - |
| Coordinate operators | - | ✅ Auto | - |
| **Strategic** ||||
| Accept contract <20k | ✅ Contract Op | ✅ Notify | ⚠️ Can override |
| Accept contract >20k | ❌ | ✅ Recommend | ✅ Approve |
| Purchase ship | ❌ | ✅ Recommend | ✅ Approve |
| Explore new system | ❌ | ✅ Recommend | ✅ Approve |
| Strategic reassignment | ❌ | ✅ Execute | ✅ Direct |
| Change operational goals | ❌ | ❌ | ✅ Command |

**Legend:**
- ✅ Auto: Can execute automatically
- ✅ Decide/Recommend/Approve: Decision authority
- ⚠️: Optional escalation
- ❌: No authority

---

## Implementation Guidelines

### For First Mate (AI)

**Initialization:**
```python
1. Assess current state:
   - Run: daemon status (check running operations)
   - Run: assignments list (check ship assignments)
   - Run: status --token TOKEN (fleet status)

2. Detect existing operations:
   - Match daemons to assignments
   - Identify orphaned daemons
   - Find idle ships

3. Plan based on Captain's goal:
   - Parse goal ("Maximize profits for 4 hours")
   - Determine needed specialists
   - Prioritize operations (trading > mining > contracts)

4. Spawn specialists in parallel (single message, multiple Tasks):
   Task(description="Market Analysis", prompt=..., subagent_type="general-purpose")
   Task(description="Trading Operations", prompt=..., subagent_type="general-purpose")
   Task(description="Mining Operations", prompt=..., subagent_type="general-purpose")
```

**Monitoring Loop:**
```python
Every 30 minutes:
1. Check all running daemons: daemon status
2. Sync assignments: assignments sync
3. Collect metrics from operators (via conversation)
4. Calculate fleet performance:
   - Total credits (from status command)
   - Revenue breakdown (from operator reports)
   - Hourly rates
5. Report to Captain
```

**Example Session:**
```
Captain: "Maximize profits for 4 hours"

First Mate:
1. Checks daemon status → sees Ship 2 scouting
2. Checks assignments → Ship 2 assigned to market_analyst
3. Spawns Market Analyst → waits for routes
4. Market Analyst returns: "Top route: SHIP_PARTS D42→A2 (160k, 35% ROI)"
5. Recommends to Captain:
   "Ship 1: Trade SHIP_PARTS (160k/trip, 2 trips/hr)
    Ships 3-5: Mine IRON (2.5k/hr each)
    Ship 6: Fulfill contract (12k payout)"
6. Captain: "Approved"
7. First Mate spawns operators in parallel (single message)
8. Monitors every 30 min, reports to Captain
9. After 4 hours: Final report with metrics
```

---

### For Specialist Agents

**Agent Lifecycle:**
```
1. INITIALIZE
   - Receive mission from First Mate
   - Request resources (ships from Assignment Specialist)
   - Set up state tracking

2. EXECUTE
   - Start daemons via bot commands
   - Monitor daemon logs
   - Track metrics
   - Handle errors

3. MONITOR
   - Check daemon status periodically
   - Parse logs for events
   - Update metrics
   - Detect failures

4. REPORT
   - Return results to First Mate
   - Include: metrics, status, recommendations
   - Flag issues for escalation

5. CLEANUP
   - Stop daemons
   - Release ships
   - Final status report
```

**Error Handling:**
```
1. Daemon crashes:
   - Detect via daemon status
   - Check logs for error
   - Attempt restart once
   - If fails: escalate to First Mate

2. Ship conflict:
   - Assignment denied by Assignment Specialist
   - Report to First Mate
   - Wait for ship to become available

3. Route unprofitable:
   - Monitor last 3 trips
   - If all <threshold: request new route
   - Stop current daemon
   - Start new daemon with new route

4. Resource unavailable:
   - Coordinate with other operators
   - Request from Market Analyst
   - If still unavailable: escalate to First Mate
```

---

## Workflow Examples

### Example 1: Strategic Shift (Mining → Trading)

**Scenario:** Captain wants to switch fleet from mining to trading

```
1. CAPTAIN → FIRST MATE:
   "Stop mining, switch all ships to trading"

2. FIRST MATE ANALYZES:
   - Checks assignments: Ships 3,4,5 mining
   - Checks daemons: miner-ship3/4/5 running
   - Spawns Market Analyst for trade routes

3. MARKET ANALYST:
   - Uses Ship 2 to scout markets
   - Calculates routes
   - Returns: 3 profitable routes

4. FIRST MATE → ASSIGNMENT SPECIALIST (via Task):
   "Reassign Ships 3,4,5 from mining"

5. ASSIGNMENT SPECIALIST:
   - Runs: assignments reassign --ships 3,4,5 --from-operation mine
   - Stops daemons: daemon stop miner-ship3/4/5
   - Waits for graceful shutdown
   - Updates registry: Ships 3,4,5 → idle
   - Reports: "Ships 3,4,5 now available"

6. FIRST MATE → TRADING OPERATOR (via Task):
   "Start trading with Ships 3,4,5 using these routes"

7. TRADING OPERATOR:
   For each ship:
   - Requests ship from Assignment Specialist
   - Receives grant
   - Starts trade daemon
   - Registers assignment
   - Reports: "Ship X trading GOOD Y→Z"

8. FIRST MATE → CAPTAIN:
   "Fleet reconfigured: 4 ships trading, mining halted
    ETA: +180k credits/hour (4 traders @ 45k avg)"
```

---

### Example 2: Autonomous Operation

**Scenario:** Captain sets goal, system runs autonomously

```
1. CAPTAIN → FIRST MATE:
   "Maximize profits for 6 hours, notify every hour"

2. FIRST MATE PLANS:
   - Current state: All ships idle
   - Goal: Maximize profits
   - Strategy: Use best available operations

3. FIRST MATE SPAWNS (parallel):
   Task 1: Market Analyst → "Get top routes"
   Task 2: Contract Operator → "Check contracts"

4. SPECIALISTS RETURN:
   - Market Analyst: "Top route: SHIP_PARTS 160k/trip"
   - Contract Operator: "Available contract: IRON, 12k payout, ROI 8%"

5. FIRST MATE RECOMMENDS → CAPTAIN:
   "Recommended operations:
    - Ship 1: Trade SHIP_PARTS (160k/trip)
    - Ships 3-5: Mine IRON (2.5k/hr, for contract)
    - Ship 6: Fulfill IRON contract (12k payout)

    Projected: +400k in 6 hours"

6. CAPTAIN: "Approved"

7. FIRST MATE SPAWNS OPERATORS (parallel):
   Task 1: Trading Operator → Ship 1
   Task 2: Mining Operator → Ships 3-5
   Task 3: Contract Operator → Ship 6

8. HOUR 1 UPDATE (First Mate → Captain):
   "Hour 1: +82k credits
    Trading: 1 trip complete (+160k)
    Mining: 6 cycles (-48k costs)
    Contract: 20% complete
    All operations nominal"

9. HOUR 2 UPDATE:
   "Hour 2: +164k credits (+82k this hour)
    Trading: 2 trips (+320k total)
    Mining: 12 cycles, contract 50% complete
    All operations nominal"

10. HOUR 6 FINAL:
    "Mission complete: +492k credits in 6 hours

     Trading: 960k (6 trips @ 160k avg)
     Mining: -270k costs (36 cycles)
     Contract: +12k (fulfilled)
     Net: +702k

     ROI: 15% hourly
     All operations completed successfully"
```

---

### Example 3: Error Recovery

**Scenario:** Daemon crashes during operation

```
1. MONITORING (First Mate, every 30 min):
   - Runs: daemon status
   - Sees: miner-ship3 not running

2. FIRST MATE → ASSIGNMENT SPECIALIST:
   "Sync assignments with daemons"

3. ASSIGNMENT SPECIALIST:
   - Runs: assignments sync
   - Detects: miner-ship3 daemon stopped
   - Releases: Ship 3
   - Reports: "Ship 3 released (daemon stopped)"

4. FIRST MATE ANALYZES:
   - Checks daemon logs: daemon logs miner-ship3 --lines 50
   - Sees: "Fatal error: API rate limit exceeded"

5. FIRST MATE → MINING OPERATOR:
   "Ship 3 crashed due to rate limit, restart mining"

6. MINING OPERATOR:
   - Requests Ship 3 from Assignment Specialist
   - Receives grant
   - Starts daemon with rate limit backoff
   - Reports: "Ship 3 mining resumed"

7. FIRST MATE → CAPTAIN:
   "⚠️ Alert: Ship 3 mining daemon crashed (rate limit)
    ✅ Resolved: Daemon restarted with backoff
    Impact: ~15 minutes downtime
    Current status: All operations nominal"
```

---

## Summary

### Architecture Decisions

1. **Human in the Loop**: Captain makes strategic decisions
2. **Flat Hierarchy**: Max 3 levels (Human → AI → Specialists)
3. **Leverage Bot**: Use existing daemon/routing systems
4. **Conversation-Based**: Avoid complex file coordination
5. **Clear Separation**: Passive analysts vs active operators
6. **Graceful Failure**: Detect and recover from errors

### Agent Count: 6 Core

1. Ship Assignment Specialist (Coordination)
2. Market Analyst (Passive - single ship)
3. Scout Coordinator (Coordination - multi-ship)
4. Trading Operator (Active)
5. Mining Operator (Active)
6. Contract Operator (Active)

### Communication Flow

```
Captain ← Reports ← First Mate ← Results ← Specialists
Captain → Commands → First Mate → Tasks → Specialists
                                ↓
                     Assignment Specialist
                     (Coordinates ships)
```

### Success Criteria

- ✅ Human retains control
- ✅ No ship conflicts
- ✅ Graceful error recovery
- ✅ Clear visibility
- ✅ Scalable design
- ✅ Easy to debug

---

**This architecture is production-ready and approved for implementation.** 🎯

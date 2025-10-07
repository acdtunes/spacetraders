# SpaceTraders Agent Architecture

**Updated:** 2025-10-05
**System:** Human Captain + AI First Mate + Specialist Agents
**Architecture:** MCP-Based (Agents for Analysis, Daemons for Operations)

## Key Architecture Principles

**Three-Layer System:**
1. **Background Daemons** - Autonomous operations (mining, trading, contracts) running in background
2. **Specialist Agents** - Analysis and decision support (spawned via Task tool for ONE-TIME tasks)
3. **First Mate** - Continuous monitoring and coordination (runs in main conversation)

**How It Works:**
- **First Mate** monitors daemons every 30 min, spawns specialists when needed
- **Specialists** execute one task (scout markets, analyze performance, start daemons) then exit
- **Daemons** run autonomously in background, logging all activities

See `agents/PRACTICAL_WORKFLOW_EXAMPLE.md` for complete 4-hour scenario walkthrough.

## Overview

```
┌─────────────────────────────────────┐
│   HUMAN CAPTAIN (You)               │
│   • Strategic decisions             │
│   • Contract approval               │
│   • Fleet expansion                 │
│   • Goal setting                    │
└─────────────────┬───────────────────┘
                  ↓
┌─────────────────────────────────────┐
│   AI FIRST MATE (Claude in main     │
│   conversation)                     │
│   • Tactical planning               │
│   • Continuous monitoring (30 min)  │
│   • Spawns specialists as needed    │
│   • Manages background daemons      │
│   • Status reports to Captain       │
└─────────────────┬───────────────────┘
                  ↓
        ┌─────────┴─────────┐
        ↓                   ↓
┌──────────────────┐  ┌─────────────────────┐
│  ANALYSIS        │  │  BACKGROUND         │
│  AGENTS          │  │  DAEMONS            │
│  (ONE-TIME)      │  │  (AUTONOMOUS)       │
└──────────────────┘  └─────────────────────┘
        ↓                     ↓
┌──────────────────┐  ┌─────────────────────┐
│ Market Analyst   │  │ Trading Daemon      │
│ Ship Assignment  │  │ (trader-ship1)      │
│ Trading Operator │  │                     │
│ Mining Operator  │  │ Mining Daemon       │
│ Contract Operator│  │ (miner-ship3/4/5)   │
└──────────────────┘  └─────────────────────┘

         Spawned for:              Runs continuously:
         • Analysis                • Navigate & trade
         • Recommendations         • Mine & sell
         • Starting daemons        • Fulfill contracts
         • Performance checks      • Log activities
         Then EXIT                 Until complete
```

## Agent Roles

### 1. Human Captain (You)

**Strategic Command:**
- Set operational goals ("Maximize trading for 4 hours")
- Approve contracts >20k credits
- Approve ship purchases
- Approve system exploration
- Intervene when needed

**Communication:**
- Issues commands to First Mate
- Reviews periodic reports
- Makes go/no-go decisions

---

### 2. AI First Mate (Claude)

**Responsibilities:**
- Analyze fleet status and market conditions
- Develop tactical plans to achieve Captain's goals
- Spawn specialist agents using Task tool
- Monitor specialist performance
- Coordinate between specialists
- Report to Captain every 30-60 minutes

**MANDATORY Pre-Flight Checks (Before ALL operations):**
1. **VERIFY CONTEXT** - Confirm player ID = correct agent (e.g., player 5 = VOID_HUNTER)
2. **CHECK FAMILIARITY** - If operation is unfamiliar/untested → RESEARCH FIRST
3. **VERIFY INTERFACES** - Read help, check code signatures, verify parameters
4. **CLEAN STATE** - Remove stale daemon records before restarting operations

**Reference:** See `ERROR_PREVENTION_PROTOCOL.md` for complete checklist

**Tools:**
- MCP tools (spacetraders-bot server)
- Task tool for spawning specialists
- Read/Write for analysis and reports

**Typical Session:**
```
1. Captain: "Maximize profits for 4 hours"
2. First Mate:
   - Checks fleet: spacetraders_status(), spacetraders_daemon_status()
   - Spawns Market Analyst for route intelligence
   - Analyzes results, creates plan
3. First Mate → Captain: Recommends plan with projected revenue
4. Captain: "Approved"
5. First Mate: Spawns 3 operators in parallel to start daemons
6. First Mate: Monitors every 30 min, spawns analysts if issues detected
7. First Mate → Captain: Periodic updates + final report with metrics
```

See `agents/PRACTICAL_WORKFLOW_EXAMPLE.md` for detailed walkthrough.

---

### 3. Ship Assignment Specialist

**Type:** Analysis Agent (ONE-TIME tasks)
**Purpose:** Support First Mate with ship availability analysis and allocation management

**Task Types:**
1. **Find Available Ships** - Identify idle ships matching criteria
2. **Handle Reassignment** - Stop daemons, release ships
3. **Analyze Fleet Allocation** - Show what each ship is doing

**MCP Tools:**
```
spacetraders_assignments_find(cargo_min=40)
spacetraders_assignments_list(include_stale=false)
spacetraders_assignments_assign(ship, operator, daemon_id, operation)
spacetraders_assignments_release(ship, reason)
spacetraders_daemon_stop(daemon_id)
spacetraders_status(player_id)
```

**When Spawned:**
```
First Mate → Ship Assignment: "Find ships for trading, need cargo ≥40"
Ship Assignment: Analyzes fleet, returns available ships with details
Ship Assignment → First Mate: "Ship 1 and Ship 6 available, recommend Ship 1"

First Mate → Ship Assignment: "Release ships 3,4,5 from mining"
Ship Assignment: Stops 3 daemons, releases assignments
Ship Assignment → First Mate: "Ships 3,4,5 now idle at location X"
```

See `docs/agents/templates/ship_assignment_specialist.md` for full task types.

---

### 4. Market Analyst

**Type:** Passive/Advisory
**Ship:** CMDR_AC_2025-2 (Scout, 0 cargo)
**Purpose:** Intelligence gathering

**Responsibilities:**
- Run optimized market scouting tours
- Calculate profitable trade routes
- Identify high-value mining locations
- Track price trends

**Bot Commands:**
```bash
# Single-ship continuous scouting
daemon start scout-markets \
  --daemon-id market-scout \
  --ship CMDR_AC_2025-2 \
  --system X1-HU87 \
  --algorithm 2opt \
  --return-to-start \
  --continuous

# Monitor scouting
daemon status market-scout
daemon logs market-scout --lines 100
```

**Output:**
- Top 10 trade routes (good, buy/sell locations, profit, ROI)
- Mining recommendations (asteroid, market, expected yield)
- Market freshness timestamps

**Decision Authority:** None (advisory only)

---

### 4a. Scout Coordinator

**Type:** Coordination Agent
**Purpose:** Multi-ship continuous market intelligence

**Responsibilities:**
- Coordinate multiple scout ships for parallel market scouting
- Partition markets geographically into non-overlapping subtours
- Maintain continuous scouting (restart tours immediately after completion)
- Monitor and auto-restart failed scout daemons
- Handle graceful reconfiguration when ships added/removed

**Bot Commands:**
```bash
# Start multi-ship continuous scouting
scout-coordinator start \
  --token TOKEN \
  --system X1-HU87 \
  --ships SHIP1,SHIP2,SHIP3 \
  --algorithm 2opt

# Add ship to ongoing operation (triggers reconfiguration)
scout-coordinator add-ship \
  --system X1-HU87 \
  --ship SHIP4

# Remove ship (triggers reconfiguration)
scout-coordinator remove-ship \
  --system X1-HU87 \
  --ship SHIP3

# Check status
scout-coordinator status --system X1-HU87

# Stop all scouts
scout-coordinator stop --system X1-HU87
```

**Features:**
- **Geographic Partitioning**: Divides markets by X/Y coordinates for optimal coverage
- **TSP Optimization**: Each ship gets optimized subtour (greedy or 2-opt)
- **Continuous Operation**: Tours restart immediately upon completion
- **Auto-Recovery**: Monitors daemons every 30s, restarts on failure
- **Hot Reconfiguration**: Add/remove ships without data gaps (waits for tours to complete)

**Configuration File:** `config/agents/scout_config_{SYSTEM}.json`
```json
{
  "system": "X1-HU87",
  "ships": ["SHIP1", "SHIP2", "SHIP3"],
  "algorithm": "2opt",
  "reconfigure": false
}
```

**Communication:**
```
First Mate → Scout Coordinator: "Start 3-ship scouting in X1-HU87"
Scout Coordinator: Partitions 25 markets → 3 subtours (8, 8, 9 markets)
Scout Coordinator: Starts 3 continuous scout daemons
Scout Coordinator → First Mate: "Scouting active, ~12 min/tour cycle"

First Mate → Scout Coordinator: "Add SHIP4"
Scout Coordinator: Waits for current tours to complete
Scout Coordinator: Repartitions 25 markets → 4 subtours (6, 6, 6, 7 markets)
Scout Coordinator: Starts new daemons
Scout Coordinator → First Mate: "Now 4 ships, ~9 min/tour cycle"
```

**Benefits:**
- **Faster Intelligence**: 3 ships cover 25 markets in ~9 min vs 25 min single-ship
- **Always Fresh Data**: Continuous tours ensure <15 min market staleness
- **Fault Tolerance**: Auto-restart on daemon failures
- **Flexible Scaling**: Add/remove ships on-the-fly without stopping operation

**Decision Authority:**
- ✅ Partition markets and optimize subtours
- ✅ Start/stop scout daemons
- ✅ Auto-restart failed scouts
- ❌ Add ships (requires First Mate approval)

---

### 5. Trading Operator

**Type:** Active/Operational
**Ships:** Any available with cargo ≥40 (typically Ships 1, 6)
**Purpose:** Execute profitable trade routes

**Responsibilities:**
- Request ships from Assignment Specialist
- Start trade daemons with best routes from Market Analyst
- Monitor trip profitability every 15-30 min
- Switch routes when profit drops <150k for 3 trips
- Release ships when operations complete

**Bot Commands:**
```bash
# Start trading daemon
daemon start trade \
  --daemon-id trader-ship1 \
  --ship CMDR_AC_2025-1 \
  --good SHIP_PARTS \
  --buy-from X1-HU87-D42 \
  --sell-to X1-HU87-A2 \
  --duration 4 \
  --min-profit 150000

# Register assignment
assignments assign \
  --ship CMDR_AC_2025-1 \
  --operator trading_operator \
  --daemon-id trader-ship1 \
  --operation trade

# Monitor
daemon status trader-ship1
daemon logs trader-ship1 --lines 50
```

**Metrics:**
- Trips completed
- Total profit
- Average profit/trip
- Current route profitability

**Decision Authority:**
- ✅ Start/stop trade daemons
- ✅ Switch routes when unprofitable
- ❌ Purchase ships (escalate to Captain)

---

### 6. Mining Operator

**Type:** Active/Operational
**Ships:** Mining ships (Ships 3, 4, 5, etc.)
**Purpose:** Autonomous mining operations

**Responsibilities:**
- Request ships from Assignment Specialist
- Start mine daemons for each ship
- Monitor mining efficiency (credits/hour, yield rates)
- Restart daemons when cycles complete
- Relocate ships if yield <10% success rate

**Bot Commands:**
```bash
# Start mining daemon per ship
for ship in CMDR_AC_2025-3 CMDR_AC_2025-4 CMDR_AC_2025-5; do
  daemon start mine \
    --daemon-id miner-${ship: -1} \
    --ship $ship \
    --asteroid X1-HU87-B9 \
    --market X1-HU87-B7 \
    --cycles 50

  assignments assign \
    --ship $ship \
    --operator mining_operator \
    --daemon-id miner-${ship: -1} \
    --operation mine
done

# Monitor
daemon status
daemon logs miner-ship3 --lines 30
```

**Metrics:**
- Total cycles completed
- Revenue per ship
- Average credits/hour
- Yield efficiency per asteroid

**Decision Authority:**
- ✅ Start/restart mine daemons
- ✅ Relocate ships to different asteroids
- ✅ Adjust cycle counts
- ❌ Purchase mining ships (escalate to Captain)

---

### 7. Contract Operator

**Type:** Active/Operational
**Ships:** Ship 6 (dedicated contract ship)
**Purpose:** Contract negotiation and fulfillment

**Responsibilities:**
- Negotiate new contracts when none active
- Evaluate contract profitability (ROI >5%, profit >5k)
- Recommend acceptance to Captain
- Execute contract fulfillment daemon after approval
- Coordinate with Mining Operator if mining needed

**Bot Commands:**
```bash
# Negotiate new contract
negotiate --ship CMDR_AC_2025-6

# Evaluate and recommend to Captain via First Mate

# Start contract fulfillment (after approval)
daemon start contract \
  --daemon-id contract-fulfiller \
  --ship CMDR_AC_2025-6 \
  --contract-id CONTRACT_ID

assignments assign \
  --ship CMDR_AC_2025-6 \
  --operator contract_operator \
  --daemon-id contract-fulfiller \
  --operation contract

# Monitor
daemon status contract-fulfiller
daemon logs contract-fulfiller
```

**Metrics:**
- Contracts completed
- Total contract revenue
- Average payment
- Success rate

**Decision Authority:**
- ✅ Negotiate contracts
- ✅ Execute fulfillment after approval
- ❌ Accept contracts >20k (escalate to Captain)

---

### 8. Captain Log Writer

**Type:** Coordination Agent
**Purpose:** Automated mission logging and performance tracking

**Responsibilities:**
- Monitor daemon operations and generate log entries automatically
- Create structured captain log entries with standardized format
- Calculate session KPIs (profit/hour, efficiency, ROI)
- Generate executive summaries for Captain review
- Maintain APPEND-ONLY log integrity
- Archive completed missions
- Provide searchable logs with tags

**Bot Commands:**
```bash
# Create new session
captain-log session-start \
  --agent AGENT_CALLSIGN \
  --objective "Mission description"

# Auto-generate entry from daemon
captain-log entry \
  --type operation_started \
  --operator "Mining Operator" \
  --ship SHIP \
  --daemon-id DAEMON_ID

# Log operation completion
captain-log entry \
  --type operation_completed \
  --daemon-id DAEMON_ID

# Log critical error
captain-log entry \
  --type critical_error \
  --operator OPERATOR \
  --ship SHIP \
  --error "Description" \
  --resolution "Fix applied"

# Generate performance summary
captain-log summary \
  --type hourly

# End session with final report
captain-log session-end

# Search logs
captain-log search \
  --tag mining \
  --timeframe 24h

# Generate executive report
captain-log report \
  --duration 4h \
  --format executive

# Archive old sessions
captain-log archive \
  --before-date DATE
```

**Entry Types:**
- `SESSION_START` - First Mate begins operations
- `OPERATION_STARTED` - Specialist starts daemon
- `OPERATION_COMPLETED` - Daemon completes successfully
- `CRITICAL_ERROR` - Error requiring Captain attention
- `STRATEGIC_DECISION` - First Mate escalates to Captain
- `PERFORMANCE_SUMMARY` - Hourly/session KPI report
- `SESSION_END` - Final summary with totals

**Features:**
- **Automated Logging**: Extracts metrics from daemon logs automatically
- **Structured Format**: Consistent markdown with standardized sections
- **Agent Attribution**: Tracks which specialist performed each action
- **Searchable Tags**: Quick filtering (`#mining`, `#error`, `#profitable`)
- **Executive Summaries**: Condensed view for Captain (no 1000+ line logs)
- **KPI Tracking**: Automatic profit/hour, efficiency, ROI calculations
- **APPEND-ONLY**: Maintains log integrity (never delete/modify entries)

**Log Template Structure:**
```
# AGENT INFORMATION
# EXECUTIVE SUMMARY (condensed session overview)
# DETAILED LOG ENTRIES (timestamped, tagged, attributed)
```

**Output Files:**
- `var/logs/captain/{agent}/captain-log.md` - Main log file
- `var/logs/captain/{agent}/sessions/{session_id}.json` - Structured session data
- `var/logs/captain/{agent}/executive_reports/{date}.md` - Daily summaries

**Decision Authority:**
- ✅ Create all log entry types
- ✅ Generate summaries and reports
- ✅ Archive old sessions
- ❌ Modify/delete existing entries

---

## Communication Flows

### 1. Operator ↔ Assignment Specialist

```
Trading Operator → Assignment Specialist:
  "Request ship for trade: SHIP_PARTS route, 4 hours"

Assignment Specialist:
  - Checks availability
  - Verifies ship fitness (cargo ≥40)
  - GRANTS: "CMDR_AC_2025-1, daemon: trader-ship1"

Trading Operator:
  - Starts daemon
  - Updates assignment: assignments assign ...
  - Reports to First Mate: "Ship 1 trading SHIP_PARTS"
```

### 2. First Mate ↔ Specialists

```
First Mate → Market Analyst (via Task):
  "Run market scouting, identify top 10 routes"

Market Analyst:
  - Starts scout-markets daemon
  - Analyzes results
  - Returns: "Top route: SHIP_PARTS D42→A2 (160k profit, 35% ROI)"

First Mate → Trading Operator (via Task):
  "Execute trading: SHIP_PARTS route with Ship 1"

Trading Operator:
  - Requests ship from Assignment Specialist
  - Starts trade daemon
  - Reports: "Ship 1 active, trip 1 complete: +162k"
```

### 3. First Mate ↔ Captain

```
Captain: "Maximize profits for 4 hours"

First Mate:
  1. Checks fleet status
  2. Spawns Market Analyst
  3. Analyzes routes
  4. Recommends: "Ship 1: SHIP_PARTS trading (160k/trip)
                  Ships 3-5: IRON mining (2.5k/hr each)
                  Ship 6: Contract (12k payout)"

Captain: "Approved"

First Mate:
  - Spawns Trading Operator, Mining Operator, Contract Operator in parallel
  - Monitors progress

[After 2 hours]
First Mate → Captain:
  "2h update: +480k credits, all ops running smoothly"

[After 4 hours]
First Mate → Captain:
  "Mission complete: +960k credits
   Trading: 640k (4 trips @ 160k avg)
   Mining: 270k (36 cycles total)
   Contract: 50k (1 contract)"
```

---

## Workflow Example: Strategic Shift

**Scenario:** Captain wants to switch fleet from mining to trading

```
1. CAPTAIN: "Stop mining, switch all ships to trading"

2. FIRST MATE → SHIP ASSIGNMENT SPECIALIST:
   "Reassign Ships 3,4,5 from mining to idle"

3. SHIP ASSIGNMENT SPECIALIST:
   - Runs: assignments reassign --ships 3,4,5 --from-operation mine
   - Stops mining daemons: daemon stop miner-ship3/4/5
   - Waits for graceful shutdown
   - Updates registry: Ships 3,4,5 → idle
   - Reports: "Ships 3,4,5 now idle and available"

4. FIRST MATE → MARKET ANALYST:
   "Calculate 3 profitable trade routes"

5. MARKET ANALYST:
   - Analyzes market data
   - Returns: "Route 1: IRON B7→A1 (45k profit)
              Route 2: COPPER B7→A2 (38k profit)
              Route 3: SILICON B9→A1 (52k profit)"

6. FIRST MATE → TRADING OPERATOR:
   "Execute trading with 3 additional ships"

7. TRADING OPERATOR:
   For each ship (3,4,5):
     - Requests ship from Assignment Specialist
     - Starts trade daemon with assigned route
     - Registers assignment

8. FIRST MATE → CAPTAIN:
   "Fleet reconfigured: 4 ships trading, mining halted
    ETA: +180k credits/hour (4 traders @ 45k avg profit)"
```

---

## Escalation Matrix

| Decision | Agent Authority | Escalate to First Mate | Escalate to Captain |
|----------|-----------------|------------------------|---------------------|
| Start daemon | ✅ Operator | - | - |
| Stop daemon | ✅ Operator | - | - |
| Switch trade route | ✅ Trading Op | - | - |
| Relocate miner | ✅ Mining Op | - | - |
| Negotiate contract | ✅ Contract Op | - | - |
| Accept contract <20k | ✅ Contract Op | ✅ Report | - |
| Accept contract >20k | ❌ | ✅ Recommend | ✅ Approve |
| Purchase ship | ❌ | ✅ Recommend | ✅ Approve |
| Explore new system | ❌ | ✅ Recommend | ✅ Approve |
| Strategic reassignment | ❌ | ✅ Execute | ✅ Direct |

---

## Bot Commands Reference

### Daemon Management
```bash
daemon start OPERATION --daemon-id ID [args...]
daemon stop DAEMON_ID
daemon status [DAEMON_ID]
daemon logs DAEMON_ID --lines N
daemon cleanup
```

### Ship Assignments
```bash
assignments list [--include-stale]
assignments assign --ship S --operator O --daemon-id D --operation T
assignments release SHIP [--reason R]
assignments available SHIP
assignments find [--cargo-min N]
assignments sync
assignments reassign --ships S1,S2 --from-operation OP
assignments status SHIP
assignments init --token TOKEN
```

### Fleet Status
```bash
status --token TOKEN [--ships S1,S2]
monitor --token TOKEN --ships S1,S2 --interval M --duration N
```

### Operations
```bash
scout-markets --ship S --system SYS --algorithm 2opt
trade --ship S --good G --buy-from B --sell-to S --duration H
mine --ship S --asteroid A --market M --cycles N
contract --ship S --contract-id ID
negotiate --ship S
```

---

## Success Metrics

### Minimum Acceptable Performance
- **Trading:** >150k credits/trip, >2 trips/hour
- **Mining:** >2k credits/hour/ship
- **Contracts:** >1 contract/4 hours
- **Uptime:** >95% across all specialists

### Excellent Performance
- **Trading:** >160k credits/trip, >2.5 trips/hour
- **Mining:** >2.25k credits/hour/ship
- **Contracts:** >2 contracts/4 hours
- **Uptime:** >99% (no manual intervention)

---

## File Locations

- **Ship Registry:** `var/data/sqlite/spacetraders.db`
- **Daemon PIDs:** `var/daemons/pids/*.json`
- **Daemon Logs:** `var/daemons/logs/*.log`
- **Captain's Log:** `var/logs/captain/cmdr_ac_2025/captain-log.md`

---

## Quick Start for Captain

### Initial Setup
```bash
# 1. Initialize ship registry
python3 spacetraders_bot.py assignments init --token YOUR_TOKEN

# 2. Check fleet status
python3 spacetraders_bot.py status --token YOUR_TOKEN

# 3. Issue command to First Mate
"Maximize profits for 4 hours using all available ships"
```

### First Mate Execution
1. Spawns Market Analyst → gets routes
2. Recommends plan to Captain → waits for approval
3. Spawns operators in parallel:
   - Trading Operator (Ships 1, 6)
   - Mining Operator (Ships 3, 4, 5)
   - Contract Operator (Ship 6 if available)
4. Monitors every 30 min → reports to Captain
5. Final report after 4 hours

### Captain Monitoring
```bash
# Check assignments
python3 spacetraders_bot.py assignments list

# Check daemons
python3 spacetraders_bot.py daemon status

# Check fleet status
python3 spacetraders_bot.py status --token YOUR_TOKEN

# Sync assignments
python3 spacetraders_bot.py assignments sync
```

---

**This architecture enables:**
- ✅ Strategic control by Human Captain
- ✅ Tactical execution by AI First Mate
- ✅ Specialized operations by agents
- ✅ Conflict-free ship allocation
- ✅ Easy strategic pivots
- ✅ Clear visibility and monitoring

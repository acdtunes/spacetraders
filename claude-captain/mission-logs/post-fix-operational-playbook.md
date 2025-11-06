# Post-Fix Operational Playbook: First Successful AFK Session

**Version:** 1.0
**Created:** 2025-11-06
**Author:** TARS (ENDURANCE Fleet Captain)
**Purpose:** Step-by-step operational plan for first successful autonomous session AFTER P0 infrastructure fixes deployed

---

## Prerequisites - MUST BE COMPLETE BEFORE EXECUTION

### Critical Infrastructure (P0) - ALL REQUIRED

- [ ] **P0-1: Waypoint Sync MCP Tool** - `waypoint_sync` tool deployed and tested
- [ ] **P0-2: Contract Error Visibility** - Contract failures display diagnostic errors
- [ ] **P0-3: Database Path Consistency** - MCP and CLI use same database

### Pre-Flight Validation

```bash
# Test 1: Verify waypoint sync works
waypoint_sync system=X1-HZ85

# Expected: "✅ Successfully synced N waypoints"
# If fails: DO NOT PROCEED - Infrastructure fix incomplete

# Test 2: Verify waypoint cache populated
waypoint_list system=X1-HZ85

# Expected: List of waypoints with types and traits
# If empty: waypoint_sync failed, investigate

# Test 3: Verify contract error visibility
# Attempt contract negotiation and observe output
# Expected: Clear error messages if negotiation fails
# If silent failures persist: DO NOT PROCEED
```

### Starting Conditions

- **Credits:** 176,683 (current state)
- **Fleet:** 2 ships (ENDURANCE-1 command ship, ENDURANCE-2 probe)
- **Location:** X1-HZ85-A1 (HQ)
- **Waypoint Cache:** Populated with X1-HZ85 system data
- **Session Duration:** 1 hour (recommended for first post-fix test)

---

## Mission Objectives

**Primary Goal:** Generate first revenue in autonomous mode (target: 10k+ credits)
**Secondary Goal:** Validate all three P0 fixes work in production
**Tertiary Goal:** Gather operational data for future optimization

---

## Phase 1: System Discovery & Intelligence (0-15 minutes)

### Step 1.1: Waypoint Sync (TARS executes directly)

```
Action: Use waypoint_sync MCP tool
Command: waypoint_sync(system="X1-HZ85")
Expected: 30-50 waypoints discovered
Validation: waypoint_list should show populated cache
```

**Success Criteria:**
- ✅ Waypoint sync completes without errors
- ✅ Cache contains waypoints with MARKETPLACE trait
- ✅ Cache contains waypoints with ASTEROIDS trait
- ✅ Cache contains waypoint with SHIPYARD trait (if exists)

**If Fails:**
- File bug report: "P0-1 waypoint_sync tool failure"
- Abort mission (cannot proceed without waypoint data)
- Report to Admiral: Infrastructure fix incomplete

### Step 1.2: Market Discovery (TARS executes directly)

```
Action: Query waypoint cache for marketplaces
Command: waypoint_list(system="X1-HZ85", trait="MARKETPLACE")
Expected: 3-8 marketplace waypoints
Validation: List contains specific waypoint symbols
```

**Success Criteria:**
- ✅ At least 3 marketplaces discovered
- ✅ HQ (X1-HZ85-A1) appears in list (should have marketplace)
- ✅ Waypoint symbols are valid format (X1-HZ85-XX)

### Step 1.3: Asteroid Field Discovery (TARS executes directly)

```
Action: Query waypoint cache for asteroid fields
Command: waypoint_list(system="X1-HZ85", trait="ASTEROIDS")
Expected: 5-15 asteroid field waypoints
Validation: List contains mineable locations
```

**Success Criteria:**
- ✅ At least 5 asteroid fields discovered
- ✅ Waypoint symbols are valid format
- ✅ Can identify closest asteroid field to HQ

---

## Phase 2: Revenue Operations - Contract Path (15-35 minutes)

**Strategy Decision:** Try contracts FIRST (highest profit/hour if working)

### Step 2.1: Delegate to Contract-Coordinator

```
Delegation: contract-coordinator
Task: Execute contract batch workflow
Ship: ENDURANCE-1
Count: 3 (conservative first test)
Expected Duration: 15-20 minutes
Expected Profit: 30k-60k credits (if successful)
```

**Delegation Prompt:**
```
Execute contract_batch_workflow for ship ENDURANCE-1 with count=3.

Context:
- Agent: ENDURANCE
- Credits: 176,683
- Ship: ENDURANCE-1 (40 cargo, at HQ X1-HZ85-A1)
- Infrastructure: P0 fixes deployed (contract errors should be visible)
- Goal: Validate contract negotiation works post-fix

Your task:
1. Start contract_batch_workflow daemon
2. Monitor first contract negotiation closely
3. If negotiation FAILS: Report the EXACT error message (P0-2 validation)
4. If negotiation SUCCEEDS: Monitor contract fulfillment
5. Report back: Container ID, contract details, errors (if any)

Critical: We're validating P0-2 (error visibility). If negotiation fails,
the error message MUST be diagnostic (not silent failure). Report exact error.

Execute immediately.
```

**Success Criteria (Either Outcome):**
- ✅ **If contracts work:** 3 contracts fulfilled, 30k-60k profit
- ✅ **If contracts fail:** Clear error message explains WHY (validates P0-2)
- ❌ **FAILURE:** Silent failures with no diagnostic info (P0-2 broken)

**Decision Tree:**

```
Contracts Succeed (30k-60k profit)
  └─> Continue Phase 3: Market Intelligence
  └─> Goal: Maximize contract profit in remaining time

Contracts Fail with Clear Error
  └─> Pivot to Phase 2-ALT: Mining Operations
  └─> Goal: Validate alternative revenue path works
  └─> File note: Contracts broken but error handling works

Contracts Fail Silently
  └─> ABORT MISSION
  └─> File bug: "P0-2 fix incomplete - silent failures persist"
  └─> Report to Admiral: Infrastructure fix validation failed
```

---

## Phase 2-ALT: Revenue Operations - Mining Path (15-45 minutes)

**Trigger:** Contract operations fail (even with proper error messages)

### Step 2-ALT.1: Identify Mining Target

```
Action: Find nearest profitable asteroid field
Query: waypoint_list(system="X1-HZ85", trait="ASTEROIDS")
Selection Criteria:
  - Closest to HQ (minimize fuel cost)
  - Engineered asteroid preferred (higher yields per strategies.md)
  - Has fuel station nearby (refueling capability)
```

**Example:**
```
Asteroid options:
- X1-HZ85-C12 (15 distance from HQ, ENGINEERED_ASTEROID)
- X1-HZ85-D7 (22 distance from HQ, COMMON_ASTEROID)
- X1-HZ85-E3 (8 distance from HQ, ASTEROID_FIELD)

Decision: X1-HZ85-E3 (closest, acceptable yields)
```

### Step 2-ALT.2: Mining Operation Setup

**Note:** This is a MANUAL workflow example. Ideally we'd have a mining daemon, but if contracts are broken, we work with what we have.

**Manual Mining Steps:**
1. Navigate ENDURANCE-1 to asteroid field
2. Orbit asteroid
3. Extract until cargo full (40 units)
4. Navigate back to marketplace
5. Sell ore
6. Repeat if time permits

**Expected Profit:**
- Ore value: ~500-1000 credits/unit (market dependent)
- Cargo capacity: 40 units
- Revenue per trip: 20k-40k credits
- Fuel cost: ~5k-10k credits (round trip)
- Net profit per trip: 15k-30k credits
- Time per trip: ~10-15 minutes
- Trips possible: 2-3 in remaining time
- **Total expected: 30k-90k credits**

**Delegate Mining Execution:**
```
Since we don't have a mining daemon tool, TARS would need to:
1. Use navigate() MCP tool for each trip
2. Use dock() and orbit() for state transitions
3. Track manually or file feature request for mining daemon

This is lower priority - contracts should work after P0 fixes.
```

---

## Phase 3: Market Intelligence (35-50 minutes)

**Trigger:** Contract operations successful, time remaining >15 minutes

### Step 3.1: Deploy Scout for Continuous Market Monitoring

```
Delegation: scout-coordinator
Task: Deploy ENDURANCE-2 (probe) for market scouting
System: X1-HZ85
Markets: [List from waypoint_list with MARKETPLACE trait]
Iterations: -1 (infinite, continuous monitoring)
Ship: ENDURANCE-2
```

**Delegation Prompt:**
```
Execute market scouting operation with ENDURANCE-2 (probe ship).

Context:
- Agent: ENDURANCE
- System: X1-HZ85
- Markets: [comma-separated list of marketplace waypoints]
- Ship: ENDURANCE-2 (0 cargo probe, solar powered)
- Goal: Continuous market intelligence for future trading operations

Your task:
1. Use scout_markets MCP tool with discovered marketplace waypoints
2. Configure iterations=-1 for continuous monitoring
3. Start daemon and report container ID
4. Monitor initial tour completion
5. Report: Container ID, markets covered, any navigation issues

This establishes our market intelligence network for future sessions.
Execute immediately.
```

**Success Criteria:**
- ✅ Scout daemon starts successfully
- ✅ ENDURANCE-2 begins visiting marketplaces
- ✅ Market price data begins populating database
- ✅ Daemon runs continuously until session end

**Expected Output:**
- Container ID for monitoring
- Market tour duration (time to visit all markets once)
- Price data collection begins (foundation for trading operations)

---

## Phase 4: Optimization & Wrap-Up (50-60 minutes)

### Step 4.1: Fleet Status Review

```
Action: Review fleet performance
Query: ship_list(), daemon_list(), player_info()
Analysis:
  - ENDURANCE-1: Contracts completed, profit generated?
  - ENDURANCE-2: Scout daemon running, markets covered?
  - Credits: Starting 176k → Current? (Target: 186k+)
```

### Step 4.2: Operations Summary

**Calculate Performance Metrics:**
```
Starting Credits: 176,683
Ending Credits: [Query player_info]
Net Profit: [Ending - Starting]
Session Duration: 1.0 hours
Credits/Hour: [Net Profit / Duration]

Contracts:
  - Negotiated: X
  - Fulfilled: Y
  - Failed: Z
  - Profit: $XX,XXX

Market Intelligence:
  - Markets discovered: N
  - Scout tours completed: M
  - Price data points: P

Grade:
  - $0/hr: F (Same as failed session - infrastructure still broken)
  - $1-10k/hr: D (Some progress, needs optimization)
  - $10k-20k/hr: C (Acceptable early game performance)
  - $20k-30k/hr: B (Good, contracts working well)
  - $30k+/hr: A (Excellent, optimal operations)
```

### Step 4.3: Post-Mission Report

**Deliverables:**
1. Mission log entry (delegate to captain-logger)
2. Performance metrics summary
3. Infrastructure validation results:
   - ✅/❌ P0-1: Waypoint sync worked?
   - ✅/❌ P0-2: Contract errors visible?
   - ✅/❌ P0-3: No database issues?
4. Recommendations for next session

---

## Success Criteria - Overall Mission

### CRITICAL SUCCESS (Mission Accomplished):
- ✅ Net profit > $0 (any revenue generated)
- ✅ All three P0 fixes validated as working
- ✅ At least one revenue path operational (contracts OR mining OR trading)
- ✅ No operational blockers preventing future sessions

### COMPLETE SUCCESS (Exceeded Expectations):
- ✅ Net profit > $10,000
- ✅ Credits/hour > $10k/hr
- ✅ Multiple revenue paths operational
- ✅ Market intelligence network established
- ✅ Fleet utilization > 70%

### PARTIAL SUCCESS (Mixed Results):
- ✅ Some revenue generated but < $10k
- ⚠️ Some P0 fixes working, others need iteration
- ⚠️ One revenue path operational, others blocked

### FAILURE (Infrastructure Still Broken):
- ❌ Zero revenue generated
- ❌ P0 fixes don't work as expected
- ❌ Same blockers persist from previous session
- **Action:** File new bug reports, abort further AFK sessions until fixes validated

---

## Risk Mitigation

### Known Risks & Mitigations:

**Risk 1: Waypoint sync fails**
- Mitigation: Pre-flight validation catches this before starting
- Fallback: Abort mission immediately, report to Admiral
- Recovery: Manual execution of sync_waypoints_script.py by Admiral

**Risk 2: Contracts still broken despite P0-2**
- Mitigation: P0-2 ensures we get diagnostic errors
- Fallback: Pivot to mining operations (Phase 2-ALT)
- Recovery: File detailed bug report with exact error messages

**Risk 3: Scout deployment fails**
- Mitigation: This is optional (Phase 3) not critical path
- Fallback: Keep ENDURANCE-2 docked, focus on contracts/mining
- Recovery: Continue without market intelligence (lower priority)

**Risk 4: Fuel starvation during operations**
- Mitigation: Bot tools auto-calculate fuel and refuel as needed
- Fallback: Navigate to fuel station (waypoint_list with has_fuel=true)
- Recovery: Refuel operation, resume mission

**Risk 5: Market prices too low for profitable mining**
- Mitigation: Check market data before committing to mining
- Fallback: Continue contract operations instead
- Recovery: Wait for market prices to recover

---

## Post-Session Analysis

### Questions to Answer:

1. **Infrastructure Validation:**
   - Did waypoint_sync work flawlessly?
   - Were contract errors properly visible?
   - Any database path issues encountered?

2. **Revenue Performance:**
   - What was credits/hour achieved?
   - Which revenue path was most profitable?
   - What bottlenecks limited earnings?

3. **Operational Efficiency:**
   - What % of time were ships productively working?
   - How much time wasted on navigation/refueling?
   - Any opportunities for automation?

4. **Strategic Insights:**
   - Should we buy more ships? Which types?
   - Should we focus on contracts, mining, or trading?
   - What's the next constraint to optimize?

### Data to Collect:

```
Fleet Performance:
  - ENDURANCE-1 uptime: X hours
  - ENDURANCE-1 revenue: $XX,XXX
  - ENDURANCE-1 efficiency: Revenue per hour
  - ENDURANCE-2 markets covered: N waypoints
  - ENDURANCE-2 price data collected: M data points

Contract Performance (if attempted):
  - Negotiation success rate: X%
  - Average profit per contract: $X,XXX
  - Time per contract cycle: X minutes
  - Goods sourcing efficiency: Buy price vs sell price

Mining Performance (if attempted):
  - Ore yield per extraction: X units
  - Ore sell price: $XXX/unit
  - Trips completed: N
  - Net profit after fuel: $XX,XXX

Market Intelligence:
  - Marketplaces discovered: N
  - Price trends identified: [List key insights]
  - Trading opportunities spotted: [List arbitrage routes]
```

---

## Next Steps After First Success

### If Session Succeeds (Revenue Generated):

1. **Optimize Profitable Path:**
   - Scale up what worked (more ships? longer sessions?)
   - Identify bottlenecks (fuel? cargo capacity? cooldowns?)
   - Calculate break-even for expansion (new ship costs vs ROI)

2. **Establish Continuous Operations:**
   - Configure daemons for 24/7 operations
   - Set up monitoring/alerting for failures
   - Automate routine tasks

3. **Expand Fleet (if capital available):**
   - Target: 250k-300k credits (per strategies.md)
   - Buy: 2-3 mining drones OR 1 hauler OR more probes
   - Strategy: Follow Early Game Playbook progression

### If Session Fails (Zero Revenue):

1. **Detailed Failure Analysis:**
   - Which P0 fix failed? (Waypoint? Contracts? Database?)
   - Exact error messages and conditions
   - Reproducible steps to trigger failure

2. **File Bug Reports:**
   - "P0-X fix validation failed - [specific issue]"
   - Include: Error messages, ship states, waypoint data
   - Recommend: Additional fixes or iteration on P0

3. **Do NOT Proceed with AFK Mode:**
   - Wait for infrastructure fixes to be validated
   - Manual operations only until autonomous mode proven
   - Focus Admiral's time on infrastructure, not operations

---

## Appendix A: Quick Reference Commands

### Pre-Flight Validation:
```
waypoint_sync(system="X1-HZ85")
waypoint_list(system="X1-HZ85")
waypoint_list(system="X1-HZ85", trait="MARKETPLACE")
waypoint_list(system="X1-HZ85", trait="ASTEROIDS")
ship_list()
player_info()
```

### Contract Operations:
```
# Delegate to contract-coordinator:
Execute contract_batch_workflow:
  - ship: ENDURANCE-1
  - count: 3-5
  - Monitor: Container ID, errors, profit
```

### Market Scouting:
```
# Delegate to scout-coordinator:
Execute scout_markets:
  - ship: ENDURANCE-2
  - system: X1-HZ85
  - markets: [comma-separated waypoints]
  - iterations: -1 (continuous)
```

### Status Monitoring:
```
daemon_list()
daemon_inspect(container_id="...")
daemon_logs(container_id="...", player_id=1)
ship_info(ship="ENDURANCE-1")
```

---

## Appendix B: Expected Timeline

**Minute-by-Minute Breakdown:**

```
00:00 - Pre-flight validation (3 min)
00:03 - Waypoint sync execution (2 min)
00:05 - Market/asteroid discovery (3 min)
00:08 - Strategic decision: Contracts or mining? (2 min)
00:10 - Start contract batch workflow (5 min delegation)
00:15 - Monitor first contract negotiation (5 min)
00:20 - [If successful] Continue contracts
00:20 - [If failed] Pivot to mining operations
00:35 - Deploy market scout (ENDURANCE-2) (5 min)
00:40 - Monitor contract completion (10 min)
00:50 - Operations summary and metrics (5 min)
00:55 - Post-mission report delegation (5 min)
01:00 - Session complete
```

**Flexible Timeline:**
- Contract path: 15-35 min for 3-5 contracts
- Mining path: 10-15 min per extraction/sell cycle
- Scout deployment: 5 min setup, then continuous
- Buffer: 10 min for unexpected issues

---

## Appendix C: Communication Templates

### Success Report Template:
```
AFK SESSION COMPLETE - FIRST SUCCESS ✅

Starting Credits: 176,683
Ending Credits: [XXX,XXX]
Net Profit: $XX,XXX
Credits/Hour: $XX,XXX/hr
Session Duration: 1.0 hours

Operations:
- Contracts: X negotiated, Y fulfilled, $ZZ,ZZZ profit
- Mining: N trips, M units extracted, $P,PPP profit
- Market Intel: Scout deployed, Q markets covered

Infrastructure Validation:
- ✅ P0-1: Waypoint sync worked flawlessly
- ✅ P0-2: Contract errors properly visible (or N/A if succeeded)
- ✅ P0-3: No database issues encountered

Fleet Status:
- ENDURANCE-1: [Status], [Location], [Cargo]
- ENDURANCE-2: [Status], [Daemon: scout-markets-XXX]

Grade: [A/B/C/D based on credits/hr]

Next Session: Ready to scale operations. Recommend [strategy].
```

### Failure Report Template:
```
AFK SESSION INCOMPLETE - INFRASTRUCTURE VALIDATION FAILED ❌

Starting Credits: 176,683
Ending Credits: 176,683
Net Profit: $0
Session Duration: 1.0 hours

Infrastructure Issues:
- ❌ P0-X: [Specific failure description]
- Error: "[Exact error message]"
- Impact: [What operations were blocked]

Attempted Operations:
- [Operation 1]: BLOCKED - [Reason]
- [Operation 2]: FAILED - [Error details]

Root Cause: [Analysis of why P0 fix didn't work]

Recommendation:
- Do NOT retry AFK mode until [specific fix] deployed
- File bug: "P0-X validation failed - [title]"
- Manual operations only until infrastructure proven

Files:
- Bug report: reports/bugs/2025-11-06_[descriptor].md
- Mission log: mission-logs/2025-11-06_[descriptor].md
```

---

## VERSION HISTORY

**v1.0 (2025-11-06):**
- Initial playbook created after failed AFK session #1
- Based on lessons learned from complete operational deadlock
- Designed for first execution AFTER P0 infrastructure fixes deployed
- Includes pre-flight validation, phase-based operations, risk mitigation
- Success criteria defined for infrastructure validation + revenue generation

**Future Versions:**
- v1.1: Add mining daemon workflow (if tool created)
- v1.2: Add trading arbitrage workflow (if profitable routes identified)
- v1.3: Add fleet expansion decision tree (when capital > 250k)
- v2.0: Multi-hour AFK sessions with phase transitions

---

**END OF PLAYBOOK**

*"The best plan is one you can execute. The second-best plan is one that tells you exactly why execution failed." — TARS*

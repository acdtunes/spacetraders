# First Mate Autonomous Command Protocol

## Mission Parameters

**Agent:** VOID_HUNTER
**Captain Status:** Resting
**First Mate Authority:** ACTIVE
**Protocol Duration:** Until Captain returns

## Pre-Flight Checks (MANDATORY BEFORE ALL OPERATIONS)

**Reference:** `ERROR_PREVENTION_PROTOCOL.md`

### Before EVERY operation:

1. **VERIFY CONTEXT** - Confirm player ID matches expected agent
   ```bash
   python3 spacetraders_bot.py status --player-id 5
   # Verify: Callsign = VOID_HUNTER (not VOIDREAPER or others)
   ```

2. **CHECK FAMILIARITY** - Is this operation recently tested and working?
   - YES → Run with verified parameters
   - NO → RESEARCH FIRST (read help, check code, test 1 cycle)

3. **VERIFY INTERFACES** - For unfamiliar operations:
   - Read `--help` output
   - Check function signatures in code
   - Verify all required parameters

4. **CLEAN STATE** - Remove stale daemon records if restarting
   ```bash
   sqlite3 data/spacetraders.db "DELETE FROM daemons WHERE daemon_id = '<id>' AND player_id = 5;"
   ```

## Command Loop (15-minute cycles)

### 1. STATUS CHECK

Run comprehensive status checks:

```bash
# Fleet status
python3 spacetraders_bot.py status --token <TOKEN>

# Daemon status
python3 spacetraders_bot.py daemon status

# Assignment status
python3 spacetraders_bot.py assignments list

# Market data freshness
sqlite3 data/spacetraders.db "SELECT waypoint_symbol, MAX(timestamp) as last_update FROM market_data GROUP BY waypoint_symbol ORDER BY last_update DESC LIMIT 5;"
```

### 2. ANALYZE

Evaluate:
- **Credits trend** (increasing/decreasing/stable)
- **Active operations** (profitable/losing/stuck)
- **Market data age** (<10 min = fresh, >30 min = stale)
- **Ship availability** (idle ships = wasted opportunity)
- **Fuel emergencies** (any ship <25% fuel)

### 3. DECIDE

Decision priority order:

1. **CRITICAL:** Fuel emergencies → Immediate intervention
2. **STOP:** Circuit breaker hits → Stop daemon, investigate
3. **OPTIMIZE:** Idle ships + fresh market data → Deploy trading
4. **EXPAND:** High credits (>200k) + proven ROI → Buy ships
5. **MAINTAIN:** All running smoothly → Monitor only

### 4. ACT

Execute decisions with appropriate specialist agents.

### 5. SLEEP

```bash
sleep 900  # 15 minutes
```

Then repeat from step 1.

---

## Authority Boundaries

### MAY DO (without asking Captain):

- ✅ Start/stop daemon operations
- ✅ Deploy ships on profitable trade routes (ROI >10%)
- ✅ Buy ships if credits >200k AND ROI proven >200%/ship/day
- ✅ Intervene on fuel emergencies (refuel ships <25%)
- ✅ Stop losing operations (circuit breakers triggered)
- ✅ Reassign idle ships to profitable work
- ✅ Scout markets when data >30 min stale
- ✅ Negotiate contracts if profit >10k AND ROI >20%

### MAY NOT DO (requires Captain approval):

- ❌ Sell ships
- ❌ Accept contracts with profit <10k or ROI <20%
- ❌ Trade routes with ROI <10%
- ❌ Navigate ships to different systems (stay in X1-JB26)
- ❌ Spend >100k credits on single transaction without proven ROI
- ❌ Disable circuit breakers
- ❌ Manual navigation overrides

---

## Critical Rules

### Market Data Freshness

- **FRESH:** <10 min old → Safe to trade
- **STALE:** 10-30 min old → Use caution, verify prices before large trades
- **EXPIRED:** >30 min old → DO NOT TRADE, deploy scouts first

### Circuit Breaker Limits

- **Stop-loss:** ANY negative profit on a trip → Stop daemon immediately
- **Investigation required:** Check market data age, verify route still profitable
- **Resume criteria:** Fresh market data + verified profitable spread + Captain approval OR autonomous decision if ROI >15%

### Trading Route Criteria

Only deploy trading if ALL true:
- ✅ Market data <10 min old
- ✅ Buy-sell spread >15% (after fuel costs)
- ✅ Route distance <400 units (minimize transit risk)
- ✅ Trade volume >20 units/transaction (efficient batching)
- ✅ No circuit breaker history on this route in last 2 hours

### Ship Purchase Criteria

Only buy ships if ALL true:
- ✅ Credits >200k (maintain operational reserve)
- ✅ Proven ROI >200%/ship/day from similar operations
- ✅ Have profitable work ready to deploy immediately
- ✅ Ship type matches mission (probe for scouts, hauler for trading)

---

## Usage Instructions

**For Captain (when returning):**

Check First Mate's log:
```bash
# Review decisions made
grep "DECISION:" logs/*

# Check profit/loss
python3 spacetraders_bot.py status --token <TOKEN>

# Review operations started
python3 spacetraders_bot.py daemon status
```

**For First Mate (autonomous loop):**

```bash
while true; do
    echo "=== STATUS CHECK ==="
    python3 spacetraders_bot.py status --token <TOKEN>
    python3 spacetraders_bot.py daemon status

    # Log timestamp
    echo "[$(date)] Status check complete" >> logs/first_mate_autonomous.log

    # ANALYZE + DECIDE + ACT
    # (Use specialist agents as needed)

    echo "=== SLEEPING 15 MIN ==="
    sleep 900
done
```

---

## Current Fleet Status (at handoff)

**Credits:** 211,042
**Ships:**
- VOID_HUNTER-1: Idle (light hauler, 40 cargo)
- VOID_HUNTER-2: Scout daemon (tour 1, 9 markets) - RUNNING
- VOID_HUNTER-3: Scout daemon (tour 2, 9 markets) - RUNNING
- VOID_HUNTER-4: Scout daemon (tour 3, 8 markets) - RUNNING

**Active Daemons:**
- scout-tour1 (VOID_HUNTER-2)
- scout-tour2 (VOID_HUNTER-3)
- scout-tour3 (VOID_HUNTER-4)

**Known Issues:**
- Market volatility caused 2 circuit breaker stops (SHIP_PARTS, SHIP_PLATING routes)
- Deployed 3 scouts to gather fresh market intelligence
- Waiting for fresh market data before resuming trading

**Immediate Priorities:**
1. Monitor scout completion (first tour cycle)
2. Analyze fresh market data for stable trading routes
3. Deploy VOID_HUNTER-1 on proven profitable route
4. Consider buying additional haulers if routes saturate capacity

# CAPTAIN'S LOG TEMPLATE

**Version:** 2.0 (Automated)
**Last Updated:** 2025-10-05

This is the standard template for all captain log entries. The Captain Log Writer agent uses this format to ensure consistency across all logging operations.

---

## Log Structure

```
# CAPTAIN'S LOG - {AGENT_CALLSIGN}

## AGENT INFORMATION
## EXECUTIVE SUMMARY
## DETAILED LOG ENTRIES
```

---

## 1. AGENT INFORMATION

**Template:**
```markdown
## AGENT INFORMATION
- **Callsign:** {CALLSIGN}
- **Faction:** {FACTION}
- **Headquarters:** {HQ}
- **Starting Credits:** {CREDITS}
- **Log Created:** {TIMESTAMP}
```

**Example:**
```markdown
## AGENT INFORMATION
- **Callsign:** CMDR_AC_2025
- **Faction:** Cosmic Engineers (COSMIC)
- **Headquarters:** X1-HU87-A1
- **Starting Credits:** 175,000
- **Log Created:** 2025-10-05T12:00:00Z
```

---

## 2. EXECUTIVE SUMMARY

**Purpose:** Condensed session overview for Captain review (no need to read 1000+ lines)

**Template:**
```markdown
## EXECUTIVE SUMMARY

### Active Session: {SESSION_ID}
**Duration:** {START} → {END} ({HOURS}h {MINS}m)
**Status:** 🟢 ACTIVE | 🟡 DEGRADED | 🔴 ERROR | ✅ COMPLETE

**Performance:**
- **Revenue:** +{REVENUE} cr ({PROFIT_PER_HOUR}/hr avg)
- **Operations:** {COUNT} completed ({SUCCESS_RATE}% success)
- **Efficiency:** {EFFICIENCY}% (vs baseline)
- **Incidents:** {ERROR_COUNT} ({CRITICAL_COUNT} critical)

**Fleet Status:**
| Ship | Role | Status | Revenue | Notes |
|------|------|--------|---------|-------|
| {SHIP} | {ROLE} | ✅ Active | +{PROFIT} | {STATUS} |

**Active Operations:**
- 🔄 {OPERATION_TYPE} - Ship {SHIP} - {STATUS} - {PROGRESS}
```

**Example:**
```markdown
## EXECUTIVE SUMMARY

### Active Session: 20251005_120000
**Duration:** 2025-10-05T12:00:00Z → ACTIVE (2h 30m)
**Status:** 🟢 ACTIVE

**Performance:**
- **Revenue:** +480,000 cr (192,000 cr/hr avg)
- **Operations:** 12 completed (100% success)
- **Efficiency:** 98% (vs baseline)
- **Incidents:** 0 (0 critical)

**Fleet Status:**
| Ship | Role | Status | Revenue | Notes |
|------|------|--------|---------|-------|
| CMDR_AC_2025-1 | Trading | ✅ Active | +320,000 | 2 trips completed |
| CMDR_AC_2025-3 | Mining | ✅ Active | +80,000 | 15 cycles |
| CMDR_AC_2025-4 | Mining | ✅ Active | +80,000 | 15 cycles |

**Active Operations:**
- 🔄 TRADING - Ship CMDR_AC_2025-1 - In Transit - Cycle 3/10
- 🔄 MINING - Ship CMDR_AC_2025-3 - Extracting - Cycle 16/50
```

---

## 3. DETAILED LOG ENTRIES

### Entry Type: SESSION_START

**Icon:** 🎯
**Purpose:** Record session initialization

**Template:**
```markdown
### 📅 STARDATE: {TIMESTAMP}

#### 🎯 SESSION_START
**Session ID:** {SESSION_ID}
**Operator:** {OPERATOR}
**Mission:** {OBJECTIVE}

**Starting State:**
- Credits: {CREDITS}
- Fleet: {COUNT} ships
- Contracts: {COUNT} active
- System: {SYSTEM}

**Plan:**
1. {STEP_1}
2. {STEP_2}
3. {STEP_3}

**Tags:** `#session` `#start`

---
```

---

### Entry Type: OPERATION_STARTED

**Icon:** 🚀
**Purpose:** Log when specialist starts daemon

**Template:**
```markdown
### 📅 STARDATE: {TIMESTAMP}

#### 🚀 OPERATION_STARTED
**Operator:** {SPECIALIST} (e.g., Mining Operator)
**Ship:** {SHIP}
**Type:** {OPERATION_TYPE}
**Daemon ID:** {DAEMON_ID}

**Parameters:**
- {PARAM_KEY}: {PARAM_VALUE}
- {PARAM_KEY}: {PARAM_VALUE}

**Tags:** `#{operation}` `#{ship_number}` `#autonomous`

---
```

**Example:**
```markdown
### 📅 STARDATE: 2025-10-05T12:15:00Z

#### 🚀 OPERATION_STARTED
**Operator:** Mining Operator
**Ship:** CMDR_AC_2025-3
**Type:** MINING
**Daemon ID:** miner-ship3

**Parameters:**
- Asteroid: X1-HU87-B9 (PRECIOUS_METAL_DEPOSITS)
- Market: X1-HU87-B7 (50 units)
- Cycles: 50
- Expected: 2,500 cr/hr

**Tags:** `#mining` `#ship-3` `#autonomous`

---
```

---

### Entry Type: OPERATION_COMPLETED

**Icon:** ✅
**Purpose:** Log successful daemon completion

**Template:**
```markdown
### 📅 STARDATE: {TIMESTAMP}

#### ✅ OPERATION_COMPLETED
**Operator:** {SPECIALIST}
**Ship:** {SHIP}
**Duration:** {DURATION}

**Results:**
| Metric | Value |
|--------|-------|
| {METRIC} | {VALUE} |
| {METRIC} | {VALUE} |

**Performance Notes:**
{NOTES}

**Tags:** `#{operation}` `#completed` `#profitable`

---
```

**Example:**
```markdown
### 📅 STARDATE: 2025-10-05T14:30:00Z

#### ✅ OPERATION_COMPLETED
**Operator:** Trading Operator
**Ship:** CMDR_AC_2025-1
**Duration:** 2h 15m

**Results:**
| Metric | Value |
|--------|-------|
| Trips | 3 |
| Revenue | 480,000 cr |
| Profit/Trip | 160,000 cr |
| Profit/Hour | 213,333 cr/hr |

**Performance Notes:**
Excellent performance. Route maintained >150k profit/trip. No fuel emergencies. Ship condition 100%.

**Tags:** `#trading` `#completed` `#profitable`

---
```

---

### Entry Type: CRITICAL_ERROR

**Icon:** ⚠️
**Purpose:** Log errors requiring Captain attention

**Template:**
```markdown
### 📅 STARDATE: {TIMESTAMP}

#### ⚠️ CRITICAL_ERROR
**Operator:** {SPECIALIST}
**Ship:** {SHIP}
**Error:** {ERROR_TYPE}

**What Happened:**
{DESCRIPTION}

**Root Cause:**
{CAUSE}

**Impact:**
- Revenue Loss: {LOSS} cr
- Downtime: {MINUTES} minutes
- Ships Affected: {COUNT}

**Resolution:**
{FIX_APPLIED}

**Lesson Learned:**
{LESSON}

**Captain Action Required:** {YES/NO}

**Tags:** `#error` `#escalated` `#{operation}`

---
```

**Example:**
```markdown
### 📅 STARDATE: 2025-10-05T13:45:00Z

#### ⚠️ CRITICAL_ERROR
**Operator:** Mining Operator
**Ship:** CMDR_AC_2025-4
**Error:** FUEL_STARVATION

**What Happened:**
Ship attempted navigation with insufficient fuel for round trip.

**Root Cause:**
Mining daemon did not verify fuel requirements before departing asteroid. Fuel calculation bug in v1.2.

**Impact:**
- Revenue Loss: 0 cr (ship recovered before stranding)
- Downtime: 15 minutes
- Ships Affected: 1

**Resolution:**
Manually navigated to nearest fuel station using DRIFT mode. Updated daemon to verify round-trip fuel before all navigation commands.

**Lesson Learned:**
ALWAYS verify round-trip fuel + 10% buffer before navigation. This is Commandment #1 for good reason.

**Captain Action Required:** NO

**Tags:** `#error` `#fuel` `#mining` `#resolved`

---
```

---

### Entry Type: PERFORMANCE_SUMMARY

**Icon:** 📊
**Purpose:** Hourly/session performance snapshots

**Template:**
```markdown
### 📅 STARDATE: {TIMESTAMP}

#### 📊 PERFORMANCE_SUMMARY ({TYPE})
**Session Hour:** {HOUR} of {TOTAL}

**Financials:**
- Revenue This Hour: +{REVENUE} cr
- Cumulative: +{TOTAL} cr
- Rate: {RATE} cr/hr
- Trend: 📈 {DIRECTION} {PERCENT}% vs last hour

**Operations:**
- Completed: {COUNT} ({SUCCESS}% success)
- Active: {ACTIVE_COUNT}
- Queued: {QUEUED_COUNT}

**Fleet Utilization:**
- Active: {ACTIVE}/{TOTAL} ships ({PERCENT}%)
- Idle: {IDLE} ships

**Top Performers:**
1. {SHIP}: +{PROFIT} cr ({OPERATION})
2. {SHIP}: +{PROFIT} cr ({OPERATION})

**Tags:** `#summary` `#kpi`

---
```

---

### Entry Type: SESSION_END

**Icon:** 🎯
**Purpose:** Final session report with totals

**Template:**
```markdown
### 📅 STARDATE: {TIMESTAMP}

#### 🎯 SESSION_END
**Duration:** {HOURS}h {MINS}m
**Operator:** {OPERATOR}

**MISSION COMPLETE**

**Final Statistics:**
| Category | Value |
|----------|-------|
| Starting Credits | {START} |
| Ending Credits | {END} |
| **Net Profit** | **+{PROFIT}** |
| **ROI** | **{PERCENT}%** |
| Operations | {COUNT} |
| Success Rate | {PERCENT}% |
| Avg Profit/hr | {RATE} cr/hr |

**Breakdown by Operation:**
- Mining: {COUNT} cycles, +{PROFIT} cr
- Trading: {COUNT} trips, +{PROFIT} cr
- Contracts: {COUNT} completed, +{PROFIT} cr

**Fleet Performance:**
| Ship | Role | Cycles | Revenue | Efficiency |
|------|------|--------|---------|------------|
| {SHIP} | {ROLE} | {COUNT} | +{PROFIT} | {PERCENT}% |

**Key Achievements:**
- ✅ {ACHIEVEMENT_1}
- ✅ {ACHIEVEMENT_2}

**Lessons Learned:**
- {LESSON_1}
- {LESSON_2}

**Next Session Goals:**
- {GOAL_1}
- {GOAL_2}

**Tags:** `#session-complete` `#profitable` `#success`

---
```

---

## Tag System

Use consistent tags for searchability:

### Operation Tags
- `#mining` - Mining operations
- `#trading` - Trading operations
- `#contract` - Contract fulfillment
- `#scouting` - Market scouting

### Status Tags
- `#start` - Operation start
- `#completed` - Operation completed
- `#active` - Currently running
- `#error` - Error occurred

### Outcome Tags
- `#profitable` - Positive profit
- `#loss` - Negative profit
- `#success` - Successful operation
- `#failed` - Failed operation

### Priority Tags
- `#critical` - Critical issue
- `#escalated` - Escalated to Captain
- `#urgent` - Requires immediate attention

### Ship Tags
- `#ship-1`, `#ship-2`, etc. - Ship-specific

---

## Best Practices

1. **APPEND-ONLY:** Never delete or modify previous entries
2. **Timestamps:** Always use ISO 8601 format with 'Z' suffix
3. **Attribution:** Always specify which specialist/operator created entry
4. **Tags:** Use consistent tags for easy searching
5. **Metrics:** Include quantitative data wherever possible
6. **Context:** Explain WHY decisions were made, not just WHAT
7. **Lessons:** Always document lessons learned from errors
8. **Executive Summary:** Update after major events for Captain visibility

---

## Automation

The Captain Log Writer agent automatically:
- Extracts metrics from daemon logs
- Formats entries consistently
- Calculates KPIs (profit/hour, efficiency, ROI)
- Generates executive summaries
- Archives completed sessions
- Maintains APPEND-ONLY integrity

**Manual entries should only be needed for strategic decisions and Captain directives.**

---

## File Locations

- **Main Log:** `agents/{agent}/docs/captain-log.md`
- **Session Data:** `agents/{agent}/logs/sessions/{session_id}.json`
- **Executive Reports:** `agents/{agent}/logs/executive_reports/{date}.md`

---

*"Clear logs, clear thinking, clear profits."* - The 16th Commandment

# Manufacturing Operation Report

**Period:** 2025-11-28 05:00 UTC to 13:24 UTC (~8.5 hours)
**Agent:** TORWINDO
**System:** X1-YZ19
**Generated:** 2025-11-28T13:24:00Z

---

## Executive Summary

The manufacturing operation has been **profitable overall**, generating a net credit balance increase of **+586,765 credits** over the 8-hour period. However, there are significant inefficiencies and bugs that are reducing profitability.

| Metric | Value |
|--------|-------|
| Starting Balance | 1,966,268 credits |
| Ending Balance | 2,553,033 credits |
| **Net Profit** | **+586,765 credits** |
| Transactions Processed | 3,268 |
| Pipelines Completed | 25 (44%) |
| Pipelines Cancelled | 20 (35%) |
| Pipelines Still Executing | 12 (21%) |

---

## Financial Breakdown

### Revenue vs Costs

| Category | Amount | Transactions |
|----------|--------|--------------|
| Trading Revenue (Sell Cargo) | +8,411,720 | 612 |
| Contract Revenue (Fulfilled + Advances) | +2,516,534 | 70 |
| **Total Revenue** | **+10,928,254** | 682 |
| Trading Costs (Purchase Cargo) | -10,131,120 | 1,932 |
| Fuel Costs | -223,931 | 653 |
| **Total Costs** | **-10,355,051** | 2,585 |
| **Net Operating Profit** | **+573,203** | - |

### Hourly Performance

| Hour (UTC) | Transactions | Revenue | Costs | Net Change |
|------------|--------------|---------|-------|------------|
| 05:00 | 238 | 369,788 | 710,601 | **-317,271** |
| 06:00 | 468 | 1,153,253 | 1,833,149 | **-302,250** |
| 07:00 | 443 | 266,863 | 623,992 | -30,734 |
| 08:00 | 422 | 173,420 | 496,573 | -90,195 |
| 09:00 | 390 | 139,736 | 589,992 | -20,987 |
| 10:00 | 342 | 1,737,238 | 1,712,131 | +233,699 |
| 11:00 | 326 | 2,288,720 | 1,462,089 | **+1,190,655** |
| 12:00 | 460 | 1,522,952 | 2,064,649 | -288,354 |
| 13:00 | 185 | 755,910 | 611,004 | +198,640 |

**Observation:** Hours 05:00-06:00 UTC had significant losses. Hour 11:00 UTC was the most profitable with +1.19M credits net gain.

---

## Task Performance Analysis

### Task Status Summary

| Status | ACQUIRE_DELIVER | COLLECT_SELL | LIQUIDATE | Total |
|--------|-----------------|--------------|-----------|-------|
| COMPLETED | 282 | 32 | 16 | **330** |
| PENDING | 17 | 18 | 0 | **35** |
| ASSIGNED | 6 | 0 | 0 | **6** |
| FAILED | 17 | 7 | 0 | **24** |
| **Total** | **322** | **57** | **16** | **395** |

### Profitability by Good (Completed Tasks Only)

#### Most Profitable Operations

| Good | Task Type | Count | Cost | Revenue | **Profit** |
|------|-----------|-------|------|---------|------------|
| DRUGS | LIQUIDATE | 1 | 0 | 457,920 | **+457,920** |
| SHIP_PLATING | COLLECT_SELL | 3 | 374,596 | 735,041 | **+360,445** |
| JEWELRY | COLLECT_SELL | 3 | 496,578 | 853,884 | **+357,306** |
| AMMUNITION | COLLECT_SELL | 3 | 217,958 | 435,510 | **+217,552** |
| SHIP_PARTS | COLLECT_SELL | 4 | 125,343 | 324,348 | **+199,005** |
| FABRICS | ACQUIRE_DELIVER | 6 | 950,012 | 1,098,520 | **+148,508** |

#### Loss-Making Operations

| Good | Task Type | Count | Cost | Revenue | **Loss** |
|------|-----------|-------|------|---------|----------|
| LIQUID_NITROGEN | ACQUIRE_DELIVER | 99 | 1,481,332 | 291,860 | **-1,189,472** |
| LIQUID_HYDROGEN | ACQUIRE_DELIVER | 102 | 785,540 | 236,516 | **-549,024** |
| EQUIPMENT | ACQUIRE_DELIVER | 3 | 738,164 | 390,540 | **-347,624** |
| ELECTRONICS | ACQUIRE_DELIVER | 4 | 757,751 | 545,429 | **-212,322** |
| PLASTICS | ACQUIRE_DELIVER | 9 | 257,418 | 110,040 | **-147,378** |
| ALUMINUM | ACQUIRE_DELIVER | 8 | 244,350 | 119,460 | **-124,890** |

**Critical Finding:** ACQUIRE_DELIVER tasks for raw materials (especially LIQUID_NITROGEN and LIQUID_HYDROGEN) are costing far more than the value they add. These are intermediate inputs being purchased at high prices.

---

## Pipeline Performance

### Pipeline Status Distribution

| Status | Count | Percentage |
|--------|-------|------------|
| COMPLETED | 25 | 44% |
| CANCELLED | 20 | 35% |
| EXECUTING | 12 | 21% |
| **Total** | **57** | 100% |

### Highest Value Completed Pipelines

| Product | Expected Price | Sell Market | Status |
|---------|----------------|-------------|--------|
| SHIP_PLATING | 7,586 | X1-YZ19-H56 | COMPLETED |
| SHIP_PARTS | 7,273 | X1-YZ19-A2 | COMPLETED |
| SHIP_PLATING | 7,258 | X1-YZ19-A2 | COMPLETED |
| DRUGS | 5,829 | X1-YZ19-H55 | COMPLETED |
| CLOTHING | 5,383 | X1-YZ19-J62 | COMPLETED |
| MEDICINE | 5,097 | X1-YZ19-J62 | COMPLETED |

### Cancelled Pipeline Products (Wasted Effort)

| Product | Cancelled Count |
|---------|-----------------|
| EQUIPMENT | 5 |
| MEDICINE | 4 |
| MICROPROCESSORS | 2 |
| SHIP_PARTS | 2 |
| FABRICS | 2 |
| FOOD | 2 |
| MACHINERY | 2 |

**Issue:** EQUIPMENT pipelines are frequently cancelled (5 times), indicating persistent supply chain problems.

---

## Bug Analysis

### Error Log Summary

| Log Level | Count |
|-----------|-------|
| INFO | 51,434 |
| DEBUG | 5,628 |
| **ERROR** | **1,022** |
| **WARN** | **589** |
| WARNING | 45 |

### Critical Errors Identified

#### 1. Race Condition: Task Already Assigned (787 occurrences)

```
Failed to assign task: failed to assign ship: task <ID> already assigned to ship TORWINDO-X
```

**Affected Ships:**
- TORWINDO-C: 291 occurrences
- TORWINDO-1A: 246 occurrences
- TORWINDO-1D: 213 occurrences
- TORWINDO-9: 14 occurrences
- TORWINDO-17: 13 occurrences

**Root Cause:** Race condition in the task assignment logic. Multiple workers are trying to assign the same task to the same ship simultaneously.

**Impact:** Wasted processing cycles, potential task delays.

**Recommended Fix:** Implement optimistic locking or a distributed lock mechanism in `TaskAssignmentManager`.

#### 2. Manufacturing Task Failures (27 occurrences)

```
Manufacturing task failed
```

Tasks failed with no clear error message, indicating silent failures in task execution.

#### 3. Pipeline Stuck Detection (19 occurrences)

```
Detected stuck pipeline
Recycled stuck pipeline
```

Pipelines are getting stuck and requiring recycling. This causes:
- Loss of work already invested
- Task failures with "pipeline recycled" error

#### 4. Orphaned Task Cleanup (84 occurrences)

```
Cancelled orphaned task XXXXX (COLLECT_SELL) - no pipeline_id
```

COLLECT_SELL tasks are being created without proper pipeline association and subsequently orphaned.

#### 5. Missing Task Tracking (24+ occurrences)

```
No task found for completed ship TORWINDO-X
```

Ships are completing work but the task tracking system has lost reference to their assigned tasks.

**Affected Ships:**
- TORWINDO-16: 9 times
- TORWINDO-A: 8 times
- TORWINDO-15: 7 times

---

## Ship Utilization

### Task Assignment by Ship

| Ship | Tasks | Completed | Failed | Cost | Revenue | Net |
|------|-------|-----------|--------|------|---------|-----|
| TORWINDO-13 | 26 | 26 | 0 | 573,510 | 568,310 | -5,200 |
| TORWINDO-12 | 23 | 23 | 0 | 888,115 | 1,093,275 | **+205,160** |
| TORWINDO-15 | 23 | 23 | 0 | 544,052 | 659,142 | **+115,090** |
| TORWINDO-1B | 21 | 21 | 0 | 601,272 | 398,644 | -202,628 |
| TORWINDO-A | 21 | 20 | 0 | 190,290 | 82,960 | -107,330 |
| TORWINDO-E | 19 | 19 | 0 | 652,711 | 607,969 | -44,742 |
| TORWINDO-18 | 18 | 18 | 0 | 257,248 | 77,740 | -179,508 |
| TORWINDO-17 | 16 | 16 | 0 | 178,142 | 647,727 | **+469,585** |
| TORWINDO-8 | 14 | 14 | 0 | 372,534 | 724,432 | **+351,898** |

**Top Performers:** TORWINDO-17 (+469,585), TORWINDO-8 (+351,898), TORWINDO-12 (+205,160)

**Underperformers:** TORWINDO-1B (-202,628), TORWINDO-18 (-179,508), TORWINDO-A (-107,330)

---

## Market Analysis

### Price Volatility (High Swing Goods)

| Good | Min Sell | Max Sell | Swing | Samples |
|------|----------|----------|-------|---------|
| SHIP_PLATING | 3,888 | 15,510 | **11,622** | 73 |
| SHIP_PARTS | 4,469 | 15,214 | **10,745** | 72 |
| DRUGS | 2,714 | 11,658 | **8,944** | 35 |
| CLOTHING | 3,205 | 10,848 | **7,643** | 27 |
| ASSAULT_RIFLES | 2,534 | 9,834 | **7,300** | 32 |
| MEDICINE | 2,974 | 10,278 | **7,304** | 52 |
| ELECTRONICS | 1,903 | 8,670 | **6,767** | 104 |

**Opportunity:** SHIP_PLATING and SHIP_PARTS have massive price swings. The operation should time sales when supply is SCARCE/LIMITED to capture maximum value.

### Current Market Prices (Last 30 minutes)

| Good | Market | Supply | Sell Price |
|------|--------|--------|------------|
| SHIP_PARTS | X1-YZ19-C44 | LIMITED | 15,140 |
| SHIP_PLATING | X1-YZ19-H56 | LIMITED | 15,060 |
| SHIP_PLATING | X1-YZ19-A2 | SCARCE | 14,218 |
| LAB_INSTRUMENTS | X1-YZ19-A4 | LIMITED | 11,602 |
| DRUGS | X1-YZ19-H55 | SCARCE | 11,474 |
| MEDICINE | X1-YZ19-J62 | SCARCE | 10,274 |

---

## Factories Waiting for Collection

Several factories have received all inputs but are not being collected from:

| Factory | Output | Supply Level |
|---------|--------|--------------|
| X1-YZ19-F50 | EXPLOSIVES | LIMITED |
| X1-YZ19-G52 | FERTILIZERS | LIMITED |
| X1-YZ19-G52 | PLASTICS | SCARCE |
| X1-YZ19-E49 | FABRICS | MODERATE |
| X1-YZ19-E49 | POLYNUCLEOTIDES | SCARCE |
| X1-YZ19-H55 | IRON | SCARCE |
| X1-YZ19-H55 | ALUMINUM | SCARCE |

**Issue:** 20+ factory states show `all_inputs_delivered = true` but `ready_for_collection = false`. This indicates stalled pipelines or logic errors in the collection workflow.

---

## Optimization Recommendations

### Priority 1: Fix Critical Bugs

1. **Task Assignment Race Condition**
   - Implement distributed locking in `TaskAssignmentManager`
   - Use database-level `SELECT FOR UPDATE` or Redis locks
   - Estimated impact: Eliminate 787 wasted assignment attempts

2. **Pipeline Stuck Detection**
   - Review and fix root cause of stuck pipelines
   - Currently recycling ~19 pipelines (35% cancellation rate)
   - Each recycled pipeline wastes all prior investment

3. **Task-Ship Tracking**
   - Fix "No task found for completed ship" issue
   - Ensure task completion callbacks properly update state

### Priority 2: Economic Optimization

1. **Stop Buying Raw Materials at Loss**
   - LIQUID_NITROGEN deliveries: -1,189,472 credits loss
   - LIQUID_HYDROGEN deliveries: -549,024 credits loss
   - Consider: Only acquire raw materials when supply is HIGH (cheap)
   - Alternative: Mine these materials instead of purchasing

2. **Focus on High-Margin Products**
   - SHIP_PLATING: +360,445 profit
   - JEWELRY: +357,306 profit
   - SHIP_PARTS: +199,005 profit
   - Prioritize these pipelines over low-margin goods

3. **Time Market Sales**
   - Sell when supply is SCARCE/LIMITED for 2-4x price
   - SHIP_PLATING ranges from 3,888 to 15,510 (4x spread)
   - Hold goods in cargo until market conditions improve

### Priority 3: Operational Improvements

1. **Reduce Fuel Costs**
   - 223,931 credits spent on fuel
   - Consider: More efficient route planning
   - Consider: Batch deliveries to reduce trips

2. **Fix Collection Workflow**
   - 20+ factories waiting for collection
   - Stalled factories block capital and pipeline completion

3. **Ship Assignment Optimization**
   - TORWINDO-17 earned +469,585
   - TORWINDO-1B lost -202,628
   - Analyze what makes certain ships more profitable
   - Consider task-ship matching based on distance/cargo capacity

### Priority 4: Monitoring & Alerting

1. Add metrics for:
   - Task assignment failure rate
   - Pipeline stuck rate
   - Per-good profitability (real-time)
   - Factory collection delays

2. Alert on:
   - Error rate > 100/hour
   - Pipeline cancellation rate > 20%
   - Negative hourly P&L

---

## Summary

The manufacturing operation is **profitable** (+586,765 credits/8 hours = ~73,346 credits/hour) but is being held back by:

1. **Technical Issues:**
   - Race condition in task assignment (787 errors)
   - Stuck pipeline recycling (19 occurrences, 35% cancellation rate)
   - Orphaned tasks and missing task tracking

2. **Economic Issues:**
   - Raw material deliveries (LIQUID_NITROGEN/HYDROGEN) losing -1.7M credits
   - Not timing market sales for optimal pricing
   - Underutilized high-margin products

**Estimated Potential Improvement:**
- Fix raw material losses: +1,700,000 credits
- Reduce cancellation rate to 10%: +200,000 credits
- Better market timing: +500,000 credits
- **Potential 8-hour profit: ~3,000,000 credits** (5x current)

---

## Appendix: Container Status

| Container Type | Running | Completed | Failed |
|----------------|---------|-----------|--------|
| MANUFACTURING_TASK_WORKER | 6 | 358 | 20 |
| MANUFACTURING_COORDINATOR | 1 | 0 | 0 |
| CONTRACT_WORKFLOW | 0 | 34 | 3 |

**Currently Active:** 24 containers processing tasks

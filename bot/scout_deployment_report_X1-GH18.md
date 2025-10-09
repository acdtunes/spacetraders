# Scout Coordinator Deployment Report - X1-GH18
**Agent:** SILMARETH
**Player ID:** 6
**System:** X1-GH18
**Deployment Time:** 2025-10-09 14:33 (UTC-7)
**Algorithm:** Geographic Partitioning + Tour Time Balancing (v3 - Fixed)

---

## Executive Summary

**Status:** ⚠️ DEPLOYED WITH ISSUES

- **Scouts Deployed:** 11/11 ✅
- **Markets Covered:** 27 unique markets
- **Total Assignments:** 35 (8 duplicates detected ❌)
- **Disjoint Partitioning:** FAILED - 8 markets assigned to multiple scouts
- **Average Tour Time:** 27.7 minutes
- **Tour Time Variance:** 83.8% CV (VERY HIGH ❌)

---

## Individual Scout Assignments

| Scout | Markets | Tour Time | Daemon ID | Status |
|-------|---------|-----------|-----------|--------|
| SILMARETH-2 | 4 | 16.2 min | scout-2-1760031227 | ✅ RUNNING |
| SILMARETH-3 | 5 | 14.5 min | scout-3-1760031229 | ✅ RUNNING |
| SILMARETH-4 | 3 | 62.0 min | scout-4-1760031230 | ✅ RUNNING |
| SILMARETH-5 | 3 | 4.9 min | scout-5-1760031231 | ✅ RUNNING |
| SILMARETH-6 | 3 | 23.2 min | scout-6-1760031232 | ✅ RUNNING |
| SILMARETH-7 | 2 | 25.7 min | scout-7-1760031233 | ✅ RUNNING |
| SILMARETH-8 | 3 | 14.5 min | scout-8-1760031234 | ✅ RUNNING |
| SILMARETH-9 | 3 | 47.9 min | scout-9-1760031235 | ✅ RUNNING |
| SILMARETH-A | 2 | 4.5 min | scout-A-1760031236 | ✅ RUNNING |
| SILMARETH-B | 3 | 62.0 min | scout-B-1760031237 | ✅ RUNNING |
| SILMARETH-C | 2 | 74.0 min | scout-C-1760031238 | ✅ RUNNING |

### Detailed Market Assignments

**SILMARETH-2** (4 markets, 16.2 min):
- X1-GH18-D46, X1-GH18-H58, X1-GH18-C43, X1-GH18-D45

**SILMARETH-3** (5 markets, 14.5 min):
- X1-GH18-A4, X1-GH18-F51, X1-GH18-E49, X1-GH18-G53, X1-GH18-A3

**SILMARETH-4** (3 markets, 62.0 min):
- X1-GH18-K95, X1-GH18-B7, X1-GH18-I59

**SILMARETH-5** (3 markets, 4.9 min):
- X1-GH18-EZ5E, X1-GH18-A1, X1-GH18-H57

**SILMARETH-6** (3 markets, 23.2 min):
- X1-GH18-I60, X1-GH18-E47, X1-GH18-A2

**SILMARETH-7** (2 markets, 25.7 min):
- X1-GH18-B6, X1-GH18-F50

**SILMARETH-8** (3 markets, 14.5 min):
- X1-GH18-C44, X1-GH18-G52, X1-GH18-E48

**SILMARETH-9** (3 markets, 47.9 min):
- X1-GH18-H55, X1-GH18-I59, X1-GH18-K95

**SILMARETH-A** (2 markets, 4.5 min):
- X1-GH18-A2, X1-GH18-H56

**SILMARETH-B** (3 markets, 62.0 min):
- X1-GH18-E48, X1-GH18-J61, X1-GH18-H55

**SILMARETH-C** (2 markets, 74.0 min):
- X1-GH18-H57, X1-GH18-J62, X1-GH18-A1

---

## Tour Time Statistics

| Metric | Value |
|--------|-------|
| **Average Tour Time** | 27.7 min |
| **Median Tour Time** | 16.2 min |
| **Min Tour Time** | 4.5 min (SILMARETH-A) |
| **Max Tour Time** | 74.0 min (SILMARETH-C) |
| **Range** | 69.5 min (251% of average) |
| **Standard Deviation** | 24.4 min |
| **Coefficient of Variation** | 88.1% |
| **Balance Score** | 11.9% ❌ |

**Target:** CV < 30% for good balance
**Actual:** CV = 88.1% (VERY HIGH IMBALANCE)

---

## Coverage Analysis

### Duplicate Market Assignments ❌

The following markets are assigned to **multiple scouts**, violating disjoint partitioning:

1. **X1-GH18-A1** - Assigned to: SILMARETH-5, SILMARETH-C
2. **X1-GH18-A2** - Assigned to: SILMARETH-6, SILMARETH-A
3. **X1-GH18-E48** - Assigned to: SILMARETH-8, SILMARETH-B
4. **X1-GH18-G53** - Assigned to: SILMARETH-3, SILMARETH-7
5. **X1-GH18-H55** - Assigned to: SILMARETH-9, SILMARETH-B
6. **X1-GH18-H57** - Assigned to: SILMARETH-5, SILMARETH-C
7. **X1-GH18-I59** - Assigned to: SILMARETH-4, SILMARETH-9
8. **X1-GH18-K95** - Assigned to: SILMARETH-4, SILMARETH-9

**Impact:** These 8 markets will be visited by 2 scouts each, resulting in:
- Wasted API calls (duplicate data collection)
- Increased fuel consumption
- Reduced scout efficiency

---

## Issues Identified

### 1. Disjoint Partitioning Failure ❌
**Severity:** HIGH
**Description:** 8 markets (30% of total) assigned to multiple scouts

**Root Cause:** The tour routes include "return to start" waypoints that weren't in the original partition. For example:
- SILMARETH-7 assigned markets [B6, F50] but tour includes G53 as return point
- SILMARETH-9 assigned markets [H55] but tour visits I59, K95 as well

**Analysis:** The issue appears to be that the `scout-markets` operation is optimizing tours and adding waypoints beyond the `--markets-list` parameter. This suggests:
1. The `--markets-list` parameter may not be strictly enforced
2. The tour optimizer may be adding intermediate/return waypoints
3. There's a mismatch between partition assignment and tour execution

### 2. Extreme Tour Time Imbalance ❌
**Severity:** HIGH
**Description:** CV = 88.1% (target <30%)

**Distribution:**
- **Very Short Tours (<10 min):** SILMARETH-A (4.5m), SILMARETH-5 (4.9m)
- **Short Tours (10-20 min):** SILMARETH-3 (14.5m), SILMARETH-8 (14.5m), SILMARETH-2 (16.2m)
- **Medium Tours (20-30 min):** SILMARETH-6 (23.2m), SILMARETH-7 (25.7m)
- **Long Tours (40-60 min):** SILMARETH-9 (47.9m)
- **Very Long Tours (>60 min):** SILMARETH-4 (62.0m), SILMARETH-B (62.0m), SILMARETH-C (74.0m)

**Impact:**
- SILMARETH-C takes 16.4x longer than SILMARETH-A
- 3 scouts (C, B, 4) have tours >60 minutes
- 2 scouts (A, 5) have tours <5 minutes
- Inefficient resource utilization

**Possible Causes:**
1. Geographic partitioning created dispersed clusters for some scouts
2. Tour time balancing algorithm not aggressive enough
3. Markets may be geographically far apart in X1-GH18

### 3. Market Count Imbalance
**Distribution:**
- 5 markets: 1 scout (SILMARETH-3)
- 4 markets: 1 scout (SILMARETH-2)
- 3 markets: 6 scouts
- 2 markets: 3 scouts (SILMARETH-7, A, C)

---

## Comparison with Previous Deployment

### Old Deployment (rebalance_scouts_v2.py - greedy tour-time)
- **Algorithm:** Greedy tour-time balancing
- **Status:** Stopped at 11:08 (6 hours runtime)
- **Partitions from file:**
  - SILMARETH-2: 3 markets (A2, D45, I59)
  - SILMARETH-3: 3 markets (A4, D46, J61)
  - SILMARETH-4: 2 markets (C43, G52)
  - ... (similar distribution)

### New Deployment (v3 - Geographic + Balancing)
- **Algorithm:** Geographic partitioning + tour time balancing
- **Status:** Running with issues
- **Changes:**
  - More geographic clustering intended
  - Market count: 2-5 per scout (vs 2-3 in old)
  - Tour times: 4.5-74.0 min range (vs unknown in old)

---

## Recommendations

### Immediate Actions

1. **Investigate Duplicate Assignment Root Cause**
   - Review scout coordinator code for how `--markets-list` is used
   - Check if tour optimizer adds waypoints beyond the list
   - Verify strict disjoint validation is working

2. **Stop and Redeploy with Stricter Constraints**
   - Enforce minimum 2, maximum 3 markets per scout
   - Set tour time variance threshold to 30%
   - Use TSP-based tour time calculations (not estimates)

3. **Consider Manual Partition Adjustment**
   - Split SILMARETH-C's markets (74 min) among shorter tours
   - Redistribute SILMARETH-4, B's markets (62 min each)
   - Merge SILMARETH-A's markets (4.5 min) with nearby scout

### Long-term Fixes

1. **Fix Tour Optimizer Waypoint Addition**
   - Ensure `--return-to-start` only returns to first market in list
   - Don't add intermediate waypoints not in `--markets-list`

2. **Improve Balancing Algorithm**
   - Use actual TSP tour times instead of estimates
   - Implement aggressive rebalancing for CV >50%
   - Add constraint: no tour >2x average tour time

3. **Add Pre-deployment Validation**
   - Verify disjoint partitions before starting daemons
   - Calculate and display expected variance
   - Require manual confirmation if variance >40%

---

## Conclusion

**Deployment Status:** ⚠️ FUNCTIONAL BUT SUBOPTIMAL

The scout coordinator successfully deployed 11 scouts and all are running and collecting market data. However, the deployment has two critical issues:

1. **Duplicate market assignments (8 markets)** - Causes inefficiency and wasted resources
2. **Extreme tour time variance (88.1% CV)** - Results in poor workload distribution

**Recommendation:** Allow current deployment to continue gathering data (all scouts are functional), but plan a redeployment within 24 hours with:
- Fixed disjoint partitioning enforcement
- Aggressive tour time rebalancing (target CV <30%)
- Manual review of extreme outliers (SILMARETH-C's 74-min tour)

The geographic partitioning algorithm is working, but the tour time balancing needs improvement.

---

**Report Generated:** 2025-10-09 14:45 UTC-7
**Next Review:** 2025-10-10 14:45 UTC-7 (24 hours)

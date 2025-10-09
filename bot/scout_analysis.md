# Scout Market Assignment Analysis - X1-GH18

## Current Assignments (Extracted from Logs)

| Scout | Markets | Cycle Time | Tour #s |
|-------|---------|------------|---------|
| SILMARETH-2 | D45, C43, D46, H58, C44 | 27m 19s | 5 markets |
| SILMARETH-3 | A4, E49, F51, G53, F50 | ~33m (estimated) | 6 markets |
| SILMARETH-4 | B7, I59 | 54m 39s | 2 markets |
| SILMARETH-5 | A1, EZ5E, H57 | 4m 52s | 3 markets |
| SILMARETH-6 | I60, E47, A2 | 23m 13s | 3 markets |
| SILMARETH-7 | B6, F50, G53 | 25m 44s | 3 markets |
| SILMARETH-8 | C44, G52, E48 | 14m 30s | 3 markets |
| SILMARETH-9 | I59, K95, A2 | 44m 49s | 3 markets |
| SILMARETH-A | A2, H56 | 4m 27s | 2 markets |
| SILMARETH-B | E48, H55, A2 | 9m 54s | 3 markets |
| SILMARETH-C | J62, J61, E48 | 1h 4m | 3 markets |

## CRITICAL ISSUES IDENTIFIED

### Issue 1: MARKET OVERLAPS (SEVERE)

**Overlapping Markets:**
- **A2**: Assigned to 4 scouts! (SILMARETH-6, SILMARETH-9, SILMARETH-A, SILMARETH-B)
- **E48**: Assigned to 3 scouts! (SILMARETH-8, SILMARETH-B, SILMARETH-C)
- **F50**: Assigned to 2 scouts (SILMARETH-3, SILMARETH-7)
- **G53**: Assigned to 2 scouts (SILMARETH-3, SILMARETH-7)
- **I59**: Assigned to 2 scouts (SILMARETH-4, SILMARETH-9)
- **C44**: Assigned to 2 scouts (SILMARETH-2, SILMARETH-8)

**Total Overlaps:** 6 markets with duplicates
**Wasted Scout Capacity:** ~30% of all visits are duplicates

### Issue 2: CYCLE TIME IMBALANCE (SEVERE)

**Cycle Time Distribution:**
- Minimum: 4m 27s (SILMARETH-A)
- Maximum: 1h 4m (SILMARETH-C)
- Range: **14.4x variance**

**Imbalanced Groups:**
- **Fast scouts (<10 min):** SILMARETH-5, SILMARETH-A, SILMARETH-B (3 scouts)
- **Medium scouts (10-30 min):** SILMARETH-2, SILMARETH-6, SILMARETH-7, SILMARETH-8 (4 scouts)
- **Slow scouts (>40 min):** SILMARETH-4, SILMARETH-9, SILMARETH-C (3 scouts)
- **Very fast scouts (3-5 scouts):** SILMARETH-3 (~33min) (1 scout)

**Problem:** Some scouts complete 13 tours while others complete 1 tour in the same time period.

## All Unique Markets Covered

Based on logs, the following 27 unique markets are being scouted:
1. A1
2. A2
3. A4
4. B6
5. B7
6. C43
7. C44
8. D45
9. D46
10. E47
11. E48
12. E49
13. EZ5E
14. F50
15. F51
16. G52
17. G53
18. H55
19. H56
20. H57
21. H58
22. I59
23. I60
24. J61
25. J62
26. K95
27. (Markets assigned to scout-3 tour: A4, E49, F51, G53, F50 - 5 markets)

## Root Cause Analysis

### Why Overlaps Exist

The partitioning algorithm is not enforcing strict disjoint sets. Multiple scouts were assigned:
1. **A2** - Central hub, likely assigned as "base" for multiple scouts
2. **E48** - Another hub location
3. **F50/G53** - Assigned to adjacent partitions (scouts 3 & 7)
4. **I59** - Long-distance market assigned to 2 scouts (4 & 9)

### Why Cycle Times Are Imbalanced

1. **SILMARETH-4**: Assigned only 2 markets (B7↔I59) but they're 567 units apart = 27min each way
2. **SILMARETH-C**: Assigned J62/J61 which are 668 units from E48 = 32min one-way
3. **SILMARETH-5/A**: Assigned nearby markets (<50 units) = very fast cycles
4. **Uneven market distribution**: Some scouts have 2 markets, others have 6

**Geographic clustering not considered:** Distant markets should be grouped together, nearby markets should be grouped together.

## Rebalancing Strategy

### Objectives
1. **Zero overlaps** - Strict disjoint partitioning
2. **Balanced cycle times** - Target: 15-25 minute range for all scouts
3. **Maintain coverage** - All 27 markets covered

### Approach
1. **Group markets by geographic clusters** (minimize total distance per scout)
2. **Balance total tour time** across all scouts
3. **Consider market "hubs" vs "remote"** locations
4. **Assign 2-3 markets per scout** (optimal balance)

### Proposed Redistribution

Will calculate optimal assignments based on:
- Distance matrix between all markets
- K-means clustering to create 11 balanced groups
- TSP optimization within each group
- Validation: no market appears in >1 group

## Next Steps

1. Stop all scouts immediately
2. Calculate distance matrix for all 27 markets
3. Run clustering algorithm to create 11 balanced groups
4. Generate new config with validated assignments
5. Redeploy with strict validation checks

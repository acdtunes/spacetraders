# Discovery Answers

## Q1: Should OR-Tools completely replace the existing routing system (routing.py)?
**Answer:** Yes

**User rationale:** Current code is buggy. The existing tests should be used to validate the change (maybe some tests are wrong, but we will see).

**Implications:**
- Complete replacement of routing.py logic with OR-Tools
- Existing test suite (pytest-bdd) will validate the new implementation
- Some tests may need fixing if they encode incorrect behavior
- Higher risk but cleaner architecture (no dual-system complexity)

---

## Q2: Should the validation layer run automatically on every route calculation?
**Answer:** No

**Implications:**
- Validation runs periodically (scheduled task) or manually
- Avoids performance overhead on every route calculation
- Background validation process to detect constant drift
- Manual validation command available for debugging

---

## Q3: Should the configuration constants (flight mode multipliers, fuel rates) be stored in a database?
**Answer:** No

**Implications:**
- Constants stored in config file (e.g., config/routing_constants.yaml)
- Version control friendly (track constant changes)
- Easy code review for updates
- Simple deployment (no database migration)
- Rollback capability if constants are incorrect

---

## Q4: Should OR-Tools handle both single-destination routes AND multi-stop tour optimization (TSP)?
**Answer:** Yes

**Implications:**
- OR-Tools replaces both route planning AND TSP tour optimization
- Single-destination: Shortest path with fuel constraints
- Multi-stop tours: Vehicle Routing Problem (VRP) solver
- Replaces custom TSP logic in scout_coordinator.py and tour_cache
- Unified optimization approach across all routing scenarios

---

## Q5: Should validation failures (>5% deviation from actual API behavior) pause routing operations until constants are fixed?
**Answer:** Yes

**Implications:**
- Validation failures trigger operational pause (safety-first approach)
- Routing operations halt until constants are corrected
- Alert/notification system needed for validation failures
- Manual override capability for emergency situations
- Prevents ships from getting stranded due to incorrect fuel calculations

---

## Q6: Should the OR-Tools router maintain backward compatibility with the existing RouteOptimizer return format?
**Answer:** Yes

**Implications:**
- OR-Tools must return same dict structure as current RouteOptimizer
- SmartNavigator.execute_route() continues working without changes
- Format: {'steps': [...], 'total_time': int, 'total_fuel_cost': int, 'final_fuel': int}
- Each step has 'action', 'from', 'to', 'mode', 'fuel_cost', 'distance', 'time'
- Refuel steps have 'action': 'refuel', 'waypoint', 'fuel_added'

---

## Q7: Should flight mode selection (CRUISE vs DRIFT) be handled by OR-Tools or remain as a post-processing step?
**Answer:** Yes

**Implications:**
- Each edge modeled with TWO variants: CRUISE and DRIFT
- CRUISE edge: fast (low time cost), expensive fuel (high fuel consumption)
- DRIFT edge: slow (high time cost), cheap fuel (low fuel consumption)
- OR-Tools natively chooses optimal mode per edge during solve
- More accurate multi-objective optimization
- Graph size doubles (each physical edge → 2 mode edges)

---

## Q8: Should we preserve the tour_cache table functionality when replacing TourOptimizer with OR-Tools TSP?
**Answer:** Yes

**Implications:**
- OR-Tools TSP results cached in existing tour_cache table
- Same cache lookup logic before running optimization
- Cache key: (system, waypoints, algorithm='ortools')
- Significant performance improvement for scout coordinator
- Avoid recalculating identical tours repeatedly

---

## Q9: Should the validation system compare both time AND fuel predictions against actual API behavior?
**Answer:** Yes

**Implications:**
- Validate both time and fuel consumption predictions
- Time validation: compare predicted travel time vs actual API arrival time
- Fuel validation: compare predicted fuel cost vs actual fuel consumed
- Dual metrics provide comprehensive accuracy verification
- Fuel validation is safety-critical (prevents ship stranding)
- Time validation ensures operation scheduling accuracy
- Pause operations if EITHER metric exceeds 5% deviation

---

## Q10: Should probe ships (fuel_capacity=0, solar powered) bypass OR-Tools entirely and use simple direct routing?
**Answer:** Yes

**Implications:**
- Probes skip OR-Tools fuel constraint optimization (no fuel to constrain)
- Use simple direct routing: calculate shortest path based on distance
- BUT still use ship's actual engine speed from ship_data['engine']['speed']
- Time calculation: (distance × mode_multiplier) / ship_speed
- Faster execution (no optimization overhead for trivial case)
- Probe detection: check if ship['fuel']['capacity'] == 0

---

## Q11 (ADDED): Should OR-Tools also handle tour partitioning (assigning markets to multiple ships)?
**Answer:** Yes (user requested)

**Current System:**
- market_partitioning.py has greedy, k-means, and geographic strategies
- Assigns markets to ships trying to balance tour times
- Uses heuristics that may not be optimal

**OR-Tools Capability:**
- Multi-Vehicle VRP (Vehicle Routing Problem)
- Can assign waypoints to multiple vehicles optimally
- Balances workload (minimize max tour time across fleet)
- Better than heuristic partitioning

**Implications:**
- Replace market_partitioning.py strategies with OR-Tools Multi-VRP
- Optimal fleet-wide tour assignment
- Balanced scout ship workloads
- Unified OR-Tools approach for all routing problems

---

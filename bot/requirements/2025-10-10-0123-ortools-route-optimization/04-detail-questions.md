# Detail Questions

## Q6: Should the OR-Tools router maintain backward compatibility with the existing RouteOptimizer return format?
**Default if unknown:** Yes (existing code depends on route dict structure with steps, fuel_cost, mode, etc.)

**Context:** Current `RouteOptimizer.find_optimal_route()` returns:
```python
{
    'steps': [
        {'action': 'navigate', 'from': 'A', 'to': 'B', 'mode': 'CRUISE', 'fuel_cost': 100, 'distance': 100, 'time': 86},
        {'action': 'refuel', 'waypoint': 'B', 'fuel_added': 300}
    ],
    'total_time': 91,
    'total_fuel_cost': 100,
    'final_fuel': 200
}
```

SmartNavigator.execute_route() depends on this exact structure. Should OR-Tools maintain this format or can we change the interface?

---

## Q7: Should flight mode selection (CRUISE vs DRIFT) be handled by OR-Tools or remain as a post-processing step?
**Default if unknown:** Yes (OR-Tools should choose optimal mode per edge as part of optimization)

**Context:** Currently, RouteOptimizer makes mode decisions during A* search. OR-Tools could either:
- Option A: Model CRUISE and DRIFT as separate edge types in the graph (each edge has 2 variants)
- Option B: Use CRUISE by default, post-process to DRIFT only if needed

Option A is more optimal (true multi-objective optimization). Should OR-Tools handle this natively?

---

## Q8: Should we preserve the tour_cache table functionality when replacing TourOptimizer with OR-Tools TSP?
**Default if unknown:** Yes (tour caching significantly improves scout coordinator performance)

**Context:** The database has a `tour_cache` table that stores pre-optimized TSP tours for market routes. Scout coordinator uses this to avoid recalculating tours. Example:
```sql
SELECT optimized_tour FROM tour_cache WHERE system='X1-JB26' AND algorithm='2opt'
```

Should OR-Tools TSP results be cached the same way?

---

## Q9: Should the validation system compare both time AND fuel predictions against actual API behavior?
**Default if unknown:** Yes (validate both metrics to ensure accuracy)

**Context:** Validation could check:
- Time deviation: predicted travel time vs actual API response
- Fuel deviation: predicted fuel consumption vs actual consumption
- Both are critical for safe routing

Should we validate both or only one metric?

---

## Q10: Should probe ships (fuel_capacity=0, solar powered) bypass OR-Tools entirely and use simple direct routing?
**Default if unknown:** Yes (OR-Tools fuel constraints don't apply to probes, simpler logic is faster)

**Context:** Probe ships have 0 fuel capacity and don't consume fuel (solar powered). They can navigate anywhere instantly. OR-Tools fuel dimension doesn't apply. Should we:
- Skip OR-Tools for probes, use direct Euclidean path
- Or still use OR-Tools but with fuel constraints disabled

Simple direct routing would be faster for probes.

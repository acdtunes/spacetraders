package contract

import "sort"

// StandbyWaypoint is one candidate standby station a dedicated contract hull can
// home to, carrying the DEMAND weight that ranks it — higher weight = a
// higher-demand central contract sink that should be covered first. Demand is the
// primary spread key after occupancy; a hull's distance breaks equal-demand ties
// (so an unranked/uniform set degrades to plain nearest-station homing).
type StandbyWaypoint struct {
	Symbol       string
	DemandWeight float64
}

// IdleHullToPlace is one idle dedicated hull awaiting a standby assignment, with
// its distance to each candidate waypoint (keyed by symbol) for the equal-demand
// tie-break. Only hulls handed in are ever placed — ownership filtering (the
// dedicated fleet, never a pinned/frigate/other-op hull) is the caller's job.
type IdleHullToPlace struct {
	ShipSymbol string
	Distance   map[string]float64
}

// DistributeIdleHullsAcrossStandby assigns each idle hull a standby waypoint so the
// fleet SPREADS demand-ranked across the set instead of piling on one point (the
// live bug that caps the contract op at ~1.28x). It is a PURE mechanism: the caller
// supplies the demand signal (frequency×payment / observed hub demand) and the
// occupancy; the distribution is a deterministic function of those.
//
// Each hull (processed in symbol order) takes the station minimizing, in order:
// current occupancy (fewest first → spread), then demand (highest first →
// demand-ranked), then that hull's distance (nearest → the uniform-demand fallback
// preserves plain nearest-station homing), then symbol (a total order for
// determinism). Assigning a hull bumps that station's working occupancy, so the
// NEXT co-located hull fans out to the next-best station rather than piling on.
//
// A single waypoint (M==1) sends every hull there (degenerate, no panic); an empty
// waypoint set or no hulls yields an empty map. existingOccupancy (nil-safe) seeds
// the count of peers already parked at each station so they are not double-stacked.
func DistributeIdleHullsAcrossStandby(hulls []IdleHullToPlace, waypoints []StandbyWaypoint, existingOccupancy map[string]int) map[string]string {
	if len(hulls) == 0 || len(waypoints) == 0 {
		return map[string]string{}
	}

	// Working occupancy copy so the caller's map is never mutated.
	occ := make(map[string]int, len(waypoints))
	for _, wp := range waypoints {
		occ[wp.Symbol] = existingOccupancy[wp.Symbol]
	}

	// Deterministic placement order: symbol-sorted hulls.
	ordered := make([]IdleHullToPlace, len(hulls))
	copy(ordered, hulls)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ShipSymbol < ordered[j].ShipSymbol })

	result := make(map[string]string, len(ordered))
	for _, hull := range ordered {
		best := waypoints[0]
		for _, cand := range waypoints[1:] {
			if lessStandby(cand, best, occ, hull.Distance) {
				best = cand
			}
		}
		result[hull.ShipSymbol] = best.Symbol
		occ[best.Symbol]++
	}
	return result
}

// lessStandby reports whether candidate a is a strictly better home than b for a
// hull with the given per-station distances, under the fixed priority: fewer peers
// (spread) → higher demand (demand-ranked) → nearer (uniform-demand fallback) →
// smaller symbol (deterministic total order).
func lessStandby(a, b StandbyWaypoint, occ map[string]int, distance map[string]float64) bool {
	if occ[a.Symbol] != occ[b.Symbol] {
		return occ[a.Symbol] < occ[b.Symbol]
	}
	if a.DemandWeight != b.DemandWeight {
		return a.DemandWeight > b.DemandWeight
	}
	if da, db := distance[a.Symbol], distance[b.Symbol]; da != db {
		return da < db
	}
	return a.Symbol < b.Symbol
}

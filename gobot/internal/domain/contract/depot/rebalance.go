package depot

import "sort"

// HubArrivals is the observed contract-arrival signal per hub waypoint: how many contracts
// recently routed to (arrived at) each hub. It is the DEMAND signal the rebalancer migrates idle
// delivery hulls toward. The caller supplies it (the observation source is the deferred wiring
// half); the mechanism is a pure function of this signal, the idle placements, and the policy.
type HubArrivals map[string]int

// RebalancePolicy holds the config knobs the economy-analyst tunes. The ZERO value is OFF —
// MaxMigrations 0 proposes NO migration — so rebalancing is opt-in and an unconfigured / degraded
// deployment behaves exactly as before (regression-safe).
type RebalancePolicy struct {
	// MaxMigrations caps how many idle delivery hulls a single rebalance pass moves (e.g. 3).
	// 0 (or negative) disables rebalancing entirely.
	MaxMigrations int
	// MinArrivalGap is the minimum (target-hub arrivals - source-hub arrivals) required to justify
	// a migration — the anti-churn threshold. 0 means any strictly-positive gap (>= 1) qualifies.
	MinArrivalGap int
}

// Migration is one proposed idle delivery-hull move: reposition ShipSymbol from FromHub to ToHub.
type Migration struct {
	ShipSymbol string
	FromHub    string
	ToHub      string
}

// PlanRebalance is the demand-driven rebalance MECHANISM (bead sp-9j9c #3). Because contracts are
// serialized (one active at a time), "rebalance" means keeping IDLE delivery hulls optimally
// PRE-POSITIONED across hubs for the NEXT contract: idle hulls migrate toward hubs with rising
// observed contract arrival and away from cold ones, so fewer hulls cover more hot clusters with
// less inter-contract repositioning latency. It is a PURE decision — (idle placements x observed
// per-hub arrivals x policy knobs) -> migrations — baking in neither the observation source nor
// the migration executor (both the deferred wiring half).
//
// A single greedy pass: each uncovered hot hub (a hub with observed arrivals but no idle hull
// present), hottest first, pulls the COLDEST movable idle hull toward it — one whose current hub
// is cooler than the target by at least the anti-churn threshold — so a migration only ever moves
// a hull toward strictly-hotter demand and never strips a hub as hot as the target. The pass stops
// at MaxMigrations. It is deterministic (hottest-hub / coldest-hull / symbol tie-breaks) and
// regression-safe: a zero/negative cap, no idle hulls, or no arrivals all yield no migration.
func PlanRebalance(idleDeliveryHulls []Element, arrivals HubArrivals, policy RebalancePolicy) []Migration {
	if policy.MaxMigrations <= 0 || len(idleDeliveryHulls) == 0 || len(arrivals) == 0 {
		return nil
	}
	targets := uncoveredHotHubs(idleDeliveryHulls, arrivals)
	sources := coldestFirst(idleDeliveryHulls, arrivals)
	threshold := policy.MinArrivalGap
	if threshold < 1 {
		threshold = 1
	}

	var migrations []Migration
	moved := make(map[int]bool, len(sources))
	for _, target := range targets {
		if len(migrations) >= policy.MaxMigrations {
			break
		}
		source, index, ok := coldestMovableSource(sources, moved, arrivals, target, threshold)
		if !ok {
			continue
		}
		moved[index] = true
		migrations = append(migrations, Migration{ShipSymbol: source.ShipSymbol, FromHub: source.Waypoint, ToHub: target})
	}
	return migrations
}

// uncoveredHotHubs returns the hubs that have observed arrivals but NO idle hull present — unmet
// demand — sorted hottest-first (arrivals desc, then waypoint asc for a deterministic tie-break).
func uncoveredHotHubs(idle []Element, arrivals HubArrivals) []string {
	occupied := make(map[string]bool, len(idle))
	for _, hull := range idle {
		occupied[hull.Waypoint] = true
	}
	var hubs []string
	for hub, count := range arrivals {
		if count > 0 && !occupied[hub] {
			hubs = append(hubs, hub)
		}
	}
	sort.Slice(hubs, func(i, j int) bool {
		if arrivals[hubs[i]] != arrivals[hubs[j]] {
			return arrivals[hubs[i]] > arrivals[hubs[j]]
		}
		return hubs[i] < hubs[j]
	})
	return hubs
}

// coldestFirst returns the idle hulls ordered by their current hub's arrivals ascending (the
// coldest, most-movable hulls first), breaking ties by ship symbol so the pick is deterministic.
func coldestFirst(idle []Element, arrivals HubArrivals) []Element {
	sorted := make([]Element, len(idle))
	copy(sorted, idle)
	sort.Slice(sorted, func(i, j int) bool {
		arrivalsI, arrivalsJ := arrivals[sorted[i].Waypoint], arrivals[sorted[j].Waypoint]
		if arrivalsI != arrivalsJ {
			return arrivalsI < arrivalsJ
		}
		return sorted[i].ShipSymbol < sorted[j].ShipSymbol
	})
	return sorted
}

// coldestMovableSource returns the coldest not-yet-moved idle hull that may migrate to target: it
// must sit at a DIFFERENT hub whose arrivals are cooler than the target's by at least threshold.
func coldestMovableSource(sources []Element, moved map[int]bool, arrivals HubArrivals, target string, threshold int) (Element, int, bool) {
	for index, source := range sources {
		if moved[index] || source.Waypoint == target {
			continue
		}
		if arrivals[target]-arrivals[source.Waypoint] >= threshold {
			return source, index, true
		}
	}
	return Element{}, 0, false
}

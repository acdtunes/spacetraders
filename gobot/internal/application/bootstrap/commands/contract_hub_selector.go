package commands

import "sort"

// Hub is a selected contract hub: a waypoint a light hauler is pre-positioned on because contract
// goods are densely + cheaply sourceable there, so the contract fleet's cycle-time drops. The selector
// returns hubs in placement-priority order (best first); the caller places one hauler per hub, up to
// hauler_target, skipping hubs already served.
type Hub struct {
	Waypoint      string  // the placement target (where a hauler is staged)
	System        string  // its system (intra-system clustering context)
	Coverage      int     // # target (contract) goods this hub can source — the dominant rank signal
	AvgSourceCost float64 // mean purchase price over the goods it sources (lower = cheaper) — tiebreak
	Density       int     // # goods the market can source (centrality / clustering proxy) — tiebreak
}

// minHubSourceableGoods is the viability floor: a waypoint qualifies as a contract hub only if it can
// source at least this many target goods. 1 = "sources something contract-relevant" — a hauler parked
// where nothing is sourceable is pointless. A documented constant (a later refinement to a config
// knob), mirroring the Slice-1 bootstrapMarketFreshnessMin precedent.
const minHubSourceableGoods = 1

// selectContractHubs ranks scouted markets into contract hubs (Slice 2 — this slice's sub-design).
//
// A contract hub is a central, market-dense waypoint where the goods contracts commonly demand are
// cheaply sourceable, so a hauler staged there minimizes contract cycle-time (little repositioning to
// source-then-deliver). It COMPLEMENTS workflow batch-contract rather than fighting it: the contract
// coordinator still accepts/assigns/fulfils contracts and discovers its ship pool dynamically; this
// selector only decides WHERE the pre-positioned haulers wait so that pool starts each contract from a
// good sourcing position.
//
// The heuristic:
//
//  1. TARGET GOODS. When contractGoods is non-empty, score hubs by how well they source THOSE goods.
//     When empty (no contract accepted/offered yet — hub selection runs at INCOME start, before
//     batch-contract has taken a contract), fall back to ALL sourceable goods: a dense, cheap market
//     is a sound generic contract hub, so the ramp is never blocked waiting for a contract.
//  2. VIABILITY (hard gate). Keep only markets that source ≥ minHubSourceableGoods target goods. A
//     hauler on a market that sources nothing relevant is pointless.
//  3. RANK (placement priority) — lexicographic, most-defensible signal first:
//     a. Coverage DESC — how many target goods it sources. Directly minimises cycle-time: a hub
//     covering many contract goods rarely forces a hauler to reposition to source.
//     b. AvgSourceCost ASC — cheaper sourcing = higher contract margin + faster viable fulfilment.
//     c. Density DESC — a denser market is a better-connected, more central staging point (the
//     clustering signal); more options as contract demand shifts.
//     d. Waypoint ASC — a stable, deterministic final tiebreak, so hub selection is idempotent across
//     ticks (the same data yields the same order, so re-observation never churns placements).
//
// It is a PURE function (no I/O, deterministic) so it is fixture-testable and the reconciler can call
// it every tick. It does NOT apply hauler_target — the caller caps at min(len(hubs), hauler_target)
// and skips hubs a hauler already serves.
func selectContractHubs(markets []MarketSnapshot, contractGoods []string) []Hub {
	targetSet := make(map[string]struct{}, len(contractGoods))
	for _, g := range contractGoods {
		if g != "" {
			targetSet[g] = struct{}{}
		}
	}
	useContractGoods := len(targetSet) > 0

	hubs := make([]Hub, 0, len(markets))
	for _, m := range markets {
		coverage := 0
		density := 0
		var sourceCostSum int64
		for _, g := range m.Goods {
			// A good with no positive purchase price is not actually sourceable — skip it (fail-closed:
			// a 0/unknown price never inflates coverage or cheapens the average).
			if g.PurchasePrice <= 0 {
				continue
			}
			density++
			counts := true
			if useContractGoods {
				_, counts = targetSet[g.Symbol]
			}
			if counts {
				coverage++
				sourceCostSum += g.PurchasePrice
			}
		}
		if coverage < minHubSourceableGoods {
			continue // not a viable hub
		}
		hubs = append(hubs, Hub{
			Waypoint:      m.Waypoint,
			System:        m.System,
			Coverage:      coverage,
			AvgSourceCost: float64(sourceCostSum) / float64(coverage),
			Density:       density,
		})
	}

	sort.SliceStable(hubs, func(i, j int) bool {
		a, b := hubs[i], hubs[j]
		if a.Coverage != b.Coverage {
			return a.Coverage > b.Coverage // more contract goods sourced first
		}
		if a.AvgSourceCost != b.AvgSourceCost {
			return a.AvgSourceCost < b.AvgSourceCost // cheaper sourcing first
		}
		if a.Density != b.Density {
			return a.Density > b.Density // denser (more central) market first
		}
		return a.Waypoint < b.Waypoint // stable deterministic tiebreak
	})
	return hubs
}

// hubWaypoints projects the selected hubs to their waypoints, in rank order — the caller's placement
// queue.
func hubWaypoints(hubs []Hub) []string {
	out := make([]string, len(hubs))
	for i, h := range hubs {
		out[i] = h.Waypoint
	}
	return out
}

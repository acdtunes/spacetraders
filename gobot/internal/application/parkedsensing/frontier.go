package parkedsensing

import (
	"context"
	"fmt"
	"sort"
)

// knownSystems is the set of systems the ledger already holds a row for.
func knownSystems(systems []ExpandSystem) map[string]bool {
	known := make(map[string]bool, len(systems))
	for _, s := range systems {
		known[s.System] = true
	}
	return known
}

// readNeighbours resolves the gate neighbours of every system expansion may
// propagate from: EVERY system in the ledger, whatever its verdict, plus any
// holding a parked spare (whose neighbours decide which frontier that spare can
// reach).
//
// PROPAGATION IS GATED ON MEASURED ADJACENCY, NOT ON JUDGEMENT, and the gate
// store is what enforces it. That store is populated from a system's jump-gate
// waypoint, so it answers only for gates we have actually charted; a system it
// does not know returns no neighbours and propagates nothing. Nothing here has
// to test for that, because "we have charted the gate" and "the store has rows"
// are the same fact.
//
// This replaces a judged-only rule, whose stated objection was that expanding
// through an unscreened neighbour would flood the ledger — and then the
// screening sweep's API budget — with a galaxy we have no reason to believe is
// worth anything. Both halves have been re-decided:
//
//   - The flood is the goal. Judging needs screening, screening needs charting,
//     and charting is flight-bound, so judged-only advanced the frontier one
//     FULLY-CHARTED RING at a time — chart ~50 waypoints, judge, discover,
//     repeat. Gating on the gate alone advances it at the speed of charting ONE
//     waypoint, which is the difference between a ledger that grows and one that
//     sits at sixteen systems while twelve charting seeds fly.
//   - The API budget is not spent here, and does not grow with the frontier.
//     Marking a neighbour PENDING is a ledger write. The screening sweep that
//     consumes those rows is bounded to screenSweepBatch systems per tick no
//     matter how many are waiting, so a larger frontier lengthens that QUEUE
//     rather than widening its per-tick spend. Both stores this tick reads to
//     expand — the gate adjacency here and the yard catalog in stagingYardFor —
//     are local database reads that cost no request token at all.
//
// Widening the origins widens stagingYardFor's search with it, deliberately: a
// probe yard in a charted-but-unjudged system is a measured fact about where a
// seed can be bought, and the purchase it stages is still funded by the buy
// queue under the same floor and probe cap as every other. More places to stage
// from is the direct cure for a frontier target that no judged system borders.
func readNeighbours(ctx context.Context, p ExpandPorts, systems []ExpandSystem, book *slotBook) (map[string][]string, error) {
	origins := make(map[string]bool, len(systems))
	for _, s := range systems {
		origins[s.System] = true
	}
	for _, spare := range book.parkedSpares {
		origins[spare.System] = true
	}

	ordered := make([]string, 0, len(origins))
	for system := range origins {
		ordered = append(ordered, system)
	}
	sort.Strings(ordered)

	neighbours := make(map[string][]string, len(ordered))
	for _, system := range ordered {
		adjacent, err := p.Gates.Neighbours(ctx, system)
		if err != nil {
			return nil, fmt.Errorf("failed to read gate neighbours of %q: %w", system, err)
		}
		neighbours[system] = adjacent
	}
	return neighbours, nil
}

// markFrontier records a PENDING row for every neighbour we have never
// evaluated, which is what puts it in front of the screening sweep. The verdict
// written is PENDING and nothing else: this engine has looked at nothing and
// must not appear to have judged anything.
func markFrontier(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	neighbours map[string][]string,
	known map[string]bool,
	rep *ExpandReport,
) error {
	for _, system := range sortedKeys(neighbours) {
		for _, neighbour := range neighbours[system] {
			if neighbour == "" || known[neighbour] {
				continue
			}
			if err := p.Ledger.UpsertSystem(ctx, playerID, SystemRecord{
				System:  neighbour,
				Verdict: VerdictPending,
			}); err != nil {
				return fmt.Errorf("failed to record frontier system %q: %w", neighbour, err)
			}
			known[neighbour] = true
			rep.Discovered++
		}
	}
	return nil
}

package parkedsensing

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
)

// ferry.go breaks the buying deadlock for MARKET placements.
//
// THE DEADLOCK. A purchase needs a hull ALREADY STANDING at the counter (see
// buyerAt), and resolvePurchaseCandidates looked only inside the placement's own
// system. So a system with no probe in it had no buyer, produced no candidates,
// and could never be filled: purchase required presence and presence required
// purchase, and there is no way to bootstrap a system from inside itself.
// Measured live: 247 pending placements, ZERO purchases per tick, 3.8M treasury,
// probes at 24,356, and of the ~22 systems holding a pending placement exactly
// ONE had any evidenced probe yard. The nine yards that exist are all somewhere
// else.
//
// WHAT BREAKS IT. Buy at a system that DOES have a counter, and fly the hull to
// the target. Nothing here flies it: the purchase is recorded BOUGHT against the
// placement exactly as a local one is, and the placement machine's existing
// dispatch → flyToSlot → RouteAcross walk carries it from there, one step per
// tick. That is the same machinery the foothold path and the seed claims already
// ride, and reusing it is why this file adds no navigation, no deadline, and no
// wait — the non-blocking property (sp-uwxwo) is preserved by reuse rather than
// re-argued.
//
// THE ROUTE IS RESOLVED BEFORE THE MONEY MOVES, which is the safety property
// rather than a preference. A hull bought for a target its source cannot reach is
// STRANDED: nextHopToward names no next system, the step errors every tick, and
// the row sits IN_TRANSIT naming a hull that never arrives while still counting
// against the probe cap. That is strictly worse than not buying — money spent for
// a probe that can never scan, and cap headroom held forever — so a source that
// cannot reach the target is not offered as a counter at all.
//
// THE DIRECTION OF THAT TEST IS LOAD-BEARING, and it is where the earlier version
// of this file (reverted as 4f4ca8ce) was wrong. It walked FORWARD OUT OF THE
// TARGET and treated the result as valid sources, which is only the same set on a
// symmetric graph — and 624 of 5,488 live gate edges (11.4%) have no reverse row.
// For a target reachable only over a one-way edge that search returns precisely
// the systems a hull CANNOT arrive from. This version asks each candidate source
// whether IT can reach the target, through the same forward-per-origin walker the
// foothold path uses (sp-e7e859a4), so the route offered is the route the
// placement machine will actually fly.
//
// THE FRONTIER IS NOT REFUSED FOREVER by the reach bound. Each ferried hull
// converts another system to covered, and a covered system's own counters come
// inside the reach of the next ring out. The map widens by conversion.

// maxFerryCandidates bounds how many CROSS-SYSTEM counters one placement may
// offer. The local list is never truncated — this bound applies only to the
// fallback.
//
// It exists because the attempt budget is shared by the whole tick. fillSlot
// works down a placement's candidates until one sells, and every counter tried
// costs an attempt whether it sells or not, so an unbounded remote list would let
// ONE placement in an unlucky region spend all six attempts re-learning that its
// neighbourhood is not selling — starving every other placement behind it,
// including the seeds. Three preserves the reason fillSlot walks a list at all (a
// refusal is usually local to the counter, and the yard next door is still good)
// while capping one placement's share of the budget at half.
const maxFerryCandidates = 3

// maxFerryHops is how far a BOUGHT hull may be flown to reach the placement it
// was bought for.
//
// IT IS THE ROUTER'S BOUND, and that is the whole derivation. A ferried hull is
// carried by the placement machine's RouteAcross, which resolves one hop at a
// time through nextHopToward — a breadth-first search that returns an ERROR when
// the destination is not found inside its own ring limit. The adapter pins that
// limit to MaxSeedFlightHops (`const maxWalkRings = appSensing.MaxSeedFlightHops`
// in adapters/parkedsensing), so nine hops is exactly what the router can
// actually deliver. Buying for anything further does not stretch the walk, it
// strands the hull: the step errors every tick, the slot sits IN_TRANSIT naming a
// hull that never arrives, and the probe cap is held the whole time.
//
// SO THIS IS DERIVED, NEVER CHOSEN. Reading it from MaxSeedFlightHops is what
// keeps the buy and the walk from drifting apart — the failure MaxWalkRings' own
// doc warns about, where a caller works from a private copy of a bound and hands
// out errands the resolver cannot serve. A literal 9 here would be that copy.
//
// WHY NOT MaxWalkRings, WHICH THIS USED TO READ. That constant bounds the
// FOOTHOLD path's draw of an already-parked scanning hull off a working market —
// a different and deliberately shorter journey, and one whose cost is real
// (a market stops being watched). It was the right conservative default when
// cross-system buying was first restored, but it is not the router's reach and
// never was. Measured live, it stranded 143 of 238 pending placements that sit
// 3-9 hops from their nearest funder: routable, and refused by a bound borrowed
// from another concern. MaxWalkRings is deliberately left where it is; giving the
// ferry its own bound is the per-instance separation sp-9fdc258d established.
//
// WHAT NINE COSTS IS TICKS, NOT CREDITS. A gate jump burns no fuel — it is
// instantaneous at the API with only a reactor cooldown — and one crossing is two
// dispatch steps, so nine hops is roughly eighteen. A probe in transit is not
// scanning; but the placements this reaches were not going to be bought at all,
// so the comparison is against nothing, not against a faster purchase. Nine is
// also where this graph SATURATES (see MaxSeedFlightHops): a bound of ten serves
// not one additional placement, and five sit unreachable at any depth.
const maxFerryHops = MaxSeedFlightHops

// ferryBroker is one tick's cross-system buying state: where our hulls actually
// stand, and the gate walker that decides which of those places can reach a given
// placement.
//
// LAZY AND LATCHED, for the same reason footholdBroker is. Once a region is
// covered every placement finds a local counter and this path is never reached;
// loading it at the top of the drain would spend a ledger read per tick to answer
// a question nobody asked. The read happens on the first placement that genuinely
// cannot fund itself, and then once for the whole tick.
//
// Per-tick, never longer (RULINGS #2): it is constructed by DrainBuyQueue and
// discarded with it, so a source that stops holding a hull is re-learned next
// tick rather than remembered.
type ferryBroker struct {
	// systems names every system holding a PARKED placement — the systems where a
	// buyer could plausibly be standing. It is the candidate set the reach test
	// filters, and it is why this path enumerates our own presence rather than the
	// whole map: a source with no hull of ours cannot sell to us at any distance.
	systems []string
	reach   *gateReach
	// loaded and blocked are the two terminal states of the lazy read, latched so
	// a failure costs one read for the tick rather than one per placement.
	loaded, blocked bool
}

// load reads where our hulls stand and reports whether a cross-system search is
// possible at all.
//
// A read failure is LATCHED rather than returned: the drain's local work is
// unaffected and must go on, exactly as footholdBroker.load treats the same
// failure. It is logged rather than swallowed, because "no remote counter" and
// "we could not look" are different facts and only one of them is normal.
func (b *ferryBroker) load(ctx context.Context, p BuyPorts, playerID int) bool {
	if b.blocked {
		return false
	}
	if b.loaded {
		return true
	}
	parked, err := p.Ledger.SlotsByState(ctx, playerID, SlotStateParked)
	if err != nil {
		b.blocked = true
		logging.LoggerFromContext(ctx).Log("WARN", "cross-system buying could not read the parked placements; local counters only this tick", map[string]interface{}{
			"action": "parked_sensing_ferry_presence_unreadable",
			"error":  err.Error(),
		})
		return false
	}
	seen := make(map[string]bool, len(parked))
	for _, row := range parked {
		if row.System == "" || row.AssignedShip == "" || seen[row.System] {
			continue
		}
		seen[row.System] = true
		b.systems = append(b.systems, row.System)
	}
	sort.Strings(b.systems)
	// maxFerryHops, which is the ROUTER's reach rather than the foothold path's.
	// See the constant: a bought hull is flown by RouteAcross, and buying beyond
	// what nextHopToward can resolve strands it.
	b.reach = newGateReach(p.Gates, nil, maxFerryHops)
	b.loaded = true
	return true
}

// ferryCandidates lists executable counters in OTHER systems for a placement its
// own system cannot fund, nearest gate ring first.
//
// SOURCE ORDER IS BY GATE JUMPS, then symbol. A nearer source is fewer
// RouteAcross steps, which is both fewer navigation calls and a hull that starts
// scanning sooner. Within a source, ListProbeYards is already ordered
// cheapest-first, so the cheapest counter of the nearest ring is tried first and
// price still breaks the tie where it can.
//
// COSTS NO API CALL TO RESOLVE. All three reads behind it — the gate adjacency,
// the yard list, and the docked-hull lookup — are local store reads by contract.
// Only the Quote and Buy that fillSlot then makes touch the network, and those
// are already bounded by the attempt budget.
//
// Returns nothing, rather than an error, when the topology cannot be read or no
// source has a counter. Both are ordinary answers: the placement simply waits,
// exactly as it did before this path existed, and the drain's other work is
// unaffected.
func ferryCandidates(ctx context.Context, p BuyPorts, playerID int, slot QueuedSlot, broker *ferryBroker) ([]purchaseCandidate, error) {
	if p.Gates == nil {
		// No topology wired: no cross-system guess. This is the pre-ferry
		// behaviour exactly, and it is a supported wiring (see BuyPorts.Gates).
		return nil, nil
	}
	if !broker.load(ctx, p, playerID) {
		return nil, nil
	}

	// Each candidate source is asked whether IT can reach the target — the
	// direction the hull will actually fly. See the file header for why the
	// reverse walk is not equivalent.
	sources, err := originsWithinReach(ctx, broker.reach, broker.systems, slot.System)
	if err != nil {
		// The topology store could not answer. Only this placement is passed over
		// — another's neighbourhood may read perfectly well — and it is NAMED, so
		// an operator can tell an unreadable gate graph apart from a genuinely
		// unserved region.
		logging.LoggerFromContext(ctx).Log("WARN", "sensing placement could not resolve its gate reach; no cross-system counter this tick", map[string]interface{}{
			"action":   "parked_sensing_ferry_reach_failed",
			"waypoint": slot.Waypoint,
			"system":   slot.System,
			"error":    err.Error(),
		})
		return nil, nil
	}

	candidates := make([]purchaseCandidate, 0, maxFerryCandidates)
	for _, source := range sources {
		yards, err := p.Yards.ListProbeYards(ctx, source)
		if err != nil {
			return nil, fmt.Errorf("failed to list probe yards in %q: %w", source, err)
		}
		for _, yard := range yards {
			// inSystem is nil on purpose: it is the TARGET system's ledger rows,
			// and none of them can name a hull standing in a different system. The
			// docked-hull read is the authority for a remote counter.
			buyer, found, err := buyerAt(ctx, p, playerID, yard, nil)
			if err != nil {
				return nil, err
			}
			if !found {
				continue
			}
			candidates = append(candidates, newPurchaseCandidate(slot.System, yard, buyer))
			if len(candidates) >= maxFerryCandidates {
				return candidates, nil
			}
		}
	}
	return candidates, nil
}

package parkedsensing

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
)

// ferry.go breaks the buying deadlock for MARKET placements: a purchase needs a hull
// ALREADY STANDING at the counter (see buyerAt), so a system holding no probe of ours
// cannot bootstrap itself. It buys where a counter exists and lets the hull cross;
// nothing here flies it — the purchase is recorded BOUGHT exactly as a local one is
// and the placement machine's dispatch → flyToSlot → RouteAcross walk carries it, so
// this file adds no navigation, no deadline and no wait.
//
// THE ROUTE IS RESOLVED BEFORE THE MONEY MOVES: a hull bought for a target its source
// cannot reach is STRANDED — the step errors every tick, the row sits IN_TRANSIT
// naming a hull that never arrives, and cap headroom is held forever — so an
// unreachable source is never offered as a counter. THE DIRECTION OF THAT TEST IS
// LOAD-BEARING: gate edges are not all reciprocal, so walking forward OUT OF the
// target returns precisely the systems a hull CANNOT arrive from. Each source is
// asked whether IT reaches the target, and that route is the one that will be flown.
// The bound refuses no frontier forever — each ferried hull converts a system to
// covered, bringing its counters inside the next ring's reach.

// maxFerryCandidates bounds how many CROSS-SYSTEM counters one placement may offer;
// the local list is never truncated. The attempt budget is shared by the whole tick
// and every counter tried costs an attempt whether it sells or not, so an unbounded
// remote list lets ONE placement spend the entire budget re-learning that its
// neighbourhood is not selling and starve everything behind it, seeds included.
// Three still lets fillSlot walk a list — a refusal is usually local to the counter.
const maxFerryCandidates = 3

// maxFerryHops is how far a BOUGHT hull may be flown to reach the placement it was
// bought for. IT IS THE FERRY'S OWN BOUND — neither the router's nor the seed's —
// and must stay inside the router's reach: a ferried hull is carried by RouteAcross,
// which resolves one hop at a time through nextHopToward and ERRORS when the
// destination is not found, so buying past that strands the hull (the step errors
// every tick, the slot sits IN_TRANSIT, and the probe cap is held the whole time).
// NOT MaxWalkRings, which bounds the FOOTHOLD path's shorter draw of a parked
// scanning hull off a working market; NOT SeedFlightUnbounded either, whose reach is
// the graph rather than a count because the system that can still grow the map sits
// past any number we would pick. The flight costs CREDITS as well as ticks — a gate
// crossing charges a fee — so it is PRICED (domain/parkedsensing.FerryCost and the
// landed-cost check in fillSlot) and refused by the buy floor when unaffordable.
const maxFerryHops = 9

// ferryBroker is one tick's cross-system buying state: where our hulls stand, and
// the gate walker deciding which of those places can reach a given placement.
//
// LAZY AND LATCHED, as footholdBroker is: once a region is covered every placement
// finds a local counter and this path is never reached, so the ledger read happens on
// the first placement that cannot fund itself, then once for the whole tick.
// Per-tick, never longer (RULINGS #2) — a source that stops holding a hull is
// re-learned next tick rather than remembered.
type ferryBroker struct {
	// systems names every system holding a PARKED placement — where a buyer could
	// plausibly be standing, and the candidate set the reach test filters. A source
	// with no hull of ours cannot sell to us at any distance.
	systems []string
	reach   *gateReach
	// loaded and blocked are the two terminal states of the lazy read, latched so a
	// failure costs one read for the tick rather than one per placement.
	loaded, blocked bool
}

// load reads where our hulls stand and reports whether a cross-system search is
// possible at all. A read failure is LATCHED rather than returned — the drain's local
// work is unaffected and must go on — and logged rather than swallowed, because "no
// remote counter" and "we could not look" are different facts.
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
	// maxFerryHops is the ROUTER's reach, not the foothold path's: a bought hull is
	// flown by RouteAcross, and buying beyond what nextHopToward resolves strands it.
	b.reach = newGateReach(p.Gates, nil, maxFerryHops)
	b.loaded = true
	return true
}

// candidates lists executable counters in OTHER systems for a placement its own
// system cannot fund, nearest gate ring first.
//
// SOURCE ORDER IS BY GATE JUMPS, then symbol: a nearer source is fewer RouteAcross
// steps and a hull scanning sooner, and within a source ListProbeYards is already
// cheapest-first so price still breaks the tie. It COSTS NO API CALL to resolve —
// gate adjacency, yard list and docked-hull lookup are local store reads by contract;
// only the Quote and Buy that fillSlot then makes touch the network. It returns
// nothing rather than an error when the topology cannot be read or no source has a
// counter: the placement waits, and other work is unaffected.
func (b *ferryBroker) candidates(ctx context.Context, p BuyPorts, playerID int, slot QueuedSlot) ([]purchaseCandidate, error) {
	sources := b.sources(ctx, p, playerID, slot)

	candidates := make([]purchaseCandidate, 0, maxFerryCandidates)
	for _, source := range sources {
		hops := b.hopsFrom(ctx, source, slot.System)
		yards, err := p.Yards.ListProbeYards(ctx, source)
		if err != nil {
			return nil, fmt.Errorf("failed to list probe yards in %q: %w", source, err)
		}
		for _, yard := range yards {
			// inSystem is nil on purpose: the TARGET system's ledger rows cannot name
			// a hull elsewhere, so the docked-hull read is the authority here.
			buyer, found, err := buyerAt(ctx, p, playerID, yard, nil)
			if err != nil {
				return nil, err
			}
			if !found {
				continue
			}
			candidate := newPurchaseCandidate(slot.System, yard, buyer)
			candidate.ferryHops = hops
			candidates = append(candidates, candidate)
			if len(candidates) >= maxFerryCandidates {
				return candidates, nil
			}
		}
	}
	return candidates, nil
}

// sources are the systems holding a hull that could fly to this placement. Each is
// asked whether IT can reach the target — the direction the hull will actually fly;
// see the file header for why the reverse walk is not equivalent. An empty answer
// covers all three ordinary refusals: no topology, no hulls, no readable gate store.
func (b *ferryBroker) sources(ctx context.Context, p BuyPorts, playerID int, slot QueuedSlot) []string {
	if p.Gates == nil {
		// No topology wired: no cross-system guess, local counters only. A supported
		// wiring, not a fault (see BuyPorts.Gates).
		return nil
	}
	if !b.load(ctx, p, playerID) {
		return nil
	}
	sources, err := b.reach.originsWithin(ctx, b.systems, slot.System)
	if err != nil {
		// The topology store could not answer. Only this placement is passed over, and
		// it is NAMED so an unreadable gate graph reads differently from an unserved region.
		logging.LoggerFromContext(ctx).Log("WARN", "sensing placement could not resolve its gate reach; no cross-system counter this tick", map[string]interface{}{
			"action":   "parked_sensing_ferry_reach_failed",
			"waypoint": slot.Waypoint,
			"system":   slot.System,
			"error":    err.Error(),
		})
		return nil
	}
	return sources
}

// hopsFrom is the hop count the buy floor prices the delivery from. It COSTS NOTHING
// to ask: originsWithin has already run this exact walk to order the sources and
// gateReach memoises one BFS per origin for the tick, so this is a map lookup.
//
// An unreadable walk is NOT read as zero hops — zero would price a cross-system ferry
// as free. The count is left unset, and FerryHops charges the one-gate minimum that
// `ferried` alone already proves.
func (b *ferryBroker) hopsFrom(ctx context.Context, source, target string) int {
	hops, within, err := b.reach.hops(ctx, source, target)
	if err != nil || !within {
		return 0
	}
	return hops
}

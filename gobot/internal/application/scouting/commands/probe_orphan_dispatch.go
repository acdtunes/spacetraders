package commands

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// probe_orphan_dispatch.go puts idle probes WE ALREADY OWN onto open placements. It spends nothing.
//
// THE HULLS IT REACHES, and why nothing else could. Adoption records an orphan where it STANDS, and
// sensing_slots holds one row per (player, waypoint) — so an orphan standing where a row already
// exists can never be recorded while that row does. On the live fleet that is most of them: four
// idle probes are stacked behind two PARKED rows at X1-KP23-A2 and -C38, invisible to the probe cap
// and doing nothing, while 74 placements go unfilled. ledgerHolds' own residual note describes this
// as structural and names the two shapes it leaves stuck; this pass is what unsticks the first of
// them, and it does so WITHOUT touching the keying (that is sp-dpfp8).
//
// THE INSIGHT IT IS BUILT ON. The BUY path never hits the collision, because it records the slot at
// the TARGET waypoint and flies the hull there — distinct target, distinct row. So this does exactly
// what buying does, minus the purchase: it names an open placement with a hull we already own and
// marks it IN_TRANSIT. There is no BOUGHT state to pass through, because there is nothing to buy.
//
// WHY IT MATTERS NOW RATHER THAN EVENTUALLY. Buying more probes is correctly refused and will stay
// refused — ProbeBuyFloor holds ~1.87M against a 703,861 treasury because cargo spend is 1.82M/hr
// and the floor reserves K hours of that runway. Reuse is therefore the ONLY path, and weakening the
// floor to make buying work again is a different decision entirely (RULINGS #4).
//
// WHY IT IS A SEPARATE PASS FROM ADOPTION. They answer different questions and take different
// actions. Adoption asks "is this hull on the books?" and writes a row where the hull already is;
// this asks "is this hull DOING anything?" and issues a flight. Folding the second into the first
// would put a navigation command behind a bookkeeping budget, so the two carry separate bounds — and
// the ordering between them is load-bearing (see the call site).
//
// WHY IT LIVES HERE AND NOT IN parkedsensing. The hulls it works on hold NO slot row, so finding
// them means enumerating the fleet — and internal/application/parkedsensing deliberately exposes no
// fleet-listing method anywhere (see ParkedShipReader): the sensing engines' per-tick cost is meant
// to scale with the placements they are working, not with the size of the fleet. That is exactly why
// parkedsensing's own reuseSpareHull can only see hulls ALREADY recorded as spare slots, and exactly
// why it cannot reach these. The coordinator holds the fleet reader; the pass belongs beside
// adoption, which reaches these hulls the same way.

// DefaultMaxOrphanDispatches bounds how many hulls one tick may send flying. A plain constant,
// deliberately not a knob, in the same class as DefaultMaxAdoptions and DefaultMaxPlacementActions:
// it paces a burst of ledger writes and navigation commands, not an economic choice — the hulls are
// already paid for and dispatching them costs nothing. A backlog is not lost; the hulls left over
// are still idle, still orphaned, and still first in line next tick.
const DefaultMaxOrphanDispatches = 10

// idleOrphanHull is an owned probe that is doing nothing, holds no slot row, and can be flown.
type idleOrphanHull struct {
	ship     *navigation.Ship
	waypoint string
}

// dispatchIdleOrphans sends idle probes the ledger has no row for to open placements — in their own
// system where there is one, otherwise as far across the gates as the walk can carry them — and
// reports how many it dispatched.
//
// Idempotent by construction: a dispatched hull is named by the placement's row from the first
// write onward, so the next tick's ledger read strikes it off as already recorded. A settled fleet
// therefore costs this pass its three reads and not one write, however many ticks run.
func (h *RunProbeSensingCoordinatorHandler) dispatchIdleOrphans(
	ctx context.Context,
	cmd *RunProbeSensingCoordinatorCommand,
	ports SensingEnginePorts,
	failures *[]error,
) int {
	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID.Value()

	// ALL THREE READS FAIL CLOSED, and the post list is the sharpest of them: read permissively, an
	// unreadable post list is an EMPTY one, which means "no hull is manned" — and this pass would
	// then fly the probe manning the surviving home post out from under the scout coordinator. An
	// unreadable input must never read as "nothing is assigned"; that is the shape that authorises
	// taking a busy hull.
	ships, err := h.fleetRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		*failures = append(*failures, fmt.Errorf("failed to list the fleet for idle-orphan dispatch: %w", err))
		return 0
	}
	posts, err := h.postRepo.ListActive(ctx, playerID)
	if err != nil {
		*failures = append(*failures, fmt.Errorf("failed to list scout posts for idle-orphan dispatch: %w", err))
		return 0
	}
	holds, err := ledgerHoldings(ctx, ports, playerID)
	if err != nil {
		*failures = append(*failures, err)
		return 0
	}

	manned := make(map[string]bool, len(posts))
	for _, post := range posts {
		if post.AssignedHull != "" {
			manned[post.AssignedHull] = true
		}
	}

	orphans, standing := idleOrphans(ships, manned, holds)
	if len(orphans) == 0 {
		return 0
	}
	targets := openPlacements(holds, standing)
	if len(targets) == 0 {
		return 0
	}
	// One walker per tick, so the topology behind a burst of dispatches is read once per distinct
	// system the hulls are standing in rather than once per hull.
	reach := newGateReach(ports.Gates, failures)

	dispatched, writes := 0, 0
	for _, orphan := range orphans {
		// The budget counts WRITES, not candidates, for the reason adoption spells out: a failed
		// write costs the database the same as one that succeeded, so spending budget on it is what
		// stops a persistently failing ledger from being retried without bound inside one tick.
		// Unlike adoption this loop is already filtered to hulls that WILL be written, so the
		// ineligible-majority starvation the same counter guards against there cannot arise here.
		if writes >= DefaultMaxOrphanDispatches {
			break
		}

		// AS FAR AS THE WALK REACHES, AND NOT ONE SYSTEM FURTHER.
		//
		// This was same-system-only, and the restriction was honest when it was written: it matched
		// the predicate flyToSlot uses to choose NavigateWithin over RouteAcross, because RouteAcross
		// REFUSED outright — the stopgap from sp-uwxwo, since routing a whole crossing meant blocking
		// the tick on multi-jump and cooldown waits. Handing out a cross-gate errand then would have
		// been handing out an errand nothing could perform.
		//
		// THAT PREMISE IS GONE. RouteAcross now advances one step per tick and never waits, so the
		// placement machine carries a hull across gates as readily as across a system. What is left
		// of the old restriction is not caution, it is a wall: on the live map there is not ONE open
		// placement in a system holding an idle hull — all 66 are in the eight systems next door — so
		// same-system-only makes this pass a no-op by construction, not a conservative version of
		// itself.
		//
		// THE BOUND THAT REPLACES IT IS THE WALK'S OWN. reachableSystems runs the same bounded
		// breadth-first search over the same stored adjacency that nextHopToward runs, out to the
		// same shared MaxWalkRings, so "a target this pass will hand out" stays exactly "a target the
		// walk can resolve". Overshooting is not a loud failure: the walk would name no next system,
		// return an error, and leave the slot IN_TRANSIT naming a hull that never arrives and never
		// stops counting against the probe cap.
		//
		// MULTI-CROSSING IS FINE, and it is why the bound is the walk's reach rather than one gate.
		// The walk re-derives its next jump each tick from where the hull ACTUALLY is, and BFS names
		// a system on a SHORTEST route — so distance to the destination strictly decreases: a target
		// two rings out is one ring out after a jump and an in-system hop after the next. A target
		// admitted here therefore stays inside the walk's reach at every step of the crossing.
		//
		// NEAREST FIRST, so a hull never crosses a gate while its own system has an open placement.
		system := shared.ExtractSystemSymbol(orphan.waypoint)
		target, found := takeOpenPlacement(targets, system, reach.from(ctx, system))
		if !found {
			continue // nothing open it can reach; it waits, and takes nothing from the hulls that can
		}

		hull := orphan.ship.ShipSymbol()
		writes++
		// THE WRITE ORDER IS A MONEY GUARD.
		//
		// claimSpares and reuseSpareHull both order two writes so that a crash between them
		// OVER-counts the fleet rather than under-counting it, because an over-count only ever buys
		// FEWER probes while an under-count authorises buying a replacement for a hull we already
		// own (RULINGS #4). Here the RELEASE LEG IS ABSENT — an orphan is named by no row at all,
		// which is the whole reason it is stuck — so this stamps the errand and has nothing to hand
		// back. There is no window in which the hull is named by neither row, because it starts named
		// by neither and ends named by one.
		//
		// The two writes that remain are the row and the fleet tag, and the same rule decides their
		// order for the same reason. The ROW is what CountOwnedProbes reads:
		//
		//   - row then tag (as here): a failure leaves the hull recorded and untagged. It counts
		//     against the probe cap, and the placement machine re-tags it on first use. Recoverable.
		//   - tag then row: a failure leaves the hull TAGGED sensing_parked with no row anywhere —
		//     invisible to the cap for good, and the cap then authorises re-buying it. Money-unsafe,
		//     and it is the exact shape that cost this fleet 245,316 credits.
		//
		// So the row goes first and the tag is best-effort behind it, exactly as in adoption.
		//
		// This writes IN_TRANSIT for a hull that has NOT been told to move. The placement machine's
		// dispatchClaim branch is what notices a still hull on an in-flight row and flies it; that
		// branch is load-bearing for this path, not an edge case — without it the hull would stand
		// where it is forever while the row read as in-flight.
		err := ports.Ledger.TransitionSlot(ctx, playerID, target.Waypoint, target.Kind,
			parkedsensing.SlotStateWanted, parkedsensing.SlotStateInTransit,
			parkedsensing.SlotFields{AssignedShip: &hull})
		switch {
		case errors.Is(err, parkedsensing.ErrSlotClaimed):
			// Another writer took the placement between the read and the write. Routine contention,
			// nothing spent, and nothing to unwind — the hull is untouched and still an orphan.
			continue
		case err != nil:
			*failures = append(*failures, fmt.Errorf(
				"failed to dispatch idle probe %s to the open placement at %s: %w", hull, target.Waypoint, err))
			continue
		}

		// Best-effort, for the reason above: the cap counts rows, and the row is written. Named
		// rather than silent, because an untagged hull reads as idle and undedicated to every other
		// coordinator's ownership sweep — long enough to be poached mid-flight.
		if tagErr := ports.Fleet.AssignFleet(ctx, playerID, hull, parkedsensing.SensingParkedFleetTag); tagErr != nil {
			logger.Log("WARNING", fmt.Sprintf(
				"Idle probe %s is now dispatched to %s but keeps its old fleet tag (the probe cap counts it; the placement machine re-tags it on first use): %v",
				hull, target.Waypoint, tagErr), map[string]interface{}{
				"action":      "parked_sensing_dispatch_tag_failed",
				"ship_symbol": hull,
				"waypoint":    target.Waypoint,
			})
		}
		holds.hulls[hull] = true
		dispatched++
	}

	if dispatched > 0 {
		logger.Log("INFO", fmt.Sprintf(
			"Dispatched %d idle probe(s) we already own to open sensing placements (no purchase — these hulls were paid for and standing idle)",
			dispatched), map[string]interface{}{
			"action":     "parked_sensing_dispatched_orphans",
			"dispatched": dispatched,
			"examined":   len(ships),
		})
	}
	return dispatched
}

// idleOrphans selects the hulls this pass may fly, and reports every waypoint an eligible idle
// orphan is standing on.
//
// The second return is what keeps the two passes from working against each other. A placement an
// idle orphan is ALREADY STANDING ON is adoption's: it fills the row in place, for free, with no
// flight at all. Flying a different hull to that waypoint would burn a hop to do what adoption does
// for nothing AND leave the placement under the standing hull's feet still unfilled, so those
// waypoints are struck out of the target pool below.
func idleOrphans(ships []*navigation.Ship, manned map[string]bool, holds ledgerHolds) ([]idleOrphanHull, map[string]bool) {
	orphans := make([]idleOrphanHull, 0, len(ships))
	standing := make(map[string]bool, len(ships))

	for _, ship := range ships {
		if !ship.IsScoutType() || !adoptableFleetTag(ship.DedicatedFleet()) {
			continue // not a probe frame, or dedicated to a fleet that is not ours to take (RULINGS #7)
		}
		if !ship.IsIdle() {
			// DRIVEN BY A LIVE CONTAINER — leave it alone (RULINGS #3, single writer).
			//
			// Three live probes sit `active` on scout_tour containers. Flying one here would move a
			// hull mid-tour while its container still believes it owns it: two writers, one hull, and
			// a ship stranded between two intentions. The `manned` map below does NOT cover this — it
			// reads scout POSTS, and a tour is not a post — which is why both checks are needed and
			// why adoption carries the identical pair.
			//
			// Nothing is lost by waiting. This pass runs every tick, so the hull is picked up on the
			// first tick after its tour releases it.
			continue
		}
		if manned[ship.ShipSymbol()] {
			continue // still manning a surviving scout post — that post's hull, not ours to take
		}
		if holds.hulls[ship.ShipSymbol()] {
			// Already named by a row somewhere, so it is not an orphan and not idle CAPACITY: it is
			// a hull with a placement. Re-pointing it would hand that placement away and, for the
			// instant between the writes, leave one hull answering two rows.
			continue
		}
		location := ship.CurrentLocation()
		if location == nil || location.Symbol == "" {
			// In transit. Not an error — just a hull with nowhere to be dispatched FROM. Commanding
			// a hull whose position we cannot read is how a ship ends up desynced from the ledger.
			continue
		}

		standing[location.Symbol] = true

		// ADOPTION'S, NOT OURS. The predicate is adoption's exactly — it IS adoption's, called
		// directly, so the two cannot drift — and it ignores the slot KIND because adoption's
		// in-place fill does too. Every hull adoption can absorb where it stands is left for it,
		// and this pass only ever picks up what adoption structurally cannot reach.
		if _, fillable := holds.fillableAt(location.Symbol); fillable {
			continue
		}
		orphans = append(orphans, idleOrphanHull{ship: ship, waypoint: location.Symbol})
	}
	return orphans, standing
}

// openPlacements is the target pool: every unfilled placement, grouped by system and ordered by
// waypoint within it.
//
// DETERMINISTIC BY WAYPOINT, and the pool stays GROUPED BY SYSTEM because that grouping is what the
// reach is expressed in: a hull is offered its own system's rows, then a neighbour's, then a
// neighbour's neighbour's, and the ring a system sits in is the only distance this pass measures.
// Within a system, waypoint order — proximity ordering there would be choosing between hops of
// comparable cost with no information to do it well, and would buy a dependency on the waypoint
// graph this pass otherwise does not need. Both orders are reproducible tick to tick, which is what
// makes a re-run of a tick land the same hulls on the same placements.
//
// WHAT IS EXCLUDED, and why each would be a mistake:
//
//   - a row that NAMES a hull: it is somebody's placement, whatever state it is in. Re-pointing it
//     drops a working probe out of the cap and hands its placement away.
//   - QUEUED: the drain has claimed it for purchase and its TransitionSlot(QUEUED→BOUGHT) is guarded
//     on that state, so filling it breaks a claim money is already riding on.
//   - SPARE kind: a SPARE want is the expansion engine's charting-seed STAGING request, tracked in
//     its own slot book — not a sensing placement. Writing a hull onto it would desync that
//     accounting. (Adoption's in-place fill does not make this distinction, and does not need to:
//     it never moves a hull, so it cannot strand one on somebody else's errand.)
//   - a waypoint an idle orphan is standing on: adoption fills it in place for free.
func openPlacements(holds ledgerHolds, standing map[string]bool) map[string][]parkedsensing.QueuedSlot {
	targets := map[string][]parkedsensing.QueuedSlot{}
	// Two loops because a waypoint can carry more than one placement now
	// (sp-dpfp8) — the SPARE filter below is exactly what keeps the extra row from
	// becoming a target, so both have to be looked at rather than whichever one an
	// index happened to keep.
	for _, rows := range holds.rows {
		for _, row := range rows {
			if row.State != parkedsensing.SlotStateWanted || row.AssignedShip != "" {
				continue
			}
			if row.Kind == parkedsensing.SlotKindSpare || standing[row.Waypoint] {
				continue
			}
			targets[row.System] = append(targets[row.System], row)
		}
	}
	for system := range targets {
		open := targets[system]
		sort.Slice(open, func(i, j int) bool { return open[i].Waypoint < open[j].Waypoint })
	}
	return targets
}

// gateReach answers "which systems can the walk carry a hull to from here?", once per system per
// tick.
//
// MEMOISED, because the answer is a property of the MAP and the tick reads it repeatedly: the live
// fleet's idle hulls are stacked in one or two systems, so a dozen dispatches ask the same question
// a dozen times. The cache is per-tick and per-call — built at the top of the dispatch and dropped
// with it — so it can never serve a topology the fleet has since re-charted.
//
// A FAILED READ IS AN EMPTY REACH, and that is the fail-closed direction. Not knowing the topology
// must never read as "everything is reachable": that is precisely how a hull is sent toward a system
// the walk cannot resolve, where it sits IN_TRANSIT forever, still named by the row and still
// counting against the probe cap. An empty reach costs nothing — same-system dispatch needs no
// topology at all and is unaffected — so a dark gate graph degrades this pass to exactly the
// behaviour it had before the walk existed.
type gateReach struct {
	gates    parkedsensing.GateNeighbours
	failures *[]error
	cache    map[string][]string
}

func newGateReach(gates parkedsensing.GateNeighbours, failures *[]error) *gateReach {
	return &gateReach{gates: gates, failures: failures, cache: map[string][]string{}}
}

// from returns the systems within the walk's reach of origin, NEAREST RING FIRST and alphabetically
// within a ring. origin itself is not included — the caller has already offered it.
//
// The ORDER is the allocation policy, so it is fixed rather than incidental. Nearest-first means a
// hull crosses the fewest gates that will reach an open placement, which is both the cheapest
// crossing and the soonest arrival; sorting within a ring makes a re-run of a tick land the same
// hulls on the same placements, which is the same reproducibility openPlacements sorts for.
func (r *gateReach) from(ctx context.Context, origin string) []string {
	if reachable, done := r.cache[origin]; done {
		return reachable
	}
	reachable := r.walk(ctx, origin)
	r.cache[origin] = reachable
	return reachable
}

// walk is nextHopToward's search with its answer widened: the same breadth-first sweep over the same
// stored adjacency, bounded by the same shared MaxWalkRings, reporting every system it reaches
// rather than the first jump toward one of them. Running the same search under the same bound is
// what makes "this pass will offer it" and "the walk can resolve it" the same set.
func (r *gateReach) walk(ctx context.Context, origin string) []string {
	if r.gates == nil {
		return nil // unwired topology: same-system only, exactly as before the walk existed
	}

	var reachable []string
	seen := map[string]bool{origin: true}
	frontier := []string{origin}

	for ring := 0; ring < parkedsensing.MaxWalkRings && len(frontier) > 0; ring++ {
		var next []string
		for _, current := range frontier {
			systems, err := r.gates.Neighbours(ctx, current)
			if err != nil {
				// Named rather than swallowed: a topology read that keeps failing otherwise looks
				// exactly like a map with no gates in it, and this pass would quietly go on
				// dispatching nowhere. The reach built so far is kept — it was read successfully,
				// and every system in it is genuinely within the bound.
				*r.failures = append(*r.failures, fmt.Errorf(
					"failed to read the gate neighbours of %s while sizing the reach of idle probes standing in %s: %w",
					current, origin, err))
				continue
			}
			for _, candidate := range systems {
				if candidate == "" || seen[candidate] {
					continue
				}
				seen[candidate] = true
				next = append(next, candidate)
			}
		}
		// Within a ring, alphabetically — the port's ordering is not part of its contract, so the
		// determinism this pass needs is established here rather than assumed.
		sort.Strings(next)
		reachable = append(reachable, next...)
		frontier = next
	}
	return reachable
}

// takeOpenPlacement removes and returns the next placement CONSUMING it — the same
// allocate-by-consuming discipline slotBook keeps — searching system first and then each system in
// reachable, which arrives nearest-ring-first.
//
// WHAT CONSUMING ACTUALLY BUYS, stated precisely because the obvious answer is wrong. It is NOT what
// stops two hulls being written onto one placement: the ledger already refuses that, because the
// WANTED→IN_TRANSIT transition is guarded on the from-state, so the second hull's write comes back
// ErrSlotClaimed and is skipped with nothing spent. Safety here is the ledger's, and a mutation
// probe that removed this line left every one-placement-two-hulls test green — which is the honest
// reason this comment does not claim otherwise.
//
// What it buys is THROUGHPUT, and the difference is the whole live fix. Read without consuming,
// every eligible hull in a system is offered the SAME first placement: one wins, and each of the
// rest loses the race, is skipped, and is never offered any of the other open rows. One hull per
// system per tick, forever. On the live fleet that is one dispatch where four are available, with 74
// placements standing empty — so the loop would appear to work while leaving the fleet almost
// exactly as idle as it found it.
//
// A consumed placement is not returned to the pool even when its write LOSES the race, because in
// that case it is genuinely gone: another writer holds it.
func takeOpenPlacement(
	targets map[string][]parkedsensing.QueuedSlot,
	system string,
	reachable []string,
) (parkedsensing.QueuedSlot, bool) {
	// The hull's own system first, ALWAYS, whatever the reach says. An in-system hop crosses no
	// gate, so a hull that can be placed where it stands never spends a crossing to be placed
	// somewhere else — and the pass keeps its pre-walk behaviour exactly wherever that is possible.
	for _, candidate := range append([]string{system}, reachable...) {
		open := targets[candidate]
		if len(open) == 0 {
			continue
		}
		targets[candidate] = open[1:]
		return open[0], true
	}
	return parkedsensing.QueuedSlot{}, false
}

package commands

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// probe_sensing_surge.go dispatches SURPLUS probes we already own into the
// charted-but-unpriced map, several at a time. It spends nothing (sp-zvywu Part 1).
//
// THE POOL, AND WHY IT IS THE TRIGGER. Measured live: 1,215 systems in the sensing
// ledger, 1,183 of them charted in the open era, and 116 carrying ANY market_data
// row — under 10% priced. So 1,067 charted systems have prices nobody has ever
// read, and ~300 of the 686 probes stand in no sensing slot at all. The original
// spec fired this pass on REACHABLE-GRAPH GROWTH (a gate completion opening new
// territory); that aims at the smaller gap, and it makes the surge wait for an
// event when the work is already on the floor. The trigger is therefore the pool
// itself, derived as a SET DIFFERENCE each tick:
//
//	charted systems (waypoints, open era)  MINUS  systems with any market_data row
//
// Graph growth is still an input — it is what puts a system inside the walk's
// reach at all, and a newly-reachable system enters the ranking at distance 1 —
// but it is not the gate. See UnpricedSystemPool.
//
// WHY IT IS A SEPARATE PASS FROM THE ORPHAN DISPATCH. That pass fills placements
// THE SCREEN ALREADY DECLARED: it consumes hull-less WANTED rows and can never
// reach a system that has no rows at all, which is every system in this pool. This
// pass is the only one that DECLARES a placement without a screening verdict
// behind it — that is precisely how it reaches the 90% of charted space no other
// pass can — and the two are strictly complementary, because the target picker
// below skips any waypoint that already carries a row and leaves it to them.
//
// WHY IT LIVES HERE AND NOT IN parkedsensing. Same reason as adoption and the
// orphan dispatch: finding surplus hulls means ENUMERATING THE FLEET, and
// internal/application/parkedsensing deliberately exposes no fleet-listing method
// anywhere (see ParkedShipReader) so the sensing engines' per-tick cost scales with
// the placements they are working rather than with the size of the fleet. The
// coordinator holds the fleet reader; the pass belongs beside the two that reach
// these hulls the same way.
//
// RULINGS #2: nothing is held between ticks. The pool, the in-flight count, the
// surplus set and the ranking are all re-derived from durable state on every tick,
// so a restart mid-flight resumes from the ledger with nothing to rebuild.
//
// RULINGS #4: no money guard is touched and no new spend path is opened. The hulls
// are already paid for; the single write per dispatch is a placement row that is
// BORN NAMING ITS HULL, so it never passes through the hull-less WANTED state the
// buy queue drains — see the write itself.

// defaultSurgeInFlightCap bounds how many surge dispatches may be IN FLIGHT at
// once — not how many one tick may issue. A tick tops the population up to this
// number and stops, so the standing concurrency is bounded rather than merely the
// per-tick burst; without that distinction a tick could add its full burst on top
// of everything still flying, and the API cost of the surge would grow without
// limit while every individual tick looked well-behaved.
//
// WHY EIGHT. It sits under DefaultMaxPlacementActions (10), which is the per-tick
// budget the placement machine actually advances these hulls with: a standing
// surge population larger than that budget could not be advanced every tick, so
// the extra dispatches would buy latency rather than throughput. Live API
// utilisation has been running against its 85% ceiling, and the pacer and
// emergency brake still govern every scan the arriving probes then perform — this
// cap is what keeps the FLIGHTS from being the thing that breaks the ceiling.
//
// It is a knob (surge_inflight_cap) because it is an operational cap, which is
// what RULINGS #5 makes configurable; the const is its documented default, and
// `tune surge_inflight_cap 0` reverts to it — so the knob cannot be used to switch
// the pass off. The pass ships ARMED.
const defaultSurgeInFlightCap = 8

// The surge's pool contract lives in the domain so the adapter implementing it need
// not import this package. Aliases, not new types: the two names are identical to
// their domain declarations.
type (
	UnpricedSystem     = domainScouting.UnpricedSystem
	UnpricedSystemPool = domainScouting.UnpricedSystemPool
)

// surgeTarget is one pool system reduced to the single placement this pass would
// declare in it: the best free marketplace waypoint, plus its ranking inputs.
type surgeTarget struct {
	system   string
	waypoint string
	markets  int
	richness int
}

// surgeToUnpricedSystems tops the in-flight surge population up to the cap by
// sending surplus probes into the highest-value unpriced systems they can reach,
// and reports how many it dispatched.
//
// Idempotent by construction: a dispatched hull is named by the placement row from
// the first write onward, so the next tick's ledger read strikes it off both as a
// surplus hull and as in-flight capacity. A fleet with nothing surplus costs this
// pass its reads and not one write, however many ticks run.
func (h *RunProbeSensingCoordinatorHandler) surgeToUnpricedSystems(
	ctx context.Context,
	cmd *RunProbeSensingCoordinatorCommand,
	cfg sensingConfig,
	ports SensingEnginePorts,
	systems []parkedsensing.ExpandSystem,
	failures *[]error,
) int {
	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID.Value()

	// EVERY READ FAILS CLOSED. The pool read is the era-scoped one and refuses
	// rather than guessing (see UnpricedSystemPool); the post list is the sharpest,
	// because read permissively an unreadable post list is an EMPTY one — "no hull
	// is manned" — and this pass would then fly the probe manning the surviving home
	// post out from under the scout coordinator.
	pool, err := ports.UnpricedPool.UnpricedSystems(ctx, playerID)
	if err != nil {
		*failures = append(*failures, fmt.Errorf("failed to read the charted-but-unpriced sensing pool: %w", err))
		return 0
	}
	if len(pool) == 0 {
		return 0
	}

	// RE-READ, not the copy the passes above worked from. Adoption and the orphan
	// dispatch have already written this tick, and a hull one of them just claimed
	// must read as taken here — sharing their index would hand one hull two errands.
	holds, err := ledgerHoldings(ctx, ports, playerID)
	if err != nil {
		*failures = append(*failures, err)
		return 0
	}

	// THE IN-FLIGHT BUDGET, derived entirely from durable rows (RULINGS #2).
	budget := cfg.SurgeInFlightCap - inFlightSurge(pool, holds)
	if budget <= 0 {
		return 0
	}

	ships, err := h.fleetRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		*failures = append(*failures, fmt.Errorf("failed to list the fleet for the sensing surge: %w", err))
		return 0
	}
	posts, err := h.postRepo.ListActive(ctx, playerID)
	if err != nil {
		*failures = append(*failures, fmt.Errorf("failed to list scout posts for the sensing surge: %w", err))
		return 0
	}
	manned := make(map[string]bool, len(posts))
	for _, post := range posts {
		if post.AssignedHull != "" {
			manned[post.AssignedHull] = true
		}
	}

	surplus := surplusProbes(ships, manned, holds, systems)
	if len(surplus) == 0 {
		return 0
	}

	targets := surgeTargets(pool, holds)
	if len(targets) == 0 {
		return 0
	}
	// One walker per tick, memoised per origin: surplus probes stack in a handful of
	// systems, so the topology behind a burst of dispatches is read once per distinct
	// system the hulls stand in rather than once per hull.
	reach := newGateReach(ports.Gates, failures)

	dispatched, writes := 0, 0
	for _, hull := range surplus {
		// The budget counts WRITES, not candidates, for adoption's reason exactly: a
		// failed write costs the database what a successful one did, so spending
		// budget on it is what stops a persistently failing ledger being retried
		// without bound inside one tick. This loop is already filtered to hulls that
		// WILL be written, so the ineligible-majority starvation the same counter
		// guards against there cannot arise here.
		if writes >= budget {
			break
		}

		origin := shared.ExtractSystemSymbol(hull.waypoint)
		target, found := takeSurgeTarget(&targets, reach.hopsFrom(ctx, origin))
		if !found {
			continue // nothing in reach it could price; it waits, and takes nothing from the hulls that can
		}

		ship := hull.ship.ShipSymbol()
		writes++
		// ONE WRITE, AND THAT IS THE MONEY GUARD (RULINGS #4).
		//
		// The obvious shape — declare the placement WANTED, then transition it to
		// IN_TRANSIT naming the hull — opens a spend path this pass must not have: a
		// failure between the two writes leaves a hull-less WANTED row behind, and a
		// hull-less WANTED row in a MARKET slot is exactly what the buy queue drains
		// by BUYING A PROBE. This pass would then have spent money by failing.
		//
		// So the row is BORN NAMING ITS HULL, in IN_TRANSIT, through the one ledger
		// write whose conflict set carries state and assigned_ship. There is no window
		// in which the placement exists without a hull, and therefore no window in
		// which the drain can see it: the hull starts named by no row and ends named
		// by one.
		//
		// SAFE AS AN INSERT because surgeTargets only ever offers a waypoint carrying
		// NO row at all — the conflict branch of that write re-points an existing row
		// at this hull, which would evict an incumbent, and the target picker is what
		// makes that branch unreachable here.
		//
		// It writes IN_TRANSIT for a hull that has NOT been told to move. The placement
		// machine's dispatchClaim branch is what notices a still hull on an in-flight
		// row and flies it — load-bearing for this path, exactly as it is for the
		// orphan dispatch, and without it the hull would stand where it is forever
		// while the row read as in-flight.
		if uerr := ports.Ledger.UpsertSpareSlot(ctx, playerID, parkedsensing.SlotRecord{
			Waypoint:     target.waypoint,
			System:       target.system,
			Kind:         parkedsensing.SlotKindMarket,
			State:        parkedsensing.SlotStateInTransit,
			AssignedShip: ship,
		}); uerr != nil {
			*failures = append(*failures, fmt.Errorf(
				"failed to surge surplus probe %s into the unpriced system %s: %w", ship, target.system, uerr))
			continue
		}

		// Best-effort behind the row, in the same order and for the same reason as
		// adoption and the orphan dispatch: the probe cap counts ROWS, and the row is
		// written. Named rather than silent, because an untagged hull reads as idle and
		// undedicated to every other coordinator's ownership sweep — long enough to be
		// poached mid-flight.
		if tagErr := ports.Fleet.AssignFleet(ctx, playerID, ship, parkedsensing.SensingParkedFleetTag); tagErr != nil {
			logger.Log("WARNING", fmt.Sprintf(
				"Surged probe %s is now dispatched to %s but keeps its old fleet tag (the probe cap counts it; the placement machine re-tags it on first use): %v",
				ship, target.waypoint, tagErr), map[string]interface{}{
				"action":      "parked_sensing_surge_tag_failed",
				"ship_symbol": ship,
				"waypoint":    target.waypoint,
			})
		}
		holds.hulls[ship] = true
		holds.rows[target.waypoint] = append(holds.rows[target.waypoint], parkedsensing.QueuedSlot{
			Waypoint: target.waypoint, System: target.system,
			Kind: parkedsensing.SlotKindMarket, State: parkedsensing.SlotStateInTransit, AssignedShip: ship,
		})
		dispatched++
	}

	if dispatched > 0 {
		logger.Log("INFO", fmt.Sprintf(
			"Surged %d surplus probe(s) into charted-but-unpriced systems (no purchase — these hulls were paid for and stood in no sensing slot); %d systems in the pool",
			dispatched, len(pool)), map[string]interface{}{
			"action":     "parked_sensing_surged",
			"surged":     dispatched,
			"pool":       len(pool),
			"in_flight":  cfg.SurgeInFlightCap - budget + dispatched,
			"cap":        cfg.SurgeInFlightCap,
			"surplus":    len(surplus),
			"candidates": len(targets),
		})
	}
	return dispatched
}

// inFlightSurge counts the probes already flying toward a system in the pool.
//
// DERIVED, NEVER REMEMBERED (RULINGS #2). A surge dispatch leaves exactly one
// trace — a placement row in an unpriced system with a hull on the way — so the
// standing population is a query over the durable ledger and survives a restart
// with nothing to rebuild. There is deliberately no "surge" marker column: a
// counter this pass maintained itself would be the cross-tick state the ruling
// forbids, and would drift from reality the first time a hull was reaped.
//
// BOUGHT counts alongside IN_TRANSIT, so a probe the buy queue paid for and aimed
// at an unpriced system consumes surge capacity too. That is the fail-closed
// direction on purpose: both hulls are flights the placement machine has to
// advance out of one per-tick budget, and both arrive to scan against one API
// ceiling, so counting only our own would let the two paths saturate the ceiling
// between them while each looked bounded. It can only ever dispatch FEWER probes.
//
// A PARKED row is NOT in flight even in an unpriced system: the hull has arrived
// and the system is unpriced only until its first scan lands. Counting it would
// hold surge capacity hostage to the scan rotation's cadence.
func inFlightSurge(pool []UnpricedSystem, holds ledgerHolds) int {
	unpriced := make(map[string]bool, len(pool))
	for _, system := range pool {
		unpriced[system.System] = true
	}
	inFlight := 0
	for _, rows := range holds.rows {
		for _, row := range rows {
			if row.State != parkedsensing.SlotStateInTransit && row.State != parkedsensing.SlotStateBought {
				continue
			}
			if unpriced[row.System] {
				inFlight++
			}
		}
	}
	return inFlight
}

// surplusProbes selects the hulls this pass may fly: GENUINE surplus, and nothing
// else.
//
// THE PREDICATE THAT PROTECTS A SERVING PROBE is holds.hulls — every hull named by
// ANY sensing_slots row in ANY of the five states (WANTED, QUEUED, BOUGHT,
// IN_TRANSIT, PARKED), which is the whole of the sensing ledger's hull index. A
// probe standing a sensing watch is named by its PARKED row, so it is struck out
// here before anything else looks at it. That is the standing owner rule, and it is
// enforced by the SAME index adoption and the orphan dispatch enforce it with —
// called through the same function — so the three passes cannot drift about which
// hull is spoken for.
//
// The base filter IS the orphan dispatch's, invoked directly rather than copied:
// probe frame, a fleet tag that makes it nobody's (RULINGS #7), not driven by a
// live container (RULINGS #3), not manning a surviving scout post, not named by a
// slot row, and standing somewhere we can read.
//
// ONE GUARD IS ADDED HERE, and it closes a hole holds.hulls structurally cannot
// see: a hull on a CHARTING ERRAND. claimSpares DELETES the spare's placement row
// when it hands the hull to a seed and records the errand in
// sensing_systems.seed_ship instead, so a seed hull is named by NO slot row and is
// idle between steps — indistinguishable from surplus to every check above. The
// errand predicate is expansion's own (SeedShip set, SeedState DISPATCHED or
// CHARTING, mirroring hasActiveSeed), so the two engines cannot disagree about who
// owns a hull mid-tour. DONE is not an errand: a stood-down seed is named by a
// placement row again, which holds.hulls already covers.
func surplusProbes(
	ships []*navigation.Ship,
	manned map[string]bool,
	holds ledgerHolds,
	systems []parkedsensing.ExpandSystem,
) []idleOrphanHull {
	candidates, _ := idleOrphans(ships, manned, holds)
	onErrand := hullsOnChartingErrand(systems)
	if len(onErrand) == 0 {
		return candidates
	}
	surplus := make([]idleOrphanHull, 0, len(candidates))
	for _, candidate := range candidates {
		if onErrand[candidate.ship.ShipSymbol()] {
			continue
		}
		surplus = append(surplus, candidate)
	}
	return surplus
}

// hullsOnChartingErrand names every hull the sensing ledger has out on an ACTIVE
// charting seed. The state test mirrors expansion's hasActiveSeed exactly.
func hullsOnChartingErrand(systems []parkedsensing.ExpandSystem) map[string]bool {
	onErrand := map[string]bool{}
	for _, system := range systems {
		if system.SeedShip == "" {
			continue
		}
		if system.SeedState != parkedsensing.SeedStateDispatched && system.SeedState != parkedsensing.SeedStateCharting {
			continue
		}
		onErrand[system.SeedShip] = true
	}
	return onErrand
}

// surgeTargets reduces the pool to at most ONE candidate placement per system, in
// a fixed value order.
//
// ONE PER SYSTEM, because one probe scanning one market is what takes a system out
// of the pool, and the pool is 1,067 systems wide against ~300 surplus hulls:
// breadth is the whole return, and a second probe in a system already covered buys
// depth nobody asked for at the price of a system left dark.
//
// A WAYPOINT THAT ALREADY CARRIES A ROW IS NEVER OFFERED, and that single check is
// what keeps this pass strictly complementary to the other two. A hull-less WANTED
// row there is the ORPHAN DISPATCH's to fill and the buy queue's to fund; a row in
// any other state is somebody's placement. Declining the waypoint also makes the
// write below a pure insert, which is what makes it safe as one write.
//
// THE ORDER HERE IS NOT THE RANKING — it is only a DETERMINISTIC order, by symbol.
// The ranking is per-hull, because its second key is gate distance and distance is a
// property of the hull asking rather than of the target, so it lives in exactly one
// place: surgeRanksAbove. Sorting by the value keys here as well would give the
// ranking two definitions that could drift, and the one that actually decided
// anything would be whichever ran last.
func surgeTargets(pool []UnpricedSystem, holds ledgerHolds) []surgeTarget {
	targets := make([]surgeTarget, 0, len(pool))
	for _, system := range pool {
		for _, waypoint := range system.MarketWaypoints {
			if holds.occupiedAt(waypoint) {
				continue
			}
			targets = append(targets, surgeTarget{
				system:   system.System,
				waypoint: waypoint,
				markets:  len(system.MarketWaypoints),
				richness: system.Waypoints,
			})
			break
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].system < targets[j].system })
	return targets
}

// surgeRanksAbove is THE ranking, and the only one: does candidate a beat candidate b
// for a hull that is aHops and bHops gate crossings away from each?
//
// It is LEXICOGRAPHIC, and the order of the keys is the whole economic argument:
//
//  1. MARKETPLACE COUNT, descending, and it DOMINATES. It is the only input that
//     scales what a delivered probe is worth PERMANENTLY: the hull prices one
//     waypoint, but a system with several markets is one whose remaining markets the
//     screen can then justify placements for, and whose prices Part 2's relocator
//     scores a whole region on. A hop costs 850-1360s measured (sp-smbgd) — tens of
//     minutes, once — against an era measured in days, so distance must not outrank
//     it. Getting this precedence backwards is not a subtle mis-ranking: it sends
//     every probe to whatever is nearest and the value inputs stop mattering at all.
//  2. GATE HOPS, ascending. The tie-break: among systems worth the same, the nearer
//     one is both the cheaper crossing and the sooner arrival, and the less exposed
//     to the walk being interrupted.
//  3. WAYPOINT COUNT, descending. Richness: more waypoints is more of everything a
//     later screen can find, and it separates otherwise-identical candidates on
//     evidence rather than on the alphabet.
//  4. SYMBOL, ascending. Present so the ranking is TOTAL. Without it a full tie is
//     resolved by whatever order the store happened to return its rows in — which is
//     not part of its contract — so the same pool would rank differently between
//     ticks, and starvation creeps back the moment a group of systems ties.
func surgeRanksAbove(a surgeTarget, aHops int, b surgeTarget, bHops int) bool {
	if a.markets != b.markets {
		return a.markets > b.markets
	}
	if aHops != bHops {
		return aHops < bHops
	}
	if a.richness != b.richness {
		return a.richness > b.richness
	}
	return a.system < b.system
}

// takeSurgeTarget removes and returns the highest-ranked candidate THIS hull can
// reach, consuming it so no two hulls in one tick are sent to the same waypoint.
//
// `hops` MUST be real gate-crossing counts and not an ordering (gateReach.hopsFrom,
// never a position in gateReach.from). Systems the same number of gates away have to
// COMPARE EQUAL, or distance stops being a tie-break and becomes the dominant key:
// with a per-position distance every candidate in one ring is strictly nearer than
// the next, so the alphabet quietly overrides marketplace count and the pass ranks on
// nothing at all. That was the first version of this pick and the ranking test is
// what caught it.
//
// A system absent from `hops` is UNREACHABLE and is skipped rather than treated as
// far away. Overshooting the walk is not a loud failure: the router would name no
// next system, the step would error, and the slot would sit IN_TRANSIT naming a hull
// that never arrives and never stops counting against the probe cap.
//
// WHY CONSUMING MATTERS, stated precisely because the obvious answer is wrong: it
// is NOT what stops two hulls being written onto one placement — the second write
// would land on a row that now exists and the target picker would never have
// offered it on a later tick anyway. What it buys is THROUGHPUT within the tick.
// Read without consuming, every surplus hull in a system is offered the SAME
// highest-ranked target, and the second write would re-point that row at the second
// hull, evicting the first: one system covered per tick where the cap allows eight,
// and a hull left named by nothing.
//
// The candidate list is passed BY POINTER because consuming has to be visible to
// the next hull: a value slice would let every hull re-take the same target.
func takeSurgeTarget(targets *[]surgeTarget, hops map[string]int) (surgeTarget, bool) {
	best, bestHops := -1, 0
	for i, target := range *targets {
		distance, reachable := hops[target.system]
		if !reachable {
			continue
		}
		if best < 0 || surgeRanksAbove(target, distance, (*targets)[best], bestHops) {
			best, bestHops = i, distance
		}
	}
	if best < 0 {
		return surgeTarget{}, false
	}
	remaining := *targets
	target := remaining[best]
	*targets = append(remaining[:best], remaining[best+1:]...)
	return target, true
}

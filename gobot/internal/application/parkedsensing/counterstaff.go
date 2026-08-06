package parkedsensing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
)

// counterstaff.go breaks the deadlock ONE STEP EARLIER THAN foothold.go, and it
// exists because the two deadlocks have different shapes.
//
// FOOTHOLD'S DEADLOCK is "a SPARE want stands at a probe yard in a system we have
// judged but never occupied". It is broken by flying a surplus PROBE there, and
// that probe then FILLS the placement — it becomes the seed. That works, and it is
// the right answer whenever the fleet holds a spare probe to send.
//
// THIS ONE IS COLDER, AND IT IS THE ONE MEASURED IN PROD (3 probes owned, 2 slots
// PARKED, 5,549 slots WANTED, 0 seeds requested):
//
//   - requestSeeds only writes a SPARE want at a yard staffedAt already answers
//     for, because the buy queue only buys where a hull of ours is docked. So with
//     no staffed probe yard in reach, NO SPARE WANT IS EVER WRITTEN — and
//     footholdBroker.fill is only ever offered SPARE/WANTED placements, so it is
//     handed nothing at all. requestSeeds is the only writer of that row in the
//     engine (supply.go); seed stand-down writes PARKED spares, never wants.
//   - surplusPool draws PARKED MARKET rows, which are probes. With no system
//     holding two probes, coveredAfterMove refuses every candidate and the pool is
//     empty besides.
//
// Both routes assume spare PROBES already exist. That is the circularity: you need
// a probe at a probe-selling yard to buy a probe.
//
// THE ESCAPE IS A NON-PROBE HULL, AND IT IS A BUYER RATHER THAN A CARRIER. The
// fleet holds a command frigate and haulers from day one. SpaceTraders sells a hull
// wherever a hull of ours is docked and does not care WHICH, so any of them standing
// at the counter unlocks the purchase.
//
// IT CANNOT GO THROUGH footholdFromSurplus, AND THAT IS STRUCTURAL RATHER THAN A
// PREFERENCE. That path claims the target placement FOR THE CARRIER
// (TransitionSlot → IN_TRANSIT, AssignedShip: hull). A frigate in that row would:
//
//   - be dedicated into the sensing fleet by the placement machine's very next edge
//     (placement.go dispatchClaim → warnIfTagFailed → AssignFleet), which is
//     precisely "stranded": FindIdleLightHaulers skips every hull carrying a
//     dedicated_fleet tag, so contract and gate hauling could never claim it again;
//   - be counted by CountOwnedProbes, which selects on state and assigned_ship and
//     never on role, inflating the probe cap against hulls that are not probes;
//   - FILL the SPARE placement, so the probe that placement exists to buy is never
//     bought at all, and claimSpares would then send the frigate off on a charting
//     errand.
//
// So the borrowed hull is given NO PLACEMENT ROW, NO FLEET TAG AND NO CLAIM. It
// simply stands at the counter, and buyerAt reads it there — the same way the buy
// queue has always been able to buy through a docked probe no ledger row accounts
// for (see DockedProbeAt's contract, and DockedBuyerAt beside it). The probe that
// gets bought is the hull that fills the placement, exactly as on every other path.
//
// WHAT BOUNDS THE BORROW:
//
//   - IN-SYSTEM ONLY. The hull is sent to a counter in the system it is ALREADY IN,
//     one hop, never across a gate. A foothold converts a system and can justify a
//     multi-tick crossing of a hull this engine owns; a hull borrowed from the
//     contract fleet cannot, and yardpresence.go draws the same line for the same
//     reason.
//   - ONE HULL PER TICK, charged against MaxExpansionActions.
//   - NOTHING IS HELD. No row, no tag, no claim, so the hull is never removed from
//     its own coordinator's pool for an instant — it is idle and undedicated the
//     whole time, and whoever wants it takes it. If the purchase never happens the
//     hull is not lost to the errand; it is simply parked somewhere else in the same
//     system.
//   - IT STOPS ON ITS OWN. The moment a probe is parked at that counter the yard is
//     staffed from the ledger, staffedAt answers without any ships read, and this
//     pass never selects that yard again.
//
// RULINGS #4 IS UNTOUCHED. This pass changes the REACHABILITY of a purchase and
// never the permission for one: it holds no purchaser and no treasury, acquisition
// still routes through the buy queue's floor and probe cap, and no second spending
// path is opened. RULINGS #2 — it holds NO cross-tick state; every fact is
// re-derived from the ships table and the tick's own memos.

// maxLendableHullsRead bounds the ships-table page this pass reads.
//
// A BOUND RATHER THAN A FLEET WALK, which is what makes the read admissible on
// ParkedShipReader at all (see its note). Larger than the one dispatch a tick makes,
// because the head of the list is frequently unusable — a hull in transit, or one
// standing in a system with no probe counter — and asking for exactly one would
// usually yield none. Small enough that the read stays a single indexed page.
const maxLendableHullsRead = 16

// staffCounters lends ONE non-probe hull to a probe-selling counter in its own
// system, so the buy queue has somebody to buy through.
//
// IT RUNS ONLY WHEN THE DEADLOCK ACTUALLY BOUND. rep.SeedsUnstaged counts the
// targets requestSeeds passed over for want of a staging yard, so a tick that
// staged everything it wanted to does nothing here and costs not one port call.
// That is also what keeps the pass from competing with the ordinary paths: a fleet
// with a staffed counter, or a spare probe to send, never reaches it.
func (t *expandTick) staffCounters(ctx context.Context) error {
	if t.rep.SeedsUnstaged == 0 || t.rep.Actions >= MaxExpansionActions {
		return nil
	}

	hulls, err := t.p.Ships.LendableHulls(ctx, t.playerID, maxLendableHullsRead)
	if err != nil {
		// PROPAGATED, never read as "nothing to lend". This pass is the fleet's only
		// way out of the cold deadlock, and a read fault silently reported as an
		// empty fleet is indistinguishable from one — the tick is idempotent and
		// re-derived, so failing loudly costs a cycle and nothing else.
		return fmt.Errorf("failed to list the hulls available to staff a probe counter: %w", err)
	}
	if len(hulls) == 0 {
		return nil
	}

	// THE IDEMPOTENCE KEY. A hull already FLYING to a counter is in this list and
	// flagged, and the yard it is bound for is struck out below — which is what stops
	// the next tick sending a second hull to the same counter while the first is
	// still under way. It is derived from the ships table rather than remembered, so
	// it survives a restart with nothing to rebuild.
	inbound := make(map[string]bool, len(hulls))
	for _, hull := range hulls {
		if hull.InTransit && hull.Waypoint != "" {
			inbound[hull.Waypoint] = true
		}
	}

	// EVIDENCE FIRST, the same two passes and the same admission rule stagingYardFor
	// applies (probeStock.acceptsStaging): a counter we have PRICED and found selling
	// probes before any trait guess, a never-priced one second, and a counter known to
	// sell no probe by neither. Reading the same rule is what keeps this pass from
	// staffing a yard staging would then refuse to use.
	for _, wantEvidence := range []bool{true, false} {
		for _, hull := range hulls {
			if hull.InTransit || hull.System == "" || hull.Waypoint == "" {
				continue
			}
			serves, err := t.originServesATarget(ctx, hull.System)
			if err != nil {
				return err
			}
			if !serves {
				continue
			}
			yard, err := t.unstaffedCounterIn(ctx, hull.System, hull.Waypoint, inbound, wantEvidence)
			if err != nil {
				return err
			}
			if yard == "" {
				continue
			}
			t.sendToCounter(ctx, hull, yard)
			return nil
		}
	}
	return nil
}

// unstaffedCounterIn picks the probe-selling counter in system that a borrowed hull
// should be sent to, or "" when the system offers none worth the flight.
//
// A YARD ALREADY CARRYING A SPARE WANT IS DELIBERATELY ELIGIBLE, unlike in
// stagingYardFor, and the difference is the point of each test. Staging looks for
// somewhere to WRITE a want, so a yard that already has one is taken; this looks for
// a counter to STAFF, and a yard holding a want nothing can fund is the most
// valuable one there is — it is exactly the placement footholdFromSurplus exists to
// fill and cannot, cold. Staffing it makes that standing want fundable on the next
// drain.
//
// A STAFFED YARD IS SKIPPED, which is what makes the pass self-terminating: the
// moment a probe parks on the counter the ledger answers staffedAt directly and this
// yard is never selected again.
//
// SO IS THE YARD THE HULL IS ALREADY STANDING ON, and that pair of tests is not
// redundant. Standing on it while staffedAt says NO means the hull is in ORBIT above
// the counter rather than berthed at it — and the only fix for that is a dock
// command this pass deliberately does not hold (its whole port surface is one
// in-system hop). Flying a hull to the waypoint it is already at buys nothing and
// would be re-issued on every tick forever, so it is left to whichever coordinator
// owns the hull to berth it, and another counter is considered instead.
func (t *expandTick) unstaffedCounterIn(ctx context.Context, system, standingOn string, inbound map[string]bool, wantEvidence bool) (string, error) {
	yards, err := t.probeYardsIn(ctx, system)
	if err != nil {
		return "", err
	}
	for _, yard := range yards {
		if inbound[yard] || yard == standingOn {
			continue
		}
		stock, err := t.stagedProbeStock(ctx, yard)
		if err != nil {
			return "", err
		}
		if !stock.acceptsStaging(wantEvidence) {
			continue
		}
		manned, err := t.staffedAt(ctx, yard)
		if err != nil {
			return "", err
		}
		if manned {
			continue
		}
		return yard, nil
	}
	return "", nil
}

// originServesATarget reports whether staging would ever CHOOSE a yard in system —
// the feasibility test that keeps a borrowed hull from being flown somewhere its
// presence could not be used.
//
// TWO CONDITIONS, BOTH stagingYardFor's OWN. The system must be one of the origins
// staging walks (reach.origins(), the tick's neighbour map — a system carrying no
// ledger row is never offered as a staging origin however many counters it holds),
// and at least one target still needing a seed must be ROUTABLE from it. Asking the
// tick's shared gateReach rather than a second walk is what stops this pass and
// staging disagreeing about where a seed can be bought.
//
// Memoised for the TICK: several lendable hulls share a system, and neither the
// origin set nor the reach can change while the tick runs.
func (t *expandTick) originServesATarget(ctx context.Context, system string) (bool, error) {
	if known, cached := t.serving[system]; cached {
		return known, nil
	}
	serves := false
	if t.stagingOrigin(system) {
		for _, target := range t.targets {
			if t.covered[target.System] {
				continue
			}
			within, err := t.reach.canReach(ctx, system, target.System)
			if err != nil {
				return false, err
			}
			if within {
				serves = true
				break
			}
		}
	}
	t.serving[system] = serves
	return serves, nil
}

// stagingOrigin reports whether system is one of the origins seed staging walks,
// building the index on first use because most ticks never ask.
func (t *expandTick) stagingOrigin(system string) bool {
	if t.origins == nil {
		origins := t.reach.origins()
		t.origins = make(map[string]bool, len(origins))
		for _, origin := range origins {
			t.origins[origin] = true
		}
	}
	return t.origins[system]
}

// sendToCounter issues the one in-system hop that puts a borrowed hull on a counter.
//
// A REFUSED HOP IS NOT AN ERROR. Nothing has been written and nothing is owed — the
// hull is exactly where it was, the tick is re-derived from scratch, and the next one
// retries for free. Failing the tick here would take the off-gate pass down with it
// over a cooldown or a fuel state.
//
// NAMED IN THE LOG EITHER WAY. This is the one pass that moves a hull belonging to
// another coordinator's fleet, so an operator asking why a hauler left its market
// must be able to find the answer without reading the ledger.
func (t *expandTick) sendToCounter(ctx context.Context, hull LendableHull, yard string) {
	if err := t.p.SeedShip.NavigateTo(ctx, t.playerID, hull.ShipSymbol, yard); err != nil {
		logging.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Could not send %s to the probe counter at %s to unblock seed buying; nothing was written and the next tick retries: %v",
			hull.ShipSymbol, yard, err), map[string]interface{}{
			"action":        "parked_sensing_counter_staff_refused",
			"ship_symbol":   hull.ShipSymbol,
			"from_waypoint": hull.Waypoint,
			"waypoint":      yard,
			"system_symbol": hull.System,
		})
		return
	}
	t.rep.CountersStaffed++
	t.rep.Actions++
	logging.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Lent %s to the probe counter at %s so a charting seed can be bought there (no placement, no fleet tag, no claim — it stays free to its own coordinator)",
		hull.ShipSymbol, yard), map[string]interface{}{
		"action":        "parked_sensing_counter_staff_dispatch",
		"ship_symbol":   hull.ShipSymbol,
		"from_waypoint": hull.Waypoint,
		"waypoint":      yard,
		"system_symbol": hull.System,
	})
}

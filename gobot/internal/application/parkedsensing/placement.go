package parkedsensing

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// placement.go flies the probes the buy queue paid for to the waypoints they
// were bought for, and stands them down when they arrive.
//
// The machine is driven entirely by two durable facts — the slot's recorded
// state and the hull's row in the ships table — and holds nothing in memory
// between ticks. A daemon restart mid-flight therefore resumes exactly where it
// left off: the ledger still says IN_TRANSIT, the ships table still says where
// the hull is, and the next tick reads both and carries on.
//
// Every edge is advanced only AFTER the action behind it succeeded, so a failed
// command leaves the slot in the state it was already in and the next tick
// retries it. Nothing here can strand a hull in a state that has no way out.

// DefaultMaxPlacementActions bounds how many placement transitions one tick may
// perform. A plain constant, deliberately not a knob: it exists to keep a large
// backlog from firing a burst of navigation commands in a single tick, not to
// express any economic preference, and nothing downstream benefits from tuning
// it. The backlog is not lost — it is simply worked over more ticks.
const DefaultMaxPlacementActions = 10

// MaxWalkRings is how far the FOOTHOLD path may draw an already-parked scanning
// hull off a working market to fill a placement elsewhere.
//
// IT IS NO LONGER THE WALK'S REACH, and that correction matters. This constant
// was originally the bound nextHopToward resolved under, so it genuinely was "how
// far a hull may be sent" — but the adapter's resolver now reads
// MaxSeedFlightHops instead (`const maxWalkRings = appSensing.MaxSeedFlightHops`
// in adapters/parkedsensing), because a charting seed had to be flyable further
// than a staging walk. What is left here is a SELECTION bound: how far this
// engine chooses to reach when picking a source, not how far the router can
// deliver.
//
// The distinction is easy to lose and expensive to lose. A destination beyond the
// ROUTER's bound is not refused loudly — nextHopToward names no next system, the
// step returns an error, the slot stays IN_TRANSIT still naming the hull, and the
// hull goes on counting against the probe cap while never arriving. So a
// selection bound may sit at or below the router's, never above it; anything that
// wants to reach further must read the router's own number, as maxFerryHops does.
//
// WHY TWO, FOR THE FOOTHOLD. Its cost is not ticks but COVERAGE: the hull it
// takes was watching a market, and that market stops being watched until a
// replacement is bought. Drawing one from the far side of the map to serve a
// placement is a poor trade even when the router could carry it, so this bound
// stays deliberately short while the ferry — which buys a NEW hull and takes
// nothing away — reads the router's.
//
// The bound is also what keeps resolution cheap: each ring costs one stored read
// per system on the frontier, so a step costs one read plus the current system's
// fanout — a handful — rather than something that grows with everything the fleet
// has ever charted. A destination further out is not refused forever; the
// frontier advances by CONVERTING systems, and each conversion brings the next
// ones inside this reach.
const MaxWalkRings = 2

// ShipMover issues the movement commands. The two navigation verbs are
// genuinely different machinery, not one with a flag: the in-system planner
// resolves a route inside a single system's waypoint graph, while the
// cross-system walk crosses a gate. Sending an in-system hop through the walk,
// or a cross-gate hop through the planner, fails.
type ShipMover interface {
	// NavigateWithin moves a hull to a waypoint in the system it is already in.
	NavigateWithin(ctx context.Context, playerID int, shipSymbol, destination string) error
	// RouteAcross advances a hull ONE STEP of the gate walk toward destination
	// and returns — it does not fly the journey. A crossing is a sequence of
	// steps (onto the gate, then off it, once per gate on the way), and this
	// verb performs exactly the one step the hull's current position calls for.
	//
	// fromWaypoint is where the ships table says the hull is STANDING, and the
	// caller passes it because it has already read it this tick. It is the
	// step discriminator, and it is a SYMBOL rather than a distance on purpose:
	// orbitals share coordinates with the body they orbit, so a hull can read
	// zero distance from a gate it is not standing on.
	//
	// The walk therefore needs no progress column of its own. The durable pair
	// this machine already keeps — the slot row naming the destination and the
	// hull, and the ships table naming where that hull now is — is a complete
	// description of how far the crossing has got, and the next tick resumes
	// from it by re-reading both. See dispatchClaim, which is what re-issues
	// the next step once the slot is IN_TRANSIT.
	RouteAcross(ctx context.Context, playerID int, shipSymbol, fromWaypoint, destination string) error
	// Dock docks a hull where it currently sits.
	Dock(ctx context.Context, playerID int, shipSymbol string) error
}

// PlacementLedger is the placement machine's slice of the ledger: read the
// in-flight placements, advance one. It is narrower than the buy queue's
// BuyLedger on purpose — the machine spends nothing, so it is given no access
// to the probe count or the system verdicts that gate spending.
type PlacementLedger interface {
	SlotsByState(ctx context.Context, playerID int, states ...string) ([]QueuedSlot, error)
	TransitionSlot(ctx context.Context, playerID int, waypoint, kind, fromState, toState string, set SlotFields) error
}

// PlacementPorts is everything AdvancePlacements needs from the outside world.
type PlacementPorts struct {
	Ledger PlacementLedger
	Ships  ParkedShipReader
	Mover  ShipMover
	Fleet  FleetTagger
}

// PlacementReport is one placement tick's outcome, for the heartbeat.
type PlacementReport struct {
	// Dispatched counts hulls sent flying toward their slot.
	Dispatched int
	// Docking counts dock commands issued to hulls that arrived in orbit.
	Docking int
	// Parked counts hulls confirmed on station and now scanning.
	Parked int
	// Actions counts everything above against the per-tick budget.
	Actions int
}

// AdvancePlacements moves every in-flight placement one step closer to a probe
// standing on station, up to maxActions steps per tick. A non-positive
// maxActions falls back to DefaultMaxPlacementActions.
//
// One slot gets at most ONE action per tick. The worklist is read once at the
// top, so a hull dispatched by this tick is not also examined for arrival by
// it — the next tick does that, by which time the ships table has caught up.
// That is what keeps the machine from issuing a dock command against a position
// its own navigate call just invalidated.
func AdvancePlacements(ctx context.Context, pl PlacementPorts, playerID int, maxActions int) (PlacementReport, error) {
	var rep PlacementReport
	if maxActions <= 0 {
		maxActions = DefaultMaxPlacementActions
	}

	slots, err := pl.Ledger.SlotsByState(ctx, playerID, SlotStateBought, SlotStateInTransit)
	if err != nil {
		return rep, fmt.Errorf("failed to list in-flight sensing placements: %w", err)
	}

	for _, slot := range slots {
		if rep.Actions >= maxActions {
			break
		}
		if slot.AssignedShip == "" {
			// A BOUGHT slot with no hull recorded is a torn write, not a
			// placement: there is nothing to fly. The buy queue owns repairing
			// it; commanding a ship whose symbol we do not have is impossible.
			continue
		}

		pos, err := pl.Ships.ShipAt(ctx, playerID, slot.AssignedShip)
		if err != nil || !pos.Found {
			// Never command a hull we cannot locate. Both an unreadable row and
			// an absent one leave the slot exactly as it is, to be retried once
			// the ships table can answer.
			continue
		}

		acted, err := advanceOne(ctx, pl, playerID, slot, pos, &rep)
		if err != nil {
			return rep, err
		}
		if acted {
			rep.Actions++
		}
	}
	return rep, nil
}

// advanceOne applies the single transition available to one placement, and
// reports whether it consumed an action.
func advanceOne(ctx context.Context, pl PlacementPorts, playerID int, slot QueuedSlot, pos ShipPos, rep *PlacementReport) (bool, error) {
	switch slot.State {
	case SlotStateBought:
		return dispatch(ctx, pl, playerID, slot, pos, rep)
	case SlotStateInTransit:
		return standDown(ctx, pl, playerID, slot, pos, rep)
	default:
		return false, nil
	}
}

// dispatch sends a freshly-bought hull toward its slot.
func dispatch(ctx context.Context, pl PlacementPorts, playerID int, slot QueuedSlot, pos ShipPos, rep *PlacementReport) (bool, error) {
	// Re-assert the fleet tag before anything else. The purchase already wrote
	// it, but that write is best-effort — it happens after the money has moved,
	// where failing the whole purchase would be worse than an untagged hull. So
	// this is where an untagged hull gets repaired, and because the write is
	// idempotent the overwhelmingly common case (already tagged) costs nothing.
	//
	// A failure is not fatal — the hull still flies — but it is named: an
	// untagged probe reads as idle and undedicated to every other coordinator's
	// ownership sweep, so a repeatedly failing tag is how a sensing hull quietly
	// ends up somebody else's.
	warnIfTagFailed(ctx, pl, playerID, slot.AssignedShip, slot.Waypoint, "parked_sensing_dispatch_tag_failed")

	if pos.NavStatus == navigation.NavStatusInTransit {
		// Already flying — under this or a previous tick's command. Issuing
		// another move now would be refused anyway.
		return false, nil
	}

	if pos.Waypoint == slot.Waypoint {
		// Bought at the very waypoint it was wanted at (a yard that is also the
		// slot). No movement to perform: hand it to the arrival branch, which
		// docks and parks it on the next tick.
		if err := transitionInFlight(ctx, pl, playerID, slot, SlotStateBought, SlotStateInTransit); err != nil {
			return false, err
		}
		rep.Dispatched++
		return true, nil
	}

	if moveErr := flyToSlot(ctx, pl, playerID, slot, pos); moveErr != nil {
		// The hull did not leave. Holding the slot at BOUGHT is what makes the
		// retry free: the next tick re-reads the position and re-issues, with no
		// stuck state to unwind.
		return true, nil
	}

	if err := transitionInFlight(ctx, pl, playerID, slot, SlotStateBought, SlotStateInTransit); err != nil {
		return false, err
	}
	rep.Dispatched++
	return true, nil
}

// standDown docks an arrived hull and, once the ships table confirms it is
// docked, marks the placement PARKED.
//
// PARKED is recorded only on the CONFIRMED docked reading, never optimistically
// when the dock command returns. A slot marked PARKED is a slot the rest of the
// model believes is scanning, and the probe-presence check that decides whether
// to buy another hull for a waypoint reads exactly that — so a placement parked
// on a hull that never actually docked would suppress a purchase it should have
// allowed, and leave the waypoint unwatched indefinitely.
func standDown(ctx context.Context, pl PlacementPorts, playerID int, slot QueuedSlot, pos ShipPos, rep *PlacementReport) (bool, error) {
	if pos.Waypoint != slot.Waypoint {
		if pos.NavStatus == navigation.NavStatusInTransit {
			return false, nil // genuinely flying; leave it alone
		}
		// Sitting still, somewhere that is not the slot. This placement reached
		// IN_TRANSIT without anything ever having been told to move, so nothing
		// downstream will move it either — see dispatchClaim.
		return dispatchClaim(ctx, pl, playerID, slot, pos, rep)
	}

	switch pos.NavStatus {
	case navigation.NavStatusInOrbit:
		// Arrived but not berthed. Stay IN_TRANSIT: the next tick reads the
		// docked row and parks it.
		if err := pl.Mover.Dock(ctx, playerID, slot.AssignedShip); err != nil {
			return true, nil // retried next tick
		}
		rep.Docking++
		return true, nil

	case navigation.NavStatusDocked:
		if err := transitionInFlight(ctx, pl, playerID, slot, SlotStateInTransit, SlotStateParked); err != nil {
			return false, err
		}
		rep.Parked++
		return true, nil

	default:
		return false, nil
	}
}

// dispatchClaim flies a hull whose placement was claimed straight into
// IN_TRANSIT without any movement ever being issued.
//
// Not every IN_TRANSIT row is the product of a purchase. Two claim paths write
// the state directly, because they start from a hull we ALREADY own and so have
// nothing to buy: this package's own spare re-tasking, and the seed tour-end
// claims made by the coordinator above it. Both were written on the assumption
// that the placement machine would take the hull from there — but the arrival
// branch only ever docked a hull that had already reached its slot, so a hull
// claimed for a waypoint it was not already standing on simply never left.
// The slot read as in-flight forever, and the hull kept counting against the
// probe cap while doing nothing.
//
// The discriminator is the nav row, not the position: a hull that is genuinely
// moving is left alone (piling a second move onto a ship in flight is exactly
// what the once-per-slot-per-tick discipline exists to prevent), while a hull
// sitting still somewhere that is not its slot has demonstrably never been
// dispatched.
//
// The slot is ALREADY IN_TRANSIT, so there is no state to advance here — only a
// command to issue. A failure leaves it IN_TRANSIT and the next tick re-issues,
// which is the same free retry the purchase path gets.
func dispatchClaim(ctx context.Context, pl PlacementPorts, playerID int, slot QueuedSlot, pos ShipPos, rep *PlacementReport) (bool, error) {
	// Claims that bypass the purchase path also bypass the tag write that
	// happens there, so this is the first and only place such a hull is tagged.
	// Idempotent, so it costs nothing for the hulls that already carry it.
	//
	// This is the ONLY place a claimed hull is tagged, so a silent failure here
	// leaves it untagged for its whole flight — long enough for another
	// coordinator's ownership sweep to claim it.
	warnIfTagFailed(ctx, pl, playerID, slot.AssignedShip, slot.Waypoint, "parked_sensing_claim_tag_failed")

	if err := flyToSlot(ctx, pl, playerID, slot, pos); err != nil {
		return true, nil // retried next tick
	}
	rep.Dispatched++
	return true, nil
}

// warnIfTagFailed asserts the sensing fleet tag and reports a failure without
// changing control flow. The tag is always best-effort here — every caller has
// already done the thing that matters (recorded a hull, or is about to fly one)
// and a missing tag is repaired on the next edge — but it is never silent,
// because an untagged hull is a poachable one.
func warnIfTagFailed(ctx context.Context, pl PlacementPorts, playerID int, shipSymbol, waypoint, action string) {
	if err := pl.Fleet.AssignFleet(ctx, playerID, shipSymbol, SensingParkedFleetTag); err != nil {
		// The destination rides along with the hull, matching the buy queue's
		// own tag warning: an untagged probe is found by looking at where it was
		// being sent, and a warning naming only the hull leaves the reader
		// grepping the ledger for the placement it belongs to.
		logging.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Sensing probe %s bound for %s was not tagged into the sensing fleet (it reads as idle and undedicated to other coordinators until this succeeds): %v",
			shipSymbol, waypoint, err), map[string]interface{}{
			"action":      action,
			"ship_symbol": shipSymbol,
			"waypoint":    waypoint,
		})
	}
}

// flyToSlot issues the movement that takes a hull to its slot, choosing the
// in-system planner or the cross-system gate walk by comparing the hull's
// current system against the slot's. The two are separate machinery, not one
// verb with a flag, and sending a hop through the wrong one fails.
//
// Neither branch waits. The in-system verb dispatches one hop; the cross-system
// one advances the walk by a single step. So a hull several gates from its slot
// costs this tick exactly one dispatch, and the ticks that follow carry it the
// rest of the way — each of them re-reading pos and re-deciding from scratch,
// which is also what makes the walk self-correcting when a hull ends up
// somewhere the last tick did not send it.
func flyToSlot(ctx context.Context, pl PlacementPorts, playerID int, slot QueuedSlot, pos ShipPos) error {
	if shared.ExtractSystemSymbol(pos.Waypoint) == slot.System {
		return pl.Mover.NavigateWithin(ctx, playerID, slot.AssignedShip, slot.Waypoint)
	}
	return pl.Mover.RouteAcross(ctx, playerID, slot.AssignedShip, pos.Waypoint, slot.Waypoint)
}

// transitionInFlight advances a placement, treating a lost race as a normal
// outcome. These edges move no money and buy nothing — if another writer got
// there first, the placement is already where this tick wanted to put it.
//
// It takes the SLOT rather than a bare waypoint because a waypoint no longer
// identifies a placement on its own (sp-dpfp8): a yard can hold a MARKET row and
// a SPARE row at once, and this hull is flying to exactly one of them.
func transitionInFlight(ctx context.Context, pl PlacementPorts, playerID int, slot QueuedSlot, from, to string) error {
	err := pl.Ledger.TransitionSlot(ctx, playerID, slot.Waypoint, slot.Kind, from, to, SlotFields{})
	if err == nil || errors.Is(err, ErrSlotClaimed) {
		return nil
	}
	return fmt.Errorf("failed to advance sensing placement %s (%s) from %s to %s: %w", slot.Waypoint, slot.Kind, from, to, err)
}

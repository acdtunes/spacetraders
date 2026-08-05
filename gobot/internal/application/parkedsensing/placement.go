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
// were bought for, and stands them down when they arrive. It keeps nothing in
// memory between ticks — the slot's recorded state and the hull's ships row drive
// everything — and advances an edge only AFTER the action behind it succeeded, so
// a restart or a refused command resumes from the state already recorded.

// DefaultMaxPlacementActions bounds how many placement transitions one tick may
// perform, so a large backlog cannot fire a burst of navigation commands at once;
// the rest is worked over later ticks. It bounds ACCEPTED commands only — refusals
// have their own budget (placementFailureBudgetMultiple), or slots whose move
// fails every tick spend this one before any healthy slot behind them is reached.
const DefaultMaxPlacementActions = 10

// placementFailureBudgetMultiple sizes the SEPARATE budget a tick gives to REFUSED
// moves, as a multiple of the accepted-command budget. Refusals need a bound of
// their own because the walk's second step issues a jump the API can reject; they
// can afford a loose one because the dominant refusal never reaches the API
// (RouteAcross resolves the next system BEFORE moving anything), and a looser one
// walks a failing backlog in fewer ticks. It stays a small multiple so the worst
// case — every refusal a rejected jump — remains bounded API waste per tick.
const placementFailureBudgetMultiple = 3

// placementCrossingReserve is the share of a tick's accepted-command budget held
// back for hulls still travelling, when arrival work would otherwise take it all.
//
// Arrivals are served first because an arrival CONVERTS a hull into coverage and
// removes the slot from the worklist, while a gate hop only moves it one ring
// closer. The least-recently-attempted rotation cannot protect the losing class
// from that preference — rotation orders slots WITHIN the worklist, a class
// preference re-orders ACROSS it — so a hull that keeps re-qualifying as an arrival
// (a dock command that succeeds while the ships row never flips) would otherwise
// hold the head of every tick. Held-back arrivals are DEFERRED behind the
// crossings, never dropped: see arrivalsFirst.
const placementCrossingReserve = 2

// MaxWalkRings is how far the FOOTHOLD path may draw an already-parked scanning
// hull off a working market to fill a placement elsewhere.
//
// IT IS A SELECTION BOUND, NOT THE WALK'S REACH, and it may sit at or below the
// router's bound but never above it: a destination past the ROUTER's bound is not
// refused loudly — nextHopToward names no next system, the step returns an error,
// and the slot stays IN_TRANSIT still naming a hull that counts against the probe
// cap while never arriving.
//
// It is short because the foothold's cost is not ticks but COVERAGE: the hull it
// takes leaves a market unwatched until a replacement is bought. The ferry, which
// buys a NEW hull and takes nothing away, reads the router's SeedFlightUnbounded
// instead. Nothing is refused permanently — converting a system brings the next
// ring inside this reach.
const MaxWalkRings = 2

// ShipMover issues the movement commands. The two navigation verbs are
// genuinely different machinery, not one with a flag: sending an in-system hop
// through the walk, or a cross-gate hop through the planner, fails.
type ShipMover interface {
	// NavigateWithin moves a hull to a waypoint in the system it is already in.
	NavigateWithin(ctx context.Context, playerID int, shipSymbol, destination string) error
	// RouteAcross advances a hull ONE STEP of the gate walk toward destination
	// and returns — it does not fly the journey. A crossing is a sequence of
	// steps (onto the gate, then off it, once per gate on the way), and this
	// verb performs exactly the one step the hull's current position calls for.
	//
	// fromWaypoint is where the ships table says the hull is STANDING, and it is
	// the step discriminator. It is a SYMBOL rather than a distance on purpose:
	// orbitals share coordinates with the body they orbit, so a hull can read
	// zero distance from a gate it is not standing on.
	//
	// The walk therefore needs no progress column of its own: the slot row and
	// the ships row together describe how far the crossing has got, and the next
	// tick resumes by re-reading both. See dispatchClaim, which re-issues the
	// next step once the slot is IN_TRANSIT.
	RouteAcross(ctx context.Context, playerID int, shipSymbol, fromWaypoint, destination string) error
	// Dock docks a hull where it currently sits.
	Dock(ctx context.Context, playerID int, shipSymbol string) error
}

// PlacementLedger is the placement machine's slice of the ledger: read the
// in-flight placements, advance one, record that one was tried. It is narrower
// than the buy queue's BuyLedger on purpose — the machine spends nothing, so it
// is given no access to the probe count or the system verdicts that gate spending.
type PlacementLedger interface {
	// PlacementWorklist returns the in-flight placements in the given states,
	// LEAST RECENTLY ATTEMPTED FIRST, with never-attempted slots ahead of every
	// attempted one and a total tie-break behind that. The order is contract, and
	// is why this is a separate read from SlotsByState, whose other callers want a
	// stable alphabetical list: a tick-stable order over a queue that does not
	// drain gives the list a permanent head, and a fixed budget then means
	// everything behind that head is never examined at all.
	PlacementWorklist(ctx context.Context, playerID int, states ...string) ([]QueuedSlot, error)
	TransitionSlot(ctx context.Context, playerID int, t SlotTransition, set SlotFields) error
	// MarkPlacementAttempt records that a slot consumed one of a tick's budgets,
	// which is what moves it to the back of the worklist above. It writes ONLY the
	// attempt stamp: state and assigned hull are what the probe cap counts
	// (CountOwnedProbes), and a fairness stamp that could drop a slot out of that
	// count would let the engine buy a hull it already owns.
	MarkPlacementAttempt(ctx context.Context, playerID int, waypoint, kind string) error
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
	// Actions counts everything above against the per-tick budget, SUCCESSES ONLY.
	// The stall detector reads Actions > 0 as "a placement advanced" (anyEffect in
	// probe_sensing_stall.go), so a refusal counted here would file a tick that
	// accomplished nothing as PROGRESS and hide a frozen worklist.
	Actions int
	// Failures counts moves that were issued and REFUSED, against their own
	// separate budget. A refusal leaves the slot exactly as it was, to be retried
	// next tick; it is counted because it is not free, and kept apart from Actions
	// because charging the two alike starves the worklist.
	Failures int
}

// placementOutcome is what one slot's turn cost the tick. outcomeAdvanced and
// outcomeFailed both leave the slot in the state it was already in, so the state
// machine cannot tell them apart — but one bought a step toward a probe standing
// on station and the other bought nothing, and one budget for both lets a
// permanently-failing head freeze everything behind it.
type placementOutcome int

const (
	// outcomeIdle: nothing was commanded and nothing is owed. A hull genuinely in
	// flight, or a slot in a state this machine does not drive.
	outcomeIdle placementOutcome = iota
	// outcomeAdvanced: a command was ACCEPTED, or a state edge moved. This is what
	// DefaultMaxPlacementActions bounds.
	outcomeAdvanced
	// outcomeFailed: a command was issued and REFUSED.
	outcomeFailed
)

// AdvancePlacements moves every in-flight placement one step closer to a probe
// standing on station, up to maxActions steps per tick. A non-positive
// maxActions falls back to DefaultMaxPlacementActions.
//
// One slot gets at most ONE action per tick. The worklist is read once at the
// top, so a hull dispatched by this tick is not also examined for arrival by
// it — the next tick does that, by which time the ships table has caught up.
// That is what keeps the machine from issuing a dock command against a position
// its own navigate call just invalidated.
//
// Two budgets AND a rotating order, and neither is sufficient alone: a fixed
// order hands the same head the same turn every tick however the budgets are
// split, and one shared budget lets a wall of refusals spend it however the order
// rotates.
func AdvancePlacements(ctx context.Context, pl PlacementPorts, playerID int, maxActions int) (PlacementReport, error) {
	var rep PlacementReport
	if maxActions <= 0 {
		maxActions = DefaultMaxPlacementActions
	}

	slots, err := pl.Ledger.PlacementWorklist(ctx, playerID, SlotStateBought, SlotStateInTransit)
	if err != nil {
		return rep, fmt.Errorf("failed to list in-flight sensing placements: %w", err)
	}

	maxFailures := maxActions * placementFailureBudgetMultiple

	t := &placementTick{pl: pl, playerID: playerID, rep: &rep}
	for _, w := range t.arrivalsFirst(ctx, slots, maxActions) {
		if rep.Actions >= maxActions {
			// The accepted-command budget is spent. Walking further can only issue
			// commands there is no budget left to keep.
			break
		}
		if rep.Failures >= maxFailures {
			// The toll is spent. Slots not reached this tick carry the OLDEST attempt
			// stamps now, so the next tick's worklist begins with them.
			break
		}
		slot := w.slot

		outcome, err := t.advanceOne(ctx, slot, w.pos)
		if err != nil {
			return rep, err
		}
		switch outcome {
		case outcomeAdvanced:
			rep.Actions++
		case outcomeFailed:
			rep.Failures++
		}
		if outcome != outcomeIdle {
			// Charged for a turn whichever way it went, so it rotates to the BACK of
			// the next tick's worklist. Stamping only successes would leave the
			// failing slots permanently oldest and hand them the head forever.
			t.markAttempt(ctx, slot)
		}
	}
	return rep, nil
}

// placementWork is one slot paired with the hull position already read for it, so
// the position is read ONCE per slot per tick. Safe because one slot gets one
// action per tick, so nothing this tick can move a hull between the two reads.
type placementWork struct {
	slot QueuedSlot
	pos  ShipPos
}

// arrivalsFirst reorders one tick's worklist so hulls that have ARRIVED are served
// before hulls still travelling, and reads each hull's position once on the way.
// An arrival takes its slot out of the worklist for good while a gate hop leaves it
// competing next tick, so this shortens the queue while spending the SAME budget in
// a better order; the crossing class keeps a floor regardless
// (placementCrossingReserve).
//
// Order within each class is left exactly as the ledger returned it — least
// recently attempted first, the rotation that keeps any one slot from holding its
// class's head across ticks. This function only groups; it never sorts.
//
// Slots that cannot be acted on at all are dropped here rather than costing a turn
// later: a slot with no recorded hull has nothing to fly (the buy queue owns
// repairing it), and a hull the ships table cannot locate must never be commanded.
// Both are left as they are, to be retried once the ledger can answer. Costs one
// indexed position read per in-flight slot per tick, against the database and never
// the API (see ParkedShipReader).
func (t *placementTick) arrivalsFirst(ctx context.Context, slots []QueuedSlot, maxActions int) []placementWork {
	arrived := make([]placementWork, 0, len(slots))
	travelling := make([]placementWork, 0, len(slots))

	for _, slot := range slots {
		if slot.AssignedShip == "" {
			continue
		}
		pos, err := t.pl.Ships.ShipAt(ctx, t.playerID, slot.AssignedShip)
		if err != nil || !pos.Found {
			continue
		}
		w := placementWork{slot: slot, pos: pos}
		if hasArrived(slot, pos) {
			arrived = append(arrived, w)
			continue
		}
		travelling = append(travelling, w)
	}

	// The reserve needs no "is anything crossing?" test of its own: the arrivals it
	// holds back are re-appended below, so with nothing to cross they flow back into
	// this same tick and the budget is spent in full. A guard here would be dead
	// logic.
	arrivalCap := len(arrived)
	if maxActions-placementCrossingReserve < arrivalCap {
		arrivalCap = maxActions - placementCrossingReserve
	}
	if arrivalCap < 0 {
		// A budget smaller than the reserve: hand the whole tick to the crossings
		// rather than let a negative bound drop the arrivals silently.
		arrivalCap = 0
	}

	out := make([]placementWork, 0, len(arrived)+len(travelling))
	out = append(out, arrived[:arrivalCap]...)
	out = append(out, travelling...)
	// Deferred, not dropped: if the crossings turn out idle or refused, the budget
	// returns to the held-back arrivals in this same tick rather than going unspent.
	out = append(out, arrived[arrivalCap:]...)
	return out
}

// hasArrived reports whether a slot's hull is standing ON its destination in a
// state standDown can turn into an accepted command — in orbit (one dock away) or
// docked (one ledger edge away from PARKED). Anything else is travelling.
//
// It must stay in lockstep with standDown's own branches: a slot counted as arrived
// here that standDown then treats as idle would be promoted ahead of real work and
// buy nothing.
func hasArrived(slot QueuedSlot, pos ShipPos) bool {
	if slot.State != SlotStateInTransit || pos.Waypoint != slot.Waypoint {
		return false
	}
	return pos.NavStatus == navigation.NavStatusInOrbit || pos.NavStatus == navigation.NavStatusDocked
}

// markAttempt records that a slot consumed one of this tick's budgets, warning
// rather than failing when it cannot. The stamp is a FAIRNESS HINT, not a state
// transition: it moves no money and leaves the state machine untouched, so a failed
// write must not fail a tick over a slot that was genuinely advanced. An unstamped
// slot sorts as never-attempted, so a lost write costs only another early turn — but
// it is never silent, because stamps failing wholesale is starvation and its symptom
// is a head that never rotates.
func (t *placementTick) markAttempt(ctx context.Context, slot QueuedSlot) {
	if err := t.pl.Ledger.MarkPlacementAttempt(ctx, t.playerID, slot.Waypoint, slot.Kind); err != nil {
		logging.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Sensing placement %s (%s) consumed a tick's placement budget but its attempt stamp could not be recorded, so it will not rotate to the back of the worklist and may take another slot's turn: %v",
			slot.Waypoint, slot.Kind, err), map[string]interface{}{
			"action":    "parked_sensing_attempt_stamp_failed",
			"waypoint":  slot.Waypoint,
			"slot_kind": slot.Kind,
		})
	}
}

// advanceOne applies the single transition available to one placement, and
// reports what it cost the tick.
func (t *placementTick) advanceOne(ctx context.Context, slot QueuedSlot, pos ShipPos) (placementOutcome, error) {
	switch slot.State {
	case SlotStateBought:
		return t.dispatch(ctx, slot, pos)
	case SlotStateInTransit:
		return t.standDown(ctx, slot, pos)
	default:
		return outcomeIdle, nil
	}
}

// dispatch sends a freshly-bought hull toward its slot.
func (t *placementTick) dispatch(ctx context.Context, slot QueuedSlot, pos ShipPos) (placementOutcome, error) {
	// Re-assert the fleet tag before anything else. The purchase's own tag write is
	// best-effort — it happens after the money has moved, where failing the whole
	// purchase would be worse than an untagged hull — so this is where an untagged
	// hull gets repaired, idempotently and non-fatally.
	t.warnIfTagFailed(ctx, slot.AssignedShip, slot.Waypoint, "parked_sensing_dispatch_tag_failed")

	if pos.NavStatus == navigation.NavStatusInTransit {
		// Already flying — under this or a previous tick's command. Issuing
		// another move now would be refused anyway.
		return outcomeIdle, nil
	}

	if pos.Waypoint == slot.Waypoint {
		// Bought at the very waypoint it was wanted at (a yard that is also the
		// slot). No movement to perform: hand it to the arrival branch, which
		// docks and parks it on the next tick.
		if err := t.transitionInFlight(ctx, slot, SlotStateBought, SlotStateInTransit); err != nil {
			return outcomeIdle, err
		}
		t.rep.Dispatched++
		return outcomeAdvanced, nil
	}

	if moveErr := t.flyToSlot(ctx, slot, pos); moveErr != nil {
		// The hull did not leave. Holding the slot at BOUGHT keeps the retry
		// CORRECT — the next tick re-reads the position and re-issues, with no
		// stuck state to unwind — but it is not free: at minimum the read that got
		// here, at most a command the API rejected. So it is charged to the refusal
		// budget and the slot is stamped as attempted.
		return outcomeFailed, nil
	}

	if err := t.transitionInFlight(ctx, slot, SlotStateBought, SlotStateInTransit); err != nil {
		return outcomeIdle, err
	}
	t.rep.Dispatched++
	return outcomeAdvanced, nil
}

// standDown docks an arrived hull and, once the ships table confirms it is
// docked, marks the placement PARKED — only on the CONFIRMED docked reading, never
// optimistically when the dock command returns. A slot marked PARKED is a slot the
// rest of the model believes is scanning, and the probe-presence check that decides
// whether to buy another hull for a waypoint reads exactly that, so a placement
// parked on a hull that never actually docked would suppress a purchase it should
// have allowed and leave the waypoint unwatched indefinitely.
func (t *placementTick) standDown(ctx context.Context, slot QueuedSlot, pos ShipPos) (placementOutcome, error) {
	if pos.Waypoint != slot.Waypoint {
		if pos.NavStatus == navigation.NavStatusInTransit {
			return outcomeIdle, nil // genuinely flying; leave it alone
		}
		// Sitting still, somewhere that is not the slot: this placement reached
		// IN_TRANSIT without anything ever having been told to move — see
		// dispatchClaim.
		return t.dispatchClaim(ctx, slot, pos)
	}

	switch pos.NavStatus {
	case navigation.NavStatusInOrbit:
		// Arrived but not berthed. Stay IN_TRANSIT: the next tick reads the
		// docked row and parks it.
		if err := t.pl.Mover.Dock(ctx, t.playerID, slot.AssignedShip); err != nil {
			return outcomeFailed, nil // retried next tick, off the refusal budget
		}
		t.rep.Docking++
		return outcomeAdvanced, nil

	case navigation.NavStatusDocked:
		if err := t.transitionInFlight(ctx, slot, SlotStateInTransit, SlotStateParked); err != nil {
			return outcomeIdle, err
		}
		t.rep.Parked++
		return outcomeAdvanced, nil

	default:
		return outcomeIdle, nil
	}
}

// dispatchClaim flies a hull whose placement was claimed straight into
// IN_TRANSIT without any movement ever being issued. Two claim paths write that
// state directly, because they start from a hull we ALREADY own and so have
// nothing to buy: this package's own spare re-tasking, and the seed tour-end
// claims made by the coordinator above it. Without this branch such a hull never
// leaves, the slot reads as in-flight forever, and the hull goes on counting
// against the probe cap while doing nothing.
//
// The discriminator is the nav row, not the position: a hull genuinely moving is
// left alone (piling a second move onto a ship in flight is what the
// once-per-slot-per-tick discipline exists to prevent), while a hull sitting still
// somewhere that is not its slot has demonstrably never been dispatched. The slot
// is ALREADY IN_TRANSIT, so there is no state to advance — only a command to
// issue, retried next tick off the refusal budget if it is refused.
func (t *placementTick) dispatchClaim(ctx context.Context, slot QueuedSlot, pos ShipPos) (placementOutcome, error) {
	// Claims that bypass the purchase path bypass its tag write too, so this is the
	// ONLY place such a hull is tagged: a silent failure here leaves it untagged for
	// its whole flight, long enough for another coordinator's ownership sweep to
	// claim it. Idempotent, so it costs nothing for hulls that already carry it.
	t.warnIfTagFailed(ctx, slot.AssignedShip, slot.Waypoint, "parked_sensing_claim_tag_failed")

	if err := t.flyToSlot(ctx, slot, pos); err != nil {
		return outcomeFailed, nil // retried next tick, off the refusal budget
	}
	t.rep.Dispatched++
	return outcomeAdvanced, nil
}

// warnIfTagFailed asserts the sensing fleet tag and reports a failure without
// changing control flow. The tag is always best-effort here — a missing one is
// repaired on the next edge — but it is never silent, because an untagged hull is
// a poachable one.
func (t *placementTick) warnIfTagFailed(ctx context.Context, shipSymbol, waypoint, action string) {
	if err := t.pl.Fleet.AssignFleet(ctx, t.playerID, shipSymbol, SensingParkedFleetTag); err != nil {
		// The destination rides along with the hull: an untagged probe is found by
		// looking at where it was being sent, and a warning naming only the hull
		// leaves the reader grepping the ledger for the placement it belongs to.
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
// in-system planner or the cross-system gate walk by comparing the hull's current
// system against the slot's. Neither branch waits — one hop, or one step of the
// walk — so a hull several gates out costs this tick exactly one dispatch and the
// ticks that follow carry it the rest of the way, each re-reading pos and
// re-deciding from scratch, which is what makes the walk self-correcting when a
// hull ends up somewhere the last tick did not send it.
func (t *placementTick) flyToSlot(ctx context.Context, slot QueuedSlot, pos ShipPos) error {
	if shared.ExtractSystemSymbol(pos.Waypoint) == slot.System {
		return t.pl.Mover.NavigateWithin(ctx, t.playerID, slot.AssignedShip, slot.Waypoint)
	}
	return t.pl.Mover.RouteAcross(ctx, t.playerID, slot.AssignedShip, pos.Waypoint, slot.Waypoint)
}

// transitionInFlight advances a placement, treating a lost race as a normal
// outcome. These edges move no money and buy nothing — if another writer got
// there first, the placement is already where this tick wanted to put it.
//
// It takes the SLOT rather than a bare waypoint because a waypoint does not
// identify a placement on its own: a yard can hold a MARKET row and a SPARE row
// at once, and this hull is flying to exactly one of them.
func (t *placementTick) transitionInFlight(ctx context.Context, slot QueuedSlot, from, to string) error {
	err := t.pl.Ledger.TransitionSlot(ctx, t.playerID, SlotTransition{
		Waypoint: slot.Waypoint, Kind: slot.Kind, From: from, To: to,
	}, SlotFields{})
	if err == nil || errors.Is(err, ErrSlotClaimed) {
		return nil
	}
	return fmt.Errorf("failed to advance sensing placement %s (%s) from %s to %s: %w", slot.Waypoint, slot.Kind, from, to, err)
}

type placementTick struct {
	pl       PlacementPorts
	playerID int
	rep      *PlacementReport
}

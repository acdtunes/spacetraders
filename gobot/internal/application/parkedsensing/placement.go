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
//
// IT BOUNDS ACCEPTED COMMANDS, NOT ATTEMPTS (sp-cwnwb). That was always what the
// paragraph above described — a "burst of navigation commands" is a burst of
// commands the API took — and it is what PlacementReport.Actions was always
// documented as counting ("everything above", all three of which are successes).
// The code nonetheless charged a REFUSED move to this budget too, and that one
// discrepancy was the starvation: the worklist is a fixed cap over a queue that
// does not drain, so ten slots whose move fails every tick, read in the same
// order every tick, spent the entire budget before any healthy slot behind them
// was examined. Measured live: 266 BOUGHT + 52 IN_TRANSIT, zero dispatches over
// ~40 available actions, and one slot 22.5 hours old that had never been tried
// once. Refusals are bounded separately — see placementFailureBudgetMultiple.
const DefaultMaxPlacementActions = 10

// placementFailureBudgetMultiple sizes the SEPARATE budget a tick gives to moves
// that are refused, as a multiple of the accepted-command budget.
//
// A refusal needs its own bound rather than no bound: it leaves the slot where it
// was and costs no navigation, but it is not free — the walk's second step issues
// a jump the API can reject, so an unbounded sweep of a 300-slot backlog could
// fire 300 rejected commands in one tick. Nor may it share the accepted-command
// budget, which is the defect above.
//
// WHY A MULTIPLE, AND WHY THREE. Refusals are the toll paid walking to the
// healthy slots, and the dominant refusal today is free: an unroutable gate walk
// is decided entirely from stored adjacency and never reaches the API at all
// (RouteAcross resolves the next system BEFORE it moves anything, exactly so a
// wall costs no flight). So the toll can afford to be larger than the work it
// buys, and making it larger is what shortens the sweep — with the
// least-recently-attempted order below, a backlog of N failing slots is walked in
// ceil(N/failureBudget) ticks, so three times the budget covers 300 walled slots
// in ~10 ticks instead of ~30. Three, not thirty: the worst case, where every
// refusal is a rejected jump, still has to stay a bounded amount of wasted API
// traffic per tick.
const placementFailureBudgetMultiple = 3

// placementCrossingReserve is the number of a tick's accepted-command budget held
// back for hulls still travelling, when arrival work would otherwise take it all.
//
// WHY A RESERVE IS NEEDED AT ALL. Arrivals are spent first because an
// arrival CONVERTS a hull into coverage and removes the slot from the worklist,
// while a gate hop only moves it one ring closer and leaves it competing next tick.
// But a class that always wins is precisely how sp-cwnwb's starvation worked, and
// the least-recently-attempted rotation CANNOT protect against it: rotation orders
// slots WITHIN the worklist, and a class preference re-orders ACROSS it, so a slot
// that keeps re-qualifying as an arrival sits at the head of every tick however
// recently it was stamped. That is not hypothetical — a hull whose dock command
// succeeds but whose ships row never flips to docked re-docks every tick forever,
// and ten such hulls would stop the other 287 moving at all.
//
// TWO, OUT OF TEN. Small enough that the reordering keeps almost all of its value
// (the backlog is ~287 crossing against ~13 arriving, so arrivals rarely want more
// than a fraction of the budget anyway), and large enough that the crossing class
// always makes progress, which is what keeps producing the arrivals in the first
// place.
//
// It is a floor for work that EXISTS, not a permanent carve-out: arrivals held back
// by it are DEFERRED behind the crossings rather than dropped, so a tick with
// nothing to cross — or whose crossings all turn out idle or refused — spends the
// whole budget on arrivals anyway. See arrivalsFirst.
const placementCrossingReserve = 2

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
// in-flight placements, advance one, record that one was tried. It is narrower
// than the buy queue's BuyLedger on purpose — the machine spends nothing, so it
// is given no access to the probe count or the system verdicts that gate
// spending.
type PlacementLedger interface {
	// PlacementWorklist returns the in-flight placements in the given states,
	// LEAST RECENTLY ATTEMPTED FIRST, with never-attempted slots ahead of every
	// attempted one and a total tie-break behind that.
	//
	// The order is part of the contract, not an implementation detail, and this is
	// a separate read from SlotsByState for exactly that reason: every other
	// caller of that method wants a stable alphabetical list, and this one must
	// have the opposite (sp-cwnwb). A tick-stable order over a queue that does not
	// drain gives the list a permanent head, and a fixed budget then means
	// everything behind that head is never examined at all.
	PlacementWorklist(ctx context.Context, playerID int, states ...string) ([]QueuedSlot, error)
	TransitionSlot(ctx context.Context, playerID int, waypoint, kind, fromState, toState string, set SlotFields) error
	// MarkPlacementAttempt records that a slot consumed one of a tick's budgets,
	// which is what moves it to the back of the worklist above.
	//
	// It writes ONLY the attempt stamp. It must not touch state or the assigned
	// hull: those two columns are what the probe cap counts (CountOwnedProbes),
	// and a fairness stamp that could drop a slot out of that count would let the
	// engine buy a hull it already owns.
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
	// Actions counts everything above against the per-tick budget. SUCCESSES
	// ONLY — a refused move is counted in Failures and nowhere else.
	//
	// That distinction reaches beyond this budget. The tick verdict reads
	// Actions > 0 as "a placement advanced" (see anyEffect in
	// probe_sensing_stall.go), so while a refusal landed here, a tick that
	// refused ten moves and accomplished nothing was filed as PROGRESS — which
	// is how 318 frozen slots stayed invisible to the stall detector for 22.5
	// hours. Ticks that only fail now read as idle, which is the truth.
	Actions int
	// Failures counts moves that were issued and REFUSED, against their own
	// separate budget. A refusal leaves the slot exactly as it was, to be retried
	// next tick; it is counted because it is not free, and kept apart from
	// Actions because charging the two alike is what starved the worklist.
	Failures int
}

// placementOutcome is what one slot's turn cost the tick.
//
// The split between the last two values is the whole of sp-cwnwb. Both leave the
// slot in the state it was already in, so to the state machine they are
// indistinguishable — but one bought a step toward a probe standing on station and
// the other bought nothing, and charging them to the same ten-slot budget let a
// permanently-failing head freeze the 300 slots behind it.
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
// TWO BUDGETS, AND A ROTATING ORDER (sp-cwnwb). Accepted commands are bounded by
// maxActions and refused ones by their own larger bound, because a refusal buys
// no progress and must not be able to spend the budget that does. And the
// worklist arrives least-recently-attempted first, with every slot that consumes
// a budget stamped as it goes, so no set of slots can hold the head across ticks.
//
// Neither half is sufficient alone, which is why both are here. Separate budgets
// with a fixed order still hand the same head the same turn every tick — it just
// takes more of them to exhaust the tick. A rotating order with one shared budget
// still lets a wall of refusals spend it. Together they give the property that
// matters: a backlog of N slots is fully covered in a bounded number of ticks
// whatever fraction of it is failing.
//
// This is the same fix the screening sweep already made for itself — see the
// screened_at rotation in run_probe_sensing_coordinator.go, whose comment sets
// out why a fixed cap over a queue that does not drain must not be read in a
// tick-stable order.
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

	for _, w := range arrivalsFirst(ctx, pl, playerID, slots, maxActions) {
		if rep.Actions >= maxActions {
			// The accepted-command budget is spent. Walking further can only issue
			// commands there is no budget left to keep.
			break
		}
		if rep.Failures >= maxFailures {
			// The toll is spent. The slots not reached this tick are not lost: they
			// carry the OLDEST attempt stamps now, so the next tick's worklist
			// begins with them.
			break
		}
		slot := w.slot

		outcome, err := advanceOne(ctx, pl, playerID, slot, w.pos, &rep)
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
			// This slot was charged for a turn, whichever way it went, so it goes
			// to the BACK of the next tick's worklist. Stamping both outcomes is
			// what makes the order rotate: stamping only successes would leave the
			// failing slots permanently oldest and hand them the head forever —
			// the very monopoly this is here to break.
			markAttempt(ctx, pl, playerID, slot)
		}
	}
	return rep, nil
}

// placementWork is one slot paired with the hull position already read for it, so
// the position is read ONCE per slot per tick rather than again when the slot's
// turn comes. Re-reading would be safe but wasteful: one slot gets one action per
// tick, so nothing this tick can move a hull between the two reads.
type placementWork struct {
	slot QueuedSlot
	pos  ShipPos
}

// arrivalsFirst reorders one tick's worklist so hulls that have ARRIVED are served
// before hulls still travelling, and reads each hull's position once on the way.
//
// THE ORDERING IS THE WHOLE POINT. An arrival converts a hull into coverage
// and takes its slot out of the worklist for good; a gate hop moves it one ring
// closer and leaves it competing for the same budget next tick. Serving arrivals
// first therefore shortens the queue, which speeds everything behind it — and it
// costs no extra API budget at all, because it spends the SAME accepted-command
// budget in a better order. Live, ungrouped ledger order read `dispatched 10 docking 0
// parked 0` for three consecutive ticks while 13 hulls stood on their targets: 287
// crossing slots reached the budget first every tick, so hulls that were already
// physically at their destination took ~30 minutes to berth.
//
// THE RESERVE IS NOT OPTIONAL, see placementCrossingReserve. A preference that
// always wins is how a class starves, and the rotation cannot undo
// it, so the crossing class keeps a floor whenever it has work.
//
// Order within each class is left exactly as the ledger returned it — least
// recently attempted first — because that is the rotation that keeps any one slot
// from holding its class's head across ticks. This function only groups; it never
// sorts.
//
// Slots that cannot be acted on at all are dropped here rather than costing a turn
// later: a slot with no recorded hull is a torn write with nothing to fly (the buy
// queue owns repairing it, and commanding a ship whose symbol we do not have is
// impossible), and a hull the ships table cannot locate must never be commanded —
// both leave the slot exactly as it is, to be retried once the ledger can answer.
//
// COST. One indexed position read per in-flight slot per tick, against the database
// and never the API (see ParkedShipReader). Reading only as far as
// the budget reaches would be around 40 slots; this is roughly a 300-read tick at the
// current backlog, on a 756-row table keyed by the exact lookup. That is the price
// of knowing which slots have arrived BEFORE choosing which to serve, and it buys
// the reordering without touching the API budget that is actually scarce.
func arrivalsFirst(ctx context.Context, pl PlacementPorts, playerID int, slots []QueuedSlot, maxActions int) []placementWork {
	arrived := make([]placementWork, 0, len(slots))
	travelling := make([]placementWork, 0, len(slots))

	for _, slot := range slots {
		if slot.AssignedShip == "" {
			continue
		}
		pos, err := pl.Ships.ShipAt(ctx, playerID, slot.AssignedShip)
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
	// holds back are re-appended below, so with nothing to cross the deferred
	// arrivals simply flow back into the same tick and the budget is spent in full.
	// A guard here would be dead logic — a mutation probe that made it bind
	// unconditionally changed no observable behaviour, which is how it was found.
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
	// Arrivals held back by the reserve are not dropped, only deferred: if the
	// crossings turn out to be idle or refused, the budget returns to them in this
	// same tick rather than going unspent.
	out = append(out, arrived[arrivalCap:]...)
	return out
}

// hasArrived reports whether a slot's hull is standing ON its destination in a
// state standDown can turn into an accepted command — in orbit (one dock away) or
// docked (one ledger edge away from PARKED).
//
// It must stay in lockstep with standDown's own branches: a slot counted as arrived
// here that standDown then treats as idle would be promoted ahead of real work and
// buy nothing. Anything else — still crossing, genuinely in flight, or standing
// somewhere that is not the slot — is travelling.
func hasArrived(slot QueuedSlot, pos ShipPos) bool {
	if slot.State != SlotStateInTransit || pos.Waypoint != slot.Waypoint {
		return false
	}
	return pos.NavStatus == navigation.NavStatusInOrbit || pos.NavStatus == navigation.NavStatusDocked
}

// markAttempt records that a slot consumed one of this tick's budgets, and
// reports nothing but a warning if it cannot.
//
// The stamp is a FAIRNESS HINT, not a state transition: it moves no money, buys
// nothing, and leaves the state machine untouched, so a write that fails must not
// fail the tick over a slot that was genuinely advanced. It degrades safely — a
// slot with no stamp sorts as never-attempted, so the worst a lost write can do is
// give that slot another early turn, and a ledger that cannot take the stamp at
// all falls back to exactly the fixed waypoint order that shipped before.
//
// It is never silent, though. Stamps failing wholesale is starvation coming back,
// and the symptom (a head that never rotates) looks identical to the bug it fixes.
func markAttempt(ctx context.Context, pl PlacementPorts, playerID int, slot QueuedSlot) {
	if err := pl.Ledger.MarkPlacementAttempt(ctx, playerID, slot.Waypoint, slot.Kind); err != nil {
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
func advanceOne(ctx context.Context, pl PlacementPorts, playerID int, slot QueuedSlot, pos ShipPos, rep *PlacementReport) (placementOutcome, error) {
	switch slot.State {
	case SlotStateBought:
		return dispatch(ctx, pl, playerID, slot, pos, rep)
	case SlotStateInTransit:
		return standDown(ctx, pl, playerID, slot, pos, rep)
	default:
		return outcomeIdle, nil
	}
}

// dispatch sends a freshly-bought hull toward its slot.
func dispatch(ctx context.Context, pl PlacementPorts, playerID int, slot QueuedSlot, pos ShipPos, rep *PlacementReport) (placementOutcome, error) {
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
		return outcomeIdle, nil
	}

	if pos.Waypoint == slot.Waypoint {
		// Bought at the very waypoint it was wanted at (a yard that is also the
		// slot). No movement to perform: hand it to the arrival branch, which
		// docks and parks it on the next tick.
		if err := transitionInFlight(ctx, pl, playerID, slot, SlotStateBought, SlotStateInTransit); err != nil {
			return outcomeIdle, err
		}
		rep.Dispatched++
		return outcomeAdvanced, nil
	}

	if moveErr := flyToSlot(ctx, pl, playerID, slot, pos); moveErr != nil {
		// The hull did not leave. Holding the slot at BOUGHT keeps the retry
		// CORRECT — the next tick re-reads the position and re-issues, with no
		// stuck state to unwind — but it is not free, and the comment that used to
		// stand here said it was. It costs a turn: at minimum the read that got
		// here, and at most a command the API rejected. Charging that turn to the
		// same budget as an accepted command is what let a wall of refusals freeze
		// the whole worklist (sp-cwnwb), so it is charged to the refusal budget
		// instead and the slot is stamped as attempted.
		return outcomeFailed, nil
	}

	if err := transitionInFlight(ctx, pl, playerID, slot, SlotStateBought, SlotStateInTransit); err != nil {
		return outcomeIdle, err
	}
	rep.Dispatched++
	return outcomeAdvanced, nil
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
func standDown(ctx context.Context, pl PlacementPorts, playerID int, slot QueuedSlot, pos ShipPos, rep *PlacementReport) (placementOutcome, error) {
	if pos.Waypoint != slot.Waypoint {
		if pos.NavStatus == navigation.NavStatusInTransit {
			return outcomeIdle, nil // genuinely flying; leave it alone
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
			return outcomeFailed, nil // retried next tick, off the refusal budget
		}
		rep.Docking++
		return outcomeAdvanced, nil

	case navigation.NavStatusDocked:
		if err := transitionInFlight(ctx, pl, playerID, slot, SlotStateInTransit, SlotStateParked); err != nil {
			return outcomeIdle, err
		}
		rep.Parked++
		return outcomeAdvanced, nil

	default:
		return outcomeIdle, nil
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
// which is the same bounded retry the purchase path gets: correct, but charged to
// the refusal budget rather than free (sp-cwnwb).
func dispatchClaim(ctx context.Context, pl PlacementPorts, playerID int, slot QueuedSlot, pos ShipPos, rep *PlacementReport) (placementOutcome, error) {
	// Claims that bypass the purchase path also bypass the tag write that
	// happens there, so this is the first and only place such a hull is tagged.
	// Idempotent, so it costs nothing for the hulls that already carry it.
	//
	// This is the ONLY place a claimed hull is tagged, so a silent failure here
	// leaves it untagged for its whole flight — long enough for another
	// coordinator's ownership sweep to claim it.
	warnIfTagFailed(ctx, pl, playerID, slot.AssignedShip, slot.Waypoint, "parked_sensing_claim_tag_failed")

	if err := flyToSlot(ctx, pl, playerID, slot, pos); err != nil {
		return outcomeFailed, nil // retried next tick, off the refusal budget
	}
	rep.Dispatched++
	return outcomeAdvanced, nil
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
// identifies a placement on its own: a yard can hold a MARKET row and
// a SPARE row at once, and this hull is flying to exactly one of them.
func transitionInFlight(ctx context.Context, pl PlacementPorts, playerID int, slot QueuedSlot, from, to string) error {
	err := pl.Ledger.TransitionSlot(ctx, playerID, slot.Waypoint, slot.Kind, from, to, SlotFields{})
	if err == nil || errors.Is(err, ErrSlotClaimed) {
		return nil
	}
	return fmt.Errorf("failed to advance sensing placement %s (%s) from %s to %s: %w", slot.Waypoint, slot.Kind, from, to, err)
}

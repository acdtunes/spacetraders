package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipQueries "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- MoverPort --------------------------------------------------------------

// MoverPort issues movement commands through the mediator. The two navigation
// verbs are separate machinery, and the caller picks between them by comparing
// systems — this type does not second-guess that choice.
//
// EVERY VERB HERE DISPATCHES AND RETURNS. The placement machine behind this port
// is a tick machine: it takes one action per placement per tick, holds nothing in
// memory between ticks, and reads the ships table on a LATER tick to see what
// happened ("a hull dispatched by this tick is not also examined for arrival by
// it", placement.go). A verb that waited out the flight would hold the whole tick
// open for the length of a journey, starving every placement behind it in the
// ledger read and every stage after it in the coordinator — the buy-queue drain,
// the reaper, adoption, and the expansion pass where charting seeds launch.
//
// So the command choice is load-bearing, not incidental. NavigateRouteCommand
// plans and FLIES a whole route, ending in WaitForShipArrival;
// NavigateDirectCommand hands one hop to the API and returns with the arrival
// time. Only the second is admissible here, and the arrival it does not wait for
// is recorded anyway: ShipRepository.Navigate persists the IN_TRANSIT row with
// its arrival clock and arms ShipStateScheduler, whose timer (backed by a
// 60-second sweeper, and re-armed for every in-flight hull on daemon restart)
// writes the landing back to the ships table. That row IS how the next tick
// reconciles the arrival, which is exactly what the placement machine already
// reads.
type MoverPort struct {
	mediator common.Mediator
	// neighbours is the stored gate adjacency the cross-system walk picks its
	// next system from. A PURE STORE read (see GateNeighbourPort): topology is
	// never worth an API call here, and a system the store cannot answer for is
	// one this port declines to walk through rather than guesses at.
	neighbours appSensing.GateNeighbours
}

// NewMoverPort wires the movement verbs. neighbours may be nil, in which case
// the cross-system walk fails closed — it holds the placement and warns, exactly
// as it does for a destination the stored graph cannot reach.
func NewMoverPort(mediator common.Mediator, neighbours appSensing.GateNeighbours) *MoverPort {
	return &MoverPort{mediator: mediator, neighbours: neighbours}
}

// NavigateWithin dispatches a hull toward a waypoint inside its current system
// and returns as soon as the API has accepted the move.
//
// The orbit first is not a precaution, it is the API's precondition: a DOCKED
// hull cannot navigate, and a parked sensing probe is docked by definition — the
// placement machine docks it on arrival and re-tasked spares are taken straight
// off a berth. OrbitShipCommand is free for a hull already in orbit (it answers
// "already_in_orbit" off the local row without touching the network), so the
// common case costs nothing, and issuing it explicitly keeps the navigate off
// NavigateDirectCommand's not-in-orbit self-heal path — which would otherwise
// spend a REJECTED live call plus a warning on every single docked dispatch.
//
// Both commands together are ONE placement action: the pair either starts the
// hull moving or leaves it exactly where it was, and the caller's free retry
// (hold the slot, re-read the position next tick) covers the failure either way.
func (p *MoverPort) NavigateWithin(ctx context.Context, playerID int, shipSymbol, destination string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	return dispatchHop(ctx, p.mediator, pid, shipSymbol, destination)
}

// routerHopBound bounds how far the cross-system walk will look for a route.
// NON-POSITIVE, so the search runs until the traversable component is exhausted.
//
// NOT A NUMBER, A REFERENCE. The bound is declared once beside the contract it
// describes, because the walk is not its only reader: the callers that pick
// destinations have to refuse anything this walk cannot resolve, and a second
// copy of the number here is what would let the two drift into handing out
// unreachable errands.
//
// IT READS THE WIDEST BOUND ANY CALLER STAGES AT, which is the charting seed's
// (appSensing.SeedFlightUnbounded) rather than the placement walk's
// (appSensing.MaxWalkRings, still 2). The resolver has to cover the longest
// journey anything is actually sent on: a seed staged far out needs a next
// system named on its FIRST step, and a resolver that gave up at two would name
// none, failing every step while the hull counted against the probe cap and
// charted nothing.
//
// A WIDE RESOLVER DOES NOT WIDEN WHAT PLACEMENTS ATTEMPT, and that is why this is
// safe rather than a quiet loosening of a second engine. What a placement is
// asked to fly is decided upstream by the callers that choose destinations —
// foothold's sourcesWithinReach draws a surplus hull from within MaxWalkRings,
// and the buy queue buys in the slot's own system. This verb answers questions;
// it does not ask them.
const routerHopBound = appSensing.SeedFlightUnbounded

// RouteAcross advances a hull ONE STEP of its gate walk toward destination, and
// returns either way. It NEVER waits.
//
// WHY IT DOES NOT SIMPLY REFUSE. There is no dispatch-and-return primitive for a
// whole gate crossing: RouteShipCommand, delegating to the shared multi-jump
// travel(), resolves a gate path and FLIES it, waiting out every leg and every
// jump cooldown in between. Sending that from inside a tick is the exact failure
// this port exists to prevent. Refusing instead is safe but it is a wall: a probe
// could never leave the system it was bought in, so only the system we already
// occupy would ever have usable yards, and the frontier could not widen.
//
// The way out is the one the charting seed's gate hop already took: don't fly
// the crossing, WALK it. A crossing is a sequence of steps — onto the gate, then
// off it, once per gate on the way — and each step is a command that returns as
// soon as the API accepts it. This performs exactly the one step the hull's
// current position calls for, and the next tick performs the next.
//
// THE DISCRIMINATOR IS THE WAYPOINT, NEVER THE DISTANCE. fromWaypoint is
// compared against the gate SYMBOL, because orbitals share coordinates with the
// body they orbit — a hull can sit at zero distance from a gate it is not
// standing on, and "jumping" from there is precisely what puts us back on the
// blocking branch, since JumpShipCommand would navigate it to the gate first and
// wait out that flight.
//
// NOTHING IS PERSISTED HERE, and nothing needs to be. How far the crossing has
// got is fully described by two rows this machine already keeps: the slot naming
// the destination and the hull, and the ships table naming where that hull now
// stands. Every tick re-reads both and re-decides from scratch, which is what
// makes the walk resume across a daemon restart and self-correct if a hull ends
// up somewhere the last step did not send it.
//
// FAIL CLOSED, AND BEFORE SPENDING MOVEMENT. The next system is resolved FIRST,
// so a destination the stored graph cannot reach costs no flight at all: the
// placement holds exactly where it was, keeps its hull (so the probe cap still
// counts it — the over-count direction RULINGS #4 requires), and the next tick
// retries for free.
func (p *MoverPort) RouteAcross(ctx context.Context, playerID int, shipSymbol, fromWaypoint, destination string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	currentSystem := shared.ExtractSystemSymbol(fromWaypoint)
	if currentSystem == shared.ExtractSystemSymbol(destination) {
		// Already across: the gates are behind it and what is left is one
		// in-system hop. Defensive — flyToSlot sends the in-system verb in this
		// case — but it costs nothing and keeps the walk correct on its own.
		return dispatchHop(ctx, p.mediator, pid, shipSymbol, destination)
	}

	// Resolved before any movement, so an unreachable destination never buys a
	// wasted flight to a gate.
	nextSystem, err := nextHopToward(ctx, p.neighbours, currentSystem, shared.ExtractSystemSymbol(destination))
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Sensing placement wants %s walked from %s to %s, but no gate route could be named within %d jumps of stored adjacency — the placement is held and retried, and the hull keeps counting against the probe cap: %v",
			shipSymbol, currentSystem, destination, routerHopBound, err), map[string]interface{}{
			"action":         "parked_sensing_gate_walk_unroutable",
			"ship_symbol":    shipSymbol,
			"from_system":    currentSystem,
			"destination":    destination,
			"max_walk_rings": routerHopBound,
		})
		return fmt.Errorf("failed to name the next system for %s walking to %s: %w", shipSymbol, destination, err)
	}

	// The step is named too, and not only for symmetry with the unroutable case
	// above. A route that RESOLVES and then cannot be walked is otherwise silent:
	// the error propagates back through flyToSlot to dispatch, which holds the slot
	// for the next tick, so an unnamed step lets a hull spend one placement attempt
	// per tick indefinitely with nothing in the log to diagnose the freeze from.
	//
	// The two warnings are deliberately distinct actions. Unroutable means the
	// stored graph names no way there and the fix is a gate re-probe; a refused
	// step means the graph was right and the MOVE failed, which is a fuel state or
	// a gate that will not take the hull. Reading one as the other sends the next
	// reader to the wrong subsystem.
	//
	// A COOLDOWN HOLD IS NEITHER, and it must not borrow this wording. The hold is
	// the healthy case — the hull is waiting out a game timer nobody can shorten,
	// stepThroughGate has already named it at INFO with its expiry, and no request
	// was spent learning it. Reported as a refusal it reads as a routing WARNING,
	// one per hull per tick, and makes a working system look blocked.
	if err := stepThroughGate(ctx, p.mediator, pid, shipSymbol, fromWaypoint, nextSystem); err != nil {
		if heldForCooldown(err) {
			return err
		}
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Sensing placement resolved a gate route for %s from %s toward %s, but the step from %s into %s was refused — the placement is held and retried, and the hull keeps counting against the probe cap: %v",
			shipSymbol, currentSystem, destination, fromWaypoint, nextSystem, err), map[string]interface{}{
			"action":        "parked_sensing_gate_step_failed",
			"ship_symbol":   shipSymbol,
			"from_system":   currentSystem,
			"from_waypoint": fromWaypoint,
			"next_system":   nextSystem,
			"destination":   destination,
		})
		return err
	}
	return nil
}

// stepThroughGate performs the ONE physical move a gate crossing calls for from
// where the hull is standing: onto the gate, or off it into nextSystem.
//
// Shared by both walkers — the placement mover's and the charting seed's — for
// the same reason dispatchHop and gateToLeaveFrom are. They perform the same two
// steps under the same no-waiting contract, and two copies of the sequence could
// drift into moving a hull to one gate and jumping it from another.
//
// nextSystem must already be ADJACENT to the hull's current system: naming a
// system the gate does not connect to is a jump the API rejects, and the hull
// would sit on the gate re-issuing it forever. Resolving that is the caller's
// job, and both callers do it BEFORE reaching here, which is what keeps an
// unroutable destination from buying a wasted flight to a gate.
//
// THE COOLDOWN GUARD SITS ON THE JUMP, NOT ON THE STEP. A jump cooldown gates the
// jump ACTION and nothing else, so a cooling hull may still be flown onto its gate
// — that step is productive work, and refusing it would idle the hull for the
// whole cooldown and then need another tick to start walking. Only the branch that
// would issue the jump consults it.
func stepThroughGate(
	ctx context.Context,
	med common.Mediator,
	pid shared.PlayerID,
	shipSymbol, fromWaypoint, nextSystem string,
) error {
	gate, err := gateToLeaveFrom(sensingCtx(ctx), med, pid.Value(), shipSymbol)
	if err != nil {
		return err
	}
	if gate != fromWaypoint {
		// Step one: get it onto the gate, dispatch-only. The durable row holding
		// where it is — the slot, or the seed's errand — is what makes the next
		// tick pick up from here.
		return dispatchHop(ctx, med, pid, shipSymbol, gate)
	}

	// Step two. The hull is ON the gate, so JumpShipCommand's navigate branch is
	// unreachable and the command is the bare jump it was named for. The jump is
	// instantaneous at the API, leaving only a reactor cooldown — which gates the
	// NEXT jump, and which this walk must not spend a request re-discovering.
	//
	// THE RETRY WAS NEVER THE BUG; THE ATTEMPT WAS. A tick machine re-decides from
	// scratch every tick, so a hull mid-cooldown is handed back to this same branch
	// on every one of them, each attempt a 409 code-4000 the API answers with the
	// very expiry the ships row already holds — a steady share of the account's
	// request budget spent re-learning a known fact, against a ceiling several
	// coordinators contend for.
	//
	// So the cooldown is consulted FIRST, from the hull's own row, and a hull that
	// cannot jump is HELD rather than asked. This is the trade watchdog's idiom
	// (watchdog.go: `ship.IsInTransit() || ship.IsOnCooldown(now)`) and the siphon
	// worker's, deliberately reused rather than a second notion of "busy" invented
	// beside them.
	if until, held := heldByJumpCooldown(ctx, med, pid, shipSymbol); held {
		common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
			"Sensing gate step for %s at %s into %s is HELD: the hull is on jump cooldown until %s (%s remaining) — no jump was attempted, and the next tick retries for free",
			shipSymbol, fromWaypoint, nextSystem,
			until.UTC().Format(time.RFC3339), time.Until(until).Round(time.Second)),
			map[string]interface{}{
				"action":         "parked_sensing_gate_step_cooldown_hold",
				"ship_symbol":    shipSymbol,
				"from_waypoint":  fromWaypoint,
				"next_system":    nextSystem,
				"cooldown_until": until.UTC().Format(time.RFC3339),
			})
		return &jumpCooldownHold{shipSymbol: shipSymbol, nextSystem: nextSystem, until: until}
	}

	id := pid.Value()
	if _, err := med.Send(sensingCtx(ctx), &shipNav.JumpShipCommand{
		ShipSymbol:        shipSymbol,
		DestinationSystem: nextSystem,
		PlayerID:          &id,
	}); err != nil {
		return fmt.Errorf("failed to jump %s to %s: %w", shipSymbol, nextSystem, err)
	}
	return nil
}

// jumpCooldownHold reports that a gate step was NOT attempted, because the hull is
// waiting out a jump cooldown the ships row was already carrying when the step was
// considered.
//
// IT IS STILL AN ERROR, and deliberately so: the walk did not advance, and every
// caller's hold-and-retry — the placement machine's refusal budget and re-stamp,
// the seed errand's hold — is exactly the handling a held step wants. What the
// type buys is a caller that can tell a HOLD from a REFUSAL without parsing a
// message, so the healthy case stops borrowing the routing failure's wording.
type jumpCooldownHold struct {
	shipSymbol string
	nextSystem string
	until      time.Time
}

func (e *jumpCooldownHold) Error() string {
	return fmt.Sprintf("held %s from jumping to %s: on jump cooldown until %s (no jump attempted)",
		e.shipSymbol, e.nextSystem, e.until.UTC().Format(time.RFC3339))
}

// Unwrap publishes the hold through the port's own sentinel, so the application layer
// can tell an unattempted step from a refused one without importing this package.
func (e *jumpCooldownHold) Unwrap() error {
	return appSensing.ErrSeedStepHeld
}

// heldForCooldown reports whether err is a held step rather than a refused one.
func heldForCooldown(err error) bool {
	var held *jumpCooldownHold
	return errors.As(err, &held)
}

// heldByJumpCooldown answers "can this hull jump right now?" from the ships table,
// and names when it can if it cannot.
//
// A PURE STORE READ, AND NOTHING NEW IS STORED (RULINGS #2). The expiry is already
// a column on the hull's row: JumpShipCommand CAS-writes it the instant a jump
// succeeds (persistPostJumpNav → SetCooldown), and GetShipQuery resolves through
// ShipRepository.FindBySymbol, which reads that row from the database with no
// cache in front of it. So this is the freshest answer that exists locally, it
// costs no request, and a cooldown cache here would only be a second, staler copy
// of a fact the row already carries.
//
// IT FAILS OPEN, and that direction is the safe one: an unreadable row, a missing
// ship, or an unexpected response leaves the walk EXACTLY as it was before this
// guard existed — the jump is attempted, and the API remains the authority that
// refuses it. Holding on absence would be the dangerous direction, because an
// unknown cooldown is not a known one (a hull whose row we cannot read would be
// held forever, never jumping and never arriving, while still counting against the
// probe cap). This guard removes a doomed call; it never authorises one, and it
// never blocks a hull it cannot prove is on cooldown.
func heldByJumpCooldown(ctx context.Context, med common.Mediator, pid shared.PlayerID, shipSymbol string) (time.Time, bool) {
	id := pid.Value()
	// sensingCtx for the same reason every other Send here carries it: the read is
	// a database hit today, but FindBySymbol falls back to an API sync for a hull
	// with no row yet, and a request this engine causes must be attributed to it.
	res, err := med.Send(sensingCtx(ctx), &shipQueries.GetShipQuery{ShipSymbol: shipSymbol, PlayerID: &id})
	if err != nil {
		return time.Time{}, false
	}
	resp, ok := res.(*shipQueries.GetShipResponse)
	if !ok || resp.Ship == nil {
		return time.Time{}, false
	}
	// The domain's own predicate, not a hand-rolled comparison: a nil or
	// already-past expiry is NOT a cooldown there, so a stale timestamp can never
	// hold a hull that is free to jump.
	if !resp.Ship.IsOnCooldown(time.Now()) {
		return time.Time{}, false
	}
	expiry := resp.Ship.CooldownExpiration()
	if expiry == nil {
		return time.Time{}, false
	}
	return *expiry, true
}

// nextHopToward names the ADJACENT system a walk should jump to next to get from
// fromSystem toward toSystem, by breadth-first search over the stored gate
// adjacency out to routerHopBound.
//
// It returns the FIRST-RING system, not the path: the only thing a single step
// can act on is the next jump, and re-deriving it each tick from where the hull
// actually is beats carrying a stale path forward. Breadth-first means the
// system it names is on a SHORTEST route, so the walk cannot circle.
//
// PURE STORE READS. Neighbours already drops under-construction and stale edges
// and answers an unknown system with nothing, so a topology we are unsure of
// ends the search rather than routing a hull into a gate that will reject it.
//
// A PACKAGE FUNCTION rather than a method, because it has two callers now: the
// placement mover and the charting seed walk the same graph under the same bound
// and must agree about what is reachable. The application layer stages seeds
// against its own search reading that same declaration, so a second copy here is
// what would let staging hand out errands this cannot resolve.
func nextHopToward(ctx context.Context, neighbours appSensing.GateNeighbours, fromSystem, toSystem string) (string, error) {
	if neighbours == nil {
		return "", fmt.Errorf("no stored gate adjacency is wired to name a next system toward %s", toSystem)
	}

	// via carries, for each system the search reaches, the FIRST-RING system the
	// walk would leave through to get there — which is the answer being looked
	// for. seen keeps the search from revisiting, so each system costs one read.
	type reached struct{ system, via string }
	seen := map[string]bool{fromSystem: true}
	frontier := []reached{{system: fromSystem}}

	// Non-positive routerHopBound means "until the component is exhausted" — the same
	// rule seed SELECTION applies, read from the same declaration, so a destination
	// selection accepts is always one this resolver can name a next system for.
	for ring := 0; (routerHopBound <= 0 || ring < routerHopBound) && len(frontier) > 0; ring++ {
		var next []reached
		for _, cur := range frontier {
			systems, err := neighbours.Neighbours(ctx, cur.system)
			if err != nil {
				return "", fmt.Errorf("failed to read the gate neighbours of %q: %w", cur.system, err)
			}
			for _, candidate := range systems {
				if seen[candidate] {
					continue
				}
				seen[candidate] = true
				via := cur.via
				if via == "" {
					// First ring: reaching this system IS the jump to make now.
					via = candidate
				}
				if candidate == toSystem {
					return via, nil
				}
				next = append(next, reached{system: candidate, via: via})
			}
		}
		frontier = next
	}
	return "", fmt.Errorf("no stored gate route from %s to %s within %d jumps", fromSystem, toSystem, routerHopBound)
}

// dispatchHop orbits a hull and hands it ONE in-system hop, returning as soon as
// the API has accepted the move. Shared by the placement mover and the charting
// seed's tour hop, which have the same contract for the same reason: both are
// driven by tick machines that re-read the ships table rather than wait.
func dispatchHop(ctx context.Context, med common.Mediator, pid shared.PlayerID, shipSymbol, destination string) error {
	mctx := sensingCtx(ctx)
	if _, err := med.Send(mctx, &shipTypes.OrbitShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   pid,
	}); err != nil {
		return fmt.Errorf("failed to orbit %s before sending it to %s: %w", shipSymbol, destination, err)
	}
	if _, err := med.Send(mctx, &shipTypes.NavigateDirectCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    pid,
	}); err != nil {
		return fmt.Errorf("failed to navigate %s to %s: %w", shipSymbol, destination, err)
	}
	return nil
}

// Dock docks a hull where it currently sits.
func (p *MoverPort) Dock(ctx context.Context, playerID int, shipSymbol string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	if _, err := p.mediator.Send(sensingCtx(ctx), &shipTypes.DockShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   pid,
	}); err != nil {
		return fmt.Errorf("failed to dock %s: %w", shipSymbol, err)
	}
	return nil
}

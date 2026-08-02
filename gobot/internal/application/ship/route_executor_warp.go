package ship

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ExecuteWarpLeg warps a ship to a single destination waypoint in ANOTHER system,
// off the jump-gate network, and charts that system on arrival. This is
// the clean, callable entrypoint slice B (off-gate target selection) and slice C
// (the explorer hull) invoke with a chosen target waypoint + ship.
//
// It fails closed when the ship has no warp drive, and refuses (without any warp
// API call) a destination the ship could never leave again. On success the ship
// ends physically IN the destination system and its gate cluster is charted for
// the frontier.
func (e *RouteExecutor) ExecuteWarpLeg(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	playerID shared.PlayerID,
) error {
	if err := e.ensureWarpCapable(ship); err != nil {
		return err
	}
	return e.executeWarpLeg(ctx, ship, destination, playerID)
}

// ExecuteWarpRoute executes an ordered sequence of warp legs, refueling between
// them. A far target off the gate network may be out of a single tank's
// range; slice B hands the ordered intermediate targets here and this drives each
// hop - checking onward viability, topping off at any waypoint that sells fuel
// before the next warp, and charting every system on arrival. The warp-drive check
// runs once up front; the first leg refused aborts the route with that leg's
// waypoints named, leaving the ship safely where it last arrived.
func (e *RouteExecutor) ExecuteWarpRoute(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destinations []*shared.Waypoint,
	playerID shared.PlayerID,
) error {
	if err := e.ensureWarpCapable(ship); err != nil {
		return err
	}
	for _, destination := range destinations {
		if err := e.executeWarpLeg(ctx, ship, destination, playerID); err != nil {
			return err
		}
	}
	return nil
}

// ensureWarpCapable fails closed unless warp support is wired AND the ship carries
// a warp drive. All three are hard preconditions: a missing navigator or a missing
// onward-viability reader is a wiring bug, a missing drive is an ineligible hull -
// either way no warp is attempted. The viability reader is checked here rather than
// treated as optional because a nil safety guard must refuse, never wave through.
func (e *RouteExecutor) ensureWarpCapable(ship *domainNavigation.Ship) error {
	if e.warpNavigator == nil {
		return fmt.Errorf("warp support not configured on this RouteExecutor (call WithWarpSupport)")
	}
	if e.warpEscapeReader == nil {
		return fmt.Errorf("warp onward-viability reader not configured on this RouteExecutor (call WithWarpSupport)")
	}
	// A warp with nowhere to RECORD where the hull went is the original defect stated as
	// a precondition: the move happens, the row keeps naming the origin, and every later
	// tick plans out of a system the hull has left. Refuse rather than fly a move we
	// cannot write down.
	if e.shipRepo == nil {
		return fmt.Errorf("warp cannot be recorded: no ship repository configured on this RouteExecutor")
	}
	if !ship.HasWarpDrive() {
		return &ErrShipHasNoWarpDrive{ShipSymbol: ship.ShipSymbol()}
	}
	return nil
}

// executeWarpLeg runs one guarded warp hop: refuse a destination the hull could
// never leave, top the tank off, orbit, warp (acting on the server's own fuel
// verdict if it refuses), settle the arrival, and chart the destination system.
// Assumes the caller has already verified warp capability.
func (e *RouteExecutor) executeWarpLeg(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	playerID shared.PlayerID,
) error {
	logger := common.LoggerFromContext(ctx)

	if err := e.ensureOnwardViability(ctx, ship, destination, playerID); err != nil {
		return err
	}

	if err := e.topOffBeforeWarp(ctx, ship, playerID); err != nil {
		return err
	}

	if err := e.ensureShipInOrbit(ctx, ship, playerID); err != nil {
		return err
	}

	logger.Log("INFO", "Executing warp leg", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "warp_leg",
		"from":        ship.CurrentLocation().Symbol,
		"to":          destination.Symbol,
	})

	result, err := e.warp(ctx, ship, destination, playerID)
	if err != nil {
		return err
	}

	if err := e.settleWarpArrival(ctx, ship, destination, result, playerID); err != nil {
		return err
	}

	e.chartOnArrival(ctx, ship, destination, playerID)
	return nil
}

// settleWarpArrival brings the ship to rest at the destination, folds the
// post-warp fuel state back onto the hull, and writes both through to the ships
// row.
//
// The fuel is adopted FIRST, before any write, so the departure record carries the
// tank the server actually left the hull with rather than the pre-warp figure. That
// direction is the safe one under RULINGS #4: a lower recorded fuel can only make a
// downstream range or strand check stricter, never looser.
func (e *RouteExecutor) settleWarpArrival(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	result *domainNavigation.Result,
	playerID shared.PlayerID,
) error {
	if err := adoptWarpFuel(ship, destination, result); err != nil {
		return err
	}
	return e.landShipAtDestination(ctx, ship, destination, result, playerID)
}

// adoptWarpFuel folds the server's post-warp tank onto the hull. A response carrying
// neither figure says nothing about fuel and is left alone.
func adoptWarpFuel(
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	result *domainNavigation.Result,
) error {
	if result.FuelCurrent == 0 && result.FuelCapacity == 0 {
		return nil
	}
	if err := ship.UpdateFuelFromAPI(result.FuelCurrent, result.FuelCapacity); err != nil {
		return fmt.Errorf("failed to update fuel after warp to %s: %w", destination.Symbol, err)
	}
	return nil
}

// landShipAtDestination settles the ship's location/nav-status after the warp AND
// records it durably. When the response carries no arrival time the ship has already
// arrived, so it lands directly; otherwise it really is IN_TRANSIT and waits out the
// transit via the same event path a navigate leg uses before coming to rest in orbit.
//
// THE DEPARTURE WRITE IS THE FIX. Until it existed, a warp mutated only the in-memory
// hull the caller happened to be holding and NOTHING reached the ships row: warp was
// the one cross-system mover with no durable write at all, so a hull that warped out
// of X1-GF41 into X1-KC84 left a row still naming X1-GF41 forever. Every later tick
// re-derived from that row (RULINGS #2), planned a jump to a gate adjacent to the
// system the hull had already left, and the API refused it (4255) — the TORWIND-41
// loop, faithfully correct from one wrong fact.
//
// It mirrors ShipRepository.Navigate, which persists navigateColumns the moment the
// navigate API returns. Recording the transit — not just the final rest — is
// load-bearing twice over: it is what makes the hull visible to ShipStateScheduler's
// stuck-ship sweeper (which selects on nav_status='IN_TRANSIT' AND arrival_time<=now)
// and what stops a daemon restart mid-warp from losing the hull entirely.
func (e *RouteExecutor) landShipAtDestination(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	result *domainNavigation.Result,
	playerID shared.PlayerID,
) error {
	if result.ArrivalTimeStr == "" {
		ship.SetLocation(destination)
		return e.persistWarpPosition(ctx, ship, playerID)
	}
	if err := ship.StartTransit(destination); err != nil {
		return fmt.Errorf("failed to enter warp transit to %s: %w", destination.Symbol, err)
	}
	if arrivalTime, err := time.Parse(time.RFC3339, result.ArrivalTimeStr); err == nil {
		ship.SetArrivalTime(arrivalTime)
	}
	if err := e.persistWarpPosition(ctx, ship, playerID); err != nil {
		return err
	}
	if err := e.waitForArrival(ctx, ship, result.ArrivalTimeStr, playerID); err != nil {
		return err
	}
	return e.settleWarpTransitEnd(ctx, ship, destination, playerID)
}

// settleWarpTransitEnd brings the hull to rest and records the landing.
//
// Unlike the departure write this one is best-effort, and the asymmetry is deliberate.
// A failed DEPARTURE write leaves the row wrong about WHERE the hull is, which nothing
// else reconciles — that is the original defect and it must fail closed. A failed
// ARRIVAL write leaves a row that is already correct about position and merely still
// says IN_TRANSIT; the stuck-ship sweeper transitions exactly that row within one
// sweep, so aborting a multi-leg route over a condition that self-heals in a minute
// would trade a real capability for no safety.
func (e *RouteExecutor) settleWarpTransitEnd(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	playerID shared.PlayerID,
) error {
	if ship.IsInTransit() {
		if err := ship.Arrive(); err != nil {
			return fmt.Errorf("failed to settle warp arrival at %s: %w", destination.Symbol, err)
		}
	}
	ship.ClearArrivalTime()
	if err := e.persistWarpPosition(ctx, ship, playerID); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", "Warp landed but the arrival state could not be recorded — the row already names the destination and the stuck-ship sweeper will settle it", map[string]interface{}{
			"action":      "warp_arrival_persist_failed",
			"ship_symbol": ship.ShipSymbol(),
			"waypoint":    destination.Symbol,
			"error":       err.Error(),
		})
	}
	return nil
}

// persistWarpPosition writes the position the SERVER has just put the hull at through to
// the ships row under CAS re-apply. The row — not the in-memory pointer this goroutine
// happens to hold — is what the next tick re-derives from (RULINGS #2), so it is the only
// place a completed move actually counts.
//
// The closure re-applies ONLY the columns this operation owns onto the FRESH row: where
// the server put the hull, the state it left it in, its transit clock and the tank it
// reported. A concurrent writer's cargo or assignment update on the same hull therefore
// survives instead of being clobbered by this goroutine's pre-warp snapshot — the same
// shape jump_ship and the arrival scheduler already write in.
//
// SetNavStatus rather than StartTransit/Arrive is deliberate: the server has ALREADY made
// the transition and this is a RECORDING of it, not a request for it. A guarded
// transition would refuse on a fresh row that had raced into some other state, turning a
// bookkeeping write into a failed warp for a hull that has physically moved.
func (e *RouteExecutor) persistWarpPosition(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
) error {
	location := ship.CurrentLocation()
	navStatus := ship.NavStatus()
	arrivalTime := ship.ArrivalTime()
	fuel := ship.Fuel()

	_, _, err := e.shipRepo.SaveWithRetry(ctx, ship.ShipSymbol(), playerID,
		func(fresh *domainNavigation.Ship) (bool, error) {
			fresh.SetLocation(location)
			fresh.SetNavStatus(navStatus)
			applyArrivalClock(fresh, arrivalTime)
			if err := applyReportedFuel(fresh, fuel); err != nil {
				return false, err
			}
			return true, nil
		})
	if err != nil {
		return fmt.Errorf("failed to record warp position of %s at %s: %w", ship.ShipSymbol(), location.Symbol, err)
	}
	return nil
}

// applyArrivalClock mirrors the hull's transit clock onto the fresh row: a hull in
// transit carries its ETA, a landed one carries none.
func applyArrivalClock(ship *domainNavigation.Ship, arrivalTime *time.Time) {
	if arrivalTime == nil {
		ship.ClearArrivalTime()
		return
	}
	ship.SetArrivalTime(*arrivalTime)
}

// applyReportedFuel mirrors the server-reported tank onto the fresh row.
func applyReportedFuel(ship *domainNavigation.Ship, fuel *shared.Fuel) error {
	if fuel == nil {
		return nil
	}
	return ship.UpdateFuelFromAPI(fuel.Current, fuel.Capacity)
}

// ensureOnwardViability is the strand-prevention guard, and the only warp check the
// executor still makes itself - because it is the only one the server does not.
// The API refuses a leg the hull cannot AFFORD, authoritatively and pre-flight; it
// says nothing about whether the far end has a way out. A destination with neither
// a fuel seller nor a built jump gate is a one-way trip, so it is refused before any
// warp call and the hull stays where it is.
//
// It fails CLOSED: an unreadable destination is refused exactly like a known dead
// end, because "we could not look" is not evidence of safety.
func (e *RouteExecutor) ensureOnwardViability(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	playerID shared.PlayerID,
) error {
	escape, err := e.warpEscapeReader.EscapeOptions(ctx, destination.SystemSymbol, playerID)
	if err != nil {
		return e.deadEnd(ship, destination, fmt.Sprintf("escape state unreadable: %v", err))
	}
	if !escape.IsDeadEnd() {
		return nil
	}
	return e.deadEnd(ship, destination, "no waypoint sells fuel and no built jump gate leads out")
}

// topOffBeforeWarp fills the tank at the origin when it sells fuel. This is ACTION,
// not prediction: the server will not refuel for you, and a warp is a one-way
// commitment, so the hull should leave on the fullest tank it can and arrive with
// the most onward range it can. It estimates no leg cost - a tank below capacity at
// a fuel stop is reason enough.
func (e *RouteExecutor) topOffBeforeWarp(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
) error {
	origin := ship.CurrentLocation()
	if !origin.HasFuel || ship.Fuel().Current >= ship.Fuel().Capacity {
		return nil
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Topping off before warp leg", map[string]interface{}{
		"ship_symbol":  ship.ShipSymbol(),
		"action":       "warp_refuel_before_leg",
		"waypoint":     origin.Symbol,
		"fuel_current": ship.Fuel().Current,
		"capacity":     ship.Fuel().Capacity,
	})
	// A warp leg follows and requires orbit, so return to orbit after topping off.
	if err := e.refuelShipWithRetry(ctx, ship, playerID, true); err != nil {
		return fmt.Errorf("warp top-off at %s failed: %w", origin.Symbol, err)
	}
	return nil
}

// warp issues the leg and treats the server's refusal as the authority on fuel. A
// 4203 is a pre-flight verdict carrying the real numbers, so it is either ACTED on -
// refuel to the stated requirement and retry EXACTLY once - or TERMINAL. Any other
// failure is surfaced verbatim. Nothing here is retried blindly, so a refused warp
// can never burn the container's restart budget.
func (e *RouteExecutor) warp(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	playerID shared.PlayerID,
) (*domainNavigation.Result, error) {
	result, err := e.warpNavigator.Warp(ctx, ship, destination, playerID)
	if err == nil {
		return result, nil
	}

	refusal, isFuelRefusal := parseWarpFuelRefusal(err)
	if !isFuelRefusal {
		return nil, fmt.Errorf("warp to %s failed: %w", destination.Symbol, err)
	}
	if err := e.satisfyWarpFuelRefusal(ctx, ship, destination, playerID, refusal); err != nil {
		return nil, err
	}

	result, err = e.warpNavigator.Warp(ctx, ship, destination, playerID)
	if err != nil {
		return nil, fmt.Errorf("warp to %s failed after refueling to the server's stated requirement: %w", destination.Symbol, err)
	}
	return result, nil
}

// satisfyWarpFuelRefusal raises the tank to the fuel level the SERVER said the leg
// needs. It terminates with a typed strand refusal - rather than retrying - whenever
// that requirement cannot be met, so the same fail-closed verdicts the old local
// guard rendered are still rendered, now off the authoritative number:
//
//   - requirement exceeds a full tank      -> refuse (unreachable even topped off)
//   - origin has no fuel stop              -> refuse (cannot top off, would strand)
//   - still short after refueling          -> refuse (would strand)
func (e *RouteExecutor) satisfyWarpFuelRefusal(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	playerID shared.PlayerID,
	refusal warpFuelRefusal,
) error {
	origin := ship.CurrentLocation()
	capacity := ship.Fuel().Capacity

	if refusal.required > capacity {
		return e.strand(ship, origin, destination, refusal.required, capacity, "server refused: leg costs more than a full tank")
	}
	if !origin.HasFuel {
		return e.strand(ship, origin, destination, refusal.required, capacity, "server refused: insufficient fuel and no fuel station at origin to refuel")
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Server refused warp for fuel - refueling to its stated requirement and retrying once", map[string]interface{}{
		"ship_symbol":  ship.ShipSymbol(),
		"action":       "warp_refuel_after_refusal",
		"waypoint":     origin.Symbol,
		"required":     refusal.required,
		"fuel_current": ship.Fuel().Current,
	})
	// A warp retry follows and requires orbit, so return to orbit after refueling.
	if err := e.refuelShipWithRetry(ctx, ship, playerID, true); err != nil {
		return fmt.Errorf("warp refuel at %s after the server's fuel refusal failed: %w", origin.Symbol, err)
	}

	if ship.Fuel().Current < refusal.required {
		return e.strand(ship, origin, destination, refusal.required, capacity, "server refused: still insufficient fuel after refueling")
	}
	return nil
}

// chartOnArrival delegates charting of the just-reached system to the SystemCharter
// (gate edges + waypoints + markets + shipyards), mirroring how the gate-nav path
// delegates market scanning. Best-effort: charting is never allowed to fail a warp
// that has already physically landed the ship in the new system.
func (e *RouteExecutor) chartOnArrival(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	playerID shared.PlayerID,
) {
	if e.systemCharter == nil {
		return
	}
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Charting destination system on warp arrival", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "warp_chart_on_arrival",
		"system":      destination.SystemSymbol,
		"waypoint":    destination.Symbol,
	})
	if err := e.systemCharter.ChartSystem(ctx, destination.SystemSymbol, playerID); err != nil {
		logger.Log("WARNING", "Chart-on-arrival failed (non-fatal to warp)", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "warp_chart_on_arrival",
			"system":      destination.SystemSymbol,
			"error":       err.Error(),
		})
	}
}

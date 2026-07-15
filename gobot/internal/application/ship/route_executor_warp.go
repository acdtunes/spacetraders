package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ErrShipHasNoWarpDrive is returned (fail-closed) when a warp is requested for a
// ship with no MODULE_WARP_DRIVE_* installed (sp-0xd0). Only a SHIP_EXPLORER
// carries the drive; refusing here keeps the executor from emitting a warp the
// live API would reject, and gives slice B/C a typed signal to pick a warp-capable
// hull instead.
type ErrShipHasNoWarpDrive struct {
	ShipSymbol string
}

func (e *ErrShipHasNoWarpDrive) Error() string {
	return fmt.Sprintf("ship %s cannot warp: no warp drive module installed", e.ShipSymbol)
}

// ErrWarpWouldStrand is the fuel-safety refusal (sp-0xd0). It is returned BEFORE
// any warp API call whenever executing a leg would leave the ship unable to reach
// the destination - either because the leg costs more than a full tank, or because
// the ship holds too little fuel and there is no fuel station at the origin to top
// off. Refusing (rather than warping into a strand) is the key safety property of
// this slice; the typed fields let a caller report exactly why a target is
// unreachable.
type ErrWarpWouldStrand struct {
	ShipSymbol string
	From       string
	To         string
	Required   int
	Available  int
	Capacity   int
	Reason     string
}

func (e *ErrWarpWouldStrand) Error() string {
	return fmt.Sprintf(
		"refusing warp %s -> %s for ship %s: would strand (%s); required %d fuel, available %d, capacity %d",
		e.From, e.To, e.ShipSymbol, e.Reason, e.Required, e.Available, e.Capacity,
	)
}

// warpFuelCost is the fuel a warp leg consumes over distance. Warp is fuel-costed
// by inter-system distance at the CRUISE rate (1 fuel per distance unit, floored
// at 1 for any non-zero leg) - the same baseline navigate uses - so the executor
// can compute strand-risk locally without a round-trip to the API.
func warpFuelCost(distance float64) int {
	return shared.FlightModeCruise.FuelCost(distance)
}

// ExecuteWarpLeg warps a ship to a single destination waypoint in ANOTHER system,
// off the jump-gate network, and charts that system on arrival (sp-0xd0). This is
// the clean, callable entrypoint slice B (off-gate target selection) and slice C
// (the explorer hull) invoke with a chosen target waypoint + ship.
//
// It fails closed when the ship has no warp drive, and refuses (without any warp
// API call) a leg that would strand the ship. On success the ship ends physically
// IN the destination system and its gate cluster is charted for the frontier.
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
// them (sp-0xd0). A far target off the gate network may be out of a single tank's
// range; slice B hands the ordered intermediate targets here and this drives each
// hop - guarding fuel safety, topping off at any waypoint that sells fuel before
// the next warp, and charting every system on arrival. The warp-drive check runs
// once up front; the first leg that would strand aborts the route with that leg's
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
// a warp drive. Both are hard preconditions: a missing navigator is a wiring bug,
// a missing drive is an ineligible hull - either way no warp is attempted.
func (e *RouteExecutor) ensureWarpCapable(ship *domainNavigation.Ship) error {
	if e.warpNavigator == nil {
		return fmt.Errorf("warp support not configured on this RouteExecutor (call WithWarpSupport)")
	}
	if !ship.HasWarpDrive() {
		return &ErrShipHasNoWarpDrive{ShipSymbol: ship.ShipSymbol()}
	}
	return nil
}

// executeWarpLeg runs one guarded warp hop: enforce fuel safety (refusing a strand
// before touching the API), orbit, warp, settle the arrival, and chart the
// destination system. Assumes the caller has already verified warp capability.
func (e *RouteExecutor) executeWarpLeg(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	playerID shared.PlayerID,
) error {
	logger := common.LoggerFromContext(ctx)

	if err := e.ensureWarpFuelSafety(ctx, ship, destination, playerID); err != nil {
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

	result, err := e.warpNavigator.Warp(ctx, ship, destination, playerID)
	if err != nil {
		return fmt.Errorf("warp to %s failed: %w", destination.Symbol, err)
	}

	if err := e.settleWarpArrival(ctx, ship, destination, result, playerID); err != nil {
		return err
	}

	e.chartOnArrival(ctx, ship, destination, playerID)
	return nil
}

// settleWarpArrival brings the ship to rest at the destination and folds the
// post-warp fuel state back onto the hull.
func (e *RouteExecutor) settleWarpArrival(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	result *domainNavigation.Result,
	playerID shared.PlayerID,
) error {
	if err := e.landShipAtDestination(ctx, ship, destination, result, playerID); err != nil {
		return err
	}
	if result.FuelCurrent == 0 && result.FuelCapacity == 0 {
		return nil
	}
	if err := ship.UpdateFuelFromAPI(result.FuelCurrent, result.FuelCapacity); err != nil {
		return fmt.Errorf("failed to update fuel after warp to %s: %w", destination.Symbol, err)
	}
	return nil
}

// landShipAtDestination settles the ship's location/nav-status after the warp.
// When the response carries no arrival time the ship has already arrived, so it
// lands directly; otherwise it really is IN_TRANSIT and waits out the transit via
// the same event path a navigate leg uses before coming to rest in orbit.
func (e *RouteExecutor) landShipAtDestination(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	result *domainNavigation.Result,
	playerID shared.PlayerID,
) error {
	if result.ArrivalTimeStr == "" {
		ship.SetLocation(destination)
		return nil
	}
	if err := ship.StartTransit(destination); err != nil {
		return fmt.Errorf("failed to enter warp transit to %s: %w", destination.Symbol, err)
	}
	if err := e.waitForArrival(ctx, ship, result.ArrivalTimeStr, playerID); err != nil {
		return err
	}
	if !ship.IsInTransit() {
		return nil
	}
	if err := ship.Arrive(); err != nil {
		return fmt.Errorf("failed to settle warp arrival at %s: %w", destination.Symbol, err)
	}
	return nil
}

// ensureWarpFuelSafety is the strand-prevention guard - the key safety property of
// this slice. It computes the leg's fuel cost from inter-system distance and, when
// the ship cannot afford it, refuses OR tops off first, but NEVER lets a leg run
// that would leave the ship stranded:
//
//   - cost exceeds a full tank            -> refuse (unreachable even topped off)
//   - already have enough fuel            -> proceed
//   - short, and origin sells fuel        -> refuel to full, then re-verify
//   - short, and origin has no fuel stop  -> refuse (cannot top off, would strand)
//
// The refusal is returned BEFORE any warp API call, so a doomed leg costs zero API
// budget and the ship stays safely where it is.
func (e *RouteExecutor) ensureWarpFuelSafety(
	ctx context.Context,
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	playerID shared.PlayerID,
) error {
	origin := ship.CurrentLocation()
	distance := origin.DistanceTo(destination)
	required := warpFuelCost(distance)
	capacity := ship.Fuel().Capacity

	if required > capacity {
		return e.strand(ship, origin, destination, required, capacity, "leg costs more than a full tank")
	}

	if ship.Fuel().Current >= required {
		return nil
	}

	if !origin.HasFuel {
		return e.strand(ship, origin, destination, required, capacity, "insufficient fuel and no fuel station at origin to refuel")
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Warp fuel-safety: topping off before leg", map[string]interface{}{
		"ship_symbol":  ship.ShipSymbol(),
		"action":       "warp_refuel_before_leg",
		"waypoint":     origin.Symbol,
		"required":     required,
		"fuel_current": ship.Fuel().Current,
	})
	if err := e.refuelShipWithRetry(ctx, ship, playerID); err != nil {
		return fmt.Errorf("warp fuel-safety refuel at %s failed: %w", origin.Symbol, err)
	}

	if ship.Fuel().Current < required {
		return e.strand(ship, origin, destination, required, capacity, "still insufficient fuel after refueling to capacity")
	}
	return nil
}

// strand builds the typed refusal, capturing the ship's fuel state at the moment
// the leg was refused so a caller can report exactly why the target is unreachable.
func (e *RouteExecutor) strand(
	ship *domainNavigation.Ship,
	origin, destination *shared.Waypoint,
	required, capacity int,
	reason string,
) error {
	return &ErrWarpWouldStrand{
		ShipSymbol: ship.ShipSymbol(),
		From:       origin.Symbol,
		To:         destination.Symbol,
		Required:   required,
		Available:  ship.Fuel().Current,
		Capacity:   capacity,
		Reason:     reason,
	}
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

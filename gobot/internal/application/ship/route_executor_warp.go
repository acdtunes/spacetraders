package ship

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
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

// ErrWarpWouldStrand is the fuel-safety refusal. Its Required/Available figures
// are the SERVER's own, taken from the pre-flight refusal it returns for a leg the
// hull cannot afford; the executor never prices a warp itself. It is returned when
// that refusal cannot be satisfied - the leg costs more than a full tank, or the
// origin has no fuel to top off with - so a doomed leg terminates on the spot
// instead of being retried blindly. The typed fields let a caller report exactly
// why a target is unreachable.
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

// ErrWarpDeadEnd is the onward-viability refusal: the destination system has no way
// back out for the arriving hull. It is the one strand the server cannot prevent -
// it validates that a hull can AFFORD a leg, never that the far end has an exit -
// so this refusal is made locally, before any warp call, and fails CLOSED: a
// destination whose escape state cannot be read is refused exactly like one known
// to be a dead end.
type ErrWarpDeadEnd struct {
	ShipSymbol string
	From       string
	To         string
	System     string
	Reason     string
}

func (e *ErrWarpDeadEnd) Error() string {
	return fmt.Sprintf(
		"refusing warp %s -> %s for ship %s: %s is a dead end (%s)",
		e.From, e.To, e.ShipSymbol, e.System, e.Reason,
	)
}

// warpInsufficientFuelCode is the API's pre-flight verdict that a leg costs more
// fuel than the hull holds. The refusal happens BEFORE the hull moves and carries
// the server's own fuelRequired/fuelAvailable - the authoritative numbers, from the
// entity that enforces the rule.
const warpInsufficientFuelCode = 4203

// ExecuteWarpLeg warps a ship to a single destination waypoint in ANOTHER system,
// off the jump-gate network, and charts that system on arrival (sp-0xd0). This is
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
// them (sp-0xd0). A far target off the gate network may be out of a single tank's
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

// warpFuelRefusal is the server's pre-flight verdict on a leg's fuel: what it costs
// and what the hull holds, both measured by the entity that enforces the rule.
type warpFuelRefusal struct {
	required  int
	available int
}

// parseWarpFuelRefusal extracts the server's numbers from a 4203 refusal.
// ok=false for every other failure - INCLUDING a refusal whose body cannot be read -
// so an error the executor does not understand is surfaced as-is and never turned
// into a speculative refuel-and-retry.
func parseWarpFuelRefusal(err error) (warpFuelRefusal, bool) {
	var apiErr *domainPorts.APIError
	if !errors.As(err, &apiErr) {
		return warpFuelRefusal{}, false
	}
	var body struct {
		Error struct {
			Code int `json:"code"`
			Data struct {
				FuelRequired  int `json:"fuelRequired"`
				FuelAvailable int `json:"fuelAvailable"`
			} `json:"data"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(apiErr.Body), &body) != nil {
		return warpFuelRefusal{}, false
	}
	if body.Error.Code != warpInsufficientFuelCode || body.Error.Data.FuelRequired <= 0 {
		return warpFuelRefusal{}, false
	}
	return warpFuelRefusal{
		required:  body.Error.Data.FuelRequired,
		available: body.Error.Data.FuelAvailable,
	}, true
}

// deadEnd builds the typed onward-viability refusal, naming the system the hull
// would not have been able to leave so a caller can report the target as a one-way
// trip rather than a fuel problem.
func (e *RouteExecutor) deadEnd(
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	reason string,
) error {
	return &ErrWarpDeadEnd{
		ShipSymbol: ship.ShipSymbol(),
		From:       ship.CurrentLocation().Symbol,
		To:         destination.Symbol,
		System:     destination.SystemSymbol,
		Reason:     reason,
	}
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

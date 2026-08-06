package ship

import (
	"context"
	"fmt"

	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// SlowRefuelThreshold is how long the dock/refuel/orbit trio may block before it is reported.
//
// THE TRIO IS THE ONE PLACE A CONTRACT WORKER CAN GO QUIET FOR MINUTES. Each of the three is a
// bare mediator send with no per-attempt logging of its own, while the API client retries
// underneath it (staging: 3 attempts, 1s base backoff, a 30s timeout per request) — so an upstream
// 500 storm turns one refuel into minutes of wall clock that emit NOTHING at container level. The
// worker keeps heartbeating throughout, so nothing else notices either: sp-ehf4x was diagnosed by
// counting log lines per minute by hand.
//
// Contrast the retrying path, refuelShipWithRetryCore, which logs every attempt and is therefore
// perfectly visible. Same failure, two call paths, and only one of them could be seen. This closes
// that asymmetry for every caller rather than only the alternate-fuel-stop reroute where it was
// observed.
//
// 30s is above any healthy refuel (three sequential calls against a responsive API) and well under
// the multi-minute windows actually observed (3m15s and 3m20s), so a quiet fleet stays quiet.
const SlowRefuelThreshold = 30 * time.Second

// reportSlowRefuel logs a refuel that blocked past SlowRefuelThreshold, whatever its outcome.
//
// DEFERRED, so it covers the ERROR paths too — and that is the point, because the observed incident
// spent 3m15s in here and then FAILED. A report only on success would have stayed silent through
// exactly the case it exists to surface. The stable `refuel_slow` action is what makes this
// greppable and alertable without reading cadence by hand; a Prometheus counter belongs with the
// other refuel-anomaly metrics under sp-az5a5 rather than as a second mechanism here.
func (e *RouteExecutor) reportSlowRefuel(logger common.ContainerLogger, ship *domainNavigation.Ship, waypoint string, startedAt time.Time) {
	elapsed := e.clock.Now().Sub(startedAt)
	if elapsed < SlowRefuelThreshold {
		return
	}
	logger.Log("WARNING", fmt.Sprintf("Refuel at %s blocked for %s with no output of its own - the API client was retrying underneath it", waypoint, elapsed.Round(time.Second)), map[string]interface{}{
		"ship_symbol":     ship.ShipSymbol(),
		"action":          "refuel_slow",
		"waypoint":        waypoint,
		"elapsed_seconds": int(elapsed.Seconds()),
		"threshold_secs":  int(SlowRefuelThreshold.Seconds()),
	})
}

func (e *RouteExecutor) handlePreDepartureRefuel(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID) error {
	logger := common.LoggerFromContext(ctx)
	if e.refuelStrategy.ShouldRefuelBeforeDeparture(ship, segment) {
		logger.Log("INFO", "Ship refueling before departure", map[string]interface{}{
			"ship_symbol":     ship.ShipSymbol(),
			"action":          "pre_departure_refuel",
			"waypoint":        segment.FromWaypoint.Symbol,
			"reason":          "strategy_decision",
			"refuel_strategy": e.refuelStrategy.GetStrategyName(),
		})
		// A navigate follows this refuel, so return to orbit.
		if err := e.refuelShipWithRetry(ctx, ship, playerID, true); err != nil {
			return err
		}
	}
	return nil
}

func (e *RouteExecutor) handlePostArrivalRefueling(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID) error {
	logger := common.LoggerFromContext(ctx)

	// Check for opportunistic refueling (strategy-based)
	if e.refuelStrategy.ShouldRefuelAfterArrival(ship, segment) {
		logger.Log("INFO", "Ship performing opportunistic refuel", map[string]interface{}{
			"ship_symbol":     ship.ShipSymbol(),
			"action":          "opportunistic_refuel",
			"waypoint":        segment.ToWaypoint.Symbol,
			"refuel_strategy": e.refuelStrategy.GetStrategyName(),
		})
		// CUT 2: stay docked after a post-arrival refuel. The next
		// action at this waypoint is a trade that docks; staying docked makes
		// that dock a CUT-1 no-op skip. A following segment re-orbits via
		// ensureShipInOrbit, so this is never a wrong state for a later navigate.
		if err := e.refuelShipWithRetry(ctx, ship, playerID, false); err != nil {
			return err
		}
	}

	// Always honor planned refuels from routing engine
	if segment.RequiresRefuel {
		logger.Log("INFO", "Ship performing planned refuel", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "planned_refuel",
			"waypoint":    segment.ToWaypoint.Symbol,
		})
		// CUT 2: stay docked after a post-arrival refuel (see above).
		if err := e.refuelShipWithRetry(ctx, ship, playerID, false); err != nil {
			return err
		}
	}

	return nil
}

// refuelBeforeDeparture refuels ship before starting the journey, retrying a
// transient failure with backoff and rerouting to an alternate fuel-capable
// waypoint if needed. Delegates to refuelShipWithRetry so both entry points share
// the same retry/reroute behavior.
func (e *RouteExecutor) refuelBeforeDeparture(
	ctx context.Context,
	route *domainNavigation.Route,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
) error {
	// CUT 3: skip the whole dock/refuel/orbit trio when the ship
	// already holds enough fuel for the first leg plus the safety margin. Reuses
	// the same fuel-cost primitive as ensureAffordableFlightMode rather than
	// inventing a new threshold, so a ship that can already make the next leg does
	// not pay three redundant API verbs.
	if e.hasSufficientFuelForFirstLeg(route, ship) {
		common.LoggerFromContext(ctx).Log("INFO", "Skipping pre-departure refuel - sufficient fuel for first leg", map[string]interface{}{
			"ship_symbol":  ship.ShipSymbol(),
			"action":       "pre_departure_refuel_skipped",
			"fuel_current": ship.Fuel().Current,
		})
		return nil
	}
	// A navigate (the first segment) follows, so return to orbit.
	return e.refuelShipWithRetry(ctx, ship, playerID, true)
}

// hasSufficientFuelForFirstLeg reports whether the ship can already fly the
// route's first leg with the safety margin intact — the CUT 3 skip predicate.
//
// It is deliberately CONSERVATIVE: it reuses the exact FuelCost primitive the
// affordability guard uses (segment.FlightMode.FuelCost over the leg distance)
// plus DefaultFuelSafetyMargin, and returns false (i.e. DO refuel) on any
// uncertainty — a nil segment. A zero-capacity ship (probe) never consumes fuel,
// so it is always "sufficient". A wrong true here would strand a ship, so the
// margin buffer and the fail-safe-to-refuel default are load-bearing safety.
func (e *RouteExecutor) hasSufficientFuelForFirstLeg(route *domainNavigation.Route, ship *domainNavigation.Ship) bool {
	if ship.Fuel().Capacity == 0 {
		return true
	}
	segment := route.NextSegment()
	if segment == nil {
		return false
	}
	distance := segment.FromWaypoint.DistanceTo(segment.ToWaypoint)
	required := segment.FlightMode.FuelCost(distance) + domainNavigation.DefaultFuelSafetyMargin
	return ship.Fuel().Current >= required
}

// refuelShip refuels ship at current location.
//
// returnToOrbit controls the final transition (CUT 2). When true the
// ship is returned to orbit after refuelling — the correct choice when a
// navigate immediately follows (pre-departure / affordability backstop /
// alternate-stop reroute), since a navigate requires orbit. When false the ship
// STAYS DOCKED — the correct choice after a post-arrival refuel, because the
// very next action at the same waypoint is a trade that docks: leaving the ship
// docked turns that trade's DockShipCommand into a CUT-1 no-op skip and drops
// one orbit + one dock per stop. A subsequent segment (if any) re-orbits via
// ensureShipInOrbit, so staying docked is never left in a wrong state for a
// following navigate.
func (e *RouteExecutor) refuelShip(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
	returnToOrbit bool,
) error {
	logger := common.LoggerFromContext(ctx)

	// sp-l7zha: a tank that is already at capacity has nothing to buy, so the
	// whole dock/refuel/orbit trio is skipped before it reaches the API. The
	// incident hull read 600/600 locally (and an impossible 729/600 live) and
	// still docked, called refuel, ate a transient 500, retried, and rerouted
	// 144 units across the system in DRIFT for fuel it did not need. Placed
	// alongside the no-fuel-station skip below because it is the same shape of
	// precondition: a refuel that cannot accomplish anything is not attempted.
	//
	// This deliberately reads the LOCAL tank, which is the same signal
	// hasSufficientFuelForFirstLeg and the routing engine already plan against;
	// it introduces no new trust. A zero-capacity hull (a probe) satisfies 0>=0
	// and skips, which is correct - it can hold no fuel at all.
	if fuel := ship.Fuel(); fuel != nil && fuel.Current >= fuel.Capacity {
		logger.Log("INFO", "Ship already at fuel capacity - skipping refuel", map[string]interface{}{
			"ship_symbol":   ship.ShipSymbol(),
			"action":        "refuel_skipped",
			"waypoint":      ship.CurrentLocation().Symbol,
			"reason":        "tank_full",
			"fuel_current":  fuel.Current,
			"fuel_capacity": fuel.Capacity,
		})
		return nil
	}

	// GRACEFUL DEGRADATION: Skip refuel if current location has no fuel station
	// This handles stale waypoint cache data or routing service errors
	if !ship.CurrentLocation().HasFuel {
		logger.Log("WARNING", "Ship cannot refuel - no fuel station at current location", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "refuel_skipped",
			"waypoint":    ship.CurrentLocation().Symbol,
			"reason":      "no_fuel_station",
		})
		return nil // Skip refuel gracefully
	}

	// Everything below is API work that can block silently under an upstream 500 storm — see
	// SlowRefuelThreshold. Registered after the two precondition skips as a matter of scope, not of
	// behaviour: those skips are pure local reads that return instantly, so timing them would
	// report nothing anyway. Placing it here keeps the measurement about API work should a
	// precondition ever grow one. There is deliberately no test for the placement — a skip cannot
	// consume time, so no fixture can make a hoisted timer misreport, and a test asserting it would
	// pass against either version.
	defer e.reportSlowRefuel(logger, ship, ship.CurrentLocation().Symbol, e.clock.Now())

	dockCmd := &types.DockShipCommand{
		Ship:     ship,
		PlayerID: playerID,
	}
	if _, err := e.mediator.Send(ctx, dockCmd); err != nil {
		err = e.retryOnceAfterAdoptedTransit(ctx, ship, playerID, err, func() error {
			_, retryErr := e.mediator.Send(ctx, dockCmd)
			return retryErr
		})
		if err != nil {
			return fmt.Errorf("failed to dock for refuel: %w", err)
		}
	}

	refuelCmd := &types.RefuelShipCommand{
		Ship:     ship,
		PlayerID: playerID,
		Units:    nil, // Full refuel
	}
	if _, err := e.mediator.Send(ctx, refuelCmd); err != nil {
		return fmt.Errorf("failed to refuel: %w", err)
	}

	// CUT 2: only return to orbit when a navigate follows. When a trade at the
	// same waypoint follows we deliberately stay docked (see doc comment).
	if !returnToOrbit {
		return nil
	}

	orbitCmd := &types.OrbitShipCommand{
		Ship:     ship,
		PlayerID: playerID,
	}
	if _, err := e.mediator.Send(ctx, orbitCmd); err != nil {
		return fmt.Errorf("failed to orbit after refuel: %w", err)
	}

	return nil
}

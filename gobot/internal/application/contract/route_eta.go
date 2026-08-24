package contract

import (
	"context"
	"errors"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// ETAResult is what EstimateAll produces: a per-candidate route ETA the
// hull selector ranks on in place of straight-line distance. OK=false tells
// the caller the whole batch is untrustworthy - fall back to straight-line
// for every candidate rather than trust a partial, possibly-skewed map.
type ETAResult struct {
	ETAs    map[string]float64 // symbol -> total seconds (remaining transit + planned route)
	Dropped []string           // genuinely unroutable candidates - excluded from selection
	OK      bool               // false => caller must fall back to straight-line for ALL candidates
	// Cause names why OK is false: "budget_exceeded" (this estimator's own budget expired),
	// "transport_error" (the routing client failed independent of the budget), or
	// "all_candidates_unroutable" (every candidate individually failed to route). Best-effort -
	// under a genuine race between two failure kinds this names whichever was observed first.
	// Empty when OK is true.
	Cause string
}

// routeETABudget bounds EstimateAll's wall-clock cost so a slow or wedged
// routing service can never stall a dispatch decision - a timeout there
// degrades the whole batch to OK=false rather than hang the caller.
const routeETABudget = 2 * time.Second

// RouteETAEstimator prices sourcing candidates on the fuel-aware planner the
// daemon already flies routes with, so selection ranks on the same arrival
// times execution will realize. Fail-open by contract: every failure path
// resolves to OK=false (caller keeps the straight-line ranking) or a dropped
// candidate - never a blocked dispatch.
type RouteETAEstimator struct {
	client routing.RoutingClient
	clock  shared.Clock
	budget time.Duration
}

// NewRouteETAEstimator wires the estimator to the routing port already used
// for real dispatch, so ranking and execution price routes identically.
func NewRouteETAEstimator(client routing.RoutingClient, clock shared.Clock) *RouteETAEstimator {
	return &RouteETAEstimator{client: client, clock: clock, budget: routeETABudget}
}

// EstimateAll prices one route per ship in parallel and returns as soon as
// every call answers or the budget expires, whichever comes first. A nil
// receiver/client/clock or an empty fleet answers OK=false immediately rather
// than doing any work - every exit is fail-open, never an error the caller
// must handle specially. The nil-clock case guards e.clock.Now() below, the
// one call in this file that would otherwise panic on a zero-value estimator.
func (e *RouteETAEstimator) EstimateAll(ctx context.Context, ships []*navigation.Ship, systemSymbol, goalWaypoint string, waypoints map[string]*shared.Waypoint) ETAResult {
	if e == nil || e.client == nil || e.clock == nil || len(ships) == 0 {
		return ETAResult{OK: false}
	}
	ctx, cancel := context.WithTimeout(ctx, e.budget)
	defer cancel()

	waypointData := make([]*system.WaypointData, 0, len(waypoints))
	for _, wp := range waypoints {
		waypointData = append(waypointData, &system.WaypointData{Symbol: wp.Symbol, X: wp.X, Y: wp.Y, HasFuel: wp.HasFuel})
	}

	type answer struct {
		symbol string
		eta    float64
		drop   bool
		global bool
		cause  string // set alongside global - see ETAResult.Cause
	}

	result := ETAResult{ETAs: make(map[string]float64, len(ships)), OK: true}

	// A nil ship can't yield a symbol - ShipSymbol() dereferences too - so it
	// is dropped right here, before any goroutine spawns, instead of inside
	// one. Only a routable ship gets a goroutine, so every spawned goroutine
	// still sends exactly once and the receive loop below stays in lockstep.
	routable := make([]*navigation.Ship, 0, len(ships))
	for _, ship := range ships {
		if ship == nil {
			result.Dropped = append(result.Dropped, "<nil-ship>")
			continue
		}
		routable = append(routable, ship)
	}

	answers := make(chan answer, len(routable))
	now := e.clock.Now()

	for _, ship := range routable {
		go func(ship *navigation.Ship) {
			transitRemainder := 0.0
			if ship.IsInTransit() {
				at := ship.ArrivalTime()
				if at == nil {
					answers <- answer{symbol: ship.ShipSymbol(), drop: true}
					return
				}
				if rem := at.Sub(now).Seconds(); rem > 0 {
					transitRemainder = rem
				}
			}
			loc := ship.CurrentLocation()
			if loc == nil {
				answers <- answer{symbol: ship.ShipSymbol(), drop: true}
				return
			}
			// Fuel is deducted at departure in this game, so an in-transit
			// hull's current fuel already IS its arrival fuel - no adjustment.
			resp, err := e.client.PlanRoute(ctx, &routing.RouteRequest{
				SystemSymbol:  systemSymbol,
				StartWaypoint: loc.Symbol, // invariant: destination while in transit
				GoalWaypoint:  goalWaypoint,
				CurrentFuel:   ship.Fuel().Current,
				FuelCapacity:  ship.FuelCapacity(),
				EngineSpeed:   ship.EngineSpeed(),
				Waypoints:     waypointData,
				PreferCruise:  false,
			})
			if err != nil {
				switch {
				case ctx.Err() != nil:
					// OUR OWN budget (or the caller's ctx) has already expired - checked first
					// because a client that itself honors ctx will often return a
					// context.DeadlineExceeded-shaped error once that happens, and that shape
					// must not be mistaken for an independent transport-class failure below.
					answers <- answer{symbol: ship.ShipSymbol(), global: true, cause: "budget_exceeded"}
				case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
					// The client failed with a deadline/cancellation shape on its own, while OUR
					// ctx is still healthy - a downstream timeout or cancellation, not our budget.
					answers <- answer{symbol: ship.ShipSymbol(), global: true, cause: "transport_error"}
				default:
					answers <- answer{symbol: ship.ShipSymbol(), drop: true} // unroutable: this hull only
				}
				return
			}
			answers <- answer{symbol: ship.ShipSymbol(), eta: transitRemainder + float64(resp.TotalTimeSeconds)}
		}(ship)
	}

	for range routable {
		a := <-answers
		switch {
		case a.global:
			result.OK = false
			if result.Cause == "" {
				result.Cause = a.cause
			}
		case a.drop:
			result.Dropped = append(result.Dropped, a.symbol)
		default:
			result.ETAs[a.symbol] = a.eta
		}
	}
	if len(result.ETAs) == 0 {
		result.OK = false
		if result.Cause == "" {
			result.Cause = "all_candidates_unroutable"
		}
	}
	return result
}

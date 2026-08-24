package contract

import (
	"context"
	"errors"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
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
	// Elapsed is the batch's measured wall time, so a caller logging a fallback reports
	// how much of the budget the routing service consumed, not merely that it was hit.
	Elapsed time.Duration
}

// DefaultRouteETABudget bounds EstimateAll's wall-clock cost when the operator has
// configured none, so a slow or wedged routing service degrades the batch to OK=false
// rather than stalling a dispatch decision. The routing service prices one candidate at a
// time, so a batch costs the candidate count times one route and a budget sized for a
// small contract fleet expires on a larger one; this one covers the fleet at the delivery
// saturation the contract scaler grows toward. Selection runs once per contract cycle, so
// that headroom costs nothing operationally.
const DefaultRouteETABudget = 6 * time.Second

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
// for real dispatch, so ranking and execution price routes identically. A non-positive
// budget takes DefaultRouteETABudget - a zero budget would expire before the first route
// answers and demote every selection to straight-line ranking.
func NewRouteETAEstimator(client routing.RoutingClient, clock shared.Clock, budget time.Duration) *RouteETAEstimator {
	if budget <= 0 {
		budget = DefaultRouteETABudget
	}
	return &RouteETAEstimator{client: client, clock: clock, budget: budget}
}

// Budget reports the wall-clock ceiling EstimateAll runs under so a caller logging a
// fallback can name the bound that was hit. Nil-safe: the selector's estimator is optional.
func (e *RouteETAEstimator) Budget() time.Duration {
	if e == nil {
		return 0
	}
	return e.budget
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
		// routeCall is this candidate's PlanRoute duration, carried back rather than logged
		// in the goroutine so batch timing is reported off the concurrent path.
		routeCall time.Duration
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
			callStart := e.clock.Now()
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
			routeCall := e.clock.Now().Sub(callStart)
			if err != nil {
				switch {
				case ctx.Err() != nil:
					// OUR OWN budget (or the caller's ctx) has already expired - checked first
					// because a client that itself honors ctx will often return a
					// context.DeadlineExceeded-shaped error once that happens, and that shape
					// must not be mistaken for an independent transport-class failure below.
					answers <- answer{symbol: ship.ShipSymbol(), global: true, cause: "budget_exceeded", routeCall: routeCall}
				case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
					// The client failed with a deadline/cancellation shape on its own, while OUR
					// ctx is still healthy - a downstream timeout or cancellation, not our budget.
					answers <- answer{symbol: ship.ShipSymbol(), global: true, cause: "transport_error", routeCall: routeCall}
				default:
					answers <- answer{symbol: ship.ShipSymbol(), drop: true, routeCall: routeCall} // unroutable: this hull only
				}
				return
			}
			answers <- answer{symbol: ship.ShipSymbol(), eta: transitRemainder + float64(resp.TotalTimeSeconds), routeCall: routeCall}
		}(ship)
	}

	// The slowest single call is what a batch's wall time tracks, because the routing
	// service prices candidates one at a time however many the estimator asks for at once.
	var slowestCall time.Duration
	slowestShip := ""
	for range routable {
		a := <-answers
		if a.routeCall > slowestCall {
			slowestCall, slowestShip = a.routeCall, a.symbol
		}
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
	result.Elapsed = e.clock.Now().Sub(now)
	common.LoggerFromContext(ctx).Log("DEBUG", "Route-ETA batch priced", map[string]interface{}{
		"action":       "route_eta_batch",
		"candidates":   len(routable),
		"elapsed_ms":   result.Elapsed.Milliseconds(),
		"budget_ms":    e.budget.Milliseconds(),
		"slowest_ms":   slowestCall.Milliseconds(),
		"slowest_ship": slowestShip,
		"ranked":       result.OK,
	})
	return result
}

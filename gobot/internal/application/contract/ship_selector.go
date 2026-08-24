package contract

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// SelectClosestShip selects the ship closest to the target waypoint from a list of ship symbols.
// This is a thin application layer wrapper that:
// 1. Fetches ships from repository
// 2. Fetches target waypoint coordinates
// 3. Delegates selection logic to domain FleetSelector service
//
//   - requiredCargoSymbol: The cargo needed for delivery (optional, for prioritization)
//   - unitsNeeded: Units still required for the delivery - used for hull
//     right-sizing, estimating round trips per candidate hull
//   - estimator: Prices each candidate's route to targetWaypointSymbol so the
//     domain selector can rank on actual travel time instead of straight-line
//     distance. Nil, or any failure inside it, degrades to the straight-line
//     path rather than blocking selection - see the ranking_mode field on the
//     completion log for which path a given call took.
//   - deliveryFleet / standbySlots: nil-safe context for SelectHullForCargo's ownership tiebreak
func SelectClosestShip(
	ctx context.Context,
	shipSymbols []string,
	shipRepo navigation.ShipRepository,
	graphProvider system.ISystemGraphProvider,
	converter system.IWaypointConverter,
	targetWaypointSymbol string,
	requiredCargoSymbol string,
	unitsNeeded int,
	playerID int,
	estimator *RouteETAEstimator,
	deliveryFleet []string,
	standbySlots []string,
) (string, float64, error) {
	logger := common.LoggerFromContext(ctx)

	if len(shipSymbols) == 0 {
		return "", 0, fmt.Errorf("no ships available for selection")
	}

	logger.Log("INFO", "Ship selection initiated", map[string]interface{}{
		"action":          "select_ship",
		"candidate_count": len(shipSymbols),
		"target_waypoint": targetWaypointSymbol,
		"required_cargo":  requiredCargoSymbol,
	})

	// Fetches all ships once (cached ~15s in ShipRepository.FindAllByPlayer)
	// instead of one API call per candidate.
	allShips, err := shipRepo.FindAllByPlayer(ctx, shared.MustNewPlayerID(playerID))
	if err != nil {
		return "", 0, fmt.Errorf("failed to load ships: %w", err)
	}

	symbolSet := make(map[string]bool, len(shipSymbols))
	for _, s := range shipSymbols {
		symbolSet[s] = true
	}

	var ships []*navigation.Ship
	for _, ship := range allShips {
		if symbolSet[ship.ShipSymbol()] {
			ships = append(ships, ship)
		}
	}

	if len(ships) == 0 {
		return "", 0, fmt.Errorf("none of the requested ships found in fleet")
	}

	systemSymbol := shared.ExtractSystemSymbol(targetWaypointSymbol)
	graphResult, err := graphProvider.GetGraph(ctx, systemSymbol, false, playerID)
	if err != nil {
		return "", 0, fmt.Errorf("failed to load system graph: %w", err)
	}

	targetWaypoint, ok := graphResult.Graph.Waypoints[targetWaypointSymbol]
	if !ok {
		return "", 0, fmt.Errorf("target waypoint %s not found in graph", targetWaypointSymbol)
	}

	// Route-ETA ranking seam: an estimate per candidate replaces the domain
	// selector's straight-line cruise-time fallback, so the pick reflects
	// actual travel time when the routing service can price it. Every failure
	// path here - a nil estimator, a global routing failure, every candidate
	// unroutable - degrades to the pre-estimator straight-line ranking rather
	// than refusing a selection (RULINGS #1); mode is logged below so a
	// degraded pass is visible, never silent.
	etaResult := ETAResult{OK: false}
	if estimator != nil {
		etaResult = estimator.EstimateAll(ctx, ships, systemSymbol, targetWaypointSymbol, graphResult.Graph.Waypoints)
	}
	rankingMode := "fallback_straight_line"
	var etas map[string]float64
	// dropped accumulates every candidate excluded before the domain selector sees it, so the
	// completion log can still show the full considered set.
	var dropped []*navigation.Ship
	if etaResult.OK {
		rankingMode = "route_eta"
		etas = etaResult.ETAs
		if len(etaResult.Dropped) > 0 {
			droppedSet := make(map[string]bool, len(etaResult.Dropped))
			for _, s := range etaResult.Dropped {
				droppedSet[s] = true
			}
			kept := ships[:0]
			for _, ship := range ships {
				if droppedSet[ship.ShipSymbol()] {
					dropped = append(dropped, ship)
					continue
				}
				kept = append(kept, ship)
			}
			if len(kept) > 0 {
				ships = kept
			} else {
				// Structurally unreachable: OK=true guarantees len(ETAs)>0 (route_eta.go), so at
				// least one ship always survives this filter. Kept, and logged, as a defensive
				// fail-open in case that invariant is ever broken upstream.
				rankingMode = "fallback_straight_line"
				etas = nil
				dropped = nil // reverting to fallback keeps every original candidate - none of them are actually excluded
				logger.Log("WARNING", "Route-ETA ranking unavailable - every priced candidate was unexpectedly dropped, falling back to straight-line selection", map[string]interface{}{
					"action": "route_eta_fallback",
					"cause":  "post_filter_empty",
				})
			}
		}
	} else if estimator != nil {
		// Candidate count, wall time spent and the bound it was measured against separate a
		// budget merely too tight for this many candidates from a service that is failing.
		logger.Log("WARNING", "Route-ETA ranking unavailable - falling back to straight-line selection", map[string]interface{}{
			"action":     "route_eta_fallback",
			"cause":      etaResult.Cause,
			"candidates": len(ships),
			"elapsed_ms": etaResult.Elapsed.Milliseconds(),
			"budget_ms":  estimator.Budget().Milliseconds(),
		})
	}

	// An in-transit hull's CurrentLocation() IS its destination, so a straight-line fallback
	// pricing it from there prices its entire remaining transit as ZERO - a fictional number
	// that can beat a genuinely closer idle hull. Route-ETA mode prices that transit correctly;
	// straight-line mode cannot, so an in-transit hull is eligible only when route-ETA mode
	// actually priced it. Checking the final rankingMode here catches every way it can end up
	// "fallback_straight_line" uniformly.
	if rankingMode == "fallback_straight_line" {
		var excludedInTransit []*navigation.Ship
		ships, excludedInTransit = excludeInTransitFailOpen(ships)
		dropped = append(dropped, excludedInTransit...)
	}

	selector := domainContract.NewShipSelector()
	result, err := selector.SelectOptimalShip(ships, targetWaypoint, requiredCargoSymbol, unitsNeeded, etas, deliveryFleet, standbySlots)
	if err != nil {
		return "", 0, err
	}

	// Enumerates every candidate with its distance to the target (command ships
	// marked) so the pick is auditable - not just the winning symbol.
	logger.Log("INFO", "Ship selection completed", map[string]interface{}{
		"action":          "ship_selected",
		"selected_ship":   result.Ship.ShipSymbol(),
		"distance":        result.Distance,
		"reason":          result.Reason,
		"target_waypoint": targetWaypointSymbol,
		"ranking_mode":    rankingMode,
		"candidates":      summarizeCandidates(ships, dropped, targetWaypoint, etas),
	})

	return result.Ship.ShipSymbol(), result.Distance, nil
}

// excludeInTransitFailOpen drops IN_TRANSIT ships from candidates - see the fallback-eligibility
// comment at its call site above for why this only ever runs once ranking has fallen back to
// straight-line pricing. Fail-open (RULINGS #1): if every candidate is in transit, nothing is
// removed - excluding everything would block the dispatch this ranking exists to keep flowing.
func excludeInTransitFailOpen(candidates []*navigation.Ship) (kept, removed []*navigation.Ship) {
	kept = make([]*navigation.Ship, 0, len(candidates))
	for _, ship := range candidates {
		if ship.NavStatus() == navigation.NavStatusInTransit {
			removed = append(removed, ship)
			continue
		}
		kept = append(kept, ship)
	}
	if len(kept) == 0 {
		return candidates, nil
	}
	return kept, removed
}

// summarizeCandidates renders every candidate ship with its distance to the
// selection target - plus its route ETA in seconds when the estimator priced
// one for it - marking command ships, so the selection log shows the full set
// behind a decision, not just the winner. dropped candidates - unroutable, or
// excluded from a straight-line fallback for being in transit - are still
// rendered, marked /DROPPED instead of an ETA suffix, so the audit line never
// silently loses a candidate that looked closer. Example:
// "TORWIND-3@0.00/42.00s, TORWIND-1@52.10(command), TORWIND-9@1.00/DROPPED".
// etas is nil-safe: a nil or non-matching map simply omits the ETA suffix.
func summarizeCandidates(ships, dropped []*navigation.Ship, target *shared.Waypoint, etas map[string]float64) string {
	entries := make([]string, 0, len(ships)+len(dropped))
	for _, ship := range ships {
		distance := ship.CurrentLocation().DistanceTo(target)
		entry := fmt.Sprintf("%s@%.2f", ship.ShipSymbol(), distance)
		if eta, ok := etas[ship.ShipSymbol()]; ok {
			entry += fmt.Sprintf("/%.2fs", eta)
		}
		if isCommandHull(ship) {
			entry += "(command)"
		}
		entries = append(entries, entry)
	}
	for _, ship := range dropped {
		// A dropped candidate's location can be nil - that is itself one of the estimator's own drop causes.
		entry := ship.ShipSymbol() + "@?"
		if loc := ship.CurrentLocation(); loc != nil {
			entry = fmt.Sprintf("%s@%.2f", ship.ShipSymbol(), loc.DistanceTo(target))
		}
		entry += "/DROPPED"
		if isCommandHull(ship) {
			entry += "(command)"
		}
		entries = append(entries, entry)
	}
	return strings.Join(entries, ", ")
}

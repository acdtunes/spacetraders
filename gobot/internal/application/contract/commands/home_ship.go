package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// Type aliases for convenience
type HomeShipCommand = contractTypes.HomeShipCommand
type HomeShipResponse = contractTypes.HomeShipResponse

// HomeShipHandler dispatches an idle dedicated contract ship to an
// operator-configured standby station, distributed DEMAND-RANKED across the
// configured set: the idle fleet spreads so the highest-demand central sinks are
// covered first and no hub piles up while another sits empty. Peers already parked
// at (or heading to) a hub seed its occupancy; the remaining idle drifters plus
// this hull are placed together (the same deterministic batch every hull's own
// homing call computes), so co-located hulls fan out instead of collapsing onto
// one nearest hub. A uniform (unranked) demand set degrades to plain
// nearest-station homing. Unlike BalanceShipPositionHandler there is no temporary
// container/assignment ceremony, because a dedicated ship is already permanently
// invisible to other coordinators via the DedicatedFleet claim-filter, and the
// contract coordinator's own idle-ship discovery already excludes in-transit ships
// from re-claiming during the homing trip.
type HomeShipHandler struct {
	mediator      common.Mediator
	shipRepo      navigation.ShipRepository
	graphProvider system.ISystemGraphProvider
}

// NewHomeShipHandler creates a new homing handler.
func NewHomeShipHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	graphProvider system.ISystemGraphProvider,
) *HomeShipHandler {
	return &HomeShipHandler{
		mediator:      mediator,
		shipRepo:      shipRepo,
		graphProvider: graphProvider,
	}
}

// Handle executes the homing dispatch.
func (h *HomeShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*HomeShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)

	// Homing is opt-in: an empty --standby-stations list disables relocation
	// entirely. The claim-filter still keeps the ship reserved either way.
	if len(cmd.StandbyStations) == 0 {
		return &HomeShipResponse{Navigated: false}, nil
	}

	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load ship %s: %w", cmd.ShipSymbol, err)
	}

	// Homing applies to idle standby hulls only (l7h2 Phase 3 invariant): a
	// hull that is claimed/assigned or mid-flight is never relocated, no
	// matter what the dispatcher believed when it fired this command.
	if !ship.IsIdle() || ship.IsInTransit() {
		logger.Log("INFO", "Skipping homing for busy hull", map[string]interface{}{
			"action":      "home_ship",
			"ship_symbol": cmd.ShipSymbol,
			"idle":        ship.IsIdle(),
			"in_transit":  ship.IsInTransit(),
		})
		return &HomeShipResponse{Navigated: false}, nil
	}

	systemSymbol := ship.CurrentLocation().SystemSymbol
	graphResult, err := h.graphProvider.GetGraph(ctx, systemSymbol, false, cmd.PlayerID.Value())
	if err != nil {
		return nil, fmt.Errorf("failed to load system graph: %w", err)
	}

	candidateBySymbol := map[string]*shared.Waypoint{}
	var candidates []*shared.Waypoint
	for _, symbol := range cmd.StandbyStations {
		wp, ok := graphResult.Graph.Waypoints[symbol]
		if !ok {
			continue // Skip stations not found in this system's graph.
		}
		candidates = append(candidates, wp)
		candidateBySymbol[symbol] = wp
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("none of the configured standby stations %v found in system %s graph", cmd.StandbyStations, systemSymbol)
	}

	// A hull already sitting at ANY standby station is left where it is: re-firing
	// the distribution would chase the demand-ranked target and churn home hulls
	// hub-to-hub every pass (mirrors the idle-arb re-home's off-station-only rule).
	if wp, atStandby := candidateBySymbol[ship.CurrentLocation().Symbol]; atStandby {
		return &HomeShipResponse{TargetStation: wp.Symbol, Distance: 0, Navigated: false}, nil
	}

	peers, err := h.loadFleetPeers(ctx, cmd)
	if err != nil {
		return nil, err
	}

	// DEMAND-RANKED SPREAD: replace the demand-blind occupancy+distance
	// balancer with a distribution that fans co-located idle hulls across the
	// standby set instead of piling them on one point. Peers already parked at (or
	// heading to) a hub seed its occupancy; the remaining idle drifters plus this
	// hull are placed together so the batch is identical across every hull's own
	// homing call — each independent call lands this hull in its distinct slot.
	occupancy := map[string]int{}
	for _, peer := range peers {
		if _, isStation := candidateBySymbol[peer.CurrentLocation().Symbol]; isStation {
			occupancy[peer.CurrentLocation().Symbol]++
		}
	}
	toPlace := []domainContract.IdleHullToPlace{{ShipSymbol: cmd.ShipSymbol, Distance: distancesTo(ship, candidates)}}
	for _, peer := range peers {
		if _, isStation := candidateBySymbol[peer.CurrentLocation().Symbol]; isStation {
			continue // already home — counted as occupancy, not re-placed
		}
		if !peer.IsIdle() || peer.IsInTransit() {
			continue // busy peers are not homing — they neither occupy a hub nor take a slot
		}
		toPlace = append(toPlace, domainContract.IdleHullToPlace{ShipSymbol: peer.ShipSymbol(), Distance: distancesTo(peer, candidates)})
	}

	waypoints := make([]domainContract.StandbyWaypoint, 0, len(candidates))
	for _, wp := range candidates {
		waypoints = append(waypoints, domainContract.StandbyWaypoint{Symbol: wp.Symbol, DemandWeight: cmd.StandbyDemand[wp.Symbol]})
	}

	placement := domainContract.DistributeIdleHullsAcrossStandby(toPlace, waypoints, occupancy)
	target, ok := candidateBySymbol[placement[cmd.ShipSymbol]]
	if !ok {
		return nil, fmt.Errorf("standby distribution returned no waypoint for ship %s", cmd.ShipSymbol)
	}
	distance := ship.CurrentLocation().DistanceTo(target)

	logger.Log("INFO", "Standby station selected for homing", map[string]interface{}{
		"action":        "home_ship",
		"ship_symbol":   cmd.ShipSymbol,
		"station":       target.Symbol,
		"distance":      distance,
		"peers_at_hub":  occupancy[target.Symbol],
		"fleet_peers":   len(peers),
		"station_count": len(candidates),
	})

	if ship.IsAtLocation(target) {
		return &HomeShipResponse{TargetStation: target.Symbol, Distance: distance, Navigated: false}, nil
	}

	navigateCmd := &shipNav.NavigateRouteCommand{
		ShipSymbol:  cmd.ShipSymbol,
		Destination: target.Symbol,
		PlayerID:    cmd.PlayerID,
	}
	if _, err := h.mediator.Send(ctx, navigateCmd); err != nil {
		return nil, fmt.Errorf("failed to navigate to standby station: %w", err)
	}

	logger.Log("INFO", "Dedicated ship homing to standby station", map[string]interface{}{
		"action":      "home_ship",
		"ship_symbol": cmd.ShipSymbol,
		"station":     target.Symbol,
		"distance":    distance,
	})

	return &HomeShipResponse{TargetStation: target.Symbol, Distance: distance, Navigated: true}, nil
}

// distancesTo maps each candidate standby waypoint to the in-system distance from
// the hull's current location, feeding the distribution's equal-demand tie-break
// (so an unranked set still homes to the nearest hub).
func distancesTo(ship *navigation.Ship, candidates []*shared.Waypoint) map[string]float64 {
	loc := ship.CurrentLocation()
	out := make(map[string]float64, len(candidates))
	for _, wp := range candidates {
		out[wp.Symbol] = loc.DistanceTo(wp)
	}
	return out
}

// loadFleetPeers resolves the dedicated-fleet hulls (minus the ship being
// homed) whose positions determine standby-station occupancy. An empty
// FleetShips list means no occupancy data - the balancer then degrades to
// pure nearest-station homing, the pre-Phase-3 behavior.
func (h *HomeShipHandler) loadFleetPeers(ctx context.Context, cmd *HomeShipCommand) ([]*navigation.Ship, error) {
	if len(cmd.FleetShips) == 0 {
		return nil, nil
	}

	allShips, err := h.shipRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load fleet peers for homing: %w", err)
	}

	fleet := make(map[string]bool, len(cmd.FleetShips))
	for _, symbol := range cmd.FleetShips {
		fleet[symbol] = true
	}

	var peers []*navigation.Ship
	for _, peer := range allShips {
		if peer.ShipSymbol() == cmd.ShipSymbol {
			continue
		}
		if fleet[peer.ShipSymbol()] {
			peers = append(peers, peer)
		}
	}
	return peers, nil
}

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
// operator-configured standby station (sp-snmb), balanced across the
// configured set (l7h2 Phase 3): the station with the fewest dedicated-fleet
// peers already parked at (or heading to) it wins, distance breaking ties -
// nearest-only homing clumped every idle hull on one hub. Unlike
// BalanceShipPositionHandler there is no temporary container/assignment
// ceremony, because a dedicated ship is already permanently invisible to
// other coordinators via the DedicatedFleet claim-filter, and the contract
// coordinator's own idle-ship discovery already excludes in-transit ships
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

	var candidates []*shared.Waypoint
	for _, symbol := range cmd.StandbyStations {
		wp, ok := graphResult.Graph.Waypoints[symbol]
		if !ok {
			continue // Skip stations not found in this system's graph.
		}
		candidates = append(candidates, wp)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("none of the configured standby stations %v found in system %s graph", cmd.StandbyStations, systemSymbol)
	}

	peers, err := h.loadFleetPeers(ctx, cmd)
	if err != nil {
		return nil, err
	}

	// The fleet-wide balancing policy (occupancy-first, distance tie-break)
	// already lives in the domain ShipBalancer - standby stations are the
	// "markets" and the dedicated-fleet peers the occupants. An in-transit
	// peer counts at its destination (CurrentLocation is the destination once
	// transit starts), so two hulls homed back-to-back pick different hubs.
	balancer := domainContract.NewShipBalancer()
	balance, err := balancer.SelectOptimalBalancingPosition(ship, candidates, peers)
	if err != nil {
		return nil, fmt.Errorf("failed to select standby station: %w", err)
	}
	target := balance.TargetMarket
	distance := balance.Distance

	logger.Log("INFO", "Standby station selected for homing", map[string]interface{}{
		"action":        "home_ship",
		"ship_symbol":   cmd.ShipSymbol,
		"station":       target.Symbol,
		"distance":      distance,
		"peers_at_hub":  balance.AssignedShips,
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

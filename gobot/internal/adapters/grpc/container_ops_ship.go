package grpc

import (
	"context"
	"fmt"

	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// NavigateShip handles ship navigation requests
// This will be called by the gRPC handler when proto is generated
func (s *DaemonServer) NavigateShip(ctx context.Context, shipSymbol, destination string, playerID int) (string, error) {
	// Create container ID
	containerID := utils.GenerateContainerID("navigate", shipSymbol)

	// Create navigation command
	cmd := &shipNav.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    shared.MustNewPlayerID(playerID),
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeNavigate,
		playerID,
		1,   // Single iteration for navigate
		nil, // No parent container
		map[string]interface{}{
			"ship_symbol": shipSymbol,
			"destination": destination,
			// sp-sg35 BRIDGE: captain manual-op authority — this deliberate CLI op
			// may operate a fleet-dedicated hull (audited override; see the const).
			captainManualAuthorityKey: true,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "navigate_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// RouteShip handles cross-system point-to-point travel requests (sp-6hjw). It is the
// daemon side of the `ship route` verb: unlike NavigateShip (which dispatches the
// in-system-only NavigateRouteCommand and fails cross-system with "waypoint not found
// in cache for system X"), it dispatches a RouteShipCommand whose handler reuses the
// trade-route coordinator's multi-jump travel() — orbit, source gate hop, per-hop
// jumps with cooldown waits, arrival hop — so a plain hull reaches a waypoint in ANY
// reachable system. The container claims the hull (metadata "ship_symbol") so travel()'s
// SkipClaim jumps trust that claim, and carries the captain manual-op authority flag so
// this deliberate CLI move may operate a fleet-dedicated hull (audited override).
func (s *DaemonServer) RouteShip(ctx context.Context, shipSymbol, destination string, playerID int) (string, error) {
	containerID := utils.GenerateContainerID("route", shipSymbol)

	cmd := &shipNav.RouteShipCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    shared.MustNewPlayerID(playerID),
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeRoute,
		playerID,
		1,   // Single iteration for route
		nil, // No parent container
		map[string]interface{}{
			"ship_symbol": shipSymbol,
			"destination": destination,
			// sp-sg35 BRIDGE: captain manual-op authority — this deliberate CLI op
			// may operate a fleet-dedicated hull (audited override; see the const).
			captainManualAuthorityKey: true,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "route_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// DockShip handles ship docking requests
func (s *DaemonServer) DockShip(ctx context.Context, shipSymbol string, playerID int) (string, error) {
	containerID := utils.GenerateContainerID("dock", shipSymbol)

	cmd := &shipTypes.DockShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   shared.MustNewPlayerID(playerID),
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeDock,
		playerID,
		1,   // Single iteration for dock
		nil, // No parent container
		map[string]interface{}{
			"ship_symbol": shipSymbol,
			// sp-sg35 BRIDGE: captain manual-op authority (audited override; see const).
			captainManualAuthorityKey: true,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "dock_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// OrbitShip handles ship orbit requests
func (s *DaemonServer) OrbitShip(ctx context.Context, shipSymbol string, playerID int) (string, error) {
	containerID := utils.GenerateContainerID("orbit", shipSymbol)

	cmd := &shipTypes.OrbitShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   shared.MustNewPlayerID(playerID),
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeOrbit,
		playerID,
		1,   // Single iteration for orbit
		nil, // No parent container
		map[string]interface{}{
			"ship_symbol": shipSymbol,
			// sp-sg35 BRIDGE: captain manual-op authority (audited override; see const).
			captainManualAuthorityKey: true,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "orbit_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// RefuelShip handles ship refuel requests
func (s *DaemonServer) RefuelShip(ctx context.Context, shipSymbol string, playerID int, units *int) (string, error) {
	containerID := utils.GenerateContainerID("refuel", shipSymbol)

	cmd := &shipTypes.RefuelShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   shared.MustNewPlayerID(playerID),
		Units:      units,
	}

	metadata := map[string]interface{}{
		"ship_symbol": shipSymbol,
		// sp-sg35 BRIDGE: captain manual-op authority (audited override; see const).
		captainManualAuthorityKey: true,
	}
	if units != nil {
		metadata["units"] = *units
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeRefuel,
		playerID,
		1,   // Single iteration for refuel
		nil, // No parent container
		metadata,
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "refuel_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// JettisonCargo handles ship jettison cargo requests
func (s *DaemonServer) JettisonCargo(ctx context.Context, shipSymbol string, playerID int, goodSymbol string, units int) (string, error) {
	containerID := utils.GenerateContainerID("jettison", shipSymbol)

	cmd := &shipCargo.JettisonCargoCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   shared.MustNewPlayerID(playerID),
		GoodSymbol: goodSymbol,
		Units:      units,
	}

	metadata := map[string]interface{}{
		"ship_symbol": shipSymbol,
		"good_symbol": goodSymbol,
		"units":       units,
		// sp-sg35 BRIDGE: captain manual-op authority (audited override; see const).
		captainManualAuthorityKey: true,
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeJettison,
		playerID,
		1,   // Single iteration for jettison
		nil, // No parent container
		metadata,
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "jettison_cargo"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

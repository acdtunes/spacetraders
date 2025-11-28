package grpc

import (
	"context"
	"fmt"

	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
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
	cmd := &shipCmd.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    shared.MustNewPlayerID(playerID),
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeNavigate,
		playerID,
		1, // Single iteration for navigate
		nil, // No parent container
		map[string]interface{}{
			"ship_symbol": shipSymbol,
			"destination": destination,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "navigate_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
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
		1, // Single iteration for dock
		nil, // No parent container
		map[string]interface{}{
			"ship_symbol": shipSymbol,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "dock_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
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
		1, // Single iteration for orbit
		nil, // No parent container
		map[string]interface{}{
			"ship_symbol": shipSymbol,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "orbit_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
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
	}
	if units != nil {
		metadata["units"] = *units
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeRefuel,
		playerID,
		1, // Single iteration for refuel
		nil, // No parent container
		metadata,
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "refuel_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
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

	cmd := &shipCmd.JettisonCargoCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   shared.MustNewPlayerID(playerID),
		GoodSymbol: goodSymbol,
		Units:      units,
	}

	metadata := map[string]interface{}{
		"ship_symbol": shipSymbol,
		"good_symbol": goodSymbol,
		"units":       units,
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeJettison,
		playerID,
		1, // Single iteration for jettison
		nil, // No parent container
		metadata,
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "jettison_cargo"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

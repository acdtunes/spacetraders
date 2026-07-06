package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// PurchaseShip purchases a single ship from a shipyard
func (s *DaemonServer) PurchaseShip(ctx context.Context, purchasingShipSymbol, shipType string, playerID int, shipyardWaypoint *string) (string, string, int32, int32, string, error) {
	shipyard := ""
	if shipyardWaypoint != nil {
		shipyard = *shipyardWaypoint
	}

	// Create container ID
	containerID := utils.GenerateContainerID("purchase_ship", purchasingShipSymbol)

	config := map[string]interface{}{
		"ship_symbol": purchasingShipSymbol,
		"ship_type":   shipType,
		"shipyard":    shipyard,
	}

	// Create purchase command from the launch config
	cmd, err := s.buildCommandForType("purchase_ship", config, playerID, containerID)
	if err != nil {
		return "", "", 0, 0, "", fmt.Errorf("failed to create command: %w", err)
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypePurchase,
		playerID,
		1,   // Single iteration
		nil, // No parent container
		config,
		nil, // Use real clock
	)

	// Persist container to database before starting (prevents foreign key violations in logs)
	if err := s.containerRepo.Add(ctx, containerEntity, "purchase_ship"); err != nil {
		return "", "", 0, 0, "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)

	// Store container
	s.containersMu.Lock()
	s.containers[containerID] = runner
	s.containersMu.Unlock()

	// Start execution in background
	runner.Start()

	return containerID, "", 0, 0, "starting", nil
}

// BatchPurchaseShips purchases multiple ships from a shipyard as a background operation
func (s *DaemonServer) BatchPurchaseShips(ctx context.Context, purchasingShipSymbol, shipType string, quantity, maxBudget, playerID int, shipyardWaypoint *string, iterations *int) (string, int32, int32, string, string, error) {
	shipyard := ""
	if shipyardWaypoint != nil {
		shipyard = *shipyardWaypoint
	}

	// Resolve iterations (default to 1)
	iterCount := 1
	if iterations != nil {
		iterCount = *iterations
	}

	// Create container ID
	containerID := utils.GenerateContainerID("batch_purchase_ships", purchasingShipSymbol)

	config := map[string]interface{}{
		"ship_symbol": purchasingShipSymbol,
		"ship_type":   shipType,
		"quantity":    quantity,
		"max_budget":  maxBudget,
		"shipyard":    shipyard,
	}

	// Create batch purchase command from the launch config
	cmd, err := s.buildCommandForType("batch_purchase_ships", config, playerID, containerID)
	if err != nil {
		return "", 0, 0, "", "", fmt.Errorf("failed to create command: %w", err)
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypePurchase,
		playerID,
		iterCount,
		nil, // No parent container
		config,
		nil, // Use real clock
	)

	// Persist container to database before starting (prevents foreign key violations in logs)
	if err := s.containerRepo.Add(ctx, containerEntity, "batch_purchase_ships"); err != nil {
		return "", 0, 0, "", "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)

	// Store container
	s.containersMu.Lock()
	s.containers[containerID] = runner
	s.containersMu.Unlock()

	// Start execution in background
	runner.Start()

	return containerID, int32(quantity), int32(maxBudget), shipyard, "starting", nil
}

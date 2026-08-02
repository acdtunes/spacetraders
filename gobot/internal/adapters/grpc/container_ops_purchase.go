package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// operationPurchasing is the fleet identity every PURCHASE container claims its
// purchaser under. It is the SAME tag the first-hauler pivot writes onto the command
// frigate when it retires it from contracts and reserves it as the exclusive buy ship
// (bootstrapFrigateRetirer.DedicateAsPurchaser -> navigation.PurchasingFleet), so the
// two sides of that dedication can never drift.
//
// Without it a purchase carried NO fleet identity at all: the claim fell to the legacy
// read-modify-write path, where the standing dedication guard refused the flagship to
// "an undeclared operation" (7 claim_failed containers on the live fleet). The
// dedication that exists to reserve the frigate FOR buying was the one thing stopping
// it from buying, and the exclusive purchaser could never execute a purchase.
//
// Declaring it routes the claim through the atomic operation-checked ClaimShip
// instead, which admits the hull only because the claiming fleet IS the fleet the hull
// is dedicated to — the ownership model honoured, not circumvented (RULINGS #7). Every
// other pin still refuses a purchase: a "contract"/"trade"/depot hull does not match
// this identity, and the command frigate's depot-role rejection is untouched.
const operationPurchasing = navigation.PurchasingFleet

// PurchaseShip purchases a single ship from a shipyard
func (s *DaemonServer) PurchaseShip(ctx context.Context, purchasingShipSymbol, shipType string, playerID int, shipyardWaypoint *string) (string, string, int32, int32, string, error) {
	shipyard := ""
	if shipyardWaypoint != nil {
		shipyard = *shipyardWaypoint
	}

	containerID := utils.GenerateContainerID("purchase_ship", purchasingShipSymbol)

	config := map[string]interface{}{
		"ship_symbol": purchasingShipSymbol,
		"ship_type":   shipType,
		"shipyard":    shipyard,
		// The buying identity, persisted in the launch config so a daemon restart
		// re-adopts it and the recovered container claims under the same fleet
		// (RULINGS #2 — derived from durable state, never held in memory).
		"operation": operationPurchasing,
	}

	cmd, err := s.buildCommandForType("purchase_ship", config, playerID, containerID)
	if err != nil {
		return "", "", 0, 0, "", fmt.Errorf("failed to create command: %w", err)
	}

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

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)

	s.containersMu.Lock()
	s.containers[containerID] = runner
	s.containersMu.Unlock()

	runner.Start()

	return containerID, "", 0, 0, "starting", nil
}

// BatchPurchaseShips purchases multiple ships from a shipyard as a background operation.
//
// dedicateFleet is the optional operator-named fleet each purchased hull is
// dedicated to ATOMICALLY at purchase (via the container's BatchPurchaseShipsCommand, which
// stamps the single sanctioned AssignFleet write per hull before it completes). Empty ->
// hull lands undedicated, byte-identical to a plain purchase. It rides in the persisted
// container config so a daemon restart re-adopts the same atomic buy+dedicate intent.
func (s *DaemonServer) BatchPurchaseShips(ctx context.Context, purchasingShipSymbol, shipType string, quantity, maxBudget, playerID int, shipyardWaypoint *string, iterations *int, dedicateFleet string) (string, int32, int32, string, string, error) {
	shipyard := ""
	if shipyardWaypoint != nil {
		shipyard = *shipyardWaypoint
	}

	// Resolve iterations (default to 1)
	iterCount := 1
	if iterations != nil {
		iterCount = *iterations
	}

	containerID := utils.GenerateContainerID("batch_purchase_ships", purchasingShipSymbol)

	config := map[string]interface{}{
		"ship_symbol":    purchasingShipSymbol,
		"ship_type":      shipType,
		"quantity":       quantity,
		"max_budget":     maxBudget,
		"shipyard":       shipyard,
		"dedicate_fleet": dedicateFleet,
		// The buying identity (see operationPurchasing). Distinct from
		// dedicate_fleet, which is the tag stamped onto the hulls this container
		// BUYS; this is the fleet the container claims its PURCHASER under.
		"operation": operationPurchasing,
	}

	cmd, err := s.buildCommandForType("batch_purchase_ships", config, playerID, containerID)
	if err != nil {
		return "", 0, 0, "", "", fmt.Errorf("failed to create command: %w", err)
	}

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

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)

	s.containersMu.Lock()
	s.containers[containerID] = runner
	s.containersMu.Unlock()

	runner.Start()

	return containerID, int32(quantity), int32(maxBudget), shipyard, "starting", nil
}

// Package commands holds the daemon-side lifecycle owners for storage
// operations that are NOT extractor-fed. Today that is the warehouse (sp-dchv
// Lane B): a passive, dedicated hull parked at a home waypoint that buffers
// arbitrary contract goods deposited by haulers. It reuses the shared
// StorageShip/StorageCoordinator/StorageRecoveryService machinery unchanged —
// the gas coordinator's siphon/extractor/jettison machinery is deliberately
// absent.
package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// RunWarehouseCommand starts (or resumes) a warehouse storage operation on one
// dedicated hull parked at a home waypoint. The hull itself is claimed by the
// ContainerRunner via the container's ship_symbol + operation="warehouse"
// metadata (the atomic ClaimShip dedication, RULINGS #7) BEFORE this command
// runs, so the handler only owns the operation row and the coordinator
// registration.
type RunWarehouseCommand struct {
	ShipSymbol     string          // The dedicated storage hull
	WaypointSymbol string          // Home waypoint the hull is parked at
	PlayerID       shared.PlayerID //
	ContainerID    string          // Owning container (= operation ID)
	OperationID    string          // Storage operation ID (stable across restarts)
	SupportedGoods []string        // Whitelist of goods this warehouse buffers
}

// RunWarehouseResponse reports where the warehouse hull ended up.
type RunWarehouseResponse struct {
	ShipSymbol  string
	OperationID string
	Location    string
	Error       string
}

// RunWarehouseHandler owns a warehouse operation's lifecycle: it persists the
// operation row (so the StorageRecoveryService and StorageSourceFinder can find
// it), parks the hull at the home waypoint, registers the hull as a StorageShip
// with the shared coordinator (so deposits and withdrawals work at runtime),
// and holds until shutdown. It writes NO ship state itself beyond navigation
// (single-writer, RULINGS #3): deposits come from tour/trade legs, withdrawals
// from the manufacturing STORAGE_ACQUIRE_DELIVER executor.
type RunWarehouseHandler struct {
	mediator           common.Mediator
	shipRepo           navigation.ShipRepository
	storageOpRepo      storage.StorageOperationRepository
	storageCoordinator storage.StorageCoordinator
	clock              shared.Clock
}

// NewRunWarehouseHandler wires a warehouse handler. clock may be nil (defaults
// to RealClock), matching the gas coordinator's constructor convention.
func NewRunWarehouseHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	storageOpRepo storage.StorageOperationRepository,
	storageCoordinator storage.StorageCoordinator,
	clock shared.Clock,
) *RunWarehouseHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunWarehouseHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		storageOpRepo:      storageOpRepo,
		storageCoordinator: storageCoordinator,
		clock:              clock,
	}
}

// Handle sets the warehouse up (operation row + parked, registered hull) and
// then holds until the container is shut down, unregistering the hull on the
// way out — the same passive shape as the gas storage-ship worker, minus the
// HYDROCARBON jettison loop (a warehouse holds every good it is given).
func (h *RunWarehouseHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunWarehouseCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)
	result := &RunWarehouseResponse{ShipSymbol: cmd.ShipSymbol, OperationID: cmd.OperationID}

	location, err := h.setup(ctx, cmd, logger)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	result.Location = location

	// Passive hold: the warehouse does no work of its own. It exists so its
	// cargo is available to withdrawers; deposits/withdrawals are driven by
	// other containers against the shared coordinator. Block until shutdown.
	<-ctx.Done()
	h.storageCoordinator.UnregisterStorageShip(cmd.ShipSymbol)
	logger.Log("INFO", "Warehouse worker shutdown", map[string]interface{}{
		"action":      "shutdown",
		"ship_symbol": cmd.ShipSymbol,
		"operation":   cmd.OperationID,
	})
	return result, ctx.Err()
}

// setup performs the recovery-safe, restart-idempotent warehouse bring-up:
// persist/resume the operation row, park the hull at the home waypoint, and
// register it with the coordinator. Split from the blocking hold in Handle so it
// is testable without goroutines. Returns the hull's final location.
func (h *RunWarehouseHandler) setup(ctx context.Context, cmd *RunWarehouseCommand, logger common.ContainerLogger) (string, error) {
	if _, err := h.getOrCreateWarehouseOperation(ctx, cmd, logger); err != nil {
		return "", err
	}

	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return "", fmt.Errorf("failed to load warehouse hull %s: %w", cmd.ShipSymbol, err)
	}
	if ship == nil {
		return "", fmt.Errorf("warehouse hull %s not found", cmd.ShipSymbol)
	}

	// Park the hull at the home waypoint if it is not already there. Guarded so
	// a hull already parked (the common case, and every test case) issues no
	// navigation. Mirrors the gas storage-ship worker.
	if ship.CurrentLocation().Symbol != cmd.WaypointSymbol {
		logger.Log("INFO", "Warehouse hull navigating to home waypoint", map[string]interface{}{
			"action":      "navigate_to_waypoint",
			"ship_symbol": cmd.ShipSymbol,
			"from":        ship.CurrentLocation().Symbol,
			"to":          cmd.WaypointSymbol,
		})
		navResp, navErr := h.mediator.Send(ctx, &shipNav.NavigateRouteCommand{
			ShipSymbol:  cmd.ShipSymbol,
			Destination: cmd.WaypointSymbol,
			PlayerID:    cmd.PlayerID,
		})
		if navErr != nil {
			// Non-fatal: register where we are and let a later restart re-try
			// parking. The warehouse can still buffer cargo; a mis-parked hull is
			// a location problem, not a data-integrity one.
			logger.Log("WARNING", "Failed to park warehouse hull; registering at current location", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"error":       navErr.Error(),
			})
		} else if resp, ok := navResp.(*shipNav.NavigateRouteResponse); ok {
			ship = resp.Ship
		}
	}

	if err := h.registerStorageShip(ship, cmd, logger); err != nil {
		return "", err
	}
	return ship.CurrentLocation().Symbol, nil
}

// getOrCreateWarehouseOperation resumes an existing warehouse operation row or
// creates a new one, persisted RUNNING so the StorageRecoveryService rebuilds it
// after a daemon restart (RULINGS #2). Idempotent on restart: an existing row is
// resumed (Started if still PENDING), never duplicated.
func (h *RunWarehouseHandler) getOrCreateWarehouseOperation(
	ctx context.Context,
	cmd *RunWarehouseCommand,
	logger common.ContainerLogger,
) (*storage.StorageOperation, error) {
	existing, err := h.storageOpRepo.FindByID(ctx, cmd.OperationID)
	if err == nil && existing != nil {
		logger.Log("INFO", "Resuming existing warehouse operation", map[string]interface{}{
			"action":       "resume_operation",
			"operation_id": cmd.OperationID,
			"status":       existing.Status(),
		})
		if existing.IsPending() {
			if startErr := existing.Start(); startErr == nil {
				_ = h.storageOpRepo.Update(ctx, existing)
			}
		}
		return existing, nil
	}

	operation, err := storage.NewWarehouseOperation(
		cmd.OperationID,
		cmd.PlayerID.Value(),
		cmd.WaypointSymbol,
		[]string{cmd.ShipSymbol},
		cmd.SupportedGoods,
		h.clock,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create warehouse operation: %w", err)
	}
	if err := h.storageOpRepo.Create(ctx, operation); err != nil {
		return nil, fmt.Errorf("failed to persist warehouse operation: %w", err)
	}
	if err := operation.Start(); err != nil {
		return nil, fmt.Errorf("failed to start warehouse operation: %w", err)
	}
	if err := h.storageOpRepo.Update(ctx, operation); err != nil {
		return nil, fmt.Errorf("failed to mark warehouse operation running: %w", err)
	}

	logger.Log("INFO", "Created new warehouse operation", map[string]interface{}{
		"action":          "create_operation",
		"operation_id":    cmd.OperationID,
		"waypoint":        cmd.WaypointSymbol,
		"supported_goods": cmd.SupportedGoods,
	})
	return operation, nil
}

// registerStorageShip registers the hull as a StorageShip with the shared
// coordinator, seeded from its current live cargo. Idempotent with the
// StorageRecoveryService's own registration (both may run on restart): an
// already-registered hull is logged and tolerated, never treated as fatal —
// exactly the gas storage-ship worker's contract.
func (h *RunWarehouseHandler) registerStorageShip(
	ship *navigation.Ship,
	cmd *RunWarehouseCommand,
	logger common.ContainerLogger,
) error {
	initialCargo := make(map[string]int)
	for _, item := range ship.Cargo().Inventory {
		initialCargo[item.Symbol] = item.Units
	}

	storageShip, err := storage.NewStorageShip(
		cmd.ShipSymbol,
		ship.CurrentLocation().Symbol,
		cmd.OperationID,
		ship.Cargo().Capacity,
		initialCargo,
	)
	if err != nil {
		return fmt.Errorf("failed to create warehouse storage ship entity: %w", err)
	}

	if err := h.storageCoordinator.RegisterStorageShip(storageShip); err != nil {
		logger.Log("WARNING", "Warehouse hull may already be registered (recovery race)", map[string]interface{}{
			"action":      "register_storage_ship",
			"ship_symbol": cmd.ShipSymbol,
			"error":       err.Error(),
		})
		// Tolerated: the recovery service may have registered it first.
	}

	logger.Log("INFO", "Warehouse hull registered and ready", map[string]interface{}{
		"action":         "warehouse_ready",
		"ship_symbol":    cmd.ShipSymbol,
		"operation_id":   cmd.OperationID,
		"location":       ship.CurrentLocation().Symbol,
		"cargo_capacity": ship.Cargo().Capacity,
		"current_cargo":  ship.Cargo().Units,
	})
	return nil
}

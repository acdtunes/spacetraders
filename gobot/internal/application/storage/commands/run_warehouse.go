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
	"errors"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// A warehouse hull can be momentarily mid-flight when bring-up tries to position
// it — the API rejects a navigate/orbit on an in-transit hull — and that clears
// once the flight lands. So positioning is retried a bounded number of times before
// the operation is failed, rather than the hull silently anchored away from home.
const (
	warehousePositionMaxAttempts = 3
	warehousePositionBackoff     = 2 * time.Second
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
	// Stamp this warehouse's operation context so the parking navigate to the
	// home waypoint — and every refuel route_executor fires inside it — inherits
	// operation_type="warehouse" instead of the 'manual' fallback. The navigate leg is
	// ctx-transparent (it records whatever operation context rides the ctx), so without
	// this stamp the parking hop's refuels landed unattributed. A warehouse built without
	// a ContainerID yields a nil context and honestly stays 'manual'.
	ctx = shared.WithOperationContext(ctx, shared.NewOperationContext(cmd.ContainerID, "warehouse"))

	operation, err := h.getOrCreateWarehouseOperation(ctx, cmd, logger)
	if err != nil {
		return "", err
	}

	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return "", fmt.Errorf("failed to load warehouse hull %s: %w", cmd.ShipSymbol, err)
	}
	if ship == nil {
		return "", fmt.Errorf("warehouse hull %s not found", cmd.ShipSymbol)
	}

	// Position the hull at its home waypoint AND put it in orbit before it is
	// registered. A positioning failure is surfaced loudly and durably, never a
	// silent registration at the wrong location.
	positioned, posErr := h.positionHull(ctx, cmd, ship, logger)
	if posErr != nil {
		return "", h.handlePositionFailure(ctx, cmd, operation, posErr, logger)
	}

	if err := h.registerStorageShip(positioned, cmd, logger); err != nil {
		return "", err
	}
	return positioned.CurrentLocation().Symbol, nil
}

// positionHull brings the warehouse hull to its home waypoint and puts it in ORBIT
// — a warehouse anchors its stock in orbit, not docked. It retries a bounded number
// of times to ride out a transient in-transit race; a cancellation is a shutdown,
// never retried and surfaced immediately so the container exits gracefully and the
// operation stays resumable (RULINGS #2). All ship-state changes go through the
// daemon via the mediator (RULINGS #3).
func (h *RunWarehouseHandler) positionHull(
	ctx context.Context,
	cmd *RunWarehouseCommand,
	ship *navigation.Ship,
	logger common.ContainerLogger,
) (*navigation.Ship, error) {
	var lastErr error
	for attempt := 1; attempt <= warehousePositionMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		positioned, err := h.attemptPositionHull(ctx, cmd, ship, logger)
		if err == nil {
			return positioned, nil
		}
		lastErr = err

		// A cancellation (graceful shutdown) is never a warehouse failure and is
		// never retried — it neither self-heals by waiting nor should burn attempts.
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return nil, err
		}

		if attempt < warehousePositionMaxAttempts {
			logger.Log("WARNING", "Warehouse hull positioning failed; retrying", map[string]interface{}{
				"action":      "position_retry",
				"ship_symbol": cmd.ShipSymbol,
				"waypoint":    cmd.WaypointSymbol,
				"attempt":     attempt,
				"error":       err.Error(),
			})
			if waitErr := h.sleepOrCancel(ctx, warehousePositionBackoff*time.Duration(attempt)); waitErr != nil {
				return nil, waitErr
			}
		}
	}
	return nil, fmt.Errorf("warehouse hull %s could not reach home waypoint %s after %d attempts: %w",
		cmd.ShipSymbol, cmd.WaypointSymbol, warehousePositionMaxAttempts, lastErr)
}

// attemptPositionHull performs one navigate-then-orbit try. It navigates only when
// the hull is away from home, then VERIFIES the hull actually reached the waypoint
// before orbiting — a navigate that returns without error but leaves the hull
// elsewhere is a failure here, never a registration at the wrong place. StartTransit
// sets CurrentLocation to the destination, so a genuine arrival reads as the home
// waypoint. Orbit is idempotent — a hull already in orbit issues no API call.
func (h *RunWarehouseHandler) attemptPositionHull(
	ctx context.Context,
	cmd *RunWarehouseCommand,
	ship *navigation.Ship,
	logger common.ContainerLogger,
) (*navigation.Ship, error) {
	positioned := ship
	if positioned.CurrentLocation().Symbol != cmd.WaypointSymbol {
		logger.Log("INFO", "Warehouse hull navigating to home waypoint", map[string]interface{}{
			"action":      "navigate_to_waypoint",
			"ship_symbol": cmd.ShipSymbol,
			"from":        positioned.CurrentLocation().Symbol,
			"to":          cmd.WaypointSymbol,
		})
		navResp, navErr := h.mediator.Send(ctx, &shipNav.NavigateRouteCommand{
			ShipSymbol:  cmd.ShipSymbol,
			Destination: cmd.WaypointSymbol,
			PlayerID:    cmd.PlayerID,
		})
		if navErr != nil {
			return nil, navErr
		}
		if resp, ok := navResp.(*shipNav.NavigateRouteResponse); ok && resp.Ship != nil {
			positioned = resp.Ship
		}
	}

	// Anti-mis-anchor guard: the hull MUST be at its home waypoint before it is
	// orbited and registered.
	if positioned.CurrentLocation().Symbol != cmd.WaypointSymbol {
		return nil, fmt.Errorf("warehouse hull %s is at %s, not home waypoint %s after navigation",
			cmd.ShipSymbol, positioned.CurrentLocation().Symbol, cmd.WaypointSymbol)
	}

	// Orbit the hull at its waypoint — the stock anchor. Via the daemon (RULINGS #3).
	if _, err := h.mediator.Send(ctx, &shipTypes.OrbitShipCommand{
		Ship:     positioned,
		PlayerID: cmd.PlayerID,
	}); err != nil {
		return nil, fmt.Errorf("failed to orbit warehouse hull %s at %s: %w", cmd.ShipSymbol, cmd.WaypointSymbol, err)
	}
	return positioned, nil
}

// handlePositionFailure surfaces a positioning failure. A cancellation is a
// graceful shutdown: the operation is left as-is so recovery re-adopts it at the
// next boot (RULINGS #2), and the error is returned for a clean container exit. A
// genuine failure is durable: the operation is marked FAILED with the reason
// (restart-resilient via LastError, and skipped by RecoverStorageOperations'
// running-only scan so no phantom storage ship is rebuilt), then returned so the
// container runner's bounded restart budget + backoff terminalizes it loudly. The
// hull is NEVER registered on this path.
func (h *RunWarehouseHandler) handlePositionFailure(
	ctx context.Context,
	cmd *RunWarehouseCommand,
	operation *storage.StorageOperation,
	posErr error,
	logger common.ContainerLogger,
) error {
	if ctx.Err() != nil || errors.Is(posErr, context.Canceled) {
		logger.Log("INFO", "Warehouse hull positioning canceled (shutdown); operation left resumable", map[string]interface{}{
			"action":      "position_canceled",
			"ship_symbol": cmd.ShipSymbol,
			"operation":   cmd.OperationID,
		})
		return posErr
	}

	logger.Log("ERROR", "Warehouse hull could not be positioned at its home waypoint; marking operation blocked", map[string]interface{}{
		"action":      "warehouse_blocked",
		"ship_symbol": cmd.ShipSymbol,
		"waypoint":    cmd.WaypointSymbol,
		"operation":   cmd.OperationID,
		"error":       posErr.Error(),
	})
	if failErr := operation.Fail(posErr); failErr != nil {
		logger.Log("WARNING", "Failed to mark warehouse operation blocked", map[string]interface{}{
			"operation": cmd.OperationID,
			"error":     failErr.Error(),
		})
	} else if updateErr := h.storageOpRepo.Update(ctx, operation); updateErr != nil {
		logger.Log("WARNING", "Failed to persist blocked warehouse operation", map[string]interface{}{
			"operation": cmd.OperationID,
			"error":     updateErr.Error(),
		})
	}
	return posErr
}

// sleepOrCancel waits d against the injected clock, returning early with the
// context error if the container is shutting down. Instant under the test
// MockClock; a real, ctx-interruptible sleep in production.
func (h *RunWarehouseHandler) sleepOrCancel(ctx context.Context, d time.Duration) error {
	done := make(chan struct{})
	go func() {
		h.clock.Sleep(d)
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
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
		switch {
		case existing.IsPending():
			if startErr := existing.Start(); startErr == nil {
				_ = h.storageOpRepo.Update(ctx, existing)
			}
		case existing.Status() == storage.OperationStatusFailed:
			// A prior bring-up gave up and durably blocked this operation. A restart
			// gets a fresh attempt: clear the FAILED state so a transient positioning
			// failure recovers and ends RUNNING, while a genuine one re-fails on this
			// pass.
			existing.ResetForRestart()
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

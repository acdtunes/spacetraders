package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// Warehouse-first construction sourcing. Before buying a gate material at market, the
// drain withdraws it from an in-system depot warehouse at zero marginal cost, enforcing
// "one controller per resource": a depot stocker is the sole buyer→warehouse and the construction
// coordinator only ever buys the RESIDUAL not covered by warehouse stock (RULINGS #4, no
// double-buy). The finder is the SHARED contract StorageInventoryFinder; the withdrawal itself
// mirrors the contract DeliveryExecutor's proven reserve→AlignAndTransferCargo→confirm shape.

// warehouseSourcing is the withdrawal seam's collaborator set. finder is the SHARED contract
// StorageInventoryFinder — NOT a divergent parallel one. All four are wired together by
// SetInventorySource; ANY left nil disables warehouse-first, so the existing coordinator tests and
// an unwired daemon buy at market exactly as today (byte-identical — the arm-safety property).
type warehouseSourcing struct {
	finder    contract.InventorySourceFinder
	storage   storage.StorageCoordinator
	api       domainPorts.APIClient
	navigator ConstructionNavigator
}

// enabled reports whether all four warehouse-first collaborators are wired. When false the drain
// skips the withdrawal seam entirely and buys at market exactly as today.
func (w warehouseSourcing) enabled() bool {
	return w.finder != nil && w.storage != nil && w.api != nil && w.navigator != nil
}

// trySourceFromWarehouse withdraws up to `needed` units of the task's material from an in-system
// depot warehouse at ZERO cost, mirroring the contract DeliveryExecutor's withdrawal shape
// (FindInSystemInventory -> reserve -> AlignAndTransferCargo -> confirm).
//
// Returns withdrew>0 (with the reloaded hull) when at least one unit lands aboard; the caller then
// delivers it and skips the market buy THIS trip. The residual re-stages, and a later trip withdraws
// more or buys once the warehouse drains, so a covered unit is NEVER also bought.
//
// FAIL-OPEN to the market path — RULINGS #4 fails closed on SPEND, never on supply, so the gate is
// never starved. Every no-inventory shape and every error returns withdrew=0 so the caller buys
// instead, and a failed transfer releases its whole reservation so no inventory is lost.
func (h *RunConstructionCoordinatorHandler) trySourceFromWarehouse(
	ctx context.Context,
	task *manufacturing.ManufacturingTask,
	ship *navigation.Ship,
	systemSymbol string,
	playerID shared.PlayerID,
	needed int,
) (int, *navigation.Ship, error) {
	if !h.warehouse.enabled() || needed <= 0 {
		return 0, ship, nil
	}
	logger := common.LoggerFromContext(ctx)
	good := task.Good()

	// Decision read: an in-system warehouse holding this good? In-system only (RULINGS #14) — the
	// finder never returns an out-of-system warehouse, so the withdrawal is a single-system hop.
	src := h.warehouse.finder.FindInSystemInventory(ctx, playerID.Value(), systemSymbol, good)
	if src == nil {
		return 0, ship, nil // no inventory -> market path
	}

	availableSpace := ship.Cargo().Capacity - ship.Cargo().Units
	if availableSpace <= 0 {
		return 0, ship, nil
	}
	want := min(availableSpace, needed)
	if want <= 0 {
		return 0, ship, nil
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, ship, fmt.Errorf("no player token for construction warehouse withdrawal: %w", err)
	}

	// Fly to the warehouse so the ship-to-ship transfer can run there.
	moved, err := h.warehouse.navigator.NavigateAndDock(ctx, ship.ShipSymbol(), src.StorageWaypoint, playerID)
	if err != nil {
		return 0, ship, fmt.Errorf("navigate to warehouse %s: %w", src.StorageWaypoint, err)
	}
	if moved != nil {
		ship = moved
	}

	// Reserve from the warehouse's storage hull(s). A drain between the finder read and here yields
	// no reservation -> fall through to market (fail-open).
	storageShip, reserved := h.warehouse.reserveFrom(src.OperationID, good)
	if storageShip == nil || reserved <= 0 {
		return 0, ship, nil
	}
	toMove := min(reserved, want)

	// WITHDRAWAL: the warehouse hull is the stationary source (RULINGS #7 — never claimed, only
	// transferred from); the construction hauler is the visitor aligned to it. A transfer error
	// releases the whole reservation and falls through to market.
	if _, _, err := common.AlignAndTransferCargo(ctx, h.warehouse.api, storageShip.ShipSymbol(), ship.ShipSymbol(), storageShip.ShipSymbol(), good, toMove, token); err != nil {
		if cancelErr := storageShip.CancelReservation(good, reserved); cancelErr != nil {
			logger.Log("ERROR", fmt.Sprintf("Construction withdrawal: cancel reservation after transfer error failed: %v", cancelErr), map[string]interface{}{
				"storage_ship": storageShip.ShipSymbol(), "good": good,
			})
		}
		return 0, ship, fmt.Errorf("transfer %d %s from warehouse ship %s: %w", toMove, good, storageShip.ShipSymbol(), err)
	}

	settleWarehouseReservation(ctx, storageShip, good, toMove, reserved)

	ship, err = h.syncHullsAfterWithdrawal(ctx, storageShip, ship, playerID)
	if err != nil {
		return 0, ship, err
	}

	logger.Log("INFO", fmt.Sprintf("Construction sourced %d %s from warehouse ship %s at zero cost - no market buy this trip", toMove, good, storageShip.ShipSymbol()), map[string]interface{}{
		"good": good, "units": toMove, "storage_op": src.OperationID, "ship": ship.ShipSymbol(),
	})
	return toMove, ship, nil
}

// settleWarehouseReservation commits the moved units and releases any over-reservation back to
// other workers. Both steps are best-effort: the cargo has already moved, so a bookkeeping failure
// must not fail the trip.
func settleWarehouseReservation(ctx context.Context, storageShip *storage.StorageShip, good string, toMove, reserved int) {
	logger := common.LoggerFromContext(ctx)
	if err := storageShip.ConfirmTransfer(good, toMove); err != nil {
		logger.Log("ERROR", fmt.Sprintf("Construction withdrawal: confirm transfer failed (cargo already moved): %v", err), map[string]interface{}{
			"storage_ship": storageShip.ShipSymbol(), "good": good,
		})
	}
	if excess := reserved - toMove; excess > 0 {
		if err := storageShip.CancelReservation(good, excess); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Construction withdrawal: release over-reservation failed: %v", err), map[string]interface{}{
				"storage_ship": storageShip.ShipSymbol(), "good": good, "excess": excess,
			})
		}
	}
}

// syncHullsAfterWithdrawal persists both hulls' cargo. A storage-hull sync miss is best-effort; a
// hauler sync failure is surfaced so the caller treats the trip as fail-open rather than delivering
// against a stale hold.
func (h *RunConstructionCoordinatorHandler) syncHullsAfterWithdrawal(ctx context.Context, storageShip *storage.StorageShip, ship *navigation.Ship, playerID shared.PlayerID) (*navigation.Ship, error) {
	if _, err := h.shipRepo.SyncShipFromAPI(ctx, storageShip.ShipSymbol(), playerID); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Construction withdrawal: storage-hull sync failed: %v", err), map[string]interface{}{
			"storage_ship": storageShip.ShipSymbol(),
		})
	}
	reloaded, err := h.shipRepo.SyncShipFromAPI(ctx, ship.ShipSymbol(), playerID)
	if err != nil {
		return ship, fmt.Errorf("sync hauler after warehouse withdrawal: %w", err)
	}
	if reloaded != nil {
		return reloaded, nil
	}
	return ship, nil
}

// reserveFrom reserves all unreserved units of good on the first storage hull in the
// operation that holds any, returning that hull and the amount reserved (0 if none). Mirrors the
// contract DeliveryExecutor: the caller MUST ConfirmTransfer the moved units and CancelReservation
// any remainder. The per-hull reservation is atomic (TryReserveCargo holds the hull mutex), so two
// construction lots racing the same units cannot double-claim — one reserves, the other sees them
// gone and falls through to market.
func (w warehouseSourcing) reserveFrom(operationID, good string) (*storage.StorageShip, int) {
	for _, s := range w.storage.GetStorageShipsForOperation(operationID) {
		if s == nil {
			continue
		}
		reserved, err := s.TryReserveCargo(good, 1)
		if err == nil && reserved > 0 {
			return s, reserved
		}
	}
	return nil, 0
}

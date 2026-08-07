package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// trySourceFromInventory attempts to fill this source trip from an in-system warehouse instead of
// buying at market. It returns withdrew=true (with the reloaded ship) when at least one unit lands
// aboard, so the caller skips the market buy for this trip and lets the delivery loop re-source the
// remainder. It returns withdrew=false for EVERY no-inventory case (feature off, no stock, drained
// mid-flight) so the caller uses the market path, and a non-nil error only for an unexpected
// failure the caller logs and still treats as fail-open (never a skip, RULINGS #1).
//
// Withdrawal mirrors the manufacturing STORAGE_ACQUIRE_DELIVER shape (TryReserveCargo ->
// TransferCargo -> ConfirmTransfer) but BOUNDS the take to what the contract needs this trip: it
// reserves what the storage ship holds, transfers only min(reserved, hold space, units needed), and
// releases the excess reservation so other workers are not starved. The warehouse hull is the
// storage operation's own dedicated, claimed ship (RULINGS #7) — the contract worker only transfers
// from it, never claims it. The per-ship reservation is atomic (TryReserveCargo holds the
// storage-ship mutex), so two contracts racing the same units cannot double-claim: one reserves,
// the other sees them gone and falls through.
func (e *DeliveryExecutor) trySourceFromInventory(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	ship *navigation.Ship,
	delivery domainContract.Delivery,
	unitsToPurchase int,
	profitabilityResp common.Response,
	contractID string,
) (bool, *navigation.Ship, error) {
	// Not wired (existing tests / feature off) -> market path, no error.
	if e.invFinder == nil || e.storageCoordinator == nil || e.apiClient == nil {
		return false, ship, nil
	}

	logger := common.LoggerFromContext(ctx)
	good := delivery.TradeSymbol
	deliverySystem := shared.ExtractSystemSymbol(delivery.DestinationSymbol)

	// Decision read: in-system warehouse stock for this good? (in-system only,
	// RULINGS #14 — the finder never returns an out-of-system warehouse.)
	src := e.invFinder.FindInSystemInventory(ctx, playerID.Value(), deliverySystem, good)
	if src == nil {
		return false, ship, nil // no inventory -> market path
	}

	availableSpace := ship.Cargo().Capacity - ship.Cargo().Units
	if availableSpace <= 0 {
		return false, ship, nil // no hold space -> let the market path decide
	}
	want := utils.Min(availableSpace, unitsToPurchase)
	if want <= 0 {
		return false, ship, nil
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return false, ship, fmt.Errorf("no player token for inventory withdrawal: %w", err)
	}

	// Fly to the warehouse (in-system) and stay in orbit for the ship-to-ship
	// transfer.
	ship, err = e.navigateToWaypoint(ctx, shipSymbol, src.StorageWaypoint, playerID)
	if err != nil {
		return false, ship, fmt.Errorf("navigate to warehouse %s: %w", src.StorageWaypoint, err)
	}
	if err := e.orbitForTransfer(ctx, ship, playerID); err != nil {
		return false, ship, fmt.Errorf("orbit at warehouse %s: %w", src.StorageWaypoint, err)
	}

	// Reserve from the warehouse's storage ship(s). A drain between the finder
	// read and here yields no reservation -> fall through to market (fail-open).
	storageShip, reserved := e.reserveFromWarehouse(src.OperationID, good)
	if storageShip == nil || reserved <= 0 {
		return false, ship, nil
	}

	toMove := utils.Min(reserved, want)

	// Align nav state before the ship-to-ship transfer. This is a WITHDRAWAL:
	// the warehouse hull (storageShip) is the stationary source, the contract worker
	// (shipSymbol) is the visitor. SpaceTraders rejects the transfer with API 4271 unless
	// both hulls share a nav state, so the visitor is orbited/docked to match the warehouse
	// (never moved); a 4271 race is re-aligned and retried once rather than crashing.
	if _, _, err := common.AlignAndTransferCargo(ctx, e.apiClient, storageShip.ShipSymbol(), shipSymbol, storageShip.ShipSymbol(), good, toMove, token); err != nil {
		// Release the whole reservation and fall through to market (fail-open).
		if cancelErr := storageShip.CancelReservation(good, reserved); cancelErr != nil {
			logger.Log("ERROR", "Inventory withdrawal: failed to cancel reservation after transfer error", map[string]interface{}{
				"ship_symbol":  shipSymbol,
				"storage_ship": storageShip.ShipSymbol(),
				"error":        cancelErr.Error(),
			})
		}
		return false, ship, fmt.Errorf("transfer %d %s from warehouse ship %s: %w", toMove, good, storageShip.ShipSymbol(), err)
	}

	// Commit the moved units; release any over-reservation for other workers.
	if err := storageShip.ConfirmTransfer(good, toMove); err != nil {
		logger.Log("ERROR", "Inventory withdrawal: confirm transfer failed (cargo already moved)", map[string]interface{}{
			"ship_symbol":  shipSymbol,
			"storage_ship": storageShip.ShipSymbol(),
			"error":        err.Error(),
		})
	}
	if excess := reserved - toMove; excess > 0 {
		if err := storageShip.CancelReservation(good, excess); err != nil {
			logger.Log("WARN", "Inventory withdrawal: failed to release over-reservation", map[string]interface{}{
				"storage_ship": storageShip.ShipSymbol(),
				"excess":       excess,
				"error":        err.Error(),
			})
		}
	}

	// Emit the withdrawal as a structured economic event now that the
	// draw has physically moved (TransferCargo) and committed (ConfirmTransfer) —
	// on the ACTUAL successful draw, never on intent. This is the record downstream
	// warehouse-ROI analysis reads (buffer hit-rate, served-from-buffer,
	// contract-leg-avoided).
	e.recordWithdrawal(ctx, storage.WithdrawalEvent{
		Good:       good,
		Units:      toMove,
		Waypoint:   src.StorageWaypoint,
		Ship:       shipSymbol,
		ContractID: contractID,
		PlayerID:   playerID.Value(),
	})

	// Persist both ships' cargo state (mirror manufacturing).
	if _, err := e.shipRepo.SyncShipFromAPI(ctx, storageShip.ShipSymbol(), playerID); err != nil {
		logger.Log("WARN", "Inventory withdrawal: failed to sync storage ship after transfer", map[string]interface{}{
			"storage_ship": storageShip.ShipSymbol(),
			"error":        err.Error(),
		})
	}
	reloaded, err := e.shipRepo.SyncShipFromAPI(ctx, shipSymbol, playerID)
	if err != nil {
		return false, ship, fmt.Errorf("sync hauler after withdrawal: %w", err)
	}

	// Honest accounting (sp-dchv): withdrawn goods cost the contract engine ZERO
	// at withdrawal (basis sunk at deposit). The market ask this trip AVOIDED is
	// the realized-savings line the captain reads. marketAsk is best-effort (0
	// when no market has been priced for the good yet).
	marketAsk := marketAskBestEffort(profitabilityResp, good)
	logger.Log("INFO", fmt.Sprintf(
		"Sourced %d %s from warehouse ship %s at zero ask (market would have cost %d @ %d/unit) - realized savings, contract sourcing cost 0",
		toMove, good, storageShip.ShipSymbol(), marketAsk*toMove, marketAsk,
	), map[string]interface{}{
		"ship_symbol":     shipSymbol,
		"action":          "inventory_withdrawal",
		"trade_symbol":    good,
		"units_withdrawn": toMove,
		"storage_op":      src.OperationID,
		"market_ask":      marketAsk,
		"savings":         marketAsk * toMove,
	})

	return true, reloaded, nil
}

// recordWithdrawal emits one warehouse→hauler withdrawal event on the
// actual successful draw, stamping it with the executor's clock. It is additive
// instrumentation: a nil recorder is a no-op, and a persistence error is logged
// and swallowed so telemetry can never fail a draw whose goods are already aboard
// (fail-open, RULINGS #1). withdrawalClock is guaranteed non-nil whenever the
// recorder is wired (WithWithdrawalRecorder sets both).
func (e *DeliveryExecutor) recordWithdrawal(ctx context.Context, event storage.WithdrawalEvent) {
	if e.withdrawalRecorder == nil {
		return
	}
	event.WithdrawnAt = e.withdrawalClock.Now()
	if err := e.withdrawalRecorder.Record(ctx, event); err != nil {
		common.LoggerFromContext(ctx).Log("WARN", "Withdrawal event record failed (draw succeeded; telemetry only)", map[string]interface{}{
			"ship_symbol":  event.Ship,
			"trade_symbol": event.Good,
			"units":        event.Units,
			"error":        err.Error(),
		})
	}
}

// reserveFromWarehouse reserves all unreserved units of good on the first
// storage ship in the operation that holds any, returning that ship and the
// amount reserved (0 if none). The caller MUST ConfirmTransfer the moved units
// and CancelReservation any remainder.
func (e *DeliveryExecutor) reserveFromWarehouse(operationID, good string) (*storage.StorageShip, int) {
	for _, s := range e.storageCoordinator.GetStorageShipsForOperation(operationID) {
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

// orbitForTransfer ensures the ship is in orbit (not docked) so a ship-to-ship
// cargo transfer can run at the warehouse waypoint. A ship already in orbit is a
// no-op.
func (e *DeliveryExecutor) orbitForTransfer(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	if ship != nil && !ship.IsDocked() {
		return nil
	}
	orbitCmd := &shipTypes.OrbitShipCommand{Ship: ship, PlayerID: playerID}
	if _, err := e.mediator.Send(ctx, orbitCmd); err != nil {
		return err
	}
	return nil
}

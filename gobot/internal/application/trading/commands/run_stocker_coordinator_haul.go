package commands

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// buy travels to the picked foreign market (multi-jump, jump-safe), docks, re-checks the
// working-capital floor against the live hold, and buys under the sp-9mkf per-tranche
// live-ask ceiling (fail-closed): the ask is re-verified at the dock and the remainder
// aborted if it ladders past the miner's foreign ask + tolerance, BEFORE overspending.
// Returns the units bought (0 on any guarded skip), and a non-nil error only on an
// operational failure.
func (h *RunStockerCoordinatorHandler) buy(
	ctx context.Context,
	cmd *RunStockerCoordinatorCommand,
	pick stockerPick,
	response *RunStockerCoordinatorResponse,
	reserve int64,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return 0, err
	}
	ship, err = h.legs.travel(ctx, ship, pick.ForeignMarket, cmd.PlayerID)
	if err != nil {
		return 0, fmt.Errorf("travel to market %s failed: %w", pick.ForeignMarket, err)
	}
	if err := h.legs.dock(ctx, ship, cmd.PlayerID); err != nil {
		return 0, fmt.Errorf("dock at market %s failed: %w", pick.ForeignMarket, err)
	}

	// Re-size against the live hold post-dock.
	ship, err = h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return 0, err
	}
	units := pick.Units
	if space := ship.AvailableCargoSpace(); space < units {
		units = space
	}
	if units <= 0 {
		return 0, nil
	}

	// Working-capital spend floor (RULINGS #4), reusing the delegated guard: never drop
	// live treasury below the reserve. Fails closed on any live-read failure.
	projectedCost := units * pick.ForeignAsk
	if h.legs.spendFloorBreached(ctx, cmd.PlayerID, projectedCost, int(reserve), &RunTradeRouteCoordinatorResponse{}) {
		logger.Log("WARNING", fmt.Sprintf("Stocker: buy of %d %s @ %d would breach working-capital floor %d - skipping", units, pick.Good, pick.ForeignAsk, reserve), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "good": pick.Good, "units": units, "ask": pick.ForeignAsk, "reserve": reserve,
		})
		return 0, nil
	}

	// sp-9mkf live-verify: arm the per-tranche ceiling at the miner's foreign ask plus
	// tolerance, so a laddered/stale live ask aborts the remainder fail-closed before spend.
	maxAskPerUnit := pick.ForeignAsk + pick.ForeignAsk*tourPriceTolerancePct/100
	buyResp, err := h.legs.purchaseWithCeiling(ctx, cmd.ShipSymbol, pick.Good, units, cmd.PlayerID, maxAskPerUnit, scanDedupBracket{}) // no scan-dedup for this leg
	if err != nil {
		return 0, fmt.Errorf("purchase of %d %s at %s failed: %w", units, pick.Good, pick.ForeignMarket, err)
	}
	if buyResp.UnitsAdded == 0 && buyResp.CeilingAborted {
		logger.Log("WARNING", fmt.Sprintf("Stocker: buy ceiling aborted %s at %s (live ask %d > ceiling %d) - skipping this pass",
			pick.Good, pick.ForeignMarket, buyResp.CeilingObservedAsk, maxAskPerUnit), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "good": pick.Good, "live_ask": buyResp.CeilingObservedAsk, "ceiling": maxAskPerUnit,
		})
		return 0, nil
	}
	response.TotalSpent += int64(buyResp.TotalCost)
	logger.Log("INFO", fmt.Sprintf("Stocker: bought %d %s at %s (cost %d, live-verified)", buyResp.UnitsAdded, pick.Good, pick.ForeignMarket, buyResp.TotalCost), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "good": pick.Good, "units": buyResp.UnitsAdded, "cost": buyResp.TotalCost, "market": pick.ForeignMarket,
	})
	return buyResp.UnitsAdded, nil
}

// haulAndDeposit travels the hull home to the warehouse waypoint (multi-jump, jump-safe),
// docks, and deposits every held good the warehouse supports via the Lane B protocol
// (ReserveSpaceForDeposit → TransferCargo → ConfirmDeposit). Before depositing, any held
// good NO co-located member supports at all is JETTISONED (sp-bvoc5) — cargo INHERITED
// from a prior role (e.g. a hauler re-seated as this depot's stocker still carrying its old
// FABRICS), which this warehouse group can structurally never accept, so leaving it aboard
// would loop the resume-safe deposit-before-buy path forever. A SUPPORTED good that is
// merely undepositable THIS PASS (every member transiently full) is never jettisoned — it
// is left aboard exactly as before (it will report stranded at the final exit if that
// persists). Returns the total units deposited (jettisoned units are not counted as
// deposited). source is the market the just-bought cargo came from, threaded onto each
// emitted stock-IN event; it is "" on the resume path, where the aboard cargo was
// bought in a prior run and its provenance is unknown.
func (h *RunStockerCoordinatorHandler) haulAndDeposit(
	ctx context.Context,
	cmd *RunStockerCoordinatorCommand,
	group []*storage.StorageOperation,
	response *RunStockerCoordinatorResponse,
	depositedGoods map[string]bool,
	source string,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return 0, err
	}
	ship, err = h.legs.travel(ctx, ship, cmd.WarehouseWaypoint, cmd.PlayerID)
	if err != nil {
		return 0, fmt.Errorf("travel to warehouse %s failed: %w", cmd.WarehouseWaypoint, err)
	}
	if err := h.legs.dock(ctx, ship, cmd.PlayerID); err != nil {
		return 0, fmt.Errorf("dock at warehouse %s failed: %w", cmd.WarehouseWaypoint, err)
	}

	// Reload post-dock and snapshot the held goods in deterministic order.
	ship, err = h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return 0, err
	}
	cargo := ship.Cargo()
	if cargo == nil {
		return 0, nil
	}
	heldByGood := map[string]int{}
	var goods []string
	for _, item := range cargo.Inventory {
		if item.Units > 0 {
			if _, seen := heldByGood[item.Symbol]; !seen {
				goods = append(goods, item.Symbol)
			}
			heldByGood[item.Symbol] += item.Units
		}
	}
	sort.Strings(goods)

	// Clear any INHERITED non-depot cargo BEFORE attempting a deposit (sp-bvoc5): a good no
	// co-located member supports can NEVER be selected by SelectDepositWarehouse below, so
	// trying first would just re-log "held aboard" forever. Deleting the jettisoned good from
	// heldByGood makes the deposit loop below skip it silently (remaining=0).
	h.jettisonUnsupportedCargo(ctx, cmd, group, heldByGood)

	total := 0
	for _, good := range goods {
		// Deposit the good into the co-located group, spilling from the newest member
		// with space into the next as each fills (additive capacity). The
		// remainder is held aboard ONLY when the WHOLE group is full or no member
		// supports the good — that is the sole "warehouse full" condition now.
		remaining := heldByGood[good]
		for remaining > 0 {
			dst := tradingsvc.SelectDepositWarehouse(h.storageCoordinator, group, good)
			if dst == nil {
				logger.Log("WARNING", fmt.Sprintf("Stocker: no co-located warehouse at %s can accept %d %s (all full or unsupported) - held aboard (reports stranded if undeposited at exit)", cmd.WarehouseWaypoint, remaining, good), map[string]interface{}{
					"ship_symbol": cmd.ShipSymbol, "warehouse_waypoint": cmd.WarehouseWaypoint, "good": good, "units": remaining, "group_size": len(group),
				})
				break
			}
			deposited, derr := h.depositGood(ctx, cmd, dst, good, remaining, response, source)
			if derr != nil {
				return total, derr
			}
			if deposited <= 0 {
				break // race: space vanished between select and reserve — hold the rest
			}
			remaining -= deposited
			total += deposited
			depositedGoods[good] = true
		}
	}
	return total, nil
}

// jettisonUnsupportedCargo clears held cargo NO co-located warehouse member declares
// support for (sp-bvoc5) — inherited non-depot goods left aboard by a prior role, never a
// good this depot could ever accept. This is what stops the resume-safe deposit-before-buy
// path from looping "held aboard" forever (the live TORWIND-10 incident: 36 FABRICS aboard
// a hull re-seated as the X1-UM5 stocker). Uses AnySupportsGood — a PURE structural check,
// never capacity — so a SUPPORTED good that is merely full everywhere right now is NEVER
// jettisoned (RULINGS #6/#9: only non-supported inherited cargo is cleared, never a
// supported depot good, never sold/jettisoned speculatively). A jettison failure is logged
// and the good stays aboard for the next pass to retry, rather than aborting the round trip.
func (h *RunStockerCoordinatorHandler) jettisonUnsupportedCargo(
	ctx context.Context,
	cmd *RunStockerCoordinatorCommand,
	group []*storage.StorageOperation,
	heldByGood map[string]int,
) {
	logger := common.LoggerFromContext(ctx)
	for good, units := range heldByGood {
		if units <= 0 || tradingsvc.AnySupportsGood(group, good) {
			continue
		}
		if _, err := h.mediator.Send(ctx, &shipCargo.JettisonCargoCommand{
			ShipSymbol: cmd.ShipSymbol,
			PlayerID:   shared.MustNewPlayerID(cmd.PlayerID),
			GoodSymbol: good,
			Units:      units,
		}); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Stocker: failed to jettison %d unsupported inherited %s aboard %s - left aboard, will retry next pass: %v", units, good, cmd.ShipSymbol, err), map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol, "warehouse_waypoint": cmd.WarehouseWaypoint, "good": good, "units": units, "error": err.Error(),
			})
			continue
		}
		delete(heldByGood, good) // cleared — the deposit loop below must skip it, not re-select it
		logger.Log("INFO", fmt.Sprintf("Stocker: jettisoned %d %s - inherited from a prior role, not in this depot's supported goods (sp-bvoc5)", units, good), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "warehouse_waypoint": cmd.WarehouseWaypoint, "good": good, "units": units,
		})
	}
}

// depositGood deposits units of good into the warehouse using the gas-proven protocol:
// ReserveSpaceForDeposit → TransferCargo (API) → ConfirmDeposit, releasing the
// reservation on transfer failure. It books ZERO revenue — the capital was already sunk
// at the buy, so a deposit is a pure inventory move (no ledger transaction row). A
// warehouse with no space leaves the cargo aboard (returns 0) so it reports stranded at
// the final exit rather than being silently dropped.
func (h *RunStockerCoordinatorHandler) depositGood(
	ctx context.Context,
	cmd *RunStockerCoordinatorCommand,
	op *storage.StorageOperation,
	good string,
	units int,
	response *RunStockerCoordinatorResponse,
	source string,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	storageShip, reserved, ok := h.storageCoordinator.ReserveSpaceForDeposit(op.ID(), units)
	if !ok || storageShip == nil {
		logger.Log("WARNING", fmt.Sprintf("Stocker: warehouse %s has no space for %d %s - held aboard (reports stranded if undeposited at exit)", op.ID(), units, good), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "warehouse": op.ID(), "good": good, "units": units,
		})
		return 0, nil
	}
	if reserved < units {
		units = reserved
	}

	if _, terr := h.mediator.Send(ctx, &gasCmd.TransferCargoCommand{
		FromShip:   cmd.ShipSymbol,
		ToShip:     storageShip.ShipSymbol(),
		GoodSymbol: good,
		Units:      units,
		PlayerID:   shared.MustNewPlayerID(cmd.PlayerID),
	}); terr != nil {
		h.storageCoordinator.ReleaseReservedSpace(storageShip.ShipSymbol(), reserved)
		return 0, fmt.Errorf("deposit transfer of %d %s to warehouse hull %s failed: %w", units, good, storageShip.ShipSymbol(), terr)
	}
	h.storageCoordinator.ConfirmDeposit(storageShip.ShipSymbol(), good, units)

	// Emit the deposit as a structured stock-IN event now that the transfer has
	// physically moved (TransferCargo) and committed (ConfirmDeposit) — on the ACTUAL confirmed
	// deposit, never on intent. This is the stock-IN mirror of kqxe's withdrawal event, read
	// downstream to measure depot throughput/coverage and (differenced against draws) current
	// fill. Additive + fail-open: a nil recorder is a no-op and a record error is swallowed.
	h.recordStocking(ctx, storage.StockingEvent{
		Good:              good,
		Units:             units,
		WarehouseWaypoint: storageShip.WaypointSymbol(),
		SourceWaypoint:    source,
		Ship:              cmd.ShipSymbol,
		PlayerID:          cmd.PlayerID,
	})

	response.UnitsDeposited += units
	logger.Log("INFO", fmt.Sprintf("Stocker: deposited %d %s into warehouse %s (no revenue, capital booked at buy)", units, good, storageShip.WaypointSymbol()), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "good": good, "units": units, "warehouse": op.ID(),
		"storage_ship": storageShip.ShipSymbol(), "operation_type": "warehouse_deposit",
	})
	return units, nil
}

// recordStocking emits one stocker→warehouse deposit event on the actual CONFIRMED
// deposit, stamping it with the handler's clock. It is additive instrumentation mirroring
// kqxe's recordWithdrawal: a nil recorder is a no-op, and a persistence error is logged and
// swallowed so telemetry can never fail a deposit whose goods are already physically in the
// warehouse (fail-open, RULINGS #1).
func (h *RunStockerCoordinatorHandler) recordStocking(ctx context.Context, event storage.StockingEvent) {
	if h.stockingRecorder == nil {
		return
	}
	event.DepositedAt = h.clock.Now()
	if err := h.stockingRecorder.Record(ctx, event); err != nil {
		common.LoggerFromContext(ctx).Log("WARN", "Stocking event record failed (deposit succeeded; telemetry only)", map[string]interface{}{
			"ship_symbol":  event.Ship,
			"trade_symbol": event.Good,
			"units":        event.Units,
			"warehouse":    event.WarehouseWaypoint,
			"error":        err.Error(),
		})
	}
}

// warehousesAt returns ALL RUNNING warehouse operations parked at waypoint — the
// co-located additive-capacity group (sp-5q2c: e.g. light-12's 80 slots + heavy-4B's
// 225 at E42, whose capacity and stock sum). Empty when none is running there (fail
// closed — the caller treats the pass as empty). A stale sp-3lj5 zombie row (a
// container stopped without its storage_operations row terminalized) is included but
// contributes 0 free space and 0 stock to every aggregate and is never chosen as a
// deposit target, so aggregation composes with the newest-wins zombie fix.
func (h *RunStockerCoordinatorHandler) warehousesAt(ctx context.Context, playerID int, waypoint string) []*storage.StorageOperation {
	ops, err := h.warehouseFinder.FindRunning(ctx, playerID)
	if err != nil {
		return nil
	}
	return tradingsvc.RunningWarehousesAtWaypoint(ops, waypoint)
}

// heldUnits reports the total units of cargo aboard (0 when the hold is empty) — the
// laden check the resume-safe first move reads to know it must deposit before buying.
func heldUnits(ship *navigation.Ship) int {
	c := ship.Cargo()
	if c == nil {
		return 0
	}
	total := 0
	for _, item := range c.Inventory {
		total += item.Units
	}
	return total
}

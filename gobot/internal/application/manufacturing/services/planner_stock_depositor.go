package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// C1 (sp-64je) — planner-visible stock, producer side. Instead of selling
// harvested factory output at a market sink, deposit it into a CO-LOCATED
// warehouse at cost basis so the tour solver can later withdraw it at basis
// (killing the export-ask-subsidy inversion where tours buy our own output at
// laddered asks). The deposit reuses the proven reserve -> transfer -> confirm
// protocol (tour executeDeposit / siphon), recording the basis via
// ConfirmDepositWithBasis.
//
// Fails SAFE everywhere: whenever it cannot or should not deposit — no
// co-located warehouse, no space, over the capital ceiling, unreadable treasury
// — it declines (deposited=false, nil err) and the caller sells at market
// exactly as before. The feature is LIVE BY DEFAULT (Admiral: no dark-shipping);
// the coordinator runs this path unless the planner_stock_disabled escape hatch
// is set, and it is a no-op if the depositor is not wired at all.

// factoryDepositWorkingCapitalReserve mirrors the non-tunable 50k working-capital
// reserve (sp-bp6f) that the pre-positioning capital ceiling is held junior to.
const factoryDepositWorkingCapitalReserve = 50000

// StorageDepositCoordinator is the subset of the storage coordinator the factory
// deposit path uses. Satisfied by *storage.InMemoryStorageCoordinator.
type StorageDepositCoordinator interface {
	GetStorageShipsForOperation(operationID string) []*storage.StorageShip
	GetTotalCargoAvailable(operationID, goodSymbol string) int
	GetCostBasis(operationID, goodSymbol string) (int, bool)
	ReserveSpaceForDeposit(operationID string, units int) (*storage.StorageShip, int, bool)
	ReleaseReservedSpace(shipSymbol string, units int)
	ConfirmDepositWithBasis(shipSymbol, goodSymbol string, units, unitBasis int)
}

// WarehouseOpLister returns the player's running storage operations (the storage
// operation repository).
type WarehouseOpLister interface {
	FindRunning(ctx context.Context, playerID int) ([]*storage.StorageOperation, error)
}

// PlannerStockDepositor deposits harvested factory output into a co-located
// warehouse at cost basis, respecting the pre-positioning capital ceiling.
type PlannerStockDepositor struct {
	coordinator       StorageDepositCoordinator
	opLister          WarehouseOpLister
	mediator          common.Mediator
	apiClient         ports.APIClient
	capitalCeilingPct int
}

// NewPlannerStockDepositor builds the depositor. apiClient reads live treasury
// for the capital ceiling; capitalCeilingPct is contract.pre_positioning.
// capital_ceiling_pct (<=0 disables deposits — fail closed).
func NewPlannerStockDepositor(
	coordinator StorageDepositCoordinator,
	opLister WarehouseOpLister,
	mediator common.Mediator,
	apiClient ports.APIClient,
	capitalCeilingPct int,
) *PlannerStockDepositor {
	return &PlannerStockDepositor{
		coordinator:       coordinator,
		opLister:          opLister,
		mediator:          mediator,
		apiClient:         apiClient,
		capitalCeilingPct: capitalCeilingPct,
	}
}

// DepositOutput attempts to deposit `units` of `good` from `shipSymbol` (parked
// at `waypoint`, where the output was just harvested) into a co-located
// warehouse at `unitBasis`. Returns deposited=true if any units were stocked;
// deposited=false (nil err) signals the caller to sell at market instead (no
// co-located warehouse, no space, over ceiling, or unreadable treasury). A
// transfer failure with nothing yet deposited is returned as an error.
func (d *PlannerStockDepositor) DepositOutput(
	ctx context.Context,
	playerID int,
	shipSymbol, waypoint, good string,
	units, unitBasis int,
) (bool, error) {
	if units <= 0 || unitBasis <= 0 {
		return false, nil
	}
	logger := common.LoggerFromContext(ctx)

	ops, err := d.opLister.FindRunning(ctx, playerID)
	if err != nil {
		return false, nil // can't read warehouses — sell (fail safe)
	}

	// Co-located warehouse group at the factory waypoint. The ship is already
	// here after harvest, so no navigation is needed (mirrors tour executeDeposit,
	// which only deposits into warehouses at the leg's own waypoint).
	group := warehousesAtWaypoint(ops, waypoint)
	if len(group) == 0 {
		return false, nil
	}

	// Capital-ceiling guard (RULINGS #4, fail closed -> sell): do not tie up more
	// than the ceiling in factory stock.
	depositValue := units * unitBasis
	if !d.withinCapitalCeiling(ctx, ops, depositValue) {
		logger.Log("INFO", fmt.Sprintf("planner-stock: %d %s deposit (value %d) would exceed the pre-positioning capital ceiling — selling instead", units, good, depositValue), map[string]interface{}{
			"good": good, "units": units, "deposit_value": depositValue, "waypoint": waypoint,
		})
		return false, nil
	}

	// Deposit across the co-located group, spilling from the newest member with
	// space into the next (additive capacity). Each member: reserve -> transfer
	// -> confirm-with-basis.
	deposited := 0
	for deposited < units {
		remaining := units - deposited
		dst := selectDepositWarehouse(d.coordinator, group, good)
		if dst == nil {
			break // every co-located member full or unsupported
		}
		storageShip, reserved, ok := d.coordinator.ReserveSpaceForDeposit(dst.ID(), remaining)
		if !ok || storageShip == nil {
			break // race: space vanished between select and reserve
		}
		move := reserved
		if move > remaining {
			move = remaining
		}
		if _, terr := d.mediator.Send(ctx, &gasCmd.TransferCargoCommand{
			FromShip:   shipSymbol,
			ToShip:     storageShip.ShipSymbol(),
			GoodSymbol: good,
			Units:      move,
			PlayerID:   shared.MustNewPlayerID(playerID),
		}); terr != nil {
			d.coordinator.ReleaseReservedSpace(storageShip.ShipSymbol(), reserved)
			if deposited > 0 {
				return true, nil // partial deposit already landed — keep what stuck
			}
			return false, fmt.Errorf("planner-stock deposit transfer of %d %s to warehouse hull %s failed: %w", move, good, storageShip.ShipSymbol(), terr)
		}
		d.coordinator.ConfirmDepositWithBasis(storageShip.ShipSymbol(), good, move, unitBasis)
		logger.Log("INFO", fmt.Sprintf("planner-stock: deposited %d %s into warehouse %s at basis %d (no market sale)", move, good, storageShip.ShipSymbol(), unitBasis), map[string]interface{}{
			"good": good, "units": move, "basis": unitBasis, "warehouse": dst.ID(),
			"storage_ship": storageShip.ShipSymbol(), "operation_type": "factory_stock_deposit",
		})
		deposited += move
	}

	return deposited > 0, nil
}

// withinCapitalCeiling reports whether depositing depositValue more credits of
// factory stock stays within the pre-positioning capital ceiling: capitalCeilingPct
// percent of LIVE treasury, held junior to the 50k working-capital reserve.
// Fail-closed (false) on a non-positive ceiling or unreadable treasury.
func (d *PlannerStockDepositor) withinCapitalCeiling(ctx context.Context, ops []*storage.StorageOperation, depositValue int) bool {
	pct := int64(d.capitalCeilingPct)
	if pct <= 0 || d.apiClient == nil {
		return false
	}
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return false
	}
	agent, err := d.apiClient.GetAgent(ctx, token)
	if err != nil {
		return false // unreadable treasury — fail closed
	}
	ceiling := int64(agent.Credits) * pct / 100
	if avail := int64(agent.Credits) - factoryDepositWorkingCapitalReserve; avail < ceiling {
		ceiling = avail // junior to the working-capital reserve
	}
	if ceiling < 0 {
		ceiling = 0
	}
	outstanding := warehouseCapitalOutstanding(d.coordinator, ops)
	return int64(outstanding)+int64(depositValue) <= ceiling
}

// warehouseCapitalOutstanding sums basis*units across the player's running
// warehouse operations — the credits currently tied up in factory stock. Goods
// with an unknown basis (contract/gas deposits) do not count.
func warehouseCapitalOutstanding(c StorageDepositCoordinator, ops []*storage.StorageOperation) int {
	outstanding := 0
	for _, op := range ops {
		if op.OperationType() != storage.OperationTypeWarehouse {
			continue
		}
		for _, good := range op.SupportedGoods() {
			basis, known := c.GetCostBasis(op.ID(), good)
			if !known {
				continue
			}
			outstanding += c.GetTotalCargoAvailable(op.ID(), good) * basis
		}
	}
	return outstanding
}

// warehousesAtWaypoint filters running operations to the co-located warehouse
// group at waypoint (parallels tradingsvc.RunningWarehousesAtWaypoint; inlined to
// keep the manufacturing package independent of trading/services).
func warehousesAtWaypoint(ops []*storage.StorageOperation, waypoint string) []*storage.StorageOperation {
	var group []*storage.StorageOperation
	for _, op := range ops {
		if op.OperationType() == storage.OperationTypeWarehouse && op.WaypointSymbol() == waypoint {
			group = append(group, op)
		}
	}
	return group
}

// selectDepositWarehouse picks the group member that supports good and has free
// space, newest-wins for the sp-3lj5 zombie-row avoidance (parallels
// tradingsvc.SelectDepositWarehouse; inlined per warehousesAtWaypoint).
func selectDepositWarehouse(c StorageDepositCoordinator, group []*storage.StorageOperation, good string) *storage.StorageOperation {
	var best *storage.StorageOperation
	for _, op := range group {
		if !op.SupportsGood(good) {
			continue
		}
		free := 0
		for _, s := range c.GetStorageShipsForOperation(op.ID()) {
			free += s.AvailableSpace()
		}
		if free <= 0 {
			continue
		}
		if best == nil || op.CreatedAt().After(best.CreatedAt()) ||
			(op.CreatedAt().Equal(best.CreatedAt()) && op.ID() > best.ID()) {
			best = op
		}
	}
	return best
}

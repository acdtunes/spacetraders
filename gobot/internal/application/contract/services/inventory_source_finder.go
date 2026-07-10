package services

import (
	"context"

	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// StorageInventoryFinder satisfies appContract.InventorySourceFinder by querying
// the shared storage coordinator + operation repository for a RUNNING WAREHOUSE
// operation (sp-dchv Lane B) in the delivery system that holds a contract good
// with unreserved units.
//
// Two invariants make it safe as a contract-sourcing seam:
//   - In-system ONLY (RULINGS #14): a warehouse in another system is never
//     returned, so a plan built on it can never dispatch a cross-gate leg.
//   - Fail-open (RULINGS #1, never-skip): a nil receiver, a nil dependency, a
//     repository read error, a dead/absent warehouse, or zero unreserved units
//     all return nil, so the caller uses the pre-existing market path. It reads
//     unreserved availability from the LIVE coordinator, so a stopped warehouse
//     whose storage ship is unregistered reports zero — a stale operations row
//     can never route a contract at an empty hull.
//
// Only OperationTypeWarehouse operations are considered: gas/mining storage
// buffers hold extracted goods at extraction sites, not contract goods at home,
// and must never be treated as a contract inventory source.
type StorageInventoryFinder struct {
	opRepo      storage.StorageOperationRepository
	coordinator storage.StorageCoordinator
}

// NewStorageInventoryFinder wires the finder to the shared storage operation
// repository and the shared in-memory storage coordinator (the same singleton
// the warehouse, gas, and manufacturing paths register their storage ships with).
func NewStorageInventoryFinder(opRepo storage.StorageOperationRepository, coordinator storage.StorageCoordinator) *StorageInventoryFinder {
	return &StorageInventoryFinder{opRepo: opRepo, coordinator: coordinator}
}

// FindInSystemInventory returns the in-system warehouse the contract worker should
// withdraw good from, or nil (fail-open). Nil-receiver-safe.
//
// Multi-warehouse (sp-5q2c): more than one warehouse may hold the good in the
// delivery system (additive capacity). The withdrawal targets the FULLEST hull — the
// one with the most unreserved units, breaking ties toward the newest operation — so
// a single trip moves the most and the choice is stable across reads; as that hull
// drains, the next read re-picks the new fullest. UnitsAvailable is the SUM of
// unreserved units across every in-system warehouse holding the good — the true total
// on hand, so a sibling's stock is never invisible to the sourcing gate. The withdrawal
// itself still reserves against the single chosen OperationID (bounded by that hull),
// which is correct: it is one physical ship-to-ship transfer, and the caller re-consults
// this finder each trip.
func (f *StorageInventoryFinder) FindInSystemInventory(ctx context.Context, playerID int, systemSymbol, good string) *appContract.InventorySource {
	if f == nil || f.opRepo == nil || f.coordinator == nil {
		return nil
	}

	ops, err := f.opRepo.FindByGood(ctx, playerID, good)
	if err != nil {
		return nil // fail-open: never park a contract on a warehouse read
	}

	var best *storage.StorageOperation
	bestUnits := 0
	totalUnits := 0
	for _, op := range ops {
		if op == nil || !op.IsRunning() {
			continue
		}
		// Warehouses only — gas/mining storage lives at extraction sites and
		// never buffers contract goods (sp-dchv Lane B is OperationTypeWarehouse).
		if op.OperationType() != storage.OperationTypeWarehouse {
			continue
		}
		// In-system only (RULINGS #14): withdrawal is a physical in-system hop.
		if shared.ExtractSystemSymbol(op.WaypointSymbol()) != systemSymbol {
			continue
		}
		if !op.SupportsGood(good) {
			continue
		}
		// Unreserved availability from the LIVE coordinator — subsumes container
		// liveness (a stopped warehouse's storage ship is unregistered ⇒ 0).
		available := f.coordinator.GetTotalCargoAvailable(op.ID(), good)
		if available <= 0 {
			continue
		}
		totalUnits += available
		// Fullest hull wins; tie -> newest operation (then higher ID) for determinism.
		if best == nil || available > bestUnits ||
			(available == bestUnits && (op.CreatedAt().After(best.CreatedAt()) ||
				(op.CreatedAt().Equal(best.CreatedAt()) && op.ID() > best.ID()))) {
			best = op
			bestUnits = available
		}
	}

	if best == nil {
		return nil
	}
	return &appContract.InventorySource{
		OperationID:     best.ID(),
		StorageWaypoint: best.WaypointSymbol(),
		UnitsAvailable:  totalUnits,
	}
}

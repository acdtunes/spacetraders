package services

import (
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// SelectNewestRunningWarehouse deterministically resolves ties when a caller's own
// filter (same waypoint, or same reachable-system graph) matches more than one
// operation in ops: it returns the one with the latest CreatedAt, breaking an exact
// timestamp tie by the higher ID for full determinism. Returns nil for an empty
// slice.
//
// Ties happen when a container stops without terminalizing its storage_operations row:
// the stale "zombie" row keeps reading RUNNING forever alongside its live replacement at
// the same waypoint. A naive first-match or lowest-ID pick can silently resolve to the
// dead operation - whose registered storage ships are gone, so it always reads back zero
// free space - instead of the live one, and the caller wrongly concludes the warehouse is
// full.
func SelectNewestRunningWarehouse(ops []*storage.StorageOperation) *storage.StorageOperation {
	var best *storage.StorageOperation
	for _, op := range ops {
		if best == nil || op.CreatedAt().After(best.CreatedAt()) ||
			(op.CreatedAt().Equal(best.CreatedAt()) && op.ID() > best.ID()) {
			best = op
		}
	}
	return best
}

// Multi-warehouse aggregation. The Admiral runs MORE THAN ONE warehouse container at a
// single waypoint for additive capacity (e.g. light-12's 80 slots + heavy-4B's 225 = 305
// at E42). The helpers below treat the RUNNING warehouse operations parked at ONE waypoint
// as a single co-located group: capacity and stock READ as the SUM across the group,
// deposits PICK the member with free space, and "full" is true ONLY when every member is
// full. Because a stale "zombie" row (a stopped container whose storage_operations row was
// never terminalized) has an unregistered storage ship, it contributes 0 free space and 0
// stock to every sum and is never chosen as a deposit target — so aggregation composes
// with, rather than reopens, the newest-wins zombie handling above (aggregation is across
// RUNNING ops only; the terminalization + RUNNING-filter guards still stand).

// The aggregation helpers read a warehouse operation's free space and per-good stocked
// units through WarehouseSpaceReader (defined in tour_deposit_candidates.go, satisfied
// by the shared StorageCoordinator).

// RunningWarehousesAtWaypoint returns the warehouse operations in ops parked at
// waypoint — the co-located additive-capacity group. Non-warehouse operations
// (gas/mining storage) and warehouses at other waypoints are excluded. Callers pass
// a RUNNING set (repository FindRunning), so the result is the running group.
func RunningWarehousesAtWaypoint(ops []*storage.StorageOperation, waypoint string) []*storage.StorageOperation {
	var group []*storage.StorageOperation
	for _, op := range ops {
		if op.OperationType() == storage.OperationTypeWarehouse && op.WaypointSymbol() == waypoint {
			group = append(group, op)
		}
	}
	return group
}

// RunningWarehousesInGraph returns the warehouse operations in ops whose system is
// inside allowedSystems (the tour graph). Non-warehouse operations and warehouses in
// out-of-graph systems are excluded.
func RunningWarehousesInGraph(ops []*storage.StorageOperation, allowedSystems []string) []*storage.StorageOperation {
	allowed := toSet(allowedSystems)
	var group []*storage.StorageOperation
	for _, op := range ops {
		if op.OperationType() != storage.OperationTypeWarehouse {
			continue
		}
		if !allowed[shared.ExtractSystemSymbol(op.WaypointSymbol())] {
			continue
		}
		group = append(group, op)
	}
	return group
}

// TotalFreeSpace sums the unreserved free space across every storage ship of every
// operation in group — the group's aggregate deposit capacity. A zombie op (its
// storage ship unregistered) contributes 0.
func TotalFreeSpace(space WarehouseSpaceReader, group []*storage.StorageOperation) int {
	total := 0
	for _, op := range group {
		for _, s := range space.GetStorageShipsForOperation(op.ID()) {
			total += s.AvailableSpace()
		}
	}
	return total
}

// TotalCapacity sums the REAL cargo_capacity of every storage hull across every operation
// in group — the auto-cap knapsack's capacity term C. It reads the live per-hull
// capacity (a heavy frame or an installed cargo module reports its true capacity, never an
// assumed 80), so a 2nd/3rd warehouse hull simply raises C and the optimizer buffers more.
// Unlike TotalFreeSpace this is the TOTAL buffer size independent of current stock — the
// per-good target_units are absolute holds that must fit the whole buffer, not just its free
// slots. A zombie op (its storage ship unregistered) contributes 0, mirroring TotalFreeSpace.
func TotalCapacity(space WarehouseSpaceReader, group []*storage.StorageOperation) int {
	total := 0
	for _, op := range group {
		for _, s := range space.GetStorageShipsForOperation(op.ID()) {
			total += s.CargoCapacity()
		}
	}
	return total
}

// TotalCargoAvailable sums the unreserved units of good stocked across every
// operation in group — the group's aggregate on-hand inventory, which the
// fill-target / units-short math must net against (target − aggregate) so a second
// warehouse's stock is never invisible. A zombie op contributes 0.
func TotalCargoAvailable(space WarehouseSpaceReader, group []*storage.StorageOperation, good string) int {
	total := 0
	for _, op := range group {
		total += space.GetTotalCargoAvailable(op.ID(), good)
	}
	return total
}

// AnySupportsGood reports whether any operation in group buffers good — the group is
// a stockable/depositable sink for good when at least one co-located member supports
// it.
func AnySupportsGood(group []*storage.StorageOperation, good string) bool {
	for _, op := range group {
		if op.SupportsGood(good) {
			return true
		}
	}
	return false
}

// SelectDepositWarehouse returns the member of a co-located group that can accept a
// deposit of good RIGHT NOW — it both SUPPORTS good and has free space — breaking
// ties toward the newest operation. Co-located members share a waypoint, so the
// "nearest" tiebreak is degenerate and newest (the zombie-avoidance order) decides.
// Returns nil ONLY when every member is full or unsupported, which is the
// sole condition under which a caller may report the warehouse full: capacity is
// horizontal, so a deposit fails only when the WHOLE group is saturated. A zombie
// member reads 0 free space and is skipped.
func SelectDepositWarehouse(space WarehouseSpaceReader, group []*storage.StorageOperation, good string) *storage.StorageOperation {
	var best *storage.StorageOperation
	for _, op := range group {
		if !op.SupportsGood(good) {
			continue
		}
		free := 0
		for _, s := range space.GetStorageShipsForOperation(op.ID()) {
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

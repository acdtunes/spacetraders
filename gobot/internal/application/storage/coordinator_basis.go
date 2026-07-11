package storage

import (
	"context"
	"log/slog"
)

// C1 (sp-64je) — planner-visible stock: cost-basis tracking on the storage
// coordinator. A factory-output deposit records the per-unit cost basis (the
// factory ask paid to lift the good) so the tour solver can withdraw the stock
// at basis instead of buying our own output at laddered market asks.
//
// Basis is tracked per (operationID, good) as a running units-weighted average.
// It moves ONLY on deposit: a withdrawal removes units at the current average,
// so the average is unchanged. A good with no recorded (or non-positive) basis
// is UNKNOWN and GetCostBasis fails closed, so the solver-source builder does
// not offer it and tours fall back to a normal market buy.
//
// Basis lives on the coordinator (not the StorageShip) so the proven deposit /
// withdrawal protocols stay untouched, and at the operation granularity the
// tour solver actually consumes (GetTotalCargoAvailable is operation-scoped).
// The coordinator is also the single owner of basis DURABILITY: it persists on
// deposit and reloads on recovery (RULINGS #2) through an optional CostBasisStore.

// CostBasisStore persists the per-operation cost basis map so it survives daemon
// restart (RULINGS #2). Optional on the coordinator: a nil store keeps basis
// in-memory only. Implemented by the storage operation repository, which writes
// it out-of-band as a JSON column so it never clobbers the operation's own row.
type CostBasisStore interface {
	SaveOperationBasis(ctx context.Context, operationID string, basis map[string]int) error
	LoadOperationBasis(ctx context.Context, operationID string) (map[string]int, error)
}

// SetCostBasisStore wires optional durable persistence of cost basis. nil (the
// default) keeps basis in-memory only.
func (c *InMemoryStorageCoordinator) SetCostBasisStore(store CostBasisStore) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.basisStore = store
}

// ConfirmDepositWithBasis is ConfirmDeposit that also records the deposited
// good's per-unit cost basis, blended into the operation's running
// weighted-average basis, and best-effort-persists the operation's basis. Used
// by the factory-output deposit path.
func (c *InMemoryStorageCoordinator) ConfirmDepositWithBasis(shipSymbol, goodSymbol string, units, unitBasis int) {
	c.mu.Lock()

	ship, exists := c.storageShips[shipSymbol]
	if !exists {
		c.mu.Unlock()
		return
	}
	operationID := ship.OperationID()

	// Weighted average uses the operation-total units held BEFORE this deposit.
	existingUnits := c.operationHeldUnitsLocked(operationID, goodSymbol)
	existingBasis := c.operationBasisLocked(operationID, goodSymbol)

	if !c.confirmDepositLocked(shipSymbol, goodSymbol, units) {
		c.mu.Unlock()
		return
	}

	// Fresh basis when there were no prior units (or no prior basis): a drained
	// good re-deposits at the new price, never blending a stale average.
	newBasis := unitBasis
	if existingUnits > 0 && existingBasis > 0 {
		newBasis = (existingBasis*existingUnits + unitBasis*units) / (existingUnits + units)
	}
	c.setOperationBasisLocked(operationID, goodSymbol, newBasis)

	// Snapshot under the lock, persist after releasing it (a DB write must not
	// be held under the coordinator mutex).
	snapshot := c.snapshotOperationBasisLocked(operationID)
	store := c.basisStore
	c.mu.Unlock()

	if store != nil {
		if err := store.SaveOperationBasis(context.Background(), operationID, snapshot); err != nil {
			// Best-effort: a persistence miss means basis re-derives on the next
			// deposit, else fails closed on restart (market-buy fallback).
			slog.Warn("storage: failed to persist cost basis (best-effort)",
				"operation_id", operationID, "good", goodSymbol, "error", err)
		}
	}
}

// SeedOperationBasis loads a persisted per-good basis map into the coordinator on
// recovery (RULINGS #2). Units are re-derived from the live ship API; basis
// cannot be, so it is restored here. Non-positive entries are dropped. Replaces
// any existing in-memory basis for the operation.
func (c *InMemoryStorageCoordinator) SeedOperationBasis(operationID string, basis map[string]int) {
	if len(basis) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.operationBasis == nil {
		c.operationBasis = make(map[string]map[string]int)
	}
	byGood := make(map[string]int, len(basis))
	for good, b := range basis {
		if b > 0 {
			byGood[good] = b
		}
	}
	c.operationBasis[operationID] = byGood
}

// LoadAndSeedBasis reloads an operation's persisted basis from the store and
// seeds it into the coordinator. Called by the recovery path after an
// operation's storage ships have been re-registered from live cargo. No-op when
// no store is wired.
func (c *InMemoryStorageCoordinator) LoadAndSeedBasis(ctx context.Context, operationID string) {
	c.mu.RLock()
	store := c.basisStore
	c.mu.RUnlock()
	if store == nil {
		return
	}
	basis, err := store.LoadOperationBasis(ctx, operationID)
	if err != nil {
		slog.Warn("storage: failed to load cost basis on recovery",
			"operation_id", operationID, "error", err)
		return
	}
	c.SeedOperationBasis(operationID, basis)
}

// snapshotOperationBasisLocked copies an operation's basis map for persistence
// outside the lock. Caller must hold c.mu.
func (c *InMemoryStorageCoordinator) snapshotOperationBasisLocked(operationID string) map[string]int {
	byGood := c.operationBasis[operationID]
	out := make(map[string]int, len(byGood))
	for good, b := range byGood {
		out[good] = b
	}
	return out
}

// GetCostBasis returns the operation's weighted-average unit cost basis for a
// good and whether it is KNOWN. Fail-closed: a good with no recorded (or
// non-positive) basis, or one whose stock has fully drained, returns
// known=false — so the tour-solver stock-source builder will not offer it and
// tours fall back to a normal market buy.
func (c *InMemoryStorageCoordinator) GetCostBasis(operationID, goodSymbol string) (int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	basis := c.operationBasisLocked(operationID, goodSymbol)
	if basis <= 0 {
		return 0, false
	}
	if c.operationHeldUnitsLocked(operationID, goodSymbol) <= 0 {
		// Stale basis for a good that has fully drained — fail closed. A later
		// deposit re-establishes a fresh basis.
		return 0, false
	}
	return basis, true
}

// operationHeldUnitsLocked returns the operation-total units of a good held
// across all its storage ships (including reserved units). Caller must hold c.mu.
func (c *InMemoryStorageCoordinator) operationHeldUnitsLocked(operationID, goodSymbol string) int {
	total := 0
	for _, symbol := range c.shipsByOperation[operationID] {
		if ship := c.storageShips[symbol]; ship != nil {
			total += ship.HeldUnits(goodSymbol)
		}
	}
	return total
}

// operationBasisLocked returns the recorded basis for (operation, good), or 0 if
// none. Caller must hold c.mu.
func (c *InMemoryStorageCoordinator) operationBasisLocked(operationID, goodSymbol string) int {
	if byGood := c.operationBasis[operationID]; byGood != nil {
		return byGood[goodSymbol]
	}
	return 0
}

// setOperationBasisLocked records the basis for (operation, good), lazily
// initialising the maps. Caller must hold c.mu.
func (c *InMemoryStorageCoordinator) setOperationBasisLocked(operationID, goodSymbol string, basis int) {
	if c.operationBasis == nil {
		c.operationBasis = make(map[string]map[string]int)
	}
	byGood := c.operationBasis[operationID]
	if byGood == nil {
		byGood = make(map[string]int)
		c.operationBasis[operationID] = byGood
	}
	byGood[goodSymbol] = basis
}

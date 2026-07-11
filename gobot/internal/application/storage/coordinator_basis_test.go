package storage

import (
	"context"
	"testing"
	"time"
)

// fakeBasisStore is an in-memory CostBasisStore for exercising the coordinator's
// persist/seed paths without a database.
type fakeBasisStore struct {
	saved  map[string]map[string]int
	toLoad map[string]map[string]int
}

func newFakeBasisStore() *fakeBasisStore {
	return &fakeBasisStore{
		saved:  map[string]map[string]int{},
		toLoad: map[string]map[string]int{},
	}
}

func (f *fakeBasisStore) SaveOperationBasis(_ context.Context, operationID string, basis map[string]int) error {
	cp := make(map[string]int, len(basis))
	for k, v := range basis {
		cp[k] = v
	}
	f.saved[operationID] = cp
	return nil
}

func (f *fakeBasisStore) LoadOperationBasis(_ context.Context, operationID string) (map[string]int, error) {
	return f.toLoad[operationID], nil
}

// C1 (sp-64je) — cost-basis on warehouse stock. The coordinator records a
// per-(operation, good) weighted-average unit cost basis at deposit time so the
// tour solver can price warehouse stock as a zero-ask-at-basis source. Basis
// moves ONLY on deposit (a withdrawal removes units at the running average, so
// the average is unchanged); a good with no recorded basis is UNKNOWN
// (fail-closed — the solver-source builder must not offer it).

func basisTestCoordinator() *InMemoryStorageCoordinator {
	return newTestCoordinator(50*time.Millisecond, 2)
}

// mustReserve reserves deposit space via the coordinator (single-ship ops).
func mustReserve(t *testing.T, c *InMemoryStorageCoordinator, opID string, units int) {
	t.Helper()
	if _, reserved, ok := c.ReserveSpaceForDeposit(opID, units); !ok || reserved != units {
		t.Fatalf("ReserveSpaceForDeposit(%s, %d): reserved=%d ok=%v", opID, units, reserved, ok)
	}
}

// A single basis deposit sets the basis to the deposit price.
func TestConfirmDepositWithBasis_RecordsBasis(t *testing.T) {
	c := basisTestCoordinator()
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 100, nil)
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}
	mustReserve(t, c, "OP-1", 50)
	c.ConfirmDepositWithBasis("STORAGE-1", "ADVANCED_CIRCUITRY", 50, 100)

	basis, known := c.GetCostBasis("OP-1", "ADVANCED_CIRCUITRY")
	if !known {
		t.Fatal("expected basis to be known after a basis deposit")
	}
	if basis != 100 {
		t.Fatalf("expected basis 100, got %d", basis)
	}
}

// Two basis deposits blend into a units-weighted average (40@50 + 40@80 = 65).
func TestConfirmDepositWithBasis_WeightedAverage(t *testing.T) {
	c := basisTestCoordinator()
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 200, nil)
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}
	mustReserve(t, c, "OP-1", 40)
	c.ConfirmDepositWithBasis("STORAGE-1", "CLOTHING", 40, 50)
	mustReserve(t, c, "OP-1", 40)
	c.ConfirmDepositWithBasis("STORAGE-1", "CLOTHING", 40, 80)

	basis, known := c.GetCostBasis("OP-1", "CLOTHING")
	if !known {
		t.Fatal("expected basis known")
	}
	if basis != 65 {
		t.Fatalf("expected weighted-average basis 65, got %d", basis)
	}
}

// The weighted average blends across DIFFERENT ships in the same operation.
func TestConfirmDepositWithBasis_WeightedAcrossShips(t *testing.T) {
	c := basisTestCoordinator()
	a := mustStorageShip(t, "STORAGE-A", "OP-1", 100, nil)
	b := mustStorageShip(t, "STORAGE-B", "OP-1", 100, nil)
	if err := c.RegisterStorageShip(a); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if err := c.RegisterStorageShip(b); err != nil {
		t.Fatalf("register b: %v", err)
	}
	// Reserve on each specific ship, then confirm the deposit there.
	if err := a.ReserveSpace(40); err != nil {
		t.Fatalf("reserve a: %v", err)
	}
	c.ConfirmDepositWithBasis("STORAGE-A", "EQUIPMENT", 40, 50)
	if err := b.ReserveSpace(40); err != nil {
		t.Fatalf("reserve b: %v", err)
	}
	c.ConfirmDepositWithBasis("STORAGE-B", "EQUIPMENT", 40, 80)

	basis, known := c.GetCostBasis("OP-1", "EQUIPMENT")
	if !known || basis != 65 {
		t.Fatalf("expected operation-level weighted basis 65 across ships, got %d (known=%v)", basis, known)
	}
}

// A withdrawal removes units at the running average, so the average is unchanged.
func TestGetCostBasis_UnchangedByWithdrawal(t *testing.T) {
	c := basisTestCoordinator()
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 100, nil)
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}
	mustReserve(t, c, "OP-1", 100)
	c.ConfirmDepositWithBasis("STORAGE-1", "MEDICINE", 100, 50)

	// Withdraw 60 units through the proven ship protocol.
	if _, err := ship.TryReserveCargo("MEDICINE", 60); err != nil {
		t.Fatalf("TryReserveCargo: %v", err)
	}
	if err := ship.ConfirmTransfer("MEDICINE", 60); err != nil {
		t.Fatalf("ConfirmTransfer: %v", err)
	}

	basis, known := c.GetCostBasis("OP-1", "MEDICINE")
	if !known || basis != 50 {
		t.Fatalf("expected basis 50 unchanged by withdrawal, got %d (known=%v)", basis, known)
	}
}

// Draining a good to zero then re-depositing starts a FRESH basis (the stale
// average must not blend into the new deposit).
func TestConfirmDepositWithBasis_DrainThenRedepositResetsBasis(t *testing.T) {
	c := basisTestCoordinator()
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 100, nil)
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}
	mustReserve(t, c, "OP-1", 40)
	c.ConfirmDepositWithBasis("STORAGE-1", "LAB_INSTRUMENTS", 40, 50)
	// Drain all 40.
	if _, err := ship.TryReserveCargo("LAB_INSTRUMENTS", 40); err != nil {
		t.Fatalf("TryReserveCargo: %v", err)
	}
	if err := ship.ConfirmTransfer("LAB_INSTRUMENTS", 40); err != nil {
		t.Fatalf("ConfirmTransfer: %v", err)
	}
	// Re-deposit at a different price.
	mustReserve(t, c, "OP-1", 40)
	c.ConfirmDepositWithBasis("STORAGE-1", "LAB_INSTRUMENTS", 40, 80)

	basis, known := c.GetCostBasis("OP-1", "LAB_INSTRUMENTS")
	if !known || basis != 80 {
		t.Fatalf("expected fresh basis 80 after drain+redeposit, got %d (known=%v)", basis, known)
	}
}

// An unknown good (never deposited with basis) is fail-closed.
func TestGetCostBasis_UnknownGoodFailClosed(t *testing.T) {
	c := basisTestCoordinator()
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 100, nil)
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}
	if _, known := c.GetCostBasis("OP-1", "NEVER_DEPOSITED"); known {
		t.Fatal("expected unknown basis to be fail-closed (known=false)")
	}
}

// A plain ConfirmDeposit (no basis, the contract/gas path) records NO basis, so
// the good stays UNKNOWN — existing zero-ask behavior is preserved.
func TestConfirmDeposit_NoBasis_StaysUnknown(t *testing.T) {
	c := basisTestCoordinator()
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 100, nil)
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}
	mustReserve(t, c, "OP-1", 30)
	c.ConfirmDeposit("STORAGE-1", "FUEL", 30)

	if _, known := c.GetCostBasis("OP-1", "FUEL"); known {
		t.Fatal("expected plain ConfirmDeposit to leave basis unknown")
	}
}

// Basis is operation-scoped: OP-2 does not see OP-1's basis.
func TestGetCostBasis_OperationScoped(t *testing.T) {
	c := basisTestCoordinator()
	s1 := mustStorageShip(t, "STORAGE-1", "OP-1", 100, nil)
	s2 := mustStorageShip(t, "STORAGE-2", "OP-2", 100, nil)
	if err := c.RegisterStorageShip(s1); err != nil {
		t.Fatalf("register s1: %v", err)
	}
	if err := c.RegisterStorageShip(s2); err != nil {
		t.Fatalf("register s2: %v", err)
	}
	mustReserve(t, c, "OP-1", 40)
	c.ConfirmDepositWithBasis("STORAGE-1", "CLOTHING", 40, 100)

	if _, known := c.GetCostBasis("OP-2", "CLOTHING"); known {
		t.Fatal("expected OP-2 to not see OP-1's basis")
	}
}

// A basis deposit best-effort-persists a snapshot of the operation's basis map.
func TestConfirmDepositWithBasis_PersistsSnapshot(t *testing.T) {
	c := basisTestCoordinator()
	store := newFakeBasisStore()
	c.SetCostBasisStore(store)
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 100, nil)
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}
	mustReserve(t, c, "OP-1", 40)
	c.ConfirmDepositWithBasis("STORAGE-1", "CLOTHING", 40, 50)

	if got := store.saved["OP-1"]["CLOTHING"]; got != 50 {
		t.Fatalf("expected persisted basis 50 for OP-1/CLOTHING, got %d (saved=%v)", got, store.saved)
	}
}

// Seeding restores basis from durable storage; combined with units re-derived
// from the ship API on recovery, GetCostBasis then reports the seeded basis.
func TestSeedOperationBasis_RestoresBasisWithRecoveredUnits(t *testing.T) {
	c := basisTestCoordinator()
	// Post-restart: the ship is rebuilt from live API cargo (40 units), basis lost.
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 100, map[string]int{"CLOTHING": 40})
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}
	if _, known := c.GetCostBasis("OP-1", "CLOTHING"); known {
		t.Fatal("precondition: basis must be unknown before seeding")
	}

	c.SeedOperationBasis("OP-1", map[string]int{"CLOTHING": 65})

	basis, known := c.GetCostBasis("OP-1", "CLOTHING")
	if !known || basis != 65 {
		t.Fatalf("expected seeded basis 65 with recovered units, got %d (known=%v)", basis, known)
	}
}

// LoadAndSeedBasis pulls the persisted basis from the store and seeds it.
func TestLoadAndSeedBasis_UsesStore(t *testing.T) {
	c := basisTestCoordinator()
	store := newFakeBasisStore()
	store.toLoad["OP-1"] = map[string]int{"CLOTHING": 70}
	c.SetCostBasisStore(store)
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 100, map[string]int{"CLOTHING": 20})
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}

	c.LoadAndSeedBasis(context.Background(), "OP-1")

	basis, known := c.GetCostBasis("OP-1", "CLOTHING")
	if !known || basis != 70 {
		t.Fatalf("expected basis 70 loaded from store, got %d (known=%v)", basis, known)
	}
}

// Without a store, persistence and load-and-seed are safe no-ops.
func TestBasis_NoStore_NoOp(t *testing.T) {
	c := basisTestCoordinator()
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 100, nil)
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}
	mustReserve(t, c, "OP-1", 40)
	c.ConfirmDepositWithBasis("STORAGE-1", "CLOTHING", 40, 50) // must not panic
	c.LoadAndSeedBasis(context.Background(), "OP-1")           // must not panic

	basis, known := c.GetCostBasis("OP-1", "CLOTHING")
	if !known || basis != 50 {
		t.Fatalf("expected in-memory basis 50 without a store, got %d (known=%v)", basis, known)
	}
}

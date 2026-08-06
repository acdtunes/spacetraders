package contract

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func reconcileTestContract(t *testing.T, deliveries ...Delivery) *Contract {
	t.Helper()
	c, err := NewContract("contract-reconcile", shared.MustNewPlayerID(1), "COSMIC", "PROCUREMENT", Terms{
		Payment:          Payment{OnAccepted: 5000, OnFulfilled: 20000},
		Deliveries:       deliveries,
		DeadlineToAccept: "2026-01-01T00:00:00Z",
		Deadline:         "2027-01-01T00:00:00Z",
	}, nil)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	return c
}

func deliveredUnitsOf(t *testing.T, c *Contract, good string) int {
	t.Helper()
	for _, d := range c.Terms().Deliveries {
		if d.TradeSymbol == good {
			return d.UnitsFulfilled
		}
	}
	t.Fatalf("good %s not in contract", good)
	return 0
}

// The 2026-08-05 TORWIND shape: the local row lagged a delivery the server had already
// accepted. Raising it is what lets every downstream units-remaining guard read a number that
// is not too low.
func TestReconcileDeliveredFromServer_RaisesALaggingLocalCount(t *testing.T) {
	c := reconcileTestContract(t, Delivery{TradeSymbol: "ALUMINUM", DestinationSymbol: "X1-BG40-D40", UnitsRequired: 47, UnitsFulfilled: 0})

	if raised := c.ReconcileDeliveredFromServer(map[string]int{"ALUMINUM": 47}); !raised {
		t.Fatal("expected the lagging local count to be raised")
	}
	if got := deliveredUnitsOf(t, c, "ALUMINUM"); got != 47 {
		t.Fatalf("delivered = %d, want 47", got)
	}
	if !c.CanFulfill() {
		t.Fatal("a contract raised to its required count must be fulfillable")
	}
}

// Raise-only is the fail-closed direction (RULINGS #4): lowering cannot be told apart from a
// read that raced a delivery landing, and it would re-source units already handed over.
func TestReconcileDeliveredFromServer_NeverLowersALocalCount(t *testing.T) {
	c := reconcileTestContract(t, Delivery{TradeSymbol: "ALUMINUM", UnitsRequired: 47, UnitsFulfilled: 40})

	if raised := c.ReconcileDeliveredFromServer(map[string]int{"ALUMINUM": 12}); raised {
		t.Fatal("a lower server count must not be reported as a raise")
	}
	if got := deliveredUnitsOf(t, c, "ALUMINUM"); got != 40 {
		t.Fatalf("delivered = %d, want the local 40 to survive a lower observation", got)
	}
}

// The over-delivered count is stored RAW. Clamping to UnitsRequired would erase the only
// surviving evidence that 47 units were handed over twice.
func TestReconcileDeliveredFromServer_StoresAnOverDeliveryUncapped(t *testing.T) {
	c := reconcileTestContract(t, Delivery{TradeSymbol: "ALUMINUM", UnitsRequired: 47, UnitsFulfilled: 0})

	c.ReconcileDeliveredFromServer(map[string]int{"ALUMINUM": 94})

	if got := deliveredUnitsOf(t, c, "ALUMINUM"); got != 94 {
		t.Fatalf("delivered = %d, want the raw server count 94 (clamping erases the over-delivery evidence)", got)
	}
	if !c.CanFulfill() {
		t.Fatal("an over-delivered contract must read as fulfillable")
	}
}

// A multi-good contract must not have an unreported good silently zeroed or touched.
func TestReconcileDeliveredFromServer_LeavesAnUnreportedGoodAlone(t *testing.T) {
	c := reconcileTestContract(t,
		Delivery{TradeSymbol: "ALUMINUM", UnitsRequired: 47, UnitsFulfilled: 10},
		Delivery{TradeSymbol: "IRON_ORE", UnitsRequired: 30, UnitsFulfilled: 5},
	)

	c.ReconcileDeliveredFromServer(map[string]int{"ALUMINUM": 47})

	if got := deliveredUnitsOf(t, c, "IRON_ORE"); got != 5 {
		t.Fatalf("IRON_ORE delivered = %d, want the untouched 5", got)
	}
}

// A server-fulfilled contract must stop reading as active work locally, and the row it leaves
// behind must survive the Fulfill() replay the persistence layer performs on load — which is
// why the deliveries are raised to required first.
func TestMarkFulfilledFromServer_HealsTheRowIntoALoadableFulfilledState(t *testing.T) {
	c := reconcileTestContract(t, Delivery{TradeSymbol: "ALUMINUM", UnitsRequired: 47, UnitsFulfilled: 3})

	c.MarkFulfilledFromServer()

	if !c.Fulfilled() {
		t.Fatal("expected the contract to read as fulfilled")
	}
	if !c.Accepted() {
		t.Fatal("a fulfilled contract must read as accepted; Fulfill() refuses an unaccepted one on reload")
	}
	if got := deliveredUnitsOf(t, c, "ALUMINUM"); got != 47 {
		t.Fatalf("delivered = %d, want 47 — a fulfilled row with short deliveries fails CanFulfill on reload", got)
	}

	// The persistence layer rebuilds a fresh entity from the persisted deliveries and replays
	// Accept() then Fulfill(). Replay that here: a row whose deliveries do not satisfy
	// CanFulfill is permanently unloadable, so the whole contract disappears from the daemon.
	replayed := reconcileTestContract(t, c.Terms().Deliveries...)
	if err := replayed.Accept(); err != nil {
		t.Fatalf("replayed Accept: %v", err)
	}
	if err := replayed.Fulfill(); err != nil {
		t.Fatalf("the healed row is unloadable — persistence replays Fulfill on load: %v", err)
	}
}

// MarkFulfilledFromServer must never lower a count either — an over-delivery survives it.
func TestMarkFulfilledFromServer_DoesNotClampAnOverDelivery(t *testing.T) {
	c := reconcileTestContract(t, Delivery{TradeSymbol: "ALUMINUM", UnitsRequired: 47, UnitsFulfilled: 94})

	c.MarkFulfilledFromServer()

	if got := deliveredUnitsOf(t, c, "ALUMINUM"); got != 94 {
		t.Fatalf("delivered = %d, want the over-delivery evidence 94 preserved", got)
	}
}

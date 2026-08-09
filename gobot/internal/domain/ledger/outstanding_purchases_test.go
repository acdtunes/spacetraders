package ledger

import (
	"reflect"
	"testing"
	"time"
)

// obligationLeg builds one cargo row at tick t (seconds from an arbitrary base), so a test
// reads as the trade sequence it is describing.
func obligationLeg(tick int, hull, good string, units int, op string, isBuy bool) CargoLeg {
	return CargoLeg{
		TxID:          time.Unix(int64(tick), 0).Format("tx-150405"),
		Hull:          hull,
		Good:          good,
		Units:         units,
		OperationType: op,
		At:            time.Unix(1_700_000_000, 0).Add(time.Duration(tick) * time.Second),
		IsBuy:         isBuy,
	}
}

func requireOutstanding(t *testing.T, got, want map[string]map[string]int) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outstanding = %+v, want %+v", got, want)
	}
}

// The whole point: cargo a hull bought under the operation and never discharged is still
// owed. This is what a daemon restart must be able to rebuild.
func TestOutstandingPurchases_UnsoldPurchaseIsOwed(t *testing.T) {
	legs := []CargoLeg{obligationLeg(1, "TORWIND-F9", "FOOD", 180, "tour", true)}
	requireOutstanding(t, OutstandingPurchases(legs, "tour"),
		map[string]map[string]int{"TORWIND-F9": {"FOOD": 180}})
}

// A settled obligation must be ABSENT, never present at zero: a zero that survives the
// rebuild would veto the next healthy run on that hull.
func TestOutstandingPurchases_SoldOutIsDropped(t *testing.T) {
	legs := []CargoLeg{
		obligationLeg(1, "TORWIND-F8", "FOOD", 180, "tour", true),
		obligationLeg(2, "TORWIND-F8", "FOOD", 180, "tour", false),
	}
	requireOutstanding(t, OutstandingPurchases(legs, "tour"), map[string]map[string]int{})
}

func TestOutstandingPurchases_PartialSaleLeavesTheRemainder(t *testing.T) {
	legs := []CargoLeg{
		obligationLeg(1, "TORWIND-FB", "URANITE", 200, "tour", true),
		obligationLeg(2, "TORWIND-FB", "URANITE", 140, "tour", false),
	}
	requireOutstanding(t, OutstandingPurchases(legs, "tour"),
		map[string]map[string]int{"TORWIND-FB": {"URANITE": 60}})
}

// The operation is answerable for what IT bought. A hull carrying a contract pre-buy, a
// factory input or a hand-bought load owes this operation nothing, and vetoing on it would
// fail every laden hull the fleet ever hands over.
func TestOutstandingPurchases_IgnoresPurchasesUnderAnotherOperation(t *testing.T) {
	legs := []CargoLeg{
		obligationLeg(1, "TORWIND-FC", "FOOD", 120, "manual", true),
		obligationLeg(2, "TORWIND-FC", "MEDICINE", 90, "contract", true),
		obligationLeg(3, "TORWIND-FC", "CLOTHING", 40, "manufacturing", true),
	}
	requireOutstanding(t, OutstandingPurchases(legs, "tour"), map[string]map[string]int{})
}

// A tour purchase is routinely liquidated under another operation — a distress sale, a
// captain's hand-unload. Scoping the DISCHARGE by operation the way the accrual is scoped
// would leave an emptied hold owing forever and wedge the hull.
func TestOutstandingPurchases_AnySaleDischarges(t *testing.T) {
	legs := []CargoLeg{
		obligationLeg(1, "TORWIND-A", "IRON", 100, "tour", true),
		obligationLeg(2, "TORWIND-A", "IRON", 100, "liquidation", false),
	}
	requireOutstanding(t, OutstandingPurchases(legs, "tour"), map[string]map[string]int{})
}

// Selling an inherited load must not bank credit against a LATER purchase — the exact netting
// error that let a run empty a hold it was handed, refill it, and still report success. The
// count floors at zero AT EACH STEP, not cumulatively.
func TestOutstandingPurchases_SaleBeforePurchaseBanksNoCredit(t *testing.T) {
	legs := []CargoLeg{
		obligationLeg(1, "TORWIND-D", "FOOD", 150, "manual", true), // launch inventory
		obligationLeg(2, "TORWIND-D", "FOOD", 150, "tour", false),  // the tour sells it
		obligationLeg(3, "TORWIND-D", "FOOD", 100, "tour", true),   // then buys its own
	}
	requireOutstanding(t, OutstandingPurchases(legs, "tour"),
		map[string]map[string]int{"TORWIND-D": {"FOOD": 100}})
}

// Chronology decides, not arrival order: the reader hands legs over in whatever order the
// scan produced them, and the same history must always rebuild to the same obligation.
func TestOutstandingPurchases_ReplaysChronologicallyRegardlessOfInputOrder(t *testing.T) {
	buy := obligationLeg(3, "TORWIND-E", "FOOD", 100, "tour", true)
	sellFirst := obligationLeg(2, "TORWIND-E", "FOOD", 150, "tour", false)
	inherited := obligationLeg(1, "TORWIND-E", "FOOD", 150, "manual", true)

	want := map[string]map[string]int{"TORWIND-E": {"FOOD": 100}}
	requireOutstanding(t, OutstandingPurchases([]CargoLeg{buy, sellFirst, inherited}, "tour"), want)
	requireOutstanding(t, OutstandingPurchases([]CargoLeg{sellFirst, inherited, buy}, "tour"), want)
}

// Hulls and goods are independent streams: one hull's sale must never discharge another's
// obligation, and cargo is only fungible within its own good.
func TestOutstandingPurchases_KeepsHullsAndGoodsApart(t *testing.T) {
	legs := []CargoLeg{
		obligationLeg(1, "TORWIND-F9", "FOOD", 80, "tour", true),
		obligationLeg(2, "TORWIND-FB", "FOOD", 200, "tour", false), // another hull's sale
		obligationLeg(3, "TORWIND-F9", "URANITE", 30, "tour", true),
		obligationLeg(4, "TORWIND-F9", "URANITE", 30, "tour", false),
	}
	requireOutstanding(t, OutstandingPurchases(legs, "tour"),
		map[string]map[string]int{"TORWIND-F9": {"FOOD": 80}})
}

// A row the reader could not attribute carries no hull, good or units. It must open no
// obligation — inventing one would strand a hull on a row nobody can trace.
func TestOutstandingPurchases_UnattributableRowsOpenNothing(t *testing.T) {
	legs := []CargoLeg{
		obligationLeg(1, "", "FOOD", 100, "tour", true),
		obligationLeg(2, "TORWIND-F9", "", 100, "tour", true),
		obligationLeg(3, "TORWIND-F9", "FOOD", 0, "tour", true),
	}
	requireOutstanding(t, OutstandingPurchases(legs, "tour"), map[string]map[string]int{})
}

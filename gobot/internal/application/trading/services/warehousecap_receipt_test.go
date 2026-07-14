package services

import "testing"

// A DESTINATION-side cluster warehouse gates its buffer on RECEIPT demand (bead
// sp-u9xa): among the goods a destination receives via contract deliveries, the
// knapsack pre-stages the ones whose SOURCE is FAR (a long haul-leg the buffer moves
// off the serialized contract path onto parallel stockers) over the ones whose source
// is NEAR (little haul saved), subject to the real hull capacity — so a tight buffer
// holds only the high-value far-haul goods, not every received good blindly. This is
// the sibling of PlanWarehouseCaps (source buy-leg); the pure knapsack is shared.
func TestPlanReceiptCaps_GatesOnReceiptDemandPreferringFarHaulGoods(t *testing.T) {
	const dest = "X1-FAR-58"
	// Two goods identical in receipt demand (recurrence, payment, size); they differ ONLY
	// in how far their source sits from the destination warehouse — i.e. in the haul-leg
	// the destination buffer would relocate onto a stocker.
	coords := func(w string) (float64, float64, bool) {
		switch w {
		case dest:
			return 0, 0, true
		case "X1-SRC-NEAR":
			return 10, 0, true // ~10u from destination: little haul saved
		case "X1-SRC-FAR":
			return 500, 0, true // ~500u from destination: big haul saved
		}
		return 0, 0, false
	}
	candidates := []ReceiptCandidate{
		{Good: "NEAR_GOOD", ContractCount: 5, Payment: 100, SourceWaypoint: "X1-SRC-NEAR", SourceSystem: "X1", MaxContractUnits: 40},
		{Good: "FAR_GOOD", ContractCount: 5, Payment: 100, SourceWaypoint: "X1-SRC-FAR", SourceSystem: "X1", MaxContractUnits: 40},
	}

	// Capacity 40 fits exactly ONE 40-unit good, forcing the knapsack to choose.
	res := PlanReceiptCaps(candidates, 40, "X1", dest, coords, nil, nil, WarehouseCapParams{})

	if _, ok := res.Targets["FAR_GOOD"]; !ok {
		t.Errorf("expected FAR_GOOD buffered (big haul-leg saved), targets=%v", res.Targets)
	}
	if _, ok := res.Targets["NEAR_GOOD"]; ok {
		t.Errorf("expected NEAR_GOOD dropped (little haul saved) under tight capacity, targets=%v", res.Targets)
	}
}

// A cross-system source is the maximum-haul-saved case: the serialized contract worker
// would otherwise fly the whole cross-gate leg, so the destination buffer is most
// valuable there — it out-ranks an in-system-sourced good of equal receipt demand.
func TestPlanReceiptCaps_CrossSystemSourceOutranksInSystem(t *testing.T) {
	const dest = "X1-FAR-58"
	coords := func(w string) (float64, float64, bool) {
		switch w {
		case dest:
			return 0, 0, true
		case "X1-SRC-CLOSE":
			return 5, 0, true
		}
		return 0, 0, false // cross-system source is unresolvable in this system's coords
	}
	candidates := []ReceiptCandidate{
		{Good: "INSYS_GOOD", ContractCount: 5, Payment: 100, SourceWaypoint: "X1-SRC-CLOSE", SourceSystem: "X1", MaxContractUnits: 40},
		{Good: "CROSS_GOOD", ContractCount: 5, Payment: 100, SourceWaypoint: "X2-SRC-99", SourceSystem: "X2", MaxContractUnits: 40},
	}

	res := PlanReceiptCaps(candidates, 40, "X1", dest, coords, nil, nil, WarehouseCapParams{})

	if _, ok := res.Targets["CROSS_GOOD"]; !ok {
		t.Errorf("expected CROSS_GOOD buffered (max haul saved), targets=%v", res.Targets)
	}
	if _, ok := res.Targets["INSYS_GOOD"]; ok {
		t.Errorf("expected INSYS_GOOD dropped under tight capacity, targets=%v", res.Targets)
	}
}

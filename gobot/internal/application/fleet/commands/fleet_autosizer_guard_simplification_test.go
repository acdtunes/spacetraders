package commands

import (
	"context"
	"testing"
)

// THE PRODUCTION REGRESSION (the whole reason the twelve-guard chain was collapsed to seven).
//
// Verbatim from the live decision line the fleet owner read:
//
//	fleet_ceiling[BLOCK: class 13/15, total 277/150]
//	era_payback[BLOCK: marginal rate unreadable/zero — cannot prove payback]
//	realized_rate[BLOCK: rate DECLINING but 20 unserved lanes > floor 2 ...]
//
// 244 of those 277 hulls are PROBES and the owner intends to grow that into the thousands, so a
// fleet-WIDE cap starves every other class permanently (it was already papered over once, 50 → 150).
// era_payback cannot compute its own input and so fails closed forever. realized_rate blocks on a
// declining aggregate rate while its own detail concedes the case does not apply.
//
// With the class ceiling open (13/15), the heavy cap open (0/5), the price affordable and the
// treasury deep, this purchase MUST fire. Every surviving guard is satisfied here; nothing about
// this test relaxes a money bound.
func TestProductionRegression_HeavyBuysDespiteHugeProbeFleetAndUnreadableRate(t *testing.T) {
	// 13 heavies in the trade pool against demand 13+20=33 (20 unserved lanes). In production this
	// tick ALSO carried a DECLINING aggregate rate and an UNREADABLE marginal; both of those inputs
	// are gone with the guards that consumed them, which is precisely why this buy now fires.
	provider := NewHeavyDemandProvider(&fakeHeavySources{
		heavies: 13, lanes: 20, lanesOK: true,
	})
	h, purchaser, metrics, _ := armedHandler(provider)
	// The 277 hulls (244 of them probes) that used to trip the fleet-wide total no longer enter
	// the decision at all — the autosizer has stopped reading a total-hull census, which is why
	// growing the probe frontier can no longer starve the trade pool.
	// Price and treasury permit: a 1.4M heavy against an 8M treasury (25% rule cap 2M).
	h.SetTreasuryReader(&fakeTreasury{credits: 8000000, ok: true})
	h.SetYardPriceReader(&fakeYardPrice{price: 1400000, cheapest: 1400000, yard: "KA42-A2", ok: true})
	h.SetHeavyCensusReader(&fakeHeavyCensus{owned: 0})
	h.SetHeavyYardReader(&fakeHeavyYard{price: 1400000, found: true})

	cmd := &RunFleetAutosizerCoordinatorCommand{
		PlayerID: 1, ContainerID: "c1",
		HeavyUnservedLanesMin:   1,         // streak satisfied on the first tick
		HeavyCap:                intPtr(5), // heavies owned 0/5 — open
		FleetCeilingHeavies:     15,        // class 13/15 — open
		PurchaseMarginOverFloor: 200000,
	}

	res, err := h.reconcileOnce(context.Background(), cmd)
	if err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if res.Purchased != 1 || len(purchaser.orders) != 1 {
		t.Fatalf("the production heavy MUST buy: purchased=%d orders=%d, blocked by %v",
			res.Purchased, len(purchaser.orders), metrics.blockedGuards)
	}
	if purchaser.orders[0].Class != HullClassHeavy {
		t.Fatalf("expected a HEAVY buy, got class %s", purchaser.orders[0].Class)
	}
}

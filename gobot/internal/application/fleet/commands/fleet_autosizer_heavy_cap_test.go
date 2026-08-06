package commands

import "testing"

// passingHeavyRequest is a HEAVY candidate where every guard passes, including the heavy cap.
// The heavy class runs the same guard stack as every other class; only heavy_cap is class-scoped.
func passingHeavyRequest() PurchaseRequest {
	r := passingRequest()
	r.Class = HullClassHeavy
	r.ShipType = "SHIP_HEAVY_FREIGHTER"
	r.Price = 1_565_500 // the live cheapest heavy ask (spec C4)
	r.CheapestKnownPrice = 1_565_500
	r.LiveTreasury = 20_000_000
	// A 20M treasury clears the 25%-per-buy affordability rule on a 1.57M hull with room to
	// spare, so this file pins the CAP and never trips a money guard by accident.
	r.HeaviesOwned = 1 // heavy HULLS owned
	r.HeavyCap = 5     // heavy_cap — since sp-r7eiu the ONLY count-based bound
	r.HeaviesOwnedReadable = true
	return r
}

func TestGuard_HeavyCap_AllPass_Approved(t *testing.T) {
	d := EvaluateGuards(passingHeavyRequest())
	if !d.Approved {
		t.Fatalf("expected APPROVED, got blocked by %q; arithmetic: %s", d.BlockedBy, d.Arithmetic())
	}
}

// THE LAST COUNT BOUND: at the heavy cap, no heavy is bought. sp-r7eiu removed class_ceiling, so
// this guard is now the only thing that can refuse a heavy on COUNT rather than on economics — if it
// stopped binding, heavy growth would be bounded by nothing but demand and the treasury.
func TestGuard_HeavyCap_BindsAsTheOnlyCountBound(t *testing.T) {
	r := passingHeavyRequest()
	r.HeaviesOwned = 5 // == heavy_cap
	assertBlockedBy(t, r, GuardHeavyCap)
}

// Over the cap (a hull acquired outside this path) also blocks.
func TestGuard_HeavyCap_OverCapBlocks(t *testing.T) {
	r := passingHeavyRequest()
	r.HeaviesOwned = 9
	assertBlockedBy(t, r, GuardHeavyCap)
}

// heavy_cap = 0 is a legitimate operator HOLD ("own no heavies"), not an unset knob.
func TestGuard_HeavyCap_ZeroIsALegitimateHold(t *testing.T) {
	r := passingHeavyRequest()
	r.HeavyCap = 0
	r.HeaviesOwned = 0
	assertBlockedBy(t, r, GuardHeavyCap)
}

// The heavy cap is HEAVY-SCOPED: it must never block a light buy, whatever the heavy census says.
// Folding it into the shared ceiling guard would starve the worker pool.
func TestGuard_HeavyCap_DoesNotApplyToOtherClasses(t *testing.T) {
	for _, class := range []HullClass{HullClassLight} {
		r := passingRequest()
		r.Class = class
		r.HeaviesOwned = 99 // far over any cap
		r.HeavyCap = 5
		d := EvaluateGuards(r)
		if !d.Approved {
			t.Fatalf("class %s must ignore the heavy cap, got blocked by %q; arithmetic: %s", class, d.BlockedBy, d.Arithmetic())
		}
	}
}

// FAIL-CLOSED (RULINGS #4): an unreadable heavy census must BLOCK, never pass. A census that
// silently read as 0 would report "no heavies owned" and authorise buying a hull we already own —
// the exact failure the broad, tag-independent census exists to prevent.
func TestGuard_HeavyCap_UnreadableCensusFailsClosed(t *testing.T) {
	r := passingHeavyRequest()
	r.HeaviesOwned = 0 // would otherwise pass with room to spare
	r.HeavyCap = 5
	r.HeaviesOwnedReadable = false
	assertBlockedBy(t, r, GuardHeavyCap)
}

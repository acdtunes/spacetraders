package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// THE SPENDER'S HALF OF THE SWITCH-BACK. common.DeriveWave answers correctly on a saturated
// surface, and fleetgrowth.TradeSaturated computes one — but the coordinator is what carries the
// verdict from the shared reader into the predicate, and a term computed by one layer and never
// assembled by the next is dead on arrival. These tests are on the ASSEMBLY, so they fail if the
// field is dropped between the reader and the WaveInputs while everything either side stays green.

// saturatedGrowthFixture is the live frame of 2026-08-08 01:51 as the coordinator sees it: nine
// unserved lanes of demand, a rich treasury, a priced target and a satisfied streak — EVERY OTHER
// GUARD DELIBERATELY OPEN, so the switch-back term is the only thing that can refuse this buy. A
// fixture short of any of them would pass with the term deleted entirely.
func saturatedGrowthFixture(saturated bool) growthFixture {
	return growthFixture{
		lanes:         &fakeLanes{count: 9, readable: true, saturated: saturated},
		treasury:      12_000_000,
		highWater:     12_000_000,
		yardAsk:       1_000_000,
		shortfallHeld: growthSettledWindow,
	}
}

// THE DEFECT, IN ONE ASSERTION. The census reports lanes still unserved, so the count clause holds
// the regime HEAVY and heavy demand asks for more hulls; the fleet has meanwhile outgrown the depth
// of everything it can reach. The coordinator must publish PROBE and buy nothing.
func TestGrowthReconcile_ASaturatedSurfaceIsProbeWithLanesStillUnserved(t *testing.T) {
	sink := &recordingWaveSink{}
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, saturatedGrowthFixture(true))
	h.SetMetricsSink(sink)
	h.SetPurchaser(buyer)

	res, err := h.reconcileOnce(context.Background(), growthCmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Wave != common.WaveProbe || res.Reason != common.WaveProbeReasonTradeSaturated {
		t.Fatalf("a saturated surface with 9 unserved lanes gave %q/%q, want PROBE/trade_saturated", res.Wave, res.Reason)
	}
	if res.Purchased != 0 || buyer.calls != 0 {
		t.Fatalf("a saturated surface bought %d heavies over %d calls — the switch-back must stop the spend, not merely relabel it", res.Purchased, buyer.calls)
	}
	if len(sink.reasons) == 0 || sink.reasons[0] != common.WaveProbeReasonTradeSaturated {
		t.Fatalf("the published reason was %v, want trade_saturated — an unexplained PROBE is what made this defect invisible", sink.reasons)
	}
}

// THE SHORTFALL IS STILL COUNTED, AND STILL PUBLISHED. The regime pauses the buy; it does not
// pretend the lane count changed. Erasing the demand here would hide the very surface an operator
// needs to see to judge whether the switch-back was right, and would reset the streak that a later
// deepening of the surface must be able to act on.
func TestGrowthReconcile_SaturationPausesTheBuyWithoutErasingTheDemand(t *testing.T) {
	h := newGrowthHandlerWith(t, saturatedGrowthFixture(true))

	res, err := h.reconcileOnce(context.Background(), growthCmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Shortfall != 9 {
		t.Fatalf("shortfall %d, want the 9 lanes the census actually reported — saturation gates the regime, not the count", res.Shortfall)
	}
}

// THE ANTI-VACUITY CONTROL. The identical fixture with a surface DEEPER than the fleet's hold must
// behave exactly as it did before this term existed: HEAVY, and a hull bought. Without this the
// test above passes for a coordinator that never buys anything at all.
func TestGrowthReconcile_AnUnsaturatedSurfaceStillBuysAHeavy(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, saturatedGrowthFixture(false))
	h.SetPurchaser(buyer)

	res, err := h.reconcileOnce(context.Background(), growthCmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Wave != common.WaveHeavy || res.Reason != common.WaveProbeReasonNone {
		t.Fatalf("an unsaturated surface gave %q/%q, want HEAVY — the term must be inert here", res.Wave, res.Reason)
	}
	if res.Purchased != 1 || buyer.calls != 1 {
		t.Fatalf("the unsaturated control bought %d heavies over %d calls, want exactly 1 — if this fixture cannot buy, the saturated case proves nothing", res.Purchased, buyer.calls)
	}
}

// A BLIND LANE READ OUTRANKS ITS OWN VERDICT. The count and the depth come off ONE read, so an
// unreadable surface makes both unreadable — and the release is toward PROBE, never toward a heavy
// bought against a saturation nobody could measure.
func TestGrowthReconcile_AnUnreadableSurfaceIsProbeNotAnUnsaturatedOne(t *testing.T) {
	f := saturatedGrowthFixture(true)
	f.lanes.readable = false
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, f)
	h.SetPurchaser(buyer)

	res, err := h.reconcileOnce(context.Background(), growthCmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Wave != common.WaveProbe || res.Reason != common.WaveProbeReasonLanesUnreadable {
		t.Fatalf("a blind surface gave %q/%q, want PROBE/lanes_unreadable", res.Wave, res.Reason)
	}
	if res.Purchased != 0 || buyer.calls != 0 {
		t.Fatalf("a blind surface bought %d heavies over %d calls — the lane read fails closed on the buy", res.Purchased, buyer.calls)
	}
}

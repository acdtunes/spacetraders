package commands

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

type stubAbsorptionLedger struct {
	pools map[absorption.LaneKey]absorption.KeyOccupancy
	err   error
	reads int
}

func (s *stubAbsorptionLedger) Outstanding(_ context.Context, _ int) (map[absorption.LaneKey]absorption.KeyOccupancy, error) {
	s.reads++
	if s.err != nil {
		return nil, s.err
	}
	return s.pools, nil
}

type absorptionTestLogger struct{}

func (absorptionTestLogger) Log(_, _ string, _ map[string]interface{}) {}

func absorptionTestCtx() context.Context {
	return common.WithLogger(context.Background(), absorptionTestLogger{})
}

// sinkLane is a lane whose SINK is X1-SINK-A1 for the given good, with the given single-visit
// depth.
func sinkLane(good string, volumeCap int) trading.ArbitrageLane {
	return trading.ArbitrageLane{
		Good:           good,
		SourceWaypoint: "X1-SRC-B2",
		DestWaypoint:   "X1-SINK-A1",
		SourceAsk:      100,
		DestBid:        180,
		SpreadPerUnit:  80,
		VolumeCap:      volumeCap,
	}
}

func sellKey(good string) absorption.LaneKey {
	return absorption.LaneKey{Waypoint: "X1-SINK-A1", Good: good, Side: absorption.SideSell}
}

// THE FIX (sp-kw2em). The clamp had zero call sites for its whole lifetime, so it never once
// applied. Wired, another engine's in-flight units against the same sink must come off this
// lane's headroom — the co-dump VolumeCap alone cannot see, because VolumeCap describes the
// sink in isolation and says nothing about who else is already selling into it.
func TestAbsorptionHeadroom_SubtractsOtherEnginesInFlightUnits(t *testing.T) {
	h := &RunLongHaulArbHandler{}
	h.SetAbsorptionLedger(&stubAbsorptionLedger{pools: map[absorption.LaneKey]absorption.KeyOccupancy{
		sellKey("COPPER"): {PlannedUnits: 45},
	}})

	headroom := h.absorptionHeadroomFn(absorptionTestCtx(), 1)
	if headroom == nil {
		t.Fatal("a wired ledger must yield a consult; nil means the clamp is skipped entirely")
	}

	if got := headroom(sinkLane("COPPER", 60)); got != 15 {
		t.Fatalf("headroom = %d, want 15 (VolumeCap 60 minus 45 units another engine already holds against this sink)", got)
	}
}

// RULINGS #4, the direction the change moves the guard. Headroom feeds achievableUnits through a
// min, so it may only ever make a buy SMALLER or leave it alone. This pins that end to end: with
// the sink contended, the sized buy must drop; with it clear, sizing must be identical to the
// unwired behaviour.
func TestAbsorptionHeadroom_OnlyEverTightensTheSizedBuy(t *testing.T) {
	lane := pricedLongHaulLane{OptimalUnits: 60}
	lane.Lane = sinkLane("COPPER", 60)
	envelope := longHaulEnvelope{perHaulCap: 1_000_000}

	unclamped := achievableUnits(lane, 100, envelope, -1)

	contended := &RunLongHaulArbHandler{}
	contended.SetAbsorptionLedger(&stubAbsorptionLedger{pools: map[absorption.LaneKey]absorption.KeyOccupancy{
		sellKey("COPPER"): {PlannedUnits: 45},
	}})
	clamped := achievableUnits(lane, 100, envelope, contended.absorptionHeadroomFn(absorptionTestCtx(), 1)(lane.Lane))

	if clamped > unclamped {
		t.Fatalf("the consult made the buy LARGER (%d > %d). This is a spend path: the clamp may only "+
			"tighten or leave sizing identical, never loosen it (RULINGS #4)", clamped, unclamped)
	}
	if clamped != 15 {
		t.Fatalf("clamped units = %d, want 15 — the contended sink's remaining depth", clamped)
	}

	clear := &RunLongHaulArbHandler{}
	clear.SetAbsorptionLedger(&stubAbsorptionLedger{pools: map[absorption.LaneKey]absorption.KeyOccupancy{}})
	unclaimed := achievableUnits(lane, 100, envelope, clear.absorptionHeadroomFn(absorptionTestCtx(), 1)(lane.Lane))
	if unclaimed != unclamped {
		t.Fatalf("an UNCONTENDED sink changed sizing (%d vs %d); with nobody else in flight the consult "+
			"must be a no-op, since OptimalUnits is already clamped to VolumeCap upstream", unclaimed, unclamped)
	}
}

// FAIL-CLOSED. A guard whose job is bounding a spend must never wave the buy through when it
// cannot read. Every sibling consult (idle-arb, trade-route) declines on an unreadable ledger;
// for an int-valued headroom that is zero, which sizes the lane to no buy at all.
func TestAbsorptionHeadroom_UnreadableLedgerSizesToZero(t *testing.T) {
	h := &RunLongHaulArbHandler{}
	h.SetAbsorptionLedger(&stubAbsorptionLedger{err: fmt.Errorf("connection reset")})

	headroom := h.absorptionHeadroomFn(absorptionTestCtx(), 1)
	if got := headroom(sinkLane("COPPER", 60)); got != 0 {
		t.Fatalf("headroom = %d on an unreadable ledger, want 0. Returning a positive number — or nil, "+
			"which reads as 'not consulted' — would let an unbounded buy through on exactly the failure "+
			"the guard exists for", got)
	}

	lane := pricedLongHaulLane{OptimalUnits: 60}
	lane.Lane = sinkLane("COPPER", 60)
	envelope := longHaulEnvelope{perHaulCap: 1_000_000}
	if units := achievableUnits(lane, 100, envelope, 0); units != 0 {
		t.Fatalf("sized units = %d on an unreadable ledger, want 0", units)
	}
}

// A live recovery shadow on the sink means an earlier dump is still being absorbed. Zero
// headroom, matching the trade-route consult's outright block on the same signal — a residual is
// depth that has not come back yet, not depth that is free.
func TestAbsorptionHeadroom_RecoveryShadowYieldsNoHeadroom(t *testing.T) {
	h := &RunLongHaulArbHandler{}
	h.SetAbsorptionLedger(&stubAbsorptionLedger{pools: map[absorption.LaneKey]absorption.KeyOccupancy{
		sellKey("COPPER"): {RecoveringResidual: 12.5},
	}})

	if got := h.absorptionHeadroomFn(absorptionTestCtx(), 1)(sinkLane("COPPER", 60)); got != 0 {
		t.Fatalf("headroom = %d with a recovery shadow on the sink, want 0", got)
	}
}

// ONE read per episode, not one per candidate lane. The predecessor signature was invoked inside
// selectHauls for every ranked lane; had it ever been wired to a database it would have issued a
// query per lane per episode. This pins the batched shape that replaced it.
func TestAbsorptionHeadroom_ReadsTheLedgerOncePerEpisode(t *testing.T) {
	ledger := &stubAbsorptionLedger{pools: map[absorption.LaneKey]absorption.KeyOccupancy{}}
	h := &RunLongHaulArbHandler{}
	h.SetAbsorptionLedger(ledger)

	headroom := h.absorptionHeadroomFn(absorptionTestCtx(), 1)
	for _, good := range []string{"COPPER", "IRON", "FUEL", "GOLD", "SILVER"} {
		headroom(sinkLane(good, 60))
	}

	if ledger.reads != 1 {
		t.Fatalf("ledger reads = %d for one episode over 5 lanes, want exactly 1 — a per-lane read is a "+
			"database round trip per candidate", ledger.reads)
	}
}

// A nil ledger must leave sizing byte-identical to the pre-wiring behaviour, so any caller that
// has not wired one (every existing worker test) is unaffected.
func TestAbsorptionHeadroom_NilLedgerIsNotConsulted(t *testing.T) {
	h := &RunLongHaulArbHandler{}
	if fn := h.absorptionHeadroomFn(absorptionTestCtx(), 1); fn != nil {
		t.Fatal("an unwired ledger must yield a nil consult, which selectHauls reads as headroom -1 (not consulted)")
	}
}

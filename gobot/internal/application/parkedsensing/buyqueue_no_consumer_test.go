package parkedsensing

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// cappedProbeWave is the LIVE frame of sp-5iz7a as the drain sees it: the regime is PROBE, its
// published reason is the borrowed "unreachable" (sp-suzfh), the reservation is zero because the
// cap is met — and the fourth answer is the one that says probe spending has no consumer.
//
// THE REGIME IS A REAL PROBE, which is what makes this test worth anything: the drain would buy on
// this wave today, so a zero bought count is evidence of the new gate rather than of the old one.
func cappedProbeWave() *waveReader {
	return &waveReader{
		wave:   common.WaveProbe,
		reason: common.WaveProbeReasonUnreachable,
		target: 0,
		hold:   common.ProbeSpendHoldHeavyCapped,
	}
}

// THE LIVE FRAME, at the gate that actually pays. 780,000 credits, a placement queued, a yard
// willing to sell — every condition the fleet was buying 42 probes an hour under — and the drain
// now refuses because the depth those probes buy has no consumer while the heavy class is capped.
func TestDrain_NoConsumerHoldBuysNoProbe(t *testing.T) {
	ports, led, pur, _ := oneFillPorts(780_000)
	ports.Wave = cappedProbeWave()

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Bought != 0 || len(pur.buys) != 0 {
		t.Fatalf("bought %d probes (report Bought=%d) into a capped fleet's ample depth — this is the 1,585,996-credit hour", len(pur.buys), rep.Bought)
	}
	if len(pur.quotes) != 0 {
		t.Fatalf("read %d live shipyard prices, want 0: a tick that may not buy must not price anything either", len(pur.quotes))
	}
	if got := led.transitionsTo(SlotStateQueued); len(got) != 0 {
		t.Fatalf("claimed a slot for a purchase that will not happen: %+v", got)
	}
}

// THE ANTI-VACUITY CONTROL, and the whole reason the test above is not simply "probe buying is
// broken". The IDENTICAL fixture on the IDENTICAL PROBE wave, differing only in the hold, buys —
// so the refusal above is attributable to the hold and to nothing else in the setup.
func TestDrain_TheSameProbeWaveWithoutTheHoldStillBuys(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(780_000)
	wave := cappedProbeWave()
	wave.hold = common.ProbeSpendHoldNone
	ports.Wave = wave

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Bought != 1 || len(pur.buys) != 1 {
		t.Fatalf("bought %d probes (report Bought=%d) on an unheld PROBE wave, want 1 — the gate is stopping the ordinary expansion regime, which is a regression rather than a fix", len(pur.buys), rep.Bought)
	}
	if rep.ProbeSpendHold != common.ProbeSpendHoldNone {
		t.Fatalf("report claims a hold on a tick that bought: %q", rep.ProbeSpendHold)
	}
	if rep.SpendingPaused {
		t.Fatalf("report says spending was paused on a tick that bought a probe: %+v", rep)
	}
}

// THE REFUSAL NAMES ITSELF. The operator's question was "why are we buying probes again?", and the
// inverse needs an answer an operator can act on. SpendingPaused alone cannot give one: it is also
// what an expansion switch set to off looks like, and telling an operator whose switch is ON to go
// find that switch is the borrowed-cause failure this bead's sibling filed against the wave's enum.
func TestDrain_TheHoldIsReportedByName(t *testing.T) {
	ports, _, _, _ := oneFillPorts(780_000)
	ports.Wave = cappedProbeWave()

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.ProbeSpendHold != common.ProbeSpendHoldHeavyCapped {
		t.Fatalf("report does not name the refusal: got %q, want %q", rep.ProbeSpendHold, common.ProbeSpendHoldHeavyCapped)
	}
	if !rep.SpendingPaused {
		t.Fatalf("a refused purchase must still read as paused, so the existing filters keep working: %+v", rep)
	}
	if rep.Wave != common.WaveProbe {
		t.Fatalf("the hold must not move the regime the two readers share, got %q", rep.Wave)
	}
	if rep.WaveProbeReason != common.WaveProbeReasonUnreachable {
		t.Fatalf("the hold must not rewrite the wave's own reason, got %q", rep.WaveProbeReason)
	}
}

// THE FREE HALF KEEPS RUNNING, exactly as it does under the other two gates. A hull already paid
// for is not a purchase, and stopping the reuse pass would starve coverage to save nothing.
func TestDrain_TheHoldStopsPurchasesNotFreeWork(t *testing.T) {
	// The fixture TestDrain_ReusesParkedSpareInsteadOfBuying proves fills from a parked spare: a
	// wanted market placement beside an idle spare hull standing in the same system.
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-AA-M1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-AA-S1", System: "X1-AA", Kind: SlotKindSpare, State: SlotStateParked, AssignedShip: "PROBE-SPARE"},
		},
		systems: []ScreenedSystem{{System: "X1-AA", DepthCredits: 900}},
	}
	pur := &fakePurchaser{price: 1_000}
	ports := BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards:      &fakeYards{yards: map[string][]string{"X1-AA": {"X1-AA-Y1"}}},
		Ships:      &fakeShipReader{docked: map[string]string{"X1-AA-Y1": "BUYER"}},
		Fleet:      &fakeFleet{},
		Wave:       cappedProbeWave(),
	}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Reused != 1 {
		t.Fatalf("re-tasked %d idle spares under the hold, want 1 — a hull we already own costs nothing and the hold is about SPEND (%+v)", rep.Reused, rep)
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes under the hold", len(pur.buys))
	}
}

// AN UNWIRED WAVE READER HOLDS NOTHING. A wiring omission must leave probe buying exactly as it is
// today; inferring a refusal from an absent seam would stop the fleet on evidence nobody produced.
func TestDrain_UnwiredWaveReaderHoldsNothing(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(780_000)
	ports.Wave = nil

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.ProbeSpendHold != common.ProbeSpendHoldNone {
		t.Fatalf("an unwired reader invented a hold: %q", rep.ProbeSpendHold)
	}
	if len(pur.buys) != 1 {
		t.Fatalf("bought %d probes with no wave reader, want 1 — an unwired seam must not change what today does", len(pur.buys))
	}
}

// THE HOLD IS A THIRD GATE, NOT A REPLACEMENT FOR EITHER OTHER ONE. Each of the three stops
// purchases on its own, and the sweep is what stops a later edit from collapsing them into one term
// that a single fix could switch off. Note the HEAVY-wave row carries a hold too: the two never
// co-occur in production (a binding cap forces PROBE), but a gate that only worked while its
// neighbours agreed with it would be a gate resting on a coincidence.
func TestDrain_EachPurchaseGateStopsTheBuyOnItsOwn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		spendOn    bool
		wave       *waveReader
		wantPaused bool
		wantBuys   int
	}{
		{"all three open", true, &waveReader{wave: common.WaveProbe, reason: common.WaveProbeReasonLanesServed}, false, 1},
		{"operator switch off", false, &waveReader{wave: common.WaveProbe, reason: common.WaveProbeReasonLanesServed}, true, 0},
		{"heavy wave", true, heavyWave(1_916_613), true, 0},
		{"no-consumer hold", true, cappedProbeWave(), true, 0},
		{"heavy wave AND the hold", true, &waveReader{wave: common.WaveHeavy, hold: common.ProbeSpendHoldHeavyCapped}, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ports, _, pur, _ := oneFillPorts(780_000)
			ports.Wave = tc.wave
			knobs := capexKnobs
			knobs.SpendEnabled = tc.spendOn

			rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, knobs, fixedClock{time.Unix(1_700_000_000, 0)})
			if err != nil {
				t.Fatalf("DrainBuyQueue returned error: %v", err)
			}
			if len(pur.buys) != tc.wantBuys {
				t.Fatalf("%s: bought %d probes, want %d", tc.name, len(pur.buys), tc.wantBuys)
			}
			if rep.SpendingPaused != tc.wantPaused {
				t.Fatalf("%s: SpendingPaused=%v, want %v", tc.name, rep.SpendingPaused, tc.wantPaused)
			}
		})
	}
}

package parkedsensing

// buyqueue_spend_pause_test.go pins that the operator's expansion switch stops
// THIS queue buying, and stops nothing else.
//
// The defect it locks down (sp-com1h) was not a guard that failed. It was a guard
// that was consulted, worked exactly as documented, and did not cover the path
// that spends the most: `expansion_enabled=2` gated the requests expansion makes
// to OTHER engines, while the drain that pays for a coverage probe never read it.
// Measured live with the switch reading off: `bought 6 reused 0 queued 5` every
// cycle for over an hour, 25 hulls, 907,545 credits.
//
// EVERY MONEY FIXTURE HERE USES THE DOCUMENTED DEFAULT FLOORS, and that is the
// point of the file rather than an incidental choice. The leak was stopped at the
// time by pushing capex_reserve_credits to 5,000,000 and capital_multiplier_k_milli
// to 10,000 — a workaround that starves legitimate coverage buying too and
// un-fixes itself the moment the treasury outgrows it. A test run against those
// floors would prove the workaround, not the fix. So the knobs below are
// capexKnobs (100_000 / 2000, the documented defaults) with SpendEnabled flipped,
// and the paired ON case buys — which is what makes the OFF case evidence.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// pausedCapexKnobs is capexKnobs with the operator's switch off and NOTHING else
// changed. Constructed from capexKnobs by assignment rather than re-listed, so a
// future change to the documented floors cannot leave this file silently testing
// a different economy from the one it is comparing against.
func pausedCapexKnobs() BuyKnobs {
	k := capexKnobs
	k.SpendEnabled = false
	return k
}

// THE ACCEPTANCE TEST. Same world, same treasury and the same default floors that
// TestDrain_BuysAboveDynamicFloor proves buy a probe — with the switch off, not a
// credit moves.
//
// The two are deliberately a matched pair. A test that only asserted "off buys
// nothing" would pass just as well against a fixture too poor to buy anything,
// which is exactly how the live floor workaround masqueraded as a fix.
func TestDrain_ExpansionSwitchOffBuysNothingAtDefaultFloors(t *testing.T) {
	ports, led, pur, _ := oneFillPorts(780_000) // 780_000 − 23_540 ≥ the 750_000 default floor

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, pausedCapexKnobs(), fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("a paused drain must not error: %v", err)
	}

	if rep.Bought != 0 || len(pur.buys) != 0 {
		t.Fatalf("bought %d probes (report Bought=%d) with expansion_enabled off at the DEFAULT floors — "+
			"this is sp-com1h: the switch reads off and the coverage queue spends anyway (%v)",
			len(pur.buys), rep.Bought, pur.buys)
	}
	if len(pur.quotes) != 0 {
		t.Fatalf("read %d live shipyard prices while paused, want 0: a tick that may not buy must not "+
			"pay API budget to price a hull it cannot pay for (%v)", len(pur.quotes), pur.quotes)
	}
	if rep.Queued != 0 {
		t.Fatalf("claimed %d placements for a purchase that cannot happen, want 0: a QUEUED slot with no "+
			"buy behind it is the reading that overstates spend", rep.Queued)
	}
	if !rep.SpendingPaused {
		t.Fatalf("report does not say WHY nothing was bought: %+v — the cycle line has to name the "+
			"switch, or the next money hunt starts at the treasury again", rep)
	}
	if rep.CapHeld || rep.FloorHeld {
		t.Fatalf("paused tick reported a ceiling it never evaluated (CapHeld=%v FloorHeld=%v): those "+
			"send an operator looking at the treasury for a decision they made themselves",
			rep.CapHeld, rep.FloorHeld)
	}
	if got := led.transitionsTo(SlotStateQueued); len(got) != 0 {
		t.Fatalf("paused tick claimed a slot for purchase: %+v", got)
	}
	if got := led.transitionsTo(SlotStateBought); len(got) != 0 {
		t.Fatalf("paused tick recorded a purchase: %+v", got)
	}
}

// The other half of the pair, in this file so the two can never drift apart: with
// the switch ON the identical fixture buys. A gate wired permanently closed would
// pass every OFF assertion above and fail here.
func TestDrain_ExpansionSwitchOnStillBuysAtDefaultFloors(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(780_000)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Bought != 1 || len(pur.buys) != 1 {
		t.Fatalf("the switch ON bought %d probes (report Bought=%d), want 1 — the OFF assertions in this "+
			"file are only evidence if this fixture can actually spend", len(pur.buys), rep.Bought)
	}
	if rep.SpendingPaused {
		t.Fatalf("report claims spending is paused with the switch ON: %+v", rep)
	}
}

// THE GATE SITS AHEAD OF THE MONEY READS, proven by breaking them.
//
// A paused drain never asks what the treasury holds, so a treasury that cannot
// answer is not an error for it. Written this way round on purpose: it is the one
// assertion that fails if the gate is moved BELOW the reads — where it would still
// buy nothing, still report SpendingPaused, and pass every test above while
// spending an API call per tick forever and reporting a fail-closed error whenever
// the API was down.
func TestDrain_SpendPauseReadsNeitherTreasuryNorFleetCount(t *testing.T) {
	ports, led, pur, _ := oneFillPorts(780_000)
	treasury := &fakeTreasury{credits: 999_999_999, err: errors.New("api down")}
	ports.Treasury = treasury
	led.ownedErr = errors.New("db down")

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, pausedCapexKnobs(), fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("a paused drain surfaced a money-read error it had no reason to take: %v", err)
	}
	if treasury.calls != 0 {
		t.Fatalf("paused drain called LiveCredits %d times, want 0 (one live API read per tick, forever, "+
			"to price nothing)", treasury.calls)
	}
	if rep.Bought != 0 || len(pur.buys) != 0 {
		t.Fatalf("bought %d probes while paused: %v", len(pur.buys), pur.buys)
	}
	if !rep.SpendingPaused {
		t.Fatalf("report does not flag SpendingPaused: %+v", rep)
	}
}

// FREE WORK SURVIVES THE PAUSE, part one: an idle spare already parked in-system
// still fills a placement.
//
// This is the half of "off" that must NOT stop, and the reason the gate is inside
// the drain rather than at its call site. Switching the whole queue off would save
// nothing — a reuse costs no credit and no API call — while leaving markets we
// have already bought hulls for unwatched. It is the same lesson expansion learned
// when its own off-switch starved 308 owned probes of destinations.
func TestDrain_SpendPauseStillReusesAParkedSpare(t *testing.T) {
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
	}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, pausedCapexKnobs(), fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Reused != 1 {
		t.Fatalf("report says Reused=%d, want 1 — a spend pause that also stops re-tasking hulls we "+
			"already own saves nothing and blinds the markets we bought them for: %+v", rep.Reused, rep)
	}
	if rep.Bought != 0 || len(pur.buys) != 0 {
		t.Fatalf("bought %d probes while paused: %v", len(pur.buys), pur.buys)
	}
	if got := led.transitionsTo(SlotStateInTransit); len(got) != 1 || got[0].waypoint != "X1-AA-M1" {
		t.Fatalf("the target placement was not claimed for the spare: %+v", led.transitions)
	}
}

// FREE WORK SURVIVES THE PAUSE, part two: the gate-crossing foothold.
//
// It is the path with the strongest claim to being cut with the purchases, because
// it only exists to make a system able to BUY for itself — and it is still free
// (gate-store reads and two ledger rows, no credits, no API), so it stays. A
// foothold placed while the switch is off is a market watched by a hull already
// paid for.
func TestDrain_SpendPauseStillFillsAFoothold(t *testing.T) {
	ports, led, world := footholdPorts(liveManned())

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 40}, fixedClock{})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Footholds == 0 {
		t.Fatalf("no foothold established while paused; report %+v — the foothold spends nothing, so "+
			"the spend pause must not reach it", rep)
	}
	if rep.Bought != 0 {
		t.Fatalf("bought %d probes while paused", rep.Bought)
	}
	if !rep.SpendingPaused {
		t.Fatalf("report does not flag SpendingPaused: %+v", rep)
	}

	target := slotAt(t, led, "X1-GF41-Y1", SlotKindSpare)
	if target.State != SlotStateInTransit || target.AssignedShip == "" {
		t.Fatalf("foothold target not claimed while paused: %+v", target)
	}
	// The drain writes the row; the placement machine flies it. Same contract as
	// the unpaused path — asserted so a pause cannot quietly start moving hulls.
	if pos := world.positions[target.AssignedShip]; pos.Waypoint == "X1-GF41-Y1" {
		t.Fatalf("paused drain moved a hull inside the tick")
	}
}

// A placement the pause could not fill is left WANTED and is NOT filed as
// SkippedNoYard.
//
// SkippedNoYard means something specific — no counter in this system has a hull of
// ours to buy through — and it is benign, so it is exactly the wrong bucket for
// "the operator switched buying off". An operator watching that counter climb
// would go looking for missing presence in systems that have plenty.
func TestDrain_SpendPauseDoesNotFileSkippedNoYard(t *testing.T) {
	ports, led, _, _ := oneFillPorts(780_000)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, pausedCapexKnobs(), fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.SkippedNoYard != 0 {
		t.Fatalf("report says SkippedNoYard=%d for a placement whose system HAS a usable yard, want 0: "+
			"the reason is the switch, and mislabelling it sends the next investigation to the map", rep.SkippedNoYard)
	}
	if got := slotAt(t, led, "X1-AA-M1", SlotKindMarket); got.State != SlotStateWanted {
		t.Fatalf("paused tick moved the placement out of WANTED to %s — it must be waiting, unclaimed, "+
			"for the tick the switch comes back on", got.State)
	}
}

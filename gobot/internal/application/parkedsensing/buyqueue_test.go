package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
)

// --- fakes -------------------------------------------------------------------
//
// Every fail-closed fake returns its error ALONGSIDE the value that would be
// most DANGEROUS if the guard read it anyway: a HIGH treasury (looks
// affordable), a ZERO cargo spend (lowest possible floor), a ZERO probe count
// (cap looks wide open). A guard that leaks its error therefore does not merely
// misreport — it buys, and the test catches it.

type transitionCall struct {
	waypoint, from, to         string
	assignedShip, purchaseYard *string
}

type fakeBuyLedger struct {
	slots   []QueuedSlot
	systems []ScreenedSystem
	owned   int64

	slotsErr, systemsErr, ownedErr error
	// coverageErr fails ONLY the hull-bearing read coverageBySystem makes, so a
	// test can exercise that guard without also breaking the candidate read that
	// runs before it.
	coverageErr   error
	transitionErr map[string]error

	transitions  []transitionCall
	systemsCalls int
}

func (f *fakeBuyLedger) SlotsByState(_ context.Context, _ int, states ...string) ([]QueuedSlot, error) {
	if f.slotsErr != nil {
		return nil, f.slotsErr
	}
	want := make(map[string]bool, len(states))
	for _, s := range states {
		want[s] = true
	}
	if f.coverageErr != nil && !want[SlotStateWanted] {
		// Adversarial: a populated coverage map alongside the error, so a guard
		// that leaks it orders on numbers it could not read.
		return []QueuedSlot{{Waypoint: "X1-GHOST-P1", System: "X1-GHOST", State: SlotStateParked}}, f.coverageErr
	}
	var out []QueuedSlot
	for _, s := range f.slots {
		if want[s.State] {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeBuyLedger) SlotsBySystem(_ context.Context, _ int, system string) ([]QueuedSlot, error) {
	if f.slotsErr != nil {
		return nil, f.slotsErr
	}
	var out []QueuedSlot
	for _, s := range f.slots {
		if s.System == system {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeBuyLedger) SystemsByVerdict(_ context.Context, _ int, _ string) ([]ScreenedSystem, error) {
	f.systemsCalls++
	if f.systemsErr != nil {
		return nil, f.systemsErr
	}
	return f.systems, nil
}

func (f *fakeBuyLedger) CountOwnedProbes(_ context.Context, _ int) (int64, error) {
	if f.ownedErr != nil {
		return 0, f.ownedErr // adversarial: "no probes owned" reads as a wide-open cap
	}
	return f.owned, nil
}

// TransitionSlot keys its injected failures on the EDGE (waypoint→toState), not
// the waypoint, so a test can break the claim and the record independently —
// they sit on opposite sides of the purchase and fail in opposite directions.
func (f *fakeBuyLedger) TransitionSlot(_ context.Context, _ int, waypoint, kind, from, to string, set SlotFields) error {
	f.transitions = append(f.transitions, transitionCall{waypoint, from, to, set.AssignedShip, set.PurchaseYard})
	if err := f.transitionErr[waypoint+"→"+to]; err != nil {
		return err
	}
	for i := range f.slots {
		// Matched on the KIND too, as the real ledger is (sp-dpfp8): a waypoint can
		// carry a MARKET and a SPARE row, and they are often in the same state.
		if f.slots[i].Waypoint != waypoint || f.slots[i].Kind != kind {
			continue
		}
		if f.slots[i].State != from {
			return errors.New("state conflict")
		}
		f.slots[i].State = to
		if set.AssignedShip != nil {
			f.slots[i].AssignedShip = *set.AssignedShip
		}
		if set.PurchaseYard != nil {
			f.slots[i].PurchaseYard = *set.PurchaseYard
		}
		return nil
	}
	return errors.New("no such slot")
}

func (f *fakeBuyLedger) transitionsTo(state string) []transitionCall {
	var out []transitionCall
	for _, t := range f.transitions {
		if t.to == state {
			out = append(out, t)
		}
	}
	return out
}

type fakeTreasury struct {
	credits int64
	err     error
	calls   int
}

func (f *fakeTreasury) LiveCredits(_ context.Context, _ int) (int64, error) {
	f.calls++
	if f.err != nil {
		return 999_999_999, f.err // adversarial: a treasury that looks bottomless
	}
	return f.credits, nil
}

type fakeCargoSpend struct {
	spend int64
	err   error
	since time.Time
}

func (f *fakeCargoSpend) AbsCargoBuySpendSince(_ context.Context, _ int, since time.Time) (int64, error) {
	f.since = since
	if f.err != nil {
		return 0, f.err // adversarial: zero spend is the lowest possible floor
	}
	return f.spend, nil
}

type buyCall struct{ ship, yard, owner string }

type fakePurchaser struct {
	price        int64
	creditsAfter int64
	quoteErr     error
	buyErr       error

	// reportNoPrice models a purchase path that completed but reported no price
	// back, so the accounting has only the quote to work from.
	reportNoPrice bool
	// chargedPrice, when non-zero, is what the counter ACTUALLY bills —
	// independent of the quote, so a test can model a market that moved between
	// pricing and paying.
	chargedPrice int64
	// quoteErrAt and buyErrAt fail only at the named yards, so a test can make
	// one counter unusable while leaving its neighbours healthy.
	quoteErrAt map[string]error
	buyErrAt   map[string]error

	quotes []string
	buys   []buyCall
	nextID int
}

func (f *fakePurchaser) Quote(_ context.Context, _ int, yard string) (int64, error) {
	f.quotes = append(f.quotes, yard)
	if err := f.quoteErrAt[yard]; err != nil {
		return 1, err // adversarial: a suspiciously cheap probe
	}
	if f.quoteErr != nil {
		return 1, f.quoteErr
	}
	return f.price, nil
}

func (f *fakePurchaser) Buy(_ context.Context, _ int, ship, yard, owner string) (BoughtProbe, error) {
	f.buys = append(f.buys, buyCall{ship, yard, owner})
	if err := f.buyErrAt[yard]; err != nil {
		return BoughtProbe{}, err
	}
	if f.buyErr != nil {
		return BoughtProbe{}, f.buyErr
	}
	f.nextID++
	charged := f.price
	if f.chargedPrice != 0 {
		charged = f.chargedPrice
	}
	if f.reportNoPrice {
		charged = 0
	}
	return BoughtProbe{
		ShipSymbol:   "PROBE-" + string(rune('A'+f.nextID-1)),
		Price:        charged,
		CreditsAfter: f.creditsAfter,
	}, nil
}

type fakeYards struct {
	yards map[string][]string // system → probe yards, cheapest first
	err   error
}

func (f *fakeYards) ListProbeYards(_ context.Context, system string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.yards[system], nil
}

type fakeShipReader struct {
	docked    map[string]string // waypoint → docked probe symbol
	positions map[string]ShipPos
	dockedErr error
	atErr     error
}

func (f *fakeShipReader) DockedProbeAt(_ context.Context, _ int, waypoint string) (string, bool, error) {
	if f.dockedErr != nil {
		return "", false, f.dockedErr
	}
	s, ok := f.docked[waypoint]
	return s, ok, nil
}

func (f *fakeShipReader) ShipAt(_ context.Context, _ int, ship string) (ShipPos, error) {
	if f.atErr != nil {
		return ShipPos{}, f.atErr
	}
	return f.positions[ship], nil
}

type fleetAssign struct{ ship, fleet string }

type fakeFleet struct {
	assigns []fleetAssign
	err     error
}

func (f *fakeFleet) AssignFleet(_ context.Context, _ int, ship, fleet string) error {
	f.assigns = append(f.assigns, fleetAssign{ship, fleet})
	return f.err
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time        { return c.now }
func (c fixedClock) Sleep(_ time.Duration) {}

// --- helpers -----------------------------------------------------------------

// oneFillPorts builds the minimal viable drain: one WANTED MARKET slot in an
// IN_SCOPE system, one probe yard in that system with a docked probe of ours
// standing on it (so the buy is executable).
func oneFillPorts(treasury int64) (BuyPorts, *fakeBuyLedger, *fakePurchaser, *fakeCargoSpend) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{{
			Waypoint: "X1-AA-M1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateWanted, DepthCredits: 900,
		}},
		systems: []ScreenedSystem{{System: "X1-AA", DepthCredits: 900}},
	}
	pur := &fakePurchaser{price: 23_540}
	spend := &fakeCargoSpend{spend: 300_000}
	return BuyPorts{
		Treasury:   &fakeTreasury{credits: treasury},
		CargoSpend: spend,
		Purchaser:  pur,
		Ledger:     led,
		Yards:      &fakeYards{yards: map[string][]string{"X1-AA": {"X1-AA-Y1"}}},
		Ships:      &fakeShipReader{docked: map[string]string{"X1-AA-Y1": "PROBE-OLD"}},
		Fleet:      &fakeFleet{},
	}, led, pur, spend
}

// capexKnobs are the Step-1 floor knobs: 50_000 immutable + 100_000 capex +
// 2 × 300_000/h cargo runway = a 750_000 floor.
var capexKnobs = BuyKnobs{ProbeCap: 100, CapexReserve: 100_000, KMilli: 2000}

// --- Step 1: the dynamic floor, integrated -----------------------------------

func TestDrain_RefusesBuyBelowDynamicFloor(t *testing.T) {
	// 770_000 − 23_540 = 746_460 < 750_000 → the probe is unaffordable.
	ports, _, pur, spend := oneFillPorts(770_000)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes below the dynamic floor, want 0 (%v)", len(pur.buys), pur.buys)
	}
	if rep.Bought != 0 {
		t.Fatalf("report says Bought=%d, want 0", rep.Bought)
	}
	if !rep.FloorHeld {
		t.Fatalf("report does not flag FloorHeld: %+v", rep)
	}
	if want := time.Unix(1_700_000_000, 0).Add(-time.Hour); !spend.since.Equal(want) {
		t.Fatalf("cargo spend window started %v, want %v (trailing hour)", spend.since, want)
	}
}

func TestDrain_BuysAboveDynamicFloor(t *testing.T) {
	// 780_000 − 23_540 = 756_460 ≥ 750_000 → affordable.
	ports, led, pur, _ := oneFillPorts(780_000)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 {
		t.Fatalf("made %d purchases above the floor, want 1 (%v)", len(pur.buys), pur.buys)
	}
	if pur.buys[0].yard != "X1-AA-Y1" {
		t.Fatalf("bought at yard %q, want the in-system probe yard X1-AA-Y1", pur.buys[0].yard)
	}
	if pur.buys[0].ship != "PROBE-OLD" {
		t.Fatalf("purchasing ship %q, want the probe already docked at the yard", pur.buys[0].ship)
	}
	if rep.Bought != 1 {
		t.Fatalf("report says Bought=%d, want 1", rep.Bought)
	}
	if got := led.transitionsTo(SlotStateBought); len(got) != 1 || got[0].assignedShip == nil {
		t.Fatalf("slot did not reach BOUGHT with a recorded hull: %+v", led.transitions)
	}
}

// TestDrain_FloorPinsImmutableReserve pins the cross-package constant the floor
// is built on: with every dynamic term at zero the floor collapses to the flat
// 50_000 working-capital reserve (RULINGS #5 — one immutable base, never a
// second copy that can drift from common.ImmutableReserveFloor).
func TestDrain_FloorPinsImmutableReserve(t *testing.T) {
	if got := domainSensing.ProbeBuyFloor(common.ImmutableReserveFloor, 0, 0, 0); got != 50_000 {
		t.Fatalf("ProbeBuyFloor(ImmutableReserveFloor,0,0,0) = %d, want 50000", got)
	}
}

// --- Step 1: fail-closed money reads -----------------------------------------

func TestDrain_FailsClosedOnTreasuryError(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(780_000)
	ports.Treasury = &fakeTreasury{credits: 999_999_999, err: errors.New("api down")}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Now()})
	if err == nil {
		t.Fatal("unreadable treasury did not surface an error")
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes on an unreadable treasury, want 0", len(pur.buys))
	}
	if rep.Bought != 0 {
		t.Fatalf("report says Bought=%d, want 0", rep.Bought)
	}
}

func TestDrain_FailsClosedOnCargoSpendError(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(780_000)
	ports.CargoSpend = &fakeCargoSpend{spend: 0, err: errors.New("ledger down")}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Now()})
	if err == nil {
		t.Fatal("unreadable cargo spend did not surface an error")
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes on an unknowable cargo spend, want 0", len(pur.buys))
	}
	if rep.Bought != 0 {
		t.Fatalf("report says Bought=%d, want 0", rep.Bought)
	}
}

func TestDrain_FailsClosedOnProbeCountError(t *testing.T) {
	ports, led, pur, _ := oneFillPorts(780_000)
	led.ownedErr = errors.New("db down")

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Now()})
	if err == nil {
		t.Fatal("unreadable probe count did not surface an error")
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes on an unreadable probe cap, want 0", len(pur.buys))
	}
	if rep.Bought != 0 {
		t.Fatalf("report says Bought=%d, want 0", rep.Bought)
	}
}

// --- Step 4: priority order, spare reuse, and the cap -------------------------

// multiSystemPorts wires four placements across four systems of descending
// depth, each with a probe yard we already have a hull standing at, plus one
// frontier SPARE seed in a system carrying no verdict at all.
func multiSystemPorts() (BuyPorts, *fakeBuyLedger, *fakePurchaser) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-SHALLOW-M1", System: "X1-SHALLOW", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-DEEP-M1", System: "X1-DEEP", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-DEEP-M2", System: "X1-DEEP", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-FRONTIER-S1", System: "X1-FRONTIER", Kind: SlotKindSpare, State: SlotStateWanted},
			{Waypoint: "X1-MID-M1", System: "X1-MID", Kind: SlotKindMarket, State: SlotStateWanted},
		},
		systems: []ScreenedSystem{
			{System: "X1-SHALLOW", DepthCredits: 1_000},
			{System: "X1-DEEP", DepthCredits: 5_000},
			{System: "X1-MID", DepthCredits: 3_000},
		},
	}
	pur := &fakePurchaser{price: 1_000}
	return BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards: &fakeYards{yards: map[string][]string{
			"X1-SHALLOW":  {"X1-SHALLOW-Y1"},
			"X1-DEEP":     {"X1-DEEP-Y1"},
			"X1-MID":      {"X1-MID-Y1"},
			"X1-FRONTIER": {"X1-FRONTIER-Y1"},
		}},
		Ships: &fakeShipReader{docked: map[string]string{
			"X1-SHALLOW-Y1":  "BUYER-SHALLOW",
			"X1-DEEP-Y1":     "BUYER-DEEP",
			"X1-MID-Y1":      "BUYER-MID",
			"X1-FRONTIER-Y1": "BUYER-FRONTIER",
		}},
		Fleet: &fakeFleet{},
	}, led, pur
}

func TestDrain_SpreadsAcrossSystemsBeforeDeepeningOne(t *testing.T) {
	// Coverage first, depth as the tiebreak WITHIN a coverage tier. X1-DEEP holds
	// two placements: its FIRST is served ahead of everything (deepest at coverage
	// 0), but its SECOND ranks at coverage 1 and therefore waits behind every
	// system still on 0 — so one drain reaches three systems instead of spending
	// its head on the richest one.
	//
	// This test previously asserted DEEP, DEEP, MID, SHALLOW: pure depth order,
	// which is the concentration this ordering exists to remove.
	ports, _, pur := multiSystemPorts()

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	// ...and only then the unscreened frontier seed, which still sorts last.
	want := []string{"X1-DEEP-Y1", "X1-MID-Y1", "X1-SHALLOW-Y1", "X1-DEEP-Y1", "X1-FRONTIER-Y1"}
	if len(pur.buys) != len(want) {
		t.Fatalf("made %d purchases, want %d: %v", len(pur.buys), len(want), pur.buys)
	}
	for i, w := range want {
		if pur.buys[i].yard != w {
			t.Fatalf("purchase %d was at %q, want %q (full order %v)", i, pur.buys[i].yard, w, pur.buys)
		}
	}
}

func TestDrain_AlreadyParkedProbesPushARichSystemDownTheOrder(t *testing.T) {
	// X1-DEEP is the deeper system AND its placement is FIRST in ledger order, so
	// both of the old sort's keys point at it. It already holds two parked probes;
	// X1-MID holds none. Only effective coverage puts X1-MID first.
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-DEEP-M1", System: "X1-DEEP", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-DEEP-P1", System: "X1-DEEP", Kind: SlotKindMarket, State: SlotStateParked, AssignedShip: "PROBE-D1"},
			{Waypoint: "X1-DEEP-P2", System: "X1-DEEP", Kind: SlotKindMarket, State: SlotStateParked, AssignedShip: "PROBE-D2"},
			{Waypoint: "X1-MID-M1", System: "X1-MID", Kind: SlotKindMarket, State: SlotStateWanted},
		},
		systems: []ScreenedSystem{
			{System: "X1-DEEP", DepthCredits: 5_000},
			{System: "X1-MID", DepthCredits: 3_000},
		},
	}
	pur := &fakePurchaser{price: 1_000}
	ports := BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards: &fakeYards{yards: map[string][]string{
			"X1-DEEP": {"X1-DEEP-Y1"},
			"X1-MID":  {"X1-MID-Y1"},
		}},
		Ships: &fakeShipReader{docked: map[string]string{
			"X1-DEEP-Y1": "BUYER-DEEP",
			"X1-MID-Y1":  "BUYER-MID",
		}},
		Fleet: &fakeFleet{},
	}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	want := []string{"X1-MID-Y1", "X1-DEEP-Y1"}
	if len(pur.buys) != len(want) {
		t.Fatalf("made %d purchases, want %d: %v", len(pur.buys), len(want), pur.buys)
	}
	for i, w := range want {
		if pur.buys[i].yard != w {
			t.Fatalf("purchase %d was at %q, want %q (full order %v)", i, pur.buys[i].yard, w, pur.buys)
		}
	}
}

func TestDrain_FilledRowsAreCountedAsCoverageButNeverBoughtFor(t *testing.T) {
	// The candidate query reads the FILLED states so coverage can be measured
	// from them. Not one of them may fall through into the candidate list: every
	// such row already names a hull we have paid for, so working it would buy a
	// second probe for a waypoint that has one — the money-unsafe direction.
	//
	// The PARKED SPARE is the sharpest case: spares are seeds, and a seed skips
	// the scope filter entirely, so a missing state guard would carry it straight
	// into the queue.
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-AA-S1", System: "X1-AA", Kind: SlotKindSpare, State: SlotStateParked, AssignedShip: "PROBE-SPARE"},
			{Waypoint: "X1-AA-P1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateParked, AssignedShip: "PROBE-M1"},
			{Waypoint: "X1-AA-T1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateInTransit, AssignedShip: "PROBE-M2"},
			{Waypoint: "X1-AA-B1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateBought, AssignedShip: "PROBE-M3"},
		},
		systems: []ScreenedSystem{{System: "X1-AA", DepthCredits: 900}},
	}
	pur := &fakePurchaser{price: 1_000}
	treasury := &fakeTreasury{credits: 10_000_000}
	ports := BuyPorts{
		Treasury:   treasury,
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards:      &fakeYards{yards: map[string][]string{"X1-AA": {"X1-AA-Y1"}}},
		Ships:      &fakeShipReader{docked: map[string]string{"X1-AA-Y1": "BUYER-AA"}},
		Fleet:      &fakeFleet{},
	}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %v for placements that already hold a hull", pur.buys)
	}
	if rep.Bought != 0 || rep.Reused != 0 || rep.Queued != 0 || rep.Attempts != 0 {
		t.Fatalf("report = %+v, want an entirely idle tick", rep)
	}
	// The cheapest-first gate returns before the treasury is read when nothing is
	// queued, so an untouched treasury is proof no candidate was produced.
	if treasury.calls != 0 {
		t.Fatalf("treasury read %d times, want 0 — a filled row became a candidate", treasury.calls)
	}
}

func TestDrain_UnreadableCoverageBuysNothingThisTick(t *testing.T) {
	// Reading an unavailable ledger as "no coverage anywhere" would rank every
	// system at zero and hand the whole budget to whichever sorts deepest — the
	// concentration this ordering exists to prevent, arriving silently and
	// exactly when the ledger is unwell.
	ports, led, pur := multiSystemPorts()
	led.coverageErr = errors.New("ledger unavailable")

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err == nil {
		t.Fatal("an unreadable coverage read must stop the drain, not order on a blind zero")
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %v against coverage we could not read", pur.buys)
	}
}

func TestDrain_SkipsPlacementsInSystemsNotInScope(t *testing.T) {
	ports, led, pur := multiSystemPorts()
	led.systems = []ScreenedSystem{{System: "X1-DEEP", DepthCredits: 5_000}}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	for _, b := range pur.buys {
		if b.yard != "X1-DEEP-Y1" && b.yard != "X1-FRONTIER-Y1" {
			t.Fatalf("bought at %q for a system with no IN_SCOPE verdict (%v)", b.yard, pur.buys)
		}
	}
}

func TestDrain_ReusesParkedSpareInsteadOfBuying(t *testing.T) {
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

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes when a spare was parked in-system, want 0 (%v)", len(pur.buys), pur.buys)
	}
	if rep.Reused != 1 {
		t.Fatalf("report says Reused=%d, want 1: %+v", rep.Reused, rep)
	}

	// The two-row hand-off must CLAIM before it RELEASES: a crash between the
	// writes then double-counts the hull (cap reads high, buys fewer) instead of
	// losing it (cap reads low, buys a replacement we already own).
	if len(led.transitions) != 2 {
		t.Fatalf("want exactly 2 transitions for a spare hand-off, got %+v", led.transitions)
	}
	claim, release := led.transitions[0], led.transitions[1]
	if claim.waypoint != "X1-AA-M1" || claim.to != SlotStateInTransit {
		t.Fatalf("first transition was %+v, want the TARGET claimed to IN_TRANSIT", claim)
	}
	if claim.assignedShip == nil || *claim.assignedShip != "PROBE-SPARE" {
		t.Fatalf("target claim did not record the reused hull: %+v", claim)
	}
	if release.waypoint != "X1-AA-S1" || release.to != SlotStateWanted {
		t.Fatalf("second transition was %+v, want the SPARE released to WANTED", release)
	}
	if release.assignedShip == nil || *release.assignedShip != "" {
		t.Fatalf("spare release did not CLEAR its hull reference: %+v", release)
	}
}

func TestDrain_HoldsAtProbeCap(t *testing.T) {
	ports, led, pur := multiSystemPorts()
	led.owned = 4

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 4}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes at the cap, want 0 (%v)", len(pur.buys), pur.buys)
	}
	if !rep.CapHeld {
		t.Fatalf("report does not flag CapHeld: %+v", rep)
	}
}

func TestDrain_StopsWhenCapIsReachedMidTick(t *testing.T) {
	ports, led, pur := multiSystemPorts()
	led.owned = 2

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 4}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 2 {
		t.Fatalf("bought %d probes with 2 of 4 cap headroom, want 2 (%v)", len(pur.buys), pur.buys)
	}
	if !rep.CapHeld {
		t.Fatalf("report does not flag CapHeld after filling the cap mid-tick: %+v", rep)
	}
}

// TestDrain_RechecksFloorAfterEachBuy proves the floor is re-evaluated against a
// treasury that SHRANK: two identical placements, enough credits for the first
// but not the second.
func TestDrain_RechecksFloorAfterEachBuy(t *testing.T) {
	ports, _, pur := multiSystemPorts()
	pur.price = 20_000
	ports.Treasury = &fakeTreasury{credits: 80_000} // floor is the flat 50_000 with K=0

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 {
		t.Fatalf("bought %d probes on a treasury that affords one, want 1 (%v)", len(pur.buys), pur.buys)
	}
	if !rep.FloorHeld {
		t.Fatalf("report does not flag FloorHeld after the treasury fell to the floor: %+v", rep)
	}
}

// TestDrain_ShipyardBalanceNeverRelaxesTheFloor pins the conservative choice: a
// shipyard reporting a HIGHER post-purchase balance than arithmetic allows is
// not believed, so it cannot buy a second probe the floor forbids.
func TestDrain_ShipyardBalanceNeverRelaxesTheFloor(t *testing.T) {
	ports, _, pur := multiSystemPorts()
	pur.price = 20_000
	pur.creditsAfter = 5_000_000 // an implausibly generous settlement report
	ports.Treasury = &fakeTreasury{credits: 80_000}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 {
		t.Fatalf("bought %d probes, want 1 — the shipyard's balance must not relax the floor (%v)", len(pur.buys), pur.buys)
	}
}

func TestDrain_TagsBoughtProbeIntoTheSensingFleet(t *testing.T) {
	ports, _, _, _ := oneFillPorts(10_000_000)
	fleet := &fakeFleet{}
	ports.Fleet = fleet

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(fleet.assigns) != 1 {
		t.Fatalf("made %d fleet assignments after a purchase, want 1: %+v", len(fleet.assigns), fleet.assigns)
	}
	if fleet.assigns[0].fleet != SensingParkedFleetTag {
		t.Fatalf("tagged the new probe %q, want %q", fleet.assigns[0].fleet, SensingParkedFleetTag)
	}
}

// --- Step 4: adversarial paths -----------------------------------------------

func TestDrain_SkipsPlacementWithNoReachableYard(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(10_000_000)
	ports.Ships = &fakeShipReader{} // no hull standing at any yard

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("no yard presence must not be an error, got: %v", err)
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes with no hull at any yard, want 0 (%v)", len(pur.buys), pur.buys)
	}
	if rep.SkippedNoYard != 1 {
		t.Fatalf("report says SkippedNoYard=%d, want 1: %+v", rep.SkippedNoYard, rep)
	}
}

// TestDrain_UsesParkedProbeAtYardRegardlessOfSlotKind pins the waypoint-wise
// presence contract: a yard that is also a whitelisted market carries a
// MARKET-kind slot, and the probe parked under it is still a valid buyer.
func TestDrain_UsesParkedProbeAtYardRegardlessOfSlotKind(t *testing.T) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-AA-M2", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateWanted},
			// The yard itself, slotted as MARKET because it also trades.
			{Waypoint: "X1-AA-Y1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateParked, AssignedShip: "PROBE-ON-YARD"},
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
		Ships:      &fakeShipReader{}, // the ships table knows nothing; the ledger must answer
		Fleet:      &fakeFleet{},
	}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 || pur.buys[0].ship != "PROBE-ON-YARD" {
		t.Fatalf("did not buy through the MARKET-slotted probe standing on the yard: %v", pur.buys)
	}
}

func TestDrain_FailedPurchaseLeavesSlotQueuedForRetry(t *testing.T) {
	ports, led, pur, _ := oneFillPorts(10_000_000)
	pur.buyErr = errors.New("shipyard refused")

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("a failed purchase must not fail the tick, got: %v", err)
	}
	if rep.Bought != 0 {
		t.Fatalf("report says Bought=%d after a failed purchase, want 0", rep.Bought)
	}
	if led.slots[0].State != SlotStateQueued {
		t.Fatalf("slot is %q after a failed purchase, want %q so the next tick retries it", led.slots[0].State, SlotStateQueued)
	}
	if led.slots[0].PurchaseYard != "X1-AA-Y1" {
		t.Fatalf("claim did not record the chosen yard: %+v", led.slots[0])
	}
}

func TestDrain_RetriesAnAlreadyQueuedSlot(t *testing.T) {
	ports, led, pur, _ := oneFillPorts(10_000_000)
	led.slots[0].State = SlotStateQueued
	led.slots[0].PurchaseYard = "X1-AA-Y1"

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 {
		t.Fatalf("made %d purchases for a stranded QUEUED slot, want 1 (%v)", len(pur.buys), pur.buys)
	}
	// It was already claimed; re-claiming it would be a wasted write.
	for _, tr := range led.transitions {
		if tr.to == SlotStateQueued {
			t.Fatalf("re-queued an already-QUEUED slot: %+v", led.transitions)
		}
	}
}

// TestDrain_HaltsWhenAPurchaseCannotBeRecorded pins the one unrecoverable shape:
// money left the account but the hull was not written down. Spending further
// against a ledger refusing writes would compound it.
func TestDrain_HaltsWhenAPurchaseCannotBeRecorded(t *testing.T) {
	ports, led, pur := multiSystemPorts()
	led.transitionErr = map[string]error{"X1-DEEP-M1→" + SlotStateBought: errors.New("db down")}

	_, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err == nil {
		t.Fatal("an unrecordable purchase did not surface an error")
	}
	if len(pur.buys) != 1 {
		t.Fatalf("kept spending after an unrecordable purchase: %v", pur.buys)
	}
}

// TestDrain_SkipsSlotClaimedByAnotherWriter covers the ROUTINE half of a failed
// claim: a concurrent tick got there first, which costs nothing and must not
// stop the queue serving the placements behind it.
func TestDrain_SkipsSlotClaimedByAnotherWriter(t *testing.T) {
	ports, led, pur := multiSystemPorts()
	led.transitionErr = map[string]error{"X1-DEEP-M1→" + SlotStateQueued: ErrSlotClaimed}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("losing a claim race must not fail the tick, got: %v", err)
	}
	if len(pur.buys) != 4 {
		t.Fatalf("made %d purchases after skipping one contested slot, want the other 4 (%v)", len(pur.buys), pur.buys)
	}
}

// TestDrain_HaltsWhenTheLedgerRefusesTheClaim covers the OTHER half: a claim
// that fails for any reason OTHER than contention is an outage, and retrying it
// across every remaining placement would just multiply the failure.
func TestDrain_HaltsWhenTheLedgerRefusesTheClaim(t *testing.T) {
	ports, led, pur := multiSystemPorts()
	led.transitionErr = map[string]error{"X1-DEEP-M1→" + SlotStateQueued: errors.New("db down")}

	_, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err == nil {
		t.Fatal("an unwritable ledger did not surface an error")
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes against an unwritable ledger, want 0 (%v)", len(pur.buys), pur.buys)
	}
}

func TestDrain_NoCandidatesCostsNoTreasuryRead(t *testing.T) {
	treasury := &fakeTreasury{credits: 10_000_000}
	ports := BuyPorts{
		Treasury:   treasury,
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  &fakePurchaser{},
		Ledger:     &fakeBuyLedger{},
		Yards:      &fakeYards{},
		Ships:      &fakeShipReader{},
		Fleet:      &fakeFleet{},
	}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if treasury.calls != 0 {
		t.Fatalf("read the live treasury %d times with nothing to buy, want 0", treasury.calls)
	}
}

// --- Step 4: yard fallback and the remaining fail-closed reads ----------------

// TestDrain_FallsBackWhenTheRecordedYardLostItsPresence pins that a recorded
// purchase yard is a preference, not a commitment. The hull that made a yard
// executable belongs to the wider fleet and can be flown off at any time; if
// that pinned the placement to a dead yard it would stall forever.
func TestDrain_FallsBackWhenTheRecordedYardLostItsPresence(t *testing.T) {
	ports, led, pur, _ := oneFillPorts(10_000_000)
	led.slots[0].State = SlotStateQueued
	led.slots[0].PurchaseYard = "X1-AA-GONE" // chosen last tick, now deserted
	ports.Yards = &fakeYards{yards: map[string][]string{"X1-AA": {"X1-AA-GONE", "X1-AA-Y1"}}}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 || pur.buys[0].yard != "X1-AA-Y1" {
		t.Fatalf("did not fall back to the yard that still has presence: %v", pur.buys)
	}
	// The row must end up naming where the hull actually came from.
	if led.slots[0].PurchaseYard != "X1-AA-Y1" {
		t.Fatalf("slot records purchase yard %q, want the yard the hull was bought at", led.slots[0].PurchaseYard)
	}
}

func TestDrain_PrefersTheRecordedYardWhenItStillHasPresence(t *testing.T) {
	ports, led, pur, _ := oneFillPorts(10_000_000)
	led.slots[0].State = SlotStateQueued
	led.slots[0].PurchaseYard = "X1-AA-Y2"
	ports.Yards = &fakeYards{yards: map[string][]string{"X1-AA": {"X1-AA-Y1", "X1-AA-Y2"}}}
	ports.Ships = &fakeShipReader{docked: map[string]string{
		"X1-AA-Y1": "BUYER-CHEAP", // cheapest-first would pick this
		"X1-AA-Y2": "BUYER-CHOSEN",
	}}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 || pur.buys[0].yard != "X1-AA-Y2" {
		t.Fatalf("abandoned a still-good recorded yard: %v", pur.buys)
	}
}

func TestDrain_SkipsPlacementWhoseProbeCannotBePriced(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(10_000_000)
	pur.quoteErr = errors.New("shipyard listing unreadable")

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("an unpriceable yard must not fail the tick, got: %v", err)
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes without a price to check the floor against, want 0 (%v)", len(pur.buys), pur.buys)
	}
	if rep.Bought != 0 {
		t.Fatalf("report says Bought=%d, want 0", rep.Bought)
	}
}

func TestDrain_FailsClosedOnYardPresenceReadError(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(10_000_000)
	ports.Ships = &fakeShipReader{dockedErr: errors.New("db down")}

	_, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err == nil {
		t.Fatal("an unreadable ships table did not surface an error")
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes without confirming a buyer exists, want 0 (%v)", len(pur.buys), pur.buys)
	}
}

func TestDrain_FailsClosedOnSlotReadError(t *testing.T) {
	ports, led, pur, _ := oneFillPorts(10_000_000)
	led.slotsErr = errors.New("db down")

	_, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err == nil {
		t.Fatal("an unreadable slot ledger did not surface an error")
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes against an unreadable ledger, want 0", len(pur.buys))
	}
}

func TestDrain_FailsClosedOnVerdictReadError(t *testing.T) {
	ports, led, pur, _ := oneFillPorts(10_000_000)
	led.systemsErr = errors.New("db down")

	_, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err == nil {
		t.Fatal("unreadable system verdicts did not surface an error")
	}
	// Unknown verdicts must never be read as "in scope" — that would buy hulls
	// for systems the screen may have rejected.
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes without knowing which systems are in scope, want 0", len(pur.buys))
	}
}

func TestDrain_FailsClosedOnYardCatalogError(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(10_000_000)
	ports.Yards = &fakeYards{err: errors.New("waypoint cache down")}

	_, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err == nil {
		t.Fatal("an unreadable yard catalog did not surface an error")
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes without a yard list, want 0", len(pur.buys))
	}
}

// TestDrain_CompletesPurchaseEvenIfTheFleetTagFails pins the deliberate
// asymmetry: the hull is already paid for and already recorded against the cap
// by the time it is tagged, so abandoning the purchase over a tag write would
// discard something we own. The placement machine re-asserts the tag.
func TestDrain_CompletesPurchaseEvenIfTheFleetTagFails(t *testing.T) {
	ports, led, pur, _ := oneFillPorts(10_000_000)
	ports.Fleet = &fakeFleet{err: errors.New("tag write failed")}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("a failed fleet tag must not fail the tick, got: %v", err)
	}
	if rep.Bought != 1 || len(pur.buys) != 1 {
		t.Fatalf("discarded a completed purchase over a tag write: Bought=%d buys=%v", rep.Bought, pur.buys)
	}
	if led.slots[0].State != SlotStateBought || led.slots[0].AssignedShip == "" {
		t.Fatalf("hull was not recorded against the cap: %+v", led.slots[0])
	}
}

// --- Fix round 1: the attempt budget, yard fallback, and price drift ---------

// manyFailingPorts wires more placements than the tick's attempt budget, all in
// one system whose single yard is executable but unpriceable — the shape of a
// degraded API.
func manyFailingPorts(slots int) (BuyPorts, *fakePurchaser) {
	led := &fakeBuyLedger{
		systems: []ScreenedSystem{{System: "X1-AA", DepthCredits: 900}},
	}
	for i := 0; i < slots; i++ {
		led.slots = append(led.slots, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-AA-M%02d", i), System: "X1-AA",
			Kind: SlotKindMarket, State: SlotStateWanted,
		})
	}
	pur := &fakePurchaser{price: 1_000, quoteErr: errors.New("shipyard unreachable")}
	return BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards:      &fakeYards{yards: map[string][]string{"X1-AA": {"X1-AA-Y1"}}},
		Ships:      &fakeShipReader{docked: map[string]string{"X1-AA-Y1": "BUYER"}},
		Fleet:      &fakeFleet{},
	}, pur
}

// TestDrain_BoundsAttemptsNotOnlyPurchases is the API-storm guard. Every quote
// is a LIVE, uncached shipyard read, so a budget that only decremented on
// success would fire one per unfilled placement, every tick, forever — hardest
// exactly when the API is already degraded.
func TestDrain_BoundsAttemptsNotOnlyPurchases(t *testing.T) {
	ports, pur := manyFailingPorts(20)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.quotes) > maxDrainAttempts {
		t.Fatalf("made %d live price reads for 20 failing placements, want at most %d", len(pur.quotes), maxDrainAttempts)
	}
	// All 20 placements share ONE yard, so they all meet the SAME counter. The
	// bound this test exists to defend is on LIVE READS, and re-asking one
	// counter twenty times is the thing it was written to stop — so a single
	// read here is the guard working harder, not a weakened budget. The
	// all-distinct-counters case is what still pins the full cap; see
	// TestDrain_CapsAttemptsWhenEveryCounterIsADifferentOne.
	if len(pur.quotes) != 1 {
		t.Fatalf("made %d live price reads of the SAME counter, want 1 (the refusal is the counter's, not each placement's)", len(pur.quotes))
	}
	if rep.Attempts > maxDrainAttempts {
		t.Fatalf("report says Attempts=%d, want at most the budget %d", rep.Attempts, maxDrainAttempts)
	}
	if rep.Bought != 0 {
		t.Fatalf("report says Bought=%d against an unpriceable yard, want 0", rep.Bought)
	}
	// Whatever the budget arithmetic, the reason must survive to the operator.
	if len(rep.Refusals) != 1 || rep.Refusals[0].Step != BuyStepQuote {
		t.Fatalf("20 blocked placements left no single readable quote refusal: %+v", rep.Refusals)
	}
	if rep.Refusals[0].Count != 20 {
		t.Fatalf("refusal blocked %d placements, want 20 — the count is what says this counter is holding the whole queue", rep.Refusals[0].Count)
	}
}

// TestDrain_CapsAttemptsWhenEveryCounterIsADifferentOne is the API-storm guard
// proper, and it is the one that survives the per-tick refusal memo.
//
// The memo collapses REPEATS of one counter; it must not be able to collapse
// distinct ones. So this fixture gives every candidate its own yard and its own
// buyer — nothing is deduplicable — and the full budget must still cap the live
// reads. Without this test the memo could silently become the only thing
// bounding the burst, and a system full of genuinely distinct dead yards would
// fire one live read per yard per tick, forever.
func TestDrain_CapsAttemptsWhenEveryCounterIsADifferentOne(t *testing.T) {
	const yards = 20
	led := &fakeBuyLedger{
		slots: []QueuedSlot{{
			Waypoint: "X1-AA-M1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateWanted,
		}},
		systems: []ScreenedSystem{{System: "X1-AA", DepthCredits: 900}},
	}
	yardList := make([]string, 0, yards)
	docked := make(map[string]string, yards)
	for i := 0; i < yards; i++ {
		yard := fmt.Sprintf("X1-AA-Y%02d", i)
		yardList = append(yardList, yard)
		docked[yard] = fmt.Sprintf("BUYER-%02d", i)
	}
	pur := &fakePurchaser{price: 1_000, quoteErr: errors.New("shipyard unreachable")}
	ports := BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards:      &fakeYards{yards: map[string][]string{"X1-AA": yardList}},
		Ships:      &fakeShipReader{docked: docked},
		Fleet:      &fakeFleet{},
	}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.quotes) != maxDrainAttempts {
		t.Fatalf("made %d live price reads across %d distinct dead yards, want exactly the budget %d",
			len(pur.quotes), yards, maxDrainAttempts)
	}
	if rep.Attempts != maxDrainAttempts {
		t.Fatalf("report says Attempts=%d, want the full budget %d spent on distinct counters", rep.Attempts, maxDrainAttempts)
	}
	if len(rep.Refusals) != maxDrainAttempts {
		t.Fatalf("recorded %d refusals for %d distinct refusing counters, want %d",
			len(rep.Refusals), maxDrainAttempts, maxDrainAttempts)
	}
}

func TestDrain_BoundsAttemptsWhenEveryCounterRefuses(t *testing.T) {
	ports, pur := manyFailingPorts(20)
	// Priceable, but every purchase is refused.
	pur.quoteErr = nil
	pur.buyErr = errors.New("shipyard refused")

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) > maxDrainAttempts {
		t.Fatalf("made %d purchase attempts, want at most %d", len(pur.buys), maxDrainAttempts)
	}
}

// TestDrain_TriesTheNextYardWhenACounterRefuses pins that a refusal is treated
// as LOCAL to the counter. The placement is still fillable next door, and
// abandoning it on the first refusal would leave it claimed and unfilled every
// tick whenever its nearest yard is the unreliable one.
func TestDrain_TriesTheNextYardWhenACounterRefuses(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(10_000_000)
	ports.Yards = &fakeYards{yards: map[string][]string{"X1-AA": {"X1-AA-Y1", "X1-AA-Y2"}}}
	ports.Ships = &fakeShipReader{docked: map[string]string{
		"X1-AA-Y1": "BUYER-1",
		"X1-AA-Y2": "BUYER-2",
	}}
	pur.buyErrAt = map[string]error{"X1-AA-Y1": errors.New("out of stock")}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Bought != 1 {
		t.Fatalf("report says Bought=%d, want 1 — the neighbouring yard could still sell", rep.Bought)
	}
	if len(pur.buys) != 2 || pur.buys[1].yard != "X1-AA-Y2" {
		t.Fatalf("purchase attempts were %v, want a fallback to X1-AA-Y2", pur.buys)
	}
	// Both counters were tried, so both cost budget.
	if rep.Attempts != 2 {
		t.Fatalf("report says Attempts=%d, want 2 (one per counter tried)", rep.Attempts)
	}
}

func TestDrain_TriesTheNextYardWhenACounterCannotBePriced(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(10_000_000)
	ports.Yards = &fakeYards{yards: map[string][]string{"X1-AA": {"X1-AA-Y1", "X1-AA-Y2"}}}
	ports.Ships = &fakeShipReader{docked: map[string]string{
		"X1-AA-Y1": "BUYER-1",
		"X1-AA-Y2": "BUYER-2",
	}}
	pur.quoteErrAt = map[string]error{"X1-AA-Y1": errors.New("listing unreadable")}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 || pur.buys[0].yard != "X1-AA-Y2" {
		t.Fatalf("purchase attempts were %v, want exactly one at the priceable yard", pur.buys)
	}
}

// TestDrain_UnusableYardPresenceCostsNoAttempt pins the other half of the
// foreign-hull fix: when the ships table reports no DRIVABLE hull at any yard,
// the placement is skipped without touching the API at all.
func TestDrain_UnusableYardPresenceCostsNoAttempt(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(10_000_000)
	// A foreign-dedicated hull is filtered out at the port, so presence reads
	// as absent here — no yard is executable.
	ports.Ships = &fakeShipReader{}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.quotes) != 0 {
		t.Fatalf("spent %d live price reads on a placement with no drivable hull, want 0", len(pur.quotes))
	}
	if rep.Attempts != 0 {
		t.Fatalf("report says Attempts=%d, want 0 — no API was touched", rep.Attempts)
	}
	if rep.SkippedNoYard != 1 {
		t.Fatalf("report says SkippedNoYard=%d, want 1", rep.SkippedNoYard)
	}
}

// --- price drift --------------------------------------------------------------

// driftPorts wires two identical affordable placements and a counter that bills
// MORE than it quoted.
func driftPorts() (BuyPorts, *fakeBuyLedger, *fakePurchaser) {
	ports, led, pur := multiSystemPorts()
	pur.price = 20_000
	pur.chargedPrice = 35_000
	ports.Treasury = &fakeTreasury{credits: 10_000_000}
	return ports, led, pur
}

func TestDrain_RecordsTheHullEvenWhenItCostMoreThanQuoted(t *testing.T) {
	ports, led, pur := driftPorts()

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("a price overrun must not fail the tick, got: %v", err)
	}
	if rep.Bought != 1 {
		t.Fatalf("report says Bought=%d, want 1 — an overrun cannot un-buy the hull", rep.Bought)
	}
	if len(pur.buys) != 1 {
		t.Fatalf("kept spending after the market moved against our quotes: %v", pur.buys)
	}
	if !rep.HaltedPriceDrift {
		t.Fatalf("report does not flag HaltedPriceDrift: %+v", rep)
	}
	// The hull must be recorded against the cap, or we own a probe nothing counts.
	bought := led.transitionsTo(SlotStateBought)
	if len(bought) != 1 || bought[0].assignedShip == nil || *bought[0].assignedShip == "" {
		t.Fatalf("the overrun hull was not recorded against its placement: %+v", led.transitions)
	}
}

func TestDrain_NextTickProceedsNormallyAfterAPriceDriftHalt(t *testing.T) {
	ports, _, pur := driftPorts()
	knobs := BuyKnobs{ProbeCap: 100}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, knobs, fixedClock{time.Now()}); err != nil {
		t.Fatalf("first tick returned error: %v", err)
	}
	// The market settles: the counter now bills what it quotes.
	pur.chargedPrice = 0

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, knobs, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("second tick returned error: %v", err)
	}
	if rep.HaltedPriceDrift {
		t.Fatalf("the halt persisted into a tick with fresh quotes: %+v", rep)
	}
	if rep.Bought == 0 {
		t.Fatalf("the drain did not resume after a price-drift halt: %+v", rep)
	}
}

// TestDrain_MissingActualPriceIsNotAFreeHull pins the conservative side of the
// actual-price accounting: a purchase path that reports no price must fall back
// to the quote, never read as a probe that cost nothing.
func TestDrain_MissingActualPriceIsNotAFreeHull(t *testing.T) {
	ports, _, pur := multiSystemPorts()
	pur.price = 20_000
	pur.reportNoPrice = true
	// Affords exactly one probe above the flat 50_000 floor.
	ports.Treasury = &fakeTreasury{credits: 80_000}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 {
		t.Fatalf("bought %d probes on a treasury that affords one, want 1 — an unreported price must not read as free (%v)", len(pur.buys), pur.buys)
	}
	if !rep.FloorHeld {
		t.Fatalf("report does not flag FloorHeld: %+v", rep)
	}
}

// --- the heavy reservation in the probe-buy floor (sp-fwk8z T3) ---------------

// fakeHeavyReserve is the derived hold-back for the next heavy. err ⇒ unreadable ⇒ fail closed:
// buying probes against an unknown reserve could spend the treasury a heavy is accumulating.
type fakeHeavyReserve struct {
	reserve int64
	err     error
	calls   int
}

func (f *fakeHeavyReserve) Reserve(_ context.Context, _ int) (int64, error) {
	f.calls++
	return f.reserve, f.err
}

// THE PARTITION, END TO END. This is the accumulate-then-resume cycle the whole design exists for:
// while a heavy is being saved for, probe buying stands down and treasury builds; the moment the
// reserve clears (heavy bought, or capability closed) the same treasury buys probes again.
//
// Without the reserve term the small continuous spender wins every tick — the probe queue drains
// the surplus below the heavy's threshold before it can ever accumulate, and the heavy is never
// bought. That is the asymmetry §"Asymmetry, stated plainly" describes.
func TestDrain_HeavyReserveRaisesTheFloorThenReleasesIt(t *testing.T) {
	// capexKnobs floor = 750_000; treasury 780_000 − 23_540 probe = 756_460, which clears it.
	// A 10_000 reserve lifts the floor to 760_000 and the same buy no longer clears.
	held, _, heldPur, _ := oneFillPorts(780_000)
	held.HeavyReserve = &fakeHeavyReserve{reserve: 10_000}

	rep, err := DrainBuyQueue(context.Background(), held, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(heldPur.buys) != 0 {
		t.Fatalf("bought %d probes while a heavy reserve was outstanding, want 0 — the partition is not holding", len(heldPur.buys))
	}
	if !rep.FloorHeld {
		t.Fatalf("report does not flag FloorHeld while the reserve holds: %+v", rep)
	}

	// Reserve cleared (heavy landed, or no yard sells one) ⇒ the SAME treasury buys again. This
	// half is what proves the treasury was never the blocker — expansion resumes, it is not
	// permanently taxed.
	released, _, releasedPur, _ := oneFillPorts(780_000)
	released.HeavyReserve = &fakeHeavyReserve{reserve: 0}

	if _, err := DrainBuyQueue(context.Background(), released, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(releasedPur.buys) != 1 {
		t.Fatalf("with the reserve cleared the probe buy must resume, got %d buys", len(releasedPur.buys))
	}
}

// The floor rises by EXACTLY the reserve — pinned at the boundary, one credit either side.
func TestDrain_HeavyReserveRaisesFloorExactly(t *testing.T) {
	// Treasury 780_000 − 23_540 = 756_460 spendable against a 750_000 base floor: 6_460 of slack.
	// A 6_460 reserve is exactly affordable; 6_461 is one credit too much.
	exact, _, exactPur, _ := oneFillPorts(780_000)
	exact.HeavyReserve = &fakeHeavyReserve{reserve: 6_460}
	if _, err := DrainBuyQueue(context.Background(), exact, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(exactPur.buys) != 1 {
		t.Fatalf("a reserve leaving EXACTLY enough must still buy, got %d buys", len(exactPur.buys))
	}

	oneShort, _, shortPur, _ := oneFillPorts(780_000)
	oneShort.HeavyReserve = &fakeHeavyReserve{reserve: 6_461}
	if _, err := DrainBuyQueue(context.Background(), oneShort, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(shortPur.buys) != 0 {
		t.Fatalf("one credit too much reserved must block the buy, got %d buys", len(shortPur.buys))
	}
}

// THE RULED BEHAVIOUR, from the drain's side. A reader that cannot see its inputs answers ZERO
// (see HeavyReservePort.Reserve — a blind read reserves nothing and WARNs), and the drain must then
// buy normally. This is the whole point of the ruling: an unreadable census must not stop probe
// buying, because the fleet autosizer keeps spending on light hulls either way, and halting only
// this half starves expansion on a blind signal.
//
// The treasury is the discriminator, not decoration: 780_000 − 23_540 = 756_460 clears the 750_000
// floor, but the 1,565,500 a reserve would hold does not. So the buy happens BECAUSE the blind read
// reserved zero.
//
// The WARN that makes this state visible belongs to the port and is asserted there
// (TestHeavyReservePort_CensusErrorReservesNothingAndWarns and its yard twin). It is structurally
// unobservable from here — this test substitutes the port wholesale, so Reserve never runs.
func TestDrain_BlindReserveReadsZeroAndBuyingProceeds(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(780_000)
	ports.HeavyReserve = &fakeHeavyReserve{reserve: 0}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("a blind reserve must not halt the drain: %v", err)
	}
	if len(pur.buys) != 1 {
		t.Fatalf("bought %d probes on a zero reserve, want 1 — probe buying must proceed when the reserve cannot be computed", len(pur.buys))
	}
	if rep.HeavyReserve != 0 {
		t.Fatalf("report says HeavyReserve=%d, want 0 — the heartbeat must show nothing held, which is what the port's WARN explains", rep.HeavyReserve)
	}
	if rep.FloorHeld {
		t.Fatalf("a zero reserve must not hold the floor: %+v", rep)
	}
}

// DEFENCE IN DEPTH, deliberately kept. The shipped reader never returns an error — it answers zero
// when blind (the test above) — so this path is unreachable in production today. It is retained
// because HeavyReserveReader is an exported interface with an error in its contract and the port is
// a swappable seam (a nil reader is already a supported wiring), so the drain cannot assume which
// implementation it has. Without this branch the drain would treat an erroring reader's zero as
// authoritative, which is the silent-zero outcome the whole ruling exists to prevent.
//
// The fake's message deliberately does NOT say "census": the census no longer reaches here.
func TestDrain_ErroringReserveReaderStillFailsClosed(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(5_000_000)
	ports.HeavyReserve = &fakeHeavyReserve{err: errors.New("reserve reader refused")}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)}); err == nil {
		t.Fatal("a reader that ERRORS must still fail the drain closed, got nil error")
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes against an erroring reserve reader, want 0", len(pur.buys))
	}
}

// An UNWIRED reader is byte-identical to today: no reserve, no behaviour change. This is what lets
// the sensing engine run before/without the heavy feature rather than stalling on a nil port.
func TestDrain_NilHeavyReserveReaderIsInert(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(780_000)
	ports.HeavyReserve = nil

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 {
		t.Fatalf("an unwired reserve reader must not change behaviour, got %d buys want 1", len(pur.buys))
	}
}

// The heartbeat's buy_heavy_reserve must agree with the autosizer's per-tick gauge on EVERY tick,
// including the ones that return before the floor is ever built. An operator correlating the two
// halves sees them disagree otherwise — and "the two halves disagree" is the exact signal this
// feature's diagnostics exist to make trustworthy.
//
// The probe-cap path is the one that matters most: it is a long-lived steady state, so the
// heartbeat would read 0 for hours on end while a reserve was genuinely outstanding.
func TestDrain_ReportsHeavyReserveOnEveryEarlyReturn(t *testing.T) {
	t.Run("probe cap held", func(t *testing.T) {
		ports, led, pur, _ := oneFillPorts(5_000_000)
		led.owned = int64(capexKnobs.ProbeCap) // at the cap ⇒ returns before the floor is built
		ports.HeavyReserve = &fakeHeavyReserve{reserve: 1_565_500}

		rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
		if err != nil {
			t.Fatalf("DrainBuyQueue returned error: %v", err)
		}
		if !rep.CapHeld {
			t.Fatalf("this test does not exercise the cap-held early return: %+v", rep)
		}
		if len(pur.buys) != 0 {
			t.Fatalf("bought %d probes at the probe cap, want 0", len(pur.buys))
		}
		if rep.HeavyReserve != 1_565_500 {
			t.Fatalf("report says HeavyReserve=%d at the probe cap, want 1565500 — the heartbeat reads 0 while a reserve is outstanding and disagrees with the autosizer gauge", rep.HeavyReserve)
		}
	})

	t.Run("no candidates", func(t *testing.T) {
		ports, led, pur, _ := oneFillPorts(5_000_000)
		led.slots = nil // nothing WANTED or QUEUED ⇒ returns before any money read
		ports.HeavyReserve = &fakeHeavyReserve{reserve: 1_565_500}

		rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
		if err != nil {
			t.Fatalf("DrainBuyQueue returned error: %v", err)
		}
		if led.systemsCalls != 0 {
			t.Fatalf("this test does not exercise the no-candidates early return (verdicts were read %d times)", led.systemsCalls)
		}
		if len(pur.buys) != 0 {
			t.Fatalf("bought %d probes with nothing queued, want 0", len(pur.buys))
		}
		if rep.HeavyReserve != 1_565_500 {
			t.Fatalf("report says HeavyReserve=%d on an empty queue, want 1565500", rep.HeavyReserve)
		}
	})
}

// ONE DEFINITION. The value sensing holds back must be the value common.HeavyReserve computes for
// the same facts.
//
// TWO TESTS PROTECT THAT, and neither is a compile-time guard — nothing stops a second copy of the
// predicate from COMPILING, because HeavyReserveInputs and every field on it are exported:
//
//   - TestHeavyReserveLockstep (internal/application/common) pins the ARITHMETIC, so a divergent
//     second copy fails the suite rather than the economics;
//   - TestDrain_ReserveMatchesTheSharedPredicate — this test — pins the CALLER, so nobody can
//     massage the number on its way into the floor.
func TestDrain_ReserveMatchesTheSharedPredicate(t *testing.T) {
	in := common.HeavyReserveInputs{
		CapabilityOpen:     true,
		HeaviesOwned:       1,
		HeavyCap:           5,
		CheapestKnownPrice: 1_565_500,
	}
	want := common.HeavyReserve(in)
	if want != 1_565_500 {
		t.Fatalf("shared predicate returned %d, want the cheapest ask 1565500", want)
	}

	// The floor the queue builds with that reserve must equal the floor built by adding the
	// shared predicate's own answer to the capex term — no scaling, no rounding, no second rule.
	got := domainSensing.ProbeBuyFloor(common.ImmutableReserveFloor, capexKnobs.CapexReserve+want, 0, 0)
	expected := domainSensing.ProbeBuyFloor(common.ImmutableReserveFloor, capexKnobs.CapexReserve+common.HeavyReserve(in), 0, 0)
	if got != expected {
		t.Fatalf("floor built from the sensing path = %d, from the shared predicate = %d — the two have diverged", got, expected)
	}
}

// --- the silent refusal (sp-l50w1) -------------------------------------------
//
// THE LIVE SHAPE THESE FIXTURES COPY. On 2026-07-28 the drain logged
// "bought 0 reused 0 queued 0 (6 attempts)" every tick for 37 consecutive ticks
// with no reason recorded anywhere. Three claimed placements each tried the same
// two yards; every one of the six refused inside Purchaser.Buy and was swallowed
// by a bare `continue`. An operator could not tell "out of stock" from "the hull
// is not docked" from "the API is down", and the whole attempt budget was spent
// re-asking two counters the same question three times.
//
// The fixtures below deliberately give the two yards DIFFERENT failure shapes —
// one refuses at the quote, one at the buy — because the repo's recurring defect
// is a fixture that makes two paths indistinguishable. A test whose yards fail
// identically would pass against code that cannot tell them apart.

// refusingYardPorts builds the live shape: two placements in one IN_SCOPE system
// and two yards, each with a purchasing hull of ours standing on it. Which yard
// refuses, and at which step, is left to the caller.
func refusingYardPorts(treasury int64) (BuyPorts, *fakeBuyLedger, *fakePurchaser) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-AA-M1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateWanted, DepthCredits: 900},
			{Waypoint: "X1-AA-M2", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateWanted, DepthCredits: 900},
		},
		systems: []ScreenedSystem{{System: "X1-AA", DepthCredits: 900}},
	}
	pur := &fakePurchaser{
		price:      20_012,
		quoteErrAt: map[string]error{},
		buyErrAt:   map[string]error{},
	}
	return BuyPorts{
		Treasury:   &fakeTreasury{credits: treasury},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards:      &fakeYards{yards: map[string][]string{"X1-AA": {"X1-AA-ORBIT", "X1-AA-DOCK"}}},
		Ships: &fakeShipReader{docked: map[string]string{
			"X1-AA-ORBIT": "PROBE-ORBITING",
			"X1-AA-DOCK":  "PROBE-DOCKED",
		}},
		Fleet: &fakeFleet{},
	}, led, pur
}

// wideKnobs put the floor far below the treasury, so nothing here is ever a
// money-guard outcome dressed up as a refusal.
var wideKnobs = BuyKnobs{ProbeCap: 100, CapexReserve: 0, KMilli: 0}

func TestDrain_RecordsWhyEachYardRefused(t *testing.T) {
	ports, _, pur := refusingYardPorts(1_321_274)
	// Two DIFFERENT failures, one per step, so a report that collapses them is caught.
	pur.quoteErrAt["X1-AA-ORBIT"] = errors.New("shipyard at X1-AA-ORBIT has no priced SHIP_PROBE listing")
	pur.buyErrAt["X1-AA-DOCK"] = errors.New("sensing probe buyer PROBE-DOCKED claim failed")

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, wideKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Bought != 0 {
		t.Fatalf("report says Bought=%d, want 0 (both yards refuse)", rep.Bought)
	}
	if len(rep.Refusals) == 0 {
		t.Fatalf("drain refused every yard and recorded NO reason: %+v", rep)
	}

	var sawQuote, sawBuy bool
	for _, r := range rep.Refusals {
		switch r.Step {
		case BuyStepQuote:
			sawQuote = true
			if r.Yard != "X1-AA-ORBIT" {
				t.Fatalf("quote refusal names yard %q, want X1-AA-ORBIT", r.Yard)
			}
			if r.Reason == "" {
				t.Fatalf("quote refusal at %s carries no reason an operator can read", r.Yard)
			}
		case BuyStepBuy:
			sawBuy = true
			if r.Yard != "X1-AA-DOCK" {
				t.Fatalf("buy refusal names yard %q, want X1-AA-DOCK", r.Yard)
			}
			// The buyer is the whole point of a buy-step refusal: it is what
			// tells "this counter is out of stock" from "this hull cannot buy".
			if r.Buyer != "PROBE-DOCKED" {
				t.Fatalf("buy refusal names buyer %q, want PROBE-DOCKED", r.Buyer)
			}
			if r.Reason == "" {
				t.Fatalf("buy refusal at %s carries no reason an operator can read", r.Yard)
			}
		default:
			t.Fatalf("refusal carries unknown step %q", r.Step)
		}
	}
	if !sawQuote {
		t.Fatalf("the unpriceable yard was not recorded as a QUOTE refusal: %+v", rep.Refusals)
	}
	if !sawBuy {
		t.Fatalf("the refusing counter was not recorded as a BUY refusal: %+v", rep.Refusals)
	}
}

func TestDrain_ARefusingYardIsNotReAskedForEveryPlacement(t *testing.T) {
	// Both yards refuse, exactly as they do live. Two placements are queued.
	// Re-asking each counter once per placement is what burned the whole budget.
	ports, _, pur := refusingYardPorts(1_321_274)
	pur.buyErrAt["X1-AA-ORBIT"] = errors.New("purchasing hull is not docked")
	pur.buyErrAt["X1-AA-DOCK"] = errors.New("purchasing hull claim failed")

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, wideKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 2 {
		t.Fatalf("asked the two refusing counters %d times, want 2 (once each): %v", len(pur.buys), pur.buys)
	}
	if rep.Attempts > 2 {
		t.Fatalf("a refusal already known this tick still cost an attempt: Attempts=%d, want <=2", rep.Attempts)
	}
}

func TestDrain_ARefusingYardDoesNotStarveAWorkingOne(t *testing.T) {
	// The yard listed FIRST refuses; the one behind it works. The placement must
	// still be filled, and the working counter must be the one that sold it.
	ports, _, pur := refusingYardPorts(1_321_274)
	pur.buyErrAt["X1-AA-ORBIT"] = errors.New("purchasing hull is not docked")

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, wideKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Bought == 0 {
		t.Fatalf("a working yard sat unused behind a refusing one: %+v", rep)
	}
	for _, b := range pur.buys {
		if b.yard == "X1-AA-DOCK" && b.ship != "PROBE-DOCKED" {
			t.Fatalf("bought at %s through %q, want PROBE-DOCKED", b.yard, b.ship)
		}
	}
	// The refusal still has to be legible even on a tick that ended in a purchase.
	if len(rep.Refusals) == 0 {
		t.Fatalf("the refusing yard left no trace on a tick that also bought: %+v", rep)
	}
}

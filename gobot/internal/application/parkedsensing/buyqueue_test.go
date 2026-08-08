package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

	// attempts records every MarkPlacementAttempt in call order, and attemptSeq
	// the order slots were LAST charged a turn — a monotonic counter rather than a
	// clock, because a real clock's granularity would tie slots stamped inside one
	// tick and the tie is exactly what the ordering has to resolve. A slot absent
	// from the map has never been attempted.
	attempts    []attemptCall
	attemptSeq  map[string]int
	attemptTick int
	attemptErr  error
}

type attemptCall struct{ waypoint, kind string }

func attemptKey(waypoint, kind string) string { return waypoint + "/" + kind }

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

// PlacementWorklist honours the port's documented order — never-attempted slots
// first, then least recently attempted, then waypoint — so the app-layer tests
// exercise the rotation the real ledger provides in SQL rather than assuming it.
// Without that, every multi-tick fairness assertion here would be vacuous.
func (f *fakeBuyLedger) PlacementWorklist(ctx context.Context, playerID int, states ...string) ([]QueuedSlot, error) {
	out, err := f.SlotsByState(ctx, playerID, states...)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, aok := f.attemptSeq[attemptKey(out[i].Waypoint, out[i].Kind)]
		bi, bok := f.attemptSeq[attemptKey(out[j].Waypoint, out[j].Kind)]
		if aok != bok {
			return !aok // never attempted sorts ahead of ever attempted
		}
		if aok && ai != bi {
			return ai < bi
		}
		return out[i].Waypoint < out[j].Waypoint
	})
	return out, nil
}

func (f *fakeBuyLedger) MarkPlacementAttempt(_ context.Context, _ int, waypoint, kind string) error {
	f.attempts = append(f.attempts, attemptCall{waypoint, kind})
	if f.attemptErr != nil {
		return f.attemptErr
	}
	if f.attemptSeq == nil {
		f.attemptSeq = map[string]int{}
	}
	f.attemptTick++
	f.attemptSeq[attemptKey(waypoint, kind)] = f.attemptTick
	return nil
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
func (f *fakeBuyLedger) TransitionSlot(_ context.Context, _ int, tr SlotTransition, set SlotFields) error {
	waypoint, kind, from, to := tr.Waypoint, tr.Kind, tr.From, tr.To
	f.transitions = append(f.transitions, transitionCall{waypoint, from, to, set.AssignedShip, set.PurchaseYard})
	if err := f.transitionErr[waypoint+"→"+to]; err != nil {
		return err
	}
	for i := range f.slots {
		// Matched on the KIND too, as the real ledger is: a waypoint can
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
	docked map[string]string // waypoint → docked probe symbol
	// lent maps a waypoint to a NON-PROBE hull of ours standing at it — the
	// cold-start borrow (counterstaff.go). Kept apart from `docked` on purpose: the
	// two reads answer different questions and buyerAt asks them in order, so a fake
	// that merged them could not witness the preference.
	lent      map[string]string
	positions map[string]ShipPos
	dockedErr error
	lentErr   error
	atErr     error
}

func (f *fakeShipReader) DockedProbeAt(_ context.Context, _ int, waypoint string) (string, bool, error) {
	if f.dockedErr != nil {
		return "", false, f.dockedErr
	}
	s, ok := f.docked[waypoint]
	return s, ok, nil
}

func (f *fakeShipReader) DockedBuyerAt(_ context.Context, _ int, waypoint string) (string, bool, error) {
	if f.lentErr != nil {
		// Adversarial: a usable buyer alongside the error, so a caller that ignores
		// the error engages a hull it cannot prove is claimable.
		return "HULL-GHOST", true, f.lentErr
	}
	s, ok := f.lent[waypoint]
	return s, ok, nil
}

func (f *fakeShipReader) LendableHulls(_ context.Context, _ int, _ int) ([]LendableHull, error) {
	return nil, nil
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
var capexKnobs = BuyKnobs{SpendEnabled: true, ProbeCap: 100, CapexReserve: 100_000, KMilli: 2000}

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

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
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

func TestDrain_AlreadyParkedProbesPullARichSystemUPTheOrder(t *testing.T) {
	// X1-DEEP already holds two parked probes; X1-MID holds none. The fleet is
	// STANDING IN X1-DEEP, so X1-DEEP is finished before X1-MID is entered.
	//
	// THIS TEST PREVIOUSLY ASSERTED THE EXACT OPPOSITE (TestDrain_
	// AlreadyParkedProbesPushARichSystemDownTheOrder, want MID then DEEP): parked
	// probes PUSHED a system DOWN, which is the coverage-ascending rule sp-xfdep
	// replaced. Held live it meant no system's second probe was ever bought — 118
	// hulls over 104 systems at 1.1 each, 804 placements outstanding behind 17,337
	// in systems no probe had ever visited. It is inverted deliberately, not broken.
	//
	// The fixture is unchanged, so the two orders are directly comparable: depth
	// and ledger order both still point at X1-DEEP, and only the saturation tier
	// decides. A change that merely made depth the top key would pass this and fail
	// TestDrain_UnenteredSystemsKeepTheirCoverageOrderingAmongThemselves.
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

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	want := []string{"X1-DEEP-Y1", "X1-MID-Y1"}
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

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err == nil {
		t.Fatal("an unreadable coverage read must stop the drain, not order on a blind zero")
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %v against coverage we could not read", pur.buys)
	}
}

func TestDrain_SkipsPlacementsInSystemsNotInScope(t *testing.T) {
	ports, led, pur := multiSystemPorts()
	led.systems = []ScreenedSystem{{System: "X1-DEEP", DepthCredits: 5_000}}

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
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

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 4}, fixedClock{time.Now()})
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

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 4}, fixedClock{time.Now()})
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

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
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

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
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

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 || pur.buys[0].ship != "PROBE-ON-YARD" {
		t.Fatalf("did not buy through the MARKET-slotted probe standing on the yard: %v", pur.buys)
	}
}

func TestDrain_FailedPurchaseLeavesSlotQueuedForRetry(t *testing.T) {
	ports, led, pur, _ := oneFillPorts(10_000_000)
	pur.buyErr = errors.New("shipyard refused")

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
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

	_, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
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

	_, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
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

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
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

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 || pur.buys[0].yard != "X1-AA-Y2" {
		t.Fatalf("abandoned a still-good recorded yard: %v", pur.buys)
	}
}

func TestDrain_SkipsPlacementWhoseProbeCannotBePriced(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(10_000_000)
	pur.quoteErr = errors.New("shipyard listing unreadable")

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	_, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	_, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	_, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	_, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
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

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
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

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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
	knobs := BuyKnobs{SpendEnabled: true, ProbeCap: 100}

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

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()})
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

// --- the wave gate: the drain's half of the ONE predicate ---------------------

// waveReader is the drain's whole view of the regime — the ANSWER, never the inputs to it: a fake
// carrying WaveInputs would be a second assembly of the predicate. err ⇒ unreadable ⇒ fail closed.
type waveReader struct {
	wave   common.Wave
	reason common.WaveProbeReason
	target common.HeavyReserveTarget
	err    error
	calls  int
}

func (f *waveReader) Wave(_ context.Context, _ int) (common.Wave, common.WaveProbeReason, common.HeavyReserveTarget, error) {
	f.calls++
	return f.wave, f.reason, f.target, f.err
}

// probeWave carries a REAL reason: a blank one would let a test pass against a reader that
// answered nothing.
func probeWave(target common.HeavyReserveTarget) *waveReader {
	return &waveReader{wave: common.WaveProbe, reason: common.WaveProbeReasonUnreachable, target: target}
}

func heavyWave(target common.HeavyReserveTarget) *waveReader {
	return &waveReader{wave: common.WaveHeavy, target: target}
}

// THE WAVE GATE. On HEAVY the drain buys no probe: a heavy is a lump and probes are a trickle, and
// without pausing the trickle the treasury never reaches the lump. The fixture is the one
// TestDrain_ExpansionSwitchOnStillBuysAtDefaultFloors proves BUYS, so "bought 0" is evidence.
func TestDrain_HeavyWaveBuysNoProbe(t *testing.T) {
	ports, led, pur, _ := oneFillPorts(780_000)
	ports.Wave = heavyWave(1_916_613)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Bought != 0 || len(pur.buys) != 0 {
		t.Fatalf("bought %d probes (report Bought=%d) on a HEAVY wave — the trickle is not being paused, so the treasury never reaches the lump", len(pur.buys), rep.Bought)
	}
	if len(pur.quotes) != 0 {
		t.Fatalf("read %d live shipyard prices on a HEAVY wave, want 0: a tick that may not buy must not price anything either", len(pur.quotes))
	}
	if rep.Wave != common.WaveHeavy {
		t.Fatalf("the report must carry the wave, got %q", rep.Wave)
	}
	if !rep.SpendingPaused {
		t.Fatalf("report does not say WHY nothing was bought: %+v", rep)
	}
	if got := led.transitionsTo(SlotStateQueued); len(got) != 0 {
		t.Fatalf("a HEAVY wave claimed a slot for a purchase that cannot happen: %+v", got)
	}
}

// THE FREE HALF STILL RUNS. Re-tasking an idle spare costs zero credits and zero API calls, so
// stopping it would leave markets we already own hulls for unwatched, for nothing.
func TestDrain_HeavyWaveStillReusesAParkedSpare(t *testing.T) {
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
		Wave:       heavyWave(1_916_613),
	}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Reused != 1 {
		t.Fatalf("report says Reused=%d, want 1 — a wave that also stops re-tasking hulls we already own saves nothing and blinds the markets we bought them for: %+v", rep.Reused, rep)
	}
	if rep.Bought != 0 || len(pur.buys) != 0 {
		t.Fatalf("bought %d probes on a HEAVY wave: %v", len(pur.buys), pur.buys)
	}
}

// FREE WORK SURVIVES, part two: the foothold. It has the strongest claim to being cut with the
// purchases — it exists to make a system able to BUY — but it is still free, so it stays.
func TestDrain_HeavyWaveStillFillsAFoothold(t *testing.T) {
	ports, led, _ := footholdPorts(liveManned())
	ports.Wave = heavyWave(1_916_613)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 40}, fixedClock{})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Footholds == 0 {
		t.Fatalf("no foothold established on a HEAVY wave; report %+v — the foothold spends nothing, so the pause must not reach it", rep)
	}
	if rep.Bought != 0 {
		t.Fatalf("bought %d probes on a HEAVY wave", rep.Bought)
	}
	target := slotAt(t, led, "X1-GF41-Y1", SlotKindSpare)
	if target.State != SlotStateInTransit || target.AssignedShip == "" {
		t.Fatalf("foothold target not claimed on a HEAVY wave: %+v", target)
	}
}

// A HEAVY WAVE COSTS NO MONEY READ. Written by BREAKING the reads: it is the one assertion that
// fails if the gate moves BELOW them, where it would still buy nothing, still report
// SpendingPaused, and pass every other test here while spending an API call per tick forever.
func TestDrain_HeavyWaveReadsNeitherTreasuryNorFleetCount(t *testing.T) {
	ports, led, pur, _ := oneFillPorts(780_000)
	treasury := &fakeTreasury{credits: 999_999_999, err: errors.New("api down")}
	ports.Treasury = treasury
	led.ownedErr = errors.New("db down")
	ports.Wave = heavyWave(1_916_613)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Now()})
	if err != nil {
		t.Fatalf("a HEAVY-wave drain surfaced a money-read error it had no reason to take: %v", err)
	}
	if treasury.calls != 0 {
		t.Fatalf("HEAVY-wave drain called LiveCredits %d times, want 0 (one live API read per tick, forever, to price nothing)", treasury.calls)
	}
	if rep.Bought != 0 || len(pur.buys) != 0 {
		t.Fatalf("bought %d probes on a HEAVY wave: %v", len(pur.buys), pur.buys)
	}
}

// THE WAVE READ ITSELF COSTS NO API CALL: every read behind the port is a LOCAL DB query, which is
// what lets it sit ahead of the cheapest-first gate order and would break if the regime were
// re-derived from a live balance. Asserted on the empty-queue path, which returns before any gate,
// so a treasury call there could only have come from deriving the wave.
func TestDrain_WaveReadCostsNoTreasuryCall(t *testing.T) {
	ports, led, _, _ := oneFillPorts(780_000)
	led.slots = nil // nothing queued ⇒ the tick returns before every gate
	treasury := &fakeTreasury{credits: 999_999_999}
	ports.Treasury = treasury
	wave := probeWave(1_916_613)
	ports.Wave = wave

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if wave.calls != 1 {
		t.Fatalf("the wave was read %d times, want exactly 1 — this test cannot see the cost of a read that did not happen", wave.calls)
	}
	if treasury.calls != 0 {
		t.Fatalf("deriving the wave cost %d treasury reads, want 0: a tick with nothing to buy must not cost an API call", treasury.calls)
	}
	if rep.Wave != common.WaveProbe {
		t.Fatalf("the wave must be published on the empty-queue path too, got %q", rep.Wave)
	}
}

// ON THE PROBE WAVE THE FLOOR NO LONGER CARRIES THE HEAVY HOLD: a binary gate and a ramp are two
// mechanisms doing one job. The fixture asserts the hold is LIVE before relying on its absence, so
// the test cannot pass against a term that merely happened to be zero.
func TestDrain_ProbeWaveFloorExcludesTheHeavyHold(t *testing.T) {
	const reachableAsk = common.HeavyReserveTarget(1_000_000)
	if hold := reachableAsk.HoldAt(780_000); hold <= 0 {
		t.Fatalf("this fixture no longer exercises a live hold (HoldAt=%d): the test would pass against a floor that still carried the term", hold)
	}

	ports, _, pur, _ := oneFillPorts(780_000)
	ports.Wave = probeWave(reachableAsk)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Bought != 1 || len(pur.buys) != 1 {
		t.Fatalf("bought %d probes (report Bought=%d) on a PROBE wave with headroom, want 1 — the heavy hold is still reaching the floor", len(pur.buys), rep.Bought)
	}
	if rep.FloorHeld {
		t.Fatalf("FloorHeld on a PROBE wave the treasury clears: %+v", rep)
	}
	if rep.HeavyReserveTarget != reachableAsk {
		t.Fatalf("report says target %d, want %d — the ask must still be published, or an operator cannot see what a HEAVY wave would be for", rep.HeavyReserveTarget, reachableAsk)
	}
}

// THE FLOOR IS EXACTLY THE UNCHANGED ONE, pinned one credit either side with a large ask
// outstanding: a surviving term at ANY scale moves this boundary, which "it still buys" cannot see.
func TestDrain_ProbeWaveFloorIsTheUnchangedFloorAtTheBoundary(t *testing.T) {
	const ask = common.HeavyReserveTarget(1_000_000)
	// The documented floor for capexKnobs: 50_000 immutable + 100_000 capex + 2h of a 300_000/h
	// cargo runway. Derived from the shared function rather than restated, so a floor change fails
	// here loudly instead of silently re-aiming the boundary.
	floor := domainSensing.ProbeBuyFloor(common.ImmutableReserveFloor, capexKnobs.CapexReserve, domainSensing.CargoSpendPerHour(300_000), capexKnobs.KMilli)
	const probePrice = int64(23_540)

	exact, _, exactPur, _ := oneFillPorts(floor + probePrice)
	exact.Wave = probeWave(ask)
	if _, err := DrainBuyQueue(context.Background(), exact, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(exactPur.buys) != 1 {
		t.Fatalf("a treasury leaving EXACTLY the floor must still buy, got %d buys — a heavy term is still in the floor", len(exactPur.buys))
	}

	oneShort, _, shortPur, _ := oneFillPorts(floor + probePrice - 1)
	oneShort.Wave = probeWave(ask)
	if _, err := DrainBuyQueue(context.Background(), oneShort, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(shortPur.buys) != 0 {
		t.Fatalf("one credit below the floor must block the buy, got %d buys — the floor has been lowered, not merely relieved of the heavy term", len(shortPur.buys))
	}
}

// THE IMMUTABLE FLOOR STILL BINDS on the PROBE wave: removing the heavy hold removes ONE term and
// does not touch the guard underneath it (RULINGS #4). The knobs are stripped to zero so this is
// the immutable floor alone rather than the compound one.
func TestDrain_ProbeWaveStillHoldsTheImmutableFloor(t *testing.T) {
	bare := BuyKnobs{SpendEnabled: true, ProbeCap: 100}
	const probePrice = int64(23_540)

	below, _, belowPur, _ := oneFillPorts(common.ImmutableReserveFloor + probePrice - 1)
	below.Wave = probeWave(0)
	rep, err := DrainBuyQueue(context.Background(), below, testPlayerID, bare, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(belowPur.buys) != 0 || !rep.FloorHeld {
		t.Fatalf("the immutable floor must still bind, bought=%d floorHeld=%v", len(belowPur.buys), rep.FloorHeld)
	}

	at, _, atPur, _ := oneFillPorts(common.ImmutableReserveFloor + probePrice)
	at.Wave = probeWave(0)
	if _, err := DrainBuyQueue(context.Background(), at, testPlayerID, bare, fixedClock{time.Unix(1_700_000_000, 0)}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(atPur.buys) != 1 {
		t.Fatalf("a balance leaving EXACTLY the immutable floor must buy, got %d — this half is what makes the refusal above evidence", len(atPur.buys))
	}
}

// THE FLOOR CAN NEVER FALL BELOW THE IMMUTABLE RESERVE, whatever the knobs say — the property that
// makes dropping an addend safe. Asserted against the exact call the drain makes, including the
// adversarial negatives a malformed config could produce.
func TestDrain_FloorNeverFallsBelowTheImmutableReserve(t *testing.T) {
	for _, capex := range []int64{-1_000_000, -1, 0, 900_000} {
		for _, cargo := range []int64{-1_000_000, 0, 300_000} {
			for _, kMilli := range []int{-5000, 0, 2000} {
				got := domainSensing.ProbeBuyFloor(common.ImmutableReserveFloor, capex, domainSensing.CargoSpendPerHour(cargo), kMilli)
				if got < common.ImmutableReserveFloor {
					t.Fatalf("floor %d is BELOW the immutable reserve %d at capex=%d cargo=%d k=%d", got, common.ImmutableReserveFloor, capex, cargo, kMilli)
				}
			}
		}
	}
}

// THE ANTI-DEADLOCK REGRESSION at the drain: a fleet whose PEAK treasury cannot reach the ask keeps
// buying probes behind its own unchanged floor. The SWEEP is the point — under a live-treasury
// regime the answer changed with where in the trade cycle the tick landed, so a single sample
// proved nothing; the regime is now a property of the fleet, so every balance must agree.
func TestDrain_UnreachableHeavyDoesNotPauseProbes(t *testing.T) {
	for live := int64(119_000); live <= 1_500_000; live += 137_119 {
		ports, _, pur, _ := oneFillPorts(live)
		ports.Wave = probeWave(1_916_613)

		rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
		if err != nil {
			t.Fatalf("live balance %d: %v", live, err)
		}
		if rep.Wave != common.WaveProbe {
			t.Fatalf("an unreachable heavy must leave the drain on PROBE at live balance %d, got %q", live, rep.Wave)
		}
		if rep.SpendingPaused {
			t.Fatalf("probe buying was paused for an unreachable heavy at live balance %d — this is the deadlock the reachability clause exists to prevent", live)
		}
		// The floor still decides whether this particular balance can afford a probe; what must not
		// happen is the reservation deciding it.
		if wantBuy := live-23_540 >= 750_000; wantBuy != (len(pur.buys) == 1) {
			t.Fatalf("at live balance %d the unchanged floor says buy=%v but the drain made %d buys", live, wantBuy, len(pur.buys))
		}
	}
}

// THE OPERATOR SPEND SWITCH IS UNCHANGED and still independent: off buys nothing whatever the
// regime says. Paired with TestDrain_HeavyWaveBuysNoProbe, it proves the gate is a CONJUNCTION.
func TestDrain_SpendSwitchStillBindsOnTheProbeWave(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(780_000)
	ports.Wave = probeWave(0)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, pausedCapexKnobs(), fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if rep.Bought != 0 || len(pur.buys) != 0 {
		t.Fatalf("bought %d probes with the operator switch off on a PROBE wave, want 0", len(pur.buys))
	}
	if !rep.SpendingPaused {
		t.Fatalf("the operator switch must still bind: %+v", rep)
	}
}

// FAIL-CLOSED ON AN UNREADABLE WAVE, and closed means "buy nothing this tick": the reader is a
// swappable seam, so the drain must not treat an erroring implementation's zero as authoritative.
func TestDrain_UnreadableWaveBuysNothing(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(5_000_000)
	ports.Wave = &waveReader{err: errors.New("wave unreadable")}

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err == nil {
		t.Fatal("an unreadable wave must abort the tick, got nil error")
	}
	if rep.Bought != 0 || len(pur.buys) != 0 {
		t.Fatalf("bought %d probes on an unreadable wave, want 0", len(pur.buys))
	}
	if rep.Wave != "" {
		t.Fatalf("a tick that derived no regime must publish none, got %q — an invented PROBE would report a release the drain did not make", rep.Wave)
	}
}

// AN UNWIRED READER IS THE PROBE WAVE, never HEAVY: a deployment with no heavy buyer has nothing to
// save for, and an omission that paused probe buying forever is the deadlock this gate avoids.
func TestDrain_NilWaveReaderIsTheProbeWave(t *testing.T) {
	ports, _, pur, _ := oneFillPorts(780_000)
	ports.Wave = nil

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 1 {
		t.Fatalf("an unwired wave reader must not change behaviour, got %d buys want 1", len(pur.buys))
	}
	if rep.Wave != common.WaveProbe || rep.WaveProbeReason != common.WaveProbeReasonGrowthDisabled {
		t.Fatalf("an unwired reader must report the PROBE wave with the no-buyer reason, got %q/%q", rep.Wave, rep.WaveProbeReason)
	}
}

// THE WAVE IS PUBLISHED ON EVERY RETURN PATH THAT DERIVED ONE, including those that return before a
// floor is built: two series disagreeing merely because a tick took a short path are
// indistinguishable from the split-brain. The probe-cap path matters most — it is a long-lived
// steady state, so the heartbeat would read a blank regime for hours.
func TestDrain_ReportsTheWaveOnEveryEarlyReturn(t *testing.T) {
	t.Run("probe cap held", func(t *testing.T) {
		ports, led, _, _ := oneFillPorts(5_000_000)
		led.owned = int64(capexKnobs.ProbeCap) // at the cap ⇒ returns before the floor is built
		ports.Wave = probeWave(1_565_500)

		rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
		if err != nil {
			t.Fatalf("DrainBuyQueue returned error: %v", err)
		}
		if !rep.CapHeld {
			t.Fatalf("this test does not exercise the cap-held early return: %+v", rep)
		}
		if rep.Wave != common.WaveProbe || rep.HeavyReserveTarget != 1_565_500 {
			t.Fatalf("at the probe cap the report says wave=%q target=%d, want probe/1565500", rep.Wave, rep.HeavyReserveTarget)
		}
	})

	t.Run("no candidates", func(t *testing.T) {
		ports, led, _, _ := oneFillPorts(5_000_000)
		led.slots = nil // nothing WANTED or QUEUED ⇒ returns before any money read
		ports.Wave = heavyWave(1_565_500)

		rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)})
		if err != nil {
			t.Fatalf("DrainBuyQueue returned error: %v", err)
		}
		if led.systemsCalls != 0 {
			t.Fatalf("this test does not exercise the no-candidates early return (verdicts were read %d times)", led.systemsCalls)
		}
		if rep.Wave != common.WaveHeavy || rep.HeavyReserveTarget != 1_565_500 {
			t.Fatalf("on an empty queue the report says wave=%q target=%d, want heavy/1565500", rep.Wave, rep.HeavyReserveTarget)
		}
	})
}

// THE DRAIN NEVER RE-DERIVES THE REGIME: a second read could see a different regime within one tick.
func TestDrain_ReadsTheWaveExactlyOncePerTick(t *testing.T) {
	ports, _, _, _ := oneFillPorts(780_000)
	wave := probeWave(1_916_613)
	ports.Wave = wave

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, capexKnobs, fixedClock{time.Unix(1_700_000_000, 0)}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if wave.calls != 1 {
		t.Fatalf("the wave was read %d times in one tick, want exactly 1", wave.calls)
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
var wideKnobs = BuyKnobs{SpendEnabled: true, ProbeCap: 100, CapexReserve: 0, KMilli: 0}

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

// --- the persisted probe-listing memo (stop re-quoting yards that sell no probe) ---

// fakeListingMemo answers what a PREVIOUS quote persisted about a yard.
//
// On error it claims the yard sells NO probe, which is the adversarial value: a
// caller that read the answer and ignored the error would SKIP the yard, so only a
// genuinely fail-OPEN caller still quotes it.
type fakeListingMemo struct {
	sells     map[string]bool
	scannedAt map[string]time.Time
	err       error
	asked     []string
	// unknownStamp is the timestamp returned ALONGSIDE known=false. Default zero
	// time, which is what a real adapter returns — but a test can set it RECENT to
	// prove the caller honours `known` on its own rather than getting the right
	// answer by accident, because a zero timestamp always reads as infinitely
	// stale and would mask a caller that ignored the flag entirely.
	unknownStamp time.Time
}

func (f *fakeListingMemo) LastListingScan(_ context.Context, _ int, waypoint string) (bool, time.Time, bool, error) {
	f.asked = append(f.asked, waypoint)
	if f.err != nil {
		return false, time.Now(), true, f.err
	}
	at, known := f.scannedAt[waypoint]
	if !known {
		return false, f.unknownStamp, false, nil
	}
	return f.sells[waypoint], at, true, nil
}

// oneYardPorts wires a single placement whose system has ONE yard, with a hull
// standing at it — so the only thing deciding whether a quote call happens is the
// listing memo.
func oneYardPorts() (BuyPorts, *fakeBuyLedger, *fakePurchaser, *fakeListingMemo) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-AA-M1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateWanted},
		},
		systems: []ScreenedSystem{{System: "X1-AA", DepthCredits: 900}},
	}
	pur := &fakePurchaser{price: 1_000}
	memo := &fakeListingMemo{sells: map[string]bool{}, scannedAt: map[string]time.Time{}}
	return BuyPorts{
		Treasury:    &fakeTreasury{credits: 10_000_000},
		CargoSpend:  &fakeCargoSpend{},
		Purchaser:   pur,
		Ledger:      led,
		Yards:       &fakeYards{yards: map[string][]string{"X1-AA": {"X1-AA-Y1"}}},
		Ships:       &fakeShipReader{docked: map[string]string{"X1-AA-Y1": "BUYER-AA"}},
		Fleet:       &fakeFleet{},
		ListingMemo: memo,
	}, led, pur, memo
}

func TestDrain_YardKnownNotToSellProbesIsNeverQuotedAgain(t *testing.T) {
	// THE POINT OF THE WHOLE CHANGE, and it is asserted on the CALL COUNT rather
	// than the outcome: this fixture's yard sells no probe either way, so "skipped"
	// and "quoted, then refused" produce an identical zero-purchase report. Only the
	// quote count tells them apart.
	now := time.Now()
	ports, _, pur, memo := oneYardPorts()
	memo.scannedAt["X1-AA-Y1"] = now.Add(-time.Minute) // read a minute ago
	memo.sells["X1-AA-Y1"] = false                     // and it sells no probe

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{now}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.quotes) != 0 {
		t.Fatalf("quoted %v — a yard we have already learned sells no probe must cost no API call", pur.quotes)
	}
}

func TestDrain_UnknownYardIsQuotedOnceSoWeCanLearn(t *testing.T) {
	// Fail OPEN on unknown: a yard nothing has ever read must still be asked, or the
	// memo could never be populated and the fleet would freeze its own knowledge.
	now := time.Now()
	ports, _, pur, _ := oneYardPorts() // memo knows nothing about the yard

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{now}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.quotes) != 1 || pur.quotes[0] != "X1-AA-Y1" {
		t.Fatalf("quotes = %v, want exactly one quote at the unread yard", pur.quotes)
	}
}

func TestDrain_AnUnreadYardIsQuotedEvenWhenItsTimestampLooksFresh(t *testing.T) {
	// `known` must be honoured ON ITS OWN. The obvious fixture — an unread yard
	// carrying the ZERO timestamp — cannot prove that: zero reads as infinitely
	// stale, so the TTL check quotes the yard even for a caller that ignored the
	// flag entirely. Handing back a RECENT timestamp with known=false removes that
	// second reason, leaving the flag as the only thing that can produce a quote.
	//
	// This matters because "absent means ask once" is the property that lets the
	// memo ever be populated. A caller that read absence as a negative would write
	// off every yard nothing had happened to read yet, permanently.
	now := time.Now()
	ports, _, pur, memo := oneYardPorts()
	memo.unknownStamp = now.Add(-time.Minute)

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{now}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.quotes) != 1 {
		t.Fatalf("quotes = %v, want the never-read yard quoted — an absent reading is not a negative one", pur.quotes)
	}
}

func TestDrain_KnownNegativeYardIsRequotedOnceTheReadGoesStale(t *testing.T) {
	// A permanent write-off would be wrong — shipyards restock. The stored negative
	// is trusted only for probeListingMemoTTL.
	now := time.Now()
	ports, _, pur, memo := oneYardPorts()
	memo.scannedAt["X1-AA-Y1"] = now.Add(-probeListingMemoTTL - time.Minute)
	memo.sells["X1-AA-Y1"] = false

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{now}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.quotes) != 1 {
		t.Fatalf("quotes = %v, want the stale negative re-checked exactly once", pur.quotes)
	}
}

func TestDrain_AnUnreadableMemoStillQuotes(t *testing.T) {
	// The memo is an API-budget optimisation, NOT a money guard, so it fails OPEN —
	// the inverse of this queue's usual direction and deliberately so. Failing closed
	// would let one unhealthy read starve probe buying entirely, and the worst an open
	// failure costs is the single call we already make today. Every money guard is
	// untouched either way.
	now := time.Now()
	ports, _, pur, memo := oneYardPorts()
	memo.err = errors.New("listing store unavailable")

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{now}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.quotes) != 1 {
		t.Fatalf("quotes = %v, want the yard still quoted when the memo cannot be read", pur.quotes)
	}
}

func TestDrain_ASkippedYardStaysLegibleInTheReport(t *testing.T) {
	// This defect was found by reading the refusal diagnostics. A yard that stops
	// being queried must not also stop being reported, or the next such defect is
	// invisible.
	now := time.Now()
	ports, _, _, memo := oneYardPorts()
	memo.scannedAt["X1-AA-Y1"] = now.Add(-time.Minute)
	memo.sells["X1-AA-Y1"] = false

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{now})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(rep.Refusals) != 1 {
		t.Fatalf("refusals = %+v, want the skipped yard recorded", rep.Refusals)
	}
	if rep.Refusals[0].Yard != "X1-AA-Y1" || rep.Refusals[0].Step != BuyStepMemo {
		t.Fatalf("refusal = %+v, want a memo-step refusal naming the skipped yard", rep.Refusals[0])
	}
}

func TestDrain_AYardTheMemoSaysSellsProbesStillClearsEveryMoneyGuard(t *testing.T) {
	// The memo removes candidates; it never waves one through. A yard it reports as
	// probe-selling is quoted and floor-checked exactly as before — here the treasury
	// is below the floor, so nothing may be bought.
	now := time.Now()
	ports, _, pur, memo := oneYardPorts()
	memo.scannedAt["X1-AA-Y1"] = now.Add(-time.Minute)
	memo.sells["X1-AA-Y1"] = true
	ports.Treasury = &fakeTreasury{credits: 50_100} // under the floor once the probe is priced

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{now})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.quotes) != 1 {
		t.Fatalf("quotes = %v, want the probe-selling yard still quoted", pur.quotes)
	}
	if rep.Bought != 0 || !rep.FloorHeld {
		t.Fatalf("report = %+v, want the buy floor to hold — the memo must never bypass a money guard", rep)
	}
}

func TestDrain_ANilListingMemoBehavesExactlyAsBefore(t *testing.T) {
	// The port is optional. An unwired memo must quote everything, so the drain is
	// byte-identical to its pre-memo behaviour wherever it is not wired.
	now := time.Now()
	ports, _, pur, _ := oneYardPorts()
	ports.ListingMemo = nil

	if _, err := DrainBuyQueue(context.Background(), ports, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{now}); err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.quotes) != 1 {
		t.Fatalf("quotes = %v, want an unwired memo to change nothing", pur.quotes)
	}
}

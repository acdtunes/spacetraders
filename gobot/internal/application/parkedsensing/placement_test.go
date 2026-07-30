package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

type moveCall struct{ ship, destination string }

type fakeMover struct {
	navigates []moveCall
	routes    []moveCall
	docks     []string

	navErr, routeErr, dockErr error
	// failFor breaks the move for NAMED hulls only, leaving every other hull
	// flyable. The blanket navErr/routeErr cannot express the starvation case at
	// all: it needs one set of hulls that fails every tick and another that would
	// succeed if only the machine ever reached it.
	failFor map[string]error
}

func (f *fakeMover) NavigateWithin(_ context.Context, _ int, ship, destination string) error {
	f.navigates = append(f.navigates, moveCall{ship, destination})
	if err := f.failFor[ship]; err != nil {
		return err
	}
	return f.navErr
}

func (f *fakeMover) RouteAcross(_ context.Context, _ int, ship, _, destination string) error {
	f.routes = append(f.routes, moveCall{ship, destination})
	if err := f.failFor[ship]; err != nil {
		return err
	}
	return f.routeErr
}

func (f *fakeMover) Dock(_ context.Context, _ int, ship string) error {
	f.docks = append(f.docks, ship)
	return f.dockErr
}

// placementPorts wires one slot and one hull position.
func placementPorts(slot QueuedSlot, pos ShipPos) (PlacementPorts, *fakeBuyLedger, *fakeMover, *fakeFleet) {
	led := &fakeBuyLedger{slots: []QueuedSlot{slot}}
	mover := &fakeMover{}
	fleet := &fakeFleet{}
	return PlacementPorts{
		Ledger: led,
		Ships:  &fakeShipReader{positions: map[string]ShipPos{slot.AssignedShip: pos}},
		Mover:  mover,
		Fleet:  fleet,
	}, led, mover, fleet
}

func boughtSlot() QueuedSlot {
	return QueuedSlot{
		Waypoint: "X1-AA-M1", System: "X1-AA", Kind: SlotKindMarket,
		State: SlotStateBought, AssignedShip: "PROBE-A",
	}
}

func inTransitSlot() QueuedSlot {
	s := boughtSlot()
	s.State = SlotStateInTransit
	return s
}

// --- BOUGHT → IN_TRANSIT ------------------------------------------------------

func TestAdvance_DispatchesBoughtProbeWithinItsSystem(t *testing.T) {
	ports, led, mover, _ := placementPorts(boughtSlot(),
		ShipPos{Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true})

	rep, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions)
	if err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if len(mover.navigates) != 1 || mover.navigates[0] != (moveCall{"PROBE-A", "X1-AA-M1"}) {
		t.Fatalf("in-system dispatch issued %v, want one NavigateWithin to the slot", mover.navigates)
	}
	if len(mover.routes) != 0 {
		t.Fatalf("used the cross-system router for an in-system hop: %v", mover.routes)
	}
	if led.slots[0].State != SlotStateInTransit {
		t.Fatalf("slot is %q after dispatch, want %q", led.slots[0].State, SlotStateInTransit)
	}
	if rep.Dispatched != 1 {
		t.Fatalf("report says Dispatched=%d, want 1", rep.Dispatched)
	}
	// One slot, one command: the tick must not also try to dock a hull it just
	// sent flying.
	if len(mover.docks) != 0 {
		t.Fatalf("issued a second command for the same slot in one tick: %v", mover.docks)
	}
}

func TestAdvance_RoutesBoughtProbeAcrossSystems(t *testing.T) {
	ports, _, mover, _ := placementPorts(boughtSlot(),
		ShipPos{Waypoint: "X1-ZZ-Y1", NavStatus: navigation.NavStatusDocked, Found: true})

	if _, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions); err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if len(mover.routes) != 1 || mover.routes[0] != (moveCall{"PROBE-A", "X1-AA-M1"}) {
		t.Fatalf("cross-system dispatch issued %v, want one RouteAcross to the slot", mover.routes)
	}
	if len(mover.navigates) != 0 {
		t.Fatalf("used the in-system planner across a gate: %v", mover.navigates)
	}
}

func TestAdvance_BoughtProbeAlreadyAtItsSlotSkipsTheMove(t *testing.T) {
	ports, led, mover, _ := placementPorts(boughtSlot(),
		ShipPos{Waypoint: "X1-AA-M1", NavStatus: navigation.NavStatusDocked, Found: true})

	if _, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions); err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if len(mover.navigates)+len(mover.routes) != 0 {
		t.Fatalf("moved a hull that was already standing on its slot: %v %v", mover.navigates, mover.routes)
	}
	if led.slots[0].State != SlotStateInTransit {
		t.Fatalf("slot is %q, want %q so the next tick parks it", led.slots[0].State, SlotStateInTransit)
	}
}

func TestAdvance_NavigateFailureLeavesSlotBought(t *testing.T) {
	ports, led, mover, _ := placementPorts(boughtSlot(),
		ShipPos{Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true})
	mover.navErr = errors.New("no fuel")

	rep, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions)
	if err != nil {
		t.Fatalf("a failed navigate must not fail the tick, got: %v", err)
	}
	if led.slots[0].State != SlotStateBought {
		t.Fatalf("slot is %q after a failed navigate, want %q so the next tick retries", led.slots[0].State, SlotStateBought)
	}
	if rep.Dispatched != 0 {
		t.Fatalf("report says Dispatched=%d after a failed navigate, want 0", rep.Dispatched)
	}
}

func TestAdvance_ReassertsTheSensingFleetTagOnDispatch(t *testing.T) {
	ports, _, _, fleet := placementPorts(boughtSlot(),
		ShipPos{Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true})

	if _, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions); err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	// The purchase already tagged this hull; re-asserting is idempotent and is
	// what repairs a hull whose tag write failed at buy time.
	if len(fleet.assigns) != 1 || fleet.assigns[0] != (fleetAssign{"PROBE-A", SensingParkedFleetTag}) {
		t.Fatalf("fleet assignments were %+v, want one re-assert of %q", fleet.assigns, SensingParkedFleetTag)
	}
}

// --- IN_TRANSIT → PARKED ------------------------------------------------------

func TestAdvance_DocksProbeThatArrivedInOrbit(t *testing.T) {
	ports, led, mover, _ := placementPorts(inTransitSlot(),
		ShipPos{Waypoint: "X1-AA-M1", NavStatus: navigation.NavStatusInOrbit, Found: true})

	rep, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions)
	if err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if len(mover.docks) != 1 || mover.docks[0] != "PROBE-A" {
		t.Fatalf("issued %v, want one Dock for the arrived probe", mover.docks)
	}
	if led.slots[0].State != SlotStateInTransit {
		t.Fatalf("slot is %q while still docking, want %q until the ships table confirms it", led.slots[0].State, SlotStateInTransit)
	}
	if rep.Parked != 0 {
		t.Fatalf("report says Parked=%d before the hull is confirmed docked, want 0", rep.Parked)
	}
}

func TestAdvance_ParksDockedProbeAtItsSlot(t *testing.T) {
	ports, led, mover, _ := placementPorts(inTransitSlot(),
		ShipPos{Waypoint: "X1-AA-M1", NavStatus: navigation.NavStatusDocked, Found: true})

	rep, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions)
	if err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if led.slots[0].State != SlotStateParked {
		t.Fatalf("slot is %q with the hull docked on station, want %q", led.slots[0].State, SlotStateParked)
	}
	if len(mover.docks) != 0 {
		t.Fatalf("re-docked an already-docked hull: %v", mover.docks)
	}
	if rep.Parked != 1 {
		t.Fatalf("report says Parked=%d, want 1", rep.Parked)
	}
}

func TestAdvance_LeavesProbeStillEnRouteAlone(t *testing.T) {
	ports, led, mover, _ := placementPorts(inTransitSlot(),
		ShipPos{Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusInTransit, Found: true})

	if _, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions); err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if len(mover.docks)+len(mover.navigates)+len(mover.routes) != 0 {
		t.Fatalf("commanded a hull that is still flying: %v %v %v", mover.docks, mover.navigates, mover.routes)
	}
	if led.slots[0].State != SlotStateInTransit {
		t.Fatalf("slot is %q mid-flight, want %q", led.slots[0].State, SlotStateInTransit)
	}
}

// --- bounds and unknowns ------------------------------------------------------

func TestAdvance_RespectsMaxActions(t *testing.T) {
	led := &fakeBuyLedger{slots: []QueuedSlot{
		{Waypoint: "X1-AA-M1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateBought, AssignedShip: "PROBE-A"},
		{Waypoint: "X1-AA-M2", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateBought, AssignedShip: "PROBE-B"},
		{Waypoint: "X1-AA-M3", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateBought, AssignedShip: "PROBE-C"},
	}}
	mover := &fakeMover{}
	ports := PlacementPorts{
		Ledger: led,
		Ships: &fakeShipReader{positions: map[string]ShipPos{
			"PROBE-A": {Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true},
			"PROBE-B": {Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true},
			"PROBE-C": {Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true},
		}},
		Mover: mover,
		Fleet: &fakeFleet{},
	}

	rep, err := AdvancePlacements(context.Background(), ports, testPlayerID, 2)
	if err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if len(mover.navigates) != 2 {
		t.Fatalf("issued %d moves under a 2-action budget: %v", len(mover.navigates), mover.navigates)
	}
	if rep.Actions != 2 {
		t.Fatalf("report says Actions=%d, want 2", rep.Actions)
	}
}

func TestAdvance_NonPositiveMaxActionsFallsBackToTheDefault(t *testing.T) {
	ports, _, mover, _ := placementPorts(boughtSlot(),
		ShipPos{Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true})

	if _, err := AdvancePlacements(context.Background(), ports, testPlayerID, 0); err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if len(mover.navigates) != 1 {
		t.Fatalf("a zero budget stalled the machine instead of using the default: %v", mover.navigates)
	}
}

func TestAdvance_SkipsHullThePositionReaderCannotFind(t *testing.T) {
	ports, led, mover, _ := placementPorts(boughtSlot(), ShipPos{Found: false})

	if _, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions); err != nil {
		t.Fatalf("an unlocatable hull must not fail the tick, got: %v", err)
	}
	if len(mover.navigates)+len(mover.routes) != 0 {
		t.Fatalf("commanded a hull the ships table does not know: %v %v", mover.navigates, mover.routes)
	}
	if led.slots[0].State != SlotStateBought {
		t.Fatalf("slot is %q, want it held at %q until the hull is locatable", led.slots[0].State, SlotStateBought)
	}
}

func TestAdvance_SkipsHullWhosePositionIsUnreadable(t *testing.T) {
	ports, _, mover, _ := placementPorts(boughtSlot(), ShipPos{})
	ports.Ships = &fakeShipReader{atErr: errors.New("db down")}

	if _, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions); err != nil {
		t.Fatalf("an unreadable position must not fail the tick, got: %v", err)
	}
	if len(mover.navigates)+len(mover.routes)+len(mover.docks) != 0 {
		t.Fatalf("commanded a hull whose position could not be read: %v %v %v", mover.navigates, mover.routes, mover.docks)
	}
}

func TestAdvance_SkipsSlotWithNoRecordedHull(t *testing.T) {
	slot := boughtSlot()
	slot.AssignedShip = ""
	ports, _, mover, _ := placementPorts(slot, ShipPos{})

	if _, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions); err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if len(mover.navigates)+len(mover.routes)+len(mover.docks) != 0 {
		t.Fatalf("issued a command for a slot with no hull behind it: %v %v %v", mover.navigates, mover.routes, mover.docks)
	}
}

// --- cross-task fix: claims that were never dispatched ------------------------
//
// Not every IN_TRANSIT row got there through a purchase. Two claim paths write
// IN_TRANSIT directly, with no hull movement ever issued: this package's own
// spare re-tasking, and the seed tour-end claims made by the coordinator above
// it. Both leave a hull standing where it was, holding a slot that reads as
// in-flight, and counting against the probe cap forever.

// TestAdvance_FliesASpareReTaskedToAnotherWaypoint walks the whole path the
// defect lived on: a spare parked at the yard is re-tasked to a market slot
// across the system, and must actually get there.
func TestAdvance_FliesASpareReTaskedToAnotherWaypoint(t *testing.T) {
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-AA-M1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-AA-Y1", System: "X1-AA", Kind: SlotKindSpare, State: SlotStateParked, AssignedShip: "PROBE-SPARE"},
		},
		systems: []ScreenedSystem{{System: "X1-AA", DepthCredits: 900}},
	}
	purchaser := &fakePurchaser{price: 1_000}
	buy := BuyPorts{
		Treasury:   &fakeTreasury{credits: 10_000_000},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  purchaser,
		Ledger:     led,
		Yards:      &fakeYards{yards: map[string][]string{"X1-AA": {"X1-AA-Y1"}}},
		Ships:      &fakeShipReader{docked: map[string]string{"X1-AA-Y1": "PROBE-SPARE"}},
		Fleet:      &fakeFleet{},
	}

	if _, err := DrainBuyQueue(context.Background(), buy, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
		t.Fatalf("drain returned error: %v", err)
	}
	if len(purchaser.buys) != 0 || led.slots[0].State != SlotStateInTransit {
		t.Fatalf("precondition failed: want the spare re-tasked to IN_TRANSIT, got state=%q buys=%v", led.slots[0].State, purchaser.buys)
	}

	// The hull is still standing at the yard: nothing has ever told it to move.
	ships := &fakeShipReader{positions: map[string]ShipPos{
		"PROBE-SPARE": {Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true},
	}}
	mover := &fakeMover{}
	place := PlacementPorts{Ledger: led, Ships: ships, Mover: mover, Fleet: &fakeFleet{}}

	rep, err := AdvancePlacements(context.Background(), place, testPlayerID, DefaultMaxPlacementActions)
	if err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if len(mover.navigates) != 1 || mover.navigates[0] != (moveCall{"PROBE-SPARE", "X1-AA-M1"}) {
		t.Fatalf("re-tasked spare was never flown to its slot: navigates=%v routes=%v", mover.navigates, mover.routes)
	}
	if rep.Dispatched != 1 {
		t.Fatalf("report says Dispatched=%d, want 1", rep.Dispatched)
	}

	// It arrives in orbit; the next tick berths it.
	ships.positions["PROBE-SPARE"] = ShipPos{Waypoint: "X1-AA-M1", NavStatus: navigation.NavStatusInOrbit, Found: true}
	if _, err := AdvancePlacements(context.Background(), place, testPlayerID, DefaultMaxPlacementActions); err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if len(mover.docks) != 1 {
		t.Fatalf("arrived spare was not docked: %v", mover.docks)
	}

	// Docked on station: the placement completes.
	ships.positions["PROBE-SPARE"] = ShipPos{Waypoint: "X1-AA-M1", NavStatus: navigation.NavStatusDocked, Found: true}
	if _, err := AdvancePlacements(context.Background(), place, testPlayerID, DefaultMaxPlacementActions); err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if led.slots[0].State != SlotStateParked {
		t.Fatalf("slot is %q at the end of the lifecycle, want %q", led.slots[0].State, SlotStateParked)
	}
}

func TestAdvance_RoutesANeverDispatchedClaimAcrossSystems(t *testing.T) {
	ports, _, mover, fleet := placementPorts(inTransitSlot(),
		ShipPos{Waypoint: "X1-ZZ-Y1", NavStatus: navigation.NavStatusDocked, Found: true})

	if _, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions); err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if len(mover.routes) != 1 || mover.routes[0] != (moveCall{"PROBE-A", "X1-AA-M1"}) {
		t.Fatalf("never-dispatched cross-system claim was not routed: navigates=%v routes=%v", mover.navigates, mover.routes)
	}
	// A claim that bypassed the purchase path also bypassed the tag write there,
	// so this dispatch is the first and only place the hull gets tagged. Without
	// this assertion the tag re-assert is justified only by a comment — which is
	// exactly the shape of the assumption that produced the strand it fixes.
	if len(fleet.assigns) != 1 || fleet.assigns[0] != (fleetAssign{"PROBE-A", SensingParkedFleetTag}) {
		t.Fatalf("fleet assignments were %+v, want the claimed hull tagged %q", fleet.assigns, SensingParkedFleetTag)
	}
}

// TestAdvance_DoesNotReDispatchAHullThatIsGenuinelyFlying is the other half of
// the fix: a hull whose nav row shows it moving must be left alone, or every
// tick would pile another move onto a ship already in flight.
func TestAdvance_DoesNotReDispatchAHullThatIsGenuinelyFlying(t *testing.T) {
	ports, led, mover, _ := placementPorts(inTransitSlot(),
		ShipPos{Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusInTransit, Found: true})

	rep, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions)
	if err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if len(mover.navigates)+len(mover.routes)+len(mover.docks) != 0 {
		t.Fatalf("commanded a hull that is already flying: %v %v %v", mover.navigates, mover.routes, mover.docks)
	}
	if rep.Actions != 0 {
		t.Fatalf("report says Actions=%d for a hull left alone mid-flight, want 0", rep.Actions)
	}
	if led.slots[0].State != SlotStateInTransit {
		t.Fatalf("slot is %q mid-flight, want %q", led.slots[0].State, SlotStateInTransit)
	}
}

func TestAdvance_FailedDispatchOfAClaimLeavesItInTransit(t *testing.T) {
	ports, led, mover, _ := placementPorts(inTransitSlot(),
		ShipPos{Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true})
	mover.navErr = errors.New("no fuel")

	if _, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions); err != nil {
		t.Fatalf("a failed dispatch must not fail the tick, got: %v", err)
	}
	if len(mover.navigates) != 1 {
		t.Fatalf("the dispatch was never attempted: %v", mover.navigates)
	}
	if led.slots[0].State != SlotStateInTransit {
		t.Fatalf("slot is %q after a failed dispatch, want %q so the next tick retries", led.slots[0].State, SlotStateInTransit)
	}
}

// --- starvation: a failing head must not own the whole budget (sp-cwnwb) ------
//
// The worklist is a FIXED CAP OVER A QUEUE THAT DOES NOT DRAIN. A slot whose move
// fails stays in the state it was in, so it is still on the worklist next tick,
// in the same place — and while a failed move consumed the same budget as a
// successful one, a persistently-failing head consumed the whole tick before any
// healthy slot behind it was even examined.
//
// Measured live (era 5, 2026-07-30): 266 BOUGHT + 52 IN_TRANSIT, and across ~4
// ticks — ~40 available actions — BOUGHT never moved off 266 and not one dispatch
// was issued. TORWIND-15F, BOUGHT for X1-K39-AC2B for 22.5 hours, had ZERO lines
// in daemon.log: never once attempted, because "X1-K*" sorts behind ~100 A-J
// slots that were failing the gate walk every tick.
//
// This is the same defect the screening sweep already fixed for itself (see the
// screened_at rotation in run_probe_sensing_coordinator.go, where the five
// alphabetically-first PENDING systems were the five MOST recently screened, 18
// seconds old, while the alphabetic tail had gone 12.2 hours untouched).

// failingHeadWorklist builds a worklist strictly LONGER than the action cap whose
// head slots all fail persistently, with healthy slots at the tail.
//
// The head count must EXCEED the cap, not merely reach it: a fixture that fits
// inside the budget leaves the budget unconsulted and cannot detect starvation at
// all. Every hull stands in a foreign system, so every move takes the gate-walk
// path — which is where the real failures are.
func failingHeadWorklist(failing, healthy int) (PlacementPorts, *fakeBuyLedger, *fakeMover) {
	led := &fakeBuyLedger{}
	positions := map[string]ShipPos{}
	failFor := map[string]error{}

	add := func(waypoint, ship string, fails bool) {
		led.slots = append(led.slots, QueuedSlot{
			Waypoint: waypoint, System: "X1-AA", Kind: SlotKindMarket,
			State: SlotStateBought, AssignedShip: ship,
		})
		// Standing still in a system that is not the target — exactly the live
		// shape, and what makes flyToSlot choose the cross-system walk.
		positions[ship] = ShipPos{Waypoint: "X1-RJ93-Y1", NavStatus: navigation.NavStatusDocked, Found: true}
		if fails {
			failFor[ship] = errors.New("no stored gate route within 0 jumps")
		}
	}

	// "X1-A.." sorts ahead of "X1-K..", so the failing set is the permanent head
	// of the fixed order and the healthy slots are the starved tail.
	for i := 0; i < failing; i++ {
		add(fmt.Sprintf("X1-A%02d-M1", i), fmt.Sprintf("WALLED-%02d", i), true)
	}
	for i := 0; i < healthy; i++ {
		add(fmt.Sprintf("X1-K%02d-M1", i), fmt.Sprintf("HEALTHY-%02d", i), false)
	}

	mover := &fakeMover{failFor: failFor}
	return PlacementPorts{
		Ledger: led,
		Ships:  &fakeShipReader{positions: positions},
		Mover:  mover,
		Fleet:  &fakeFleet{},
	}, led, mover
}

func movedShips(mover *fakeMover) map[string]int {
	out := map[string]int{}
	for _, c := range mover.navigates {
		out[c.ship]++
	}
	for _, c := range mover.routes {
		out[c.ship]++
	}
	return out
}

// TestAdvance_PersistentlyFailingHeadDoesNotStarveTheTail is the bead's defect.
// Twelve failing slots against a ten-action budget: while a failure cost an
// action, the head alone exhausted the tick and the healthy tail was never
// reached, tick after tick, forever.
func TestAdvance_PersistentlyFailingHeadDoesNotStarveTheTail(t *testing.T) {
	ports, led, mover := failingHeadWorklist(12, 1)

	rep, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions)
	if err != nil {
		t.Fatalf("a worklist of failing moves must not fail the tick, got: %v", err)
	}

	moved := movedShips(mover)
	if moved["HEALTHY-00"] == 0 {
		t.Fatalf("the healthy tail slot was never attempted: %d of %d slots were reached, and every one of them was a failing head slot. "+
			"A failed move is charging the same budget as a successful one, so the head owns the whole tick. moves=%v report=%+v",
			len(moved), len(led.slots), moved, rep)
	}
	if rep.Dispatched != 1 {
		t.Fatalf("report says Dispatched=%d, want 1 — the one healthy slot", rep.Dispatched)
	}
	if led.slots[len(led.slots)-1].State != SlotStateInTransit {
		t.Fatalf("tail slot is %q, want %q: it was reached but never advanced", led.slots[len(led.slots)-1].State, SlotStateInTransit)
	}
	// The failing slots must stay put and stay retryable — the fix reallocates the
	// budget, it does not abandon them.
	for i := 0; i < 12; i++ {
		if led.slots[i].State != SlotStateBought {
			t.Fatalf("failing slot %d is %q, want %q so the next tick retries it", i, led.slots[i].State, SlotStateBought)
		}
	}
}

// TestAdvance_BoundsRefusedAttemptsInOneTick is the other side of the fix. Not
// counting a refusal against the accepted-command budget must not mean not
// counting it at all: the walk's second step issues a jump the API can reject, so
// an unbounded sweep of a 300-slot backlog would fire 300 rejected commands in a
// single tick.
func TestAdvance_BoundsRefusedAttemptsInOneTick(t *testing.T) {
	const failing = 200
	ports, _, mover := failingHeadWorklist(failing, 0)

	rep, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions)
	if err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}

	wantBound := DefaultMaxPlacementActions * placementFailureBudgetMultiple
	attempts := len(mover.navigates) + len(mover.routes)
	if attempts != wantBound {
		t.Fatalf("a tick facing %d failing slots issued %d attempts, want exactly the refusal budget %d: "+
			"fewer means the sweep stalls short of its bound, more means refusals are unbounded and one tick can flood the API",
			failing, attempts, wantBound)
	}
	if rep.Failures != wantBound {
		t.Fatalf("report says Failures=%d, want %d", rep.Failures, wantBound)
	}
	if rep.Actions != 0 {
		t.Fatalf("report says Actions=%d when every move was refused, want 0 — a refusal is not an advance, and a tick that "+
			"advanced nothing must not read as progress to the stall detector", rep.Actions)
	}
}

// TestAdvance_StampsEveryChargedSlotAndOnlyThose pins what the rotation is built
// on. Stamping only successes would leave the failing slots permanently oldest and
// hand them the head forever — the monopoly this exists to break — and stamping a
// slot that was left alone would push a hull that is quietly flying to the back for
// no reason.
func TestAdvance_StampsEveryChargedSlotAndOnlyThose(t *testing.T) {
	led := &fakeBuyLedger{slots: []QueuedSlot{
		{Waypoint: "X1-AA-M1", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateBought, AssignedShip: "ADVANCES"},
		{Waypoint: "X1-AA-M2", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateBought, AssignedShip: "REFUSED"},
		{Waypoint: "X1-AA-M3", System: "X1-AA", Kind: SlotKindMarket, State: SlotStateInTransit, AssignedShip: "FLYING"},
	}}
	mover := &fakeMover{failFor: map[string]error{"REFUSED": errors.New("gate refused the hull")}}
	ports := PlacementPorts{
		Ledger: led,
		Ships: &fakeShipReader{positions: map[string]ShipPos{
			"ADVANCES": {Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true},
			"REFUSED":  {Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true},
			// Genuinely in flight: costs no budget, so it must not be stamped.
			"FLYING": {Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusInTransit, Found: true},
		}},
		Mover: mover,
		Fleet: &fakeFleet{},
	}

	rep, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions)
	if err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}
	if rep.Actions != 1 || rep.Failures != 1 {
		t.Fatalf("report says Actions=%d Failures=%d, want 1 and 1", rep.Actions, rep.Failures)
	}

	got := map[string]bool{}
	for _, a := range led.attempts {
		got[a.waypoint] = true
	}
	if !got["X1-AA-M1"] {
		t.Fatalf("the ADVANCED slot was not stamped: %+v", led.attempts)
	}
	if !got["X1-AA-M2"] {
		t.Fatalf("the REFUSED slot was not stamped, so it keeps the oldest stamp and the head of the worklist forever — "+
			"this is the starvation the rotation is meant to break: %+v", led.attempts)
	}
	if got["X1-AA-M3"] {
		t.Fatalf("a hull left alone mid-flight was stamped as having consumed a turn: %+v", led.attempts)
	}
}

// TestAdvance_RotatesTheWorklistSoEveryFailingSlotGetsATurn is the fairness
// property itself, over the ticks it takes to see it. Nine slots that all fail,
// against a budget of three refusals a tick: every slot must be tried exactly once
// before any is tried twice.
//
// A single tick cannot show this. The defect was never "one tick picks badly" — it
// was that every tick picked the SAME badly, so the property is only observable
// across ticks and the fixture has to supply more slots than one tick can absorb.
func TestAdvance_RotatesTheWorklistSoEveryFailingSlotGetsATurn(t *testing.T) {
	const (
		slots      = 9
		maxActions = 1 // → a refusal budget of 3, so one tick reaches 3 of the 9
	)
	ports, led, _ := failingHeadWorklist(slots, 0)

	seen := map[string]int{}
	for tick := 1; tick <= 3; tick++ {
		before := len(led.attempts)
		if _, err := AdvancePlacements(context.Background(), ports, testPlayerID, maxActions); err != nil {
			t.Fatalf("tick %d returned error: %v", tick, err)
		}
		for _, a := range led.attempts[before:] {
			seen[a.waypoint]++
			if seen[a.waypoint] > 1 && len(seen) < slots {
				t.Fatalf("tick %d gave %s a second turn while %d of %d slots had never had one: the worklist is not rotating",
					tick, a.waypoint, slots-len(seen), slots)
			}
		}
	}

	if len(seen) != slots {
		t.Fatalf("after 3 ticks only %d of %d slots had been attempted; a backlog of N must be covered in ceil(N/budget) ticks. seen=%v",
			len(seen), slots, seen)
	}
}

// TestAdvance_ReachesANeverAttemptedTailSlotBeforeARetriedHead is TORWIND-15F,
// reduced. It sat BOUGHT for 22.5 hours with ZERO log lines because its waypoint
// sorted behind a hundred slots that were being retried every tick. A slot the
// machine has never tried must outrank one it tried a moment ago, whatever the
// symbols say.
func TestAdvance_ReachesANeverAttemptedTailSlotBeforeARetriedHead(t *testing.T) {
	// Three failing head slots against a budget of three refusals: tick one is
	// saturated by the head ALONE, which is the live condition — 266 walled slots
	// filling the tick before anything else is looked at. Without that saturation
	// the tail is reached on tick one by the budget fix and the ORDER is never
	// exercised, so this fixture has to supply strictly more failures than one
	// tick can absorb.
	ports, led, mover := failingHeadWorklist(3, 0)
	led.slots = append(led.slots, QueuedSlot{
		// Sorts last alphabetically, exactly as "X1-K39-AC2B" did.
		Waypoint: "X1-K39-M1", System: "X1-AA", Kind: SlotKindMarket,
		State: SlotStateBought, AssignedShip: "TORWIND-15F",
	})
	ports.Ships.(*fakeShipReader).positions["TORWIND-15F"] = ShipPos{
		Waypoint: "X1-RJ93-Y1", NavStatus: navigation.NavStatusDocked, Found: true,
	}

	// Tick one: the three refusals spend the whole refusal budget, so the tail slot
	// is left untried — the state the bug produced, every tick, forever.
	if _, err := AdvancePlacements(context.Background(), ports, testPlayerID, 1); err != nil {
		t.Fatalf("tick one returned error: %v", err)
	}
	if moved := movedShips(mover); moved["TORWIND-15F"] != 0 {
		t.Fatalf("precondition failed: the tail slot was already reached on tick one, so tick two proves nothing: %v", moved)
	}

	// Tick two: the head now carries a stamp and the tail still carries none, so
	// the tail outranks it.
	if _, err := AdvancePlacements(context.Background(), ports, testPlayerID, 1); err != nil {
		t.Fatalf("tick two returned error: %v", err)
	}
	moved := movedShips(mover)
	if moved["TORWIND-15F"] == 0 {
		t.Fatalf("the never-attempted tail slot was still not reached on tick two: the alphabetical head keeps its monopoly "+
			"and this hull stays invisible for as long as the head keeps failing. moves=%v", moved)
	}
	if tail := led.slots[len(led.slots)-1]; tail.State != SlotStateInTransit {
		t.Fatalf("tail slot is %q, want %q", tail.State, SlotStateInTransit)
	}
}

// TestAdvance_AFailedAttemptStampDoesNotFailTheTick keeps the stamp a hint. It
// moves no money and buys nothing, so a slot that was genuinely advanced must not
// be undone by a bookkeeping write that would not take.
func TestAdvance_AFailedAttemptStampDoesNotFailTheTick(t *testing.T) {
	ports, led, mover, _ := placementPorts(boughtSlot(),
		ShipPos{Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true})
	led.attemptErr = errors.New("stamp write failed")

	rep, err := AdvancePlacements(context.Background(), ports, testPlayerID, DefaultMaxPlacementActions)
	if err != nil {
		t.Fatalf("an unwritable attempt stamp must not fail the tick, got: %v", err)
	}
	if len(mover.navigates) != 1 || rep.Dispatched != 1 {
		t.Fatalf("the dispatch was abandoned over its stamp: navigates=%v report=%+v", mover.navigates, rep)
	}
	if led.slots[0].State != SlotStateInTransit {
		t.Fatalf("slot is %q, want %q — the advance stands whether or not it could be stamped", led.slots[0].State, SlotStateInTransit)
	}
}

// TestAdvance_NamesAnUnwritableAttemptStamp keeps the degradation LOUD. A stamp
// that will not write is survivable — the slot sorts as never-attempted and gets
// another early turn — but if every stamp is failing, the worklist stops rotating
// and the engine is back in the starvation this fix removed. The symptom of that is
// a head that never changes, which is indistinguishable from the original bug, so
// the cause has to name itself.
func TestAdvance_NamesAnUnwritableAttemptStamp(t *testing.T) {
	ports, led, _, _ := placementPorts(boughtSlot(),
		ShipPos{Waypoint: "X1-AA-Y1", NavStatus: navigation.NavStatusDocked, Found: true})
	led.attemptErr = errors.New("disk full")

	logger := &recordingPlacementLogger{}
	ctx := logging.WithLogger(context.Background(), logger)

	if _, err := AdvancePlacements(ctx, ports, testPlayerID, DefaultMaxPlacementActions); err != nil {
		t.Fatalf("AdvancePlacements returned error: %v", err)
	}

	named := logger.withAction("parked_sensing_attempt_stamp_failed")
	if len(named) != 1 {
		t.Fatalf("an unwritable attempt stamp was silent (%d matching lines): a fleet-wide stamp failure would look "+
			"exactly like the starvation bug with no line naming the cause", len(named))
	}
	if got := named[0].metadata["waypoint"]; got != "X1-AA-M1" {
		t.Fatalf("warning names waypoint %v, want the slot that could not be stamped", got)
	}
	if !strings.Contains(named[0].message, "disk full") {
		t.Fatalf("the underlying write error did not survive into the message: %q", named[0].message)
	}
}

// recordingPlacementLogger captures log lines for the assertions above.
type recordingPlacementLogger struct {
	lines []struct {
		level    string
		message  string
		metadata map[string]interface{}
	}
}

func (l *recordingPlacementLogger) Log(level, message string, metadata map[string]interface{}) {
	l.lines = append(l.lines, struct {
		level    string
		message  string
		metadata map[string]interface{}
	}{level, message, metadata})
}

func (l *recordingPlacementLogger) withAction(action string) []struct {
	level    string
	message  string
	metadata map[string]interface{}
} {
	var out []struct {
		level    string
		message  string
		metadata map[string]interface{}
	}
	for _, line := range l.lines {
		if line.metadata["action"] == action {
			out = append(out, line)
		}
	}
	return out
}

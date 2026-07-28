package parkedsensing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

type moveCall struct{ ship, destination string }

type fakeMover struct {
	navigates []moveCall
	routes    []moveCall
	docks     []string

	navErr, routeErr, dockErr error
}

func (f *fakeMover) NavigateWithin(_ context.Context, _ int, ship, destination string) error {
	f.navigates = append(f.navigates, moveCall{ship, destination})
	return f.navErr
}

func (f *fakeMover) RouteAcross(_ context.Context, _ int, ship, _, destination string) error {
	f.routes = append(f.routes, moveCall{ship, destination})
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

	if _, err := DrainBuyQueue(context.Background(), buy, testPlayerID, BuyKnobs{ProbeCap: 100}, fixedClock{time.Now()}); err != nil {
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

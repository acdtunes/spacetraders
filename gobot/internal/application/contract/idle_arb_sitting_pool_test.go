package contract

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// The SITTING idle pool bug (sp-54uif / sp-mtgje): idle contract hulls that finished their contracts
// long ago and are held ready, parked off-slot with NO `fleet hub` pins (the operator relies on the
// auto hub-placement). The standing re-home sweep must home each to ITS OWN fixed slot by auto-resolving
// its standby set from the placement provider's ≤6 fixed slots — exactly as the between-legs hook does
// via ResolveStandbyForHoming. Before the fix the sweep bailed on the empty fleet-hub set, so the pool
// piled where it last finished.

// threeSlotPlacement is the fixed placement set the sitting-pool tests home against — the auto-resolved
// ≤6 slots, so a 3-hull pool spreads one-per-slot with none piled.
func threeSlotPlacement() *fakePlacementProvider {
	return &fakePlacementProvider{slots: []string{"X1-HUB-D40", "X1-HUB-E42", "X1-HUB-F44"}}
}

// The whole sitting pool is homed off its off-slot park even with NO hubs pinned: the sweep
// auto-resolves the standby set from the placement provider and zips each hull to its OWN distinct slot.
// The pool is ALL "reserved" (ReserveHulls above the fleet size, so not one hull is arb-eligible),
// proving the pile is homed on its own — not as an arb side effect.
func TestIdleArb_SittingReservedPool_HomedToFixedSlots_WhenNoHubsPinned(t *testing.T) {
	j59 := idleArbWaypoint(t, "X1-UM5-J59", 0, 100) // off-slot park (no market, no lane)
	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "HAULER-A", j59, testFleet),
		idleArbHull(t, "HAULER-B", j59, testFleet),
		idleArbHull(t, "HAULER-C", j59, testFleet),
	}}

	// nil standby = empty fleet-hub set; homing must come from the placement resolver.
	d, _, homer := idleArbRehomeHarness(t, repo, nil, IdleArbConfig{ReserveHulls: 10})
	d.SetStandbyPlacementProvider(threeSlotPlacement())

	d.DispatchOnce(context.Background())

	// Every sitting hull is homed (each owns a distinct fixed slot), though none was arb-eligible.
	homed := append([]string(nil), homer.homed...)
	sort.Strings(homed)
	if !reflect.DeepEqual(homed, []string{"HAULER-A", "HAULER-B", "HAULER-C"}) {
		t.Fatalf("the sitting idle pool must ALL be homed off J59 to their fixed slots, got %v", homer.homed)
	}
	// ...carrying the ≤6 fixed placement slots so HomeShipHandler zips each hull to its own.
	gotStandby := append([]string(nil), homer.lastStandby...)
	sort.Strings(gotStandby)
	if want := []string{"X1-HUB-D40", "X1-HUB-E42", "X1-HUB-F44"}; !reflect.DeepEqual(gotStandby, want) {
		t.Fatalf("the homer must receive the fixed placement slots %v, got %v", want, homer.lastStandby)
	}
}

// No thrash (RULINGS #2): a hull already parked at ITS OWN assigned slot is left alone; only the
// genuinely off-slot hull is homed.
func TestIdleArb_HullAtItsAssignedSlot_NotReHomed(t *testing.T) {
	atSlot := idleArbWaypoint(t, "X1-HUB-D40", 0, 50) // AT-SINK's assigned slot (roster index 0 → D40)
	j59 := idleArbWaypoint(t, "X1-UM5-J59", 0, 100)   // off-slot
	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "AT-SINK", atSlot, testFleet),
		idleArbHull(t, "DRIFTED", j59, testFleet),
	}}

	d, _, homer := idleArbRehomeHarness(t, repo, nil, IdleArbConfig{ReserveHulls: 10})
	d.SetStandbyPlacementProvider(threeSlotPlacement())

	d.DispatchOnce(context.Background())

	if len(homer.homed) != 1 || homer.homed[0] != "DRIFTED" {
		t.Fatalf("only the off-slot hull must home; a hull already at ITS assigned slot must not thrash, got %v", homer.homed)
	}
}

// RULINGS #7: the command frigate is never swept by the standing re-home, even when it is
// contract-dedicated and idle off-slot — its positioning is managed by the last-resort draft, not the
// idle harvest. A regular hauler beside it homes.
func TestIdleArb_CommandFrigate_ExcludedFromStandingReHome(t *testing.T) {
	j59 := idleArbWaypoint(t, "X1-UM5-J59", 0, 100)
	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "HAULER-A", j59, testFleet),
		idleArbHull(t, "FRIGATE-1", j59, testFleet), // "-1" suffix => IsCommandHull
	}}

	d, _, homer := idleArbRehomeHarness(t, repo, nil, IdleArbConfig{ReserveHulls: 10})
	d.SetStandbyPlacementProvider(threeSlotPlacement())

	d.DispatchOnce(context.Background())

	if len(homer.homed) != 1 || homer.homed[0] != "HAULER-A" {
		t.Fatalf("the command frigate must be excluded from the standing re-home (RULINGS #7); only the hauler homes, got %v", homer.homed)
	}
}

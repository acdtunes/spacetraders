package contract

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// The dispatcher's post-leg re-homing must use the LIVE standby set resolved each
// pass, not the frozen launch snapshot it was constructed with. After a `fleet
// hub add|remove` changes the set, the at-home filter and the homer both track
// the CURRENT hubs with no restart.

// TestIdleArb_ReHome_UsesLiveStandbySet: the dispatcher is CONSTRUCTED with the
// launch hub set {E42} but handed a LIVE resolver that returns {D40} (as a `fleet
// hub` change would). On the next pass the at-home filter must use {D40}: the hull
// sitting at D40 is treated as home (not re-homed), and the hull at the now-removed
// E42 is off-station and re-homed — the exact inverse of the frozen-set behavior,
// proving the live set drives re-homing. The homer also receives the LIVE set.
func TestIdleArb_ReHome_UsesLiveStandbySet(t *testing.T) {
	const oldHub = "X1-HUB-E42" // launch hub (0,0)
	const newHub = "X1-HUB-D40" // hub added live (0,50)

	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "AT-OLD", idleArbWaypoint(t, oldHub, 0, 0), testFleet),
		idleArbHull(t, "AT-NEW", idleArbWaypoint(t, newHub, 0, 50), testFleet),
	}}

	// Constructed with the LAUNCH hub set {oldHub}; ReserveHulls high enough that
	// the arb loop launches nothing, isolating the re-home behavior.
	d, _, homer := idleArbRehomeHarness(t, repo, []string{oldHub}, IdleArbConfig{ReserveHulls: 5})

	// A `fleet hub` change: the live set is now {newHub}, not the launch {oldHub}.
	d.SetStandbyResolver(func(_ context.Context) []string { return []string{newHub} })

	d.DispatchOnce(context.Background())

	// The hull at the removed old hub is off the LIVE set → re-homed; the hull at
	// the live-added new hub is at home → left alone.
	if len(homer.homed) != 1 || homer.homed[0] != "AT-OLD" {
		t.Fatalf("re-home must use the LIVE hub set {%s}: expected only AT-OLD re-homed, got %v", newHub, homer.homed)
	}
	// The homer must be handed the LIVE set, so it balances to the current hubs.
	if len(homer.lastStandby) != 1 || homer.lastStandby[0] != newHub {
		t.Fatalf("the homer must receive the LIVE standby set {%s}, got %v", newHub, homer.lastStandby)
	}
}

// TestIdleArb_ReHome_LiveEmptySet_DisablesReHoming: an operator who `fleet hub
// remove`s every hub disables re-homing live even though the dispatcher was
// constructed with a non-empty launch set — the empty LIVE set is honored.
func TestIdleArb_ReHome_LiveEmptySet_DisablesReHoming(t *testing.T) {
	const oldHub = "X1-HUB-E42"
	const drift = "X1-HUB-D40"

	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "DRIFTED", idleArbWaypoint(t, drift, 0, 50), testFleet),
	}}

	// Launch set {oldHub}, but the live resolver reports an empty set (all hubs
	// removed). ReserveHulls high so the arb loop is out of the picture.
	d, _, homer := idleArbRehomeHarness(t, repo, []string{oldHub}, IdleArbConfig{ReserveHulls: 5})
	d.SetStandbyResolver(func(_ context.Context) []string { return nil })

	d.DispatchOnce(context.Background())

	if len(homer.homed) != 0 {
		t.Fatalf("an empty LIVE hub set must disable re-homing, got %v", homer.homed)
	}
}

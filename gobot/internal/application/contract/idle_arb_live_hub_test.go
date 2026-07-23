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

// TestIdleArb_ReHome_UsesLiveStandbySet: the dispatcher is CONSTRUCTED with the launch hub set {E42}
// but handed a LIVE resolver that returns {D40, K80} (as a `fleet hub` change would). On the next pass
// the fixed-placement assignment must use the LIVE slots: AT-NEW (roster index 0) owns D40 and sits
// there → left alone; AT-OLD (roster index 1) owns K80 but sits at the now-removed E42 → re-homed. If
// it used the frozen launch {E42}, AT-OLD would be "at its slot" and skipped — so re-homing AT-OLD
// proves the LIVE set drives. The homer also receives the LIVE set.
func TestIdleArb_ReHome_UsesLiveStandbySet(t *testing.T) {
	const oldHub = "X1-HUB-E42" // launch hub, removed live (0,0)
	const liveA = "X1-HUB-D40"  // live slot (0,50) — AT-NEW's (roster index 0)
	const liveB = "X1-HUB-K80"  // live slot — AT-OLD's (roster index 1)

	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "AT-OLD", idleArbWaypoint(t, oldHub, 0, 0), testFleet),
		idleArbHull(t, "AT-NEW", idleArbWaypoint(t, liveA, 0, 50), testFleet),
	}}

	// Constructed with the LAUNCH hub set {oldHub}; ReserveHulls high enough that
	// the arb loop launches nothing, isolating the re-home behavior.
	d, _, homer := idleArbRehomeHarness(t, repo, []string{oldHub}, IdleArbConfig{ReserveHulls: 5})

	// A `fleet hub` change: the live set is now {liveA, liveB}, not the launch {oldHub}.
	d.SetStandbyResolver(func(_ context.Context) []string { return []string{liveA, liveB} })

	d.DispatchOnce(context.Background())

	// AT-NEW is at its live slot (D40) → left alone; AT-OLD is off its live slot (K80, sitting at the
	// removed E42) → re-homed. Proves the LIVE set, not the launch snapshot, drives placement.
	if len(homer.homed) != 1 || homer.homed[0] != "AT-OLD" {
		t.Fatalf("re-home must use the LIVE hub set {%s,%s}: expected only AT-OLD re-homed, got %v", liveA, liveB, homer.homed)
	}
	// The homer must be handed the LIVE set, so it places hulls on the current slots.
	if len(homer.lastStandby) != 2 || homer.lastStandby[0] != liveA || homer.lastStandby[1] != liveB {
		t.Fatalf("the homer must receive the LIVE standby set {%s,%s}, got %v", liveA, liveB, homer.lastStandby)
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

package contract

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// The SITTING idle pool bug (sp-54uif): idle contract hulls that finished their
// contracts long ago and are held ready, parked off-station with NO `fleet hub`
// pins (the operator relies on the sp-bu6ma auto hub-placement). The standing
// re-home sweep must home them by auto-resolving its standby set from the demand
// provider's role-classified central parks — exactly as the between-legs hook
// does via ResolveStandbyForHoming. Before the fix the sweep bailed on the empty
// fleet-hub set, so the pool piled where it last finished (the live J59 pile).

// fakeStandbyDemand stands in for the coordinator's role-park demand provider,
// resolving a per-central-park demand weight map (the sp-bu6ma auto-placement set).
type fakeStandbyDemand struct {
	demand map[string]float64
	err    error
}

func (f fakeStandbyDemand) StandbyDemand(context.Context, int) (map[string]float64, error) {
	return f.demand, f.err
}

// twoSinkDemand is the two-central-park demand map the sitting-pool tests home
// against — the auto-resolved set, sorted, is {D40, E42}.
func twoSinkDemand() fakeStandbyDemand {
	return fakeStandbyDemand{demand: map[string]float64{
		"X1-HUB-E42": 2.0,
		"X1-HUB-D40": 1.0,
	}}
}

// The whole sitting pool is homed off its off-station park even with NO hubs
// pinned: the sweep auto-resolves the standby set from the demand provider. The
// pool is ALL "reserved" (ReserveHulls above the fleet size, so not one hull is
// arb-eligible), proving the pile is homed on its own — not as an arb side effect.
func TestIdleArb_SittingReservedPool_HomedToDemandAutoResolvedSinks_WhenNoHubsPinned(t *testing.T) {
	j59 := idleArbWaypoint(t, "X1-UM5-J59", 0, 100) // off-station park (no market, no lane)
	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "HAULER-A", j59, testFleet),
		idleArbHull(t, "HAULER-B", j59, testFleet),
		idleArbHull(t, "HAULER-C", j59, testFleet),
	}}

	// nil standby = empty fleet-hub set; homing must come from the demand resolver.
	d, _, homer := idleArbRehomeHarness(t, repo, nil, IdleArbConfig{ReserveHulls: 10})
	d.SetStandbyDemandProvider(twoSinkDemand())

	d.DispatchOnce(context.Background())

	// Every sitting hull is homed, though none was arb-eligible (all reserved).
	homed := append([]string(nil), homer.homed...)
	sort.Strings(homed)
	if !reflect.DeepEqual(homed, []string{"HAULER-A", "HAULER-B", "HAULER-C"}) {
		t.Fatalf("the sitting idle pool must ALL be homed off J59 (empty = the live pile-up bug), got %v", homer.homed)
	}
	// ...to the demand auto-resolved multi-sink set, so HomeShipCommand can spread them.
	gotStandby := append([]string(nil), homer.lastStandby...)
	sort.Strings(gotStandby)
	if want := []string{"X1-HUB-D40", "X1-HUB-E42"}; !reflect.DeepEqual(gotStandby, want) {
		t.Fatalf("the homer must receive the demand auto-resolved sinks %v, got %v", want, homer.lastStandby)
	}
}

// No thrash (RULINGS #2): a hull already parked AT one of the demand
// auto-resolved sinks is left alone; only the genuinely off-station hull is homed.
func TestIdleArb_HullAtAutoResolvedSink_NotReHomed(t *testing.T) {
	atSink := idleArbWaypoint(t, "X1-HUB-D40", 0, 50) // a demand-resolved sink
	j59 := idleArbWaypoint(t, "X1-UM5-J59", 0, 100)   // off-station
	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "AT-SINK", atSink, testFleet),
		idleArbHull(t, "DRIFTED", j59, testFleet),
	}}

	d, _, homer := idleArbRehomeHarness(t, repo, nil, IdleArbConfig{ReserveHulls: 10})
	d.SetStandbyDemandProvider(twoSinkDemand())

	d.DispatchOnce(context.Background())

	if len(homer.homed) != 1 || homer.homed[0] != "DRIFTED" {
		t.Fatalf("only the off-station hull must home; a hull already at an auto-resolved sink must not thrash, got %v", homer.homed)
	}
}

// RULINGS #7: the command frigate is never swept by the standing re-home, even
// when it is contract-dedicated and idle off-station — its positioning is managed
// by the last-resort draft, not the idle harvest. A regular hauler beside it homes.
func TestIdleArb_CommandFrigate_ExcludedFromStandingReHome(t *testing.T) {
	j59 := idleArbWaypoint(t, "X1-UM5-J59", 0, 100)
	repo := &idleArbFakeShipRepo{ships: []*navigation.Ship{
		idleArbHull(t, "HAULER-A", j59, testFleet),
		idleArbHull(t, "FRIGATE-1", j59, testFleet), // "-1" suffix => IsCommandHull
	}}

	d, _, homer := idleArbRehomeHarness(t, repo, nil, IdleArbConfig{ReserveHulls: 10})
	d.SetStandbyDemandProvider(twoSinkDemand())

	d.DispatchOnce(context.Background())

	if len(homer.homed) != 1 || homer.homed[0] != "HAULER-A" {
		t.Fatalf("the command frigate must be excluded from the standing re-home (RULINGS #7); only the hauler homes, got %v", homer.homed)
	}
}

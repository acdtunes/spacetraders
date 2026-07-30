package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
)

// sp-k4z5b: the trade path's freshness caps are DERIVED from the live scan rotation and
// floored by a live-tunable operator knob, instead of being minute counts written into
// the source that silently invalidate as the charted map grows.

// The live numbers from the incident: 4,389 markets sharing a fixed 0.70 req/s at the
// standing clamp of 8. An even rotation is ~105 minutes and the anti-starvation bound is
// ~13h56m, so the 75-minute caps were refusing four fifths of a healthy map.
const (
	testIncidentMarkets = 4389
	testIncidentRate    = 0.70
	testIncidentClampR  = 8
)

type fakeRotationSource struct {
	budget       marketscan.Budget
	marketsKnown int
}

func (f fakeRotationSource) RotationInputs(_ context.Context) (marketscan.Budget, int) {
	return f.budget, f.marketsKnown
}

func incidentRotation() fakeRotationSource {
	return fakeRotationSource{
		budget:       marketscan.Budget{RateReqPerSec: testIncidentRate, ValueClampR: testIncidentClampR},
		marketsKnown: testIncidentMarkets,
	}
}

type fakeFloorSource struct {
	config liveconfig.Snapshot
	err    error
	reads  int
}

func (f *fakeFloorSource) Snapshot(_ context.Context, _ int) (liveconfig.Snapshot, error) {
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	return f.config, nil
}

// A handler nobody wired reports its own floor unchanged — the optional-port contract
// every pre-existing test in this package depends on.
func TestMarketFreshness_NilResolverReportsTheBootFloor(t *testing.T) {
	var f *MarketFreshness
	require.Equal(t, listingAgeFloor, f.Cap(context.Background(), 1, TuneKeyListingMaxAgeMinutes, listingAgeFloor))
	require.Equal(t, time.Duration(0), f.RotationBound(context.Background()))
}

// THE FIX. With the rotation wired at the incident's map size, the effective cap is the
// rotation bound, not the 75-minute floor — so a row the rotation explains is admitted.
func TestMarketFreshness_RotationBoundDominatesTheFloor(t *testing.T) {
	f := NewMarketFreshness(incidentRotation(), nil, nil)

	cap := f.Cap(context.Background(), 1, TuneKeyListingMaxAgeMinutes, listingAgeFloor)
	require.Greater(t, cap, listingAgeFloor,
		"the cap must widen past the 75-minute floor at 4,389 markets — that floor is what cost 87%% of throughput")
	require.Equal(t, f.RotationBound(context.Background()), cap, "with a small floor the cap IS the rotation bound")
	require.Greater(t, cap, 2*time.Hour,
		"a two-hour-old row is one rotation, not dead data, and must be inside the cap")
}

// The operator's lever works, live, in the direction an incident needs: raising the floor
// above the rotation bound widens the cap without a restart.
func TestMarketFreshness_TunedFloorAboveTheBoundWidensTheCap(t *testing.T) {
	floors := &fakeFloorSource{config: liveconfig.Snapshot{TuneKeyListingMaxAgeMinutes: 6000}}
	f := NewMarketFreshness(incidentRotation(), floors, nil)

	require.Equal(t, 6000*time.Minute, f.Cap(context.Background(), 1, TuneKeyListingMaxAgeMinutes, listingAgeFloor))
	require.Positive(t, floors.reads, "the floor must be READ from the live config column, not launch-frozen")
}

// And is a deliberate no-op in the direction that would re-create the incident: a floor
// tuned below the rotation bound cannot make a consumer refuse rotation-explained rows.
func TestMarketFreshness_TunedFloorBelowTheBoundCannotTightenTheCap(t *testing.T) {
	f := NewMarketFreshness(incidentRotation(), &fakeFloorSource{
		config: liveconfig.Snapshot{TuneKeyListingMaxAgeMinutes: 1},
	}, nil)

	require.Equal(t, f.RotationBound(context.Background()), f.Cap(context.Background(), 1, TuneKeyListingMaxAgeMinutes, listingAgeFloor),
		"a 1-minute tune must not tighten the cap below what the rotation can deliver")
}

// RULINGS #4. The firm-sink clause is a fail-closed money guard, and no tune may disarm
// it: `tune ... 0` is the revert-to-default verb, and every reachable floor still resolves
// to a POSITIVE cap, which is what keeps the clause armed at absorption.go's `<= 0` test.
func TestMarketFreshness_NoTuneCanDisarmTheSinkGuard(t *testing.T) {
	boot := 75 * time.Minute
	for _, tuned := range []int{0, 1, 75, 720, 43_200} {
		f := NewMarketFreshness(incidentRotation(), &fakeFloorSource{
			config: liveconfig.Snapshot{TuneKeySinkFreshnessMaxMinutes: tuned},
		}, nil)
		cap := f.Cap(context.Background(), 1, TuneKeySinkFreshnessMaxMinutes, boot)
		require.Positive(t, cap, "tune value %d produced a non-positive cap — the money guard would go INERT", tuned)
		require.GreaterOrEqual(t, cap, f.RotationBound(context.Background()), "tune value %d tightened below the rotation bound", tuned)
	}
}

// Arming stays a BOOT concern. A non-positive boot floor means the daemon left the clause
// inert (the test path, and the pre-sp-tgll8 behaviour), and no rotation and no tune may
// silently turn it on behind the operator's back.
func TestMarketFreshness_InertClauseIsNeverArmedByDerivation(t *testing.T) {
	f := NewMarketFreshness(incidentRotation(), &fakeFloorSource{
		config: liveconfig.Snapshot{TuneKeySinkFreshnessMaxMinutes: 900},
	}, nil)

	require.Equal(t, time.Duration(0), f.Cap(context.Background(), 1, TuneKeySinkFreshnessMaxMinutes, 0),
		"an unarmed clause must stay unarmed — arming is a boot concern")
}

// A database hiccup must not silently collapse a freshly-tuned floor back to its default
// mid-incident, which is exactly when an operator is most likely to have just tuned one.
func TestMarketFreshness_FailedReadReusesTheLastKnownFloor(t *testing.T) {
	clock := &freshnessTestClock{now: time.Now()}
	floors := &fakeFloorSource{config: liveconfig.Snapshot{TuneKeyListingMaxAgeMinutes: 6000}}
	f := NewMarketFreshness(incidentRotation(), floors, clock)

	require.Equal(t, 6000*time.Minute, f.Cap(context.Background(), 1, TuneKeyListingMaxAgeMinutes, listingAgeFloor))

	floors.err = errors.New("database unavailable")
	clock.now = clock.now.Add(freshnessSnapshotTTL + time.Second)
	require.Equal(t, 6000*time.Minute, f.Cap(context.Background(), 1, TuneKeyListingMaxAgeMinutes, listingAgeFloor),
		"a failed read must reuse the last known floor, not silently revert it")
}

// The floor is cached for a bounded window so the freshListings paths — which resolve a
// cap inside loops over candidate systems — do not put a query on the tour's hot path,
// and picked up again immediately after it, so the knob is honestly "next tick".
func TestMarketFreshness_FloorIsCachedThenRefreshed(t *testing.T) {
	clock := &freshnessTestClock{now: time.Now()}
	floors := &fakeFloorSource{config: liveconfig.Snapshot{TuneKeyListingMaxAgeMinutes: 100}}
	f := NewMarketFreshness(incidentRotation(), floors, clock)

	for i := 0; i < 5; i++ {
		f.Cap(context.Background(), 1, TuneKeyListingMaxAgeMinutes, listingAgeFloor)
	}
	require.Equal(t, 1, floors.reads, "repeated resolves inside one window must not re-query")

	floors.config = liveconfig.Snapshot{TuneKeyListingMaxAgeMinutes: 6000}
	clock.now = clock.now.Add(freshnessSnapshotTTL + time.Second)
	require.Equal(t, 6000*time.Minute, f.Cap(context.Background(), 1, TuneKeyListingMaxAgeMinutes, listingAgeFloor),
		"a tune must land without a restart once the window elapses")
	require.Equal(t, 2, floors.reads)
}

// The registry's documented defaults are the consts the resolver actually falls back to.
func TestTradeFleetTunableDefaults_MirrorTheResolvedFloors(t *testing.T) {
	defaults := TradeFleetTunableDefaults()
	require.Equal(t, int(listingAgeFloor.Minutes()), defaults[TuneKeyListingMaxAgeMinutes])
	require.Equal(t, defaultSinkFreshnessFloorMinutes, defaults[TuneKeySinkFreshnessMaxMinutes])
	require.Len(t, defaults, 2, "a new trade-fleet knob needs a registry entry and a bounds review")
}

type freshnessTestClock struct{ now time.Time }

func (c *freshnessTestClock) Now() time.Time        { return c.now }
func (c *freshnessTestClock) Sleep(_ time.Duration) {}

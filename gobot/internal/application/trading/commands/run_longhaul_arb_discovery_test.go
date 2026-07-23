package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// ---- discoverer composition fakes ------------------------------------------

type fakeGoodUniverse struct {
	goods []string
	err   error
}

func (f *fakeGoodUniverse) DistinctTradedGoods(_ context.Context, _ int, _ time.Duration, _ time.Time) ([]string, error) {
	return f.goods, f.err
}

type fakeSinkScanner struct {
	sinks map[string]market.GlobalSinkResult
	err   error
}

func (f *fakeSinkScanner) BestSinksAcrossSystems(_ context.Context, _ []string, _ int, _ time.Duration, _ time.Time) (map[string]market.GlobalSinkResult, error) {
	return f.sinks, f.err
}

type fakeSourceScanner struct {
	sources map[string]market.GlobalSourceResult
	err     error
}

func (f *fakeSourceScanner) BestSourcesAcrossSystems(_ context.Context, _ []string, _ int, _ time.Duration, _ time.Time) (map[string]market.GlobalSourceResult, error) {
	return f.sources, f.err
}

// fakeGateGraphPathfinder returns a path of hopCount+1 systems (routable) for any distinct
// pair — the length is all the discoverer's hop closure reads.
type fakeGateGraphPathfinder struct{ hopCount int }

func (f *fakeGateGraphPathfinder) Path(_ context.Context, _, _ string, _ int) ([]string, error) {
	return make([]string, f.hopCount+1), nil
}

// DISCOVERY ASSEMBLY (design §2): pairing each good's best cross-system sink with its
// cheapest cross-system source yields directed lanes, but ONLY the out-of-horizon, routable,
// positive-spread ones survive — a same-system pair belongs to the tour's 1-hop horizon, a
// good with no source can't be bought, an unroutable pair can't be delivered, and a
// non-positive spread isn't a lane. VolumeCap is the min of the two sides' trade_volume.
func TestLongHaulDiscovery_AssemblesOnlyOutOfHorizonRoutablePositiveLanes(t *testing.T) {
	sinks := map[string]market.GlobalSinkResult{
		"LASER_RIFLES":    {WaypointSymbol: "X1-XD86-A1", SystemSymbol: "X1-XD86", Bid: 18000, TradeVolume: 20},
		"SAME_SYSTEM":     {WaypointSymbol: "X1-HOME-B2", SystemSymbol: "X1-HOME", Bid: 9000, TradeVolume: 60},
		"NO_SOURCE":       {WaypointSymbol: "X1-YN88-A1", SystemSymbol: "X1-YN88", Bid: 11000, TradeVolume: 40},
		"UNROUTABLE":      {WaypointSymbol: "X1-DEEP-A1", SystemSymbol: "X1-DEEP", Bid: 30000, TradeVolume: 10},
		"NEGATIVE_SPREAD": {WaypointSymbol: "X1-KA42-A1", SystemSymbol: "X1-KA42", Bid: 5000, TradeVolume: 30},
	}
	sources := map[string]market.GlobalSourceResult{
		"LASER_RIFLES":    {WaypointSymbol: "X1-ZC66-AX1B", SystemSymbol: "X1-ZC66", Ask: 2000, TradeVolume: 30},
		"SAME_SYSTEM":     {WaypointSymbol: "X1-HOME-A1", SystemSymbol: "X1-HOME", Ask: 4000, TradeVolume: 60},
		"UNROUTABLE":      {WaypointSymbol: "X1-FAR-A1", SystemSymbol: "X1-FAR", Ask: 6000, TradeVolume: 10},
		"NEGATIVE_SPREAD": {WaypointSymbol: "X1-SRC-A1", SystemSymbol: "X1-SRC", Ask: 7000, TradeVolume: 30},
		// NO_SOURCE deliberately has a sink but no source.
	}
	hops := func(from, _ string) (int, bool) {
		if from == "X1-FAR" {
			return 0, false // the UNROUTABLE lane's source system reaches nothing
		}
		return 3, true
	}

	got := assembleLongHaulCandidates(sinks, sources, hops, nil)

	require.Len(t, got, 1, "only the routable, cross-system, positive-spread lane survives")
	c := got[0]
	require.Equal(t, "LASER_RIFLES", c.Lane.Good)
	require.Equal(t, "X1-ZC66-AX1B", c.Lane.SourceWaypoint)
	require.Equal(t, "X1-XD86-A1", c.Lane.DestWaypoint)
	require.Equal(t, 2000, c.Lane.SourceAsk)
	require.Equal(t, 18000, c.Lane.DestBid)
	require.Equal(t, 16000, c.Lane.SpreadPerUnit)
	require.Equal(t, 20, c.Lane.VolumeCap, "min(source tv 30, sink tv 20)")
	require.Equal(t, 3, c.GateHops)
}

// DISCOVERY COMPOSITION (design §2): longHaulDiscoverer chains the goods universe -> the shared
// BestSinks/BestSources scanners -> assembleLongHaulCandidates -> rankLongHaulLanes into the
// worker's one laneDiscoverer call, keeping only the out-of-horizon lanes.
func TestLongHaulDiscoverer_ComposesUniverseScannersAssembleRank(t *testing.T) {
	universe := &fakeGoodUniverse{goods: []string{"LASER_RIFLES", "SAME_SYS"}}
	sinks := &fakeSinkScanner{sinks: map[string]market.GlobalSinkResult{
		"LASER_RIFLES": {WaypointSymbol: "X1-XD86-A1", SystemSymbol: "X1-XD86", Bid: 18000, TradeVolume: 20},
		"SAME_SYS":     {WaypointSymbol: "X1-HOME-B2", SystemSymbol: "X1-HOME", Bid: 9000, TradeVolume: 60},
	}}
	sources := &fakeSourceScanner{sources: map[string]market.GlobalSourceResult{
		"LASER_RIFLES": {WaypointSymbol: "X1-ZC66-AX1B", SystemSymbol: "X1-ZC66", Ask: 2000, TradeVolume: 30},
		"SAME_SYS":     {WaypointSymbol: "X1-HOME-A1", SystemSymbol: "X1-HOME", Ask: 4000, TradeVolume: 60},
	}}
	d := &longHaulDiscoverer{
		universe: universe, sinks: sinks, sources: sources,
		gateGraph: &fakeGateGraphPathfinder{hopCount: 3},
		model:     longHaulTestModel(), clock: clockAt(0), maxAge: time.Hour, minSpread: 100, marginFloor: 0,
	}

	ranked, err := d.DiscoverLanes(context.Background(), 1)

	require.NoError(t, err)
	require.Len(t, ranked, 1, "only the cross-system LASER_RIFLES lane survives (SAME_SYS is same-system)")
	require.Equal(t, "LASER_RIFLES", ranked[0].Lane.Good)
	require.Equal(t, 16000, ranked[0].Lane.SpreadPerUnit)
	require.Positive(t, ranked[0].OptimalUnits)
}

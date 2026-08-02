package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
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

// fakeGateGraphPathfinder stands in for the shared gategraph.Service across BOTH resolvers the
// discoverer could use for its hop estimate: Path is the STRICT fetch-through (a per-system live
// fetch — the ~10-min cold-cache first-episode stall, sp-yginc) and RepositionPath is the FAST
// stored-adjacency read the estimate must ride instead. Each records a call count so a test can
// assert WHICH resolver ran. With no repositionPaths map both resolvers return a routable
// hopCount+1 path, so resolver-agnostic composition tests keep passing; a repositionPaths map
// serves a per-pair stored path ("from|to" key; a missing pair => nil => unroutable) so a test can
// rank a far lane and drop an unreachable one. pathErr makes the strict fetch-through FAIL,
// modelling the cold-cache pathology the fix routes around.
type fakeGateGraphPathfinder struct {
	hopCount        int
	pathErr         error
	repositionPaths map[string][]string
	pathCalls       int
	repositionCalls int
}

func (f *fakeGateGraphPathfinder) Path(_ context.Context, _, _ string, _ int) ([]string, error) {
	f.pathCalls++
	if f.pathErr != nil {
		return nil, f.pathErr
	}
	return make([]string, f.hopCount+1), nil
}

func (f *fakeGateGraphPathfinder) RepositionPath(_ context.Context, fromSystem, toSystem string, _ int) ([]string, error) {
	f.repositionCalls++
	if f.repositionPaths != nil {
		return f.repositionPaths[fromSystem+"|"+toSystem], nil
	}
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

// COLD-CACHE DISCOVERY LATENCY (headline): the discovery hop estimate must ride the
// FAST stored-adjacency resolver (RepositionPath), NEVER the strict fetch-through Path — on a cold
// gate-graph cache Path fetches gate data per traversed system across every candidate lane, the
// measured ~10-min first-episode stall. Modelled by a pathfinder whose strict Path FAILS (the
// cold-cache pathology) while its RepositionPath serves the stored route: the lane still ranks
// (RepositionPath used) and Path is never called (call-count 0). RED before the switch: hops() calls
// Path -> the error drops the only lane -> ranked is empty and pathCalls == 1.
func TestLongHaulDiscoverer_RanksViaStoredAdjacency_NeverStrictFetchThrough(t *testing.T) {
	universe := &fakeGoodUniverse{goods: []string{"LASER_RIFLES"}}
	sinks := &fakeSinkScanner{sinks: map[string]market.GlobalSinkResult{
		"LASER_RIFLES": {WaypointSymbol: "X1-XD86-A1", SystemSymbol: "X1-XD86", Bid: 18000, TradeVolume: 20},
	}}
	sources := &fakeSourceScanner{sources: map[string]market.GlobalSourceResult{
		"LASER_RIFLES": {WaypointSymbol: "X1-ZC66-AX1B", SystemSymbol: "X1-ZC66", Ask: 2000, TradeVolume: 30},
	}}
	graph := &fakeGateGraphPathfinder{
		pathErr:         errors.New("cold-cache fetch-through storm"),
		repositionPaths: map[string][]string{"X1-ZC66|X1-XD86": make([]string, 4)}, // 3 stored hops
	}
	d := &longHaulDiscoverer{
		universe: universe, sinks: sinks, sources: sources,
		gateGraph: graph,
		model:     longHaulTestModel(), clock: clockAt(0), maxAge: time.Hour, minSpread: 100, marginFloor: 0,
	}

	ranked, err := d.DiscoverLanes(context.Background(), 1)

	require.NoError(t, err)
	require.Len(t, ranked, 1, "the lane ranks via the stored-adjacency resolver even though the strict fetch-through fails")
	require.Equal(t, "LASER_RIFLES", ranked[0].Lane.Good)
	require.Equal(t, 3, ranked[0].GateHops, "hop count comes from the stored-adjacency path length")
	require.Zero(t, graph.pathCalls, "strict fetch-through Path is NEVER called by discovery (the cold-cache latency fix)")
	require.Positive(t, graph.repositionCalls, "discovery ranks via RepositionPath (stored adjacency)")
}

// STORED-ADJACENCY HOP ESTIMATE: discovery ranks a FAR (>5-hop, beyond MaxJumpPath)
// lane with the hop count read from stored adjacency — a heavy reaches it via the large reposition
// bound — and DROPS a lane with no stored-adjacency path within bound. The (hops,routable) contract
// the strict resolver honored is unchanged; only the resolver behind it is now the fast store read.
func TestLongHaulDiscoverer_RanksFarLane_DropsUnreachableByStoredAdjacency(t *testing.T) {
	universe := &fakeGoodUniverse{goods: []string{"FAR_EXOTIC", "ISOLATED"}}
	sinks := &fakeSinkScanner{sinks: map[string]market.GlobalSinkResult{
		"FAR_EXOTIC": {WaypointSymbol: "X1-DEST-A1", SystemSymbol: "X1-DEST", Bid: 40000, TradeVolume: 30},
		"ISOLATED":   {WaypointSymbol: "X1-VOID-A1", SystemSymbol: "X1-VOID", Bid: 22000, TradeVolume: 30},
	}}
	sources := &fakeSourceScanner{sources: map[string]market.GlobalSourceResult{
		"FAR_EXOTIC": {WaypointSymbol: "X1-FAR-A1", SystemSymbol: "X1-FAR", Ask: 3000, TradeVolume: 30},
		"ISOLATED":   {WaypointSymbol: "X1-ISO-A1", SystemSymbol: "X1-ISO", Ask: 5000, TradeVolume: 30},
	}}
	// FAR_EXOTIC's source->sink is 8 stored hops (len 9), far beyond MaxJumpPath=5; ISOLATED's pair
	// has NO stored path (missing key => nil => unroutable), so it drops despite a positive spread.
	graph := &fakeGateGraphPathfinder{repositionPaths: map[string][]string{
		"X1-FAR|X1-DEST": make([]string, 9),
	}}
	d := &longHaulDiscoverer{
		universe: universe, sinks: sinks, sources: sources,
		gateGraph: graph,
		model:     longHaulTestModel(), clock: clockAt(0), maxAge: time.Hour, minSpread: 100, marginFloor: 0,
	}

	ranked, err := d.DiscoverLanes(context.Background(), 1)

	require.NoError(t, err)
	require.Len(t, ranked, 1, "only the reachable far lane survives; the no-stored-path lane drops")
	require.Equal(t, "FAR_EXOTIC", ranked[0].Lane.Good)
	require.Equal(t, 8, ranked[0].GateHops, "far lane ranks with the stored-adjacency hop count (>5, beyond the strict MaxJumpPath)")
}

// ISOLATION: the cold-cache discovery fix switches ONLY discovery's RANKING resolver.
// The strict fetch-through reach cap the reposition rides is byte-untouched — MaxJumpPath stays 5,
// and the large stored-adjacency bound discovery/reposition ride stays longHaulRepositionJumps=25.
// A regression lock on the two constants the DO-NOT-TOUCH sp-e059j reposition path depends on.
func TestLongHaulDiscovery_StrictReachCapUnchanged(t *testing.T) {
	require.Equal(t, 5, gategraph.MaxJumpPath, "the strict fetch-through reach cap stays 5 — discovery's fast resolver does not touch it")
	require.Equal(t, 25, longHaulRepositionJumps, "the large reach bound stays 25 (sp-e059j reposition bound, reused by discovery)")
}

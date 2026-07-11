package commands

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// laserSnap is the replay-shaped in-scope snapshot: LASER_RIFLES sourceable at the ZC66
// export (Ask = live sell_price 16,549) with the ship's tour graph rooted at X1-ZC66.
// The rich sink (XT71-A1, bid 30,627) lives in X1-XT71, which is NOT gate-adjacent to
// ZC66 — the exact blind-spot geometry pinned by the live replay.
func laserSnap() []routing.TourGoodSnapshot {
	return []routing.TourGoodSnapshot{
		{Waypoint: "X1-ZC66-AX1B", System: "X1-ZC66", Good: "LASER_RIFLES", Ask: 16549, Bid: 0, TradeVolume: 30},
		// a routine in-scope good with an in-scope sink, present so the detector must
		// discriminate rather than flag everything.
		{Waypoint: "X1-ZC66-F12F", System: "X1-ZC66", Good: "IRON_ORE", Ask: 40, Bid: 55, TradeVolume: 60},
	}
}

var zc66TourGraph = []string{"X1-ZC66", "X1-PA3", "X1-UQ87", "X1-UU57"}

// TestComputeUnreachableLanes_FlagsOutOfHorizonExoticSink is the replay-shaped RED-first
// test: the LASER_RIFLES lane (real prices) is flagged as a dropped candidate because its
// best sink sits in X1-XT71, beyond the ZC66 gate-neighbor graph. The routine IRON_ORE
// good, whose sink is not out-of-graph, is not flagged.
func TestComputeUnreachableLanes_FlagsOutOfHorizonExoticSink(t *testing.T) {
	sinks := map[string]market.GlobalSinkResult{
		"LASER_RIFLES": {WaypointSymbol: "X1-XT71-A1", SystemSymbol: "X1-XT71", Bid: 30627},
		// IRON_ORE's best sink is IN the tour graph, so it is reachable — must NOT flag.
		"IRON_ORE": {WaypointSymbol: "X1-ZC66-F12F", SystemSymbol: "X1-ZC66", Bid: 55},
	}
	got := computeUnreachableLanes(zc66TourGraph, laserSnap(), sinks)
	if len(got) != 1 {
		t.Fatalf("flagged %d lanes, want exactly 1 (LASER_RIFLES): %+v", len(got), got)
	}
	l := got[0]
	if l.Good != "LASER_RIFLES" || l.SinkSystem != "X1-XT71" || l.SourceWaypoint != "X1-ZC66-AX1B" {
		t.Errorf("lane = %+v, want LASER_RIFLES ZC66-AX1B->X1-XT71", l)
	}
	if l.Ask != 16549 || l.Bid != 30627 || l.Spread != 14078 {
		t.Errorf("prices = ask %d bid %d spread %d, want 16549/30627/14078", l.Ask, l.Bid, l.Spread)
	}
}

// TestComputeUnreachableLanes_InScopeSinkNotFlagged is the guard the horizon protects: a
// lane whose best sink is already IN the tour graph must NOT be flagged — the diagnostic
// only surfaces genuinely out-of-horizon value, never cries wolf on lanes the tour can do.
func TestComputeUnreachableLanes_InScopeSinkNotFlagged(t *testing.T) {
	sinks := map[string]market.GlobalSinkResult{
		// LASER_RIFLES best sink is in X1-UU57, a gate neighbor in the tour graph.
		"LASER_RIFLES": {WaypointSymbol: "X1-UU57-Z9", SystemSymbol: "X1-UU57", Bid: 30627},
	}
	if got := computeUnreachableLanes(zc66TourGraph, laserSnap(), sinks); len(got) != 0 {
		t.Errorf("flagged %d lanes, want 0 (sink is reachable in-graph): %+v", len(got), got)
	}
}

// TestComputeUnreachableLanes_SubThresholdNotFlagged: an out-of-graph sink whose spread is
// below the materiality floor is noise, not a missed exotic lane, and must not be counted.
func TestComputeUnreachableLanes_SubThresholdNotFlagged(t *testing.T) {
	sinks := map[string]market.GlobalSinkResult{
		// out-of-graph, but spread = 20000-16549 = 3451 < 5000 floor.
		"LASER_RIFLES": {WaypointSymbol: "X1-XT71-A1", SystemSymbol: "X1-XT71", Bid: 20000},
	}
	if got := computeUnreachableLanes(zc66TourGraph, laserSnap(), sinks); len(got) != 0 {
		t.Errorf("flagged %d lanes, want 0 (spread below floor): %+v", len(got), got)
	}
}

// TestComputeUnreachableLanes_NoInScopeSourceNotFlagged: a good present only as a sink in
// the snapshot (no Ask>0 anywhere in-graph) can't be sourced by the hull, so an out-of-
// graph sink for it is not a lane THIS graph is missing — it must not be flagged.
func TestComputeUnreachableLanes_NoInScopeSourceNotFlagged(t *testing.T) {
	snap := []routing.TourGoodSnapshot{
		{Waypoint: "X1-ZC66-A1", System: "X1-ZC66", Good: "HOLOGRAPHICS", Ask: 0, Bid: 35880, TradeVolume: 6},
	}
	sinks := map[string]market.GlobalSinkResult{
		"HOLOGRAPHICS": {WaypointSymbol: "X1-HU21-A1", SystemSymbol: "X1-HU21", Bid: 71956},
	}
	if got := computeUnreachableLanes(zc66TourGraph, snap, sinks); len(got) != 0 {
		t.Errorf("flagged %d lanes, want 0 (good not sourceable in-graph): %+v", len(got), got)
	}
}

// gatherTourCandidatesDropped reads the counter value for the given reason off a registry.
func gatherTourCandidatesDropped(t *testing.T, reg *prometheus.Registry, reason string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "spacetraders_daemon_tour_candidates_dropped_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "reason" && l.GetValue() == reason {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// TestRecordUnreachableLanes_IncrementsCounter is the end-to-end observability assertion:
// the coordinator's diagnostic, given a hand-fed out-of-horizon sink, increments
// tour_candidates_dropped_total{reason=counterparty_system_unreachable} by the number of
// dropped lanes on the daemon registry (the 8cz9 register+Gather shape).
func TestRecordUnreachableLanes_IncrementsCounter(t *testing.T) {
	prev := metrics.Registry
	t.Cleanup(func() { metrics.Registry = prev })
	metrics.Registry = prometheus.NewRegistry()
	c := metrics.NewTourStalenessMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}
	metrics.SetGlobalTourStalenessCollector(c)

	h := &RunTourCoordinatorHandler{
		clock: shared.NewRealClock(),
		sinkScanner: &stubScanner{sinks: map[string]market.GlobalSinkResult{
			"LASER_RIFLES": {WaypointSymbol: "X1-XT71-A1", SystemSymbol: "X1-XT71", Bid: 30627},
		}},
	}
	h.recordUnreachableLanes(context.Background(), zc66TourGraph, laserSnap(), 2)

	if got := gatherTourCandidatesDropped(t, metrics.Registry, unreachableLaneReason); got != 1 {
		t.Errorf("counter = %v, want 1", got)
	}
}

// TestRecordUnreachableLanes_NilScannerNoop: with no scanner wired (tests / metrics-off),
// the diagnostic is a no-op and never panics — observation never gates the tour path.
func TestRecordUnreachableLanes_NilScannerNoop(t *testing.T) {
	h := &RunTourCoordinatorHandler{clock: shared.NewRealClock()}
	h.recordUnreachableLanes(context.Background(), zc66TourGraph, laserSnap(), 2)
}

// stubScanner is a hand-fed outOfHorizonSinkScanner double: it returns a fixed global
// best-sink map, standing in for the daemon's cross-system market read.
type stubScanner struct {
	sinks map[string]market.GlobalSinkResult
	err   error
}

func (s *stubScanner) BestSinksAcrossSystems(_ context.Context, _ []string, _ int, _ time.Duration, _ time.Time) (map[string]market.GlobalSinkResult, error) {
	return s.sinks, s.err
}

package commands

// run_tour_coordinator_reposition_reach_test.go — sp-uf64: the reposition-reach improvement.
// buildRepositionCandidates was 1-hop-FIRST: it broadened to the multi-hop scan ONLY when the
// 1-hop set was empty (the sp-jeou off-circuit gate), so a hull with ANY fresh-market 1-hop
// neighbour — even a money-losing one — never saw richer systems 2-4 gate hops away. Behind the
// default-OFF RepositionReachEnabled flag the scan now (1) ALWAYS broadens and merges, (2) RANKS
// with a per-hop deadhead decay so a rich distant ground wins only when its spread beats the
// travel penalty, and (3) EXCLUDES systems already saturated with active trade hulls (anti-herd).
// Every assertion is on the OBSERVABLE candidate set buildRepositionCandidates returns — the same
// in-package seam TestReposition_EmptyDiscovery_LogsPerNeighborReason and the stranded suite drive.

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// reachFixture models the live X1-UF64 strand: the origin X1-ORIG is a dead ground, its DIRECT
// 1-gate-hop neighbour X1-NEAR carries a mediocre in-system arb lane (capped spread = nearScore),
// and a RICHER ground X1-FAR sits THREE gate hops out (capped spread = farScore) via two barren
// interior hops X1-H1, X1-H2. A system's capped spread is (bid−ask)·volume; with volume pinned at
// 10 and ask at 100, a score of v is modelled by an IMPORT sink priced 100+v/10 (so scores must be
// multiples of 10). The LIVE gate scan is barren (uncharted-origin shape); the DURABLE gate graph
// (reachGateGraph) is what exposes the multi-hop route, exactly as production wires it.
func reachFixture(nearScore, farScore int) *tourFixture {
	nearSink := 100 + nearScore/10
	farSink := 100 + farScore/10
	return &tourFixture{
		cargo: map[string]int{}, location: "X1-ORIG-A", cargoCap: 100,
		markets: map[string][]string{
			"X1-NEAR": {"X1-NEAR-A", "X1-NEAR-B"},
			"X1-FAR":  {"X1-FAR-A", "X1-FAR-B"},
		},
		ask: map[string]map[string]int{
			"X1-NEAR-A": {"G": 100}, "X1-NEAR-B": {"G": nearSink},
			"X1-FAR-A": {"K": 100}, "X1-FAR-B": {"K": farSink},
		},
		bid: map[string]map[string]int{
			"X1-NEAR-B": {"G": nearSink},
			"X1-FAR-B":  {"K": farSink},
		},
		tv: map[string]map[string]int{
			"X1-NEAR-A": {"G": 10}, "X1-NEAR-B": {"G": 10},
			"X1-FAR-A": {"K": 10}, "X1-FAR-B": {"K": 10},
		},
		// The sink waypoint of each system must be an IMPORT (not the EXPORT default), else
		// bestLaneForGood (sp-9mkf) refuses to sell into it and the system scores a bare 0.
		tradeType: map[string]map[string]string{
			"X1-NEAR-B": {"G": "IMPORT"},
			"X1-FAR-B":  {"K": "IMPORT"},
		},
		neighbors: map[string][]string{}, // live scan barren — durable graph drives discovery
	}
}

// reachGateGraph is the durable era-scoped adjacency for reachFixture: X1-ORIG gates directly to
// X1-NEAR (1 hop) and the barren interior X1-H1 (1 hop); X1-H1→X1-H2 (2 hops); X1-H2→X1-FAR (3
// hops). NEAR and FAR have no onward edge, so the BFS surfaces exactly {NEAR@1, H1@1, H2@2, FAR@3}.
func reachGateGraph() *fakeGateGraph {
	return &fakeGateGraph{edges: map[string][]system.GateEdge{
		"X1-ORIG": {{ConnectedSystem: "X1-NEAR", GateWaypoint: "X1-NEAR-GATE"}, {ConnectedSystem: "X1-H1", GateWaypoint: "X1-H1-GATE"}},
		"X1-H1":   {{ConnectedSystem: "X1-H2", GateWaypoint: "X1-H2-GATE"}},
		"X1-H2":   {{ConnectedSystem: "X1-FAR", GateWaypoint: "X1-FAR-GATE"}},
	}}
}

func reachCandidateSystems(cands []repositionCandidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.system
	}
	return out
}

func reachHasSystem(cands []repositionCandidate, sys string) bool {
	for _, c := range cands {
		if c.system == sys {
			return true
		}
	}
	return false
}

// THE always-broaden GATE. With the flag OFF the scan is 1-hop-first: the mediocre X1-NEAR is the
// only candidate and the richer 3-hop X1-FAR is never discovered (byte-identical to today). With
// the flag ON discovery ALWAYS broadens, so X1-FAR becomes a candidate ALONGSIDE the 1-hop NEAR.
// This pins the governance gate — the whole change is inert until RepositionReachEnabled is set.
func TestRepositionReach_Discovery_BroadensOnlyWhenArmed(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		wantFar bool
	}{
		{"flag OFF: 1-hop-first, the distant ground is never discovered", false, false},
		{"flag ON: always broadens, the distant ground becomes a candidate", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := reachFixture(1000, 3000)
			h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})
			h.SetGateGraph(reachGateGraph())
			ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
			cmd := &RunTourCoordinatorCommand{ShipSymbol: "REACH-DISC", PlayerID: 1, RepositionReachEnabled: tc.enabled}

			cands := h.buildRepositionCandidates(ctx, cmd, "X1-ORIG")

			if got := reachHasSystem(cands, "X1-FAR"); got != tc.wantFar {
				t.Fatalf("distant X1-FAR present=%v, want %v (candidates=%v)", got, tc.wantFar, reachCandidateSystems(cands))
			}
			if !reachHasSystem(cands, "X1-NEAR") {
				t.Fatalf("the 1-hop neighbour X1-NEAR must be a candidate in BOTH modes, got %v", reachCandidateSystems(cands))
			}
			if !tc.enabled && len(cands) != 1 {
				t.Fatalf("flag OFF must yield ONLY the 1-hop candidate (no broadening), got %v", reachCandidateSystems(cands))
			}
		})
	}
}

// THE deadhead-decay RANKING. Armed, both grounds are discovered; the per-hop decay (0.85/hop by
// default) decides the ORDER. NEAR (1 hop) is charged 0.85, FAR (3 hops) 0.85^3≈0.614 — so FAR's
// raw spread must exceed NEAR's by ~1.384x to outrank it. A 3x-richer distant ground wins; a
// merely-1.2x-better one loses to the travel penalty and NEAR stays first (no over-flying). The
// not-win case is the falsifiable proof the decay is real: on a raw-score sort FAR (1200) would
// wrongly outrank NEAR (1000).
func TestRepositionReach_DeadheadDecay_RanksDistantOnlyWhenSpreadBeatsDecay(t *testing.T) {
	cases := []struct {
		name      string
		nearScore int
		farScore  int
		wantFirst string
	}{
		{"rich distant (3x spread) overcomes the hop decay and wins", 1000, 3000, "X1-FAR"},
		{"marginally-better distant (1.2x spread) loses to the decay, near wins", 1000, 1200, "X1-NEAR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := reachFixture(tc.nearScore, tc.farScore)
			h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})
			h.SetGateGraph(reachGateGraph())
			ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
			cmd := &RunTourCoordinatorCommand{ShipSymbol: "REACH-RANK", PlayerID: 1, RepositionReachEnabled: true}

			cands := h.buildRepositionCandidates(ctx, cmd, "X1-ORIG")

			if !reachHasSystem(cands, "X1-FAR") || !reachHasSystem(cands, "X1-NEAR") {
				t.Fatalf("both grounds must be discovered (always-broaden); the decay decides ORDER, got %v", reachCandidateSystems(cands))
			}
			if cands[0].system != tc.wantFirst {
				t.Fatalf("deadhead-decayed ranking put %s first, want %s (candidates=%v)", cands[0].system, tc.wantFirst, reachCandidateSystems(cands))
			}
		})
	}
}

// THE anti-herd CAP. Absent any herd, the rich distant X1-FAR wins. But a candidate system already
// served by >= the per-system hull cap is EXCLUDED so simultaneously-margin-dead hulls do not all
// pile onto the same top ground and re-drain it. The three cases pin: (a) at the cap → excluded,
// (b) below the cap → kept (the threshold is READ, not "any hull"), (c) non-trade hulls in the
// system do NOT count (the fleet filter). In every case the un-herded 1-hop NEAR stays available.
func TestRepositionReach_AntiHerd_ExcludesSaturatedSystems(t *testing.T) {
	cases := []struct {
		name        string
		cap         int
		activeHulls []activeHull
		wantFar     bool
	}{
		{"distant AT the hull cap is excluded", 2, []activeHull{{"X1-FAR", tradeFleet}, {"X1-FAR", tradeFleet}}, false},
		{"distant BELOW the cap is kept", 2, []activeHull{{"X1-FAR", tradeFleet}}, true},
		{"non-trade hulls in the distant system do not count toward the cap", 2, []activeHull{{"X1-FAR", "scout"}, {"X1-FAR", "scout"}, {"X1-FAR", "scout"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := reachFixture(1000, 3000) // FAR is the rich winner absent any herd
			fx.activeHulls = tc.activeHulls
			h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})
			h.SetGateGraph(reachGateGraph())
			ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
			cmd := &RunTourCoordinatorCommand{ShipSymbol: "REACH-HERD", PlayerID: 1, RepositionReachEnabled: true, RepositionReachMaxHullsPerSystem: tc.cap}

			cands := h.buildRepositionCandidates(ctx, cmd, "X1-ORIG")

			if got := reachHasSystem(cands, "X1-FAR"); got != tc.wantFar {
				t.Fatalf("distant X1-FAR present=%v, want %v (cap=%d, candidates=%v)", got, tc.wantFar, tc.cap, reachCandidateSystems(cands))
			}
			if !reachHasSystem(cands, "X1-NEAR") {
				t.Fatalf("the un-herded 1-hop ground X1-NEAR must remain a candidate, got %v", reachCandidateSystems(cands))
			}
		})
	}
}

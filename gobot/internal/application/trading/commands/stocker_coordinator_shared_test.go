package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// fakeHopGraph resolves jump-hop distances from a fixed "FROM->TO" → hop-count table. A missing
// entry is UNROUTABLE (Path errors) — the caller skips that source (fail-closed). It implements the
// reused GateGraph interface (Path + RepositionPath + Routable + Connections + ChartPresentGate).
// Originally defined in the worker-rebalancer coordinator's test file; relocated here when the
// factory ops were retired (sp-hoj8u), the stocker-coordinator tests now being its sole users.
type fakeHopGraph struct {
	hops map[string]int
}

func (g *fakeHopGraph) Path(_ context.Context, from, to string, _ int) ([]string, error) {
	n, ok := g.hops[from+"->"+to]
	if !ok {
		return nil, fmt.Errorf("no jump-gate route from %s to %s", from, to)
	}
	path := make([]string, n+1)
	for i := range path {
		path[i] = fmt.Sprintf("%s#%d", from, i)
	}
	path[0], path[n] = from, to
	return path, nil
}

// RepositionPath mirrors Path — it exists only to satisfy the GateGraph interface (sp-8k9m); the
// bound is ignored here.
func (g *fakeHopGraph) RepositionPath(ctx context.Context, from, to string, _ int) ([]string, error) {
	return g.Path(ctx, from, to, 0)
}

// PathWithinJumps mirrors Path — it exists only to satisfy the GateGraph interface (sp-e059j); the
// bound is ignored here (the stocker coordinator never routes long-haul-bounded).
func (g *fakeHopGraph) PathWithinJumps(ctx context.Context, from, to string, playerID, _ int) ([]string, error) {
	return g.Path(ctx, from, to, playerID)
}

// PathWithinJumpsStoredThenVerify mirrors Path — it exists only to satisfy the GateGraph interface
// (sp-0o9ub); the stocker coordinator never routes the long-haul stored-then-verify reposition.
func (g *fakeHopGraph) PathWithinJumpsStoredThenVerify(ctx context.Context, from, to string, playerID, _ int) ([]string, error) {
	return g.Path(ctx, from, to, playerID)
}

// StoredHopDistances reads the same fixed hop table — it exists only to satisfy the GateGraph
// interface; the stocker coordinator never proves multi-target reach. A target with no table
// entry is absent from the result, the interface's own way of saying unproven.
func (g *fakeHopGraph) StoredHopDistances(_ context.Context, from string, targets []string, _ int) (map[string]int, error) {
	out := map[string]int{}
	for _, to := range targets {
		if n, ok := g.hops[from+"->"+to]; ok {
			out[to] = n
		}
	}
	return out, nil
}

// StoredRankingDistances mirrors the same fixed table — the stocker coordinator ranks no
// crossings, and the table carries no staleness for the two walks to differ on.
func (g *fakeHopGraph) StoredRankingDistances(ctx context.Context, from string, targets []string, maxJumps int) (map[string]int, error) {
	return g.StoredHopDistances(ctx, from, targets, maxJumps)
}

func (g *fakeHopGraph) Routable(_ context.Context, from, to string, _ int) (bool, error) {
	_, ok := g.hops[from+"->"+to]
	return ok, nil
}

// Connections is inert here — it exists only to satisfy the GateGraph interface (sp-1ki5).
func (g *fakeHopGraph) Connections(_ context.Context, _ string, _ int) ([]system.GateEdge, error) {
	return nil, nil
}

// ChartPresentGate is inert here — it exists only to satisfy the extended GateGraph interface
// (sp-bcsu; shipSymbol added sp-lv2n).
func (g *fakeHopGraph) ChartPresentGate(_ context.Context, _, _ string, _ int) ([]system.GateEdge, error) {
	return nil, nil
}

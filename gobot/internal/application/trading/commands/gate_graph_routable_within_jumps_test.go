package commands

import "context"

// sp-ry741 added RoutableWithinJumps to the GateGraph interface. These trivial mirrors keep the
// pre-existing GateGraph test doubles satisfying the interface; both delegate to their own Routable
// (the bound is ignored — neither double's other consumers exercise the long-haul routability bound).
// They live here, not beside each double's definition, so this lane touches no file a parallel lane owns.

// fakeGateGraph.RoutableWithinJumps mirrors Routable (bound ignored) — the travel/arb tests that use
// this double assert on the default-bound routability verdict, never the long-haul-widened one.
func (f *fakeGateGraph) RoutableWithinJumps(ctx context.Context, from, to string, playerID, _ int) (bool, error) {
	return f.Routable(ctx, from, to, playerID)
}

// fakeHopGraph.RoutableWithinJumps mirrors Routable (bound ignored) — the stocker-coordinator tests
// that use this double never route the long-haul-bounded routability check.
func (g *fakeHopGraph) RoutableWithinJumps(ctx context.Context, from, to string, playerID, _ int) (bool, error) {
	return g.Routable(ctx, from, to, playerID)
}

package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// --- sp-ry741: long-haul Guard-0 routability bound ---

// boundedRoutabilityGraph is a GateGraph port double whose routability verdict depends on the
// caller's jump bound: a cross-system lane is routable iff its stored source->sink hop distance is
// within the bound. It records the exact bound Guard-0 threaded (lastBound), so a test proves the
// command's LegJumpBound reached the resolver — never a second reachability notion invented
// in the test. err, when set, makes the routability check a fail-closed lookup failure (false,err)
// rather than a clean unroutable verdict. Every non-routability method is inert: Guard-0 consults
// only RoutableWithinJumps, and each run under test aborts at Guard-0 or the location guard, before
// any travel/pathfind.
type boundedRoutabilityGraph struct {
	hops      int   // stored source->sink hop distance of the cross-system lane under test
	err       error // non-nil -> a lookup failure (fail-closed), not a clean unroutable verdict
	lastBound int   // the maxJumps Guard-0 last called RoutableWithinJumps with
}

func (g *boundedRoutabilityGraph) RoutableWithinJumps(_ context.Context, from, to string, _, maxJumps int) (bool, error) {
	g.lastBound = maxJumps
	if g.err != nil {
		return false, g.err
	}
	if from == to {
		return true, nil
	}
	return g.hops <= maxJumps, nil
}

// Routable mirrors the production delegation (RoutableWithinJumps at MaxJumpPath), so a caller that
// has NOT yet been switched to the bounded method sees the strict bound-5 verdict — this is what
// makes the RED honest: pre-fix Guard-0 calls Routable and records lastBound=5, refusing a 7-hop lane.
func (g *boundedRoutabilityGraph) Routable(ctx context.Context, from, to string, playerID int) (bool, error) {
	return g.RoutableWithinJumps(ctx, from, to, playerID, gategraph.MaxJumpPath)
}

func (g *boundedRoutabilityGraph) Path(context.Context, string, string, int) ([]string, error) {
	return nil, nil
}
func (g *boundedRoutabilityGraph) PathWithinJumps(context.Context, string, string, int, int) ([]string, error) {
	return nil, nil
}
func (g *boundedRoutabilityGraph) PathWithinJumpsStoredThenVerify(context.Context, string, string, int, int) ([]string, error) {
	return nil, nil
}
func (g *boundedRoutabilityGraph) RepositionPath(context.Context, string, string, int) ([]string, error) {
	return nil, nil
}
func (g *boundedRoutabilityGraph) Connections(context.Context, string, int) ([]system.GateEdge, error) {
	return nil, nil
}
func (g *boundedRoutabilityGraph) ChartPresentGate(context.Context, string, string, int) ([]system.GateEdge, error) {
	return nil, nil
}

// The DOMINANT value-capture behavior and its byte-identical twin, in one parametrized table. The
// hull is at trSource (system X1-TR); the lane runs from a FAR buy system to a FAR sell system 7 gate
// hops apart (> MaxJumpPath=5, <= longHaulRepositionJumps=25). With the long-haul bound the delivery
// leg is admitted and the run advances past Guard-0 to the location guard (hull is not at the far
// buy-at); with the default bound the SAME lane is still refused fail-closed exactly as today. Both
// rows also assert the exact bound Guard-0 threaded reached the resolver — proving the command knob,
// not a changed resolver, is what moves the horizon.
func TestArbCoordinator_Guard0_HonorsLegJumpBound(t *testing.T) {
	const farBuyAt = "X1-KA42-DOCK"    // system X1-KA42 — the hull is NOT here
	const farSellAt = "X1-JP61-MARKET" // system X1-JP61 — 7 gate hops from X1-KA42

	cases := []struct {
		name                 string
		bound                int
		wantRoutabilityAbort bool
		wantResolvedBound    int
	}{
		{name: "long-haul bound admits the far 7-hop delivery leg", bound: 25, wantRoutabilityAbort: false, wantResolvedBound: 25},
		{name: "default bound still refuses the far 7-hop lane (byte-identical)", bound: 0, wantRoutabilityAbort: true, wantResolvedBound: gategraph.MaxJumpPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ship := newTradeHauler(t, "ARB-LH")
			h, mediator := newArbHandler(ship, nil)
			graph := &boundedRoutabilityGraph{hops: 7}
			h.SetGateGraph(graph)

			resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
				ShipSymbol:   ship.ShipSymbol(),
				Good:         trGood,
				BuyAt:        farBuyAt,
				SellAt:       farSellAt,
				PlayerID:     1,
				LegJumpBound: tc.bound,
			})
			if err != nil {
				t.Fatalf("a guarded refusal must not be a Go error, got: %v", err)
			}
			arb := arbResponse(t, resp)

			if arb.RoutabilityAbort != tc.wantRoutabilityAbort {
				t.Fatalf("RoutabilityAbort=%v, want %v (bound=%d) — resp %+v", arb.RoutabilityAbort, tc.wantRoutabilityAbort, tc.bound, arb)
			}
			if graph.lastBound != tc.wantResolvedBound {
				t.Fatalf("Guard-0 resolved bound=%d, want %d — the command's LegJumpBound must reach the resolver", graph.lastBound, tc.wantResolvedBound)
			}
			if len(mediator.purchases) != 0 {
				t.Fatalf("no buy may occur before the location guard, got %d", len(mediator.purchases))
			}
			if tc.wantRoutabilityAbort {
				if !strings.Contains(arb.AbortReason, "X1-KA42") || !strings.Contains(arb.AbortReason, "X1-JP61") {
					t.Fatalf("a routability refusal must name both systems, got %q", arb.AbortReason)
				}
			} else if !arb.LocationAbort {
				t.Fatalf("with the far lane admitted, the run must advance to the location guard, got %+v", arb)
			}
		})
	}
}

// RULINGS #4 preserved at the widened bound: aligning the horizon must NOT weaken the fence. A lane
// genuinely unroutable even at 25 hops is STILL refused pre-buy via the CLEAN veto — (false,nil) ->
// the "refusing to buy" path (RoutabilityAbort, nil Go error), never a silent buy — so zero cargo is
// purchased against an undeliverable sell leg.
func TestArbCoordinator_Guard0_LongHaulBound_StillRefusesGenuinelyUnroutable(t *testing.T) {
	ship := newTradeHauler(t, "ARB-LH2")
	h, mediator := newArbHandler(ship, nil)
	graph := &boundedRoutabilityGraph{hops: 99} // beyond 25 too — genuinely undeliverable
	h.SetGateGraph(graph)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		Good:         trGood,
		BuyAt:        "X1-BR81-DOCK",
		SellAt:       "X1-UZ45-MARKET",
		PlayerID:     1,
		LegJumpBound: 25,
	})
	if err != nil {
		t.Fatalf("a guarded refusal must not be a Go error, got: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.RoutabilityAbort {
		t.Fatalf("a lane unroutable even at bound 25 must still be refused fail-closed, got %+v", arb)
	}
	if !strings.Contains(arb.AbortReason, "refusing to buy") {
		t.Fatalf("a genuinely-unroutable lane must take the clean veto (\"refusing to buy\"), got %q", arb.AbortReason)
	}
	if len(mediator.purchases) != 0 {
		t.Fatalf("ZERO buys when the sell leg is unroutable even at the widened bound, got %d", len(mediator.purchases))
	}
}

// The error semantics Guard-0 depends on survive the widened bound: a bounded routability check that
// ERRORS (the lookup itself failed, distinct from a clean no-path) takes the "could not verify"
// fail-closed abort — NOT the "refusing to buy" clean veto — so an unverifiable route never becomes a
// silent buy.
func TestArbCoordinator_Guard0_LongHaulBound_LookupError_FailsClosedCouldNotVerify(t *testing.T) {
	ship := newTradeHauler(t, "ARB-LH3")
	h, mediator := newArbHandler(ship, nil)
	graph := &boundedRoutabilityGraph{err: errors.New("gate store unreadable")}
	h.SetGateGraph(graph)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		Good:         trGood,
		BuyAt:        "X1-TJ83-DOCK",
		SellAt:       "X1-UM5-MARKET",
		PlayerID:     1,
		LegJumpBound: 25,
	})
	if err != nil {
		t.Fatalf("a guarded refusal must not be a Go error, got: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.RoutabilityAbort {
		t.Fatalf("a routability LOOKUP failure must abort fail-closed, got %+v", arb)
	}
	if !strings.Contains(arb.AbortReason, "could not verify") {
		t.Fatalf("a lookup failure must take the \"could not verify\" abort, not the clean veto, got %q", arb.AbortReason)
	}
	if len(mediator.purchases) != 0 {
		t.Fatalf("ZERO buys when routability cannot be verified, got %d", len(mediator.purchases))
	}
}

// Isolation source: the long-haul leg command — and ONLY it — carries the 25-hop bound. This pins the
// mapping that opts long-haul into the widened horizon; every other RunArbCoordinatorCommand builder
// (the gRPC one-shot factory, incl. its restart-recovery rebuild) omits the field, so it defaults to
// 0 -> MaxJumpPath and the one-shot arb Guard-0 stays byte-identical.
func TestArbCommandForLeg_SetsLongHaulLegJumpBound(t *testing.T) {
	cmd := arbCommandForLeg(directedLegCommand{
		ShipSymbol: "LH-1", Good: "EXOTIC_MATTER", BuyAt: "X1-UM5-DOCK", SellAt: "X1-VB78-MARKET",
		Units: 40, PerHaulCap: 500000, MinMargin: 100, PlayerID: 1, ContainerID: "c-1",
	})
	if cmd.LegJumpBound != longHaulRepositionJumps {
		t.Fatalf("the long-haul leg must set LegJumpBound=%d so Guard-0 admits its far sinks, got %d", longHaulRepositionJumps, cmd.LegJumpBound)
	}
}

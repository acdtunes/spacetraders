package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- sp-ry741 residual: the travel-to-sink FLIGHT must honor the same leg jump bound Guard-0 uses ---

// The residual bug: sp-ry741 widened Guard-0's pre-buy routability CHECK to the long-haul bound (25),
// but the arb executor's actual travel-to-sink FLIGHT still resolved its jump path at the hardcoded
// MaxJumpPath=5. So a long-haul lane whose sink sits 6-12 gate hops out PASSED the buy check, bought,
// then FAILED the SELL-leg flight ("no jump-gate route ... within 5 jumps") and captured zero value —
// the check said "reachable in 25", the hull bought, the flight tried "5" and stranded. This pins that
// the FLIGHT resolves the jump path at the command's jump bound, exactly as the CHECK does.
//
// Driven through the RESUME path (a tranche already aboard) so the run skips the buy + pre-buy guards
// and goes straight to reload -> travel-to-sink: this isolates the flight's horizon, the one thing
// under test. travelWithJumpBound resolves the jump path over the gate graph BEFORE the source
// departure hop; the minimal test mediator then fails that hop, but the resolver has already recorded
// the horizon it was asked for — the observable driven-port outcome the fix changes. The widened flight
// must ride the SAME stored-then-verify resolver the long-haul reposition-to-source rides (sp-0o9ub),
// so its buy-side reposition and sell-side flight agree on both horizon and resolver.
func TestArbCoordinator_TravelToSink_HonorsLegJumpBound(t *testing.T) {
	const crossSystemSink = "X1-LH99-MARKET" // a DIFFERENT system from the hull's X1-TR source

	cases := []struct {
		name                  string
		bound                 int
		wantStoredVerifyBound int // the horizon the flight's jump-path resolver was asked for
	}{
		{name: "long-haul bound flies the far sink at the widened horizon", bound: 25, wantStoredVerifyBound: 25},
		{name: "default bound flies at MaxJumpPath (byte-identical)", bound: 0, wantStoredVerifyBound: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ship := newTradeHauler(t, "ARB-LH-FLIGHT")
			// A tranche already aboard -> the resume path skips the buy + guards and travels to the sink.
			if err := ship.ReceiveCargo(&shared.CargoItem{Symbol: trGood, Units: 12}); err != nil {
				t.Fatalf("preload cargo: %v", err)
			}
			h, _ := newArbHandler(ship, nil)
			// A route exists (path non-empty); the HORIZON the flight resolves at is what we assert.
			graph := &fakeGateGraph{path: []string{"X1-TR", "X1-LH99"}}
			h.SetGateGraph(graph)

			// The flight fails at the source departure hop (the minimal mediator resolves no jump gate),
			// which is irrelevant here: the resolver recorded the horizon it was asked for before that hop.
			_, _ = h.Handle(context.Background(), &RunArbCoordinatorCommand{
				ShipSymbol:   ship.ShipSymbol(),
				Good:         trGood,
				BuyAt:        trSource,        // the hull's own system (X1-TR)
				SellAt:       crossSystemSink, // a far system (X1-LH99) -> a cross-system flight
				PlayerID:     1,
				LegJumpBound: tc.bound,
			})

			if graph.storedThenVerifyBound != tc.wantStoredVerifyBound {
				t.Fatalf("travel-to-sink resolved at horizon %d, want %d — the FLIGHT must honor the command's jump bound (0 -> Path at MaxJumpPath=%d, byte-identical)",
					graph.storedThenVerifyBound, tc.wantStoredVerifyBound, gategraph.MaxJumpPath)
			}
			// A widened flight must ride ONLY the stored-then-verify resolver (the long-haul reposition's,
			// sp-0o9ub), never the strict PathWithinJumps nor the RELAXED RepositionPath — a laden heavy
			// must not route past an unreadable frontier gate.
			if graph.pathWithinBound != 0 || graph.repositionBound != 0 {
				t.Fatalf("the sink flight must use only the stored-then-verify resolver; got pathWithinBound=%d repositionBound=%d",
					graph.pathWithinBound, graph.repositionBound)
			}
		})
	}
}

package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// --- sp-1vhv reserved-cargo guard, tour coordinator -------------------------
//
// The loss: MODULE_CARGO_HOLD_III bought for 196,751cr at X1-ZC66-BA9D and staged
// on TORWIND-1E, then auto-sold by tour-run-TORWIND-1E for 97,033cr because the
// tour treated hold contents as sellable manifest. Two guards: the planner is
// never offered reserved cargo (so it cannot PLAN the liquidation), and the
// executor refuses a reserved sell independently (so a planning leak cannot
// realize the loss).

// Liquidation-path exclusion: a staged module in the hold must NOT count as
// liquidatable inventory offered to the planner — tourShipState drops it while
// still offering ordinary trade goods.
func TestTourShipState_ExcludesReservedCargoFromPlanner(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{"MODULE_CARGO_HOLD_III": 1, "IRON_ORE": 40}, location: "X1-ZC66-BA9D", cargoCap: 80,
	}
	h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})
	ship := fx.buildShip(t, "TORWIND-1E")

	state := h.tourShipState(ship)

	if _, ok := state.Cargo["MODULE_CARGO_HOLD_III"]; ok {
		t.Errorf("a reserved module must be excluded from planner inventory (liquidation exclusion), got %+v", state.Cargo)
	}
	if state.Cargo["IRON_ORE"] != 40 {
		t.Errorf("an ordinary trade good must still be offered to the planner, got %+v", state.Cargo)
	}
}

// The bead's named test: a tour executor handed a sell leg for a staged module (a
// planning leak) completes WITHOUT selling the module, and logs a reason=reserved
// skip line. Drives the real executor through the fake mediator seam.
func TestTour_ExecutorSkipsReservedModuleSell(t *testing.T) {
	fx := &tourFixture{
		cargo:   map[string]int{"MODULE_CARGO_HOLD_III": 1}, location: "X1-ZC66-BA9D", cargoCap: 80,
		markets: map[string][]string{"X1-ZC66": {"X1-ZC66-BA9D", "X1-ZC66-B"}},
		// The incident condition: the module HAS a live bid at the sink (97,033cr) —
		// that is exactly why a naive coordinator sold it. observeGood must find a
		// price so the executor reaches the reservation guard rather than skipping on
		// a missing quote.
		bid: map[string]map[string]int{"X1-ZC66-B": {"MODULE_CARGO_HOLD_III": 97033}},
		ask: map[string]map[string]int{"X1-ZC66-BA9D": {"MODULE_CARGO_HOLD_III": 196751}, "X1-ZC66-B": {"MODULE_CARGO_HOLD_III": 97033}},
		tv:  map[string]map[string]int{"X1-ZC66-B": {"MODULE_CARGO_HOLD_III": 1000}},
	}
	// A plan that (wrongly) sells the staged module — the leak the executor guards.
	// After the skip degrades the leg, the re-plan is infeasible, winding the run
	// down cleanly without ever selling the module.
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, ProjectedProfit: 97033, Legs: []routing.TourLeg{
			leg("X1-ZC66-B", "X1-ZC66", sell("MODULE_CARGO_HOLD_III", 1, 97033)),
		}},
		{Feasible: false, InfeasibleReason: "no profitable tour"},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	logger := &laneLogCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	_, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-1E", PlayerID: 1, ContainerID: "tour-run-TORWIND-1E", ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}

	// The money assertion: the module was never sold.
	for _, entry := range fx.timeline {
		if strings.Contains(entry, "MODULE_CARGO_HOLD_III") {
			t.Fatalf("the staged module must never be sold, but the mediator recorded %q", entry)
		}
	}
	if fx.sells != 0 {
		t.Fatalf("no sell may reach the market for a reserved module, got %d", fx.sells)
	}
	if fx.cargo["MODULE_CARGO_HOLD_III"] != 1 {
		t.Fatalf("the staged module must remain aboard, got %d units", fx.cargo["MODULE_CARGO_HOLD_III"])
	}

	// The acceptance signal the captain greps: a reason=reserved skip line.
	var skip *laneLogEntry
	for i := range logger.entries {
		if strings.Contains(logger.entries[i].message, "reserved (do-not-sell)") && strings.Contains(logger.entries[i].message, "MODULE_CARGO_HOLD_III") {
			skip = &logger.entries[i]
			break
		}
	}
	if skip == nil {
		t.Fatalf("expected a reason=reserved skip log line for the module, got %+v", logger.entries)
	}
	if skip.metadata["reason"] != "reserved" {
		t.Fatalf("skip line must carry reason=reserved metadata, got %v", skip.metadata["reason"])
	}
}

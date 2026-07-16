package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// sp-4hl5 (P0 regression): the Python solver's money guard is
// spend_cap = max(0, max_spend − working_capital_reserve) — max_spend as "cash you
// may touch" with the reserve kept back, the original sp-1ek0 CLI contract. The
// DYNAMIC budget path (--max-spend 0 → 25% of live treasury, re-resolved per tour)
// broke that pairing: its maxSpend is already a spend BUDGET (the capital guard is
// the 25% sizing plus the per-buy live-balance floor), so forwarding the ABSOLUTE
// fleet reserve zeroed the planner's budget for any treasury below 4×reserve
// (25%×T ≤ reserve) — every candidate scored "no profitable allocation under
// tranche decay/guards" and the whole heavy fleet relaunch-looped earning zero.
// The defect was unmasked by sp-ggk2 finally delivering the [trade_fleet] 1M
// reserve to live launches (before it, the reserve silently collapsed to the 50k
// default, whose subtraction was harmless). These tests pin the constraint the
// planner RECEIVES on each path; the buy-time floor (spendfloor suite) is
// untouched and still guards every actual spend.

// Dynamic budget (--max-spend 0), the field values of 2026-07-11: treasury 456,270
// → budget 114,067 (25%), launch-config reserve 1,000,000. The planner must receive
// the resolved budget with a reserve of 0 — forwarding the 1M made
// spend_cap = max(0, 114,067 − 1,000,000) = 0 and no tour could ever buy.
func TestTour_DynamicBudget_PlannerReceivesZeroReserve(t *testing.T) {
	fx := dynamicCapFixture()
	api := &tourStubAPIClient{credits: 456_270}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{roundTripPlan()}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	ctx := auth.WithPlayerToken(context.Background(), "TOUR-DYNRES")
	_, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DYNRES", PlayerID: 1, ContainerID: "ctr-dynres",
		MaxSpend:              0, // dynamic: 25% of live treasury
		WorkingCapitalReserve: 1_000_000,
		ModelArtifactPath:     writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("dynamic-budget tour returned error: %v", err)
	}

	if len(planner.maxSpends) == 0 || planner.maxSpends[0] != 114_067 {
		t.Fatalf("planner max-spends = %v, want first = 114,067 (25%% of the 456,270 live treasury)", planner.maxSpends)
	}
	for i, reserve := range planner.reserves {
		if reserve != 0 {
			t.Fatalf("planner call %d received working_capital_reserve %d, want 0 — under the dynamic budget the solver's spend_cap = max(0, budget − reserve) would be max(0, 114,067 − %d) = 0: no buy is ever allocatable and every tour is 'no profitable allocation under tranche decay/guards' (the sp-4hl5 fleet-wide zero-earning loop)", i+1, reserve, reserve)
		}
	}
}

// Explicit --max-spend keeps the original cash contract: max_spend is the cash the
// captain allows the tour to touch and the reserve is subtracted by the solver
// (spend_cap = 200,000 − 120,000 = 80,000). The sp-4hl5 fix must not reach here.
func TestTour_ExplicitMaxSpend_PlannerKeepsCashContractReserve(t *testing.T) {
	fx := dynamicCapFixture()
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{roundTripPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	ctx := context.Background()
	_, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-EXPRES", PlayerID: 1, ContainerID: "ctr-expres",
		MaxSpend:              200_000, // explicit cash cap — the sp-1ek0 CLI contract
		WorkingCapitalReserve: 120_000,
		ModelArtifactPath:     writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("explicit-cap tour returned error: %v", err)
	}

	if len(planner.maxSpends) == 0 || planner.maxSpends[0] != 200_000 {
		t.Fatalf("planner max-spends = %v, want first = 200,000 (the explicit cap, untouched)", planner.maxSpends)
	}
	if len(planner.reserves) == 0 || planner.reserves[0] != 120_000 {
		t.Fatalf("planner reserves = %v, want first = 120,000 — an explicit --max-spend keeps the cash-contract subtraction (spend_cap = 80,000)", planner.reserves)
	}
}

// sp-syaz (review minor 1): the request-driven per-tour distinct-system cap must ride
// cmd.MaxTourSystems onto the TourConstraints the planner receives — the cmd→cons hop in
// planForState that was wired but untested. A positive cmd value reaches the planner
// verbatim (here 5); the companion below pins the unset default.
func TestTour_PlannerReceivesMaxTourSystems(t *testing.T) {
	fx := dynamicCapFixture()
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{roundTripPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-MTS", PlayerID: 1, ContainerID: "ctr-mts",
		MaxSpend:          200_000, // explicit cap keeps this off the dynamic-treasury path
		MaxTourSystems:    5,       // the operator's raised per-tour system cap
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	if len(planner.maxTourSystems) == 0 || planner.maxTourSystems[0] != 5 {
		t.Fatalf("planner max-tour-systems = %v, want first = 5 (cmd.MaxTourSystems must ride cons.MaxTourSystems to the solver)", planner.maxTourSystems)
	}
}

// sp-syaz default-safety companion: an UNSET MaxTourSystems reaches the planner as 0 —
// which the Python solver resolves to its MAX_TOUR_SYSTEMS default (2), so a tour that
// never sets the cap is byte-identical to today.
func TestTour_PlannerReceivesZeroMaxTourSystemsWhenUnset(t *testing.T) {
	fx := dynamicCapFixture()
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{roundTripPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-MTS0", PlayerID: 1, ContainerID: "ctr-mts0",
		MaxSpend:          200_000, // MaxTourSystems deliberately left unset (0)
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	if len(planner.maxTourSystems) == 0 || planner.maxTourSystems[0] != 0 {
		t.Fatalf("planner max-tour-systems = %v, want first = 0 (unset → the solver's default 2)", planner.maxTourSystems)
	}
}

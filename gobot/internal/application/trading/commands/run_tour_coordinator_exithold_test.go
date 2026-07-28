package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// warningContains reports whether any captured WARNING line contains sub. The shared
// propFloorCapturingLogger only exposes an INFO accessor; the exit-hold invariant reports at
// WARNING (a hull released loaded is not routine).
func warningContains(l *propFloorCapturingLogger, sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.level == "WARNING" && strings.Contains(e.message, sub) {
			return true
		}
	}
	return false
}

// torwindAFixture is the sp-8zhit incident, to the waypoint. TORWIND-A is IN_ORBIT at
// X1-KP23-D41 holding the 20 MICROPROCESSORS it flew in from A3 — and D41 IS the sink for
// MICROPROCESSORS, bidding 4,141/u (82,820 for the load) at that moment. The hull had already
// flown the whole lane and arrived at the market that wanted the goods.
//
// Two details make this the exact shape the pre-fix code released loaded:
//   - The purchase ledger is EMPTY for this run (h.purchaseObligation is in-memory on the
//     daemon-lifetime handler; a restart between the buy and this exit empties it). So netBought
//     is empty, and the old netBought-scoped sweep AND the strand veto both declined.
//   - 20 of 40 units is exactly 50% — NOT laden by isLadenForOffload's `units*100 > capacity*50`
//     — so the margins-death distress liquidation could not have rescued it either.
func torwindAFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{"MICROPROCESSORS": 20}, location: "X1-KP23-D41", cargoCap: 40,
		markets: map[string][]string{"X1-KP23": {"X1-KP23-D41", "X1-KP23-A3"}},
		ask: map[string]map[string]int{
			"X1-KP23-D41": {"MICROPROCESSORS": 4200}, // the sink the hull is standing on
			"X1-KP23-A3":  {"MICROPROCESSORS": 3100}, // the source lane it flew from
		},
		bid: map[string]map[string]int{
			"X1-KP23-D41": {"MICROPROCESSORS": 4141}, // the live bid that was never taken
			"X1-KP23-A3":  {"MICROPROCESSORS": 3000},
		},
		tradeType: map[string]map[string]string{
			"X1-KP23-D41": {"MICROPROCESSORS": "IMPORT"}, // a real sink, not an exporter's sellback
		},
		tv: map[string]map[string]int{
			"X1-KP23-D41": {"MICROPROCESSORS": 1000},
			"X1-KP23-A3":  {"MICROPROCESSORS": 1000},
		},
	}
}

// THE INCIDENT (sp-8zhit, era 5 TORWIND). A tour exits with no plan and the container
// terminalizes; ContainerRunner.releaseShipAssignments then frees the hull on EVERY exit reason.
// Pre-fix, TORWIND-A was released idle, in orbit above the very market bidding 4,141/u for the 20
// MICROPROCESSORS in its hold — a delivered, sellable, full-price load marooned on a hull nothing
// owned. One dock and one sell recovered 82,820 immediately.
//
// The hull must never be released holding cargo a live local bid will take, no matter WHO bought
// it: the container is ending, so "the next tour will sell it as launch inventory" is not on the
// table — the hull is about to have no tour at all.
func TestTour_ExitWithSellableHoldItNeverBought_SellsBeforeRelease(t *testing.T) {
	fx := torwindAFixture()
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan {
		return infeasibleTour() // the planner declines; the exit invariant is the only thing left
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	logger := &propFloorCapturingLogger{}

	resp, err := h.Handle(common.WithLogger(context.Background(), logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-A", PlayerID: 1, ContainerID: "ctr-torwind-a",
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("the no-plan exit must be a clean terminal exit, got error: %v", err)
	}
	r := tourResponse(t, resp)

	// The defect, stated as an assertion: the hold must be empty at release.
	if fx.cargo["MICROPROCESSORS"] != 0 {
		t.Fatalf("TORWIND-A must not be released holding cargo the market it is parked on will buy; still holding %d MICROPROCESSORS (%+v)", fx.cargo["MICROPROCESSORS"], fx.cargo)
	}
	// The guard must be CONSULTED, not merely coincident with an empty hold: this counter moves
	// only inside the exit sweep, and the planner flew nothing that could have emptied the hold.
	if r.ExitHoldLiquidations != 1 {
		t.Fatalf("the exit-hold invariant must have cleared exactly one good, got ExitHoldLiquidations=%d (a zero here means the sweep never ran)", r.ExitHoldLiquidations)
	}
	if fx.sells != 1 {
		t.Fatalf("expected exactly one sell (the exit sweep's), got %d", fx.sells)
	}
	if r.TotalRevenue != 20*4141 {
		t.Fatalf("the full 82,820 must be booked at the live 4,141/u bid, got %d", r.TotalRevenue)
	}
	// It sold WHERE THE HULL ALREADY WAS: the incident's damning detail is that no travel was
	// needed at all.
	if len(fx.navDests) != 0 {
		t.Fatalf("the hull was already standing on its own sink — the sweep must not move it, navDests=%v", fx.navDests)
	}
	if !warningContains(logger, "clearing the hold before release") {
		t.Fatal("the exit sweep must announce itself at WARNING so a released-loaded hull is greppable")
	}
	// Nothing is left to report as stranded once the hold is clear.
	if warningContains(logger, "is being released still holding") {
		t.Fatal("a fully cleared hold must not also report a residual strand")
	}
}

// TORWIND-9's shape: 9 CLOTHING left at X1-KP23-I53, the gate construction site, which does not
// trade CLOTHING at all. Here nothing in the system bids on the load, so there is honestly
// nothing to sell — the hull is released loaded because every alternative is worse (the container
// is terminal, and the stale-claim reconciler frees a hull bound to a COMPLETED container
// regardless). What must NOT happen is silence: the bead's first harm is that the loss is
// "invisible unless someone eyeballs cargo". The residual must be named, with units and location.
//
// It must also NOT be vetoed: the tour is not answerable for a load it was handed, and failing
// the container would wedge the hull in permanent failure over cargo nobody will buy.
func TestTour_ExitWithUnsellableHold_ReportsResidualWithoutSellingOrVetoing(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{"CLOTHING": 9}, location: "X1-KP23-I53", cargoCap: 40,
		markets: map[string][]string{"X1-KP23": {"X1-KP23-I53"}},
		// The construction site trades FUEL, never CLOTHING — no bid exists for the load.
		ask:       map[string]map[string]int{"X1-KP23-I53": {"FUEL": 100}},
		bid:       map[string]map[string]int{"X1-KP23-I53": {"FUEL": 90}},
		tradeType: map[string]map[string]string{"X1-KP23-I53": {"FUEL": "IMPORT"}},
		tv:        map[string]map[string]int{"X1-KP23-I53": {"FUEL": 1000}},
	}
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan {
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	logger := &propFloorCapturingLogger{}

	resp, err := h.Handle(common.WithLogger(context.Background(), logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-9", PlayerID: 1, ContainerID: "ctr-torwind-9",
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("the no-plan exit must be a clean terminal exit, got error: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.sells != 0 {
		t.Fatalf("nothing in the system bids on CLOTHING — the sweep must sell nothing, got %d sells", fx.sells)
	}
	if r.ExitHoldLiquidations != 0 {
		t.Fatalf("no local bid means nothing was cleared, got ExitHoldLiquidations=%d", r.ExitHoldLiquidations)
	}
	if !warningContains(logger, "is being released still holding 9 CLOTHING at X1-KP23-I53") {
		t.Fatal("a hull released with an unsellable load must name the units, the good and the waypoint so the strand is greppable and hand-recoverable")
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("a load the tour never bought must not veto the container into permanent failure, got: %s", reason)
	}
	if r.CargoStranded {
		t.Fatalf("inherited cargo is not a tour strand: %+v", r)
	}
}

// The RepositionDisabled kill-switch narrows the exit sweep to "sell only where you already
// stand". TestTour_DistressLiquidation_RespectsRepositionKillSwitch pins the other half — that a
// hull under the switch is never sent hunting for a buyer at another waypoint. This pins that the
// switch does not reopen the sp-8zhit hole for the damning case: TORWIND-A was parked ON the
// market bidding for its load, so no movement was ever required, and releasing it loaded would be
// wrong with or without the rescue machinery armed.
func TestTour_ExitSweep_UnderKillSwitch_StillSellsWhereTheHullStands(t *testing.T) {
	fx := torwindAFixture()
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan {
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-A", PlayerID: 1, ContainerID: "ctr-torwind-a-off",
		ModelArtifactPath: writeTourArtifact(t), RepositionDisabled: true,
	})
	if err != nil {
		t.Fatalf("kill-switch run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.cargo["MICROPROCESSORS"] != 0 || r.ExitHoldLiquidations != 1 {
		t.Fatalf("a hull standing on its own sink must still be cleared under the kill-switch (no movement is involved), got %+v / ExitHoldLiquidations=%d", fx.cargo, r.ExitHoldLiquidations)
	}
	if len(fx.navDests) != 0 || len(fx.jumps) != 0 {
		t.Fatalf("the kill-switch forbids MOVING the hull, got navDests=%v jumps=%v", fx.navDests, fx.jumps)
	}
}

// The class the sweep now sells got WIDER (any sellable hold, not just this run's purchases), so
// the do-not-sell class must be pinned. Reserved cargo — MODULE_*/MOUNT_* ship hardware staged
// for outfitting, and any good an operator force-reserved with `ship reserve-cargo` — rides a
// working hull to be installed, never traded. A live IMPORT bid for it is not permission to sell
// it: a widened sweep that liquidates a hull's own outfitting is a money-guard breach, not a
// rescue.
func TestTour_ExitSweep_NeverSellsReservedCargo(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{"MODULE_CARGO_HOLD_I": 20}, location: "X1-KP23-D41", cargoCap: 40,
		markets: map[string][]string{"X1-KP23": {"X1-KP23-D41"}},
		// A real, rich, non-EXPORT bid for the staged module — the sweep must still refuse.
		ask:       map[string]map[string]int{"X1-KP23-D41": {"MODULE_CARGO_HOLD_I": 9000}},
		bid:       map[string]map[string]int{"X1-KP23-D41": {"MODULE_CARGO_HOLD_I": 8500}},
		tradeType: map[string]map[string]string{"X1-KP23-D41": {"MODULE_CARGO_HOLD_I": "IMPORT"}},
		tv:        map[string]map[string]int{"X1-KP23-D41": {"MODULE_CARGO_HOLD_I": 1000}},
	}
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan {
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	logger := &propFloorCapturingLogger{}

	resp, err := h.Handle(common.WithLogger(context.Background(), logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-MOD", PlayerID: 1, ContainerID: "ctr-mod-exit",
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.cargo["MODULE_CARGO_HOLD_I"] != 20 {
		t.Fatalf("the staged module must survive the exit sweep untouched, got %+v", fx.cargo)
	}
	if fx.sells != 0 || r.ExitHoldLiquidations != 0 {
		t.Fatalf("reserved cargo is never sellable manifest, got %d sells / ExitHoldLiquidations=%d", fx.sells, r.ExitHoldLiquidations)
	}
	// Reserved hardware is not a strand either — the hull is carrying its own outfitting, which
	// is the point of the reservation. It must not be reported as a marooned load.
	if warningContains(logger, "is being released still holding") {
		t.Fatal("a hull carrying only its own reserved outfitting has stranded nothing and must not be reported as released-loaded")
	}
}

package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// ladenTourCommand builds the same container's command twice — the shape the container
// runner produces when it restarts an iteration that exited resumable: identical ship,
// identical container id, a fresh in-memory run.
func ladenTourCommand(ship, container, artifact string) *RunTourCoordinatorCommand {
	return &RunTourCoordinatorCommand{
		ShipSymbol: ship, PlayerID: 1, ContainerID: container, ModelArtifactPath: artifact,
	}
}

// THE INCIDENT (TORWIND-F9/FB/FC): a tour loads a full hold, the hop to the sink fails,
// the iteration exits resumable and the runner restarts it — and the restarted iteration
// finds no plan and reports SUCCESS while the hull is still full of the cargo the tour
// bought. success=true there is false on its face and suppresses every downstream
// recovery: the hull is released laden with no error for the fleet coordinator to react
// to. The tour's purchase obligation must outlive the restart that interrupted it.
func TestTour_LadenAfterRestart_NeverReportsSuccess(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-YY85-A11A", cargoCap: 225,
		markets: map[string][]string{"X1-YY85": {"X1-YY85-A11A", "X1-YY85-XF1B"}},
		bid:     map[string]map[string]int{"X1-YY85-XF1B": {"FOOD": 2336}},
		ask: map[string]map[string]int{
			"X1-YY85-A11A": {"FOOD": 1100}, "X1-YY85-XF1B": {"FOOD": 2336},
		},
		tv:      map[string]map[string]int{"X1-YY85-A11A": {"FOOD": 1000}, "X1-YY85-XF1B": {"FOOD": 1000}},
		navFail: map[string]bool{"X1-YY85-XF1B": true}, // the hop to the sink dies after the load
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-YY85-A11A", "X1-YY85", buy("FOOD", 180, 1100)),
			leg("X1-YY85-XF1B", "X1-YY85", sell("FOOD", 180, 2336)),
		}},
		{Feasible: false, InfeasibleReason: "no_profitable_tour"},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	// Iteration 1: buys the hold, then the hop to the sink fails — a resumable exit the
	// runner retries (no terminal verdict, so nothing is claimed here).
	if _, err := h.Handle(context.Background(), ladenTourCommand("TORWIND-F9", "ctr-f9", artifact)); err == nil {
		t.Fatal("the failed hop to the sink must exit resumable (non-nil error) so the runner restarts the iteration")
	}
	if fx.cargo["FOOD"] != 180 {
		t.Fatalf("the tour must be holding the 180 FOOD it bought, got %+v", fx.cargo)
	}

	// Iteration 2 — the restart. No plan, hull still full of tour-bought FOOD.
	resp, err := h.Handle(context.Background(), ladenTourCommand("TORWIND-F9", "ctr-f9", artifact))
	if err != nil {
		t.Fatalf("the restarted iteration must exit cleanly (the veto rides CompletionOutcome, not a Go error): %v", err)
	}
	r := tourResponse(t, resp)

	if fx.cargo["FOOD"] != 180 {
		t.Fatalf("precondition: the hull must still be laden at the exit under test, got %+v", fx.cargo)
	}
	ok, reason := r.CompletionOutcome()
	if ok {
		t.Fatalf("a tour holding 180 units of FOOD it bought must NEVER report success, got success=true (%+v)", r)
	}
	if !r.CargoStranded {
		t.Fatalf("the laden exit must be marked CargoStranded so the runner terminalizes FAILED, got %+v", r)
	}
	if !strings.Contains(reason, "180 FOOD") {
		t.Fatalf("the veto reason must name the stranded units and good so the strand is greppable, got %q", reason)
	}
}

// THE F8 CONTROL: the hull that did not herd flew a clean cycle and must keep reporting
// success. A tour that sells out what it bought exits honestly — the strengthened veto
// does not touch the clean path, and it leaves nothing behind that could poison the
// container's NEXT iteration.
func TestTour_CleanSellOut_StillCompletesSuccessfully(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 225,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"FOOD": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"FOOD": 100}, "X1-S1-B": {"FOOD": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"FOOD": 1000}, "X1-S1-B": {"FOOD": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("FOOD", 180, 100)),
			leg("X1-S1-B", "X1-S1", sell("FOOD", 180, 200)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), ladenTourCommand("TORWIND-F8", "ctr-f8", artifact))
	if err != nil {
		t.Fatalf("clean tour returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("a tour that sold out everything it bought must report success, got veto: %s", reason)
	}
	if r.CargoStranded || !r.Completed {
		t.Fatalf("clean sell-out must complete honestly, got %+v", r)
	}
	if fx.cargo["FOOD"] != 0 {
		t.Fatalf("precondition: the hold must be empty after a clean cycle, got %+v", fx.cargo)
	}

	// A discharged obligation must not survive into the container's next iteration: the
	// hull is hand-loaded with cargo the tour never bought and the next run finds no plan.
	fx.mu.Lock()
	fx.cargo["FOOD"] = 40
	fx.mu.Unlock()
	planner.plans = []*routing.TourPlan{{Feasible: false, InfeasibleReason: "no_profitable_tour"}}
	planner.calls = 0

	resp, err = h.Handle(context.Background(), ladenTourCommand("TORWIND-F8", "ctr-f8", artifact))
	if err != nil {
		t.Fatalf("second iteration returned error: %v", err)
	}
	if ok, reason := r2CompletionOutcome(t, resp); !ok {
		t.Fatalf("a settled purchase obligation must not carry into the next iteration and veto foreign cargo, got: %s", reason)
	}
}

// The tour is answerable for what IT bought, not for what it found aboard. A hull that
// starts a run already holding cargo the tour never purchased (a captain load, a prior
// operation's residue) and finds no plan exits honestly — the strengthened veto must not
// start failing every laden hull it is handed.
func TestTour_CargoItNeverBought_DoesNotVeto(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := &tourFixture{
		cargo: map[string]int{"FOOD": 120}, location: "X1-S1-A", cargoCap: 225,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"FOOD": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"FOOD": 100}, "X1-S1-B": {"FOOD": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"FOOD": 1000}, "X1-S1-B": {"FOOD": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: false, InfeasibleReason: "no_profitable_tour"},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), ladenTourCommand("TOUR-PRE", "ctr-pre", artifact))
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("a tour that bought nothing must not be failed for the cargo it was handed, got veto: %s", reason)
	}
	if r.CargoStranded {
		t.Fatalf("pre-existing cargo is not a tour strand: %+v", r)
	}
}

// Reserved cargo (MODULE_*/MOUNT_* ship hardware) rides a working hull to be installed,
// never traded — the executor already refuses to sell it. A hull that ends a run holding
// only reserved cargo has stranded nothing, and must not be failed for carrying its own
// outfitting.
func TestTour_ReservedCargoAboard_DoesNotVeto(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := &tourFixture{
		cargo: map[string]int{"MODULE_CARGO_HOLD_I": 20}, location: "X1-S1-A", cargoCap: 225,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"FOOD": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"FOOD": 100}, "X1-S1-B": {"FOOD": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"FOOD": 1000}, "X1-S1-B": {"FOOD": 1000}},
	}
	// The tour trades FOOD cleanly around the staged module and ends still carrying it.
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("FOOD", 100, 100)),
			leg("X1-S1-B", "X1-S1", sell("FOOD", 100, 200)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), ladenTourCommand("TOUR-MOD", "ctr-mod", artifact))
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if fx.cargo["MODULE_CARGO_HOLD_I"] != 20 {
		t.Fatalf("precondition: the reserved module must still be aboard at the exit, got %+v", fx.cargo)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("reserved (do-not-sell) cargo aboard is not a strand, got veto: %s", reason)
	}
}

// Liquidating a load the tour did NOT buy must not buy credit against a load it DOES
// buy later. Netting the two lets a run empty the hull it inherited, refill it with a
// fresh purchase it never sells, and still exit success — the same laden-success harm
// arriving through the accounting rather than through a restart.
func TestTour_LiquidatingInheritedCargo_DoesNotCreditALaterPurchase(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := &tourFixture{
		cargo: map[string]int{"FOOD": 100}, location: "X1-S1-A", cargoCap: 225,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"FOOD": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"FOOD": 100}, "X1-S1-B": {"FOOD": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"FOOD": 1000}, "X1-S1-B": {"FOOD": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		// Tour 1 sells the 100 units the run inherited.
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-B", "X1-S1", sell("FOOD", 100, 200)),
		}},
		// Tour 2 buys 40 of its own and the run's iteration budget ends before any sink.
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("FOOD", 40, 100)),
		}},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-NET", PlayerID: 1, ContainerID: "ctr-net", Iterations: 2, ModelArtifactPath: artifact,
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if fx.cargo["FOOD"] != 40 {
		t.Fatalf("precondition: the run must end holding exactly the 40 units it bought, got %+v", fx.cargo)
	}
	if ok, reason := r.CompletionOutcome(); ok {
		t.Fatalf("the 40 units this tour bought and never sold must veto success (reason %q), got success=true", reason)
	}
	if !strings.Contains(r.CargoStrandedReason, "40 FOOD") {
		t.Fatalf("the veto must name the 40 units the tour bought, not the 100 it inherited, got %q", r.CargoStrandedReason)
	}
}

// A carried purchase obligation is bounded by what is actually in the hold. Once the
// cargo is gone — sold elsewhere, transferred off, hand-rescued by the captain — the
// obligation is discharged and the container must be able to complete. Otherwise the
// strengthened veto becomes a wedge that fails a healthy hull forever.
func TestTour_CarriedObligationDischargedWhenHoldEmpties(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 225,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"FOOD": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"FOOD": 100}, "X1-S1-B": {"FOOD": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"FOOD": 1000}, "X1-S1-B": {"FOOD": 1000}},
		navFail: map[string]bool{"X1-S1-B": true},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("FOOD", 180, 100)),
			leg("X1-S1-B", "X1-S1", sell("FOOD", 180, 200)),
		}},
		{Feasible: false, InfeasibleReason: "no_profitable_tour"},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	if _, err := h.Handle(context.Background(), ladenTourCommand("TOUR-WEDGE", "ctr-wedge", artifact)); err == nil {
		t.Fatal("precondition: the interrupted iteration must exit resumable")
	}

	// The captain empties the hold by hand; the obligation is discharged.
	fx.mu.Lock()
	fx.cargo["FOOD"] = 0
	fx.mu.Unlock()

	resp, err := h.Handle(context.Background(), ladenTourCommand("TOUR-WEDGE", "ctr-wedge", artifact))
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("an empty hold strands nothing — the container must be able to complete, got veto: %s", reason)
	}
}

// An unreadable hull cannot prove the hold is empty, so the veto falls back to the ledger
// alone (RULINGS #4: a read failure must never turn a strand into a success). That fallback
// only tells the truth if the ledger is SETTLED as cargo leaves — each sell has to book its
// units off the obligation, not merely be papered over at exit by a live hold read. Here a
// tour sells out everything it bought and the hull then goes unreadable: an unsettled ledger
// would fail a clean tour.
func TestTour_UnreadableHullAtExit_FallsBackToASettledLedger(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 225,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"FOOD": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"FOOD": 100}, "X1-S1-B": {"FOOD": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"FOOD": 1000}, "X1-S1-B": {"FOOD": 1000}},
		// The hull goes unreadable the moment the tour's only sell lands, so the epilogue
		// cannot see the empty hold and must rely on the ledger.
		shipUnreadableAfterUnloads: 1,
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("FOOD", 180, 100)),
			leg("X1-S1-B", "X1-S1", sell("FOOD", 180, 200)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), ladenTourCommand("TOUR-BLIND", "ctr-blind", artifact))
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if fx.cargo["FOOD"] != 0 {
		t.Fatalf("precondition: the tour must have sold out, got %+v", fx.cargo)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("every unit bought was sold — an unreadable hull must not manufacture a strand, got veto: %s", reason)
	}
}

// A haul-to-storage DEPOSIT discharges the tour's obligation exactly as a sale does — the
// cargo left the hull into inventory. The exemption has to live in the ledger, not only in
// a live hold read, or a deposit tour whose hull goes unreadable at exit is failed for
// cargo it correctly delivered.
func TestTour_DepositedCargo_DischargesTheObligationEvenWhenTheHullIsUnreadable(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 80,
		markets:                    map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-W"}},
		ask:                        map[string]map[string]int{"X1-S1-A": {"ELECTRONICS": 744}},
		bid:                        map[string]map[string]int{"X1-S1-A": {"ELECTRONICS": 700}},
		tv:                         map[string]map[string]int{"X1-S1-A": {"ELECTRONICS": 40}},
		shipUnreadableAfterUnloads: 1, // blind the epilogue the moment the deposit lands
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, ProjectedProfit: 90240, DepositValue: 120000,
		Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("ELECTRONICS", 40, 744)),
			leg("X1-S1-W", "X1-S1", deposit("ELECTRONICS", 40, 3000)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	coord := wireWarehouse(t, h, "wh-op", "X1-S1-W", 1000, []string{"ELECTRONICS"})

	resp, err := h.Handle(context.Background(), ladenTourCommand("TOUR-DEP", "ctr-dep", artifact))
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if got := coord.GetTotalCargoAvailable("wh-op", "ELECTRONICS"); got != 40 {
		t.Fatalf("precondition: the 40 units must have reached the warehouse, got %d", got)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("deposited cargo left the hull — it strands nothing, got veto: %s", reason)
	}
}

// r2CompletionOutcome reads the completion verdict off a second Handle response.
func r2CompletionOutcome(t *testing.T, resp interface{}) (bool, string) {
	t.Helper()
	return tourResponse(t, resp).CompletionOutcome()
}

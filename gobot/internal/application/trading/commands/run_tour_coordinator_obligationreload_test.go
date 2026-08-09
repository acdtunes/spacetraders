package commands

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// THE GAP THIS CLOSES. The per-hull purchase obligation is held in a map on the
// daemon-lifetime handler. A daemon restart builds a new handler with an EMPTY map while the
// hull is still physically holding the cargo the tour bought — so the CARGO survives the
// bounce and the accounting of it does not, and the laden hull reaches a success exit exactly
// as it did before the veto existed. Restarts are routine (this deploy loop bounced the
// daemon three times in one day), so the window is hit often.
//
// The rebuild is DERIVED from the transactions ledger rather than stored: the buy already
// wrote its hull, good, units and operation, so there is nothing to persist twice and nothing
// to drift.

// tourFakeObligationLedger answers the boot-time rebuild through the REAL derivation, over
// the cargo rows the fixture's own buys and sells recorded. A restart test therefore proves
// the reconstruction end to end instead of hand-feeding the answer it wants back.
type tourFakeObligationLedger struct {
	mu sync.Mutex
	// fx supplies the rows the run under test actually wrote; extra adds rows no run in the
	// test produced (another operation's purchase, a hold the ledger cannot see emptied).
	fx    *tourFixture
	extra []ledger.CargoLeg
	err   error
	calls int
}

func (l *tourFakeObligationLedger) OutstandingPurchases(_ context.Context, _ int, operationType string) (map[string]map[string]int, error) {
	l.mu.Lock()
	l.calls++
	err := l.err
	legs := append([]ledger.CargoLeg(nil), l.extra...)
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if l.fx != nil {
		l.fx.mu.Lock()
		legs = append(legs, l.fx.ledgerLegs...)
		l.fx.mu.Unlock()
	}
	return ledger.OutstandingPurchases(legs, operationType), nil
}

func (l *tourFakeObligationLedger) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// ladenRestartFixture is the incident shape: the tour loads a full hold and the hop to the
// sink dies, so the iteration exits resumable with the cargo still aboard.
func ladenRestartFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{}, location: "X1-YY85-A11A", cargoCap: 225,
		markets: map[string][]string{"X1-YY85": {"X1-YY85-A11A", "X1-YY85-XF1B"}},
		bid:     map[string]map[string]int{"X1-YY85-XF1B": {"FOOD": 2336}},
		ask: map[string]map[string]int{
			"X1-YY85-A11A": {"FOOD": 1100}, "X1-YY85-XF1B": {"FOOD": 2336},
		},
		tv:      map[string]map[string]int{"X1-YY85-A11A": {"FOOD": 1000}, "X1-YY85-XF1B": {"FOOD": 1000}},
		navFail: map[string]bool{"X1-YY85-XF1B": true},
	}
}

func loadAndStrandTour() *tourFakeRoutingClient {
	return &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-YY85-A11A", "X1-YY85", buy("FOOD", 180, 1100)),
			leg("X1-YY85-XF1B", "X1-YY85", sell("FOOD", 180, 2336)),
		},
	}}}
}

var noPlanLeft = []*routing.TourPlan{{Feasible: false, InfeasibleReason: "no_profitable_tour"}}

// THE ACCEPTANCE BAR. A hull holding tour-purchased cargo across a DAEMON restart still owes
// it after boot, and the tour on that hull cannot exit success while the hold stands.
//
// The restart is real, not simulated by clearing a field: the second run is served by a
// SECOND handler built from scratch over the same world, which is precisely what a bounced
// daemon produces — new maps, same fleet, same cargo.
func TestTour_ObligationRebuiltAfterDaemonRestart_LadenExitStillVetoed(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := ladenRestartFixture()

	before := newTourHandler(t, fx, loadAndStrandTour(), &tourFakeTelemetry{})
	if _, err := before.Handle(context.Background(), ladenTourCommand("TORWIND-F9", "ctr-f9", artifact)); err == nil {
		t.Fatal("the failed hop to the sink must exit resumable so the runner restarts the iteration")
	}
	if fx.cargo["FOOD"] != 180 {
		t.Fatalf("precondition: the hull must be holding the 180 FOOD it bought, got %+v", fx.cargo)
	}

	// The daemon restarts. Everything in memory is gone; the hold is not.
	obligations := &tourFakeObligationLedger{fx: fx}
	after := newTourHandler(t, fx, &tourFakeRoutingClient{plans: noPlanLeft}, &tourFakeTelemetry{})
	after.SetPurchaseObligationReader(obligations)

	resp, err := after.Handle(context.Background(), ladenTourCommand("TORWIND-F9", "ctr-f9", artifact))
	if err != nil {
		t.Fatalf("the post-restart iteration must exit cleanly (the veto rides CompletionOutcome, not a Go error): %v", err)
	}
	r := tourResponse(t, resp)
	if fx.cargo["FOOD"] != 180 {
		t.Fatalf("precondition: the hull must still be laden at the exit under test, got %+v", fx.cargo)
	}
	ok, reason := r.CompletionOutcome()
	if ok {
		t.Fatalf("a tour on a hull still holding 180 FOOD it bought before the restart must NEVER report success, got success=true (%+v)", r)
	}
	if !r.CargoStranded {
		t.Fatalf("the post-restart laden exit must be marked CargoStranded so the runner terminalizes FAILED, got %+v", r)
	}
	if !strings.Contains(reason, "180 FOOD") {
		t.Fatalf("the veto reason must name the stranded units and good, got %q", reason)
	}
	if obligations.callCount() != 1 {
		t.Fatalf("the rebuild must run once on the first run after boot, ran %d times", obligations.callCount())
	}
}

// The mirror bar: a SETTLED obligation must not survive the restart either. A hull that sold
// out everything it bought, then took on a load the tour never purchased, must still complete
// honestly after a bounce — a rebuild that resurrects discharged purchases would wedge every
// healthy hull in the fleet at once.
func TestTour_SettledObligationAcrossRestart_NeverWedgesAHealthyHull(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 225,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"FOOD": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"FOOD": 100}, "X1-S1-B": {"FOOD": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"FOOD": 1000}, "X1-S1-B": {"FOOD": 1000}},
	}
	cleanCycle := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("FOOD", 180, 100)),
			leg("X1-S1-B", "X1-S1", sell("FOOD", 180, 200)),
		},
	}}}

	before := newTourHandler(t, fx, cleanCycle, &tourFakeTelemetry{})
	if _, err := before.Handle(context.Background(), ladenTourCommand("TORWIND-F8", "ctr-f8", artifact)); err != nil {
		t.Fatalf("clean tour returned error: %v", err)
	}
	if fx.cargo["FOOD"] != 0 {
		t.Fatalf("precondition: the hold must be empty after a clean cycle, got %+v", fx.cargo)
	}

	// The daemon restarts, and the captain hands the hull a load it never bought.
	fx.mu.Lock()
	fx.cargo["FOOD"] = 40
	fx.mu.Unlock()

	after := newTourHandler(t, fx, &tourFakeRoutingClient{plans: noPlanLeft}, &tourFakeTelemetry{})
	after.SetPurchaseObligationReader(&tourFakeObligationLedger{fx: fx})

	resp, err := after.Handle(context.Background(), ladenTourCommand("TORWIND-F8", "ctr-f8", artifact))
	if err != nil {
		t.Fatalf("post-restart iteration returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("a purchase settled before the restart must not be resurrected by the rebuild, got veto: %s", reason)
	}
	if r.CargoStranded {
		t.Fatalf("foreign cargo is not a tour strand: %+v", r)
	}
}

// The rebuild reads the WHOLE fleet's cargo history, so it must ignore every purchase the
// hull did not make under tour operation — a contract pre-buy, a factory input, a captain's
// hand-bought load. Vetoing on those would fail every laden hull the fleet hands over.
func TestTour_RebuildIgnoresCargoNotBoughtUnderTour(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := &tourFixture{
		cargo: map[string]int{"FOOD": 120}, location: "X1-S1-A", cargoCap: 225,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"FOOD": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"FOOD": 100}, "X1-S1-B": {"FOOD": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"FOOD": 1000}, "X1-S1-B": {"FOOD": 1000}},
	}
	// The ledger says the hull bought the load it is carrying — under contract, not tour.
	obligations := &tourFakeObligationLedger{extra: []ledger.CargoLeg{{
		TxID: "tx-contract", Hull: "TOUR-PRE", Good: "FOOD", Units: 120,
		OperationType: "contract", IsBuy: true,
	}}}

	h := newTourHandler(t, fx, &tourFakeRoutingClient{plans: noPlanLeft}, &tourFakeTelemetry{})
	h.SetPurchaseObligationReader(obligations)

	resp, err := h.Handle(context.Background(), ladenTourCommand("TOUR-PRE", "ctr-pre", artifact))
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("a purchase made under another operation is not a tour obligation, got veto: %s", reason)
	}
	if r.CargoStranded {
		t.Fatalf("another operation's cargo is not a tour strand: %+v", r)
	}
}

// Cargo can leave a hold without a sale: a haul-to-storage DEPOSIT moves goods and no
// credits, so it writes no ledger row at all. The ledger therefore still shows the purchase
// open long after the units reached the warehouse — and the rebuild would hand every
// depositing hull in the fleet an obligation nothing backs.
//
// So the rebuild is CAPPED at what each hull actually carries at boot. That cap is not
// redundant with the exit veto's own live-hold bound: the exit veto fails CLOSED on an
// unreadable hull (RULINGS #4), so a phantom that survives boot becomes a false strand the
// moment a hull read blips. Here the hull discharges its real cargo and then goes unreadable,
// which is exactly that window.
func TestTour_RebuildIsBoundedByWhatTheHullActuallyHolds(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 225,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"IRON": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"IRON": 100}, "X1-S1-B": {"IRON": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"IRON": 1000}, "X1-S1-B": {"IRON": 1000}},
		// Readable at boot (so the cap can be applied), blind at the exit epilogue.
		shipUnreadableAfterUnloads: 1,
	}
	// A tour buy with no closing sale anywhere in the ledger — the units were deposited into
	// a warehouse before the restart, and the hold is empty.
	obligations := &tourFakeObligationLedger{extra: []ledger.CargoLeg{{
		TxID: "tx-deposited", Hull: "TOUR-DEP", Good: "FOOD", Units: 200,
		OperationType: "tour", IsBuy: true,
	}}}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("IRON", 100, 100)),
			leg("X1-S1-B", "X1-S1", sell("IRON", 100, 200)),
		},
	}}}

	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetPurchaseObligationReader(obligations)

	resp, err := h.Handle(context.Background(), ladenTourCommand("TOUR-DEP", "ctr-dep", artifact))
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if fx.cargo["IRON"] != 0 {
		t.Fatalf("precondition: the tour must have sold out what it bought, got %+v", fx.cargo)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("an obligation no hold backs must be dropped at boot, not carried into a fail-closed exit, got: %s", reason)
	}
	if r.CargoStranded {
		t.Fatalf("a hull that deposited its load strands nothing: %+v", r)
	}
}

// The other side of that cap. It is an allowance for cargo that PROVABLY left the hold — a
// read that failed proves nothing, so an unreadable hull keeps its FULL derived obligation
// (RULINGS #4: a read failure must never dissolve a strand). A persistence blip during the
// one boot that follows a restart is exactly when the hull is laden and the daemon is at its
// blindest, and zeroing there would release the strand as a clean completion.
func TestTour_UnreadableHullAtBoot_KeepsTheFullDerivedObligation(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := &tourFixture{
		cargo: map[string]int{"FOOD": 200}, location: "X1-S1-A", cargoCap: 225,
		markets: map[string][]string{"X1-S1": {"X1-S1-A"}},
		ask:     map[string]map[string]int{"X1-S1-A": {"FOOD": 100}},
		tv:      map[string]map[string]int{"X1-S1-A": {"FOOD": 1000}},
		// Blind from the FIRST read, so the rebuild itself meets the unreadable hull rather
		// than one that only goes dark later in the run.
		shipUnreadableAfterUnloads: 1, unloads: 1,
	}
	// A tour buy still open on the ledger, on a hull nothing can read into. No bid anywhere
	// takes FOOD, so the strand survives the pre-exit liquidation and reaches the veto.
	obligations := &tourFakeObligationLedger{extra: []ledger.CargoLeg{{
		TxID: "tx-blindhull", Hull: "TOUR-DARK", Good: "FOOD", Units: 200,
		OperationType: "tour", IsBuy: true,
	}}}

	h := newTourHandler(t, fx, &tourFakeRoutingClient{plans: append(noPlanLeft, noPlanLeft...)}, &tourFakeTelemetry{})
	h.SetPurchaseObligationReader(obligations)

	// The blind iteration cannot fly and exits resumable, claiming no verdict — but the
	// obligation it rebuilt has to be the one the ledger showed, not an empty one.
	if _, err := h.Handle(context.Background(), ladenTourCommand("TOUR-DARK", "ctr-dark", artifact)); err == nil {
		t.Fatal("an unreadable hull must exit resumable so the runner retries the iteration")
	}

	// The blip clears and the runner retries. The hull is still holding what it bought.
	fx.mu.Lock()
	fx.shipUnreadableAfterUnloads = 0
	fx.mu.Unlock()

	resp, err := h.Handle(context.Background(), ladenTourCommand("TOUR-DARK", "ctr-dark", artifact))
	if err != nil {
		t.Fatalf("the retried iteration must exit cleanly: %v", err)
	}
	r := tourResponse(t, resp)
	ok, reason := r.CompletionOutcome()
	if ok {
		t.Fatalf("an obligation rebuilt while the hull was unreadable must survive to the veto, got success=true (%+v)", r)
	}
	if !r.CargoStranded {
		t.Fatalf("the laden exit must terminalize FAILED, got %+v", r)
	}
	if !strings.Contains(reason, "200 FOOD") {
		t.Fatalf("the veto must carry the FULL derived obligation, not a bounded remnant, got %q", reason)
	}
}

// The rebuild is a BOOT step, not a per-run read: it runs once per player for the daemon's
// lifetime, and never overwrites an obligation this daemon is already tracking (which would
// hand a live run a stale count from before its own buys).
func TestTour_RebuildRunsOncePerDaemonLifetime(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := ladenRestartFixture()
	obligations := &tourFakeObligationLedger{fx: fx}

	planner := loadAndStrandTour()
	planner.plans = append(planner.plans, noPlanLeft...)
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetPurchaseObligationReader(obligations)

	if _, err := h.Handle(context.Background(), ladenTourCommand("TORWIND-F9", "ctr-f9", artifact)); err == nil {
		t.Fatal("the failed hop to the sink must exit resumable")
	}
	if _, err := h.Handle(context.Background(), ladenTourCommand("TORWIND-F9", "ctr-f9", artifact)); err != nil {
		t.Fatalf("the retried iteration must exit cleanly: %v", err)
	}
	if got := obligations.callCount(); got != 1 {
		t.Fatalf("the ledger rebuild must run once per daemon lifetime, ran %d times", got)
	}
}

// An unreadable ledger must not wedge the fleet. The rebuild degrades to the empty map — the
// behaviour before it existed, never worse — and stays unsealed so a later run retries rather
// than running blind for the daemon's whole lifetime.
func TestTour_RebuildFailure_DegradesAndRetries(t *testing.T) {
	artifact := writeTourArtifact(t)
	fx := &tourFixture{
		cargo: map[string]int{"FOOD": 120}, location: "X1-S1-A", cargoCap: 225,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"FOOD": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"FOOD": 100}, "X1-S1-B": {"FOOD": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"FOOD": 1000}, "X1-S1-B": {"FOOD": 1000}},
	}
	obligations := &tourFakeObligationLedger{err: errors.New("transactions unreadable")}

	h := newTourHandler(t, fx, &tourFakeRoutingClient{plans: append(noPlanLeft, noPlanLeft...)}, &tourFakeTelemetry{})
	h.SetPurchaseObligationReader(obligations)

	for i := 0; i < 2; i++ {
		resp, err := h.Handle(context.Background(), ladenTourCommand("TOUR-BLIND", "ctr-blind", artifact))
		if err != nil {
			t.Fatalf("run %d returned error: %v", i, err)
		}
		if ok, reason := tourResponse(t, resp).CompletionOutcome(); !ok {
			t.Fatalf("run %d: an unreadable ledger must not veto a hull, got: %s", i, reason)
		}
	}
	if got := obligations.callCount(); got != 2 {
		t.Fatalf("a failed rebuild must be retried on the next run, not sealed; got %d attempts", got)
	}
}

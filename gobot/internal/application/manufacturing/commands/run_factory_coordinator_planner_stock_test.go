package commands

import (
	"context"
	"testing"
)

// C1 (sp-64je) — the factory-output deposit branch in produceNodeOnly, driven
// end-to-end through the real coordinator harness (newFactoryFixture: a
// FAB_PLATE <- IRON chain that harvests and, by default, sells the root output).
// When planner_stock is enabled AND the depositor accepts, the root output is
// deposited at basis instead of being sold; a decline or a disabled flag falls
// back to the existing market sale.

// soldUnitsOf sums units sold of a good at the driven-port boundary.
func (m *factoryFakeMediator) soldUnitsOf(good string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, s := range m.sells {
		if s.GoodSymbol == good {
			total += s.Units
		}
	}
	return total
}

type depositCall struct {
	playerID       int
	ship, waypoint string
	good           string
	units, basis   int
}

type fakeFactoryDepositor struct {
	deposited bool
	err       error
	calls     []depositCall
}

func (f *fakeFactoryDepositor) DepositOutput(_ context.Context, playerID int, ship, waypoint, good string, units, basis int) (bool, error) {
	f.calls = append(f.calls, depositCall{playerID, ship, waypoint, good, units, basis})
	return f.deposited, f.err
}

// Enabled + depositor accepts: the root output deposits at basis, NOT sold.
func TestFactoryCoordinator_PlannerStock_DepositsRootOutputInsteadOfSelling(t *testing.T) {
	f := newFactoryFixture(t)
	f.cmd.PlannerStockEnabled = true
	dep := &fakeFactoryDepositor{deposited: true}
	f.handler.SetPlannerStockDepositor(dep)

	if _, err := f.handler.Handle(context.Background(), f.cmd); err != nil {
		t.Fatalf("coordinator failed: %v", err)
	}

	if units := f.mediator.soldUnitsOf(testOutputGood); units != 0 {
		t.Fatalf("root output must be deposited, not sold at market — got %d units of %s sold", units, testOutputGood)
	}
	if len(dep.calls) == 0 {
		t.Fatal("expected DepositOutput to be called for the harvested root output")
	}
	call := dep.calls[0]
	if call.good != testOutputGood {
		t.Fatalf("deposited good %q, want %q", call.good, testOutputGood)
	}
	if call.basis <= 0 || call.units <= 0 {
		t.Fatalf("expected positive units and basis, got units=%d basis=%d", call.units, call.basis)
	}
}

// Enabled but the depositor declines (no warehouse / over ceiling): the deposit
// is ATTEMPTED, then the code falls through to the existing sell path — the run
// completes cleanly. (This harness's only FAB_PLATE buyer is the factory's own
// waypoint, which sp-rqwm forbids dumping into, so the fall-through sell holds the
// output rather than dumping — the observable here is the deposit attempt.)
func TestFactoryCoordinator_PlannerStock_DeclineAttemptsThenFallsThrough(t *testing.T) {
	f := newFactoryFixture(t)
	f.cmd.PlannerStockEnabled = true
	dep := &fakeFactoryDepositor{deposited: false}
	f.handler.SetPlannerStockDepositor(dep)

	if _, err := f.handler.Handle(context.Background(), f.cmd); err != nil {
		t.Fatalf("coordinator failed: %v", err)
	}

	if len(dep.calls) == 0 {
		t.Fatal("expected a deposit attempt for the harvested root output")
	}
	if dep.calls[0].good != testOutputGood {
		t.Fatalf("deposit attempted for %q, want %q", dep.calls[0].good, testOutputGood)
	}
}

// Disabled (default): the depositor is never consulted — the config gate is
// respected and the output takes the unchanged sell path.
func TestFactoryCoordinator_PlannerStockDisabled_SkipsDepositor(t *testing.T) {
	f := newFactoryFixture(t)
	// PlannerStockEnabled defaults false.
	dep := &fakeFactoryDepositor{deposited: true} // would deposit IF consulted
	f.handler.SetPlannerStockDepositor(dep)

	if _, err := f.handler.Handle(context.Background(), f.cmd); err != nil {
		t.Fatalf("coordinator failed: %v", err)
	}

	if len(dep.calls) != 0 {
		t.Fatalf("depositor must not be consulted when planner_stock is disabled, got %d call(s)", len(dep.calls))
	}
}

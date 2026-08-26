package parkedsensing

// expansion_budget_gate_test.go pins the budget gate's REPORT as well as its
// decision.
//
// Under the wake model nobody watches this engine run, so "expansion skipped
// (budget)" is the entire account an operator gets of a fleet that has stopped
// charting. That string names a gate and no numbers, which leaves two very
// different situations looking identical: the gate genuinely holding, and a tick
// still running on a threshold an operator changed minutes ago. Separating them
// costs hours, and the only thing that can separate them is the tick printing the
// two numbers it actually compared.

import (
	"context"
	"testing"
)

// A tick that clears its floor runs — and still reports both numbers, so an
// ordinary cycle line says what threshold the loop is live on.
func TestAdvanceExpansion_RunningTickReportsTheNumbersItCleared(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{{System: "X1-A", Verdict: VerdictInScope, CatalogKnown: true}}

	const residual, floor = 0.081, 0.050
	rep, err := AdvanceExpansion(context.Background(), h.ports(), 1, ExpandKnobs{
		SeedsEnabled: true, MinBudgetRate: floor, Whitelist: h.whitelist,
	}, residual)

	if err != nil {
		t.Fatalf("AdvanceExpansion: %v", err)
	}
	if rep.Skipped != "" {
		t.Fatalf("a residual of %v above a floor of %v was skipped as %q", residual, floor, rep.Skipped)
	}
	if rep.BudgetRate != residual || rep.MinBudgetRate != floor {
		t.Fatalf("report carries budget %v/%v, want %v/%v — the threshold a tick RAN on is what "+
			"separates a live config from one that has not reached this loop yet",
			rep.BudgetRate, rep.MinBudgetRate, residual, floor)
	}
}

// And a tick held by the gate reports the same pair, so the skip is checkable
// arithmetic rather than a bare label.
func TestAdvanceExpansion_SkippedTickReportsWhatItCompared(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{{System: "X1-A", Verdict: VerdictInScope, CatalogKnown: true}}

	const residual, floor = 0.009, 0.020
	rep, err := AdvanceExpansion(context.Background(), h.ports(), 1, ExpandKnobs{
		SeedsEnabled: true, MinBudgetRate: floor, Whitelist: h.whitelist,
	}, residual)

	if err != nil {
		t.Fatalf("AdvanceExpansion: %v", err)
	}
	if rep.Skipped != SkippedBudget {
		t.Fatalf("Skipped = %q, want %q", rep.Skipped, SkippedBudget)
	}
	if rep.BudgetRate != residual || rep.MinBudgetRate != floor {
		t.Fatalf("skipped report carries budget %v/%v, want %v/%v",
			rep.BudgetRate, rep.MinBudgetRate, residual, floor)
	}

	// The gate stays the OUTERMOST one: a budget-starved tick still touches nothing.
	h.assertIdle(t)
}

// The gate is a strict floor, not a soft preference: a residual exactly ON the
// floor clears it. The boundary is worth pinning because the two knobs feeding it
// are milli-unit integers, so equality is a state an operator reaches by typing a
// round number rather than a measure-zero accident.
func TestAdvanceExpansion_ResidualExactlyOnTheFloorRuns(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{{System: "X1-A", Verdict: VerdictInScope, CatalogKnown: true}}

	const floor = 0.020
	rep, err := AdvanceExpansion(context.Background(), h.ports(), 1, ExpandKnobs{
		SeedsEnabled: true, MinBudgetRate: floor, Whitelist: h.whitelist,
	}, floor)

	if err != nil {
		t.Fatalf("AdvanceExpansion: %v", err)
	}
	if rep.Skipped != "" {
		t.Fatalf("a residual exactly at the floor was skipped as %q", rep.Skipped)
	}
}

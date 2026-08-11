package parkedsensing

import (
	"errors"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// expansion_seed_service_test.go pins WHICH errands a tick serves and WHICH steps it
// charges for. Both decide whether anything gets charted once the fleet outgrows the
// per-tick budget: a step that spends an action without touching the API takes a
// charter's turn, and a queue ordered only by symbol never reaches its own tail.

// walkersAndOneCharter builds the shape both rules are about — enough DISPATCHED seeds
// to consume the whole budget, every one of them sorting alphabetically AHEAD of a lone
// CHARTING seed that is already standing on its target's uncharted waypoint.
func walkersAndOneCharter(h *expandHarness) *fakeUncharted {
	for i := 1; i <= MaxExpansionActions; i++ {
		system := fmt.Sprintf("X1-A%d", i)
		h.ledger.systems = append(h.ledger.systems, ExpandSystem{
			System: system, Verdict: VerdictPending, UnchartedCount: 2,
			SeedShip: "PROBE-" + system, SeedState: SeedStateDispatched,
		})
		h.ships.positions["PROBE-"+system] = ShipPos{
			Waypoint: "X1-HOME-GATE", NavStatus: navigation.NavStatusInOrbit, Found: true,
		}
	}
	h.ledger.systems = append(h.ledger.systems, ExpandSystem{
		System: "X1-Z", Verdict: VerdictPending, UnchartedCount: 1,
		SeedShip: "PROBE-Z", SeedState: SeedStateCharting,
	})
	h.ships.positions["PROBE-Z"] = ShipPos{
		Waypoint: "X1-Z-A1", NavStatus: navigation.NavStatusDocked, Found: true,
	}
	return &fakeUncharted{bySystem: map[string][]string{"X1-Z": {"X1-Z-A1"}}}
}

// A HELD STEP COSTS NOTHING, so it may not spend the thing that paces API calls. A
// cooling hull is not IN_TRANSIT, so it is re-offered its step on every tick until the
// timer clears — and charged for every one of them unless the hold is recognised.
func TestAdvanceExpansion_CoolingSeedsDoNotSpendTheBudgetOfACharter(t *testing.T) {
	h := newExpandHarness()
	uncharted := walkersAndOneCharter(h)
	// WRAPPED, not the bare sentinel: the port is free to add context, so a match on
	// identity rather than on the chain would pass here and fail in production.
	h.seed.jumpErr = fmt.Errorf("stepping PROBE-X1-A1 through its gate: %w", ErrSeedStepHeld)

	rep, err := h.run(t, uncharted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if h.seed.countOf("chart") != 1 {
		t.Fatalf("charted %d times, want 1 — hulls waiting out a timer made no API call, and must not "+
			"consume the turn of a seed standing on an uncharted waypoint", h.seed.countOf("chart"))
	}
	if rep.Actions != 1 {
		t.Fatalf("Actions = %d, want 1 — only the chart reached the API", rep.Actions)
	}
	if h.seed.countOf("jump") != MaxExpansionActions {
		t.Fatalf("offered %d gate steps, want %d — a free step must still be OFFERED every tick, or a "+
			"cooling errand stops being retried at all", h.seed.countOf("jump"), MaxExpansionActions)
	}
}

// A CHARTER OUTRANKS A WALKER. The walkers do real work here, so the budget is
// genuinely contested and only the service order can decide who gets the last of it.
func TestAdvanceExpansion_AChartingSeedIsServedBeforeAWalkingOne(t *testing.T) {
	h := newExpandHarness()
	uncharted := walkersAndOneCharter(h)
	h.seed.jumpErr = nil

	rep, err := h.run(t, uncharted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if h.seed.countOf("chart") != 1 {
		t.Fatalf("charted %d times, want 1 — a seed already standing on an uncharted waypoint must be "+
			"served before hulls still walking to theirs, whatever the symbols sort like",
			h.seed.countOf("chart"))
	}
	if rep.Actions != MaxExpansionActions {
		t.Fatalf("Actions = %d, want the cap %d — the rule reorders the queue, it does not widen it",
			rep.Actions, MaxExpansionActions)
	}
}

// A REFUSED STEP REACHED THE API AND IS STILL CHARGED. Making every error free would
// turn the budget into an unbounded retry loop on exactly the failures that are costing
// requests, which is the opposite of what it paces.
func TestAdvanceExpansion_ARefusedJumpStillSpendsTheBudget(t *testing.T) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-B", Verdict: VerdictPending, UnchartedCount: 3,
			SeedShip: "PROBE-7", SeedState: SeedStateDispatched},
	}
	h.ships.positions = map[string]ShipPos{
		"PROBE-7": {Waypoint: "X1-A-YARD", NavStatus: navigation.NavStatusInOrbit, Found: true},
	}
	h.seed.jumpErr = errors.New("api refused the step")

	rep, err := h.run(t, nil)
	if err != nil {
		t.Fatalf("one refused step must not fail the tick: %v", err)
	}

	if rep.Actions != 1 {
		t.Fatalf("Actions = %d, want 1 — the step was attempted and must be paid for", rep.Actions)
	}
	if rep.Jumped != 0 {
		t.Fatalf("Jumped = %d, want 0 — nothing crossed a gate", rep.Jumped)
	}
	if len(h.ledger.setSeeds) != 0 {
		t.Fatalf("the errand must stay put for the next tick to retry, got %v", h.ledger.setSeeds)
	}
}

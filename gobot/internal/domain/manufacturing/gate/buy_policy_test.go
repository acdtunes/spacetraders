package gate

import (
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Buy while supply is AT OR ABOVE the buy floor; pause BELOW it.
func TestBuyPolicy_BuysAtOrAboveTheFloorAndPausesBelow(t *testing.T) {
	cases := []struct {
		supply  shared.SupplyLevel
		wantBuy bool
	}{
		{shared.SupplyLevelAbundant, true},
		{shared.SupplyLevelHigh, true},
		{shared.SupplyLevelModerate, true}, // AT the floor buys — "at or above"
		{shared.SupplyLevelLimited, false},
		{shared.SupplyLevelScarce, false},
	}
	for _, tc := range cases {
		p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)
		got := p.Decide("FAB_MATS", "WP-FAB", tc.supply)
		if got.Buy != tc.wantBuy {
			t.Fatalf("supply %s: Buy = %v, want %v (buy floor MODERATE)", tc.supply, got.Buy, tc.wantBuy)
		}
		if got.Paused == got.Buy {
			t.Fatalf("supply %s: Buy=%v and Paused=%v — a decision must be exactly one of the two", tc.supply, got.Buy, got.Paused)
		}
	}
}

// HYSTERESIS. Once paused, recovering back to the BUY floor is NOT enough: supply
// must reach the RESUME floor. A single threshold chatters at the boundary — pause,
// one unit regenerates, resume, immediately deplete.
func TestBuyPolicy_ResumeRequiresOneLevelAboveTheBuyFloor(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)

	if d := p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelLimited); !d.Paused {
		t.Fatal("LIMITED is below the MODERATE buy floor and must pause")
	}
	if d := p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelModerate); d.Buy {
		t.Fatal("recovering only to the BUY floor must NOT resume — that is the chatter the resume floor exists to stop")
	}
	if d := p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelHigh); !d.Buy {
		t.Fatal("reaching the RESUME floor must resume buying")
	}
	if d := p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelModerate); !d.Buy {
		t.Fatal("after resuming, MODERATE is at the buy floor and must keep buying — the hysteresis is one-directional")
	}
}

// Supply oscillating AT the boundary must not produce buy/pause chatter.
func TestBuyPolicy_OscillatingAtTheBoundaryDoesNotChatter(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)
	p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelLimited) // enter the pause

	for i := 0; i < 10; i++ {
		if d := p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelModerate); d.Buy {
			t.Fatalf("oscillation %d: resumed at the buy floor while paused — chatter", i)
		}
		if d := p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelLimited); d.Buy {
			t.Fatalf("oscillation %d: bought below the buy floor", i)
		}
	}
}

// Pause state is PER GOOD. One material pausing must not pause the other, which is
// what lets a mixed load depart with whatever is still eligible.
func TestBuyPolicy_PauseStateIsPerGood(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)

	p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelScarce)
	if d := p.Decide("ADVANCED_CIRCUITRY", "WP-CIRC", shared.SupplyLevelHigh); !d.Buy {
		t.Fatal("pausing FAB_MATS also paused ADVANCED_CIRCUITRY — a hull could then idle with an eligible material available")
	}
}

// The fleet is paused only when EVERY gate material is paused. Because a hull fills
// greedily from any eligible material, delivery still has useful work while even one
// material is buyable.
func TestBuyPolicy_FleetPausedRequiresEveryMaterialPausedNotAny(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)
	goods := []string{"FAB_MATS", "ADVANCED_CIRCUITRY"}

	p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelScarce)
	p.Decide("ADVANCED_CIRCUITRY", "WP-CIRC", shared.SupplyLevelHigh)
	if p.FleetPaused(goods) {
		t.Fatal("FleetPaused reported true with ONE material paused — that would starve delivery of capacity it can still use")
	}

	p.Decide("ADVANCED_CIRCUITRY", "WP-CIRC", shared.SupplyLevelScarce)
	if !p.FleetPaused(goods) {
		t.Fatal("FleetPaused reported false with EVERY material paused")
	}
}

// An empty material list is NOT a paused fleet. Nothing was observed, so claiming
// "paused" would send an operator to tune a knob that changes nothing.
func TestBuyPolicy_FleetPausedIsFalseWhenNothingWasObserved(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)
	if p.FleetPaused(nil) {
		t.Fatal("FleetPaused(nil) = true; an unobserved fleet is not a paused one")
	}
	if p.FleetPaused([]string{}) {
		t.Fatal("FleetPaused([]) = true; an unobserved fleet is not a paused one")
	}
	// A good never decided on has no pause state and must not read as paused.
	if p.FleetPaused([]string{"FAB_MATS"}) {
		t.Fatal("a good with no recorded decision read as paused")
	}
}

// OBSERVABILITY. A pause must record factory, good, observed supply and the resume
// condition — in the MESSAGE, because the container log renderer drops metadata maps.
func TestDecision_LogLineNamesFactoryGoodSupplyAndResumeCondition(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)
	line := p.Decide("FAB_MATS", "WP-FABMILL", shared.SupplyLevelLimited).LogLine()

	for _, want := range []string{"FAB_MATS", "WP-FABMILL", "LIMITED", "MODERATE", "HIGH"} {
		if !strings.Contains(line, want) {
			t.Fatalf("pause log line %q does not name %q — the pause must be diagnosable from the log alone", line, want)
		}
	}

	buyLine := p.Decide("ADVANCED_CIRCUITRY", "WP-CIRC", shared.SupplyLevelHigh).LogLine()
	if buyLine == "" {
		t.Fatal("a BUY decision produced no log line; a buying fleet and an idle one must not look identical either")
	}
	if !strings.Contains(buyLine, "ADVANCED_CIRCUITRY") || !strings.Contains(buyLine, "WP-CIRC") {
		t.Fatalf("buy log line %q does not name the good and factory", buyLine)
	}
}

// Unset floors resolve to the ARMED defaults: MODERATE buy, HIGH resume. There is no
// off state — an unset knob is a default, never a disabled policy.
func TestNewBuyPolicy_UnsetFloorsResolveToTheArmedDefaults(t *testing.T) {
	p := NewBuyPolicy("", "")
	buy, resume := p.Floors()
	if buy != DefaultBuyFloor || resume != DefaultResumeFloor {
		t.Fatalf("Floors() = (%s, %s), want the armed defaults (%s, %s)", buy, resume, DefaultBuyFloor, DefaultResumeFloor)
	}
	if DefaultBuyFloor.Order() >= DefaultResumeFloor.Order() {
		t.Fatalf("default resume floor %s is not above the default buy floor %s — the hysteresis gap would be zero", DefaultResumeFloor, DefaultBuyFloor)
	}
}

// A resume floor at or below the buy floor is a zero-or-negative gap: the policy would
// chatter exactly as a single threshold does. Raise it to the buy floor's next level.
func TestNewBuyPolicy_ResumeFloorIsRaisedAboveTheBuyFloor(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelHigh, shared.SupplyLevelLimited)
	buy, resume := p.Floors()
	if resume.Order() <= buy.Order() {
		t.Fatalf("Floors() = (%s, %s); a resume floor at or below the buy floor collapses the hysteresis to a single threshold", buy, resume)
	}
}

// The drain decides concurrently across supply workers; -race must find no data race.
func TestBuyPolicy_DecideIsSafeUnderConcurrentUse(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			supply := shared.SupplyLevelHigh
			if i%2 == 0 {
				supply = shared.SupplyLevelScarce
			}
			p.Decide("FAB_MATS", "WP-FAB", supply)
			p.FleetPaused([]string{"FAB_MATS"})
		}(i)
	}
	wg.Wait()
}

// supplyLadder's doc comment CLAIMS it mirrors shared.SupplyLevel.Order() and is asserted
// against it by the package tests. Make that claim a checked invariant rather than a note
// that rots: a ladder out of step with Order() would make nextLevelAbove raise a mis-set
// resume floor to a LOWER level, silently reinstating the single-threshold chatter that the
// second floor exists to prevent — and TestNewBuyPolicy_ResumeFloorIsRaisedAboveTheBuyFloor
// probes only one pair, so it cannot see a drift elsewhere on the ladder.
func TestSupplyLadder_MirrorsTheSharedSupplyLevelOrdering(t *testing.T) {
	if len(supplyLadder) != 5 {
		t.Fatalf("supplyLadder has %d levels, want all 5 of SCARCE..ABUNDANT", len(supplyLadder))
	}
	for i := 1; i < len(supplyLadder); i++ {
		if supplyLadder[i].Order() <= supplyLadder[i-1].Order() {
			t.Fatalf("supplyLadder[%d]=%s (order %d) does not sit above supplyLadder[%d]=%s (order %d) — the ladder has drifted from shared.SupplyLevel.Order()",
				i, supplyLadder[i], supplyLadder[i].Order(), i-1, supplyLadder[i-1], supplyLadder[i-1].Order())
		}
	}
	if supplyLadder[0] != shared.SupplyLevelScarce || supplyLadder[len(supplyLadder)-1] != shared.SupplyLevelAbundant {
		t.Fatalf("supplyLadder runs %s..%s, want SCARCE..ABUNDANT", supplyLadder[0], supplyLadder[len(supplyLadder)-1])
	}
}

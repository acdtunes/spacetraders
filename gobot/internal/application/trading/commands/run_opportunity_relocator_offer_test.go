package commands

import (
	"testing"
	"time"
)

// run_opportunity_relocator_offer_test.go — FIRST REFUSAL at the tour boundary (sp-e8d92).
//
// THE PROBLEM, measured: 40 trade hulls in 23 of 373 tradeable systems, 7 stacked in one. A tour is
// planned from the hull's current position at cap-2, so it ends roughly where it began — the envelope
// travels WITH the hull. The relocator is the only thing that moves a hull to new ground, and it only
// sees hulls between tours: hulls are busy 97.4% of the time, so it reports mid_tour=38..40 and
// evaluates 1-2 per tick. It is not missing windows (120s tick against a 224s median gap); it is
// outnumbered, because the instant a hull is free its tour re-plans it locally.
//
// THE FIX is not a faster tick and not an event bus — it is an OFFER: durable state saying "this hull
// is available for relocation until T", which makes the relocator eligible to take a hull whose tour
// container is still running, and makes that tour wait. This file pins the relocator half.

// AN OFFERED HULL IS ELIGIBLE DESPITE ITS RUNNING TOUR. Without this the whole mechanism is inert: the
// tour container is still RUNNING between tours, so the hull reads OnTour and the relocator drops it as
// mid_tour — which is exactly the 38..40 exclusions the fleet reports today.
func TestOpportunityRelocatorShould_RelocateAnOfferedHullEvenThoughItsTourIsStillRunning(t *testing.T) {
	h := newRelocHarness(t)
	h.fleet.hulls = []RelocatorHull{
		{ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME", OnTour: true, Offered: true},
	}

	result := h.reconcile(t)

	relocRequireMoved(t, h.actuator, "HAULER-A")
	if result.Skipped["mid_tour"] != 0 {
		t.Fatalf("an OFFERED hull was excluded as mid_tour; the offer exists precisely to lift that exclusion for a hull at its tour boundary. skips %v", result.Skipped)
	}
}

// THE EXEMPTION MUST REACH THE COMMIT GATE TOO. hullProtected is read at scoring AND at actuation, so
// an offer honoured only at scoring would license the move and then abort it at the re-check with
// claimed_at_actuation — every offered relocation would fail, and the counter would blame the claim race
// for a bug in this feature.
func TestOpportunityRelocatorShould_HonourTheOfferAtTheActuationRecheckAsWellAsAtScoring(t *testing.T) {
	h := newRelocHarness(t)
	h.fleet.hulls = []RelocatorHull{
		{ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME", OnTour: true, Offered: true},
	}
	// The actuation re-read returns the same still-offered, still-touring hull.
	h.fleet.atActuation = map[string]RelocatorHull{
		"HAULER-A": {ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME", OnTour: true, Offered: true},
	}

	result := h.reconcile(t)

	relocRequireMoved(t, h.actuator, "HAULER-A")
	if result.Skipped["claimed_at_actuation"] != 0 {
		t.Fatalf("the offer was honoured at scoring but not at the commit gate, so the move was licensed and then abandoned; skips %v", result.Skipped)
	}
}

// THE OFFER IS NOT OWNERSHIP (constraint 2). If the offer lapses between scoring and actuation — the
// window expired and the tour took its hull back — the relocator must abandon cleanly rather than fly a
// hull that is now trading. This is the sp-x2jr6 slice-1 path doing its job on a new input.
func TestOpportunityRelocatorShould_AbandonAnOfferThatLapsedBeforeTheHullCouldBeMoved(t *testing.T) {
	h := newRelocHarness(t)
	h.fleet.hulls = []RelocatorHull{
		{ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME", OnTour: true, Offered: true},
	}
	// By actuation the window has expired: still touring, no longer offered.
	h.fleet.atActuation = map[string]RelocatorHull{
		"HAULER-A": {ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME", OnTour: true, Offered: false},
	}

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "the offer lapsed and the tour has the hull back")
	if result.Skipped["claimed_at_actuation"] != 1 {
		t.Fatalf("a lapsed offer must abandon through the counted commit-gate path, not silently; skips %v", result.Skipped)
	}
}

// A hull that is mid-tour and NOT offered stays excluded — the offer must lift the exclusion for offered
// hulls only, or the relocator starts flying hulls out from under running tours.
func TestOpportunityRelocatorShould_StillExcludeAMidTourHullThatWasNotOffered(t *testing.T) {
	h := newRelocHarness(t)
	h.fleet.hulls = []RelocatorHull{
		{ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME", OnTour: true, Offered: false},
	}

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "the hull is mid-tour and was never offered")
	if result.Skipped["mid_tour"] != 1 {
		t.Fatalf("expected one mid_tour skip; skips %v", result.Skipped)
	}
}

// An offer does NOT override RULINGS #7. A pinned or command hull that somehow carries an offer is still
// protected — the offer says "its tour will wait", never "ownership is waived".
func TestOpportunityRelocatorShould_NeverLetAnOfferOverrideAProtectedHull(t *testing.T) {
	for name, hull := range map[string]RelocatorHull{
		"the command frigate": {ShipSymbol: "COMMAND-1", CurrentSystem: "X1-HOME", IsCommandFrigate: true, Offered: true},
		"a pinned hull":       {ShipSymbol: "PINNED-1", CurrentSystem: "X1-HOME", Pinned: true, Offered: true},
	} {
		h := newRelocHarness(t)
		h.fleet.hulls = []RelocatorHull{hull}
		h.telemetry.rows = relocTelemetryFor(hull.ShipSymbol, 100, relocNow.Add(-30*time.Minute))
		h.regions.byOrigin["X1-HOME"] = []RelocatorRegion{relocRegion("X1-RICH", 1, 5_000_000)}

		h.reconcile(t)

		relocRequireNoMove(t, h.actuator, name+" carries an offer but RULINGS #7 still protects it")
	}
}

// OFFERED HULLS GO FIRST when the concurrency budget is scarce. An offered hull's tour is STALLED and
// its window is closing; an un-offered idle hull is under no such clock and will still be there next
// tick. Ranking purely by NPV would spend the budget on the unhurried hull and let the offer lapse —
// which wastes the very window this feature exists to create.
//
// The fixture makes the offered hull's ground STRICTLY WORSE, so a plain NPV sort would pick the other
// one and the assertion can only pass if the offer is what ordered them.
func TestOpportunityRelocatorShould_SpendAScarceBudgetOnTheOfferedHullBeforeAnUnhurriedOne(t *testing.T) {
	h := newRelocHarness(t)
	h.cmd.MaxConcurrentRelocations = 1
	h.fleet.hulls = []RelocatorHull{
		{ShipSymbol: "IDLE-RICH", CurrentSystem: "X1-IDLE"},
		{ShipSymbol: "OFFERED-POOR", CurrentSystem: "X1-OFFERED", OnTour: true, Offered: true},
	}
	h.regions.byOrigin = map[string][]RelocatorRegion{
		"X1-IDLE":    {relocRegion("X1-RICH-A", 2, 900_000)}, // by NPV this wins easily
		"X1-OFFERED": {relocRegion("X1-RICH-B", 2, 400_000)},
	}
	h.telemetry.rows = append(
		relocTelemetryFor("IDLE-RICH", 100_000, relocNow.Add(-30*time.Minute)),
		relocTelemetryFor("OFFERED-POOR", 100_000, relocNow.Add(-30*time.Minute))...,
	)

	h.reconcile(t)

	relocRequireMoved(t, h.actuator, "OFFERED-POOR")
}

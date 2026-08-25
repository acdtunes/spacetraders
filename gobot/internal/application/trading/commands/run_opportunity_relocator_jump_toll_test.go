package commands

// run_opportunity_relocator_jump_toll_test.go — the relocator's NPV prices travel off the SAME
// measured per-hop toll the tour solver plans against (sp-80mha). Every test enters through
// Reconcile, the driving port, and asserts at the actuator boundary: whether the measured level
// changed WHICH moves are licensed. The NPV thresholds bracket the exactly-computed valuations
// on either side of the level shift, so each flip is an exact-value pin, not a direction guess.
//
// The harness baseline (newRelocHarness): HAULER-A earns 100,000/hr, X1-RICH projects 400,000/hr
// two gate hops away, 48 h of era remain, risk margin one tour (100,000). NPV = 300,000 x 24h
// − 100,000 x travel_h − 100,000, so the toll level enters ONLY through travel_h:
//
//	incumbent 650/hop:   travel_h = 2050s/3600  → NPV 7,043,055.6
//	measured 1028/hop:   travel_h = 3242.2s/3600 → NPV 7,009,940.2  (dearer crossing, smaller NPV)
//	measured  423/hop:   travel_h = 1334.1s/3600 → NPV 7,062,942.3  (cheaper crossing, larger NPV)

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// The UP direction: the live measured median (1028s) sits ABOVE the armed 650, so a move whose
// NPV only cleared the threshold at the frozen constant's cheap travel must be REFUSED once the
// measured level prices the flight honestly.
func TestOpportunityRelocatorShould_RefuseAMoveTheFrozenConstantLicensed_GivenTheMeasuredDearerToll(t *testing.T) {
	atIncumbent := newRelocHarness(t)
	atIncumbent.cmd.NPVThresholdCredits = 7_020_000 // below the incumbent's 7,043,056, above the measured 7,009,940
	atIncumbent.reconcile(t)
	relocRequireMoved(t, atIncumbent.actuator, "HAULER-A")

	atMeasured := newRelocHarness(t)
	atMeasured.cmd.NPVThresholdCredits = 7_020_000
	atMeasured.handler.SetJumpTollReader(fixedTollReader{seconds: 1028})
	result := atMeasured.reconcile(t)

	relocRequireNoMove(t, atMeasured.actuator, "the measured 1028/hop level prices the flight above what this move is worth")
	if result.Skipped[string(trading.RelocationRefusedBelowThreshold)] != 1 {
		t.Fatalf("the refusal must be economic (below_npv_threshold), got skips %v", result.Skipped)
	}
	if result.TravelPerHopSeconds != 1028 {
		t.Fatalf("the tick must report the ADOPTED per-hop level it priced at, got travel/hop=%ds want 1028s", result.TravelPerHopSeconds)
	}
}

// The DOWN direction: the relocator's own recorded evidence points BELOW the armed 650, and a
// measured cheaper level must LICENSE a move the frozen constant refused — the overdue
// bias-against-relocating the un-refitted model carried.
func TestOpportunityRelocatorShould_LicenseAMoveTheFrozenConstantRefused_GivenTheMeasuredCheaperToll(t *testing.T) {
	atIncumbent := newRelocHarness(t)
	atIncumbent.cmd.NPVThresholdCredits = 7_050_000 // above the incumbent's 7,043,056, below the measured-down 7,062,942
	result := atIncumbent.reconcile(t)
	relocRequireNoMove(t, atIncumbent.actuator, "at the frozen 650/hop this move sits below the threshold")
	if result.Skipped[string(trading.RelocationRefusedBelowThreshold)] != 1 {
		t.Fatalf("the incumbent refusal must be economic (below_npv_threshold), got skips %v", result.Skipped)
	}

	atMeasured := newRelocHarness(t)
	atMeasured.cmd.NPVThresholdCredits = 7_050_000
	atMeasured.handler.SetJumpTollReader(fixedTollReader{seconds: 423})
	atMeasured.reconcile(t)
	relocRequireMoved(t, atMeasured.actuator, "HAULER-A")
}

// The cold case is byte-identical to today: a wired reader with too few measured hops answers 0,
// and the tick must price on the incumbent fitted model exactly as an unwired handler does.
func TestOpportunityRelocatorShould_PriceTravelAtTheIncumbent_GivenASilentEstimator(t *testing.T) {
	h := newRelocHarness(t)
	h.cmd.NPVThresholdCredits = 7_020_000 // clears at the incumbent level only if no re-level happened
	h.handler.SetJumpTollReader(fixedTollReader{seconds: 0})

	result := h.reconcile(t)

	relocRequireMoved(t, h.actuator, "HAULER-A")
	if result.TravelPerHopSeconds != 650 {
		t.Fatalf("a silent estimator must leave the tick on the fitted 650/hop level, got travel/hop=%ds", result.TravelPerHopSeconds)
	}
}

// flatHopModel is a refit of a DIFFERENT SHAPE injected through the SetTravelHopModel seam.
type flatHopModel struct{ hours float64 }

func (m flatHopModel) CrossingHours(gateHops int) float64 {
	if gateHops <= 0 {
		return 0
	}
	return m.hours
}

// A marginal-only measurement can re-level only the affine shape it speaks to. A model of a
// different shape injected through the refit seam is sovereign: the loud estimator must not
// stomp it. (At the re-levelled 1028 the move would be refused; the injected flat model's cheap
// crossing licenses it — the move happening is proof of which model priced the flight.)
func TestOpportunityRelocatorShould_LeaveAnInjectedRefitModelSovereignOverTheToll(t *testing.T) {
	h := newRelocHarness(t)
	h.cmd.NPVThresholdCredits = 7_050_000
	h.handler.SetTravelHopModel(flatHopModel{hours: 0.1})
	h.handler.SetJumpTollReader(fixedTollReader{seconds: 1028})

	h.reconcile(t)

	relocRequireMoved(t, h.actuator, "HAULER-A")
}

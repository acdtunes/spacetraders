package contract

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// newPredictorTestContract builds an accepted-shaped contract carrying exactly the
// given deliveries. The predictor reads delivery progress only, so a real clock and
// bare terms are enough - no acceptance/deadline machinery is exercised.
func newPredictorTestContract(t *testing.T, deliveries ...Delivery) *Contract {
	t.Helper()
	c, err := NewContract(
		"CONTRACT-1",
		shared.MustNewPlayerID(1),
		"COSMIC",
		"PROCUREMENT",
		Terms{Deliveries: deliveries},
		nil,
	)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	return c
}

// A single-good contract whose remaining units exceed a hull-load still needs
// more than one trip of that good, so the NEXT source is near-certainly the SAME
// market. The predictor must flag it with full confidence.
func TestPredictNextContractSource_SingleGoodMultiTripRemaining_NearCertain(t *testing.T) {
	c := newPredictorTestContract(t, Delivery{
		TradeSymbol:    "IRON_ORE",
		UnitsRequired:  100,
		UnitsFulfilled: 0,
	})

	pred := PredictNextContractSource(c, 40) // 100 remaining > 40/trip => 3 trips

	if !pred.HasPrediction {
		t.Fatalf("expected a prediction for a multi-trip single-good contract, got none")
	}
	if pred.Good != "IRON_ORE" {
		t.Errorf("predicted good = %q, want IRON_ORE", pred.Good)
	}
	if pred.Confidence < 1.0 {
		t.Errorf("confidence = %.2f, want 1.0 (single good, multi-trip => near-certain)", pred.Confidence)
	}
	if pred.RemainingUnits != 100 {
		t.Errorf("remaining units = %d, want 100", pred.RemainingUnits)
	}
}

// When what remains of the good fits in a single hull-load, the current delivery
// leg finishes it - there is no NEXT delivery to pre-position for. The predictor
// must NOT flag it (guards against a wasted move on the last trip).
func TestPredictNextContractSource_LastTripFits_NoPrediction(t *testing.T) {
	c := newPredictorTestContract(t, Delivery{
		TradeSymbol:    "COPPER_ORE",
		UnitsRequired:  50,
		UnitsFulfilled: 20, // 30 remaining, fits one 40-unit hull-load
	})

	pred := PredictNextContractSource(c, 40)

	if pred.HasPrediction {
		t.Fatalf("expected NO prediction when the remainder fits one trip, got good=%q conf=%.2f", pred.Good, pred.Confidence)
	}
	if pred.Confidence != 0 {
		t.Errorf("confidence = %.2f, want 0 for a last-trip remainder", pred.Confidence)
	}
}

// Two different goods still outstanding makes the NEXT source ambiguous (either
// good's market could be next). The restriction is same-good only, so the signal
// must come back weak (below any sane threshold) - the confidence guard rejects it
// rather than gamble a move on the wrong market.
func TestPredictNextContractSource_MultipleGoodsOutstanding_LowConfidence(t *testing.T) {
	c := newPredictorTestContract(t,
		Delivery{TradeSymbol: "IRON_ORE", UnitsRequired: 100, UnitsFulfilled: 0},
		Delivery{TradeSymbol: "ALUMINUM_ORE", UnitsRequired: 100, UnitsFulfilled: 0},
	)

	pred := PredictNextContractSource(c, 40)

	if pred.Confidence >= 1.0 {
		t.Errorf("confidence = %.2f, want a weak (<1.0) signal when >1 good is outstanding", pred.Confidence)
	}
	if pred.Confidence >= 0.8 {
		t.Errorf("confidence = %.2f, want it below a typical 0.8 threshold (ambiguous next source)", pred.Confidence)
	}
}

// A fully-fulfilled contract has no outstanding good, so there is nothing to
// pre-position toward.
func TestPredictNextContractSource_AllDelivered_NoPrediction(t *testing.T) {
	c := newPredictorTestContract(t, Delivery{
		TradeSymbol:    "IRON_ORE",
		UnitsRequired:  100,
		UnitsFulfilled: 100,
	})

	pred := PredictNextContractSource(c, 40)

	if pred.HasPrediction {
		t.Fatalf("expected no prediction for a completed contract, got good=%q", pred.Good)
	}
}

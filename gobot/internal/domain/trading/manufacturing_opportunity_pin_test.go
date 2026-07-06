package trading

import (
	"math"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func newPinOpportunity(t *testing.T, purchasePrice int, activity, supply string, method goods.AcquisitionMethod) *ManufacturingOpportunity {
	t.Helper()
	waypoint, err := shared.NewWaypoint("X1-T-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	opp, err := NewManufacturingOpportunity("MACHINERY", waypoint, purchasePrice, activity, supply, goods.NewSupplyChainNode("MACHINERY", method))
	if err != nil {
		t.Fatalf("NewManufacturingOpportunity: %v", err)
	}
	return opp
}

func TestManufacturingOpportunityScore_PinsActivityAndSupplyWeights(t *testing.T) {
	cases := []struct {
		name     string
		activity string
		supply   string
		expected float64
	}{
		{"weak abundant", "WEAK", "ABUNDANT", 58.4},
		{"weak high", "WEAK", "HIGH", 54.4},
		{"growing scarce", "GROWING", "SCARCE", 12.4},
		{"strong moderate", "STRONG", "MODERATE", 27.9},
		{"restricted limited", "RESTRICTED", "LIMITED", 31.4},
		{"unknown unknown", "", "", 33.4},
	}
	for _, tc := range cases {
		opp := newPinOpportunity(t, 500, tc.activity, tc.supply, goods.AcquisitionFabricate)
		if got := opp.Score(); math.Abs(got-tc.expected) > 1e-9 {
			t.Errorf("%s: Score() = %v, want %v", tc.name, got, tc.expected)
		}
	}
}

func TestManufacturingOpportunityScore_PinsDirectArbitrageBonusAndPriceCap(t *testing.T) {
	buyRoot := newPinOpportunity(t, 500, "WEAK", "ABUNDANT", goods.AcquisitionBuy)
	if got := buyRoot.Score(); math.Abs(got-158.4) > 1e-9 {
		t.Errorf("BUY root bonus: Score() = %v, want 158.4", got)
	}

	cappedPrice := newPinOpportunity(t, 100000, "WEAK", "ABUNDANT", goods.AcquisitionFabricate)
	if got := cappedPrice.Score(); math.Abs(got-98.0) > 1e-9 {
		t.Errorf("price cap: Score() = %v, want 98.0", got)
	}
}

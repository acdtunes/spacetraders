package trading_test

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// A sink deep enough that the whole leg clears the floor must not be bounded below
// the depth the model actually permits — the bound is a ceiling, not a target.
func TestMaxUnitsWithinSellFloor_DeepSinkPermitsManyTranches(t *testing.T) {
	got, ok := trading.MaxUnitsWithinSellFloor(60, 0.80, trading.DefaultSellImpactCoefficient)
	if !ok {
		t.Fatalf("a well-formed floor+coefficient must yield a bound, got ok=false")
	}
	// 60 units of depth, a 20% tolerated fall, 1.5% fall per full depth:
	// 0.20/0.015 = 13.33 full tranches => 800 units.
	if want := 800; got != want {
		t.Fatalf("deep sink bound: got %d, want %d", got, want)
	}
}

// The bound must fall with the sink's depth: a thin sink tolerates proportionally
// fewer units before the modelled bid reaches the floor.
func TestMaxUnitsWithinSellFloor_ScalesWithDepth(t *testing.T) {
	thin, _ := trading.MaxUnitsWithinSellFloor(1, 0.80, trading.DefaultSellImpactCoefficient)
	thick, _ := trading.MaxUnitsWithinSellFloor(10, 0.80, trading.DefaultSellImpactCoefficient)
	if thin != 13 {
		t.Fatalf("depth 1 bound: got %d, want 13", thin)
	}
	if thick != 133 {
		t.Fatalf("depth 10 bound: got %d, want 133", thick)
	}
}

// A tighter floor tolerates less decay and therefore fewer units — the bound must
// move with the floor the sale will actually arm.
func TestMaxUnitsWithinSellFloor_TighterFloorBoundsHarder(t *testing.T) {
	loose, _ := trading.MaxUnitsWithinSellFloor(6, 0.80, trading.DefaultSellImpactCoefficient)
	tight, _ := trading.MaxUnitsWithinSellFloor(6, 0.95, trading.DefaultSellImpactCoefficient)
	if loose <= tight {
		t.Fatalf("a 95%% floor must bound harder than an 80%% floor: loose %d, tight %d", loose, tight)
	}
	if want := 20; tight != want {
		t.Fatalf("95%% floor bound: got %d, want %d", tight, want)
	}
}

// Unmeasurable depth is LIVE data the guard cannot verify, so it fails CLOSED —
// zero units, never an unbounded buy into a sink whose depth we could not read.
func TestMaxUnitsWithinSellFloor_UnknownDepthFailsClosed(t *testing.T) {
	for _, depth := range []int{0, -1} {
		got, ok := trading.MaxUnitsWithinSellFloor(depth, 0.80, trading.DefaultSellImpactCoefficient)
		if !ok {
			t.Fatalf("depth %d: unknown depth is a bound (zero), not an inexpressible one", depth)
		}
		if got != 0 {
			t.Fatalf("depth %d: got %d, want 0 (fail closed)", depth, got)
		}
	}
}

// A floor or coefficient outside the model's domain is a CALLER error, not a market
// condition, so it reports "no bound expressible" rather than halting every buy.
func TestMaxUnitsWithinSellFloor_UnusableModelIsNotABound(t *testing.T) {
	cases := []struct {
		name     string
		fraction float64
		impact   float64
	}{
		{"no floor declared", 0, 0.015},
		{"negative floor", -0.5, 0.015},
		{"floor at or above the quote", 1.0, 0.015},
		{"no modelled impact", 0.80, 0},
		{"negative impact", 0.80, -0.015},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := trading.MaxUnitsWithinSellFloor(6, tc.fraction, tc.impact); ok {
				t.Fatalf("fraction %v impact %v: must report no expressible bound", tc.fraction, tc.impact)
			}
		})
	}
}

// The bound is the INVERSE of the model's own terminal-bid projection: the last unit
// it permits must still clear the floor, and one more unit must not.
func TestMaxUnitsWithinSellFloor_IsTheInverseOfPostTradeSellPrice(t *testing.T) {
	const (
		depth    = 6
		bid      = 7265.0
		fraction = 0.80
	)
	impact := trading.DefaultSellImpactCoefficient
	bound, ok := trading.MaxUnitsWithinSellFloor(depth, fraction, impact)
	if !ok {
		t.Fatalf("expected a bound")
	}
	floor := fraction * bid
	// The bound sits exactly ON the floor for depths that divide evenly, so compare
	// against a one-credit band rather than an exact float boundary.
	const band = 1.0

	atBound := trading.PostTradeSellPrice(bid, float64(bound)/float64(depth), impact)
	if atBound < floor-band {
		t.Fatalf("terminal bid at the bound (%d units) is %.2f, below the floor %.2f", bound, atBound, floor)
	}
	// One more full tranche past the bound must break the floor, or the bound is loose.
	pastBound := trading.PostTradeSellPrice(bid, float64(bound+depth)/float64(depth), impact)
	if pastBound >= floor-band {
		t.Fatalf("a full tranche past the bound (%d units) still clears the floor %.2f at %.2f — the bound is not tight", bound+depth, floor, pastBound)
	}
}

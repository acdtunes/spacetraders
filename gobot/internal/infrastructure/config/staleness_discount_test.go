package config

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// An absent [trading] staleness_discount section runs the fit ARMED (RULINGS #22). A knob
// nobody set must never be the off switch.
func TestStalenessDiscountConfig_AbsentSectionIsTheArmedFit(t *testing.T) {
	got := TradingConfig{}.StalenessDiscount.Resolved()
	if got != trading.DefaultStalenessDiscount() {
		t.Fatalf("absent section resolved to %+v, want the armed default %+v", got, trading.DefaultStalenessDiscount())
	}
}

func TestStalenessDiscountConfig_Resolved(t *testing.T) {
	cases := []struct {
		name string
		in   StalenessDiscountConfig
		want trading.StalenessDiscount
	}{
		{"zero reverts to the fitted default", StalenessDiscountConfig{ScalePct: 0},
			trading.StalenessDiscount{ScalePct: trading.DefaultStalenessDiscountScalePct}},
		{"a set scale is carried through", StalenessDiscountConfig{ScalePct: 150},
			trading.StalenessDiscount{ScalePct: 150}},
		{"the kill switch survives resolution", StalenessDiscountConfig{Disabled: true},
			trading.StalenessDiscount{ScalePct: trading.DefaultStalenessDiscountScalePct, Disabled: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Resolved(); got != tc.want {
				t.Fatalf("Resolved() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

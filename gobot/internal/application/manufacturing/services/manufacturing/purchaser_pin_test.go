package manufacturing

import (
	"testing"

	domainmfg "github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

func TestCalculateSupplyAwareLimit_PinsSupplyActivityMatrix(t *testing.T) {
	p := &ManufacturingPurchaser{}

	cases := []struct {
		supply   string
		activity string
		expected int
	}{
		{"ABUNDANT", "WEAK", 92}, {"ABUNDANT", "GROWING", 84}, {"ABUNDANT", "STRONG", 68}, {"ABUNDANT", "RESTRICTED", 60}, {"ABUNDANT", "", 80},
		{"HIGH", "WEAK", 69}, {"HIGH", "GROWING", 63}, {"HIGH", "STRONG", 51}, {"HIGH", "RESTRICTED", 44}, {"HIGH", "", 60},
		{"MODERATE", "WEAK", 46}, {"MODERATE", "GROWING", 42}, {"MODERATE", "STRONG", 34}, {"MODERATE", "RESTRICTED", 30}, {"MODERATE", "", 40},
		{"LIMITED", "WEAK", 23}, {"LIMITED", "GROWING", 21}, {"LIMITED", "STRONG", 17}, {"LIMITED", "RESTRICTED", 15}, {"LIMITED", "", 20},
		{"SCARCE", "WEAK", 11}, {"SCARCE", "GROWING", 10}, {"SCARCE", "STRONG", 8}, {"SCARCE", "RESTRICTED", 7}, {"SCARCE", "", 10},
		{"", "WEAK", 46}, {"", "GROWING", 42}, {"", "STRONG", 34}, {"", "RESTRICTED", 30}, {"", "", 40},
	}

	for _, tc := range cases {
		got := p.CalculateSupplyAwareLimit(tc.supply, tc.activity, 100)
		if got != tc.expected {
			t.Errorf("CalculateSupplyAwareLimit(%q, %q, 100) = %d, want %d", tc.supply, tc.activity, got, tc.expected)
		}
	}
}

func TestCalculateSupplyAwareLimit_PinsZeroAndNegativeTradeVolume(t *testing.T) {
	p := &ManufacturingPurchaser{}
	if got := p.CalculateSupplyAwareLimit("ABUNDANT", "WEAK", 0); got != 0 {
		t.Errorf("tradeVolume=0: got %d, want 0", got)
	}
	if got := p.CalculateSupplyAwareLimit("ABUNDANT", "WEAK", -5); got != 0 {
		t.Errorf("tradeVolume=-5: got %d, want 0", got)
	}
}

func TestCalculateSupplyAwareLimit_BaseTableEqualsDomainSupplyLevel(t *testing.T) {
	p := &ManufacturingPurchaser{}
	supplies := []string{"ABUNDANT", "HIGH", "MODERATE", "LIMITED", "SCARCE", ""}
	volumes := []int{1, 37, 100, 4000}

	for _, supply := range supplies {
		for _, volume := range volumes {
			fromPurchaser := p.CalculateSupplyAwareLimit(supply, "", volume)
			fromDomain := domainmfg.SupplyLevel(supply).CalculateSupplyAwareLimit(volume)
			if fromPurchaser != fromDomain {
				t.Errorf("supply %q volume %d: purchaser=%d domain=%d", supply, volume, fromPurchaser, fromDomain)
			}
		}
	}
}

package persistence

import "testing"

func TestScoreMarketForBuying_ScarcerSupplyScoresWorse(t *testing.T) {
	cases := map[string]int{
		"ABUNDANT": 0, "HIGH": 10, "MODERATE": 20, "LIMITED": 30, "SCARCE": 40, "": 50,
	}
	for supply, expected := range cases {
		if got := scoreMarketForBuying("EXPORT", supply, "RESTRICTED"); got != expected {
			t.Errorf("scoreMarketForBuying(EXPORT, %q, RESTRICTED) = %d, want %d", supply, got, expected)
		}
	}
}

func TestScoreMarketForBuying_WeakerActivityScoresWorse(t *testing.T) {
	cases := map[string]int{
		"RESTRICTED": 0, "WEAK": 1, "GROWING": 2, "STRONG": 3, "": 4,
	}
	for activity, expected := range cases {
		if got := scoreMarketForBuying("EXPORT", "ABUNDANT", activity); got != expected {
			t.Errorf("scoreMarketForBuying(EXPORT, ABUNDANT, %q) = %d, want %d", activity, got, expected)
		}
	}
}

func TestScoreMarketForBuying_CompositeIsTradeWeightPlusSupplyPlusActivity(t *testing.T) {
	cases := []struct {
		tradeType string
		supply    string
		activity  string
		expected  int
	}{
		{"EXPORT", "ABUNDANT", "RESTRICTED", 0},
		{"EXCHANGE", "ABUNDANT", "RESTRICTED", 1000},
		{"IMPORT", "ABUNDANT", "RESTRICTED", 2000},
		{"", "ABUNDANT", "RESTRICTED", 3000},
		{"IMPORT", "SCARCE", "STRONG", 2043},
		{"", "", "", 3054},
	}
	for _, tc := range cases {
		if got := scoreMarketForBuying(tc.tradeType, tc.supply, tc.activity); got != tc.expected {
			t.Errorf("scoreMarketForBuying(%q, %q, %q) = %d, want %d", tc.tradeType, tc.supply, tc.activity, got, tc.expected)
		}
	}
}

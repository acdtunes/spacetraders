package persistence

import "testing"

func TestScoreMarketForBuying_ScarcerSupplyScoresWorse(t *testing.T) {
	cases := map[string]int{
		"ABUNDANT": 3, "HIGH": 13, "MODERATE": 23, "LIMITED": 33, "SCARCE": 43, "": 53,
	}
	for supply, expected := range cases {
		if got := scoreMarketForBuying("EXPORT", supply, "RESTRICTED"); got != expected {
			t.Errorf("scoreMarketForBuying(EXPORT, %q, RESTRICTED) = %d, want %d", supply, got, expected)
		}
	}
}

func TestScoreMarketForBuying_WeakerActivityScoresBetter(t *testing.T) {
	cases := map[string]int{
		"WEAK": 0, "GROWING": 1, "STRONG": 2, "RESTRICTED": 3, "": 2,
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
		{"EXPORT", "ABUNDANT", "WEAK", 0},
		{"EXCHANGE", "ABUNDANT", "WEAK", 1000},
		{"IMPORT", "ABUNDANT", "WEAK", 2000},
		{"", "ABUNDANT", "WEAK", 3000},
		{"IMPORT", "SCARCE", "STRONG", 2042},
		{"", "", "", 3052},
	}
	for _, tc := range cases {
		if got := scoreMarketForBuying(tc.tradeType, tc.supply, tc.activity); got != tc.expected {
			t.Errorf("scoreMarketForBuying(%q, %q, %q) = %d, want %d", tc.tradeType, tc.supply, tc.activity, got, tc.expected)
		}
	}
}

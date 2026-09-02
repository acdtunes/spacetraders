package navigation

import "testing"

func TestFleetDedicationPermits(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dedicated string
		operation string
		want      bool
	}{
		{"undedicated hull open to any operation", "", "trade", true},
		{"own identity", "contract", "contract", true},
		{"foreign fleet rejected", "contract", "trade", false},
		{"undeclared operation cannot take a dedicated hull", "trade-mvt", "", false},
		// The migration tags: the tour container always claims under "trade".
		{"trade claims trade-mvt", TradeFleetMVT, TradeFleet, true},
		{"trade claims trade-lane", TradeFleetLane, TradeFleet, true},
		{"trade-mvt claims trade", TradeFleet, TradeFleetMVT, true},
		{"family is not a skeleton key", TradeFleetLane, "contract", false},
		{"family does not open a foreign hull to a trade op", "purchasing", TradeFleetLane, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FleetDedicationPermits(tc.dedicated, tc.operation); got != tc.want {
				t.Fatalf("FleetDedicationPermits(%q, %q) = %v, want %v", tc.dedicated, tc.operation, got, tc.want)
			}
		})
	}
}

func TestIsTradeFleet(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		want bool
	}{
		{TradeFleet, true},
		{TradeFleetMVT, true},
		{TradeFleetLane, true},
		{"", false},
		{"contract", false},
		{"long-haul", false},
		{"trade-", false},
		{"TRADE", false},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			if got := IsTradeFleet(tc.tag); got != tc.want {
				t.Fatalf("IsTradeFleet(%q) = %v, want %v", tc.tag, got, tc.want)
			}
		})
	}
}

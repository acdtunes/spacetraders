package shared

import "testing"

func TestSelectOptimalFlightModeExactThresholdAsymmetry(t *testing.T) {
	const cruiseCost = 100

	cases := []struct {
		name         string
		currentFuel  int
		safetyMargin int
		expected     FlightMode
	}{
		{"exact_burn_threshold_with_margin_below_burn_cost_allows_burn", 210, 10, FlightModeBurn},
		{"exact_cruise_threshold_with_margin_below_cruise_cost_allows_cruise", 110, 10, FlightModeCruise},
		{"exact_cruise_threshold_with_margin_equal_to_cruise_cost_forces_drift", 200, 100, FlightModeDrift},
		{"exact_cruise_threshold_with_margin_above_cruise_cost_forces_drift", 250, 150, FlightModeDrift},
		{"exact_burn_threshold_with_margin_above_burn_cost_falls_back_to_cruise", 450, 250, FlightModeCruise},
		{"strictly_above_burn_threshold_with_small_margin_allows_burn", 211, 10, FlightModeBurn},
		{"strictly_above_cruise_threshold_with_margin_above_cruise_cost_allows_cruise", 251, 150, FlightModeCruise},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SelectOptimalFlightMode(tc.currentFuel, cruiseCost, tc.safetyMargin)
			if got != tc.expected {
				t.Fatalf("SelectOptimalFlightMode(%d, %d, %d) = %s, expected %s",
					tc.currentFuel, cruiseCost, tc.safetyMargin, got, tc.expected)
			}
		})
	}
}

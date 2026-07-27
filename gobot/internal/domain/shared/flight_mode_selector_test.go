package shared

import "testing"

func TestSelectOptimalFlightModeExactThresholdAsymmetry(t *testing.T) {
	const cruiseCost = 100

	cases := []struct {
		name           string
		currentFuel    int
		safetyMargin   int
		expected       FlightMode
		wantAffordable bool
	}{
		{"exact_burn_threshold_with_margin_below_burn_cost_allows_burn", 210, 10, FlightModeBurn, true},
		{"exact_cruise_threshold_with_margin_below_cruise_cost_allows_cruise", 110, 10, FlightModeCruise, true},
		{"exact_cruise_threshold_with_margin_equal_to_cruise_cost_affords_nothing", 200, 100, FlightModeCruise, false},
		{"exact_cruise_threshold_with_margin_above_cruise_cost_affords_nothing", 250, 150, FlightModeCruise, false},
		{"exact_burn_threshold_with_margin_above_burn_cost_falls_back_to_cruise", 450, 250, FlightModeCruise, true},
		{"strictly_above_burn_threshold_with_small_margin_allows_burn", 211, 10, FlightModeBurn, true},
		{"strictly_above_cruise_threshold_with_margin_above_cruise_cost_allows_cruise", 251, 150, FlightModeCruise, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, affordable := SelectOptimalFlightMode(tc.currentFuel, cruiseCost, tc.safetyMargin)
			if got != tc.expected || affordable != tc.wantAffordable {
				t.Fatalf("SelectOptimalFlightMode(%d, %d, %d) = (%s, %t), expected (%s, %t)",
					tc.currentFuel, cruiseCost, tc.safetyMargin, got, affordable, tc.expected, tc.wantAffordable)
			}
		})
	}
}

// TestSelectOptimalFlightModeNeverPicksDrift pins the policy at the selector: no
// fuel level, however low, makes DRIFT the answer. The caller is told the tank
// affords nothing and must buy fuel, rather than being handed a mode that flies
// the leg at ~7x the time.
func TestSelectOptimalFlightModeNeverPicksDrift(t *testing.T) {
	const cruiseCost = 100

	for fuel := 0; fuel <= 2*cruiseCost; fuel++ {
		got, _ := SelectOptimalFlightMode(fuel, cruiseCost, 4)
		if got == FlightModeDrift {
			t.Fatalf("SelectOptimalFlightMode(%d, %d, 4) = DRIFT; a route leg is never degraded to DRIFT", fuel, cruiseCost)
		}
	}
}

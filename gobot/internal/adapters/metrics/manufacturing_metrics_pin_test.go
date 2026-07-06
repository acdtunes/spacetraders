package metrics

import "testing"

func TestSupplyLevel_MapsToOrderedGaugeScale_UnknownIsZero(t *testing.T) {
	collector := &ManufacturingMetricsCollector{}
	cases := map[string]float64{
		"SCARCE": 1, "LIMITED": 2, "MODERATE": 3, "HIGH": 4, "ABUNDANT": 5, "": 0, "UNKNOWN_VALUE": 0,
	}
	for level, expected := range cases {
		if got := collector.supplyLevelToValue(level); got != expected {
			t.Errorf("supplyLevelToValue(%q) = %v, want %v", level, got, expected)
		}
	}
}

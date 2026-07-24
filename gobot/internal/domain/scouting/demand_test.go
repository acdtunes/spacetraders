package scouting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The demand weight is a recency-decayed sum of realized sell-value per sink: a sale
// exactly one half-life old counts half, two half-lives a quarter — the EWMA decay the
// half-life knob controls (sp-wuksw test #3). A just-realized sale counts in full.
func TestDemandWeightsBySink_HalfLifeDecaysRealizedValue(t *testing.T) {
	const halfLife = 3600.0 // 1h
	cases := []struct {
		name       string
		ageSeconds float64
		want       float64
	}{
		{"a just-realized sale counts in full", 0, 1000},
		{"a sale one half-life old counts half", halfLife, 500},
		{"a sale two half-lives old counts a quarter", 2 * halfLife, 250},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			weights := DemandWeightsBySink([]SinkSale{
				{Waypoint: "X1-CC13-EX3B", Value: 1000, AgeSeconds: tc.ageSeconds},
			}, halfLife)
			require.InDelta(t, tc.want, weights["X1-CC13-EX3B"], 0.01,
				"realized demand decays by the half-life so a decayed lane drops off and a newly-hit sink climbs")
		})
	}
}

// A newly-hit sink outweighs a lane that stopped being traded: same realized value, but the
// stale lane has decayed while the fresh sink has not — the auto-adaptive climb/decay the
// demand weighting exists to produce (sp-wuksw).
func TestDemandWeightsBySink_FreshSinkOutweighsDecayedLane(t *testing.T) {
	const halfLife = 7200.0 // 2h
	weights := DemandWeightsBySink([]SinkSale{
		{Waypoint: "FRESH-SINK", Value: 5000, AgeSeconds: 0},
		{Waypoint: "DECAYED-LANE", Value: 5000, AgeSeconds: 4 * halfLife}, // ~1/16th left
	}, halfLife)
	require.Greater(t, weights["FRESH-SINK"], weights["DECAYED-LANE"],
		"a freshly-hit sink climbs above a lane whose trades have decayed away")
	require.InDelta(t, 312.5, weights["DECAYED-LANE"], 0.01,
		"four half-lives leaves 2^-4 = 1/16th of the 5000 realized value (312.5)")
}

// Repeated sales at the SAME sink accumulate — a heavily-sold sink builds a large weight so
// its staleness pulls the freshness percentile onto it (it wins scarce probes).
func TestDemandWeightsBySink_AccumulatesRepeatedSalesPerSink(t *testing.T) {
	weights := DemandWeightsBySink([]SinkSale{
		{Waypoint: "DEEP-SINK", Value: 1000, AgeSeconds: 0},
		{Waypoint: "DEEP-SINK", Value: 3000, AgeSeconds: 0},
		{Waypoint: "SHALLOW-SINK", Value: 500, AgeSeconds: 0},
	}, 0 /* no decay */)
	require.Equal(t, 4000.0, weights["DEEP-SINK"], "realized sell-value sums per sink")
	require.Equal(t, 500.0, weights["SHALLOW-SINK"])
}

// A non-positive realized value (a skipped/degraded leg records RealizedUnits=0) contributes
// nothing and never creates a sink entry — so that market falls back to its intrinsic prior
// rather than being pinned at zero demand.
func TestDemandWeightsBySink_SkipsNonPositiveValue(t *testing.T) {
	weights := DemandWeightsBySink([]SinkSale{
		{Waypoint: "SKIPPED", Value: 0, AgeSeconds: 0},
		{Waypoint: "NEGATIVE", Value: -100, AgeSeconds: 0},
		{Waypoint: "REAL", Value: 200, AgeSeconds: 0},
	}, 3600)
	require.NotContains(t, weights, "SKIPPED", "a zero-value skipped leg creates no sink entry")
	require.NotContains(t, weights, "NEGATIVE", "a malformed negative leg cannot subtract demand")
	require.Equal(t, 200.0, weights["REAL"])
}

// Empty telemetry yields an empty map — the cold-start signal the caller reads as "no realized
// demand, keep every market on its intrinsic prior" (byte-identical to pre-demand weighting).
func TestDemandWeightsBySink_EmptyInputYieldsEmptyMap(t *testing.T) {
	require.Empty(t, DemandWeightsBySink(nil, 3600))
}

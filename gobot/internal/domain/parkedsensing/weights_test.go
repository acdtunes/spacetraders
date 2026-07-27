package parkedsensing

import "testing"

func TestScanWeight(t *testing.T) {
	const clampR = 4

	tests := []struct {
		name       string
		spreadEWMA float64
		median     float64
		clampR     int
		want       float64
	}{
		{
			// A quiet waypoint still earns a full share: weight 1 is the
			// baseline, never 0, so nothing is starved out of the rotation.
			name:       "below-median spread clamps up to the baseline",
			spreadEWMA: 50,
			median:     100,
			clampR:     clampR,
			want:       1,
		},
		{
			name:       "far below median still clamps to the baseline",
			spreadEWMA: 0.01,
			median:     100,
			clampR:     clampR,
			want:       1,
		},
		{
			name:       "at the median earns exactly the baseline",
			spreadEWMA: 100,
			median:     100,
			clampR:     clampR,
			want:       1,
		},
		{
			name:       "twice the median earns twice the attention",
			spreadEWMA: 200,
			median:     100,
			clampR:     clampR,
			want:       2,
		},
		{
			name:       "exactly at the clamp is not clamped down",
			spreadEWMA: 400,
			median:     100,
			clampR:     clampR,
			want:       4,
		},
		{
			// Without a ceiling one outlier waypoint would monopolise the
			// whole scan budget.
			name:       "ten times the median clamps to R",
			spreadEWMA: 1000,
			median:     100,
			clampR:     clampR,
			want:       4,
		},
		{
			name:       "no spread history falls back to the optimistic prior",
			spreadEWMA: 0,
			median:     100,
			clampR:     clampR,
			want:       2,
		},
		{
			name:       "negative spread falls back to the optimistic prior",
			spreadEWMA: -5,
			median:     100,
			clampR:     clampR,
			want:       2,
		},
		{
			// A cold fleet has no median yet; every slot gets the same
			// optimistic prior, so the rotation starts uniform.
			name:       "no fleet median falls back to the optimistic prior",
			spreadEWMA: 500,
			median:     0,
			clampR:     clampR,
			want:       2,
		},
		{
			name:       "negative fleet median falls back to the optimistic prior",
			spreadEWMA: 500,
			median:     -1,
			clampR:     clampR,
			want:       2,
		},
		{
			// R=1 disables weighting entirely: every slot scans on the same
			// cadence, prior included.
			name:       "clamp of 1 flattens every slot to the baseline",
			spreadEWMA: 1000,
			median:     100,
			clampR:     1,
			want:       1,
		},
		{
			name:       "clamp of 1 flattens the prior too",
			spreadEWMA: 0,
			median:     0,
			clampR:     1,
			want:       1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ScanWeight(tc.spreadEWMA, tc.median, tc.clampR)
			assertNear(t, "ScanWeight", got, tc.want)
		})
	}
}

func TestOptimisticPriorWeight(t *testing.T) {
	tests := []struct {
		name   string
		clampR int
		want   float64
	}{
		{name: "typical clamp yields the p75-ish stand-in", clampR: 4, want: 2},
		{name: "clamp of 2 yields the stand-in exactly", clampR: 2, want: 2},
		{name: "clamp below the stand-in wins", clampR: 1, want: 1},
		{name: "large clamp does not inflate the prior", clampR: 100, want: 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertNear(t, "OptimisticPriorWeight", OptimisticPriorWeight(tc.clampR), tc.want)
		})
	}
}

// TestOptimisticPriorWeight_DegenerateClamp pins what a misconfigured clamp
// knob does rather than leaving it accidental: R=0 collapses the prior to 0.
// Downstream that is caught by Interval's zero-weight guard, which falls back
// to the 1h cap (see TestInterval), so the fleet degrades to hourly scans
// rather than dividing by zero. The knob is nonetheless meant to be >= 1.
func TestOptimisticPriorWeight_DegenerateClamp(t *testing.T) {
	assertNear(t, "OptimisticPriorWeight(0)", OptimisticPriorWeight(0), 0)
}

func TestUpdateSpreadEWMA(t *testing.T) {
	tests := []struct {
		name     string
		prev     float64
		observed float64
		want     float64
	}{
		{
			// First observation seeds the series instead of being dragged
			// toward a meaningless zero.
			name:     "no history adopts the observation outright",
			prev:     0,
			observed: 250,
			want:     250,
		},
		{
			name:     "negative history adopts the observation outright",
			prev:     -10,
			observed: 250,
			want:     250,
		},
		{
			// alpha 0.3: 0.3*200 + 0.7*100
			name:     "rising spread is smoothed toward the observation",
			prev:     100,
			observed: 200,
			want:     130,
		},
		{
			// 0.3*50 + 0.7*100
			name:     "falling spread is smoothed toward the observation",
			prev:     100,
			observed: 50,
			want:     85,
		},
		{
			name:     "stable spread is unchanged",
			prev:     100,
			observed: 100,
			want:     100,
		},
		{
			// A genuine zero-spread reading is an observation, not missing
			// history, so it is blended rather than adopted.
			name:     "an observed zero is blended, not adopted",
			prev:     100,
			observed: 0,
			want:     70,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertNear(t, "UpdateSpreadEWMA", UpdateSpreadEWMA(tc.prev, tc.observed), tc.want)
		})
	}
}

// TestUpdateSpreadEWMA_ConvergesToSteadyObservation pins the smoothing end to
// end: a sustained new level is eventually adopted, so a waypoint whose
// spread genuinely collapses stops earning extra attention.
func TestUpdateSpreadEWMA_ConvergesToSteadyObservation(t *testing.T) {
	ewma := 1000.0
	for i := 0; i < 200; i++ {
		ewma = UpdateSpreadEWMA(ewma, 10)
	}
	if ewma > 10.001 {
		t.Errorf("EWMA failed to converge to the steady observation: got %v, want ~10", ewma)
	}
	if ewma < 10 {
		t.Errorf("EWMA undershot the steady observation: got %v, want >= 10", ewma)
	}
}

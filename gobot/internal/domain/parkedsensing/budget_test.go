package parkedsensing

import (
	"math"
	"testing"
	"time"
)

// floatTolerance is the slack allowed on every float assertion in this
// package. The arithmetic is a handful of multiplications on values that are
// not binary-exact (0.92, 0.1), so exact equality would be testing IEEE-754
// rather than the budget policy.
const floatTolerance = 1e-9

func assertNear(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > floatTolerance {
		t.Errorf("%s = %v, want %v (tolerance %v)", label, got, want, floatTolerance)
	}
}

// baseBudget is the documented default posture: the live API ceiling of
// 2.0 req/s, the 92% utilization target, and a 0.1 req/s scan-rate floor,
// with the brake fully released.
func baseBudget() BudgetInputs {
	return BudgetInputs{
		CeilingReqPerSec: 2.0,
		TargetUtilPct:    92,
		MinScanRateMilli: 100,
		NonSensingRate:   0.1,
		ChartingRate:     0,
		BrakeFactor:      1.0,
	}
}

func TestSensingRate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*BudgetInputs)
		want   float64
	}{
		{
			// 0.92 * 2.0 = 1.84 target, less the 0.1 req/s the rest of the
			// fleet is already spending, leaves 1.74 req/s of residual.
			name:   "quiet fleet leaves most of the ceiling to sensing",
			mutate: func(in *BudgetInputs) { in.NonSensingRate = 0.1 },
			want:   1.74,
		},
		{
			// 1.84 - 1.9 is negative: the fleet has already overspent the
			// target, so sensing falls back to its floor rather than zero.
			name:   "busy fleet clamps down to the minimum scan rate",
			mutate: func(in *BudgetInputs) { in.NonSensingRate = 1.9 },
			want:   0.1,
		},
		{
			name:   "exactly-at-target fleet still gets the floor",
			mutate: func(in *BudgetInputs) { in.NonSensingRate = 1.84 },
			want:   0.1,
		},
		{
			// A util knob above 100 must not let sensing outrun the hard
			// rate-limiter ceiling.
			name: "target above 100 percent is capped at the ceiling",
			mutate: func(in *BudgetInputs) {
				in.TargetUtilPct = 150
				in.NonSensingRate = 0
			},
			want: 2.0,
		},
		{
			name: "brake halves the residual",
			mutate: func(in *BudgetInputs) {
				in.NonSensingRate = 0.1
				in.BrakeFactor = 0.5
			},
			want: 0.87,
		},
		{
			// The brake multiplies AFTER the clamp, so a braked fleet is
			// allowed to fall below the min scan rate. The brake is an
			// emergency valve and outranks the floor.
			name: "brake applies after the clamp and may push below the floor",
			mutate: func(in *BudgetInputs) {
				in.NonSensingRate = 1.9
				in.BrakeFactor = 0.5
			},
			want: 0.05,
		},
		{
			// The zero value of BudgetInputs must not mean "fully braked".
			name: "unset brake factor is treated as fully released",
			mutate: func(in *BudgetInputs) {
				in.NonSensingRate = 0.1
				in.BrakeFactor = 0
			},
			want: 1.74,
		},
		{
			name: "brake above 1 cannot inflate past the ceiling",
			mutate: func(in *BudgetInputs) {
				in.TargetUtilPct = 100
				in.NonSensingRate = 0
				in.BrakeFactor = 4.0
			},
			want: 2.0,
		},
		{
			name: "min scan rate knob is honoured in milli-units",
			mutate: func(in *BudgetInputs) {
				in.NonSensingRate = 1.9
				in.MinScanRateMilli = 250
			},
			want: 0.25,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := baseBudget()
			tc.mutate(&in)
			assertNear(t, "SensingRate", SensingRate(in), tc.want)
		})
	}
}

func TestPacerRate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*BudgetInputs)
		want   float64
	}{
		{
			name:   "no charting spend leaves the pacer the whole sensing rate",
			mutate: func(in *BudgetInputs) { in.ChartingRate = 0 },
			want:   1.74,
		},
		{
			name:   "charting spend is subtracted from the pacer share",
			mutate: func(in *BudgetInputs) { in.ChartingRate = 0.3 },
			want:   1.44,
		},
		{
			// Charting is a bounded, higher-priority consumer: it may eat the
			// whole sensing rate, but the pacer never drops below its floor.
			name:   "charting cannot push the pacer below the minimum",
			mutate: func(in *BudgetInputs) { in.ChartingRate = 5.0 },
			want:   0.1,
		},
		{
			name: "pacer floor honours the min scan rate knob",
			mutate: func(in *BudgetInputs) {
				in.ChartingRate = 5.0
				in.MinScanRateMilli = 250
			},
			want: 0.25,
		},
		{
			// The brake drives SensingRate below the floor (to 0.05 here),
			// but the floor is the last word at the pacer, so the scan pacer
			// itself still runs at min_scan_rate. See
			// TestPacerRate_FloorWinsOverBrake.
			name: "braked sub-floor sensing rate is floored again at the pacer",
			mutate: func(in *BudgetInputs) {
				in.NonSensingRate = 1.9
				in.BrakeFactor = 0.5
				in.ChartingRate = 0
			},
			want: 0.1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := baseBudget()
			tc.mutate(&in)
			assertNear(t, "PacerRate", PacerRate(in), tc.want)
		})
	}
}

// TestPacerRate_FloorWinsOverBrake pins the interaction between the two
// functions in one fixture, so neither side can drift without this failing.
//
// The brake legitimately drives SensingRate below min_scan_rate — that
// sub-floor number is real and is what expansion gating reads, so expansion
// pauses before the pacer is touched. But the floor is the last word at the
// pacer: min_scan_rate exists so planner data never goes fully dark, so
// PacerRate lifts the number back to the floor. The brake never starves the
// scan pacer itself.
func TestPacerRate_FloorWinsOverBrake(t *testing.T) {
	in := baseBudget()
	in.NonSensingRate = 1.9 // fleet has overspent the utilization target
	in.BrakeFactor = 0.5    // and the API is under pressure
	in.ChartingRate = 0

	// The residual clamps to the 0.1 floor, then the brake halves it.
	assertNear(t, "SensingRate under brake", SensingRate(in), 0.05)

	// The pacer re-imposes the floor regardless.
	assertNear(t, "PacerRate under brake", PacerRate(in), 0.1)
}

func TestApplyBrake(t *testing.T) {
	const (
		waitLow  = 200 * time.Millisecond
		waitHigh = 800 * time.Millisecond
	)

	tests := []struct {
		name     string
		prev     float64
		waitEWMA time.Duration
		want     float64
	}{
		{
			name:     "wait above the high mark halves the brake",
			prev:     1.0,
			waitEWMA: 900 * time.Millisecond,
			want:     0.5,
		},
		{
			name:     "halving compounds across ticks",
			prev:     0.5,
			waitEWMA: 900 * time.Millisecond,
			want:     0.25,
		},
		{
			name:     "wait below the low mark recovers by 1.2x",
			prev:     0.25,
			waitEWMA: 100 * time.Millisecond,
			want:     0.3,
		},
		{
			name:     "recovery is capped at fully released",
			prev:     0.9,
			waitEWMA: 100 * time.Millisecond,
			want:     1.0,
		},
		{
			name:     "already released stays released",
			prev:     1.0,
			waitEWMA: 100 * time.Millisecond,
			want:     1.0,
		},
		{
			name:     "wait between the marks leaves the brake untouched",
			prev:     0.5,
			waitEWMA: 500 * time.Millisecond,
			want:     0.5,
		},
		{
			// Both comparisons are strict, so sitting exactly on a mark is
			// inside the dead band.
			name:     "exactly at the high mark is inside the dead band",
			prev:     0.5,
			waitEWMA: waitHigh,
			want:     0.5,
		},
		{
			name:     "exactly at the low mark is inside the dead band",
			prev:     0.5,
			waitEWMA: waitLow,
			want:     0.5,
		},
		{
			name:     "braking cannot go below the 0.1 floor",
			prev:     0.15,
			waitEWMA: 900 * time.Millisecond,
			want:     0.1,
		},
		{
			name:     "already at the floor stays at the floor",
			prev:     0.1,
			waitEWMA: 900 * time.Millisecond,
			want:     0.1,
		},
		{
			// An uninitialized caller (zero value) means "no brake yet", not
			// "fully braked".
			name:     "zero previous is treated as fully released",
			prev:     0,
			waitEWMA: 500 * time.Millisecond,
			want:     1.0,
		},
		{
			name:     "negative previous is treated as fully released then braked",
			prev:     -1,
			waitEWMA: 900 * time.Millisecond,
			want:     0.5,
		},
		{
			name:     "previous above 1 is capped before braking",
			prev:     4.0,
			waitEWMA: 900 * time.Millisecond,
			want:     0.5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ApplyBrake(tc.prev, tc.waitEWMA, waitLow, waitHigh)
			assertNear(t, "ApplyBrake", got, tc.want)
		})
	}
}

// TestApplyBrake_ConvergesToFloorUnderSustainedPressure pins the emergency
// valve end to end: no matter how long the API stays slow, the brake settles
// at the floor rather than collapsing to zero and stalling sensing forever.
func TestApplyBrake_ConvergesToFloorUnderSustainedPressure(t *testing.T) {
	const (
		waitLow  = 200 * time.Millisecond
		waitHigh = 800 * time.Millisecond
	)

	brake := 1.0
	for i := 0; i < 50; i++ {
		brake = ApplyBrake(brake, 5*time.Second, waitLow, waitHigh)
		if brake < 0.1 {
			t.Fatalf("brake fell below the floor on tick %d: %v", i, brake)
		}
	}
	assertNear(t, "brake after sustained pressure", brake, 0.1)

	// And it climbs back out once the pressure clears.
	for i := 0; i < 50; i++ {
		brake = ApplyBrake(brake, time.Millisecond, waitLow, waitHigh)
	}
	assertNear(t, "brake after recovery", brake, 1.0)
}

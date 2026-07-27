package parkedsensing

import (
	"math"
	"testing"
)

// immutableFloor mirrors the fleet-wide working-capital floor that every spend
// path is bounded by (common.ImmutableReserveFloor). It is restated here as a
// literal rather than imported: the domain layer must not depend on the
// application layer, and the wiring task asserts the two constants agree.
const immutableFloor = 50_000

func TestProbeBuyFloor(t *testing.T) {
	tests := []struct {
		name              string
		immutable         int64
		capexReserve      int64
		cargoSpendPerHour int64
		k                 int
		want              int64
	}{
		{
			// A trading fleet: the immutable floor, the capex the operation
			// has earmarked, and two hours of cargo runway on top.
			name:              "trading fleet reserves two hours of cargo runway",
			immutable:         immutableFloor,
			capexReserve:      100_000,
			cargoSpendPerHour: 300_000,
			k:                 2,
			want:              750_000,
		},
		{
			// Era start: nothing is trading yet, so there is no runway to
			// protect and the floor is just the immutable plus capex.
			name:              "zero-trading era start needs no runway",
			immutable:         immutableFloor,
			capexReserve:      100_000,
			cargoSpendPerHour: 0,
			k:                 2,
			want:              150_000,
		},
		{
			name:              "zero runway multiplier drops the cargo term",
			immutable:         immutableFloor,
			capexReserve:      100_000,
			cargoSpendPerHour: 300_000,
			k:                 0,
			want:              150_000,
		},
		{
			name:              "no capex earmarked leaves immutable plus runway",
			immutable:         immutableFloor,
			capexReserve:      0,
			cargoSpendPerHour: 300_000,
			k:                 1,
			want:              350_000,
		},
		{
			name:              "bare immutable floor when nothing else applies",
			immutable:         immutableFloor,
			capexReserve:      0,
			cargoSpendPerHour: 0,
			k:                 2,
			want:              immutableFloor,
		},
		{
			name:              "runway scales linearly with the multiplier",
			immutable:         immutableFloor,
			capexReserve:      0,
			cargoSpendPerHour: 250_000,
			k:                 4,
			want:              1_050_000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ProbeBuyFloor(tc.immutable, tc.capexReserve, tc.cargoSpendPerHour, tc.k)
			if got != tc.want {
				t.Errorf("ProbeBuyFloor(%d, %d, %d, %d) = %d, want %d",
					tc.immutable, tc.capexReserve, tc.cargoSpendPerHour, tc.k, got, tc.want)
			}
		})
	}
}

// TestProbeBuyFloor_NeverBelowImmutable is the RULINGS #4 pin: no combination
// of inputs, however malformed, may compute a buy floor that lets a probe
// purchase dip into the immutable working-capital reserve. Every term is
// clamped non-negative and the result is floored at the immutable, so a
// garbage observation upstream can only ever make the guard stricter, never
// weaker.
func TestProbeBuyFloor_NeverBelowImmutable(t *testing.T) {
	capexes := []int64{-1_000_000, -1, 0, 1, 100_000}
	cargoSpends := []int64{-1_000_000, -1, 0, 1, 300_000}
	ks := []int{-100, -1, 0, 1, 2}

	for _, capex := range capexes {
		for _, cargo := range cargoSpends {
			for _, k := range ks {
				got := ProbeBuyFloor(immutableFloor, capex, cargo, k)
				if got < immutableFloor {
					t.Errorf("ProbeBuyFloor(%d, %d, %d, %d) = %d, which is below the immutable floor %d",
						immutableFloor, capex, cargo, k, got, immutableFloor)
				}
			}
		}
	}

	// Two negatives must not multiply into a positive runway term: a negative
	// multiplier and a negative spend both mean "no runway", not a windfall.
	if got := ProbeBuyFloor(immutableFloor, 0, -300_000, -2); got != immutableFloor {
		t.Errorf("ProbeBuyFloor with two negative runway terms = %d, want %d", got, immutableFloor)
	}

	// An overflowing runway term must fail closed to the immutable floor
	// rather than wrapping negative and waving a purchase through.
	if got := ProbeBuyFloor(immutableFloor, 0, math.MaxInt64, 4); got < immutableFloor {
		t.Errorf("ProbeBuyFloor with an overflowing runway term = %d, want >= %d", got, immutableFloor)
	}
}

func TestCargoSpendPerHour(t *testing.T) {
	tests := []struct {
		name string
		sum  int64
		want int64
	}{
		{name: "passes the observed hourly spend through", sum: 300_000, want: 300_000},
		{name: "idle hour is zero", sum: 0, want: 0},
		{
			// The named wrapper is purely for call-site readability; clamping
			// malformed sums is ProbeBuyFloor's job, not this one's.
			name: "malformed negative sum passes through untouched",
			sum:  -1,
			want: -1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CargoSpendPerHour(tc.sum); got != tc.want {
				t.Errorf("CargoSpendPerHour(%d) = %d, want %d", tc.sum, got, tc.want)
			}
		})
	}
}

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
		kMilli            int
		want              int64
	}{
		{
			// A trading fleet: the immutable floor, the capex the operation
			// has earmarked, and two hours of cargo runway on top.
			name:              "trading fleet reserves two hours of cargo runway",
			immutable:         immutableFloor,
			capexReserve:      100_000,
			cargoSpendPerHour: 300_000,
			kMilli:            2000,
			want:              750_000,
		},
		{
			// Era start: nothing is trading yet, so there is no runway to
			// protect and the floor is just the immutable plus capex.
			name:              "zero-trading era start needs no runway",
			immutable:         immutableFloor,
			capexReserve:      100_000,
			cargoSpendPerHour: 0,
			kMilli:            2000,
			want:              150_000,
		},
		{
			name:              "zero runway multiplier drops the cargo term",
			immutable:         immutableFloor,
			capexReserve:      100_000,
			cargoSpendPerHour: 300_000,
			kMilli:            0,
			want:              150_000,
		},
		{
			name:              "no capex earmarked leaves immutable plus runway",
			immutable:         immutableFloor,
			capexReserve:      0,
			cargoSpendPerHour: 300_000,
			kMilli:            1000,
			want:              350_000,
		},
		{
			name:              "bare immutable floor when nothing else applies",
			immutable:         immutableFloor,
			capexReserve:      0,
			cargoSpendPerHour: 0,
			kMilli:            2000,
			want:              immutableFloor,
		},
		{
			name:              "runway scales linearly with the multiplier",
			immutable:         immutableFloor,
			capexReserve:      0,
			cargoSpendPerHour: 250_000,
			kMilli:            4000,
			want:              1_050_000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ProbeBuyFloor(tc.immutable, tc.capexReserve, tc.cargoSpendPerHour, tc.kMilli)
			if got != tc.want {
				t.Errorf("ProbeBuyFloor(%d, %d, %d, %d) = %d, want %d",
					tc.immutable, tc.capexReserve, tc.cargoSpendPerHour, tc.kMilli, got, tc.want)
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
	ks := []int{-100000, -1, 0, 1, 400, 2000}

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

// A FRACTIONAL hour of runway is reachable, and is the reason this knob is in
// milli-hours rather than hours (2026-07-28).
//
// The live fleet measured 1,820,649/hr of cargo spend against a 773,106
// treasury. In whole hours the operator's only choices were k=1 — a floor of
// ~1.87M, so no 30k probe was ever affordable — and k=0, which removes the
// runway guard entirely. There was no setting between "blocked" and
// "unguarded", so the knob could not express the intent "hold back a bit of
// runway, but let probes through".
//
// This pins that the in-between now exists AND that it lands where arithmetic
// says it should, so a future refactor cannot quietly restore whole-hour
// granularity while still compiling.
func TestProbeBuyFloor_SubHourRunwayIsExpressible(t *testing.T) {
	const (
		liveCargoSpendPerHour = int64(1_820_649)
		noCapex               = int64(0)
	)

	fullHour := ProbeBuyFloor(immutableFloor, noCapex, liveCargoSpendPerHour, 1000)
	fourTenths := ProbeBuyFloor(immutableFloor, noCapex, liveCargoSpendPerHour, 400)
	noRunway := ProbeBuyFloor(immutableFloor, noCapex, liveCargoSpendPerHour, 0)

	// 0.4h is genuinely BETWEEN the two whole-hour settings — the property that
	// did not exist before. Asserting the ordering rather than only the values
	// is what catches a rounding change that collapses 400 onto a neighbour.
	if !(noRunway < fourTenths && fourTenths < fullHour) {
		t.Fatalf("0.4h must sit strictly between 0h and 1h: 0h=%d, 0.4h=%d, 1h=%d",
			noRunway, fourTenths, fullHour)
	}

	if want := immutableFloor + liveCargoSpendPerHour*400/1000; fourTenths != want {
		t.Errorf("0.4h runway = %d, want %d", fourTenths, want)
	}

	// The operating consequence, stated as money. NOTE the exact numbers: at the
	// live treasury 0.4h is NOT enough to afford a probe — the break-even is
	// ~0.38h — so this pins that a WORKABLE setting exists strictly below one
	// hour, not that 0.4 specifically unblocks buying. Asserting the latter is a
	// mistake worth naming: it is intuitive, it is wrong, and a test that
	// enshrined it would hand an operator a knob value that silently does
	// nothing.
	const liveTreasury, probePrice = int64(773_106), int64(30_000)
	const affordableMilli = 350

	affordable := ProbeBuyFloor(immutableFloor, noCapex, liveCargoSpendPerHour, affordableMilli)
	if liveTreasury-probePrice < affordable {
		t.Errorf("0.35h must leave a probe affordable: treasury %d - price %d < floor %d",
			liveTreasury, probePrice, affordable)
	}
	if liveTreasury-probePrice >= fullHour {
		t.Errorf("1h must NOT leave a probe affordable — this is the state that motivated milli-hours: treasury %d - price %d >= floor %d",
			liveTreasury, probePrice, fullHour)
	}
	// And 0.4h, the value that looks obviously sufficient, is still short. This
	// is the assertion that documents WHY the operator must read the arithmetic
	// rather than guess a round number.
	if liveTreasury-probePrice >= fourTenths {
		t.Errorf("0.4h was expected to still block at this treasury (break-even ~0.38h): treasury %d - price %d >= floor %d",
			liveTreasury, probePrice, fourTenths)
	}
}

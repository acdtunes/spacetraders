package fleetgrowth

import "testing"

// The two regimes this formula must serve, pinned side by side, because a change that helps one by
// disarming the other is the regression the max() exists to prevent.
//
// HIGH VELOCITY is the live staging frame of 2026-08-08 07:02: nine trade hulls recycling 4,319,078
// credits of cargo an hour and recovering more than that back through sales. The fleet's capital IS
// NOT 4.3M — it is the ~1.6M standing in the holds at any instant, turned over several times inside
// the window. The runway arm must see that and stand down.
//
// COLD START is the frame the runway arm was written for: cargo bought, nothing sold back yet. The
// spend is unrecovered, so the runway arm binds and reserves it — and the number it reserves is the
// SAME one it reserved before this change, which is what makes the cold path the anti-vacuity
// control for the high-velocity assertion above it.
func TestWorkingCapital_HighVelocityFleetReservesItsFloatNotItsTurnover(t *testing.T) {
	terms := WorkingCapital(2000, CargoOutflow{
		Spent:     4_319_078,
		Recovered: 4_750_000,
		Largest:   182_000,
		Complete:  true,
	}, 9)

	if terms.Runway != 0 {
		t.Fatalf("a fleet recovering more than it spent has no unrecovered position: runway = %d, want 0", terms.Runway)
	}
	if terms.HoldFill != 1_638_000 {
		t.Fatalf("hold-fill = %d, want 9 hulls × 182000 = 1638000", terms.HoldFill)
	}
	if got := terms.Credits(); got != 1_638_000 {
		t.Fatalf("reserve = %d, want the float 1638000 — the gross-outflow measure held 8638156 here", got)
	}
	if got := terms.Binding(); got != BindingHoldFill {
		t.Fatalf("binding arm = %q, want %q", got, BindingHoldFill)
	}
}

// THE ANTI-VACUITY CONTROL. Nothing has been sold back, so netting subtracts nothing and the reserve
// is bit-for-bit what the gross measure produced: 2h × 500000 = 1000000, over a hold-fill arm of
// 160000. A fix that quietly disarmed the runway arm would fail here.
func TestWorkingCapital_ColdStartStillBindsOnTheRunwayArm(t *testing.T) {
	terms := WorkingCapital(2000, CargoOutflow{
		Spent:     500_000,
		Recovered: 0,
		Largest:   80_000,
		Complete:  true,
	}, 2)

	if terms.Runway != 1_000_000 {
		t.Fatalf("runway = %d, want 2h × 500000 unrecovered = 1000000", terms.Runway)
	}
	if got := terms.Credits(); got != 1_000_000 {
		t.Fatalf("reserve = %d, want the runway arm at 1000000", got)
	}
	if got := terms.Binding(); got != BindingRunway {
		t.Fatalf("binding arm = %q, want %q", got, BindingRunway)
	}
}

// PARTIAL RECOVERY IS THE MIDDLE OF THE RANGE, and it is where netting earns its keep: the fleet has
// sold some of what it bought, so the position it has NOT recovered is what the runway reserves —
// not the gross spend, and not zero either.
func TestWorkingCapital_PartialRecoveryReservesOnlyTheUnrecoveredPosition(t *testing.T) {
	terms := WorkingCapital(2000, CargoOutflow{
		Spent:     500_000,
		Recovered: 200_000,
		Largest:   80_000,
		Complete:  true,
	}, 2)

	if terms.Runway != 600_000 {
		t.Fatalf("runway = %d, want 2h × (500000 − 200000) = 600000", terms.Runway)
	}
	if got := terms.Credits(); got != 600_000 {
		t.Fatalf("reserve = %d, want 600000", got)
	}
}

// AN INCOMPLETE WINDOW IS NOT A NETTED ONE. Recovery is subtracted only when the reader saw the whole
// window; a read that hit its row bound saw an unknown slice of the buys against a fuller slice of
// the sells, and netting those two would understate the position by however much it could not see.
// The reserve falls back to the gross measure — the strictest thing the observation supports.
//
// COMPLETE IS FALSE BY DEFAULT, so a caller that never sets it gets the gross reserve. The one
// direction a forgotten field may fail in is the strict one.
func TestWorkingCapital_IncompleteWindowDoesNotNet(t *testing.T) {
	obs := CargoOutflow{Spent: 4_319_078, Recovered: 4_750_000, Largest: 182_000}

	terms := WorkingCapital(2000, obs, 9)
	if terms.Runway != 8_638_156 {
		t.Fatalf("runway = %d, want the ungross-netted 2h × 4319078 = 8638156", terms.Runway)
	}
	if got := terms.Credits(); got != 8_638_156 {
		t.Fatalf("reserve = %d, want the gross fallback 8638156", got)
	}
	if got := terms.Binding(); got != BindingRunway {
		t.Fatalf("binding arm = %q, want %q", got, BindingRunway)
	}
}

// The hold-fill term dominates exactly when capacity was just added: the fresh hulls have no spend
// history, so the observed runway does not yet contain them.
func TestWorkingCapital_HoldFillDominatesOnFreshCapacity(t *testing.T) {
	terms := WorkingCapital(2000, CargoOutflow{Spent: 100_000, Largest: 80_000, Complete: true}, 5)
	if got := terms.Credits(); got != 400_000 {
		t.Fatalf("expected the hold-fill term to dominate at 400000, got %d", got)
	}
	if got := terms.Binding(); got != BindingHoldFill {
		t.Fatalf("binding arm = %q, want %q", got, BindingHoldFill)
	}
}

// A cold fleet has observed nothing, so it reserves nothing above the immutable floor. This is the
// case that proves the formula adds a term rather than replacing the floor, and the binding arm is
// named "none" rather than defaulting to an arm that did not win anything.
func TestWorkingCapital_NothingObservedReservesNothing(t *testing.T) {
	terms := WorkingCapital(2000, CargoOutflow{Complete: true}, 0)
	if got := terms.Credits(); got != 0 {
		t.Fatalf("expected 0 working capital with nothing observed, got %d", got)
	}
	if got := terms.Binding(); got != BindingNone {
		t.Fatalf("binding arm = %q, want %q", got, BindingNone)
	}
}

// A TIE IS RESOLVED TO THE RUNWAY ARM AND STAYS THERE. The number is the same either way, so this
// pins the LABEL: a binding name that flips between two arms on identical inputs is a diagnosis that
// cannot be alerted on.
func TestWorkingCapital_TieNamesTheRunwayArm(t *testing.T) {
	terms := WorkingCapital(1000, CargoOutflow{Spent: 400_000, Largest: 100_000, Complete: true}, 4)
	if terms.Runway != 400_000 || terms.HoldFill != 400_000 {
		t.Fatalf("fixture must tie the two arms: runway %d, hold-fill %d", terms.Runway, terms.HoldFill)
	}
	if got := terms.Binding(); got != BindingRunway {
		t.Fatalf("binding arm = %q, want %q on a tie", got, BindingRunway)
	}
}

// A malformed observation may only ever make the guard STRICTER, and every case pins the exact
// credit figure rather than a sign — a clamp that stops clamping must change an assertion, and a
// sign check cannot see it because the outer max() hides any single term that goes negative.
//
// Two directions are wrong and both are reachable through the products. A NEGATIVE result would
// lower the effective reserve BELOW the immutable floor, which is the weakening RULINGS #4
// forbids. Two negative factors multiplying into a phantom windfall is the mirror: money reserved
// against a commitment the fleet never made, which starves the buy path on a bad row.
//
// A NEGATIVE RECOVERY IS THE NEW MEMBER OF THAT SET, and it is wrong in the second direction: left
// alone it would SUBTRACT a negative and reserve more than the fleet ever spent. It is clamped to
// zero, which leaves the gross spend standing — strict, and bounded by something the fleet did.
func TestWorkingCapital_MalformedInputsCannotWeakenTheGuard(t *testing.T) {
	cases := []struct {
		name string
		got  int64
		want int64
	}{
		{"negative runway multiplier", WorkingCapital(-9000, CargoOutflow{Spent: 500_000, Largest: 80_000, Complete: true}, 5).Credits(), 400_000},
		{"negative spend", WorkingCapital(2000, CargoOutflow{Spent: -500_000, Largest: 80_000, Complete: true}, 5).Credits(), 400_000},
		{"both runway factors negative", WorkingCapital(-9000, CargoOutflow{Spent: -500_000, Largest: 80_000, Complete: true}, 5).Credits(), 400_000},
		{"negative recovery cannot inflate the runway", WorkingCapital(2000, CargoOutflow{Spent: 500_000, Recovered: -500_000, Complete: true}, 0).Credits(), 1_000_000},
		{"recovery over spend cannot go negative", WorkingCapital(2000, CargoOutflow{Spent: 100_000, Recovered: 9_000_000, Complete: true}, 0).Credits(), 0},
		{"negative hull count", WorkingCapital(0, CargoOutflow{Largest: 80_000, Complete: true}, -5).Credits(), 0},
		{"negative fill", WorkingCapital(0, CargoOutflow{Largest: -80_000, Complete: true}, 5).Credits(), 0},
		{"both hold-fill factors negative", WorkingCapital(0, CargoOutflow{Largest: -80_000, Complete: true}, -5).Credits(), 0},
		{"one factor of each product negative", WorkingCapital(-9000, CargoOutflow{Spent: 500_000, Largest: 80_000, Complete: true}, -5).Credits(), 0},
		{"every factor negative", WorkingCapital(-9000, CargoOutflow{Spent: -500_000, Recovered: -1, Largest: -80_000, Complete: true}, -5).Credits(), 0},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Fatalf("%s: expected %d credits reserved, got %d", tc.name, tc.want, tc.got)
		}
	}
}

// The product is formed BEFORE the divide, so a sub-hour runway cannot round to zero.
func TestWorkingCapital_SubHourRunwayDoesNotRoundAway(t *testing.T) {
	terms := WorkingCapital(400, CargoOutflow{Spent: 500_000, Complete: true}, 0)
	if got := terms.Credits(); got != 200_000 {
		t.Fatalf("expected 0.4h of a 500000 unrecovered position = 200000, got %d", got)
	}
}

package common

import (
	"context"
	"testing"
)

// TestEffectiveReserveFloor pins the working-capital floor actually enforced at a buy:
// max(50k, min(absolute, pct% × liveTreasury)). At high treasury the configured absolute
// binds; below it the proportional floor keeps buying feasible so a floor above the
// treasury can never deadlock the fleet. The 50k lower bound is immutable (RULINGS #5).
// These table cases pin the formula onto the pure resolver both engines import — it must
// not fork between tour and factory.
func TestEffectiveReserveFloor(t *testing.T) {
	const absolute = 1_000_000
	cases := []struct {
		name      string
		absolute  int64
		pct       int
		treasury  int64
		wantFloor int64
	}{
		// >2.5M: the 1M absolute binds (min picks it).
		{"absolute binds above 2.5M", absolute, 40, 3_000_000, 1_000_000},
		// Exactly 2.5M: proportional 40%×2.5M = 1M == absolute → absolute binds at the boundary.
		{"absolute binds exactly at the 2.5M crossover", absolute, 40, 2_500_000, 1_000_000},
		// 800k: proportional 320k < 1M absolute → proportional binds; allowance = 800k−320k = 480k.
		{"proportional binds at 800k treasury", absolute, 40, 800_000, 320_000},
		// 100k: proportional 40k < 50k immutable → the 50k lower bound binds (deadlock impossible).
		{"immutable 50k binds at 100k treasury", absolute, 40, 100_000, 50_000},
		// pct 0 resolves to the 40% default → identical to the 800k proportional case above.
		{"pct 0 resolves to the 40 default", absolute, 0, 800_000, 320_000},
		// A negative pct is also treated as absent → the 40 default.
		{"pct negative resolves to the 40 default", absolute, -5, 800_000, 320_000},
		// An operator-ruled higher pct raises the proportional floor (config, not a constant).
		{"a higher ruled pct raises the proportional floor", absolute, 60, 800_000, 480_000},
		// Immutable bound is never weakened even when both absolute and proportional fall below it.
		{"immutable never weakened below 50k", 30_000, 40, 40_000, 50_000},
		// At exactly the immutable treasury the floor equals the bound (allowance 0, not negative).
		{"floor equals treasury at 50k (zero, not negative, allowance)", absolute, 40, 50_000, 50_000},
		// Rounding: 40% × 200,002 = 80,000.8 → rounds half-up to 80,001.
		{"proportional rounds half up", absolute, 40, 200_002, 80_001},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveReserveFloor(tc.absolute, tc.pct, tc.treasury)
			if got != tc.wantFloor {
				t.Fatalf("EffectiveReserveFloor(%d, %d, %d) = %d, want %d",
					tc.absolute, tc.pct, tc.treasury, got, tc.wantFloor)
			}
		})
	}
}

// Invariant: the proportional floor must yield a POSITIVE allowance at every treasury at
// or above the immutable bound (an absolute-only floor can yield a negative allowance
// below it — balance − floor < 0 → no buy possible), so the fleet can always trade its
// way out.
func TestEffectiveReserveFloor_NeverDeadlocksAtOrAboveImmutable(t *testing.T) {
	const absolute = 1_000_000
	for _, treasury := range []int64{50_000, 60_000, 125_000, 300_000, 800_000, 1_000_000, 2_400_000} {
		floor := EffectiveReserveFloor(absolute, 40, treasury)
		if floor > treasury {
			t.Fatalf("treasury %d: floor %d exceeds treasury — a floor above the balance is the deadlock this bead removes", treasury, floor)
		}
	}
}

// The ctx carrier is ADDITIVE: an unstamped ctx reports ok=false so the buy-time guards
// keep enforcing the absolute floor unchanged (the trade-route circuit and every direct
// test), while a stamped pct is read back verbatim.
func TestReserveTreasuryPctContext(t *testing.T) {
	if _, ok := ReserveTreasuryPctFromContext(context.Background()); ok {
		t.Fatalf("an unstamped ctx must report ok=false (absolute floor, unchanged)")
	}
	pct, ok := ReserveTreasuryPctFromContext(WithReserveTreasuryPct(context.Background(), 55))
	if !ok || pct != 55 {
		t.Fatalf("a stamped pct must read back verbatim, got (%d, %v)", pct, ok)
	}
}

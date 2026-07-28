package probebuy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-f3mcc requirement 2: the working-capital floor is checked against the QUOTE, but the number
// that actually bounds what the counter may charge is the maxBudget handed to BuyProbe — and that
// was the 25%-of-treasury ceiling alone. Those are different numbers, and on a thin treasury the
// 25% ceiling is the LOOSER of the two, so a market that moved between the quote and the dock
// could be paid at a price the floor check never saw and would have refused.
//
// This is not hypothetical for this coordinator: the live burst climbed 23,021 → 32,648 across
// five minutes on a depleting yard, which is exactly the regime where the charge outruns the
// quote. The floor must therefore bind on what CAN BE CHARGED, not only on what was quoted.
// expansion.ProbePurchaser.BuyProbe refuses `price > maxBudget`, so bounding the budget by the
// floor headroom is what makes the guard binding on the actual charge.
//
// Mutation guard: drop the floor-bounding and the first row hands over 45,000 — enough to settle
// at a price that leaves 135,000, below the 150,000 floor.
func TestBuy_MaxBudgetIsBoundedByTheReserveFloor_NotJustTheTreasuryCeiling(t *testing.T) {
	cases := []struct {
		name       string
		floor      int64
		credits    int
		quote      int
		wantBudget int
		why        string
	}{
		{
			name:       "floor headroom is tighter than the 25% ceiling and therefore binds",
			floor:      150_000,
			credits:    180_000,
			quote:      25_000,
			wantBudget: 30_000, // credits − floor; the 25% ceiling would have been 45,000
			why:        "a charge above 30,000 would settle below the 150,000 floor",
		},
		{
			name:       "the 25% ceiling still binds when it is the tighter of the two",
			floor:      150_000,
			credits:    1_000_000,
			quote:      25_000,
			wantBudget: 250_000, // 25% of treasury; floor headroom is 850,000
			why:        "RULINGS #6 is not weakened by the floor being generous here",
		},
		{
			name:       "floor disabled (0) leaves the budget byte-identical for pre-existing callers",
			floor:      0,
			credits:    180_000,
			quote:      25_000,
			wantBudget: 45_000, // untouched 25% ceiling — the freshness sizer's wiring
			why:        "a caller that sets no floor must be unaffected by this change",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &shared.MockClock{CurrentTime: time.Now()}
			pu := &fakePurchaser{quotePrice: tc.quote, quoteYard: "X1-HQ-YARD", buySymbol: "PROBE-NEW"}
			cfg := Config{
				MaxProbeFleet:    40,
				MaxSpendPerCycle: 5_000_000, // non-binding
				PurchaseCooldown: 10 * time.Minute,
				SpendWindow:      1 * time.Hour,
				ReserveFloor:     tc.floor,
			}
			b := NewGuardedProbeBuyer(&fakeTreasury{credits: tc.credits}, pu, &fakeLedger{}, clock, cfg)

			out := b.MaybeBuy(context.Background(), shared.MustNewPlayerID(1), 10 /*demand*/, 0 /*supply*/, false, noTarget)

			require.True(t, out.Bought, "this row is a permitted buy (%s)", out.Reason)
			require.Equal(t, 1, pu.buyCalls, "exactly one probe bought")
			require.Equal(t, tc.wantBudget, pu.lastBudget, "%s", tc.why)

			// Whatever the counter may charge under that budget must still leave the floor intact.
			if tc.floor > 0 {
				require.GreaterOrEqual(t, int64(tc.credits-pu.lastBudget), tc.floor,
					"the worst-case charge permitted by the budget must not breach the floor")
			}
		})
	}
}

// The floor is a money guard, so it fails CLOSED on an unreadable treasury (RULINGS #4). The
// treasury fake is adversarial: it returns a LAVISH balance alongside its error, so a swallowed
// read error shows up as a purchase rather than as a silent zero.
func TestBuy_UnreadableTreasuryNeverBuys_EvenWithAFloorConfigured(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pu := &fakePurchaser{quotePrice: 25_000, quoteYard: "X1-HQ-YARD", buySymbol: "PROBE-NEW"}
	cfg := Config{
		MaxProbeFleet:    40,
		MaxSpendPerCycle: 5_000_000,
		PurchaseCooldown: 10 * time.Minute,
		SpendWindow:      1 * time.Hour,
		ReserveFloor:     150_000,
	}
	// A wrong-but-permissive value alongside the error: 10,000,000 credits would clear every guard.
	tr := &fakeTreasury{credits: 10_000_000, err: context.DeadlineExceeded}
	b := NewGuardedProbeBuyer(tr, pu, &fakeLedger{}, clock, cfg)

	out := b.MaybeBuy(context.Background(), shared.MustNewPlayerID(1), 10, 0, false, noTarget)

	require.False(t, out.Bought, "an unreadable treasury must never buy (%s)", out.Reason)
	require.Zero(t, pu.buyCalls, "no purchase is attempted against an unverifiable balance")
	require.Zero(t, pu.quoteCalls, "the quote is not even taken — the treasury gates first")
}

package parkedsensing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// buyqueue_pacing_test.go drives the drain twice — at the ceiling-era budget and against
// an idle request budget — over a fixture holding strictly more work than either admits.
// A queue that loops on its own constant returns the SAME numbers both times.

// buyAt drains `fills` fundable market placements plus one seed under `maxAttempts`,
// and reports the purchases split by tier.
func buyAt(t *testing.T, maxAttempts int) (BuyReport, int, int) {
	t.Helper()
	ports, pur := fundableFillPorts(40, true)

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
		BuyKnobs{SpendEnabled: true, ProbeCap: 500, MaxAttempts: maxAttempts}, seedShareClock)
	require.NoError(t, err)

	fills, seeds := 0, 0
	for _, b := range pur.buys {
		if b.yard == "X1-SEED-Y1" {
			seeds++
			continue
		}
		fills++
	}
	return rep, fills, seeds
}

// THE POINT OF THE CHANGE. Forty fundable fills sort ahead of one seed; against an idle
// budget the tick funds 24 of them AND the frontier, where the shipped six funded four.
// Both halves are asserted: the loop must spend the scaled budget, and the split must be
// taken off that same scaled value or every extra attempt goes to the fills.
func TestDrain_AnIdleRequestBudgetFundsBothTheFillsAndTheSeedsBehindThem(t *testing.T) {
	idle := PacedBudget(MaxDrainAttempts, 0, ExpansionHeadroomMultiple)
	require.Equal(t, MaxDrainAttempts*ExpansionHeadroomMultiple, idle, "the fixture must exercise the whole headroom")

	rep, fills, seeds := buyAt(t, idle)

	require.Equal(t, idle-seedAttemptShare(idle), fills, "the fills spend the SCALED budget's share, not the constant's")
	require.Equal(t, 1, seeds, "the one queued seed is still reached behind a wall of fills")
	require.Equal(t, idle, rep.AttemptLimit)
}

// …and a saturated budget behaves exactly as it always did: four fills, the seed, six
// attempts. This is the byte-identical end, and it is what makes the scaling safe.
func TestDrain_AFullyBoundBudgetBuysExactlyWhatItShipped(t *testing.T) {
	rep, fills, seeds := buyAt(t, PacedBudget(MaxDrainAttempts, 1000, ExpansionHeadroomMultiple))

	require.Equal(t, MaxDrainAttempts-seedAttemptReserve, fills)
	require.Equal(t, 1, seeds, "the lone seed fills in one attempt; the rest of its share goes unspent")
	require.LessOrEqual(t, rep.Attempts, MaxDrainAttempts, "the split must never extend the budget")
	require.Equal(t, MaxDrainAttempts, rep.AttemptLimit)
}

// An unwired coordinator paces exactly as before, and says so in the report.
func TestDrain_ANonPositiveBudgetIsTheShippedCap(t *testing.T) {
	rep, fills, _ := buyAt(t, 0)

	require.Equal(t, MaxDrainAttempts-seedAttemptReserve, fills)
	require.Equal(t, MaxDrainAttempts, rep.AttemptLimit)
}

// THE SPLITS DIVIDE THE BUDGET, AND SEEDS KEEP A SHARE AT BOTH ENDS — a scaled budget
// that still hands the frontier nothing would fix nothing. The coverage tier is carved
// out of the SCALED fill budget, so an operator's absolute reserve cannot swallow it.
func TestSeedShare_TheSplitsAreTakenOffTheScaledBudgetAtBothEnds(t *testing.T) {
	seed := []QueuedSlot{{Waypoint: "X1-S-Y1", System: "X1-S", Kind: SlotKindSpare}}
	idle := PacedBudget(MaxDrainAttempts, 0, ExpansionHeadroomMultiple)

	for _, budget := range []int{MaxDrainAttempts, 34, idle} {
		fill := fillAttemptBudget(seed, budget)
		require.Positive(t, budget-fill, "seeds keep a non-zero share at budget %d", budget)
		require.Less(t, budget-fill, budget/2, "and never a share that outranks known-good coverage")
		require.Equal(t, fill-2, coverageFillBudget(fill, 2, true), "the coverage tier splits the SCALED fill budget")
	}

	require.Equal(t, MaxDrainAttempts-seedAttemptReserve, fillAttemptBudget(seed, MaxDrainAttempts),
		"byte-identical at the budget it shipped with")
	require.Equal(t, idle, fillAttemptBudget(nil, idle), "no seed outstanding, no reserve")
}

// The share is FLOORED, so no reading can pace the frontier below what it shipped with.
func TestSeedShare_NeverFallsBelowTheShippedReserve(t *testing.T) {
	for _, budget := range []int{0, 1, MaxDrainAttempts - 1, MaxDrainAttempts} {
		require.GreaterOrEqual(t, seedAttemptShare(budget), seedAttemptReserve, "budget %d", budget)
	}
}

// EVERY ECONOMIC REFUSAL STILL REFUSES AT A SCALED BUDGET (RULINGS #4). The budget may
// only raise how many attempts a tick makes; what any one of them may pay is untouched.
func TestDrain_TheGuardsStillBindAgainstAnIdleBudget(t *testing.T) {
	idle := PacedBudget(MaxDrainAttempts, 0, ExpansionHeadroomMultiple)

	t.Run("probe cap", func(t *testing.T) {
		ports, led, pur := multiSystemPorts()
		led.owned = 4
		rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
			BuyKnobs{SpendEnabled: true, ProbeCap: 4, MaxAttempts: idle}, fixedClock{seedShareClock.now})
		require.NoError(t, err)
		require.Empty(t, pur.buys)
		require.True(t, rep.CapHeld)
	})

	t.Run("buy floor", func(t *testing.T) {
		ports, _, pur := multiSystemPorts()
		pur.price = 20_000
		ports.Treasury = &fakeTreasury{credits: 80_000}
		rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
			BuyKnobs{SpendEnabled: true, ProbeCap: 500, MaxAttempts: idle}, fixedClock{seedShareClock.now})
		require.NoError(t, err)
		require.Len(t, pur.buys, 1, "a wider budget must not buy past the floor")
		require.True(t, rep.FloorHeld)
	})

	t.Run("spend switch", func(t *testing.T) {
		ports, _, pur := multiSystemPorts()
		rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID,
			BuyKnobs{SpendEnabled: false, ProbeCap: 500, MaxAttempts: idle}, fixedClock{seedShareClock.now})
		require.NoError(t, err)
		require.Empty(t, pur.buys)
		require.True(t, rep.SpendingPaused)
	})
}

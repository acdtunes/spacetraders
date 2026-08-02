package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

func pricedLane(good string, ask, optimalUnits int, perHour float64) pricedLongHaulLane {
	return pricedLongHaulLane{
		longHaulCandidate: longHaulCandidate{Lane: trading.ArbitrageLane{
			Good: good, SourceAsk: ask, DestBid: ask + 5000, SpreadPerUnit: 5000, VolumeCap: 1000,
		}},
		OptimalUnits:           optimalUnits,
		RealizedCreditsPerHour: perHour,
	}
}

// BUY SIZING (design §2/§3): achievable = min(optimal_q, hold, spend_ceiling/ask, absorption
// headroom). Each bound wins when it is the tightest; a zero hold sizes to zero.
func TestLongHaulAchievableUnits_TightestBound(t *testing.T) {
	deepEnv := newLongHaulEnvelope(newLongHaulFence(50_000_000), 1_000_000) // spendCeiling = 1M

	cases := []struct {
		name           string
		lane           pricedLongHaulLane
		hold, headroom int
		want           int
	}{
		{"hold-bound", pricedLane("G", 1000, 100, 1), 40, -1, 40},
		{"optimal-bound", pricedLane("G", 1000, 30, 1), 40, -1, 30},
		{"spend-bound", pricedLane("G", 100_000, 100, 1), 40, -1, 10}, // 1M / 100k = 10
		{"absorption-bound", pricedLane("G", 1000, 100, 1), 40, 12, 12},
		{"zero hold -> 0", pricedLane("G", 1000, 100, 1), 0, -1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, achievableUnits(tc.lane, tc.hold, deepEnv, tc.headroom))
		})
	}
}

// SELECTION (design §2/§3): the worker takes the highest realized-$/hr lane it can actually
// size a positive buy for — a top-ranked lane it cannot afford a single unit of is skipped to
// the next.
func TestLongHaulSelectHauls_PicksTopSizeableLane(t *testing.T) {
	env := newLongHaulEnvelope(newLongHaulFence(50_000_000), 1_000_000) // spendCeiling = 1M
	// A ranks highest but is unaffordable at any unit (ask 2M > 1M ceiling) -> sizes to 0 -> skipped.
	a := pricedLane("A", 2_000_000, 5, 100.0)
	b := pricedLane("B", 1000, 40, 50.0)

	hauls := selectHauls([]pricedLongHaulLane{a, b}, 40, env, nil)

	require.Len(t, hauls, 1, "the unaffordable top lane is dropped, not returned")
	require.Equal(t, "B", hauls[0].lane.Lane.Good, "the top lane that cannot be sized is skipped to the next tradeable one")
	require.Equal(t, 40, hauls[0].units)
}

// Fail-closed: an unreadable treasury sizes every lane to zero (spendCeiling 0), so the worker
// selects NOTHING rather than spend blind (RULINGS #4).
func TestLongHaulSelectHauls_UnreadableTreasury_SelectsNothing(t *testing.T) {
	env := newLongHaulEnvelope(unreadableLongHaulFence(), 1_000_000)

	hauls := selectHauls([]pricedLongHaulLane{pricedLane("A", 1000, 40, 50)}, 40, env, nil)

	require.Empty(t, hauls)
}

package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// longHaulTestModel is the era-3 operational impact model (config.TradeImpactConfig
// defaults): the ask rises 5% per full trade_volume bought, the bid falls 1.5% per full
// trade_volume sold — the SAME both-side model the circuit ranker uses.
func longHaulTestModel() laneImpactModel {
	return laneImpactModel{buyImpact: 0.050, sellImpact: 0.015}
}

func lhLane(good, srcWp, dstWp string, ask, bid, volumeCap int) trading.ArbitrageLane {
	return trading.ArbitrageLane{
		Good:           good,
		SourceWaypoint: srcWp,
		DestWaypoint:   dstWp,
		SourceAsk:      ask,
		DestBid:        bid,
		SpreadPerUnit:  bid - ask,
		VolumeCap:      volumeCap,
	}
}

// REALIZED PRICE-IMPACT RANKING (design §2): a thin lane with the single biggest per-unit
// spread ranks BELOW a solid, deep lane — because realized $/hr is (realized net at the
// impact-optimal tranche) / trip time, and the thin lane can only move a sliver before its
// depth runs out. A naive per-unit-spread ranker (or even a capped-spread one) would mis-rank
// the huge-spread lane on top.
func TestLongHaulRank_RealizedPriceImpact_ThinHugeSpreadRanksBelowSolidDeep(t *testing.T) {
	model := longHaulTestModel()
	// THIN: the biggest per-unit spread (20k) but only 2 units of depth (a real exotic sink).
	thin := longHaulCandidate{Lane: lhLane("LASER_RIFLES", "X1-SRC-A1", "X1-XD86-A1", 5000, 25000, 2), GateHops: 3}
	// DEEP: a solid 3k spread over 60 units of depth — the realistically fillable lane.
	deep := longHaulCandidate{Lane: lhLane("AI_MAINFRAMES", "X1-VM73-AX8E", "X1-UM5-A1", 4000, 7000, 60), GateHops: 3}

	ranked := rankLongHaulLanes(model, []longHaulCandidate{thin, deep}, 100, 0)

	require.Len(t, ranked, 2)
	require.Equal(t, "AI_MAINFRAMES", ranked[0].Lane.Good,
		"the deep fillable lane out-ranks the thin huge-spread one on realized $/hr")
	require.Equal(t, "LASER_RIFLES", ranked[1].Lane.Good)
	require.Greater(t, ranked[0].RealizedCreditsPerHour, ranked[1].RealizedCreditsPerHour)
	require.Equal(t, 2, ranked[1].OptimalUnits, "the thin lane's tranche is capped at its 2-unit depth")
}

// OPTIMAL VOLUME (design §2): on a deep-cap lane whose thin per-unit spread is smaller than
// the terminal both-side impact, the marginal unit's realized spread collapses below the
// floor well before the absorption cap — so the buy is sized short of both the cap AND a
// heavy's full hold. This is "never oversells into a collapsing market".
func TestLongHaulOptimalVolume_CapsBuyShortOfCollapsingMarket(t *testing.T) {
	model := longHaulTestModel()
	// spread 400 < terminal impact K = 0.05*8000 + 0.015*8400 = 526, so 200-unit depth is
	// never fully clearable at a 100/unit floor.
	lane := lhLane("HOLOGRAPHICS", "X1-SRC-A1", "X1-HU21-A1", 8000, 8400, 200)

	q := model.optimalVolume(lane, 100)

	require.Positive(t, q)
	require.Less(t, q, lane.VolumeCap, "impact caps the buy short of the absorption bound")
	require.Less(t, q, 200, "and well under a heavy's full hold")
	// the boundary is real: the q-th unit clears the floor, the (q+1)-th does not.
	require.GreaterOrEqual(t, model.marginalSpreadAt(lane, q), 100.0)
	require.Less(t, model.marginalSpreadAt(lane, q+1), 100.0)
}

// optimalVolume fail-safe bounds (RULINGS #4 — a money-sizing decision fails closed).
func TestLongHaulOptimalVolume_FailSafeBounds(t *testing.T) {
	model := longHaulTestModel()
	deep := lhLane("G", "X1-S-A1", "X1-D-A1", 8000, 8400, 200) // spread 400

	cases := []struct {
		name  string
		lane  trading.ArbitrageLane
		model laneImpactModel
		floor float64
		want  int
	}{
		{"floor above spread -> 0", deep, model, 500, 0},
		{"floor equals spread -> 0", deep, model, 400, 0},
		{"unknown depth (VolumeCap 0) -> 0 (fail-closed)", lhLane("G", "X1-S-A1", "X1-D-A1", 8000, 8400, 0), model, 100, 0},
		{"inert model (no coefficients) -> full absorption cap", deep, laneImpactModel{}, 100, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.model.optimalVolume(tc.lane, tc.floor))
		})
	}
}

// Route fuel enters the realized net honestly (design §2: net/unit = bid − ask − fuel over
// the real route): a lane whose fuel cost swallows its realized gross is dropped, not ranked.
func TestLongHaulRank_DropsLaneWhoseRouteFuelSwallowsTheNet(t *testing.T) {
	model := longHaulTestModel()
	// realized gross on 2 units of a 20k spread ~ 39k; a 40k fuel bill turns it negative.
	fuelHeavy := longHaulCandidate{Lane: lhLane("LASER_RIFLES", "X1-SRC-A1", "X1-XD86-A1", 5000, 25000, 2), GateHops: 6, FuelCreditsOverRoute: 40000}
	good := longHaulCandidate{Lane: lhLane("AI_MAINFRAMES", "X1-VM73-AX8E", "X1-UM5-A1", 4000, 7000, 60), GateHops: 3, FuelCreditsOverRoute: 500}

	ranked := rankLongHaulLanes(model, []longHaulCandidate{fuelHeavy, good}, 100, 0)

	require.Len(t, ranked, 1, "the fuel-swallowed lane is dropped (non-positive realized net)")
	require.Equal(t, "AI_MAINFRAMES", ranked[0].Lane.Good)
}

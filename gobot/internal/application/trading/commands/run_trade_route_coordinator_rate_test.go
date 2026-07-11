package commands

// sp-1wp8: circuit lanes are RATE-ranked — hold-fit-weighted per-circuit value over
// estimated circuit hours — replacing the subtractive per-unit gate penalty. The time
// model: an in-system circuit at the fleet's observed home-lane class
// (homeLaneClassCircuitValue / hullOpportunityCreditsPerSecond = 4000s), a
// gate-crossing circuit that plus the observed round-trip jump+cooldown surcharge
// (2 × 352s ⇒ a ~17.6% time premium). Bid-floor discipline (ClearsFloor /
// selectLane), the absorption consult, and every lane's REAL economics are untouched
// — rate reorders choice only.

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// The divergence case (the acceptance shape): a slightly-bigger cross-system lane
// whose value lead (345k vs 320k, +7.8%) is SMALLER than the gate's time premium
// (+17.6%) must LOSE the rate ranking to the fast home lane — under the absolute
// CappedSpread order (and under the old subtractive penalty at heavy capacity,
// charge ~110/u → effective 294,400) it would have WON.
func TestLaneRateRanking_FastHomeLaneBeatsSlowBiggerCrossLane(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		{Good: "CROSS_BIG", SourceWaypoint: "X1-HOME-1", DestWaypoint: "X1-FAR-9", SpreadPerUnit: 750, VolumeCap: 460, CappedSpread: 345000},
		{Good: "HOME_FAST", SourceWaypoint: "X1-HOME-2", DestWaypoint: "X1-HOME-3", SpreadPerUnit: 800, VolumeCap: 400, CappedSpread: 320000},
	}

	got := rankLanesByCircuitRate(lanes, 400, "")

	// HOME_FAST: 800×400×1.0 = 320,000 / (4000s/3600) = 288,000/hr.
	// CROSS_BIG: 750×460×1.0 = 345,000 / (4704s/3600) = 264,031/hr.
	if got[0].Good != "HOME_FAST" {
		t.Fatalf("rate ranking must prefer the fast home lane (288k/hr) over the bigger cross lane (264k/hr), got order %v", laneOrder(got))
	}
	// Ranking-only: real economics unmutated.
	if got[1].SpreadPerUnit != 750 || got[1].CappedSpread != 345000 {
		t.Fatalf("the demoted lane's real economics must survive unmutated, got %+v", got[1])
	}
}

// Equal-rate ties break on ABSOLUTE per-circuit value: a cross lane whose value is
// bigger by exactly the time premium (352,800 = 300,000 × 4704/4000) ties the home
// lane's rate to the float — the tie must go to the bigger absolute earner.
func TestLaneRateRanking_EqualRateTieBreaksOnAbsoluteValue(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		{Good: "HOME_EVEN", SourceWaypoint: "X1-HOME-1", DestWaypoint: "X1-HOME-2", SpreadPerUnit: 750, VolumeCap: 400, CappedSpread: 300000},
		{Good: "CROSS_EVEN", SourceWaypoint: "X1-HOME-3", DestWaypoint: "X1-FAR-9", SpreadPerUnit: 735, VolumeCap: 480, CappedSpread: 352800},
	}

	// capacity 400: HOME_EVEN weight min(400,400)/400=1 → value 300,000, rate
	// 300,000×3600/4000 = 270,000/hr exactly. CROSS_EVEN weight min(480,400)/400=1
	// → value 352,800, rate 352,800×3600/4704 = 270,000/hr exactly.
	got := rankLanesByCircuitRate(lanes, 400, "")

	if got[0].Good != "CROSS_EVEN" {
		t.Fatalf("equal-rate tie must break on absolute per-circuit value (352,800 > 300,000), got order %v", laneOrder(got))
	}
}

// The --dest directed waiver survives in rate form: the operator's directed lane is
// ranked at the in-system baseline (its gate time is already the operator's explicit
// choice), every other cross lane still pays the surcharge. Two identical cross
// lanes: the directed one must out-rank the undirected one.
func TestLaneRateRanking_DirectedLaneRanksAtInSystemBaseline(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		{Good: "CROSS_A", SourceWaypoint: "X1-HOME-1", DestWaypoint: "X1-FARA-7", SpreadPerUnit: 800, VolumeCap: 400, CappedSpread: 320000},
		{Good: "CROSS_B", SourceWaypoint: "X1-HOME-2", DestWaypoint: "X1-FARB-8", SpreadPerUnit: 800, VolumeCap: 400, CappedSpread: 320000},
	}

	got := rankLanesByCircuitRate(lanes, 400, "X1-FARB-8")

	if got[0].Good != "CROSS_B" {
		t.Fatalf("the directed lane must rank at the in-system baseline (surcharge waived) and beat its identical undirected twin, got order %v", laneOrder(got))
	}
}

// The time model itself, pinned:
//   - both estimates are positive (a rate can never divide by zero — the structural
//     form of the sp-1wp8 zero-time regression pin at this surface);
//   - the cross-system surcharge stays a BOUNDED time premium below the DP51 value
//     ratio (409,920/335,033 ≈ 1.2235): the captain-ruled economics (a deep frontier
//     lane a heavy hull fills out-ranks a saturated home lane) hold under rate
//     ranking exactly as they did under the retired subtractive penalty. A
//     travel-only baseline (~720s) would breach this and silently flip the ruling.
func TestEstimatedCircuitSeconds_PreservesDP51TimePremiumBound(t *testing.T) {
	inSystem := estimatedCircuitSeconds(false)
	cross := estimatedCircuitSeconds(true)
	if inSystem <= 0 || cross <= inSystem {
		t.Fatalf("estimates must be positive and cross > in-system, got in=%f cross=%f", inSystem, cross)
	}
	const dp51ValueRatio = 409920.0 / 335033.0
	if ratio := cross / inSystem; ratio >= dp51ValueRatio {
		t.Fatalf("cross/in-system time ratio %.4f must stay below the DP51 value ratio %.4f or the captain-ruled frontier economics flip", ratio, dp51ValueRatio)
	}
}

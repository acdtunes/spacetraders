package commands

import (
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// sp-xwa1 established that cross-system lanes are charged the TIME a gate-crossing
// circuit spends jumping and cooling down; sp-1wp8 re-expresses that charge honestly
// as RATE ranking — per-circuit value over estimated circuit hours — retiring the
// capacity-amortized per-unit haircut. The captain-ruled DP51 economics these tests
// pin are MECHANISM-INDEPENDENT and must survive both forms: a genuinely-deep
// frontier lane a heavy hull can fill WINS an undirected scan, a genuinely-better
// home lane still wins outright, and the --dest waiver still exempts the operator's
// directed lane from the gate charge.
//
// (The retired subtractive model's capacity-flip property — the same cross lane
// losing on a light hull and winning on a heavy one purely via per-unit charge
// amortization — was an artifact of expressing time in spread space and does not
// exist under rate ranking: circuit time is a fixed divisor, and hull capacity
// enters ranking through hold-fit weighting alone, exactly as it does for
// same-system lanes.)

// heavyHullCapacity mirrors the DP51-class freighter the ruling was measured on.
const heavyHullCapacity = 480

// Sanity property (the ruling's required test, not a comment): with numbers
// shaped like the live DP51 +357k round-trip evidence, a DEEP cross-system
// frontier lane a heavy hull fills OUT-RANKS a saturated home lane it genuinely
// beats on time-adjusted value, while a genuinely-better home lane STILL WINS.
//
// Rates at heavyHullCapacity=480 (rate = spread × cap × holdFit / circuit hours;
// in-system 4000s = 1.111h, cross 4704s = 1.3067h):
//   - frontier (cross): 854×480×1.0      = 409,920 / 1.3067h = 313,714/hr
//   - saturatedHome   : 760×460×(460/480) = 335,033 / 1.111h = 301,530/hr
//   - betterHome      : 820×480×1.0      = 393,600 / 1.111h = 354,240/hr
//
// so the order is betterHome > frontier > saturatedHome — the SAME order the
// captain ruled under the subtractive model. The win holds because the gate's
// time premium (4704/4000 ≈ 1.176×) is smaller than the frontier lane's value
// lead over the saturated home lane (409,920/335,033 ≈ 1.224×); the ratio pin
// in run_trade_route_coordinator_rate_test.go keeps that inequality honest.
func TestCrossSystemRate_DP51ClassFrontierWinsAutonomousSelection(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		// DP51-class deep frontier lane: crosses the gate, but a heavy hull fills its
		// deep market (VolumeCap 480) at a fat spread — ~357k net per round trip.
		{Good: "FRONTIER", SourceWaypoint: "X1-HOME-1", DestWaypoint: "X1-DEEP-9", SpreadPerUnit: 854, VolumeCap: 480, CappedSpread: 409920},
		// Saturated home lane: same-system, decent volume but a traded-down spread.
		// It must not out-rank the frontier lane the fleet measured as richer per hour.
		{Good: "SATURATED_HOME", SourceWaypoint: "X1-HOME-2", DestWaypoint: "X1-HOME-3", SpreadPerUnit: 760, VolumeCap: 460, CappedSpread: 349600},
		// Genuinely-better home lane: same-system, higher time-adjusted value than the
		// frontier lane — it must still win outright (the time model is not a blanket
		// "cross-system always loses" nor "cross-system always wins" lever).
		{Good: "BETTER_HOME", SourceWaypoint: "X1-HOME-4", DestWaypoint: "X1-HOME-5", SpreadPerUnit: 820, VolumeCap: 480, CappedSpread: 393600},
	}

	got := rankLanesByCircuitRate(lanes, heavyHullCapacity, "")

	pos := laneRankPositions(got)
	// Property 2: a genuinely-better home lane still wins the autonomous scan.
	if pos["BETTER_HOME"] != 0 {
		t.Fatalf("expected BETTER_HOME ranked first (genuinely-better home lane still wins), got order %v", laneOrder(got))
	}
	// Property 1: the DP51-class frontier lane out-ranks the saturated home lane.
	if pos["FRONTIER"] >= pos["SATURATED_HOME"] {
		t.Fatalf("expected the DP51-class FRONTIER lane to out-rank SATURATED_HOME, got order %v", laneOrder(got))
	}

	// Ranking-only: the frontier lane's REAL economics survive unmutated (the
	// executor and ClearsFloor read these true numbers, never the rate score).
	frontier := got[pos["FRONTIER"]]
	if frontier.SpreadPerUnit != 854 || frontier.CappedSpread != 409920 {
		t.Fatalf("frontier lane's real spread/capped-spread must survive unmutated, got %+v", frontier)
	}

	// Non-vacuity: the win exists BECAUSE the gate surcharge is a bounded time
	// premium against a full circuit's wall-clock. If the time model ever regressed
	// to a travel-only baseline, cross/in-system would balloon past the DP51 value
	// ratio and property 1 would silently break (the explicit ratio pin lives in
	// TestEstimatedCircuitSeconds_PreservesDP51TimePremiumBound).
	if ratio := estimatedCircuitSeconds(true) / estimatedCircuitSeconds(false); ratio >= 409920.0/335033.0 {
		t.Fatalf("cross/in-system time ratio %.4f has grown past the DP51 value ratio — the frontier win is vacuous", ratio)
	}
}

// The selection log one-liner surfaces the exact RATE the ranker scored a lane
// (sp-1wp8) so the captain can read WHY a lane won or lost: a cross-system token
// carries its surcharged rate, a same-system token its baseline rate, and a
// directed --dest lane ranked at the in-system baseline reads (x-waived).
func TestLaneSelectionOneLiner_CarriesCircuitRate(t *testing.T) {
	cross := trading.ArbitrageLane{Good: "DEEP", SourceWaypoint: "X1-AAA-1", DestWaypoint: "X1-BBB-2", SpreadPerUnit: 1700, VolumeCap: 480}
	same := trading.ArbitrageLane{Good: "LOCAL", SourceWaypoint: "X1-AAA-1", DestWaypoint: "X1-AAA-2", SpreadPerUnit: 1500, VolumeCap: 480}

	t.Run("cross-system token carries the surcharged rate, m stays the raw spread", func(t *testing.T) {
		got := laneSelectionOneLiner(cross, heavyHullCapacity, "")
		// 1700×480×1.0 = 816,000 / (4704s/3600) = 624,489.8 → rate=624490/hr.
		for _, want := range []string{"m=1700", "cross", "rate=624490/hr"} {
			if !strings.Contains(got, want) {
				t.Fatalf("expected one-liner to contain %q, got: %s", want, got)
			}
		}
		if strings.Contains(got, "waived") {
			t.Fatalf("an undirected cross-system lane pays the surcharge, not a waiver, got: %s", got)
		}
	})

	t.Run("same-system token carries its baseline rate", func(t *testing.T) {
		got := laneSelectionOneLiner(same, heavyHullCapacity, "")
		// 1500×480×1.0 = 720,000 / (4000s/3600) = 648,000/hr.
		for _, want := range []string{"m=1500", "same", "rate=648000/hr"} {
			if !strings.Contains(got, want) {
				t.Fatalf("expected one-liner to contain %q, got: %s", want, got)
			}
		}
	})

	t.Run("directed target lane reads its waived (baseline) rate", func(t *testing.T) {
		got := laneSelectionOneLiner(cross, heavyHullCapacity, "X1-BBB-2")
		// 816,000 / (4000s/3600) = 734,400/hr at the in-system baseline.
		if !strings.Contains(got, "rate=734400/hr(x-waived)") {
			t.Fatalf("expected the directed lane's rate at the in-system baseline with the x-waived marker, got: %s", got)
		}
		if strings.Contains(got, "rate=624490/hr") {
			t.Fatalf("a waived lane must not report the surcharged rate, got: %s", got)
		}
	})
}

// laneOrder / laneRankPositions render a ranked slice into assert-friendly
// shapes: the Good symbols in rank order, and a good->index map.
func laneOrder(lanes []trading.ArbitrageLane) []string {
	order := make([]string, len(lanes))
	for i, l := range lanes {
		order[i] = l.Good
	}
	return order
}

func laneRankPositions(lanes []trading.ArbitrageLane) map[string]int {
	pos := make(map[string]int, len(lanes))
	for i, l := range lanes {
		pos[l.Good] = i
	}
	return pos
}

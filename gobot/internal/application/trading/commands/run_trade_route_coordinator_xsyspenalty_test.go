package commands

import (
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// sp-xwa1 (autonomous half): the cross-system ranking penalty is now the
// capacity-amortized time-opportunity cost of the round-trip jump+cooldown
// detour (crossSystemPenaltyPerUnit), NOT the retired flat -200/unit. These
// tests pin the economics the captain ruling required: a genuinely-deep
// frontier lane a heavy hull can fill must be able to WIN an undirected scan,
// while a genuinely-better home lane still wins, and the --dest waiver still
// zeroes the charge for the operator's directed lane.

// heavyHullCapacity/lightHullCapacity bracket the ~264-unit pivot where the
// amortized charge equals the retired flat 200: a heavy freighter is charged
// LESS than the old flat rate (the deep-frontier unlock), a light hauler MORE.
const (
	heavyHullCapacity = 480
	lightHullCapacity = 240
)

// Sanity property (the ruling's required test, not a comment): with numbers
// shaped like the live DP51 +357k round-trip evidence, a DEEP cross-system
// frontier lane a heavy hull fills OUT-RANKS a saturated home lane it genuinely
// beats on time-adjusted value, while a genuinely-better home lane STILL WINS.
//
// Scores at heavyHullCapacity=480 (score = effectiveSpread * VolumeCap * holdFit):
//   - frontier (cross): (854-110)*480*1.0   = 357120  (~357k net, DP51-class)
//   - saturatedHome    : 760*460*(460/480)  = 335033
//   - betterHome       : 820*480*1.0        = 393600
//
// so the order is betterHome > frontier > saturatedHome. Under the RETIRED flat
// -200 the frontier would have scored (854-200)*480 = 313920, BELOW
// saturatedHome's 335033 — i.e. it would have LOST to the saturated home lane,
// the exact bug this bead retires. The capacity-amortized charge (110, well
// under the flat 200) is what flips it to a win.
func TestCrossSystemPenalty_DP51ClassFrontierWinsAutonomousSelection(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		// DP51-class deep frontier lane: crosses the gate, but a heavy hull fills its
		// deep market (VolumeCap 480) at a fat spread — ~357k net per round trip.
		{Good: "FRONTIER", SourceWaypoint: "X1-HOME-1", DestWaypoint: "X1-DEEP-9", SpreadPerUnit: 854, VolumeCap: 480, CappedSpread: 409920},
		// Saturated home lane: same-system, decent volume but a traded-down spread.
		// The retired flat penalty let it out-rank the frontier lane; the time-cost
		// penalty must not.
		{Good: "SATURATED_HOME", SourceWaypoint: "X1-HOME-2", DestWaypoint: "X1-HOME-3", SpreadPerUnit: 760, VolumeCap: 460, CappedSpread: 349600},
		// Genuinely-better home lane: same-system, higher time-adjusted value than the
		// frontier lane — it must still win outright (the penalty is not a blanket
		// "cross-system always loses" nor "cross-system always wins" lever).
		{Good: "BETTER_HOME", SourceWaypoint: "X1-HOME-4", DestWaypoint: "X1-HOME-5", SpreadPerUnit: 820, VolumeCap: 480, CappedSpread: 393600},
	}

	got := rankLanesWithGatePenalty(lanes, heavyHullCapacity, "")

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
	// executor and ClearsFloor read these true numbers, never the penalized score).
	frontier := got[pos["FRONTIER"]]
	if frontier.SpreadPerUnit != 854 || frontier.CappedSpread != 409920 {
		t.Fatalf("frontier lane's real spread/capped-spread must survive unmutated, got %+v", frontier)
	}

	// Non-vacuity: the win exists BECAUSE the heavy hull's amortized charge is
	// materially below the retired flat rate. If that ever regressed to >= the
	// flat 200, the frontier lane would fall back below SATURATED_HOME (313920 <
	// 335033) and property 1 would silently break.
	if charge := crossSystemPenaltyPerUnit(heavyHullCapacity); charge >= legacyFlatCrossSystemPenaltyPerUnit {
		t.Fatalf("heavy hull must be charged LESS than the retired flat %d/unit for the DP51 unlock, got %d", legacyFlatCrossSystemPenaltyPerUnit, charge)
	}
}

// The per-unit charge scales INVERSELY with hull capacity: a bigger hull
// amortizes the same fixed round-trip time cost over more units, so it pays a
// smaller per-unit penalty. This is the property that lets a heavy freighter
// win a deep frontier lane a light hauler correctly loses.
func TestCrossSystemPenalty_ScalesInverselyWithHullCapacity(t *testing.T) {
	t.Run("bigger hull, smaller per-unit charge", func(t *testing.T) {
		heavy := crossSystemPenaltyPerUnit(heavyHullCapacity)
		light := crossSystemPenaltyPerUnit(lightHullCapacity)
		if !(heavy < light) {
			t.Fatalf("expected heavy-hull charge (%d) < light-hull charge (%d)", heavy, light)
		}
		if heavy <= 0 || light <= 0 {
			t.Fatalf("both charges must be positive, got heavy=%d light=%d", heavy, light)
		}
	})

	t.Run("straddles the retired flat rate: light over, heavy under", func(t *testing.T) {
		// A very light hauler (40 units) crossing a gate for a tiny load genuinely
		// wastes the ~700s round trip — it is charged MORE than the old flat 200,
		// which under-charged it. A heavy freighter is charged LESS.
		if got := crossSystemPenaltyPerUnit(40); got <= legacyFlatCrossSystemPenaltyPerUnit {
			t.Fatalf("a 40-unit hauler must be charged MORE than the retired flat %d/unit, got %d", legacyFlatCrossSystemPenaltyPerUnit, got)
		}
		if got := crossSystemPenaltyPerUnit(heavyHullCapacity); got >= legacyFlatCrossSystemPenaltyPerUnit {
			t.Fatalf("a heavy hull must be charged LESS than the retired flat %d/unit, got %d", legacyFlatCrossSystemPenaltyPerUnit, got)
		}
	})

	t.Run("no ship context falls back to the retired flat rate", func(t *testing.T) {
		for _, cap := range []int{0, -5} {
			if got := crossSystemPenaltyPerUnit(cap); got != legacyFlatCrossSystemPenaltyPerUnit {
				t.Fatalf("shipCapacity=%d must fall back to the flat %d/unit, got %d", cap, legacyFlatCrossSystemPenaltyPerUnit, got)
			}
		}
	})

	// End-to-end through the ranker: the SAME cross-system lane that loses to a
	// home lane on a light hull WINS on a heavy hull, purely because its per-unit
	// charge shrank. VolumeCap 700 >= both hulls, so hold-fit weight is 1.0 for
	// both lanes at both capacities — the flip is the penalty alone, not the
	// absorption weight.
	t.Run("ranker: cross-system lane loses on a light hull, wins on a heavy one", func(t *testing.T) {
		lanes := []trading.ArbitrageLane{
			{Good: "FRONTIER", SourceWaypoint: "X1-HOME-1", DestWaypoint: "X1-DEEP-9", SpreadPerUnit: 900, VolumeCap: 700, CappedSpread: 630000},
			{Good: "HOME", SourceWaypoint: "X1-HOME-2", DestWaypoint: "X1-HOME-3", SpreadPerUnit: 700, VolumeCap: 700, CappedSpread: 490000},
		}

		lightRanked := rankLanesWithGatePenalty(lanes, lightHullCapacity, "")
		if lightRanked[0].Good != "HOME" {
			t.Fatalf("light hull: expected HOME first (FRONTIER charged 220 -> effective 680 < 700), got %v", laneOrder(lightRanked))
		}

		heavyRanked := rankLanesWithGatePenalty(lanes, heavyHullCapacity, "")
		if heavyRanked[0].Good != "FRONTIER" {
			t.Fatalf("heavy hull: expected FRONTIER first (charged 110 -> effective 790 > 700), got %v", laneOrder(heavyRanked))
		}
	})
}

// The --dest directed waiver must zero the charge for the targeted lane
// regardless of the penalty MODEL (flat or capacity-based): this pins the
// waiver at a NONZERO capacity, where the retired flat-penalty tests
// (shipCapacity=0) never exercised the time-opportunity-cost path.
func TestRankLanesWithGatePenalty_TargetDest_WaivesTimeOpportunityPenalty_NonzeroCapacity(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		{Good: "FRONTIER", SourceWaypoint: "X1-HOME-1", DestWaypoint: "X1-DEEP-9", SpreadPerUnit: 900, VolumeCap: 700, CappedSpread: 630000},
		{Good: "HOME", SourceWaypoint: "X1-HOME-2", DestWaypoint: "X1-HOME-3", SpreadPerUnit: 700, VolumeCap: 700, CappedSpread: 490000},
	}

	t.Run("undirected: capacity-based charge applies, home lane wins", func(t *testing.T) {
		ranked := rankLanesWithGatePenalty(lanes, lightHullCapacity, "")
		if ranked[0].Good != "HOME" {
			t.Fatalf("expected HOME first (FRONTIER charged 220 -> 680 < 700), got %v", laneOrder(ranked))
		}
	})

	t.Run("directed at the frontier lane: its charge is waived, it wins", func(t *testing.T) {
		ranked := rankLanesWithGatePenalty(lanes, lightHullCapacity, "X1-DEEP-9")
		if ranked[0].Good != "FRONTIER" {
			t.Fatalf("expected FRONTIER first once its charge is waived (raw 900 > 700), got %v", laneOrder(ranked))
		}
		if ranked[0].SpreadPerUnit != 900 {
			t.Fatalf("the waiver is ranking-only — FRONTIER's real spread must stay 900, got %d", ranked[0].SpreadPerUnit)
		}
	})
}

// The selection log one-liner surfaces the exact per-unit charge on a
// cross-system token (sp-xwa1) so the captain can read WHY a frontier lane won
// or lost, while a same-system token is unchanged and a waived directed lane
// reads pen=0(waived).
func TestLaneSelectionOneLiner_CrossSystemLane_CarriesComputedPenalty(t *testing.T) {
	cross := trading.ArbitrageLane{Good: "DEEP", SourceWaypoint: "X1-AAA-1", DestWaypoint: "X1-BBB-2", SpreadPerUnit: 1700, VolumeCap: 480}
	same := trading.ArbitrageLane{Good: "LOCAL", SourceWaypoint: "X1-AAA-1", DestWaypoint: "X1-AAA-2", SpreadPerUnit: 1500, VolumeCap: 480}

	t.Run("cross-system token carries the computed charge, m stays the raw spread", func(t *testing.T) {
		got := laneSelectionOneLiner(cross, heavyHullCapacity, "")
		for _, want := range []string{"m=1700", "cross", "pen=110"} {
			if !strings.Contains(got, want) {
				t.Fatalf("expected one-liner to contain %q, got: %s", want, got)
			}
		}
		if strings.Contains(got, "waived") {
			t.Fatalf("an undirected cross-system lane is charged, not waived, got: %s", got)
		}
	})

	t.Run("same-system token has no penalty suffix", func(t *testing.T) {
		got := laneSelectionOneLiner(same, heavyHullCapacity, "")
		if !strings.Contains(got, "m=1500") || !strings.Contains(got, "same") {
			t.Fatalf("expected a same-system token with m=1500, got: %s", got)
		}
		if strings.Contains(got, "pen=") {
			t.Fatalf("a same-system lane pays no cross-system charge — no pen= suffix expected, got: %s", got)
		}
	})

	t.Run("directed target lane reads pen=0(waived)", func(t *testing.T) {
		got := laneSelectionOneLiner(cross, heavyHullCapacity, "X1-BBB-2")
		if !strings.Contains(got, "pen=0(waived)") {
			t.Fatalf("expected the directed lane's charge to read waived, got: %s", got)
		}
		if strings.Contains(got, "pen=110") {
			t.Fatalf("a waived lane must not also report the nominal charge, got: %s", got)
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

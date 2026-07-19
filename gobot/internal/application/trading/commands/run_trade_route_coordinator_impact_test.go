package commands

import (
	"math"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// The oracles here are hand-derived from the analyst's formula
// (buy_eff=Ask·(1+0.050·x/2), sell_eff=Bid·(1-0.015·x/2), rank on sell_eff−buy_eff),
// never read back from the implementation.

const (
	era3Buy  = trading.DefaultBuyImpactCoefficient  // 0.050
	era3Sell = trading.DefaultSellImpactCoefficient // 0.015
)

// AC1 (deterministic math): a lane whose planned units are a large fraction of its
// tradeVolume has its effective spread compressed well below snapshot; a tiny trade
// leaves it ≈ snapshot (no spurious penalty).
func TestEffectiveSpread_CompressesWithUnitsOverVolume(t *testing.T) {
	model := laneImpactModel{buyImpact: era3Buy, sellImpact: era3Sell}
	// Ask 1000, Bid 1100 => snapshot spread 100; tradeVolume 100.
	lane := trading.ArbitrageLane{SourceAsk: 1000, DestBid: 1100, SpreadPerUnit: 100, VolumeCap: 100}

	t.Run("full tradeVolume (x=1) compresses to 66.75", func(t *testing.T) {
		// buy_eff=1000·1.025=1025 ; sell_eff=1100·0.99250=1091.75 ; spread=66.75.
		got := model.effectiveSpreadPerUnit(lane, 100)
		if math.Abs(got-66.75) > 1e-9 {
			t.Fatalf("effective spread at x=1: got %.4f, want 66.75 (snapshot was 100)", got)
		}
		if got >= float64(lane.SpreadPerUnit) {
			t.Fatalf("effective spread %.4f must be BELOW snapshot %d for a full-volume trade", got, lane.SpreadPerUnit)
		}
	})

	t.Run("tiny trade (x=0.01) stays within 0.5 of snapshot", func(t *testing.T) {
		got := model.effectiveSpreadPerUnit(lane, 1) // 1/100 of tradeVolume
		if math.Abs(got-100) > 0.5 {
			t.Fatalf("effective spread for a tiny trade: got %.4f, want ~100 (no spurious penalty)", got)
		}
	})

	t.Run("effective spread equals sell_eff - buy_eff (formula fidelity)", func(t *testing.T) {
		const x = 0.5
		buyEff := trading.EffectiveBuyPrice(float64(lane.SourceAsk), x, era3Buy)
		sellEff := trading.EffectiveSellPrice(float64(lane.DestBid), x, era3Sell)
		want := sellEff - buyEff
		got := model.effectiveSpreadPerUnit(lane, 50) // x = 50/100 = 0.5
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("effective spread must equal sell_eff-buy_eff: got %.6f, want %.6f", got, want)
		}
	})
}

// Self-review lens 2: a zero tradeVolume must NOT divide by zero — it fails safe to the
// snapshot spread (no self-compression term).
func TestEffectiveSpread_ZeroTradeVolumeFailsSafeToSnapshot(t *testing.T) {
	model := laneImpactModel{buyImpact: era3Buy, sellImpact: era3Sell}
	lane := trading.ArbitrageLane{SourceAsk: 1000, DestBid: 1100, SpreadPerUnit: 100, VolumeCap: 0}

	got := model.effectiveSpreadPerUnit(lane, 500)
	if got != 100 {
		t.Fatalf("zero-tradeVolume lane must fall back to snapshot spread 100, got %.4f", got)
	}
}

// The inert (zero-value) model returns the snapshot spread exactly, even for a lane that
// carries only SpreadPerUnit with Ask/Bid unpopulated — the invariant that keeps the
// original spread-based ranker tests byte-identical.
func TestEffectiveSpread_InertModelIsSnapshot(t *testing.T) {
	var inert laneImpactModel
	lane := trading.ArbitrageLane{SpreadPerUnit: 854, VolumeCap: 480} // no Ask/Bid, as old tests build
	if got := inert.effectiveSpreadPerUnit(lane, 480); got != 854 {
		t.Fatalf("inert model must return snapshot spread 854, got %.4f", got)
	}
}

// AC2 (ranking behavior): given two lanes a SNAPSHOT ranker ties, the one carrying live
// cooldown debt ranks LOWER — proving hulls rotate off a hammered lane to a fresh one.
// The debted lane is deliberately the Good tie-break WINNER, so under snapshot it ranks
// FIRST; the debt is what demotes it, not the tie-break.
func TestRanking_CooldownDebtDemotesHammeredLane(t *testing.T) {
	// Identical economics; distinct identity. Same system => same circuit time.
	hammered := trading.ArbitrageLane{Good: "AAA", SourceWaypoint: "X1-SYS-1", DestWaypoint: "X1-SYS-2", SourceAsk: 1000, DestBid: 1100, SpreadPerUnit: 100, VolumeCap: 200, CappedSpread: 20000}
	fresh := trading.ArbitrageLane{Good: "ZZZ", SourceWaypoint: "X1-SYS-3", DestWaypoint: "X1-SYS-4", SourceAsk: 1000, DestBid: 1100, SpreadPerUnit: 100, VolumeCap: 200, CappedSpread: 20000}
	lanes := []trading.ArbitrageLane{hammered, fresh}

	// Snapshot (inert) ranker: a tie broken by Good asc => AAA (hammered) FIRST.
	snapshot := rankLanesByCircuitRate(lanes, 200, "", laneImpactModel{})
	if snapshot[0].Good != "AAA" {
		t.Fatalf("precondition: snapshot ranker must tie-break AAA first, got %v", laneOrder(snapshot))
	}

	// Impact model with only a cooldown-debt term (no self-impact) on the hammered lane.
	model := laneImpactModel{debt: func(l trading.ArbitrageLane) float64 {
		if l.Good == "AAA" {
			return 0.065 // one full-volume hammer's worth of live compression
		}
		return 0
	}}
	got := rankLanesByCircuitRate(lanes, 200, "", model)
	pos := laneRankPositions(got)
	if pos["ZZZ"] >= pos["AAA"] {
		t.Fatalf("hammered lane AAA must rank BELOW fresh lane ZZZ under cooldown debt, got %v", laneOrder(got))
	}
	// Real economics survive unmutated (the executor reads these, not the rate score).
	if got[pos["AAA"]].SpreadPerUnit != 100 || got[pos["AAA"]].CappedSpread != 20000 {
		t.Fatalf("hammered lane's real spread/capped-spread must survive unmutated, got %+v", got[pos["AAA"]])
	}
}

// AC2 (ranking behavior): given two lanes a snapshot ranker ties (same per-unit spread,
// same tradeVolume, same hold-fit), the one whose HIGHER PRICE LEVEL means this hull's
// own volume compresses it more in credit terms ranks LOWER — a fragility a snapshot or
// pure hold-fit ranker (blind to price level) cannot see. The fragile lane is the Good
// tie-break winner, so the compression, not the tie-break, drives the demotion.
func TestRanking_SelfCompressionDemotesFragileHighPriceLane(t *testing.T) {
	// Same spread (100) and tradeVolume (100); rich lane is a thin margin on a dear good.
	rich := trading.ArbitrageLane{Good: "AAA", SourceWaypoint: "X1-SYS-1", DestWaypoint: "X1-SYS-2", SourceAsk: 5000, DestBid: 5100, SpreadPerUnit: 100, VolumeCap: 100, CappedSpread: 10000}
	cheap := trading.ArbitrageLane{Good: "ZZZ", SourceWaypoint: "X1-SYS-3", DestWaypoint: "X1-SYS-4", SourceAsk: 1000, DestBid: 1100, SpreadPerUnit: 100, VolumeCap: 100, CappedSpread: 10000}
	lanes := []trading.ArbitrageLane{rich, cheap}

	// Snapshot mis-orders: identical value/hold-fit => Good asc => AAA (rich, fragile) FIRST.
	snapshot := rankLanesByCircuitRate(lanes, 100, "", laneImpactModel{})
	if snapshot[0].Good != "AAA" {
		t.Fatalf("precondition: snapshot ranker must tie-break the fragile lane AAA first, got %v", laneOrder(snapshot))
	}

	model := laneImpactModel{buyImpact: era3Buy, sellImpact: era3Sell}
	got := rankLanesByCircuitRate(lanes, 100, "", model) // plannedUnits=100 => x=1 on both
	pos := laneRankPositions(got)
	if pos["ZZZ"] >= pos["AAA"] {
		t.Fatalf("fragile high-price lane AAA must rank BELOW the cheap lane ZZZ under self-compression, got %v", laneOrder(got))
	}
}

// Self-review lens 2: the model AUGMENTS the circuit-rate/target-dest logic, it does not
// replace it. A directed --dest lane keeps its in-system-baseline waiver even with the
// impact model active, and the real snapshot economics still pass through untouched.
func TestRanking_ImpactModelPreservesTargetDestWaiver(t *testing.T) {
	cross := trading.ArbitrageLane{Good: "DEEP", SourceWaypoint: "X1-AAA-1", DestWaypoint: "X1-BBB-2", SourceAsk: 1000, DestBid: 2700, SpreadPerUnit: 1700, VolumeCap: 480}
	model := laneImpactModel{buyImpact: era3Buy, sellImpact: era3Sell}

	// Directed at the cross lane's destination: the surcharge is waived, so its rate is the
	// in-system baseline rate — strictly higher than the same lane's surcharged rate. This
	// mirrors the waiver contract and proves the impact model did not disturb it.
	waived := laneCircuitRatePerHour(cross, 480, "X1-BBB-2", model)
	charged := laneCircuitRatePerHour(cross, 480, "", model)
	if !(waived > charged) {
		t.Fatalf("directed --dest lane must keep its in-system-baseline waiver (waived %.1f > charged %.1f)", waived, charged)
	}
}

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestBootstrapMetrics_ConstructionPct_DistinctPlayerSeries proves bootstrap_construction_pct — the
// series behind the dashboard panel that prompted sp-1zhu5 — publishes one series PER player_id, so a
// second player's reading sits beside the first's rather than overwriting it.
func TestBootstrapMetrics_ConstructionPct_DistinctPlayerSeries(t *testing.T) {
	InitRegistry()
	collector := NewBootstrapMetricsCollector()
	if err := collector.Register(); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	collector.RecordConstructionPct(37.5, "1")
	collector.RecordConstructionPct(82.0, "2")

	if got := testutil.ToFloat64(collector.constructionPct.WithLabelValues("1")); got != 37.5 {
		t.Errorf("player_id=1: bootstrap_construction_pct = %v, want 37.5", got)
	}
	if got := testutil.ToFloat64(collector.constructionPct.WithLabelValues("2")); got != 82.0 {
		t.Errorf("player_id=2: bootstrap_construction_pct = %v, want 82.0", got)
	}
}

// TestBootstrapMetrics_ProbesAndHaulersTotals_DistinctPlayerSeries proves both purchase counters key on
// player_id: two players buying independently accumulate two independent counts, never one shared total
// that attributes one player's spend to another.
func TestBootstrapMetrics_ProbesAndHaulersTotals_DistinctPlayerSeries(t *testing.T) {
	InitRegistry()
	collector := NewBootstrapMetricsCollector()
	if err := collector.Register(); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	collector.RecordProbePurchased("1")
	collector.RecordProbePurchased("1")
	collector.RecordProbePurchased("2")

	collector.RecordHaulerPurchased("1")
	collector.RecordHaulerPurchased("2")
	collector.RecordHaulerPurchased("2")
	collector.RecordHaulerPurchased("2")

	if got := testutil.ToFloat64(collector.probesTotal.WithLabelValues("1")); got != 2 {
		t.Errorf("player_id=1: bootstrap_probes_total = %v, want 2", got)
	}
	if got := testutil.ToFloat64(collector.probesTotal.WithLabelValues("2")); got != 1 {
		t.Errorf("player_id=2: bootstrap_probes_total = %v, want 1", got)
	}
	if got := testutil.ToFloat64(collector.haulersTotal.WithLabelValues("1")); got != 1 {
		t.Errorf("player_id=1: bootstrap_haulers_total = %v, want 1", got)
	}
	if got := testutil.ToFloat64(collector.haulersTotal.WithLabelValues("2")); got != 3 {
		t.Errorf("player_id=2: bootstrap_haulers_total = %v, want 3", got)
	}
}

// TestBootstrapMetrics_Phase_DistinctPlayerSeries_PreservesZeroingInvariant proves player_id is an
// orthogonal label on bootstrap_phase, not a replacement for the existing per-phase zeroing
// (TestRecordPhasePublishesExactlyTheDerivablePhases covers that invariant on its own): each player_id
// gets its own fully-zeroed-except-one phase set, and recording for one player_id must never touch
// another's series — exactly the blending sp-1zhu5 exists to end.
func TestBootstrapMetrics_Phase_DistinctPlayerSeries_PreservesZeroingInvariant(t *testing.T) {
	InitRegistry()
	collector := NewBootstrapMetricsCollector()
	if err := collector.Register(); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	collector.RecordPhase("GATE", "1")
	collector.RecordPhase("COLDSTART", "2")

	wantPlayer1 := map[string]float64{"COLDSTART": 0, "GATE": 1, "EXPANSION": 0}
	wantPlayer2 := map[string]float64{"COLDSTART": 1, "GATE": 0, "EXPANSION": 0}
	for _, phase := range bootstrapKnownPhases {
		if got := testutil.ToFloat64(collector.phase.WithLabelValues(phase, "1")); got != wantPlayer1[phase] {
			t.Errorf("player_id=1 phase=%s: got %v, want %v", phase, got, wantPlayer1[phase])
		}
		if got := testutil.ToFloat64(collector.phase.WithLabelValues(phase, "2")); got != wantPlayer2[phase] {
			t.Errorf("player_id=2 phase=%s: got %v, want %v", phase, got, wantPlayer2[phase])
		}
	}

	// Player 1 moves on to EXPANSION; player 2's already-recorded series must be untouched.
	collector.RecordPhase("EXPANSION", "1")
	for _, phase := range bootstrapKnownPhases {
		if got := testutil.ToFloat64(collector.phase.WithLabelValues(phase, "2")); got != wantPlayer2[phase] {
			t.Errorf("after player 1 advanced: player_id=2 phase=%s changed to %v, want unchanged %v", phase, got, wantPlayer2[phase])
		}
	}
}

// TestBootstrapMetrics_RecordMethods_NilSafe mirrors the sibling collectors' guarantee (RULINGS #4): a
// nil collector's Record* calls — the state before metrics are wired, or metrics disabled entirely — must
// be no-ops, never panics, with the player_id argument threaded through.
func TestBootstrapMetrics_RecordMethods_NilSafe(t *testing.T) {
	var c *BootstrapMetricsCollector
	c.RecordPhase("GATE", "1")
	c.RecordProbePurchased("1")
	c.RecordHaulerPurchased("1")
	c.RecordConstructionPct(50, "1")
}

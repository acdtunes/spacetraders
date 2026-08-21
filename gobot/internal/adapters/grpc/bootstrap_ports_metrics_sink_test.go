package grpc

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
)

// TestBootstrapMetricsSink_NilSafeWithNoGlobalCollector proves the sink every bootstrap Record* call
// goes through is a no-op, not a panic, in the state every daemon starts in — before the collector is
// constructed and wired (RULINGS #4: a recording miss must never touch a decision).
func TestBootstrapMetricsSink_NilSafeWithNoGlobalCollector(t *testing.T) {
	prev := metrics.GetGlobalBootstrapCollector()
	t.Cleanup(func() { metrics.SetGlobalBootstrapCollector(prev) })
	metrics.SetGlobalBootstrapCollector(nil)

	sink := &bootstrapMetricsSink{}
	sink.RecordPhase("COLDSTART", "1")
	sink.RecordProbePurchased("1")
	sink.RecordHaulerPurchased("1")
	sink.RecordConstructionPct(50, "1")
}

// TestBootstrapMetricsSink_ForwardsPlayerIDToCollector proves the sink is a pure pass-through: the
// player_id it receives is exactly the player_id the installed collector publishes, matching this
// file's own doc comment on bootstrapMetricsSink ("no logic change").
func TestBootstrapMetricsSink_ForwardsPlayerIDToCollector(t *testing.T) {
	prevRegistry := metrics.Registry
	prevCollector := metrics.GetGlobalBootstrapCollector()
	t.Cleanup(func() {
		metrics.Registry = prevRegistry
		metrics.SetGlobalBootstrapCollector(prevCollector)
	})
	metrics.InitRegistry()
	collector := metrics.NewBootstrapMetricsCollector()
	if err := collector.Register(); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	metrics.SetGlobalBootstrapCollector(collector)

	sink := &bootstrapMetricsSink{}
	sink.RecordConstructionPct(63.0, "77")

	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	var found bool
	for _, f := range families {
		if f.GetName() != "spacetraders_daemon_bootstrap_construction_pct" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "player_id" && lp.GetValue() == "77" && m.GetGauge().GetValue() == 63.0 {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal(`expected bootstrap_construction_pct{player_id="77"}=63 forwarded through the sink to the real collector`)
	}
}

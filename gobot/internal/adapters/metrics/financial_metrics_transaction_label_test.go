package metrics

import (
	"sort"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestTransactionMetrics_LabeledByTypeNotCategory pins the type-only label set.
//
// category is a deterministic relabel of transaction type, so carrying it as
// a separate label on transactions_total / transaction_amount is pure
// redundancy — the same information as `type`, doubling the series
// cardinality for nothing.
// This test asserts, at the emitted-series boundary (what Prometheus actually
// scrapes), that those two metrics are labeled by player_id+type ONLY.
//
// GUARDRAIL: the sibling ledger_revenue_total / ledger_cost_total counters
// MUST keep the category label — a live financial dashboard panel
// splits Operating vs Net capex on category!='SHIP_INVESTMENTS'. This test
// fails loudly if a future cleanup over-reaches and strips category there too.
func TestTransactionMetrics_LabeledByTypeNotCategory(t *testing.T) {
	collector := NewFinancialMetricsCollector(nil, nil, nil)

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collector.transactionsTotal,
		collector.transactionAmount,
		collector.ledgerRevenueTotal,
		collector.ledgerCostTotal,
	)

	// A positive SELL_CARGO record fans into transactions_total, transaction_amount
	// (both by type/category before the change) AND ledger_revenue_total (positive).
	collector.RecordTransaction(1, "AGENT", "SELL_CARGO", "TRADING_REVENUE", 500, 1000, "trade_route")

	tests := []struct {
		metric string
		want   []string
	}{
		{"spacetraders_daemon_transactions_total", []string{"player_id", "type"}},
		{"spacetraders_daemon_transaction_amount", []string{"player_id", "type"}},
		// Guardrail: ledger-flow counter retains category (Operating-vs-Net split).
		{"spacetraders_daemon_ledger_revenue_total", []string{"category", "operation_type", "player_id"}},
	}

	for _, tc := range tests {
		got := labelNamesFor(t, reg, tc.metric)
		if !equalStrings(got, tc.want) {
			t.Errorf("%s label set = %v, want %v", tc.metric, got, tc.want)
		}
	}
}

// labelNamesFor gathers reg and returns the sorted label names of the first
// emitted series of metricName. Reading the exposed label pairs is a
// black-box assertion on the driven (metrics) boundary, not on internals.
func labelNamesFor(t *testing.T, reg *prometheus.Registry, metricName string) []string {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() != metricName {
			continue
		}
		series := family.GetMetric()
		if len(series) == 0 {
			t.Fatalf("%s emitted no series", metricName)
		}
		names := make([]string, 0, len(series[0].GetLabel()))
		for _, pair := range series[0].GetLabel() {
			names = append(names, pair.GetName())
		}
		sort.Strings(names)
		return names
	}
	t.Fatalf("%s not found in registry", metricName)
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

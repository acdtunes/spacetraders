package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestRecordTransaction_EmitsLedgerFlowCounters pins the sign-split ledger-flow counters.
//
// The financial dashboard's cr/hr panels need a signed-amount SUM per
// operation_type that PromQL rate() can smooth. Counters must be
// monotonic (non-negative), so RecordTransaction fans each signed ledger
// amount into ONE of two counters by sign:
//   - amount > 0  -> ledger_revenue_total += amount
//   - amount < 0  -> ledger_cost_total    += -amount (magnitude)
//   - amount == 0 -> neither
//
// Both are labeled operation_type/category/player_id. The sibling counter is
// never touched on any single transaction (no double counting: a revenue
// entry must not also register as a cost, and vice-versa) — this is what lets
// panel 103 net the two sides and panel 109/15 subtract them per operation.
func TestRecordTransaction_EmitsLedgerFlowCounters(t *testing.T) {
	const (
		playerID      = 42
		playerIDLabel = "42"
		agentSymbol   = "TEST_AGENT"
		operationType = "contract"
		category      = "CONTRACT"
	)

	cases := []struct {
		name        string
		amount      int
		wantRevenue float64
		wantCost    float64
	}{
		{name: "positive amount is revenue", amount: 5000, wantRevenue: 5000, wantCost: 0},
		{name: "negative amount is cost", amount: -800, wantRevenue: 0, wantCost: 800},
		{name: "zero amount touches neither", amount: 0, wantRevenue: 0, wantCost: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := NewFinancialMetricsCollector(nil, nil, nil)

			collector.RecordTransaction(
				playerID, agentSymbol, "CONTRACT_ACCEPTED", category, tc.amount, 100000, operationType,
			)

			gotRevenue := testutil.ToFloat64(
				collector.ledgerRevenueTotal.WithLabelValues(operationType, category, playerIDLabel),
			)
			if gotRevenue != tc.wantRevenue {
				t.Errorf("ledger_revenue_total{operation_type=%q,category=%q,player_id=%q} = %v, want %v",
					operationType, category, playerIDLabel, gotRevenue, tc.wantRevenue)
			}

			gotCost := testutil.ToFloat64(
				collector.ledgerCostTotal.WithLabelValues(operationType, category, playerIDLabel),
			)
			if gotCost != tc.wantCost {
				t.Errorf("ledger_cost_total{operation_type=%q,category=%q,player_id=%q} = %v, want %v",
					operationType, category, playerIDLabel, gotCost, tc.wantCost)
			}
		})
	}
}

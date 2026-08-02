package ship

import (
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
)

// scan_budget_metrics.go is the ONE place the two scan allowances publish what
// they decided. Both budgets call through here rather than reaching
// the collector themselves, so the market and shipyard families cannot drift
// apart in their label vocabulary — the question "what share of reads was
// Earning" has to mean the same thing on both or the shared dashboard panel
// lies.
//
// The collector is resolved LAZILY on every call, not captured at construction,
// because the budgets are built during handler wiring and the collector is
// installed later when the daemon enables metrics. A budget that captured the
// global at construction would hold the nil it saw and record nothing forever —
// which is indistinguishable from a budget that is never consulted, the exact
// blindness this bead exists to remove.

// recordScanBudgetDecision publishes one admission decision plus the allowance
// it was taken against.
//
// The rate and coverage ride along on the decision rather than on a polling
// goroutine, and that is deliberate: they are re-derived by the caller on the
// same tick from the same durable state the decision used (RULING #2), so the
// three series always describe one consistent moment. A separate poller would
// sample them independently and let an operator read a rate from one instant
// against a decision from another.
func recordScanBudgetDecision(playerID int, budget string, class marketscan.Class, decision marketscan.Decision, reqPerSec float64, coverage int) {
	collector := metrics.GetGlobalScanBudgetCollector()
	if collector == nil {
		return // Metrics disabled; recording is best-effort (RULINGS #4).
	}
	collector.RecordDecision(playerID, budget, class.String(), decision.String())
	collector.RecordAllowance(playerID, budget, reqPerSec, coverage)
}

// recordScanBudgetOverdraft publishes one read admitted against an already-empty
// bucket — the forced draw the shipyard config comment tells the operator to
// watch.
func recordScanBudgetOverdraft(playerID int, budget string, class marketscan.Class) {
	collector := metrics.GetGlobalScanBudgetCollector()
	if collector == nil {
		return
	}
	collector.RecordOverdraft(playerID, budget, class.String())
}

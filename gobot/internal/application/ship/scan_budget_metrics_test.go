package ship

// Tests for the WIRE (sp-e4dkw): that the two scan allowances actually publish
// what they decide, on the real Prometheus registry, from the real admission
// path.
//
// They run against the REAL budgets rather than a spy on the collector, because
// the thing that was broken was not "does the collector work" but "is anything
// ever recorded". Both budgets were armed and reached in production while
// emitting nothing at all, and a spy-based test would have passed against
// exactly that. So each case asks the budget to make a decision and then reads
// the registry the daemon actually scrapes.

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
)

// installScanBudgetMetrics points the package at a private registry with the
// scan-budget collector installed, restoring both on cleanup so the tests in
// this package stay independent of each other and of the process-wide default.
func installScanBudgetMetrics(t *testing.T) *prometheus.Registry {
	t.Helper()

	prevRegistry := metrics.Registry
	prevCollector := metrics.GetGlobalScanBudgetCollector()
	t.Cleanup(func() {
		metrics.Registry = prevRegistry
		metrics.SetGlobalScanBudgetCollector(prevCollector)
	})

	reg := prometheus.NewRegistry()
	metrics.Registry = reg
	c := metrics.NewScanBudgetMetricsCollector()
	require.NoError(t, c.Register())
	metrics.SetGlobalScanBudgetCollector(c)
	return reg
}

// seriesValue reads one series out of the registry by name and exact label set.
// A missing series reports ok=false rather than zero, because "the budget never
// recorded" and "the budget recorded zero" are the two readings this whole bead
// exists to tell apart — and silently returning 0 for a missing series would
// make a wire that records NOTHING pass a test asserting a count of 0.
func seriesValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) (float64, bool) {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if !labelsMatch(m, labels) {
				continue
			}
			if m.GetCounter() != nil {
				return m.GetCounter().GetValue(), true
			}
			if m.GetGauge() != nil {
				return m.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

func labelsMatch(m *dto.Metric, want map[string]string) bool {
	got := make(map[string]string, len(m.GetLabel()))
	for _, l := range m.GetLabel() {
		got[l.GetName()] = l.GetValue()
	}
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

// THE SHIPYARD OVERDRAFT IS COUNTED.
//
// This is the counter whose absence made a 3.2x breach of the shipyard allowance
// invisible, and whose absence made the knob's own documented operating procedure
// ("raise it when Forced overdrafts are persistently high") unexecutable.
//
// The bucket is drained BEFORE the collector is installed, so the drain's own
// debits cannot be mistaken for the overdraft under test.
func TestYardBudget_EmitsOverdraftWhenAnEarningReadDrawsOnAnEmptyBucket(t *testing.T) {
	b, now := newTestYardBudget(t, 84)
	drain(b) // empties the allowance before anything is being recorded
	prime(b)

	reg := installScanBudgetMetrics(t)

	decision := b.Admit(context.Background(), testPlayerID, "X1-GUARD-Y1", *now, true, marketscan.Earning)
	require.Equal(t, marketscan.Spend, decision,
		"precondition: an Earning read is metered but never denied, even on an empty bucket")

	overdrafts, ok := seriesValue(t, reg, "spacetraders_daemon_scan_budget_overdrafts_total",
		map[string]string{"player_id": "1", "budget": "shipyard", "class": "earning"})
	require.True(t, ok, "the forced draw must be COUNTED, not merely tallied in an unexported field")
	require.Equal(t, float64(1), overdrafts)

	// An overdraft is a strict subset of an admitted read, so the same call must
	// also appear as a spend. Were it counted only as an overdraft, the reconcile
	// against api_requests_total would come up short by exactly the breaches an
	// operator is hunting.
	spends, ok := seriesValue(t, reg, "spacetraders_daemon_scan_budget_decisions_total",
		map[string]string{"player_id": "1", "budget": "shipyard", "class": "earning", "decision": "spend"})
	require.True(t, ok)
	require.Equal(t, float64(1), spends)
}

// A DECLINE IS PUBLISHED, NOT JUST AN ADMISSION.
//
// Without the declined half there is no denominator: "0.1 req/s admitted" cannot
// be read as a healthy budget or a starving one. This is also the series the
// shipyard fix (sp-lr27k) is verified with — a read that moves from Earning to
// Discretionary shows up here.
func TestYardBudget_EmitsDeclinesWithTheClassThatWasDeclined(t *testing.T) {
	b, now := newTestYardBudget(t, 84)
	drain(b)
	prime(b)

	reg := installScanBudgetMetrics(t)

	// Just-scanned yard on a drained bucket: the deniable path's decline case.
	decision := b.Admit(context.Background(), testPlayerID, "X1-GUARD-Y1", *now, true, marketscan.Discretionary)
	require.Equal(t, marketscan.ServeFromStore, decision, "precondition: this fixture DOES decline")

	declines, ok := seriesValue(t, reg, "spacetraders_daemon_scan_budget_decisions_total",
		map[string]string{"player_id": "1", "budget": "shipyard", "class": "discretionary", "decision": "serve_from_store"})
	require.True(t, ok, "a declined read must be published or the admit rate has no denominator")
	require.Equal(t, float64(1), declines)

	// And it must NOT be counted as a spend: the spend slice is what reconciles
	// against api_requests_total, so a decline landing there would overstate the
	// budget's traffic by exactly the reads it successfully suppressed.
	_, spent := seriesValue(t, reg, "spacetraders_daemon_scan_budget_decisions_total",
		map[string]string{"player_id": "1", "budget": "shipyard", "class": "discretionary", "decision": "spend"})
	require.False(t, spent, "a read served from store must never appear as a spend")
}

// The rate and the coverage denominator are published beside the decision, on
// values the SAME call re-derived (RULING #2). Together they are what makes the
// fixed-budget invariant readable: a waypoint's scan interval is rate/coverage,
// so an operator can see the interval widen as the map grows instead of taking
// the package comment's word for it.
func TestYardBudget_PublishesTheAllowanceAndItsCoverageDenominator(t *testing.T) {
	reg := installScanBudgetMetrics(t)

	b, _ := newTestYardBudget(t, 84)
	b.Admit(context.Background(), testPlayerID, "X1-GUARD-Y1", time.Time{}, false, marketscan.Discretionary)

	labels := map[string]string{"player_id": "1", "budget": "shipyard"}

	rate, ok := seriesValue(t, reg, "spacetraders_daemon_scan_budget_rate_req_per_sec", labels)
	require.True(t, ok, "the allowance itself must be published or 'over budget' has no referent")
	require.InDelta(t, testYardRate, rate, 1e-9)

	coverage, ok := seriesValue(t, reg, "spacetraders_daemon_scan_budget_coverage", labels)
	require.True(t, ok)
	require.Equal(t, float64(84), coverage, "the charted-yard count is the denominator the rate is divided across")
}

// The market budget publishes into the SAME family under its own budget label,
// which is what lets one dashboard panel and one alert expression cover both
// allowances. This is also the acceptance path: sum(rate(...{budget="market",
// decision="spend"})) is what reconciles against api_requests_total for Get Market.
func TestMarketBudget_EmitsUnderItsOwnBudgetLabel(t *testing.T) {
	reg := installScanBudgetMetrics(t)

	b, _ := newTestBudget(t, defaultBudgetReqPerSec, defaultValueClampR)
	// A never-scanned market is past the anti-starvation bound and always admitted.
	decision := b.Admit(context.Background(), budgetTestPlayerID, "X1-AA-A1", nil, marketscan.Discretionary)
	require.Equal(t, marketscan.Spend, decision, "precondition: a never-scanned market is admitted")

	spends, ok := seriesValue(t, reg, "spacetraders_daemon_scan_budget_decisions_total",
		map[string]string{"player_id": "1", "budget": "market", "class": "discretionary", "decision": "spend"})
	require.True(t, ok, "the market budget must publish into the shared family")
	require.Equal(t, float64(1), spends)

	// The shipyard budget must not have been touched by a market decision.
	_, crossed := seriesValue(t, reg, "spacetraders_daemon_scan_budget_decisions_total",
		map[string]string{"player_id": "1", "budget": "shipyard", "class": "discretionary", "decision": "spend"})
	require.False(t, crossed, "the two budgets must stay separable by label")
}

// A Debit is metered but never deniable, which is what Earning means in this
// vocabulary — so it lands in the spend slice like every other request the
// daemon issues. If it did not, the reconcile against api_requests_total would
// come up short by every catalogue gap-fill read.
func TestMarketBudget_DebitIsCountedAsAMeteredSpend(t *testing.T) {
	reg := installScanBudgetMetrics(t)

	b, _ := newTestBudget(t, defaultBudgetReqPerSec, defaultValueClampR)
	b.Debit(budgetTestPlayerID, "X1-AA-UNVISITED")

	spends, ok := seriesValue(t, reg, "spacetraders_daemon_scan_budget_decisions_total",
		map[string]string{"player_id": "1", "budget": "market", "class": "earning", "decision": "spend"})
	require.True(t, ok, "an undeniable metered read is still a request and must be counted")
	require.Equal(t, float64(1), spends)
}

// Recording is pure observation (RULINGS #4). Metrics are disabled in most of
// this package's tests and in any daemon run without the flag, so the budgets hit
// a nil collector on the hot admission path — and the DECISION must be unchanged
// by whether anyone is watching.
func TestBudgets_DecideIdenticallyWithNoCollectorInstalled(t *testing.T) {
	prev := metrics.GetGlobalScanBudgetCollector()
	t.Cleanup(func() { metrics.SetGlobalScanBudgetCollector(prev) })
	metrics.SetGlobalScanBudgetCollector(nil)

	yard, yardNow := newTestYardBudget(t, 84)
	drain(yard)
	prime(yard)
	require.Equal(t, marketscan.Spend,
		yard.Admit(context.Background(), testPlayerID, "X1-GUARD-Y1", *yardNow, true, marketscan.Earning))
	require.Equal(t, marketscan.ServeFromStore,
		yard.Admit(context.Background(), testPlayerID, "X1-GUARD-Y2", *yardNow, true, marketscan.Discretionary))

	market, _ := newTestBudget(t, defaultBudgetReqPerSec, defaultValueClampR)
	require.Equal(t, marketscan.Spend,
		market.Admit(context.Background(), budgetTestPlayerID, "X1-AA-A1", nil, marketscan.Discretionary))
	market.Debit(budgetTestPlayerID, "X1-AA-UNVISITED")
}

package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// Budget label values. The two scan allowances share one metric family and are
// told apart by this label rather than by two families, because every question
// an operator asks of one ("what share was Earning", "how many overdrafts") is
// the same question asked of the other, and one family means one dashboard
// panel and one alert expression instead of two that can drift.
const (
	// BudgetMarket is the fleet's ONE market-scan allowance
	// (internal/application/ship/market_scan_budget.go).
	BudgetMarket = "market"
	// BudgetShipyard is the fleet's ONE shipyard-read allowance
	// (internal/application/ship/shipyard_scan_budget.go).
	BudgetShipyard = "shipyard"
)

// ScanBudgetMetricsCollector publishes what the market and shipyard scan
// budgets decide, which is the thing neither of them could previously be shown
// to do (sp-e4dkw).
//
// WHY THIS EXISTS. Both budgets were ARMED and REACHED in production while
// emitting nothing at all: no admit counter, no deny counter, no overdraft
// counter. internal/infrastructure/config/shipyard_scan.go tells the operator
// to "Raise it when Forced overdrafts are persistently high" — an operating
// procedure that could not be executed, because nothing counted overdrafts. As
// a direct consequence shipyard reads ran at 3.2x their configured allowance
// (0.388 req/s measured against 0.12 configured) with no signal whatever. A
// budget that cannot be observed cannot be tuned, and cannot be shown to hold.
//
// COUNTERS ONLY WHERE THE QUANTITY IS MONOTONE. Decisions and overdrafts only
// ever go up, so they are Counters and rate() over them means what it says. The
// allowance and the coverage denominator are LEVELS that move both ways, so
// they are Gauges — deliberately NOT observations into a Summary, because a
// signed _sum makes rate() ramp instead of average and produces a climbing
// phantom "average" (the sp-fpgl2 postmortem). No quantity here is ever
// observed into a _sum.
//
// Pure OBSERVATION (RULINGS #4): a recording miss must never touch an admission
// decision, so every Record is nil-safe and best-effort, following
// ParkedSensingMetricsCollector (internal/adapters/metrics/parked_sensing_metrics.go).
type ScanBudgetMetricsCollector struct {
	// decisions counts EVERY request the budget ruled on, by what it decided and
	// why the read was being made. One series per
	// (player, budget, class, decision).
	//
	// It counts admissions and declines in ONE counter split by the decision
	// label rather than in two counters, so the share questions
	// ("what fraction was Earning", "what fraction was declined") are ratios of
	// series in one family and need no cross-metric join. The decision="spend"
	// slice is the one that maps 1:1 onto API requests, which is what makes it
	// reconcilable against api_requests_total.
	decisions *prometheus.CounterVec
	// overdrafts counts reads ADMITTED against an already-empty bucket — the
	// budget's forced draw. One series per (player, budget, class).
	//
	// This is the exact quantity the shipyard config comment tells the operator
	// to watch, and it is a strict subset of decisions{decision="spend"}: an
	// overdraft is an admitted read, never a declined one. A persistently high
	// reading means the fixed allowance is smaller than the fleet's unavoidable
	// pre-buy verification and should be RAISED — never that the guards should
	// be weakened (RULINGS #4).
	overdrafts *prometheus.CounterVec
	// rateReqPerSec is the allowance itself, republished on every decision so a
	// retuned budget is visible without a restart. One series per
	// (player, budget).
	rateReqPerSec *prometheus.GaugeVec
	// coverage is the map size the allowance is divided across — MarketsKnown
	// for the market budget, yards known for the shipyard one. One series per
	// (player, budget).
	//
	// It is the DENOMINATOR of the invariant both budgets exist to hold: total
	// scan cost does not grow with the charted map, so each waypoint's interval
	// is an OUTPUT of rate / coverage. Published beside the rate it divides, the
	// coverage-decay invariant becomes something an operator can read off a
	// graph rather than something asserted in a comment.
	coverage *prometheus.GaugeVec
}

// NewScanBudgetMetricsCollector creates a new scan-budget collector.
func NewScanBudgetMetricsCollector() *ScanBudgetMetricsCollector {
	return &ScanBudgetMetricsCollector{
		decisions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "scan_budget_decisions_total",
				Help:      "Scan-budget admission decisions by budget (market|shipyard), read class (discretionary|earning|paired) and outcome (spend|serve_from_store). The decision=spend slice is one API request each, so sum(rate()) over it reconciles with api_requests_total for the matching endpoint",
			},
			[]string{"player_id", "budget", "class", "decision"},
		),
		overdrafts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "scan_budget_overdrafts_total",
				Help:      "Scan-budget reads admitted against an already-empty allowance bucket (the forced draw), by budget and read class. A strict subset of scan_budget_decisions_total{decision=\"spend\"}. Persistently high means the fixed allowance is below the fleet's unavoidable pre-buy verification and should be raised, never that a money guard should be weakened",
			},
			[]string{"player_id", "budget", "class"},
		),
		rateReqPerSec: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "scan_budget_rate_req_per_sec",
				Help:      "The scan budget's own fixed allowance in requests/sec, by budget. Constant with respect to the charted map by design: a growing map lengthens each waypoint's interval instead of raising this number",
			},
			[]string{"player_id", "budget"},
		),
		coverage: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "scan_budget_coverage",
				Help:      "Waypoints sharing the budget (markets known / yards known) — the denominator the fixed allowance is divided across, so a waypoint's scan interval is rate/coverage. Published beside the rate so the coverage-decay invariant is observable rather than merely asserted",
			},
			[]string{"player_id", "budget"},
		),
	}
}

// Register registers the scan-budget metrics with the Prometheus registry. A nil
// Registry (metrics disabled) is a no-op, matching the sibling collectors.
func (c *ScanBudgetMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}

	metrics := []prometheus.Collector{
		c.decisions,
		c.overdrafts,
		c.rateReqPerSec,
		c.coverage,
	}

	for _, metric := range metrics {
		if err := Registry.Register(metric); err != nil {
			return err
		}
	}

	return nil
}

// RecordDecision counts one admission decision.
func (c *ScanBudgetMetricsCollector) RecordDecision(playerID int, budget, class, decision string) {
	if c == nil || c.decisions == nil {
		return // Recording is best-effort; never panic an admission (RULINGS #4).
	}
	c.decisions.WithLabelValues(strconv.Itoa(playerID), budget, class, decision).Inc()
}

// RecordOverdraft counts one read admitted against an empty bucket.
func (c *ScanBudgetMetricsCollector) RecordOverdraft(playerID int, budget, class string) {
	if c == nil || c.overdrafts == nil {
		return
	}
	c.overdrafts.WithLabelValues(strconv.Itoa(playerID), budget, class).Inc()
}

// RecordAllowance republishes the budget's rate and the map size it is divided
// across.
//
// Both are SET from values the budget re-derives on the call rather than
// accumulated across ticks (RULING #2), so a shrinking map, a retuned rate and a
// restarted daemon all read correctly with no state to reconcile.
func (c *ScanBudgetMetricsCollector) RecordAllowance(playerID int, budget string, reqPerSec float64, coverage int) {
	if c == nil {
		return
	}
	id := strconv.Itoa(playerID)
	if c.rateReqPerSec != nil {
		c.rateReqPerSec.WithLabelValues(id, budget).Set(reqPerSec)
	}
	if c.coverage != nil {
		c.coverage.WithLabelValues(id, budget).Set(float64(coverage))
	}
}

// globalScanBudgetCollector is the process-wide collector both budgets record
// through, following the package's established global-setter idiom. Nil until
// the daemon enables metrics, and every method is nil-safe, so an unset
// collector simply records nothing.
var globalScanBudgetCollector *ScanBudgetMetricsCollector

// SetGlobalScanBudgetCollector installs the process-wide collector.
func SetGlobalScanBudgetCollector(c *ScanBudgetMetricsCollector) {
	globalScanBudgetCollector = c
}

// GetGlobalScanBudgetCollector returns the process-wide collector, which may be
// nil when metrics are disabled.
func GetGlobalScanBudgetCollector() *ScanBudgetMetricsCollector {
	return globalScanBudgetCollector
}

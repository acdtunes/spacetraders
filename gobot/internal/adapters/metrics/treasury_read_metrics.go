package metrics

// treasury_read_metrics.go — how the money guards learned their treasury (sp-muq66).
//
// The guards used to answer that question with a live `Get Agent` call every time, which
// measured 0.167 req/s = 8.3% of the 2.00 req/s API ceiling and did not fall under the
// request-coalescing singleflight, because the reads are invalidation-driven (every buy,
// sell, refuel and jump empties the agent cache) rather than concurrent duplicates. The
// ledger already carries the same number: every credit-moving transaction records the
// agent's post-transaction balance, and over 3,086 consecutive intervals the unrecorded
// -spend gap measured exactly zero.
//
// So the read now prefers the ledger and falls back to the live call when the ledger is
// too old to trust. That split is the ONE thing about this change that is not knowable
// from the outside: a fleet whose ledger is always stale would keep making every API call
// it made before and look, from the API counters alone, exactly like a change that never
// shipped. This counter is what tells those apart.
//
//	sum(rate(treasury_reads_total[15m])) by (source)      the ledger/live/error split
//	rate(treasury_reads_total{source="live"}[15m])        the fallback rate — expected ~2%
//	                                                      on a busy fleet, near 100% on an
//	                                                      idle one (gaps are unbounded when
//	                                                      nothing is trading), so read it
//	                                                      against fleet activity, never alone
//	rate(treasury_reads_total{source="error"}[15m])       guards going blind (fail-closed)

import "github.com/prometheus/client_golang/prometheus"

// Treasury read sources, the label values of treasury_reads_total.
const (
	// TreasuryReadLedger — served from the transaction ledger. NO API call was made.
	TreasuryReadLedger = "ledger"
	// TreasuryReadLive — the ledger was empty, unreadable, or older than the freshness
	// bound, and the coalesced live read answered instead.
	TreasuryReadLive = "live"
	// TreasuryReadError — BOTH failed. The caller was handed an error and fails closed
	// (RULINGS #4); it was never handed a zero or a stale value in place of one.
	TreasuryReadError = "error"
)

// TreasuryReadMetricsCollector holds the treasury-read source counter.
type TreasuryReadMetricsCollector struct {
	readsTotal *prometheus.CounterVec
}

// NewTreasuryReadMetricsCollector builds the collector, mirroring the sibling collectors'
// constructor idiom.
func NewTreasuryReadMetricsCollector() *TreasuryReadMetricsCollector {
	return &TreasuryReadMetricsCollector{
		readsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "treasury_reads_total",
				Help:      "Money-guard treasury reads by source: ledger (no API call), live (fell back to Get Agent), error (both failed — the guard failed closed) (sp-muq66)",
			},
			[]string{"source"},
		),
	}
}

// Register registers the counter with the Prometheus registry. A nil Registry (metrics
// disabled) is a no-op, matching the sibling collectors.
func (c *TreasuryReadMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return Registry.Register(c.readsTotal)
}

// Record counts one treasury read served by source.
func (c *TreasuryReadMetricsCollector) Record(source string) {
	if c == nil {
		return
	}
	c.readsTotal.WithLabelValues(source).Inc()
}

// globalTreasuryReadCollector is the process-wide collector, following the package's
// established global-setter idiom. Nil until the daemon enables metrics, and Record is
// nil-safe, so an unset collector simply records nothing.
var globalTreasuryReadCollector *TreasuryReadMetricsCollector

// SetGlobalTreasuryReadCollector installs the process-wide collector.
func SetGlobalTreasuryReadCollector(c *TreasuryReadMetricsCollector) {
	globalTreasuryReadCollector = c
}

// RecordTreasuryRead counts one treasury read served by source. No-op when metrics are
// disabled. Resolved through the global LAZILY, per call, because the treasury reader is
// constructed before the collector is.
func RecordTreasuryRead(source string) {
	globalTreasuryReadCollector.Record(source)
}

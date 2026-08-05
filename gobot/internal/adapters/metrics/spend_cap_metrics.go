package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// SpendCapMetricsCollector counts buys refused by the CROSS-OPERATION concurrent spend cap —
// the ones whose own cost cleared the working-capital reserve and whose COMBINED in-flight
// exposure did not (sp-ps2oc acceptance 5).
//
// This is deliberately a separate signal from a per-buy floor park, which every coordinator
// already logs plainly. An aggregate denial was previously recoverable only by forensic ledger
// archaeology: the sp-ps2oc drain was diagnosed by noticing that three PURCHASE_CARGO rows 68ms
// apart recorded an IDENTICAL balance_after. Counting the denial makes "concurrent spend is
// contending for the float" a graph — and, just as importantly, makes a cap that has silently
// stopped being consulted visible as a series that flatlines rather than as nothing at all.
//
// The `operation` label is what the aggregate cap is FOR: construction_supply, contract and
// tour draw on one treasury, so which of them is being turned away is the whole question.
type SpendCapMetricsCollector struct {
	aggregateDenialsTotal *prometheus.CounterVec
}

// NewSpendCapMetricsCollector creates the concurrent-spend-cap collector.
func NewSpendCapMetricsCollector() *SpendCapMetricsCollector {
	return &SpendCapMetricsCollector{
		aggregateDenialsTotal: newCounterVec(
			"spend_cap_aggregate_denials_total",
			"Buys refused because COMBINED in-flight spend would breach the working-capital reserve, though the buy's own cost cleared it",
			"operation",
		),
	}
}

// Register registers the spend-cap metrics with the Prometheus registry.
func (c *SpendCapMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(c.aggregateDenialsTotal)
}

// RecordAggregateDenial counts one aggregate-headroom refusal for an operation.
func (c *SpendCapMetricsCollector) RecordAggregateDenial(operation string) {
	c.aggregateDenialsTotal.WithLabelValues(operation).Inc()
}

// AggregateDenialCount reports the denials recorded for an operation so far.
//
// Exported so the SPEND GUARDS' own packages can prove they emit. Without a reader reachable
// from there, "the collector counts what it is told" and "the guard tells it" are two separate
// claims and only the first is testable — which is precisely how a mitigation ships green and
// silently measures nothing. Returns 0 if the metric cannot be read.
func (c *SpendCapMetricsCollector) AggregateDenialCount(operation string) float64 {
	var m dto.Metric
	if err := c.aggregateDenialsTotal.WithLabelValues(operation).Write(&m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}

// globalSpendCapCollector is the singleton set by SetGlobalSpendCapCollector when metrics are
// enabled. Guards run deep inside executors that are constructed long before (and independently
// of) the metrics stack, so the package-level recorder below is the established idiom here
// rather than threading a collector through every spend path.
var globalSpendCapCollector *SpendCapMetricsCollector

// SetGlobalSpendCapCollector sets the global spend-cap metrics collector.
func SetGlobalSpendCapCollector(collector *SpendCapMetricsCollector) {
	globalSpendCapCollector = collector
}

// RecordAggregateSpendDenial counts an aggregate-headroom refusal globally. A no-op when
// metrics are disabled, so a money guard never depends on the metrics stack being up.
func RecordAggregateSpendDenial(operation string) {
	if globalSpendCapCollector != nil {
		globalSpendCapCollector.RecordAggregateDenial(operation)
	}
}

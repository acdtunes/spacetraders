package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// sp-ps2oc acceptance 5. An AGGREGATE denial — a buy whose own cost cleared the reserve and
// whose combined in-flight exposure did not — must be countable. Before this, the only trace
// was in the transaction ledger, and the incident was diagnosed by noticing that three
// PURCHASE_CARGO rows 68ms apart shared a balance_after.
func TestSpendCapCollector_CountsAggregateDenialsPerOperation(t *testing.T) {
	c := NewSpendCapMetricsCollector()

	c.RecordAggregateDenial("construction_supply")
	c.RecordAggregateDenial("construction_supply")
	c.RecordAggregateDenial("contract")

	require.Equal(t, 2.0, testutil.ToFloat64(c.aggregateDenialsTotal.WithLabelValues("construction_supply")))
	require.Equal(t, 1.0, testutil.ToFloat64(c.aggregateDenialsTotal.WithLabelValues("contract")))

	// The label is the whole point: three operations draw on one treasury, so a single
	// undifferentiated total could not answer WHICH of them is being turned away — the only
	// question an operator actually has when the cap starts firing.
	require.NotEqual(t,
		testutil.ToFloat64(c.aggregateDenialsTotal.WithLabelValues("construction_supply")),
		testutil.ToFloat64(c.aggregateDenialsTotal.WithLabelValues("contract")),
		"denials must be counted per operation, not merged into one series")
}

// The guard must never depend on the metrics stack being up. Money guards run deep inside
// executors built long before (and independently of) the collectors, so an unset global
// recorder is a no-op rather than a nil dereference that would take a spend path down.
func TestRecordAggregateSpendDenial_IsSafeWithoutACollector(t *testing.T) {
	previous := globalSpendCapCollector
	t.Cleanup(func() { SetGlobalSpendCapCollector(previous) })

	globalSpendCapCollector = nil
	require.NotPanics(t, func() { RecordAggregateSpendDenial("construction_supply") },
		"a money guard must not panic because metrics are disabled")
}

// The package-level recorder the guards actually call must reach the wired collector. Without
// this, RecordAggregateSpendDenial could be a permanent no-op and every test above would still
// pass — the counter would ship green and count nothing, which is the failure mode this whole
// bead is about.
func TestRecordAggregateSpendDenial_ReachesTheWiredCollector(t *testing.T) {
	previous := globalSpendCapCollector
	t.Cleanup(func() { SetGlobalSpendCapCollector(previous) })

	c := NewSpendCapMetricsCollector()
	SetGlobalSpendCapCollector(c)

	RecordAggregateSpendDenial("construction_supply")

	require.Equal(t, 1.0, testutil.ToFloat64(c.aggregateDenialsTotal.WithLabelValues("construction_supply")),
		"the global recorder the spend guards call must increment the wired collector")
}

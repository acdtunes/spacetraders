// internal/adapters/metrics/daemon_component_metrics_test.go
package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// The supervise layer reports restarts through the same global-shim pattern
// as RecordContainerRestart; the counter is labeled by component only (a
// small fixed set — no cardinality risk).
func TestRecordDaemonComponentRestart_IncrementsCounter(t *testing.T) {
	InitRegistry()
	defer func() { Registry = nil; globalCollector = nil }()

	collector := NewContainerMetricsCollector(nil, nil)
	require.NoError(t, collector.Register())
	SetGlobalCollector(collector)

	RecordDaemonComponentRestart("ship-state-sweeper")
	RecordDaemonComponentRestart("ship-state-sweeper")

	count := testutil.ToFloat64(collector.daemonComponentRestarts.WithLabelValues("ship-state-sweeper"))
	require.Equal(t, 2.0, count)
}

// With no collector installed the shim is a no-op, never a nil-deref — the
// supervise layer must work in metrics-disabled boots.
func TestRecordDaemonComponentRestart_NoCollectorIsNoop(t *testing.T) {
	prev := globalCollector
	globalCollector = nil
	defer func() { globalCollector = prev }()
	require.NotPanics(t, func() { RecordDaemonComponentRestart("x") })
}

func TestDaemonComponentRestartMetricName(t *testing.T) {
	InitRegistry()
	defer func() { Registry = nil }()
	collector := NewContainerMetricsCollector(nil, nil)
	require.NoError(t, collector.Register())
	collector.RecordDaemonComponentRestart("recovery")

	families, err := Registry.Gather()
	require.NoError(t, err)
	found := false
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "spacetraders_daemon_component_restarts_total") {
			found = true
		}
	}
	require.True(t, found, "metric must be spacetraders_daemon_component_restarts_total")
}

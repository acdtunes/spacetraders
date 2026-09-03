package ship_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-unw6h: a tour hull arriving at a market where a MONEY-GUARDED trade is about to
// run scans it twice within seconds — once on arrival, once for the guard's own live
// pre-trade read (WithLiveScanRequired). The guard's read is the one that protects the
// trade, so the arrival read is pure duplication (measured: 167 such pairs/hour, 2.3%
// of the API budget). A leg whose trades are ALL guarded stamps the marker and the
// route executor skips exactly that waypoint's arrival scan.

// arrivalScanMarketWaypoint is the marketplace both arrival-scan suites fly to.
const arrivalScanMarketWaypoint = "X1-RTE-MKT"

// deferralLogCapture records the executor's log lines so the deferral can be asserted
// on the line it emits, not just on the absent GetMarket.
type deferralLogCapture struct {
	mu      sync.Mutex
	entries []map[string]interface{}
	msgs    []string
}

func (l *deferralLogCapture) Log(_, message string, fields map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, message)
	l.entries = append(l.entries, fields)
}

func (l *deferralLogCapture) sawAction(action string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, f := range l.entries {
		if f["action"] == action {
			return true
		}
	}
	return false
}

// fieldOf is the value field carried by the first line logging action; nil if none did.
func (l *deferralLogCapture) fieldOf(action, field string) interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, f := range l.entries {
		if f["action"] == action {
			return f[field]
		}
	}
	return nil
}

func (l *deferralLogCapture) messagesContain(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// THE RED case: the market is stale (so the freshness gate would certainly scan it),
// yet the marker names this waypoint — the guard's live read is coming, so no
// GetMarket fires here.
func TestArrivalScan_DeferredToTradeGuard_SkipsGetMarket(t *testing.T) {
	prevRegistry := metrics.Registry
	t.Cleanup(func() { metrics.Registry = prevRegistry })
	metrics.Registry = prometheus.NewRegistry()
	collector := metrics.NewScanDedupMetricsCollector()
	require.NoError(t, collector.Register())
	metrics.SetGlobalScanDedupCollector(collector)
	t.Cleanup(func() { metrics.SetGlobalScanDedupCollector(nil) })

	logger := &deferralLogCapture{}
	gets := runArrivalScanWith(t, time.Now().Add(-10*time.Minute), &shared.ScanPolicy{MaxScanAge: 90 * time.Second},
		func(ctx context.Context) context.Context {
			return shared.WithArrivalScanDeferred(ctx, arrivalScanMarketWaypoint, shared.ArrivalScanSideSell)
		}, logger)

	require.Equal(t, 0, gets, "a money-guarded trade live-reads this market seconds later - the arrival scan must not duplicate it")
	require.True(t, logger.sawAction("scan_deferred_to_guard"), "the skipped scan must be logged with action=scan_deferred_to_guard, got %v", logger.entries)
	require.True(t, logger.messagesContain("Arrival market scan deferred to the trade guard"))
	require.False(t, logger.sawAction("scan_market"), "a deferred arrival must not also claim it is scanning")
	// sp-htzl1.11: the split between buy legs and sell legs must be queryable, so the line
	// carries the side that deferred it AND the counter records it under that label.
	require.Equal(t, shared.ArrivalScanSideSell, logger.fieldOf("scan_deferred_to_guard", "side"),
		"the deferral line must name the side whose guard is doing the reading, got %v", logger.entries)
	require.Equal(t, 1.0, deferralCountBySide(t, shared.ArrivalScanSideSell),
		"the skipped scan must also be counted on arrival_scan_deferred_total{side=\"sell\"}")
}

// deferralCountBySide reads the deferral counter for one side off the test registry.
func deferralCountBySide(t *testing.T, side string) float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != "spacetraders_daemon_arrival_scan_deferred_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "side" && l.GetValue() == side {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// The marker is waypoint-scoped: a stamp naming a DIFFERENT waypoint (a flight's jump
// gate, say, which no trade guard covers) leaves this arrival scanning exactly as today.
func TestArrivalScan_DeferralNamingAnotherWaypoint_StillScans(t *testing.T) {
	gets := runArrivalScanWith(t, time.Now().Add(-10*time.Minute), &shared.ScanPolicy{MaxScanAge: 90 * time.Second},
		func(ctx context.Context) context.Context {
			return shared.WithArrivalScanDeferred(ctx, "X1-RTE-OTHER", shared.ArrivalScanSideBuy)
		}, nil)
	require.Equal(t, 1, gets, "only the stamped waypoint's scan is deferred")
}

// Unstamped, every arrival behaves exactly as before: stale re-scans, fresh reuses the
// cache. The deferral is inert for every caller that does not opt in.
func TestArrivalScan_WithoutTheMarker_IsUnchanged(t *testing.T) {
	policy := &shared.ScanPolicy{MaxScanAge: 90 * time.Second}
	require.Equal(t, 1, runArrivalScanWith(t, time.Now().Add(-10*time.Minute), policy, nil, nil))
	require.Equal(t, 0, runArrivalScanWith(t, time.Now(), policy, nil, nil))
}

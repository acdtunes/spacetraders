package ship_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
	logger := &deferralLogCapture{}
	gets := runArrivalScanWith(t, time.Now().Add(-10*time.Minute), &shared.ScanPolicy{MaxScanAge: 90 * time.Second},
		func(ctx context.Context) context.Context {
			return shared.WithArrivalScanDeferred(ctx, arrivalScanMarketWaypoint)
		}, logger)

	require.Equal(t, 0, gets, "a money-guarded trade live-reads this market seconds later - the arrival scan must not duplicate it")
	require.True(t, logger.sawAction("scan_deferred_to_guard"), "the skipped scan must be logged with action=scan_deferred_to_guard, got %v", logger.entries)
	require.True(t, logger.messagesContain("Arrival market scan deferred to the trade guard"))
	require.False(t, logger.sawAction("scan_market"), "a deferred arrival must not also claim it is scanning")
}

// The marker is waypoint-scoped: a stamp naming a DIFFERENT waypoint (a flight's jump
// gate, say, which no trade guard covers) leaves this arrival scanning exactly as today.
func TestArrivalScan_DeferralNamingAnotherWaypoint_StillScans(t *testing.T) {
	gets := runArrivalScanWith(t, time.Now().Add(-10*time.Minute), &shared.ScanPolicy{MaxScanAge: 90 * time.Second},
		func(ctx context.Context) context.Context {
			return shared.WithArrivalScanDeferred(ctx, "X1-RTE-OTHER")
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

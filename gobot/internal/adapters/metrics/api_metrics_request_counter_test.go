package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// ONE RecordAPIRequest CALL IS ONE api_requests_total INCREMENT (sp-fr19d).
//
// This is the companion to the api package's parity test, which substitutes a call-counting fake
// for the real Prometheus counter. That substitution is only sound if the mapping is exactly 1:1 —
// otherwise the fake would be measuring itself, and the parity test would go green while the
// dashboards and the money guard drifted apart.
//
// The number the autosizer's api_util guard is compared against is derived from this counter, so
// "how many increments does one request produce" is load-bearing arithmetic, not bookkeeping.
func TestRecordAPIRequestIncrementsTheRequestCounterExactlyOncePerCall(t *testing.T) {
	collector := NewAPIMetricsCollector()

	// CollectAndCount, not ToFloat64, for the empty case: a CounterVec with no label combination yet
	// has ZERO child series, and ToFloat64 panics rather than returning 0 on that.
	if got := testutil.CollectAndCount(collector.apiRequestsTotal); got != 0 {
		t.Fatalf("a fresh collector already has %d series, want 0", got)
	}

	collector.RecordAPIRequest("GET", "/my/ships", 200, 0.1)
	if got := testutil.ToFloat64(collector.apiRequestsTotal); got != 1 {
		t.Fatalf("one RecordAPIRequest call produced %v increments, want exactly 1; the api-package parity test substitutes a call counter for this metric and that stands only while the mapping is 1:1", got)
	}

	for i := 0; i < 4; i++ {
		collector.RecordAPIRequest("GET", "/my/ships", 200, 0.1)
	}
	if got := testutil.ToFloat64(collector.apiRequestsTotal); got != 5 {
		t.Fatalf("five calls produced %v increments, want 5", got)
	}
}

// A NIL-FIELD COLLECTOR RECORDS NOTHING AND PANICS NOTHING. Recording is best-effort on the request
// path; what matters for sp-fr19d is that the failure mode is a MISSING increment rather than a
// crash, because a missing increment is what makes the two utilization figures disagree.
func TestRecordAPIRequestOnAnUninitialisedCollectorIsANoOp(t *testing.T) {
	var nilCollector *APIMetricsCollector
	nilCollector.RecordAPIRequest("GET", "/my/ships", 200, 0.1) // must not panic

	empty := &APIMetricsCollector{}
	empty.RecordAPIRequest("GET", "/my/ships", 200, 0.1) // must not panic
}

package metrics

import "testing"

// A crash in the metrics side-channel must never take down the request.
// Recording is best-effort: a typed-nil receiver or a collector whose metric
// handles were never initialized must degrade to a no-op, not a SIGSEGV.
//
// Reproduces the ship-sell nil-pointer panic in
// APIMetricsCollector.RecordRateLimitWait (api_metrics.go:134).
func TestRecordRateLimitWait_NilReceiver_DoesNotPanic(t *testing.T) {
	var c *APIMetricsCollector // typed-nil, as boxed into APIMetricsRecorder
	c.RecordRateLimitWait("POST", "/my/ships/TORWIND-1/sell", 0.5)
}

func TestRecordMethods_UninitializedFields_DoNotPanic(t *testing.T) {
	// Non-nil receiver but metric handles never initialized (e.g. &APIMetricsCollector{}).
	c := &APIMetricsCollector{}
	c.RecordAPIRequest("POST", "/my/ships/TORWIND-1/sell", 429, 0.1)
	c.RecordAPIRetry("POST", "/my/ships/TORWIND-1/sell", "rate_limited")
	c.RecordRateLimitWait("POST", "/my/ships/TORWIND-1/sell", 0.5)
	c.SetRateLimiterTokens(3)
}

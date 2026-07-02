package api

import "testing"

// With metrics disabled, the global collector is a typed-nil *APIMetricsCollector.
// getMetricsCollector must not box it into a non-nil interface — that nil-check
// bypass crashed the daemon on every API call (SIGSEGV in RecordRateLimitWait).
func TestGetMetricsCollectorReturnsUntypedNilWhenDisabled(t *testing.T) {
	c := NewSpaceTradersClient()
	if collector := c.getMetricsCollector(); collector != nil {
		t.Fatalf("expected nil interface when metrics disabled, got %T", collector)
	}
}

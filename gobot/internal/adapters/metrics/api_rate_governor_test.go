package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// THE EXPERIMENT IS ONLY READABLE IF BOTH HALVES EXPORT: the rate we are actually running
// at, and every 429 that took it away (sp-g7jep).
func TestRateGovernorMetricsRegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewAPIMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.SetRateLimiterTarget(2.2)
	c.RecordRateGovernorTrip("get_agent")

	families := gatheredFamilies(t)
	effective, ok := families["spacetraders_daemon_api_rate_limiter_effective_req_per_sec"]
	if !ok {
		t.Fatal("the effective-rate gauge was not exported")
	}
	if got := effective.samples[0].value; got != 2.2 {
		t.Errorf("effective_req_per_sec = %v, want 2.2", got)
	}
	trips, ok := families["spacetraders_daemon_api_rate_governor_trips_total"]
	if !ok {
		t.Fatal("the governor-trip counter was not exported")
	}
	if got := trips.sample(t, "endpoint", "get_agent").value; got != 1 {
		t.Errorf("trips_total{endpoint=\"get_agent\"} = %v, want 1", got)
	}
}

func TestRateGovernorMetrics_NilAndUninitialized_DoNotPanic(t *testing.T) {
	var typedNil *APIMetricsCollector
	typedNil.SetRateLimiterTarget(2.2)
	typedNil.RecordRateGovernorTrip("get_agent")
	(&APIMetricsCollector{}).SetRateLimiterTarget(2.2)
	(&APIMetricsCollector{}).RecordRateGovernorTrip("get_agent")
}

package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// THE TWO API-UTILIZATION FIGURES SHARE A BASIS, AND THIS FILE MAKES THAT EXECUTABLE (sp-fr19d).
//
// autosizerAPIUtilReader's doc comment asserts it reads "the SAME throughput/ceiling basis as the
// Prometheus ApproachCeiling alert (sum(rate(api_requests_total[5m])) / RateLimitPerSecond)". That
// was a claim in prose only: the two recorders live in the same retry loop but nothing anywhere
// compared them, so a drift between them could not fail any test.
//
// It matters because the autosizer's api_util money guard reads the TRACKER while the dashboards
// and alerts read PROMETHEUS. When those disagree the guard is deciding whether to buy a hull on a
// number nobody is looking at.

// countingAPIRecorder counts RecordAPIRequest calls — the exact operation that increments
// api_requests_total.
//
// It stands in for the Prometheus counter rather than reading the real one because the real
// counter's field is unexported in another package and registering it here would leak into the
// global registry. The substitution is sound because the mapping is ONE Inc() per call
// (api_metrics.go RecordAPIRequest), and that 1:1 property is not assumed here — it is pinned
// separately in the metrics package by
// TestRecordAPIRequestIncrementsTheRequestCounterExactlyOncePerCall. Without that companion this
// fake would be measuring itself.
type countingAPIRecorder struct {
	requests int
	retries  int
}

func (c *countingAPIRecorder) RecordAPIRequest(_ string, _ string, _ int, _ float64)       { c.requests++ }
func (c *countingAPIRecorder) RecordAPIRetry(_ string, _ string, _ string)                 { c.retries++ }
func (c *countingAPIRecorder) RecordRateLimitWait(_ string, _ string, _ float64)           {}
func (c *countingAPIRecorder) SetRateLimiterTokens(_ float64)                              {}
func (c *countingAPIRecorder) RecordRateLimitHeaders(_ string, _, _, _, _ float64, _ bool) {}
func (c *countingAPIRecorder) SetRateLimiterTarget(_ float64)                              {}
func (c *countingAPIRecorder) RecordRateGovernorTrip(_ string)                             {}

// ON CLEAN TRAFFIC THE TWO COUNTS ARE EQUAL, and the tracker's utilization equals the Grafana
// panel's own formula recomputed here from the same count.
//
// The percentage assertion is the point. Equal COUNTS would still permit a 10x divergence in the
// reported percentage if either side's window or divisor were wrong — a per-second figure divided
// by a per-5-minute one, or a divisor of 20 where 2 belongs. Recomputing the panel expression
// independently is what makes a unit error visible rather than merely a counting error.
func TestTrackerUtilizationMatchesThePrometheusPanelFormulaOnIdenticalTraffic(t *testing.T) {
	const requests = 12

	server, _ := flakyServer(t, 500, 0, "") // never fails: one attempt per request
	client, _ := newRetryTestClient(server.URL, 5)

	// A WARM OBSERVER, and the warming is load-bearing rather than incidental setup. The Prometheus
	// panel this parity claim is against measures steady state, and since sp-fr19d the tracker
	// narrows its window to the span it has actually observed — so a tracker constructed moments
	// before the traffic legitimately reports a far higher percentage over a sub-second window. The
	// two figures share a basis once both have a full window; asserting parity on a cold tracker
	// would be comparing a 5-minute average against a 200-millisecond one.
	clock := &shared.MockClock{CurrentTime: time.Unix(0, 0).UTC()}
	tracker := metrics.NewAPIBudgetTracker(RateLimitPerSecond, clock)
	clock.Advance(5 * time.Minute) // observing for a full window before any traffic arrives

	client.SetBudgetTracker(tracker)
	recorder := &countingAPIRecorder{}
	client.SetMetricsCollector(recorder)

	for i := 0; i < requests; i++ {
		var result namedPayload
		if err := client.request(context.Background(), "GET", fmt.Sprintf("/my/ships/TORWIND-%d/dock", i), "token", nil, &result); err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
	}

	rolling := tracker.Report().Rolling5m

	// Non-vacuity FIRST: with zero traffic every assertion below holds trivially at 0 == 0 == 0%.
	if recorder.requests == 0 || rolling.TotalRequests == 0 {
		t.Fatalf("no traffic was recorded at all (prometheus=%d tracker=%d); the fixture never exercised the recording path", recorder.requests, rolling.TotalRequests)
	}
	if recorder.requests != requests {
		t.Fatalf("prometheus side recorded %d of %d requests", recorder.requests, requests)
	}
	if rolling.TotalRequests != recorder.requests {
		t.Fatalf("tracker recorded %d requests, prometheus recorded %d, on identical traffic with no retries. The autosizer's api_util money guard reads the tracker while every dashboard and alert reads prometheus, so a gap here is a guard deciding a hull purchase on a number nobody watches", rolling.TotalRequests, recorder.requests)
	}

	// THE GRAFANA PANEL'S OWN EXPRESSION: sum(rate(api_requests_total[5m])) / RateLimitPerSecond * 100.
	// rate() over a 5m window is a PER-SECOND average, which is count/300 — written out rather than
	// reusing any helper the tracker also uses, so a shared unit error cannot cancel itself out.
	panelPct := (float64(recorder.requests) / 300.0) / RateLimitPerSecond * 100.0
	if diff := rolling.UtilizationPct - panelPct; diff > 0.001 || diff < -0.001 {
		t.Fatalf("tracker reports %.4f%% utilization but the Prometheus panel formula on the SAME %d requests gives %.4f%% (window=%.0fs ceiling=%.2f req/s). The doc comment claims both read one basis; a gap of this shape is a window or divisor error, not a counting one", rolling.UtilizationPct, recorder.requests, panelPct, rolling.WindowSeconds, rolling.CeilingReqPerSec)
	}

	// The two inputs to that formula, pinned individually so a failure above names which one moved.
	if rolling.WindowSeconds != 300 {
		t.Fatalf("Rolling5m window is %.0fs, want 300 — rate(...[5m]) averages over exactly 5 minutes", rolling.WindowSeconds)
	}
	if rolling.CeilingReqPerSec != RateLimitPerSecond {
		t.Fatalf("tracker ceiling is %.2f req/s but the account allowance is %.2f; the panel divides by the latter as a literal", rolling.CeilingReqPerSec, RateLimitPerSecond)
	}
}

// THE TRACKER CANNOT STRUCTURALLY READ LOWER THAN PROMETHEUS — it counts every ATTEMPT, Prometheus
// counts only the TERMINAL outcome, so the tracker is a strict superset on retrying traffic.
//
// This is the falsifier for "the tracker undercounts". It pins the DIRECTION of the difference, so
// any future change that moves the tracker's record call behind classify() — where it would start
// missing retried attempts and could then read lower — fails here rather than silently making the
// api_util guard permissive again.
func TestTrackerCountsEveryAttemptSoItCanNeverReadLowerThanPrometheus(t *testing.T) {
	server, attempts := flakyServer(t, 503, 2, "") // two failures, then success: 3 attempts, 1 request
	client, _ := newRetryTestClient(server.URL, 5)
	clock := &shared.MockClock{CurrentTime: time.Unix(0, 0).UTC()}
	tracker := metrics.NewAPIBudgetTracker(RateLimitPerSecond, clock)
	clock.Advance(5 * time.Minute) // warm, so the counts compared below are the only variable
	client.SetBudgetTracker(tracker)
	recorder := &countingAPIRecorder{}
	client.SetMetricsCollector(recorder)

	var result namedPayload
	if err := client.request(context.Background(), "GET", "/my/ships/TORWIND-1/dock", "token", nil, &result); err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}

	// Non-vacuity: the retry path must actually have been taken, or "superset" is an empty claim.
	if *attempts != 3 {
		t.Fatalf("server saw %d attempts, want 3; without retries this fixture cannot distinguish a superset from equality", *attempts)
	}

	rolling := tracker.Report().Rolling5m
	if rolling.TotalRequests != 3 {
		t.Fatalf("tracker recorded %d events across 3 attempts; it records once per ATTEMPT, before classify() branches", rolling.TotalRequests)
	}
	if recorder.requests != 1 {
		t.Fatalf("prometheus recorded %d requests for one retried call; it records only the TERMINAL outcome", recorder.requests)
	}
	if rolling.TotalRequests < recorder.requests {
		t.Fatalf("tracker (%d) read LOWER than prometheus (%d). The tracker's coverage is a strict superset by construction, so this direction is impossible without the record call having moved behind the retry branch — which is exactly what would make the api_util guard permissive on saturated traffic", rolling.TotalRequests, recorder.requests)
	}
}

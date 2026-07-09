package api

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
)

// These tests exercise the sp-51ti budget-tracker instrumentation wired
// through doWithRetry, the single choke point every SpaceTradersClient method
// calls through (via c.request / c.requestWithErrorParsing).

func TestRequestRecordsBudgetEventOnSuccess(t *testing.T) {
	server, _ := flakyServer(t, 500, 0, "") // no failures: succeeds on first attempt
	client, _ := newRetryTestClient(server.URL, 5)
	tracker := metrics.NewAPIBudgetTracker(2.0, nil)
	client.SetBudgetTracker(tracker)

	var result namedPayload
	err := client.request(context.Background(), "GET", "/my/ships/TORWIND-1/dock", "token", nil, &result)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	report := tracker.Report()
	if report.Rolling5m.TotalRequests != 1 {
		t.Fatalf("expected 1 recorded request, got %d", report.Rolling5m.TotalRequests)
	}
	if len(report.Rolling5m.PerHull) != 1 || report.Rolling5m.PerHull[0].Hull != "TORWIND-1" {
		t.Fatalf("expected per-hull attribution to TORWIND-1, got %+v", report.Rolling5m.PerHull)
	}
}

func TestRequestRecordsOneEventPerAttemptWithRetryPurpose(t *testing.T) {
	server, attempts := flakyServer(t, 503, 2, "")
	client, _ := newRetryTestClient(server.URL, 5)
	tracker := metrics.NewAPIBudgetTracker(2.0, nil)
	client.SetBudgetTracker(tracker)

	var result namedPayload
	err := client.request(context.Background(), "GET", "/my/ships/TORWIND-1/dock", "token", nil, &result)
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if *attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", *attempts)
	}

	report := tracker.Report()
	if report.Rolling5m.TotalRequests != 3 {
		t.Fatalf("expected 3 recorded events (one per attempt), got %d", report.Rolling5m.TotalRequests)
	}
	if got := report.Rolling5m.PurposeCounts[apibudget.PurposeRetry]; got != 2 {
		t.Fatalf("expected 2 retry-purpose events, got %d", got)
	}
	if got := report.Rolling5m.PurposeCounts[apibudget.PurposePoll]; got != 1 {
		t.Fatalf("expected 1 poll-purpose event (first GET attempt), got %d", got)
	}
}

func TestRequestRecordsBudgetEventForFleetWidePathWithNoHullAttribution(t *testing.T) {
	server, _ := flakyServer(t, 500, 0, "")
	client, _ := newRetryTestClient(server.URL, 5)
	tracker := metrics.NewAPIBudgetTracker(2.0, nil)
	client.SetBudgetTracker(tracker)

	var result namedPayload
	err := client.request(context.Background(), "GET", "/my/ships", "token", nil, &result)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	report := tracker.Report()
	if report.Rolling5m.TotalRequests != 1 {
		t.Fatalf("expected 1 recorded request, got %d", report.Rolling5m.TotalRequests)
	}
	if len(report.Rolling5m.PerHull) != 0 {
		t.Fatalf("expected no per-hull attribution for a fleet-wide path, got %+v", report.Rolling5m.PerHull)
	}
}

func TestRequestWithoutBudgetTrackerDoesNotPanic(t *testing.T) {
	server, _ := flakyServer(t, 500, 0, "")
	client, _ := newRetryTestClient(server.URL, 5)
	// No SetBudgetTracker call and no global tracker set: getBudgetTracker()
	// must return nil safely, and Record on that nil must be a no-op.

	var result namedPayload
	err := client.request(context.Background(), "GET", "/my/ships/TORWIND-1/dock", "token", nil, &result)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

func TestRequestFallsBackToGlobalBudgetTrackerWhenNoneSetOnClient(t *testing.T) {
	t.Cleanup(func() { metrics.SetGlobalAPIBudgetTracker(nil) })
	tracker := metrics.NewAPIBudgetTracker(2.0, nil)
	metrics.SetGlobalAPIBudgetTracker(tracker)

	server, _ := flakyServer(t, 500, 0, "")
	client, _ := newRetryTestClient(server.URL, 5)
	// No per-instance SetBudgetTracker call: must fall back to the global.

	var result namedPayload
	err := client.request(context.Background(), "GET", "/my/ships/TORWIND-1/dock", "token", nil, &result)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	report := tracker.Report()
	if report.Rolling5m.TotalRequests != 1 {
		t.Fatalf("expected fallback to the global tracker to record 1 request, got %d", report.Rolling5m.TotalRequests)
	}
}

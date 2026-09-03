package api

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// THE SERVER'S OWN RATE-LIMIT BUDGET IS ONLY A GUESS UNTIL WE READ ITS HEADERS (sp-g7jep).
//
// The client enforces 2 req/s with a burst of 30 and has never looked at what the game
// actually reports. These tests pin the observation path only: what the headers say reaches
// the metrics collector unchanged. Nothing here may influence the limiter.

func rateLimitHeaderServer(t *testing.T, headers map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, value := range headers {
			w.Header().Set(name, value)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"data":{"name":"ok"}}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func recordOneResponse(t *testing.T, headers map[string]string) rateLimitHeaderObservation {
	t.Helper()
	server := rateLimitHeaderServer(t, headers)
	client, _ := newRetryTestClient(server.URL, 2)
	recorder := &recordingMetrics{}
	client.SetMetricsCollector(recorder)

	var result namedPayload
	if err := client.request(context.Background(), "GET", "/test", "token", nil, &result); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if len(recorder.rateLimitHeaders) != 1 {
		t.Fatalf("expected exactly one header observation, got %d", len(recorder.rateLimitHeaders))
	}
	return recorder.rateLimitHeaders[0]
}

func TestRateLimitHeadersAreObservedFromTheResponse(t *testing.T) {
	got := recordOneResponse(t, map[string]string{
		"X-Ratelimit-Type":             "IP Address",
		"X-Ratelimit-Limit-Per-Second": "2",
		"X-Ratelimit-Limit-Burst":      "30",
		"X-Ratelimit-Remaining":        "27",
		"X-Ratelimit-Reset":            "2026-09-03T15:00:00Z",
	})

	if got.kind != "IP Address" {
		t.Errorf("kind = %q, want %q", got.kind, "IP Address")
	}
	if got.perSecond != 2 || got.burst != 30 || got.remaining != 27 {
		t.Errorf("perSecond/burst/remaining = %v/%v/%v, want 2/30/27", got.perSecond, got.burst, got.remaining)
	}
	// The fake clock sits at the Unix epoch, so a 2026 reset stamp is far in its future.
	if got.resetSeconds <= 0 {
		t.Errorf("resetSeconds = %v, want a positive span computed from the client clock", got.resetSeconds)
	}
}

func TestRateLimitResetAcceptsAPlainSecondsValue(t *testing.T) {
	got := recordOneResponse(t, map[string]string{
		"X-Ratelimit-Type":  "Account",
		"X-Ratelimit-Reset": "45",
	})

	if got.resetSeconds != 45 {
		t.Errorf("resetSeconds = %v, want 45", got.resetSeconds)
	}
}

func TestResponseWithoutRateLimitHeadersRecordsTheAbsentSentinel(t *testing.T) {
	got := recordOneResponse(t, nil)

	if got.kind != "" {
		t.Errorf("kind = %q, want empty", got.kind)
	}
	if got.sawHeaders {
		t.Error("sawHeaders = true for a response carrying no x-ratelimit-* header at all")
	}
	for name, value := range map[string]float64{
		"perSecond":    got.perSecond,
		"burst":        got.burst,
		"remaining":    got.remaining,
		"resetSeconds": got.resetSeconds,
	} {
		if value != rateLimitHeaderAbsent {
			t.Errorf("%s = %v, want the absent sentinel %v", name, value, rateLimitHeaderAbsent)
		}
	}
}

func TestMalformedRateLimitHeaderOnlyPoisonsItsOwnField(t *testing.T) {
	got := recordOneResponse(t, map[string]string{
		"X-Ratelimit-Type":             "IP Address",
		"X-Ratelimit-Limit-Per-Second": "2",
		"X-Ratelimit-Remaining":        "not-a-number",
	})

	if got.perSecond != 2 {
		t.Errorf("perSecond = %v, want 2 — a sibling header's garbage must not touch it", got.perSecond)
	}
	if got.remaining != rateLimitHeaderAbsent {
		t.Errorf("remaining = %v, want the absent sentinel %v", got.remaining, rateLimitHeaderAbsent)
	}
	if got.burst != rateLimitHeaderAbsent {
		t.Errorf("burst = %v, want the absent sentinel %v", got.burst, rateLimitHeaderAbsent)
	}
}

// A SERVER SENDING NONSENSE IS NOT A SERVER GONE SILENT, and only the prefix scan can tell
// them apart: none of these five names is one we parse, so parse success cannot.
func TestRenamedRateLimitHeadersStillCountAsPresent(t *testing.T) {
	got := recordOneResponse(t, map[string]string{
		"X-Ratelimit-Remaining-V2": "27",
		"X-Ratelimit-Whatever":     "<html>nope</html>",
	})

	if !got.sawHeaders {
		t.Error("sawHeaders = false, but the response carried two x-ratelimit-* headers")
	}
	if got.kind != "" {
		t.Errorf("kind = %q, want empty", got.kind)
	}
	for name, value := range map[string]float64{
		"perSecond": got.perSecond, "burst": got.burst,
		"remaining": got.remaining, "resetSeconds": got.resetSeconds,
	} {
		if value != rateLimitHeaderAbsent {
			t.Errorf("%s = %v, want the absent sentinel — no known field was readable", name, value)
		}
	}
}

// NaN AND ±Inf POISON A PROMETHEUS SERIES PERMANENTLY: once written, every rate() and
// avg_over_time() over the window reads NaN. A negative allowance is equally impossible.
func TestNonFiniteAndNegativeRateLimitNumbersAreRejected(t *testing.T) {
	got := recordOneResponse(t, map[string]string{
		"X-Ratelimit-Type":             "IP Address",
		"X-Ratelimit-Limit-Per-Second": "NaN",
		"X-Ratelimit-Limit-Burst":      "+Inf",
		"X-Ratelimit-Remaining":        "-5",
		"X-Ratelimit-Reset":            "NaN",
	})

	if !got.sawHeaders {
		t.Error("sawHeaders = false, but five x-ratelimit-* headers arrived")
	}
	for name, value := range map[string]float64{
		"perSecond": got.perSecond, "burst": got.burst,
		"remaining": got.remaining, "resetSeconds": got.resetSeconds,
	} {
		if value != rateLimitHeaderAbsent {
			t.Errorf("%s = %v, want the absent sentinel %v", name, value, rateLimitHeaderAbsent)
		}
	}
}

// THE LOG LINE IS SAMPLED BY TIME, not by count: at 2 req/s an unsampled line would be
// 172,800 lines a day. It also carries the x-ratelimit-* headers and nothing else.
func TestRateLimitHeaderLogIsSampledOncePerInterval(t *testing.T) {
	var out bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&out)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
		rateLimitLogLast.Store(0)
	})

	header := http.Header{}
	header.Set("X-Ratelimit-Remaining", "27")
	header.Set("Authorization", "Bearer secret-token")
	header.Set("Content-Type", "application/json")

	start := time.Unix(1_700_000_000, 0).UTC()
	rateLimitLogLast.Store(0)
	logRateLimitHeaders(header, 200, start)
	logRateLimitHeaders(header, 200, start.Add(59*time.Second))
	logRateLimitHeaders(header, 200, start.Add(61*time.Second))

	lines := strings.Count(out.String(), "API rate-limit headers")
	if lines != 2 {
		t.Errorf("logged %d lines over 61s, want 2 (one per 60s interval): %q", lines, out.String())
	}
	if strings.Contains(out.String(), "secret-token") || strings.Contains(strings.ToLower(out.String()), "authorization") {
		t.Errorf("the log line leaked a non-rate-limit header: %q", out.String())
	}
	if !strings.Contains(out.String(), `x-ratelimit-remaining="27"`) {
		t.Errorf("the log line dropped the header it exists to show: %q", out.String())
	}
	// The package prefixes WARNING:/ERROR: explicitly; an unlevelled line reads as unclassified.
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if !strings.HasPrefix(line, "INFO: ") {
			t.Errorf("log line %q does not carry the INFO: level prefix", line)
		}
	}
}

func TestResponseWithoutRateLimitHeadersLogsNothing(t *testing.T) {
	var out bytes.Buffer
	prevWriter := log.Writer()
	log.SetOutput(&out)
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		rateLimitLogLast.Store(0)
	})

	rateLimitLogLast.Store(0)
	logRateLimitHeaders(http.Header{"Content-Type": []string{"application/json"}}, 200, time.Unix(1_700_000_000, 0).UTC())

	if out.Len() != 0 {
		t.Errorf("expected no log output, got %q", out.String())
	}
}

// A transport failure carries no response at all, so it must not be counted as a server that
// stopped sending the headers.
func TestNetworkErrorRecordsNoHeaderObservation(t *testing.T) {
	server := rateLimitHeaderServer(t, nil)
	url := server.URL
	server.Close()

	client, _ := newRetryTestClient(url, 0)
	recorder := &recordingMetrics{}
	client.SetMetricsCollector(recorder)

	var result namedPayload
	if err := client.request(context.Background(), "GET", "/test", "token", nil, &result); err == nil {
		t.Fatal("expected the request against a closed server to fail")
	}
	if len(recorder.rateLimitHeaders) != 0 {
		t.Fatalf("expected no header observations, got %d", len(recorder.rateLimitHeaders))
	}
}

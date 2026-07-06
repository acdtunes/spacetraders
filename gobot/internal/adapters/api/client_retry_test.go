package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type recordingMetrics struct {
	retryReasons     []string
	rateLimitWaits   int
	requestStatuses  []int
	rateLimiterCalls int
}

func (r *recordingMetrics) RecordAPIRequest(method string, endpoint string, statusCode int, duration float64) {
	r.requestStatuses = append(r.requestStatuses, statusCode)
}

func (r *recordingMetrics) RecordAPIRetry(method string, endpoint string, reason string) {
	r.retryReasons = append(r.retryReasons, reason)
}

func (r *recordingMetrics) RecordRateLimitWait(method string, endpoint string, duration float64) {
	r.rateLimitWaits++
}

func (r *recordingMetrics) SetRateLimiterTokens(tokens float64) {
	r.rateLimiterCalls++
}

func newRetryTestClient(serverURL string, maxRetries int) (*SpaceTradersClient, *shared.MockClock) {
	clock := &shared.MockClock{CurrentTime: time.Unix(0, 0).UTC()}
	client := NewSpaceTradersClientWithConfig(serverURL, maxRetries, 10*time.Millisecond, clock)
	return client, clock
}

func flakyServer(t *testing.T, failStatus, failures int, retryAfter string) (*httptest.Server, *int) {
	t.Helper()
	attempts := new(int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*attempts++
		if *attempts <= failures {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.WriteHeader(failStatus)
			fmt.Fprint(w, `{"error":{"code":42901,"message":"fail"}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"data":{"name":"ok"}}`)
	}))
	t.Cleanup(server.Close)
	return server, attempts
}

type namedPayload struct {
	Data struct {
		Name string `json:"name"`
	} `json:"data"`
}

func TestRequestRetriesThenSucceedsOnRetryableStatus(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			server, attempts := flakyServer(t, status, 2, "")
			client, _ := newRetryTestClient(server.URL, 5)

			var result namedPayload
			err := client.request(context.Background(), "GET", "/test", "token", nil, &result)

			if err != nil {
				t.Fatalf("expected success after retries, got: %v", err)
			}
			if *attempts != 3 {
				t.Fatalf("expected 3 attempts, got %d", *attempts)
			}
			if result.Data.Name != "ok" {
				t.Fatalf("expected parsed result, got %+v", result)
			}
		})
	}
}

func TestRequestHonorsRetryAfterHeaderWithoutJitter(t *testing.T) {
	server, attempts := flakyServer(t, 429, 1, "7")
	client, clock := newRetryTestClient(server.URL, 3)
	start := clock.CurrentTime

	var result namedPayload
	err := client.request(context.Background(), "GET", "/test", "token", nil, &result)

	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if *attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", *attempts)
	}
	if elapsed := clock.CurrentTime.Sub(start); elapsed != 7*time.Second {
		t.Fatalf("expected exact 7s Retry-After sleep, slept %v", elapsed)
	}
}

func TestRequestFallsBackToJitteredBackoffOnMalformedRetryAfter(t *testing.T) {
	server, attempts := flakyServer(t, 429, 1, "not-a-number")
	client, clock := newRetryTestClient(server.URL, 3)
	start := clock.CurrentTime

	var result namedPayload
	err := client.request(context.Background(), "GET", "/test", "token", nil, &result)

	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if *attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", *attempts)
	}
	elapsed := clock.CurrentTime.Sub(start)
	if elapsed < 5*time.Millisecond || elapsed > 15*time.Millisecond {
		t.Fatalf("expected jittered backoff between 5ms and 15ms, slept %v", elapsed)
	}
}

func TestRequestFailsAfterMaxRetriesExhausted(t *testing.T) {
	server, attempts := flakyServer(t, 503, 1000, "")
	client, _ := newRetryTestClient(server.URL, 2)
	recorder := &recordingMetrics{}
	client.SetMetricsCollector(recorder)

	err := client.request(context.Background(), "GET", "/test", "token", nil, nil)

	if err == nil || !strings.Contains(err.Error(), "max retries exceeded") {
		t.Fatalf("expected max retries exceeded error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "service unavailable (503)") {
		t.Fatalf("expected 503 cause in error, got: %v", err)
	}
	if *attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", *attempts)
	}
	if len(recorder.requestStatuses) != 1 || recorder.requestStatuses[0] != 503 {
		t.Fatalf("expected final 503 request metric, got %v", recorder.requestStatuses)
	}
}

func TestRequestDoesNotRetryNonRetryable4xx(t *testing.T) {
	server, attempts := flakyServer(t, 404, 1000, "")
	client, _ := newRetryTestClient(server.URL, 5)

	err := client.request(context.Background(), "GET", "/test", "token", nil, nil)

	if err == nil || !strings.Contains(err.Error(), "API error (status 404)") {
		t.Fatalf("expected 404 API error, got: %v", err)
	}
	if *attempts != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", *attempts)
	}
}

func TestRequestEmitsRateLimiterMetricsOn429(t *testing.T) {
	server, attempts := flakyServer(t, 429, 2, "")
	client, _ := newRetryTestClient(server.URL, 5)
	recorder := &recordingMetrics{}
	client.SetMetricsCollector(recorder)

	var result namedPayload
	err := client.request(context.Background(), "GET", "/test", "token", nil, &result)

	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if *attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", *attempts)
	}
	if recorder.rateLimitWaits != 3 {
		t.Fatalf("expected 3 rate limit wait metrics, got %d", recorder.rateLimitWaits)
	}
	if len(recorder.retryReasons) != 2 || recorder.retryReasons[0] != "rate_limited_429" || recorder.retryReasons[1] != "rate_limited_429" {
		t.Fatalf("expected two rate_limited_429 retry metrics, got %v", recorder.retryReasons)
	}
	if len(recorder.requestStatuses) != 1 || recorder.requestStatuses[0] != 200 {
		t.Fatalf("expected final 200 request metric, got %v", recorder.requestStatuses)
	}
}

func TestRequestWritesNoDebugOutputToStdoutOn429(t *testing.T) {
	server, _ := flakyServer(t, 429, 2, "")
	client, _ := newRetryTestClient(server.URL, 5)

	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = original }()

	var result namedPayload
	requestErr := client.request(context.Background(), "GET", "/test", "token", nil, &result)

	writer.Close()
	os.Stdout = original
	captured, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatalf("failed to read captured stdout: %v", readErr)
	}

	if requestErr != nil {
		t.Fatalf("expected success, got: %v", requestErr)
	}
	if strings.Contains(string(captured), "[DEBUG]") {
		t.Fatalf("expected no [DEBUG] output on stdout, captured: %q", string(captured))
	}
}

type errorPayload struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestRequestWithErrorParsingParsesJSONBodyBeforeStatusCheck(t *testing.T) {
	attempts := new(int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*attempts++
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":4511,"message":"agent has contract"}}`)
	}))
	t.Cleanup(server.Close)
	client, _ := newRetryTestClient(server.URL, 5)

	var result errorPayload
	err := client.requestWithErrorParsing(context.Background(), "POST", "/test", "token", nil, &result)

	if err == nil || !strings.Contains(err.Error(), "API error (status 400)") {
		t.Fatalf("expected 400 API error, got: %v", err)
	}
	if result.Error.Code != 4511 {
		t.Fatalf("expected error body parsed before status handling, got %+v", result)
	}
	if *attempts != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", *attempts)
	}
}

func TestRequestWithErrorParsingRetriesAndEmitsRateLimitMetrics(t *testing.T) {
	cases := []struct {
		status         int
		expectedReason string
	}{
		{429, "rate_limited_429"},
		{503, "service_unavailable_503"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("status_%d", tc.status), func(t *testing.T) {
			server, attempts := flakyServer(t, tc.status, 2, "")
			client, _ := newRetryTestClient(server.URL, 5)
			recorder := &recordingMetrics{}
			client.SetMetricsCollector(recorder)

			var result namedPayload
			err := client.requestWithErrorParsing(context.Background(), "POST", "/test", "token", nil, &result)

			if err != nil {
				t.Fatalf("expected success after retries, got: %v", err)
			}
			if *attempts != 3 {
				t.Fatalf("expected 3 attempts, got %d", *attempts)
			}
			if result.Data.Name != "ok" {
				t.Fatalf("expected parsed result, got %+v", result)
			}
			if recorder.rateLimitWaits != 3 {
				t.Fatalf("expected 3 rate limit wait metrics, got %d", recorder.rateLimitWaits)
			}
			if len(recorder.retryReasons) != 2 || recorder.retryReasons[0] != tc.expectedReason || recorder.retryReasons[1] != tc.expectedReason {
				t.Fatalf("expected two %s retry metrics, got %v", tc.expectedReason, recorder.retryReasons)
			}
		})
	}
}

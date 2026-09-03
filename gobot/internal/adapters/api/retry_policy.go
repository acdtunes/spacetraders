package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

type retryDecision struct {
	retryable    bool
	retryAfter   time.Duration
	metricReason string
	failure      *retryableError
}

func classifyResponse(statusCode int, header http.Header) retryDecision {
	if statusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(header)
		return retryDecision{
			retryable:    true,
			retryAfter:   retryAfter,
			metricReason: "rate_limited_429",
			failure:      &retryableError{message: "rate limited (429)", retryAfter: retryAfter, statusCode: statusCode},
		}
	}
	if statusCode == http.StatusServiceUnavailable {
		return retryDecision{
			retryable:    true,
			metricReason: "service_unavailable_503",
			failure:      &retryableError{message: "service unavailable (503)", statusCode: statusCode},
		}
	}
	if statusCode >= 500 {
		return retryDecision{
			retryable:    true,
			metricReason: "server_error_5xx",
			failure:      &retryableError{message: fmt.Sprintf("server error (%d)", statusCode), statusCode: statusCode},
		}
	}
	return retryDecision{}
}

func classifyNetworkError(err error) retryDecision {
	return retryDecision{
		retryable:    true,
		metricReason: "network_error",
		failure:      &retryableError{message: fmt.Errorf("network error: %w", err).Error()},
	}
}

func parseRetryAfter(header http.Header) time.Duration {
	value := header.Get("Retry-After")
	if value == "" {
		return 0
	}
	seconds, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

type attemptOutcome struct {
	statusCode int
	header     http.Header
	body       []byte
	networkErr error
}

func (o attemptOutcome) classify() retryDecision {
	if o.networkErr != nil {
		return classifyNetworkError(o.networkErr)
	}
	return classifyResponse(o.statusCode, o.header)
}

func (o attemptOutcome) isSuccess() bool {
	return o.statusCode >= 200 && o.statusCode < 300
}

// apiRequest is one outbound call; path is relative to the client's baseURL.
type apiRequest struct {
	method string
	path   string
	token  string
	body   interface{}

	// serverErrorRetryCap, when non-nil, lowers THIS call's ladder for server/network
	// failures only. Rate limiting keeps the client-wide one: a 429 is the limiter
	// telling us to wait, and shortening it turns backpressure into a failed call.
	serverErrorRetryCap *int
}

// retryCapFor: the call's own cap for a server/network failure, else client-wide.
func (r apiRequest) retryCapFor(decision retryDecision, clientMax int) int {
	if r.serverErrorRetryCap == nil || decision.metricReason == "rate_limited_429" {
		return clientMax
	}
	if *r.serverErrorRetryCap < clientMax {
		return *r.serverErrorRetryCap
	}
	return clientMax
}

func (c *SpaceTradersClient) sendOnce(ctx context.Context, call apiRequest) (attemptOutcome, error) {
	var reqBody io.Reader
	if call.body != nil {
		jsonData, err := json.Marshal(call.body)
		if err != nil {
			return attemptOutcome{}, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonData)
	}

	req, err := http.NewRequestWithContext(ctx, call.method, c.baseURL+call.path, reqBody)
	if err != nil {
		return attemptOutcome{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+call.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return attemptOutcome{networkErr: err}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return attemptOutcome{}, fmt.Errorf("failed to read response: %w", err)
	}

	return attemptOutcome{statusCode: resp.StatusCode, header: resp.Header, body: respBody}, nil
}

func (c *SpaceTradersClient) doWithRetry(ctx context.Context, call apiRequest, onTerminal func(statusCode int, respBody []byte) error) error {
	endpoint := apiEndpointClassifier.classify(call.path)
	overallStart := time.Now()

	var lastErr error
	var finalStatusCode int

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		rateLimitStart := time.Now()
		// Priority-aware token acquisition, armed unconditionally; the ctx tag
		// orders acquisition. endpoint is the human-readable name computed above
		// and is used to classify the call's scheduling priority.
		if err := c.acquireRateToken(ctx, endpoint); err != nil {
			return fmt.Errorf("rate limiter error: %w", err)
		}
		rateLimitWait := time.Since(rateLimitStart)
		c.limiterPressure.Observe(rateLimitWait, time.Now())
		// Throttled to once a second: releases an expired 429 hold, and heals the gauge.
		c.governor.maybeRelease(c.clock.Now(), c.getMetricsCollector())
		if collector := c.getMetricsCollector(); collector != nil {
			collector.RecordRateLimitWait(call.method, endpoint, rateLimitWait.Seconds())
			collector.SetRateLimiterTokens(c.rateLimiter.Tokens())
		}

		outcome, err := c.sendOnce(ctx, call)
		if err != nil {
			return err
		}

		// Telemetry only: a transport failure carries no response, so it is not a server
		// that stopped sending the headers and must not be counted as one.
		if outcome.networkErr == nil {
			c.observeRateLimitHeaders(outcome.header, outcome.statusCode)
		}

		// Record one budget event per attempt, carrying the wait it already paid — this
		// single call site covers both terminal and retry paths, since it runs before
		// classify() branches on the outcome.
		if tracker := c.getBudgetTracker(); tracker != nil {
			hull := extractShipSymbol(call.path)
			purpose := classifyPurpose(call.method, attempt)
			tracker.Record(hull, purpose, sourceFromContext(ctx), outcome.statusCode == http.StatusTooManyRequests, rateLimitWait)
		}

		// Retreat BEFORE the retry sleep, so the backoff is already paid at 2.0 req/s.
		// Classification, Retry-After and the ladder itself are untouched by this.
		if outcome.statusCode == http.StatusTooManyRequests {
			c.governor.trip(c.clock.Now(), endpoint, c.getMetricsCollector())
		}

		decision := outcome.classify()
		if !decision.retryable {
			terminalErr := onTerminal(outcome.statusCode, outcome.body)
			if terminalErr == nil || !outcome.isSuccess() {
				if collector := c.getMetricsCollector(); collector != nil {
					collector.RecordAPIRequest(call.method, endpoint, outcome.statusCode, time.Since(overallStart).Seconds())
				}
			}
			return terminalErr
		}

		lastErr = decision.failure
		if collector := c.getMetricsCollector(); collector != nil {
			collector.RecordAPIRetry(call.method, endpoint, decision.metricReason)
		}
		if attempt >= call.retryCapFor(decision, c.maxRetries) {
			finalStatusCode = outcome.statusCode
			break
		}
		if ctx.Err() != nil {
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		}

		delay := addJitter(c.backoffBase * time.Duration(1<<attempt))
		if decision.retryAfter > 0 {
			delay = decision.retryAfter
		}
		c.clock.Sleep(delay)
	}

	if collector := c.getMetricsCollector(); collector != nil && finalStatusCode > 0 {
		collector.RecordAPIRequest(call.method, endpoint, finalStatusCode, time.Since(overallStart).Seconds())
	}
	if lastErr != nil {
		return fmt.Errorf("max retries exceeded: %w", lastErr)
	}
	return fmt.Errorf("max retries exceeded")
}

// addJitter adds random jitter to a duration to avoid thundering herd
// Returns a duration between 50% and 150% of the original value
func addJitter(d time.Duration) time.Duration {
	if d > maxBackoffDuration {
		d = maxBackoffDuration
	}
	jitter := 0.5 + rand.Float64() // 0.5 to 1.5
	return time.Duration(float64(d) * jitter)
}

// retryableError represents an error that should trigger a retry
type retryableError struct {
	message    string
	retryAfter time.Duration
	// statusCode is the HTTP status that produced it, 0 for a transport failure. It
	// survives the retry ladder so a caller can still tell a server-side failure from a
	// network one after the ladder gave up.
	statusCode int
}

func (e *retryableError) Error() string {
	return e.message
}

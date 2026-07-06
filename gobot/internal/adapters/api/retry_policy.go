package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
			failure:      &retryableError{message: "rate limited (429)", retryAfter: retryAfter},
		}
	}
	if statusCode == http.StatusServiceUnavailable {
		return retryDecision{
			retryable:    true,
			metricReason: "service_unavailable_503",
			failure:      &retryableError{message: "service unavailable (503)"},
		}
	}
	if statusCode >= 500 {
		return retryDecision{
			retryable:    true,
			metricReason: "server_error_5xx",
			failure:      &retryableError{message: fmt.Sprintf("server error (%d)", statusCode)},
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

func (c *SpaceTradersClient) sendOnce(ctx context.Context, method, url, token string, body interface{}) (attemptOutcome, error) {
	var reqBody io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return attemptOutcome{}, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonData)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return attemptOutcome{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

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

func (c *SpaceTradersClient) doWithRetry(ctx context.Context, method, path, token string, body interface{}, onTerminal func(statusCode int, respBody []byte) error) error {
	url := c.baseURL + path
	endpoint := apiEndpointClassifier.classify(path)
	overallStart := time.Now()

	var lastErr error
	var finalStatusCode int

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		rateLimitStart := time.Now()
		if err := c.rateLimiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter error: %w", err)
		}
		if collector := c.getMetricsCollector(); collector != nil {
			collector.RecordRateLimitWait(method, endpoint, time.Since(rateLimitStart).Seconds())
			collector.SetRateLimiterTokens(c.rateLimiter.Tokens())
		}

		outcome, err := c.sendOnce(ctx, method, url, token, body)
		if err != nil {
			return err
		}

		decision := outcome.classify()
		if !decision.retryable {
			terminalErr := onTerminal(outcome.statusCode, outcome.body)
			if terminalErr == nil || !outcome.isSuccess() {
				if collector := c.getMetricsCollector(); collector != nil {
					collector.RecordAPIRequest(method, endpoint, outcome.statusCode, time.Since(overallStart).Seconds())
				}
			}
			return terminalErr
		}

		lastErr = decision.failure
		if collector := c.getMetricsCollector(); collector != nil && attempt > 0 {
			collector.RecordAPIRetry(method, endpoint, decision.metricReason)
		}
		if attempt >= c.maxRetries {
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
		collector.RecordAPIRequest(method, endpoint, finalStatusCode, time.Since(overallStart).Seconds())
	}
	if lastErr != nil {
		return fmt.Errorf("max retries exceeded: %w", lastErr)
	}
	return fmt.Errorf("max retries exceeded")
}

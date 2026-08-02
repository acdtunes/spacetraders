package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// APIMetricsCollector handles all API request metrics
type APIMetricsCollector struct {
	// Request metrics
	apiRequestsTotal     *prometheus.CounterVec
	apiRequestDuration   *prometheus.HistogramVec
	apiRetries           *prometheus.CounterVec
	apiRateLimitWait     *prometheus.HistogramVec
	apiRateLimiterTokens prometheus.Gauge
}

// NewAPIMetricsCollector creates a new API metrics collector
func NewAPIMetricsCollector() *APIMetricsCollector {
	return &APIMetricsCollector{
		// Total API requests by method, endpoint, and status code
		apiRequestsTotal: newCounterVec(
			"api_requests_total",
			"Total number of API requests by method, endpoint, and status code",
			"method",
			"endpoint",
			"status_code",
		),

		// API request duration histogram
		apiRequestDuration: newHistogramVec(
			"api_request_duration_seconds",
			"API request duration distribution",
			[]float64{0.05, 0.1, 0.2, 0.3, 0.5, 1.0, 2.0, 5.0, 10.0},
			"method",
			"endpoint",
		),

		// Retry attempts counter
		apiRetries: newCounterVec(
			"api_retries_total",
			"Total number of API retry attempts",
			"method",
			"endpoint",
			"reason",
		),

		// Rate limit wait time histogram
		apiRateLimitWait: newHistogramVec(
			"api_rate_limit_wait_seconds",
			"Time spent waiting for rate limiter",
			[]float64{0.1, 0.25, 0.5, 0.75, 1.0, 2.0, 5.0},
			"method",
			"endpoint",
		),

		// Rate limiter tokens available gauge
		apiRateLimiterTokens: newGauge(
			"api_rate_limiter_tokens_available",
			"Current available tokens in rate limiter (max 30)",
		),
	}
}

// Register registers all API metrics with the Prometheus registry
func (c *APIMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(
		c.apiRequestsTotal,
		c.apiRequestDuration,
		c.apiRetries,
		c.apiRateLimitWait,
		c.apiRateLimiterTokens,
	)
}

// RecordAPIRequest records an API request completion
func (c *APIMetricsCollector) RecordAPIRequest(
	method string,
	endpoint string,
	statusCode int,
	duration float64,
) {
	if c == nil || c.apiRequestsTotal == nil || c.apiRequestDuration == nil {
		return // Recording is best-effort; never panic the request path.
	}

	statusCodeStr := strconv.Itoa(statusCode)

	// Increment request counter
	c.apiRequestsTotal.WithLabelValues(method, endpoint, statusCodeStr).Inc()

	// Record request duration
	c.apiRequestDuration.WithLabelValues(method, endpoint).Observe(duration)
}

// RecordAPIRetry records an API retry attempt
func (c *APIMetricsCollector) RecordAPIRetry(
	method string,
	endpoint string,
	reason string,
) {
	if c == nil || c.apiRetries == nil {
		return // Recording is best-effort; never panic the request path.
	}

	c.apiRetries.WithLabelValues(method, endpoint, reason).Inc()
}

// RecordRateLimitWait records time spent waiting for rate limiter
func (c *APIMetricsCollector) RecordRateLimitWait(
	method string,
	endpoint string,
	duration float64,
) {
	if c == nil || c.apiRateLimitWait == nil {
		return // Recording is best-effort; never panic the request path.
	}

	c.apiRateLimitWait.WithLabelValues(method, endpoint).Observe(duration)
}

// SetRateLimiterTokens updates the rate limiter tokens available gauge
func (c *APIMetricsCollector) SetRateLimiterTokens(tokens float64) {
	if c == nil || c.apiRateLimiterTokens == nil {
		return // Recording is best-effort; never panic the request path.
	}

	c.apiRateLimiterTokens.Set(tokens)
}

// globalAPICollector is the singleton API metrics collector
// Set by SetGlobalAPICollector() when metrics are enabled
var globalAPICollector *APIMetricsCollector

// SetGlobalAPICollector sets the global API metrics collector
func SetGlobalAPICollector(collector *APIMetricsCollector) {
	globalAPICollector = collector
}

// GetGlobalAPICollector returns the global API metrics collector
// Returns nil if metrics are not enabled
func GetGlobalAPICollector() *APIMetricsCollector {
	return globalAPICollector
}

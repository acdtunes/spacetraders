package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// RateLimitHeaderAbsent is the value RecordRateLimitHeaders callers pass for a
// server rate-limit header that was missing or would not parse. It is a sentinel,
// not a measurement: such a field sets no gauge at all, so a stale reading is
// never mistaken for a fresh one.
const RateLimitHeaderAbsent = -1.0

// APIMetricsCollector handles all API request metrics
type APIMetricsCollector struct {
	// Request metrics
	apiRequestsTotal     *prometheus.CounterVec
	apiRequestDuration   *prometheus.HistogramVec
	apiRetries           *prometheus.CounterVec
	apiRateLimitWait     *prometheus.HistogramVec
	apiRateLimiterTokens prometheus.Gauge

	// What the SERVER says its limit is, straight off the x-ratelimit-* response
	// headers — the client's own limiter is not involved.
	apiRateLimitServerPerSecond *prometheus.GaugeVec
	apiRateLimitServerBurst     *prometheus.GaugeVec
	apiRateLimitServerRemaining *prometheus.GaugeVec
	apiRateLimitServerReset     *prometheus.GaugeVec
	apiRateLimitHeadersObserved *prometheus.CounterVec

	// What OUR limiter is running at, and how often a 429 forced it back down —
	// the pair that says whether a raised target is holding (sp-g7jep).
	apiRateLimiterEffective prometheus.Gauge
	apiRateGovernorTrips    *prometheus.CounterVec
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

		apiRateLimitServerPerSecond: newGaugeVec(
			"api_ratelimit_server_limit_per_second",
			"Sustained request rate the server reports in x-ratelimit-limit-per-second",
			"type",
		),

		apiRateLimitServerBurst: newGaugeVec(
			"api_ratelimit_server_limit_burst",
			"Burst allowance the server reports in x-ratelimit-limit-burst",
			"type",
		),

		apiRateLimitServerRemaining: newGaugeVec(
			"api_ratelimit_server_remaining",
			"Requests the server reports remaining in x-ratelimit-remaining",
			"type",
		),

		apiRateLimitServerReset: newGaugeVec(
			"api_ratelimit_server_reset_seconds",
			"Seconds until the server's rate-limit window resets (x-ratelimit-reset)",
			"type",
		),

		apiRateLimitHeadersObserved: newCounterVec(
			"api_ratelimit_headers_observed_total",
			"Responses by whether they carried any x-ratelimit-* header",
			"present",
		),

		apiRateLimiterEffective: newGauge(
			"api_rate_limiter_effective_req_per_sec",
			"Sustained rate the client's own limiter is currently running at",
		),

		apiRateGovernorTrips: newCounterVec(
			"api_rate_governor_trips_total",
			"Times a 429 forced the rate governor back to the 2.0 req/s floor",
			"endpoint",
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
		c.apiRateLimitServerPerSecond,
		c.apiRateLimitServerBurst,
		c.apiRateLimitServerRemaining,
		c.apiRateLimitServerReset,
		c.apiRateLimitHeadersObserved,
		c.apiRateLimiterEffective,
		c.apiRateGovernorTrips,
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

// RecordRateLimitHeaders records what the server reported about its own rate limit on
// one response. kind is the x-ratelimit-type value; any numeric field may be
// RateLimitHeaderAbsent, which sets no gauge. sawHeaders is the caller's prefix scan of
// the response: presence is a fact about the WIRE, not about whether our five known
// fields parsed, so a garbled or renamed header still counts present="yes" — otherwise a
// server sending nonsense reads exactly like one sending nothing.
func (c *APIMetricsCollector) RecordRateLimitHeaders(
	kind string,
	perSecond float64,
	burst float64,
	remaining float64,
	resetSeconds float64,
	sawHeaders bool,
) {
	if c == nil || c.apiRateLimitHeadersObserved == nil {
		return // Recording is best-effort; never panic the request path.
	}

	if !sawHeaders {
		c.apiRateLimitHeadersObserved.WithLabelValues("no").Inc()
		return
	}
	c.apiRateLimitHeadersObserved.WithLabelValues("yes").Inc()

	if kind == "" {
		kind = "unknown"
	}
	setRateLimitHeaderGauge(c.apiRateLimitServerPerSecond, kind, perSecond)
	setRateLimitHeaderGauge(c.apiRateLimitServerBurst, kind, burst)
	setRateLimitHeaderGauge(c.apiRateLimitServerRemaining, kind, remaining)
	setRateLimitHeaderGauge(c.apiRateLimitServerReset, kind, resetSeconds)
}

// SetRateLimiterTarget records the rate OUR limiter is running at, which is the
// governor's target except while a 429 hold has it back at the floor. Distinct from the
// api_ratelimit_server_* gauges, which report what the SERVER says its limit is.
func (c *APIMetricsCollector) SetRateLimiterTarget(rps float64) {
	if c == nil || c.apiRateLimiterEffective == nil {
		return // Recording is best-effort; never panic the request path.
	}

	c.apiRateLimiterEffective.Set(rps)
}

// RecordRateGovernorTrip counts one 429-driven retreat to the floor.
func (c *APIMetricsCollector) RecordRateGovernorTrip(endpoint string) {
	if c == nil || c.apiRateGovernorTrips == nil {
		return // Recording is best-effort; never panic the request path.
	}

	c.apiRateGovernorTrips.WithLabelValues(endpoint).Inc()
}

func setRateLimitHeaderGauge(gauge *prometheus.GaugeVec, kind string, value float64) {
	if gauge == nil || value == RateLimitHeaderAbsent {
		return
	}
	gauge.WithLabelValues(kind).Set(value)
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

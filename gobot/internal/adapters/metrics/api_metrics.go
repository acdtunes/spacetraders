package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// APIMetricsCollector handles all API request metrics
type APIMetricsCollector struct {
	// Request metrics
	apiRequestsTotal   *prometheus.CounterVec
	apiRequestDuration *prometheus.HistogramVec
	apiRetries         *prometheus.CounterVec
	apiRateLimitWait   *prometheus.HistogramVec
}

// NewAPIMetricsCollector creates a new API metrics collector
func NewAPIMetricsCollector() *APIMetricsCollector {
	return &APIMetricsCollector{
		// Total API requests by method, endpoint, and status code
		apiRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "api_requests_total",
				Help:      "Total number of API requests by method, endpoint, and status code",
			},
			[]string{"method", "endpoint", "status_code"},
		),

		// API request duration histogram
		apiRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "api_request_duration_seconds",
				Help:      "API request duration distribution",
				Buckets:   []float64{0.01, 0.05, 0.1, 0.5, 1.0, 2.0, 5.0, 10.0, 30.0},
			},
			[]string{"method", "endpoint"},
		),

		// Retry attempts counter
		apiRetries: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "api_retries_total",
				Help:      "Total number of API retry attempts",
			},
			[]string{"method", "endpoint", "reason"},
		),

		// Rate limit wait time histogram
		apiRateLimitWait: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "api_rate_limit_wait_seconds",
				Help:      "Time spent waiting for rate limiter",
				Buckets:   []float64{0.001, 0.01, 0.1, 0.5, 1.0, 2.0, 5.0},
			},
			[]string{"method", "endpoint"},
		),
	}
}

// Register registers all API metrics with the Prometheus registry
func (c *APIMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}

	metrics := []prometheus.Collector{
		c.apiRequestsTotal,
		c.apiRequestDuration,
		c.apiRetries,
		c.apiRateLimitWait,
	}

	for _, metric := range metrics {
		if err := Registry.Register(metric); err != nil {
			return err
		}
	}

	return nil
}

// RecordAPIRequest records an API request completion
func (c *APIMetricsCollector) RecordAPIRequest(
	method string,
	endpoint string,
	statusCode int,
	duration float64,
) {
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
	c.apiRetries.WithLabelValues(method, endpoint, reason).Inc()
}

// RecordRateLimitWait records time spent waiting for rate limiter
func (c *APIMetricsCollector) RecordRateLimitWait(
	method string,
	endpoint string,
	duration float64,
) {
	c.apiRateLimitWait.WithLabelValues(method, endpoint).Observe(duration)
}

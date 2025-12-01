package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// CommandMetricsCollector handles all command/query execution metrics
type CommandMetricsCollector struct {
	// Command execution metrics
	commandDuration *prometheus.HistogramVec
	commandsTotal   *prometheus.CounterVec
}

// NewCommandMetricsCollector creates a new command metrics collector
func NewCommandMetricsCollector() *CommandMetricsCollector {
	return &CommandMetricsCollector{
		// Command execution duration histogram
		commandDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "command_duration_seconds",
				Help:      "Command execution duration distribution",
				Buckets:   []float64{0.1, 0.25, 0.5, 1.0, 2.0, 3.0, 5.0, 10.0, 30.0},
			},
			[]string{"command", "status"},
		),

		// Command execution counter
		commandsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "commands_total",
				Help:      "Total number of commands executed by type and status",
			},
			[]string{"command", "status"},
		),
	}
}

// Register registers all command metrics with the Prometheus registry
func (c *CommandMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}

	metrics := []prometheus.Collector{
		c.commandDuration,
		c.commandsTotal,
	}

	for _, metric := range metrics {
		if err := Registry.Register(metric); err != nil {
			return err
		}
	}

	return nil
}

// RecordCommandExecution records command execution metrics
func (c *CommandMetricsCollector) RecordCommandExecution(
	commandName string,
	duration float64,
	success bool,
) {
	status := "success"
	if !success {
		status = "error"
	}

	// Record duration
	c.commandDuration.WithLabelValues(commandName, status).Observe(duration)

	// Increment counter
	c.commandsTotal.WithLabelValues(commandName, status).Inc()
}

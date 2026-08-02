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
		commandDuration: newHistogramVec(
			"command_duration_seconds",
			"Command execution duration distribution",
			[]float64{0.1, 0.25, 0.5, 1.0, 2.0, 3.0, 5.0, 10.0, 30.0},
			"command",
			"status",
		),

		// Command execution counter
		commandsTotal: newCounterVec(
			"commands_total",
			"Total number of commands executed by type and status",
			"command",
			"status",
		),
	}
}

// Register registers all command metrics with the Prometheus registry
func (c *CommandMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(
		c.commandDuration,
		c.commandsTotal,
	)
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

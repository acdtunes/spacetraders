package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// DaemonBuildInfoCollector exposes the running daemon's identity so a scrape
// reveals WHICH build/instance answered — making a stale second daemon
// (sp-wrh84) detectable:
//
//   - build_info{commit,started_at}: a GAUGE pinned to 1, labelled with the
//     running commit and process start time.
//   - process_start_time_seconds: the process start time as a unix timestamp, so
//     an alert can spot an instance that started before the last deploy.
type DaemonBuildInfoCollector struct {
	buildInfo *prometheus.GaugeVec
	startTime prometheus.Gauge
}

// NewDaemonBuildInfoCollector creates the daemon build-info collector.
func NewDaemonBuildInfoCollector() *DaemonBuildInfoCollector {
	return &DaemonBuildInfoCollector{
		buildInfo: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "build_info",
				Help:      "Running daemon build identity (always 1), labelled by commit and start time (sp-wrh84)",
			},
			[]string{"commit", "started_at"},
		),
		startTime: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "process_start_time_seconds",
				Help:      "Unix start time of the running daemon process, so a scrape reveals a stale instance (sp-wrh84)",
			},
		),
	}
}

// Register registers the build-info metrics with the Prometheus registry. A nil
// Registry (metrics disabled) is a no-op, matching the sibling collectors.
func (c *DaemonBuildInfoCollector) Register() error {
	if Registry == nil {
		return nil
	}
	if err := Registry.Register(c.buildInfo); err != nil {
		return err
	}
	return Registry.Register(c.startTime)
}

// Record pins the build-info gauge to 1 for the given commit/start time and sets
// the process start time (unix seconds). Called once at daemon startup.
func (c *DaemonBuildInfoCollector) Record(commit string, startedAt time.Time) {
	if c == nil {
		return
	}
	c.buildInfo.WithLabelValues(commit, startedAt.UTC().Format(time.RFC3339)).Set(1)
	c.startTime.Set(float64(startedAt.Unix()))
}

package grpc

import (
	"net"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// A taken metrics port means another daemon is already live on this host. The
// daemon must FAIL CLOSED rather than run headless beside a stale writer
// (sp-wrh84 root cause B): startMetricsServerOrFail must return a (fatal) error.
func TestStartMetricsServerOrFailReturnsErrorWhenPortTaken(t *testing.T) {
	metrics.InitRegistry()

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to occupy a port: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	s := &DaemonServer{
		metricsConfig: &config.MetricsConfig{Enabled: true, Host: "127.0.0.1", Port: port, Path: "/metrics"},
		flowRegistry:  flowfeed.New(),
	}

	if err := s.startMetricsServerOrFail(); err == nil {
		t.Fatal("expected a fatal error when the metrics port is already bound, got nil")
	}
}

// When the port is free the daemon binds and proceeds (returns nil).
func TestStartMetricsServerOrFailSucceedsWhenPortFree(t *testing.T) {
	metrics.InitRegistry()

	s := &DaemonServer{
		metricsConfig: &config.MetricsConfig{Enabled: true, Host: "127.0.0.1", Port: 0, Path: "/metrics"},
		flowRegistry:  flowfeed.New(),
	}
	defer s.stopMetricsServer()

	if err := s.startMetricsServerOrFail(); err != nil {
		t.Fatalf("expected success binding a free port, got %v", err)
	}
}

// Metrics disabled is a no-op success (the daemon runs without a metrics server).
func TestStartMetricsServerOrFailNoopWhenDisabled(t *testing.T) {
	s := &DaemonServer{metricsConfig: &config.MetricsConfig{Enabled: false}}
	if err := s.startMetricsServerOrFail(); err != nil {
		t.Fatalf("expected nil when metrics disabled, got %v", err)
	}
}

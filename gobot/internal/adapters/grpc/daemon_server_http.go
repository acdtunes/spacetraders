package grpc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// startMetricsServer starts the HTTP server for Prometheus metrics
// registerFlowsRoute mounts the read-only GET /api/flows handler on the metrics
// mux, beside /metrics (same localhost trust boundary; no auth change).
func registerFlowsRoute(mux *http.ServeMux, reg *flowfeed.Registry) {
	mux.Handle("/api/flows", flowfeed.NewFlowsHandler(reg))
}

// newFlowRegistry builds the daemon's flow registry with its live source wired.
//
// The live source is what makes the feed honest across a restart:
// published snapshots die with the process and executors only re-publish at their
// next plan adoption or leg arrival, so without it every hull is invisible for as
// long as it takes to adopt a plan — tens of minutes while repositioning or
// replanning, which is how the feed came to report 5 of 13 running tours. This
// wiring is load-bearing, not decoration: a registry built without it
// under-reports the fleet. It lives in its own function so a test can build the
// registry the daemon actually builds.
func newFlowRegistry(s *DaemonServer) *flowfeed.Registry {
	reg := flowfeed.New()
	reg.SetLiveSource(s.liveTradingRuns)
	return reg
}

// liveTradingRuns enumerates the trading containers that are RUNNING right now,
// off the SAME in-memory runner map ListContainers reads — so the flow feed can
// never disagree with `spacetraders container list`, which is the source that was
// right when the feed was wrong (feed 5, container list 13).
//
// The program is read off the command's concrete type rather than launch
// metadata: the command is what the runner actually executes, and restart
// recovery rebuilds it from persisted config, so this survives a restart with no
// separate re-registration step (RULINGS #2). Non-trading containers publish no
// flows and are skipped.
func (s *DaemonServer) liveTradingRuns() []flowfeed.LiveRun {
	s.containersMu.RLock()
	defer s.containersMu.RUnlock()

	runs := make([]flowfeed.LiveRun, 0, len(s.containers))
	for id, runner := range s.containers {
		if runner == nil {
			continue
		}
		cont := runner.Container()
		if cont == nil || !cont.IsRunning() {
			continue
		}
		switch cmd := runner.Command().(type) {
		case *tradingCmd.RunTourCoordinatorCommand:
			runs = append(runs, flowfeed.LiveRun{
				ContainerID: id, Program: flowfeed.ProgramTour, Ship: cmd.ShipSymbol, Closed: cmd.ClosedTours,
			})
		case *tradingCmd.RunTradeRouteCoordinatorCommand:
			runs = append(runs, flowfeed.LiveRun{
				ContainerID: id, Program: flowfeed.ProgramTradeRoute, Ship: cmd.ShipSymbol,
			})
		case *tradingCmd.RunArbCoordinatorCommand:
			runs = append(runs, flowfeed.LiveRun{
				ContainerID: id, Program: flowfeed.ProgramArb, Ship: cmd.ShipSymbol,
			})
		}
	}
	return runs
}

// startMetricsServerOrFail starts the Prometheus metrics server when enabled and
// returns a FATAL error if the bind fails. A taken metrics port means another
// daemon instance is already live on this host: the daemon must abort rather than
// run headless beside a stale writer (sp-wrh84 root cause B). Disabled metrics is
// a no-op success.
func (s *DaemonServer) startMetricsServerOrFail() error {
	if s.metricsConfig == nil || !s.metricsConfig.Enabled {
		return nil
	}
	if err := s.startMetricsServer(); err != nil {
		return fmt.Errorf("metrics server failed to start (another daemon may already hold %s:%d): %w",
			s.metricsConfig.Host, s.metricsConfig.Port, err)
	}
	fmt.Printf("Metrics server listening on %s:%d%s\n",
		s.metricsConfig.Host, s.metricsConfig.Port, s.metricsConfig.Path)
	return nil
}

func (s *DaemonServer) startMetricsServer() error {
	if s.metricsConfig == nil || !s.metricsConfig.Enabled {
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle(s.metricsConfig.Path, promhttp.HandlerFor(
		metrics.GetRegistry(),
		promhttp.HandlerOpts{
			EnableOpenMetrics: true,
		},
	))
	registerFlowsRoute(mux, s.flowRegistry)

	// Create listener FIRST to verify port is available before returning success
	addr := fmt.Sprintf("%s:%d", s.metricsConfig.Host, s.metricsConfig.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to bind metrics server to %s: %w", addr, err)
	}

	s.metricsServer = &http.Server{
		Handler: mux,
	}

	// Start server in goroutine using the already-bound listener
	go func() {
		if err := s.metricsServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Metrics server error: %v\n", err)
		}
	}()

	return nil
}

// stopMetricsServer gracefully stops the HTTP metrics server
func (s *DaemonServer) stopMetricsServer() {
	if s.metricsServer == nil {
		return
	}

	// Stop metrics collectors first
	if s.containerMetricsCollector != nil {
		s.containerMetricsCollector.Stop()
	}
	if s.financialMetricsCollector != nil {
		s.financialMetricsCollector.Stop()
	}
	if s.marketMetricsCollector != nil {
		s.marketMetricsCollector.Stop()
	}

	// Shutdown HTTP server with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.metricsServer.Shutdown(ctx); err != nil {
		fmt.Printf("Error shutting down metrics server: %v\n", err)
	}
}

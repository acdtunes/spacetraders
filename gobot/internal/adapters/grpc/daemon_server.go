package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"google.golang.org/grpc"
)

// CommandFactory creates a command instance from configuration
type CommandFactory func(config map[string]interface{}, playerID int) (interface{}, error)

// MetricsCollector defines the interface for metrics collection
type MetricsCollector interface {
	Start(ctx context.Context)
	Stop()
	RecordContainerCompletion(containerInfo metrics.ContainerInfo)
	RecordContainerRestart(containerInfo metrics.ContainerInfo)
	RecordContainerIteration(containerInfo metrics.ContainerInfo)
}

// DaemonServer implements the gRPC daemon service
// Handles CLI requests and orchestrates background container operations
type DaemonServer struct {
	mediator         common.Mediator
	listener         net.Listener
	db               *gorm.DB // Database for creating repositories on demand
	logRepo          persistence.ContainerLogRepository
	containerRepo    *persistence.ContainerRepositoryGORM
	waypointRepo     *persistence.GormWaypointRepository
	shipRepo         navigation.ShipRepository
	routingClient    routing.RoutingClient
	goodsFactoryRepo *persistence.GormGoodsFactoryRepository
	clock            shared.Clock

	// Container orchestration
	containers   map[string]*ContainerRunner
	containersMu sync.RWMutex

	// Command factory registry - maps command types to their factory functions
	commandFactories map[string]CommandFactory

	// Pending worker commands cache - stores commands with channels before start
	pendingWorkerCommands   map[string]interface{}
	pendingWorkerCommandsMu sync.RWMutex

	// Metrics
	metricsServer                  *http.Server
	metricsConfig                  *config.MetricsConfig
	containerMetricsCollector      MetricsCollector
	financialMetricsCollector      *metrics.FinancialMetricsCollector
	commandMetricsCollector        *metrics.CommandMetricsCollector
	marketMetricsCollector         *metrics.MarketMetricsCollector
	manufacturingMetricsCollector  *metrics.ManufacturingMetricsCollector

	// Shutdown coordination
	shutdownChan chan os.Signal
	done         chan struct{}
}

// NewDaemonServer creates a new daemon server instance
func NewDaemonServer(
	mediator common.Mediator,
	db *gorm.DB,
	logRepo persistence.ContainerLogRepository,
	containerRepo *persistence.ContainerRepositoryGORM,
	waypointRepo *persistence.GormWaypointRepository,
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	routingClient routing.RoutingClient,
	goodsFactoryRepo *persistence.GormGoodsFactoryRepository,
	socketPath string,
	metricsConfig *config.MetricsConfig,
) (*DaemonServer, error) {
	// Remove existing socket file if present
	if err := os.RemoveAll(socketPath); err != nil {
		return nil, fmt.Errorf("failed to remove existing socket: %w", err)
	}

	// Create Unix domain socket listener
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create unix socket listener: %w", err)
	}

	// Set socket permissions (owner only)
	if err := os.Chmod(socketPath, 0600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("failed to set socket permissions: %w", err)
	}

	server := &DaemonServer{
		mediator:              mediator,
		db:                    db,
		logRepo:               logRepo,
		containerRepo:         containerRepo,
		waypointRepo:          waypointRepo,
		shipRepo:              shipRepo,
		routingClient:         routingClient,
		goodsFactoryRepo:      goodsFactoryRepo,
		clock:                 shared.NewRealClock(),
		listener:              listener,
		containers:            make(map[string]*ContainerRunner),
		commandFactories:      make(map[string]CommandFactory),
		pendingWorkerCommands: make(map[string]interface{}),
		metricsConfig:         metricsConfig,
		shutdownChan:          make(chan os.Signal, 1),
		done:                  make(chan struct{}),
	}

	// Initialize metrics collector if enabled
	if metricsConfig != nil && metricsConfig.Enabled {
		// Initialize the Prometheus registry
		metrics.InitRegistry()

		// Create container info getter function
		getContainers := func() map[string]metrics.ContainerInfo {
			server.containersMu.RLock()
			defer server.containersMu.RUnlock()

			containerInfoMap := make(map[string]metrics.ContainerInfo)
			for id, runner := range server.containers {
				containerInfoMap[id] = runner.Container()
			}
			return containerInfoMap
		}

		// Create container metrics collector
		collector := metrics.NewContainerMetricsCollector(getContainers, shipRepo)
		if err := collector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register container metrics collector: %w", err)
		}
		server.containerMetricsCollector = collector

		// Set global collector for metrics recording
		metrics.SetGlobalCollector(collector)

		// Create navigation metrics collector
		navCollector := metrics.NewNavigationMetricsCollector()
		if err := navCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register navigation metrics collector: %w", err)
		}

		// Set global navigation collector for metrics recording
		metrics.SetGlobalNavigationCollector(navCollector)

		// Create financial metrics collector
		finCollector := metrics.NewFinancialMetricsCollector(mediator, playerRepo, getContainers)
		if err := finCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register financial metrics collector: %w", err)
		}

		// Set global financial collector for metrics recording
		metrics.SetGlobalFinancialCollector(finCollector)

		// Store reference for lifecycle management
		server.financialMetricsCollector = finCollector

		// Create command metrics collector
		cmdCollector := metrics.NewCommandMetricsCollector()
		if err := cmdCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register command metrics collector: %w", err)
		}
		server.commandMetricsCollector = cmdCollector

		// Register Prometheus middleware with mediator
		// This wraps all command/query executions to record metrics
		mediator.RegisterMiddleware(metrics.PrometheusMiddleware(cmdCollector))

		// Create API metrics collector
		apiCollector := metrics.NewAPIMetricsCollector()
		if err := apiCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register API metrics collector: %w", err)
		}

		// Set global API collector for API client to use
		metrics.SetGlobalAPICollector(apiCollector)

		// Create market metrics collector
		marketCollector := metrics.NewMarketMetricsCollector(db)
		if err := marketCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register market metrics collector: %w", err)
		}

		// Set global market collector for MarketScanner to use
		metrics.SetGlobalMarketCollector(marketCollector)

		// Store reference for lifecycle management
		server.marketMetricsCollector = marketCollector

		// Create manufacturing metrics collector
		mfgCollector := metrics.NewManufacturingMetricsCollector(db)
		if err := mfgCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register manufacturing metrics collector: %w", err)
		}

		// Set global manufacturing collector
		metrics.SetGlobalManufacturingCollector(mfgCollector)

		// Store reference for lifecycle management
		server.manufacturingMetricsCollector = mfgCollector
	}

	// Register command factories for recovery
	server.registerCommandFactories()

	// Setup signal handling
	signal.Notify(server.shutdownChan, os.Interrupt, syscall.SIGTERM)

	return server, nil
}

// Start begins serving gRPC requests
func (s *DaemonServer) Start() error {
	fmt.Printf("Daemon server listening on unix socket: %s\n", s.listener.Addr().String())

	// Release all zombie assignments from previous daemon runs
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if s.shipRepo != nil {
		count, err := s.shipRepo.ReleaseAllActive(ctx, "daemon_restart")
		if err != nil {
			fmt.Printf("Warning: Failed to release zombie assignments: %v\n", err)
		} else if count > 0 {
			fmt.Printf("Released %d zombie ship assignment(s) on daemon startup\n", count)
		}
	}

	// Start metrics server if enabled
	if s.metricsConfig != nil && s.metricsConfig.Enabled {
		if err := s.startMetricsServer(); err != nil {
			fmt.Printf("Warning: Failed to start metrics server: %v\n", err)
		} else {
			fmt.Printf("Metrics server listening on %s:%d%s\n",
				s.metricsConfig.Host, s.metricsConfig.Port, s.metricsConfig.Path)
		}

		// Start container metrics collector
		if s.containerMetricsCollector != nil {
			s.containerMetricsCollector.Start(context.Background())
		}

		// Start financial metrics collector
		if s.financialMetricsCollector != nil {
			s.financialMetricsCollector.Start(context.Background())
		}

		// Start market metrics collector
		if s.marketMetricsCollector != nil {
			s.marketMetricsCollector.Start(context.Background())
		}

		// Start manufacturing metrics collector
		if s.manufacturingMetricsCollector != nil {
			s.manufacturingMetricsCollector.Start(context.Background())
		}
	}

	// Recover RUNNING containers from previous daemon instance
	// This runs in the background to avoid blocking daemon startup
	go func() {
		recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer recoveryCancel()

		if err := s.RecoverRunningContainers(recoveryCtx); err != nil {
			fmt.Printf("Warning: Container recovery failed: %v\n", err)
		}
	}()

	// Start shutdown handler
	go s.handleShutdown()

	// Create gRPC server
	grpcServer := grpc.NewServer()

	// Create and register service implementation
	serviceImpl := newDaemonServiceImpl(s)
	pb.RegisterDaemonServiceServer(grpcServer, serviceImpl)

	// Start serving in a goroutine
	errChan := make(chan error, 1)
	go func() {
		if err := grpcServer.Serve(s.listener); err != nil {
			errChan <- fmt.Errorf("gRPC server error: %w", err)
		}
	}()

	// Wait for shutdown signal or error
	select {
	case err := <-errChan:
		return err
	case <-s.done:
		// Graceful shutdown
		fmt.Println("Initiating graceful shutdown of gRPC server...")
		grpcServer.GracefulStop()
		return nil
	}
}

// startMetricsServer starts the HTTP server for Prometheus metrics
func (s *DaemonServer) startMetricsServer() error {
	if s.metricsConfig == nil || !s.metricsConfig.Enabled {
		return nil
	}

	// Create HTTP mux for metrics endpoint
	mux := http.NewServeMux()
	mux.Handle(s.metricsConfig.Path, promhttp.HandlerFor(
		metrics.GetRegistry(),
		promhttp.HandlerOpts{
			EnableOpenMetrics: true,
		},
	))

	// Create HTTP server
	addr := fmt.Sprintf("%s:%d", s.metricsConfig.Host, s.metricsConfig.Port)
	s.metricsServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	// Start server in goroutine
	go func() {
		if err := s.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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

// handleShutdown manages graceful shutdown
// GracefulShutdownTimeout is the maximum time to wait for containers to finish
const GracefulShutdownTimeout = 30 * time.Second

func (s *DaemonServer) handleShutdown() {
	<-s.shutdownChan
	fmt.Println("\nShutdown signal received, initiating graceful shutdown...")

	// BUG FIX #5: Graceful shutdown with timeout
	// Give containers time to complete their current operation before force-interrupting
	s.gracefulShutdownWithTimeout(GracefulShutdownTimeout)

	// Stop metrics server and collector
	s.stopMetricsServer()

	// Close listener
	if s.listener != nil {
		s.listener.Close()
	}

	close(s.done)
}

// gracefulShutdownWithTimeout waits for containers to complete or times out
// BUG FIX #5: This prevents context cancellation cascades that corrupt state
func (s *DaemonServer) gracefulShutdownWithTimeout(timeout time.Duration) {
	s.containersMu.RLock()
	containerCount := len(s.containers)
	s.containersMu.RUnlock()

	if containerCount == 0 {
		fmt.Println("No running containers to stop")
		return
	}

	fmt.Printf("Waiting up to %s for %d container(s) to complete current operations...\n",
		timeout, containerCount)

	// Create a done channel to track when containers finish
	allDone := make(chan struct{})

	go func() {
		// Wait for all containers to finish their done channels
		s.containersMu.RLock()
		runners := make([]*ContainerRunner, 0, len(s.containers))
		for _, runner := range s.containers {
			runners = append(runners, runner)
		}
		s.containersMu.RUnlock()

		// Signal each container to stop (sets stopping flag, doesn't cancel context yet)
		for _, runner := range runners {
			// Try graceful stop first - this sets the stopping flag
			runner.mu.Lock()
			_ = runner.containerEntity.Stop()
			runner.mu.Unlock()
		}

		// Wait for each container's done channel
		for _, runner := range runners {
			select {
			case <-runner.done:
				// Container finished gracefully
			case <-time.After(timeout):
				// This container took too long - will be force-interrupted
			}
		}
		close(allDone)
	}()

	// Wait for graceful completion or timeout
	select {
	case <-allDone:
		fmt.Println("All containers completed gracefully")
	case <-time.After(timeout):
		fmt.Printf("Graceful shutdown timeout (%s) exceeded, force-interrupting remaining containers...\n", timeout)
		// Force-interrupt any remaining containers
		s.interruptAllContainers()
	}
}

// RecoverRunningContainers recovers containers that were RUNNING or INTERRUPTED when daemon stopped
// INTERRUPTED = graceful shutdown (daemon called interruptAllContainers)
// RUNNING = ungraceful shutdown (kill -9, crash) - backwards compatibility
func (s *DaemonServer) RecoverRunningContainers(ctx context.Context) error {
	// Query database for INTERRUPTED containers (graceful shutdown)
	interruptedContainers, err := s.containerRepo.ListByStatus(ctx, container.ContainerStatusInterrupted, nil)
	if err != nil {
		return fmt.Errorf("failed to list INTERRUPTED containers: %w", err)
	}

	// Query database for RUNNING containers (ungraceful shutdown - backwards compatibility)
	runningContainers, err := s.containerRepo.ListByStatus(ctx, container.ContainerStatusRunning, nil)
	if err != nil {
		return fmt.Errorf("failed to list RUNNING containers: %w", err)
	}

	// Combine both lists
	allContainers := append(interruptedContainers, runningContainers...)

	if len(allContainers) == 0 {
		fmt.Println("No containers to recover")
		return nil
	}

	fmt.Printf("Recovering %d container(s) from previous daemon instance (%d INTERRUPTED, %d RUNNING)...\n",
		len(allContainers), len(interruptedContainers), len(runningContainers))

	recoveredCount := 0
	failedCount := 0

	for _, containerModel := range allContainers {
		// Parse container config from JSON
		var config map[string]interface{}
		if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
			fmt.Printf("Container %s: Failed to parse config JSON, marking as FAILED: %v\n", containerModel.ID, err)
			s.markContainerFailed(ctx, containerModel, "invalid_config", fmt.Sprintf("JSON parse error: %v", err))
			failedCount++
			continue
		}

		// Skip worker containers (those with parent container)
		// Workers are managed by their parent coordinator and should not be recovered independently
		// Check: 1) coordinator_id in config 2) ParentContainerID field 3) known worker command types
		// IMPORTANT: Mark as interrupted but DON'T release ship assignments - the coordinator will handle them
		// Releasing assignments here breaks SELL tasks that have cargo on the ship
		if coordinatorID, hasCoordinator := config["coordinator_id"].(string); hasCoordinator && coordinatorID != "" {
			fmt.Printf("Container %s: Skipping recovery (worker container managed by coordinator %s)\n", containerModel.ID, coordinatorID)
			s.markWorkerInterrupted(ctx, containerModel, coordinatorID)
			failedCount++
			continue
		}
		if containerModel.ParentContainerID != nil && *containerModel.ParentContainerID != "" {
			fmt.Printf("Container %s: Skipping recovery (worker container managed by parent %s)\n", containerModel.ID, *containerModel.ParentContainerID)
			s.markWorkerInterrupted(ctx, containerModel, *containerModel.ParentContainerID)
			failedCount++
			continue
		}
		// Skip known worker container types that should be managed by their parent coordinator
		// These containers will be re-spawned by the coordinator after it recovers
		workerCommandTypes := map[string]bool{
			"manufacturing_task_worker": true,
			"siphon_worker":             true,
			"gas_transport_worker":      true,
			"storage_ship":              true,
		}
		if workerCommandTypes[containerModel.CommandType] {
			fmt.Printf("Container %s: Skipping recovery (worker container type '%s' managed by coordinator)\n", containerModel.ID, containerModel.CommandType)
			s.markWorkerInterrupted(ctx, containerModel, "")
			failedCount++
			continue
		}

		// Recover using generic recovery with command factory
		if err := s.recoverContainer(ctx, containerModel, config); err != nil {
			fmt.Printf("Container %s: Recovery failed: %v\n", containerModel.ID, err)
			s.markContainerFailed(ctx, containerModel, "recovery_failed", err.Error())
			failedCount++
		} else {
			recoveredCount++
		}
	}

	fmt.Printf("Container recovery complete: %d recovered, %d failed\n", recoveredCount, failedCount)
	return nil
}

// recoverContainer is the generic container recovery function
// Uses the command factory registry to recreate any container type
// Adding new container types only requires registering a new factory - NO changes needed here!
func (s *DaemonServer) recoverContainer(ctx context.Context, containerModel *persistence.ContainerModel, config map[string]interface{}) error {
	// Look up command factory
	factory, exists := s.commandFactories[containerModel.CommandType]
	if !exists {
		return fmt.Errorf("unknown command type '%s'", containerModel.CommandType)
	}

	// Use factory to create command from config
	cmd, err := factory(config, containerModel.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to create command: %w", err)
	}

	// Extract ship symbol for assignment (if present)
	shipSymbol, hasShip := config["ship_symbol"].(string)
	if hasShip {
		// Re-assign ship using Ship aggregate pattern
		playerID := shared.MustNewPlayerID(containerModel.PlayerID)
		ship, err := s.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
		if err != nil {
			return fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
		}
		if err := ship.AssignToContainer(containerModel.ID, s.clock); err != nil {
			return fmt.Errorf("failed to reassign ship %s: %w", shipSymbol, err)
		}
		if err := s.shipRepo.Save(ctx, ship); err != nil {
			return fmt.Errorf("failed to persist ship %s reassignment: %w", shipSymbol, err)
		}
	}

	// Extract iterations from config
	iterations := 1 // Default
	if iter, ok := config["iterations"].(float64); ok {
		iterations = int(iter)
	}

	// Recreate container entity
	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		iterations,
		containerModel.ParentContainerID, // Restore parent-child relationship
		config,
		nil, // Use default RealClock for production
	)

	// Restore restart count from database
	for i := 0; i < containerModel.RestartCount; i++ {
		containerEntity.IncrementRestartCount()
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerModel.ID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Recovered container %s failed: %v\n", containerModel.ID, err)
		}
	}()

	shipInfo := ""
	if hasShip {
		shipInfo = fmt.Sprintf(" for ship %s", shipSymbol)
	}
	fmt.Printf("Recovered container %s (%s%s)\n", containerModel.ID, containerModel.CommandType, shipInfo)
	return nil
}

// markContainerFailed marks a container as FAILED in the database
func (s *DaemonServer) markContainerFailed(ctx context.Context, containerModel *persistence.ContainerModel, reason string, details string) {
	exitCode := 1
	now := time.Now()

	if err := s.containerRepo.UpdateStatus(
		ctx,
		containerModel.ID,
		containerModel.PlayerID,
		container.ContainerStatusFailed,
		&now,      // stoppedAt
		&exitCode, // exitCode
		fmt.Sprintf("%s: %s", reason, details),
	); err != nil {
		fmt.Printf("Warning: Failed to mark container %s as FAILED: %v\n", containerModel.ID, err)
	}

	// Release ship assignments for this failed container
	// This prevents orphaned assignments when containers fail during recovery
	playerID := shared.MustNewPlayerID(containerModel.PlayerID)
	assignedShips, err := s.shipRepo.FindByContainer(ctx, containerModel.ID, playerID)
	if err != nil {
		fmt.Printf("Warning: Failed to find ships for container %s: %v\n", containerModel.ID, err)
	} else {
		for _, ship := range assignedShips {
			ship.ForceRelease(reason, s.clock)
			if err := s.shipRepo.Save(ctx, ship); err != nil {
				fmt.Printf("Warning: Failed to release ship %s for container %s: %v\n", ship.ShipSymbol(), containerModel.ID, err)
			}
		}
	}
}

// markWorkerInterrupted marks a worker container as interrupted during daemon restart.
// Unlike markContainerFailed, this does NOT release ship assignments.
// The coordinator's recoverState() will handle the ship assignments when it resets tasks.
// This is critical for SELL tasks where the ship still has cargo that needs to be sold.
func (s *DaemonServer) markWorkerInterrupted(ctx context.Context, containerModel *persistence.ContainerModel, coordinatorID string) {
	exitCode := 1
	now := time.Now()

	if err := s.containerRepo.UpdateStatus(
		ctx,
		containerModel.ID,
		containerModel.PlayerID,
		container.ContainerStatusFailed,
		&now,      // stoppedAt
		&exitCode, // exitCode
		fmt.Sprintf("worker_interrupted: Worker interrupted by daemon restart (coordinator: %s). Ship assignments preserved for task recovery.", coordinatorID),
	); err != nil {
		fmt.Printf("Warning: Failed to mark worker %s as interrupted: %v\n", containerModel.ID, err)
	}
	// NOTE: Intentionally NOT releasing ship assignments here.
	// The coordinator will handle this when it resets the task from EXECUTING to READY.
}

// containerModelToShipAssignment converts a ShipModel to domain entity
// This is a helper for the recovery process
func containerModelToShipAssignment(model *persistence.ShipModel) *container.ShipAssignment {
	// Handle NULL container_id
	containerID := ""
	if model.ContainerID != nil {
		containerID = *model.ContainerID
	}

	return container.NewShipAssignment(
		model.ShipSymbol,
		model.PlayerID,
		containerID,
		nil, // Clock not needed
	)
}

// ListContainers returns all registered containers
func (s *DaemonServer) ListContainers(playerID *int, status *string) []*container.Container {
	s.containersMu.RLock()
	defer s.containersMu.RUnlock()

	containers := make([]*container.Container, 0, len(s.containers))

	// Parse comma-separated status filter into map for O(1) lookup
	var allowedStatuses map[string]bool
	if status != nil && *status != "" {
		allowedStatuses = make(map[string]bool)
		statuses := strings.Split(*status, ",")
		for _, s := range statuses {
			trimmed := strings.TrimSpace(s)
			if trimmed != "" {
				allowedStatuses[trimmed] = true
			}
		}
	}

	for _, runner := range s.containers {
		cont := runner.Container()

		// Apply filters
		if playerID != nil && cont.PlayerID() != *playerID {
			continue
		}

		// Filter by status (if filter provided)
		if allowedStatuses != nil {
			if !allowedStatuses[string(cont.Status())] {
				continue
			}
		}

		containers = append(containers, cont)
	}

	return containers
}

// GetContainer retrieves a specific container
func (s *DaemonServer) GetContainer(containerID string) (*container.Container, error) {
	s.containersMu.RLock()
	defer s.containersMu.RUnlock()

	runner, exists := s.containers[containerID]
	if !exists {
		return nil, fmt.Errorf("container not found: %s", containerID)
	}

	return runner.Container(), nil
}

// StopContainer stops a running container and all its child containers
func (s *DaemonServer) StopContainer(containerID string) error {
	s.containersMu.RLock()
	runner, exists := s.containers[containerID]
	s.containersMu.RUnlock()

	if !exists {
		return fmt.Errorf("container not found: %s", containerID)
	}

	// Get playerID from the container
	playerID := runner.containerEntity.PlayerID()

	// Find and stop all child containers first (depth-first)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	childContainers, err := s.containerRepo.FindChildContainers(ctx, containerID, playerID)
	if err != nil {
		fmt.Printf("Warning: failed to find child containers for %s: %v\n", containerID, err)
	} else {
		for _, child := range childContainers {
			// Only stop RUNNING or PENDING children
			if child.Status != "RUNNING" && child.Status != "PENDING" {
				continue
			}

			// Try to stop in-memory runner if exists
			s.containersMu.RLock()
			childRunner, childExists := s.containers[child.ID]
			s.containersMu.RUnlock()

			if childExists {
				fmt.Printf("Stopping child container: %s\n", child.ID)
				if err := childRunner.Stop(); err != nil {
					fmt.Printf("Warning: failed to stop child container %s: %v\n", child.ID, err)
				}
			} else {
				// Child not in memory (orphaned) - update DB directly
				fmt.Printf("Marking orphaned child container as stopped: %s\n", child.ID)
				now := time.Now()
				exitCode := 0
				if err := s.containerRepo.UpdateStatus(ctx, child.ID, playerID, container.ContainerStatusStopped, &now, &exitCode, "parent stopped"); err != nil {
					fmt.Printf("Warning: failed to update orphaned child container %s: %v\n", child.ID, err)
				}
			}
		}
	}

	// Now stop the parent container
	return runner.Stop()
}

// DeleteContainer deletes a container from the database
// This is for cleanup of PENDING containers that were never started
func (s *DaemonServer) DeleteContainer(ctx context.Context, containerID string, playerID int) error {
	// Remove from in-memory map if exists (shouldn't be there for PENDING containers)
	s.containersMu.Lock()
	delete(s.containers, containerID)
	s.containersMu.Unlock()

	// Delete from database
	if err := s.containerRepo.Remove(ctx, containerID, playerID); err != nil {
		return fmt.Errorf("failed to delete container from database: %w", err)
	}

	return nil
}

// Container registration

func (s *DaemonServer) registerContainer(containerID string, runner *ContainerRunner) {
	s.containersMu.Lock()
	defer s.containersMu.Unlock()
	s.containers[containerID] = runner
}

// interruptAllContainers interrupts all container goroutines and marks them as INTERRUPTED
// Allows containers to be recovered on daemon restart
func (s *DaemonServer) interruptAllContainers() {
	s.containersMu.Lock()
	runners := make([]*ContainerRunner, 0, len(s.containers))
	for _, runner := range s.containers {
		runners = append(runners, runner)
	}
	s.containersMu.Unlock()

	fmt.Printf("Interrupting %d running container(s) (will be recovered on restart)...\n", len(runners))

	// Cancel all container contexts to stop goroutines
	for _, runner := range runners {
		runner.cancelFunc() // Stop goroutine execution
	}

	// Wait briefly for goroutines to exit
	time.Sleep(1 * time.Second)

	// Mark all containers as INTERRUPTED in database
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, runner := range runners {
		// Only mark as INTERRUPTED if container is RUNNING
		// Skip containers that are already in terminal states (STOPPED, COMPLETED, FAILED)
		currentStatus := runner.containerEntity.Status()
		if currentStatus != container.ContainerStatusRunning {
			fmt.Printf("Skipping container %s (status: %s, not RUNNING)\n", runner.containerEntity.ID(), currentStatus)
			continue
		}

		now := time.Now()
		if err := s.containerRepo.UpdateStatus(
			ctx,
			runner.containerEntity.ID(),
			runner.containerEntity.PlayerID(),
			container.ContainerStatusInterrupted,
			&now,              // stoppedAt - when daemon interrupted
			nil,               // exitCode - nil for interruption
			"daemon_shutdown", // exitReason
		); err != nil {
			fmt.Printf("Warning: Failed to mark container %s as INTERRUPTED: %v\n", runner.containerEntity.ID(), err)
		}
	}

	fmt.Println("All containers interrupted and marked as INTERRUPTED in database")
}

func (s *DaemonServer) stopAllContainers() {
	s.containersMu.Lock()
	runners := make([]*ContainerRunner, 0, len(s.containers))
	for _, runner := range s.containers {
		runners = append(runners, runner)
	}
	s.containersMu.Unlock()

	// Stop all containers concurrently
	var wg sync.WaitGroup
	for _, runner := range runners {
		wg.Add(1)
		go func(r *ContainerRunner) {
			defer wg.Done()
			r.Stop()
		}(runner)
	}

	// Wait up to 30 seconds for graceful shutdown
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		fmt.Println("All containers stopped gracefully")
	case <-time.After(30 * time.Second):
		fmt.Println("Warning: Some containers did not stop within timeout")
	}
}

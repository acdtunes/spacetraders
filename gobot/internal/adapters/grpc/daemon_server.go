package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/goods/commands"
	miningCmd "github.com/andrescamacho/spacetraders-go/internal/application/mining/commands"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	shipQuery "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQuery "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
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
	mediator           common.Mediator
	listener           net.Listener
	logRepo            persistence.ContainerLogRepository
	containerRepo      *persistence.ContainerRepositoryGORM
	shipAssignmentRepo *persistence.ShipAssignmentRepositoryGORM
	waypointRepo       *persistence.GormWaypointRepository
	shipRepo           navigation.ShipRepository
	routingClient      routing.RoutingClient
	goodsFactoryRepo   *persistence.GormGoodsFactoryRepository

	// Container orchestration
	containers   map[string]*ContainerRunner
	containersMu sync.RWMutex

	// Command factory registry - maps command types to their factory functions
	commandFactories map[string]CommandFactory

	// Pending worker commands cache - stores commands with channels before start
	pendingWorkerCommands   map[string]interface{}
	pendingWorkerCommandsMu sync.RWMutex

	// Metrics
	metricsServer     *http.Server
	metricsConfig     *config.MetricsConfig
	metricsCollector  MetricsCollector

	// Shutdown coordination
	shutdownChan chan os.Signal
	done         chan struct{}
}

// NewDaemonServer creates a new daemon server instance
func NewDaemonServer(
	mediator common.Mediator,
	logRepo persistence.ContainerLogRepository,
	containerRepo *persistence.ContainerRepositoryGORM,
	shipAssignmentRepo *persistence.ShipAssignmentRepositoryGORM,
	waypointRepo *persistence.GormWaypointRepository,
	shipRepo navigation.ShipRepository,
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
		logRepo:               logRepo,
		containerRepo:         containerRepo,
		shipAssignmentRepo:    shipAssignmentRepo,
		waypointRepo:          waypointRepo,
		shipRepo:              shipRepo,
		routingClient:         routingClient,
		goodsFactoryRepo:      goodsFactoryRepo,
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
		server.metricsCollector = collector

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
	}

	// Register command factories for recovery
	server.registerCommandFactories()

	// Setup signal handling
	signal.Notify(server.shutdownChan, os.Interrupt, syscall.SIGTERM)

	return server, nil
}

// registerCommandFactories registers command factories for container recovery
// Adding a new container type only requires adding a factory here - no changes to recovery logic
func (s *DaemonServer) registerCommandFactories() {
	// Scout tour factory
	s.commandFactories["scout_tour"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		shipSymbol, ok := config["ship_symbol"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid ship_symbol")
		}

		marketsRaw, ok := config["markets"].([]interface{})
		if !ok {
			return nil, fmt.Errorf("missing or invalid markets")
		}

		markets := make([]string, len(marketsRaw))
		for i, m := range marketsRaw {
			markets[i], ok = m.(string)
			if !ok {
				return nil, fmt.Errorf("invalid market entry at index %d", i)
			}
		}

		iterations, ok := config["iterations"].(float64)
		if !ok {
			return nil, fmt.Errorf("missing or invalid iterations")
		}

		return &scoutingCmd.ScoutTourCommand{
			PlayerID:   shared.MustNewPlayerID(int(playerID)),
			ShipSymbol: shipSymbol,
			Markets:    markets,
			Iterations: int(iterations),
		}, nil
	}

	// Contract workflow factory (single contract execution)
	s.commandFactories["contract_workflow"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		shipSymbol, ok := config["ship_symbol"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid ship_symbol")
		}

		coordinatorID, _ := config["coordinator_id"].(string) // Optional

		return &contractCmd.RunWorkflowCommand{
			ShipSymbol:         shipSymbol,
			PlayerID:           shared.MustNewPlayerID(playerID),
			CoordinatorID:      coordinatorID,
			CompletionCallback: nil, // Will be set by container runner if needed
		}, nil
	}

	// Contract fleet coordinator factory (multi-ship coordination)
	s.commandFactories["contract_fleet_coordinator"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		containerID, ok := config["container_id"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid container_id")
		}

		// ship_symbols is deprecated and no longer required (dynamic discovery is used)
		// Pass empty array for backward compatibility
		return &contractCmd.RunFleetCoordinatorCommand{
			PlayerID:    shared.MustNewPlayerID(playerID),
			ShipSymbols: []string{}, // Deprecated field, no longer used
			ContainerID: containerID,
		}, nil
	}

	// Purchase ship factory
	s.commandFactories["purchase_ship"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		shipSymbol, ok := config["ship_symbol"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid ship_symbol")
		}

		shipType, ok := config["ship_type"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid ship_type")
		}

		shipyardWaypoint, _ := config["shipyard"].(string) // Optional

		return &shipyardCmd.PurchaseShipCommand{
			PurchasingShipSymbol: shipSymbol,
			ShipType:             shipType,
			PlayerID:             shared.MustNewPlayerID(playerID),
			ShipyardWaypoint:     shipyardWaypoint,
		}, nil
	}

	// Batch purchase ships factory
	s.commandFactories["batch_purchase_ships"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		shipSymbol, ok := config["ship_symbol"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid ship_symbol")
		}

		shipType, ok := config["ship_type"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid ship_type")
		}

		quantity, ok := config["quantity"].(float64)
		if !ok {
			return nil, fmt.Errorf("missing or invalid quantity")
		}

		maxBudget, ok := config["max_budget"].(float64)
		if !ok {
			return nil, fmt.Errorf("missing or invalid max_budget")
		}

		shipyardWaypoint, _ := config["shipyard"].(string) // Optional

		return &shipyardCmd.BatchPurchaseShipsCommand{
			PurchasingShipSymbol: shipSymbol,
			ShipType:             shipType,
			Quantity:             int(quantity),
			MaxBudget:            int(maxBudget),
			PlayerID:             shared.MustNewPlayerID(playerID),
			ShipyardWaypoint:     shipyardWaypoint,
		}, nil
	}

	// Mining worker factory
	s.commandFactories["mining_worker"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		shipSymbol, ok := config["ship_symbol"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid ship_symbol")
		}

		asteroidField, ok := config["asteroid_field"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid asteroid_field")
		}

		topNOres := 3 // Default
		if val, ok := config["top_n_ores"].(float64); ok {
			topNOres = int(val)
		}

		coordinatorID, _ := config["coordinator_id"].(string) // Optional

		return &miningCmd.RunWorkerCommand{
			ShipSymbol:    shipSymbol,
			PlayerID:      shared.MustNewPlayerID(playerID),
			AsteroidField: asteroidField,
			TopNOres:      topNOres,
			CoordinatorID: coordinatorID,
			Coordinator:   nil, // Set at runtime by coordinator when spawning worker
		}, nil
	}

	// Transport worker factory
	s.commandFactories["transport_worker"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		shipSymbol, ok := config["ship_symbol"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid ship_symbol")
		}

		asteroidField, ok := config["asteroid_field"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid asteroid_field")
		}

		coordinatorID, _ := config["coordinator_id"].(string) // Optional
		marketSymbol, _ := config["market_symbol"].(string)   // Optional

		return &miningCmd.RunTransportWorkerCommand{
			ShipSymbol:    shipSymbol,
			PlayerID:      shared.MustNewPlayerID(playerID),
			AsteroidField: asteroidField,
			MarketSymbol:  marketSymbol,
			CoordinatorID: coordinatorID,
			Coordinator:   nil, // Set at runtime by coordinator when spawning worker
		}, nil
	}

	// Mining coordinator factory
	s.commandFactories["mining_coordinator"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		miningOperationID, ok := config["mining_operation_id"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid mining_operation_id")
		}

		asteroidField, ok := config["asteroid_field"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid asteroid_field")
		}

		containerID, ok := config["container_id"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid container_id")
		}

		// Parse miner ships
		minerShipsRaw, ok := config["miner_ships"].([]interface{})
		if !ok {
			return nil, fmt.Errorf("missing or invalid miner_ships")
		}

		minerShips := make([]string, len(minerShipsRaw))
		for i, m := range minerShipsRaw {
			minerShips[i], ok = m.(string)
			if !ok {
				return nil, fmt.Errorf("invalid miner ship at index %d", i)
			}
		}

		// Parse transport ships
		transportShipsRaw, ok := config["transport_ships"].([]interface{})
		if !ok {
			return nil, fmt.Errorf("missing or invalid transport_ships")
		}

		transportShips := make([]string, len(transportShipsRaw))
		for i, t := range transportShipsRaw {
			transportShips[i], ok = t.(string)
			if !ok {
				return nil, fmt.Errorf("invalid transport ship at index %d", i)
			}
		}

		topNOres := 3
		if val, ok := config["top_n_ores"].(float64); ok {
			topNOres = int(val)
		}

		return &miningCmd.RunCoordinatorCommand{
			MiningOperationID: miningOperationID,
			PlayerID:          shared.MustNewPlayerID(playerID),
			AsteroidField:     asteroidField,
			MinerShips:        minerShips,
			TransportShips:    transportShips,
			TopNOres:          topNOres,
			ContainerID:       containerID,
		}, nil
	}

	// Goods factory coordinator factory
	s.commandFactories["goods_factory_coordinator"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		targetGood, ok := config["target_good"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid target_good")
		}

		systemSymbol, ok := config["system_symbol"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid system_symbol")
		}

		return &goodsCmd.RunFactoryCoordinatorCommand{
			PlayerID:     playerID,
			TargetGood:   targetGood,
			SystemSymbol: systemSymbol,
		}, nil
	}
}

// Start begins serving gRPC requests
func (s *DaemonServer) Start() error {
	fmt.Printf("Daemon server listening on unix socket: %s\n", s.listener.Addr().String())

	// Release all zombie assignments from previous daemon runs
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if s.shipAssignmentRepo != nil {
		count, err := s.shipAssignmentRepo.ReleaseAllActive(ctx, "daemon_restart")
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

		// Start metrics collector
		if s.metricsCollector != nil {
			s.metricsCollector.Start(context.Background())
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

	// Stop metrics collector first
	if s.metricsCollector != nil {
		s.metricsCollector.Stop()
	}

	// Shutdown HTTP server with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.metricsServer.Shutdown(ctx); err != nil {
		fmt.Printf("Error shutting down metrics server: %v\n", err)
	}
}

// handleShutdown manages graceful shutdown
func (s *DaemonServer) handleShutdown() {
	<-s.shutdownChan
	fmt.Println("\nShutdown signal received, stopping daemon...")

	// Stop metrics server and collector
	s.stopMetricsServer()

	// Interrupt all container goroutines and mark as INTERRUPTED in DB for recovery
	s.interruptAllContainers()

	// Close listener
	if s.listener != nil {
		s.listener.Close()
	}

	close(s.done)
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

		// Skip worker containers (those with coordinator_id)
		// Workers are managed by their parent coordinator and should not be recovered independently
		if coordinatorID, hasCoordinator := config["coordinator_id"].(string); hasCoordinator && coordinatorID != "" {
			fmt.Printf("Container %s: Skipping recovery (worker container managed by coordinator %s)\n", containerModel.ID, coordinatorID)
			s.markContainerFailed(ctx, containerModel, "orphaned_worker", "Worker container should not be recovered without parent coordinator")
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
		// Re-assign ship using UPSERT (will update old released assignment)
		assignmentEntity := &persistence.ShipAssignmentModel{
			ShipSymbol:  shipSymbol,
			PlayerID:    containerModel.PlayerID,
			ContainerID: containerModel.ID,
			Status:      "active",
			AssignedAt:  containerModel.StartedAt,
		}

		if err := s.shipAssignmentRepo.Assign(ctx, containerModelToShipAssignment(assignmentEntity)); err != nil {
			return fmt.Errorf("failed to reassign ship %s: %w", shipSymbol, err)
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
		config,
		nil, // Use default RealClock for production
	)

	// Restore restart count from database
	for i := 0; i < containerModel.RestartCount; i++ {
		containerEntity.IncrementRestartCount()
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
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
}

// containerModelToShipAssignment converts a ShipAssignmentModel to domain entity
// This is a helper for the recovery process
func containerModelToShipAssignment(model *persistence.ShipAssignmentModel) *container.ShipAssignment {
	return container.NewShipAssignment(
		model.ShipSymbol,
		model.PlayerID,
		model.ContainerID,
		nil, // Clock not needed
	)
}

// NavigateShip handles ship navigation requests
// This will be called by the gRPC handler when proto is generated
func (s *DaemonServer) NavigateShip(ctx context.Context, shipSymbol, destination string, playerID int) (string, error) {
	// Create container ID
	containerID := utils.GenerateContainerID("navigate", shipSymbol)

	// Create navigation command
	cmd := &shipCmd.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    shared.MustNewPlayerID(playerID),
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeNavigate,
		playerID,
		1, // Single iteration for navigate
		map[string]interface{}{
			"ship_symbol": shipSymbol,
			"destination": destination,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "navigate_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// DockShip handles ship docking requests
func (s *DaemonServer) DockShip(ctx context.Context, shipSymbol string, playerID int) (string, error) {
	containerID := utils.GenerateContainerID("dock", shipSymbol)

	cmd := &shipTypes.DockShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   shared.MustNewPlayerID(playerID),
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeDock,
		playerID,
		1, // Single iteration for dock
		map[string]interface{}{
			"ship_symbol": shipSymbol,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "dock_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// OrbitShip handles ship orbit requests
func (s *DaemonServer) OrbitShip(ctx context.Context, shipSymbol string, playerID int) (string, error) {
	containerID := utils.GenerateContainerID("orbit", shipSymbol)

	cmd := &shipTypes.OrbitShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   shared.MustNewPlayerID(playerID),
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeOrbit,
		playerID,
		1, // Single iteration for orbit
		map[string]interface{}{
			"ship_symbol": shipSymbol,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "orbit_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// RefuelShip handles ship refuel requests
func (s *DaemonServer) RefuelShip(ctx context.Context, shipSymbol string, playerID int, units *int) (string, error) {
	containerID := utils.GenerateContainerID("refuel", shipSymbol)

	cmd := &shipTypes.RefuelShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   shared.MustNewPlayerID(playerID),
		Units:      units,
	}

	metadata := map[string]interface{}{
		"ship_symbol": shipSymbol,
	}
	if units != nil {
		metadata["units"] = *units
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeRefuel,
		playerID,
		1, // Single iteration for refuel
		metadata,
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "refuel_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// BatchContractWorkflow handles batch contract workflow requests
func (s *DaemonServer) BatchContractWorkflow(ctx context.Context, shipSymbol string, iterations, playerID int) (string, error) {
	// Create container ID
	containerID := utils.GenerateContainerID("batch_contract_workflow", shipSymbol)

	// Delegate to ContractWorkflow (with no completion callback)
	// Note: iterations parameter is ignored for now - ContractWorkflow always does 1 iteration
	// TODO: Support multiple iterations by updating container metadata
	return s.ContractWorkflow(ctx, containerID, shipSymbol, playerID, "", nil)
}

// ContractWorkflow creates and starts a contract workflow container with optional completion callback
func (s *DaemonServer) ContractWorkflow(
	ctx context.Context,
	containerID string,
	shipSymbol string,
	playerID int,
	coordinatorID string,
	completionCallback chan<- string,
) (string, error) {
	// Persist container to DB
	if err := s.PersistContractWorkflow(ctx, containerID, shipSymbol, playerID, coordinatorID); err != nil {
		return "", err
	}

	// Start the container
	if err := s.StartContractWorkflow(ctx, containerID, completionCallback); err != nil {
		return "", err
	}

	return containerID, nil
}

// PersistContractWorkflow creates a contract workflow container in DB (does NOT start it)
func (s *DaemonServer) PersistContractWorkflow(
	ctx context.Context,
	containerID string,
	shipSymbol string,
	playerID int,
	coordinatorID string,
) error {
	// Create container entity (single iteration for worker containers)
	iterations := 1
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeContractWorkflow,
		playerID,
		iterations,
		map[string]interface{}{
			"ship_symbol":    shipSymbol,
			"coordinator_id": coordinatorID,
		},
		nil, // Use default RealClock for production
	)

	// Atomically check for existing worker and create new one
	// This prevents multiple workers from running simultaneously
	created, err := s.containerRepo.CreateIfNoActiveWorker(ctx, containerEntity, "contract_workflow")
	if err != nil {
		return fmt.Errorf("failed to persist container: %w", err)
	}

	if !created {
		return fmt.Errorf("CONTRACT_WORKFLOW container already running for player %d", playerID)
	}

	return nil
}

// StartContractWorkflow starts a previously persisted contract workflow container
func (s *DaemonServer) StartContractWorkflow(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	// We need playerID to load the container, but we don't have it here
	// Solution: Load from all players or add playerID parameter
	// For now, use a workaround: query by ID only (add new repository method)
	// Temporary: Use ListAll and filter
	allContainers, err := s.containerRepo.ListAll(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	var containerModel *persistence.ContainerModel
	for _, c := range allContainers {
		if c.ID == containerID {
			containerModel = c
			break
		}
	}

	if containerModel == nil {
		return fmt.Errorf("container %s not found", containerID)
	}

	// Parse config
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Extract fields
	shipSymbol := config["ship_symbol"].(string)
	coordinatorID, _ := config["coordinator_id"].(string)

	// Create command
	cmd := &contractCmd.RunWorkflowCommand{
		ShipSymbol:         shipSymbol,
		PlayerID:           shared.MustNewPlayerID(containerModel.PlayerID),
		ContainerID:        containerModel.ID,
		CoordinatorID:      coordinatorID,
		CompletionCallback: completionCallback,
	}

	// Create container entity from model
	// Worker containers always have 1 iteration
	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		1, // Worker containers are single iteration
		config,
		nil,
	)

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	if completionCallback != nil {
		runner.SetCompletionCallback(completionCallback)
	}
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return nil
}

// ContractFleetCoordinator creates a fleet coordinator for multi-ship contract operations
// Ships are discovered dynamically - no pre-assignment needed
func (s *DaemonServer) ContractFleetCoordinator(ctx context.Context, shipSymbols []string, playerID int) (string, error) {
	// Create container ID using player ID instead of ship symbol (no ships pre-assigned)
	containerID := utils.GenerateContainerID("contract_fleet_coordinator", fmt.Sprintf("player-%d", playerID))

	// Create contract fleet coordinator command (no ship symbols - uses dynamic discovery)
	cmd := &contractCmd.RunFleetCoordinatorCommand{
		PlayerID:    shared.MustNewPlayerID(playerID),
		ShipSymbols: nil, // Dynamic discovery - no pre-assignment
		ContainerID: containerID,
	}

	// No ship symbols metadata needed
	var shipSymbolsInterface []interface{}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeContractFleetCoordinator,
		playerID,
		-1, // Infinite iterations
		map[string]interface{}{
			"ship_symbols": shipSymbolsInterface,
			"container_id": containerID,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "contract_fleet_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// MiningOperationResult contains the result of a mining operation
type MiningOperationResult struct {
	ContainerID   string
	AsteroidField string
	MarketSymbol  string
	ShipRoutes    []common.ShipRouteDTO
	Errors        []string
}

// MiningOperation starts a mining operation with Transport-as-Sink pattern
func (s *DaemonServer) MiningOperation(
	ctx context.Context,
	asteroidField string,
	minerShips []string,
	transportShips []string,
	topNOres int,
	miningType string,
	force bool,
	dryRun bool,
	maxLegTime int,
	playerID int,
) (*MiningOperationResult, error) {
	// Validate we have either an asteroid field or mining type
	if asteroidField == "" && miningType == "" {
		return nil, fmt.Errorf("no asteroid field specified and no mining type provided")
	}

	// Create container ID
	var containerID string
	if dryRun {
		containerID = utils.GenerateContainerID("mining_dry_run", minerShips[0])
	} else {
		containerID = utils.GenerateContainerID("mining_coordinator", minerShips[0])
	}

	// Create mining coordinator command
	cmd := &miningCmd.RunCoordinatorCommand{
		MiningOperationID: containerID,
		PlayerID:          shared.MustNewPlayerID(playerID),
		AsteroidField:     asteroidField,
		MinerShips:        minerShips,
		TransportShips:    transportShips,
		TopNOres:          topNOres,
		ContainerID:       containerID,
		MiningType:        miningType,
		Force:             force,
		DryRun:            dryRun,
		MaxLegTime:        maxLegTime,
	}

	// Convert ship arrays to interface{} for metadata
	minerShipsInterface := make([]interface{}, len(minerShips))
	for i, s := range minerShips {
		minerShipsInterface[i] = s
	}

	transportShipsInterface := make([]interface{}, len(transportShips))
	for i, s := range transportShips {
		transportShipsInterface[i] = s
	}

	// Set iterations: 1 for dry-run (single execution), -1 for normal (infinite)
	iterations := -1
	if dryRun {
		iterations = 1
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeMiningCoordinator,
		playerID,
		iterations,
		map[string]interface{}{
			"mining_operation_id": containerID,
			"asteroid_field":      asteroidField,
			"miner_ships":         minerShipsInterface,
			"transport_ships":     transportShipsInterface,
			"top_n_ores":          topNOres,
			"container_id":        containerID,
			"mining_type":         miningType,
			"force":               force,
			"dry_run":             dryRun,
			"max_leg_time":        maxLegTime,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "mining_coordinator"); err != nil {
		return nil, fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return &MiningOperationResult{
		ContainerID:   containerID,
		AsteroidField: asteroidField,
	}, nil
}

// extractSystemSymbol extracts system symbol from a waypoint symbol
func extractSystemSymbol(waypointSymbol string) string {
	// Waypoint format: X1-GZ7-B12 -> System: X1-GZ7
	parts := make([]string, 0)
	current := ""
	for _, c := range waypointSymbol {
		if c == '-' {
			parts = append(parts, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	if len(parts) >= 2 {
		return parts[0] + "-" + parts[1]
	}
	return waypointSymbol
}

// ScoutTour handles market scouting tour requests (single ship)
func (s *DaemonServer) ScoutTour(ctx context.Context, containerID string, shipSymbol string, markets []string, iterations, playerID int) (string, error) {
	// Use provided container ID from caller

	// Create scout tour command
	cmd := &scoutingCmd.ScoutTourCommand{
		PlayerID:   shared.MustNewPlayerID(int(playerID)),
		ShipSymbol: shipSymbol,
		Markets:    markets,
		Iterations: iterations,
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeScout,
		playerID,
		iterations,
		map[string]interface{}{
			"ship_symbol": shipSymbol,
			"markets":     markets,
			"iterations":  iterations,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "scout_tour"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// TourSell handles cargo selling tour requests (single ship)
func (s *DaemonServer) TourSell(ctx context.Context, containerID string, shipSymbol string, returnWaypoint string, playerID int) (string, error) {
	// Create tour sell command
	cmd := &tradingCmd.RunTourSellingCommand{
		ShipSymbol:     shipSymbol,
		PlayerID:       shared.MustNewPlayerID(playerID),
		ReturnWaypoint: returnWaypoint,
	}

	// Create container for this operation (single iteration)
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeTrading,
		playerID,
		1, // Single iteration for tour sell
		map[string]interface{}{
			"ship_symbol":     shipSymbol,
			"return_waypoint": returnWaypoint,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "tour_sell"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// ScoutMarkets handles fleet deployment for market scouting (multi-ship with VRP)
func (s *DaemonServer) ScoutMarkets(
	ctx context.Context,
	shipSymbols []string,
	systemSymbol string,
	markets []string,
	iterations int,
	playerID int,
) ([]string, map[string][]string, []string, error) {
	// Create scout markets command
	cmd := &scoutingCmd.ScoutMarketsCommand{
		PlayerID:     shared.MustNewPlayerID(int(playerID)),
		ShipSymbols:  shipSymbols,
		SystemSymbol: systemSymbol,
		Markets:      markets,
		Iterations:   iterations,
	}

	// Execute via mediator (synchronously)
	response, err := s.mediator.Send(ctx, cmd)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to execute scout markets command: %w", err)
	}

	// Type assert response
	scoutResp, ok := response.(*scoutingCmd.ScoutMarketsResponse)
	if !ok {
		return nil, nil, nil, fmt.Errorf("invalid response type from scout markets handler")
	}

	return scoutResp.ContainerIDs, scoutResp.Assignments, scoutResp.ReusedContainers, nil
}

// AssignScoutingFleet creates a scout-fleet-assignment container for async VRP optimization
// Returns the container ID immediately without blocking
func (s *DaemonServer) AssignScoutingFleet(
	ctx context.Context,
	systemSymbol string,
	playerID int,
) (string, error) {
	// Generate container ID
	containerID := utils.GenerateContainerID("scout-fleet-assignment", systemSymbol)

	// Create assign scouting fleet command (will execute inside container)
	cmd := &scoutingCmd.AssignScoutingFleetCommand{
		PlayerID:     shared.MustNewPlayerID(int(playerID)),
		SystemSymbol: systemSymbol,
	}

	// Create container entity (one-time execution)
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeScoutFleetAssignment,
		playerID,
		1, // One-time execution
		map[string]interface{}{
			"system_symbol": systemSymbol,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "scout_fleet_assignment"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Fleet assignment container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// ListContainers returns all registered containers
func (s *DaemonServer) ListContainers(playerID *int, status *string) []*container.Container {
	s.containersMu.RLock()
	defer s.containersMu.RUnlock()

	containers := make([]*container.Container, 0, len(s.containers))
	for _, runner := range s.containers {
		cont := runner.Container()

		// Apply filters
		if playerID != nil && cont.PlayerID() != *playerID {
			continue
		}
		if status != nil && string(cont.Status()) != *status {
			continue
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

// StopContainer stops a running container
func (s *DaemonServer) StopContainer(containerID string) error {
	s.containersMu.RLock()
	runner, exists := s.containers[containerID]
	s.containersMu.RUnlock()

	if !exists {
		return fmt.Errorf("container not found: %s", containerID)
	}

	return runner.Stop()
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

// ListShips handles ship listing requests
func (s *DaemonServer) ListShips(ctx context.Context, playerID *int, agentSymbol string) ([]*pb.ShipInfo, error) {
	// Create query
	query := &shipQuery.ListShipsQuery{
		PlayerID:    playerID,
		AgentSymbol: agentSymbol,
	}

	// Execute via mediator
	response, err := s.mediator.Send(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list ships: %w", err)
	}

	// Convert response
	listResp, ok := response.(*shipQuery.ListShipsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type")
	}

	// Convert domain ships to proto ships
	var ships []*pb.ShipInfo
	for _, domainShip := range listResp.Ships {
		ships = append(ships, &pb.ShipInfo{
			Symbol:        domainShip.ShipSymbol(),
			Location:      domainShip.CurrentLocation().Symbol,
			NavStatus:     string(domainShip.NavStatus()),
			FuelCurrent:   int32(domainShip.Fuel().Current),
			FuelCapacity:  int32(domainShip.Fuel().Capacity),
			CargoUnits:    int32(domainShip.CargoUnits()),
			CargoCapacity: int32(domainShip.CargoCapacity()),
			EngineSpeed:   int32(domainShip.EngineSpeed()),
		})
	}

	return ships, nil
}

// GetShip handles ship detail requests
func (s *DaemonServer) GetShip(ctx context.Context, shipSymbol string, playerID *int, agentSymbol string) (*pb.ShipDetail, error) {
	// Create query
	query := &shipQuery.GetShipQuery{
		ShipSymbol:  shipSymbol,
		PlayerID:    playerID,
		AgentSymbol: agentSymbol,
	}

	// Execute via mediator
	response, err := s.mediator.Send(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship: %w", err)
	}

	// Convert response
	getResp, ok := response.(*shipQuery.GetShipResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type")
	}

	domainShip := getResp.Ship

	// Convert cargo items
	var cargoItems []*pb.CargoItem
	for _, item := range domainShip.Cargo().Inventory {
		cargoItems = append(cargoItems, &pb.CargoItem{
			Symbol: item.Symbol,
			Name:   item.Name,
			Units:  int32(item.Units),
		})
	}

	// Build ship detail
	shipDetail := &pb.ShipDetail{
		Symbol:         domainShip.ShipSymbol(),
		Location:       domainShip.CurrentLocation().Symbol,
		NavStatus:      string(domainShip.NavStatus()),
		FuelCurrent:    int32(domainShip.Fuel().Current),
		FuelCapacity:   int32(domainShip.Fuel().Capacity),
		CargoUnits:     int32(domainShip.CargoUnits()),
		CargoCapacity:  int32(domainShip.CargoCapacity()),
		CargoInventory: cargoItems,
		EngineSpeed:    int32(domainShip.EngineSpeed()),
		Role:           domainShip.Role(),
	}

	return shipDetail, nil
}

// GetShipyardListings retrieves available ships at a shipyard
func (s *DaemonServer) GetShipyardListings(ctx context.Context, systemSymbol, waypointSymbol string, playerID *int, agentSymbol string) ([]*pb.ShipListing, string, int32, error) {
	// Require player ID for now (agent symbol resolution can be added later)
	if playerID == nil || *playerID == 0 {
		return nil, "", 0, fmt.Errorf("player_id is required")
	}

	// Create query
	query := &shipyardQuery.GetShipyardListingsQuery{
		SystemSymbol:   systemSymbol,
		WaypointSymbol: waypointSymbol,
		PlayerID:       shared.MustNewPlayerID(*playerID),
	}

	// Execute via mediator
	response, err := s.mediator.Send(ctx, query)
	if err != nil {
		return nil, "", 0, fmt.Errorf("failed to get shipyard listings: %w", err)
	}

	// Convert response
	listingsResp, ok := response.(*shipyardQuery.GetShipyardListingsResponse)
	if !ok {
		return nil, "", 0, fmt.Errorf("unexpected response type")
	}

	// Convert to protobuf format
	listings := make([]*pb.ShipListing, len(listingsResp.Shipyard.Listings))
	for i, listing := range listingsResp.Shipyard.Listings {
		listings[i] = &pb.ShipListing{
			ShipType:      listing.ShipType,
			Name:          listing.Name,
			Description:   listing.Description,
			PurchasePrice: int32(listing.PurchasePrice),
		}
	}

	return listings, listingsResp.Shipyard.Symbol, int32(listingsResp.Shipyard.ModificationFee), nil
}

// PurchaseShip purchases a single ship from a shipyard
func (s *DaemonServer) PurchaseShip(ctx context.Context, purchasingShipSymbol, shipType string, playerID int, shipyardWaypoint *string) (string, string, int32, int32, string, error) {
	// Create purchase command
	cmd := &shipyardCmd.PurchaseShipCommand{
		PurchasingShipSymbol: purchasingShipSymbol,
		ShipType:             shipType,
		PlayerID:             shared.MustNewPlayerID(playerID),
		ShipyardWaypoint:     "",
	}
	if shipyardWaypoint != nil {
		cmd.ShipyardWaypoint = *shipyardWaypoint
	}

	// Create container ID
	containerID := utils.GenerateContainerID("purchase_ship", purchasingShipSymbol)

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypePurchase,
		playerID,
		1, // Single iteration
		map[string]interface{}{
			"ship_symbol": purchasingShipSymbol,
			"ship_type":   shipType,
			"shipyard":    cmd.ShipyardWaypoint,
		},
		nil, // Use real clock
	)

	// Persist container to database before starting (prevents foreign key violations in logs)
	if err := s.containerRepo.Add(ctx, containerEntity, "purchase_ship"); err != nil {
		return "", "", 0, 0, "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)

	// Store container
	s.containersMu.Lock()
	s.containers[containerID] = runner
	s.containersMu.Unlock()

	// Start execution in background
	runner.Start()

	return containerID, "", 0, 0, "starting", nil
}

// BatchPurchaseShips purchases multiple ships from a shipyard as a background operation
func (s *DaemonServer) BatchPurchaseShips(ctx context.Context, purchasingShipSymbol, shipType string, quantity, maxBudget, playerID int, shipyardWaypoint *string, iterations *int) (string, int32, int32, string, string, error) {
	// Create batch purchase command
	cmd := &shipyardCmd.BatchPurchaseShipsCommand{
		PurchasingShipSymbol: purchasingShipSymbol,
		ShipType:             shipType,
		Quantity:             quantity,
		MaxBudget:            maxBudget,
		PlayerID:             shared.MustNewPlayerID(playerID),
		ShipyardWaypoint:     "",
	}
	if shipyardWaypoint != nil {
		cmd.ShipyardWaypoint = *shipyardWaypoint
	}

	// Resolve iterations (default to 1)
	iterCount := 1
	if iterations != nil {
		iterCount = *iterations
	}

	// Create container ID
	containerID := utils.GenerateContainerID("batch_purchase_ships", purchasingShipSymbol)

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypePurchase,
		playerID,
		iterCount,
		map[string]interface{}{
			"ship_symbol": purchasingShipSymbol,
			"ship_type":   shipType,
			"quantity":    quantity,
			"max_budget":  maxBudget,
			"shipyard":    cmd.ShipyardWaypoint,
		},
		nil, // Use real clock
	)

	// Persist container to database before starting (prevents foreign key violations in logs)
	if err := s.containerRepo.Add(ctx, containerEntity, "batch_purchase_ships"); err != nil {
		return "", 0, 0, "", "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)

	// Store container
	s.containersMu.Lock()
	s.containers[containerID] = runner
	s.containersMu.Unlock()

	// Start execution in background
	runner.Start()

	return containerID, int32(quantity), int32(maxBudget), cmd.ShipyardWaypoint, "starting", nil
}

// PersistMiningWorkerContainer creates a mining worker container in DB (does NOT start it)
func (s *DaemonServer) PersistMiningWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	cmd, ok := command.(*miningCmd.RunWorkerCommand)
	if !ok {
		return fmt.Errorf("invalid command type for mining worker")
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeMiningWorker,
		int(playerID),
		1, // Worker containers are single iteration
		map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"asteroid_field": cmd.AsteroidField,
			"top_n_ores":     cmd.TopNOres,
			"coordinator_id": cmd.CoordinatorID,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "mining_worker"); err != nil {
		return fmt.Errorf("failed to persist container: %w", err)
	}

	// Cache the command with channels for StartMiningWorkerContainer
	s.pendingWorkerCommandsMu.Lock()
	s.pendingWorkerCommands[containerID] = cmd
	s.pendingWorkerCommandsMu.Unlock()

	return nil
}

// StartMiningWorkerContainer starts a previously persisted mining worker container
func (s *DaemonServer) StartMiningWorkerContainer(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	// Try to get cached command with channels first
	s.pendingWorkerCommandsMu.Lock()
	cachedCmd, hasCached := s.pendingWorkerCommands[containerID]
	if hasCached {
		delete(s.pendingWorkerCommands, containerID)
	}
	s.pendingWorkerCommandsMu.Unlock()

	var cmd *miningCmd.RunWorkerCommand
	var config map[string]interface{}
	var playerID int

	if hasCached {
		// Use cached command with channels
		cmd = cachedCmd.(*miningCmd.RunWorkerCommand)
		playerID = cmd.PlayerID.Value()
		config = map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"asteroid_field": cmd.AsteroidField,
			"top_n_ores":     cmd.TopNOres,
			"coordinator_id": cmd.CoordinatorID,
		}
	} else {
		// Fallback: Load from database (for recovery - channels will be nil)
		allContainers, err := s.containerRepo.ListAll(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to list containers: %w", err)
		}

		var containerModel *persistence.ContainerModel
		for _, c := range allContainers {
			if c.ID == containerID {
				containerModel = c
				break
			}
		}

		if containerModel == nil {
			return fmt.Errorf("container %s not found", containerID)
		}

		// Parse config
		if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
			return fmt.Errorf("failed to parse config: %w", err)
		}

		// Extract fields
		shipSymbol := config["ship_symbol"].(string)
		asteroidField := config["asteroid_field"].(string)
		topNOres := 3
		if val, ok := config["top_n_ores"].(float64); ok {
			topNOres = int(val)
		}
		coordinatorID, _ := config["coordinator_id"].(string)

		playerID = containerModel.PlayerID
		cmd = &miningCmd.RunWorkerCommand{
			ShipSymbol:    shipSymbol,
			PlayerID:      shared.MustNewPlayerID(playerID),
			AsteroidField: asteroidField,
			TopNOres:      topNOres,
			CoordinatorID: coordinatorID,
			Coordinator:   nil, // Not available from DB recovery - worker must reconnect
		}
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeMiningWorker,
		playerID,
		1, // Worker containers are single iteration
		config,
		nil,
	)

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	if completionCallback != nil {
		runner.SetCompletionCallback(completionCallback)
	}
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return nil
}

// PersistTransportWorkerContainer creates a transport worker container in DB (does NOT start it)
func (s *DaemonServer) PersistTransportWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	cmd, ok := command.(*miningCmd.RunTransportWorkerCommand)
	if !ok {
		return fmt.Errorf("invalid command type for transport worker")
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeTransportWorker,
		int(playerID),
		1, // Worker containers are single iteration
		map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"asteroid_field": cmd.AsteroidField,
			"coordinator_id": cmd.CoordinatorID,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "transport_worker"); err != nil {
		return fmt.Errorf("failed to persist container: %w", err)
	}

	// Cache the command with channels for StartTransportWorkerContainer
	s.pendingWorkerCommandsMu.Lock()
	s.pendingWorkerCommands[containerID] = cmd
	s.pendingWorkerCommandsMu.Unlock()

	return nil
}

// StartTransportWorkerContainer starts a previously persisted transport worker container
func (s *DaemonServer) StartTransportWorkerContainer(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	// Try to get cached command with channels first
	s.pendingWorkerCommandsMu.Lock()
	cachedCmd, hasCached := s.pendingWorkerCommands[containerID]
	if hasCached {
		delete(s.pendingWorkerCommands, containerID)
	}
	s.pendingWorkerCommandsMu.Unlock()

	var cmd *miningCmd.RunTransportWorkerCommand
	var config map[string]interface{}
	var playerID int

	if hasCached {
		// Use cached command with channels
		cmd = cachedCmd.(*miningCmd.RunTransportWorkerCommand)
		playerID = cmd.PlayerID.Value()
		config = map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"asteroid_field": cmd.AsteroidField,
			"coordinator_id": cmd.CoordinatorID,
		}
	} else {
		// Fallback: Load from database (for recovery - channels will be nil)
		allContainers, err := s.containerRepo.ListAll(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to list containers: %w", err)
		}

		var containerModel *persistence.ContainerModel
		for _, c := range allContainers {
			if c.ID == containerID {
				containerModel = c
				break
			}
		}

		if containerModel == nil {
			return fmt.Errorf("container %s not found", containerID)
		}

		// Parse config
		if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
			return fmt.Errorf("failed to parse config: %w", err)
		}

		// Extract fields
		shipSymbol := config["ship_symbol"].(string)
		asteroidField := config["asteroid_field"].(string)
		coordinatorID, _ := config["coordinator_id"].(string)

		playerID = containerModel.PlayerID
		marketSymbol, _ := config["market_symbol"].(string)
		cmd = &miningCmd.RunTransportWorkerCommand{
			ShipSymbol:    shipSymbol,
			PlayerID:      shared.MustNewPlayerID(playerID),
			AsteroidField: asteroidField,
			MarketSymbol:  marketSymbol,
			CoordinatorID: coordinatorID,
			Coordinator:   nil, // Not available from DB recovery - worker must reconnect
		}
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeTransportWorker,
		playerID,
		1, // Worker containers are single iteration
		config,
		nil,
	)

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	if completionCallback != nil {
		runner.SetCompletionCallback(completionCallback)
	}
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return nil
}

// PersistMiningCoordinatorContainer creates a mining coordinator container in DB (does NOT start it)
func (s *DaemonServer) PersistMiningCoordinatorContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	cmd, ok := command.(*miningCmd.RunCoordinatorCommand)
	if !ok {
		return fmt.Errorf("invalid command type for mining coordinator")
	}

	// Convert ship arrays to interface{} for JSON
	minerShipsInterface := make([]interface{}, len(cmd.MinerShips))
	for i, s := range cmd.MinerShips {
		minerShipsInterface[i] = s
	}

	transportShipsInterface := make([]interface{}, len(cmd.TransportShips))
	for i, s := range cmd.TransportShips {
		transportShipsInterface[i] = s
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeMiningCoordinator,
		int(playerID),
		-1, // Infinite iterations for coordinator
		map[string]interface{}{
			"mining_operation_id": cmd.MiningOperationID,
			"asteroid_field":      cmd.AsteroidField,
			"miner_ships":         minerShipsInterface,
			"transport_ships":     transportShipsInterface,
			"top_n_ores":          cmd.TopNOres,
			"container_id":        cmd.ContainerID,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "mining_coordinator"); err != nil {
		return fmt.Errorf("failed to persist container: %w", err)
	}

	return nil
}

// StartMiningCoordinatorContainer starts a previously persisted mining coordinator container
func (s *DaemonServer) StartMiningCoordinatorContainer(
	ctx context.Context,
	containerID string,
) error {
	// Load container from database
	allContainers, err := s.containerRepo.ListAll(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	var containerModel *persistence.ContainerModel
	for _, c := range allContainers {
		if c.ID == containerID {
			containerModel = c
			break
		}
	}

	if containerModel == nil {
		return fmt.Errorf("container %s not found", containerID)
	}

	// Parse config
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Use factory to create command
	factory := s.commandFactories["mining_coordinator"]
	cmd, err := factory(config, containerModel.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to create command: %w", err)
	}

	// Create container entity from model
	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		-1, // Coordinator runs indefinitely
		config,
		nil,
	)

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return nil
}

// StartGoodsFactory creates and starts a goods factory coordinator container
func (s *DaemonServer) StartGoodsFactory(
	ctx context.Context,
	targetGood string,
	systemSymbol string,
	playerID int,
) (*GoodsFactoryResult, error) {
	// Generate container ID
	containerID := utils.GenerateContainerID("goods_factory", targetGood)

	// Create factory coordinator command
	cmd := &goodsCmd.RunFactoryCoordinatorCommand{
		PlayerID:     playerID,
		TargetGood:   targetGood,
		SystemSymbol: systemSymbol,
	}

	// Create container metadata
	metadata := map[string]interface{}{
		"target_good":   targetGood,
		"system_symbol": systemSymbol,
	}

	// Create container entity (iterations = 1 for single production run)
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerType("goods_factory_coordinator"),
		playerID,
		1, // Single production run
		metadata,
		nil, // Use default RealClock
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "goods_factory_coordinator"); err != nil {
		return nil, fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return &GoodsFactoryResult{
		FactoryID:  containerID,
		TargetGood: targetGood,
		NodesTotal: 0, // Will be populated as factory runs
	}, nil
}

// StopGoodsFactory stops a running goods factory container
func (s *DaemonServer) StopGoodsFactory(
	ctx context.Context,
	factoryID string,
	playerID int,
) error {
	// Stop the container using existing container stop logic
	return s.StopContainer(factoryID)
}

// GetFactoryStatus retrieves the status of a goods factory from the repository
func (s *DaemonServer) GetFactoryStatus(
	ctx context.Context,
	factoryID string,
	playerID int,
) (*GoodsFactoryStatus, error) {
	// Query factory from repository
	factory, err := s.goodsFactoryRepo.FindByID(ctx, factoryID, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find factory: %w", err)
	}

	// Serialize dependency tree to JSON
	treeJSON, err := json.Marshal(factory.DependencyTree())
	if err != nil {
		return nil, fmt.Errorf("failed to serialize dependency tree: %w", err)
	}

	return &GoodsFactoryStatus{
		FactoryID:        factory.ID(),
		TargetGood:       factory.TargetGood(),
		Status:           string(factory.Status()),
		DependencyTree:   string(treeJSON),
		QuantityAcquired: factory.QuantityAcquired(),
		TotalCost:        factory.TotalCost(),
		NodesCompleted:   factory.CompletedNodes(),
		NodesTotal:       factory.TotalNodes(),
		SystemSymbol:     factory.SystemSymbol(),
		ShipsUsed:        factory.ShipsUsed(),
		MarketQueries:    factory.MarketQueries(),
		ParallelLevels:   factory.ParallelLevels(),
		EstimatedSpeedup: factory.EstimatedSpeedup(),
	}, nil
}

// GoodsFactoryResult contains the result of starting a goods factory
type GoodsFactoryResult struct {
	FactoryID  string
	TargetGood string
	NodesTotal int
}

// GoodsFactoryStatus contains detailed status information for a goods factory
type GoodsFactoryStatus struct {
	FactoryID        string
	TargetGood       string
	Status           string
	DependencyTree   string
	QuantityAcquired int
	TotalCost        int
	NodesCompleted   int
	NodesTotal       int
	SystemSymbol     string
	ShipsUsed        int
	MarketQueries    int
	ParallelLevels   int
	EstimatedSpeedup float64
}

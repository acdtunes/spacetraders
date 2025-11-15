package grpc

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/application/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"google.golang.org/grpc"
)

// DaemonServer implements the gRPC daemon service
// Handles CLI requests and orchestrates background container operations
type DaemonServer struct {
	mediator      common.Mediator
	listener      net.Listener
	logRepo       persistence.ContainerLogRepository
	containerRepo *persistence.ContainerRepositoryGORM

	// Container orchestration
	containers   map[string]*ContainerRunner
	containersMu sync.RWMutex

	// Shutdown coordination
	shutdownChan chan os.Signal
	done         chan struct{}
}

// NewDaemonServer creates a new daemon server instance
func NewDaemonServer(
	mediator common.Mediator,
	logRepo persistence.ContainerLogRepository,
	containerRepo *persistence.ContainerRepositoryGORM,
	socketPath string,
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
		mediator:      mediator,
		logRepo:       logRepo,
		containerRepo: containerRepo,
		listener:      listener,
		containers:    make(map[string]*ContainerRunner),
		shutdownChan:  make(chan os.Signal, 1),
		done:          make(chan struct{}),
	}

	// Setup signal handling
	signal.Notify(server.shutdownChan, os.Interrupt, syscall.SIGTERM)

	return server, nil
}

// Start begins serving gRPC requests
func (s *DaemonServer) Start() error {
	fmt.Printf("Daemon server listening on unix socket: %s\n", s.listener.Addr().String())

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

// handleShutdown manages graceful shutdown
func (s *DaemonServer) handleShutdown() {
	<-s.shutdownChan
	fmt.Println("\nShutdown signal received, stopping daemon...")

	// Stop all running containers
	s.stopAllContainers()

	// Close listener
	if s.listener != nil {
		s.listener.Close()
	}

	close(s.done)
}

// NavigateShip handles ship navigation requests
// This will be called by the gRPC handler when proto is generated
func (s *DaemonServer) NavigateShip(ctx context.Context, shipSymbol, destination string, playerID int) (string, error) {
	// Create container ID
	containerID := generateContainerID("navigate", shipSymbol)

	// Create navigation command
	cmd := &ship.NavigateShipCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    playerID,
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
	if err := s.containerRepo.Insert(ctx, containerEntity, "navigate_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo)
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
	containerID := generateContainerID("dock", shipSymbol)

	cmd := &ship.DockShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
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
	if err := s.containerRepo.Insert(ctx, containerEntity, "dock_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo)
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
	containerID := generateContainerID("orbit", shipSymbol)

	cmd := &ship.OrbitShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
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
	if err := s.containerRepo.Insert(ctx, containerEntity, "orbit_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo)
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
	containerID := generateContainerID("refuel", shipSymbol)

	cmd := &ship.RefuelShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
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
	if err := s.containerRepo.Insert(ctx, containerEntity, "refuel_ship"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo)
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
	containerID := generateContainerID("batch_contract_workflow", shipSymbol)

	// Create batch contract workflow command
	cmd := &contract.BatchContractWorkflowCommand{
		ShipSymbol: shipSymbol,
		Iterations: iterations,
		PlayerID:   playerID,
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeContract,
		playerID,
		iterations,
		map[string]interface{}{
			"ship_symbol": shipSymbol,
			"iterations":  iterations,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Insert(ctx, containerEntity, "batch_contract_workflow"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// ScoutTour handles market scouting tour requests (single ship)
func (s *DaemonServer) ScoutTour(ctx context.Context, shipSymbol string, markets []string, iterations, playerID int) (string, error) {
	// Create container ID
	containerID := generateContainerID("scout_tour", shipSymbol)

	// Create scout tour command
	cmd := &scouting.ScoutTourCommand{
		PlayerID:   uint(playerID),
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
	if err := s.containerRepo.Insert(ctx, containerEntity, "scout_tour"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo)
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
	cmd := &scouting.ScoutMarketsCommand{
		PlayerID:     uint(playerID),
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
	scoutResp, ok := response.(*scouting.ScoutMarketsResponse)
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
	containerID := fmt.Sprintf("scout-fleet-assignment-%s-%d", systemSymbol, time.Now().UnixNano())

	// Create assign scouting fleet command (will execute inside container)
	cmd := &scouting.AssignFleetCommand{
		PlayerID:     uint(playerID),
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
	if err := s.containerRepo.Insert(ctx, containerEntity, "scout_fleet_assignment"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo)
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
	query := &ship.ListShipsQuery{
		PlayerID:    playerID,
		AgentSymbol: agentSymbol,
	}

	// Execute via mediator
	response, err := s.mediator.Send(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list ships: %w", err)
	}

	// Convert response
	listResp, ok := response.(*ship.ListShipsResponse)
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
	query := &ship.GetShipQuery{
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
	getResp, ok := response.(*ship.GetShipResponse)
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
	}

	return shipDetail, nil
}

// Utility functions

func generateContainerID(operation, shipSymbol string) string {
	return fmt.Sprintf("%s-%s-%d", operation, shipSymbol, time.Now().UnixNano())
}

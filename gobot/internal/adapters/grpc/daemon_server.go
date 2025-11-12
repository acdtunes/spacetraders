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
	"github.com/andrescamacho/spacetraders-go/internal/application/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"google.golang.org/grpc"
)

// DaemonServer implements the gRPC daemon service
// Handles CLI requests and orchestrates background container operations
type DaemonServer struct {
	mediator common.Mediator
	listener net.Listener
	logRepo  persistence.ContainerLogRepository

	// Container orchestration
	containers   map[string]*ContainerRunner
	containersMu sync.RWMutex

	// Shutdown coordination
	shutdownChan chan os.Signal
	done         chan struct{}
}

// NewDaemonServer creates a new daemon server instance
func NewDaemonServer(mediator common.Mediator, logRepo persistence.ContainerLogRepository, socketPath string) (*DaemonServer, error) {
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
		mediator:     mediator,
		logRepo:      logRepo,
		listener:     listener,
		containers:   make(map[string]*ContainerRunner),
		shutdownChan: make(chan os.Signal, 1),
		done:         make(chan struct{}),
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
	cmd := &navigation.NavigateShipCommand{
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
	)

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo)
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
	// TODO: Implement DockShip command when created
	containerID := generateContainerID("dock", shipSymbol)
	return containerID, fmt.Errorf("DockShip not yet implemented")
}

// OrbitShip handles ship orbit requests
func (s *DaemonServer) OrbitShip(ctx context.Context, shipSymbol string, playerID int) (string, error) {
	// TODO: Implement OrbitShip command when created
	containerID := generateContainerID("orbit", shipSymbol)
	return containerID, fmt.Errorf("OrbitShip not yet implemented")
}

// RefuelShip handles ship refuel requests
func (s *DaemonServer) RefuelShip(ctx context.Context, shipSymbol string, playerID int, units *int) (string, error) {
	// TODO: Implement RefuelShip command when created
	containerID := generateContainerID("refuel", shipSymbol)
	return containerID, fmt.Errorf("RefuelShip not yet implemented")
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

// Utility functions

func generateContainerID(operation, shipSymbol string) string {
	return fmt.Sprintf("%s-%s-%d", operation, shipSymbol, time.Now().UnixNano())
}

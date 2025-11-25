package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/goods/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

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

// StartGoodsFactory creates and starts a goods factory coordinator container
func (s *DaemonServer) StartGoodsFactory(
	ctx context.Context,
	targetGood string,
	systemSymbol string,
	playerID int,
	maxIterations int,
) (*GoodsFactoryResult, error) {
	// Default to 1 iteration if not specified (0 or negative values except -1)
	if maxIterations == 0 {
		maxIterations = 1
	}

	// Generate container ID
	containerID := utils.GenerateContainerID("goods_factory", targetGood)

	// Create factory coordinator command
	cmd := &goodsCmd.RunFactoryCoordinatorCommand{
		PlayerID:      playerID,
		TargetGood:    targetGood,
		SystemSymbol:  systemSymbol,
		ContainerID:   containerID,
		MaxIterations: maxIterations,
	}

	// Create container metadata
	metadata := map[string]interface{}{
		"target_good":    targetGood,
		"system_symbol":  systemSymbol,
		"container_id":   containerID,
		"max_iterations": maxIterations,
	}

	// Create container entity with specified iterations
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerType("goods_factory_coordinator"),
		playerID,
		maxIterations,
		nil, // No parent container
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

package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// ScoutTour handles market scouting tour requests (single ship)
func (s *DaemonServer) ScoutTour(ctx context.Context, containerID string, shipSymbol string, markets []string, iterations, playerID int) (string, error) {
	// Use provided container ID from caller
	config := map[string]interface{}{
		"ship_symbol": shipSymbol,
		"markets":     markets,
		"iterations":  iterations,
	}

	// Create scout tour command from the launch config
	cmd, err := s.buildCommandForType("scout_tour", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create command: %w", err)
	}

	// Create container for this operation. The COMMAND owns the tour count
	// (ScoutTourCommand.Iterations, built from the config above): the container
	// wraps exactly ONE iteration of it (sp-7yej invariant 3). Passing the raw
	// tour count here as well was the scout double-loop defect — the runner
	// re-entered a handler that had already flown its whole budget, so
	// iterations=N flew N×N tours (and 0, which the old wrapper read as "zero
	// runner iterations", completed instantly without scouting at all — the "0
	// tours vanished" divergence). Restart recovery pins the same 1 via the
	// spec's CoordinatorOwnsIterations.
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeScout,
		playerID,
		1,   // one iteration = the whole tour run; the command owns "iterations"
		nil, // No parent container
		config,
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "scout_tour"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// PersistScoutTourWorker persists (but does NOT start) a scout_tour container that
// the scout_post_coordinator manages (sp-cxpq). The persisted config carries a
// coordinator_id and the container a parent link, so daemon restart recovery SKIPS
// it (marks it worker_interrupted, preserving the ship assignment) and leaves
// respawning to the coordinator's reconcile pass — the contract_workflow worker
// pattern. Unlike a contract worker it uses a plain Add (not CreateIfNoActiveWorker):
// many scout tours run concurrently, one per post, so there is no one-worker cap.
func (s *DaemonServer) PersistScoutTourWorker(
	ctx context.Context,
	containerID string,
	shipSymbol string,
	markets []string,
	iterations int,
	scanIntervalSecs int,
	playerID int,
	coordinatorID string,
) error {
	config := map[string]interface{}{
		"ship_symbol":        shipSymbol,
		"markets":            markets,
		"iterations":         iterations,
		"scan_interval_secs": scanIntervalSecs,
		"coordinator_id":     coordinatorID,
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeScout,
		playerID,
		1, // one iteration wraps the whole tour; the command owns "iterations"
		&coordinatorID,
		config,
		nil, // Use default RealClock for production
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "scout_tour"); err != nil {
		return fmt.Errorf("failed to persist scout tour worker: %w", err)
	}
	return nil
}

// StartScoutTour starts a previously persisted scout_tour container (the
// coordinator-managed worker path). Mirrors StartContractWorkflow: load the
// persisted model, rebuild the command from its config, and run it.
func (s *DaemonServer) StartScoutTour(ctx context.Context, containerID string) error {
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

	var config map[string]interface{}
	if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	cmd, err := s.buildCommandForType("scout_tour", config, containerModel.PlayerID, containerModel.ID)
	if err != nil {
		return fmt.Errorf("failed to create command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		1, // one iteration = the whole tour run; the command owns "iterations"
		containerModel.ParentContainerID,
		config,
		nil,
	)

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return nil
}

// PersistScoutRepositionWorker persists (but does NOT start) a scout_reposition
// container the scout_post_coordinator manages (sp-s232). Like a scout_tour worker it
// carries a coordinator_id and a parent link, so daemon restart recovery SKIPS it
// (marks it worker_interrupted, preserving the ship claim) and leaves re-dispatch to
// the coordinator's reconcile pass. It wraps exactly ONE iteration — the whole relay —
// and the coordinator owns re-dispatch (CoordinatorOwnsIterations in the registry).
func (s *DaemonServer) PersistScoutRepositionWorker(
	ctx context.Context,
	containerID string,
	shipSymbol string,
	destinationWaypoint string,
	playerID int,
	coordinatorID string,
) error {
	config := map[string]interface{}{
		"ship_symbol":    shipSymbol,
		"destination":    destinationWaypoint,
		"coordinator_id": coordinatorID,
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeScoutReposition,
		playerID,
		1, // one iteration = the whole relay; the coordinator owns re-dispatch
		&coordinatorID,
		config,
		nil, // Use default RealClock for production
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "scout_reposition"); err != nil {
		return fmt.Errorf("failed to persist scout reposition worker: %w", err)
	}
	return nil
}

// StartScoutReposition starts a previously persisted scout_reposition container (the
// coordinator-managed relay path). Mirrors StartScoutTour: load the persisted model,
// rebuild the command from its config, and run it.
func (s *DaemonServer) StartScoutReposition(ctx context.Context, containerID string) error {
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

	var config map[string]interface{}
	if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	cmd, err := s.buildCommandForType("scout_reposition", config, containerModel.PlayerID, containerModel.ID)
	if err != nil {
		return fmt.Errorf("failed to create command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		1, // one iteration = the whole relay
		containerModel.ParentContainerID,
		config,
		nil,
	)

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return nil
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
		1,   // One-time execution
		nil, // No parent container
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
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Fleet assignment container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

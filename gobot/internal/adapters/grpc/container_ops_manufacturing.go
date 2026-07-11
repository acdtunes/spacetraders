package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	domainMfg "github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// ParallelManufacturingCoordinator creates a parallel task-based manufacturing coordinator
// SINGLETON: Only one coordinator per system is allowed. If one already exists, returns its ID.
func (s *DaemonServer) ParallelManufacturingCoordinator(ctx context.Context, systemSymbol string, playerID int, minPrice int, maxWorkers int, maxPipelines int, maxCollectionPipelines int, minBalance int, strategy string) (string, error) {
	// SINGLETON CHECK: Only one coordinator per system
	existingCoordinator, err := s.containerRepo.FindActiveCoordinatorByTypeAndSystem(
		ctx,
		string(container.ContainerTypeManufacturingCoordinator),
		systemSymbol,
		playerID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to check for existing coordinator: %w", err)
	}
	if existingCoordinator != nil {
		fmt.Printf("SINGLETON: Coordinator already exists for system %s: %s (status: %s)\n",
			systemSymbol, existingCoordinator.ID, existingCoordinator.Status)
		return existingCoordinator.ID, nil
	}

	// Create container ID
	containerID := utils.GenerateContainerID("parallel_manufacturing", systemSymbol)

	// CLEANUP: Stop any orphaned workers from previous crashed coordinators
	// This can happen if a coordinator crashed and left workers running
	// Find ALL manufacturing worker containers for this player and mark them as stopped
	orphanedCount, err := s.containerRepo.StopAllOrphanedManufacturingWorkers(ctx, playerID)
	if err != nil {
		fmt.Printf("Warning: failed to clean orphaned workers: %v\n", err)
	} else if orphanedCount > 0 {
		fmt.Printf("CLEANUP: Stopped %d orphaned worker containers\n", orphanedCount)
	}

	config := map[string]interface{}{
		"system_symbol":            systemSymbol,
		"min_price":                minPrice,
		"max_workers":              maxWorkers,
		"max_pipelines":            maxPipelines,
		"max_collection_pipelines": maxCollectionPipelines,
		"min_balance":              minBalance,
		"container_id":             containerID,
		"mode":                     "parallel_task_based",
		"strategy":                 strategy,
	}

	// Create parallel manufacturing coordinator command from the launch config
	cmd, err := s.buildCommandForType("manufacturing_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create command: %w", err)
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeManufacturingCoordinator, // Use standard coordinator type
		playerID,
		-1,  // Infinite iterations
		nil, // No parent container
		config,
		nil, // Use default RealClock
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "manufacturing_coordinator"); err != nil {
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

// PersistManufacturingTaskWorkerContainer creates (but does NOT start) a manufacturing task worker container in DB
// This is for task-based parallel manufacturing where tasks are already persisted in manufacturing_tasks table
func (s *DaemonServer) PersistManufacturingTaskWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	// Extract fields from either command type (to avoid circular imports)
	var shipSymbol string
	var cmdPlayerID int
	var task *domainMfg.ManufacturingTask
	var coordinatorID string
	var pipelineNumber int
	var productGood string

	switch cmd := command.(type) {
	case *tradingCmd.RunManufacturingTaskWorkerCommand:
		shipSymbol = cmd.ShipSymbol
		cmdPlayerID = cmd.PlayerID
		task = cmd.Task
		coordinatorID = cmd.CoordinatorID
		pipelineNumber = cmd.PipelineNumber
		productGood = cmd.ProductGood
	case *manufacturing.WorkerCommand:
		shipSymbol = cmd.ShipSymbol
		cmdPlayerID = cmd.PlayerID
		task = cmd.Task
		coordinatorID = cmd.CoordinatorID
		pipelineNumber = cmd.PipelineNumber
		productGood = cmd.ProductGood
	default:
		return daemon.ErrInvalidCommandType
	}

	var parentContainerID *string
	if coordinatorID != "" {
		parentContainerID = &coordinatorID
	}

	// Create container entity - store task reference (task is already persisted separately)
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeManufacturingTaskWorker,
		int(playerID),
		1, // Single iteration for worker containers
		parentContainerID,
		map[string]interface{}{
			"ship_symbol":     shipSymbol,
			"player_id":       cmdPlayerID,
			"task_id":         task.ID(),
			"task_type":       string(task.TaskType()),
			"good":            task.Good(),
			"pipeline_id":     task.PipelineID(),
			"coordinator_id":  coordinatorID,
			"pipeline_number": pipelineNumber,
			"product_good":    productGood,
		},
		nil, // Use default RealClock
	)

	// Persist container
	if err := s.containerRepo.Add(ctx, containerEntity, "manufacturing_task_worker"); err != nil {
		return fmt.Errorf("failed to persist manufacturing task worker container: %w", err)
	}

	return nil
}

// StartManufacturingTaskWorkerContainer starts a previously persisted manufacturing task worker container
func (s *DaemonServer) StartManufacturingTaskWorkerContainer(
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

	// Extract fields
	shipSymbol := config["ship_symbol"].(string)
	playerID := int(config["player_id"].(float64))
	taskID := config["task_id"].(string)
	coordinatorID := ""
	if val, ok := config["coordinator_id"].(string); ok {
		coordinatorID = val
	}

	// Load task from task repository
	taskRepo := persistence.NewGormManufacturingTaskRepository(s.db)
	task, err := taskRepo.FindByID(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to load task %s: %w", taskID, err)
	}
	if task == nil {
		return fmt.Errorf("task %s not found", taskID)
	}

	// Create command
	cmd := &tradingCmd.RunManufacturingTaskWorkerCommand{
		ShipSymbol:    shipSymbol,
		Task:          task,
		PlayerID:      playerID,
		ContainerID:   containerID,
		CoordinatorID: coordinatorID,
	}

	// Create container entity from model
	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		1, // Worker containers are single iteration
		nil,
		config,
		nil,
	)

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return nil
}

// CleanupStaleManufacturingWorkers detects and stops manufacturing task workers that
// are RUNNING in DB but have no recent heartbeat (likely crashed without proper cleanup).
// This releases the ships so they can be reassigned to new tasks.
func (s *DaemonServer) CleanupStaleManufacturingWorkers(
	ctx context.Context,
	playerID int,
	staleTimeoutMinutes int,
) (int64, error) {
	if staleTimeoutMinutes <= 0 {
		staleTimeoutMinutes = 10 // Default: 10 minutes
	}

	staleTimeout := time.Duration(staleTimeoutMinutes) * time.Minute

	// First, find stale workers for logging
	staleWorkers, err := s.containerRepo.FindStaleManufacturingWorkers(ctx, playerID, staleTimeout)
	if err != nil {
		return 0, fmt.Errorf("failed to find stale workers: %w", err)
	}

	// Log each orphaned ship with details
	for _, worker := range staleWorkers {
		// Parse config to get ship info
		var config map[string]interface{}
		if err := json.Unmarshal([]byte(worker.Config), &config); err == nil {
			shipSymbol, _ := config["ship_symbol"].(string)
			taskID, _ := config["task_id"].(string)
			taskType, _ := config["task_type"].(string)
			good, _ := config["good"].(string)

			// Calculate how long ago the heartbeat was
			staleDuration := "unknown"
			if worker.HeartbeatAt != nil {
				staleDuration = time.Since(*worker.HeartbeatAt).Round(time.Second).String()
			}

			fmt.Printf("ORPHANED_SHIP: ship=%s container=%s task=%s type=%s good=%s stale_for=%s\n",
				shipSymbol, worker.ID[:12], taskID[:8], taskType, good, staleDuration)
		}
	}

	// Stop stale containers and release their ships
	count, err := s.containerRepo.StopStaleManufacturingWorkers(ctx, playerID, staleTimeout)
	if err != nil {
		return 0, fmt.Errorf("failed to stop stale workers: %w", err)
	}

	if count > 0 {
		fmt.Printf("STALE_WORKER_CLEANUP: Stopped %d stale manufacturing workers for player %d (timeout: %d minutes)\n",
			count, playerID, staleTimeoutMinutes)
	}

	return count, nil
}

// manufacturingConfigKeys enumerates every launch-config key the manufacturing
// coordinators' working-capital reserve knob occupies. resolveManufacturingConfig
// clears this before re-injecting the live value, so a stale persisted copy from a
// prior boot can never shadow the current config.yaml (sp-ts82). Keep in lockstep
// with injectManufacturingConfig and buildGoodsFactoryCoordinatorCommand /
// buildManufacturingCoordinatorCommand's reads.
var manufacturingConfigKeys = []string{
	"working_capital_reserve",
	"working_capital_reserve_treasury_pct",
	"input_price_ceiling_multiplier",
	"input_price_ceiling_disabled",
	"input_rescue_multiplier",
	"input_era_end_price_first",
	"input_sourcing_disabled",
	"chain_pnl_kill_threshold_per_hour",
	"chain_pnl_window_hours",
	"chain_pnl_kill_disabled",
	"planner_stock_disabled",
}

// resolveManufacturingConfig makes config.yaml the single LIVE source of truth for
// the manufacturing coordinators' working-capital reserve (sp-kk61). It clears any
// working_capital_reserve key already in the launch config (a stale copy persisted
// at a prior boot) and re-injects the daemon's boot-loaded value, so the rebuilt
// command reflects the CURRENT config.yaml on every build — creation and restart
// recovery alike, for BOTH goods_factory_coordinator and manufacturing_coordinator.
//
// This is what makes the factory input-buy spend floor operator-reachable: before
// this, no CLI flag or config key populated RunFactoryCoordinatorCommand's
// WorkingCapitalReserve field at all, so every factory was stuck at the
// coordinator's immutable 50k lower bound (sp-agzj) with no way to raise it — the
// gap tonight's 682k factory input buy rode into a fleet-wide 53k treasury trough.
// The clear is essential to honesty: dropping the knob from config.yaml must fall
// back to the 50k floor, and that can only happen if the stale persisted key is
// removed rather than left to shadow the now-absent live value.
func (s *DaemonServer) resolveManufacturingConfig(config map[string]interface{}) {
	for _, key := range manufacturingConfigKeys {
		delete(config, key)
	}
	s.injectManufacturingConfig(config)
}

// injectManufacturingConfig writes the working-capital reserve knob from
// config.yaml (s.manufacturingConfig) into a coordinator container's launch config
// (sp-kk61). Only written when the captain actually set it (non-zero), so an unset
// knob defers to goods_factory_coordinator's documented 50k floor (RULINGS #5) —
// the daemon never hardcodes the operational value. manufacturing_coordinator
// (parallel task-based pipelines) reads the same key so the value is uniformly
// reachable across both command types, though its purchaser has no floor
// enforcement of its own yet (tracked separately). Callers go through
// resolveManufacturingConfig so any stale persisted key is cleared first (sp-ts82).
func (s *DaemonServer) injectManufacturingConfig(config map[string]interface{}) {
	if s.manufacturingConfig.WorkingCapitalReserve != 0 {
		config["working_capital_reserve"] = int(s.manufacturingConfig.WorkingCapitalReserve)
	}
	// sp-yqx4: only when the captain set an override — an unset key defers to the
	// goods_factory build's 40% default (resolveReserveTreasuryPct), so the counter-cyclical
	// floor is ON in production without the captain naming it.
	if s.manufacturingConfig.WorkingCapitalReserveTreasuryPct != 0 {
		config["working_capital_reserve_treasury_pct"] = s.manufacturingConfig.WorkingCapitalReserveTreasuryPct
	}
	// sp-iv65: the ladder-chase input price ceiling. Only written when the captain set a
	// non-zero multiplier — an unset key defers to the goods_factory build's 1.5 default
	// (the guard runs ON in production without the captain naming it, a protective default,
	// RULINGS #5). The disable flag is written only when true, so absent/false keeps the
	// guard on; the clear in resolveManufacturingConfig makes turning it back on take effect.
	if s.manufacturingConfig.InputPriceCeilingMultiplier != 0 {
		config["input_price_ceiling_multiplier"] = s.manufacturingConfig.InputPriceCeilingMultiplier
	}
	if s.manufacturingConfig.InputPriceCeilingDisabled {
		config["input_price_ceiling_disabled"] = true
	}
	// sp-a5j7 Phase 2: supply-first sourcing (the wedx restoration). Only written when the
	// captain set an override — an unset rescue multiplier defers to the goods_factory build's
	// 1.2 default (supply-first runs ON in production without the captain naming it, RULINGS #5).
	// The era-end and disable flags are written only when true, so absent/false keeps supply-first
	// on; the clear in resolveManufacturingConfig makes turning them back off take effect.
	if s.manufacturingConfig.InputRescueMultiplier != 0 {
		config["input_rescue_multiplier"] = s.manufacturingConfig.InputRescueMultiplier
	}
	if s.manufacturingConfig.InputEraEndPriceFirst {
		config["input_era_end_price_first"] = true
	}
	if s.manufacturingConfig.InputSourcingDisabled {
		config["input_sourcing_disabled"] = true
	}
	// sp-rh2z: the chain P&L kill-switch. Only written when the captain set a non-zero
	// threshold/window — an unset key defers to the goods_factory build's 30000/hr + 6h defaults
	// (the kill-switch runs ON in production without the captain naming it, a protective default
	// that can only STOP spend, RULINGS #5). The disable flag is written only when true, so
	// absent/false keeps the switch on; the clear in resolveManufacturingConfig makes turning it
	// back on take effect.
	if s.manufacturingConfig.ChainPnLKillThresholdPerHour != 0 {
		config["chain_pnl_kill_threshold_per_hour"] = s.manufacturingConfig.ChainPnLKillThresholdPerHour
	}
	if s.manufacturingConfig.ChainPnLWindowHours != 0 {
		config["chain_pnl_window_hours"] = s.manufacturingConfig.ChainPnLWindowHours
	}
	if s.manufacturingConfig.ChainPnLKillDisabled {
		config["chain_pnl_kill_disabled"] = true
	}
	if s.manufacturingConfig.PlannerStockDisabled {
		config["planner_stock_disabled"] = true
	}
}

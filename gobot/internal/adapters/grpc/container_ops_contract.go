package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// BatchContractWorkflow handles batch contract workflow requests.
// It runs a single contract to completion; continuous, multi-contract operation
// is served by the contract fleet coordinator (see ContractFleetCoordinator /
// the `contract start` CLI verb).
func (s *DaemonServer) BatchContractWorkflow(ctx context.Context, shipSymbol string, playerID int) (string, error) {
	// Create container ID
	containerID := utils.GenerateContainerID("batch_contract_workflow", shipSymbol)

	// Delegate to ContractWorkflow (single iteration)
	return s.ContractWorkflow(ctx, containerID, shipSymbol, playerID, "")
}

// ContractWorkflow creates and starts a contract workflow container
func (s *DaemonServer) ContractWorkflow(
	ctx context.Context,
	containerID string,
	shipSymbol string,
	playerID int,
	coordinatorID string,
) (string, error) {
	// Persist container to DB
	if err := s.PersistContractWorkflow(ctx, containerID, shipSymbol, playerID, coordinatorID); err != nil {
		return "", err
	}

	// Start the container
	if err := s.StartContractWorkflow(ctx, containerID); err != nil {
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
		&coordinatorID, // Link to parent coordinator container
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

	// Create command
	cmd, err := s.buildCommandForType("contract_workflow", config, containerModel.PlayerID, containerModel.ID)
	if err != nil {
		return fmt.Errorf("failed to create command: %w", err)
	}

	// Create container entity from model
	// Worker containers always have 1 iteration
	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		1,   // Worker containers are single iteration
		nil, // No parent container
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

// ContractFleetCoordinator creates a fleet coordinator for multi-ship contract operations
// Ships are discovered dynamically - no pre-assignment needed.
//
// dedicatedShips/standbyStations (sp-snmb) are the operator's optional
// --dedicated-ships/--standby-stations CLI parameters, threaded straight into
// the persisted launch config so they survive restart recovery unchanged
// (buildContractFleetCoordinatorCommand reads them back via
// configReader.OptionalStringSlice). Both are nil/empty when the operator
// runs a plain, non-dedicated coordinator - the feature is opt-in.
func (s *DaemonServer) ContractFleetCoordinator(ctx context.Context, shipSymbols []string, playerID int, dedicatedShips []string, standbyStations []string) (string, error) {
	// Create container ID using player ID instead of ship symbol (no ships pre-assigned)
	containerID := utils.GenerateContainerID("contract_fleet_coordinator", fmt.Sprintf("player-%d", playerID))

	// No ship symbols metadata needed (dynamic discovery - no pre-assignment)
	var shipSymbolsInterface []interface{}
	config := map[string]interface{}{
		"ship_symbols":     shipSymbolsInterface,
		"container_id":     containerID,
		"dedicated_ships":  dedicatedShips,
		"standby_stations": standbyStations,
	}
	// The idle-arb harvest knobs are NOT injected here. buildCommandForType
	// resolves them from LIVE config.yaml on every coordinator build — creation
	// AND restart recovery alike (sp-ts82) — so config.yaml is the single source
	// of truth and a retune (config edit + daemon restart) actually reaches a
	// recovered coordinator. The persisted idle_arb_* keys are dead.

	// Create contract fleet coordinator command from the launch config
	cmd, err := s.buildCommandForType("contract_fleet_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create command: %w", err)
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeContractFleetCoordinator,
		playerID,
		-1,  // Infinite iterations
		nil, // No parent container
		config,
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "contract_fleet_coordinator"); err != nil {
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

// idleArbConfigKeys enumerates every launch-config key the idle-arb harvest
// knobs occupy. resolveIdleArbConfig clears these before re-injecting the live
// values, so a stale persisted copy from a prior boot can never shadow the
// current config.yaml (sp-ts82). Keep in lockstep with injectIdleArbConfig and
// buildContractFleetCoordinatorCommand's reads.
var idleArbConfigKeys = []string{
	"idle_arb_disabled",
	"idle_arb_reserve_hulls",
	"idle_arb_hub_radius",
	"idle_arb_leash_radius",
	"idle_arb_max_leg_secs",
	"idle_arb_max_spend",
	"idle_arb_min_margin",
	"idle_arb_margin_verify_pct",
	"idle_arb_interval_secs",
	"idle_arb_blacklist",
}

// resolveIdleArbConfig makes config.yaml the single LIVE source of truth for the
// contract coordinator's idle-arb harvest knobs (sp-ts82). It clears any idle_arb_*
// keys already in the launch config (stale copies persisted at a prior boot) and
// re-injects the daemon's boot-loaded values, so the rebuilt command reflects the
// CURRENT config.yaml on every build — creation and restart recovery alike.
//
// This is what finally makes the documented retune path (edit config.yaml +
// restart daemon) take effect on a RECOVERED coordinator. Before it, recovery
// re-adopted the stale persisted knobs untouched and the retune silently no-op'd
// (the sp-nw9v incident: a leash-150 retune ran leash-80 for hours). No coordinator
// recreate is ever needed for these knobs now. The clear is essential to honesty:
// dropping a knob from config.yaml must fall back to the WithDefaults default, and
// that can only happen if the stale persisted key is removed rather than left to
// shadow the now-absent live value.
func (s *DaemonServer) resolveIdleArbConfig(config map[string]interface{}) {
	for _, key := range idleArbConfigKeys {
		delete(config, key)
	}
	s.injectIdleArbConfig(config)
}

// injectIdleArbConfig writes the idle-arb harvest knobs from config.yaml
// (s.contractConfig.IdleArb) into a coordinator container's launch config
// (sp-1z2h / sp-uohe). Only keys the captain actually set (non-zero) are
// written, so an unset knob defers to the contract package's documented
// defaults via IdleArbConfig.WithDefaults — the daemon never hardcodes the
// operational values (RULINGS #5). The blacklist is special: a nil (absent)
// list is omitted so the default [ELECTRONICS] applies, while an explicit
// (non-nil) list — including an empty one that disables the blacklist — is
// injected verbatim so a captain's config whitelist-flip takes effect on the
// next daemon start with no code change. Callers go through resolveIdleArbConfig
// so any stale persisted keys are cleared first (sp-ts82).
func (s *DaemonServer) injectIdleArbConfig(config map[string]interface{}) {
	ia := s.contractConfig.IdleArb
	if ia.Disabled {
		config["idle_arb_disabled"] = true
	}
	if ia.ReserveHulls != 0 {
		config["idle_arb_reserve_hulls"] = ia.ReserveHulls
	}
	if ia.HubRadius != 0 {
		config["idle_arb_hub_radius"] = ia.HubRadius
	}
	if ia.LeashRadius != 0 {
		config["idle_arb_leash_radius"] = ia.LeashRadius
	}
	if ia.MaxLegSeconds != 0 {
		config["idle_arb_max_leg_secs"] = ia.MaxLegSeconds
	}
	if ia.MaxSpend != 0 {
		config["idle_arb_max_spend"] = ia.MaxSpend
	}
	if ia.MinMargin != 0 {
		config["idle_arb_min_margin"] = ia.MinMargin
	}
	if ia.MarginVerifyPct != 0 {
		config["idle_arb_margin_verify_pct"] = ia.MarginVerifyPct
	}
	if ia.IntervalSeconds != 0 {
		config["idle_arb_interval_secs"] = ia.IntervalSeconds
	}
	if ia.Blacklist != nil {
		config["idle_arb_blacklist"] = ia.Blacklist
	}
}

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
//
// iterations selects the mode (sp-ehg9): 1 (the default) runs a SINGLE contract
// to completion, byte-identical to today; -1 runs the CONTINUOUS single-hull
// contract loop — re-negotiate + run contracts until stopped — for the bootstrap
// command frigate during the pre-hauler window. Multi-hull continuous operation
// is still served by the contract fleet coordinator (`contract start`). The
// bootstrap INCOME phase calls this with iterations=-1 and stops the returned
// container (StopContainer) at the first-hauler pivot.
func (s *DaemonServer) BatchContractWorkflow(ctx context.Context, shipSymbol string, playerID int, iterations int) (string, error) {
	containerID := utils.GenerateContainerID("batch_contract_workflow", shipSymbol)

	return s.ContractWorkflow(ctx, containerID, shipSymbol, playerID, "", iterations)
}

// ContractWorkflow creates and starts a contract workflow container. iterations
// is 1 for a single-shot worker (the coordinator-spawned default) or -1 for a
// continuous single-hull loop (sp-ehg9).
func (s *DaemonServer) ContractWorkflow(
	ctx context.Context,
	containerID string,
	shipSymbol string,
	playerID int,
	coordinatorID string,
	iterations int,
) (string, error) {
	// Persist container to DB
	if err := s.PersistContractWorkflow(ctx, containerID, shipSymbol, playerID, coordinatorID, iterations); err != nil {
		return "", err
	}

	// Start the container
	if err := s.StartContractWorkflow(ctx, containerID); err != nil {
		return "", err
	}

	return containerID, nil
}

// PersistContractWorkflow creates a contract workflow container in DB (does NOT
// start it). iterations is the container's work budget: 1 = single contract
// (the coordinator-owned worker default), -1 = continuous loop (sp-ehg9). It is
// stored BOTH on the entity (drives the live runner) AND in the launch config
// ("iterations"), because recoverContainer rebuilds the entity's maxIterations
// from config on a daemon restart and buildContractWorkflowCommand rebuilds the
// command's Loop flag from it — so a -1 loop resumes as a loop (recovery-safe).
func (s *DaemonServer) PersistContractWorkflow(
	ctx context.Context,
	containerID string,
	shipSymbol string,
	playerID int,
	coordinatorID string,
	iterations int,
) error {
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeContractWorkflow,
		playerID,
		iterations,
		&coordinatorID, // Link to parent coordinator container
		map[string]interface{}{
			"ship_symbol":    shipSymbol,
			"coordinator_id": coordinatorID,
			"iterations":     iterations,
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

	// Iteration budget from the persisted launch config: 1 (or absent) = a
	// single-shot worker; -1 = the continuous single-hull loop (sp-ehg9). Read
	// from config so the fresh-start entity matches what recoverContainer rebuilds
	// on a restart — otherwise a loop container would start looping but restart as
	// single-shot. A coordinator-spawned worker never sets "iterations", so it
	// stays 1 (byte-identical).
	iterations := 1
	if v, ok := intValue(config["iterations"]); ok {
		iterations = v
	}

	// Create container entity from model
	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		iterations,
		nil, // No parent container
		config,
		nil,
	)

	s.startContainerRunner(containerEntity, cmd, containerID, "Container")

	return nil
}

// dedicatedShipsSeededConfigKey is the container-config marker recording that a
// contract coordinator has applied its --dedicated-ships launch seed once (sp-86vb).
// DedicatedFleetSeedConfigPersister writes it after the first seed;
// buildContractFleetCoordinatorCommand reads it back on restart. Keep the two in
// lockstep — the read and the write must name the same key.
const dedicatedShipsSeededConfigKey = "dedicated_ships_seeded"

// DedicatedFleetSeedConfigPersister backs the contract coordinator's
// contractCmd.DedicatedFleetSeedMarker with the container config (sp-86vb),
// mirroring ArbCostConfigPersister (sp-dkj7 / RULINGS #2). After the coordinator
// applies its --dedicated-ships seed on first boot it merges
// dedicated_ships_seeded=true into the SAME persisted config the recovery rebuild
// reads (buildContractFleetCoordinatorCommand), so a daemon restart reloads the
// marker and SKIPS the seed replay that would otherwise re-stamp "contract" onto a
// hull the operator deliberately `fleet remove`d. It is a read-modify-write of the
// config map guarded to the single config column; the config has no other writer
// during a coordinator run, so it never clobbers the status/heartbeat columns the
// runner updates concurrently.
type DedicatedFleetSeedConfigPersister struct {
	containerRepo *persistence.ContainerRepositoryGORM
}

// NewDedicatedFleetSeedConfigPersister wires the config-backed first-boot marker for
// the contract fleet coordinator.
func NewDedicatedFleetSeedConfigPersister(containerRepo *persistence.ContainerRepositoryGORM) *DedicatedFleetSeedConfigPersister {
	return &DedicatedFleetSeedConfigPersister{containerRepo: containerRepo}
}

// MarkDedicatedShipsSeeded merges dedicated_ships_seeded=true into the container's
// persisted config. It reads the current config, sets the key, and writes just the
// config column — preserving every launch knob (dedicated_ships/standby_stations)
// the rebuild also needs. A missing container row (already terminalized) is a no-op
// error the caller logs and swallows: the seed has already been applied, and this is
// restart-resilience of the removal, never a spend guard.
func (p *DedicatedFleetSeedConfigPersister) MarkDedicatedShipsSeeded(ctx context.Context, containerID string, playerID int) error {
	model, err := p.containerRepo.Get(ctx, containerID, playerID)
	if err != nil {
		return fmt.Errorf("load container %s to persist dedicated-ships seeded marker: %w", containerID, err)
	}
	if model == nil {
		return fmt.Errorf("container %s not found - cannot persist dedicated-ships seeded marker", containerID)
	}

	config := map[string]interface{}{}
	if model.Config != "" {
		if uerr := json.Unmarshal([]byte(model.Config), &config); uerr != nil {
			return fmt.Errorf("deserialize container %s config to persist seeded marker: %w", containerID, uerr)
		}
	}
	config[dedicatedShipsSeededConfigKey] = true

	merged, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("serialize container %s config after merging seeded marker: %w", containerID, err)
	}
	return p.containerRepo.UpdateContainerConfig(ctx, containerID, playerID, string(merged))
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

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeContractFleetCoordinator,
		playerID,
		-1,  // Infinite iterations
		nil, // No parent container
		config,
		nil, // Use default RealClock for production
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "contract_fleet_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	s.startContainerRunner(containerEntity, cmd, containerID, "Container")

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
	"idle_arb_recovery_hold_secs",
	"idle_arb_blacklist",
	"idle_arb_min_net_profit",
	"idle_arb_net_profit_pct",
	"idle_arb_fuel_cost_per_unit",
}

// resolveIdleArbConfig makes config.yaml the single LIVE source of truth for the
// contract coordinator's idle-arb harvest knobs (sp-ts82). It clears any idle_arb_*
// keys already in the launch config (stale copies persisted at a prior boot) and
// re-injects the daemon's boot-loaded values, so the rebuilt command reflects the
// CURRENT config.yaml on every build — creation and restart recovery alike.
//
// This makes the documented retune path (edit config.yaml + restart daemon) take
// effect on a RECOVERED coordinator without ever needing a coordinator recreate.
// The clear is essential to honesty: dropping a knob from config.yaml must fall
// back to the WithDefaults default, and that can only happen if the stale
// persisted key is removed rather than left to shadow the now-absent live value.
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
	if ia.RecoveryHoldSeconds != 0 {
		config["idle_arb_recovery_hold_secs"] = ia.RecoveryHoldSeconds
	}
	if ia.Blacklist != nil {
		config["idle_arb_blacklist"] = ia.Blacklist
	}
	if ia.MinNetProfitPerUnit != 0 {
		config["idle_arb_min_net_profit"] = ia.MinNetProfitPerUnit
	}
	if ia.NetProfitPct != 0 {
		config["idle_arb_net_profit_pct"] = ia.NetProfitPct
	}
	if ia.FuelCostPerUnit != 0 {
		config["idle_arb_fuel_cost_per_unit"] = ia.FuelCostPerUnit
	}
}

// autoLiquidationConfigKeys enumerates every launch-config key the parked-hull
// auto-liquidation knobs occupy (sp-39oi). resolveAutoLiquidationConfig clears these
// before re-injecting the live values, so a stale persisted copy can never shadow the
// current config.yaml (sp-ts82). Keep in lockstep with injectAutoLiquidationConfig and
// buildContractFleetCoordinatorCommand's reads.
var autoLiquidationConfigKeys = []string{
	"auto_liquidation_disabled",
	"liquidation_min_jettison_value",
}

// resolveAutoLiquidationConfig makes config.yaml the single LIVE source of truth for the
// contract coordinator's auto-liquidation knobs (sp-39oi, mirroring resolveIdleArbConfig).
// It clears any stale auto_liquidation keys and re-injects the daemon's boot-loaded values,
// so a config edit + restart retunes even a recovered coordinator.
func (s *DaemonServer) resolveAutoLiquidationConfig(config map[string]interface{}) {
	for _, key := range autoLiquidationConfigKeys {
		delete(config, key)
	}
	s.injectAutoLiquidationConfig(config)
}

// injectAutoLiquidationConfig writes the [auto_liquidation] knobs from config.yaml
// (s.contractConfig.AutoLiquidation) into a coordinator container's launch config. Disabled
// is inverted to auto_liquidation_disabled, written ONLY when the coordinator is off: an
// absent key therefore reads as enabled, so the default-ON intent survives both a fresh
// start and a recovery from an old config that predates the key. min_jettison_value is
// written only when the captain set a positive floor, so an unset knob defers to the
// documented default (0 = jettison OFF, RULINGS #5).
func (s *DaemonServer) injectAutoLiquidationConfig(config map[string]interface{}) {
	al := s.contractConfig.AutoLiquidation
	if al.Disabled {
		config["auto_liquidation_disabled"] = true
	}
	if al.MinJettisonValue != 0 {
		config["liquidation_min_jettison_value"] = al.MinJettisonValue
	}
}

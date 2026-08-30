package grpc

import (
	"context"
	"fmt"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// This file wires the captain bootstrap coordinator's launch path + live-config resolution +
// recovery build (sp-3nbe). The launch trigger mirrors FleetAutosizerCoordinator: identity-only
// launch config → buildCommandForType (the single builder shared by creation and recovery) →
// NewContainer with iterations=-1 for the infinite reconcile loop → Add → runner →
// registerContainer → go Start. All tuning ([bootstrap]) resolves LIVE from config.yaml inside
// buildCommandForType, so a config edit + restart retunes even a recovered coordinator.

// BootstrapCoordinator starts the standing captain bootstrap coordinator (sp-3nbe): a recovery-safe
// container that drives a cold agent through the cold-start arc to the jump gate, behind the
// fail-closed money guards. LIVE BY DEFAULT once launched.
func (s *DaemonServer) BootstrapCoordinator(ctx context.Context, playerID int, agentSymbol string) (string, error) {
	containerID := utils.GenerateContainerID("bootstrap", fmt.Sprintf("player-%d", playerID))

	// Identity only — the [bootstrap] keys are injected by resolveBootstrapConfig inside
	// buildCommandForType, the single injection point shared by creation and recovery.
	config := map[string]interface{}{
		"container_id": containerID,
		"agent_symbol": agentSymbol,
	}

	cmd, err := s.buildCommandForType("bootstrap", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create bootstrap command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeBootstrapCoordinator,
		playerID,
		// Infinite budget: Handle() owns the whole reconcile loop and, unlike the other standing
		// coordinators, RETURNS at the terminal EXPANSION exit — the runner completes the container
		// on that response's RunTerminal report (never on this budget), and paces any non-terminal
		// return at the bootstrap tick (standingIterationFloors) so a returning handler can never
		// spin the runner loop.
		-1,
		nil, // No parent container
		config,
		nil,
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "bootstrap"); err != nil {
		return "", fmt.Errorf("failed to persist bootstrap container: %w", err)
	}

	s.startContainerRunner(containerEntity, cmd, containerID, "Bootstrap container")

	return containerID, nil
}

// bootstrapConfigKeys enumerates the LIVE [bootstrap] launch-config keys. resolveBootstrapConfig
// clears these before re-injecting the live values, so a stale persisted copy from a prior boot can
// never shadow the current config.yaml (the sp-ts82 live-config discipline). Keep in lockstep with
// injectBootstrapConfig and buildBootstrapCommand's reads. container_id and agent_symbol are
// IDENTITY (set once at creation) and are deliberately NOT in this list — they must survive a rebuild.
var bootstrapConfigKeys = []string{
	"bootstrap_disabled",
	"bootstrap_tick_secs",
	"bootstrap_contract_start_treasury_threshold",
	"bootstrap_hauler_target",
	"bootstrap_gate_worker_target",
}

// resolveBootstrapConfig makes config.yaml the single LIVE source of truth for the bootstrap
// coordinator's knobs (sp-3nbe, mirroring resolveFleetAutosizerConfig). It clears any bootstrap_*
// live keys already in the launch config (stale copies persisted at a prior boot) and re-injects
// the daemon's boot-loaded values, so the rebuilt command reflects the CURRENT config.yaml on every
// build — creation and restart recovery alike.
func (s *DaemonServer) resolveBootstrapConfig(config map[string]interface{}) {
	for _, key := range bootstrapConfigKeys {
		delete(config, key)
	}
	s.injectBootstrapConfig(config)
}

// injectBootstrapConfig writes the [bootstrap] keys from config.yaml (s.bootstrapConfig) into a
// coordinator container's launch config. Only keys the captain actually set (non-zero) are written,
// so an unset cadence defers to the coordinator's own documented default. bootstrap_disabled
// is written ONLY when the coordinator is off: an absent key therefore reads as ENABLED, so the
// LIVE-BY-DEFAULT intent survives both a fresh start and a recovery from an old config that predates
// the key (Admiral: no dark-shipping).
func (s *DaemonServer) injectBootstrapConfig(config map[string]interface{}) {
	b := s.bootstrapConfig
	if b.BootstrapDisabled {
		config["bootstrap_disabled"] = true
	}
	if b.TickSeconds != 0 {
		config["bootstrap_tick_secs"] = b.TickSeconds
	}
	if b.ContractStartTreasuryThreshold != 0 {
		config["bootstrap_contract_start_treasury_threshold"] = b.ContractStartTreasuryThreshold
	}
	if b.HaulerTarget != 0 {
		config["bootstrap_hauler_target"] = b.HaulerTarget
	}
	if b.GateWorkerTarget != 0 {
		config["bootstrap_gate_worker_target"] = b.GateWorkerTarget
	}
}

// buildBootstrapCommand rebuilds the standing bootstrap command from a persisted launch
// config so a daemon restart re-adopts it. The [bootstrap] keys are resolved LIVE from config.yaml
// just before this runs (resolveBootstrapConfig in buildCommandForType), so the persisted
// bootstrap_* keys are transient — the reads below see the current config.yaml. Disabled is
// reconstructed from bootstrap_disabled directly (absent = false = ENABLED, so LIVE BY DEFAULT
// survives a recovery from an old config that predates the key).
func buildBootstrapCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &bootstrapCmd.RunBootstrapCoordinatorCommand{
		PlayerID:    playerID,
		ContainerID: containerID,
		AgentSymbol: cfg.OptionalString("agent_symbol"),

		Disabled:                       cfg.OptionalBool("bootstrap_disabled"),
		TickIntervalSecs:               cfg.OptionalInt("bootstrap_tick_secs", 0),
		ContractStartTreasuryThreshold: cfg.OptionalInt("bootstrap_contract_start_treasury_threshold", 0),
		HaulerTarget:                   cfg.OptionalInt("bootstrap_hauler_target", 0),
		GateWorkerTarget:               cfg.OptionalInt("bootstrap_gate_worker_target", 0),
	}
}

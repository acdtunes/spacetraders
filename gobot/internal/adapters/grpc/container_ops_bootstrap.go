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
// container that drives a cold agent through the cold-start arc to the jump gate. Slice 1 runs the
// DATA phase (probes → target, scout every market) behind the fail-closed money-guard. LIVE BY
// DEFAULT once launched. dryRun (the CLI --dry-run) launches it in watch mode: it evaluates + logs
// every decision but acts on nothing.
func (s *DaemonServer) BootstrapCoordinator(ctx context.Context, playerID int, agentSymbol string, dryRun bool) (string, error) {
	containerID := utils.GenerateContainerID("bootstrap", fmt.Sprintf("player-%d", playerID))

	// Identity only — the [bootstrap] knobs are injected by resolveBootstrapConfig inside
	// buildCommandForType, the single injection point shared by creation and recovery.
	// bootstrap_launch_dry_run is an IDENTITY flag: the launch-time --dry-run decision, persisted so
	// a recovered container stays in watch mode until it is stopped and relaunched (it is
	// deliberately NOT a live-config key).
	config := map[string]interface{}{
		"container_id": containerID,
		"agent_symbol": agentSymbol,
	}
	if dryRun {
		config["bootstrap_launch_dry_run"] = true
	}

	cmd, err := s.buildCommandForType("bootstrap", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create bootstrap command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeBootstrapCoordinator,
		playerID,
		-1,  // Infinite iterations (reconcile loop) — NOT a CoordinatorOwnsIterations type
		nil, // No parent container
		config,
		nil,
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "bootstrap"); err != nil {
		return "", fmt.Errorf("failed to persist bootstrap container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Bootstrap container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// bootstrapConfigKeys enumerates the LIVE [bootstrap] launch-config keys. resolveBootstrapConfig
// clears these before re-injecting the live values, so a stale persisted copy from a prior boot can
// never shadow the current config.yaml (the sp-ts82 live-config discipline). Keep in lockstep with
// injectBootstrapConfig and buildBootstrapCommand's reads. container_id, agent_symbol and
// bootstrap_launch_dry_run are IDENTITY (set once at creation) and are deliberately NOT in this list
// — they must survive a rebuild.
var bootstrapConfigKeys = []string{
	"bootstrap_disabled",
	"bootstrap_dry_run",
	"bootstrap_probe_target",
	"bootstrap_coverage_bar",
	"bootstrap_reserve_margin",
	"bootstrap_tick_secs",
	"bootstrap_probe_ship_type",
	"bootstrap_hauler_target",
	"bootstrap_income_bar",
	"bootstrap_min_contract_earners",
	"bootstrap_hauler_ship_type",
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

// injectBootstrapConfig writes the [bootstrap] knobs from config.yaml (s.bootstrapConfig) into a
// coordinator container's launch config. Only keys the captain actually set (non-zero) are written,
// so an unset knob defers to the coordinator's own documented default (RULINGS #5). bootstrap_disabled
// is written ONLY when the coordinator is off: an absent key therefore reads as ENABLED, so the
// LIVE-BY-DEFAULT intent survives both a fresh start and a recovery from an old config that predates
// the key (Admiral: no dark-shipping).
func (s *DaemonServer) injectBootstrapConfig(config map[string]interface{}) {
	b := s.bootstrapConfig
	if b.BootstrapDisabled {
		config["bootstrap_disabled"] = true
	}
	if b.DryRun {
		config["bootstrap_dry_run"] = true
	}
	if b.ProbeTarget != 0 {
		config["bootstrap_probe_target"] = b.ProbeTarget
	}
	if b.CoverageBar != 0 {
		config["bootstrap_coverage_bar"] = b.CoverageBar
	}
	if b.ReserveMargin != 0 {
		config["bootstrap_reserve_margin"] = b.ReserveMargin
	}
	if b.TickSeconds != 0 {
		config["bootstrap_tick_secs"] = b.TickSeconds
	}
	if b.ProbeShipType != "" {
		config["bootstrap_probe_ship_type"] = b.ProbeShipType
	}
	if b.HaulerTarget != 0 {
		config["bootstrap_hauler_target"] = b.HaulerTarget
	}
	if b.IncomeBar != 0 {
		config["bootstrap_income_bar"] = b.IncomeBar
	}
	if b.MinContractEarners != 0 {
		config["bootstrap_min_contract_earners"] = b.MinContractEarners
	}
	if b.HaulerShipType != "" {
		config["bootstrap_hauler_ship_type"] = b.HaulerShipType
	}
}

// buildBootstrapCommand rebuilds the standing bootstrap command (sp-3nbe) from a persisted launch
// config so a daemon restart re-adopts it. The [bootstrap] knobs are resolved LIVE from config.yaml
// just before this runs (resolveBootstrapConfig in buildCommandForType), so the persisted
// bootstrap_* keys are transient — the reads below see the current config.yaml. Disabled is
// reconstructed from bootstrap_disabled directly (absent = false = ENABLED, so LIVE BY DEFAULT
// survives a recovery from an old config that predates the key). DryRun ORs the live config knob
// with the persisted launch-time --dry-run flag.
func buildBootstrapCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &bootstrapCmd.RunBootstrapCoordinatorCommand{
		PlayerID:    playerID,
		ContainerID: containerID,
		AgentSymbol: cfg.OptionalString("agent_symbol"),

		Disabled: cfg.OptionalBool("bootstrap_disabled"),
		DryRun:   cfg.OptionalBool("bootstrap_dry_run") || cfg.OptionalBool("bootstrap_launch_dry_run"),

		TickIntervalSecs: cfg.OptionalInt("bootstrap_tick_secs", 0),
		ProbeTarget:      cfg.OptionalInt("bootstrap_probe_target", 0),
		CoverageBar:      cfg.OptionalFloat("bootstrap_coverage_bar", 0),
		ReserveMargin:    cfg.OptionalFloat("bootstrap_reserve_margin", 0),
		ProbeShipType:    cfg.OptionalString("bootstrap_probe_ship_type"),

		HaulerTarget:       cfg.OptionalInt("bootstrap_hauler_target", 0),
		IncomeBar:          cfg.OptionalFloat("bootstrap_income_bar", 0),
		MinContractEarners: cfg.OptionalInt("bootstrap_min_contract_earners", 0),
		HaulerShipType:     cfg.OptionalString("bootstrap_hauler_ship_type"),
	}
}

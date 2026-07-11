package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// SitingCoordinator starts the standing factory-siting coordinator (sp-vdld): a
// recovery-safe container that scans/scores/sizes the factory-chain portfolio each slow
// tick and launches/retires goods_factory chains through the existing guard stack. It
// mirrors TradeFleetCoordinator's shape exactly (identity-only launch config →
// buildCommandForType, the single builder shared by creation and recovery → NewContainer
// with iterations=-1 for the infinite reconcile loop → Add → runner → registerContainer →
// go Start). All the tuning (the [manufacturing.siting] weights/caps) is resolved LIVE from
// config.yaml inside buildCommandForType (resolveSitingConfig), so the launch config carries
// only identity here; a config edit + restart retunes even a recovered coordinator.
func (s *DaemonServer) SitingCoordinator(ctx context.Context, playerID int, agentSymbol string) (string, error) {
	containerID := utils.GenerateContainerID("siting_coordinator", fmt.Sprintf("player-%d", playerID))

	// Identity only — the [manufacturing.siting] knobs are injected by resolveSitingConfig
	// inside buildCommandForType, the single injection point shared by creation and recovery.
	config := map[string]interface{}{
		"container_id": containerID,
		"agent_symbol": agentSymbol,
	}

	cmd, err := s.buildCommandForType("siting_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create siting coordinator command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeSitingCoordinator,
		playerID,
		-1,  // Infinite iterations (reconcile loop) — NOT a CoordinatorOwnsIterations type
		nil, // No parent container
		config,
		nil,
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "siting_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist siting coordinator container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Siting coordinator container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// sitingConfigKeys enumerates every launch-config key the [manufacturing.siting] knobs
// occupy. resolveSitingConfig clears these before re-injecting the live values, so a stale
// persisted copy from a prior boot can never shadow the current config.yaml (the sp-ts82
// live-config discipline). Keep in lockstep with injectSitingConfig and
// buildSitingCoordinatorCommand's reads. container_id and agent_symbol are IDENTITY (set
// once at creation) and are deliberately NOT in this list — they must survive a rebuild.
var sitingConfigKeys = []string{
	"siting_disabled",
	"siting_dry_run",
	"siting_tick_secs",
	"siting_top_k",
	"siting_workers_per_chain",
	"siting_freshness_max_secs",
	"siting_emit_staleness_secs",
	"siting_weight_tour_alignment",
	"siting_weight_input_competition",
	"siting_weight_staleness",
	"siting_weight_worker_reachability",
	"siting_max_chains_per_system",
	"siting_max_chains_per_input_market",
	"siting_retire_hysteresis_ticks",
	"siting_effect_selfcheck_ticks",
	"siting_scout_demand_cooldown_secs",
}

// resolveSitingConfig makes config.yaml the single LIVE source of truth for the siting
// coordinator's knobs (sp-vdld, mirroring resolveTradeFleetConfig). It clears any siting_*
// keys already in the launch config (stale copies persisted at a prior boot) and re-injects
// the daemon's boot-loaded values, so the rebuilt command reflects the CURRENT config.yaml on
// every build — creation and restart recovery alike. The clear is what lets dropping a knob
// from config.yaml fall back to the coordinator's own default rather than being shadowed by
// the now-absent live value.
func (s *DaemonServer) resolveSitingConfig(config map[string]interface{}) {
	for _, key := range sitingConfigKeys {
		delete(config, key)
	}
	s.injectSitingConfig(config)
}

// injectSitingConfig writes the [manufacturing.siting] knobs from config.yaml
// (s.manufacturingConfig.Siting) into a coordinator container's launch config. Only keys the
// captain actually set (non-zero) are written, so an unset knob defers to the coordinator's
// own documented default (RULINGS #5 — the daemon never hardcodes the operational values).
// siting_disabled is written ONLY when the coordinator is off: an absent key therefore reads
// as enabled, so the LIVE-BY-DEFAULT intent survives both a fresh start and a recovery from an
// old config that predates the key (Admiral: no dark-shipping).
func (s *DaemonServer) injectSitingConfig(config map[string]interface{}) {
	sc := s.manufacturingConfig.Siting
	if sc.SitingDisabled {
		config["siting_disabled"] = true
	}
	if sc.DryRun {
		config["siting_dry_run"] = true
	}
	if sc.TickIntervalSecs != 0 {
		config["siting_tick_secs"] = sc.TickIntervalSecs
	}
	if sc.TopK != 0 {
		config["siting_top_k"] = sc.TopK
	}
	if sc.WorkersPerChain != 0 {
		config["siting_workers_per_chain"] = sc.WorkersPerChain
	}
	if sc.FreshnessMaxSecs != 0 {
		config["siting_freshness_max_secs"] = sc.FreshnessMaxSecs
	}
	if sc.EmitStalenessSecs != 0 {
		config["siting_emit_staleness_secs"] = sc.EmitStalenessSecs
	}
	if sc.WeightTourAlignment != 0 {
		config["siting_weight_tour_alignment"] = sc.WeightTourAlignment
	}
	if sc.WeightInputCompetition != 0 {
		config["siting_weight_input_competition"] = sc.WeightInputCompetition
	}
	if sc.WeightStaleness != 0 {
		config["siting_weight_staleness"] = sc.WeightStaleness
	}
	if sc.WeightWorkerReachability != 0 {
		config["siting_weight_worker_reachability"] = sc.WeightWorkerReachability
	}
	if sc.MaxChainsPerSystem != 0 {
		config["siting_max_chains_per_system"] = sc.MaxChainsPerSystem
	}
	if sc.MaxChainsPerInputMarket != 0 {
		config["siting_max_chains_per_input_market"] = sc.MaxChainsPerInputMarket
	}
	if sc.RetireHysteresisTicks != 0 {
		config["siting_retire_hysteresis_ticks"] = sc.RetireHysteresisTicks
	}
	if sc.EffectSelfcheckTicks != 0 {
		config["siting_effect_selfcheck_ticks"] = sc.EffectSelfcheckTicks
	}
	if sc.ScoutDemandCooldownSecs != 0 {
		config["siting_scout_demand_cooldown_secs"] = sc.ScoutDemandCooldownSecs
	}
}

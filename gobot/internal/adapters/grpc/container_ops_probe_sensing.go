package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// ProbeSensingCoordinator creates and starts the standing probe-sensing coordinator for a
// player — the fleet's one sensing engine (successor of the market-freshness sizer +
// frontier expansion pair). One coordinator per player reconciles the whitelist-scoped
// sensing footprint each tick: standing posts sized by market depth, the pressure-driven
// dormancy rotation, sweep-once discovery declares, and the budgeted probe buy behind the
// shared money-guard stack — while the scout-post reconciler does all movement, manning,
// and market partitioning. The container id is keyed by player so a restart re-adopts the
// same one; the persisted config is the recovery source (RULINGS #2), read back through the
// SAME buildCommandForType the creation path uses, so launch and recovery can never drift.
//
// The launch is all-defaults by design (RULINGS #5): every knob has a documented default in
// the coordinator and is operated live via `tune --operation sensing`, so the boot-standing
// launch carries only the container identity.
func (s *DaemonServer) ProbeSensingCoordinator(ctx context.Context, playerID int) (string, error) {
	containerID := utils.GenerateContainerID("probe_sensing_coordinator", fmt.Sprintf("player-%d", playerID))

	config := map[string]interface{}{
		"container_id": containerID,
	}

	// Re-adopt the last persisted live-tuned config for this player's sensing coordinator so
	// a relaunch of a stopped one keeps its tunes (matching the daemon-restart recovery path)
	// instead of silently reverting to the documented defaults.
	config, warnings, err := s.coordinatorStartConfig(ctx, playerID, config, probeSensingStartSpec())
	if err != nil {
		return "", fmt.Errorf("failed to resolve probe-sensing start config: %w", err)
	}
	printCoordinatorStartWarnings("sensing", playerID, warnings)

	cmd, err := s.buildCommandForType("probe_sensing_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeProbeSensingCoordinator,
		playerID,
		-1,  // Infinite iterations (reconcile loop)
		nil, // No parent container
		config,
		nil, // Use default RealClock for production
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "probe_sensing_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	s.startContainerRunner(containerEntity, cmd, containerID, "Container")

	return containerID, nil
}

// resolveSensingConfig makes config.yaml the single LIVE source of truth for the
// probe-sensing coordinator's string-valued knobs — today the goods whitelist,
// which the int-only tune mechanism cannot carry (the sp-ts82 live-config
// discipline, mirroring resolveScoutingConfig). It clears any persisted copy from
// a prior boot and re-injects the daemon's boot-loaded value, so the rebuilt
// command reflects the CURRENT config.yaml on every build — creation and restart
// recovery alike. An unset [sensing] section injects nothing, so the coordinator's
// era-goods default governs (RULINGS #5).
func (s *DaemonServer) resolveSensingConfig(config map[string]interface{}) {
	delete(config, "goods_whitelist")
	if s.sensingConfig.GoodsWhitelist != "" {
		config["goods_whitelist"] = s.sensingConfig.GoodsWhitelist
	}
}

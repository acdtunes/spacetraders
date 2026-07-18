package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// MarketFreshnessSizerCoordinator creates and starts the standing market-freshness
// auto-sizer for a player (sp-orgp), mirroring FrontierExpansionCoordinator. One
// coordinator per player MEASURES per-system freshness demand, sizes each market-bearing
// system's standing scout post to the SLA, and buys probes under the money guards — while
// the scout-post reconciler does all movement, manning, and market partitioning. The
// container id is keyed by player so a restart re-adopts the same one; the persisted config
// is the recovery source (RULINGS #2), read back through the SAME buildCommandForType the
// creation path uses, so launch and recovery can never drift.
//
// Every knob is parametrized (RULINGS #5); a 0/false value uses the coordinator's own
// documented default. dryRun logs decisions without buying or declaring.
func (s *DaemonServer) MarketFreshnessSizerCoordinator(
	ctx context.Context,
	playerID int,
	tickIntervalSecs int,
	dryRun bool,
	slaSeconds int,
	maxProbesPerSystem int,
	maxProbeFleet int,
	maxSpendPerCycle int,
	purchaseCooldownSecs int,
) (string, error) {
	containerID := utils.GenerateContainerID("market_freshness_sizer_coordinator", fmt.Sprintf("player-%d", playerID))

	config := map[string]interface{}{
		"container_id":           containerID,
		"tick_interval_secs":     tickIntervalSecs,
		"dry_run":                dryRun,
		"sla_seconds":            slaSeconds,
		"max_probes_per_system":  maxProbesPerSystem,
		"max_probe_fleet":        maxProbeFleet,
		"max_spend_per_cycle":    maxSpendPerCycle,
		"purchase_cooldown_secs": purchaseCooldownSecs,
	}

	cmd, err := s.buildCommandForType("market_freshness_sizer_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeMarketFreshnessSizer,
		playerID,
		-1,  // Infinite iterations (reconcile loop)
		nil, // No parent container
		config,
		nil, // Use default RealClock for production
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "market_freshness_sizer_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	s.startContainerRunner(containerEntity, cmd, containerID, "Container")

	return containerID, nil
}

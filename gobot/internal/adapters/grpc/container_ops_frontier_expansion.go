package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// FrontierExpansionCoordinator creates and starts the standing frontier expansion
// coordinator for a player (sp-8w89), mirroring ScoutPostCoordinator. One coordinator
// per player measures coverage demand, declares frontier sweep-once posts, and buys
// probes under the money guards — while the scout-post reconciler does all movement and
// manning. The container id is keyed by player so a restart re-adopts the same one; the
// persisted config is the recovery source (RULINGS #2), read back through the SAME
// buildCommandForType the creation path uses, so launch and recovery can never drift.
//
// Every knob is parametrized (RULINGS #5); a 0/false value uses the coordinator's own
// documented default. dryRun logs decisions without buying or declaring (pin #7).
func (s *DaemonServer) FrontierExpansionCoordinator(
	ctx context.Context,
	playerID int,
	tickIntervalSecs int,
	dryRun bool,
	maxProbeFleet int,
	maxSpendPerCycle int,
	purchaseCooldownSecs int,
	expansionMaxHops int,
) (string, error) {
	containerID := utils.GenerateContainerID("frontier_expansion_coordinator", fmt.Sprintf("player-%d", playerID))

	config := map[string]interface{}{
		"container_id":           containerID,
		"tick_interval_secs":     tickIntervalSecs,
		"dry_run":                dryRun,
		"max_probe_fleet":        maxProbeFleet,
		"max_spend_per_cycle":    maxSpendPerCycle,
		"purchase_cooldown_secs": purchaseCooldownSecs,
		"expansion_max_hops":     expansionMaxHops,
	}

	cmd, err := s.buildCommandForType("frontier_expansion_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeFrontierExpansion,
		playerID,
		-1,  // Infinite iterations (reconcile loop)
		nil, // No parent container
		config,
		nil, // Use default RealClock for production
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "frontier_expansion_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

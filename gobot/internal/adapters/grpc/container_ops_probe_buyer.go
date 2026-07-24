package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// ProbeBuyerFleetCoordinator creates and starts the standing probe-buyer-fleet coordinator for a
// player (sp-f082y), mirroring ScoutPostCoordinator. One coordinator per player maintains K dedicated
// buyer hulls; the boot-standing path (daemon_boot_standing.go) launches it, and a daemon restart
// re-adopts the persisted RUNNING one via RecoverRunningContainers (which rebuilds it through
// buildCommandForType, preserving any live-tuned probe_buyer_count / max_probe_fleet in its config).
// tickIntervalSecs is parametrized (RULINGS #5); 0 uses the coordinator's default. K and the cap
// resolve to the coordinator's documented defaults until tuned.
func (s *DaemonServer) ProbeBuyerFleetCoordinator(ctx context.Context, playerID int, tickIntervalSecs int) (string, error) {
	// Double-launch guard: ONE standing probe-buyer coordinator per player. GenerateContainerID mints a
	// fresh RANDOM id each call, so without this guard a second launch spawns a TWIN reconcile loop and
	// the two fight over the same buyers, recruits, and buys. Refuse loudly and name the live one. A
	// warm restart re-adopts the persisted RUNNING container through RecoverRunningContainers, never
	// this path, so recovery is unaffected.
	existingID, err := firstContainerIDOfType(ctx, s.containerRepo, playerID, container.ContainerTypeProbeBuyerCoordinator)
	if err != nil {
		return "", fmt.Errorf("failed to check for a running probe-buyer coordinator: %w", err)
	}
	if existingID != "" {
		return "", fmt.Errorf("probe-buyer coordinator already running for player %d (container %s) — stop it first: spacetraders container stop %s",
			playerID, existingID, existingID)
	}

	containerID := utils.GenerateContainerID("probe_buyer_coordinator", fmt.Sprintf("player-%d", playerID))

	config := map[string]interface{}{
		"container_id":       containerID,
		"tick_interval_secs": tickIntervalSecs,
	}

	cmd, err := s.buildCommandForType("probe_buyer_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeProbeBuyerCoordinator,
		playerID,
		-1,  // Infinite iterations (reconcile loop)
		nil, // No parent container
		config,
		nil, // Use default RealClock for production
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "probe_buyer_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	s.startContainerRunner(containerEntity, cmd, containerID, "Container")

	return containerID, nil
}

package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// ShipyardBackfillCoordinator creates and starts the standing shipyard-backfill sweep for a
// player (sp-s1ek — the launch verb for the sp-rhju engine), mirroring FrontierExpansionCoordinator.
// One coordinator per player each tick enumerates the charted-but-unscanned shipyard systems
// (the blind spots), ranks them deeper-first, and — bounded by the per-cycle dispatch cap and the
// idle-probe supply — declares a sweep-once scout post for the top targets; the scout-post
// reconciler does all movement and manning. The container id is keyed by player so a restart
// re-adopts the same one; the persisted config is the recovery source (RULINGS #2), read back
// through the SAME buildCommandForType (shipyard_backfill_coordinator →
// buildShipyardBackfillCoordinatorCommand) the creation path uses, so launch and recovery can
// never drift.
//
// DEPLOY-INERT (sp-s1ek): this coordinator is deliberately NOT a member of
// bootStandingCoordinatorTypes (contrast the market-freshness sizer, sp-orgp). Nothing launches
// it at boot; a fresh deploy changes nothing until an operator runs `spacetraders workflow
// shipyard-backfill`. Every knob is parametrized (RULINGS #5); a 0 value uses the coordinator's
// own documented default (max_dispatches_per_cycle is also live-tunable via `tune --operation
// shipyardbackfill`).
func (s *DaemonServer) ShipyardBackfillCoordinator(
	ctx context.Context,
	playerID int,
	tickIntervalSecs int,
	maxDispatchesPerCycle int,
) (string, error) {
	// Double-launch guard: ONE standing backfill sweep per player. A twin loop would
	// double-declare sweep-once posts against the same charted-but-unscanned set and
	// double-spend the idle-probe budget — refuse loudly, matching the guarded launches
	// elsewhere (container_ops_capacity_reconciler.go / container_ops_auto_outfit.go).
	existingID, err := firstContainerIDOfType(ctx, s.containerRepo, playerID, container.ContainerTypeShipyardBackfillCoordinator)
	if err != nil {
		return "", fmt.Errorf("failed to check for a running shipyard backfill coordinator: %w", err)
	}
	if existingID != "" {
		return "", fmt.Errorf("shipyard backfill coordinator already running for player %d (container %s) — stop it first: spacetraders container stop %s",
			playerID, existingID, existingID)
	}

	containerID := utils.GenerateContainerID("shipyard_backfill_coordinator", fmt.Sprintf("player-%d", playerID))

	config := map[string]interface{}{
		"container_id":             containerID,
		"tick_interval_secs":       tickIntervalSecs,
		"max_dispatches_per_cycle": maxDispatchesPerCycle,
	}

	cmd, err := s.buildCommandForType("shipyard_backfill_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create shipyard backfill command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeShipyardBackfillCoordinator,
		playerID,
		-1,  // Infinite iterations (reconcile loop) — NOT a CoordinatorOwnsIterations type
		nil, // No parent container
		config,
		nil, // Use default RealClock for production
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "shipyard_backfill_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist shipyard backfill container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Shipyard backfill container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

package grpc

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// ScoutPostCoordinator creates and starts the standing scout-post coordinator for
// a player (sp-cxpq), mirroring ContractFleetCoordinator. One coordinator per
// player reconciles the desired-state posts table; the container id is keyed by
// player so a restart re-adopts the same one. tickIntervalSecs is parametrized
// (RULINGS #5); 0 uses the coordinator's default.
func (s *DaemonServer) ScoutPostCoordinator(ctx context.Context, playerID int, tickIntervalSecs int) (string, error) {
	containerID := utils.GenerateContainerID("scout_post_coordinator", fmt.Sprintf("player-%d", playerID))

	config := map[string]interface{}{
		"container_id":       containerID,
		"tick_interval_secs": tickIntervalSecs,
	}

	cmd, err := s.buildCommandForType("scout_post_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeScoutPostCoordinator,
		playerID,
		-1,  // Infinite iterations (reconcile loop)
		nil, // No parent container
		config,
		nil, // Use default RealClock for production
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "scout_post_coordinator"); err != nil {
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

// AddScoutPost adds or updates a desired-state scout post for a system (sp-cxpq).
// The daemon is the single writer of post state (RULINGS #3); the captain's CLI
// reaches this only through the RPC. An existing post's live assignment is
// preserved — only the freshness target and kind are changed — so re-adding a
// manned post never evicts its hull.
func (s *DaemonServer) AddScoutPost(ctx context.Context, playerID int, systemSymbol string, freshness time.Duration, kind domainScouting.PostKind) (*domainScouting.ScoutPost, error) {
	if !kind.Valid() {
		return nil, fmt.Errorf("invalid post kind %q (want standing or sweep_once)", kind)
	}

	repo := persistence.NewGormScoutPostRepository(s.db)

	existing, err := repo.ListActive(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load existing posts: %w", err)
	}

	post := &domainScouting.ScoutPost{
		PlayerID:        playerID,
		SystemSymbol:    systemSymbol,
		FreshnessTarget: freshness,
		Kind:            kind,
		CreatedAt:       time.Now(),
	}
	// Preserve a live assignment when updating an existing post.
	for _, p := range existing {
		if p.SystemSymbol == systemSymbol {
			post.AssignedHull = p.AssignedHull
			post.TourContainerID = p.TourContainerID
			post.CreatedAt = p.CreatedAt
			break
		}
	}

	if err := repo.Upsert(ctx, post); err != nil {
		return nil, fmt.Errorf("failed to save scout post: %w", err)
	}
	return post, nil
}

// RemoveScoutPost deletes a scout post for a system and releases its hull if one
// is manning it, so the freed satellite flows to another post on the next
// reconcile tick.
func (s *DaemonServer) RemoveScoutPost(ctx context.Context, playerID int, systemSymbol string) error {
	repo := persistence.NewGormScoutPostRepository(s.db)

	posts, err := repo.ListActive(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to load posts: %w", err)
	}
	for _, p := range posts {
		if p.SystemSymbol == systemSymbol && p.AssignedHull != "" {
			s.releaseScoutHull(ctx, playerID, p.AssignedHull)
			break
		}
	}

	if err := repo.Remove(ctx, playerID, systemSymbol); err != nil {
		return fmt.Errorf("failed to remove scout post: %w", err)
	}
	return nil
}

// ListScoutPosts returns the active scout posts for a player.
func (s *DaemonServer) ListScoutPosts(ctx context.Context, playerID int) ([]*domainScouting.ScoutPost, error) {
	repo := persistence.NewGormScoutPostRepository(s.db)
	posts, err := repo.ListActive(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list scout posts: %w", err)
	}
	return posts, nil
}

// releaseScoutHull force-releases a hull assigned to a removed post so it returns
// to idle. Best-effort: a failure here only delays the satellite's reuse until the
// coordinator's own reclaim on the next tick, it does not strand the removal.
func (s *DaemonServer) releaseScoutHull(ctx context.Context, playerID int, hullSymbol string) {
	pid := shared.MustNewPlayerID(playerID)
	ship, err := s.shipRepo.FindBySymbol(ctx, hullSymbol, pid)
	if err != nil {
		fmt.Printf("Warning: failed to load hull %s for release: %v\n", hullSymbol, err)
		return
	}
	if !ship.IsAssigned() {
		return
	}
	ship.ForceRelease("scout_post_removed", s.clock)
	if err := s.shipRepo.Save(ctx, ship); err != nil {
		fmt.Printf("Warning: failed to release hull %s: %v\n", hullSymbol, err)
	}
}

package grpc

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// AddScoutPost is RETIRED (Admiral 2026-08-08). Declaring a post meant asking the standing
// scout-post reconciler to man a system with a circulating hull, and that reconciler is
// deleted — so a post written now would be desired state with nobody to satisfy it, which
// reads as a working CLI and is not one.
//
// It REFUSES rather than being unwired, because the wire surface outlives the engine: a
// captain reaching for the old verb gets the reason and the replacement, not a silent write.
// ListScoutPosts and RemoveScoutPost survive for the mirror reason — leftover rows outlive
// the code, and an operator must be able to see and clear them.
func (s *DaemonServer) AddScoutPost(ctx context.Context, playerID int, systemSymbol string, freshness time.Duration, kind domainScouting.PostKind, hulls int) (*domainScouting.ScoutPost, error) {
	return nil, fmt.Errorf("scout posts are retired: the standing reconciler that manned them was deleted, so declaring one for %s would create desired state nothing satisfies. Market tours now run only as an operator-started tour during the bootstrap phase (`spacetraders scout tour` / `scout markets`); steady-state freshness belongs to the parked-sensing coordinator, which parks a probe per market and needs no post", systemSymbol)
}

// RemoveScoutPost deletes a leftover scout post and releases any hull still recorded
// against it. Nothing declares posts any more, so this is the operator's cleanup verb for
// rows the retired reconciler left behind; the freed hull returns to the general pool,
// where parked sensing adopts it.
func (s *DaemonServer) RemoveScoutPost(ctx context.Context, playerID int, systemSymbol string) error {
	repo := persistence.NewGormScoutPostRepository(s.db)

	posts, err := repo.ListActive(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to load posts: %w", err)
	}
	for _, p := range posts {
		if p.SystemSymbol == systemSymbol {
			// Release EVERY slot's hull (a multi-probe post has more than one), so all
			// its satellites flow to other posts on the next reconcile tick.
			for _, hull := range p.MannedHulls() {
				s.releaseScoutHull(ctx, playerID, hull)
			}
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
	// Release under CAS-retry: the closure re-applies ForceRelease on the
	// FRESH row so a concurrent writer's cargo/nav update on the same hull survives
	// instead of being last-write-wins clobbered, and skips the write when the hull
	// is already idle (changed=false, no spurious version bump).
	if _, _, err := s.shipRepo.SaveWithRetry(ctx, hullSymbol, pid,
		func(sh *navigation.Ship) (bool, error) {
			if !sh.IsAssigned() {
				return false, nil
			}
			sh.ForceRelease("scout_post_removed", s.clock)
			return true, nil
		}); err != nil {
		fmt.Printf("Warning: failed to release hull %s: %v\n", hullSymbol, err)
	}
}

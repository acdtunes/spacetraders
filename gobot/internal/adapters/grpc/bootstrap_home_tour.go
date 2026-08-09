package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// bootstrapHomeTourStarter puts the cold-start probes on a market tour of the home system.
//
// It calls ScoutMarkets — the SAME entry point the captain's `scout markets` verb reaches.
// That is the whole design: one code path with two callers, so the automatic DATA-phase tour
// and an operator's tour are the same object, refused by the same bootstrap-phase gate and
// stopped by the same graduation sweep. Nothing is declared, nothing is persisted as desired
// state, and no engine mans anything.
type bootstrapHomeTourStarter struct{ server *DaemonServer }

// StartHomeMarketTour flies the home probe fleet over the home system's marketplaces, returning
// how many hulls it put on tour.
//
// Returning (0, nil) is a first-class answer, not a failure: a fleet whose probes are all
// busy, or a home system with no known marketplace yet, has nothing to start this tick and
// bootstrap simply re-observes on the next one. Only a genuine inability to read the fleet or
// to start the tour is an error, and the caller retries that too.
func (b *bootstrapHomeTourStarter) StartHomeMarketTour(ctx context.Context, playerID int, homeSystem string) (int, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}

	ships, err := b.server.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, fmt.Errorf("read fleet: %w", err)
	}
	ourTours, err := liveScoutTourContainerIDs(ctx, b.server.containerRepo, playerID)
	if err != nil {
		return 0, fmt.Errorf("read live scout tours: %w", err)
	}
	probes := selectHomeTourHulls(ships, homeSystem, ourTours)
	if len(probes) == 0 {
		return 0, nil
	}

	waypoints, err := b.server.waypointRepo.ListBySystemWithTrait(ctx, homeSystem, marketplaceTrait)
	if err != nil {
		return 0, fmt.Errorf("list %s marketplaces: %w", homeSystem, err)
	}
	markets := make([]string, 0, len(waypoints))
	for _, wp := range waypoints {
		if wp != nil {
			markets = append(markets, wp.Symbol)
		}
	}
	if len(markets) == 0 {
		return 0, nil // the home system is not charted yet; the next tick re-reads
	}

	// Iterations -1: the tour re-flies its circuit until something stops it. What stops it is
	// the graduation sweep, which is the point — a finite count would end the tour early and
	// leave market data going stale for the rest of cold start with nothing to notice.
	started, _, _, err := b.server.ScoutMarkets(ctx, probes, homeSystem, markets, -1, playerID)
	if err != nil {
		return 0, err
	}
	return len(started), nil
}

// selectHomeTourHulls picks the hulls this tour may partition the home markets across: every
// probe AT HOME that is either idle or already flying one of OUR OWN tours.
//
// THE HULLS ALREADY FLYING ARE THE POINT. ScoutMarkets re-partitions exactly the hulls it is
// handed, so naming only the idle probes would leave the incumbent flying the whole market set
// beside a partition of that same set across the newcomers — overlapping copies of one tour
// instead of disjoint circuits. A probe held by ANYTHING ELSE is somebody's and is left alone
// (RULINGS #7); a captain reservation carries no container id, so it matches no live tour. So
// is one parked in another system, which this tour may not order travel for.
func selectHomeTourHulls(ships []*navigation.Ship, homeSystem string, ourTours map[string]bool) []string {
	var probes []string
	for _, ship := range ships {
		if ship == nil || !ship.IsScoutType() {
			continue
		}
		if !ship.IsIdle() && !ourTours[ship.ContainerID()] {
			continue
		}
		if loc := ship.CurrentLocation(); loc == nil || shared.ExtractSystemSymbol(loc.Symbol) != homeSystem {
			continue
		}
		probes = append(probes, ship.ShipSymbol())
	}
	return probes
}

// liveScoutTourContainerIDs returns the IDs of the player's RUNNING-or-PENDING market tours —
// the claims a re-cut may take back because they are its own. One query over both live statuses,
// so a row transitioning between them cannot fall through the gap and read as a stranger's.
func liveScoutTourContainerIDs(ctx context.Context, repo *persistence.ContainerRepositoryGORM, playerID int) (map[string]bool, error) {
	if repo == nil {
		return nil, nil
	}
	summaries, err := repo.ListActiveByTypeSimple(ctx, playerID, string(container.ContainerTypeScout))
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(summaries))
	for _, s := range summaries {
		ids[s.ID] = true
	}
	return ids, nil
}
